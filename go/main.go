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
	"vol20toglm/power"
	"vol20toglm/types"
)

const version = "0.8.0"

func main() {
	cfg := config.Parse(os.Args[1:])

	if cfg.ListDevices {
		fmt.Printf("vol20toglm v%s — device discovery\n\n", version)
		listDevices()
		return
	}

	log := applog.Setup(cfg.LogLevel, cfg.LogFileName)

	fmt.Printf("vol20toglm v%s\n", version)
	log.Info("========== vol20toglm start ==========",
		"version", version,
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

	// Power commander and observer — selected based on config flags.
	// Path A (--no-ui-automation): MIDI-only, no screen interaction.
	// Path B (--headless, no --ui-power): MIDI power, pixel verification.
	// Path B+ui (--headless --ui-power): UI click power, pixel verification.
	// Default (no flags): MIDI power, no verification.
	var powerCmd power.Commander
	var powerObs power.Observer

	// We need a gated writer for MIDICommander but gate is set up later.
	// Use a deferred assignment via a closure so the right writer is used.
	// For now, create MIDICommander with a placeholder; we'll reassign after gate is set up.
	// Actually we build the commander after gate is ready — see below after gate creation.

	// Power observer (windows controller for pixel scanning)
	var winPowerCtrl power.Controller // retained for startup pixel scan + legacy PID wiring

	if cfg.NoUIAutomation {
		// Path A: MIDI-only
		winPowerCtrl = nil
	} else if cfg.Headless {
		// Paths B and B+ui: need pixel scanning
		winPowerCtrl = createPowerController(log, cfg.DebugCaptures)
	} else {
		// Default: no pixel scanning
		winPowerCtrl = nil
	}

	// Pass GLM PID to pixel-scanning controller so it finds the correct window (not splash).
	// The restart callback is wired later after powerObs is resolved.
	if glmMgr != nil && winPowerCtrl != nil {
		winPowerCtrl.SetPID(glmMgr.GetPID())
	}

	// Power pattern detector
	midiLog := log.With("component", "midi-in")
	powerDetector := controller.NewPowerPatternDetector(func() {
		if ctrl.IsPowerCommandPending() {
			// Pattern was triggered by our own command — ignore, we already track state.
			// No need to clear: the 3-second timestamp window expires automatically.
			midiLog.Debug("power pattern detected (self-initiated, ignoring)")
			return
		}
		// External power change detected. If observer is available, verify via pixel read.
		if powerObs != nil {
			time.Sleep(controller.PowerVerifyDelay)
			actualState, err := powerObs.GetPowerState()
			if err != nil {
				// Pixel read failed — fall back to toggle
				newPower := ctrl.TogglePowerFromMIDIPattern()
				midiLog.Warn("power pattern detected (external), pixel verify failed, toggled", "new_power_state", newPower, "err", err)
			} else {
				ctrl.SetPower(actualState)
				midiLog.Info("power pattern detected (external), verified via pixel", "power", actualState)
			}
		} else {
			// No observer — blind toggle
			newPower := ctrl.TogglePowerFromMIDIPattern()
			midiLog.Info("power pattern detected (external)", "new_power_state", newPower)
		}
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

	// Probe GLM state at startup (uses raw midiOut, before gate is active).
	// Also sends CC28=127 to force speakers ON before probing volume.
	probeGLMState(midiOut, probeCh, ctrl, log)

	// Detect initial power state from pixel scan.
	// Retry a few times to allow splash screen to clear after fresh launch.
	// prepareWindow/restoreWindow are called internally by GetState.
	if winPowerCtrl != nil {
		var initialPower bool
		var scanErr error
		for attempt := 1; attempt <= 5; attempt++ {
			initialPower, scanErr = winPowerCtrl.GetState()
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

	// Build Commander and Observer now that consumerWriter is resolved.
	if cfg.NoUIAutomation {
		// Path A: MIDI-only, no screen interaction
		powerCmd = power.NewMIDICommander(consumerWriter, 0, log.With("component", "power-midi"))
		powerObs = nil
	} else if cfg.Headless {
		if cfg.UIPower {
			// Path B+ui: UI click for power, pixel for verification
			// winPowerCtrl implements both Commander and Observer
			if wc, ok := winPowerCtrl.(interface {
				power.Commander
				power.Observer
			}); ok {
				powerCmd = wc
				powerObs = wc
			}
		} else {
			// Path B: MIDI for power, pixel for verification
			powerCmd = power.NewMIDICommander(consumerWriter, 0, log.With("component", "power-midi"))
			if obs, ok := winPowerCtrl.(power.Observer); ok {
				powerObs = obs
			}
		}
	} else {
		// Default (no flags): MIDI power, no verification
		powerCmd = power.NewMIDICommander(consumerWriter, 0, log.With("component", "power-midi"))
		powerObs = nil
	}

	// UI power mode: read current state via pixel scan (don't force via MIDI).
	// For MIDI modes, power-on is handled inside probeGLMState above.
	if _, isMIDI := powerCmd.(*power.MIDICommander); !isMIDI && powerObs != nil {
		if initialState, err := powerObs.GetPowerState(); err == nil {
			ctrl.SetPower(initialState)
			log.Info("startup power state read via pixel scan", "power", initialState)
		}
	}

	// Wire Observer PID updates from GLM manager
	if powerObs != nil && glmMgr != nil {
		powerObs.SetPID(glmMgr.GetPID())
		glmMgr.SetRestartCallback(func(pid int) {
			powerObs.SetPID(pid)
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
func probeGLMState(midiOut midi.Writer, probeCh <-chan int, ctrl *controller.Controller, log *slog.Logger) {
	if midiOut == nil {
		return
	}

	// Small delay to let MIDI reader goroutine start
	time.Sleep(100 * time.Millisecond)

	// Step 0: Send CC28=127 (power ON) — ensures speakers are on before probing volume.
	// Uses raw writer (not gate) for reliable startup delivery.
	log.Info("probe: forcing power ON via CC28=127")
	ctrl.SetPowerCommandPending() // suppress ACK pattern from being treated as external
	if err := midiOut.SendCC(0, types.CCPower, 127, "startup"); err != nil {
		log.Warn("probe: failed to send CC28 (power ON)", "err", err)
	} else {
		// Wait for CC20 in the ACK burst to confirm GLM processed the command.
		select {
		case vol := <-probeCh:
			log.Info("probe: power ON acknowledged", "volume_in_ack", vol)
		case <-time.After(2 * time.Second):
			// No ACK — speakers were likely already ON (GLM skips ACK for no-ops)
			log.Info("probe: no power ACK (speakers likely already ON)")
		}
		// Drain any remaining burst messages
		time.Sleep(200 * time.Millisecond)
		for {
			select {
			case <-probeCh:
			default:
				goto drained
			}
		}
	drained:
	}

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
