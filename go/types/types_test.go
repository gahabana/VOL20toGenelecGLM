package types

import (
	"sync"
	"testing"
)

func TestTraceIDGenerator_Sequential(t *testing.T) {
	g := NewTraceIDGenerator()
	if id := g.Next("hid"); id != "hid-0001" {
		t.Errorf("got %q, want hid-0001", id)
	}
	if id := g.Next("hid"); id != "hid-0002" {
		t.Errorf("got %q, want hid-0002", id)
	}
	if id := g.Next("api"); id != "api-0001" {
		t.Errorf("got %q, want api-0001", id)
	}
}

func TestTraceIDGenerator_Concurrent(t *testing.T) {
	g := NewTraceIDGenerator()
	var wg sync.WaitGroup
	ids := make(chan string, 200)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- g.Next("hid")
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate trace ID: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 100 {
		t.Errorf("got %d unique IDs, want 100", len(seen))
	}
}

func TestActionKind_String(t *testing.T) {
	tests := []struct {
		kind ActionKind
		want string
	}{
		{KindSetVolume, "SetVolume"},
		{KindAdjustVolume, "AdjustVolume"},
		{KindSetMute, "SetMute"},
		{KindSetDim, "SetDim"},
		{KindSetPower, "SetPower"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("ActionKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}
