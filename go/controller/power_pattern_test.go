package controller

import (
	"testing"

	"vol20toglm/types"
)

func TestPowerPattern_ExactMatch(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCDim, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	if !detected {
		t.Error("power pattern should have been detected")
	}
}

func TestPowerPattern_WrongSequence(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCMute, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	if detected {
		t.Error("wrong sequence should not trigger pattern")
	}
}

func TestPowerPattern_TooSlow(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.30)
	pp.Feed(types.CCDim, 0, base+0.60)
	pp.Feed(types.CCMute, 0, base+0.90)
	pp.Feed(types.CCVolumeAbs, 50, base+1.20)

	if detected {
		t.Error("too-slow pattern should not trigger")
	}
}

func TestPowerPattern_TooFast_BufferDump(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.007)
	pp.Feed(types.CCDim, 0, base+0.015)
	pp.Feed(types.CCMute, 0, base+0.022)
	pp.Feed(types.CCVolumeAbs, 50, base+0.030)

	if detected {
		t.Error("buffer dump (too fast) should not trigger")
	}
}

func TestPowerPattern_NoPreGap(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCVolumeAbs, 60, base)

	pp.Feed(types.CCMute, 0, base+0.05)
	pp.Feed(types.CCVolumeAbs, 50, base+0.11)
	pp.Feed(types.CCDim, 0, base+0.17)
	pp.Feed(types.CCMute, 0, base+0.23)
	pp.Feed(types.CCVolumeAbs, 50, base+0.29)

	if detected {
		t.Error("pattern without sufficient pre-gap should not trigger")
	}
}

func TestPowerPattern_DuplicateReportsSinceLastMatch(t *testing.T) {
	var matches []PatternMatch
	pp := NewPowerPatternDetector(func(m PatternMatch) {
		matches = append(matches, m)
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCDim, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	if len(matches) != 1 {
		t.Fatalf("first pattern: count = %d, want 1", len(matches))
	}
	if matches[0].SinceLastMatch != -1 {
		t.Errorf("first pattern SinceLastMatch = %v, want -1", matches[0].SinceLastMatch)
	}

	// Second pattern 1s later — detector fires with SinceLastMatch info
	// (suppression logic is now in the callback, not the detector)
	base2 := base + 1.0
	pp.Feed(types.CCMute, 0, base2)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.06)
	pp.Feed(types.CCDim, 0, base2+0.12)
	pp.Feed(types.CCMute, 0, base2+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.24)

	if len(matches) != 2 {
		t.Fatalf("second pattern: count = %d, want 2 (detector always fires, callback decides)", len(matches))
	}
	if matches[1].SinceLastMatch < 0.9 || matches[1].SinceLastMatch > 1.1 {
		t.Errorf("second pattern SinceLastMatch = %v, want ~1.0", matches[1].SinceLastMatch)
	}
}

func TestPowerPattern_TwoPatterns_FarApart(t *testing.T) {
	count := 0
	pp := NewPowerPatternDetector(func(PatternMatch) {
		count++
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCDim, 0, base+0.12)
	pp.Feed(types.CCMute, 0, base+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base+0.24)

	base2 := base + 5.0
	pp.Feed(types.CCMute, 0, base2)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.06)
	pp.Feed(types.CCDim, 0, base2+0.12)
	pp.Feed(types.CCMute, 0, base2+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.24)

	if count != 2 {
		t.Errorf("two far-apart patterns: count = %d, want 2", count)
	}
}

func TestPowerPattern_TotalGapExceeded(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.10)
	pp.Feed(types.CCDim, 0, base+0.20)
	pp.Feed(types.CCMute, 0, base+0.30)
	pp.Feed(types.CCVolumeAbs, 50, base+0.40)

	if detected {
		t.Error("pattern with total gaps > 350ms should not trigger")
	}
}

func TestPowerPattern_ResetAfterFailure(t *testing.T) {
	detected := false
	pp := NewPowerPatternDetector(func(PatternMatch) {
		detected = true
	})

	base := 1000.0
	pp.Feed(types.CCMute, 0, base)
	pp.Feed(types.CCVolumeAbs, 50, base+0.06)
	pp.Feed(types.CCVolumeAbs, 50, base+0.12)

	base2 := base + 0.5
	pp.Feed(types.CCMute, 0, base2)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.06)
	pp.Feed(types.CCDim, 0, base2+0.12)
	pp.Feed(types.CCMute, 0, base2+0.18)
	pp.Feed(types.CCVolumeAbs, 50, base2+0.24)

	if !detected {
		t.Error("valid pattern after failed one should trigger")
	}
}
