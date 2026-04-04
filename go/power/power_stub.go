//go:build !windows

package power

import "log/slog"

// StubPower is a no-op power implementation for non-Windows platforms.
// Implements Commander and Observer interfaces.
type StubPower struct {
	Log   *slog.Logger
	State bool
}

func (s *StubPower) SetPID(pid int) {}

// GetPowerState returns the stub power state. Implements Observer.
func (s *StubPower) GetPowerState() (bool, error) {
	return s.State, nil
}

// PowerOn sets the stub power state to ON. Implements Commander.
func (s *StubPower) PowerOn(traceID string) error {
	s.State = true
	s.Log.Info("power stub: ON", "trace_id", traceID)
	return nil
}

// PowerOff sets the stub power state to OFF. Implements Commander.
func (s *StubPower) PowerOff(traceID string) error {
	s.State = false
	s.Log.Info("power stub: OFF", "trace_id", traceID)
	return nil
}
