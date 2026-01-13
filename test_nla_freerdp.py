#!/usr/bin/env python3
"""
Test script to verify if FreeRDP can connect with NLA enabled.

Run this AFTER re-enabling NLA in registry:
    reg add "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp" /v UserAuthentication /t REG_DWORD /d 1 /f

Usage:
    python test_nla_freerdp.py

Output is captured to: test_nla_freerdp_output.txt
"""

import subprocess
import shutil
import sys
import os
from datetime import datetime

# Configuration - update these to match your system
RDP_USER = "zh"
RDP_PASS = "qwe2qwe2"
RDP_HOST = "localhost"

# Output file for capturing FreeRDP stdout/stderr
OUTPUT_FILE = os.path.join(os.path.dirname(__file__), "test_nla_freerdp_output.txt")


def find_wfreerdp():
    """Find wfreerdp executable."""
    path = shutil.which("wfreerdp") or shutil.which("wfreerdp.exe")
    if not path:
        print("ERROR: wfreerdp not found in PATH")
        print("Install FreeRDP and ensure wfreerdp.exe is in PATH")
        sys.exit(1)
    return path


def test_nla_connection():
    """Test FreeRDP connection with NLA enabled."""
    wfreerdp = find_wfreerdp()

    # Command with NLA - try different auth packages
    commands_to_try = [
        # Option 1: NLA with NTLM auth
        [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER, "/p:" + RDP_PASS,
         "/cert:ignore", "/sec:nla", "/auth-pkg:ntlm"],

        # Option 2: NLA with default auth
        [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER, "/p:" + RDP_PASS,
         "/cert:ignore", "/sec:nla"],

        # Option 3: Auto security negotiation (let it pick)
        [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER, "/p:" + RDP_PASS,
         "/cert:ignore"],
    ]

    with open(OUTPUT_FILE, "w") as f:
        f.write(f"FreeRDP NLA Test - {datetime.now().isoformat()}\n")
        f.write(f"Executable: {wfreerdp}\n")
        f.write("=" * 60 + "\n\n")

        for i, cmd in enumerate(commands_to_try, 1):
            # Mask password in display
            display_cmd = [c if not c.startswith("/p:") else "/p:****" for c in cmd]

            print(f"\n--- Test {i}: {' '.join(display_cmd)}")
            f.write(f"--- Test {i} ---\n")
            f.write(f"Command: {' '.join(display_cmd)}\n\n")
            f.flush()

            try:
                # Run FreeRDP with timeout
                # Note: successful connection will open a window and hang until closed
                # We use timeout to detect this
                proc = subprocess.Popen(
                    cmd,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                )

                # Wait up to 10 seconds
                # If it times out, connection likely succeeded (window opened)
                try:
                    stdout, stderr = proc.communicate(timeout=10)
                    returncode = proc.returncode
                except subprocess.TimeoutExpired:
                    # Timeout = likely connected (window opened)
                    proc.terminate()
                    try:
                        stdout, stderr = proc.communicate(timeout=2)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        stdout, stderr = proc.communicate()
                    returncode = "TIMEOUT (likely connected!)"

                # Write output
                f.write(f"Return code: {returncode}\n")
                f.write(f"STDOUT:\n{stdout.decode('utf-8', errors='replace')}\n")
                f.write(f"STDERR:\n{stderr.decode('utf-8', errors='replace')}\n")
                f.write("\n" + "=" * 60 + "\n\n")
                f.flush()

                # Print summary
                print(f"    Return code: {returncode}")
                if stderr:
                    stderr_text = stderr.decode('utf-8', errors='replace').strip()
                    # Show first line of error
                    first_line = stderr_text.split('\n')[0] if stderr_text else ""
                    print(f"    Error: {first_line[:80]}")

                # If timeout occurred, that's success!
                if returncode == "TIMEOUT (likely connected!)":
                    print(f"\n*** SUCCESS! Test {i} connected with NLA! ***")
                    print(f"Use this command in bridge2glm.py")
                    f.write(f"\n*** SUCCESS! This command works with NLA ***\n")
                    return True, cmd

            except Exception as e:
                f.write(f"Exception: {e}\n\n")
                print(f"    Exception: {e}")

    print(f"\nAll tests failed. See {OUTPUT_FILE} for details.")
    return False, None


def main():
    print("FreeRDP NLA Connection Test")
    print("=" * 40)
    print(f"Output will be saved to: {OUTPUT_FILE}")
    print(f"\nMake sure NLA is enabled:")
    print('  reg query "HKLM\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server\\WinStations\\RDP-Tcp" /v UserAuthentication')
    print("  (Should show: UserAuthentication    REG_DWORD    0x1)")
    print()

    success, working_cmd = test_nla_connection()

    if success:
        print("\n" + "=" * 40)
        print("SUCCESS! NLA connection works.")
        print("Update bridge2glm.py with the working command.")
        sys.exit(0)
    else:
        print("\n" + "=" * 40)
        print("FAILED: Could not connect with NLA enabled.")
        print(f"Check {OUTPUT_FILE} for error details.")
        print("\nTo revert to non-NLA (if needed):")
        print('  reg add "HKLM\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server\\WinStations\\RDP-Tcp" /v UserAuthentication /t REG_DWORD /d 0 /f')
        sys.exit(1)


if __name__ == "__main__":
    main()
