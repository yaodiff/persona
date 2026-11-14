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
        with pr_watch_repair.repair_repository_copy(str(self.repo)) as copy:
            self.assertTrue(Path(copy, ".git").is_dir())
            self.assertFalse(Path(copy, ".git").is_file())
            self.assertNotEqual(Path(copy), self.repo)
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


if __name__ == "__main__":
    unittest.main()
