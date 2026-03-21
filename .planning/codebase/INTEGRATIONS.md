# External Integrations

**Analysis Date:** 2026-03-21

## Hardware Interfaces

**Fosi Audio VOL20 USB HID Knob:**
- Protocol: USB HID (raw)
- SDK/Client: `hidapi` Python library (`import hid`)
- Identification: VID `0x07d7`, PID `0x0000` (configurable via `--device`)
- Access pattern: `hid.device()` opened in `HIDThread`, polled with `read(64, HID_READ_TIMEOUT_MS=1000)`
- Key codes decoded in `midi_constants.py`: `KEY_VOL_UP=2`, `KEY_VOL_DOWN=1`, `KEY_CLICK=32`, `KEY_DOUBLE_CLICK=16`, `KEY_TRIPLE_CLICK=8`, `KEY_LONG_PRESS=4`
- Location: HID thread in `bridge2glm.py`

**Genelec GLM v5 Software (MIDI interface):**
- Protocol: MIDI Control Change (CC) messages over virtual MIDI ports
- SDK/Client: `mido` + `python-rtmidi` backend
- MIDI input port (commands TO GLM): default `GLMMIDI 1` (configurable `--midi_in_channel`)
- MIDI output port (state FROM GLM): default `GLMOUT 1` (configurable `--midi_out_channel`)
- CC assignments (defined in `midi_constants.py`):
  - CC 20: Absolute volume (0-127, bidirectional - GLM reports current volume)
  - CC 21: Volume increment (momentary, send 127)
  - CC 22: Volume decrement (momentary, send 127)
  - CC 23: Mute (toggle, send 127=mute, 0=unmute)
  - CC 24: Dim (toggle, send 127=dim, 0=undim)
  - CC 28: System power (momentary toggle, no MIDI feedback from GLM)
- GLM executable path: `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe`

## APIs & External Services

**REST API (self-hosted, outbound to clients):**
- Framework: FastAPI + uvicorn
- Default port: 8080 (configurable `--api_port`, disabled if 0)
- Endpoints:
  - `GET /api/state` - Current GLM state (volume, mute, dim, power, settling status)
  - `POST /api/volume` - Set absolute volume (`{"value": 0-127}`)
  - `POST /api/volume/adjust` - Relative volume (`{"delta": N}`)
  - `POST /api/mute` - Set/toggle mute (`{"state": true/false/null}`)
  - `POST /api/dim` - Set/toggle dim (`{"state": true/false/null}`)
  - `POST /api/power` - Set/toggle power (`{"state": true/false/null}`)
  - `GET /api/health` - Health check with `volume_initialized` flag
  - `WS /ws/state` - WebSocket for real-time state push to clients
  - `GET /` - Serves `web/index.html` (web UI)
- Auth: None
- Location: `api/rest.py`

**MQTT / Home Assistant (optional, outbound):**
- Library: `paho-mqtt` (`CallbackAPIVersion.VERSION2`)
- Broker: configurable `--mqtt_broker` hostname (disabled if absent)
- Port: default 1883 (`--mqtt_port`)
- Auth: optional `--mqtt_user` / `--mqtt_pass`
- Topic prefix: default `glm` (`--mqtt_topic`)
- Published state topic: `glm/state` (JSON with volume, volume_db, mute, dim, power, power_transitioning)
- Availability topic: `glm/availability` (LWT = `offline`, online = `online`)
- Command topics subscribed:
  - `glm/set/volume` - Integer dB (≤0) or raw (0-127)
  - `glm/set/mute` - ON/OFF/TOGGLE/true/false/1/0
  - `glm/set/dim` - ON/OFF/TOGGLE/true/false/1/0
  - `glm/set/power` - ON/OFF/TOGGLE/true/false/1/0
- Home Assistant MQTT Discovery: auto-publishes entity configs to `homeassistant/{type}/glm_{entity}/config` when `--mqtt_ha_discovery` (default enabled)
  - Creates: `number` entity (volume in dB), `switch` entities for mute/dim/power
- Location: `api/mqtt.py`

## Windows System Integrations

**Windows User32 / UI Automation (GlmPowerController):**
- Purpose: Deterministic power on/off by reading the GLM power button's pixel color and synthesizing mouse clicks
- Why needed: GLM CC 28 is toggle-only with no MIDI feedback; UI automation provides explicit on/off and state verification
- Window discovery: `pywinauto.Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")` filtered by "GLM" in title
- Pixel sampling: `PIL.ImageGrab.grab(bbox=..., all_screens=True)` - captures a patch around the power button position relative to window rect (default: 28px from right, 80px from top)
- Color classification thresholds (in `GlmPowerConfig`):
  - OFF: `max(R,G,B) <= 95` AND channels within 22 of each other (dark grey)
  - ON: `G >= 110` AND `G - R >= 35` (green/teal)
