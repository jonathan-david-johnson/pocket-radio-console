// Package player is the only component that talks to mpv. It wraps the mpv
// subprocess behind the Player interface so the engine and tests never touch
// mpv directly. See docs/console/adr/0002-mpv-as-audio-engine.md.
package player

import (
	"errors"
	"time"
)

// ErrMpvNotFound is returned by New when the mpv binary is not on PATH.
var ErrMpvNotFound = errors.New("mpv not found on PATH (install with: brew install mpv)")

// PlaybackState is a snapshot of the player. Duration == 0 means unknown/live.
type PlaybackState struct {
	Playing  bool
	Position time.Duration
	Duration time.Duration // 0 == unknown/live
	IsLive   bool
}

// EventKind tags a PlayerEvent.
type EventKind int

const (
	// Tick is a periodic position update while playing.
	Tick EventKind = iota
	// Metadata carries ICY title etc. in Metadata.
	Metadata
	// Ended fires when a finite file completes.
	Ended
	// Error carries a playback error in Err.
	Error
)

// PlayerEvent is emitted on the Events channel.
type PlayerEvent struct {
	Kind     EventKind
	Metadata map[string]string // ICY title etc. for Kind==Metadata
	Err      error
}

// Player is the frozen M0 interface. mpv calls are a system boundary; this is
// the seam. Unit tests use Fake; real-mpv assertions live in an integration test.
type Player interface {
	Load(url string, startAt time.Duration) error
	Pause() error
	Resume() error
	Seek(to time.Duration) error
	State() PlaybackState
	Events() <-chan PlayerEvent
	Close() error
}
