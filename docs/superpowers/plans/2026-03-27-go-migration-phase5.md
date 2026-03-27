# Go Migration Phase 5: REST API + WebSocket

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drop-in replacement REST API and WebSocket state broadcast matching the Python FastAPI endpoints, so existing clients (Home Assistant, web UI) work unchanged.

**Architecture:** `net/http` stdlib for routing, `nhooyr.io/websocket` for WebSocket. A `Server` struct holds the controller, action channel, WebSocket client set, and logger. REST handlers create Actions and send them to the shared channel. Controller state callbacks trigger WebSocket broadcast.

**Tech Stack:** Go 1.26, `net/http` (stdlib), `nhooyr.io/websocket`, `encoding/json` (stdlib), existing `controller`, `types` packages.

---

## File Map

| File | Purpose |
|------|---------|
| `go/api/rest.go` | Server struct, REST handlers, state JSON helper |
| `go/api/rest_test.go` | Tests for REST endpoints using httptest |
| `go/api/websocket.go` | WebSocket upgrade handler, client fan-out |
| `go/api/websocket_test.go` | WebSocket connection and broadcast tests |
| `go/main.go` | Wire API server, register state callback |
| `go/go.mod` | Add nhooyr.io/websocket dependency |

---

### Task 1: Implement REST Handlers

All REST endpoints with tests. No WebSocket yet.

**Files:**
- Modify: `go/api/rest.go` (currently just `package api`)
- Create: `go/api/rest_test.go`

- [ ] **Step 1: Add websocket dependency**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go get nhooyr.io/websocket
```

- [ ] **Step 2: Write REST tests**

Create `go/api/rest_test.go`:
```go
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vol20toglm/controller"
	"vol20toglm/types"
)

func newTestServer() (*Server, chan types.Action) {
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 50)
	actions := make(chan types.Action, 10)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(ctrl, actions, "test", log)
	return srv, actions
}

func TestGetState(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var state APIState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}

	if state.Volume != 50 {
		t.Errorf("volume = %d, want 50", state.Volume)
	}
	if state.Power != true {
		t.Error("power should be true")
	}
}

func TestSetVolume(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json", strings.NewReader(`{"value":80}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case a := <-actions:
		if a.Kind != types.KindSetVolume || a.Value != 80 {
			t.Errorf("action = %v/%d, want SetVolume/80", a.Kind, a.Value)
		}
		if a.Source != "api" {
			t.Errorf("source = %q, want api", a.Source)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no action received")
	}
}

func TestAdjustVolume(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume/adjust", "application/json", strings.NewReader(`{"delta":-5}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case a := <-actions:
		if a.Kind != types.KindAdjustVolume || a.Value != -5 {
			t.Errorf("action = %v/%d, want AdjustVolume/-5", a.Kind, a.Value)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no action received")
	}
}

func TestToggleMute(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/mute", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case a := <-actions:
		if a.Kind != types.KindSetMute || !a.Toggle {
			t.Errorf("action = %v toggle=%v, want SetMute/true", a.Kind, a.Toggle)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no action received")
	}
}

func TestSetMuteExplicit(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/mute", "application/json", strings.NewReader(`{"state":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case a := <-actions:
		if a.Kind != types.KindSetMute || a.Toggle || !a.BoolValue {
			t.Errorf("action = %v toggle=%v bool=%v, want SetMute/false/true", a.Kind, a.Toggle, a.BoolValue)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no action received")
	}
}

func TestToggleDim(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/dim", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case a := <-actions:
		if a.Kind != types.KindSetDim || !a.Toggle {
			t.Errorf("action = %v toggle=%v, want SetDim/true", a.Kind, a.Toggle)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no action received")
	}
}

func TestTogglePower(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/power", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case a := <-actions:
		if a.Kind != types.KindSetPower || !a.Toggle {
			t.Errorf("action = %v toggle=%v, want SetPower/true", a.Kind, a.Toggle)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no action received")
	}
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

func TestSetVolume_InvalidJSON(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json", strings.NewReader(`not json`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSetVolume_OutOfRange(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json", strings.NewReader(`{"value":200}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPowerSettling_Returns503(t *testing.T) {
	srv, _ := newTestServer()
	// Start a power transition to trigger settling
	srv.ctrl.StartPowerTransition(false, "test")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json", strings.NewReader(`{"value":50}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	if resp.Header.Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}
}
```

- [ ] **Step 3: Implement rest.go**

Replace `go/api/rest.go` with:
```go
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"vol20toglm/controller"
	"vol20toglm/types"
)

