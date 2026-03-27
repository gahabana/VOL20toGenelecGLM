package controller

import (
	"testing"
	"time"
	"vol20toglm/types"
)

func TestApplyAction_SetVolume_Clamps(t *testing.T) {
	c := New()
	// Initialize volume so controller accepts commands
	c.UpdateFromMIDI(types.CCVolumeAbs, 50)

	tests := []struct {
		name      string
		target    int
		wantValue int
	}{
		{"normal", 80, 80},
		{"clamp high", 200, 127},
		{"clamp low", -10, 0},
		{"zero", 0, 0},
		{"max", 127, 127},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := types.Action{
				Kind:      types.KindSetVolume,
				Value:     tt.target,
				Timestamp: time.Now(),
			}
			cc, val, err := c.ApplyAction(action)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cc != types.CCVolumeAbs {
				t.Errorf("cc = %d, want %d", cc, types.CCVolumeAbs)
			}
			if val != tt.wantValue {
				t.Errorf("value = %d, want %d", val, tt.wantValue)
			}
		})
	}
}

func TestApplyAction_AdjustVolume(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCVolumeAbs, 50) // Start at 50

	action := types.Action{
		Kind:      types.KindAdjustVolume,
		Value:     3,
		Timestamp: time.Now(),
	}
	cc, val, err := c.ApplyAction(action)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cc != types.CCVolumeAbs || val != 53 {
		t.Errorf("got cc=%d val=%d, want cc=%d val=53", cc, val, types.CCVolumeAbs)
	}
}

func TestApplyAction_AdjustVolume_ClampsAtBounds(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCVolumeAbs, 126)

	action := types.Action{
		Kind:      types.KindAdjustVolume,
		Value:     5,
		Timestamp: time.Now(),
	}
	_, val, _ := c.ApplyAction(action)
	if val != 127 {
		t.Errorf("val = %d, want 127 (clamped)", val)
	}
}

func TestApplyAction_SetMute_Toggle(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCMute, 0) // mute off

	action := types.Action{
		Kind:      types.KindSetMute,
		Toggle:    true,
		Timestamp: time.Now(),
	}
	cc, val, _ := c.ApplyAction(action)
	if cc != types.CCMute || val != 127 {
		t.Errorf("toggle mute off->on: cc=%d val=%d, want cc=%d val=127", cc, val, types.CCMute)
	}
}

func TestApplyAction_SetMute_Explicit(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCMute, 0) // mute off

	action := types.Action{
		Kind:      types.KindSetMute,
		BoolValue: false, // explicitly set to off (already off)
		Toggle:    false,
		Timestamp: time.Now(),
	}
	cc, val, _ := c.ApplyAction(action)
	if cc != types.CCMute || val != 0 {
		t.Errorf("explicit mute off: cc=%d val=%d, want cc=%d val=0", cc, val, types.CCMute)
	}
}

func TestApplyAction_SetDim_Toggle(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCDim, 127) // dim on

	action := types.Action{
		Kind:      types.KindSetDim,
		Toggle:    true,
		Timestamp: time.Now(),
	}
	cc, val, _ := c.ApplyAction(action)
	if cc != types.CCDim || val != 0 {
		t.Errorf("toggle dim on->off: cc=%d val=%d, want cc=%d val=0", cc, val, types.CCDim)
	}
}

func TestUpdateFromMIDI_Volume(t *testing.T) {
	c := New()
	changed := c.UpdateFromMIDI(types.CCVolumeAbs, 75)
	if !changed {
		t.Error("expected changed=true for first volume update")
	}
	state := c.GetState()
	if state.Volume != 75 {
		t.Errorf("volume = %d, want 75", state.Volume)
	}
}

func TestUpdateFromMIDI_Mute(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCMute, 127)
	state := c.GetState()
	if !state.Mute {
		t.Error("expected mute=true after CC23=127")
	}
	c.UpdateFromMIDI(types.CCMute, 0)
	state = c.GetState()
	if state.Mute {
		t.Error("expected mute=false after CC23=0")
	}
}

func TestUpdateFromMIDI_Dim(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCDim, 127)
	state := c.GetState()
	if !state.Dim {
		t.Error("expected dim=true after CC24=127")
	}
}

