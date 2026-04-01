//go:build windows

package power

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"vol20toglm/rdp"
)

// Windows API constants.
const (
	srccopy             = 0x00CC0020
	dibRGBColors        = 0
	biRGB               = 0
	mouseeventfLeftDown = 0x0002
	mouseeventfLeftUp   = 0x0004

	swRestore      = 9
	swMinimize     = 6
	spiGetWorkArea = 0x0030
)

// Pixel analysis thresholds.
const (
	goldMinRed        = 150
	goldMinGreen      = 120
	goldMaxGreen      = 200
	goldMaxBlue       = 80
	goldCountOff      = 50
	offMaxBrightness  = 95
	offMaxChannelDiff = 22
	onMinGreen        = 110
	onGreenRedDiff    = 35
)

// Timing constants.
const (
	pollInterval      = 150 * time.Millisecond
	verifyTimeout     = 3 * time.Second
	postClickDelay    = 350 * time.Millisecond
	clickDownUpDelay  = 20 * time.Millisecond
	hwndCacheTTL      = 5 * time.Second
	powerPrepareDelay = 250 * time.Millisecond // Wait for GLM to repaint after window resize/move
)

// Windows API structs.
type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

// Lazy-loaded DLL procedures.
var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")
	procSetCursorPos             = user32.NewProc("SetCursorPos")
	procMouseEvent               = user32.NewProc("mouse_event")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procIsIconic                 = user32.NewProc("IsIconic")
	procShowWindow               = user32.NewProc("ShowWindow")
	procMoveWindow               = user32.NewProc("MoveWindow")
	procSystemParametersInfoW    = user32.NewProc("SystemParametersInfoW")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
)

// restoreInfo holds window state captured before pixel operations,
// allowing exact restoration afterward.
type restoreInfo struct {
	prevForeground  uintptr
	wasMinimized    bool
	wasRepositioned bool
	originalRect    rect
}

// WindowsController detects GLM power state via pixel analysis and toggles
// power by simulating mouse clicks on the GLM window.
type WindowsController struct {
	log           *slog.Logger
	mu            sync.Mutex
	pid           int
	cachedHWND    uintptr
	cacheTime     time.Time
	debugCaptures bool
}

// NewWindowsController creates a new WindowsController.
func NewWindowsController(log *slog.Logger, debugCaptures bool) *WindowsController {
	return &WindowsController{log: log, debugCaptures: debugCaptures}
}

// SetPID sets the GLM process ID for window filtering.
func (wc *WindowsController) SetPID(pid int) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.pid = pid
	wc.cachedHWND = 0 // invalidate cache when PID changes
	wc.cacheTime = time.Time{}
}

// findGLMWindowLocked locates the GLM window by enumerating top-level windows.
// It matches windows whose class name starts with "JUCE_" and whose title
// contains "GLM". When PID is set, only windows belonging to that process match.
// The result is cached for hwndCacheTTL.
// Caller must hold wc.mu.
func (wc *WindowsController) findGLMWindowLocked() (uintptr, error) {
	if wc.cachedHWND != 0 && time.Since(wc.cacheTime) < hwndCacheTTL {
		return wc.cachedHWND, nil
	}

	targetPID := wc.pid
	var foundHWND uintptr
	classNameBuf := make([]uint16, 256)
	windowTextBuf := make([]uint16, 256)

	callback := windows.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
		// Filter by PID if set.
		if targetPID > 0 {
			var windowPID uint32
			procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
			if int(windowPID) != targetPID {
				return 1 // wrong process, continue
			}
		}

		// Get class name.
		ret, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&classNameBuf[0])), 256)
		if ret == 0 {
			return 1 // continue enumeration
		}
		className := windows.UTF16ToString(classNameBuf[:ret])

		if !strings.HasPrefix(className, "JUCE_") {
			return 1
		}

		// Get window title.
		ret, _, _ = procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&windowTextBuf[0])), 256)
		if ret == 0 {
			return 1
		}
		windowTitle := windows.UTF16ToString(windowTextBuf[:ret])

		if strings.Contains(windowTitle, "GLM") {
			foundHWND = hwnd
			return 0 // stop enumeration
		}
		return 1
	})

	procEnumWindows.Call(callback, 0) //nolint:errcheck

	if foundHWND == 0 {
		wc.cachedHWND = 0
		return 0, fmt.Errorf("GLM window not found")
	}

	wc.cachedHWND = foundHWND
	wc.cacheTime = time.Now()
	return foundHWND, nil
}

