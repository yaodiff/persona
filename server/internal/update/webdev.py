"""Private helper for the existing Hub updater. No publication or service installation.

Requests arrive on stdin; credentials and command output never go into the receipt.
Run `python3 webdev.py verify BACKUP` to verify a retained recovery point offline.
"""
import contextlib
import ctypes
import ctypes.util
import datetime
import errno
import fcntl
import gzip
import hashlib
import json
import os
import posixpath
from pathlib import Path, PurePosixPath
import re
import ntpath
import shutil
import shlex
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import zlib
from urllib.parse import urlsplit, urlunsplit

OWNER = "persona-web-development-backup-v1"
REPOSITORY = "https://github.com/nwverse/persona.git"
NAME = re.compile(r"snapshot-[0-9]{20}-[a-f0-9]{12}")
PLATFORMS = {"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64"}
# Public Go target components are the only values eligible for progress labels.
GO_TARGET = re.compile(
    r"(aix|android|darwin|dragonfly|freebsd|illumos|ios|js|linux|netbsd|openbsd|plan9|solaris|wasip1|windows)-"
    r"(386|amd64|arm|arm64|loong64|mips|mipsle|mips64|mips64le|ppc64|ppc64le|riscv64|s390x|wasm)"
)
# Automatic adoption must prove ancestry within this bounded history window.
# Older/divergent installations can still explicitly choose a manual update.
HISTORY_LIMIT = 1024
PROGRESS_INTERVAL = 0.75


class Progress:
    def __init__(self, phase, send=None):
        self.phase = phase
        self.steps = {
            "building": ("fetch", "npm_install", "web_build", "runtime_packages", "go_build", "manifest"),
            "backing_up": ("preflight", "quiesce", "database_dump", "archive_files",
                           "resume_writers", "archive_programs", "verify_snapshot"),
            "applying_backup": ("verify_snapshot", "prepare_artifacts"),
        }[phase]
        self.send = send
        self.started = time.monotonic()

    def step(self, name, **fields):
        self.fields = {"phase": self.phase, "step": name, "step_index": self.steps.index(name) + 1,
                       "step_total": len(self.steps), **fields}
        self.last_at = time.monotonic()
        self.last_bytes = fields.get("bytes_done")
        self.rate = None
        self.emit(force=True)

    def update(self, *, force=False, **fields):
        self.fields.update(fields)
        self.emit(force=force)

    def add_bytes(self, size):
        self.update(bytes_done=self.fields["bytes_done"] + size)

    def emit(self, *, force=False):
        if self.send is None:
            return
        now = time.monotonic()
        interval = now - self.last_at
        if interval < PROGRESS_INTERVAL and not force:
            return
        event = dict(self.fields, elapsed_seconds=int(now - self.started))
        written = event.get("bytes_done")
        if written is not None:
            if self.last_bytes is not None and interval >= PROGRESS_INTERVAL:
                sample = max(0, written - self.last_bytes) / interval
                self.rate = sample if self.rate is None else 0.65 * self.rate + 0.35 * sample
            self.last_bytes = written
        if self.rate is not None:
            event["bytes_per_second"] = round(self.rate)
        if event.get("bytes_total"):
            event["percent"] = min(100, 100 * written // event["bytes_total"])
        elif event.get("item_total") and ("bytes_total" in event or event["step"] == "go_build"):
            event["percent"] = 100 * event["item_done"] // event["item_total"]
        self.last_at = now
        self.send(event)


def progress_writer(fd):
    # The pipe is exclusively for short JSON messages assembled by Progress.
    # A slow or gone reader must never block the helper or its cleanup.
    os.set_inheritable(fd, False)
    os.set_blocking(fd, False)

    def send(event):
        try:
            os.write(fd, json.dumps(event, separators=(",", ":")).encode("ascii") + b"\n")
        except (BlockingIOError, BrokenPipeError):
            pass

    return send


@contextlib.contextmanager
def defer_cancellation():
    # Cleanup and resume have their own bounded deadlines. A second cancellation
    # must not interrupt them and leave a stopped writer or orphaned child.
    pending = []
    previous = {s: signal.getsignal(s) for s in (signal.SIGINT, signal.SIGTERM)}
    try:
        for s in previous:
            signal.signal(s, lambda signum, _: pending.append(signum))
        yield pending
    finally:
        for s, handler in previous.items():
            signal.signal(s, handler)


def interrupt(*_):
    # The first signal unwinds into cleanup; later signals cannot abort it.
    for s in (signal.SIGINT, signal.SIGTERM):
        signal.signal(s, signal.SIG_IGN)
    raise InterruptedError()


def run(argv, cwd=None, env=None, output=False, timeout=1800, on_tick=None):
    # Do not echo argv: Git credentials, database URLs and hooks can be private.
    process = None
    with contextlib.ExitStack() as stack:
        try:
            # A cancellation during Popen must reach the cleanup below once the
            # child group exists and its leader is available to signal and reap.
            with defer_cancellation() as pending:
                process = stack.enter_context(subprocess.Popen(
                    argv, cwd=cwd, env=env, start_new_session=True,
                    stdout=subprocess.PIPE if output else None, text=output))
            if pending:
                signal.raise_signal(pending[0])
            deadline = time.monotonic() + timeout
            while True:
                remaining = max(0, deadline - time.monotonic())
                try:
                    stdout, _ = process.communicate(timeout=min(remaining, PROGRESS_INTERVAL) if on_tick else remaining)
                    break
                except subprocess.TimeoutExpired:
                    if on_tick is None or time.monotonic() >= deadline:
                        raise
                    on_tick()
        except BaseException:
            if process is None:
                raise
            with defer_cancellation():
                # Signal the whole child group, first allowing hook cleanup.
                # Timeout and cancellation share the same bounded escalation.
                for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGKILL):
                    deadline = time.monotonic() + 2
                    try:
                        os.killpg(process.pid, sig)
                    except ProcessLookupError:
                        pass
                    try:
                        process.communicate(timeout=2)
                    except subprocess.TimeoutExpired:
                        if sig == signal.SIGKILL:
                            raise
                        continue
                    # Reaping the leader does not mean its group is finished:
                    # descendants may still be completing their signal hooks.
                    try:
                        os.killpg(process.pid, 0)
                    except ProcessLookupError:
                        break
                    time.sleep(max(0, deadline - time.monotonic()))
            raise
        if process.returncode:
            raise subprocess.CalledProcessError(process.returncode, argv)
        return stdout


