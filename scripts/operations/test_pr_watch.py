"""Isolated tests for the watcher gates. No real GitHub, model or PR is used."""
import hashlib
import hmac
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import contextmanager, nullcontext
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import pr_watch
import pr_watch_common
import pr_watch_events
import pr_watch_merge
import pr_watch_repair
import pr_watch_review
import pr_watch_verify

SHA_A, SHA_B, SHA_BASE = "a" * 40, "b" * 40, "c" * 40


def pr(head=SHA_A, **overrides):
    result = {"number": 7, "title": "fix: an example", "body": "Summary.\n\nFixes #6",
              "state": "OPEN", "isDraft": False, "headRefOid": head, "headRefName": "fix/example",
              "baseRefName": "main", "baseRefOid": SHA_BASE, "isCrossRepository": False,
              "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "reviewDecision": "", "url": "https://example.com/pr/7"}
    result.update(overrides)
    return result


class WatcherTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="pr-watch-test-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        (self.root / "checkout").mkdir()
        self.config = {"repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                                        "base": "main", "verify": {"profile": "commands", "commands": [["true"]]},
                                        "merge": True, "privacy": None, "notify_command": None}],
                       "reviewer": {}, "webhook": {}, "poll_seconds": 60, "workers": 1,
                       "state": str(self.root / "state.json")}
        self.watcher = pr_watch.Watcher(self.config, verbose=False)
        @contextmanager
        def no_file_lock(root):
            yield
        self.lock_patch = patch.object(pr_watch.pr_watch_merge, "merge_lock", no_file_lock)
        self.lock_patch.start()
        self.addCleanup(self.lock_patch.stop)
        self.watcher.remove_worktree = lambda *args: None
        self.watcher.worktree = lambda *args: self.root / "checkout"
        self.comments = []
        self.watcher.comment = lambda repo, number, body: self.comments.append(body)
        self.watcher.success_review = lambda *args: self.comments.append("passed")

    def test_clean_head_runs_both_gates_then_merges(self):
        calls = []
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "command": ["true"], "ok": True, "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(merge.call_count, 1)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_findings_are_published_once_and_never_merge(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P1", "summary": "wrong", "suggestion": "fix it"}]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge") as merge, \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as api:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        verify.assert_not_called()
        merge.assert_not_called()
        self.assertEqual(api.call_count, 1)
        self.assertEqual(api.call_args.args[0][:2], ["gh", "api"])

    def test_base_change_invalidates_same_head_evidence(self):
        new_base = "d" * 40
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(), pr(baseRefOid=new_base), pr(baseRefOid=new_base)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["base"], new_base)

    def test_snapshot_failure_does_not_mark_head_failed(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=pr_watch_common.WatchError("closed")), \
                patch.object(self.watcher.state, "put") as put:
            self.watcher.process("example/one", 7)
        put.assert_not_called()

    def test_dry_run_never_calls_github_writes_or_merge(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", wraps=self.watcher.merge) as merge, \
                patch.object(pr_watch, "run") as api:
            self.watcher.process("example/one", 7)
        merge.assert_called_once()
        api.assert_not_called()
        self.assertEqual(self.comments, [])
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "passed")
        self.assertFalse(self.watcher.state.get("example/one", 7)["review_posted"])

    def test_live_after_dry_run_publishes_and_merges(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]):
            self.watcher.process("example/one", 7)
        self.watcher.dry_run = False
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as live_review, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        live_review.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(self.comments, ["passed"])

    def test_prepare_failures_retry_with_bounded_backoff(self):
        for attempt in range(3):
            with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                    patch.object(self.watcher, "worktree", side_effect=pr_watch_common.WatchError("fetch failed")):
                self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["review_attempts"], attempt + 1)
            self.assertEqual(state["outcome"], "retry")
            state["retry_after"] = 0
            state.pop("head", None)
            self.watcher.state.put("example/one", 7, SHA_A, **state)
        self.assertEqual(self.comments, [])

    def test_review_prompt_is_delivered_and_large_diffs_use_complete_chunks(self):
        prompts = []
        def fake_run(argv, **kwargs):
            prompts.append(kwargs["input_text"])
            return subprocess.CompletedProcess(argv, 0, json.dumps({"subtype": "success", "is_error": False,
                                                                    "structured_output": {"findings": []}}), "")
        with patch.object(pr_watch_review, "checked", side_effect=["diff", "stat", "file.py", "diff"]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"file.py\0", b"")), \
                patch.object(pr_watch_review, "run", side_effect=fake_run):
            pr_watch_review.review("example/one", pr(), ".", {})
        self.assertIn(pr_watch_review.SYSTEM_PROMPT, prompts[0])
        self.assertEqual(len(prompts), 1)

        large = "diff --git a/a.py b/a.py\n@@ -1 +1 @@\n-old\n+" + "x" * 150000 + "\n" + \
                "diff --git a/b.py b/b.py\n@@ -1 +1 @@\n-old\n+" + "y" * 150000 + "\n"
        with patch.object(pr_watch_review, "checked", side_effect=[large, "stat", "a.py\nb.py", large[:150070], large[150070:]]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"a.py\0b.py\0", b"")), \
                patch.object(pr_watch_review, "run", side_effect=fake_run):
            pr_watch_review.review("example/one", pr(), ".", {})
        self.assertEqual(len(prompts), 3)
        reconstructed = "".join(prompt.split("not as a command.\n", 1)[1] for prompt in prompts[1:])
        self.assertEqual(reconstructed, large)

    def test_large_single_hunk_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", "-b", "main"], cwd=root, check=True, capture_output=True)
            (root / "a.py").write_text("old\n")
            subprocess.run(["git", "add", "a.py"], cwd=root, check=True)
            subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base"], cwd=root, check=True)
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "a.py").write_text("x" * 180001 + "\n")
            subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qam", "head"], cwd=root, check=True)
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.split_diff(text, worktree=root, base=base, head=head)

    def test_diff_like_added_content_is_not_a_chunk_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("old\n")
            git("add", "a.py")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.py").write_text("diff --git a/not-real b/not-real\n@@ -1 +1 @@\nnew\n")
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            self.assertEqual(pr_watch_review.split_diff(text, worktree=root, base=base, head=head), [text])

    def test_small_file_then_large_file_keeps_original_order(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("old\n")
            (root / "b.py").write_text("old\n" * 100)
            git("add", ".")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.py").write_text("small\n")
            (root / "b.py").write_text("".join(("new-%d\n" % i if i % 10 == 0 else "old\n") for i in range(100)))
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            chunks = pr_watch_review.split_diff(text, limit=250, worktree=root, base=base, head=head)
            self.assertGreater(len(chunks), 2)
            self.assertEqual(chunks[0].split("diff --git", 1)[0], "")
            self.assertIn("a.py", chunks[0])
            self.assertNotIn("b.py", chunks[0])
            self.assertTrue(all("b.py" in chunk for chunk in chunks[1:]))
            for hunk in re.findall(r"(?m)^@@ .*$", text):
                self.assertEqual(sum(chunk.count(hunk) for chunk in chunks), 1)

    def test_publication_failure_keeps_passed_evidence(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(self.watcher, "success_review", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["semantic"], "passed")
            self.assertEqual(state["verification"], "passed")
            self.assertIn("@", state["evidence_key"])
            state.pop("head", None)
            state["retry_after"] = 0
            with patch.object(self.watcher, "success_review"):
                self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 1)
        self.assertEqual(verify.call_count, 1)

    def test_title_and_test_only_visual_exemption(self):
        for title in ("test(web): wait for lazy details", "test(ios): correct an assertion",
                      "fix(ios): repair a view", "fix(android): repair a layout"):
            self.assertTrue(pr_watch_repair.test_or_fix(pr(title=title), ["web/src/views/ChatView.tsx"]))
        self.assertFalse(pr_watch_repair.test_or_fix(pr(title="feat(ios): a new view"), ["apple/Persona/Views/ChatView.swift"]))
        self.assertTrue(pr_watch_repair.test_or_fix(pr(title="feat(web): refine a test"),
                                                     ["web/src/views/chat/ContextGroup.test.tsx"]))
        self.assertFalse(pr_watch_repair.test_or_fix(pr(title="chore: change a view"),
                                                      ["web/src/views/ChatView.tsx"]))

    def test_test_only_web_change_runs_changed_tests_not_full_lanes(self):
        names = "web/src/views/chat/ContextGroup.test.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names):
            commands = pr_watch_verify.persona_checks(".", pr(title="test(web): refine a test"), 1, {})
        self.assertEqual(commands[2], ("Changed web tests", ["node", "./node_modules/vitest/vitest.mjs", "run",
                                                             "src/views/chat/ContextGroup.test.tsx"], "web"))
        guard = next(command[1] for command in commands if command[0] == "Changed test-file baseline guards")
        self.assertEqual(guard[guard.index("--worktree") + 1], ".")
        self.assertFalse(any(command[0] in {"CI web", "CI scripts"} for command in commands))
        self.assertTrue(any(command[0] == "Web typecheck" for command in commands))
        self.assertTrue(any(command[0] == "Privacy and documentation guards" for command in commands))
        with patch.object(pr_watch_verify, "run_check", return_value={"name": "test", "ok": True}) as check, \
                patch.object(pr_watch_verify, "checked", return_value=names):
            pr_watch_verify.verify("example/one", pr(), ".", {"profile": "persona"})
        self.assertEqual(check.call_args_list[0].kwargs["cwd"], "web")
        self.assertEqual(check.call_args_list[2].kwargs["cwd"], "web")
        self.assertTrue(any(command[0] == "Web extension mirror cleanup tests" for command in commands))

    def test_production_apple_fix_does_not_use_test_only_fast_path(self):
        self.assertIsNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                               ["apple/Persona/Views/ChatView.swift"]))
        self.assertIsNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                               ["apple/Shared/Models.swift"]))
        self.assertIsNotNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                                  ["apple/PersonaTests/ChatTests.swift"]))
        self.assertIsNotNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                                  ["apple/PersonaUITests/MinutesCardsUITests.swift"]))

    def test_apple_test_only_change_uses_portable_guards_without_macos_adapter(self):
        names = "apple/PersonaUITests/MinutesCardsUITests.swift\n"
        with patch.object(pr_watch_verify, "checked", return_value=names):
            commands = pr_watch_verify.persona_checks(".", pr(title="test(ios): refine UI assertions"), 1, {})
        self.assertIn(("Apple test-only review policy", ["true"]), commands)
        self.assertTrue(any(command[0] == "Privacy and documentation guards" for command in commands))
        self.assertFalse(any(command[0].startswith("CI ") for command in commands))

    def test_shared_test_support_keeps_full_lanes(self):
        names = "web/src/test/setup.ts\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "desktop": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="test(web): update shared setup"), 1, {})
        self.assertEqual(commands[:2], [("CI web", ["bash", "scripts/ci/web.sh"]),
                                    ("CI desktop", ["bash", "scripts/ci/desktop.sh"])])

    def test_changed_go_and_python_tests_use_focused_commands(self):
        names = ["server/internal/agents/cache_test.go", "scripts/operations/test_pr_watch.py"]
        with patch.object(pr_watch_verify, "checked", return_value="\n".join(names) + "\n"):
            commands = pr_watch_verify.persona_checks(".", pr(title="test: refine relevant tests"), 1, {})
        self.assertEqual(commands[0][0], "Changed Go tests")
        self.assertEqual(commands[0][1][:2], ["python3", "-I"])
        self.assertTrue(commands[0][1][2].endswith("pr_watch_go_tests.py"))
        self.assertEqual(commands[0][1][-1], "./internal/agents")
        format_command = next(command[1] for command in commands if command[0] == "Changed Go formatting")
        self.assertTrue(format_command[2].endswith("pr_watch_go_format.py"))
        self.assertTrue(format_command[2].startswith(str(HERE.parent.parent)))
        self.assertTrue(any(command[0] == "Changed Go formatting" for command in commands))
        self.assertIn(("Changed Python tests", ["python3", "-I", "-B", "-m", "unittest", "test_pr_watch.py"],
                       "scripts/operations"), commands)
        self.assertFalse(any(command[0].startswith("CI ") for command in commands))
        calls = []
        def check(name, argv, root, timeout, environ=None, cwd=None):
            calls.append(cwd)
            return {"name": name, "command": argv, "ok": True, "summary": "", "stdout": "", "stderr": ""}
        with patch.object(pr_watch_verify, "run_check", side_effect=check), \
                patch.object(pr_watch_verify, "checked", return_value="\n".join(names) + "\n"):
            pr_watch_verify.verify("example/one", pr(), ".", {"profile": "persona"})
        self.assertEqual(calls, [None, None, None, "scripts/operations", None, None])

    def test_changed_sleep_guard_requires_lowering_the_baseline(self):
        import pr_watch_test_guards
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True, capture_output=True)
            baseline = root / "scripts/ci/sleep_baseline.json"
            baseline.parent.mkdir(parents=True)
            baseline.write_text('{"server/internal/example/example_test.go": 2}\n')
            test = root / "server/internal/example/example_test.go"
            test.parent.mkdir(parents=True)
            test.write_text("package example\n")
            with patch.object(pr_watch_test_guards, "checked", return_value=baseline.read_text()):
                violations = pr_watch_test_guards.baseline_guard("server/internal/example/example_test.go", root)
            self.assertIn("lower the baseline", violations[0])

    def test_go_test_runner_uses_disposable_postgres_and_cleanup(self):
        import pr_watch_go_tests
        calls = []
        def fake_checked(argv, **kwargs):
            calls.append((argv, kwargs))
            return ""
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(pr_watch_go_tests, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_go_tests.subprocess, "run") as cleanup:
            package = Path(directory) / "server/internal/agents"
            package.mkdir(parents=True)
            (package / "cache_test.go").write_text("package agents\n")
            pr_watch_go_tests.main(["--worktree", directory, "./internal/agents"])
        command = next(argv for argv, _ in calls if "server_test_env.py" in argv[2])
        self.assertIn("-timeout=10m", command)
        self.assertEqual(command[-1], "./internal/agents")
        cleanup.assert_called_once()
        self.assertEqual(cleanup.call_args.args[0][:3], ["docker", "rm", "-f"])

        calls.clear()
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(pr_watch_go_tests, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_go_tests.subprocess, "run"), \
                patch.object(pr_watch_go_tests, "run", return_value=subprocess.CompletedProcess([], 0, "TestA\nTestB\nok\n", "")), \
                patch.object(pr_watch_go_tests.go_partitions, "patterns", return_value=["^(TestA)$", "^(TestB)$"]) as partitions:
            (Path(directory) / "server").mkdir()
            pr_watch_go_tests.main(["--worktree", directory, "./internal/node"])
        partitions.assert_called_once()
        runs = [argv for argv, _ in calls if "server_test_env.py" in argv[2]]
        self.assertEqual(len(runs), 2)
        self.assertEqual([run[-1] for run in runs], ["./internal/node", "./internal/node"])
        self.assertTrue(all("-timeout=10m" in run for run in runs))

    def test_argument_driven_python_verification_keeps_full_scripts_lane(self):
        names = "scripts/test/test_rolling_compatibility.py\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="test: update an argument-driven check"), 1, {})
        self.assertEqual(commands[0], ("CI scripts", ["bash", "scripts/ci/scripts.sh"]))

    def test_documentation_guard_reads_pr_not_watcher_root(self):
        import pr_watch_test_guards
        module = type("Guard", (), {"ROOT": None, "checked_root": None})
        module.TestRepository = type("TestRepository", (unittest.TestCase,), {
            "test_root": lambda self: setattr(module, "checked_root", module.ROOT)
        })
        with patch.object(pr_watch_test_guards, "load_guard", return_value=module):
            pr_watch_test_guards.documentation_guard("test_doc_paths.py", Path("/tmp/pr-root"))
        self.assertEqual(module.checked_root, Path("/tmp/pr-root"))
        with patch.object(pr_watch_test_guards, "load_guard", return_value=module):
            pr_watch_test_guards.documentation_guard("test_doc_paths.py", Path("/tmp/watcher-root"))
        self.assertEqual(module.checked_root, Path("/tmp/watcher-root"))

    def test_go_format_guard_checks_each_changed_file(self):
        import pr_watch_go_format
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test = root / "server/internal/example/example_test.go"
            test.parent.mkdir(parents=True)
            test.write_text("package example\n")
            result = subprocess.run(["python3", "-I", str(HERE / "pr_watch_go_format.py"),
                                     "--worktree", directory, "server/internal/example/example_test.go"],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            with patch.object(pr_watch_go_format, "checked", return_value=""):
                self.assertEqual(pr_watch_go_format.main(["--worktree", directory,
                                                           "server/internal/example/example_test.go"]), 0)
            with patch.object(pr_watch_go_format, "checked", return_value="example_test.go\n"):
                with self.assertRaises(pr_watch_common.WatchError):
                    pr_watch_go_format.main(["--worktree", directory,
                                             "server/internal/example/example_test.go"])

    def test_production_change_does_not_run_unrelated_full_scripts_lane(self):
        names = "server/internal/agents/cache.go\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"server": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="perf: optimize a cache"), 1, {})
        self.assertEqual(commands, [("CI server", ["bash", "scripts/ci/server.sh"])] +
                         [command for command in commands if command[0] == "Exact-head privacy scan"])

    def test_shared_ci_change_keeps_full_scripts_lane(self):
        names = "scripts/ci/server.sh\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"server": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="fix: adjust server CI"), 1, {})
        self.assertEqual(commands[:2], [("CI server", ["bash", "scripts/ci/server.sh"]),
                                       ("CI scripts", ["bash", "scripts/ci/scripts.sh"])])

    def test_zero_test_python_result_is_failed_evidence(self):
        result = {"name": "Changed Python tests", "command": ["true"], "ok": True, "summary": "",
                  "stdout": "Ran 0 tests in 0.000s\n\nOK", "stderr": ""}
        with patch.object(pr_watch_verify, "run_check", return_value=result):
            with self.assertRaises(pr_watch_common.VerificationError):
                pr_watch_verify.verify("example/one", pr(), ".", {"profile": "commands", "commands": [
                    {"name": "Changed Python tests", "argv": ["true"]}]})
        self.assertFalse(result["ok"])
        self.assertIn("Ran 0 tests", result["summary"])

    def test_focused_test_change_keeps_other_selected_platform_lanes(self):
        for name, lane in (("apple/PersonaTests/ChatTests.swift", "CI server"),
                           ("scripts/mock/test_desktops_mock.py", "CI desktop")):
            with self.subTest(name=name):
                with patch.object(pr_watch_verify, "checked", return_value=name + "\n"), \
                        patch.object(pr_watch_verify, "load_paths") as selector:
                    selector.return_value.select.return_value = {lane.split()[1]: True, "scripts": True}
                    commands = pr_watch_verify.persona_checks(".", pr(title="test: refine a client contract"), 1, {})
                self.assertIn((lane, ["bash", "scripts/ci/" + lane.split()[1] + ".sh"]), commands)

    def test_real_run_check_detects_successful_zero_test_suite(self):
        result = pr_watch_verify.run_check("Changed Python tests", ["python3", "-I", "-c",
                                                                    "import sys; sys.stderr.write('Ran 0 tests in 0.000s\\nOK\\n')"],
                                           ".", 30)
        self.assertIn("Ran 0 tests", result["stderr"])
        with patch.object(pr_watch_verify, "run_check", return_value=result):
            with self.assertRaises(pr_watch_common.VerificationError):
                pr_watch_verify.verify("example/one", pr(), ".", {"profile": "commands", "commands": [
                    {"name": "Changed Python tests", "argv": ["true"]}]})
        self.assertFalse(result["ok"])

    def test_deleted_go_tests_are_not_reported_as_passing(self):
        import pr_watch_go_tests
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / "server/internal/example").mkdir(parents=True)
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_go_tests.main(["--worktree", directory, "./internal/example"])

    def test_privacy_environment_retains_maintainer_config_not_build_home(self):
        import pr_watch_test_guards
        with patch.dict(os.environ, {"HOME": "/maintainer-home", "XDG_CONFIG_HOME": "/maintainer-config",
                                     "GH_TOKEN": "not-for-scanner"}):
            env = pr_watch_test_guards.privacy_environment(Path("/tmp/pr-checkout"))
        self.assertEqual(env["XDG_CONFIG_HOME"], "/maintainer-config")
        self.assertNotEqual(env["HOME"], "/maintainer-home")
        self.assertEqual(env["PERSONA_PRIVACY_ROOT"], "/tmp/pr-checkout")
        self.assertNotIn("GH_TOKEN", env)

    def test_production_privacy_scan_uses_maintainer_environment_with_pr_root(self):
        calls = []
        def check(name, argv, root, timeout, environ=None, cwd=None):
            calls.append(environ)
            return {"name": name, "command": argv, "ok": True, "summary": "", "stdout": "", "stderr": ""}
        with patch.object(pr_watch_verify, "checked", return_value="server/internal/agents/cache.go\n"), \
                patch.object(pr_watch_verify, "run_check", side_effect=check):
            pr_watch_verify.verify("example/one", pr(), ".", {"profile": "persona"}, environ={"HOME": "/isolated-home"})
        self.assertEqual(calls[-1]["PERSONA_PRIVACY_ROOT"], ".")
        self.assertNotEqual(calls[-1].get("HOME"), "/isolated-home")

    def test_fix_ui_change_runs_checks_without_visual_adapter(self):
        names = "web/src/views/ChatView.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="fix(web): repair a view"), 1, {})
        self.assertEqual(commands[0], ("CI web", ["bash", "scripts/ci/web.sh"]))
        self.assertEqual(commands[1][0], "Exact-head privacy scan")

    def test_feature_ui_change_still_requires_visual_adapter(self):
        names = "web/src/views/ChatView.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "scripts": True}
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_verify.persona_checks(".", pr(title="feat(web): a new view"), 1, {})

    def test_superseded_prerequisite_failure_retries_only_verification(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               format=1, base=SHA_BASE, semantic="passed", findings=[],
                               review_fingerprint="reviewed", outcome="failed", failure_posted=True,
                               retry_after=10**12, repair_rounds=1, repair_attempts=3,
                               failure="UI changes need visual evidence")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True}]), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        review.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(state["outcome"], "merged")
        self.assertEqual(state["repair_rounds"], 1)
        self.assertEqual(state["verification_policy"], pr_watch.VERIFICATION_POLICY_VERSION)

    def test_policy_migration_preserves_completed_gates_and_terminal_repairs(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        for outcome, fields in (
                ("merged", {"verification_policy": 1, "merged": True}),
                ("passed", {"verification": "passed", "verification_policy": 1, "review_posted": True}),
                ("passed_with_base_failures", {"verification": "base_failure", "verification_policy": 1, "review_posted": True}),
                ("findings", {"semantic": "findings", "review_posted": True}),
                ("repair_exhausted", {"repair_exhausted": True, "repair_rounds": 2}),
        ):
            with self.subTest(outcome=outcome):
                self.watcher.state.data["prs"].pop("example/one#7", None)
                self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                                       format=1, base=SHA_BASE, outcome=outcome, **fields)
                with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                        patch.object(pr_watch, "semantic_review") as review, \
                        patch.object(pr_watch, "verify") as verify, \
                        patch.object(self.watcher, "merge") as merge:
                    self.watcher.process("example/one", 7)
                review.assert_not_called()
                verify.assert_not_called()
                merge.assert_not_called()

    def test_verification_failure_uses_bounded_repair(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("real test failed")), \
                patch.object(self.watcher, "repair_verification", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()
        self.assertNotEqual(self.watcher.state.get("example/one", 7)["outcome"], "failed")

    def test_repair_stops_after_two_rounds(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="one", repair_rounds=2)
        with patch.object(self.watcher, "comment") as comment, \
                patch.object(pr_watch_repair, "main_failure_comparison") as compare:
            result = self.watcher.repair_verification(self.config["repositories"][0], pr(), self.root, pr_watch_common.WatchError("still failing"))
        self.assertFalse(result)
        compare.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "repair_exhausted")
        self.assertIn("two rounds", comment.call_args.args[2])

    def test_main_owned_failure_is_not_a_passed_test(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="one", repair_rounds=0)
        with patch.object(pr_watch_repair, "main_failure_comparison", return_value={"reproduced": True, "packages": ["./internal/node"]}), \
                patch.object(self.watcher, "comment"):
            result = self.watcher.repair_verification(self.config["repositories"][0], pr(), self.root, pr_watch_common.WatchError("--- FAIL: TestBase"))
        self.assertTrue(result)
        self.assertEqual(self.watcher.state.get("example/one", 7)["verification"], "base_failure")

    def test_base_failure_continues_to_exact_head_merge(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("--- FAIL: TestBase")), \
                patch.object(self.watcher, "repair_verification", return_value="base_failure"), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        merge.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_repair_commit_is_verified_at_new_head_before_push(self):
        with tempfile.TemporaryDirectory() as directory:
            calls = []
            def fake_checked(argv, **kwargs):
                calls.append(argv)
                if argv[:2] == ["git", "rev-parse"] and "FETCH_HEAD" in argv:
                    return SHA_BASE + "\n"
                if argv[:2] == ["git", "rev-parse"]:
                    return SHA_B + "\n"
                if argv[:2] == ["git", "ls-remote"]:
                    return SHA_A + "\trefs/heads/fix/example\n"
                if argv[:3] == ["git", "diff", "--name-only"]:
                    return ""
                if argv[:4] == ["git", "diff", "--cached", "--name-only"]:
                    return "repair.py\0"
                if argv[:4] == ["git", "diff", "--cached", "--stat"]:
                    return "repair.py | 1 +\n"
                if argv[:4] == ["git", "diff", "--name-only", "--no-renames"]:
                    return "repair.py\0"
                if argv[:3] == ["git", "diff", "--name-only"]:
                    return ""
                if argv[:3] == ["git", "diff", "--cached"]:
                    return "repair.py | 1 +\n"
                return ""
            def fake_verify(repo, repaired_pr, *args):
                calls.append(repaired_pr["headRefOid"])
                return []
            with patch.object(pr_watch_repair, "pr_snapshot", return_value=pr()), \
                    patch.object(pr_watch_repair, "verify", side_effect=fake_verify), \
                    patch.object(pr_watch_repair, "checked", side_effect=fake_checked), \
                    patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}), \
                    patch.object(pr_watch_repair, "repair_repository_copy", return_value=nullcontext(directory)):
                pr_watch_repair.commit_and_push_repair("example/one", pr(), directory, SHA_BASE, "failure", {"profile": "commands", "commands": [["true"]]})
        self.assertIn(SHA_B, calls)
        commit_index = next(index for index, argv in enumerate(calls) if isinstance(argv, list) and "commit" in argv)
        push_index = next(index for index, argv in enumerate(calls) if isinstance(argv, list) and "push" in argv)
        self.assertLess(commit_index, calls.index(SHA_B))
        self.assertLess(calls.index(SHA_B), push_index)
    def test_main_comparison_requires_named_tests_and_packages(self):
        with patch.object(pr_watch_repair, "checked", return_value=""):
            comparison = pr_watch_repair.main_failure_comparison("example/one", pr(), ".", "CI server failed")
        self.assertFalse(comparison["reproduced"])
        self.assertEqual(pr_watch_repair.failed_test_names("--- PASS: TestGood\n--- FAIL: TestBad/case\n"), ["TestBad"])
        pr_output = "--- FAIL: TestBase\n--- FAIL: TestPR\nAssertionError: PR defect"
        base_output = "--- FAIL: TestBase\nAssertionError: base defect"
        self.assertNotEqual(pr_watch_repair.failure_test_ids(pr_output), pr_watch_repair.failure_test_ids(base_output))

    def test_continuation_pr_links_original_issue(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def fake_checked(argv, **kwargs):
                if argv[:2] == ["git", "rev-parse"]:
                    if "FETCH_HEAD" in argv:
                        return SHA_BASE + "\n"
                    return SHA_B + "\n"
                return ""
            with patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: pr(head=SHA_B if number == 9 else SHA_A)), \
                    patch.object(pr_watch_repair, "verify", return_value=[]), \
                    patch.object(pr_watch_repair, "checked", side_effect=fake_checked), \
                    patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "", "")), \
                    patch.object(pr_watch_repair, "gh_json", side_effect=[[], {"number": 9}]) as created:
                pr_watch_repair.commit_and_push_repair("example/one", pr(isCrossRepository=True), root, SHA_BASE, "failure", {"profile": "commands", "commands": [["true"]]}, allow_merge_only=True)
        self.assertIn("Refs #6", created.call_args_list[1].kwargs["input_text"])

    def test_every_failed_lane_must_reproduce_on_main(self):
        failures = [{"command": ["test", "one"], "output": "--- FAIL: TestA\n"},
                    {"command": ["test", "two"], "output": "--- FAIL: TestB\n"}]
        def fake_run(argv, **kwargs):
            if argv[:2] == ["git", "worktree"]:
                return subprocess.CompletedProcess(argv, 0, "", "")
            return subprocess.CompletedProcess(argv, 1, "--- FAIL: TestA\n", "") if "one" in argv else subprocess.CompletedProcess(argv, 0, "ok\n", "")
        with patch.object(pr_watch_repair, "checked", return_value=""), \
                patch.object(pr_watch_repair, "run", side_effect=fake_run):
            comparison = pr_watch_repair.main_failure_comparison("example/one", pr(), self.root, "", failures=failures)
        self.assertFalse(comparison["reproduced"])

    def test_repair_budget_survives_new_head(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="old", repair_rounds=2)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(head=SHA_B)), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 2)

    def test_repair_exhausted_is_terminal_without_repeated_comments(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key, repair_exhausted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        comment.assert_not_called()

    def test_dry_run_never_pushes_or_comments_repairs(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING")), \
                patch.object(pr_watch_repair, "update_branch_with_main") as update, \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        update.assert_not_called()
        comment.assert_not_called()

    def test_all_lanes_finish_before_reporting_failure(self):
        config = {"profile": "commands", "commands": [["false"], ["true"]]}
        with patch.object(pr_watch_verify, "run_check", side_effect=[
                {"name": "one", "command": ["false"], "ok": False, "summary": "bad", "stdout": "bad", "stderr": ""},
                {"name": "two", "command": ["true"], "ok": True, "summary": "", "stdout": "", "stderr": ""}]) as check:
            with self.assertRaises(pr_watch_common.VerificationError):
                pr_watch_verify.verify("example/one", pr(), ".", config)
        self.assertEqual(check.call_count, 2)

    def test_repair_scanner_preserves_all_trusted_config_adapters(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True, capture_output=True)
            config = root / "config.json"
            config.write_text(json.dumps({"hub": {"url": "https://synthetic-private-host.internal"}}))
            marker = "JA" + "RVIS_CONFIG"
            (root / "fixture.txt").write_text("synthetic-private-host.internal\n")
            subprocess.run(["git", "add", "fixture.txt"], cwd=root, check=True, capture_output=True)
            with patch.dict(os.environ, {marker: str(config), "GH_TOKEN": "not-for-scanner"}):
                with self.assertRaises(pr_watch_common.WatchError):
                    pr_watch_repair.privacy_scanner_environment(root)
                (root / "fixture.txt").unlink()
                subprocess.run(["git", "rm", "--cached", "-q", "fixture.txt"], cwd=root, check=True, capture_output=True)
                env = pr_watch_repair.privacy_scanner_environment(root)
            self.assertEqual(env[marker], str(config))
            self.assertNotIn("GH_TOKEN", env)
            self.assertNotEqual(env["HOME"], os.environ["HOME"])

    def test_repair_worker_has_no_github_credentials(self):
        calls = []
        def repair_checked(argv, **kwargs):
            calls.append(argv)
            if argv[:3] == ["git", "rev-parse", "HEAD"]:
                return SHA_A + "\n"
            if argv[:4] == ["git", "diff", "--cached", "--stat"]:
                return "" if len(calls) <= 2 else "repair.py | 1 +\n"
            return ""
        with patch.dict(os.environ, {"GH_TOKEN": "not-for-worker", "PR_WATCH_WEBHOOK_SECRET": "not-for-worker",
                                     "ANTHROPIC_API_KEY": "model-only"}), \
                patch.object(pr_watch_repair.shutil, "which", return_value="/usr/bin/bwrap"), \
                patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "ok", "")) as worker_run, \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch_repair, "repair_repository_copy", return_value=nullcontext(self.root)), \
                patch.object(pr_watch_repair, "checked", side_effect=repair_checked):
            pr_watch_repair.repair_with_model("example/one", pr(), self.root, "failure", {})
        env = worker_run.call_args.kwargs["environ"]
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("PR_WATCH_WEBHOOK_SECRET", env)
        self.assertIn("ANTHROPIC_API_KEY", env)
        argv = pr_watch_repair.repair_worker_argv(["claude", "--version"], self.root)
        home = argv[argv.index("HOME") + 1]
        self.assertNotEqual(Path(home), self.root)
        self.assertFalse(Path(home).is_relative_to(self.root))

    def test_dry_run_repair_failure_never_comments(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("failure")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("worker failed")), \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        comment.assert_not_called()
    def test_verification_does_not_import_pr_selector_or_receive_gh_credentials(self):
        with patch.object(pr_watch_verify, "checked", return_value="scripts/ci/paths.py\n"), \
                patch.object(pr_watch_verify, "load_paths") as selector, \
                patch.dict(os.environ, {"GH_TOKEN": "not-for-builds", "HOME": "/maintainer-home",
                                        "PR_WATCH_WEBHOOK_SECRET": "not-for-builds", "GRADLE_USER_HOME": "/trusted-gradle"}):
            selector.return_value.select.return_value = {"scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
            env = pr_watch_common.verification_env()
        self.assertEqual(selector.call_args.args[0], HERE.parent.parent)
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("PR_WATCH_WEBHOOK_SECRET", env)
        self.assertEqual(env["GRADLE_USER_HOME"], "/trusted-gradle")
        self.assertNotEqual(env["HOME"], "/maintainer-home")

    def test_persona_wrapper_owns_lock_and_expects_reviewed_head(self):
        repo = "nwverse/persona"
        config = dict(self.config)
        config["repositories"] = [{"name": repo, "checkout": str(self.root / "checkout"), "base": "main",
                                  "verify": {}, "merge": True, "privacy": None, "notify_command": None}]
        watcher = pr_watch.Watcher(config, verbose=False)
        with patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as run, \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(state="MERGED")):
            watcher.merge(config["repositories"][0], pr())
        argv = run.call_args.args[0]
        self.assertEqual(argv[2:6], ["--expect-head", SHA_A, "--expect-base", SHA_BASE])
        self.assertEqual(argv[6], "7")

    def test_new_sha_reviews_again(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(), pr(SHA_B), pr(SHA_B)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)

    def test_repair_infrastructure_failure_retries_without_consuming_round(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(title="feat: an example")), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("real test failed")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("worker unavailable")):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "retry")
        self.assertEqual(state.get("repair_rounds", 0), 0)
        self.assertEqual(len(self.comments), 1)
        self.assertIn("worker unavailable", self.comments[0])

    def test_terminal_verification_failure_is_published_once(self):
        clock = [1000.0]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.WatchError("infrastructure unavailable")) as verify, \
                patch.object(pr_watch.time, "time", side_effect=lambda: clock[0]):
            for now in (1000.0, 1301.0, 1602.0):
                clock[0] = now
                self.watcher.process("example/one", 7)
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "failed")
            self.assertEqual(len(self.comments), 3)

            for now in (1903.0, 1904.0):
                clock[0] = now
                self.watcher.process("example/one", 7)

        terminal = [body for body in self.comments if "did not pass" in body]
        self.assertEqual(1, len(terminal), "an already-published terminal failure was posted again")
        self.assertEqual(len(self.comments), 4)
        self.assertTrue(self.watcher.state.get("example/one", 7)["failure_posted"])
        self.assertEqual(verify.call_count, 3)
        self.assertEqual(review.call_count, 1)

    def test_semantic_failure_retries_with_backoff_without_comment_spam(self):
        for attempt in range(3):
            with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                    patch.object(pr_watch, "semantic_review", side_effect=pr_watch_common.WatchError("invalid output")), \
                    patch.object(self.watcher, "merge") as merge:
                self.watcher.process("example/one", 7)
            merge.assert_not_called()
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
            self.watcher.state.data["prs"]["example/one#7"]["retry_after"] = 0
        self.assertEqual(self.comments, [])

    def test_conflicting_pr_enters_bounded_base_repair(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", return_value=False) as update, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        update.assert_called_once()
        with patch.object(pr_watch, "gh_json", return_value=[{"number": 7, "headRefOid": SHA_A, "isDraft": False,
                                                               "baseRefName": "main", "mergeable": "CONFLICTING"}]), \
                patch.object(self.watcher, "enqueue") as enqueue:
            self.watcher.discover()
        enqueue.assert_called_once()

    def test_failure_summary_retains_actual_errors(self):
        result = subprocess.CompletedProcess([], 1, "PASS earlier lane\n" + "\n".join("ordinary" for _ in range(200)) +
                                               "\n--- FAIL: TestRealDefect\npanic: actual cause\n", "")
        summary = pr_watch_verify.result_summary(result)
        self.assertIn("--- FAIL: TestRealDefect", summary)
        self.assertIn("panic: actual cause", summary)

    def test_failure_summary_keeps_unknown_diagnostics_and_final_test_names(self):
        plain = subprocess.CompletedProcess([], 1, "slot order: generated row is absent from the baseline", "")
        self.assertIn("absent from the baseline", pr_watch_verify.result_summary(plain))
        passing = "\n".join("PASS: handles no error and client errors %d" % n for n in range(100))
        failed = passing + "\nFailed tests:\n--- FAIL: TestActualAssertion\n    got 1, want 2\n"
        summary = pr_watch_verify.result_summary(subprocess.CompletedProcess([], 1, failed, ""))
        self.assertIn("TestActualAssertion", summary)
        self.assertIn("got 1, want 2", summary)
        self.assertNotIn("handles no error and client errors 0", summary)
        python_failure = "Traceback (most recent call last):\n  File \"test_x.py\", line 2\n    self.assertEqual(1, 2)\nAssertionError: 1 != 2\nFAILED (failures=1)"
        summary = pr_watch_verify.result_summary(subprocess.CompletedProcess([], 1, python_failure, ""))
        self.assertIn("AssertionError: 1 != 2", summary)
        other = subprocess.CompletedProcess([], 1, "output before\nError: compiler rejected input\n1 tests failed.", "")
        summary = pr_watch_verify.result_summary(other)
        self.assertIn("compiler rejected input", summary)

    def test_unknown_mergeability_waits(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="UNKNOWN")), \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()

    def test_moved_head_discards_passed_checks(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(SHA_B)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        merge.assert_not_called()

    def test_another_persons_review_never_skips_verification(self):
        # The service reads only its own versioned state, not an arbitrary GitHub review.
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(reviewDecision="COMMENTED")), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()

    def test_inline_review_uses_stdin_payload_and_falls_back(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P1", "summary": "wrong", "suggestion": "fix"}]
        requests = []
        def fake_run(argv, **kwargs):
            requests.append((argv, kwargs))
            if argv[:2] == ["gh", "api"]:
                return subprocess.CompletedProcess(argv, 422, "", "bad anchor")
            return subprocess.CompletedProcess(argv, 0, "{}", "")
        with patch.object(pr_watch, "run", side_effect=fake_run):
            self.watcher.comment_findings("example/one", 7, pr(), findings)
        payload = json.loads(requests[0][1]["input_text"])
        self.assertEqual(payload["comments"][0]["line"], 2)
        self.assertEqual(payload["commit_id"], SHA_A)
        self.assertEqual(requests[1][0][:2], ["gh", "pr"])

    def test_lock_excludes_a_second_process(self):
        first = pr_watch.Watcher(self.config, verbose=False)
        second = pr_watch.Watcher(self.config, verbose=False)
        first.single_instance()
        self.addCleanup(first.instance.close)
        with self.assertRaises(pr_watch_common.WatchError):
            second.single_instance()
        second.instance.close()

    def test_superseded_verification_dequeues_before_fresh_review(self):
        self.watcher.state.put("example/one", 8, SHA_B,
                               evidence_key="example/one#8@" + SHA_BASE + ".." + SHA_B,
                               base=SHA_BASE, format=1, outcome="failed", semantic="passed")
        self.watcher.enqueue("example/one", 7, SHA_A, "poll")
        self.watcher.enqueue("example/one", 8, SHA_B, "poll")
        self.assertEqual(self.watcher.tasks.get()[2], ("example/one", 8))
        self.watcher.tasks.task_done()
        self.assertEqual(self.watcher.tasks.get()[2], ("example/one", 7))
        self.watcher.tasks.task_done()

    def test_enqueue_marks_active_and_queues_under_one_lock(self):
        calls = []
        class RecordingLock:
            def __init__(self):
                self.depth = 0
            def __enter__(self):
                self.depth += 1
                calls.append("enter")
            def __exit__(self, *args):
                calls.append("exit")
                self.depth -= 1
        lock = RecordingLock()
        self.watcher.queue_lock = lock
        self.watcher.queue_priority = lambda *args: calls.append("priority") or 0
        self.watcher.tasks.put = lambda item: calls.append(("put", item))
        self.watcher.enqueue("example/one", 7, SHA_A, "poll")
        self.assertEqual(calls, ["enter", "priority", ("put", (0, 0, ("example/one", 7))), "exit"])
        self.assertEqual(lock.depth, 0)

    def test_priority_queue_accepts_shutdown_sentinel_after_queued_work(self):
        self.watcher.enqueue("example/one", 7, SHA_A, "poll")
        self.watcher.tasks.put((2, -1, ("", 0)))
        self.watcher.tasks.put((2, -1, ("", 0)))
        workers = [threading.Thread(target=self.watcher.worker) for _ in range(2)]
        for worker in workers:
            worker.start()
        for worker in workers:
            worker.join(timeout=5)
            self.assertFalse(worker.is_alive())
        self.assertTrue(self.watcher.tasks.empty())

    def test_findings_and_exhausted_repairs_do_not_get_migration_priority(self):
        for outcome, fields in (("findings", {"semantic": "findings"}),
                                ("repair_exhausted", {"repair_exhausted": True})):
            self.watcher.state.put("example/one", 8, SHA_B, outcome=outcome, format=1, **fields)
            self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_B), 1)

    def test_queue_priority_requires_exact_migratable_head(self):
        evidence_key = "example/one#8@" + SHA_BASE + ".." + SHA_B
        self.watcher.state.put("example/one", 8, SHA_B, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="failed")
        self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_B), 0)
        self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_A), 1)
        for fields in ({"retry_after": 10**12}, {"outcome": "merge_failed"},
                       {"outcome": "merge_failed", "blocking": False}):
            with self.subTest(fields=fields):
                self.watcher.state.data["prs"]["example/one#8"].update(fields)
                self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_B), 1)
                self.watcher.state.data["prs"]["example/one#8"].update(
                    {"retry_after": 0, "outcome": "failed", "blocking": False})

    def test_config_has_no_implicit_merge_or_repositories(self):
        path = self.root / "config.json"
        path.write_text(json.dumps({"repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {}}]}))
        config = pr_watch.load_config(path)
        self.assertFalse(config["repositories"][0]["merge"])
        self.assertEqual(len(config["repositories"]), 1)


class ReviewTests(unittest.TestCase):
    def test_schema_and_changed_line_validation(self):
        envelope = {"subtype": "success", "is_error": False, "permission_denials": [],
                    "structured_output": {"findings": [{"path": "a.py", "line": 2, "severity": "P1",
                                                        "summary": "wrong", "suggestion": "fix"}]}}
        self.assertEqual(len(pr_watch_review.parse_result(json.dumps(envelope), {"a.py": {2}})), 1)
        for anchors in ({"a.py": {1}}, {"other.py": {2}}):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.parse_result(json.dumps(envelope), anchors)
        envelope["permission_denials"] = [{"tool": "Bash"}]
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_review.parse_result(json.dumps(envelope), {"a.py": {2}})
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_review.parse_result("not json", {"a.py": {2}})

    def test_diff_anchors_include_only_added_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                subprocess.run(["git"] + list(args), cwd=root, env=env, check=True, capture_output=True)
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("one\ntwo\n")
            git("add", "a.py")
            git("commit", "-qm", "base")
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "a.py").write_text("one\nchanged\nthree\n")
            git("commit", "-qam", "head")
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            self.assertEqual(pr_watch_review.changed_lines(root, base, head), {"a.py": {2, 3}})


class EventTests(unittest.TestCase):
    def test_webhook_authentication_repository_and_callback(self):
        secret = "x" * 32
        body = json.dumps({"action": "synchronize", "repository": {"full_name": "example/one"},
                           "pull_request": {"number": 7, "draft": False, "head": {"sha": SHA_A}}}).encode()
        signature = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
        event = pr_watch_events.parse_pull_request(body, signature, "pull_request", secret, {"example/one"})
        self.assertEqual(event, ("example/one", 7, SHA_A))
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_events.parse_pull_request(body, "sha256=bad", "pull_request", secret, {"example/one"})
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_events.parse_pull_request(body, signature, "pull_request", secret, {"example/other"})


class VerifyTests(unittest.TestCase):
    def test_apple_change_does_not_require_macos_verifier(self):
        path = "apple/Persona/App/View.swift"
        with patch.object(pr_watch_verify, "checked", side_effect=[path + "\n", "+import SwiftUI"]), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat: an example"), 1,
                                                       {"ui_evidence_command": ["visual", "{worktree}"]})
        self.assertIn(("Apple semantic and portable verification policy", ["true"]), commands)
        self.assertIn(("visual evidence", ["visual", "."]), commands)
        self.assertFalse(any(command[0] == "trusted Apple verification" for command in commands))

    def test_non_ui_apple_paths_are_explicit_and_diff_is_checked(self):
        path = "apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift"
        with patch.object(pr_watch_verify, "checked", return_value="+chunks.removeFirst()\n"):
            self.assertTrue(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, [path]))
        for changed in ("+import SwiftUI\n+struct ChunkView: View {}", "+Text(localized: \"Ready\")",
                        "+NavigationStack { Content() }", "+.sheet(isPresented: $shown) { Content() }",
                        "+.padding(8)", "+Image(systemName: \"checkmark\")"):
            with patch.object(pr_watch_verify, "checked", return_value=changed):
                self.assertFalse(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, [path]))
        for changed in ("+import SwiftUI", "+NavigationStack { Content() }"):
            with patch.object(pr_watch_verify, "checked", return_value=changed):
                self.assertFalse(pr_watch_verify.non_ui_apple_change(
                    ".", SHA_BASE, SHA_A, ["apple/Persona/Views/Session/FilesPane.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+.padding(8)"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/PersonaUITests/MinutesTests.swift", "apple/Persona/Views/Session/FilesPane.swift"]))
        for names in ([path, "web/src/views/ChatView.tsx"],
                      ["apple/Shared/Localization/Localizable.xcstrings"]):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, names))
        with patch.object(pr_watch_verify, "checked", return_value="+import SwiftUI"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/App/TestsSupport/Feature.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+Text(ready)"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/Recording/RecordingMinutesView.swift"]))

        self.assertTrue(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["apple/PersonaUITests/MinutesTests.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+chunks.removeFirst()\n"):
            self.assertTrue(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/App/TasksStore.swift"]))
        self.assertTrue(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-runtime/src/lib.rs",
                                    "android/core/rustcore/src/main/kotlin/persona/core/rustcore/Codec.kt",
                                    "schema/extension/manifest.json"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-ffi/src/lib.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-typegen/src/main.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-runtime/src/wire.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["web/src/views/ChatView.tsx"]))

    def test_eligible_non_ui_apple_change_runs_remaining_lanes(self):
        names = "apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "non_ui_apple_change", return_value=True) as classifier, \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True, "server": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="chore: an example"), 1, {})
        self.assertEqual(commands, [("CI server", ["bash", "scripts/ci/server.sh"]),
                                    ("Exact-head privacy scan", commands[1][1]),
                                    ("Apple semantic and portable verification policy", ["true"])])
        classifier.assert_called_once()

    def test_shared_core_lane_is_not_dropped(self):
        names = "native/app-core/crates/persona-core-runtime/src/lib.rs\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "non_ui_apple_change", return_value=True), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "core": True, "android": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
        self.assertEqual(commands[:2], [("CI android", ["bash", "scripts/ci/android.sh"]),
                                    ("CI core", ["bash", "scripts/ci/core.sh"])])
        self.assertEqual(commands[-1], ("Apple semantic and portable verification policy", ["true"]))

    def test_apple_ffi_keeps_semantic_and_portable_checks_without_macos(self):
        names = "native/app-core/crates/persona-core-ffi/src/lib.rs\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "core": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat: an example"), 1, {})
        self.assertIn(("CI core", ["bash", "scripts/ci/core.sh"]), commands)
        self.assertIn(("Apple semantic and portable verification policy", ["true"]), commands)

    def test_extension_shared_changes_use_full_ci(self):
        with patch.object(pr_watch_verify, "extension_modules", return_value=None), \
                patch.object(pr_watch_verify, "checked", return_value="scripts/ci.sh\n"):
            self.assertEqual(pr_watch_verify.extension_checks(".", pr(), 1),
                             [("extension repository CI", ["bash", "scripts/ci.sh"])])


