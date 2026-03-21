# Coding Conventions

**Analysis Date:** 2026-03-21

## Naming Patterns

**Files:**
- `snake_case.py` for all modules: `bridge2glm.py`, `glm_power.py`, `glm_manager.py`, `retry_logger.py`, `logging_setup.py`, `midi_constants.py`, `acceleration.py`
- Subpackages use `snake_case` directories: `PowerOnOff/`, `glm_core/`, `api/`
- Constants modules named by domain: `midi_constants.py`, not `constants.py`

**Classes:**
- `PascalCase` throughout: `GlmController`, `GlmPowerController`, `GlmManager`, `GlmManagerConfig`, `GlmPowerConfig`, `SmartRetryLogger`, `AccelerationHandler`
- Config dataclasses suffix with `Config`: `GlmManagerConfig`, `GlmPowerConfig`
- Exception classes suffix with `Error`: `GlmPowerError`, `GlmWindowNotFoundError`, `GlmStateUnknownError`, `GlmStateChangeFailedError`
- Action dataclasses use imperative verbs: `SetVolume`, `AdjustVolume`, `SetMute`, `SetDim`, `SetPower`

**Functions and Methods:**
- `snake_case` for all functions and methods
- Private methods prefixed with `_`: `_find_window`, `_ensure_foreground`, `_click_point`, `_watchdog_loop`
- Boolean-returning query methods prefixed with `is_` or `has_`: `is_alive`, `is_responding`, `is_available`, `has_valid_volume`
- Boolean-returning state checks: `needs_rdp_priming`, `is_console_session`, `is_session_disconnected`
- Side-effectful setters prefixed with `set_` or `ensure_`: `set_state`, `ensure_on`, `ensure_off`, `ensure_session_connected`

**Variables:**
- `snake_case` for all locals and instance attributes
- Private instance state prefixed with `_`: `_process`, `_hwnd`, `_running`, `_lock`, `_window_cache`
- Module-level constants in `SCREAMING_SNAKE_CASE`: `MAX_EVENT_AGE`, `RETRY_DELAY`, `POWER_SETTLING_TIME`, `HID_READ_TIMEOUT_MS`
- Boolean flags named positively: `_volume_initialized`, `_power_settling`, `cpu_gating_enabled`
- Descriptive variable names throughout (no abbreviations in public APIs)

**Enums:**
- `PascalCase` class names, `SCREAMING_SNAKE_CASE` members: `Action.VOL_UP`, `ControlMode.MOMENTARY`

**Type Aliases:**
- `PascalCase`: `PowerState = Literal["on", "off", "unknown"]`, `GlmAction = Union[...]`

## Code Style

**Formatting:**
- No `.prettierrc`, no `pyproject.toml` formatter config detected
- PEP 8 style followed consistently
- 4-space indentation
- Single blank line between methods, double blank line between top-level definitions
- Horizontal section dividers using `# ===========...` (80 chars) to separate logical groups within files

**Type Annotations:**
- Used consistently on all public method signatures
- `Optional[X]` preferred over `X | None` (Python 3.9 compat style)
- `from __future__ import annotations` used in `glm_manager.py` and `glm_power.py`
- Return types always annotated on public methods
- `Literal` used for constrained string types: `Literal["on", "off"]`, `Literal["DEBUG", "INFO", "NONE"]`

**Linting:**
- No `.flake8`, `.pylintrc`, or `ruff.toml` detected — no enforced linting config

## Import Organization

**Order (observed):**
1. `from __future__ import annotations` (when present)
2. Standard library: `logging`, `os`, `threading`, `time`, `subprocess`, `queue`
3. Third-party: `psutil`, `hid`, `mido`, `fastapi`, `pydantic`
4. Conditional/platform imports wrapped in `try/except ImportError` blocks
5. Local package imports: `from glm_core import ...`, `from config import ...`

**Conditional Import Pattern (Windows-only deps):**
```python
try:
    import psutil
    import ctypes
    from ctypes import wintypes
    HAS_DEPS = True
except ImportError:
    HAS_DEPS = False
    psutil = None

try:
    from pywinauto import Desktop
    HAS_PYWINAUTO = True
except ImportError:
    HAS_PYWINAUTO = False
    Desktop = None
```
All Windows-specific code is guarded by `HAS_DEPS`, `HAS_WIN32_DEPS`, `HAS_PYWINAUTO`, or `IS_WINDOWS` flags set at import time. Methods check these flags at entry and return safe defaults.

**Path Aliases:**
- None. Relative imports used within packages: `from .exceptions import ...`

## Error Handling

