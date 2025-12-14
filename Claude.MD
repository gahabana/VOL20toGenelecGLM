# VOL20toGenelecGLM Refactoring Project

## Project Overview

Refactor the VOL20toGenelecGLM Python script to support multiple input sources (HID knob, REST API, MQTT/Home Assistant, Web UI) while preserving the proven threading and queue-based architecture.

**Repository:** https://github.com/gahabana/VOL20toGenelecGLM
**Current working script:** `fosi2-glm-midi-sonnet4.5.py`

---

## ⚠️ CRITICAL CONSTRAINTS - READ FIRST

### DO NOT CHANGE unless explicitly approved by user:

1. **Threading Model**: The current multi-threaded approach with queue-based message passing works correctly. DO NOT replace with async/await or change thread structure without explicit discussion and approval.

2. **Queue Mechanism**: The `deque` + `threading.Condition` pattern for inter-thread communication is proven. Keep it.

3. **MIDI Communication**: The existing MIDI send/receive logic via `mido` and `python-rtmidi` works. Don't refactor MIDI handling unless necessary.

4. **HID Reading**: The hidapi-based VOL20 reading with reconnection logic works. Preserve it.

### If you believe a change to the above is necessary:
1. STOP and explain WHY it must change
2. Show WHAT the minimal change would be
3. Wait for user confirmation before proceeding

---

## Current Architecture (Working)

```
┌─────────────────────────────────────────────────────────────────┐
│                     CURRENT ARCHITECTURE                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────┐    HID-specific     ┌─────────┐    Domain Actions  │
│  │   HID   │───{'key':X,────────→│  Queue  │───────────────┐    │
│  │ Reader  │    'distance':Y}    │ (deque) │               │    │
│  │ Thread  │                     └─────────┘               │    │
│  └─────────┘                                               ▼    │
│                                                     ┌───────────┐│
│                                                     │ Consumer  ││
│                                                     │  Thread   ││
│                                                     │           ││
│  ┌─────────┐                     ┌─────────────┐   │ Maps keys ││
│  │  MIDI   │◄───────────────────│GlmController│◄──│ to MIDI   ││
│  │ Reader  │    State Updates   │  (state)    │   │           ││
│  │ Thread  │───────────────────→│             │   └───────────┘│
│  └─────────┘                    └─────────────┘                 │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**Key threads:**
- HID Reader: Reads VOL20 Bluetooth HID events
- Consumer/Worker: Processes queue, sends MIDI commands
- MIDI Reader: Receives state feedback from GLM

---

## Target Architecture

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                                                                              │
│                              INPUT ADAPTERS                                  │
│   ┌──────────────────────────────────────────────────────────────────────┐   │
│   │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  │   │
│   │  │     HID     │  │    REST     │  │    MQTT     │  │   Web UI    │  │   │
│   │  │   Adapter   │  │   Adapter   │  │   Adapter   │  │  (served)   │  │   │
│   │  │             │  │  (FastAPI)  │  │(paho-mqtt)  │  │             │  │   │
│   │  │ Handles:    │  │             │  │             │  │             │  │   │
│   │  │ -Accel.     │  │ Handles:    │  │ Handles:    │  │ Uses REST   │  │   │
│   │  │ -Debounce   │  │ -Validation │  │ -Subscribe  │  │ + WebSocket │  │   │
│   │  │ -Reconnect  │  │ -WebSocket  │  │ -HA Discov. │  │             │  │   │
│   │  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └─────────────┘  │   │
│   └─────────┼────────────────┼────────────────┼──────────────────────────┘   │
│             │                │                │                              │
│             │   GlmAction dataclasses         │                              │
│             └───────────────┬┴────────────────┘                              │
│                             ▼                                                │
│          ┌──────────────────────────────────────────────────────┐            │
│          │              ACTION QUEUE (existing pattern)         │            │
│          │  SetVolume(75) | AdjustVolume(+3) | ToggleMute | ... │            │
│          └──────────────────────────┬───────────────────────────┘            │
│                                     ▼                                        │
│  ┌──────────────────────────────────────────────────────────────────────┐    │
│  │                        GlmController CORE                            │    │
│  │  ┌────────────────────────────────────────────────────────────────┐  │    │
│  │  │  WORKER THREAD (existing consumer, modified)                   │  │    │
│  │  │    - Processes GlmAction objects from queue                    │  │    │
│  │  │    - Sends MIDI commands                                       │  │    │
│  │  │    - Calls state change callbacks                              │  │    │
│  │  └────────────────────────────────────────────────────────────────┘  │    │
│  │  ┌─────────────────────┐    ┌─────────────────────────────────────┐  │    │
│  │  │   MIDI IN THREAD    │    │           GlmState                  │  │    │
│  │  │   (existing)        │───→│  volume: int (0-127 or dB)          │  │    │
│  │  └─────────────────────┘    │  mute: bool                         │  │    │
│  │                             │  dim: bool                          │  │    │
│  │  ┌─────────────────────┐    │  power: bool                        │  │    │
│  │  │ STATE CALLBACKS     │    └─────────────────────────────────────┘  │    │
│  │  │ on_state_change[]   │                                             │    │
│  │  └─────────────────────┘                                             │    │
│  └──────────────────────────────────────────────────────────────────────┘    │
│                                                                              │
│  STATE CHANGE LISTENERS (register callbacks):                                │
│    - WebSocketBroadcaster → push to connected web clients                    │
│    - MqttPublisher → publish to HA broker                                    │
│    - Logger → audit trail                                                    │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## GlmAction Dataclasses (New)

```python
from dataclasses import dataclass
from typing import Optional, Union

