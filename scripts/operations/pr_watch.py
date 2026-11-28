#!/usr/bin/env python3
"""Resident, multi-repository semantic PR review and exact-head merging.

This is maintainer infrastructure, not a Persona hub service. A fresh config is
review-only. GitHub writes and merging require explicit local opt-in.
"""
import argparse
import fcntl
import json
import os
import queue
from concurrent.futures import ThreadPoolExecutor
import sys
import tempfile
import threading
import time
import traceback
from contextlib import contextmanager
from pathlib import Path

# python -I removes the script's directory from sys.path. These modules are the
# trusted sibling implementation, not imports from an untrusted PR checkout.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from pr_watch_common import WatchError, atomic_json, bounded, checked, fingerprint, gh_json, pr_snapshot, repository, run, SHA, verification_env
from pr_watch_merge import linked_issue, merge as generic_merge
import pr_watch_merge
import pr_watch_common
import pr_watch_repair
import pr_watch_recovery
from pr_watch_review import review as semantic_review
import pr_watch_human
from pr_watch_local_gate import require_local_gate, LocalGateError
from pr_watch_verify import verify

FORMAT_VERSION = 1
VERIFICATION_POLICY_VERSION = 3
REVIEW_POLICY_VERSION = 3
REPAIR_LIMIT = 3
INFRASTRUCTURE_ATTEMPT_LIMIT = 3
# `gh pr list` returns the newest PRs first and pages through `--limit` itself,
# so the bound must exceed the open PRs or the oldest are never polled (#14864).
DISCOVERY_LIMIT = 2000


def blocking_findings(findings):
    """Only P0 is a semantic merge gate; P1/P2 remain published notes."""
    return [finding for finding in findings or [] if finding.get("severity") == "P0"]


def finding_anchor(finding):
    """Where a finding points, independent of a reviewer's wording."""
    return (finding.get("path"), finding.get("line"), finding.get("severity"))


def load_config(path):
    try:
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise WatchError("cannot read config: %s" % exc)
    if not isinstance(raw, dict) or not isinstance(raw.get("repositories"), list):
        raise WatchError("config needs a repositories array")
    seen = set()
    repos = []
    for item in raw["repositories"]:
        if not isinstance(item, dict):
            raise WatchError("repository config must be an object")
        repo = repository(item.get("name"))
        if repo in seen:
            raise WatchError("duplicate repository: " + repo)
        seen.add(repo)
        root = item.get("checkout")
        if not isinstance(root, str) or not Path(root).is_absolute():
            raise WatchError("checkout must be an absolute local path")
        base = item.get("base", "main")
        if not isinstance(base, str) or not base or base.startswith("-") or any(c in base for c in " ~^:?*[\\"):
            raise WatchError("invalid base branch")
        if not isinstance(item.get("verify"), dict):
            raise WatchError("verify config must be an object")
        if item.get("merge", False) is not True and item.get("merge", False) is not False:
            raise WatchError("merge must be a boolean")
        if item.get("close_stale", False) is not True and item.get("close_stale", False) is not False:
            raise WatchError("close_stale must be a boolean")
        if item.get("respect_human_reviews", False) is not True and item.get("respect_human_reviews", False) is not False:
            raise WatchError("respect_human_reviews must be a boolean")
        # Every repository here states its own contribution rules. One that has no issue-link workflow opts this gate
        # out instead of the watcher failing every PR it can never merge.
        if item.get("require_issue_link", True) is not True and item.get("require_issue_link", True) is not False:
            raise WatchError("require_issue_link must be a boolean")
        repos.append({"name": repo, "checkout": str(Path(root).expanduser()), "base": base,
                      "verify": item["verify"], "merge": item.get("merge", False),
                      "close_stale": item.get("close_stale", False),
                      "respect_human_reviews": item.get("respect_human_reviews", False),
                      "require_issue_link": item.get("require_issue_link", True),
                      "privacy": item.get("privacy"), "notify_command": item.get("notify_command")})
    reviewer = raw.get("reviewer", {})
    if not isinstance(reviewer, dict):
        raise WatchError("reviewer config must be an object")
    reviewer = dict(reviewer)
    reviewer.setdefault("timeout_seconds", 3600)
    reviewer.setdefault("repair_timeout_seconds", 3600)
    concurrency = reviewer.pop("concurrency", 16)
    webhook = raw.get("webhook", {})
    if not isinstance(webhook, dict):
        raise WatchError("webhook config must be an object")
    # The human-review opt-in only adds an alternative semantic-evidence source
    # behind an off-by-default switch; it never weakens a verification or merge
    # gate, so toggling it must not invalidate existing exact-head evidence.
    # require_issue_link likewise decides a merge-time gate only: the review and
    # verification evidence of an unchanged head stays valid when it is set.
    repository_policy = [{key: value for key, value in item.items()
                          if key not in ("reviewer", "respect_human_reviews", "require_issue_link")}
                         for item in raw["repositories"]]
    return {"repositories": repos, "reviewer": reviewer, "reviewer_concurrency": concurrency, "webhook": webhook,
            "repository_policy": repository_policy,
            "poll_seconds": int(raw.get("poll_seconds", 60)), "workers": int(raw.get("workers", 2)),
            "state": raw.get("state", str(Path(path).parent / "state.json"))}


