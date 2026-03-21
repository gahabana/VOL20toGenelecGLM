# Architecture

**Analysis Date:** 2026-03-21

## Pattern Overview

**Overall:** Event-driven MIDI bridge with multi-input adapter pattern, process supervision, and system integration via UI automation.

**Key Characteristics:**
- HID input → domain action transformation (adapter pattern)
- Central action queue with stale event filtering
- Conditional platform-specific features (Windows only for power control, process management)
- Multi-threaded architecture with input isolation and graceful thread shutdown
- Dual-path control for power (MIDI toggle + UI automation state enforcement)
- Watchdog process supervision for GLM application lifecycle

## Layers

**Input Adapters Layer:**
- Purpose: Consume physical/network inputs and emit domain actions
- Location: `bridge2glm.py` (HID reader thread), `api/rest.py` (REST endpoints), `api/mqtt.py` (MQTT subscriber)
- Contains: Protocol-specific parsing, validation, rate limiting
- Depends on: `glm_core` actions, `config` for parameters
- Used by: Central action queue (coordination)

**Domain Model Layer:**
- Purpose: Define system capabilities independent of input/output
- Location: `glm_core/actions.py`
- Contains: `SetVolume`, `AdjustVolume`, `SetMute`, `SetDim`, `SetPower`, `QueuedAction`
- Depends on: Nothing (pure dataclasses)
- Used by: All input adapters and action consumer

**MIDI Control Layer:**
- Purpose: Translate domain actions to MIDI messages for GLM
- Location: `bridge2glm.py` (MIDI output, `_send_to_glm()` and related functions)
- Contains: CC mapping, MIDI message construction, channel management
- Depends on: `mido` library, `midi_constants.py` for CC numbers
- Used by: Action consumer thread

**UI Automation / Power Layer (Windows only):**
- Purpose: Verify and enforce power state via pixel sampling and mouse automation
- Location: `PowerOnOff/glm_power.py`
- Contains: Power button detection, pixel color sampling, mouse click synthesis, display diagnostics
- Depends on: `pywinauto`, `PIL`, Windows API (`win32api`, `ctypes`)
- Used by: Power command handler in `bridge2glm.py` (conditional, fallback to MIDI)

**Process Supervision Layer (Windows only):**
- Purpose: Lifecycle management and watchdog for GLM application
- Location: `PowerOnOff/glm_manager.py`
- Contains: CPU gating at startup, process monitoring, hang detection, auto-restart logic
- Depends on: `psutil`, `pywinauto`, Windows API
- Used by: Main script initialization (via `if GLM_MANAGER_AVAILABLE`)

**Configuration & Constants Layer:**
- Purpose: Centralize parameters, MIDI mappings, and CLI argument parsing
- Location: `config.py`, `midi_constants.py`, `acceleration.py`
- Contains: Argument validation, MIDI CC numbers, action enums, acceleration curves
- Depends on: `argparse`
- Used by: All layers (global configuration)

**Logging & Diagnostics Layer:**
- Purpose: Structured logging with queue-based async handler, telemetry
- Location: `logging_setup.py`, `retry_logger.py`
- Contains: Log format standardization, rotating file handlers, retry tracking
- Depends on: Python `logging` module
- Used by: All layers

## Data Flow

**Volume Control Flow (User Rotation):**

```
1. HID Reader Thread (bridge2glm.py):
   - Read VOL20 device via python-hid
   - Decode button press (KEY_VOL_UP, KEY_VOL_DOWN, KEY_CLICK, etc.)
   - Detect click patterns to identify intent (single click, double, long press)

2. Acceleration Calculation:
   - AccelerationHandler.calculate_speed() applies velocity-based acceleration
   - Faster rotation = larger delta (1→3 steps based on volume_increases_list)

3. Action Creation:
   - Create AdjustVolume(delta=N) or SetVolume(target=N)
   - Wrap in QueuedAction(action, timestamp=now)

4. Queue Submission:
   - Submit to thread-safe queue.Queue (max 100 items)
   - If queue full, apply backpressure (skip/drop events)

5. Consumer Thread (bridge2glm.py main loop):
   - Pop QueuedAction from queue
   - Filter stale events (>2 seconds old)
   - Send MIDI: CC 21 (vol up) or CC 22 (vol down)
   - Log via retry_logger

6. GLM MIDI Handler:
   - GLM receives MIDI CC and updates volume
   - GLM outputs CC 20 (absolute volume) on every change

7. MIDI Reader Thread (bridge2glm.py):
   - Capture GLM's CC 20 feedback
   - Update local state (for REST API and MQTT)
```

