package library

import (
	"context"
	"sync"
	"testing"
	"time"

	"pocket-radio-console/internal/player"
	"pocket-radio-console/internal/pocketcasts"
)

// mockClock is a controllable clock for throttle tests.
type mockClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mockClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *mockClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Behavior 3: resume seeks the player to playedUpTo before playing.
func TestResumeSeeksToPlayedUpTo(t *testing.T) {
	api := &fakeAPI{
		upNextResult: []pocketcasts.Episode{{UUID: "e1", Title: "Ep", URL: "https://e1", PodcastUUID: "pod"}},
		podcastInfos: map[string][]pocketcasts.PlaybackInfo{
			"pod": {{UUID: "e1", PlayedUpTo: 120, Duration: 1800}},
		},
	}
	p := player.NewFake()
	e := New(api, p, &fakeAuth{token: "t", hasToken: true})
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	if p.StartAt != 120*time.Second {
		t.Fatalf("resumed at %v, want 120s", p.StartAt)
	}
	if got := e.State().Position; got != 120*time.Second {
		t.Fatalf("NowPlaying position %v, want 120s", got)
	}
}

// Behavior 4: two ticks <30s apart → one save; a tick ≥30s later → another.
func TestThrottledSave(t *testing.T) {
	clk := &mockClock{t: time.Unix(1_000_000, 0)}
	api := &fakeAPI{upNextResult: []pocketcasts.Episode{
		{UUID: "e1", Title: "Ep", URL: "https://e1", PodcastUUID: "pod", Duration: 3600},
	}}
	p := player.NewFake()
	e := New(api, p, &fakeAuth{token: "t", hasToken: true}, WithClock(clk.now))
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}

	// Tick at +10s: too soon, no save.
	clk.advance(10 * time.Second)
	_ = p.Seek(10 * time.Second)
	p.Emit(player.PlayerEvent{Kind: player.Tick})
	time.Sleep(30 * time.Millisecond)
	if n := api.updateCount(); n != 0 {
		t.Fatalf("save count %d after 10s, want 0", n)
	}

	// Tick at +40s with a changed position: one save, status inProgress.
	clk.advance(30 * time.Second)
	_ = p.Seek(40 * time.Second)
	p.Emit(player.PlayerEvent{Kind: player.Tick})
	waitFor(t, func() bool { return api.updateCount() == 1 })

	u, _ := api.lastUpdate()
	if u.Status != pocketcasts.StatusInProgress || u.Position != 40 {
		t.Fatalf("update = %+v, want inProgress @40s", u)
	}
}

// Behavior 5: an Ended event → one completed save + one remove + advance to next.
func TestFinishAdvancesAndRemoves(t *testing.T) {
	api := &fakeAPI{upNextResult: []pocketcasts.Episode{
		{UUID: "e1", Title: "First", URL: "https://e1", PodcastUUID: "pod", Duration: 100},
		{UUID: "e2", Title: "Second", URL: "https://e2", PodcastUUID: "pod", Duration: 200},
	}}
	p := player.NewFake()
	e := New(api, p, &fakeAuth{token: "t", hasToken: true})
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	p.Emit(player.PlayerEvent{Kind: player.Ended})

	waitFor(t, func() bool { return e.State().Title == "Second" })
	if p.LoadedURL != "https://e2" {
		t.Fatalf("did not advance: loaded %q", p.LoadedURL)
	}
	if api.removedCount() != 1 {
		t.Fatalf("removed %d, want 1", api.removedCount())
	}
	u, ok := api.lastUpdate()
	if !ok || u.Status != pocketcasts.StatusCompleted || u.UUID != "e1" {
		t.Fatalf("last update = %+v, want completed e1", u)
	}
}

// Behavior 6: pausing with ≤10s remaining completes + removes; mid-episode keeps.
func TestPauseNearEndCompletes(t *testing.T) {
	api := &fakeAPI{upNextResult: []pocketcasts.Episode{
		{UUID: "e1", Title: "Only", URL: "https://e1", PodcastUUID: "pod", Duration: 100},
	}}
	p := player.NewFake()
	e := New(api, p, &fakeAuth{token: "t", hasToken: true})
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	_ = p.Seek(95 * time.Second) // 5s remaining
	e.TogglePlayback()           // pause near end

	waitFor(t, func() bool { return api.removedCount() == 1 })
	u, _ := api.lastUpdate()
	if u.Status != pocketcasts.StatusCompleted {
		t.Fatalf("status %v, want completed", u.Status)
	}
}

func TestPauseMidEpisodeKeeps(t *testing.T) {
	api := &fakeAPI{upNextResult: []pocketcasts.Episode{
		{UUID: "e1", Title: "Only", URL: "https://e1", PodcastUUID: "pod", Duration: 100},
	}}
	p := player.NewFake()
	e := New(api, p, &fakeAuth{token: "t", hasToken: true})
	defer e.Close()

	if err := e.PlayTarget(context.Background(), UpNextTop{}); err != nil {
		t.Fatal(err)
	}
	_ = p.Seek(50 * time.Second) // mid-episode
	e.TogglePlayback()           // pause

	waitFor(t, func() bool {
		u, ok := api.lastUpdate()
		return ok && u.Status == pocketcasts.StatusInProgress
	})
	if api.removedCount() != 0 {
		t.Fatalf("removed %d mid-episode, want 0", api.removedCount())
	}
}
