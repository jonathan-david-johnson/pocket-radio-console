// Package library is THE ENGINE: the non-UI orchestration core. It owns the
// Source, drives the Player, and emits NowPlaying state both UIs render. The Go
// equivalent of the menubar's PlayerViewModel.
package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	ArtURL   string // album art or station logo URL (empty if none)
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

// UpNextAt plays the Up Next episode at the given queue index.
type UpNextAt struct{ Index int }

func (UpNextAt) isTarget() {}

// PlayRelease plays a specific New Release episode.
type PlayRelease struct{ Release pocketcasts.NewRelease }

func (PlayRelease) isTarget() {}

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

	// playMu serializes PlayTarget calls so rapid source-switching never races
	// on player.Load or engine state. playCancel cancels the in-flight load.
	playMu     sync.Mutex
	playCancel context.CancelFunc

	// pmu guards the podcast playback state below.
	pmu           sync.Mutex
	queue         []pocketcasts.Episode
	idx           int
	current       pocketcasts.Episode
	hasCurrent    bool
	lastSaveAt    time.Time
	lastSavePos   time.Duration
	skip          pocketcasts.Skip
	infoCache     map[string]map[string]pocketcasts.PlaybackInfo // podcastUUID → uuid → info
	clock         func() time.Time
	currentTarget Target // last target passed to PlayTarget
	stagedTarget  Target // pending selection (nil = none)
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
// Concurrent calls are serialized: a newer call cancels the in-flight one and
// waits for it to finish before proceeding.
func (e *Engine) PlayTarget(ctx context.Context, t Target) error {
	slog.Debug("PlayTarget.request", "target", fmt.Sprintf("%T", t))

	// Register this call as the intended target and cancel any in-flight load.
	e.pmu.Lock()
	e.currentTarget = t
	e.stagedTarget = nil
	if e.playCancel != nil {
		slog.Debug("PlayTarget.cancelling_previous")
		e.playCancel()
	}
	playCtx, cancel := context.WithCancel(ctx)
	e.playCancel = cancel
	e.pmu.Unlock()

	// Serialize: wait for any prior PlayTarget to finish.
	e.playMu.Lock()
	defer e.playMu.Unlock()

	// If a newer call already superseded us, bail out.
	e.pmu.Lock()
	stillCurrent := e.currentTarget == t
	e.pmu.Unlock()
	if !stillCurrent {
		slog.Debug("PlayTarget.superseded", "target", fmt.Sprintf("%T", t))
		cancel()
		return nil
	}

	slog.Info("PlayTarget.start", "target", fmt.Sprintf("%T", t))
	switch tt := t.(type) {
	case UpNextTop:
		err := e.playUpNextTop(playCtx)
		if err != nil {
			slog.Error("PlayTarget.upnext_failed", "err", err)
		}
		return err
	case PlayStation:
		err := e.playStation(tt.Station)
		if err != nil {
			slog.Error("PlayTarget.station_failed", "station", tt.Station.Name, "err", err)
		}
		return err
	case UpNextAt:
		err := e.playUpNextAt(playCtx, tt.Index)
		if err != nil {
			slog.Error("PlayTarget.upnext_at_failed", "index", tt.Index, "err", err)
		}
		return err
	case NewestRelease:
		err := e.playNewestRelease(playCtx)
		if err != nil {
			slog.Error("PlayTarget.newest_release_failed", "err", err)
		}
		return err
	case PlayRelease:
		err := e.playRelease(playCtx, tt.Release)
		if err != nil {
			slog.Error("PlayTarget.release_failed", "title", tt.Release.Title, "err", err)
		}
		return err
	default:
		cancel()
		return errors.New("unsupported target")
	}
}

// StageTarget marks t as the next source to play without starting playback.
// A subsequent TogglePlayback will switch to it when it differs from the current source.
func (e *Engine) StageTarget(t Target) {
	slog.Debug("StageTarget", "target", fmt.Sprintf("%T", t))
	e.pmu.Lock()
	e.stagedTarget = t
	e.pmu.Unlock()
}

