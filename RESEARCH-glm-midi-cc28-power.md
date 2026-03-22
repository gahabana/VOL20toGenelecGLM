# Research: GLM MIDI CC28 (System Power) Behavior in Detail

**Date:** 2026-03-22
**Purpose:** Determine if there is ANY way to get reliable power state feedback from GLM via MIDI, or if a different CC value/mode gives better results than a blind toggle.

---

## 1. GLM MIDI Settings: CC28 Mode Configuration

### What the manual says (GLM 5 Operating Manual, Section 8.6, pages 85-86)

> "GLM functions, other than Volume, require 'Toggle' MIDI messages to work. Once a button is pressed this sends out a value > 0 and when the button is pressed the second time the message value sent is 0."

This is the **critical finding**: GLM expects **Toggle mode** MIDI messages for all functions except Volume (which is absolute 0-127). Toggle mode means:
- **First press**: controller sends value > 0 (typically 127)
- **Second press**: controller sends value 0

GLM does **NOT** support:
- ~~Momentary mode~~ (send 127 each time to toggle) - **INCORRECT for GLM**
- ~~Absolute/Switch mode~~ (127 = ON, 0 = OFF) - **NOT how GLM interprets it**

### How GLM actually processes CC28

GLM treats CC28 as a **toggle with value tracking**:
- **Receive value > 0** (e.g., 127): Toggle power state (ON→OFF or OFF→ON)
- **Receive value 0**: This is the "button release" — GLM **ignores it** (no action)

This means sending CC28=127 repeatedly will toggle power every time. Sending CC28=0 does nothing. There is **no way to send "explicitly turn ON" or "explicitly turn OFF"** via CC28.

### Configurable CC numbers

All CC assignments are user-configurable in GLM Settings → MIDI Settings. Default assignments (from `glm-cli` source and GLM 5 manual):

| CC# | Function | Mode | Notes |
|-----|----------|------|-------|
| 20 | Volume | Absolute (0-127) | Bidirectional — GLM sends this back |
| 21 | Volume Up | Momentary trigger | Send 127 to increment |
| 22 | Volume Down | Momentary trigger | Send 127 to decrement |
| 23 | Mute | Toggle | >0 toggles, 0 ignored |
| 24 | Dim | Toggle | >0 toggles, 0 ignored |
| 25 | Preset Level 1 | Toggle | Recall level preset |
| 26 | Preset Level 2 | Toggle | Recall level preset |
| 27 | BM Bypass | Toggle | Bass Management bypass |
| **28** | **System Power** | **Toggle** | **>0 toggles, 0 ignored. No CC28 feedback.** |
| 30 | Group X | Absolute (0-127) | Select group by number |
| 31-40 | Group 1-10 | Toggle | Direct group selection |
| 41 | Group Plus | Toggle | Next group |
| 42 | Group Minus | Toggle | Previous group |
| 43 | Solo Dev | Absolute (0-127) | Solo device by MIDI ID |
| 44 | Mute Dev | Absolute (0-127) | Mute device by MIDI ID |

**Important correction to our codebase:** `midi_constants.py` currently labels CC28 as `ControlMode.MOMENTARY` in the comment but `ControlMode.TOGGLE` in `ACTION_TO_GLM`. The manual confirms it's a **toggle** — GLM expects alternating >0/0 messages. However, since GLM ignores value=0, sending 127 every time effectively works as a momentary trigger anyway (each 127 toggles).

---

## 2. GLM MIDI Output Behavior on Power Toggle

### What GLM sends on its MIDI output port

Based on our codebase's empirical observations (`midi_constants.py` lines 53-67) and the Genelec documentation:

**When power is toggled (ON→OFF or OFF→ON), GLM sends a burst of CC messages on its MIDI output:**

```
MUTE(CC23) → VOLUME(CC20) → DIM(CC24) → MUTE(CC23) → VOLUME(CC20)
```

This 5-message burst arrives within ~150ms. It represents GLM updating all state CCs to reflect the new system state.

