# GLM 5 ↔ GLM Net adapter ↔ SAM speakers: protocol notes

Observed 2026-10-29 on this setup by capturing USB traffic on the host (`usbmon`) between
the Windows VM running GLM 5.2.0 and the GLM Net adapter (firmware 1.4.0.6113). Speakers:
two 8330A and one 7350A, firmware 1.14.7.6281. Bus-level command semantics follow the
public spec in [robcazzaro/hlm](https://github.com/robcazzaro/hlm) (`docs/Genelec GLM
Protocol.13.md`); this file records what GLM 5.2 actually does on the wire, and where it
differs. Tools: `tools/glm-protocol/`.

These are observations, not Genelec documentation. Nothing here is ported from the GPL
projects (genlc, hlm, espgensam); only protocol facts are used.

## USB layer

- Adapter: USB `1781:0e39`, full speed, HID vendor page `0xFFA0`, 64-byte interrupt IN
  (EP1) and OUT (EP3), 1-byte feature report, no report IDs.
- **Host → adapter**: one output report: `0x80 | len`, then the bus frame (escaped), zero
  padded. hidapi's leading `0x00` is the report-ID byte, not data.
- **Adapter → host**: input report `0x3F`, then the bus frame directly (address, payload,
  CRC, `7E`). A reply may span up to three reports.
- Bus frame: `addr cmd data… CRC16-BE 7E`. CRC-16/GSM over `addr…data`. HDLC escaping of
  `7E`/`7D` after the CRC. Speaker and adapter replies both use address `01`.
- Adapter timeout marker: reply `01 7E` (payload is a bare `7E`) means nobody answered.

## Idle cycle (GLM connected, speakers on)

Every ~545 ms:

1. `FF FE` discovery probe → `01 7E` when no unassigned speaker exists
2. `FF 1F vvvvvv` volume broadcast, current level, `floor(10^(dB/20)·2^23)`
3. `ss 08` telemetry poll to each session → bare ACK `01 09` or telemetry `01 09 41 …`

Independently, every 100 ms, **two** polls 2 ms apart: `01 08` to the adapter itself →
`01 09 33 31`. This is the adapter's event queue (see remote). No `04` keep-alive is sent
while idle; `04` appears only during enumeration.

Telemetry TLVs seen beyond the spec: `49` (1 byte, counter that resets to 1 on wake,
increments ~1/s) and `8A` (2 bytes, slow counter). Telemetry is occasionally split over two
consecutive polls (`41 temp 81 83` half, then `42…84` half). Input-meter silence floor is
`0x80` on these units.

## Remote control (9101 RF)

The remote talks to the adapter. GLM learns about it through the 10 Hz `01 08` poll:

| Adapter reply | Meaning |
|---|---|
| `01 09 33 31` | idle |
| `01 09 33 31 DC 00 08 80 00` | volume down step |
| `01 09 33 31 DC 00 08 00 00` | volume up step |
| `01 09 33 31 DC 00 06 80 00` | mute toggle |

The event is returned on the first poll of each pair; the second poll reads idle. The
remote auto-repeats (3–4 events per press). GLM then sends its own `FF 1F` (±0.5 or 1 dB)
or mute frames within ~4 ms. The adapter does not change speaker volume itself while a host
is polling it. The same poll works from Linux via hidraw with GLM absent (verified).

## Volume, dim, mute

- Volume: `FF 1F` broadcast immediately per 0.5 dB UI step, plus the cyclic broadcast.
  No ACK, no retry.
- Dim on: `FF 1F (level−20 dB)` twice, 1 ms apart; ~100 ms later one round of `2B 04` and
  `3D 00 00` to every speaker. Dim off: same with the restored level.
- Mute all: `FF 1F` re-broadcast, then **`01 2B 01` to the adapter**, then `2B 03` to each
  speaker, two rounds. Unmute all: `01 2B 00`, then `2B 04` ×2 rounds.
- Single mute: target `2B 03`, others `2B 04` (sync), two rounds, no adapter frame.
  "Play all": `2B 04` to all ×2, no adapter frame.
- Mute is invisible in telemetry.

## Standby and wake

- Standby: `FF 3A 03 02`, 90 ms, `FF 3A 03 00`; same pair again ~0.8 s later
  (order 02,00,02,00). Speakers report `47 02` and flags `03 xx` within 0.6 s; polling,
  probing and volume broadcasts continue; sessions are kept.
- Wake: `FF 3A 03 7F` + `FF 3A 03 01`, three pairs 12 ms apart. `3D` to the old sessions
  times out: speakers dropped their sessions. GLM re-discovers (`FE` → HW ID → `F0 02 hwid
  sess`, new IDs) and pushes the full configuration (below). Speakers live ~1.7 s after the
  first wake frame.

## GLM start and enumeration

On start GLM opens the HID device (`SET_IDLE`, string descriptors), then:

1. Adapter identity: `01 22` → `GLM Adapter 0 0000 0104 06113`; `01 39` →
   `c-0;model-GLM Adapter;ver-1.4.0.6113;hw-0.0.64;build-2025.05.19.s`; `01 19 01` →
   serial `H99999F000`
2. Wake pairs ×3; `01 51 82` → ASCII `202026` (meaning unknown)
3. `FF 2D 00`, `FF 3D 00 00`, then **the GLM startup-volume setting as `FF 1F`**, before
   any speaker is discovered. GLM, not the speakers, applies the startup level.
4. Discovery from session `02`, then per speaker: `39`, `04`, `05 04 00 …` (generator off),
   `17 01`, `10 02` delay, `10 01 00` level, `10 01 09 000000`, `10 0E` PEQ ×20, `3B`
   crossover, `3D`, `40` routing, `39`, `19 01`. The subwoofer block additionally has `3C`,
   `3E`, `42` and is sent three times over.
5. `3D` rounds, `2B 04` round, idle cycle begins. ~2.7 s from device open to telemetry.

GLM quit sends nothing; the pending IN URB is just cancelled. Adapter then runs standalone
(RS-485 only, invisible on USB). GLM never reads volume back; on reconnect it re-broadcasts
its own value.

## This system's configuration as pushed by GLM

| Session (fresh start) | HW ID | Model, serial | Level | Delay | XO | Input (`40`) |
|---|---|---|---|---|---|---|
| 02 | 0f1f03 | 8330A 8330apm1002181 (R) | −7.6 dB | 0 | 60 Hz | `00 02 00 02` AES3 B |
| 03 | 10e752 | 7350A 7350APM61003048 | 0 dB | 267 samples | 60 Hz | `00 02 00 03` sum, `01 02 00 00` |
| 04 | 0f1f02 | 8330A 8330apm1002180 (L) | −8.0 dB | 0 | 60 Hz | `00 02 00 01` AES3 A |

Session IDs change on every wake and every GLM start. Anything addressing speakers directly
must re-discover after a power toggle.

## Capturing again

Host side, no USBPcap in the VM needed (bus 7 holds only the adapter):

```sh
sudo modprobe usbmon
sudo tcpdump -i usbmon7 -U -s 0 -w /var/tmp/glm-capture/glm.pcap -Z $USER
python3 -I tools/glm-protocol/glmdecode.py /var/tmp/glm-capture/glm.pcap --summary
python3 -I tools/glm-protocol/glmdecode.py /var/tmp/glm-capture/glm.pcap | less
```

To talk to the adapter from the host, detach it from the VM, then use `glmtalk.py`:

```sh
virsh detach-device glmv5 glmnet.xml --live   # hostdev xml with vendor 0x1781 product 0x0e39
sudo chmod 666 /dev/hidrawN                   # the new hidraw node
python3 -I tools/glm-protocol/glmtalk.py /dev/hidrawN ident
python3 -I tools/glm-protocol/glmtalk.py /dev/hidrawN poll 60
virsh attach-device glmv5 glmnet.xml --live
```

GLM shows all speakers offline while detached and reconnects by itself on reattach. The
adapter applies its own standalone level to the speakers while no host polls it.

## Inside GLMv5.exe (Ghidra 12.1.4 via REA, GLM 5.2.0 x64, sha256 5440da85…)

Function addresses are from the shipped binary; names are ours.

| Address | Role |
|---|---|
| `FUN_14031b0f0` | HID writer. Splits a bus frame into 64-byte output reports: intermediate reports carry header `0x3E` and 62 payload bytes, the last carries `0x80 | remaining`. hidapi's report-ID byte is prepended. |
| `FUN_14031b420` | HID reply reader. `hid_read_timeout` 40 ms per report (300 ms after command `15` commit), appends report bytes until `7E`. |
| `FUN_1402ce9e0` | Frame sender. Appends `7E`, computes CRC-16 from a table and inverts it (CRC-16/GSM), then escapes `7E`/`7D` **starting at byte index 2**: the address and command bytes are never escaped. Sends under a mutex, then reads and unescapes the reply unless the frame was a broadcast other than `FE`. ~270 call sites, one per command builder. |
| `FUN_1402cd780` | Adapter detection. Sends `01 08`, requires reply address `01`, retries once on failure (the doubled poll seen on the wire), then sends `01 22` and classifies the adapter from the reply text: `G…` → type 5 (`GLM Adapter`), `9…` → type 6, otherwise type 4 (legacy). |
| `FUN_1402a82e0` | Periodic adapter poll. For adapter types other than 6 sends `01 08`; on a `09` reply calls the status decoder below. |
| `FUN_1402a2290` | **Adapter status / remote event decoder** (below). |

### Adapter `01 08` reply format, as GLM decodes it

Payload after `09` is a stream of tags up to the CRC. A tag with bits `0xC0` clear is a
one-byte flag; any other tag is followed by a 4-byte big-endian signed value.

| Tag | Meaning in GLM |
|---|---|
| `30` / `31` | Clears/sets a mode flag. While `30` is active a remote volume press is treated as **ChangeGroup** instead of Volume; `31` returns to volume mode. Idle replies carry `31`. |
| `32` / `33` | Second flag; `33` also zeroes the value last received with `DE`. Idle replies carry `33`. |
| `DF` + int32 | Knob/level position, clamped below −1200 to −1300 (0.1 dB units, so −120.0 dB floor). Change notifies the UI. |
| `DD` + int32 | Stored level (default −120). |
| `DE` + int32 | Value cleared by `33`; purpose unknown. |
| `DC` + int32 | **Remote event**. GLM reads the adapter firmware number (from the `22`/`39` identity reply) and uses the table below when it is ≥ 3000; older adapters send a signed step instead. |

Remote event values for firmware ≥ 3000 (adapter 1.4.0.6113 qualifies):

| Value | Event |
|---|---|
| `0x00080000`…`0x00080003` | Volume + |
| `0x00088000` | Volume − |
| `0x00068000` | Mute toggle |
| `0x00060000` | Power |
| `0x00100000` / `0x00900000` | ChangeGroup + |
| `0x00108000` / `0x00908000` | ChangeGroup − |

Captured bytes match: `DC 00 08 80 00` = Volume −, `DC 00 08 00 00` = Volume +,
`DC 00 06 80 00` = Mute. Power and group events exist on the remote but were not pressed.

Volume acceleration is GLM-side: consecutive volume events are counted and the step is
1, 1, then 2 units for presses 3–7, 10 for 8–12, 20 from 13 on, in 0.5 dB units. That is
why the capture shows 0.5 dB and 1 dB steps.

## Input meter with programme material (2026-10-29, TV via AES3)

149 s capture, TV audio playing. Tag `42` per speaker: `0xCF`–`0xF8`, median `0xF1`. Silent
baseline (23 min, AES3 carrier on, digital zeros): `0x80` on every sample, no exceptions. The
meter reports decoded content, not link state, so `42 > 0x80` on any speaker is a reliable
"signal present" test for a digital feed. While audio plays every poll returns telemetry
(~1 frame/s per speaker); the sub's `46` meter stayed at `0x80` throughout.

## Compared with prior art

hlm spec v13 already cross-validates espgensam, so one column covers both. genlc is the
Python USB-side client.

| Item | genlc | hlm/espgensam | This work |
|---|---|---|---|
| OUT report `0x80\|len`, intermediate `0x3E` | single report only | n/a (bus side) | from Ghidra writer |
| IN report `0x3F` + raw frame; `01 7E` = timeout | extra "response code" byte | n/a | corrected |
| Escaping scope | everything after address | payload + CRC | from byte index 2, command byte never escaped |
| `01 08` reply tag stream (`30/31/32/33`, `DC/DD/DE/DF`) | bytes 3–5 read as mic SPL | not covered | full decoder |
| RF remote events (`DC` values), fw ≥ 3000 branch, GLM-side acceleration | – | – (knob path listed unknown) | new |
| 10 Hz doubled adapter poll | – | – | new |
| `01 51 82` → "202026" | `51 82 44` as mic serial | – | observed, meaning unknown |
| `01 2B 01/00` to adapter on mute-all | – | – | new |
| `04` keep-alive | sent | every 1.2–1.8 s in OEM traffic | none while idle (GLM 5.2), only in enumeration |
| Dim | – | plain `1F −20 dB` | `1F` ×2, then `2B 04` + `3D 00 00` round |
| Standby timing | 02,00 each ×2 | `02`×2 then `00`×2, ~22 ms | `02`, 90 ms, `00`; pair repeated 0.8 s later |
| Sub config block sent 3× | – | – | new |
| Tag `42` floor | – | `0x89` (8320A) | `0x80` (8330A/7350A fw 1.14.7) |
| Tags `49`, `8A` | – | – | new |
| Telemetry split over two polls | – | – | new |
| GLM startup volume before discovery; never reads volume back | – | – | new |
| GLM quit sends nothing | – | bus-side hub takeover after 2.6 s | consistent |
| Software ISS in the app ("fires standby after silence") | – | claimed | not tested here |
