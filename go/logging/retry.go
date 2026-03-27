package logging

import (
	"fmt"
	"sync"
	"time"
)

// Default milestone intervals: 2s, 10s, 1min, 10min, 1hr, 1day from first failure.
// Last value repeats indefinitely.
var DefaultIntervals = []float64{2, 10, 60, 600, 3600, 86400}

// RetryLogger throttles log messages during retry loops using time-based milestones.
// Retries continue at their normal frequency, but log messages are only emitted
// when enough time has elapsed since the first failure.
// Thread-safe — can be used from multiple goroutines.
type RetryLogger struct {
	intervals []float64
	mu        sync.Mutex
	trackers  map[string]*tracker
}

type tracker struct {
	firstTime     time.Time
	nextLogAt     float64 // seconds from first failure
	prevLogAt     float64
	intervalIndex int
	retryCount    int
}

// NewRetryLogger creates a retry logger with the given milestone intervals.
// If intervals is nil, DefaultIntervals is used.
func NewRetryLogger(intervals []float64) *RetryLogger {
	if intervals == nil {
		intervals = DefaultIntervals
	}
	return &RetryLogger{
		intervals: intervals,
		trackers:  make(map[string]*tracker),
	}
}

// ShouldLog returns true if a log message should be emitted for this key.
// Call on every retry attempt — it tracks timing internally.
func (r *RetryLogger) ShouldLog(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	t, ok := r.trackers[key]
	if !ok {
		// First attempt — always log
		firstInterval := r.intervals[0]
		r.trackers[key] = &tracker{
			firstTime:     now,
			nextLogAt:     firstInterval,
			prevLogAt:     0,
			intervalIndex: 0,
			retryCount:    1,
		}
		return true
	}

	t.retryCount++
	elapsed := now.Sub(t.firstTime).Seconds()

	if elapsed >= t.nextLogAt {
		t.prevLogAt = t.nextLogAt
		t.intervalIndex++

		idx := t.intervalIndex
		if idx >= len(r.intervals) {
			idx = len(r.intervals) - 1
		}
		nextInterval := r.intervals[idx]

		// Milestone rule: if interval > prev, use as absolute; otherwise cumulative
		if nextInterval > t.prevLogAt {
			t.nextLogAt = nextInterval
		} else {
			t.nextLogAt = t.prevLogAt + nextInterval
		}
		return true
	}

	return false
}

// RetryCount returns the current retry count for a key.
func (r *RetryLogger) RetryCount(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.trackers[key]; ok {
		return t.retryCount
	}
	return 0
}

// RetryInfo returns a formatted string like "(retry #5, next log at ~10m)".
func (r *RetryLogger) RetryInfo(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.trackers[key]
	if !ok {
		return ""
	}
	if t.intervalIndex > 0 {
		return fmt.Sprintf("(retry #%d, next log at ~%s)", t.retryCount, formatDuration(t.nextLogAt))
	}
	return fmt.Sprintf("(retry #%d)", t.retryCount)
}

// Reset clears the tracker for a key. Call when connection succeeds.
func (r *RetryLogger) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.trackers, key)
}

func formatDuration(seconds float64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", int(seconds))
	case seconds < 3600:
		return fmt.Sprintf("%dm", int(seconds/60))
	case seconds < 86400:
		return fmt.Sprintf("%dh", int(seconds/3600))
	default:
		return fmt.Sprintf("%dd", int(seconds/86400))
	}
}
