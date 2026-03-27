# Go Migration Phase 8: RDP Priming + GLM Process Management

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automate the full cold-boot startup sequence: RDP session priming, MIDI service restart, GLM process launch with CPU gating, window stabilization, and crash watchdog. All three subsystems are optional via CLI flags for desktop users.

**Architecture:** Three independent Windows-only subsystems orchestrated by main.go's startup sequence. RDP priming uses FreeRDP + tscon. GLM manager uses process enumeration, priority setting, window handle stabilization, and a watchdog goroutine. MIDI restart is a simple `net stop/start midisrv`. All behind build tags with stubs on macOS.

**Tech Stack:** Go 1.26, `golang.org/x/sys/windows`, `kernel32.dll`, `advapi32.dll`, `user32.dll` syscalls, `os/exec` for FreeRDP/tscon/net commands.

---

## File Map

| File | Purpose |
|------|---------|
| `go/config/config.go` | Add RDPPriming, MIDIRestart flags |
| `go/config/config_test.go` | Update defaults test |
| `go/rdp/rdp_windows.go` | RDP priming: boot check, credentials, FreeRDP, tscon |
| `go/rdp/rdp_stub.go` | Existing stub (already correct) |
| `go/glm/manager.go` | Manager interface |
| `go/glm/manager_windows.go` | Windows: CPU gating, launch, watchdog, window stabilization |
| `go/glm/manager_stub.go` | Stub for macOS |
| `go/main.go` | Startup sequence, version 0.6.0 |
| `go/platform_windows.go` | Add createGLMManager factory |
| `go/platform_stub.go` | Add createGLMManager stub factory |

---

### Task 1: Add Config Flags

Add `RDPPriming` and `MIDIRestart` boolean config fields.

**Files:**
- Modify: `go/config/config.go`
- Modify: `go/config/config_test.go`

- [ ] **Step 1: Add fields to Config struct**

Read `go/config/config.go`. Add two fields to the Config struct after the GLM fields:
```go
	// Startup automation
	RDPPriming  bool
	MIDIRestart bool
```

- [ ] **Step 2: Add CLI flags**

In the `Parse` function, after the `noGLMCPUGating` line, add:
```go
	fs.BoolVar(&cfg.RDPPriming, "rdp_priming", true, "Prime RDP session at startup (headless VM)")
	noRDPPriming := fs.Bool("no_rdp_priming", false, "Disable RDP session priming")
	fs.BoolVar(&cfg.MIDIRestart, "midi_restart", true, "Restart Windows MIDI service at startup")
	noMIDIRestart := fs.Bool("no_midi_restart", false, "Disable MIDI service restart")
```

In the negation flags section, add:
```go
	if *noRDPPriming {
		cfg.RDPPriming = false
	}
	if *noMIDIRestart {
		cfg.MIDIRestart = false
	}
```

- [ ] **Step 3: Update config_test.go**

Read `go/config/config_test.go`. Add assertions in `TestDefaults`:
```go
	if !cfg.RDPPriming {
		t.Error("RDPPriming should default to true")
	}
	if !cfg.MIDIRestart {
		t.Error("MIDIRestart should default to true")
	}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./config/ -v -count=1
```

- [ ] **Step 5: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/config/
git commit -m "feat(go): add RDPPriming and MIDIRestart config flags"
```

---

### Task 2: Implement RDP Priming

Windows-only RDP session priming. Boot flag check, credential reading, FreeRDP launch, tscon reconnect.

**Files:**
- Create: `go/rdp/rdp_windows.go`

- [ ] **Step 1: Implement rdp_windows.go**

Create `go/rdp/rdp_windows.go`:
```go
//go:build windows

package rdp

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	advapi32         = windows.NewLazySystemDLL("advapi32.dll")
	procGetTickCount64 = kernel32.NewProc("GetTickCount64")
	procCredReadW      = advapi32.NewProc("CredReadW")
	procCredFree       = advapi32.NewProc("CredFree")
)

const (
	CRED_TYPE_GENERIC = 1
	bootTimeTolerance = 60.0 // seconds
)

// credential holds a username/password pair.
type credential struct {
	Username string
	Password string
}

// CREDENTIALW structure (simplified).
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        uint64
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// WindowsPrimer handles RDP session priming on Windows.
type WindowsPrimer struct {
	Log *slog.Logger
}

