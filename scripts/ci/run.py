#!/usr/bin/env python3
import argparse
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time


WINDOWS = os.name == "nt"


def resolve(command):
    """On Windows, launchers such as npm are `.cmd` shims that CreateProcess finds only by their full path."""
    if not WINDOWS:
        return command
    found = shutil.which(command[0])
    return [found, *command[1:]] if found else command


def terminate(process):
    """Stop the command and everything it started: a process group on POSIX, the process tree on Windows."""
    if WINDOWS:
        subprocess.run(["taskkill", "/T", "/F", "/PID", str(process.pid)], capture_output=True, check=False)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            # A failed taskkill must not hold up the verdict: kill the child directly, then stop waiting.
            try:
                process.kill()
                process.wait(timeout=5)
            except (OSError, subprocess.TimeoutExpired):
                pass
        return
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()


def run(label, seconds, command):
    started = time.monotonic()
    print(f"::group::{label}", flush=True)
    try:
        process = subprocess.Popen(resolve(command), start_new_session=not WINDOWS)
    except OSError as error:
        print(f"::endgroup::\nFAIL {label} (could not start command: {error})", flush=True)
        return 127
    def stop(signum, frame):
        terminate(process)
        print(f"INTERRUPTED {label}", flush=True)
        raise SystemExit(128 + signum)
    for signum in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signum, stop)
    try:
        code = process.wait(timeout=seconds)
        verdict = "PASS" if code == 0 else "FAIL"
    except subprocess.TimeoutExpired:
        terminate(process)
        code, verdict = 124, "TIMEOUT"
    elapsed = time.monotonic() - started
    print(f"::endgroup::\n{verdict} {label} ({elapsed:.1f}s, exit {code})", flush=True)
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with Path(summary).open("a") as output:
            output.write(f"- **{verdict}** {label}: {elapsed:.1f}s\n")
    return code if code >= 0 else 128 - code


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--label", required=True)
    parser.add_argument("--timeout", type=float, required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command or args.timeout <= 0:
        parser.error("a command and positive timeout are required")
    sys.exit(run(args.label, args.timeout, command))