**Power Control Flow (Power Toggle):**

```
1. HID Input:
   - Detect KEY_TRIPLE_CLICK on VOL20 power button
   - Create SetPower(state=None) for toggle

2. Power Pattern Detection:
   - MIDI reader monitors for MUTE→VOL→DIM→MUTE→VOL pattern
   - Pattern indicates GLM power state change (fires 150ms window)
   - Timestamp checked to distinguish power event from normal volume changes

3. Action Consumer:
   - Receives SetPower action
   - MIDI attempt: Send CC 28 (power toggle) if available
   - UI Automation fallback (if GlmPowerController available):
     * If state=None: Already sent MIDI, done
     * If state=True/False: Call controller.set_state() to enforce

4. UI Automation Verification:
   - Sample power button color at fixed window coordinates
   - Median of 5 samples to reduce noise
   - "on"=green, "off"=red, "unknown"=neither
   - If mismatch with desired state: Click power button at computed coords
   - Retry up to 3 times with 500ms wait

5. Power Settling:
   - Block ALL incoming commands for 2 seconds (POWER_SETTLING_TIME)
   - Then block power-specific commands for 3 more seconds
   - Total 5-second lockout to prevent race conditions

6. State Feedback:
   - REST API and MQTT publish power state
   - Web UI displays button state (green/red/unknown)
```

**REST API & WebSocket Flow:**

```
1. FastAPI Server (api/rest.py):
   - Listens on configurable port (default 8080)
   - POST /api/actions/<action_name> → create GlmAction → queue
   - GET /api/state → return current state (volume, mute, dim, power)

2. WebSocket Handler:
   - Connected clients subscribe to state changes
   - On any state update (HID/MQTT/REST), broadcast to all clients
   - Graceful disconnect handling with error suppression

3. Web Frontend (web/index.html):
   - React-like component state (volume slider, power/mute/dim toggles)
   - WebSocket listener updates UI in real-time
   - User clicks → POST /api/actions/... → queued → executed
```

**MQTT Flow (Home Assistant Integration):**

```
1. MQTT Client (api/mqtt.py):
   - Connect to broker with optional TLS and auth
   - Publish to glm/state/{entity} on any state change

2. Home Assistant Discovery:
   - Publish MQTT Discovery payloads to homeassistant/number/glm/volume/config
   - HA automatically creates entities (slider, buttons, binary sensors)

3. HA → GLM Commands:
   - HA publishes to glm/command/{entity}
   - MQTT client creates GlmAction and queues
   - Follows same execution path as HID/REST

4. State Sync:
   - On startup, query GLM volume (vol+1, vol-1, read response)
   - Publish initial state to HA
   - From then on, event-driven updates
```

**GLM Process Lifecycle (if GLM_MANAGER_AVAILABLE):**

```
1. Startup:
   - Check if GLM already running (psutil)
   - If not running:
     * CPU gating: Wait for system CPU <10% (max 5 min)
     * Start GLMv5.exe with AboveNormal priority
     * Stabilize window handle (pywinauto Desktop lookup)
     * Minimize window non-blocking (WM_SYSCOMMAND)

2. Watchdog Thread:
   - Monitors GLM process handle every 1 second
   - Detects hangs (process still exists but unresponsive)
   - On hang: Log, trigger reinit callback (if set), restart GLM

3. Shutdown:
   - Stop watchdog thread
   - Don't kill GLM (user can close manually or GLM auto-stops)
```

**State Management:**

- **HID State:** `last_button`, `last_time`, `first_time` (in AccelerationHandler)
- **MIDI State:** Current volume, mute, dim, power (updated from GLM CC feedback)
- **Power State:** Cached in GlmPowerController (last known on/off/unknown)
- **Queue State:** Python `queue.Queue` with timestamp filtering
- **Thread State:** Event flags for graceful shutdown (`shutdown_event`)

## Key Abstractions

**Action Dataclass Hierarchy:**
- Purpose: Domain-level command representation independent of input method
- Examples: `SetVolume(target=79)`, `AdjustVolume(delta=2)`, `SetPower(state=True)`
- Pattern: Frozen dataclasses (immutable, hashable) for thread safety

**Adapter Pattern (Input → Action):**
- Purpose: Isolate protocol-specific logic from domain logic
- Examples:
  - HID adapter (bridge2glm.py lines 200-300+): Button codes → Actions
  - REST adapter (api/rest.py): JSON payloads → Actions
  - MQTT adapter (api/mqtt.py): Topic messages → Actions
- Pattern: Each adapter creates `QueuedAction(action, timestamp)` and submits

