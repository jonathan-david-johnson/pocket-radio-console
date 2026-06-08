package full

import (
	"context"
	"image"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pocket-radio-console/internal/art"
	"pocket-radio-console/internal/library"
	"pocket-radio-console/internal/pocketcasts"
	"pocket-radio-console/internal/radio"
)

// fakeEngine satisfies the Engine interface for tests.
type fakeEngine struct {
	now         library.NowPlaying
	sub         chan library.NowPlaying
	staged      library.Target
	scrubs      []time.Duration
	upNext      []pocketcasts.Episode
	upNextErr   error
	newReleases []pocketcasts.NewRelease
	newRelErr   error

	mu         sync.Mutex
	lastTarget library.Target
}

func newFake(now library.NowPlaying) *fakeEngine {
	ch := make(chan library.NowPlaying, 8)
	return &fakeEngine{now: now, sub: ch}
}

func (f *fakeEngine) State() library.NowPlaying            { return f.now }
func (f *fakeEngine) Subscribe() <-chan library.NowPlaying { return f.sub }
func (f *fakeEngine) TogglePlayback()                      {}
func (f *fakeEngine) SkipForward()                         {}
func (f *fakeEngine) SkipBack()                            {}
func (f *fakeEngine) Scrub(d time.Duration)                { f.scrubs = append(f.scrubs, d) }
func (f *fakeEngine) StageTarget(t library.Target)  { f.staged = t }
func (f *fakeEngine) StagedTarget() library.Target  { return f.staged }
func (f *fakeEngine) CurrentTarget() library.Target { return nil }
func (f *fakeEngine) PlayTarget(_ context.Context, t library.Target) error {
	f.mu.Lock()
	f.lastTarget = t
	f.now.Title = "switched"
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) target() library.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTarget
}
func (f *fakeEngine) UpNextList(_ context.Context) ([]pocketcasts.Episode, error) {
	return f.upNext, f.upNextErr
}
func (f *fakeEngine) NewReleases(_ context.Context) ([]pocketcasts.NewRelease, error) {
	return f.newReleases, f.newRelErr
}

// Behavior 6: View() contains the title and the correct play/pause glyph.
func TestViewShowsTitleAndGlyph(t *testing.T) {
	cases := []struct {
		playing bool
		glyph   string
	}{
		{true, "⏸"},  // playing → show the pause button
		{false, "▶"}, // paused → show the play button
	}
	for _, c := range cases {
		now := library.NowPlaying{Title: "Test Song", Playing: c.playing}
		m := New(newFake(now), nil)
		m.width = 80
		m.height = 24
		view := m.View()
		if !strings.Contains(view, "Test Song") {
			t.Errorf("playing=%v: title not found in View()", c.playing)
		}
		if !strings.Contains(view, c.glyph) {
			t.Errorf("playing=%v: glyph %q not found in View()", c.playing, c.glyph)
		}
	}
}

// Progress meter shows only for seekable sources (Duration > 0): a 20-cell bar
// with skip controls. Live/continuous sources show an elapsed clock and only the
// play/pause control.
func TestTransportMeterVsElapsed(t *testing.T) {
	// Seekable podcast: meter present, skip controls present.
	seek := library.NowPlaying{Title: "Ep", Playing: true, Position: 30 * time.Second, Duration: time.Minute}
	m := New(newFake(seek), nil)
	m.width, m.height = 80, 24
	row := m.transportRow()
	if !strings.Contains(row, "█") || !strings.Contains(row, "░") {
		t.Errorf("seekable: expected meter bar, got %q", row)
	}
	if !strings.Contains(row, "⏮") || !strings.Contains(row, "⏭") {
		t.Errorf("seekable: expected skip controls, got %q", row)
	}
	// Meter is exactly meterWidth cells.
	if got := strings.Count(row, "█") + strings.Count(row, "░"); got != meterWidth {
		t.Errorf("meter width = %d, want %d", got, meterWidth)
	}
	// Half elapsed → half filled.
	if got := strings.Count(row, "█"); got != meterWidth/2 {
		t.Errorf("filled = %d, want %d", got, meterWidth/2)
	}

	// Live stream: no meter, no skip controls, elapsed clock instead.
	live := library.NowPlaying{Title: "KEXP", Playing: true, Position: 90 * time.Second, Duration: 0, IsLive: true}
	lm := New(newFake(live), nil)
	lm.width, lm.height = 80, 24
	lrow := lm.transportRow()
	if strings.Contains(lrow, "█") || strings.Contains(lrow, "░") {
		t.Errorf("live: meter should be hidden, got %q", lrow)
	}
	if strings.Contains(lrow, "⏮") || strings.Contains(lrow, "⏭") {
		t.Errorf("live: skip controls should be hidden, got %q", lrow)
	}
	if !strings.Contains(lrow, "1:30") {
		t.Errorf("live: expected elapsed clock, got %q", lrow)
	}
}

