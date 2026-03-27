package types

import (
	"fmt"
	"sync"
	"time"
)

// ActionKind identifies what kind of action to perform.
type ActionKind int

const (
	KindSetVolume    ActionKind = iota // Set absolute volume (0-127)
	KindAdjustVolume                   // Relative volume change (+/-)
	KindSetMute                        // Set or toggle mute
	KindSetDim                         // Set or toggle dim
	KindSetPower                       // Set or toggle power
)

var actionKindNames = [...]string{
	"SetVolume", "AdjustVolume", "SetMute", "SetDim", "SetPower",
}

func (k ActionKind) String() string {
	if int(k) < len(actionKindNames) {
		return actionKindNames[k]
	}
	return fmt.Sprintf("ActionKind(%d)", k)
}

// Action represents a command flowing through the action channel.
type Action struct {
	Kind      ActionKind
	Value     int       // Volume level (0-127) for SetVolume, delta for AdjustVolume
	BoolValue bool      // Target state for SetMute/SetDim/SetPower
	Toggle    bool      // If true, toggle current state instead of using BoolValue
	Source    string    // Origin: "hid", "api", "mqtt"
	TraceID   string    // Correlation ID: "hid-0001", "api-0042"
	Timestamp time.Time // When the action was created
}

// State represents the current GLM state.
type State struct {
	Volume int    `json:"volume"`
	Mute   bool   `json:"mute"`
	Dim    bool   `json:"dim"`
	Power  bool   `json:"power"`
	Source string `json:"source"` // Last source that changed state
}

// StateCallback is invoked when GLM state changes.
type StateCallback func(old, new_ State)

// TraceIDGenerator produces unique, sequential trace IDs per source.
// Thread-safe.
type TraceIDGenerator struct {
	mu       sync.Mutex
	counters map[string]int
}

// NewTraceIDGenerator creates a new generator.
func NewTraceIDGenerator() *TraceIDGenerator {
	return &TraceIDGenerator{counters: make(map[string]int)}
}

// Next returns the next trace ID for the given source (e.g., "hid-0001").
func (g *TraceIDGenerator) Next(source string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counters[source]++
	return fmt.Sprintf("%s-%04d", source, g.counters[source])
}
