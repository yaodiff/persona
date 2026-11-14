"""Shared process, GitHub and JSON helpers for the maintainer PR watcher.

Only trusted local configuration supplies commands. Pull-request content is always
an argument or a file to inspect, never a shell fragment.
"""
import hashlib
import json
import os
import re
import subprocess
from pathlib import Path

REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
SHA = re.compile(r"^[0-9a-f]{40}$")
PR_FIELDS = (
    "number,title,body,state,isDraft,headRefOid,headRefName,baseRefName,"
    "baseRefOid,isCrossRepository,mergeable,mergeStateStatus,reviewDecision,url"
).replace("baseRefOid,", "")


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
    proc = subprocess.Popen(
        argv, cwd=cwd, env=clean_env(environ), stdin=subprocess.PIPE if input_text is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True,
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
        raise WatchError("%s failed (%s): %s" % (Path(argv[0]).name, result.returncode, detail))
    return result.stdout


def gh_json(argv, timeout=90, environ=None):
    text = checked(["gh"] + argv, timeout=timeout, environ=environ)
    try:
        return json.loads(text)
    except json.JSONDecodeError as exc:
        raise WatchError("gh returned invalid JSON: %s" % exc)


def pr_snapshot(repo, number, timeout=90, environ=None):
    data = gh_json(["pr", "view", str(number), "--repo", repository(repo), "--json", PR_FIELDS],
                   timeout=timeout, environ=environ)
    if not isinstance(data, dict) or data.get("number") != number:
        raise WatchError("unexpected PR snapshot")
    for field in ("headRefOid", "baseRefOid"):
        if field == "baseRefOid" and not data.get(field):
            # gh pr view does not expose baseRefOid on every supported version.
            # Resolve the actual current base tip, not the PR's original base.
            data[field] = checked(["gh", "api", "repos/%s/commits/%s" % (repo, data["baseRefName"]),
                                   "--jq", ".sha"], timeout=timeout, environ=environ).strip()
        if not isinstance(data.get(field), str) or not SHA.fullmatch(data[field]):
            raise WatchError("PR snapshot has no valid %s" % field)
        if field == "baseRefOid" and data[field] == "0" * 40:
            raise WatchError("PR snapshot has no repository base commit")
    for field in ("title", "body", "state", "headRefName", "baseRefName", "url", "mergeable"):
        if not isinstance(data.get(field), str):
            raise WatchError("PR snapshot has no valid %s" % field)
    if not isinstance(data.get("isDraft"), bool) or not isinstance(data.get("isCrossRepository"), bool):
        raise WatchError("PR snapshot has invalid flags")
    return data


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
    return env


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
