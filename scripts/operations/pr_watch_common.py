"""Shared process, GitHub and JSON helpers for the maintainer PR watcher.

Only trusted local configuration supplies commands. Pull-request content is always
an argument or a file to inspect, never a shell fragment.
"""
import hashlib
import json
import os
import re
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path

REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
SHA = re.compile(r"^[0-9a-f]{40}$")
PR_FIELDS = (
    "number,title,body,state,isDraft,headRefOid,headRefName,baseRefName,"
    "isCrossRepository,mergeable,mergeStateStatus,reviewDecision,url,"
    "createdAt,updatedAt"
)
STALE_LABEL = "stale"
STALE_SECONDS = 7 * 86400


def github_time(value):
    """Parse only GitHub's explicit UTC timestamps; missing dates fail closed."""
    if not isinstance(value, str):
        raise WatchError("GitHub timestamp is missing")
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z", value):
        raise WatchError("GitHub timestamp is not explicit UTC")
    try:
        return datetime.fromisoformat(value[:-1] + "+00:00").timestamp()
    except ValueError as error:
        raise WatchError("GitHub timestamp is invalid") from error


def progress_timestamp(snapshot, comments, reviews, commits):
    """Actual PR activity, not a poll or a retry counter, resets stale cleanup."""
    latest = github_time(snapshot.get("createdAt"))
    if not isinstance(comments, list) or not isinstance(reviews, list) or not isinstance(commits, list):
        raise WatchError("stale-PR activity must contain comments, reviews and commits")
    for item, field in [(item, "createdAt") for item in comments + reviews] + [(item, "committedDate") for item in commits]:
        if not isinstance(item, dict):
            raise WatchError("stale-PR activity contains an invalid record")
        latest = max(latest, github_time(item.get(field)))
    return latest


class WatchError(Exception):
    """An unavailable or invalid prerequisite, never a successful verification."""


def repository(value):
    if not isinstance(value, str) or not REPOSITORY.fullmatch(value):
        raise WatchError("repository must be owner/name")
    return value


def clean_env(environ=None):
    """Keep credentials for the explicitly invoked gh, but isolate Git and tools."""
    env = dict(os.environ if environ is None else environ)
    for key in list(env):
        if key.startswith("GIT_") or key in {
                "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "PWD", "OLDPWD"}:
            env.pop(key, None)
    env.update(GIT_TERMINAL_PROMPT="0", GH_PROMPT_DISABLED="1", CI="1")
    return env


def run(argv, cwd=None, timeout=120, environ=None, input_text=None):
    """Run an argv command with a bounded lifetime, including its process group."""
    if not isinstance(argv, (list, tuple)) or not argv or not all(isinstance(x, str) for x in argv):
        raise WatchError("a command must be a non-empty argv list")
    arguments = list(argv)
    pass_fds = ()
    if len(arguments) > 1 and arguments[1] == "--args-fd":
        # bwrap accepts its NUL-separated options from a file descriptor. A
        # full-tree conflict can otherwise exceed execve's per-argument limit.
        # The options remain the same mounts and environment built by the
        # trusted caller; this changes transport, never sandbox permissions.
        with tempfile.TemporaryFile(prefix="pr-watch-bwrap-args-", buffering=0) as options:
            if "--" not in arguments[2:]:
                raise WatchError("bwrap FD arguments need a command after --")
            # --args FD replaces bwrap's option parser input, including its
            # final --. The worker command and its arguments stay in argv.
            command = arguments.index("--", 2)
            for argument in arguments[2:command]:
                if "\0" in argument:
                    raise WatchError("bwrap arguments must not contain NUL")
                options.write(argument.encode() + b"\0")
            options.flush()
            options.seek(0)
            arguments = [arguments[0], "--args", str(options.fileno()), *arguments[command + 1:]]
            pass_fds = (options.fileno(),)
            return _run_process(arguments, cwd, timeout, environ, input_text, pass_fds)
    return _run_process(arguments, cwd, timeout, environ, input_text, pass_fds)


def _bounded_command(argv):
    """Use only the maintainer's explicit wrapper, never a host-specific default."""
    wrapper = os.environ.get("PR_WATCH_PROCESS_WRAPPER", "").strip()
    if not wrapper:
        return list(argv)
    import shlex
    return shlex.split(wrapper) + list(argv)


def _run_process(argv, cwd, timeout, environ, input_text, pass_fds):
    proc = subprocess.Popen(
        _bounded_command(argv), cwd=cwd, env=clean_env(environ), stdin=subprocess.PIPE if input_text is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True, pass_fds=pass_fds,
    )
    try:
        stdout, stderr = proc.communicate(input=input_text, timeout=timeout)
    except subprocess.TimeoutExpired:
        stop_process(proc)
        raise WatchError("command timed out after %ss: %s" % (timeout, Path(argv[0]).name))
    except BaseException:
        stop_process(proc)
        raise
    return subprocess.CompletedProcess(argv, proc.returncode, stdout, stderr)


def stop_process(proc):
    if proc.poll() is not None:
        return
    try:
        os.killpg(proc.pid, 15)
        proc.wait(timeout=10)
    except ProcessLookupError:
        return
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, 9)
        except ProcessLookupError:
            pass
        proc.wait(timeout=10)


