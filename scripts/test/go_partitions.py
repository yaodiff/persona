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
# -p and its own -timeout. Two for internal/node keeps CI's four-CPU runners, which also drive a real browser there,
# from contending much. internal/assistant took 570 s serially on the hub machine (#7644); its tests mostly wait on
# timers and Postgres, and two partitions still took 244 and 291 s side by side, three about 150 to 200 s each.
PACKAGES = {"./internal/node": 2, "./internal/assistant": 3}
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


def list_patterns(cwd, package, env=None):
    """List one partitioned package's tests in the server module at cwd and split them."""
    listing = subprocess.check_output(["go", "test", "-list", ".*", package], cwd=cwd, env=env, text=True,
                                      timeout=600)
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
