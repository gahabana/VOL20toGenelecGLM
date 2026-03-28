package power

// Controller detects GLM power state and can toggle it.
type Controller interface {
	// SetPID sets the GLM process ID for window filtering.
	// Must be called before GetState/Toggle to ensure we find the correct window.
	SetPID(pid int)
	// GetState returns the current power state by inspecting the GLM window.
	GetState() (bool, error)
	// Toggle clicks the power button in the GLM window.
	Toggle() error
	// BringToForeground brings the GLM window to the foreground so pixel
	// scanning can read it. Required before GetState when another window
	// may be covering GLM.
	BringToForeground() error
	// RestoreForeground restores the window that was in foreground before
	// BringToForeground was called.
	RestoreForeground()
}
