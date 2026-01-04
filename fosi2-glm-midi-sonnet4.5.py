import time
import signal
import sys
import os
import threading
import queue
from queue import Queue
from enum import Enum
from dataclasses import dataclass
from typing import Dict, Optional, List, Callable
import hid

from glm_core import SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction
from mido import Message, open_output, open_input

# Power control via UI automation (Windows only)
try:
    from PowerOnOff import GlmPowerController, POWER_CONTROL_AVAILABLE, get_display_diagnostics, is_console_session
except ImportError:
    POWER_CONTROL_AVAILABLE = False
    get_display_diagnostics = None
    is_console_session = None
    GlmPowerController = None
import psutil
import argparse

import logging
from logging.handlers import RotatingFileHandler, QueueHandler, QueueListener

# Platform-specific imports (Windows thread priority)
IS_WINDOWS = sys.platform == 'win32'
if IS_WINDOWS:
    try:
        import ctypes
        import win32process
        HAS_WIN32 = True
        # Thread priority constants
        THREAD_PRIORITY_IDLE = win32process.THREAD_PRIORITY_IDLE
        THREAD_PRIORITY_BELOW_NORMAL = win32process.THREAD_PRIORITY_BELOW_NORMAL
        THREAD_PRIORITY_ABOVE_NORMAL = win32process.THREAD_PRIORITY_ABOVE_NORMAL
        THREAD_PRIORITY_HIGHEST = win32process.THREAD_PRIORITY_HIGHEST
    except ImportError:
        HAS_WIN32 = False
        THREAD_PRIORITY_IDLE = THREAD_PRIORITY_BELOW_NORMAL = 0
        THREAD_PRIORITY_ABOVE_NORMAL = THREAD_PRIORITY_HIGHEST = 0
else:
    HAS_WIN32 = False
    THREAD_PRIORITY_IDLE = THREAD_PRIORITY_BELOW_NORMAL = 0
    THREAD_PRIORITY_ABOVE_NORMAL = THREAD_PRIORITY_HIGHEST = 0

# Parameters

MAX_EVENT_AGE = 2.0  # seconds
SEND_DELAY = 0  # seconds for non-volume commands
RETRY_DELAY = 2.0  # seconds
HID_READ_TIMEOUT_MS = 200  # milliseconds - responsive shutdown
QUEUE_MAX_SIZE = 100  # Maximum queued events before backpressure

# Power control timing (UI automation based)
POWER_SETTLING_TIME = 2.0   # Block ALL commands during power settling
POWER_COOLDOWN_TIME = 5.0   # Block power commands after settling ends
POWER_TOTAL_LOCKOUT = POWER_SETTLING_TIME + POWER_COOLDOWN_TIME  # 7s total

# Smart retry logging intervals (absolute milestones from first event)
# Format: list of seconds. If value > prev_log_time, it's an absolute milestone.
#         Otherwise, it's added to prev_log_time. Last value repeats indefinitely.
# Example: [2, 10, 60, 600, 3600, 86400] logs at t=2s, 10s, 60s, 10min, 1hr, 1day from start
# Example: [2, 2, 2, 10, 10, 60] logs at t=2, 4, 6, 10, 20, 60, 120, 180... from start
RETRY_LOG_INTERVALS = [2, 10, 60, 600, 3600, 86400]  # 2s, 10s, 1min, 10min, 1hr, 1day


# ==============================================================================
# SMART RETRY LOGGER - Absolute milestone logging with exponential backoff
# ==============================================================================

class SmartRetryLogger:
    """
    Manages smart logging during retry loops using absolute time milestones.

    Retries continue at their normal frequency (RETRY_DELAY), but log messages
    are throttled based on elapsed time since the first failure.

    Interval values work as follows:
    - If interval > previous_log_time: use as absolute milestone from first event
    - Otherwise: add interval to previous_log_time (cumulative)
    - Last interval repeats indefinitely

    Example: [2, 10, 60, 600, 3600, 86400] → logs at 2s, 10s, 1min, 10min, 1hr, 1day
    Example: [2, 2, 2, 10, 10] → logs at 2s, 4s, 6s, 10s, 20s, 30s...
    """

    def __init__(self, intervals: list = None):
        """
        Initialize the smart retry logger.

        Args:
            intervals: List of interval values (see class docstring for behavior).
                      Last value repeats indefinitely.
                      Defaults to RETRY_LOG_INTERVALS.
        """
        self.intervals = intervals or RETRY_LOG_INTERVALS
        self._trackers: Dict[str, dict] = {}
        self._lock = threading.Lock()

    def _compute_next_log_time(self, prev_log_time: float, interval: float) -> float:
        """
        Compute next log time using the milestone rule.

        If interval > prev_log_time: use interval as absolute milestone
        Otherwise: use prev_log_time + interval (cumulative)
        """
        if interval > prev_log_time:
            return interval
        return prev_log_time + interval

    def should_log(self, key: str) -> bool:
        """
        Check if we should log a retry message for the given key.

        Args:
            key: Unique identifier for this retry context (e.g., "hid_connect", "midi_reader")

        Returns:
            True if enough time has passed since first event, False otherwise.
        """
        now = time.time()

        with self._lock:
            if key not in self._trackers:
                # First attempt - always log
                first_interval = self.intervals[0] if self.intervals else 2
                self._trackers[key] = {
                    'first_event_time': now,
                    'next_log_time': first_interval,  # Absolute time from first event
                    'prev_log_time': 0,  # Track previous log time for milestone calculation
                    'interval_index': 0,
                    'retry_count': 1
                }
                return True

            tracker = self._trackers[key]
            tracker['retry_count'] += 1

            elapsed = now - tracker['first_event_time']

            if elapsed >= tracker['next_log_time']:
                # Time to log - compute next log time
                tracker['prev_log_time'] = tracker['next_log_time']
                tracker['interval_index'] += 1

                # Get next interval (use last value if we've exceeded the list)
                idx = min(tracker['interval_index'], len(self.intervals) - 1)
                next_interval = self.intervals[idx]

                tracker['next_log_time'] = self._compute_next_log_time(
                    tracker['prev_log_time'], next_interval
                )
                return True

            return False

    def get_retry_count(self, key: str) -> int:
        """Get the current retry count for a key."""
        with self._lock:
            if key in self._trackers:
                return self._trackers[key]['retry_count']
            return 0

    def reset(self, key: str):
        """
        Reset the tracker for a key (call when connection succeeds).

        Args:
            key: The retry context key to reset.
        """
        with self._lock:
            if key in self._trackers:
                del self._trackers[key]

    def _format_duration(self, seconds: float) -> str:
        """Format a duration in seconds to a human-readable string."""
        if seconds < 60:
            return f"{int(seconds)}s"
        elif seconds < 3600:
            return f"{int(seconds // 60)}m"
        elif seconds < 86400:
            return f"{int(seconds // 3600)}h"
        else:
            return f"{int(seconds // 86400)}d"

    def format_retry_info(self, key: str) -> str:
        """
        Format retry information for logging.

        Returns a string like "(retry #5)" or "(retry #100, next log at ~10m)"
        """
        with self._lock:
            if key not in self._trackers:
                return ""

            tracker = self._trackers[key]
            count = tracker['retry_count']
            next_log = tracker['next_log_time']

            if tracker['interval_index'] > 0:
                return f"(retry #{count}, next log at ~{self._format_duration(next_log)})"
            else:
                return f"(retry #{count})"


