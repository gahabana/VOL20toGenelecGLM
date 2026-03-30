//go:build !windows

package glm

import "log/slog"

// StubManager is a no-op GLM manager for non-Windows platforms.
type StubManager struct {
	Log *slog.Logger
}

func (s *StubManager) Start() error {
	s.Log.Warn("GLM manager not available on this platform")
	return nil
}
func (s *StubManager) Stop()                               {}
func (s *StubManager) IsAlive() bool                       { return false }
func (s *StubManager) GetPID() int                         { return 0 }
func (s *StubManager) SetPreRestartCallback(fn func())     {}
func (s *StubManager) SetRestartCallback(fn func(pid int)) {}