func TestApplyAction_RejectsBeforeVolumeInit(t *testing.T) {
	c := New()
	// Don't initialize volume
	action := types.Action{
		Kind:      types.KindSetVolume,
		Value:     50,
		Timestamp: time.Now(),
	}
	_, _, err := c.ApplyAction(action)
	if err == nil {
		t.Error("expected error when volume not initialized")
	}
}

func TestGetEffectiveVolume_PendingOverride(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCVolumeAbs, 50)

	// After ApplyAction, pending volume should be set
	action := types.Action{
		Kind:      types.KindSetVolume,
		Value:     80,
		Timestamp: time.Now(),
	}
	c.ApplyAction(action)

	vol := c.GetEffectiveVolume()
	if vol != 80 {
		t.Errorf("effective volume = %d, want 80 (pending)", vol)
	}

	// After MIDI confirms, pending clears
	c.UpdateFromMIDI(types.CCVolumeAbs, 80)
	vol = c.GetEffectiveVolume()
	if vol != 80 {
		t.Errorf("effective volume = %d, want 80 (confirmed)", vol)
	}
}

func TestCanAcceptCommand_DuringSettling(t *testing.T) {
	c := New()
	c.StartPowerTransition(false, "test-0001")

	allowed, wait, reason := c.CanAcceptCommand()
	if allowed {
		t.Error("should be blocked during settling")
	}
	if reason != "power_settling" {
		t.Errorf("reason = %q, want power_settling", reason)
	}
	if wait <= 0 || wait > PowerSettlingTime {
		t.Errorf("wait = %f, expected 0 < wait <= %f", wait, PowerSettlingTime)
	}
}

func TestCanAcceptPowerCommand_DuringCooldown(t *testing.T) {
	c := New()
	// Simulate: settling done, cooldown just started
	c.mu.Lock()
	c.powerSettling = false
	c.powerCooldownStart = float64(time.Now().UnixMilli()) / 1000 // cooldown started now
	c.mu.Unlock()

	allowed, _, reason := c.CanAcceptPowerCommand()
	if allowed {
		t.Error("power command should be blocked during cooldown")
	}
	if reason != "power_cooldown" {
		t.Errorf("reason = %q, want power_cooldown", reason)
	}
}

func TestCanAcceptPowerCommand_AfterLockout(t *testing.T) {
	c := New()
	// Simulate: cooldown expired (started 2s ago, only lasts 1.5s)
	c.mu.Lock()
	c.powerSettling = false
	c.powerCooldownStart = float64(time.Now().Add(-2*time.Second).UnixMilli()) / 1000
	c.mu.Unlock()

	allowed, _, _ := c.CanAcceptPowerCommand()
	if !allowed {
		t.Error("power command should be allowed after cooldown expires")
	}
}

func TestTogglePowerFromMIDIPattern(t *testing.T) {
	c := New()
	// Default power is true
	newPower := c.TogglePowerFromMIDIPattern()
	if newPower {
		t.Error("toggle from ON should give OFF")
	}
	newPower = c.TogglePowerFromMIDIPattern()
	if !newPower {
		t.Error("toggle from OFF should give ON")
	}
}

func TestStateCallback_CalledOnChange(t *testing.T) {
	c := New()
	var called bool
	var gotOld, gotNew types.State

	c.OnStateChange(func(old, new_ types.State) {
		called = true
		gotOld = old
		gotNew = new_
	})

	c.UpdateFromMIDI(types.CCVolumeAbs, 80)

	if !called {
		t.Fatal("callback not called")
	}
	if gotOld.Volume != 0 {
		t.Errorf("old volume = %d, want 0", gotOld.Volume)
	}
	if gotNew.Volume != 80 {
		t.Errorf("new volume = %d, want 80", gotNew.Volume)
	}
}

func TestStateCallback_NotCalledWhenUnchanged(t *testing.T) {
	c := New()
	c.UpdateFromMIDI(types.CCVolumeAbs, 80)

	callCount := 0
	c.OnStateChange(func(old, new_ types.State) {
		callCount++
	})

	c.UpdateFromMIDI(types.CCVolumeAbs, 80) // same value
	if callCount != 0 {
		t.Errorf("callback called %d times for unchanged state", callCount)
	}
}

func TestEndPowerTransition_UpdatesState(t *testing.T) {
	c := New()
	c.StartPowerTransition(false, "test-0001")

	actualState := false
	c.EndPowerTransition(true, &actualState)

	state := c.GetState()
	if state.Power {
		t.Error("power should be OFF after successful transition to OFF")
	}
}
