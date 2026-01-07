# Future Work and Improvements

This document captures planned improvements and best practice recommendations for the GLM MIDI Controller project.

---

## 1. Logging System Improvements

### Current State (as of 2026-01-06)

The logging system uses a QueueHandler + QueueListener pattern for thread-safety, which is correct for multi-threaded applications. However, there are several areas for improvement.

#### Current Architecture
```
Log Record -> QueueHandler -> Queue -> QueueListener -> [FileHandler, ConsoleHandler]
```

#### Current Format
```python
'%(asctime)s [%(levelname)s] %(message)s'
# Output: 2026-01-06 22:39:06,669 [INFO] Connected to MIDI...
```

### Issues to Address

#### 1.1 Global Logger Anti-Pattern
**Problem:** Logger is reassigned inside `setup_logging()` using `global logger`.

**Current (problematic):**
```python
def setup_logging(...):
    global logger
    logger = logging.getLogger(__name__)  # Inside function - bad
```

**Recommended:**
```python
# At module level, top of file
logger = logging.getLogger(__name__)  # Never reassigned
```

#### 1.2 Limited Log Format
**Problem:** Missing critical debugging information for daemon/service troubleshooting.

**Current format lacks:**
- Thread name (critical for multi-threaded debugging)
- Module/logger name (which component logged this?)
- Line number (where in code?)
- Milliseconds precision

**Recommended format:**
```python
DETAILED_FORMAT = (
    '%(asctime)s.%(msecs)03d [%(levelname)-8s] '
    '[%(threadName)-15s] %(name)s:%(lineno)d - %(message)s'
)
# Output: 2026-01-06 22:39:06.669 [INFO    ] [MIDIReaderThread] __main__:1064 - GLM startup detected
```

#### 1.3 Inconsistent Log Level Usage
**Problem:** No clear guidelines on what level to use, leading to noise at INFO level and important info at DEBUG level.

**Recommended Guidelines:**

| Level | Use For | Examples |
|-------|---------|----------|
| **DEBUG** | Developer diagnostics, variable dumps, internal state | `"RGB values: (28, 134, 100)"`, `"Cache hit for window"` |
| **INFO** | Operational milestones, state changes, user actions | `"Power state changed to ON"`, `"Volume set to 80"` |
| **WARNING** | Recoverable issues, retry situations, deprecations | `"MIDI reconnecting after timeout"`, `"Config value missing, using default"` |
| **ERROR** | Failures requiring attention, but system continues | `"Failed to set power state"`, `"WebSocket client error"` |
| **CRITICAL** | System cannot continue, immediate attention needed | `"Cannot bind to port, exiting"`, `"Database connection failed"` |

**Messages to reclassify:**
- `logger.debug("Power pattern with 6 msgs...")` -> Should be INFO (operational)
- `logger.debug("GLM state: vol=82...")` -> Should be INFO (state change)
- `logger.info("MIDI TX: Vol+(CC21)=127")` -> Should be DEBUG (verbose operational detail)

#### 1.4 No Structured Logging
**Problem:** All messages are free-form strings, hard to parse/aggregate/query in log management systems.

**Recommended JSON Formatter:**
```python
import json
import logging

class JsonFormatter(logging.Formatter):
    """JSON formatter for structured logging."""

    def format(self, record):
        log_obj = {
            "timestamp": self.formatTime(record, self.datefmt),
            "level": record.levelname,
            "logger": record.name,
            "thread": record.threadName,
            "module": record.module,
            "line": record.lineno,
            "message": record.getMessage(),
        }
        if record.exc_info:
            log_obj["exception"] = self.formatException(record.exc_info)
        if hasattr(record, 'extra_data'):
            log_obj["data"] = record.extra_data
        return json.dumps(log_obj)
```

### Recommended Implementation

