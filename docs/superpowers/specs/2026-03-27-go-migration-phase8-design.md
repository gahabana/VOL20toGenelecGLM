# Go Migration Phase 8: RDP Priming + GLM Process Management

## Overview

Startup automation for headless Windows VM: RDP session priming (prevents high CPU from display driver issues), GLM process lifecycle management (launch, monitor, restart-on-crash), and MIDI service restart (re-enumerates virtual MIDI ports). All three are optional — desktop users who launch GLM manually can disable them.

## Disable Switches

All three subsystems are enabled by default (headless VM scenario). Desktop users disable with flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `--no_glm_manager` | enabled | Skip GLM launch, watchdog, window stabilization |
| `--no_rdp_priming` | enabled | Skip RDP connect/disconnect cycle |
| `--no_midi_restart` | enabled | Skip `net stop/start midisrv` |
| `--no_glm_cpu_gating` | enabled | Skip CPU idle wait before GLM launch (only relevant with glm_manager) |

Existing config flags `--glm_manager`, `--glm_cpu_gating` already exist from Phase 2. Add new boolean flags for RDP priming and MIDI restart.

## Component 1: RDP Priming

**File:** `go/rdp/rdp_windows.go` (replaces current stub concept)

**Purpose:** One RDP connect/disconnect cycle per boot prevents Windows display driver issues that cause high CPU in GLM (OpenGL app) after RDP session switches.

### Boot Flag Check

1. Get boot time: `GetTickCount64()` → compute boot timestamp
2. Read `%TEMP%\rdp_primed.flag`
3. Compare stored boot time with current (60-second tolerance for clock drift)
4. If same boot → skip (already primed)
5. If different boot or file missing → prime, then write flag

### Credential Reading

Read from Windows Credential Manager via `CredReadW` (advapi32.dll):
- Try target `"localhost"` first, then `"TERMSRV/localhost"`
- Extract username and password from `CREDENTIALW` struct
- Prepend `.\` to username if no domain prefix (required for NLA)

### Priming Sequence

1. Launch: `wfreerdp /v:localhost /u:.\USER /p:PASS /cert:ignore /sec:nla`
2. Poll `query session` every 500ms for up to 10s, looking for `"rdp-tcp#"` in output
3. Wait 1s after session detected
4. Kill FreeRDP: terminate → wait 2s → force kill
5. Reconnect console: `tscon 1 /dest:console`
6. Wait 1s
7. Write boot timestamp to flag file

## Component 2: MIDI Service Restart

**Location:** Simple function in `go/main.go` or small utility.

Runs `net stop midisrv` then `net start midisrv` via `os/exec`. This forces Windows to re-enumerate LoopMIDI virtual ports, working around a Windows 11 update bug.

Skipped with `--no_midi_restart`. Only runs on Windows.

## Component 3: GLM Manager

**File:** `go/glm/manager_windows.go` + `go/glm/manager.go` (interface) + `go/glm/manager_stub.go`

### Interface

```go
type Manager interface {
    Start() error           // Launch or attach to GLM, start watchdog
    Stop()                  // Stop watchdog, optionally kill GLM
    IsAlive() bool          // Is GLM process running?
    GetPID() int            // Current GLM PID (0 if not running)
    SetRestartCallback(fn func(pid int))  // Called after successful restart
}
```

### CPU Gating

Before launching GLM (first start only, not restarts):
1. `GetSystemTimes` (kernel32) to compute system-wide CPU %
2. Poll every 1s until CPU < 10%
3. Max 300 checks (5 minute timeout), proceed with warning if exceeded

### Launch / Attach

1. Search for running `GLMv5.exe` via process enumeration (`CreateToolhelp32Snapshot` + `Process32FirstW/NextW`)
2. If found: attach (reuse PID), skip launch
3. If not found: `os/exec.Command(glmPath).Start()`
4. Set process priority to AboveNormal (`SetPriorityClass`)
5. Wait 3s post-start delay

### Window Stabilization

After launch/attach, wait for GLM's JUCE window handle to stabilize:
1. Poll every 1s: enumerate windows, find JUCE class + "GLM" title + matching PID
2. Track consecutive matching HWNDs
3. Stable = same HWND 2 consecutive times
4. Timeout: 60s (proceed with warning)
5. Once stable: update power controller's cached HWND

### Watchdog Goroutine

Runs every 5s:
1. `IsAlive()` — check process exists. If dead → restart immediately
2. `IsHungAppWindow(hwnd)` — check responsiveness. If hung → increment counter
3. 6 consecutive hangs (30s) → kill process → wait 5s → restart
4. Reset counter when responsive
5. On restart: call restart callback (re-init power controller)

### Interaction with Power Controller

After GLM starts or restarts, the manager needs to notify the power controller so it invalidates its cached HWND. The restart callback handles this.

## Startup Sequence (main.go updates)

```
1. Parse config, setup logging
2. RDP priming (if --no_rdp_priming not set, and needs_priming())
3. MIDI service restart (if --no_midi_restart not set)
4. GLM Manager start (if --no_glm_manager not set):
   a. CPU gating (if --no_glm_cpu_gating not set)
   b. Launch or attach to GLM
   c. Window stabilization
   d. Start watchdog goroutine
5. Initialize MIDI output + input
6. Initialize HID reader
7. Start consumer
8. Start API server
9. Wait for shutdown signal
```

## New Config Flags

Add to `go/config/config.go`:
- `RDPPriming bool` (default true) + `--no_rdp_priming`
- `MIDIRestart bool` (default true) + `--no_midi_restart`

Existing flags already handle GLM manager and CPU gating.

## Files

| File | Purpose |
|------|---------|
| `go/rdp/rdp_windows.go` | RDP priming: boot check, credential read, FreeRDP, tscon |
| `go/rdp/rdp_stub.go` | Existing stub (no-op on macOS) |
| `go/glm/manager.go` | Manager interface |
| `go/glm/manager_windows.go` | Windows implementation: launch, watchdog, CPU gating |
| `go/glm/manager_stub.go` | Stub for macOS |
| `go/config/config.go` | Add RDPPriming, MIDIRestart flags |
| `go/main.go` | Startup sequence orchestration, version 0.6.0 |
| `go/platform_windows.go` | Factory for GLM manager |
| `go/platform_stub.go` | Stub factory |

## Windows Syscalls

- `kernel32.dll`: `GetTickCount64`, `GetSystemTimes`, `CreateToolhelp32Snapshot`, `Process32FirstW`, `Process32NextW`, `OpenProcess`, `TerminateProcess`, `SetPriorityClass`
- `advapi32.dll`: `CredReadW`, `CredFree`
- `user32.dll`: `IsHungAppWindow` (reuse existing EnumWindows from power)

## Exit Criteria

- Cold boot on VM: RDP priming runs, GLM launches, everything connects automatically
- GLM crash: watchdog detects, restarts GLM, power controller re-initializes
- Desktop user: `--no_glm_manager --no_rdp_priming --no_midi_restart` disables all automation
- `--no_glm_cpu_gating` skips CPU wait on VM when not needed
