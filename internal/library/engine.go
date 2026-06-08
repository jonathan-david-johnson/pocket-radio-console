// Package library is THE ENGINE: the non-UI orchestration core. It owns the
// Source, drives the Player, and emits NowPlaying state both UIs render. The Go
// equivalent of the menubar's PlayerViewModel.
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

// saveInterval throttles position write-back: at most one save this often.
const saveInterval = 30 * time.Second

// completeThreshold: pausing/finishing with this little remaining counts as done.
const completeThreshold = 10 * time.Second

// tracklistInterval is how often the engine re-polls a station's tracklist.
const tracklistInterval = 30 * time.Second

// defaultSkip is used until the user's synced skip amounts are fetched.
var defaultSkip = pocketcasts.Skip{Back: 10, Forward: 45}

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

	// pmu guards the podcast playback state below.
	pmu         sync.Mutex
	queue       []pocketcasts.Episode
	idx         int
	current     pocketcasts.Episode
	hasCurrent  bool
	lastSaveAt  time.Time
	lastSavePos time.Duration
	skip        pocketcasts.Skip
	infoCache   map[string]map[string]pocketcasts.PlaybackInfo // podcastUUID → uuid → info
	clock       func() time.Time
}

// Option configures an Engine at construction.
type Option func(*Engine)

// WithTracklister enables station tracklist polling.
func WithTracklister(t radio.Tracklister) Option {
	return func(e *Engine) { e.tracklister = t }
}

// WithClock injects a clock for deterministic throttle tests.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.clock = now }
}

