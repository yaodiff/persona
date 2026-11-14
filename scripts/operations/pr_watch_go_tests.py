"""Run selected Go test packages with the same disposable Postgres as server CI."""
import argparse
from pathlib import Path
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from pr_watch_common import WatchError, checked, run
from scripts.test import go_partitions


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--worktree", required=True)
    parser.add_argument("packages", nargs="+")
    args = parser.parse_args(argv)
    root = Path(args.worktree).resolve()
    trusted = Path(__file__).resolve().parents[2]
    for package in args.packages:
        if not package.startswith("./") or ".." in Path(package).parts:
            raise WatchError("Go test package must be a repository-relative package")
    try:
        with tempfile.TemporaryDirectory(prefix="pr-watch-go-db-") as temporary:
            fixture = Path(temporary)
            container = "pr-watch-db-" + fixture.name
            try:
                checked(["docker", "run", "--detach", "--name", container,
                         "-e", "POSTGRES_USER=persona", "-e", "POSTGRES_PASSWORD=ci-only",
                         "-e", "POSTGRES_DB=persona_test", "-p", "127.0.0.1::5432",
                         "postgres:18-alpine"], cwd=root, timeout=180)
                checked(["python3", "-I", str(trusted / "scripts/ci/postgres.py"), container,
                         str(fixture / "database.json")], cwd=root, timeout=90)
                rest, partitioned = go_partitions.split(args.packages)
                commands = []
                if rest:
                    for package in rest:
                        if not list((root / "server" / package[2:]).glob("*_test.go")):
                            raise WatchError("no selected Go tests remain in " + package)
                    commands.append(["go", "test", "-count=1", "-p", "1", "-timeout=10m"] + rest)
                for package in partitioned:
                    listing = run(["python3", "-I", str(trusted / "scripts/test/server_test_env.py"),
                                   "--database-file", str(fixture / "database.json"), "--",
                                   "go", "test", "-list", ".*", package], cwd=root / "server", timeout=600)
                    if listing.returncode:
                        raise WatchError("cannot list partitioned Go tests: " + listing.stderr[-1500:])
                    patterns = go_partitions.patterns(go_partitions.names(listing.stdout),
                                                      go_partitions.PACKAGES[package], package)
                    commands.extend(["go", "test", "-count=1", "-p", "1", "-timeout=10m", "-run", pattern, package]
                                    for pattern in patterns)
                for command in commands:
                    checked(["python3", "-I", str(trusted / "scripts/test/server_test_env.py"),
                             "--database-file", str(fixture / "database.json"), "--"] + command,
                            cwd=root / "server", timeout=720)
            finally:
                subprocess.run(["docker", "rm", "-f", container], cwd=root,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=60, check=False)
    except ValueError as error:
        raise WatchError("cannot select Go tests: " + str(error)) from error
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except WatchError as error:
        raise SystemExit(str(error))
