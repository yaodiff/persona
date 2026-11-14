#!/usr/bin/env python3
"""Consume version tags on a private release machine, without copying its credentials.

A private JSON config selects one platform per process. The command is an argv
array, never a shell string, and receives {repo}, {worktree}, {state_dir},
{tag}, {commit}, and {version}. The command uses the machine's existing signing
configuration and returns only after its platform-specific publication checks.
Its private artifact receipt lists files to attach to the GitHub Release.

A separate Claude loop may invoke this script for every new tag. This script
owns source/tag identity, exclusive execution, the exact worktree, and uploads;
it never creates or moves a public tag and never builds without an explicit
publication command. Use --once for one scheduler tick; --watch polls forever.
"""
import argparse
from contextlib import contextmanager
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import string
import subprocess
import sys
import tempfile
import time

TAG = re.compile(r"v[0-9A-Za-z][0-9A-Za-z._-]*\Z")
COMMIT = re.compile(r"[0-9a-f]{40}|[0-9a-f]{64}")
ALLOWED_KEYS = {"repo", "worktree", "state_dir", "repository", "platform", "command", "receipt", "agent"}
FIELDS = {"repo", "worktree", "state_dir", "tag", "commit", "version"}
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")


class ConfigError(ValueError):
    """Configuration or tag identity is invalid; no publication was started."""


def argv(value, fields=FIELDS):
    if not isinstance(value, list) or not value or any(not isinstance(item, str) or not item or "\0" in item for item in value):
        raise ConfigError("command must be a nonempty argv array")
    for item in value:
        try:
            parsed = list(string.Formatter().parse(item))
        except ValueError as error:
            raise ConfigError("invalid command placeholder") from error
        for _, field, spec, conversion in parsed:
            if field is not None and (field not in fields or spec or conversion):
                raise ConfigError("unknown command placeholder: " + str(field))
    return value


def private_file(path):
    if not path.is_file() or path.is_symlink():
        raise ConfigError("private config must be a regular file")
    if path.stat().st_mode & 0o077:
        raise ConfigError("private config permissions must be 0600")
    return path


def load_config(path):
    path = private_file(path.expanduser().resolve())
    config = json.loads(path.read_text())
    if not isinstance(config, dict) or set(config) - ALLOWED_KEYS:
        raise ConfigError("unsupported private release configuration")
    for key in ("repo", "worktree", "state_dir", "repository", "platform"):
        if key not in config:
            raise ConfigError("missing private release configuration: " + key)
    for key in ("repo", "worktree", "state_dir"):
        if not isinstance(config[key], str) or not Path(config[key]).is_absolute():
            raise ConfigError(key + " must be an absolute path")
        config[key] = Path(config[key]).resolve()
    if config["repo"] == config["worktree"]:
        raise ConfigError("publication worktree must not be the shared checkout")
    if config["state_dir"] == config["worktree"] or config["worktree"] in config["state_dir"].parents:
        raise ConfigError("state directory must not be inside the publication worktree")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", config["repository"]):
        raise ConfigError("repository must be owner/name")
    if not re.fullmatch(r"[a-z][a-z0-9-]*", config["platform"]):
        raise ConfigError("platform must be a lowercase identifier")
    argv(config.get("command"))
    if not config.get("receipt"):
        raise ConfigError("receipt is required; use an empty asset list for store-only publication")
    if not isinstance(config["receipt"], str) or not Path(config["receipt"]).is_absolute():
        raise ConfigError("receipt must be an absolute path")
    if "agent" in config:
        argv(config["agent"], FIELDS | {"prompt_file", "log_file"})
    return config


def git(repo, *args):
    result = subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True, text=True, timeout=120)
    return result.stdout.strip()