// NeedsPriming checks if RDP priming is needed this boot.
func (p *WindowsPrimer) NeedsPriming() bool {
	bootTime := getBootTime()
	flagPath := filepath.Join(os.TempDir(), "rdp_primed.flag")

	data, err := os.ReadFile(flagPath)
	if err == nil {
		storedTime, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err == nil && math.Abs(storedTime-bootTime) < bootTimeTolerance {
			p.Log.Info("RDP already primed this boot")
			return false
		}
	}

	// Write flag file for this boot
	os.WriteFile(flagPath, []byte(fmt.Sprintf("%.2f", bootTime)), 0644)
	return true
}

// Prime performs the RDP connect/disconnect cycle.
func (p *WindowsPrimer) Prime() error {
	p.Log.Info("starting RDP session priming")

	cred, err := readCredential()
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}

	username := cred.Username
	if !strings.Contains(username, "\\") {
		username = ".\\" + username
	}

	// Launch FreeRDP
	cmd := exec.Command("wfreerdp",
		"/v:localhost",
		"/u:"+username,
		"/p:"+cred.Password,
		"/cert:ignore",
		"/sec:nla",
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start wfreerdp: %w", err)
	}
	p.Log.Info("FreeRDP started", "pid", cmd.Process.Pid)

	// Poll for RDP session
	sessionDetected := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		out, err := exec.Command("query", "session").Output()
		if err == nil && strings.Contains(strings.ToLower(string(out)), "rdp-tcp#") {
			sessionDetected = true
			p.Log.Info("RDP session detected")
			break
		}
	}

	if !sessionDetected {
		p.Log.Warn("RDP session not detected within timeout, proceeding anyway")
	}

	// Wait for Windows to register the session
	time.Sleep(1 * time.Second)

	// Kill FreeRDP
	cmd.Process.Kill()
	cmd.Wait()
	p.Log.Info("FreeRDP terminated")

	// Reconnect to console
	tscon := exec.Command("tscon", "1", "/dest:console")
	if out, err := tscon.CombinedOutput(); err != nil {
		p.Log.Warn("tscon failed", "err", err, "output", string(out))
		// Not fatal — may already be on console
	} else {
		p.Log.Info("reconnected to console session")
	}

	time.Sleep(1 * time.Second)
	p.Log.Info("RDP session priming complete")
	return nil
}

func getBootTime() float64 {
	ticks, _, _ := procGetTickCount64.Call()
	uptimeMs := float64(ticks)
	now := float64(time.Now().UnixMilli())
	return (now - uptimeMs) / 1000.0
}

func readCredential() (*credential, error) {
	targets := []string{"localhost", "TERMSRV/localhost"}

	for _, target := range targets {
		targetPtr, _ := windows.UTF16PtrFromString(target)
		var pcred *credentialW

		ret, _, err := procCredReadW.Call(
			uintptr(unsafe.Pointer(targetPtr)),
			CRED_TYPE_GENERIC,
			0,
			uintptr(unsafe.Pointer(&pcred)),
		)
		if ret == 0 {
			continue
		}
		defer procCredFree.Call(uintptr(unsafe.Pointer(pcred)))

		username := windows.UTF16PtrToString(pcred.UserName)

		// CredentialBlob is a byte array of UTF-16LE password
		var password string
		if pcred.CredentialBlobSize > 0 && pcred.CredentialBlob != nil {
			blob := unsafe.Slice(pcred.CredentialBlob, pcred.CredentialBlobSize)
			// Convert UTF-16LE bytes to string
			u16 := make([]uint16, pcred.CredentialBlobSize/2)
			for i := range u16 {
				u16[i] = uint16(blob[i*2]) | uint16(blob[i*2+1])<<8
			}
			password = windows.UTF16ToString(u16)
		}

		if username != "" && password != "" {
			return &credential{Username: username, Password: password}, nil
		}
		// CredFree already deferred, but we need to handle the case where
		// the first target succeeds but has empty creds. Continue to next.
		_ = err
	}

	return nil, fmt.Errorf("no credentials found for localhost in Credential Manager")
}
```

- [ ] **Step 2: Verify build**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build ./... && go vet ./...
```