// findGLMWindow is the mutex-acquiring wrapper around findGLMWindowLocked.
// Use findGLMWindowLocked when the caller already holds wc.mu.
func (wc *WindowsController) findGLMWindow() (uintptr, error) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.findGLMWindowLocked()
}

// captureScreen captures a rectangular region of the screen and returns
// a BGRA pixel buffer (top-down, 4 bytes per pixel).
func (wc *WindowsController) captureScreen(x, y, width, height int) ([]byte, error) {
	// Get screen device context.
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(0) failed")
	}
	defer procReleaseDC.Call(0, screenDC) //nolint:errcheck

	// Create compatible DC and bitmap.
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC) //nolint:errcheck

	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bitmap) //nolint:errcheck

	// Select bitmap into memory DC and BitBlt from screen.
	procSelectObject.Call(memDC, bitmap) //nolint:errcheck

	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(width), uintptr(height),
		screenDC, uintptr(x), uintptr(y),
		srccopy,
	)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	// Prepare BITMAPINFOHEADER for top-down 32-bit BGRA.
	header := bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(width),
		BiHeight:      -int32(height), // negative = top-down
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: biRGB,
	}

	// Allocate pixel buffer.
	bufSize := width * height * 4
	pixels := make([]byte, bufSize)

	ret, _, _ = procGetDIBits.Call(
		memDC, bitmap,
		0, uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&header)),
		dibRGBColors,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	return pixels, nil
}

// pixelState represents the detected power state from pixel analysis.
type pixelState int

const (
	stateUnknown pixelState = iota
	stateOn
	stateOff
)

func (s pixelState) String() string {
	switch s {
	case stateOn:
		return "ON"
	case stateOff:
		return "OFF"
	default:
		return "unknown"
	}
}

// pixelAnalysis holds the individual and combined results of pixel detection.
type pixelAnalysis struct {
	honeycomb pixelState
	button    pixelState
	combined  pixelState
}

// analyzePixels combines two independent detections of the power state from a
// BGRA pixel buffer captured from the GLM window region.
//
// Honeycomb: count gold-colored pixels (reliable when no speakers are OFFLINE).
// Button: sample a 9x9 patch at the power button position (has a known GLM
// startup rendering bug, but correct once the UI settles).
//
// When the two detectors disagree, button is trusted — it reflects the actual
// UI control state and is reliable post-startup. The honeycomb can lag behind
// during external power changes (e.g. RF remote) because GLM may not repaint
// the honeycomb region immediately.
func analyzePixels(pixels []byte, width, height int) pixelAnalysis {
	honeycomb := analyzeHoneycomb(pixels, width, height)
	button := analyzeButtonPatch(pixels, width, height, 0)

	var combined pixelState

	// Agreement → trust it.
	if honeycomb == button {
		combined = honeycomb
	} else if honeycomb == stateUnknown {
		// One is unknown → trust the other.
		combined = button
	} else if button == stateUnknown {
		combined = honeycomb
	} else {
		// Disagreement — trust button. It reflects the actual power control
		// state and is reliable once the UI has settled after startup.
		combined = button
	}

	return pixelAnalysis{honeycomb: honeycomb, button: button, combined: combined}
}