**Stale Event Filtering:**
- Purpose: Prevent delayed inputs from affecting state (graceful backpressure)
- Location: Consumer loop in bridge2glm.py (check `MAX_EVENT_AGE = 2.0s`)
- Pattern: Timestamp comparison at consumption time, not submission

**Power State Pattern (Dual-Path):**
- Purpose: Achieve reliable power control despite MIDI-only protocol limitation
- MIDI Path: CC 28 (toggle only, no feedback)
- UI Path: Pixel sampling + mouse clicks (explicit on/off with verification)
- Pattern: Try MIDI first, fall back to UI automation if power state mismatch detected

**Process Watchdog Pattern:**
- Purpose: Auto-recovery from GLM hangs without user intervention
- Location: `PowerOnOff/glm_manager.py`, watchdog thread
- Pattern: Spawn daemon thread, poll process handle, trigger reinit callback on hang

## Entry Points

**Main Entry Point:**
- Location: `bridge2glm.py` (executed as `python bridge2glm.py`)
- Triggers: Manual invocation or scheduled startup (via Task Scheduler on Windows)
- Responsibilities:
  * Parse CLI arguments
  * Setup logging
  * Start GLM manager (if enabled)
  * Initialize GlmPowerController (if available)
  * Start HID reader thread
  * Start MIDI reader thread
  * Start API server (FastAPI in background thread)
  * Start MQTT client (if configured)
  * Main consumer loop: Dequeue actions, execute, handle errors

**HID Reader Thread:**
- Location: `bridge2glm.py`, spawned in `__main__`
- Triggers: Application startup
- Responsibilities: Poll VOL20 device, pattern recognition, queue actions

**MIDI Reader Thread:**
- Location: `bridge2glm.py`, spawned in `__main__`
- Triggers: Application startup
- Responsibilities: Listen for GLM state feedback, detect power patterns, update state

**API Server Thread:**
- Location: `api/rest.py`, spawned via `start_api_server()`
- Triggers: If `--api_port > 0`
- Responsibilities: Serve HTTP endpoints, handle WebSocket connections

**MQTT Client Thread:**
- Location: `api/mqtt.py`, spawned via `MqttClient(...).connect_and_loop()`
- Triggers: If `--mqtt_broker` specified
- Responsibilities: Publish state, subscribe to commands, handle HA discovery

**GLM Manager Watchdog Thread:**
- Location: `PowerOnOff/glm_manager.py`, spawned in `GlmManager.start()`
- Triggers: If `--glm_manager` and `GLM_MANAGER_AVAILABLE`
- Responsibilities: Monitor process, detect hangs, auto-restart

## Error Handling

**Strategy:** Graceful degradation with per-layer isolation and retry logic.

**Patterns:**

