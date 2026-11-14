"""Check gofmt only for changed Go files, without executing PR Python helpers."""
import argparse
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pr_watch_common import WatchError, checked


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--worktree", required=True)
    parser.add_argument("names", nargs="+")
    args = parser.parse_args(argv)
    root = Path(args.worktree).resolve()
    for name in args.names:
        path = Path(name)
        if path.is_absolute() or ".." in path.parts or not name.endswith(".go"):
            raise WatchError("Go formatting expects a repository-relative Go file")
        if not (root / path).is_file():
            continue
        result = checked(["gofmt", "-l", str(root / path)], cwd=root, timeout=30)
        if result:
            raise WatchError("Go test file is not gofmt formatted: " + name)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except WatchError as error:
        raise SystemExit(str(error))
