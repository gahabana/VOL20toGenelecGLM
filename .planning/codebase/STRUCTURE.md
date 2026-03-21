# Codebase Structure

**Analysis Date:** 2026-03-21

## Directory Layout

```
VOL20toGenelecGLM/
├── .planning/                    # GSD planning artifacts (generated, not committed)
│   └── codebase/                # This analysis output
├── .claude/                      # Claude Code context guidelines
├── .git/                         # Version control
├── PowerOnOff/                   # Power control & process management (Windows only)
│   ├── __init__.py              # Public API exports
│   ├── glm_power.py             # UI automation for power button
│   ├── glm_manager.py           # Process lifecycle & watchdog
│   └── exceptions.py            # Custom exception types
├── api/                          # Network interfaces
│   ├── __init__.py
│   ├── rest.py                  # FastAPI server + WebSocket
│   └── mqtt.py                  # MQTT client for Home Assistant
├── glm_core/                     # Domain model
│   ├── __init__.py              # Public API exports
│   └── actions.py               # GlmAction dataclasses
├── web/                          # Frontend assets
│   ├── index.html               # Web UI (served by FastAPI)
│   └── favicon.svg              # Browser icon
├── __pycache__/                  # Compiled Python bytecode (generated)
├── bridge2glm.py                # Main application entry point (1492 lines)
├── config.py                    # CLI argument parsing & validation
├── midi_constants.py            # MIDI CC mappings & enums
├── acceleration.py              # Volume acceleration handler
├── logging_setup.py             # Logging configuration
├── retry_logger.py              # Retry-aware logging wrapper
├── requirements.txt             # Python package dependencies
├── README.md                    # User documentation
├── CLAUDE.md                    # Claude Code guidelines for this project
├── HANDOFF-v3.0.0.md            # Version history & known issues
├── FUTURE_work.md               # Planned improvements
├── .gitignore                   # Git ignore rules
└── *.log                        # Log files (gitignored)
```

## Directory Purposes

**PowerOnOff/:**
- Purpose: Windows-only power and process management for Genelec GLM
- Contains: UI automation (pixel sampling + mouse clicks), process lifecycle, watchdog, custom exceptions
- Key files: `glm_power.py` (1012 lines), `glm_manager.py` (655 lines)
- Conditionally imported in `bridge2glm.py` (graceful fallback on non-Windows)

**api/:**
- Purpose: Network interfaces for remote control and integration
- Contains: FastAPI REST endpoints, WebSocket handler, MQTT client with Home Assistant discovery
- Key files: `rest.py` (core server logic), `mqtt.py` (MQTT protocol)
- Spawned as background threads from main application

**glm_core/:**
- Purpose: Domain model and core abstractions
- Contains: Immutable action dataclasses (SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction)
- Key files: `actions.py` (all action definitions)
- No dependencies outside Python stdlib; used by all layers

**web/:**
- Purpose: Frontend UI served by FastAPI static mount
- Contains: HTML5 + JavaScript, WebSocket client, responsive control panel
- Key files: `index.html`, `favicon.svg`
- Served from FastAPI at `/` (default) or custom route

**Root Level (Main Application):**
- `bridge2glm.py` (1492 lines): Main entry point, orchestrates all subsystems
  - HID reader thread (polling VOL20 device)
  - MIDI reader thread (listening for GLM feedback)
  - Action consumer loop (processes queue, sends MIDI)
  - Power control integration (UI automation fallback)
  - Acceleration handling (velocity-based volume changes)
  - Logging and CLI interface

- `config.py`: Argument parsing with validation for:
  - Device VID/PID (HID target device)
  - Click timing (double-tap detection)
  - Volume acceleration curve
  - MIDI channel names
  - API port, MQTT broker, GLM executable path

- `midi_constants.py`: Immutable MIDI mappings:
  - Action enum (VOL_UP, VOL_DOWN, MUTE, DIM, POWER)
  - GlmControl dataclass (CC number, label, mode)
  - GLM CC numbers (20-28 range)
  - Power pattern detection constants

- `acceleration.py`: AccelerationHandler class for velocity-sensitive volume
  - Detects click speed, applies configurable acceleration curve
  - Tracks click timing and button state

- `logging_setup.py`: Logging initialization
  - RotatingFileHandler for file logs
  - QueueHandler + QueueListener for async I/O
  - Configurable level (DEBUG, INFO, NONE)

- `retry_logger.py`: Wrapper for retryable operations
  - Exponential backoff logging
  - Tracks retry attempts

## Key File Locations

**Entry Points:**
- `bridge2glm.py` (line ~1450+): Main script execution
  - Parses args, initializes subsystems, runs main loop
  - `if __name__ == '__main__': setup_logging(...); main()`

**Configuration:**
- `config.py` (lines 90-177): `parse_arguments()` function
  - Returns namespace with all CLI options
  - Validation rules for each argument

