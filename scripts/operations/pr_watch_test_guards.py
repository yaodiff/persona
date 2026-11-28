"""Trusted, read-only guards for a test-file-only watcher verification."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from pr_watch_common import SHA, WatchError, checked


def load_guard(relative, name, root=None):
    source_root = ROOT if root is None else Path(root)
    spec = importlib.util.spec_from_file_location(name, source_root / relative)
    if spec is None or spec.loader is None:
        raise WatchError("cannot load trusted guard: " + relative)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def baseline_guard(name, checkout):
    """Reject a changed test-file baseline violation without rescanning unrelated files."""
    if not (checkout / name).is_file():
        return []
    if name.startswith("web/src/"):
        module = load_guard("scripts/ci/web_font_ratchet.py", "pr_watch_font_guard")
        if module.is_test(name):
            return []
        actual = module.count_sizes((checkout / name).read_text(encoding="utf-8"))
    elif name.startswith(("server/", "sdk/go/")) and name.endswith("_test.go"):
        module = load_guard("scripts/ci/sleep_ratchet.py", "pr_watch_sleep_guard")
        actual = module.count_sleeps((checkout / name).read_text(encoding="utf-8"))
    else:
        return []
    baseline = module.parse_baseline((checkout / module.BASELINE_REL).read_text(encoding="utf-8"),
                                     module.BASELINE_REL)
    previous = checked(["git", "show", "HEAD:" + module.BASELINE_REL], cwd=checkout)
    if json.loads(previous) != baseline:
        raise WatchError("test-file baseline differs between the index and worktree")
    violations = module.violations if name.startswith("web/src/") else module.count_violations
    return violations({name: actual}, {name: baseline.get(name, 0)})


def privacy_environment(checkout):
    """Keep maintainer config locations, but never take them from the PR checkout."""
    env = {key: value for key, value in os.environ.items()
           if key in ("PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TERM", "HOME", "XDG_CONFIG_HOME")}
    with tempfile.TemporaryDirectory(prefix="pr-watch-scanner-home-") as home:
        env["HOME"] = home
        scanner = load_guard("scripts/privacy_check.py", "pr_watch_privacy_config")
        for name in scanner.CONFIG_ENV:
            if name in os.environ:
                env[name] = os.environ[name]
    env.update({"PERSONA_PRIVACY_ROOT": str(checkout), "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_CONFIG_GLOBAL": os.devnull})
    return env


def documentation_guard(relative, checkout, base):
    """Run the exact base's trusted tests against the PR's files, never the PR's Python."""
    original_path = sys.path.copy()
    try:
        with tempfile.TemporaryDirectory(prefix="pr-watch-base-guards-") as directory:
            source_root = Path(directory)
            test_root = source_root / "scripts/test"
            test_root.mkdir(parents=True)
            for name in sorted({"test_doc_paths.py", relative}):
                source = checked(["git", "show", base + ":scripts/test/" + name], cwd=checkout)
                (test_root / name).write_text(source, encoding="utf-8")
            if relative == "test_doc_sections.py":
                known = checked(["git", "show", base + ":scripts/test/doc_sections_known.txt"], cwd=checkout)
                (test_root / "doc_sections_known.txt").write_text(known, encoding="utf-8")

            common = load_guard("scripts/test/test_doc_paths.py", "test_doc_paths", source_root)
            module = common if relative == "test_doc_paths.py" else load_guard(
                "scripts/test/" + relative, "pr_watch_doc_" + relative[:-3], source_root)
            common.ROOT = Path(checkout)
            module.ROOT = Path(checkout)
            if relative == "test_doc_sections.py":
                # The base test formats KNOWN relative to ROOT. Keep that lexical relationship while the path resolves
                # through .. to the temporary base copy, never to the PR's own data file.
                module.KNOWN = Path(checkout) / os.path.relpath(test_root / "doc_sections_known.txt", checkout)
            loader = unittest.TestLoader()
            result = unittest.TextTestRunner(verbosity=1).run(loader.loadTestsFromModule(module))
    finally:
        sys.path[:] = original_path
    if result.testsRun == 0 or not result.wasSuccessful():
        raise WatchError("documentation guard did not pass: " + relative)


