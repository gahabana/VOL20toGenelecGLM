import time
import signal
import sys
import os
import threading
import queue
from queue import Queue
import hid
from mido import Message, open_output
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

KEYCODE_2_DESCRIPTION = {
     2: "VolUp",  # Volume Up to GLM Volume Down
     1: "VolDown",  # Volume Down to GLM Volume Down
    32: "Play/Pause",  # Play/Pause keys to GLM Mute (click on VOL20)
    16: "NextTrack",  # Next Track key to GLM Dim (double click on VOL20)
     8: "PrevTrack",  # Previous Track to GLM Power-On/Off (tripple click on VOL20)
     4: "MuteOnOff",  # Mute On / Mute Off (2 second press on a button on VOL20) to Power On/Off
}

MIDI_CC_MAPPING = {
     2: 21,  # Volume Up to GLM Volume Down
     1: 22,  # Volume Down to GLM Volume Down
    32: 23,  # Play/Pause keys to GLM Mute (click on VOL20)
    16: 24,  # Next Track key to GLM Dim (double click on VOL20)
     8: 28,  # Previous Track to GLM Power-On/Off (tripple click on VOL20)
     4: 28,  # Mute On / Mute Off (2 second press on a button on VOL20) to Power On/Off
}

MIDI_2_GLM_MAPPING = {
    21: "VolUp",      # GLM Volume Up
    22: "VolDown",    # GLM Volume Down
    23: "Mute",      # GLM Mute
    24: "Dim",       # GLM Dim
    28: "PwrOnOff",  # GLM Power-On/Off
}

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
    parser.add_argument("--click_times", type=validate_click_times, default=(0.26, 0.19),
                        help="Comma-separated values for MIN_CLICK_TIME and MAX_AVG_CLICK_TIME. "
                             "MIN_CLICK_TIME must be > 0.01 and < 1, and MAX_AVG_CLICK_TIME must be <= MIN_CLICK_TIME. "
                             "Default is '0.2,0.18'.")
    
    parser.add_argument("--volume_increases_list", type=validate_volume_increases, default=[1, 2, 2, 2, 2, 3, 3, 4],
                        help="List of volume increases. Must be between 2 and 15 integers, each >=1 and <=10. Default is [1, 2, 2, 2, 3, 3, 3, 4].")
    
    
    # VID/PID combination
    parser.add_argument("--device", type=validate_device, default=(0x07d7, 0x0000),
                        help="VID and PID of the device to be listened to, in the format 'VID,PID'. Default is '0x07d7,0x0000'.")

    # MIDI channel name
    parser.add_argument("--midi_channel_name", type=str, default="GLMMIDI 1",
                        help="Name of the MIDI channel. If the name contains spaces, enclose it in quotes (e.g., 'MIDI Channel 1' or \"MIDI Channel 1\"). Default is 'GLMMIDI 1'.")

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
    def __init__(self, min_click_time, max_avg_click_time, volume_increases_list, VID, PID, midi_channel_name):
        self.queue = queue.Queue()
        self.running = True
        self.producer_thread = threading.Thread(target=self.producer, daemon=True, name="ProducerThread")
        self.consumer_thread = threading.Thread(target=self.consumer, daemon=True, name="ConsumerThread")
        self.volume_knob = AccelerationHandler(min_click_time, max_avg_click_time, volume_increases_list)
        self.midi_channel_name = midi_channel_name
        self.vid = VID
        self.pid = PID
        
    def producer(self):
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
                    # logger.debug(f"Received report: time={now} key={KEYCODE_2_DESCRIPTION[keyreported]}, distance={distance}")
                    logger.debug(f"Received report: delta={self.volume_knob.delta_time*1000:.0f}ms, distance={distance if distance != 1 else '--1--'}, key={KEYCODE_2_DESCRIPTION[keyreported]}")
            except Exception as e:
                logger.warning(f"HID device error: {e}. Reconnecting...")
                if device:
                    device.close()
                device = None
                time.sleep(RETRY_DELAY)

    def consumer(self):
        """Processes events from the queue and sends MIDI messages."""
        set_current_thread_priority(win32process.THREAD_PRIORITY_ABOVE_NORMAL)

        midi_handler = self.MIDIHandler(self.midi_channel_name)
        midi_handler.connect()

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
            # logger.debug(f"Sending to midi handler: delay {event_age}, button {button} for {distance} times")
            # logger.debug(f"Sending to midi handler: delay {event_age}, button {button} for {distance} times")
            logger.debug(f"Sending MIDI msg to channel {MIDI_CC_MAPPING[button]} {MIDI_2_GLM_MAPPING[MIDI_CC_MAPPING[button]]} x{distance:>1}")
            for _ in range(distance):
                midi_handler.send(button, 127)
                time.sleep(SEND_DELAY)
                
    class MIDIHandler:
        def __init__(self, midi_channel_name):
            self.output = None
            self.midi_channel_name = midi_channel_name

        def connect(self):
            while True:
                try:
                    self.output = open_output(self.midi_channel_name)
                    logger.info(f"Connected to MIDI channel '{self.midi_channel_name}'.")
                    return
                except Exception as e:
                    logger.warning(f"Failed to open MIDI channel '{self.midi_channel_name}': {e}. Retrying...")
                    time.sleep(RETRY_DELAY)

        def send(self, button, value=127):
            try:
                if self.output is None:
                    logger.warning("MIDI port not connected. Reconnecting...")
                    self.connect()
                if button in MIDI_CC_MAPPING:
                    cc_number = MIDI_CC_MAPPING[button]
                    self.output.send(Message('control_change', control=cc_number, value=value))
                    # logger.debug(f"Sent MIDI msg to channel {cc_number}  with value = {value}")
                    # logger.debug(f"Sent MIDI msg to channel {cc_number} -> {MIDI_2_GLM_MAPPING[cc_number]}")
                else:
                    logger.debug(f"Unknown key {button} received !!!")
            except Exception as e:
                logger.error(f"Error sending MIDI message: {e}")
                self.output = None  # Force reconnect

    def start(self):
        """Starts the producer and consumer threads."""
        self.producer_thread.start()
        self.consumer_thread.start()

    def stop(self):
        """Stops the daemon gracefully."""
        logger.info("Stopping daemon...")
        self.running = False
        self.queue.put(None)  # Sentinel to unblock the consumer
        self.producer_thread.join()
        self.consumer_thread.join()
        logger.info("Daemon stopped.")

