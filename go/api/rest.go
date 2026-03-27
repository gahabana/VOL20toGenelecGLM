package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"vol20toglm/controller"
	"vol20toglm/types"
)

// APIState is the JSON response returned by all state endpoints.
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

// Server handles REST API requests for GLM control.
type Server struct {
	ctrl    *controller.Controller
	actions chan<- types.Action
	traceID *types.TraceIDGenerator
	version string
	webDir  string
	log     *slog.Logger
	wsHub   *WSHub
}

// NewServer creates a new REST API server.
// webDir is the path to the web/ directory containing index.html and favicon.svg.
func NewServer(ctrl *controller.Controller, actions chan<- types.Action, version, webDir string, log *slog.Logger) *Server {
	return &Server{
		ctrl:    ctrl,
		actions: actions,
		traceID: types.NewTraceIDGenerator(),
		version: version,
		webDir:  webDir,
		log:     log,
		wsHub:   NewWSHub(log),
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
	mux.HandleFunc("GET /favicon.svg", s.handleFavicon)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

// BroadcastState sends the current state to all WebSocket clients.
// Intended for use as a state change callback.
func (s *Server) BroadcastState() {
	apiState := s.getAPIState()
	s.wsHub.Broadcast(apiState)
}

// getAPIState computes the full API state from the controller.
func (s *Server) getAPIState() APIState {
	baseState := s.ctrl.GetState()

	apiState := APIState{
		Volume: baseState.Volume,
		Mute:   baseState.Mute,
		Dim:    baseState.Dim,
		Power:  baseState.Power,
	}

	// Check power settling (blocks all commands)
	canAccept, settlingWait, settlingReason := s.ctrl.CanAcceptCommand()
	if !canAccept && settlingReason == "power_settling" {
		apiState.PowerTransitioning = true
		apiState.PowerSettlingRemaining = roundTo(settlingWait, 2)
	}

	// Check power cooldown (blocks only power commands)
	canPower, cooldownWait, cooldownReason := s.ctrl.CanAcceptPowerCommand()
	if !canPower {
		if cooldownReason == "power_cooldown" {
			apiState.PowerCooldown = true
			apiState.PowerCooldownRemaining = roundTo(cooldownWait, 2)
		} else if cooldownReason == "power_settling" {
			// During settling, the cooldown remaining includes settling time
			apiState.PowerTransitioning = true
			apiState.PowerSettlingRemaining = roundTo(settlingWait, 2)
			apiState.PowerCooldownRemaining = roundTo(cooldownWait, 2)
		}
	}

	return apiState
}

// checkSettling returns true (and writes a 503 response) if the system is in
// power settling and cannot accept commands. For power commands, it also checks
// the cooldown period.
func (s *Server) checkSettling(w http.ResponseWriter, actionKind types.ActionKind) bool {
	// All commands blocked during power settling
	canAccept, waitSeconds, reason := s.ctrl.CanAcceptCommand()
	if !canAccept {
		retryAfter := int(math.Ceil(waitSeconds))
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		writeJSONError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("blocked: %s (%.1fs remaining)", reason, waitSeconds))
		return true
	}

	// Power commands also blocked during cooldown
	if actionKind == types.KindSetPower {
		canPower, powerWait, powerReason := s.ctrl.CanAcceptPowerCommand()
		if !canPower {
			retryAfter := int(math.Ceil(powerWait))
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			writeJSONError(w, http.StatusServiceUnavailable,
				fmt.Sprintf("blocked: %s (%.1fs remaining)", powerReason, powerWait))
			return true
		}
	}

	return false
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.getAPIState())
}

func (s *Server) handleSetVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Value == nil {
		writeJSONError(w, http.StatusBadRequest, "missing required field: value")
		return
	}
	if *body.Value < 0 || *body.Value > 127 {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("volume must be 0-127, got %d", *body.Value))
		return
	}

	if s.checkSettling(w, types.KindSetVolume) {
		return
	}

	action := types.Action{
		Kind:      types.KindSetVolume,
		Value:     *body.Value,
		Source:    "api",
		TraceID:   s.traceID.Next("api"),
		Timestamp: time.Now(),
	}
	s.sendAction(w, action)
}

func (s *Server) handleAdjustVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Delta *int `json:"delta"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Delta == nil {
		writeJSONError(w, http.StatusBadRequest, "missing required field: delta")
		return
	}

	if s.checkSettling(w, types.KindAdjustVolume) {
		return
	}

	action := types.Action{
		Kind:      types.KindAdjustVolume,
		Value:     *body.Delta,
		Source:    "api",
		TraceID:   s.traceID.Next("api"),
		Timestamp: time.Now(),
	}
	s.sendAction(w, action)
}

func (s *Server) handleMute(w http.ResponseWriter, r *http.Request) {
	s.handleToggleBool(w, r, types.KindSetMute)
}

func (s *Server) handleDim(w http.ResponseWriter, r *http.Request) {
	s.handleToggleBool(w, r, types.KindSetDim)
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	s.handleToggleBool(w, r, types.KindSetPower)
}

// handleToggleBool handles endpoints that accept either {} (toggle) or {"state": bool}.
func (s *Server) handleToggleBool(w http.ResponseWriter, r *http.Request, kind types.ActionKind) {
	var body struct {
		State *bool `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if s.checkSettling(w, kind) {
		return
	}

	action := types.Action{
		Kind:      kind,
		Source:    "api",
		TraceID:   s.traceID.Next("api"),
		Timestamp: time.Now(),
	}

	if body.State != nil {
		action.BoolValue = *body.State
	} else {
		action.Toggle = true
	}

	s.sendAction(w, action)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if s.webDir == "" {
		http.Error(w, "web UI not configured", http.StatusNotFound)
		return
	}
	path := filepath.Join(s.webDir, "index.html")
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	if s.webDir == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.webDir, "favicon.svg"))
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	s.wsHub.HandleUpgrade(w, r, s.getAPIState())
}

// sendAction attempts a non-blocking send on the actions channel and writes
// the current state as the response.
func (s *Server) sendAction(w http.ResponseWriter, action types.Action) {
	select {
	case s.actions <- action:
		s.log.Debug("action dispatched",
			"kind", action.Kind,
			"source", action.Source,
			"traceID", action.TraceID,
		)
	default:
		s.log.Warn("action channel full, dropping action",
			"kind", action.Kind,
			"traceID", action.TraceID,
		)
	}
	writeJSON(w, http.StatusOK, s.getAPIState())
}

// writeJSON marshals data as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// At this point headers are already sent, so we can only log
		slog.Error("failed to encode JSON response", "error", err)
	}
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// roundTo rounds a float to n decimal places.
func roundTo(value float64, decimals int) float64 {
	multiplier := math.Pow(10, float64(decimals))
	return math.Round(value*multiplier) / multiplier
}