// APIState is the JSON response for state endpoints.
type APIState struct {
	Volume                 int     `json:"volume"`
	Mute                   bool    `json:"mute"`
	Dim                    bool    `json:"dim"`
	Power                  bool    `json:"power"`
	PowerTransitioning     bool    `json:"power_transitioning"`
	PowerSettlingRemaining float64 `json:"power_settling_remaining"`
	PowerCooldown          bool    `json:"power_cooldown"`
	PowerCooldownRemaining float64 `json:"power_cooldown_remaining"`
}

// Server handles REST API and WebSocket connections.
type Server struct {
	ctrl     *controller.Controller
	actions  chan<- types.Action
	traceGen *types.TraceIDGenerator
	version  string
	log      *slog.Logger
	wsHub    *WSHub
}

// NewServer creates a new API server.
func NewServer(ctrl *controller.Controller, actions chan<- types.Action, version string, log *slog.Logger) *Server {
	return &Server{
		ctrl:     ctrl,
		actions:  actions,
		traceGen: types.NewTraceIDGenerator(),
		version:  version,
		log:      log,
		wsHub:    NewWSHub(log),
	}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleGetState)
	mux.HandleFunc("POST /api/volume", s.handleSetVolume)
	mux.HandleFunc("POST /api/volume/adjust", s.handleAdjustVolume)
	mux.HandleFunc("POST /api/mute", s.handleMute)
	mux.HandleFunc("POST /api/dim", s.handleDim)
	mux.HandleFunc("POST /api/power", s.handlePower)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /ws/state", s.handleWebSocket)
	return mux
}

// BroadcastState sends the current state to all WebSocket clients.
// Intended to be called from a controller state callback.
func (s *Server) BroadcastState() {
	state := s.getAPIState()
	s.wsHub.Broadcast(state)
}

func (s *Server) getAPIState() APIState {
	st := s.ctrl.GetState()

	var settling bool
	var settlingRemaining float64
	allowed, wait, reason := s.ctrl.CanAcceptCommand()
	if !allowed && reason == "power_settling" {
		settling = true
		settlingRemaining = wait
	}

	var cooldown bool
	var cooldownRemaining float64
	allowedPower, waitPower, reasonPower := s.ctrl.CanAcceptPowerCommand()
	if !allowedPower {
		if reasonPower == "power_cooldown" {
			cooldown = true
			cooldownRemaining = waitPower
		} else if reasonPower == "power_settling" {
			settling = true
			settlingRemaining = waitPower
		}
	}
	_ = allowed // suppress unused

	return APIState{
		Volume:                 st.Volume,
		Mute:                   st.Mute,
		Dim:                    st.Dim,
		Power:                  st.Power,
		PowerTransitioning:     settling || cooldown,
		PowerSettlingRemaining: settlingRemaining,
		PowerCooldown:          cooldown,
		PowerCooldownRemaining: cooldownRemaining,
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.getAPIState())
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func (s *Server) handleSetVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Value == nil {
		s.writeError(w, http.StatusBadRequest, "missing 'value' field")
		return
	}
	if *body.Value < 0 || *body.Value > 127 {
		s.writeError(w, http.StatusBadRequest, "volume must be 0-127")
		return
	}

	if blocked, retryAfter := s.checkSettling(w, types.KindSetVolume); blocked {
		_ = retryAfter
		return
	}

	s.sendAction(types.Action{
		Kind:      types.KindSetVolume,
		Value:     *body.Value,
		Source:    "api",
		TraceID:   s.traceGen.Next("api"),
		Timestamp: time.Now(),
	})

	s.writeJSON(w, http.StatusOK, s.getAPIState())
}

