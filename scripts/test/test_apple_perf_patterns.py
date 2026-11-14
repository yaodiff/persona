#!/usr/bin/env python3
"""Calls that cost milliseconds each time a view redraws, or that are never right, may not grow in the Apple sources
(docs/apple/performance.md, "Automated checks"): localization lookups with an explicit locale (P4), frame-rate
timelines that never pause (P9) and `UserDefaults.synchronize()` (P2).

This is a ratchet: each file has a committed count per pattern (apple_perf_baseline.json) that may stay the same or go
down, never up, and a file with no entry starts at zero. A call that is known to be fine does not count when its line
carries `// perf-ok: <reason>` (the reason must be non-empty), for example
`// perf-ok: resolved once per locale`. Comments are ignored.

The calls that block the thread they run on (Photos fetches, keychain reads, file reads, directory listings, image
decodes) were counted here wherever they were written, because telling the main actor from a background task needs
more than a regex. scripts/test/isolation-lint.sh now reads the isolation of the code around each of them and counts
a call only on the main actor or where it cannot tell (#4262); `// perf-ok:` means nothing to it.

  python3 scripts/test/test_apple_perf_patterns.py            run the check (unittest)
  python3 scripts/test/test_apple_perf_patterns.py --update   rewrite the baseline downward after removing calls
  python3 scripts/test/test_apple_perf_patterns.py --list     print every counted call, file:line
"""
from collections import Counter
import json
from pathlib import Path
import re
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "i18n"))
import apple_comments  # noqa: E402

BASELINE = Path(__file__).resolve().with_name("apple_perf_baseline.json")
ESCAPE = re.compile(r"//\s*perf-ok:\s*\S")

# Rule ids match docs/apple/performance.md.
PATTERNS = {
    # P2: never needed, and it blocks on the defaults daemon.
    "defaults-synchronize": re.compile(r"\bUserDefaults\b[^\n]*\.\s*synchronize\s*\(\s*\)"),
    # P4: a localization lookup with an explicit locale reads the string tables on every call (1-2 ms; #5438).
    # `interfaceText(_:)` is one; its declaration is not a call.
    "localized-lookup": re.compile(r"(?<!func )\binterfaceText\s*\(|\bString\s*\(\s*localized:[^\n]*\blocale\s*:"),
    # P7: a view's frame in the window changes on every frame its page slides (the shell's strip, a sheet, a drag), so
    # state written from one is written, and its readers drawn, on each of them (#5438).
    "window-frame": re.compile(r"\.\s*frame\s*\(\s*in\s*:\s*\.global\s*\)"),
    # P9: a frame-rate timeline that never pauses.
    "unpaused-animation": re.compile(r"\bTimelineView\s*\(\s*\.animation(?:\s*\((?![^()\n]*\bpaused\s*:)[^()\n]*\))?\s*\)"),
}

# Test targets and tooling: they run on purpose where they run and ship nothing.
SKIP_PARTS = {"PersonaTests", "PersonaUITests", "PersonaWatchTests", "Tests", "verify"}


def blank_comments(src):
    """The source with its comments blanked out, lines kept."""
    out = list(src)
    for start, end in apple_comments.c_comments(src):
        for i in range(start, end):
            if out[i] != "\n":
                out[i] = " "
    return "".join(out)


def calls(src):
    """(rule, line) for each counted call in one Swift file."""
    raw = src.splitlines()
    code = blank_comments(src)
    found = []
    for rule, pattern in PATTERNS.items():
        for m in pattern.finditer(code):
            line = code.count("\n", 0, m.start()) + 1
            if not ESCAPE.search(raw[line - 1]):
                found.append((rule, line))
    return sorted(found, key=lambda x: (x[1], x[0]))


def swift_files():
    for path in apple_comments.sources():
        p = Path(path)
        rel = p.relative_to(ROOT)
        if p.suffix == ".swift" and not SKIP_PARTS.intersection(rel.parts) and not rel.parts[1].endswith("Tests"):
            yield rel


def scan():
    """{file: {rule: count}} and the call sites behind it."""
    counts, sites = {}, []
    for rel in swift_files():
        found = calls((ROOT / rel).read_text(encoding="utf-8", errors="replace"))
        if found:
            counts[str(rel)] = dict(sorted(Counter(rule for rule, _ in found).items()))
            sites += [f"{rel}:{line} {rule}" for rule, line in found]
    return dict(sorted(counts.items())), sites


def load_baseline():
    return json.loads(BASELINE.read_text(encoding="utf-8"))


def compare(counts, baseline):
    """(grown, shrunk): lines describing files over their baseline, and under it."""
    grown, shrunk = [], []
    for f in sorted(set(counts) | set(baseline)):
        now, was = counts.get(f, {}), baseline.get(f, {})
        for rule in sorted(set(now) | set(was)):
            n, w = now.get(rule, 0), was.get(rule, 0)
            if n > w:
                grown.append(f"{f}: {rule} {w} -> {n}")
            elif n < w:
                shrunk.append(f"{f}: {rule} {w} -> {n}")
    return grown, shrunk


