package full

import (
	"testing"
	"time"
)

// Behavior 7: rapid query changes collapse to a single Search after the debounce
// window, driven by an injected clock.
func TestSearchDebounce_CollapsesBurst(t *testing.T) {
	var now time.Time
	clock := func() time.Time { return now }
	d := newSearchDebouncer(100*time.Millisecond, clock)

	// Burst of edits inside the window.
	now = time.Unix(0, 0)
	d.Edit("k")
	now = now.Add(10 * time.Millisecond)
	d.Edit("ke")
	now = now.Add(10 * time.Millisecond)
	d.Edit("kex")
	now = now.Add(10 * time.Millisecond)
	d.Edit("kexp")

	// A tick before the window elapses must not fire.
	now = now.Add(50 * time.Millisecond) // 50ms since last edit
	if q, ok := d.Fire(); ok {
		t.Fatalf("fired early with %q", q)
	}

	// After the window, fire exactly once with the final query.
	now = now.Add(60 * time.Millisecond) // 110ms since last edit
	q, ok := d.Fire()
	if !ok || q != "kexp" {
		t.Fatalf("fire = (%q, %v), want (kexp, true)", q, ok)
	}

	// A second tick with no further edits must not fire again.
	now = now.Add(200 * time.Millisecond)
	if q, ok := d.Fire(); ok {
		t.Fatalf("fired twice with %q", q)
	}
}

func TestSearchDebounce_NewEditResetsWindow(t *testing.T) {
	var now time.Time
	d := newSearchDebouncer(100*time.Millisecond, func() time.Time { return now })

	now = time.Unix(0, 0)
	d.Edit("a")
	now = now.Add(90 * time.Millisecond)
	d.Edit("ab") // resets the window
	now = now.Add(90 * time.Millisecond)
	if _, ok := d.Fire(); ok {
		t.Fatal("window should have reset on second edit")
	}
	now = now.Add(20 * time.Millisecond)
	if q, ok := d.Fire(); !ok || q != "ab" {
		t.Fatalf("fire = (%q, %v), want (ab, true)", q, ok)
	}
}
