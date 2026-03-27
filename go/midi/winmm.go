//go:build windows

package midi

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"vol20toglm/types"
)

var (
	winmm              = syscall.NewLazyDLL("winmm.dll")
	midiOutGetNumDevs  = winmm.NewProc("midiOutGetNumDevs")
	midiOutGetDevCapsW = winmm.NewProc("midiOutGetDevCapsW")
	midiOutOpen        = winmm.NewProc("midiOutOpen")
	midiOutClose       = winmm.NewProc("midiOutClose")
	midiOutShortMsg    = winmm.NewProc("midiOutShortMsg")
)

// midiOutCaps mirrors the Windows MIDIOUTCAPSW structure (simplified).
type midiOutCaps struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [32]uint16 // WCHAR[32]
	wTechnology    uint16
	wVoices        uint16
	wNotes         uint16
	wChannelMask   uint16
	dwSupport      uint32
}

// WinMMWriter sends MIDI messages via the Windows Multimedia API.
type WinMMWriter struct {
	mu     sync.Mutex
	handle uintptr
	log    *slog.Logger
}

// OpenWinMMWriter opens a MIDI output port by name substring match.
func OpenWinMMWriter(portName string, log *slog.Logger) (*WinMMWriter, error) {
	numDevs, _, _ := midiOutGetNumDevs.Call()
	if numDevs == 0 {
		return nil, fmt.Errorf("no MIDI output devices found")
	}

	portNameLower := strings.ToLower(portName)
	for i := uintptr(0); i < numDevs; i++ {
		var caps midiOutCaps
		ret, _, _ := midiOutGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret != 0 {
			continue
		}

		deviceName := syscall.UTF16ToString(caps.szPname[:])
		if strings.Contains(strings.ToLower(deviceName), portNameLower) {
			var handle uintptr
			ret, _, err := midiOutOpen.Call(
				uintptr(unsafe.Pointer(&handle)),
				i,
				0, 0, 0,
			)
			if ret != 0 {
				return nil, fmt.Errorf("midiOutOpen failed for %q: %v", deviceName, err)
			}
			log.Info("MIDI output opened", "port", deviceName, "device_id", i)
			return &WinMMWriter{handle: handle, log: log}, nil
		}
	}

	return nil, fmt.Errorf("MIDI output port %q not found", portName)
}

// SendCC sends a MIDI Control Change message.
// Message format: status | (cc << 8) | (value << 16)
// Status byte: 0xB0 | channel (channel is 0-15)
func (w *WinMMWriter) SendCC(channel, cc, value int, traceID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	ccName := types.CCNames[cc]
	if ccName == "" {
		ccName = fmt.Sprintf("CC%d", cc)
	}
	w.log.Debug("MIDI send", "cc", ccName, "cc_num", cc, "value", value, "channel", channel, "trace_id", traceID)

	statusByte := 0xB0 | (channel & 0x0F)
	midiMessage := uintptr(statusByte) | uintptr(cc&0x7F)<<8 | uintptr(value&0x7F)<<16

	ret, _, err := midiOutShortMsg.Call(w.handle, midiMessage)
	if ret != 0 {
		return fmt.Errorf("midiOutShortMsg failed: %v", err)
	}
	return nil
}

// Close closes the MIDI output port.
func (w *WinMMWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.handle != 0 {
		ret, _, err := midiOutClose.Call(w.handle)
		if ret != 0 {
			return fmt.Errorf("midiOutClose failed: %v", err)
		}
		w.handle = 0
	}
	return nil
}

// ListOutputPorts returns names of all available MIDI output ports.
func ListOutputPorts() []string {
	numDevs, _, _ := midiOutGetNumDevs.Call()
	outputPorts := make([]string, 0, numDevs)
	for i := uintptr(0); i < numDevs; i++ {
		var caps midiOutCaps
		ret, _, _ := midiOutGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret == 0 {
			outputPorts = append(outputPorts, syscall.UTF16ToString(caps.szPname[:]))
		}
	}
	return outputPorts
}
