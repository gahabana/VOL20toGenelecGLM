# REST API Service Design — Making vol20toglm a Valid Integration Target

## Current State

The REST API exposes 6 control endpoints + 1 WebSocket stream on `net/http` (default port 8080).

### Endpoints

| Endpoint | Method | Body | Purpose |
|---|---|---|---|
| `/api/state` | GET | — | Current state snapshot |
| `/api/volume` | POST | `{"value": 0-127}` | Set absolute volume |
| `/api/volume/adjust` | POST | `{"delta": N}` | Relative volume change |
| `/api/mute` | POST | `{}` or `{"state": bool}` | Toggle or set mute |
| `/api/dim` | POST | `{}` or `{"state": bool}` | Toggle or set dim |
| `/api/power` | POST | `{}`, `{"state": bool}`, or `{"state": "on"/"off"/"toggle"}` | Power control |
| `/api/health` | GET | — | Version + status |
| `/ws/state` | GET | — | WebSocket: real-time state push |

### Architecture

```
HTTP request → validate → Action{} → chan (non-blocking) → consumer → MIDI → GLM
                                                                          ↓
WebSocket ← broadcast ← controller.OnStateChange ← MIDI response ←──────┘
```

Key properties:
- Non-blocking dispatch: HTTP returns 200 immediately with current state
- Power settling: returns 503 + Retry-After during transition lockout
- WebSocket pushes same `APIState` JSON on every state change
- Trace IDs generated per request but not exposed to clients

### What Works Well

1. **Non-blocking fire-and-forget** — HTTP server can't be stalled by slow GLM
2. **503 + Retry-After** for power transitions — correct HTTP semantics
3. **WebSocket push** — no polling needed, dead clients cleaned up
4. **Consistent state shape** — same `APIState` struct everywhere
5. **Toggle-or-set pattern** — pragmatic for both UIs and automation

---

## Gaps for Third-Party Integration

### G1: No CORS headers

**Impact:** Any browser-based app on a different origin is blocked.

**Severity:** Blocking — this prevents the most common integration scenario.

### G2: No API versioning

**Impact:** If state shape changes, existing clients break silently. No way for clients to detect incompatibility.

**Severity:** Low today (one consumer), high once others depend on it.

### G3: No trace_id in POST responses

**Impact:** Client sends a command, gets back current state (pre-change). No way to correlate "I sent this" with "this happened" without watching WebSocket and guessing.

**Severity:** Medium — functional but frustrating for automation scripts.

### G4: Volume only accepts MIDI range (0-127)

**Impact:** API consumers must know this is MIDI underneath. MQTT already accepts dB (-127 to 0). Inconsistent across interfaces.

**Severity:** Low — usable but leaky abstraction.

### G5: No authentication

**Impact:** Anyone on the network can control the speakers.

**Severity:** Low for LAN use. Becomes critical if API is ever exposed beyond local network.

### G6: No machine-readable API spec

**Impact:** Third-party developers must read Go source to understand the API contract.

**Severity:** Low — 6 endpoints are simple enough to document by hand.

---

## Open Decisions

These must be resolved before or during implementation. Each has a recommendation but needs explicit sign-off.

### D1: Delete legacy Python code?

The Python codebase (`bridge2glm.py`, `api/rest.py`, `api/mqtt.py`, `acceleration.py`, `config.py`, `retry_logger.py`, `logging_setup.py`, `midi_constants.py`, `glm_midi_test.py`, `glm_core/`, `PowerOnOff/`) is dead code — the Go binary is production. Keeping it creates confusion about which API server is real.

| Option | Pros | Cons |
|---|---|---|
| **A) Delete** | Eliminates maintenance hazard, no false impressions | History only in git log |
| **B) Keep, mark legacy** | Easy to reference | Someone may accidentally run it |
| **C) Keep in sync** | Two implementations | High cost for dead code |