- [ ] **Step 3: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/rdp/rdp_windows.go
git commit -m "feat(go): implement RDP session priming via FreeRDP + tscon"
```

---

### Task 3: Implement GLM Manager Interface + Stubs

Create the `glm` package with interface and macOS stub.

**Files:**
- Create: `go/glm/manager.go`
- Create: `go/glm/manager_stub.go`

- [ ] **Step 1: Create glm directory**

```bash
mkdir -p /Users/zh/git/VOL20toGenelecGLM/go/glm
```

- [ ] **Step 2: Create manager.go**

Create `go/glm/manager.go`:
```go
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
	// SetRestartCallback sets a function called after GLM restarts (e.g., to re-init power controller).
	SetRestartCallback(fn func(pid int))
}
```

- [ ] **Step 3: Create manager_stub.go**

Create `go/glm/manager_stub.go`:
```go
//go:build !windows

package glm

import "log/slog"

// StubManager is a no-op GLM manager for non-Windows platforms.
type StubManager struct {
	Log *slog.Logger
}

func (s *StubManager) Start() error               { s.Log.Warn("GLM manager not available on this platform"); return nil }
func (s *StubManager) Stop()                       {}
func (s *StubManager) IsAlive() bool               { return false }
func (s *StubManager) GetPID() int                 { return 0 }
func (s *StubManager) SetRestartCallback(fn func(pid int)) {}
```

- [ ] **Step 4: Verify build**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build ./... && go vet ./...
```

- [ ] **Step 5: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/glm/
git commit -m "feat(go): add GLM manager interface and stub"
```

---

### Task 4: Implement GLM Manager (Windows)

CPU gating, process launch/attach, window stabilization, watchdog.

**Files:**
- Create: `go/glm/manager_windows.go`

- [ ] **Step 1: Implement manager_windows.go**

Create `go/glm/manager_windows.go`:
```go
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

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	user32                = windows.NewLazySystemDLL("user32.dll")
	procGetSystemTimes    = kernel32.NewProc("GetSystemTimes")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW   = kernel32.NewProc("Process32FirstW")
	procProcess32NextW    = kernel32.NewProc("Process32NextW")
	procSetPriorityClass  = kernel32.NewProc("SetPriorityClass")
	procIsHungAppWindow   = user32.NewProc("IsHungAppWindow")
	procEnumWindows       = user32.NewProc("EnumWindows")
	procGetClassNameW     = user32.NewProc("GetClassNameW")
	procGetWindowTextW    = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
)

const (
	TH32CS_SNAPPROCESS        = 0x2
	ABOVE_NORMAL_PRIORITY_CLASS = 0x8000
	INVALID_HANDLE_VALUE       = ^uintptr(0)

	cpuThreshold       = 10.0  // percent
	cpuCheckInterval   = 1 * time.Second
	cpuMaxChecks       = 300   // 5 minutes
	postStartDelay     = 3 * time.Second
	windowPollInterval = 1 * time.Second
	windowStableCount  = 2
	windowTimeout      = 60 * time.Second
	watchdogInterval   = 5 * time.Second
	hangThreshold      = 6     // 30s of hangs
	restartDelay       = 5 * time.Second
)

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

// WindowsManager manages the GLM process lifecycle on Windows.
type WindowsManager struct {
	glmPath         string
	cpuGating       bool
	log             *slog.Logger
	pid             int
	processHandle   windows.Handle
	hwnd            uintptr
	restartCallback func(pid int)
	stopCh          chan struct{}
	mu              sync.Mutex
}

// NewWindowsManager creates a GLM manager.
func NewWindowsManager(glmPath string, cpuGating bool, log *slog.Logger) *WindowsManager {
	return &WindowsManager{
		glmPath: glmPath,
		cpuGating: cpuGating,
		log:     log,
		stopCh:  make(chan struct{}),
	}
}

func (m *WindowsManager) SetRestartCallback(fn func(pid int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restartCallback = fn
}

func (m *WindowsManager) GetPID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pid
}

