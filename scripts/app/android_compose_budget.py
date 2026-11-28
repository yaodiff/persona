#!/usr/bin/env python3
"""Hold each Android @Composable function to a size budget, so a page splits before ART stops compiling it.

    android_compose_budget.py [--update-baseline]

ART compiles a method up to 10,000 dex code units and leaves a bigger one interpreted; scripts/release/check_android_shrink.py
measures that on a release build, which nothing runs on a PR (docs/android/android.md, "Release shrinking"). ChatPage
crossed it six times (#17578, #17580). This counts, from the source and in milliseconds, what makes such a function big:
its code lines (comments and blank lines left out), its `remember*` calls and its effects (LaunchedEffect,
DisposableEffect, SideEffect). A function over a cap must be in the baseline with the counts it had, and a listed count
may only go down: a count ratchet (docs/operations/testing.md, "Count ratchets"). A count the baseline does not list is
held to the cap, so a function under every cap needs no entry. Split a page that fails: its state into a holder, its
effects into a side-effect composable, its sheets and dialogs into their own composable.

Lambdas count toward the function they are written in, though the compiler gives a composable lambda a method of its
own: the source does not say which ones, and a function this long is hard to review either way. The release check
stays the authority on method size.
"""
import argparse
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from android_token_baseline import MODULE_GROUPS, ROOT  # noqa: E402

BASELINE = Path(__file__).with_name('android_compose_budget_baseline.json')
# Caps a function may reach without a baseline entry. The largest functions on main exceed them by far (ChatPage had
# 1,350 code lines, 108 remembers and 20 effects); 400 lines or 30 remembers or 12 effects is where a few of the 1,800
# composables sit, and each of those was already near the ART limit or had crossed it.
CAPS = {'lines': 400, 'remembers': 30, 'effects': 12}
METRICS = tuple(CAPS)
# A @Composable function declaration (the annotation bare or as `@androidx.compose.runtime.Composable`), more
# annotations or modifiers, `fun`, type parameters, a receiver.
DECLARATION = re.compile(
    r'@(?:androidx\.compose\.runtime\.)?Composable\b(?:\s*@[\w.]+(?:\([^()]*\))?)*\s*'
    r'(?:(?:private|internal|public|protected|inline|override|actual|expect|suspend|operator|infix|tailrec)\s+)*'
    r'fun\s+(?:<[^>]*>\s*)?(?:[\w.]+(?:<[^>]*>)?\.)?(\w+)\s*\(')
# Calls bare or qualified with the Compose runtime's package (`androidx.compose.runtime.saveable.rememberSaveable(...)`);
# a method of another object (`state.remember(...)`) is not one.
RUNTIME = r'(?<![\w.])(?:androidx\.compose\.runtime\.(?:\w+\.)*)?'
REMEMBER = re.compile(RUNTIME + r'remember\w*\s*(?:<[^\n]*?>)?\s*[({]')
EFFECT = re.compile(RUNTIME + r'(?:LaunchedEffect|DisposableEffect|SideEffect)\s*[({]')


# Comments and string or character literals, in the order a lexer meets them, so `//` in a string or a quote in a
# comment is read right. Block comments do not nest here (Kotlin's may); on the app's sources the counts are the same
# as with android_token_baseline.code_only, which nests them.
LITERALS = re.compile(r'//[^\n]*|/\*[\s\S]*?\*/|"""[\s\S]*?"""(?!")|"""[\s\S]*?"+(?=[^"]|$)'
                      r'|"(?:\\.|[^"\\\n])*"|\'(?:\\.|[^\'\\\n])*\'')
NOT_NEWLINE = re.compile(r'[^\n]')


def code_of(source):
    """source with comments and literals blanked to spaces (offsets and newlines kept): braces and words inside them are
    not read as code, and a line holding only a comment is blank. One regular expression over the file, so the whole
    tree takes a fraction of a second (android_token_baseline.code_only walks it character by character)."""
    return LITERALS.sub(lambda match: NOT_NEWLINE.sub(' ', match.group(0)), source)


def matching(code, start, open_ch, close_ch):
    """Index of the bracket closing code[start] (which is open_ch), or len(code)."""
    depth = 0
    for i in range(start, len(code)):
        if code[i] == open_ch:
            depth += 1
        elif code[i] == close_ch:
            depth -= 1
            if depth == 0:
                return i
    return len(code)


def body_span(code, params_open):
    """(start, end) of the body of the function whose parameter list opens at params_open: a block body's braces, or
    an expression body up to the end of its line or its first block, or None for a declaration without a body."""
    i = matching(code, params_open, '(', ')') + 1
    # The return type, a `where` clause: anything up to `{` or `=` at this level.
    depth = 0
    while i < len(code):
        ch = code[i]
        if ch in '(<[':
            depth += 1
        elif ch in ')>]':
            depth -= 1
        elif depth <= 0 and ch == '{':
            return i, matching(code, i, '{', '}')
        elif depth <= 0 and ch == '=' and code[i + 1:i + 2] != '=':
            line_end = code.find('\n', i)
            line_end = len(code) if line_end < 0 else line_end
            brace = code.find('{', i, line_end)
            return (i, matching(code, brace, '{', '}')) if brace >= 0 else (i, line_end)
        elif depth <= 0 and ch == '}':  # an abstract or expect declaration: no body
            return None
        i += 1
    return None


