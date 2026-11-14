#!/usr/bin/env python3
"""Report recorded performance tests without running the app (#5730).

    python3 -I scripts/app/perf_report.py <results> --output-dir <fresh directory>
    python3 -I scripts/app/perf_report.py <results> --iterations 4 --final-acceptance

frames.json attachments are primary; perf-frames log summaries are fallback only.
Cadence FPS is CADisplayLink callback cadence, not GPU/presented FPS. Custom hitch
ms/s is the app probe's lateness, not Instruments' Animation Hitches. Tagged glide
integrity is necessary, not sufficient, for the project's full 60 FPS acceptance.
PERF_ITERATIONS/--iterations/metadata.expected_iterations count measured runs;
exactly one additional, explicitly tagged warm-up run is required. Old arrays
exclude their first sample for diagnostics only and remain legacy_unverified.
XCTest measurements already exclude XCTest's warm-up; do not drop another value.
PerfLaunchTests are complete from their XCTApplicationLaunchMetric alone, with no frame
probe; --final-acceptance remains glide-only, so launch-only reports are diagnostic.
"""
import argparse
import json
import math
import os
import pathlib
import re
import statistics
import subprocess
import sys
import tempfile


PROBE_KEYS = ("hitch_ms_per_s", "hitches", "p95_ms", "max_ms", "busy_p95_ms")
TAG_KEYS = ("name", "run_id", "warm_up", "action", "sample_source", "expected_iterations")
ALIASES = {"fps": "cadence_fps", "hitch_ms_per_s": "custom_hitch_ms_per_s"}
LAUNCH_TEST_PREFIX = "PerfLaunchTests/"


def test_id(value):
    if not isinstance(value, str) or not value or "/" not in value:
        raise ValueError("missing or invalid test identifier")
    return value.removesuffix("()") + "()"


def positive_integer(value):
    if isinstance(value, bool):
        raise ValueError("expected_iterations must be a positive integer")
    if isinstance(value, str) and value.isdecimal():
        value = int(value)
    if not isinstance(value, int) or value <= 0:
        raise ValueError("expected_iterations must be a positive integer")
    return value


