# Go Migration Phase 4: MIDI Reader (Feedback Loop + Power Pattern Detection)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Read MIDI CC messages from GLM so the controller knows the true volume/mute/dim state. Detect GLM's 5-message power toggle pattern from the RF remote. This completes the feedback loop — the "volume not initialized" fallback goes away and acceleration works with absolute volume.

**Architecture:** winmm.dll `midiInOpen` with `syscall.NewCallback` drops CC messages into a buffered channel (never blocks). A reader goroutine drains the channel, updates the controller, and feeds messages to a power pattern detector. The pattern detector is platform-independent and fully testable on macOS.

**Tech Stack:** Go 1.26, `winmm.dll` via `syscall` (same pattern as existing writer), existing `controller` and `types` packages.

**Critical constraint:** The winmm callback runs on a Windows system thread. It MUST be minimal — extract bytes, non-blocking channel send, return. No allocation, no logging, no locks.

---

## File Map

| File | Purpose |
|------|---------|
| `go/controller/power_pattern.go` | Platform-independent power pattern detector (state machine) |
| `go/controller/power_pattern_test.go` | Tests for pattern detection, timing, startup suppression |
| `go/midi/winmm_reader.go` | Windows MIDI input via winmm.dll syscalls (new file) |
| `go/main.go` | Wire MIDI reader + power pattern detector |
| `go/platform_windows.go` | Add `createMIDIReader` factory |
| `go/platform_stub.go` | Add `createMIDIReader` stub factory |

---

### Task 1: Implement Power Pattern Detector

Platform-independent state machine that watches incoming MIDI CC messages and detects GLM's power toggle pattern. Fully testable on macOS.

**Files:**
- Create: `go/controller/power_pattern.go`
- Create: `go/controller/power_pattern_test.go`

- [ ] **Step 1: Write power pattern tests**

Create `go/controller/power_pattern_test.go`:
```go
package controller

import (
	"testing"

	"vol20toglm/types"
)

func TestPowerPattern_ExactMatch(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	// Simulate GLM power toggle: Mute→Vol→Dim→Mute→Vol
	// Each message 60ms apart (total 240ms) — within timing constraints
	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCDim, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	if !detected {
		t.Error("power pattern should have been detected")
	}
}

func TestPowerPattern_WrongSequence(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCMute, 0, base+0.12) // Wrong — should be Dim
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	if detected {
		t.Error("wrong sequence should not trigger pattern")
	}
}

func TestPowerPattern_TooSlow(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	// Each gap 300ms — exceeds MaxGap (260ms)
	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.30)
	pp.Feed(types.CCDim, 0, base+0.60)
	pp.Feed(types.CCMute, 0, base+0.90)
	pp.Feed(types.CCVolumeAbs, 50, base+1.20)

	if detected {
		t.Error("too-slow pattern should not trigger")
	}
}

func TestPowerPattern_TooFast_BufferDump(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	// Total span 30ms — below MinSpan (50ms), looks like buffer dump
	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.007)
	pp.Feed(types.CCDim, 0, base+0.015)
	pp.Feed(types.CCMute, 0, base+0.022)
	pp.Feed(types.CCVolumeAbs, 50, base+0.030)

	if detected {
		t.Error("buffer dump (too fast) should not trigger")
	}
}

func TestPowerPattern_NoPreGap(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	base := 1000.0
	// Send a normal CC just 50ms before the pattern (below PreGap 120ms)
	pp.Feed(types.CCVolumeAbs, 60, base)

	pp.Feed(types.CCMute, 0, base+0.05)
	pp.Feed(types.CCVolumeAbs, 50, base+0.11)
	pp.Feed(types.CCDim, 0, base+0.17)
	pp.Feed(types.CCMute, 0, base+0.23)
	pp.Feed(types.CCVolumeAbs, 50, base+0.29)

	if detected {
		t.Error("pattern without sufficient pre-gap should not trigger")
	}
}

func TestPowerPattern_StartupSuppression(t *testing.T) {
	count := 0
	pp := NewPowerPatternDetector(func() {
		count++
	})

	// First pattern — should fire
	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCDim, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	if count != 1 {
		t.Fatalf("first pattern: count = %d, want 1", count)
	}

	// Second pattern 1s later — within StartupWindow (3s), suppress
	base2 := base + 1.0
	pp.Feed(types.CCMute, 0, base2)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.06)
	pp.Feed(types.CCDim, 0, base2+0.12)
	pp.Feed(types.CCMute, 0, base2+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.24)

	if count != 1 {
		t.Errorf("startup suppression: count = %d, want 1 (second pattern within 3s should be suppressed)", count)
	}
}

func TestPowerPattern_TwoPatterns_FarApart(t *testing.T) {
	count := 0
	pp := NewPowerPatternDetector(func() {
		count++
	})

	// First pattern
	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCDim, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	// Second pattern 5s later — outside StartupWindow, should fire
	base2 := base + 5.0
	pp.Feed(types.CCMute, 0, base2)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.06)
	pp.Feed(types.CCDim, 0, base2+0.12)
	pp.Feed(types.CCMute, 0, base2+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.24)

	if count != 2 {
		t.Errorf("two far-apart patterns: count = %d, want 2", count)
	}
}

func TestPowerPattern_TotalGapExceeded(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	// Each gap ~100ms, total 400ms — exceeds MaxTotal (350ms)
	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.10)
	pp.Feed(types.CCDim, 0, base+0.20)
	pp.Feed(types.CCMute, 0, base+0.30)
	pp.Feed(types.CCVolumeAbs, 50, base+0.40)

	if detected {
		t.Error("pattern with total gaps > 350ms should not trigger")
	}
}

func TestPowerPattern_ResetAfterFailure(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func() {
		detected = true
	})

	base := 1000.0
	// Start a pattern that fails (wrong CC at position 3)
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCVolumeAbs, 50, base+0.12) // Wrong — resets

	// Then a valid pattern with enough pre-gap
	base2 := base + 0.5
	pp.Feed(types.CCMute, 0, base2)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.06)
	pp.Feed(types.CCDim, 0, base2+0.12)
	pp.Feed(types.CCMute, 0, base2+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.24)

	if !detected {
		t.Error("valid pattern after failed one should trigger")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./controller/ -v -run TestPowerPattern
```

