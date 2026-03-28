//go:build !windows

package power

import "log/slog"

// StubController always reports power as on. For non-Windows platforms.
type StubController struct {
	Log   *slog.Logger
	State bool
}

func (s *StubController) GetState() (bool, error)    { return s.State, nil }
func (s *StubController) BringToForeground() error    { return nil }
func (s *StubController) Toggle() error {
	s.State = !s.State
	s.Log.Info("power stub: toggled", "state", s.State)
	return nil
}
