# Power Control Refactor Specification

**Date:** 2026-03-29
**Status:** Draft
**Context:** Empirical testing of GLM 5.2.0 MIDI behavior revealed deterministic power control via CC28 in Toggle mode, enabling a simplified architecture.

---

## Background

Prior to this refactor, power control relied exclusively on UI automation (pixel sampling + mouse click simulation via pywinauto/Win32 API). This required:
- Console session access
- GLM window in foreground for pixel reads
- Complex settling/verification logic
- Fragile pixel color thresholds (broken by partial OFFLINE scenarios)

Empirical testing (2026-03-28/29) proved that GLM 5.2.0 in **Toggle mode** (`powerMessageType=0`) treats MIDI CC28 as an absolute switch:
- CC28 = 0 → speakers OFF (deterministic, idempotent)
- CC28 > 0 → speakers ON (deterministic, idempotent)

Same applies to Mute (CC23) and Dim (CC24). See `RESEARCH-glm-midi-cc28-power.md` Section 11.

---

## Path A: MIDI-Only Power Control

### CLI Flag

**`--no-ui-automation`** — Disables all pixel reading and mouse click simulation. Power control via MIDI only.

**`--headless`** — Enables UI automation (pixel reads, mouse clicks). Use when system is headless/idle/VM where screen interaction is safe.

**Default behavior:** TBD — discuss whether default should be no-ui-automation (safe, non-intrusive) or headless (backwards-compatible with current behavior).

### Startup Behavior

1. **Validate GLM MIDI settings** — Read `%APPDATA%\Genelec\glmv5.cfg`, verify:
   - `powerMessageType` = `0` (Toggle mode)
   - `muteMessageType` = `0` (Toggle mode)
   - `dimMessageType` = `0` (Toggle mode)
   - If any are `1` (Momentary), log a WARNING: deterministic control unavailable
2. **Force known power state** — Send CC28=127 (power ON) at startup
   - Idempotent: if speakers already ON, no harm
   - Sets tracked state to ON
   - Suppress pattern detection during startup window (`POWER_STARTUP_WINDOW`)

### Power Control Flow

**Bridge-initiated (HID, HTTP API, MQTT):**
```
Receive command (toggle, explicit ON, explicit OFF)
  → Determine target state:
      - Toggle: flip tracked state
      - Explicit ON/OFF: use requested state
  → Send CC28=127 (ON) or CC28=0 (OFF)
  → Update tracked state to target
  → Wait settling time (5-6 seconds recommended)
  → Done (no pixel verification needed)
```

**External change (RF remote, GLM GUI click):**
```
5-message MIDI pattern detected
  → Source is external (we didn't send a command)
  → Flip tracked power state
  → If --headless: optionally pixel-read power button to verify direction
  → If --no-ui-automation: trust the flip (small drift risk accepted)
```

### HID (VOL20 Knob)

- Single click = power toggle (current binding, unchanged)
- Bridge sends CC28 with opposite of tracked state
- If tracked state is wrong, user clicks again — standard toggle UX

### HTTP API / Web UI

Two design options. Both use explicit ON/OFF at the API level. Both work with Path A and Path B — no UI automation needed for the HTTP/frontend layer itself.

**Option 1: Two explicit buttons (ON / OFF)**

- **ON button:** Sends `{"state": "on"}` → CC28=127. Highlighted/green when tracked state is ON, muted otherwise.
- **OFF button:** Sends `{"state": "off"}` → CC28=0. Highlighted/red when tracked state is OFF, muted otherwise.
- Both always enabled — tapping the active one is a safe no-op (idempotent).
- User intent is always unambiguous regardless of tracked state accuracy.
- More space on UI, but clearest possible UX.

**Option 2: Single toggle button (current UX, smarter implementation)**

- Single button showing current tracked state (green=ON, grey=OFF).
- **Frontend** reads the displayed state and sends the **opposite** as explicit command:
  - Button shows ON → user taps → frontend sends `{"state": "off"}` → CC28=0
  - Button shows OFF → user taps → frontend sends `{"state": "on"}` → CC28=127
- Key difference from current: the **frontend decides the target state**, not the backend. The backend always receives an explicit ON or OFF, never a blind toggle.
- If displayed state is stale and user taps twice: second tap sends the same state again (idempotent no-op), not a double-toggle.
- Same compact single-button UX as today.

**Comparison:**

| Scenario | Option 1 (two buttons) | Option 2 (smart single) |
|----------|----------------------|------------------------|
| State display correct | User taps desired button | User taps to toggle — works |
| State display stale | User taps desired button — still correct | User taps — sends wrong direction, taps again to fix |
| Double-tap | Each button idempotent | Second tap is idempotent (same explicit state) |
| UI footprint | Larger (two buttons) | Compact (one button) |

**Recommendation:** Option 2 for compactness with the safety of explicit commands. Option 1 if state accuracy is frequently in doubt.

API endpoints (both options use the same API):
- `POST /api/power` with `{"state": "on"}` → CC28=127
- `POST /api/power` with `{"state": "off"}` → CC28=0
- `POST /api/power` with `{"state": "toggle"}` or no body → backend flips tracked state (backwards-compatible, used by HID)

### MQTT / Home Assistant

- Switch entity with explicit ON/OFF commands (already supported via `SetPower(state=True/False)`)
- State published based on tracked state
- No change needed in MQTT protocol — just the underlying implementation switches from UI automation to MIDI

### Mute and Dim

Same deterministic approach applies:
- CC23=127 → Mute ON, CC23=0 → Mute OFF
- CC24=127 → Dim ON, CC24=0 → Dim OFF
- Already working this way in current code for MIDI sends
- Tracked state updated from both our sends and GLM MIDI output (CC23/CC24 feedback)

