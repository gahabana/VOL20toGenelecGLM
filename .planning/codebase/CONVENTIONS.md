# Coding Conventions

**Analysis Date:** 2026-03-21

## Dual Perspective Analysis

### Senior Developer Perspective
This codebase demonstrates strong adherence to Python best practices with comprehensive docstrings, clear separation of concerns, and explicit error handling via custom exceptions. The use of dataclasses for domain models, Pydantic for API validation, and enums for constants shows modern Python idioms. Threading is carefully managed with locks and event synchronization. Code organization around logical domains (actions, power control, MIDI) promotes maintainability.

### macOS App Architect Perspective
From a system integration standpoint, the approach differs significantly from native Cocoa/AppKit patterns. Instead of leveraging NSThread or GCD (Grand Central Dispatch), the code uses Python's threading module directly. macOS-native approaches would use Objective-C frameworks for system integration, delegation patterns, and run loop integration. However, the Windows-focused architecture (pywinauto, ctypes, win32 APIs) and Python's cross-platform nature make the current approach pragmatic for this Windows desktop bridge application. The explicit state management (via `GlmController`) mirrors AppKit's Model-View-Controller separation, though without platform-native persistence or lifecycle hooks.

**Convergence:** Both perspectives agree on explicit state management, comprehensive error handling, and clear function/class responsibilities.

**Divergence:** macOS perspective would prefer native system frameworks; the developer perspective focuses on Python-idiomatic solutions that work cross-platform.

---

## Naming Patterns

### Files
- **Module files:** snake_case (e.g., `glm_manager.py`, `retry_logger.py`, `glm_power.py`)
- **Package directories:** snake_case (e.g., `PowerOnOff/`, `api/`, `glm_core/`)
- **Constants files:** descriptive snake_case (e.g., `midi_constants.py`)

Examples: `bridge2glm.py` (main entry), `config.py` (configuration parsing), `logging_setup.py` (logging initialization)

### Functions and Methods
- **Public methods:** snake_case, descriptive verb phrases
  - `_wait_for_cpu_calm()` - clarifies the wait purpose
  - `_get_main_window_handle()` - specific about what's retrieved
  - `ensure_on()`, `ensure_off()` - imperative action names
- **Private methods:** Leading underscore + snake_case (e.g., `_start_glm()`, `_find_glm_process()`)
- **Callback/handler methods:** Descriptive suffix (_callback, _handler, _thread)
  - `reinit_callback` - called after GLM restart
  - `log_listener_thread()` - logging thread target
  - `_watchdog_loop()` - background monitoring loop

### Variables
- **Class-level state:** Descriptive, use leading underscore for private (e.g., `_process`, `_hwnd`, `_lock`, `_running`)
- **Local variables:** Concise, context-clear names
  - `hwnd` - Windows window handle (domain-specific abbreviation, acceptable given platform context)
  - `proc` - process object (common abbreviation)
  - `elapsed` - time measurements
  - `parsed` - after parsing input
- **Configuration/constants:** UPPER_CASE or camelCase
  - `MAX_EVENT_AGE`, `RETRY_DELAY`, `HID_READ_TIMEOUT_MS` (global timing constants)
  - `cpu_threshold`, `watchdog_interval` (dataclass config fields use snake_case)

### Types and Classes
- **Exception classes:** PascalCase + "Error" suffix
  - `GlmPowerError` - base exception
  - `GlmWindowNotFoundError` - specific failure case
  - `GlmStateUnknownError` - state determination failed
- **Data classes:** PascalCase
  - `GlmManagerConfig` - immutable configuration container
  - `SetVolume`, `AdjustVolume`, `SetMute`, `SetDim`, `SetPower` - domain actions (frozen dataclasses)
- **Enums:** PascalCase class, UPPER_CASE members
  - `Action.VOL_UP`, `Action.POWER` (logical system actions)
  - `ControlMode.MOMENTARY`, `ControlMode.TOGGLE` (control behavior modes)
