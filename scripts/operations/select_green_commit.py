#!/usr/bin/env python3
"""Select the newest first-parent main commit whose full CI workflow passed.

The release scheduler calls this script instead of blindly pinning current main
HEAD. Walking backward along main's first-parent history avoids releasing broken
commits while never regressing to or before the previously released commit.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys

SHA = re.compile(r"[0-9a-f]{40}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
WORKFLOW_FILE = "ci.yml"
REQUIRED_JOB_NAMES = (
    "Manual path selection",
    "Manual repository guards",
    "CI required",
)
REQUIRED_PLATFORMS = (
    "server",
    "web",
    "android",
    "iOS and Watch",
    "core",
    "runtimes",
    "desktop",
    "scripts",
)
ALLOWED_CONCLUSIONS = frozenset(("success", "skipped", "neutral"))


class GreenCommitError(ValueError):
    """A validation or GitHub/git query error while selecting a green commit."""


def _validate_sha(value, label):
    if not isinstance(value, str) or not SHA.fullmatch(value):
        raise GreenCommitError(f"{label} must be a full 40-character commit SHA")


def candidate_commits(head, previous_commit="", max_commits=200, cwd=None, runner=subprocess.check_output):
    """Return first-parent commits strictly newer than previous_commit up to head (newest first)."""
    _validate_sha(head, "head")
    if max_commits < 1:
        raise GreenCommitError("max_commits must be positive")
    if previous_commit:
        _validate_sha(previous_commit, "previous_commit")
        if previous_commit == head:
            return []
        try:
            runner(["git", "merge-base", "--is-ancestor", previous_commit, head],
                   cwd=cwd, text=True, timeout=60)
        except subprocess.CalledProcessError as exc:
            raise GreenCommitError("previous_commit is not an ancestor of head") from exc
        rev_range = f"{previous_commit}..{head}"
    else:
        rev_range = head
    output = runner(
        ["git", "rev-list", "--first-parent", f"--max-count={max_commits}", rev_range],
        cwd=cwd,
        text=True,
        timeout=60,
    )
    commits = [line.strip() for line in output.splitlines() if line.strip()]
    for commit in commits:
        _validate_sha(commit, "candidate commit")
    return commits


def is_full_green_run(run, jobs):
    """Return True only when run is a completed, successful full-platform CI run on main."""
    if not isinstance(run, dict) or not isinstance(jobs, list) or not jobs:
        return False
    if run.get("head_branch") != "main":
        return False
    if run.get("status") != "completed" or run.get("conclusion") != "success":
        return False
    if not SHA.fullmatch(str(run.get("head_sha") or "")):
        return False

    by_name = {}
    for job in jobs:
        if not isinstance(job, dict):
            return False
        name = job.get("name")
        status = job.get("status")
        conclusion = job.get("conclusion")
        if not isinstance(name, str) or not name:
            return False
        if status != "completed" or conclusion not in ALLOWED_CONCLUSIONS:
            return False
        by_name.setdefault(name, []).append(conclusion)

    for required in REQUIRED_JOB_NAMES:
        conclusions = by_name.get(required)
        if not conclusions or not any(item == "success" for item in conclusions):
            return False

    for platform in REQUIRED_PLATFORMS:
        # When a reusable workflow lane is unselected (full=false), GitHub emits either no subjobs
        # or a skipped top-level stub named for that platform. Require at least one passing subjob
        # under `<platform> / ` and no skipped top-level stub.
        if "skipped" in by_name.get(platform, ()):
            return False
        prefix = platform + " / "
        if not any(
            name.startswith(prefix) and "success" in conclusions
            for name, conclusions in by_name.items()
        ):
            return False

    return True


def fetch_successful_runs(repository, workflow=WORKFLOW_FILE, max_pages=3, runner=subprocess.check_output):
    """Fetch recent successful workflow runs on main for the given workflow file."""
    if not REPOSITORY.fullmatch(repository):
        raise GreenCommitError("repository must be owner/name")
    runs = []
    for page in range(1, max_pages + 1):
        endpoint = (
            f"repos/{repository}/actions/workflows/{workflow}/runs"
            f"?branch=main&status=success&per_page=100&page={page}"
        )
        raw = runner(["gh", "api", endpoint], text=True, timeout=120)
        payload = json.loads(raw)
        page_runs = payload.get("workflow_runs") or []
        runs.extend(page_runs)
        if len(page_runs) < 100:
            break
    return runs


def fetch_run_jobs(repository, run_id, max_pages=5, runner=subprocess.check_output):
    """Fetch all jobs for a workflow run."""
    if not REPOSITORY.fullmatch(repository):
        raise GreenCommitError("repository must be owner/name")
    jobs = []
    for page in range(1, max_pages + 1):
        endpoint = f"repos/{repository}/actions/runs/{int(run_id)}/jobs?per_page=100&page={page}"
        raw = runner(["gh", "api", endpoint], text=True, timeout=120)
        payload = json.loads(raw)
        page_jobs = payload.get("jobs") or []
        jobs.extend(page_jobs)
        if len(page_jobs) < 100:
            break
    return jobs


def select_green_commit(candidates, runs, jobs_for_run, workflow=WORKFLOW_FILE):
    """Return metadata for the newest candidate commit with a full green CI run, or None."""
    runs_by_sha = {}
    for run in runs:
        if not isinstance(run, dict):
            continue
        sha = run.get("head_sha")
        if (
            run.get("head_branch") == "main"
            and run.get("status") == "completed"
            and run.get("conclusion") == "success"
            and isinstance(sha, str)
            and SHA.fullmatch(sha)
        ):
            runs_by_sha.setdefault(sha, []).append(run)
    for sha_runs in runs_by_sha.values():
        sha_runs.sort(key=lambda item: int(item.get("id") or 0), reverse=True)

    for commit in candidates:
        for run in runs_by_sha.get(commit, ()):
            run_id = int(run["id"])
            jobs = jobs_for_run(run_id)
            if is_full_green_run(run, jobs):
                result = {
                    "commit": commit,
                    "ci_run_id": run_id,
                    "workflow": workflow,
                }
                if isinstance(run.get("html_url"), str) and run["html_url"]:
                    result["html_url"] = run["html_url"]
                return result
    return None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--previous-commit", default="")
    parser.add_argument("--workflow", default=WORKFLOW_FILE)
    parser.add_argument("--max-commits", type=int, default=200)
    parser.add_argument("--max-pages", type=int, default=3)
    parser.add_argument("--out", type=Path, default=None)
    args = parser.parse_args(argv)

    if not REPOSITORY.fullmatch(args.repository):
        raise GreenCommitError("repository must be owner/name")

    candidates = candidate_commits(args.head, args.previous_commit, max_commits=args.max_commits)
    if not candidates:
        if args.out is not None and args.out.exists():
            args.out.unlink()
        print("No newer commits between previous release and main head.", file=sys.stderr)
        return 0

    runs = fetch_successful_runs(args.repository, workflow=args.workflow, max_pages=args.max_pages)
    selected = select_green_commit(
        candidates,
        runs,
        lambda run_id: fetch_run_jobs(args.repository, run_id),
        workflow=args.workflow,
    )
    if selected is None:
        if args.out is not None and args.out.exists():
            args.out.unlink()
        print(
            f"No newer full green {args.workflow} commit found in {len(candidates)} candidate(s).",
            file=sys.stderr,
        )
        return 0

    if args.out is not None:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(selected, indent=2) + "\n")
    print(selected["commit"])
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (GreenCommitError, OSError, ValueError, subprocess.SubprocessError) as error:
        print("Green CI commit selection failed: " + str(error), file=sys.stderr)
        sys.exit(1)
