"""android_build_perf.py's arithmetic: the histogram gfxinfo prints, the percentiles and shares read from it, medians,
the order packages run in, which starts count as cold, and the links a release build is opened with."""
from contextlib import redirect_stdout
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("android_build_perf", pathlib.Path(__file__).with_name("android_build_perf.py"))
perf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(perf)

GFXINFO = """Total frames rendered: 10
Janky frames: 2 (20.00%)
50th percentile: 9ms
GPU HISTOGRAM: 1ms=9 2ms=1
HISTOGRAM: 5ms=1 6ms=0 8ms=4 16ms=3 32ms=1 150ms=1
50th gpu percentile: 3ms
GPU HISTOGRAM: 3ms=7 4ms=3
"""


class AndroidBuildPerfTest(unittest.TestCase):
    def test_reads_the_frame_histogram_not_the_gpu_one(self):
        self.assertEqual(perf.histogram(GFXINFO), dict([(5, 1), (6, 0), (8, 4), (16, 3), (32, 1), (150, 1)]))
        self.assertEqual(perf.histogram("Total frames rendered: 0"), dict())

    def test_percentiles_are_the_bucket_they_fall_in(self):
        found = perf.histogram_stats(perf.histogram(GFXINFO))
        self.assertEqual(found["frames"], 10)
        self.assertEqual((found["p50"], found["p90"], found["p99"]), (8, 32, 150))

    def test_shares_count_from_a_bucket_up(self):
        found = perf.histogram_stats(perf.histogram(GFXINFO))
        # the 16 ms bucket is under 17; the 32 ms bucket (32 to 33.99) is under 34
        self.assertEqual(found["from_17ms_percent"], 20.0)
        self.assertEqual(found["from_34ms_percent"], 10.0)

    def test_no_frames_is_no_statistics(self):
        self.assertIsNone(perf.histogram_stats(dict()))
        self.assertIsNone(perf.histogram_stats(dict([(8, 0)])))

    def test_runs_add_up_bucket_by_bucket(self):
        first = dict(histogram=dict([(8, 2), (16, 1)]))
        second = dict(histogram=dict([(8, 3), (32, 1)]))
        self.assertEqual(perf.merged([first, second]), dict([(8, 5), (16, 1), (32, 1)]))

    def test_median_of_an_even_count_is_between_the_middle_two(self):
        six = perf.stats([2489, 2422, 2405, 2439, 2443, 2411])
        self.assertEqual((six["n"], six["min"], six["median"], six["max"]), (6, 2405, 2430.5, 2489))
        self.assertEqual(perf.stats(list(range(1, 11)))["median"], 5.5)
        self.assertEqual(perf.stats([3, None, 1, 2])["median"], 2)
        self.assertIsNone(perf.stats([None]))

    def test_packages_swap_places_every_other_round(self):
        packages = [["debug", "a"], ["release", "b"]]
        self.assertEqual(perf.ordered(packages, 0), packages)
        self.assertEqual(perf.ordered(packages, 1), [["release", "b"], ["debug", "a"]])
        self.assertEqual(perf.ordered(packages, 2), packages)

    def test_only_a_cold_start_is_timed(self):
        self.assertEqual(perf.cold_start_ms("LaunchState: COLD\nTotalTime: 557\nWaitTime: 560"), 557)
        self.assertIsNone(perf.cold_start_ms("LaunchState: WARM\nTotalTime: 120"))
        # another activity took the front: no TotalTime at all
        self.assertIsNone(perf.cold_start_ms("LaunchState: UNKNOWN (0)\nWaitTime: 455"))
        # an older `am` that prints no LaunchState
        self.assertEqual(perf.cold_start_ms("TotalTime: 2422"), 2422)

    def test_cold_report_omits_timed_out_launches(self):
        launches = iter((
            "Status: timeout\nLaunchState: COLD\nTotalTime: 5000\nComplete\n",
            "Status: timeout\nTotalTime: 5000\nComplete\n",
            "Status: ok\nLaunchState: COLD\nTotalTime: 230\nComplete\n",
            "TotalTime: 330\nComplete\n",
        ))

        def adb(command, **kwargs):
            args = command[4:]
            if args == ["wm", "size"]:
                stdout = "Physical size: 1080x2400"
            elif args[:3] == ["am", "start", "-W"]:
                stdout = next(launches)
            else:
                stdout = ""
            return subprocess.CompletedProcess(command, 0, stdout)

        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory) / "out.json"
            argv = ["android_build_perf", "synthetic", str(out), "debug=test.app"]
            with (patch.object(sys, "argv", argv), patch.dict(os.environ, {"CHAT": "", "ONLY": "agents"}),
                  patch.object(perf, "RUNS", 0), patch.object(perf, "COLD", 4), patch.object(perf.time, "sleep"),
                  patch.object(subprocess, "run", side_effect=adb), redirect_stdout(io.StringIO())):
                perf.main()
            saved = json.loads(out.read_text())["debug/cold_start_ms"]
        self.assertEqual(saved["values"], [230, 330])
        self.assertEqual((saved["n"], saved["median"]), (2, 280))

    def test_links_open_the_list_and_a_conversation(self):
        self.assertEqual(perf.scenarios(None, None), dict(agents="persona://open?tab=agents"))
        both = perf.scenarios("server/s-1", None)
        self.assertEqual(both["chat"], "persona://open?tab=agents&machine=server&session=s-1")
        self.assertEqual(list(perf.scenarios("server/s-1", ["chat"])), ["chat"])

    def test_interrupted_report_write_preserves_previous_round(self):
        def adb(command, **kwargs):
            args = command[4:]
            if args == ["wm", "size"]:
                stdout = "Physical size: 1080x2400"
            elif args[:2] == ["dumpsys", "gfxinfo"] and args[-1] != "reset":
                stdout = GFXINFO
            else:
                stdout = ""
            return subprocess.CompletedProcess(command, 0, stdout)

        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory) / "out.json"
            dump = json.dump
            completed = []

            def write(report, stream, **kwargs):
                if not completed:
                    dump(report, stream, **kwargs)
                    completed.append(json.dumps(report, **kwargs).encode())
                else:
                    stream.write('{"partial":')
                    raise OSError("synthetic write failure")

            argv = ["android_build_perf", "synthetic", str(out), "debug=test.app"]
            with (patch.object(sys, "argv", argv), patch.dict(os.environ, {"CHAT": "", "ONLY": "agents"}),
                  patch.object(perf, "RUNS", 2), patch.object(perf, "COLD", 0), patch.object(perf.time, "sleep"),
                  patch.object(subprocess, "run", side_effect=adb), patch.object(json, "dump", side_effect=write),
                  redirect_stdout(io.StringIO())):
                with self.assertRaisesRegex(OSError, "synthetic write failure"):
                    perf.main()
            self.assertEqual(len(completed), 1)
            self.assertEqual(out.read_bytes(), completed[0])
            self.assertEqual(json.loads(out.read_bytes())["debug/agents"]["frames_total"], 10)
            self.assertEqual(list(out.parent.iterdir()), [out])

    def test_failed_or_incomplete_round_preserves_complete_sample(self):
        for failure, expected in (
                ("reset", "adb shell dumpsys failed (exit 1)"),
                ("swipe", "adb shell input failed (exit 1)"),
                ("read", "adb shell dumpsys failed (exit 1)"),
                ("Total frames rendered: 0\nJanky frames: 0", "gfxinfo did not report a usable frame sample"),
                ("Total frames rendered: 10", "gfxinfo did not report a usable frame sample"),
                ("Total frames rendered: 10\nJanky frames: 11", "gfxinfo did not report a usable frame sample")):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                out = pathlib.Path(directory) / "out.json"
                reads = 0

                def adb(command, **kwargs):
                    nonlocal reads
                    args = command[4:]
                    stage, stdout = None, ""
                    if args == ["wm", "size"]:
                        stdout = "Physical size: 1080x2400"
                    elif args[:2] == ["dumpsys", "gfxinfo"] and args[-1] == "reset":
                        stage = "reset"
                    elif args[:2] == ["input", "swipe"]:
                        stage = "swipe"
                    elif args[:2] == ["dumpsys", "gfxinfo"]:
                        stage, stdout = "read", failure if reads else GFXINFO
                    if reads and stage == failure:
                        if kwargs.get("check"):
                            raise subprocess.CalledProcessError(1, command)
                        return subprocess.CompletedProcess(command, 1, "")
                    reads += stage == "read"
                    return subprocess.CompletedProcess(command, 0, stdout)

                argv = ["android_build_perf", "synthetic", str(out), "debug=test.app"]
                with (patch.object(sys, "argv", argv), patch.dict(os.environ, {"CHAT": "", "ONLY": "agents"}),
                      patch.object(perf, "RUNS", 2), patch.object(perf, "COLD", 0), patch.object(perf.time, "sleep"),
                      patch.object(subprocess, "run", side_effect=adb), redirect_stdout(io.StringIO())):
                    with self.assertRaises(SystemExit) as error:
                        perf.main()
                    self.assertEqual(str(error.exception), expected)
                saved = json.loads(out.read_text())["debug/agents"]
                self.assertEqual(saved["runs"], [{"total": 10, "janky": 2}])
                self.assertEqual((saved["frames_total"], saved["janky_percent_total"]), (10, 20.0))
                self.assertEqual(saved["frame_ms"]["frames"], 10)


if __name__ == "__main__":
    unittest.main()
