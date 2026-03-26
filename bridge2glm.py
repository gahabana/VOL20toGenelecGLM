"""
GLM Manager - VOL20 to Genelec GLM MIDI Bridge

Bridges a Fosi Audio VOL20 USB volume knob to Genelec GLM software via MIDI.
Supports volume control, mute, dim, and power management with UI automation.
"""

__version__ = "3.2.30"

import time
import signal
import sys
import os
import threading
import queue
from queue import Queue
from typing import Dict, Optional, List, Callable
import hid

from glm_core import SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction, trace_ids
from mido import Message, open_output, open_input

# Import from extracted modules
from config import parse_arguments
from retry_logger import retry_logger
from midi_constants import (
    Action, ControlMode, GlmControl,
    GLM_VOLUME_ABS, GLM_VOL_UP_CC, GLM_VOL_DOWN_CC, GLM_MUTE_CC, GLM_DIM_CC, GLM_POWER_CC,
    POWER_PATTERN, POWER_PATTERN_WINDOW, POWER_PATTERN_MIN_SPAN, POWER_STARTUP_WINDOW,
    POWER_PATTERN_MAX_GAP, POWER_PATTERN_MAX_TOTAL, POWER_PATTERN_PRE_GAP,
    CC_NAMES, ACTION_TO_GLM, CC_TO_ACTION,
    KEY_VOL_UP, KEY_VOL_DOWN, KEY_CLICK, KEY_DOUBLE_CLICK, KEY_TRIPLE_CLICK, KEY_LONG_PRESS,
    KEY_NAMES, DEFAULT_BINDINGS, log_midi as _log_midi
)
from acceleration import AccelerationHandler
from logging_setup import LOG_FORMAT

# Power control via UI automation (Windows only)
try:
    from PowerOnOff import GlmPowerController, POWER_CONTROL_AVAILABLE, get_display_diagnostics, ensure_session_connected
except ImportError:
    POWER_CONTROL_AVAILABLE = False
    get_display_diagnostics = None
    ensure_session_connected = None
    GlmPowerController = None

# GLM process manager (Windows only) - replaces PowerShell script
try:
    from PowerOnOff import GlmManager, GlmManagerConfig, GLM_MANAGER_AVAILABLE
except ImportError:
    GLM_MANAGER_AVAILABLE = False
    GlmManager = None
    GlmManagerConfig = None

import psutil
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
HID_READ_TIMEOUT_MS = 1000  # milliseconds - balance between CPU usage and shutdown responsiveness
QUEUE_MAX_SIZE = 100  # Maximum queued events before backpressure

# Power control timing (UI automation based)
POWER_SETTLING_TIME = 2.0   # Block ALL commands during power settling
POWER_COOLDOWN_TIME = 1.5   # Block power commands after settling ends
POWER_TOTAL_LOCKOUT = POWER_SETTLING_TIME + POWER_COOLDOWN_TIME  # 3.5s total

# GLM volume initialization timing
GLM_INIT_WAIT = 0.5  # seconds - wait for MIDI reader to connect
GLM_VOL_QUERY_DELAY = 0.1  # seconds - delay between vol+1 and vol-1
GLM_VOL_RESPONSE_WAIT = 0.3  # seconds - wait for GLM to report volume

# Module-level logger (set by setup_logging)
logger = logging.getLogger(__name__)


def log_midi(direction: str, msg_type: str, cc: int = None, value: int = None, channel: int = None, raw: str = None, trace_id: str = ""):
    """Wrapper for log_midi that uses the module logger."""
    _log_midi(logger, direction, msg_type, cc, value, channel, raw, trace_id=trace_id)


