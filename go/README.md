# VOL20toGenelecGLM — Go Version

Single-binary bridge between a Griffin PowerMate VOL20 USB knob and Genelec GLM software. Controls volume, mute, dim, and power via USB HID input and MIDI output. Includes a REST API, WebSocket state broadcast, web UI, and optional GLM process management for headless VMs.

## Prerequisites

| Component | Requirement | Who needs it |
|-----------|-------------|-------------|
| Core (HID, MIDI, API, power) | **None** — single static binary | Everyone |
| Virtual MIDI ports | [LoopMIDI](https://www.tobias-erichsen.de/software/loopmidi.html) | Only if no physical MIDI controller |
| RDP priming | [FreeRDP](https://github.com/FreeRDP/FreeRDP/releases) (`wfreerdp.exe` in PATH) | Only headless VM users |
| RDP priming | Stored credentials (`cmdkey /generic:localhost /user:USER /pass:PASS`) | Only headless VM users |

### Go Toolchain

Go 1.22+ required to build from source. Download from [go.dev](https://go.dev/dl/).

## Building

**Development build** (includes debug symbols):

```cmd
cd go
go build -o vol20toglm.exe .
```

**Release build** (stripped, ~30% smaller):

```cmd
cd go
go build -ldflags="-s -w" -o vol20toglm.exe .
```

| Build | Windows size | macOS size |
|-------|-------------|------------|
| Development | 10.3 MB | 8.8 MB |
| Release (`-s -w`) | 7.2 MB | 6.0 MB |

## Quick Start

**Desktop user** (GLM already running, user interacting with screen):

```cmd
vol20toglm.exe --no_glm_manager --no_rdp_priming --no_midi_restart --no_ui_automation
```

**Headless VM** (full automation — launches GLM, primes RDP, restarts MIDI, pixel verification):

```cmd
vol20toglm.exe --headless
```

**Headless VM with UI-based power** (fallback if MIDI power causes speaker disconnects):

```cmd
vol20toglm.exe --headless --ui_power
```

## CLI Flags

### Logging

| Flag | Default | Description |
|------|---------|-------------|
| `--log_level` | `DEBUG` | Logging level: `DEBUG`, `INFO`, `NONE` |
| `--log_file_name` | `vol20toglm.log` | Log file name (placed next to binary) |

Console shows INFO and above. Log file captures DEBUG for full detail. File rotates at 4MB with 5 backups.

### HID Device

| Flag | Default | Description |
|------|---------|-------------|
| `--device` | `0x07d7,0x0000` | USB VID,PID in hex |

### MIDI

| Flag | Default | Description |
|------|---------|-------------|
| `--midi_in_channel` | `GLMMIDI` | MIDI port where GLM reads (we write to this) |
| `--midi_out_channel` | `GLMOUT` | MIDI port where GLM writes (we read from this) |

Port matching is substring-based — `GLMMIDI` matches `GLMMIDI 1`, `GLMMIDI 2`, etc.

### Volume Acceleration

| Flag | Default | Description |
|------|---------|-------------|
| `--min_click_time` | `0.2` | Min seconds between clicks to consider separate |
| `--max_avg_click_time` | `0.15` | Max average click time for acceleration |
| `--volume_increases_list` | `1,1,2,2,3` | Volume delta per acceleration level |

### REST API

| Flag | Default | Description |
|------|---------|-------------|
| `--api_port` | `8080` | HTTP port for REST API and web UI (0 to disable) |
| `--cors_origin` | `*` | CORS Allow-Origin header (empty string to disable) |

### MQTT / Home Assistant

| Flag | Default | Description |
|------|---------|-------------|
| `--mqtt_broker` | *(empty)* | MQTT broker hostname (empty to disable) |
| `--mqtt_port` | `1883` | MQTT broker port |
| `--mqtt_user` | *(empty)* | MQTT username |
| `--mqtt_pass` | *(empty)* | MQTT password |
| `--mqtt_topic` | `glm` | MQTT topic prefix |
| `--mqtt_ha_discovery` | `true` | Enable Home Assistant MQTT Discovery |
| `--no_mqtt_ha_discovery` | | Disable HA Discovery |

### GLM Process Manager

| Flag | Default | Description |
|------|---------|-------------|
| `--glm_manager` | `true` | Launch/monitor GLM process |
| `--no_glm_manager` | | Disable GLM management |
| `--glm_path` | `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe` | Path to GLM executable |
| `--glm_cpu_gating` | `true` | Wait for CPU < 10% before launching GLM |
| `--no_glm_cpu_gating` | | Disable CPU gating |

### Startup Automation

| Flag | Default | Description |
|------|---------|-------------|
| `--rdp_priming` | `true` | RDP connect/disconnect cycle at startup |
| `--no_rdp_priming` | | Disable RDP priming |
| `--midi_restart` | `true` | Restart Windows MIDI service at startup |
| `--no_midi_restart` | | Disable MIDI service restart |
| `--high_priority` | `true` | Set process priority to AboveNormal |
| `--no_high_priority` | | Run at normal priority |

### Power Control Mode

| Flag | Default | Description |
|------|---------|-------------|
| `--no_ui_automation` | `false` | Disable all pixel reading and mouse clicks. Power via MIDI CC28 only. Best for desktop use. |
| `--headless` | `false` | Enable UI automation for pixel verification and speaker health monitoring. Power still via MIDI CC28 unless `--ui_power` is set. |
| `--ui_power` | `false` | Use UI click for power instead of MIDI. Requires `--headless`. Fallback if MIDI power causes speaker disconnects. |

**Modes summary:**

| Flags | Power control | Screen reading | Use case |
|-------|--------------|----------------|----------|
| `--no_ui_automation` | MIDI CC28 | Disabled | Desktop, user interacting with GLM |
| `--headless` | MIDI CC28 | Enabled (verify + health) | Headless VM, unattended |
| `--headless --ui_power` | UI click | Enabled | Fallback if MIDI power unreliable |
| *(no flags)* | MIDI CC28 | Disabled | Same as `--no_ui_automation` |

**GLM prerequisite:** MIDI Settings must have Power, Mute, and Dim set to **"Toggle"** (not "Momentary") for deterministic MIDI control. See `RESEARCH-glm-midi-cc28-power.md` Section 11 for details.

### Startup

| Flag | Default | Description |
|------|---------|-------------|
| `--startup_volume` | `-1` | Initial volume (0-127), -1 to discover from GLM startup burst |

### Discovery

| Flag | Description |
|------|-------------|
| `--list` | List available HID devices and MIDI ports, then exit |

### Debug

| Flag | Default | Description |
|------|---------|-------------|
| `--debug_captures` | `false` | Dump pixel captures to BMP files for inspection |

## Utilities

### midiprobe

Standalone MIDI diagnostic tool — sends Vol+/Vol- (CC21/CC22) to GLM and prints the response to discover current state. Windows only.

```cmd
cd go
go build -o midiprobe.exe ./cmd/midiprobe
midiprobe.exe
```

Useful for verifying GLM MIDI port names and confirming CC messages are flowing.

## REST API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/state` | Current GLM state (JSON) |
| `POST` | `/api/volume` | Set volume: `{"value": 0-127}` or `{"db": -127..0}` |
| `POST` | `/api/volume/adjust` | Adjust volume: `{"delta": int}` |
| `POST` | `/api/mute` | Toggle mute (empty body) or set: `{"state": bool}` |
| `POST` | `/api/dim` | Toggle dim (empty body) or set: `{"state": bool}` |
| `POST` | `/api/power` | Power control: `{"state": "on"}`, `{"state": "off"}`, `{"state": "toggle"}`, `{"state": bool}`, or empty body (toggle) |
| `GET` | `/api/health` | Health check |
| `WS` | `/ws/state` | WebSocket — real-time state updates |
| `GET` | `/` | Web UI |
| `GET` | `/v1` | Web UI — skeuomorphic variant |
| `GET` | `/v2` | Web UI — minimal Genelec variant |
| `GET` | `/v3` | Web UI — enhanced skeuomorphic variant |

### State JSON

```json
{
  "volume": 83,
  "volume_db": -44,
  "mute": false,
  "dim": false,
  "power": true,
  "power_transitioning": false,
  "power_settling_remaining": 0,
  "power_cooldown": false,
  "power_cooldown_remaining": 0
}
```

POST responses include all state fields plus a `trace_id` for command correlation:

```json
{
  "volume": 83,
  "volume_db": -44,
  "trace_id": "api-0042",
  "mute": false,
  "dim": false,
  "power": true,
  "power_transitioning": false,
  "power_settling_remaining": 0,
  "power_cooldown": false,
  "power_cooldown_remaining": 0
}
```

## Architecture

```
                    +----------+
                    | REST API |--+
                    +----------+  |
                    +----------+  |
                    |   MQTT   |--+
                    +----------+  |
                                  v
+---------+    +---------------------+    +----------+    +-----------+
|   HID   |--->|   actions channel   |--->| Consumer |--->| MIDI Out  |
|  reader |    |   chan Action (100)  |    |          |    |           |
+---------+    +---------------------+    +----+-----+    +-----------+
                                               |
                                               v
                                        +--------------+
                                        |  Controller  |
                                        |  (state)     |
                                        +------+-------+
                                               | state change callbacks
                                        +------+-------+
                                        v              v
                                  +----------+  +----------+
                                  | REST WS  |  |   MQTT   |
                                  | broadcast|  | publish  |
                                  +----------+  +----------+

+-----------+
| MIDI In   |---> Controller.UpdateFromMIDI()
|  reader   |---> Power Pattern Detector
+-----------+

+---------------+
| Power Control |---> Pixel detection + mouse click (Windows)
+---------------+

+---------------+
| GLM Manager   |---> Process launch, watchdog, window stabilization
+---------------+
```

## Differences from Python Version

| Feature | Python (`bridge2glm.py`) | Go (`vol20toglm.exe`) |
|---------|--------------------------|----------------------|
| Runtime | Python 3.10+ with venv | Single binary, no dependencies |
| MIDI library | mido + python-rtmidi | Direct winmm.dll syscalls |
| HID library | hidapi | Direct Windows HID syscalls |
| HTTP framework | FastAPI + Uvicorn | net/http stdlib |
| WebSocket | FastAPI built-in | nhooyr.io/websocket |
| UI automation | pywinauto | Direct Win32 syscalls |
| Power detection | ImageGrab (PIL) | BitBlt screen capture |
| Click simulation | SetCursorPos + mouse_event | SetCursorPos + mouse_event (same) |
| MQTT | paho-mqtt | paho.mqtt.golang |
| Logging | Python logging + RotatingFileHandler | slog + lumberjack rotation |
| Config | argparse | flag stdlib |

## RDP Priming Setup

See the main [CLAUDE.md](../CLAUDE.md) for detailed RDP priming setup instructions (FreeRDP installation, NLA configuration, credential storage).

## License

Same as the parent project.