- `midi_constants.py` (lines 45-59): MIDI CC number definitions
  - GLM_VOLUME_ABS = 20
  - GLM_MUTE_CC = 23
  - GLM_DIM_CC = 24
  - GLM_POWER_CC = 28

**Core Logic:**
- `glm_core/actions.py`: Domain action definitions (SetVolume, etc.)
- `bridge2glm.py` (lines 200-400+): HID reader thread implementation
- `bridge2glm.py` (lines 700-900+): MIDI reader thread (power pattern detection)
- `bridge2glm.py` (lines 1000-1200+): Main consumer loop (action execution)
- `PowerOnOff/glm_power.py` (lines 100-300+): Power button pixel sampling
- `PowerOnOff/glm_manager.py` (lines 200-400+): Process watchdog thread

**Testing:**
- No automated test files in codebase
- Manual testing via log inspection and `bridge2glm.log`

## Naming Conventions

**Files:**
- Snake case: `glm_manager.py`, `midi_constants.py`, `logging_setup.py`
- Exception module: `exceptions.py` (by convention)
- Main entry point: `bridge2glm.py` (descriptive)

**Directories:**
- Camel case / Descriptive: `PowerOnOff/`, `glm_core/`, `api/`, `web/`
- Plural for collections: `api/` (multiple endpoints), `web/` (multiple assets)

**Classes:**
- Pascal case: `GlmPowerController`, `GlmManager`, `AccelerationHandler`, `MqttClient`
- Exception classes: `GlmWindowNotFoundError`, `GlmStateUnknownError`
- Config dataclasses: `GlmPowerConfig`, `GlmManagerConfig`

**Functions:**
- Snake case: `parse_arguments()`, `setup_logging()`, `main()`, `calculate_speed()`
- Private functions (internal module use): `_send_to_glm()`, `_apply_websocket_suppression()`

**Variables:**
- Module-level constants: ALL_CAPS: `MAX_EVENT_AGE`, `POWER_SETTLING_TIME`, `HID_READ_TIMEOUT_MS`
- Instance attributes: snake_case: `self.last_button`, `self.last_time`
- Thread names: Descriptive: `"HID Reader"`, `"MIDI Reader"`, `"API Server"`, `"MQTT Client"`

**Types & Enums:**
- Action enum: `Action.VOL_UP`, `Action.MUTE`, `Action.POWER`
- ControlMode enum: `ControlMode.MOMENTARY`, `ControlMode.TOGGLE`
- PowerState: `"on"`, `"off"`, `"unknown"` (string literals, not enum)

## Where to Add New Code

**New Feature (e.g., new GLM control like "Scene Recall"):**
1. Add action class to `glm_core/actions.py`:
   ```python
   @dataclass(frozen=True)
   class SetScene:
       scene_id: int  # 0-127
   ```

2. Add MIDI constant to `midi_constants.py`:
   ```python
   GLM_SCENE_RECALL_CC = 29  # Your chosen CC number
   ```

3. Add to Action enum in `midi_constants.py`:
   ```python
   SCENE_RECALL = "SceneRecall"
   ```

4. Add handler in `bridge2glm.py` action consumer (main loop):
   ```python
   elif isinstance(action, SetScene):
       _send_to_glm(GLM_SCENE_RECALL_CC, action.scene_id)
   ```

5. Add REST endpoint in `api/rest.py`:
   ```python
   @app.post("/api/actions/scene/{scene_id}")
   async def recall_scene(scene_id: int):
       action = SetScene(scene_id=scene_id)
       action_queue.put(QueuedAction(action, time.time()))
       return {"status": "queued"}
   ```

6. Add MQTT topic handler in `api/mqtt.py` (on_message callback):
   ```python
   elif payload_dict.get("action") == "recall_scene":
       action = SetScene(scene_id=payload_dict["scene_id"])
       self._action_queue.put(QueuedAction(action, time.time()))
   ```

**New Input Adapter (e.g., OSC protocol from external app):**
1. Create `api/osc.py` with OSCServer class
2. In main `bridge2glm.py`, import and start OSC server thread:
   ```python
   from api.osc import OscServer
   osc_server = OscServer(action_queue)
   osc_thread = threading.Thread(target=osc_server.start, daemon=True)
   osc_thread.start()
   ```
3. OSCServer.on_message() creates QueuedAction and queues it (same pattern as MQTT)

**New Component/Module (e.g., Advanced State Manager):**
1. Create `state_manager.py` in root or new `core/` directory
2. Define class:
   ```python
   class GlmStateManager:
       def __init__(self):
           self.volume = 79
           self.mute = False
           self.power = "unknown"

       def update(self, action: GlmAction):
           # Sync with action execution
           pass
   ```
3. Instantiate in main `bridge2glm.py` and pass to threads needing state
4. No separate directory needed unless it has 5+ related files

**Utilities (e.g., MIDI validation helper):**
- Add to `midi_constants.py` if MIDI-related
- Add to `config.py` if config-related
- Create standalone `utils.py` only if >50 lines and used by multiple modules

## Special Directories

