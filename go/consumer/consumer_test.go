package consumer

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"vol20toglm/controller"
	"vol20toglm/types"
)

// mockCommander records the last power action sent and any error to return.
type mockCommander struct {
	mu         sync.Mutex
	lastAction string
	returnErr  error
}

func (m *mockCommander) PowerOn(traceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastAction = "on"
	return m.returnErr
}

func (m *mockCommander) PowerOff(traceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastAction = "off"
	return m.returnErr
}

func (m *mockCommander) getLastAction() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAction
}

// mockWriter records MIDI CC messages sent.
type mockWriter struct {
	mu   sync.Mutex
	sent []ccMsg
}

type ccMsg struct {
	channel, cc, value int
}

func (m *mockWriter) SendCC(channel, cc, value int, traceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, ccMsg{channel, cc, value})
	return nil
}

func (m *mockWriter) Close() error { return nil }

func (m *mockWriter) getSent() []ccMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]ccMsg, len(m.sent))
	copy(cp, m.sent)
	return cp
}

func newTestSetup() (context.Context, context.CancelFunc, chan types.Action, *controller.Controller, *mockWriter, *mockCommander, *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	actions := make(chan types.Action, 10)
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 50) // Initialize volume
	mw := &mockWriter{}
	mc := &mockCommander{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return ctx, cancel, actions, ctrl, mw, mc, log
}

func TestConsumer_SetVolume(t *testing.T) {
	ctx, cancel, actions, ctrl, mw, mc, log := newTestSetup()
	defer cancel()

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	actions <- types.Action{
		Kind:      types.KindSetVolume,
		Value:     80,
		Timestamp: time.Now(),
		TraceID:   "test-0001",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	sent := mw.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if sent[0].cc != types.CCVolumeAbs || sent[0].value != 80 {
		t.Errorf("got cc=%d val=%d, want cc=%d val=80", sent[0].cc, sent[0].value, types.CCVolumeAbs)
	}
}

func TestConsumer_AdjustVolume(t *testing.T) {
	ctx, cancel, actions, ctrl, mw, mc, log := newTestSetup()
	defer cancel()

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	actions <- types.Action{
		Kind:      types.KindAdjustVolume,
		Value:     3,
		Timestamp: time.Now(),
		TraceID:   "test-0002",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	sent := mw.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if sent[0].cc != types.CCVolumeAbs || sent[0].value != 53 {
		t.Errorf("got cc=%d val=%d, want cc=%d val=53", sent[0].cc, sent[0].value, types.CCVolumeAbs)
	}
}

func TestConsumer_MuteToggle(t *testing.T) {
	ctx, cancel, actions, ctrl, mw, mc, log := newTestSetup()
	defer cancel()

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	actions <- types.Action{
		Kind:      types.KindSetMute,
		Toggle:    true,
		Timestamp: time.Now(),
		TraceID:   "test-0003",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	sent := mw.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if sent[0].cc != types.CCMute || sent[0].value != 127 {
		t.Errorf("got cc=%d val=%d, want cc=%d val=127", sent[0].cc, sent[0].value, types.CCMute)
	}
}

func TestConsumer_StaleEventDropped(t *testing.T) {
	ctx, cancel, actions, ctrl, mw, mc, log := newTestSetup()
	defer cancel()

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	actions <- types.Action{
		Kind:      types.KindSetVolume,
		Value:     80,
		Timestamp: time.Now().Add(-3 * time.Second),
		TraceID:   "test-stale",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	sent := mw.getSent()
	if len(sent) != 0 {
		t.Errorf("stale event should be dropped, got %d messages", len(sent))
	}
}

func TestConsumer_PowerSettlingBlocks(t *testing.T) {
	ctx, cancel, actions, ctrl, mw, mc, log := newTestSetup()
	defer cancel()

	ctrl.StartPowerTransition(false, "test-power")

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	actions <- types.Action{
		Kind:      types.KindSetVolume,
		Value:     80,
		Timestamp: time.Now(),
		TraceID:   "test-blocked",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	sent := mw.getSent()
	if len(sent) != 0 {
		t.Errorf("command during power settling should be dropped, got %d messages", len(sent))
	}
}

func TestConsumer_PowerToggle(t *testing.T) {
	ctx, cancel, actions, ctrl, mw, mc, log := newTestSetup()
	defer cancel()

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	// Default controller state has Power=true, so Toggle=true → target OFF
	actions <- types.Action{
		Kind:      types.KindSetPower,
		Toggle:    true,
		Timestamp: time.Now(),
		TraceID:   "test-power",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	// Power command goes through Commander, not MIDI writer
	lastAction := mc.getLastAction()
	if lastAction != "off" {
		t.Errorf("expected commander PowerOff (toggle from ON), got %q", lastAction)
	}
	// No MIDI messages should have been sent for the power action
	sent := mw.getSent()
	if len(sent) != 0 {
		t.Errorf("expected 0 MIDI messages for power toggle, got %d", len(sent))
	}
}

func TestConsumer_AdjustVolumeBeforeInit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actions := make(chan types.Action, 10)
	ctrl := controller.New() // Volume NOT initialized
	mw := &mockWriter{}
	mc := &mockCommander{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	go Run(ctx, actions, ctrl, mw, 0, mc, nil, log)

	actions <- types.Action{
		Kind:      types.KindAdjustVolume,
		Value:     1,
		Timestamp: time.Now(),
		TraceID:   "test-noinit",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	sent := mw.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 fallback message, got %d", len(sent))
	}
	if sent[0].cc != types.CCVolUp {
		t.Errorf("got cc=%d, want cc=%d (Vol+ fallback)", sent[0].cc, types.CCVolUp)
	}
}
