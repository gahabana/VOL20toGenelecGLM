//go:build windows

package hid

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	applog "vol20toglm/logging"
	"vol20toglm/types"
)

// GUID_DEVINTERFACE_HID = {4D1E55B2-F16F-11CF-88CB-001111000030}
var guidDevinterfaceHID = windows.GUID{
	Data1: 0x4D1E55B2,
	Data2: 0xF16F,
	Data3: 0x11CF,
	Data4: [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30},
}

var (
	setupapi = windows.NewLazySystemDLL("setupapi.dll")
	hidDll   = windows.NewLazySystemDLL("hid.dll")

	procSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	procHidD_GetAttributes               = hidDll.NewProc("HidD_GetAttributes")
)

const (
	digcfPresent         = 0x02
	digcfDeviceInterface = 0x10

	readTimeoutMs = 1000
	retryDelay    = 5 * time.Second
	maxReportSize = 64 // Typical max HID report size
)

// SP_DEVICE_INTERFACE_DATA
type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGuid windows.GUID
	flags              uint32
	reserved           uintptr
}

// HIDD_ATTRIBUTES
type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

// hidDevice wraps a Windows file handle for a HID device.
type hidDevice struct {
	handle windows.Handle
}

func (d *hidDevice) Close() error {
	return windows.CloseHandle(d.handle)
}

// USBReader reads HID events from a VOL20 USB device.
type USBReader struct {
	VID      uint16
	PID      uint16
	Bindings map[int]types.ActionKind
	Accel    *AccelerationHandler
	TraceGen *types.TraceIDGenerator
	Log      *slog.Logger
}

// Run opens the HID device and reads reports in a loop until ctx is cancelled.
// Retries every 5s if the device is not found, with exponential backoff on log messages.
func (r *USBReader) Run(ctx context.Context, actions chan<- types.Action) error {
	for {
		// Try to connect, with exponential log backoff while waiting
		dev, err := r.connectWithBackoff(ctx)
		if err != nil {
			return err // ctx cancelled
		}

		r.Log.Info("HID device connected",
			"vid", fmt.Sprintf("0x%04x", r.VID),
			"pid", fmt.Sprintf("0x%04x", r.PID),
		)

		err = r.readLoop(ctx, dev, actions)
		dev.Close()

		if ctx.Err() != nil {
			return ctx.Err()
		}

		r.Log.Warn("HID device disconnected, reconnecting", "err", err)
	}
}

// connectWithBackoff retries opening the HID device every 5s.
// Log messages throttled via time-based milestones (2s, 10s, 1m, 10m, 1h, 1d).
func (r *USBReader) connectWithBackoff(ctx context.Context) (*hidDevice, error) {
	retryLog := applog.NewRetryLogger(nil)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		dev, err := r.openDevice()
		if err == nil {
			if retryLog.RetryCount("hid") > 0 {
				r.Log.Info("HID device found after retrying", "attempts", retryLog.RetryCount("hid"))
			}
			return dev, nil
		}

		if retryLog.ShouldLog("hid") {
			r.Log.Warn("HID device not found",
				"vid", fmt.Sprintf("0x%04x", r.VID),
				"pid", fmt.Sprintf("0x%04x", r.PID),
				"err", err,
				"info", retryLog.RetryInfo("hid"),
			)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}

func (r *USBReader) openDevice() (*hidDevice, error) {
	devInfoSet, _, err := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guidDevinterfaceHID)),
		0,
		0,
		digcfPresent|digcfDeviceInterface,
	)
	if devInfoSet == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevs: %w", err)
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devInfoSet)

	var ifData spDeviceInterfaceData
	ifData.cbSize = uint32(unsafe.Sizeof(ifData))

	for i := uint32(0); ; i++ {
		ret, _, _ := procSetupDiEnumDeviceInterfaces.Call(
			devInfoSet,
			0,
			uintptr(unsafe.Pointer(&guidDevinterfaceHID)),
			uintptr(i),
			uintptr(unsafe.Pointer(&ifData)),
		)
		if ret == 0 {
			break
		}

		devicePath, pathErr := getDevicePath(devInfoSet, &ifData)
		if pathErr != nil {
			continue
		}

		pathPtr, pathErr := windows.UTF16PtrFromString(devicePath)
		if pathErr != nil {
			continue
		}

		handle, openErr := windows.CreateFile(
			pathPtr,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED,
			0,
		)
		if openErr != nil {
			continue
		}

		var attrs hiddAttributes
		attrs.Size = uint32(unsafe.Sizeof(attrs))
		ret, _, _ = procHidD_GetAttributes.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&attrs)),
		)
		if ret == 0 {
			windows.CloseHandle(handle)
			continue
		}

		if attrs.VendorID == r.VID && attrs.ProductID == r.PID {
			return &hidDevice{handle: handle}, nil
		}

		windows.CloseHandle(handle)
	}

	return nil, fmt.Errorf("device %04x:%04x not found", r.VID, r.PID)
}

