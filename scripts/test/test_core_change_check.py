"""scripts/hooks/core-change-check.py (#14916): the rule on sample file lists, labels and descriptions, and the CLI against
a fake gh. Run: python3 scripts/test/test_core_change_check.py"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "test"))
from fake_gh import FAKE_GH, rows  # noqa: E402
spec = importlib.util.spec_from_file_location("core_change_check", ROOT / "scripts/hooks/core-change-check.py")
ccc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ccc)

CORE = "server/internal/hub/hub.go"
WHY = "Fixes #1\n\n## Why this must change the core\n\nThe hub must carry the new frame.\n"


class RuleTests(unittest.TestCase):
    def ok(self, files, labels=(), body=""):
        return ccc.evaluate(files, list(labels), body)

    def test_pr_without_core_paths_passes_untouched(self):
        for files in (["docs/a.md"], ["server/internal/modules/voice/v.go"], ["web/src/modules/lark/x.ts", "README.md"],
                      [CORE.replace(".go", "_test.go")], ["server/internal/hub/testdata/a.json"],
                      ["web/src/views/chat/Chat.test.tsx"], [],
                      # the default panel (tier panel) is not Core
                      ["web/src/views/chat/ChatView.tsx", "web/src/App.tsx"], ["android/feature/agents/src/main/kotlin/A.kt"],
                      ["android/app/src/main/kotlin/A.kt", "apple/Persona/Views/Chat/ChatView.swift"],
                      # web/src/lib files that serve one page, menu or popover (the review of #14928)
                      ["web/src/lib/model-panel.ts", "web/src/lib/session-panel.ts", "web/src/lib/node-palette.ts",
                       "web/src/lib/model-id-input.ts", "web/src/lib/machine-alerts.ts", "web/src/lib/machine-capabilities.ts",
                       "web/src/lib/todo-slim-row.ts", "web/src/lib/todo-detail-template.ts", "web/src/lib/session-insights.ts",
                       "web/src/lib/todo-list-filter.ts", "web/src/lib/handoff.ts", "web/src/lib/agent-open.ts",
                       "web/src/lib/todo-details.ts", "web/src/lib/todo-decision-label.ts", "web/src/lib/todo-thread.ts",
                       "web/src/lib/todo-goal-fold.ts", "web/src/lib/todo-stage.ts", "web/src/lib/todo-wait-reason.ts",
                       "web/src/lib/todo-shared-blockers.ts", "web/src/lib/todo-steps.ts", "web/src/lib/agent-actions.ts",
                       "web/src/lib/node-actions.ts"]):
            ok, message = self.ok(files)
            self.assertTrue(ok, (files, message))

    def test_core_change_no_longer_blocked_without_label(self):
        # #17195: a core change merges without the label; evaluate is advisory and always returns True.
        for labels, body in (((), ""), ((), WHY), (("core-change",), "Fixes #1"), (("other",), WHY), (("core-change",), WHY)):
            ok, message = self.ok([CORE], labels, body)
            self.assertTrue(ok, (labels, body))
            self.assertIn(CORE, message)

    def test_message_mentions_label_and_reason_as_optional(self):
        # #17195: the label and section are now optional (advisory), named for visibility only.
        ok, message = self.ok([CORE])
        self.assertTrue(ok)
        self.assertIn("`core-change`", message)
        self.assertIn("## Why this must change the core", message)
        ok, message = self.ok([CORE], ("core-change",), WHY)
        self.assertIn("core change with label and reason", message)

    def test_core_paths_named_even_without_label(self):
        ok, message = self.ok(["web/src/views/chat/ChatView.tsx", "web/src/lib/model-config.ts"])
        self.assertTrue(ok)
        self.assertIn("tier core", message)
        self.assertIn("web/src/lib/model-config.ts", message)
        self.assertNotIn("ChatView.tsx", message)
        for headless in ("android/core/hub/src/main/kotlin/A.kt", "apple/Packages/PersonaWire/Sources/A.swift", "native/app-core/src/lib.rs"):
            self.assertTrue(self.ok([headless])[0], headless)

    def test_lists_the_first_twenty(self):
        files = [f"server/internal/hub/f{i:02d}.go" for i in range(25)]
        ok, message = self.ok(files)
        self.assertIn("25 tier-core files", message)
        self.assertIn("f19.go", message)
        self.assertNotIn("f20.go", message)
        self.assertIn("and 5 more", message)

    def test_reason_rules(self):
        h = ccc.HEADING
        self.assertIsNone(ccc.reason(""))
        self.assertIsNone(ccc.reason(None))
        self.assertIsNone(ccc.reason(f"{h}\n"))
        for placeholder in ("N/A", "n/a", "none", "TBD", "TODO", "-", "...", "…", "<why>", "<!-- say why -->", "[why]", ""):
            self.assertIsNone(ccc.reason(f"{h}\n\n{placeholder}\n\n## Next\ntext"), placeholder)
        self.assertEqual(ccc.reason(f"{h}\n\n<!-- hint -->\nBecause the hub routes it.\n"), "Because the hub routes it.")
        self.assertEqual(ccc.reason(f"intro\n\n{h}  \nReason here\n## Verification\n"), "Reason here")

    def test_reason_must_be_inside_the_section(self):
        self.assertIsNone(ccc.reason(f"{ccc.HEADING}\n\n## Verification\n\nRan the tests.\n"))
        self.assertIsNone(ccc.reason("## Why this must change the core (draft)\n\nBecause.\n"))
        self.assertIsNone(ccc.reason("### Why this must change the core\n\nBecause.\n"))
        self.assertIsNone(ccc.reason("Because.\n"))

    def test_fenced_code_in_the_section(self):
        h = ccc.HEADING
        # A fence delimiter is neither content nor a heading; a `#` line in quoted code does not end the section.
        self.assertEqual(ccc.reason(f"{h}\n\n```sh\n# build it\nmake core\n```\n"), "# build it")
        self.assertEqual(ccc.reason(f"{h}\n\n```\n```\nBecause the hub routes it.\n"), "Because the hub routes it.")
        self.assertEqual(ccc.reason(f"{h}\n\n~~~\n## Not a heading\n~~~\n"), "## Not a heading")
        # An empty fence is no reason, and the next real heading still ends the section.
        self.assertIsNone(ccc.reason(f"{h}\n\n```\n```\n\n## Verification\n\nRan the tests.\n"))
        # A longer fence is not closed by a shorter one, nor a ~ fence by a backtick one.
        self.assertEqual(ccc.reason(f"{h}\n\n````\n```\n# x\n````\n"), "```")
        self.assertEqual(ccc.reason(f"{h}\n\n~~~\n```\n~~~\n\n## Next\ntext\n"), "```")
        # A heading-looking line in a fence BEFORE the section is not the section.
        self.assertIsNone(ccc.reason(f"```\n{h}\nBecause.\n```\n"))
        # Prose after the closing fence is still the section; a fence before the section does not leak into it.
        self.assertEqual(ccc.reason(f"```\ncode\n```\n{h}\n\nBecause.\n"), "Because.")

    def test_a_heading_in_a_comment_does_not_count(self):
        body = f"<!--\n{ccc.HEADING}\nBecause.\n-->\nFixes #1"
        self.assertIsNone(ccc.reason(body))

    def test_pr_text_is_data(self):
        body = f"{ccc.HEADING}\n\n$(touch /tmp/should-not-exist-core-change-check) `rm -rf /`\n"
        self.assertTrue(ccc.reason(body).startswith("$(touch"))
        self.assertFalse(os.path.exists("/tmp/should-not-exist-core-change-check"))


class CliTests(unittest.TestCase):
    def run_cli(self, state, *args):
        with tempfile.TemporaryDirectory() as d:
            gh = Path(d) / "gh"
            gh.write_text("#!" + sys.executable + "\n" + FAKE_GH)
            gh.chmod(0o755)
            (Path(d) / "state.json").write_text(json.dumps(dict(state, pr=state.get("pr", {"labels": state["labels"], "body": state["body"]}))))
            env = dict(os.environ, PATH=d + os.pathsep + os.environ["PATH"], FAKE_GH_STATE=str(Path(d) / "state.json"),
                       FAKE_GH_LOG=str(Path(d) / "log"))
            return subprocess.run([sys.executable, str(ROOT / "scripts/hooks/core-change-check.py"), *args], env=env,
                                  capture_output=True, text=True, timeout=60)

    def test_cli_verdicts(self):
        r = self.run_cli({"labels": [], "body": "", "files": rows(CORE)}, "9")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)  # advisory: no label needed to merge (#17195)
        self.assertIn(CORE, r.stdout)
        r = self.run_cli({"labels": [{"name": "core-change"}], "body": WHY, "files": rows(CORE)}, "9")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        r = self.run_cli({"labels": [], "body": "", "files": rows("docs/a.md")}, "9")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_the_jq_projection_reads_both_names_of_a_rename(self):
        # The fake runs the real jq program per page: a rename out of a core path is found through `previous_filename`.
        r = self.run_cli({"labels": [], "body": "", "files": rows("server/internal/hub/hub.go -> server/internal/modules/voice/hub.go")}, "9")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("server/internal/hub/hub.go", r.stdout)

    def test_a_core_file_beyond_the_first_page_is_found(self):
        # 250 changed files, the one core file last: only --paginate (the fake serves 100 rows a page) reaches it.
        names = [f"docs/note{i:03d}.md" for i in range(249)] + [CORE]
        r = self.run_cli({"labels": [], "body": "", "files": rows(*names)}, "9")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn(CORE, r.stdout)

    def test_the_fake_serves_one_page_without_paginate(self):
        # The fake itself: no --paginate means one page, like the real CLI (so a script that forgets it fails above).
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / "gh").write_text("#!" + sys.executable + "\n" + FAKE_GH)
            (Path(d) / "gh").chmod(0o755)
            (Path(d) / "state.json").write_text(json.dumps({"files": rows(*[f"f{i:03d}" for i in range(250)])}))
            env = dict(os.environ, FAKE_GH_STATE=str(Path(d) / "state.json"), FAKE_GH_LOG=str(Path(d) / "log"))
            def call(*a):
                return subprocess.run([str(Path(d) / "gh"), "api", *a], env=env, capture_output=True, text=True)
            jq = ["--jq", '.[] | [.filename, (.previous_filename // "")] | @tsv']
            url = "repos/o/r/pulls/9/files?per_page=100"
            self.assertEqual(len(call(url, *jq).stdout.split()), 100)
            self.assertEqual(len(call("--paginate", url, *jq).stdout.split()), 250)
            self.assertEqual(call(url + "&page=3", *jq).stdout.split(), [f"f{i:03d}" for i in range(200, 250)])
            self.assertNotEqual(call("--paginate", url, "--jq", ".[].filename").returncode, 0)  # an unknown program is refused

    def test_files_from_overrides_the_api(self):
        with tempfile.TemporaryDirectory() as d:
            names = Path(d) / "names"
            names.write_bytes(b"docs/a.md\0server/internal/hub/hub.go\0")
            r = self.run_cli({"labels": [], "body": "", "files": []}, "--files-from", str(names), "9")
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertIn(CORE, r.stdout)
            names.write_bytes(b"docs/a.md\0")
            r = self.run_cli({"labels": [], "body": "", "files": rows(CORE)}, "--files-from", str(names), "9")
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)  # the API's rows are not read


if __name__ == "__main__":
    unittest.main()
