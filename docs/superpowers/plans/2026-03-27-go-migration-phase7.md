# Go Migration Phase 7: Power Control via Pixel Detection

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect GLM power state via screen pixel analysis and toggle power by simulating mouse clicks on the GLM window. This enables power control from HID, REST API, and WebSocket.

**Architecture:** Windows-only syscalls via `golang.org/x/sys/windows` and `user32.dll`/`gdi32.dll`. Window found by enumerating JUCE class windows with "GLM" title. Screen captured via `BitBlt` from screen DC. Pixel analysis uses dual detection (OFFLINE gold labels + button color). Click via `SetCursorPos` + `mouse_event`. Consumer updated to use power controller for KindSetPower actions.

**Tech Stack:** Go 1.26, `golang.org/x/sys/windows` (already a dependency), `user32.dll`, `gdi32.dll` syscalls.

---

## File Map

| File | Purpose |
|------|---------|
| `go/power/power_windows.go` | Window finding, capture, pixel analysis, click, toggle (Windows only) |
| `go/power/power_stub.go` | Existing stub — no changes needed |
| `go/power/power.go` | Existing interface — no changes needed |
| `go/consumer/consumer.go` | Add power controller parameter for KindSetPower |
| `go/consumer/consumer_test.go` | Update Run() calls to pass nil power controller |
| `go/main.go` | Wire power controller, pass to consumer |
| `go/platform_windows.go` | Add createPowerController factory |
| `go/platform_stub.go` | Add createPowerController stub factory |

---

### Task 1: Implement Windows Power Controller

All Windows syscalls for window finding, screen capture, pixel analysis, click simulation, and toggle with verification.

**Files:**
- Create: `go/power/power_windows.go`

- [ ] **Step 1: Implement power_windows.go**