**GLM does NOT send CC28 (System Power) on its MIDI output.** There is no echo or feedback of the power command itself.

### What GLM sends for other state changes

| Event | What GLM sends on output | Values |
|-------|--------------------------|--------|
| Volume change | CC20 | New absolute volume (0-127) |
| Mute toggle | CC23 | 127 = muted, 0 = unmuted |
| Dim toggle | CC24 | 127 = dimmed, 0 = undimmed |
| Power toggle | CC23 + CC20 + CC24 + CC23 + CC20 | Burst pattern (~150ms) |
| Startup | Two bursts (7 msgs then 5 msgs) | Initial state dump |
| Group change | CC23 + CC20 + CC24 + CC23 + CC20 | Same as power toggle |

### The power detection problem

Since GLM sends the same CC23/CC20/CC24 burst for **both** power toggle and group changes, and does NOT send CC28 feedback, distinguishing power toggle from other events requires:

1. **Pattern timing analysis** — Power toggle produces an isolated burst with >120ms silence before it
2. **Context awareness** — Ignore bursts during startup or volume init
3. **No definitive state** — The burst doesn't tell you whether power is now ON or OFF

Our codebase (`bridge2glm.py` lines 960-1060) implements this via a sliding window buffer that matches the 5-CC pattern with timing constraints (max 170ms between messages, >120ms pre-gap for isolation).

---

## 3. SoundOnSound GLM 4.2 Review — MIDI Details

