"""
Configuration and Argument Parsing for GLM Manager.

Handles CLI argument parsing and validation for the GLM Manager application.
"""

import argparse
import os
from typing import List, Tuple


def validate_volume_increases(value: str) -> List[int]:
    """
    Validate and parse volume increases list.

    Args:
        value: Comma-separated list of integers, optionally in brackets

    Returns:
        List of integers

    Raises:
        argparse.ArgumentTypeError: If validation fails
    """
    try:
        parsed = list(map(int, value.strip("[]").split(",")))
        if len(parsed) < 2 or len(parsed) > 15:
            raise argparse.ArgumentTypeError("Volume increase list must have between 2 and 15 items.")
        if not all(1 <= x <= 10 for x in parsed):
            raise argparse.ArgumentTypeError("All values in the list must be integers between 1 and 10.")
        return parsed
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"Invalid format for volume_increase_list: {e}")


def validate_click_times(values: str) -> Tuple[float, float]:
    """
    Validate and parse click time values.

    Args:
        values: Comma-separated pair of floats

    Returns:
        Tuple of (min_click_time, max_avg_click_time)

    Raises:
        argparse.ArgumentTypeError: If validation fails
    """
    try:
        # Split and parse the two values
        parsed = list(map(float, values.split(",")))
        if len(parsed) != 2:
            raise argparse.ArgumentTypeError("You must provide exactly two values: MIN_CLICK_TIME,MAX_AVG_CLICK_TIME.")

        min_click_time, max_avg_click_time = parsed

        # Validate the values
        if not (0.01 < min_click_time < 1):
            raise argparse.ArgumentTypeError("MIN_CLICK_TIME must be > 0.01 and < 1.")
        if max_avg_click_time > min_click_time:
            raise argparse.ArgumentTypeError("MAX_AVG_CLICK_TIME must be <= MIN_CLICK_TIME.")

        return min_click_time, max_avg_click_time
    except ValueError:
        raise argparse.ArgumentTypeError("Click times must be two float values separated by a comma.")


def validate_device(value: str) -> Tuple[int, int]:
    """
    Validate and parse VID/PID device identifier.

    Args:
        value: Comma-separated hex values (e.g., "0x07d7,0x0000")

    Returns:
        Tuple of (vid, pid)

    Raises:
        argparse.ArgumentTypeError: If validation fails
    """
    try:
        vid, pid = map(lambda x: int(x, 16), value.split(","))
        if vid < 0x0000 or vid > 0xFFFF or pid < 0x0000 or pid > 0xFFFF:
            raise argparse.ArgumentTypeError("VID and PID must be valid 16-bit hexadecimal values.")
        return vid, pid
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"Invalid VID/PID format: {e}")


def parse_arguments(script_file: str = None):
    """
    Parse command-line arguments.

    Args:
        script_file: Path to the main script file (for default log file name)

    Returns:
        Parsed arguments namespace
    """
    parser = argparse.ArgumentParser(description="GLM Manager - HID to MIDI Agent for Genelec GLM control.")

    # Determine default log file name
    if script_file:
        default_log_file = os.path.splitext(os.path.basename(script_file))[0] + ".log"
    else:
        default_log_file = "glm_manager.log"

    parser.add_argument("--log_level", choices=["DEBUG", "INFO", "NONE"], default="DEBUG",
                        help="Set logging level. Default is DEBUG.")

    parser.add_argument("--log_file_name", type=str, default=default_log_file,
                        help=f"Name of the log file. Default is '{default_log_file}'.")

    # Single argument for click times
    parser.add_argument("--click_times", type=validate_click_times, default=(0.2, 0.15),
                        help="Comma-separated values for MIN_CLICK_TIME and MAX_AVG_CLICK_TIME. "
                             "MIN_CLICK_TIME must be > 0.01 and < 1, and MAX_AVG_CLICK_TIME must be <= MIN_CLICK_TIME. "
                             "Default is '0.2,0.15'.")

    parser.add_argument("--volume_increases_list", type=validate_volume_increases, default=[1, 1, 2, 2, 3],
                        help="List of volume increases. Must be between 2 and 15 integers, each >=1 and <=10. Default is [1, 1, 2, 2, 3].")

    # VID/PID combination
    parser.add_argument("--device", type=validate_device, default=(0x07d7, 0x0000),
                        help="VID and PID of the device to be listened to, in the format 'VID,PID'. Default is '0x07d7,0x0000'.")

    # MIDI channel names
    parser.add_argument("--midi_in_channel", type=str, default="GLMMIDI 1",
                        help="MIDI input channel name (to send commands TO GLM). Default is 'GLMMIDI 1'.")

    parser.add_argument("--midi_out_channel", type=str, default="GLMOUT 1",
                        help="MIDI output channel name (to receive state FROM GLM). Default is 'GLMOUT 1'.")

    parser.add_argument("--startup_volume", type=int, default=None, choices=range(0, 128), metavar="0-127",
                        help="Optional startup volume (0-127). If set, GLM volume will be set to this value on startup. "
                             "79 corresponds to -46dB in GLM. If not set, script will query current volume.")

    # REST API
    parser.add_argument("--api_port", type=int, default=8080,
                        help="Port for REST API server. Set to 0 to disable API. Default is 8080.")

    # MQTT / Home Assistant
    parser.add_argument("--mqtt_broker", type=str, default=None,
                        help="MQTT broker hostname. If not set, MQTT is disabled.")
    parser.add_argument("--mqtt_port", type=int, default=1883,
                        help="MQTT broker port. Default is 1883.")
    parser.add_argument("--mqtt_user", type=str, default=None,
                        help="MQTT username (optional).")
    parser.add_argument("--mqtt_pass", type=str, default=None,
                        help="MQTT password (optional).")
    parser.add_argument("--mqtt_topic", type=str, default="glm",
                        help="MQTT topic prefix. Default is 'glm'.")
    parser.add_argument("--mqtt_ha_discovery", action="store_true", default=True,
                        help="Enable Home Assistant MQTT Discovery. Default is True.")
    parser.add_argument("--no_mqtt_ha_discovery", action="store_false", dest="mqtt_ha_discovery",
                        help="Disable Home Assistant MQTT Discovery.")

    # GLM Manager options
    parser.add_argument("--glm_manager", action="store_true", default=True,
                        help="Enable GLM process manager (start GLM, watchdog, auto-restart). Default is True.")
    parser.add_argument("--no_glm_manager", action="store_false", dest="glm_manager",
                        help="Disable GLM process manager (use external script or manual GLM start).")
    parser.add_argument("--glm_path", type=str, default=r"C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe",
                        help="Path to GLM executable.")
    parser.add_argument("--glm_cpu_gating", action="store_true", default=True,
                        help="Wait for CPU idle before starting GLM. Default is True.")
    parser.add_argument("--no_glm_cpu_gating", action="store_false", dest="glm_cpu_gating",
                        help="Disable CPU gating for GLM startup.")

    # Parse arguments
    args = parser.parse_args()

    # Assign parsed click times to individual variables for clarity
    args.min_click_time, args.max_avg_click_time = args.click_times
    args.vid, args.pid = args.device
    return args
