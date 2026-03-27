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

// Windows API constants.
const (
	srccopy           = 0x00CC0020
	dibRGBColors      = 0
	biRGB             = 0
	mouseeventfLeftDown = 0x0002
	mouseeventfLeftUp   = 0x0004
)

// Pixel analysis thresholds.
const (
	goldMinRed       = 150
	goldMinGreen     = 120
	goldMaxGreen     = 200
	goldMaxBlue      = 80
	goldCountOff     = 50
	offMaxBrightness = 95
	offMaxChannelDiff = 22
	onMinGreen       = 110
	onGreenRedDiff   = 35
)

// Timing constants.
const (
	pollInterval    = 150 * time.Millisecond
	verifyTimeout   = 3 * time.Second
	postClickDelay  = 350 * time.Millisecond
	clickDownUpDelay = 20 * time.Millisecond
	hwndCacheTTL    = 5 * time.Second
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

// WindowsController detects GLM power state via pixel analysis and toggles
// power by simulating mouse clicks on the GLM window.
type WindowsController struct {
	log        *slog.Logger
	mu         sync.Mutex
	cachedHWND uintptr
	cacheTime  time.Time
}

// NewWindowsController creates a new WindowsController.
func NewWindowsController(log *slog.Logger) *WindowsController {
	return &WindowsController{log: log}
}

// findGLMWindow locates the GLM window by enumerating top-level windows.
// It matches windows whose class name starts with "JUCE_" and whose title
// contains "GLM". The result is cached for hwndCacheTTL.
func (wc *WindowsController) findGLMWindow() (uintptr, error) {
	if wc.cachedHWND != 0 && time.Since(wc.cacheTime) < hwndCacheTTL {
		return wc.cachedHWND, nil
	}

	var foundHWND uintptr
	classNameBuf := make([]uint16, 256)
	windowTextBuf := make([]uint16, 256)

	callback := windows.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
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

// analyzePixels performs dual detection of the power state from a BGRA pixel
// buffer captured from the GLM window region.
//
// Primary: count gold-colored pixels in the honeycomb region.
// Fallback: sample a 9x9 patch at the expected button position.
func analyzePixels(pixels []byte, width, height int) pixelState {
	// Primary detection: gold pixels in honeycomb region.
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

	// Fallback detection: 9x9 patch at button position.
	return analyzeButtonPatch(pixels, width, height, 0)
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

// GetState returns the current power state of the GLM application.
// Returns true if GLM is powered on, false if powered off.
func (wc *WindowsController) GetState() (bool, error) {
	hwnd, err := wc.findGLMWindow()
	if err != nil {
		return false, err
	}

	windowRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return false, err
	}

	windowWidth := int(windowRect.Right - windowRect.Left)
	windowHeight := int(windowRect.Bottom - windowRect.Top)
	if windowWidth <= 0 || windowHeight <= 0 {
		return false, fmt.Errorf("invalid window dimensions: %dx%d", windowWidth, windowHeight)
	}

	pixels, err := wc.captureScreen(
		int(windowRect.Left), int(windowRect.Top),
		windowWidth, windowHeight,
	)
	if err != nil {
		return false, fmt.Errorf("screen capture failed: %w", err)
	}

	state := analyzePixels(pixels, windowWidth, windowHeight)
	switch state {
	case stateOn:
		return true, nil
	case stateOff:
		return false, nil
	default:
		return false, fmt.Errorf("unable to determine power state")
	}
}

// Toggle clicks the power button in the GLM window to change the power state.
// It captures the current state, performs the click, then polls until the
// state changes or a timeout is reached.
func (wc *WindowsController) Toggle() error {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	hwnd, err := wc.findGLMWindow()
	if err != nil {
		return fmt.Errorf("cannot toggle: %w", err)
	}

	// Capture current state before clicking.
	windowRect, err := wc.getWindowRect(hwnd)
	if err != nil {
		return fmt.Errorf("cannot get window rect: %w", err)
	}

	windowWidth := int(windowRect.Right - windowRect.Left)
	windowHeight := int(windowRect.Bottom - windowRect.Top)

	pixels, err := wc.captureScreen(
		int(windowRect.Left), int(windowRect.Top),
		windowWidth, windowHeight,
	)
	if err != nil {
		return fmt.Errorf("pre-toggle capture failed: %w", err)
	}

	previousState := analyzePixels(pixels, windowWidth, windowHeight)
	wc.log.Info("toggle: current state", "state", previousState)

	// Calculate button click position (absolute screen coordinates).
	clickX := int(windowRect.Right) - 28
	clickY := int(windowRect.Top) + 80

	// Focus window and click.
	procSetForegroundWindow.Call(hwnd) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)

	procSetCursorPos.Call(uintptr(clickX), uintptr(clickY)) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)

	procMouseEvent.Call(mouseeventfLeftDown, 0, 0, 0, 0) //nolint:errcheck
	time.Sleep(clickDownUpDelay)
	procMouseEvent.Call(mouseeventfLeftUp, 0, 0, 0, 0) //nolint:errcheck

	wc.log.Info("toggle: clicked power button", "x", clickX, "y", clickY)

	// Wait for state change.
	time.Sleep(postClickDelay)

	deadline := time.Now().Add(verifyTimeout)
	for time.Now().Before(deadline) {
		pixels, err = wc.captureScreen(
			int(windowRect.Left), int(windowRect.Top),
			windowWidth, windowHeight,
		)
		if err != nil {
			wc.log.Warn("toggle: verification capture failed", "error", err)
			time.Sleep(pollInterval)
			continue
		}

		currentState := analyzePixels(pixels, windowWidth, windowHeight)
		if currentState != stateUnknown && currentState != previousState {
			wc.log.Info("toggle: state changed", "from", previousState, "to", currentState)
			return nil
		}

		time.Sleep(pollInterval)
	}

	// If previous state was unknown, we can't verify — assume success.
	if previousState == stateUnknown {
		wc.log.Warn("toggle: could not verify state change (previous state unknown)")
		return nil
	}

	return fmt.Errorf("toggle: state did not change within %v", verifyTimeout)
}
