# External Integrations

**Analysis Date:** 2026-03-21

## Hardware Interfaces

**USB HID Device:**
- Fosi Audio VOL20 volume knob
  - VID/PID: 0x07d7:0x0000 (configurable via `--device`)
  - Client: `hidapi` (Python binding)
  - Data: Analog rotary encoder events and button presses
  - Read method: `bridge2glm.py` HID reader thread with 1000ms timeout
  - Example code: `bridge2glm.py` lines 18-45 (import hid, open device)

**MIDI Virtual Ports:**
- **Input (Host→GLM):** "GLMMIDI 1" (configurable via `--midi_in_channel`)
  - Purpose: Send volume up/down, mute, dim, power commands to Genelec GLM
  - Message types: CC (Control Change), channel 0
  - CC mappings: See `midi_constants.py` lines 45-51
    - CC 20: Absolute volume (0-127)
    - CC 21: Volume up (momentary)
    - CC 22: Volume down (momentary)
    - CC 23: Mute (toggle)
    - CC 24: Dim (toggle)
    - CC 28: Power toggle
  - Client: `mido` + `python-rtmidi`
  - Example: `bridge2glm.py` line 21 (open_output("GLMMIDI 1"))

- **Output (GLM→Host):** "GLMOUT 1" (configurable via `--midi_out_channel`)
  - Purpose: Monitor GLM state changes, detect power toggles via pattern matching
  - Message types: CC values and patterns
  - Power detection: Pattern of 5 CC messages (MUTE, VOL, DIM, MUTE, VOL) within 150-200ms window
  - Pattern constants: `midi_constants.py` lines 53-60 (POWER_PATTERN, timing windows)
  - Reader thread: Subscribes to all incoming MIDI messages to detect power state

## External APIs & Services

**Genelec GLM (Proprietary):**
- Software: Genelec GLM v5+ (Windows application)
- Integration method: Virtual MIDI I/O (no REST API exposed)
- Default executable: `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe` (configurable via `--glm_path`)
- Control protocol: MIDI Control Change (CC) numbers (documented in `midi_constants.py`)
- State feedback: Via power pattern detection on MIDI output port
- Manager: `PowerOnOff/glm_manager.py` handles process lifecycle (start, watchdog, restart)
- UI automation fallback: `PowerOnOff/glm_power.py` for explicit power on/off (pixel-based state reading)

## Remote Control APIs

**REST HTTP API:**
- Port: 8080 (configurable via `--api_port`, set to 0 to disable)
- Framework: FastAPI with WebSocket support
- Implementation: `api/rest.py`
- Endpoints: Not fully documented in explored code, but server structure visible
- WebSocket: Real-time state push via `websockets` library
- Error handling: Custom `WebSocketErrorFilter` to suppress expected disconnect logs (lines 25-80 in `api/rest.py`)

**MQTT Home Assistant Integration:**
- Broker: Configurable via `--mqtt_broker` (disabled if not set)
- Port: 1883 (configurable via `--mqtt_port`)
- Authentication: Optional username/password via `--mqtt_user`, `--mqtt_pass`
- Topic prefix: "glm" (configurable via `--mqtt_topic`)
- Home Assistant Discovery: Enabled by default (configurable via `--mqtt_ha_discovery` / `--no_mqtt_ha_discovery`)
- Implementation: `api/mqtt.py`
- Topics published:
  - `{prefix}/state` - Current GLM state (volume, mute, dim, power)
  - `{prefix}/availability` - Online/offline status
- Commands subscribed:
  - `{prefix}/set/volume` - Set absolute volume (0-127)
  - `{prefix}/set/mute` - Mute/unmute
  - `{prefix}/set/dim` - Dim/undim
  - `{prefix}/set/power` - Power on/off
- MQTT Client: `paho.mqtt.client` (MqttClient class in `api/mqtt.py` lines 26-80+)

## Authentication & Identity

**Windows Session Security:**
- RDP credentials: Stored securely in Windows Credential Manager (not hardcoded)
- Retrieval: `cmdkey /generic:localhost` stores credentials; script reads via Windows API
- Session validation: `PowerOnOff/glm_power.py` validates console session ID before UI automation
  - Function: `is_console_session()` checks current session == console session (lines 54-74)
  - Fallback: Script detects RDP session and automatically runs RDP priming if needed