func getDevicePath(devInfoSet uintptr, ifData *spDeviceInterfaceData) (string, error) {
	var requiredSize uint32
	procSetupDiGetDeviceInterfaceDetailW.Call(
		devInfoSet,
		uintptr(unsafe.Pointer(ifData)),
		0,
		0,
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if requiredSize == 0 {
		return "", fmt.Errorf("failed to get detail size")
	}

	buf := make([]byte, requiredSize)
	// cbSize for SP_DEVICE_INTERFACE_DETAIL_DATA_W:
	// 8 on 64-bit Windows (4 byte DWORD + 2 byte WCHAR + 2 padding)
	// 6 on 32-bit Windows (4 byte DWORD + 2 byte WCHAR)
	cbSize := uint32(4 + unsafe.Sizeof(uint16(0)))
	if unsafe.Sizeof(uintptr(0)) == 8 {
		cbSize = 8
	}
	*(*uint32)(unsafe.Pointer(&buf[0])) = cbSize

	ret, _, err := procSetupDiGetDeviceInterfaceDetailW.Call(
		devInfoSet,
		uintptr(unsafe.Pointer(ifData)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(requiredSize),
		0,
		0,
	)
	if ret == 0 {
		return "", fmt.Errorf("SetupDiGetDeviceInterfaceDetail: %w", err)
	}

	// Device path starts at offset 4 (after cbSize DWORD)
	pathSlice := (*[4096]uint16)(unsafe.Pointer(&buf[4]))
	return windows.UTF16ToString(pathSlice[:]), nil
}

func (r *USBReader) readLoop(ctx context.Context, dev *hidDevice, actions chan<- types.Action) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("CreateEvent: %w", err)
	}
	defer windows.CloseHandle(event)

	buf := make([]byte, maxReportSize)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var overlapped windows.Overlapped
		overlapped.HEvent = event

		var bytesRead uint32
		readErr := windows.ReadFile(dev.handle, buf, &bytesRead, &overlapped)
		if readErr != nil && readErr != windows.ERROR_IO_PENDING {
			return fmt.Errorf("ReadFile: %w", readErr)
		}

		result, _ := windows.WaitForSingleObject(event, uint32(readTimeoutMs))
		if result == uint32(windows.WAIT_TIMEOUT) {
			windows.CancelIo(dev.handle)
			windows.ResetEvent(event)
			continue
		}
		if result != windows.WAIT_OBJECT_0 {
			windows.CancelIo(dev.handle)
			return fmt.Errorf("WaitForSingleObject: unexpected result %d", result)
		}

		err = windows.GetOverlappedResult(dev.handle, &overlapped, &bytesRead, true)
		if err != nil {
			return fmt.Errorf("GetOverlappedResult: %w", err)
		}

		if bytesRead == 0 {
			continue
		}

		// Windows HID ReadFile prepends a report ID byte.
		// Skip it — the actual keycode is in buf[1].
		var keycode int
		if bytesRead > 1 {
			keycode = int(buf[1])
		} else {
			keycode = int(buf[0])
		}

		if keycode != 0 {
			name := types.KeyNames[keycode]
			if name == "" {
				name = fmt.Sprintf("unknown(%d)", keycode)
			}
			r.Log.Debug("HID report", "keycode", keycode, "key", name, "bytes_read", bytesRead)
		}

		ProcessReport(keycode, time.Now(), r.Bindings, r.Accel, r.TraceGen, actions)
	}
}
