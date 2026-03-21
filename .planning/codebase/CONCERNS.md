# Codebase Concerns

**Analysis Date:** 2026-03-21

---

## Perspective 1: Senior Developer

### SD-1: Global Mutable State — GlmController Singleton

**Issue:** `glm_controller` is a module-level singleton instantiated at the top of `bridge2glm.py` (line 407). It is mutated directly by the `midi_reader` thread (`glm_controller.power = not old_power`, line 1025) bypassing the class's own locking methods. The `_notify_state_change()` private method is also called directly from outside the class (line 1027).

- **Files:** `bridge2glm.py:407`, `bridge2glm.py:1024-1027`, `bridge2glm.py:843-844`
- **Impact:** Race condition between `midi_reader` thread direct assignment and `consumer` thread calling `update_from_midi`. The lock is bypassed for RF-remote power toggle detection, meaning a simultaneous MIDI update could see a half-updated state. Also couples bridge logic directly to controller internals.
- **Fix approach:** Route RF power detection through `glm_controller.set_power_from_midi_pattern(new_state)` — a new public method that acquires the lock and calls `_notify_state_change()` internally.

---

### SD-2: Password Exposed in Process Arguments

**Issue:** In `prime_rdp_session()`, the RDP password from Credential Manager is passed directly on the `wfreerdp` command line: `/p:` + `password` (`bridge2glm.py:564`). On Windows, command-line arguments of running processes are visible to any user with access to `WMI` or `tasklist /v`.

- **Files:** `bridge2glm.py:563-565`
- **Impact:** The RDP password for the local account is visible in process listings for the ~3–10 seconds the FreeRDP process is alive. Any process running as the same user can read it via `psutil.Process.cmdline()`.
- **Fix approach:** Use FreeRDP's `/from-stdin` flag or a temporary credential file that is immediately deleted. Alternatively, use `wfreerdp /v:localhost /u:USERNAME /cert:ignore /sec:nla` with credential manager passthrough via `/credentials-delegation:1` (no `/p:` flag) — FreeRDP picks up Credential Manager credentials automatically when no `/p:` is given and NLA is used.

---

### SD-3: MQTT Password in CLI Arguments

**Issue:** `--mqtt_pass` is accepted as a CLI argument (`config.py:149`). On Windows, process command lines are world-readable via WMI. The password also ends up in `ps` output on any process listing.

- **Files:** `config.py:149`, `bridge2glm.py:750`
- **Impact:** MQTT broker password visible in process list, shell history, and any task manager / WMI query.
- **Fix approach:** Read MQTT credentials from Windows Credential Manager (same pattern as RDP credentials) or from an environment variable. Keep `--mqtt_user` for the username but remove `--mqtt_pass` from CLI.

---

### SD-4: REST API Has No Authentication

**Issue:** The FastAPI server binds to `0.0.0.0:8080` by default with no authentication (`api/rest.py:388`). Any process or device on the local network can call `/api/power` to toggle the speakers.

- **Files:** `api/rest.py:388-468`, `config.py:139`
- **Impact:** On a home network or VM with bridged networking, any device can control the speakers. The power endpoint is especially sensitive since toggling the Genelec amplifiers repeatedly could damage them.
- **Fix approach:** Add an optional `--api_key` CLI argument. Inject a FastAPI dependency that checks `Authorization: Bearer <key>` or an `X-API-Key` header. Default to localhost-only binding (`127.0.0.1`) unless explicitly set to `0.0.0.0`.

---

### SD-5: No Tests Whatsoever

**Issue:** There are zero test files in the repository — no `tests/` directory, no `*.test.py`, no `*.spec.py`, no pytest config, no test framework in `requirements.txt`.

- **Files:** Entire codebase
- **Impact:** The power state machine (`GlmController`), acceleration logic (`AccelerationHandler`), MIDI pattern detection (the 5-message burst filter in `bridge2glm.py:972-1029`), and pixel-color classification (`GlmPowerController._classify_state`) are all untested. Regressions in any of these are invisible until runtime.
- **Fix approach:** Start with pure-logic unit tests: `AccelerationHandler.calculate_speed`, `GlmController` state machine (lock behavior, power settling timer), `_classify_state` with known RGB tuples, and the MIDI pattern filter gap logic. These require no Windows APIs.

---

### SD-6: Hardcoded GLM Executable Path

**Issue:** The GLM path `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe` is hardcoded as defaults in both `GlmManagerConfig` (`PowerOnOff/glm_manager.py:71`) and `config.py:163`. The process name `GLMv5` is also hardcoded (`glm_manager.py:72`).

