package controller

import (
	"vol20toglm/types"
)

// PatternMatch contains timing details of a matched power pattern.
type PatternMatch struct {
	Span            float64 // Duration of the 5-CC pattern in seconds
	SinceLastMatch  float64 // Seconds since the previous pattern match (-1 if first)
}

// PowerPatternDetector watches incoming MIDI CC messages for GLM's
// 5-message power toggle pattern (Mute->Vol->Dim->Mute->Vol).
// When the pattern is recognized (timing constraints pass), calls
// the onMatched callback with match details. The callback decides
// whether to act on or suppress the pattern.
// NOT thread-safe -- caller must serialize calls to Feed().
type PowerPatternDetector struct {
	onMatched func(PatternMatch)

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

// NewPowerPatternDetector creates a detector that calls onMatched
// every time the power pattern is recognized with valid timing.
func NewPowerPatternDetector(onMatched func(PatternMatch)) *PowerPatternDetector {
	return &PowerPatternDetector{onMatched: onMatched}
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

	// Build match info
	match := PatternMatch{
		Span:           totalSpan,
		SinceLastMatch: -1,
	}
	if d.lastPatternTime > 0 {
		match.SinceLastMatch = firstEvent.time - d.lastPatternTime
	}
	d.lastPatternTime = firstEvent.time

	d.onMatched(match)
}