# ==============================================================================
# GLM STATE CONTROLLER - Tracks and controls GLM state
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
        self._power_trace_id: str = ""           # Trace ID for current power transition

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

    def start_power_transition(self, target_state: bool, trace_id: str = ""):
        """
        Mark the start of a power transition.

        Called when power command is initiated. Blocks all commands during settling.
        """
        with self._lock:
            self._power_transition_start = time.time()
            self._power_settling = True
            self._power_target = target_state
            self._power_trace_id = trace_id
        self._notify_state_change(force=True)  # Notify UI of transitioning state
        prefix = f"[{trace_id}] " if trace_id else ""
        logger.info(f"{prefix}power.begin: target={'ON' if target_state else 'OFF'}")

    def end_power_transition(self, success: bool, actual_state: Optional[bool] = None):
        """
        Mark the end of a power transition.

        Called when UI automation confirms state change (or fails).
        """
        with self._lock:
            self._power_settling = False
            duration = time.time() - self._power_transition_start if self._power_transition_start else 0
            trace_id = getattr(self, '_power_trace_id', '')
            if success and actual_state is not None:
                self.power = actual_state
            elif success and self._power_target is not None:
                self.power = self._power_target
            self._power_target = None
        self._notify_state_change(force=True)
        prefix = f"[{trace_id}] " if trace_id else ""
        result = "OK" if success else "FAILED"
        logger.info(f"{prefix}power.end: {result}, power={'ON' if self.power else 'OFF'} (took {duration:.1f}s)")

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

    def toggle_power_from_midi_pattern(self) -> bool:
        """Toggle power state when RF remote MIDI pattern is detected.

        Acquires the lock to prevent race conditions with other threads
        modifying power state (e.g., consumer thread via UI automation).

        Returns:
            The new power state (True=ON, False=OFF).
        """
        with self._lock:
            self.power = not self.power
            new_power = self.power
        self._notify_state_change()
        return new_power

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
                    logger.debug(f"volume: GLM clipped: sent {self._pending_volume}, got {value}")
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
            cooldown_remaining = 0
            in_cooldown = False

            if self._power_transition_start > 0:
                elapsed = time.time() - self._power_transition_start
                if elapsed < POWER_SETTLING_TIME:
                    settling_remaining = POWER_SETTLING_TIME - elapsed
                elif elapsed < POWER_TOTAL_LOCKOUT:
                    in_cooldown = True
                    cooldown_remaining = POWER_TOTAL_LOCKOUT - elapsed

            return {
                "volume": self.volume,
                "volume_db": self.volume - 127,  # 0-127 → -127 to 0 dB
                "mute": self.mute,
                "dim": self.dim,
                "power": self.power,
                "power_transitioning": self._power_settling,
                "power_settling_remaining": round(settling_remaining, 1),
                "power_cooldown": in_cooldown,
                "power_cooldown_remaining": round(cooldown_remaining, 1),
            }

    def send_volume_absolute(self, target: int, midi_output, trace_id: str = "") -> bool:
        """
        Send absolute volume command to GLM via CC 20.
        Target is clamped to 0-127 range.
        Returns True if message was sent.
        """
        target = max(0, min(127, target))
        try:
            midi_output.send(Message('control_change', control=GLM_VOLUME_ABS, value=target))
            log_midi("TX", "control_change", cc=GLM_VOLUME_ABS, value=target, trace_id=trace_id)
            return True
        except (OSError, IOError) as e:
            prefix = f"[{trace_id}] " if trace_id else ""
            logger.debug(f"{prefix}midi.error: Failed to send volume command: {e}")
            return False

    def send_action(self, action: Action, midi_output, explicit_state: Optional[bool] = None, trace_id: str = "") -> bool:
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
            log_midi("TX", "control_change", cc=glm_ctrl.cc, value=value, trace_id=trace_id)
            return True
        except (OSError, IOError) as e:
            prefix = f"[{trace_id}] " if trace_id else ""
            logger.debug(f"{prefix}midi.error: Failed to send action {action.value}: {e}")
            return False


# Global GLM controller instance
glm_controller = GlmController()


def set_higher_priority():
    try:
        p = psutil.Process(os.getpid())
        p.nice(psutil.ABOVE_NORMAL_PRIORITY_CLASS)  # Set to Above Normal
        logger.debug("Main Process priority set to AboveNormal.")
    except Exception as e:
        logger.warning(f"Failed to set higher priority: {e}")


def minimize_console_window():
    """Minimize the script's console window (Windows only)."""
    if not IS_WINDOWS:
        return

    try:
        # Get console window handle
        hwnd = ctypes.windll.kernel32.GetConsoleWindow()
        if hwnd:
            # SW_MINIMIZE = 6
            ctypes.windll.user32.ShowWindow(hwnd, 6)
            logger.debug("Console window minimized")
    except Exception as e:
        logger.debug(f"Failed to minimize console window: {e}")


# ==============================================================================
# RDP SESSION PRIMING - Prevents high CPU after RDP disconnect
# ==============================================================================

def get_boot_time() -> int:
    """Get system boot time as Unix timestamp (Windows only)."""
    if not IS_WINDOWS:
        return 0
    try:
        kernel32 = ctypes.windll.kernel32
        tick_count = kernel32.GetTickCount64()
        boot_time = time.time() - (tick_count / 1000)
        return int(boot_time)
    except Exception:
        return 0


