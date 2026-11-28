import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest
from unittest import mock

import android_adaptive_shots as shots


APP_ID = "com.example.persona.shots"
TASK_DUMP = """
Task{abcdef #42 I=com.example.persona.shots/persona.android.MainActivity}
  mBounds=Rect(0, 0 - 1200, 960)
  CurrentConfiguration: {1.0 320dpi sw600dp w600dp h480dp}
  ActivityRecord{fedcba com.example.persona.shots/persona.android.MainActivity t42}
topResumedActivity=ActivityRecord{fedcba com.example.persona.shots/persona.android.MainActivity t42}
"""
OTHER_TASK_DUMP = TASK_DUMP.replace(APP_ID, "com.example.other")


class FakeAdb:
    def __init__(self):
        self.serial = "emulator-test"
        self.commands = []
        self.ui_failures = 0
        self.png_frames = []
        self.ui_text = ""
        self.probe_text = ""
        self.window = {
            "widthDp": 600, "heightDp": 480, "widthPx": 1200, "heightPx": 960,
            "densityDpi": 320, "orientation": "landscape", "taskId": 42, "bounds": (0, 0, 1200, 960),
            "evidence": "task-configuration",
        }

    def run(self, arguments, timeout=20, binary=False, check=True):
        self.commands.append(list(arguments))
        if binary:
            if self.png_frames:
                return self.png_frames.pop(0)
            return shots.PNG_SIGNATURE + b"frame"
        return b"" if binary else ""

    def shell(self, *arguments, timeout=20, check=True):
        self.commands.append(list(arguments))
        if arguments[:2] == ("wm", "size"):
            return "Physical size: 2560x1600\n"
        if arguments[:2] == ("wm", "density"):
            return "Physical density: 320\n"
        if arguments[:1] == ("pidof",):
            return "42\n"
        if arguments[:1] == ("getprop",):
            return "en-US\n"
        if arguments[:2] == ("dumpsys", "activity"):
            return TASK_DUMP
        if arguments[:2] == ("dumpsys", "window"):
            return "mCurrentRotation=1\n"
        if arguments[:2] == ("dumpsys", "device_state"):
            return "mCommittedState=OPENED\n"
        if arguments[:2] == ("uiautomator", "dump"):
            if self.ui_failures:
                self.ui_failures -= 1
                raise shots.AdaptiveFailure("null root")
            return "UI hierchary dumped to: %s" % arguments[2]
        if arguments[:1] == ("cat",) and arguments[1] == shots.UI_PATH:
            return '<hierarchy><node resource-id="field" text="%s" content-desc="" class="EditText" bounds="[0,0][10,10]" focused="true" /></hierarchy>' % self.ui_text
        if arguments[:2] == ("cat", "/proc/uptime"):
            return "100.00 100.00\n"
        if arguments == ("run-as", APP_ID, "cat", shots.PROBE_PATH):
            return self.probe_text
        if arguments[:2] == ("run-as", APP_ID) and "cat" in arguments and any(arg.endswith("shell-position.json") for arg in arguments):
            return '{"tab":"Agents","chats":{"Agents":{"machineId":"machine","sessionId":"session"}}}'
        if arguments[:2] == ("run-as", APP_ID) and "shell-position.json" in arguments:
            return "no_backup/hub-data/scope/shell-position.json\n"
        return ""


def scenario(steps=None):
    return {
        "id": "test-scene", "manifestId": "adaptive-test", "description": "test",
        "requires": {"fields": []}, "steps": steps or [{"step": "launch"}, {"step": "capture", "name": "one", "state": "flat"}],
    }


def make_runner(adb, output, document=None, reset=False):
    environment = {
        "APP_ID": APP_ID, "MOCK_ADDRESS": "http://127.0.0.1:19000", "SERIAL": adb.serial,
        "SHOTS_SETTLE": "0", "SHOTS_STYLE": "glass",
    }
    with mock.patch.dict(os.environ, environment, clear=False):
        return shots.AdaptiveRunner(adb, document or scenario(), output, "light", reset=reset)


def composer_probe():
    return {"fieldVersion": 1, "session": "test-session", "pid": 42, "updatedAtNanos": 100_000_000_000,
            "pages": {"chat:machine/session": {"updatedAtNanos": 100_000_000_000,
                                               "composer": {"draftLoaded": True}}}}


