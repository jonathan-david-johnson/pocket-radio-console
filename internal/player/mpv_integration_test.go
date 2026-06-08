//go:build integration

package player

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// M0 behaviors 1,2,4,5 against real mpv with a short silent local file.
func TestMpvRealPlayback(t *testing.T) {
	path := writeSilentWAV(t, 1*time.Second)

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Load(path, 0); err != nil {
		t.Fatal(err)
	}

	// Behavior 4: a position tick arrives while playing.
	sawTick := waitEvent(t, p, Tick, 5*time.Second)
	if !sawTick {
		t.Fatal("no Tick event within 5s")
	}
	if !p.State().Playing {
		t.Fatal("want Playing == true")
	}

	// Behavior 2: pause/resume flip Playing.
	_ = p.Pause()
	time.Sleep(300 * time.Millisecond)
	if p.State().Playing {
		t.Fatal("want paused")
	}
	_ = p.Resume()

	// Behavior 5: Ended fires when the finite file completes.
	if !waitEvent(t, p, Ended, 6*time.Second) {
		t.Fatal("no Ended event")
	}
}

// Regression: Load must force play even when mpv is currently paused, so a
// source switch starts immediately instead of loading silently.
func TestMpvLoadUnpauses(t *testing.T) {
	path := writeSilentWAV(t, 3*time.Second)

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Load(path, 0); err != nil {
		t.Fatal(err)
	}
	if !waitEvent(t, p, Tick, 5*time.Second) {
		t.Fatal("no Tick after first load")
	}
	_ = p.Pause()
	time.Sleep(300 * time.Millisecond)
	if p.State().Playing {
		t.Fatal("want paused before reload")
	}

	// Reload while paused — must come back playing without an explicit Resume.
	if err := p.Load(path, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.State().Playing {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Load did not unpause mpv")
}

func waitEvent(t *testing.T, p Player, kind EventKind, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-p.Events():
			if ev.Kind == kind {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// writeSilentWAV writes a mono 8kHz 16-bit silent WAV and returns its path.
func writeSilentWAV(t *testing.T, d time.Duration) string {
	const rate = 8000
	samples := int(d.Seconds() * rate)
	dataLen := samples * 2 // 16-bit
	path := filepath.Join(t.TempDir(), "silent.wav")

	var buf []byte
	put := func(b ...byte) { buf = append(buf, b...) }
	putU32 := func(v uint32) { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); buf = append(buf, b...) }
	putU16 := func(v uint16) { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); buf = append(buf, b...) }

	put('R', 'I', 'F', 'F')
	putU32(uint32(36 + dataLen))
	put('W', 'A', 'V', 'E')
	put('f', 'm', 't', ' ')
	putU32(16)       // fmt chunk size
	putU16(1)        // PCM
	putU16(1)        // mono
	putU32(rate)     // sample rate
	putU32(rate * 2) // byte rate
	putU16(2)        // block align
	putU16(16)       // bits per sample
	put('d', 'a', 't', 'a')
	putU32(uint32(dataLen))
	buf = append(buf, make([]byte, dataLen)...) // silence

	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
