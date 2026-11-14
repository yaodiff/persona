#!/usr/bin/env python3
"""Run a `go test` command with the partitioned packages split (scripts/test/test-server.sh on the hub machine).

    python3 go_test_partitioned.py go test -count=1 -p 1 [flags] [packages]

internal/node's and internal/assistant's serial runs outgrew Go's ten-minute -timeout (#5328, #5594, #7644). When the
packages are the whole module (`./...`) or name one of go_partitions.PACKAGES, and no -run narrows the tests, the other
packages run in one `go test` as before and each partitioned package's tests in the partitions go_partitions.py makes
for CI's shards and the release build, all at once. Every command keeps the caller's flags (a deploy's -skip, -v,
-timeout). Anything else runs exactly as given.

Only this process writes the output: the other packages' lines as they come, each partition's whole when it ends, so
lines never mix and `FAIL <package>` / `--- FAIL` read as in a serial run. The exit status is non-zero when any fails.
"""
import os
import subprocess
import sys
import tempfile
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # go_partitions sits beside it; -I leaves it off the path
import go_partitions  # noqa: E402

WHOLE_MODULE = "./..."


def is_package(token):
    """A package argument as this repository writes them: a ./ or ../ path, or the module's import path. Flags and
    their values (`-p 1`, `-skip 'A|B'`, `-coverprofile /tmp/c`) are everything else."""
    return token.startswith(("./", "../")) or token == "." or token.startswith("github.com/")


def split(args):
    """The go test arguments after `go test`: the flags with their values (in order) and the packages."""
    flags = [token for token in args if not is_package(token)]
    packages = [token for token in args if is_package(token)]
    return flags, packages


def narrowed(flags):
    """-run picks the tests already: partitions would only intersect it."""
    return any(token in ("-run", "--run") or token.startswith(("-run=", "--run=")) for token in flags)


def plan(argv, list_packages, list_patterns):
    """The commands to run for argv: [argv] unchanged, or the other packages' command and one per partition.
    list_packages() is the module's packages (`go list ./...`), list_patterns(package) a partitioned package's -run
    patterns."""
    if argv[:2] != ["go", "test"]:
        return [argv]
    flags, packages = split(argv[2:])
    whole = WHOLE_MODULE in packages
    if narrowed(flags) or not (whole or any(map(go_partitions.is_partitioned, packages))):
        return [argv]
    named = [p for p in packages if p != WHOLE_MODULE]
    rest, parts = go_partitions.split((list_packages() if whole else []) + named)
    commands = [["go", "test", *flags, *rest]] if rest else []
    return commands + [["go", "test", *flags, "-run", pattern, package]
                       for package in parts for pattern in list_patterns(package)]


def describe(command):
    """A short line for a command: a partition's -run list is thousands of names."""
    if "-run" in command and command[-1] in go_partitions.PACKAGES:
        pattern = command[command.index("-run") + 1]
        return "%s partition (%d tests)" % (command[-1], pattern.count("|") + 1)
    return "%d package(s)" % sum(map(is_package, command))


def run(commands):
    """Run the commands at once; the first streams, the others print whole when they end. The worst exit status."""
    lock = threading.Lock()

    def emit(text):
        with lock:
            sys.stdout.write(text)
            sys.stdout.flush()

    for command in commands:
        emit("== go test: %s\n" % describe(command))
    first = subprocess.Popen(commands[0], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, errors="replace")
    rest = []
    for command in commands[1:]:
        out = tempfile.TemporaryFile(mode="w+", errors="replace")
        rest.append((command, subprocess.Popen(command, stdout=out, stderr=subprocess.STDOUT, text=True), out))

    def finish(command, process, out):
        status = process.wait()
        out.seek(0)
        emit("== go test: %s, exit %d\n%s" % (describe(command), status, out.read()))
        out.close()

    threads = [threading.Thread(target=finish, args=item) for item in rest]
    for thread in threads:
        thread.start()
    try:
        with first.stdout:
            for line in first.stdout:
                emit(line)
        statuses = [first.wait()]
        for thread in threads:
            thread.join()
    except KeyboardInterrupt:
        for process in [first] + [process for _, process, _ in rest]:
            process.terminate()
        raise
    statuses += [process.returncode for _, process, _ in rest]
    return max(statuses, key=lambda status: status != 0)


def go_list():
    return subprocess.check_output(["go", "list", WHOLE_MODULE], text=True, timeout=600).split()


def main(argv):
    if not argv:
        print("usage: go_test_partitioned.py go test [flags] [packages]", file=sys.stderr)
        return 2
    try:
        commands = plan(argv, go_list, lambda package: go_partitions.list_patterns(os.getcwd(), package))
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, ValueError) as err:
        # A package does not build or list (its error is above): run the command as given, which reports it.
        print("go_test_partitioned: could not split %s (%s); running it whole" % (", ".join(go_partitions.PACKAGES),
                                                                                  err), flush=True)
        commands = [argv]
    if len(commands) == 1:
        return subprocess.call(commands[0])
    return run(commands)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