def resolve_tag(repo, repository, tag):
    if not TAG.fullmatch(tag) or ".." in tag or tag.endswith("."):
        raise ConfigError("invalid version tag")
    remote = git(repo, "ls-remote", "--tags", "https://github.com/" + repository + ".git",
                 "refs/tags/" + tag, "refs/tags/" + tag + "^{}")
    rows = [line.split("\t", 1) for line in remote.splitlines() if "\t" in line]
    if any(len(row) != 2 or not COMMIT.fullmatch(row[0]) for row in rows):
        raise ConfigError("remote tag identity is not a Git object")
    peeled = [row for row in rows if row[1] == "refs/tags/" + tag + "^{}"]
    direct = [row for row in rows if row[1] == "refs/tags/" + tag]
    if len(rows) > 2 or len(peeled) > 1 or len(direct) != 1:
        raise ConfigError("remote tag identity is ambiguous")
    # A lightweight tag names its commit directly; an annotated tag is peeled
    # to its commit by the second ls-remote row.
    commit = peeled[0][0] if peeled else direct[0][0]
    if not COMMIT.fullmatch(commit):
        raise ConfigError("remote tag does not resolve to a commit")
    git(repo, "fetch", "--no-tags", "https://github.com/" + repository + ".git", commit)
    if git(repo, "rev-parse", commit + "^{commit}") != commit:
        raise ConfigError("fetched tag object is not the requested commit")
    # Fail if the tag moved while its commit was being fetched.
    again = git(repo, "ls-remote", "--tags", "https://github.com/" + repository + ".git",
                "refs/tags/" + tag, "refs/tags/" + tag + "^{}")
    if again != remote:
        raise ConfigError("tag moved during release discovery; retry")
    return commit


def prepare_worktree(config, commit):
    repo, worktree = config["repo"], config["worktree"]
    if not (repo / ".git").exists():
        raise ConfigError("configured repo is not a Git repository")
    if not worktree.exists():
        git(repo, "worktree", "add", "--detach", str(worktree), commit)
    else:
        if git(worktree, "rev-parse", "--show-toplevel") != str(worktree):
            raise ConfigError("publication worktree is not its root")
        branch = subprocess.run(["git", "-C", str(worktree), "symbolic-ref", "-q", "HEAD"],
                                capture_output=True, text=True, timeout=30)
        if branch.returncode == 0:
            raise ConfigError("publication worktree must be detached")
        if branch.returncode != 1:
            raise ConfigError("cannot verify publication worktree HEAD")
        if (worktree / git(worktree, "rev-parse", "--git-common-dir")).resolve() != (repo / git(repo, "rev-parse", "--git-common-dir")).resolve():
            raise ConfigError("publication worktree belongs to another repository")
        if git(worktree, "status", "--porcelain=v1", "--untracked-files=all"):
            raise ConfigError("publication worktree is not clean; refusing to discard changes")
        git(worktree, "checkout", "--detach", commit)
    if git(worktree, "rev-parse", "HEAD") != commit:
        raise ConfigError("publication worktree source differs from the tag")
    return worktree


@contextmanager
def exclusive(path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("a") as stream:
        os.chmod(path, 0o600)
        fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
        yield


def read_receipt(path, commit):
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or data.get("commit") != commit:
        raise ConfigError("artifact receipt must identify the exact tag commit")
    assets = data.get("assets")
    if not isinstance(assets, list):
        raise ConfigError("artifact receipt must contain an asset list")
    if not assets and data.get("store_verified") is not True:
        raise ConfigError("an empty asset list requires store_verified: true")
    result = []
    for item in assets:
        if not isinstance(item, dict) or not isinstance(item.get("path"), str):
            raise ConfigError("invalid artifact receipt asset")
        path = Path(item["path"])
        name = item.get("name", path.name)
        if (not path.is_absolute() or not path.is_file() or path.is_symlink()
                or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name)):
            raise ConfigError("artifact must be a regular absolute file with a safe name")
        if "sha256" in item:
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            if item["sha256"] != digest:
                raise ConfigError("artifact digest differs from its receipt")
        result.append({"path": str(path), "name": name})
    return result


