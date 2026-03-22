# glm-cli and GLM MIDI Control Research

## 1. glm-cli Package Overview

- **PyPI**: https://pypi.org/project/glm-cli/
- **Version**: 0.1.0 (only release, published 2024-09-24)
- **Summary**: "Genelec GLM API (works over virtual midi, expects app to be open)"
- **Author**: araa47 (Akshay) — GitHub profile: https://github.com/araa47
- **Python**: >=3.12
- **License**: Not specified in metadata
- **Size**: 3.1 KB wheel (single file: `glm_cli/__init__.py`)
- **Source repo**: No public GitHub repository linked from PyPI. The author's GitHub profile exists but no glm-cli repo is visible (may be private).
- **Dependencies**: `click>=8.1.7`, `mido>=1.3.2`, `python-rtmidi>=1.5.8`
- **Dev deps**: pre-commit, pytest, ruff
- **Entry point**: `glm-cli` CLI command

## 2. glm-cli Source Code (complete, from wheel)

The entire package is a single `__init__.py` file (~100 lines). Here is the complete architecture:

### GLMControl Class

```python
class GLMControl:
    # Default CC numbers from GLM 5 on Mac -> Settings -> MIDI Settings
    CC_MAP = {
        "volume_up": 21,
        "volume_down": 22,
        "mute": 23,
        "dim": 24,
        "preset_level1": 25,
        "preset_level2": 26,
        "bm_bypass": 27,
        "system_power": 28,
        "group1": 31,
        "group2": 32,
        "group3": 33,
        "group4": 34,
        "group5": 35,
        "group6": 36,
        "group7": 37,
        "group8": 38,
        "group9": 39,
        "group10": 40,
        "group_plus": 41,
        "group_minus": 42,
    }

    # Special CCs (take a value 0-127 rather than just toggling)
    VOLUME_CC = 20      # Absolute volume (0-127)
    GROUPX_CC = 30      # Select group by number
    SOLO_DEV_CC = 43    # Solo a device by MIDI ID
    MUTE_DEV_CC = 44    # Mute a device by MIDI ID
```

### CLI Commands

- `glm-cli activate <function_name>` — Sends CC value 127 for the named function (momentary trigger)
- `glm-cli set-volume <value>` — Sends CC20 with value 0-127
- `glm-cli set-groupx <value>` — Sends CC30 with group number
- `glm-cli set-solo-dev <value>` — Sends CC43 with device MIDI ID (0-127)
- `glm-cli set-mute-dev <value>` — Sends CC44 with device MIDI ID (0-127)

### How It Works

1. On init, searches for a virtual MIDI device by name (default: "IAC Driver")
2. Uses `mido` library to open the MIDI output port
3. Sends standard MIDI Control Change messages: `mido.Message("control_change", control=CC, value=VALUE)`
4. GLM must be running and configured to listen on the same virtual MIDI device

## 3. GLM MIDI CC Number Table (Default Assignments)

These are the GLM 5 defaults (user-configurable in GLM Settings -> MIDI Settings):

| CC# | Function | Type | Notes |
|-----|----------|------|-------|
| 20 | Volume | Absolute (0-127) | Maps to GLM volume range |
| 21 | Volume Up | Momentary (send 127) | Increments volume |
| 22 | Volume Down | Momentary (send 127) | Decrements volume |
| 23 | Mute | Momentary (send 127) | System-wide mute toggle |
| 24 | Dim | Momentary (send 127) | System-wide dim toggle |
| 25 | Preset Level 1 | Momentary (send 127) | Recall level preset 1 |
| 26 | Preset Level 2 | Momentary (send 127) | Recall level preset 2 |
| 27 | BM Bypass | Momentary (send 127) | Bass Management bypass toggle |
| 28 | System Power | Momentary (send 127) | **Power ON/OFF toggle** |
| 30 | Group X | Absolute (0-127) | Select group by number |
| 31-40 | Group 1-10 | Momentary (send 127) | Direct group selection |
| 41 | Group Plus | Momentary (send 127) | Next group |
| 42 | Group Minus | Momentary (send 127) | Previous group |
| 43 | Solo Dev | Absolute (0-127) | Solo device by MIDI ID |
| 44 | Mute Dev | Absolute (0-127) | Mute device by MIDI ID |

**Important**: All CC assignments are user-configurable in GLM's MIDI Settings dialog. The numbers above are the defaults observed in GLM 5 on Mac. GLM 4.2 had the same system but the defaults may have differed slightly.

## 4. Key Answers to Your Questions

### Can you control power on/off via MIDI?
**YES.** CC28 (default) = "System Power" toggle. Sending value 127 toggles power state. This was added in GLM 5 (the `glm-cli` source comments say "GLM-5"). The Gearspace thread confirms users wanted a "system power toggle" via MIDI.

