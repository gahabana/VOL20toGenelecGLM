//go:build windows

package midi

import (
	"fmt"
	"log/slog"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	midiInGetNumDevs  = winmm.NewProc("midiInGetNumDevs")
	midiInGetDevCapsW = winmm.NewProc("midiInGetDevCapsW")
	midiInOpen        = winmm.NewProc("midiInOpen")
	midiInStart       = winmm.NewProc("midiInStart")
	midiInStop        = winmm.NewProc("midiInStop")
	midiInReset       = winmm.NewProc("midiInReset")
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

// midiMsg is a parsed MIDI message from the callback.
type midiMsg struct {
	status  byte // Raw status byte (0xB0=CC, 0x90=NoteOn, etc.)
	channel int
	data1   int // CC number or note number
	data2   int // Value or velocity
}

// Package-level channel for the callback. Only one MIDI reader per process.
var globalMidiInCh chan midiMsg

// midiInProc is the winmm callback. Runs on a Windows system thread.
// MUST be minimal: extract bytes, non-blocking send, return.
func midiInProc(hMidiIn uintptr, msg uint32, instance uintptr, param1 uintptr, param2 uintptr) uintptr {
	if msg == mimData {
		status := byte(param1 & 0xFF)
		m := midiMsg{
			status:  status,
			channel: int(status & 0x0F),
			data1:   int((param1 >> 8) & 0xFF),
			data2:   int((param1 >> 16) & 0xFF),
		}
		select {
		case globalMidiInCh <- m:
		default:
			// Drop if buffer full — never block the system thread
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

		deviceName := windows.UTF16ToString(caps.szPname[:])
		if strings.Contains(strings.ToLower(deviceName), portNameLower) {
			// Set up the global channel before opening
			globalMidiInCh = make(chan midiMsg, midiInBufSize)

			callbackPointer := windows.NewCallback(midiInProc)

			var handle uintptr
			ret, _, _ := midiInOpen.Call(
				uintptr(unsafe.Pointer(&handle)),
				i,
				callbackPointer,
				0,
				callbackFunction,
			)
			if ret != 0 {
				return nil, fmt.Errorf("midiInOpen for %q: %w", deviceName, mmresultError("midiInOpen", ret))
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
	ret, _, _ := midiInStart.Call(r.handle)
	if ret != 0 {
		return mmresultError("midiInStart", ret)
	}

	r.log.Info("MIDI input started")

	for {
		select {
		case msg := <-globalMidiInCh:
			msgType := msg.status & 0xF0
			switch {
			case msgType == 0xB0: // Control Change
				cb(msg.channel, msg.data1, msg.data2)
			case msgType == 0x90: // Note On
				r.log.Warn("unexpected MIDI Note On", "channel", msg.channel, "note", msg.data1, "velocity", msg.data2)
			case msgType == 0x80: // Note Off
				r.log.Warn("unexpected MIDI Note Off", "channel", msg.channel, "note", msg.data1)
			case msgType == 0xE0: // Pitch Bend
				r.log.Warn("unexpected MIDI Pitch Bend", "channel", msg.channel)
			case msgType == 0xC0: // Program Change
				r.log.Warn("unexpected MIDI Program Change", "channel", msg.channel, "program", msg.data1)
			default:
				r.log.Warn("unexpected MIDI message", "status", fmt.Sprintf("0x%02X", msg.status), "data1", msg.data1, "data2", msg.data2)
			}
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
	midiInReset.Call(r.handle)

	ret, _, _ := midiInClose.Call(r.handle)
	if ret != 0 {
		return mmresultError("midiInClose", ret)
	}
	r.log.Info("MIDI input closed")
	return nil
}

// ListInputPorts returns names of all available MIDI input ports.
func ListInputPorts() []string {
	numDevs, _, _ := midiInGetNumDevs.Call()
	inputPorts := make([]string, 0, numDevs)
	for i := uintptr(0); i < numDevs; i++ {
		var caps midiInCaps
		ret, _, _ := midiInGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
		if ret == 0 {
			inputPorts = append(inputPorts, windows.UTF16ToString(caps.szPname[:]))
		}
	}
	return inputPorts
}
