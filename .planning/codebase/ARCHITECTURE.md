# Architecture

**Analysis Date:** 2026-03-21

## Pattern Overview

**Overall:** Multi-threaded Producer/Consumer bridge with UI Automation sidecar

**Key Characteristics:**
- A single bounded `queue.Queue` decouples all input sources (HID hardware, REST API, MQTT) from the single consumer that executes GLM commands
- Domain actions (`glm_core/actions.py`) are the shared lingua franca — every input adapter creates them, the consumer dispatches them
- Two parallel control paths exist for power: MIDI CC 28 (toggle only, legacy) and pywinauto pixel-sampling UI automation (explicit on/off with verification); the UI automation path is authoritative
- The Windows session/display layer is treated as an explicit concern: WTS session state, tscon reconnection, RDP priming, and console-session detection are first-class operations, not afterthoughts

---

## Layers

**Entry Point / Startup (`bridge2glm.py:__main__`):**
- Purpose: Parse CLI args, run one-time boot-time operations (RDP priming, MIDI service restart), construct and wire all components, install SIGINT handler, block main thread
- Location: `bridge2glm.py` lines 1410–1492
- Depends on: `config.py`, `logging_setup.py`, all subsystems
- Used by: Nothing (top of call graph)

**Configuration Layer (`config.py`):**
- Purpose: Validates and parses all CLI arguments; defines all tuneable parameters including HID VID/PID, MIDI channel names, click timing, volume acceleration curve, API port, MQTT settings, GLM path
- Location: `config.py`
- Contains: `parse_arguments()`, `validate_*` functions
- Depends on: `argparse`, stdlib only
- Used by: `bridge2glm.py:__main__`

**Domain Actions (`glm_core/actions.py`):**
- Purpose: Frozen dataclasses representing what the system should do, independent of input source. `QueuedAction` wraps any action with a timestamp for stale-event filtering.
- Location: `glm_core/actions.py`
- Contains: `SetVolume`, `AdjustVolume`, `SetMute`, `SetDim`, `SetPower`, `QueuedAction`, `GlmAction` union type
- Depends on: stdlib only
- Used by: all input adapters (HID, REST, MQTT) and the consumer

**State Tracker (`bridge2glm.py:GlmController`):**
- Purpose: Single source of truth for GLM state (volume, mute, dim, power). Tracks pending volume to allow command accumulation before GLM confirms. Manages power transition / settling / cooldown state machine. Fires registered callbacks on state change (used by REST WebSocket and MQTT).
- Location: `bridge2glm.py` lines 110–403
- Key state: `volume` (0–127), `mute`, `dim`, `power`, `_pending_volume`, `_power_settling`, `_power_transition_start`
- Thread safety: All mutable state protected by `threading.Lock()`
- Used by: Consumer thread (writes), MIDI reader thread (writes via `update_from_midi`), REST API (reads), MQTT (reads)

**HID Input Thread (`bridge2glm.py:HIDToMIDIDaemon.hid_reader`):**
- Purpose: Reads raw USB HID reports from the Fosi Audio VOL20 knob. Maps physical keycodes to `Action` enum via configurable `DEFAULT_BINDINGS`. Applies `AccelerationHandler` for volume. Enqueues `QueuedAction` objects.
- Thread name: `HIDReaderThread`
- Win32 priority: `THREAD_PRIORITY_HIGHEST`
- Device: opened by VID/PID (`0x07d7, 0x0000` default). Blocking read with 1000 ms timeout for clean shutdown.
- Reconnect: auto-retry with `SmartRetryLogger` throttling

**MIDI Reader Thread (`bridge2glm.py:HIDToMIDIDaemon.midi_reader`):**
- Purpose: Reads MIDI CC messages from GLM's MIDI output port. Updates `GlmController` state from CC 20 (volume), 23 (mute), 24 (dim). Also performs MIDI pattern detection for power state: detects the 5-message burst `[MUTE, VOL, DIM, MUTE, VOL]` that GLM emits on power toggle from RF remote.
- Thread name: `MIDIReaderThread`
- Win32 priority: `THREAD_PRIORITY_ABOVE_NORMAL`
- Blocking iteration over `mido` input port

