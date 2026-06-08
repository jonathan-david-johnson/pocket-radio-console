package art

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
)

// Behavior 1: Detect maps env vars to the right Protocol.
func TestDetectEnv(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want Protocol
	}{
		{map[string]string{"TERM_PROGRAM": "kitty"}, ProtocolKitty},
		{map[string]string{"TERM": "xterm-kitty"}, ProtocolKitty},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, ProtocolITerm2},
		{map[string]string{"LC_TERMINAL": "iTerm2"}, ProtocolITerm2},
		{map[string]string{"TERM_PROGRAM": "WezTerm"}, ProtocolSixel},
		{map[string]string{}, ProtocolNone},
	}
	for _, c := range cases {
		got := DetectEnv(func(k string) string { return c.env[k] })
		if got != c.want {
			t.Errorf("env=%v: got %v, want %v", c.env, got, c.want)
		}
	}
}

func tiny() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	return img
}

// Behavior 2: kitty encoder emits ESC_G header and ESC\ terminator.
func TestRenderKitty(t *testing.T) {
	out := Render(tiny(), ProtocolKitty, 20, 6)
	if len(out) == 0 {
		t.Fatal("kitty: empty output")
	}
	s := string(out)
	if !strings.HasPrefix(s, "\x1b_G") {
		t.Errorf("kitty: missing ESC_G header: %q", s[:min(len(s), 10)])
	}
	if !strings.Contains(s, "\x1b\\") {
		t.Error("kitty: missing ESC\\ terminator")
	}
}

// Behavior 2: iTerm2 encoder emits OSC 1337 header and BEL terminator.
func TestRenderITerm2(t *testing.T) {
	out := Render(tiny(), ProtocolITerm2, 20, 6)
	if len(out) == 0 {
		t.Fatal("iterm2: empty output")
	}
	s := string(out)
	if !strings.HasPrefix(s, "\x1b]1337;File=") {
		t.Errorf("iterm2: missing OSC 1337 header: %q", s[:min(len(s), 20)])
	}
	if !bytes.HasSuffix(out, []byte("\a")) {
		t.Error("iterm2: missing BEL terminator")
	}
}

// Behavior 3: ProtocolNone returns nil, no panic when piped/unsupported.
func TestRenderNone(t *testing.T) {
	out := Render(tiny(), ProtocolNone, 20, 6)
	if out != nil {
		t.Errorf("none: want nil, got %d bytes", len(out))
	}
	// nil image also safe
	out2 := Render(nil, ProtocolKitty, 20, 6)
	if out2 != nil {
		t.Errorf("nil image: want nil, got %d bytes", len(out2))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