**Decision: Keep Python.** The functionality gap is small and Python may be brought to parity with Go. API changes in Go should be documented clearly so Python can follow. Python code is NOT a consumer of the Go API — it's a parallel implementation, so Go changes won't break it. But any new API contract (dB volume, trace_id) should eventually be mirrored in the Python API for consistency.

### D2: CORS — wildcard or configurable?

| Option | Pros | Cons |
|---|---|---|
| **A) Hardcode `*`** (recommended) | Zero config, correct for LAN appliance | Can't restrict if exposed |
| **B) `--cors_origin` flag, default `*`** | Configurable for paranoid setups | Extra flag nobody uses |

### D3: API versioning — now or defer?

No external consumers exist today. Adding `/api/v1/` doubles route registrations.

| Option | Pros | Cons |
|---|---|---|
| **A) Defer** (recommended) | No complexity now, add when contract breaks | Costs more to retrofit later |
| **B) Add now** | Free insurance, low cost | Premature if API never breaks |

### D4: trace_id scope — POST responses only, or also WebSocket?

| Option | Pros | Cons |
|---|---|---|
| **A) POST only** (recommended) | Clean separation, WS stays read-only state | Scripts must correlate across two channels |
| **B) Add to APIState (visible on WS too)** | One place to look | Noisy — every broadcast carries a stale trace_id |

### D5: Web UI volume_db — use server value or keep client-side calc?

`web/index.html` line 677 computes `volume - 127` locally. After P3, the server provides `volume_db`.

| Option | Pros | Cons |
|---|---|---|
| **A) Switch to `state.volume_db`** (recommended) | Single source of truth | Minor regression risk if field missing |
| **B) Keep client-side calc** | No change, no risk | Duplicated logic, diverges if mapping changes |

### D6: Auth — exempt same-origin web UI?

Only relevant if P6 (auth) is implemented.

| Option | Pros | Cons |
|---|---|---|
| **A) Exempt same-origin** (recommended) | Web UI works without key injection | Need origin detection logic |
| **B) Require key everywhere** | Simple, uniform | Web UI needs key injected into HTML |
| **C) Read-only without key, write requires key** | GET/WS open, POST protected | Most pragmatic split |

### D7: Shared dB offset constant?

`127` appears in `mqtt/mqtt.go` (lines 37, 162) and will appear in `api/rest.go`. Extract to `types.VolumeDBOffset`?

| Option | Pros | Cons |
|---|---|---|
| **A) Extract constant** (recommended) | Single source of truth, three consumers | One more thing in types package |
| **B) Keep inline** | Simple, obvious | Silent divergence if someone changes one |

---

## Implementation Plan

### Phase 1: Unblock third-party apps (P1-P3)

These three changes unblock real integration use cases with minimal risk.

#### P1: CORS middleware

**File:** `go/api/rest.go`

**What:** Wrap the `http.Handler` returned by `Handler()` with CORS headers.

**Implementation:**

Add a `corsMiddleware` function in `rest.go`:

```go
func corsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

        if r.Method == http.MethodOptions {
            w.WriteHeader(http.StatusNoContent)
            return
        }

        next.ServeHTTP(w, r)
    })
}
```

Update `Handler()` return:

```go
func (s *Server) Handler() http.Handler {
    mux := http.NewServeMux()
    // ... register routes ...
    return corsMiddleware(mux)
}
```

**Config:** Add `--cors_origin` flag (default `"*"`). Empty string disables CORS headers.

**Tests:** Add test that OPTIONS returns 204 + headers. Add test that GET/POST includes Allow-Origin.

#### P2: Return trace_id in POST responses

**File:** `go/api/rest.go`

**What:** Include the `trace_id` in the JSON response body so clients can correlate commands with WebSocket state updates.

**Implementation:**

Add `TraceID` to the response. Change `sendAction` to:

```go
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
```

**Consideration:** This changes the POST response shape (adds `trace_id` field). Since no versioning exists yet, this is additive and non-breaking — existing clients that don't look for `trace_id` are unaffected.

