//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"vol20toglm/bootflag"
	"vol20toglm/config"
	"vol20toglm/glm"
	"vol20toglm/hid"
	applog "vol20toglm/logging"
	"vol20toglm/midi"
	"vol20toglm/power"
	"vol20toglm/rdp"
	"vol20toglm/types"
)

func createMIDIWriter(cfg config.Config, ctx context.Context, log *slog.Logger) midi.Writer {
	// MIDIInChannel = GLM's input port (where we WRITE to)
	retryLog := applog.NewRetryLogger(nil)
	for {
		w, err := midi.OpenWinMMWriter(cfg.MIDIInChannel, log)
		if err == nil {
			if retryLog.RetryCount("midi_out") > 0 {
				log.Info("MIDI output connected after retrying", "port", cfg.MIDIInChannel, "attempts", retryLog.RetryCount("midi_out"))
			}
			return w
		}
		if retryLog.ShouldLog("midi_out") {
			log.Warn("MIDI output not found, retrying",
				"port", cfg.MIDIInChannel,
				"err", err,
				"info", retryLog.RetryInfo("midi_out"),
			)
		}
		select {
		case <-ctx.Done():
			log.Error("MIDI output not available, using stub", "port", cfg.MIDIInChannel)
			return &midi.StubWriter{Log: log}
		case <-time.After(5 * time.Second):
		}
	}
}

func createHIDReader(cfg config.Config, accel *hid.AccelerationHandler, traceGen *types.TraceIDGenerator, log *slog.Logger) hid.Reader {
	return &hid.USBReader{
		VID:      cfg.VID,
		PID:      cfg.PID,
		Bindings: types.DefaultBindings,
		Accel:    accel,
		TraceGen: traceGen,
		Log:      log.With("component", "hid"),
	}
}

func createMIDIReader(cfg config.Config, ctx context.Context, log *slog.Logger) midi.Reader {
	// MIDIOutChannel = GLM's output port (where we READ from)
	retryLog := applog.NewRetryLogger(nil)
	for {
		r, err := midi.OpenWinMMReader(cfg.MIDIOutChannel, log)
		if err == nil {
			if retryLog.RetryCount("midi_in") > 0 {
				log.Info("MIDI input connected after retrying", "port", cfg.MIDIOutChannel, "attempts", retryLog.RetryCount("midi_in"))
			}
			return r
		}
		if retryLog.ShouldLog("midi_in") {
			log.Warn("MIDI input not found, retrying",
				"port", cfg.MIDIOutChannel,
				"err", err,
				"info", retryLog.RetryInfo("midi_in"),
			)
		}
		select {
		case <-ctx.Done():
			log.Error("MIDI input not available, using stub", "port", cfg.MIDIOutChannel)
			return &midi.StubReader{Log: log}
		case <-time.After(5 * time.Second):
		}
	}
}

func createPowerController(log *slog.Logger, debugCaptures bool) power.Controller {
	return power.NewWindowsController(log.With("component", "power"), debugCaptures)
}

func createGLMManager(cfg config.Config, log *slog.Logger) glm.Manager {
	return glm.NewWindowsManager(cfg.GLMPath, cfg.GLMCPUGating, log.With("component", "glm"))
}

func listDevices() {
	fmt.Println("=== MIDI Output Ports (write to GLM) ===")
	outPorts := midi.ListOutputPorts()
	if len(outPorts) == 0 {
		fmt.Println("  (none found)")
	}
	for i, name := range outPorts {
		fmt.Printf("  [%d] %s\n", i, name)
	}

	fmt.Println("\n=== MIDI Input Ports (read from GLM) ===")
	inPorts := midi.ListInputPorts()
	if len(inPorts) == 0 {
		fmt.Println("  (none found)")
	}
	for i, name := range inPorts {
		fmt.Printf("  [%d] %s\n", i, name)
	}

	fmt.Println("\n=== HID Devices ===")
	listHIDDevices()
}

// Known USB vendor IDs for identification when device strings are unavailable.
var knownVendors = map[uint16]string{
	0x07D7: "Griffin Technology",
	0x1AF4: "Red Hat / VirtIO (VM virtual device)",
	0x1781: "Genelec",
	0x045E: "Microsoft",
	0x046D: "Logitech",
	0x1532: "Razer",
	0x04F2: "Chicony Electronics",
	0x0B05: "ASUS",
	0x2516: "Cooler Master",
	0x258A: "SINO WEALTH (generic keyboard/mouse)",
}

