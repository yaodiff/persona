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
from pr_watch_common import WatchError, checked


def load_guard(relative, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
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


def documentation_guard(relative, checkout):
    """Load trusted tests, rebind their repository root, then use unittest discovery."""
    module = load_guard("scripts/test/" + relative, "pr_watch_doc_" + relative[:-3])
    module.ROOT = Path(checkout)
    for related in ("test_doc_paths", "pr_watch_doc_test_doc_paths"):
        if related in sys.modules:
            sys.modules[related].ROOT = Path(checkout)
    loader = unittest.TestLoader()
    result = unittest.TextTestRunner(verbosity=1).run(loader.loadTestsFromModule(module))
    if result.testsRun == 0 or not result.wasSuccessful():
        raise WatchError("documentation guard did not pass: " + relative)


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--worktree", default=str(ROOT))
    parser.add_argument("--repository-guards", action="store_true")
    parser.add_argument("names", nargs="*")
    args = parser.parse_args(argv)
    checkout = Path(args.worktree).resolve()
    if not checkout.is_dir():
        raise WatchError("verification worktree does not exist")
    if args.repository_guards:
        checked(["python3", "-I", str(ROOT / "scripts/privacy_check.py"),
                 "--range", args.base + ".." + args.head], cwd=checkout,
                environ=privacy_environment(checkout))
        for relative in ("test_doc_paths.py", "test_doc_go_paths.py", "test_doc_sections.py", "test_doc_wire.py"):
            documentation_guard(relative, checkout)
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