- **Main classes:** PascalCase, domain-specific prefixes
  - `GlmManager` - manages GLM process lifecycle
  - `GlmController` - tracks GLM state
  - `GlmPowerController` - UI automation for power control
  - `SmartRetryLogger` - intelligent retry logging

---

## Code Style

### Formatting
- **Tool:** No explicit formatter configured (no `.prettierrc`, `black`, or `autopep8` config found)
- **De facto style:** PEP 8 compliant with 4-space indentation
- **Line length:** ~100-120 characters (implicit, no configuration file)
- **Imports:**
  - Standard library first
  - Third-party packages next (typing, dataclasses, threading, etc.)
  - Platform-conditional imports wrapped in try/except blocks
  - Relative imports within package (e.g., `from PowerOnOff import ...`)

### Module Structure
- **Docstrings:** Module-level docstring (PEP 257 style) at the top
  ```python
  """GLM Manager - Process management and watchdog for Genelec GLM application.

  Replaces the functionality of minimize-glm.newer.ps1 PowerShell script:
  - CPU gating before startup
  - Start GLM with AboveNormal priority
  - ...
  """
  ```
- **Logging setup:** Module-level logger created immediately
  ```python
  logger = logging.getLogger(__name__)
  ```
- **Conditional dependencies:** Try/except import blocks with fallback flags
  ```python
  try:
      import psutil
      HAS_DEPS = True
  except ImportError:
      HAS_DEPS = False
      psutil = None
  ```

---

## Linting and Type Hints

### Type Hints
- **Usage:** Comprehensive type hints on function signatures and return types
- **Pattern:**
  ```python
  def validate_click_times(values: str) -> Tuple[float, float]:
      """Function description."""
  ```
- **Complex types:** Imported from `typing` module
  - `Optional[T]` for nullable values
  - `Union[T1, T2, ...]` for discriminated types
  - `Callable[[ArgTypes], ReturnType]` for callbacks
  - `Dict[K, V]`, `List[T]`, `Tuple[T, ...]` for collections
- **No Mypy/Pyright config found** - types are for developer clarity, not enforced at build time

### Comments and Docstrings
- **Docstrings:** Triple-quoted PEP 257 style, present on classes and most public functions
- **Docstring format:**
  ```python
  def start_power_transition(self, target_state: bool):
      """Mark the start of a power transition.

      Called when power command is initiated. Blocks all commands during settling.
      """
  ```
- **Parameter documentation:** Args/Returns format in docstrings where complex
- **Inline comments:** Minimal; used only for non-obvious logic
  - Examples: `# Cached process is dead, clear caches`, `# Auto-end settling if timeout`
- **Section comments:** Single-line comments marking logical sections
  ```python
  # ==============================================================================
  # POWER TRANSITION MANAGEMENT
  # ==============================================================================
  ```

---

## Import Organization

### Order
1. **Python standard library** (logging, os, sys, threading, time, typing, etc.)
2. **Third-party packages** (hid, mido, psutil, fastapi, pydantic, etc.)
3. **Conditional platform imports** (wrapped in try/except, pywinauto, win32api, ctypes)
4. **Local relative imports** (from glm_core, from config, from PowerOnOff)

### Example from `bridge2glm.py`
```python
import time
import signal
import sys
import os
import threading
import queue
from queue import Queue
from typing import Dict, Optional, List, Callable
import hid

from glm_core import SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction
from mido import Message, open_output, open_input

from config import parse_arguments
from retry_logger import retry_logger
from midi_constants import (...)
```

### Path Aliases
- No import path aliases configured (no `sys.path` manipulation or `__init__.py` path setup)
- Relative imports use package structure directly

---

## Error Handling

### Exception Hierarchy
**Base exception:** `GlmPowerError` (in `PowerOnOff/exceptions.py`)
- Allows catching all GLM-related errors with a single except clause
- Domain-specific, not using generic `Exception`

**Specific exceptions:**
- `GlmWindowNotFoundError` - UI automation target not found (Windows-specific)
- `GlmStateUnknownError` - Pixel sampling failed to determine power state
  - Carries context: `rgb` (sampled color), `point` (pixel location)
