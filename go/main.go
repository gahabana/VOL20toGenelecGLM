package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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
	appmqtt "vol20toglm/mqtt"
	"vol20toglm/power"
	"vol20toglm/types"
	"vol20toglm/version"
)

func main() {
	runtime.GOMAXPROCS(2)
	cfg := config.Parse(os.Args[1:])

	if cfg.ListDevices {
		fmt.Printf("vol20toglm v%s — device discovery\n\n", version.Version)
		listDevices()
		return
	}

	log := applog.Setup(cfg.LogLevel, cfg.LogFileName)

	fmt.Printf("vol20toglm v%s\n", version.Version)
	cfg.PrintStartupSummary()
	log.Info("========== vol20toglm start ==========",
		"version", version.Version,
		"vid", fmt.Sprintf("0x%04x", cfg.VID),
		"pid", fmt.Sprintf("0x%04x", cfg.PID),
		"midi_in", cfg.MIDIInChannel,
		"midi_out", cfg.MIDIOutChannel,
		"api_port", cfg.APIPort,
	)

	// Set process priority
	if cfg.HighPriority {
		setProcessPriority(log)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// === Startup automation (headless VM) ===
	runStartupTasks(cfg, log)

	// GLM Manager — created here, started after MIDI reader is running
	// so the reader captures GLM's 12-message startup burst.
	var glmMgr glm.Manager
	if cfg.GLMManager {
		glmMgr = createGLMManager(cfg, log)
	}

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
	apiServer := api.NewServer(ctrl, actions, version.Version, webDir, cfg.CORSOrigin, log.With("component", "api"))
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

	// Power commander and observer — selected based on config flags.
	// Default: MIDI power, no screen interaction.
	// --pixel_verify: MIDI power, pixel verification.
	// --ui_power: UI click power, pixel verification.
	// --desktop: MIDI power, no screen interaction, no GLM management.
	var powerCmd power.Commander
	var powerObs power.Observer

	// We need a gated writer for MIDICommander but gate is set up later.
	// Use a deferred assignment via a closure so the right writer is used.
	// For now, create MIDICommander with a placeholder; we'll reassign after gate is set up.
	// Actually we build the commander after gate is ready — see below after gate creation.

	// Power observer (pixel scanning, nil unless --pixel_verify)
	if cfg.PixelVerify {
		powerObs = createPowerObserver(log, cfg.DebugCaptures)
	}

	// Power pattern detector
	midiLog := log.With("component", "midi-in")
	powerDetector := controller.NewPowerPatternDetector(func(match controller.PatternMatch) {
		sinceLastStr := "first"
		if match.SinceLastMatch >= 0 {
			sinceLastStr = fmt.Sprintf("%dms", int(match.SinceLastMatch*1000))
		}
		midiLog.Info("power pattern matched",
			"span_ms", int(match.Span*1000),
			"since_last", sinceLastStr,
		)

		// Suppression: self-initiated command pending (our CC28 ACK)
		if ctrl.IsPowerCommandPending() {
			midiLog.Debug("power pattern suppressed (self-initiated)")
			return
		}

		// Suppression: duplicate pattern within startup window (GLM startup noise or ACK)
		if match.SinceLastMatch >= 0 && match.SinceLastMatch < types.PowerStartupWindow {
			midiLog.Debug("power pattern suppressed (duplicate)",
				"since_last_ms", int(match.SinceLastMatch*1000),
				"window_s", types.PowerStartupWindow,
			)
			return
		}

		// External power change — send deterministic CC28 follow-through.
		currentPower := ctrl.GetState().Power
		targetPower := !currentPower
		midiLog.Info("external power change detected",
			"previous", currentPower, "target", targetPower)

		// Short delay for the external command to settle in GLM.
		time.Sleep(500 * time.Millisecond)

		// Send CC28 directly (bypass gate — this IS the power command).
		var cc28Value int
		if targetPower {
			cc28Value = 127
		} else {
			cc28Value = 0
		}
		ctrl.SetPowerCommandPending()
		if err := midiOut.SendCC(0, types.CCPower, cc28Value, "ext-followthrough"); err != nil {
			midiLog.Error("power follow-through failed", "err", err)
			return
		}
		ctrl.SetPower(targetPower)
		midiLog.Info("follow-through sent", "state", targetPower, "cc28", cc28Value)
	})

	// Channel for startup volume probe (buffered, non-blocking send from callback)
	probeCh := make(chan int, 10)

	// Response-gated MIDI sender (sits between consumer and raw MIDI writer)
	var gate *midigate.Gate
	if midiOut != nil {
		gate = midigate.New(midiOut, log.With("component", "midigate"))
		gate.OnVolumeSent = ctrl.NotifyVolumeSent
	}

	// Acceleration handler
	accel := hid.NewAccelerationHandler(cfg.MinClickTime, cfg.MaxAvgClickTime, cfg.VolumeIncreases)

	// HID reader — platform-specific
	hidReader := createHIDReader(cfg, accel, traceGen, log)

	// Flag to suppress power pattern detector during GLM startup burst consumption.
	var startupConsuming atomic.Bool

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

			// Feed to power pattern detector (skip during startup burst consumption)
			if !startupConsuming.Load() {
				powerDetector.Feed(cc, value, now)
			}

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

	// Start GLM Manager now that MIDI reader is capturing.
	// GLM's 12-message startup burst will be caught by the reader.
	if glmMgr != nil {
		startupConsuming.Store(true)
		if err := glmMgr.Start(); err != nil {
			log.Error("GLM manager start failed", "err", err)
			startupConsuming.Store(false)
		} else {
			defer glmMgr.Stop()
		}
		// Pass GLM PID to pixel-scanning observer
		if powerObs != nil {
			powerObs.SetPID(glmMgr.GetPID())
		}
	}

	// Probe GLM state: consume startup burst (if managed) then force power state.
	probeGLMState(midiOut, probeCh, ctrl, &startupConsuming, cfg.StartupPower, log)

	// Detect initial power state from pixel scan.
	// Retry a few times to allow splash screen to clear after fresh launch.
	if powerObs != nil {
		var initialPower bool
		var scanErr error
		for attempt := 1; attempt <= 5; attempt++ {
			initialPower, scanErr = powerObs.GetPowerState()
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
	}

	// Start gate goroutine (after probe, before consumer)
	if gate != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate.Run(ctx)
		}()
	}

	// Resolve the MIDI writer for the consumer (gated if available, else raw)
	var consumerWriter midi.Writer
	if gate != nil {
		consumerWriter = gate
	} else if midiOut != nil {
		consumerWriter = midiOut
	}

	// Build Commander now that consumerWriter is resolved.
	// powerObs was already set above (nil unless --pixel_verify).
	if cfg.UIPower {
		// Path B+ui: UI click for power, pixel for verification
		powerCmd = createPowerCommander(powerObs)
	}
	if powerCmd == nil {
		// Paths A, B, and default: MIDI for power
		powerCmd = power.NewMIDICommander(consumerWriter, 0, log.With("component", "power-midi"))
	}

	// UI power mode: read current state via pixel scan (don't force via MIDI).
	// For MIDI modes, power-on is handled inside probeGLMState above.
	if _, isMIDI := powerCmd.(*power.MIDICommander); !isMIDI && powerObs != nil {
		if initialState, err := powerObs.GetPowerState(); err == nil {
			ctrl.SetPower(initialState)
			log.Info("startup power state read via pixel scan", "power", initialState)
		}
	}

	// Wire Observer PID updates and re-probe on GLM restart
	if glmMgr != nil {
		if powerObs != nil {
			powerObs.SetPID(glmMgr.GetPID())
		}
		glmMgr.SetPreRestartCallback(func() {
			startupConsuming.Store(true)
		})
		glmMgr.SetRestartCallback(func(pid int) {
			if powerObs != nil {
				powerObs.SetPID(pid)
			}
			// Re-probe after GLM restart — consume startup burst + force power state.
			log.Info("re-probing GLM state after restart")
			probeGLMState(midiOut, probeCh, ctrl, &startupConsuming, cfg.StartupPower, log)
		})
	}

	// Start consumer goroutine (uses gate for response-gated sending)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if consumerWriter == nil {
			log.Warn("no MIDI output, consumer running in dry-run mode")
		}
		consumer.Run(ctx, actions, ctrl, consumerWriter, 0, powerCmd, powerObs, log.With("component", "consumer"))
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
			Addr:              fmt.Sprintf(":%d", cfg.APIPort),
			Handler:           apiServer.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
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

	// Start MQTT client if configured
	var mqttClient *appmqtt.Client
	if cfg.MQTTBroker != "" {
		mqttClient = appmqtt.Start(
			cfg.MQTTBroker, cfg.MQTTPort,
			cfg.MQTTUser, cfg.MQTTPass,
			cfg.MQTTTopic, cfg.MQTTHADiscovery,
			ctrl, actions, traceGen,
			log.With("component", "mqtt"),
		)
		if mqttClient != nil {
			log.Info("MQTT enabled", "broker", cfg.MQTTBroker, "port", cfg.MQTTPort, "topic", cfg.MQTTTopic, "ha_discovery", cfg.MQTTHADiscovery)
		}
	}

	log.Info("running — press Ctrl+C to stop")
	<-ctx.Done()
	log.Info("shutting down")

	if mqttClient != nil {
		mqttClient.Stop()
	}
	cancel()
	midiIn.Close()
	wg.Wait()
	log.Info("shutdown complete")
}

