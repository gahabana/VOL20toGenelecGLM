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

```cmd
cd go
go build -o vol20toglm.exe .
```

## Quick Start

**Desktop user** (GLM already running, monitor attached):

```cmd
vol20toglm.exe --no_glm_manager --no_rdp_priming --no_midi_restart
```

**Headless VM** (full automation — launches GLM, primes RDP, restarts MIDI):

```cmd
vol20toglm.exe
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

### MQTT (not yet implemented)

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

## REST API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/state` | Current GLM state (JSON) |
| `POST` | `/api/volume` | Set volume: `{"value": 0-127}` |
| `POST` | `/api/volume/adjust` | Adjust volume: `{"delta": int}` |
| `POST` | `/api/mute` | Toggle mute (empty body) or set: `{"state": bool}` |
| `POST` | `/api/dim` | Toggle dim (empty body) or set: `{"state": bool}` |
| `POST` | `/api/power` | Toggle power (empty body) or set: `{"state": bool}` |
| `GET` | `/api/health` | Health check |
| `WS` | `/ws/state` | WebSocket — real-time state updates |
| `GET` | `/` | Web UI |

### State JSON

```json
{
  "volume": 83,
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
                    |   MQTT   |--+  (planned)
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
| MQTT | paho-mqtt | Not yet implemented |
| Logging | Python logging + RotatingFileHandler | slog + lumberjack rotation |
| Config | argparse | flag stdlib |

## RDP Priming Setup

See the main [CLAUDE.md](../CLAUDE.md) for detailed RDP priming setup instructions (FreeRDP installation, NLA configuration, credential storage).

## License

Same as the parent project.