// analyzeHoneycomb counts gold-colored pixels in the honeycomb region.
// Returns stateOff if gold count exceeds threshold, stateOn if zero gold,
// or stateUnknown if ambiguous.
func analyzeHoneycomb(pixels []byte, width, height int) pixelState {
	// Insets: 15% left, 15% top, 25% right, 10% bottom.
	honeycombLeft := width * 15 / 100
	honeycombTop := height * 15 / 100
	honeycombRight := width - width*25/100
	honeycombBottom := height - height*10/100

	goldCount := 0
	for row := honeycombTop; row < honeycombBottom; row++ {
		for col := honeycombLeft; col < honeycombRight; col++ {
			offset := (row*width + col) * 4
			if offset+3 >= len(pixels) {
				continue
			}
			blue := pixels[offset]
			green := pixels[offset+1]
			red := pixels[offset+2]
			// Gold pixel: R>150, 120<G<200, B<80
			if red > goldMinRed && green > goldMinGreen && green < goldMaxGreen && blue < goldMaxBlue {
				goldCount++
			}
		}
	}

	if goldCount >= goldCountOff {
		return stateOff
	}
	if goldCount == 0 {
		return stateOn
	}
	return stateUnknown
}

// analyzeButtonPatch samples a 9x9 patch at the expected power button
// position and classifies it as ON (green), OFF (dark grey), or unknown.
// nudgeX allows shifting the sample point horizontally for a retry.
func analyzeButtonPatch(pixels []byte, width, height int, nudgeX int) pixelState {
	// Button position: right-28, top+80
	buttonX := width - 28 + nudgeX
	buttonY := 80

	if buttonX < 4 || buttonX >= width-4 || buttonY < 4 || buttonY+4 >= height {
		return stateUnknown
	}

	// Sample 9x9 patch centered on button position.
	var totalRed, totalGreen, totalBlue int
	sampleCount := 0
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			sampleX := buttonX + dx
			sampleY := buttonY + dy
			if sampleX < 0 || sampleX >= width || sampleY < 0 || sampleY >= height {
				continue
			}
			offset := (sampleY*width + sampleX) * 4
			if offset+3 >= len(pixels) {
				continue
			}
			totalBlue += int(pixels[offset])
			totalGreen += int(pixels[offset+1])
			totalRed += int(pixels[offset+2])
			sampleCount++
		}
	}

	if sampleCount == 0 {
		return stateUnknown
	}

	avgRed := totalRed / sampleCount
	avgGreen := totalGreen / sampleCount
	avgBlue := totalBlue / sampleCount

	// OFF = dark grey: max brightness <= 95, channel diff <= 22
	maxBrightness := avgRed
	if avgGreen > maxBrightness {
		maxBrightness = avgGreen
	}
	if avgBlue > maxBrightness {
		maxBrightness = avgBlue
	}

	minBrightness := avgRed
	if avgGreen < minBrightness {
		minBrightness = avgGreen
	}
	if avgBlue < minBrightness {
		minBrightness = avgBlue
	}

	channelDiff := maxBrightness - minBrightness

	if maxBrightness <= offMaxBrightness && channelDiff <= offMaxChannelDiff {
		return stateOff
	}

	// ON = green: G >= 110, G-R >= 35
	if avgGreen >= onMinGreen && avgGreen-avgRed >= onGreenRedDiff {
		return stateOn
	}

	// Try nudge position if this was the primary attempt.
	if nudgeX == 0 {
		return analyzeButtonPatch(pixels, width, height, -8)
	}

	return stateUnknown
}

// getWindowRect retrieves the screen coordinates of a window.
func (wc *WindowsController) getWindowRect(hwnd uintptr) (rect, error) {
	var windowRect rect
	ret, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&windowRect)))
	if ret == 0 {
		return rect{}, fmt.Errorf("GetWindowRect failed: %w", err)
	}
	return windowRect, nil
}