# The release index files hubs and install.sh read: uploaded after the downloads they list, latest.json the very last,
# so a hub never reads a manifest whose files are not there yet (GitHub serves an asset as soon as it is uploaded).
# android.json is the Android platform's: the signed APK hubs offer their phones (android_tag_release.py).
INDEX_FILES = ("VERSION", "SHA256SUMS", "android.json", "latest.json")


def host_platform():
    """This machine as a release names its platforms: linux-amd64, darwin-arm64…"""
    machine = platform.machine().lower()
    return platform.system().lower() + "-" + {"x86_64": "amd64", "aarch64": "arm64"}.get(machine, machine)


def scan_gzip(path, token, out=None):
    """The sha256 of what the gzip at path unpacks to, and whether it holds token with no digit right before or
    after it (the build number -X main.build leaves in every binary, whatever its platform). out gets the bytes."""
    pattern = re.compile(rb"(?<![0-9])" + re.escape(token) + rb"(?![0-9])")
    digest, carry, found, first = hashlib.sha256(), b"", False, True
    with gzip.open(path, "rb") as stream:
        while chunk := stream.read(1 << 20):
            digest.update(chunk)
            if out is not None:
                out.write(chunk)
            if not found:
                window = carry + chunk
                # A match at the window's end waits for the byte after it; one at its start was decided before.
                found = any(m.end() < len(window) and (m.start() > 0 or first) for m in pattern.finditer(window))
                carry, first = window[-(len(token) + 1):], first and len(window) <= len(token) + 1
    found = found or any(m.end() == len(carry) and (m.start() > 0 or first) for m in pattern.finditer(carry))
    return digest.hexdigest(), found


def check_release_manifest(assets):
    """A receipt with latest.json (the hub's) is what every release hub installs from once it is uploaded: latest.json
    must describe the downloads uploaded with it, each must be its build, and the personad among them that runs here
    must say its version and build. v0.2.1's latest.json said another build than its binaries, written by a later run
    than theirs, and every hub refused it after downloading (#4522)."""
    by_name = {item["name"]: Path(item["path"]) for item in assets}
    if "latest.json" not in by_name:
        return
    try:
        manifest = json.loads(by_name["latest.json"].read_text())
    except (OSError, ValueError) as error:
        raise ConfigError("latest.json does not read: " + str(error)) from error
    version, build = manifest.get("version"), manifest.get("build")
    if not isinstance(version, str) or not version or not isinstance(build, int) or not re.fullmatch(r"[0-9]{12}", str(build)):
        raise ConfigError("latest.json has no version or build number")
    listed = [("personad", plat, item) for plat, item in (manifest.get("files") or {}).items()]
    for group in ("programs", "nodes"):
        for program, platforms in (manifest.get(group) or {}).items():
            listed += [(program, plat, item) for plat, item in (platforms or {}).items()]
    listed += [("persona-link", plat, item) for plat, item in (manifest.get("links") or {}).items()]
    here, ran = host_platform(), False
    if not any(program == "personad" for program, _, _ in listed):
        raise ConfigError("latest.json lists no personad")
    with tempfile.TemporaryDirectory(prefix="persona-release-check-") as directory:
        for program, plat, item in listed:
            name = item.get("name") if isinstance(item, dict) else None
            path = by_name.get(name)
            if path is None:
                raise ConfigError("latest.json lists %s, which is not in the receipt" % name)
            if path.stat().st_size != item.get("size") or hashlib.sha256(path.read_bytes()).hexdigest() != item.get("sha256"):
                raise ConfigError("%s is not the file latest.json lists (size or sha256)" % name)
            run = Path(directory) / name.removesuffix(".gz") if program == "personad" and plat == here else None
            if run is None:
                inner, stamped = scan_gzip(path, str(build).encode())
            else:
                with run.open("wb") as out:
                    inner, stamped = scan_gzip(path, str(build).encode(), out)
            if inner != item.get("binary_sha256"):
                raise ConfigError("%s does not unpack to the binary latest.json lists" % name)
            if run is None:
                if not stamped:
                    raise ConfigError("%s is not build %d: latest.json was written for another build than it" % (name, build))
                continue
            run.chmod(0o755)
            said = subprocess.run([str(run), "version", "-v"], capture_output=True, text=True, timeout=60).stdout.strip()
            want = "%s\nchannel release, build %d" % (version, build)
            if said != want:
                raise ConfigError("%s says %r, not %r: latest.json was written for another build than it" % (name, said, want))
            ran = True
    if not ran:
        raise ConfigError("latest.json has no personad that runs on this machine (%s) to check it with" % here)


