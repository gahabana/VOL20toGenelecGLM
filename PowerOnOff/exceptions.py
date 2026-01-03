"""
Custom exceptions for GLM Power control.
"""


class GlmPowerError(Exception):
    """Base exception for GLM Power control errors."""
    pass


class GlmWindowNotFoundError(GlmPowerError):
    """Raised when the GLM window cannot be found."""
    pass


class GlmStateUnknownError(GlmPowerError):
    """Raised when the power state cannot be determined from pixel sampling."""

    def __init__(self, message: str, rgb: tuple = None, point: tuple = None):
        super().__init__(message)
        self.rgb = rgb
        self.point = point


class GlmStateChangeFailedError(GlmPowerError):
    """Raised when the power state change failed after retries."""

    def __init__(self, message: str, desired: str, actual: str = None):
        super().__init__(message)
        self.desired = desired
        self.actual = actual
