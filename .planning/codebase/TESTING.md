# Testing Patterns

**Analysis Date:** 2026-03-21

## Dual Perspective Analysis

### Senior Developer Perspective
The codebase currently lacks a formal testing framework and automated test suite. No pytest, unittest, or comparable test infrastructure is present. Testing appears to be primarily manual and exploratory. This creates maintenance and regression risks as features grow. The code is reasonably testable (good separation of concerns, dependency injection), but lacks the infrastructure to realize that potential. Key testable areas (validation logic, state management, error handling) could benefit from unit tests.

### macOS App Architect Perspective
From a system testing perspective, this application faces unique challenges. Testing Windows-specific functionality (pywinauto UI automation, Win32 APIs, process management) on macOS is impossible without mocking or conditional test suites. Native macOS testing would use XCTest, XCUITest (for UI testing), and rely on Swift/Objective-C frameworks. The current Python approach requires test doubles (mocks/stubs) for platform-specific code, but the codebase provides no such infrastructure. A proper macOS-native implementation would have platform-specific test bundles and integration tests with Xcode's test runner.

**Convergence:** Both perspectives agree that system-level integration testing (HID, MIDI, process management) is challenging and requires careful isolation of concerns.

**Divergence:** The developer perspective sees value in automated unit tests within Python; the macOS architect would use platform-native testing frameworks and XCTest bundles.

---

## Test Framework

### Current State
- **Status:** No test framework detected
- **Evidence:**
  - No pytest config (`pytest.ini`, `pyproject.toml` without test config, `tox.ini`)
  - No unittest test suite (no `*_test.py` or `test_*.py` files found)
  - No test directory structure (`tests/`, `test/`)
  - `requirements.txt` has no pytest, pytest-cov, unittest2, or similar

### Testing Approach (Observed)
- **Type:** Manual testing (exploratory, integration-level)
- **Entry points:** Script can be run directly: `python bridge2glm.py` with various `--` flags
- **Configuration:** Command-line arguments allow runtime behavior changes
- **Diagnostics:** Logging output (DEBUG, INFO, WARNING) provides observability

### Recommended Framework Setup (Future)
If automated testing is adopted:
- **Pytest:** Modern, fixture-based, parametrization support
- **Config location:** `pyproject.toml` or `pytest.ini`
- **Test discovery:** `tests/test_*.py` or `*_test.py` convention

---

## Test Structure (Not Present)

Since no tests exist, this section describes what test structure *could* look like based on codebase organization:

### Hypothetical Test Organization
```
tests/
├── unit/
│   ├── test_config.py              # Validation function tests
│   ├── test_glm_controller.py       # State management tests
│   ├── test_retry_logger.py         # Smart retry logging tests
│   ├── test_midi_constants.py       # Constant/enum tests
│   └── test_actions.py              # Domain action tests
├── integration/
│   ├── test_glm_manager.py          # Process management (Windows-only)
│   ├── test_glm_power.py            # UI automation (Windows-only)
│   └── test_bridge2glm.py           # End-to-end (requires hardware)
└── fixtures/
    ├── mock_devices.py              # Mock HID/MIDI devices
    └── test_data.py                 # Sample inputs
```

### Unit Test Scope
- **Validation:** `validate_volume_increases()`, `validate_click_times()`, `validate_device()`
  - Location: `tests/unit/test_config.py`
  - What to test: Valid inputs, boundary conditions, error messages
- **State machine:** `GlmController` state transitions, lock safety
  - Location: `tests/unit/test_glm_controller.py`
  - What to test: Power state transitions, command blocking during settling
- **Retry logging:** `SmartRetryLogger` milestone calculation
  - Location: `tests/unit/test_retry_logger.py`
  - What to test: Interval tracking, reset behavior, thread safety

### Integration Test Scope
- **Process management:** `GlmManager` (Windows only)
  - Requires: GLM executable path, process spawning permission
  - What to test: CPU gating, window stabilization, watchdog restart
- **Power control:** `GlmPowerController` (Windows only)
  - Requires: GLM window, screen access, pixel sampling
  - What to test: State detection accuracy, retry logic
- **End-to-end:** Full `bridge2glm.py` with hardware
  - Requires: VOL20 USB device, GLM application, MIDI ports
  - What to test: User interactions trigger correct GLM responses

---

## Test File Naming