#### Complete Setup Function
```python
import logging
import logging.handlers
import sys
from pathlib import Path
from queue import Queue

def setup_logging(
    log_level: str = "INFO",
    log_dir: Path = None,
    app_name: str = "glm-agent",
    max_bytes: int = 10 * 1024 * 1024,  # 10MB
    backup_count: int = 5,
    json_format: bool = False,
    console_level: str = None,  # Separate console level
):
    """
    Configure logging for a system daemon.

    Best practices implemented:
    - Separate handlers for console (stderr) and file
    - Rotating file handler with size limits
    - Thread-safe queue-based logging
    - Optional JSON formatting for log aggregation
    - Proper level filtering per handler

    Args:
        log_level: Minimum level for file logging (DEBUG, INFO, WARNING, ERROR)
        log_dir: Directory for log files (default: script directory)
        app_name: Application name for log file naming
        max_bytes: Max size per log file before rotation
        backup_count: Number of rotated files to keep
        json_format: If True, use JSON formatting (for log aggregation)
        console_level: Separate level for console (default: WARNING)

    Returns:
        Callable to stop logging (call on shutdown)
    """
    log_dir = log_dir or Path(__file__).parent
    log_file = log_dir / f"{app_name}.log"
    console_level = console_level or "WARNING"

    # Formatters
    if json_format:
        formatter = JsonFormatter()
    else:
        formatter = logging.Formatter(
            '%(asctime)s.%(msecs)03d [%(levelname)-8s] '
            '[%(threadName)-15s] %(name)s - %(message)s',
            datefmt='%Y-%m-%d %H:%M:%S'
        )

    # Console handler (stderr for visibility in systemd/docker)
    console_handler = logging.StreamHandler(sys.stderr)
    console_handler.setLevel(getattr(logging, console_level.upper()))
    console_handler.setFormatter(formatter)

    # File handler with rotation
    file_handler = logging.handlers.RotatingFileHandler(
        log_file,
        maxBytes=max_bytes,
        backupCount=backup_count,
        encoding='utf-8',
    )
    file_handler.setLevel(logging.DEBUG)  # File gets everything
    file_handler.setFormatter(formatter)

    # Queue-based async logging (thread-safe)
    log_queue = Queue(-1)  # Unlimited size
    queue_handler = logging.handlers.QueueHandler(log_queue)

    # Configure root logger
    root = logging.getLogger()
    root.setLevel(getattr(logging, log_level.upper()))
    root.handlers = [queue_handler]

    # Start listener thread
    listener = logging.handlers.QueueListener(
        log_queue,
        file_handler,
        console_handler,
        respect_handler_level=True,
    )
    listener.start()

    # Suppress noisy third-party loggers
    for name in ["websockets", "uvicorn", "paho.mqtt", "asyncio"]:
        logging.getLogger(name).setLevel(logging.WARNING)

    return listener.stop
```

#### Per-Module Logger Pattern
Each module should have its own logger at the top of the file:

```python
# api/rest.py
import logging
logger = logging.getLogger(__name__)  # Gets "api.rest"

# api/mqtt.py
import logging
logger = logging.getLogger(__name__)  # Gets "api.mqtt"

# PowerOnOff/glm_power.py
import logging
logger = logging.getLogger(__name__)  # Gets "PowerOnOff.glm_power"
```

Benefits:
- Per-module log level control: `logging.getLogger("api.mqtt").setLevel(logging.DEBUG)`
- Clear source identification in logs
- Easy filtering by component

### Advanced: Separate Log Streams

For production system agents, consider separate log files for different purposes:

```python
# Audit log - who did what (security/compliance)
audit_logger = logging.getLogger("glm.audit")
audit_handler = RotatingFileHandler("glm-audit.log", ...)
audit_logger.addHandler(audit_handler)
audit_logger.info("Volume set to 80 via REST API", extra={
    "source_ip": "192.168.1.5",
    "user": "anonymous",
    "action": "set_volume",
    "value": 80,
})

# Metrics log - for monitoring/alerting
metrics_logger = logging.getLogger("glm.metrics")
metrics_logger.info("metrics", extra={
    "midi_latency_ms": 15,
    "queue_depth": 0,
    "power_state": "on",
    "volume": 80,
})

# Application log - operational events
app_logger = logging.getLogger("glm.app")
app_logger.info("Power state changed: OFF -> ON")
```

### Time-Based Rotation

For long-running daemons, consider time-based rotation instead of/in addition to size-based:

```python
from logging.handlers import TimedRotatingFileHandler

# Rotate daily at midnight, keep 30 days
handler = TimedRotatingFileHandler(
    "glm-agent.log",
    when='midnight',
    interval=1,
    backupCount=30,
    encoding='utf-8',
)
```

