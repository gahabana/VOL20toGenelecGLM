package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/lumberjack.v2"
)

// Setup creates a logger with dual output:
//   - Console (stderr): INFO level — clean terminal output
//   - File: DEBUG level — full detail for troubleshooting
//
// File uses lumberjack for rotation (4MB max, 5 backups).
// If logLevel is "NONE", both handlers are disabled.
// logFileName is relative to the binary's directory.
func Setup(logLevel, logFileName string) *slog.Logger {
	if logLevel == "NONE" {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	// Console handler: INFO level
	consoleLevel := slog.LevelInfo
	if logLevel == "DEBUG" {
		// In DEBUG mode, console also gets DEBUG for development
		consoleLevel = slog.LevelDebug
	}
	consoleHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: consoleLevel,
	})

	// File handler: always DEBUG (captures everything)
	logFilePath := logFileName
	if !filepath.IsAbs(logFilePath) {
		// Place log file next to the binary
		exePath, err := os.Executable()
		if err == nil {
			logFilePath = filepath.Join(filepath.Dir(exePath), logFileName)
		}
	}

	fileWriter := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    4, // MB
		MaxBackups: 5,
		MaxAge:     0, // Don't delete by age
		Compress:   false,
	}

	fileHandler := slog.NewTextHandler(fileWriter, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	return slog.New(&multiHandler{
		console: consoleHandler,
		file:    fileHandler,
	})
}

// multiHandler fans out log records to both console and file handlers.
type multiHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.console.Enabled(ctx, level) || h.file.Enabled(ctx, level)
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.console.Enabled(ctx, r.Level) {
		h.console.Handle(ctx, r.Clone())
	}
	if h.file.Enabled(ctx, r.Level) {
		h.file.Handle(ctx, r.Clone())
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &multiHandler{
		console: h.console.WithAttrs(attrs),
		file:    h.file.WithAttrs(attrs),
	}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	return &multiHandler{
		console: h.console.WithGroup(name),
		file:    h.file.WithGroup(name),
	}
}