### Convention (Proposed)
- **Unit tests:** `test_<module_name>.py`
  - Example: `test_config.py` for `config.py`, `test_retry_logger.py` for `retry_logger.py`
- **Location:** `tests/unit/` for unit tests, `tests/integration/` for integration tests
- **Modules with multiple test files:** Use `test_<module>_<concern>.py`
  - Example: `test_glm_manager_startup.py`, `test_glm_manager_watchdog.py`

---

## Mock Patterns (Hypothetical)

Since no tests exist, these are patterns that *should* be used if tests are added:

### Pattern 1: Mocking External Dependencies

**What to mock:**
- `psutil.Process` (for process checks in `GlmManager`)
- `hid.device()` (for HID device operations)
- `mido.open_input()`/`open_output()` (for MIDI ports)
- Windows APIs (`ctypes.windll`, `win32api`)
- `pywinauto.Desktop` (for window finding)

**What NOT to mock:**
- Validation logic (`validate_*()` functions) - pure, deterministic
- `SmartRetryLogger` - stateful but simple, test directly
- Dataclasses (`SetVolume`, `GlmManagerConfig`) - immutable data, no mocking needed

### Pattern 2: Fixture-Based Mocking (Pytest)

```python
import pytest
from unittest.mock import Mock, patch, MagicMock

@pytest.fixture
def mock_process():
    """Mock psutil.Process for testing GlmManager."""
    proc = MagicMock()
    proc.pid = 1234
    proc.is_running.return_value = True
    return proc

@pytest.fixture
def mock_glm_manager_config():
    """Minimal config for testing."""
    from PowerOnOff.glm_manager import GlmManagerConfig
    return GlmManagerConfig(glm_path="dummy.exe")

def test_glm_manager_restart(mock_process, mock_glm_manager_config):
    """Test that GlmManager restarts process when dead."""
    with patch('PowerOnOff.glm_manager.psutil') as mock_psutil:
        mock_psutil.Process.return_value = mock_process
        # Test code here
```

### Pattern 3: Platform-Conditional Tests

Since Windows API testing is impossible on macOS/Linux, use markers:

```python
import pytest
import sys

pytestmark = pytest.mark.skipif(
    sys.platform != "win32",
    reason="Windows-only process management tests"
)

def test_glm_manager_start():
    """Test GLM process startup (Windows only)."""
    # Windows-specific test code
```

### Pattern 4: Timeout Testing for Retry Logic

```python
def test_retry_logger_milestones():
    """Test SmartRetryLogger interval computation."""
    from retry_logger import SmartRetryLogger

    logger = SmartRetryLogger(intervals=[0.1, 0.5, 1.0])

    # First attempt always logs
    assert logger.should_log("test_key") is True

    # Reset and test boundary
    logger.reset("test_key")
    assert logger.should_log("test_key") is True
    assert logger.get_retry_count("test_key") == 1
```

---

## Fixtures and Test Data

### Proposed Fixtures (Hypothetical)

**Location:** `tests/fixtures/`

### test_data.py
```python
"""Test data and sample inputs."""

# Valid inputs for validation functions
VALID_VOLUME_INCREASES = [
    [1, 1, 2, 2, 3],
    [1, 2, 3, 4, 5, 6, 7, 8, 9, 10],  # Max length
    [1, 1],  # Min length
]

INVALID_VOLUME_INCREASES = [
    [1],  # Too short
    [1] * 16,  # Too long
    [0, 1, 2],  # 0 not allowed
    [11, 1, 2],  # 11 exceeds max
]

# Valid device VID/PID pairs
VALID_DEVICES = [
    ("0x07d7,0x0000", (0x07d7, 0x0000)),  # VOL20
    ("0xFFFF,0xFFFF", (0xFFFF, 0xFFFF)),  # Max values
]

INVALID_DEVICES = [
    "0x10000,0x0000",  # VID exceeds 16-bit
    "invalid,0x0000",  # Non-hex
]

# GlmController test scenarios
POWER_TRANSITION_SCENARIO = {
    "initial_state": {"power": True, "mute": False},
    "action": "power_off",
    "settling_time": 2.0,
    "expected_final": {"power": False},
}
```

