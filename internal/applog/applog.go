// Package applog initialises the global slog logger from a file path.
// Call Init once in main before launching any goroutines or TUI.
package applog

import (
	"io"
	"log/slog"
	"os"
)

// Init configures the global slog logger. An empty path discards all output
// (safe default when --debug is absent). Otherwise logs are appended to path
// at the given minimum level; pass slog.LevelDebug to capture everything.
// The returned Closer must be closed on program exit to flush the file.
func Init(path string, level slog.Level) (io.Closer, error) {
	if path == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return io.NopCloser(nil), nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	h := slog.NewTextHandler(f, &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	})
	slog.SetDefault(slog.New(h))
	slog.Info("log opened", "path", path, "level", level.String())
	return f, nil
}
