package library

import (
	"context"
	"sync"
	"testing"
	"time"

	"pocket-radio-console/internal/player"
	"pocket-radio-console/internal/radio"
)

// fakeTracklister returns canned tracks and reports support by station name.
type fakeTracklister struct {
	mu     sync.Mutex
	tracks []radio.Track
	has    bool
	calls  int
}

func (f *fakeTracklister) HasTracklist(s radio.Station) bool { return f.has }
func (f *fakeTracklister) Tracklist(ctx context.Context, s radio.Station) ([]radio.Track, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.tracks, nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// Behavior 7: while a Station plays, a tracklist tick sets Title to "Song — Artist".
func TestStationTracklistSetsTitle(t *testing.T) {
	tl := &fakeTracklister{has: true, tracks: []radio.Track{{Title: "Money", Artist: "Pink Floyd"}}}
	p := player.NewFake()
	e := New(&fakeAPI{}, p, &fakeAuth{token: "t", hasToken: true}, WithTracklister(tl))
	defer e.Close()

	st := radio.Station{ID: "1", Name: "KCRW", StreamURL: "https://kcrw"}
	if err := e.PlayTarget(context.Background(), PlayStation{Station: st}); err != nil {
		t.Fatal(err)
	}
	if p.LoadedURL != "https://kcrw" {
		t.Fatalf("loaded %q", p.LoadedURL)
	}
	waitFor(t, func() bool { return e.State().Title == "Money — Pink Floyd" })
	if e.State().Subtitle != "KCRW" {
		t.Fatalf("subtitle %q", e.State().Subtitle)
	}
}

// Behavior 7b: an empty tracklist falls back to the station name.
func TestStationEmptyTracklistFallsBackToName(t *testing.T) {
	tl := &fakeTracklister{has: true, tracks: nil}
	e := New(&fakeAPI{}, player.NewFake(), &fakeAuth{token: "t", hasToken: true}, WithTracklister(tl))
	defer e.Close()

	st := radio.Station{Name: "KEXP", StreamURL: "https://kexp"}
	if err := e.PlayTarget(context.Background(), PlayStation{Station: st}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tl.calls > 0 })
	if got := e.State().Title; got != "KEXP" {
		t.Fatalf("title %q, want station name", got)
	}
}

// Behavior 8: a live stream sets IsLive and SkipForward is a no-op.
func TestLiveStreamSkipNoOp(t *testing.T) {
	p := player.NewFake()
	e := New(&fakeAPI{}, p, &fakeAuth{token: "t", hasToken: true})
	defer e.Close()

	st := radio.Station{Name: "Some Stream", StreamURL: "https://live"}
	if err := e.PlayTarget(context.Background(), PlayStation{Station: st}); err != nil {
		t.Fatal(err)
	}
	if !e.State().IsLive {
		t.Fatal("want IsLive == true for station")
	}
	_ = p.Seek(0)
	e.SkipForward()
	if got := p.State().Position; got != 0 {
		t.Fatalf("SkipForward moved live stream to %v, want no-op", got)
	}
	e.Scrub(30 * time.Second)
	if got := p.State().Position; got != 0 {
		t.Fatalf("Scrub moved live stream to %v, want no-op", got)
	}
}

// A station without a tracklist does not poll.
func TestStationNoTracklistNoPoll(t *testing.T) {
	tl := &fakeTracklister{has: false}
	e := New(&fakeAPI{}, player.NewFake(), &fakeAuth{token: "t", hasToken: true}, WithTracklister(tl))
	defer e.Close()
	st := radio.Station{Name: "Plain", StreamURL: "https://x"}
	if err := e.PlayTarget(context.Background(), PlayStation{Station: st}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	tl.mu.Lock()
	calls := tl.calls
	tl.mu.Unlock()
	if calls != 0 {
		t.Fatalf("polled %d times for unsupported station", calls)
	}
	if e.State().Title != "Plain" {
		t.Fatalf("title %q", e.State().Title)
	}
}