**RDP Session Priming:**
- Tool: FreeRDP (`wfreerdp.exe`) - external dependency
- Purpose: Prevent high CPU after RDP disconnect by "priming" the session at startup
- Sequence: `bridge2glm.py` calls RDP priming before GLM starts
  - Functions: `needs_rdp_priming()`, `prime_rdp_session()` (implementation in `bridge2glm.py`)
  - Priming happens once per boot (tracked via `%TEMP%\rdp_primed.flag` with boot timestamp)
  - Credentials: Read from Credential Manager (`localhost` generic entry)
  - Process: Connect via RDP, wait 3s, disconnect, run `tscon 1 /dest:console`

**MQTT Credentials:**
- Method: Command-line arguments (visible in `ps` output if not careful)
- Environment variable alternative: Recommended but not enforced
- Security: No TLS/SSL in default setup (use `mqtt_broker` with TLS termination in production)

## System Integration

**Windows API Calls:**
- Session management: `ctypes.windll.kernel32.ProcessIdToSessionId()` (check current session ID)
- Console session detection: `ctypes.windll.kernel32.WTSGetActiveConsoleSessionId()` (RDP-aware)
- Process priorities: `win32process.THREAD_PRIORITY_*` constants (thread scheduling)
- Window manipulation: `pywinauto.Desktop` for window finding, state inspection
- Display capture: `PIL.ImageGrab` for power button pixel sampling (RGB value extraction)

**Process Management:**
- CPU gating: `psutil` monitors CPU usage before GLM startup (wait for <10% idle threshold)
- Process monitoring: `psutil.Process` for watchdog (PID tracking, hang detection)
- Window stabilization: `pywinauto` for consistent window handle discovery
- Minimize command: Direct Win32 `WM_SYSCOMMAND` / `SC_MINIMIZE` messages (non-blocking)

**Logging:**
- Framework: Python `logging` module
- Format: Thread name, module, function, line number (centralized in `logging_setup.py`)
- Handlers: RotatingFileHandler (max 4MB, 5 backups), console handler, async QueueHandler
- Level: DEBUG/INFO/NONE (configurable via `--log_level`)
- Async logging: QueueListener prevents blocking during high-throughput HID reads

## Monitoring & Observability

**Error Tracking:**
- No external service detected (Sentry, DataDog, etc.)
- Local file logging only: `.log` files in application directory
- Exception handling: Try/except blocks with retry logic in `retry_logger.py`

**Logs:**
- File location: `{script_dir}/{log_file_name}.log` (default: `glm_manager.log`)
- Rotating: 4MB per file, max 5 backup files
- Format: `[timestamp] [LEVEL] [ThreadName] module:function:line - message`
- Suppression: WebSocket library errors filtered to prevent spam during normal operation

## CI/CD & Deployment

**Hosting:**
- Deployment: Windows system (local or remote via RDP)
- Installation: Direct Python with pip install -r requirements.txt
- No containerization: Not designed for Docker (requires direct USB HID and MIDI access)
- Startup: Via CLI invocation `python bridge2glm.py [args]` or Windows Task Scheduler

**Version Control:**
- No automatic update mechanism
- Manual update: Git clone/pull, pip install -r requirements.txt, restart script

**Packaging:**
- No PyPI package detected
- Installable as: `pip install -e .` (if setup.py added) or direct script execution
- Dependencies: `requirements.txt` only (no `setup.py`, `setup.cfg`, or `pyproject.toml`)

## Environment Configuration

**Required environment variables (optional, alternatives provided):**
- None strictly required (all configurable via CLI)
- Recommended for production:
  - `MQTT_USER` → `--mqtt_user` (instead of CLI where visible in `ps`)
  - `MQTT_PASS` → `--mqtt_pass`
  - `GLM_VOLUME` → `--startup_volume`

**Secrets location:**
- Windows Credential Manager: RDP credentials stored securely (accessed at runtime)
- CLI arguments: MQTT credentials (insecure; visible in process listing)
- Recommendation: Use environment variables or encrypted config file

## Webhooks & Callbacks

**Incoming Webhooks:**
- Not detected in REST API
- Potential: MQTT commands from Home Assistant (`homeassistant/+/+` pattern)

**Outgoing Webhooks:**
- Not detected
- Could be added via FastAPI endpoints for remote power control

**Notifications:**
- MQTT publish on state change (volume, mute, dim, power)
- Home Assistant MQTT Discovery (automatic entity creation in Home Assistant)

## Network Protocols

