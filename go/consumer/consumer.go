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
func Run(ctx context.Context, actions <-chan types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, powerCtrl power.Controller, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-actions:
			processAction(a, ctrl, midiOut, midiChannel, powerCtrl, log)
		}
	}
}

func processAction(a types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, powerCtrl power.Controller, log *slog.Logger) {
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

	// Power actions: check power-specific settling, then use pixel toggle or MIDI
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

		// Pixel-based power toggle when power controller is available
		if powerCtrl != nil {
			log.Info("toggling power via UI automation", "trace_id", a.TraceID)
			ctrl.StartPowerTransition(!ctrl.GetState().Power, a.TraceID)

			if err := powerCtrl.Toggle(); err != nil {
				log.Error("power toggle failed", "trace_id", a.TraceID, "err", err)
				ctrl.EndPowerTransition(false, nil)
				return
			}

			newState, err := powerCtrl.GetState()
			if err != nil {
				log.Error("power state read failed", "trace_id", a.TraceID, "err", err)
				ctrl.EndPowerTransition(false, nil)
				return
			}

			ctrl.EndPowerTransition(true, &newState)
			log.Info("power toggle complete", "trace_id", a.TraceID, "power", newState)
			return
		}
		// Fall through to MIDI send when powerCtrl is nil
	} else {
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
