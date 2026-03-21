# VOL20toGenelecGLM — Codebase Analysis Summary

**Analysis Date:** 2026-03-21 | **Version:** 3.2.22 | **Perspectives:** Senior Developer + Senior Windows Desktop App Architect

---

## What This Project Is

A Python 3.13 Windows application that bridges a **Fosi Audio VOL20 USB knob** to **Genelec GLM v5 speaker software** via MIDI. It adds remote control via REST API + MQTT (Home Assistant), and manages GLM's process lifecycle with a watchdog. Power control uses Win32 UI automation (pixel sampling + synthesized mouse clicks) because GLM's MIDI power command (CC 28) is toggle-only with no state feedback.

---

## 1. STACK.md — Technology

**Core:** Python 3.13, no build system, no virtual environment, launched as `python bridge2glm.py [args]`

**Key dependencies:**
| Layer | Libraries |
|-------|-----------|
| Hardware | `hidapi` (USB HID), `mido` + `python-rtmidi` (MIDI via WinMM) |
| APIs | `FastAPI` + `uvicorn` (REST/WebSocket on port 8080), `paho-mqtt` v2 (Home Assistant) |
| Win32 automation | `pywinauto` (JUCE window finding), `Pillow` (pixel capture), `pywin32` (mouse/keyboard synthesis, thread priority) |
| System | `psutil` (process management, CPU gating), `ctypes` (Win32 kernel/user32/wtsapi32 calls) |

**Dev says:** No version pinning in `requirements.txt` — a fresh install could get incompatible paho-mqtt 1.x. No tests, no linter, no formatter config. Mixed sync/async architecture (threaded main + async FastAPI in a background thread) works but is fragile.

**Windows Architect says:** Thread priority management via `win32process.SetThreadPriority` is correctly applied (HID at HIGHEST, logging at IDLE). No Windows Service wrapper (correct — UI automation requires interactive session). No Job Object wrapping GLM process. No Windows Event Log integration. Credential Manager access pattern for RDP priming is a deliberate and documented security trade-off.

---

## 2. INTEGRATIONS.md — External Systems

**5 integration layers:**

1. **USB HID** — VOL20 knob (VID 0x07d7), polled in dedicated thread, keycodes mapped to actions
2. **MIDI** — Bidirectional CC messages to GLM via virtual ports ("GLMMIDI 1" / "GLMOUT 1"). CC 20=volume, 23=mute, 24=dim, 28=power toggle. Power detection via 5-message burst pattern matching (MUTE,VOL,DIM,MUTE,VOL within 150ms)
3. **REST API** — FastAPI on 8080: `/api/state`, `/api/volume`, `/api/power`, `/ws/state` WebSocket. No auth. Serves web UI from `web/index.html`
4. **MQTT** — Home Assistant discovery, publishes state to `glm/state`, subscribes to `glm/set/{volume,mute,dim,power}`. LWT for availability
5. **Windows system** — WTS session management (detect/reconnect disconnected RDP), pixel sampling for power button state (median RGB of 9x9 patch), Alt-key trick for `SetForegroundWindow`, `IsHungAppWindow` for GLM hang detection, RDP priming via FreeRDP once per boot

**Dev says:** No TLS on REST or MQTT. MQTT password visible in process list via `--mqtt_pass` CLI arg. WebSocket error suppression pattern is fragile. Power state is best-effort when pywinauto is unavailable.

**Windows Architect says:** JUCE window class name `JUCE_.*` is an implementation detail that could break on GLM update. Pixel offsets (28px from right, 80px from top) break with DPI scaling. Session 0 isolation prevents running as a Windows Service. `mouse_event` sends to system input queue (not directly to window) — focus management before clicking is essential. `tscon` requires Admin or psexec for elevation.

---

## 3. ARCHITECTURE.md — Design

**Pattern:** Multi-threaded Producer/Consumer bridge with UI Automation sidecar

**6 threads at runtime:**
| Thread | Priority | Role |
|--------|----------|------|
| `HIDReaderThread` | HIGHEST | Polls VOL20 USB HID, maps keycodes to actions, applies acceleration |
| `MIDIReaderThread` | ABOVE_NORMAL | Reads GLM MIDI feedback, updates state, detects power pattern |
| `ConsumerThread` | ABOVE_NORMAL | Dequeues actions, sends MIDI commands, runs UI automation for power |
| `APIServerThread` | Normal (daemon) | FastAPI + WebSocket server with own asyncio event loop |
| `GLMWatchdog` | Normal (daemon) | Monitors GLM process health via psutil + IsHungAppWindow |
| `LoggingThread` | IDLE (non-daemon) | Async log queue flush, ensures final messages on shutdown |

**Data flow:** All inputs (HID, REST, MQTT) produce `GlmAction` frozen dataclasses → single bounded `queue.Queue` (max 100) → consumer dispatches to MIDI or UI automation → `GlmController` state updated → callbacks fire to REST WebSocket + MQTT

