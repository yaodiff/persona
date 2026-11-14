#!/usr/bin/env python3
"""Offline reporter regressions; run python3 -I scripts/app/test_perf_report.py."""
import contextlib
import copy
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("perf_report", pathlib.Path(__file__).with_name("perf_report.py"))
reporter = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(reporter)
TEST = "PerfExampleTests/testScroll()"
OTHER = "PerfExampleTests/testOther()"
COLD = "PerfLaunchTests/testColdLaunch()"
WARM = "PerfLaunchTests/testWarmLaunch()"


def sample(fps=60, warm_up=False, run_id="measured", **changes):
    value = {"name": TEST.removesuffix("()"), "run_id": run_id, "warm_up": warm_up,
             "action": "glide", "sample_source": "app-list-glide", "expected_iterations": 2,
             "viewport_height_pt": 700, "content_height_pt": 2100, "scroll_offset_delta_pt": 280,
             "frames": fps * 6, "seconds": 6, "hitch_ms_per_s": 2, "hitches": 2,
             "p95_ms": 16.7, "max_ms": 20, "busy_p95_ms": 3}
    value.update(changes)
    return value


def samples():
    return [sample(10, True, "warm"), sample(50, run_id="first", hitch_ms_per_s=4, hitches=3),
            sample(60, run_id="second", hitch_ms_per_s=8, hitches=6)]


def records(values):
    return [{"sample": value, "provenance": {"bundle": "example.xcresult", "sample_index": i,
            "attachment": {"suggestedHumanReadableName": "frames.json", "exportedFileName": "example.json"}}}
            for i, value in enumerate(values)]


def legacy_samples():
    return [{key: value for key, value in row.items() if key not in reporter.TAG_KEYS and not key.endswith("_pt")}
            for row in samples()]


class FakeExporter:
    """Implement the real xcresulttool manifest shape without invoking Apple tools."""
    def __init__(self, values=None, result="Passed", kind="array", errors=None):
        self.values = {TEST: samples()} if values is None else values
        self.results = {ident: result for ident in self.values}
        self.kind = kind
        self.errors = errors or {}
        self.commands = []
        self.output_paths = []
        self.metric_runs = [{"device": {"deviceName": "Example Simulator"}, "metrics": [{
            "displayName": "Duration (Scroll_DraggingAndDeceleration)", "identifier": "scroll.duration",
            "unitOfMeasurement": "s", "measurements": [2, 4]}]}]

    def __call__(self, command, **kwargs):
        self.commands.append(command)
        kind = "attachments" if "export" in command else command[4]
        failure = self.errors.get(kind)
        if isinstance(failure, Exception):
            raise failure
        if failure:
            return subprocess.CompletedProcess(command, 7, "", "example exporter failure")
        if kind == "tests":
            data = {"testNodes": [{"nodeType": "Test Suite", "children": [
                {"nodeType": "Test Case", "nodeIdentifier": ident, "result": result}
                for ident, result in self.results.items()]}]}
        elif kind == "metrics":
            rows = [{"testIdentifier": ident, "testRuns": self.metric_runs} for ident in self.results]
            data = rows if self.kind == "array" else {self.kind: rows}
        else:
            path = pathlib.Path(command[command.index("--output-path") + 1])
            if list(path.iterdir()):
                raise AssertionError("export directory must be fresh and empty")
            self.output_paths.append(path)
            manifest = []
            for i, (ident, value) in enumerate(self.values.items()):
                filename = f"attachment-{i}.json"
                (path / filename).write_text(json.dumps(value))
                manifest.append({"testIdentifier": ident, "testIdentifierURL": "test://example/" + ident,
                    "attachments": [{"exportedFileName": filename, "suggestedHumanReadableName": f"frames_0_example-{i}.json",
                        "isAssociatedWithFailure": False, "configurationName": "Example", "deviceName": "Example Simulator",
                        "deviceId": "example-device", "timestamp": 1, "repetitionNumber": 0}]})
            (path / "manifest.json").write_text(json.dumps(manifest))
            return subprocess.CompletedProcess(command, 0, "", "")
        return subprocess.CompletedProcess(command, 0, json.dumps(data), "")


