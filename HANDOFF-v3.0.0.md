# VOL20toGenelecGLM v3.0.0 - Session Handoff Document

## Project Overview

**VOL20 to Genelec GLM MIDI Bridge** - Bridges a Fosi Audio VOL20 USB volume knob to Genelec GLM software via MIDI. Supports volume control, mute, dim, and power management with UI automation.

**Repository:** `gahabana/VOL20toGenelecGLM`
**Current Version:** v3.0.0 (merged to main)

---

## What Was Accomplished This Session

### 1. Created GLM Manager Module (`PowerOnOff/glm_manager.py`)
- Replaces external PowerShell script for GLM lifecycle management
- Handles GLM start, stop, watchdog, and window handling
- Pure Python implementation using pywinauto

### 2. Fixed Critical Window Handle Mismatch Bug
**Problem:** GLM window was not minimizing and power control was unstable.

**Root Cause:** Two different window-finding methods were returning DIFFERENT handles:
- `glm_manager.py` used `EnumWindows` (Win32 API) → found handle 852304
- `glm_power.py` used `pywinauto` → found handle 918098

**Solution:** Consolidated ALL window finding to use pywinauto with JUCE class filter:
```python
from pywinauto import Desktop
wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")
candidates = [w for w in wins if "GLM" in (w.window_text() or "")]
```

### 3. Fixed GLM Window Not Minimizing After Power Operations
**Problem:** `_restore_window_state()` used `PostMessageW(WM_SYSCOMMAND, SC_MINIMIZE)` which doesn't work for JUCE apps.

**Solution:** Changed to use pywinauto's `win.minimize()` method.

### 4. Added `minimize()` Method to `GlmPowerController`
```python
def minimize(self) -> bool:
    """Minimize the GLM window using pywinauto."""
    win = self._find_window(use_cache=False)
    win.minimize()
    # ... verification logic
```

### 5. Fixed Script Not Exiting When Another Instance Running
**Problem:** Logging thread was non-daemon, blocking `sys.exit()`.

**Solution:** Call `stop_logging()` before `sys.exit(1)`.

### 6. Tuned Timing Parameters
| Parameter | Old Value | New Value |
|-----------|-----------|-----------|
| cpu_threshold | 2.0% | 6.0% |
| cpu_check_interval | 5.0s | 3.0s |
| post_start_sleep | 5.0s | 3.0s |
| stable_handle_count | 4 | 2 |

### 7. Added Version Number
- Added `__version__ = "3.0.0"` to `fosi2-glm-midi-sonnet4.5.py`
- Logs version at startup: `Starting fosi2-glm-midi-sonnet4.5.py v3.0.0`

### 8. Created Diagnostic Tool (`debug_window_finder.py`)
- Compares EnumWindows vs pywinauto window finding in real-time
- Logs mismatches with `[MISMATCH]` prefix
- Useful for debugging window handle issues

---

## Files Modified

| File | Changes |
|------|---------|
| `PowerOnOff/glm_manager.py` | New file - GLM lifecycle manager |
| `PowerOnOff/glm_power.py` | Added `minimize()`, fixed `_restore_window_state()` |
| `PowerOnOff/__init__.py` | Updated exports |
| `fosi2-glm-midi-sonnet4.5.py` | Integrated GLM Manager, version, various fixes |
| `debug_window_finder.py` | New diagnostic tool |

---

## Git Status

- **Branch:** `main` (merged from `claude/analyze-glm-power-script-AEchq`)
- **PR:** Created and merged via GitHub
- **Tag:** `v3.0.0` should be created:
  ```bash
  git tag -a v3.0.0 -m "GLM Manager module with consolidated pywinauto window handling"
  git push origin v3.0.0
  ```

---

## Key Technical Details

### Window Finding (Consolidated Approach)
```python
from pywinauto import Desktop

def _get_main_window_handle(self, pid: int) -> int:
    wins = Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")
    candidates = [w for w in wins if "GLM" in (w.window_text() or "")]
    for w in candidates:
        if w.process_id() == pid:
            return w.handle
    return 0
```

### Window Minimize (Working Method for JUCE Apps)
```python
# DON'T USE (doesn't work for JUCE):
# PostMessageW(hwnd, WM_SYSCOMMAND, SC_MINIMIZE, 0)

# USE THIS INSTEAD:
win.minimize()  # pywinauto method
```

### GLM Manager Config Defaults
```python
@dataclass
class GlmManagerConfig:
    glm_path: str = r"C:\Program Files\Genelec\GLM\GLM.exe"
    cpu_threshold: float = 6.0
    cpu_check_interval: float = 3.0
    max_cpu_wait: float = 120.0
    post_start_sleep: float = 3.0
    stable_handle_count: int = 2
    watchdog_interval: float = 30.0
    minimize_on_start: bool = True
```

---

## Known Considerations

1. **Admin Privileges:** `tscon` (for Windows session recovery when running headless) requires admin. Script works without it but logs a warning.

2. **Headless Mode:** When running via Windows Task Scheduler without RDP session, window operations work but session reconnection fails without admin.

3. **pywinauto Dependency:** All window operations now require pywinauto. The script checks for it at import time.

---

## Testing Checklist

- [x] GLM starts and minimizes correctly on script startup
- [x] Power ON/OFF cycles work via MIDI button
- [x] Window state preserved/restored after power operations
- [x] Watchdog recovers when GLM is manually closed
- [x] Script exits cleanly when another instance is running
- [x] Version number appears in startup log

---

## Next Steps (if continuing development)

1. Create the v3.0.0 git tag (see command above)
2. Any future enhancements should branch from `main`

---

*Handoff document created: 2026-01-09*
