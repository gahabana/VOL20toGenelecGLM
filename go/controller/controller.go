package controller

import (
	"errors"
	"sync"
	"time"
	"vol20toglm/types"
)

// Timing constants for power transitions.
const (
	PowerSettlingTime = 2.0                                   // Seconds to block ALL commands after power toggle
	PowerCooldownTime = 1.5                                   // Seconds to block power commands after settling ends
	PowerTotalLockout = PowerSettlingTime + PowerCooldownTime // 3.5s total
)

var errVolumeNotInitialized = errors.New("volume not initialized from GLM")

// Controller tracks GLM state and provides command acceptance logic.
type Controller struct {
	mu                   sync.Mutex
	state                types.State
	pendingVolume        *int // What we sent but GLM hasn't confirmed
	volumeInitialized    bool
	callbacks            []types.StateCallback
	lastNotifiedState    *types.State
	powerTransitionStart float64 // Unix timestamp when settling started
	powerSettling        bool
	powerCooldownStart   float64 // Unix timestamp when cooldown started (0 = no cooldown)
	powerTarget         *bool
	powerTraceID        string
	powerCommandSentAt  time.Time // zero value means no command pending; expires after 3 seconds
}

// New creates a Controller with default state (power on).
func New() *Controller {
	return &Controller{
		state: types.State{Power: true},
	}
}

// OnStateChange registers a callback for state changes.
func (c *Controller) OnStateChange(cb types.StateCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callbacks = append(c.callbacks, cb)
}

// GetState returns a snapshot of the current state.
func (c *Controller) GetState() types.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// GetEffectiveVolume returns pending volume if set, otherwise confirmed volume.
func (c *Controller) GetEffectiveVolume() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingVolume != nil {
		return *c.pendingVolume
	}
	return c.state.Volume
}

// HasValidVolume returns true if we've received volume from GLM.
func (c *Controller) HasValidVolume() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.volumeInitialized
}

// CanAcceptCommand checks if any command can be accepted.
// Returns (allowed, waitSeconds, reason).
// Only blocks during active settling (not during cooldown — cooldown only blocks power commands).
func (c *Controller) CanAcceptCommand() (bool, float64, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.powerSettling {
		return true, 0, ""
	}

	elapsed := float64(time.Now().UnixMilli())/1000 - c.powerTransitionStart
	if elapsed < PowerSettlingTime {
		return false, PowerSettlingTime - elapsed, "power_settling"
	}

	c.powerSettling = false
	return true, 0, ""
}

// CanAcceptPowerCommand checks if a power command can be accepted.
// Blocked during settling (all commands blocked) and during cooldown (power-only block).
func (c *Controller) CanAcceptPowerCommand() (bool, float64, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Settling blocks everything including power
	if c.powerSettling {
		elapsed := float64(time.Now().UnixMilli())/1000 - c.powerTransitionStart
		if elapsed < PowerSettlingTime {
			return false, PowerSettlingTime - elapsed, "power_settling"
		}
		c.powerSettling = false
	}

	// Cooldown blocks power commands only
	if c.powerCooldownStart > 0 {
		elapsed := float64(time.Now().UnixMilli())/1000 - c.powerCooldownStart
		if elapsed < PowerCooldownTime {
			return false, PowerCooldownTime - elapsed, "power_cooldown"
		}
		c.powerCooldownStart = 0 // Cooldown expired
	}

	return true, 0, ""
}

// StartPowerTransition marks the beginning of a power transition.
func (c *Controller) StartPowerTransition(targetState bool, traceID string) {
	c.mu.Lock()
	c.powerTransitionStart = float64(time.Now().UnixMilli()) / 1000
	c.powerSettling = true
	c.powerTarget = &targetState
	c.powerTraceID = traceID
	c.powerCommandSentAt = time.Now()
	c.mu.Unlock()
	c.notifyStateChange(true)
}

// EndPowerTransition marks the end of a power transition.
// Settling ends immediately. Cooldown (power-only block) starts from now.
// powerCommandSentAt is intentionally NOT cleared here — it expires naturally
// after 3 seconds so the MIDI pattern ACK can still be recognised if it
// arrives slightly after the transition ends.
func (c *Controller) EndPowerTransition(success bool, actualState *bool) {
	c.mu.Lock()
	c.powerSettling = false
	c.powerTransitionStart = 0
	c.powerCooldownStart = float64(time.Now().UnixMilli()) / 1000
	oldState := c.state
	if success {
		if actualState != nil {
			c.state.Power = *actualState
		} else if c.powerTarget != nil {
			c.state.Power = *c.powerTarget
		}
	}
	c.powerTarget = nil
	newState := c.state
	c.mu.Unlock()
	c.fireCallbacks(oldState, newState)
}

// SetPowerCommandPending records that a self-initiated power command was just sent.
func (c *Controller) SetPowerCommandPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.powerCommandSentAt = time.Now()
}

