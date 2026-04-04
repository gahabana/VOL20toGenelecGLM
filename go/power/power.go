package power

// Commander sends power on/off commands (via MIDI or UI click).
type Commander interface {
	// PowerOn ensures the speakers are powered on.
	PowerOn(traceID string) error
	// PowerOff ensures the speakers are powered off.
	PowerOff(traceID string) error
}

// Observer reads power state from the screen (optional — nil in MIDI-only mode).
// Window preparation (foreground, unminimize, on-screen placement) is handled
// internally before each pixel read.
type Observer interface {
	// GetPowerState returns the current power state by inspecting the GLM window.
	GetPowerState() (bool, error)
	// SetPID sets the GLM process ID for window filtering.
	SetPID(pid int)
}

