# Testing Patterns

**Analysis Date:** 2026-03-21

## Test Framework

**Runner:** None — no test framework is installed or configured.

**Test Files:** Zero test files found. No `test_*.py`, `*_test.py`, `pytest.ini`, `setup.cfg [tool:pytest]`, `pyproject.toml [pytest]`, `unittest` discovery, or `tox.ini` detected anywhere in the repository.

**Coverage:** Not measured. No `.coveragerc` or coverage configuration present.

**Run Commands:**
```bash
# Not applicable — no tests exist
```

## Test File Organization

**Location:** Not established.

**Naming:** Not established.

**Recommended structure (to be created):**
```
tests/
├── unit/
│   ├── test_acceleration.py
│   ├── test_retry_logger.py
│   ├── test_midi_constants.py
│   ├── test_glm_controller.py        # GlmController state machine
│   ├── test_config.py                # Argument validation functions
│   └── PowerOnOff/
│       ├── test_glm_power.py         # GlmPowerController (mocked Win32)
│       └── test_glm_manager.py       # GlmManager (mocked psutil/ctypes)
└── integration/
    └── (manual / hardware-in-loop only)
```

## Testability Assessment

### Currently Testable (no Windows/hardware required)

**`acceleration.py` — `AccelerationHandler`:**
- Pure Python, no external dependencies
- `calculate_speed(current_time, button)` is a pure function of time and state
- Can be tested with synthetic timestamps to verify acceleration curve behavior
- Example: feed rapid clicks and assert higher `delta` values; feed slow clicks and assert `delta == 1`

**`retry_logger.py` — `SmartRetryLogger`:**
- Pure Python, no external dependencies
- `should_log(key)` behavior is fully deterministic given mocked `time.time()`
- Test milestone schedule: first call returns True, subsequent calls within 2s return False, call at t=2s returns True, etc.
- `format_retry_info(key)` is a pure formatting function

**`config.py` — validation functions:**
- `validate_volume_increases(value)`, `validate_click_times(values)`, `validate_device(value)` are pure functions
- Test valid inputs, boundary values, and invalid inputs that should raise `argparse.ArgumentTypeError`

**`midi_constants.py`:**
- Constants and enum definitions — import-time tests verify structure
- `log_midi(logger, ...)` can be tested with a mock logger

**`glm_core/actions.py`:**
- Frozen dataclasses — construction and immutability can be verified trivially

**`GlmController` (in `bridge2glm.py`):**
- Thread-safe state machine with no direct Windows dependencies
- `update_from_midi(cc, value)` is testable with mock MIDI values
- `can_accept_command()` / `can_accept_power_command()` timing logic testable with mocked `time.time()`
- `get_state()` returns a plain dict — assertable without mocking
- `start_power_transition()` / `end_power_transition()` state transitions fully testable

### Requires Mocking (Windows API surface)

**`PowerOnOff/glm_power.py` — `GlmPowerController`:**
The following must be mocked for unit testing:
- `Desktop(backend="win32").windows(class_name_re=...)` — pywinauto window enumeration
- `ImageGrab.grab(bbox=..., all_screens=True)` — Pillow screen capture
- `win32api.SetCursorPos`, `win32api.mouse_event` — cursor/mouse
- `win32api.keybd_event` — keyboard simulation
- `ctypes.windll.user32.IsIconic`, `IsWindow`, `IsHungAppWindow`, `SetForegroundWindow`, `GetForegroundWindow` — Win32 window state
- `ctypes.windll.kernel32.ProcessIdToSessionId`, `WTSGetActiveConsoleSessionId` — session API
- `ctypes.windll.wtsapi32.WTSEnumerateSessionsW`, `WTSFreeMemory` — session enumeration

Key testable logic after mocking:
- `_classify_state(rgb)` — pure RGB → `"on"/"off"/"unknown"` classification, no mocking needed
- `_find_window(use_cache=...)` — cache TTL expiry logic
- `set_state` retry loop — mock `_read_state_internal` to return controlled sequences
- `_capture_window_state` / `_restore_window_state` — focus/minimize save-restore logic

**`PowerOnOff/glm_manager.py` — `GlmManager`:**
Must mock:
- `psutil.process_iter()`, `psutil.Process`, `psutil.cpu_percent()` — process/CPU queries
- `ctypes.windll.user32.IsWindow`, `IsHungAppWindow`, `IsIconic`, `ShowWindow`, `PostMessageW` — window API
- `Desktop(backend="win32").windows(...)` — pywinauto
- `subprocess.Popen` — process launch

