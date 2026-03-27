package api

import (
	"log/slog"
	"net/http"
)

// WSHub manages WebSocket connections for real-time state broadcasts.
// This is a minimal stub; full implementation comes in a later phase.
type WSHub struct {
	log *slog.Logger
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub(log *slog.Logger) *WSHub {
	return &WSHub{log: log}
}

// Broadcast sends the current state to all connected WebSocket clients.
func (h *WSHub) Broadcast(state APIState) {}

// HandleUpgrade upgrades an HTTP connection to a WebSocket connection.
func (h *WSHub) HandleUpgrade(w http.ResponseWriter, r *http.Request, currentState APIState) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