Expected: FAIL — `NewPowerPatternDetector` not defined.

- [ ] **Step 3: Implement power_pattern.go**

Create `go/controller/power_pattern.go`:
```go
package controller

import (
	"vol20toglm/types"
)

// PowerPatternDetector watches incoming MIDI CC messages for GLM's
// 5-message power toggle pattern (Mute→Vol→Dim→Mute→Vol).
// When detected, calls the onDetected callback.
// NOT thread-safe — caller must serialize calls to Feed().
type PowerPatternDetector struct {
	onDetected func()

	// Pattern matching state
	buf       [5]ccEvent
	pos       int // Next position to fill (0-4)
	lastTime  float64 // Timestamp of most recent Feed() call (any CC)

	// Startup suppression
	lastPatternTime float64 // When last pattern was detected
}

type ccEvent struct {
	cc    int
	value int
	time  float64
}

// NewPowerPatternDetector creates a detector that calls onDetected
// when the power pattern is recognized.
func NewPowerPatternDetector(onDetected func()) *PowerPatternDetector {
	return &PowerPatternDetector{onDetected: onDetected}
}

// Feed processes an incoming MIDI CC message. Call for every CC received.
func (d *PowerPatternDetector) Feed(cc, value int, timestamp float64) {
	defer func() { d.lastTime = timestamp }()

	expected := types.PowerPattern[d.pos]

	if cc != expected {
		// Does this CC match the START of the pattern?
		if cc == types.PowerPattern[0] {
			d.buf[0] = ccEvent{cc, value, timestamp}
			d.pos = 1
		} else {
			d.pos = 0
		}
		return
	}

	d.buf[d.pos] = ccEvent{cc, value, timestamp}
	d.pos++

	if d.pos < len(types.PowerPattern) {
		return
	}

	// Full sequence collected — validate timing
	d.pos = 0

	first := d.buf[0]
	last := d.buf[len(types.PowerPattern)-1]

	// Check total span
	totalSpan := last.time - first.time
	if totalSpan > types.PowerPatternWindow {
		return
	}
	if totalSpan < types.PowerPatternMinSpan {
		return // Buffer dump — too fast
	}

	// Check individual gaps and total gap sum
	totalGaps := 0.0
	for i := 1; i < len(types.PowerPattern); i++ {
		gap := d.buf[i].time - d.buf[i-1].time
		if gap > types.PowerPatternMaxGap {
			return
		}
		totalGaps += gap
	}
	if totalGaps > types.PowerPatternMaxTotal {
		return
	}

	// Check pre-gap (silence before pattern)
	if d.lastTime > 0 && d.lastTime != first.time {
		preGap := first.time - d.lastTime
		if preGap < types.PowerPatternPreGap {
			return
		}
	}

	// Startup suppression: second pattern within 3s = GLM startup, ignore
	if d.lastPatternTime > 0 {
		sinceLastPattern := first.time - d.lastPatternTime
		if sinceLastPattern < types.PowerStartupWindow {
			d.lastPatternTime = first.time
			return
		}
	}

	d.lastPatternTime = first.time
	d.onDetected()
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./controller/ -v -run TestPowerPattern
```

