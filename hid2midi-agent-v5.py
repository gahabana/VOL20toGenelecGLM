import time
import signal
import sys
import os
import threading
import queue
from queue import Queue
from enum import Enum
from dataclasses import dataclass
from typing import Dict, Optional
import hid
from mido import Message, open_output, open_input
import psutil
import argparse

import logging
from logging.handlers import RotatingFileHandler, QueueHandler, QueueListener

import ctypes
import win32api
import win32process
import win32con

# Parameters

MAX_EVENT_AGE = 2.0  # seconds
SEND_DELAY = 0  # seconds (0 seconds is just OS yield to other threads if needed ...
#               # i.e. MIDI receiving app .. it also work with 0.0005 sec (0.5ms) but i found it not to be needed)
RETRY_DELAY = 2.0  # seconds


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

# Catalogue of GLM controls
ACTION_TO_GLM: Dict[Action, GlmControl] = {
    Action.VOL_UP:   GlmControl(cc=GLM_VOL_UP_CC,   label="Vol+",  mode=ControlMode.MOMENTARY),
    Action.VOL_DOWN: GlmControl(cc=GLM_VOL_DOWN_CC, label="Vol-",  mode=ControlMode.MOMENTARY),
    Action.MUTE:     GlmControl(cc=GLM_MUTE_CC,     label="Mute",  mode=ControlMode.TOGGLE),
    Action.DIM:      GlmControl(cc=GLM_DIM_CC,      label="Dim",   mode=ControlMode.TOGGLE),
    Action.POWER:    GlmControl(cc=GLM_POWER_CC,    label="Power", mode=ControlMode.MOMENTARY),
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

# Active bindings (can be modified at runtime)
BINDINGS: Dict[int, Action] = DEFAULT_BINDINGS.copy()


# ==============================================================================
# 5) GLM STATE CONTROLLER - Tracks and controls GLM state
# ==============================================================================

class GlmController:
    """Tracks GLM state and provides smart control methods."""

    def __init__(self):
        self.volume: int = 0       # 0-127, from CC 20
        self.mute: bool = False    # from CC 23
        self.dim: bool = False     # from CC 24
        self.power: bool = True    # tracked locally (no MIDI feedback from GLM)
        self._lock = threading.Lock()
        self._volume_changed = threading.Event()

    def update_from_midi(self, cc: int, value: int) -> bool:
        """Update state from MIDI message. Returns True if state changed."""
        with self._lock:
            if cc == GLM_VOLUME_ABS:
                if self.volume != value:
                    self.volume = value
                    self._volume_changed.set()  # Signal volume change
                    return True
            elif cc == GLM_MUTE_CC:
                new_mute = value > 0
                if self.mute != new_mute:
                    self.mute = new_mute
                    return True
            elif cc == GLM_DIM_CC:
                new_dim = value > 0
                if self.dim != new_dim:
                    self.dim = new_dim
                    return True
            return False

    def wait_for_volume_change(self, timeout: float = 0.15) -> bool:
        """Wait for GLM to confirm volume change. Returns True if confirmed."""
        # Note: Event should be cleared BEFORE sending the command (by caller)
        return self._volume_changed.wait(timeout)

    def clear_volume_change_event(self):
        """Clear the volume change event before sending a command."""
        self._volume_changed.clear()

    def get_state(self) -> dict:
        """Get current state as a dictionary (for future REST API)."""
        with self._lock:
            return {
                "volume": self.volume,
                "mute": self.mute,
                "dim": self.dim,
                "power": self.power,
            }

    def send_action(self, action: Action, midi_output, explicit_state: Optional[bool] = None) -> bool:
        """
        Send an action to GLM via MIDI.

        For toggle actions (Mute, Dim):
          - If explicit_state is None, toggle based on current state
          - If explicit_state is True/False, set that state explicitly

        For momentary actions (Vol+, Vol-, Power):
          - Always send 127

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

            # Special handling for power (track locally since no MIDI feedback)
            if action == Action.POWER:
                self.power = not self.power

        try:
            midi_output.send(Message('control_change', control=glm_ctrl.cc, value=value))
            return True
        except Exception:
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
    except Exception as e:
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
    except Exception as e:
        raise argparse.ArgumentTypeError(f"Invalid VID/PID format: {e}")
    
def parse_arguments():
    parser = argparse.ArgumentParser(description="HID to MIDI Agent with CLI options.")

    parser.add_argument("--log_level", choices=["DEBUG", "INFO", "NONE"], default="DEBUG",
                        help="Set logging level. Default is DEBUG.")

    # Log file name
    parser.add_argument("--log_file_name", type=str, default="hid_to_midi.log",
                        help="Name of the log file. Default is 'hid_to_midi.log'.")
    
    # Single argument for click times
    parser.add_argument("--click_times", type=validate_click_times, default=(0.2, 0.15),
                        help="Comma-separated values for MIN_CLICK_TIME and MAX_AVG_CLICK_TIME. "
                             "MIN_CLICK_TIME must be > 0.01 and < 1, and MAX_AVG_CLICK_TIME must be <= MIN_CLICK_TIME. "
                             "Default is '0.2,0.18'.")
    
    parser.add_argument("--volume_increases_list", type=validate_volume_increases, default=[1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 4],
                        help="List of volume increases. Must be between 2 and 15 integers, each >=1 and <=10. Default is [1, 2, 2, 2, 3, 3, 3, 4].")
    
    
    # VID/PID combination
    parser.add_argument("--device", type=validate_device, default=(0x07d7, 0x0000),
                        help="VID and PID of the device to be listened to, in the format 'VID,PID'. Default is '0x07d7,0x0000'.")

    # MIDI channel names
    parser.add_argument("--midi_in_channel", type=str, default="GLMMIDI 1",
                        help="MIDI input channel name (to send commands TO GLM). Default is 'GLMMIDI 1'.")

    parser.add_argument("--midi_out_channel", type=str, default="GLMOUT 1",
                        help="MIDI output channel name (to receive state FROM GLM). Default is 'GLMOUT 1'.")

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
        # p.ionice(psutil.IOPRIO_HIGH) ... i get an error
        logger.debug("Main Process priority set to AboveNormal.")
    except Exception as e:
        print(f"Failed to set higher priority: {e}")

def set_current_thread_priority(priority_level):
    """Set the priority of the current thread."""
    try:
        # Get the current thread handle
        thread_handle = ctypes.windll.kernel32.GetCurrentThread()
        # Get the thread's name and ID
        thread_name = threading.current_thread().name
        thread_id = threading.get_ident()
        # Set the thread priority
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
        # logger.info(f">----- Starting {os.path.basename(__file__)} agent. Logger setup complete. Initializing application...")

        # Lower thread priority
        set_current_thread_priority(win32process.THREAD_PRIORITY_IDLE)

        stop_event.wait()
        listener.stop()

    logging_thread = threading.Thread(target=log_listener_thread, name="LoggingThread", daemon=False)
    logging_thread.start()

    def stop_logging():
        stop_event.set()

    return stop_logging

def signal_handler(sig, frame, daemon):
    """Handles SIGINT and shuts down the daemon."""
    logger.info("SIGINT received, shutting down...")
    daemon.stop()
    sys.exit(0)

class AccelerationHandler:
    def __init__(self, min_click, max_per_click_avg, volume_list):
        self.min_click = min_click
        self.max_per_click_avg = max_per_click_avg
        self.volume_increases_list = volume_list
        self.last_button = 0
        self.last_time = 0
        self.first_time = 0
        self.distance = 0
        self.count = 1
        self.delta_time = 0

    def calculate_speed(self, current_time, button):
        self.delta_time = current_time - self.last_time
        avg_step_time = (current_time - self.first_time) / self.count
        if (self.last_button != button) or (avg_step_time > self.max_per_click_avg) or (self.delta_time > self.min_click):
            self.distance = 1
            self.count = 1
            self.first_time = current_time
            # self.delta_time = 0
        else:
            if self.count < len(self.volume_increases_list):
                self.distance = self.volume_increases_list[self.count]
            else:
                self.distance = self.volume_increases_list[-1]
            self.count += 1
        self.last_button = button
        self.last_time = current_time
        return int(self.distance)

class HIDToMIDIDaemon:
    def __init__(self, min_click_time, max_avg_click_time, volume_increases_list,
                 VID, PID, midi_in_channel, midi_out_channel):
        self.queue = queue.Queue()
        self.running = True
        self.hid_reader_thread = threading.Thread(target=self.hid_reader, daemon=True, name="HIDReaderThread")
        self.midi_reader_thread = threading.Thread(target=self.midi_reader, daemon=True, name="MIDIReaderThread")
        self.consumer_thread = threading.Thread(target=self.consumer, daemon=True, name="ConsumerThread")
        self.volume_knob = AccelerationHandler(min_click_time, max_avg_click_time, volume_increases_list)
        self.midi_in_channel = midi_in_channel
        self.midi_out_channel = midi_out_channel
        self.vid = VID
        self.pid = PID
        self.midi_output = None  # Shared MIDI output for sending to GLM
        self.midi_input = None   # MIDI input for reading GLM state

    def hid_reader(self):
        """Reads events from the HID device and puts them in the queue."""
        set_current_thread_priority(win32process.THREAD_PRIORITY_HIGHEST)
        device = None
        while self.running:
            if device is None:
                try:
                    device = hid.device()
                    device.open(self.vid, self.pid)
                    logger.info(f"Connected to HID device VID: {hex(self.vid)} PID: {hex(self.pid)}.")
                except Exception as e:
                    logger.warning(f"Failed to open HID device: {e}. Retrying...")
                    time.sleep(RETRY_DELAY)
                    continue

            try:
                report = device.read(3, timeout_ms=1000)
                if report:
                    keyreported = report[0]
                    if keyreported == 0:
                        continue
                    now = time.time()
                    distance = self.volume_knob.calculate_speed(now, keyreported)
                    self.queue.put({'timestamp': now, 'key': keyreported, 'distance': distance})
                    logger.debug(f"HID: delta={self.volume_knob.delta_time*1000:.0f}ms, dist={distance}, key={KEY_NAMES.get(keyreported, keyreported)} {'(*)' if self.volume_knob.count == 1 else ''}")
            except Exception as e:
                logger.warning(f"HID device error: {e}. Reconnecting...")
                if device:
                    device.close()
                device = None
                time.sleep(RETRY_DELAY)

    def midi_reader(self):
        """Reads MIDI messages from GLMOUT and updates GLM state."""
        set_current_thread_priority(win32process.THREAD_PRIORITY_BELOW_NORMAL)

        while self.running:
            try:
                self.midi_input = open_input(self.midi_out_channel)
                logger.info(f"Connected to MIDI output channel '{self.midi_out_channel}' for state reading.")

                # Blocking iteration - waits for messages, no polling
                for msg in self.midi_input:
                    if not self.running:
                        break
                    if msg.type == 'control_change':
                        changed = glm_controller.update_from_midi(msg.control, msg.value)
                        if changed:
                            state = glm_controller.get_state()
                            logger.debug(f"GLM state: vol={state['volume']}, mute={state['mute']}, dim={state['dim']}, pwr={state['power']}")

            except Exception as e:
                if self.running:  # Only log if not shutting down
                    logger.warning(f"MIDI reader error: {e}. Reconnecting...")
                    time.sleep(RETRY_DELAY)
            finally:
                if self.midi_input:
                    try:
                        self.midi_input.close()
                    except Exception:
                        pass
                    self.midi_input = None

    def consumer(self):
        """Processes events from the queue and sends MIDI messages."""
        set_current_thread_priority(win32process.THREAD_PRIORITY_ABOVE_NORMAL)

        # Connect to MIDI output
        while self.midi_output is None and self.running:
            try:
                self.midi_output = open_output(self.midi_in_channel)
                logger.info(f"Connected to MIDI input channel '{self.midi_in_channel}' for sending commands.")
            except Exception as e:
                logger.warning(f"Failed to open MIDI input channel '{self.midi_in_channel}': {e}. Retrying...")
                time.sleep(RETRY_DELAY)

        while True:
            event = self.queue.get()
            if event is None:  # Sentinel for consumer shutdown
                logger.info("Consumer thread exiting...")
                break

            now = time.time()
            time_then = event['timestamp']
            event_age = now - time_then
            if event_age > MAX_EVENT_AGE:
                logger.warning(f"Discarded stale event: {event}")
                continue

            button = event['key']
            distance = event['distance']

            # Get the action for this key
            action = BINDINGS.get(button)
            if not action:
                logger.debug(f"No binding for key {button}")
                continue

            # Get GLM control info for logging
            glm_ctrl = ACTION_TO_GLM.get(action)
            if glm_ctrl:
                logger.debug(f"Sending {action.value} (CC {glm_ctrl.cc}) x{distance}")

                for _ in range(distance):
                    # For volume commands, wait for GLM confirmation before sending next
                    if action in (Action.VOL_UP, Action.VOL_DOWN):
                        glm_controller.clear_volume_change_event()  # Clear BEFORE sending
                        self._send_action(action)
                        if not glm_controller.wait_for_volume_change(timeout=0.20):
                            logger.debug("Volume change not confirmed by GLM (timeout)")
                    else:
                        self._send_action(action)
                        time.sleep(SEND_DELAY)
            else:
                # Non-GLM action (future: route to other apps)
                logger.debug(f"Action {action.value} has no GLM mapping (yet)")

    def _send_action(self, action: Action):
        """Send an action to GLM using the controller."""
        try:
            if self.midi_output is None:
                logger.warning("MIDI output not connected. Reconnecting...")
                self.midi_output = open_output(self.midi_in_channel)

            glm_controller.send_action(action, self.midi_output)
        except Exception as e:
            logger.error(f"Error sending MIDI message: {e}")
            self.midi_output = None  # Force reconnect

    def start(self):
        """Starts all threads."""
        self.hid_reader_thread.start()
        self.midi_reader_thread.start()
        self.consumer_thread.start()

    def stop(self):
        """Stops the daemon gracefully."""
        logger.info("Stopping daemon...")
        self.running = False
        self.queue.put(None)  # Sentinel to unblock the consumer
        # Close MIDI input to unblock the blocking read
        if self.midi_input:
            try:
                self.midi_input.close()
            except Exception:
                pass
        self.hid_reader_thread.join(timeout=2.0)
        self.midi_reader_thread.join(timeout=2.0)
        self.consumer_thread.join(timeout=2.0)
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
    logger.info(f"<--- End configuration")

    set_higher_priority()
    time.sleep(2.0)

    daemon = HIDToMIDIDaemon(
        args.min_click_time,
        args.max_avg_click_time,
        args.volume_increases_list,
        args.vid,
        args.pid,
        args.midi_in_channel,
        args.midi_out_channel
    )
    signal.signal(signal.SIGINT, lambda sig, frame: signal_handler(sig, frame, daemon))
    daemon.start()

    try:
        while True:
            time.sleep(3)  # Keep the main thread alive
    except KeyboardInterrupt:
        signal_handler(None, None, daemon)
    finally:
        stop_logging()
        
        