// prepareWindow ensures the GLM window is visible, on-screen, and in the
// foreground before pixel operations. It returns a restoreInfo that captures
// the previous window state so restoreWindow can undo every change.
// Caller must hold wc.mu.
func (wc *WindowsController) prepareWindow(hwnd uintptr) (restoreInfo, error) {
	var info restoreInfo

	// Ensure session is connected to console (handles RDP disconnect).
	if err := rdp.EnsureSessionConnected(wc.log); err != nil {
		return info, fmt.Errorf("session not connected: %w", err)
	}

	// Save current foreground window.
	info.prevForeground, _, _ = procGetForegroundWindow.Call()

	// Restore if minimized.
	isIconicRet, _, _ := procIsIconic.Call(hwnd)
	info.wasMinimized = isIconicRet != 0
	if info.wasMinimized {
		wc.log.Debug("prepareWindow: un-minimizing")
		procShowWindow.Call(hwnd, swRestore) //nolint:errcheck
		time.Sleep(100 * time.Millisecond)   // wait for window to fully restore
	}

	// Capture original position/size.
	originalRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return info, fmt.Errorf("GetWindowRect failed: %w", err)
	}
	info.originalRect = originalRect

	// Get available screen work area (excludes taskbar).
	var workArea rect
	ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&workArea)), 0)
	if ret == 0 {
		wc.log.Warn("prepareWindow: SystemParametersInfoW failed, skipping bounds check")
		wc.log.Debug("prepareWindow: bringing to foreground")
		procSetForegroundWindow.Call(hwnd) //nolint:errcheck
		time.Sleep(100 * time.Millisecond)
		return info, nil
	}

	// Validate work area is reasonable.
	workAreaWidth := int(workArea.Right - workArea.Left)
	workAreaHeight := int(workArea.Bottom - workArea.Top)
	if workAreaWidth <= 0 || workAreaHeight <= 0 {
		wc.log.Warn("prepareWindow: invalid work area, skipping bounds check", "workArea", workArea)
		wc.log.Debug("prepareWindow: bringing to foreground")
		procSetForegroundWindow.Call(hwnd) //nolint:errcheck
		time.Sleep(100 * time.Millisecond)
		return info, nil
	}

	windowWidth := int(originalRect.Right - originalRect.Left)
	windowHeight := int(originalRect.Bottom - originalRect.Top)

	wc.log.Debug("prepareWindow: window rect after restore",
		"left", originalRect.Left, "top", originalRect.Top,
		"right", originalRect.Right, "bottom", originalRect.Bottom,
		"width", windowWidth, "height", windowHeight,
		"workArea_left", workArea.Left, "workArea_top", workArea.Top,
		"workArea_right", workArea.Right, "workArea_bottom", workArea.Bottom,
	)

	// Determine whether the window needs repositioning or resizing.
	targetX := int(originalRect.Left)
	targetY := int(originalRect.Top)
	targetW := windowWidth
	targetH := windowHeight
	needsAdjustment := false

	if windowWidth > workAreaWidth {
		targetW = workAreaWidth * 80 / 100
		needsAdjustment = true
	}
	if windowHeight > workAreaHeight {
		targetH = workAreaHeight * 80 / 100
		needsAdjustment = true
	}

	// Check if any edge of the window extends beyond the work area.
	// The power button is at width-28 (right edge) and pixel analysis
	// needs the full window visible, so partial off-screen is not acceptable.
	if int(originalRect.Left) < int(workArea.Left) ||
		int(originalRect.Top) < int(workArea.Top) ||
		int(originalRect.Right) > int(workArea.Right) ||
		int(originalRect.Bottom) > int(workArea.Bottom) {
		targetX = int(workArea.Left)
		targetY = int(workArea.Top)
		needsAdjustment = true
	}

	// Bring window to foreground first — GPU/display driver gives foreground
	// windows rendering priority, so subsequent resize repaints faster.
	wc.log.Debug("prepareWindow: bringing to foreground")
	procSetForegroundWindow.Call(hwnd) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)

	if needsAdjustment {
		wc.log.Debug("prepareWindow: repositioning (off-screen/oversized)",
			"original", originalRect,
			"targetX", targetX, "targetY", targetY,
			"targetW", targetW, "targetH", targetH,
		)
		procMoveWindow.Call(
			hwnd,
			uintptr(targetX), uintptr(targetY),
			uintptr(targetW), uintptr(targetH),
			1, // repaint = TRUE
		) //nolint:errcheck
		time.Sleep(powerPrepareDelay)
		info.wasRepositioned = true
	}

	return info, nil
}