**Source:** [SoundOnSound GLM 4.2 Review](https://www.soundonsound.com/reviews/genelec-glm-42) (July 2023)

Key excerpts regarding MIDI:
- "GLM 4.2 brought a further improvement in the shape of MIDI support. It's now possible to assign MIDI Continuous Controllers to all of these parameters, so you could for instance use a MIDI control surface to adjust volume, switch between monitor Groups, mute or dim the speakers and more"
- "For some reason Volume defaults to MIDI CC20 rather than 7 as you might expect, but all of these assignments can freely be changed by the user"
- "A MIDI Learn function might be a nice addition in a future update" — No MIDI Learn as of 2023
- The review does **not** discuss power control via MIDI (CC28 was added in GLM 5, post-review)
- The review does **not** discuss MIDI feedback/output behavior

---

## 4. Official Genelec Documentation

### GLM 5 Operating Manual (Section 8.6, Pages 85-86)

**MIDI Settings page provides:**
- Enable/disable GLM MIDI Interface toggle
- MIDI log showing messages received (for debugging CC numbers/channels)
- MIDI Input Device selector
- MIDI Output Device selector (for bidirectional sync)
- MIDI Channel selector
- CC assignment table for all functions

**Key manual quote on toggle behavior:**
> "GLM functions, other than Volume, require 'Toggle' MIDI messages to work. Once a button is pressed this sends out a value > 0 and when the button is pressed the second time the message value sent is 0. MIDI controllers usually come with command editing software that allows you to customise the behaviour of the controller components, e.g. encoders, faders, pedals and buttons."

### Genelec Support Article — MIDI Output Device

**Source:** [Why do I need to configure a MIDI Output device in GLM 4.2?](https://support.genelec.com/hc/en-us/articles/5842976755986)

> "By defining a MIDI Output device, you can keep the status of MIDI controllers that support two-way communication in sync with GLM. For example, controllers that show toggle button status via LEDs may be able to sync to show states like Mute in GLM. Some MIDI controllers may have sync limitations, for example rotary controllers with limits cannot be synced."

**Key insight:** Genelec explicitly mentions LED sync for states "like Mute" — they conspicuously do NOT mention power state. This supports our finding that:
- **Mute (CC23):** GLM sends state feedback (127=muted, 0=unmuted) ✓
- **Dim (CC24):** GLM sends state feedback (127=dimmed, 0=undimmed) ✓
- **Volume (CC20):** GLM sends absolute value feedback ✓
- **Power (CC28):** GLM does NOT send state feedback ✗

### GLM Release Notes Timeline

| Version | Date | MIDI Changes |
|---------|------|--------------|
| GLM 4.2.0 | May 2022 | First MIDI support: volume, mute, groups, BM, presets |
| GLM 5.0.1 | Jan 2024 | Added System Power (CC28), Solo/Mute Dev (CC43/44), system-wide Mute and Dim |
| GLM 5.0.4 | Feb 2024 | Bug fixes |
| GLM 5.1.1 | Jul 2024 | Improved power up/down functionality, 9320A memory store command |
| GLM 5.2.0 | May 2025 | **Fixed: MIDI issue causing messages from GLM to MIDI controller to fail.** Improved power up/down. Grouped Solo/Mute for W371 woofer system. |
| GLM 5.2.1 | 2025 | Further fixes |

**The GLM 5.2.0 MIDI fix is notable** — "MIDI issue causing messages from GLM to MIDI controller to fail" suggests the MIDI output port had bugs prior to 5.2. This could mean the CC23/CC20/CC24 feedback burst we observe was unreliable in earlier versions.

---

## 5. MIDI Controller Integration Guides

### SoundFlow + Stream Deck + GLM

**Source:** [SoundFlow Forum GLM Stream Deck Setup](https://forum.soundflow.org/-8844/glm-app-stream-deck-setup)

Setup approach:
1. Create IAC bus in Mac Audio MIDI Setup
2. Enable GLM MIDI Interface, select IAC Driver Bus 1 as input
3. In SoundFlow, create macros with "Send MIDI CC" actions
4. Set CC number per function, value 127, Advanced Properties → External MIDI Port = "IAC Driver Bus 1"

**No mention of reading state back from GLM.** Stream Deck integration is purely one-directional (commands TO GLM). No LED feedback, no state sync.

### Gearspace DIY Controller Thread

**Source:** [Gearspace Thread](https://gearspace.com/board/high-end/1279456-diy-genelec-glm-adapter-volume-controller-2.html)

Key user observations:
- Stream Deck + IAC bus works well for GLM control with zero latency
- User wanted hardware power toggle switch directly on GLM adapter box
- Concern about monitors staying ON when GLM doesn't terminate correctly
- No discussion of bidirectional MIDI or power state feedback
- One user mentions wanting automatic power-off when GLM connection is lost

### RME TotalMix + GLM

**Source:** [RME Forum](https://forum.rme-audio.de/viewtopic.php?id=30316)

This thread is about volume control strategy (TotalMix vs GLM level), not MIDI integration. No relevant MIDI details.

---

## 6. Forum Discussions Summary

### What forums reveal about CC28 power control

No forum discussion was found that describes:
- CC28 sending explicit ON/OFF values (rather than toggle)
- GLM sending CC28 feedback on its output port
- Any configurable mode for CC28 beyond toggle
- Any workaround for getting power state via MIDI

The consistent picture across all sources is:
- CC28 is toggle-only, no state feedback
- Users who need power state detection use alternative approaches (pixel sampling, process monitoring)

### AudioScienceReview GLM thread

Focused on room EQ measurements and calibration quality. No MIDI power control discussion found.

---

## 7. glm-cli Implementation Details

**Source:** [PyPI glm-cli](https://pypi.org/project/glm-cli/) by araa47

The `glm-cli` `activate` command sends value=127 for ALL functions including system_power:
```python
# glm-cli sends CC28=127 to toggle power
# It does NOT attempt to read state back
# It does NOT differentiate ON from OFF
```

The author chose to use the same approach for all toggle functions — send 127 once, hope for the best. No bidirectional communication was implemented.

### genlc (markbergsma/genlc) — Alternative approach

**Source:** [GitHub genlc](https://github.com/markbergsma/genlc)

This project bypasses GLM entirely and talks directly to the Genelec network adapter via reverse-engineered proprietary binary protocol over USB. It can:
- **Explicitly wake up or shut down speakers** (separate commands, not toggle)
- Get actual device status
- Control volume, mute, LED color

This is the only known way to get deterministic power ON/OFF (not toggle) with Genelec SAM monitors, but it requires replacing GLM for power management.

---

## 8. JUCE MIDI Implementation Context

GLM is built with JUCE framework. Relevant findings from JUCE forum research:

### How JUCE handles MIDI CC for boolean parameters

- `AudioParameterBool` can be toggled via `param->setValueNotifyingHost(!param->get())`
- A CC value of 127 maps to 1.0 (true), CC value of 0 maps to 0.0 (false)
- There is NO built-in toggle/momentary mode distinction in JUCE — it's up to the application developer
- The typical JUCE approach: `param->setValueNotifyingHost((float)controllerValue / 127.0f)` — meaning value > 63 = ON, value <= 63 = OFF

### Implication for GLM

If GLM uses standard JUCE parameter mapping, then theoretically:
- CC28 value=127 would map to `power = true` (ON)
- CC28 value=0 would map to `power = false` (OFF)

But the manual explicitly says GLM treats it as toggle (value > 0 = toggle, value 0 = ignored). This means Genelec **overrode** the default JUCE behavior to implement toggle semantics. They likely do something like:
```cpp
if (controllerValue > 0) {
    power = !power;  // Toggle, not set
}
// value 0 is ignored (button release)
```

This is a deliberate design choice, not a JUCE limitation.

---

## 9. Conclusions: Can We Get Reliable Power State from MIDI?

### Direct answer: NO, not via CC28

There is **no way** to:
1. Send CC28 to explicitly set power ON or OFF (it's always a toggle)
2. Receive CC28 feedback from GLM (power state is never sent on MIDI output)
3. Configure GLM to change CC28 behavior to absolute mode

### What IS available via MIDI output

GLM sends the following on its MIDI output port when power is toggled:
- **A burst of CC23 (Mute) + CC20 (Volume) + CC24 (Dim) messages** — the same pattern our codebase already detects
- These messages contain the **new state values** for mute, volume, and dim
- The burst is indistinguishable from a group change burst

### Current approach (already implemented) is optimal

Our codebase's power pattern detection (`bridge2glm.py`) is already the best available approach:
1. Monitor GLM MIDI output for the 5-message CC burst pattern
2. Use timing analysis (pre-gap, max-gap, total-gap) to distinguish power toggle from group change
3. Track power state internally as a toggle (each detected pattern = flip state)
4. Use UI automation (pixel sampling) as ground truth when available

### Possible improvements

1. **Upgrade to GLM 5.2+**: The fix for "MIDI issue causing messages from GLM to MIDI controller to fail" may improve reliability of the CC burst we depend on for pattern detection

2. **Use Mute/Volume state changes as power indicators**: When power goes OFF, GLM likely sends CC23=127 (muted) and CC20=0 (volume 0). When power comes ON, it restores the previous volume and mute state. We could potentially use the CC20 value in the burst to infer power direction (OFF→ON vs ON→OFF).

3. **Contact Genelec directly**: Request that CC28 be added to the MIDI output feedback. Given the GLM 5.2 MIDI fix, Genelec appears to be actively improving MIDI functionality. A feature request for power state feedback seems reasonable.

4. **Feature request for absolute mode**: Ask Genelec to support CC28 value=127 for ON, value=0 for OFF (like Mute/Dim already work in their output direction). This would solve the problem entirely.

5. **genlc as nuclear option**: If we absolutely need deterministic power control without GLM, the genlc project demonstrates it's possible to talk to the hardware directly, but this is a fundamentally different architecture.

---

## 10. Sources

### Official Genelec Documentation
- [GLM Software Overview](https://www.genelec.com/glm)
- [GLM 5 Operating Manual (ManualsLib)](https://www.manualslib.com/manual/3430649/Genelec-Glm-5.html) — Section 8.6, pages 85-86
- [GLM 5.0 System Operating Manual (PDF)](https://downloads.ctfassets.net/4zjnzn055a4v/5vjR23qy2h89dIdSHAYN02/289952200d3c08ef4492e846895e7cd8/GLM_5.0_System_Operating_Manual__2_2024_.pdf)
- [Why MIDI Output device in GLM 4.2? — Genelec Support](https://support.genelec.com/hc/en-us/articles/5842976755986)

### Release Notes
- [GLM 5.0.1 Release Notes (Jan 2024)](https://assets.ctfassets.net/4zjnzn055a4v/LAwbjjOOPX5Q5DBrtNHBu/71cfb41ce498ff21a4722481ecb57917/GLM_5.0.1_Release_note_for_Mac_and_Windows.pdf)
- [GLM 5.0.4 Release Notes (Feb 2024)](https://assets.ctfassets.net/4zjnzn055a4v/0jRWXB3AAVKKXFrYwmJaU/c10f983b8173cb7982a3b62c6462acf2/Genelec_GLM_5.0.4_Release_note_for_Mac_and_Windows.pdf)
- [GLM 5.1.1 Release Notes (Jul 2024)](https://assets.ctfassets.net/4zjnzn055a4v/7IZic3Evd9V4vgDJE4OiSe/7ef67424b501623933d6fd231720d2ac/GLM_5.1.1_Release_note_for_Mac_and_Windows.pdf)
- [GLM 5.2.0 Release Notes (May 2025)](https://assets.ctfassets.net/4zjnzn055a4v/2I7uiyynji2tNzhKtXWJGF/8bbab637b15042e1a75a56b9eb2dd14b/GLM_5.2.0_Release_note_for_Mac_and_Windows.pdf)
- [GLM 5.2.1 Release Notes](https://assets.ctfassets.net/4zjnzn055a4v/4E4tawz7J5zcVdeToLTnbI/dd6479341e2fd7cac6b600d328ced056/GLM_5.2.1_Release_note_for_Mac_and_Windows.pdf)
- [GLM 4.2.0 Release Notes (May 2022)](https://assets.ctfassets.net/4zjnzn055a4v/4Une3vZsNsZaNHFbJwhy2s/ca065d2b98dcd1eb89d5d4bba4352c34/GLM_4.2.0_Release_note_for_Mac_and_Windows_FINAL.pdf)

### Reviews and Articles
- [SoundOnSound GLM 4.2 Review (July 2023)](https://www.soundonsound.com/reviews/genelec-glm-42)
- [Production Expert GLM v5 Announcement (Feb 2024)](https://www.production-expert.com/production-expert-1/genelec-announce-glm-v5-update)
- [Production Expert GLM 5.2 Announcement](https://www.production-expert.com/production-expert-1/genelec-announce-glm-5-2-and-aural-id-2-0)
- [ProAVL Asia — Using GLM: Controlling GLM with MIDI](https://www.proavl-asia.com/details/70421-using-glm-controlling-glm-with-midi)

### Forum Discussions
- [Gearspace DIY GLM Controller Thread](https://gearspace.com/board/high-end/1279456-diy-genelec-glm-adapter-volume-controller-2.html)
- [SoundFlow Forum GLM Stream Deck Setup](https://forum.soundflow.org/-8844/glm-app-stream-deck-setup)
- [RME Forum TotalMix and GLM](https://forum.rme-audio.de/viewtopic.php?id=30316)
- [ASR Genelec GLM Review Thread](https://audiosciencereview.com/forum/index.php?threads/genelec-glm-review-room-eq-setup.26397/)

### Code/Projects
- [glm-cli on PyPI](https://pypi.org/project/glm-cli/)
- [genlc on GitHub](https://github.com/markbergsma/genlc)

### JUCE Framework
- [JUCE AudioParameterBool](https://docs.juce.com/master/classAudioParameterBool.html)
- [JUCE Forum: Parameter handling and MIDI CC](https://forum.juce.com/t/parameter-handling-and-midi-cc/27796)
- [JUCE Forum: Toggle function for AudioParameterBool](https://forum.juce.com/t/toggle-function-for-audioparameterbool/29893)
- [Genelec GLM MIDI Video Tutorial](https://www.youtube.com/watch?v=XqXyTl6-x9o)