### Can you READ power state via MIDI?
**PARTIALLY.** GLM supports bidirectional MIDI (MIDI Output device config). Per Genelec support: "By defining a MIDI Output device, you can keep the status of MIDI controllers that support two-way communication in sync with GLM. For example, controllers that show toggle button status via LEDs may be able to sync to show states like Mute in GLM." However, `glm-cli` does NOT implement reading MIDI output — it only sends. Reading state would require monitoring GLM's MIDI output port for CC messages it sends back.

### What version of GLM added MIDI support?
- **GLM 4.2** (2022): First MIDI support — volume, mute, groups, bass management, level presets
- **GLM 5.0** (2024-01): Added "System Power" and "Solo/Mute Dev" commands, plus system-wide Mute and Dim

### Is this virtual MIDI (loopback) or hardware MIDI?
**Both.** GLM accepts MIDI from any MIDI input device — virtual or hardware. Common setups:
- **Mac**: IAC Driver (built-in virtual MIDI bus) — configure in Audio MIDI Setup
- **Windows**: loopMIDI or similar virtual MIDI cable
- **Hardware**: Any MIDI controller (Stream Deck via SoundFlow, control surfaces, etc.)

### What exactly can glm-cli do?
It is a thin CLI wrapper that sends MIDI CC messages to GLM over a virtual MIDI port. It can:
- Set absolute volume (0-127)
- Toggle system mute, dim, power
- Switch between monitor groups (1-10, or by number)
- Recall level presets
- Toggle bass management bypass
- Solo/mute individual devices by MIDI ID

It **cannot**:
- Read any state back from GLM
- Start or stop GLM
- Perform calibration
- Modify acoustic settings
- Work without GLM running and MIDI configured

## 5. Related Projects

### genlc (markbergsma/genlc)
- **Different approach**: Talks directly to the GLM network adapter via reverse-engineered proprietary binary protocol
- **Does NOT require GLM running** — replaces GLM for basic operations
- Supports: discovery, wakeup/shutdown, volume, mute/unmute, LED control
- Author: Mark Bergsma (ASR forum user "markb")
- GitHub: https://github.com/markbergsma/genlc (32 stars)
- Tested with 8330 monitors + 7350 subwoofer
- Main use case: Home Assistant integration

### Key difference
- `glm-cli`: Controls GLM software via MIDI (GLM must be running)
- `genlc`: Controls hardware directly via USB adapter (GLM not needed, but limited features)

## 6. Relevance to VOL20toGenelecGLM

The `glm-cli` approach (MIDI CC to GLM) could potentially replace or supplement the current pywinauto pixel-sampling approach for power control:

**Potential advantages:**
- CC28 "System Power" could toggle power without UI automation
- No pixel color sampling needed
- No focus stealing
- Works regardless of GLM window state/position
- Faster and more reliable than UI automation

**Potential limitations:**
- Still requires GLM to be running
- MIDI must be configured in GLM settings (one-time setup)
- Need a virtual MIDI driver on Windows (e.g., loopMIDI)
- Power state feedback would require monitoring GLM's MIDI output
- The CC numbers are configurable by the user — would need to match
- Untested whether "System Power" CC works identically to clicking the power button in GLM UI

**Setup required on Windows:**
1. Install loopMIDI (free, by Tobias Erichsen) or similar virtual MIDI driver
2. In GLM Settings -> MIDI Settings, enable MIDI interface and select the virtual MIDI port
3. Configure the script to send to the same virtual MIDI port

## 7. Sources

- PyPI: https://pypi.org/project/glm-cli/
- Libraries.io: https://libraries.io/pypi/glm-cli
- piwheels: https://www.piwheels.org/project/glm-cli/
- Genelec GLM: https://www.genelec.com/glm
- SoundOnSound review: https://www.soundonsound.com/reviews/genelec-glm-42
- Genelec MIDI support article: https://support.genelec.com/hc/en-us/articles/5842976755986
- GLM 5 manual (ManualsLib): https://www.manualslib.com/manual/3430649/Genelec-Glm-5.html
- SoundFlow StreamDeck GLM setup: https://forum.soundflow.org/-8844/glm-app-stream-deck-setup
- Gearspace thread: https://gearspace.com/board/high-end/1279456-diy-genelec-glm-adapter-volume-controller-2.html
- ASR genlc thread: https://www.audiosciencereview.com/forum/index.php?threads/python-module-to-manage-genelec-sam.25814/
- genlc GitHub: https://github.com/markbergsma/genlc
- GLM MIDI video tutorial: https://www.youtube.com/watch?v=XqXyTl6-x9o
