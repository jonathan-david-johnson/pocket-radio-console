package player

import (
	"sync"
	"time"
)

// Fake is an in-memory Player for unit tests and the engine test suite. It
// records the last loaded URL and start offset and lets tests drive events.
type Fake struct {
	mu        sync.Mutex
	state     PlaybackState
	LoadedURL string
	StartAt   time.Duration
	LoadCount int
	events    chan PlayerEvent
	closed    bool
}

// NewFake returns a ready Fake.
func NewFake() *Fake {
	return &Fake{events: make(chan PlayerEvent, 64)}
}

func (f *Fake) Load(url string, startAt time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LoadedURL = url
	f.StartAt = startAt
	f.LoadCount++
	f.state = PlaybackState{Playing: true, Position: startAt}
	return nil
}

func (f *Fake) Pause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Playing = false
	return nil
}

func (f *Fake) Resume() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Playing = true
	return nil
}

func (f *Fake) Seek(to time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Position = to
	return nil
}

func (f *Fake) State() PlaybackState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *Fake) Events() <-chan PlayerEvent { return f.events }

// Emit pushes an event for the engine to consume (test helper).
func (f *Fake) Emit(ev PlayerEvent) { f.events <- ev }

// SetDuration sets the reported duration (test helper).
func (f *Fake) SetDuration(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Duration = d
}

func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.events)
	}
	return nil
}
