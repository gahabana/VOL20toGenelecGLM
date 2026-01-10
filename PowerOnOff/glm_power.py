"""
GLM Power Controller - UI automation for Genelec GLM power button.

Provides deterministic power state reading and setting via pixel sampling
and mouse automation. Use as a verification/fallback layer alongside MIDI.

Requirements (Windows only):
    pip install pywinauto pillow pywin32

Example usage:
    from PowerOnOff import GlmPowerController

    controller = GlmPowerController()

    # Read current state
    state = controller.get_state()  # "on", "off", or "unknown"

    # Set explicit state (not toggle!)
    controller.set_state("on")   # Ensure speakers are ON
    controller.set_state("off")  # Ensure speakers are OFF

    # Convenience methods
    controller.ensure_on()
    controller.ensure_off()
"""
from __future__ import annotations

import logging
import threading
import time
from dataclasses import dataclass
from statistics import median
from typing import Literal, Optional, Tuple, Callable

from .exceptions import (
    GlmWindowNotFoundError,
    GlmStateUnknownError,
    GlmStateChangeFailedError,
)

# Conditional imports for Windows-only functionality
try:
    import win32api
    import win32con
    import ctypes
    from ctypes import wintypes
    from PIL import ImageGrab
    from pywinauto import Desktop
    HAS_WIN32_DEPS = True
except ImportError:
    HAS_WIN32_DEPS = False


def is_console_session() -> bool:
    """
    Check if the current process is running in the console session.

    When started from RDP, the script runs in the RDP session and cannot
    access the console display after RDP disconnects. This function returns
    True only if current_session == console_session.

    Returns:
        True if running in console session (UI automation will work),
        False if running in a different session (UI automation may fail).
    """
    if not HAS_WIN32_DEPS:
        return False

    import os
    current_session = wintypes.DWORD()
    ctypes.windll.kernel32.ProcessIdToSessionId(os.getpid(), ctypes.byref(current_session))
    console_session = ctypes.windll.kernel32.WTSGetActiveConsoleSessionId()

    return current_session.value == console_session


def get_current_session_id() -> int:
    """
    Get the current process's Windows session ID.

    Returns:
        The session ID as an integer, or -1 if unavailable.
    """
    if not HAS_WIN32_DEPS:
        return -1

    import os
    current_session = wintypes.DWORD()
    ctypes.windll.kernel32.ProcessIdToSessionId(os.getpid(), ctypes.byref(current_session))
    return current_session.value


# WTS session states
WTS_CURRENT_SERVER_HANDLE = 0
WTSActive = 0
WTSConnected = 1
WTSConnectQuery = 2
WTSShadow = 3
WTSDisconnected = 4
WTSIdle = 5
WTSListen = 6
WTSReset = 7
WTSDown = 8
WTSInit = 9


def is_session_disconnected(session_id: int = None) -> bool:
    """
    Check if a session is in disconnected state using WTSEnumerateSessionsW.

    When RDP disconnects, the session enters 'Disconnected' state (4).
    This function detects that state so we can reconnect to console.

    Args:
        session_id: Session ID to check. If None, checks current process's session.

    Returns:
        True if the session is disconnected, False otherwise.
    """
    if not HAS_WIN32_DEPS:
        return False

    if session_id is None:
        session_id = get_current_session_id()
        if session_id < 0:
            return False

    try:
        # Define WTS_SESSION_INFO structure
        class WTS_SESSION_INFO(ctypes.Structure):
            _fields_ = [
                ("SessionId", wintypes.DWORD),
                ("pWinStationName", wintypes.LPWSTR),
                ("State", ctypes.c_int),
            ]

        ppSessionInfo = ctypes.POINTER(WTS_SESSION_INFO)()
        count = wintypes.DWORD()

        # Enumerate all sessions
        if ctypes.windll.wtsapi32.WTSEnumerateSessionsW(
            WTS_CURRENT_SERVER_HANDLE, 0, 1, ctypes.byref(ppSessionInfo), ctypes.byref(count)
        ):
            try:
                for i in range(count.value):
                    info = ppSessionInfo[i]
                    if info.SessionId == session_id:
                        return info.State == WTSDisconnected
            finally:
                ctypes.windll.wtsapi32.WTSFreeMemory(ppSessionInfo)

        return False

    except Exception:
        return False