func (s *Server) handleAdjustVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Delta *int `json:"delta"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Delta == nil {
		s.writeError(w, http.StatusBadRequest, "missing 'delta' field")
		return
	}

	if blocked, _ := s.checkSettling(w, types.KindAdjustVolume); blocked {
		return
	}

	s.sendAction(types.Action{
		Kind:      types.KindAdjustVolume,
		Value:     *body.Delta,
		Source:    "api",
		TraceID:   s.traceGen.Next("api"),
		Timestamp: time.Now(),
	})

	s.writeJSON(w, http.StatusOK, s.getAPIState())
}

func (s *Server) handleMute(w http.ResponseWriter, r *http.Request) {
	s.handleToggleOrSet(w, r, types.KindSetMute)
}

func (s *Server) handleDim(w http.ResponseWriter, r *http.Request) {
	s.handleToggleOrSet(w, r, types.KindSetDim)
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	s.handleToggleOrSet(w, r, types.KindSetPower)
}

func (s *Server) handleToggleOrSet(w http.ResponseWriter, r *http.Request, kind types.ActionKind) {
	var body struct {
		State *bool `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if blocked, _ := s.checkSettling(w, kind); blocked {
		return
	}

	a := types.Action{
		Kind:      kind,
		Source:    "api",
		TraceID:   s.traceGen.Next("api"),
		Timestamp: time.Now(),
	}

	if body.State != nil {
		a.BoolValue = *body.State
	} else {
		a.Toggle = true
	}

	s.sendAction(a)
	s.writeJSON(w, http.StatusOK, s.getAPIState())
}

// checkSettling returns true if the command is blocked by power settling/cooldown.
// If blocked, writes a 503 response with Retry-After header.
func (s *Server) checkSettling(w http.ResponseWriter, kind types.ActionKind) (blocked bool, retryAfter float64) {
	var allowed bool
	var wait float64
	var reason string

	if kind == types.KindSetPower {
		allowed, wait, reason = s.ctrl.CanAcceptPowerCommand()
	} else {
		allowed, wait, reason = s.ctrl.CanAcceptCommand()
	}

	if allowed {
		return false, 0
	}

	w.Header().Set("Retry-After", fmt.Sprintf("%.0f", wait+1))
	s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error":       reason,
		"retry_after": wait,
	})
	return true, wait
}

func (s *Server) sendAction(a types.Action) {
	select {
	case s.actions <- a:
	default:
		s.log.Warn("action channel full, dropping API action", "trace_id", a.TraceID)
	}
}
```

- [ ] **Step 4: Create minimal WSHub stub**

The REST tests reference `NewWSHub`, so create `go/api/websocket.go` with a minimal stub:
```go
package api

import (
	"log/slog"
	"net/http"
)

// WSHub manages WebSocket client connections and broadcasts.
type WSHub struct {
	log *slog.Logger
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub(log *slog.Logger) *WSHub {
	return &WSHub{log: log}
}

// Broadcast sends state to all connected WebSocket clients.
func (h *WSHub) Broadcast(state APIState) {
	// Implemented in Task 2
}

// HandleUpgrade is a placeholder for the WebSocket upgrade handler.
func (h *WSHub) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
```

- [ ] **Step 5: Add handleWebSocket route method to Server**

Add to `rest.go` (after the existing handler methods):
```go
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	s.wsHub.HandleUpgrade(w, r)
}
```

Wait — this is already in the Handler() mux setup. The `handleWebSocket` method needs to be added to the Server. Actually, let me include it inline in the implementation above. Let me re-check... Yes, `s.handleWebSocket` is referenced in `Handler()` but not defined. Add it to rest.go.

- [ ] **Step 6: Run tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./api/ -v -count=1
```

Expected: PASS (all 11 tests).

- [ ] **Step 7: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/api/ go/go.mod go/go.sum
git commit -m "feat(go): implement REST API handlers with 11 tests"
```

---

### Task 2: Implement WebSocket Hub and Broadcast

Full WebSocket implementation: upgrade handler, client tracking, fan-out broadcast.

**Files:**
- Modify: `go/api/websocket.go` (replace stub)
- Create: `go/api/websocket_test.go`

- [ ] **Step 1: Write WebSocket tests**

Create `go/api/websocket_test.go`:
```go
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
	srv := NewServer(ctrl, actions, "test", log)

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

	// Should receive current state on connect
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
	srv := NewServer(ctrl, actions, "test", log)

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

	// Simulate state change
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 75)
	srv.BroadcastState()

	// Should receive updated state
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
	srv := NewServer(ctrl, actions, "test", log)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/state"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Connect two clients
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

	// Both should receive
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
```

- [ ] **Step 2: Implement websocket.go**

Replace `go/api/websocket.go` with:
```go
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const (
	wsWriteTimeout = 5 * time.Second
)

// WSHub manages WebSocket client connections and broadcasts.
type WSHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
	log     *slog.Logger
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub(log *slog.Logger) *WSHub {
	return &WSHub{
		clients: make(map[*websocket.Conn]struct{}),
		log:     log,
	}
}