def needs_rdp_priming() -> bool:
    """
    Check if RDP priming is needed (only once per boot).

    RDP priming prevents high CPU in GLM after RDP disconnect by doing
    an RDP connect/disconnect cycle before GLM starts. This only needs
    to happen once per boot.

    Returns True if priming is needed, False if already primed this boot.
    """
    if not IS_WINDOWS:
        return False

    flag_file = os.path.join(os.environ.get('TEMP', r'C:\temp'), 'rdp_primed.flag')
    current_boot = get_boot_time()

    if os.path.exists(flag_file):
        try:
            with open(flag_file, 'r') as f:
                stored_boot = int(f.read().strip())
            # Same boot session (within 60 second tolerance for clock drift)
            if abs(stored_boot - current_boot) < 60:
                return False  # Already primed this boot
        except Exception:
            pass  # Flag file corrupted, re-prime

    # Write current boot time as flag
    try:
        with open(flag_file, 'w') as f:
            f.write(str(current_boot))
    except Exception as e:
        logger.warning(f"Failed to write RDP priming flag: {e}")

    return True  # Need to prime


def get_credential_from_manager(target: str) -> tuple[str, str] | None:
    """
    Read credentials from Windows Credential Manager using keyring library.

    Args:
        target: The credential target name (e.g., "localhost")

    Returns:
        Tuple of (username, password) if found, None otherwise.
    """
    try:
        import keyring
    except ImportError:
        logger.warning("keyring module not installed, cannot read credentials")
        return None

    cred = keyring.get_credential(target, None)
    if cred and cred.password:
        logger.debug(f"Credential found for target: {target}, user: {cred.username}")
        return (cred.username, cred.password)

    return None


def prime_rdp_session() -> bool:
    """
    Prime the RDP session to prevent high CPU after disconnect.

    This does an RDP connect/disconnect cycle using FreeRDP, which initializes
    the Windows display driver properly. Without this, GLM (OpenGL app) may
    consume high CPU after the first RDP disconnect on a headless VM.

    Credentials are read from Windows Credential Manager for security.
    To set up: cmdkey /add:localhost /user:.\\USERNAME /pass:PASSWORD

    Returns True if priming succeeded, False otherwise.
    """
    if not IS_WINDOWS:
        return True  # Not needed on non-Windows

    import subprocess
    import shutil

    # Find wfreerdp.exe
    wfreerdp = shutil.which("wfreerdp") or shutil.which("wfreerdp.exe")
    if not wfreerdp:
        logger.warning("RDP priming skipped: wfreerdp not found in PATH")
        return False

    # Try to get credentials from Windows Credential Manager
    # Try multiple target names that might have been created
    credential = None
    for target in ["localhost", "TERMSRV/localhost"]:
        credential = get_credential_from_manager(target)
        if credential:
            logger.debug(f"Found credential for target: {target}")
            break

    if not credential:
        logger.warning(
            "RDP priming skipped: No credentials found in Windows Credential Manager. "
            "Run: cmdkey /generic:localhost /user:USERNAME /pass:PASSWORD"
        )
        return False

    username, password = credential
    # Ensure username has local domain prefix for NLA
    if not username.startswith(".\\") and "\\" not in username:
        username = ".\\" + username

    logger.info("Priming RDP session to prevent high CPU after disconnect...")

    try:
        # Start FreeRDP connection to localhost
        # Note: Don't use stdout/stderr=DEVNULL - causes 12s blocking delay on Windows
        proc = subprocess.Popen(
            [wfreerdp, "/v:localhost", "/u:" + username, "/p:" + password, "/cert:ignore", "/sec:nla"],
        )

        # Poll for RDP session to establish (max 10s)
        logger.debug("Waiting for RDP session...")
        rdp_connected = False
        for i in range(20):
            time.sleep(0.5)
            result = subprocess.run(["query", "session"], capture_output=True, timeout=5)
            output = result.stdout.decode('utf-8', errors='replace')
            if "rdp-tcp#" in output:
                rdp_connected = True
                logger.debug(f"RDP session detected after {(i+1)*0.5:.1f}s")
                time.sleep(1.0)  # Allow Windows to fully register session before tscon
                break

        if not rdp_connected:
            logger.warning("RDP session not detected within 10s, continuing anyway...")

        # Kill FreeRDP to disconnect
        proc.terminate()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()

        logger.debug("FreeRDP disconnected")

        # Reconnect session to console
        result = subprocess.run(
            ["tscon", "1", "/dest:console"],
            capture_output=True,
            timeout=10,
        )

        if result.returncode == 0:
            logger.debug("tscon reconnected session to console")
            logger.info("RDP session primed successfully")
            time.sleep(1)
            return True
        else:
            stderr = result.stderr.decode('utf-8', errors='ignore').strip()
            logger.warning(f"tscon failed during priming: {stderr}")
            return False

    except Exception as e:
        logger.error(f"RDP priming failed: {e}")
        return False

