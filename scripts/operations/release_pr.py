#!/usr/bin/env python3
"""Coordinate an explicit release PR and its immutable GitHub Actions publication.

The pull request verifies a proposed merge commit without release credentials. A
merged release PR is the only new publication input: the protected workflow
resolves its version and exact merge result, creates the immutable tag once, and
publishes verified artifacts to that tag. Ordinary PR merges do nothing here.
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SHA = re.compile(r"[0-9a-f]{40}\Z")
STABLE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
BETA = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-beta\.([1-9][0-9]*)\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
RELEASE_TITLE = re.compile(r"^release(?:\([^)]*\))?:\s+")
TITLE_VERSION = re.compile(r"^release(?:\([^)]*\))?:\s+v?(\d+\.\d+\.\d+(?:-beta\.\d+)?)\s*$", re.I)


class ReleaseError(ValueError):
    """A safe-to-report release identity or authorization failure."""


def validate_identity(repository, commit):
    if not REPOSITORY.fullmatch(repository) or not SHA.fullmatch(commit):
        raise ReleaseError("a public owner/repository and full merge commit are required")


def release_title(title, beta):
    if not isinstance(title, str) or not RELEASE_TITLE.match(title):
        raise ReleaseError("release PR title must start with release:")
    match = TITLE_VERSION.fullmatch(title.strip())
    if not match:
        raise ReleaseError("release PR title must name exactly vX.Y.Z or vX.Y.Z-beta.N")
    version = match[1]
    if bool(BETA.fullmatch("v" + version)) != beta:
        raise ReleaseError("release PR version and selected channel differ")
    return version


def verify_pr(pr, repository, beta, merged=False, commit=None):
    if not isinstance(pr, dict):
        raise ReleaseError("release PR metadata is missing")
    if pr.get("state") != ("MERGED" if merged else "OPEN") or pr.get("isDraft") is not False:
        raise ReleaseError("release PR must be open and non-draft" if not merged else "release PR must be merged and non-draft")
    if pr.get("baseRefName") != "main" or pr.get("headRefOid") == "":
        raise ReleaseError("release PR must target main")
    head_repo = pr.get("headRepository") or {}
    if head_repo.get("nameWithOwner") != repository:
        raise ReleaseError("a release PR must originate in the protected repository, not a fork")
    if pr.get("author", {}).get("login") != "chyroc":
        raise ReleaseError("only the administrator's release PR can publish")
    if merged and (pr.get("mergeCommit") or {}).get("oid") != commit:
        raise ReleaseError("main's merge result does not belong to this release PR")
    return release_title(pr.get("title", ""), beta)


def tags(repository, runner=subprocess.check_output):
    output = runner(["git", "ls-remote", "--tags", "https://github.com/" + repository + ".git", "refs/tags/v*"],
                    text=True, timeout=120)
    result = {}
    for line in output.splitlines():
        object_id, ref = line.split("\t", 1)
        if ref.startswith("refs/tags/") and SHA.fullmatch(object_id):
            result[ref.removeprefix("refs/tags/")] = object_id
    return result


def next_version(repository, beta, requested="", runner=subprocess.check_output, stable_tag="", reserved=()):
    known = tags(repository, runner)
    if not beta:
        # A stable release is a deliberate PR decision: feature commits bump minor, fix-only commits bump patch.
        # Never silently reuse the current stable tag or guess that an unchanged tree needs another release.
        if not STABLE.fullmatch("v" + requested):
            raise ReleaseError("a stable release PR must request an explicit vX.Y.Z version")
        if "v" + requested in known or requested in reserved:
            raise ReleaseError("version already exists; choose a new immutable release PR version")
        return requested
    stable = []
    if stable_tag:
        match = STABLE.fullmatch(stable_tag)
        if not match:
            raise ReleaseError("latest stable Release is not a semantic version")
        base = tuple(map(int, match.groups()))
    else:
        for tag in known:
            match = STABLE.fullmatch(tag)
            if match:
                stable.append(tuple(map(int, match.groups())))
        if not stable:
            raise ReleaseError("no existing stable release version was found")
        base = max(stable)
    if beta:
        # An orphaned release-PR branch also reserves its version. Its previous failed source must never be
        # overwritten with a different main commit, even while no immutable tag exists for it yet.
        reserved_beta = {value if value.startswith("v") else "v" + value for value in reserved
                         if BETA.fullmatch(value if value.startswith("v") else "v" + value)}
        used = {int(match.group(4)) for tag in (*known, *reserved_beta) if (match := BETA.fullmatch(tag))}
        number = max(used, default=0) + 1
        # An orphan may be newer than the latest tag, so its next number may already be reserved too.
        while number in used:
            number += 1
        version = "%d.%d.%d-beta.%d" % (*base, number)
    else:
        raise ReleaseError("version already exists; choose a new immutable release PR version")
    return version


def feature_subject(subject):
    if re.match(r"(?:feat|feature)(?:\([^)]*\))?\s*:", subject):
        return True
    # Some client features use an imperative subject without Conventional Commits. Only recognize an
    # explicit addition, not every imperative verb: "Fix", "Test" and "Document" remain patch changes.
    if re.match(r"add(?:s|ed)?\s+\S", subject, re.IGNORECASE):
        return True
    client = re.match(r"(?:android|ios|iphone|ipad|watch|watchos|web|desktop)(?:\([^)]*\))?:\s+(.+)", subject, re.IGNORECASE)
    if not client:
        return False
    # Platform-qualified features also use imperative nouns: "saved prompts" or "location sharing".
    action = client.group(1).split(None, 1)[0].lower().rstrip("s")
    return action in {"add", "support", "enable", "introduce", "implement", "save", "saved"}


def stable_version(stable_tag, subjects):
    match = STABLE.fullmatch(stable_tag)
    if not match:
        raise ReleaseError("latest stable release has no semantic version")
    if any(feature_subject(subject) for subject in subjects):
        major, minor, _ = map(int, match.groups())
        return "%d.%d.0" % (major, minor + 1)
    major, minor, patch = map(int, match.groups())
    return "%d.%d.%d" % (major, minor, patch + 1)


def schedule_version(repository, channel, stable_tag, commit, previous_commit, subjects, reserved=()):
    if not isinstance(commit, str) or not SHA.fullmatch(commit):
        raise ReleaseError("the scheduler requires a full main commit")
    if channel == "beta":
        if previous_commit == commit:
            return None
        return next_version(repository, True, stable_tag=stable_tag, reserved=reserved)
    if channel != "release":
        raise ReleaseError("schedule channel must be release or beta")
    if previous_commit == commit:
        return None
    return next_version(repository, False, stable_version(stable_tag, subjects))


def next_build(current_build):
    if type(current_build) is not int or not 0 <= current_build < 999999999999:
        raise ReleaseError("current release build is invalid")
    candidate = current_build + 1
    now = int(subprocess.check_output(["date", "-u", "+%Y%m%d%H%M"], text=True).strip())
    return max(candidate, now)


def write_plan(path, repository, channel, version, commit, pr_number, merge_commit, build=None, source_commit=""):
    validate_identity(repository, commit)
    if source_commit and not SHA.fullmatch(source_commit):
        raise ReleaseError("release PR source head must be a full commit")
    if channel not in ("release", "beta"):
        raise ReleaseError("channel must be release or beta")
    tag = "v" + version
    if not ((channel == "beta" and BETA.fullmatch(tag)) or (channel == "release" and STABLE.fullmatch(tag))):
        raise ReleaseError("version and channel differ")
    if build is None:
        # The administrator chooses a previously unused build number through the protected release workflow.
        # A wall-clock minute is not unique across squash merges, and an older/newer commit must not share TestFlight's
        # build number. Retries keep this plan's number rather than minting another one from the current minute.
        raise ReleaseError("release plan requires an explicit unused build number")
    if type(build) is not int or not re.fullmatch(r"[0-9]{12}", str(build)):
        raise ReleaseError("release build must be a UTC twelve-digit number")
    data = {"schema": 1, "repository": repository, "channel": channel, "version": version,
            "tag": tag, "commit": commit, "build": build, "pr": pr_number,
            "merge_commit": merge_commit, "source_commit": source_commit or commit, "artifacts": []}
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + "\n")
    return data


def load_plan(path, repository, channel, commit):
    data = json.loads(Path(path).read_text())
    if (not isinstance(data, dict) or data.get("schema") != 1 or data.get("repository") != repository
            or data.get("channel") != channel or data.get("commit") != commit):
        raise ReleaseError("release plan does not belong to this exact merge result")
    if not isinstance(data.get("version"), str) or "v" + data["version"] != data.get("tag"):
        raise ReleaseError("release plan version and tag differ")
    if channel == "beta" and not BETA.fullmatch(data["tag"]):
        raise ReleaseError("release plan is not an immutable beta")
    if channel == "release" and not STABLE.fullmatch(data["tag"]):
        raise ReleaseError("release plan is not an immutable stable version")
    if type(data.get("build")) is not int or not re.fullmatch(r"[0-9]{12}", str(data["build"])):
        raise ReleaseError("release plan has no valid build number")
    if not isinstance(data.get("artifacts"), list):
        raise ReleaseError("release plan artifact list is invalid")
    return data


def verify_merged_pr(pr, repository, channel, commit, actor):
    if actor != "chyroc":
        raise ReleaseError("only the administrator can authorize publication")
    if not isinstance(pr, dict) or pr.get("state") != "MERGED" or pr.get("isDraft") is not False:
        raise ReleaseError("only a merged non-draft release PR can publish")
    if pr.get("baseRefName") != "main" or (pr.get("headRepository") or {}).get("nameWithOwner") != repository:
        raise ReleaseError("release PR source or target is not the protected main repository")
    if (pr.get("author") or {}).get("login") != "chyroc":
        raise ReleaseError("release PR author must be the administrator")
    merged_by = (pr.get("mergedBy") or {}).get("login")
    if merged_by != "chyroc":
        raise ReleaseError("the verified release workflow must merge with the administrator's protected token")
    version = release_title(pr.get("title", ""), channel == "beta")
    if (pr.get("mergeCommit") or {}).get("oid") != commit:
        raise ReleaseError("the merged commit differs from the release plan")
    return version


def sha256_stream(stream):
    digest = hashlib.sha256()
    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
        digest.update(chunk)
    return digest.hexdigest()


def check_manifest_bytes(directory, item):
    path = directory / item["name"]
    if not path.is_file() or path.stat().st_size != item.get("size"):
        raise ReleaseError("a release download's size differs from its manifest")
    with path.open("rb") as stream:
        if sha256_stream(stream) != item.get("sha256"):
            raise ReleaseError("a release download's archive digest differs from its manifest")
    with path.open("rb") as stream:
        with gzip.open(stream, "rb") as unpacked:
            if sha256_stream(unpacked) != item.get("binary_sha256"):
                raise ReleaseError("a release download's executable digest differs from its manifest")


def bind_cli_web(plan_path, directory):
    plan = json.loads(Path(plan_path).read_text())
    directory = Path(directory)
    for name in ("install.sh", "VERSION", "SHA256SUMS", "latest.json"):
        path = directory / name
        if not path.is_file() or path.is_symlink() or path.stat().st_size == 0:
            raise ReleaseError("a required release bootstrap file is missing, empty or not a regular file: " + name)
    manifest = json.loads((directory / "latest.json").read_text())
    if (manifest.get("version") != plan.get("version") or manifest.get("commit") != plan.get("commit")
            or manifest.get("build") != plan.get("build") or manifest.get("channel", "release") != plan.get("channel")):
        raise ReleaseError("the built manifest is not the release PR's exact version, source, build and channel")
    artifacts = []
    for path in sorted(directory.iterdir()):
        if not path.is_file():
            continue
        artifacts.append({"name": path.name, "path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
    names = {item["name"] for item in artifacts}
    if not artifacts or len(names) != len(artifacts):
        raise ReleaseError("release downloads are empty or have duplicate names")
    for group in ("files", "links"):
        for item in manifest.get(group, {}).values():
            if item.get("name") not in names:
                raise ReleaseError("the release manifest names a download absent from the verified artifacts")
            check_manifest_bytes(directory, item)
    for group in ("programs", "nodes", "helpers"):
        for platforms in manifest.get(group, {}).values():
            for item in platforms.values():
                if item.get("name") not in names:
                    raise ReleaseError("the release manifest names a node/helper absent from the verified artifacts")
                check_manifest_bytes(directory, item)
    plan["artifacts"] = artifacts
    Path(plan_path).write_text(json.dumps(plan, indent=2) + "\n")
    return plan


def resolve_target_commit(plan_dir, version, channel, merge_commit, cwd=None, runner=subprocess.check_output):
    """Resolve the pinned target commit from .github/release-prs/<version>.json, or fall back to merge_commit."""
    if channel not in ("release", "beta"):
        raise ReleaseError("channel must be release or beta")
    if not isinstance(merge_commit, str) or not SHA.fullmatch(merge_commit):
        raise ReleaseError("a full merge commit is required to resolve the release target")
    tag = "v" + version
    if not ((channel == "beta" and BETA.fullmatch(tag)) or (channel == "release" and STABLE.fullmatch(tag))):
        raise ReleaseError("version and channel differ")
    descriptor = Path(plan_dir) / (version + ".json")
    if not descriptor.exists():
        return merge_commit
    if descriptor.is_symlink() or not descriptor.is_file():
        raise ReleaseError("release target descriptor must be a regular file")
    data = json.loads(descriptor.read_text())
    if not isinstance(data, dict):
        raise ReleaseError("release target descriptor must be a JSON object")
    if data.get("version") != version or data.get("channel") != channel:
        raise ReleaseError("release target descriptor does not match the release PR version and channel")
    target = data.get("commit")
    if not isinstance(target, str) or not SHA.fullmatch(target):
        raise ReleaseError("release target descriptor must name a full 40-character commit SHA")
    if "ci_run_id" in data:
        ci_run_id = data["ci_run_id"]
        if type(ci_run_id) is not int or ci_run_id < 1:
            raise ReleaseError("release target descriptor ci_run_id must be a positive integer")
    if target != merge_commit:
        try:
            runner(["git", "merge-base", "--is-ancestor", target, merge_commit],
                   cwd=cwd, text=True, timeout=60)
        except subprocess.CalledProcessError as exc:
            raise ReleaseError("pinned release target commit is not an ancestor of the merge commit") from exc
    return target


def verify_artifacts(plan_path, repository, channel, commit, additional=(), merge_commit=""):
    plan = load_plan(plan_path, repository, channel, commit)
    expected_merge = merge_commit or commit
    if not isinstance(expected_merge, str) or not SHA.fullmatch(expected_merge) or plan.get("merge_commit") != expected_merge:
        raise ReleaseError("the verified release plan does not name the actual merge commit")
    artifacts = list(plan["artifacts"])
    by_name = {item["name"]: item for item in artifacts}
    for directory in additional:
        for path in sorted(Path(directory).iterdir()):
            if path.is_file():
                item = {"name": path.name, "path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                existing = by_name.get(path.name)
                if existing is not None:
                    # The same verified download may be presented again by another platform's job. A different path
                    # with the same bytes is fine; changed bytes or a different hash is never a duplicate to ignore.
                    if existing["sha256"] != item["sha256"]:
                        raise ReleaseError("another platform's artifact already owns this name: " + path.name)
                    continue
                by_name[path.name] = item
    artifacts = sorted(by_name.values(), key=lambda item: item["name"])
    if not artifacts:
        raise ReleaseError("the verified release plan has no artifacts")
    plan["artifacts"] = artifacts
    for item in plan["artifacts"]:
        path = Path(item.get("path", ""))
        if (not path.is_file() or path.is_symlink() or item.get("name") != path.name
                or hashlib.sha256(path.read_bytes()).hexdigest() != item.get("sha256")):
            raise ReleaseError("a verified artifact is missing, renamed or differs from its receipt")
    Path(plan_path).write_text(json.dumps(plan, indent=2) + "\n")
    return plan


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--channel", choices=("release", "beta"), required=True)
    sub = parser.add_subparsers(dest="command", required=True)
    verify = sub.add_parser("verify-pr")
    verify.add_argument("--pr-json", type=Path, required=True)
    verify.add_argument("--merged", action="store_true")
    verify.add_argument("--commit", default="")
    version = sub.add_parser("next-version")
    version.add_argument("--version", default="")
    version.add_argument("--stable-tag", default="")
    version.add_argument("--reserved", nargs="*", default=[])
    schedule = sub.add_parser("schedule-version")
    schedule.add_argument("--stable-tag", required=True)
    schedule.add_argument("--commit", required=True)
    schedule.add_argument("--previous-commit", required=True)
    schedule.add_argument("--subjects", type=Path, required=True)
    schedule.add_argument("--reserved", nargs="*", default=[])
    plan = sub.add_parser("plan")
    plan.add_argument("--version", required=True)
    plan.add_argument("--commit", required=True)
    plan.add_argument("--pr", type=int, required=True)
    plan.add_argument("--merge-commit", required=True)
    plan.add_argument("--build", type=int, required=True)
    plan.add_argument("--source-commit", default="")
    plan.add_argument("--out", type=Path, required=True)
    check = sub.add_parser("check-plan")
    check.add_argument("--plan", type=Path, required=True)
    check.add_argument("--commit", required=True)
    build_parser = sub.add_parser("next-build")
    build_parser.add_argument("--current", type=int, required=True)
    bind = sub.add_parser("bind-cli-web")
    bind.add_argument("--plan", type=Path, required=True)
    bind.add_argument("--downloads", type=Path, required=True)
    verify_artifacts_parser = sub.add_parser("verify-artifacts")
    verify_artifacts_parser.add_argument("--plan", type=Path, required=True)
    verify_artifacts_parser.add_argument("--commit", required=True)
    verify_artifacts_parser.add_argument("--merge-commit", default="")
    verify_artifacts_parser.add_argument("--add", type=Path, nargs="*", default=[])
    resolve = sub.add_parser("resolve-target")
    resolve.add_argument("--dir", type=Path, required=True)
    resolve.add_argument("--version", required=True)
    resolve.add_argument("--merge-commit", required=True)
    merged = sub.add_parser("verify-merged-pr")
    merged.add_argument("--pr-json", type=Path, required=True)
    merged.add_argument("--commit", required=True)
    merged.add_argument("--actor", required=True)
    args = parser.parse_args(argv)
    if not REPOSITORY.fullmatch(args.repository):
        raise ReleaseError("repository must be owner/name")
    if args.command == "verify-pr":
        print(verify_pr(json.loads(args.pr_json.read_text()), args.repository, args.channel == "beta", args.merged, args.commit))
    elif args.command == "next-version":
        print(next_version(args.repository, args.channel == "beta", args.version, stable_tag=args.stable_tag, reserved=args.reserved))
    elif args.command == "schedule-version":
        subjects = args.subjects.read_text().splitlines()
        selected = schedule_version(args.repository, args.channel, args.stable_tag, args.commit, args.previous_commit, subjects, args.reserved)
        if selected:
            print(selected)
        else:
            print('No new release needed; the exact source is already published.')
    elif args.command == "next-build":
        print(next_build(args.current))
    elif args.command == "plan":
        if args.pr < 1:
            raise ReleaseError("release PR number must be positive")
        validate_identity(args.repository, args.merge_commit)
        data = write_plan(args.out, args.repository, args.channel, args.version, args.commit, args.pr, args.merge_commit, args.build, args.source_commit)
        print(data["tag"])
    elif args.command == "verify-merged-pr":
        print(verify_merged_pr(json.loads(args.pr_json.read_text()), args.repository, args.channel, args.commit, args.actor))
    elif args.command == "bind-cli-web":
        data = bind_cli_web(args.plan, args.downloads)
        print('Bound %d verified CLI/Web artifacts' % len(data['artifacts']))
    elif args.command == "verify-artifacts":
        data = verify_artifacts(args.plan, args.repository, args.channel, args.commit, args.add, args.merge_commit)
        print('Verified %d artifacts for %s' % (len(data['artifacts']), data['tag']))
    elif args.command == "resolve-target":
        print(resolve_target_commit(args.dir, args.version, args.channel, args.merge_commit))
    else:
        data = load_plan(args.plan, args.repository, args.channel, args.commit)
        print(json.dumps({key: data[key] for key in ("tag", "version", "build", "commit", "pr")}, sort_keys=True))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ReleaseError, OSError, ValueError, subprocess.SubprocessError) as error:
        print("Release coordination failed: " + str(error), file=sys.stderr)
        sys.exit(1)
