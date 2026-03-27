package types

// Action represents a discrete command from any input source (HID, MIDI, API).
type Action struct {
	// Kind identifies the action type (e.g. "volume", "power", "mute").
	Kind string
	// Value is the numeric payload (e.g. volume delta, CC value).
	Value int
}