def restart_midi_service():
    """Restart Windows MIDI Service so LoopMIDI virtual ports are visible.

    Windows MIDI Services (introduced in Windows 11 24H2) doesn't detect
    virtual MIDI ports created by LoopMIDI before the service starts.
    Restarting midisrv forces re-enumeration of all MIDI ports.
    """
    if not HAS_WIN32:
        return

    import subprocess

    logger.info("Restarting Windows MIDI Service (midisrv) for virtual port detection...")
    try:
        result = subprocess.run(
            ["net", "stop", "midisrv"],
            capture_output=True, timeout=10,
        )
        if result.returncode != 0:
            stderr = result.stderr.decode('utf-8', errors='ignore').strip()
            # "not started" is fine — service may already be stopped
            if "not started" not in stderr.lower() and "not running" not in stderr.lower():
                logger.warning(f"net stop midisrv: {stderr}")

        result = subprocess.run(
            ["net", "start", "midisrv"],
            capture_output=True, timeout=10,
        )
        if result.returncode == 0:
            logger.info("Windows MIDI Service restarted successfully")
        else:
            stderr = result.stderr.decode('utf-8', errors='ignore').strip()
            # "already been started" is fine
            if "already" in stderr.lower():
                logger.info("Windows MIDI Service was already running")
            else:
                logger.warning(f"net start midisrv: {stderr}")
    except Exception as e:
        logger.warning(f"Failed to restart MIDI service: {e}")

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

    # Import WebSocket error filter to suppress disconnect errors in logs
    from api.rest import WebSocketErrorFilter
    ws_filter = WebSocketErrorFilter()

    # File Handler
    file_handler = RotatingFileHandler(log_file_path, maxBytes=max_bytes, backupCount=backup_count)
    file_handler.setLevel(logging.DEBUG if log_level != "NONE" else logging.CRITICAL)
    file_handler.setFormatter(logging.Formatter(LOG_FORMAT))
    file_handler.addFilter(ws_filter)  # Filter WebSocket disconnect errors

    # Console Handler
    console_handler = logging.StreamHandler()
    console_handler.setLevel(logging.INFO if log_level in ["INFO", "DEBUG"] else logging.CRITICAL)
    console_handler.setFormatter(logging.Formatter(LOG_FORMAT))
    console_handler.addFilter(ws_filter)  # Filter WebSocket disconnect errors

    # QueueHandler
    queue_handler = QueueHandler(log_queue)

    # Root Logger
    root_logger = logging.getLogger()
    root_logger.handlers = []  # Clear all handlers
    root_logger.setLevel(logging.DEBUG if log_level == "DEBUG" else logging.INFO)
    root_logger.addHandler(queue_handler)

    # Suppress verbose debug logging from third-party libraries
    logging.getLogger("keyring").setLevel(logging.WARNING)
    logging.getLogger("jaraco").setLevel(logging.WARNING)

    # Custom Module Logger
    global logger
    logger = logging.getLogger(__name__)
    logger.setLevel(logging.DEBUG if log_level == "DEBUG" else logging.INFO)
    logger.addHandler(console_handler)  # Optional: Direct console output
    logger.addHandler(file_handler)     # Optional: Direct file output
    logger.propagate = False  # Avoid double logging

    # Listener Thread
    stop_event = threading.Event()
    logger.info(f"sys.init: >----- Starting {os.path.basename(__file__)} v{__version__}. Initializing...")

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
    logger.info("sys.shutdown: SIGINT received, shutting down...")
    daemon.stop()
    stop_logging_func()
    sys.exit(0)


