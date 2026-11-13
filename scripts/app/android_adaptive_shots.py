#!/usr/bin/env python3
"""Run same-process adaptive Android scenes from scripts/app/android.sh shots-live."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
import xml.etree.ElementTree as ET

PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"
PROBE_PATH = "no_backup/hub-data/adaptive-probe.json"
UI_PATH = "/data/local/tmp/persona-adaptive-ui.xml"
MODIFIER_KEYS = ("SHIFT_LEFT", "CTRL_LEFT")
TRANSFORM_STEPS = {"rotate", "posture", "wm-size", "freeform-resize"}
MAX_PROBE_AGE_NANOS = 15_000_000_000
MAX_PROBE_BYTES = 2_000_000


class AdaptiveFailure(RuntimeError):
    pass


@dataclass(frozen=True)
class TaskEvidence:
    task_id: int
    block: str
    bounds: tuple | None
    configuration_dp: tuple | None


def device_quote(value):
    return shlex.quote(str(value))


def strip_png(data):
    if not data:
        raise AdaptiveFailure("empty PNG")
    start = data.find(PNG_SIGNATURE)
    if start < 0:
        raise AdaptiveFailure("bad PNG signature")
    return data[start:]


def parse_wm_size(text):
    result = {}
    for name, pattern in (("physical", r"Physical size:\s*(\d+)x(\d+)"), ("override", r"Override size:\s*(\d+)x(\d+)")):
        match = re.search(pattern, text)
        if match:
            result[name] = (int(match.group(1)), int(match.group(2)))
    return result


def parse_wm_density(text):
    result = {}
    for name, pattern in (("physical", r"Physical density:\s*(\d+)"), ("override", r"Override density:\s*(\d+)")):
        match = re.search(pattern, text)
        if match:
            result[name] = int(match.group(1))
    return result


def parse_device_state(text):
    named = re.search(r"mCommittedState=.*\bname='([A-Z_]+)'", text)
    if named:
        return named.group(1)
    match = re.search(r"mCommittedState=([A-Z_]+|\d+)", text)
    return match.group(1) if match else None


def _task_block(text, task_id):
    lines = text.splitlines()
    for index, line in enumerate(lines):
        if re.match(r"^\s*Task\{", line) and re.search(r"(?:#|\bt)\s*" + str(task_id) + r"\b", line):
            indent = len(line) - len(line.lstrip())
            block = [line]
            for following in lines[index + 1:]:
                following_indent = len(following) - len(following.lstrip())
                if following.strip() and following_indent <= indent and re.match(r"^\s*(?:Task\{|[A-Z][A-Za-z ]*\{)", following):
                    break
                block.append(following)
            return "\n".join(block)
    return None


def parse_persona_task(text, app_id):
    resumed = re.search(
        r"(?:topResumedActivity|mResumedActivity)=ActivityRecord\{[^}\n]*\bt(\d+)\}?", text)
    if not resumed:
        raise AdaptiveFailure("current resumed task is not identifiable")
    task_id = int(resumed.group(1))
    block = _task_block(text, task_id)
    if block is None or app_id not in block:
        raise AdaptiveFailure("current resumed task is not Persona")
    bounds_match = re.search(
        r"\bmBounds=Rect\(\s*(-?\d+),\s*(-?\d+)\s*-\s*(-?\d+),\s*(-?\d+)\)", block)
    bounds = tuple(int(value) for value in bounds_match.groups()) if bounds_match else None
    config = None
    for line in block.splitlines():
        if "CurrentConfiguration" not in line and "mOverrideConfig" not in line:
            continue
        match = re.search(r"\bsw(\d+)dp\s+w(\d+)dp\s+h(\d+)dp\b", line)
        if match:
            config = tuple(int(value) for value in match.groups())
            break
    return TaskEvidence(task_id, block, bounds, config)


def parse_ui_nodes(xml_bytes):
    if not xml_bytes:
        raise AdaptiveFailure("empty UI XML")
    try:
        root = ET.fromstring(xml_bytes)
    except ET.ParseError as error:
        raise AdaptiveFailure("invalid UI XML") from error
    if root is None or root.tag != "hierarchy":
        raise AdaptiveFailure("UI XML has a null or unknown root")
    nodes = list(root.iter("node"))
    if not nodes:
        raise AdaptiveFailure("UI XML contains no nodes")
    return nodes


def parse_bounds(value):
    match = re.fullmatch(r"\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]", value or "")
    if not match:
        raise AdaptiveFailure("UI node has invalid bounds")
    left, top, right, bottom = (int(item) for item in match.groups())
    if right <= left or bottom <= top:
        raise AdaptiveFailure("UI node has empty bounds")
    return (left + right) // 2, (top + bottom) // 2


def ascii_input_argument(value):
    if not isinstance(value, str) or not value or any(ord(char) < 32 or ord(char) > 126 for char in value):
        raise AdaptiveFailure("text input supports printable ASCII only; IME composing requires instrumentation")
    escaped = value.replace(" ", "%s")
    if any(char in escaped for char in "\r\n"):
        raise AdaptiveFailure("text input cannot contain a newline; use SHIFT_LEFT ENTER")
    return device_quote(escaped)


def page_requirements(requires):
    pages = requires.get("pages")
    if pages is None:
        return [("default", requires["page"], [])] if requires.get("page") else []
    if not isinstance(pages, dict) or not pages or requires.get("page"):
        raise AdaptiveFailure("requires.pages must be a non-empty object and cannot coexist with requires.page")
    result = []
    for alias, specification in pages.items():
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", str(alias)):
            raise AdaptiveFailure("probe page alias must be an identifier")
        if isinstance(specification, str):
            specification = {"page": specification}
        if not isinstance(specification, dict) or not specification.get("page"):
            raise AdaptiveFailure("each probe page needs a page value")
        fields = specification.get("fields", [])
        if not isinstance(fields, list) or any(not isinstance(field, str) or not field for field in fields):
            raise AdaptiveFailure("probe page fields must be non-empty strings")
        result.append((alias, specification["page"], fields))
    return result


def rectangles_overlap(first, second):
    if not isinstance(first, dict) or not isinstance(second, dict):
        raise AdaptiveFailure("notOverlaps needs two probe rectangles")
    try:
        values = [first[key] for key in ("left", "top", "right", "bottom")] + [second[key] for key in ("left", "top", "right", "bottom")]
        if any(isinstance(value, bool) or not isinstance(value, (int, float)) for value in values):
            raise AdaptiveFailure("probe rectangle coordinates must be numeric")
        for rectangle in (first, second):
            if rectangle["right"] <= rectangle["left"] or rectangle["bottom"] <= rectangle["top"]:
                raise AdaptiveFailure("probe rectangle has no area")
        separated = (max(first["left"], second["left"]) >= min(first["right"], second["right"]) or
                     max(first["top"], second["top"]) >= min(first["bottom"], second["bottom"]))
    except (KeyError, TypeError) as error:
        raise AdaptiveFailure("probe rectangle needs numeric left/top/right/bottom") from error
    return not separated


def validate_requirements(requires):
    if not isinstance(requires, dict) or not isinstance(requires.get("fields", []), list):
        raise AdaptiveFailure("scenario requires must contain a fields list")
    if any(not isinstance(field, str) or not field for field in requires.get("fields", [])):
        raise AdaptiveFailure("required evidence fields must be non-empty strings")
    page_requirements(requires)


def path_value(snapshot, path):
    aliases = {
        "pid": "pidAfter",
        "posture": "deviceState",
        "draftHmac": "draftHmac",
        "selection": "selection",
        "focused": "focused",
        "attachments": "attachments",
        "firstVisibleKey": "firstVisibleKey",
        "firstVisibleOffset": "firstVisibleOffsetPx",
        "firstVisibleOffsetPx": "firstVisibleOffsetPx",
    }
    path = aliases.get(path, path)
    value = snapshot
    for part in path.split("."):
        if not isinstance(value, dict) or part not in value:
            return None
        value = value[part]
    return value


def normalize_scenario(document):
    """Accept the compact {step-name: {arguments}} spelling as well as the canonical step field."""
    document = copy.deepcopy(document)
    for index, original in enumerate(document.get("steps", [])):
        if not isinstance(original, dict) or "step" in original:
            continue
        kinds = [key for key in original if key in {"launch", "open", "tap", "text", "key", "rotate", "posture", "wm-size", "freeform-resize", "assert", "capture"}]
        if len(kinds) != 1 or len(original) != 1:
            continue
        kind = kinds[0]
        payload = original[kind]
        if not isinstance(payload, dict):
            continue
        step = dict(payload)
        step["step"] = kind
        if kind == "wm-size" and "width" in step and "height" in step:
            step["size"] = "%dx%d" % (step.pop("width"), step.pop("height"))
        if kind == "posture" and "state" in step:
            step["value"] = step.pop("state")
        if kind == "rotate" and "direction" in step:
            step["orientation"] = step.pop("direction")
        if kind == "assert" and "checks" not in step:
            checks = dict(step)
            checks.pop("step")
            step = {"step": "assert", "checks": checks}
        if kind == "capture":
            step.setdefault("state", "capture")
        document["steps"][index] = step
    return document


def validate_scenario(document):
    if not isinstance(document, dict):
        raise AdaptiveFailure("scenario root must be an object")
    scenario_id = document.get("id")
    if not isinstance(scenario_id, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", scenario_id):
        raise AdaptiveFailure("scenario id must be a filename-safe identifier")
    if not isinstance(document.get("description"), str) or not document["description"].strip():
        raise AdaptiveFailure("scenario description is required")
    requires = document.get("requires")
    validate_requirements(requires)
    steps = document.get("steps")
    if not isinstance(steps, list) or not steps:
        raise AdaptiveFailure("scenario steps must be a non-empty list")
    supported = {"launch", "open", "tap", "text", "key", "rotate", "posture", "wm-size", "freeform-resize", "assert", "capture"}
    for step in steps:
        if not isinstance(step, dict) or step.get("step") not in supported:
            raise AdaptiveFailure("unsupported scenario step: %r" % (step.get("step") if isinstance(step, dict) else step,))
        if "requires" in step:
            validate_requirements(step["requires"])
    if steps[0].get("step") != "launch":
        raise AdaptiveFailure("the first step must be launch")
    if sum(step["step"] == "launch" for step in steps) != 1:
        raise AdaptiveFailure("a continuous scenario has exactly one launch")
    transformed = False
    last_target = None
    for step in steps:
        kind = step["step"]
        if kind in ("launch", "open"):
            target = (step.get("session"), step.get("page"), step.get("tab"), step.get("link"))
            if transformed and kind == "open" and target == last_target:
                raise AdaptiveFailure("open cannot restore the same page after a window transformation")
            last_target = target
        transformed = transformed or kind in TRANSFORM_STEPS
    return document


def poll(description, operation, timeout=20.0, interval=0.2):
    deadline = time.monotonic() + timeout
    last_error = None
    while True:
        try:
            value = operation()
            if value is not None and value is not False:
                return value
        except AdaptiveFailure as error:
            last_error = error
        if time.monotonic() >= deadline:
            detail = ": %s" % last_error if last_error else ""
            raise AdaptiveFailure("TIMEOUT %s%s" % (description, detail))
        time.sleep(min(interval, max(0.0, deadline - time.monotonic())))


class Adb:
    def __init__(self, executable, serial):
        self.executable = executable
        self.serial = serial
        self.commands = []

    def run(self, arguments, timeout=20, binary=False, check=True):
        command = [self.executable, "-s", self.serial] + [str(item) for item in arguments]
        self.commands.append(command)
        try:
            completed = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            raise AdaptiveFailure("TIMEOUT adb %s" % " ".join(arguments[:2])) from error
        if check and completed.returncode != 0:
            detail = completed.stderr.decode("utf-8", "replace").strip()[-2000:]
            raise AdaptiveFailure("adb %s failed: %s" % (" ".join(arguments[:2]), detail))
        return completed.stdout if binary else completed.stdout.decode("utf-8", "replace")

    def shell(self, *arguments, timeout=20, check=True):
        return self.run(["shell"] + list(arguments), timeout=timeout, check=check)


class AdaptiveRunner:
    def __init__(self, adb, scenario, output, theme, reset=False):
        self.adb = adb
        self.scenario = scenario
        self.output = Path(output)
        self.theme = theme
        self.reset = reset
        self.app_id = os.environ["APP_ID"]
        self.mock_address = os.environ["MOCK_ADDRESS"]
        self.style = os.environ.get("SHOTS_STYLE") or "glass"
        self.settle = float(os.environ.get("SHOTS_SETTLE", "2"))
        self.serial = adb.serial
        self.initial_pid = None
        self.previous_pid = None
        self.previous_snapshot = None
        self.assertions = []
        self.action_log = []
        self.posture_evidence = "none"
        self.route_override = None
        self.selected_key_override = None
        self.current_open_target = None
        self.transformed = False
        self.screenshots = 0
        self.fixtures = self._read_fixtures()
        self.original_size = parse_wm_size(adb.shell("wm", "size"))
        self.original_density = parse_wm_density(adb.shell("wm", "density"))
        original_dimensions = self.original_size.get("override", self.original_size.get("physical"))
        self.original_orientation = "landscape" if original_dimensions and original_dimensions[0] > original_dimensions[1] else "portrait"
        self.orientation_changed = False
        self.size_changed = False
        self.density_changed = False
        self.posture_changed = False
        self.fake_posture_changed = False
        self._step_pid_before = None

    def _read_fixtures(self):
        result = {}
        path = self.output / "fixture.tsv"
        if not path.exists():
            return result
        for line in path.read_text().splitlines():
            parts = line.split("\t")
            if len(parts) >= 2:
                result[parts[0]] = parts[1]
        return result

    def pid(self):
        output = self.adb.shell("pidof", self.app_id, timeout=5, check=False).strip()
        if not output:
            return None
        values = output.split()
        if len(values) != 1 or not values[0].isdigit():
            raise AdaptiveFailure("ambiguous Persona PID: %s" % output)
        return int(values[0])

    def _launch_arguments(self, step, pairing=False):
        activity = ("persona.android.debug.DebugEntryActivity" if step.get("step") == "launch"
                    else "persona.android.MainActivity")
        automation_type = "--ez" if step.get("step") == "launch" else "--es"
        arguments = ["am", "start", "-W", "-n", self.app_id + "/" + activity,
                     automation_type, "debug.automation", "true", "--es", "debug.theme", self.theme]
        if os.environ.get("SHOTS_STYLE"):
            arguments += ["--es", "debug.style", self.style]
        if pairing:
            arguments += ["--es", "debug.mockAddress", self.mock_address]
        if step.get("windowingMode") is not None:
            arguments += ["--windowingMode", str(int(step["windowingMode"]))]
        if step.get("tab"):
            arguments += ["--es", "debug.openTab", str(step["tab"])]
        if step.get("session"):
            target = self.fixtures.get(step["session"], step["session"])
            arguments += ["--es", "debug.openSession", target,
                          "--es", "debug.conversationKind", str(step.get("conversationKind", "agent"))]
        if step.get("page"):
            arguments += ["--es", "debug.openPage", str(step["page"])]
        if step.get("link"):
            target = self.fixtures.get(step["link"], step["link"])
            arguments += ["--es", "debug.openLink", device_quote(target)]
        if step.get("posture") in ("book", "tabletop") and step.get("fake"):
            arguments += ["--es", "debug.posture", step["posture"]]
        return arguments

    def note_open(self, step):
        target = (step.get("session"), step.get("page"), step.get("tab"), step.get("link"))
        if self.transformed and target == self.current_open_target:
            raise AdaptiveFailure("open cannot restore the same page after a window transformation")
        self.current_open_target = target
        if step.get("page"):
            self.route_override = str(step["page"])
            self.selected_key_override = (str(step["page"]).split("machine:", 1)[1]
                                          if str(step["page"]).startswith("machine:") else None)
        else:
            self.route_override = None
            self.selected_key_override = None

    def shell_ready(self):
        output = self.adb.shell("run-as", self.app_id, "find", "no_backup/hub-data", "-name", "shell-position.json",
                                timeout=5)
        files = [line for line in output.splitlines() if line.strip().endswith("shell-position.json")]
        if len(files) != 1:
            return None
        return files[0]

    def wait_ready(self, step):
        poll("pairing and shell creation", self.shell_ready, timeout=30)
        expected = step.get("session")
        if expected:
            target = self.fixtures.get(expected, expected)
            def selected():
                route, key = self.read_navigation()
                return True if key == target else None
            poll("selected session " + target, selected, timeout=float(step.get("timeout", 60)))
            def draft_ready():
                probe = self.read_probe(self.pid())
                now = self.elapsed_nanos()
                texts = [node.attrib.get("text", "") for node in self.ui_tree()]
                page = self.page_evidence(probe, "chat:" + target, now, texts)
                return page.get("composer", {}).get("draftLoaded") is True or None
            poll("composer draft " + target, draft_ready, timeout=float(step.get("timeout", 60)))
        probe_page = step.get("probePage") or {
            "machines": "Machines", "settings": "Settings", "phone": "Phone data",
        }.get(step.get("page"))
        if (step.get("page") or step.get("link")) and not probe_page:
            raise AdaptiveFailure("open page/link needs probePage evidence")
        if probe_page:
            def target_ready():
                probe = self.read_probe(self.pid())
                now = self.elapsed_nanos()
                texts = [node.attrib.get("text", "") for node in self.ui_tree()]
                self.page_evidence(probe, probe_page, now, texts)
                return True
            poll("open target " + probe_page, target_ready, timeout=float(step.get("timeout", 60)))

    def reset_modifiers(self):
        for key in MODIFIER_KEYS:
            self.adb.shell("input", "keyevent", key, timeout=5)
        self.action_log.append({"type": "modifierReset", "keys": list(MODIFIER_KEYS)})

    def ui_tree(self):
        last_error = None
        for attempt in range(5):
            self.adb.shell("rm", "-f", UI_PATH, timeout=5, check=False)
            try:
                self.adb.shell("uiautomator", "dump", UI_PATH, timeout=12)
                xml = self.adb.shell("cat", UI_PATH, timeout=5)
                nodes = parse_ui_nodes(xml.encode("utf-8"))
                if not nodes:
                    raise AdaptiveFailure("UI XML contains no nodes")
                return nodes
            except AdaptiveFailure as error:
                last_error = error
                if attempt < 4:
                    time.sleep(0.5)
        raise AdaptiveFailure("uiautomator dump failed after 5 fresh attempts: %s" % last_error)

    def tap(self, step):
        last_count = [0]
        def unique_node():
            nodes = self.ui_tree()
            matches = []
            for node in nodes:
                resource = node.attrib.get("resource-id", "")
                resource_match = step.get("resource-id") and (
                    resource == step["resource-id"] or resource.endswith(":id/" + step["resource-id"]))
                desc_match = step.get("content-desc") is not None and node.attrib.get("content-desc") == step["content-desc"]
                text_match = step.get("text") is not None and node.attrib.get("text") == step["text"]
                if resource_match or desc_match or text_match:
                    matches.append(node)
            last_count[0] = len(matches)
            return matches[0] if len(matches) == 1 else None
        try:
            node = poll("tap selector", unique_node, timeout=float(step.get("timeout", 30)))
        except AdaptiveFailure as error:
            raise AdaptiveFailure("tap selector matched %d nodes, expected exactly one" % last_count[0]) from error
        x, y = parse_bounds(node.attrib.get("bounds"))
        self.adb.shell("input", "tap", str(x), str(y), timeout=5)
        self.action_log.append({"type": "tap", "x": x, "y": y})

    def text(self, step):
        self.reset_modifiers()
        def focused():
            return True if any(node.attrib.get("focused") == "true" for node in self.ui_tree()) else None
        poll("focused input before text", focused, timeout=10)
        argument = ascii_input_argument(step.get("text"))
        self.adb.shell("input", "text", argument, timeout=10)
        self.action_log.append({"type": "text", "length": len(step["text"]), "ascii": True})
        self.reset_modifiers()

    def key(self, step):
        self.reset_modifiers()
        if step.get("keycombination"):
            keys = [str(key) for key in step["keycombination"]]
            self.adb.shell("input", "keyboard", "keycombination", *keys, timeout=8)
        elif step.get("key"):
            keys = [str(step["key"])]
            self.adb.shell("input", "keyevent", keys[0], timeout=5)
        else:
            raise AdaptiveFailure("key step needs key or keycombination")
        self.action_log.append({"type": "key", "keys": keys})
        self.reset_modifiers()

    def read_navigation(self):
        path = poll("navigation checkpoint", self.shell_ready, timeout=10)
        raw = self.adb.shell("run-as", self.app_id, "cat", path, timeout=5)
        try:
            position = json.loads(raw)
        except json.JSONDecodeError as error:
            raise AdaptiveFailure("invalid shell-position.json") from error
        tab = position.get("tab")
        if not isinstance(tab, str) or not tab:
            raise AdaptiveFailure("shell position has no route tab")
        chats = position.get("chats") if isinstance(position.get("chats"), dict) else {}
        selected = chats.get(tab)
        key = None
        route = tab
        if isinstance(selected, dict) and selected.get("machineId") and selected.get("sessionId"):
            key = selected["machineId"] + "/" + selected["sessionId"]
            route += "/chat"
        elif position.get("machine"):
            key = position["machine"]
        return route, key

    def elapsed_nanos(self):
        value = self.adb.shell("cat", "/proc/uptime", timeout=5).split()[0]
        return int(float(value) * 1_000_000_000)

    def read_probe(self, pid):
        raw = self.adb.shell("run-as", self.app_id, "cat", PROBE_PATH, timeout=5)
        if len(raw) > MAX_PROBE_BYTES:
            raise AdaptiveFailure("adaptive probe exceeds the size bound")
        try:
            probe = json.loads(raw)
        except json.JSONDecodeError as error:
            raise AdaptiveFailure("adaptive probe is corrupt") from error
        if probe.get("fieldVersion") != 1 or probe.get("pid") != pid or not probe.get("session"):
            raise AdaptiveFailure("adaptive probe belongs to a different field version, PID or session")
        now = self.elapsed_nanos()
        updated = probe.get("updatedAtNanos")
        if not isinstance(updated, int) or updated - now > 2_000_000_000 or now - updated > MAX_PROBE_AGE_NANOS:
            raise AdaptiveFailure("adaptive probe is stale")
        if not isinstance(probe.get("pages"), dict):
            raise AdaptiveFailure("adaptive probe has no pages")
        return probe

    def page_evidence(self, probe, page_name, now, visible_texts=()):
        pages = probe["pages"]
        if page_name is None:
            matches = [value for key, value in pages.items() if key in visible_texts]
        else:
            exact = pages.get(page_name)
            matches = [exact] if exact is not None else [value for key, value in pages.items() if key.startswith(page_name + ":")]
        if len(matches) != 1:
            raise AdaptiveFailure("required probe page %s matched %d pages" % (page_name or "<visible>", len(matches)))
        page = matches[0]
        updated = page.get("updatedAtNanos")
        if not isinstance(updated, int) or updated - now > 2_000_000_000 or now - updated > MAX_PROBE_AGE_NANOS:
            raise AdaptiveFailure("required probe page is stale")
        return page

    def read_window(self, require_task=False):
        density_data = parse_wm_density(self.adb.shell("wm", "density"))
        density = density_data.get("override", density_data.get("physical"))
        if not density:
            raise AdaptiveFailure("window density is unavailable")
        activities = self.adb.shell("dumpsys", "activity", "activities", timeout=15)
        task = None
        try:
            task = parse_persona_task(activities, self.app_id)
        except AdaptiveFailure:
            if require_task:
                raise
        size_data = parse_wm_size(self.adb.shell("wm", "size"))
        size = size_data.get("override", size_data.get("physical"))
        if not size:
            raise AdaptiveFailure("window size is unavailable")
        width_px, height_px = size
        width_dp = round(width_px * 160 / density)
        height_dp = round(height_px * 160 / density)
        evidence = "display"
        bounds = None
        task_id = None
        if task:
            task_id = task.task_id
            bounds = task.bounds
            if task.configuration_dp:
                _, width_dp, height_dp = task.configuration_dp
                evidence = "task-configuration"
            elif task.bounds:
                width_px = task.bounds[2] - task.bounds[0]
                height_px = task.bounds[3] - task.bounds[1]
                width_dp = round(width_px * 160 / density)
                height_dp = round(height_px * 160 / density)
                evidence = "task-bounds"
        displays = self.adb.shell("dumpsys", "window", "displays", timeout=15)
        current = re.search(r"\bcur=(\d+)x(\d+)", displays)
        if current and not (task and task.configuration_dp):
            width_px, height_px = (int(value) for value in current.groups())
            width_dp = round(width_px * 160 / density)
            height_dp = round(height_px * 160 / density)
            evidence = "display-current"
        rotation = re.search(r"mCurrentRotation=(?:ROTATION_)?(\d+)", displays)
        rotation_value = {"90": 1, "180": 2, "270": 3}.get(rotation.group(1), int(rotation.group(1))) if rotation else None
        orientation = ("landscape" if rotation_value in (1, 3) else "portrait") if rotation_value is not None else (
            "landscape" if width_dp > height_dp else "portrait")
        return {
            "widthDp": width_dp, "heightDp": height_dp, "widthPx": width_px, "heightPx": height_px,
            "densityDpi": density, "orientation": orientation, "taskId": task_id, "bounds": bounds,
            "evidence": evidence,
        }

    def device_state(self):
        output = self.adb.shell("dumpsys", "device_state", timeout=10)
        state = parse_device_state(output)
        if not state:
            raise AdaptiveFailure("DeviceState is unavailable")
        return state

    def rotate(self, step):
        before = self.read_window()
        self.adb.run(["emu", "rotate"], timeout=10)
        expected = step.get("orientation")
        def condition():
            window = self.read_window()
            matches = window["orientation"] == expected if expected else window["orientation"] != before["orientation"]
            return window if matches else None
        window = poll("rotated window orientation", condition, timeout=float(step.get("timeout", 20)))
        self.orientation_changed = True
        self.transformed = True
        return window

    def update_fake_posture(self, step):
        if not step.get("fake"):
            return
        raw = step.get("value", step.get("posture"))
        fake_active = raw in ("book", "tabletop")
        self.fake_posture_changed = fake_active
        self.posture_evidence = "fake" if fake_active else "none"
        self.transformed = True

    def posture(self, step):
        raw = step.get("value", step.get("posture"))
        fake_tabletop = step.get("fake") and raw == "tabletop"
        value = 3 if fake_tabletop else raw
        names = {"book": 2, "half-open": 2, "open": 3, "unfold": 3, "closed": 1}
        value = names.get(value, value)
        if value not in (1, 2, 3):
            raise AdaptiveFailure("posture must be 1, 2, 3, book, tabletop, open or closed")
        if step.get("fake"):
            self.update_fake_posture(step)
            self.adb.shell(*self._launch_arguments(step), timeout=20)
            return self.device_state()
        self.adb.run(["emu", "posture", str(value)], timeout=10)
        self.posture_evidence = "folding-feature"
        expected = {1: "CLOSED", 2: "HALF_OPENED", 3: "OPENED"}[value]
        state = poll("DeviceState " + expected, lambda: expected if self.device_state() == expected else None,
                     timeout=float(step.get("timeout", 20)))
        self.posture_changed = self.posture_changed or not step.get("fake")
        self.orientation_changed = True
        self.transformed = True
        return state

    def wm_size(self, step):
        if step.get("reset"):
            self.adb.shell("wm", "size", "reset", timeout=10)
            if step.get("density") or step.get("densityReset"):
                self.adb.shell("wm", "density", "reset", timeout=10)
            self.size_changed = True
            self.density_changed = True
            expected_size = None
        else:
            match = re.fullmatch(r"(\d+)x(\d+)", str(step.get("size", "")))
            if not match:
                raise AdaptiveFailure("wm-size needs size=WxH or reset=true")
            width_px, height_px = (int(value) for value in match.groups())
            density = step.get("density")
            if not density:
                density = parse_wm_density(self.adb.shell("wm", "density")).get("override")
            if not density:
                density = parse_wm_density(self.adb.shell("wm", "density")).get("physical")
            compact = round(width_px * 160 / density) < 600 or round(height_px * 160 / density) < 480
            if compact:
                self.adb.run(["emu", "unfold"], timeout=10)
                self.posture_changed = True
                poll("unfolded before compact wm-size", lambda: "OPENED" if self.device_state() == "OPENED" else None,
                     timeout=20)
            self.adb.shell("wm", "size", f"{width_px}x{height_px}", timeout=10)
            self.size_changed = True
            if density:
                self.adb.shell("wm", "density", str(density), timeout=10)
                self.density_changed = True
            expected_size = (width_px, height_px)
        def readback():
            if step.get("reset") and "override" in parse_wm_size(self.adb.shell("wm", "size")):
                return None
            if step.get("densityReset") and "override" in parse_wm_density(self.adb.shell("wm", "density")):
                return None
            window = self.read_window()
            if expected_size and (window["widthPx"], window["heightPx"]) != expected_size:
                return None
            if step.get("widthDp") is not None and window["widthDp"] != step["widthDp"]:
                return None
            if step.get("heightDp") is not None and window["heightDp"] != step["heightDp"]:
                return None
            return window
        window = poll("wm-size readback", readback, timeout=float(step.get("timeout", 20)))
        self.orientation_changed = True
        self.transformed = True
        return window

    def freeform_resize(self, step):
        width_dp, height_dp = step.get("widthDp"), step.get("heightDp")
        if not isinstance(width_dp, int) or not isinstance(height_dp, int):
            raise AdaptiveFailure("freeform-resize needs integer widthDp and heightDp")
        before = self.read_window(require_task=True)
        task_id = before["taskId"]
        density = before["densityDpi"]
        right = round(width_dp * density / 160)
        bottom = round(height_dp * density / 160)
        self.adb.shell("am", "task", "resize", str(task_id), "0", "0", str(right), str(bottom), timeout=10)
        def readback():
            window = self.read_window(require_task=True)
            if window["taskId"] != task_id:
                raise AdaptiveFailure("Persona task changed during freeform resize")
            return window if (window["widthDp"], window["heightDp"]) == (width_dp, height_dp) else None
        window = poll("freeform task bounds %dx%ddp" % (width_dp, height_dp), readback,
                      timeout=float(step.get("timeout", 20)))
        self.orientation_changed = True
        self.transformed = True
        return window

    def focused_node(self, nodes):
        focused = [node for node in nodes if node.attrib.get("focused") == "true"]
        if len(focused) > 1:
            raise AdaptiveFailure("UI XML has ambiguous focused nodes")
        if not focused:
            return None
        node = focused[0]
        return {key: node.attrib.get(key) for key in ("resource-id", "content-desc", "class", "bounds")}

    def collect(self, step_name, state, pid_before, requires=None):
        pid = self.pid()
        if pid is None:
            raise AdaptiveFailure("Persona process is not running")
        if self.route_override is not None:
            route, selected_key = self.route_override, self.selected_key_override
        else:
            route, selected_key = self.read_navigation()
        window = self.read_window()
        device_state = self.device_state()
        nodes = self.ui_tree()
        requires = self.scenario.get("requires", {}) if requires is None else requires
        requirements = page_requirements(requires)
        probe_fields = {"firstVisibleKey", "firstVisibleOffset", "firstVisibleOffsetPx", "columns", "contentBounds",
                        "hingeBounds", "draftHmac", "selection", "focused", "attachments"}
        if not requirements and any(field in probe_fields for field in requires.get("fields", [])):
            requirements = [("default", None, [])]
        texts = [node.attrib.get("text", "") for node in nodes]
        expanded_true = sum(text in {"Show less", "收起", "折りたたむ"} for text in texts)
        expanded_false = sum(bool(re.fullmatch(r"Show all \d+|显示全部 \d+ 个|すべて表示（\d+件）", text)) for text in texts)
        if expanded_true and expanded_false:
            raise AdaptiveFailure("detail agent expansion controls are ambiguous")
        detail_agents_expanded = True if expanded_true else False if expanded_false else None
        snapshot = {
            "scenario": self.scenario["id"], "manifestId": self.scenario.get("manifestId"),
            "step": step_name, "state": state, "serial": self.serial,
            "pidBefore": pid_before, "pidAfter": pid, "initialPid": self.initial_pid,
            "window": window, "widthDp": window["widthDp"], "heightDp": window["heightDp"],
            "orientation": window["orientation"], "deviceState": device_state,
            "postureEvidenceType": self.posture_evidence, "style": self.style, "theme": self.theme,
            "locale": self.adb.shell("getprop", "persist.sys.locale", timeout=5).strip() or "unknown",
            "route": route, "selectedKey": selected_key, "firstVisibleKey": None, "firstVisibleOffsetPx": None,
            "draftHmac": None, "selection": None, "focused": None, "focusedNode": self.focused_node(nodes),
            "attachments": None, "contentBounds": None, "hingeBounds": None, "probePages": {},
            "columns": None, "detailAgentsExpanded": detail_agents_expanded,
            "assertions": list(self.assertions), "inputEvents": list(self.action_log),
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }
        if requirements:
            probe = self.read_probe(pid)
            now = self.elapsed_nanos()
            for alias, page_name, fields in requirements:
                page = self.page_evidence(probe, page_name, now, texts)
                list_evidence = page.get("list")
                composer = page.get("composer")
                bounds = page.get("bounds") or {}
                flattened = {
                    "updatedAtNanos": page["updatedAtNanos"], "list": list_evidence, "composer": composer,
                    "firstVisibleKey": list_evidence.get("firstVisibleKey") if list_evidence else None,
                    "firstVisibleOffsetPx": list_evidence.get("firstVisibleOffsetPx") if list_evidence else None,
                    "columns": list_evidence.get("columns") if list_evidence else None,
                    "draftHmac": composer.get("draftHmac") if composer else None,
                    "selection": composer.get("selection") if composer else None,
                    "focused": composer.get("focused") if composer else None,
                    "attachments": composer.get("attachments") if composer else None,
                    "contentBounds": bounds.get("content"), "hingeBounds": bounds.get("hinge"),
                }
                for field in fields:
                    value = path_value(flattened, field)
                    if value is None or value == "":
                        raise AdaptiveFailure("required probe field is missing: %s.%s" % (alias, field))
                snapshot["probePages"][alias] = flattened
                if alias == "default":
                    snapshot["probeSession"] = probe["session"]
                    snapshot["probeUpdatedAtNanos"] = page["updatedAtNanos"]
                    for field in ("firstVisibleKey", "firstVisibleOffsetPx", "columns", "draftHmac", "selection", "focused",
                                  "attachments", "contentBounds", "hingeBounds"):
                        snapshot[field] = flattened[field]
        required = ["pidAfter", "window.widthDp", "window.heightDp", "route"] + list(requires.get("fields", []))
        for path in required:
            if path in ("selectedKey", "detailAgentsExpanded") and path in snapshot:
                continue
            if path == "columns" and route != "machines":
                continue
            value = path_value(snapshot, path)
            if value is None or value == "":
                raise AdaptiveFailure("required evidence field is missing: %s" % path)
        return snapshot

    def check_assertions(self, step):
        checks = step.get("checks")
        if not isinstance(checks, dict) or not checks:
            raise AdaptiveFailure("assert step needs a non-empty checks object")

        def evaluate(snapshot):
            results = []
            for path, expectation in checks.items():
                operation = "equals"
                expected = expectation
                if isinstance(expectation, str) and expectation == "unchanged":
                    operation = "unchanged"
                if isinstance(expectation, dict) and len(expectation) == 1 and next(iter(expectation)) in ("equals", "unchanged", "matches", "notOverlaps"):
                    operation, expected = next(iter(expectation.items()))
                actual = path_value(snapshot, path)
                if operation == "unchanged":
                    if path == "pid":
                        expected = self.initial_pid
                    elif self.previous_snapshot is not None:
                        expected = path_value(self.previous_snapshot, path)
                    else:
                        raise AdaptiveFailure("unchanged assertion has no previous evidence: %s" % path)
                if operation == "matches":
                    passed = actual is not None and re.search(str(expected), str(actual)) is not None
                elif operation == "notOverlaps":
                    other = path_value(snapshot, str(expected))
                    passed = actual is not None and other is not None and not rectangles_overlap(actual, other)
                else:
                    passed = actual == expected
                results.append({"path": path, "operation": operation, "expected": expected,
                                "actual": actual, "passed": passed})
            return results

        timeout = float(step.get("timeout", 10))
        deadline = time.monotonic() + timeout
        last_error = AdaptiveFailure("no assertion evidence was collected")
        while True:
            try:
                snapshot = self.collect("assert", str(step.get("state", "assert")), self._step_pid_before, step.get("requires"))
                results = evaluate(snapshot)
                if all(result["passed"] for result in results):
                    self.assertions.extend(results)
                    snapshot["assertions"] = list(self.assertions)
                    self.previous_snapshot = snapshot
                    return
                failed = next(result for result in results if not result["passed"])
                last_error = AdaptiveFailure("assertion failed: %s %s %r, actual %r" % (
                    failed["path"], failed["operation"], failed["expected"], failed["actual"]))
            except AdaptiveFailure as error:
                last_error = error
            if time.monotonic() >= deadline:
                raise AdaptiveFailure("assertion or fresh evidence timed out: %s" % last_error)
            time.sleep(0.2)

    def stable_png(self):
        if self.settle:
            time.sleep(self.settle)
        previous = None
        identical = 0
        deadline = time.monotonic() + 20
        while identical < 2:
            data = strip_png(self.adb.run(["exec-out", "screencap", "-p"], timeout=15, binary=True))
            if previous is not None and data == previous:
                identical += 1
            else:
                identical = 0
            previous = data
            if time.monotonic() >= deadline and identical < 2:
                raise AdaptiveFailure("TIMEOUT stable screenshot frames")
            if identical < 2:
                time.sleep(0.3)
        return previous

    def capture(self, step, index):
        name = str(step.get("name", "%02d-capture" % (index + 1)))
        state = str(step.get("state", "state"))
        for value in (name, state):
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", value):
                raise AdaptiveFailure("capture name and state must be filename-safe")
        png = self.stable_png()
        snapshot = self.collect(name, state, self._step_pid_before, step.get("requires"))
        snapshot["assertions"] = list(self.assertions)
        directory = self.output / self.scenario["id"]
        directory.mkdir(parents=True, exist_ok=True)
        base = "%s-%s-%s-%s" % (name, state, self.style, self.theme)
        png_path = directory / (base + ".png")
        sidecar_path = directory / (base + ".json")
        snapshot["pngSha256"] = hashlib.sha256(png).hexdigest()
        for path, data in ((png_path, png), (sidecar_path, (json.dumps(snapshot, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8"))):
            temporary = path.with_suffix(path.suffix + ".tmp")
            temporary.write_bytes(data)
            os.replace(temporary, path)
        self.screenshots += 1
        self.previous_snapshot = snapshot
        print("PASS capture %s" % png_path, flush=True)

    def check_pid(self):
        current = self.pid()
        if self.initial_pid is not None and current != self.initial_pid:
            raise AdaptiveFailure("Persona PID changed from %s to %s" % (self.initial_pid, current))
        return current

    def run(self):
        try:
            if self.reset:
                self.adb.shell("pm", "clear", self.app_id, timeout=20)
                self.adb.shell("pm", "grant", self.app_id, "android.permission.POST_NOTIFICATIONS", timeout=5, check=False)
            for index, step in enumerate(self.scenario["steps"]):
                kind = step["step"]
                self._step_pid_before = self.pid()
                if kind == "launch":
                    self.note_open(step)
                    self.update_fake_posture(step)
                    self.adb.shell(*self._launch_arguments(step, pairing=self.reset), timeout=30)
                    self.wait_ready(step)
                    self.initial_pid = self.pid()
                    if self.initial_pid is None:
                        raise AdaptiveFailure("launch did not start Persona")
                    self.action_log.append({"type": "launch", "reusedProcess": self._step_pid_before == self.initial_pid})
                elif kind == "open":
                    self.note_open(step)
                    self.update_fake_posture(step)
                    self.adb.shell(*self._launch_arguments(step), timeout=30)
                    self.wait_ready(step)
                elif kind == "tap":
                    self.tap(step)
                elif kind == "text":
                    self.text(step)
                elif kind == "key":
                    self.key(step)
                elif kind == "rotate":
                    self.rotate(step)
                elif kind == "posture":
                    self.posture(step)
                elif kind == "wm-size":
                    self.wm_size(step)
                elif kind == "freeform-resize":
                    self.freeform_resize(step)
                elif kind == "assert":
                    self.check_assertions(step)
                elif kind == "capture":
                    self.capture(step, index)
                self.check_pid()
                print("PASS %s %02d %s" % (self.scenario["id"], index + 1, kind), flush=True)
            if self.screenshots == 0:
                raise AdaptiveFailure("scenario produced no screenshots")
        finally:
            self.cleanup()
        print("PASS %s %s: %d screenshots, PID %s" % (self.scenario["id"], self.theme, self.screenshots, self.initial_pid), flush=True)
        return self.screenshots

    def cleanup(self):
        errors = []
        def attempt(operation):
            try:
                operation()
            except Exception as error:
                errors.append(str(error))
        attempt(lambda: self.adb.shell("rm", "-f", UI_PATH, timeout=5))
        attempt(lambda: self.adb.shell("run-as", self.app_id, "rm", "-f", PROBE_PATH, timeout=5))
        attempt(self.reset_modifiers)
        if self.size_changed:
            original = self.original_size.get("override")
            attempt(lambda: self.adb.shell("wm", "size", "%dx%d" % original if original else "reset", timeout=10))
        if self.density_changed:
            original = self.original_density.get("override")
            attempt(lambda: self.adb.shell("wm", "density", str(original) if original else "reset", timeout=10))
        if self.fake_posture_changed:
            attempt(lambda: self.adb.shell(*self._launch_arguments({"value": 3, "fake": True}), timeout=20))
        if self.posture_changed:
            attempt(lambda: self.adb.run(["emu", "posture", "3"], timeout=10))
        if self.orientation_changed:
            def restore_orientation():
                for _ in range(4):
                    if self.read_window()["orientation"] == self.original_orientation:
                        return
                    self.adb.run(["emu", "rotate"], timeout=10)
                    poll("cleanup orientation", lambda: True if self.read_window()["orientation"] == self.original_orientation else None,
                         timeout=5)
                if self.read_window()["orientation"] != self.original_orientation:
                    raise AdaptiveFailure("cleanup could not restore orientation")
            attempt(restore_orientation)
        if errors:
            if sys.exc_info()[0] is None:
                raise AdaptiveFailure("cleanup failed: " + "; ".join(errors))
            print("FAIL cleanup: " + "; ".join(errors), file=sys.stderr, flush=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", required=True)
    parser.add_argument("--theme", choices=("light", "dark"), required=True)
    parser.add_argument("--reset", action="store_true")
    parser.add_argument("--out", default=os.environ.get("OUTPUT"))
    args = parser.parse_args(argv)
    if not args.out:
        parser.error("--out or OUTPUT is required")
    try:
        with open(args.scenario, encoding="utf-8") as source:
            scenario = validate_scenario(normalize_scenario(json.load(source)))
        adb = Adb(os.environ.get("ADB", "adb"), os.environ["SERIAL"])
        runner = AdaptiveRunner(adb, scenario, args.out, args.theme, reset=args.reset)
        runner.run()
        return 0
    except (AdaptiveFailure, KeyError, ValueError, OSError, json.JSONDecodeError) as error:
        print("FAIL %s: %s" % (args.scenario, error), file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    sys.exit(main())
