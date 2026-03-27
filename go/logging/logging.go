package logging

import "log/slog"

// Setup creates and returns a configured slog.Logger.
// Placeholder — will add file rotation and trace ID support later.
func Setup(level string) *slog.Logger {
	return slog.Default()
}
