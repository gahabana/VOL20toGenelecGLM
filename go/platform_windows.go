//go:build windows

package main

import (
	"log/slog"

	"vol20toglm/config"
	"vol20toglm/hid"
	"vol20toglm/midi"
	"vol20toglm/types"
)

func createMIDIWriter(cfg config.Config, log *slog.Logger) midi.Writer {
	// MIDIInChannel = GLM's input port (where we WRITE to)
	w, err := midi.OpenWinMMWriter(cfg.MIDIInChannel, log)
	if err != nil {
		log.Error("failed to open MIDI output", "port", cfg.MIDIInChannel, "err", err)
		return &midi.StubWriter{Log: log}
	}
	return w
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
