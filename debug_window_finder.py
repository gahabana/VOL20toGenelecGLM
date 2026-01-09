#!/usr/bin/env python3
"""
Diagnostic script to compare two window-finding methods for GLM.

Thread 1: EnumWindows approach (like glm_manager.py)
Thread 2: pywinauto approach (like glm_power.py)

Both log their findings each second to help identify mismatches.
"""

import argparse
import ctypes
from ctypes import wintypes
import logging
import os
import subprocess
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

# pywinauto for method 2
try:
    from pywinauto import Desktop
    HAS_PYWINAUTO = True
except ImportError:
    HAS_PYWINAUTO = False
    print("WARNING: pywinauto not available, thread 2 will not run")

# Setup logging with thread-safe file handlers
def setup_logging(log_dir: Path):
    """Setup separate log files for each thread."""
    log_dir.mkdir(exist_ok=True)

    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    # Thread 1 logger (EnumWindows)
    logger1 = logging.getLogger("enumwindows")
    logger1.setLevel(logging.DEBUG)
    fh1 = logging.FileHandler(log_dir / f"enumwindows_{timestamp}.log")
    fh1.setFormatter(logging.Formatter('%(asctime)s.%(msecs)03d [%(levelname)s] %(message)s',
                                        datefmt='%H:%M:%S'))
    logger1.addHandler(fh1)

    # Thread 2 logger (pywinauto)
    logger2 = logging.getLogger("pywinauto")
    logger2.setLevel(logging.DEBUG)
    fh2 = logging.FileHandler(log_dir / f"pywinauto_{timestamp}.log")
    fh2.setFormatter(logging.Formatter('%(asctime)s.%(msecs)03d [%(levelname)s] %(message)s',
                                        datefmt='%H:%M:%S'))
    logger2.addHandler(fh2)

    # Combined logger for comparison (also to console)
    logger_combined = logging.getLogger("combined")
    logger_combined.setLevel(logging.DEBUG)
    fh_combined = logging.FileHandler(log_dir / f"combined_{timestamp}.log")
    fh_combined.setFormatter(logging.Formatter('%(asctime)s.%(msecs)03d [%(levelname)s] %(message)s',
                                                datefmt='%H:%M:%S'))
    logger_combined.addHandler(fh_combined)

    # Console handler for combined
    ch = logging.StreamHandler()
    ch.setFormatter(logging.Formatter('%(asctime)s [%(levelname)s] %(message)s',
                                       datefmt='%H:%M:%S'))
    logger_combined.addHandler(ch)

    return logger1, logger2, logger_combined, log_dir / f"combined_{timestamp}.log"


