#!/usr/bin/env python3
"""A PR that changes Persona Core says why (#14916; CONTRIBUTING.md "Changing Persona Core"):

    scripts/hooks/core-change-check.py [--snapshot <file>] [--files-from <file>] <PR number>

Reads the PR's changed files, labels and description through the GitHub CLI (merge-pr.sh passes the changed files of the exact
head it verified with --files-from, so they cannot differ from what it merges). Persona Core has no UI: the check covers
tier `core` of schema/core-paths.txt only (the server core, the clients' headless code, the foundation interfaces). A PR
that changes only the default panel (the clients' interface code), modules, docs or tests passes untouched. When it changes
a tier-core path (a test-only change does not count) the PR must carry the label `core-change` and its description
must have a `## Why this must change the core` section with at least one real line. Anything else passes untouched:
module-only, docs-only and tests-only PRs. Exit 0 = ok, 1 = the PR needs the label or the section, 2 = could not read the PR.

The PR text is data: it is only searched, never run. scripts/hooks/merge-pr.sh calls this before it merges, and a workflow
may call it the same way (the proposal is in the issue).
"""
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "test"))
import core_paths  # noqa: E402

LABEL = "core-change"
HEADING = "## Why this must change the core"
LISTED = 20
PLACEHOLDER = re.compile(r"(|n/?a|none|nothing|no|tbd|todo|to do|-+|\.+|…|x|\?+|<[^>]*>|\[[^\]]*\])[.!]?", re.I)


def reason(body):
    """The first real line of the `## Why this must change the core` section, or None.

    Fenced code is tracked on its own: a fence delimiter is never content and never a heading, and while a fence is open
    no line is a heading, so a `# comment` in quoted code neither ends the section nor opens another one."""
    text = re.sub(r"<!--.*?-->", "", body or "", flags=re.S)
    fence, in_section = None, False
    for line in text.splitlines():
        stripped = line.strip()
        opening = re.match(r"(`{3,}|~{3,})", stripped)
        if fence:
            if opening and stripped[0] == fence[0] and len(opening.group(1)) >= len(fence) and stripped == opening.group(1):
                fence = None  # the closing delimiter
            elif in_section and stripped:
                return stripped  # code in the section is still an explanation
            continue
        if opening:
            fence = opening.group(1)
            continue
        if re.match(r"#{1,6}\s", stripped):
            if in_section:
                return None
            in_section = stripped == HEADING
            continue
        if in_section and stripped and not PLACEHOLDER.fullmatch(re.sub(r"^[-*>]\s*", "", stripped)):
            return stripped
    return None


def evaluate(files, labels, body):
    """(ok, message) for a PR with these changed files (renames: both names), label names and description.

    Advisory only (#17195): a Core change no longer needs the `core-change` label or a reason section to merge.
    The reviewer applies the Core rules strictly instead. This still reports which core files changed and whether
    a label / reason section is present, for visibility; `ok` is always True."""
    touched = core_paths.core_files(files)
    if not touched:
        return True, "core-change-check: no tier-core path changed (panel, module, docs and test changes are free); nothing to add"
    missing = []
    if LABEL not in labels:
        missing.append(f"the label `{LABEL}`")
    if reason(body) is None:
        missing.append(f"a `{HEADING}` section")
    shown = "\n".join(f"  {p}" for p in touched[:LISTED])
    more = f"\n  … and {len(touched) - LISTED} more" if len(touched) > LISTED else ""
    listing = f"{len(touched)} tier-core file{'s' * (len(touched) != 1)} changed (tier core of schema/core-paths.txt):\n{shown}{more}"
    if missing:
        return True, ("core-change-check: this PR changes Persona Core (tier core: no UI); it gets strict Core review "
                      "(#17195) and does not need a label to merge. For visibility you may still add: "
                      + "; ".join(missing) + ".\n" + listing)
    return True, "core-change-check: core change with label and reason.\n" + listing


def gh(*args):
    return subprocess.run(["gh", *args], check=True, capture_output=True, text=True).stdout


def read_files(path):
    """Changed paths from a file of NUL-separated names (`git diff --no-renames --name-only -z`: a rename lists both names)."""
    return [n for n in Path(path).read_bytes().decode("utf-8", "surrogateescape").split("\0") if n]


def read_pr(number, snapshot=None, files_from=None):
    """(files, label names, body); labels and body from the snapshot file (merge-pr.sh's one read of the PR) when given.
    The files come from files_from (merge-pr.sh: the diff of the exact head it verified) or else the PR's files API, which
    follows the PR if it moves."""
    repo = gh("repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").strip()
    if snapshot:
        meta = json.loads(Path(snapshot).read_text(encoding="utf-8"))
    else:
        meta = json.loads(gh("pr", "view", number, "--json", "labels,body"))
    if files_from:
        return read_files(files_from), [l["name"] for l in meta.get("labels") or []], meta.get("body") or ""
    files = []
    pages = gh("api", "--paginate", f"repos/{repo}/pulls/{number}/files?per_page=100", "--jq",
               '.[] | [.filename, (.previous_filename // "")] | @tsv')
    for row in pages.splitlines():
        for name in row.split("\t"):
            if name and name not in files:
                files.append(name)
    return files, [l["name"] for l in meta.get("labels") or []], meta.get("body") or ""


def main(argv):
    args, snapshot, files_from = argv[1:], None, None
    while len(args) > 1 and args[0] in ("--snapshot", "--files-from"):
        if args[0] == "--snapshot":
            snapshot = args[1]
        else:
            files_from = args[1]
        args = args[2:]
    if len(args) != 1 or not args[0].isdigit():
        print("usage: scripts/hooks/core-change-check.py [--snapshot <pr view json file>] [--files-from <NUL-separated names>] "
              "<PR number>", file=sys.stderr)
        return 2
    try:
        files, labels, body = read_pr(args[0], snapshot, files_from)
    except (subprocess.CalledProcessError, ValueError, OSError) as e:
        print(f"core-change-check: could not read PR #{args[0]}: {e}", file=sys.stderr)
        return 2
    ok, message = evaluate(files, labels, body)
    print(message, file=sys.stderr if not ok else sys.stdout)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