// restoreWindow undoes every change made by prepareWindow, in reverse order.
// Caller must hold wc.mu.
func (wc *WindowsController) restoreWindow(hwnd uintptr, info restoreInfo) {
	if info.wasRepositioned {
		wc.log.Debug("restoreWindow: repositioning back to original", "rect", info.originalRect)
		ret, _, _ := procMoveWindow.Call(
			hwnd,
			uintptr(info.originalRect.Left), uintptr(info.originalRect.Top),
			uintptr(info.originalRect.Right-info.originalRect.Left),
			uintptr(info.originalRect.Bottom-info.originalRect.Top),
			1, // repaint = TRUE
		)
		if ret == 0 {
			wc.log.Warn("restoreWindow: MoveWindow failed")
		}
	}
	// Restore foreground BEFORE re-minimizing — while GLM is still the
	// foreground window we have "permission" to call SetForegroundWindow.
	// After minimizing, we lose foreground status and the call may fail.
	if info.prevForeground != 0 {
		wc.log.Debug("restoreWindow: restoring foreground")
		ret, _, _ := procSetForegroundWindow.Call(info.prevForeground)
		if ret == 0 {
			// Common: Windows' foreground lock timer expires during our ~1.5s operation.
			// The previous window is still there, just not explicitly focused. Benign.
			wc.log.Debug("restoreWindow: SetForegroundWindow failed (foreground lock expired)")
		}
	}
	if info.wasMinimized {
		wc.log.Debug("restoreWindow: re-minimizing")
		ret, _, _ := procShowWindow.Call(hwnd, swMinimize)
		if ret == 0 {
			wc.log.Warn("restoreWindow: ShowWindow(minimize) failed")
		}
	}
}

// readStateLocked captures the current pixel state of the GLM window.
// windowRect must already be post-prepare (accurate current position/size).
// Caller must hold wc.mu.
func (wc *WindowsController) readStateLocked(windowRect rect) (pixelState, error) {
	windowWidth := int(windowRect.Right - windowRect.Left)
	windowHeight := int(windowRect.Bottom - windowRect.Top)
	if windowWidth <= 0 || windowHeight <= 0 {
		return stateUnknown, fmt.Errorf("invalid window dimensions: %dx%d", windowWidth, windowHeight)
	}

	pixels, err := wc.captureScreen(
		int(windowRect.Left), int(windowRect.Top),
		windowWidth, windowHeight,
	)
	if err != nil {
		return stateUnknown, fmt.Errorf("screen capture failed: %w", err)
	}

	analysis := analyzePixels(pixels, windowWidth, windowHeight)

	if analysis.honeycomb != analysis.button {
		wc.log.Warn("pixel detectors disagree",
			"honeycomb", analysis.honeycomb,
			"button", analysis.button,
			"combined", analysis.combined,
		)
	}

	// Debug: dump captured pixels to BMP file for inspection.
	if wc.debugCaptures {
		wc.dumpPixelsBMP(pixels, windowWidth, windowHeight, analysis.combined)
	}

	return analysis.combined, nil
}

