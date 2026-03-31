//go:build windows

package glm

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows API constants for process and window management.
const (
	thirtytwoCSSnapProcess       = 0x00000002
	aboveNormalPriorityClass     = 0x00008000
	invalidHandleValue           = ^uintptr(0)
	stillActive              int = 259
)

// CPU gating constants.
const (
	cpuThreshold     = 10.0
	cpuCheckInterval = 1 * time.Second
	cpuMaxChecks     = 300
)

// Process and window timing constants.
const (
	postStartDelay     = 3 * time.Second
	windowPollInterval = 1 * time.Second
	windowStableCount  = 3
	windowTimeout      = 60 * time.Second
)

// Watchdog constants.
const (
	watchdogInterval = 10 * time.Second
	hangThreshold    = 3 // 3 x 10s = 30s before kill+restart
	restartDelay     = 5 * time.Second
)

// processEntry32W mirrors the Windows PROCESSENTRY32W structure.
type processEntry32W struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [260]uint16
}

// Lazy-loaded DLL procedures for glm manager.
var (
	kernel32DLL = windows.NewLazySystemDLL("kernel32.dll")
	user32DLL   = windows.NewLazySystemDLL("user32.dll")

	procGetSystemTimes           = kernel32DLL.NewProc("GetSystemTimes")
	procCreateToolhelp32Snapshot = kernel32DLL.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32DLL.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32DLL.NewProc("Process32NextW")
	procSetPriorityClass         = kernel32DLL.NewProc("SetPriorityClass")

	procIsHungAppWindow          = user32DLL.NewProc("IsHungAppWindow")
	procEnumWindowsGLM           = user32DLL.NewProc("EnumWindows")
	procGetClassNameGLM          = user32DLL.NewProc("GetClassNameW")
	procGetWindowTextGLM         = user32DLL.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId = user32DLL.NewProc("GetWindowThreadProcessId")
)

// Package-level state for CPU usage delta calculation.
var (
	previousIdleTime   uint64
	previousKernelTime uint64
	previousUserTime   uint64
	cpuPrimed          bool
)

// WindowsManager manages the GLM application lifecycle on Windows.
type WindowsManager struct {
	glmPath         string
	cpuGating       bool
	log             *slog.Logger
	pid             int
	hwnd            uintptr
	preRestartCallback func()
	restartCallback    func(pid int)
	stopCh             chan struct{}
	mu              sync.Mutex
}

// NewWindowsManager creates a new WindowsManager.
func NewWindowsManager(glmPath string, cpuGating bool, log *slog.Logger) *WindowsManager {
	return &WindowsManager{
		glmPath:   glmPath,
		cpuGating: cpuGating,
		log:       log,
		stopCh:    make(chan struct{}),
	}
}

// Start launches or attaches to GLM, stabilizes the window, and starts the watchdog.
func (m *WindowsManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cpuGating {
		m.log.Info("waiting for CPU to calm down before launching GLM")
		if err := m.waitForCPUCalm(); err != nil {
			m.log.Warn("CPU gating timed out, proceeding anyway", "error", err)
		}
	}

	// Try to find an existing GLM process.
	existingPID, err := m.findGLMProcess()
	if err == nil && existingPID > 0 {
		m.log.Info("found existing GLM process", "pid", existingPID)
		m.pid = existingPID
	} else {
		// Launch a new GLM process.
		m.log.Info("launching GLM", "path", m.glmPath)
		cmd := exec.Command(m.glmPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to launch GLM: %w", err)
		}
		m.pid = cmd.Process.Pid
		m.log.Info("GLM launched", "pid", m.pid)
		time.Sleep(postStartDelay)
	}

	// Set process priority to above normal.
	if err := m.setPriority(m.pid); err != nil {
		m.log.Warn("failed to set GLM priority", "pid", m.pid, "error", err)
	}

	// Wait for the window to stabilize.
	hwnd, err := m.waitForWindowStable(m.pid)
	if err != nil {
		return fmt.Errorf("GLM window did not stabilize: %w", err)
	}
	m.hwnd = hwnd
	m.log.Info("GLM window stabilized", "pid", m.pid, "hwnd", fmt.Sprintf("0x%X", hwnd))

	// Start watchdog goroutine.
	go m.watchdogLoop()

	return nil
}

