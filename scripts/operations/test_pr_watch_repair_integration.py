"""Integration tests for authorized repair Git operations; no live GitHub."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import pr_watch_repair


def real_dir(directory):
    """directory with its symlinks resolved, as the repair copy the watcher hands the worker is (repair_repository_copy):
    the sandbox mounts the checkout where it really is, so a path under a temp dir reached through a link is not there
    (#9204)."""
    return Path(directory).resolve()


class RepairIntegrationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="pr-watch-repair-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.origin = self.root / "origin.git"
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                        GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                        GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
        self.git("init", "-q", "--bare", str(self.origin), cwd=self.root)
        self.git("init", "-q", "-b", "main")
        # The helper invokes Git directly, so exercise its configured identity
        # without inheriting the maintainer's global configuration.
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.com")
        self.git("remote", "add", "origin", str(self.origin))
        (self.repo / "file.txt").write_text("base\n")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        self.git("push", "-q", "origin", "main")
        self.base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-qb", "fix/example")
        (self.repo / "file.txt").write_text("pr\n")
        self.git("commit", "-qam", "pr")
        self.git("push", "-q", "origin", "fix/example")
        self.head = self.git("rev-parse", "HEAD")
        self.git("checkout", "-q", "main")
        (self.repo / "main.txt").write_text("new main\n")
        self.git("add", "main.txt")
        self.git("commit", "-qm", "main")
        self.git("push", "-q", "origin", "main")
        self.main = self.git("rev-parse", "HEAD")
        self.git("checkout", "-q", "fix/example")
        self.pr = {"number": 7, "title": "fix: example", "body": "Fixes #6", "baseRefName": "main",
                   "headRefName": "fix/example", "headRefOid": self.head, "baseRefOid": self.main,
                   "isCrossRepository": False}

    def git(self, *args, cwd=None):
        result = subprocess.run(["git"] + list(args), cwd=cwd or self.repo, env=self.env,
                                capture_output=True, text=True)
        if result.returncode:
            self.fail(result.stdout + result.stderr)
        return result.stdout.strip()

    def test_same_repository_base_update_pushes_ordinary_merge(self):
        updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main)
        pr_watch_repair.push_updated_head("example/one", self.pr, str(self.repo), updated)
        remote = self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0]
        self.assertEqual(remote, self.git("rev-parse", "HEAD"))
        parents = self.git("show", "-s", "--format=%P", "HEAD").split()
        self.assertEqual(parents, [self.head, self.main])
        self.assertFalse(pr_watch_repair.update_branch_with_main("example/one", dict(self.pr, headRefOid=remote), str(self.repo), self.main))

    def test_fork_never_pushes_original_branch(self):
        pr = dict(self.pr, isCrossRepository=True)
        self.assertTrue(pr_watch_repair.update_branch_with_main("example/one", pr, str(self.repo), self.main))
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    def test_worker_clone_cleanup_never_removes_source_checkout(self):
        with pr_watch_repair.repair_repository_copy(str(self.repo), [self.main]) as copy:
            self.assertTrue(Path(copy, ".git").is_dir())
            self.assertFalse(Path(copy, ".git").is_file())
            self.assertNotEqual(Path(copy), self.repo)
            self.assertEqual(self.git("cat-file", "-t", self.main, cwd=copy), "commit")
        self.assertTrue(self.repo.exists())
        self.assertTrue(Path(self.repo, ".git").exists())
    def test_head_change_before_repair_push_is_refused(self):
        (self.repo / "repair.txt").write_text("repair\n")
        with patch.object(pr_watch_repair, "verify", return_value=[]), \
                patch.object(pr_watch_repair, "checked", side_effect=lambda argv, **kwargs: self.git(*argv[1:], cwd=kwargs.get("cwd")) if argv[0] == "git" else ""), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=dict(self.pr, headRefOid="d" * 40)):
            with self.assertRaises(Exception):
                pr_watch_repair.commit_and_push_repair("example/one", self.pr, str(self.repo), self.base, "failure", {"profile": "commands", "commands": [["true"]]})
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    # #7840: main moves every few minutes and a verification takes longer; a repair follows it when it only moved on.
    def push_main(self, name, parent=None):
        """A new commit on origin's main, on top of `parent` (a rewrite) or of its current tip; the checkout stays put."""
        branch = self.git("rev-parse", "--abbrev-ref", "HEAD")
        self.git("fetch", "-q", "origin", "main")
        self.git("checkout", "-q", "--detach", parent or "FETCH_HEAD")
        (self.repo / (name + ".txt")).write_text(name + "\n")
        self.git("add", name + ".txt")
        self.git("commit", "-qm", name)
        self.git("push", "-q", "--force", "origin", "HEAD:refs/heads/main")
        tip = self.git("rev-parse", "HEAD")
        self.git("checkout", "-q", branch)
        return tip

    def passthrough(self):
        def passthrough(argv, **kwargs):
            if argv[0] != "git":
                return ""
            if argv[1] == "cat-file":
                return "".join(line.strip() + " commit\n" for line in kwargs["input_text"].splitlines())
            return self.git(*argv[1:], cwd=kwargs.get("cwd"))
        return patch.object(pr_watch_repair, "checked", side_effect=passthrough)

    def test_merge_follows_main_that_moved_after_the_pr_was_read(self):
        newer = self.push_main("newer")
        updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main)
        self.assertEqual(self.git("show", "-s", "--format=%P", updated).split(), [self.head, newer])
        self.assertEqual(pr_watch_repair.merged_main(str(self.repo), updated), newer)

    def test_a_pr_up_to_date_with_its_verified_base_is_not_merged_when_only_main_moved(self):
        self.git("merge", "-q", "--no-edit", self.main)
        head = self.git("rev-parse", "HEAD")
        self.push_main("newer")
        self.assertIsNone(pr_watch_repair.update_branch_with_main("example/one", dict(self.pr, headRefOid=head),
                                                                  str(self.repo), self.main))
        self.assertEqual(self.git("rev-parse", "HEAD"), head)

    def test_a_rewritten_main_stops_the_merge(self):
        self.push_main("rewritten", parent=self.base)
        with self.assertRaisesRegex(pr_watch_repair.WatchError, "main was rewritten"):
            pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main)
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)

    def fork_merge(self):
        pr = dict(self.pr, isCrossRepository=True)
        updated = pr_watch_repair.update_branch_with_main("example/one", pr, str(self.repo), self.main)
        base = pr_watch_repair.merged_main(str(self.repo), updated)
        return pr, dict(pr, headRefOid=updated, baseRefOid=base), base

    def test_fork_continuation_follows_main_moving_during_verification(self):
        pr, merged, base = self.fork_merge()
        later = self.push_main("later")
        snapshots = {7: dict(pr, baseRefOid=later), 8: dict(merged, number=8)}
        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={"repair_command": "repair-worker"}), \
                patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: snapshots[number]), \
                patch.object(pr_watch_repair, "gh_json", side_effect=lambda argv, **kwargs: [] if argv[:2] == ["pr", "list"] else {"number": 8}):
            number, _ = pr_watch_repair.commit_and_push_repair("example/one", merged, str(self.repo), base, "failure",
                                                               {"profile": "commands", "commands": [["true"]]},
                                                               allow_merge_only=True, original_pr=pr)
        self.assertEqual(number, 8)
        branch = "refs/heads/pr-watch/repair-7-" + merged["headRefOid"][:12]
        self.assertEqual(self.git("ls-remote", "origin", branch).split()[0], merged["headRefOid"])

    def test_fork_continuation_checks_github_before_publishing(self):
        pr, merged, base = self.fork_merge()
        existing = {"number": 8, "title": "fix: continue #7 verification repair", "body": "",
                    "headRefOid": pr["headRefOid"]}
        created = []
        snapshots = {7: pr, 9: dict(merged, number=9)}

        def github(argv, **kwargs):
            if "--search" in argv:
                return [existing]
            if "--head" in argv:
                return []
            created.append(json.loads(kwargs["input_text"]))
            return {"number": 9}

        original_checked = pr_watch_repair.checked

        def checked(argv, **kwargs):
            if argv[:2] == ["gh", "api"]:
                self.assertEqual(argv[2], "repos/example/one/commits/" + pr["headRefOid"])
                return "2026-11-16T00:00:00Z\n"
            return original_checked(argv, **kwargs)

        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}), \
                patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: snapshots[number]), \
                patch.object(pr_watch_repair, "gh_json", side_effect=github), \
                patch.object(pr_watch_repair, "checked", side_effect=checked):
            for state, merged_at, expected in [("OPEN", None, (8, None)),
                                               ("MERGED", "2026-11-17T00:00:00Z", (8, None)),
                                               ("MERGED", "2026-11-15T00:00:00Z", (9, []))]:
                with self.subTest(state=state, merged_at=merged_at):
                    existing.update(state=state, mergedAt=merged_at)
                    result = pr_watch_repair.commit_and_push_repair("example/one", merged, str(self.repo), base, "failure",
                                                                      {"profile": "commands", "commands": [["true"]]},
                                                                      allow_merge_only=True, original_pr=pr)
                    self.assertEqual(result, expected)
                    if expected[0] == 8:
                        self.assertEqual(created, [])
                        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/pr-watch/*"), "")
        self.assertEqual(len(created), 1)

    def test_stale_open_continuation_does_not_block_repair_after_fork_push(self):
        fork, old_repair, base = self.fork_merge()
        self.git("checkout", "-q", "--detach", fork["headRefOid"])
        self.git("commit", "--allow-empty", "-qm", "new fork head")
        fork["headRefOid"] = self.git("rev-parse", "HEAD")
        repaired_head = pr_watch_repair.update_branch_with_main("example/one", fork, str(self.repo), base)
        repaired = dict(fork, headRefOid=repaired_head)
        old_head = old_repair["headRefOid"]
        existing = {"number": 8, "title": "fix: verification repair", "body": "Continuation of #7.",
                    "state": "OPEN", "headRefOid": old_head}

        def github(argv, **kwargs):
            if "--search" in argv:
                return [existing]
            if "--head" in argv:
                return []
            return {"number": 9}

        original_checked = pr_watch_repair.checked

        def checked(argv, **kwargs):
            if argv[:2] == ["gh", "api"]:
                self.assertEqual(argv[2], "repos/example/one/compare/%s...%s" % (fork["headRefOid"], old_head))
                return "diverged\n"
            return original_checked(argv, **kwargs)

        snapshots = {7: fork, 9: dict(repaired, number=9)}
        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}), \
                patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: snapshots[number]), \
                patch.object(pr_watch_repair, "gh_json", side_effect=github) as api, \
                patch.object(pr_watch_repair, "checked", side_effect=checked):
            number, results = pr_watch_repair.commit_and_push_repair(
                "example/one", repaired, str(self.repo), base, "failure", {"profile": "commands", "commands": [["true"]]},
                allow_merge_only=True, original_pr=fork)
        self.assertEqual((number, results), (9, []))
        branch = "pr-watch/repair-7-" + repaired_head[:12]
        self.assertIn("headRefOid", api.call_args_list[0].args[0][-1].split(","))
        self.assertEqual(json.loads(api.call_args.kwargs["input_text"])["head"], "example:" + branch)
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/" + branch).split()[0], repaired_head)

    def test_fork_continuation_retries_an_unchanged_absent_branch_without_force(self):
        pr, merged, base = self.fork_merge()
        snapshots = {7: pr, 8: dict(merged, number=8)}
        created = []
        original_checked = pr_watch_repair.checked
        pushes = []

        def reject_first_push(argv, **kwargs):
            if argv[:2] == ["git", "push"]:
                pushes.append(argv)
                if len(pushes) == 1:
                    raise pr_watch_repair.WatchError(
                        "git failed (1): [remote rejected] missing necessary objects")
            return original_checked(argv, **kwargs)

        def github(argv, **kwargs):
            if argv[:2] == ["pr", "list"]:
                return []
            created.append(json.loads(kwargs["input_text"]))
            return {"number": 8}

        common = dict(verify_config={"profile": "commands", "commands": [["true"]]},
                      allow_merge_only=True, original_pr=pr)
        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}), \
                patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: snapshots[number]), \
                patch.object(pr_watch_repair, "gh_json", side_effect=github), \
                patch.object(pr_watch_repair, "checked", side_effect=reject_first_push):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "missing necessary objects"):
                pr_watch_repair.commit_and_push_repair("example/one", merged, str(self.repo), base, "failure", **common)
            self.assertEqual(created, [])
            self.assertEqual(self.git("ls-remote", "origin", "refs/heads/pr-watch/*"), "")
            self.assertEqual(self.git("rev-parse", "HEAD"), merged["headRefOid"])
            number, _ = pr_watch_repair.commit_and_push_repair("example/one", merged, str(self.repo), base, "failure", **common)
        self.assertEqual(number, 8)
        self.assertTrue(all("--force" not in argv and "--force-with-lease" not in argv for argv in pushes))
        branch = "refs/heads/pr-watch/repair-7-" + merged["headRefOid"][:12]
        self.assertEqual(self.git("ls-remote", "origin", branch).split()[0], merged["headRefOid"])
        self.assertEqual(created[0]["head"], "example:pr-watch/repair-7-" + merged["headRefOid"][:12])

    def test_fork_continuation_refuses_a_main_rewritten_before_the_push(self):
        pr, merged, base = self.fork_merge()
        rewritten = self.push_main("rewritten", parent=self.base)
        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={"repair_command": "repair-worker"}), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=dict(pr, baseRefOid=rewritten)), \
                patch.object(pr_watch_repair, "gh_json") as gh:
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "base moved before the repair push"):
                pr_watch_repair.commit_and_push_repair("example/one", merged, str(self.repo), base, "failure",
                                                       {"profile": "commands", "commands": [["true"]]},
                                                       allow_merge_only=True, original_pr=pr)
        gh.assert_not_called()
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/pr-watch/*"), "")

    def test_an_ordinary_repair_refuses_a_forward_moving_base(self):
        newer = self.push_main("newer")
        (self.repo / "repair.txt").write_text("repair\n")
        self.git("add", "repair.txt")
        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=dict(self.pr, baseRefOid=newer)):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "head/base moved"):
                pr_watch_repair.commit_and_push_repair("example/one", self.pr, str(self.repo), self.main, "failure",
                                                       {"profile": "commands", "commands": [["true"]]})
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    def test_a_model_repair_refuses_a_rewritten_base(self):
        rewritten = self.push_main("rewritten", parent=self.base)
        (self.repo / "repair.txt").write_text("repair\n")
        self.git("add", "repair.txt")
        with patch.object(pr_watch_repair, "verify", return_value=[]), self.passthrough(), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=dict(self.pr, baseRefOid=rewritten)):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "head/base moved"):
                pr_watch_repair.commit_and_push_repair("example/one", self.pr, str(self.repo), self.main, "failure",
                                                       {"profile": "commands", "commands": [["true"]]})
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    def conflicting_merge(self):
        self.git("checkout", "-q", "main")
        (self.repo / "file.txt").write_text("main conflict\n")
        self.git("commit", "-qam", "main conflict")
        self.git("push", "-q", "origin", "main")
        self.main = self.git("rev-parse", "HEAD")
        self.pr["baseRefOid"] = self.main
        self.git("checkout", "-q", "fix/example")

    def conflict_worker(self, action, envelope='{"is_error":false,"permission_denials":[],"result":"resolved"}'):
        def resolve(argv, **kwargs):
            if argv[0] == "git":
                command = argv[1:]
                while command and command[0] == "-c":
                    command = command[2:]
                if command and command[0] == "merge":
                    return subprocess.run(argv, cwd=kwargs.get("cwd"), env=self.env, capture_output=True, text=True)
                result = subprocess.run(argv, cwd=kwargs.get("cwd"), env=self.env, capture_output=True, text=True)
                if result.returncode and command[:2] != ["merge-base", "--is-ancestor"]:
                    self.fail(result.stdout + result.stderr)
                return result
            self.assertEqual(Path(argv[0]).name, "bwrap")
            self.assertEqual(argv[1], "--args-fd")
            self.assertEqual(argv[argv.index("--") + 1], "repair-worker")
            self.assertNotIn("GH_TOKEN", kwargs["environ"])
            self.assertNotIn("origin", self.git("remote", cwd=kwargs["cwd"]))
            prompt = kwargs["input_text"]
            edit_root = Path(next(line.split(": ", 1)[1] for line in prompt.splitlines()
                                 if line.startswith("Editable copies: ")))
            action(Path(kwargs["cwd"]), edit_root)
            return subprocess.CompletedProcess(argv, 0, envelope, "")
        return patch.object(pr_watch_repair, "run", side_effect=resolve)

    def test_worker_clone_ignores_source_hooks_and_local_configuration(self):
        hook = self.root / "hook.sh"
        hook.write_text("#!/bin/sh\nprintf executed > " + str(self.root / "hook-ran") + "\n")
        hook.chmod(0o755)
        self.git("config", "core.hooksPath", str(self.root))
        self.git("config", "user.name", "Untrusted source name")
        with pr_watch_repair.repair_repository_copy(str(self.repo)) as copy:
            self.assertEqual(self.git("config", "--local", "--get", "core.hooksPath", cwd=copy), os.devnull)
            self.assertEqual(subprocess.run(["git", "config", "--local", "--get", "user.name"], cwd=copy,
                                            env=self.env, capture_output=True, text=True).returncode, 1)
            self.assertFalse((self.root / "hook-ran").exists())

    def test_conflict_clone_fetches_a_main_not_reachable_from_the_pr(self):
        self.conflicting_merge()
        result = subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com",
                                 "merge", "--no-ff", "--no-edit", self.main],
                                cwd=self.repo, env=self.env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(pr_watch_repair.unmerged_paths(str(self.repo)), ["file.txt"])
        try:
            with pr_watch_repair.repair_repository_copy(str(self.repo), [self.main]) as copy:
                # A normal branch clone has no object for a non-ancestor main.
                # The conflict path must explicitly fetch that exact parent.
                self.assertEqual(self.git("cat-file", "-t", self.main, cwd=copy), "commit")
                result = subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com",
                                         "merge", "--no-ff", "--no-edit", self.main],
                                        cwd=copy, env=self.env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(pr_watch_repair.unmerged_paths(str(copy)), ["file.txt"])
        finally:
            self.git("merge", "--abort")

    def test_conflict_clone_refuses_an_unavailable_main_parent(self):
        with self.assertRaisesRegex(pr_watch_repair.WatchError, "not our ref"):
            with pr_watch_repair.repair_repository_copy(str(self.repo), ["d" * 40]):
                pass

    def test_conflict_worker_rejects_a_permission_denial_even_after_a_correct_edit(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").write_text("pr\nmain conflict\n")
        with self.conflict_worker(resolve, '{"is_error":false,"permission_denials":[{"tool_name":"Bash"}]}'), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "outside its edit-only boundary"):
                pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_conflict_worker_rejects_a_gitlink_resolution(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").unlink()
            nested = edits / "file.txt"
            nested.mkdir()
        with self.conflict_worker(resolve), patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "unsupported file type"):
                pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    def test_conflict_sandbox_only_exposes_conflict_files_for_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = real_dir(directory)
            (root / "conflicted.txt").write_text("conflict\n")
            (root / "other.txt").write_text("keep\n")
            (root / "home").mkdir()
            edits = root / "edits"
            edits.mkdir()
            (edits / "conflicted.txt").write_text("conflict\n")
            argv = pr_watch_repair.repair_worker_argv(["repair-worker"], root, home=root / "home",
                                                      writable_paths=["conflicted.txt"], edit_root=edits)
            self.assertIn(["--ro-bind", str(root), str(root)], [argv[i:i + 3] for i in range(len(argv) - 2)])
            self.assertIn(["--bind", str(edits), str(edits)], [argv[i:i + 3] for i in range(len(argv) - 2)])
            self.assertNotIn(["--ro-bind", str(root / "conflicted.txt"), str(root / "conflicted.txt")],
                             [argv[i:i + 3] for i in range(len(argv) - 2)])
    def test_large_mount_vector_reaches_the_worker_without_execve_overflow(self):
        with tempfile.TemporaryDirectory() as directory:
            root = real_dir(directory)
            (root / "conflicted.txt").write_text("conflict\n")
            for index in range(6000):
                path = root / ("directory-%04d" % index) / "nested" / "deeper" / ("a-much-longer-file-name-%04d.txt" % index)
                path.parent.mkdir(parents=True)
                path.write_text("read only\n")
            home = root / "home"
            home.mkdir()
            edits = root / "edits"
            edits.mkdir()
            (edits / "conflicted.txt").write_text("conflict\n")
            argv = pr_watch_repair.repair_worker_argv(["/usr/bin/python3", "-c", "print('worker reached')"],
                                                      root, home=home, writable_paths=["conflicted.txt"], edit_root=edits)
            self.assertEqual(argv[1], "--args-fd")
            self.assertLess(len(argv), 200)
            result = pr_watch_repair.run(argv[:-1] + [
                "from pathlib import Path\n"
                "Path('" + str(edits) + "/conflicted.txt').write_text('resolved\\n')\n"
                "try:\n"
                " Path('directory-0001/nested/deeper/a-much-longer-file-name-0001.txt').write_text('not allowed')\n"
                "except OSError:\n"
                " print('unrelated file stays read-only')\n"
                "print('worker reached')"], cwd=root, timeout=90,
                environ={key: value for key, value in os.environ.items() if key in ("PATH", "LANG")})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("worker reached", result.stdout)
            self.assertIn("unrelated file stays read-only", result.stdout)
            self.assertEqual((edits / "conflicted.txt").read_text(), "resolved\n")
            self.assertEqual((root / "conflicted.txt").read_text(), "conflict\n")

    def test_committed_conflict_repair_never_calls_merge_abort(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").write_text("pr\nmain conflict\n")
        with self.conflict_worker(resolve), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), updated)
        self.assertEqual(self.git("show", "-s", "--format=%P", updated).split(), [self.head, self.main])
        self.assertFalse((self.repo / ".git" / "MERGE_HEAD").exists())

    # #16701: the whitespace gate saw every file the merge brought in, so one blank line at the end of a file main
    # carries failed every conflict repair, naming a file no worker had edited.
    def test_conflict_repair_ignores_an_unrelated_blank_line_at_eof_in_main(self):
        self.conflicting_merge()
        self.git("checkout", "-q", "main")
        (self.repo / "main-note.txt").write_text("note\n\n")
        self.git("add", "main-note.txt")
        self.git("commit", "-qm", "a main file that ends with a blank line")
        self.git("push", "-q", "origin", "main")
        self.main = self.git("rev-parse", "HEAD")
        self.pr["baseRefOid"] = self.main
        self.git("checkout", "-q", "fix/example")
        def resolve(root, edits):
            (edits / "file.txt").write_text("pr\nmain conflict\n")
        with self.conflict_worker(resolve), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), updated)
        self.assertEqual((self.repo / "main-note.txt").read_text(), "note\n\n")

    def test_conflict_repair_still_refuses_a_blank_line_the_resolution_adds(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").write_text("pr\nmain conflict\n\n")
        with self.conflict_worker(resolve), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "new blank line at EOF"):
                pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)

    def test_conflict_worker_resolves_only_conflict_and_preserves_merge_parents(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").write_text("pr\nmain conflict\n")
        with self.conflict_worker(resolve), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual((self.repo / "file.txt").read_text(), "pr\nmain conflict\n")
        self.assertEqual((self.repo / "main.txt").read_text(), "new main\n")
        self.assertEqual(self.git("show", "-s", "--format=%P", updated).split(), [self.head, self.main])
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_conflict_worker_cannot_create_an_unconflicted_workspace_file(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "main.txt").write_text("overwritten main\n")
            (edits / "file.txt").write_text("resolved\n")
        with self.conflict_worker(resolve), patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "outside the conflict paths"):
                pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    def test_conflict_worker_must_resolve_every_conflict(self):
        self.conflicting_merge()
        with self.conflict_worker(lambda root, edits: None), patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "unresolved"):
                pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_conflict_worker_refuses_a_moved_pr_head(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").write_text("resolved\n")
        with self.conflict_worker(resolve), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=dict(self.pr, headRefOid="d" * 40)):
            with self.assertRaisesRegex(pr_watch_repair.WatchError, "head moved"):
                pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("ls-remote", "origin", "refs/heads/fix/example").split()[0], self.head)

    def test_nested_conflict_directory_stays_writable_in_real_bwrap(self):
        with tempfile.TemporaryDirectory() as directory:
            root = real_dir(directory)
            nested = root / "src" / "deep"
            nested.mkdir(parents=True)
            (nested / "file.txt").write_text("base\n")
            (root / "other.txt").write_text("keep\n")
            home = root / "home"
            home.mkdir()
            script = nested / "write.py"
            script.write_text("from pathlib import Path\nimport sys\nroot = Path(sys.argv[1])\npath = root / 'file.txt'\ntemporary = root / '.file.tmp'\ntemporary.write_text('resolved\\n')\ntemporary.replace(path)\n")
            edits = root / "edits" / "src" / "deep"
            edits.mkdir(parents=True)
            (edits / "file.txt").write_text("base\n")
            argv = pr_watch_repair.repair_worker_argv(["/usr/bin/python3", str(script), str(root / "edits" / "src" / "deep")], root,
                                                      home=home, writable_paths=["src/deep/file.txt"], edit_root=root / "edits")
            result = pr_watch_repair.run(argv, cwd=root, timeout=45,
                                         environ={key: value for key, value in os.environ.items() if key in ("PATH", "LANG")})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((edits / "file.txt").read_text(), "resolved\n")
            self.assertEqual((nested / "file.txt").read_text(), "base\n")

    def test_pr_symlink_cannot_mount_an_external_sentinel_into_the_worker(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repo"
            repo.mkdir()
            (repo / "file.txt").write_text("conflict\n")
            sentinel = root / "sentinel.txt"
            sentinel.write_text("must not be mounted\n")
            (repo / "link.txt").symlink_to(sentinel)
            (root / "home").mkdir()
            (root / "edits").mkdir()
            (root / "edits" / "file.txt").write_text("conflict\n")
            argv = pr_watch_repair.repair_worker_argv(["repair-worker"], repo, home=root / "home",
                                                      writable_paths=["file.txt"], edit_root=root / "edits")
            triples = [argv[i:i + 3] for i in range(len(argv) - 2)]
            self.assertNotIn(["--ro-bind", str(repo / "link.txt"), str(repo / "link.txt")], triples)
            self.assertNotIn(["--ro-bind", str(sentinel), str(sentinel)], triples)
            result = pr_watch_repair.run(argv[:-1] + ["/usr/bin/python3", "-c",
                                                       "from pathlib import Path\ntry:\n print(Path('link.txt').read_text())\nexcept FileNotFoundError:\n print('external sentinel unavailable')"],
                                         cwd=repo, timeout=45,
                                         environ={key: value for key, value in os.environ.items() if key in ("PATH", "LANG")})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), "external sentinel unavailable")

    def test_conflict_worker_can_resolve_a_deletion(self):
        self.conflicting_merge()
        def resolve(root, edits):
            (edits / "file.txt").unlink()
            # The real edit-only worker cannot stage through its read-only
            # index. Leave the conflict in the index for the outer stager.
        with self.conflict_worker(resolve), patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main, {"repair_command": "repair-worker"})
        self.assertFalse((self.repo / "file.txt").exists())
        self.assertEqual(self.git("show", "-s", "--format=%P", updated).split(), [self.head, self.main])
        self.assertEqual(self.git("status", "--porcelain"), "")
    def test_conflict_worker_can_resolve_a_missing_source_with_a_deletion(self):
        self.conflicting_merge()
        self.git("rm", "-q", "file.txt")
        self.git("commit", "-qm", "delete conflicted file")
        head = self.git("rev-parse", "HEAD")
        self.pr["headRefOid"] = head
        self.head = head
        with self.conflict_worker(lambda root, edits: None), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=self.pr), \
                patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}):
            updated = pr_watch_repair.update_branch_with_main("example/one", self.pr, str(self.repo), self.main,
                                                              {"repair_command": "repair-worker"})
        self.assertTrue((self.repo / "file.txt").exists())
        self.assertEqual(self.git("show", "-s", "--format=%P", updated).split(), [head, self.main])


if __name__ == "__main__":
    unittest.main()