// Broadcast sends state to all connected WebSocket clients.
// Slow or disconnected clients are removed.
func (h *WSHub) Broadcast(state APIState) {
	data, err := json.Marshal(state)
	if err != nil {
		h.log.Error("failed to marshal state for broadcast", "err", err)
		return
	}

	h.mu.Lock()
	clients := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
		err := c.Write(ctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			h.remove(c)
			c.CloseNow()
		}
	}
}

// HandleUpgrade upgrades an HTTP connection to WebSocket and manages the client lifecycle.
func (h *WSHub) HandleUpgrade(w http.ResponseWriter, r *http.Request, currentState APIState) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.log.Error("websocket upgrade failed", "err", err)
		return
	}

	h.add(conn)
	h.log.Info("websocket client connected", "remote", r.RemoteAddr)

	// Send current state immediately
	data, _ := json.Marshal(currentState)
	ctx, cancel := context.WithTimeout(r.Context(), wsWriteTimeout)
	err = conn.Write(ctx, websocket.MessageText, data)
	cancel()
	if err != nil {
		h.remove(conn)
		conn.CloseNow()
		return
	}

	// Block reading to detect disconnect
	for {
		_, _, err := conn.Read(r.Context())
		if err != nil {
			break
		}
	}

	h.remove(conn)
	h.log.Info("websocket client disconnected", "remote", r.RemoteAddr)
}

func (h *WSHub) add(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *WSHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}
```

- [ ] **Step 3: Update handleWebSocket in rest.go**

Update the `handleWebSocket` method in `rest.go` to pass current state:
```go
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	s.wsHub.HandleUpgrade(w, r, s.getAPIState())
}
```

- [ ] **Step 4: Run all tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go test ./api/ -v -count=1
```

Expected: PASS (11 REST + 3 WebSocket = 14 tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/api/
git commit -m "feat(go): implement WebSocket hub with fan-out broadcast"
```

---

### Task 3: Wire API Server in main.go

Start the HTTP server and register state callback for WebSocket broadcast.

**Files:**
- Modify: `go/main.go`

- [ ] **Step 1: Update main.go**

Read current `go/main.go`, then add the API server setup. The key changes are:
1. Import `vol20toglm/api` and `net/http`
2. Create the API server
3. Register state callback for WebSocket broadcast
4. Start HTTP server in a goroutine
5. Version bump to 0.4.0

Replace `go/main.go` with:
```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"vol20toglm/api"
	"vol20toglm/config"
	"vol20toglm/consumer"
	"vol20toglm/controller"
	"vol20toglm/hid"
	"vol20toglm/types"
)

const version = "0.4.0"

func main() {
	cfg := config.Parse(os.Args[1:])

	var logLevel slog.Level
	switch cfg.LogLevel {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "INFO":
		logLevel = slog.LevelInfo
	default:
		logLevel = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	fmt.Printf("vol20toglm v%s\n", version)
	log.Info("starting",
		"version", version,
		"vid", fmt.Sprintf("0x%04x", cfg.VID),
		"pid", fmt.Sprintf("0x%04x", cfg.PID),
		"midi_in", cfg.MIDIInChannel,
		"midi_out", cfg.MIDIOutChannel,
		"api_port", cfg.APIPort,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Core components
	ctrl := controller.New()
	traceGen := types.NewTraceIDGenerator()
	actions := make(chan types.Action, 100)

	// MIDI output — platform-specific
	midiOut := createMIDIWriter(cfg, log)
	if midiOut != nil {
		defer midiOut.Close()
	}

	// MIDI input — platform-specific
	midiIn := createMIDIReader(cfg, log)
	defer midiIn.Close()

	// Power pattern detector
	midiLog := log.With("component", "midi-in")
	powerDetector := controller.NewPowerPatternDetector(func() {
		newPower := ctrl.TogglePowerFromMIDIPattern()
		midiLog.Info("power pattern detected", "new_power_state", newPower)
	})

	// REST API + WebSocket server
	apiServer := api.NewServer(ctrl, actions, version, log.With("component", "api"))

	// Register state callback for WebSocket broadcast
	ctrl.OnStateChange(func(old, new_ types.State) {
		apiServer.BroadcastState()
	})

	// Acceleration handler
	accel := hid.NewAccelerationHandler(cfg.MinClickTime, cfg.MaxAvgClickTime, cfg.VolumeIncreases)

	// HID reader — platform-specific
	hidReader := createHIDReader(cfg, accel, traceGen, log)

	var wg sync.WaitGroup

	// Start HTTP server
	if cfg.APIPort > 0 {
		httpServer := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.APIPort),
			Handler: apiServer.Handler(),
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("API server listening", "port", cfg.APIPort)
			if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
				log.Error("API server error", "err", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			httpServer.Shutdown(shutdownCtx)
		}()
	}

	// Start consumer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if midiOut == nil {
			log.Warn("no MIDI output, consumer running in dry-run mode")
		}
		consumer.Run(ctx, actions, ctrl, midiOut, 0, log.With("component", "consumer"))
	}()

	// Start HID reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := hidReader.Run(ctx, actions); err != nil && ctx.Err() == nil {
			log.Error("HID reader exited with error", "err", err)
		}
	}()

	// Start MIDI input reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := midiIn.Start(func(channel, cc, value int) {
			now := float64(time.Now().UnixMilli()) / 1000.0

			ccName := types.CCNames[cc]
			if ccName == "" {
				ccName = fmt.Sprintf("CC%d", cc)
			}
			midiLog.Debug("MIDI recv", "cc", ccName, "cc_num", cc, "value", value, "channel", channel)

			changed := ctrl.UpdateFromMIDI(cc, value)
			if changed {
				midiLog.Debug("state updated from MIDI", "cc", ccName, "value", value)
			}

			powerDetector.Feed(cc, value, now)
		})
		if err != nil && ctx.Err() == nil {
			log.Error("MIDI reader exited with error", "err", err)
		}
	}()

	log.Info("running — press Ctrl+C to stop")
	<-ctx.Done()
	log.Info("shutting down")

	cancel()
	midiIn.Close()
	wg.Wait()
	log.Info("shutdown complete")
}
```

- [ ] **Step 2: Verify build and tests**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && go build -o vol20toglm . && go vet ./... && go test ./... -count=1
```