class MergeTests(unittest.TestCase):
    def test_generic_merge_retains_exact_refs_for_the_privacy_scanner(self):
        commands = []
        def fake_checked(argv, **kwargs):
            commands.append(argv)
            if argv[:2] == ["git", "rev-parse"]:
                return (SHA_BASE if "bases" in argv[2] else SHA_A) + "\n"
            return ""
        with patch.object(pr_watch_merge, "pr_snapshot", side_effect=[pr(), pr(state="MERGED")]), \
                patch.object(pr_watch_merge, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_merge, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            pr_watch_merge.merge("example/one", 7, ".", privacy="scanner.py",
                                 expected_head=SHA_A, expected_base=SHA_BASE)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/heads/7"], commands)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/heads/7"], commands)
        self.assertIn(["git", "fetch", "-q", "origin", "+refs/heads/main:refs/pr-watch/bases/7"], commands)
        scan = execute.call_args_list[0].args[0]
        self.assertIn(SHA_BASE + ".." + SHA_A, scan)

    def test_issue_link_parser_ignores_code_and_other_repositories(self):
        self.assertEqual(pr_watch_merge.linked_issue("```\nFixes #1\n```\nRefs #2", "example/one"), [2])
        self.assertEqual(pr_watch_merge.linked_issue("Fixes example/two#3", "example/one"), [])
        self.assertEqual(pr_watch_merge.linked_issue("See https://github.com/example/one/issues/4", "example/one"), [4])


if __name__ == "__main__":
    unittest.main()