Expected: PASS (all 9 tests).

- [ ] **Step 5: Run all tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./... -count=1
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/controller/power_pattern.go go/controller/power_pattern_test.go
git commit -m "feat(go): implement power pattern detector with 9 tests"
```

---

### Task 2: Implement MIDI Input Reader (winmm.dll)

Windows MIDI input using direct syscalls. The callback is minimal — extracts CC bytes and does a non-blocking send to a buffered channel. A goroutine drains the channel and calls the user-provided ReaderCallback.

**Files:**
- Create: `go/midi/winmm_reader.go` (Windows only, `//go:build windows`)

- [ ] **Step 1: Implement winmm_reader.go**

Create `go/midi/winmm_reader.go`:
```go
//go:build windows

package midi

import (
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"unsafe"
)

var (
	midiInGetNumDevs  = winmm.NewProc("midiInGetNumDevs")
	midiInGetDevCapsW = winmm.NewProc("midiInGetDevCapsW")
	midiInOpen        = winmm.NewProc("midiInOpen")
	midiInStart       = winmm.NewProc("midiInStart")
	midiInStop        = winmm.NewProc("midiInStop")
	midiInClose       = winmm.NewProc("midiInClose")
)

const (
	mimData          = 0x3C3 // MIM_DATA: short MIDI message received
	callbackFunction = 0x30000
	midiInBufSize    = 256
)

// midiInCaps mirrors MIDIINCAPSW (simplified).
type midiInCaps struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16
	dwSupport      uint32
}

// midiMsg is a parsed CC message from the callback.
type midiMsg struct {
	channel int
	cc      int
	value   int
}

// Package-level channel for the callback. Only one MIDI reader per process.
var globalMidiInCh chan midiMsg

// midiInProc is the winmm callback. Runs on a Windows system thread.
// MUST be minimal: extract bytes, non-blocking send, return.
func midiInProc(hMidiIn uintptr, msg uint32, instance uintptr, param1 uintptr, param2 uintptr) uintptr {
	if msg == mimData {
		status := byte(param1 & 0xFF)
		// Only CC messages (0xB0-0xBF)
		if status >= 0xB0 && status <= 0xBF {
			m := midiMsg{
				channel: int(status & 0x0F),
				cc:      int((param1 >> 8) & 0xFF),
				value:   int((param1 >> 16) & 0xFF),
			}
			select {
			case globalMidiInCh <- m:
			default:
				// Drop if buffer full — never block the system thread
			}
		}
	}
	return 0
}

// WinMMReader reads MIDI input via the Windows Multimedia API.
type WinMMReader struct {
	handle uintptr
	log    *slog.Logger
	done   chan struct{}
}

// OpenWinMMReader opens a MIDI input port by name substring match.
func OpenWinMMReader(portName string, log *slog.Logger) (*WinMMReader, error) {
	numDevs, _, _ := midiInGetNumDevs.Call()
	if numDevs == 0 {
		return nil, fmt.Errorf("no MIDI input devices found")
	}

	portNameLower := strings.ToLower(portName)
	for i := uintptr(0); i < numDevs; i++ {
		var caps midiInCaps
		ret, _, _ := midiInGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret != 0 {
			continue
		}

		deviceName := syscall.UTF16ToString(caps.szPname[:])
		if strings.Contains(strings.ToLower(deviceName), portNameLower) {
			// Set up the global channel before opening
			globalMidiInCh = make(chan midiMsg, midiInBufSize)

			cbPtr := syscall.NewCallback(midiInProc)

			var handle uintptr
			ret, _, err := midiInOpen.Call(
				uintptr(unsafe.Pointer(&handle)),
				i,
				cbPtr,
				0,
				callbackFunction,
			)
			if ret != 0 {
				return nil, fmt.Errorf("midiInOpen failed for %q: %v", deviceName, err)
			}

			log.Info("MIDI input opened", "port", deviceName, "device_id", i)
			return &WinMMReader{handle: handle, log: log, done: make(chan struct{})}, nil
		}
	}

	return nil, fmt.Errorf("MIDI input port %q not found", portName)
}

// Start begins reading MIDI messages and calling cb for each CC received.
// Blocks until Close() is called. Must be called from a goroutine.
func (r *WinMMReader) Start(cb ReaderCallback) error {
	ret, _, err := midiInStart.Call(r.handle)
	if ret != 0 {
		return fmt.Errorf("midiInStart failed: %v", err)
	}

	r.log.Info("MIDI input started")

	for {
		select {
		case msg := <-globalMidiInCh:
			cb(msg.channel, msg.cc, msg.value)
		case <-r.done:
			return nil
		}
	}
}

// Close stops MIDI input and closes the port.
func (r *WinMMReader) Close() error {
	// Signal the Start goroutine to exit
	select {
	case <-r.done:
		// Already closed
	default:
		close(r.done)
	}

	midiInStop.Call(r.handle)
	// midiInReset drains pending buffers before close
	midiInReset := winmm.NewProc("midiInReset")
	midiInReset.Call(r.handle)

	ret, _, err := midiInClose.Call(r.handle)
	if ret != 0 {
		return fmt.Errorf("midiInClose failed: %v", err)
	}
	r.log.Info("MIDI input closed")
	return nil
}

// ListInputPorts returns names of all available MIDI input ports.
func ListInputPorts() []string {
	numDevs, _, _ := midiInGetNumDevs.Call()
	ports := make([]string, 0, numDevs)
	for i := uintptr(0); i < numDevs; i++ {
		var caps midiInCaps
		ret, _, _ := midiInGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret == 0 {
			ports = append(ports, syscall.UTF16ToString(caps.szPname[:]))
		}
	}
	return ports
}
```