Create `go/power/power_windows.go`:
```go
//go:build windows

package power

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows API constants
const (
	SRCCOPY          = 0x00CC0020
	DIB_RGB_COLORS   = 0
	BI_RGB           = 0
	MOUSEEVENTF_LEFTDOWN = 0x0002
	MOUSEEVENTF_LEFTUP   = 0x0004
)

// Pixel detection thresholds (matching Python GlmPowerConfig)
const (
	offMaxBrightness  = 95  // Max pixel brightness for OFF state
	offMaxChannelDiff = 22  // Max RGB channel variation for OFF
	onMinGreen        = 110 // Min green channel for ON state
	onGreenRedDiff    = 35  // Min (G-R) difference for ON
	goldMinR          = 150 // OFFLINE gold label thresholds
	goldMinG          = 120
	goldMaxG          = 200
	goldMaxB          = 80
	goldThreshold     = 50  // Min gold pixels to confirm OFF
	patchRadius       = 4   // 9x9 pixel sample patch

	// Button position relative to window
	dxFromRight    = 28
	dyFromTop      = 80
	fallbackNudgeX = 8

	// Timing
	pollInterval   = 150 * time.Millisecond
	verifyTimeout  = 3 * time.Second
	postClickDelay = 350 * time.Millisecond
	focusDelay     = 150 * time.Millisecond
	hwndCacheTTL   = 5 * time.Second
)

// Windows API DLLs and procs
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procEnumWindows          = user32.NewProc("EnumWindows")
	procGetClassNameW        = user32.NewProc("GetClassNameW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowRect        = user32.NewProc("GetWindowRect")
	procGetDC                = user32.NewProc("GetDC")
	procReleaseDC            = user32.NewProc("ReleaseDC")
	procSetCursorPos         = user32.NewProc("SetCursorPos")
	procMouseEvent           = user32.NewProc("mouse_event")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
)

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

// WindowsController detects and toggles GLM power state via pixel analysis.
type WindowsController struct {
	log       *slog.Logger
	hwnd      uintptr
	hwndTime  time.Time
	mu        sync.Mutex
}

// NewWindowsController creates a power controller for GLM on Windows.
func NewWindowsController(log *slog.Logger) *WindowsController {
	return &WindowsController{log: log}
}

// GetState returns the current power state by pixel analysis.
func (c *WindowsController) GetState() (bool, error) {
	hwnd, err := c.findGLMWindow()
	if err != nil {
		return false, err
	}

	var r rect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return false, fmt.Errorf("GetWindowRect failed")
	}

	width := int(r.Right - r.Left)
	height := int(r.Bottom - r.Top)
	if width <= 0 || height <= 0 {
		return false, fmt.Errorf("invalid window size %dx%d", width, height)
	}

	pixels, err := c.captureScreen(int(r.Left), int(r.Top), width, height)
	if err != nil {
		return false, fmt.Errorf("screen capture: %w", err)
	}

	return c.analyzePixels(pixels, width, height)
}

// Toggle clicks the GLM power button and verifies the state changed.
func (c *WindowsController) Toggle() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	hwnd, err := c.findGLMWindow()
	if err != nil {
		return fmt.Errorf("find GLM window: %w", err)
	}

	var r rect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return fmt.Errorf("GetWindowRect failed")
	}

	// Get current state
	width := int(r.Right - r.Left)
	height := int(r.Bottom - r.Top)
	pixels, err := c.captureScreen(int(r.Left), int(r.Top), width, height)
	if err != nil {
		return fmt.Errorf("capture before click: %w", err)
	}

	currentState, err := c.analyzePixels(pixels, width, height)
	if err != nil {
		c.log.Warn("could not determine pre-click state, clicking anyway", "err", err)
	}
	desiredState := !currentState

	// Focus window and click
	procSetForegroundWindow.Call(hwnd)
	time.Sleep(focusDelay)

	btnX := int(r.Right) - dxFromRight
	btnY := int(r.Top) + dyFromTop

	c.log.Info("clicking power button", "x", btnX, "y", btnY, "current_state", currentState)

	procSetCursorPos.Call(uintptr(btnX), uintptr(btnY))
	time.Sleep(20 * time.Millisecond)
	procMouseEvent.Call(MOUSEEVENTF_LEFTDOWN, 0, 0, 0, 0)
	time.Sleep(20 * time.Millisecond)
	procMouseEvent.Call(MOUSEEVENTF_LEFTUP, 0, 0, 0, 0)

	// Wait for GLM to process the click
	time.Sleep(postClickDelay)

	// Poll until state changes
	deadline := time.Now().Add(verifyTimeout)
	for time.Now().Before(deadline) {
		// Re-read window rect in case it moved
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		width = int(r.Right - r.Left)
		height = int(r.Bottom - r.Top)

		pixels, err = c.captureScreen(int(r.Left), int(r.Top), width, height)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		newState, err := c.analyzePixels(pixels, width, height)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		if newState == desiredState {
			c.log.Info("power toggle verified", "new_state", newState)
			return nil
		}

		time.Sleep(pollInterval)
	}

	return fmt.Errorf("power toggle timeout: state did not change to %v within %v", desiredState, verifyTimeout)
}

// findGLMWindow finds the GLM JUCE window, using a cached HWND if fresh.
func (c *WindowsController) findGLMWindow() (uintptr, error) {
	if c.hwnd != 0 && time.Since(c.hwndTime) < hwndCacheTTL {
		// Verify cached HWND is still valid
		var r rect
		ret, _, _ := procGetWindowRect.Call(c.hwnd, uintptr(unsafe.Pointer(&r)))
		if ret != 0 {
			return c.hwnd, nil
		}
		c.hwnd = 0
	}

	var found uintptr
	cb := windows.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
		// Check class name starts with "JUCE_"
		className := make([]uint16, 256)
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&className[0])), 256)
		name := windows.UTF16ToString(className)
		if !strings.HasPrefix(name, "JUCE_") {
			return 1 // continue
		}

		// Check window title contains "GLM"
		title := make([]uint16, 256)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
		titleStr := windows.UTF16ToString(title)
		if !strings.Contains(titleStr, "GLM") {
			return 1 // continue
		}

		found = hwnd
		return 0 // stop enumeration
	})

	procEnumWindows.Call(cb, 0)

	if found == 0 {
		return 0, fmt.Errorf("GLM window not found")
	}

	c.hwnd = found
	c.hwndTime = time.Now()
	return found, nil
}

// captureScreen captures a region of the screen into a BGRA pixel buffer.
func (c *WindowsController) captureScreen(x, y, width, height int) ([]byte, error) {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(0) failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bitmap)

	procSelectObject.Call(memDC, bitmap)

	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(width), uintptr(height),
		screenDC, uintptr(x), uintptr(y),
		SRCCOPY,
	)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	// Read pixels via GetDIBits
	bmi := bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       int32(width),
		biHeight:      -int32(height), // negative = top-down
		biPlanes:      1,
		biBitCount:    32, // BGRA
		biCompression: BI_RGB,
	}

	pixels := make([]byte, width*height*4)
	ret, _, _ = procGetDIBits.Call(
		memDC, bitmap,
		0, uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bmi)),
		DIB_RGB_COLORS,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	return pixels, nil
}

// analyzePixels determines power state from BGRA pixel buffer.
// Returns (powerOn, error). Uses dual detection: OFFLINE labels (primary) + button color (fallback).
func (c *WindowsController) analyzePixels(pixels []byte, width, height int) (bool, error) {
	// Primary: check for OFFLINE gold labels in honeycomb region
	goldCount := c.countGoldPixels(pixels, width, height)

	if goldCount >= goldThreshold {
		c.log.Debug("OFFLINE labels detected", "gold_pixels", goldCount)
		return false, nil // OFF
	}
	if goldCount == 0 {
		// No gold pixels at all — likely ON, but verify with button
	}

	// Fallback: check power button pixel color
	btnX := width - dxFromRight
	btnY := dyFromTop

	if btnX < patchRadius || btnX >= width-patchRadius || btnY < patchRadius || btnY >= height-patchRadius {
		if goldCount == 0 {
			return true, nil // No gold, button out of bounds — assume ON
		}
		return false, fmt.Errorf("button position out of bounds and ambiguous gold count %d", goldCount)
	}

	state := c.analyzeButtonPatch(pixels, width, btnX, btnY)
	if state != "" {
		c.log.Debug("button state detected", "state", state, "gold_pixels", goldCount)
		return state == "on", nil
	}

	// Try fallback nudge position
	state = c.analyzeButtonPatch(pixels, width, btnX-fallbackNudgeX, btnY)
	if state != "" {
		c.log.Debug("button state detected (nudged)", "state", state, "gold_pixels", goldCount)
		return state == "on", nil
	}

	// Gold count as tiebreaker
	if goldCount == 0 {
		return true, nil
	}
	return false, fmt.Errorf("could not determine power state (gold=%d, button=unknown)", goldCount)
}

// countGoldPixels counts gold/amber pixels in the honeycomb region.
func (c *WindowsController) countGoldPixels(pixels []byte, width, height int) int {
	// Honeycomb region with insets: 15% left, 15% top, 25% right, 10% bottom
	x0 := width * 15 / 100
	y0 := height * 15 / 100
	x1 := width - width*25/100
	y1 := height - height*10/100

	count := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			offset := (y*width + x) * 4
			if offset+2 >= len(pixels) {
				continue
			}
			b := int(pixels[offset])
			g := int(pixels[offset+1])
			r := int(pixels[offset+2])

			if r > goldMinR && g > goldMinG && g < goldMaxG && b < goldMaxB {
				count++
			}
		}
	}
	return count
}

// analyzeButtonPatch checks a 9x9 patch around (cx, cy) for power button color.
// Returns "on", "off", or "" (unknown).
func (c *WindowsController) analyzeButtonPatch(pixels []byte, width, cx, cy int) string {
	var onCount, offCount, total int

	for dy := -patchRadius; dy <= patchRadius; dy++ {
		for dx := -patchRadius; dx <= patchRadius; dx++ {
			x := cx + dx
			y := cy + dy
			offset := (y*width + x) * 4
			if offset+2 >= len(pixels) || offset < 0 {
				continue
			}

			b := int(pixels[offset])
			g := int(pixels[offset+1])
			r := int(pixels[offset+2])
			total++

			maxC := r
			if g > maxC { maxC = g }
			if b > maxC { maxC = b }

			// OFF: dark grey
			channelDiff := max3(r, g, b) - min3(r, g, b)
			if maxC <= offMaxBrightness && channelDiff <= offMaxChannelDiff {
				offCount++
				continue
			}

			// ON: green/teal
			if g >= onMinGreen && (g-r) >= onGreenRedDiff {
				onCount++
			}
		}
	}

	if total == 0 {
		return ""
	}

	// Majority vote
	half := total / 2
	if onCount > half {
		return "on"
	}
	if offCount > half {
		return "off"
	}
	return ""
}

func max3(a, b, c int) int {
	if a >= b && a >= c { return a }
	if b >= c { return b }
	return c
}

func min3(a, b, c int) int {
	if a <= b && a <= c { return a }
	if b <= c { return b }
	return c
}
```

