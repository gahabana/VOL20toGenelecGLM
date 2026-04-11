package glm

// Manager handles the GLM application lifecycle.
type Manager interface {
	// Start launches or attaches to GLM, stabilizes the window, and starts the watchdog.
	Start() error
	// Stop stops the watchdog. Does NOT kill GLM.
	Stop()
	// IsAlive returns true if the GLM process is running.
	IsAlive() bool
	// GetPID returns the current GLM process ID (0 if not running).
	GetPID() int
	// SetPreRestartCallback sets a function called before GLM is relaunched.
	// Use this to prepare for the startup burst (e.g., suppress pattern detection).
	SetPreRestartCallback(fn func())
	// SetRestartCallback sets a function called after GLM restarts.
	SetRestartCallback(fn func(pid int))
	// MinimizeWindow minimizes the tracked GLM window. Returns an error
	// if no window has been stabilized yet, or if the underlying syscall
	// reports a failure. Safe to call after Start() returns successfully.
	MinimizeWindow() error
}