// dumpPixelsBMP writes the BGRA pixel buffer as a BMP file next to the binary.
// Filename includes timestamp and detected state for easy identification.
func (wc *WindowsController) dumpPixelsBMP(pixels []byte, width, height int, state pixelState) {
	stateName := "unknown"
	switch state {
	case stateOn:
		stateName = "ON"
	case stateOff:
		stateName = "OFF"
	}

	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("glm_capture_%s_%s_%dx%d.bmp", ts, stateName, width, height)

	// BMP file: 14-byte file header + 40-byte DIB header + pixel data
	rowSize := width * 4 // BGRA, already 4-byte aligned
	pixelDataSize := rowSize * height
	fileSize := 14 + 40 + pixelDataSize

	buf := make([]byte, fileSize)

	// File header (14 bytes)
	buf[0], buf[1] = 'B', 'M'
	buf[2] = byte(fileSize)
	buf[3] = byte(fileSize >> 8)
	buf[4] = byte(fileSize >> 16)
	buf[5] = byte(fileSize >> 24)
	buf[10] = 54 // pixel data offset (14 + 40)

	// DIB header (BITMAPINFOHEADER, 40 bytes)
	buf[14] = 40 // header size
	buf[18] = byte(width)
	buf[19] = byte(width >> 8)
	buf[20] = byte(width >> 16)
	buf[21] = byte(width >> 24)
	// Height negative = top-down; BMP uses positive = bottom-up, so flip
	buf[22] = byte(height)
	buf[23] = byte(height >> 8)
	buf[24] = byte(height >> 16)
	buf[25] = byte(height >> 24)
	buf[26] = 1  // planes
	buf[28] = 32 // bits per pixel
	buf[34] = byte(pixelDataSize)
	buf[35] = byte(pixelDataSize >> 8)
	buf[36] = byte(pixelDataSize >> 16)
	buf[37] = byte(pixelDataSize >> 24)

	// Pixel data: our buffer is top-down BGRA, BMP expects bottom-up
	for y := 0; y < height; y++ {
		srcRow := y * rowSize
		dstRow := (height - 1 - y) * rowSize
		copy(buf[54+dstRow:54+dstRow+rowSize], pixels[srcRow:srcRow+rowSize])
	}

	if err := os.WriteFile(filename, buf, 0644); err != nil {
		wc.log.Warn("dumpPixelsBMP: failed to write", "file", filename, "err", err)
	} else {
		wc.log.Debug("dumpPixelsBMP: saved capture", "file", filename, "state", stateName)
	}
}

// clickPowerButtonLocked clicks the power button and polls until the state
// changes from previousState, or verifyTimeout elapses.
// windowRect must be accurate (post-prepare).
// Caller must hold wc.mu.
func (wc *WindowsController) clickPowerButtonLocked(windowRect rect, previousState pixelState) error {
	// Calculate button click position (absolute screen coordinates).
	clickX := int(windowRect.Right) - 28
	clickY := int(windowRect.Top) + 80

	// Window is already in foreground (prepareWindow called SetForegroundWindow).
	procSetCursorPos.Call(uintptr(clickX), uintptr(clickY)) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)

	procMouseEvent.Call(mouseeventfLeftDown, 0, 0, 0, 0) //nolint:errcheck
	time.Sleep(clickDownUpDelay)
	procMouseEvent.Call(mouseeventfLeftUp, 0, 0, 0, 0) //nolint:errcheck

	wc.log.Info("clickPowerButton: clicked power button", "x", clickX, "y", clickY)

	// Wait for state change.
	time.Sleep(postClickDelay)

	deadline := time.Now().Add(verifyTimeout)
	for time.Now().Before(deadline) {
		currentState, err := wc.readStateLocked(windowRect)
		if err != nil {
			wc.log.Warn("clickPowerButton: verification capture failed", "error", err)
			time.Sleep(pollInterval)
			continue
		}

		if currentState != stateUnknown && currentState != previousState {
			wc.log.Info("clickPowerButton: state changed", "from", previousState, "to", currentState)
			return nil
		}

		time.Sleep(pollInterval)
	}

	// If previous state was unknown, we can't verify — assume success.
	if previousState == stateUnknown {
		wc.log.Warn("clickPowerButton: could not verify state change (previous state unknown)")
		return nil
	}

	return fmt.Errorf("clickPowerButton: state did not change within %v", verifyTimeout)
}

