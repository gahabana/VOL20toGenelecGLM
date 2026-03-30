# Future Work

Items documented for future improvement. Not blocking current functionality.

## Multi-Monitor Support for Pixel Detection

**Current:** `SystemParametersInfo(SPI_GETWORKAREA)` returns the primary monitor's work area only. If GLM is placed on a secondary monitor, off-screen detection may incorrectly flag it.

**Fix:** Replace with `MonitorFromWindow(hwnd, MONITOR_DEFAULTTONEAREST)` + `GetMonitorInfoW` to get the work area of the monitor the GLM window is actually on.

**Impact:** Low — VMs are typically single-monitor. Only affects multi-monitor setups where GLM is on a non-primary monitor.

## DPI Scaling for Power Button Position

**Current:** Button position is calculated as `(width - 28, 80)` which assumes 100% DPI scaling. On high-DPI displays, the actual button position may differ.

**Fix:** Detect DPI scale factor from window size vs expected size, or use `GetDpiForWindow` (Win10 1607+) to adjust coordinates.

**Impact:** Low — VMs typically run at 100% scaling.

## CC28 on GLM MIDI Output

**Current:** GLM never sends CC28 (System Power) on its MIDI output port. Power state detection relies on the 5-message burst pattern (CC23/CC20/CC24/CC23/CC20) which is identical for ON, OFF, and no-op.

**Action:** Feature request to Genelec to add CC28 to MIDI output (likely a one-line change in their JUCE code). This would give us direct power state feedback and eliminate the need for pixel-based verification entirely.