// probeGLMState discovers GLM state at startup or after restart.
// Phase 1: If startupConsuming is set (we launched GLM), waits for GLM's
//
//	12-message startup burst (5 CC20s) which provides volume/mute/dim via UpdateFromMIDI.
//
// Phase 2: Sends CC28 to force speakers to the desired state (on/off).
// GLM responds with 5-message ACK (2 CC20s).
// Vol+/Vol- probing is not needed — volume is discovered passively from MIDI output.
func probeGLMState(midiOut midi.Writer, probeCh <-chan int, ctrl *controller.Controller,
	startupConsuming *atomic.Bool, startupPower string, log *slog.Logger) {

	// Phase 1: Consume GLM startup burst (if we launched GLM).
	// GLM emits 12 messages in 2x 5-msg patterns (~1s total) when starting.
	// UpdateFromMIDI (in the MIDI reader callback) captures state automatically.
	// Power pattern detector is suppressed via startupConsuming flag.
	if startupConsuming != nil && startupConsuming.Load() {
		consumeStartupBurst(probeCh, startupConsuming, ctrl, log)
	}

	// Phase 2: Force speakers to desired state via CC28.
	// GLM responds with 5-message pattern (Mute→Vol→Dim→Mute→Vol, 2 CC20s).
	if midiOut == nil {
		return
	}
	sendPowerProbe(midiOut, probeCh, ctrl, startupPower, log)
}

