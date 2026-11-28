#!/usr/bin/env python3
"""Lower every portable count ratchet's baseline to the current counts: tidying anyone can run (#12785).

Count ratchets fail only when a count rises (docs/operations/testing.md, "Count ratchets"), so a count that falls
leaves room under its baseline until someone lowers it. The change that lowered the count does not have to; run this
whenever you like (weekly, say) on an up-to-date main and send the result as a PR of its own:

    python3 scripts/ci/lower_baselines.py                      # lower every baseline it can
    python3 scripts/ci/lower_baselines.py --dry-run            # say what would change, write nothing
    python3 scripts/ci/lower_baselines.py --only sleep,web-font
    python3 scripts/ci/lower_baselines.py --list

Each ratchet is lowered by its own update command, so its counting stays in one place. This then checks that the
command only lowered: every number in each of its baseline files at or under what it was, no new entry and no new
file (a removed entry or file is a count gone to zero). A result that raises or adds anything is put back and the
ratchet reported as left alone, as is one whose update fails: a count above its baseline, a file the web font ratchet
would let take sizes that moved to it, or a prompt-size move between pieces is a change's business, not tidying. A
ratchet whose tools are missing here (go, or web/node_modules) is skipped and says so.

Apple's ratchets (TokenRatchetTests and the other PersonaTests baselines, the isolation and performance baselines under
scripts/test) are not here yet: they are lowered and verified on a Mac.

Exit codes: 0 every ratchet was lowered, already at its counts or skipped; 1 one was left alone; 2 bad arguments.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PYTHON = sys.executable or "python3"
TAIL_LINES = 15  # lines of a failed update's output shown in the report


class Ratchet:
    """One count ratchet: its baseline files (glob patterns under the root), the command that lowers them, and how a
    baseline file reads as counts (JSON unless it says otherwise)."""

    def __init__(self, name, what, files, command, cwd=".", env=None, needs=(), timeout=600, parse=json.loads):
        self.name = name
        self.what = what
        self.files = files
        self.command = command
        self.cwd = cwd
        self.env = env or {}
        self.needs = needs  # commands that must be on PATH, or paths (with a "/") that must exist under the root
        self.timeout = timeout
        self.parse = parse  # bytes of a baseline file -> its counts, for only_lowers


def domain_counts(data):
    """The domain rules' baseline (server/internal/archguard/testdata/domain_layers/baseline.txt) as counts: each
    count's limit, its "<key> <rule> <count>" line plus one per "<key> <rule> <Type> #PR ..." allowance, as
    {"<key> <rule>": limit}, and each allowance as {"<key> <rule> <Type>": 1}. An allowance the count takes in (its
    type landed) keeps the limit and removes its own entry; nothing may add one. Comments and blank lines are left out."""
    counts = {}
    for line in data.decode("utf-8").splitlines():
        fields = line.split()
        if not fields or fields[0].startswith("#"):
            continue
        if len(fields) == 3 and fields[2].isdigit():
            key = " ".join(fields[:2])
            counts[key] = counts.get(key, 0) + int(fields[2])
        elif len(fields) > 3 and not fields[2].isdigit() and all(f.startswith("#") and f[1:].isdigit() for f in fields[3:]):
            key = " ".join(fields[:2])
            counts[key] = counts.get(key, 0) + 1
            counts[" ".join(fields[:3])] = 1
        else:
            raise ValueError("neither <key> <rule> <count> nor <key> <rule> <Type> #PR ...: " + line)
    return counts


RATCHETS = (
    Ratchet("sleep", "fixed time.Sleep calls in Go tests", ["scripts/ci/sleep_baseline.json"],
            [PYTHON, "scripts/ci/sleep_ratchet.py", "--update"], timeout=300),
    Ratchet("web-font", "hard-coded px font sizes in web/src", ["scripts/ci/web_font_baseline.json"],
            [PYTHON, "scripts/ci/web_font_ratchet.py", "--update"], timeout=300),
    Ratchet("web-module-boundary", "disables of web/src's feature-module boundary rules",
            ["scripts/ci/web_module_boundary_baseline.json"],
            [PYTHON, "scripts/ci/web_module_boundary_ratchet.py", "--update"], timeout=300),
    Ratchet("migration-enum", "enum-like text columns without a CHECK and JSON columns without a shape in the migrations",
            ["scripts/ci/migration_enum_baseline.json"],
            [PYTHON, "scripts/ci/migration_enum_check.py", "--update"], timeout=300),
    Ratchet("prompt-language", "prompt templates without language handling", ["scripts/ci/prompt_language_baseline.json"],
            [PYTHON, "scripts/ci/prompt_language_ratchet.py", "--update"], timeout=300),
    Ratchet("android-i18n", "hard-coded Chinese, Japanese and English text in Android code",
            ["android/**/i18n-baseline.json", "android/**/i18n-english-baseline.json"],
            [PYTHON, "scripts/app/android_i18n_check.py", "--update"], timeout=300),
    Ratchet("android-tokens", "hard-coded Android design values and Material 3 components in pages",
            ["android/core/designsystem/tools/token-baseline.json"],
            [PYTHON, "scripts/app/android_token_baseline.py", "--update-baseline"], timeout=300),
    Ratchet("android-motion", "literal Android animation specs", ["android/core/designsystem/tools/motion-baseline.json"],
            [PYTHON, "scripts/app/android_motion_baseline.py", "--update-baseline"], timeout=300),
    Ratchet("android-slot-order", "Android design-system components that end after their slot",
            ["android/core/designsystem/tools/slot-order-baseline.json"],
            [PYTHON, "scripts/app/android_slot_order.py", "--update-baseline"], timeout=300),
    Ratchet("android-compose-budget", "Android @Composable functions over the code line, remember and effect caps",
            ["scripts/app/android_compose_budget_baseline.json"],
            [PYTHON, "scripts/app/android_compose_budget.py", "--update-baseline"], timeout=300),
    Ratchet("android-nested-throwables", "nested Android exceptions whose outer class has no -keepnames rule",
            ["scripts/app/android_nested_throwables_baseline.json"],
            [PYTHON, "scripts/app/android_nested_throwables.py", "--update-baseline"], timeout=300),
    Ratchet("hub-hardcoded-text", "hard-coded Chinese in the hub's code and command lines",
            ["server/internal/i18n/hardcoded-baseline.json", "server/internal/i18n/hardcoded-terminal-baseline.json"],
            ["go", "test", "-count=1", "-run", "^(TestHardcodedChineseBaseline|TestHardcodedTerminalBaseline)$",
             "./internal/i18n/"], cwd="server", env={"UPDATE_I18N_BASELINE": "1"}, needs=("go",)),
    Ratchet("prompt-sizes", "sizes of the assistant's instructions, tool descriptions and supervision events",
            ["server/internal/assistant/testdata/prompt_sizes.json"],
            ["go", "test", "-count=1", "-run", "^TestPromptSizeRatchet$", "./internal/assistant/"], cwd="server",
            env={"PROMPT_SIZE_UPDATE": "1"}, needs=("go",), timeout=1200),
    Ratchet("web-hardcoded-text", "hard-coded Chinese in web/src", ["web/src/i18n/hardcoded-baseline.json"],
            ["npx", "vitest", "run", "src/i18n/catalog.test.ts"], cwd="web", env={"UPDATE_I18N_BASELINE": "1"},
            needs=("npx", "web/node_modules")),
    Ratchet("wire-types", "weak types in the contract packages and the hub's handlers",
            ["scripts/ci/wire_types_baseline.json"], [PYTHON, "scripts/ci/wire_types_ratchet.py", "--update"],
            needs=("go",), timeout=300),
    Ratchet("domain-rules", "the domain rules' findings in server code that predates them",
            ["server/internal/archguard/testdata/domain_layers/baseline.txt"],
            ["go", "test", "-count=1", "-run", "^TestDomainLayers$", "./internal/archguard/"], cwd="server",
            env={"DOMAIN_LAYERS_UPDATE": "1"}, needs=("go",), parse=domain_counts),
)


def only_lowers(old, new, where=""):
    """Where new is above old or has something old does not; a number, entry or file new lacks is a lowering."""
    if isinstance(old, dict) and isinstance(new, dict):
        problems = []
        for key in new:
            if key not in old:
                problems.append("{0}{1}: added".format(where, key))
            else:
                problems.extend(only_lowers(old[key], new[key], "{0}{1}/".format(where, key)))
        return problems
    if isinstance(old, list) and isinstance(new, list):
        return ["{0}: adds {1}".format(where.rstrip("/"), json.dumps(item, ensure_ascii=False))
                for item in new if item not in old]
    numbers = all(isinstance(value, (int, float)) and not isinstance(value, bool) for value in (old, new))
    if numbers and new <= old:
        return []
    if new == old:
        return []
    return ["{0}: {1} -> {2}".format(where.rstrip("/"), json.dumps(old, ensure_ascii=False),
                                     json.dumps(new, ensure_ascii=False))]


def baseline_files(root, ratchet):
    """The ratchet's baseline files that exist now, as repository paths."""
    found = set()
    for pattern in ratchet.files:
        if any(ch in pattern for ch in "*?["):
            found.update(path.relative_to(root).as_posix() for path in root.glob(pattern) if path.is_file())
        elif (root / pattern).is_file():
            found.add(pattern)
    return found