// GetState returns the current power state of the GLM application.
// Returns true if GLM is powered on, false if powered off.
// The full prepare→capture→analyze→restore sequence runs under wc.mu.
func (wc *WindowsController) GetState() (bool, error) {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	hwnd, err := wc.findGLMWindowLocked()
	if err != nil {
		return false, err
	}

	info, err := wc.prepareWindow(hwnd)
	if err != nil {
		return false, fmt.Errorf("prepare window failed: %w", err)
	}
	defer wc.restoreWindow(hwnd, info)

	// Re-read the window rect — it may have changed after prepare.
	windowRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return false, err
	}

	state, err := wc.readStateLocked(windowRect)
	if err != nil {
		return false, err
	}

	switch state {
	case stateOn:
		return true, nil
	case stateOff:
		return false, nil
	default:
		return false, fmt.Errorf("unable to determine power state")
	}
}

// GetPowerState returns the current power state. Implements Observer.
func (wc *WindowsController) GetPowerState() (bool, error) {
	return wc.GetState()
}

// PowerOn ensures power is ON via UI click. Implements Commander.
// If power is already on, the click is skipped (idempotent).
// Holds wc.mu for the entire read-check-click-verify sequence to eliminate TOCTOU.
func (wc *WindowsController) PowerOn(traceID string) error {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	hwnd, err := wc.findGLMWindowLocked()
	if err != nil {
		return fmt.Errorf("cannot find GLM window: %w", err)
	}

	info, err := wc.prepareWindow(hwnd)
	if err != nil {
		return fmt.Errorf("prepare window failed: %w", err)
	}
	defer wc.restoreWindow(hwnd, info)

	windowRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return fmt.Errorf("cannot get window rect: %w", err)
	}

	currentState, err := wc.readStateLocked(windowRect)
	if err != nil {
		return fmt.Errorf("cannot read state: %w", err)
	}

	if currentState == stateOn {
		wc.log.Info("power already ON, skipping UI click", "trace_id", traceID)
		return nil
	}

	wc.log.Info("power ON via UI click", "trace_id", traceID)
	return wc.clickPowerButtonLocked(windowRect, currentState)
}

// PowerOff ensures power is OFF via UI click. Implements Commander.
// If power is already off, the click is skipped (idempotent).
// Holds wc.mu for the entire read-check-click-verify sequence to eliminate TOCTOU.
func (wc *WindowsController) PowerOff(traceID string) error {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	hwnd, err := wc.findGLMWindowLocked()
	if err != nil {
		return fmt.Errorf("cannot find GLM window: %w", err)
	}

	info, err := wc.prepareWindow(hwnd)
	if err != nil {
		return fmt.Errorf("prepare window failed: %w", err)
	}
	defer wc.restoreWindow(hwnd, info)

	windowRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return fmt.Errorf("cannot get window rect: %w", err)
	}

	currentState, err := wc.readStateLocked(windowRect)
	if err != nil {
		return fmt.Errorf("cannot read state: %w", err)
	}

	if currentState == stateOff {
		wc.log.Info("power already OFF, skipping UI click", "trace_id", traceID)
		return nil
	}

	wc.log.Info("power OFF via UI click", "trace_id", traceID)
	return wc.clickPowerButtonLocked(windowRect, currentState)
}

// Toggle clicks the power button in the GLM window to change the power state.
// It prepares the window, captures the current state, performs the click, then
// polls until the state changes or a timeout is reached, then restores the window.
func (wc *WindowsController) Toggle() error {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	hwnd, err := wc.findGLMWindowLocked()
	if err != nil {
		return fmt.Errorf("cannot toggle: %w", err)
	}

	info, err := wc.prepareWindow(hwnd)
	if err != nil {
		return fmt.Errorf("prepare window failed: %w", err)
	}
	defer wc.restoreWindow(hwnd, info)

	// Re-read window rect after prepare (position/size may have changed).
	windowRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return fmt.Errorf("cannot get window rect: %w", err)
	}

	previousState, err := wc.readStateLocked(windowRect)
	if err != nil {
		return fmt.Errorf("pre-toggle capture failed: %w", err)
	}

	wc.log.Info("toggle: current state", "state", previousState)
	return wc.clickPowerButtonLocked(windowRect, previousState)
}
