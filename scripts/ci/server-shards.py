#!/usr/bin/env python3
import argparse
import collections
import json
from pathlib import Path
import subprocess
import sys
import threading

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/test"))
import go_partitions  # noqa: E402  (internal/node's and internal/assistant's partitions, shared with the release build)
SHARDS = ("node", "managers", "other")


def shard_for(package):
    for name in ("node", "hub", "voice", "assistant"):
        suffix = "/internal/" + name
        if package.endswith(suffix) or suffix + "/" in package:
            return "node" if name == "node" else "managers"
    return "other"


def select_packages(packages, shard):
    if shard not in SHARDS:
        raise ValueError(f"Unknown server shard: {shard}")
    return [package for package in packages if shard_for(package) == shard]


def commands(packages, shard, database_file, partitions=None):
    """The go test processes a shard runs at once: one for its other packages, and one more per partition of each
    partitioned package it has (go_partitions: internal/node in the node shard, internal/assistant in the managers
    shard; the same split as the release build). partitions maps such a package to its -run patterns. Each process
    keeps -p and -timeout."""
    workers = {"node": 1, "managers": 3, "other": 2}[shard]
    prefix = [sys.executable, str(ROOT / "scripts/test/server_test_env.py"), "--database-file", database_file,
              "--", "go", "test", "-json", "-p", str(workers), "-count=1", "-timeout=10m"]
    rest, parts = go_partitions.split(packages)
    if not parts:
        return [prefix + packages]
    partitions = partitions or {}
    for package in parts:
        if not partitions.get(package):
            raise ValueError(f"{package} needs its partitions")
    return ([prefix + rest] if rest else []) + [prefix + ["-run", pattern, package]
                                                 for package in parts for pattern in partitions[package]]


class Results:
    """What a shard's go test -json events add up to: the slowest tests, and each failed test with the first lines it
    printed. Among thousands of passing tests' output a failure is easy to lose, all the more in a report that keeps
    only its head and tail (the PR watcher's), so the shard repeats the failures last."""

    PACKAGE_LINES = 20  # a package that fails outside its tests (build, panic, timeout): its last lines

    def __init__(self):
        self.slow, self.failed, self.output, self.package_output = [], [], {}, {}

    def take(self, event, process=0):
        """Record one event of a go test process (a shard runs a partitioned package in several); return the text to
        print, if it has any."""
        package, test, action = event.get("Package", ""), event.get("Test"), event.get("Action")
        if action == "output":
            text = event.get("Output", "")
            if test:
                self.output.setdefault((process, package, test), []).append(text)
            else:
                lines = self.package_output.setdefault((process, package), collections.deque(maxlen=self.PACKAGE_LINES))
                lines.append(text)
            return text
        if action in ("pass", "fail", "skip") and test:
            printed = self.output.pop((process, package, test), [])
            if action != "skip":
                self.slow.append((event.get("Elapsed", 0), package, test))
            if action == "fail":
                self.failed.append((package, test, event.get("Elapsed", 0), printed))
        elif action == "fail":
            # A panic or timeout ends the package inside the tests still running, which never end on their own.
            for name in [name for name in self.output if name[:2] == (process, package)]:
                self.failed.append((package, name[2], None, self.output.pop(name)))
            self.failed.append((package, None, event.get("Elapsed", 0), list(self.package_output.get((process, package), ()))))
        return None

    def failures(self, tests=5, lines=5, width=300):
        """Each failed test and the first lines it printed (from its panic, if it has one); a parent failed only by
        its subtests is left out, and a package that failed outside its tests shows its last lines."""
        names = {(package, test) for package, test, _, _ in self.failed if test}
        blocks = []
        for package, test, elapsed, printed in self.failed:
            own = [line.rstrip()[:width] for line in printed if line.strip()]
            if test is None:
                if any(name[0] == package for name in names):
                    continue
                blocks.append([f"--- FAIL: {package} ({elapsed:.2f}s), outside any test; its last output:"] + own[-lines:])
                continue
            own = [line for line in own if not line.lstrip().startswith(("=== ", "--- "))]
            if not own and any(name[0] == package and name[1].startswith(test + "/") for name in names):
                continue
            panic = next((i for i, line in enumerate(own) if line.startswith("panic: ")), 0)
            took = "did not finish" if elapsed is None else f"{elapsed:.2f}s"
            blocks.append([f"--- FAIL: {test} ({package}, {took})"] + own[panic:panic + lines])
        summary = [line for block in blocks[:tests] for line in block]
        if len(blocks) > tests:
            summary.append(f"... and {len(blocks) - tests} more failed")
        return summary

    def report(self, shard, packages, runs, status):
        lines = [f"Server shard {shard}: {packages} packages in {runs} go test processes; exit {status}",
                 "Slowest test events (parent/subtest durations may overlap):"]
        lines += [f"SLOW {elapsed:.3f}s {package} {test}" for elapsed, package, test in sorted(self.slow, reverse=True)[:20]]
        failed = self.failures()
        if failed:
            lines += ["Failed tests (repeated from above with their first lines):"] + failed
        return lines


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("shard", choices=SHARDS)
    parser.add_argument("--database-file", required=True)
    args = parser.parse_args()
    listed = subprocess.check_output(["go", "list", "./..."], cwd=ROOT / "server", text=True, timeout=120)
    packages = select_packages(listed.splitlines(), args.shard)
    if not packages:
        parser.error(f"No packages in server shard {args.shard}")
    partitions = {package: go_partitions.list_patterns(ROOT / "server", package)
                  for package in go_partitions.split(packages)[1]}
    runs = commands(packages, args.shard, args.database_file, partitions)
    results, lock = Results(), threading.Lock()

    def follow(index, process):
        for line in process.stdout:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                with lock:
                    print(line, end="", flush=True)
                continue
            with lock:
                text = results.take(event, index)
                if text is not None:
                    print(text, end="", flush=True)

    processes = [subprocess.Popen(command, cwd=ROOT / "server", stdout=subprocess.PIPE, text=True) for command in runs]
    readers = [threading.Thread(target=follow, args=(index, process)) for index, process in enumerate(processes)]
    try:
        for reader in readers:
            reader.start()
        for reader in readers:
            reader.join()
        status = next((code for code in [process.wait() for process in processes] if code), 0)
    finally:
        for process in processes:
            process.stdout.close()
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=10)
    print("\n".join(results.report(args.shard, len(packages), len(runs), status)))
    return status


if __name__ == "__main__":
    sys.exit(main())
