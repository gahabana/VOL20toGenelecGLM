# Claude Code Guidelines for VOL20toGenelecGLM

## Workflow Preferences

### Before Making Changes
1. **Propose first, implement second**: Always explain the intended approach/fix BEFORE writing any code
2. Wait for explicit approval before implementing
3. If the approach is rejected, discuss alternatives before proceeding

### User Experience Constraints
- **No long waits for user actions**: Solutions that make the user wait 5-10+ seconds for basic operations (like power toggle) are NOT acceptable
- Prefer graceful degradation over disruptive recovery mechanisms
- UI responsiveness is a priority

### Change Management
- Discuss the trade-offs of each approach
- Consider the impact on normal operation, not just edge case recovery
- Prefer solutions that don't require restarting GLM during normal operation

## Technical Context

### Known Issues (Resolved)
- **High CPU after RDP disconnect** - FIXED with RDP session priming
  - **Root cause**: GLM (OpenGL app) encounters Windows display driver context issues when tscon switches session from disconnected RDP back to console. The Windows USER subsystem gets stuck in `UserSessionSwitchLeaveCrit`, causing high CPU.
  - **Solution**: Do an RDP connect/disconnect cycle BEFORE GLM starts. This "primes" the session so subsequent RDP disconnects don't cause issues.
  - **Implementation**: `bridge2glm.py` uses FreeRDP (`wfreerdp.exe`) to do a quick localhost RDP connection at startup, then disconnects and reconnects to console via `tscon`.
  - **Requirements**:
    - FreeRDP must be installed and `wfreerdp.exe` in PATH
    - NLA must be disabled on RDP server (registry: `HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp\UserAuthentication = 0`)
    - RDP credentials for localhost (currently hardcoded in script)
  - Priming only runs once per boot (tracked via `%TEMP%\rdp_primed.flag` with boot timestamp)

### Architecture Notes
- Multi-threaded application (HID, MIDI, Consumer, Logging threads)
- Uses UI automation (pywinauto) for power control via pixel sampling
- GlmManager handles GLM process lifecycle and watchdog
- Session reconnection via tscon when RDP disconnects
