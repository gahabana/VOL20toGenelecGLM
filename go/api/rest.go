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
	VolumeDB               int     `json:"volume_db"`
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
	ctrl       *controller.Controller
	actions    chan<- types.Action
	traceID    *types.TraceIDGenerator
	version    string
	webDir     string
	corsOrigin string
	log        *slog.Logger
	wsHub      *WSHub
}

// NewServer creates a new REST API server.
// webDir is the path to the web/ directory containing index.html and favicon.svg.
// corsOrigin sets the Access-Control-Allow-Origin header ("*" = all, "" = disabled).
func NewServer(ctrl *controller.Controller, actions chan<- types.Action, version, webDir, corsOrigin string, log *slog.Logger) *Server {
	return &Server{
		ctrl:       ctrl,
		actions:    actions,
		traceID:    types.NewTraceIDGenerator(),
		version:    version,
		webDir:     webDir,
		corsOrigin: corsOrigin,
		log:        log,
		wsHub:      NewWSHub(log),
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
	return s.corsMiddleware(mux)
}

// corsMiddleware adds CORS headers when corsOrigin is configured.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	if s.corsOrigin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
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
		Volume:   baseState.Volume,
		VolumeDB: baseState.Volume - types.VolumeDBOffset,
		Mute:     baseState.Mute,
		Dim:      baseState.Dim,
		Power:    baseState.Power,
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
		DB    *int `json:"db"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	var midiValue int
	switch {
	case body.Value != nil && body.DB != nil:
		writeJSONError(w, http.StatusBadRequest, "provide value or db, not both")
		return
	case body.Value != nil:
		midiValue = *body.Value
	case body.DB != nil:
		midiValue = *body.DB + types.VolumeDBOffset
	default:
		writeJSONError(w, http.StatusBadRequest, "missing required field: value or db")
		return
	}

	if midiValue < 0 || midiValue > 127 {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("volume must be 0-127 (or db -127..0), resolved to %d", midiValue))
		return
	}

	if s.checkSettling(w, types.KindSetVolume) {
		return
	}

	action := types.Action{
		Kind:      types.KindSetVolume,
		Value:     midiValue,
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
	// Accept both legacy bool format {"state": true/false} and explicit string
	// format {"state": "on"|"off"|"toggle"}, as well as empty body for toggle.
	var rawBody struct {
		State *json.RawMessage `json:"state"`
	}
	bodyErr := json.NewDecoder(r.Body).Decode(&rawBody)
	if bodyErr != nil && bodyErr.Error() != "EOF" {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+bodyErr.Error())
		return
	}

	if s.checkSettling(w, types.KindSetPower) {
		return
	}

	action := types.Action{
		Kind:      types.KindSetPower,
		Source:    "api",
		TraceID:   s.traceID.Next("api"),
		Timestamp: time.Now(),
	}

	if rawBody.State != nil {
		// Try string first ("on", "off", "toggle")
		var stateStr string
		if err := json.Unmarshal(*rawBody.State, &stateStr); err == nil {
			switch stateStr {
			case "on":
				action.BoolValue = true
			case "off":
				action.BoolValue = false
			case "toggle":
				action.Toggle = true
			default:
				writeJSONError(w, http.StatusBadRequest,
					`state must be "on", "off", or "toggle"`)
				return
			}
		} else {
			// Try bool (legacy format)
			var stateBool bool
			if err := json.Unmarshal(*rawBody.State, &stateBool); err != nil {
				writeJSONError(w, http.StatusBadRequest,
					`state must be a boolean or "on"/"off"/"toggle"`)
				return
			}
			action.BoolValue = stateBool
		}
	} else {
		// No state field → toggle (backwards-compatible default)
		action.Toggle = true
	}

	s.sendAction(w, action)
}

// handleToggleBool handles endpoints that accept either {} (toggle) or {"state": bool}.
func (s *Server) handleToggleBool(w http.ResponseWriter, r *http.Request, kind types.ActionKind) {
	var body struct {
		State *bool `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
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
// the current state (with trace_id) as the response.
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
	resp := struct {
		APIState
		TraceID string `json:"trace_id"`
	}{
		APIState: s.getAPIState(),
		TraceID:  action.TraceID,
	}
	writeJSON(w, http.StatusOK, resp)
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