**Consumer Thread (`bridge2glm.py:HIDToMIDIDaemon.consumer`):**
- Purpose: Dequeues `QueuedAction` objects and executes them. Discards events older than `MAX_EVENT_AGE` (2.0 s). Enforces power settling and cooldown lockouts. Dispatches to volume handlers (absolute CC 20, or fallback CC 21/22), MIDI action sender, or UI automation power handler.
- Thread name: `ConsumerThread`
- Win32 priority: `THREAD_PRIORITY_ABOVE_NORMAL`
- Power execution: calls `GlmPowerController.set_state()` in-line (blocking; by design, power takes time)

**UI Automation Layer (`PowerOnOff/glm_power.py:GlmPowerController`):**
- Purpose: Deterministic power state control via pixel sampling + synthesized mouse click. Finds GLM's JUCE window (`JUCE_.*` class, "GLM" in title), restores it to foreground, reads median RGB of a fixed screen patch at the power button, classifies as "on"/"off"/"unknown", clicks if needed, verifies with polling. Restores window state (minimize, focus) after operation.
- Key Win32 APIs: `ImageGrab` (GDI screen capture), `win32api.SetCursorPos`, `win32api.mouse_event`, `ctypes.windll.user32.SetForegroundWindow`, `IsIconic`, `IsHungAppWindow`
- Thread safety: protected by `threading.Lock()`
- Session guards: calls `ensure_session_connected()` before UI automation to detect and recover from disconnected RDP sessions (using `WTSEnumerateSessionsW` and `tscon`)

**GLM Process Manager (`PowerOnOff/glm_manager.py:GlmManager`):**
- Purpose: Full lifecycle management of the `GLMv5.exe` process. CPU gating at initial start (waits for `psutil.cpu_percent` < 10%). Starts GLM via `subprocess.Popen`, sets `ABOVE_NORMAL_PRIORITY_CLASS`. Waits for JUCE window handle to stabilize (polls until same HWND seen `stable_handle_count` times). Runs background watchdog thread checking liveness (`psutil`) and responsiveness (`IsHungAppWindow`). Restarts GLM after exit or hang (>30 s non-responsive). Calls `reinit_callback` after restart so power controller can re-bind to new window.
- Thread name: `GLMWatchdog` (daemon thread)
- Location: `PowerOnOff/glm_manager.py`

**REST API (`api/rest.py`):**
- Purpose: FastAPI + uvicorn server in a dedicated thread with its own asyncio event loop. Exposes `/api/state`, `/api/volume`, `/api/volume/adjust`, `/api/mute`, `/api/dim`, `/api/power`, `/api/health`, `/ws/state`. Reads state from `GlmController`, submits `QueuedAction` objects to shared queue. WebSocket endpoint broadcasts state changes pushed via `asyncio.run_coroutine_threadsafe` from `GlmController` callbacks.
- Thread name: `APIServerThread` (daemon thread)
- Cross-thread broadcast: `_broadcast_state_sync` → `asyncio.run_coroutine_threadsafe` → `_api_event_loop`
- Serves static web UI from `web/`

**MQTT Client (`api/mqtt.py:MqttClient`):**
- Purpose: paho-mqtt client; subscribes to `glm/set/{volume,mute,dim,power}`, enqueues `QueuedAction` on message. Publishes state on `glm/state` (retained). Publishes HA MQTT Discovery configs for Home Assistant entity auto-creation. Registers as `GlmController` state callback to publish on every state change.
- Thread model: `loop_start()` runs paho's background thread

**Logging (`logging_setup.py`, `retry_logger.py`):**
- Purpose: Async queue-based logging via `QueueListener`; prevents lock contention in hot paths. Dedicated `LoggingThread` (non-daemon; ensures final messages flush on exit) set to `THREAD_PRIORITY_IDLE`. `SmartRetryLogger` throttles retry-loop messages using absolute-time milestones to avoid log spam without hiding persistent failures.

---

## Data Flow

