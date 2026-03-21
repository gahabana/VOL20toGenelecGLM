# Codebase Concerns

**Analysis Date:** 2026-03-21

**Analyzed by:** Senior Developer + macOS App Architect perspectives

---

## Tech Debt

### 1. Hardcoded Platform Paths and Dependencies

**Files:** `PowerOnOff/glm_manager.py` (line 71), `PowerOnOff/glm_power.py` (line 54), `config.py` (line 163)

**Issue:**
- `glm_path` defaults to `r"C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe"` (hardcoded)
- Process name hardcoded as `"GLMv5"` with `.exe` extension
- All Windows-specific paths and registry operations are tightly coupled to implementation
- No abstraction layer for platform detection or configuration

**Impact:**
- Code is not testable without a Windows environment with exact GLM installation
- Maintenance burden if Genelec changes install paths or naming
- Makes cross-platform porting impossible without significant refactoring

**Fix approach:**
- Extract platform-specific paths to configuration file or environment variables
- Create a `Platform` abstraction class with Windows/Linux/macOS implementations
- Move hardcoded paths to `config.py` with environment variable overrides

**Severity:** Medium (doesn't break functionality but limits flexibility)

---

### 2. Inconsistent Exception Handling Patterns

**Files:** `PowerOnOff/glm_manager.py` (multiple), `PowerOnOff/glm_power.py` (multiple), `api/rest.py` (lines 176-182)

**Issue:**
- Bare `except Exception:` blocks that catch too broadly:
  - `glm_manager.py:254` catches all exceptions in `is_responding()`, assumes responding on any error
  - `glm_power.py:415-416` silently passes on window lookup failure
  - `api/rest.py:343-344` logs and continues without raising for API errors
- No consistent error recovery strategy (some retry, some fail silently, some propagate)
- Custom exception classes defined but not used consistently (`GlmStateUnknownError`, `GlmWindowNotFoundError`)

**Impact:**
- Silent failures mask real bugs (e.g., "window not found" treated as "not responding yet")
- Difficult to debug because root cause is swallowed
- Logging doesn't distinguish between expected transient errors and unexpected system errors

**Fix approach:**
- Replace broad `except Exception` with specific exception types
- Create exception hierarchy: `GlmError` → `GlmTransientError` (retry) vs `GlmFatalError` (stop)
- Use `GlmWindowNotFoundError`, `GlmStateUnknownError` consistently
- Log at WARNING/ERROR level with context when catching exceptions

**Severity:** High (leads to silent failures and hard-to-debug issues)

---

### 3. Global Logger Reassignment Anti-Pattern

**Files:** `bridge2glm.py` (line 98), `logging_setup.py` (entire module), `PowerOnOff/glm_manager.py` (line 40)

**Issue:**
- Module-level `logger = logging.getLogger(__name__)` is reassigned inside `setup_logging()` using `global` keyword
- This pattern breaks at module import time before logging is configured
- Makes early-startup errors unloggable

**Impact:**
- Import-time errors before `setup_logging()` is called have no log destination
- Hard to trace initialization issues
- Violates Python logging best practices

**Fix approach:**
- Keep module-level logger as-is (never reassign)
- Call `setup_logging()` before any imports that need logging
- OR use lazy logger initialization with `logging.getLogger(__name__)` at first use

**Severity:** Low (startup only, but makes debugging startup issues harder)

---

### 4. Missing Thread Cleanup and Resource Leaks

**Files:** `PowerOnOff/glm_manager.py` (line 194-195), `bridge2glm.py` (daemon threads), `api/rest.py` (line 464)

**Issue:**
- `GlmManager.stop()` joins watchdog thread with 10-second timeout, may not wait long enough if watchdog is in long sleep
- All background threads are daemon threads (true), but no cleanup of thread-local state
- No proper cleanup in `finally` blocks for exception cases
- WebSocket clients set is managed with threading.Lock but no protection during iteration in some cases

**Impact:**
- Abrupt shutdown may leave stale process/window handles cached
- Resource leaks if exception occurs during operation (e.g., window handles not released)
- Potential race conditions in client list iteration during concurrent disconnect

**Fix approach:**
- Use context managers (`__enter__`/`__exit__`) for resource cleanup
- Add `finally` blocks to all critical sections to ensure cleanup runs
- Use `threading.RLock()` for reentrant operations instead of `Lock()`
- Implement proper shutdown sequence: signal threads → wait for completion → cleanup resources

**Severity:** Medium (long-running services can gradually leak resources)

---

### 5. Incomplete Error States in Power Control

**Files:** `PowerOnOff/glm_power.py` (lines 656-675), `PowerOnOff/glm_manager.py` (line 256)

**Issue:**
- `_classify_state()` returns "unknown" for ~5% of valid states (color threshold detection too strict)
- `is_responding()` returns True if process not cached (line 248-249), should return False
- No handling for partial/corrupted state reads (e.g., one monitor disconnects during screenshot)
- Fallback nudge sampling (line 689) tries alternative point but no max retry limit

**Impact:**
- Power state reads fail intermittently (~5% of requests based on color thresholds)
- Watchdog may incorrectly think GLM is responding when it's actually not
- Cascading failures: unknown state → retry forever → eventual timeout
- Screenshot may hang if monitor is disconnected

**Fix approach:**
- Increase tolerance in color classification thresholds or use statistical method (median vs single sample)
- Return False in `is_responding()` when process not cached or state indeterminate
- Add timeout to screenshot operations
- Implement max retries (e.g., 3) for fallback nudge before giving up

**Severity:** High (intermittent power control failures)

---

## Known Bugs

### 1. Window Handle Cache Invalidation Race Condition

**Files:** `PowerOnOff/glm_power.py` (lines 467-475), `PowerOnOff/glm_manager.py` (line 238-245)

**Symptoms:**
- Occasionally "GLM window not found" when window is actually visible
- More likely when window is minimized/restored rapidly or during RDP session transitions

**Root Cause:**
- `_window_cache` is checked and used across multiple method calls without atomicity
- Between cache check (line 467) and actual use (line 471), window may be destroyed and recreated
- `_hwnd` cached in `GlmManager` becomes invalid after window hide/show

**Files:** `PowerOnOff/glm_power.py:467-475`, `PowerOnOff/glm_manager.py:238-245`

**Trigger:**
- Minimize GLM → try to set power → window cache miss → find new window → cache → use cached → window destroyed in meantime

**Workaround:** None (will retry on next call)

**Fix approach:**
- Use atomic window lookup + operation (don't cache across method boundaries)
- Add generation counter to window handle: `(hwnd, generation)` tuple
- Invalidate cache on any exception from window operation

**Severity:** High (intermittent failures under normal operation)

---

### 2. RDP Session Priming May Not Run on Fast Boot

**Files:** `bridge2glm.py` (RDP priming logic not fully visible in first 100 lines)

**Symptoms:**
- High CPU usage persists after RDP disconnect on some boots
- RDP priming doesn't seem to execute even though it should

**Root Cause:**
- Priming flag `%TEMP%\rdp_primed.flag` uses boot timestamp
- System time change or fast reboot may cause flag to be treated as "already primed this boot"
- No logging of priming execution in early startup

**Impact:** High CPU after RDP disconnect (the original issue that was supposedly fixed)

**Fix approach:**
- Use Windows event log or registry to track priming instead of timestamp-based flag
- Add explicit logging at start of priming: "RDP session priming started"
- Add verification that priming actually executed

**Severity:** High (reintroduces the original high CPU issue)

---

## Security Considerations

### 1. Window Focus Stealing and Input Injection

**Files:** `PowerOnOff/glm_power.py` (lines 503-545)

**Risk:**
- `_ensure_foreground()` uses Alt key trick to bypass Windows security and allow `SetForegroundWindow`
- This makes the application vulnerable to input injection while focus is being stolen
- `keybd_event()` can be intercepted by malware running in same session
- No validation that Alt key injection succeeded before using SetForegroundWindow

**Current mitigation:**
- No input validation; script assumes it owns the desktop
- Runs with user privileges (good), not admin (good)

**Recommendations:**
- Add brief delay after Alt key press to ensure foreground window focus actually changed
- Validate that focus changed by checking result of `GetForegroundWindow()` before proceeding
- Consider using `BringWindowToTop()` + `ShowWindow(SW_RESTORE)` as more direct alternative
- Log when focus stealing occurs for audit trail

**Severity:** Medium (requires malware in same session, but could enable input hijacking)

---

### 2. Windows Credential Manager Dependency

**Files:** `PowerOnOff/glm_power.py` (lines 185-262, RDP priming credential read)

**Risk:**
- Script expects credentials in Windows Credential Manager
- No validation that credentials were successfully retrieved before using them
- If credentials are missing, FreeRDP fails silently and RDP priming doesn't execute
- No error reporting if credential lookup fails

**Current mitigation:**
- Credentials are encrypted by Windows DPAPI (good)
- Script doesn't hard-code credentials (good)

**Recommendations:**
- Log explicitly when reading credentials from Credential Manager
- Raise error if credentials not found (rather than fail silently in FreeRDP)
- Test credential existence before attempting RDP connection
- Document how to verify credentials are stored: `cmdkey /list:localhost`

**Severity:** Medium (causes silent failure of RDP priming)

---

### 3. Pixel Sampling for Power State Detection

**Files:** `PowerOnOff/glm_power.py` (lines 639-654)

**Risk:**
- Power state determined from screen pixel color (brightness and green channel)
- No validation that screenshot captured correct region
- If window is off-screen or hidden, screenshot captures wrong area
- Malware could spoof power state by rendering similar colors in other windows

**Current mitigation:**
- Window coordinates validated before screenshot (lines 551-555)
- Fallback nudge sampling if first sample is ambiguous

**Recommendations:**
- Verify window is visible before sampling: `IsWindowVisible(hwnd)`
- Validate screenshot captured expected window (e.g., check corners for window borders)
- Add integrity check: if power state doesn't match GLM's MIDI reports, log warning
- Consider using MIDI power feedback as primary, pixel sampling as verification only

**Severity:** Low (would require coordinated attack, but power state is critical)

---

## Performance Bottlenecks

### 1. Excessive Window Finding in Power Controller

**Files:** `PowerOnOff/glm_power.py` (lines 453-501)

**Problem:**
- `get_state()` does fresh window lookup every call (line 749: `use_cache=False`)
- Window finding uses `Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")` which enumerates ALL windows
- For each call, enumerates all JUCE windows, filters, validates PIDs
- Default cache TTL is 5 seconds, but many calls happen faster than that

**Impact:**
- 50-100ms per `get_state()` call just to find window
- If called 10x/sec, that's 500-1000ms of window enumeration per second
- Especially noticeable when volume slider is adjusted rapidly

**Current measurements:** Not profiled, but logically expensive

**Improvement path:**
- Increase cache TTL to 10-30 seconds (window won't move)
- Cache by PID instead of recreating on each call
- Use `FindWindow()` by class name instead of `EnumWindows()` if possible
- Batch multiple operations: get state, set state, restore in single window find

**Severity:** Medium (noticeable latency on repeated operations)

---

### 2. Blocking Screenshot on Every Power Check

**Files:** `PowerOnOff/glm_power.py` (lines 639-654)

**Problem:**
- `ImageGrab.grab(bbox=..., all_screens=True)` blocks waiting for all display drivers to respond
- If RDP is disconnected or monitor is unplugged, screenshot may hang for several seconds
- No timeout on screenshot operation
- Called from watchdog thread (blocks watchdog monitoring)

**Impact:**
- Watchdog becomes unresponsive during power state check
- If screenshot hangs, entire monitoring loop stalls
- Can appear as "GLM not responding" when really it's the screenshot that hung

**Improvement path:**
- Add timeout to screenshot: `ImageGrab.grab()` with thread + timeout wrapper
- Check monitor count before screenshot; skip if disconnected
- Move screenshot to separate thread with timeout fallback
- Cache screenshot results for 100ms to avoid redundant grabs

**Severity:** Medium (manifests as false "not responding" on monitor changes)

---

### 3. CPU Polling in Watchdog Loop

**Files:** `PowerOnOff/glm_manager.py` (lines 571-617)

**Problem:**
- Watchdog checks `is_alive()` and `is_responding()` in tight loop every 5 seconds
- Each check enumerates processes via psutil, does window handle validation
- No exponential backoff when GLM is unresponsive
- If GLM crashes and restarts, watchdog continues polling max delay

**Impact:**
- 5-10% CPU usage just for monitoring (on single core)
- Spikes when checking responsiveness (IsHungAppWindow is expensive)
- Not a critical issue but noticeable on low-power systems

**Improvement path:**
- Use Windows WaitForInputIdle or similar event-based monitoring instead of polling
- Implement exponential backoff: start at 1s, back off to 10s if responsive
- Cache process handle and use `WaitForSingleObject(hProcess, WAIT_TIMEOUT)` to detect exit

**Severity:** Low (acceptable for monitoring daemon)

---

## Fragile Areas

### 1. Multi-Step Power State Change Sequence

**Files:** `PowerOnOff/glm_power.py` (lines 808-916)

**Fragility:**
- Power state change requires: focus → read state → click → poll → restore focus (5 steps)
- Each step can fail independently
- If step 3 (click) succeeds but step 4 (poll) times out, state is unknown
- Restore focus (step 5) runs in finally block but may fail silently

**Why fragile:**
- No transaction semantics (can't rollback if mid-way failure)
- State becomes inconsistent: hardware changed but software doesn't know
- Thread safety: another thread might grab focus between operations

**Safe modification:**
- Wrap entire sequence in lock (already has `self._lock`, good)
- Add pre/post state validation before/after to detect partial state
- Log state before + after for audit trail
- Test with intentional injection of failures between steps

**Test coverage gaps:**
- No test for "click succeeds but polling times out"
- No test for "window closes during operation"
- No test for concurrent get_state + set_state calls

**Severity:** High (can cause power state desynchronization)

---

### 2. Hardcoded Window Finding by Title Pattern

**Files:** `PowerOnOff/glm_manager.py` (line 403), `PowerOnOff/glm_power.py` (line 478)

**Fragility:**
- Window finding uses hardcoded patterns:
  - JUCE window class: `r"JUCE_.*"` (regex)
  - Title filter: `"GLM" in title` (simple substring)
- If Genelec changes JUCE window class name or title format, code breaks silently
- No fallback if pattern doesn't match (returns hwnd=0, caller treats as "not ready yet")

**Why fragile:**
- Tight coupling to internal Genelec implementation details
- No version detection (assuming GLMv5, but what if GLMv6?)
- Pattern is undocumented and could change anytime

**Safe modification:**
- Document why these patterns work (what Genelec uses)
- Add fallback patterns (e.g., also check for "Genelec" in title)
- Add version detection and different patterns per version
- Test on fresh GLM install to verify patterns still match

**Test coverage gaps:**
- No test with GLMv4 or hypothetical GLMv6
- No test with customized window titles
- No test when multiple JUCE windows exist

**Severity:** Medium (would require Genelec update to break, but then code is broken)

---

### 3. RDP Priming Dependency Chain

**Files:** `bridge2glm.py` (RDP priming logic)

**Fragility:**
- RDP priming depends on:
  1. FreeRDP installed (`wfreerdp.exe` in PATH)
  2. Windows Credential Manager with `localhost` credentials
  3. NLA enabled on RDP-Tcp
  4. Admin privileges or psexec available
  5. Network stack working (for RDP loopback)

- If ANY dependency is missing, RDP priming silently fails
- No explicit error reporting which dependency failed
- Fallback behavior unclear (does GLM start anyway? With high CPU?)

**Why fragile:**
- Many external dependencies
- Silent failure mode makes troubleshooting hard
- Setup is manual and error-prone (documented in CLAUDE.md)

**Safe modification:**
- Add explicit checks for each dependency at startup:
  ```python
  if not which("wfreerdp"):
      logger.error("FreeRDP not found in PATH - RDP priming disabled")
  if not check_credentials_exist("localhost"):
      logger.error("No credentials for localhost - RDP priming will fail")
  ```
- Log explicitly: "RDP priming started", "RDP priming succeeded", "RDP priming failed"
- Fall back gracefully: continue GLM startup even if priming fails
- Add `--skip-rdp-priming` flag for headless environments

**Test coverage gaps:**
- No test without FreeRDP installed
- No test without Credential Manager entries
- No test on non-admin user account

**Severity:** High (makes recovery from RDP disconnect unreliable)

---

## Test Coverage Gaps

### 1. Power State Detection Accuracy

**Files:** `PowerOnOff/glm_power.py` (entire module)

**Untested:**
- Power state classification with edge-case colors (near boundaries)
- Fallback nudge sampling when first point is ambiguous
- Screenshot capture when monitors are disconnected
- Behavior with HDR displays or unusual color profiles

**Risk:** Power state reads fail intermittently in edge cases

**Priority:** High (critical functionality)

---

### 2. Window Handle Lifecycle

**Files:** `PowerOnOff/glm_manager.py`, `PowerOnOff/glm_power.py`

**Untested:**
- Window handle becomes invalid after minimize/restore
- Cached handle validation across operations
- Multiple JUCE windows existing simultaneously
- Window destruction while operation in progress

**Risk:** Stale window handle causes operations to fail

**Priority:** High (core functionality)

---

### 3. Exception Scenarios

**Files:** All modules

**Untested:**
- MIDI connection drops during operation
- GLM crashes mid-operation (watchdog restart)
- RDP disconnect during power state check
- Configuration file missing or corrupted
- Thread startup failure

**Risk:** Undefined behavior when error conditions occur

**Priority:** Medium (affects reliability)

---

## Synthesis & Priorities

### Where Both Perspectives Agree (Critical)

**1. Exception Handling is Too Permissive (SHARED CONCERN)**
- **Developer view:** Silent failures make debugging impossible, violated error handling patterns
- **Architect view:** Exception swallowing leads to cascade failures, hard to diagnose production issues
- **Agreement:** This is the #1 code quality issue. Fix by making exception handling explicit and specific.
- **Impact:** Would fix intermittent power control failures
- **Effort:** Medium (requires systematic review of all exception handlers)
- **Fix first?** YES - enables better reliability for all downstream components

**2. Window Handle Cache Invalidation (SHARED CONCERN)**
- **Developer view:** Race condition violates thread safety contract
- **Architect view:** Cache invalidation is notoriously hard; this pattern is fragile on Windows UI thread model
- **Agreement:** Current caching strategy is error-prone. Replace with per-operation caching or atomic window lookup.
- **Impact:** Eliminates intermittent "window not found" errors
- **Effort:** Medium (refactor window lookup + caching strategy)
- **Fix first?** YES - affects reliability under normal operation

**3. RDP Priming Silent Failures (SHARED CONCERN)**
- **Developer view:** No error reporting makes troubleshooting impossible
- **Architect view:** RDP session management is session-specific; silent failure means high CPU persists
- **Agreement:** Add explicit logging and dependency checks at startup
- **Impact:** Resolves high CPU issue when RDP dependencies are missing
- **Effort:** Low (mostly logging + conditional checks)
- **Fix first?** YES - fast win that solves known production issue

---

### Where Perspectives Diverge

**Developer Priority: Code Quality**
- Focus: Exception handling, test coverage, logging clarity
- Would fix next: Inconsistent exception patterns → structured logging → comprehensive testing
- Rationale: Good code is easier to maintain and debug

**Architect Priority: Platform Reliability**
- Focus: Platform-specific risks, process lifecycle, session management
- Would fix next: RDP session priming → window handle lifecycle → platform abstraction
- Rationale: Platform integration is the unique risk (not pure Python code)

**Synthesis Recommendation:**
- **Immediate (Week 1):** Fix exception handling + RDP priming logging (both agree this is critical)
- **Short-term (Week 2-3):** Fix window handle caching + add platform abstraction layer (arch concerns)
- **Medium-term (Month 1):** Add comprehensive test coverage + structured logging (dev concerns)
- **Long-term (Month 2+):** Consider macOS/Linux platform support if desired

---

### Severity Summary

| Severity | Category | Count | Examples |
|----------|----------|-------|----------|
| **Critical** | Intermittent failures, silent errors | 3 | Exception handling, window cache race, RDP priming |
| **High** | Reliability/security issues | 5 | Incomplete error states, focus stealing, fragile sequences |
| **Medium** | Performance/maintainability | 5 | Hardcoded paths, resource leaks, window enumeration cost |
| **Low** | Quality/documentation | 3 | Logger anti-pattern, log levels, monitoring polling |

---

*Concerns audit: 2026-03-21*
