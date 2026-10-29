#!/usr/bin/env python3
"""Decode a usbmon pcap of GLM <-> GLM Net adapter traffic into GLM bus frames.

usage: glmdecode.py CAP.pcap [--since EPOCH] [--summary] [--raw] [--dev N]
       glmdecode.py --self-test

Reads linktype 220 (USB_LINUX_MMAPPED) or 189 (USB_LINUX). Takes interrupt
transfers on the adapter (HID OUT = host->adapter in the Submit event, HID IN =
adapter->host in the Complete event), reassembles replies that span up to three
64-byte reports, unescapes HDLC, checks CRC-16/GSM, names the command per hlm
spec rev 13. Control transfers are printed raw (enumeration / report descriptor).
"""
import struct
import sys
import time
from collections import OrderedDict

TERM = 0x7E

CMD = {  # hub -> speakers, hlm spec §7 + genlc const.py
    0x02: "ASSIGN_SESSION_ID", 0x03: "SOFT_RESET", 0x04: "KEEPALIVE",
    0x05: "TEST_SIGNAL", 0x08: "POLL_TELEMETRY", 0x09: "ACK?", 0x0D: "SET_INPUT",
    0x10: "DSP_PARAM", 0x15: "COMMIT_FLASH", 0x17: "PREPARE_CONFIG",
    0x19: "SERIAL_REQ", 0x1F: "SET_VOLUME", 0x22: "HARDWARE_QUERY",
    0x2B: "MUTE_LED", 0x2D: "INPUT_PREAMBLE?", 0x39: "SYSINFO_REQ",
    0x3A: "POWER_STANDALONE", 0x3B: "BASS_MGMT_XO", 0x3C: "SUB_UNK_3C",
    0x3D: "INPUT_SYNC", 0x3E: "SUB_UNK_3E", 0x40: "INPUT_ROUTING",
    0x42: "SUB_UNK_42", 0x51: "MIC_SERIAL?", 0xFE: "DISCOVERY_PROBE",
}
DSP_SUB = {0x0E: "PEQ", 0x02: "DELAY", 0x01: "LEVEL"}
TLV_LEN = {0x42: 1, 0x43: 1, 0x44: 1, 0x45: 1, 0x46: 1, 0x47: 1, 0x49: 1, 0x81: 2, 0x83: 2, 0x84: 2}
TLV_NAME = {0x42: "in_meter", 0x43: "hf_meter", 0x44: "mid_meter", 0x45: "lf_meter",
            0x46: "sub_meter", 0x47: "power", 0x49: "tag49_UNDOC", 0x81: "sensor81", 0x83: "sensor83", 0x84: "flags"}


def crc16_gsm(data: bytes) -> int:
    crc = 0
    for b in data:
        crc ^= b << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) if crc & 0x8000 else crc << 1
            crc &= 0xFFFF
    return crc ^ 0xFFFF


def unescape(b: bytes) -> bytes:
    return b.replace(b"\x7d\x5e", b"\x7e").replace(b"\x7d\x5d", b"\x7d")


def addr_name(a: int) -> str:
    return {0xFF: "BCAST", 0xF0: "MCAST", 0x01: "ADAPTER/SPK"}.get(a, f"spk{a:02X}")


def vol_db(v: int) -> str:
    import math
    return "-inf" if v == 0 else f"{20 * math.log10(v / 2 ** 23):.1f}dB"