def ensure_session_connected(logger=None) -> bool:
    """
    Check if current session is disconnected and reconnect to console if needed.

    This should be called before UI automation to ensure the display is available.

    Args:
        logger: Optional logger for debug output.

    Returns:
        True if session is connected (or was successfully reconnected),
        False if session is disconnected and reconnection failed.
    """
    session_id = get_current_session_id()
    if session_id < 0:
        return True  # Can't check, assume OK

    if not is_session_disconnected(session_id):
        return True  # Already connected

    # Session is disconnected, try to reconnect
    if logger:
        logger.info(f"Session {session_id} is disconnected, reconnecting to console...")

    return reconnect_to_console(logger=logger)


def reconnect_to_console(logger=None) -> bool:
    """
    Reconnect the current session to the console display using tscon.

    This is useful when RDP disconnects and leaves the session without an
    active desktop. Running tscon moves the session to the console (QXL/VNC)
    display, reactivating the desktop for UI automation.

    Requires the script to be run as Administrator, or psexec to be available
    in PATH for elevation to SYSTEM privileges.

    Args:
        logger: Optional logger for debug output.

    Returns:
        True if reconnection succeeded, False otherwise.
    """
    import subprocess
    import shutil
    import time

    session_id = get_current_session_id()
    if session_id < 0:
        if logger:
            logger.warning("Cannot reconnect: unable to get session ID")
        return False

    try:
        if logger:
            logger.info(f"Reconnecting session {session_id} to console via tscon...")

        # First try direct tscon (works if script is running as Admin)
        result = subprocess.run(
            ["tscon", str(session_id), "/dest:console"],
            capture_output=True,
            timeout=10,
            creationflags=subprocess.CREATE_NO_WINDOW if hasattr(subprocess, 'CREATE_NO_WINDOW') else 0,
        )

        if result.returncode == 0:
            if logger:
                logger.info("Successfully reconnected session to console, waiting 0.5s for display driver to settle...")
            time.sleep(0.5)  # Allow display driver to settle after tscon
            return True

        # If direct tscon failed, try with psexec for SYSTEM privileges
        psexec_path = shutil.which("psexec") or shutil.which("psexec64")
        if psexec_path:
            if logger:
                logger.debug("Direct tscon failed, trying with psexec...")
            result = subprocess.run(
                [psexec_path, "-s", "-accepteula", "tscon", str(session_id), "/dest:console"],
                capture_output=True,
                timeout=10,
                creationflags=subprocess.CREATE_NO_WINDOW if hasattr(subprocess, 'CREATE_NO_WINDOW') else 0,
            )

            if result.returncode == 0:
                if logger:
                    logger.info("Successfully reconnected session to console via psexec, waiting 0.5s for display driver to settle...")
                time.sleep(0.5)  # Allow display driver to settle after tscon
                return True

        # Both methods failed
        if logger:
            stderr = result.stderr.decode('utf-8', errors='ignore').strip()
            logger.warning(f"tscon failed (code {result.returncode}): {stderr}")
            logger.warning("To use session recovery, run the script as Administrator")
        return False

    except subprocess.TimeoutExpired:
        if logger:
            logger.warning("tscon timed out after 10 seconds")
        return False
    except Exception as e:
        if logger:
            logger.warning(f"Failed to run tscon: {e}")
        return False


