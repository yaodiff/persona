#!/usr/bin/env python3
import argparse
import concurrent.futures
import collections
import json
from pathlib import Path
import re
import subprocess
import sys
import threading

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/test"))
import go_partitions  # noqa: E402  (the partitioned packages' split, shared with the release build)
SHARDS = ("node", "managers", "other")
# scripts/ci/affected.sh: the packages a change affects (--packages-file), not a fixed shard.
AFFECTED = "affected"


def shard_for(package):
    # modules/voice: the voice module (#13431), in the managers shard as internal/voice was.
    for name in ("node", "hub", "voice", "modules/voice", "assistant"):
        suffix = "/internal/" + name
        if package.endswith(suffix) or suffix + "/" in package:
            return "node" if name == "node" else "managers"
    return "other"


def select_packages(packages, shard):
    if shard not in SHARDS:
        raise ValueError(f"Unknown server shard: {shard}")
    return [package for package in packages if shard_for(package) == shard]


def server_test_env(database_file):
    """The server_test_env.py wrapper shared by partition listing and partition go test runs so both use the exact
    same cleaned environment and GOFLAGS (including -trimpath) and share GOCACHE (#14633)."""
    return [sys.executable, str(ROOT / "scripts/test/server_test_env.py"), "--database-file", database_file, "--"]


def list_patterns(package, database_file):
    """List one partitioned package's tests through server_test_env.py so GOCACHE is warmed with the matching build
    action ID before the shard fans out its partition workers."""
    return go_partitions.list_patterns(ROOT / "server", package, prefix=server_test_env(database_file))


def warm_packages(packages, shard, database_file):
    """Pre-compile a large non-partitioned package set with go test -list ^$ through server_test_env.py so
    cold-GOCACHE runs do not duplicate dependency builds or thrash parallel compilers across concurrent rest batches."""
    workers = {"node": 1, "managers": 3, "other": 4, AFFECTED: 3}[shard]
    subprocess.run(
        server_test_env(database_file) + ["go", "test", "-p", str(workers), "-list", "^$", *packages],
        cwd=ROOT / "server", stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False, timeout=600,
    )


def go_test(shard, database_file):
    """The go test command line a shard's processes start with, before their packages."""
    workers = {"node": 1, "managers": 3, "other": 4, AFFECTED: 3}[shard]
    return [sys.executable, str(ROOT / "scripts/test/server_test_env.py"), "--database-file", database_file,
            "--", "go", "test", "-json", "-p", str(workers), "-count=1", "-timeout=10m"]


REST_BATCHES = 4
REST_BATCH_MIN = 24


def rest_batches(packages):
    """Split a large non-partitioned package list round-robin so slow rest packages run across concurrent go test
    processes instead of serializing behind one -p queue."""
    if not packages:
        return []
    if len(packages) < REST_BATCH_MIN:
        return [packages]
    return [packages[i::REST_BATCHES] for i in range(REST_BATCHES)]


def commands(packages, shard, database_file, partitions=None):
    """The go test processes a shard runs at once: one or more batches for its other packages, and one more per
    partition of each partitioned package it has (go_partitions: internal/node in the node shard, internal/assistant
    and internal/hub in the managers shard, internal/devices, internal/extmgr and internal/db in the other shard; the
    same split as the release build). partitions maps such a package to its -run patterns. Each process keeps -p and
    -timeout."""
    prefix = go_test(shard, database_file)
    rest, parts = go_partitions.split(packages)
    if not parts:
        return [prefix + batch for batch in rest_batches(packages)]
    partitions = partitions or {}
    for package in parts:
        if not partitions.get(package):
            raise ValueError(f"{package} needs its partitions")
    return [prefix + batch for batch in rest_batches(rest)] + [
        prefix + ["-run", pattern, package]
        for package in parts for pattern in partitions[package]
    ]


