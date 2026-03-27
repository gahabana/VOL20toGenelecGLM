package types

// GLM MIDI CC numbers (from GLM MIDI Settings).
const (
	CCVolumeAbs = 20 // Absolute volume (0-127)
	CCVolUp     = 21 // Volume increment (momentary)
	CCVolDown   = 22 // Volume decrement (momentary)
	CCMute      = 23 // Mute (toggle)
	CCDim       = 24 // Dim (toggle)
	CCPower     = 28 // System Power (momentary, no MIDI feedback)
)

// CCNames maps CC numbers to human-readable names for logging.
var CCNames = map[int]string{
	CCVolumeAbs: "Volume",
	CCVolUp:     "Vol+",
	CCVolDown:   "Vol-",
	CCMute:      "Mute",
	CCDim:       "Dim",
	CCPower:     "Power",
}

// ControlMode describes how a GLM MIDI control behaves.
type ControlMode int

const (
	Momentary ControlMode = iota // Send 127 to trigger, auto-resets
	Toggle                       // Send 127/0 to set state
)

// PowerPattern is the GLM MIDI CC sequence emitted on power toggle.
// GLM sends MUTE -> VOL -> DIM -> MUTE -> VOL on power toggle.
var PowerPattern = []int{CCMute, CCVolumeAbs, CCDim, CCMute, CCVolumeAbs}

const (
	PowerPatternWindow   = 0.5  // Max time window for entire pattern (seconds)
	PowerPatternMinSpan  = 0.05 // Min span — faster means buffer dump, ignore
	PowerPatternMaxGap   = 0.26 // Max gap between consecutive messages (260ms)
	PowerPatternMaxTotal = 0.35 // Max total of all 4 gaps (350ms)
	PowerPatternPreGap   = 0.12 // Min silence before pattern (120ms)
	PowerStartupWindow   = 3.0  // Second pattern within this = GLM startup
)
