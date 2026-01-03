# GLM Power Control Integration Guide

This document describes how to integrate `GlmPowerController` with the main daemon script for reliable power control.

## Overview

The power control system uses a **hybrid approach**:

| Path | Method | Use Case |
|------|--------|----------|
| **Primary** | MIDI CC 28 | Fast toggle, non-intrusive |
| **Verification** | UI pixel sampling | Confirm state after MIDI |
| **Fallback** | UI click automation | When MIDI pattern detection fails |
| **Startup** | UI state read | Sync internal state to actual GLM state |

## Integration Points in Main Script

### 1. Startup Sync (in `HIDToMIDIDaemon.start()`)

Read actual power state on startup instead of assuming `True`:

```python
from PowerOnOff import GlmPowerController, POWER_CONTROL_AVAILABLE

def start(self):
    # ... existing startup code ...

    # Sync power state from GLM UI (if available)
    if POWER_CONTROL_AVAILABLE:
        try:
            power_ctrl = GlmPowerController(steal_focus=False)
            if power_ctrl.is_available():
                state = power_ctrl.get_state()
                if state != "unknown":
                    glm_controller.power = (state == "on")
                    logger.info(f"Power synced from GLM UI: {state}")
        except Exception as e:
            logger.debug(f"Could not sync power state: {e}")
```

### 2. Enhanced SetPower Handling (in `consumer()`)

Handle explicit on/off in addition to toggle:

```python
elif isinstance(action, SetPower):
    if action.state is None:
        # Toggle via MIDI (existing behavior)
        logger.debug(f"Sending Power toggle (CC {GLM_POWER_CC})")
        self._send_action(Action.POWER)
    else:
        # Explicit state requires UI automation
        desired = "on" if action.state else "off"
        if POWER_CONTROL_AVAILABLE and self._power_controller:
            try:
                self._power_controller.set_state(desired)
                glm_controller.power = action.state
                logger.info(f"Power set to {desired} via UI")
            except GlmStateChangeFailedError as e:
                logger.error(f"Failed to set power to {desired}: {e}")
        else:
            # Fallback: toggle and hope for the best
            logger.warning(f"Explicit power {desired} requested but UI automation unavailable, using toggle")
            self._send_action(Action.POWER)
```

### 3. Power Verification (after MIDI pattern detection)

Optional verification when pattern detection seems unreliable:

```python
# In midi_reader, after pattern detection:
if POWER_CONTROL_AVAILABLE and self._power_controller:
    # Verify power state matches our detection
    try:
        actual = self._power_controller.get_state()
        if actual != "unknown" and actual != ("on" if glm_controller.power else "off"):
            logger.warning(f"Power state mismatch: detected={glm_controller.power}, UI={actual}")
            glm_controller.power = (actual == "on")
    except Exception:
        pass  # UI verification is optional
```

### 4. Daemon Initialization

Add power controller as optional component:

```python
class HIDToMIDIDaemon:
    def __init__(self, ...):
        # ... existing init ...

        # Power control via UI automation (optional)
        self._power_controller = None
        if POWER_CONTROL_AVAILABLE:
            try:
                from PowerOnOff import GlmPowerController
                self._power_controller = GlmPowerController(
                    steal_focus=True,  # or False for non-intrusive
                    logger=logger
                )
            except Exception as e:
                logger.debug(f"UI power control not available: {e}")
```

## REST API Enhancement

Update `api/rest.py` to support explicit power states:

```python
@app.post("/api/power")
async def set_power(request: PowerRequest = None):
    """
    Set power state.

    Body:
        {} - Toggle power
        {"state": true} - Ensure ON
        {"state": false} - Ensure OFF
    """
    if request and request.state is not None:
        action = SetPower(state=request.state)
    else:
        action = SetPower()  # Toggle

    action_queue.put(QueuedAction(action=action, timestamp=time.time()))
    return {"status": "queued"}
```

## MQTT Enhancement

Update `api/mqtt.py` for Home Assistant compatibility:

```python
def on_power_command(client, userdata, msg):
    payload = msg.payload.decode().upper()

    if payload == "ON":
        action = SetPower(state=True)
    elif payload == "OFF":
        action = SetPower(state=False)
    else:
        action = SetPower()  # Toggle

    action_queue.put(QueuedAction(action=action, timestamp=time.time()))
```

## Configuration

Add CLI arguments for power control behavior:

```python
parser.add_argument("--power_ui_automation", action="store_true", default=True,
                    help="Enable UI automation for power control (Windows only)")
parser.add_argument("--power_verify", action="store_true", default=False,
                    help="Verify power state via UI after MIDI commands")
parser.add_argument("--power_sync_startup", action="store_true", default=True,
                    help="Sync power state from GLM UI on startup")
```

## Error Handling

The library provides specific exceptions:

```python
from PowerOnOff import (
    GlmWindowNotFoundError,  # GLM not running
    GlmStateUnknownError,    # Cannot classify button color
    GlmStateChangeFailedError,  # Click didn't change state
)

try:
    power_controller.set_state("on")
except GlmWindowNotFoundError:
    logger.error("GLM window not found - is GLM running?")
except GlmStateUnknownError as e:
    logger.error(f"Cannot determine power state: rgb={e.rgb}")
except GlmStateChangeFailedError as e:
    logger.error(f"Power change failed: wanted {e.desired}, got {e.actual}")
```

## Thread Safety

`GlmPowerController` is thread-safe for reading state. However, **UI operations
should ideally be called from a single thread** to avoid focus/click race conditions.

Recommended approach:
- Read state from any thread (e.g., startup sync, periodic polling)
- Route all `set_state()` calls through the consumer thread's queue

## Platform Compatibility

| Platform | Status |
|----------|--------|
| Windows | Full support |
| Linux | Not supported (no pywinauto/win32) |
| macOS | Not supported |

The library gracefully degrades: `POWER_CONTROL_AVAILABLE` is `False` on unsupported platforms, and the main script should fall back to MIDI-only power control.
