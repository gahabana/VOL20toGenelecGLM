package controller

import (
	"vol20toglm/types"
)

// PowerPatternDetector watches incoming MIDI CC messages for GLM's
// 5-message power toggle pattern (Mute->Vol->Dim->Mute->Vol).
// When detected, calls the onDetected callback.
// NOT thread-safe -- caller must serialize calls to Feed().
type PowerPatternDetector struct {
	onDetected func()

	buf             [5]ccEvent
	pos             int
	lastTime        float64 // timestamp of the most recent Feed call
	timeBeforeStart float64 // lastTime snapshot when pattern collection began

	lastPatternTime float64
}

type ccEvent struct {
	cc    int
	value int
	time  float64
}

// NewPowerPatternDetector creates a detector that calls onDetected
// when the power pattern is recognized.
func NewPowerPatternDetector(onDetected func()) *PowerPatternDetector {
	return &PowerPatternDetector{onDetected: onDetected}
}

// Feed processes an incoming MIDI CC message. Call for every CC received.
func (d *PowerPatternDetector) Feed(cc, value int, timestamp float64) {
	defer func() { d.lastTime = timestamp }()

	expected := types.PowerPattern[d.pos]

	if cc != expected {
		// If this message matches the start of the pattern, begin fresh
		if cc == types.PowerPattern[0] {
			d.timeBeforeStart = d.lastTime
			d.buf[0] = ccEvent{cc, value, timestamp}
			d.pos = 1
		} else {
			d.pos = 0
		}
		return
	}

	// Snapshot lastTime when starting a new pattern at position 0
	if d.pos == 0 {
		d.timeBeforeStart = d.lastTime
	}

	d.buf[d.pos] = ccEvent{cc, value, timestamp}
	d.pos++

	if d.pos < len(types.PowerPattern) {
		return
	}

	// We have all 5 messages -- validate timing constraints
	d.pos = 0

	firstEvent := d.buf[0]
	lastEvent := d.buf[len(types.PowerPattern)-1]

	totalSpan := lastEvent.time - firstEvent.time
	if totalSpan > types.PowerPatternWindow {
		return
	}
	if totalSpan < types.PowerPatternMinSpan {
		return
	}

	totalGaps := 0.0
	for i := 1; i < len(types.PowerPattern); i++ {
		gap := d.buf[i].time - d.buf[i-1].time
		if gap > types.PowerPatternMaxGap {
			return
		}
		totalGaps += gap
	}
	if totalGaps > types.PowerPatternMaxTotal {
		return
	}

	// Check pre-gap: sufficient silence before the pattern started
	if d.timeBeforeStart > 0 {
		preGap := firstEvent.time - d.timeBeforeStart
		if preGap < types.PowerPatternPreGap {
			return
		}
	}

	// Startup suppression: second pattern within window is GLM startup noise
	if d.lastPatternTime > 0 {
		sinceLastPattern := firstEvent.time - d.lastPatternTime
		if sinceLastPattern < types.PowerStartupWindow {
			d.lastPatternTime = firstEvent.time
			return
		}
	}

	d.lastPatternTime = firstEvent.time
	d.onDetected()
}