func (m *WindowsManager) IsAlive() bool {
	m.mu.Lock()
	pid := m.pid
	m.mu.Unlock()

	if pid == 0 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

// Start launches or attaches to GLM, stabilizes the window, starts watchdog.
func (m *WindowsManager) Start() error {
	// CPU gating
	if m.cpuGating {
		m.waitForCPUCalm()
	}

	// Find or launch GLM
	pid := m.findGLMProcess()
	if pid > 0 {
		m.log.Info("attaching to existing GLM process", "pid", pid)
	} else {
		m.log.Info("launching GLM", "path", m.glmPath)
		cmd := exec.Command(m.glmPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("launch GLM: %w", err)
		}
		pid = cmd.Process.Pid
		cmd.Process.Release() // Detach — GLM runs independently
		m.log.Info("GLM started", "pid", pid)
		time.Sleep(postStartDelay)
	}

	m.mu.Lock()
	m.pid = pid
	m.mu.Unlock()

	// Set priority to AboveNormal
	m.setPriority(pid)

	// Stabilize window handle
	hwnd, err := m.waitForWindowStable(pid)
	if err != nil {
		m.log.Warn("window stabilization failed, proceeding", "err", err)
	} else {
		m.mu.Lock()
		m.hwnd = hwnd
		m.mu.Unlock()
		m.log.Info("GLM window stabilized", "hwnd", fmt.Sprintf("0x%x", hwnd))
	}

	// Start watchdog
	go m.watchdogLoop()
	m.log.Info("GLM watchdog started")

	return nil
}

func (m *WindowsManager) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

func (m *WindowsManager) waitForCPUCalm() {
	m.log.Info("waiting for CPU to calm down", "threshold", cpuThreshold)

	// Prime the first reading
	getSystemCPU()
	time.Sleep(cpuCheckInterval)

	for i := 0; i < cpuMaxChecks; i++ {
		cpu := getSystemCPU()
		m.log.Debug("CPU check", "percent", fmt.Sprintf("%.1f", cpu), "check", i+1)
		if cpu < cpuThreshold {
			m.log.Info("CPU calm", "percent", fmt.Sprintf("%.1f", cpu), "checks", i+1)
			return
		}
		time.Sleep(cpuCheckInterval)
	}
	m.log.Warn("CPU gating timeout, proceeding anyway")
}

func (m *WindowsManager) findGLMProcess() int {
	snapshot, _, _ := procCreateToolhelp32Snapshot.Call(TH32CS_SNAPPROCESS, 0)
	if snapshot == INVALID_HANDLE_VALUE {
		return 0
	}
	defer windows.CloseHandle(windows.Handle(snapshot))

	var entry processEntry32W
	entry.dwSize = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		name := windows.UTF16ToString(entry.szExeFile[:])
		if strings.EqualFold(name, "GLMv5.exe") {
			return int(entry.th32ProcessID)
		}
		entry.dwSize = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	return 0
}

func (m *WindowsManager) setPriority(pid int) {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		m.log.Warn("could not open process for priority", "pid", pid, "err", err)
		return
	}
	defer windows.CloseHandle(handle)

	ret, _, err := procSetPriorityClass.Call(uintptr(handle), ABOVE_NORMAL_PRIORITY_CLASS)
	if ret == 0 {
		m.log.Warn("SetPriorityClass failed", "err", err)
	} else {
		m.log.Debug("GLM priority set to AboveNormal")
	}
}

func (m *WindowsManager) waitForWindowStable(pid int) (uintptr, error) {
	deadline := time.Now().Add(windowTimeout)
	var lastHWND uintptr
	stableCount := 0

	for time.Now().Before(deadline) {
		hwnd := m.findWindowByPID(pid)
		if hwnd == 0 {
			stableCount = 0
			time.Sleep(windowPollInterval)
			continue
		}

		if hwnd == lastHWND {
			stableCount++
			if stableCount >= windowStableCount {
				return hwnd, nil
			}
		} else {
			lastHWND = hwnd
			stableCount = 1
		}

		time.Sleep(windowPollInterval)
	}

	if lastHWND != 0 {
		return lastHWND, nil // Best effort
	}
	return 0, fmt.Errorf("GLM window not found within %v", windowTimeout)
}