- **Files:** `PowerOnOff/glm_manager.py:71-72`, `config.py:163`
- **Impact:** Every GLM version upgrade requires a code or config change. No error message distinguishes "wrong path" from "GLM not installed."
- **Fix approach:** Auto-detect via Windows Registry (`HKLM\SOFTWARE\WOW6432Node\Genelec\GLM`) or a `glob` of `C:\Program Files*\Genelec\GLM*\GLM*.exe`. Emit a clear error if not found and `--glm_path` was not specified.

---

### SD-7: Power State Initialized Optimistically to `True`

**Issue:** `GlmController.__init__` sets `self.power = True` (`bridge2glm.py:118`). If GLM is actually off when the script starts and `GlmPowerController.get_state()` fails (window not found, display issue), the controller silently keeps `power=True`.

- **Files:** `bridge2glm.py:118`, `bridge2glm.py:1321-1332`
- **Impact:** The REST API and MQTT will report `power: true` even when the speakers are physically off, until the first successful UI read or MIDI pattern. Home Assistant automations keyed on power state will malfunction.
- **Fix approach:** Initialize `self.power = None` (unknown). Reflect `"unknown"` in `get_state()` output. Only set `True`/`False` after a confirmed UI read or MIDI pattern.

---

### SD-8: `requirements.txt` Missing Several Runtime Dependencies

**Issue:** `requirements.txt` lists only `hidapi`, `mido`, `python-rtmidi`, `psutil`, `fastapi`, `uvicorn[standard]`, `paho-mqtt`. The code also imports `pywinauto`, `pillow`, `pywin32`, and `keyring` — all required for the core feature set on Windows.

- **Files:** `requirements.txt`
- **Impact:** A fresh `pip install -r requirements.txt` on a new Windows machine will fail at runtime with `ImportError` on first use of power control or RDP priming.
- **Fix approach:** Add `pywinauto`, `pillow`, `pywin32`, `keyring` to `requirements.txt`. Consider splitting into `requirements.txt` (core) and `requirements-windows.txt` (Windows-only extras), or use a `[windows]` extras_require in a `pyproject.toml`.

---

### SD-9: Stale Action Dropping is Silent to the Caller

**Issue:** The consumer silently discards queued actions older than `MAX_EVENT_AGE = 2.0s` (`bridge2glm.py:1071-1073`). REST API and MQTT callers receive a `200 OK` when they submit an action, then the action may be silently dropped if the consumer is backlogged.

- **Files:** `bridge2glm.py:1071-1073`, `bridge2glm.py:84`
- **Impact:** A REST API client sending a power command during a brief queue backup gets `{"status": "ok"}` but the command is never executed. No feedback loop exists.
- **Fix approach:** Either use a smaller `MAX_EVENT_AGE` for power commands (which are user-initiated and time-sensitive), or return a `202 Accepted` with a status token that the client can poll. At minimum, log the stale drop at WARNING level with the action type.

---

### SD-10: `_reinit_power_controller` Accesses Global `glm_controller` Directly

**Issue:** `HIDToMIDIDaemon._reinit_power_controller` directly references the module-level `glm_controller` singleton and calls its private `_notify_state_change()` method (`bridge2glm.py:843-844`). The daemon also calls `_notify_state_change()` directly from the midi_reader at line 1027.

- **Files:** `bridge2glm.py:843-844`, `bridge2glm.py:1027`
- **Impact:** Tight coupling; private API leakage. If `GlmController` is refactored, these call sites are invisible to the type checker and easy to miss.
- **Fix approach:** Expose `glm_controller.notify_power_changed(new_state)` as a public method that sets `self.power` and calls `_notify_state_change()` internally.

---

## Perspective 2: Senior Windows Desktop App Architect

### WA-1: Pixel-Color Power Detection Breaks with DPI Scaling and Themes

**Issue:** `GlmPowerController` determines power state by sampling a 9×9 pixel patch at a fixed offset from the window's right edge (`dx_from_right=28`, `dy_from_top=80` in `PowerOnOff/glm_power.py:392-394`). The color thresholds (`off_max_brightness=95`, `on_min_green=110`) are hardcoded.