def check_android_index(assets, version):
    """A receipt with android.json (the Android platform's) must ship the APK it names, at its size and sha256, for
    this version: hubs download that APK and offer it to every phone (docs/operations/release.md "Android on version tags")."""
    by_name = {item["name"]: Path(item["path"]) for item in assets}
    if "android.json" not in by_name:
        return
    try:
        index = json.loads(by_name["android.json"].read_text())
    except (OSError, ValueError) as error:
        raise ConfigError("android.json does not read: " + str(error)) from error
    if not isinstance(index, dict) or index.get("version") != version:
        raise ConfigError("android.json is not version " + version)
    code = index.get("version_code")
    if type(code) is not int or not 0 < code <= 2100000000 or not isinstance(index.get("package_name"), str) \
            or not index["package_name"] or not re.fullmatch(r"[0-9a-f]{64}", str(index.get("certificate_sha256"))):
        raise ConfigError("android.json has no valid package, version code or certificate")
    name = index.get("name")
    path = by_name.get(name) if isinstance(name, str) and name.endswith(".apk") else None
    if path is None:
        raise ConfigError("android.json names %s, which is not an APK in the receipt" % name)
    if path.stat().st_size != index.get("size") or hashlib.sha256(path.read_bytes()).hexdigest() != index.get("sha256"):
        raise ConfigError("%s is not the APK android.json describes (size or sha256)" % name)


def verify_remote_assets(repository, tag, assets, listed, gh="gh", missing=False):
    """Verify exact remote bytes, also on gh versions whose release view has no digest field.

    GitHub CLI 2.23.0 omits digest from --json assets. Its REST response has the
    digest; if an older GitHub API omits that too, download the asset instead of
    accepting its name or size as proof.
    """
    actual = {}
    for remote in listed:
        if not isinstance(remote, dict) or not isinstance(remote.get("name"), str):
            raise ConfigError("GitHub returned an invalid Release asset")
        actual[remote["name"]] = remote
    if missing and not actual:
        raise ConfigError("uploaded asset digest verification failed: " + assets[0]["name"])
    fallback = None
    for item in assets:
        path = Path(item["path"])
        digest = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
        remote = actual.get(item["name"])
        if remote is not None and remote.get("digest") is not None:
            if remote["digest"] != digest:
                raise ConfigError("another artifact already owns Release asset name: " + item["name"])
            continue
        if remote is None and not missing:
            # Before uploading, an absent asset is what pending is for. The
            # post-upload view must contain it, through this REST fallback.
            continue
        if fallback is None:
            fallback = {}
            data = json.loads(subprocess.check_output(
                [gh, "api", "repos/" + repository + "/releases/tags/" + tag], text=True, timeout=60))
            if not isinstance(data, dict) or not isinstance(data.get("assets"), list):
                raise ConfigError("GitHub REST returned no Release asset list")
            fallback = {remote.get("name"): remote for remote in data["assets"] if isinstance(remote, dict)}
        remote = fallback.get(item["name"])
        if not isinstance(remote, dict) or remote.get("size") != path.stat().st_size:
            raise ConfigError("uploaded asset digest verification failed: " + item["name"])
        if remote.get("digest") is not None:
            if remote["digest"] != digest:
                raise ConfigError("uploaded asset digest verification failed: " + item["name"])
            continue
        url = remote.get("url")
        prefix = "https://api.github.com/repos/" + repository + "/releases/assets/"
        if not isinstance(url, str) or not url.startswith(prefix) or not url[len(prefix):].isdigit():
            raise ConfigError("GitHub returned an invalid asset download URL: " + item["name"])
        body = subprocess.check_output([gh, "api", "-H", "Accept: application/octet-stream", url], timeout=1800)
        if hashlib.sha256(body).hexdigest() != digest.removeprefix("sha256:"):
            raise ConfigError("uploaded asset digest verification failed: " + item["name"])