def digest(path, on_read=None):
    with path.open("rb") as stream:
        return stream_digest(stream, on_read)


def stream_digest(stream, on_read=None):
    h = hashlib.sha256()
    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
        h.update(chunk)
        if on_read is not None:
            on_read(len(chunk))
    return h.hexdigest()


def write_json(path, value):
    with path.open("x", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def lock_backup(lock):
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as exc:
        raise BackupValidationError("busy", "another backup or prepare is in progress") from exc


def failure_detail(exc):
    # Do not stringify subprocess errors: their argv can contain credentials.
    if isinstance(exc, ValueError):
        return str(exc)
    if isinstance(exc, subprocess.CalledProcessError):
        return f"CalledProcessError (exit {exc.returncode})"
    return type(exc).__name__


class BackupValidationError(ValueError):
    def __init__(self, reason, message):
        super().__init__(message)
        self.reason = reason


def backup_failure_reason(exc):
    if isinstance(exc, BackupValidationError):
        return exc.reason
    if isinstance(exc, InterruptedError):
        return "interrupted"
    if isinstance(exc, PermissionError):
        return "permission"
    if isinstance(exc, FileNotFoundError):
        return "missing"
    if isinstance(exc, subprocess.TimeoutExpired):
        return "timeout"
    if isinstance(exc, subprocess.CalledProcessError):
        return "command"
    if isinstance(exc, OSError):
        return "storage"
    return "validation"


def resolve(request):
    refs = run(["git", "ls-remote", REPOSITORY, "refs/heads/main"], output=True)
    match = re.fullmatch(r"([a-f0-9]{40})\trefs/heads/main\n?", refs)
    if not match:
        raise ValueError("project main branch did not return a full commit")
    source = Path(request["work"]) / "metadata"
    source.mkdir()
    run(["git", "init", "--quiet", str(source)])
    run(["git", "-C", str(source), "remote", "add", "origin", REPOSITORY])
    # Read the committer date of the captured object, not a later branch head or check time.
    run(["git", "-C", str(source), "fetch", "--depth=1", "--filter=blob:none", "--no-tags", "origin", match[1]])
    updated_at = run(["git", "-C", str(source), "show", "-s", "--format=%cI", match[1]], output=True).strip()
    return {"branch": "main", "commit": match[1], "updated_at": updated_at}


def binary_name(program, platform):
    suffix = ".exe" if platform.startswith("windows-") else ""
    return program + "-" + platform + suffix


def build(request, progress=None):
    progress = progress or Progress("building")
    progress.step("fetch")
    selected = request["candidate"]
    if not re.fullmatch(r"[a-f0-9]{40}", selected["commit"]):
        raise ValueError("development build requires a full commit")
    root = Path(request["work"])
    source, artifacts = root / "source", root / "artifacts"
    source.mkdir()
    artifacts.mkdir()
    run(["git", "init", "--quiet", str(source)])
    run(["git", "-C", str(source), "remote", "add", "origin", REPOSITORY])
    # Fetch the captured object, never a moving branch head; detached checkout only.
    run(["git", "-C", str(source), "fetch", f"--depth={HISTORY_LIMIT}", "--filter=blob:none", "--no-tags", "origin", selected["commit"]])
    run(["git", "-C", str(source), "checkout", "--detach", selected["commit"]])
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0", CGO_ENABLED="0")
    web, server = source / "web", source / "server"
    progress.step("npm_install")
    run(["npm", "ci", "--no-audit", "--no-fund"], web, env)
    progress.step("web_build")
    run(["npm", "run", "build:development"], web, env)
    shutil.copytree(web / "dist", server / "internal/hub/webdist", dirs_exist_ok=True)
    progress.step("runtime_packages")
    run(["go", "run", "./internal/webasset/precompress", "internal/hub/webdist"], server, env)
    # Runtime assets are build inputs, not a test or release pipeline.
    run(["go", "generate", "./internal/runtimepkg"], server, env)
    flags = (f'-s -w -X main.version={request["version"]} -X main.channel=release '
             f'-X main.build={request["build"]} -X main.commit={selected["commit"]}')
    if request.get("release_url"):
        flags += " -X " + shlex.quote("main.updateURL=" + request["release_url"])
    tags = (server / "build-tags.txt").read_text().strip()
    manifest = {"commit": selected["commit"], "version": request["version"], "build": request["build"],
                "files": {}, "programs": {}, "nodes": {"persona-node": {}}}
    progress.step("go_build")
    # Only targets from the Go toolchain may become paths, env values or labels.
    if request["platform"] not in PLATFORMS:
        raise ValueError("unsupported Hub platform")
    dist = run(["go", "tool", "dist", "list"], server, env, output=True)
    available = {p.replace("/", "-") for p in dist.splitlines() if re.fullmatch(r"[a-z0-9]+/[a-z0-9]+", p)}
    targets = []
    for platform in sorted((set(request["platforms"]) | {request["platform"]}) & available):
        programs = ["personad", "persona-node", "persona-manager"]
        if platform == request["platform"]:
            programs.append("persona-hub")
        targets.extend((program, platform) for program in programs)
    for i, (program, platform) in enumerate(targets):
        goos, arch = platform.split("-")
        label = f"{program} ({platform})" if GO_TARGET.fullmatch(platform) else ""
        progress.update(force=i == 0, item_done=i, item_total=len(targets), item_label=label)
        binary = root / binary_name(program, platform)
        try:
            run(["go", "build", "-tags", tags, "-trimpath", "-ldflags", flags,
                 "-o", str(binary), "./cmd/" + program], server, dict(env, GOOS=goos, GOARCH=arch),
                on_tick=progress.emit if progress.send is not None else None)
        except subprocess.CalledProcessError:
            if platform in PLATFORMS:
                raise
            # Some third-party dependencies support fewer ports than Go. An
            # unbuilt optional program stays absent from the published manifest.
            print(f"development build: {program} unavailable for {platform}", flush=True)
            continue
        archive = artifacts / (binary.name + ".gz")
        with binary.open("rb") as src, gzip.open(archive, "wb") as dst:
            shutil.copyfileobj(src, dst)
        entry = {"name": archive.name, "size": archive.stat().st_size,
                 "sha256": digest(archive), "binary_sha256": digest(binary)}
        if program == "personad":
            manifest["files"][platform] = entry
        elif program == "persona-node":
            manifest["nodes"][program][platform] = entry
        else:
            manifest["programs"].setdefault(program, {})[platform] = entry
    if targets:
        progress.update(force=True, item_done=len(targets), item_label="")
    progress.step("manifest")
    write_json(artifacts / "manifest.json", manifest)
    ancestors = run(["git", "rev-list", f"--max-count={HISTORY_LIMIT}", selected["commit"]], source, output=True).splitlines()
    return {"ancestors": ancestors, "artifacts": str(artifacts)}


def native_digest(path):
    try:
        native = hashlib.sha256()
        with gzip.open(path, "rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                native.update(chunk)
        return native.hexdigest()
    except (OSError, EOFError, zlib.error) as exc:
        raise ValueError("invalid recovery program archive") from exc


def absolute_metadata_path(value):
    return (isinstance(value, str) and value.startswith("/") and
            "\x00" not in value and ".." not in PurePosixPath(value).parts)


def read_manifest(path):
    if path.is_symlink() or not path.is_dir() or not NAME.fullmatch(path.name):
        raise ValueError("not an owned snapshot directory")
    file = path / "manifest.json"
    if file.is_symlink():
        raise ValueError("linked snapshot manifest")
    m = json.loads(file.read_text())
    keys = {"owner", "id", "commit", "from_version", "from_commit", "from_build", "from_updated_at",
            "created_at", "platform", "roots", "databases", "programs", "nodes", "files"}
    if not isinstance(m, dict) or set(m) != keys or m["owner"] != OWNER or m["id"] != path.name:
        raise ValueError("invalid snapshot recovery schema")
    for key in ("commit", "from_commit"):
        value = m[key]
        if not isinstance(value, str) or ((value or key == "commit") and not re.fullmatch(r"[a-f0-9]{40}", value)):
            raise ValueError("invalid snapshot commit")
    if (not isinstance(m["from_version"], str) or any(c in m["from_version"] for c in "\r\n\x00") or
            type(m["from_build"]) is not int or m["from_build"] < 0 or
            not isinstance(m["platform"], str) or m["platform"] not in PLATFORMS | {""}):
        raise ValueError("invalid saved version metadata")
    for key in ("created_at", "from_updated_at"):
        value = m[key]
        if not isinstance(value, str) or (not value and key == "created_at"):
            raise ValueError("invalid snapshot timestamp")
        if value:
            try:
                # Python 3.9 needs an explicit offset for the Hub's UTC timestamps.
                timestamp = value[:-1] + "+00:00" if value.endswith("Z") else value
                if "Z" in timestamp or datetime.datetime.fromisoformat(timestamp).tzinfo is None:
                    raise ValueError("timestamp needs an offset")
            except ValueError as exc:
                raise ValueError("invalid snapshot timestamp") from exc
    if (not isinstance(m["roots"], list) or not m["roots"] or
            not all(absolute_metadata_path(v) for v in m["roots"]) or
            len(set(m["roots"])) != len(m["roots"]) or
            type(m["databases"]) is not int or m["databases"] < 1):
        raise ValueError("invalid snapshot inventory")
    files = m["files"]
    if not isinstance(files, dict) or not all(isinstance(k, str) and isinstance(v, str) and
            re.fullmatch(r"[a-f0-9]{64}", v) for k, v in files.items()):
        raise ValueError("invalid snapshot file checksums")
    if m["databases"] > len(files):
        raise ValueError("snapshot database count exceeds its inventory")
    expected = {"files.tar.gz", "effective-databases.json"} | {f"database-{i}.dump" for i in range(m["databases"])}
    for field, prefix in (("programs", "program-"), ("nodes", "node-")):
        entries = m[field]
        if not isinstance(entries, dict):
            raise ValueError("invalid recovery program inventory")
        if field == "programs" and ("personad" not in entries or not set(entries) <= {"personad", "persona-hub", "persona-manager"}):
            raise ValueError("invalid recovery program inventory")
        for name, info in entries.items():
            if field == "nodes" and not re.fullmatch(r"[a-z0-9]+-[a-z0-9]+", name):
                raise ValueError("invalid recovery node platform")
            artifact = prefix + name + ".gz"
            fields = {"file", "sha256", "target"} if field == "programs" else {"file", "sha256"}
            if (not isinstance(info, dict) or set(info) != fields or info["file"] != artifact or
                    not isinstance(info["sha256"], str) or not re.fullmatch(r"[a-f0-9]{64}", info["sha256"]) or
                    (field == "programs" and not absolute_metadata_path(info["target"]))):
                raise ValueError("invalid recovery program metadata")
            expected.add(artifact)
    if set(files) != expected:
        raise ValueError("snapshot artifacts do not match their recovery inventory")
    return m


def archive_path(name, member=None):
    if (not isinstance(name, str) or not name or "\\" in name or "\x00" in name or
            name.startswith("/") or ntpath.splitdrive(name)[0]):
        raise ValueError(f"{member or name!r}: unsafe history archive path")
    parts = name.split("/")
    if any(p in ("", ".", "..") or ":" in p for p in parts):
        raise ValueError(f"{member or name!r}: noncanonical history archive path")
    return PurePosixPath(name)


def symlink_target(member):
    # A sibling subtree needs leading ".." components. Only accept the canonical
    # relative spelling, so no component traversed and then cancelled can be a link.
    parent = posixpath.dirname(member.name)
    target = posixpath.normpath(posixpath.join(parent, member.linkname))
    path = archive_path(target, member.name)
    if (len(path.parts) < 2 or path.parts[0] != member.name.split("/")[0] or
            posixpath.relpath(target, parent) != member.linkname):
        raise BackupValidationError("archive_link", f"{member.name!r}: symlink target must stay within its history root")
    return target


def validate_hardlink(member, by_name):
    target = by_name.get(str(archive_path(member.linkname, member.name)))
    if target is None or not target.isfile():
        raise BackupValidationError("archive_link", f"{member.name!r}: hard link target is not an archived regular file")


def history_members(archive, roots):
    members = archive.getmembers()
    by_name = {}
    root_names = {f"root-{i}" for i in range(len(roots))}
    for member in members:
        path = archive_path(member.name)
        if (path.parts[0] not in root_names or
                not (member.isfile() or member.isdir() or member.issym() or member.islnk()) or member.name in by_name):
            raise ValueError(f"{member.name!r}: invalid or duplicate history archive member")
        by_name[member.name] = member
    for name, member in by_name.items():
        path = PurePosixPath(name)
        for parent in path.parents:
            if str(parent) != "." and (str(parent) not in by_name or not by_name[str(parent)].isdir()):
                raise ValueError(f"{name!r}: history archive parent is not a directory")
        if member.islnk():
            validate_hardlink(member, by_name)
        if member.issym():
            target = by_name.get(symlink_target(member))
            if target is None or not (target.isfile() or target.isdir() or target.islnk()):
                raise BackupValidationError("archive_link", f"{name!r}: symlink target is not an archived file or directory")
    for name in root_names:
        if name not in by_name or not by_name[name].isdir():
            raise ValueError(f"{name!r}: history archive is missing an inventory root")
    return members


def verify(path, progress=None):
    manifest = read_manifest(path)
    files = manifest["files"]
    if {p.name for p in path.iterdir()} != set(files) | {"manifest.json"}:
        raise ValueError("snapshot contains unowned entries")
    items = [(path / name, expected) for name, expected in files.items()]
    if any(item.is_symlink() or not item.is_file() for item, _ in items):
        raise ValueError("snapshot checksum mismatch")
    if progress is not None:
        progress.update(force=True, bytes_done=0, bytes_total=sum(item.stat().st_size for item, _ in items),
                        item_done=0, item_total=len(items))
    for i, (item, expected) in enumerate(items, 1):
        if digest(item, progress.add_bytes if progress is not None else None) != expected:
            raise ValueError("snapshot checksum mismatch")
        if progress is not None:
            progress.update(item_done=i)
    for program in list(manifest["programs"].values()) + list(manifest["nodes"].values()):
        if native_digest(path / program["file"]) != program["sha256"]:
            raise ValueError("recovery program checksum mismatch")
    try:
        with tarfile.open(path / "files.tar.gz", "r:gz") as archive:
            history_members(archive, manifest["roots"])
    except (tarfile.TarError, OSError, EOFError, zlib.error) as exc:
        raise ValueError("invalid history archive") from exc
    return manifest


def extract_history(path, destination):
    # Operator-only, offline extraction. Apply backup never calls this path.
    manifest = verify(path)
    if destination.is_symlink() or (destination.exists() and (not destination.is_dir() or any(destination.iterdir()))):
        raise ValueError("recovery destination must be an empty directory")
    with tarfile.open(path / "files.tar.gz", "r:gz") as archive:
        members = history_members(archive, manifest["roots"])
        destination.mkdir(mode=0o700, exist_ok=True)
        destination.chmod(0o700)
        # All supported Pythons use the same restricted writer. Do not delegate
        # paths, links, ownership or special files to tarfile's extraction API.
        directories = sorted((m for m in members if m.isdir()),
                             key=lambda m: len(PurePosixPath(m.name).parts))
        for member in directories:
            target = destination / member.name
            target.mkdir(mode=0o700)
            target.chmod(0o700)
        for member in members:
            if not member.isfile():
                continue
            target = destination / member.name
            # Exclusive creation also refuses a colliding name or existing link.
            with archive.extractfile(member) as source, target.open("xb") as output:
                os.fchmod(output.fileno(), 0o600)
                shutil.copyfileobj(source, output)
                mode = member.mode & 0o755
                if not mode & 0o100:
                    mode &= ~0o111
                os.fchmod(output.fileno(), mode | 0o600)
            os.utime(target, (member.mtime, member.mtime))
        # No more file writes through archive paths once links exist. Validation
        # requires each target to be a direct, archived regular file/directory.
        for member in members:
            if member.islnk():
                os.link(destination / member.linkname, destination / member.name)
            elif member.issym():
                (destination / member.name).symlink_to(member.linkname)
        for member in reversed(directories):
            os.utime(destination / member.name, (member.mtime, member.mtime))


def has_new_backslash_component(path):
    current = path
    while True:
        try:
            os.lstat(current)
            return False
        except FileNotFoundError:
            if "\\" in current.name:
                return True
            parent = current.parent
            if parent == current:
                return False
            current = parent
        except OSError:
            return False


def backup_store(request):
    # The Web UI's folder (already canonical) or hub.json's advanced setting;
    # absent: the recommended directory the Hub has always used.
    directory = request["config"]["backup"].get("directory")
    if directory:
        if (not isinstance(directory, str) or not directory.startswith("/") or
                "\x00" in directory or ".." in PurePosixPath(directory).parts):
            raise BackupValidationError("directory", "invalid backup directory")
        if "\\" in directory and (posixpath.normpath(directory) != directory or
                                  has_new_backslash_component(Path(directory))):
            raise BackupValidationError("directory", "invalid backup directory")
        store = Path(directory)
    else:
        store = Path(request["runtime"]).resolve(strict=True) / "update/development-backups"
    if store.is_symlink():
        raise BackupValidationError("directory", "linked backup directory")
    return store


def backup_version(path):
    # Schema validation is shared; payload checks still happen before use.
    m = read_manifest(path)
    if not m["from_commit"] or not m["from_version"] or not m["from_build"] or not m["platform"]:
        raise ValueError("snapshot has no installable program version")
    return {"id": path.name, "version": m["from_version"], "build": m["from_build"],
            "commit": m["from_commit"], "updated_at": m.get("from_updated_at", ""),
            "created_at": m["created_at"]}


def backups(request):
    store = backup_store(request)
    versions = []
    if store.exists():
        for path in sorted(store.iterdir(), reverse=True):
            try:
                versions.append(backup_version(path))
            except (ValueError, OSError, KeyError, json.JSONDecodeError):
                pass
    return {"backups": versions}


def prepare_backup(request, progress=None):
    progress = progress or Progress("applying_backup")
    progress.step("verify_snapshot")
    ident = request["backup_id"]
    if not isinstance(ident, str) or not NAME.fullmatch(ident):
        raise ValueError("invalid snapshot ID")
    store = backup_store(request)
    with (store / ".lock").open("a") as lock:
        lock_backup(lock)
        path = store / ident
        version = backup_version(path)
        m = verify(path, progress)
        if m["platform"] != request["platform"]:
            raise ValueError("snapshot belongs to a different Hub platform")
        if request.get("candidate") and (version["commit"] != request["candidate"]["commit"] or
                version["version"] != request["version"] or version["build"] != request["build"]):
            raise ValueError("snapshot version changed")
        packages = {"personad-" + p: v for p, v in m.get("nodes", {}).items()}
        packages.update({name + "-" + m["platform"]: v for name, v in m["programs"].items()
                         if name in ("personad", "persona-hub", "persona-manager")})
        if any("personad-" + p not in packages for p in request.get("platforms", [])):
            raise ValueError("snapshot is missing a required Node platform")
        progress.step("prepare_artifacts")
        artifacts = Path(request["work"]) / "artifacts"
        artifacts.mkdir()
        # Copy only programs, never database dumps, configuration or history.
        # These private copies also survive rotation of the selected snapshot.
        progress.update(force=True, bytes_done=0, bytes_total=sum((path / v["file"]).stat().st_size for v in packages.values()),
                        item_done=0, item_total=len(packages))
        for i, (name, info) in enumerate(packages.items(), 1):
            target = artifacts / (name + (".exe.gz" if "-windows-" in name else ".gz"))
            with (path / info["file"]).open("rb") as src, target.open("wb") as dst:
                for chunk in iter(lambda: src.read(1024 * 1024), b""):
                    dst.write(chunk)
                    progress.add_bytes(len(chunk))
            if digest(target) != m["files"][info["file"]]:
                raise ValueError("staged program checksum mismatch")
            if native_digest(target) != info["sha256"]:
                raise ValueError("staged native program checksum mismatch")
            progress.update(item_done=i)
        progress.emit(force=True)
        return {"artifacts": str(artifacts)}


def inventory(request):
    runtime = Path(request["runtime"]).resolve(strict=True)
    hub = json.loads((runtime / "hub.json").read_text())
    databases = [request.get("hub_database") or hub["database_url"]]
    roots = [runtime]
    kb_roots = set()
    assistant_configured_kb = False
    # Include every local manager, not just the built-in one. Missing/malformed
    # configuration is an error rather than silently omitting its history.
    manager_root = runtime / "managers"
    if manager_root.exists():
        for file in sorted(manager_root.glob("*/manager.json")):
            manager = json.loads(file.read_text())
            database = manager.get("database_url")
            if file.parent.name == "assistant":
                if not manager.get("moved_to_own_database", {}).get("at"):
                    database = None  # Its live tables are still in the Hub database.
                elif manager.get("database_name"):
                    # Match provisionOwn: a named database on the effective Hub
                    # server takes precedence over a previously saved URL.
                    url = urlsplit(databases[0])
                    database = urlunsplit(url._replace(path="/" + manager["database_name"]))
            if database:
                databases.append(database)
            kb = manager.get("kb_dir")
            if kb:
                kb = Path(kb).expanduser()
                resolved_kb = (file.parent / kb).resolve(strict=True)
                roots.append(resolved_kb)
                kb_roots.add(resolved_kb)
                if file.parent.name == "assistant":
                    assistant_configured_kb = True
            elif file.parent.name != "assistant":
                kb_roots.add(file.parent / "data")
    if not assistant_configured_kb:
        kb_roots.add(runtime / "data")
    backup = request["config"]["backup"]
    roots += [Path(p).expanduser().resolve(strict=True) for p in backup.get("extra_roots", [])]
    databases += backup.get("extra_databases", [])
    previous_web = runtime / "web.prev"
    excluded = {runtime / n for n in ("update", "bin", "cache", "logs")}
    # An explicitly inventoried knowledge directory or extra root can live
    # inside the old bundle. Preserve it before collapsing contained roots.
    if (previous_web.is_dir() and not previous_web.is_symlink() and
            not any(p.is_relative_to(previous_web) for p in roots) and
            not (runtime / "web").resolve().is_relative_to(previous_web)):
        excluded.add(previous_web)
    # Contained roots are already archived, and duplicate databases are dumped once.
    roots = sorted(set(roots), key=lambda p: (len(p.parts), str(p)))
    roots = [p for i, p in enumerate(roots) if not any(p.is_relative_to(q) for q in roots[:i])]
    return runtime, roots, list(dict.fromkeys(databases)), excluded, sorted(kb_roots)


class ConninfoOption(ctypes.Structure):
    _fields_ = [(name, ctypes.c_char_p) for name in ("keyword", "envvar", "compiled", "val", "label", "dispchar")] + [("dispsize", ctypes.c_int)]


def database_environment(database):
    if not isinstance(database, str) or not database or "\x00" in database:
        raise BackupValidationError("database", "invalid database connection")
    env = dict(os.environ)
    if "=" not in database and not database.startswith(("postgres://", "postgresql://")):
        env["PGDATABASE"] = database
        return env
    # Use the same libpq parser as PostgreSQL tools, including escaped passwords,
    # query options and keyword strings. Never put the connection string in argv.
    library = ctypes.util.find_library("pq")
    if not library:
        raise BackupValidationError("missing", "development backups require libpq beside PostgreSQL tools")
    pq = ctypes.CDLL(library)
    pq.PQconninfoParse.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_void_p)]
    pq.PQconninfoParse.restype = ctypes.POINTER(ConninfoOption)
    pq.PQconninfoFree.argtypes = [ctypes.POINTER(ConninfoOption)]
    pq.PQconninfoFree.restype = None
    pq.PQfreemem.argtypes = [ctypes.c_void_p]
    pq.PQfreemem.restype = None
    error = ctypes.c_void_p()
    try:
        encoded = database.encode()
    except UnicodeError as exc:
        raise BackupValidationError("database", "invalid database connection encoding") from exc
    options = pq.PQconninfoParse(encoded, ctypes.byref(error))
    if not options:
        if error.value:
            pq.PQfreemem(error)
        raise BackupValidationError("database", "invalid database connection parameters")
    env.pop("PGDATABASE", None)
    try:
        i = 0
        while options[i].keyword:
            option = options[i]
            if option.val is not None:
                if not option.envvar:
                    raise BackupValidationError("database", "database parameter has no safe environment binding")
                env[option.envvar.decode()] = option.val.decode()
            i += 1
    except UnicodeError as exc:
        raise BackupValidationError("database", "invalid database connection encoding") from exc
    finally:
        pq.PQconninfoFree(options)
    return env