// consumeStartupBurst waits for GLM's 12-message startup burst.
// The burst contains 5 CC20 messages (2 patterns of Mute→Vol→Dim→Mute→Vol).
// Counting CC20s lets us finish as soon as the burst is complete — no settle timer.
func consumeStartupBurst(probeCh <-chan int, startupConsuming *atomic.Bool,
	ctrl *controller.Controller, log *slog.Logger) {

	const expectedCC20 = 5                      // 12 messages total, 5 are CC20
	const firstTimeout = 15 * time.Second       // max wait for first CC20 (GLM boot time)
	const msgTimeout = 2 * time.Second          // max wait between consecutive CC20s

	probeStart := time.Now()

	for i := 0; i < expectedCC20; i++ {
		timeout := msgTimeout
		if i == 0 {
			timeout = firstTimeout
		}
		select {
		case <-probeCh:
		case <-time.After(timeout):
			log.Warn("probe: startup burst incomplete", "received", i, "expected", expectedCC20)
			startupConsuming.Store(false)
			return
		}
	}

	startupConsuming.Store(false)
	state := ctrl.GetState()
	log.Info("probe: startup burst consumed",
		"cc20_count", expectedCC20,
		"volume", state.Volume, "mute", state.Mute, "dim", state.Dim,
		"elapsed", time.Since(probeStart).Round(time.Millisecond))
}

// sendPowerProbe sends CC28 to force the desired power state and waits for the 5-message response.
// The response pattern is Mute→Vol→Dim→Mute→Vol — 2 CC20 messages.
func sendPowerProbe(midiOut midi.Writer, probeCh <-chan int,
	ctrl *controller.Controller, startupPower string, log *slog.Logger) {

	const expectedCC20 = 2                // response has 2 CC20 messages
	const respTimeout = 3 * time.Second   // max wait for first response CC20
	const msgTimeout = 2 * time.Second    // max wait between CC20s within response

	powerOn := startupPower == "on"
	cc28Value := 0
	if powerOn {
		cc28Value = 127
	}

	log.Info("probe: forcing power state via CC28", "state", startupPower, "cc28", cc28Value)
	ctrl.SetPowerCommandPending()
	if err := midiOut.SendCC(0, types.CCPower, cc28Value, "startup"); err != nil {
		log.Warn("probe: failed to send CC28", "state", startupPower, "err", err)
		return
	}

	respStart := time.Now()
	for i := 0; i < expectedCC20; i++ {
		timeout := msgTimeout
		if i == 0 {
			timeout = respTimeout
		}
		select {
		case <-probeCh:
		case <-time.After(timeout):
			if i == 0 {
				log.Info("probe: no power response within timeout")
			} else {
				log.Warn("probe: power response incomplete", "received", i, "expected", expectedCC20)
			}
			goto done
		}
	}
	log.Info("probe: power response complete", "elapsed", time.Since(respStart).Round(time.Millisecond))

done:
	ctrl.SetPower(powerOn)
	state := ctrl.GetState()
	log.Info("probe: GLM state initialized",
		"volume", state.Volume, "mute", state.Mute, "dim", state.Dim, "power", state.Power)
}
