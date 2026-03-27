package midi

import "log/slog"

// Writer sends MIDI CC messages.
type Writer interface {
	SendCC(channel, cc, value int) error
	Close() error
}

// ReaderCallback is called when a MIDI CC message is received.
type ReaderCallback func(channel, cc, value int)

// Reader reads incoming MIDI messages and calls a callback.
type Reader interface {
	Start(cb ReaderCallback) error
	Close() error
}

// StubWriter is a no-op MIDI writer (used on non-Windows or when no port found).
type StubWriter struct {
	Log *slog.Logger
}

func (s *StubWriter) SendCC(channel, cc, value int) error {
	s.Log.Debug("MIDI stub: SendCC", "channel", channel, "cc", cc, "value", value)
	return nil
}

func (s *StubWriter) Close() error { return nil }

// StubReader is a no-op MIDI reader.
type StubReader struct {
	Log *slog.Logger
}

func (s *StubReader) Start(cb ReaderCallback) error {
	s.Log.Warn("MIDI reader not available on this platform")
	return nil
}

func (s *StubReader) Close() error { return nil }