**Tests:** Add test that POST responses contain `trace_id` field.

#### P3: Accept dB values in volume endpoint

**File:** `go/api/rest.go`

**What:** `POST /api/volume` accepts either `{"value": 0-127}` (raw MIDI) or `{"db": -127..0}` (decibels, same as MQTT).

**Implementation:**

Change `handleSetVolume` body struct and logic:

```go
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
        midiValue = *body.DB + 127 // -127→0, -47→80, 0→127
    default:
        writeJSONError(w, http.StatusBadRequest, "missing required field: value or db")
        return
    }

    if midiValue < 0 || midiValue > 127 {
        writeJSONError(w, http.StatusBadRequest,
            fmt.Sprintf("volume must be 0-127 (or db -127..0), resolved to %d", midiValue))
        return
    }

    // ... rest unchanged, use midiValue ...
}
```

**Also:** Add `VolumeDB` field to `APIState` (matches MQTT's `volume_db`):

```go
type APIState struct {
    Volume                 int     `json:"volume"`
    VolumeDB               int     `json:"volume_db"`
    // ... rest unchanged ...
}
```

And in `getAPIState`:

```go
apiState := APIState{
    Volume:   baseState.Volume,
    VolumeDB: baseState.Volume - 127,
    // ...
}
```

**Tests:** Add test for `{"db": -47}` → MIDI 80. Add test for mutual exclusion error. Add test that `volume_db` appears in GET state.

### Phase 2: API hygiene (P4-P5)

#### P4: Version prefix

**File:** `go/api/rest.go`

**What:** Add `/api/v1/` prefix to all API routes. Keep `/api/` routes as aliases for backwards compatibility during transition.

**Implementation:**

```go
func (s *Server) Handler() http.Handler {
    mux := http.NewServeMux()

    // v1 routes (canonical)
    mux.HandleFunc("GET /api/v1/state", s.handleGetState)
    mux.HandleFunc("POST /api/v1/volume", s.handleSetVolume)
    mux.HandleFunc("POST /api/v1/volume/adjust", s.handleAdjustVolume)
    mux.HandleFunc("POST /api/v1/mute", s.handleMute)
    mux.HandleFunc("POST /api/v1/dim", s.handleDim)
    mux.HandleFunc("POST /api/v1/power", s.handlePower)
    mux.HandleFunc("GET /api/v1/health", s.handleHealth)

    // Legacy routes (redirect to v1)
    mux.HandleFunc("GET /api/state", s.handleGetState)
    mux.HandleFunc("POST /api/volume", s.handleSetVolume)
    mux.HandleFunc("POST /api/volume/adjust", s.handleAdjustVolume)
    mux.HandleFunc("POST /api/mute", s.handleMute)
    mux.HandleFunc("POST /api/dim", s.handleDim)
    mux.HandleFunc("POST /api/power", s.handlePower)
    mux.HandleFunc("GET /api/health", s.handleHealth)

    // WebSocket and static (unversioned)
    mux.HandleFunc("GET /ws/state", s.handleWebSocket)
    mux.HandleFunc("GET /favicon.svg", s.handleFavicon)
    mux.HandleFunc("GET /", s.handleIndex)

    return corsMiddleware(mux)
}
```

**Note:** Legacy routes can be removed in a future major version. WebSocket stays unversioned — the state shape is the contract, not the URL.

#### P5: OpenAPI spec

**File:** `go/api/openapi.yaml` (new file, hand-written)

**What:** Machine-readable API spec covering all endpoints, request/response shapes, and error formats. Served at `GET /api/v1/openapi.yaml`.

**Implementation:** Hand-write the YAML. Add one route:

```go
mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
    http.ServeFile(w, r, filepath.Join(s.webDir, "openapi.yaml"))
})
```

**Scope:** Document all endpoints, `APIState` schema, error response shape `{"error": "string"}`, 503 Retry-After behavior, and WebSocket message format.

### Phase 3: Security (P6, when needed)

#### P6: Optional API key authentication

**Files:** `go/api/rest.go`, `go/config/config.go`

**What:** `--api_key` CLI flag. When set, all POST endpoints require `Authorization: Bearer <key>` header. GET endpoints and WebSocket remain open (read-only).

**Implementation:**

```go
// In config.go:
fs.StringVar(&cfg.APIKey, "api_key", "", "API key for POST endpoints (empty = no auth)")

// In rest.go:
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
    if s.apiKey == "" {
        return next
    }
    return func(w http.ResponseWriter, r *http.Request) {
        auth := r.Header.Get("Authorization")
        if auth != "Bearer "+s.apiKey {
            writeJSONError(w, http.StatusUnauthorized, "invalid or missing API key")
            return
        }
        next.ServeHTTP(w, r)
    }
}
```

Wrap only POST handlers: `mux.HandleFunc("POST /api/v1/volume", s.authMiddleware(s.handleSetVolume))`

**Not needed until:** API is exposed beyond LAN or untrusted clients are on the network.

---

## Implementation Order and Dependencies

```
P1 (CORS) ──────────┐
P2 (trace_id) ──────┤── can be done in parallel, no dependencies
P3 (dB volume) ─────┘
         │
         ▼
P4 (versioning) ───── depends on P1 (wrap corsMiddleware in Handler)
         │
         ▼
P5 (OpenAPI) ──────── depends on P2, P3, P4 (spec must reflect final shape)
         │
         ▼
P6 (auth) ─────────── independent, do when needed
```

**Estimated scope:** P1-P3 are ~50 lines of code total + tests. P4 is ~20 lines (route duplication). P5 is a YAML file. P6 is ~30 lines + config flag.

---

## State Response Shape After All Changes

```json
{
  "volume": 80,
  "volume_db": -47,
  "mute": false,
  "dim": false,
  "power": true,
  "power_transitioning": false,
  "power_settling_remaining": 0,
  "power_cooldown": false,
  "power_cooldown_remaining": 0,
  "trace_id": "api-0042"
}
```

Note: `trace_id` only present in POST responses, not GET /api/state or WebSocket pushes.

---

## Ripple Effects — Other Files That Must Change

The implementation plan above focuses on `go/api/rest.go`, but several other consumers depend on the API contract and must be updated in lockstep.

### Legacy Python Code: `api/rest.py`, `api/mqtt.py`, `bridge2glm.py`

The Go binary has fully replaced the Python stack (README: "Built as a single Go binary"). However, the Python code still exists in the repo:

- `bridge2glm.py` (line 1351): `from api import start_api_server`
- `bridge2glm.py` (line 1356): `from api.mqtt import start_mqtt_client`
- `api/rest.py`: FastAPI server with identical endpoint paths (`/api/state`, `/api/volume`, etc.)
- `api/mqtt.py`: Paho MQTT client with same HA Discovery config and `volume_db` field

**Decision needed before implementing:** Either:
- **A) Delete the Python API/MQTT code** — it's dead code now that Go is production. Eliminates a maintenance hazard where someone runs the Python version and hits a different API contract.
- **B) Keep but mark as legacy** — add a note that Python code is not maintained and may diverge from Go API.
- **C) Keep in sync** — update Python API to match Go changes. High maintenance cost for dead code.

