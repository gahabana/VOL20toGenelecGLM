package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"vol20toglm/controller"
	"vol20toglm/types"
)

func TestWebSocket_ConnectReceivesState(t *testing.T) {
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 65)
	actions := make(chan types.Action, 10)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(ctrl, actions, "test", "", log)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/state"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	_, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var state APIState
	if err := json.Unmarshal(msg, &state); err != nil {
		t.Fatal(err)
	}
	if state.Volume != 65 {
		t.Errorf("volume = %d, want 65", state.Volume)
	}

	conn.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocket_BroadcastOnStateChange(t *testing.T) {
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 50)
	actions := make(chan types.Action, 10)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(ctrl, actions, "test", "", log)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/state"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	// Read initial state
	_, _, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate state change and broadcast
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 75)
	srv.BroadcastState()

	_, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var state APIState
	if err := json.Unmarshal(msg, &state); err != nil {
		t.Fatal(err)
	}
	if state.Volume != 75 {
		t.Errorf("volume = %d, want 75", state.Volume)
	}

	conn.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocket_MultipleClients(t *testing.T) {
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 50)
	actions := make(chan types.Action, 10)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(ctrl, actions, "test", "", log)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/state"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.CloseNow()

	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.CloseNow()

	// Read initial state from both
	conn1.Read(ctx)
	conn2.Read(ctx)

	// Broadcast
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 99)
	srv.BroadcastState()

	_, msg1, err := conn1.Read(ctx)
	if err != nil {
		t.Fatal("client1 read:", err)
	}
	_, msg2, err := conn2.Read(ctx)
	if err != nil {
		t.Fatal("client2 read:", err)
	}

	var s1, s2 APIState
	json.Unmarshal(msg1, &s1)
	json.Unmarshal(msg2, &s2)

	if s1.Volume != 99 || s2.Volume != 99 {
		t.Errorf("volumes = %d, %d, want 99, 99", s1.Volume, s2.Volume)
	}

	conn1.Close(websocket.StatusNormalClosure, "")
	conn2.Close(websocket.StatusNormalClosure, "")
}