func (m *WindowsManager) findWindowByPID(targetPID int) uintptr {
	var found uintptr
	cb := windows.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
		className := make([]uint16, 256)
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&className[0])), 256)
		name := windows.UTF16ToString(className)
		if !strings.HasPrefix(name, "JUCE_") {
			return 1
		}

		title := make([]uint16, 256)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
		titleStr := windows.UTF16ToString(title)
		if !strings.Contains(titleStr, "GLM") {
			return 1
		}

		var windowPID uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		if int(windowPID) == targetPID {
			found = hwnd
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

func (m *WindowsManager) watchdogLoop() {
	hangCount := 0

	for {
		select {
		case <-m.stopCh:
			return
		case <-time.After(watchdogInterval):
		}

		if !m.IsAlive() {
			m.log.Warn("GLM process died, restarting")
			m.restart()
			hangCount = 0
			continue
		}

		m.mu.Lock()
		hwnd := m.hwnd
		m.mu.Unlock()

		if hwnd != 0 {
			ret, _, _ := procIsHungAppWindow.Call(hwnd)
			if ret != 0 {
				hangCount++
				m.log.Warn("GLM not responding", "hang_count", hangCount, "threshold", hangThreshold)
				if hangCount >= hangThreshold {
					m.log.Error("GLM hung too long, killing and restarting")
					m.killProcess()
					time.Sleep(restartDelay)
					m.restart()
					hangCount = 0
				}
			} else {
				if hangCount > 0 {
					m.log.Info("GLM responding again", "was_hung_for", hangCount)
				}
				hangCount = 0
			}
		}
	}
}

func (m *WindowsManager) killProcess() {
	m.mu.Lock()
	pid := m.pid
	m.mu.Unlock()

	if pid == 0 {
		return
	}

	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	windows.TerminateProcess(handle, 1)
	m.log.Info("GLM process killed", "pid", pid)
}

func (m *WindowsManager) restart() {
	m.log.Info("restarting GLM")
	cmd := exec.Command(m.glmPath)
	if err := cmd.Start(); err != nil {
		m.log.Error("failed to restart GLM", "err", err)
		return
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()

	m.mu.Lock()
	m.pid = pid
	m.mu.Unlock()

	m.log.Info("GLM restarted", "pid", pid)
	time.Sleep(postStartDelay)
	m.setPriority(pid)

	hwnd, err := m.waitForWindowStable(pid)
	if err != nil {
		m.log.Warn("window stabilization failed after restart", "err", err)
	} else {
		m.mu.Lock()
		m.hwnd = hwnd
		m.mu.Unlock()
	}

	m.mu.Lock()
	cb := m.restartCallback
	m.mu.Unlock()
	if cb != nil {
		cb(pid)
	}
}

// getSystemCPU returns system-wide CPU usage percentage.
// Must be called at least twice with a delay between calls for meaningful results.
var lastIdle, lastKernel, lastUser uint64

func getSystemCPU() float64 {
	var idle, kernel, user windows.Filetime
	ret, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ret == 0 {
		return 0
	}

	idleVal := uint64(idle.HighDateTime)<<32 | uint64(idle.LowDateTime)
	kernelVal := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	userVal := uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)

	if lastKernel == 0 {
		lastIdle, lastKernel, lastUser = idleVal, kernelVal, userVal
		return 0
	}

	idleDelta := float64(idleVal - lastIdle)
	totalDelta := float64((kernelVal - lastKernel) + (userVal - lastUser))

	lastIdle, lastKernel, lastUser = idleVal, kernelVal, userVal

	if totalDelta == 0 {
		return 0
	}

	return (1.0 - idleDelta/totalDelta) * 100.0
}
```

- [ ] **Step 2: Verify build**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build ./... && go vet ./...
```

- [ ] **Step 3: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/glm/manager_windows.go
git commit -m "feat(go): implement GLM manager with CPU gating, launch, and watchdog"
```

---

### Task 5: Wire Startup Sequence in main.go

Orchestrate RDP priming → MIDI restart → GLM manager → existing components.

**Files:**
- Modify: `go/main.go`
- Modify: `go/platform_windows.go`
- Modify: `go/platform_stub.go`

- [ ] **Step 1: Add platform factories**

Add to `go/platform_windows.go`:
```go
func createGLMManager(cfg config.Config, log *slog.Logger) glm.Manager {
	return glm.NewWindowsManager(cfg.GLMPath, cfg.GLMCPUGating, log.With("component", "glm"))
}

func createRDPPrimer(log *slog.Logger) *rdp.WindowsPrimer {
	return &rdp.WindowsPrimer{Log: log.With("component", "rdp")}
}

func restartMIDIService(log *slog.Logger) {
	log.Info("restarting Windows MIDI service")
	exec.Command("net", "stop", "midisrv").Run()
	time.Sleep(1 * time.Second)
	exec.Command("net", "start", "midisrv").Run()
	time.Sleep(1 * time.Second)
	log.Info("MIDI service restarted")
}
```

Add imports: `"vol20toglm/glm"`, `"vol20toglm/rdp"`, `"os/exec"`, `"time"`.

Add to `go/platform_stub.go`:
```go
func createGLMManager(cfg config.Config, log *slog.Logger) glm.Manager {
	return &glm.StubManager{Log: log.With("component", "glm")}
}

func runStartupTasks(cfg config.Config, log *slog.Logger) {
	// No startup tasks on non-Windows
}
```

Add import: `"vol20toglm/glm"`.

- [ ] **Step 2: Update main.go**

Read current `go/main.go`. Make these changes:

1. Bump version to `"0.6.0"`
2. After config parse and logging setup, before creating core components, add the startup sequence:

```go
	// === Startup automation (headless VM) ===
	runStartupTasks(cfg, log)

	// GLM Manager
	var glmMgr glm.Manager
	if cfg.GLMManager {
		glmMgr = createGLMManager(cfg, log)
		if err := glmMgr.Start(); err != nil {
			log.Error("GLM manager start failed", "err", err)
		} else {
			defer glmMgr.Stop()
		}
	}
```

3. In `platform_windows.go`, implement `runStartupTasks`:
```go
func runStartupTasks(cfg config.Config, log *slog.Logger) {
	// RDP priming
	if cfg.RDPPriming {
		primer := createRDPPrimer(log)
		if primer.NeedsPriming() {
			if err := primer.Prime(); err != nil {
				log.Error("RDP priming failed", "err", err)
			}
		}
	}

	// MIDI service restart
	if cfg.MIDIRestart {
		restartMIDIService(log)
	}
}
```

4. Add import for `"vol20toglm/glm"` in main.go.

- [ ] **Step 3: Verify build and tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build -o vol20toglm . && go vet ./... && go test ./... -count=1
```

- [ ] **Step 4: Test run on macOS**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && timeout 2 ./vol20toglm --log_level DEBUG --no_glm_manager 2>&1 || true
```

Expected: v0.6.0, no startup tasks on macOS, clean shutdown.

- [ ] **Step 5: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/
git commit -m "feat(go): wire startup sequence in main.go (v0.6.0)"
```

---

### Task 6: Test on Windows VM

Manual testing of the full startup sequence.

- [ ] **Step 1: Push, pull, build**

```bash
git push
# On VM:
git pull
cd go
go build -o vol20toglm.exe .
```

- [ ] **Step 2: Test with GLM already running (attach mode)**

```cmd
vol20toglm.exe --log_level DEBUG --no_rdp_priming --no_midi_restart
```

Expected:
- `attaching to existing GLM process pid=XXXX`
- `GLM window stabilized`
- `GLM watchdog started`
- Everything else works as before (HID, MIDI, API, power)

- [ ] **Step 3: Test with GLM not running (launch mode)**

Stop GLM first, then:
```cmd
vol20toglm.exe --log_level DEBUG --no_rdp_priming --no_midi_restart
```

Expected:
- `launching GLM path=C:\Program Files...`
- `GLM started pid=XXXX`
- `GLM window stabilized`
- `GLM watchdog started`

- [ ] **Step 4: Test desktop mode (all disabled)**

```cmd
vol20toglm.exe --log_level DEBUG --no_glm_manager --no_rdp_priming --no_midi_restart
```

Expected: same behavior as v0.5.0, no startup automation.

- [ ] **Step 5: Test RDP priming (if safe to do so)**

Only test if you can verify the VM recovers from tscon:
```cmd
vol20toglm.exe --log_level DEBUG --no_glm_manager --no_midi_restart
```

Expected: `starting RDP session priming` → `FreeRDP started` → `RDP session detected` → `reconnected to console`

- [ ] **Step 6: Commit any fixes**

```bash
git add -A && git commit -m "fix(go): adjustments from Phase 8 Windows VM testing"
```

---

## Summary

After completing all 6 tasks:

- **Config flags** — `--no_rdp_priming`, `--no_midi_restart` (new), `--no_glm_manager`, `--no_glm_cpu_gating` (existing)
- **RDP priming** — boot flag check, credential read from Credential Manager, FreeRDP + tscon
- **GLM manager** — CPU gating, process launch/attach, window stabilization, watchdog with hang detection
- **MIDI service restart** — `net stop/start midisrv`
- **Startup sequence** — RDP prime → MIDI restart → GLM launch → existing components

**New tests:** 2 (config defaults)
**Running total:** ~65 tests

**Exit criteria:**
- Cold boot on VM: full automation works
- GLM crash: watchdog restarts it
- Desktop user: `--no_glm_manager --no_rdp_priming --no_midi_restart` disables all

**Next phase:** Phase 9 (Integration + Polish)