class WindowFinder:
    """Compare two window-finding methods."""

    def __init__(self, glm_path: str, wait_seconds: float, poll_interval: float):
        self.glm_path = glm_path
        self.wait_seconds = wait_seconds
        self.poll_interval = poll_interval
        self.glm_pid = None
        self.glm_process = None
        self.stop_event = threading.Event()

        # Results storage for comparison
        self.last_enumwindows_handle = None
        self.last_pywinauto_handle = None
        self.lock = threading.Lock()

    def start_glm(self) -> int:
        """Start GLM and return PID."""
        if not os.path.exists(self.glm_path):
            raise FileNotFoundError(f"GLM not found at: {self.glm_path}")

        self.glm_process = subprocess.Popen(
            [self.glm_path],
            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP
        )
        self.glm_pid = self.glm_process.pid
        return self.glm_pid

    def stop_glm(self):
        """Stop GLM process."""
        if self.glm_process:
            try:
                self.glm_process.terminate()
                self.glm_process.wait(timeout=5)
            except Exception as e:
                print(f"Error stopping GLM: {e}")

    # =========================================================================
    # Method 1: EnumWindows (like glm_manager.py)
    # =========================================================================
    def find_windows_enumwindows(self, pid: int) -> list:
        """
        Find windows using EnumWindows + PID filter.
        Returns list of (hwnd, title, is_visible) tuples.
        """
        results = []

        def enum_callback(hwnd, _):
            # Get the PID for this window
            window_pid = wintypes.DWORD()
            ctypes.windll.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(window_pid))

            if window_pid.value == pid:
                # Get window title
                length = ctypes.windll.user32.GetWindowTextLengthW(hwnd) + 1
                buffer = ctypes.create_unicode_buffer(length)
                ctypes.windll.user32.GetWindowTextW(hwnd, buffer, length)
                title = buffer.value

                # Check visibility
                is_visible = bool(ctypes.windll.user32.IsWindowVisible(hwnd))

                # Get class name
                class_buffer = ctypes.create_unicode_buffer(256)
                ctypes.windll.user32.GetClassNameW(hwnd, class_buffer, 256)
                class_name = class_buffer.value

                results.append({
                    'hwnd': hwnd,
                    'title': title,
                    'class': class_name,
                    'visible': is_visible
                })
            return True  # Continue enumeration

        WNDENUMPROC = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
        ctypes.windll.user32.EnumWindows(WNDENUMPROC(enum_callback), 0)

        return results

    def get_main_window_enumwindows(self, pid: int) -> int:
        """Get main window handle using EnumWindows (first visible window)."""
        windows = self.find_windows_enumwindows(pid)
        for w in windows:
            if w['visible']:
                return w['hwnd']
        return 0

    # =========================================================================
    # Method 2: pywinauto (like glm_power.py)
    # =========================================================================
    def find_windows_pywinauto(self, pid: int) -> list:
        """
        Find windows using pywinauto JUCE filter.
        Returns list of window info dicts.
        """
        if not HAS_PYWINAUTO:
            return []

        results = []
        try:
            # Find JUCE windows (like glm_power.py does)
            wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")

            for w in wins:
                try:
                    w_pid = w.process_id()
                    title = w.window_text() or ""
                    hwnd = w.handle
                    class_name = w.class_name()
                    is_visible = w.is_visible()

                    # Include all JUCE windows, mark which match PID
                    results.append({
                        'hwnd': hwnd,
                        'title': title,
                        'class': class_name,
                        'visible': is_visible,
                        'pid': w_pid,
                        'matches_pid': (w_pid == pid),
                        'has_glm': ('GLM' in title)
                    })
                except Exception as e:
                    results.append({'error': str(e)})
        except Exception as e:
            results.append({'error': str(e)})

        return results

    def get_main_window_pywinauto(self, pid: int) -> int:
        """Get main window handle using pywinauto (JUCE + GLM in title + PID)."""
        if not HAS_PYWINAUTO:
            return 0

        try:
            wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")
            candidates = [w for w in wins if "GLM" in (w.window_text() or "")]

            # Filter by PID
            for w in candidates:
                try:
                    if w.process_id() == pid:
                        return w.handle
                except Exception:
                    pass
        except Exception:
            pass

        return 0

    # =========================================================================
    # Thread functions
    # =========================================================================
    def thread_enumwindows(self, logger, logger_combined):
        """Thread 1: Poll using EnumWindows method."""
        logger.info(f"=== EnumWindows thread started (PID={self.glm_pid}) ===")

        iteration = 0
        while not self.stop_event.is_set():
            iteration += 1
            try:
                windows = self.find_windows_enumwindows(self.glm_pid)
                main_hwnd = self.get_main_window_enumwindows(self.glm_pid)

                # Log all windows found
                logger.info(f"--- Iteration {iteration} ---")
                logger.info(f"Total windows for PID {self.glm_pid}: {len(windows)}")
                for w in windows:
                    logger.info(f"  hwnd={w['hwnd']} visible={w['visible']} "
                               f"class='{w['class']}' title='{w['title']}'")
                logger.info(f"Selected main window: {main_hwnd}")

                # Store for comparison
                with self.lock:
                    old_handle = self.last_enumwindows_handle
                    self.last_enumwindows_handle = main_hwnd

                    # Log to combined if changed or mismatch
                    if main_hwnd != old_handle:
                        logger_combined.info(f"[ENUM] Main window changed: {old_handle} -> {main_hwnd}")

                    if self.last_pywinauto_handle is not None and main_hwnd != self.last_pywinauto_handle:
                        logger_combined.warning(
                            f"[MISMATCH] EnumWindows={main_hwnd} vs pywinauto={self.last_pywinauto_handle}"
                        )

            except Exception as e:
                logger.error(f"Error in iteration {iteration}: {e}")

            time.sleep(self.poll_interval)

        logger.info("=== EnumWindows thread stopped ===")

    def thread_pywinauto(self, logger, logger_combined):
        """Thread 2: Poll using pywinauto method."""
        if not HAS_PYWINAUTO:
            logger.error("pywinauto not available")
            return

        logger.info(f"=== pywinauto thread started (PID={self.glm_pid}) ===")

        iteration = 0
        while not self.stop_event.is_set():
            iteration += 1
            try:
                windows = self.find_windows_pywinauto(self.glm_pid)
                main_hwnd = self.get_main_window_pywinauto(self.glm_pid)

                # Log all JUCE windows found
                logger.info(f"--- Iteration {iteration} ---")
                logger.info(f"Total JUCE windows: {len(windows)}")
                for w in windows:
                    if 'error' in w:
                        logger.info(f"  Error: {w['error']}")
                    else:
                        logger.info(f"  hwnd={w['hwnd']} pid={w['pid']} visible={w['visible']} "
                                   f"matches_pid={w['matches_pid']} has_glm={w['has_glm']} "
                                   f"class='{w['class']}' title='{w['title']}'")
                logger.info(f"Selected main window: {main_hwnd}")

                # Store for comparison
                with self.lock:
                    old_handle = self.last_pywinauto_handle
                    self.last_pywinauto_handle = main_hwnd

                    # Log to combined if changed or mismatch
                    if main_hwnd != old_handle:
                        logger_combined.info(f"[PYWIN] Main window changed: {old_handle} -> {main_hwnd}")

                    if self.last_enumwindows_handle is not None and main_hwnd != self.last_enumwindows_handle:
                        logger_combined.warning(
                            f"[MISMATCH] pywinauto={main_hwnd} vs EnumWindows={self.last_enumwindows_handle}"
                        )

            except Exception as e:
                logger.error(f"Error in iteration {iteration}: {e}")

            # Slight offset so threads don't always poll at exact same time
            time.sleep(self.poll_interval)

        logger.info("=== pywinauto thread stopped ===")

    def run(self, log_dir: Path):
        """Main run loop."""
        logger1, logger2, logger_combined, combined_log_path = setup_logging(log_dir)

        logger_combined.info("=" * 60)
        logger_combined.info("Window Finder Diagnostic Tool")
        logger_combined.info("=" * 60)
        logger_combined.info(f"GLM path: {self.glm_path}")
        logger_combined.info(f"Wait before start: {self.wait_seconds}s")
        logger_combined.info(f"Poll interval: {self.poll_interval}s")
        logger_combined.info("")

        # Wait before starting GLM
        logger_combined.info(f"Waiting {self.wait_seconds}s before starting GLM...")
        time.sleep(self.wait_seconds)

        # Start GLM
        logger_combined.info("Starting GLM...")
        try:
            pid = self.start_glm()
            logger_combined.info(f"GLM started with PID={pid}")
        except Exception as e:
            logger_combined.error(f"Failed to start GLM: {e}")
            return

        # Wait a moment for GLM to initialize
        logger_combined.info("Waiting 5s for GLM to initialize...")
        time.sleep(5)

        # Start threads
        logger_combined.info("Starting polling threads...")
        logger_combined.info("Press Ctrl+C to stop")
        logger_combined.info("")

        t1 = threading.Thread(target=self.thread_enumwindows, args=(logger1, logger_combined),
                             name="EnumWindows", daemon=True)
        t2 = threading.Thread(target=self.thread_pywinauto, args=(logger2, logger_combined),
                             name="pywinauto", daemon=True)

        t1.start()
        # Small offset between threads
        time.sleep(0.1)
        t2.start()

        try:
            while True:
                time.sleep(0.5)
        except KeyboardInterrupt:
            logger_combined.info("")
            logger_combined.info("Ctrl+C received, stopping...")

        self.stop_event.set()
        t1.join(timeout=2)
        t2.join(timeout=2)

        # Summary
        logger_combined.info("")
        logger_combined.info("=" * 60)
        logger_combined.info("Final state:")
        logger_combined.info(f"  EnumWindows main handle: {self.last_enumwindows_handle}")
        logger_combined.info(f"  pywinauto main handle:   {self.last_pywinauto_handle}")
        if self.last_enumwindows_handle == self.last_pywinauto_handle:
            logger_combined.info("  Status: MATCH")
        else:
            logger_combined.warning("  Status: MISMATCH!")
        logger_combined.info("=" * 60)
        logger_combined.info(f"Log files saved to: {log_dir}")

        # Stop GLM
        logger_combined.info("Stopping GLM...")
        self.stop_glm()
        logger_combined.info("Done.")

        print(f"\nLog files saved to: {log_dir}")
        print(f"Combined log: {combined_log_path}")


def main():
    parser = argparse.ArgumentParser(
        description="Compare window-finding methods for GLM",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python debug_window_finder.py
  python debug_window_finder.py --wait 10 --interval 0.5
  python debug_window_finder.py --glm "C:\\Path\\To\\GLMv5.exe"
        """
    )
    parser.add_argument('--wait', '-w', type=float, default=5.0,
                       help='Seconds to wait before starting GLM (default: 5)')
    parser.add_argument('--interval', '-i', type=float, default=1.0,
                       help='Poll interval in seconds (default: 1)')
    parser.add_argument('--glm', '-g', type=str,
                       default=r"C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe",
                       help='Path to GLM executable')
    parser.add_argument('--log-dir', '-l', type=str, default='debug_logs',
                       help='Directory for log files (default: debug_logs)')

    args = parser.parse_args()

    if sys.platform != 'win32':
        print("This script only runs on Windows")
        sys.exit(1)

    finder = WindowFinder(
        glm_path=args.glm,
        wait_seconds=args.wait,
        poll_interval=args.interval
    )

    finder.run(Path(args.log_dir))


if __name__ == "__main__":
    main()