def dump_database(target, env, progress, before):
    def report_size():
        try:
            size = target.stat().st_size
        except FileNotFoundError:
            size = 0
        progress.update(bytes_done=before + size)

    # pg_dump writes directly to disk. Sample the file while it runs without
    # reading its output or relying on a database size or compression estimate.
    run(["pg_dump", "--format=custom", "--no-owner", "--no-acl", "--file", str(target)],
        env=env, on_tick=report_size if progress.send is not None else None)
    size = target.stat().st_size
    progress.update(bytes_done=before + size)
    with target.open("rb") as stream:
        if size < 5 or stream.read(5) != b"PGDMP":
            raise BackupValidationError("dump", "database dump is incomplete")
    run(["pg_restore", "--list", str(target)], output=True)
    return size


def is_meeting_audio_path(path, kb_roots):
    # Match server/internal/recordingwire.IsMeetingAudioPath:
    # <kb>/recordings/<id>/attachments/audio[/...] holds ephemeral hub-mode
    # meeting audio that is deleted after final minutes are written.
    for kb_root in kb_roots:
        try:
            parts = path.relative_to(kb_root).parts
        except ValueError:
            continue
        if (len(parts) >= 4 and parts[0] == "recordings" and
                parts[1] not in ("", ".", "..") and
                parts[2] == "attachments" and parts[3] == "audio"):
            return True
    return False


