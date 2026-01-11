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

### Known Issues
- **High CPU after RDP disconnect**: When RDP connects then disconnects on a headless-started VM, GLM may spin at high CPU after tscon reconnects the session to console. This is due to display driver context mismatch.
  - The issue only occurs on the FIRST RDP connect/disconnect after headless VM start
  - Subsequent RDP connect/disconnect cycles work fine
  - Reconnecting via RDP resolves the high CPU

### Architecture Notes
- Multi-threaded application (HID, MIDI, Consumer, Logging threads)
- Uses UI automation (pywinauto) for power control via pixel sampling
- GlmManager handles GLM process lifecycle and watchdog
- Session reconnection via tscon when RDP disconnects