class HIDToMIDIDaemon:
    def __init__(self, min_click_time, max_avg_click_time, volume_increases_list,
                 VID, PID, midi_in_channel, midi_out_channel, startup_volume=None, api_port=8080,
                 mqtt_broker=None, mqtt_port=1883, mqtt_user=None, mqtt_pass=None,
                 mqtt_topic="glm", mqtt_ha_discovery=True,
                 glm_manager_enabled=False, glm_path=None, glm_cpu_gating=True):
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
        self._suppress_power_pattern = False  # Temporarily suppress pattern detection

        # GLM Manager (process lifecycle and watchdog)
        # Initialize this BEFORE power controller, since it may need to start GLM first
        self._glm_manager = None
        if glm_manager_enabled and GLM_MANAGER_AVAILABLE:
            try:
                config = GlmManagerConfig(
                    glm_path=glm_path or r"C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe",
                    cpu_gating_enabled=glm_cpu_gating,
                )
                # Callback to reinitialize power controller after GLM restart
                self._glm_manager = GlmManager(
                    config=config,
                    reinit_callback=self._reinit_power_controller,
                )
                logger.info("sys.init: GlmManager initialized (will start GLM and watchdog)")
            except Exception as e:
                logger.warning(f"GlmManager not available: {e}")
        elif glm_manager_enabled and not GLM_MANAGER_AVAILABLE:
            logger.warning("--glm_manager requested but GlmManager not available (missing dependencies)")

        # Power control via UI automation
        # Skip init if GLM Manager is enabled - it will init after starting GLM
        self._power_controller = None
        if POWER_CONTROL_AVAILABLE and not self._glm_manager:
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
                logger.info("sys.init: GlmPowerController initialized for UI-based power control")
            except Exception as e:
                logger.warning(f"GlmPowerController not available: {e}")

    def _reinit_power_controller(self, pid: int = None, minimize_after: bool = True):
        """Reinitialize power controller after GLM restart and sync power state.

        Args:
            pid: GLM process ID for window filtering
            minimize_after: If True, minimize GLM window after reinit (for restarts)
        """
        if POWER_CONTROL_AVAILABLE:
            try:
                # Recreate power controller with PID to find correct window
                self._power_controller = GlmPowerController(steal_focus=True, pid=pid)
                logger.info(f"power.init: Controller reinitialized after GLM restart (PID={pid})")

                # Wait for GLM UI to fully render (splash screen ~5s + OpenGL init)
                time.sleep(1.0)

                # Sync power state from UI (overrides any MIDI-based detection)
                state = self._power_controller.get_state()
                if state in ("on", "off"):
                    glm_controller.power = (state == "on")
                    glm_controller._notify_state_change()
                    logger.info(f"power.init: State synced from GLM UI after restart: {state.upper()}")
                else:
                    logger.warning(f"Could not determine GLM power state after restart: {state}")

                # Minimize GLM window (uses same pywinauto window as power operations)
                if minimize_after:
                    time.sleep(1.0)  # Let GLM finish any startup animation
                    logger.info("Minimizing GLM window after reinit")
                    self._power_controller.minimize()
            except Exception as e:
                logger.warning(f"Failed to reinitialize power controller: {e}")

    def _get_midi_output(self):
        """Get connected MIDI output, reconnecting if necessary. Thread-safe."""
        with self._midi_output_lock:
            if self._midi_output is None:
                try:
                    self._midi_output = open_output(self.midi_in_channel)
                    logger.info(f"midi.connect: Connected to MIDI channel '{self.midi_in_channel}'")
                    retry_logger.reset("midi_output")  # Reset on successful connection
                except (OSError, IOError) as e:
                    if retry_logger.should_log("midi_output"):
                        info = retry_logger.format_retry_info("midi_output")
                        logger.warning(f"midi.error: Failed to connect to '{self.midi_in_channel}': {e} {info}")
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
                    logger.info(f"hid.connect: Connected to HID device VID: {hex(self.vid)} PID: {hex(self.pid)}")
                    retry_logger.reset("hid_connect")  # Reset on successful connection
                except (OSError, IOError) as e:
                    if retry_logger.should_log("hid_connect"):
                        info = retry_logger.format_retry_info("hid_connect")
                        logger.warning(f"hid.error: Failed to open HID device: {e}. Retrying... {info}")
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
                        logger.debug(f"hid.input: No binding for key {KEY_NAMES.get(keyreported, keyreported)}")
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
                        logger.debug(f"hid.input: Action {action_type.value} not yet supported")
                        continue

                    tid = trace_ids.next("hid")
                    self.queue.put(QueuedAction(action=glm_action, timestamp=now, trace_id=tid))
                    logger.debug(f"[{tid}] hid.input: key={KEY_NAMES.get(keyreported, keyreported)} -> {glm_action}")
            except (OSError, IOError) as e:
                if retry_logger.should_log("hid_error"):
                    info = retry_logger.format_retry_info("hid_error")
                    logger.warning(f"hid.error: Device error: {e}. Reconnecting... {info}")
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
                logger.info(f"midi.connect: Connected to MIDI output channel '{self.midi_out_channel}' for state reading")
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
                                # Early pre-gap check: if pattern is clearly embedded in a
                                # message stream (< 50ms silence before), skip full gap analysis
                                if len(self._rx_seq) > 5:
                                    pre_gap = self._rx_seq[-5][0] - self._rx_seq[-6][0]
                                    if pre_gap < 0.05:
                                        self._rx_seq = []
                                        continue
                                else:
                                    pre_gap = float('inf')  # No prior message = isolated burst

                                # Full gap analysis for plausible candidates
                                # Triple-condition filter for robustness:
                                # 1. No single gap > MAX_GAP (260ms) - covers RF remote and GUI/RDP clicks
                                # 2. Total of all gaps < MAX_TOTAL (350ms) - catches false positives
                                # 3. Pre-gap before pattern > PRE_GAP (120ms) - primary defense
                                # RF remote bursts: uniform ~31ms gaps, total ~124ms
                                # GUI clicks via RDP: gap[1] can reach ~243ms, total ~316ms
                                pattern_times = [t for t, _ in self._rx_seq[-5:]]
                                gaps = [pattern_times[i+1] - pattern_times[i] for i in range(4)]
                                max_gap = max(gaps)
                                total_gap = sum(gaps)

                                # Reject if any condition fails
                                if max_gap > POWER_PATTERN_MAX_GAP or total_gap > POWER_PATTERN_MAX_TOTAL or pre_gap < POWER_PATTERN_PRE_GAP:
                                    reason = []
                                    if max_gap > POWER_PATTERN_MAX_GAP:
                                        reason.append(f"max gap {max_gap*1000:.0f}ms > {POWER_PATTERN_MAX_GAP*1000:.0f}ms")
                                    if total_gap > POWER_PATTERN_MAX_TOTAL:
                                        reason.append(f"total {total_gap*1000:.0f}ms > {POWER_PATTERN_MAX_TOTAL*1000:.0f}ms")
                                    if pre_gap < POWER_PATTERN_PRE_GAP:
                                        reason.append(f"pre-gap {pre_gap*1000:.0f}ms < {POWER_PATTERN_PRE_GAP*1000:.0f}ms")
                                    logger.debug(f"power.pattern: Rejected: {', '.join(reason)} (gaps: {[f'{g*1000:.0f}ms' for g in gaps]})")
                                    self._rx_seq = []
                                    continue

                                # Skip pattern processing during startup/volume init
                                if self._suppress_power_pattern:
                                    logger.debug("power.pattern: Ignored (suppressed during init)")
                                    self._rx_seq = []
                                    continue

                                # Skip pattern processing during power cooldown
                                # (UI automation already verified state)
                                allowed, wait_time, _ = glm_controller.can_accept_power_command()
                                if not allowed:
                                    logger.debug(f"power.pattern: Ignored during cooldown ({wait_time:.1f}s remaining)")
                                    self._rx_seq = []
                                    continue

                                # Power pattern detected - trust the MIDI pattern and toggle state
                                # GLM bug: Power button visual doesn't update on RF remote toggle
                                # (only updates when button is clicked directly). Verified via RDP observation.
                                new_power = glm_controller.toggle_power_from_midi_pattern()
                                logger.info(f"power.pattern: RF power toggle detected - now {'ON' if new_power else 'OFF'}")

                                self._rx_seq = []  # Clear after detection

                        # Process state update
                        changed = glm_controller.update_from_midi(msg.control, msg.value)
                        if changed:
                            state = glm_controller.get_state()
                            logger.debug(f"state.change: vol={state['volume']}, mute={state['mute']}, dim={state['dim']}, pwr={state['power']}")
                    else:
                        # Log non-control_change messages (unexpected but want to see them)
                        log_midi("RX", msg.type, raw=str(msg))

            except (OSError, IOError) as e:
                if not self._stop_event.is_set():  # Only log if not shutting down
                    if retry_logger.should_log("midi_reader"):
                        info = retry_logger.format_retry_info("midi_reader")
                        logger.warning(f"midi.error: Reader error: {e}. Reconnecting... {info}")
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
                logger.info("sys.shutdown: Consumer thread exiting")
                break

            # Handle QueuedAction objects
            now = time.time()
            event_age = now - queued.timestamp
            tid = queued.trace_id
            prefix = f"[{tid}] " if tid else ""

            if event_age > MAX_EVENT_AGE:
                logger.warning(f"{prefix}queue.stale: Discarded {queued.action} (age={event_age:.1f}s)")
                continue

            action = queued.action

            # Check if commands are blocked during power settling
            if isinstance(action, SetPower):
                # Power commands have extended cooldown
                allowed, wait_time, reason = glm_controller.can_accept_power_command()
                if not allowed:
                    if reason == "power_settling":
                        logger.warning(f"{prefix}power.blocked: settling ({wait_time:.1f}s remaining)")
                    else:
                        logger.warning(f"{prefix}power.blocked: cooldown ({wait_time:.1f}s remaining)")
                    continue
            else:
                # All other commands blocked only during settling
                allowed, wait_time, reason = glm_controller.can_accept_command()
                if not allowed:
                    logger.warning(f"{prefix}queue.blocked: power settling ({wait_time:.1f}s remaining)")
                    continue

            # Dispatch based on action type
            try:
                if isinstance(action, SetVolume):
                    self._handle_set_volume(action.target, trace_id=tid)
                elif isinstance(action, AdjustVolume):
                    self._handle_adjust_volume(action.delta, trace_id=tid)
                elif isinstance(action, SetMute):
                    logger.debug(f"{prefix}midi.tx: Sending Mute (CC {GLM_MUTE_CC})")
                    self._send_action(Action.MUTE, trace_id=tid)
                    time.sleep(SEND_DELAY)
                elif isinstance(action, SetDim):
                    logger.debug(f"{prefix}midi.tx: Sending Dim (CC {GLM_DIM_CC})")
                    self._send_action(Action.DIM, trace_id=tid)
                    time.sleep(SEND_DELAY)
                elif isinstance(action, SetPower):
                    self._handle_power_action(action, trace_id=tid)
                else:
                    logger.debug(f"{prefix}queue.unknown: {type(action).__name__}")
            except Exception as e:
                logger.error(f"{prefix}queue.error: Processing {action}: {e}", exc_info=True)

    def _send_action(self, action: Action, trace_id: str = ""):
        """Send an action to GLM using the controller."""
        prefix = f"[{trace_id}] " if trace_id else ""
        midi_out = self._get_midi_output()
        if midi_out is None:
            logger.warning(f"{prefix}midi.error: Output not connected, skipping action")
            return

        try:
            glm_controller.send_action(action, midi_out, trace_id=trace_id)
        except (OSError, IOError) as e:
            logger.error(f"{prefix}midi.error: Sending {action.value}: {e}")
            self._reset_midi_output()

    def _handle_power_action(self, action: SetPower, trace_id: str = ""):
        """
        Handle power control via UI automation.

        This uses GlmPowerController to click the power button in GLM,
        providing deterministic state control with verification.
        """
        prefix = f"[{trace_id}] " if trace_id else ""

        if self._power_controller is None:
            logger.error(f"{prefix}power.error: GlmPowerController not initialized")
            return

        # Determine target state
        if action.state is None:
            # Toggle: invert current state
            target_state = not glm_controller.power
        else:
            target_state = action.state

        desired = "on" if target_state else "off"
        logger.info(f"{prefix}power.begin: Setting to {desired.upper()} via UI automation")

        # Start power transition (blocks all commands)
        glm_controller.start_power_transition(target_state, trace_id=trace_id)
        transition_start = time.time()

        # Ensure session is connected to console before UI automation
        # This uses WTSEnumerateSessionsW to detect disconnected RDP sessions
        # and reconnects via tscon if needed
        if ensure_session_connected:
            if not ensure_session_connected(logger=logger):
                logger.error(f"{prefix}power.error: Could not ensure session is connected to console")
                glm_controller.end_power_transition(success=False)
                return

        success = False
        try:
            # Execute via UI automation
            self._power_controller.set_state(desired, verify=True)
            success = True
        except Exception as e:
            logger.error(f"{prefix}power.error: UI automation failed: {e}")

        # Wait for full settling time before ending transition
        # This ensures UI shows transitioning state for the full 2 seconds
        elapsed = time.time() - transition_start
        if elapsed < POWER_SETTLING_TIME:
            remaining = POWER_SETTLING_TIME - elapsed
            logger.debug(f"{prefix}power.settling: Waiting {remaining:.1f}s")
            time.sleep(remaining)

        # Now end transition (UI will stop showing transitioning state)
        if success:
            glm_controller.end_power_transition(success=True, actual_state=target_state)
        else:
            glm_controller.end_power_transition(success=False)

    def _handle_adjust_volume(self, delta: int, trace_id: str = ""):
        """
        Handle volume changes using absolute volume (CC 20) when possible.

        Args:
            delta: Volume change amount. Positive = up, negative = down.
            trace_id: Trace ID for log correlation.

        If we have a valid volume reading from GLM, calculate target and send
        one absolute command. This avoids GLM dropping rapid increment commands.

        If volume is not yet initialized, fall back to single CC 21/22.
        """
        prefix = f"[{trace_id}] " if trace_id else ""
        midi_out = self._get_midi_output()
        if midi_out is None:
            logger.warning(f"{prefix}midi.error: Output not connected, skipping volume action")
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
                    logger.debug(f"{prefix}volume: {current} -> {target} (delta={sign}{delta}, CC 20)")
                    glm_controller.set_pending_volume(target)
                    glm_controller.send_volume_absolute(target, midi_out, trace_id=trace_id)
                    # Clear power pattern buffer - GLM's response (DIM, MUTE, VOL)
                    # should not be mistaken for power toggle pattern
                    self._rx_seq = []
                else:
                    direction = "up" if delta > 0 else "down"
                    logger.debug(f"{prefix}volume: Already at limit ({current}), ignoring {direction}")
            else:
                # Volume not initialized yet - use CC 21/22 to trigger GLM state report
                action = Action.VOL_UP if delta > 0 else Action.VOL_DOWN
                logger.debug(f"{prefix}volume: Not initialized, using {action.value} (CC 21/22) to trigger state")
                glm_controller.send_action(action, midi_out, trace_id=trace_id)
        except (OSError, IOError) as e:
            logger.error(f"{prefix}midi.error: Volume action failed: {e}")
            self._reset_midi_output()

    def _handle_set_volume(self, target: int, trace_id: str = ""):
        """
        Handle absolute volume setting (from REST API).

        Args:
            target: Target volume (0-127).
            trace_id: Trace ID for log correlation.
        """
        prefix = f"[{trace_id}] " if trace_id else ""
        midi_out = self._get_midi_output()
        if midi_out is None:
            logger.warning(f"{prefix}midi.error: Output not connected, skipping volume action")
            return

        target = max(0, min(127, target))
        try:
            logger.debug(f"{prefix}volume: Setting to {target} (CC 20)")
            glm_controller.set_pending_volume(target)
            glm_controller.send_volume_absolute(target, midi_out, trace_id=trace_id)
            # Clear power pattern buffer - GLM's response should not trigger pattern
            self._rx_seq = []
        except (OSError, IOError) as e:
            logger.error(f"{prefix}midi.error: Setting volume failed: {e}")
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

        # Suppress power pattern detection during volume init
        # (GLM responses can form false power patterns)
        self._suppress_power_pattern = True

        init_tid = trace_ids.next("sys")
        try:
            if self.startup_volume is not None:
                # Set volume to specified value
                logger.info(f"[{init_tid}] sys.init: Setting startup volume to {self.startup_volume}")
                glm_controller.send_volume_absolute(self.startup_volume, midi_out, trace_id=init_tid)
            else:
                # Query current volume by sending vol+1 then vol-1
                logger.info(f"[{init_tid}] sys.init: Querying current GLM volume (sending vol+1, vol-1)...")
                glm_controller.send_action(Action.VOL_UP, midi_out, trace_id=init_tid)
                time.sleep(GLM_VOL_QUERY_DELAY)
                glm_controller.send_action(Action.VOL_DOWN, midi_out, trace_id=init_tid)

            # Wait for GLM to respond with volume state
            time.sleep(GLM_VOL_RESPONSE_WAIT)
        finally:
            # Clear power pattern buffer and re-enable detection
            self._rx_seq = []
            self._suppress_power_pattern = False

        if glm_controller.has_valid_volume:
            logger.info(f"[{init_tid}] sys.init: GLM volume initialized: {glm_controller.volume}")
        else:
            logger.warning(f"[{init_tid}] sys.init: GLM volume state not yet received. Will initialize on first volume command.")

    def start(self):
        """Starts all threads."""
        # Start GLM Manager first (ensures GLM is running before we try to sync state)
        if self._glm_manager:
            logger.info("Starting GLM Manager (will start GLM and watchdog)...")
            if self._glm_manager.start():
                logger.info("GLM Manager started successfully")
                # Reinitialize power controller now that GLM is running (window still visible)
                # Don't minimize here - we do it at end of start() after all init complete
                self._reinit_power_controller(pid=self._glm_manager.pid, minimize_after=False)
            else:
                logger.error("GLM Manager failed to start")

        # Register state change callback for logging
        def log_state_change(state: dict):
            transitioning = " [TRANSITIONING]" if state.get('power_transitioning') else ""
            logger.info(f"state.change: vol={state['volume']}, mute={state['mute']}, dim={state['dim']}, pwr={state['power']}{transitioning}")
        glm_controller.add_state_callback(log_state_change)

        # Sync power state from GLM UI (before starting threads)
        # Skip if GLM Manager already did this in _reinit_power_controller
        if self._power_controller and not self._glm_manager:
            try:
                state = self._power_controller.get_state()
                if state in ("on", "off"):
                    glm_controller.power = (state == "on")
                    logger.info(f"power.init: State synced from GLM UI: {state.upper()}")
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

        # Minimize GLM window at the very end of startup
        # Use power controller's minimize to ensure same window handle as power operations
        if self._power_controller:
            # Give GLM a moment to finish any startup animation
            time.sleep(1.0)
            logger.info("Minimizing GLM window (post-startup)")
            self._power_controller.minimize()

    def stop(self):
        """Stops the daemon gracefully."""
        logger.info("sys.shutdown: Stopping daemon...")
        self._stop_event.set()
        self.queue.put(None)  # Sentinel to unblock the consumer

        # Stop GLM Manager watchdog (but don't kill GLM - let it keep running)
        if self._glm_manager:
            self._glm_manager.stop(kill_glm=False)

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

        logger.info("sys.shutdown: Daemon stopped")

if __name__ == "__main__":
    args = parse_arguments(__file__)
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
            stop_logging()  # Stop logging thread before exit
            sys.exit(1)

    set_higher_priority()
    minimize_console_window()

    # RDP session priming - prevents high CPU in GLM after RDP disconnect
    # Only runs once per boot (before GLM starts)
    if needs_rdp_priming():
        prime_rdp_session()
    else:
        logger.debug("RDP session already primed this boot, skipping")

    # Restart Windows MIDI Service so LoopMIDI ports are visible
    # (Windows MIDI Services doesn't detect virtual ports created before it starts)
    restart_midi_service()

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
        args.glm_manager,
        args.glm_path,
        args.glm_cpu_gating,
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
