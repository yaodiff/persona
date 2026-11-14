#!/usr/bin/env python3
"""Fail when a view function in a Debug iOS build reserves a big fixed stack frame.

An iPhone's main thread has a 1 MB stack (the simulator's has 8 MB), and a Debug build does not reuse stack slots, so a
SwiftUI function whose frame is tens of kilobytes leaves little room for the rest of a render (#6735). This reads the
arm64 Mach-O of a Debug build (`Persona.debug.dylib`, or the app binary when there is none) and, for every function
whose mangled name mentions SwiftUI or returns an opaque type (`some View`), adds up the `sub sp, sp, #imm` and the
`___chkstk_darwin` probe in its prologue: the frame it reserves on entry.

Scope: only the fixed part of a frame. Slots sized at run time (opaque intermediates of a generic modifier chain, which
caused #6735) are invisible here; `apple/PersonaTests/MainPagesStackTests.swift` measures those by rendering the pages.

A function over the threshold (64 KB) fails unless it is in the baseline (scripts/ci/apple_stack_frames_baseline.json),
and a baselined function may not grow beyond its recorded size plus 1 KB (toolchain noise). The baseline only goes
down: --update lowers sizes and drops functions now under the threshold; it never adds or raises one. Fixing a function:
docs/lessons/ios.md ("A Debug build on an iPhone has a 1 MB main-thread stack").

Usage: apple_stack_frames.py <Persona.app or binary> [--baseline FILE] [--threshold BYTES] [--update] [--json]
The last line is the verdict.
"""
import argparse
import array
import json
from pathlib import Path
import struct
import subprocess
import sys

THRESHOLD = 64 * 1024
SLACK = 1024
PROLOGUE = 64  # instructions searched for the frame setup
BASELINE_REL = "scripts/ci/apple_stack_frames_baseline.json"

MH_MAGIC_64 = 0xFEEDFACF
CPU_TYPE_ARM64 = 0x0100000C
LC_SEGMENT_64 = 0x19


class FrameError(Exception):
    """The check could not run (not an arm64 Mach-O, no symbols, broken baseline)."""


def text_section(data):
    """(address, bytes) of __TEXT,__text in a thin arm64 Mach-O."""
    if len(data) < 32:
        raise FrameError("not a Mach-O file")
    magic, cputype = struct.unpack_from("<II", data, 0)
    if magic != MH_MAGIC_64 or cputype != CPU_TYPE_ARM64:
        raise FrameError("not a thin arm64 Mach-O (magic %#x, cpu %#x)" % (magic, cputype))
    ncmds = struct.unpack_from("<I", data, 16)[0]
    off = 32
    for _ in range(ncmds):
        cmd, size = struct.unpack_from("<II", data, off)
        if cmd == LC_SEGMENT_64:
            nsects = struct.unpack_from("<I", data, off + 64)[0]
            for i in range(nsects):
                s = off + 72 + i * 80
                if data[s:s + 16].rstrip(b"\0") == b"__text" and data[s + 16:s + 32].rstrip(b"\0") == b"__TEXT":
                    addr, sz = struct.unpack_from("<QQ", data, s + 32)
                    foff = struct.unpack_from("<I", data, s + 48)[0]
                    return addr, data[foff:foff + sz]
        off += size
    raise FrameError("no __TEXT,__text section")


def frame_size(words):
    """Bytes a function reserves on entry, from its first instructions (`words`, little-endian arm64)."""
    total = 0
    x15 = None
    for w in words:
        if (w & 0xFF8003FF) == 0xD10003FF:  # sub sp, sp, #imm{, lsl #12}
            total += ((w >> 10) & 0xFFF) << (12 if (w >> 22) & 1 else 0)
        elif (w & 0xFF80001F) == 0xD280000F:  # movz x15, #imm16{, lsl #hw*16}
            x15 = ((w >> 5) & 0xFFFF) << (16 * ((w >> 21) & 3))
        elif (w & 0xFF80001F) == 0xF280000F and x15 is not None:  # movk x15, #imm16, lsl #hw*16
            shift = 16 * ((w >> 21) & 3)
            x15 = (x15 & ~(0xFFFF << shift)) | (((w >> 5) & 0xFFFF) << shift)
        elif w == 0xCB2F73FF and x15 is not None:  # sub sp, sp, x15, uxtx #4 (after ___chkstk_darwin)
            total += x15 * 16
            x15 = None
    return total


def is_view_function(name):
    """A SwiftUI-facing function, by its mangled name: one that mentions SwiftUI or returns an opaque type."""
    if not name.startswith(("_$s", "$s")):
        return False
    return "7SwiftUI" in name or "Qr" in name