// ClearPowerCommandPending clears the pending timestamp immediately.
// Normally not needed — IsPowerCommandPending expires after 3 seconds automatically.
func (c *Controller) ClearPowerCommandPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.powerCommandSentAt = time.Time{}
}

// IsPowerCommandPending returns true when we sent a power command within the
// last 3 seconds and are waiting for GLM's MIDI pattern acknowledgement.
// The 3-second window is generous to catch the ACK pattern even if it arrives
// slightly after EndPowerTransition has been called.
func (c *Controller) IsPowerCommandPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.powerCommandSentAt.IsZero() {
		return false
	}
	return time.Since(c.powerCommandSentAt) < 3*time.Second
}

// SetPower sets the power state directly (e.g. from initial pixel scan at startup).
func (c *Controller) SetPower(on bool) {
	c.mu.Lock()
	oldState := c.state
	c.state.Power = on
	newState := c.state
	c.mu.Unlock()
	c.fireCallbacks(oldState, newState)
}

// TogglePowerFromMIDIPattern toggles power when RF remote pattern is detected.
// Returns the new power state.
func (c *Controller) TogglePowerFromMIDIPattern() bool {
	c.mu.Lock()
	oldState := c.state
	c.state.Power = !c.state.Power
	newPower := c.state.Power
	newState := c.state
	c.mu.Unlock()
	c.fireCallbacks(oldState, newState)
	return newPower
}

// ApplyAction processes an action and returns the MIDI CC + value to send.
// Does NOT send MIDI -- caller is responsible for that.
func (c *Controller) ApplyAction(action types.Action) (cc int, value int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch action.Kind {
	case types.KindSetVolume:
		if !c.volumeInitialized {
			return 0, 0, errVolumeNotInitialized
		}
		target := clamp(action.Value, 0, 127)
		pendingValue := target
		c.pendingVolume = &pendingValue
		return types.CCVolumeAbs, target, nil

	case types.KindAdjustVolume:
		if !c.volumeInitialized {
			return 0, 0, errVolumeNotInitialized
		}
		effectiveVolume := c.state.Volume
		if c.pendingVolume != nil {
			effectiveVolume = *c.pendingVolume
		}
		target := clamp(effectiveVolume+action.Value, 0, 127)
		pendingValue := target
		c.pendingVolume = &pendingValue
		return types.CCVolumeAbs, target, nil

	case types.KindSetMute:
		var midiValue int
		if action.Toggle {
			if c.state.Mute {
				midiValue = 0
			} else {
				midiValue = 127
			}
		} else {
			if action.BoolValue {
				midiValue = 127
			} else {
				midiValue = 0
			}
		}
		return types.CCMute, midiValue, nil

	case types.KindSetDim:
		var midiValue int
		if action.Toggle {
			if c.state.Dim {
				midiValue = 0
			} else {
				midiValue = 127
			}
		} else {
			if action.BoolValue {
				midiValue = 127
			} else {
				midiValue = 0
			}
		}
		return types.CCDim, midiValue, nil

	case types.KindSetPower:
		// NOTE: Power actions are now handled directly by power.Commander in the consumer,
		// bypassing ApplyAction. This case is retained for completeness but should not be reached.
		return types.CCPower, 127, nil

	default:
		return 0, 0, errors.New("unknown action kind")
	}
}

// UpdateFromMIDI updates state from a MIDI CC message. Returns true if state changed.
func (c *Controller) UpdateFromMIDI(cc, value int) bool {
	c.mu.Lock()
	oldState := c.state
	changed := false
	forceNotify := false

	switch cc {
	case types.CCVolumeAbs:
		c.volumeInitialized = true
		if c.pendingVolume != nil && *c.pendingVolume != value {
			forceNotify = true
		}
		c.pendingVolume = nil
		if c.state.Volume != value {
			c.state.Volume = value
			changed = true
		}
	case types.CCMute:
		newMute := value > 0
		if c.state.Mute != newMute {
			c.state.Mute = newMute
			changed = true
		}
	case types.CCDim:
		newDim := value > 0
		if c.state.Dim != newDim {
			c.state.Dim = newDim
			changed = true
		}
	}

	newState := c.state
	c.mu.Unlock()

	if changed || forceNotify {
		c.fireCallbacks(oldState, newState)
	}
	return changed
}

func (c *Controller) notifyStateChange(force bool) {
	c.mu.Lock()
	oldState := c.state
	newState := c.state
	if !force && c.lastNotifiedState != nil && *c.lastNotifiedState == newState {
		c.mu.Unlock()
		return
	}
	stateCopy := newState
	c.lastNotifiedState = &stateCopy
	c.mu.Unlock()
	c.fireCallbacks(oldState, newState)
}

func (c *Controller) fireCallbacks(oldState, newState types.State) {
	c.mu.Lock()
	callbacksCopy := make([]types.StateCallback, len(c.callbacks))
	copy(callbacksCopy, c.callbacks)
	c.mu.Unlock()
	for _, cb := range callbacksCopy {
		cb(oldState, newState)
	}
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