def snapshot(root, paths):
    return {path: (root / path).read_bytes() if (root / path).is_file() else None for path in paths}


def restore(root, before):
    for path, data in before.items():
        target = root / path
        if data is None:
            if target.is_file():
                target.unlink()
        elif not target.is_file() or target.read_bytes() != data:
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)


def missing_needs(root, ratchet):
    for need in ratchet.needs:
        if "/" in need:
            if not (root / need).exists():
                return need + " is missing"
        elif shutil.which(need) is None:
            return need + " is not on PATH"
    return None


def run_command(root, ratchet):
    """(exit code, output); the command runs in its own process group so a timeout stops its children too."""
    env = dict(os.environ, **ratchet.env)
    group = hasattr(os, "killpg")  # POSIX; elsewhere only the command itself is stopped
    try:
        process = subprocess.Popen(ratchet.command, cwd=str(root / ratchet.cwd), env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=group)
    except OSError as error:
        return 127, str(error)
    try:
        out, _ = process.communicate(timeout=ratchet.timeout)
    except subprocess.TimeoutExpired:
        try:
            if group:
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
        except (ProcessLookupError, PermissionError):
            process.kill()
        out, _ = process.communicate()
        return None, out.decode("utf-8", "replace")
    return process.returncode, out.decode("utf-8", "replace")