### Settling Time

- Current: `POWER_SETTLING_TIME` = 2.0s + `POWER_COOLDOWN_TIME` = 1.5s = 3.5s total
- Recommended: increase to 5-6s total to account for speaker GNet boot time (2-5s observed)
- During settling: block further power commands, allow volume/mute/dim through

### What Gets Removed/Simplified (in --no-ui-automation mode)

- `GlmPowerController` / `WindowsController` — not used for power
- `captureScreen`, `analyzePixels`, `analyzeButtonPatch` — not called
- `BringToForeground` / `RestoreForeground` — not needed
- `ensure_session_connected` before power — not needed (MIDI works regardless of session state)
- Power pattern detector toggle assumption — changed to flip-on-external-only

### What Stays

- Power pattern detector — still detects external changes
- MIDI output reader — still reads mute/volume/dim state from GLM
- `POWER_STARTUP_WINDOW` suppression — still needed for startup burst
- Settling time logic — still needed, possibly increased

---

## Risks and Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| Missing external toggle pattern → state drift | Medium | Optional periodic pixel reconciliation in --headless mode |
| GLM restart resets power state | Low | Re-send CC28=127 after GLM restart detected (GlmManager already monitors process) |
| Config reset to Momentary mode | Low | Startup validation + warning log |
| State drift over long uptime | Low | Accept or add optional background reconciliation |
| Speaker disconnect after power cycle | Medium | Extended settling time, separate OFFLINE health metric |
| glmv5.cfg not accessible from bridge | Low | Warn but continue — MIDI commands still work, just no validation |

---

## GLM Prerequisites

- GLM version: 5.2.0 or later (MIDI fixes)
- MIDI Settings → Power, Mute, Dim: set to **"Toggle"** (not "Momentary")
- MIDI enabled with correct input/output port names (`GLMMIDI 1` / `GLMOUT 1`)
- Corresponding `glmv5.cfg` values: `powerMessageType=0`, `muteMessageType=0`, `dimMessageType=0`

---

## Path B: UI Automation Layer (`--headless`)

Path B builds on top of Path A. All of Path A applies. The `--headless` flag enables UI automation for **reading** the screen (verification, health monitoring). An additional `--ui-power` flag enables UI automation for **controlling** power (clicking the button instead of MIDI).

### CLI Flags

| Flags | Power Control | Screen Reading | Use Case |
|-------|--------------|----------------|----------|
| (default = `--no-ui-automation`) | MIDI CC28 | Disabled | Desktop use, user interacting with GLM |
| `--headless` | MIDI CC28 | Enabled | VM/unattended, safe to read screen |
| `--headless --ui-power` | UI click | Enabled | Fallback if MIDI power causes speaker disconnects |

`--ui-power` requires `--headless` — error if used alone.

### Power Verification (screen reading)

When `--headless` is active, after any power command (MIDI or UI click):
1. Wait settling time (5-6s)
2. Pixel-read the power button (green=ON, grey=OFF)
3. Compare with tracked state
4. If mismatch: log WARNING, update tracked state to pixel truth
5. This corrects any state drift from missed external toggle patterns

**Button-only detection** — do NOT use the honeycomb gold pixel count for power state. The current `goldCountOff=50` threshold produces false positives when one speaker is OFFLINE (single OFFLINE label = ~1700 gold pixels). Power state comes exclusively from the button color.

### Speaker Health Monitoring (separate from power)

When `--headless` is active, scan honeycomb for OFFLINE labels as a **separate health metric**:
- Count gold pixel clusters → estimate number of OFFLINE speakers
- Report via MQTT/API as `offline_speakers: 0/1/2/3`
- Does NOT affect power state tracking
- Useful for alerting ("speaker disconnected, may need power cycle")
- Run periodically (e.g., every 30-60s) or after power transitions

### Power via UI Click (`--headless --ui-power`)

When `--ui-power` is active, power commands use the current UI automation approach:
1. `BringToForeground()` — focus GLM window
2. `ensure_session_connected()` — verify console session
3. Click power button at calculated coordinates
4. Pixel-read to verify state change
5. `RestoreForeground()` — return focus to previous window

This is the current behavior, preserved as a fallback for cases where MIDI CC28 power commands cause more speaker disconnects than GUI clicks. The hypothesis (unproven but suspected) is that the GUI click path may have different internal timing in GLM that is gentler on speaker reconnection.

### Background Reconciliation

When `--headless` is active (with or without `--ui-power`):
- Optional periodic pixel read of power button (e.g., every 60s)
- Only when GLM window is not obscured (check if foreground or use PrintWindow)
- Corrects state drift from missed external toggles
- Does NOT steal focus — skips if GLM is not accessible

### DPI Awareness

Current button position calculation (`width-28, 80`) does not account for DPI scaling. Path B should:
- Detect DPI scale factor from window size vs expected size
- Adjust button coordinates accordingly
- Or use relative positioning from window edges with scale factor

### What Path B Adds Over Path A

| Component | Path A | Path B | Path B + `--ui-power` |
|-----------|--------|--------|----------------------|
| MIDI CC28 power control | Yes | Yes | No (uses UI click) |
| Power button pixel verification | No | Yes | Yes |
| Speaker OFFLINE health metric | No | Yes | Yes |
| Background reconciliation | No | Yes | Yes |
| Focus stealing | Never | Minimal (reads only) | Yes (click requires focus) |
| Console session required | No | No | Yes |
| `GlmPowerController` needed | No | Read-only mode | Full (read + click) |