// StagedTarget returns the currently staged target, or nil if none.
func (e *Engine) StagedTarget() Target {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	return e.stagedTarget
}

// CurrentTarget returns the target that is currently loaded/playing, or nil if
// nothing has been played yet.
func (e *Engine) CurrentTarget() Target {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	return e.currentTarget
}

// ---- Podcast playback (Up Next) ----

// UpNextList fetches the current Up Next queue without changing playback. The
// full TUI uses it to render the Up Next tab.
func (e *Engine) UpNextList(ctx context.Context) ([]pocketcasts.Episode, error) {
	return e.fetchUpNext(ctx)
}

// playUpNextAt fetches Up Next and starts playback at queue index i.
func (e *Engine) playUpNextAt(ctx context.Context, i int) error {
	e.stopTracklist()
	episodes, err := e.fetchUpNext(ctx)
	if err != nil {
		return err
	}
	if i < 0 || i >= len(episodes) {
		return errors.New("up next index out of range")
	}

	e.pmu.Lock()
	e.queue = episodes
	e.idx = i
	e.pmu.Unlock()

	go e.refreshSkipSettings(context.Background())
	return e.playEpisodeAt(ctx, i)
}

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
	slog.Info("playEpisodeAt", "index", i, "title", ep.Title, "played_up_to", ep.PlayedUpTo, "duration", ep.Duration)

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
		n.ArtURL = podcastArtURL(ep.PodcastUUID)
		n.Playing = true
		n.Position = resume
		n.Duration = time.Duration(ep.Duration) * time.Second
		n.IsLive = false
	})
	return nil
}

// newReleaseDays is the look-back window for the New Releases list.
const newReleaseDays = 14

// playNewestRelease fetches the New Releases list, plays the newest episode, and
// bubbles it to the server-side Up Next via PlayNow.
// NewReleases fetches the New Releases list without changing playback. The full
// TUI uses it to render the New Releases tab.
func (e *Engine) NewReleases(ctx context.Context) ([]pocketcasts.NewRelease, error) {
	var rel []pocketcasts.NewRelease
	err := e.withToken(ctx, func(token string) error {
		var err error
		rel, err = e.api.NewReleases(ctx, token, newReleaseDays)
		return err
	})
	return rel, err
}

func (e *Engine) playNewestRelease(ctx context.Context) error {
	releases, err := e.NewReleases(ctx)
	if err != nil {
		return err
	}
	if len(releases) == 0 {
		return errors.New("no new releases")
	}
	return e.playRelease(ctx, releases[0])
}

// playRelease plays a single New Release and bubbles it to the server-side Up
// Next via PlayNow.
func (e *Engine) playRelease(ctx context.Context, r pocketcasts.NewRelease) error {
	e.stopTracklist()
	ep := pocketcasts.Episode{
		UUID:        r.UUID,
		Title:       r.Title,
		URL:         r.URL,
		PodcastUUID: r.PodcastUUID,
		Duration:    r.Duration,
		Published:   r.Published,
	}

	// Bubble the release into the server-side Up Next queue.
	go func() {
		_ = e.withToken(context.Background(), func(token string) error {
			return e.api.PlayNow(context.Background(), token, e.auth.DeviceID(), ep)
		})
	}()

	e.pmu.Lock()
	e.queue = []pocketcasts.Episode{ep}
	e.idx = 0
	e.pmu.Unlock()

	go e.refreshSkipSettings(context.Background())
	if err := e.playEpisodeAt(ctx, 0); err != nil {
		return err
	}
	// New Releases label the podcast rather than the queue.
	if r.PodcastTitle != "" {
		e.update(func(n *NowPlaying) { n.Subtitle = r.PodcastTitle })
	}
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
		slog.Debug("completeAndAdvance.no_current_skipped")
		e.pmu.Unlock()
		return
	}
	cur := e.current
	next := e.idx + 1
	e.hasCurrent = false
	e.pmu.Unlock()

	slog.Info("completeAndAdvance", "completed", cur.Title, "next_index", next)
	dur := time.Duration(cur.Duration) * time.Second
	e.saveProgress(context.Background(), cur, dur, pocketcasts.StatusCompleted)
	_ = e.withToken(context.Background(), func(token string) error {
		return e.api.RemoveFromUpNext(context.Background(), token, e.auth.DeviceID(), cur)
	})

	e.pmu.Lock()
	hasNext := next < len(e.queue)
	e.pmu.Unlock()
	if hasNext {
		slog.Info("completeAndAdvance.advancing", "next_index", next)
		_ = e.playEpisodeAt(context.Background(), next)
		return
	}
	slog.Info("completeAndAdvance.queue_empty")
	e.update(func(n *NowPlaying) { n.Playing = false })
}

