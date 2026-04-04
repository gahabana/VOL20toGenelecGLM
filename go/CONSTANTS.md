# Behavioral Constants Reference

All tunable constants that define the behavior of the Go binary. When adding new constants, update this file AND the relevant source code.

## Power Control (`power/power_windows.go`)

### Pixel Detection Thresholds

| Constant | Value | Purpose |
|----------|-------|---------|
| `goldMinRed` | 150 | Min red channel for OFFLINE gold label |
| `goldMinGreen` | 120 | Min green channel for OFFLINE gold label |
| `goldMaxGreen` | 200 | Max green channel for OFFLINE gold label |
| `goldMaxBlue` | 80 | Max blue channel for OFFLINE gold label |
| `goldCountOff` | 50 | Min gold pixels to confirm speakers OFF |
| `offMaxBrightness` | 95 | Max pixel brightness for OFF (dark grey) button |
| `offMaxChannelDiff` | 22 | Max RGB channel variation for OFF button |
| `onMinGreen` | 110 | Min green channel for ON (green/teal) button |
| `onGreenRedDiff` | 35 | Min (G - R) difference for ON button |

### Button Position (hardcoded inline — TODO: extract to named constants)

| Value | Purpose |
|-------|---------|
| `width - 28` | Power button X offset from window right edge |
| `80` | Power button Y offset from window top |
| `4` | Patch sampling radius (9x9 pixel patch) |
| `-8` | Fallback nudge X offset (avoids glyph overlap) |

### Timing

| Constant | Value | Purpose |
|----------|-------|---------|
| `pollInterval` | 150ms | State polling interval during power toggle verification |
| `verifyTimeout` | 3s | Max time to wait for state change after click |
| `postClickDelay` | 350ms | Delay after click before starting to poll |
| `clickDownUpDelay` | 20ms | Delay between mouse down and mouse up events |
| `hwndCacheTTL` | 5s | Window handle cache lifetime |
| `powerPrepareDelay` | 250ms | Wait for GLM to repaint after window resize/move (unexported) |

## Controller (`controller/controller.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `PowerSettlingTime` | 2.0s | Block ALL commands during power transition (when no pixel detection) |
| `PowerCooldownTime` | 1.5s | Block power-only commands after transition completes |
| `PowerTotalLockout` | 3.5s | Total settling + cooldown (only used without pixel detection) |
| `PowerVerifyDelay` | 2.0s | Wait before pixel-verifying power state after command or external change |

## Consumer (`consumer/consumer.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `MaxEventAge` | 2.0s | Discard actions older than this (stale event filter) |

## HID Reader (`hid/hid_windows.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `retryDelay` | 5s | Retry interval when HID device not found |
| `maxReportSize` | 64 | Max HID report buffer size (bytes) |
| `readTimeoutMs` | 1000 | HID read timeout (ms) for context check |

## MIDI (`types/midi.go`)

### CC Numbers

| Constant | Value | Purpose |
|----------|-------|---------|
| `CCVolumeAbs` | 20 | Absolute volume (0-127) |
| `CCVolUp` | 21 | Volume increment (momentary) |
| `CCVolDown` | 22 | Volume decrement (momentary) |
| `CCMute` | 23 | Mute (toggle) |
| `CCDim` | 24 | Dim (toggle) |
| `CCPower` | 28 | System Power — in Toggle mode: 0=OFF, >0=ON (deterministic). No MIDI feedback (CC28 never sent by GLM). |

**GLM MIDI Settings prerequisite:** Power, Mute, and Dim must be set to **"Toggle"** (not "Momentary") for deterministic control. Toggle mode: value 0=OFF, value >0=ON (absolute switch, idempotent). Momentary mode: value >0=blind toggle, value 0=ignored. Corresponds to `powerMessageType=0` in `glmv5.cfg`.

### Volume Conversion

| Constant | Value | Purpose |
|----------|-------|---------|
| `VolumeDBOffset` | 127 | Offset between MIDI value (0-127) and dB (-127..0). Used by REST API and MQTT. |