- [ ] **Step 2: Verify build on macOS**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build ./... && go vet ./...
```

Expected: builds clean (winmm_reader.go skipped by build tag).

- [ ] **Step 3: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/midi/winmm_reader.go
git commit -m "feat(go): add Windows MIDI input reader via winmm.dll syscalls"
```

---

### Task 3: Wire MIDI Reader + Power Pattern in main.go

Connect the MIDI reader to the controller and power pattern detector.

**Files:**
- Modify: `go/main.go`
- Modify: `go/platform_windows.go`
- Modify: `go/platform_stub.go`

- [ ] **Step 1: Add createMIDIReader to platform_windows.go**

Add to `go/platform_windows.go` after the existing `createHIDReader` function:
```go
func createMIDIReader(cfg config.Config, log *slog.Logger) midi.Reader {
	// MIDIOutChannel = GLM's output port (where we READ from)
	r, err := midi.OpenWinMMReader(cfg.MIDIOutChannel, log)
	if err != nil {
		log.Error("failed to open MIDI input", "port", cfg.MIDIOutChannel, "err", err)
		return &midi.StubReader{Log: log}
	}
	return r
}
```

- [ ] **Step 2: Add createMIDIReader to platform_stub.go**

Add to `go/platform_stub.go` after the existing `createHIDReader` function:
```go
func createMIDIReader(cfg config.Config, log *slog.Logger) midi.Reader {
	return &midi.StubReader{Log: log.With("component", "midi-in")}
}
```

- [ ] **Step 3: Update main.go**

