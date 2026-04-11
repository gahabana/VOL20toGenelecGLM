//go:build !windows

package winutil

import "fmt"

// FindGLMWindow is not supported on non-Windows platforms.
func FindGLMWindow(pid int) (uintptr, error) {
	return 0, fmt.Errorf("window enumeration not supported on this platform")
}

// FindAllGLMWindows is not supported on non-Windows platforms.
func FindAllGLMWindows(pid int) []WindowInfo {
	return nil
}

// MinimizeWindow is not supported on non-Windows platforms.
func MinimizeWindow(hwnd uintptr) error {
	return fmt.Errorf("window minimize not supported on this platform")
}