- [ ] **Step 2: Verify build on macOS**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build ./... && go vet ./...
```

Expected: builds clean (power_windows.go skipped by build tag).

- [ ] **Step 3: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/power/power_windows.go
git commit -m "feat(go): implement Windows power controller with pixel detection"
```

---

### Task 2: Update Consumer for Power Controller

Add optional `power.Controller` parameter to the consumer. When present, KindSetPower actions use pixel-based toggle instead of MIDI CC28.

**Files:**
- Modify: `go/consumer/consumer.go`
- Modify: `go/consumer/consumer_test.go`

- [ ] **Step 1: Update consumer.go**

Replace `go/consumer/consumer.go` with:
```go
package consumer

import (
	"context"
	"log/slog"
	"time"

	"vol20toglm/controller"
	"vol20toglm/midi"
	"vol20toglm/power"
	"vol20toglm/types"
)

const (
	MaxEventAge = 2.0 // seconds — discard actions older than this
)

// Run is the consumer goroutine. It reads actions from the channel,
// applies them to the controller, and sends the resulting MIDI messages.
// powerCtrl is optional (nil on non-Windows) — when present, power actions
// use pixel-based toggle instead of MIDI CC28.
func Run(ctx context.Context, actions <-chan types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, powerCtrl power.Controller, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-actions:
			processAction(a, ctrl, midiOut, midiChannel, powerCtrl, log)
		}
	}
}

func processAction(a types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, powerCtrl power.Controller, log *slog.Logger) {
	// Stale event filter
	age := time.Since(a.Timestamp).Seconds()
	if age > MaxEventAge {
		log.Warn("dropping stale action",
			"trace_id", a.TraceID,
			"kind", a.Kind.String(),
			"age_s", age,
		)
		return
	}

	// Power settling check
	if a.Kind == types.KindSetPower {
		allowed, wait, reason := ctrl.CanAcceptPowerCommand()
		if !allowed {
			log.Warn("power command blocked",
				"trace_id", a.TraceID,
				"reason", reason,
				"wait_s", wait,
			)
			return
		}

		// Use pixel-based power controller if available
		if powerCtrl != nil {
			log.Info("toggling power via UI automation", "trace_id", a.TraceID)
			ctrl.StartPowerTransition(!ctrl.GetState().Power, a.TraceID)
			if err := powerCtrl.Toggle(); err != nil {
				log.Error("power toggle failed", "trace_id", a.TraceID, "err", err)
				ctrl.EndPowerTransition(false, nil)
				return
			}
			// Verify state via pixel detection
			newState, err := powerCtrl.GetState()
			if err != nil {
				log.Warn("could not verify power state after toggle", "trace_id", a.TraceID, "err", err)
			}
			ctrl.EndPowerTransition(true, &newState)
			log.Info("power toggle complete", "trace_id", a.TraceID, "power", newState)
			return
		}

		// Fallback: send MIDI CC28 (no pixel detection available)
	}

	// Non-power actions: check settling
	if a.Kind != types.KindSetPower {
		allowed, wait, reason := ctrl.CanAcceptCommand()
		if !allowed {
			log.Warn("command blocked",
				"trace_id", a.TraceID,
				"kind", a.Kind.String(),
				"reason", reason,
				"wait_s", wait,
			)
			return
		}
	}

	// Special handling for AdjustVolume when volume not initialized
	if a.Kind == types.KindAdjustVolume && !ctrl.HasValidVolume() {
		cc := types.CCVolUp
		if a.Value < 0 {
			cc = types.CCVolDown
		}
		log.Debug("volume not initialized, using fallback",
			"trace_id", a.TraceID,
			"cc", cc,
		)
		if err := midiOut.SendCC(midiChannel, cc, 127); err != nil {
			log.Error("MIDI send failed", "trace_id", a.TraceID, "err", err)
		}
		return
	}

	// Apply action to controller — get MIDI CC + value
	cc, val, err := ctrl.ApplyAction(a)
	if err != nil {
		log.Error("apply action failed",
			"trace_id", a.TraceID,
			"kind", a.Kind.String(),
			"err", err,
		)
		return
	}

	// Send MIDI
	log.Debug("sending MIDI",
		"trace_id", a.TraceID,
		"cc", types.CCNames[cc],
		"cc_num", cc,
		"value", val,
	)
	if err := midiOut.SendCC(midiChannel, cc, val); err != nil {
		log.Error("MIDI send failed",
			"trace_id", a.TraceID,
			"cc", cc,
			"value", val,
			"err", err,
		)
	}
}
```

