"""
GlmAction dataclasses - domain actions for GLM control.

These represent what the system should do, independent of input source.
All input adapters (HID, REST, MQTT) create these actions and submit to the queue.
"""
import threading
from dataclasses import dataclass, field
from typing import Optional, Union


class TraceIdGenerator:
    """Thread-safe trace ID generator with source prefixes.

    Each input source (hid, api, mqtt, sys, pwr) gets its own counter.
    IDs are sequential per source: hid-0001, hid-0002, api-0001, etc.
    """

    def __init__(self):
        self._counters: dict = {}
        self._lock = threading.Lock()

    def next(self, source: str) -> str:
        """Generate next trace ID for a source."""
        with self._lock:
            count = self._counters.get(source, 0) + 1
            self._counters[source] = count
            return f"{source}-{count:04d}"


# Global trace ID generator instance
trace_ids = TraceIdGenerator()


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
    Wrapper for actions in the queue, carrying timestamp and trace ID.

    Input adapters create QueuedAction(action=..., timestamp=time.time(), trace_id=trace_ids.next("source"))
    and submit to the queue. Consumer checks timestamp to discard stale events.
    The trace_id follows the action through HID→Queue→Consumer→MIDI TX for log correlation.
    """
    action: GlmAction
    timestamp: float
    trace_id: str = ""