// New builds an Engine from its boundary dependencies.
func New(api pocketcasts.PocketCasts, p player.Player, auth Auth, opts ...Option) *Engine {
	e := &Engine{
		api:       api,
		player:    p,
		auth:      auth,
		skip:      defaultSkip,
		infoCache: map[string]map[string]pocketcasts.PlaybackInfo{},
		clock:     time.Now,
	}
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

// ---- Podcast playback (Up Next) ----

func (e *Engine) playUpNextTop(ctx context.Context) error {
	e.stopTracklist()
	episodes, err := e.fetchUpNext(ctx)
	if err != nil {
		return err
	}
	if len(episodes) == 0 {
		return errors.New("Up Next is empty")
	}

	e.pmu.Lock()
	e.queue = episodes
	e.idx = 0
	e.pmu.Unlock()

	go e.refreshSkipSettings(context.Background())
	return e.playEpisodeAt(ctx, 0)
}

// playEpisodeAt resumes the episode at index i at its saved position.
func (e *Engine) playEpisodeAt(ctx context.Context, i int) error {
	e.pmu.Lock()
	if i < 0 || i >= len(e.queue) {
		e.pmu.Unlock()
		return errors.New("episode index out of range")
	}
	ep := e.queue[i]
	e.pmu.Unlock()

	if ep.URL == "" {
		return errors.New("episode has no playable URL")
	}

	// The real progress lives in /user/podcast/episodes, not up_next/sync.
	if info, ok := e.playbackInfoFor(ctx, ep); ok {
		if info.PlayedUpTo > 0 {
			ep.PlayedUpTo = info.PlayedUpTo
		}
		if info.Duration > 0 {
			ep.Duration = info.Duration
		}
	}

	resume := time.Duration(ep.PlayedUpTo) * time.Second
	if err := e.player.Load(ep.URL, resume); err != nil {
		return err
	}

	e.pmu.Lock()
	e.idx = i
	e.current = ep
	e.hasCurrent = true
	e.lastSavePos = resume
	e.lastSaveAt = e.clock()
	e.pmu.Unlock()

	e.update(func(n *NowPlaying) {
		n.Title = ep.Title
		n.Subtitle = "Up Next"
		n.Playing = true
		n.Position = resume
		n.Duration = time.Duration(ep.Duration) * time.Second
		n.IsLive = false
	})
	return nil
}

// playbackInfoFor returns merged progress for an episode, caching per podcast.
func (e *Engine) playbackInfoFor(ctx context.Context, ep pocketcasts.Episode) (pocketcasts.PlaybackInfo, bool) {
	if ep.PodcastUUID == "" {
		return pocketcasts.PlaybackInfo{}, false
	}
	e.pmu.Lock()
	cached, ok := e.infoCache[ep.PodcastUUID]
	e.pmu.Unlock()

	if !ok {
		var infos []pocketcasts.PlaybackInfo
		err := e.withToken(ctx, func(token string) error {
			var err error
			infos, err = e.api.PodcastEpisodes(ctx, token, ep.PodcastUUID)
			return err
		})
		cached = map[string]pocketcasts.PlaybackInfo{}
		if err == nil {
			for _, in := range infos {
				cached[in.UUID] = in
			}
		}
		e.pmu.Lock()
		e.infoCache[ep.PodcastUUID] = cached
		e.pmu.Unlock()
	}

	info, found := cached[ep.UUID]
	return info, found
}

func (e *Engine) refreshSkipSettings(ctx context.Context) {
	var s pocketcasts.Skip
	err := e.withToken(ctx, func(token string) error {
		var err error
		s, err = e.api.SkipSettings(ctx, token)
		return err
	})
	if err != nil || (s.Back == 0 && s.Forward == 0) {
		return
	}
	e.pmu.Lock()
	e.skip = s
	e.pmu.Unlock()
}

// ---- Write-back, finish, advance ----

// maybeSave writes the current position back, throttled to saveInterval unless
// force is set. No-op for live streams or when nothing is playing.
func (e *Engine) maybeSave(force bool, status pocketcasts.EpisodeStatus) {
	if e.isLive() {
		return
	}
	pos := e.player.State().Position
	now := e.clock()

	e.pmu.Lock()
	if !e.hasCurrent {
		e.pmu.Unlock()
		return
	}
	if !force && (now.Sub(e.lastSaveAt) < saveInterval || pos == e.lastSavePos) {
		e.pmu.Unlock()
		return
	}
	cur := e.current
	e.lastSaveAt = now
	e.lastSavePos = pos
	e.pmu.Unlock()

	go e.saveProgress(context.Background(), cur, pos, status)
}

func (e *Engine) saveProgress(ctx context.Context, ep pocketcasts.Episode, pos time.Duration, status pocketcasts.EpisodeStatus) {
	dur := ep.Duration
	if dur == 0 {
		dur = int(e.player.State().Duration.Seconds())
	}
	_ = e.withToken(ctx, func(token string) error {
		return e.api.UpdateEpisode(ctx, token, pocketcasts.EpisodeUpdate{
			UUID:        ep.UUID,
			PodcastUUID: ep.PodcastUUID,
			Position:    int(pos.Seconds()),
			Duration:    dur,
			Status:      status,
		})
	})
}

// completeAndAdvance marks the current episode completed, removes it from Up
// Next, and auto-plays the next episode.
func (e *Engine) completeAndAdvance() {
	e.pmu.Lock()
	if !e.hasCurrent {
		e.pmu.Unlock()
		return
	}
	cur := e.current
	next := e.idx + 1
	e.hasCurrent = false
	e.pmu.Unlock()

	dur := time.Duration(cur.Duration) * time.Second
	e.saveProgress(context.Background(), cur, dur, pocketcasts.StatusCompleted)
	_ = e.withToken(context.Background(), func(token string) error {
		return e.api.RemoveFromUpNext(context.Background(), token, e.auth.DeviceID(), cur)
	})

	e.pmu.Lock()
	hasNext := next < len(e.queue)
	e.pmu.Unlock()
	if hasNext {
		_ = e.playEpisodeAt(context.Background(), next)
		return
	}
	e.update(func(n *NowPlaying) { n.Playing = false })
}

// ---- Radio (station) playback ----

func (e *Engine) playStation(st radio.Station) error {
	e.stopTracklist()
	e.pmu.Lock()
	e.hasCurrent = false // leaving podcast context; no write-back for stations
	e.pmu.Unlock()
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

// ---- Token helpers ----

func (e *Engine) fetchUpNext(ctx context.Context) ([]pocketcasts.Episode, error) {
	var eps []pocketcasts.Episode
	err := e.withToken(ctx, func(token string) error {
		var err error
		eps, err = e.api.UpNext(ctx, token, e.auth.DeviceID())
		return err
	})
	return eps, err
}

// withToken runs fn with a token, re-logging-in once on ErrInvalidCredentials.
func (e *Engine) withToken(ctx context.Context, fn func(token string) error) error {
	if token, ok := e.auth.Token(); ok {
		if err := fn(token); !errors.Is(err, pocketcasts.ErrInvalidCredentials) {
			return err
		}
	}
	newToken, err := e.auth.Relogin(ctx)
	if err != nil {
		return err
	}
	return fn(newToken)
}

// ---- Transport ----

// TogglePlayback flips play/pause, saving progress (or completing) on pause.
func (e *Engine) TogglePlayback() {
	if !e.player.State().Playing {
		_ = e.player.Resume()
		e.update(func(n *NowPlaying) { n.Playing = true })
		return
	}

	_ = e.player.Pause()
	e.update(func(n *NowPlaying) { n.Playing = false })

	if e.isLive() {
		return
	}
	st := e.player.State()
	e.pmu.Lock()
	has := e.hasCurrent
	dur := time.Duration(e.current.Duration) * time.Second
	e.pmu.Unlock()
	if !has {
		return
	}
	if dur <= 0 {
		dur = st.Duration
	}
	if dur > 0 && dur-st.Position <= completeThreshold {
		go e.completeAndAdvance()
		return
	}
	e.maybeSave(true, pocketcasts.StatusInProgress)
}

func (e *Engine) isLive() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.now.IsLive
}

func (e *Engine) skipAmounts() (back, forward time.Duration) {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	return time.Duration(e.skip.Back) * time.Second, time.Duration(e.skip.Forward) * time.Second
}

// SkipForward seeks forward by the synced skip amount. No-op on live streams.
func (e *Engine) SkipForward() {
	if e.isLive() {
		return
	}
	_, forward := e.skipAmounts()
	_ = e.player.Seek(e.player.State().Position + forward)
}

// SkipBack seeks backward by the synced skip amount, clamped at zero.
func (e *Engine) SkipBack() {
	if e.isLive() {
		return
	}
	back, _ := e.skipAmounts()
	pos := e.player.State().Position - back
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

// ---- Event pump ----

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
			if st.Playing {
				e.maybeSave(false, pocketcasts.StatusInProgress)
			}
		case player.Metadata:
			if title := ev.Metadata["icy-title"]; title != "" {
				e.update(func(n *NowPlaying) { n.Title = title })
			}
		case player.Ended:
			e.update(func(n *NowPlaying) { n.Playing = false })
			if !e.isLive() {
				e.completeAndAdvance()
			}
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