class AdaptiveShotsTest(unittest.TestCase):
    def test_embedded_assistant_waits_for_its_live_composer_without_a_root_chat(self):
        adb = FakeAdb()
        adb.probe_text = json.dumps(composer_probe())
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(adb, output)
            runner.read_navigation = mock.Mock(return_value=("Persona", None))
            runner.wait_ready({"session": "machine/session", "conversationKind": "assistant", "timeout": 0.01})
            runner.read_navigation.assert_not_called()
            self.assertIn(["run-as", APP_ID, "cat", shots.PROBE_PATH], adb.commands)

    def test_embedded_assistant_keeps_live_chat_identity_and_draft_guards(self):
        for failure in ("wrong-chat", "missing-composer", "unloaded", "wrong-pid", "stale", "stale-page"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as output:
                probe = composer_probe()
                page = probe["pages"]["chat:machine/session"]
                if failure == "wrong-chat":
                    probe["pages"] = {"chat:other/session": page}
                elif failure == "missing-composer":
                    del page["composer"]
                elif failure == "unloaded":
                    page["composer"]["draftLoaded"] = False
                elif failure == "wrong-pid":
                    probe["pid"] = 43
                elif failure == "stale":
                    probe["updatedAtNanos"] = 0
                else:
                    page["updatedAtNanos"] = 0
                adb = FakeAdb()
                adb.probe_text = json.dumps(probe)
                runner = make_runner(adb, output)
                runner.read_navigation = mock.Mock(return_value=("Persona", None))
                with self.assertRaisesRegex(shots.AdaptiveFailure, "composer draft"):
                    runner.wait_ready({"session": "machine/session", "conversationKind": "assistant", "timeout": 0.01})

    def test_agent_keeps_its_root_chat_gate_before_reading_the_composer(self):
        for kind in (None, "agent"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as output:
                adb = FakeAdb()
                adb.probe_text = json.dumps(composer_probe())
                runner = make_runner(adb, output)
                runner.read_navigation = mock.Mock(return_value=("Persona", None))
                step = {"session": "machine/session", "timeout": 0.01}
                if kind is not None:
                    step["conversationKind"] = kind
                with self.assertRaisesRegex(shots.AdaptiveFailure, "selected session"):
                    runner.wait_ready(step)
                runner.read_navigation.assert_called()
                self.assertNotIn(["run-as", APP_ID, "cat", shots.PROBE_PATH], adb.commands)

    def test_navigation_reads_default_and_explicit_tabs(self):
        chat = {"machineId": "machine", "sessionId": "session"}
        cases = [
            ({}, ("Persona", None)),
            ({"chats": {"Persona": chat}}, ("Persona/chat", "machine/session")),
            ({"tab": "Agents", "chats": {"Agents": chat}}, ("Agents/chat", "machine/session")),
        ]
        for position, expected in cases:
            with self.subTest(position=position), tempfile.TemporaryDirectory() as output:
                runner = make_runner(FakeAdb(), output)
                runner.shell_ready = mock.Mock(return_value="shell-position.json")
                runner.adb.shell = mock.Mock(return_value=json.dumps(position))
                self.assertEqual(expected, runner.read_navigation())

    def test_navigation_rejects_explicit_invalid_tabs(self):
        for tab in (None, "", False, 1):
            with self.subTest(tab=tab), tempfile.TemporaryDirectory() as output:
                runner = make_runner(FakeAdb(), output)
                runner.shell_ready = mock.Mock(return_value="shell-position.json")
                runner.adb.shell = mock.Mock(return_value=json.dumps({"tab": tab}))
                with self.assertRaisesRegex(shots.AdaptiveFailure, "shell position has no route tab"):
                    runner.read_navigation()

    def test_evidence_readers_reject_non_objects_like_invalid_json(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            shell = runner.adb.shell
            readers = (
                (shots.PROBE_PATH, lambda: runner.read_probe(42), "adaptive probe is corrupt"),
                (shots.STACK_PROBE_PATH, lambda: runner.read_stack_probe(42), "Assistant stack probe is corrupt"),
                ("shell-position.json", runner.read_navigation, "invalid shell-position.json"),
            )
            for path, read, message in readers:
                for raw in ("{", "null", "[]", "true", '"text"'):
                    with self.subTest(path=path, raw=raw):
                        def evidence(*args, **kwargs):
                            if args[:3] == ("run-as", APP_ID, "cat") and args[-1].endswith(path):
                                return raw
                            return shell(*args, **kwargs)

                        with mock.patch.object(runner.adb, "shell", side_effect=evidence):
                            with self.assertRaisesRegex(shots.AdaptiveFailure, message):
                                read()

    def test_readiness_retries_non_object_evidence(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            runner = make_runner(adb, output)
            shell = adb.shell
            no_composer = composer_probe()
            no_composer["pages"]["chat:machine/session"]["composer"] = None
            probes = iter(("null", json.dumps(no_composer), json.dumps(composer_probe())))

            def evidence(*args, **kwargs):
                if args == ("run-as", APP_ID, "cat", shots.PROBE_PATH):
                    return next(probes)
                return shell(*args, **kwargs)

            with mock.patch.object(adb, "shell", side_effect=evidence), mock.patch.object(shots.time, "sleep"):
                runner.wait_ready({"session": "machine/session", "conversationKind": "assistant", "timeout": 1})
            self.assertEqual(list(probes), [])

    def test_page_evidence_rejects_non_object_page_and_containers(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            now = 100_000_000_000
            invalid = [[]] + [{"updatedAtNanos": now, part: "wrong"} for part in ("list", "composer", "bounds")]
            for page in invalid:
                with self.subTest(page=page), self.assertRaises(shots.AdaptiveFailure):
                    runner.page_evidence({"pages": {"Agents": page}}, "Agents", now)

    def test_steps_never_restart_and_each_theme_may_clear_once(self):
        document = scenario([{"step": "launch"}, {"step": "open", "tab": "Agents"},
                             {"step": "rotate"}, {"step": "capture", "name": "one", "state": "flat"}])
        for reset in (False, True):
            adb = FakeAdb()
            with tempfile.TemporaryDirectory() as output:
                runner = make_runner(adb, output, document, reset=reset)
                runner.wait_ready = mock.Mock()
                runner.rotate = mock.Mock()
                runner.capture = mock.Mock(side_effect=lambda step, index: setattr(runner, "screenshots", 1))
                runner.cleanup = mock.Mock()
                runner.run()
            flattened = [str(item) for command in adb.commands for item in command]
            self.assertNotIn("force-stop", flattened)
            clears = [command for command in adb.commands if command[:2] == ["pm", "clear"]]
            self.assertEqual(1 if reset else 0, len(clears))
            if clears:
                clear_index = adb.commands.index(clears[0])
                self.assertTrue(all(command[:1] == ["wm"] for command in adb.commands[:clear_index]))

    def test_open_uses_main_activity_while_launch_uses_debug_entry(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            launch = runner._launch_arguments({"step": "launch"})
            open_step = runner._launch_arguments({"step": "open"})
            self.assertTrue(any("persona.android.debug.DebugEntryActivity" in arg for arg in launch))
            self.assertIn("--ez", launch)
            self.assertTrue(any(arg.endswith("persona.android.MainActivity") for arg in open_step))
            self.assertNotIn("persona.android.debug.DebugEntryActivity", open_step)

    def test_device_state_reads_optional_name_form(self):
        text = "mCommittedState=Optional[DeviceState{identifier=2, name='HALF_OPENED', app_accessible=true}]"
        self.assertEqual("HALF_OPENED", shots.parse_device_state(text))

    def test_transforms_execute_before_bounded_readback(self):
        for method, step, expected in (
            ("rotate", {"orientation": "landscape"}, ["emu", "rotate"]),
            ("posture", {"value": 2}, ["emu", "posture", "2"]),
            ("wm-size", {"size": "1200x960", "density": 320}, ["wm", "size", "1200x960"]),
            ("freeform-resize", {"widthDp": 600, "heightDp": 480}, ["am", "task", "resize", "42", "0", "0", "1200", "960"]),
        ):
            with self.subTest(method=method), tempfile.TemporaryDirectory() as output:
                adb = FakeAdb()
                runner = make_runner(adb, output)
                runner.read_window = mock.Mock(return_value=adb.window)
                runner.device_state = mock.Mock(return_value="HALF_OPENED")
                with mock.patch.object(shots, "poll", side_effect=shots.AdaptiveFailure("TIMEOUT condition")) as waiting:
                    with self.assertRaisesRegex(shots.AdaptiveFailure, "TIMEOUT"):
                        getattr(runner, method.replace("-", "_"))(step)
                waiting.assert_called_once()
                self.assertIn(expected, adb.commands)

    def test_poll_timeout_is_nonzero_verdict(self):
        with self.assertRaisesRegex(shots.AdaptiveFailure, "TIMEOUT example"):
            shots.poll("example", lambda: None, timeout=0)

    def test_current_task_must_be_persona(self):
        task = shots.parse_persona_task(TASK_DUMP, APP_ID)
        self.assertEqual(42, task.task_id)
        self.assertEqual((0, 0, 1200, 960), task.bounds)
        self.assertEqual((600, 600, 480), task.configuration_dp)
        with self.assertRaisesRegex(shots.AdaptiveFailure, "not Persona"):
            shots.parse_persona_task(OTHER_TASK_DUMP, APP_ID)
        with self.assertRaisesRegex(shots.AdaptiveFailure, "resumed"):
            shots.parse_persona_task(TASK_DUMP.replace("topResumedActivity", "otherActivity"), APP_ID)

    def test_current_task_accepts_the_dumpsys_star_prefix(self):
        dump = """  * Task{abcdef #42 I=com.example.persona.shots/persona.android.MainActivity}
    mBounds=Rect(0, 0 - 1200, 960)
    CurrentConfiguration: {1.0 320dpi sw600dp w600dp h480dp}
    ActivityRecord{fedcba com.example.persona.shots/persona.android.MainActivity t42}
  * Task{ffffff #43 I=com.example.other}
    mBounds=Rect(0, 0 - 10, 10)
  topResumedActivity=ActivityRecord{fedcba com.example.persona.shots/persona.android.MainActivity t42}
"""
        task = shots.parse_persona_task(dump, APP_ID)
        self.assertEqual(42, task.task_id)
        self.assertEqual((0, 0, 1200, 960), task.bounds)
        self.assertEqual((600, 600, 480), task.configuration_dp)
        self.assertNotIn("com.example.other", task.block)

    def test_rotation_and_current_display_dimensions_are_real_evidence(self):
        adb = FakeAdb()
        original = adb.shell
        def shell(*arguments, **kwargs):
            if arguments[:2] == ("dumpsys", "window"):
                return "init=1768x2208 420dpi cur=2208x1768 mCurrentRotation=ROTATION_270\n"
            if arguments[:2] == ("dumpsys", "activity"):
                return OTHER_TASK_DUMP
            if arguments[:2] == ("wm", "density"):
                return "Physical density: 420\n"
            return original(*arguments, **kwargs)
        adb.shell = shell
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(adb, output)
            window = runner.read_window()
            self.assertEqual("landscape", window["orientation"])
            self.assertEqual(841, window["widthDp"])
            self.assertEqual(674, window["heightDp"])

    def test_freeform_right_bottom_coordinates_match_boundary_table(self):
        for width, height, right, bottom in ((599, 480, 1198, 960), (600, 480, 1200, 960),
                                             (759, 400, 1518, 800), (760, 400, 1520, 800),
                                             (839, 480, 1678, 960), (840, 480, 1680, 960)):
            with self.subTest(width=width, height=height), tempfile.TemporaryDirectory() as output:
                adb = FakeAdb()
                runner = make_runner(adb, output)
                target = dict(adb.window, widthDp=width, heightDp=height, widthPx=right, heightPx=bottom)
                runner.read_window = mock.Mock(side_effect=[adb.window, target])
                runner.freeform_resize({"widthDp": width, "heightDp": height})
                self.assertIn(["am", "task", "resize", "42", "0", "0", str(right), str(bottom)], adb.commands)

    def test_ui_dump_deletes_old_xml_and_retries_at_most_five_times(self):
        adb = FakeAdb()
        adb.ui_failures = 4
        with tempfile.TemporaryDirectory() as output, mock.patch.object(shots.time, "sleep") as sleeping:
            runner = make_runner(adb, output)
            self.assertTrue(runner.ui_tree())
        self.assertEqual(4, sleeping.call_count)
        sleeping.assert_has_calls([mock.call(0.5)] * 4)
        self.assertEqual(5, adb.commands.count(["rm", "-f", shots.UI_PATH]))
        adb.ui_failures = 5
        with self.assertRaisesRegex(shots.AdaptiveFailure, "after 5 fresh attempts"):
            runner.ui_tree()
        self.assertEqual(10, adb.commands.count(["rm", "-f", shots.UI_PATH]))
        with self.assertRaises(shots.AdaptiveFailure):
            shots.parse_ui_nodes(b"<hierarchy />")

    def test_png_warning_is_stripped_and_bad_frames_fail(self):
        self.assertEqual(shots.PNG_SIGNATURE + b"data", shots.strip_png(b"warning\n" + shots.PNG_SIGNATURE + b"data"))
        for data in (b"", b"not a png"):
            with self.subTest(data=data), self.assertRaises(shots.AdaptiveFailure):
                shots.strip_png(data)

    def test_stable_frame_timeout_does_not_save_last_frame(self):
        adb = FakeAdb()
        adb.png_frames = [shots.PNG_SIGNATURE + bytes([index]) for index in range(5)]
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(adb, output)
            with mock.patch.object(shots.time, "monotonic", side_effect=[0, 1, 2, 3, 21]), \
                 mock.patch.object(shots.time, "sleep"):
                with self.assertRaisesRegex(shots.AdaptiveFailure, "TIMEOUT stable"):
                    runner.stable_png()
            self.assertEqual([], list(Path(output).glob("*.png")))

    def test_missing_required_probe_field_and_pid_change_fail(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            document = scenario()
            document["requires"] = {"page": "chat", "fields": ["draftHmac"]}
            runner = make_runner(adb, output, document)
            runner.read_probe = mock.Mock(return_value={"session": "session", "pages": {}})
            runner.page_evidence = mock.Mock(return_value={"updatedAtNanos": 99_000_000_000})
            runner.elapsed_nanos = mock.Mock(return_value=100_000_000_000)
            with self.assertRaisesRegex(shots.AdaptiveFailure, "draftHmac"):
                runner.collect("capture", "flat", 42)
            runner.initial_pid = 42
            runner.pid = mock.Mock(return_value=43)
            with self.assertRaisesRegex(shots.AdaptiveFailure, "PID changed"):
                runner.check_pid()

    def test_sidecar_is_complete_and_contains_no_draft_or_hmac_key(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            runner = make_runner(adb, output)
            runner.stable_png = mock.Mock(return_value=shots.PNG_SIGNATURE + b"image")
            runner.collect = mock.Mock(return_value={
                "scenario": "test-scene", "step": "one", "state": "flat", "serial": adb.serial,
                "pidBefore": 42, "pidAfter": 42, "initialPid": 42, "window": adb.window,
                "widthDp": 600, "heightDp": 480, "orientation": "landscape", "deviceState": "OPENED",
                "postureEvidenceType": "none", "style": "glass", "theme": "light", "locale": "en-US",
                "route": "Agents/chat", "selectedKey": "machine/session", "firstVisibleKey": None,
                "firstVisibleOffsetPx": None, "draftHmac": "a" * 64, "selection": {"start": 0, "end": 4},
                "focused": True, "focusedNode": {"resource-id": "field"}, "attachments": [],
                "contentBounds": None, "hingeBounds": None, "probePages": {}, "columns": None, "detailAgentsExpanded": None,
                "assertions": [], "timestamp": "2026-01-01T00:00:00+00:00",
            })
            runner.capture({"name": "one", "state": "flat"}, 0)
            sidecar = Path(output, "test-scene", "one-flat-glass-light.json")
            data = json.loads(sidecar.read_text())
            for key in ("scenario", "step", "serial", "pidBefore", "pidAfter", "window", "orientation",
                        "deviceState", "postureEvidenceType", "style", "theme", "locale", "route", "selectedKey",
                        "firstVisibleKey", "firstVisibleOffsetPx", "draftHmac", "selection", "focused",
                        "focusedNode", "attachments", "contentBounds", "hingeBounds", "probePages", "columns",
                        "detailAgentsExpanded", "assertions", "timestamp", "pngSha256"):
                self.assertIn(key, data)
            self.assertNotIn("secret draft", sidecar.read_text())
            self.assertNotIn("hmacKey", sidecar.read_text())
            self.assertTrue(sidecar.with_suffix(".png").read_bytes().startswith(shots.PNG_SIGNATURE))

    def test_multiple_probe_pages_and_per_step_requirements(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            document = scenario()
            document["requires"] = {"fields": [], "pages": {
                "settings": {"page": "Settings", "fields": ["firstVisibleKey", "contentBounds", "hingeBounds"]},
                "composer": {"page": "chat", "fields": ["draftHmac"]},
            }}
            runner = make_runner(adb, output, document)
            runner.read_probe = mock.Mock(return_value={"session": "session", "pages": {}})
            runner.elapsed_nanos = mock.Mock(return_value=100_000_000_000)
            pages = {
                "Settings": {
                    "updatedAtNanos": 99_000_000_000,
                    "list": {"firstVisibleKey": "notifications", "firstVisibleOffsetPx": 12},
                    "bounds": {"content": {"left": 0, "top": 0, "right": 600, "bottom": 400},
                               "hinge": {"left": 0, "top": 400, "right": 600, "bottom": 440}},
                },
                "chat": {
                    "updatedAtNanos": 99_000_000_000,
                    "composer": {"draftHmac": "a" * 64, "selection": {"start": 0, "end": 1},
                                 "focused": True, "attachments": []},
                },
            }
            runner.page_evidence = mock.Mock(side_effect=lambda probe, name, now, visible=(): pages[name])
            snapshot = runner.collect("capture", "tabletop", 42)
            self.assertEqual({"settings", "composer"}, set(snapshot["probePages"]))
            self.assertEqual("notifications", snapshot["probePages"]["settings"]["firstVisibleKey"])
            self.assertEqual("a" * 64, snapshot["probePages"]["composer"]["draftHmac"])
            local = runner.collect("capture", "tabletop", 42,
                                   {"page": "Settings", "fields": ["firstVisibleKey", "hingeBounds"]})
            self.assertEqual("notifications", local["firstVisibleKey"])
            self.assertEqual(400, local["hingeBounds"]["top"])

    def test_hinge_bounds_assertion_rejects_overlap_and_missing_rectangles(self):
        content = {"left": 0, "top": 0, "right": 600, "bottom": 400}
        touching = {"left": 0, "top": 400, "right": 600, "bottom": 440}
        overlap = {"left": 0, "top": 399, "right": 600, "bottom": 440}
        self.assertFalse(shots.rectangles_overlap(content, touching))
        self.assertTrue(shots.rectangles_overlap(content, overlap))
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner._step_pid_before = 42
            runner.collect = mock.Mock(return_value={"contentBounds": content, "hingeBounds": touching})
            runner.check_assertions({"checks": {"contentBounds": {"notOverlaps": "hingeBounds"}}})
            runner.collect = mock.Mock(return_value={"contentBounds": content, "hingeBounds": overlap})
            with self.assertRaisesRegex(shots.AdaptiveFailure, "notOverlaps"):
                runner.check_assertions({"checks": {"contentBounds": {"notOverlaps": "hingeBounds"}}, "timeout": 0})
            runner.collect = mock.Mock(return_value={"contentBounds": content, "hingeBounds": None})
            with self.assertRaises(shots.AdaptiveFailure):
                runner.check_assertions({"checks": {"contentBounds": {"notOverlaps": "hingeBounds"}}, "timeout": 0})

    def test_machine_columns_detail_expansion_and_compact_dsl(self):
        compact = shots.normalize_scenario({"id": "machines", "description": "test", "requires": {"fields": []}, "steps": [
            {"launch": {"name": "initial"}}, {"open": {"page": "machines"}},
            {"wm-size": {"width": 1080, "height": 2400, "density": 420}},
            {"assert": {"pid": {"unchanged": True}, "columns": {"equals": 2}}},
            {"posture": {"state": 2}}, {"rotate": {"direction": "landscape"}},
            {"capture": {"name": "grid"}},
        ]})
        self.assertEqual("1080x2400", compact["steps"][2]["size"])
        self.assertEqual(2, compact["steps"][4]["value"])
        self.assertEqual("landscape", compact["steps"][5]["orientation"])
        self.assertEqual(2, compact["steps"][3]["checks"]["columns"]["equals"])
        self.assertEqual("capture", compact["steps"][6]["state"])
        shots.validate_scenario(compact)
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            adb.ui_text = "Show less"
            document = scenario()
            document["requires"] = {"page": "Machines", "fields": ["firstVisibleKey", "columns", "detailAgentsExpanded"]}
            runner = make_runner(adb, output, document)
            runner.read_probe = mock.Mock(return_value={"session": "session", "pages": {}})
            runner.elapsed_nanos = mock.Mock(return_value=100_000_000_000)
            runner.page_evidence = mock.Mock(return_value={
                "updatedAtNanos": 99_000_000_000,
                "list": {"firstVisibleKey": "machine-row|1:only", "firstVisibleOffsetPx": 0, "columns": 3},
            })
            runner.note_open({"page": "machines"})
            snapshot = runner.collect("capture", "grid", 42)
            self.assertEqual(3, snapshot["columns"])
            self.assertTrue(snapshot["detailAgentsExpanded"])
            self.assertEqual("machines", snapshot["route"])
            self.assertIsNone(snapshot["selectedKey"])
            self.assertEqual(0, shots.path_value(snapshot, "firstVisibleOffset"))
            runner.transformed = True
            runner.note_open({"page": "machine:concern-needs"})
            self.assertEqual("concern-needs", runner.selected_key_override)
            with self.assertRaisesRegex(shots.AdaptiveFailure, "same page"):
                runner.note_open({"page": "machine:concern-needs"})

    def test_fake_tabletop_can_be_set_and_cleared_without_a_real_posture_command(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            runner = make_runner(adb, output)
            runner.posture({"posture": "tabletop", "fake": True})
            self.assertEqual("fake", runner.posture_evidence)
            self.assertTrue(runner.fake_posture_changed)
            launch = next(command for command in adb.commands if "debug.posture" in command)
            self.assertIn("tabletop", launch)
            runner.posture({"value": 3, "fake": True})
            self.assertEqual("none", runner.posture_evidence)
            self.assertFalse(runner.fake_posture_changed)
            self.assertNotIn(["emu", "posture", "3"], adb.commands)

    def test_open_fake_posture_updates_sidecar_state_and_marks_cleanup(self):
        document = scenario([
            {"step": "launch"},
            {"step": "open", "page": "settings", "probePage": "Settings", "posture": "tabletop", "fake": True},
            {"step": "capture", "name": "one", "state": "tabletop"},
        ])
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output, document)
            runner.wait_ready = mock.Mock()
            runner.capture = mock.Mock(side_effect=lambda step, index: setattr(runner, "screenshots", 1))
            runner.cleanup = mock.Mock()
            runner.run()
            self.assertEqual("fake", runner.posture_evidence)
            self.assertTrue(runner.fake_posture_changed)

    def test_tap_waits_for_the_target_node(self):
        empty = shots.parse_ui_nodes(b'<hierarchy><node resource-id="other" bounds="[0,0][10,10]" /></hierarchy>')
        ready = shots.parse_ui_nodes(b'<hierarchy><node resource-id="field" bounds="[0,0][10,10]" /></hierarchy>')
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner.ui_tree = mock.Mock(side_effect=[empty, ready])
            runner.tap({"resource-id": "field", "timeout": 0.1})
            self.assertEqual(2, runner.ui_tree.call_count)

    def test_tap_index_picks_one_of_several_rows_with_the_same_label(self):
        nodes = shots.parse_ui_nodes(
            b'<hierarchy><node text="Direct" bounds="[0,0][10,10]" /><node text="Direct" bounds="[0,20][10,30]" /></hierarchy>')
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            runner = make_runner(adb, output)
            runner.ui_tree = mock.Mock(return_value=nodes)
            runner.tap({"text": "Direct", "index": 1, "timeout": 0.1})
            self.assertEqual({"type": "tap", "x": 5, "y": 25}, runner.action_log[-1])
            with self.assertRaises(shots.AdaptiveFailure):
                runner.tap({"text": "Direct", "timeout": 0.1})

    def test_route_override_does_not_require_a_shell_checkpoint(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner.route_override = "phone"
            runner.selected_key_override = None
            runner.read_navigation = mock.Mock(side_effect=shots.AdaptiveFailure("shell position has no route tab"))
            snapshot = runner.collect("capture", "settings", 42)
            self.assertEqual("phone", snapshot["route"])
            runner.read_navigation.assert_not_called()

    def test_unchanged_string_shorthand_is_an_operation(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner.initial_pid = 42
            runner.collect = mock.Mock(return_value={"pidAfter": 42})
            runner.check_assertions({"checks": {"pid": "unchanged"}})
            self.assertEqual(42, runner.previous_snapshot["pidAfter"])

    def test_assertions_poll_for_delayed_evidence(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner._step_pid_before = 42
            runner.collect = mock.Mock(side_effect=[{"value": "old"}, {"value": "new"}])
            with mock.patch.object(shots.time, "sleep"):
                runner.check_assertions({"checks": {"value": {"equals": "new"}}, "timeout": 0.1})
            self.assertEqual(2, runner.collect.call_count)
            self.assertEqual("new", runner.previous_snapshot["value"])

    def test_open_page_waits_for_its_probe_evidence(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner.shell_ready = mock.Mock(return_value="shell-position.json")
            runner.read_probe = mock.Mock(return_value={"pages": {}})
            runner.elapsed_nanos = mock.Mock(return_value=100_000_000_000)
            runner.ui_tree = mock.Mock(return_value=[])
            runner.page_evidence = mock.Mock(side_effect=[shots.AdaptiveFailure("not ready"), {"updatedAtNanos": 99_000_000_000}])
            with mock.patch.object(shots.time, "sleep"):
                runner.wait_ready({"page": "machines", "probePage": "Machines"})
            self.assertEqual(2, runner.page_evidence.call_count)

    def test_cleanup_commands_are_checked_and_final_success_follows_cleanup(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            runner = make_runner(adb, output)
            runner.size_changed = True
            runner.density_changed = True
            checks = []
            original = adb.shell
            def tracked(*arguments, **kwargs):
                checks.append(kwargs.get("check", True))
                return original(*arguments, **kwargs)
            adb.shell = tracked
            runner.cleanup()
            self.assertNotIn(False, checks)
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner.wait_ready = mock.Mock()
            runner.capture = mock.Mock(side_effect=lambda step, index: setattr(runner, "screenshots", 1))
            runner.cleanup = mock.Mock(side_effect=shots.AdaptiveFailure("cleanup failed"))
            with io.StringIO() as stdout, mock.patch("sys.stdout", stdout):
                with self.assertRaisesRegex(shots.AdaptiveFailure, "cleanup failed"):
                    runner.run()
                captured = stdout.getvalue()
            self.assertNotIn("PASS test-scene light", captured)

    def test_shell_live_cleanup_preserves_failure_verdicts(self):
        source = Path(__file__).with_name("android.sh").read_text()
        start = source.index("    cleanup_shots() {")
        end = source.index("    trap 'cleanup_shots; exit $?' EXIT", start)
        function = source[start:end]
        for command, adb_status, expected in (("shots-live", 0, 0), ("shots-live", 1, 1), ("shots", 1, 0)):
            with self.subTest(command=command, adb_status=adb_status):
                script = """
set -euo pipefail
COMMAND=%s
SERIAL=emulator-test
APP_ID=com.example.persona.shots
ADB=fake-adb
MOCK_PORT=19000
MOCK_PID=
CLOCK_PID=
CLOCK_STOP=/data/local/tmp/test.stop
SAVED_SETTINGS=
demo() { return 0; }
fake-adb() { return %s; }
%s
cleanup_shots
""" % (command, adb_status, function)
                completed = subprocess.run(["bash", "-s"], input=script, text=True, capture_output=True, timeout=10)
                self.assertEqual(expected, completed.returncode, completed.stderr)
                if expected:
                    self.assertIn("FAIL shots-live cleanup", completed.stderr)

    def test_cleanup_targets_only_owned_device_resources(self):
        with tempfile.TemporaryDirectory() as output:
            adb = FakeAdb()
            runner = make_runner(adb, output)
            runner.size_changed = True
            runner.density_changed = True
            runner.posture_changed = True
            runner.cleanup()
            flattened = [str(item) for command in adb.commands for item in command]
            self.assertNotIn("pkill", flattened)
            self.assertNotIn("kill", flattened)
            self.assertIn(["rm", "-f", shots.UI_PATH], adb.commands)
            self.assertIn(["run-as", APP_ID, "rm", "-f", shots.PROBE_PATH], adb.commands)
            self.assertIn(["wm", "size", "reset"], adb.commands)
            self.assertIn(["wm", "density", "reset"], adb.commands)
            self.assertIn(["emu", "posture", "3"], adb.commands)

    def test_text_preserves_literal_percent_s_after_android_decoding(self):
        for text in ("%s", "100%sure", "%s%s", "%%s", "%ss", "literal %s and spaces",
                     " %s  %s ", "'%s'; $(echo unsafe) & `echo unsafe` \\\"",
                     "hello world", "100% safe", "".join(chr(code) for code in range(32, 127))):
            with self.subTest(text=text), tempfile.TemporaryDirectory() as output:
                adb = FakeAdb()
                runner = make_runner(adb, output)
                runner.text({"text": text})
                commands = [command for command in adb.commands if command[:2] == ["input", "text"]]
                arguments = [shlex.split(command[2]) for command in commands]
                self.assertTrue(all(len(argument) == 1 for argument in arguments))
                # Android InputShellCommand.sendText decodes %s after the remote shell removes quotes.
                received = "".join(argument[0].replace("%s", " ") for argument in arguments)
                self.assertEqual(text, received)
                if "%s" not in text:
                    self.assertEqual(1, len(commands))
                self.assertEqual([{"type": "text", "length": len(text), "ascii": True}],
                                 [action for action in runner.action_log if action["type"] == "text"])

    def test_text_rejects_invalid_input_before_any_text_command(self):
        for text in (None, 17, "", "中文", "valid%s\ninvalid", "valid%s\tinvalid", "\x7f"):
            with self.subTest(text=text), tempfile.TemporaryDirectory() as output:
                adb = FakeAdb()
                runner = make_runner(adb, output)
                with self.assertRaises(shots.AdaptiveFailure):
                    runner.text({"text": text})
                self.assertFalse(any(command[:2] == ["input", "text"] for command in adb.commands))

    def test_scenario_validation_and_ascii_input_boundaries(self):
        document = scenario()
        document["manifestId"] = "r1-adaptive-window"
        self.assertIs(document, shots.validate_scenario(document))
        invalid = scenario([{"step": "launch", "tab": "Agents"}, {"step": "rotate"}, {"step": "open", "tab": "Agents"}])
        with self.assertRaisesRegex(shots.AdaptiveFailure, "cannot restore"):
            shots.validate_scenario(invalid)
        self.assertEqual(["hello%sworld"], shots.ascii_input_arguments("hello world"))
        for text in ("中文", "line\nfeed", ""):
            with self.subTest(text=text), self.assertRaises(shots.AdaptiveFailure):
                shots.ascii_input_arguments(text)

    def test_stack_navigation_preserves_legacy_scenes_by_default(self):
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output)
            runner.read_stack_probe = mock.Mock(return_value={"route": "inbox/mail", "selectedKey": "mail-item"})
            self.assertEqual(("Agents/chat", "machine/session"), runner.read_navigation())
            runner.read_stack_probe.assert_not_called()

    def test_stack_navigation_is_used_only_when_scene_opts_in(self):
        document = scenario()
        document["requires"]["stackProbe"] = True
        with tempfile.TemporaryDirectory() as output:
            runner = make_runner(FakeAdb(), output, document)
            runner.read_stack_probe = mock.Mock(return_value={"route": "inbox/mail", "selectedKey": "mail-item"})
            runner.route_override = "inbox"
            runner.selected_key_override = "override"
            self.assertEqual(("inbox/mail", "mail-item"), runner.read_navigation())
            self.assertIsNone(runner.route_override)
            self.assertIsNone(runner.selected_key_override)
        document["requires"]["stackProbe"] = "yes"
        with self.assertRaisesRegex(shots.AdaptiveFailure, "stackProbe"):
            shots.validate_scenario(document)


if __name__ == "__main__":
    unittest.main()