def functions(path):
    """{mangled name: frame bytes} for every view function in the binary."""
    data = Path(path).read_bytes()
    addr0, text = text_section(data)
    words = array.array("I")
    words.frombytes(text[: len(text) // 4 * 4])
    if sys.byteorder != "little":
        words.byteswap()
    out = subprocess.run(["nm", "-n", "--defined-only", str(path)], capture_output=True, text=True, check=False)
    if out.returncode != 0:
        raise FrameError("nm failed: " + out.stderr.strip())
    syms = []
    for line in out.stdout.splitlines():
        parts = line.split()
        if len(parts) == 3 and parts[1] in "tT":
            a = int(parts[0], 16)
            if addr0 <= a < addr0 + len(text):
                syms.append((a, parts[2]))
    if not syms:
        raise FrameError("no function symbols (stripped binary?)")
    sizes = {}
    for k, (a, name) in enumerate(syms):
        if not is_view_function(name):
            continue
        start = (a - addr0) // 4
        end = (syms[k + 1][0] - addr0) // 4 if k + 1 < len(syms) else len(words)
        size = frame_size(words[start:min(end, start + PROLOGUE)])
        sizes[name] = max(size, sizes.get(name, 0))
    return sizes


def resolve_binary(path):
    p = Path(path)
    if p.suffix == ".app":
        debug = p / "Persona.debug.dylib"
        return debug if debug.exists() else p / p.stem
    return p


def demangle(names):
    if not names:
        return {}
    out = subprocess.run(["xcrun", "swift-demangle", "--simplified"], input="\n".join(names), capture_output=True,
                         text=True, check=False)
    lines = out.stdout.splitlines() if out.returncode == 0 else []
    return dict(zip(names, lines)) if len(lines) == len(names) else {n: n for n in names}


def check(sizes, baseline, threshold):
    """(problems, lowered baseline). A problem is (name, bytes, reason)."""
    problems = []
    lowered = {}
    for name, size in sizes.items():
        known = baseline.get(name)
        if size > threshold and known is None:
            problems.append((name, size, "over %d KB and not in the baseline" % (threshold // 1024)))
        elif known is not None and size > known + SLACK:
            problems.append((name, size, "grew from %d KB in the baseline" % (known // 1024)))
    for name, known in baseline.items():
        size = sizes.get(name)
        if size is not None and size > threshold:
            lowered[name] = min(known, size)
    return problems, lowered


def load_baseline(path):
    if not path.exists():
        return {}
    try:
        data = json.loads(path.read_text())
    except ValueError as e:
        raise FrameError("broken baseline %s: %s" % (path, e))
    if not isinstance(data, dict) or not all(isinstance(v, int) for v in data.values()):
        raise FrameError("baseline %s must map mangled names to byte counts" % path)
    return data


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("binary", help="Persona.app of a Debug iOS build, or a Mach-O binary")
    ap.add_argument("--baseline", default=None, help="default: " + BASELINE_REL)
    ap.add_argument("--threshold", type=int, default=THRESHOLD)
    ap.add_argument("--update", action="store_true", help="lower the baseline (never adds or raises an entry)")
    ap.add_argument("--json", action="store_true", help="print every view function over the threshold as JSON")
    args = ap.parse_args(argv)
    root = Path(__file__).resolve().parents[2]
    baseline_path = Path(args.baseline) if args.baseline else root / BASELINE_REL
    try:
        binary = resolve_binary(args.binary)
        sizes = functions(binary)
        baseline = load_baseline(baseline_path)
    except (FrameError, OSError) as e:
        print("apple-stack-frames: ERROR %s" % e)
        return 2
    over = {n: s for n, s in sizes.items() if s > args.threshold}
    if args.json:
        print(json.dumps(over, indent=2, sort_keys=True))
    problems, lowered = check(sizes, baseline, args.threshold)
    names = demangle(sorted(set(over) | {p[0] for p in problems}))
    for name in sorted(over, key=lambda n: -over[n]):
        mark = "baseline" if name in baseline else "NEW"
        print("%7d KB  %-8s %s" % (over[name] // 1024, mark, names.get(name, name)))
    for name, size, reason in problems:
        print("FAIL %s: %d KB, %s" % (names.get(name, name), size // 1024, reason))
    if args.update:
        if lowered != baseline:
            baseline_path.write_text(json.dumps(lowered, indent=2, sort_keys=True) + "\n")
            print("apple-stack-frames: baseline lowered to %d function(s)" % len(lowered))
    stale = sorted(set(baseline) - set(lowered))
    if stale and not args.update:
        problems.append(("", 0, "stale"))
        print("FAIL %d baseline function(s) are gone or under %d KB: run %s --update to lower the baseline"
              % (len(stale), args.threshold // 1024, "scripts/ci/apple_stack_frames.py <app>"))
    if problems:
        print("apple-stack-frames: FAIL %d problem(s); %d view function(s) over %d KB in %s (fix: docs/lessons/ios.md)"
              % (len(problems), len(over), args.threshold // 1024, binary.name))
        return 1
    print("apple-stack-frames: PASS %d view function(s) checked, %d over %d KB, all in the baseline"
          % (len(sizes), len(over), args.threshold // 1024))
    return 0


if __name__ == "__main__":
    sys.exit(main())
