package consumer

import (
	"context"
	"log/slog"
	"time"

	"vol20toglm/controller"
	"vol20toglm/midi"
	"vol20toglm/power"
	"vol20toglm/types"
)

const (
	MaxEventAge = 2.0 // seconds — discard actions older than this
)

// Run is the consumer goroutine. It reads actions from the channel,
// applies them to the controller, and sends the resulting MIDI messages.
// powerCmd sends power on/off commands (MIDI or UI click).
// powerObs reads power state from screen for verification (nil = no verification).
func Run(ctx context.Context, actions <-chan types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, powerCmd power.Commander, powerObs power.Observer, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-actions:
			processAction(a, ctrl, midiOut, midiChannel, powerCmd, powerObs, log)
		}
	}
}

func processAction(a types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, powerCmd power.Commander, powerObs power.Observer, log *slog.Logger) {
	// Stale event filter
	age := time.Since(a.Timestamp).Seconds()
	if age > MaxEventAge {
		log.Warn("dropping stale action",
			"trace_id", a.TraceID,
			"kind", a.Kind.String(),
			"age_s", age,
		)
		return
	}

	// Power actions: check power-specific settling, then delegate to Commander
	if a.Kind == types.KindSetPower {
		allowed, wait, reason := ctrl.CanAcceptPowerCommand()
		if !allowed {
			log.Warn("power command blocked",
				"trace_id", a.TraceID,
				"reason", reason,
				"wait_s", wait,
			)
			return
		}

		// Determine target state
		var targetOn bool
		if a.Toggle {
			targetOn = !ctrl.GetState().Power
		} else {
			targetOn = a.BoolValue
		}

		ctrl.StartPowerTransition(targetOn, a.TraceID)

		var commandErr error
		if targetOn {
			commandErr = powerCmd.PowerOn(a.TraceID)
		} else {
			commandErr = powerCmd.PowerOff(a.TraceID)
		}

		if commandErr != nil {
			log.Error("power command failed", "trace_id", a.TraceID, "err", commandErr)
			ctrl.EndPowerTransition(false, nil)
			return
		}

		// Optional verification via observer
		if powerObs != nil {
			// Wait for GLM to process the command and update its UI before sampling pixels.
			time.Sleep(time.Duration(controller.PowerVerifyDelay * float64(time.Second)))
			actualState, verifyErr := powerObs.GetPowerState()
			if verifyErr != nil {
				log.Warn("power verify failed", "trace_id", a.TraceID, "err", verifyErr)
				ctrl.EndPowerTransition(true, &targetOn)
			} else {
				ctrl.EndPowerTransition(true, &actualState)
				if actualState != targetOn {
					log.Warn("power state mismatch after command",
						"trace_id", a.TraceID,
						"expected", targetOn,
						"actual", actualState,
					)
				}
			}
		} else {
			ctrl.EndPowerTransition(true, &targetOn)
		}

		log.Info("power command complete", "trace_id", a.TraceID, "target", targetOn)
		return
	}

	// Non-power actions: check general settling
	allowed, wait, reason := ctrl.CanAcceptCommand()
	if !allowed {
		log.Warn("command blocked",
			"trace_id", a.TraceID,
			"kind", a.Kind.String(),
			"reason", reason,
			"wait_s", wait,
		)
		return
	}

	// Special handling for AdjustVolume when volume not initialized
	if a.Kind == types.KindAdjustVolume && !ctrl.HasValidVolume() {
		cc := types.CCVolUp
		if a.Value < 0 {
			cc = types.CCVolDown
		}
		log.Debug("volume not initialized, using fallback",
			"trace_id", a.TraceID,
			"cc", cc,
		)
		if err := midiOut.SendCC(midiChannel, cc, 127, a.TraceID); err != nil {
			log.Error("MIDI send failed", "trace_id", a.TraceID, "err", err)
		}
		return
	}

	// Apply action to controller — get MIDI CC + value
	cc, val, err := ctrl.ApplyAction(a)
	if err != nil {
		log.Error("apply action failed",
			"trace_id", a.TraceID,
			"kind", a.Kind.String(),
			"err", err,
		)
		return
	}

	// Send MIDI
	if err := midiOut.SendCC(midiChannel, cc, val, a.TraceID); err != nil {
		log.Error("MIDI send failed",
			"trace_id", a.TraceID,
			"cc", cc,
			"value", val,
			"err", err,
		)
	}
}