**Key abstractions:**
- `GlmAction` union type (command pattern) — `SetVolume`, `AdjustVolume`, `SetMute`, `SetDim`, `SetPower`
- `GlmController` — observable state model with optimistic pending-volume tracking and power settling state machine
- `GlmPowerController` — pixel-sampling UI automation with session guards
- `GlmManager` — GLM process lifecycle with CPU gating, window handle stabilization, and hung-app watchdog

**Dev says:** Clean command pattern, good seam for extensibility. `GlmController` singleton as module-level global couples everything. `bridge2glm.py` at 1500 lines handles too many responsibilities. No unit tests for any of the stateful logic.

**Windows Architect says:** Thread model is correct — Python OS threads with proper Win32 priority APIs. No COM apartment concerns (pywinauto uses Win32 directly, not COM). Cross-thread asyncio bridge via `run_coroutine_threadsafe` is the correct pattern. No Job Object wraps GLM. MIDI service restart requires undocumented elevation.

---

## 4. STRUCTURE.md — Layout

```
bridge2glm.py          (1492 lines) — Monolith: entry point + GlmController + all threads
PowerOnOff/            — Win32 power control + process management (conditional import)
  glm_power.py         (1012 lines) — Pixel sampling, mouse synthesis, session guards
  glm_manager.py       (655 lines)  — Process lifecycle, watchdog, CPU gating
  exceptions.py        — Rich exception hierarchy with diagnostic payloads
api/                   — REST + MQTT (take queue + controller as constructor args)
glm_core/              — Pure domain actions (frozen dataclasses, zero dependencies)
web/                   — Single-page web UI (inline HTML+CSS+JS)
+ 5 root modules       — config, midi_constants, acceleration, logging_setup, retry_logger
```

**Adding new features:** Action in `glm_core/` → CC constant in `midi_constants.py` → handler in consumer → endpoint in `api/` → MQTT topic handler. Clean extension pattern.

**Dev says:** Good module separation. `bridge2glm.py` should be split — extract `GlmController` to `glm_core/controller.py`.

**Windows Architect says:** Log files write to script directory — fails silently if run from `Program Files`. `move_mesa_files.bat` is a deployment concern for headless VMs without hardware OpenGL. No Registry usage; all config is CLI args + Credential Manager + flag file.

---

## 5. CONVENTIONS.md — Code Quality

**Naming:** Consistent throughout — snake_case files/functions, PascalCase classes, SCREAMING_SNAKE constants, `_` prefix for private, `is_`/`has_` for boolean queries, `Config` suffix for config dataclasses, `Error` suffix for exceptions.

**Type hints:** Comprehensive on all public methods. `Optional[X]` style (Python 3.9 compat). `Literal` for constrained strings. No mypy/pyright enforcement.

**Error handling:** Custom exception hierarchy (`GlmPowerError` base) with diagnostic payloads (`rgb`, `point`, `desired`, `actual`). Conditional imports with `HAS_DEPS` flags for graceful degradation. `SmartRetryLogger` for throttled retry-loop messages.

**Logging:** Async queue-based (`QueueHandler` + `QueueListener`). Format includes thread name, module, function, line number. Rotating file (4MB x 5 backups). Smart retry logger with exponential milestones (2s, 10s, 1m, 10m, 1h, 1d).

**Dev says:** Strong conventions. `LOG_FORMAT` duplicated in two files (DRY violation). `bridge2glm.py` violates Single Responsibility. Some inline imports inside function bodies. `can_accept_command` and `can_accept_power_command` duplicate time-elapsed logic.

**Windows Architect says:** Win32 API usage is mostly correct (proper ctypes structures, correct calling conventions, Alt-key trick is the standard workaround). Issues: no `GetLastError()` checking after Win32 calls, no explicit COM apartment initialization, `mouse_event`/`keybd_event` use legacy API (should use `SendInput`), no `is_console_session()` guard before `ImageGrab.grab`.

---

## 6. TESTING.md — Test Coverage

**Current state: Zero tests.** No framework, no test files, no coverage measurement.

**Testable without Windows (high ROI, start here):**
- `AccelerationHandler.calculate_speed` — pure function of time + state
- `SmartRetryLogger.should_log` — deterministic with mocked time
- `config.py` validation functions — pure, no dependencies
- `GlmController` state machine — `update_from_midi`, `can_accept_command`, power settling transitions
- `_classify_state` — pure RGB → "on"/"off"/"unknown" classification

**Requires mocking (medium effort):**
- `GlmPowerController` — mock pywinauto Desktop, ImageGrab, win32api, ctypes.windll
- `GlmManager` — mock psutil, subprocess, ctypes.windll

**Realistic coverage targets:** 100% on pure logic, >80% on mocked Win32 code, 60-70% overall

**Dev says:** Zero test coverage is the single largest quality risk. Start with pure-logic unit tests. Move `GlmController` out of `bridge2glm.py` to make it importable without side effects.