def history_entries(roots, excluded, kb_roots=()):
    def walk(path, name):
        if path in excluded or is_meeting_audio_path(path, kb_roots):
            return
        info = path.lstat()
        yield path, name, info
        if stat.S_ISDIR(info.st_mode):
            for child in sorted(path.iterdir()):
                yield from walk(child, name + "/" + child.name)

    for i, root in enumerate(roots):
        yield from walk(root, f"root-{i}")


class HistoryReader:
    def __init__(self, source, progress):
        self.source, self.progress = source, progress

    def read(self, size):
        chunk = self.source.read(size)
        self.progress.add_bytes(len(chunk))
        return chunk


def write_history_symlinks(archive, links, by_name):
    # Resolve source aliases using only the collected names. An excluded directory
    # or an alias leading outside the root is never consulted on the host.
    targets = {name: symlink_target(info) for name, info in links.items()}
    for name, info in links.items():
        target = targets[name]
        # Match the usual filesystem limit, including cycles with a growing suffix.
        for _ in range(40):
            path = PurePosixPath(target)
            alias = next((str(p) for p in (path, *path.parents) if str(p) in targets), None)
            if alias is None:
                break
            target = str(PurePosixPath(targets[alias]) / path.relative_to(alias))
        entry = by_name.get(target)
        if entry is None or not (entry.isfile() or entry.isdir() or entry.islnk()):
            raise BackupValidationError("archive_link", f"{name!r}: symlink target is not an archived file or directory")
        info.linkname = posixpath.relpath(target, posixpath.dirname(name))
        archive.addfile(info)


