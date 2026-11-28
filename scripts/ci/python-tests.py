#!/usr/bin/env python3
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from run import resolve, terminate
sys.path.insert(0, str(ROOT / "scripts" / "test"))
from server_test_env import clean_environment


def is_suite(path):
    """A test_*.py that runs under unittest. Some are verification scripts that take arguments (test_rolling_compatibility.py,
    test_source_development_resources.py): unittest runs no tests in them, which exits 0 on Python 3.11 but 5 ("NO TESTS
    RAN") from 3.12, so a local run on a newer Python would count them as failures."""
    return "unittest" in path.read_text(encoding="utf-8", errors="replace")


def select_suites(suites, index=0, count=1):
    if count < 1 or not 0 <= index < count:
        raise ValueError("Shard count must be positive and index must be within the count")
    return sorted(suites)[index::count]


# Each suite is its own python process and most wait on subprocesses, sockets or files, not the CPU: run in turn they
# took over 900 s on a loaded CI machine, CI's limit for this step, while their CPU time is a fraction of that. Run several
# at once, each with its own HOME, and print each suite's output in one block when it ends so logs stay readable.
DEFAULT_JOBS = 6
SUITE_SECONDS = 120


def toolchain_homes(environment):
    """Rust shims need their installed toolchain and shared Cargo cache after HOME is isolated."""
    home = Path(environment.get("HOME") or str(Path.home()))
    homes = {key: str(Path(value).expanduser().resolve())
             for key, value in environment.items() if key in {"RUSTUP_HOME", "CARGO_HOME"} and value}
    homes.setdefault("RUSTUP_HOME", str(home / ".rustup"))
    homes.setdefault("CARGO_HOME", str(home / ".cargo"))
    return homes


def run_suite(suite, home_root, lock, homes):
    """Run one suite with captured output; print its block whole; return (label, exit code)."""
    label = str(suite.relative_to(ROOT))
    home = tempfile.mkdtemp(prefix="h-", dir=home_root)
    env = dict(os.environ, HOME=home, **homes)
    started = time.monotonic()
    with tempfile.TemporaryFile(dir=home_root) as output:
        process = subprocess.Popen(resolve([sys.executable, "-B", "-m", "unittest", "-q", suite.stem]), cwd=suite.parent,
                                   env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            code = process.wait(timeout=SUITE_SECONDS)
            verdict = "PASS" if code == 0 else "FAIL"
        except subprocess.TimeoutExpired:
            terminate(process)
            code, verdict = 124, "TIMEOUT"
        output.seek(0)
        text = output.read().decode("utf-8", errors="replace")
    elapsed = time.monotonic() - started
    with lock:
        print(f"::group::{label}\n{text}", end="" if text.endswith("\n") or not text else "\n", flush=True)
        print(f"::endgroup::\n{verdict} {label} ({elapsed:.1f}s, exit {code})", flush=True)
    return label, (code if code >= 0 else 128 - code)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--shard-index", type=int, default=int(os.environ.get("CI_PYTHON_SHARD_INDEX", "0")))
    parser.add_argument("--shard-count", type=int, default=int(os.environ.get("CI_PYTHON_SHARD_COUNT", "1")))
    parser.add_argument("--jobs", type=int, default=int(os.environ.get("CI_PYTHON_JOBS", str(DEFAULT_JOBS))))
    args = parser.parse_args()
    if args.jobs < 1:
        parser.error("--jobs must be positive")
    found = [path for path in ROOT.joinpath("scripts").rglob("test_*.py") if "node_modules" not in path.parts]
    suites = [path for path in found if is_suite(path)]
    if args.shard_index == 0:
        for path in sorted(set(found) - set(suites)):
            print(f"SKIP {path.relative_to(ROOT)} (not a unittest suite)", flush=True)
    try:
        suites = select_suites(suites, args.shard_index, args.shard_count)
    except ValueError as error:
        parser.error(str(error))
    if not suites:
        sys.exit("FAIL no Python script suites discovered for shard")
    print(f"Python shard {args.shard_index + 1}/{args.shard_count}: {len(suites)} suites", flush=True)
    caches = json.loads(subprocess.check_output(["go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH"], text=True, timeout=30))
    # Resolve the caller's rustup home before the launcher replaces its own HOME; doing this in run_suite would be too late.
    homes = toolchain_homes(os.environ)
    failed = []
    with tempfile.TemporaryDirectory(prefix="persona-ci-home-", dir=os.environ.get("TMPDIR")) as home:
        cleaned = clean_environment(dict(os.environ))
        os.environ.clear()
        os.environ.update(cleaned)
        os.environ.update(caches)
        os.environ.update(HOME=home)
        reservations = [socket.socket(), socket.socket()]
        try:
            for reservation in reservations:
                reservation.bind(("127.0.0.1", 0))
            os.environ["MOCK_MESH_TEST_PORTS"] = ",".join(str(reservation.getsockname()[1]) for reservation in reservations)
        finally:
            for reservation in reservations:
                reservation.close()
        lock = threading.Lock()
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
            for label, code in pool.map(lambda suite: run_suite(suite, home, lock, homes), suites):
                if code:
                    failed.append(label)
    print(f"Python suites: {len(suites)}; failed: {len(failed)}")
    for suite in failed:
        print(f"FAIL {suite}")
    return bool(failed)


if __name__ == "__main__":
    sys.exit(main())