**HID Knob → GLM Volume:**
1. VOL20 USB HID report arrives at `HIDReaderThread`
2. Physical keycode mapped via `DEFAULT_BINDINGS` → `Action.VOL_UP/DOWN`
3. `AccelerationHandler.calculate_speed()` returns delta
4. `AdjustVolume(delta=N)` wrapped in `QueuedAction` → `queue.put()`
5. `ConsumerThread` dequeues; checks stale-event age and power-settling lockout
6. `_handle_adjust_volume`: reads `GlmController.get_volume_if_valid()` for pending/confirmed volume; calculates absolute target; calls `GlmController.send_volume_absolute()` → `mido` CC 20 → LoopMIDI virtual port → GLM
7. GLM confirms by sending CC 20 back on its output port
8. `MIDIReaderThread` receives CC 20; calls `GlmController.update_from_midi()` which clears `_pending_volume` and fires state callbacks
9. State callbacks notify REST WebSocket clients and MQTT broker

**Power Toggle (UI Automation Path):**
1. Click on VOL20 → `SetPower(state=None)` enqueued
2. Consumer dispatches to `_handle_power_action()`
3. `GlmController.start_power_transition()` — begins 2 s settling lockout; notifies all callbacks with `power_transitioning: true`
4. `ensure_session_connected()` checks WTS session state; runs `tscon` if disconnected
5. `GlmPowerController.set_state(desired, verify=True)` — finds JUCE window, restores to foreground, reads pixel, clicks if needed, polls until verified
6. Consumer waits for remainder of `POWER_SETTLING_TIME` (2 s)
7. `GlmController.end_power_transition(success, actual_state)` — clears settling flag; fires callbacks
8. 3 s additional cooldown blocks further power commands (`POWER_TOTAL_LOCKOUT = 5 s`)

**Remote RF Power Toggle (MIDI Pattern Detection):**
1. GLM emits 5-message burst: `[MUTE, VOL, DIM, MUTE, VOL]` within ~150 ms when power is toggled via RF remote
2. `MIDIReaderThread` maintains a sliding window of recent `(timestamp, cc)` tuples
3. Triple-condition filter: max single gap < 170 ms, total gap < 200 ms, pre-gap > 120 ms
4. On match: `glm_controller.power = not old_power` (in-place toggle, bypasses UI automation path since RF remote clicked the physical button directly)

**State → External Systems:**
1. Any state change fires `GlmController._notify_state_change()`
2. REST: `_broadcast_state_sync` schedules `_broadcast_to_all` on uvicorn's asyncio event loop
3. MQTT: `MqttClient.on_state_change` calls `_publish_state` which publishes JSON to `glm/state` (retained)

---

## Key Abstractions

**`GlmAction` union (`glm_core/actions.py`):**
- Purpose: Type-safe representation of every command the system can execute. Frozen dataclasses — immutable once created.
- Examples: `SetVolume(target=79)`, `AdjustVolume(delta=2)`, `SetPower(state=None)` (toggle), `SetPower(state=True)` (explicit on)
- Pattern: Command pattern. All input sources produce these; consumer is the single handler.

**`GlmController` (`bridge2glm.py`):**
- Purpose: Stateful model of GLM's observable state. Dual update paths: MIDI feedback (ground truth) and optimistic pending-volume tracking. Observer pattern via `_state_callbacks` list.
- Pattern: Observable state model with optimistic concurrency for volume.

**`GlmPowerConfig` / `GlmManagerConfig` dataclasses:**
- Purpose: All magic numbers and timeouts are configurable via dataclass fields with documented defaults. No scattered constants inside methods.
- Examples: `GlmPowerConfig.dx_from_right`, `GlmManagerConfig.watchdog_interval`

**`PowerOnOff/exceptions.py`:**
- Hierarchy: `GlmPowerError` → `GlmWindowNotFoundError`, `GlmStateUnknownError` (carries `rgb` + `point`), `GlmStateChangeFailedError` (carries `desired` + `actual`)
- Pattern: Rich exceptions with diagnostic payload; callers can introspect failure reason without string parsing.

---

## Entry Points

**`bridge2glm.py` (`__main__` block):**
- Location: `bridge2glm.py` line 1410
- Triggers: `python bridge2glm.py [args]` or Windows Task Scheduler at boot
- Responsibilities: Parse args → setup logging → RDP priming → restart MIDI service → construct `HIDToMIDIDaemon` → install signal handler → `daemon.start()` → block main thread