**Patterns:**
- Custom exception hierarchy rooted at `GlmPowerError` in `PowerOnOff/exceptions.py`:
  - `GlmWindowNotFoundError` — window lookup failure
  - `GlmStateUnknownError(rgb, point)` — pixel classification failure, carries diagnostic data
  - `GlmStateChangeFailedError(desired, actual)` — retry exhaustion, carries intent vs. result
- Public API methods raise typed exceptions; callers can catch specifically
- Internal/private methods catch `Exception` broadly and log at `debug` or `warning` level, returning safe fallback values
- Windows API calls (`ctypes.windll.*`) always wrapped in `try/except Exception`
- `subprocess` calls catch `subprocess.TimeoutExpired` separately from `Exception`
- IO operations on files (flag file, credential manager) catch `Exception` with `pass` or `logger.warning`

**Return-code convention:**
- Boolean returns (`True`/`False`) for operations that can fail non-fatally: `_start_glm`, `reconnect_to_console`, `_post_minimize`
- `None` returns for optional lookups: `_find_glm_process`, `get_credential_from_manager`
- Raise exceptions for caller-actionable failures in public API

## Logging

**Framework:** Python standard `logging` module with `QueueHandler`/`QueueListener` for async thread-safe delivery (`logging_setup.py`).

**Log Format (centralized):**
```
%(asctime)s [%(levelname)s] %(threadName)s %(module)s:%(funcName)s:%(lineno)d - %(message)s
```
Defined as `LOG_FORMAT` constant in both `logging_setup.py` and `glm_manager.py` (duplication noted in CONCERNS.md).

**Patterns:**
- Each module obtains its own logger: `logger = logging.getLogger(__name__)`
- `GlmPowerController` accepts an injected `logger` parameter (testability)
- `reconnect_to_console` and `ensure_session_connected` accept optional `logger` parameter
- `logger.info` for major state transitions and lifecycle events
- `logger.debug` for per-operation internals (pixel coordinates, handle values, MIDI bytes)
- `logger.warning` for recoverable failures (minimize failed, CPU check error, tscon failed)
- `logger.error` for unrecoverable failures (GLM not found, start failed)
- Long operations use BEGIN/END markers: `"========== GlmManager.start() BEGIN =========="` / `"========== GlmManager.start() END =========="`
- Timing instrumentation in hot paths: `f"[find={t1-t0:.3f}s, focus={t2-t1:.3f}s, read={t3-t2:.3f}s]"`
- `SmartRetryLogger` (`retry_logger.py`) throttles repetitive retry messages with exponential milestones (2s, 10s, 1m, 10m, 1h, 1d)
- `WebSocketErrorFilter` suppresses expected WebSocket disconnect noise

**Rotation:**
- `RotatingFileHandler` with 4 MB max, 5 backup files
- Log thread runs as non-daemon (`daemon=False`) to flush on shutdown

## Comments

**When to Comment:**
- Module-level docstrings on every file explaining purpose, requirements, and usage example
- Class docstrings explain thread safety contracts (e.g., `GlmPowerController` documents lock semantics)
- Method docstrings on all public methods with `Args:`, `Returns:`, `Raises:` sections
- Inline comments on non-obvious Win32 behavior: `# Alt key trick to allow SetForegroundWindow to work`, `# SW_MINIMIZE (6) minimizes the window`
- Constants annotated with unit and purpose: `cpu_threshold: float = 10.0  # % CPU considered "idle enough"`
- `NOTE:` comments mark intentional design decisions to prevent well-meaning regressions

**Docstring style:** Google-style (Args/Returns/Raises sections).

## Function Design

**Size:** Methods tend to be focused; complex orchestration methods (`set_state`, `_watchdog_loop`) are ~60-80 lines with clear phase comments.

**Parameters:**
- Config objects (`GlmPowerConfig`, `GlmManagerConfig`) used to avoid long parameter lists
- Optional parameters default to `None` with `Optional[X]` annotation
- Callbacks injected as `Optional[Callable[...]]` for testability

**Return Values:**
- Documented in docstrings
- Tuples used for multi-value returns: `(state, rgb, point)`, `(allowed, wait_time, reason)`
- Named tuple alternatives not used — plain tuples with documented semantics

## Module Design

**Exports:**
- `__init__.py` files define public surface: `PowerOnOff/__init__.py` re-exports `GlmPowerController`, `GlmManager`, `GlmManagerConfig`, availability flags
- `glm_core/__init__.py` re-exports action dataclasses

