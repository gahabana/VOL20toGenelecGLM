# Technology Stack

**Analysis Date:** 2026-03-21

## Languages

**Primary:**
- Python 3.13 - All application code (confirmed by `__pycache__` `.cpython-313.pyc` files)

**Secondary:**
- HTML/SVG - Web UI (`web/index.html`, `web/favicon.svg`)
- Batch - Utility script (`move_mesa_files.bat`)

## Runtime

**Environment:**
- CPython 3.13 (Windows)
- **No virtual environment or `.python-version` file detected** - dependencies assumed globally installed

**Package Manager:**
- pip (inferred from `requirements.txt`)
- Lockfile: **absent** - `requirements.txt` has no pinned versions (e.g., `hidapi`, `mido`, not `hidapi==0.14.0`)

## Frameworks

**Core:**
- `mido` - MIDI I/O abstraction layer (send/receive MIDI CC messages to/from GLM)
- `python-rtmidi` - Backend for `mido` (Windows MIDI port access via WinMM)
- `hidapi` - Raw HID device access (reads Fosi Audio VOL20 USB knob, VID `0x07d7`)

**REST API:**
- `fastapi` - Async REST + WebSocket server (`api/rest.py`)
- `uvicorn[standard]` - ASGI server run in a background `threading.Thread`

**MQTT:**
- `paho-mqtt` - MQTT v5 client (`api/mqtt.py`), uses `CallbackAPIVersion.VERSION2`

**UI Automation (Windows-only):**
- `pywinauto` - Window enumeration via `Desktop(backend="win32")`, finds JUCE windows by `class_name_re=r"JUCE_.*"` with "GLM" in title
- `Pillow` (`PIL.ImageGrab`) - Screen pixel sampling for power button color detection
- `pywin32` (`win32api`, `win32con`, `win32process`) - Mouse synthesis, keyboard events, thread priority

**Process Management:**
- `psutil` - Process enumeration (`process_iter`), priority setting (`ABOVE_NORMAL_PRIORITY_CLASS`), CPU usage polling

**Build/Dev:**
- No build system detected (no `pyproject.toml`, `setup.py`, `Makefile`)
- No test framework detected (no `pytest`, `unittest`, test files)
- No linter/formatter config detected (no `.flake8`, `pyproject.toml`, `.pylintrc`)

## Key Dependencies

**Critical:**
- `hidapi` - Without this, no input from the VOL20 knob. HID read loop polls every `HID_READ_TIMEOUT_MS=1000ms`.
- `mido` + `python-rtmidi` - Without these, no MIDI communication with GLM. App retries connection continuously.
- `pywinauto` + `Pillow` + `pywin32` - Required for `PowerOnOff` module. Gracefully degrades to `POWER_CONTROL_AVAILABLE=False` if absent.
- `psutil` - Required for `GlmManager` watchdog and CPU gating. Also used for thread priority in main script.

**Infrastructure:**
- `fastapi` + `uvicorn` - REST API on port 8080 (default). Disabled if `--api_port 0`. Runs in `APIServerThread` daemon thread.
- `paho-mqtt` - MQTT integration for Home Assistant. Disabled unless `--mqtt_broker` is specified.

## Configuration

**Environment:**
- All configuration via CLI arguments parsed in `config.py` using `argparse`
- No `.env` file usage - configuration is not environment-variable-based
- Windows Credential Manager used for RDP priming credentials (read via `cmdkey`/Win32 API, not a Python library in this codebase)
- Key configurable parameters:
  - `--device VID,PID` (default: `0x07d7,0x0000`) - VOL20 HID device
  - `--midi_in_channel` / `--midi_out_channel` - MIDI port names
  - `--glm_path` - Path to `GLMv5.exe` (default: `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe`)
  - `--api_port` (default: 8080, 0 = disabled)
  - `--mqtt_broker` (absent = disabled)
  - `--startup_volume` (0-127, optional)

**Build:**
- No build configuration files

