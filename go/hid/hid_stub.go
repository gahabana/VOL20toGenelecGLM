//go:build !windows

package hid

import (
	"context"
	"log/slog"
	"vol20toglm/types"
)

// StubReader is a no-op HID reader for non-Windows platforms.
type StubReader struct {
	Log *slog.Logger
}

// Run blocks until ctx is cancelled. No HID device on this platform.
func (s *StubReader) Run(ctx context.Context, actions chan<- types.Action) error {
	s.Log.Warn("HID reader not available on this platform")
	<-ctx.Done()
	return ctx.Err()
}