// Stop stops the watchdog goroutine. It does NOT kill the GLM process.
func (m *WindowsManager) Stop() {
	select {
	case <-m.stopCh:
		// Already stopped.
	default:
		close(m.stopCh)
	}
}

// IsAlive returns true if the GLM process is still running.
func (m *WindowsManager) IsAlive() bool {
	m.mu.Lock()
	currentPID := m.pid
	m.mu.Unlock()

	if currentPID == 0 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(currentPID))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}

	return exitCode == uint32(stillActive)
}

// GetPID returns the current GLM process ID (0 if not running).
func (m *WindowsManager) GetPID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pid
}

// SetPreRestartCallback sets a function called before GLM is relaunched.
func (m *WindowsManager) SetPreRestartCallback(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.preRestartCallback = fn
}

// SetRestartCallback sets a function called after GLM restarts.
func (m *WindowsManager) SetRestartCallback(fn func(pid int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restartCallback = fn
}

// waitForCPUCalm polls CPU usage until it drops below the threshold or times out.
func (m *WindowsManager) waitForCPUCalm() error {
	for checkIndex := 0; checkIndex < cpuMaxChecks; checkIndex++ {
		cpuPercent, err := getSystemCPU()
		if err != nil {
			m.log.Warn("failed to read CPU usage", "error", err)
			time.Sleep(cpuCheckInterval)
			continue
		}

		if cpuPercent < cpuThreshold {
			m.log.Info("CPU is calm", "cpu_percent", fmt.Sprintf("%.1f", cpuPercent), "check", checkIndex+1)
			return nil
		}

		if checkIndex%30 == 0 {
			m.log.Info("waiting for CPU to calm", "cpu_percent", fmt.Sprintf("%.1f", cpuPercent), "check", checkIndex+1)
		}

		time.Sleep(cpuCheckInterval)
	}

	return fmt.Errorf("CPU did not calm below %.0f%% within %d checks", cpuThreshold, cpuMaxChecks)
}

// getSystemCPU returns the current system CPU usage percentage using GetSystemTimes.
// The first call primes the values and returns 0.
func getSystemCPU() (float64, error) {
	var idleTime, kernelTime, userTime windows.Filetime

	ret, _, err := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetSystemTimes failed: %w", err)
	}

	currentIdle := filetimeToUint64(idleTime)
	currentKernel := filetimeToUint64(kernelTime)
	currentUser := filetimeToUint64(userTime)

	if !cpuPrimed {
		previousIdleTime = currentIdle
		previousKernelTime = currentKernel
		previousUserTime = currentUser
		cpuPrimed = true
		return 0, nil
	}

	idleDelta := currentIdle - previousIdleTime
	kernelDelta := currentKernel - previousKernelTime
	userDelta := currentUser - previousUserTime

	previousIdleTime = currentIdle
	previousKernelTime = currentKernel
	previousUserTime = currentUser

	totalDelta := kernelDelta + userDelta
	if totalDelta == 0 {
		return 0, nil
	}

	// kernel time includes idle time, so total busy = total - idle
	cpuPercent := (1.0 - float64(idleDelta)/float64(totalDelta)) * 100.0
	return cpuPercent, nil
}