def archive_history(roots, excluded, target, progress, kb_roots=()):
    # The metadata-only pass skips the same subtrees as the archive pass.
    # Counters track regular files and uncompressed bytes entering the archive.
    size = count = 0
    linked_inodes = set()
    for _, _, info in history_entries(roots, excluded, kb_roots):
        if stat.S_ISREG(info.st_mode):
            if info.st_nlink > 1:
                inode = (info.st_dev, info.st_ino)
                if inode in linked_inodes:
                    continue
                linked_inodes.add(inode)
            size += info.st_size
            count += 1
    progress.update(force=True, bytes_done=0, bytes_total=size, item_done=0, item_total=count)
    written = 0
    by_name = {}
    links = {}
    root_paths = {f"root-{i}": str(root).rstrip("/") + "/" for i, root in enumerate(roots)}
    with tarfile.open(target, "w:gz", dereference=False, copybufsize=1024 * 1024) as archive:
        for path, name, _ in history_entries(roots, excluded, kb_roots):
            info = archive.gettarinfo(str(path), name)
            if info is None:
                continue
            archive_path(info.name)
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            if info.issym():
                if info.linkname.startswith("/"):
                    root = name.split("/")[0]
                    prefix = root_paths[root]
                    if not info.linkname.startswith(prefix):
                        raise BackupValidationError("archive_link", f"{name!r}: absolute symlink target is outside its history root")
                    relative = archive_path(info.linkname[len(prefix):], name)
                    info.linkname = posixpath.relpath(f"{root}/{relative}", posixpath.dirname(name))
                links[name] = info
                continue
            if info.islnk():
                validate_hardlink(info, by_name)
            elif not (info.isfile() or info.isdir()):
                continue
            if info.isfile():
                with path.open("rb") as source:
                    archive.addfile(info, HistoryReader(source, progress))
                written += 1
                progress.update(item_done=written)
            else:
                archive.addfile(info)
            by_name[name] = info
        write_history_symlinks(archive, links, by_name)
    if written == 0:
        raise ValueError("no history or configuration files found")
    progress.emit(force=True)