def functions(source):
    """[(name, {'lines', 'remembers', 'effects'})] of every @Composable function in a Kotlin source, in order."""
    code = code_of(source)
    found = []
    for match in DECLARATION.finditer(code):
        span = body_span(code, match.end() - 1)
        if span is None:
            continue
        body = code[span[0]:span[1] + 1]
        found.append((match.group(1), {
            'lines': sum(1 for line in body.splitlines() if line.strip()),
            'remembers': len(REMEMBER.findall(body)),
            'effects': len(EFFECT.findall(body)),
        }))
    return found


def source_files(root):
    """Production Kotlin of every app module, the way the other Android ratchets read it (no test source sets)."""
    for group in MODULE_GROUPS:
        base = root / 'android' / group
        if not base.is_dir():
            continue
        for module in [base] if (base / 'src').is_dir() else sorted(base.iterdir()):
            if not module.is_dir() or not (module / 'src').is_dir():
                continue
            for source_set in sorted((module / 'src').iterdir()):
                if source_set.is_dir() and 'test' not in source_set.name.lower():
                    yield from sorted(source_set.rglob('*.kt'))


def scan(root):
    """{'<path>::<function>': counts} of every @Composable function; a second one of a name in a file is `::<name>#2`."""
    counts = {}
    for path in source_files(root):
        seen = {}
        source = path.read_text(encoding='utf-8')
        if 'Composable' not in source:  # bare or `@androidx.compose.runtime.Composable`
            continue
        for name, metrics in functions(source):
            seen[name] = seen.get(name, 0) + 1
            key = f'{path.relative_to(root).as_posix()}::{name}' + (f'#{seen[name]}' if seen[name] > 1 else '')
            counts[key] = metrics
    return counts


def over_caps(metrics):
    """The counts of a function that are over their caps: what its baseline entry holds."""
    return {metric: value for metric, value in metrics.items() if value > CAPS[metric]}


def baseline_of(current):
    return {'version': 1, 'functions': {key: over_caps(metrics) for key, metrics in sorted(current.items())
                                        if over_caps(metrics)}}


def regressions(current, baseline):
    """A count above its limit: the baseline's for the counts it lists, the cap for the others."""
    failures = []
    for key, metrics in sorted(current.items()):
        listed = baseline['functions'].get(key, {})
        rises = [f'{metric} {value} (limit {listed.get(metric, CAPS[metric])})' for metric, value in metrics.items()
                 if value > listed.get(metric, CAPS[metric])]
        if rises:
            failures.append(f'{key}: {", ".join(rises)}')
    return failures


def update_raises(current, baseline):
    """What --update-baseline would raise or add: a listed count that went up, or a count newly over its cap."""
    new = baseline_of(current)['functions']
    return [f'{key}: {metric} {value}' + (f', baseline {baseline["functions"][key][metric]}'
                                          if metric in baseline['functions'].get(key, {}) else ', not in the baseline')
            for key, metrics in sorted(new.items()) for metric, value in metrics.items()
            if value > baseline['functions'].get(key, {}).get(metric, CAPS[metric])]


def slack(current, baseline):
    """Baseline counts above the current ones, and entries whose function is gone or back under every cap."""
    notes = []
    for key, listed in sorted(baseline['functions'].items()):
        metrics = current.get(key)
        if metrics is None:
            notes.append(f'{key}: gone')
            continue
        lower = [f'{metric} {metrics[metric]} (baseline {value})' for metric, value in listed.items()
                 if metrics[metric] < value]
        if lower:
            notes.append(f'{key}: {", ".join(lower)}')
    return notes


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n', 1)[0])
    parser.add_argument('--root', type=Path, default=ROOT, help='repository root')
    parser.add_argument('--baseline', type=Path, default=BASELINE, help='baseline file')
    parser.add_argument('--update-baseline', action='store_true',
                        help='accept lower counts and rewrite the baseline (refuses any rise or new entry)')
    args = parser.parse_args(argv)
    current = scan(args.root)
    baseline = json.loads(args.baseline.read_text()) if args.baseline.is_file() else None
    if baseline is not None and baseline.get('version') != 1:
        parser.error('baseline version does not match')
    if args.update_baseline:
        rises = update_raises(current, baseline) if baseline is not None else []
        if rises:
            print('Refusing to raise the Compose budget baseline (counts may only go down; split the function):\n'
                  + '\n'.join(rises))
            return 1
        args.baseline.write_text(json.dumps(baseline_of(current), indent=2, sort_keys=True) + '\n')
        print('Compose budget baseline written')
        return 0
    if baseline is None:
        parser.error('no baseline; run --update-baseline first')
    print(f'Compose budget: {len(current)} @Composable functions, {len(baseline["functions"])} over a cap in the '
          f'baseline (caps: {CAPS["lines"]} code lines, {CAPS["remembers"]} remembers, {CAPS["effects"]} effects)')
    notes = slack(current, baseline)
    if notes:
        print(f'{len(notes)} baseline entr{"y" if len(notes) == 1 else "ies"} above the current counts; lowering them '
              'is tidying anyone can do, not this change\'s job: python3 scripts/ci/lower_baselines.py')
    failures = regressions(current, baseline)
    if failures:
        print('\nOver the Compose budget (split the function: its state into a holder, its effects into a '
              'side-effect composable, its sheets and dialogs into their own composable; docs/android/android.md '
              '"Release shrinking"):\n' + '\n'.join(failures))
        return 1
    print('Compose budget check passed')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
