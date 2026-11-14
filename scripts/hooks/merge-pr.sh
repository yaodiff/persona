#!/bin/bash
# Merge a PR only after the privacy scan passes on what it changes (CLAUDE.md "Development workflow"):
#   scripts/hooks/merge-pr.sh <PR number>
# Reads the PR once, scans its commits on top of its base and the squash commit's message (`<title> (#N)` and the body)
# with this machine's private values (docs/operations/testing.md "Privacy scan"), then squash-merges exactly the head
# and the message it scanned and deletes the branch. CI cannot do this: it does not have the values.
set -euo pipefail
expect_head="${PR_WATCH_EXPECT_HEAD:-}"
expect_base="${PR_WATCH_EXPECT_BASE:-}"
if [ "${1:-}" = "--expect-head" ]; then
  expect_head="${2:?--expect-head requires a full SHA}"
  expect_base="${4:?--expect-head requires --expect-base and a full SHA}"
  [ "${3:-}" = "--expect-base" ] || { echo "merge-pr: --expect-head requires --expect-base" >&2; exit 2; }
  shift 4
fi
if [ -n "$expect_head" ] && { [ "${#expect_head}" -ne 40 ] || [[ "$expect_head" = *[!0-9a-f]* ]]; }; then
  echo "merge-pr: invalid expected head" >&2
  exit 2
fi
if [ -n "$expect_base" ] && { [ "${#expect_base}" -ne 40 ] || [[ "$expect_base" = *[!0-9a-f]* ]]; }; then
  echo "merge-pr: invalid expected base" >&2
  exit 2
fi
if [ -z "${PR_WATCH_MERGE_LOCKED:-}" ]; then
  export PR_WATCH_MERGE_LOCKED=1
  export PR_WATCH_EXPECT_HEAD="$expect_head"
  export PR_WATCH_EXPECT_BASE="$expect_base"
  root="$(git rev-parse --show-toplevel)"
  # Not --path-format=absolute: git before 2.31 echoes that option back as a line of output.
  git_common="$(cd "$root" && cd "$(git rev-parse --git-common-dir)" && pwd -P)"
  python3 - "$git_common/persona-pr-merge.lock" "$root/scripts/hooks/merge-pr.sh" "$@" <<'PY'
import os, subprocess, sys, time
fd = os.open(sys.argv[1], os.O_RDWR | os.O_CREAT, 0o600)
bash = "bash"
if os.name == "nt":  # Windows Python has no fcntl: lock the file's first byte, trying again each second while held.
    import msvcrt, shutil
    while True:
        try:
            msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
            break
        except PermissionError:
            time.sleep(1)
    # Through PATH (Git Bash puts its own bash first): a bare name would find WSL's bash.exe in System32 first.
    bash = shutil.which("bash") or bash
else:
    import fcntl
    fcntl.flock(fd, fcntl.LOCK_EX)
with open(sys.argv[2], "rb") as source:
    result = subprocess.run([bash, "-s", "--"] + sys.argv[3:], stdin=source, env=os.environ)
raise SystemExit(result.returncode)
PY
  exit "$?"
fi
pr="${1:?usage: scripts/hooks/merge-pr.sh <PR number>}"
case "$pr" in *[!0-9]*) echo "merge-pr: not a PR number: $pr" >&2; exit 2 ;; esac
root="$(git rev-parse --show-toplevel)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/merge-pr.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
# The snapshot and all later writes run while holding persona-pr-merge.lock.
# One snapshot, so every field comes from the same moment. The squash message is passed to the merge instead of being
# left to GitHub, which would take the title and body as they are when it merges, edits after the scan included.
gh pr view "$pr" --json headRefOid,headRefName,isCrossRepository,baseRefName,title,body > "$tmp/pr.json"
python3 - "$tmp" "$pr" <<'PY'
import json, re, sys
tmp, pr = sys.argv[1:]
v = json.load(open(tmp + "/pr.json", encoding="utf-8"))
head, branch, base, title, body = (v.get(k) for k in ("headRefOid", "headRefName", "baseRefName", "title", "body"))
if not (all(isinstance(r, str) and re.fullmatch(r"\S+", r) for r in (head, branch, base))
        and isinstance(title, str) and isinstance(body, str) and isinstance(v.get("isCrossRepository"), bool)):
    sys.exit("merge-pr: unexpected PR fields from gh pr view")
subject = "%s (#%s)" % (title, pr)
same_repo = "" if v["isCrossRepository"] else "yes"
files = {"fields": "".join(line + "\n" for line in (head, branch, same_repo, base)), "subject": subject, "body": body,
         "message": subject + "\n\n" + body}  # what the squash commit records
for name, text in files.items():
    with open(tmp + "/" + name, "w", encoding="utf-8", newline="") as out:
        out.write(text)
PY
{ IFS= read -r head; IFS= read -r branch; IFS= read -r same_repo; IFS= read -r base; } < "$tmp/fields"
subject="$(<"$tmp/subject")"
[ -z "$expect_head" ] || [ "$head" = "$expect_head" ] || { echo "merge-pr: the PR head differs from the reviewed head" >&2; exit 1; }
# The fetched sha, not origin/<base>: that ref only moves when the clone's fetch refspec maps it.
git -C "$root" fetch -q origin "refs/heads/$base"
base_sha="$(git -C "$root" rev-parse FETCH_HEAD)"
[ -z "$expect_base" ] || [ "$base_sha" = "$expect_base" ] || { echo "merge-pr: the PR base differs from the reviewed base" >&2; exit 1; }
git -C "$root" fetch -q origin "pull/$pr/head"
[ "$(git -C "$root" rev-parse FETCH_HEAD)" = "$head" ] || { echo "merge-pr: the PR moved while fetching; run it again" >&2; exit 1; }
python3 "$root/scripts/privacy_check.py" --range "$base_sha..$head" --message "$tmp/message" \
  || { echo "merge-pr: #$pr not merged: fix the hits above in the PR first" >&2; exit 1; }
# From outside the repository, by name: gh then only merges on GitHub. (--delete-branch inside a worktree also checks
# out the base branch locally and fails when another worktree holds it; and it fails when GitHub already deleted the
# branch on merge.) The branch is deleted here instead, if it is still there.
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
(cd / && gh pr merge "$pr" --repo "$repo" --squash --match-head-commit "$head" \
  --subject "$subject" --body-file "$tmp/body")
if [ -n "$same_repo" ] && gh api "repos/$repo/git/ref/heads/$branch" >/dev/null 2>&1; then
  gh api -X DELETE "repos/$repo/git/refs/heads/$branch" >/dev/null 2>&1 || echo "merge-pr: merged; could not delete the branch $branch" >&2
fi
echo "merge-pr: #$pr merged"