# Global smart retry logger instance
retry_logger = SmartRetryLogger()

# GLM volume initialization timing
GLM_INIT_WAIT = 0.5  # seconds - wait for MIDI reader to connect
GLM_VOL_QUERY_DELAY = 0.1  # seconds - delay between vol+1 and vol-1
GLM_VOL_RESPONSE_WAIT = 0.3  # seconds - wait for GLM to report volume


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
CC_NAMES = {
    GLM_VOLUME_ABS: "Volume",
    GLM_VOL_UP_CC: "Vol+",
    GLM_VOL_DOWN_CC: "Vol-",
    GLM_MUTE_CC: "Mute",
    GLM_DIM_CC: "Dim",
    GLM_POWER_CC: "Power",
}


def log_midi(direction: str, msg_type: str, cc: int = None, value: int = None, channel: int = None, raw: str = None):
    """
    Log MIDI message in consistent format.

    Args:
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


# Catalogue of GLM controls
ACTION_TO_GLM: Dict[Action, GlmControl] = {
    Action.VOL_UP:   GlmControl(cc=GLM_VOL_UP_CC,   label="Vol+",  mode=ControlMode.MOMENTARY),
    Action.VOL_DOWN: GlmControl(cc=GLM_VOL_DOWN_CC, label="Vol-",  mode=ControlMode.MOMENTARY),
    Action.MUTE:     GlmControl(cc=GLM_MUTE_CC,     label="Mute",  mode=ControlMode.TOGGLE),
    Action.DIM:      GlmControl(cc=GLM_DIM_CC,      label="Dim",   mode=ControlMode.TOGGLE),
    Action.POWER:    GlmControl(cc=GLM_POWER_CC,    label="Power", mode=ControlMode.TOGGLE),
    # Non-GLM actions don't have GLM controls (yet)
}

# Reverse lookup: CC number → Action (for reading GLM state from MIDI output)
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
    KEY_CLICK: Action.POWER,         # Click → Power
    KEY_DOUBLE_CLICK: Action.DIM,    # Double click → Dim
    KEY_TRIPLE_CLICK: Action.DIM,    # Triple click → Dim
    KEY_LONG_PRESS: Action.MUTE,     # Long press → Mute
}


# ==============================================================================
# 5) GLM STATE CONTROLLER - Tracks and controls GLM state
# ==============================================================================

class GlmController:
    """Tracks GLM state and provides smart control methods."""

    def __init__(self):
        self.volume: int = 0       # 0-127, confirmed from GLM via CC 20
        self._pending_volume: Optional[int] = None  # What we've sent but GLM hasn't confirmed yet
        self.mute: bool = False    # from CC 23
        self.dim: bool = False     # from CC 24
        self.power: bool = True    # tracked locally (no MIDI feedback from GLM)
        self._volume_initialized = False  # True once we've received volume from GLM
        self._lock = threading.Lock()
        self._state_callbacks: List[Callable[[dict], None]] = []
        self._last_notified_state: Optional[dict] = None  # Debounce duplicate notifications
        # Power transition state
        self._power_transition_start: float = 0  # When power transition started
        self._power_settling: bool = False       # True during power settling period
        self._power_target: Optional[bool] = None  # Target state during transition

    def add_state_callback(self, callback: Callable[[dict], None]):
        """Register a callback to be called when state changes."""
        self._state_callbacks.append(callback)

    def remove_state_callback(self, callback: Callable[[dict], None]):
        """Unregister a state change callback."""
        if callback in self._state_callbacks:
            self._state_callbacks.remove(callback)

    # =========================================================================
    # Power transition management
    # =========================================================================

    def start_power_transition(self, target_state: bool):
        """
        Mark the start of a power transition.

        Called when power command is initiated. Blocks all commands during settling.
        """
        with self._lock:
            self._power_transition_start = time.time()
            self._power_settling = True
            self._power_target = target_state
        self._notify_state_change(force=True)  # Notify UI of transitioning state
        logger.info(f"Power transition started: target={'ON' if target_state else 'OFF'}")

    def end_power_transition(self, success: bool, actual_state: Optional[bool] = None):
        """
        Mark the end of a power transition.

        Called when UI automation confirms state change (or fails).
        """
        with self._lock:
            self._power_settling = False
            if success and actual_state is not None:
                self.power = actual_state
            elif success and self._power_target is not None:
                self.power = self._power_target
            self._power_target = None
        self._notify_state_change(force=True)
        logger.info(f"Power transition ended: success={success}, power={'ON' if self.power else 'OFF'}")

    def is_power_settling(self) -> bool:
        """Check if power is currently settling (2s window)."""
        with self._lock:
            if not self._power_settling:
                return False
            elapsed = time.time() - self._power_transition_start
            if elapsed >= POWER_SETTLING_TIME:
                # Auto-end settling if timeout (shouldn't happen normally)
                self._power_settling = False
                return False
            return True

    def can_accept_command(self) -> tuple:
        """
        Check if any command can be accepted.

        Returns (allowed, wait_time, reason).
        During power settling, ALL commands are blocked.
        """
        with self._lock:
            if not self._power_settling:
                return True, 0, None

            elapsed = time.time() - self._power_transition_start
            if elapsed < POWER_SETTLING_TIME:
                wait = POWER_SETTLING_TIME - elapsed
                return False, wait, "power_settling"

            # Settling done, commands allowed
            self._power_settling = False
            return True, 0, None

    def can_accept_power_command(self) -> tuple:
        """
        Check if a power command can be accepted.

        Returns (allowed, wait_time, reason).
        Power commands are blocked for POWER_TOTAL_LOCKOUT after a power transition.
        """
        with self._lock:
            if self._power_transition_start == 0:
                return True, 0, None

            elapsed = time.time() - self._power_transition_start
            if elapsed < POWER_TOTAL_LOCKOUT:
                wait = POWER_TOTAL_LOCKOUT - elapsed
                if elapsed < POWER_SETTLING_TIME:
                    return False, wait, "power_settling"
                else:
                    return False, wait, "power_cooldown"

            return True, 0, None

    def _notify_state_change(self, force: bool = False):
        """Call all registered callbacks with current state (if changed or forced)."""
        state = self.get_state()
        # Debounce: only notify if state actually changed (unless forced)
        if not force and state == self._last_notified_state:
            return
        self._last_notified_state = state.copy()
        for callback in self._state_callbacks:
            try:
                callback(state)
            except Exception as e:
                logger.error(f"State callback error: {e}")

    @property
    def has_valid_volume(self) -> bool:
        """Check if we have received a valid volume reading from GLM."""
        with self._lock:
            return self._volume_initialized

    def get_effective_volume(self) -> int:
        """
        Get the effective volume for calculating new targets.

        Returns pending volume if we've sent a command that GLM hasn't confirmed yet,
        otherwise returns the last confirmed volume from GLM.
        """
        with self._lock:
            if self._pending_volume is not None:
                return self._pending_volume
            return self.volume

    def get_volume_if_valid(self) -> Optional[int]:
        """
        Atomically check if volume is initialized and return effective volume.

        Returns effective volume (pending or confirmed) if initialized, None otherwise.
        This combines has_valid_volume + get_effective_volume in a single lock acquisition.
        """
        with self._lock:
            if not self._volume_initialized:
                return None
            if self._pending_volume is not None:
                return self._pending_volume
            return self.volume

    def set_pending_volume(self, target: int):
        """Set the pending volume after sending a command."""
        with self._lock:
            self._pending_volume = target

    def update_from_midi(self, cc: int, value: int) -> bool:
        """Update state from MIDI message. Returns True if state changed."""
        changed = False
        notify = False
        force_notify = False  # Force notification even if state unchanged (for clipped values)
        with self._lock:
            if cc == GLM_VOLUME_ABS:
                self._volume_initialized = True
                # Check if GLM clipped/adjusted our requested value
                # If so, force notification to sync UI even if volume unchanged
                if self._pending_volume is not None and self._pending_volume != value:
                    logger.debug(f"GLM clipped volume: sent {self._pending_volume}, got {value}")
                    force_notify = True
                # Clear pending and trust GLM's reported value as source of truth.
                # This ensures we respect GLM's volume limits (e.g., max volume cap).
                self._pending_volume = None
                if self.volume != value:
                    self.volume = value
                    changed = True
                # Always notify on volume to sync UI when GLM clamps values
                notify = True
            elif cc == GLM_MUTE_CC:
                new_mute = value > 0
                if self.mute != new_mute:
                    self.mute = new_mute
                    changed = True
                    notify = True
            elif cc == GLM_DIM_CC:
                new_dim = value > 0
                if self.dim != new_dim:
                    self.dim = new_dim
                    changed = True
                    notify = True

        if notify:
            self._notify_state_change(force=force_notify)
        return changed

    def get_state(self) -> dict:
        """Get current state as a dictionary (for REST API and WebSocket)."""
        with self._lock:
            # Calculate remaining settling/cooldown time
            settling_remaining = 0
            if self._power_transition_start > 0:
                elapsed = time.time() - self._power_transition_start
                if elapsed < POWER_SETTLING_TIME:
                    settling_remaining = POWER_SETTLING_TIME - elapsed

            return {
                "volume": self.volume,
                "volume_db": self.volume - 127,  # 0-127 → -127 to 0 dB
                "mute": self.mute,
                "dim": self.dim,
                "power": self.power,
                "power_transitioning": self._power_settling,
                "power_settling_remaining": round(settling_remaining, 1),
            }

    def send_volume_absolute(self, target: int, midi_output) -> bool:
        """
        Send absolute volume command to GLM via CC 20.
        Target is clamped to 0-127 range.
        Returns True if message was sent.
        """
        target = max(0, min(127, target))
        try:
            midi_output.send(Message('control_change', control=GLM_VOLUME_ABS, value=target))
            log_midi("TX", "control_change", cc=GLM_VOLUME_ABS, value=target)
            return True
        except (OSError, IOError) as e:
            logger.debug(f"Failed to send volume command: {e}")
            return False

    def send_action(self, action: Action, midi_output, explicit_state: Optional[bool] = None) -> bool:
        """
        Send an action to GLM via MIDI.

        For toggle actions (Mute, Dim):
          - If explicit_state is None, toggle based on current state
          - If explicit_state is True/False, set that state explicitly

        For momentary actions (Vol+, Vol-):
          - Always send 127

        Note: Power is now handled via UI automation, not MIDI.

        Returns True if message was sent.
        """
        glm_ctrl = ACTION_TO_GLM.get(action)
        if not glm_ctrl:
            return False  # Action doesn't map to GLM

        with self._lock:
            if glm_ctrl.mode == ControlMode.TOGGLE:
                if action == Action.MUTE:
                    current = self.mute
                elif action == Action.DIM:
                    current = self.dim
                elif action == Action.POWER:
                    current = self.power
                else:
                    current = False

                if explicit_state is None:
                    # Toggle: send opposite of current
                    value = 0 if current else 127
                else:
                    # Explicit: set the requested state
                    value = 127 if explicit_state else 0
            else:
                # Momentary: always send 127
                value = 127

            # Note: Power state is now tracked via MIDI pattern detection in midi_reader,
            # not here. GLM responds to power commands with a 5-message pattern that we detect.

        try:
            midi_output.send(Message('control_change', control=glm_ctrl.cc, value=value))
            log_midi("TX", "control_change", cc=glm_ctrl.cc, value=value)
            return True
        except (OSError, IOError) as e:
            logger.debug(f"Failed to send action {action.value}: {e}")
            return False


# Global GLM controller instance
glm_controller = GlmController()

def validate_volume_increases(value):
    try:
        parsed = list(map(int, value.strip("[]").split(",")))
        if len(parsed) < 2 or len(parsed) > 15:
            raise argparse.ArgumentTypeError("Volume increase list must have between 2 and 15 items.")
        if not all(1 <= x <= 10 for x in parsed):
            raise argparse.ArgumentTypeError("All values in the list must be integers between 1 and 10.")
        return parsed
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"Invalid format for volume_increase_list: {e}")

def validate_click_times(values):
    try:
        # Split and parse the two values
        parsed = list(map(float, values.split(",")))
        if len(parsed) != 2:
            raise argparse.ArgumentTypeError("You must provide exactly two values: MIN_CLICK_TIME,MAX_AVG_CLICK_TIME.")

        min_click_time, max_avg_click_time = parsed

        # Validate the values
        if not (0.01 < min_click_time < 1):
            raise argparse.ArgumentTypeError("MIN_CLICK_TIME must be > 0.01 and < 1.")
        if max_avg_click_time > min_click_time:
            raise argparse.ArgumentTypeError("MAX_AVG_CLICK_TIME must be <= MIN_CLICK_TIME.")

        return min_click_time, max_avg_click_time
    except ValueError:
        raise argparse.ArgumentTypeError("Click times must be two float values separated by a comma.")

def validate_device(value):
    try:
        vid, pid = map(lambda x: int(x, 16), value.split(","))
        if vid < 0x0000 or vid > 0xFFFF or pid < 0x0000 or pid > 0xFFFF:
            raise argparse.ArgumentTypeError("VID and PID must be valid 16-bit hexadecimal values.")
        return vid, pid
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"Invalid VID/PID format: {e}")

def parse_arguments():
    parser = argparse.ArgumentParser(description="HID to MIDI Agent with CLI options.")

    parser.add_argument("--log_level", choices=["DEBUG", "INFO", "NONE"], default="DEBUG",
                        help="Set logging level. Default is DEBUG.")

    # Log file name (defaults to script name with .log extension)
    default_log_file = os.path.splitext(os.path.basename(__file__))[0] + ".log"
    parser.add_argument("--log_file_name", type=str, default=default_log_file,
                        help=f"Name of the log file. Default is '{default_log_file}'.")

    # Single argument for click times
    parser.add_argument("--click_times", type=validate_click_times, default=(0.2, 0.15),
                        help="Comma-separated values for MIN_CLICK_TIME and MAX_AVG_CLICK_TIME. "
                             "MIN_CLICK_TIME must be > 0.01 and < 1, and MAX_AVG_CLICK_TIME must be <= MIN_CLICK_TIME. "
                             "Default is '0.2,0.15'.")

    parser.add_argument("--volume_increases_list", type=validate_volume_increases, default=[1, 1, 2, 2, 3],
                        help="List of volume increases. Must be between 2 and 15 integers, each >=1 and <=10. Default is [1, 1, 2, 2, 3].")


    # VID/PID combination
    parser.add_argument("--device", type=validate_device, default=(0x07d7, 0x0000),
                        help="VID and PID of the device to be listened to, in the format 'VID,PID'. Default is '0x07d7,0x0000'.")

    # MIDI channel names
    parser.add_argument("--midi_in_channel", type=str, default="GLMMIDI 1",
                        help="MIDI input channel name (to send commands TO GLM). Default is 'GLMMIDI 1'.")

    parser.add_argument("--midi_out_channel", type=str, default="GLMOUT 1",
                        help="MIDI output channel name (to receive state FROM GLM). Default is 'GLMOUT 1'.")

    parser.add_argument("--startup_volume", type=int, default=None, choices=range(0, 128), metavar="0-127",
                        help="Optional startup volume (0-127). If set, GLM volume will be set to this value on startup. "
                             "79 corresponds to -46dB in GLM. If not set, script will query current volume.")

    # REST API
    parser.add_argument("--api_port", type=int, default=8080,
                        help="Port for REST API server. Set to 0 to disable API. Default is 8080.")

    # MQTT / Home Assistant
    parser.add_argument("--mqtt_broker", type=str, default=None,
                        help="MQTT broker hostname. If not set, MQTT is disabled.")
    parser.add_argument("--mqtt_port", type=int, default=1883,
                        help="MQTT broker port. Default is 1883.")
    parser.add_argument("--mqtt_user", type=str, default=None,
                        help="MQTT username (optional).")
    parser.add_argument("--mqtt_pass", type=str, default=None,
                        help="MQTT password (optional).")
    parser.add_argument("--mqtt_topic", type=str, default="glm",
                        help="MQTT topic prefix. Default is 'glm'.")
    parser.add_argument("--mqtt_ha_discovery", action="store_true", default=True,
                        help="Enable Home Assistant MQTT Discovery. Default is True.")
    parser.add_argument("--no_mqtt_ha_discovery", action="store_false", dest="mqtt_ha_discovery",
                        help="Disable Home Assistant MQTT Discovery.")

    # Parse arguments
    args = parser.parse_args()

    # Assign parsed click times to individual variables for clarity
    args.min_click_time, args.max_avg_click_time = args.click_times
    args.vid, args.pid = args.device
    return args


def set_higher_priority():
    try:
        p = psutil.Process(os.getpid())
        p.nice(psutil.ABOVE_NORMAL_PRIORITY_CLASS)  # Set to Above Normal
        logger.debug("Main Process priority set to AboveNormal.")
    except Exception as e:
        logger.warning(f"Failed to set higher priority: {e}")

def set_current_thread_priority(priority_level):
    """Set the priority of the current thread (Windows only)."""
    if not HAS_WIN32:
        return  # Skip on non-Windows platforms

    thread_name = threading.current_thread().name
    thread_id = threading.get_ident()
    try:
        thread_handle = ctypes.windll.kernel32.GetCurrentThread()
        success = win32process.SetThreadPriority(thread_handle, priority_level)
        if not success:
            last_error = ctypes.windll.kernel32.GetLastError()
            if last_error != 0:
                raise ctypes.WinError(last_error)
        logger.debug(f"Set priority of thread '{thread_name}' (ID: {thread_id}) to {priority_level}.")
    except Exception as e:
        logger.warning(f"Failed to set priority for thread '{thread_name}' (ID: {thread_id}): {e}")


# Setup logging
def setup_logging(log_level, log_file_name, max_bytes=4*1024*1024, backup_count=5):
    script_directory = os.path.dirname(os.path.abspath(__file__))
    log_file_path = os.path.join(script_directory, log_file_name)

    log_queue = Queue()

    # File Handler
    file_handler = RotatingFileHandler(log_file_path, maxBytes=max_bytes, backupCount=backup_count)
    file_handler.setLevel(logging.DEBUG if log_level != "NONE" else logging.CRITICAL)
    file_handler.setFormatter(logging.Formatter('%(asctime)s [%(levelname)s] %(message)s'))

    # Console Handler
    console_handler = logging.StreamHandler()
    console_handler.setLevel(logging.INFO if log_level in ["INFO", "DEBUG"] else logging.CRITICAL)
    console_handler.setFormatter(logging.Formatter('%(asctime)s [%(levelname)s] %(message)s'))

    # QueueHandler
    queue_handler = QueueHandler(log_queue)

    # Root Logger
    root_logger = logging.getLogger()
    root_logger.handlers = []  # Clear all handlers
    root_logger.setLevel(logging.DEBUG if log_level == "DEBUG" else logging.INFO)
    root_logger.addHandler(queue_handler)

    # Custom Module Logger
    global logger
    logger = logging.getLogger(__name__)
    logger.setLevel(logging.DEBUG if log_level == "DEBUG" else logging.INFO)
    logger.addHandler(console_handler)  # Optional: Direct console output
    logger.addHandler(file_handler)     # Optional: Direct file output
    logger.propagate = False  # Avoid double logging

    # Listener Thread
    stop_event = threading.Event()
    logger.info(f">----- Starting {os.path.basename(__file__)} agent. Logger setup complete. Initializing application...")

    def log_listener_thread():
        listener = QueueListener(log_queue, file_handler, console_handler)
        listener.start()

        # Lower thread priority
        set_current_thread_priority(THREAD_PRIORITY_IDLE)

        stop_event.wait()
        listener.stop()

    logging_thread = threading.Thread(target=log_listener_thread, name="LoggingThread", daemon=False)
    logging_thread.start()

    def stop_logging():
        stop_event.set()

    return stop_logging

def signal_handler(sig, frame, daemon, stop_logging_func):
    """Handles SIGINT and shuts down the daemon."""
    logger.info("SIGINT received, shutting down...")
    daemon.stop()
    stop_logging_func()
    sys.exit(0)

class AccelerationHandler:
    def __init__(self, min_click, max_per_click_avg, volume_list):
        self.min_click = min_click
        self.max_per_click_avg = max_per_click_avg
        self.volume_increases_list = volume_list
        self.len = len(volume_list)  # Cache length (list is immutable)
        self.last_button = 0
        self.last_time = 0
        self.first_time = 0
        self.distance = 0
        self.count = 1
        self.delta_time = 0

    def calculate_speed(self, current_time, button):
        self.delta_time = current_time - self.last_time
        # Guard against division by zero (shouldn't happen with count initialized to 1)
        if self.count > 0:
            avg_step_time = (current_time - self.first_time) / self.count
        else:
            avg_step_time = float('inf')

        if (self.last_button != button) or (avg_step_time > self.max_per_click_avg) or (self.delta_time > self.min_click):
            self.distance = 1
            self.count = 1
            self.first_time = current_time
        else:
            if self.count <= self.len:  # count 1..len maps to indices 0..len-1
                self.distance = self.volume_increases_list[self.count - 1]
            else:
                self.distance = self.volume_increases_list[-1]
            self.count += 1
        self.last_button = button
        self.last_time = current_time
        return int(self.distance)

class HIDToMIDIDaemon:
    def __init__(self, min_click_time, max_avg_click_time, volume_increases_list,
                 VID, PID, midi_in_channel, midi_out_channel, startup_volume=None, api_port=8080,
                 mqtt_broker=None, mqtt_port=1883, mqtt_user=None, mqtt_pass=None,
                 mqtt_topic="glm", mqtt_ha_discovery=True):
        self.queue = queue.Queue(maxsize=QUEUE_MAX_SIZE)
        self._stop_event = threading.Event()
        self.hid_reader_thread = threading.Thread(target=self.hid_reader, daemon=True, name="HIDReaderThread")
        self.midi_reader_thread = threading.Thread(target=self.midi_reader, daemon=True, name="MIDIReaderThread")
        self.consumer_thread = threading.Thread(target=self.consumer, daemon=True, name="ConsumerThread")
        self.volume_knob = AccelerationHandler(min_click_time, max_avg_click_time, volume_increases_list)
        self.midi_in_channel = midi_in_channel
        self.midi_out_channel = midi_out_channel
        self.vid = VID
        self.pid = PID
        self._midi_output = None  # Shared MIDI output for sending to GLM
        self._midi_output_lock = threading.Lock()  # Protects _midi_output access
        self.midi_input = None   # MIDI input for reading GLM state
        self.hid_device = None   # HID device handle for cleanup
        self.startup_volume = startup_volume  # Optional startup volume (0-127)
        self.bindings = DEFAULT_BINDINGS.copy()  # Instance-level key bindings
        self.api_port = api_port  # REST API port (0 = disabled)
        self.api_thread = None   # API server thread
        # MQTT settings
        self.mqtt_broker = mqtt_broker
        self.mqtt_port = mqtt_port
        self.mqtt_user = mqtt_user
        self.mqtt_pass = mqtt_pass
        self.mqtt_topic = mqtt_topic
        self.mqtt_ha_discovery = mqtt_ha_discovery
        self.mqtt_client = None  # MQTT client instance
        # Power pattern detection state (legacy, kept for MIDI state sync)
        self._rx_seq = []  # List of (timestamp, cc) for pattern detection
        self._last_pattern_time = None  # For startup detection (double-burst)
        # Power control via UI automation
        self._power_controller = None
        if POWER_CONTROL_AVAILABLE:
            try:
                # Log display/session diagnostics (for debugging)
                if get_display_diagnostics:
                    diag = get_display_diagnostics()
                    logger.info(f"Display diagnostics: session={diag.get('current_session_id')}, "
                               f"console={diag.get('console_session_id')}, "
                               f"rdp={diag.get('is_rdp_session')}, "
                               f"monitors={diag.get('monitor_count')}, "
                               f"glm_windows={len(diag.get('glm_windows', []))}")

                # Always try to initialize - let it fail naturally if display inaccessible
                self._power_controller = GlmPowerController(steal_focus=True)
                logger.info("GlmPowerController initialized for UI-based power control")
            except Exception as e:
                logger.warning(f"GlmPowerController not available: {e}")

    def _get_midi_output(self):
        """Get connected MIDI output, reconnecting if necessary. Thread-safe."""
        with self._midi_output_lock:
            if self._midi_output is None:
                try:
                    self._midi_output = open_output(self.midi_in_channel)
                    logger.info(f"Connected to MIDI channel '{self.midi_in_channel}'.")
                    retry_logger.reset("midi_output")  # Reset on successful connection
                except (OSError, IOError) as e:
                    if retry_logger.should_log("midi_output"):
                        info = retry_logger.format_retry_info("midi_output")
                        logger.warning(f"Failed to connect to MIDI channel '{self.midi_in_channel}': {e} {info}")
                    return None
            return self._midi_output

    def _reset_midi_output(self):
        """Reset MIDI output connection (call after send error). Thread-safe."""
        with self._midi_output_lock:
            if self._midi_output:
                try:
                    self._midi_output.close()
                except (OSError, IOError):
                    logger.debug("Error closing MIDI output during reset")
            self._midi_output = None

    def hid_reader(self):
        """Reads events from the HID device and puts them in the queue."""
        set_current_thread_priority(THREAD_PRIORITY_HIGHEST)
        while not self._stop_event.is_set():
            if self.hid_device is None:
                try:
                    self.hid_device = hid.device()
                    self.hid_device.open(self.vid, self.pid)
                    logger.info(f"Connected to HID device VID: {hex(self.vid)} PID: {hex(self.pid)}.")
                    retry_logger.reset("hid_connect")  # Reset on successful connection
                except (OSError, IOError) as e:
                    if retry_logger.should_log("hid_connect"):
                        info = retry_logger.format_retry_info("hid_connect")
                        logger.warning(f"Failed to open HID device: {e}. Retrying... {info}")
                    self.hid_device = None
                    time.sleep(RETRY_DELAY)
                    continue

            try:
                report = self.hid_device.read(3, timeout_ms=HID_READ_TIMEOUT_MS)
                if report:
                    keyreported = report[0]
                    if keyreported == 0:
                        continue
                    now = time.time()

                    # Map physical key to logical action
                    action_type = self.bindings.get(keyreported)
                    if not action_type:
                        logger.debug(f"No binding for key {KEY_NAMES.get(keyreported, keyreported)}")
                        continue

                    # Create appropriate GlmAction based on action type
                    if action_type == Action.VOL_UP:
                        distance = self.volume_knob.calculate_speed(now, keyreported)
                        glm_action = AdjustVolume(delta=distance)
                    elif action_type == Action.VOL_DOWN:
                        distance = self.volume_knob.calculate_speed(now, keyreported)
                        glm_action = AdjustVolume(delta=-distance)
                    elif action_type == Action.MUTE:
                        glm_action = SetMute()
                    elif action_type == Action.DIM:
                        glm_action = SetDim()
                    elif action_type == Action.POWER:
                        glm_action = SetPower()
                    else:
                        # Non-GLM actions (PLAY_PAUSE, etc.) - skip for now
                        logger.debug(f"Action {action_type.value} not yet supported")
                        continue

                    self.queue.put(QueuedAction(action=glm_action, timestamp=now))
                    logger.debug(f"HID: key={KEY_NAMES.get(keyreported, keyreported)} -> {glm_action}")
            except (OSError, IOError) as e:
                if retry_logger.should_log("hid_error"):
                    info = retry_logger.format_retry_info("hid_error")
                    logger.warning(f"HID device error: {e}. Reconnecting... {info}")
                if self.hid_device:
                    try:
                        self.hid_device.close()
                    except (OSError, IOError):
                        logger.debug("Error closing HID device during reconnect")
                self.hid_device = None
                retry_logger.reset("hid_connect")  # Reset connect tracker since we need to reconnect
                time.sleep(RETRY_DELAY)

    def midi_reader(self):
        """Reads MIDI messages from GLMOUT and updates GLM state."""
        set_current_thread_priority(THREAD_PRIORITY_ABOVE_NORMAL)  # Match consumer for balanced send/receive

        while not self._stop_event.is_set():
            try:
                self.midi_input = open_input(self.midi_out_channel)
                logger.info(f"Connected to MIDI output channel '{self.midi_out_channel}' for state reading.")
                retry_logger.reset("midi_reader")  # Reset on successful connection

                # Blocking iteration - waits for messages, no polling
                for msg in self.midi_input:
                    if self._stop_event.is_set():
                        break
                    # Log ALL received MIDI messages
                    if msg.type == 'control_change':
                        log_midi("RX", "control_change", cc=msg.control, value=msg.value)

                        # Power pattern detection
                        now = time.time()
                        self._rx_seq.append((now, msg.control))
                        # Keep only messages within time window
                        self._rx_seq = [(t, c) for (t, c) in self._rx_seq
                                       if now - t <= POWER_PATTERN_WINDOW]

                        seq = [c for _, c in self._rx_seq]
                        if len(seq) >= 5 and seq[-5:] == POWER_PATTERN:
                            time_span = self._rx_seq[-1][0] - self._rx_seq[-5][0]
                            if time_span >= POWER_PATTERN_MIN_SPAN:  # Not a buffer dump
                                if len(seq) == 5:
                                    # Clean 5-message burst
                                    if (self._last_pattern_time and
                                        (now - self._last_pattern_time) < POWER_STARTUP_WINDOW):
                                        # Second pattern within window = GLM startup
                                        old_power = glm_controller.power
                                        glm_controller.power = True  # Sync to ON
                                        logger.info(f"GLM startup detected - power synced to ON (was {'ON' if old_power else 'OFF'})")
                                        glm_controller._notify_state_change()
                                        self._last_pattern_time = None
                                    else:
                                        # Skip toggle detection during power cooldown
                                        # UI automation verified state is authoritative
                                        allowed, wait_time, _ = glm_controller.can_accept_power_command()
                                        if not allowed:
                                            logger.debug(f"MIDI power pattern ignored during cooldown ({wait_time:.1f}s remaining)")
                                        else:
                                            # Single burst = real power toggle
                                            glm_controller.power = not glm_controller.power
                                            logger.info(f"Power toggle detected (now {'ON' if glm_controller.power else 'OFF'})")
                                            glm_controller._notify_state_change()
                                            self._last_pattern_time = now
                                    self._rx_seq = []  # Clear after detection
                                else:
                                    # Burst with extra messages (len > 5) - likely first burst of startup
                                    # Record time but don't toggle - wait for second burst
                                    logger.debug(f"Power pattern with {len(seq)} msgs - recording for startup detection")
                                    self._last_pattern_time = now
                                    self._rx_seq = []

                        # Process state update
                        changed = glm_controller.update_from_midi(msg.control, msg.value)
                        if changed:
                            state = glm_controller.get_state()
                            logger.debug(f"GLM state: vol={state['volume']}, mute={state['mute']}, dim={state['dim']}, pwr={state['power']}")
                    else:
                        # Log non-control_change messages (unexpected but want to see them)
                        log_midi("RX", msg.type, raw=str(msg))

            except (OSError, IOError) as e:
                if not self._stop_event.is_set():  # Only log if not shutting down
                    if retry_logger.should_log("midi_reader"):
                        info = retry_logger.format_retry_info("midi_reader")
                        logger.warning(f"MIDI reader error: {e}. Reconnecting... {info}")
                    time.sleep(RETRY_DELAY)
            finally:
                if self.midi_input:
                    try:
                        self.midi_input.close()
                    except (OSError, IOError):
                        logger.debug("Error closing MIDI input during cleanup")
                    self.midi_input = None

    def consumer(self):
        """Processes GlmAction objects from the queue and sends MIDI messages."""
        set_current_thread_priority(THREAD_PRIORITY_ABOVE_NORMAL)

        # Wait for initial MIDI connection
        while self._get_midi_output() is None and not self._stop_event.is_set():
            time.sleep(RETRY_DELAY)

        while True:
            queued = self.queue.get()
            if queued is None:  # Sentinel for consumer shutdown
                logger.info("Consumer thread exiting...")
                break

            # Handle QueuedAction objects
            now = time.time()
            event_age = now - queued.timestamp
            if event_age > MAX_EVENT_AGE:
                logger.warning(f"Discarded stale action: {queued.action}")
                continue

            action = queued.action

            # Check if commands are blocked during power settling
            if isinstance(action, SetPower):
                # Power commands have extended cooldown
                allowed, wait_time, reason = glm_controller.can_accept_power_command()
                if not allowed:
                    if reason == "power_settling":
                        logger.warning(f"Power command blocked: settling ({wait_time:.1f}s remaining)")
                    else:
                        logger.warning(f"Power command blocked: cooldown ({wait_time:.1f}s remaining)")
                    continue
            else:
                # All other commands blocked only during settling
                allowed, wait_time, reason = glm_controller.can_accept_command()
                if not allowed:
                    logger.warning(f"Command blocked: power settling ({wait_time:.1f}s remaining)")
                    continue

            # Dispatch based on action type
            try:
                if isinstance(action, SetVolume):
                    self._handle_set_volume(action.target)
                elif isinstance(action, AdjustVolume):
                    self._handle_adjust_volume(action.delta)
                elif isinstance(action, SetMute):
                    logger.debug(f"Sending Mute (CC {GLM_MUTE_CC})")
                    self._send_action(Action.MUTE)
                    time.sleep(SEND_DELAY)
                elif isinstance(action, SetDim):
                    logger.debug(f"Sending Dim (CC {GLM_DIM_CC})")
                    self._send_action(Action.DIM)
                    time.sleep(SEND_DELAY)
                elif isinstance(action, SetPower):
                    self._handle_power_action(action)
                else:
                    logger.debug(f"Unknown action type: {type(action).__name__}")
            except Exception as e:
                logger.error(f"Error processing action {action}: {e}", exc_info=True)

    def _send_action(self, action: Action):
        """Send an action to GLM using the controller."""
        midi_out = self._get_midi_output()
        if midi_out is None:
            logger.warning("MIDI output not connected, skipping action.")
            return

        try:
            glm_controller.send_action(action, midi_out)
        except (OSError, IOError) as e:
            logger.error(f"Error sending MIDI message: {e}")
            self._reset_midi_output()

    def _handle_power_action(self, action: SetPower):
        """
        Handle power control via UI automation.

        This uses GlmPowerController to click the power button in GLM,
        providing deterministic state control with verification.
        """
        if self._power_controller is None:
            logger.error("Power control unavailable: GlmPowerController not initialized")
            return

        # Determine target state
        if action.state is None:
            # Toggle: invert current state
            target_state = not glm_controller.power
        else:
            target_state = action.state

        desired = "on" if target_state else "off"
        logger.info(f"Power command: setting to {desired.upper()} via UI automation")

        # Start power transition (blocks all commands)
        glm_controller.start_power_transition(target_state)
        transition_start = time.time()

        success = False
        try:
            # Execute via UI automation
            self._power_controller.set_state(desired, verify=True)
            success = True
        except Exception as e:
            logger.error(f"Power control failed: {e}")

        # Wait for full settling time before ending transition
        # This ensures UI shows transitioning state for the full 2 seconds
        elapsed = time.time() - transition_start
        if elapsed < POWER_SETTLING_TIME:
            remaining = POWER_SETTLING_TIME - elapsed
            logger.debug(f"Waiting {remaining:.1f}s for power settling")
            time.sleep(remaining)

        # Now end transition (UI will stop showing transitioning state)
        if success:
            glm_controller.end_power_transition(success=True, actual_state=target_state)
        else:
            glm_controller.end_power_transition(success=False)

    def _handle_adjust_volume(self, delta: int):
        """
        Handle volume changes using absolute volume (CC 20) when possible.

        Args:
            delta: Volume change amount. Positive = up, negative = down.

        If we have a valid volume reading from GLM, calculate target and send
        one absolute command. This avoids GLM dropping rapid increment commands.

        If volume is not yet initialized, fall back to single CC 21/22.
        """
        midi_out = self._get_midi_output()
        if midi_out is None:
            logger.warning("MIDI output not connected, skipping volume action.")
            return

        try:
            # Atomically check if volume is initialized and get effective volume
            current = glm_controller.get_volume_if_valid()
            if current is not None:
                # Calculate target volume based on effective volume (pending or confirmed)
                # This allows consecutive commands to accumulate before GLM confirms
                target = max(0, min(127, current + delta))

                if target != current:
                    sign = '+' if delta > 0 else ''
                    logger.debug(f"Volume: {current} -> {target} (delta={sign}{delta}, CC 20)")
                    glm_controller.set_pending_volume(target)
                    glm_controller.send_volume_absolute(target, midi_out)
                else:
                    direction = "up" if delta > 0 else "down"
                    logger.debug(f"Volume already at limit ({current}), ignoring {direction}")
            else:
                # Volume not initialized yet - use CC 21/22 to trigger GLM state report
                action = Action.VOL_UP if delta > 0 else Action.VOL_DOWN
                logger.debug(f"Volume not initialized, using {action.value} (CC 21/22) to trigger state")
                glm_controller.send_action(action, midi_out)
        except (OSError, IOError) as e:
            logger.error(f"Error handling volume action: {e}")
            self._reset_midi_output()

    def _handle_set_volume(self, target: int):
        """
        Handle absolute volume setting (from REST API).

        Args:
            target: Target volume (0-127).
        """
        midi_out = self._get_midi_output()
        if midi_out is None:
            logger.warning("MIDI output not connected, skipping volume action.")
            return

        target = max(0, min(127, target))
        try:
            logger.debug(f"Setting volume to {target} (CC 20)")
            glm_controller.set_pending_volume(target)
            glm_controller.send_volume_absolute(target, midi_out)
        except (OSError, IOError) as e:
            logger.error(f"Error setting volume: {e}")
            self._reset_midi_output()

    def _initialize_glm_volume(self):
        """
        Initialize GLM volume state on startup.

        If startup_volume is set, send that absolute volume to GLM.
        Otherwise, send vol+1 then vol-1 to trigger GLM to report its current volume.

        This must be called AFTER midi_reader is started so we can receive the response.
        """
        # Wait for MIDI output connection
        midi_out = None
        while midi_out is None and not self._stop_event.is_set():
            midi_out = self._get_midi_output()
            if midi_out is None:
                time.sleep(RETRY_DELAY)

        if midi_out is None:
            return  # Shutting down

        # Wait a moment for MIDI reader to connect and be ready
        time.sleep(GLM_INIT_WAIT)

        if self.startup_volume is not None:
            # Set volume to specified value
            logger.info(f"Setting startup volume to {self.startup_volume}")
            glm_controller.send_volume_absolute(self.startup_volume, midi_out)
        else:
            # Query current volume by sending vol+1 then vol-1
            logger.info("Querying current GLM volume (sending vol+1, vol-1)...")
            glm_controller.send_action(Action.VOL_UP, midi_out)
            time.sleep(GLM_VOL_QUERY_DELAY)
            glm_controller.send_action(Action.VOL_DOWN, midi_out)

        # Wait for GLM to respond with volume state
        time.sleep(GLM_VOL_RESPONSE_WAIT)
        if glm_controller.has_valid_volume:
            logger.info(f"GLM volume initialized: {glm_controller.volume}")
        else:
            logger.warning("GLM volume state not yet received. Will initialize on first volume command.")

    def start(self):
        """Starts all threads."""
        # Register state change callback for logging (proof of concept)
        def log_state_change(state: dict):
            transitioning = " [TRANSITIONING]" if state.get('power_transitioning') else ""
            logger.info(f"State changed: vol={state['volume']}, mute={state['mute']}, dim={state['dim']}, pwr={state['power']}{transitioning}")
        glm_controller.add_state_callback(log_state_change)

        # Sync power state from GLM UI (before starting threads)
        if self._power_controller:
            try:
                state = self._power_controller.get_state()
                if state in ("on", "off"):
                    glm_controller.power = (state == "on")
                    logger.info(f"Power state synced from GLM UI: {state.upper()}")
                else:
                    logger.warning(f"Could not determine GLM power state: {state}")
            except Exception as e:
                logger.warning(f"Failed to sync power state: {e}")

        # Start MIDI reader first so we can receive GLM responses
        self.midi_reader_thread.start()

        # Initialize GLM volume state
        self._initialize_glm_volume()

        # Start remaining threads
        self.hid_reader_thread.start()
        self.consumer_thread.start()

        # Start REST API server if enabled
        if self.api_port > 0:
            from api import start_api_server
            self.api_thread = start_api_server(self.queue, glm_controller, port=self.api_port)

        # Start MQTT client if enabled
        if self.mqtt_broker:
            from api.mqtt import start_mqtt_client
            self.mqtt_client = start_mqtt_client(
                action_queue=self.queue,
                glm_controller=glm_controller,
                broker=self.mqtt_broker,
                port=self.mqtt_port,
                username=self.mqtt_user,
                password=self.mqtt_pass,
                topic_prefix=self.mqtt_topic,
                ha_discovery=self.mqtt_ha_discovery,
            )

    def stop(self):
        """Stops the daemon gracefully."""
        logger.info("Stopping daemon...")
        self._stop_event.set()
        self.queue.put(None)  # Sentinel to unblock the consumer

        # Stop MQTT client
        if self.mqtt_client:
            self.mqtt_client.stop()

        # Close MIDI input to unblock the blocking read
        if self.midi_input:
            try:
                self.midi_input.close()
            except (OSError, IOError):
                logger.debug("Error closing MIDI input during shutdown")

        # Close HID device
        if self.hid_device:
            try:
                self.hid_device.close()
                logger.debug("HID device closed.")
            except (OSError, IOError):
                logger.debug("Error closing HID device during shutdown")
            self.hid_device = None

        # Close MIDI output
        self._reset_midi_output()

        # Give threads a moment to exit cleanly (they're daemon threads)
        # This allows graceful shutdown but doesn't block if they're stuck
        time.sleep(0.1)

        logger.info("Daemon stopped.")

if __name__ == "__main__":
    args = parse_arguments()
    stop_logging = setup_logging(args.log_level, args.log_file_name)

    # Log the configurations for confirmation
    logger.info(f"---> Configuration:")
    logger.info(f"     Click times: min={args.min_click_time}, max_avg={args.max_avg_click_time}")
    logger.info(f"     Volume acceleration: {args.volume_increases_list}")
    logger.info(f"     Log level: {args.log_level}, file: {args.log_file_name}")
    logger.info(f"     MIDI IN (to GLM): {args.midi_in_channel}")
    logger.info(f"     MIDI OUT (from GLM): {args.midi_out_channel}")
    logger.info(f"     HID Device: VID={hex(args.vid)}, PID={hex(args.pid)}")
    if args.startup_volume is not None:
        logger.info(f"     Startup volume: {args.startup_volume}")
    else:
        logger.info(f"     Startup volume: (query current)")
    if args.api_port > 0:
        logger.info(f"     REST API: http://0.0.0.0:{args.api_port}")
    else:
        logger.info(f"     REST API: disabled")
    if args.mqtt_broker:
        logger.info(f"     MQTT: {args.mqtt_broker}:{args.mqtt_port} (topic: {args.mqtt_topic})")
        logger.info(f"     MQTT HA Discovery: {args.mqtt_ha_discovery}")
    else:
        logger.info(f"     MQTT: disabled")
    logger.info(f"<--- End configuration")

    # Check if another instance is already running (by checking if API port is in use)
    if args.api_port > 0:
        import socket
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(1)
        result = sock.connect_ex(('127.0.0.1', args.api_port))
        sock.close()
        if result == 0:
            logger.error(f"Another instance is already running (port {args.api_port} in use). Exiting.")
            sys.exit(1)

    set_higher_priority()
    time.sleep(2.0)

    daemon = HIDToMIDIDaemon(
        args.min_click_time,
        args.max_avg_click_time,
        args.volume_increases_list,
        args.vid,
        args.pid,
        args.midi_in_channel,
        args.midi_out_channel,
        args.startup_volume,
        args.api_port,
        args.mqtt_broker,
        args.mqtt_port,
        args.mqtt_user,
        args.mqtt_pass,
        args.mqtt_topic,
        args.mqtt_ha_discovery,
    )
    signal.signal(signal.SIGINT, lambda sig, frame: signal_handler(sig, frame, daemon, stop_logging))
    daemon.start()

    try:
        while True:
            time.sleep(3)  # Keep the main thread alive
    except KeyboardInterrupt:
        signal_handler(None, None, daemon, stop_logging)
    finally:
        stop_logging()