if __name__ == "__main__":
    args = parse_arguments()
    stop_logging = setup_logging(args.log_level, args.log_file_name)
    
    # logger.info(f">--- Starting {os.path.basename(__file__)} agent. Logging setup complete. Priority to be now set to high and will wait 2.0 seconds for Bluetooth to be connected")
    
    # Log the configurations for confirmation
    logger.info(f"---> Here are the input values (either default input or set in command line)")
    logger.info(f"---> Minimum click time: {args.min_click_time}")
    logger.info(f"---> Maximum aveg click time: {args.max_avg_click_time}")
    logger.info(f"---> Volume increases list: {args.volume_increases_list}")
    logger.info(f"---> Debug level: {args.log_level}")
    logger.info(f"---> Debug file: {args.log_file_name}")
    logger.info(f"---> Midi channel name: {args.midi_channel_name}")
    logger.info(f"---> VID/PID : VID: {hex(args.vid)} PID: {hex(args.pid)}")
    logger.info(f"<--- End of  the values (either input or set in command line)")

    set_higher_priority()
    time.sleep(2.0)
    
    # logger.info("....About to start the deamon.")
    daemon = HIDToMIDIDaemon(args.min_click_time, args.max_avg_click_time, args.volume_increases_list, args.vid, args.pid, args.midi_channel_name)
    signal.signal(signal.SIGINT, lambda sig, frame: signal_handler(sig, frame, daemon))
    daemon.start()
    
    try:
        while True:
            time.sleep(3)  # Keep the main thread alive
    except KeyboardInterrupt:
        signal_handler(None, None, daemon)
    finally:
        stop_logging()
        
        