**Recommendation:** Option A. The Python files are the pre-migration implementation. Keeping them creates a false impression that two API servers exist. If someone needs the Python version's history, it's in git.

**No changes needed for Go API implementation regardless** — the Python code doesn't consume the Go API; it's a parallel (dead) implementation.

### Documentation: `README.md`, `go/README.md`

`README.md` lines 153-161 document the current endpoint paths and request bodies:
```
| GET  | /api/state         | Current state (JSON)                                     |
| POST | /api/volume        | Set volume: {"value": 0-127}                             |
| POST | /api/volume/adjust | Adjust volume: {"delta": int}                            |
| POST | /api/mute          | Toggle mute, or set: {"state": bool}                     |
| POST | /api/dim           | Toggle dim, or set: {"state": bool}                      |
| POST | /api/power         | {"state": "on"}, {"state": "off"}, {"state": "toggle"}   |
| GET  | /api/health        | Health check                                             |
```

**Changes needed per phase:**
- **P3:** Add `{"db": -127..0}` as alternative for volume endpoint. Add `volume_db` to state response description.
- **P4:** Document `/api/v1/` prefix as canonical, note legacy aliases.
- **P6:** Document `--api_key` flag and `Authorization: Bearer` header.

### Migration planning docs

Several docs under `docs/superpowers/` reference API endpoints as part of Go migration specs:
- `docs/superpowers/plans/2026-03-27-go-migration-phase5.md`
- `docs/superpowers/specs/2026-03-27-go-migration-phase5-design.md`
- `docs/superpowers/plans/2026-03-27-go-migration-phase7.md`
- `docs/superpowers/specs/2026-03-27-go-migration-phase7-design.md`