// Regression: an inline image must reserve exactly `height` logical lines, with
// the cursor-advancing escape neutralized by DECSC/DECRC (\x1b7 … \x1b8). The
// earlier code blank-padded the single escape line to height, so the escape's
// own row advance plus the padding newlines double-counted — Bubble Tea then
// miscounted the frame height and ghosted a duplicate now-playing block on the
// next redraw.
func TestInlineArtLinesReserveExactRowsAndPinCursor(t *testing.T) {
	m := New(newFake(library.NowPlaying{Title: "ep"}), nil)
	m.proto = art.ProtocolITerm2
	m.artImg = image.NewRGBA(image.Rect(0, 0, 8, 8))

	const height = 6
	lines := m.inlineArtLines(12, height)

	if len(lines) != height {
		t.Fatalf("inlineArtLines returned %d lines, want %d", len(lines), height)
	}
	if !strings.HasPrefix(lines[0], "\x1b7") || !strings.HasSuffix(lines[0], "\x1b8") {
		t.Errorf("image escape not wrapped in DECSC/DECRC: %q", lines[0])
	}
	for i := 1; i < height; i++ {
		if lines[i] != "" {
			t.Errorf("reserved line %d not blank: %q", i, lines[i])
		}
	}
}

// Regression: with an inline image set, View() renders the title exactly once.
func TestViewInlineImageTitleNotDuplicated(t *testing.T) {
	m := New(newFake(library.NowPlaying{Title: "1011: tmux + Terminal Maxxing", Playing: true}), nil)
	m.width = 80
	m.height = 24
	m.proto = art.ProtocolITerm2
	m.artImg = image.NewRGBA(image.Rect(0, 0, 8, 8))

	if got := strings.Count(m.View(), "1011: tmux + Terminal Maxxing"); got != 1 {
		t.Errorf("title appears %d times in View(), want 1", got)
	}
}