def get_display_diagnostics() -> dict:
    """
    Get diagnostic information about displays, sessions, and windows.

    Returns dict with:
        - is_rdp_session: bool
        - current_session_id: int
        - console_session_id: int
        - is_console_session: bool (True if current == console)
        - monitors: list of monitor info
        - glm_windows: list of GLM window info
    """
    if not HAS_WIN32_DEPS:
        return {"error": "Windows dependencies not available"}

    result = {}

    # Check if in RDP session
    SM_REMOTESESSION = 0x1000
    result["is_rdp_session"] = ctypes.windll.user32.GetSystemMetrics(SM_REMOTESESSION) != 0

    # Get current process session ID
    import os
    current_session = wintypes.DWORD()
    ctypes.windll.kernel32.ProcessIdToSessionId(os.getpid(), ctypes.byref(current_session))
    result["current_session_id"] = current_session.value

    # Get console session ID
    result["console_session_id"] = ctypes.windll.kernel32.WTSGetActiveConsoleSessionId()

    # Check if we're in the console session
    result["is_console_session"] = result["current_session_id"] == result["console_session_id"]

    # Enumerate monitors
    monitors = []
    def monitor_callback(hMonitor, hdcMonitor, lprcMonitor, dwData):
        info = win32api.GetMonitorInfo(hMonitor)
        monitors.append({
            "device": info.get("Device", "unknown"),
            "work_area": info.get("Work", ()),
            "monitor_area": info.get("Monitor", ()),
            "is_primary": info.get("Flags", 0) == 1,
        })
        return True

    # EnumDisplayMonitors callback type
    MonitorEnumProc = ctypes.WINFUNCTYPE(
        ctypes.c_bool,
        ctypes.c_ulong,  # hMonitor
        ctypes.c_ulong,  # hdcMonitor
        ctypes.POINTER(ctypes.c_long * 4),  # lprcMonitor
        ctypes.c_double  # dwData
    )

    try:
        ctypes.windll.user32.EnumDisplayMonitors(
            None, None, MonitorEnumProc(monitor_callback), 0
        )
    except Exception as e:
        monitors.append({"error": str(e)})

    result["monitors"] = monitors
    result["monitor_count"] = len(monitors)

    # Find GLM windows
    glm_windows = []
    try:
        wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")
        for w in wins:
            try:
                title = w.window_text() or ""
                if "GLM" in title:
                    rect = w.rectangle()
                    glm_windows.append({
                        "title": title,
                        "rect": (rect.left, rect.top, rect.right, rect.bottom),
                        "visible": w.is_visible(),
                    })
            except Exception:
                pass
    except Exception as e:
        glm_windows.append({"error": str(e)})

    result["glm_windows"] = glm_windows

    return result


# Type alias for power state
PowerState = Literal["on", "off", "unknown"]


@dataclass(frozen=True)
class Point:
    """Screen coordinate."""
    x: int
    y: int


@dataclass
class WindowState:
    """
    Captured state of a window before UI automation.

    Used to restore the window to its previous state after operations.
    """
    was_minimized: bool = False
    previous_foreground_hwnd: int = 0


@dataclass
class GlmPowerConfig:
    """
    Configuration for GLM power button detection.

    Attributes:
        dx_from_right: Horizontal offset from window right edge to button center.
        dy_from_top: Vertical offset from window top edge to button center.
        patch_radius: Radius for median color sampling (2*r+1 square).
        fallback_nudge_x: Secondary sample point offset (avoid glyph).
        focus_delay: Seconds to wait after focusing window.
        post_click_delay: Seconds to wait after clicking.
        verify_timeout: Seconds to wait for state change verification.
        poll_interval: Seconds between state polls during verification.
    """
    dx_from_right: int = 28
    dy_from_top: int = 80
    patch_radius: int = 4
    fallback_nudge_x: int = 8
    focus_delay: float = 0.15
    post_click_delay: float = 0.35
    verify_timeout: float = 3.0
    poll_interval: float = 0.15
    # Color thresholds for state classification
    off_max_brightness: int = 95
    off_max_channel_diff: int = 22
    on_min_green: int = 110
    on_green_red_diff: int = 35