@contextlib.contextmanager
def backup_preflight(request):
    backup = request["config"]["backup"]
    builtin = backup.get("builtin") is True
    runtime, roots, databases, excluded, kb_roots = inventory(request)
    database_envs = [database_environment(database) for database in databases]
    # The Hub pauses its own writers and says so (builtin); otherwise the
    # configured hooks must pause every writer this snapshot includes.
    if builtin:
        if backup.get("extra_databases") or backup.get("extra_roots"):
            raise ValueError("built-in writer pause cannot coordinate extra_databases or extra_roots; configure quiesce and resume commands")
        manager_root = runtime / "managers"
        if manager_root.exists() and any(f.parent.name != "assistant" for f in manager_root.glob("*/manager.json")):
            raise ValueError("built-in writer pause cannot coordinate other local managers; configure quiesce and resume commands")
    elif not backup.get("quiesce") or not backup.get("resume"):
        raise ValueError("configure writer quiesce and resume commands before updating")
    retain = backup.get("retain") or 3
    if type(retain) is not int or not 1 <= retain <= 50:
        raise BackupValidationError("retention", "invalid snapshot retention")
    store = backup_store(request)
    # A store that is a collected root or holds one cannot be left out of the
    # archive without dropping that root, so the snapshot would be hollow.
    resolved_store = store.resolve()
    for root in roots:
        if root == resolved_store or root.is_relative_to(resolved_store):
            raise BackupValidationError("directory", "the backup directory may not be the runtime or contain a collected root")
    store.mkdir(mode=0o700, parents=True, exist_ok=True)
    if store.is_symlink():
        raise BackupValidationError("directory", "linked backup directory")
    if store.stat().st_mode & 0o077:
        raise BackupValidationError("private_directory", "backup directory must be private (0700)")
    with (store / ".lock").open("a") as lock:
        lock_backup(lock)
        coordination = store / "coordination-pending.json"
        if os.path.lexists(coordination):
            raise BackupValidationError("coordination", "previous writer coordination needs recovery; inspect the retained marker")
        for stale in store.glob(".incomplete-*"):
            if stat.S_ISDIR(stale.lstat().st_mode):
                shutil.rmtree(stale)
                print("development backup: removed an incomplete snapshot from an earlier attempt", flush=True)
        yield excluded, roots, databases, database_envs, store, resolved_store, retain, kb_roots


