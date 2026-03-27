package power

// Controller detects GLM power state and can toggle it.
type Controller interface {
	// GetState returns the current power state by inspecting the GLM window.
	GetState() (bool, error)
	// Toggle clicks the power button in the GLM window.
	Toggle() error
}
