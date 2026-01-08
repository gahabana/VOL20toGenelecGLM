"""
GLM Manager - Process management and watchdog for Genelec GLM application.

Replaces the functionality of minimize-glm.newer.ps1 PowerShell script:
- CPU gating before startup (wait for system idle)
- Start GLM with AboveNormal priority
- Window handle stabilization and non-blocking minimize
- Watchdog thread to detect hangs and restart

Requirements (Windows only):
    pip install psutil pywinauto

Example usage:
    from PowerOnOff.glm_manager import GlmManager

    def on_glm_restart():
        # Reinitialize power controller after GLM restart
        power_controller.reinitialize()

    manager = GlmManager(reinit_callback=on_glm_restart)
    manager.start()  # Starts GLM and watchdog

    # Later...
    manager.stop()   # Stops watchdog (doesn't kill GLM)
"""
from __future__ import annotations

import logging
import os
import subprocess
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

logger = logging.getLogger(__name__)

# Conditional imports for Windows-only functionality
try:
    import psutil
    import ctypes
    from ctypes import wintypes
    HAS_DEPS = True
except ImportError:
    HAS_DEPS = False
    psutil = None

# Win32 constants for non-blocking minimize
WM_SYSCOMMAND = 0x0112
SC_MINIMIZE = 0xF020
SW_MINIMIZE = 6  # ShowWindow command to minimize


@dataclass
class GlmManagerConfig:
    """Configuration for GLM Manager."""

    # GLM executable
    glm_path: str = r"C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe"
    process_name: str = "GLMv5"

    # CPU gating (only at initial start, not restarts)
    cpu_threshold: float = 2.0  # % CPU considered "idle enough"
    cpu_check_interval: float = 5.0  # seconds between checks
    cpu_max_checks: int = 60  # 60 * 5s = 5 minutes max wait
    cpu_gating_enabled: bool = True  # Set False to skip CPU check

    # Window stabilization and minimize
    post_start_sleep: float = 5.0  # seconds after start before minimize
    enforce_poll_interval: float = 1.0  # seconds between stabilization polls
    enforce_max_seconds: float = 60.0  # max time for stabilization
    stable_handle_count: int = 4  # handle must be same N times
    minimize_attempts_needed: int = 1  # minimize at least N times
    minimize_on_start: bool = True  # Minimize GLM window after startup

    # Watchdog
    watchdog_interval: float = 5.0  # seconds between checks
    max_non_responsive: int = 6  # checks before kill (6*5=30s)
    restart_delay: float = 5.0  # seconds to wait before restart

    # Log file (None = no separate log, use main logger)
    log_file: Optional[Path] = None


