//go:build windows

package main

import (
	"log/slog"
	"os/exec"
	"time"

	"vol20toglm/config"
	"vol20toglm/glm"
	"vol20toglm/hid"
	"vol20toglm/midi"
	"vol20toglm/power"
	"vol20toglm/rdp"
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

func createMIDIReader(cfg config.Config, log *slog.Logger) midi.Reader {
	// MIDIOutChannel = GLM's output port (where we READ from)
	r, err := midi.OpenWinMMReader(cfg.MIDIOutChannel, log)
	if err != nil {
		log.Error("failed to open MIDI input", "port", cfg.MIDIOutChannel, "err", err)
		return &midi.StubReader{Log: log}
	}
	return r
}

func createPowerController(log *slog.Logger) power.Controller {
	return power.NewWindowsController(log.With("component", "power"))
}

func createGLMManager(cfg config.Config, log *slog.Logger) glm.Manager {
	return glm.NewWindowsManager(cfg.GLMPath, cfg.GLMCPUGating, log.With("component", "glm"))
}

func runStartupTasks(cfg config.Config, log *slog.Logger) {
	// RDP priming
	if cfg.RDPPriming {
		primer := &rdp.WindowsPrimer{Log: log.With("component", "rdp")}
		if primer.NeedsPriming() {
			if err := primer.Prime(); err != nil {
				log.Error("RDP priming failed", "err", err)
			}
		}
	}

	// MIDI service restart
	if cfg.MIDIRestart {
		log.Info("restarting Windows MIDI service")
		exec.Command("net", "stop", "midisrv").Run()
		time.Sleep(1 * time.Second)
		exec.Command("net", "start", "midisrv").Run()
		time.Sleep(1 * time.Second)
		log.Info("MIDI service restarted")
	}
}
