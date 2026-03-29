#!/usr/bin/env python3
"""
GLM MIDI Test Tool — Send MIDI CC messages to GLM for testing.

Usage examples:
    python glm_midi_test.py                     # Send CC28=127 (power toggle)
    python glm_midi_test.py --cc 23 --value 127 # Send CC23=127 (mute toggle)
    python glm_midi_test.py --cc 28 --value 0   # Send CC28=0 (GLM ignores this)
    python glm_midi_test.py --list               # List available MIDI ports
    python glm_midi_test.py --listen             # Listen on GLM output port
"""

import argparse
import sys
import time

try:
    from mido import Message, open_output, open_input, get_output_names, get_input_names
except ImportError:
    print("ERROR: mido not installed. Run: pip install mido python-rtmidi")
    sys.exit(1)

# GLM CC defaults (from midi_constants.py)
CC_NAMES = {
    20: "Volume (absolute)",
    21: "Volume Up",
    22: "Volume Down",
    23: "Mute",
    24: "Dim",
    25: "Preset Level 1",
    26: "Preset Level 2",
    27: "BM Bypass",
    28: "System Power",
    30: "Group Change",
    31: "Group 1", 32: "Group 2", 33: "Group 3", 34: "Group 4", 35: "Group 5",
    36: "Group 6", 37: "Group 7", 38: "Group 8", 39: "Group 9", 40: "Group 10",
    41: "Group Up",
    42: "Group Down",
    43: "Solo Device",
    44: "Mute Device",
}

DEFAULT_PORT = "GLMMIDI 1"
DEFAULT_LISTEN_PORT = "GLMOUT 1"
DEFAULT_CC = 28
DEFAULT_VALUE = 127
DEFAULT_CHANNEL = 0  # mido 0-indexed = MIDI channel 1


def list_ports():
    """List all available MIDI ports."""
    print("=== MIDI Output Ports (send TO GLM) ===")
    outputs = get_output_names()
    if outputs:
        for name in outputs:
            marker = " <-- default" if name == DEFAULT_PORT else ""
            print(f"  {name}{marker}")
    else:
        print("  (none found)")

    print()
    print("=== MIDI Input Ports (receive FROM GLM) ===")
    inputs = get_input_names()
    if inputs:
        for name in inputs:
            marker = " <-- default" if name == DEFAULT_LISTEN_PORT else ""
            print(f"  {name}{marker}")
    else:
        print("  (none found)")


def send_cc(port_name, channel, cc, value):
    """Send a single MIDI CC message."""
    cc_label = CC_NAMES.get(cc, f"CC{cc}")

    print(f"Opening port: {port_name}")
    try:
        port = open_output(port_name)
    except (OSError, IOError) as e:
        print(f"ERROR: Cannot open MIDI port '{port_name}': {e}")
        print(f"  Hint: Run with --list to see available ports")
        sys.exit(1)

    msg = Message('control_change', channel=channel, control=cc, value=value)
    print(f"Sending: CC{cc} ({cc_label}) = {value}  [channel {channel + 1}]")
    try:
        port.send(msg)
        print("OK — message sent")
    except (OSError, IOError) as e:
        print(f"ERROR: Failed to send: {e}")
        sys.exit(1)
    finally:
        port.close()


def listen(port_name, channel, duration):
    """Listen for MIDI messages from GLM and print them."""
    print(f"Opening input port: {port_name}")
    try:
        port = open_input(port_name)
    except (OSError, IOError) as e:
        print(f"ERROR: Cannot open MIDI port '{port_name}': {e}")
        print(f"  Hint: Run with --list to see available ports")
        sys.exit(1)

    print(f"Listening for {duration}s on channel {channel + 1} (Ctrl+C to stop)...")
    print()
    start = time.time()
    count = 0
    try:
        while time.time() - start < duration:
            msg = port.poll()
            if msg is not None:
                if msg.type == 'control_change':
                    cc_label = CC_NAMES.get(msg.control, f"CC{msg.control}")
                    elapsed = time.time() - start
                    print(f"  [{elapsed:6.3f}s] CC{msg.control:3d} ({cc_label:20s}) = {msg.value:3d}  ch={msg.channel + 1}")
                    count += 1
                else:
                    print(f"  {msg}")
                    count += 1
            else:
                time.sleep(0.001)  # 1ms poll interval
    except KeyboardInterrupt:
        print()

    print(f"\nReceived {count} messages in {time.time() - start:.1f}s")
    port.close()


def main():
    parser = argparse.ArgumentParser(
        description="GLM MIDI Test Tool — send CC messages to GLM for testing",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
examples:
  %(prog)s                          Send CC28=127 (power toggle) to GLMMIDI 1
  %(prog)s --cc 23                  Send CC23=127 (mute toggle)
  %(prog)s --cc 28 --value 0        Send CC28=0 (GLM should ignore this)
  %(prog)s --cc 20 --value 80       Send CC20=80 (set volume to ~63%%)
  %(prog)s --list                   List available MIDI ports
  %(prog)s --listen                 Listen on GLMOUT 1 for 30s
  %(prog)s --listen --duration 5    Listen for 5s then stop

note:
  GLM treats CC28 (power) as toggle: value > 0 toggles, value 0 is ignored.
  MIDI channel in GLM settings shows 1-16; mido uses 0-15 (channel 0 = GLM channel 1).
""")

    parser.add_argument("--list", action="store_true",
                        help="List available MIDI ports and exit")
    parser.add_argument("--listen", action="store_true",
                        help="Listen on GLM output port instead of sending")
    parser.add_argument("--duration", type=float, default=30.0,
                        help="Listen duration in seconds (default: 30)")
    parser.add_argument("--port", type=str, default=DEFAULT_PORT,
                        help=f"MIDI port name (default: '{DEFAULT_PORT}')")
    parser.add_argument("--listen-port", type=str, default=DEFAULT_LISTEN_PORT,
                        help=f"MIDI input port for --listen (default: '{DEFAULT_LISTEN_PORT}')")
    parser.add_argument("--channel", type=int, default=DEFAULT_CHANNEL,
                        help=f"MIDI channel, 0-indexed (default: {DEFAULT_CHANNEL} = channel 1)")
    parser.add_argument("--cc", type=int, default=DEFAULT_CC,
                        help=f"CC number to send (default: {DEFAULT_CC} = {CC_NAMES.get(DEFAULT_CC, '?')})")
    parser.add_argument("--value", type=int, default=DEFAULT_VALUE,
                        help=f"CC value to send, 0-127 (default: {DEFAULT_VALUE})")

    args = parser.parse_args()

    if args.list:
        list_ports()
        return

    if args.listen:
        listen(args.listen_port, args.channel, args.duration)
        return

    # Validate
    if not 0 <= args.cc <= 127:
        print(f"ERROR: CC number must be 0-127, got {args.cc}")
        sys.exit(1)
    if not 0 <= args.value <= 127:
        print(f"ERROR: Value must be 0-127, got {args.value}")
        sys.exit(1)
    if not 0 <= args.channel <= 15:
        print(f"ERROR: Channel must be 0-15, got {args.channel}")
        sys.exit(1)

    send_cc(args.port, args.channel, args.cc, args.value)


if __name__ == "__main__":
    main()