def upload(repository, tag, assets, gh="gh"):
    # The workflow owns Release creation; never create a tag from this machine.
    release = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "tagName,isDraft,assets"],
        text=True, timeout=60))
    if release.get("tagName") != tag or release.get("isDraft") is not False:
        raise ConfigError("published GitHub Release for the exact tag is required")
    if not assets:
        print("Store publication verified; this platform has no Release assets", flush=True)
        return
    if len({item["name"] for item in assets}) != len(assets):
        raise ConfigError("duplicate Release asset name in receipt")
    verify_remote_assets(repository, tag, assets, release["assets"], gh)
    pending = [item for item in assets if item["name"] not in {remote["name"] for remote in release["assets"]}]
    if not pending:
        print("Release assets already have the verified digests", flush=True)
        return
    with tempfile.TemporaryDirectory(prefix="persona-release-assets-") as directory:
        staging = Path(directory)
        for item in pending:
            destination = staging / item["name"]
            # The # suffix in gh is a display label, not an uploaded filename.
            # A hard link preserves exact bytes while supplying the actual name.
            try:
                os.link(item["path"], destination)
            except OSError:
                import shutil
                shutil.copyfile(item["path"], destination)
        # The downloads in one upload (gh sends them side by side), then each index file alone, in INDEX_FILES order.
        names = [item["name"] for item in pending]
        batches = [[n for n in names if n not in INDEX_FILES]] + [[n] for n in INDEX_FILES if n in names]
        for batch in (b for b in batches if b):
            subprocess.run([gh, "release", "upload", tag, "--repo", repository,
                            *(str(staging / name) for name in batch)], check=True, timeout=1800)
    remote = json.loads(subprocess.check_output(
        [gh, "release", "view", tag, "--repo", repository, "--json", "assets"], text=True, timeout=60))
    # gh returns {"assets": [...]}; older callers may return the bare list.
    listed = remote if isinstance(remote, list) else remote.get("assets") if isinstance(remote, dict) else None
    if not isinstance(listed, list) or not all(isinstance(item, dict) and isinstance(item.get("name"), str) for item in listed):
        raise ConfigError("gh release view returned no asset list")
    verify_remote_assets(repository, tag, assets, listed, gh, missing=True)


def write_state(path, state):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(state, indent=2, sort_keys=True) + "\n")
    temporary.chmod(0o600)
    temporary.replace(path)


def publish_once(config, tag, commit):
    platform = config["platform"]
    state = config["state_dir"] / "state.json"
    if state.exists():
        previous = json.loads(state.read_text())
        if previous.get("tag") == tag and previous.get("commit") == commit and previous.get("phase") == "complete":
            print("Already published " + platform + " " + tag, flush=True)
            return 0
    prepare_worktree(config, commit)
    fields = {key: str(config[key]) for key in ("repo", "worktree", "state_dir")}
    fields.update(tag=tag, commit=commit, version=tag[1:])
    write_state(state, {"tag": tag, "commit": commit, "platform": platform, "phase": "publishing"})
    command = [item.format(**fields) for item in config["command"]]
    env = dict(os.environ, PERSONA_RELEASE_TAG=tag, PERSONA_RELEASE_COMMIT=commit,
               PERSONA_RELEASE_VERSION=tag[1:], PERSONA_RELEASE_REPOSITORY=config["repository"],
               PERSONA_RELEASE_PLATFORM=platform, PERSONA_RELEASE_WORKTREE=str(config["worktree"]))
    subprocess.run(command, cwd=config["worktree"], env=env, check=True)
    assets = read_receipt(Path(config["receipt"]).expanduser(), commit)
    check_release_manifest(assets)
    check_android_index(assets, tag[1:])
    upload(config["repository"], tag, assets)
    write_state(state, {"tag": tag, "commit": commit, "platform": platform, "phase": "complete", "assets": assets})
    print("Published " + platform + " " + tag, flush=True)
    return 0


