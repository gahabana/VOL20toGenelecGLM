# Go Migration Phase 5: REST API + WebSocket Design

## Overview

Drop-in replacement for the Python FastAPI REST API and WebSocket state broadcast. All endpoints, paths, and JSON shapes match the existing Python implementation so existing clients (Home Assistant, web UI) work unchanged.

## Endpoints

### REST

| Method | Path | Request Body | Response | Notes |
|--------|------|-------------|----------|-------|
| `GET` | `/api/state` | — | State JSON | Current GLM state |
| `POST` | `/api/volume` | `{"value": int}` | State JSON | Set absolute volume (0-127) |
| `POST` | `/api/volume/adjust` | `{"delta": int}` | State JSON | Relative volume change |
| `POST` | `/api/mute` | `{}` or `{"state": bool}` | State JSON | Toggle or set mute |
| `POST` | `/api/dim` | `{}` or `{"state": bool}` | State JSON | Toggle or set dim |
| `POST` | `/api/power` | `{}` or `{"state": bool}` | State JSON | Toggle or set power |
| `GET` | `/api/health` | — | `{"status":"ok","version":"0.4.0"}` | Health check |

All POST endpoints return the updated state JSON after applying the action. If the action is blocked by power settling/cooldown, return HTTP 503 with `Retry-After` header and JSON error body `{"error": "reason", "retry_after": seconds}`.

### WebSocket

| Path | Protocol | Notes |
|------|----------|-------|
| `WS /ws/state` | JSON frames | Real-time state broadcast |

On connect: send current state immediately. On any state change: broadcast updated state to all connected clients.

## State JSON Shape

```json
{
  "volume": 83,
  "mute": false,
  "dim": false,
  "power": true,
  "power_transitioning": false,
  "power_settling_remaining": 0.0,
  "power_cooldown": false,
  "power_cooldown_remaining": 0.0
}
```

The `power_transitioning`, `power_settling_remaining`, `power_cooldown`, and `power_cooldown_remaining` fields come from the controller's power state. These are computed at response time from the controller's `CanAcceptCommand()` and `CanAcceptPowerCommand()` methods.

## Architecture

### Package: `api/rest.go`

Single file implementing all HTTP handlers and WebSocket management.

**Server struct:**
```go
type Server struct {
    ctrl    *controller.Controller
    actions chan<- types.Action
    clients *wsClients
    log     *slog.Logger
    version string
}
```

**HTTP routing** uses `net/http` stdlib `ServeMux`. No framework.

**WebSocket** uses `nhooyr.io/websocket` (pure Go, maintained, per design spec).

### Action Flow

REST POST handlers create `types.Action` structs and send them to the same `chan Action` channel that HID uses. The consumer goroutine processes them identically — no special path for API actions.

```
POST /api/volume {"value": 80}
    → Action{Kind: KindSetVolume, Value: 80, Source: "api", TraceID: "api-0001"}
    → actions channel
    → Consumer → Controller → MIDI out
    → Controller state callback → WebSocket broadcast
```

### WebSocket Fan-Out

```go
type wsClients struct {
    mu      sync.Mutex
    clients map[*websocket.Conn]struct{}
}
```

- `Add(conn)` / `Remove(conn)` protected by mutex
- Controller `OnStateChange` callback iterates all clients, writes JSON
- Each `Write` has a 5-second timeout — if a client is slow, it gets dropped
- On connect: send current state immediately, then block reading (to detect disconnect)

### Error Handling

- Invalid JSON body: 400 Bad Request
- Volume out of range: 400 Bad Request
- Power settling blocks command: 503 Service Unavailable with `Retry-After` header
- WebSocket upgrade failure: logged, connection closed

### No CORS, No Auth

Matches Python behavior. The API binds to `0.0.0.0:{port}` (configurable via `--api_port`, default 8080).

## Dependencies

- `nhooyr.io/websocket` — WebSocket library (pure Go, no cgo)

## Testing Strategy

REST endpoints are testable on macOS using `httptest.NewServer`. Tests use a real controller + mock MIDI writer (same pattern as consumer tests). WebSocket tests use the websocket library's dial function against the test server.

## Exit Criteria

- `curl localhost:8080/api/state` returns correct JSON
- `curl -X POST localhost:8080/api/volume -d '{"value":50}'` changes volume
- WebSocket client at `ws://localhost:8080/ws/state` receives live state updates
- Existing clients (Home Assistant, web UI) work unchanged against the Go binary