class GlmPowerController:
    """
    Thread-safe controller for GLM power button via UI automation.

    This provides deterministic power state reading and setting by sampling
    the power button's background color and synthesizing mouse clicks.

    Thread Safety:
        All public methods are thread-safe. Internal state is protected by a lock.
        However, the actual UI operations (focus, click) should ideally be called
        from a single thread to avoid race conditions with window focus.
    """

    def __init__(
        self,
        config: Optional[GlmPowerConfig] = None,
        logger: Optional[logging.Logger] = None,
        steal_focus: bool = True,
        pid: Optional[int] = None,
    ):
        """
        Initialize the power controller.

        Args:
            config: Configuration for button detection. Uses defaults if None.
            logger: Logger instance. Creates a default if None.
            steal_focus: If True, will focus GLM window before operations.
                        If False, operations may fail if window is not visible.
            pid: Optional GLM process ID. If provided, only windows belonging to
                this PID will be considered. This avoids finding wrong windows
                during startup when multiple JUCE windows may exist.
        """
        if not HAS_WIN32_DEPS:
            raise ImportError(
                "GlmPowerController requires Windows with pywinauto, pillow, and pywin32. "
                "Install with: pip install pywinauto pillow pywin32"
            )

        self.config = config or GlmPowerConfig()
        self.logger = logger or logging.getLogger(__name__)
        self.steal_focus = steal_focus
        self._pid = pid
        self._lock = threading.Lock()
        self._last_known_state: PowerState = "unknown"
        self._window_cache = None
        self._window_cache_time = 0
        self._window_cache_ttl = 5.0  # Re-find window after 5 seconds

    def _find_window(self, use_cache: bool = True):
        """
        Find the GLM window.

        Args:
            use_cache: If True, returns cached window if still valid.

        Returns:
            pywinauto window wrapper.

        Raises:
            GlmWindowNotFoundError: If GLM window not found.
        """
        now = time.time()
        if use_cache and self._window_cache is not None:
            if (now - self._window_cache_time) < self._window_cache_ttl:
                # Verify window still exists
                try:
                    self._window_cache.window_text()
                    return self._window_cache
                except Exception:
                    self._window_cache = None

        # Find GLM window (JUCE app)
        wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")
        candidates = [w for w in wins if "GLM" in (w.window_text() or "")]

        # Filter by PID if specified (avoids finding wrong window during startup)
        if self._pid and candidates:
            pid_filtered = []
            for w in candidates:
                try:
                    if w.process_id() == self._pid:
                        pid_filtered.append(w)
                except Exception:
                    pass  # Skip windows we can't query
            # When PID is specified, ONLY use PID-filtered results (don't fall back)
            candidates = pid_filtered
            if candidates:
                self.logger.debug(f"Filtered to {len(candidates)} window(s) by PID {self._pid}")

        if not candidates:
            raise GlmWindowNotFoundError(
                "GLM window not found. Is GLM running and visible?"
            )

        self._window_cache = candidates[0]
        self._window_cache_time = now
        return self._window_cache

    def _ensure_foreground(self, win) -> None:
        """Restore and focus the GLM window if steal_focus is enabled."""
        if not self.steal_focus:
            return

        hwnd = win.handle

        # First, dismiss any overlays (Start menu, etc.) by pressing Escape
        try:
            win32api.keybd_event(0x1B, 0, 0, 0)  # VK_ESCAPE key down
            win32api.keybd_event(0x1B, 0, 2, 0)  # VK_ESCAPE key up
            time.sleep(0.05)  # Brief pause for overlay to dismiss
        except Exception:
            pass

        try:
            # Restore if minimized
            win.restore()
        except Exception:
            pass

        # Use Alt key trick to allow SetForegroundWindow to work
        # Pressing Alt temporarily allows any process to set foreground window
        try:
            VK_MENU = 0x12  # Alt key
            KEYEVENTF_EXTENDEDKEY = 0x0001
            KEYEVENTF_KEYUP = 0x0002

            # Press and release Alt
            win32api.keybd_event(VK_MENU, 0, KEYEVENTF_EXTENDEDKEY, 0)
            win32api.keybd_event(VK_MENU, 0, KEYEVENTF_EXTENDEDKEY | KEYEVENTF_KEYUP, 0)

            # Now SetForegroundWindow should work
            ctypes.windll.user32.SetForegroundWindow(hwnd)
        except Exception as e:
            self.logger.debug(f"Alt+SetForegroundWindow failed: {e}")

            # Fallback: try pywinauto's set_focus
            try:
                win.set_focus()
            except Exception as e2:
                self.logger.debug(f"Could not set focus (continuing anyway): {e2}")

        time.sleep(self.config.focus_delay)

        # Wait for window to have valid coordinates (may take time after restore)
        # Invalid coordinates like negative values indicate window isn't fully restored
        for _ in range(10):  # Max 10 retries = ~1 second
            r = win.rectangle()
            if r.left >= 0 and r.top >= 0 and r.right > r.left and r.bottom > r.top:
                break  # Valid rectangle
            self.logger.debug(f"Waiting for window restore (rect={r.left},{r.top},{r.right},{r.bottom})")
            time.sleep(0.1)

    def _capture_window_state(self, win) -> WindowState:
        """
        Capture window state before UI operations.

        Call this BEFORE any focus/restore operations to record:
        - Whether GLM was minimized
        - Which window had foreground focus

        Args:
            win: The GLM window wrapper.

        Returns:
            WindowState with captured state.
        """
        state = WindowState()

        try:
            hwnd = win.handle
            # Check if window is minimized (IsIconic returns non-zero if minimized)
            state.was_minimized = bool(ctypes.windll.user32.IsIconic(hwnd))
        except Exception as e:
            self.logger.debug(f"Failed to check minimized state: {e}")

        try:
            # Record current foreground window so we can restore focus
            state.previous_foreground_hwnd = ctypes.windll.user32.GetForegroundWindow()
        except Exception as e:
            self.logger.debug(f"Failed to get foreground window: {e}")

        return state

    def _restore_window_state(self, win, state: WindowState) -> None:
        """
        Restore window state after UI operations.

        Call this AFTER operations complete to:
        - Re-minimize GLM if it was minimized before
        - Restore focus to the previous foreground window

        Args:
            win: The GLM window wrapper.
            state: WindowState captured before operations.
        """
        if not self.steal_focus:
            return  # Nothing was changed

        hwnd = win.handle

        # If GLM was minimized before, minimize it again
        if state.was_minimized:
            try:
                # Use pywinauto's minimize (more reliable for JUCE apps)
                win.minimize()
                time.sleep(0.1)  # Brief delay for minimize to complete
                # Verify it worked
                is_iconic = bool(ctypes.windll.user32.IsIconic(hwnd))
                if is_iconic:
                    self.logger.debug("Restored GLM to minimized state")
                else:
                    self.logger.warning(f"Failed to restore GLM to minimized state (IsIconic={is_iconic})")
            except Exception as e:
                self.logger.debug(f"Failed to minimize GLM: {e}")

        # Restore focus to previous foreground window
        if state.previous_foreground_hwnd and state.previous_foreground_hwnd != hwnd:
            try:
                # Brief delay to let minimize complete if we're minimizing
                if state.was_minimized:
                    time.sleep(0.05)
                ctypes.windll.user32.SetForegroundWindow(state.previous_foreground_hwnd)
                self.logger.debug(f"Restored focus to previous window (hwnd={state.previous_foreground_hwnd})")
            except Exception as e:
                self.logger.debug(f"Failed to restore foreground window: {e}")

    def _get_power_point(self, win) -> Point:
        """Get screen coordinates of power button center."""
        r = win.rectangle()
        return Point(
            r.right - self.config.dx_from_right,
            r.top + self.config.dy_from_top
        )

    def _get_patch_median_rgb(self, center: Point) -> Tuple[int, int, int]:
        """Sample a patch and return per-channel median RGB."""
        radius = self.config.patch_radius
        left = center.x - radius
        top = center.y - radius
        right = center.x + radius + 1
        bottom = center.y + radius + 1

        img = ImageGrab.grab(bbox=(left, top, right, bottom), all_screens=True)
        pixels = list(img.getdata())

        rs = [p[0] for p in pixels]
        gs = [p[1] for p in pixels]
        bs = [p[2] for p in pixels]

        return (int(median(rs)), int(median(gs)), int(median(bs)))

    def _classify_state(self, rgb: Tuple[int, int, int]) -> PowerState:
        """
        Classify power state from RGB color.

        Returns "on", "off", or "unknown".
        """
        r, g, b = rgb
        cfg = self.config

        # OFF: dark grey (low brightness, channels close together)
        if (max(r, g, b) <= cfg.off_max_brightness and
            abs(r - g) <= cfg.off_max_channel_diff and
            abs(g - b) <= cfg.off_max_channel_diff):
            return "off"

        # ON: green/teal (green channel elevated above red)
        if g >= cfg.on_min_green and (g - r) >= cfg.on_green_red_diff:
            return "on"

        return "unknown"

    def _read_state_internal(self, win) -> Tuple[PowerState, Tuple[int, int, int], Point]:
        """
        Internal state reading without window finding.

        Returns (state, rgb, point).
        """
        pt = self._get_power_point(win)
        rgb = self._get_patch_median_rgb(pt)
        state = self._classify_state(rgb)

        # Try fallback point if unknown
        if state == "unknown" and self.config.fallback_nudge_x:
            pt2 = Point(pt.x - self.config.fallback_nudge_x, pt.y)
            rgb2 = self._get_patch_median_rgb(pt2)
            state2 = self._classify_state(rgb2)
            if state2 != "unknown":
                return state2, rgb2, pt2

        return state, rgb, pt

    def _click_point(self, pt: Point) -> None:
        """Synthesize a left mouse click."""
        win32api.SetCursorPos((pt.x, pt.y))
        time.sleep(0.02)
        win32api.mouse_event(win32con.MOUSEEVENTF_LEFTDOWN, 0, 0, 0, 0)
        time.sleep(0.02)
        win32api.mouse_event(win32con.MOUSEEVENTF_LEFTUP, 0, 0, 0, 0)

    def _wait_for_state(
        self,
        win,
        desired: PowerState,
        timeout: float = None,
    ) -> Tuple[PowerState, Tuple[int, int, int], Point]:
        """Poll until desired state or timeout."""
        timeout = timeout or self.config.verify_timeout
        deadline = time.time() + timeout
        last = ("unknown", (0, 0, 0), Point(0, 0))

        while time.time() < deadline:
            last = self._read_state_internal(win)
            if last[0] == desired:
                return last
            time.sleep(self.config.poll_interval)

        return last

    # =========================================================================
    # Public API
    # =========================================================================

    def get_state(self, restore_window: bool = True) -> PowerState:
        """
        Read current power state from GLM UI.

        Args:
            restore_window: If True (default), restore GLM to its previous state
                           (minimized/focus) after reading. Set to False if you
                           plan to interact with GLM immediately after.

        Returns:
            "on", "off", or "unknown"

        Raises:
            GlmWindowNotFoundError: If GLM window not found.
        """
        with self._lock:
            win = self._find_window()

            # Capture state before any focus changes
            saved_state = None
            if self.steal_focus and restore_window:
                saved_state = self._capture_window_state(win)

            try:
                if self.steal_focus:
                    self._ensure_foreground(win)

                state, rgb, pt = self._read_state_internal(win)
                self.logger.debug(f"Power state: {state} (rgb={rgb}, pt=({pt.x},{pt.y}))")

                if state != "unknown":
                    self._last_known_state = state

                return state
            finally:
                # Restore window state (runs even if exception occurred)
                if saved_state:
                    self._restore_window_state(win, saved_state)

    def get_state_with_details(self, restore_window: bool = True) -> Tuple[PowerState, Tuple[int, int, int], Tuple[int, int]]:
        """
        Read current power state with diagnostic details.

        Args:
            restore_window: If True (default), restore GLM to its previous state
                           after reading.

        Returns:
            Tuple of (state, rgb, (x, y))

        Raises:
            GlmWindowNotFoundError: If GLM window not found.
        """
        with self._lock:
            win = self._find_window()

            # Capture state before any focus changes
            saved_state = None
            if self.steal_focus and restore_window:
                saved_state = self._capture_window_state(win)

            try:
                if self.steal_focus:
                    self._ensure_foreground(win)

                state, rgb, pt = self._read_state_internal(win)

                if state != "unknown":
                    self._last_known_state = state

                return state, rgb, (pt.x, pt.y)
            finally:
                if saved_state:
                    self._restore_window_state(win, saved_state)

    def set_state(
        self,
        desired: Literal["on", "off"],
        verify: bool = True,
        retries: int = 2,
        restore_window: bool = True,
    ) -> bool:
        """
        Set power to desired state. Only clicks if state differs.

        Args:
            desired: Target state ("on" or "off").
            verify: If True, poll to verify state changed.
            retries: Number of click retries if verification fails.
            restore_window: If True (default), restore GLM to its previous state
                           (minimized/focus) after setting.

        Returns:
            True if state is now as desired, False otherwise.

        Raises:
            GlmWindowNotFoundError: If GLM window not found.
            GlmStateUnknownError: If initial state cannot be determined.
            GlmStateChangeFailedError: If state change fails after retries.
        """
        if desired not in ("on", "off"):
            raise ValueError("desired must be 'on' or 'off'")

        with self._lock:
            t0 = time.time()
            win = self._find_window(use_cache=False)  # Fresh lookup for state changes

            # Capture state before any focus changes
            saved_state = None
            if self.steal_focus and restore_window:
                saved_state = self._capture_window_state(win)

            try:
                t1 = time.time()
                self._ensure_foreground(win)
                t2 = time.time()

                # Read current state (single read, no polling)
                state, rgb, pt = self._read_state_internal(win)
                t3 = time.time()
                self.logger.debug(
                    f"Power set_state({desired}): current={state}, rgb={rgb} "
                    f"[find={t1-t0:.3f}s, focus={t2-t1:.3f}s, read={t3-t2:.3f}s]"
                )

                if state == desired:
                    self.logger.debug(f"Power already {desired}")
                    self._last_known_state = state
                    return True

                if state == "unknown":
                    raise GlmStateUnknownError(
                        f"Cannot determine initial power state",
                        rgb=rgb,
                        point=(pt.x, pt.y)
                    )

                # Attempt clicks with retries
                for attempt in range(retries + 1):
                    state, rgb, pt = self._read_state_internal(win)
                    self.logger.debug(
                        f"Power attempt {attempt}: state={state}, rgb={rgb}"
                    )

                    if state == desired:
                        self._last_known_state = state
                        return True

                    if state == "unknown":
                        raise GlmStateUnknownError(
                            f"Lost track of power state during set_state",
                            rgb=rgb,
                            point=(pt.x, pt.y)
                        )

                    # Click the button
                    self._click_point(pt)

                    if verify:
                        # Wait for state to change
                        state2, rgb2, pt2 = self._wait_for_state(win, desired)
                        self.logger.debug(
                            f"Power verify {attempt}: state={state2}, rgb={rgb2}"
                        )

                        if state2 == desired:
                            self._last_known_state = state2
                            self.logger.info(f"Power set to {desired}")
                            return True
                    else:
                        # Assume success without verification
                        self._last_known_state = desired
                        return True

                # All retries exhausted
                final_state, final_rgb, final_pt = self._read_state_internal(win)
                raise GlmStateChangeFailedError(
                    f"Failed to set power to {desired} after {retries + 1} attempts",
                    desired=desired,
                    actual=final_state
                )
            finally:
                if saved_state:
                    self._restore_window_state(win, saved_state)

    def ensure_on(self, verify: bool = True) -> bool:
        """
        Ensure power is ON.

        Returns True if successful, raises on failure.
        """
        return self.set_state("on", verify=verify)

    def ensure_off(self, verify: bool = True) -> bool:
        """
        Ensure power is OFF.

        Returns True if successful, raises on failure.
        """
        return self.set_state("off", verify=verify)

    def toggle(self, verify: bool = True, restore_window: bool = True) -> PowerState:
        """
        Toggle power state.

        Args:
            verify: If True, poll to verify state changed.
            restore_window: If True (default), restore GLM to its previous state after toggling.

        Returns the new state after toggling.

        Raises:
            GlmStateUnknownError: If current state cannot be determined.
        """
        current = self.get_state(restore_window=False)  # Don't restore yet
        if current == "unknown":
            raise GlmStateUnknownError("Cannot toggle: current state unknown")

        new_state = "off" if current == "on" else "on"
        self.set_state(new_state, verify=verify, restore_window=restore_window)
        return new_state

    @property
    def last_known_state(self) -> PowerState:
        """
        Return the last successfully read power state.

        This does not perform a new read - use get_state() for that.
        """
        with self._lock:
            return self._last_known_state

    def minimize(self) -> bool:
        """
        Minimize the GLM window.

        Uses pywinauto to find and minimize the window, ensuring we use
        the same window handle as all other power controller operations.

        Returns:
            True if window was minimized, False if window not found or error.
        """
        try:
            win = self._find_window(use_cache=False)
            hwnd = win.handle

            # Check if already minimized
            is_iconic = bool(ctypes.windll.user32.IsIconic(hwnd))
            if is_iconic:
                self.logger.debug(f"Window already minimized (Handle={hwnd})")
                return True

            # Use pywinauto's minimize
            win.minimize()

            # Verify it worked
            time.sleep(0.2)
            is_iconic = bool(ctypes.windll.user32.IsIconic(hwnd))
            self.logger.info(f"Minimize GLM window: Handle={hwnd} IsIconic={is_iconic}")

            return is_iconic
        except GlmWindowNotFoundError:
            self.logger.warning("Cannot minimize: GLM window not found")
            return False
        except Exception as e:
            self.logger.warning(f"Failed to minimize GLM window: {e}")
            return False

    def is_available(self) -> bool:
        """
        Check if GLM window is available for power control.

        Returns True if GLM window found, False otherwise.
        Does not raise exceptions.
        """
        try:
            self._find_window(use_cache=False)
            return True
        except GlmWindowNotFoundError:
            return False


# Convenience function for simple usage
def get_power_state() -> PowerState:
    """Quick helper to read current power state."""
    return GlmPowerController().get_state()


def set_power_state(desired: Literal["on", "off"]) -> bool:
    """Quick helper to set power state."""
    return GlmPowerController().set_state(desired)
