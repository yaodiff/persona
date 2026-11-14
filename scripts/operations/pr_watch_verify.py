"""Repository-specific local verification, selected from the PR's changed paths."""
import importlib.util
import os
import re
import sys
import uuid
from pathlib import Path

from pr_watch_common import VerificationError, WatchError, bounded, checked, run, test_or_fix, verification_env


def command_with_prefix(command, arguments):
    if isinstance(command, str):
        command = [command]
    if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
        raise WatchError("verification command must be an executable or argv prefix")
    return command + arguments


def result_summary(result):
    """Keep terminal diagnostics and every explicit failed test, not passing noise."""
    output = (result.stdout + "\n" + result.stderr).strip()
    lines = output.splitlines()
    selected = set()
    marker = re.compile(r"(?:^--- FAIL:|^FAIL(?:\s|\t)|^FAILED(?:\s|:)|^\s*(?:AssertionError|ERROR):|"
                        r"^panic:|^Traceback|^FAIL\s+\S|^(?:fatal|time out|command not found)\b|"
                        r"\b(?:error|failed|panic|timeout)\b|\d+ tests? failed)", re.I)
    generic = re.compile(r"\b(?:error|failed|panic|timeout)\b|\d+ tests? failed", re.I)
    precise = re.compile(r"(?:^--- FAIL:|^FAIL(?:\s|\t)|^FAILED(?:\s|:)|^\s*(?:AssertionError|ERROR):|"
                         r"^panic:|^Traceback|^FAIL\s+\S|^(?:fatal|time out|command not found)\b)", re.I)
    for index, line in enumerate(lines):
        if marker.search(line) and not (line.startswith(("PASS", "ok ")) and generic.search(line) and not precise.search(line)):
            selected.update(range(max(0, index - 4), min(len(lines), index + 6)))
    if selected:
        evidence = "\n".join(line for index, line in enumerate(lines) if index in selected)
    else:
        # A nonzero command can explain itself without using a familiar keyword.
        evidence = output
    # Preserve the beginning for context and, especially, the shard's final failed-test summary.
    return bounded(evidence, 12000)


def run_check(name, argv, worktree, timeout, environ=None, cwd=None):
    result = run(argv, cwd=cwd or worktree, timeout=timeout, environ=environ)
    return {"name": name, "command": argv, "ok": result.returncode == 0,
            "summary": "" if result.returncode == 0 else result_summary(result),
            "stdout": bounded(result.stdout, 24000), "stderr": bounded(result.stderr, 24000)}


