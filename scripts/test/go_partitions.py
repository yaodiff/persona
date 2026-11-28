#!/usr/bin/env python3
"""Run the largest server packages' tests in concurrent `go test -run` partitions (docs/operations/testing.md, "Hosted CI").

internal/node runs about 2,400 top-level tests one after another and internal/assistant about 4,100 (almost none call
t.Parallel: most swap a package variable or the environment), so each serial run outgrew Go's ten-minute -timeout and
CI's ten-minute step (#5328, #5594, #7644). CI's shards (scripts/ci/server-shards.py), the release build
(scripts/release/build_hub_release.py) and test-server.sh (go_test_partitioned.py) all split them here, so they cannot
drift. Each partition is a whole `go test` process with an exact `-run '^(A|B|…)$'` list: every listed test runs in
exactly one of them, and subtests follow their parent.
"""
import re
import subprocess

# The packages split into partitions and how many processes share each one's tests; each partition keeps the caller's
# -p and its own -timeout. internal/node has grown beyond two processes: 4,096 tests took about 569 and 572 seconds in
# its two partitions before the unchanged 600-second lane cut both off while their subtests were still advancing.
# Three keeps every test exactly once. internal/assistant took 570 s serially on the hub machine (#7644); its tests
# mostly wait on timers and Postgres. By 2026-11-22 it had about 7,300 tests and three partitions took 420 to 500 s
# each on the CI machine while using about 13% of a core each; with the test database's fsync off
# (scripts/ci/server.sh), eight took at most 165 s, with the same CPU time. internal/node, likewise mostly waiting
# (4,300 tests, about 1,060 s of test time, under a core and a half of CPU): three took 360 s, six 186 s.
# internal/devices (about 580 tests, 260 s in one process, mostly link-switch timers) and internal/extmgr (about 1,040
# tests, 145 s) were the other shard's longest packages once the database stopped syncing; three each took 106 s and
# 59 s. internal/hub (about 2,000 tests) took 283 s in one process beside the other managers' partitions; three took
# 112 s. internal/voice, now internal/modules/voice (about 300 tests, many playing audio in real time) then held the managers shard: 314 s in one
# process for under ten seconds of CPU; four took 97 s. internal/db (about 440 tests, isolated per process via
# dbtest.Main) took 175 s serially in core-other; four partitions bring it in line with devices and extmgr.
PACKAGES = {"./internal/node": 6, "./internal/assistant": 8, "./internal/hub": 3, "./internal/devices": 4,
            "./internal/extmgr": 3, "./internal/modules/voice": 4, "./internal/db": 4}
# The PACKAGES whose plain `go test` (no -run) splits itself: internal/node's TestMain lists its tests and runs them in
# concurrent processes of its own binary, half the CPUs but at least three and at most six
# (server/internal/node/partition_test.go, #5734). go_test_partitioned.py leaves them to that split when it expands a
# `<dir>/...` argument, since on eight CPUs or more it runs more processes than PACKAGES' three. Named on their own or
# under ./... they are partitioned here as before.
SPLITS_ITSELF = {"./internal/node"}
# What `go test -run` selects: tests, examples and fuzz seeds. Benchmarks need -bench and are not listed here.
TEST_NAME = re.compile(r"(?:Test|Example|Fuzz)\w*")
# One argv string must stay well below Linux's 128 KiB limit for a single argument (MAX_ARG_STRLEN).
MAX_PATTERN = 100_000


def names(listing):
    """The runnable names in `go test -list .*` output; the trailing `ok <package>` line and benchmarks are not."""
    return [line.strip() for line in listing.splitlines() if TEST_NAME.fullmatch(line.strip())]


def patterns(test_names, partitions=2, package="the package"):
    """Exact -run patterns, round-robin so the slow tests (listed together by file) spread across partitions."""
    if not test_names:
        raise ValueError("go test -list found no tests in %s: refusing to run an empty partition" % package)
    if len(set(test_names)) != len(test_names):
        raise ValueError("go test -list repeated a test name")
    groups = [test_names[i::partitions] for i in range(partitions)]
    result = ["^(" + "|".join(group) + ")$" for group in groups if group]
    if max(map(len, result)) > MAX_PATTERN:
        raise ValueError("a -run pattern for %s is too long for one argument: raise its partitions" % package)
    return result


def list_patterns(cwd, package, env=None, prefix=(), flags=()):
    """List one partitioned package's tests in the server module at cwd and split them.

    prefix prepends a wrapper command (such as scripts/test/server_test_env.py) and flags forwards build-affecting
    go test flags (such as -tags, -race, -trimpath) so listing warms GOCACHE with the exact same build action ID
    as the partition runs that follow.
    """
    listing = subprocess.check_output([*prefix, "go", "test", *flags, "-list", ".*", package], cwd=cwd, env=env,
                                      text=True, timeout=600)
    return patterns(names(listing), PACKAGES[package], package)


def partitioned(package):
    """The PACKAGES entry an import path or ./ path is (not one of its subpackages), or None."""
    package = package.rstrip("/")
    for name in PACKAGES:
        if package == name or package.endswith(name[1:]):
            return name
    return None


def is_partitioned(package):
    return partitioned(package) is not None


def split(packages):
    """packages without the partitioned ones, and the partitioned ones among them as PACKAGES names (each once, in
    the order given)."""
    rest, parts = [], []
    for package in packages:
        name = partitioned(package)
        if name is None:
            rest.append(package)
        elif name not in parts:
            parts.append(name)
    return rest, parts
