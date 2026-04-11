//go:build windows

package winutil

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procShowWindow               = user32.NewProc("ShowWindow")
)

// SW_MINIMIZE value for ShowWindow — matches the Windows SDK constant.
const swMinimize = 6

// MinimizeWindow minimizes the given top-level window via ShowWindow(hwnd,
// SW_MINIMIZE). Returns an error if hwnd is zero; otherwise returns nil
// regardless of ShowWindow's return value (which reports the *previous*
// visibility state, not success/failure — a zero return just means the
// window was already hidden, which is still a successful "it's minimized
// now" outcome for our purposes).
func MinimizeWindow(hwnd uintptr) error {
	if hwnd == 0 {
		return fmt.Errorf("cannot minimize: hwnd is zero")
	}
	procShowWindow.Call(hwnd, swMinimize) //nolint:errcheck
	return nil
}

// FindGLMWindow finds the first top-level window whose class starts with
// "JUCE_" and whose title contains "GLM". If pid > 0, only windows belonging
// to that process are considered. Returns the HWND or an error.
func FindGLMWindow(pid int) (uintptr, error) {
	windows := FindAllGLMWindows(pid)
	for _, w := range windows {
		if strings.Contains(w.Title, "GLM") {
			return w.HWND, nil
		}
	}
	return 0, fmt.Errorf("GLM window not found")
}

// FindAllGLMWindows enumerates all top-level windows matching JUCE class prefix
// for the given PID. Returns titled ("GLM" in title) and untitled JUCE windows.
// If pid <= 0, all JUCE+GLM windows are returned regardless of process.
func FindAllGLMWindows(pid int) []WindowInfo {
	var results []WindowInfo
	classNameBuf := make([]uint16, 256)
	windowTextBuf := make([]uint16, 256)

	callback := windows.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
		if pid > 0 {
			var windowPID uint32
			procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
			if int(windowPID) != pid {
				return 1
			}
		}

		ret, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&classNameBuf[0])), 256)
		if ret == 0 {
			return 1
		}
		className := windows.UTF16ToString(classNameBuf[:ret])

		if !strings.HasPrefix(className, "JUCE_") {
			return 1
		}

		ret, _, _ = procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&windowTextBuf[0])), 256)
		title := ""
		if ret > 0 {
			title = windows.UTF16ToString(windowTextBuf[:ret])
		}

		if strings.Contains(title, "GLM") || title == "" {
			results = append(results, WindowInfo{HWND: hwnd, ClassName: className, Title: title})
		}
		return 1
	})

	procEnumWindows.Call(callback, 0) //nolint:errcheck
	return results
}