**REST API (`/api/` endpoints):**
- Location: `api/rest.py`
- Triggers: HTTP requests on port 8080 (default)
- Responsibilities: Validate input → enqueue `QueuedAction` → return immediately (fire-and-forget)

**MQTT (`glm/set/+` topics):**
- Location: `api/mqtt.py`
- Triggers: MQTT messages from Home Assistant or other brokers
- Responsibilities: Parse payload → enqueue `QueuedAction`

---

## Error Handling

**Strategy:** Fail-soft with automatic reconnection. No subsystem crash propagates to others.

**Patterns:**
- HID/MIDI connection failures: `SmartRetryLogger`-throttled warnings; thread loops with `RETRY_DELAY` sleep. Never crashes.
- Power control failures: `GlmPowerError` subclasses caught in `_handle_power_action`; `end_power_transition(success=False)` always called (in `try/finally`-equivalent logic) to unblock the settling lockout.
- GLM process crash/hang: `GlmManager` watchdog detects via `psutil.is_running()` and `IsHungAppWindow`; kills and restarts. `reinit_callback` re-binds power controller to new HWND.
- UI automation state unknown: retries with `retries=2` default; raises `GlmStateChangeFailedError` if all retries fail.
- Stale events: `ConsumerThread` discards `QueuedAction` objects older than `MAX_EVENT_AGE = 2.0 s`. Prevents backlog replay after transient outages.
- Queue backpressure: `queue.Queue(maxsize=QUEUE_MAX_SIZE)` — HID reader will block if consumer is stalled; bounded to prevent unbounded memory growth.

---

## Cross-Cutting Concerns

**Logging:**
- Format: `%(asctime)s [%(levelname)s] %(threadName)s %(module)s:%(funcName)s:%(lineno)d - %(message)s`
- All threads log thread name; enables log-based thread activity tracing
- Async queue (`QueueHandler` + `QueueListener`) offloads I/O to `LoggingThread`
- WebSocket disconnect noise suppressed via `WebSocketErrorFilter` applied at root logger and all handlers

**Thread Priority (Win32):**
- Main process: `ABOVE_NORMAL_PRIORITY_CLASS` (via `psutil.nice`)
- `HIDReaderThread`: `THREAD_PRIORITY_HIGHEST` — ensures hardware input is never missed
- `ConsumerThread` + `MIDIReaderThread`: `THREAD_PRIORITY_ABOVE_NORMAL` — balanced send/receive
- `LoggingThread`: `THREAD_PRIORITY_IDLE` — never steals cycles from control path
- GLM process: `ABOVE_NORMAL_PRIORITY_CLASS` — set by `GlmManager` after startup

**Session / Display Awareness:**
- `is_console_session()`: uses `ProcessIdToSessionId` + `WTSGetActiveConsoleSessionId`
- `is_session_disconnected()`: uses `WTSEnumerateSessionsW` to check session state
- `ensure_session_connected()`: called before every UI automation operation; runs `tscon` (direct or via psexec for elevation) to reconnect disconnected RDP session to console display

**Shutdown:**
- `daemon.stop()` sets `_stop_event`, puts sentinel `None` on queue, stops MQTT, closes MIDI/HID
- `LoggingThread` is non-daemon (ensures final log messages flush before process exits)
- `GlmManager` watchdog stops without killing GLM (intentional — GLM continues running)

---

## Dual Perspective Analysis

### Senior Developer Perspective

**Strengths:**
- The `GlmAction` command pattern is a clean seam. Adding a new control (e.g., Bass EQ) requires: add a dataclass to `glm_core/actions.py`, add a `elif isinstance(action, NewAction)` arm to the consumer, and add a queue.put call to whichever adapter needs it. Nothing else changes.
- `GlmController.get_volume_if_valid()` is a well-designed atomic read combining `_volume_initialized` check and effective-volume selection in one lock acquisition — avoids TOCTOU race.
- `GlmStateChangeFailedError` carries structured diagnostic data (`rgb`, `point`, `desired`, `actual`) rather than embedding it in the message string — testable and introspectable.
- `SmartRetryLogger` absolute-milestone throttling is elegant: first failure always logged, then 2s, 10s, 1min, 10min, 1hr, 1day. Never silent, never spammy.