- **HID Device Errors:**
  - Retry with `retry_logger` (exponential backoff 2s)
  - If persistent: Log warning, continue (won't crash main loop)

- **MIDI Channel Errors:**
  - Exception caught, logged, consumer loop continues
  - Queue remains intact for next command

- **Power Control Errors:**
  - UI automation failure: Log error, fall back to MIDI toggle
  - Window not found: Log as "unknown" state, don't crash
  - Custom exceptions: `GlmWindowNotFoundError`, `GlmStateUnknownError`, `GlmStateChangeFailedError`

- **API/WebSocket Errors:**
  - WebSocket disconnect: Caught, client removed from set
  - HTTP 400: Bad payload, return 400 with error message
  - Suppressed warnings: `WebSocketErrorFilter` (lines in api/rest.py)

- **Process Management Errors:**
  - GLM not found: Log, don't crash watchdog
  - Process start failure: Retry (with CPU gating), log after max retries
  - Session issues (RDP): Handled by `ensure_session_connected()` in glm_power.py

- **Queue Overflow:**
  - If queue full (max 100): Skip incoming events (backpressure)
  - Consumer processes queue as fast as MIDI allows

## Cross-Cutting Concerns

**Logging:**
- Centralized format (thread name, module, function, line number, timestamp)
- Setup in `logging_setup.py` with RotatingFileHandler
- Async queue-based handler (`QueueHandler`, `QueueListener`) to avoid blocking
- Level configurable via `--log_level` (DEBUG/INFO/NONE)

**Validation:**
- CLI arguments: Type checking and range validation in `config.py`
- MIDI values: Clamped to 0-127 before sending
- Volume deltas: Limited by acceleration curve (max 3-5 per click)
- Timestamps: Filtered for stale events (>2 seconds)

**Authentication:**
- MQTT: Optional username/password from CLI args
- REST API: No auth (assumes local network or VPN)
- Windows credentials: Stored in Windows Credential Manager for RDP priming

**Platform Detection:**
- `IS_WINDOWS = sys.platform == 'win32'`
- Conditional imports for Windows-only deps (psutil, pywinauto, win32process)
- Graceful fallback on non-Windows (no power control, no process management)

**Thread Safety:**
- Action queue: `queue.Queue` (thread-safe by design)
- State dict: No locks needed (each value set once, never modified)
- MIDI channels: Single writer per direction (one thread sends, one reads)
- Power controller: Lock not needed (reads only, no concurrent state changes)

---

## Dual Perspective Analysis

### Senior Developer Perspective vs. macOS App Architect Perspective

**AGREEMENT:**
- Both perspectives recognize the **adapter pattern** as well-implemented (HID, REST, MQTT all feed the same action queue)
- Both appreciate the **separation of concerns**: actions, MIDI protocol, UI automation, and process management are cleanly layered
- Both see the **stale event filtering** (2-second timeout) as a pragmatic anti-pattern mitigation
- Both view the **conditional imports** as correct for cross-platform compatibility
- Both recognize the **thread-per-input-source** model as appropriate for this workload (HID polling, MQTT blocking receive, WebSocket long-polling)

**DIVERGENCE:**

| Senior Developer | macOS App Architect |
|------------------|---------------------|
| **Modularity:** Code is well-modularized into `glm_core/`, `PowerOnOff/`, `api/`. Good separation. | **Runtime Model:** Application is architected as a background daemon/agent loop, not a macOS-idiomatic lifecycle model. No AppKit integration, no Cocoa event loop, no application delegate pattern. |
| **Testability:** Frozen dataclasses (actions) enable unit testing without mocking. Queue-based design allows test injection. | **System Integration:** Python MIDI library (`mido`) requires low-level port discovery—works on Windows/Linux but fragile on macOS where Core MIDI is the native API. HID via `python-hid` also sidestepped macOS native frameworks. |
| **Scalability:** Central queue bottleneck if HID/MQTT/REST rates spike, but acceptable for a volume controller. | **Resource Management:** Multi-threaded design (HID thread, MIDI thread, API thread, MQTT thread, Watchdog thread = 5+ threads) is higher overhead than event-driven single-threaded or async/await model. No runloop integration means threads can't coordinate on macOS' unified event system. |
| **Error Handling:** Per-layer error boundaries with logging is standard; good resilience. | **Process Supervision:** Replicating GLM watchdog in Python (psutil polling every 1s) is crude compared to macOS' `launchd` mechanism for process supervision and auto-restart with exponential backoff. On macOS, would use plist + `defaults write` to set up system-level daemon supervision. |
| **Coupling:** MIDI constants tightly coupled in `midi_constants.py` but reasonable given MIDI spec immutability. | **UI Automation:** Dependency on `pywinauto` + pixel color sampling is Windows-only and fragile. macOS equivalent would be using Accessibility API (`AXUIElement`) or controlling Genelec GLM via AppleScript if available. Pixel sampling is not reliable across display scaling/themes. |
| | **Threading Model:** Python GIL means threads can't truly parallelize CPU work, only I/O. On macOS, would prefer `OperationQueue` or `DispatchQueue` for better CPU utilization and priority management. Current approach works but is not idiomatic. |
| | **CLI/Config:** Argument parsing is fine for CLI, but macOS app would use Info.plist, NSUserDefaults, or a preferences window instead of CLI args. No user-facing config UI here. |

**Translation to macOS:**
If this were ported to macOS:
1. Replace `python-hid` with IOKit USB HID framework or Core HID
2. Replace `mido` with Core MIDI API (direct Objective-C or Swift)
3. Replace `pywinauto` pixel sampling with Accessibility API (NSAccessibility) to read GLM UI state
4. Replace watchdog polling with `launchd` plist for daemon auto-restart
5. Replace multi-threaded design with `DispatchQueue` (GCD) for async, non-blocking I/O
6. Use `CFRunLoop` or Cocoa event loop to integrate all I/O sources
7. Package as agent (LaunchAgent if user-level) with System Preferences for configuration

**Senior Developer Assessment:** Architecture is sound, maintainable, and well-separated. MIDI/HID bridges benefit from being platform-independent. The queue-based design is robust.

**macOS Architect Assessment:** Application is fully functional on Windows but not idiomatic for macOS. Would require significant rearchitecture to integrate with system frameworks and follow macOS app lifecycle patterns. Currently works but doesn't leverage macOS' native system integration (Core MIDI, Accessibility API, launchd).

---

*Architecture analysis: 2026-03-21*