- **Files:** `PowerOnOff/glm_power.py:376-403`, `PowerOnOff/glm_power.py:656-675`
- **Impact:**
  - At 125% or 150% DPI scaling (common on HiDPI displays or RDP sessions with custom resolution), the pixel offset `(right - 28, top + 80)` hits the wrong pixel — likely the window chrome or an adjacent control. The method silently returns `"unknown"` and the system fails to control power.
  - Windows High Contrast mode or any future GLM skin change breaks the RGB thresholds.
  - The fallback `nudge_x=8` shifts 8 pixels left, but this is also a pixel-distance that scales with DPI.
  - `ImageGrab.grab(all_screens=True)` uses virtual-screen coordinates; if GLM is on a secondary monitor, DPI-per-monitor awareness can cause coordinate mismatches.
- **Fix approach (near-term):** Query `GetDpiForWindow(hwnd)` at runtime and scale `dx_from_right` and `dy_from_top` proportionally. Log actual DPI on each operation.
- **Fix approach (long-term):** Switch to UI Automation (UIA) accessibility tree to find the power button element by `AutomationId` or `Name`, then query its `Toggle.ToggleState` property. This is DPI-immune and theme-immune. `pywinauto` already wraps COM UIA; use `backend="uia"` instead of `"win32"`.

---

### WA-2: `tscon` Session Hardcoded to Session 1

**Issue:** `prime_rdp_session()` calls `tscon 1 /dest:console` with session ID hardcoded to `1` (`bridge2glm.py:593-597`). The `reconnect_to_console()` function in `glm_power.py` correctly uses the dynamic session ID, but the priming function does not.

- **Files:** `bridge2glm.py:593-597`
- **Impact:** On a machine where the user's interactive session is session 2 (e.g., the machine has had multiple login/logout cycles or a service session is session 1), `tscon 1 /dest:console` reconnects the wrong session. The RDP priming "succeeds" but leaves the actual user session disconnected. GLM is running in the wrong session context.
- **Fix approach:** After killing FreeRDP, enumerate WTS sessions with `WTSEnumerateSessionsW` to find the newly-connected RDP session by state `WTSConnected`, then pass that session ID to `tscon`. Alternatively, use `get_current_session_id()` (already implemented in `glm_power.py`) before priming to know which session to reconnect.

---

### WA-3: Subprocess Handle Leak During RDP Priming on FreeRDP Hang

**Issue:** In `prime_rdp_session()`, `proc.terminate()` is called followed by `proc.wait(timeout=2)`. If FreeRDP does not terminate within 2 seconds, `proc.kill()` is called (`bridge2glm.py:584-588`). However, `proc` (the `subprocess.Popen` object) is never explicitly closed after `kill()`. On Windows, `Popen` objects hold an OS handle to the child process until the Python object is garbage-collected.

- **Files:** `bridge2glm.py:583-607`
- **Impact:** Minor handle leak per boot (since priming runs once per boot flag). However, if priming is ever triggered multiple times due to flag corruption, handles accumulate. More critically, `subprocess.run(["query", "session"], ...)` is called in a polling loop up to 20 times (`bridge2glm.py:570-578`) — each `subprocess.run` creates, waits, and returns a `CompletedProcess` whose handles are released, but only after GC. Under CPython this is fine; under PyPy it would leak.
- **Fix approach:** Use `proc` as a context manager (`with subprocess.Popen(...) as proc:`) which calls `proc.__exit__()` → `proc.communicate()` or `proc.wait()` and then closes handles. For the polling `subprocess.run` calls, they are already safe as `CompletedProcess` has no lingering OS handles under CPython.

---

### WA-4: `IsHungAppWindow` is Unreliable for Detecting JUCE/OpenGL Apps

**Issue:** `GlmManager.is_responding()` uses `ctypes.windll.user32.IsHungAppWindow(hwnd)` to detect if GLM has frozen (`glm_manager.py:252`). `IsHungAppWindow` returns true only when the window's message queue hasn't been pumped for 5 seconds (`GHND_TIMEOUT`). JUCE OpenGL apps pump their message queue normally but do all heavy work on the render thread — a stuck render thread is invisible to `IsHungAppWindow`.

- **Files:** `PowerOnOff/glm_manager.py:222-256`
- **Impact:** If GLM's OpenGL render thread hangs (e.g., due to the display driver issue described in `CLAUDE.md`), `IsHungAppWindow` returns false (not hung) because the message queue is still pumped. The watchdog never detects the freeze and never restarts GLM.
- **Fix approach:** Supplement `IsHungAppWindow` with CPU usage monitoring: if GLM's CPU usage is at or near 100% for `max_non_responsive * watchdog_interval` seconds, treat it as hung. `psutil.Process.cpu_percent()` is already available. Alternatively, send `WM_NULL` with `SendMessageTimeout(hwnd, WM_NULL, 0, 0, SMTO_ABORTIFHUNG, 5000, ...)` — this is more reliable than `IsHungAppWindow`.