- `GlmStateChangeFailedError` - Power state change failed after retries
  - Carries context: `desired` state, `actual` state observed

### Error Handling Patterns

**Pattern 1: Try/Except for Resource Cleanup**
```python
try:
    # Acquire resource / perform operation
    listener = QueueListener(log_queue, file_handler, console_handler)
    listener.start()
except Exception as e:
    logger.error(f"Failed to start listener: {e}")
    return False
finally:
    # Cleanup if needed
```

**Pattern 2: Graceful Degradation with Conditional Dependencies**
```python
try:
    import psutil
    HAS_DEPS = True
except ImportError:
    HAS_DEPS = False
    psutil = None

# Later:
if not HAS_DEPS:
    logger.error("GLM Manager requires psutil (Windows only)")
    return False
```

**Pattern 3: Retry with Smart Logging**
```python
while self._running:
    try:
        if not self.is_alive():
            logger.warning("Watchdog: GLM process not found. Restarting.")
            self._restart_glm()
            continue
    except Exception as e:
        logger.error(f"Watchdog loop error: {e}")

    time.sleep(self.config.watchdog_interval)
```

**Pattern 4: Return Tuples for Multi-value Status**
```python
def can_accept_command(self) -> tuple:
    """Check if any command can be accepted.

    Returns (allowed, wait_time, reason).
    """
    if not self._power_settling:
        return True, 0, None

    elapsed = time.time() - self._power_transition_start
    if elapsed < POWER_SETTLING_TIME:
        wait = POWER_SETTLING_TIME - elapsed
        return False, wait, "power_settling"

    return True, 0, None
```

### Assertion Policy
- **Not used** for production error handling
- Error conditions are explicitly checked and exceptions/returns used

---

## Logging

### Framework
- **Standard library:** `logging` module (no external logging framework)
- **Format:** Centralized format string with thread, module, function, line number
  ```python
  LOG_FORMAT = '%(asctime)s [%(levelname)s] %(threadName)s %(module)s:%(funcName)s:%(lineno)d - %(message)s'
  ```
- **Setup:** `logging_setup.py` provides `setup_logging()` function with rotating file handler

### Logging Patterns

**Pattern 1: Informational Milestones**
```python
logger.info("========== GlmManager.start() BEGIN ==========")
logger.info(f"Watchdog thread started")
logger.info("========== GlmManager.start() END ==========")
```

**Pattern 2: Warning with Context**
```python
logger.warning(
    f"Watchdog: GLM NOT responding. "
    f"Streak={self._non_responsive_count}/{self.config.max_non_responsive}."
)
```

**Pattern 3: Debug for Verbose Details**
```python
logger.debug(f"Found GLM main window: PID={self._process.pid} Handle={hwnd}")
logger.debug(f"Error finding GLM window: {e}")
```

**Pattern 4: Smart Retry Logging (Throttled)**
```python
# Use SmartRetryLogger to avoid log spam
if retry_logger.should_log("hid_connect"):
    logger.warning(f"HID connection failed {retry_logger.format_retry_info('hid_connect')}")
```

### Log Levels
- **DEBUG:** Detailed information (window handle found, config values, intermediate states)
- **INFO:** Significant milestones (process started, state transitions, initialization complete)
- **WARNING:** Recoverable issues (connection retries, timeouts, non-responsive processes)
- **ERROR:** Failures that affect functionality (missing executable, process crash)
- **CRITICAL:** System-level failures (not explicitly used in codebase)

### Thread-Safe Logging
- Uses `QueueHandler` and `QueueListener` for thread-safe async logging
- All threads (HID reader, MIDI reader, watchdog, etc.) can safely call `logger.info()` etc.
- No custom thread-local state needed; Python logging handles it

---

## Comments

### When to Comment
- **Why, not what:** Comments explain intent, not repeat code
  - Bad: `count = count + 1  # Increment count`
  - Good: `# Reset non-responsive streak (process recovered)`