def checked(argv, cwd=None, timeout=120, environ=None, input_text=None):
    result = run(argv, cwd=cwd, timeout=timeout, environ=environ, input_text=input_text)
    if result.returncode:
        detail = (result.stderr.strip() or result.stdout.strip())[-2000:]
        if argv[:2] == ["git", "fetch"] and "exhaustive rename detection was skipped" in detail:
            # This fallback supports only the exact single-refspec shape used
            # by the watcher, never arbitrary options or multiple refspecs. An
            # empty refmap (repair fetches, #10584) never updates a tracking
            # ref, which the FETCH_HEAD recovery below already honors.
            if argv[3:4] == ["--refmap="]:
                argv = argv[:3] + argv[4:]
            if len(argv) != 5 or argv[3].startswith("-"):
                raise WatchError("unsupported fetch recovery command shape")
            if argv[4].lstrip("+").startswith("-"):
                raise WatchError("unsupported fetch recovery refspec")
            remote_name = argv[3]
            original_refspecs = argv[4:]
            if len(original_refspecs) != 1:
                raise WatchError("fetch recovery requires exactly one refspec")
            refspecs = [refspec.rsplit(":", 1)[0].lstrip("+") for refspec in original_refspecs]
            remote_refspecs = [refspec if refspec.startswith("refs/") else "refs/" + refspec for refspec in refspecs]
            if remote_name.startswith("-") or any(refspec.startswith("-") for refspec in refspecs):
                raise WatchError("cannot recover an unsafe fetch refspec")
            refreshed = run(["git", "ls-remote", remote_name, *remote_refspecs], cwd=cwd,
                            timeout=60, environ=environ)
            if refreshed.returncode:
                raise WatchError("cannot resolve the exact remote ref after a refused fetch: " + bounded(refreshed.stderr, 800))
            entries = [line.split(None, 1) for line in refreshed.stdout.splitlines() if line.strip()]
            if not (len(entries) == 1 and len(entries[0]) == 2
                    and entries[0][1] in remote_refspecs and SHA.fullmatch(entries[0][0])):
                raise WatchError("refused fetch could not resolve one exact remote ref")
            # The commit was received but the ref transaction was refused.
            # Re-fetch the validated SHA and finish the exact requested ref.
            checked(["git", "-c", "diff.renames=false", "fetch", "-q", "--no-write-fetch-head",
                     remote_name, entries[0][0]], cwd=cwd, timeout=timeout, environ=environ)
            checked(["git", "cat-file", "-e", entries[0][0] + "^{commit}"], cwd=cwd,
                    timeout=60, environ=environ)
            original_refspec = next(value for value in original_refspecs
                                    if (value.rsplit(":", 1)[0].lstrip("+") == entries[0][1]
                                        or "refs/" + value.rsplit(":", 1)[0].lstrip("+") == entries[0][1]))
            if ":" in original_refspec:
                destination = original_refspec.split(":", 1)[1]
                if not destination.startswith("refs/"):
                    raise WatchError("fetch recovery needs a fully qualified destination ref")
                checked(["git", "update-ref", destination, entries[0][0]], cwd=cwd,
                        timeout=60, environ=environ)
            else:
                if not cwd:
                    raise WatchError("FETCH_HEAD recovery requires the actual checkout path")
                git_dir_text = checked(["git", "rev-parse", "--git-dir"], cwd=cwd).strip()
                git_dir = (Path(cwd).resolve() / git_dir_text).resolve() if not Path(git_dir_text).is_absolute() else Path(git_dir_text).resolve()
                common_text = checked(["git", "rev-parse", "--git-path", "FETCH_HEAD"], cwd=cwd).strip()
                destination = (Path(cwd).resolve() / common_text).resolve() if not Path(common_text).is_absolute() else Path(common_text).resolve()
                if (destination.name != "FETCH_HEAD" or destination.parent != git_dir
                        or git_dir.is_symlink() or destination.is_symlink() or not git_dir.is_dir()):
                    raise WatchError("fetch recovery requires a path inside the checkout's Git directory")
                temporary = destination.parent / ("FETCH_HEAD.pr-watch-" + entries[0][0] + ".tmp")
                temporary.write_text(entries[0][0] + "\t\tbranch '" + entries[0][1] + "' of " + remote_name + "\n",
                                     encoding="utf-8")
                os.chmod(temporary, 0o600)
                temporary.replace(destination)
                if checked(["git", "rev-parse", "FETCH_HEAD"], cwd=cwd).strip() != entries[0][0]:
                    raise WatchError("fetch recovery could not establish the exact FETCH_HEAD")
            return entries[0][0] + "\n"
        raise WatchError("%s failed (%s): %s" % (Path(argv[0]).name, result.returncode, detail))
    return result.stdout


