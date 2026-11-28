#!/usr/bin/env python3
"""android_build_perf.py <serial> <out.json> <label>=<package> [<label>=<package> ...]: compare builds of the app on one
phone, for example a debug and a release package of the same commit installed side by side under two application ids
(docs/android/android.md, "Debug against release").

Pages open through persona://open links, which a release build answers too (android_scroll_perf.py needs the debug
entry). Each round opens the Agents list, and a conversation when CHAT=<machine>/<session> is set, in every package,
in alternating order so drift on the phone (heat, background work) lands on all of them: wait, reset gfxinfo, fling up
and down twice, read the summary and the histogram. The histogram counts every frame since the reset; the framestats
rows keep only the last 120 per window, so the percentiles here come from the histogram. Its buckets are 1 ms wide up
to 31 ms and wider above, so shares are counted from a bucket's edge: 17 ms or longer, 34 ms or longer.
COLD=<n> also times n cold starts of each package with `am start -W`; a start the phone does not report as cold is
dropped. Every package must be paired already.
RUNS (10), SWIPE_MS (180) and SETTLE_S (0.9) override the round count, swipe duration and idle delay; ONLY=agents,chat
limits the scenarios."""
from functools import partial
import json, os, re, statistics, sys, time

from android_perf_capture import adb_command, frame_counts, write_report

RUNS = int(os.environ.get("RUNS", "10"))
SWIPE_MS = int(os.environ.get("SWIPE_MS", "180"))
SETTLE_S = float(os.environ.get("SETTLE_S", "0.9"))
COLD = int(os.environ.get("COLD", "0"))


def histogram(text):
    """gfxinfo's frame-time HISTOGRAM line (not the GPU one) as a map from each bucket's name in ms to its frames."""
    line = re.search(r"^HISTOGRAM: (.*)", text, re.MULTILINE)
    return dict((int(ms), int(n)) for ms, n in re.findall(r"(\d+)ms=(\d+)", line.group(1))) if line else dict()


def histogram_stats(buckets):
    """Percentiles and shares over a histogram: the bucket a percentile falls in, and the share of frames from a bucket
    up. The 32 ms bucket is 2 ms wide, so the long-frame share starts at 34."""
    rows = sorted(buckets.items())
    total = sum(n for _, n in rows)
    if not total:
        return None

    def at(p):
        seen = 0
        for ms, n in rows:
            seen += n
            if seen >= p * total:
                return ms
        return rows[-1][0]

    def from_bucket(first):
        return round(100.0 * sum(n for ms, n in rows if ms >= first) / total, 2)

    return dict(frames=total, p50=at(0.5), p90=at(0.9), p95=at(0.95), p99=at(0.99),
            from_17ms_percent=from_bucket(17), from_34ms_percent=from_bucket(34))


def merged(runs):
    total = dict()
    for run in runs:
        for ms, n in run["histogram"].items():
            total[ms] = total.get(ms, 0) + n
    return total


def stats(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return None
    return dict(n=len(values), min=round(values[0], 1), median=round(statistics.median(values), 1),
            max=round(values[-1], 1), mean=round(statistics.fmean(values), 2), values=[round(v, 2) for v in values])


def ordered(packages, i):
    """Reversed every other round, so no package always runs first."""
    return packages if i % 2 == 0 else packages[::-1]


def cold_start_ms(text):
    """TotalTime for a successful cold start. Older shells may omit status or launch state."""
    status = re.search(r"^Status:[ \t]*(.*)$", text, re.MULTILINE)
    if status and status.group(1).strip() != "ok":
        return None
    state = re.search(r"LaunchState: (\w+)", text)
    total = re.search(r"TotalTime: (\d+)", text)
    if not total or (state and state.group(1) != "COLD"):
        return None
    return int(total.group(1))


def scenarios(chat, only):
    found = dict(agents="persona://open?tab=agents")
    if chat:
        machine, session = chat.split("/", 1)
        found["chat"] = "persona://open?tab=agents&machine=%s&session=%s" % (machine, session)
    return dict((name, link) for name, link in found.items() if not only or name in only)


def main():
    serial, out = sys.argv[1:3]
    packages = [arg.split("=", 1) for arg in sys.argv[3:]]
    only = os.environ["ONLY"].split(",") if os.environ.get("ONLY") else None
    links = scenarios(os.environ.get("CHAT"), only)

    adb = partial(adb_command, serial, timeout=90)

    w, h = map(int, re.findall(r"(\d+)x(\d+)", adb("shell", "wm", "size", capture=True))[-1])

    def fling(up):
        y1, y2 = (int(h * 0.78), int(h * 0.28)) if up else (int(h * 0.28), int(h * 0.78))
        adb("shell", "input", "swipe", str(w // 2), str(y1), str(w // 2), str(y2), str(SWIPE_MS))

    def summary(package):
        text = adb("shell", "dumpsys", "gfxinfo", package, capture=True)
        total, janky = frame_counts(text)
        return dict(total=total, janky=janky, histogram=histogram(text))

    raw = dict(("%s/%s" % (label, name), []) for label, _ in packages for name in links)
    cold = dict((label, []) for label, _ in packages)
    result = dict(device=adb("shell", "getprop", "ro.product.model", capture=True).strip(), size="%dx%d" % (w, h),
            runs=RUNS, swipe_ms=SWIPE_MS, settle_s=SETTLE_S)

    def save():
        for key, runs in raw.items():
            if not runs:
                continue
            frames = sum(r["total"] for r in runs)
            result[key] = dict(
                frames_total=frames,
                janky_percent_total=round(100.0 * sum(r["janky"] for r in runs) / frames, 2),
                janky_percent_per_run=stats([100.0 * r["janky"] / r["total"] for r in runs]),
                frame_ms=histogram_stats(merged(runs)),
                runs=[dict(total=r["total"], janky=r["janky"]) for r in runs],
            )
        for label, values in cold.items():
            if values:
                result[label + "/cold_start_ms"] = stats(values)
        write_report(out, result)

    for i in range(COLD):
        for label, package in ordered(packages, i):
            adb("shell", "am", "force-stop", package)
            time.sleep(1.5)
            text = adb("shell", "am", "start", "-W", "-n", package + "/persona.android.MainActivity", capture=True)
            took = cold_start_ms(text)
            if took is not None:
                cold[label].append(took)
            print("cold", label, i, took, flush=True)
            time.sleep(4)
        save()
    for i in range(RUNS):
        for name, link in links.items():
            for label, package in ordered(packages, i):
                adb("shell", "am", "force-stop", package)
                # quoted for the phone's shell, where & would end the command
                adb("shell", "am", "start", "-W", "-a", "android.intent.action.VIEW", "-d", "'%s'" % link, "-p", package)
                time.sleep(3.5)
                adb("shell", "dumpsys", "gfxinfo", package, "reset")
                for _ in range(2):
                    fling(True)
                    time.sleep(SETTLE_S)
                    fling(False)
                    time.sleep(SETTLE_S)
                run = summary(package)
                raw["%s/%s" % (label, name)].append(run)
                print(label, name, i, run["total"], run["janky"], flush=True)
                adb("shell", "am", "force-stop", package)
            save()
    brief = dict((k, dict((x, y) for x, y in v.items() if x != "runs") if isinstance(v, dict) else v) for k, v in result.items())
    print(json.dumps(brief, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
