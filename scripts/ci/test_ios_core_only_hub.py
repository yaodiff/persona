#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import uuid

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ios_core_only_hub", ROOT / "scripts/ci/ios-core-only-hub.py")
DRIVER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DRIVER)


class IOSCoreOnlyHubProofTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.shots = Path(self.temp.name)
        self.addCleanup(self.temp.cleanup)

    def proof(self, edition, run_id="run"):
        full = edition == "full"
        entries = {name: full for name in (
            "sidebar-health", "settings-live-screen", "settings-meetings", "settings-health", "settings-mail",
            "connector-lark", "connector-lark-bot", "channel-feishu", "voice-extension", "composer-menu-meeting",
            "inbox-lark-filter", "inbox-mail-filter", "inbox-empty-lark-mail")}
        entries["sidebar-meeting-recording"] = True
        captures = [f"{edition}-{look}-{page}.png" for look in ("light", "dark") for page in (
            "assistant", "agents", "inbox", "inbox-beautiful", "todos", "drawer", "settings", "advanced", "connectors", "channels", "devices")]
        for name in captures:
            (self.shots / name).write_bytes(b"png")
        return {
            "schema": 1, "run_id": run_id, "edition": edition,
            "device_credential_issued": True, "current_device_confirmed": True, "paired_devices": 1,
            "core_reads": {"sessions": 0, "inbox_items": 0, "todos": 0},
            "registry": {"active": sorted(["health", "screen-share", "voice"] if full else []),
                         "off_by_hub": [] if full else ["health", "screen-share", "voice"]},
            "module_flags": ["modules"] + [f"module.{name}" for name in DRIVER.smoke.module_ids()] if full else ["modules"],
            "pages": {"agents": "loaded", "inbox": "loaded empty", "todos": "loaded empty"},
            "inbox_styles": {style: {"lark_filter": full, "mail_filter": full, "lark_mail_message": full,
                                     "generic_message": not full}
                             for style in ("glass", "beautiful")},
            "entries": entries, "app_alive_at_end": True, "screenshots": captures,
        }

    def test_accepts_both_real_editions(self):
        for edition in ("core-only", "full"):
            with self.subTest(edition=edition):
                DRIVER.validate_proof(self.proof(edition), edition, "run", self.shots)

    def test_rejects_another_run_or_a_missing_core_page(self):
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(self.proof("core-only", "other"), "core-only", "run", self.shots)
        proof = self.proof("core-only")
        proof["pages"]["inbox"] = "not read"
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "core-only", "run", self.shots)

    def test_module_assertions_are_not_vacuous(self):
        for edition in ("core-only", "full"):
            proof = self.proof(edition)
            proof["entries"]["sidebar-health"] = not (edition == "full")
            with self.subTest(edition=edition), self.assertRaises(DRIVER.Failed):
                DRIVER.validate_proof(proof, edition, "run", self.shots)
        proof = self.proof("core-only")
        proof["module_flags"].append("module.voice")
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "core-only", "run", self.shots)
        proof = self.proof("core-only")
        proof["entries"]["connector-lark"] = True
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "core-only", "run", self.shots)

    def test_inbox_style_presence_is_not_vacuous(self):
        proof = self.proof("core-only")
        proof["inbox_styles"]["beautiful"]["lark_filter"] = True
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "core-only", "run", self.shots)
        proof = self.proof("core-only")
        proof["inbox_styles"]["glass"]["mail_filter"] = True
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "core-only", "run", self.shots)
        proof = self.proof("full")
        proof["inbox_styles"]["beautiful"]["mail_filter"] = False
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "full", "run", self.shots)
        proof = self.proof("full")
        proof["inbox_styles"]["glass"]["generic_message"] = True
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "full", "run", self.shots)

    def test_rejects_missing_captures_and_unproven_device_exchange(self):
        proof = self.proof("full")
        (self.shots / proof["screenshots"][0]).unlink()
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "full", "run", self.shots)
        proof = self.proof("full")
        proof["device_credential_issued"] = False
        with self.assertRaises(DRIVER.Failed):
            DRIVER.validate_proof(proof, "full", "run", self.shots)

    def test_composer_ui_test_is_opt_in_and_uses_the_real_xctest_gesture(self):
        source = (ROOT / "apple/PersonaUITests/CoreOnlyHubLiveUITests.swift").read_text()
        self.assertIn("throw XCTSkip", source)
        self.assertIn("app.launchPinned()", source)
        self.assertIn("addButton.press(forDuration:", source)
        self.assertIn('NSPredicate(format: "enabled == true")', source)
        self.assertIn("Synthetic core-edition fixture", source)
        driver_source = (ROOT / "scripts/ci/ios-core-only-hub.py").read_text()
        self.assertIn('"main_session": "fixture-machine/" + main_session', driver_source)

    def test_machine_fixture_never_starts_a_local_node(self):
        source = (ROOT / "scripts/ci/ios-core-only-hub.py").read_text()
        self.assertIn('"kind": "link"', source)
        self.assertNotIn('"kind": "local"', source)
        self.assertIn('"PERSONA_NODE_SESSION_DIR"', source)
        self.assertIn('"CLAUDE_CONFIG_DIR": str(paths["claude"])', source)
        self.assertIn("wait_for_main_fixture(base, master, main_session, nodes)", source)
        self.assertIn('"herdr_autostart": False', source)
        self.assertIn('"botmux": False', source)

    def test_voice_check_uses_the_real_plugin_identifier(self):
        source = (ROOT / "scripts/ci/ios-core-only-hub.py").read_text()
        self.assertIn('edition_entry(ctl, full, "the Voice extension", element_id="plugin-voice")', source)
        self.assertIn('require(ctl, element_id="plugin-iphone")', source)
        self.assertNotIn('element_id="extension-voice"', source)

    def test_positive_module_controls_wait_but_negative_checks_use_loaded_anchors(self):
        with (patch.object(DRIVER, "wait_for", return_value=True) as waiting,
              patch.object(DRIVER, "find_on_page", return_value=False) as finding):
            self.assertTrue(DRIVER.edition_entry(None, True, "Voice", element_id="plugin-voice"))
            self.assertFalse(DRIVER.edition_entry(None, False, "Voice", element_id="plugin-voice"))
            waiting.assert_called_once()
            finding.assert_called_once()

    def test_connector_status_text_does_not_hide_visible_rows(self):
        class Tree:
            def call(self, command, **kwargs):
                if command == "tree":
                    return {"elements": [
                        {"label": "Mail, None added", "visible": True},
                        {"label": "Lark Bot, Off", "visible": False},
                    ]}
                if command == "scroll":
                    return {"offset": 0}
                raise AssertionError(command)

        ctl = Tree()
        self.assertTrue(DRIVER.find_on_page(ctl, label_prefix="Mail"))
        self.assertFalse(DRIVER.find_on_page(ctl, label="Mail"))
        self.assertFalse(DRIVER.find_on_page(ctl, label_prefix="Lark Bot"))

    def test_hub_shutdown_runs_after_fixture_node_cleanup(self):
        source = (ROOT / "scripts/ci/ios-core-only-hub.py").read_text()
        self.assertIn("stop_fixture_nodes(nodes)\n        smoke.stop_hub(hub, bindir)", source)
        self.assertIn("finally:\n        stop_fixture_nodes(nodes)", source)

    def test_main_session_is_a_uuid_with_a_directly_discoverable_transcript(self):
        def database(*args, **kwargs):
            query = args[0][-1]
            if "select datname" in query:
                return SimpleNamespace(stdout="persona\n", returncode=0, stderr="")
            if "select to_regclass" in query:
                return SimpleNamespace(stdout="t|t\n", returncode=0, stderr="")
            return SimpleNamespace(stdout="", returncode=0, stderr="")

        with patch.object(DRIVER, "run", side_effect=database):
            session = DRIVER.seed_main("fixture-database", str(self.shots / "assistant"))
        self.assertEqual(str(uuid.UUID(session)), session)
        paths = DRIVER.fixture_node_paths(self.shots)
        cwd = paths["assistant"]
        DRIVER.write_main_transcript(paths["claude"], str(cwd), session)
        project = DRIVER.re.sub(r"[^A-Za-z0-9]", "-", str(cwd))
        transcript = paths["claude"] / "projects" / project / f"{session}.jsonl"
        self.assertTrue(transcript.is_file())
        first = json.loads(transcript.read_text().splitlines()[0])
        self.assertEqual(first["sessionId"], session)

    def test_builds_require_the_owner_idle_window(self):
        def reading_at(local_time, seconds):
            def outputs(args, *unused):
                if args[0] == "date":
                    return SimpleNamespace(stdout=local_time + "\n")
                return SimpleNamespace(stdout=f'  "HIDIdleTime" = {seconds * 1_000_000_000}\n')
            with patch.object(DRIVER, "run", side_effect=outputs):
                return DRIVER.idle_reading()

        self.assertEqual(reading_at("2026-11-28T09:00:00+0800", 901)["idle_seconds"], 901)
        self.assertEqual(reading_at("2026-11-28T23:00:00+0800", 1)["idle_seconds"], 1)
        for seconds in (119, 900):
            with self.subTest(seconds=seconds), self.assertRaises(DRIVER.Failed):
                reading_at("2026-11-28T09:00:00+0800", seconds)

    def test_fixture_scope_uses_the_builtin_assistant_and_independent_root(self):
        paths = DRIVER.fixture_node_paths(self.shots)
        self.assertEqual(paths["assistant"], paths["state"] / "assistant")
        self.assertNotEqual(paths["state"], paths["home"] / ".persona")
        for name in ("assistant", "claude", "codex"):
            self.assertTrue(paths[name].is_relative_to(paths["state"]))
            self.assertNotEqual(paths[name], paths["state"])

    def test_proof_contains_no_credentials(self):
        encoded = json.dumps(self.proof("core-only"))
        self.assertNotIn("pairing_token", encoded)
        self.assertNotIn("jp_", encoded)


if __name__ == "__main__":
    unittest.main()
