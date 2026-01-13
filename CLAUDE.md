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
    - RDP credentials for localhost (currently hardcoded in script)
    - Use explicit local domain syntax (`.\username`) for NLA compatibility
  - Priming only runs once per boot (tracked via `%TEMP%\rdp_primed.flag` with boot timestamp)

#### RDP Priming Setup Instructions

Follow these steps to set up RDP priming on a new VM:

**1. Install FreeRDP**

Download the latest Windows release from:
https://github.com/FreeRDP/FreeRDP/releases

- Download `FreeRDP-*.zip` (Windows binaries)
- Extract the archive
- Copy `wfreerdp.exe` to a directory in your PATH (e.g., `C:\Users\<username>\AppData\Local\Microsoft\WindowsApps\`)
- Verify installation: `where wfreerdp` should return the path

**2. NLA Configuration**

NLA (Network Level Authentication) can remain **enabled** for better security. The key is to use explicit local domain syntax (`.\username`) when specifying credentials.

To verify NLA is enabled (recommended):
```cmd
reg query "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp" /v UserAuthentication
```
Should show `UserAuthentication    REG_DWORD    0x1` (enabled).

**Note**: If you previously disabled NLA, you can re-enable it:
```cmd
reg add "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp" /v UserAuthentication /t REG_DWORD /d 1 /f
```

**3. Configure RDP Credentials**

The script currently has credentials hardcoded in `bridge2glm.py` in the `prime_rdp_session()` function:
```python
[wfreerdp, "/v:localhost", "/u:.\\zh", "/p:qwe2qwe2", "/cert:ignore", "/sec:nla"]
```

Update the `/u:` (username) and `/p:` (password) values to match your Windows user account.
**Important**: Keep the `.\` prefix before the username - this is required for NLA to work.

**4. Verify Setup**

Test manually from command prompt (with no VNC/RDP connected):
```cmd
wfreerdp /v:localhost /u:.\YOUR_USER /p:YOUR_PASS /cert:ignore /sec:nla
```

This should:
- Open an RDP window to localhost
- Connect without prompting for credentials
- You can close it manually after verifying it works

**Note**: The `.\` prefix specifies the local machine domain, which is required for NLA authentication with local accounts.

**5. How It Works**

At script startup (`bridge2glm.py`):
1. `needs_rdp_priming()` checks if priming was already done this boot (via `%TEMP%\rdp_primed.flag`)
2. If not primed, `prime_rdp_session()` runs:
   - Starts FreeRDP connection to localhost
   - Waits 3 seconds for connection to establish
   - Kills FreeRDP process (disconnects)
   - Runs `tscon 1 /dest:console` to reconnect session to console
3. GLM then starts normally
4. Subsequent RDP connect/disconnect cycles won't cause high CPU

**Troubleshooting**

| Issue | Cause | Solution |
|-------|-------|----------|
| `wfreerdp not found` | Not in PATH | Add FreeRDP directory to PATH |
| `SEC_E_UNKNOWN_CREDENTIALS` | Missing `.\` prefix or wrong password | Use `.\username` syntax, verify password |
| `HYBRID_REQUIRED_BY_SERVER` | NLA required but not using `/sec:nla` | Add `/sec:nla` to command |
| Priming runs every boot | Flag file issue | Check `%TEMP%\rdp_primed.flag` exists and is writable |
| High CPU still occurs | Priming failed or didn't run | Check logs for "RDP session primed successfully" |

### Architecture Notes
- Multi-threaded application (HID, MIDI, Consumer, Logging threads)
- Uses UI automation (pywinauto) for power control via pixel sampling
- GlmManager handles GLM process lifecycle and watchdog
- Session reconnection via tscon when RDP disconnects