### Third-Party Logger Suppression

Current approach with `WebSocketErrorFilter` works but is complex. Simpler approach:

```python
# At the very start of the application, before other imports
import logging

# Pre-configure noisy loggers before they're created
for name in [
    "websockets",
    "websockets.legacy.protocol",
    "uvicorn",
    "uvicorn.error",
    "asyncio",
    "paho.mqtt",
]:
    logging.getLogger(name).setLevel(logging.CRITICAL)
```

### Implementation Priority

| Priority | Task | Effort | Impact |
|----------|------|--------|--------|
| **P1** | Add thread name to format | 5 min | High - critical for debugging |
| **P1** | Add module name to format | 5 min | High - identifies log source |
| **P2** | Standardize log levels across codebase | 1-2 hours | Medium - reduces noise |
| **P2** | Use module-level loggers properly | 30 min | Medium - enables filtering |
| **P3** | Add JSON formatter option | 30 min | Low - useful for log aggregation |
| **P3** | Separate audit/metrics logs | 2 hours | Low - useful for production |
| **P3** | Time-based rotation | 15 min | Low - useful for long-running |

---

## 2. Session Management / UI Automation

### Current State
- Uses `WTSEnumerateSessionsW` for session state detection
- Uses `tscon` for reconnecting disconnected sessions to console
- Requires admin/SYSTEM privileges for `tscon`

### Known Issues
- `tscon` works via SSH (OpenSSH runs as SYSTEM) but fails without admin when run normally
- Workaround: Run script via SSH or as Administrator

### Potential Improvements
- Create a Windows scheduled task for `tscon` that runs as SYSTEM (one-time admin setup)
- Create a Windows service wrapper for the agent
- Investigate alternative approaches (VDD - Virtual Display Driver)

---

## 3. Code Structure Improvements

### Potential Refactoring
- Split `fosi2-glm-midi-sonnet4.5.py` into smaller modules
- Create a proper package structure
- Add type hints throughout
- Add unit tests for core logic

### Configuration Management
- Consider using a config file (YAML/TOML) instead of command-line args
- Add config validation
- Support environment variable overrides

---

## 4. Documentation

### Needed Documentation
- Architecture diagram showing component interactions
- API documentation for REST endpoints
- MQTT topic documentation
- Installation/setup guide for Windows
- Troubleshooting guide

---

## 5. Testing

### Test Coverage Needed
- Unit tests for `GlmController` state machine
- Unit tests for `AccelerationHandler`
- Integration tests for MIDI communication
- Mock-based tests for UI automation

---

## 6. GLM Manager Module (Replace PowerShell Script)

### Goal
Eliminate `minimize-glm.newer.ps1` by integrating its functionality into the Python agent as an imported module (`glm_manager.py`).

### Current PowerShell Script Functionality

The script `minimize-glm.newer.ps1` performs:

1. **CPU Gating** - Wait for CPU < 2% before starting GLM (avoids competing with Windows startup)
2. **Start GLM** - Launch `GLMv5.exe` if not running
3. **Priority Boost** - Set process priority to AboveNormal
4. **Window Handle Stabilization** - Poll until MainWindowHandle stops changing
5. **Non-blocking Minimize** - Use `PostMessage(WM_SYSCOMMAND, SC_MINIMIZE)` to minimize without blocking
6. **Watchdog Loop** - Monitor `Responding` property, kill and restart if hung for ~30s

### Proposed Architecture

```
glm_manager.py (new module)
├── GlmManager class
│   ├── start_glm() - Start GLM with CPU gating, priority, minimize
│   ├── stop_glm() - Graceful shutdown
│   ├── restart_glm() - Kill + start
│   ├── is_alive() - Check process exists
│   ├── is_responding() - Check GUI not hung
│   └── _watchdog_thread() - Background monitoring
└── Imported by main script, runs watchdog in background thread
```

### Key Design Decisions

1. **Imported Module, Not Separate Process** - `glm_manager.py` is imported by the main script and runs a watchdog thread, not a separate Python process.

