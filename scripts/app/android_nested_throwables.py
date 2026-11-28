#!/usr/bin/env python3
"""Fail when a nested Android exception's outer class has no name rule, so its simple name survives R8.

    android_nested_throwables.py [--update-baseline]

Error reports and breadcrumbs print a Throwable's simple name, which comes from its InnerClass annotation, and an
optimizing R8 keeps that annotation only when it keeps the names of both the nested class and its outer class
(docs/android/android.md, "Release shrinking"). `-keepnames class * extends java.lang.Throwable` keeps the first; the
second needs a rule per outer class in android/app/proguard-rules.pro. scripts/release/check_android_shrink.py finds a
missing one in a release build, which nothing runs on a PR: #17454 and #17474 nested four exceptions in DocumentText and
Xlsx without the rule and stopped the next release (#17578, #17580).

This reads the Kotlin sources instead, in a fraction of a second: every class nested in another class or object (not
in a function) that extends a Throwable, directly or through the repository's own classes (a supertype the sources do
not declare counts when its name ends in Exception or Error, as the platform's and the libraries' do), must have an
outer class that a `-keep` / `-keepnames` class rule names or matches with its wildcards, in proguard-rules.pro or a
module's consumer-rules.pro, or that is a Throwable itself. Nested exceptions that lack one today are in the baseline,
which may only shrink: add the rule, then lower it. The release check stays the authority.
"""
import argparse
import bisect
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from android_compose_budget import code_of, matching, source_files  # noqa: E402
from android_token_baseline import ROOT  # noqa: E402

BASELINE = Path(__file__).with_name('android_nested_throwables_baseline.json')
RULE_FILES = ('android/app/proguard-rules.pro', 'android/**/consumer-rules.pro')
THROWABLE_ROOTS = {'Throwable', 'Exception', 'Error', 'RuntimeException'}
PACKAGE = re.compile(r'^\s*package\s+([\w.]+)', re.M)
# A brace, or a class-like declaration's keyword. `Foo::class` and `.class` are references, not declarations.
EVENTS = re.compile(r'[{}]|(?<![:.\w])(?:companion\s+)?(?:class|interface|object)\b')
NAME = re.compile(r'\s*(`[^`]+`|\w+)')
SPACE = re.compile(r'\s*')
NEWLINE = re.compile(r'\n')
KEEP = re.compile(r'^-(?:keep|keepnames)(?:,[\w,]+)?\s+(?:(?:public|final|abstract|!?@\S+)\s+)*(?:class|interface|enum)\s+'
                  r'(.+?)(?:\s+(?:extends|implements)\s+(\S+))?\s*(?:\{.*)?$')


def header(code, start):
    """(name, supertypes, body brace index or None) of the declaration whose keyword starts at start; name is None for
    an anonymous `object : T { }`."""
    keyword = code[start:start + 40].split()[0]
    i = start + len(keyword)
    if keyword == 'companion':
        i = code.index('object', i) + len('object')
    name_match = NAME.match(code, i)
    name = name_match.group(1).strip('`') if name_match else None
    if name_match:
        i = name_match.end()
    if keyword == 'companion' and name is None:
        name = 'Companion'
    supertypes, depth, colon = [], 0, None
    n = len(code)
    while i < n:
        ch = code[i]
        if ch == '<' and depth == 0 and colon is None:
            i = matching(code, i, '<', '>') + 1
            continue
        if ch == '(' and depth == 0 and colon is None:
            i = matching(code, i, '(', ')') + 1
            continue
        if ch in '([<':
            depth += 1
        elif ch in ')]>':
            depth -= 1
        elif depth == 0 and ch == ':' and colon is None:
            colon = i + 1
        elif depth == 0 and ch == '{':
            if colon is not None:
                supertypes = split_supertypes(code[colon:i])
            return name, supertypes, i
        elif depth == 0 and ch in ';}=':
            break
        elif depth == 0 and ch == '\n':
            # The header goes on when the next line continues it or this one ends in `:` or `,`.
            after = SPACE.match(code, i).end()
            rest = code[after:after + 12]
            j = i - 1
            while j > start and code[j] in ' \t\r':
                j -= 1
            if not (rest.startswith((':', ',', 'where', '{')) or code[j] in ':,' or
                    (colon is None and rest.startswith('constructor'))):
                break
        i += 1
    if colon is not None:
        supertypes = split_supertypes(code[colon:i])
    return name, supertypes, None


def split_supertypes(text):
    """Simple names of the supertypes in a declaration's `: A(), b.B<T>, C by d` list."""
    names, depth, part = [], 0, []
    for ch in text + ',':
        if ch in '(<[{':
            depth += 1
        elif ch in ')>]}':
            depth -= 1
        if ch == ',' and depth == 0:
            words = re.sub(r'@\w+(\([^)]*\))?', ' ', ''.join(part)).split('(')[0].split('<')[0].split(' by ')[0].split()
            if words:
                names.append(words[-1].split('.')[-1])
            part = []
        else:
            part.append(ch)
    return names


