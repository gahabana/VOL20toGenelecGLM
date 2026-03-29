//go:build !windows

package power

import "log/slog"

// StubController always reports power as on. For non-Windows platforms.
// Implements Controller (deprecated), Commander, and Observer interfaces.
type StubController struct {
	Log   *slog.Logger
	State bool
}

func (s *StubController) SetPID(pid int)           {}
func (s *StubController) GetState() (bool, error)  { return s.State, nil }
func (s *StubController) BringToForeground() error { return nil }
func (s *StubController) RestoreForeground()       {}
func (s *StubController) Toggle() error {
	s.State = !s.State
	s.Log.Info("power stub: toggled", "state", s.State)
	return nil
}

// GetPowerState returns the stub power state. Implements Observer.
func (s *StubController) GetPowerState() (bool, error) {
	return s.State, nil
}

// PowerOn sets the stub power state to ON. Implements Commander.
func (s *StubController) PowerOn(traceID string) error {
	s.State = true
	s.Log.Info("power stub: ON", "trace_id", traceID)
	return nil
}

// PowerOff sets the stub power state to OFF. Implements Commander.
func (s *StubController) PowerOff(traceID string) error {
	s.State = false
	s.Log.Info("power stub: OFF", "trace_id", traceID)
	return nil
}
