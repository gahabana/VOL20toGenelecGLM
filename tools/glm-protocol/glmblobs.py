#!/usr/bin/env python3
"""Extract the per-speaker configuration GLM pushes after enumeration, as replayable frames.

usage: glmblobs.py CAPTURE.pcap [...] [--json OUT.json]

For every enumeration run (session assignments via `F0 02 hwid sess`) collect the unicast
frames GLM sends to each session, except telemetry polls (`08`). Frames are stored as the
unescaped payload after the address byte (command + data); `glmtalk.frame()` re-adds
address, CRC and escaping. Runs are compared by hash so drift between captures shows up.
"""
import hashlib
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import glmdecode as g  # noqa: E402

TERM = 0x7E


def out_frames(path):
    """Yield (ts, addr, payload) for every host->bus frame in a usbmon pcap."""
    for ts, d, rep, _ in g.hid_reports(g.read_pcap(path)):
        if d != "OUT" or not rep or not rep[0] & 0x80:
            continue
        fr = rep[1:1 + (rep[0] & 0x7F)]
        if not fr or fr[-1] != TERM:
            continue
        body = g.unescape(fr[:-1])[:-2]  # drop CRC
        if len(body) >= 2:
            yield ts, body[0], body[1:]


def runs(path):
    """Split a capture into enumeration runs: {hwid: [payload, ...]} plus a bus-wide sequence."""
    out, cur, sess = [], None, {}
    for ts, addr, pl in out_frames(path):
        if addr == 0xF0 and pl[0] == 0x02 and len(pl) == 5:
            hwid, s = pl[1:4].hex(), pl[4]
            if s in sess or (cur and hwid in cur["speakers"]):
                out.append(cur)
                cur, sess = None, {}
            if cur is None:
                cur = dict(start=ts, speakers={}, sequence=[])
            sess[s] = hwid
            cur["speakers"][hwid] = []
        if cur is None:
            continue
        if addr in sess and pl[0] != 0x08:
            cur["speakers"][sess[addr]].append(pl.hex())
        if pl[0] not in (0x08, 0xFE, 0x1F):
            cur["sequence"].append(f"{addr:02x} {pl.hex()}")
    if cur:
        out.append(cur)
    return out


def digest(frames):
    return hashlib.sha256("\n".join(frames).encode()).hexdigest()[:12]


def main(argv):
    want_json = None
    if "--json" in argv:
        i = argv.index("--json")
        want_json = argv[i + 1]
        argv = argv[:i] + argv[i + 2:]
    result = {}
    for path in argv:
        for n, r in enumerate(runs(path)):
            print(f"{os.path.basename(path)} run {n} @ {r['start']:.3f}: {len(r['sequence'])} frames")
            for hwid, fr in r["speakers"].items():
                cmds = sorted({f[:2] for f in fr})
                print(f"  {hwid}: {len(fr):3d} frames  sha {digest(fr)}  cmds {' '.join(cmds)}")
                result.setdefault(hwid, {})[digest(fr)] = fr
    if want_json:
        with open(want_json, "w") as fh:
            json.dump(result, fh, indent=1)
        print(f"wrote {want_json}: {sum(len(v) for v in result.values())} distinct blobs")


if __name__ == "__main__":
    main(sys.argv[1:])
