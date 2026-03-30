//go:build !windows

package rdp

import "log/slog"

// StubPrimer is a no-op for non-Windows platforms.
type StubPrimer struct{}

func (s *StubPrimer) NeedsPriming() bool { return false }
func (s *StubPrimer) Prime() error       { return nil }

// EnsureSessionConnected is a no-op on non-Windows platforms.
func EnsureSessionConnected(_ *slog.Logger) error { return nil }
