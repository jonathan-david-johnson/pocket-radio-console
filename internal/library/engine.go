// Package library is THE ENGINE: the non-UI orchestration core. It owns the
// Source, drives the Player, and emits NowPlaying state both UIs render. The Go
// equivalent of the menubar's PlayerViewModel. M1 surface only.
package library

import (
	"context"
	"errors"
	"sync"
	"time"

	"pocket-radio-console/internal/player"
	"pocket-radio-console/internal/pocketcasts"
	"pocket-radio-console/internal/radio"
)

// defaultSkip is the seek step until M3 wires synced skip settings.
const defaultSkip = 45 * time.Second

// tracklistInterval is how often the engine re-polls a station's tracklist.
const tracklistInterval = 30 * time.Second

// NowPlaying is the single state struct both UIs render.
type NowPlaying struct {
	Title    string // episode title or "Song — Artist"
	Subtitle string // "Up Next" | "Live Stream" | podcast/station name
	Playing  bool
	Position time.Duration
	Duration time.Duration
	IsLive   bool
}

// Target is what a play command resolves to before it becomes the Source.
type Target interface{ isTarget() }

// UpNextTop plays the top of the Up Next queue.
type UpNextTop struct{}

func (UpNextTop) isTarget() {}

// NewestRelease plays the newest release (playback lands in M5).
type NewestRelease struct{}

func (NewestRelease) isTarget() {}

// PlayStation plays a resolved radio station.
type PlayStation struct{ Station radio.Station }

func (PlayStation) isTarget() {}

// Auth abstracts the credential/token resolution the engine needs. Tests inject
// a fake; production uses the config.Store-backed implementation below.
type Auth interface {
	// Token returns a cached token if present.
	Token() (token string, ok bool)
	// DeviceID returns the stable device id.
	DeviceID() string
	// Relogin authenticates from stored creds and caches the new token.
	Relogin(ctx context.Context) (token string, err error)
}

// Engine orchestrates auth, the Pocket Casts API, the radio surface, and the Player.
type Engine struct {
	api         pocketcasts.PocketCasts
	player      player.Player
	auth        Auth
	tracklister radio.Tracklister

	mu       sync.RWMutex
	now      NowPlaying
	tlCancel context.CancelFunc // stops the running tracklist poller

	subs []chan NowPlaying
}

// Option configures an Engine at construction.
type Option func(*Engine)

// WithTracklister enables station tracklist polling.
func WithTracklister(t radio.Tracklister) Option {
	return func(e *Engine) { e.tracklister = t }
}

// New builds an Engine from its boundary dependencies.
func New(api pocketcasts.PocketCasts, p player.Player, auth Auth, opts ...Option) *Engine {
	e := &Engine{api: api, player: p, auth: auth}
	for _, opt := range opts {
		opt(e)
	}
	go e.pump()
	return e
}

// Subscribe returns a channel that receives NowPlaying on every state change.
func (e *Engine) Subscribe() <-chan NowPlaying {
	ch := make(chan NowPlaying, 8)
	e.mu.Lock()
	e.subs = append(e.subs, ch)
	cur := e.now
	e.mu.Unlock()
	ch <- cur
	return ch
}

// State returns the current NowPlaying snapshot.
func (e *Engine) State() NowPlaying {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.now
}

// PlayTarget resolves t to a Source and starts playback.
func (e *Engine) PlayTarget(ctx context.Context, t Target) error {
	switch tt := t.(type) {
	case UpNextTop:
		return e.playUpNextTop(ctx)
	case PlayStation:
		return e.playStation(tt.Station)
	case NewestRelease:
		return errors.New("New Releases playback lands in M5")
	default:
		return errors.New("unsupported target")
	}
}

// playStation loads a radio stream and starts tracklist polling if supported.
func (e *Engine) playStation(st radio.Station) error {
	e.stopTracklist()
	if st.StreamURL == "" {
		return errors.New("station has no stream URL")
	}
	if err := e.player.Load(st.StreamURL, 0); err != nil {
		return err
	}
	e.update(func(n *NowPlaying) {
		n.Title = st.Name
		n.Subtitle = st.Name
		n.Playing = true
		n.Position = 0
		n.Duration = 0
		n.IsLive = true // refined by mpv duration on the first tick
	})
	e.startTracklist(st)
	return nil
}

// startTracklist polls the station's tracklist on an interval, setting the
// now-playing title to the top track ("Song — Artist").
func (e *Engine) startTracklist(st radio.Station) {
	if e.tracklister == nil || !e.tracklister.HasTracklist(st) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.tlCancel = cancel
	e.mu.Unlock()

	go func() {
		e.pollTracklistOnce(ctx, st)
		ticker := time.NewTicker(tracklistInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.pollTracklistOnce(ctx, st)
			}
		}
	}()
}

