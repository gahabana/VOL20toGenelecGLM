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

// Controller detects GLM power state and can toggle it.
//
// Deprecated: Use Commander for sending power commands and Observer for reading
// power state. Controller is retained for backwards compatibility.
type Controller interface {
	// SetPID sets the GLM process ID for window filtering.
	// Must be called before GetState/Toggle to ensure we find the correct window.
	SetPID(pid int)
	// GetState returns the current power state by inspecting the GLM window.
	GetState() (bool, error)
	// Toggle clicks the power button in the GLM window.
	Toggle() error
}