**Dataclasses:**
- `frozen=True` on immutable value objects: `SetVolume`, `AdjustVolume`, `SetMute`, `SetDim`, `SetPower`, `Point`, `GlmControl`
- Mutable state holders use plain `@dataclass`: `WindowState`, `QueuedAction`
- Config objects use `@dataclass` (mutable, intentionally): `GlmManagerConfig`, `GlmPowerConfig`

**Singleton pattern:**
- `glm_controller = GlmController()` at module level in `bridge2glm.py` — global instance
- `retry_logger = SmartRetryLogger()` at module level in `retry_logger.py` — global instance

## Dual Perspective Analysis

### Senior Developer View

**Strengths:**
- Consistent naming and docstring coverage is high
- Custom exception hierarchy with diagnostic payload (rgb, point) is well-designed
- `SmartRetryLogger` is a reusable, well-abstracted utility
- Frozen dataclasses for action types enforce immutability correctly
- Dependency injection (`logger`, `config`, `reinit_callback`) makes key classes testable in isolation

**Issues:**
- `LOG_FORMAT` string is duplicated in `logging_setup.py` (line 19) and `glm_manager.py` (line 38) — violates DRY
- `glm_controller` global in `bridge2glm.py` makes unit testing harder; no dependency injection at the bridge level
- `bridge2glm.py` exceeds 500 lines and contains multiple responsibilities (HID reader, MIDI consumer, RDP priming, thread management) — violates Single Responsibility
- `get_display_diagnostics` has inline `import os` inside function body (lines 287-288) — inconsistent with top-level imports elsewhere
- `reconnect_to_console` also has inline `import subprocess`, `import shutil`, `import time` inside function body
- `can_accept_command` and `can_accept_power_command` in `GlmController` duplicate the time-elapsed logic with slightly different thresholds — DRY violation
- No `pyproject.toml` or `setup.py` — project is not installable as a package

### Senior Windows Desktop App Architect View

**Strengths:**
- `ctypes.windll.user32.IsHungAppWindow(hwnd)` used correctly for hung-window detection (correct Win32 API, avoids SendMessage timeout hack)
- `ctypes.windll.kernel32.WTSGetActiveConsoleSessionId()` and `ProcessIdToSessionId` used correctly for session identity
- `WTSEnumerateSessionsW` with proper `WTS_SESSION_INFO` ctypes structure and `WTSFreeMemory` cleanup in `finally` block — correct resource management
- `ShowWindow(hwnd, SW_MINIMIZE)` with `IsIconic` verification, then `PostMessageW(WM_SYSCOMMAND, SC_MINIMIZE)` fallback — correct escalation for JUCE apps
- `ctypes.WINFUNCTYPE` used correctly for `EnumDisplayMonitors` callback — correct calling convention for Win32 callbacks
- Alt-key trick before `SetForegroundWindow` is the correct workaround for `FOREGROUNDLOCKTIMEOUT` restriction
- `psutil.ABOVE_NORMAL_PRIORITY_CLASS` used via psutil wrapper (correct constant, avoids raw `SetPriorityClass`)
- `subprocess.CREATE_NO_WINDOW` flag suppresses console windows from child processes correctly
- pywinauto `backend="win32"` specified explicitly (correct for non-UIA apps like JUCE)

**Issues:**
- No HRESULT checking on `ctypes.windll` calls — `PostMessageW`, `SetForegroundWindow`, `ShowWindow` return values are sometimes ignored or only checked implicitly. Win32 functions return 0 on failure; `GetLastError()` is never called to get the actual Win32 error code.
- `WTS_SESSION_INFO` ctypes struct defines `pWinStationName` as `wintypes.LPWSTR` — this is technically correct but the field is never used; no memory lifetime concern here, but worth documenting.
- No COM initialization (`CoInitializeEx`) before pywinauto usage — `Desktop(backend="win32")` internally uses COM; if called from a thread not initialized as STA/MTA, behavior is undefined. Pywinauto handles this internally, but there is no explicit apartment model documentation or assertion.
- `ImageGrab.grab(all_screens=True)` (Pillow) requires the process to be in the active console session with a valid display context. No guard checks `is_console_session()` before calling it — a disconnect during `get_state()` could silently return wrong pixel data.
- `win32api.SetCursorPos` / `win32api.mouse_event` use the legacy `mouse_event` API (deprecated since Windows 2000) rather than `SendInput` — `SendInput` is the correct modern API and respects `UIPI` (User Interface Privilege Isolation) correctly.
- `keybd_event` (VK_ESCAPE, VK_MENU) also uses legacy API — should use `SendInput` with `INPUT` structures.
- No Windows Event Log integration — significant errors (GLM crash, session disconnect) only go to file log; Windows Event Log (`win32evtlog`) would allow monitoring via standard Windows tools.