func (e *Engine) pollTracklistOnce(ctx context.Context, st radio.Station) {
	tracks, err := e.tracklister.Tracklist(ctx, st)
	if err != nil || ctx.Err() != nil {
		return
	}
	e.update(func(n *NowPlaying) {
		if len(tracks) > 0 {
			n.Title = tracks[0].Label()
		} else {
			n.Title = st.Name // fall back to the station name
		}
	})
}

func (e *Engine) stopTracklist() {
	e.mu.Lock()
	cancel := e.tlCancel
	e.tlCancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) playUpNextTop(ctx context.Context) error {
	e.stopTracklist()
	episodes, err := e.withAuth(ctx, func(token string) ([]pocketcasts.Episode, error) {
		return e.api.UpNext(ctx, token, e.auth.DeviceID())
	})
	if err != nil {
		return err
	}
	if len(episodes) == 0 {
		return errors.New("Up Next is empty")
	}
	ep := episodes[0]
	if ep.URL == "" {
		return errors.New("top episode has no playable URL")
	}
	// M1 starts at 0; podcast resume (playedUpTo) lands in M3.
	if err := e.player.Load(ep.URL, 0); err != nil {
		return err
	}
	e.update(func(n *NowPlaying) {
		n.Title = ep.Title
		n.Subtitle = "Up Next"
		n.Playing = true
		n.Position = 0
		n.Duration = time.Duration(ep.Duration) * time.Second
		n.IsLive = false
	})
	return nil
}

// withAuth runs fn with a token, re-logging-in once on ErrInvalidCredentials.
func (e *Engine) withAuth(ctx context.Context, fn func(token string) ([]pocketcasts.Episode, error)) ([]pocketcasts.Episode, error) {
	token, ok := e.auth.Token()
	if ok {
		eps, err := fn(token)
		if !errors.Is(err, pocketcasts.ErrInvalidCredentials) {
			return eps, err
		}
		// token rejected — fall through to relogin
	}
	newToken, err := e.auth.Relogin(ctx)
	if err != nil {
		return nil, err
	}
	return fn(newToken)
}

// TogglePlayback flips play/pause.
func (e *Engine) TogglePlayback() {
	if e.player.State().Playing {
		_ = e.player.Pause()
		e.update(func(n *NowPlaying) { n.Playing = false })
	} else {
		_ = e.player.Resume()
		e.update(func(n *NowPlaying) { n.Playing = true })
	}
}

// isLive reports whether the current source is a live stream (no seeking).
func (e *Engine) isLive() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.now.IsLive
}

// SkipForward seeks forward by defaultSkip. No-op on live streams.
func (e *Engine) SkipForward() {
	if e.isLive() {
		return
	}
	pos := e.player.State().Position
	_ = e.player.Seek(pos + defaultSkip)
}

// SkipBack seeks backward by defaultSkip, clamped at zero. No-op on live streams.
func (e *Engine) SkipBack() {
	if e.isLive() {
		return
	}
	pos := e.player.State().Position - defaultSkip
	if pos < 0 {
		pos = 0
	}
	_ = e.player.Seek(pos)
}

// Scrub seeks by a relative delta. No-op on live streams.
func (e *Engine) Scrub(d time.Duration) {
	if e.isLive() {
		return
	}
	pos := e.player.State().Position + d
	if pos < 0 {
		pos = 0
	}
	_ = e.player.Seek(pos)
}

// Close stops the tracklist poller and shuts down the player.
func (e *Engine) Close() error {
	e.stopTracklist()
	return e.player.Close()
}

// pump fans player events into NowPlaying updates.
func (e *Engine) pump() {
	for ev := range e.player.Events() {
		switch ev.Kind {
		case player.Tick:
			st := e.player.State()
			e.update(func(n *NowPlaying) {
				n.Position = st.Position
				if st.Duration > 0 {
					n.Duration = st.Duration
				}
				n.Playing = st.Playing
				n.IsLive = st.IsLive
			})
		case player.Metadata:
			if title := ev.Metadata["icy-title"]; title != "" {
				e.update(func(n *NowPlaying) { n.Title = title })
			}
		case player.Ended:
			e.update(func(n *NowPlaying) { n.Playing = false })
		}
	}
}

// update mutates NowPlaying under lock and broadcasts to subscribers.
func (e *Engine) update(fn func(n *NowPlaying)) {
	e.mu.Lock()
	fn(&e.now)
	snapshot := e.now
	subs := append([]chan NowPlaying(nil), e.subs...)
	e.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- snapshot:
		default: // drop if subscriber is slow
		}
	}
}
