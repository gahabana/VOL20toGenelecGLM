package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"vol20toglm/api"
	"vol20toglm/config"
	"vol20toglm/consumer"
	"vol20toglm/controller"
	"vol20toglm/hid"
	"vol20toglm/types"
)

const version = "0.4.0"

func main() {
	cfg := config.Parse(os.Args[1:])

	var logLevel slog.Level
	switch cfg.LogLevel {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "INFO":
		logLevel = slog.LevelInfo
	default:
		logLevel = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	fmt.Printf("vol20toglm v%s\n", version)
	log.Info("starting",
		"version", version,
		"vid", fmt.Sprintf("0x%04x", cfg.VID),
		"pid", fmt.Sprintf("0x%04x", cfg.PID),
		"midi_in", cfg.MIDIInChannel,
		"midi_out", cfg.MIDIOutChannel,
		"api_port", cfg.APIPort,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Core components
	ctrl := controller.New()
	traceGen := types.NewTraceIDGenerator()
	actions := make(chan types.Action, 100)

	// API server
	apiServer := api.NewServer(ctrl, actions, version, log.With("component", "api"))
	ctrl.OnStateChange(func(old, new_ types.State) {
		apiServer.BroadcastState()
	})

	// MIDI output — platform-specific, created in platform_*.go
	midiOut := createMIDIWriter(cfg, log)
	if midiOut != nil {
		defer midiOut.Close()
	}

	// MIDI input — platform-specific
	midiIn := createMIDIReader(cfg, log)
	defer midiIn.Close()

	// Power pattern detector
	midiLog := log.With("component", "midi-in")
	powerDetector := controller.NewPowerPatternDetector(func() {
		newPower := ctrl.TogglePowerFromMIDIPattern()
		midiLog.Info("power pattern detected", "new_power_state", newPower)
	})

	// Acceleration handler
	accel := hid.NewAccelerationHandler(cfg.MinClickTime, cfg.MaxAvgClickTime, cfg.VolumeIncreases)

	// HID reader — platform-specific
	hidReader := createHIDReader(cfg, accel, traceGen, log)

	var wg sync.WaitGroup

	// Start consumer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if midiOut == nil {
			log.Warn("no MIDI output, consumer running in dry-run mode")
		}
		consumer.Run(ctx, actions, ctrl, midiOut, 0, log.With("component", "consumer"))
	}()

	// Start HID reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := hidReader.Run(ctx, actions); err != nil && ctx.Err() == nil {
			log.Error("HID reader exited with error", "err", err)
		}
	}()

	// Start MIDI input reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := midiIn.Start(func(channel, cc, value int) {
			now := float64(time.Now().UnixMilli()) / 1000.0

			ccName := types.CCNames[cc]
			if ccName == "" {
				ccName = fmt.Sprintf("CC%d", cc)
			}
			midiLog.Debug("MIDI recv", "cc", ccName, "cc_num", cc, "value", value, "channel", channel)

			// Update controller state
			changed := ctrl.UpdateFromMIDI(cc, value)
			if changed {
				midiLog.Debug("state updated from MIDI", "cc", ccName, "value", value)
			}

			// Feed to power pattern detector
			powerDetector.Feed(cc, value, now)
		})
		if err != nil && ctx.Err() == nil {
			log.Error("MIDI reader exited with error", "err", err)
		}
	}()

	// Start API server
	if cfg.APIPort > 0 {
		httpServer := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.APIPort),
			Handler: apiServer.Handler(),
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("API server listening", "port", cfg.APIPort)
			if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
				log.Error("API server error", "err", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			httpServer.Shutdown(shutdownCtx)
		}()
	}

	log.Info("running — press Ctrl+C to stop")
	<-ctx.Done()
	log.Info("shutting down")

	cancel()
	midiIn.Close()
	wg.Wait()
	log.Info("shutdown complete")
}
