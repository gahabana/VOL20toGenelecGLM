package consumer

import (
	"context"
	"log/slog"
	"time"

	"vol20toglm/controller"
	"vol20toglm/midi"
	"vol20toglm/types"
)

const (
	MaxEventAge = 2.0 // seconds — discard actions older than this
)

// Run is the consumer goroutine. It reads actions from the channel,
// applies them to the controller, and sends the resulting MIDI messages.
func Run(ctx context.Context, actions <-chan types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-actions:
			processAction(a, ctrl, midiOut, midiChannel, log)
		}
	}
}

func processAction(a types.Action, ctrl *controller.Controller, midiOut midi.Writer, midiChannel int, log *slog.Logger) {
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

	// Power settling check
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
	} else {
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
		if err := midiOut.SendCC(midiChannel, cc, 127); err != nil {
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
	log.Debug("sending MIDI",
		"trace_id", a.TraceID,
		"cc", types.CCNames[cc],
		"cc_num", cc,
		"value", val,
	)
	if err := midiOut.SendCC(midiChannel, cc, val); err != nil {
		log.Error("MIDI send failed",
			"trace_id", a.TraceID,
			"cc", cc,
			"value", val,
			"err", err,
		)
	}
}