**MIDI over USB:**
- Virtual ports created by OS (Windows: "GLMMIDI 1", "GLMOUT 1")
- Protocol: MIDI 1.0 (Control Change messages)
- Real-time: Latency-sensitive (100-200ms acceptable for volume changes)

**MQTT over TCP:**
- Broker: Configurable hostname and port
- Protocol: MQTT 3.1.1 or 5.0 (paho-mqtt supports both)
- Security: Optional username/password (no TLS default)
- QoS: Not explicitly configured (defaults to 0 - fire and forget)

**WebSocket over HTTP:**
- Protocol: WebSocket (RFC 6455) via FastAPI/uvicorn
- Real-time state push from server to connected clients
- Error handling: Custom filter suppresses expected disconnect logs

---

## Dual Perspective Analysis

### Senior Developer Perspective

**Integration Complexity:**
- Well-factored: HID reader, MIDI I/O, REST API, MQTT client all isolated in separate modules (`bridge2glm.py`, `api/rest.py`, `api/mqtt.py`)
- Clear abstractions: Actions (`glm_core/actions.py`) decouple input sources from GLM control logic
- Callback patterns: MQTT/REST publish commands to shared action queue (`action_queue`)

**Reliability Concerns:**
- Power detection via pattern matching is timing-sensitive (3 MIDI messages within 150ms window) - could fail with network MIDI latency
- No retry logic for MQTT connection failures beyond reconnect attempts
- RDP priming is brittle: depends on FreeRDP executable, Credential Manager entries, Windows API calls
- No validation that Genelec GLM MIDI ports are actually connected before attempting I/O (script will silently fail if ports unavailable)

**Security Issues:**
- MQTT credentials in CLI args: `--mqtt_pass` visible in `ps aux` output
- No TLS for MQTT by default (insecure over network)
- RDP credentials in Credential Manager: Accessible to any process with user elevation
- No input validation on REST API endpoints (potential injection if user-supplied commands execute)

**Recommendations:**
- Add port availability check at startup (attempt MIDI connection, fail fast with clear error)
- Use environment variables for MQTT credentials with validation
- Document MQTT TLS setup for production (broker-side only, no client cert)
- Add integration tests for MQTT reconnection and RDP priming paths

### Senior macOS App Architect Perspective

**MIDI Dependency Problem:**
- Current: Assumes Windows virtual MIDI ports created by Genelec GLM
- macOS CoreMIDI model: Apps create virtual ports; Genelec GLM would create its own
- Issue: Port names may differ ("GLM MIDI In" vs. "GLMMIDI 1"); no macOS-specific testing visible
- Fix: Auto-discover GLM MIDI ports by filtering on source (Genelec prefix) rather than hardcoded name

**UI Automation Irrelevant on macOS:**
- Current: `PowerOnOff/glm_power.py` pixel-samples power button in GLM window (Windows-specific)
- macOS equivalent: GLM likely supports AppleScript or native macOS accessibility events
- Genelec should expose power state via MIDI feedback (check if POWER_PATTERN applies to macOS GLM)
- Alternative: Use native NSAccessibility or AppleScript to read button state

**RDP Priming Irrelevant on macOS:**
- Current: Windows-specific RDP session handling with FreeRDP priming
- macOS: Native screen sleep/wake handling via `Quartz` framework
- No session switching; direct display connection assumed
- RDP integration would be via native macOS screen sharing (AppleScript/Automation)

**Network Integration Strengths:**
- REST API (FastAPI) is platform-agnostic; works identically on macOS
- MQTT integration is platform-agnostic; works on macOS with no changes
- WebSocket state push is platform-agnostic
- **These are the portable integrations** (keep them)

**macOS-Specific Rewrite Areas:**
1. **HID input**: Replace hidapi with PyObjC IOKit bindings (native Cocoa integration, better permission handling)
2. **MIDI output**: Replace python-rtmidi with PyObjC CoreMIDI (eliminates cross-platform binding fragility)
3. **Process management**: Replace watchdog thread with launchd plist (native macOS pattern, auto-restart)
4. **Power control**: Use macOS Accessibility API or AppleScript instead of pixel sampling
5. **Auto-discovery**: Add MIDI port discovery by endpoint matching (not hardcoded names)

**Conflict: Windows Optimization vs. macOS Simplicity:**
- Windows approach: Pixel sampling + UI automation (fragile, version-dependent)
- macOS approach: Native APIs + Accessibility (robust, system-integrated)
- Solution: Platform-specific adapters with shared REST/MQTT interfaces

---

*Integration audit: 2026-03-21*
