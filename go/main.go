package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"vol20toglm/api"
	"vol20toglm/config"
	"vol20toglm/consumer"
	"vol20toglm/controller"
	"vol20toglm/glm"
	"vol20toglm/hid"
	applog "vol20toglm/logging"
	"vol20toglm/midi"
	"vol20toglm/midigate"
	"vol20toglm/types"
)

const version = "0.7.0"

func main() {
	cfg := config.Parse(os.Args[1:])

	if cfg.ListDevices {
		fmt.Printf("vol20toglm v%s — device discovery\n\n", version)
		listDevices()
		return
	}

	log := applog.Setup(cfg.LogLevel, cfg.LogFileName)

	// Set process priority
	if cfg.HighPriority {
		setProcessPriority(log)
	}

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

	// === Startup automation (headless VM) ===
	runStartupTasks(cfg, log)

	// GLM Manager (launch/attach, watchdog)
	var glmMgr glm.Manager
	if cfg.GLMManager {
		glmMgr = createGLMManager(cfg, log)
		if err := glmMgr.Start(); err != nil {
			log.Error("GLM manager start failed", "err", err)
		} else {
			defer glmMgr.Stop()
		}
	}
	_ = glmMgr

	// Core components
	ctrl := controller.New()
	traceGen := types.NewTraceIDGenerator()
	actions := make(chan types.Action, 100)

	// API server
	// Web UI directory — look relative to executable, then fall back to ../web
	webDir := filepath.Join(filepath.Dir(os.Args[0]), "..", "web")
	if _, err := os.Stat(filepath.Join(webDir, "index.html")); err != nil {
		webDir = "" // No web UI found
	}
	apiServer := api.NewServer(ctrl, actions, version, webDir, log.With("component", "api"))
	ctrl.OnStateChange(func(old, new_ types.State) {
		apiServer.BroadcastState()
	})

	// MIDI output — platform-specific, created in platform_*.go
	midiOut := createMIDIWriter(cfg, ctx, log)
	if midiOut != nil {
		defer midiOut.Close()
	}

	// MIDI input — platform-specific
	midiIn := createMIDIReader(cfg, ctx, log)
	defer midiIn.Close()

	// Power controller — platform-specific
	powerCtrl := createPowerController(log)

	// Power pattern detector
	midiLog := log.With("component", "midi-in")
	powerDetector := controller.NewPowerPatternDetector(func() {
		newPower := ctrl.TogglePowerFromMIDIPattern()
		midiLog.Info("power pattern detected", "new_power_state", newPower)
	})

	// Channel for startup volume probe (buffered, non-blocking send from callback)
	probeCh := make(chan int, 10)

	// Response-gated MIDI sender (sits between consumer and raw MIDI writer)
	var gate *midigate.Gate
	if midiOut != nil {
		gate = midigate.New(midiOut, log.With("component", "midigate"))
	}

	// Acceleration handler
	accel := hid.NewAccelerationHandler(cfg.MinClickTime, cfg.MaxAvgClickTime, cfg.VolumeIncreases)

	// HID reader — platform-specific
	hidReader := createHIDReader(cfg, accel, traceGen, log)

	var wg sync.WaitGroup

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

			// Notify gate about GLM response (for send gating)
			if gate != nil {
				gate.NotifyReceive(cc)
			}

			// Feed volume to startup probe (non-blocking)
			if cc == types.CCVolumeAbs {
				select {
				case probeCh <- value:
				default:
				}
			}
		})
		if err != nil && ctx.Err() == nil {
			log.Error("MIDI reader exited with error", "err", err)
		}
	}()

	// Probe GLM state at startup (uses raw midiOut, before gate is active)
	probeGLMState(midiOut, probeCh, log)

	// Detect initial power state from pixel scan.
	// Bring GLM to foreground first (console may be covering it), then retry
	// a few times to allow splash screen to clear after fresh launch.
	if powerCtrl != nil {
		powerCtrl.BringToForeground()
		var initialPower bool
		var scanErr error
		for attempt := 1; attempt <= 5; attempt++ {
			initialPower, scanErr = powerCtrl.GetState()
			if scanErr == nil {
				break
			}
			log.Debug("power scan attempt failed, retrying", "attempt", attempt, "err", scanErr)
			time.Sleep(1 * time.Second)
		}
		if scanErr == nil {
			ctrl.SetPower(initialPower)
			log.Info("initial power state from pixel scan", "power", initialPower)
		} else {
			log.Warn("could not read initial power state, assuming ON", "err", scanErr)
		}
		powerCtrl.RestoreForeground()
	}

	// Start gate goroutine (after probe, before consumer)
	if gate != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate.Run(ctx)
		}()
	}

	// Start consumer goroutine (uses gate for response-gated sending)
	var consumerWriter midi.Writer
	if gate != nil {
		consumerWriter = gate
	} else if midiOut != nil {
		consumerWriter = midiOut
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if consumerWriter == nil {
			log.Warn("no MIDI output, consumer running in dry-run mode")
		}
		consumer.Run(ctx, actions, ctrl, consumerWriter, 0, powerCtrl, log.With("component", "consumer"))
	}()

	// Start HID reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := hidReader.Run(ctx, actions); err != nil && ctx.Err() == nil {
			log.Error("HID reader exited with error", "err", err)
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

// probeGLMState sends CC21 (Vol+) then CC22 (Vol-) to discover GLM's state.
// The Vol- response gives us the original volume (handles GLM's min/max clamping).
// Net zero volume change in all cases.
func probeGLMState(midiOut midi.Writer, probeCh <-chan int, log *slog.Logger) {
	if midiOut == nil {
		return
	}

	// Small delay to let MIDI reader goroutine start
	time.Sleep(100 * time.Millisecond)

	// Step 1: Send CC21 (Vol+) — triggers GLM state burst
	log.Info("probing GLM state...")
	start := time.Now()
	if err := midiOut.SendCC(0, types.CCVolUp, 127, "probe"); err != nil {
		log.Warn("probe: failed to send CC21 (Vol+)", "err", err)
		return
	}

	var volUp int
	select {
	case volUp = <-probeCh:
		elapsed := time.Since(start)
		log.Info("probe: Vol+ response", "volume", volUp, "response_time", elapsed.Round(time.Millisecond))
	case <-time.After(1 * time.Second):
		log.Warn("probe: no response from GLM within 1s (volume not initialized)")
		return
	}

	// GLM needs ~30ms between commands; 100ms gives 3x margin
	time.Sleep(100 * time.Millisecond)

	// Step 2: Send CC22 (Vol-) — restores original volume
	start2 := time.Now()
	if err := midiOut.SendCC(0, types.CCVolDown, 127, "probe"); err != nil {
		log.Warn("probe: failed to send CC22 (Vol-)", "err", err)
		return
	}

	select {
	case volDown := <-probeCh:
		elapsed2 := time.Since(start2)
		log.Info("probe: Vol- response", "volume", volDown, "response_time", elapsed2.Round(time.Millisecond))

		// volDown is the original volume (Vol+ then Vol- = net zero)
		// If GLM clamped the Vol+ (at max), volUp == volDown and both are correct
		log.Info("probe: GLM state initialized", "volume", volDown)

	case <-time.After(1 * time.Second):
		// Vol- didn't respond, but Vol+ did — use volUp-1 as best guess
		log.Warn("probe: Vol- no response, using Vol+ value", "volume", volUp)
	}
}