- **Non-obvious logic:** Timing windows, retry logic, pixel sampling thresholds
  - Example: `# Cached handle is still valid` (explains why we use cached value)
- **Gotchas and platform-specific behavior:**
  - Example: `# Note: Don't minimize here - let caller do it after reinit_callback`
  - Example: `# ShowWindow returns previous visibility state, not success/failure`
- **Section headers:** Separate logical groups with comment blocks

### Comment Style
- Single-line comments use `#` with space
- Multiple-line comments use `"""` docstrings (for module/class/function docs, not inline)
- Avoid obsolete/dead code comments; delete the code instead

### JSDoc/Docstring Format
- **Module:** Module-level docstring at top describing purpose, requirements, usage
- **Classes:** Single-line description + detailed docstring if complex
- **Public methods/functions:**
  - Summary line
  - Longer description if needed
  - Args section with types and meanings
  - Returns section with return type and meaning
  - Raises section if exceptions are documented

Example:
```python
def _wait_for_window_stable(self) -> int:
    """
    Wait for GLM main window handle to stabilize.

    GLM's window handle can change during startup (splash screen → main window).
    This method polls until the handle is stable, confirming GLM has fully started.

    Returns:
        The stable window handle, or 0 if stabilization failed/timed out.
    """
```

---

## Function Design

### Size Guidelines
- **Typical range:** 20-50 lines (methods)
- **Maximum:** ~100 lines before considering split (e.g., `_wait_for_window_stable`)
- **Large methods:** Used when coherent logic (not arbitrary line split)
  - Example: `_start_glm()` does sequential startup steps without useful intermediate boundaries

### Parameter Patterns
- **Positional:** Limited to required parameters (usually 1-2)
- **Keyword-only:** Configuration or optional parameters
  - Example: `block_until_ready: bool = True` in `start()`
- **Callback parameters:** Optional, nullable
  - Example: `reinit_callback: Optional[Callable[[int], None]] = None`
- **Return values:**
  - Simple success/failure: `bool`
  - Multi-value status: `tuple` (allowed, used, e.g., `can_accept_command()`)
  - Complex results: Dataclass or `dict` (API responses use Pydantic models)

### Function Naming Clarity
- **Action verbs:** `start()`, `stop()`, `ensure_on()`, `minimize()`
- **Query verbs:** `is_alive()`, `is_responding()`, `can_accept_command()`
- **Internal state changes:** `_restart_glm()`, `_kill_glm()`, `_set_priority()`
- **Callbacks/handlers:** Suffix with purpose (e.g., `reinit_callback`, `log_listener_thread`)

---

## Module Design

### Exports
- **Convention:** Package `__init__.py` files explicitly export public classes/functions
- **Example:** `PowerOnOff/__init__.py` exports:
  ```python
  from .glm_power import GlmPowerController
  from .glm_manager import GlmManager, GlmManagerConfig
  from .exceptions import GlmPowerError, GlmWindowNotFoundError, GlmStateUnknownError, GlmStateChangeFailedError
  ```
- **Benefit:** Clear public API, allows internal reorganization

### Barrel Files
- **Used:** `PowerOnOff/__init__.py`, `api/__init__.py`, `glm_core/__init__.py`
- **Purpose:** Centralize imports for package-level API
- **Pattern:** Import and re-export from submodules

### Domain Separation
- **`glm_core/`:** Pure domain actions (SetVolume, SetMute, etc.) - independent of UI/API
- **`PowerOnOff/`:** Windows-specific process/power management (GlmManager, GlmPowerController)
- **`api/`:** HTTP/WebSocket/MQTT interfaces (FastAPI, Pydantic models)
- **Root level:** Entry point (`bridge2glm.py`), configuration (`config.py`), logging setup

### Thread Safety
- **Pattern:** Private `_lock = threading.Lock()` for shared mutable state
- **Usage:** Explicit `with self._lock:` blocks protecting state reads and writes
- **Example:**
  ```python
  def can_accept_command(self) -> tuple:
      with self._lock:
          if not self._power_settling:
              return True, 0, None
          # ...
  ```

