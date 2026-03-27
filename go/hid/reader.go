package hid

import (
	"time"

	"vol20toglm/types"
)

// ProcessReport maps a raw HID keycode to an Action and sends it to the channel.
// This is the platform-independent core of the HID reader.
// Returns true if an action was sent, false if the keycode was ignored.
func ProcessReport(keycode int, now time.Time, bindings map[int]types.ActionKind, accel *AccelerationHandler, traceGen *types.TraceIDGenerator, actions chan<- types.Action) bool {
	if keycode == 0 {
		return false
	}

	kind, ok := bindings[keycode]
	if !ok {
		return false
	}

	var action types.Action
	action.Kind = kind
	action.Source = "hid"
	action.TraceID = traceGen.Next("hid")
	action.Timestamp = now

	switch kind {
	case types.KindAdjustVolume:
		currentTimeSeconds := float64(now.UnixMilli()) / 1000.0
		distance := accel.CalculateSpeed(currentTimeSeconds, keycode)
		if keycode == types.KeyVolDown {
			distance = -distance
		}
		action.Value = distance

	case types.KindSetMute, types.KindSetDim, types.KindSetPower:
		action.Toggle = true
	}

	actions <- action
	return true
}
