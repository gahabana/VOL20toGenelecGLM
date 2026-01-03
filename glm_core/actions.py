"""
GlmAction dataclasses - domain actions for GLM control.

These represent what the system should do, independent of input source.
All input adapters (HID, REST, MQTT) create these actions and submit to the queue.
"""
from dataclasses import dataclass
from typing import Optional, Union


@dataclass(frozen=True)
class SetVolume:
    """Set absolute volume level (0-127 MIDI value)."""
    target: int


@dataclass(frozen=True)
class AdjustVolume:
    """Relative volume change. Positive = up, negative = down."""
    delta: int


@dataclass(frozen=True)
class SetMute:
    """Set or toggle mute state. None = toggle."""
    state: Optional[bool] = None


@dataclass(frozen=True)
class SetDim:
    """Set or toggle dim state. None = toggle."""
    state: Optional[bool] = None


@dataclass(frozen=True)
class SetPower:
    """
    Set or toggle power state.

    Attributes:
        state: Target power state.
            - None: Toggle (MIDI only - sends CC 28)
            - True: Ensure ON (requires UI automation fallback)
            - False: Ensure OFF (requires UI automation fallback)

    Note:
        MIDI CC 28 only supports toggle. Explicit on/off requires the
        GlmPowerController UI automation. The consumer should:
        1. For state=None: Send MIDI CC 28 (toggle)
        2. For state=True/False: Use GlmPowerController.set_state() if available,
           otherwise log a warning and fall back to toggle behavior.
    """
    state: Optional[bool] = None


# Union type for type hints
GlmAction = Union[SetVolume, AdjustVolume, SetMute, SetDim, SetPower]


@dataclass
class QueuedAction:
    """
    Wrapper for actions in the queue, carrying timestamp for stale event filtering.

    Input adapters create QueuedAction(action=..., timestamp=time.time())
    and submit to the queue. Consumer checks timestamp to discard stale events.
    """
    action: GlmAction
    timestamp: float
