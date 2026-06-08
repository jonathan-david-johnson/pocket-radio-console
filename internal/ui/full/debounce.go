package full

import "time"

// searchDebouncer collapses rapid query edits into a single search. Each
// keystroke calls Edit; the UI schedules a tick `window` later and calls Fire on
// it. Fire only returns a query once the window has elapsed since the last edit,
// so a burst of edits resolves to exactly one search of the final text. The
// clock is injectable for deterministic tests.
type searchDebouncer struct {
	window  time.Duration
	clock   func() time.Time
	pending string
	hasEdit bool
	lastAt  time.Time
}

func newSearchDebouncer(window time.Duration, clock func() time.Time) *searchDebouncer {
	if clock == nil {
		clock = time.Now
	}
	return &searchDebouncer{window: window, clock: clock}
}

// Edit records a new query value, restarting the debounce window.
func (d *searchDebouncer) Edit(q string) {
	d.pending = q
	d.hasEdit = true
	d.lastAt = d.clock()
}

// Fire returns (query, true) if the debounce window has elapsed since the last
// Edit, consuming the pending query so it fires at most once per burst.
func (d *searchDebouncer) Fire() (string, bool) {
	if !d.hasEdit {
		return "", false
	}
	if d.clock().Sub(d.lastAt) < d.window {
		return "", false
	}
	q := d.pending
	d.hasEdit = false
	d.pending = ""
	return q, true
}