// ---- Radio (station) playback ----

func (e *Engine) playStation(st radio.Station) error {
	slog.Info("playStation", "name", st.Name, "url", st.StreamURL, "logo", st.LogoURL)
	e.stopTracklist()
	e.pmu.Lock()
	e.hasCurrent = false // leaving podcast context; no write-back for stations
	e.pmu.Unlock()
	if st.StreamURL == "" {
		return errors.New("station has no stream URL")
	}
	slog.Debug("playStation.load", "url", st.StreamURL)
	if err := e.player.Load(st.StreamURL, 0); err != nil {
		return err
	}
	e.update(func(n *NowPlaying) {
		n.Title = st.Name
		n.Subtitle = st.Name
		n.ArtURL = st.LogoURL
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

// TogglePlayback flips play/pause. If a staged target different from the current
// source is set, switches to that source instead.
func (e *Engine) TogglePlayback() {
	e.pmu.Lock()
	staged := e.stagedTarget
	current := e.currentTarget
	e.pmu.Unlock()

	if staged != nil && staged != current {
		slog.Info("TogglePlayback.switch_source",
			"from", fmt.Sprintf("%T", current),
			"to", fmt.Sprintf("%T", staged))
		e.pmu.Lock()
		e.stagedTarget = nil
		e.pmu.Unlock()
		go func() { _ = e.PlayTarget(context.Background(), staged) }()
		return
	}

	// Use engine's intent state, not raw mpv state. mpv can flip pause on live
	// stream reconnects, making player.State().Playing unreliable.
	e.mu.RLock()
	playing := e.now.Playing
	e.mu.RUnlock()
	slog.Debug("TogglePlayback", "intent_playing", playing)
	if !playing {
		// Live/continuous streams aren't buffered while paused, so "play" reloads
		// the source from the live edge rather than resuming stale audio.
		if e.isLive() && current != nil {
			slog.Info("TogglePlayback.live_reload")
			go func() { _ = e.PlayTarget(context.Background(), current) }()
			return
		}
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
				// Do NOT update Playing from Tick: engine intent (set by
				// Pause/Resume/PlayTarget) is authoritative. Live streams can
				// auto-reconnect and flip mpv's pause state unexpectedly.
				n.IsLive = st.IsLive
			})
			if st.Playing {
				e.maybeSave(false, pocketcasts.StatusInProgress)
			}
		case player.Metadata:
			title := ev.Metadata["icy-title"]
			slog.Debug("pump.metadata", "icy_title", title)
			if title != "" {
				e.update(func(n *NowPlaying) { n.Title = title })
			}
		case player.Ended:
			live := e.isLive()
			slog.Info("pump.ended", "is_live", live)
			e.update(func(n *NowPlaying) { n.Playing = false })
			if !live {
				e.completeAndAdvance()
			}
		case player.Error:
			slog.Error("pump.player_error", "err", ev.Err)
		}
	}
	slog.Info("pump.events_channel_closed")
}

// podcastArtURL returns the Pocket Casts CDN artwork URL for a podcast UUID.
func podcastArtURL(podcastUUID string) string {
	if podcastUUID == "" {
		return ""
	}
	return "https://static.pocketcasts.com/discover/images/280/" + podcastUUID + ".jpg"
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
