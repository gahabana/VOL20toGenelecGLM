# glmpower.py
# Robust Power (⏻) automation for Genelec GLM 5.x (JUCE/OpenGL UI)
#
# Requirements:
#   pip install pywinauto pillow pywin32
#
# Notes:
# - GLM power button is not exposed via UI Automation; we use pixel sampling + click.
# - We use a median color of a small patch to avoid the white ⏻ glyph and anti-aliasing.
# - We treat "ON" as green/teal background, "OFF" as dark grey background.
#
# Usage:
#   python glmpower.py
# (Runs a simple ON then OFF test sequence.)

from __future__ import annotations

import time
from dataclasses import dataclass
from statistics import median

import win32api
import win32con
from PIL import ImageGrab
from pywinauto import Desktop


# ---- Configuration (calibrated offsets from your measurements) ----
DX_FROM_RIGHT = 28
DY_FROM_TOP = 80

# Sampling / robustness
PATCH_RADIUS = 4          # 9x9 patch median
FALLBACK_NUDGE_X = 8      # if state unknown, sample a second point nudged left

# Timing
FOCUS_DELAY_SEC = 0.15
POST_CLICK_DELAY_SEC = 0.35


@dataclass(frozen=True)
class Point:
    x: int
    y: int


def find_glm_window():
    """
    Find the top-level GLM window reliably.
    GLM is a JUCE app; class names are JUCE_*.
    We filter JUCE windows by caption containing 'GLM'.
    """
    wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")
    candidates = [w for w in wins if "GLM" in (w.window_text() or "")]
    if not candidates:
        raise RuntimeError("GLM window not found. Is GLM running and visible?")

    # You had an extra 'JUCEWindow' in the win32 list; filtering by 'GLM' avoids it.
    return candidates[0]


def ensure_foreground(win) -> None:
    """Restore (if minimized) and focus the GLM window."""
    try:
        win.restore()
    except Exception:
        pass
    win.set_focus()
    time.sleep(FOCUS_DELAY_SEC)


def get_power_point(win, dx: int = DX_FROM_RIGHT, dy: int = DY_FROM_TOP) -> Point:
    """
    Compute the screen coordinate of the power button sampling/click point,
    as a fixed offset from the top-right corner of the GLM window rectangle.
    """
    r = win.rectangle()
    return Point(r.right - dx, r.top + dy)


def get_patch_median_rgb(center: Point, radius: int = PATCH_RADIUS) -> tuple[int, int, int]:
    """
    Sample a (2*radius+1)x(2*radius+1) patch around center and return per-channel medians.
    Median is robust to the white ⏻ glyph, anti-aliased edges, and gradients.
    """
    left = center.x - radius
    top = center.y - radius
    right = center.x + radius + 1
    bottom = center.y + radius + 1

    img = ImageGrab.grab(bbox=(left, top, right, bottom), all_screens=True)
    pixels = list(img.getdata())

    rs = [p[0] for p in pixels]
    gs = [p[1] for p in pixels]
    bs = [p[2] for p in pixels]

    return (int(median(rs)), int(median(gs)), int(median(bs)))


def classify_power_state(rgb: tuple[int, int, int]) -> str:
    """
    Return 'on', 'off', or 'unknown'.

    OFF: dark grey circular background.
    ON: green/teal circular background.

    This is intentionally conservative: if unsure, return 'unknown' and refuse to click blindly.
    """
    r, g, b = rgb

    # OFF: dark/near-grey (low brightness, channels close)
    if max(r, g, b) <= 95 and abs(r - g) <= 22 and abs(g - b) <= 22:
        return "off"

    # ON: green/teal family.
    # Key: green channel elevated and materially above red.
    if g >= 110 and (g - r) >= 35:
        return "on"

    return "unknown"


def click_point(pt: Point) -> None:
    """Synthesize a left mouse click at pt."""
    win32api.SetCursorPos((pt.x, pt.y))
    time.sleep(0.02)
    win32api.mouse_event(win32con.MOUSEEVENTF_LEFTDOWN, 0, 0, 0, 0)
    time.sleep(0.02)
    win32api.mouse_event(win32con.MOUSEEVENTF_LEFTUP, 0, 0, 0, 0)