- [ ] **Step 3: Test run on macOS**

```bash
cd /Users/zh/git/VOL20toGenelecGLM/go && timeout 3 ./vol20toglm --log_level DEBUG 2>&1 || true
```

In a separate terminal:
```bash
curl -s http://localhost:8080/api/state | jq .
curl -s http://localhost:8080/api/health | jq .
```

Expected: JSON state response, health check response.

- [ ] **Step 4: Commit**

```bash
cd /Users/zh/git/VOL20toGenelecGLM
git add go/
git commit -m "feat(go): wire REST API + WebSocket server in main.go (v0.4.0)"
```

---

### Task 4: Test on Windows VM

Manual testing with real hardware.

- [ ] **Step 1: Push, pull, build**

```bash
# macOS:
git push

# Windows VM:
git pull
cd go
go build -o vol20toglm.exe .
```

- [ ] **Step 2: Test REST API**

```cmd
vol20toglm.exe --log_level DEBUG
```

In another terminal:
```cmd
curl http://localhost:8080/api/state
curl -X POST http://localhost:8080/api/volume -d "{\"value\":50}"
curl -X POST http://localhost:8080/api/volume/adjust -d "{\"delta\":5}"
curl -X POST http://localhost:8080/api/mute -d "{}"
curl -X POST http://localhost:8080/api/dim -d "{}"
curl http://localhost:8080/api/health
```

Expected: JSON responses, GLM responds to volume/mute/dim commands.

- [ ] **Step 3: Test WebSocket**

Use a WebSocket client or:
```cmd
curl --include --no-buffer -H "Connection: Upgrade" -H "Upgrade: websocket" -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" http://localhost:8080/ws/state
```

Or test from browser console:
```javascript
ws = new WebSocket("ws://localhost:8080/ws/state");
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

Turn the knob — WebSocket should receive state updates.

- [ ] **Step 4: Commit any fixes**

```bash
git add -A && git commit -m "fix(go): adjustments from Phase 5 Windows VM testing"
```

---

## Summary

After completing all 4 tasks:

- **REST API** — 7 endpoints matching Python FastAPI contract, 11 tests
- **WebSocket** — `/ws/state` with fan-out broadcast, 3 tests
- **main.go** — HTTP server with graceful shutdown, state callback wiring, version 0.4.0
- **Tested on Windows VM** — curl + WebSocket + existing clients

**New tests:** 14 (11 REST + 3 WebSocket)
**Running total:** ~63 tests across packages

**Exit criteria:** curl and WebSocket client show live state, can control via API.

**Next phase:** Phase 6 (MQTT + Home Assistant Discovery)
