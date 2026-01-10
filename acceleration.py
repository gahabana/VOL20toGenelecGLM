"""
Volume Acceleration Handler.

Provides acceleration-based volume control that increases the volume
change rate based on how fast the user is rotating the knob.
"""

from typing import List


class AccelerationHandler:
    """
    Handles volume acceleration based on click speed.

    When the user rotates the knob quickly, the volume changes faster.
    The acceleration curve is configurable via volume_increases_list.
    """

    def __init__(self, min_click: float, max_per_click_avg: float, volume_list: List[int]):
        """
        Initialize the acceleration handler.

        Args:
            min_click: Minimum time between clicks to consider them separate (seconds)
            max_per_click_avg: Maximum average time per click for acceleration (seconds)
            volume_list: List of volume increments for each acceleration level
        """
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

    def calculate_speed(self, current_time: float, button: int) -> int:
        """
        Calculate volume change based on click speed.

        Args:
            current_time: Current timestamp
            button: Button/direction identifier (to detect direction changes)

        Returns:
            Volume delta (how much to change volume by)
        """
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