def finite(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def safe_json(value):
    """Preserve invalid non-finite raw values explicitly in standards-compliant JSON."""
    if isinstance(value, float) and not math.isfinite(value):
        return {"non_finite": str(value)}
    if isinstance(value, dict):
        return {key: safe_json(item) for key, item in value.items()}
    if isinstance(value, list):
        return [safe_json(item) for item in value]
    return value


def nonfinite_paths(value, path="sample"):
    if isinstance(value, float) and not math.isfinite(value):
        return [path]
    if isinstance(value, dict):
        return [p for key, item in value.items() for p in nonfinite_paths(item, path + "." + key)]
    if isinstance(value, list):
        return [p for i, item in enumerate(value) for p in nonfinite_paths(item, f"{path}[{i}]")]
    return []


def invoke(command, runner=None):
    """Never turn a tool failure, timeout, or malformed JSON into an empty result."""
    state = {"command": command, "status": "ok"}
    try:
        out = (runner or subprocess.run)(command, capture_output=True, text=True, timeout=300)
        state.update(returncode=out.returncode, stderr=out.stderr)
        if out.returncode:
            raise ValueError(f"xcresulttool exited {out.returncode}")
        return out.stdout, state
    except (OSError, subprocess.TimeoutExpired, ValueError) as error:
        state.update(status="failed", error=str(error))
        return None, state


def query(bundle, kind, runner=None):
    text, state = invoke(["xcrun", "xcresulttool", "get", "test-results", kind,
                          "--path", str(bundle), "--compact"], runner)
    if text is None:
        return None, state
    try:
        return json.loads(text), state
    except (ValueError, TypeError) as error:
        state.update(status="failed", error=f"invalid {kind} JSON: {error}")
        return None, state


def statuses(bundle, runner=None):
    data, state = query(bundle, "tests", runner)
    found = {}
    if state["status"] != "ok":
        return found, state
    try:
        if not isinstance(data, dict) or not isinstance(data.get("testNodes"), list):
            raise ValueError("expected testNodes array")

        def walk(node):
            if not isinstance(node, dict):
                raise ValueError("invalid test node")
            if node.get("nodeType") == "Test Case":
                ident = test_id(node.get("nodeIdentifier"))
                if ident in found:
                    raise ValueError(f"duplicate test status: {ident}")
                found[ident] = node.get("result", "")
            children = node.get("children", [])
            if not isinstance(children, list):
                raise ValueError("invalid test children")
            for child in children:
                walk(child)

        for node in data["testNodes"]:
            walk(node)
        if not found:
            raise ValueError("no test cases in result bundle")
    except (ValueError, TypeError) as error:
        state.update(status="failed", error=str(error))
    state["test_count"] = len(found)
    return found, state


def metrics(bundle, runner=None):
    data, state = query(bundle, "metrics", runner)
    if state["status"] != "ok":
        return [], state
    if isinstance(data, dict):
        data = data.get("tests", data.get("testsWithMetrics"))
    if not isinstance(data, list) or not all(isinstance(test, dict) for test in data):
        state.update(status="failed", error="expected metrics array (or tests/testsWithMetrics array)")
        return [], state
    state["test_count"] = len(data)
    return data, state


def read_exported_json(path):
    """Keep JSON parsing isolated even when a caller imports this module without -I."""
    command = [sys.executable, "-I", "-c",
               "import json,sys; print(json.dumps(json.load(open(sys.argv[1]))))", str(path)]
    out = subprocess.run(command, capture_output=True, text=True, timeout=300)
    if out.returncode:
        raise ValueError(f"invalid exported JSON {path.name}: {out.stderr.strip()}")
    return json.loads(out.stdout)


def is_frame_attachment(name):
    # Xcode renames frames.json to frames_<repetition>_<UUID>.json on export.
    return isinstance(name, str) and (name == "frames.json" or
                                     (name.startswith("frames_") and name.endswith(".json")))


def attachments(bundle, runner=None):
    found = {}
    with tempfile.TemporaryDirectory(prefix="persona-perf-attachments-") as directory:
        export = pathlib.Path(directory).resolve()
        _, state = invoke(["xcrun", "xcresulttool", "export", "attachments", "--path", str(bundle),
                           "--output-path", str(export)], runner)
        if state["status"] != "ok":
            return found, state
        # The temporary path is gone after parsing; provenance uses the bundle and manifest filename.
        state["command"][-1] = "<fresh temporary directory>"
        try:
            manifest = read_exported_json(export / "manifest.json")
            if not isinstance(manifest, list):
                raise ValueError("expected attachment manifest array")
            for test in manifest:
                if not isinstance(test, dict) or not isinstance(test.get("attachments"), list):
                    raise ValueError("invalid attachment manifest test entry")
                ident = test_id(test.get("testIdentifier"))
                for attachment in test["attachments"]:
                    if not isinstance(attachment, dict):
                        raise ValueError("invalid attachment manifest attachment entry")
                    if not is_frame_attachment(attachment.get("suggestedHumanReadableName")):
                        continue
                    records = found.setdefault(ident, [])
                    filename = attachment.get("exportedFileName")
                    if not isinstance(filename, str) or pathlib.Path(filename).name != filename:
                        raise ValueError("unsafe exported attachment filename")
                    path = export / filename
                    if path.resolve().parent != export or not path.is_file():
                        raise ValueError(f"missing or unsafe frame attachment: {filename}")
                    samples = read_exported_json(path)
                    if not isinstance(samples, list):
                        raise ValueError(f"frame attachment must contain a sample array: {filename}")
                    for i, sample in enumerate(samples):
                        records.append({"sample": sample, "provenance": {
                            "bundle": str(bundle), "test_identifier": ident,
                            "attachment": attachment, "sample_index": i}})
        except (OSError, ValueError, TypeError, subprocess.TimeoutExpired) as error:
            state.update(status="failed", error=str(error))
        state["frame_test_count"] = len(found)
        state["sample_count"] = sum(len(records) for records in found.values())
        return found, state


def probe_lines(root):
    """Retain every log summary and line number; summaries are never primary samples."""
    found, issues = {}, []
    for log in sorted(pathlib.Path(root).glob("test-*.log")):
        try:
            for number, line in enumerate(log.read_text(errors="replace").splitlines(), 1):
                match = re.search(r"perf-frames: (\S+) (.*)$", line)
                if not match:
                    continue
                values = {}
                for key, value in re.findall(r"(\w+)=(\S+)", match.group(2)):
                    try:
                        values[key] = float(value)
                    except ValueError:
                        pass
                found.setdefault(test_id(match.group(1)), []).append({
                    "values": values, "source": "log_fallback_only", "raw_line": line,
                    "provenance": {"log": str(log), "line": number}})
        except (OSError, ValueError) as error:
            issues.append(f"cannot read {log}: {error}")
    return found, issues


def summarize_probe(ident, records, expected_iterations=None, log_summaries=(), has_attachment=False):
    probe = {"source": "frames_attachment" if has_attachment else "log_fallback_only" if log_summaries else "absent",
             "trust": "missing_samples", "raw_samples": records, "log_fallback": list(log_summaries),
             "issues": [], "warnings": [], "statistics": {}, "medians": {},
             "expected_iterations": expected_iterations, "complete": False, "acceptance_eligible": False}
    issues = probe["issues"]
    if not records:
        issues.append("no raw frame samples")
        if not has_attachment and log_summaries:
            probe["trust"] = "log_fallback_only"
            probe["warnings"].append("log summaries cannot be recalculated or used for final acceptance")
            if len(log_summaries) == 1:
                probe["medians"] = {ALIASES.get(key, key): value
                                    for key, value in log_summaries[0]["values"].items() if finite(value)}
            else:
                issues.append("multiple log summaries; no summary selected")
        probe.update(raw_count=0, measured_count=0, excluded_warm_up_indices=[], statistic_sample_indices=[])
        return probe

    samples = [record["sample"] for record in records]
    tagged = any(isinstance(sample, dict) and any(key in sample for key in TAG_KEYS) for sample in samples)
    probe["trust"] = "tagged_glide" if tagged else "legacy_unverified"
    probe["warm_up_policy"] = "explicit_tag" if tagged else "legacy_first_sample_assumed_for_diagnostics_only"
    if not tagged:
        probe["warnings"].append("legacy untagged samples: no verified IDs, action, movement, warm-up, or run count")
    numeric_indices, run_ids, sample_expected, sources = [], set(), set(), set()
    for i, sample in enumerate(samples):
        prefix = f"sample[{i}]"
        if not isinstance(sample, dict):
            issues.append(f"{prefix}: expected object")
            continue
        numeric_issues = [f"{prefix}: non-finite value at {path}" for path in nonfinite_paths(sample)]
        for key in ("frames", "seconds") + PROBE_KEYS:
            value = sample.get(key)
            if not finite(value) or (value <= 0 if key in ("frames", "seconds") else value < 0):
                numeric_issues.append(f"{prefix}: invalid {key}")
        if not numeric_issues:
            cadence = sample["frames"] / sample["seconds"]
            if finite(cadence):
                numeric_indices.append(i)
            else:
                numeric_issues.append(f"{prefix}: non-finite cadence FPS")
        issues.extend(numeric_issues)
        if not tagged:
            continue
        if sample.get("name") != ident.removesuffix("()"):
            issues.append(f"{prefix}: name does not match test ID {ident.removesuffix('()')}")
        run_id = sample.get("run_id")
        if not isinstance(run_id, str) or not run_id.strip():
            issues.append(f"{prefix}: missing run_id")
        elif run_id in run_ids:
            issues.append(f"{prefix}: duplicate run_id {run_id}")
        else:
            run_ids.add(run_id)
        if type(sample.get("warm_up")) is not bool:
            issues.append(f"{prefix}: warm_up must be bool")
        if sample.get("action") != "glide":
            issues.append(f"{prefix}: action must be glide")
        source = sample.get("sample_source")
        if source != "app-list-glide":
            issues.append(f"{prefix}: sample_source must be app-list-glide")
        if isinstance(source, str):
            sources.add(source)
        viewport, content, movement = (sample.get(key) for key in
                                      ("viewport_height_pt", "content_height_pt", "scroll_offset_delta_pt"))
        if not finite(viewport) or viewport <= 0:
            issues.append(f"{prefix}: invalid viewport_height_pt")
        if not finite(content) or not finite(viewport) or content <= viewport:
            issues.append(f"{prefix}: content_height_pt must exceed viewport_height_pt")
        if not finite(movement) or movement <= 24:
            issues.append(f"{prefix}: scroll_offset_delta_pt must exceed 24 pt")
        if "expected_iterations" in sample:
            try:
                sample_expected.add(positive_integer(sample["expected_iterations"]))
            except ValueError as error:
                issues.append(f"{prefix}: {error}")
    probe["sample_sources"] = sorted(sources) if tagged else ["legacy_unspecified"]
    if tagged:
        if len(sample_expected) > 1:
            issues.append("conflicting expected_iterations in samples")
        if sample_expected:
            count = next(iter(sorted(sample_expected)))
            if expected_iterations is not None and expected_iterations != count:
                issues.append("sample expected_iterations does not match requested count")
            elif expected_iterations is None:
                expected_iterations = count
        warm_up = [i for i, sample in enumerate(samples) if isinstance(sample, dict) and sample.get("warm_up") is True]
        measured = [i for i, sample in enumerate(samples) if isinstance(sample, dict) and sample.get("warm_up") is False]
        if warm_up != [0]:
            issues.append("exactly one first warm-up sample is required")
        if expected_iterations is None:
            issues.append("expected_iterations unknown; run completeness cannot be verified")
        elif len(samples) != expected_iterations + 1 or len(measured) != expected_iterations:
            issues.append(f"incomplete run count: expected {expected_iterations}+1 raw / {expected_iterations} measured, "
                          f"got {len(samples)} raw / {len(measured)} measured")
    else:
        warm_up, measured = [0], list(range(1, len(samples)))
    indices = [i for i in measured if i in numeric_indices]
    if not indices:
        issues.append("no valid non-warm-up samples")
    probe.update(expected_iterations=expected_iterations, raw_count=len(samples), measured_count=len(measured),
                 excluded_warm_up_indices=warm_up, statistic_sample_indices=indices)
    values = {"cadence_fps": [samples[i]["frames"] / samples[i]["seconds"] for i in indices]}
    values.update({ALIASES.get(key, key): [samples[i][key] for i in indices] for key in PROBE_KEYS})
    for key in ("busy_over_16ms", "p50_ms", "p99_ms", "busy_p50_ms", "busy_max_ms"):
        if indices and all(finite(samples[i].get(key)) for i in indices):
            values[key] = [samples[i][key] for i in indices]
    for key, runs in values.items():
        if runs:
            probe["statistics"][key] = {"median": statistics.median(runs), "values": runs, "sample_indices": indices}
            probe["medians"][key] = statistics.median(runs)
    probe["complete"] = not issues
    probe["acceptance_eligible"] = tagged and not issues
    return probe


def add_metrics(entry, test, bundle):
    """Append all XCTest runs instead of overwriting earlier device/configuration measurements."""
    try:
        runs = test.get("testRuns", [])
        if not isinstance(runs, list):
            raise ValueError("invalid XCTest testRuns")
        for run in runs:
            if not isinstance(run, dict) or not isinstance(run.get("metrics", []), list):
                raise ValueError("invalid XCTest metric run")
            for metric in run.get("metrics", []):
                if not isinstance(metric, dict) or not isinstance(metric.get("measurements"), list):
                    raise ValueError("invalid XCTest metric measurements")
                values = metric["measurements"]
                key = metric.get("identifier") or metric.get("displayName")
                if not isinstance(key, str) or not key:
                    raise ValueError("missing XCTest metric identifier")
                target = entry["metrics"].setdefault(key, {"name": metric.get("displayName", key),
                    "unit": metric.get("unitOfMeasurement", ""), "values": [], "runs": [], "median": None})
                target["runs"].append({"bundle": str(bundle), "device": run.get("device"),
                    "configuration": run.get("testPlanConfiguration"), "raw_metric": metric})
                if not all(finite(value) for value in values):
                    raise ValueError(f"non-finite or nonnumeric XCTest measurements: {key}")
                if target["unit"] != metric.get("unitOfMeasurement", ""):
                    raise ValueError(f"conflicting XCTest metric units: {key}")
                target["values"].extend(values)
                target["median"] = statistics.median(target["values"]) if target["values"] else None
    except (ValueError, TypeError) as error:
        entry["issues"].append(str(error))


def launch_measurement(entry, expected_iterations=None):
    """Summarize XCTApplicationLaunchMetric without inventing frame-probe samples."""
    issues, values, summary = [], [], None
    metric = pick(entry, "launch")
    if metric is None:
        issues.append("no XCTApplicationLaunchMetric measurements")
    else:
        factors = {"ms": 1, "millisecond": 1, "milliseconds": 1,
                   "s": 1000, "second": 1000, "seconds": 1000}
        raw_unit = metric.get("unit", "")
        factor = factors.get(raw_unit.lower()) if isinstance(raw_unit, str) else None
        if factor is None:
            issues.append(f"unsupported launch metric unit: {raw_unit}")
        else:
            values = metric.get("values", [])
            if not values or not all(finite(value) and value > 0 for value in values):
                issues.append("launch metric has no finite positive measurements")
                values = []
            elif expected_iterations is None:
                issues.append("expected_iterations unknown; launch run completeness cannot be verified")
            else:
                runs = metric.get("runs", [])
                if not runs:
                    issues.append("launch metric has no per-run measurements")
                for index, run in enumerate(runs, 1):
                    count = len(run["raw_metric"]["measurements"])
                    if count != expected_iterations:
                        issues.append(f"incomplete launch run {index} count: expected {expected_iterations}, got {count}")
            values = [value * factor for value in values]
            if not all(finite(value) for value in values):
                issues.append("launch metric values are not finite after milliseconds conversion")
                values = []
    if values:
        candidate = {"unit": "ms", "values": values, "median": statistics.median(values),
                     "minimum": min(values), "maximum": max(values), "spread": max(values) - min(values),
                     "budget_gate": "none; the 400 ms target is for a recent iPhone, not a simulator"}
        if all(finite(candidate[key]) for key in ("median", "minimum", "maximum", "spread")):
            summary = candidate
        else:
            issues.append("launch metric summary is not finite")
    probe = {"source": "xcresult_launch_metric", "trust": "xctest_measurements", "raw_samples": [],
             "log_fallback": [], "issues": issues, "warnings": [], "statistics": {}, "medians": {},
             "expected_iterations": expected_iterations, "complete": not issues, "acceptance_eligible": False,
             "warm_up_policy": "XCTest discards its warm-up; no additional value dropped",
             "raw_count": len(values), "measured_count": len(values), "excluded_warm_up_indices": [],
             "statistic_sample_indices": list(range(len(values)))}
    return summary, probe


def collect(root, expected_iterations=None, runner=None):
    root = pathlib.Path(root).resolve()
    tests, extraction, raw, issues = {}, [], {}, []
    bundles = sorted(root.glob("*.xcresult"))
    if not root.is_dir() or not bundles:
        issues.append("no xcresult bundles in input directory")
    logs, log_issues = probe_lines(root)
    issues.extend(log_issues)

    def entry_for(ident, suite="", bundle=None):
        entry = tests.setdefault(ident, {"suite": suite, "result": "", "metrics": {}, "issues": [], "bundles": []})
        if bundle and str(bundle) not in entry["bundles"]:
            entry["bundles"].append(str(bundle))
            if len(entry["bundles"]) > 1:
                entry["issues"].append("test ID occurs in multiple result bundles")
        return entry

    for bundle in bundles:
        results, status_state = statuses(bundle, runner)
        metric_tests, metric_state = metrics(bundle, runner)
        frames, frame_state = attachments(bundle, runner)
        extraction.append({"bundle": str(bundle), "statuses": status_state, "metrics": metric_state,
                           "attachments": frame_state})
        for ident, result in results.items():
            entry_for(ident, bundle.stem, bundle)["result"] = result
        for test in metric_tests:
            try:
                ident = test_id(test.get("testIdentifier"))
                add_metrics(entry_for(ident, bundle.stem, bundle), test, bundle)
            except ValueError as error:
                metric_state.update(status="failed", error=str(error))
        for ident, records in frames.items():
            entry_for(ident, bundle.stem, bundle)
            raw.setdefault(ident, []).extend(records)
    for ident in logs:
        entry_for(ident)
    if not tests:
        issues.append("no tests found")
    failed_stages = [str(item["bundle"]) + ": " + stage for item in extraction
                     for stage in ("statuses", "metrics", "attachments") if item[stage]["status"] != "ok"]
    if failed_stages:
        issues.append("xcresult extraction failed: " + "; ".join(failed_stages))
    # IDs must also be unique across different tests, not just within an attachment.
    owners = {}
    for ident, records in raw.items():
        for record in records:
            sample = record["sample"]
            run_id = sample.get("run_id") if isinstance(sample, dict) else None
            if isinstance(run_id, str) and run_id:
                previous = owners.setdefault(run_id, ident)
                if previous != ident:
                    for owner in (previous, ident):
                        tests[owner]["issues"].append(f"run_id shared with another test: {run_id}")
    for ident, entry in tests.items():
        if ident.startswith(LAUNCH_TEST_PREFIX):
            launch, probe = launch_measurement(entry, expected_iterations)
            entry["measurement_kind"] = "launch"
            if launch is not None:
                entry["launch"] = launch
        else:
            probe = summarize_probe(ident, raw.get(ident, []), expected_iterations, logs.get(ident, []), ident in raw)
        entry["probe"] = probe
        if entry["result"] != "Passed":
            entry["issues"].append("test did not pass: " + (entry["result"] or "status absent"))
        entry["complete"] = not entry["issues"] and probe["complete"]
        entry["acceptance_eligible"] = entry["complete"] and probe["acceptance_eligible"] and not issues
    complete = bool(tests) and not issues and all(entry["complete"] for entry in tests.values())
    glide_entries = [entry for ident, entry in tests.items() if not ident.startswith(LAUNCH_TEST_PREFIX)]
    eligible = complete and bool(glide_entries) and all(entry["acceptance_eligible"] for entry in glide_entries)
    return safe_json({"schema_version": 2, "input_directory": str(root), "expected_iterations": expected_iterations,
        "extraction": extraction, "issues": issues, "tests": tests, "complete": complete,
        "acceptance_eligible": eligible, "acceptance_scope": "tagged glide sample integrity only; launch metrics are diagnostic completeness, not presented FPS or a launch-budget gate",
        "status": "diagnostic_complete" if complete else "incomplete"})


def pick(entry, *needles, exclude=()):
    for key, metric in entry.get("metrics", {}).items():
        hay = (key + " " + metric["name"]).lower()
        if all(needle in hay for needle in needles) and not any(item in hay for item in exclude):
            return metric
    return None


def fmt(value, digits=1):
    return "—" if not finite(value) else f"{value:.{digits}f}"


def sample_extreme(probe, key, maximum=False):
    values = probe.get("statistics", {}).get(key, {}).get("values", [])
    values = [value for value in values if finite(value)]
    return (max(values) if maximum else min(values)) if values else None


def cells_of(entry):
    probe = entry.get("probe", {})
    # Pre-v2 reports contained only unverified, rounded log summaries.
    medians = probe.get("medians", {ALIASES.get(key, key): value for key, value in probe.items()})
    scroll = pick(entry, "scroll", "duration")
    hitch = pick(entry, "hitch", "ratio", exclude=("scroll", "navigation", "transition"))
    launch = entry.get("launch")
    launch_range = (f"{fmt(launch['minimum'])}–{fmt(launch['maximum'])}" if launch else "—")
    return [fmt(medians.get(key)) for key in
            ("cadence_fps", "custom_hitch_ms_per_s", "hitches", "p95_ms", "max_ms", "busy_p95_ms")] + [
            fmt(sample_extreme(probe, "cadence_fps")),
            fmt(sample_extreme(probe, "custom_hitch_ms_per_s", maximum=True)),
            fmt(sample_extreme(probe, "max_ms", maximum=True)),
            fmt(hitch.get("median") if hitch else None), fmt(scroll.get("median") if scroll else None, 2),
            fmt(launch.get("median") if launch else None), launch_range,
            fmt(launch.get("spread") if launch else None)]


def markdown(report, before=None):
    lines = ["Cadence FPS = CADisplayLink callbacks, not GPU/presented FPS. Custom hitch ms/s = app-probe lateness.",
             "Legacy samples and log fallback are diagnostic only, never a valid 60 FPS acceptance baseline.",
             "Integrity eligibility does not imply smoothness: inspect minimum cadence, worst hitch and worst frame, not only medians.",
             f"Report: {report['status']}; final-acceptance integrity eligible: {str(report['acceptance_eligible']).lower()}."]
    if any(entry.get("measurement_kind") == "launch" for entry in report["tests"].values()):
        lines.append("Launch ms = XCTApplicationLaunchMetric to the first frame. Simulator numbers have no 400 ms device-budget gate.")
    lines.append("")
    headings = ["Test", "Result", "Source / trust", "Cadence FPS median", "Custom hitch median ms/s", "Hitches median", "p95 frame ms",
                "Longest median ms", "Main busy p95 ms", "Cadence min FPS", "Worst hitch ms/s", "Worst frame ms", "XCT hitch ms/s", "Scroll s",
                "Launch median ms", "Launch range ms", "Launch spread ms"]
    lines += ["| " + " | ".join(headings) + " |", "|" + "---|" * len(headings)]
    for ident, entry in sorted(report["tests"].items(), key=lambda pair: (pair[1]["suite"], pair[0])):
        cells = cells_of(entry)
        if before and ident in before:
            cells = [f"{old} → {new}" for old, new in zip(cells_of(before[ident]), cells)]
        probe = entry["probe"]
        label = probe["source"] + " / " + probe["trust"]
        lines.append("| " + " | ".join([ident, entry["result"] or "?", label] + cells) + " |")
    if before:
        lines.append("\nComparison numbers inherit their original source/trust; old log summaries remain unverified.")
    problems = list(report["issues"])
    for item in report["extraction"]:
        for stage in ("statuses", "metrics", "attachments"):
            if item[stage]["status"] != "ok":
                problems.append(f"{item['bundle']} {stage}: {item[stage].get('error', 'failed')}")
    for ident, entry in report["tests"].items():
        problems += [f"{ident}: {issue}" for issue in entry["issues"] + entry["probe"]["issues"]]
    if problems:
        lines.append("\nIncomplete / failed:")
        lines += ["- " + problem.replace("\n", " ") for problem in problems]
    return "\n".join(lines) + "\n"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("root", type=pathlib.Path)
    parser.add_argument("--output-dir", type=pathlib.Path, help="fresh output location; existing report.json is never overwritten")
    parser.add_argument("--compare", type=pathlib.Path)
    parser.add_argument("--iterations", "--expected-iterations", type=positive_integer)
    parser.add_argument("--metadata", type=pathlib.Path, help='JSON object containing expected_iterations (measured runs)')
    parser.add_argument("--final-acceptance", action="store_true", help="also fail on untrusted or unverifiable sample provenance")
    args = parser.parse_args(argv)
    try:
        expected = args.iterations
        if args.metadata:
            metadata = json.loads(args.metadata.read_text())
            if not isinstance(metadata, dict):
                raise ValueError("metadata must be an object")
            count = positive_integer(metadata.get("expected_iterations"))
            if expected is not None and expected != count:
                raise ValueError("CLI iterations conflict with metadata")
            expected = count
        if expected is None and "PERF_ITERATIONS" in os.environ:
            expected = positive_integer(os.environ["PERF_ITERATIONS"])
        output = args.output_dir or args.root
        if (output / "report.json").exists():
            raise ValueError("report.json already exists; use a fresh --output-dir")
        before = None
        if args.compare:
            path = args.compare / "report.json"
            baseline = json.loads(path.read_text()) if path.exists() else collect(args.compare, expected)
            before = baseline.get("tests", baseline)
        report = collect(args.root, expected)
        report["expected_iterations_source"] = ("cli" if args.iterations is not None else "metadata" if args.metadata else
                                                "env:PERF_ITERATIONS" if expected is not None else "sample_metadata_or_unknown")
        report["mode"] = "final_acceptance_integrity" if args.final_acceptance else "diagnostic"
        output.mkdir(parents=True, exist_ok=True)
        with (output / "report.json").open("x") as stream:
            json.dump(report, stream, ensure_ascii=False, indent=2, allow_nan=False)
            stream.write("\n")
        print(markdown(report, before), end="")
        ready = report["acceptance_eligible"] if args.final_acceptance else report["complete"]
        return 0 if ready else 1
    except (OSError, ValueError, TypeError) as error:
        print(f"perf-report: failed: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