- [ ] **Step 2: Update consumer_test.go**

Read `go/consumer/consumer_test.go` and update all `Run()` calls to pass `nil` as the power controller (6th argument, before logger):

Every line like:
```go
go Run(ctx, actions, ctrl, mw, 0, log)
```
becomes:
```go
go Run(ctx, actions, ctrl, mw, 0, nil, log)
```

There should be 7 test functions, each with one `go Run(...)` call to update.

- [ ] **Step 3: Run tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./consumer/ -v -count=1
```

Expected: all 7 tests pass (nil power controller = MIDI fallback, same behavior as before).

- [ ] **Step 4: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/consumer/
git commit -m "feat(go): add power controller support to consumer goroutine"
```

---

### Task 3: Wire Power Controller in main.go

Create platform factory functions and pass power controller to consumer.

**Files:**
- Modify: `go/main.go`
- Modify: `go/platform_windows.go`
- Modify: `go/platform_stub.go`

- [ ] **Step 1: Add createPowerController to platform_windows.go**

Read current `go/platform_windows.go`, then add after the last function:
```go
func createPowerController(log *slog.Logger) power.Controller {
	return power.NewWindowsController(log.With("component", "power"))
}
```

Also add `"vol20toglm/power"` to the imports.

- [ ] **Step 2: Add createPowerController to platform_stub.go**