Replace `go/main.go` with:
```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"vol20toglm/config"
	"vol20toglm/consumer"
	"vol20toglm/controller"
	"vol20toglm/hid"
	"vol20toglm/types"
)

const version = "0.3.0"

func main() {
	cfg := config.Parse(os.Args[1:])

	var logLevel slog.Level
	switch cfg.LogLevel {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "INFO":
		logLevel = slog.LevelInfo
	default:
		logLevel = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	fmt.Printf("vol20toglm v%s\n", version)
	log.Info("starting",
		"version", version,
		"vid", fmt.Sprintf("0x%04x", cfg.VID),
		"pid", fmt.Sprintf("0x%04x", cfg.PID),
		"midi_in", cfg.MIDIInChannel,
		"midi_out", cfg.MIDIOutChannel,
		"api_port", cfg.APIPort,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Core components
	ctrl := controller.New()
	traceGen := types.NewTraceIDGenerator()
	actions := make(chan types.Action, 100)

	// MIDI output — platform-specific, created in platform_*.go
	midiOut := createMIDIWriter(cfg, log)
	if midiOut != nil {
		defer midiOut.Close()
	}

	// MIDI input — platform-specific
	midiIn := createMIDIReader(cfg, log)
	defer midiIn.Close()

	// Power pattern detector
	midiLog := log.With("component", "midi-in")
	powerDetector := controller.NewPowerPatternDetector(func() {
		newPower := ctrl.TogglePowerFromMIDIPattern()
		midiLog.Info("power pattern detected", "new_power_state", newPower)
	})

	// Acceleration handler
	accel := hid.NewAccelerationHandler(cfg.MinClickTime, cfg.MaxAvgClickTime, cfg.VolumeIncreases)

	// HID reader — platform-specific
	hidReader := createHIDReader(cfg, accel, traceGen, log)

	var wg sync.WaitGroup

	// Start consumer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if midiOut == nil {
			log.Warn("no MIDI output, consumer running in dry-run mode")
		}
		consumer.Run(ctx, actions, ctrl, midiOut, 0, log.With("component", "consumer"))
	}()

	// Start HID reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := hidReader.Run(ctx, actions); err != nil && ctx.Err() == nil {
			log.Error("HID reader exited with error", "err", err)
		}
	}()

	// Start MIDI input reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := midiIn.Start(func(channel, cc, value int) {
			now := float64(time.Now().UnixMilli()) / 1000.0

			ccName := types.CCNames[cc]
			if ccName == "" {
				ccName = fmt.Sprintf("CC%d", cc)
			}
			midiLog.Debug("MIDI recv", "cc", ccName, "cc_num", cc, "value", value, "channel", channel)

			// Update controller state
			changed := ctrl.UpdateFromMIDI(cc, value)
			if changed {
				midiLog.Debug("state updated from MIDI", "cc", ccName, "value", value)
			}

			// Feed to power pattern detector
			powerDetector.Feed(cc, value, now)
		})
		if err != nil && ctx.Err() == nil {
			log.Error("MIDI reader exited with error", "err", err)
		}
	}()

	log.Info("running — press Ctrl+C to stop")
	<-ctx.Done()
	log.Info("shutting down")

	cancel()
	midiIn.Close()
	wg.Wait()
	log.Info("shutdown complete")
}
```

- [ ] **Step 4: Verify build and tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build -o vol20toglm . && go vet ./... && go test ./... -count=1
```

Expected: builds, vet clean, all tests pass.

- [ ] **Step 5: Test run on macOS**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && timeout 2 ./vol20toglm --log_level DEBUG 2>&1 || true
```

Expected: startup with MIDI stub warnings, clean shutdown.

- [ ] **Step 6: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/
git commit -m "feat(go): wire MIDI reader + power pattern detector in main.go"
```

---

### Task 4: Test on Windows VM

Manual hardware testing. Build and verify on the actual Windows VM.

- [ ] **Step 1: Push, pull, build**

```bash
# On macOS:
git push

# On Windows VM:
git pull
cd go
go build -o vol20toglm.exe .
```

- [ ] **Step 2: Test MIDI feedback**

```cmd
vol20toglm.exe --log_level DEBUG
```

Turn the knob. Expected:
- `MIDI recv cc=Volume value=XX` — GLM sends back volume feedback
- After first feedback: `state updated from MIDI cc=Volume` — volume initialized
- Subsequent knob turns: `sending MIDI cc=Volume value=XX` — absolute volume, no more fallback
- Volume acceleration works (fast turns = bigger jumps)

- [ ] **Step 3: Test power pattern**

Change volume directly in GLM UI. Expected:
- `MIDI recv cc=Volume value=XX` — state stays in sync

Use the RF remote to toggle power (or trigger the pattern from GLM). Expected:
- `power pattern detected new_power_state=false` — pattern recognized

- [ ] **Step 4: Test mute/dim from GLM**

Toggle mute/dim in GLM UI. Expected:
- `MIDI recv cc=Mute value=127` / `MIDI recv cc=Dim value=127`
- `state updated from MIDI cc=Mute`

- [ ] **Step 5: Commit any fixes**

```bash
git add -A && git commit -m "fix(go): adjustments from Phase 4 Windows VM testing"
```

---

## Summary

After completing all 4 tasks:

- **Power pattern detector** — 9 tests covering exact match, wrong sequence, timing (too slow, too fast, no pre-gap, total gaps exceeded), startup suppression, reset after failure, two patterns far apart
- **MIDI input reader** — winmm.dll syscalls with minimal callback + buffered channel
- **main.go wiring** — MIDI reader goroutine feeds controller + power pattern detector
- **Tested on Windows VM** — volume feedback, acceleration, power pattern, mute/dim sync

**New tests:** 9 (power pattern detector)
**Running total:** ~49 tests across packages

**Exit criteria:** Change volume in GLM → Go binary sees the new value. RF remote power toggle → detected.

**Next phase:** Phase 5 (REST API + WebSocket)
