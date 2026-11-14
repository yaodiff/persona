"""The only generic merge exit: scan and squash exactly the reviewed head."""
import argparse
import os
import re
import sys
import tempfile
import time
from pathlib import Path

if os.name == "nt":
    import msvcrt
else:
    import fcntl

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pr_watch_common import WatchError, checked, pr_snapshot, repository, run


def linked_issue(body, repo):
    """Match the issue-link workflow without interpreting PR text as commands."""
    kept = []
    fence = None
    for line in body.splitlines():
        match = re.match(r"^[ \t>]*(`{3,}|~{3,})(.*)$", line)
        if fence:
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= len(fence) and not match.group(2).strip():
                fence = None
            continue
        if match and not (match.group(1)[0] == "`" and "`" in match.group(2)):
            fence = match.group(1)
            continue
        kept.append(line)
    text = re.sub(r"<!--[\s\S]*?-->", " ", "\n".join(kept))
    text = re.sub(r"(?<!`)(`+)(?!`)((?:(?!\n[ \t]*\n)[\s\S])*?[^`])\1(?!`)", " ", text)
    owner, name = repo.split("/", 1)
    own = re.escape(owner) + "/" + re.escape(name)
    keyword = re.compile(r"\b(?:fix(?:e[sd])?|close[sd]?|resolve[sd]?|refs?)(?::\s*|\s+)(?:" + own + r")?#(\d+)\b", re.I)
    link = re.compile(r"\bhttps?://(?:www\.)?github\.com/" + own + r"/issues/(\d+)\b", re.I)
    return sorted(set(int(m.group(1)) for m in keyword.finditer(text)) |
                  set(int(m.group(1)) for m in link.finditer(text)))


def merge_lock(root):
    """Coordinate manual and automated merges using the repository's shared Git directory."""
    common = checked(["git", "rev-parse", "--git-common-dir"], cwd=root).strip()
    directory = Path(root, common).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    handle = (directory / "persona-pr-merge.lock").open("a+")
    if os.name == "nt":
        # Windows Python has no fcntl: lock the first byte, as merge-pr.sh does, trying again each second while held.
        handle.seek(0)
        while True:
            try:
                msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
                break
            except PermissionError:
                time.sleep(1)
    else:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
    return handle


def merge(repo, number, root, privacy=None, timeout=300, expected_head=None, expected_base=None):
    """Do not read or merge PR metadata outside one exact-head decision."""
    repo = repository(repo)
    pr = pr_snapshot(repo, number)
    if expected_head is not None and pr["headRefOid"] != expected_head:
        raise WatchError("the PR head differs from the reviewed head; not merged")
    if expected_base is not None and pr["baseRefOid"] != expected_base:
        raise WatchError("the PR base differs from the reviewed base; not merged")
    if pr["state"] != "OPEN" or pr["isDraft"]:
        raise WatchError("PR is not open and non-draft")
    if pr["mergeable"] != "MERGEABLE" or pr.get("mergeStateStatus") not in ("CLEAN", "HAS_HOOKS", "UNSTABLE"):
        raise WatchError("PR is not mergeable; update its branch or resolve conflicts")
    if pr.get("reviewDecision") == "CHANGES_REQUESTED":
        raise WatchError("PR has an outstanding changes request")
    if not linked_issue(pr["body"], repo):
        raise WatchError("PR does not link an issue in its own repository")
    head, base = pr["headRefOid"], pr["baseRefOid"]
    with tempfile.TemporaryDirectory(prefix="pr-watch-merge-") as temporary:
        temporary = Path(temporary)
        message = temporary / "message"
        body = temporary / "body"
        subject = "%s (#%s)" % (pr["title"], number)
        message.write_text(subject + "\n\n" + pr["body"], encoding="utf-8")
        body.write_text(pr["body"], encoding="utf-8")
        head_ref = "refs/pr-watch/heads/%s" % number
        base_ref = "refs/pr-watch/bases/%s" % number
        checked(["git", "fetch", "-q", "origin", "+refs/heads/" + pr["baseRefName"] + ":" + base_ref],
                cwd=root, timeout=timeout)
        fetched_base = checked(["git", "rev-parse", base_ref], cwd=root).strip()
        if fetched_base != pr["baseRefOid"]:
            raise WatchError("the PR base moved; review and merge it again")
        checked(["git", "fetch", "-q", "origin", "+pull/%s/head:%s" % (number, head_ref)], cwd=root, timeout=timeout)
        fetched_head = checked(["git", "rev-parse", head_ref], cwd=root).strip()
        if fetched_head != head:
            raise WatchError("the PR head moved while fetching; not merged")
        if privacy:
            if not isinstance(privacy, str) or not privacy:
                raise WatchError("privacy command must be an executable path")
            result = run([sys.executable, "-I", privacy, "--range", base + ".." + head,
                          "--message", str(message)], cwd=root, timeout=timeout)
            if result.returncode:
                raise WatchError("privacy scan rejected the exact PR head/message:\n" +
                                 (result.stdout + result.stderr)[-3500:])
        result = run(["gh", "pr", "merge", str(number), "--repo", repo, "--squash",
                      "--match-head-commit", head, "--subject", subject, "--body-file", str(body)],
                     cwd="/", timeout=timeout)
        if result.returncode:
            # gh may report a local cleanup failure after a remote merge. Never
            # blindly retry: read the actual state first.
            latest = pr_snapshot(repo, number)
            if latest["state"] != "MERGED":
                raise WatchError("merge failed: " + (result.stderr or result.stdout)[-1500:])
    latest = pr_snapshot(repo, number)
    if latest["state"] != "MERGED":
        raise WatchError("merge command returned without a merged PR")
    return latest


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--number", type=int, required=True)
    parser.add_argument("--root", required=True)
    parser.add_argument("--privacy", help="optional generic public-content scanner; never reuse another checkout's private values")
    args = parser.parse_args(argv)
    if args.number < 1:
        parser.error("number must be positive")
    lock = merge_lock(args.root)
    merge(args.repo, args.number, args.root, args.privacy)
    print("merged %s#%s" % (args.repo, args.number))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except WatchError as error:
        sys.exit("pr-watch merge: " + str(error))
