"""
PowerOnOff - GLM Power control via UI automation.

This module provides deterministic power state reading and setting for
Genelec GLM by sampling the power button's visual state and synthesizing
mouse clicks.

Windows only. Requires: pip install pywinauto pillow pywin32

Usage:
    from PowerOnOff import GlmPowerController, GlmPowerConfig

    # Basic usage
    controller = GlmPowerController()
    state = controller.get_state()  # "on", "off", or "unknown"
    controller.set_state("on")      # Ensure speakers are ON

    # With custom config (e.g., different button position)
    config = GlmPowerConfig(dx_from_right=30, dy_from_top=85)
    controller = GlmPowerController(config=config)

    # Non-intrusive mode (won't steal focus, may fail if window not visible)
    controller = GlmPowerController(steal_focus=False)
"""

from .exceptions import (
    GlmPowerError,
    GlmWindowNotFoundError,
    GlmStateUnknownError,
    GlmStateChangeFailedError,
)

# Conditional import - only available on Windows with dependencies
try:
    from .glm_power import (
        GlmPowerController,
        GlmPowerConfig,
        PowerState,
        get_power_state,
        set_power_state,
        get_display_diagnostics,
        is_console_session,
        get_current_session_id,
        reconnect_to_console,
    )
    POWER_CONTROL_AVAILABLE = True
except ImportError:
    POWER_CONTROL_AVAILABLE = False
    GlmPowerController = None
    GlmPowerConfig = None
    PowerState = None
    get_power_state = None
    set_power_state = None
    get_display_diagnostics = None
    is_console_session = None
    get_current_session_id = None
    reconnect_to_console = None


__all__ = [
    # Exceptions (always available)
    'GlmPowerError',
    'GlmWindowNotFoundError',
    'GlmStateUnknownError',
    'GlmStateChangeFailedError',
    # Controller (Windows only)
    'GlmPowerController',
    'GlmPowerConfig',
    'PowerState',
    'get_power_state',
    'set_power_state',
    'get_display_diagnostics',
    'is_console_session',
    'get_current_session_id',
    'reconnect_to_console',
    # Availability flag
    'POWER_CONTROL_AVAILABLE',
]