def declarations(source):
    """[(binary name, supertypes, nested in a class body, line)] of the class-like declarations in a Kotlin source."""
    code = code_of(source)
    package = (PACKAGE.search(code) or [None, ''])[1]
    prefix = package + '.' if package else ''
    newlines = [match.start() for match in NEWLINE.finditer(code)]
    bodies, stack, found = {}, [], []
    for match in EVENTS.finditer(code):
        token = match.group(0)
        if token == '{':
            stack.append(bodies.pop(match.start(), None))
        elif token == '}':
            if stack:
                stack.pop()
        else:
            name, supertypes, body = header(code, match.start())
            if name is None:  # an anonymous object: what it declares is local
                if body is not None:
                    bodies[body] = None
                continue
            member = all(scope is not None for scope in stack)
            outer = stack[-1] if stack and member else None
            # A local class is named under its nearest enclosing class, so it never takes a top-level class's name.
            nearest = next((scope for scope in reversed(stack) if scope is not None), None)
            binary = f'{nearest}${name}' if nearest else prefix + name
            if body is not None:
                bodies[body] = binary if member else None
            found.append((binary, supertypes, bool(outer), bisect.bisect(newlines, match.start()) + 1))
    return found


def scan(root):
    """{binary name: (supertypes, nested, '<path>:<line>')} of the app's production Kotlin declarations."""
    classes, prefix = {}, len(root.as_posix()) + 1
    for path in source_files(root):
        rel = path.as_posix()[prefix:]
        for binary, supertypes, nested, line in declarations(path.read_text(encoding='utf-8')):
            classes[binary] = (supertypes, nested, f'{rel}:{line}')
    return classes


def throwables(classes):
    """The binary names among classes that are Throwables."""
    by_simple = {}
    for binary in classes:
        by_simple.setdefault(re.split(r'[.$]', binary)[-1], []).append(binary)
    known = {}

    def throws(binary, seen):
        if binary in known:
            return known[binary]
        result = False
        for supertype in classes[binary][0]:
            if supertype in THROWABLE_ROOTS:
                result = True
            elif supertype in by_simple:
                result = any(throws(other, seen | {binary}) for other in by_simple[supertype] if other not in seen)
            else:
                result = supertype.endswith(('Exception', 'Error'))
            if result:
                break
        known[binary] = result
        return result

    return {binary for binary in classes if throws(binary, frozenset())}


def keep_rules(root):
    """[(class name pattern, extends or None)] of the `-keep` and `-keepnames` class rules the release build reads."""
    rules = []
    for pattern in RULE_FILES:
        for path in sorted(root.glob(pattern)):
            for line in path.read_text(encoding='utf-8').splitlines():
                match = KEEP.match(line.split('#', 1)[0].strip())
                if match:
                    rules += [(name.strip(), match.group(2)) for name in match.group(1).split(',') if name.strip()]
    return rules


def matches(pattern, name):
    """ProGuard's class name wildcards: `**` any characters, `*` any but a package separator, `?` one of those; `*` on its
    own is every class."""
    if pattern == '*':
        return True
    regex = ''.join('.*' if part == '**' else '[^.]*' if part == '*' else '[^.]' if part == '?' else re.escape(part)
                    for part in re.split(r'(\*\*|\*|\?)', pattern))
    return re.fullmatch(regex, name) is not None


def uncovered(classes, rules):
    """{nested Throwable: (its outer class, where)} whose outer class no rule keeps the name of."""
    thrown = throwables(classes)
    missing = {}
    for binary, (_, nested, where) in sorted(classes.items()):
        if not nested or binary not in thrown:
            continue
        outer = binary.rsplit('$', 1)[0]
        kept = any(matches(pattern, outer) and (extends is None or
                                                (extends.split('.')[-1] in THROWABLE_ROOTS and outer in thrown))
                   for pattern, extends in rules)
        if not kept:
            missing[binary] = (outer, where)
    return missing


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n', 1)[0])
    parser.add_argument('--root', type=Path, default=ROOT, help='repository root')
    parser.add_argument('--baseline', type=Path, default=BASELINE, help='baseline file')
    parser.add_argument('--update-baseline', action='store_true',
                        help='drop the nested exceptions that have their rule now (refuses to add one)')
    args = parser.parse_args(argv)
    classes = scan(args.root)
    missing = uncovered(classes, keep_rules(args.root))
    baseline = json.loads(args.baseline.read_text()) if args.baseline.is_file() else None
    if baseline is not None and baseline.get('version') != 1:
        parser.error('baseline version does not match')
    known = set(baseline['uncovered']) if baseline else set()
    if args.update_baseline:
        added = sorted(set(missing) - known) if baseline is not None else []
        if added:
            print('Refusing to add to the nested Throwable baseline (add the outer classes\' rules instead):\n'
                  + '\n'.join(added))
            return 1
        args.baseline.write_text(json.dumps({'version': 1, 'uncovered': sorted(missing)}, indent=2) + '\n')
        print('nested Throwable baseline written')
        return 0
    if baseline is None:
        parser.error('no baseline; run --update-baseline first')
    nested = sum(1 for binary in throwables(classes) if classes[binary][1])
    print(f'nested Throwables: {nested}, {len(missing)} without an outer-class name rule (baseline {len(known)})')
    fixed = sorted(known - set(missing))
    if fixed:
        print(f'{len(fixed)} baseline entr{"y" if len(fixed) == 1 else "ies"} now covered or gone; lowering the baseline '
              'is tidying anyone can do, not this change\'s job: python3 scripts/ci/lower_baselines.py')
    failures = [f'{where}: {binary} is nested in {outer}, which no -keepnames rule keeps the name of: add '
                f'`-keepnames class {outer}` to android/app/proguard-rules.pro (docs/android/android.md "Release shrinking")'
                for binary, (outer, where) in sorted(missing.items()) if binary not in known]
    if failures:
        print('\n' + '\n'.join(failures))
        return 1
    print('nested Throwable name rules check passed')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