def gh_json(argv, timeout=90, environ=None, input_text=None):
    text = checked(["gh"] + argv, timeout=timeout, environ=environ, input_text=input_text)
    if not text.strip():
        raise WatchError("gh returned an empty response")
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        # --paginate applies --jq per page. Depending on the endpoint and gh
        # version, that is several adjacent JSON values, not one array.
        decoder = json.JSONDecoder()
        values = []
        index = 0
        try:
            while index < len(text):
                while index < len(text) and text[index].isspace():
                    index += 1
                if index == len(text):
                    break
                value, index = decoder.raw_decode(text, index)
                values.append(value)
        except json.JSONDecodeError as exc:
            raise WatchError("gh returned invalid JSON: %s" % exc)
        if len(values) == 1:
            return values[0]
        if all(not isinstance(value, list) for value in values):
            return values
        flattened = []
        for value in values:
            if not isinstance(value, list):
                raise WatchError("gh returned mixed paginated JSON values")
            flattened.extend(value)
        return flattened


def pr_snapshot(repo, number, timeout=90, environ=None):
    # Resolve the live base in the same query as the PR. The PR's recorded
    # baseRefOid is not necessarily its base branch's current tip (#10568).
    owner, name = repository(repo).split("/", 1)
    query = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title body state isDraft headRefOid headRefName baseRefName
      author { login }
      baseRef { target { oid } }
      isCrossRepository mergeable mergeStateStatus reviewDecision url createdAt updatedAt
    }
  }
}
"""
    payload = {"query": query, "variables": {"owner": owner, "name": name, "number": number}}
    response = gh_json(["api", "graphql", "--input", "-"], input_text=json.dumps(payload),
                       timeout=timeout, environ=environ)
    if not isinstance(response, dict):
        raise WatchError("GitHub returned an invalid PR snapshot")
    try:
        repository_data = response["data"]["repository"]
        data = repository_data["pullRequest"]
        base = data["baseRef"]["target"]["oid"]
    except (KeyError, TypeError) as error:
        raise WatchError("GitHub returned an incomplete PR snapshot") from error
    if not isinstance(data, dict) or data.get("number") != number:
        raise WatchError("unexpected PR snapshot")
    data["baseRefOid"] = base
    for field in ("headRefOid", "baseRefOid"):
        if not isinstance(data.get(field), str) or not SHA.fullmatch(data[field]):
            raise WatchError("PR snapshot has no valid %s" % field)
        if field == "baseRefOid" and data[field] == "0" * 40:
            raise WatchError("PR snapshot has no repository base commit")
    for field in ("title", "body", "state", "headRefName", "baseRefName", "url", "mergeable", "createdAt", "updatedAt"):
        if not isinstance(data.get(field), str):
            raise WatchError("PR snapshot has no valid %s" % field)
    github_time(data["createdAt"])
    github_time(data["updatedAt"])
    if not isinstance(data.get("isDraft"), bool) or not isinstance(data.get("isCrossRepository"), bool):
        raise WatchError("PR snapshot has invalid flags")
    author = data.get("author")
    if author is not None and (not isinstance(author, dict) or not isinstance(author.get("login"), str)):
        raise WatchError("PR snapshot has invalid author")
    return data


class RepairAttemptError(WatchError):
    """A completed repair attempt that produced no usable patch."""


class VerificationError(WatchError):
    """A completed failed check with enough information for a base comparison."""
    def __init__(self, message, command=None, output="", failures=None, checks=None):
        super().__init__(message)
        self.command = command
        self.output = output
        self.failures = failures or []
        self.checks = checks or []


def test_or_fix(pr, names):
    """Maintainer policy: test/fix titles and test-only changes need no screenshots."""
    if re.match(r"^(?:test|fix)(?:\([^)]*\))?:\s+", pr.get("title", ""), re.I):
        return True
    TEST_PARTS = {"test", "tests", "testdata", "fixtures", "__tests__", "e2e"}
    def test_file(name):
        path = Path(name)
        parts = path.parts
        if len(parts) > 1 and parts[1] in ("PersonaTests", "PersonaUITests"):
            return True
        if path.name.endswith(("_test.go", "_test.py", ".test.ts", ".test.tsx", ".test.js", ".test.mjs")):
            return True
        return any(part.lower() in TEST_PARTS and "test" in path.name.lower() for part in parts)
    return bool(names) and all(test_file(name) for name in names)


def verification_env():
    """Allow only non-secret process/toolchain inputs into PR-controlled builds."""
    import shutil
    import tempfile
    original_home = Path(os.environ.get("HOME", str(Path.home())))
    home = Path(tempfile.mkdtemp(prefix="pr-watch-verification-home-"))
    env = {
        "PATH": os.environ.get("PATH", os.defpath),
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "GIT_TERMINAL_PROMPT": "0",
        "GH_PROMPT_DISABLED": "1",
        "CI": "1",
    }
    # Explicitly non-secret locale, display, toolchain and proxy routing inputs.
    for key in ("LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR", "TERM", "DISPLAY",
                "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
                "http_proxy", "https_proxy", "all_proxy", "no_proxy",
                "NODE_ENV", "NPM_CONFIG_REGISTRY", "GOPROXY", "GOSUMDB", "GOFLAGS",
                "GRADLE_USER_HOME", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV",
                "ANDROID_HOME", "ANDROID_SDK_ROOT", "JAVA_HOME"):
        value = os.environ.get(key)
        if value:
            env[key] = value
    # The host's Go build caches may be shared read/write by the trusted Go tool;
    # no GitHub config or credentials are carried with them.
    for key, relative in (("GOCACHE", ".cache/go-build"), ("GOMODCACHE", "go/pkg/mod"),
                          ("GOPATH", "go"), ("GRADLE_USER_HOME", ".gradle")):
        if key not in env and (original_home / relative).exists():
            env[key] = str(original_home / relative)
    go = shutil.which("go")
    if go:
        result = run([go, "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"], timeout=30)
        if result.returncode == 0:
            values = json.loads(result.stdout)
            for key in ("GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"):
                if values.get(key):
                    env[key] = values[key]
    # PR build/test checks do not need debug metadata, but keep compiler
    # optimizations and every test/vet assertion. go vet is analysis, not a link
    # step, so it explicitly clears the linker flag. Explicit GOFLAGS wins.
    if "GOFLAGS" not in env:
        env["GOFLAGS"] = "-gcflags=all=-dwarf=false -ldflags=-w"
    env["GOFLAGS"] = with_trimpath(env["GOFLAGS"])
    return env


def with_trimpath(goflags):
    """GOFLAGS with -trimpath added unless it already sets the flag (-trimpath=false included).

    Without -trimpath Go's build cache key includes each package's absolute directory, so every PR worktree would
    compile and cache every package again (#10614; scripts/test/go_trimpath.sh is the shell scripts' copy).
    """
    if any(flag.lstrip("-").split("=", 1)[0] == "trimpath" for flag in goflags.split()):
        return goflags
    return (goflags + " -trimpath").strip()


def atomic_json(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(data, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(str(temporary), 0o600)
    temporary.replace(path)
    try:
        os.chmod(str(path.parent), 0o700)
    except OSError:
        pass


def fingerprint(repo, number, sha, kind, content=""):
    raw = json.dumps([repo, number, sha, kind, content], ensure_ascii=False, sort_keys=True)
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def bounded(text, limit=3500):
    text = str(text or "").strip()
    if len(text) <= limit:
        return text
    return text[:limit // 2] + "\n… output truncated …\n" + text[-limit // 2:]
