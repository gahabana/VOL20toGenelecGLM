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