@dataclass(frozen=True)
class SetVolume:
    """Set absolute volume level"""
    target: int  # 0-127 MIDI value or dB depending on GLM config

@dataclass(frozen=True)
class AdjustVolume:
    """Relative volume change"""
    delta: int  # Positive = up, negative = down

@dataclass(frozen=True)
class SetMute:
    """Set or toggle mute state"""
    state: Optional[bool] = None  # None = toggle

@dataclass(frozen=True)
class SetDim:
    """Set or toggle dim state"""
    state: Optional[bool] = None  # None = toggle

@dataclass(frozen=True)
class SetPower:
    """Toggle power (GLM only supports toggle, not explicit on/off)"""
    pass

# Union type for queue
GlmAction = Union[SetVolume, AdjustVolume, SetMute, SetDim, SetPower]
```

---

## Implementation Phases

### Phase 1: Extract and Refactor Core (LOW RISK)
**Goal:** Introduce GlmAction dataclasses without changing behavior

1. Create `glm_core/` package structure
2. Define GlmAction dataclasses in `glm_core/actions.py`
3. Define GlmState dataclass in `glm_core/state.py`
4. Extract GlmController class to `glm_core/controller.py`
5. **Modify queue to use GlmAction instead of raw HID dicts**
6. Update consumer/worker to match on GlmAction types
7. HID adapter creates GlmAction objects instead of raw dicts

**Test:** Script behaves identically to before with VOL20 knob

### Phase 2: Add State Change Callbacks
**Goal:** Enable reactive updates for future adapters

1. Add `on_state_change: List[Callable[[GlmState], None]]` to GlmController
2. Call callbacks after every state change (worker action or MIDI feedback)
3. Add simple logging callback as proof of concept

**Test:** Log shows state changes when using VOL20 knob

### Phase 3: Add REST API + WebSocket
**Goal:** Web UI can control GLM

1. Create `api/rest.py` with FastAPI app
2. Endpoints:
   - `GET /api/state` → current GlmState
   - `POST /api/volume` ← `{"value": 75}` (absolute)
   - `POST /api/volume/adjust` ← `{"delta": -5}` (relative)
   - `POST /api/mute` ← `{"state": true}` or `{}` for toggle
   - `POST /api/dim` ← `{"state": true}` or `{}` for toggle
   - `POST /api/power` ← `{}` (toggle)
   - `WS /ws/state` → push state changes
3. REST handlers create GlmAction and submit to queue
4. WebSocket handler registers as state change callback

**Test:** curl commands work, WebSocket receives updates

### Phase 4: Add Simple Web UI
**Goal:** Mobile-friendly control page

1. Create `web/index.html` - single file, no build step
2. Elements:
   - Volume slider (0-100 or dB range)
   - Large dB display
   - Mute button (toggle)
   - Dim button (toggle)
   - Power button (toggle)
3. Uses REST for commands, WebSocket for live updates
4. Serve via FastAPI static files

**Test:** Access from phone browser, controls work

### Phase 5: Add MQTT/Home Assistant Integration
**Goal:** BILRESA remote via HA can control GLM

1. Create `api/mqtt.py` with paho-mqtt client
2. Subscribe to command topics:
   - `glm/set/volume` ← value
   - `glm/set/mute` ← ON/OFF
   - `glm/set/power` ← ON/OFF
3. Publish state to `glm/state` on changes
4. Implement HA MQTT Discovery (auto-creates entities)
5. MQTT handler creates GlmAction and submits to queue

**Test:** HA shows GLM as media player, can control

### Phase 6: (Optional) Remove Legacy Queue Wrapper
**Goal:** Cleaner code if all adapters work

Only if Phase 1-5 complete and stable. May not be necessary.

---

## File Structure (Target)

```
VOL20toGenelecGLM/
├── CLAUDE.md                    # This file
├── README.md                    # Updated with new features
├── requirements.txt             # Dependencies
├── config.py                    # Configuration (ports, MQTT broker, etc.)
│
├── glm_core/
│   ├── __init__.py
│   ├── actions.py               # GlmAction dataclasses
│   ├── state.py                 # GlmState dataclass
│   ├── controller.py            # GlmController (queue, worker, MIDI)
│   └── midi_handler.py          # MIDI I/O (extracted from current)
│
├── adapters/
│   ├── __init__.py
│   ├── hid_adapter.py           # VOL20 HID reader (refactored from current)
│   └── (mqtt_adapter.py)        # Phase 5
│
├── api/
│   ├── __init__.py
│   ├── rest.py                  # FastAPI REST + WebSocket
│   └── (mqtt.py)                # Phase 5
│
├── web/
│   └── index.html               # Simple control UI
│
├── glm_server.py                # Main entry point (new)
└── legacy/
    └── hid2midi-agent-v5.py     # Original working script (preserved)
