package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"vol20toglm/controller"
	"vol20toglm/types"
)

func newTestServer() (*Server, chan types.Action) {
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 50) // Initialize volume to 50
	actions := make(chan types.Action, 10)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(ctrl, actions, "test-v1.0", "", "*", log)
	return srv, actions
}

func TestGetState(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var state APIState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if state.Volume != 50 {
		t.Errorf("expected volume=50, got %d", state.Volume)
	}
	if !state.Power {
		t.Error("expected power=true")
	}
	if state.Mute {
		t.Error("expected mute=false")
	}
	if state.Dim {
		t.Error("expected dim=false")
	}
}

func TestSetVolume(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"value": 80}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify action was dispatched
	action := <-actions
	if action.Kind != types.KindSetVolume {
		t.Errorf("expected KindSetVolume, got %v", action.Kind)
	}
	if action.Value != 80 {
		t.Errorf("expected value=80, got %d", action.Value)
	}
	if action.Source != "api" {
		t.Errorf("expected source=api, got %s", action.Source)
	}
}

func TestAdjustVolume(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume/adjust", "application/json",
		strings.NewReader(`{"delta": -5}`))
	if err != nil {
		t.Fatalf("POST /api/volume/adjust failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	action := <-actions
	if action.Kind != types.KindAdjustVolume {
		t.Errorf("expected KindAdjustVolume, got %v", action.Kind)
	}
	if action.Value != -5 {
		t.Errorf("expected value=-5, got %d", action.Value)
	}
	if action.Source != "api" {
		t.Errorf("expected source=api, got %s", action.Source)
	}
}

func TestToggleMute(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/mute", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/mute failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	action := <-actions
	if action.Kind != types.KindSetMute {
		t.Errorf("expected KindSetMute, got %v", action.Kind)
	}
	if !action.Toggle {
		t.Error("expected Toggle=true for empty body")
	}
}

func TestSetMuteExplicit(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/mute", "application/json",
		strings.NewReader(`{"state": true}`))
	if err != nil {
		t.Fatalf("POST /api/mute failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	action := <-actions
	if action.Kind != types.KindSetMute {
		t.Errorf("expected KindSetMute, got %v", action.Kind)
	}
	if action.Toggle {
		t.Error("expected Toggle=false for explicit state")
	}
	if !action.BoolValue {
		t.Error("expected BoolValue=true")
	}
}

func TestToggleDim(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/dim", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/dim failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	action := <-actions
	if action.Kind != types.KindSetDim {
		t.Errorf("expected KindSetDim, got %v", action.Kind)
	}
	if !action.Toggle {
		t.Error("expected Toggle=true for empty body")
	}
}

func TestTogglePower(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/power", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/power failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	action := <-actions
	if action.Kind != types.KindSetPower {
		t.Errorf("expected KindSetPower, got %v", action.Kind)
	}
	if !action.Toggle {
		t.Error("expected Toggle=true for empty body")
	}
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %s", body["status"])
	}
	if body["version"] != "test-v1.0" {
		t.Errorf("expected version=test-v1.0, got %s", body["version"])
	}
}

func TestSetVolume_InvalidJSON(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`not json`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.StatusCode)
	}
}

func TestSetVolume_OutOfRange(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"value": 200}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestPowerSettling_Returns503(t *testing.T) {
	srv, _ := newTestServer()
	// Trigger a power transition to put the controller in settling state
	srv.ctrl.StartPowerTransition(false, "test-power-001")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"value": 60}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", resp.StatusCode)
	}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Error("expected Retry-After header")
	}
}

// --- P1: CORS tests ---

func TestCORS_Preflight(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/state", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /api/state failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected CORS origin *, got %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("expected Access-Control-Allow-Methods header")
	}
}

func TestCORS_HeaderOnGET(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state failed: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected CORS origin *, got %q", got)
	}
}

func TestCORS_Disabled(t *testing.T) {
	ctrl := controller.New()
	ctrl.UpdateFromMIDI(types.CCVolumeAbs, 50)
	actions := make(chan types.Action, 10)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(ctrl, actions, "test-v1.0", "", "", log) // empty = CORS disabled

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state failed: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no CORS header when disabled, got %q", got)
	}
}

// --- P2: trace_id tests ---

func TestSetVolume_ReturnsTraceID(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"value": 80}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	traceID, ok := body["trace_id"].(string)
	if !ok || traceID == "" {
		t.Error("expected non-empty trace_id in POST response")
	}
}

func TestGetState_NoTraceID(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state failed: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, exists := body["trace_id"]; exists {
		t.Error("GET /api/state should NOT contain trace_id")
	}
}

// --- P3: dB volume tests ---

func TestSetVolume_DB(t *testing.T) {
	srv, actions := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"db": -47}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	action := <-actions
	if action.Value != 80 {
		t.Errorf("expected MIDI value 80 for db=-47, got %d", action.Value)
	}
}

func TestSetVolume_DBAndValue_Error(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"value": 80, "db": -47}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.StatusCode)
	}
}

func TestSetVolume_DBOutOfRange(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/volume", "application/json",
		strings.NewReader(`{"db": 1}`))
	if err != nil {
		t.Fatalf("POST /api/volume failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400 for db=1 (resolves to 128), got %d", resp.StatusCode)
	}
}

func TestGetState_IncludesVolumeDB(t *testing.T) {
	srv, _ := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state failed: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	volumeDB, ok := body["volume_db"].(float64)
	if !ok {
		t.Fatal("expected volume_db field in state response")
	}
	// Volume initialized to 50, so volume_db = 50 - 127 = -77
	if int(volumeDB) != -77 {
		t.Errorf("expected volume_db=-77, got %d", int(volumeDB))
	}
}