def preflight(request):
    with backup_preflight(request) as (_, _, _, _, store, _, _, _):
        # An existing writable lock does not prove we can create a snapshot.
        with tempfile.TemporaryDirectory(prefix=".incomplete-", dir=store):
            return {}


def recovery_program_paths(request):
    for name, path in request.get("executables", {}).items():
        if name not in ("personad", "persona-hub", "persona-manager"):
            raise ValueError("unknown recovery program")
        yield "programs", name, Path(path)

    if not request.get("node_directory"):
        return
    node_dir = Path(request["node_directory"])
    if node_dir.is_symlink():
        raise ValueError("linked Node package directory")
    # Include matching packages even for platforms with no registered node.
    platforms = {p.name.removeprefix("personad-").removesuffix(".exe") for p in node_dir.glob("personad-*")}
    for platform in sorted(p for p in platforms if re.fullmatch(r"[a-z0-9]+-[a-z0-9]+", p)):
        path = node_dir / binary_name("personad", platform)
        if platform.startswith("windows-") and not path.exists():
            path = node_dir / ("personad-" + platform)  # earlier development builds
        if path.is_symlink() or not path.is_file():
            continue
        version = path.with_name(path.name + ".version")
        if version.exists() and version.read_text().strip() != request["from_version"]:
            continue
        yield "nodes", platform, path


def program_stamp(stream):
    info = os.fstat(stream.fileno())
    return info.st_size, info.st_mtime_ns


def archive_programs(sources, temporary, manifest):
    for field, name, source, stamp in sources:
        node = field == "nodes"
        artifact = temporary / (("node-" if node else "program-") + name + ".gz")
        host = manifest["programs"].get("personad")
        compare_host = node and name == manifest["platform"] and host is not None
        if compare_host and stream_digest(source) == host["sha256"]:
            # Keep both v1 names, including on stores without hard-link support.
            original = temporary / host["file"]
            try:
                os.link(original, artifact)
            except OSError as exc:
                if exc.errno not in (errno.EPERM, errno.EOPNOTSUPP, errno.ENOSYS, errno.EMLINK):
                    raise
                shutil.copyfile(original, artifact)
            native = host["sha256"]
            manifest["files"][artifact.name] = manifest["files"][host["file"]]
        else:
            if compare_host:
                source.seek(0)
            with gzip.open(artifact, "wb") as dst:
                checksum = hashlib.sha256()
                for chunk in iter(lambda: source.read(1024 * 1024), b""):
                    checksum.update(chunk)
                    dst.write(chunk)
                native = checksum.hexdigest()
            manifest["files"][artifact.name] = digest(artifact)
        if program_stamp(source) != stamp:
            raise ValueError("installed program changed during backup")
        entry = {"file": artifact.name, "sha256": native}
        if not node:
            entry["target"] = source.name
        manifest[field][name] = entry


