#!/usr/bin/env python3
"""Create a release PR once, with bounded backoff for GitHub's transient content-creation limits."""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import tempfile
import time
from pathlib import Path


class ReleasePRError(ValueError):
    pass


REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
PR_URL = re.compile(r"https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)\Z")
RELEASE_ADMINISTRATOR = "chyroc"


def _has_space_or_control(text):
    return any(ch.isspace() or ord(ch) < 0x20 or ord(ch) == 0x7f for ch in text)


def _validate_repository(repository):
    if not isinstance(repository, str) or not REPOSITORY.fullmatch(repository):
        raise ReleasePRError("release repository must be owner/name")
    owner, name = repository.split("/", 1)
    if owner in (".", "..") or name in (".", ".."):
        raise ReleasePRError("release repository must be owner/name")


def _validate_branch(branch):
    if (
        not isinstance(branch, str)
        or not branch
        or branch != branch.strip()
        or ":" in branch
        or _has_space_or_control(branch)
        or branch.startswith("refs/")
    ):
        raise ReleasePRError("release PR branch is invalid")


def _validate_pr_url(url, repository, number=None):
    if not isinstance(url, str) or _has_space_or_control(url):
        raise ReleasePRError("release PR has no valid url")
    match = PR_URL.fullmatch(url)
    if not match:
        raise ReleasePRError("release PR has no valid url")
    url_repo, url_number = match.group(1), int(match.group(2))
    if url_repo.lower() != repository.lower():
        raise ReleasePRError("release PR url does not match repository")
    if number is not None and url_number != number:
        raise ReleasePRError("release PR url does not match PR number")
    return url


def _load_pull_pages(raw):
    """Parse one or more JSON arrays emitted by `gh api --paginate`."""
    if not isinstance(raw, str):
        raise ReleasePRError("existing release PR response is not a list")
    decoder = json.JSONDecoder()
    index = 0
    length = len(raw)
    pulls = []
    saw_page = False
    while index < length:
        while index < length and raw[index].isspace():
            index += 1
        if index >= length:
            break
        try:
            page, index = decoder.raw_decode(raw, index)
        except json.JSONDecodeError as error:
            raise ReleasePRError("existing release PR response is not valid JSON") from error
        if not isinstance(page, list):
            raise ReleasePRError("existing release PR response is not a list")
        saw_page = True
        for pull in page:
            if not isinstance(pull, dict):
                raise ReleasePRError("existing release PR entry must be an object")
            pulls.append(pull)
    if not saw_page:
        raise ReleasePRError("existing release PR response is empty")
    return pulls


def _owned_release_pulls(pulls, repository, branch):
    matched = []
    seen_numbers = set()
    want_repo = repository.lower()
    want_admin = RELEASE_ADMINISTRATOR.lower()
    for pull in pulls:
        if pull.get("state") not in ("open", "OPEN"):
            continue
        base = pull.get("base")
        if not isinstance(base, dict) or base.get("ref") != "main":
            continue
        head = pull.get("head")
        if not isinstance(head, dict) or head.get("ref") != branch:
            continue
        head_repo = head.get("repo")
        full_name = head_repo.get("full_name") if isinstance(head_repo, dict) else None
        if not isinstance(full_name, str) or not full_name.isascii() or full_name.lower() != want_repo:
            continue
        user = pull.get("user")
        login = user.get("login") if isinstance(user, dict) else None
        if not isinstance(login, str) or not login.isascii() or login.lower() != want_admin:
            continue
        number = pull.get("number")
        if type(number) is not int or number <= 0:
            raise ReleasePRError("existing release PR has no valid number")
        if number in seen_numbers:
            continue
        seen_numbers.add(number)
        matched.append(pull)
    return matched


def gh(arguments, token, repository):
    environment = dict(os.environ)
    environment["GH_TOKEN"] = token
    environment.pop("GITHUB_TOKEN", None)
    return subprocess.run(["gh", *arguments], env=environment, capture_output=True, text=True)


