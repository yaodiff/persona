#!/usr/bin/env python3
"""android_scroll_perf.py <serial> <package> <out.json> [styles...]: scroll the Agents list, a conversation and the
inbox in each interface style (glass / brief / board) on a debug build, 10 runs per scenario, and record
dumpsys gfxinfo for every run (docs/android/android.md, "Interface style performance").

Each run: open the page through the debug entry (debug.style, debug.automation: no frame-drop fallback), wait, reset
gfxinfo, fling up and down twice, then read the summary (total / janky frames, percentiles) and the framestats rows
(frame time = FrameCompleted - IntendedVsync). The conversation is the Persona tab unless CHAT=<machine>/<session>; ONLY=chat,inbox limits the scenarios.
The last style passed stays selected in the app: end with the one the phone should keep.
RUNS, SWIPE_MS and SETTLE_S override the run count, swipe duration and idle delay; for example, SWIPE_MS=1000 SETTLE_S=0
keeps the page scrolling through most of the sample instead of counting ambient-rendering frames during the default idle delays.
Failed ADB commands and unusable frame counts stop the current scenario before its results are written."""
from functools import partial
import json, os, re, statistics, sys, time

from android_perf_capture import adb_command, frame_counts, write_report

serial, package, out = sys.argv[1:4]
styles = sys.argv[4:] or ["glass", "brief", "board"]
RUNS = int(os.environ.get("RUNS", "10"))
if RUNS < 1:
    raise SystemExit("RUNS must be positive")
SWIPE_MS = int(os.environ.get("SWIPE_MS", "180"))
SETTLE_S = float(os.environ.get("SETTLE_S", "0.9"))

adb = partial(adb_command, serial, timeout=60)

size = adb("shell", "wm", "size", capture=True)
w, h = map(int, re.findall(r"(\d+)x(\d+)", size)[-1])
x = w // 2

def launch(style, extras):
    adb("shell", "am", "force-stop", package)
    args = ["shell", "am", "start", "-W", "-n", f"{package}/persona.android.debug.DebugEntryActivity",
            "--es", "debug.automation", "true", "--es", "debug.style", style]
    for i in range(0, len(extras), 2):
        args += ["--es", extras[i], extras[i + 1]]
    adb(*args)

def fling(up=True):
    y1, y2 = (int(h * 0.78), int(h * 0.28)) if up else (int(h * 0.28), int(h * 0.78))
    adb("shell", "input", "swipe", str(x), str(y1), str(x), str(y2), str(SWIPE_MS))

def frame_times(text):
    """Read usable timing rows from the first optional profile section."""
    blocks = text.split("---PROFILEDATA---", 2)
    if len(blocks) < 3:
        return []
    lines = blocks[1].strip().splitlines()
    if not lines:
        return []
    header = lines[0].split(",")
    try:
        iv, fc, flags = (header.index(name) for name in ("IntendedVsync", "FrameCompleted", "Flags"))
    except ValueError:
        return []

    frames = []
    last_column = max(iv, fc, flags)
    for line in lines[1:]:
        cols = line.split(",")
        if len(cols) <= last_column or cols[flags] != "0":
            continue
        try:
            start, done = int(cols[iv]), int(cols[fc])
        except ValueError:
            continue
        if start <= 0 or done <= start or done - start > 5e9:  # unfinished or bogus rows
            continue
        frames.append((done - start) / 1e6)
    return frames

def summary():
    text = adb("shell", "dumpsys", "gfxinfo", package, "framestats", capture=True)
    total, janky = frame_counts(text)
    def num(pattern):
        m = re.search(pattern, text)
        return float(m.group(1)) if m else None
    return {
        "total": total,
        "janky": janky,
        "p50": num(r"50th percentile: (\d+)ms"), "p90": num(r"90th percentile: (\d+)ms"),
        "p95": num(r"95th percentile: (\d+)ms"), "p99": num(r"99th percentile: (\d+)ms"),
        "frames": frame_times(text),
    }

SCENARIOS = {
    "agents": ["debug.openTab", "Agents"],
    "inbox": ["debug.openTab", "Inbox"],
    "chat": None,  # filled from argv env: a long conversation path
}

def run_scenario(style, name, extras):
    runs = []
    for i in range(RUNS):
        launch(style, extras)
        time.sleep(3.5)
        adb("shell", "dumpsys", "gfxinfo", package, "reset")
        for _ in range(2):
            fling(up=True); time.sleep(SETTLE_S)
            fling(up=False); time.sleep(SETTLE_S)
        runs.append(summary())
        print(style, name, i, runs[-1]["total"], runs[-1]["janky"], flush=True)
    return runs

def stats(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return None
    q = lambda p: values[min(len(values) - 1, int(round(p * (len(values) - 1))))]
    return {"n": len(values), "min": values[0], "p50": q(0.5), "p90": q(0.9), "max": values[-1], "mean": round(statistics.fmean(values), 2)}

if __name__ == "__main__":
    chat = os.environ.get("CHAT")
    result = {}
    for style in styles:
        for name, extras in SCENARIOS.items():
            if os.environ.get("ONLY") and name not in os.environ["ONLY"].split(","):
                continue
            if name == "chat":
                # Without a session path, the Persona tab: the assistant's long-running main conversation.
                extras = ["debug.openSession", chat, "debug.conversationKind", "agent"] if chat else ["debug.openTab", "Persona"]
            runs = run_scenario(style, name, extras)
            frames = [f for r in runs for f in r["frames"]]
            total = sum(r["total"] for r in runs)
            jank = [100.0 * r["janky"] / r["total"] for r in runs]
            result[f"{style}/{name}"] = {
                "frame_ms": stats(frames),
                "janky_percent_per_run": stats(jank),
                "janky_percent_total": round(100.0 * sum(r["janky"] for r in runs) / total, 2),
                "frames_total": total,
                "gfx_p90_ms_per_run": stats([r["p90"] for r in runs]),
                "runs": [{k: v for k, v in r.items() if k != "frames"} for r in runs],
            }
            write_report(out, result)
    print(json.dumps({k: {"frame_ms": v["frame_ms"], "janky_total": v["janky_percent_total"]} for k, v in result.items()}, ensure_ascii=False, indent=1))
