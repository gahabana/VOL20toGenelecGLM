import threading
import queue
from queue import Queue
import time
import signal
import sys
import hid
from mido import Message, open_output
import psutil

import logging
from logging.handlers import RotatingFileHandler, QueueHandler, QueueListener
import os

import ctypes
import win32api
import win32process
import win32con

# Parameters
VID = 0x07d7
PID = 0x0000
MIDI_CHANNEL_NAME = "GLMMIDI 1"
MAX_EVENT_AGE = 2.0  # seconds
SEND_DELAY = 0  # seconds (0 seconds is just OS yield to other threads if needed ... 
#               # i.e. MIDI receiving app .. it also work with 0.0005 sec (0.5ms) but i found it not to be needed)
RETRY_DELAY = 2.0  # seconds

MIDI_CC_MAPPING = {
     2: 21,  # Volume Up to GLM Volume Down
     1: 22,  # Volume Down to GLM Volume Down
    32: 23,  # Play/Pause keys to GLM Mute (click on VOL20)
    16: 24,  # Next Track key to GLM Dim (double click on VOL20)
     8: 28,  # Previous Track to GLM Power-On/Off (tripple click on VOL20)
     4: 28,  # Mute On / Mute Off (2 second press on a button on VOL20) to Power On/Off
}

volume_increases_list = [1, 2, 2, 2, 3, 3, 3, 4]
volume_steps = len(volume_increases_list)
MIN_CLICK_TIME = 0.2 #minimum time between keyboard events to be considered as 'accelerated'
MAX_AVG_CLICK_TIME = 0.18 # maximum average time between the clicks 

def set_high_priority():
    try:
        p = psutil.Process(os.getpid())
        p.nice(psutil.HIGH_PRIORITY_CLASS)  # Set to high priority
        logger.debug("Main Process priority set to high.")
    except Exception as e:
        print(f"Failed to set high priority: {e}")

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

def setup_logging(log_file_name="hid_to_midi.log", max_bytes=4*1024*1024, backup_count=5):
    script_directory = os.path.dirname(os.path.abspath(__file__))
    log_file_path = os.path.join(script_directory, log_file_name)

    log_queue = Queue()

    # File Handler
    file_handler = RotatingFileHandler(log_file_path, maxBytes=max_bytes, backupCount=backup_count)
    file_handler.setLevel(logging.DEBUG)
    file_handler.setFormatter(logging.Formatter('%(asctime)s [%(levelname)s] %(message)s'))

    # Console Handler
    console_handler = logging.StreamHandler()
    console_handler.setLevel(logging.INFO)
    console_handler.setFormatter(logging.Formatter('%(asctime)s [%(levelname)s] %(message)s'))

    # QueueHandler
    queue_handler = QueueHandler(log_queue)

    # Root Logger
    root_logger = logging.getLogger()
    root_logger.handlers = []  # Clear all handlers
    root_logger.setLevel(logging.DEBUG)
    root_logger.addHandler(queue_handler)

    # Custom Module Logger
    global logger
    logger = logging.getLogger(__name__)
    logger.setLevel(logging.DEBUG)
    logger.addHandler(console_handler)  # Optional: Direct console output
    logger.addHandler(file_handler)     # Optional: Direct file output
    logger.propagate = False  # Avoid double logging

    # Listener Thread
    stop_event = threading.Event()

    def log_listener_thread():
        listener = QueueListener(log_queue, file_handler, console_handler)
        listener.start()
        
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
    def __init__(self, min_click=MIN_CLICK_TIME, max_per_click_avg=MAX_AVG_CLICK_TIME):
        self.last_button = 0
        self.last_time = 0
        self.first_time = 0
        self.distance = 0
        self.count = 1
        self.max_per_click_avg = max_per_click_avg
        self.min_click = min_click

    def calculate_speed(self, current_time, button):
        delta_time = current_time - self.last_time
        avg_step_time = (current_time - self.first_time) / self.count
        if (self.last_button != button) or (avg_step_time > self.max_per_click_avg) or (delta_time > self.min_click):
            self.distance = 1
            self.count = 1
            self.first_time = current_time
        else:
            if self.count < volume_steps:
                self.distance = volume_increases_list[self.count]
            else:
                self.distance = volume_increases_list[-1]
            self.count += 1
        self.last_button = button
        self.last_time = current_time
        return int(self.distance)

class HIDToMIDIDaemon:
    def __init__(self):
        self.queue = queue.Queue()
        self.running = True
        self.producer_thread = threading.Thread(target=self.producer, daemon=True, name="ProducerThread")
        self.consumer_thread = threading.Thread(target=self.consumer, daemon=True, name="ConsumerThread")
        self.volume_knob = AccelerationHandler()

    def producer(self):
        """Reads events from the HID device and puts them in the queue."""
        set_current_thread_priority(win32process.THREAD_PRIORITY_TIME_CRITICAL)
        device = None
        while self.running:
            if device is None:
                try:
                    device = hid.device()
                    device.open(VID, PID)
                    logger.info(f"Connected to HID device VID: {hex(VID)} PID: {hex(PID)}.")
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
                    logger.debug(f"Received report: time={now} key={keyreported}, distance={distance}")
            except Exception as e:
                logger.warning(f"HID device error: {e}. Reconnecting...")
                if device:
                    device.close()
                device = None
                time.sleep(RETRY_DELAY)

    def consumer(self):
        """Processes events from the queue and sends MIDI messages."""
        set_current_thread_priority(win32process.THREAD_PRIORITY_HIGHEST)

        midi_handler = self.MIDIHandler()
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
            logger.debug(f"Sending to midi handler: delay{event_age}, button {button} for {distance} times")
            for _ in range(distance):
                midi_handler.send(button, 127)
                time.sleep(SEND_DELAY)
                
    class MIDIHandler:
        def __init__(self):
            self.output = None

        def connect(self):
            while True:
                try:
                    self.output = open_output(MIDI_CHANNEL_NAME)
                    logger.info(f"Connected to MIDI channel '{MIDI_CHANNEL_NAME}'.")
                    return
                except Exception as e:
                    logger.warning(f"Failed to open MIDI channel '{MIDI_CHANNEL_NAME}': {e}. Retrying...")
                    time.sleep(RETRY_DELAY)

        def send(self, button, value=127):
            try:
                if self.output is None:
                    logger.warning("MIDI port not connected. Reconnecting...")
                    self.connect()
                if button in MIDI_CC_MAPPING:
                    cc_number = MIDI_CC_MAPPING[button]
                    self.output.send(Message('control_change', control=cc_number, value=value))
                    logger.debug(f"Sent MIDI msg to channel {cc_number} with value = {value}")
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
    stop_logging = setup_logging()
    logger.info(">--- Starting HID2MIDI-V3 agent. Logging setup complete. Priority to be now set to high and will wait 2.0 seconds for Bluetooth to be connected")
    set_high_priority()
    time.sleep(2.0)
    
    # logger.info("....About to start the deamon.")
    daemon = HIDToMIDIDaemon()
    signal.signal(signal.SIGINT, lambda sig, frame: signal_handler(sig, frame, daemon))
    daemon.start()
    
    try:
        while True:
            time.sleep(3)  # Keep the main thread alive
    except KeyboardInterrupt:
        signal_handler(None, None, daemon)
    finally:
        stop_logging()
        
        