def create(repository, title, branch, body, token, attempts=6, wait=30, sleeper=None):
    if not token:
        raise ReleasePRError("persona-release is missing RELEASE_PR_TOKEN; no release PR or branch was created")
    if sleeper is None:
        sleeper = time.sleep
    if attempts < 1:
        raise ReleasePRError("release PR creation needs at least one attempt")
    if wait < 0:
        raise ReleasePRError("retry wait cannot be negative")
    _validate_repository(repository)
    if not isinstance(title, str) or not title.strip():
        raise ReleasePRError("release PR title is required")
    _validate_branch(branch)
    # REST keeps release-PR recovery independent of GraphQL's separate rate budget. Both the read and the create
    # stay within the protected token's existing repository/pull-request permissions.
    existing = gh(["api", f"repos/{repository}/pulls?state=open&base=main&per_page=100", "--paginate"], token, repository)
    if existing.returncode:
        raise ReleasePRError("could not check the existing release PR: " + existing.stderr.strip())
    # A slash in a server-side head filter can return unrelated PRs, and forks can spoof the branch name.
    # Only an open, administrator-owned PR from the target repository itself owns this version.
    pulls = _owned_release_pulls(_load_pull_pages(existing.stdout), repository, branch)
    if len(pulls) > 1:
        raise ReleasePRError("more than one open release PR owns " + branch)
    if pulls:
        if pulls[0].get("draft") is not False:
            raise ReleasePRError("the existing release PR is a draft or has unknown draft state")
        existing_title = pulls[0].get("title")
        if existing_title != title:
            raise ReleasePRError("the existing release PR has a different version: " + str(existing_title))
        html_url = _validate_pr_url(pulls[0].get("html_url"), repository, number=pulls[0]["number"])
        print(html_url)
        return
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", prefix="persona-release-pr-", delete=False) as stream:
        json.dump({"title": title, "head": branch, "base": "main", "body": body}, stream)
        payload = stream.name
    try:
        for attempt in range(attempts):
            created = gh(["api", "-X", "POST", f"repos/{repository}/pulls", "--input", payload], token, repository)
            if created.returncode == 0:
                try:
                    created_data = json.loads(created.stdout)
                except json.JSONDecodeError as error:
                    raise ReleasePRError("created release PR response is not valid JSON") from error
                if not isinstance(created_data, dict):
                    raise ReleasePRError("created release PR response must be an object")
                created_number = None
                if "number" in created_data:
                    raw_number = created_data["number"]
                    if type(raw_number) is not int or raw_number <= 0:
                        raise ReleasePRError("created release PR has no valid number")
                    created_number = raw_number
                html_url = _validate_pr_url(created_data.get("html_url"), repository, number=created_number)
                print(html_url)
                return
            transient = re.search(r"was submitted too quickly|secondary rate limit|temporarily blocked|HTTP 50[234]", created.stderr, re.I)
            if not transient or attempt + 1 == attempts:
                raise ReleasePRError("release PR creation failed after the bounded retry budget: " + created.stderr.strip())
            delay = wait * (2 ** attempt)
            print(f"release PR creation was throttled; retrying in {delay} seconds", flush=True)
            sleeper(delay)
    finally:
        Path(payload).unlink(missing_ok=True)
    raise ReleasePRError("release PR creation exhausted its bounded retry budget")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--title", required=True)
    parser.add_argument("--branch", required=True)
    parser.add_argument("--body-file", type=Path, required=True)
    parser.add_argument("--attempts", type=int, default=int(os.environ.get("RELEASE_PR_CREATE_ATTEMPTS", "6")))
    parser.add_argument("--wait", type=int, default=int(os.environ.get("RELEASE_PR_CREATE_WAIT", "30")))
    args = parser.parse_args()
    if args.wait < 0:
        parser.error("retry wait cannot be negative")
    body = args.body_file.read_text(encoding="utf-8")
    create(args.repository, args.title, args.branch, body, os.environ.get("RELEASE_PR_TOKEN", ""), args.attempts, args.wait)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        raise SystemExit(str(error))