Key testable logic after mocking:
- `_wait_for_cpu_calm()` — loop termination when CPU drops below threshold
- `_wait_for_window_stable()` — stable-handle-count logic
- `_watchdog_loop()` — non-responsive counter, kill-and-restart sequencing
- `reinit_callback` invocation after restart

## Mocking

**Recommended framework:** `pytest` with `unittest.mock` (`MagicMock`, `patch`)

**Patterns for Windows API mocking:**
```python
from unittest.mock import patch, MagicMock

# Mock ctypes windll calls
@patch('PowerOnOff.glm_power.ctypes.windll.user32')
def test_is_responding(mock_user32):
    mock_user32.IsWindow.return_value = 1
    mock_user32.IsHungAppWindow.return_value = 0
    # ... test logic

# Mock pywinauto Desktop
@patch('PowerOnOff.glm_power.Desktop')
def test_find_window(mock_desktop):
    mock_win = MagicMock()
    mock_win.window_text.return_value = "GLM v5"
    mock_win.process_id.return_value = 1234
    mock_desktop.return_value.windows.return_value = [mock_win]
    # ... test logic

# Mock ImageGrab for pixel tests
@patch('PowerOnOff.glm_power.ImageGrab.grab')
def test_classify_state_on(mock_grab):
    mock_img = MagicMock()
    mock_img.getdata.return_value = [(50, 180, 90)] * 81  # Green = ON
    mock_grab.return_value = mock_img
    # ... test logic
```

**`_classify_state` can be tested without any mocking:**
```python
from PowerOnOff.glm_power import GlmPowerController, GlmPowerConfig

def test_classify_off():
    controller = GlmPowerController.__new__(GlmPowerController)
    controller.config = GlmPowerConfig()
    assert controller._classify_state((80, 75, 78)) == "off"

def test_classify_on():
    controller = GlmPowerController.__new__(GlmPowerController)
    controller.config = GlmPowerConfig()
    assert controller._classify_state((50, 180, 90)) == "on"

def test_classify_unknown():
    controller = GlmPowerController.__new__(GlmPowerController)
    controller.config = GlmPowerConfig()
    assert controller._classify_state((200, 150, 10)) == "unknown"
```

**What to Mock:**
- All `ctypes.windll.*` calls
- All `psutil` process iteration and CPU queries
- `subprocess.Popen` and `subprocess.run`
- `Desktop(backend="win32")` and all pywinauto window methods
- `ImageGrab.grab`
- `win32api.*` (cursor, mouse, keyboard)
- `time.sleep` (to speed up tests)
- `time.time` (to control timing-dependent logic)

**What NOT to Mock:**
- `GlmPowerConfig` and `GlmManagerConfig` dataclasses — use real instances
- `GlmController` state machine — pure Python, test directly
- `AccelerationHandler` — pure Python, test directly
- `SmartRetryLogger` — pure Python, test directly
- `_classify_state` — pure function, test directly

## Fixtures and Factories

**Test Data (recommended):**
```python
# tests/conftest.py
import pytest
from PowerOnOff.glm_power import GlmPowerConfig
from PowerOnOff.glm_manager import GlmManagerConfig

@pytest.fixture
def default_power_config():
    return GlmPowerConfig()

@pytest.fixture
def fast_watchdog_config():
    return GlmManagerConfig(
        watchdog_interval=0.1,
        max_non_responsive=2,
        restart_delay=0.1,
        post_start_sleep=0.0,
        enforce_max_seconds=1.0,
    )
```

**Location:** `tests/conftest.py` (not yet created)

## Coverage

**Requirements:** None enforced.

**Recommended targets:**
- `acceleration.py`: 100% (pure logic)
- `retry_logger.py`: 100% (pure logic)
- `config.py` validation functions: 100% (pure logic)
- `PowerOnOff/exceptions.py`: 100% (trivial)
- `glm_core/actions.py`: 100% (trivial)
- `GlmController` state machine methods: >90%
- `_classify_state`: 100%
- `GlmPowerController` (mocked): >80%
- `GlmManager` (mocked): >70%

## Test Types

**Unit Tests:**
- Scope: Individual classes and pure functions with all external dependencies mocked
- Should cover all state transitions in `GlmController`, all classification branches in `_classify_state`, all acceleration curve levels in `AccelerationHandler`, all retry milestone transitions in `SmartRetryLogger`

