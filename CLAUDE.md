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
- **High CPU after RDP disconnect** - FIXED with Mesa3D software OpenGL
  - **Root cause**: GLMv5 is an OpenGL application. When RDP disconnects and tscon switches session to console, the OpenGL context created during RDP becomes invalid, causing GLM to spin at high CPU.
  - **Solution**: Install Mesa3D software OpenGL renderer (llvmpipe) for GLM only:
    1. Download MSVC build from https://github.com/pal1000/mesa-dist-win/releases
    2. Copy all DLLs from `x64/` folder to GLM's installation directory (e.g., `C:\Program Files\Genelec\GLM5\`)
    3. Windows DLL loading prefers local directory, so GLM uses software OpenGL while other apps use hardware
  - Software rendering is immune to display context switching because it's CPU-based, not GPU-bound

### Architecture Notes
- Multi-threaded application (HID, MIDI, Consumer, Logging threads)
- Uses UI automation (pywinauto) for power control via pixel sampling
- GlmManager handles GLM process lifecycle and watchdog
- Session reconnection via tscon when RDP disconnects
