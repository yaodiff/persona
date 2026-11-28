#!/usr/bin/env python3
"""Recognize the single open, administrator-owned release candidate for one channel."""
from __future__ import annotations

import argparse
import json
from urllib.parse import urlsplit

import release_pr


def _ascii_equal(value, expected):
    return (
        isinstance(value, str)
        and isinstance(expected, str)
        and value.isascii()
        and expected.isascii()
        and value.lower() == expected.lower()
    )


def _validate_repository(repository):
    if not isinstance(repository, str) or not release_pr.REPOSITORY.fullmatch(repository):
        raise release_pr.ReleaseError("a public owner/repository is required")
    owner, name = repository.split("/", 1)
    if owner in (".", "..") or name in (".", ".."):
        raise release_pr.ReleaseError("a public owner/repository is required")
    return repository


def _validate_branch(branch):
    if (
        not isinstance(branch, str)
        or not branch
        or branch.startswith("refs/")
        or ":" in branch
        or "\\" in branch
        or ".." in branch
        or any(ch.isspace() or ord(ch) <= 0x20 or 0x7F <= ord(ch) <= 0x9F for ch in branch)
        or any(part in ("", ".", "..") for part in branch.split("/"))
    ):
        raise release_pr.ReleaseError("a valid release branch name is required")
    return branch


def _validate_html_url(html_url):
    if (
        not isinstance(html_url, str)
        or not html_url.startswith("https://")
        or any(ch.isspace() or ord(ch) < 0x20 or 0x7F <= ord(ch) <= 0x9F for ch in html_url)
    ):
        raise release_pr.ReleaseError("an open release candidate has no valid PR url")
    try:
        parsed = urlsplit(html_url)
        _ = parsed.port
    except ValueError as error:
        raise release_pr.ReleaseError("an open release candidate has no valid PR url") from error
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or not parsed.netloc
        or "@" in parsed.netloc
        or "\\" in parsed.netloc
        or not parsed.path.startswith("/")
    ):
        raise release_pr.ReleaseError("an open release candidate has no valid PR url")
    return html_url


def load_pull_pages(raw):
    """Parse one JSON array or gh api --paginate concatenated JSON arrays into pull dicts."""
    if not isinstance(raw, str):
        raise release_pr.ReleaseError("open release PR response must be text")
    decoder = json.JSONDecoder()
    length = len(raw)
    index = 0
    pulls = []
    pages = 0
    while index < length:
        while index < length and raw[index].isspace():
            index += 1
        if index >= length:
            break
        try:
            page, index = decoder.raw_decode(raw, index)
        except json.JSONDecodeError as error:
            raise release_pr.ReleaseError("open release PR response is not valid JSON") from error
        if not isinstance(page, list):
            raise release_pr.ReleaseError("open release PR response is not a list")
        for entry in page:
            if not isinstance(entry, dict):
                raise release_pr.ReleaseError("open release PR entry must be an object")
            pulls.append(entry)
        pages += 1
    if pages == 0:
        raise release_pr.ReleaseError("open release PR response is empty")
    return pulls


def _matching_pulls(pulls, repository, matcher, label):
    _validate_repository(repository)
    if isinstance(pulls, (str, bytes, bytearray, dict)) or not hasattr(pulls, "__iter__"):
        raise release_pr.ReleaseError("open release PR response is not a list")
    by_number = {}
    for pull in pulls:
        if not isinstance(pull, dict):
            raise release_pr.ReleaseError("open release PR entry must be an object")
        if pull.get("state") not in ("open", "OPEN") or pull.get("draft") is not False:
            continue
        base = pull.get("base")
        if not isinstance(base, dict) or base.get("ref") != "main":
            continue
        head = pull.get("head")
        head_repo = head.get("repo") if isinstance(head, dict) else None
        if not isinstance(head_repo, dict) or not _ascii_equal(head_repo.get("full_name"), repository):
            continue
        user = pull.get("user")
        if not isinstance(user, dict) or not _ascii_equal(user.get("login"), "chyroc"):
            continue
        if not matcher(pull, head):
            continue
        number = pull.get("number")
        if type(number) is not int or number <= 0:
            raise release_pr.ReleaseError("an open release candidate has no valid PR number")
        if number not in by_number:
            by_number[number] = pull
    candidates = list(by_number.values())
    if len(candidates) > 1:
        numbers = ", ".join("#" + str(pull["number"]) for pull in candidates)
        raise release_pr.ReleaseError(
            "multiple open " + label + " release candidates exist (" + numbers
            + "); explicitly supersede all but one without moving their immutable sources"
        )
    return candidates[0] if candidates else None


def active_candidate(pulls, repository, channel):
    """Return the one eligible PR for a channel; do not silently choose among competing sources."""
    if channel not in ("release", "beta"):
        raise release_pr.ReleaseError("channel must be release or beta")

    def matches_channel(pull, _head):
        title = pull.get("title")
        if not isinstance(title, str):
            return False
        try:
            release_pr.release_title(title, channel == "beta")
        except release_pr.ReleaseError:
            return False
        return True

    return _matching_pulls(pulls, repository, matches_channel, channel)


def active_branch_candidate(pulls, repository, branch):
    """Return the one eligible same-repo admin PR on the exact release branch, ignoring forks and drafts."""
    _validate_branch(branch)
    return _matching_pulls(pulls, repository, lambda _pull, head: head.get("ref") == branch, branch)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    selector = parser.add_mutually_exclusive_group(required=True)
    selector.add_argument("--channel", choices=("release", "beta"))
    selector.add_argument("--branch")
    parser.add_argument("--pulls", required=True, help="JSON list (or paginated lists) from the open-PR API")
    args = parser.parse_args()
    try:
        _validate_repository(args.repository)
        with open(args.pulls, encoding="utf-8") as stream:
            pulls = load_pull_pages(stream.read())
        if args.channel is not None:
            candidate = active_candidate(pulls, args.repository, args.channel)
        else:
            candidate = active_branch_candidate(pulls, args.repository, args.branch)
        if candidate:
            html_url = _validate_html_url(candidate.get("html_url"))
            print(candidate["number"])
            print(html_url)
    except (OSError, release_pr.ReleaseError) as error:
        raise SystemExit(str(error))


if __name__ == "__main__":
    main()