def update():
    counts, _ = scan()
    baseline = load_baseline()
    grown, _ = compare(counts, baseline)
    if grown:
        print("refusing to raise the baseline (take the call out, or mark a verified one `// perf-ok: <reason>`):")
        print("\n".join(grown))
        return 1
    BASELINE.write_text(json.dumps(counts, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"apple perf baseline: {sum(sum(r.values()) for r in counts.values())} calls in {len(counts)} files")
    return 0


class ApplePerfPatternTests(unittest.TestCase):
    def test_no_file_grows_its_counted_calls(self):
        counts, _ = scan()
        grown, shrunk = compare(counts, load_baseline())
        self.assertEqual(grown, [], "a per-call localization lookup, a read of a window frame, an unpaused animation "
                         "timeline or a UserDefaults.synchronize() was added (docs/apple/performance.md, rules P2, P4, "
                         "P7, P9): cache the lookup per locale, measure in the page's own space or keep the frame "
                         "unobserved, pause the timeline, drop the call, or mark a verified one `// perf-ok: <reason>`")
        self.assertEqual(shrunk, [], "fewer counted calls than the baseline: run "
                         "python3 scripts/test/test_apple_perf_patterns.py --update and commit the baseline")

    def test_what_counts(self):
        self.assertEqual(calls("UserDefaults.standard.synchronize()"), [("defaults-synchronize", 1)])
        self.assertEqual(calls("x\nUserDefaults.standard.synchronize()"), [("defaults-synchronize", 2)])
        self.assertEqual(calls('// UserDefaults.standard.synchronize()\n/* interfaceText("Done") */'), [])
        self.assertEqual(calls(".onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { r in }"), [("window-frame", 1)])
        self.assertEqual(calls("let f = g.frame( in: .global )"), [("window-frame", 1)])
        self.assertEqual(calls("$0.frame(in: .named(space))\n$0.frame(in: .local)"), [])
        self.assertEqual(calls("$0.frame(in: .global).minY // perf-ok: only y, and the box is unobserved"), [])
        self.assertEqual(calls("TimelineView(.animation) { c in }"), [("unpaused-animation", 1)])
        self.assertEqual(calls("TimelineView(.animation(minimumInterval: 1 / 30)) { c in }"), [("unpaused-animation", 1)])
        self.assertEqual(calls("TimelineView(.animation(minimumInterval: 1 / 60, paused: still)) { c in }"), [])
        self.assertEqual(calls("TimelineView(.periodic(from: .now, by: 1)) { c in }"), [])
        self.assertEqual(calls('Text(interfaceText("Needs You"))'), [("localized-lookup", 1)])
        self.assertEqual(calls("nonisolated func interfaceText(_ resource: LocalizedStringResource) -> String {"), [])
        self.assertEqual(calls('let s = String(localized: "Done", locale: .interface)'), [("localized-lookup", 1)])
        self.assertEqual(calls('let s = String(localized: "Done")'), [])
        self.assertEqual(calls('let s = interfaceText("Done") // perf-ok: resolved once per locale, StateName.text'), [])
        self.assertEqual(calls('let s = interfaceText("Done") // perf-ok:'), [("localized-lookup", 1)])

    def test_thread_dependent_calls_belong_to_the_isolation_lint(self):
        # Counted by scripts/test/isolation-lint.sh from the isolation of the code around them, not here; what counts
        # as one is pinned in scripts/test/isolation-lint/Tests/IsolationLintTests/RuleAndProjectTests.swift.
        self.assertEqual(calls("let r = PHAsset.fetchAssets(with: o)\nlet d = try Data(contentsOf: url)\n"
                               "let i = UIImage(data: d)\nSecItemCopyMatching(q as CFDictionary, &out)\n"
                               "try fm.contentsOfDirectory(at: u, includingPropertiesForKeys: nil)"), [])
        self.assertEqual(set(PATTERNS), {"defaults-synchronize", "localized-lookup", "unpaused-animation", "window-frame"})

    def test_compare(self):
        self.assertEqual(compare({"a": {"x": 2}}, {"a": {"x": 1}}), (["a: x 1 -> 2"], []))
        self.assertEqual(compare({"b": {"x": 1}}, {}), (["b: x 0 -> 1"], []))
        self.assertEqual(compare({}, {"a": {"x": 1}}), ([], ["a: x 1 -> 0"]))
        self.assertEqual(compare({"a": {"x": 1}}, {"a": {"x": 1}}), ([], []))


if __name__ == "__main__":
    if sys.argv[1:] == ["--update"]:
        sys.exit(update())
    if sys.argv[1:] == ["--list"]:
        print("\n".join(scan()[1]))
        sys.exit(0)
    unittest.main()
