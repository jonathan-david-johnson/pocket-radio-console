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
)

// defaultSkip is the seek step until M3 wires synced skip settings.
const defaultSkip = 45 * time.Second

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

// Engine orchestrates auth, the Pocket Casts API, and the Player.
type Engine struct {
	api    pocketcasts.PocketCasts
	player player.Player
	auth   Auth

	mu  sync.RWMutex
	now NowPlaying

	subs []chan NowPlaying
}

// New builds an Engine from its boundary dependencies.
func New(api pocketcasts.PocketCasts, p player.Player, auth Auth) *Engine {
	e := &Engine{api: api, player: p, auth: auth}
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
	switch t.(type) {
	case UpNextTop:
		return e.playUpNextTop(ctx)
	default:
		return errors.New("unsupported target")
	}
}

func (e *Engine) playUpNextTop(ctx context.Context) error {
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

// SkipForward seeks forward by defaultSkip.
func (e *Engine) SkipForward() {
	pos := e.player.State().Position
	_ = e.player.Seek(pos + defaultSkip)
}

// SkipBack seeks backward by defaultSkip, clamped at zero.
func (e *Engine) SkipBack() {
	pos := e.player.State().Position - defaultSkip
	if pos < 0 {
		pos = 0
	}
	_ = e.player.Seek(pos)
}

// Scrub seeks by a relative delta.
func (e *Engine) Scrub(d time.Duration) {
	pos := e.player.State().Position + d
	if pos < 0 {
		pos = 0
	}
	_ = e.player.Seek(pos)
}

// Close shuts down the player.
func (e *Engine) Close() error { return e.player.Close() }

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
