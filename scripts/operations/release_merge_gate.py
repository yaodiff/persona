#!/usr/bin/env python3
"""Require the release PR's exact verified merge result before a manual merge."""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from urllib.parse import urlencode

SHA = re.compile(r"[0-9a-f]{40}\Z")
PR_NUMBER = re.compile(r"[1-9][0-9]*\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")


class ReleaseMergeError(ValueError):
    pass


def _validate_repository(repository):
    if not isinstance(repository, str) or not REPOSITORY.fullmatch(repository):
        raise ReleaseMergeError("release repository must be owner/name")
    owner, name = repository.split("/", 1)
    if owner in (".", "..") or name in (".", ".."):
        raise ReleaseMergeError("release repository must be owner/name")


def _validate_pr_number(number):
    if isinstance(number, bool) or not PR_NUMBER.fullmatch(str(number)):
        raise ReleaseMergeError("release PR number must be a positive integer")


def _load_json_pages(raw):
    """Parse one or more JSON objects emitted by `gh api --paginate`."""
    if not isinstance(raw, str):
        raise TypeError("expected JSON text")
    decoder = json.JSONDecoder()
    index = 0
    length = len(raw)
    pages = []
    while index < length:
        while index < length and raw[index].isspace():
            index += 1
        if index >= length:
            break
        page, index = decoder.raw_decode(raw, index)
        if not isinstance(page, dict):
            raise TypeError("expected JSON object page")
        pages.append(page)
    if not pages:
        raise json.JSONDecodeError("empty JSON response", raw, 0)
    return pages


def resolve_repository(env=None, runner=None):
    effective_env = os.environ if env is None else env
    repository = (effective_env.get("GITHUB_REPOSITORY") or "").strip()
    if repository:
        _validate_repository(repository)
        return repository
    call = runner or subprocess.check_output
    kwargs = {"text": True}
    if env is not None and runner is None:
        kwargs["env"] = env
    try:
        repository = call(
            ["gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
            **kwargs,
        ).strip()
    except (subprocess.SubprocessError, OSError) as error:
        raise ReleaseMergeError("could not determine release repository") from error
    _validate_repository(repository)
    return repository


def require_verified(repository, number, head, title, runner=None):
    runner = runner or subprocess.check_output
    if not isinstance(title, str):
        raise ReleaseMergeError("release PR title is required")
    if not isinstance(head, str) or not SHA.fullmatch(head):
        raise ReleaseMergeError("release PR head is not a full commit")
    if not title.startswith("release:"):
        return
    _validate_pr_number(number)
    _validate_repository(repository)
    try:
        data = json.loads(runner(["gh", "api", f"repos/{repository}/pulls/{number}"], text=True))
        if not isinstance(data, dict):
            raise TypeError("pull request payload must be a JSON object")
        if data.get("state") != "open" or data.get("draft") is not False:
            raise ReleaseMergeError("release PR is not open and non-draft")
        base = data.get("base") or {}
        head_info = data.get("head") or {}
        if base.get("ref") != "main" or head_info.get("sha") != head:
            raise ReleaseMergeError("release PR source moved or is not main")
        if (head_info.get("repo") or {}).get("full_name") != repository:
            raise ReleaseMergeError("release PR must be in the protected repository")
        if (data.get("user") or {}).get("login") != "chyroc":
            raise ReleaseMergeError("release PR author is not the administrator")
        if data.get("mergeable") is not True or data.get("mergeable_state") in ("dirty", "unknown"):
            raise ReleaseMergeError("release PR has no clean up-to-date merge result yet; wait for GitHub or resolve conflicts first")
        merge_commit = data.get("merge_commit_sha")
        if not isinstance(merge_commit, str) or not SHA.fullmatch(merge_commit):
            raise ReleaseMergeError("release PR has no proposed merge result yet; resolve conflicts first")
        check_pages = _load_json_pages(
            runner(["gh", "api", f"repos/{repository}/commits/{merge_commit}/check-runs?per_page=100", "--paginate"], text=True)
        )
        check_runs = []
        for page in check_pages:
            runs = page["check_runs"]
            if not isinstance(runs, list) or not all(isinstance(check, dict) for check in runs):
                raise TypeError("check_runs must be a list of objects")
            check_runs.extend(runs)
        matching = [check for check in check_runs
                    if check.get("name") == "All available release-PR checks passed"
                    and check.get("head_sha") == merge_commit]
        if not matching or any(check.get("status") != "completed" or check.get("conclusion") != "success" for check in matching):
            raise ReleaseMergeError("all required release-PR checks must pass at the exact merge result")
        artifact_name = f"release-pr-verified-{number}-{merge_commit}"
        artifact_query = urlencode({"name": artifact_name, "per_page": 100})
        artifact_pages = _load_json_pages(
            runner(["gh", "api", f"repos/{repository}/actions/artifacts?{artifact_query}", "--paginate"], text=True)
        )
        artifacts = []
        for page in artifact_pages:
            items = page["artifacts"]
            if not isinstance(items, list) or not all(isinstance(item, dict) for item in items):
                raise TypeError("artifacts must be a list of objects")
            artifacts.extend(items)
        found = [item for item in artifacts
                 if item.get("name") == artifact_name and item.get("expired") is False]
        if not found:
            raise ReleaseMergeError("the exact verified release plan artifact is missing or expired")
    except (KeyError, TypeError, AttributeError, json.JSONDecodeError, subprocess.SubprocessError, OSError) as error:
        raise ReleaseMergeError("could not verify the release PR's exact merge result") from error


def main(argv=None, env=None, runner=None):
    argv = sys.argv if argv is None else argv
    if len(argv) < 4:
        raise ReleaseMergeError("usage: release_merge_gate.py <pr> <head> <title>")
    number, head, title = argv[1], argv[2], argv[3]
    if not isinstance(title, str):
        raise ReleaseMergeError("release PR title is required")
    if not isinstance(head, str) or not SHA.fullmatch(head):
        raise ReleaseMergeError("release PR head is not a full commit")
    if not title.startswith("release:"):
        return
    _validate_pr_number(number)
    repository = resolve_repository(env=env, runner=runner)
    require_verified(repository, number, head, title, runner=runner)


if __name__ == "__main__":
    try:
        main()
    except ReleaseMergeError as error:
        print("release merge gate: " + str(error), file=sys.stderr)
        sys.exit(1)
