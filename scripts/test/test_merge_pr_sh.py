"""scripts/hooks/merge-pr.sh merges exactly the head, title and body it scanned, read from one snapshot of the PR.

Each test builds a throwaway clone (isolated HOME and Git config) whose origin is a local bare repository with
refs/heads/main and refs/pull/7/head, and puts a fake gh first on PATH. The fake serves `pr view` from a state file,
answers `repo view` and `api`, and logs every call. Like GitHub with squash messages set to PR_TITLE / PR_BODY, it
records `<title> (#7)` and the body as they are at merge time (`at_merge`: an edit saved after the last view) unless
the merge passes --subject / --body-file. The real gh never runs. The clone carries this checkout's copies of the scripts
merge-pr.sh runs: privacy_check.py and scripts/ci/changed_ratchets.py with the two ratchets it loads (#7977) and the
ratchet_base.py they import. Its main has no ratchet baselines unless a test commits them, so only that test's PRs are
checked against them.
"""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent.parent
WORD = "quill" + "wren"  # a made-up private word, split so the repository scan of this file does not see it
# The variables that name a build config (privacy_check.CONFIG_ENV): left set, they would point the scan at this
# machine's real config.
_spec = importlib.util.spec_from_file_location("privacy_check", SCRIPTS / "privacy_check.py")
_privacy_check = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_privacy_check)
CONFIG_ENV = set(_privacy_check.CONFIG_ENV)
# The automated merge (docs/operations/pr-review-automation.md) takes the same lock.
sys.path.insert(0, str(SCRIPTS / "operations"))
import pr_watch_merge  # noqa: E402
sys.path.insert(0, str(SCRIPTS / "test"))
from fake_gh import FAKE_GH, rows  # noqa: E402

RATCHET_SCRIPTS = ("changed_ratchets.py", "sleep_ratchet.py", "web_font_ratchet.py", "wire_types_ratchet.py", "merge_wire_types.py", "ratchet_base.py")  # scripts/ci
GO_TEST = "server/internal/demo/demo_test.go"

class MergePrTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="merge-pr-test-")
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        self.repo, self.home, self.origin, bin_dir = root / "repo", root / "home", root / "origin.git", root / "bin"
        for d in (self.repo, self.home, bin_dir):
            d.mkdir()
        (bin_dir / "gh").write_text("#!" + sys.executable + "\n" + FAKE_GH)
        (bin_dir / "gh").chmod(0o755)
        self.state, self.log = root / "state.json", root / "gh.log"
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("GIT_", "GH_", "PYTHON")) and k not in CONFIG_ENV}
        self.env.update({"HOME": str(self.home), "XDG_CONFIG_HOME": str(self.home / ".config"),
                         "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
                         "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                         "GIT_AUTHOR_NAME": "Test User", "GIT_AUTHOR_EMAIL": "test@example.com",
                         "GIT_COMMITTER_NAME": "Test User", "GIT_COMMITTER_EMAIL": "test@example.com",
                         "FAKE_GH_STATE": str(self.state), "FAKE_GH_LOG": str(self.log)})
        words = self.home / ".config/persona/private-words.txt"
        words.parent.mkdir(parents=True)
        words.write_text(WORD + "\n")
        self.git("init", "-q", "--bare", str(self.origin))
        self.git("init", "-q", "-b", "main")
        self.git("config", "commit.gpgSign", "false")
        self.git("remote", "add", "origin", str(self.origin))
        (self.repo / "scripts").mkdir()
        shutil.copy2(SCRIPTS / "privacy_check.py", self.repo / "scripts/privacy_check.py")
        shutil.copytree(SCRIPTS / "hooks", self.repo / "scripts/hooks")
        (self.repo / "scripts/ci").mkdir()
        for name in RATCHET_SCRIPTS:
            shutil.copy2(SCRIPTS / "ci" / name, self.repo / "scripts/ci" / name)
        for rel in ("scripts/test/core_paths.py", "scripts/test/module_classification.py", "scripts/ci/android-shards.py",
                    "schema/core-paths.txt", "schema/module-classification.txt", "android/settings.gradle.kts"):
            (self.repo / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(SCRIPTS.parent / rel, self.repo / rel)  # what core-change-check.py reads
        (self.repo / "config.example.json").write_text("{}\n")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "Initial")
        self.git("push", "-q", "origin", "main")

    def git(self, *args):
        result = subprocess.run(("git",) + args, cwd=self.repo, env=self.env, capture_output=True, text=True,
                                timeout=30)
        if result.returncode:
            self.fail(result.stdout + result.stderr)
        return result.stdout.strip()

    def open_pr(self, message="Add notes", content="Public notes.\n", files=None):
        """A commit on top of main (notes.md, or files: {path: text}) pushed as refs/pull/7/head; returns its sha."""
        self.git("checkout", "-q", "-B", "feature", "main")
        files = files or {"notes.md": content}
        for path, text in files.items():
            (self.repo / path).parent.mkdir(parents=True, exist_ok=True)
            (self.repo / path).write_text(text)
        self.git("add", "--", *files)
        self.git("commit", "-q", "-m", message)
        self.git("push", "-q", "origin", "+HEAD:refs/pull/7/head")
        return self.git("rev-parse", "HEAD")

    def view(self, head, **fields):
        return dict({"headRefOid": head, "headRefName": "fix/7-notes", "isCrossRepository": False,
                     "baseRefName": "main", "title": "fix: add notes", "body": "Adds notes.\n\nFixes #6"}, **fields)

    def merge_pr(self, pr, at_merge=None, arg="7", extra_args=()):
        self.state.write_text(json.dumps({"pr": pr, "at_merge": at_merge}))
        result = subprocess.run(["bash", str(self.repo / "scripts/hooks/merge-pr.sh")] + list(extra_args) + [arg],
                                cwd=self.repo, env=self.env, capture_output=True, text=True, timeout=60)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        return result, calls

    def merges(self, calls):
        return [c for c in calls if c["argv"][:2] == ["pr", "merge"]]

    def merge_with_files(self, files, labels=(), body="Adds notes.\n\nFixes #6", api_files=None):
        """Merge a PR whose commit adds `files` (paths). The fake files API answers `api_files` (default: nothing at all):
        the check reads the verified head's diff, so what the API says must not matter."""
        head = self.open_pr(files={f: "x\n" for f in files})
        pr = self.view(head, body=body, labels=[{"name": n} for n in labels])
        self.state.write_text(json.dumps({"pr": pr, "files": rows(*(api_files or []))}))
        result = subprocess.run(["bash", str(self.repo / "scripts/hooks/merge-pr.sh"), "7"], cwd=self.repo, env=self.env,
                                capture_output=True, text=True, timeout=60)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        return result, calls

    def test_a_core_change_merges_without_the_label_advisory(self):
        """core-change-check.py (#17195): a PR that changes a core path merges without the label; the check is advisory."""
        core = ["server/internal/hub/hub.go"]
        why = "Fixes #6\n\n## Why this must change the core\n\nThe hub must route the new frame.\n"
        for name, labels, body in (("no label, no section", (), "Fixes #6"), ("no label", (), why),
                                   ("no section", ("core-change",), "Fixes #6"),
                                   ("with label and reason", ("core-change",), why)):
            with self.subTest(name):
                self.log.unlink(missing_ok=True)
                result, calls = self.merge_with_files(core, labels, body)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(len(self.merges(calls)), 1)
        self.log.unlink(missing_ok=True)
        result, calls = self.merge_with_files(core, ("core-change",), why)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)
        self.assertEqual(len([c for c in calls if c["argv"][:2] == ["pr", "view"]]), 1)  # still one snapshot

    def test_module_docs_and_tests_only_changes_merge_untouched(self):
        for name, files in (("module", ["server/internal/modules/voice/voice.go", "web/src/modules/lark/a.ts"]),
                            ("docs", ["docs/architecture/core-and-modules.md"]),
                            ("tests", ["server/internal/hub/hub_test.go", "web/src/lib/model-config.test.ts"]),
                            ("panel only", ["web/src/views/chat/ChatView.tsx", "web/src/lib/model-panel.ts",
                                            "android/feature/agents/src/main/kotlin/A.kt"])):
            with self.subTest(name):
                self.log.unlink(missing_ok=True)
                result, calls = self.merge_with_files(files)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(len(self.merges(calls)), 1)

    def test_a_headless_web_file_is_core(self):
        # #17195: a headless (core) file merges without a label now; the check is advisory.
        result, calls = self.merge_with_files(["web/src/lib/model-config.ts"])
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("web/src/lib/model-config.ts", result.stdout)
        self.assertEqual(len(self.merges(calls)), 1)

    def test_the_check_reads_the_verified_head_not_the_files_api(self):
        """The files come from the diff of the head merge-pr.sh verified: an API that says nothing (or something else,
        as it would if the PR moved after the check) changes no verdict."""
        for name, api in (("api empty", []), ("api names a panel file", ["web/src/views/chat/ChatView.tsx"])):
            with self.subTest(name):
                self.log.unlink(missing_ok=True)
                result, calls = self.merge_with_files(["server/internal/hub/hub.go"], api_files=api)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)  # merges now (#17195)
                self.assertIn("server/internal/hub/hub.go", result.stdout)
                self.assertEqual(len(self.merges(calls)), 1)
                # no files list read from the API (the delete-branch call after merging is fine)
                self.assertEqual([c for c in calls if c["argv"][:1] == ["api"] and "/files" in c["argv"][1]], [])

    def test_a_rename_out_of_a_core_path_counts(self):
        self.git("checkout", "-q", "main")
        (self.repo / "web/src/lib").mkdir(parents=True)
        (self.repo / "web/src/lib/model-config.ts").write_text("export const a = 1;\n" * 20)
        self.git("add", ".")
        self.git("commit", "-q", "-m", "A headless file")
        self.git("push", "-q", "origin", "main")
        self.git("checkout", "-q", "-B", "feature", "main")
        (self.repo / "web/src/modules/lark").mkdir(parents=True)
        self.git("mv", "web/src/lib/model-config.ts", "web/src/modules/lark/model-config.ts")
        self.git("commit", "-q", "-m", "Move it into a module")
        self.git("push", "-q", "origin", "+HEAD:refs/pull/7/head")
        pr = self.view(self.git("rev-parse", "HEAD"))
        result, calls = self.merge_pr(pr)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)  # merges now (#17195)
        self.assertIn("web/src/lib/model-config.ts", result.stdout)  # the name it moved away from
        self.assertEqual(len(self.merges(calls)), 1)

    def test_merges_the_scanned_head_title_and_body_once(self):
        head = self.open_pr()
        result, calls = self.merge_pr(self.view(head))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        [merge] = self.merges(calls)
        argv = merge["argv"]
        self.assertIn("--squash", argv)
        self.assertEqual(argv[argv.index("--match-head-commit") + 1], head)
        self.assertEqual(argv[argv.index("--repo") + 1], "example/persona")
        self.assertEqual(merge["subject"], "fix: add notes (#7)")
        self.assertEqual(merge["body"], "Adds notes.\n\nFixes #6")
        # Git Bash's / is its install folder, as Windows Python sees it.
        root = "/" if os.name != "nt" else os.path.normpath(
            subprocess.run(["bash", "-c", "cd / && pwd -W"], capture_output=True, text=True).stdout.strip())
        self.assertEqual(merge["cwd"], root)
        self.assertEqual(len([c for c in calls if c["argv"][:2] == ["pr", "view"]]), 1)  # one snapshot
        self.assertIn(["api", "-X", "DELETE", "repos/example/persona/git/refs/heads/fix/7-notes"],
                      [c["argv"] for c in calls])

    def test_an_edit_after_the_scan_does_not_reach_the_merge(self):
        head = self.open_pr()
        edited = self.view(head, title="fix: " + WORD, body="Now names " + WORD + ".")
        result, calls = self.merge_pr(self.view(head), at_merge=edited)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        [merge] = self.merges(calls)
        self.assertEqual((merge["subject"], merge["body"]), ("fix: add notes (#7)", "Adds notes.\n\nFixes #6"))

    def test_a_private_word_blocks_the_merge(self):
        cases = {"title": ({}, {"title": "fix: " + WORD}), "body": ({}, {"body": "Tested on " + WORD + "."}),
                 "commit message": ({"message": "Add " + WORD + " notes"}, {}),
                 "file": ({"content": "Runs on " + WORD + ".\n"}, {})}
        for name, (commit, fields) in cases.items():
            with self.subTest(name):
                self.log.unlink(missing_ok=True)
                result, calls = self.merge_pr(self.view(self.open_pr(**commit), **fields))
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("not merged", result.stderr)
                self.assertEqual(self.merges(calls), [])

    def test_a_ratchet_failure_the_merge_would_add_blocks_it(self):
        """A fixed sleep the PR adds to a Go test stops the merge (changed_ratchets.py); marked sleep-ok, it merges."""
        self.git("checkout", "-q", "main")
        for name in ("sleep_baseline.json", "web_font_baseline.json"):
            (self.repo / "scripts/ci" / name).write_text("{}\n")
        self.git("add", "scripts/ci")
        self.git("commit", "-q", "-m", "Baselines")
        self.git("push", "-q", "origin", "main")
        wait = "package demo\n\nfunc TestWait(t *testing.T) {\n\ttime.Sleep(time.Second)%s\n}\n"
        result, calls = self.merge_pr(self.view(self.open_pr(files={GO_TEST: wait % ""})))
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn(GO_TEST + ": 1 time.Sleep call(s), baseline 0", result.stderr)
        self.assertIn("not merged: fix the ratchet failures", result.stderr)
        self.assertEqual(self.merges(calls), [])
        self.log.unlink()
        result, calls = self.merge_pr(self.view(self.open_pr(files={GO_TEST: wait % " // sleep-ok: the child exits"})))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)

    def test_a_pr_that_only_lowers_a_count_merges(self):
        """A count under its baseline is not a failure (docs/operations/testing.md, "Count ratchets"), so a PR that takes
        a sleep out and leaves the baseline merges; lowering the baseline is tidying for later."""
        wait = "package demo\n\nfunc TestWait(t *testing.T) {\n%s}\n"
        self.git("checkout", "-q", "main")
        (self.repo / GO_TEST).parent.mkdir(parents=True, exist_ok=True)
        (self.repo / GO_TEST).write_text(wait % "\ttime.Sleep(time.Second)\n")
        (self.repo / "scripts/ci/sleep_baseline.json").write_text(json.dumps({GO_TEST: 1}) + "\n")
        (self.repo / "scripts/ci/web_font_baseline.json").write_text("{}\n")
        self.git("add", "scripts/ci", GO_TEST)
        self.git("commit", "-q", "-m", "Baselines")
        self.git("push", "-q", "origin", "main")
        result, calls = self.merge_pr(self.view(self.open_pr(files={GO_TEST: wait % ""})))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)

    def test_a_moved_head_is_refused(self):
        self.open_pr()
        result, calls = self.merge_pr(self.view(self.git("rev-parse", "main")))
        self.assertEqual(result.returncode, 1)
        self.assertIn("moved", result.stderr)
        self.assertEqual(self.merges(calls), [])

    def test_a_cross_repository_branch_is_not_deleted(self):
        result, calls = self.merge_pr(self.view(self.open_pr(), isCrossRepository=True))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)
        self.assertEqual([c for c in calls if c["argv"][0] == "api" and "-X" in c["argv"]], [])  # no DELETE (the files read is a GET)

    def test_a_clone_without_remote_tracking_refs_scans_the_fetched_base(self):
        self.git("config", "--unset-all", "remote.origin.fetch")
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        result, calls = self.merge_pr(self.view(self.open_pr()))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)

    def test_a_competing_fetch_between_the_two_fetches_cannot_change_the_merged_base(self):
        """The watcher preparing a worktree in the same clone fetches the PR head into the shared FETCH_HEAD (#16759).
        That fetch is made to land between merge-pr.sh's own base and head fetches; the base it verified must still
        merge, because each fetch lands in its own ref instead of FETCH_HEAD."""
        head = self.open_pr()
        base = self.git("rev-parse", "main")
        real_git = shutil.which("git")
        wrapper = Path(self.env["PATH"].split(os.pathsep)[0]) / "git"
        wrapper.write_text("#!/bin/sh\n\"%s\" \"$@\"\nstatus=$?\n"
                           "case \" $* \" in\n"
                           "  (*\" fetch \"*\" refs/heads/main\"*) \"%s\" -C \"%s\" fetch -q origin pull/7/head ;;\n"
                           "esac\nexit $status\n" % (real_git, real_git, self.repo))
        wrapper.chmod(0o755)
        try:
            result, calls = self.merge_pr(self.view(head), extra_args=["--expect-head", head, "--expect-base", base])
        finally:
            wrapper.unlink()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)

    def test_expected_head_and_base_are_required(self):
        head = self.open_pr()
        base = self.git("rev-parse", "main")
        expected = ["--expect-head", head, "--expect-base", base]
        result, calls = self.merge_pr(self.view(head), extra_args=expected)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)
        for args in (["--expect-head", "f" * 40, "--expect-base", base],
                     ["--expect-head", head, "--expect-base", "f" * 40]):
            self.log.unlink(missing_ok=True)
            result, calls = self.merge_pr(self.view(head), extra_args=args)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(self.merges(calls), [])

    def test_a_held_lock_makes_the_merge_wait(self):
        """Another merge-pr.sh (flock, or msvcrt.locking on Windows) or pr_watch_merge holding the lock blocks this one."""
        def raw_lock():
            fd = os.open(self.repo / ".git/persona-pr-merge.lock", os.O_RDWR | os.O_CREAT, 0o600)
            if os.name == "nt":
                import msvcrt
                msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                fcntl.flock(fd, fcntl.LOCK_EX)
            return lambda: os.close(fd)

        def watcher_lock():
            return pr_watch_merge.merge_lock(str(self.repo)).close

        for name, hold in (("merge-pr.sh", raw_lock), ("pr_watch_merge", watcher_lock)):
            with self.subTest(name):
                self.log.unlink(missing_ok=True)
                self.state.write_text(json.dumps({"pr": self.view(self.open_pr()), "at_merge": None}))
                release = hold()
                try:
                    proc = subprocess.Popen(["bash", str(self.repo / "scripts/hooks/merge-pr.sh"), "7"],
                                            cwd=self.repo, env=self.env, stdout=subprocess.PIPE,
                                            stderr=subprocess.PIPE, text=True)
                    self.addCleanup(lambda p=proc: p.poll() is None and p.kill())
                    with self.assertRaises(subprocess.TimeoutExpired):
                        proc.wait(timeout=3)
                    self.assertFalse(self.log.exists())  # gh has not run
                finally:
                    release()
                out, err = proc.communicate(timeout=60)
                self.assertEqual(proc.returncode, 0, out + err)
                calls = [json.loads(line) for line in self.log.read_text().splitlines()]
                self.assertEqual(len(self.merges(calls)), 1)

    def test_a_non_number_is_refused_before_gh_runs(self):
        result, calls = self.merge_pr(self.view("0" * 40), arg="7a")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
