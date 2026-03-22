# Known Issue: RF Remote Power State Drift

**Date:** 2026-03-22
**Version:** v3.2.29
**Severity:** Medium — causes inverted power state tracking until manual correction

---

## Summary

When the RF remote toggles speaker power, GLM sometimes loses sync with actual speaker state. From that point forward, GLM's internal state, its power button rendering, and its MIDI output are all inverted relative to reality. Since bridge2glm relies on GLM as its source of truth (both MIDI pattern detection and pixel sampling), the bridge inherits GLM's confusion.

---

## Observed Behavior (2026-03-22, v3.2.29)

### Timeline

| Time | Action | Script state | Actual speakers | GLM button |
|------|--------|-------------|-----------------|------------|
| boot | Pixel sync | ON | ON | GREEN ✓ |
| 15:53:03 | GUI click #1 | OFF | OFF | GREY ✓ |
| 15:53:26 | GUI click #2 | ON | ON | GREEN ✓ |
| **15:54:58** | **RF remote #1** | **OFF** | **still ON** | **GREEN** |
| 15:55:40 | RF remote #2 | ON | OFF | GREEN (stale) |
| *— 34 minute gap — everything inverted from here —* |
| 16:29:42 | RF remote #3 | OFF | ON (turned on) | GREEN? |
| 16:29:52 | RF remote #4 | ON | OFF (turned off) | GREEN (stale) |
| 16:30:21 | GUI click | OFF | OFF (no change) | GREY (re-synced!) |

### Root Cause Event

At **15:54:58**, RF remote press #1:
- GLM processed the RF event internally and sent the 5-message MIDI burst
- But the speakers **did not actually toggle** — they remained ON
- GLM's power button stayed GREEN (showing ON, which was actually correct at that moment, but GLM thought it toggled)
- From this point forward, every toggle detection is inverted

### GLM Screenshot After Drift

After RF remote #2 (15:55:40), GLM showed:
- Power button: **GREEN** (thinks ON)
- All 3 speakers: **"OFFLINE"** (orange labels)
- Speakers were actually powered off

### Self-Correction via GUI Click

When the user clicked the stale green power button at 16:30:21:
- GLM detected speakers were offline during the click handler
- Instead of toggling power, GLM **re-synced** its button to grey (OFF)
- Speakers stayed off (no actual toggle)
- But GLM still sent the MIDI burst, which our bridge processed as a toggle

---

## Why This Happens

### The Communication Chain

```
RF Remote → (868/915 MHz radio) → GLM Network Adapter → (USB) → GLM Software → (virtual MIDI) → bridge2glm
```

The failure occurs between the RF remote and the GLM Network Adapter (or between the adapter and the speakers). GLM processes the RF event before confirming the speakers actually responded. The MIDI burst is sent based on GLM's **intent**, not on **confirmed speaker state**.

### What GLM Gets Wrong

1. **Internal state**: GLM toggles its internal power boolean without speaker confirmation
2. **Button render**: Power button stays green even when speakers are OFFLINE
3. **MIDI output**: Sends the 5-message burst based on internal state change, not actual speaker state

### What bridge2glm Cannot Know

- Actual speaker power state (only accessible via direct adapter/RS485 communication)
- Whether GLM's state matches reality
- Whether an OFFLINE speaker is "powered off" vs "lost communication"

---

## Impact on bridge2glm

### MIDI Pattern Detection

The 5-message burst (`CC23→CC20→CC24→CC23→CC20`) is a toggle signal with no direction. bridge2glm tracks state as a flip: each detected pattern inverts the stored state. If any single event causes drift (RF glitch, false positive, missed pattern), all subsequent states are inverted until corrected.

### Pixel-Based Detection

The power button pixel color reflects GLM's belief, not reality:
- GREEN `rgb=(28, 134, 100)` → GLM thinks ON (may be wrong)
- GREY `rgb=(71, 71, 71)` → GLM thinks OFF (may be wrong)
- Speakers can be OFFLINE while button shows GREEN

### Both Methods Fail Together

Since both MIDI patterns and pixel colors reflect GLM's confused state, there is no independent ground truth available to the bridge. Both detection methods will agree with each other (and with GLM) while all three are wrong.

---

## Potential Mitigations (Future Work)

### 1. Periodic Pixel Re-sync After RF Toggle

After detecting an RF power pattern, schedule a delayed pixel read (2-3 seconds later) to verify the button state matches expectations. This helps when GLM self-corrects its render but doesn't help when GLM's render is also wrong (the OFFLINE scenario).

**Limitation**: Requires window focus (disruptive), and GLM's button can lie (green while speakers OFFLINE).

### 2. OFFLINE Detection via Additional Pixel Sampling

Monitor a speaker icon region for the orange "OFFLINE" label. If speakers show OFFLINE but power button shows ON, flag a desync.

**Limitation**: Fragile — speaker icon positions vary by setup. Would need template matching (OpenCV). Over-engineered for an edge case.

### 3. GLM Log File Monitoring

GLM may log adapter communication errors or speaker OFFLINE events to a log file. Monitoring this file could detect desync events.

**Not investigated** — GLM's log format and location are unknown.

### 4. Direct Speaker State Query (genlc-style)

Bypass GLM entirely and query speaker power state via the USB adapter using the reverse-engineered protocol (see `RESEARCH-button-detection-approaches.md`, Approach 6).

**Limitation**: genlc is abandoned (2021), may conflict with GLM's adapter access, and the protocol is undocumented.

### 5. Genelec Feature Request

Request Genelec add:
- CC28 to MIDI output (power state feedback)
- Absolute mode for CC28 (127=ON, 0=OFF instead of toggle)
- Speaker OFFLINE status via MIDI

This would solve the problem at the source. GLM 5.2.0 already fixed a MIDI output bug, suggesting active development in this area.

### 6. GUI Click as Drift Correction

The 16:30:21 observation showed that clicking the power button in GLM can force a re-sync with actual speaker state. If drift is detected, the bridge could programmatically click the power button twice (read state, click, read state) to force GLM to re-evaluate.

**Limitation**: Only works if GLM's click handler actually checks speaker state (observed once, not confirmed as consistent behavior).

---

## Conclusions

1. **This is a GLM bug**, not a bridge2glm bug. GLM sends MIDI output based on its internal state change intent, not confirmed speaker state.
2. **The RF remote ↔ adapter communication is unreliable** — toggle commands can be lost, causing permanent state drift.
3. **No purely software-based detection can reliably determine actual speaker power state** from outside GLM. Both MIDI and pixel methods reflect GLM's belief.
4. **The bridge correctly processes all MIDI patterns** — the 4 detected patterns in this session all had proper timing (30ms gaps, isolated bursts). The issue is that GLM's patterns don't reflect reality.
5. **GUI clicks appear to force GLM to re-sync** with actual speaker state, which could be exploited as a drift correction mechanism.

---

## Related Files

- `RESEARCH-button-detection-approaches.md` — 7 alternative approaches for power state detection
- `RESEARCH-glm-midi-cc28-power.md` — Deep dive on CC28 behavior and MIDI feedback limitations
- `research_glm_cli_and_midi.md` — glm-cli analysis and complete GLM MIDI CC mapping
- `PowerOnOff/glm_power.py` — Pixel-based power state detection implementation
- `bridge2glm.py` — MIDI pattern detection implementation (lines ~986-1043)
- `midi_constants.py` — Pattern timing thresholds and CC definitions
