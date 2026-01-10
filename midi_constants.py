"""
MIDI Constants and Mappings for GLM control.

Contains all MIDI CC numbers, enums, and mappings between
logical actions and GLM MIDI controls.
"""

from dataclasses import dataclass
from enum import Enum
from typing import Dict


# ==============================================================================
# 1) LOGICAL ACTIONS - What the system can do
# ==============================================================================

class Action(Enum):
    VOL_UP = "VolUp"
    VOL_DOWN = "VolDown"
    MUTE = "Mute"
    DIM = "Dim"
    POWER = "Power"
    # Non-GLM actions (for future routing to other apps)
    PLAY_PAUSE = "Play/Pause"
    NEXT_TRACK = "NextTrack"
    PREV_TRACK = "PrevTrack"


# ==============================================================================
# 2) GLM MIDI MAPPING - How GLM exposes controls via MIDI
# ==============================================================================

class ControlMode(Enum):
    MOMENTARY = "momentary"  # Send 127 to trigger, auto-resets
    TOGGLE = "toggle"        # Send 127/0 to set state explicitly


@dataclass(frozen=True)
class GlmControl:
    cc: int                 # MIDI CC number
    label: str              # Human-readable label
    mode: ControlMode       # How this control behaves


# GLM MIDI CC numbers (from GLM MIDI Settings)
GLM_VOLUME_ABS = 20   # Absolute volume (0-127) - GLM outputs this, can also set it
GLM_VOL_UP_CC = 21    # Volume increment (momentary)
GLM_VOL_DOWN_CC = 22  # Volume decrement (momentary)
GLM_MUTE_CC = 23      # Mute (toggle)
GLM_DIM_CC = 24       # Dim (toggle)
GLM_POWER_CC = 28     # System Power (momentary trigger, no MIDI feedback)

# Power detection pattern: MUTE -> VOL -> DIM -> MUTE -> VOL (5 messages within ~150ms)
# GLM sends this pattern on power toggle and startup (startup sends 7 then 5)
POWER_PATTERN = [GLM_MUTE_CC, GLM_VOLUME_ABS, GLM_DIM_CC, GLM_MUTE_CC, GLM_VOLUME_ABS]
POWER_PATTERN_WINDOW = 0.5  # seconds - max time window for pattern
POWER_PATTERN_MIN_SPAN = 0.05  # seconds - min span (faster = buffer dump, ignore)
POWER_STARTUP_WINDOW = 3.0  # seconds - if second pattern within this, it's GLM startup

# CC number to human-readable name (for logging)
CC_NAMES: Dict[int, str] = {
    GLM_VOLUME_ABS: "Volume",
    GLM_VOL_UP_CC: "Vol+",
    GLM_VOL_DOWN_CC: "Vol-",
    GLM_MUTE_CC: "Mute",
    GLM_DIM_CC: "Dim",
    GLM_POWER_CC: "Power",
}

# Catalogue of GLM controls
ACTION_TO_GLM: Dict[Action, GlmControl] = {
    Action.VOL_UP:   GlmControl(cc=GLM_VOL_UP_CC,   label="Vol+",  mode=ControlMode.MOMENTARY),
    Action.VOL_DOWN: GlmControl(cc=GLM_VOL_DOWN_CC, label="Vol-",  mode=ControlMode.MOMENTARY),
    Action.MUTE:     GlmControl(cc=GLM_MUTE_CC,     label="Mute",  mode=ControlMode.TOGGLE),
    Action.DIM:      GlmControl(cc=GLM_DIM_CC,      label="Dim",   mode=ControlMode.TOGGLE),
    Action.POWER:    GlmControl(cc=GLM_POWER_CC,    label="Power", mode=ControlMode.TOGGLE),
    # Non-GLM actions don't have GLM controls (yet)
}

# Reverse lookup: CC number -> Action (for reading GLM state from MIDI output)
CC_TO_ACTION: Dict[int, Action] = {
    ctrl.cc: action for action, ctrl in ACTION_TO_GLM.items()
}


# ==============================================================================
# 3) PHYSICAL DEVICE - VOL20 Hardware Keycodes (immutable)
# ==============================================================================

KEY_VOL_UP = 2
KEY_VOL_DOWN = 1
KEY_CLICK = 32          # Single click on VOL20
KEY_DOUBLE_CLICK = 16   # Double click on VOL20
KEY_TRIPLE_CLICK = 8    # Triple click on VOL20
KEY_LONG_PRESS = 4      # 2-second press on VOL20

KEY_NAMES: Dict[int, str] = {
    KEY_VOL_UP: "VolUp",
    KEY_VOL_DOWN: "VolDown",
    KEY_CLICK: "Click",
    KEY_DOUBLE_CLICK: "DblClick",
    KEY_TRIPLE_CLICK: "TplClick",
    KEY_LONG_PRESS: "LongPress",
}


# ==============================================================================
# 4) KEY BINDINGS - Map physical keys to logical actions (configurable)
# ==============================================================================

DEFAULT_BINDINGS: Dict[int, Action] = {
    KEY_VOL_UP: Action.VOL_UP,
    KEY_VOL_DOWN: Action.VOL_DOWN,
    KEY_CLICK: Action.POWER,         # Click -> Power
    KEY_DOUBLE_CLICK: Action.DIM,    # Double click -> Dim
    KEY_TRIPLE_CLICK: Action.DIM,    # Triple click -> Dim
    KEY_LONG_PRESS: Action.MUTE,     # Long press -> Mute
}


def log_midi(logger, direction: str, msg_type: str, cc: int = None, value: int = None, channel: int = None, raw: str = None):
    """
    Log MIDI message in consistent format.

    Args:
        logger: Logger instance to use
        direction: "TX" for sent, "RX" for received
        msg_type: MIDI message type (e.g., "control_change", "note_on")
        cc: Control change number (if applicable)
        value: Value (if applicable)
        channel: MIDI channel (if applicable)
        raw: Raw message string for unknown types
    """
    if msg_type == "control_change" and cc is not None:
        cc_name = CC_NAMES.get(cc, f"CC{cc}")
        logger.info(f"MIDI {direction}: {cc_name}(CC{cc})={value}")
    elif raw:
        logger.info(f"MIDI {direction}: {raw}")
    else:
        parts = [f"MIDI {direction}: {msg_type}"]
        if channel is not None:
            parts.append(f"ch={channel}")
        if value is not None:
            parts.append(f"val={value}")
        logger.info(" ".join(parts))
