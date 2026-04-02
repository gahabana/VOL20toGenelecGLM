# Findings: Pixel Polling Diagnostic (v0.9.2 — v0.9.4)

## Context

Added diagnostic pixel polling in v0.9.4 to empirically measure how long GLM takes
to repaint its UI after external power toggles (RF remote). Polling read both
honeycomb (gold pixel count) and button (9x9 patch) detectors every 500ms for up
to 5 seconds, requiring 4 consecutive reads where both detectors agreed after
observing a state change.

## Key Findings

### 1. Button detector is fast and reliable (~30ms)

When GLM's UI does update, the **button** pixel patch reflects the change on the
very first read (~30ms after polling starts). It is the reliable, fast-responding
detector for power state.

### 2. Honeycomb detector lags by ~1 second

The honeycomb (gold pixel count) consistently takes **~1.0-1.1 seconds** to catch
up with the button. During this window, the two detectors disagree:

- Power ON→OFF: button=OFF immediately, honeycomb=ON for ~1s (gold pixels still rendered)
- Power OFF→ON: button=ON immediately, honeycomb=OFF for ~1s (gold pixels not yet cleared)

### 3. Both detectors agree by ~1.1 seconds, stable by ~2.7 seconds

Once honeycomb catches up, both detectors agree and remain stable. The 4-consecutive-
agreement criterion was met at read 6 (~2.7s elapsed) in both successful test cases.

### 4. Sometimes GLM never updates (even after 5 seconds)

Three RF remote power toggles while speakers were OFF showed **zero change** across
all 10 polling reads (5 full seconds). Both detectors consistently showed OFF.

This means either:
- The RF toggle command didn't reach the speakers (command ignored in standby)
- GLM genuinely never received the state change from the Genelec network

This is NOT a pixel detection timing issue — it's a GLM/speaker communication issue.

### 5. Honeycomb=ON + button=OFF is a real disagreement scenario

Observed at startup (line 7716) and during power-off transitions (line 7882-7885).
The honeycomb falsely reads ON because:
- No gold pixels = stateOn in honeycomb logic
- This can mean "speakers are on" OR "GLM hasn't painted gold pixels yet"

The button correctly reads OFF in these cases.

## Empirical Timing Data (v0.9.4 session, 2026-04-01)

### RF toggle ON→OFF (21:44:37, speakers were ON)

| Read | Elapsed | Honeycomb | Button | Agreed |
|------|---------|-----------|--------|--------|
| 1 | 36ms | ON | OFF | no |
| 2 | 568ms | ON | OFF | no |
| 3 | 1.1s | OFF | OFF | yes |
| 4 | 1.6s | OFF | OFF | yes |
| 5 | 2.2s | OFF | OFF | yes |
| 6 | 2.7s | OFF | OFF | yes ← stable |

### RF toggle OFF→ON (21:45:08, speakers were OFF)

| Read | Elapsed | Honeycomb | Button | Agreed |
|------|---------|-----------|--------|--------|
| 1 | 29ms | OFF | ON | no |
| 2 | 559ms | OFF | ON | no |
| 3 | 1.1s | ON | ON | yes |
| 4 | 1.6s | ON | ON | yes |
| 5 | 2.2s | ON | ON | yes |
| 6 | 2.7s | ON | ON | yes ← stable |

### RF toggles while speakers OFF (21:42:29, 21:42:44, 21:42:56)

All three: 10 reads over 5 seconds, all `honeycomb=OFF button=OFF`. No change observed.
Timeout reached. Speakers likely did not respond to the RF toggle command while in standby.

## Design Decisions Made From These Findings

1. **v0.9.2**: Trust button over honeycomb on disagreement — button is reliable post-startup
2. **v0.9.3**: Increased PowerVerifyDelay from 1s to 2s — but insufficient for all cases
3. **v0.9.4**: Added polling diagnostic — confirmed exact timing characteristics
4. **Next step**: Deterministic CC28 follow-through design (see `docs/design-deterministic-power-followthrough.md`) — eliminates pixel detection from the external power path entirely

## Conclusion

Pixel polling confirmed that the fundamental problem is not detection accuracy but
**GLM UI repaint timing** for external changes and **RF command reliability** to
speakers in standby. The deterministic CC28 follow-through approach bypasses both
issues by sending our own idempotent command after detecting an external pattern.

The polling code served its diagnostic purpose and has been removed.
