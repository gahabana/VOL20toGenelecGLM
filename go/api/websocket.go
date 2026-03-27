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