class LaunchExporter(FakeExporter):
    """XCTest launch metrics and screenshots, deliberately with no frames.json attachments."""
    def __init__(self, rows, unit="ms", result="Passed", with_glide=False):
        super().__init__(values={TEST: samples()} if with_glide else {}, result=result)
        self.rows = rows
        self.results = {ident: result for ident in rows}
        if with_glide:
            self.results[TEST] = "Passed"
        self.unit = unit
        self.with_glide = with_glide

    def __call__(self, command, **kwargs):
        kind = "attachments" if "export" in command else command[4]
        if kind != "metrics" or self.errors.get(kind):
            return super().__call__(command, **kwargs)
        rows = []
        for ident, values in self.rows.items():
            runs = [] if values is None else (values if values and isinstance(values[0], list) else [values])
            test_runs = [{"device": {"deviceName": f"Example Simulator {index}"}, "testPlanConfiguration": "Example",
                          "metrics": [{"displayName": "App Launch Duration (ApplicationLaunch)",
                                       "identifier": "com.apple.dt.XCTMetric_ApplicationLaunch-ApplicationLaunch.duration",
                                       "unitOfMeasurement": self.unit, "measurements": measurements}]}
                         for index, measurements in enumerate(runs, 1)]
            rows.append({"testIdentifier": ident, "testRuns": test_runs})
        if self.with_glide:
            rows.append({"testIdentifier": TEST, "testRuns": self.metric_runs})
        return subprocess.CompletedProcess(command, 0, json.dumps(rows), "")


