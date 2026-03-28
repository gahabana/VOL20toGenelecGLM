package midigate

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"vol20toglm/types"
)

type mockWriter struct {
	mu   sync.Mutex
	sent []cmd
}

func (m *mockWriter) SendCC(channel, cc, value int, traceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, cmd{channel, cc, value, traceID})
	return nil
}

func (m *mockWriter) Close() error { return nil }

func (m *mockWriter) getSent() []cmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]cmd, len(m.sent))
	copy(cp, m.sent)
	return cp
}

func newTestGate() (context.Context, context.CancelFunc, *Gate, *mockWriter) {
	ctx, cancel := context.WithCancel(context.Background())
	mw := &mockWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	g := New(mw, log)
	go g.Run(ctx)
	time.Sleep(10 * time.Millisecond) // let goroutine start
	return ctx, cancel, g, mw
}

func TestGate_FirstCommandSentImmediately(t *testing.T) {
	_, cancel, g, mw := newTestGate()
	defer cancel()

	g.SendCC(0, types.CCVolumeAbs, 50, "t1")
	time.Sleep(20 * time.Millisecond)

	sent := mw.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 immediate send, got %d", len(sent))
	}
	if sent[0].value != 50 {
		t.Errorf("got value=%d, want 50", sent[0].value)
	}
}

func TestGate_SecondCommandWaitsForResponse(t *testing.T) {
	_, cancel, g, mw := newTestGate()
	defer cancel()

	g.SendCC(0, types.CCVolumeAbs, 50, "t1")
	time.Sleep(20 * time.Millisecond)

	// Second command should be queued, not sent
	g.SendCC(0, types.CCVolumeAbs, 60, "t2")
	time.Sleep(20 * time.Millisecond)

	sent := mw.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send (second should be queued), got %d", len(sent))
	}

	// Simulate GLM response burst
	g.NotifyReceive(types.CCDim)
	g.NotifyReceive(types.CCMute)
	g.NotifyReceive(types.CCVolumeAbs)

	// Wait for settle + processing
	time.Sleep(SettleDelay + 30*time.Millisecond)

	sent = mw.getSent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 sends after response, got %d", len(sent))
	}
	if sent[1].value != 60 {
		t.Errorf("second send value=%d, want 60", sent[1].value)
	}
}

func TestGate_VolumeCoalesces(t *testing.T) {
	_, cancel, g, mw := newTestGate()
	defer cancel()

	g.SendCC(0, types.CCVolumeAbs, 50, "t1")
	time.Sleep(20 * time.Millisecond)

	// Queue 3 volume commands — should coalesce to last
	g.SendCC(0, types.CCVolumeAbs, 55, "t2")
	g.SendCC(0, types.CCVolumeAbs, 60, "t3")
	g.SendCC(0, types.CCVolumeAbs, 65, "t4")
	time.Sleep(20 * time.Millisecond)

	// Simulate response
	g.NotifyReceive(types.CCVolumeAbs)
	time.Sleep(SettleDelay + 30*time.Millisecond)

	sent := mw.getSent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 sends (first + coalesced), got %d", len(sent))
	}
	if sent[1].value != 65 {
		t.Errorf("coalesced value=%d, want 65", sent[1].value)
	}
}

func TestGate_MuteNotCoalesced(t *testing.T) {
	_, cancel, g, mw := newTestGate()
	defer cancel()

	g.SendCC(0, types.CCVolumeAbs, 50, "t1")
	time.Sleep(20 * time.Millisecond)

	// Queue two mute toggles — both must be sent (not coalesced)
	g.SendCC(0, types.CCMute, 127, "mute-on")
	g.SendCC(0, types.CCMute, 0, "mute-off")
	time.Sleep(20 * time.Millisecond)

	// Response to first command
	g.NotifyReceive(types.CCVolumeAbs)
	time.Sleep(SettleDelay + 30*time.Millisecond)

	sent := mw.getSent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(sent))
	}
	if sent[1].cc != types.CCMute || sent[1].value != 127 {
		t.Errorf("second send: cc=%d val=%d, want cc=%d val=127", sent[1].cc, sent[1].value, types.CCMute)
	}

	// Response to mute-on
	g.NotifyReceive(types.CCVolumeAbs)
	time.Sleep(SettleDelay + 30*time.Millisecond)

	sent = mw.getSent()
	if len(sent) != 3 {
		t.Fatalf("expected 3 sends, got %d", len(sent))
	}
	if sent[2].cc != types.CCMute || sent[2].value != 0 {
		t.Errorf("third send: cc=%d val=%d, want cc=%d val=0", sent[2].cc, sent[2].value, types.CCMute)
	}
}

func TestGate_MutePriorityOverVolume(t *testing.T) {
	_, cancel, g, mw := newTestGate()
	defer cancel()

	g.SendCC(0, types.CCVolumeAbs, 50, "t1")
	time.Sleep(20 * time.Millisecond)

	// Queue volume and mute — mute should go first
	g.SendCC(0, types.CCVolumeAbs, 60, "vol")
	g.SendCC(0, types.CCMute, 127, "mute")
	time.Sleep(20 * time.Millisecond)

	// Response
	g.NotifyReceive(types.CCVolumeAbs)
	time.Sleep(SettleDelay + 30*time.Millisecond)

	sent := mw.getSent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(sent))
	}
	// Mute should be sent before coalesced volume
	if sent[1].cc != types.CCMute {
		t.Errorf("expected mute sent first, got cc=%d", sent[1].cc)
	}

	// Response to mute
	g.NotifyReceive(types.CCVolumeAbs)
	time.Sleep(SettleDelay + 30*time.Millisecond)

	sent = mw.getSent()
	if len(sent) != 3 {
		t.Fatalf("expected 3 sends, got %d", len(sent))
	}
	if sent[2].cc != types.CCVolumeAbs || sent[2].value != 60 {
		t.Errorf("expected coalesced volume=60, got cc=%d val=%d", sent[2].cc, sent[2].value)
	}
}

func TestGate_TimeoutProceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mw := &mockWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	g := &Gate{
		writer: mw,
		log:    log,
		sendCh: make(chan cmd, sendChSize),
		recvCh: make(chan int, recvChSize),
	}
	go g.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	g.SendCC(0, types.CCMute, 127, "mute")
	time.Sleep(20 * time.Millisecond)

	g.SendCC(0, types.CCVolumeAbs, 50, "vol")
	// Don't send any response — wait for timeout
	time.Sleep(ResponseTimeout + 100*time.Millisecond)

	sent := mw.getSent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 sends (original + after timeout), got %d", len(sent))
	}
	if sent[1].cc != types.CCVolumeAbs || sent[1].value != 50 {
		t.Errorf("timeout send: cc=%d val=%d, want volume=50", sent[1].cc, sent[1].value)
	}
}