def tail(text):
    lines = [line for line in text.splitlines() if line.strip()]
    return "\n".join("    " + line for line in lines[-TAIL_LINES:])


def lower(root, ratchet, dry_run=False):
    """(outcome, detail): outcome is lowered, unchanged, left alone or skipped."""
    reason = missing_needs(root, ratchet)
    if reason:
        return "skipped", reason
    before = snapshot(root, baseline_files(root, ratchet))
    if not before:
        return "left alone", "no baseline file found (" + ", ".join(ratchet.files) + ")"
    code, output = run_command(root, ratchet)
    after = snapshot(root, set(before) | baseline_files(root, ratchet))
    before = {path: before.get(path) for path in after}  # a file the update created is restored by removing it
    if code is None or code != 0:
        restore(root, before)
        what = "timed out after {0} s".format(ratchet.timeout) if code is None else "failed (exit {0})".format(code)
        return "left alone", "its update {0}; nothing written:\n{1}".format(what, tail(output))
    problems = []
    for path in sorted(after):
        old, new = before.get(path), after[path]
        if old is None and new is not None:
            problems.append(path + ": a new baseline file")
        elif old is not None and new is not None and old != new:
            try:
                problems.extend(path + ": " + line for line in only_lowers(ratchet.parse(old), ratchet.parse(new)))
            except ValueError as error:
                problems.append("{0}: does not read as counts after the update ({1})".format(path, error))
    if problems:
        restore(root, before)
        return "left alone", ("the update would raise or add, which is a change's business, not tidying; nothing "
                              "written:\n" + "\n".join("    " + line for line in problems[:TAIL_LINES]))
    changed = sorted(path for path in after if after[path] != before.get(path))
    if dry_run:
        restore(root, before)
    if not changed:
        return "unchanged", "already at the counts"
    return "lowered", ("would lower " if dry_run else "lowered ") + ", ".join(changed)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0],
                                     formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    parser.add_argument("--root", default=str(ROOT), help="repository root (default: this checkout)")
    parser.add_argument("--only", help="comma-separated ratchet names (see --list)")
    parser.add_argument("--dry-run", action="store_true", help="report what would be lowered and write nothing")
    parser.add_argument("--list", action="store_true", help="list the ratchets and their baseline files")
    args = parser.parse_args(argv)
    names = {ratchet.name: ratchet for ratchet in RATCHETS}
    if args.list:
        for ratchet in RATCHETS:
            print("{0}: {1} ({2})".format(ratchet.name, ratchet.what, ", ".join(ratchet.files)))
        return 0
    chosen = RATCHETS
    if args.only:
        wanted = [name.strip() for name in args.only.split(",") if name.strip()]
        unknown = [name for name in wanted if name not in names]
        if unknown or not wanted:
            parser.error("unknown ratchet(s): {0} (known: {1})".format(", ".join(unknown) or "none given",
                                                                         ", ".join(names)))
        chosen = [names[name] for name in wanted]
    root = Path(args.root).resolve()
    tally = {"lowered": 0, "unchanged": 0, "left alone": 0, "skipped": 0}
    for ratchet in chosen:
        outcome, detail = lower(root, ratchet, args.dry_run)
        tally[outcome] += 1
        print("lower_baselines: {0}: {1}: {2}".format(ratchet.name, outcome, detail), flush=True)
    verb = "would lower" if args.dry_run else "lowered"
    print("lower_baselines: {0} {1}, {2} already at the counts, {3} left alone, {4} skipped".format(
        verb, tally["lowered"], tally["unchanged"], tally["left alone"], tally["skipped"]))
    return 1 if tally["left alone"] else 0


if __name__ == "__main__":
    sys.exit(main())