def describe(addr: int, payload: bytes, from_host: bool) -> str:
    """payload = bytes after address, before CRC (unescaped)."""
    if not payload:
        return "<empty>"
    c, d = payload[0], payload[1:]
    if from_host:
        name = CMD.get(c, f"CMD_{c:02X}")
        extra = ""
        if c == 0x1F and len(d) == 3:
            extra = f" {vol_db(int.from_bytes(d, 'big'))}"
        elif c == 0x10 and d:
            sub = DSP_SUB.get(d[0], f"sub{d[0]:02X}")
            if d[0] == 0x0E and len(d) >= 22:
                idx = d[1]
                fl = struct.unpack("<5f", d[2:22])
                extra = f" {sub}[{idx}] b0={fl[0]:.5g} b1={fl[1]:.5g} b2={fl[2]:.5g} a1={fl[3]:.5g} a2={fl[4]:.5g} flag={d[22] if len(d) > 22 else '?'}"
            elif d[0] == 0x01 and len(d) >= 5:
                extra = f" {sub} sub={d[1]:02X} {vol_db(int.from_bytes(d[2:5], 'big'))}"
            elif d[0] == 0x02 and len(d) >= 5:
                extra = f" {sub} {int.from_bytes(d[1:5], 'big')} samples@48k"
            else:
                extra = f" {sub}"
        elif c == 0x3A and len(d) >= 2:
            extra = {0x03: {0x02: " STANDBY_PREP", 0x00: " POWER_OFF", 0x7F: " WAKE_7F", 0x01: " WAKE_01"}.get(d[1], ""),
                     0x01: " ISS_DELAY", 0x02: " ISS_SENS", 0x06: " LED_STATE"}.get(d[0], "")
        elif c == 0x2B and d:
            extra = f" mute={d[0] & 1} led={(d[0] >> 1) & 3} flash={(d[0] >> 3) & 1}"
        elif c == 0x02 and len(d) == 4:
            extra = f" hwid={d[:3].hex()} -> session {d[3]:02X}"
        return f"{name} {d.hex(' ')}{extra}".rstrip()
    # speaker/adapter -> host
    if c == TERM and not d:
        return "BUS_TIMEOUT (no device answered)"
    if c != 0x09:
        return f"RESP_{c:02X} {d.hex(' ')}"
    if not d:
        return "ACK"
    if d[0] in (0x06, 0x07) and len(d) == 1:
        return f"BUSY_MARKER {d[0]:02X}"
    body = d[1:] if d[0] in (0x06, 0x07) and len(d) > 1 else d
    if body and body[0] == 0x41:
        out, i = [f"temp={body[1]}C" if len(body) > 1 else "temp=?"], 2
        while i < len(body):
            tag = body[i]
            n = TLV_LEN.get(tag)
            if n is None:
                out.append(f"?{body[i:].hex()}")
                break
            val = body[i + 1:i + 1 + n]
            out.append(f"{TLV_NAME[tag]}={val.hex()}")
            i += 1 + n
        return "TELEMETRY " + " ".join(out)
    if body and body[0] == 0x63:
        return "SYSINFO " + body[1:].split(b"\0")[0].decode("ascii", "replace")
    if len(body) == 1:
        return f"SESSION_ACK session={body[0]:02X}"
    if len(body) == 3:
        return f"HWID {body.hex()}"
    if all(32 <= x < 127 for x in body) and len(body) >= 8:
        return f"SERIAL {body.decode('ascii')}"
    return f"RESP09 {body.hex(' ')}"


def parse_frame(raw: bytes, from_host: bool) -> str:
    """raw = escaped bus frame incl. trailing 0x7E."""
    if not raw or raw[-1] != TERM:
        return f"NOTERM {raw.hex(' ')}"
    f = unescape(raw[:-1])
    if len(f) < 3:
        return f"SHORT {raw.hex(' ')}"
    body, crc = f[:-2], int.from_bytes(f[-2:], "big")
    ok = crc16_gsm(body) == crc
    return f"[{addr_name(body[0])}] {describe(body[0], body[1:], from_host)}" + ("" if ok else f" CRC_BAD(got {crc:04X} want {crc16_gsm(body):04X})")


# ---------------------------------------------------------------- pcap reading
def read_pcap(path, since=0.0):
    with open(path, "rb") as fh:
        data = fh.read()
    magic = struct.unpack("<I", data[:4])[0]
    if magic in (0xA1B2C3D4, 0xA1B23C4D):
        o = "<"
    elif magic in (0xD4C3B2A1, 0x4D3CB2A1):
        o = ">"
    else:
        raise SystemExit("not a pcap file (pcapng not supported; use tcpdump -w)")
    nanos = magic in (0xA1B23C4D, 0x4D3CB2A1)
    linktype = struct.unpack(o + "I", data[20:24])[0]
    if linktype not in (189, 220):
        raise SystemExit(f"linktype {linktype} is not usbmon (189/220)")
    hdrlen = 64 if linktype == 220 else 48
    off = 24
    while off + 16 <= len(data):
        sec, frac, caplen, _ = struct.unpack(o + "IIII", data[off:off + 16])
        off += 16
        pkt = data[off:off + caplen]
        off += caplen
        if len(pkt) < hdrlen:
            continue
        ts = sec + frac / (1e9 if nanos else 1e6)
        if ts < since:
            continue
        (urb_id, ev, xfer, ep, dev, bus, fsetup, fdata, tsec, tusec, status, length, lencap) = struct.unpack(
            "<QBBBBHBBqiiII", pkt[:40])
        setup = pkt[40:48]
        payload = pkt[hdrlen:hdrlen + lencap] if fdata == 0 else b""
        yield dict(ts=ts, ev=chr(ev), xfer=xfer, ep=ep & 0x7F, dirin=bool(ep & 0x80), dev=dev, bus=bus,
                   status=status, length=length, setup=setup if fsetup == 0 else None, data=payload)


def hid_reports(events, dev=None):
    """Yield (ts, 'OUT'|'IN'|'CTRL', bytes, note) for the adapter only."""
    for e in events:
        if dev is not None and e["dev"] != dev:
            continue
        if e["xfer"] == 2 and e["setup"] is not None:  # control submit
            yield e["ts"], "CTRL", e["data"], f"setup={e['setup'].hex(' ')}"
        elif e["xfer"] == 1:  # interrupt
            if not e["dirin"] and e["ev"] == "S" and e["data"]:
                yield e["ts"], "OUT", e["data"], ""
            elif e["dirin"] and e["ev"] == "C" and e["data"]:
                yield e["ts"], "IN", e["data"], ""
            elif e["dirin"] and e["ev"] == "C" and e["status"] not in (0, -115):
                yield e["ts"], "IN", b"", f"status={e['status']}"


