// Package winutil provides shared Windows window enumeration utilities.
package winutil

// WindowInfo holds details about a discovered window.
type WindowInfo struct {
	HWND      uintptr
	ClassName string
	Title     string
}
