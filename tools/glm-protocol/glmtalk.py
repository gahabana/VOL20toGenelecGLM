#!/usr/bin/env python3
"""Talk to the GLM Net adapter over /dev/hidrawN using GLM's own framing.

usage: glmtalk.py /dev/hidrawN ident          # adapter 22 / 39 / 19 01 queries
       glmtalk.py /dev/hidrawN poll [SECONDS] # 10 Hz 01 08 poll, print non-idle replies
       glmtalk.py /dev/hidrawN raw ADDR CMD [DATA_HEX]   # one frame to the bus, print reply

Only sends to the adapter (addr 01) unless you use `raw`. Nothing here writes speaker flash.
"""
import os
import select
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import glmdecode as g  # noqa: E402


def frame(addr: int, payload: bytes) -> bytes:
    body = bytes([addr]) + payload
    f = body + g.crc16_gsm(body).to_bytes(2, "big")
    f = f.replace(b"\x7d", b"\x7d\x5d").replace(b"\x7e", b"\x7d\x5e")
    return f + b"\x7e"


def send(fd: int, addr: int, payload: bytes) -> None:
    f = frame(addr, payload)
    rep = bytes([0x80 | len(f)]) + f
    os.write(fd, b"\x00" + rep.ljust(64, b"\x00"))  # leading 0x00 = hidraw report-id byte


def recv(fd: int, timeout=0.5) -> bytes:
    """Collect reports until a frame ends with 7E; return the bus frame (after 0x3F hdr)."""
    buf = b""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r, _, _ = select.select([fd], [], [], max(0, deadline - time.monotonic()))
        if not r:
            break
        rep = os.read(fd, 64)
        buf += rep[1:]
        s = buf.rstrip(b"\x00")
        if s.endswith(b"\x7e"):
            return s
    return buf.rstrip(b"\x00")


def ask(fd, addr, payload, from_host=False):
    send(fd, addr, payload)
    r = recv(fd)
    return r, (g.parse_frame(r, from_host) if r else "<no reply>")


def main():
    dev, cmd = sys.argv[1], sys.argv[2]
    fd = os.open(dev, os.O_RDWR)
    if cmd == "ident":
        for p in (b"\x22", b"\x39", b"\x19\x01", b"\x51\x82"):
            r, d = ask(fd, 0x01, p)
            print(f"01 {p.hex(' ')} -> {r.hex(' ')}\n    {d}")
    elif cmd == "poll":
        secs = float(sys.argv[3]) if len(sys.argv) > 3 else 30
        end = time.monotonic() + secs
        n = 0
        print("polling adapter; press remote buttons")
        while time.monotonic() < end:
            r, d = ask(fd, 0x01, b"\x08")
            n += 1
            if r != bytes.fromhex("0109333161 6e7e".replace(" ", "")):
                print(f"{time.strftime('%H:%M:%S')} {r.hex(' ')}  {d}")
            time.sleep(0.1)
        print(f"{n} polls")
    elif cmd == "raw":
        addr, c = int(sys.argv[3], 16), int(sys.argv[4], 16)
        data = bytes.fromhex(sys.argv[5]) if len(sys.argv) > 5 else b""
        r, d = ask(fd, addr, bytes([c]) + data)
        print(f"{r.hex(' ')}\n    {d}")
    else:
        raise SystemExit(__doc__)


if __name__ == "__main__":
    assert frame(0xFF, b"\x04") == bytes.fromhex("ff04bc847e")
    main()