def frames(reports):
    """Turn HID reports into (ts, dir, raw_report_hex, decoded)."""
    inbuf, in_ts = b"", None
    for ts, d, rep, note in reports:
        if d == "CTRL":
            yield ts, d, rep.hex(" "), note
            continue
        if d == "OUT":
            # qlm/genlc: first byte 0x80+len (hidapi strips its report-id 0x00), then escaped frame
            if rep and rep[0] & 0x80 and (rep[0] & 0x7F) <= len(rep) - 1:
                n = rep[0] & 0x7F
                fr = rep[1:1 + n]
                dec = parse_frame(fr, True) if fr and fr[-1] == TERM else f"OUT_RAW len={n} {fr.hex(' ')}"
            else:
                body = rep.rstrip(b"\0")
                dec = parse_frame(body, True) if body and body[-1] == TERM else f"OUT_RAW {body.hex(' ')}"
            yield ts, d, rep.rstrip(b"\0").hex(" "), dec
            continue
        # IN: 64-byte reports, first byte is report length/kind (genlc: "63"), reply spans <=3 reports
        if not rep:
            yield ts, d, "", note
            continue
        hdr, body = rep[0], rep[1:]
        if in_ts is None:
            in_ts = ts
        inbuf += body
        stripped = inbuf.rstrip(b"\0")
        if stripped.endswith(b"\x7e") or len(inbuf) > 3 * 63:
            # observed: after the 0x3F header the bus frame follows directly: addr(01) payload CRC 7E
            if stripped and stripped[-1] == TERM:
                dec = parse_frame(stripped, False)
            else:
                dec = f"IN_RAW {stripped.hex(' ')}"
            yield in_ts, d, f"hdr={hdr:02X} " + stripped.hex(" "), dec
            inbuf, in_ts = b"", None


def summarize(rows):
    groups = OrderedDict()
    for ts, d, raw, dec in rows:
        key = (d, dec.split(" CRC_BAD")[0])
        # collapse telemetry/meter values so periodic polls group together
        if key[1].startswith("TELEMETRY "):
            key = (d, "TELEMETRY " + " ".join(p.split("=")[0] for p in key[1].split()[1:]))
        g = groups.setdefault(key, [0, ts, ts])
        g[0] += 1
        g[2] = ts
    for (d, dec), (n, t0, t1) in sorted(groups.items(), key=lambda kv: -kv[1][0]):
        span = t1 - t0
        rate = f" every {span / (n - 1):.2f}s" if n > 1 and span > 0 else ""
        print(f"{n:6d}x {d:4s} {dec}{rate}  [{time.strftime('%H:%M:%S', time.localtime(t0))}..{time.strftime('%H:%M:%S', time.localtime(t1))}]")


def self_test():
    assert crc16_gsm(bytes.fromhex("ff04")) == 0xBC84, hex(crc16_gsm(bytes.fromhex("ff04")))
    assert crc16_gsm(bytes.fromhex("0109")) == 0x5DE7
    assert parse_frame(bytes.fromhex("ff04bc847e"), True) == "[BCAST] KEEPALIVE"
    assert parse_frame(bytes.fromhex("01095de77e"), False) == "[ADAPTER/SPK] ACK"
    v = bytes.fromhex("ff1f0ccccc")
    fr = v + crc16_gsm(v).to_bytes(2, "big") + b"\x7e"
    assert "SET_VOLUME 0c cc cc -20.0dB" in parse_frame(fr, True), parse_frame(fr, True)
    t = bytes.fromhex("0109412681002483002442e243af45cb47018402b0")
    fr = t + crc16_gsm(t).to_bytes(2, "big") + b"\x7e"
    out = parse_frame(fr, False)
    assert "TELEMETRY temp=38C sensor81=0024 sensor83=0024 in_meter=e2 hf_meter=af lf_meter=cb power=01 flags=02b0" == out.split("] ", 1)[1], out
    # escaping round trip
    assert unescape(b"\x7d\x5e\x7d\x5d") == b"\x7e\x7d"
    print("self-test OK")


def main(argv):
    if "--self-test" in argv:
        return self_test()
    path = argv[1]
    since = float(argv[argv.index("--since") + 1]) if "--since" in argv else 0.0
    dev = int(argv[argv.index("--dev") + 1]) if "--dev" in argv else None
    rows = list(frames(hid_reports(read_pcap(path, since), dev)))
    if "--summary" in argv:
        return summarize(rows)
    for ts, d, raw, dec in rows:
        t = time.strftime("%H:%M:%S", time.localtime(ts)) + f".{int((ts % 1) * 1000):03d}"
        if "--raw" in argv:
            print(f"{t} {d:4s} {raw}\n{'':17s} -> {dec}")
        else:
            print(f"{t} {d:4s} {dec}")


if __name__ == "__main__":
    main(sys.argv)