These are historical planning artifacts (migration is complete). **No changes needed** — they describe what was built, not what should be.

### Web UI: `web/index.html`

The single-page web app uses the REST API and WebSocket directly.

**P1 (CORS):** No change needed — the web UI is served from the same origin (same port), so CORS doesn't apply to it.

**P2 (trace_id):** No change needed — the web UI fires POST requests fire-and-forget and relies on WebSocket for state updates. It does not inspect POST response bodies beyond checking HTTP status codes (lines 989, 1004 check `response.status === 503`). The added `trace_id` field is ignored by existing `JSON.parse`.

**P3 (dB volume):** Two changes needed:

1. **State display already handles dB locally** (line 677-683):
   ```js
   const volumeDb = volume - 127;
   ```
   Once `volume_db` is in the API response, the web UI should use `state.volume_db` directly instead of computing it client-side. This keeps the dB mapping in one place (server).

2. **Volume SET still uses raw MIDI** (line 914):
   ```js
   await fetch(`${API_BASE}/api/volume`, {
       method: 'POST',
       headers: {'Content-Type': 'application/json'},
       body: JSON.stringify({value: Math.round(currentVolume)})
   });
   ```
   No change required — `{"value": N}` continues to work. But if the UI ever wants to display/send dB natively, the `{"db": N}` path is now available.

**P4 (versioning):** The web UI hardcodes API paths:
- Line 852: `` `${API_BASE}/api/state` ``
- Line 914: `` `${API_BASE}/api/volume` ``
- Line 974: `` `${API_BASE}/api/volume/adjust` ``
- Line 987: `` `${API_BASE}/api/mute` ``
- Line 1002: `` `${API_BASE}/api/dim` ``
- Line 1021: `` `${API_BASE}/api/power` ``
- Line 495: WebSocket URL `/ws/state`

Since legacy routes remain as aliases, **no immediate change needed**. When legacy routes are eventually removed, update `API_BASE` or add a version path segment. Consider defining `const API_VERSION = 'v1'` and using `` `${API_BASE}/api/${API_VERSION}/state` `` to make migration easy.

**P6 (auth):** If auth is enabled, the web UI's POST requests need the `Authorization: Bearer <key>` header. Options:
- Inject the key into the HTML template at serve time (server-side)
- Prompt user for key and store in `localStorage`
- Exempt same-origin requests from auth (safest — the web UI is served by the same process)

### MQTT Client: `go/mqtt/mqtt.go`

**P3 (dB volume):** MQTT already does the dB conversion independently (line 37: `VolumeDB: state.Volume - 127` in `statePayload`, line 162: `value = value + 127` for incoming dB values). No functional change needed, but for consistency the conversion constant should be shared. Consider extracting to `types` package:

```go
// In types/midi.go:
const VolumeDBOffset = 127 // MIDI 0 = -127dB, MIDI 127 = 0dB

// Usage:
volumeDB = midiValue - types.VolumeDBOffset
midiValue = dbValue + types.VolumeDBOffset
```