---

### WA-5: UI Automation Runs in Consumer Thread — Contends with MIDI Processing

**Issue:** `_handle_power_action()` runs UI automation (window restore, Alt-key simulation, screenshot grab, mouse click) directly inside the `consumer` thread (`bridge2glm.py:1128-1182`). This blocks the consumer for up to `POWER_SETTLING_TIME (2s) + verify_timeout (3s) = 5s` while MIDI messages continue arriving.

- **Files:** `bridge2glm.py:1128-1182`, `PowerOnOff/glm_power.py:808-916`
- **Impact:** During a 5-second power operation, the consumer queue accumulates HID and REST events. After the operation completes, stale events (>2s) are dropped and fresh ones are processed. The `QUEUE_MAX_SIZE=100` limit acts as a safety valve, but the fundamental design is that a single slow operation blocks all other command processing.
- **Fix approach:** Move UI automation to a dedicated `PowerControlThread`. Submit power actions to a separate single-slot power queue. The consumer thread enqueues power actions and immediately resumes processing other actions. The power thread handles the slow UI operation and updates `glm_controller` state when done.

---

### WA-6: `SetForegroundWindow` via Alt-key Trick is Fragile

**Issue:** `GlmPowerController._ensure_foreground()` uses a well-known hack: synthesizing Alt key events to allow `SetForegroundWindow` to succeed from a background process (`glm_power.py:527-538`). It also synthesizes Escape before Alt to dismiss overlays.

- **Files:** `PowerOnOff/glm_power.py:503-556`
- **Impact:**
  - Sending VK_ESCAPE (`0x1B`) dismisses whatever dialog or application happens to have focus at the moment of the power command (e.g., a browser address bar, a game prompt). This is a user-visible side effect.
  - The Alt-key trick is deliberately exploiting a Windows bug/quirk documented to be unreliable. Microsoft's UIPI (User Interface Privilege Isolation) can block it.
  - If the user is typing when a power command arrives, the injected keystrokes corrupt the input.
- **Fix approach:** Use `AllowSetForegroundWindow(ASFW_ANY)` or `LockSetForegroundWindow(LSFW_UNLOCK)` from a thread with appropriate privileges, without synthetic key injection. Even better: don't bring GLM to foreground at all. Use `PrintWindow(hwnd, hdc, PW_RENDERFULLCONTENT)` to capture the window contents without focus, then use `SendMessage` (not `PostMessage`) for the click to avoid needing foreground status.

---

### WA-7: `ImageGrab.grab` Captures Entire Screen Region — Breaks Under RDP

**Issue:** `GlmPowerController._get_patch_median_rgb()` uses `PIL.ImageGrab.grab(bbox=..., all_screens=True)` to capture a pixel patch (`glm_power.py:647`). Under RDP sessions, `ImageGrab.grab` often returns a black rectangle because the display surface is a remote frame buffer and screen capture APIs don't work across session boundaries.

- **Files:** `PowerOnOff/glm_power.py:639-654`
- **Impact:** When the bridge is accessed via RDP and the user runs a power command before the session is fully reconnected to console (or if `ensure_session_connected` returns `True` prematurely), `ImageGrab.grab` returns all-black pixels. `_classify_state` returns `"unknown"`. The power command is effectively a no-op with no error to the user.
- **Current mitigation:** `ensure_session_connected` checks WTS session state before UI automation. This mitigates but does not eliminate the risk.
- **Fix approach:** After calling `ensure_session_connected`, validate that `ImageGrab.grab` returns a non-black frame before attempting classification. Alternatively, use `win32gui.GetWindowDC(hwnd)` + `BitBlt` to capture directly from the window's device context — this works regardless of session/RDP state as long as the window is not occluded.

---

### WA-8: `wtsapi32.WTSEnumerateSessionsW` Memory Not Always Freed

**Issue:** In `is_session_disconnected()`, `WTSFreeMemory(ppSessionInfo)` is called in a `finally` block (`glm_power.py:149`). However, the call to `WTSEnumerateSessionsW` passes `ctypes.byref(ppSessionInfo)` — if the function fails and leaves `ppSessionInfo` as a null pointer, `WTSFreeMemory(NULL)` is called. On Windows, `WTSFreeMemory(NULL)` is documented as a no-op, so this is safe. But the outer `try/except Exception: return False` at line 154 swallows all errors including access violations, making debugging difficult.