**__pycache__/:**
- Purpose: Generated Python bytecode cache
- Generated: Yes (automatic, by Python interpreter)
- Committed: No (in .gitignore)
- Safe to delete: Yes (will be regenerated)

**.planning/codebase/:**
- Purpose: GSD analysis artifacts (this file, ARCHITECTURE.md, etc.)
- Generated: Yes (by GSD analysis tools)
- Committed: Yes (in git, tracked)
- Updates: Regenerate when architecture changes

**.claude/:**
- Purpose: Claude Code context and instructions
- Generated: No (hand-written project guidelines)
- Committed: Yes (in git, tracked)
- Updates: Manually edited project-wide preferences

**.git/:**
- Purpose: Version control metadata
- Generated: Yes (by git)
- Committed: Not applicable
- Safe to delete: Only by `git reset --hard` (destructive)

---

## Dual Perspective Analysis

### Senior Developer Perspective vs. macOS App Architect Perspective

**AGREEMENT:**
- Both perspectives see the **clear module separation**: `PowerOnOff/`, `api/`, `glm_core/` are well-organized
- Both appreciate **single entry point** (`bridge2glm.py`) that orchestrates all subsystems
- Both recognize **immutable actions** in `glm_core/` as the right choice for a queue-based architecture
- Both view the **conditional imports** (Windows-only graceful fallback) as pragmatic
- Both see `.planning/` directory as appropriate for generated artifacts (separate from source)

**DIVERGENCE:**

| Senior Developer | macOS App Architect |
|------------------|---------------------|
| **Modularity:** Layout is clean; good cohesion within each module (HID, MIDI, API all separate). | **Bundle Structure:** No macOS app bundle (`.app` directory). Python script + assets sitting flat. Would need `Info.plist`, `Contents/` structure, code signing setup. |
| **Entry Point:** Single `bridge2glm.py` is clear and obvious. Easy to trace execution flow. | **Application Lifecycle:** No `AppDelegate` or lifecycle hooks. Python script runs indefinitely via loop—works but not macOS-idiomatic. Would expect app delegate + quit handler + preferences window. |
| **Reusability:** `glm_core/`, `PowerOnOff/`, `api/` could be imported as libraries in other projects. Good boundaries. | **Packaging:** No setup.py, pyproject.toml, or pip-installable structure. Can't `pip install` this package. On macOS, would want: signed `.app` with embedded Python, installable via DMG or Homebrew. |
| **Growth Path:** Adding new adapters (OSC, HTTP server) is straightforward—add to `api/`, import in `bridge2glm.py`. | **System Integration:** No `.plist` files for LaunchAgent/Daemon. No code signing certificate. No Gatekeeper-approved installer. Won't install cleanly on macOS beyond user's own machine. |
| **Testing:** Missing unit tests, but code structure supports them well (actions are testable dataclasses). | **Persistence:** No macOS-native preferences storage (NSUserDefaults, System Preferences pane). CLI args only; no GUI for settings. |
| | **Accessibility:** No Accessibility Framework (NSAccessibility) integration. Pixel sampling (`PowerOnOff/glm_power.py`) is fragile across display scaling and Dark Mode. Would use AXUIElement API if staying on macOS. |

**Translation to macOS:**
1. **Directory layout** would become:
   ```
   VOL20toGenelecGLM.app/
   ├── Contents/
   │   ├── MacOS/
   │   │   └── glm_bridge (executable launcher)
   │   ├── Resources/
   │   │   ├── glm_core/
   │   │   ├── api/
   │   │   ├── PowerOnOff/  (would use Accessibility API instead)
   │   │   ├── web/
   │   │   └── icon.icns
   │   └── Info.plist
   └── [source code same as above]
   ```

2. **Entry point** would be a compiled launcher (Swift/Objective-C shim) that:
   - Ensures Python runtime bundled with app
   - Calls `bridge2glm.py` with correct PYTHONPATH
   - Handles app lifecycle (quit, reopen, etc.)

3. **Configuration** would use:
   - `~/Library/Preferences/com.genelec.glm-bridge.plist` for persistent settings
   - System Preferences pane (if needed) instead of CLI args

4. **Process supervision** would use:
   - `~/Library/LaunchAgents/com.genelec.glm-bridge.plist` if running as user agent
   - Or rely on macOS Finder's persistent app launch

5. **Power control** would use:
   - Core MIDI instead of `mido` (native macOS MIDI)
   - Accessibility API instead of pixel sampling
   - AppleScript if Genelec GLM supports it

**Senior Developer Assessment:** Code structure is excellent for cross-platform maintainability. Clean module boundaries, good separation of concerns. Would grow well.

**macOS Architect Assessment:** Application works on macOS (with proper Python environment) but is not packaged or distributed as a native macOS app. Missing app bundle, code signing, preferences integration, native framework usage (Core MIDI, Accessibility). Treating it as "just a Python script" limits user experience and system integration.

---

*Structure analysis: 2026-03-21*