2. **Integration with Power Controller** - When GLM restarts, the power controller must reinitialize its window handle cache.

3. **Watchdog Responsibilities** - Only monitors GLM health and restarts when needed. Does NOT handle MIDI health (that remains separate).

### Watchdog Pseudo-code

```python
class GlmManager:
    def __init__(self, power_controller_reinit_callback):
        self.reinit_callback = power_controller_reinit_callback
        self.non_responsive_count = 0
        self._running = False
        self._watchdog_thread = None

    def start_watchdog(self):
        self._running = True
        self._watchdog_thread = threading.Thread(target=self._watchdog_loop, daemon=True)
        self._watchdog_thread.start()

    def _watchdog_loop(self):
        """Watchdog thread - runs every 5 seconds."""
        while self._running:
            if not self.is_alive():
                # GLM not running - start it
                self.start_glm()
                self.reinit_callback()  # Reinitialize power controller
                self.non_responsive_count = 0
            elif not self.is_responding():
                # GLM hung
                self.non_responsive_count += 1
                if self.non_responsive_count >= 6:  # ~30 seconds
                    self.kill_glm()
                    self.start_glm()
                    self.reinit_callback()
                    self.non_responsive_count = 0
            else:
                # Healthy
                self.non_responsive_count = 0

            # TODO: MIDI health check placeholder for future

            time.sleep(5)
```

### Configuration Options

```python
GLM_MANAGER_CONFIG = {
    "glm_path": r"C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe",
    "process_name": "GLMv5",

    # CPU gating (only at initial script start)
    "cpu_threshold": 2,          # % CPU considered "idle enough"
    "cpu_check_interval": 5,     # seconds between checks
    "cpu_max_checks": 60,        # 60 * 5s = 5 minutes max wait

    # Window stabilization
    "post_start_sleep": 5,       # seconds after start before minimize
    "stable_handle_count": 2,    # handle must be same N times
    "minimize_attempts": 1,      # minimize at least N times
    "stabilize_timeout": 60,     # max seconds for stabilization

    # Watchdog
    "watchdog_interval": 5,      # seconds between checks
    "max_non_responsive": 6,     # checks before kill (6*5=30s)
    "restart_delay": 5,          # seconds to wait before restart
}
```

### Implementation Steps

1. **Create `glm_manager.py`** with GlmManager class
2. **Port CPU gating** from PowerShell (use `psutil` for CPU monitoring)
3. **Port process management** (start, kill, priority) using `subprocess` and `psutil`
4. **Port window stabilization** using `pywinauto` or `ctypes` for Win32 calls
5. **Port non-blocking minimize** using `ctypes` PostMessage
6. **Implement watchdog thread** with callback for power controller reinitialization
7. **Integrate into main script** - instantiate GlmManager early, pass reinit callback
8. **Test thoroughly** - startup, crash recovery, hang detection, minimize behavior
9. **Remove PowerShell dependency** - delete `minimize-glm.newer.ps1` after validation

### Dependencies

- `psutil` - CPU monitoring, process management (already used in project)
- `pywinauto` - Window handle management (already used in project)
- `ctypes` - Win32 API calls for PostMessage minimize

### Migration Strategy

1. Implement Python version alongside PowerShell script
2. Add command-line flag to choose which manager to use
3. Test Python version extensively
4. Make Python version the default
5. Remove PowerShell script after validation period

---

## Notes for Future Implementation

### When Implementing Logging Changes
1. Start with format changes (low risk, high value)
2. Test with DEBUG level to see all messages
3. Then adjust individual message levels
4. Finally, consider structural changes (separate loggers)

### Key Files to Modify
- `fosi2-glm-midi-sonnet4.5.py`: Main logging setup, ~90 logger calls
- `api/rest.py`: WebSocket error filtering, uvicorn log config
- `api/mqtt.py`: Module logger
- `PowerOnOff/glm_power.py`: Module logger

### Testing Logging Changes
```bash
# Run with DEBUG to see all messages
python fosi2-glm-midi-sonnet4.5.py --log-level DEBUG

# Check log file for proper formatting
tail -f fosi2-glm-midi-sonnet4.5.log

# Test WebSocket disconnect handling
# (connect browser to http://localhost:8080, then close tab)
```
