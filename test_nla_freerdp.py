#!/usr/bin/env python3
r"""
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


def run_test(f, test_num, cmd, description, stdin_data=None):
    """Run a single test and return (success, returncode)."""
    # Mask password in display
    display_cmd = []
    for c in cmd:
        if c.startswith("/p:"):
            display_cmd.append("/p:****")
        elif c == RDP_PASS:
            display_cmd.append("****")
        else:
            display_cmd.append(c)

    print(f"\n--- Test {test_num}: {description}")
    print(f"    Command: {' '.join(display_cmd)}")
    f.write(f"--- Test {test_num}: {description} ---\n")
    f.write(f"Command: {' '.join(display_cmd)}\n")
    if stdin_data:
        f.write(f"Stdin: {'****' if stdin_data == RDP_PASS else stdin_data}\n")
    f.write("\n")
    f.flush()

    try:
        # Run FreeRDP with timeout
        proc = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            stdin=subprocess.PIPE if stdin_data else None,
        )

        # Wait up to 10 seconds
        # If it times out, connection likely succeeded (window opened)
        try:
            stdin_bytes = stdin_data.encode() if stdin_data else None
            stdout, stderr = proc.communicate(input=stdin_bytes, timeout=10)
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
            # Look for key errors
            for line in stderr_text.split('\n'):
                if '[ERROR]' in line:
                    # Extract just the error message
                    print(f"    {line[line.find('[ERROR]'):][:80]}")
                    break

        # If timeout occurred, that's success!
        if returncode == "TIMEOUT (likely connected!)":
            print(f"\n*** SUCCESS! Test {test_num} connected with NLA! ***")
            f.write(f"\n*** SUCCESS! This command works with NLA ***\n")
            return True

        return False

    except Exception as e:
        f.write(f"Exception: {e}\n\n")
        print(f"    Exception: {e}")
        return False


def test_nla_connection():
    """Test FreeRDP connection with NLA enabled."""
    wfreerdp = find_wfreerdp()

    with open(OUTPUT_FILE, "w") as f:
        f.write(f"FreeRDP NLA Test - {datetime.now().isoformat()}\n")
        f.write(f"Executable: {wfreerdp}\n")
        f.write("=" * 60 + "\n")
        f.write("\nNOTE: If you see 'OpenSSL LEGACY provider failed to load, no md4'\n")
        f.write("this means NTLM auth may not work (MD4 required for NTLM hashing).\n")
        f.write("=" * 60 + "\n\n")

        tests = [
            # Test 1: Correct auth-pkg-list syntax (not /auth-pkg)
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore", "/sec:nla", "/auth-pkg-list:ntlm"],
                "desc": "NLA with /auth-pkg-list:ntlm (correct syntax)",
            },
            # Test 2: Explicit local domain with backslash-dot notation
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:.\\" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore", "/sec:nla"],
                "desc": "NLA with explicit local domain (.\\user)",
            },
            # Test 3: Explicit /d:. domain parameter
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/d:.", "/u:" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore", "/sec:nla"],
                "desc": "NLA with /d:. (explicit local domain)",
            },
            # Test 4: Try without explicitly forcing NLA (auto-negotiate)
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:.\\" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore"],
                "desc": "Auto-negotiate with local domain (.\\user)",
            },
            # Test 5: Credentials from stdin (bypasses SSPI credential handling?)
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER,
                        "/cert:ignore", "/sec:nla", "/from-stdin:force"],
                "desc": "NLA with /from-stdin:force",
                "stdin": RDP_PASS,
            },
            # Test 6: Try with credentials-delegation AND local domain
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:.\\" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore", "/sec:nla", "+credentials-delegation"],
                "desc": "NLA with +credentials-delegation and local domain",
            },
            # Test 7: Kerberos only (unlikely to work for local, but worth trying)
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore", "/sec:nla", "/auth-pkg-list:kerberos"],
                "desc": "NLA with /auth-pkg-list:kerberos",
            },
            # Test 8: Disable NTLM explicitly, see what happens
            {
                "cmd": [wfreerdp, "/v:" + RDP_HOST, "/u:" + RDP_USER, "/p:" + RDP_PASS,
                        "/cert:ignore", "/sec:nla", "/auth-pkg-list:!ntlm"],
                "desc": "NLA with /auth-pkg-list:!ntlm (disable NTLM)",
            },
        ]

        for i, test in enumerate(tests, 1):
            stdin_data = test.get("stdin")
            success = run_test(f, i, test["cmd"], test["desc"], stdin_data)
            if success:
                return True, test["cmd"]

    print(f"\nAll tests failed. See {OUTPUT_FILE} for details.")
    return False, None


def main():
    print("FreeRDP NLA Connection Test (Round 2)")
    print("=" * 40)
    print(f"Output will be saved to: {OUTPUT_FILE}")
    print(f"\nMake sure NLA is enabled:")
    print(r'  reg query "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp" /v UserAuthentication')
    print("  (Should show: UserAuthentication    REG_DWORD    0x1)")
    print()
    print("IMPORTANT: Watch for 'OpenSSL LEGACY provider failed to load' warning.")
    print("If MD4 is unavailable, NTLM authentication cannot work.")
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
        print("\nThe MD4/OpenSSL issue may be the root cause.")
        print("Consider keeping NLA disabled with firewall protection instead.")
        print("\nTo revert to non-NLA (if needed):")
        print(r'  reg add "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp" /v UserAuthentication /t REG_DWORD /d 0 /f')
        sys.exit(1)


if __name__ == "__main__":
    main()