**Logging:**
- `RotatingFileHandler`: 4MB max, 5 backups, written to script directory
- `QueueHandler` + `QueueListener` for async logging from multiple threads
- Format: `%(asctime)s [%(levelname)s] %(threadName)s %(module)s:%(funcName)s:%(lineno)d - %(message)s`
- Log file named after script (e.g., `bridge2glm.log`)

## Platform Requirements

**Development & Production (Windows only):**
- Windows 10/11 (RDP session management, WTS APIs, JUCE window class names)
- Genelec GLM v5 installed at `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe`
- MIDI loopback driver providing virtual ports `GLMMIDI 1` (input to GLM) and `GLMOUT 1` (output from GLM)
- Fosi Audio VOL20 USB HID device
- Optional: FreeRDP (`wfreerdp.exe`) in PATH for RDP session priming
- Optional: PsExec in PATH for elevated `tscon` calls

**Version:** `bridge2glm.py` declares `__version__ = "3.2.22"`

---

## Dual Perspective Analysis

### Senior Developer Perspective

**Dependency Version Pinning - High Risk:**
`requirements.txt` lists packages without versions (`hidapi`, `mido`, etc.). A `pip install -r requirements.txt` on a new machine may install incompatible versions. `paho-mqtt` version is critical: the code uses `CallbackAPIVersion.VERSION2` (paho-mqtt >= 2.0). Installing paho-mqtt 1.x would cause a runtime `AttributeError`. No `requirements-lock.txt` or `pip freeze` output exists.

**No Virtual Environment:**
No `venv`, `.python-version`, or `pyenv` configuration exists. The application is assumed to run with globally-installed packages on the Windows machine.

**No Test Suite:**
Zero test files exist. The application has complex stateful logic (power settling timers, MIDI pattern detection, UI automation timing) with no automated verification. Manual testing only.

**No Linting/Formatting Config:**
No `.flake8`, `pylint`, `black`, or `ruff` configuration. Code quality relies entirely on author discipline.

**Mixed Sync/Async Architecture:**
`bridge2glm.py` is synchronous/threaded. The REST API (`api/rest.py`) is async (FastAPI/uvicorn) running in a separate thread with its own event loop. Cross-thread WebSocket broadcasts use `asyncio.run_coroutine_threadsafe()`. This dual-paradigm design works but is fragile - the `_api_event_loop` global must be set before broadcasts are attempted.

**No Entry Point Script/Packaging:**
The application is launched directly as `python bridge2glm.py`. No `__main__` guard issue (it does use `parse_arguments()`), but there is no installable package structure.

### Senior Windows Desktop App Architect Perspective

**Thread Priority Management via win32process:**
The main script uses `win32process.SetThreadPriority()` with constants (`THREAD_PRIORITY_ABOVE_NORMAL`, `THREAD_PRIORITY_IDLE`) to prioritize the HID reader thread above normal and the logging thread at idle. This is correct Windows practice for real-time HID polling.

**Process Priority via psutil:**
`GlmManager._start_glm()` sets GLM's process priority to `ABOVE_NORMAL_PRIORITY_CLASS` using `psutil.Process.nice()`. This maps correctly to Windows `SetPriorityClass`.

**No Windows Service:**
The application runs as a foreground process (or via Task Scheduler, based on CLAUDE.md context). There is no `win32serviceutil.ServiceFramework` wrapper. Running as a Windows Service would require refactoring the UI automation components (session 0 isolation problem).

**No Windows Event Log:**
Logging is file-only + console. No `win32evtlog` or `win32evtlogutil` integration. Production diagnostics rely entirely on the rotating log file.

**No Job Object:**
`GlmManager` starts GLM via `subprocess.Popen` without assigning it to a Windows Job Object. If the bridge process crashes, GLM continues running unmanaged. A Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` would ensure GLM is killed when the bridge exits.

**Credential Manager Access Pattern:**
RDP priming credentials are read from Windows Credential Manager at runtime (via `cmdkey` CLI or Win32 API). The credentials use `/generic:localhost` type (not domain credentials) specifically because generic credentials expose passwords via the API while domain credentials do not. This is a deliberate security trade-off documented in CLAUDE.md.

---

*Stack analysis: 2026-03-21*