def load_paths(worktree):
    """Load the PR's selector, not a stale module imported by another worker."""
    name = "pr_watch_paths_" + uuid.uuid4().hex
    spec = importlib.util.spec_from_file_location(name, Path(worktree) / "scripts" / "ci" / "paths.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def external_check(command, pr, worktree, name):
    """A trusted local adapter may supply otherwise unavailable platform evidence."""
    if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
        raise WatchError(name + " must be an argv array")
    replacements = {"worktree": str(worktree), "head": pr["headRefOid"],
                    "base": pr["baseRefOid"], "number": str(pr["number"])}
    argv = []
    for arg in command:
        for key, value in replacements.items():
            arg = arg.replace("{" + key + "}", value)
        argv.append(arg)
    return name, argv


# Tests do not change the interface they exercise. Production source outside
# the known UI paths must still be read; SwiftUI in any file stays gated.
APPLE_NON_UI_PATHS = ("apple/",)
APPLE_VISUAL_PATHS = (
    "apple/Packages/BeautifulUI/",
    "apple/Shared/Design/",
    "apple/Shared/Localization/",
    "apple/Persona/DesignSystem/",
    "apple/Persona/Views/",
    "apple/Persona/Resources/",
    "apple/PersonaWidgets/",
    "apple/PersonaWatch/",
    "apple/LiveActivity/",
    "apple/Vision/",
)
APPLE_VISUAL_MARKERS = re.compile(
    r"\b(?:SwiftUI|UIKit|AppKit|UIView|NSView|View|ViewModifier|ViewBuilder|"
    r"NavigationStack|NavigationSplitView|TabView|GeometryReader|"
    r"Image|Color|Font|Theme|StyleSpec|ComponentSpec|ColorSpec|AppStyle|"
    r"localized|String\(localized|Text\s*\(|Button\s*\(|Label\s*\(|"
    r"XCUIApplication|XCUITest)\b|@(?:MainActor|State|Binding|ObservedObject)\b|"
    r"\.(?:frame|padding|font|foregroundStyle|background|sheet|fullScreenCover|"
    r"foregroundColor|opacity|offset|position|lineLimit|minimumScaleFactor)\s*\(")


APPLE_NON_UI_SHARED = (
    "schema/",
    "spec/",
    "scripts/ci/",
    "scripts/app/",
    "docs/",
)
APPLE_PORTABLE_CORE = (
    "native/app-core/crates/persona-core-runtime/",
    "native/app-core/crates/persona-core-ohos/",
)
APPLE_ANDROID_SHARED = (
    "android/core/rustcore/",
)


def non_ui_apple_change(worktree, base, head, names):
    """Shared backend/schema changes can use the Linux non-visual policy too."""
    if not names or not all(name.startswith(APPLE_NON_UI_PATHS + APPLE_NON_UI_SHARED +
                                          APPLE_PORTABLE_CORE + APPLE_ANDROID_SHARED) for name in names):
        return False
    for path in names:
        if path == "native/app-core/crates/persona-core-runtime/src/wire.rs":
            return False
        if not path.startswith("apple/"):
            continue
        parts = path.split("/")
        test_path = (len(parts) >= 2 and parts[1] in ("PersonaTests", "PersonaUITests")) or (
            len(parts) >= 5 and parts[1] == "Packages" and parts[3] == "Tests") or (
            len(parts) >= 4 and parts[1] == "PersonaLink" and parts[2] == "Tests")
        if path.startswith(APPLE_VISUAL_PATHS) and not test_path:
            return False
        if "/Assets" in path or "/Design" in path or "/Localization" in path:
            return False
        if path.endswith((".xcstrings", ".strings", ".stringsdict", ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".svg")):
            return False
        # A UI test's own imports and assertions do not change the interface.
        if not test_path and not path.endswith(".swift"):
            return False
        if path.endswith(".swift") and not test_path:
            diff = checked(["git", "diff", "--unified=0", "--no-renames", base + "..." + head, "--", path], cwd=worktree)
            changed = [line[1:] for line in diff.splitlines()
                       if line.startswith(("+", "-")) and not line.startswith(("+++", "---"))]
            if any(APPLE_VISUAL_MARKERS.search(line) for line in changed):
                return False
    return True


def ui_change(names):
    for path in names:
        if path.startswith(("apple/", "web/src/", "android/", "harmony/")):
            if "/test" not in path and "/docs/" not in path and not path.endswith((".md", ".xcstrings", "strings.xml")):
                return True
    return False


def apple_test_file(name):
    parts = name.split("/")
    return ((len(parts) > 1 and parts[1] in ("PersonaTests", "PersonaUITests")) or
            (len(parts) >= 5 and parts[1] == "Packages" and parts[3] == "Tests") or
            (len(parts) >= 4 and parts[1] == "PersonaLink" and parts[2] == "Tests"))


def python_test_suite(worktree, name):
    """Match CI's unittest-content predicate, not merely the test_ filename."""
    path = Path(worktree, name)
    return path.is_file() and "unittest" in path.read_text(encoding="utf-8", errors="replace")


def scoped_test_checks(worktree, base, head, names):
    """Run changed test files, not every suite, for a test-file-only PR."""
    groups = {
        "web": [name for name in names if name.startswith("web/src/") and
                name.endswith((".test.ts", ".test.tsx", ".test.js", ".test.mjs"))],
        "go": [name for name in names if name.startswith("server/") and name.endswith("_test.go")],
        "python": [name for name in names if name.startswith("scripts/") and
                   Path(name).name.startswith("test_") and name.endswith(".py") and
                   python_test_suite(worktree, name)],
        "apple": [name for name in names if name.startswith("apple/") and apple_test_file(name)],
    }
    if sorted(groups["web"] + groups["go"] + groups["python"] + groups["apple"]) != sorted(names):
        return None
    commands = []
    if groups["web"]:
        runnable = [name for name in groups["web"] if (Path(worktree) / name).is_file()]
        commands.append(("Web dependencies", ["npm", "ci", "--no-audit", "--no-fund"], "web"))
        commands.append(("Web extension mirror cleanup tests",
                         ["node", "--test", "e2e/extension-mirrors-cleanup.node-test.mjs"], "web"))
        if runnable:
            commands.append(("Changed web tests", ["node", "./node_modules/vitest/vitest.mjs", "run"] +
                             [name[len("web/"):] for name in runnable], "web"))
        commands.append(("Web typecheck", ["npm", "run", "typecheck"], "web"))
        commands.append(("Web lint", ["npm", "run", "lint"], "web"))
        commands.append(("Web build", ["npm", "run", "build"], "web"))
    if groups["go"]:
        packages = sorted({"./" + str(Path(name).parent.relative_to("server")) for name in groups["go"]})
        runner = str(Path(__file__).resolve().parent / "pr_watch_go_tests.py")
        commands.append(("Changed Go tests", ["python3", "-I", runner, "--worktree", worktree] + packages))
        commands.append(("Changed Go packages vet", ["go", "-C", "server", "vet"] + packages))
        commands.append(("Changed Go formatting", ["python3", "-I", str(Path(__file__).resolve().parent / "pr_watch_go_format.py"),
                                                   "--worktree", worktree] + groups["go"]))
    if groups["python"]:
        for name in groups["python"]:
            relative = str(Path(name).parent)
            commands.append(("Changed Python tests", ["python3", "-I", "-B", "-m", "unittest", Path(name).name], relative))
    if groups["apple"]:
        # Test-only Apple changes retain semantic review and portable guards;
        # this host does not claim a Swift build, simulator or visual acceptance.
        commands.append(("Apple test-only review policy", ["true"]))
    guard = str(Path(__file__).resolve().parent / "pr_watch_test_guards.py")
    commands.extend([
        ("Changed test-file baseline guards", ["python3", "-I", guard, "--base", base, "--head", head,
                                               "--worktree", worktree] + names),
        ("Privacy and documentation guards", ["python3", "-I", guard,
                                               "--base", base, "--head", head, "--worktree", worktree, "--repository-guards"]),
    ])
    return commands


def persona_checks(worktree, pr, timeout, config):
    """Use trusted watcher/base path mapping, never import PR-controlled Python."""
    base, head = pr["baseRefOid"], pr["headRefOid"]
    names = checked(["git", "diff", "--name-only", "--no-renames", base + "..." + head], cwd=worktree).splitlines()
    selected = load_paths(Path(__file__).resolve().parents[2]).select(names)
    commands = []
    focused = scoped_test_checks(worktree, base, head, names) if test_or_fix(pr, names) else None
    if focused is not None:
        commands.extend(focused)
        selected["web"] = False
        selected["scripts"] = False
        if any(name.startswith("server/") for name in names):
            selected["server"] = False
    else:
        # CI's wildcard scripts lane is a CI-selection requirement, not a
        # reason to run every Python suite for an unrelated production file.
        # Keep repository/privacy gates, but do not multiply that cost per PR.
        selected["scripts"] = any(name.startswith(("scripts/", ".github/")) for name in names)
    for lane in ("server", "web", "android", "runtimes", "scripts", "core", "desktop"):
        if selected.get(lane):
            commands.append(("CI " + lane, ["bash", "scripts/ci/" + lane + ".sh"]))
    if focused is None:
        scanner = Path(__file__).resolve().parents[2] / "scripts/privacy_check.py"
        commands.append(("Exact-head privacy scan", ["python3", "-I", str(scanner),
                                                    "--range", base + ".." + head]))
    if selected.get("apple"):
        # Maintainer policy: a Mac verifier is not a merge prerequisite.
        # Keep semantic review and every selected portable check; do not claim
        # an Apple build, simulator or visual acceptance on this host.
        commands.append(("Apple semantic and portable verification policy", ["true"]))
        non_ui = non_ui_apple_change(worktree, base, head, names)
    else:
        non_ui = False
    if ui_change(names) and not non_ui and not test_or_fix(pr, names):
        if not config.get("ui_evidence_command"):
            raise WatchError("UI changes need the configured visual-evidence verifier (dark/light screenshots and manual inspection); tests alone are insufficient")
        commands.append(external_check(config["ui_evidence_command"], pr, worktree, "visual evidence"))
    if not commands:
        raise WatchError("no verification lane selected")
    # These scripts create a disposable Postgres and do not use the production hub.
    return commands


def extension_modules(worktree, names):
    text = checked(["python3", "-I", "scripts/check-layout.py", "modules"], cwd=worktree)
    modules = [m for m in text.splitlines() if m]
    affected = set()
    for module in modules:
        if any(name == module or name.startswith(module + "/") for name in names):
            affected.add(module)
    if any(name.startswith(("scripts/", "schema/", "sdk/", "pluginkit/", ".github/"))
           or name in ("go.mod", "go.sum") for name in names):
        return None
    # Shared code can affect consumers in every module. Let go list compute the
    # actual dependency closure rather than maintaining another extension index.
    shared = [name for name in names if name.startswith("sdk/go/")]
    if shared:
        for module in modules:
            result = run(["go", "-C", module, "list", "-deps", "./..."], cwd=worktree, timeout=180)
            if result.returncode:
                raise WatchError("cannot determine extension dependencies: " + result_summary(result))
            for name in shared:
                pkg = Path(name).parent.as_posix()
                if pkg in result.stdout.splitlines():
                    affected.add(module)
    if not affected:
        return []
    return sorted(affected)


def extension_checks(worktree, pr, timeout):
    base, head = pr["baseRefOid"], pr["headRefOid"]
    names = checked(["git", "diff", "--name-only", "--no-renames", base + "..." + head], cwd=worktree).splitlines()
    modules = extension_modules(worktree, names)
    if modules is None:
        return [("extension repository CI", ["bash", "scripts/ci.sh"])]
    if not modules:
        # Documentation-only changes still have repository-wide doc/layout gates.
        return [("extension repository CI", ["bash", "scripts/ci.sh"])]
    commands = []
    for module in modules:
        commands.extend([
            (module + " tidy", ["go", "-C", module, "mod", "tidy", "-diff"]),
            (module + " vet", ["go", "-C", module, "vet", "./..."]),
            (module + " tests", ["go", "-C", module, "test", "-race", "./..."]),
        ])
        if module.startswith("extensions/"):
            identifier = module.split("/", 1)[1]
            commands.append((identifier + " package", ["bash", "scripts/package.sh", identifier]))
            if Path(worktree, module, "requirements.txt").is_file():
                commands.append((identifier + " Python tests",
                                 ["python3", "-I", "-B", "-m", "unittest", "discover", "-s", module + "/tests"]))
    return commands


def verify(repo, pr, worktree, config, environ=None):
    """Return actual completed check evidence. A failed or unavailable check raises."""
    profile = config.get("profile", "commands")
    timeout = int(config.get("timeout_seconds", 3600))
    if not 60 <= timeout <= 21600:
        raise WatchError("verification timeout must be between 60 and 21600 seconds")
    if profile == "persona":
        commands = persona_checks(worktree, pr, timeout, config)
    elif profile == "extensions":
        commands = extension_checks(worktree, pr, timeout)
    elif profile == "commands":
        raw = config.get("commands")
        if not isinstance(raw, list) or not raw:
            raise WatchError("commands profile needs a non-empty commands array")
        commands = []
        for item in raw:
            if isinstance(item, list):
                name, argv = Path(item[0]).name if item and isinstance(item[0], str) else "check", item
            elif isinstance(item, dict):
                name, argv = item.get("name"), item.get("argv")
            else:
                raise WatchError("each verification command needs argv")
            commands.append((name, command_with_prefix(argv, [])))
            if not isinstance(name, str) or not name or len(name) > 100:
                raise WatchError("invalid verification command name")
    else:
        raise WatchError("unknown verification profile")
    results = []
    failures = []
    for name, argv, *relative in commands:
        if len(relative) > 1 or (relative and (Path(relative[0]).is_absolute()
                                          or ".." in Path(relative[0]).parts)):
            raise WatchError("verification subdirectory must stay inside the repository")
        if name == "Exact-head privacy scan":
            env = dict(os.environ, PERSONA_PRIVACY_ROOT=worktree)
            result = run_check(name, argv, worktree, timeout, env)
        else:
            result = run_check(name, argv, worktree, timeout, environ,
                               cwd=str(Path(worktree, relative[0])) if relative else None)
        if name == "Changed Python tests" and "Ran 0 tests" in (result["stdout"] + result["stderr"]):
            result["ok"] = False
            result["summary"] = bounded(result["stdout"] + "\n" + result["stderr"], 6000) + \
                "\nChanged Python suite ran no tests; refusing to call it verified."
        results.append(result)
        if not result["ok"]:
            failures.append(VerificationError("%s failed:\n%s" % (name, result["summary"]), command=argv,
                                              output=result["stdout"] + "\n" + result["stderr"]))
    if failures:
        raise VerificationError("\n\n".join(str(failure) for failure in failures),
                                output="\n\n".join(failure.output for failure in failures),
                                command=failures[0].command,
                                failures=[{"command": failure.command, "output": failure.output} for failure in failures],
                                checks=results)
    return results