def invoke_agent(config, tag, commit):
    if "agent" not in config:
        return publish_once(config, tag, commit)
    # Prepare the source before the Agent starts, and retain this process's lock
    # across both its decision and the deterministic publication command.
    prepare_worktree(config, commit)
    fields = {key: str(config[key]) for key in ("repo", "worktree", "state_dir")}
    log = config["state_dir"] / "agent.log"
    log.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    prompt = Path(__file__).with_name("tag_release_prompt.md")
    fields.update(tag=tag, commit=commit, version=tag[1:], prompt_file=str(prompt), log_file=str(log))
    command = [item.format(**fields) for item in config["agent"]]
    environment = dict(os.environ, PERSONA_RELEASE_TAG=tag, PERSONA_RELEASE_COMMIT=commit,
                       PERSONA_RELEASE_VERSION=tag[1:], PERSONA_RELEASE_REPOSITORY=config["repository"],
                       PERSONA_RELEASE_PLATFORM=config["platform"], PERSONA_RELEASE_WORKTREE=str(config["worktree"]),
                       PERSONA_RELEASE_COMMAND=json.dumps(config["command"]),
                       PERSONA_RELEASE_RECEIPT=config.get("receipt", ""))
    with log.open("a") as output:
        subprocess.run(command, cwd=config["repo"], env=environment, stdin=subprocess.DEVNULL,
                       stdout=output, stderr=subprocess.STDOUT, check=True)
    # The command runs under the same lock and source identity even when an Agent
    # chooses the concrete platform commands. It never receives credentials here.
    return publish_once(config, tag, commit)


def tick(config):
    for tag in latest_tags(config["repo"], config["repository"]):
        commit = resolve_tag(config["repo"], config["repository"], tag)
        with exclusive(config["state_dir"] / "release.lock"):
            state = config["state_dir"] / "state.json"
            if state.exists():
                previous = json.loads(state.read_text())
                if previous.get("tag") == tag and previous.get("commit") == commit and previous.get("phase") == "complete":
                    continue
            invoke_agent(config, tag, commit)


def latest_tags(repo, repository):
    output = git(repo, "ls-remote", "--tags", "--refs", "https://github.com/" + repository + ".git", "refs/tags/v*")
    result = set()
    for row in output.splitlines():
        object_id, ref = row.split("\t", 1)
        if COMMIT.fullmatch(object_id) and TAG.fullmatch(ref.removeprefix("refs/tags/")):
            result.add(ref.removeprefix("refs/tags/"))
    return sorted(result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--tag", help="publish exactly this existing remote version tag")
    parser.add_argument("--repository", help="print the Claude-loop prompt for this owner/name repository")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--once", action="store_true", help="process all uncompleted remote version tags once")
    mode.add_argument("--watch", action="store_true", help="poll for version tags")
    parser.add_argument("--interval", type=int, default=60)
    args = parser.parse_args()
    if args.repository is not None:
        if not REPOSITORY.fullmatch(args.repository) or args.tag:
            parser.error("--repository requires the default --once or --watch mode")
        prompt_path = Path(__file__).with_name("tag_release_prompt.md")
        prompt = prompt_path.read_text().replace("<owner>/<name>", args.repository)
        print(prompt, end="")
        return 0
    config = load_config(args.config)
    if args.interval < 1:
        parser.error("interval must be positive")
    if args.tag:
        with exclusive(config["state_dir"] / "release.lock"):
            invoke_agent(config, args.tag, resolve_tag(config["repo"], config["repository"], args.tag))
        return 0
    if not args.once and not args.watch:
        parser.error("choose --tag, --once, or --watch")
    while True:
        tick(config)
        if not args.watch:
            return 0
        time.sleep(args.interval)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ConfigError, OSError, ValueError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print("Tag publication failed: " + str(error), file=sys.stderr, flush=True)
        sys.exit(1)