**Weaknesses / Coupling:**
- `GlmController` is a module-level singleton (`glm_controller = GlmController()` at line 407 of `bridge2glm.py`). The MQTT client, REST API, and consumer all access it as a global. This makes testing and future multi-GLM scenarios harder.
- `bridge2glm.py` is still a 1500-line file. `GlmController`, `HIDToMIDIDaemon`, RDP priming, MIDI service restart, console minimization, and logging setup all coexist. The classes are well-structured, but the module boundary is blurred.
- Power settling state (`_power_settling`, `_power_transition_start`) lives inside `GlmController` but is checked by the consumer, REST, and MQTT — this is reasonable but means `GlmController` conflates "GLM state" with "our command throttling state."
- No unit tests currently exist. The pixel-sampling classification in `_classify_state` (hardcoded RGB thresholds) is especially fragile without tests.

### Senior Windows Desktop App Architect Perspective

**Thread Model:**
- Python `threading.Thread` objects are standard OS threads (not lightweight fibers). All Win32 thread priority APIs (`SetThreadPriority` via `win32process`) work correctly. There are no COM apartment concerns because no COM objects are used — pywinauto uses Win32 APIs directly.
- The asyncio event loop for the REST/WebSocket server runs in its own thread (`APIServerThread`). Cross-thread coroutine scheduling via `asyncio.run_coroutine_threadsafe` is the correct pattern for bridging synchronous callbacks (from `GlmController`) into an async event loop.

**UI Automation:**
- Using pywinauto with `backend="win32"` and window class regex `JUCE_.*` is correct and robust for JUCE-based apps. The Alt-key trick before `SetForegroundWindow` (lines 527–536 of `glm_power.py`) correctly works around the Windows foreground lock restriction that prevents non-foreground processes from stealing focus.
- Pixel sampling via `ImageGrab.grab` (GDI-based `BitBlt`) is a valid approach when the target app has no accessible COM/UIA elements. The `all_screens=True` parameter handles multi-monitor setups. The median-of-patch approach is resilient to single noisy pixels.
- `IsHungAppWindow` (used in `GlmManager.is_responding`) is the correct Win32 API for detecting hung GUI applications — it uses the same 5-second threshold as Task Manager.

**WTS Session Management:**
- `WTSEnumerateSessionsW` → session state check → `tscon session_id /dest:console` is the correct sequence for reconnecting a disconnected RDP session to the console display. The psexec fallback for SYSTEM-level elevation is appropriate when the script doesn't run as Administrator.
- The RDP priming mechanism (FreeRDP connect/disconnect before GLM starts, tracked by `%TEMP%\rdp_primed.flag` with boot timestamp) addresses a real Windows display driver initialization order problem with OpenGL apps on headless VMs.

**Process Management:**
- `subprocess.Popen` + `psutil.Process(popen.pid)` for GLM lifecycle is correct. Using `psutil.ABOVE_NORMAL_PRIORITY_CLASS` with `proc.nice()` is the proper cross-version way to set process priority class.
- `IsWindow(hwnd)` before `IsHungAppWindow(hwnd)` (line 239 of `glm_manager.py`) correctly handles the case where the HWND has been destroyed between cache and use.

**Gaps from Windows Architect Perspective:**
- No Windows Event (kernel object) is used for inter-thread synchronization — Python `threading.Event` is used instead. This is fine functionally, but means thread state cannot be observed from external tools (Process Monitor, etc.).
- No Job Object wraps the GLM process. If bridge2glm.py crashes, GLM keeps running as an orphan — the next invocation reuses it (by design), but unintended orphan accumulation is possible.
- The MIDI service restart (`net stop/start midisrv`) requires the process to run elevated or as a service account with service control permissions. This is undocumented in CLI help.
- `GetConsoleWindow()` + `ShowWindow(hwnd, SW_MINIMIZE)` for console minimization (lines 425–432) can fail silently if the process has no console window (e.g., started with `pythonw.exe`).

---

*Architecture analysis: 2026-03-21*
