# Technology Stack

**Analysis Date:** 2026-03-21

## Languages

**Primary:**
- Python 3.x - Core application, HID-to-MIDI bridge, REST API, MQTT client, process management, power control via UI automation

**Secondary:**
- None (Windows-native APIs via ctypes, pywinauto for UI automation)

## Runtime

**Environment:**
- Windows (primary deployment target with full feature support)
- macOS/Linux (partial support - HID and MIDI work, Windows-specific power control unavailable)

**Package Manager:**
- pip (Python package management)
- Lockfile: `requirements.txt` (present, pinned versions not enforced)

## Frameworks

**Core:**
- hidapi 0.x - Low-level HID device communication (USB knob input)
- mido 1.x - MIDI message abstraction and sequencing
- python-rtmidi 1.x - Real-time MIDI I/O backend

**API & Integration:**
- FastAPI (latest stable) - REST API with WebSocket support for real-time state updates (Phase 3)
- uvicorn[standard] - ASGI server for FastAPI
- paho-mqtt 1.x - MQTT client for Home Assistant integration (Phase 5)

**System Management:**
- psutil (latest stable) - Process and CPU monitoring for watchdog and CPU gating

**Testing:**
- None detected - no test framework configured

**Build/Dev:**
- None detected - direct Python execution (no build step)

## Key Dependencies

**Critical - Hardware Interface:**
- `hidapi` - Direct USB HID access to Fosi Audio VOL20 knob (VID: 0x07d7, PID: 0x0000)
- `mido` + `python-rtmidi` - MIDI output to Genelec GLM software via virtual MIDI ports ("GLMMIDI 1" / "GLMOUT 1")

**Critical - Windows Automation (optional):**
- `pywinauto` - UI automation for pixel-based power button state reading and clicking (fallback when MIDI insufficient)
- `pillow (PIL)` - Screenshot capture for power button state detection (RGB value sampling)
- `pywin32` - Windows API access (session management, window manipulation, process priorities)

**Infrastructure:**
- `psutil` - CPU monitoring for pre-startup idle gating, process lifecycle tracking
- `fastapi` + `uvicorn` - HTTP/WebSocket server for remote control
- `paho-mqtt` - MQTT message publishing/subscription for Home Assistant discovery

**System Integration:**
- `ctypes` (stdlib) - Direct Windows kernel32 API calls (session ID detection, RDP status checking)
- `win32api`, `win32process`, `win32con` (from pywin32) - Thread priorities (ABOVE_NORMAL, HIGHEST)

## Configuration

**Environment:**
- Command-line argument parsing via `config.py:parse_arguments()` for all operational parameters
- No `.env` file detected - credentials and secrets passed via CLI or Windows Credential Manager

**Key Configuration Parameters:**
- MIDI channels: `--midi_in_channel` (default "GLMMIDI 1"), `--midi_out_channel` (default "GLMOUT 1")
- HID device: `--device` (default 0x07d7:0x0000 for Fosi VOL20)
- Volume acceleration: `--volume_increases_list`, `--click_times` for adaptive knob response
- REST API: `--api_port` (default 8080, set to 0 to disable)
- MQTT: `--mqtt_broker`, `--mqtt_port`, `--mqtt_user`, `--mqtt_pass`, `--mqtt_topic`, `--mqtt_ha_discovery`
- GLM management: `--glm_manager`, `--glm_path`, `--glm_cpu_gating`
- Logging: `--log_level` (DEBUG/INFO/NONE), `--log_file_name`

**Build:**
- No build configuration - direct Python execution via `python bridge2glm.py [args]`
- Logging configured via `logging_setup.py` with rotating file handlers and async queue logging

## Platform Requirements

**Development:**
- Python 3.8+
- Windows: Full feature set (HID, MIDI, MAPI UI automation, process management, RDP priming)
- macOS: HID and MIDI work (pywinauto/pywin32 not available)
- Linux: HID and MIDI work (pywinauto/pywin32 not available)

**Production (Windows):**
- Windows 10/11 (tested on Windows for RDP session handling, display automation)
- FreeRDP (`wfreerdp.exe`) - For RDP session priming to prevent high CPU after RDP disconnect
- Windows Credential Manager - Stores RDP credentials securely (accessed via cmdkey)
- Genelec GLM software with MIDI support enabled
- Fosi Audio VOL20 USB knob (or compatible HID device via `--device` parameter)

**Runtime Dependencies:**
- MIDI virtual ports: GLM exposes "GLMMIDI 1" (input) and "GLMOUT 1" (output) by default
- HID device: Fosi VOL20 must be connected and recognized by Windows HID stack
- RDP: Only priming required on startup; script includes fallback for console-only sessions

## Security & Dependency Management

