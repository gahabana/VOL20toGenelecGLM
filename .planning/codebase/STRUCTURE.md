# Codebase Structure

**Analysis Date:** 2026-03-21

## Directory Layout

```
VOL20toGenelecGLM/
├── bridge2glm.py          # Main entry point — daemon, GlmController, all threads
├── config.py              # CLI argument parsing and validation
├── acceleration.py        # Volume acceleration handler (knob speed → delta)
├── midi_constants.py      # MIDI CC numbers, Action/ControlMode enums, key bindings
├── logging_setup.py       # Async queue-based logging setup
├── retry_logger.py        # SmartRetryLogger — throttled retry-loop logging
│
├── glm_core/
│   ├── __init__.py        # Re-exports all action types
│   └── actions.py         # GlmAction frozen dataclasses (SetVolume, SetPower, etc.)
│
├── PowerOnOff/
│   ├── __init__.py        # Re-exports GlmPowerController, GlmManager, helpers
│   ├── glm_power.py       # UI automation power control (pixel sampling, mouse click)
│   ├── glm_manager.py     # GLM process lifecycle, watchdog, restart
│   ├── exceptions.py      # GlmPowerError hierarchy
│   └── INTEGRATION.md     # Integration notes for PowerOnOff module
│
├── api/
│   ├── __init__.py        # Re-exports start_api_server, start_mqtt_client
│   ├── rest.py            # FastAPI + WebSocket REST server
│   └── mqtt.py            # MQTT client (paho) with HA Discovery
│
├── web/
│   ├── index.html         # Single-page web UI (served by REST API at /)
│   └── favicon.svg        # Web UI favicon
│
├── requirements.txt       # Python dependencies
├── move_mesa_files.bat    # Windows batch script (OpenGL Mesa DLL management)
├── CLAUDE.md              # Claude Code guidelines (checked in)
├── README.md              # Project README
├── FUTURE_work.md         # Backlog / planned features
└── HANDOFF-v3.0.0.md      # Version 3.0.0 handoff notes
```

---

## Directory Purposes

**Root level (`.py` files):**
- Purpose: Application entry point and flat utility modules
- `bridge2glm.py` is the monolithic main file: contains `HIDToMIDIDaemon`, `GlmController`, all thread methods, RDP priming, MIDI service restart, console minimization, and `__main__` startup sequence
- Utility modules (`acceleration.py`, `midi_constants.py`, `logging_setup.py`, `retry_logger.py`, `config.py`) are standalone with no inter-module imports beyond stdlib

**`glm_core/`:**
- Purpose: Pure domain model — what the system can do, independent of how
- Contains: Frozen dataclass commands only. No Win32, no I/O, no state.
- Key file: `glm_core/actions.py`
- Import pattern: `from glm_core import SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction`

**`PowerOnOff/`:**
- Purpose: Windows-only UI automation and process management for GLM
- `glm_power.py`: pixel-sampling power controller (requires pywinauto, Pillow, pywin32)
- `glm_manager.py`: GLM process lifecycle + watchdog (requires psutil, pywinauto)
- All imports are conditional (`try/except ImportError`) so the module loads on non-Windows without crashing
- Availability flags: `POWER_CONTROL_AVAILABLE`, `GLM_MANAGER_AVAILABLE` exported from `__init__.py`

**`api/`:**
- Purpose: External control interfaces
- `rest.py`: FastAPI server with WebSocket broadcast
- `mqtt.py`: MQTT client with Home Assistant Discovery
- Both modules take `action_queue` and `glm_controller` as constructor/factory arguments — no global state except module-level `_action_queue` / `_glm_controller` set by `create_app()`

**`web/`:**
- Purpose: Single-page web UI served statically by the REST API
- `index.html`: complete self-contained SPA (HTML + CSS + JS inline)
- Served at `GET /` by `api/rest.py`

---

## Key File Locations

**Entry Point:**
- `bridge2glm.py` line 1410: `if __name__ == "__main__":`

**Domain Actions (add new commands here):**
- `glm_core/actions.py`

**MIDI CC Numbers and Key Bindings (change hardware mappings here):**
- `midi_constants.py`

**CLI Configuration (add new CLI arguments here):**
- `config.py:parse_arguments()`

**Power Button Color Thresholds (tune pixel detection here):**
- `PowerOnOff/glm_power.py:GlmPowerConfig` dataclass (lines 375–402)

**Watchdog Tuning (adjust GLM restart behavior here):**
- `PowerOnOff/glm_manager.py:GlmManagerConfig` dataclass (lines 67–95)

**State-to-External Serialization (change REST/MQTT state format here):**
- REST: `bridge2glm.py:GlmController.get_state()` (returns the `dict` consumed by all APIs)
- MQTT: `api/mqtt.py:MqttClient._publish_state()`

**Thread Startup Sequence:**
- `bridge2glm.py:HIDToMIDIDaemon.start()` (lines 1302–1369)

**Consumer Dispatch Logic (add handling for new action types here):**
- `bridge2glm.py:HIDToMIDIDaemon.consumer()` (lines 1054–1113)

---

## Naming Conventions

**Files:**
- `snake_case.py` throughout
- Module names describe their primary concern: `glm_power.py`, `glm_manager.py`, `midi_constants.py`