- **Files:** `PowerOnOff/glm_power.py:128-155`
- **Impact:** Low risk of leak (Windows handles NULL gracefully), but any WTS API failure is silently swallowed. If `wtsapi32.dll` is not loaded (unusual but possible in Session 0), the call raises `AttributeError` which is caught by `except Exception` and returns `False` — causing `ensure_session_connected` to think the session is connected when it cannot be determined.
- **Fix approach:** Catch `AttributeError` and `OSError` explicitly. Log the specific error. Add a check that `ppSessionInfo` is non-null before passing to `WTSFreeMemory`.

---

### WA-9: `tscon` Requires Elevation — Failure Mode is Silent

**Issue:** `reconnect_to_console()` first tries `tscon` directly, then falls back to `psexec -s` (`glm_power.py:217-252`). If neither works, it logs a warning and returns `False`. The caller (`ensure_session_connected`) returns `False`. The caller of that (`_handle_power_action`) logs an error and returns early without performing the power operation.

- **Files:** `PowerOnOff/glm_power.py:185-262`, `bridge2glm.py:1156-1160`
- **Impact:** If the script is not running as Administrator and `psexec` is not installed, all UI-automation-based power commands silently fail after logging one warning. The REST API returns `200 OK` for the submitted action (because it was enqueued successfully) but the operation never executes.
- **Fix approach:** Emit a startup warning (not a per-command warning) if the privilege check fails so the operator knows at boot time. Return a proper error from `_handle_power_action` that causes the REST API to return `503` rather than the optimistic `200 OK` from queue submission. Document the elevation requirement prominently.

---

### WA-10: No Windows Event Log Integration

**Issue:** All logging goes to a rotating file and console. Critical events (GLM restart, power state change failure, RDP session reconnect failure) are not written to the Windows Application Event Log.

- **Files:** `logging_setup.py`, `bridge2glm.py:673-736`
- **Impact:** When the script is running as a Windows Service or Task Scheduler job (headless), the console is not visible and the log file location may not be obvious. System administrators have no way to monitor health via standard Windows tools (`eventvwr.exe`, `wevtutil`, `Get-EventLog`).
- **Fix approach:** Add a `logging.handlers.NTEventLogHandler` for CRITICAL and ERROR levels. This is a one-line addition to `setup_logging`. The handler writes to the Windows Application Event Log under a configurable source name (e.g., `"GLMBridge"`).

---

### WA-11: No Job Object — Orphaned GLM Process on Bridge Crash

**Issue:** GLM is launched via `subprocess.Popen([self.config.glm_path], ...)` in `GlmManager._start_glm()` (`glm_manager.py:336-342`). No Windows Job Object is created. If the Python bridge process crashes (not a clean `stop()`), GLM continues running with no parent.