- Mouse synthesis: `win32api.SetCursorPos()` + `win32api.mouse_event(MOUSEEVENTF_LEFTDOWN/UP)`
- Focus management: Alt key trick (`keybd_event(VK_MENU)`) to bypass `SetForegroundWindow` restrictions
- Window state restore: captures `IsIconic()` + `GetForegroundWindow()` before operations, restores after
- Location: `PowerOnOff/glm_power.py`

**Windows User32 / IsHungAppWindow (GlmManager watchdog):**
- Purpose: Detect if GLM GUI has frozen (not just process death)
- API: `ctypes.windll.user32.IsHungAppWindow(hwnd)`
- Fallback: if no window found, assumes responsive (GLM may still be loading)
- Hung detection threshold: 6 consecutive failed checks × 5s interval = 30s before kill+restart
- Location: `PowerOnOff/glm_manager.py`

**Windows User32 / ShowWindow + PostMessage (window minimize):**
- Purpose: Minimize GLM after startup to keep it out of the way
- Primary: `ctypes.windll.user32.ShowWindow(hwnd, SW_MINIMIZE=6)`
- Fallback: `ctypes.windll.user32.PostMessageW(hwnd, WM_SYSCOMMAND=0x0112, SC_MINIMIZE=0xF020, 0)`
- Verification: `ctypes.windll.user32.IsIconic(hwnd)`
- Location: `PowerOnOff/glm_manager.py`

**Windows WTS API (session management):**
- Purpose: Detect RDP disconnect and reconnect session to console display so UI automation can access the screen
- APIs used (via ctypes):
  - `wtsapi32.WTSEnumerateSessionsW` - enumerate sessions to find disconnected state
  - `kernel32.ProcessIdToSessionId` - get current process's session ID
  - `kernel32.WTSGetActiveConsoleSessionId` - get console session ID
  - `user32.GetSystemMetrics(SM_REMOTESESSION=0x1000)` - detect if running in RDP
  - `user32.EnumDisplayMonitors` - enumerate connected monitors (diagnostics)
- Session reconnect: `subprocess.run(["tscon", session_id, "/dest:console"])` - requires Admin
- Elevation fallback: `psexec -s -accepteula tscon ...` if direct tscon fails
- Location: `PowerOnOff/glm_power.py` (`is_console_session`, `is_session_disconnected`, `ensure_session_connected`, `reconnect_to_console`)

**Windows Credential Manager (RDP priming):**
- Purpose: Store RDP credentials for localhost connection used in session priming
- Access method: `cmdkey /generic:localhost /user:USERNAME /pass:PASSWORD`
- Credential type: Generic (not domain) - required so Win32 API can read the password
- Runtime access: credentials read at bridge startup before GLM launch
- Location: `bridge2glm.py` (RDP priming logic, references `CLAUDE.md` for setup)

**FreeRDP (`wfreerdp.exe`) - RDP Session Priming:**
- Purpose: Pre-prime the Windows display session before GLM starts, preventing high CPU from OpenGL/display driver issues after RDP disconnect
- Binary: `wfreerdp.exe` (must be in PATH)
- Invocation: `wfreerdp /v:localhost /u:.\USERNAME /p:PASSWORD /cert:ignore /sec:nla`
- Boot tracking: `%TEMP%\rdp_primed.flag` with boot timestamp - priming runs only once per boot
- Location: `bridge2glm.py`

**Windows Process / Priority:**
- `psutil.Process.nice(psutil.ABOVE_NORMAL_PRIORITY_CLASS)` - sets GLM to above-normal priority after launch
- `win32process.SetThreadPriority(win32api.GetCurrentThread(), priority)` - sets HID thread to above-normal, logging thread to idle
- Location: `bridge2glm.py`, `PowerOnOff/glm_manager.py`

## Data Storage

**Databases:** None

**File Storage:**
- Log files: rotating `.log` in script directory (e.g., `bridge2glm.log`)
- Boot priming flag: `%TEMP%\rdp_primed.flag` (Windows temp directory, persists across sessions but compared against boot time)

**Caching:** In-memory only (no Redis, no disk cache beyond log files)

## Authentication & Identity

**REST API:** No authentication. Listens on `0.0.0.0:8080` - network-accessible with no auth. Appropriate only for trusted LAN use.

**MQTT:** Optional username/password via `--mqtt_user` / `--mqtt_pass`. Passed as plaintext to paho-mqtt; no TLS configuration present.