class ProbeTests(unittest.TestCase):
    def test_medians_do_not_hide_a_single_half_second_frame(self):
        rows = [sample(60, True, "warm", expected_iterations=4)]
        rows += [sample(60, run_id=f"clean-{i}", expected_iterations=4,
                        hitch_ms_per_s=0, hitches=0, max_ms=16.7) for i in range(3)]
        rows.append(sample(55, run_id="stall", expected_iterations=4,
                           hitch_ms_per_s=82, hitches=2, max_ms=506))
        probe = reporter.summarize_probe(TEST, records(rows))
        cells = reporter.cells_of({"probe": probe, "metrics": {}})
        self.assertEqual("60.0", cells[0])
        self.assertEqual(["55.0", "82.0", "506.0"], cells[6:9])

    def summary(self, values=None, **kwargs):
        return reporter.summarize_probe(TEST, records(samples() if values is None else values), has_attachment=True, **kwargs)

    def test_true_even_median_and_explicit_warm_up(self):
        result = self.summary()
        self.assertEqual(result["medians"]["cadence_fps"], 55)
        self.assertEqual(result["medians"]["custom_hitch_ms_per_s"], 6)
        self.assertEqual(result["medians"]["hitches"], 4.5)
        self.assertEqual(result["statistics"]["cadence_fps"]["values"], [50, 60])
        self.assertEqual(result["statistic_sample_indices"], [1, 2])
        self.assertTrue(result["acceptance_eligible"])
        self.assertEqual(result["sample_sources"], ["app-list-glide"])

    def test_legacy_keeps_raw_values_without_inventing_ids(self):
        values = legacy_samples()
        result = self.summary(values)
        self.assertEqual(result["trust"], "legacy_unverified")
        self.assertEqual(result["medians"]["cadence_fps"], 55)
        self.assertFalse(result["acceptance_eligible"])
        self.assertEqual([row["sample"] for row in result["raw_samples"]], values)
        self.assertNotIn("run_id", result["raw_samples"][1]["sample"])
        self.assertTrue(result["complete"])

    def test_absent_and_empty_samples(self):
        for attached in (True, False):
            with self.subTest(attached=attached):
                result = reporter.summarize_probe(TEST, [], has_attachment=attached)
                self.assertFalse(result["complete"])
                self.assertFalse(result["acceptance_eligible"])
                self.assertEqual(result["trust"], "missing_samples")

    def test_single_legacy_sample_is_not_used_as_measured_run(self):
        result = self.summary(legacy_samples()[:1])
        self.assertEqual(result["medians"], {})
        self.assertFalse(result["complete"])

    def test_wrong_missing_or_partial_tags(self):
        for changes in ({"name": OTHER.removesuffix("()")}, {"name": TEST}, {"action": "swipe"},
                        {"sample_source": "display-link"}, {"warm_up": 0}, {"run_id": ""}, {"run_id": 12}):
            with self.subTest(changes=changes):
                values = samples()
                values[1].update(changes)
                self.assertFalse(self.summary(values)["acceptance_eligible"])
        values = legacy_samples()
        values[0]["name"] = TEST.removesuffix("()")
        self.assertFalse(self.summary(values)["complete"])

    def test_duplicate_run_id(self):
        values = samples()
        values[2]["run_id"] = values[1]["run_id"]
        result = self.summary(values)
        self.assertIn("duplicate run_id", " ".join(result["issues"]))
        self.assertFalse(result["complete"])

    def test_zero_negative_nan_and_inf(self):
        for key, value in (("frames", 0), ("seconds", 0), ("frames", -1), ("frames", True),
                           ("seconds", float("nan")), ("frames", float("inf")), ("max_ms", -1),
                           ("busy_p95_ms", float("nan")), ("p95_ms", None)):
            with self.subTest(key=key, value=value):
                values = samples()
                values[1][key] = value
                result = self.summary(values)
                self.assertFalse(result["complete"])
                self.assertEqual(result["statistic_sample_indices"], [2])
        values = samples()
        values[1]["long"] = [[0, float("inf")]]
        self.assertFalse(self.summary(values)["complete"])
        json.dumps(reporter.safe_json(self.summary(values)), allow_nan=False)

    def test_bad_glide_geometry_and_missing_movement(self):
        for key, value in (("viewport_height_pt", 0), ("content_height_pt", 700),
                           ("scroll_offset_delta_pt", 24), ("scroll_offset_delta_pt", float("nan")),
                           ("scroll_offset_delta_pt", None)):
            with self.subTest(key=key, value=value):
                values = samples()
                values[1][key] = value
                self.assertFalse(self.summary(values)["complete"])

    def test_expected_count_does_not_include_warm_up(self):
        self.assertTrue(self.summary(expected_iterations=2)["complete"])
        self.assertFalse(self.summary(expected_iterations=3)["complete"])
        values = samples()[:-1]
        self.assertIn("incomplete run count", " ".join(self.summary(values)["issues"]))

    def test_unknown_or_conflicting_run_count(self):
        values = samples()
        for row in values:
            del row["expected_iterations"]
        self.assertFalse(self.summary(values)["complete"])
        self.assertTrue(self.summary(values, expected_iterations=2)["complete"])
        values[1]["expected_iterations"] = 2
        values[2]["expected_iterations"] = 3
        self.assertFalse(self.summary(values)["complete"])
        for invalid in (0, -1, 1.5, True, "unknown"):
            with self.subTest(count=invalid):
                values = samples()
                values[0]["expected_iterations"] = invalid
                self.assertFalse(self.summary(values)["complete"])

    def test_warm_up_must_be_first_exactly_once(self):
        for flags in ([False, False, False], [False, True, False], [True, True, False]):
            values = samples()
            for row, flag in zip(values, flags):
                row["warm_up"] = flag
            self.assertFalse(self.summary(values)["complete"])

    def test_log_fallback_explicit_and_not_accepted(self):
        logs = [{"values": {"fps": 60, "hitch_ms_per_s": 4}}]
        result = reporter.summarize_probe(TEST, [], log_summaries=logs)
        self.assertEqual(result["source"], "log_fallback_only")
        self.assertEqual(result["medians"]["cadence_fps"], 60)
        self.assertFalse(result["acceptance_eligible"])
        result = reporter.summarize_probe(TEST, [], has_attachment=True, log_summaries=logs)
        self.assertEqual(result["source"], "frames_attachment")
        self.assertEqual(result["medians"], {})
        result = reporter.summarize_probe(TEST, records(samples()), has_attachment=True, log_summaries=logs)
        self.assertEqual(result["medians"]["cadence_fps"], 55)


class CollectionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="perf-reporter-tests-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.bundle = self.root / "example.xcresult"
        self.bundle.mkdir()

    def collect(self, fake=None, **kwargs):
        return reporter.collect(self.root, runner=fake or FakeExporter(), **kwargs)

    def test_real_manifest_shape_primary_samples_and_provenance(self):
        fake = FakeExporter()
        (self.root / "test-example.log").write_text("perf-frames: " + TEST.removesuffix("()") + " runs=2 fps=60\n")
        result = self.collect(fake)
        self.assertTrue(result["complete"])
        self.assertTrue(result["acceptance_eligible"])
        probe = result["tests"][TEST]["probe"]
        self.assertEqual(probe["medians"]["cadence_fps"], 55)
        self.assertEqual(probe["raw_samples"][1]["provenance"]["attachment"]["exportedFileName"], "attachment-0.json")
        self.assertEqual(probe["raw_samples"][1]["provenance"]["sample_index"], 1)
        self.assertEqual(len(probe["log_fallback"]), 1)
        self.assertTrue(all(not path.exists() for path in fake.output_paths))
        self.assertEqual(len(fake.commands), 3)

    def test_multiple_xct_runs_are_retained_without_extra_warm_up_drop(self):
        fake = FakeExporter()
        fake.metric_runs += [{"device": {"deviceName": "Another Example Simulator"}, "metrics": [{
            "displayName": "Duration (Scroll_DraggingAndDeceleration)", "identifier": "scroll.duration",
            "unitOfMeasurement": "s", "measurements": [6, 8]}]}]
        result = self.collect(fake)
        metric = result["tests"][TEST]["metrics"]["scroll.duration"]
        self.assertEqual(metric["values"], [2, 4, 6, 8])
        self.assertEqual(metric["median"], 5)
        self.assertEqual(len(metric["runs"]), 2)

    def test_metrics_array_and_known_wrapper_shapes(self):
        for kind in ("array", "tests", "testsWithMetrics"):
            with self.subTest(kind=kind):
                self.assertTrue(self.collect(FakeExporter(kind=kind))["complete"])
        result = self.collect(FakeExporter(kind="unsupported"))
        self.assertFalse(result["complete"])
        self.assertEqual(result["extraction"][0]["metrics"]["status"], "failed")

    def test_tool_errors_timeouts_and_missing_binary_propagate(self):
        for stage in ("tests", "metrics", "attachments"):
            for error in (True, subprocess.TimeoutExpired("example", 300), FileNotFoundError("example tool absent")):
                with self.subTest(stage=stage, error=error):
                    result = self.collect(FakeExporter(errors={stage: error}))
                    key = "statuses" if stage == "tests" else stage
                    self.assertEqual(result["extraction"][0][key]["status"], "failed")
                    self.assertFalse(result["complete"])
                    self.assertFalse(result["acceptance_eligible"])

    def test_invalid_tool_json_does_not_become_empty_success(self):
        fake = FakeExporter()
        def runner(command, **kwargs):
            if "tests" in command:
                return subprocess.CompletedProcess(command, 0, "not json", "")
            return fake(command, **kwargs)
        result = reporter.collect(self.root, runner=runner)
        self.assertEqual(result["extraction"][0]["statuses"]["status"], "failed")
        self.assertFalse(result["complete"])

    def test_failed_skipped_unknown_and_empty_statuses(self):
        for status in ("Failed", "Skipped", "", "Not Run"):
            with self.subTest(status=status):
                self.assertFalse(self.collect(FakeExporter(result=status))["complete"])
        fake = FakeExporter()
        fake.results = {}
        self.assertFalse(self.collect(fake)["complete"])

    def test_missing_frame_attachment_and_fallback(self):
        fake = FakeExporter(values={})
        fake.results = {TEST: "Passed"}
        log = self.root / "test-example.log"
        log.write_text("noise\nperf-frames: " + TEST.removesuffix("()") + " runs=2 fps=60 hitch_ms_per_s=nan\n")
        result = self.collect(fake)
        self.assertFalse(result["complete"])
        probe = result["tests"][TEST]["probe"]
        self.assertEqual(probe["source"], "log_fallback_only")
        self.assertEqual(probe["log_fallback"][0]["provenance"]["line"], 2)
        self.assertEqual(probe["log_fallback"][0]["values"]["hitch_ms_per_s"], {"non_finite": "nan"})
        self.assertFalse(result["acceptance_eligible"])

    def test_wrong_manifest_test_tag_and_shared_run_ids(self):
        wrong = self.collect(FakeExporter(values={OTHER: samples()}))
        self.assertFalse(wrong["complete"])
        others = [dict(row, name=OTHER.removesuffix("()")) for row in samples()]
        shared = self.collect(FakeExporter(values={TEST: samples(), OTHER: others}))
        for entry in shared["tests"].values():
            self.assertFalse(entry["acceptance_eligible"])
            self.assertIn("run_id shared", " ".join(entry["issues"]))

    def test_bad_exporter_shape_missing_and_unsafe_files(self):
        for shape in ("object_manifest", "object_samples", "missing", "traversal"):
            fake = FakeExporter()
            def runner(command, **kwargs):
                result = fake(command, **kwargs)
                if "export" in command:
                    path = pathlib.Path(command[-1])
                    manifest = json.loads((path / "manifest.json").read_text())
                    if shape == "object_manifest":
                        (path / "manifest.json").write_text("{}")
                    elif shape == "object_samples":
                        (path / "attachment-0.json").write_text("{}")
                    elif shape == "missing":
                        (path / "attachment-0.json").unlink()
                    else:
                        manifest[0]["attachments"][0]["exportedFileName"] = "../example.json"
                        (path / "manifest.json").write_text(json.dumps(manifest))
                return result
            with self.subTest(shape=shape):
                result = reporter.collect(self.root, runner=runner)
                self.assertEqual(result["extraction"][0]["attachments"]["status"], "failed")
                self.assertFalse(result["complete"])

    def test_exported_json_reader_is_isolated(self):
        path = self.root / "input.json"
        path.write_text('{"ok": true}')
        with mock.patch.object(reporter.subprocess, "run", wraps=subprocess.run) as run:
            self.assertEqual(reporter.read_exported_json(path), {"ok": True})
        self.assertEqual(run.call_args.args[0][1], "-I")

    def test_frame_attachment_names(self):
        for name in ("frames.json", "frames_0_example.json"):
            self.assertTrue(reporter.is_frame_attachment(name))
        for name in ("other.json", "frames.txt", None):
            self.assertFalse(reporter.is_frame_attachment(name))

    def test_no_bundles_or_no_tests_is_incomplete(self):
        self.bundle.rmdir()
        self.assertFalse(self.collect()["complete"])

    def test_markdown_uses_honest_labels_and_half_hitch_counts(self):
        text = reporter.markdown(self.collect())
        self.assertIn("Cadence FPS", text)
        self.assertIn("Custom hitch ms/s", text)
        self.assertIn("| 55.0 | 6.0 | 4.5 |", text)
        self.assertNotIn("| FPS |", text)
        old = {TEST: {"probe": {"fps": 60}, "metrics": {}}}
        self.assertIn("60.0 → 55.0", reporter.markdown(self.collect(), old))

    def run_main(self, fake=None, arguments=(), env=None):
        out = self.root / ("output-" + str(len(list(self.root.glob("output-*")))))
        fake = fake or FakeExporter()
        original_collect = reporter.collect
        def collect(root, expected=None):
            return original_collect(root, expected, runner=fake)
        with mock.patch.object(reporter, "collect", side_effect=collect), mock.patch.dict(os.environ, env or {}, clear=True), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            code = reporter.main([str(self.root), "--output-dir", str(out)] + list(arguments))
        return code, out

    def test_final_acceptance_exit_codes(self):
        self.assertEqual(self.run_main(arguments=("--final-acceptance",))[0], 0)
        self.assertEqual(self.run_main(FakeExporter(values={TEST: legacy_samples()}), ("--final-acceptance",))[0], 1)
        self.assertEqual(self.run_main(FakeExporter(values={TEST: legacy_samples()}))[0], 0)
        for status in ("Failed", "Skipped"):
            self.assertEqual(self.run_main(FakeExporter(result=status), ("--final-acceptance",))[0], 1)
        self.assertEqual(self.run_main(FakeExporter(values={TEST: samples()[:-1]}), ("--final-acceptance",))[0], 1)
        self.assertEqual(self.run_main(FakeExporter(errors={"attachments": True}), ("--final-acceptance",))[0], 1)

    def test_env_and_cli_metadata_iterations(self):
        self.assertEqual(self.run_main(env={"PERF_ITERATIONS": "3"})[0], 1)
        self.assertEqual(self.run_main(arguments=("--iterations", "2"), env={"PERF_ITERATIONS": "3"})[0], 0)
        metadata = self.root / "metadata.json"
        metadata.write_text('{"expected_iterations": 2}')
        code, out = self.run_main(arguments=("--metadata", str(metadata)))
        self.assertEqual(code, 0)
        self.assertEqual(json.loads((out / "report.json").read_text())["expected_iterations_source"], "metadata")
        self.assertEqual(self.run_main(arguments=("--metadata", str(metadata), "--iterations", "3"))[0], 2)
        self.assertEqual(self.run_main(env={"PERF_ITERATIONS": "invalid"})[0], 2)

    def test_existing_report_is_not_overwritten(self):
        path = self.root / "report.json"
        path.write_text("original")
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(reporter.main([str(self.root)]), 2)
        self.assertEqual(path.read_text(), "original")


class LaunchReportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="perf-launch-reporter-tests-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        (self.root / "launch.xcresult").mkdir()

    def collect(self, fake, **kwargs):
        return reporter.collect(self.root, runner=fake, **kwargs)

    def test_cold_and_warm_launch_metrics_need_no_frame_probe(self):
        fake = LaunchExporter({COLD: [100, 140, 180], WARM: [80, 100, 140]})
        result = self.collect(fake, expected_iterations=3)
        self.assertTrue(result["complete"])
        self.assertFalse(result["acceptance_eligible"], "launch-only final acceptance remains glide-only")
        self.assertFalse(result["tests"][COLD]["acceptance_eligible"])
        cold = result["tests"][COLD]
        warm = result["tests"][WARM]
        self.assertEqual(cold["measurement_kind"], "launch")
        self.assertEqual(cold["probe"]["source"], "xcresult_launch_metric")
        self.assertEqual(cold["probe"]["raw_samples"], [])
        self.assertEqual(cold["launch"]["values"], [100, 140, 180])
        self.assertEqual(cold["launch"]["median"], 140)
        self.assertEqual(cold["launch"]["minimum"], 100)
        self.assertEqual(cold["launch"]["maximum"], 180)
        self.assertEqual(cold["launch"]["spread"], 80)
        self.assertEqual(warm["launch"]["spread"], 60)
        text = reporter.markdown(result)
        self.assertIn("Launch median ms", text)
        self.assertIn("100.0–180.0", text)
        self.assertIn("no 400 ms device-budget gate", text)
        self.assertIn("xcresult_launch_metric / xctest_measurements", text)

    def test_launch_seconds_convert_to_ms_without_a_simulator_budget_failure(self):
        result = self.collect(LaunchExporter({COLD: [0.4, 0.8, 1.2]}, unit="s"), expected_iterations=3)
        self.assertTrue(result["complete"])
        launch = result["tests"][COLD]["launch"]
        self.assertEqual(launch["values"], [400, 800, 1200])
        self.assertEqual(launch["median"], 800)
        self.assertEqual(launch["spread"], 800)
        self.assertIn("none", launch["budget_gate"])

    def test_missing_metric_and_incomplete_iterations_fail(self):
        missing = self.collect(LaunchExporter({COLD: None}), expected_iterations=3)
        self.assertFalse(missing["complete"])
        self.assertIn("no XCTApplicationLaunchMetric", " ".join(missing["tests"][COLD]["probe"]["issues"]))
        incomplete = self.collect(LaunchExporter({COLD: [100, 140, 180]}), expected_iterations=4)
        self.assertFalse(incomplete["complete"])
        self.assertIn("expected 4, got 3", " ".join(incomplete["tests"][COLD]["probe"]["issues"]))
        unsupported = self.collect(LaunchExporter({COLD: [1, 2, 3]}, unit="ticks"), expected_iterations=3)
        self.assertFalse(unsupported["complete"])
        self.assertIn("unsupported launch metric unit", " ".join(unsupported["tests"][COLD]["probe"]["issues"]))

    def test_invalid_launch_values_fail(self):
        for values in ([0, 100, 200], [-100, 100, 200], [100, float("nan"), 200], [100, None, 200]):
            with self.subTest(values=values):
                result = self.collect(LaunchExporter({COLD: values}), expected_iterations=3)
                self.assertFalse(result["complete"])
                self.assertFalse(result["tests"][COLD]["acceptance_eligible"])

    def test_launch_rejects_non_string_units_and_non_finite_conversions(self):
        for unit in (None, 7):
            with self.subTest(unit=unit):
                result = self.collect(LaunchExporter({COLD: [100]}, unit=unit), expected_iterations=1)
                self.assertFalse(result["complete"])
                self.assertIn("unsupported launch metric unit", " ".join(result["tests"][COLD]["probe"]["issues"]))
        overflow = self.collect(LaunchExporter({COLD: [1e308, 1e308]}, unit="s"), expected_iterations=2)
        self.assertFalse(overflow["complete"])
        self.assertNotIn("launch", overflow["tests"][COLD])
        self.assertIn("not finite after milliseconds conversion", " ".join(overflow["tests"][COLD]["probe"]["issues"]))

    def test_launch_counts_are_checked_per_test_run(self):
        complete = self.collect(LaunchExporter({COLD: [[100, 140, 180], [200, 240, 280]]}), expected_iterations=3)
        self.assertTrue(complete["complete"])
        self.assertEqual(complete["tests"][COLD]["launch"]["values"], [100, 140, 180, 200, 240, 280])
        self.assertEqual(complete["tests"][COLD]["launch"]["median"], 190)
        incomplete = self.collect(LaunchExporter({COLD: [[100, 140, 180], [200, 240]]}), expected_iterations=3)
        self.assertFalse(incomplete["complete"])
        self.assertIn("launch run 2 count: expected 3, got 2",
                      " ".join(incomplete["tests"][COLD]["probe"]["issues"]))

    def test_launch_requires_a_verifiable_iteration_count(self):
        one = LaunchExporter({COLD: [100]})
        unknown = self.collect(one)
        self.assertFalse(unknown["complete"])
        self.assertIn("expected_iterations unknown", " ".join(unknown["tests"][COLD]["probe"]["issues"]))
        self.assertTrue(self.collect(one, expected_iterations=1)["complete"])
        out = self.root / "main-output"
        original_collect = reporter.collect
        def collect(root, expected=None):
            return original_collect(root, expected, runner=one)
        with mock.patch.object(reporter, "collect", side_effect=collect), mock.patch.dict(os.environ, {}, clear=True), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            code = reporter.main([str(self.root), "--output-dir", str(out)])
        self.assertEqual(code, 1)
        self.assertFalse(json.loads((out / "report.json").read_text())["complete"])

    def test_combined_final_acceptance_still_uses_the_glide(self):
        fake = LaunchExporter({COLD: [100, 140]}, with_glide=True)
        result = self.collect(fake, expected_iterations=2)
        self.assertTrue(result["complete"])
        self.assertTrue(result["acceptance_eligible"])
        self.assertTrue(result["tests"][TEST]["acceptance_eligible"])
        self.assertFalse(result["tests"][COLD]["acceptance_eligible"])

    def test_failed_launch_does_not_become_a_pass(self):
        result = self.collect(LaunchExporter({COLD: [100, 140, 180]}, result="Failed"), expected_iterations=3)
        self.assertFalse(result["complete"])
        self.assertFalse(result["acceptance_eligible"])
        self.assertIn("test did not pass: Failed", result["tests"][COLD]["issues"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