- **Files:** `PowerOnOff/glm_manager.py:334-344`
- **Impact:** After a bridge crash, GLM remains running but unmonitored. On the next bridge start, `_find_glm_process()` detects the existing GLM and logs "Reusing" (`glm_manager.py:330`), but the watchdog starts fresh. If GLM's window handle has changed since it was "reused," `is_responding()` may silently use a stale handle forever.
- **Fix approach:** Use `CreateJobObject` + `AssignProcessToJobObject` with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` so GLM is automatically killed when the bridge process terminates for any reason. Alternatively, since GLM is a user-facing audio app that the user may want to persist, at minimum detect the "reuse" case and refresh the window handle cache immediately.

---

### WA-12: Thread Priority Escalation Without Reversion

**Issue:** `hid_reader` thread is set to `THREAD_PRIORITY_HIGHEST` and `consumer` / `midi_reader` to `THREAD_PRIORITY_ABOVE_NORMAL` (`bridge2glm.py:883`, `bridge2glm.py:948`, `bridge2glm.py:1056`). The main process is also escalated to `ABOVE_NORMAL_PRIORITY_CLASS` (`bridge2glm.py:413`). There is no mechanism to revert priority if, for example, the HID device is not found and the thread is sleeping in a retry loop.

- **Files:** `bridge2glm.py:410-416`, `bridge2glm.py:883`, `bridge2glm.py:948`, `bridge2glm.py:1056`
- **Impact:** The HID reader thread at `THREAD_PRIORITY_HIGHEST` sleeping in a 2-second retry loop still yields CPU normally when sleeping, so this is low risk in practice. However, if any tight loop exists in those threads (e.g., an exception that doesn't sleep), HIGHEST-priority spinning would starve all normal-priority threads. The Windows scheduler's starvation prevention mitigates this after ~4s.
- **Fix approach:** Lower HID reader to `ABOVE_NORMAL` (same as consumer) during the retry/sleep phase. Only escalate to HIGHEST when a device is connected and actively reading. Use `set_current_thread_priority` to lower before `time.sleep(RETRY_DELAY)` in the reconnect path.

---

## Synthesis & Priorities

### Where Both Perspectives Agree (Critical)

| Issue | SD ref | WA ref | Why it matters |
|-------|--------|--------|----------------|
| Power state detection is fragile / unreliable | SD-7 | WA-1, WA-7 | The entire power control feature is built on pixel sampling that breaks under DPI scaling, RDP, and theme changes. Both perspectives identify this as the most fragile subsystem. |
| UI automation blocks the consumer thread | SD-9 | WA-5 | A 5-second blocking operation in the same thread that handles HID and REST events is a design problem both perspectives flag. |
| `tscon` failure is silently swallowed after queue accept | SD-9 | WA-9 | REST API returns `200 OK` when enqueuing, but the action may never execute. Both perspectives flag the false success signal. |

### What the Senior Developer Would Fix First

1. **SD-5 (No Tests)** — Every other fix is harder to validate without a test harness. Start with unit tests for `AccelerationHandler`, `GlmController` state machine, and `_classify_state`.
2. **SD-2 / SD-3 (Credentials in CLI/process args)** — Security issues with direct customer impact. Quick wins.
3. **SD-1 (Global singleton + lock bypass)** — The `glm_controller.power = not old_power` direct assignment is a data race. Fix before any other state machine work.

### What the Windows Architect Would Fix First

1. **WA-1 (DPI-aware pixel detection)** — The pixel sampling approach is fundamentally fragile. The highest-leverage fix is switching to `GetDpiForWindow` scaling in the short term and UIA accessibility API in the medium term. This eliminates WA-7 (ImageGrab under RDP) simultaneously.
2. **WA-2 (tscon session 1 hardcoded)** — High risk of reconnecting the wrong session; one-line fix with significant reliability improvement.
3. **WA-5 (UI automation in consumer thread)** — Architectural change: dedicate a `PowerControlThread` to avoid blocking MIDI/HID processing during power transitions.

### Disagreement

The Senior Developer would defer WA-10 (Windows Event Log) and WA-11 (Job Objects) as nice-to-haves for a personal/home project. The Windows Architect considers them prerequisites for reliable headless operation, especially since the application is designed to run unattended on a VM.

The Windows Architect would accept the current pixel-based approach with DPI scaling as a pragmatic near-term fix (WA-1). The Senior Developer would push for eliminating pixel-based detection entirely in favor of MIDI state (already available for mute/dim/volume) — noting that if GLM's power button has no MIDI feedback, a state-verified click via `SendMessage` to the known HWND is more reliable than a pixel snapshot.

---

## Tech Debt Summary

| ID | Area | Severity | Effort |
|----|------|----------|--------|
| SD-1 | Global state / lock bypass | High | Low |
| SD-2 | Password in process args | High | Low |
| SD-3 | MQTT password in CLI | Medium | Low |
| SD-4 | No REST API auth | Medium | Medium |
| SD-5 | No tests | High | High |
| SD-6 | Hardcoded GLM path | Low | Low |
| SD-7 | Power state init optimistic | Medium | Low |
| SD-8 | Incomplete requirements.txt | Medium | Low |
| SD-9 | False `200 OK` on stale drop | Medium | Medium |
| SD-10 | Private method leakage | Low | Low |
| WA-1 | DPI-fragile pixel detection | High | Medium |
| WA-2 | tscon session ID hardcoded | High | Low |
| WA-3 | Subprocess handle leak | Low | Low |
| WA-4 | IsHungAppWindow unreliable | Medium | Medium |
| WA-5 | UI automation in consumer thread | Medium | High |
| WA-6 | SetForegroundWindow via Alt hack | Medium | Medium |
| WA-7 | ImageGrab fails under RDP | High | Medium |
| WA-8 | WTS memory / error swallow | Low | Low |
| WA-9 | tscon privilege failure silent | Medium | Low |
| WA-10 | No Windows Event Log | Low | Low |
| WA-11 | No Job Object for GLM | Low | Medium |
| WA-12 | Thread priority not reverted | Low | Low |

---

*Concerns audit: 2026-03-21*
