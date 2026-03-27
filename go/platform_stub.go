//go:build !windows

package main

import (
	"log/slog"

	"vol20toglm/config"
	"vol20toglm/hid"
	"vol20toglm/midi"
	"vol20toglm/types"
)

func createMIDIWriter(cfg config.Config, log *slog.Logger) midi.Writer {
	return &midi.StubWriter{Log: log}
}

func createHIDReader(cfg config.Config, accel *hid.AccelerationHandler, traceGen *types.TraceIDGenerator, log *slog.Logger) hid.Reader {
	return &hid.StubReader{Log: log.With("component", "hid")}
}

func createMIDIReader(cfg config.Config, log *slog.Logger) midi.Reader {
	return &midi.StubReader{Log: log.With("component", "midi-in")}
}