class Results:
    """What a shard's go test -json events add up to: the slowest tests, and each failed test with the first lines it
    printed. Among thousands of passing tests' output a failure is easy to lose, all the more in a report that keeps
    only its head and tail (the PR watcher's), so the shard repeats the failures last."""

    PACKAGE_LINES = 20  # a package that fails outside its tests (build, panic, timeout): its last lines

    def __init__(self):
        self.slow, self.failed, self.output, self.package_output, self.build_output = [], [], {}, {}, {}

    def take(self, event, process=0):
        """Record one event of a go test process (a shard runs a partitioned package in several); return the text to
        print, if it has any."""
        package, test, action = event.get("Package", ""), event.get("Test"), event.get("Action")
        if action == "build-output":
            # Since Go 1.24 a package that does not compile (or fails go test's vet) reports its errors only as
            # build-output events. Dropping them made such a shard exit in seconds with no error text.
            text = event.get("Output", "")
            self.build_output.setdefault(event.get("ImportPath", ""), []).append(text)
            return text
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
            lines = list(self.package_output.get((process, package), ()))
            if event.get("FailedBuild"):
                # The package's fail event names the build that failed; its errors are that build's output.
                lines = self.build_output.pop(event["FailedBuild"], [])[-self.PACKAGE_LINES:] + lines
            self.failed.append((package, None, event.get("Elapsed", 0), lines))
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


def guard_lists(check_fast=ROOT / "scripts/test/check-fast.sh"):
    """check-fast's repo-wide guards (#12842): the packages that run whole and (packages, -run pattern) for the guard
    tests of the others. One list for both, read from check-fast.sh, which scripts/test/test_check_fast_guards.py
    keeps in step with the tests. Only read: nothing here runs check-fast or takes a heavy-build slot, so there is no
    PERSONA_HEAVY_SLOTS=0 to set."""
    text = check_fast.read_text()

    def array(name):
        body = re.search(name + r"=\(([^)]*)\)", text).group(1)
        return [word for word in body.split() if word.startswith("./")]
    pattern = "".join(re.findall(r"'([^']*)'", re.search(r"GO_GUARD_TESTS=((?:'[^']*'\\?\n?)+)", text).group(1)))
    return array("GO_GUARD_PKGS"), array("GO_GUARD_TEST_PKGS"), pattern


def affected_commands(packages, database_file, partitions, guards):
    """affected.sh's go test processes: the affected packages as commands() runs a shard, then the guard packages
    that are not among them, whole, and the guard tests of the guard test packages that are not."""
    runs = commands(packages, AFFECTED, database_file, partitions) if packages else []
    if guards:
        whole, tested, pattern = guard_lists()
        prefix = go_test(AFFECTED, database_file)
        listed = set(packages)
        module = "github.com/nwverse/persona/server/"
        whole = [p for p in whole if module + p[2:] not in listed]
        tested = [p for p in tested if module + p[2:] not in listed]
        runs += [prefix + whole] if whole else []
        runs += [prefix + ["-run", pattern] + tested] if tested else []
    return runs


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("shard", choices=SHARDS + (AFFECTED,))
    parser.add_argument("--database-file", required=True)
    parser.add_argument("--packages-file", help="affected: the import paths to test, one per line")
    parser.add_argument("--guards", action="store_true", help="affected: also run check-fast's repo-wide guards")
    args = parser.parse_args()
    if args.shard == AFFECTED:
        if not args.packages_file:
            parser.error("affected needs --packages-file")
        packages = [line.strip() for line in Path(args.packages_file).read_text().splitlines() if line.strip()]
        if not packages and not args.guards:
            print("Server affected: no Go package to test", flush=True)
            return 0
    else:
        listed = subprocess.check_output(["go", "list", "./..."], cwd=ROOT / "server", text=True, timeout=120)
        packages = select_packages(listed.splitlines(), args.shard)
    if not packages and args.shard != AFFECTED:
        parser.error(f"No packages in server shard {args.shard}")
    rest, listed = go_partitions.split(packages)
    if len(rest) >= REST_BATCH_MIN:
        warm_packages(packages, args.shard, args.database_file)
    try:
        # Listing compiles each partitioned package's tests; after a change most of them compile from scratch, so they
        # are listed at once rather than one after another (an affected run reaching five of them: 161 s in turn, 113 s
        # at once, on a busy CI machine).
        with concurrent.futures.ThreadPoolExecutor(max_workers=max(len(listed), 1)) as pool:
            patterns = list(pool.map(lambda package: list_patterns(package, args.database_file), listed))
        partitions = dict(zip(listed, patterns))
    except subprocess.CalledProcessError as error:
        # Listing a partitioned package's tests compiles it; a compile error ends the shard here, before any test.
        print(f"Server shard {args.shard}: could not list the tests of a partitioned package "
              f"(go test -list exit {error.returncode}); its build errors are printed above.", flush=True)
        return error.returncode or 1
    if args.shard == AFFECTED:
        runs = affected_commands(packages, args.database_file, partitions, args.guards)
    else:
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
