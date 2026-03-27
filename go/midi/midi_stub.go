//go:build !windows

package midi

import "log/slog"

// StubWriter is a no-op MIDI writer for non-Windows platforms.
type StubWriter struct {
	Log *slog.Logger
}

func (s *StubWriter) SendCC(channel, cc, value int) error {
	s.Log.Debug("MIDI stub: SendCC", "channel", channel, "cc", cc, "value", value)
	return nil
}

func (s *StubWriter) Close() error { return nil }

// StubReader is a no-op MIDI reader for non-Windows platforms.
type StubReader struct {
	Log *slog.Logger
}

func (s *StubReader) Start(cb ReaderCallback) error {
	s.Log.Warn("MIDI reader not available on this platform")
	return nil
}

func (s *StubReader) Close() error { return nil }
