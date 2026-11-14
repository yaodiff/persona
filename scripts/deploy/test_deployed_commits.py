import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "deployed_commits.py"
GIT = shutil.which("git")


class DeployedCommitsTest(unittest.TestCase):
    """deployed_commits.py against a throwaway repository: a local branch that merged a PR's branch before upstream did,
    and a fake GitHub CLI for the open PRs (#4867)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="persona-deployed-commits-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.repo = self.base / "repo"
        self.bin = self.base / "bin"
        self.bin.mkdir()
        home = self.base / "home"
        home.mkdir()
        self.env = {"GIT_CONFIG_NOSYSTEM": "1", "HOME": str(home),
                    "PATH": f"{self.bin}:{Path(GIT).parent}:/usr/bin:/bin"}
        self.git("init", "-q", "--initial-branch=main", str(self.repo), cwd=self.base)
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.com")
        self.git("remote", "add", "origin", "https://github.com/Owner/Repo.git")
        self.old = self.commit("fix: written for the previous tracker (#31)", "2026-11-06T12:00:00+08:00")
        self.commit("feat: the first (#1)")
        self.git("checkout", "-q", "-b", "feature")
        self.head = self.commit("feat: the open PR's work")
        self.git("checkout", "-q", "-b", "other", "main")
        self.other = self.commit("feat: another PR, not deployed")
        self.git("checkout", "-q", "-b", "local", "main")
        self.commit("fix: a local fix")
        self.git("merge", "-q", "--no-ff", "-m", "Merge branch 'feature' into local", "feature")
        self.deployed = self.git("rev-parse", "HEAD")

    def git(self, *args, cwd=None):
        run = subprocess.run([GIT, *args], cwd=cwd or self.repo, env=self.env, capture_output=True, text=True, check=True)
        return run.stdout.strip()

    def commit(self, subject, date=None):
        env = dict(self.env, GIT_AUTHOR_DATE=date, GIT_COMMITTER_DATE=date) if date else self.env
        subprocess.run([GIT, "commit", "-q", "--allow-empty", "-m", subject], cwd=self.repo, env=env, check=True)
        return self.git("rev-parse", "HEAD")

    def run_script(self, *args):
        return subprocess.run([sys.executable, "-I", str(SCRIPT), *args], cwd=self.repo, env=self.env,
                              capture_output=True, text=True)

    def fake_gh(self, prs):
        gh = self.bin / "gh"
        gh.write_text("#!/bin/sh\necho \"$@\" >> \"$(dirname \"$0\")/calls\"\ncat <<'EOF'\n" + json.dumps(prs) + "\nEOF\n")
        gh.chmod(0o755)

    def test_local_branch_with_named_prs(self):
        out = self.base / "deployed-commits.txt"
        run = self.run_script("--out", str(out), "--no-open-prs", "--pr", f"31={self.head}", "--pr", f"#32={self.other}")
        self.assertEqual(run.returncode, 0, run.stderr)
        lines = out.read_text().splitlines()
        self.assertEqual(lines[:3], ["# persona deployed commits v1", "# repo: owner/repo", f"# pr: 31 {self.head}"])
        self.assertNotIn(f"# pr: 32 {self.other}", lines)  # not in the deployed commit
        self.assertEqual(lines[3], f"{self.deployed} Merge branch 'feature' into local")
        self.assertIn(f"{self.head} feat: the open PR's work", lines)
        self.assertIn("PRs by head: #31", run.stdout)
        self.assertFalse((self.bin / "calls").exists())

    def test_commits_of_the_previous_tracker_are_left_out(self):
        # Its "(#31)" is the previous tracker's number: listed, the hub would take it for this repository's PR #31.
        out = self.base / "deployed-commits.txt"
        run = self.run_script("--out", str(out), "--no-open-prs")
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertNotIn(self.old, out.read_text())
        run = self.run_script("--out", str(out), "--no-open-prs", "--since", "")
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertIn(f"{self.old} fix: written for the previous tracker (#31)", out.read_text().splitlines())

    def test_open_prs_from_github_cli(self):
        self.fake_gh([{"number": 41, "headRefOid": self.head}, {"number": 42, "headRefOid": self.other},
                      {"number": 43, "headRefOid": "f" * 40}])
        run = self.run_script("--out", str(self.base), "--max", "1")
        self.assertEqual(run.returncode, 0, run.stderr)
        lines = (self.base / "deployed-commits.txt").read_text().splitlines()
        self.assertEqual([x for x in lines if x.startswith("# pr:")], [f"# pr: 41 {self.head}"])
        # One commit of history asked for: the PR's head is listed after it, so the hub can check the PR line.
        self.assertEqual(lines[3:], [f"{self.deployed} Merge branch 'feature' into local", f"{self.head} (head of PR 41)"])
        self.assertIn("pr list -R owner/repo --state open", (self.bin / "calls").read_text())

    def test_pr_branch_that_merged_the_base_since(self):
        # After the local build took the PR's branch, main moved on and the PR branch merged it: its head is not in the
        # build, but every commit of its own is. Another PR got a commit of its own since: not deployed as it is.
        self.git("checkout", "-q", "main")
        self.commit("feat: main moved on (#2)")
        self.git("checkout", "-q", "feature")
        self.git("merge", "-q", "--no-ff", "-m", "Merge main into feature", "main")
        merged_head = self.git("rev-parse", "HEAD")
        self.git("checkout", "-q", "-b", "grown", self.head)
        grown = self.commit("feat: more work after the build")
        self.git("checkout", "-q", "local")
        out = self.base / "deployed-commits.txt"
        run = self.run_script("--out", str(out), "--no-open-prs", "--base", "main", "--pr", f"51={merged_head}", "--pr", f"52={grown}")
        self.assertEqual(run.returncode, 0, run.stderr)
        prs = [x for x in out.read_text().splitlines() if x.startswith("# pr:")]
        self.assertEqual(prs, [f"# pr: 51 {self.head}"])
        # Without a base to tell its own commits, only a head the build contains counts.
        run = self.run_script("--out", str(out), "--no-open-prs", "--base", "nowhere", "--pr", f"51={merged_head}")
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual([x for x in out.read_text().splitlines() if x.startswith("# pr:")], [])

    def test_github_cli_signed_out_and_no_repo(self):
        gh = self.bin / "gh"
        gh.write_text("#!/bin/sh\necho 'To get started with GitHub CLI, please run:  gh auth login' >&2\nexit 4\n")
        gh.chmod(0o755)
        run = self.run_script("--out", str(self.base / "f.txt"))
        self.assertEqual(run.returncode, 0, run.stderr)  # gh cannot answer: no PR lines, the history still written
        self.assertIn("PRs by head: none", run.stdout)
        self.assertIn("gh auth login", run.stderr)
        self.git("remote", "set-url", "origin", "https://example.com/somewhere.git")
        run = self.run_script("--out", str(self.base / "g.txt"))
        self.assertNotEqual(run.returncode, 0)
        self.assertIn("--repo", run.stderr)
        self.assertFalse(os.path.exists(self.base / "g.txt"))


if __name__ == "__main__":
    unittest.main()