class GlmManager:
    """
    Manages GLM process lifecycle: start, minimize, watchdog, restart.

    Runs a background watchdog thread that monitors GLM health and
    automatically restarts if the process exits or becomes unresponsive.
    """

    def __init__(
        self,
        config: Optional[GlmManagerConfig] = None,
        reinit_callback: Optional[Callable[[int], None]] = None,
    ):
        """
        Initialize GLM Manager.

        Args:
            config: Configuration options (uses defaults if None)
            reinit_callback: Called after GLM restart with GLM's PID to reinitialize
                           dependent components (e.g., power controller window handles)
        """
        self.config = config or GlmManagerConfig()
        self.reinit_callback = reinit_callback

        self._process: Optional[psutil.Process] = None
        self._hwnd: int = 0  # Cached window handle
        self._running = False
        self._watchdog_thread: Optional[threading.Thread] = None
        self._non_responsive_count = 0
        self._cpu_gating_done = False  # Only gate CPU once at first start
        self._lock = threading.Lock()

        # Set up logging
        if self.config.log_file:
            self._setup_file_logging()

    def _setup_file_logging(self):
        """Set up file logging if configured."""
        file_handler = logging.FileHandler(
            self.config.log_file, encoding="utf-8"
        )
        file_handler.setFormatter(
            logging.Formatter("%(asctime)s\t%(message)s")
        )
        logger.addHandler(file_handler)

    def start(self, block_until_ready: bool = True) -> bool:
        """
        Start GLM and the watchdog thread.

        Args:
            block_until_ready: If True, wait for GLM to start and stabilize
                             before returning. If False, return immediately.

        Returns:
            True if GLM started successfully (or already running),
            False if startup failed.
        """
        if not HAS_DEPS:
            logger.error("GLM Manager requires psutil and ctypes (Windows only)")
            return False

        logger.info("========== GlmManager.start() BEGIN ==========")

        # CPU gating only on first start
        if self.config.cpu_gating_enabled and not self._cpu_gating_done:
            self._wait_for_cpu_calm()
            self._cpu_gating_done = True

        # Start GLM process
        success = self._start_glm()
        if not success:
            logger.error("Failed to start GLM")
            return False

        # Start watchdog thread
        self._running = True
        self._watchdog_thread = threading.Thread(
            target=self._watchdog_loop,
            name="GLMWatchdog",
            daemon=True,
        )
        self._watchdog_thread.start()
        logger.info("Watchdog thread started")

        logger.info("========== GlmManager.start() END ==========")
        return True

    def stop(self, kill_glm: bool = False):
        """
        Stop the watchdog thread.

        Args:
            kill_glm: If True, also kill the GLM process. If False, just
                     stop monitoring (GLM continues running).
        """
        logger.info("GlmManager stopping...")
        self._running = False

        if self._watchdog_thread and self._watchdog_thread.is_alive():
            self._watchdog_thread.join(timeout=10)

        if kill_glm:
            self._kill_glm()

        logger.info("GlmManager stopped")

    def is_alive(self) -> bool:
        """Check if GLM process is running."""
        # Use cached process first
        if self._process:
            try:
                if self._process.is_running():
                    return True
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                pass
            # Cached process is dead, clear caches
            self._process = None
            self._hwnd = 0

        # Fallback: search for process (only if cache miss)
        proc = self._find_glm_process()
        if proc:
            self._process = proc
            return True
        return False

    def is_responding(self) -> bool:
        """
        Check if GLM GUI is responding (not hung).

        Uses Windows' IsHungAppWindow API for accurate detection.
        Uses cached window handle to avoid expensive EnumWindows call.
        """
        if not HAS_DEPS:
            return False

        # Use cached process - don't re-search
        if not self._process:
            return False

        try:
            # Use cached window handle if valid
            hwnd = self._hwnd
            if hwnd and ctypes.windll.user32.IsWindow(hwnd):
                # Cached handle is still valid
                pass
            else:
                # Need to find window handle (cache miss or invalid)
                hwnd = self._get_main_window_handle(self._process.pid)
                self._hwnd = hwnd

            if hwnd == 0:
                # No window yet, consider it responding
                return True

            # Check if window is hung
            is_hung = ctypes.windll.user32.IsHungAppWindow(hwnd)
            return not is_hung
        except Exception as e:
            logger.debug(f"is_responding check failed: {e}")
            return True  # Assume responding if check fails

    def _find_glm_process(self) -> Optional[psutil.Process]:
        """Find running GLM process by name."""
        if not HAS_DEPS:
            return None

        for proc in psutil.process_iter(["name"]):
            try:
                if proc.info["name"] == f"{self.config.process_name}.exe":
                    return proc
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                continue
        return None

    def _wait_for_cpu_calm(self) -> bool:
        """
        Wait for CPU to drop below threshold before starting GLM.

        This prevents starting GLM during Windows boot when CPU is busy.

        Returns:
            True when CPU is calm or timeout reached, False on error.
        """
        logger.info(
            f"CPU pre-launch check: threshold={self.config.cpu_threshold}%, "
            f"interval={self.config.cpu_check_interval}s, "
            f"maxChecks={self.config.cpu_max_checks}"
        )

        for check in range(1, self.config.cpu_max_checks + 1):
            try:
                # psutil.cpu_percent with interval gives accurate reading
                cpu = psutil.cpu_percent(interval=1)

                if cpu < self.config.cpu_threshold:
                    logger.info(
                        f"CPU {cpu:.1f}% < threshold {self.config.cpu_threshold}%. Proceeding."
                    )
                    return True

                logger.info(
                    f"CPU {cpu:.1f}% >= threshold {self.config.cpu_threshold}%. "
                    f"Waiting {self.config.cpu_check_interval}s... "
                    f"(check {check}/{self.config.cpu_max_checks})"
                )

            except Exception as e:
                logger.warning(f"CPU check failed: {e}. Proceeding without gating.")
                return True

            time.sleep(self.config.cpu_check_interval - 1)  # -1 for cpu_percent interval

        logger.warning("CPU did not drop below threshold in allotted time; proceeding anyway.")
        return True

    def _start_glm(self) -> bool:
        """
        Start GLM process with priority boost and window minimization.

        Returns:
            True if GLM is running after this call, False on failure.
        """
        logger.info("=== _start_glm BEGIN ===")

        # Check if executable exists
        if not os.path.isfile(self.config.glm_path):
            logger.error(f"GLM executable not found at '{self.config.glm_path}'")
            return False

        # Check if already running
        proc = self._find_glm_process()
        if proc is not None:
            logger.info(f"Existing {self.config.process_name} detected. PID={proc.pid}. Reusing.")
            self._process = proc
        else:
            # Start new process
            try:
                logger.info(f"Starting {self.config.process_name} from '{self.config.glm_path}'...")
                popen = subprocess.Popen(
                    [self.config.glm_path],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
                self._process = psutil.Process(popen.pid)
                logger.info(f"{self.config.process_name} started. PID={popen.pid}")
            except Exception as e:
                logger.error(f"Failed to start {self.config.process_name}: {e}")
                return False

        # Post-start delay
        logger.info(f"Sleeping {self.config.post_start_sleep}s after start before priority.")
        time.sleep(self.config.post_start_sleep)

        # Set priority to AboveNormal
        try:
            if self._process.is_running():
                self._process.nice(psutil.ABOVE_NORMAL_PRIORITY_CLASS)
                logger.info(f"Priority set to AboveNormal for PID {self._process.pid}")
        except Exception as e:
            logger.warning(f"Failed to set priority: {e}")

        # Wait for window to stabilize (always needed for watchdog to work)
        hwnd = self._wait_for_window_stable()
        self._hwnd = hwnd  # Cache for watchdog

        # NOTE: Don't minimize here - let caller do it after reinit_callback
        # This ensures power controller can find the window before it's minimized

        # Check if still alive
        if self._process and self._process.is_running():
            logger.info(f"=== _start_glm END (success: PID={self._process.pid}) ===")
            return True

        logger.error("=== _start_glm END (failure: process not alive) ===")
        return False

    def minimize(self):
        """
        Minimize the GLM window.

        Call this after reinit_callback has completed to ensure
        power controller can find the window first.
        """
        if self.config.minimize_on_start and self._hwnd:
            self._minimize_window(self._hwnd)

    def _get_main_window_handle(self, pid: int) -> int:
        """
        Get the main window handle for a process.

        Args:
            pid: Process ID

        Returns:
            Window handle (HWND) or 0 if not found
        """
        if not HAS_DEPS:
            return 0

        result = [0]

        def enum_callback(hwnd, _):
            # Get the PID for this window
            window_pid = wintypes.DWORD()
            ctypes.windll.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(window_pid))

            if window_pid.value == pid:
                # Check if this is a main window (visible, has title)
                if ctypes.windll.user32.IsWindowVisible(hwnd):
                    result[0] = hwnd
                    return False  # Stop enumeration
            return True  # Continue

        WNDENUMPROC = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
        ctypes.windll.user32.EnumWindows(WNDENUMPROC(enum_callback), 0)

        return result[0]

    def _post_minimize(self, hwnd: int) -> bool:
        """
        Minimize window using ShowWindow (more reliable than PostMessage for JUCE apps).

        Args:
            hwnd: Window handle

        Returns:
            True if minimize succeeded, False on error
        """
        if hwnd == 0:
            return False

        try:
            # Check if window is valid
            if not ctypes.windll.user32.IsWindow(hwnd):
                return False

            # Use ShowWindow which is more reliable for JUCE-based applications
            # SW_MINIMIZE (6) minimizes the window
            result = ctypes.windll.user32.ShowWindow(hwnd, SW_MINIMIZE)
            # ShowWindow returns previous visibility state, not success/failure
            # Check if actually minimized
            is_iconic = bool(ctypes.windll.user32.IsIconic(hwnd))
            if is_iconic:
                return True

            # Fallback: try PostMessage approach
            logger.debug(f"ShowWindow didn't minimize, trying PostMessage")
            result = ctypes.windll.user32.PostMessageW(
                hwnd, WM_SYSCOMMAND, SC_MINIMIZE, 0
            )
            return bool(result)
        except Exception as e:
            logger.debug(f"Minimize failed: {e}")
            return False

    def _wait_for_window_stable(self) -> int:
        """
        Wait for GLM main window handle to stabilize.

        GLM's window handle can change during startup (splash screen → main window).
        This method polls until the handle is stable, confirming GLM has fully started.

        Returns:
            The stable window handle, or 0 if stabilization failed/timed out.
        """
        if not self._process:
            return 0

        deadline = time.time() + self.config.enforce_max_seconds
        last_handle = 0
        stable_count = 0

        logger.info(
            f"Waiting for GLM window to stabilize: poll every {self.config.enforce_poll_interval}s, "
            f"for up to {self.config.enforce_max_seconds}s."
        )

        while time.time() < deadline:
            try:
                if not self._process.is_running():
                    logger.warning(f"{self.config.process_name} exited during stabilization.")
                    return 0

                hwnd = self._get_main_window_handle(self._process.pid)

                if hwnd == 0:
                    logger.debug(f"Current main window: PID={self._process.pid} Handle=0")
                    last_handle = 0
                    stable_count = 0
                    time.sleep(self.config.enforce_poll_interval)
                    continue

                logger.debug(f"Current main window: PID={self._process.pid} Handle={hwnd}")

                # Track handle stability
                if hwnd == last_handle:
                    stable_count += 1
                else:
                    last_handle = hwnd
                    stable_count = 1
                    logger.debug(f"New window handle detected. Resetting counters. Handle={hwnd}")

                logger.debug(f"StableCount={stable_count} Handle={hwnd}")

                # Check if stable enough
                if stable_count >= self.config.stable_handle_count:
                    logger.info(
                        f"Window handle {hwnd} is stable (StableCount={stable_count})."
                    )
                    return hwnd

            except Exception as e:
                logger.warning(f"Error in stabilization loop: {e}")

            time.sleep(self.config.enforce_poll_interval)

        logger.warning("Window stabilization timed out.")
        return last_handle  # Return whatever we have

    def _minimize_window(self, hwnd: int):
        """
        Minimize the GLM window using non-blocking PostMessage.

        Args:
            hwnd: Window handle to minimize
        """
        if hwnd == 0:
            return

        logger.info(f"Minimizing window Handle={hwnd}")
        for attempt in range(self.config.minimize_attempts_needed):
            ok = self._post_minimize(hwnd)
            logger.debug(f"Minimize posted (non-blocking). ok={ok} Handle={hwnd} attempt={attempt + 1}")
            if attempt < self.config.minimize_attempts_needed - 1:
                time.sleep(self.config.enforce_poll_interval)

        # Give the window a moment to process the minimize message
        time.sleep(0.2)

        # Verify minimize actually happened
        try:
            is_iconic = bool(ctypes.windll.user32.IsIconic(hwnd))
            is_visible = bool(ctypes.windll.user32.IsWindowVisible(hwnd))
            logger.info(f"After minimize: Handle={hwnd} IsIconic={is_iconic} IsVisible={is_visible}")
            if not is_iconic:
                logger.warning(f"Window Handle={hwnd} did NOT minimize (IsIconic=False)")
        except Exception as e:
            logger.debug(f"Could not verify minimize state: {e}")

    def _kill_glm(self):
        """Kill the GLM process."""
        proc = self._find_glm_process()
        if proc is None:
            return

        try:
            logger.info(f"Killing GLM PID={proc.pid}...")
            proc.kill()
            proc.wait(timeout=10)
            logger.info(f"GLM PID={proc.pid} killed.")
        except Exception as e:
            logger.error(f"Error killing GLM: {e}")

    def _watchdog_loop(self):
        """
        Watchdog thread main loop.

        Monitors GLM health every watchdog_interval seconds.
        Restarts GLM if it exits or becomes unresponsive.
        """
        logger.info(f"Watchdog loop started for PID {self._process.pid if self._process else 'unknown'}")
        self._non_responsive_count = 0

        while self._running:
            try:
                # Check if process is alive
                if not self.is_alive():
                    logger.warning("Watchdog: GLM process not found. Restarting.")
                    self._restart_glm()
                    continue

                # Check if responding
                if not self.is_responding():
                    self._non_responsive_count += 1
                    logger.warning(
                        f"Watchdog: GLM NOT responding. "
                        f"Streak={self._non_responsive_count}/{self.config.max_non_responsive}."
                    )

                    if self._non_responsive_count >= self.config.max_non_responsive:
                        hung_time = self.config.watchdog_interval * self.config.max_non_responsive
                        logger.error(f"Watchdog: GLM hung for ~{hung_time}s. Killing and restarting.")
                        self._kill_glm()
                        time.sleep(self.config.restart_delay)
                        self._restart_glm()
                        continue
                else:
                    if self._non_responsive_count > 0:
                        logger.info(
                            f"Watchdog: GLM responsive again. "
                            f"Resetting non-responsive streak (was {self._non_responsive_count})."
                        )
                    self._non_responsive_count = 0

            except Exception as e:
                logger.error(f"Watchdog loop error: {e}")

            time.sleep(self.config.watchdog_interval)

        logger.info("Watchdog loop ended.")

    def _restart_glm(self):
        """Restart GLM and call reinit callback."""
        logger.info("=== Restarting GLM ===")

        # Start GLM (skip CPU gating on restart)
        success = self._start_glm()

        if success and self.reinit_callback and self._process:
            logger.info(f"Calling reinit callback after GLM restart (PID={self._process.pid})...")
            try:
                self.reinit_callback(self._process.pid)
            except Exception as e:
                logger.error(f"Reinit callback failed: {e}")
            # Minimize after callback (so power controller finds window first)
            self.minimize()

        self._non_responsive_count = 0

    @property
    def pid(self) -> Optional[int]:
        """Return the GLM process ID, or None if not running."""
        if self._process and self._process.is_running():
            return self._process.pid
        return None

    def reinitialize(self):
        """
        Reinitialize internal state after external GLM restart.

        Call this if GLM was restarted externally and you need the
        manager to pick up the new process.
        """
        self._process = self._find_glm_process()
        self._non_responsive_count = 0
        if self._process:
            logger.info(f"Reinitialized with GLM PID={self._process.pid}")
        else:
            logger.warning("Reinitialized but GLM process not found")