### Power Pattern Detection

| Constant | Value | Purpose |
|----------|-------|---------|
| `PowerPatternWindow` | 0.5s | Max time window for entire 5-message pattern |
| `PowerPatternMinSpan` | 0.05s | Min span — faster means buffer dump, ignore |
| `PowerPatternMaxGap` | 0.26s | Max gap between consecutive messages |
| `PowerPatternMaxTotal` | 0.35s | Max total of all 4 gaps |
| `PowerPatternPreGap` | 0.12s | Min silence before pattern starts |
| `PowerStartupWindow` | 3.0s | Second pattern within this = GLM startup, suppress |

## MIDI Gate (`midigate/gate.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `SettleDelay` | 50ms | Gap after GLM's state burst (Volume CC20) before sending next command. GLM needs ~30ms min; 50ms gives 67% margin. |
| `ResponseTimeout` | 2s | Max wait for GLM state burst before giving up and sending next queued command. p99 from production logs is ~600ms, max observed 1.9s. |
| `sendChSize` | 32 | Buffered channel so consumer never blocks on SendCC |
| `recvChSize` | 10 | Buffered channel so MIDI callback never blocks on NotifyReceive |

Volume commands coalesce (latest wins). Mute/dim/power are queued individually (each toggle matters). Non-volume commands have priority over coalesced volume.

## Startup Probe (`main.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `expectedCC20` (burst) | 5 | CC20 count in GLM's 12-message startup burst (2 patterns × Mute→Vol→Dim→Mute→Vol) |
| `expectedCC20` (ACK) | 2 | CC20 count in power command 5-message ACK (1 pattern) |
| `firstTimeout` (burst) | 15s | Max wait for first CC20 from GLM startup burst. GLM typically emits ~1.7s after launch. |
| `ackTimeout` | 3s | Max wait for first CC20 after CC28=127 send |
| `msgTimeout` | 2s | Max wait between consecutive CC20s within a burst or ACK |

**Startup sequence (when managed):** MIDI reader starts → GLM launched → reader captures 12-message startup burst (5 CC20s give volume; mute/dim via UpdateFromMIDI) → CC28=127 forces power ON → 5-message ACK (2 CC20s). Count-based detection: proceeds as soon as expected CC20 count is reached, no settle timer. Vol+/Vol- probing is not needed — volume is discovered passively from GLM's MIDI output.

## MIDI Reader (`midi/winmm_reader.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `midiInBufSize` | 256 | Buffered channel size for MIDI callback |

## GLM Manager (`glm/manager_windows.go`)

### CPU Gating

| Constant | Value | Purpose |
|----------|-------|---------|
| `cpuThreshold` | 10.0% | CPU must be below this to launch GLM |
| `cpuCheckInterval` | 1s | Polling interval for CPU check |
| `cpuMaxChecks` | 300 | Max checks (5 minute timeout) |

### Process Launch

| Constant | Value | Purpose |
|----------|-------|---------|
| `postStartDelay` | 3s | Wait after launching GLM before proceeding |

### Window Stabilization

| Constant | Value | Purpose |
|----------|-------|---------|
| `windowPollInterval` | 1s | Polling interval for window handle check |
| `windowStableCount` | 3 | Consecutive identical titled-window HWND required |
| `windowTimeout` | 60s | Max wait for window to stabilize |

### Watchdog

| Constant | Value | Purpose |
|----------|-------|---------|
| `watchdogInterval` | 10s | How often the watchdog checks GLM |
| `hangThreshold` | 3 | Consecutive hangs before kill+restart (30s total) |
| `restartDelay` | 5s | Wait after killing before restarting |

## WebSocket (`api/websocket.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `wsWriteTimeout` | 5s | Max time to write to a WebSocket client before dropping |

## Retry Logging (`logging/retry.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `DefaultIntervals` | [2, 10, 60, 600, 3600, 86400] | Log milestones: 2s, 10s, 1min, 10min, 1hr, 1day |

## CLI Defaults (`config/config.go`)

See [README.md](README.md) for all CLI flags and their defaults.