// filetimeToUint64 converts a Windows FILETIME to a uint64.
func filetimeToUint64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// findGLMProcess enumerates running processes and looks for GLMv5.exe.
func (m *WindowsManager) findGLMProcess() (int, error) {
	snapshotHandle, _, err := procCreateToolhelp32Snapshot.Call(thirtytwoCSSnapProcess, 0)
	if snapshotHandle == invalidHandleValue {
		return 0, fmt.Errorf("CreateToolhelp32Snapshot failed: %w", err)
	}
	defer windows.CloseHandle(windows.Handle(snapshotHandle))

	var entry processEntry32W
	entry.dwSize = uint32(unsafe.Sizeof(entry))

	ret, _, err := procProcess32FirstW.Call(snapshotHandle, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return 0, fmt.Errorf("Process32FirstW failed: %w", err)
	}

	for {
		exeName := windows.UTF16ToString(entry.szExeFile[:])
		if strings.EqualFold(exeName, "GLMv5.exe") {
			return int(entry.th32ProcessID), nil
		}

		entry.dwSize = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshotHandle, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	return 0, fmt.Errorf("GLMv5.exe process not found")
}

// setPriority sets the process priority to above normal.
func (m *WindowsManager) setPriority(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess failed for PID %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	ret, _, err := procSetPriorityClass.Call(uintptr(handle), aboveNormalPriorityClass)
	if ret == 0 {
		return fmt.Errorf("SetPriorityClass failed for PID %d: %w", pid, err)
	}

	m.log.Info("set GLM priority to above normal", "pid", pid)
	return nil
}

// windowInfo holds details about a discovered JUCE/GLM window.
type windowInfo struct {
	hwnd      uintptr
	className string
	title     string
}

// waitForWindowStable polls for GLM windows matching the given PID until the
// titled main window HWND has been the same for windowStableCount consecutive polls.
// Only the titled window matters for HWND selection; untitled helper windows are ignored.
func (m *WindowsManager) waitForWindowStable(targetPID int) (uintptr, error) {
	deadline := time.Now().Add(windowTimeout)
	var lastTitledHWND uintptr
	consecutiveCount := 0
	poll := 0

	for time.Now().Before(deadline) {
		poll++
		windows := findAllWindowsByPID(targetPID)

		// Find the main window (title contains "GLM").
		var titledHWND uintptr
		var titledTitle string
		untitled := 0
		for _, w := range windows {
			if strings.Contains(w.title, "GLM") {
				titledHWND = w.hwnd
				titledTitle = w.title
			} else {
				untitled++
			}
		}

		// Log summary per poll (compact).
		if len(windows) == 0 {
			m.log.Debug("window poll: no JUCE+GLM windows found", "poll", poll, "pid", targetPID)
		} else {
			m.log.Debug("window poll",
				"poll", poll,
				"hwnd", fmt.Sprintf("0x%X", titledHWND),
				"title", titledTitle,
				"others", untitled,
			)
		}

		if titledHWND != 0 && titledHWND == lastTitledHWND {
			consecutiveCount++
		} else {
			if lastTitledHWND != 0 && titledHWND != lastTitledHWND {
				m.log.Info("titled window changed",
					"poll", poll,
					"old_hwnd", fmt.Sprintf("0x%X", lastTitledHWND),
					"new_hwnd", fmt.Sprintf("0x%X", titledHWND),
				)
			}
			lastTitledHWND = titledHWND
			consecutiveCount = 1
		}

		if consecutiveCount >= windowStableCount {
			m.log.Info("window stabilized",
				"polls", poll,
				"consecutive", consecutiveCount,
				"hwnd", fmt.Sprintf("0x%X", titledHWND),
				"title", titledTitle,
			)
			return titledHWND, nil
		}

		time.Sleep(windowPollInterval)
	}

	return 0, fmt.Errorf("GLM window for PID %d did not stabilize within %v", targetPID, windowTimeout)
}

// findAllWindowsByPID enumerates ALL top-level windows matching JUCE+GLM
// for the given PID. Returns all matches, not just the first.
func findAllWindowsByPID(targetPID int) []windowInfo {
	var results []windowInfo
	classNameBuf := make([]uint16, 256)
	windowTextBuf := make([]uint16, 256)

	callback := windows.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
		var windowPID uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		if int(windowPID) != targetPID {
			return 1
		}

		ret, _, _ := procGetClassNameGLM.Call(hwnd, uintptr(unsafe.Pointer(&classNameBuf[0])), 256)
		if ret == 0 {
			return 1
		}
		className := windows.UTF16ToString(classNameBuf[:ret])

		if !strings.HasPrefix(className, "JUCE_") {
			return 1
		}

		ret, _, _ = procGetWindowTextGLM.Call(hwnd, uintptr(unsafe.Pointer(&windowTextBuf[0])), 256)
		title := ""
		if ret > 0 {
			title = windows.UTF16ToString(windowTextBuf[:ret])
		}

		if strings.Contains(title, "GLM") || title == "" {
			results = append(results, windowInfo{hwnd: hwnd, className: className, title: title})
		}
		return 1 // continue — find ALL matches
	})

	procEnumWindowsGLM.Call(callback, 0) //nolint:errcheck
	return results
}