// New Releases tab: switching with "]" shows the release list; enter plays the
// selected release via PlayRelease.
func TestNewReleasesTab(t *testing.T) {
	fe := newFake(library.NowPlaying{Title: "now"})
	fe.newReleases = []pocketcasts.NewRelease{
		{UUID: "r1", Title: "Episode One", PodcastTitle: "Pod A", Published: time.Now().Add(-2 * time.Hour)},
		{UUID: "r2", Title: "Episode Two", PodcastTitle: "Pod B", Published: time.Now().Add(-26 * time.Hour)},
	}
	m := New(fe, nil)
	m.width, m.height = 80, 30

	// Switch to New Releases (Podcast pill is index 0, selected by default).
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	fm := m2.(Model)
	if fm.podcastTab != tabNewReleases {
		t.Fatalf("podcastTab = %d, want New Releases", fm.podcastTab)
	}
	// Lazy load fires a cmd; run it and feed the message back.
	if cmd == nil {
		t.Fatal("expected lazy-load command")
	}
	fm2, _ := fm.Update(cmd())
	fm = fm2.(Model)

	view := fm.View()
	if !strings.Contains(view, "Episode One") || !strings.Contains(view, "Pod A") {
		t.Errorf("New Releases list not rendered: %q", view)
	}

	// Move to the second release and play it.
	d, _ := fm.Update(tea.KeyMsg{Type: tea.KeyDown})
	fm = d.(Model)
	e, _ := fm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = e

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pr, ok := fe.target().(library.PlayRelease); ok {
			if pr.Release.UUID != "r2" {
				t.Fatalf("played %q, want r2", pr.Release.UUID)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("PlayRelease not invoked")
}

// fakeRadio implements RadioService for the Browse panel tests.
type fakeRadio struct {
	favs       []radio.Station
	browse     []radio.Station
	added      []string
	removed    []string
	savedOrder []string
}

func (f *fakeRadio) Favorites(context.Context) ([]radio.Station, error) { return f.favs, nil }
func (f *fakeRadio) Browse(context.Context) ([]radio.Station, error)    { return f.browse, nil }
func (f *fakeRadio) Search(_ context.Context, _ string) ([]radio.Station, error) {
	return f.browse, nil
}
func (f *fakeRadio) AddFavorite(_ context.Context, st radio.Station) error {
	f.added = append(f.added, st.ID)
	f.favs = append(f.favs, st)
	return nil
}
func (f *fakeRadio) RemoveFavorite(_ context.Context, st radio.Station) error {
	f.removed = append(f.removed, st.ID)
	out := f.favs[:0]
	for _, s := range f.favs {
		if s.ID != st.ID {
			out = append(out, s)
		}
	}
	f.favs = out
	return nil
}
func (f *fakeRadio) SaveOrder(ids []string) { f.savedOrder = ids }

func mustModel(m tea.Model, _ tea.Cmd) Model { return m.(Model) }

// Radio panel: selecting Browse loads favorites; reorder persists; enter plays
// the selected station; f toggles favorite.
func TestRadioPanelFavoritesReorderPlay(t *testing.T) {
	fe := newFake(library.NowPlaying{})
	fr := &fakeRadio{favs: []radio.Station{
		{ID: "a", Name: "Alpha", StreamURL: "http://a"},
		{ID: "b", Name: "Bravo", StreamURL: "http://b"},
		{ID: "c", Name: "Charlie", StreamURL: "http://c"},
	}}
	m := New(fe, nil).WithRadio(fr) // pills = [Podcast, Browse]
	m.width, m.height = 80, 30

	// Select the Browse pill (index 1) — lazy-loads favorites.
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	fm := m2.(Model)
	if !fm.browseSelected() {
		t.Fatal("Browse pill not selected")
	}
	if cmd == nil {
		t.Fatal("expected favorites fetch command")
	}
	fm = mustModel(fm.Update(cmd()))
	if !strings.Contains(fm.View(), "Alpha") {
		t.Errorf("favorites not rendered: %q", fm.View())
	}

	// Reorder: move Alpha (sel 0) down → order b,a,c persisted.
	fm = mustModel(fm.Update(tea.KeyMsg{Type: tea.KeyShiftDown}))
	if want := []string{"b", "a", "c"}; !reflect.DeepEqual(fr.savedOrder, want) {
		t.Fatalf("savedOrder = %v, want %v", fr.savedOrder, want)
	}
	if fm.favSel != 1 || fm.favorites[1].ID != "a" {
		t.Fatalf("after reorder favSel=%d favorites=%v", fm.favSel, fr.savedOrder)
	}

	// Enter plays the now-selected favorite (Alpha).
	fm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ps, ok := fe.target().(library.PlayStation); ok {
			if ps.Station.ID != "a" {
				t.Fatalf("played %q, want a", ps.Station.ID)
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := fe.target().(library.PlayStation); !ok {
		t.Fatal("enter did not play a station")
	}

	// Unfavorite the selected station via f → RemoveFavorite + refresh.
	_, fcmd := fm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if fcmd == nil {
		t.Fatal("expected favorite-toggle command")
	}
	fcmd() // runs RemoveFavorite + re-fetch
	if len(fr.removed) != 1 || fr.removed[0] != "a" {
		t.Fatalf("removed = %v, want [a]", fr.removed)
	}
}

// Behavior 5 (engine): staging sets StagedTarget without starting playback.
func TestEngineStagingDoesNotChangeCurrent(t *testing.T) {
	// This test lives in the full package as an integration check;
	// the authoritative engine test is in library/engine_test.go.
	fe := newFake(library.NowPlaying{Title: "current"})
	m := New(fe, []radio.Station{{Name: "KCRW", StreamURL: "https://kcrw"}})
	m.width = 80
	m.height = 24

	// Select pill 1 (KCRW) via keyboard '2'.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	fm := m2.(Model)

	if fm.engine.StagedTarget() == nil {
		t.Fatal("StageTarget not called after pill selection")
	}
	// Current title still "current" — no playback switched yet.
	if fe.now.Title != "current" {
		t.Errorf("current title changed to %q", fe.now.Title)
	}
}