### mock_devices.py
```python
"""Mock device/hardware for testing without real hardware."""

class MockHidDevice:
    """Mock HID device for testing."""
    def __init__(self):
        self.events = []

    def read(self, length, timeout):
        """Return mock VOL20 event."""
        if self.events:
            return self.events.pop(0)
        return [0] * length  # Empty event

    def close(self):
        """Mock close (no-op)."""
        pass

class MockMidiPort:
    """Mock MIDI input/output port."""
    def __init__(self, name):
        self.name = name
        self.messages_sent = []

    def send(self, msg):
        """Record MIDI message sent."""
        self.messages_sent.append(msg)

    def receive(self, block=False):
        """Mock receive (returns None)."""
        return None
```

---

## Coverage

### Current State
- **No coverage measurement:** No `.coverage`, `coverage.xml`, or coverage config found
- **Reason:** No test suite to measure coverage against

### Recommended Setup (Future)
```bash
# Install coverage
pip install pytest-cov

# Run with coverage
pytest --cov=glm_core --cov=PowerOnOff --cov=api --cov-report=html

# View report
open htmlcov/index.html
```

### Target Coverage
- **Unit-testable code (validation, state, retry logic):** 80%+
- **Integration/system code (Windows APIs, MIDI):** 0% (can't test on non-Windows, requires hardware)
- **Overall target:** 60-70% (realistic given platform-specific constraints)

### Coverage Gaps (Current)
- **Untested:** All of `bridge2glm.py` (main orchestration, no tests)
- **Untested:** `PowerOnOff/glm_manager.py` (process management, Windows-specific)
- **Untested:** `PowerOnOff/glm_power.py` (UI automation, Windows-specific)
- **Untested:** `api/rest.py`, `api/mqtt.py` (API endpoints, requires server setup)
- **Untestable:** HID input reading (requires hardware)
- **Untestable:** MIDI port communication (requires audio interface)

---

## Test Types

### Unit Tests (If Implemented)

**Validation Functions** - `test_config.py`
```python
def test_validate_volume_increases_valid():
    """Test parsing valid volume increase list."""
    result = validate_volume_increases("[1, 2, 3, 4]")
    assert result == [1, 2, 3, 4]

def test_validate_volume_increases_invalid_length():
    """Test validation rejects short list."""
    with pytest.raises(argparse.ArgumentTypeError):
        validate_volume_increases("[1]")
```

**State Management** - `test_glm_controller.py`
```python
def test_power_settling_blocks_commands():
    """Test that ALL commands are blocked during power settling."""
    controller = GlmController()
    controller.start_power_transition(target_state=False)

    allowed, wait, reason = controller.can_accept_command()
    assert allowed is False
    assert reason == "power_settling"
    assert wait > 0

def test_power_transition_completes():
    """Test power transition state ends after settling time."""
    controller = GlmController()
    controller.start_power_transition(target_state=False)

    # Wait for settling
    time.sleep(POWER_SETTLING_TIME + 0.1)

    allowed, wait, reason = controller.can_accept_command()
    assert allowed is True
```

### Integration Tests (If Implemented)

**Process Management** - `test_glm_manager.py` (Windows only)
```python
@pytest.mark.skipif(sys.platform != "win32", reason="Windows only")
def test_glm_manager_finds_existing_process(mock_psutil):
    """Test manager detects existing GLM process."""
    config = GlmManagerConfig(glm_path=r"C:\path\GLMv5.exe")
    manager = GlmManager(config=config)

    assert manager.is_alive()

@pytest.mark.skipif(sys.platform != "win32", reason="Windows only")
def test_cpu_gating_waits_for_idle():
    """Test manager waits for CPU below threshold."""
    # Requires real CPU measurement or mock psutil.cpu_percent()
    pass
```

**Retry Logic** - `test_retry_logger.py`
```python
def test_retry_logger_milestone_spacing():
    """Test retry logger respects milestone intervals."""
    logger = SmartRetryLogger(intervals=[1, 2, 5])
    key = "test_retry"

    # First call
    assert logger.should_log(key) is True

    # Immediate retry - too soon
    assert logger.should_log(key) is False

    # After 1 second
    time.sleep(1.01)
    assert logger.should_log(key) is True
```

### E2E Tests (Manual)

**Current approach:** Run `bridge2glm.py` with test devices and verify console output/MIDI messages.

**Ideal approach (not implemented):**
- Connect physical VOL20 device
- Launch GLM
- Rotate knob, verify volume changes
- Click for power toggle, verify state change
- Check MQTT/REST API responses

---

## Common Patterns

### Async Testing (Not Used)

This codebase uses `threading`, not `async/await`, so async testing is N/A.

If moving to async:
```python
import pytest
import asyncio

@pytest.mark.asyncio
async def test_async_operation():
    result = await some_async_function()
    assert result == expected
```

### Error Testing

**Pattern: Exception Assertion**
```python
def test_validate_device_invalid_format():
    """Test device validation rejects invalid hex."""
    with pytest.raises(argparse.ArgumentTypeError) as exc_info:
        validate_device("invalid,0x0000")
    assert "Invalid VID/PID format" in str(exc_info.value)

def test_glm_state_unknown_carries_context():
    """Test GlmStateUnknownError stores diagnostic data."""
    from PowerOnOff.exceptions import GlmStateUnknownError

    error = GlmStateUnknownError("Unable to determine state", rgb=(255, 0, 0), point=(100, 100))
    assert error.rgb == (255, 0, 0)
    assert error.point == (100, 100)
```

### Thread Safety Testing

**Pattern: Lock Stress Test**
```python
import threading

def test_glm_controller_thread_safety():
    """Test GlmController state is safe under concurrent access."""
    controller = GlmController()
    results = []

    def read_state(n):
        for _ in range(100):
            allowed, wait, reason = controller.can_accept_command()
            results.append(allowed)

    threads = [threading.Thread(target=read_state, args=(i,)) for i in range(5)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    # Should never raise, results should be consistent
    assert len(results) == 500
```

### Time-Based Testing

**Pattern: Mocking time for fast tests**
```python
from unittest.mock import patch
import time

def test_power_settling_timeout():
    """Test power settling respects timeout."""
    controller = GlmController()
    controller.start_power_transition(target_state=False)

    with patch('time.time') as mock_time:
        # Simulate time passing
        mock_time.return_value = time.time()
        assert controller.is_power_settling() is True

        mock_time.return_value += POWER_SETTLING_TIME + 0.1
        assert controller.is_power_settling() is False
```

---

## Running Tests (Proposed)

### Single Test
```bash
pytest tests/unit/test_config.py::test_validate_volume_increases_valid -v
```

### All Unit Tests
```bash
pytest tests/unit/ -v
```

### With Coverage
```bash
pytest tests/ --cov=glm_core --cov=PowerOnOff --cov=config --cov=retry_logger --cov-report=html
```

### Windows-Only Integration Tests
```bash
pytest tests/integration/ -v -m "not requires_hardware"
```

### Watch Mode (Using pytest-watch)
```bash
ptw tests/unit/ -- -v
```

---

## Dual Perspective Summary

| Aspect | Senior Developer | macOS Architect |
|--------|-----------------|------------------|
| **Test framework** | Pytest recommended | XCTest for Xcode integration |
| **Unit test scope** | Validation, state, retry logic | Model layer, business logic |
| **Integration tests** | Mock Windows APIs | XCUITest for UI, integration bundles |
| **Mocking strategy** | unittest.mock, pytest fixtures | OCMock, or Swift mock objects |
| **Platform testing** | Conditional skipif markers | Scheme-based test configurations |
| **CI/CD integration** | pytest in GitHub Actions | Xcode Cloud, TestFlight |
| **Coverage tools** | coverage.py, pytest-cov | Xcode built-in coverage |

**Key alignment:** Both perspectives recognize that testing Windows-specific functionality (process management, UI automation) is a primary challenge and requires careful isolation and mocking.

**Key difference:** The developer perspective uses Python testing tools; the macOS architect would use Xcode's native XCTest framework, leveraging compiler-supported test discovery and IDE integration.

---

## Recommendations

### High Priority (If Testing is Adopted)
1. **Validation function tests** - Quick wins, deterministic, no mocking needed
2. **Retry logger tests** - Verify exponential backoff logic
3. **GlmController state tests** - Verify power transition logic, thread safety

### Medium Priority
4. **Configuration parsing tests** - Verify CLI argument handling end-to-end
5. **Mock HID/MIDI integration** - Test input handling without hardware
6. **Mock process management** - Test watchdog, restart logic

### Low Priority (Hardware-Dependent)
7. **Real HID device tests** - Requires VOL20 connected
8. **Real MIDI port tests** - Requires audio interface and GLM
9. **End-to-end tests** - Full system with hardware

---

*Testing analysis: 2026-03-21*