def extension_documentation_guards(checkout, base):
    """persona-extensions' own documentation gates (its scripts/ci.sh runs check-docs.py and test-docs.py).

    Like documentation_guard, the exact base's copies run against the PR's files; the PR's Python is never loaded.
    """
    original_path = sys.path.copy()
    try:
        with tempfile.TemporaryDirectory(prefix="pr-watch-base-guards-") as directory:
            source_root = Path(directory)
            (source_root / "scripts").mkdir()
            for name in ("check-docs.py", "test-docs.py"):
                source = checked(["git", "show", base + ":scripts/" + name], cwd=checkout)
                (source_root / "scripts" / name).write_text(source, encoding="utf-8")
            # test-docs.py loads its sibling check-docs.py, so it tests the base checker on synthetic files only.
            tests = load_guard("scripts/test-docs.py", "pr_watch_extension_test_docs", source_root)
            result = unittest.TextTestRunner(verbosity=1).run(unittest.TestLoader().loadTestsFromModule(tests))
            if result.testsRun == 0 or not result.wasSuccessful():
                raise WatchError("documentation guard did not pass: scripts/test-docs.py")
            checker = load_guard("scripts/check-docs.py", "pr_watch_extension_check_docs", source_root)
            checker.ROOT = Path(checkout)
            if checker.main():
                raise WatchError("documentation guard did not pass: scripts/check-docs.py")
    finally:
        sys.path[:] = original_path


def validate_base_commit(base, checkout):
    """Require the exact base object to be an available commit before guarding a PR."""
    if not SHA.fullmatch(base):
        raise WatchError("base must be exactly 40 lowercase hexadecimal characters")
    try:
        object_type = checked(["git", "cat-file", "-t", base], cwd=checkout).strip()
    except WatchError as error:
        raise WatchError("base must name an available commit") from error
    if object_type != "commit":
        raise WatchError("base must name an available commit")


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--worktree", default=str(ROOT))
    parser.add_argument("--repository-guards", action="store_true")
    # Each repository keeps its own documentation gates; persona-extensions has no scripts/test/test_doc_*.py.
    parser.add_argument("--repository", choices=("nwverse/persona", "nwverse/persona-extensions"),
                        default="nwverse/persona")
    parser.add_argument("names", nargs="*")
    args = parser.parse_args(argv)
    checkout = Path(args.worktree).resolve()
    if not checkout.is_dir():
        raise WatchError("verification worktree does not exist")
    validate_base_commit(args.base, checkout)
    if args.repository_guards:
        checked(["python3", "-I", str(ROOT / "scripts/privacy_check.py"),
                 "--range", args.base + ".." + args.head], cwd=checkout,
                environ=privacy_environment(checkout))
        if args.repository == "nwverse/persona-extensions":
            extension_documentation_guards(checkout, args.base)
            return 0
        for relative in ("test_doc_paths.py", "test_doc_go_paths.py", "test_doc_sections.py", "test_doc_wire.py"):
            documentation_guard(relative, checkout, args.base)
        return 0
    violations = []
    for name in args.names:
        path = Path(name)
        if path.is_absolute() or ".." in path.parts:
            raise WatchError("changed test path must stay within the PR worktree")
        if (checkout / path).exists() and not (checkout / path).is_file():
            raise WatchError("changed test file is missing or not regular: " + name)
        violations.extend(baseline_guard(name, checkout))
    if violations:
        raise WatchError("\n".join(violations))
    print("Changed test-file baseline guards passed: %d file(s)" % len(args.names))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except WatchError as error:
        sys.exit(str(error))