**Integration Tests:**
- Scope: Interaction between `GlmController` and `GlmPowerController` (mocked Win32 surface)
- Power transition flow: `start_power_transition` → `set_state` → `end_power_transition` → state callback
- MIDI round-trip: send CC → `update_from_midi` → `get_state` reflects update

**E2E Tests:**
- Not applicable — requires physical VOL20 hardware, MIDI loopback, and running GLM on Windows
- Manual testing only on target Windows host

## Common Patterns

**Async Testing (REST API):**
```python
# api/rest.py uses FastAPI — use httpx AsyncClient
import pytest
from httpx import AsyncClient
from api.rest import create_app

@pytest.mark.asyncio
async def test_get_state():
    app = create_app(action_queue=mock_queue, glm_controller=mock_controller)
    async with AsyncClient(app=app, base_url="http://test") as client:
        response = await client.get("/api/state")
    assert response.status_code == 200
```

**Timing-Sensitive Testing:**
```python
# Use unittest.mock.patch for time.time to control elapsed time
from unittest.mock import patch
import time

def test_power_cooldown_expires():
    controller = GlmController()
    controller.start_power_transition(True)

    with patch('bridge2glm.time') as mock_time:
        mock_time.time.return_value = time.time() + 6.0  # Past POWER_TOTAL_LOCKOUT
        allowed, wait, reason = controller.can_accept_power_command()
    assert allowed is True
```

**Error Testing:**
```python
# GlmWindowNotFoundError
from PowerOnOff.exceptions import GlmWindowNotFoundError
import pytest

@patch('PowerOnOff.glm_power.Desktop')
def test_window_not_found(mock_desktop):
    mock_desktop.return_value.windows.return_value = []
    controller = GlmPowerController.__new__(GlmPowerController)
    # ... setup
    with pytest.raises(GlmWindowNotFoundError):
        controller.get_state()
```

## Dual Perspective Analysis

### Senior Developer View

**Critical gap:** Zero test coverage is the single largest quality risk in this codebase. The business logic is non-trivial (power state machine, retry timing, acceleration curves, MIDI pattern detection) and all of it is untested.

**Immediately writable without any mocking (high ROI, low effort):**
- `AccelerationHandler.calculate_speed` — feeds synthetic timestamps
- `SmartRetryLogger.should_log` — feeds mocked `time.time`
- `validate_volume_increases`, `validate_click_times`, `validate_device` in `config.py`
- `GlmController.update_from_midi`, `get_state`, `can_accept_command`, `can_accept_power_command`
- `_classify_state` in `GlmPowerController`

**The `bridge2glm.py` god-module problem:** `GlmController` is defined inside `bridge2glm.py` rather than in `glm_core/`. This makes it harder to import and test in isolation without triggering all of `bridge2glm.py`'s module-level side effects (global instantiation, import of `hid`, platform checks). Recommend moving `GlmController` to `glm_core/controller.py`.

### Senior Windows Desktop App Architect View

**Windows-specific testing challenges:**

1. **Session/display context:** `GlmPowerController` requires the process to be in the active console session (session 0 or the physical console session). CI pipelines running in headless Windows Server environments will fail `ImageGrab.grab` — all pixel-sampling tests must mock `ImageGrab`. Document this constraint explicitly in test setup.

2. **UI Automation isolation:** pywinauto's `Desktop(backend="win32")` enumerates real windows on the test machine. Tests must always mock `Desktop` to avoid finding actual GLM windows (or any JUCE app) during test runs.

3. **ctypes calling conventions:** When mocking `ctypes.windll.user32.IsHungAppWindow`, the mock must return an integer (not bool) because the real function returns `BOOL` (int). Use `return_value = 0` not `return_value = False` to match Win32 semantics.

4. **Thread apartment model:** `GlmManager._watchdog_loop` and `GlmPowerController` methods that call pywinauto are invoked from background threads. In a real Windows process, COM must be initialized per-thread (STA for UI threads). Tests that call these from `pytest`'s main thread may work differently than production. Document that `pytest-asyncio` or thread-based test fixtures should call `CoInitializeEx` or rely on pywinauto's internal COM init.

5. **`wtsapi32.dll` on test machines:** `WTSEnumerateSessionsW` may not be available in all Windows SKUs (e.g., Windows Home). Mock at the `ctypes.windll.wtsapi32` level, not at the Python function level, to catch import-time availability issues.
