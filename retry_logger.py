"""
Smart Retry Logger - Absolute milestone logging with exponential backoff.

Manages smart logging during retry loops using absolute time milestones.
Retries continue at their normal frequency, but log messages are throttled
based on elapsed time since the first failure.
"""

import threading
import time
from typing import Dict, List, Optional

# Smart retry logging intervals (absolute milestones from first event)
# Format: list of seconds. If value > prev_log_time, it's an absolute milestone.
#         Otherwise, it's added to prev_log_time. Last value repeats indefinitely.
# Example: [2, 10, 60, 600, 3600, 86400] logs at t=2s, 10s, 60s, 10min, 1hr, 1day from start
# Example: [2, 2, 2, 10, 10, 60] logs at t=2, 4, 6, 10, 20, 60, 120, 180... from start
RETRY_LOG_INTERVALS = [2, 10, 60, 600, 3600, 86400]  # 2s, 10s, 1min, 10min, 1hr, 1day


class SmartRetryLogger:
    """
    Manages smart logging during retry loops using absolute time milestones.

    Retries continue at their normal frequency (RETRY_DELAY), but log messages
    are throttled based on elapsed time since the first failure.

    Interval values work as follows:
    - If interval > previous_log_time: use as absolute milestone from first event
    - Otherwise: add interval to previous_log_time (cumulative)
    - Last interval repeats indefinitely

    Example: [2, 10, 60, 600, 3600, 86400] -> logs at 2s, 10s, 1min, 10min, 1hr, 1day
    Example: [2, 2, 2, 10, 10] -> logs at 2s, 4s, 6s, 10s, 20s, 30s...
    """

    def __init__(self, intervals: Optional[List[float]] = None):
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