// watchdogLoop monitors the GLM process and restarts it if it dies or hangs.
func (m *WindowsManager) watchdogLoop() {
	m.log.Info("GLM watchdog started", "pid", m.pid)
	consecutiveHangs := 0

	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			m.log.Info("GLM watchdog stopped")
			return
		case <-ticker.C:
			if !m.IsAlive() {
				m.log.Warn("GLM process died, restarting", "pid", m.pid)
				consecutiveHangs = 0
				m.restart()
				continue
			}

			// Check if the window is hung.
			m.mu.Lock()
			currentHWND := m.hwnd
			m.mu.Unlock()

			if currentHWND != 0 {
				ret, _, _ := procIsHungAppWindow.Call(currentHWND)
				if ret != 0 {
					consecutiveHangs++
					m.log.Warn("GLM window is hung", "hwnd", fmt.Sprintf("0x%X", currentHWND), "consecutive", consecutiveHangs)

					if consecutiveHangs >= hangThreshold {
						m.log.Warn("GLM hung too many times, killing and restarting", "threshold", hangThreshold)
						m.killProcess()
						consecutiveHangs = 0
						m.restart()
					}
				} else {
					consecutiveHangs = 0
				}
			}
		}
	}
}

// killProcess terminates the current GLM process.
func (m *WindowsManager) killProcess() {
	m.mu.Lock()
	currentPID := m.pid
	m.mu.Unlock()

	if currentPID == 0 {
		return
	}

	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(currentPID))
	if err != nil {
		m.log.Warn("failed to open process for termination", "pid", currentPID, "error", err)
		return
	}
	defer windows.CloseHandle(handle)

	if err := windows.TerminateProcess(handle, 1); err != nil {
		m.log.Warn("failed to terminate GLM process", "pid", currentPID, "error", err)
		return
	}

	m.log.Info("terminated GLM process", "pid", currentPID)
}

// restart launches a new GLM process, sets priority, stabilizes the window,
// and invokes the restart callback.
func (m *WindowsManager) restart() {
	// Notify caller before relaunch so it can prepare for startup burst.
	m.mu.Lock()
	preCallback := m.preRestartCallback
	m.mu.Unlock()
	if preCallback != nil {
		preCallback()
	}

	time.Sleep(restartDelay)

	m.log.Info("restarting GLM", "path", m.glmPath)

	cmd := exec.Command(m.glmPath)
	if err := cmd.Start(); err != nil {
		m.log.Error("failed to restart GLM", "error", err)
		return
	}

	newPID := cmd.Process.Pid
	m.log.Info("GLM restarted", "pid", newPID)
	time.Sleep(postStartDelay)

	if err := m.setPriority(newPID); err != nil {
		m.log.Warn("failed to set priority on restarted GLM", "pid", newPID, "error", err)
	}

	hwnd, err := m.waitForWindowStable(newPID)
	if err != nil {
		m.log.Error("restarted GLM window did not stabilize", "error", err)
		// Still update the PID so the watchdog can track it.
		m.mu.Lock()
		m.pid = newPID
		m.hwnd = 0
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	m.pid = newPID
	m.hwnd = hwnd
	callbackFn := m.restartCallback
	m.mu.Unlock()

	m.log.Info("GLM restart complete", "pid", newPID, "hwnd", fmt.Sprintf("0x%X", hwnd))

	if callbackFn != nil {
		callbackFn(newPID)
	}
}