**Vulnerability Considerations:**
- `pywinauto` - Mirrors desktop UI; security depends on GLM window title matching (deterministic)
- `paho-mqtt` - Username/password passed via CLI (can be exposed in process args; prefer env vars)
- `pywin32` - Requires admin elevation for RDP priming (session context verification)
- `ctypes` - Direct kernel API calls; validates session IDs before RDP priming

**Dependency Stability:**
- `hidapi`, `mido`, `python-rtmidi` - Stable, minimal maintenance
- `psutil` - Actively maintained, platform abstractions well-tested
- `FastAPI` - Rapid updates; uvicorn[standard] adds WebSocket support
- `paho-mqtt` - Stable; Home Assistant integrations mature
- `pywinauto` - Slower maintenance; UI automation inherently fragile across Windows versions

**Version Pinning:**
- `requirements.txt` lists packages without version constraints (e.g., `hidapi` not `hidapi==0.14.0`)
- Recommendation: Consider pinning major versions or using `requirements-lock.txt` for reproducible builds

---

## Dual Perspective Analysis

### Senior Developer Perspective

**Strengths:**
- Clean dependency separation: HID/MIDI (hardware), FastAPI (REST), paho-mqtt (messaging), psutil (monitoring)
- Conditional imports handle platform differences gracefully (Windows-specific power control optional)
- Argument parsing with validation (`config.py`) provides safe CLI interface
- Async logging via queue handlers prevents thread blocking in real-time loops

**Concerns:**
- No version pinning in `requirements.txt` risks version skew and reproducibility issues
- No test framework; untested code paths in power control, MQTT, and RDP priming
- MQTT credentials passed via CLI (visible in `ps` output); should use environment variables or encrypted config
- Direct `ctypes` and `win32api` calls increase Windows-specific fragility; pywin32 version conflicts possible
- No dependency lock file (`pip freeze` output) for production deployment

**Recommendations:**
- Generate `requirements-lock.txt` with pinned versions and hashes for production
- Introduce pytest and mock hardware/MIDI interfaces for testing
- Move secrets to environment variables with fallback to Credential Manager (already done for RDP)
- Document Windows version compatibility matrix for pywinauto and pywin32

### Senior macOS App Architect Perspective

**macOS/Darwin Native Alternatives:**
- **HID (current: hidapi via ctypes)** → Use native `IOKit` framework via PyObjC for HID enumeration/reading
  - Better integration with macOS permission model (Input Monitoring may be required)
  - Eliminates dependency on platform-specific ctypes bindings

- **MIDI (current: mido + python-rtmidi)** → Use native `CoreMIDI` via PyObjC
  - Native virtual port creation (MIDIClientCreateVirtualDestination)
  - Eliminates RtMidi jni bloat; native CoreAudio integration

- **UI Automation (current: pywinauto + PIL + win32api)** → Not needed on macOS
  - Genelec GLM likely uses AppleScript or native macOS APIs
  - Use `py-applescript` or native `EventKit` for accessibility

- **Process Management (current: psutil + pywin32 priorities)** → Use native `launchd`
  - More elegant than watchdog threads; integrates with macOS system
  - Priority via `nice`/`renice` or QoS classes in plist
  - Auto-restart on crash without polling

- **RDP Session Handling (current: Windows-specific ctypes)** → Not applicable; use native screen sharing
  - Use `Quartz` framework for display detection (wake from sleep, display sleep)
  - No RDP priming needed; AppleScript or automation handles window state

**Cross-Platform Architecture Problem:**
The codebase assumes Windows-centric deployment. Proper macOS support requires:
1. Abstraction layer for UI automation (Interface protocol with Windows/macOS implementations)
2. Native HID/MIDI implementations (PyObjC-based on macOS, ctypes-based on Windows)
3. Process management strategy per platform (launchd vs. watchdog thread)

**Hybrid Approach (Recommended):**
- Core bridge logic (HID→MIDI translation) stays platform-agnostic
- Platform-specific adapters:
  - `PowerOnOff/windows_controller.py` - Current pywinauto/pywin32 code
  - `PowerOnOff/macos_controller.py` - PyObjC + native APIs
  - Platform factory in `PowerOnOff/__init__.py` to select implementation

**Conflict Points:**
- **pywinauto fragility on Windows vs. PyObjC maturity on macOS**: Windows automation is inherently fragile (window titles, pixel coordinates); macOS Accessibility APIs more stable
- **RDP priming (Windows-only) vs. native screen sleep handling (macOS)**: Fundamentally different problems; abstraction layer needed
- **Win32 process priorities vs. launchd scheduling**: Different scheduling models; Win32 thread priorities don't map cleanly to macOS QoS classes

---

*Stack analysis: 2026-03-21*
