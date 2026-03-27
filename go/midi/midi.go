package midi

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
