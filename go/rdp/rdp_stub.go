//go:build !windows

package rdp

// StubPrimer is a no-op for non-Windows platforms.
type StubPrimer struct{}

func (s *StubPrimer) NeedsPriming() bool { return false }
func (s *StubPrimer) Prime() error       { return nil }