def read_power_state(win) -> tuple[str, tuple[int, int, int], Point]:
    """
    Read current power state by sampling the power button background color.

    If the primary point is 'unknown', we sample a second point nudged left (into the fill)
    to avoid landing on the white glyph or a highlight ring.
    """
    pt = get_power_point(win)
    rgb = get_patch_median_rgb(pt)
    state = classify_power_state(rgb)

    if state == "unknown" and FALLBACK_NUDGE_X:
        pt2 = Point(pt.x - FALLBACK_NUDGE_X, pt.y)
        rgb2 = get_patch_median_rgb(pt2)
        state2 = classify_power_state(rgb2)
        if state2 != "unknown":
            return state2, rgb2, pt2

    return state, rgb, pt

def wait_for_state(win, desired: str, timeout_sec: float = 3.0, poll_interval_sec: float = 0.15):
    """
    Poll power state until it matches desired or timeout expires.
    Returns (state, rgb, pt) from the last read.
    """
    deadline = time.time() + timeout_sec
    last = ("unknown", (0, 0, 0), Point(0, 0))

    while time.time() < deadline:
        last = read_power_state(win)
        state, rgb, pt = last
        if state == desired:
            return last
        time.sleep(poll_interval_sec)

    return last

def set_power_state(desired: str, retries: int = 2) -> None:
    """
    Ensure power state is 'on' or 'off'. Click only if needed.
    Uses polling verification because GLM updates the UI asynchronously.
    """
    if desired not in ("on", "off"):
        raise ValueError("desired must be 'on' or 'off'")

    win = find_glm_window()
    ensure_foreground(win)

    title = win.window_text()

    # First, check current state with polling (gives GLM time to settle)
    state, rgb, pt = wait_for_state(win, desired=desired, timeout_sec=0.6, poll_interval_sec=0.15)
    rect = win.rectangle()
    print(f"[Init] title={title!r} rect={rect} pt={pt} rgb={rgb} state={state}")

    if state == desired:
        print(f"OK: power already {desired}")
        return

    if state == "unknown":
        raise RuntimeError(f"Cannot classify initial power state at pt={pt} rgb={rgb}.")

    # Attempt toggles
    for attempt in range(retries + 1):
        rect = win.rectangle()
        state, rgb, pt = read_power_state(win)
        print(f"[Attempt {attempt}] title={title!r} rect={rect} pt={pt} rgb={rgb} state={state}")

        if state == desired:
            print(f"OK: power now {desired}")
            return

        if state == "unknown":
            raise RuntimeError(f"Cannot classify power state at pt={pt} rgb={rgb}.")

        # Click once
        click_point(pt)

        # Poll for the desired state (give GLM time)
        state2, rgb2, pt2 = wait_for_state(win, desired=desired, timeout_sec=3.0, poll_interval_sec=0.15)
        rect2 = win.rectangle()
        print(f"[Verify {attempt}] rect={rect2} pt={pt2} rgb={rgb2} state={state2}")

        if state2 == desired:
            print(f"OK: power set to {desired}")
            return

    # Final read for error reporting
    state_f, rgb_f, pt_f = read_power_state(win)
    raise RuntimeError(f"Failed to set power to {desired}. Final: pt={pt_f} rgb={rgb_f} state={state_f}")


def main():
    """
    Test sequence to cover scenarios:
      1) Check if power is on; print
      2) Ensure ON if needed
      3) Sleep 5 seconds
      4) Check if power is off; print
      5) Ensure OFF if needed
    """
    print("=== GLM Power Automation Test ===")

    win = find_glm_window()
    ensure_foreground(win)

    state, rgb, pt = read_power_state(win)
    print(f"[1] Initial state check: state={state}, rgb={rgb}, pt={pt}")

    if state != "on":
        print("[2] Power is not ON -> attempting to turn ON")
        set_power_state("on")
        state2, rgb2, pt2 = read_power_state(win)
        print(f"[3] After ON attempt: state={state2}, rgb={rgb2}, pt={pt2}")
    else:
        print("[2] Power already ON -> proceeding")

    print("[4] Sleeping 5 seconds...")
    time.sleep(5)

    state3, rgb3, pt3 = read_power_state(win)
    print(f"[5] Pre-OFF check: state={state3}, rgb={rgb3}, pt={pt3}")

    if state3 != "off":
        print("[6] Power is not OFF -> attempting to turn OFF")
        set_power_state("off")
        state4, rgb4, pt4 = read_power_state(win)
        print(f"[7] After OFF attempt: state={state4}, rgb={rgb4}, pt={pt4}")
    else:
        print("[6] Power already OFF -> proceeding")

    print("=== Test complete ===")


if __name__ == "__main__":
    main()