**Windows Credential Manager:** Used only for RDP priming credentials (localhost RDP). Encrypted at rest via Windows DPAPI.

## Monitoring & Observability

**Error Tracking:** None (no Sentry, no Application Insights)

**Logs:**
- Rotating file: 4MB × 5 backups, written to script directory
- Console: INFO+ to stdout
- Thread-safe via `QueueHandler` + `QueueListener` (dedicated `LoggingThread`)
- Smart retry throttling via `SmartRetryLogger` (`retry_logger.py`) - logs at 2s, 10s, 1min, 10min, 1hr, 1day milestones during failure loops

**Windows Event Log:** Not used

## CI/CD & Deployment

**Hosting:** Windows desktop/server VM (accessed via RDP + VNC/console)

**CI Pipeline:** None

**Startup:** Manual launch or Windows Task Scheduler (inferred from context; no Task Scheduler XML included in repo)

## Webhooks & Callbacks

**Incoming:** None (MQTT subscriptions serve this role for Home Assistant commands)

**Outgoing:**
- WebSocket push to browser clients at `ws://<host>:8080/ws/state` on every GLM state change
- MQTT retained publish to `glm/state` on every GLM state change
- MQTT LWT (Last Will and Testament) publishes `offline` to `glm/availability` on disconnect

---

## Dual Perspective Analysis

### Senior Developer Perspective

**No TLS on REST API or MQTT:**
The REST API binds to `0.0.0.0` with no authentication or TLS. MQTT uses plaintext with optional basic auth. Both are suitable only for a closed LAN. Any future external access requires a reverse proxy (nginx/Caddy) with TLS and auth.

**MQTT Password in CLI Args:**
`--mqtt_pass` is passed as a command-line argument, visible in `ps`/Task Manager process list and shell history. Should be moved to Credential Manager or environment variable.

**WebSocket Error Suppression is Fragile:**
`api/rest.py` applies `WebSocketErrorFilter` at import time AND re-applies in `start_api_server()`. This multi-stage suppression pattern is a code smell indicating that uvicorn/websockets logging is not well-controlled. The `_apply_websocket_suppression()` function mutates global logging state.

**No Retry/Reconnect on MQTT:**
`MqttClient.start()` calls `client.connect()` once. If the broker is unavailable at startup, it logs an error and returns silently. `paho-mqtt`'s `loop_start()` handles reconnection automatically, but there is no startup retry logic.

**Power State is Best-Effort:**
The `power` field in `GlmController` is tracked locally (not confirmed via MIDI, since GLM CC 28 has no feedback). UI automation (`GlmPowerController`) provides confirmation via pixel sampling, but only when `POWER_CONTROL_AVAILABLE=True`. If pywinauto/Pillow/pywin32 are missing, power state is estimated only.

### Senior Windows Desktop App Architect Perspective

**JUCE Window Class Name Dependency:**
Window discovery uses `class_name_re=r"JUCE_.*"` which is a JUCE framework implementation detail. A GLM update that changes the JUCE version or window class naming could silently break all UI automation. No fallback enumeration strategy exists.

**Pixel Sampling Fragility:**
Power state detection relies on hardcoded pixel offsets from the window edge (`dx_from_right=28`, `dy_from_top=80`). A GLM UI update, DPI scaling change, or Windows theme change could shift these coordinates. The `GlmPowerConfig` dataclass makes them configurable, but there is no auto-calibration or self-test.

**Session 0 Isolation:**
If the application were ever run as a Windows Service (Session 0), the UI automation would fail entirely because services cannot interact with the interactive desktop. The current architecture requires the process to run in the interactive user session. This is correct for the use case but must be maintained.

**tscon Requires SYSTEM or Admin:**
`reconnect_to_console()` first tries direct `tscon` (requires Admin) then `psexec -s` (requires psexec in PATH and local SYSTEM access). If neither is available, session reconnection silently fails and UI automation may not work after RDP disconnect. This is a documented operational requirement.

**RDP Priming is Boot-Level State:**
The `%TEMP%\rdp_primed.flag` file is compared against boot time to ensure priming only runs once per boot. If `%TEMP%` is on a RAM disk or is cleared on login, priming will re-run unnecessarily but harmlessly. The flag file approach is simpler than a registry key but less robust.

**Win32api Mouse Synthesis Scope:**
`win32api.mouse_event()` with `MOUSEEVENTF_LEFTDOWN/UP` sends to the system input queue (not directly to the window), meaning the click goes to whichever window is at the cursor coordinates at time of delivery. The focus management (Alt key trick + `SetForegroundWindow`) before clicking is essential to ensure GLM receives the click, not another foreground window.

---

*Integration audit: 2026-03-21*