**Windows Architect says:** CI on headless Windows Server will fail `ImageGrab.grab` — must mock. Mock ctypes return values as integers (not bools) to match Win32 `BOOL` semantics. `WTSEnumerateSessionsW` may not be available on Windows Home SKUs.

---

## 7. CONCERNS.md — Issues & Priorities

### 22 concerns identified (10 Senior Dev, 12 Windows Architect)

#### Critical Issues (both agree)

| ID | Issue | Impact | Fix Effort |
|----|-------|--------|------------|
| **SD-1** | `glm_controller.power` set directly bypassing lock in MIDI reader thread | Data race between threads | Low |
| **SD-2** | RDP password passed on `wfreerdp` command line, visible in process list | Credential exposure | Low |
| **SD-5** | Zero test coverage for complex stateful logic | Invisible regressions | High |
| **WA-1** | Pixel detection breaks at 125%/150% DPI scaling | Power control fails silently | Medium |
| **WA-2** | `tscon 1` hardcoded — reconnects wrong session on multi-session machines | RDP priming fails | Low |
| **WA-7** | `ImageGrab.grab` returns black under RDP — power reads as "unknown" | Power commands become no-ops | Medium |

#### High-Priority Issues

| ID | Issue | Fix Effort |
|----|-------|------------|
| SD-3 | MQTT password in CLI args (visible in process list) | Low |
| SD-4 | REST API on 0.0.0.0 with no auth | Medium |
| SD-7 | Power state initialized optimistically to `True` (should be `None`/unknown) | Low |
| SD-8 | `requirements.txt` missing pywinauto, pillow, pywin32 | Low |
| WA-4 | `IsHungAppWindow` doesn't detect JUCE/OpenGL render thread hangs | Medium |
| WA-5 | UI automation blocks consumer thread for up to 5 seconds | High |
| WA-9 | `tscon` privilege failure silently drops power commands | Low |

#### Medium/Low Issues

| ID | Issue | Fix Effort |
|----|-------|------------|
| SD-6 | Hardcoded GLM path `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe` | Low |
| SD-9 | REST returns 200 OK but action may be silently dropped if stale | Medium |
| SD-10 | Private `_notify_state_change()` called from outside the class | Low |
| WA-3 | Subprocess handle leak during RDP priming if FreeRDP hangs | Low |
| WA-6 | Alt-key trick sends VK_ESCAPE that can dismiss user's active dialog | Medium |
| WA-8 | WTS API errors silently swallowed (catch-all `except Exception`) | Low |
| WA-10 | No Windows Event Log — headless monitoring impossible | Low |
| WA-11 | No Job Object — GLM orphaned if bridge crashes | Medium |
| WA-12 | Thread priority stays at HIGHEST even during retry/sleep loops | Low |

### What Each Expert Would Fix First

**Senior Developer priority:**
1. Add unit tests (SD-5) — everything else is harder to validate without tests
2. Fix credential exposure (SD-2, SD-3) — quick security wins
3. Fix lock bypass race condition (SD-1) — data race in the state machine

**Windows Architect priority:**
1. DPI-aware pixel detection (WA-1) — or switch to UIA accessibility API long-term
2. Fix hardcoded `tscon 1` (WA-2) — one-line fix with high reliability impact
3. Separate power control to dedicated thread (WA-5) — unblock consumer during 5s power operations

**Where they disagree:**
- Dev sees Event Log (WA-10) and Job Objects (WA-11) as nice-to-haves for a home project
- Architect sees them as prerequisites for reliable headless VM operation
- Dev wants to eliminate pixel detection entirely in favor of MIDI state
- Architect accepts pixel detection with DPI scaling as pragmatic near-term fix

---

## Quick Reference: Key Files

| What | Where |
|------|-------|
| Entry point | `bridge2glm.py:1410` |
| State machine | `bridge2glm.py:110-403` (GlmController) |
| HID input | `bridge2glm.py` HIDReaderThread |
| MIDI communication | `bridge2glm.py` MIDIReaderThread + consumer |
| Power pixel sampling | `PowerOnOff/glm_power.py:639-675` |
| Power color thresholds | `PowerOnOff/glm_power.py:376-403` (GlmPowerConfig) |
| GLM watchdog | `PowerOnOff/glm_manager.py:571-617` |
| REST API | `api/rest.py` |
| MQTT client | `api/mqtt.py` |
| Domain actions | `glm_core/actions.py` |
| MIDI CC numbers | `midi_constants.py:45-60` |
| CLI config | `config.py:parse_arguments()` |
| Acceleration curve | `acceleration.py` |
| Retry logging | `retry_logger.py` |

---

*Full analysis documents: `.planning/codebase/{STACK,INTEGRATIONS,ARCHITECTURE,STRUCTURE,CONVENTIONS,TESTING,CONCERNS}.md`*