func listHIDDevices() {
	var guidHID = windows.GUID{
		Data1: 0x4D1E55B2, Data2: 0xF16F, Data3: 0x11CF,
		Data4: [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30},
	}

	setupapi := windows.NewLazySystemDLL("setupapi.dll")
	hidDll := windows.NewLazySystemDLL("hid.dll")

	procSetupDiGetClassDevsW := setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces := setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW := setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList := setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	procHidD_GetAttributes := hidDll.NewProc("HidD_GetAttributes")
	procHidD_GetProductString := hidDll.NewProc("HidD_GetProductString")
	procHidD_GetManufacturerString := hidDll.NewProc("HidD_GetManufacturerString")

	type spDeviceInterfaceData struct {
		cbSize             uint32
		interfaceClassGuid windows.GUID
		flags              uint32
		reserved           uintptr
	}
	type hiddAttributes struct {
		Size          uint32
		VendorID      uint16
		ProductID     uint16
		VersionNumber uint16
	}

	devInfoSet, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guidHID)), 0, 0, 0x02|0x10,
	)
	if devInfoSet == uintptr(windows.InvalidHandle) {
		fmt.Println("  (failed to enumerate HID devices)")
		return
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devInfoSet)

	var ifData spDeviceInterfaceData
	ifData.cbSize = uint32(unsafe.Sizeof(ifData))

	count := 0
	seen := make(map[string]bool)

	for i := uint32(0); ; i++ {
		ret, _, _ := procSetupDiEnumDeviceInterfaces.Call(
			devInfoSet, 0, uintptr(unsafe.Pointer(&guidHID)), uintptr(i), uintptr(unsafe.Pointer(&ifData)),
		)
		if ret == 0 {
			break
		}

		// Get device path
		var requiredSize uint32
		procSetupDiGetDeviceInterfaceDetailW.Call(devInfoSet, uintptr(unsafe.Pointer(&ifData)), 0, 0, uintptr(unsafe.Pointer(&requiredSize)), 0)
		if requiredSize == 0 {
			continue
		}
		buf := make([]byte, requiredSize)
		cbSize := uint32(8)
		if unsafe.Sizeof(uintptr(0)) == 4 {
			cbSize = 6
		}
		*(*uint32)(unsafe.Pointer(&buf[0])) = cbSize
		ret, _, _ = procSetupDiGetDeviceInterfaceDetailW.Call(devInfoSet, uintptr(unsafe.Pointer(&ifData)), uintptr(unsafe.Pointer(&buf[0])), uintptr(requiredSize), 0, 0)
		if ret == 0 {
			continue
		}

		pathSlice := (*[4096]uint16)(unsafe.Pointer(&buf[4]))
		devicePath := windows.UTF16ToString(pathSlice[:])

		// Open device
		pathPtr, _ := windows.UTF16PtrFromString(devicePath)
		handle, err := windows.CreateFile(pathPtr, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}

		// Get VID/PID
		var attrs hiddAttributes
		attrs.Size = uint32(unsafe.Sizeof(attrs))
		ret, _, _ = procHidD_GetAttributes.Call(uintptr(handle), uintptr(unsafe.Pointer(&attrs)))
		if ret == 0 {
			windows.CloseHandle(handle)
			continue
		}

		key := fmt.Sprintf("%04x:%04x", attrs.VendorID, attrs.ProductID)
		if seen[key] {
			windows.CloseHandle(handle)
			continue
		}
		seen[key] = true

		// Get product name
		productBuf := make([]uint16, 128)
		ret, _, _ = procHidD_GetProductString.Call(uintptr(handle), uintptr(unsafe.Pointer(&productBuf[0])), 128*2)
		product := ""
		if ret != 0 {
			product = strings.TrimRight(windows.UTF16ToString(productBuf), "\x00")
		}

		// Get manufacturer
		mfgBuf := make([]uint16, 128)
		ret, _, _ = procHidD_GetManufacturerString.Call(uintptr(handle), uintptr(unsafe.Pointer(&mfgBuf[0])), 128*2)
		manufacturer := ""
		if ret != 0 {
			manufacturer = strings.TrimRight(windows.UTF16ToString(mfgBuf), "\x00")
		}

		windows.CloseHandle(handle)

		count++
		fmt.Printf("  [%d] VID=0x%04X PID=0x%04X", count, attrs.VendorID, attrs.ProductID)

		// Show manufacturer (from device or known vendor table)
		if manufacturer != "" {
			fmt.Printf("  Manufacturer=%q", manufacturer)
		} else if known, ok := knownVendors[attrs.VendorID]; ok {
			fmt.Printf("  Vendor=%q", known)
		}
		if product != "" {
			fmt.Printf("  Product=%q", product)
		}
		fmt.Println()
		fmt.Printf("        Path: %s\n", devicePath)
	}

	if count == 0 {
		fmt.Println("  (none found)")
	}
}

func setProcessPriority(log *slog.Logger) {
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		log.Warn("could not get current process handle", "err", err)
		return
	}
	// ABOVE_NORMAL_PRIORITY_CLASS = 0x8000
	ret, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetPriorityClass").Call(
		uintptr(handle), 0x8000,
	)
	if ret == 0 {
		log.Warn("SetPriorityClass failed", "err", err)
	} else {
		log.Info("process priority set to AboveNormal")
	}
}

func runStartupTasks(cfg config.Config, log *slog.Logger) {
	// RDP priming must complete before MIDI restart — the MIDI virtual ports
	// depend on a stable desktop session, and RDP priming disconnects/reconnects it.
	if cfg.RDPPriming {
		primer := &rdp.WindowsPrimer{Log: log.With("component", "rdp")}
		if primer.NeedsPriming() {
			if err := primer.Prime(); err != nil {
				log.Error("RDP priming failed", "err", err)
			}
		}
	}

	// MIDI service restart (once per boot)
	if cfg.MIDIRestart {
		if bootflag.NeedsRun("midi_restarted.flag", log) {
			log.Info("restarting Windows MIDI service")
			exec.Command("net", "stop", "midisrv").Run()
			time.Sleep(1 * time.Second)
			exec.Command("net", "start", "midisrv").Run()
			time.Sleep(1 * time.Second)
			log.Info("MIDI service restarted")
		}
	}
}