class State:
    def __init__(self, path):
        self.path = Path(path)
        self.lock = threading.RLock()
        if self.path.exists():
            try:
                self.data = json.loads(self.path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                raise WatchError("cannot read watcher state: %s" % exc)
        else:
            self.data = {}
        if not isinstance(self.data, dict):
            raise WatchError("watcher state must be an object")
        if self.data.get("version") not in (None, FORMAT_VERSION):
            raise WatchError("unsupported watcher state version")
        self.data["version"] = FORMAT_VERSION
        self.data.setdefault("prs", {})

    def get(self, repo, number):
        with self.lock:
            item = self.data["prs"].get(repo + "#" + str(number), {})
            return json.loads(json.dumps(item))

    def annotate(self, repo, number, **fields):
        """Persist lifecycle metadata without invalidating exact-head evidence."""
        key = repo + "#" + str(number)
        with self.lock:
            item = self.data["prs"].setdefault(key, {})
            item.update(fields)
            item["updated_at"] = int(time.time())
            atomic_json(self.path, self.data)
            return dict(item)

    def put(self, repo, number, head, **fields):
        key = repo + "#" + str(number)
        with self.lock:
            item = self.data["prs"].setdefault(key, {})
            evidence_key = fields.get("evidence_key")
            if item.get("head") != head or (evidence_key and item.get("evidence_key") != evidence_key):
                item.clear()
                item["head"] = head
            # Omitted fields retain prior evidence; these fields use None as removal.
            for field in ("failure", "repair_exhausted", "published_failure",
                          "semantic_source", "human_review"):
                if field in fields and fields[field] is None:
                    fields.pop(field, None)
                    item.pop(field, None)
            item.update(fields)
            item["updated_at"] = int(time.time())
            atomic_json(self.path, self.data)
            return dict(item)


def public_base_evidence(evidence):
    """Keep failure identity and reason, never local paths or raw command output."""
    if not isinstance(evidence, dict):
        return str(evidence)
    def clean(item):
        if isinstance(item, dict):
            return {key: clean(value) for key, value in item.items()
                    if key in ("reproduced", "reason", "packages", "tests", "lanes")}
        if isinstance(item, list):
            return [clean(value) for value in item]
        return str(item)
    return json.dumps(clean(evidence), ensure_ascii=False)


class Watcher:
    def __init__(self, config, dry_run=False, verbose=True, allow_config_migration=False):
        self.config = config
        self.allow_config_migration = allow_config_migration
        self.repositories = {item["name"]: item for item in config["repositories"]}
        self.dry_run = dry_run
        self.verbose = verbose
        self.state = State(config["state"])
        self.tasks = queue.PriorityQueue()
        self.queue_sequence = 0
        self.pending = {}
        self.active = {}
        self.wake = threading.Event()
        self.fatal = None
        self.queue_lock = threading.Lock()
        self.git_lock = threading.Lock()
        self.finish_lock = threading.Lock()
        self.merge_locks = {}
        self.merge_locks_guard = threading.Lock()
        self.log_lock = threading.Lock()
        self.stopping = threading.Event()
        self.wake = threading.Event()
        self.instance = None
        self.events = None
        self.workers = []
        self.review_executor = None
        self.review_executor_lock = threading.Lock()
        self.gh_login = None
        self.gh_login_lock = threading.Lock()

    def log(self, message):
        if not self.verbose:
            return
        with self.log_lock:
            print(time.strftime("%Y-%m-%d %H:%M:%S ") + message, flush=True)

    def single_instance(self):
        # Record this configuration's SHA. An old numeric-only state is not
        # silently reused after a repository mapping or gate changes.
        digest = fingerprint("configuration", 0, "0" * 40, "config", self.config)
        marker = Path(self.config["state"]).with_suffix(".config")
        marker.parent.mkdir(parents=True, exist_ok=True)
        previous = self.state.data.get("config_fingerprint")
        if previous is None and not self.state.data.get("prs"):
            previous = digest
        elif previous is None and marker.exists():
            previous = marker.read_text(encoding="utf-8").strip()
        elif previous is None and not self.allow_config_migration:
            raise WatchError("existing watcher state has no configuration fingerprint; use --migrate-config while the service is stopped")
        if previous != digest and not self.allow_config_migration:
            raise WatchError("watcher configuration changed; use a fresh state file or --migrate-config while the service is stopped")
        path = Path(self.config["state"]).with_suffix(".lock")
        path.parent.mkdir(parents=True, exist_ok=True)
        self.instance = path.open("a+")
        try:
            fcntl.flock(self.instance.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise WatchError("another watcher instance already holds the service lock")
        marker.write_text(digest + "\n", encoding="utf-8")
        self.migrate_policy(digest, allow_config_migration=self.allow_config_migration)
        with self.state.lock:
            for lease in self.state.data.get("worktree_leases", {}).values():
                # A newly acquired single-instance lock proves the previous
                # process no longer owns active leases; heartbeat governs age.
                lease["active"] = False
            atomic_json(self.state.path, self.state.data)

    def migrate_policy(self, digest, allow_config_migration=False):
        """Apply reviewed policy changes without silently changing repository mappings."""
        if allow_config_migration and not self.allow_config_migration:
            raise WatchError("configuration migration requires the explicit offline command")
        migration = "review-policy-v2:" + digest
        policy_fingerprint = fingerprint("repository-policy", 0, "0" * 40, "config", self.config.get("repository_policy", self.config["repositories"]))
        with self.state.lock:
            if self.state.data.get("policy_migration") == migration and self.state.data.get("repository_policy") == policy_fingerprint:
                return
            policy_changed = self.state.data.get("repository_policy") not in (None, policy_fingerprint)
            for item in self.state.data["prs"].values():
                # A repository mapping change never reopens terminal work.
                # In particular, a fork's continuation belongs to its original
                # PR and must not be created a second time by a migration.
                if policy_changed and item.get("outcome") not in ("merged", "continuation"):
                    for field in ("semantic", "verification", "verification_policy", "checks", "review_posted", "success_posted",
                                  "review_fingerprint", "published_fingerprint", "published_findings"):
                        item.pop(field, None)
                    item["outcome"] = "retry"
                    item["retry_after"] = 0
                if item.get("head") and not item.get("base"):
                    evidence_key = item.get("evidence_key", "")
                    if "@" in evidence_key and ".." in evidence_key.split("@", 1)[1]:
                        item["base"] = evidence_key.split("@", 1)[1].split("..", 1)[0]
                    elif item.get("outcome") not in ("merged", "continuation"):
                        item["outcome"] = "retry"
                        item["retry_after"] = 0
                        item.pop("semantic", None)
                        item.pop("verification", None)
                        item.pop("review_posted", None)
                if item.get("outcome") in ("merged", "continuation"):
                    continue
                if item.get("review_policy", 1) >= REVIEW_POLICY_VERSION:
                    continue
                item["review_policy"] = REVIEW_POLICY_VERSION
                item["policy_migration"] = migration
                item.setdefault("success_posted", bool(item.get("review_posted") and item.get("semantic") == "passed"
                                                       and item.get("format") == FORMAT_VERSION
                                                       and item.get("outcome") in ("passed", "passed_with_base_failures", "merged")))
                if item.get("repair_exhausted") and not item.get("failure"):
                    item["failure"] = "The previous repair budget was exhausted without a recorded diagnostic; the new three-round budget permits one more attempt."
                if item.get("outcome") == "repair_exhausted" and not item.get("failure_posted"):
                    item["failure_posted"] = False
                if item.get("outcome") == "findings":
                    item["published_findings"] = list(item.get("findings", []))
                    item["review_posted"] = bool(item.get("review_posted"))
                if item.get("outcome") == "failed" and item.get("semantic") == "passed":
                    item["outcome"] = "retry"
                    item["retry_after"] = 0
                    item["retry_reason"] = "failed_verification"
                    item["review_attempts"] = 0
                    item["repair_attempts"] = 0
                if item.get("outcome") in ("reviewing", "repairing", "base_updated", "repair_published"):
                    item["outcome"] = "retry"
                    item["retry_after"] = 0
                elif item.get("outcome") == "findings" and blocking_findings(item.get("findings", [])):
                    item["outcome"] = "retry"
                    item["retry_after"] = 0
                elif item.get("outcome") == "findings":
                    item["semantic"] = "passed"
                    item["outcome"] = "retry"
                    item["retry_after"] = 0
                if item.get("repair_rounds", 0) < REPAIR_LIMIT:
                    item.pop("repair_exhausted", None)
                    if item.get("outcome") == "repair_exhausted":
                        item["outcome"] = "repair_comment_pending"
                        item["retry_after"] = 0
            self.state.data["policy_migration"] = migration
            self.state.data["repository_policy"] = policy_fingerprint
            self.state.data["config_fingerprint"] = digest
            atomic_json(self.state.path, self.state.data)

    def discover(self):
        try:
            for repo, config in self.repositories.items():
                raw = gh_json(["pr", "list", "--repo", repo, "--state", "open", "--json",
                               "number,headRefOid,isDraft,baseRefName,mergeable", "--limit", str(DISCOVERY_LIMIT)])
                if not isinstance(raw, list):
                    raise WatchError("PR discovery did not return an array")
                if len(raw) >= DISCOVERY_LIMIT:
                    self.log("%s: discovery reached its bound of %d open PRs; older PRs are not polled"
                             % (repo, DISCOVERY_LIMIT))
                for item in raw:
                    if not isinstance(item, dict) or not isinstance(item.get("number"), int):
                        raise WatchError("invalid PR discovery entry")
                    if item.get("isDraft") or item.get("baseRefName") != config["base"]:
                        continue
                    if not isinstance(item.get("headRefOid"), str) or not SHA.fullmatch(item["headRefOid"]):
                        raise WatchError("PR discovery entry has no valid head SHA")
                    if self.unchanged_head_complete_or_waiting(repo, item["number"], item["headRefOid"]):
                        continue
                    self.enqueue(repo, item["number"], item["headRefOid"], "poll")
        except WatchError as error:
            # A transient discovery outage must not tear down active reviewers.
            # Keep their exact-head work alive and retry at the normal poll.
            self.log("discovery failed; retaining workers for the next poll: " + str(error))

    def queue_priority(self, repo, number, sha):
        """Prioritize only a migration that process() will actually attempt."""
        old = self.state.get(repo, number)
        evidence_key = repo + "#" + str(number) + "@" + str(old.get("base", "")) + ".." + sha
        if (old.get("head") == sha and old.get("evidence_key") == evidence_key
                and old.get("retry_after", 0) <= time.time()
                and old.get("outcome") != "merge_failed"
                and old.get("format") == FORMAT_VERSION
                and old.get("verification_policy") != VERIFICATION_POLICY_VERSION
                and old.get("verification") not in ("passed", "base_failure")
                and not old.get("repair_exhausted")
                and old.get("outcome") not in ("merged", "continuation", "findings")):
            return 0
        return 1

    def unchanged_head_complete_or_waiting(self, repo, number, sha):
        """A settled head needs no snapshot until its retry or a new head."""
        old = self.state.get(repo, number)
        if old.get("head") != sha or old.get("format") != FORMAT_VERSION:
            return False
        if self.repositories[repo].get("close_stale", False):
            # A settled head is precisely what stale cleanup must keep checking.
            # Preserve only terminal states that already skip that cleanup.
            return old.get("outcome") in ("merged", "continuation")
        return (old.get("retry_after", 0) > time.time()
                or old.get("outcome") in ("merged", "continuation", "repair_published")
                or old.get("outcome") == "merge_failed" and old.get("blocking")
                or bool(old.get("repair_exhausted") and old.get("failure_posted"))
                or old.get("outcome") == "findings" and not self.repositories[repo]["merge"]
                and old.get("semantic") == "findings" and old.get("review_posted")
                and blocking_findings(old.get("findings", [])))

    def enqueue(self, repo, number, sha, source):
        if repo not in self.repositories or not isinstance(number, int) or number < 1:
            return
        key = (repo, number)
        with self.queue_lock:
            if key in self.active:
                old = self.pending.get(key)
                if old != sha:
                    self.pending[key] = sha
                    self.log("%s#%s: newer head noticed while busy" % (repo, number))
                return
            self.active[key] = {"started": time.monotonic(), "phase": "queued", "source": source}
            priority = self.queue_priority(repo, number, sha)
            sequence = self.queue_sequence
            self.queue_sequence += 1
            self.tasks.put((priority, sequence, key))
        self.log("%s#%s queued (%s)" % (repo, number, source))

    def phase(self, key, phase):
        with self.queue_lock:
            if key in self.active:
                self.active[key]["phase"] = phase
                self.active[key]["started"] = time.monotonic()
        self.touch_leases(key)

    def touch_leases(self, key):
        repo, number = key
        with self.state.lock:
            for lease in self.state.data.get("worktree_leases", {}).values():
                if lease.get("repository") == repo and lease.get("number") == number and lease.get("active"):
                    lease["heartbeat"] = time.time()
            try:
                pr_watch_recovery.atomic_leases(self.state.path, self.state.data)
            except OSError as error:
                self.log("lease heartbeat persistence failed; retaining the in-memory lease: " + bounded(error, 300))

    @contextmanager
    def merging(self, repo):
        """Generic merges share the file lock; Persona delegates it to merge-pr.sh."""
        with self.merge_locks_guard:
            lock = self.merge_locks.setdefault(repo, threading.Lock())
        with lock:
            if repo.casefold() == repository("nwverse/persona"):
                yield
            else:
                with pr_watch_merge.merge_lock(self.repositories[repo]["checkout"]):
                    yield

    def worktree(self, repo, number, pr):
        config = self.repositories[repo]
        root = config["checkout"]
        with self.git_lock:
            result = run(["git", "worktree", "prune", "--expire", "now"], cwd=root, timeout=90)
            if result.returncode:
                raise WatchError("cannot prune stale watcher worktree metadata: " + bounded(result.stderr, 800))
        for attempt in range(2):
            try:
                return self.prepare_worktree(repo, number, pr)
            except (WatchError, OSError) as error:
                if attempt or "moved while preparing" in str(error):
                    raise
                with self.git_lock:
                    recovered = pr_watch_recovery.recover_worktree_failure(
                        root, self.config["state"], error, state=self.state, repo=repo)
                if not recovered:
                    raise
                self.log("%s#%s repaired a watcher-owned Git failure; retrying the same head" % (repo, number))
        raise WatchError("Git recovery exhausted before preparing the PR")

    def prepare_worktree(self, repo, number, pr):
        config = self.repositories[repo]
        root = config["checkout"]
        state_root = pr_watch_recovery.owned_root(self.config["state"])
        state_root.mkdir(parents=True, exist_ok=True)
        worktree = Path(tempfile.mkdtemp(prefix="%s-%s-" % (repo.split("/")[1], number), dir=state_root))
        try:
            self.lease_worktree(worktree, repo, number, pr["headRefOid"])
            with self.git_lock:
                identity = checked(["gh", "repo", "view", repo, "--json", "nameWithOwner", "--jq", ".nameWithOwner"], cwd=root)
                if identity.strip() != repo:
                    raise WatchError("checkout does not belong to " + repo)
                base = pr["baseRefOid"]
                # FETCH_HEAD belongs to the checkout, not to this process: merge-pr.sh and
                # every other fetch in the same clone overwrite it between these two lines.
                # Each fetch lands in its own per-PR ref and the check reads that ref (#16759).
                base_ref = "refs/pr-watch/prepare/%s/base" % number
                head_ref = "refs/pr-watch/prepare/%s/head" % number
                checked(["git", "fetch", "-q", "origin", "+refs/heads/" + pr["baseRefName"] + ":" + base_ref],
                        cwd=root, timeout=180)
                fetched_base = checked(["git", "rev-parse", base_ref], cwd=root).strip()
                if fetched_base != base:
                    raise WatchError("base moved while preparing the PR")
                checked(["git", "fetch", "-q", "origin", "+pull/%s/head:%s" % (number, head_ref)], cwd=root, timeout=180)
                if checked(["git", "rev-parse", head_ref], cwd=root).strip() != pr["headRefOid"]:
                    raise WatchError("head moved while preparing the PR")
                # The temporary directory exists only as an mkdtemp reservation.
                worktree.rmdir()
                checked(["git", "worktree", "add", "--detach", str(worktree), pr["headRefOid"]], cwd=root, timeout=180)
            return worktree
        except BaseException:
            try:
                with self.git_lock:
                    pr_watch_recovery.remove_worktree(root, self.config["state"], worktree)
            except (OSError, WatchError) as cleanup_error:
                self.log("temporary worktree cleanup failed: " + bounded(cleanup_error, 300))
            finally:
                self.release_worktree(worktree)
            raise

    def remove_worktree(self, repo, worktree):
        if not worktree:
            return
        with self.git_lock:
            try:
                pr_watch_recovery.remove_worktree(self.repositories[repo]["checkout"], self.config["state"], worktree)
            except (OSError, WatchError) as error:
                self.log("temporary worktree cleanup failed: " + bounded(error, 300))
            finally:
                self.release_worktree(worktree)

    def lease_worktree(self, path, repo, number, head):
        """Persist ownership before any process can use or reclaim the checkout."""
        path = pr_watch_recovery.owned_path(self.config["state"], path)
        with self.state.lock:
            leases = self.state.data.setdefault("worktree_leases", {})
            leases[str(path)] = {"repository": repo, "number": number, "head": head,
                                 "created_at": int(time.time()), "heartbeat": time.time(),
                                 "active": True}
            for suffix in ("-base-check",):
                leases[str(path) + suffix] = dict(leases[str(path)])
            pr_watch_recovery.atomic_leases(self.state.path, self.state.data)

    def release_worktree(self, path):
        path = pr_watch_recovery.owned_path(self.config["state"], path)
        with self.state.lock:
            leases = self.state.data.setdefault("worktree_leases", {})
            lease = leases.get(str(path))
            if lease:
                lease["active"] = False
                lease["released_at"] = time.time()
                alias = leases.get(str(path) + "-base-check")
                if alias:
                    alias["active"] = False
                    alias["released_at"] = lease["released_at"]
                if not path.exists():
                    leases.pop(str(path), None)
                    if not Path(str(path) + "-base-check").exists():
                        leases.pop(str(path) + "-base-check", None)
                pr_watch_recovery.atomic_leases(self.state.path, self.state.data)

    def active_worktrees(self):
        with self.state.lock:
            now = time.time()
            return [path for path, lease in self.state.data.get("worktree_leases", {}).items()
                    if lease.get("active") or now - float(lease.get("heartbeat", 0)) < 86400]

    def recover_infrastructure(self, repo, worktree, error):
        with self.git_lock:
            return pr_watch_recovery.recover_worktree_failure(
                self.repositories[repo]["checkout"], self.config["state"], error,
                protected=self.active_worktrees(), state=self.state, repo=repo)

    def record_review_publication(self, repo, number, pr, findings, posted):
        """Keep PR-level publication history outside replaceable head evidence."""
        key = repo + "#" + str(number)
        with self.state.lock:
            item = self.state.data.setdefault("finding_publications", {}).setdefault(key, {})
            head = pr.get("headRefOid") or pr.get("head")
            if not isinstance(head, str) or not pr_watch_common.SHA.fullmatch(head):
                raise WatchError("review publication requires an exact head")
            entry = item.setdefault(head, {})
            published = list(entry.get("published_findings") or [])
            if posted:
                known = {finding_anchor(f) for f in published}
                published += [f for f in findings if finding_anchor(f) not in known]
            entry.update({"findings": findings, "published_findings": published,
                          "updated_at": int(time.time())})
            atomic_json(self.state.path, self.state.data)

    def head_publication(self, repo, number, head):
        """What this watcher already published for one exact head (#11738).

        Evidence records are replaced whenever the base moves, so publication
        history lives beside them, keyed by head."""
        with self.state.lock:
            return dict(self.state.data.get("finding_publications", {}).get(repo + "#" + str(number), {}).get(head) or {})

    def record_success_publication(self, repo, number, head):
        with self.state.lock:
            item = self.state.data.setdefault("finding_publications", {}).setdefault(repo + "#" + str(number), {})
            item.setdefault(head, {}).update({"success_posted": True, "updated_at": int(time.time())})
            atomic_json(self.state.path, self.state.data)

    def comment_findings(self, repo, number, pr, findings, inline=True):
        head = pr.get("headRefOid") or pr.get("head")
        if not isinstance(head, str) or not pr_watch_common.SHA.fullmatch(head):
            raise WatchError("findings publication requires an exact head")
        if inline:
            # A head's findings are published once (#12302). Re-reviews after
            # main moves word notes differently and find new P1/P2 nits; only a
            # P0 at a new anchor blocks and is worth another review, alone.
            published_findings = self.head_publication(repo, number, head).get("published_findings") or []
            if published_findings:
                published = {finding_anchor(f) for f in published_findings}
                findings = [f for f in blocking_findings(findings) if finding_anchor(f) not in published]
                if not findings:
                    return
        blocking = bool(blocking_findings(findings))
        if not self.repositories[repo]["merge"]:
            action = "Review-only mode: no automatic repair or merge will be attempted. P1/P2 findings are notes; only P0 blocks."
        elif blocking:
            action = "P0 findings block this head; the watcher will use its bounded repair flow. P1/P2 findings are non-blocking notes."
        elif self.dry_run:
            action = "Dry run: P1/P2 findings are non-blocking notes; no automatic verification publication or merge will be attempted."
        else:
            action = "P1/P2 findings are non-blocking notes. Verification and exact-head merging continue."
        if not inline:
            body = "Findings from the previous head; this is not a review of the current head.\n\n%s" % action
        else:
            body = "Automated semantic review at `%s` found %d issue(s).\n\n%s" % (
                head[:9], len(findings), action)
        comments = [{"path": f["path"], "line": f["line"], "side": "RIGHT",
                     "body": "**%s** — %s\n\n**Suggestion:** %s" % (f["severity"], f["summary"], f["suggestion"])}
                    for f in findings]
        payload = {"commit_id": head, "event": "COMMENT", "body": body, "comments": comments}
        if not inline:
            fallback = body + "\n\nThe following findings belong to the previous head; they are grouped because its line anchors are no longer current.\n\n" + "\n\n".join(
                "%d. **%s `%s:%s`** — %s\n   **Suggestion:** %s" %
                (index, f["severity"], f["path"], f["line"], f["summary"], f["suggestion"])
                for index, f in enumerate(findings, 1))
            result = run(["gh", "pr", "review", str(number), "--repo", repo, "--comment", "--body", bounded(fallback, 55000)], timeout=90)
        else:
            result = run(["gh", "api", "repos/%s/pulls/%s/reviews" % (repo, number), "--input", "-"],
                         input_text=json.dumps(payload), timeout=90)
        if result.returncode and inline:
            # A moved diff anchor must not discard the useful review or permit a merge.
            fallback = body + "\n\n" + "\n\n".join(
                "%d. **%s `%s:%s`** — %s\n   **Suggestion:** %s" %
                (index, f["severity"], f["path"], f["line"], f["summary"], f["suggestion"])
                for index, f in enumerate(findings, 1))
            result = run(["gh", "pr", "review", str(number), "--repo", repo, "--comment", "--body", bounded(fallback, 55000)], timeout=90)
        if result.returncode:
            raise WatchError("could not post review findings: " + bounded(result.stderr, 1000))
        self.record_review_publication(repo, number, pr, findings, posted=True)

    def comment(self, repo, number, body):
        result = run(["gh", "pr", "comment", str(number), "--repo", repo, "--body", bounded(body, 55000)], timeout=90)
        if result.returncode:
            raise WatchError("could not post PR comment: " + bounded(result.stderr, 1000))

    def success_review(self, repo, number, pr, results, base_failures=None):
        if self.head_publication(repo, number, pr["headRefOid"]).get("success_posted"):
            # Already said for this exact head; a moved base reran the gates.
            return
        visual_missing = any(result.get("name", "").startswith("Visual acceptance not performed:") for result in results)
        checks = "\n".join("- `%s`" % r["name"] for r in results if r.get("ok") and not r.get("skipped")) or "- No additional successful lanes"
        if base_failures:
            body = ("Automated independent semantic review at `%s` found no blocking defects.\n\n"
                    "Some failed checks were independently reproduced on clean main and are not attributed to this PR. They are not claimed as passing tests:\n\n```text\n%s\n```\n\n"
                    "Checks that actually passed:\n%s") % (
                pr["headRefOid"][:9], bounded(public_base_evidence(base_failures), 6000), checks)
        else:
            body = ("Automated independent semantic review at `%s` found no blocking defects.\n\n"
                    "Local checks completed:\n%s\n\n"
                    "This records only the checks listed above; it does not claim unrun device or visual acceptance.") % (
                pr["headRefOid"][:9], checks)
        if self.repositories[repo]["verify"].get("profile") == "semantic_only":
            body += ("\n\nSemantic-only mode: no full build/vet, cross-platform, build-tag or full-test suite was run. "
                     "Only the lightweight checks listed above and the exact-head merge gates apply.")
        if visual_missing:
            body += "\n\nScreenshot acceptance was not performed: no UI evidence adapter is configured."
        result = run(["gh", "pr", "review", str(number), "--repo", repo, "--comment", "--body", body], timeout=90)
        if result.returncode:
            raise WatchError("could not post successful review: " + bounded(result.stderr, 1000))
        self.record_success_publication(repo, number, pr["headRefOid"])

    def notify(self, config, number, head, outcome):
        command = config.get("notify_command")
        if not command:
            return
        if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
            raise WatchError("notify_command must be an argv array")
        result = run(command + [config["name"], str(number), head, outcome], timeout=90)
        if result.returncode:
            self.log("notification failed: " + bounded(result.stderr, 300))

    def merge(self, config, pr):
        repo, number, root = config["name"], pr["number"], config["checkout"]
        if pr.get("title", "").startswith("release:"):
            # Release PRs belong only to release-pr.yml: ordinary semantic/portable evidence cannot replace its
            # signed-platform verification, TestFlight receipt or protected administrator merge.
            self.log("%s#%s is a release PR; ordinary watcher merging is disabled" % (repo, number))
            return False
        if self.dry_run or not config["merge"]:
            self.log("%s#%s passed; merge disabled" % (repo, number))
            return False
        if repo.casefold() == repository("nwverse/persona"):
            require_local_gate(repo, pr["headRefOid"], pr["baseRefOid"])
            # merge-pr.sh owns the shared lock for this path. Acquiring it in
            # another process while self.merging() holds it would deadlock.
            wrapper = str(Path(__file__).resolve().parents[2] / "scripts" / "hooks" / "merge-pr.sh")
            result = run(["bash", wrapper, "--expect-head", pr["headRefOid"],
                          "--expect-base", pr["baseRefOid"], str(number)], cwd=root, timeout=600)
            if result.returncode:
                raise WatchError("exact-head merge failed: " + bounded(result.stderr or result.stdout, 2000))
        else:
            privacy = config.get("privacy")
            generic_merge(repo, number, root, privacy=privacy, timeout=600,
                          expected_head=pr["headRefOid"], expected_base=pr["baseRefOid"],
                          require_issue_link=config.get("require_issue_link", True))
        if pr_snapshot(repo, number)["state"] != "MERGED":
            raise WatchError("merge returned without a merged PR")
        self.log("%s#%s merged at %s" % (repo, number, pr["headRefOid"][:9]))
        return True

    def exhaust_repair(self, repo, number, pr, evidence_key, failure):
        """Persist a terminal reason before its exactly-once best-effort comment."""
        reason = bounded(str(failure) or "The repair worker did not provide a diagnostic.", 12000)
        previous = self.state.get(repo, number)
        if previous.get("head") != pr["headRefOid"] or previous.get("evidence_key") != evidence_key:
            self.log("%s#%s discarded an exhausted repair result for a superseded head" % (repo, number))
            return False
        rounds = int(previous.get("repair_rounds", 0))
        if rounds < REPAIR_LIMIT and not previous.get("repair_exhausted"):
            # Starting a worker is not a completed repair round. An interrupted
            # publication or infrastructure retry must not fabricate exhaustion.
            self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                           outcome="retry", retry_after=time.time() + 60,
                           repair_rounds=rounds, failure=reason)
            self.log("%s#%s retains its remaining repair budget after an interrupted attempt" % (repo, number))
            return False
        current = self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                                 outcome="repair_exhausted", repair_exhausted=True, failure=reason,
                                 repair_rounds=rounds)
        if not current.get("failure_posted") or current.get("published_failure") != reason:
            if self.dry_run:
                return False
            try:
                self.comment(repo, number, "Automatic repair stopped after %d rounds. The last failure was:\n\n```text\n%s\n```" % (
                        REPAIR_LIMIT, reason))
            except WatchError as error:
                self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                               outcome="repair_exhausted", repair_exhausted=True, failure=reason,
                               failure_posted=False, comment_failure=str(error))
                return False
            self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                           outcome="repair_exhausted", repair_exhausted=True, failure=reason,
                           failure_posted=True, published_failure=reason)
        return False

    def repair_findings(self, config, pr, worktree, findings):
        """Repair P0 findings with the same sandbox, publication and budget as test failures."""
        repo, number = config["name"], pr["number"]
        old = self.state.get(repo, number)
        evidence_key = old.get("evidence_key")
        rounds = int(old.get("repair_rounds", 0))
        reason = "\n".join("%s %s:%s\n%s\nCorrection: %s" % (
            finding["severity"], finding["path"], finding["line"], finding["summary"], finding["suggestion"])
            for finding in findings)
        if rounds >= REPAIR_LIMIT:
            return self.exhaust_repair(repo, number, pr, evidence_key, old.get("failure") or reason)
        latest = pr_snapshot(repo, number)
        if latest["headRefOid"] != pr["headRefOid"] or latest["baseRefOid"] != pr["baseRefOid"]:
            return False
        self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                       outcome="repairing", failure=reason,
                       findings=old.get("findings", findings), semantic="findings", base=pr["baseRefOid"])
        checked(["git", "reset", "--hard", pr["headRefOid"]], cwd=worktree, timeout=90)
        try:
            pr_watch_repair.repair_with_model(repo, pr, str(worktree), reason, self.config["reviewer"])
        except pr_watch_common.RepairAttemptError:
            self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                           repair_rounds=rounds + 1, outcome="repairing", failure=reason)
            raise
        self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                       repair_rounds=rounds + 1, outcome="repairing", failure=reason)
        new_number, checks = (None, [])
        try:
            new_number, checks = pr_watch_repair.commit_and_push_repair(
                repo, pr, str(worktree), pr["baseRefOid"], reason, config["verify"],
                require_issue_link=config.get("require_issue_link", True))
        except pr_watch_common.RepairAttemptError:
            self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                           repair_rounds=rounds + 1, outcome="repairing", failure=reason)
            raise
        except WatchError as error:
            if isinstance(error, pr_watch_common.VerificationError):
                # A concrete failed verification belongs to this repair round.
                self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                               repair_rounds=rounds + 1, outcome="repairing", failure=str(error))
            else:
                # Launch/transport/push outages are not completed repairs.
                self.state.put(repo, number, pr["headRefOid"], evidence_key=evidence_key,
                               repair_rounds=rounds, outcome="repairing", failure=str(error))
            raise
        if new_number == number:
            published = pr_snapshot(repo, number)
            published_head = published["headRefOid"]
            if published_head == pr["headRefOid"]:
                raise WatchError("the repair push returned without advancing the PR head")
            published_key = repo + "#" + str(number) + "@" + published["baseRefOid"] + ".." + published_head
            self.state.put(repo, number, published_head, evidence_key=published_key,
                           base=published["baseRefOid"], repair_rounds=rounds + 1,
                           outcome="retry", retry_after=0, failure=None, checks=checks)
            self.publication_comment(repo, number, published_head, published_key,
                                     "The watcher pushed an automated repair for the P0 semantic findings. The new head will receive a fresh semantic review and verification.")
        else:
            self.record_continuation(repo, pr, evidence_key, new_number, checks)
        return False

    def record_continuation(self, repo, pr, evidence_key, continuation, checks, **fields):
        number, head = pr["number"], pr["headRefOid"]
        self.state.put(repo, number, head, evidence_key=evidence_key, base=pr["baseRefOid"],
                       outcome="continuation", continuation=continuation, failure=None, **fields)
        if checks is None:
            self.log("%s#%s already has continuation PR #%d" % (repo, number, continuation))
        else:
            self.publication_comment(repo, number, head, evidence_key,
                                     "A continuation PR was opened for the fork branch: #%d." % continuation)

    def publication_comment(self, repo, number, head, evidence_key, body):
        """A push is already durable; a later comment outage cannot roll it back."""
        try:
            self.comment(repo, number, body)
        except WatchError as error:
            self.state.put(repo, number, head, evidence_key=evidence_key,
                           comment_failure=str(error))
            self.log("%s#%s publication comment failed after the state was saved: %s" % (
                repo, number, bounded(error, 500)))

    def repair_verification(self, config, pr, worktree, error):
        """Attribute a failure, update a stale base, or perform one repair round."""
        repo, number = config["name"], pr["number"]
        old = self.state.get(repo, number)
        rounds = int(old.get("repair_rounds", 0))
        if rounds >= REPAIR_LIMIT:
            return self.exhaust_repair(repo, number, pr, old.get("evidence_key"), error)
        comparison = pr_watch_repair.main_failure_comparison(repo, pr, str(worktree), getattr(error, "output", "") or str(error),
                                                             git_root=config["checkout"], command=getattr(error, "command", None),
                                                             failures=getattr(error, "failures", None), environ=verification_env())
        if comparison.get("reproduced"):
            self.log("%s#%s failure also reproduces on clean main; continue the remaining gates" % (repo, number))
            self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                           base_failures=comparison, verification="base_failure", checks=getattr(error, "checks", []),
                           outcome="passed_with_base_failures", failure=str(error))
            try:
                self.comment(repo, number, "The failed test(s) also fail on a clean main checkout and are not attributed to this PR. The watcher will continue the remaining gates without claiming those failing tests passed.\n\n```text\n%s\n```" %
                             bounded(str(error), 6000))
            except WatchError as comment_error:
                self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                               outcome="base_comment_pending", failure=str(error),
                               comment_failure=str(comment_error),
                               base_failures=comparison, verification="base_failure",
                               checks=getattr(error, "checks", []))
                return False
            return "base_failure"
        updated_head = pr_watch_repair.update_branch_with_main(
            repo, pr, str(worktree), pr["baseRefOid"], self.config["reviewer"],
            before_conflict=lambda: self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                                                  repair_rounds=rounds + 1, outcome="repairing"))
        if updated_head:
            # main may have moved on since the PR was read (#7840): the merged head stands on the main it brought in
            base = pr_watch_repair.merged_main(str(worktree), updated_head)
            merged_pr = dict(pr, headRefOid=updated_head, baseRefOid=base)
            results = verify(repo, merged_pr, str(worktree), config["verify"], environ=verification_env())
            scanner = Path(__file__).resolve().parents[2] / "scripts" / "privacy_check.py"
            checked(["python3", "-I", str(scanner), "--range", base + ".." + updated_head],
                    cwd=worktree, timeout=180, environ=dict(os.environ, PERSONA_PRIVACY_ROOT=str(worktree)))
            if pr.get("isCrossRepository"):
                new_number, repair_checks = pr_watch_repair.commit_and_push_repair(repo, merged_pr, str(worktree), base, str(error), config["verify"], allow_merge_only=True, original_pr=pr,
                                                                                  require_issue_link=config.get("require_issue_link", True))
                self.record_continuation(repo, pr, old.get("evidence_key"), new_number, repair_checks,
                                         repair_rounds=rounds + 1)
            else:
                pr_watch_repair.push_updated_head(repo, pr, str(worktree), updated_head)
                updated_key = repo + "#" + str(number) + "@" + base + ".." + updated_head
                self.state.put(repo, number, updated_head, evidence_key=updated_key,
                               repair_rounds=rounds + 1, outcome="retry", retry_after=0,
                               failure=None, base=base,
                               retry_reason=None, failure_posted=False,
                               review_attempts=0, repair_attempts=0)
                self.publication_comment(repo, number, updated_head, updated_key,
                                         "The verification branch was behind main. The watcher merged current main into the same-repository branch and will review the new head.")
            return False
        latest = pr_snapshot(repo, number)
        if latest["headRefOid"] != pr["headRefOid"] or latest["baseRefOid"] != pr["baseRefOid"]:
            self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                           outcome="head_moved", repair_rounds=rounds, failure=str(error))
            self.log("%s#%s moved before automatic repair; old checkout discarded" % (repo, number))
            return False
        self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                       outcome="repairing", failure=str(error))
        if latest["headRefOid"] != pr["headRefOid"] or latest["baseRefOid"] != pr["baseRefOid"]:
            self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                           outcome="head_moved", repair_rounds=rounds, failure=str(error))
            self.log("%s#%s moved before automatic repair; old checkout discarded" % (repo, number))
            return False
        checked(["git", "reset", "--hard", pr["headRefOid"]], cwd=worktree, timeout=90)
        try:
            pr_watch_repair.repair_with_model(repo, pr, str(worktree), str(error), self.config["reviewer"])
        except pr_watch_common.RepairAttemptError:
            self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                           repair_rounds=rounds + 1, outcome="repairing", failure=str(error))
            raise
        self.state.put(repo, number, pr["headRefOid"], evidence_key=old.get("evidence_key"),
                       repair_rounds=rounds + 1, outcome="repairing", failure=str(error))
        new_number, repair_checks = pr_watch_repair.commit_and_push_repair(repo, pr, str(worktree), pr["baseRefOid"], str(error), config["verify"],
                                                                          require_issue_link=config.get("require_issue_link", True))
        if new_number == number:
            published = pr_snapshot(repo, number)
            published_head = published["headRefOid"]
            if published_head == pr["headRefOid"]:
                raise WatchError("the repair push returned without advancing the PR head")
            published_key = repo + "#" + str(number) + "@" + published["baseRefOid"] + ".." + published_head
            self.state.put(repo, number, published_head, evidence_key=published_key,
                           base=published["baseRefOid"], repair_rounds=rounds + 1,
                           outcome="retry", retry_after=0, failure=None, checks=repair_checks)
            self.publication_comment(repo, number, published_head, published_key,
                                     "The watcher pushed an automated verification repair. The new head will receive a fresh semantic review and verification.")
        else:
            self.record_continuation(repo, pr, old.get("evidence_key"), new_number, repair_checks)
        return False

    def mergeable(self, pr):
        if pr["state"] != "OPEN" or pr["isDraft"]:
            return False
        if pr["mergeable"] == "CONFLICTING":
            return False
        if pr["mergeable"] == "UNKNOWN":
            # Let the next round observe a definite state; never interpret an
            # unknown GitHub state as either permission or a conflict.
            return False
        if pr["mergeable"] != "MERGEABLE" or pr.get("mergeStateStatus") not in ("CLEAN", "HAS_HOOKS", "UNSTABLE"):
            raise WatchError("PR is not mergeable; update its branch or resolve conflicts")
        if pr.get("reviewDecision") == "CHANGES_REQUESTED":
            raise WatchError("PR has an outstanding changes request")
        return True

    def stale_activity(self, repo, number):
        """Read every activity record through bounded GraphQL cursor pages."""
        owner, name = repository(repo).split("/", 1)
        query = """
query($owner: String!, $name: String!, $number: Int!, $pageSize: Int!,
      $commentsCursor: String, $reviewsCursor: String, $threadsCursor: String,
      $needComments: Boolean!, $needReviews: Boolean!, $needThreads: Boolean!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      comments(first: $pageSize, after: $commentsCursor) @include(if: $needComments) {
        nodes { createdAt body }
        pageInfo { hasNextPage endCursor }
      }
      reviews(first: $pageSize, after: $reviewsCursor) @include(if: $needReviews) {
        nodes { createdAt: submittedAt }
        pageInfo { hasNextPage endCursor }
      }
      reviewThreads(first: $pageSize, after: $threadsCursor) @include(if: $needThreads) {
        nodes {
          comments(first: 100) {
            nodes { createdAt body }
            pageInfo { hasNextPage endCursor }
          }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}
"""
        cursors = {"comments": None, "reviews": None, "threads": None}
        activity = {"comments": [], "reviews": [], "inline": []}
        seen = set()
        for _ in range(1000):
            variables = {"owner": owner, "name": name, "number": number, "pageSize": 100}
            # `False` marks a finished collection here, but the cursor variable is declared String: sending
            # `false` is rejected ("Could not coerce value false to String") on every second page, which used
            # to block every PR with more than one page of activity (#16737). The @include directive already
            # drops a finished collection, so its cursor only has to be a legal value.
            variables.update({field + "Cursor": None if value is False else value
                              for field, value in cursors.items()})
            variables.update({"needComments": cursors["comments"] is not False,
                              "needReviews": cursors["reviews"] is not False,
                              "needThreads": cursors["threads"] is not False})
            response = gh_json(["api", "graphql", "--input", "-"],
                               input_text=json.dumps({"query": query, "variables": variables}), timeout=90)
            try:
                data = response["data"]["repository"]["pullRequest"]
            except (KeyError, TypeError) as error:
                raise WatchError("stale activity returned an incomplete GraphQL response") from error
            if not isinstance(data, dict):
                raise WatchError("stale activity returned an incomplete GraphQL response")
            pending = False
            for field in ("comments", "reviews", "threads"):
                collection = "reviewThreads" if field == "threads" else field
                if cursors[field] is False:
                    if collection in data:
                        raise WatchError("stale activity repeated a completed collection")
                    continue
                connection = data.get(collection)
                if not isinstance(connection, dict) or not isinstance(connection.get("nodes"), list):
                    raise WatchError("stale activity has an invalid " + collection + " collection")
                page = connection.get("pageInfo")
                if not isinstance(page, dict) or not isinstance(page.get("hasNextPage"), bool):
                    raise WatchError("stale activity has invalid pagination")
                if field == "threads":
                    for thread in connection["nodes"]:
                        if not isinstance(thread, dict) or not isinstance(thread.get("comments"), dict):
                            raise WatchError("stale activity has an invalid review thread")
                        nested = thread["comments"]
                        if not isinstance(nested.get("nodes"), list) or not isinstance(nested.get("pageInfo"), dict):
                            raise WatchError("stale activity has invalid review comments")
                        if nested["pageInfo"].get("hasNextPage") is not False:
                            # Never silently close a PR after truncating its
                            # activity. Fetching nested cursors can be added
                            # separately if a thread ever reaches this bound.
                            raise WatchError("stale activity review-thread pagination is incomplete")
                        activity["inline"].extend(nested["nodes"])
                else:
                    activity[field].extend(connection["nodes"])
                if page["hasNextPage"]:
                    cursor = page.get("endCursor")
                    if not isinstance(cursor, str) or not cursor or (field, cursor) in seen:
                        raise WatchError("stale activity pagination has no advancing cursor")
                    seen.add((field, cursor))
                    cursors[field] = cursor
                    pending = True
                else:
                    cursors[field] = False
            if not pending:
                return activity
        raise WatchError("stale activity exceeded its bounded GraphQL pagination")

    def stale_progress_at(self, repo, number, pr, old, ignore_notice=None):
        """Use actual GitHub activity, never aggregate timestamps or polling."""
        activity = self.stale_activity(repo, number)
        comments, reviews, inline = activity["comments"], activity["reviews"], activity["inline"]
        if isinstance(inline, dict):
            inline = [inline]
        if not isinstance(inline, list):
            raise WatchError("stale review comments must be a JSON array")
        if isinstance(comments, dict):
            comments = [comments]
        if isinstance(reviews, dict):
            reviews = [reviews]
        if not isinstance(comments, list) or not isinstance(reviews, list):
            raise WatchError("stale activity endpoints must return JSON arrays")
        if old.get("stale_notice"):
            comments = [item for item in comments if item.get("body") != old["stale_notice"]]
            inline = [item for item in inline if item.get("body") != old["stale_notice"]]
        if ignore_notice is not None:
            # Only the exact notice posted by this invocation is excluded;
            # other new contributor comments still keep the PR open.
            comments = [item for item in comments if item.get("body") != ignore_notice]
            inline = [item for item in inline if item.get("body") != ignore_notice]
        progress = pr_watch_common.progress_timestamp(pr, comments + inline, reviews, [])
        if old.get("last_progress_at"):
            progress = max(progress, float(old["last_progress_at"]))
        # Aggregate PR timestamps include our own reviews and labels. Only a
        # changed head is independent evidence of new work. A same-head update
        # without any contributor comment/review is not enough to close a PR.
        if old.get("stale_head") != pr["headRefOid"]:
            progress = max(progress, pr_watch_common.github_time(pr["updatedAt"]))
        return progress

    def mark_and_close_stale(self, config, pr, old):
        """Explain and mark before closing; neither action counts as new progress."""
        repo, number = config["name"], pr["number"]
        if (not config.get("close_stale", False) or self.dry_run
                or old.get("outcome") == "continuation" or pr["isDraft"]):
            return False
        last_progress = self.stale_progress_at(repo, number, pr, old)
        elapsed = time.time() - last_progress
        if elapsed <= pr_watch_common.STALE_SECONDS:
            return False
        age = int(elapsed // 86400)
        reason = ("This PR has had no substantive progress for more than seven days (%d days since the last "
                  "new commit, review, comment, newly published failure, or automatic repair). It is marked `stale` "
                  "and closed without merging. Reopen it when work resumes; the exact head was not merged." % age)
        result = run(["gh", "pr", "edit", str(number), "--repo", repo, "--add-label", pr_watch_common.STALE_LABEL],
                     timeout=90)
        if result.returncode:
            # GitHub has no built-in `stale` label in every repository. A
            # missing label is a failed marker, not permission to close silently.
            raise WatchError("could not mark the long-inactive PR: " + bounded(result.stderr, 1000))
        self.state.annotate(repo, number, last_progress_at=last_progress,
                            stale_marked_at=int(time.time()), stale_head=pr["headRefOid"],
                            stale_updated_at=pr_watch_common.github_time(pr["updatedAt"]))
        if old.get("stale_notice") != reason:
            self.comment(repo, number, reason)
            self.state.annotate(repo, number, stale_notice=reason)
        latest = pr_snapshot(repo, number)
        if latest["state"] != "OPEN" or latest["isDraft"] or latest["headRefOid"] != pr["headRefOid"]:
            # A push during publication is substantive work, even if it is an
            # old commit. Leave the marked PR open for its next review cycle.
            self.state.annotate(repo, number, stale_head=pr["headRefOid"])
            return False
        marked = self.state.get(repo, number)
        progress = self.stale_progress_at(repo, number, latest, marked, ignore_notice=reason)
        # Only this exact notice is excluded; other comments, reviews and
        # commits still reset the interval. `old` is the pre-mark snapshot, so
        # the marker write cannot make its own timestamp look like new work.
        if progress > last_progress + 1:
            self.state.annotate(repo, number, stale_head=pr["headRefOid"])
            self.state.annotate(repo, number, stale_close_pending=True,
                                last_progress_at=last_progress, stale_activity_at=progress,
                                failure="New substantive activity arrived while the stale notice was published; the PR stays open.")
            return False
        self.state.annotate(repo, number, outcome="stale_close_pending",
                            stale_close_pending=True, semantic=None, verification=None,
                            verification_policy=None, checks=[], success_posted=False,
                            review_posted=False, base_failures=None)
        result = run(["gh", "pr", "close", str(number), "--repo", repo,
                      "--comment", "Closing after the stale notice above: no substantive progress for more than seven days."],
                     timeout=90)
        if result.returncode:
            raise WatchError("could not close the long-inactive PR after its notice: " + bounded(result.stderr, 1000))
        closed = pr_snapshot(repo, number)
        if closed["state"] != "CLOSED":
            raise WatchError("stale-PR close did not reach CLOSED; no stale terminal state was saved")
        self.state.annotate(repo, number, outcome="stale_closed",
                            last_progress_at=last_progress, stale_closed_at=int(time.time()),
                            failure=None)
        self.log("%s#%s closed after %d days without substantive progress" % (repo, number, age))
        self.notify(config, number, pr["headRefOid"], "stale_closed")
        return True

    def human_review_decision(self, repo, number, head, pr_author=None):
        """Fetch and classify real-user reviews bound to this exact head."""
        if self.gh_login is None:
            with self.gh_login_lock:
                if self.gh_login is None:
                    self.gh_login = pr_watch_human.authenticated_login()
        reviews = pr_watch_human.fetch_reviews(repo, number)
        return pr_watch_human.classify_human_reviews(reviews, head, self.gh_login, pr_author=pr_author)

    def apply_human_review_policy(self, repo, number, pr, evidence_key):
        """Apply the opt-in human-review policy before any reviewer call.

        Returns True when the head is parked on a human decision (change
        request or comment-only); an approval records human-sourced semantic
        evidence and returns False so the normal verification and merge gates
        continue. A head without a qualifying human review returns False and
        gets the watcher's own semantic review unchanged.
        """
        head = pr["headRefOid"]
        current = self.state.get(repo, number)
        if (current.get("head") == head and current.get("evidence_key") == evidence_key
                and current.get("format") == FORMAT_VERSION
                and current.get("semantic_source") != "human"
                and current.get("semantic") in ("passed", "findings")):
            # Valid exact-head watcher evidence already exists; never override it.
            # Human evidence instead tracks later reviews on the same head.
            return False
        author = pr.get("author")
        pr_author = author.get("login") if isinstance(author, dict) and isinstance(author.get("login"), str) else None
        decision = (self.human_review_decision(repo, number, head, pr_author=pr_author)
                    if pr_author else self.human_review_decision(repo, number, head))
        kind = decision["decision"]
        if kind == pr_watch_human.DECISION_NONE:
            if (current.get("head") == head and current.get("evidence_key") == evidence_key
                    and current.get("format") == FORMAT_VERSION
                    and current.get("semantic_source") == "human"):
                # A dismissal or superseding neutral state removes the human pass.
                # Keep exact-head verification, but require the normal AI review.
                self.state.put(repo, number, head, evidence_key=evidence_key,
                               base=pr["baseRefOid"], format=FORMAT_VERSION, outcome="reviewing",
                               semantic=None, findings=[], semantic_source=None, human_review=None,
                               review_fingerprint=None, review_posted=False, success_posted=False,
                               published_findings=[], published_fingerprint=None, retry_reason=None)
            return False
        record = decision["review"]
        if kind == pr_watch_human.DECISION_APPROVED:
            # No reviewer call and no watcher review/comment for this head: the
            # human review is the semantic credential; every later gate still runs.
            self.state.put(repo, number, head, evidence_key=evidence_key,
                           base=pr["baseRefOid"], format=FORMAT_VERSION, outcome="reviewed",
                           semantic="passed", findings=[], semantic_source="human",
                           human_review=record, review_attempts=0, retry_reason=None,
                           review_posted=not self.dry_run, success_posted=not self.dry_run)
            self.log("%s#%s uses human %s review %s on %s; skipping the watcher semantic review" %
                     (repo, number, record["state"], record["id"], head[:9]))
            return False
        outcome = ("human_changes_requested" if kind == pr_watch_human.DECISION_CHANGES_REQUESTED
                   else "human_commented")
        if (current.get("head") == head and current.get("evidence_key") == evidence_key
                and current.get("outcome") == outcome
                and (current.get("human_review") or {}).get("id") == record["id"]):
            # Already parked on this exact decision; do not churn state each poll.
            return True
        self.state.put(repo, number, head, evidence_key=evidence_key,
                       base=pr["baseRefOid"], format=FORMAT_VERSION, outcome=outcome,
                       semantic=None, findings=[], semantic_source="human", human_review=record,
                       review_fingerprint=None, review_posted=False, success_posted=False,
                       published_findings=[], published_fingerprint=None, retry_reason=None)
        self.log("%s#%s parked on human %s review %s on %s; no watcher semantic review or merge" %
                 (repo, number, record["state"], record["id"], head[:9]))
        return True

    def semantic_review(self, repo, pr, worktree):
        """Only actual model calls consume the dedicated reviewer pool.

        PR reads, checkout preparation, publication and merging remain on the
        lifecycle workers and never consume a reviewer slot. Submitting to the
        unbounded ready queue refills a slot as soon as a reviewer returns.
        """
        if self.review_executor is None:
            with self.review_executor_lock:
                if self.review_executor is None:
                    self.review_executor = ThreadPoolExecutor(
                        max_workers=self.config.get("reviewer_concurrency", 16),
                        thread_name_prefix="semantic-reviewer")
        return self.review_executor.submit(
            semantic_review, repo, pr, worktree, self.config["reviewer"]).result()

    def process(self, repo, number):
        config = self.repositories[repo]
        key = (repo, number)
        self.phase(key, "reading PR")
        try:
            pr = pr_snapshot(repo, number)
        except WatchError as error:
            self.log("%s#%s snapshot unavailable; no state or merge decision: %s" % (repo, number, bounded(error, 500)))
            return
        if pr["baseRefName"] != config["base"] or pr["state"] != "OPEN" or pr["isDraft"]:
            return
        head = pr["headRefOid"]
        old = self.state.get(repo, number)
        # A continuation covers the fork head even as the base branch advances.
        if old.get("outcome") == "continuation" and old.get("head") == head:
            return
        evidence_key = repo + "#" + str(number) + "@" + pr["baseRefOid"] + ".." + head
        if old.get("head") == head and old.get("format") == FORMAT_VERSION and not config.get("close_stale", False):
            if old.get("retry_after", 0) > time.time() \
                    and old.get("verification_policy") == VERIFICATION_POLICY_VERSION:
                return
            if old.get("outcome") in ("merged", "repair_published"):
                return
            if old.get("repair_exhausted") and old.get("failure_posted"):
                return
            if old.get("outcome") == "merge_failed" and old.get("blocking"):
                return
            if old.get("outcome") == "findings" and old.get("semantic") == "findings" \
                    and old.get("review_posted") and not config["merge"] \
                    and blocking_findings(old.get("findings", [])):
                return
        if old.get("outcome") in ("stale_closed", "stale_close_pending"):
            # Closing or an interrupted close invalidates the old approval.
            # A later reopen must produce new semantic and verification evidence.
            self.state.annotate(repo, number, outcome="retry", semantic=None,
                                verification=None, verification_policy=None,
                                checks=[], success_posted=False, review_posted=False,
                                published_findings=[], published_fingerprint=None,
                                base_failures=None, retry_reason=None, retry_after=0)
            old = self.state.get(repo, number)
        try:
            if self.mark_and_close_stale(config, pr, old):
                return
        except WatchError as error:
            # Marking, commenting and closing are independent GitHub writes.
            # A partial notice never proves that the PR was actually closed.
            self.state.annotate(repo, number,
                                stale_close_failure=str(error),
                                stale_close_attempts=int(old.get("stale_close_attempts", 0)) + 1)
            self.log("%s#%s stale-PR cleanup failed; no merge was attempted: %s" % (
                repo, number, bounded(error, 500)))
            return
        evidence_key = repo + "#" + str(number) + "@" + pr["baseRefOid"] + ".." + head
        if old.get("evidence_key") != evidence_key:
            previous = old
            history = self.state.data.get("finding_publications", {}).get(repo + "#" + str(number), {}).get(previous.get("head"), {})
            published_anchors = {finding_anchor(f) for f in history.get("published_findings") or []}
            unpublished = (previous.get("head") and previous.get("findings")
                           and not all(finding_anchor(f) in published_anchors for f in previous["findings"])
                           and not (previous.get("review_posted") and
                                   previous.get("published_findings", previous["findings"]) == previous["findings"]))
            if unpublished and not self.dry_run:
                # A changed head invalidates line anchors, not the usefulness
                # of the complete old review. Publish it once as grouped text,
                # then review the new head independently.
                self.comment_findings(repo, number, previous, previous["findings"], inline=False)
                self.record_review_publication(repo, number, previous, previous["findings"], posted=True)
            old = {"repair_rounds": int(previous.get("repair_rounds", 0))}
        if (old.get("head") == head and old.get("format") == FORMAT_VERSION
                and old.get("retry_reason") not in ("semantic_review", "verification_repair", "semantic_repair", "base_update")
                and old.get("verification_policy") != VERIFICATION_POLICY_VERSION
                and old.get("verification") not in ("passed", "base_failure")
                and not old.get("repair_exhausted")
                and old.get("outcome") not in ("merged", "continuation", "findings", "repair_comment_pending", "reviewed",
                                               "human_changes_requested", "human_commented")):
            # A prerequisite failure is not evidence against this exact head.
            # Retry only its verification under the new policy. Completed gates,
            # published findings and exhausted repair budgets keep their meaning.
            old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                 verification_policy=VERIFICATION_POLICY_VERSION,
                                 outcome="retry", retry_after=0,
                                 repair_rounds=int(old.get("repair_rounds", 0)),
                                 repair_attempts=0, review_attempts=0,
                                 semantic=old.get("semantic"), findings=old.get("findings", []),
                                 review_fingerprint=old.get("review_fingerprint"),
                                 review_posted=old.get("review_posted", False),
                                 base=pr["baseRefOid"], format=FORMAT_VERSION)
        if old.get("head") == head and old.get("evidence_key") == evidence_key and old.get("retry_after", 0) > time.time():
            return
        if old.get("head") == head and old.get("evidence_key") == evidence_key \
                and old.get("retry_reason") in ("semantic_review", "verification_repair", "semantic_repair", "base_update") \
                and ((old.get("retry_reason") == "semantic_review" and int(old.get("review_attempts", 0)) >= INFRASTRUCTURE_ATTEMPT_LIMIT)
                     or (old.get("retry_reason") in ("verification_repair", "semantic_repair", "base_update")
                         and int(old.get("repair_attempts", 0)) >= INFRASTRUCTURE_ATTEMPT_LIMIT)):
            # The completed backoff permits a fresh independent attempt budget.
            old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                 review_attempts=0, repair_attempts=0, retry_after=0)
        if pr.get("isCrossRepository"):
            # Skip review when another continuation already contains this head.
            # A failed lookup raises so a later poll can retry it.
            existing = pr_watch_repair.existing_continuation(repo, pr)
            if existing:
                self.record_continuation(repo, pr, evidence_key, existing, None,
                                         repair_rounds=int(old.get("repair_rounds", 0)), format=FORMAT_VERSION)
                return
        if (old.get("head") == head and old.get("evidence_key") == evidence_key
                and old.get("outcome") == "retry" and old.get("retry_reason") == "failed_verification"):
            # The migration record is a failed verification, not a success to
            # publish. Reuse the bounded repair path with its saved diagnostic.
            worktree = self.worktree(repo, number, pr)
            try:
                return self.repair_verification(config, pr, worktree,
                                                pr_watch_common.VerificationError(old.get("failure") or "recorded verification failure"))
            finally:
                self.remove_worktree(repo, worktree)
        if (old.get("head") == head and old.get("evidence_key") == evidence_key
                and old.get("outcome") == "findings" and old.get("semantic") == "findings" and old.get("review_posted")
                and blocking_findings(old.get("findings", []))
                and not config["merge"]):
            return
        if old.get("outcome") == "repair_comment_pending":
            if old.get("head") != head or old.get("evidence_key") != evidence_key:
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     outcome="retry", retry_after=0, failure=None,
                                     repair_comment_posted=False)
            elif self.dry_run:
                return False
            else:
                reason = old.get("failure") or "The previous repair budget was exhausted without a recorded diagnostic."
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     outcome="repair_comment_pending", repair_comment_posted=False,
                                     failure=reason)
                try:
                    self.comment(repo, number, "The previous repair budget was exhausted. The last failure was:\n\n```text\n%s\n```\n\nThe new three-round budget permits one more repair attempt." % bounded(reason, 12000))
                except WatchError as error:
                    self.state.put(repo, number, head, evidence_key=evidence_key,
                                   outcome="repair_comment_pending", repair_comment_posted=False,
                                   comment_failure=str(error), failure=reason)
                    return False
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     outcome="retry", retry_after=0, failure=None,
                                     repair_comment_posted=True)
        if old.get("outcome") in ("repair_published", "base_updated") \
                and old.get("head") == head and old.get("evidence_key") == evidence_key:
            return
        if old.get("outcome") == "base_comment_pending":
            if not self.dry_run:
                self.comment(repo, number, "The failed test(s) also fail on a clean main checkout and are not attributed to this PR. The watcher will continue the remaining gates without claiming those failing tests passed.\n\n```text\n%s\n```" %
                             bounded(old.get("failure", ""), 6000))
            old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                 outcome="passed_with_base_failures", review_posted=old.get("review_posted", False),
                                 verification=old.get("verification"), checks=old.get("checks", []),
                                 base_failures=old.get("base_failures"), base_comment_posted=True)
        if old.get("repair_exhausted") and old.get("evidence_key") == evidence_key:
            if not old.get("failure_posted"):
                self.exhaust_repair(repo, number, pr, evidence_key, old.get("failure"))
            return
        if old.get("repair_exhausted") and old.get("head") == head:
            # Legacy records may predate evidence keys. Bind them to the
            # current exact head only for terminal-comment publication.
            if old.get("base") == pr["baseRefOid"]:
                self.state.put(repo, number, head, evidence_key=evidence_key,
                               base=pr["baseRefOid"], outcome="repair_exhausted",
                               repair_exhausted=True)
                self.exhaust_repair(repo, number, pr, evidence_key, old.get("failure"))
            return
        if (old.get("head") == head and old.get("evidence_key") == evidence_key
                and old.get("format") == FORMAT_VERSION and old.get("outcome") == "reviewed"
                and old.get("semantic") in ("findings", "passed") and old.get("findings")):
            if self.dry_run:
                return False
            if not old.get("review_posted"):
                self.comment_findings(repo, number, pr, old["findings"])
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     review_posted=True, published_findings=old["findings"],
                                     published_fingerprint=old.get("review_fingerprint"))
            if not config["merge"] and old.get("semantic") == "findings":
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     outcome="findings")
                return False
            if old.get("semantic") == "passed":
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     outcome="passed_with_findings")
            else:
                old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                     outcome="findings")
        if (old.get("head") == head and old.get("evidence_key") == evidence_key
                and old.get("format") == FORMAT_VERSION and old.get("outcome") == "retry"
                and old.get("semantic") == "passed" and old.get("retry_reason") != "verification_repair"
                and old.get("verification") in ("passed", "base_failure")
                and not blocking_findings(old.get("findings", []))):
            # A completed retry retains valid exact-head evidence. Normalize
            # it into the existing publication path instead of verifying in a
            # loop merely because an infrastructure retry once set `retry`.
            if old.get("verification") == "base_failure":
                outcome = "passed_with_base_failures"
            else:
                outcome = "passed_with_findings" if old.get("findings") else "passed"
            old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                 outcome=outcome, retry_reason=None, retry_after=0)
        # Only human evidence resuming verification or merging follows later same-head reviews.
        if old.get("head") == head and old.get("evidence_key") == evidence_key and old.get("format") == FORMAT_VERSION \
                and (old.get("semantic_source") != "human" or old.get("outcome") not in
                     ("passed", "passed_with_base_failures", "passed_with_findings", "retry", "reviewed")) \
                and old.get("outcome") in ("findings", "failed", "merged", "passed", "passed_with_base_failures", "passed_with_findings", "repairing", "retry", "reviewed") \
                and (old.get("outcome") != "retry" or old.get("semantic") == "passed" and old.get("verification") in ("passed", "base_failure") and old.get("retry_reason") != "verification_repair") \
                and (old.get("outcome") not in ("passed", "passed_with_base_failures", "passed_with_findings")
                     or old.get("verification") in ("passed", "base_failure")) \
                and not blocking_findings(old.get("findings", [])):
            if old.get("outcome") == "merged":
                return
            if self.dry_run:
                return
            if old.get("review_posted") and not config["merge"] and old.get("verification") in ("passed", "base_failure"):
                return
            if old.get("review_posted") and not (old.get("outcome") == "passed_with_base_failures"
                                                and old.get("semantic") == "passed") \
                    and not (old.get("outcome") in ("passed_with_findings", "passed") and not old.get("success_posted")) \
                    and old.get("outcome") != "reviewed" \
                    and not (old.get("outcome") == "findings" and old.get("semantic") == "findings" and config["merge"]) \
                    and old.get("outcome") != "repairing" \
                    and not (old.get("outcome") == "passed_with_findings" and old.get("verification") in ("passed", "base_failure")
                             and not blocking_findings(old.get("findings", []))) \
                    and not (old.get("outcome") == "passed_with_findings" and old.get("merge_attempts", 0) > 0
                             and not old.get("success_posted")):
                return
            if old.get("outcome") in ("passed", "passed_with_base_failures", "passed_with_findings") \
                    and old.get("format") == FORMAT_VERSION and old.get("semantic") == "passed" \
                    and not blocking_findings(old.get("findings", [])) \
                    and old.get("verification") in ("passed", "base_failure") and old.get("success_posted") and config["merge"]:
                if old.get("merge_attempts", 0) > 0 and old.get("retry_after", 0) > time.time():
                    return
                with self.merging(repo):
                    current_pr = pr_snapshot(repo, number)
                    if current_pr["headRefOid"] != head or current_pr["baseRefOid"] != pr["baseRefOid"] \
                            or not self.mergeable(current_pr):
                        return
                    if config.get("require_issue_link", True) and not linked_issue(current_pr["body"], repo):
                        raise WatchError("PR has no linked issue; not merged")
                    self.finish_merge(config, current_pr, old)
                return
            if old.get("outcome") in ("passed", "passed_with_base_failures", "passed_with_findings") \
                    and old.get("format") == FORMAT_VERSION and old.get("semantic") == "passed" \
                    and not blocking_findings(old.get("findings", [])) \
                    and old.get("verification") in ("passed", "base_failure") and not old.get("success_posted") and config["merge"]:
                if old.get("findings") and not old.get("review_posted"):
                    published = old.get("published_findings", [])
                    if published != old["findings"]:
                        self.comment_findings(repo, number, pr, old["findings"])
                    old = self.state.put(repo, number, head, evidence_key=evidence_key,
                                         review_posted=True, published_findings=old["findings"],
                                         published_fingerprint=old.get("review_fingerprint"))
                with self.merging(repo):
                    current_pr = pr_snapshot(repo, number)
                    if current_pr["headRefOid"] != head or current_pr["baseRefOid"] != pr["baseRefOid"] \
                            or not self.mergeable(current_pr):
                        return
                    if config.get("require_issue_link", True) and not linked_issue(current_pr["body"], repo):
                        raise WatchError("PR has no linked issue; not merged")
                    try:
                        self.success_review(repo, number, current_pr, old.get("checks", []), old.get("base_failures"))
                    except WatchError as error:
                        self.state.put(repo, number, head, evidence_key=evidence_key,
                                       outcome=old.get("outcome", "passed"), retry_after=time.time() + 60,
                                       success_posted=False, failure=str(error))
                        return False
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             review_posted=True, success_posted=True)
                    self.finish_merge(config, current_pr, current)
                return
            # A dry run keeps semantic/test evidence but has no external side
            # effects. A live run resumes publication and the merge gate.
            if not blocking_findings(old.get("findings", [])):
                if not old.get("review_posted"):
                    self.comment_findings(repo, number, pr, old.get("findings", []))
                    self.state.put(repo, number, head, evidence_key=evidence_key, review_posted=True)
                # a completed exact-head gate into a new review every poll.
                # a completed exact-head gate into a new review every poll.
                if old.get("verification") not in ("passed", "base_failure"):
                    return
                old = self.state.get(repo, number)
            elif old.get("semantic") == "findings":
                if not old.get("review_posted"):
                    self.comment_findings(repo, number, pr, old.get("findings", []))
                    self.state.put(repo, number, head, evidence_key=evidence_key, review_posted=True)
                if not config["merge"]:
                    return
                old = self.state.get(repo, number)
            if old.get("outcome") == "passed_with_base_failures" and old.get("semantic") == "passed" and not old.get("review_posted"):
                old = dict(old, outcome="passed", review_posted=False)
            if old.get("outcome") == "failed":
                if old.get("failure_posted"):
                    return
                self.comment(repo, number, "Automated review at `%s` did not pass. Not merged.\n\n```text\n%s\n```" %
                             (head[:9], bounded(old.get("failure", "verification failed"), 14000)))
                self.state.put(repo, number, head, evidence_key=evidence_key, failure_posted=True)
                return
        if not self.mergeable(pr):
            if pr["mergeable"] == "CONFLICTING" and not self.dry_run:
                worktree = None
                try:
                    rounds = int(self.state.get(repo, number).get("repair_rounds", 0))
                    if rounds >= REPAIR_LIMIT:
                        current = self.state.get(repo, number)
                        if current.get("head") != head or current.get("evidence_key") != evidence_key:
                            self.state.put(repo, number, head, evidence_key=evidence_key,
                                           base=pr["baseRefOid"], repair_rounds=rounds,
                                           repair_exhausted=None, failure_posted=False,
                                           published_failure=None, failure=None)
                        if not self.state.get(repo, number).get("repair_exhausted"):
                            self.exhaust_repair(repo, number, pr, evidence_key,
                                                "The PR still conflicts with main after %d repair rounds; no further base update was attempted." % REPAIR_LIMIT)
                        return
                    conflict_started = [False]
                    def before_conflict():
                        conflict_started[0] = True
                        self.state.put(repo, number, head, evidence_key=evidence_key,
                                       repair_rounds=rounds + 1, outcome="repairing")
                    worktree = self.worktree(repo, number, pr)
                    updated_head = pr_watch_repair.update_branch_with_main(
                        repo, pr, str(worktree), pr["baseRefOid"], self.config["reviewer"],
                        before_conflict=before_conflict)
                    if updated_head:
                        base = pr_watch_repair.merged_main(str(worktree), updated_head)
                        merged_pr = dict(pr, headRefOid=updated_head, baseRefOid=base)
                        results = verify(repo, merged_pr, str(worktree), config["verify"], environ=verification_env())
                        scanner = Path(__file__).resolve().parents[2] / "scripts" / "privacy_check.py"
                        checked(["python3", "-I", str(scanner), "--range", base + ".." + updated_head],
                                cwd=worktree, timeout=180, environ=dict(os.environ, PERSONA_PRIVACY_ROOT=str(worktree)))
                        if pr.get("isCrossRepository"):
                            new_number, repair_checks = pr_watch_repair.commit_and_push_repair(repo, merged_pr, str(worktree), base, "conflicting base", config["verify"], allow_merge_only=True, original_pr=pr,
                                                                                              require_issue_link=config.get("require_issue_link", True))
                            self.record_continuation(repo, pr, evidence_key, new_number, repair_checks,
                                                     repair_rounds=rounds + 1)
                        else:
                            pr_watch_repair.push_updated_head(repo, pr, str(worktree), updated_head)
                            updated_key = repo + "#" + str(number) + "@" + base + ".." + updated_head
                            self.state.put(repo, number, updated_head, evidence_key=updated_key,
                                           repair_rounds=rounds + 1, outcome="retry", retry_after=0,
                                           failure=None, base=base,
                               retry_reason=None, failure_posted=False,
                               review_attempts=0, repair_attempts=0)
                            self.comment(repo, number, "The watcher merged current main into the same-repository branch to resolve the stale base. The new head will be reviewed.")
                except WatchError as error:
                    self.recover_infrastructure(repo, worktree, error)
                    attempts = int(self.state.get(repo, number).get("repair_attempts", 0)) + 1
                    rounds = int(self.state.get(repo, number).get("repair_rounds", 0))
                    if rounds > REPAIR_LIMIT or rounds == REPAIR_LIMIT and conflict_started[0]:
                        return self.exhaust_repair(repo, number, pr, evidence_key, error)
                    if attempts >= INFRASTRUCTURE_ATTEMPT_LIMIT:
                        self.state.put(repo, number, head, evidence_key=evidence_key,
                                       outcome="retry", repair_attempts=attempts,
                                       retry_after=time.time() + 3600,
                                       retry_reason="base_update", failure=str(error))
                        if not self.state.get(repo, number).get("failure_posted") and not self.dry_run:
                            self.comment(repo, number, "Automatic base update could not complete after three attempts. No merge was attempted.\n\n```text\n%s\n```" % bounded(str(error), 6000))
                            self.state.put(repo, number, head, evidence_key=evidence_key,
                                           outcome="retry", repair_attempts=attempts, retry_after=time.time() + 3600,
                                           retry_reason="base_update", failure=str(error), failure_posted=True)
                    else:
                        self.state.put(repo, number, head, evidence_key=evidence_key,
                                       outcome="retry", repair_attempts=attempts,
                                       retry_after=time.time() + 60 * attempts, failure=str(error))
                    self.log("%s#%s automatic base update failed: %s" % (repo, number, bounded(error, 500)))
                finally:
                    self.remove_worktree(repo, worktree)
            return
        phase = "merge"
        if old.get("head") == head and old.get("outcome") == "merge_failed" \
                and old.get("semantic_source") != "human":
            if old.get("retry_after", 0) > time.time():
                return
            if old.get("blocking"):
                return
            self.phase(key, "retrying exact-head merge")
            with self.merging(repo):
                current = pr_snapshot(repo, number)
                if not self.mergeable(current) or current["headRefOid"] != head or current["baseRefOid"] != pr["baseRefOid"]:
                    return
                self.finish_merge(config, current, old)
            return
        worktree = None
        phase = "preparing"
        evidence_key = repo + "#" + str(number) + "@" + pr["baseRefOid"] + ".." + head
        try:
            if config.get("respect_human_reviews"):
                # A real user's review on this exact head can replace the
                # watcher's semantic review. Failures use the semantic-gate
                # retry path below; no worktree is needed for parked heads.
                phase = "semantic"
                self.phase(key, "checking human review state")
                if self.apply_human_review_policy(repo, number, pr, evidence_key):
                    return False
                phase = "preparing"
            self.phase(key, "preparing isolated checkout")
            worktree = self.worktree(repo, number, pr)
            phase = "semantic"
            current = self.state.get(repo, number)
            if current.get("evidence_key") != evidence_key or current.get("format") != FORMAT_VERSION:
                rounds = int(self.state.get(repo, number).get("repair_rounds", 0))
                current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                         base=pr["baseRefOid"], format=FORMAT_VERSION, outcome="reviewing",
                                         verification_policy=VERIFICATION_POLICY_VERSION,
                                         review_policy=REVIEW_POLICY_VERSION,
                                         repair_rounds=rounds)
            if current.get("semantic") != "passed" and current.get("semantic") != "findings" or current.get("retry_reason") in ("semantic_review", "base_update"):
                self.phase(key, "independent semantic review")
                review_attempts = int(current.get("review_attempts", 0))
                if review_attempts >= INFRASTRUCTURE_ATTEMPT_LIMIT:
                    self.state.put(repo, number, head, evidence_key=evidence_key,
                                   outcome="retry", review_attempts=review_attempts,
                                   retry_after=time.time() + 3600,
                                   retry_reason="semantic_review",
                                   failure=current.get("failure") or "semantic review is unavailable")
                    return False
                findings = None
                for attempt in range(INFRASTRUCTURE_ATTEMPT_LIMIT - review_attempts):
                    try:
                        findings = self.semantic_review(repo, pr, str(worktree))
                        break
                    except WatchError as error:
                        attempt_number = review_attempts + attempt + 1
                        current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                                 outcome="retry", review_attempts=attempt_number,
                                                 retry_after=time.time() + 3600 if attempt_number >= INFRASTRUCTURE_ATTEMPT_LIMIT else 0,
                                                 retry_reason="semantic_review")
                        if attempt_number >= INFRASTRUCTURE_ATTEMPT_LIMIT:
                            raise
                        self.log("%s#%s retrying semantic review after infrastructure failure: %s" % (
                            repo, number, bounded(error, 500)))
                if findings is None:
                    raise WatchError(current.get("failure") or "semantic review exhausted its attempts without a valid answer")
                blocking = blocking_findings(findings)
                semantic = "findings" if blocking else "passed"
                review_fingerprint = fingerprint(repo, number, head, "semantic", findings)
                published = current.get("published_findings", [])
                published_fingerprint = (fingerprint(repo, number, head, "semantic", published)
                                         if published and current.get("review_posted")
                                         else current.get("review_fingerprint") if current.get("review_posted") else None)
                current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                         semantic=semantic, findings=findings,
                                         review_fingerprint=review_fingerprint,
                                         outcome="reviewed",
                                         review_attempts=0,
                                         publication_attempts=0,
                                         retry_reason=None,
                                         review_posted=bool(current.get("review_posted") and
                                                            published_fingerprint == review_fingerprint))
            findings = current.get("findings", [])
            if not current.get("review_posted") and findings:
                phase = "publication"
                self.phase(key, "posting actionable findings")
                if not self.dry_run:
                    self.comment_findings(repo, number, pr, findings)
                phase = "semantic"
                current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                         review_posted=not self.dry_run,
                                         published_findings=findings if not self.dry_run else current.get("published_findings", []),
                                         published_fingerprint=current.get("review_fingerprint"))
            if blocking_findings(findings):
                if self.dry_run or not config["merge"]:
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             outcome="findings")
                    self.log("%s#%s has blocking semantic findings; repair is not enabled" % (repo, number))
                    return False
                phase = "semantic_repair"
                return self.repair_findings(config, pr, worktree, blocking_findings(findings))
            if current.get("findings"):
                current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                         outcome="passed_with_findings")
            if current.get("verification") not in ("passed", "base_failure"):
                self.phase(key, "repository verification")
                phase = "verification"
                try:
                    results = verify(repo, pr, str(worktree), config["verify"], environ=verification_env())
                except pr_watch_common.VerificationError as error:
                    if self.dry_run:
                        raise
                    if self.state.get(repo, number).get("repair_rounds", 0) >= REPAIR_LIMIT:
                        return self.exhaust_repair(repo, number, pr, evidence_key, error)
                    repaired = self.repair_verification(config, pr, worktree, error)
                    if repaired != "base_failure":
                        return
                    current = self.state.get(repo, number)
                    base_evidence = current.get("base_failures")
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             verification="base_failure", checks=error.checks,
                                             verification_policy=VERIFICATION_POLICY_VERSION,
                                             base_failures=base_evidence)
                else:
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             verification="passed", checks=results,
                                             verification_policy=VERIFICATION_POLICY_VERSION)
            if current.get("verification") == "base_failure":
                self.phase(key, "continuing after a clean-main failure")
            with self.merging(repo):
                phase = "merge"
                self.phase(key, "rechecking exact head")
                current_pr = pr_snapshot(repo, number)
                if (current_pr["headRefOid"] != head or current_pr["baseRefOid"] != pr["baseRefOid"]
                        or not self.mergeable(current_pr)):
                    self.log("%s#%s moved during review; old result discarded" % (repo, number))
                    return
                if config.get("require_issue_link", True) and not linked_issue(current_pr["body"], repo):
                    raise WatchError("PR has no linked issue; not merged")
                if not current.get("success_posted"):
                    self.phase(key, "posting successful review")
                    if not self.dry_run:
                        self.success_review(repo, number, current_pr, current.get("checks", []),
                                            current.get("base_failures"))
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             review_posted=not self.dry_run,
                                             success_posted=not self.dry_run)
                self.finish_merge(config, current_pr, current)
        except WatchError as error:
            if phase in ("verification", "semantic_repair"):
                self.recover_infrastructure(repo, worktree, error)
            if phase == "verification":
                attempts = int(self.state.get(repo, number).get("repair_attempts", 0)) + 1
                latest = self.state.get(repo, number)
                if isinstance(error, pr_watch_common.RepairAttemptError) and int(latest.get("repair_rounds", 0)) >= REPAIR_LIMIT:
                    return self.exhaust_repair(repo, number, pr, evidence_key, error)
                failure_fingerprint = fingerprint(repo, number, head, "repair-infrastructure", str(error))
                publish = latest.get("infrastructure_fingerprint") != failure_fingerprint
                self.state.put(repo, number, head, evidence_key=evidence_key,
                               outcome="retry",
                               repair_attempts=attempts,
                               repair_rounds=int(latest.get("repair_rounds", 0)),
                               verification=latest.get("verification"), checks=latest.get("checks", []),
                               base_failures=latest.get("base_failures"),
                               retry_after=time.time() + 300, failure=str(error),
                               retry_reason="verification_repair",
                               review_posted=latest.get("review_posted"),
                               published_findings=latest.get("published_findings", []),
                               published_fingerprint=latest.get("published_fingerprint"),
                               infrastructure_fingerprint=latest.get("infrastructure_fingerprint"))
                if attempts >= INFRASTRUCTURE_ATTEMPT_LIMIT:
                    # An unavailable repair worker is not a successfully
                    # completed repair round. Keep its budget intact but leave
                    # a durable diagnostic and a bounded later retry.
                    self.state.put(repo, number, head, evidence_key=evidence_key,
                                   outcome="retry",
                                   repair_attempts=attempts, repair_rounds=int(latest.get("repair_rounds", 0)),
                                   retry_after=time.time() + 3600, retry_reason="verification_repair",
                                   failure=str(error),
                                   review_posted=latest.get("review_posted"),
                                   published_findings=latest.get("published_findings", []),
                                   published_fingerprint=latest.get("published_fingerprint"),
                                   infrastructure_fingerprint=latest.get("infrastructure_fingerprint"))
                if not self.dry_run and publish:
                    self.comment(repo, number, "Automatic verification repair could not complete.\n\n```text\n%s\n```" %
                                 bounded(str(error), 12000))
                    self.state.put(repo, number, head, evidence_key=evidence_key,
                                   infrastructure_fingerprint=failure_fingerprint)
                return
            # A missing semantic answer never becomes approval. Verification
            # Infrastructure failures remain retryable with capped backoff;
            # verification failures and semantic findings stay terminal for
            # this exact base/head until the PR changes.
            human_review_retry = (phase == "semantic" and config.get("respect_human_reviews")
                                  and old.get("head") == head and old.get("evidence_key") == evidence_key
                                  and old.get("semantic_source") == "human")
            # Re-reading stored human evidence stays retryable even after verification passed.
            retry = (phase in ("preparing", "semantic") and old.get("verification") != "passed") or human_review_retry
            exhausted_review = (phase == "semantic" and
                                int(self.state.get(repo, number).get("review_attempts", 0)) >= INFRASTRUCTURE_ATTEMPT_LIMIT)
            if phase == "publication":
                # A completed semantic verdict stays valid. Only its GitHub
                # publication failed; never consume the reviewer retry budget
                # or postpone the already-required P0 repair.
                latest = self.state.get(repo, number)
                attempts = int(latest.get("publication_attempts", 0)) + 1
                self.state.put(repo, number, head, evidence_key=evidence_key,
                               outcome="reviewed", review_posted=False,
                               publication_attempts=attempts,
                               retry_after=time.time() + min(1800, 60 * (2 ** min(attempts - 1, 5))),
                               failure=str(error))
                self.log("%s#%s findings publication failed; preserving the completed review: %s" % (
                    repo, number, bounded(error, 500)))
                return False
            if phase == "semantic" and exhausted_review:
                self.state.put(repo, number, head, evidence_key=evidence_key,
                               outcome="retry", retry_after=time.time() + 3600,
                               retry_reason="semantic_review", failure=str(error))
                self.log("%s#%s semantic review is still unavailable; retaining its bounded retry" % (repo, number))
                return False
            if phase == "semantic_repair":
                latest = self.state.get(repo, number)
                repair_attempts = int(latest.get("repair_attempts", 0)) + 1
                if isinstance(error, pr_watch_common.RepairAttemptError) and int(latest.get("repair_rounds", 0)) >= REPAIR_LIMIT:
                    return self.exhaust_repair(repo, number, pr, evidence_key, error)
                if repair_attempts >= INFRASTRUCTURE_ATTEMPT_LIMIT:
                    self.state.put(repo, number, head, evidence_key=evidence_key, outcome="retry",
                                   repair_attempts=repair_attempts, retry_after=time.time() + 3600,
                                   retry_reason="semantic_repair", failure=str(error))
                    return False
                delay = 60 * repair_attempts
                self.state.put(repo, number, head, evidence_key=evidence_key, outcome="retry",
                               repair_attempts=repair_attempts, retry_after=time.time() + delay,
                               retry_reason="semantic_repair", failure=str(error))
                return False
            attempts = int(self.state.get(repo, number).get("review_attempts", 0)) + (0 if phase in ("semantic", "semantic_repair") else 1)
            exhausted_review = phase == "semantic" and attempts >= INFRASTRUCTURE_ATTEMPT_LIMIT
            outcome = ("retry" if retry or phase == "verification" else
                       "merge_failed" if phase == "merge" else "failed")
            delay = 3600 if exhausted_review else min(1800, 30 * (2 ** min(attempts - 1, 6)))
            self.state.put(repo, number, head, evidence_key=evidence_key, outcome=outcome,
                           review_attempts=attempts, retry_after=time.time() + delay,
                           failure=str(error))
            if not retry and phase not in ("verification", "merge") and not old.get("failure_posted") and phase != "semantic_repair":
                if not self.dry_run:
                    self.comment(repo, number, "Automated review at `%s` could not complete. Not merged.\n\n```text\n%s\n```" %
                                 (head[:9], bounded(error, 14000)))
                self.state.put(repo, number, head, evidence_key=evidence_key, failure_posted=True)
            self.log("%s#%s %s failed: %s" % (repo, number, phase, bounded(error, 700)))
        finally:
            self.remove_worktree(repo, worktree)

    def finish_merge(self, config, pr, current):
        repo, number, head = config["name"], pr["number"], pr["headRefOid"]
        try:
            merged = self.merge(config, pr)
            outcome = "merged" if merged else "passed"
            self.state.put(repo, number, head, evidence_key=current.get("evidence_key"),
                           outcome=outcome, merged=merged, failure=None)
            if not self.dry_run:
                self.notify(config, number, head, outcome)
        except WatchError as error:
            blocking = "not mergeable" in str(error) or "conflict" in str(error) or "issue" in str(error)
            retry = 0 if blocking else time.time() + min(900, 60 * (2 ** min(int(current.get("merge_attempts", 0)), 4)))
            self.state.put(repo, number, head, evidence_key=current.get("evidence_key"),
                           outcome="merge_failed", blocking=blocking,
                           merge_attempts=int(current.get("merge_attempts", 0)) + 1,
                           retry_after=retry, failure=str(error))
            self.log("%s#%s merge blocked: %s" % (repo, number, bounded(error, 700)))
            if isinstance(error, LocalGateError) and current.get("local_gate_notice") != str(error) and not self.dry_run:
                self.comment(repo, number, str(error))
                self.state.put(repo, number, head, local_gate_notice=str(error))

    def worker(self):
        while True:
            priority, _, key = self.tasks.get()
            if priority == 2:
                self.tasks.task_done()
                return
            repo, number = key
            try:
                self.process(repo, number)
            except Exception as error:
                # Programming errors are logged, never interpreted as a pass.
                self.log("%s#%s worker error: %s: %s" % (repo, number, type(error).__name__, bounded(error, 500)))
                if self.verbose:
                    traceback.print_exc()
            finally:
                requeue = False
                with self.queue_lock:
                    self.active.pop(key, None)
                    latest = self.pending.pop(key, None)
                    if latest is not None:
                        requeue = True
                self.tasks.task_done()
                if requeue and not self.stopping.is_set():
                    self.enqueue(repo, number, latest, "updated head")

    def heartbeat(self):
        while not self.stopping.wait(60):
            with self.queue_lock:
                detail = ", ".join("%s#%s %s %ss" % (repo, number, item["phase"], int(time.monotonic() - item["started"]))
                                   for (repo, number), item in sorted(self.active.items()))
                keys = [key for key, item in self.active.items() if item["phase"] != "queued"]
            for key in keys:
                self.touch_leases(key)
            self.log("workers: " + (detail or "idle"))

    def start_workers(self):
        count = self.config["workers"]
        if not 1 <= count <= 20:
            raise WatchError("workers must be between 1 and 20")
        self.workers = [threading.Thread(target=self.worker, daemon=True) for _ in range(count)]
        concurrency = self.config.get("reviewer_concurrency", 16)
        if not 1 <= concurrency <= 20:
            raise WatchError("reviewer concurrency must be between 1 and 20")
        self.review_executor = ThreadPoolExecutor(
            max_workers=concurrency, thread_name_prefix="semantic-reviewer")
        for worker in self.workers:
            worker.start()

    def start_events(self):
        from pr_watch_events import Events
        settings = self.config["webhook"]
        secret = os.environ.get("PR_WATCH_WEBHOOK_SECRET", "")
        self.events = Events(secret, int(settings.get("port", 18777)), set(self.repositories), self.enqueue, self.log)
        self.events.start()

    def run(self, once=False):
        self.single_instance()
        self.start_workers()
        threading.Thread(target=self.heartbeat, daemon=True).start()
        self.start_events()
        poll = self.config["poll_seconds"]
        if not 30 <= poll <= 3600:
            raise WatchError("poll_seconds must be between 30 and 3600")
        try:
            while not self.stopping.is_set():
                self.discover()
                if once:
                    self.tasks.join()
                    break
                self.wake.wait(poll)
                self.wake.clear()
                if self.fatal:
                    raise self.fatal
        finally:
            self.stopping.set()
            for _ in self.workers:
                self.tasks.put((2, -1, ("", 0)))
            for worker in self.workers:
                worker.join(timeout=30)
            if self.review_executor is not None:
                self.review_executor.shutdown(wait=True, cancel_futures=True)
            if self.events:
                self.events.close()
            if self.instance:
                self.instance.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default=os.environ.get("PR_WATCH_CONFIG", str(Path.home() / ".config" / "persona" / "pr-watch.json")))
    parser.add_argument("--once", action="store_true", help="one discovery round, then wait for its queued work")
    parser.add_argument("--dry-run", action="store_true", help="review and test locally, but never comment, notify or merge")
    parser.add_argument("--migrate-config", action="store_true",
                        help="with the service stopped, preserve evidence and migrate the reviewed configuration, then exit")
    parser.add_argument("--repository", action="append", help="restrict this run to a configured owner/name; repeatable")
    parser.add_argument("--pr", type=int, action="append", help="restrict this run to PR numbers; requires --repository")
    args = parser.parse_args(argv)
    config = load_config(args.config)
    if args.migrate_config:
        if args.repository or args.pr or args.dry_run or args.once:
            raise WatchError("--migrate-config is an exclusive offline operation")
        watcher = Watcher(config, allow_config_migration=True)
        watcher.single_instance()
        watcher.instance.close()
        return 0
    if args.repository:
        unknown = set(args.repository) - {r["name"] for r in config["repositories"]}
        if unknown:
            raise WatchError("repository is not configured: " + ", ".join(sorted(unknown)))
        config["repositories"] = [r for r in config["repositories"] if r["name"] in args.repository]
    if args.pr:
        if len(config["repositories"]) != 1:
            raise WatchError("--pr requires exactly one selected repository")
        selected = set(args.pr)
        watcher = Watcher(config, args.dry_run)
        watcher.discover = lambda: [watcher.enqueue(config["repositories"][0]["name"], number, "", "manual") for number in sorted(selected)]
    else:
        watcher = Watcher(config, args.dry_run)
    watcher.run(args.once)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except WatchError as error:
        sys.exit("pr-watch: " + str(error))
