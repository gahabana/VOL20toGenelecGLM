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
    from PIL import ImageGrab
    from pywinauto import Desktop
    HAS_WIN32_DEPS = True
except ImportError:
    HAS_WIN32_DEPS = False


# Type alias for power state
PowerState = Literal["on", "off", "unknown"]


@dataclass(frozen=True)
class Point:
    """Screen coordinate."""
    x: int
    y: int


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
    ):
        """
        Initialize the power controller.

        Args:
            config: Configuration for button detection. Uses defaults if None.
            logger: Logger instance. Creates a default if None.
            steal_focus: If True, will focus GLM window before operations.
                        If False, operations may fail if window is not visible.
        """
        if not HAS_WIN32_DEPS:
            raise ImportError(
                "GlmPowerController requires Windows with pywinauto, pillow, and pywin32. "
                "Install with: pip install pywinauto pillow pywin32"
            )

        self.config = config or GlmPowerConfig()
        self.logger = logger or logging.getLogger(__name__)
        self.steal_focus = steal_focus
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

        try:
            win.restore()
        except Exception:
            pass
        win.set_focus()
        time.sleep(self.config.focus_delay)

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

    def get_state(self) -> PowerState:
        """
        Read current power state from GLM UI.

        Returns:
            "on", "off", or "unknown"

        Raises:
            GlmWindowNotFoundError: If GLM window not found.
        """
        with self._lock:
            win = self._find_window()
            if self.steal_focus:
                self._ensure_foreground(win)

            state, rgb, pt = self._read_state_internal(win)
            self.logger.debug(f"Power state: {state} (rgb={rgb}, pt=({pt.x},{pt.y}))")

            if state != "unknown":
                self._last_known_state = state

            return state

    def get_state_with_details(self) -> Tuple[PowerState, Tuple[int, int, int], Tuple[int, int]]:
        """
        Read current power state with diagnostic details.

        Returns:
            Tuple of (state, rgb, (x, y))

        Raises:
            GlmWindowNotFoundError: If GLM window not found.
        """
        with self._lock:
            win = self._find_window()
            if self.steal_focus:
                self._ensure_foreground(win)

            state, rgb, pt = self._read_state_internal(win)

            if state != "unknown":
                self._last_known_state = state

            return state, rgb, (pt.x, pt.y)

    def set_state(
        self,
        desired: Literal["on", "off"],
        verify: bool = True,
        retries: int = 2,
    ) -> bool:
        """
        Set power to desired state. Only clicks if state differs.

        Args:
            desired: Target state ("on" or "off").
            verify: If True, poll to verify state changed.
            retries: Number of click retries if verification fails.

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

    def toggle(self, verify: bool = True) -> PowerState:
        """
        Toggle power state.

        Returns the new state after toggling.

        Raises:
            GlmStateUnknownError: If current state cannot be determined.
        """
        current = self.get_state()
        if current == "unknown":
            raise GlmStateUnknownError("Cannot toggle: current state unknown")

        new_state = "off" if current == "on" else "on"
        self.set_state(new_state, verify=verify)
        return new_state

    @property
    def last_known_state(self) -> PowerState:
        """
        Return the last successfully read power state.

        This does not perform a new read - use get_state() for that.
        """
        with self._lock:
            return self._last_known_state

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