Read current `go/platform_stub.go`, then add after the last function:
```go
func createPowerController(log *slog.Logger) power.Controller {
	return &power.StubController{Log: log.With("component", "power"), State: true}
}
```

Also add `"vol20toglm/power"` to the imports.

- [ ] **Step 3: Update main.go**

Read current `go/main.go` and make these changes:

1. After MIDI input setup, add:
```go
	// Power controller — platform-specific
	powerCtrl := createPowerController(log)
```

2. Update the consumer.Run call to pass powerCtrl:
```go
		consumer.Run(ctx, actions, ctrl, midiOut, 0, powerCtrl, log.With("component", "consumer"))
```

3. Bump version to `"0.5.0"`

- [ ] **Step 4: Verify build and tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build -o vol20toglm . && go vet ./... && go test ./... -count=1
```

- [ ] **Step 5: Test run on macOS**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && timeout 2 ./vol20toglm --log_level DEBUG 2>&1 || true
```

Expected: starts with v0.5.0, power stub logs.

- [ ] **Step 6: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/
git commit -m "feat(go): wire power controller in main.go (v0.5.0)"
```

---

### Task 4: Test on Windows VM

Manual testing with real hardware and GLM.

- [ ] **Step 1: Push, pull, build**

```bash
# macOS:
git push

# Windows VM:
git pull
cd go
go build -o vol20toglm.exe .
```

- [ ] **Step 2: Test power state detection**

```cmd
vol20toglm.exe --log_level DEBUG
```

The power controller should find the GLM window on startup. Check debug log for "HID device connected" and "MIDI input started" as before.

- [ ] **Step 3: Test power toggle via HID**

Press the knob (single click = power toggle). Expected:
- `toggling power via UI automation`
- `clicking power button x=... y=...`
- `power toggle verified new_state=false`
- GLM speakers turn off

Press again to power on:
- `power toggle verified new_state=true`
- GLM speakers turn on

- [ ] **Step 4: Test power toggle via API**

```cmd
curl -X POST http://localhost:8080/api/power
```

Expected: same toggle flow, verified by pixel detection.

- [ ] **Step 5: Test OFFLINE label detection**

With speakers powered off, the OFFLINE gold labels should be visible. The pixel analysis should detect them:
- `OFFLINE labels detected gold_pixels=XXX`

- [ ] **Step 6: Troubleshoot if needed**

| Issue | Fix |
|-------|-----|
| `GLM window not found` | GLM not running, or window title doesn't contain "GLM" |
| `button position out of bounds` | GLM window too small or position constants need adjustment |
| `power toggle timeout` | Button coordinates wrong, or GLM didn't respond to click |
| `could not determine power state` | Pixel thresholds need tuning for this display/DPI |
| Build fails | Check `golang.org/x/sys/windows` version |

- [ ] **Step 7: Commit any fixes**

```bash
git add -A && git commit -m "fix(go): adjustments from Phase 7 Windows VM testing"
```

---

## Summary

After completing all 4 tasks:

- **Windows power controller** — window finding (JUCE + GLM title), screen capture (BitBlt), dual pixel analysis (OFFLINE gold labels + button color), mouse click simulation, toggle with verification polling
- **Consumer integration** — optional power controller for KindSetPower, falls back to MIDI CC28 when nil
- **Platform wiring** — factory functions in platform_*.go, version 0.5.0

**Exit criteria:**
- Power toggle works from HID click
- Power toggle works from REST API
- OFFLINE label detection identifies powered-off speakers
- Power settling/cooldown blocks commands during transition

**Next phase:** Phase 8 (RDP Priming + GLM Process Management)
