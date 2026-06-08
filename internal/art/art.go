// Package art handles terminal image detection, encoding, and caching.
package art

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Protocol is the terminal graphics protocol to use for rendering images.
type Protocol int

const (
	ProtocolNone   Protocol = iota
	ProtocolKitty          // kitty terminal graphics (ESC_G)
	ProtocolITerm2         // iTerm2 OSC 1337 inline images
	ProtocolSixel          // DEC sixel via chafa
	ProtocolChafa          // chafa shell fallback (block characters)
)

func (p Protocol) String() string {
	switch p {
	case ProtocolKitty:
		return "kitty"
	case ProtocolITerm2:
		return "iterm2"
	case ProtocolSixel:
		return "sixel"
	case ProtocolChafa:
		return "chafa"
	default:
		return "none"
	}
}

// DetectEnv determines the best protocol from environment variables alone.
// Production code wraps this with a chafa PATH check; tests call it directly.
func DetectEnv(getenv func(string) string) Protocol {
	tp := getenv("TERM_PROGRAM")
	term := getenv("TERM")
	switch {
	case tp == "iTerm.app" || getenv("LC_TERMINAL") == "iTerm2":
		return ProtocolITerm2
	case tp == "kitty" || term == "xterm-kitty" || tp == "ghostty":
		return ProtocolKitty
	case tp == "WezTerm":
		return ProtocolSixel
	default:
		return ProtocolNone
	}
}

// Detect returns the best protocol for the current terminal, including a
// chafa shell fallback when no graphics protocol is detected.
func Detect() Protocol {
	p := DetectEnv(os.Getenv)
	if p == ProtocolNone {
		if _, err := exec.LookPath("chafa"); err == nil {
			p = ProtocolChafa
		}
	}
	slog.Debug("art.Detect",
		"protocol", p,
		"TERM_PROGRAM", os.Getenv("TERM_PROGRAM"),
		"TERM", os.Getenv("TERM"),
		"LC_TERMINAL", os.Getenv("LC_TERMINAL"),
	)
	return p
}

// Render encodes img for the given terminal protocol. Returns nil for
// ProtocolNone or on encoding failure. width is the desired column width;
// height is the desired row count (used by kitty/iTerm2 to reserve space so
// the TUI layout accounts for the correct number of lines).
func Render(img image.Image, proto Protocol, width, height int) []byte {
	if proto == ProtocolNone || img == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	raw := buf.Bytes()
	switch proto {
	case ProtocolKitty:
		return kittyEncode(raw, width, height)
	case ProtocolITerm2:
		return iterm2Encode(raw, len(raw), height)
	case ProtocolSixel, ProtocolChafa:
		return chafaEncode(&buf, width)
	}
	return nil
}

// kittyEncode emits the Kitty terminal graphics protocol escape sequence.
// c=<width> and r=<height> tell the terminal exactly how many columns/rows to
// reserve, matching the placeholder box dimensions. i=1 is a fixed image ID so
// each new image replaces the previous one.
func kittyEncode(pngBytes []byte, width, height int) []byte {
	enc := base64.StdEncoding.EncodeToString(pngBytes)
	var out bytes.Buffer
	const chunk = 4096
	for i := 0; i < len(enc); i += chunk {
		end := i + chunk
		more := 1
		if end >= len(enc) {
			end = len(enc)
			more = 0
		}
		if i == 0 {
			fmt.Fprintf(&out, "\x1b_Ga=T,f=100,i=1,c=%d,r=%d,m=%d;%s\x1b\\", width, height, more, enc[i:end])
		} else {
			fmt.Fprintf(&out, "\x1b_Gm=%d;%s\x1b\\", more, enc[i:end])
		}
	}
	return out.Bytes()
}

// iterm2Encode emits the iTerm2 OSC 1337 inline image escape sequence.
// height=<rows> reserves the correct row count in the layout.
func iterm2Encode(pngBytes []byte, size, height int) []byte {
	enc := base64.StdEncoding.EncodeToString(pngBytes)
	return []byte(fmt.Sprintf("\x1b]1337;File=inline=1;size=%d;height=%d;preserveAspectRatio=1:%s\a", size, height, enc))
}

// chafaEncode shells out to chafa to produce block-character art.
func chafaEncode(pngBuf *bytes.Buffer, width int) []byte {
	if _, err := exec.LookPath("chafa"); err != nil {
		return nil
	}
	h := width / 2
	if h < 1 {
		h = 1
	}
	cmd := exec.Command("chafa", fmt.Sprintf("--size=%dx%d", width, h), "-")
	cmd.Stdin = bytes.NewReader(pngBuf.Bytes())
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return out
}

// Cache fetches and decodes images by URL, memoizing results.
type Cache struct {
	mu     sync.Mutex
	items  map[string]image.Image
	client *http.Client
}

// NewCache returns a Cache with a 10-second HTTP timeout.
func NewCache() *Cache {
	return &Cache{
		items:  map[string]image.Image{},
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Fetch returns a cached image or downloads and decodes it.
func (c *Cache) Fetch(ctx context.Context, url string) (image.Image, error) {
	c.mu.Lock()
	if img, ok := c.items[url]; ok {
		c.mu.Unlock()
		slog.Debug("art.Fetch.cache_hit", "url", url)
		return img, nil
	}
	c.mu.Unlock()

	slog.Debug("art.Fetch.downloading", "url", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("art.Fetch.request_failed", "url", url, "err", err)
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("art.Fetch.http_failed", "url", url, "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	slog.Debug("art.Fetch.http_ok", "url", url, "status", resp.StatusCode, "content_type", resp.Header.Get("Content-Type"))
	img, format, err := image.Decode(resp.Body)
	if err != nil {
		slog.Warn("art.Fetch.decode_failed", "url", url, "err", err)
		return nil, err
	}

	slog.Info("art.Fetch.success", "url", url, "format", format, "bounds", img.Bounds())
	c.mu.Lock()
	c.items[url] = img
	c.mu.Unlock()
	return img, nil
}
