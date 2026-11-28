"""Tests for selecting the newest full green CI commit on main."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import select_green_commit as selector

SHA_PREV = "1" * 40
SHA_OLD_GREEN = "2" * 40
SHA_NEW_GREEN = "3" * 40
SHA_PARTIAL = "4" * 40
SHA_HEAD_BROKEN = "5" * 40


def make_run(run_id, sha, *, branch="main", status="completed", conclusion="success"):
    return {
        "id": run_id,
        "head_sha": sha,
        "head_branch": branch,
        "status": status,
        "conclusion": conclusion,
        "html_url": f"https://github.com/nwverse/persona/actions/runs/{run_id}",
    }


def make_full_green_jobs(*, omit_platform=None, skip_optional_ios=True, fail_job=None):
    jobs = [
        {"name": "Manual path selection", "status": "completed", "conclusion": "success"},
        {"name": "Manual repository guards", "status": "completed", "conclusion": "success"},
        {"name": "CI required", "status": "completed", "conclusion": "success"},
        {"name": "server / Server build", "status": "completed", "conclusion": "success"},
        {"name": "server / Server managers", "status": "completed", "conclusion": "success"},
        {"name": "web / Web lint, tests and build (1/2)", "status": "completed", "conclusion": "success"},
        {"name": "android / Android lint-build-test-0", "status": "completed", "conclusion": "success"},
        {"name": "android / Android test-1", "status": "completed", "conclusion": "success"},
        {"name": "iOS and Watch / iOS, Watch and HTML tests (Xcode 26.0)", "status": "completed", "conclusion": "success"},
        {"name": "iOS and Watch / Core-only iOS build (Xcode 26.0)", "status": "completed", "conclusion": "success"},
        {"name": "core / Shared Rust core", "status": "completed", "conclusion": "success"},
        {"name": "runtimes / Runtime integrations and TypeScript SDK", "status": "completed", "conclusion": "success"},
        {"name": "desktop / Linux tests", "status": "completed", "conclusion": "success"},
        {"name": "scripts / Scripts and guards shard 0", "status": "completed", "conclusion": "success"},
    ]
    if skip_optional_ios:
        jobs.append({
            "name": "iOS and Watch / iOS 18.6 compatibility (Xcode 26.3)",
            "status": "completed",
            "conclusion": "skipped",
        })
    if omit_platform:
        prefix = omit_platform + " / "
        jobs = [job for job in jobs if not job["name"].startswith(prefix)]
        jobs.append({"name": omit_platform, "status": "completed", "conclusion": "skipped"})
    if fail_job:
        for job in jobs:
            if job["name"] == fail_job:
                job["conclusion"] = "failure"
    return jobs


class SelectGreenCommitTests(unittest.TestCase):
    def test_picks_newest_full_green_commit_skipping_broken_and_partial_commits(self):
        candidates = [SHA_HEAD_BROKEN, SHA_PARTIAL, SHA_NEW_GREEN, SHA_OLD_GREEN]
        runs = [
            make_run(104, SHA_HEAD_BROKEN, conclusion="failure"),
            make_run(103, SHA_PARTIAL, conclusion="success"),
            make_run(102, SHA_NEW_GREEN, conclusion="success"),
            make_run(101, SHA_OLD_GREEN, conclusion="success"),
        ]
        jobs_by_id = {
            103: make_full_green_jobs(omit_platform="server"),
            102: make_full_green_jobs(skip_optional_ios=True),
            101: make_full_green_jobs(),
        }
        selected = selector.select_green_commit(candidates, runs, lambda run_id: jobs_by_id[run_id])
        self.assertEqual(selected["commit"], SHA_NEW_GREEN)
        self.assertEqual(selected["ci_run_id"], 102)
        self.assertEqual(selected["workflow"], "ci.yml")

    def test_rejects_partial_run_even_without_top_level_skipped_stub(self):
        jobs = [job for job in make_full_green_jobs() if not job["name"].startswith("desktop / ")]
        self.assertFalse(selector.is_full_green_run(make_run(200, SHA_NEW_GREEN), jobs))

    def test_rejects_failed_or_missing_guards_or_ci_required(self):
        run = make_run(201, SHA_NEW_GREEN)
        self.assertFalse(selector.is_full_green_run(run, make_full_green_jobs(fail_job="Manual repository guards")))
        self.assertFalse(selector.is_full_green_run(run, make_full_green_jobs(fail_job="CI required")))
        no_guards = [job for job in make_full_green_jobs() if job["name"] != "Manual repository guards"]
        self.assertFalse(selector.is_full_green_run(run, no_guards))

    def test_rejects_non_main_branch_or_incomplete_jobs(self):
        self.assertFalse(selector.is_full_green_run(
            make_run(202, SHA_NEW_GREEN, branch="feature/test"),
            make_full_green_jobs(),
        ))
        in_progress = make_full_green_jobs()
        in_progress[0]["status"] = "in_progress"
        self.assertFalse(selector.is_full_green_run(make_run(203, SHA_NEW_GREEN), in_progress))

    def test_returns_none_when_no_candidate_after_previous_is_green(self):
        candidates = [SHA_HEAD_BROKEN, SHA_PARTIAL]
        runs = [
            make_run(301, SHA_PREV, conclusion="success"),
            make_run(302, SHA_PARTIAL, conclusion="success"),
        ]
        jobs_by_id = {
            301: make_full_green_jobs(),
            302: make_full_green_jobs(omit_platform="android"),
        }
        self.assertIsNone(selector.select_green_commit(candidates, runs, lambda run_id: jobs_by_id[run_id]))

    def test_falls_back_to_earlier_run_for_same_commit_if_latest_was_partial(self):
        candidates = [SHA_NEW_GREEN]
        runs = [
            make_run(402, SHA_NEW_GREEN, conclusion="success"),
            make_run(401, SHA_NEW_GREEN, conclusion="success"),
        ]
        jobs_by_id = {
            402: make_full_green_jobs(omit_platform="web"),
            401: make_full_green_jobs(),
        }
        selected = selector.select_green_commit(candidates, runs, lambda run_id: jobs_by_id[run_id])
        self.assertEqual(selected["ci_run_id"], 401)

    def test_candidate_commits_walks_real_git_first_parent_range(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            env = {
                "GIT_AUTHOR_NAME": "Test",
                "GIT_AUTHOR_EMAIL": "test@example.com",
                "GIT_COMMITTER_NAME": "Test",
                "GIT_COMMITTER_EMAIL": "test@example.com",
            }
            subprocess.run(["git", "init", "-b", "main"], cwd=repo, check=True, capture_output=True)

            def commit(name):
                (repo / "f.txt").write_text(name + "\n")
                subprocess.run(["git", "add", "f.txt"], cwd=repo, check=True)
                subprocess.run(["git", "commit", "-m", name], cwd=repo, env=env, check=True, capture_output=True)
                return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo, text=True).strip()

            c1 = commit("c1")
            c2 = commit("c2")
            c3 = commit("c3")
            self.assertEqual(selector.candidate_commits(c3, c1, cwd=repo), [c3, c2])
            self.assertEqual(selector.candidate_commits(c3, c3, cwd=repo), [])
            with self.assertRaises(selector.GreenCommitError):
                selector.candidate_commits(c1, c3, cwd=repo)

    def test_cli_writes_json_when_found_and_removes_stale_file_when_none(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "green.json"
            with patch.object(selector, "candidate_commits", return_value=[SHA_HEAD_BROKEN, SHA_NEW_GREEN]), \
                 patch.object(selector, "fetch_successful_runs", return_value=[make_run(500, SHA_NEW_GREEN)]), \
                 patch.object(selector, "fetch_run_jobs", return_value=make_full_green_jobs()):
                rc = selector.main([
                    "--repository", "nwverse/persona",
                    "--head", SHA_HEAD_BROKEN,
                    "--previous-commit", SHA_PREV,
                    "--out", str(out),
                ])
            self.assertEqual(rc, 0)
            payload = json.loads(out.read_text())
            self.assertEqual(payload["commit"], SHA_NEW_GREEN)
            self.assertEqual(payload["ci_run_id"], 500)

            with patch.object(selector, "candidate_commits", return_value=[SHA_HEAD_BROKEN]), \
                 patch.object(selector, "fetch_successful_runs", return_value=[]):
                rc = selector.main([
                    "--repository", "nwverse/persona",
                    "--head", SHA_HEAD_BROKEN,
                    "--previous-commit", SHA_PREV,
                    "--out", str(out),
                ])
            self.assertEqual(rc, 0)
            self.assertFalse(out.exists())


if __name__ == "__main__":
    unittest.main()