### Constants
- **Global constants:** Module-level, UPPER_CASE
  - Timing: `MAX_EVENT_AGE`, `RETRY_DELAY`, `POWER_SETTLING_TIME`
  - Hardware: `HID_READ_TIMEOUT_MS`, `QUEUE_MAX_SIZE`
  - MIDI: `GLM_VOLUME_ABS`, `GLM_MUTE_CC` (in `midi_constants.py`)
- **Configuration constants:** Dataclass fields, camelCase/snake_case
  - Example: `GlmManagerConfig.cpu_threshold`, `watchdog_interval`

---

## SOLID Principles and DRY

### Single Responsibility
- **`GlmController`:** Tracks GLM state and notifies callbacks (single responsibility: state management)
- **`GlmManager`:** Process lifecycle and watchdog (single responsibility: process management)
- **`GlmPowerController`:** UI automation for power (single responsibility: power state verification)
- **Separation:** Each class has one reason to change

### Open/Closed
- **Configuration via dataclass:** `GlmManagerConfig` allows extensibility without modifying class
- **Callbacks:** `reinit_callback` pattern allows external behavior injection
- **State callbacks:** `add_state_callback()` allows observers without coupling

### Liskov Substitution
- Not heavily used (limited inheritance hierarchy)
- Custom exceptions inherit from `GlmPowerError`, allowing unified error handling

### Interface Segregation
- **Small interfaces:** Each class exposes minimal public API
  - `GlmManager.start()`, `stop()`, `is_alive()`, `is_responding()`
  - Clients don't depend on internal details

### Dependency Inversion
- **Dependency injection:** Configuration passed to constructors
  - Example: `GlmManager(config=GlmManagerConfig(...))`
- **Optional dependencies:** Callbacks are optional, graceful degradation if absent
  - Example: `reinit_callback` only called if provided

### DRY (Don't Repeat Yourself)
- **Validation functions:** `validate_volume_increases()`, `validate_click_times()`, `validate_device()` centralized in `config.py`
- **Constants:** All MIDI mappings in `midi_constants.py` (single source of truth)
- **Logging format:** Centralized in `LOG_FORMAT` constant (reused across all handlers)
- **Retry logic:** `SmartRetryLogger` encapsulates exponential backoff (shared by all retry loops)

---

## Testability

### Design for Testing
- **Dependency injection:** Configuration passed to constructors
- **Optional dependencies:** Can disable features for testing (HAS_DEPS flags)
- **Separable concerns:**
  - State management (`GlmController`) separate from process management (`GlmManager`)
  - Validation logic (`config.py` functions) separate from parsing
- **Callbacks:** Allow test code to inject custom behavior

### Challenges (Current State)
- **Windows-specific:** Process management, UI automation heavily tied to Windows APIs
- **External dependencies:** HID device, MIDI ports, network APIs not mocked
- **No test framework found:** No pytest, unittest, or equivalent configured
- **No integration tests:** Manual testing appears to be primary approach

---

## Dual Perspective Summary

| Aspect | Senior Developer | macOS Architect |
|--------|-----------------|------------------|
| **Type hints** | Good practice, enforced | Helpful but not native Objective-C pattern |
| **Threading** | Python threading with locks | Would use GCD, NSThread, or async/await |
| **Error handling** | Custom exception hierarchy | Native NSError pattern with error codes |
| **State management** | Explicit with callbacks | KVO (Key-Value Observing) or Combine |
| **Configuration** | Dataclasses, argparse | NSUserDefaults, property lists |
| **Logging** | Rotating file handler, thread-safe | OSLog framework with privacy controls |
| **Platforms** | Windows-focused pragmatism | Would be Cocoa/AppKit native |

**Key alignment:** Both value explicit state management, clear separation of concerns, and comprehensive error handling.

**Key difference:** The developer perspective optimizes for Python cross-platform code; the macOS perspective would leverage native frameworks for system integration, lifecycle management, and platform-idiomatic patterns.

---

*Convention analysis: 2026-03-21*
