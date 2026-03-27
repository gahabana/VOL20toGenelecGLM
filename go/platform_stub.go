//go:build !windows

package main

import (
	"fmt"
	"log/slog"

	"vol20toglm/config"
	"vol20toglm/glm"
	"vol20toglm/hid"
	"vol20toglm/midi"
	"vol20toglm/power"
	"vol20toglm/types"
)

func setProcessPriority(log *slog.Logger) {
	// No-op on non-Windows
}

func listDevices() {
	fmt.Println("Device listing is only available on Windows.")
	fmt.Println("Run this command on the Windows machine where GLM is installed.")
}

func createMIDIWriter(cfg config.Config, log *slog.Logger) midi.Writer {
	return &midi.StubWriter{Log: log}
}

func createHIDReader(cfg config.Config, accel *hid.AccelerationHandler, traceGen *types.TraceIDGenerator, log *slog.Logger) hid.Reader {
	return &hid.StubReader{Log: log.With("component", "hid")}
}

func createMIDIReader(cfg config.Config, log *slog.Logger) midi.Reader {
	return &midi.StubReader{Log: log.With("component", "midi-in")}
}

func createPowerController(log *slog.Logger) power.Controller {
	return &power.StubController{Log: log.With("component", "power"), State: true}
}

func createGLMManager(cfg config.Config, log *slog.Logger) glm.Manager {
	return &glm.StubManager{Log: log.With("component", "glm")}
}

func runStartupTasks(cfg config.Config, log *slog.Logger) {
	// No startup tasks on non-Windows
}