```

---

## API Specification

### REST Endpoints

| Method | Path | Request Body | Response |
|--------|------|--------------|----------|
| GET | `/api/state` | - | `{"volume": 85, "mute": false, "dim": false, "power": true}` |
| POST | `/api/volume` | `{"value": 75}` | `{"volume": 75, ...}` |
| POST | `/api/volume/adjust` | `{"delta": -5}` | `{"volume": 70, ...}` |
| POST | `/api/mute` | `{"state": true}` or `{}` | `{"mute": true, ...}` |
| POST | `/api/dim` | `{"state": false}` or `{}` | `{"dim": false, ...}` |
| POST | `/api/power` | `{}` | `{"power": true, ...}` |
| GET | `/api/health` | - | `{"status": "ok", "midi": true, "hid": true}` |

### WebSocket

- Connect to `/ws/state`
- Receives JSON state object on every change
- No client-to-server messages needed (use REST for commands)

### MQTT Topics (Phase 5)

| Topic | Direction | Payload |
|-------|-----------|---------|
| `glm/state` | Publish | `{"volume": 85, "mute": "OFF", "power": "ON"}` |
| `glm/set/volume` | Subscribe | `75` (integer) |
| `glm/set/mute` | Subscribe | `ON` or `OFF` |
| `glm/set/power` | Subscribe | `ON` or `OFF` |
| `homeassistant/number/glm_volume/config` | Publish | HA Discovery payload |

---

## Dependencies

```
# Core (existing)
hidapi
mido
python-rtmidi

# New for API
fastapi
uvicorn[standard]
websockets

# New for MQTT (Phase 5)
paho-mqtt
```

---

## Configuration

Create `config.py` with:

```python
# MIDI
MIDI_PORT_NAME = "GLMMIDI 1"

# HID (VOL20)
HID_VID = 0x07d7
HID_PID = 0x0000

# REST API
API_HOST = "0.0.0.0"
API_PORT = 8080

# MQTT (Phase 5)
MQTT_BROKER = "homeassistant.local"
MQTT_PORT = 1883
MQTT_USER = None
MQTT_PASS = None
```

---

## Testing Checklist

### Phase 1
- [ ] VOL20 knob still controls volume
- [ ] Mute/dim buttons work
- [ ] Power toggle works
- [ ] No regression in behavior

### Phase 2
- [ ] State changes logged
- [ ] Callbacks fire on HID input
- [ ] Callbacks fire on MIDI feedback

### Phase 3
- [ ] `curl http://localhost:8080/api/state` returns state
- [ ] `curl -X POST -d '{"value":50}' http://localhost:8080/api/volume` changes volume
- [ ] WebSocket client receives updates
- [ ] HID and REST don't interfere

### Phase 4
- [ ] Web UI loads in browser
- [ ] Slider controls volume
- [ ] Buttons toggle mute/dim/power
- [ ] UI updates when VOL20 knob used

### Phase 5
- [ ] MQTT publishes state on change
- [ ] MQTT commands work
- [ ] HA auto-discovers GLM entity
- [ ] BILRESA → HA → MQTT → GLM works

---

## Notes for Claude Code

1. **Start by reading the current working script** (`fosi2-glm-midi-sonnet4.5.py` or latest)
2. **Understand the threading model before making changes**
3. **Make incremental changes** - one phase at a time
4. **Test after each phase** before proceeding
5. **Preserve the original script** in `legacy/` folder
6. **Ask before changing core threading/queue logic**
7. **In order to keep existing code - start new branch or decide on easiest way to potentially create new version
---

## User Preferences

- Prefers short, correct answers over verbose explanations
- Has intermediate Python knowledge, strong computing fundamentals
- Wants to understand WHY before changes are made to working code
- Values data-driven analysis and underlying principles