Both `api/rest.go` and `mqtt/mqtt.go` then use the same constant instead of hardcoding `127` in multiple places.

**All other phases:** No MQTT changes needed. MQTT has its own topic structure independent of REST API versioning and CORS.

### Existing Tests: `go/api/rest_test.go`

All 11 existing tests use `/api/` paths (no version prefix). They will continue to work with legacy route aliases after P4. However, new tests should target `/api/v1/` paths.

**P2 (trace_id) test impact:** `TestSetVolume` (line 58) decodes the response as a plain action check. After P2, the response body gains a `trace_id` field. The existing test doesn't decode the response body as `APIState`, so it's unaffected. But **add new tests**:

```go
func TestSetVolume_ReturnsTraceID(t *testing.T) {
    // POST /api/volume → response must contain trace_id field
}

func TestGetState_NoTraceID(t *testing.T) {
    // GET /api/state → response must NOT contain trace_id field
}
```

**P3 (dB volume) test additions:**

```go
func TestSetVolume_DB(t *testing.T) {
    // POST /api/volume {"db": -47} → action.Value should be 80
}

func TestSetVolume_DBAndValue_Error(t *testing.T) {
    // POST /api/volume {"value": 80, "db": -47} → 400
}

func TestGetState_IncludesVolumeDB(t *testing.T) {
    // GET /api/state → response contains volume_db field = volume - 127
}
```

**P1 (CORS) test additions:**

```go
func TestCORS_Preflight(t *testing.T) {
    // OPTIONS /api/state → 204 + Access-Control-Allow-Origin
}

func TestCORS_HeaderPresent(t *testing.T) {
    // GET /api/state → response includes Access-Control-Allow-Origin
}
```

### WebSocket Tests: `go/api/websocket_test.go`

**P3 (dB volume):** WebSocket tests unmarshal into the `APIState` Go struct (lines 43, 89, 143). Since `APIState` is defined in the same package, adding `VolumeDB` to the struct means the tests automatically see the new field. Existing assertions only check `state.Volume`, so **no test changes needed** — but consider adding a `VolumeDB` assertion to `TestWebSocket_ConnectReceivesState`.

### Config: `go/config/config.go`

**P1:** Add `CORSOrigin string` field + `--cors_origin` flag (default `"*"`).

**P6:** Add `APIKey string` field + `--api_key` flag (default `""`).

**P1/P6 config tests:** Add to `go/config/config_test.go`:
- Verify `CORSOrigin` defaults to `"*"`
- Verify `APIKey` defaults to `""`

### Summary: Files Changed Per Phase

| Phase | Files Modified | Files Created |
|---|---|---|
| P1 (CORS) | `api/rest.go`, `config/config.go`, `api/rest_test.go`, `config/config_test.go` | — |
| P2 (trace_id) | `api/rest.go`, `api/rest_test.go` | — |
| P3 (dB volume) | `api/rest.go`, `api/rest_test.go`, `types/midi.go`, `mqtt/mqtt.go`, `web/index.html` | — |
| P4 (versioning) | `api/rest.go`, `api/rest_test.go` | — |
| P5 (OpenAPI) | `api/rest.go` | `api/openapi.yaml` |
| P6 (auth) | `api/rest.go`, `config/config.go`, `config/config_test.go`, `web/index.html` | — |

### Consistency Checks After All Phases

After implementing all phases, verify these invariants:

1. `web/index.html` uses `state.volume_db` from API instead of computing `volume - 127` locally
2. `mqtt/mqtt.go` and `api/rest.go` both use `types.VolumeDBOffset` for the dB conversion
3. All new tests target `/api/v1/` paths; legacy tests still pass on `/api/` aliases
4. WebSocket broadcast includes `volume_db` field (same `APIState` struct)
5. `GET /api/state` does NOT include `trace_id`; only POST responses do