**Classes:**
- `PascalCase`: `GlmController`, `GlmPowerController`, `GlmManager`, `HIDToMIDIDaemon`, `MqttClient`
- Config dataclasses suffixed `Config`: `GlmPowerConfig`, `GlmManagerConfig`

**Functions / Methods:**
- `snake_case`
- Private methods prefixed `_`: `_handle_power_action`, `_find_window`, `_classify_state`
- Thread target methods named after thread: `hid_reader`, `midi_reader`, `consumer`

**Constants:**
- `UPPER_SNAKE_CASE` for module-level constants: `MAX_EVENT_AGE`, `POWER_SETTLING_TIME`, `GLM_MUTE_CC`
- Availability flags: `HAS_WIN32`, `HAS_DEPS`, `POWER_CONTROL_AVAILABLE`, `GLM_MANAGER_AVAILABLE`

**Thread Names:**
- `PascalCaseThread` or `PascalCaseWordThread`: `HIDReaderThread`, `MIDIReaderThread`, `ConsumerThread`, `APIServerThread`, `LoggingThread`, `GLMWatchdog`

---

## Where to Add New Code

**New controllable GLM parameter (e.g., Bass EQ level):**
1. Add frozen dataclass to `glm_core/actions.py`: `@dataclass(frozen=True) class SetBass: ...`
2. Export from `glm_core/__init__.py`
3. Add `elif isinstance(action, SetBass)` handler in `bridge2glm.py:HIDToMIDIDaemon.consumer()`
4. Add MIDI CC constant to `midi_constants.py` if needed
5. Add REST endpoint in `api/rest.py` following existing pattern
6. Add MQTT topic handler in `api/mqtt.py:MqttClient._on_message()`

**New hardware input device:**
1. Add a new reader thread method on `HIDToMIDIDaemon` following `hid_reader` pattern
2. Register thread in `__init__` and `start()` / `stop()`
3. Map device events to `GlmAction` objects and `self.queue.put(QueuedAction(...))`

**New external API (e.g., gRPC):**
1. Create `api/grpc.py` following the structure of `api/rest.py` or `api/mqtt.py`
2. Accept `action_queue` and `glm_controller` as constructor arguments
3. Register `glm_controller.add_state_callback()` for state push notifications
4. Start in `HIDToMIDIDaemon.start()` analogous to the existing API/MQTT start blocks

**New CLI argument:**
- Add to `config.py:parse_arguments()`, pass through `HIDToMIDIDaemon.__init__` args

**New Win32 / Windows-specific functionality:**
- Place in `PowerOnOff/` package with conditional imports guarded by `try/except ImportError`
- Export availability flag from `PowerOnOff/__init__.py` (e.g., `MY_FEATURE_AVAILABLE`)
- Check flag in `bridge2glm.py` before use

**Tests (currently none — see CONCERNS.md):**
- Co-locate as `test_<module>.py` or create `tests/` directory
- `glm_core/actions.py` and `acceleration.py` are pure Python with no Win32 deps — test these first

---

## Special Directories

**`.planning/`:**
- Purpose: GSD planning documents (codebase maps, phase plans)
- Generated: No (hand-edited and agent-written)
- Committed: Yes

**`__pycache__/`, `api/__pycache__/`:**
- Purpose: Python bytecode cache
- Generated: Yes
- Committed: No (in `.gitignore`)

**`web/`:**
- Purpose: Static web UI assets served by REST API
- Generated: No
- Committed: Yes

---

## Dual Perspective Analysis

### Senior Developer Perspective

**What the structure does well:**
- `glm_core/actions.py` is a clean dependency sink — it depends on nothing, so any module can import it without circular risk
- `PowerOnOff/` package boundary cleanly separates Windows-only, import-optional code from the always-importable core. The `try/except ImportError` + availability flag pattern is consistently applied.
- Module-level constants in `midi_constants.py` are the single source of truth for all protocol details; they're not scattered across files.

**What to watch:**
- `bridge2glm.py` handles too many responsibilities. The natural next refactor would extract `GlmController` into `glm_core/controller.py` (it has no Win32 deps), and move RDP/MIDI service startup helpers into `PowerOnOff/` or a new `windows_init.py` module.
- The module-level `glm_controller` singleton in `bridge2glm.py` (line 407) is imported by nothing outside the file (it's accessed by `api/rest.py` and `api/mqtt.py` via constructor injection, not import). This is correct but would become a problem if `bridge2glm.py` were split.

### Senior Windows Desktop App Architect Perspective

**File placement rationale:**
- `move_mesa_files.bat` moves Mesa OpenGL DLLs. This is a deployment concern: Mesa is needed on headless VMs where the QXL/VirtIO display driver doesn't expose hardware OpenGL. The .bat lives at root but is not part of the Python runtime.
- Log files are written to the same directory as `bridge2glm.py` (`os.path.dirname(os.path.abspath(__file__))`). On a VM with a restricted user account, this directory must be writable. If run from `Program Files`, this will fail silently (RotatingFileHandler will raise at startup).
- `%TEMP%\rdp_primed.flag` for per-boot RDP priming state is correct — `%TEMP%` is per-user and persists across logons but is writable by the user. Boot timestamp comparison handles the case where `%TEMP%` is not cleared on reboot.
- No Registry keys are read or written by the Python code. All configuration is CLI args, Windows Credential Manager (via `keyring`), and the flag file.

---

*Structure analysis: 2026-03-21*
