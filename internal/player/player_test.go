package player

import (
	"errors"
	"testing"
	"time"
)

// M0 behavior 6: missing mpv returns a typed ErrMpvNotFound (fake exec lookup).
func TestNew_MissingMpv(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = orig }()

	if _, err := New(); !errors.Is(err, ErrMpvNotFound) {
		t.Fatalf("want ErrMpvNotFound, got %v", err)
	}
}

// Fake basic contract: Load records URL/startAt and reports Playing.
func TestFakePlayer(t *testing.T) {
	f := NewFake()
	defer f.Close()
	if err := f.Load("https://x.mp3", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if f.LoadedURL != "https://x.mp3" || f.StartAt != 30*time.Second {
		t.Fatalf("load not recorded: %q %v", f.LoadedURL, f.StartAt)
	}
	if !f.State().Playing {
		t.Fatal("want Playing after Load")
	}
	_ = f.Pause()
	if f.State().Playing {
		t.Fatal("want paused")
	}
}
