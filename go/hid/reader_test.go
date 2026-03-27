package hid

import (
	"testing"
	"time"

	"vol20toglm/types"
)

func TestProcessReport_VolUp(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(types.KeyVolUp, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case a := <-actions:
		if a.Kind != types.KindAdjustVolume {
			t.Errorf("kind = %v, want AdjustVolume", a.Kind)
		}
		if a.Value != 1 {
			t.Errorf("delta = %d, want 1 (first click)", a.Value)
		}
		if a.Source != "hid" {
			t.Errorf("source = %q, want hid", a.Source)
		}
	default:
		t.Fatal("no action sent to channel")
	}
}

func TestProcessReport_VolDown(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(types.KeyVolDown, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case a := <-actions:
		if a.Kind != types.KindAdjustVolume {
			t.Errorf("kind = %v, want AdjustVolume", a.Kind)
		}
		if a.Value >= 0 {
			t.Errorf("delta = %d, want negative", a.Value)
		}
	default:
		t.Fatal("no action sent to channel")
	}
}

func TestProcessReport_Click_Power(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(types.KeyClick, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case a := <-actions:
		if a.Kind != types.KindSetPower {
			t.Errorf("kind = %v, want SetPower", a.Kind)
		}
		if !a.Toggle {
			t.Error("expected Toggle=true for power click")
		}
	default:
		t.Fatal("no action sent to channel")
	}
}

func TestProcessReport_DoubleClick_Dim(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(types.KeyDoubleClick, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case a := <-actions:
		if a.Kind != types.KindSetDim {
			t.Errorf("kind = %v, want SetDim", a.Kind)
		}
		if !a.Toggle {
			t.Error("expected Toggle=true for dim")
		}
	default:
		t.Fatal("no action sent to channel")
	}
}

func TestProcessReport_LongPress_Mute(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(types.KeyLongPress, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case a := <-actions:
		if a.Kind != types.KindSetMute {
			t.Errorf("kind = %v, want SetMute", a.Kind)
		}
		if !a.Toggle {
			t.Error("expected Toggle=true for mute")
		}
	default:
		t.Fatal("no action sent to channel")
	}
}

func TestProcessReport_ZeroKeycode_Ignored(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(0, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case <-actions:
		t.Fatal("zero keycode should be ignored")
	default:
		// OK — nothing sent
	}
}

func TestProcessReport_UnboundKey_Ignored(t *testing.T) {
	traceGen := types.NewTraceIDGenerator()
	accel := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	actions := make(chan types.Action, 10)

	ProcessReport(99, time.Now(), types.DefaultBindings, accel, traceGen, actions)

	select {
	case <-actions:
		t.Fatal("unbound key should be ignored")
	default:
		// OK — nothing sent
	}
}
