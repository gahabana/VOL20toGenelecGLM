# Go Migration Phase 7: Power Control Design

## Overview

Windows-only power control for GLM via UI automation. Detects power state by capturing screen pixels and analyzing button/label colors. Toggles power by simulating mouse clicks. Matches the proven Python implementation exactly.

## Window Finding

Find the GLM window using Windows API:
- `EnumWindows` + `GetClassNameW` to find JUCE windows (class name starts with `JUCE_`)
- `GetWindowTextW` to filter for windows containing "GLM"
- Cache the HWND with 5-second TTL to avoid repeated enumeration
- If window not found, return error (GLM not running)

## State Detection

Dual-system approach matching Python:

### Primary: OFFLINE Gold Label Detection

Scan the honeycomb speaker grid region for gold/amber "OFFLINE" badges.

**Region**: Window rect with insets (15% left, 15% top, 25% right, 10% bottom).

**Gold pixel thresholds**:
- R > 150
- 120 < G < 200
- B < 80

**Decision**:
- Gold pixels >= 50 → OFF (speakers offline)
- Gold pixels == 0 → ON (no offline labels)
- Between → unknown (fall through to button detection)

### Fallback: Power Button Pixel Detection

Sample a 9x9 pixel patch around the power button.

**Button position**: `window.right - 28`, `window.top + 80`

**OFF state** (dark grey): `max(R,G,B) <= 95` AND max channel difference <= 22

**ON state** (green/teal): `G >= 110` AND `(G - R) >= 35`

**Fallback nudge**: If primary sample is unknown, try 8 pixels left to avoid glyph overlap.

## Screen Capture

`BitBlt` from the screen device context at the window's screen coordinates:
1. `GetWindowRect` to get window position
2. `CreateCompatibleDC` + `CreateCompatibleBitmap` for off-screen buffer
3. `BitBlt` from screen DC to buffer DC
4. `GetDIBits` to read pixel data into Go `[]byte`

Window must be visible (console session on VM — always true).

## Click Simulation

Match Python exactly:
1. `SetCursorPos(x, y)` — move cursor to button
2. Sleep 20ms
3. `mouse_event(MOUSEEVENTF_LEFTDOWN)` — press
4. Sleep 20ms
5. `mouse_event(MOUSEEVENTF_LEFTUP)` — release

## Toggle Flow

```
Toggle() called (from consumer, API, or HID)
    → Capture current state
    → If already in desired state, return
    → Click power button
    → Sleep 350ms (post-click delay)
    → Poll state every 150ms for up to 3.0s
    → If state changed → success
    → If timeout → return error
```

## Configuration Constants

| Parameter | Value | Purpose |
|-----------|-------|---------|
| `dxFromRight` | 28 | Button X offset from window right |
| `dyFromTop` | 80 | Button Y offset from window top |
| `patchRadius` | 4 | Pixel sample radius (9x9 patch) |
| `offMaxBrightness` | 95 | Max brightness for OFF state |
| `offMaxChannelDiff` | 22 | Max RGB channel difference for OFF |
| `onMinGreen` | 110 | Min green channel for ON state |
| `onGreenRedDiff` | 35 | Min (G-R) difference for ON |
| `goldMinR` | 150 | Min red for gold label |
| `goldMinG` | 120 | Min green for gold label |
| `goldMaxG` | 200 | Max green for gold label |
| `goldMaxB` | 80 | Max blue for gold label |
| `goldThreshold` | 50 | Min gold pixels for OFF |
| `pollInterval` | 150ms | State polling interval |
| `verifyTimeout` | 3.0s | Max time to wait for state change |
| `postClickDelay` | 350ms | Delay after click before polling |
| `focusDelay` | 150ms | Delay after focusing window |
| `hwndCacheTTL` | 5s | Window handle cache lifetime |
| `fallbackNudgeX` | 8 | Secondary sample offset |

## Integration

The `power.Controller` interface is already defined:
```go
type Controller interface {
    GetState() (bool, error)
    Toggle() error
}
```

The consumer calls `Toggle()` when it receives a `KindSetPower` action. Currently, power actions just send MIDI CC28. With Phase 7, the consumer should:
1. Send MIDI CC28 (tells GLM to toggle power)
2. Call `power.Controller.Toggle()` to verify via pixel detection

The pixel-based state is the ground truth. MIDI CC28 is fire-and-forget (no feedback).

## Windows Syscalls

All via `golang.org/x/sys/windows` (already a dependency):
- `EnumWindows`, `GetClassName`, `GetWindowText`, `GetWindowRect`
- `GetDC`, `CreateCompatibleDC`, `CreateCompatibleBitmap`, `BitBlt`, `GetDIBits`
- `SetCursorPos`, `mouse_event`
- `DeleteDC`, `DeleteObject`, `ReleaseDC`

## Files

| File | Purpose |
|------|---------|
| `go/power/power_windows.go` | Window finding, screen capture, pixel analysis, click simulation |
| `go/power/power_stub.go` | Existing stub (mock state for macOS) |
| `go/power/power.go` | Existing interface (may need minor updates) |
| `go/consumer/consumer.go` | Update to call power controller on SetPower actions |
| `go/main.go` | Wire power controller |
| `go/platform_windows.go` | Create power controller factory |
| `go/platform_stub.go` | Stub power controller factory |

## Exit Criteria

- Power toggle works from HID click (knob press)
- Power toggle works from REST API (`POST /api/power`)
- OFFLINE label detection correctly identifies powered-off speakers
- Power settling/cooldown blocks commands during transition
