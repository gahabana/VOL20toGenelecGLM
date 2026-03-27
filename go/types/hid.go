package types

// VOL20 hardware keycodes (from HID reports).
const (
	KeyVolUp       = 2
	KeyVolDown     = 1
	KeyClick       = 32
	KeyDoubleClick = 16
	KeyTripleClick = 8
	KeyLongPress   = 4
)

// KeyNames maps keycodes to human-readable names for logging.
var KeyNames = map[int]string{
	KeyVolUp:       "VolUp",
	KeyVolDown:     "VolDown",
	KeyClick:       "Click",
	KeyDoubleClick: "DblClick",
	KeyTripleClick: "TplClick",
	KeyLongPress:   "LongPress",
}

// DefaultBindings maps physical VOL20 keys to logical actions.
var DefaultBindings = map[int]ActionKind{
	KeyVolUp:       KindAdjustVolume, // Rotation up
	KeyVolDown:     KindAdjustVolume, // Rotation down
	KeyClick:       KindSetPower,     // Click -> Power toggle
	KeyDoubleClick: KindSetDim,       // Double click -> Dim toggle
	KeyTripleClick: KindSetDim,       // Triple click -> Dim toggle
	KeyLongPress:   KindSetMute,      // Long press -> Mute toggle
}