def snapshot(request, progress=None):
    backup = request["config"]["backup"]
    builtin = backup.get("builtin") is True
    progress = progress or Progress("backing_up")
    progress.step("preflight")
    # Recheck under the same lock held through publication. The build and idle
    # wait may have taken place since the early preflight released it.
    with backup_preflight(request) as (excluded, roots, databases, database_envs, store, resolved_store, retain, kb_roots):
        coordination = store / "coordination-pending.json"
        ident = "snapshot-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d%H%M%S%f") + "-" + request["candidate"]["commit"][:12]
        if not NAME.fullmatch(ident):
            raise ValueError("invalid snapshot commit")
        published = store / ident
        manifest = {"owner": OWNER, "id": ident, "commit": request["candidate"]["commit"],
                    "from_version": request.get("from_version", ""), "from_commit": request.get("from_commit", ""),
                    "from_build": request.get("from_build", 0), "from_updated_at": request.get("from_updated_at", ""),
                    "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "platform": request.get("platform", ""),
                    "roots": [str(p) for p in roots], "databases": len(databases), "programs": {}, "nodes": {}, "files": {}}
        with tempfile.TemporaryDirectory(prefix=".incomplete-", dir=store) as staging, contextlib.ExitStack() as handles:
            temporary = Path(staging) / ident
            temporary.mkdir(mode=0o700)
            progress.step("quiesce")
            if not builtin:
                write_json(coordination, {"owner": OWNER, "snapshot": ident, "phase": "quiescing"})
                sync_dir(store)
            snapshot_error = None
            try:
                if builtin:
                    print("development backup: writers paused by the hub", flush=True)
                else:
                    print("development backup: pausing writers", flush=True)
                    run(backup["quiesce"], timeout=60)
                    # A writer could have changed a configured root since preflight.
                    # Decide exclusions against the configuration we actually archive.
                    _, paused_roots, paused_databases, excluded, kb_roots = inventory(request)
                    if paused_roots != roots or paused_databases != databases:
                        raise ValueError("backup inventory changed while pausing writers")
                # Hooks must stop every DB/file writer in this snapshot's scope,
                # including independent managers, before returning successfully.
                progress.step("database_dump", item_done=0, item_total=len(database_envs), bytes_done=0)
                dumped = 0
                for i, database_env in enumerate(database_envs):
                    print(f"development backup: database {i}", flush=True)
                    target = temporary / f"database-{i}.dump"
                    # Parsed libpq fields go in the environment, never argv or the receipt.
                    dumped += dump_database(target, database_env, progress, dumped)
                    progress.update(item_done=i + 1)
                progress.emit(force=True)
                progress.step("archive_files")
                # A chosen folder inside a collected root is never archived:
                # each snapshot would otherwise carry every earlier one. A
                # folder outside the roots needs no exclusion (and one that
                # holds a root was refused above).
                if any(store.is_relative_to(root) or resolved_store.is_relative_to(root) for root in roots):
                    excluded.add(store)
                    excluded.add(resolved_store)
                print("development backup: configuration and history files", flush=True)
                archive_history(roots, excluded, temporary / "files.tar.gz", progress, kb_roots)
                # Effective environment overrides can differ from hub.json. Keep
                # restore DSNs only in this private backup, never in its manifest/API.
                write_json(temporary / "effective-databases.json", databases)
                # Pin the selected version across atomic package replacements on resume.
                sources = []
                for field, name, path in recovery_program_paths(request):
                    source = handles.enter_context(path.open("rb"))
                    sources.append((field, name, source, program_stamp(source)))
            except BaseException as exc:
                snapshot_error = exc
                print("development backup: snapshot failed: " + failure_detail(exc), file=sys.stderr, flush=True)
                raise
            finally:
                if builtin:
                    if snapshot_error is None:
                        progress.step("resume_writers")
                        if "capture_fd" in request:
                            os.write(request["capture_fd"], b"R")
                            if os.read(request["resumed_fd"], 1) != b"R":
                                raise ValueError("hub did not acknowledge writer resume")
                else:
                    # Even partially failed quiesce hooks can have stopped a writer.
                    # A failed resume preserves its marker and blocks adoption/retry.
                    with defer_cancellation() as cancelled:
                        if snapshot_error is None:
                            progress.step("resume_writers")
                        print("development backup: resuming writers", flush=True)
                        try:
                            run(backup["resume"], timeout=60)
                        except BaseException as exc:
                            if snapshot_error is not None:
                                progress.step("resume_writers")
                            print("development backup: resume failed: " + failure_detail(exc), file=sys.stderr, flush=True)
                            if snapshot_error is not None:
                                raise exc from snapshot_error
                            raise
                        coordination.unlink()
                        sync_dir(store)
                    if cancelled and snapshot_error is None:
                        raise InterruptedError()
            progress.step("archive_programs")
            archive_programs(sources, temporary, manifest)
            handles.close()
            progress.step("verify_snapshot")
            # Hash the immutable dumps and history after releasing live writers.
            for path in temporary.iterdir():
                if path.name not in manifest["files"]:
                    manifest["files"][path.name] = digest(path)
            write_json(temporary / "manifest.json", manifest)
            for member in temporary.iterdir():
                sync_dir(member)
            sync_dir(temporary)
            verify(temporary, progress)
            os.rename(temporary, published)
            sync_dir(store)
            progress.emit(force=True)
            print("development backup: verified " + ident, flush=True)
        pruned = []
        owned = []
        for path in store.iterdir():
            if NAME.fullmatch(path.name) and not path.is_symlink():
                try:
                    verify(path)
                    owned.append(path)
                except (ValueError, OSError, json.JSONDecodeError):
                    pass  # Unknown/damaged data is retained for inspection.
        for old in sorted(owned)[:-retain]:
            print("development backup: cleaning " + old.name, flush=True)
            shutil.rmtree(old)
            pruned.append(old.name)
        sync_dir(store)
        return {"id": ident, "retained": min(len(owned), retain), "pruned": pruned}


def main():
    os.umask(0o077)
    if len(sys.argv) == 3 and sys.argv[1] == "verify":
        verify(Path(sys.argv[2]))
        print("Snapshot verified")
        return
    if len(sys.argv) == 4 and sys.argv[1] == "extract-history":
        extract_history(Path(sys.argv[2]), Path(sys.argv[3]))
        print("History extracted into the empty recovery directory")
        return
    request = json.load(sys.stdin)
    # No interactive credentials or indefinite network prompt in an updater.
    os.environ["GIT_TERMINAL_PROMPT"] = "0"
    signal.signal(signal.SIGINT, interrupt)
    signal.signal(signal.SIGTERM, interrupt)
    operation = request["operation"]
    action = {"resolve": resolve, "build": build, "preflight": preflight, "backup": snapshot,
              "backups": backups, "prepare_backup": prepare_backup}[operation]
    phase = {"build": "building", "backup": "backing_up", "prepare_backup": "applying_backup"}.get(operation)
    fd = request.get("progress_fd")
    try:
        if phase and type(fd) is int and fd > 2:
            progress = Progress(phase, progress_writer(fd))
            result = action(request, progress)
        else:
            result = action(request)
    except Exception as exc:
        if operation in ("preflight", "backup"):
            # Only a fixed reason code may cross into status. The private log
            # retains local details, including archive member names.
            failure = {"reason": backup_failure_reason(exc)}
            if isinstance(exc, subprocess.CalledProcessError):
                failure["exit_code"] = exc.returncode
            write_json(Path(request["result"]).with_name("failure.json"), failure)
        raise
    write_json(Path(request["result"]), result)


if __name__ == "__main__":
    try:
        main()
    except BaseException as exc:
        # Child output is in a private local log; no exception/DSN/argv enters the API.
        print("Development update helper failed: " + failure_detail(exc), file=sys.stderr)
        sys.exit(1)
