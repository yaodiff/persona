"""Isolated tests for the watcher gates. No real GitHub, model or PR is used."""
import hashlib
import hmac
import importlib.util
import io
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from contextlib import contextmanager, nullcontext, redirect_stderr
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import pr_watch
import pr_watch_common
import pr_watch_events
import pr_watch_merge
import pr_watch_repair
import pr_watch_recovery
import pr_watch_review
import pr_watch_verify

SHA_A, SHA_B, SHA_BASE = "a" * 40, "b" * 40, "c" * 40


def pr(head=SHA_A, **overrides):
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    result = {"number": 7, "title": "fix: an example", "body": "Summary.\n\nFixes #6",
              "state": "OPEN", "isDraft": False, "headRefOid": head, "headRefName": "fix/example",
              "baseRefName": "main", "baseRefOid": SHA_BASE, "isCrossRepository": False,
              "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "reviewDecision": "", "url": "https://example.com/pr/7",
              "createdAt": now, "updatedAt": now}
    result.update(overrides)
    return result


class WatcherTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="pr-watch-test-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        (self.root / "checkout").mkdir()
        self.config = {"repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                                        "base": "main", "verify": {"profile": "commands", "commands": [["true"]]},
                                        "merge": True, "privacy": None, "notify_command": None}],
                       "reviewer": {}, "webhook": {}, "poll_seconds": 60, "workers": 1,
                       "state": str(self.root / "state.json")}
        self.watcher = pr_watch.Watcher(self.config, verbose=False)
        self.original_success_review = self.watcher.success_review
        @contextmanager
        def no_file_lock(root):
            yield
        self.lock_patch = patch.object(pr_watch.pr_watch_merge, "merge_lock", no_file_lock)
        self.lock_patch.start()
        self.addCleanup(self.lock_patch.stop)
        self.watcher.remove_worktree = lambda *args: None
        self.watcher.worktree = lambda *args: self.root / "checkout"
        self.comments = []
        self.watcher.comment = lambda repo, number, body: self.comments.append(body)
        self.watcher.success_review = lambda *args, **kwargs: self.original_success_review(*args, **kwargs)
        self.github = patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", ""))
        self.github = self.github.start()
        self.addCleanup(self.github.stop)
        # Stale tests provide their own activity observations. Do not replace
        # the whole activity method here: that would hide the reader under test.

    def test_stale_metadata_does_not_erase_completed_exact_head_evidence(self):
        old_base = "d" * 40
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + old_base + ".." + SHA_A,
                               base=old_base, format=1, outcome="passed_with_findings",
                               semantic="passed", verification="passed", findings=[finding],
                               repair_rounds=2, review_posted=True, success_posted=True)
        before = self.watcher.state.get("example/one", 7)
        self.watcher.state.annotate("example/one", 7, stale_marked_at=123, outcome="stale_close_pending")
        self.watcher.state.annotate("example/one", 7, outcome="stale_closed")
        after = self.watcher.state.get("example/one", 7)
        self.assertEqual(after["evidence_key"], before["evidence_key"])
        self.assertEqual(after["verification"], "passed")
        self.assertEqual(after["findings"], [finding])
        self.assertEqual(after["repair_rounds"], 2)

    def test_snapshot_resolves_live_base_in_one_graphql_request(self):
        # GitHub's baseRefOid is the base recorded at the PR's last sync, not
        # the branch tip that the watcher fetches (#10568).
        fields = dict(pr(), baseRefOid=SHA_A)
        fields["baseRefName"] = "develop"
        response = {"data": {"repository": {"pullRequest": dict(fields, baseRef={"target": {"oid": SHA_BASE}})}}}
        with patch.object(pr_watch_common, "run", return_value=subprocess.CompletedProcess(
                [], 0, json.dumps(response), "")) as execute:
            result = pr_watch_common.pr_snapshot("example/one", 7)
        self.assertEqual(result["baseRefOid"], SHA_BASE)
        execute.assert_called_once()
        argv = execute.call_args.args[0]
        self.assertEqual(argv[:2], ["gh", "api"])
        self.assertEqual(argv[2:], ["graphql", "--input", "-"])
        payload = json.loads(execute.call_args.kwargs["input_text"])
        query = payload["query"]
        self.assertIn("baseRef { target { oid } }", query)
        self.assertNotIn("baseRefOid", query)
        self.assertEqual(payload["variables"],
                         {"owner": "example", "name": "one", "number": 7})
        self.assertEqual(result["baseRefName"], "develop")

    def test_snapshot_rejects_missing_or_invalid_live_base(self):
        fields = dict(pr(), baseRefOid=None)
        for base in (None, "", "0" * 40, "not-a-sha"):
            with self.subTest(base=base):
                response = {"data": {"repository": {"pullRequest": dict(fields, baseRef={"target": {"oid": base}})}}}
                with patch.object(pr_watch_common, "run", return_value=subprocess.CompletedProcess(
                        [], 0, json.dumps(response), "")):
                    with self.assertRaises(pr_watch_common.WatchError):
                        pr_watch_common.pr_snapshot("example/one", 7)

    def test_snapshot_rejects_gh_errors(self):
        with patch.object(pr_watch_common, "run", return_value=subprocess.CompletedProcess([], 1, "", "network unavailable")):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_common.pr_snapshot("example/one", 7)

    def test_existing_continuation_contains_the_fork_head(self):
        # Separate watchers keep private state; GitHub is the shared record (#10881).
        title = "fix: continue #7 verification repair"
        listed = [{"number": 30, "title": title, "state": "CLOSED", "headRefOid": SHA_B},
                  {"number": 31, "title": "fix: continue #77 verification repair", "state": "OPEN", "headRefOid": SHA_B},
                  {"number": 32, "title": title, "state": "MERGED", "headRefOid": SHA_B}]
        for status, expected in (("ahead", 32), ("identical", 32), ("behind", None), ("diverged", None)):
            with self.subTest(status=status), \
                    patch.object(pr_watch_repair, "gh_json", return_value=listed) as search, \
                    patch.object(pr_watch_repair, "checked", return_value=status + "\n") as compare:
                self.assertEqual(pr_watch_repair.existing_continuation("example/one", pr(isCrossRepository=True)), expected)
            self.assertIn('"%s" in:title' % title, search.call_args.args[0])
            self.assertEqual(compare.call_count, 1)
            self.assertEqual(compare.call_args.args[0][:3], ["gh", "api", "repos/example/one/compare/%s...%s" % (SHA_A, SHA_B)])

    def test_existing_continuation_prefers_open_and_matches_an_identical_head(self):
        title = "fix: continue #7 verification repair"
        listed = [{"number": 40, "title": title, "state": "MERGED", "headRefOid": SHA_B},
                  {"number": 41, "title": title, "state": "OPEN", "headRefOid": SHA_A}]
        with patch.object(pr_watch_repair, "gh_json", return_value=listed), \
                patch.object(pr_watch_repair, "checked") as compare:
            self.assertEqual(pr_watch_repair.existing_continuation("example/one", pr(isCrossRepository=True)), 41)
        compare.assert_not_called()
        with patch.object(pr_watch_repair, "gh_json", return_value=[]):
            self.assertIsNone(pr_watch_repair.existing_continuation("example/one", pr(isCrossRepository=True)))

    def test_fork_with_another_watchers_continuation_is_left_to_it(self):
        with patch.object(self.watcher, "mark_and_close_stale", return_value=False), \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(isCrossRepository=True)), \
                patch.object(pr_watch_repair, "existing_continuation", return_value=42) as lookup, \
                patch.object(self.watcher, "worktree", side_effect=AssertionError("no review or repair")), \
                patch.object(self.watcher, "comment", side_effect=AssertionError("no comment")):
            self.watcher.process("example/one", 7)
        lookup.assert_called_once()
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual((state["outcome"], state["continuation"], state.get("repair_rounds")), ("continuation", 42, 0))
        with patch.object(self.watcher, "mark_and_close_stale", return_value=False), \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(isCrossRepository=True)), \
                patch.object(pr_watch_repair, "existing_continuation") as again:
            self.watcher.process("example/one", 7)
        again.assert_not_called()

    def test_fork_continuation_lookup_failure_does_not_review_or_repair(self):
        with patch.object(self.watcher, "mark_and_close_stale", return_value=False), \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(isCrossRepository=True)), \
                patch.object(pr_watch_repair, "existing_continuation", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(self.watcher, "worktree", side_effect=AssertionError("no review or repair")):
            with self.assertRaises(pr_watch_common.WatchError):
                self.watcher.process("example/one", 7)
        self.assertNotEqual(self.watcher.state.get("example/one", 7).get("outcome"), "continuation")

    def test_same_repository_pr_skips_the_continuation_lookup(self):
        with patch.object(self.watcher, "mark_and_close_stale", return_value=False), \
                patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch_repair, "existing_continuation") as lookup, \
                patch.object(self.watcher, "worktree", side_effect=pr_watch_common.WatchError("stop here")):
            try:
                self.watcher.process("example/one", 7)
            except pr_watch_common.WatchError:
                pass
        lookup.assert_not_called()

    def test_stale_failure_does_not_overwrite_terminal_outcome(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, outcome="repair_exhausted", repair_exhausted=True,
                               repair_rounds=3, failure="original repair failure")
        with patch.object(self.watcher, "mark_and_close_stale", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(pr_watch, "pr_snapshot", return_value=pr()):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "repair_exhausted")
        self.assertEqual(state["failure"], "original repair failure")
        self.assertEqual(state["stale_close_failure"], "offline")

    def test_stale_notice_does_not_close_a_newly_pushed_head(self):
        old = time.time() - 8 * 86400
        config = dict(self.watcher.config["repositories"][0], close_stale=True)
        with patch.object(self.watcher, "stale_progress_at", return_value=old), \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(SHA_B)), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            self.assertFalse(self.watcher.mark_and_close_stale(config, pr(), {}))
        self.assertEqual(execute.call_count, 1)
        self.assertNotIn("stale_closed_at", self.watcher.state.get("example/one", 7))

    def test_same_head_replacement_push_refreshes_stale_activity(self):
        snapshot = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-16T00:00:00Z")
        with patch.object(pr_watch.Watcher, "stale_activity", return_value={"comments": [], "reviews": [], "inline": []}):
            progress = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot,
                                                         {"stale_head": SHA_A, "stale_updated_at": pr_watch_common.github_time("2026-11-15T00:00:00Z")})
        self.assertEqual(progress, pr_watch_common.github_time("2026-11-01T00:00:00Z"))

    def test_migrated_failed_record_requires_verification_repair(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.data["prs"]["example/one#7"] = {
            "head": SHA_A, "base": SHA_BASE, "evidence_key": evidence_key, "format": 1,
            "outcome": "failed", "semantic": "passed", "verification": "passed",
            "failure": "PR-owned verification failure", "review_policy": 1,
        }
        self.watcher.migrate_policy("new")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(self.watcher, "merge", return_value=False) as merge, \
                patch.object(self.watcher, "repair_verification", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()
        merge.assert_not_called()
        review.assert_not_called()

    def test_review_only_published_p2_still_verifies(self):
        self.watcher.repositories["example/one"]["merge"] = False
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="passed_with_findings", semantic="passed",
                               findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        verify.assert_called_once()
        review.assert_not_called()

    def test_legacy_published_findings_are_not_republished_on_new_head(self):
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="findings", semantic="findings",
                               findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(SHA_B)), \
                patch.object(self.watcher, "comment_findings") as publish, \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        publish.assert_not_called()

    def test_first_stale_close_uses_post_mark_state(self):
        old = time.time() - 8 * 86400
        config = dict(self.watcher.config["repositories"][0], close_stale=True)
        current = pr()
        with patch.object(self.watcher, "stale_progress_at", return_value=old), \
                patch.object(pr_watch, "pr_snapshot", side_effect=[current, dict(current, state="CLOSED")]), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            self.assertTrue(self.watcher.mark_and_close_stale(config, current, {}))
        self.assertTrue(any(call.args[0][1:3] == ["pr", "close"] for call in execute.call_args_list))

    def test_stale_pr_is_marked_explained_then_closed_without_merging(self):
        self.watcher.config["repositories"][0]["close_stale"] = True
        self.watcher.repositories["example/one"]["close_stale"] = True
        calls = []
        def github(argv, **kwargs):
            calls.append(argv)
            if argv[1:3] == ["pr", "edit"]:
                return subprocess.CompletedProcess(argv, 0, "", "")
            if argv[1:3] == ["pr", "close"]:
                self.github_pr["state"] = "CLOSED"
                return subprocess.CompletedProcess(argv, 0, "", "")
            self.fail("unexpected GitHub mutation: %r" % argv)
        self.github_pr = pr()
        self.github_pr["createdAt"] = "2026-11-01T00:00:00Z"
        old = int(time.mktime(time.strptime("2026-11-01T00:00:00Z", "%Y-%m-%dT%H:%M:%SZ")))
        with patch.object(pr_watch, "pr_snapshot", side_effect=lambda *args: dict(self.github_pr)), \
                patch.object(pr_watch.time, "time", return_value=old + 8 * 86400), \
                patch.object(self.watcher, "stale_progress_at", return_value=float(old)), \
                patch.object(pr_watch, "run", side_effect=github), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        self.assertEqual(self.github_pr["state"], "CLOSED")
        self.assertEqual([call[1:3] for call in calls], [["pr", "edit"], ["pr", "close"]])
        self.assertIn("more than seven days", self.comments[0])
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "stale_closed")
        review.assert_not_called()
        merge.assert_not_called()

    def test_stale_multiple_activity_records_use_real_queries_and_ignore_only_own_notice(self):
        config = dict(self.watcher.config["repositories"][0], close_stale=True)
        snapshot = pr(createdAt="2026-11-01T00:00:00.123Z", updatedAt="2026-11-01T00:00:00.123Z")
        old = pr_watch_common.github_time("2026-11-01T00:00:00.123Z")
        now = old + 8 * 86400
        current = dict(snapshot)
        calls = []
        def activity(repo, number, snapshot, old, **kwargs):
            calls.append((repo, number))
            return pr_watch_common.github_time(snapshot["createdAt"]) + (0 if kwargs.get("ignore_notice") else 1)
        def github(argv, **kwargs):
            if argv[1:3] == ["pr", "edit"]:
                return subprocess.CompletedProcess(argv, 0, "", "")
            if argv[1:3] == ["pr", "close"]:
                current["state"] = "CLOSED"
                return subprocess.CompletedProcess(argv, 0, "", "")
            self.fail(argv)
        with patch.object(self.watcher, "stale_progress_at", side_effect=activity), \
                patch.object(pr_watch, "pr_snapshot", side_effect=lambda *args: dict(current)), \
                patch.object(pr_watch, "run", side_effect=github), \
                patch.object(pr_watch.time, "time", return_value=now):
            self.assertTrue(self.watcher.mark_and_close_stale(config, snapshot, {}))
        self.assertEqual(current["state"], "CLOSED")
        self.assertEqual(len(calls), 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "stale_closed")

    def test_stale_activity_graphql_paginates_every_collection(self):
        def connection(nodes, next_cursor):
            return {"nodes": nodes, "pageInfo": {"hasNextPage": next_cursor is not None,
                                                   "endCursor": next_cursor}}
        pages = {
            "comments": {
                None: connection([{"createdAt": "2026-11-01T01:00:00Z", "body": "one"}], "comments-1"),
                "comments-1": connection([{"createdAt": "2026-11-02T01:00:00Z", "body": "two"}], None),
            },
            "reviews": {
                None: connection([{"createdAt": "2026-11-01T02:00:00Z"}], "reviews-1"),
                "reviews-1": connection([{"createdAt": "2026-11-03T02:00:00Z"}], "reviews-2"),
                "reviews-2": connection([{"createdAt": "2026-11-05T02:00:00Z"}], None),
            },
            "reviewThreads": {
                None: connection([], "threads-1"),
                "threads-1": connection([{"comments": connection(
                    [{"createdAt": "2026-11-04T03:00:00Z", "body": "inline"}], None)}], "threads-2"),
                "threads-2": connection([{"comments": connection(
                    [{"createdAt": "2026-11-06T03:00:00Z", "body": "later inline"}], None)}], None),
            },
        }
        calls = []
        def response(argv, **kwargs):
            self.assertEqual(argv, ["api", "graphql", "--input", "-"])
            payload = json.loads(kwargs["input_text"])
            variables = payload["variables"]
            calls.append(payload)
            data = {}
            for field, needed, collection in (("comments", "needComments", "comments"),
                                              ("reviews", "needReviews", "reviews"),
                                              ("threads", "needThreads", "reviewThreads")):
                if variables[needed]:
                    data[collection] = pages[collection][variables[field + "Cursor"]]
            return {"data": {"repository": {"pullRequest": data}}}
        with patch.object(pr_watch, "gh_json", side_effect=response):
            activity = self.watcher.stale_activity("example/one", 7)
        self.assertEqual([len(activity[key]) for key in ("comments", "reviews", "inline")], [2, 3, 2])
        self.assertEqual(len(calls), 3)
        self.assertTrue(all("comments(first: $pageSize" in call["query"] for call in calls))
        self.assertIsNone(calls[0]["variables"]["commentsCursor"])
        self.assertEqual(calls[1]["variables"]["commentsCursor"], "comments-1")
        self.assertEqual(calls[-1]["variables"]["threadsCursor"], "threads-2")
        self.assertTrue(any(not call["variables"]["needComments"] for call in calls[1:]))
        self.assertEqual(calls[-1]["variables"]["reviewsCursor"], "reviews-2")

    def test_stale_activity_does_not_restart_completed_collections(self):
        calls = []
        def response(argv, **kwargs):
            payload = json.loads(kwargs["input_text"])
            calls.append(payload)
            data = {}
            if payload["variables"]["needComments"]:
                done = payload["variables"]["commentsCursor"] == "comments-1"
                data["comments"] = {"nodes": [] if done else [{"createdAt": "2026-11-01T01:00:00Z", "body": "one"}],
                                    "pageInfo": {"hasNextPage": False,
                                                 "endCursor": None if done else "comments-1"}}
            if payload["variables"]["needReviews"]:
                done = payload["variables"]["reviewsCursor"] == "reviews-1"
                data["reviews"] = {"nodes": [] if done else [{"createdAt": "2026-11-01T02:00:00Z"}],
                                   "pageInfo": {"hasNextPage": False,
                                                "endCursor": None if done else "reviews-1"}}
            if payload["variables"]["needThreads"]:
                done = payload["variables"]["threadsCursor"] == "threads-1"
                data["reviewThreads"] = {"nodes": [] if done else [{"comments": {
                    "nodes": [{"createdAt": "2026-11-01T03:00:00Z", "body": "inline"}],
                    "pageInfo": {"hasNextPage": False, "endCursor": None}}}],
                    "pageInfo": {"hasNextPage": False if done else True,
                                 "endCursor": None if done else "threads-1"}}
            return {"data": {"repository": {"pullRequest": data}}}
        with patch.object(pr_watch, "gh_json", side_effect=response):
            activity = self.watcher.stale_activity("example/one", 7)
        self.assertEqual([len(activity[key]) for key in ("comments", "reviews", "inline")], [1, 1, 1])
        self.assertEqual(calls[-1]["variables"]["needComments"], False)
        self.assertEqual(calls[1]["variables"]["needReviews"], False)
        self.assertEqual(calls[-1]["variables"]["threadsCursor"], "threads-1")
        self.assertEqual(len(calls), 2)
        self.assertTrue(all("needComments" in call["variables"] for call in calls))
        self.assertTrue(all("needReviews" in call["variables"] for call in calls))
        self.assertTrue(all("needThreads" in call["variables"] for call in calls))
        self.assertEqual([call["variables"]["commentsCursor"] for call in calls],
                         [None, None])
        # A finished collection stays out of the query through @include, but its cursor variable is still
        # declared String, so it must stay null rather than `false` (#16737).
        for call in calls:
            for field in ("comments", "reviews", "threads"):
                value = call["variables"][field + "Cursor"]
                self.assertIsInstance(value, (str, type(None)),
                                      "%sCursor must be a string or null" % field)

    def test_stale_activity_requires_complete_cursor_pages(self):
        response = {"data": {"repository": {"pullRequest": {
            "comments": {"nodes": [], "pageInfo": {"hasNextPage": True, "endCursor": None}},
            "reviews": {"nodes": [], "pageInfo": {"hasNextPage": False, "endCursor": None}},
            "reviewThreads": {"nodes": [], "pageInfo": {"hasNextPage": False, "endCursor": None}},
        }}}}
        with patch.object(pr_watch, "gh_json", return_value=response):
            with self.assertRaisesRegex(pr_watch_common.WatchError, "pagination"):
                self.watcher.stale_activity("example/one", 7)

    def test_stale_enabled_settled_heads_still_need_cleanup(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.repositories["example/one"]["close_stale"] = True
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="repair_exhausted",
                               repair_exhausted=True, failure_posted=True)
        self.assertFalse(self.watcher.unchanged_head_complete_or_waiting("example/one", 7, SHA_A))
        self.watcher.state.annotate("example/one", 7, outcome="merged")
        self.assertTrue(self.watcher.unchanged_head_complete_or_waiting("example/one", 7, SHA_A))

    def test_review_only_blocking_findings_can_still_stale_close(self):
        self.watcher.repositories["example/one"].update(merge=False, close_stale=True)
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 1, "severity": "P0", "summary": "blocking", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="findings", semantic="findings",
                               findings=[finding], review_posted=True)
        old = pr_watch_common.github_time("2026-11-01T00:00:00Z")
        snapshot = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-01T00:00:00Z")
        current = dict(snapshot)
        def github(argv, **kwargs):
            if argv[1:3] == ["pr", "edit"]:
                return subprocess.CompletedProcess(argv, 0, "", "")
            if argv[1:3] == ["pr", "close"]:
                current["state"] = "CLOSED"
                return subprocess.CompletedProcess(argv, 0, "", "")
            self.fail(argv)
        with patch.object(self.watcher, "stale_progress_at", return_value=old) as activity, \
                patch.object(pr_watch, "pr_snapshot", side_effect=lambda *args: dict(current)), \
                patch.object(pr_watch, "run", side_effect=github), \
                patch.object(pr_watch.time, "time", return_value=old + 8 * 86400):
            self.watcher.process("example/one", 7)
        self.assertEqual(activity.call_count, 2)
        self.assertEqual(current["state"], "CLOSED")
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "stale_closed")

    def test_discovery_skips_settled_heads_but_retries_expired_and_new_heads(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        for outcome in ("merged", "continuation", "repair_published"):
            self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                                   base=SHA_BASE, format=1, outcome=outcome)
            self.assertTrue(self.watcher.unchanged_head_complete_or_waiting("example/one", 7, SHA_A))
            self.assertFalse(self.watcher.unchanged_head_complete_or_waiting("example/one", 7, SHA_B))
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", retry_after=time.time() - 1)
        self.assertFalse(self.watcher.unchanged_head_complete_or_waiting("example/one", 7, SHA_A))
        self.watcher.state.annotate("example/one", 7, retry_after=time.time() + 60)
        self.assertTrue(self.watcher.unchanged_head_complete_or_waiting("example/one", 7, SHA_A))
        with patch.object(pr_watch, "gh_json", return_value=[pr(isDraft=False)]) as listing, \
                patch.object(self.watcher, "enqueue") as enqueue:
            self.watcher.discover()
        listing.assert_called_once()
        enqueue.assert_not_called()
        with patch.object(pr_watch, "gh_json", return_value=[pr(SHA_B, isDraft=False)]), \
                patch.object(self.watcher, "enqueue") as enqueue:
            self.watcher.discover()
        enqueue.assert_called_once_with("example/one", 7, SHA_B, "poll")

    def test_discovery_polls_open_prs_older_than_the_newest_two_hundred(self):
        # gh lists the newest PRs first and stops at --limit (#14864).
        listing = [{"number": 300 - index, "headRefOid": SHA_A, "isDraft": False,
                    "baseRefName": "main", "mergeable": "MERGEABLE"} for index in range(250)]
        def gh(argv, **kwargs):
            return listing[:int(argv[argv.index("--limit") + 1])]
        logs = []
        with patch.object(pr_watch, "gh_json", side_effect=gh), \
                patch.object(self.watcher, "enqueue") as enqueue, \
                patch.object(self.watcher, "log", side_effect=logs.append):
            self.watcher.discover()
        self.assertEqual(sorted(call.args[1] for call in enqueue.call_args_list), list(range(51, 301)))
        self.assertEqual(logs, [])
        with patch.object(pr_watch, "DISCOVERY_LIMIT", 250), \
                patch.object(pr_watch, "gh_json", side_effect=gh), \
                patch.object(self.watcher, "enqueue"), \
                patch.object(self.watcher, "log", side_effect=logs.append):
            self.watcher.discover()
        self.assertEqual(len(logs), 1)
        self.assertIn("example/one: discovery reached its bound of 250 open PRs", logs[0])

    def test_same_head_backoff_and_terminal_records_skip_stale_activity(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        cases = [
            {"outcome": "merge_failed", "retry_after": time.time() + 300},
            {"outcome": "merge_failed", "blocking": True},
            {"outcome": "merged"},
            {"outcome": "repair_exhausted", "repair_exhausted": True},
            {"outcome": "repair_published"},
        ]
        for record in cases:
            with self.subTest(outcome=record["outcome"]):
                self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                                       base=SHA_BASE, **record)
                with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                        patch.object(self.watcher, "stale_progress_at") as activity, \
                        patch.object(pr_watch, "semantic_review") as review:
                    self.watcher.process("example/one", 7)
                activity.assert_not_called()
                review.assert_not_called()

    def test_stale_pr_with_multiple_activity_records_closes_despite_its_own_notice_extra(self):

        config = dict(self.watcher.config["repositories"][0], close_stale=True)
        snapshot = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-01T00:00:00Z")
        old = pr_watch_common.github_time("2026-11-01T00:00:00Z")
        notice = "This PR has had no substantive progress for more than seven days"
        comments = [{"createdAt": "2026-11-08T00:00:00Z", "body": notice},
                    {"createdAt": "2026-11-08T00:00:01Z", "body": "I am still working on this"}]
        with patch.object(self.watcher, "stale_activity", side_effect=[
                {"comments": comments, "reviews": [], "inline": []},
                {"comments": comments, "reviews": [], "inline": []}]):
            activity = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot,
                                                         {"stale_head": SHA_A}, ignore_notice=notice)
            self.assertEqual(activity, pr_watch_common.github_time("2026-11-08T00:00:01Z"))
            activity = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot,
                                                         {"stale_head": SHA_A}, ignore_notice=notice)
            self.assertEqual(activity, pr_watch_common.github_time("2026-11-08T00:00:01Z"))
        with patch.object(self.watcher, "stale_activity", return_value={"comments": comments[:1], "reviews": [], "inline": []}):
            activity = self.watcher.stale_progress_at("example/one", 7, snapshot,
                                                     {"stale_head": SHA_A}, ignore_notice=notice)
            self.assertEqual(activity, pr_watch_common.github_time("2026-11-01T00:00:00Z"))

    def test_recent_pr_activity_prevents_stale_close(self):
        self.github_pr = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-14T00:00:00Z")
        self.watcher.repositories["example/one"]["close_stale"] = True
        recent = pr_watch_common.github_time("2026-11-14T00:00:00Z")
        snapshot = dict(self.github_pr)
        with patch.object(pr_watch, "pr_snapshot", return_value=snapshot), \
                patch.object(pr_watch.time, "time", return_value=pr_watch_common.github_time("2026-11-15T00:00:00Z")), \
                patch.object(self.watcher, "stale_progress_at", return_value=recent) as activity, \
                patch.object(pr_watch, "run") as mutations, \
                patch.object(self.watcher, "success_review", return_value=None), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(pr_watch, "gh_json", side_effect=[[], [], []]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        self.assertFalse(any("seven days" in body for body in self.comments))
        self.assertNotIn("stale_closed_at", self.watcher.state.get("example/one", 7))
        activity.assert_called_once()
        self.assertFalse(any(call.args[0][1:3] in (["pr", "close"], ["pr", "edit"])
                             for call in mutations.call_args_list))

    def test_stale_uses_push_activity_not_commit_creation_time(self):
        old = time.time() - 8 * 86400
        snapshot = pr(createdAt=datetime.fromtimestamp(old - 86400, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                      updatedAt=datetime.fromtimestamp(time.time(), timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
        with patch.object(pr_watch.Watcher, "stale_activity", return_value={"comments": [], "reviews": [], "inline": []}):
            progress = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot, {})
        self.assertGreater(time.time() - progress, 0)
        self.assertLess(time.time() - progress, 60)

    def test_stale_marker_does_not_change_activity_without_new_work(self):
        snapshot = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-16T00:00:00Z")
        with patch.object(pr_watch.Watcher, "stale_activity", return_value={"comments": [], "reviews": [], "inline": []}):
            progress = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot,
                                                         {"stale_head": SHA_A, "last_progress_at": pr_watch_common.github_time("2026-11-08T00:00:00Z")})
        self.assertEqual(progress, pr_watch_common.github_time("2026-11-08T00:00:00Z"))

    def test_stale_review_comments_count_as_progress(self):
        snapshot = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-16T00:00:00Z")
        inline = [{"createdAt": "2026-11-15T00:00:00Z", "body": "**P1** — newly published failure"}]
        with patch.object(pr_watch.Watcher, "stale_activity", return_value={"comments": [], "reviews": [], "inline": inline}):
            progress = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot,
                                                         {"stale_head": SHA_A})
        self.assertEqual(progress, pr_watch_common.github_time("2026-11-15T00:00:00Z"))

    def test_stale_marker_does_not_count_as_new_progress(self):
        snapshot = pr(createdAt="2026-11-01T00:00:00Z", updatedAt="2026-11-08T00:00:00Z")
        notice = "watcher's own stale notice"
        old_state = {"stale_head": snapshot["headRefOid"], "stale_notice": notice,
                     "last_progress_at": pr_watch_common.github_time("2026-11-08T00:00:00Z")}
        comments = [{"createdAt": "2026-11-08T00:00:00Z", "body": notice}]
        with patch.object(pr_watch.Watcher, "stale_activity", return_value={"comments": comments, "reviews": [], "inline": []}):
            progress = pr_watch.Watcher.stale_progress_at(self.watcher, "example/one", 7, snapshot, old_state)
        self.assertEqual(progress, pr_watch_common.github_time("2026-11-08T00:00:00Z"))

    def test_reopened_stale_pr_requires_fresh_semantic_review(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="stale_closed", semantic="passed",
                               verification="passed", success_posted=True, findings=[], repair_rounds=2)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()
        verify.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 2)

    def test_review_only_p2_findings_continue_to_verification(self):
        self.watcher.repositories["example/one"]["merge"] = False
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="reviewed", semantic="passed",
                               findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        verify.assert_called_once()
        review.assert_not_called()

    def test_empty_api_response_is_rejected_except_empty_activity_stream(self):
        with patch.object(pr_watch_common, "checked", return_value=""):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_common.gh_json(["pr", "view", "7"])
        empty = {"data": {"repository": {"pullRequest": {
            key: {"nodes": [], "pageInfo": {"hasNextPage": False, "endCursor": None}}
            for key in ("comments", "reviews", "reviewThreads")}}}}
        with patch.object(pr_watch, "gh_json", return_value=empty):
            self.assertEqual(self.watcher.stale_activity("example/one", 7),
                             {"comments": [], "reviews": [], "inline": []})

    def test_paginated_json_records_and_page_arrays_are_flattened(self):
        text = '{"createdAt": "2026-11-01T00:00:00Z"}\n{"createdAt": "2026-11-02T00:00:00Z"}\n'
        with patch.object(pr_watch_common, "checked", return_value=text):
            self.assertEqual(len(pr_watch_common.gh_json(["api", "activity"])), 2)
        with patch.object(pr_watch_common, "checked", return_value='[{"id": 1}]\n[{"id": 2}]\n'):
            self.assertEqual(pr_watch_common.gh_json(["api", "activity"]), [{"id": 1}, {"id": 2}])
        with patch.object(pr_watch_common, "checked", return_value='[{"id": 1}]\n{"id": 2}'):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_common.gh_json(["api", "activity"])

    def test_failed_stale_comment_never_closes(self):
        old = time.time() - 8 * 86400
        config = dict(self.watcher.config["repositories"][0], close_stale=True)
        with patch.object(self.watcher, "stale_progress_at", return_value=old), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute, \
                patch.object(self.watcher, "comment", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(pr_watch, "pr_snapshot") as snapshot:
            with self.assertRaises(pr_watch_common.WatchError):
                self.watcher.mark_and_close_stale(config, pr(), {})
        self.assertEqual(execute.call_count, 1)
        snapshot.assert_not_called()

    def test_new_activity_after_stale_notice_keeps_pr_open(self):
        old = time.time() - 8 * 86400
        config = dict(self.watcher.config["repositories"][0], close_stale=True)
        with patch.object(self.watcher, "stale_progress_at", side_effect=[old, old + 30]), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute, \
                patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "comment"):
            self.assertFalse(self.watcher.mark_and_close_stale(config, pr(), {}))
        self.assertEqual(execute.call_count, 1)
        self.assertNotIn("stale_closed_at", self.watcher.state.get("example/one", 7))

    def test_stale_progress_uses_substantive_activity_and_rejects_bad_timestamps(self):
        snapshot = pr(createdAt="2026-11-13T00:00:00Z", updatedAt="2026-11-13T00:00:00Z")
        activity = pr_watch_common.progress_timestamp(snapshot,
            [{"createdAt": "2026-11-14T00:00:00Z"}], [],
            [{"committedDate": "2026-11-15T00:00:00Z"}])
        self.assertGreater(activity, pr_watch_common.github_time(snapshot["createdAt"]))
        self.assertEqual(pr_watch_common.github_time("2026-11-15T12:34:56.123456Z"),
                         pr_watch_common.github_time("2026-11-15T12:34:56Z") + 0.123456)
        for bad in (None, "2026-11-15", "2026-11-15T00:00:00+08:00", "2026-11-15T12:34:56.Z"):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_common.github_time(bad)
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_common.progress_timestamp(snapshot, None, [], [])

    def test_stale_close_honors_dry_run_review_only_and_continuations(self):
        old = time.time() - 8 * 86400
        with patch.object(self.watcher, "stale_progress_at", return_value=old) as activity, \
                patch.object(pr_watch, "run") as execute:
            config = dict(self.watcher.config["repositories"][0], close_stale=True)
            self.watcher.dry_run = True
            self.assertFalse(self.watcher.mark_and_close_stale(config, pr(), {}))
            self.watcher.dry_run = False
            config = dict(self.watcher.config["repositories"][0], close_stale=False)
            self.assertFalse(self.watcher.mark_and_close_stale(config, pr(), {}))
            self.assertFalse(self.watcher.mark_and_close_stale(self.watcher.config["repositories"][0], pr(), {"outcome": "continuation"}))
            self.assertFalse(self.watcher.mark_and_close_stale(self.watcher.config["repositories"][0], pr(isDraft=True), {}))
        activity.assert_not_called()
        execute.assert_not_called()

    def test_clean_head_runs_both_gates_then_merges(self):
        calls = []
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "command": ["true"], "ok": True, "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(merge.call_count, 1)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_p1_findings_are_notes_and_do_not_block_verification_or_merge(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P1", "summary": "wrong", "suggestion": "fix it"}]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "command": ["true"], "ok": True, "summary": ""}]) as verify, \
                patch.object(self.watcher, "repair_findings") as repair, \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as api, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        verify.assert_called_once()
        repair.assert_not_called()
        merge.assert_called_once()
        self.assertIn("P1/P2 findings are non-blocking notes", api.call_args_list[0].kwargs["input_text"])
        self.assertEqual(self.watcher.state.get("example/one", 7)["findings"], findings)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_p0_findings_are_published_then_repaired_instead_of_merging(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix it"}]
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr()] + [pr(head=SHA_B)] * 4), \
                patch.object(pr_watch, "semantic_review", return_value=findings) as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge") as merge, \
                patch.object(pr_watch_repair, "repair_with_model") as worker, \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "commit_and_push_repair", return_value=(7, [])), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as api:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        verify.assert_not_called()
        merge.assert_not_called()
        self.assertEqual(worker.call_count, 2)
        self.assertEqual(worker.call_args_list[0].args[1]["headRefOid"], SHA_A)
        self.assertEqual(worker.call_args_list[1].args[1]["headRefOid"], SHA_B)
        self.assertGreaterEqual(review.call_count, 2)
        self.assertEqual(review.call_args_list[0].args[1]["headRefOid"], SHA_A)
        self.assertEqual(review.call_args_list[1].args[1]["headRefOid"], SHA_B)
        self.assertEqual(api.call_count, 2)
        self.assertTrue(all(call.args[0][:2] == ["gh", "api"] for call in api.call_args_list))
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
        self.assertEqual(len(self.comments), 1)
        self.assertIn("pushed an automated repair", self.comments[0])

    def test_p2_findings_are_published_and_do_not_block_verification_or_merge(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P2", "summary": "possible edge case", "suggestion": "handle it"}]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "command": ["true"], "ok": True, "summary": ""}]) as verify, \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as api, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        verify.assert_called_once()
        merge.assert_called_once()
        self.assertEqual(api.call_count, 2)
        self.assertIn("P1/P2 findings are non-blocking", api.call_args_list[0].kwargs["input_text"])
        self.assertIn("Automated independent semantic review", api.call_args_list[1].args[0][-1])
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_p2_after_a_verification_failure_resumes_and_merges_without_duplicate_comments(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="passed_with_findings",
                               semantic="passed", findings=[finding], review_posted=True,
                               success_posted=False, verification="passed",
                               checks=[{"name": "test", "command": ["true"], "ok": True}])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(self.github.call_count, 1)
        self.assertIn("Automated independent semantic review", self.github.call_args.args[0][-1])

    def test_p2_without_verification_never_merges_before_verification(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="passed_with_findings",
                               semantic="passed", findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True}]) as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_called_once()

    def test_crash_after_success_publication_resumes_exact_head_merge(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="passed_with_findings",
                               semantic="passed", findings=[finding], review_posted=True,
                               success_posted=True, verification="passed", merge_attempts=0,
                               checks=[{"name": "test", "command": ["true"], "ok": True}])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(self.github.call_count, 0)

    def test_p2_findings_publish_only_once_before_merge_finishes(self):
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[finding]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True}]), \
                patch.object(self.watcher, "merge", side_effect=[pr_watch_common.WatchError("busy"), True]) as merge:
            self.watcher.process("example/one", 7)
            self.watcher.state.put("example/one", 7, SHA_A,
                                   evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                                   outcome="passed_with_findings", retry_after=0)
            self.watcher.process("example/one", 7)
        self.assertEqual(merge.call_count, 2)
        finding_calls = [call for call in self.github.call_args_list
                         if call.args[0][:2] == ["gh", "api"]]
        self.assertEqual(len(finding_calls), 1)

    def test_moved_base_does_not_republish_reviews_on_an_unchanged_head(self):
        # A merge-blocked PR is re-reviewed whenever main moves; the same head
        # must not collect a new copy of the same reviews each time (#11738).
        reworded = [{"path": "x.py", "line": 2, "severity": "P2", "summary": "note %d" % n, "suggestion": "consider"}
                    for n in range(3)]
        other_base = "d" * 40
        with patch.object(pr_watch, "semantic_review", side_effect=[[f] for f in reworded]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True}]), \
                patch.object(self.watcher, "merge", side_effect=pr_watch_common.WatchError("ratchet: 1 time.Sleep call")):
            for base in (SHA_BASE, other_base, SHA_BASE):
                with patch.object(pr_watch, "pr_snapshot", return_value=pr(baseRefOid=base)):
                    self.watcher.process("example/one", 7)
                self.watcher.state.annotate("example/one", 7, retry_after=0)
        self.assertEqual(review.call_count, 3)  # each new base is still reviewed
        findings = [c for c in self.github.call_args_list if c.args[0][:2] == ["gh", "api"]]
        success = [c for c in self.github.call_args_list if c.args[0][:3] == ["gh", "pr", "review"]]
        self.assertEqual((len(findings), len(success)), (1, 1))

    def test_a_heads_findings_are_published_once_except_a_new_p0(self):
        first = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        moved = dict(first, summary="same place, new words")
        nit = {"path": "y.py", "line": 9, "severity": "P1", "summary": "another", "suggestion": "fix"}
        p0 = {"path": "z.py", "line": 4, "severity": "P0", "summary": "breaks", "suggestion": "repair"}
        snapshot = {"head": SHA_A}
        for findings in ([first], [moved], [moved, nit], [moved, nit, p0], [moved, nit, p0]):
            self.watcher.comment_findings("example/one", 7, snapshot, findings)
        posted = [c for c in self.github.call_args_list if c.args[0][:2] == ["gh", "api"]]
        self.assertEqual(len(posted), 2)  # the first review, then the new P0 alone
        second = json.loads(posted[1].kwargs["input_text"])
        self.assertEqual([(c["path"], c["line"]) for c in second["comments"]], [("z.py", 4)])
        anchors = {(f["path"], f["line"]) for f in self.watcher.head_publication("example/one", 7, SHA_A)["published_findings"]}
        self.assertEqual(anchors, {("x.py", 2), ("z.py", 4)})
        self.watcher.comment_findings("example/one", 7, {"head": SHA_B}, [nit])
        self.assertEqual(len([c for c in self.github.call_args_list if c.args[0][:2] == ["gh", "api"]]), 3)

    def test_success_review_is_posted_once_per_head(self):
        for head in (SHA_A, SHA_A, SHA_B):
            self.watcher.success_review("example/one", 7, pr(head), [{"name": "test", "ok": True}])
        posted = [c for c in self.github.call_args_list if c.args[0][:3] == ["gh", "pr", "review"]]
        self.assertEqual(len(posted), 2)

    def test_mixed_findings_are_fully_published_before_blocking_repair(self):
        findings = [
            {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix it"},
            {"path": "x.py", "line": 4, "severity": "P2", "summary": "note", "suggestion": "consider it"},
        ]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(self.watcher, "comment_findings") as publish, \
                patch.object(self.watcher, "repair_findings", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        publish.assert_called_once()
        self.assertEqual(publish.call_args.args[:2], ("example/one", 7))
        self.assertEqual(publish.call_args.args[2]["headRefOid"], SHA_A)
        self.assertEqual(publish.call_args.args[3], findings)
        repair.assert_called_once_with(self.watcher.config["repositories"][0], pr(), self.root / "checkout", [findings[0]])
        self.assertEqual(self.watcher.state.get("example/one", 7)["published_findings"], findings)

    def test_superseded_unpublished_findings_are_grouped_once_before_new_review(self):
        findings = [
            {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix it"},
            {"path": "x.py", "line": 4, "severity": "P2", "summary": "note", "suggestion": "consider it"},
        ]
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="reviewed", semantic="findings",
                               findings=findings, review_posted=False, published_findings=[])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(SHA_B)), \
                patch.object(self.watcher, "comment_findings", side_effect=lambda repo, number, snapshot, findings, **kwargs: self.watcher.record_review_publication(repo, number, snapshot, findings, True)) as publish, \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        publish.assert_called_once()
        args, kwargs = publish.call_args
        self.assertEqual(args[:2], ("example/one", 7))
        self.assertEqual(args[2]["head"], SHA_A)
        self.assertEqual(args[3], findings)
        self.assertEqual(kwargs, {"inline": False})
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)
        history = self.watcher.state.data["finding_publications"]["example/one#7"][SHA_A]
        self.assertEqual(history["published_findings"], findings)

    def test_internal_superseded_head_can_publish_grouped_findings(self):
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        with patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            self.watcher.comment_findings("example/one", 7, {"head": SHA_A}, [finding], inline=False)
        self.assertEqual(execute.call_args.args[0][1:3], ["pr", "review"])
        self.assertIn("previous head", execute.call_args.args[0][-1])
        history = self.watcher.state.data["finding_publications"]["example/one#7"]
        self.assertEqual(history[SHA_A]["published_findings"], [finding])

    def test_mixed_findings_keep_p2_evidence_in_review_only_mode(self):
        self.watcher.config["repositories"][0]["merge"] = False
        findings = [
            {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix it"},
            {"path": "x.py", "line": 4, "severity": "P2", "summary": "note", "suggestion": "consider it"},
        ]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch_repair, "repair_with_model") as worker:
            self.watcher.process("example/one", 7)
        worker.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["findings"], findings)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "findings")

    def test_p0_findings_enter_the_authorized_repair_flow(self):
        for severity in ("P0",):
            with self.subTest(severity=severity):
                self.watcher.state.data["prs"].clear()
                findings = [{"path": "x.py", "line": 2, "severity": severity, "summary": "wrong", "suggestion": "fix it"}]
                with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                        patch.object(pr_watch, "semantic_review", return_value=findings), \
                        patch.object(self.watcher, "repair_findings", return_value=False) as repair, \
                        patch.object(pr_watch, "verify") as verify, \
                        patch.object(self.watcher, "merge") as merge:
                    self.watcher.process("example/one", 7)
                repair.assert_called_once()
                self.assertEqual(repair.call_args.args[3], findings)
                verify.assert_not_called()
                merge.assert_not_called()

    def test_missing_visual_adapter_is_recorded_not_claimed_as_acceptance(self):
        names = "web/src/views/ChatView.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat(web): a new view"), 1, {})
        self.assertEqual(commands[-1][0], "Visual acceptance not performed: no UI evidence adapter is configured")
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "run_check") as check:
            results = pr_watch_verify.verify("example/one", pr(title="feat(web): a new view"), ".", {"profile": "persona"})
        self.assertTrue(results[-1]["skipped"])
        self.assertFalse(any(call.args[0].startswith("Visual acceptance not performed:") for call in check.call_args_list))
        with patch.object(self.watcher, "success_review", wraps=self.watcher.success_review), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as execute:
            self.watcher.success_review("example/one", 7, pr(), [{"name": commands[-1][0], "ok": True}])
        self.assertIn("Screenshot acceptance was not performed", execute.call_args.args[0][-1])
        self.assertNotIn("completed successfully", execute.call_args.args[0][-1])

    def test_blocking_findings_repair_consumes_one_round_and_reviews_new_head(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="one", repair_rounds=1)
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(head=SHA_B)]), \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "repair_with_model") as worker, \
                patch.object(pr_watch_repair, "commit_and_push_repair", return_value=(7, [])) as publish:
            self.watcher.repair_findings(self.config["repositories"][0], pr(), self.root,
                                         [{"path": "x.py", "line": 1, "severity": "P0",
                                           "summary": "wrong", "suggestion": "fix"}])
        worker.assert_called_once()
        publish.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)

    def test_three_round_exhaustion_persists_and_publishes_the_last_reason(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="one", repair_rounds=3)
        self.watcher.exhaust_repair("example/one", 7, pr(), "one", "the exact failed assertion")
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "repair_exhausted")
        self.assertEqual(state["failure"], "the exact failed assertion")
        self.assertIn("after 3 rounds", self.comments[-1])
        self.assertIn("the exact failed assertion", self.comments[-1])

    def test_conflict_exhaustion_retains_the_final_reason_without_starting_another_repair(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               repair_rounds=3, failure="original conflict resolution failed")
        with patch.object(pr_watch_repair, "update_branch_with_main") as update:
            with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")):
                self.watcher.process("example/one", 7)
        update.assert_not_called()
        self.assertIn("after 3 repair rounds", self.watcher.state.get("example/one", 7)["failure"])

    def test_exhausted_review_backoff_can_recover_and_success_resets_attempts(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", review_attempts=3,
                               failure="reviewer unavailable", retry_after=time.time() + 3600,
                               retry_reason="semantic_review", verification_policy=pr_watch.VERIFICATION_POLICY_VERSION)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        self.watcher.state.data["prs"]["example/one#7"]["retry_after"] = 0
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["review_attempts"], 0)

    def test_exhausted_review_records_a_bounded_retry_without_calling_the_model(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", side_effect=pr_watch_common.WatchError("reviewer unavailable")) as review:
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 3)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["review_attempts"], 3)
        self.assertEqual(state["retry_reason"], "semantic_review")
        self.assertGreater(state["retry_after"], time.time() + 3000)

    def test_review_failure_does_not_reset_the_attempt_budget_before_backoff(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", review_attempts=1,
                               retry_reason="semantic_review", retry_after=0,
                               verification_policy=pr_watch.VERIFICATION_POLICY_VERSION)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", side_effect=pr_watch_common.WatchError("timeout")) as review:
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["review_attempts"], 3)
        self.assertGreater(self.watcher.state.get("example/one", 7)["retry_after"], time.time() + 3000)

    def test_verification_repair_backoff_resets_only_after_it_expires(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", repair_attempts=3,
                               retry_reason="verification_repair", retry_after=0,
                               verification_policy=pr_watch.VERIFICATION_POLICY_VERSION)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("failed")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.process("example/one", 7)
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_attempts"], 1)

    def test_repeated_repair_infrastructure_failure_keeps_comment_deduplication(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        failure_fingerprint = pr_watch.fingerprint("example/one", 7, SHA_A, "repair-infrastructure", "offline")
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", repair_attempts=1,
                               infrastructure_fingerprint=failure_fingerprint,
                               verification_policy=pr_watch.VERIFICATION_POLICY_VERSION)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("failed")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.process("example/one", 7)
        self.assertEqual(self.comments, [])
        self.assertEqual(self.watcher.state.get("example/one", 7)["infrastructure_fingerprint"], failure_fingerprint)

    def test_verification_infrastructure_attempts_do_not_consume_semantic_budget(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("failed")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["repair_attempts"], 1)
        self.assertEqual(state["review_attempts"], 0)

    def test_post_push_comment_failure_keeps_published_head_and_repair_round(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, repair_rounds=1)
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(head=SHA_B), pr(head=SHA_B)]), \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "repair_with_model"), \
                patch.object(pr_watch_repair, "commit_and_push_repair", return_value=(7, [])), \
                patch.object(self.watcher, "comment", side_effect=pr_watch_common.WatchError("comment unavailable")):
            self.watcher.repair_findings(self.config["repositories"][0], pr(), self.root,
                                         [{"path": "x.py", "line": 1, "severity": "P0",
                                           "summary": "wrong", "suggestion": "fix"}])
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["head"], SHA_B)
        self.assertEqual(state["repair_rounds"], 2)
        self.assertIn("comment unavailable", state["comment_failure"])

    def test_base_update_retry_recovers_after_exhausted_backoff(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", repair_attempts=3,
                               retry_reason="base_update", retry_after=0)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", return_value=False) as update:
            self.watcher.process("example/one", 7)
        update.assert_called_once()

    def test_no_patch_attempts_exhaust_the_code_repair_budget(self):
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[finding]), \
                patch.object(pr_watch_repair, "repair_with_model", side_effect=pr_watch_common.RepairAttemptError("no change")), \
                patch.object(pr_watch, "checked", return_value=""):
            for _ in range(3):
                if "example/one#7" in self.watcher.state.data["prs"]:
                    self.watcher.state.data["prs"]["example/one#7"]["retry_after"] = 0
                self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "repair_exhausted")
        self.assertEqual(state["repair_rounds"], 3)
        self.assertIn("no change", state["failure"])

    def test_semantic_repair_infrastructure_exhaustion_preserves_code_repair_budget(self):
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[finding]), \
                patch.object(pr_watch_repair, "repair_with_model", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(pr_watch, "checked", return_value=""):
            for _ in range(3):
                if "example/one#7" in self.watcher.state.data["prs"]:
                    self.watcher.state.data["prs"]["example/one#7"]["retry_after"] = 0
                self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "retry")
        self.assertNotIn("repair_exhausted", state)
        self.assertEqual(state["repair_rounds"], 0)
        self.assertGreater(state["retry_after"], time.time() + 3000)

    def test_changed_blocking_findings_are_published_before_repair(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        old = {"path": "x.py", "line": 2, "severity": "P0", "summary": "old", "suggestion": "fix"}
        new = {"path": "x.py", "line": 4, "severity": "P0", "summary": "new", "suggestion": "fix differently"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", semantic="findings",
                               findings=[old], published_findings=[], review_posted=True,
                               retry_reason="semantic_review", retry_after=0,
                               review_fingerprint=pr_watch.fingerprint("example/one", 7, SHA_A, "semantic", [old]))
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[new]), \
                patch.object(self.watcher, "repair_findings", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()
        self.assertEqual(self.github.call_count, 1)
        self.assertIn("new", self.github.call_args.kwargs["input_text"])

    def test_exhausted_semantic_review_does_not_post_a_generic_failure_comment(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.process("example/one", 7)
        self.assertEqual(self.comments, [])
        self.assertEqual(self.watcher.state.get("example/one", 7)["review_attempts"], 3)

    def test_verification_retry_preserves_p2_publication_state(self):
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[finding]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertTrue(state["review_posted"])
        self.assertEqual(state["published_findings"], [finding])
        self.assertEqual(state["published_fingerprint"], state["review_fingerprint"])

    def test_semantic_repair_failure_does_not_consume_a_review_attempt(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix it"}]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch_common, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch_repair, "repair_with_model", side_effect=pr_watch_common.WatchError("worker timeout")), \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")), \
                patch.object(pr_watch, "checked", return_value=""):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["repair_rounds"], 0)
        self.assertEqual(state["review_attempts"], 0)
        self.assertEqual(state["failure"], "worker timeout")

    def test_review_infrastructure_is_retried_immediately_then_backed_off(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", side_effect=[pr_watch_common.WatchError("timeout"), []]) as review, \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "passed")

    def test_review_deadline_is_shared_by_all_diff_chunks(self):
        clock = iter([0.0, 0.0, 70.0])
        with patch.object(pr_watch_review, "checked", side_effect=["diff", "stat", "a.py\nb.py"]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review, "split_diff", return_value=["a", "b"]), \
                patch.object(pr_watch_review.time, "monotonic", side_effect=lambda: next(clock)), \
                patch.object(pr_watch_review, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(
                    {"subtype": "success", "structured_output": {"findings": []}}), "")) as execute:
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.review("example/one", pr(), ".", {"timeout_seconds": 60})
        self.assertEqual(execute.call_count, 1)

    def test_explicit_policy_migration_recovers_interrupted_work_without_resetting_repairs(self):
        key = "example/one#7"
        self.watcher.state.data["prs"][key] = {
            "head": SHA_A, "base": SHA_BASE, "outcome": "reviewing", "review_policy": 1,
            "repair_rounds": 2, "evidence_key": key + "@" + SHA_BASE + ".." + SHA_A,
        }
        self.watcher.state.data["prs"]["example/one#8"] = {
            "head": SHA_B, "base": SHA_BASE, "outcome": "repair_exhausted", "repair_exhausted": True,
            "repair_rounds": 2, "failure": "", "review_policy": 1,
        }
        self.watcher.state.data["prs"]["example/one#9"] = {
            "head": SHA_A, "outcome": "merged", "review_policy": 1,
        }
        self.watcher.migrate_policy("new")
        self.assertEqual(self.watcher.state.data["prs"][key]["outcome"], "retry")
        self.assertEqual(self.watcher.state.data["prs"][key]["repair_rounds"], 2)
        exhausted = self.watcher.state.data["prs"]["example/one#8"]
        self.assertEqual(exhausted["repair_rounds"], 2)
        self.assertNotIn("repair_exhausted", exhausted)
        self.assertEqual(exhausted["outcome"], "repair_comment_pending")
        self.assertTrue(exhausted["failure"])
        self.assertEqual(self.watcher.state.data["prs"]["example/one#9"]["outcome"], "merged")
        self.assertNotIn("base", self.watcher.state.data["prs"]["example/one#9"])
        self.assertIn("config_fingerprint", self.watcher.state.data)

    def test_offline_configuration_migration_preserves_state_and_is_idempotent(self):
        path = self.root / "config.json"
        path.write_text(json.dumps({"repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                                                      "verify": {"commands": [["true"]]}}],
                                    "state": str(self.root / "state.json")}))
        pr_watch.atomic_json(self.root / "state.json", {"version": 1, "prs": {
            "example/one#7": {"head": SHA_A, "outcome": "reviewing", "repair_rounds": 2}}})
        with patch.object(pr_watch.Watcher, "run") as run:
            self.assertEqual(pr_watch.main(["--config", str(path), "--migrate-config"]), 0)
            run.assert_not_called()
            self.assertEqual(pr_watch.main(["--config", str(path), "--migrate-config"]), 0)
        state = pr_watch.State(self.root / "state.json")
        self.assertEqual(state.data["prs"]["example/one#7"]["repair_rounds"], 2)
        self.assertEqual(state.data["prs"]["example/one#7"]["outcome"], "retry")
        self.assertIn("config_fingerprint", state.data)

    def test_success_clears_an_old_failure_without_losing_the_new_result(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", failure="old error")
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               outcome="merged", failure=None)
        self.assertNotIn("failure", self.watcher.state.get("example/one", 7))
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               outcome="retry", failure="current error")
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               outcome="repair_published", failure=None)
        self.assertNotIn("failure", self.watcher.state.get("example/one", 7))

    def test_old_head_backoff_does_not_reset_the_new_head_budget(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", review_attempts=3,
                               retry_reason="semantic_review", retry_after=time.time() + 3600)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(head=SHA_B)), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)

    def test_saved_blocking_findings_resume_repair_when_merge_is_enabled(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="findings", semantic="findings",
                               findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "repair_findings", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()

    def test_migrated_p2_findings_continue_to_verification_and_merge(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}
        self.watcher.state.data["prs"]["example/one#7"] = {
            "head": SHA_A, "base": SHA_BASE, "evidence_key": evidence_key, "format": 1,
            "outcome": "findings", "semantic": "findings", "findings": [finding], "review_posted": True,
        }
        self.watcher.migrate_policy("new")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True}]) as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["findings"], [finding])

    def test_followup_write_without_failure_retains_the_diagnostic(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, failure="precise error")
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               infrastructure_fingerprint="same")
        self.assertEqual(self.watcher.state.get("example/one", 7)["failure"], "precise error")

    def test_repair_comment_pending_never_posts_against_a_new_head(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, outcome="repair_comment_pending", failure="old failure")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(head=SHA_B)), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False), \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        comment.assert_not_called()

    def test_retry_blocking_findings_never_repair_in_review_only_mode(self):
        self.watcher.config["repositories"][0]["merge"] = False
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", semantic="findings",
                               findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "repair_findings") as repair:
            self.watcher.process("example/one", 7)
        repair.assert_not_called()

    def test_interrupted_repairing_record_resumes_instead_of_stopping(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="repairing", semantic="findings",
                               findings=[finding], review_posted=True, repair_rounds=1)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "repair_findings", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()

    def test_heartbeat_refreshes_active_leases(self):
        self.watcher.state.data["worktree_leases"] = {
            "/tmp/lease": {"repository": "example/one", "number": 7, "active": True, "heartbeat": 1}}
        self.watcher.touch_leases(("example/one", 7))
        self.assertGreater(self.watcher.state.data["worktree_leases"]["/tmp/lease"]["heartbeat"], 1)

    def test_no_patch_repair_attempt_consumes_a_code_repair_round(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, repair_rounds=1)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "repair_with_model", side_effect=pr_watch_common.RepairAttemptError("no change")):
            with self.assertRaises(pr_watch_common.RepairAttemptError):
                self.watcher.repair_findings(self.config["repositories"][0], pr(), self.root,
                                             [{"path": "x.py", "line": 1, "severity": "P0",
                                               "summary": "wrong", "suggestion": "fix"}])
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 2)

    def test_ordinary_conflict_setup_failure_does_not_exhaust_repair(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               repair_rounds=2)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", side_effect=pr_watch_common.WatchError("setup failed")):
            self.watcher.process("example/one", 7)
        self.assertNotIn("repair_exhausted", self.watcher.state.get("example/one", 7))
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 2)

    def test_exhausted_conflict_does_not_start_another_worker(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               repair_rounds=3)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main") as update:
            self.watcher.process("example/one", 7)
        update.assert_not_called()
        self.assertIn("repair_exhausted", self.watcher.state.get("example/one", 7))

    def test_resumed_success_publication_failure_uses_backoff(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="passed", semantic="passed",
                               verification="passed", review_posted=False, success_posted=False)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "success_review", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "passed")
        self.assertGreater(state["retry_after"], time.time())

    def test_published_exhaustion_migration_does_not_repost_comments(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.data["prs"]["example/one#7"] = {
            "head": SHA_A, "base": SHA_BASE, "evidence_key": evidence_key, "format": 1,
            "outcome": "repair_exhausted", "repair_exhausted": True, "repair_rounds": 3,
            "failure": "old diagnostic", "failure_posted": True,
        }
        self.watcher.migrate_policy("new")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        comment.assert_not_called()

    def test_legacy_exhaustion_publishes_once_without_restarting_repair(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.data["prs"]["example/one#7"] = {
            "head": SHA_A, "base": SHA_BASE, "evidence_key": evidence_key, "format": 1,
            "outcome": "repair_exhausted", "repair_exhausted": True, "repair_rounds": 3,
            "failure": "final failure", "failure_posted": False,
        }
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        self.assertEqual(len(self.comments), 1)
        self.assertIn("final failure", self.comments[0])

    def test_legacy_passed_record_without_semantic_evidence_is_reviewed_again(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.data["prs"]["example/one#7"] = {
            "head": SHA_A, "base": SHA_BASE, "evidence_key": evidence_key,
            "outcome": "passed", "verification": "passed", "review_posted": True,
            "success_posted": True,
        }
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "passed")

    def test_resumed_merge_requires_passing_semantic_evidence(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="passed_with_findings",
                               verification="passed", success_posted=True, review_posted=True,
                               findings=[{"path": "x.py", "line": 2, "severity": "P0",
                                          "summary": "wrong", "suggestion": "fix"}])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False) as merge:
            self.watcher.process("example/one", 7)
        review.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["findings"], [])
        merge.assert_called_once()

    def test_migrate_config_requires_an_exclusive_offline_invocation(self):
        (self.root / "config.json").write_text(json.dumps({
            "repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                              "verify": {"commands": [["true"]]}}],
            "state": str(self.root / "state.json")}))
        with self.assertRaisesRegex(pr_watch_common.WatchError, "exclusive offline"):
            pr_watch.main(["--config", str(self.root / "config.json"), "--migrate-config", "--dry-run"])

    def test_migration_publishes_an_old_exhaustion_reason_before_allowing_another_round(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="repair_comment_pending",
                               repair_rounds=2, failure="the exact last failure")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        self.assertIn("the exact last failure", self.comments[0])
        self.assertTrue(self.watcher.state.get("example/one", 7)["repair_comment_posted"])

    def test_state_migration_preserves_policy_and_rejects_unapproved_config_changes(self):
        digest = pr_watch.fingerprint("configuration", 0, "0" * 40, "config", self.config)
        self.watcher.single_instance()
        self.addCleanup(self.watcher.instance.close)
        self.assertTrue(self.watcher.state.data["policy_migration"].startswith("review-policy-v2:"))
        self.assertEqual(digest, self.watcher.state.data["policy_migration"].split(":", 1)[1])
        self.watcher.config["workers"] = 2
        with self.assertRaises(pr_watch_common.WatchError):
            self.watcher.single_instance()

    def test_explicit_offline_migration_approves_a_changed_config_after_taking_the_lock(self):
        self.watcher.state.data["config_fingerprint"] = "previous"
        pr_watch.atomic_json(self.watcher.state.path, self.watcher.state.data)
        with self.assertRaises(pr_watch_common.WatchError):
            self.watcher.single_instance()
        changed = pr_watch.Watcher(self.watcher.config, allow_config_migration=True)
        changed.single_instance()
        changed.instance.close()
        self.assertNotEqual(pr_watch.State(self.watcher.state.path).data["config_fingerprint"], "previous")

    def test_explicit_offline_migration_takes_the_lock_before_writing_state_or_marker(self):
        self.watcher.state.data["config_fingerprint"] = "previous"
        pr_watch.atomic_json(self.watcher.state.path, self.watcher.state.data)
        changed = pr_watch.Watcher(self.watcher.config, allow_config_migration=True)
        self.addCleanup(lambda: changed.instance and changed.instance.close)
        original_flock = pr_watch.fcntl.flock
        locked = []
        def flock(fd, operation):
            self.assertFalse(self.watcher.state.path.with_suffix(".config").exists())
            self.assertEqual(pr_watch.State(self.watcher.state.path).data["config_fingerprint"], "previous")
            locked.append(True)
            return original_flock(fd, operation)
        with patch.object(pr_watch.fcntl, "flock", side_effect=flock):
            changed.single_instance()
        self.assertTrue(locked)
        self.assertNotEqual(pr_watch.State(self.watcher.state.path).data["config_fingerprint"], "previous")

    def test_resume_requires_the_exact_base_as_well_as_head(self):
        new_base = "d" * 40
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="passed", semantic="passed",
                               verification="passed", findings=[], success_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(baseRefOid=new_base)), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()
        verify.assert_called_once()

    def test_base_change_invalidates_same_head_evidence(self):
        new_base = "d" * 40
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(), pr(baseRefOid=new_base), pr(baseRefOid=new_base)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["base"], new_base)

    def test_review_only_findings_never_push_an_automatic_repair(self):
        self.config["repositories"][0]["merge"] = False
        self.watcher = pr_watch.Watcher(self.config, verbose=False)
        self.watcher.worktree = lambda *args: self.root / "checkout"
        self.watcher.remove_worktree = lambda *args: None
        self.watcher.comment = lambda repo, number, body: self.comments.append(body)
        self.watcher.stale_progress_at = lambda repo, number, snapshot, old: pr_watch_common.progress_timestamp(snapshot, [], [], [])
        findings = [{"path": "x.py", "line": 2, "severity": "P1", "summary": "wrong", "suggestion": "fix it"}]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "repair_findings") as repair, \
                patch.object(pr_watch_repair, "commit_and_push_repair") as publish:
            self.watcher.process("example/one", 7)
        repair.assert_not_called()
        publish.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "passed")

    def test_snapshot_failure_does_not_mark_head_failed(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=pr_watch_common.WatchError("closed")), \
                patch.object(self.watcher.state, "put") as put:
            self.watcher.process("example/one", 7)
        put.assert_not_called()

    def test_dry_run_never_calls_github_writes_or_merge(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", wraps=self.watcher.merge) as merge, \
                patch.object(pr_watch, "run") as api:
            self.watcher.process("example/one", 7)
        merge.assert_called_once()
        api.assert_not_called()
        self.assertEqual(self.comments, [])
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "passed")
        self.assertFalse(self.watcher.state.get("example/one", 7)["review_posted"])

    def test_live_after_dry_run_publishes_and_merges(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]):
            self.watcher.process("example/one", 7)
        self.watcher.dry_run = False
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as live_review, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        live_review.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(self.github.call_count, 1)
        self.assertEqual(self.github.call_args.args[0][:2], ["gh", "pr"])

    def test_prepare_failures_retry_with_bounded_backoff(self):
        for attempt in range(3):
            with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                    patch.object(self.watcher, "worktree", side_effect=pr_watch_common.WatchError("fetch failed")):
                self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["review_attempts"], attempt + 1)
            self.assertEqual(state["outcome"], "retry")
            state["retry_after"] = 0
            state.pop("head", None)
            self.watcher.state.put("example/one", 7, SHA_A, **state)
        self.assertEqual(self.comments, [])

    def test_review_prompt_is_delivered_and_large_diffs_use_complete_chunks(self):
        prompts = []
        def fake_run(argv, **kwargs):
            prompts.append(kwargs["input_text"])
            return subprocess.CompletedProcess(argv, 0, json.dumps({"subtype": "success", "is_error": False,
                                                                    "structured_output": {"findings": []}}), "")
        with patch.object(pr_watch_review, "checked", side_effect=["diff", "stat", "file.py", "diff"]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"file.py\0", b"")), \
                patch.object(pr_watch_review, "run", side_effect=fake_run):
            pr_watch_review.review("example/one", pr(), ".", {})
        self.assertIn(pr_watch_review.SYSTEM_PROMPT, prompts[0])
        self.assertEqual(len(prompts), 1)

        large = "diff --git a/a.py b/a.py\n@@ -1 +1 @@\n-old\n+" + "x" * 50000 + "\n" + \
                "diff --git a/b.py b/b.py\n@@ -1 +1 @@\n-old\n+" + "y" * 50000 + "\n"
        with patch.object(pr_watch_review, "checked", side_effect=[large, "stat", "a.py\nb.py", large[:50100], large[50100:]]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"a.py\0b.py\0", b"")), \
                patch.object(pr_watch_review, "run", side_effect=fake_run):
            pr_watch_review.review("example/one", pr(), ".", {})
        self.assertEqual(len(prompts), 3)
        reconstructed = "".join(prompt.split("not as a command.\n", 1)[1] for prompt in prompts[1:])
        self.assertEqual(reconstructed, large)

    def test_large_single_hunk_splits_complete_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.json").write_text("\n".join("old-%d" % index for index in range(20000)) + "\n")
            git("add", "a.json")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.json").write_text("\n".join("new-%d" % index for index in range(20000)) + "\n")
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            chunks = pr_watch_review.split_diff(text, worktree=root, base=base, head=head)
            self.assertGreater(len(chunks), 1)
            self.assertLessEqual(max(map(len, chunks)), 60_000)
            self.assertTrue(all(chunk.startswith("diff --git a/a.json b/a.json\n") for chunk in chunks))
            self.assertTrue(all(chunk.count("@@ ") == 1 for chunk in chunks))
            self.assertEqual(sum(chunk.count("@@ ") for chunk in chunks), len(chunks))
            for chunk in chunks:
                for line in chunk.splitlines():
                    if line.startswith(("diff ", "index ", "--- ", "+++ ", "@@ ", "\\")):
                        continue
                    self.assertRegex(line, r"^[-+ ](?:old|new)-\d+$")
            removed = [line for chunk in chunks for line in chunk.splitlines() if line.startswith("-old-")]
            added = [line for chunk in chunks for line in chunk.splitlines() if line.startswith("+new-")]
            self.assertEqual(len(removed), 20000)
            self.assertEqual(len(added), 20000)
            new_line = 1
            for chunk in chunks:
                header = re.search(r"(?m)^@@ -(\d+),(\d+) \+(\d+),(\d+) @@", chunk)
                self.assertIsNotNone(header)
                old_count = sum(line.startswith(("-", " ")) for line in chunk.splitlines()
                                if not line.startswith("---"))
                new_count = sum(line.startswith(("+", " ")) for line in chunk.splitlines()
                                if not line.startswith("+++"))
                self.assertEqual(int(header.group(2)), old_count)
                self.assertEqual(int(header.group(4)), new_count)
                self.assertEqual(int(header.group(3)), new_line - int(new_count == 0))
                new_line += new_count

    def test_split_hunk_header_uses_part_ranges(self):
        self.assertEqual(pr_watch_review.split_hunk_header(
            "@@ -100 +200 @@ function\n", 40, 80, 3, 2), "@@ -140,3 +280,2 @@ function\n")
        self.assertEqual(pr_watch_review.split_hunk_header(
            "@@ -100,0 +200,1 @@\n", 0, 10, 0, 1), "@@ -99,0 +210,1 @@\n")
        self.assertEqual(pr_watch_review.split_hunk_header(
            "@@ -100 +200 @@\n", 40, 0, 0, 2), "@@ -139,0 +200,2 @@\n")

    def test_large_single_diff_line_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", "-b", "main"], cwd=root, check=True, capture_output=True)
            (root / "a.py").write_text("old\n")
            subprocess.run(["git", "add", "a.py"], cwd=root, check=True)
            subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base"], cwd=root, check=True)
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "a.py").write_text("x" * 180001 + "\n")
            subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qam", "head"], cwd=root, check=True)
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.split_diff(text, worktree=root, base=base, head=head)

    def test_diff_like_added_content_is_not_a_chunk_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("old\n")
            git("add", "a.py")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.py").write_text("diff --git a/not-real b/not-real\n@@ -1 +1 @@\nnew\n")
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            self.assertEqual(pr_watch_review.split_diff(text, worktree=root, base=base, head=head), [text])

    def test_small_file_then_large_file_keeps_original_order(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("old\n")
            (root / "b.py").write_text("old\n" * 100)
            git("add", ".")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.py").write_text("small\n")
            (root / "b.py").write_text("".join(("new-%d\n" % i if i % 10 == 0 else "old\n") for i in range(100)))
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            chunks = pr_watch_review.split_diff(text, limit=250, worktree=root, base=base, head=head)
            self.assertGreater(len(chunks), 2)
            self.assertEqual(chunks[0].split("diff --git", 1)[0], "")
            self.assertIn("a.py", chunks[0])
            self.assertNotIn("b.py", chunks[0])
            self.assertTrue(all("b.py" in chunk for chunk in chunks[1:]))
            for hunk in re.findall(r"(?m)^@@ .*$", text):
                self.assertEqual(sum(chunk.count(hunk) for chunk in chunks), 1)

    def test_publication_failure_keeps_passed_evidence(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(self.watcher, "success_review", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["semantic"], "passed")
            self.assertEqual(state["verification"], "passed")
            self.assertIn("@", state["evidence_key"])
            state.pop("head", None)
            state["retry_after"] = 0
            with patch.object(self.watcher, "success_review"):
                self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 1)
        self.assertEqual(verify.call_count, 1)

    def test_title_and_test_only_visual_exemption(self):
        for title in ("test(web): wait for lazy details", "test(ios): correct an assertion",
                      "fix(ios): repair a view", "fix(android): repair a layout"):
            self.assertTrue(pr_watch_repair.test_or_fix(pr(title=title), ["web/src/views/ChatView.tsx"]))
        self.assertFalse(pr_watch_repair.test_or_fix(pr(title="feat(ios): a new view"), ["apple/Persona/Views/ChatView.swift"]))
        self.assertTrue(pr_watch_repair.test_or_fix(pr(title="feat(web): refine a test"),
                                                     ["web/src/views/chat/ContextGroup.test.tsx"]))
        self.assertFalse(pr_watch_repair.test_or_fix(pr(title="chore: change a view"),
                                                      ["web/src/views/ChatView.tsx"]))

    def test_test_only_web_change_runs_changed_tests_not_full_lanes(self):
        names = "web/src/views/chat/ContextGroup.test.tsx\n"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test_file = root / names.strip()
            test_file.parent.mkdir(parents=True)
            test_file.write_text("import { test } from 'vitest'\n")
            with patch.object(pr_watch_verify, "checked", return_value=names):
                commands = pr_watch_verify.persona_checks(str(root), pr(title="test(web): refine a test"), 1, {})
            with patch.object(pr_watch_verify, "run_check", return_value={"name": "test", "ok": True}) as check, \
                    patch.object(pr_watch_verify, "checked", return_value=names):
                pr_watch_verify.verify("example/one", pr(), str(root), {"profile": "persona"})
        self.assertEqual(commands[2], ("Changed web tests", ["node", "./node_modules/vitest/vitest.mjs", "run",
                                                             "src/views/chat/ContextGroup.test.tsx"], "web"))
        guard = next(command[1] for command in commands if command[0] == "Changed test-file baseline guards")
        self.assertEqual(guard[guard.index("--worktree") + 1], str(root))
        self.assertFalse(any(command[0] in {"CI web", "CI scripts"} for command in commands))
        self.assertTrue(any(command[0] == "Web typecheck" for command in commands))
        self.assertTrue(any(command[0] == "Privacy and documentation guards" for command in commands))
        self.assertTrue(any(command[0] == "Web extension mirror cleanup tests" for command in commands))
        self.assertEqual(check.call_args_list[0].kwargs["cwd"], str(root / "web"))
        self.assertEqual(check.call_args_list[2].kwargs["cwd"], str(root / "web"))

    def test_production_apple_fix_does_not_use_test_only_fast_path(self):
        self.assertIsNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                               ["apple/Persona/Views/ChatView.swift"]))
        self.assertIsNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                               ["apple/Shared/Models.swift"]))
        self.assertIsNotNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                                  ["apple/PersonaTests/ChatTests.swift"]))
        self.assertIsNotNone(pr_watch_verify.scoped_test_checks(".", SHA_BASE, SHA_A,
                                                                  ["apple/PersonaUITests/MinutesCardsUITests.swift"]))

    def test_apple_test_only_change_uses_portable_guards_without_macos_adapter(self):
        names = "apple/PersonaUITests/MinutesCardsUITests.swift\n"
        with patch.object(pr_watch_verify, "checked", return_value=names):
            commands = pr_watch_verify.persona_checks(".", pr(title="test(ios): refine UI assertions"), 1, {})
        self.assertIn(("Apple test-only review policy", ["true"]), commands)
        self.assertTrue(any(command[0] == "Privacy and documentation guards" for command in commands))
        self.assertFalse(any(command[0].startswith("CI ") for command in commands))

    def test_shared_test_support_keeps_full_lanes(self):
        names = "web/src/test/setup.ts\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "desktop": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="test(web): update shared setup"), 1, {})
        self.assertEqual(commands[:2], [("CI web", ["bash", "scripts/ci/web.sh"]),
                                    ("CI desktop", ["bash", "scripts/ci/desktop.sh"])])

    def test_changed_go_and_python_tests_use_focused_commands(self):
        names = ["server/internal/agents/cache_test.go", "scripts/operations/test_pr_watch.py"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in names:
                test_file = root / name
                test_file.parent.mkdir(parents=True, exist_ok=True)
                test_file.write_text("import unittest\n" if name.endswith(".py") else "package agents\n")
            calls = []
            def check(name, argv, root, timeout, environ=None, cwd=None):
                calls.append(cwd)
                return {"name": name, "command": argv, "ok": True, "summary": "", "stdout": "", "stderr": ""}
            with patch.object(pr_watch_verify, "checked", return_value="\n".join(names) + "\n"):
                commands = pr_watch_verify.persona_checks(str(root), pr(title="test: refine relevant tests"), 1, {})
            with patch.object(pr_watch_verify, "run_check", side_effect=check), \
                    patch.object(pr_watch_verify, "checked", return_value="\n".join(names) + "\n"):
                pr_watch_verify.verify("example/one", pr(), str(root), {"profile": "persona"})
        self.assertEqual(commands[0][0], "Changed Go tests")
        self.assertEqual(commands[0][1][:2], ["python3", "-I"])
        self.assertTrue(commands[0][1][2].endswith("pr_watch_go_tests.py"))
        self.assertEqual(commands[0][1][-1], "./internal/agents")
        format_command = next(command[1] for command in commands if command[0] == "Changed Go formatting")
        self.assertTrue(format_command[2].endswith("pr_watch_go_format.py"))
        self.assertTrue(format_command[2].startswith(str(HERE.parent.parent)))
        self.assertTrue(any(command[0] == "Changed Go formatting" for command in commands))
        self.assertIn(("Changed Python tests", ["python3", "-I", "-B", "-m", "unittest", "test_pr_watch.py"],
                       "scripts/operations"), commands)
        self.assertFalse(any(command[0].startswith("CI ") for command in commands))
        self.assertEqual(calls, [None, None, None, str(root / "scripts/operations"), None, None])

    def test_changed_sleep_guard_refuses_a_rise_and_passes_a_fall(self):
        # Count ratchets fail only when a count rises (docs/operations/testing.md, "Count ratchets"): a test file under
        # its baseline passes without the baseline lowered.
        import pr_watch_test_guards
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True, capture_output=True)
            baseline = root / "scripts/ci/sleep_baseline.json"
            baseline.parent.mkdir(parents=True)
            baseline.write_text('{"server/internal/example/example_test.go": 2}\n')
            test = root / "server/internal/example/example_test.go"
            test.parent.mkdir(parents=True)
            test.write_text("package example\n")
            with patch.object(pr_watch_test_guards, "checked", return_value=baseline.read_text()):
                self.assertEqual(pr_watch_test_guards.baseline_guard("server/internal/example/example_test.go", root), [])
            test.write_text("package example\n\nfunc TestWait(t *testing.T) {\n" + "\ttime.Sleep(time.Second)\n" * 3 + "}\n")
            with patch.object(pr_watch_test_guards, "checked", return_value=baseline.read_text()):
                violations = pr_watch_test_guards.baseline_guard("server/internal/example/example_test.go", root)
            self.assertEqual(len(violations), 1)
            self.assertIn("3 time.Sleep call(s), baseline 2", violations[0])

    def test_go_test_runner_uses_disposable_postgres_and_cleanup(self):
        import pr_watch_go_tests
        calls = []
        def fake_checked(argv, **kwargs):
            calls.append((argv, kwargs))
            return ""
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(pr_watch_go_tests, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_go_tests.subprocess, "run") as cleanup:
            package = Path(directory) / "server/internal/agents"
            package.mkdir(parents=True)
            (package / "cache_test.go").write_text("package agents\n")
            pr_watch_go_tests.main(["--worktree", directory, "./internal/agents"])
        command = next(argv for argv, _ in calls if "server_test_env.py" in argv[2])
        self.assertIn("-timeout=10m", command)
        self.assertEqual(command[-1], "./internal/agents")
        cleanup.assert_called_once()
        container = calls[0][0][calls[0][0].index("--name") + 1]
        self.assertEqual(cleanup.call_args.args[0], ["docker", "rm", "-fv", container])

        calls.clear()
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(pr_watch_go_tests, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_go_tests.subprocess, "run"), \
                patch.object(pr_watch_go_tests, "run", return_value=subprocess.CompletedProcess([], 0, "TestA\nTestB\nok\n", "")), \
                patch.object(pr_watch_go_tests.go_partitions, "patterns", return_value=["^(TestA)$", "^(TestB)$"]) as partitions:
            (Path(directory) / "server").mkdir()
            pr_watch_go_tests.main(["--worktree", directory, "./internal/node"])
        partitions.assert_called_once()
        runs = [argv for argv, _ in calls if "server_test_env.py" in argv[2]]
        self.assertEqual(len(runs), 2)
        self.assertEqual([run[-1] for run in runs], ["./internal/node", "./internal/node"])
        self.assertTrue(all("-timeout=10m" in run for run in runs))

    def test_go_test_runner_removes_postgres_volume_on_failure(self):
        import pr_watch_go_tests
        for fail_at in (1, 2, 3):
            with self.subTest(fail_at=fail_at):
                calls = []
                def fake_checked(argv, **kwargs):
                    calls.append(argv)
                    if len(calls) == fail_at:
                        raise pr_watch_go_tests.WatchError("fixture failure")
                    return ""
                with tempfile.TemporaryDirectory() as directory, \
                        patch.object(pr_watch_go_tests, "checked", side_effect=fake_checked), \
                        patch.object(pr_watch_go_tests.subprocess, "run") as cleanup:
                    package = Path(directory) / "server/internal/agents"
                    package.mkdir(parents=True)
                    (package / "cache_test.go").write_text("package agents\n")
                    with self.assertRaisesRegex(pr_watch_go_tests.WatchError, "fixture failure"):
                        pr_watch_go_tests.main(["--worktree", directory, "./internal/agents"])
                container = calls[0][calls[0].index("--name") + 1]
                cleanup.assert_called_once()
                self.assertEqual(cleanup.call_args.args[0], ["docker", "rm", "-fv", container])

    def test_argument_driven_python_verification_keeps_full_scripts_lane(self):
        names = "scripts/test/test_rolling_compatibility.py\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="test: update an argument-driven check"), 1, {})
        self.assertEqual(commands[0], ("CI scripts", ["bash", "scripts/ci/scripts.sh"]))

    def test_invalid_base_sha_runs_no_git_or_privacy_command(self):
        import pr_watch_test_guards
        invalid = (
            "c" * 39,
            "c" * 41,
            "C" * 40,
            "g" * 40,
            "--help",
            "-" + "c" * 39,
            "c" * 40 + "\n",
        )
        with tempfile.TemporaryDirectory() as directory:
            for base in invalid:
                for repository_guards in (False, True):
                    with self.subTest(base=base, repository_guards=repository_guards), \
                            patch.object(pr_watch_test_guards, "checked") as checked, \
                            patch.object(pr_watch_test_guards, "privacy_environment") as privacy, \
                            patch.object(pr_watch_test_guards, "documentation_guard") as documentation:
                        argv = ["--base=" + base, "--head", SHA_A, "--worktree", directory]
                        if repository_guards:
                            argv.append("--repository-guards")
                        with self.assertRaisesRegex(pr_watch_common.WatchError, "base.*40 lowercase hexadecimal"):
                            pr_watch_test_guards.main(argv)
                        checked.assert_not_called()
                        privacy.assert_not_called()
                        documentation.assert_not_called()

    def test_noncommit_or_missing_base_runs_only_the_object_validator(self):
        import pr_watch_test_guards
        for object_type in ("tree", "blob", "tag", "missing"):
            with self.subTest(object_type=object_type), tempfile.TemporaryDirectory() as directory, \
                    patch.object(pr_watch_test_guards, "privacy_environment") as privacy, \
                    patch.object(pr_watch_test_guards, "documentation_guard") as documentation:
                def fake_checked(argv, **kwargs):
                    self.assertEqual(["git", "cat-file", "-t", SHA_BASE], argv)
                    if object_type == "missing":
                        raise pr_watch_common.WatchError("missing object")
                    return object_type + "\n"
                with patch.object(pr_watch_test_guards, "checked", side_effect=fake_checked) as checked:
                    with self.assertRaisesRegex(pr_watch_common.WatchError, "base.*commit"):
                        pr_watch_test_guards.main([
                            "--base", SHA_BASE, "--head", SHA_A, "--worktree", directory, "--repository-guards",
                        ])
                checked.assert_called_once()
                privacy.assert_not_called()
                documentation.assert_not_called()

    def test_valid_base_commit_is_the_changed_guard_control(self):
        import pr_watch_test_guards
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(pr_watch_test_guards, "checked", return_value="commit\n") as checked:
            self.assertEqual(0, pr_watch_test_guards.main([
                "--base", SHA_BASE, "--head", SHA_A, "--worktree", directory,
            ]))
        checked.assert_called_once_with(["git", "cat-file", "-t", SHA_BASE], cwd=Path(directory).resolve())

    def test_documentation_guards_use_base_not_stale_watcher_or_pr_source(self):
        import pr_watch_test_guards
        base_source = (
            "from pathlib import Path\n"
            "import unittest\n"
            "ROOT = Path(__file__).resolve().parent\n"
            "class BaseGuard(unittest.TestCase):\n"
            "    def test_base_revision(self):\n"
            "        self.assertTrue((ROOT / 'base-ok').is_file())\n"
        )
        section_source = (
            "from pathlib import Path\n"
            "import sys\n"
            "import unittest\n"
            "sys.path.insert(0, str(Path(__file__).resolve().parent))\n"
            "ROOT = Path(__file__).resolve().parent\n"
            "KNOWN = Path(__file__).resolve().with_name('doc_sections_known.txt')\n"
            "class BaseGuard(unittest.TestCase):\n"
            "    def test_base_revision(self):\n"
            "        self.assertTrue((ROOT / 'base-ok').is_file())\n"
            "        self.assertEqual('', KNOWN.read_text())\n"
            "        self.assertEqual('doc_sections_known.txt', KNOWN.relative_to(ROOT).name)\n"
        )
        stale_source = (
            "import unittest\n"
            "class StaleGuard(unittest.TestCase):\n"
            "    def test_stale_watcher(self):\n"
            "        self.fail('stale watcher guard ran')\n"
        )
        relatives = ("test_doc_paths.py", "test_doc_go_paths.py", "test_doc_sections.py", "test_doc_wire.py")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            watcher = root / "watcher"
            checkout = root / "pr"
            for base in (watcher, checkout):
                tests = base / "scripts/test"
                tests.mkdir(parents=True)
                for relative in relatives:
                    (tests / relative).write_text(stale_source)
            (checkout / "base-ok").write_text("")
            (checkout / "scripts/test/doc_sections_known.txt").write_text("x\ty\tz\n")
            shown = []

            def fake_checked(argv, **kwargs):
                if argv[:3] == ["git", "cat-file", "-t"]:
                    self.assertEqual(SHA_BASE, argv[3])
                    return "commit\n"
                if argv[:2] == ["git", "show"]:
                    if not argv[2].startswith(SHA_BASE + ":scripts/test/"):
                        raise AssertionError("guard source did not come from the exact base: " + argv[2])
                    shown.append(argv[2])
                    if argv[2].endswith("doc_sections_known.txt"):
                        return ""
                    if argv[2].endswith("test_doc_sections.py"):
                        return section_source
                    return base_source
                return ""

            original_path = sys.path.copy()
            with patch.dict(sys.modules), \
                    patch.object(pr_watch_test_guards, "ROOT", watcher), \
                    patch.object(pr_watch_test_guards, "privacy_environment", return_value={}), \
                    patch.object(pr_watch_test_guards, "checked", side_effect=fake_checked):
                self.assertEqual(0, pr_watch_test_guards.main([
                    "--base", SHA_BASE, "--head", SHA_A, "--worktree", str(checkout), "--repository-guards",
                ]))
            self.assertEqual(original_path, sys.path)
            expected = {SHA_BASE + ":scripts/test/" + name
                        for name in relatives + ("doc_sections_known.txt",)}
            self.assertEqual(expected, set(shown))

    def test_extension_repository_guards_use_its_own_base_doc_checks(self):
        import pr_watch_test_guards
        check_source = (
            "import pathlib\n"
            "import sys\n"
            "ROOT = pathlib.Path(__file__).resolve().parent.parent\n"
            "def check(root):\n"
            "    return [] if (pathlib.Path(root) / 'docs-ok').is_file() else ['missing target']\n"
            "def main():\n"
            "    errors = check(ROOT)\n"
            "    print('docs: %s' % ('failed' if errors else 'ok'))\n"
            "    return bool(errors)\n"
        )
        test_source = (
            "import importlib.util\n"
            "import pathlib\n"
            "import tempfile\n"
            "import unittest\n"
            "spec = importlib.util.spec_from_file_location('check_docs', pathlib.Path(__file__).with_name('check-docs.py'))\n"
            "docs = importlib.util.module_from_spec(spec)\n"
            "spec.loader.exec_module(docs)\n"
            "class LinkTests(unittest.TestCase):\n"
            "    def test_checker(self):\n"
            "        with tempfile.TemporaryDirectory() as tmp:\n"
            "            self.assertEqual(['missing target'], docs.check(tmp))\n"
        )
        stale_source = "raise SystemExit('PR or watcher documentation source ran')\n"
        for docs_ok in (True, False):
            with self.subTest(docs_ok=docs_ok), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                watcher, checkout = root / "watcher", root / "pr"
                for base in (watcher, checkout):
                    (base / "scripts").mkdir(parents=True)
                    for name in ("check-docs.py", "test-docs.py"):
                        (base / "scripts" / name).write_text(stale_source)
                if docs_ok:
                    (checkout / "docs-ok").write_text("")
                shown = []

                def fake_checked(argv, **kwargs):
                    if argv[:3] == ["git", "cat-file", "-t"]:
                        return "commit\n"
                    if argv[:2] == ["git", "show"]:
                        shown.append(argv[2])
                        if argv[2] == SHA_BASE + ":scripts/check-docs.py":
                            return check_source
                        if argv[2] == SHA_BASE + ":scripts/test-docs.py":
                            return test_source
                        raise pr_watch_common.WatchError("fatal: path does not exist: " + argv[2])
                    return ""

                original_path = sys.path.copy()
                argv = ["--base", SHA_BASE, "--head", SHA_A, "--worktree", str(checkout), "--repository-guards",
                        "--repository", "nwverse/persona-extensions"]
                with patch.dict(sys.modules), \
                        patch.object(pr_watch_test_guards, "ROOT", watcher), \
                        patch.object(pr_watch_test_guards, "privacy_environment", return_value={}), \
                        patch.object(pr_watch_test_guards, "documentation_guard") as persona_guard, \
                        patch.object(pr_watch_test_guards, "checked", side_effect=fake_checked):
                    if docs_ok:
                        self.assertEqual(0, pr_watch_test_guards.main(argv))
                    else:
                        with self.assertRaisesRegex(pr_watch_common.WatchError, "scripts/check-docs.py"):
                            pr_watch_test_guards.main(argv)
                persona_guard.assert_not_called()
                self.assertEqual(original_path, sys.path)
                self.assertEqual([SHA_BASE + ":scripts/check-docs.py", SHA_BASE + ":scripts/test-docs.py"], shown)

    def test_extension_doc_unit_test_failure_blocks(self):
        import pr_watch_test_guards
        failing = "import unittest\nclass T(unittest.TestCase):\n    def test_x(self):\n        self.fail('x')\n"
        sources = {":scripts/check-docs.py": "def main():\n    return False\n", ":scripts/test-docs.py": failing}
        # Keep the planted failure's report out of the suite log; only the raised guard error matters.
        with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules), \
                redirect_stderr(io.StringIO()), \
                patch.object(pr_watch_test_guards, "checked",
                             side_effect=lambda argv, **kwargs: sources[argv[2][len(SHA_BASE):]]):
            with self.assertRaisesRegex(pr_watch_common.WatchError, "scripts/test-docs.py"):
                pr_watch_test_guards.extension_documentation_guards(Path(directory), SHA_BASE)

    def test_documentation_guard_reads_pr_not_watcher_root(self):
        import pr_watch_test_guards
        module = type("Guard", (), {"ROOT": None, "checked_root": None})
        module.TestRepository = type("TestRepository", (unittest.TestCase,), {
            "test_root": lambda self: setattr(module, "checked_root", module.ROOT)
        })
        with patch.object(pr_watch_test_guards, "load_guard", return_value=module), \
                patch.object(pr_watch_test_guards, "checked", return_value=""):
            pr_watch_test_guards.documentation_guard("test_doc_paths.py", Path("/tmp/pr-root"), SHA_BASE)
        self.assertEqual(module.checked_root, Path("/tmp/pr-root"))
        with patch.object(pr_watch_test_guards, "load_guard", return_value=module), \
                patch.object(pr_watch_test_guards, "checked", return_value=""):
            pr_watch_test_guards.documentation_guard("test_doc_paths.py", Path("/tmp/watcher-root"), SHA_BASE)
        self.assertEqual(module.checked_root, Path("/tmp/watcher-root"))

    def test_go_format_guard_checks_each_changed_file(self):
        import pr_watch_go_format
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test = root / "server/internal/example/example_test.go"
            test.parent.mkdir(parents=True)
            test.write_text("package example\n")
            result = subprocess.run(["python3", "-I", str(HERE / "pr_watch_go_format.py"),
                                     "--worktree", directory, "server/internal/example/example_test.go"],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            with patch.object(pr_watch_go_format, "checked", return_value=""):
                self.assertEqual(pr_watch_go_format.main(["--worktree", directory,
                                                           "server/internal/example/example_test.go"]), 0)
            with patch.object(pr_watch_go_format, "checked", return_value="example_test.go\n"):
                with self.assertRaises(pr_watch_common.WatchError):
                    pr_watch_go_format.main(["--worktree", directory,
                                             "server/internal/example/example_test.go"])

    def test_production_change_does_not_run_unrelated_full_scripts_lane(self):
        names = "server/internal/agents/cache.go\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"server": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="perf: optimize a cache"), 1, {})
        self.assertEqual(commands, [("CI server", ["bash", "scripts/ci/server.sh"])] +
                         [command for command in commands if command[0] == "Exact-head privacy scan"])

    def test_shared_ci_change_keeps_full_scripts_lane(self):
        names = "scripts/ci/server.sh\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"server": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="fix: adjust server CI"), 1, {})
        self.assertEqual(commands[:2], [("CI server", ["bash", "scripts/ci/server.sh"]),
                                       ("CI scripts", ["bash", "scripts/ci/scripts.sh"])])

    def test_zero_test_python_result_is_failed_evidence(self):
        result = {"name": "Changed Python tests", "command": ["true"], "ok": True, "summary": "",
                  "stdout": "Ran 0 tests in 0.000s\n\nOK", "stderr": ""}
        with patch.object(pr_watch_verify, "run_check", return_value=result):
            with self.assertRaises(pr_watch_common.VerificationError):
                pr_watch_verify.verify("example/one", pr(), ".", {"profile": "commands", "commands": [
                    {"name": "Changed Python tests", "argv": ["true"]}]})
        self.assertFalse(result["ok"])
        self.assertIn("Ran 0 tests", result["summary"])

    def test_focused_test_change_keeps_other_selected_platform_lanes(self):
        for name, lane in (("apple/PersonaTests/ChatTests.swift", "CI server"),
                           ("scripts/mock/test_desktops_mock.py", "CI desktop")):
            with self.subTest(name=name):
                with patch.object(pr_watch_verify, "checked", return_value=name + "\n"), \
                        patch.object(pr_watch_verify, "load_paths") as selector:
                    selector.return_value.select.return_value = {lane.split()[1]: True, "scripts": True}
                    commands = pr_watch_verify.persona_checks(".", pr(title="test: refine a client contract"), 1, {})
                self.assertIn((lane, ["bash", "scripts/ci/" + lane.split()[1] + ".sh"]), commands)

    def test_real_run_check_detects_successful_zero_test_suite(self):
        result = pr_watch_verify.run_check("Changed Python tests", ["python3", "-I", "-c",
                                                                    "import sys; sys.stderr.write('Ran 0 tests in 0.000s\\nOK\\n')"],
                                           ".", 30)
        self.assertIn("Ran 0 tests", result["stderr"])
        with patch.object(pr_watch_verify, "run_check", return_value=result):
            with self.assertRaises(pr_watch_common.VerificationError):
                pr_watch_verify.verify("example/one", pr(), ".", {"profile": "commands", "commands": [
                    {"name": "Changed Python tests", "argv": ["true"]}]})
        self.assertFalse(result["ok"])

    def test_deleted_go_tests_are_not_reported_as_passing(self):
        import pr_watch_go_tests
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / "server/internal/example").mkdir(parents=True)
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_go_tests.main(["--worktree", directory, "./internal/example"])

    def test_privacy_environment_retains_maintainer_config_not_build_home(self):
        import pr_watch_test_guards
        with patch.dict(os.environ, {"HOME": "/maintainer-home", "XDG_CONFIG_HOME": "/maintainer-config",
                                     "GH_TOKEN": "not-for-scanner"}):
            env = pr_watch_test_guards.privacy_environment(Path("/tmp/pr-checkout"))
        self.assertEqual(env["XDG_CONFIG_HOME"], "/maintainer-config")
        self.assertNotEqual(env["HOME"], "/maintainer-home")
        self.assertEqual(env["PERSONA_PRIVACY_ROOT"], "/tmp/pr-checkout")
        self.assertNotIn("GH_TOKEN", env)

    def test_production_privacy_scan_uses_maintainer_environment_with_pr_root(self):
        calls = []
        def check(name, argv, root, timeout, environ=None, cwd=None):
            calls.append(environ)
            return {"name": name, "command": argv, "ok": True, "summary": "", "stdout": "", "stderr": ""}
        with patch.object(pr_watch_verify, "checked", return_value="server/internal/agents/cache.go\n"), \
                patch.object(pr_watch_verify, "run_check", side_effect=check):
            pr_watch_verify.verify("example/one", pr(), ".", {"profile": "persona"}, environ={"HOME": "/isolated-home"})
        self.assertEqual(calls[-1]["PERSONA_PRIVACY_ROOT"], ".")
        self.assertNotEqual(calls[-1].get("HOME"), "/isolated-home")

    def test_fix_ui_change_runs_checks_without_visual_adapter(self):
        names = "web/src/views/ChatView.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="fix(web): repair a view"), 1, {})
        self.assertEqual(commands[0], ("CI web", ["bash", "scripts/ci/web.sh"]))
        self.assertEqual(commands[1][0], "Exact-head privacy scan")

    def test_feature_ui_change_without_adapter_records_the_limitation(self):
        names = "web/src/views/ChatView.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat(web): a new view"), 1, {})
        self.assertEqual(commands[-1][0], "Visual acceptance not performed: no UI evidence adapter is configured")

    def test_feature_ui_change_with_adapter_still_requires_visual_evidence(self):
        names = "web/src/views/ChatView.tsx\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"web": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat(web): a new view"), 1,
                                                      {"ui_evidence_command": ["visual", "{head}"]})
        self.assertIn(("visual evidence", ["visual", SHA_A]), commands)

    def test_superseded_prerequisite_failure_retries_only_verification(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               format=1, base=SHA_BASE, semantic="passed", findings=[],
                               review_fingerprint="reviewed", outcome="failed", failure_posted=True,
                               retry_after=10**12, repair_rounds=1, repair_attempts=3,
                               failure="UI changes need visual evidence")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True}]), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        review.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(state["outcome"], "merged")
        self.assertEqual(state["repair_rounds"], 1)
        self.assertEqual(state["verification_policy"], pr_watch.VERIFICATION_POLICY_VERSION)

    def test_repository_policy_migration_preserves_merged_and_continuation_records(self):
        records = {
            "example/one#7": {"head": SHA_A, "base": SHA_BASE, "outcome": "merged",
                              "semantic": "passed", "verification": "passed", "success_posted": True},
            "example/one#8": {"head": SHA_B, "base": SHA_BASE, "outcome": "continuation",
                              "continuation": 99, "semantic": "findings", "review_posted": True},
            "example/one#9": {"head": SHA_A, "base": SHA_BASE, "outcome": "reviewing",
                              "repair_rounds": 2},
        }
        self.watcher.state.data["prs"] = json.loads(json.dumps(records))
        self.watcher.state.data["repository_policy"] = "old-mapping"
        self.watcher.allow_config_migration = True
        self.watcher.migrate_policy("new", allow_config_migration=True)
        for key in ("example/one#7", "example/one#8"):
            self.assertEqual(self.watcher.state.data["prs"][key], records[key])
        self.assertEqual(self.watcher.state.data["prs"]["example/one#9"]["outcome"], "retry")
        self.assertEqual(self.watcher.state.data["prs"]["example/one#9"]["repair_rounds"], 2)

    def test_review_only_published_blocking_findings_stop_without_a_worktree(self):
        self.watcher.repositories["example/one"]["merge"] = False
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="findings", semantic="findings",
                               findings=[finding], review_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "worktree") as worktree, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        worktree.assert_not_called()
        review.assert_not_called()

    def test_policy_migration_preserves_completed_gates_and_terminal_repairs(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        for outcome, fields in (
                ("merged", {"verification_policy": 1, "merged": True}),
                ("passed", {"verification": "passed", "verification_policy": 1, "review_posted": True, "success_posted": True}),
                ("passed_with_base_failures", {"verification": "base_failure", "verification_policy": 1, "review_posted": True, "success_posted": True}),
                ("findings", {"semantic": "findings", "review_posted": True}),
                ("repair_exhausted", {"repair_exhausted": True, "repair_rounds": 3}),
        ):
            with self.subTest(outcome=outcome):
                self.watcher.state.data["prs"].pop("example/one#7", None)
                self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                                       format=1, base=SHA_BASE, outcome=outcome, **fields)
                if outcome == "findings":
                    self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                                           findings=[{"path": "x.py", "line": 1, "severity": "P2",
                                                      "summary": "note", "suggestion": "consider it"}])
                with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                        patch.object(pr_watch, "semantic_review") as review, \
                        patch.object(pr_watch, "verify") as verify, \
                        patch.object(self.watcher, "merge") as merge:
                    self.watcher.process("example/one", 7)
                review.assert_not_called()
                verify.assert_not_called()
                merge.assert_not_called()

    def test_findings_publication_failure_preserves_review_and_retries_repair(self):
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[finding]) as review, \
                patch.object(self.watcher, "comment_findings", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(self.watcher, "repair_findings", return_value=False) as repair:
            self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["semantic"], "findings")
            self.assertEqual(state["outcome"], "reviewed")
            self.assertFalse(state["review_posted"])
            self.assertEqual(state["review_attempts"], 0)
            self.assertEqual(state["publication_attempts"], 1)
            repair.assert_not_called()
            self.watcher.state.put("example/one", 7, pr()["headRefOid"],
                                   evidence_key=state["evidence_key"], retry_after=0)
            with patch.object(self.watcher, "comment_findings", return_value=None):
                self.watcher.process("example/one", 7)
        review.assert_called_once()
        repair.assert_called_once()

    def test_review_only_p2_comment_does_not_promise_a_merge(self):
        self.watcher.config["repositories"][0]["merge"] = False
        self.watcher.repositories["example/one"]["merge"] = False
        with patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            self.watcher.comment_findings("example/one", 7, pr(), [
                {"path": "x.py", "line": 2, "severity": "P2", "summary": "note", "suggestion": "consider"}])
        payload = execute.call_args.kwargs["input_text"]
        self.assertIn("no automatic repair or merge", payload)
        self.assertNotIn("merging continue", payload)

    def test_semantic_backoff_is_not_cleared_by_verification_policy_migration(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="retry", retry_reason="semantic_review",
                               retry_after=time.time() + 3600, review_attempts=3)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["review_attempts"], 3)
        self.assertEqual(state["retry_reason"], "semantic_review")

    def test_review_only_publication_retry_reuses_completed_review(self):
        self.watcher.repositories["example/one"]["merge"] = False
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="reviewed", semantic="findings",
                               findings=[finding], review_posted=False)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "comment_findings") as publish, \
                patch.object(self.watcher, "worktree") as worktree, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        publish.assert_called_once_with("example/one", 7, pr(), [finding])
        worktree.assert_not_called()
        review.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "findings")

    def test_completed_base_failure_retry_preserves_its_disclosure(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="retry", semantic="passed",
                               verification="base_failure", findings=[], success_posted=False,
                               base_failures={"reproduced": True, "reason": "same named test"},
                               checks=[{"name": "test", "command": ["true"], "ok": False}])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "success_review") as publish, \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
        self.assertIsNotNone(publish.call_args.args[4])
        self.assertEqual(publish.call_args.args[4]["reason"], "same named test")

    def test_repository_policy_reset_reverifies_exact_head(self):
        self.watcher.state.data["repository_policy"] = "old"
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="passed", semantic="passed",
                               verification="passed", verification_policy=pr_watch.VERIFICATION_POLICY_VERSION,
                               success_posted=True, findings=[])
        self.watcher.migrate_policy("new")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        verify.assert_called_once()

    def test_publication_history_is_independent_per_head(self):
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.record_review_publication("example/one", 7, pr(SHA_A), [finding], True)
        self.watcher.record_review_publication("example/one", 7, pr(SHA_B), [finding], False)
        history = self.watcher.state.data["finding_publications"]["example/one#7"]
        self.assertEqual(history[SHA_A]["published_findings"], [finding])
        self.assertEqual(history[SHA_B]["published_findings"], [])

    def test_completed_retry_resumes_success_without_repeating_verification(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="retry", semantic="passed",
                               verification="passed", retry_reason="verification_repair",
                               checks=[{"name": "test", "command": ["true"], "ok": True}],
                               findings=[], success_posted=False, review_posted=False)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_infrastructure_retry_never_skips_saved_blocking_findings(self):
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="retry", semantic="findings",
                               findings=[finding], review_posted=True, published_findings=[finding],
                               retry_reason="verification_repair", retry_after=0)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "repair_findings", return_value=False) as repair, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()
        verify.assert_not_called()
        merge.assert_not_called()

    def test_merge_phase_failure_does_not_become_a_success_on_next_poll(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "success_review", return_value=None), \
                patch.object(self.watcher, "merge", side_effect=pr_watch_common.WatchError("merge unavailable")) as merge:
            self.watcher.process("example/one", 7)
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merge_failed")
            self.assertEqual(merge.call_count, 1)
            self.watcher.state.put("example/one", 7, SHA_A,
                                   evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                                   retry_after=0)
            self.watcher.process("example/one", 7)
            self.assertEqual(merge.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merge_failed")
        self.assertNotEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_verification_repair_comment_outage_keeps_new_head_for_fresh_review(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, semantic="passed", findings=[], repair_rounds=0)
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(SHA_B)]), \
                patch.object(pr_watch_repair, "main_failure_comparison", return_value={"reproduced": False}), \
                patch.object(pr_watch_repair, "update_branch_with_main", return_value=None), \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "repair_with_model", return_value=None), \
                patch.object(pr_watch_repair, "commit_and_push_repair", return_value=(7, [])), \
                patch.object(self.watcher, "comment", side_effect=pr_watch_common.WatchError("offline")):
            self.watcher.repair_verification(self.watcher.config["repositories"][0], pr(), self.root,
                                             pr_watch_common.VerificationError("real failure"))
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["head"], SHA_B)
        self.assertEqual(state["outcome"], "retry")
        self.assertIn("comment_failure", state)

    def test_verification_failure_uses_bounded_repair(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("real test failed")), \
                patch.object(self.watcher, "repair_verification", return_value=False) as repair:
            self.watcher.process("example/one", 7)
        repair.assert_called_once()
        self.assertNotEqual(self.watcher.state.get("example/one", 7)["outcome"], "failed")

    def test_verification_repair_publication_without_semantic_findings_retries_new_head(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, semantic="passed", findings=[], repair_rounds=0)
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(SHA_B)]), \
                patch.object(pr_watch_repair, "main_failure_comparison", return_value={"reproduced": False}), \
                patch.object(pr_watch_repair, "update_branch_with_main", return_value=None), \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "repair_with_model", return_value=None), \
                patch.object(pr_watch_repair, "commit_and_push_repair", return_value=(7, [])):
            self.watcher.repair_verification(self.watcher.config["repositories"][0], pr(), self.root,
                                             pr_watch_common.VerificationError("real failure"))
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["head"], SHA_B)
        self.assertEqual(state["outcome"], "retry")
        self.assertEqual(state["repair_rounds"], 1)

    def test_under_budget_interrupted_repair_is_not_marked_exhausted(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, outcome="repairing", repair_rounds=2)
        result = self.watcher.exhaust_repair("example/one", 7, pr(),
                                             "example/one#7@" + SHA_BASE + ".." + SHA_A,
                                             "worker interrupted")
        state = self.watcher.state.get("example/one", 7)
        self.assertFalse(result)
        self.assertEqual(state["outcome"], "retry")
        self.assertEqual(state["repair_rounds"], 2)
        self.assertNotIn("repair_exhausted", state)
        self.assertEqual(self.comments, [])

    def test_stale_close_pending_requires_fresh_review(self):
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, format=1, outcome="stale_close_pending",
                               semantic="passed", verification="passed", success_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()

    def test_semantic_repair_infrastructure_failure_preserves_round(self):
        finding = {"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}
        self.watcher.state.put("example/one", 7, SHA_A,
                               evidence_key="example/one#7@" + SHA_BASE + ".." + SHA_A,
                               base=SHA_BASE, repair_rounds=1)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "checked", return_value=""), \
                patch.object(pr_watch_repair, "repair_with_model", return_value=None), \
                patch.object(pr_watch_repair, "commit_and_push_repair", side_effect=pr_watch_common.WatchError("offline")):
            with self.assertRaises(pr_watch_common.WatchError):
                self.watcher.repair_findings(self.watcher.config["repositories"][0], pr(), self.root, [finding])
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 1)

    def test_repair_stops_after_three_rounds(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="one", repair_rounds=3)
        with patch.object(self.watcher, "comment") as comment, \
                patch.object(pr_watch_repair, "main_failure_comparison") as compare:
            result = self.watcher.repair_verification(self.config["repositories"][0], pr(), self.root, pr_watch_common.WatchError("still failing"))
        self.assertFalse(result)
        compare.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "repair_exhausted")
        self.assertIn("after 3 rounds", comment.call_args.args[2])

    def test_main_owned_failure_is_not_a_passed_test(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="one", repair_rounds=0)
        with patch.object(pr_watch_repair, "main_failure_comparison", return_value={"reproduced": True, "packages": ["./internal/node"]}), \
                patch.object(self.watcher, "comment"):
            result = self.watcher.repair_verification(self.config["repositories"][0], pr(), self.root, pr_watch_common.WatchError("--- FAIL: TestBase"))
        self.assertTrue(result)
        self.assertEqual(self.watcher.state.get("example/one", 7)["verification"], "base_failure")

    def test_base_failure_continues_to_exact_head_merge(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("--- FAIL: TestBase")), \
                patch.object(self.watcher, "repair_verification", return_value="base_failure"), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        merge.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_repair_commit_is_verified_at_new_head_before_push(self):
        with tempfile.TemporaryDirectory() as directory:
            calls = []
            def fake_checked(argv, **kwargs):
                calls.append(argv)
                if argv[:2] == ["git", "rev-parse"] and "FETCH_HEAD" in argv:
                    return SHA_BASE + "\n"
                if argv[:2] == ["git", "rev-parse"]:
                    return SHA_B + "\n"
                if argv[:2] == ["git", "ls-remote"]:
                    return SHA_A + "\trefs/heads/fix/example\n"
                if argv[:3] == ["git", "diff", "--name-only"]:
                    return ""
                if argv[:4] == ["git", "diff", "--cached", "--name-only"]:
                    return "repair.py\0"
                if argv[:4] == ["git", "diff", "--cached", "--stat"]:
                    return "repair.py | 1 +\n"
                if argv[:4] == ["git", "diff", "--name-only", "--no-renames"]:
                    return "repair.py\0"
                if argv[:3] == ["git", "diff", "--name-only"]:
                    return ""
                if argv[:3] == ["git", "diff", "--cached"]:
                    return "repair.py | 1 +\n"
                return ""
            def fake_verify(repo, repaired_pr, *args):
                calls.append(repaired_pr["headRefOid"])
                return []
            with patch.object(pr_watch_repair, "pr_snapshot", return_value=pr()), \
                    patch.object(pr_watch_repair, "verify", side_effect=fake_verify), \
                    patch.object(pr_watch_repair, "checked", side_effect=fake_checked), \
                    patch.object(pr_watch_repair, "privacy_scanner_environment", return_value={}), \
                    patch.object(pr_watch_repair, "repair_repository_copy", return_value=nullcontext(directory)):
                pr_watch_repair.commit_and_push_repair("example/one", pr(), directory, SHA_BASE, "failure", {"profile": "commands", "commands": [["true"]]})
        self.assertIn(SHA_B, calls)
        commit_index = next(index for index, argv in enumerate(calls) if isinstance(argv, list) and "commit" in argv)
        push_index = next(index for index, argv in enumerate(calls) if isinstance(argv, list) and "push" in argv)
        self.assertLess(commit_index, calls.index(SHA_B))
        self.assertLess(calls.index(SHA_B), push_index)
    def test_main_comparison_requires_named_tests_and_packages(self):
        with patch.object(pr_watch_repair, "checked", return_value=""):
            comparison = pr_watch_repair.main_failure_comparison("example/one", pr(), ".", "CI server failed")
        self.assertFalse(comparison["reproduced"])
        self.assertEqual(pr_watch_repair.failed_test_names("--- PASS: TestGood\n--- FAIL: TestBad/case\n"), ["TestBad"])
        pr_output = "--- FAIL: TestBase\n--- FAIL: TestPR\nAssertionError: PR defect"
        base_output = "--- FAIL: TestBase\nAssertionError: base defect"
        self.assertNotEqual(pr_watch_repair.failure_test_ids(pr_output), pr_watch_repair.failure_test_ids(base_output))

    def test_main_comparison_matches_infrastructure_failure_without_test_names(self):
        failure = "CI server failed:\nFAIL Browser runner resources (0.2s, exit 133)"
        base = "CI server failed:\nFAIL Browser runner resources (1.4s, exit 133)"
        with patch.object(pr_watch_repair, "checked", return_value=""), \
                patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 133, "", base)):
            comparison = pr_watch_repair.main_failure_comparison(
                "example/one", pr(), ".", failure, command=["scripts/ci/server.sh"])
        self.assertTrue(comparison["reproduced"])
        with patch.object(pr_watch_repair, "checked", return_value=""), \
                patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 1, "", "FAIL Web tests (exit 1)")):
            comparison = pr_watch_repair.main_failure_comparison(
                "example/one", pr(), ".", failure, command=["scripts/ci/server.sh"])
        self.assertFalse(comparison["reproduced"])
        with patch.object(pr_watch_repair, "checked", return_value=""), \
                patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "", "")):
            comparison = pr_watch_repair.main_failure_comparison(
                "example/one", pr(), ".", failure, command=["scripts/ci/server.sh"])
        self.assertFalse(comparison["reproduced"])

    def test_gh_json_passes_a_continuation_payload_to_stdin(self):
        payload = '{"title":"fix: continuation","head":"owner:branch"}'
        with patch.object(pr_watch_common, "checked", return_value='{"number":9}') as checked:
            result = pr_watch_common.gh_json(["api", "repos/example/one/pulls", "--input", "-"], input_text=payload)
        self.assertEqual(result, {"number": 9})
        self.assertEqual(checked.call_args.kwargs["input_text"], payload)
        self.assertEqual(checked.call_args.args[0], ["gh", "api", "repos/example/one/pulls", "--input", "-"])

    def test_open_continuation_matches_title_or_body(self):
        fork = pr(isCrossRepository=True)
        for title, body in (("fix: continue #7 verification repair", ""),
                            ("fix: verification repair", "Continuation of https://github.com/example/one/pull/7.")):
            with self.subTest(title=title, body=body):
                candidates = [
                    dict(pr(), title=title, body=body),  # The fork itself.
                    dict(pr(), number=8, title="fix: continue #70 verification repair", body="Refs #7"),
                    dict(pr(), number=9, title=title, body=body, state="CLOSED"),
                    dict(pr(), number=10, title=title, body=body, state="MERGED", mergedAt="2026-11-16T00:00:00Z"),
                    dict(pr(), number=11, title=title, body=body),
                ]
                with patch.object(pr_watch_repair, "gh_json", return_value=candidates) as search, \
                        patch.object(pr_watch_repair, "checked") as checked:
                    self.assertEqual(pr_watch_repair.find_continuation("example/one", fork), 11)
                self.assertIn('"7" in:title,body', search.call_args.args[0])
                checked.assert_not_called()

    def test_merged_continuation_must_postdate_current_fork_head(self):
        continuation = dict(pr(), number=9, title="fix: continue #7 verification repair",
                            state="MERGED", mergedAt="2026-11-16T00:00:00Z")
        with patch.object(pr_watch_repair, "gh_json", return_value=[continuation]), \
                patch.object(pr_watch_repair, "checked", side_effect=["2026-11-15T00:00:00Z\n",
                                                                     "2026-11-17T00:00:00Z\n"]) as checked:
            self.assertEqual(pr_watch_repair.find_continuation("example/one", pr(isCrossRepository=True)), 9)
            self.assertIsNone(pr_watch_repair.find_continuation("example/one", pr(head=SHA_B, isCrossRepository=True)))
        self.assertIn("repos/example/one/commits/" + SHA_A, checked.call_args_list[0].args[0])
        self.assertIn("repos/example/one/commits/" + SHA_B, checked.call_args_list[1].args[0])

    def test_reused_continuation_survives_restart_and_base_change(self):
        repo = "example/one"
        fork = pr(isCrossRepository=True)
        findings = [{"severity": "P0", "path": "file.py", "line": 1, "summary": "bad", "suggestion": "fix"}]
        evidence = repo + "#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put(repo, 7, SHA_A, evidence_key=evidence)
        with patch.object(pr_watch, "pr_snapshot", return_value=fork), \
                patch.object(pr_watch, "checked"), \
                patch.object(pr_watch_repair, "repair_with_model"), \
                patch.object(pr_watch_repair, "commit_and_push_repair", return_value=(9, None)):
            self.watcher.repair_findings(self.config["repositories"][0], fork, self.root, findings)
        self.assertEqual(self.comments, [])

        restarted = pr_watch.Watcher(self.config, verbose=False)
        record = restarted.state.get(repo, 7)
        self.assertEqual((record["head"], record["outcome"], record["continuation"]), (SHA_A, "continuation", 9))
        with patch.object(pr_watch, "pr_snapshot", return_value=dict(fork, baseRefOid="d" * 40)), \
                patch.object(restarted, "worktree") as worktree, \
                patch.object(pr_watch_repair, "find_continuation") as search:
            restarted.process(repo, 7)
        worktree.assert_not_called()
        search.assert_not_called()
        self.assertEqual(restarted.state.get(repo, 7), record)

    def test_continuation_pr_links_original_issue(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def fake_checked(argv, **kwargs):
                if argv[:2] == ["git", "rev-parse"]:
                    if "FETCH_HEAD" in argv:
                        return SHA_BASE + "\n"
                    return SHA_B + "\n"
                if argv[:2] == ["git", "cat-file"]:
                    return "".join(line.strip() + " commit\n" for line in kwargs["input_text"].splitlines())
                return ""
            with patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: pr(head=SHA_B if number == 9 else SHA_A)), \
                    patch.object(pr_watch_repair, "verify", return_value=[]), \
                    patch.object(pr_watch_repair, "checked", side_effect=fake_checked), \
                    patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "", "")), \
                    patch.object(pr_watch_repair, "gh_json", side_effect=[[], [], {"number": 9}]) as created:
                pr_watch_repair.commit_and_push_repair("example/one", pr(isCrossRepository=True), root, SHA_BASE, "failure", {"profile": "commands", "commands": [["true"]]}, allow_merge_only=True)
        self.assertIn("Refs #6", created.call_args.kwargs["input_text"])

    def test_continuation_without_issue_opens_when_the_repository_requires_no_link(self):
        """A repository with no issue workflow can still repair a fork: the body names no issue (#16751)."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def fake_checked(argv, **kwargs):
                if argv[:2] == ["git", "rev-parse"]:
                    if "FETCH_HEAD" in argv:
                        return SHA_BASE + "\n"
                    return SHA_B + "\n"
                if argv[:2] == ["git", "cat-file"]:
                    return "".join(line.strip() + " commit\n" for line in kwargs["input_text"].splitlines())
                return ""
            fork = pr(isCrossRepository=True, body="Automated repair of a fork with no issue workflow.")
            with patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: pr(head=SHA_B if number == 9 else SHA_A)), \
                    patch.object(pr_watch_repair, "verify", return_value=[]), \
                    patch.object(pr_watch_repair, "checked", side_effect=fake_checked), \
                    patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "", "")), \
                    patch.object(pr_watch_repair, "gh_json", side_effect=[[], [], {"number": 9}]) as created:
                number, _ = pr_watch_repair.commit_and_push_repair(
                    "example/one", fork, root, SHA_BASE, "failure", {"profile": "commands", "commands": [["true"]]},
                    allow_merge_only=True, require_issue_link=False)
        self.assertEqual(number, 9)
        body = json.loads(created.call_args.kwargs["input_text"])["body"]
        self.assertNotIn("Refs #", body)
        self.assertIn("Continuation of #7", body)

    def test_continuation_without_issue_is_refused_when_the_repository_requires_a_link(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def fake_checked(argv, **kwargs):
                if argv[:2] == ["git", "rev-parse"]:
                    if "FETCH_HEAD" in argv:
                        return SHA_BASE + "\n"
                    return SHA_B + "\n"
                if argv[:2] == ["git", "cat-file"]:
                    return "".join(line.strip() + " commit\n" for line in kwargs["input_text"].splitlines())
                return ""
            fork = pr(isCrossRepository=True, body="Automated repair of a fork with no issue workflow.")
            with patch.object(pr_watch_repair, "pr_snapshot", side_effect=lambda repo, number: pr(head=SHA_B if number == 9 else SHA_A)), \
                    patch.object(pr_watch_repair, "verify", return_value=[]), \
                    patch.object(pr_watch_repair, "checked", side_effect=fake_checked), \
                    patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "", "")), \
                    patch.object(pr_watch_repair, "gh_json", side_effect=[[], []]) as created:
                with self.assertRaises(pr_watch_common.WatchError):
                    pr_watch_repair.commit_and_push_repair(
                        "example/one", fork, root, SHA_BASE, "failure", {"profile": "commands", "commands": [["true"]]},
                        allow_merge_only=True)
        created.assert_called()  # lookups ran; no pull request was created

    def test_every_failed_lane_must_reproduce_on_main(self):
        failures = [{"command": ["test", "one"], "output": "--- FAIL: TestA\n"},
                    {"command": ["test", "two"], "output": "--- FAIL: TestB\n"}]
        def fake_run(argv, **kwargs):
            if argv[:2] == ["git", "worktree"]:
                return subprocess.CompletedProcess(argv, 0, "", "")
            return subprocess.CompletedProcess(argv, 1, "--- FAIL: TestA\n", "") if "one" in argv else subprocess.CompletedProcess(argv, 0, "ok\n", "")
        with patch.object(pr_watch_repair, "checked", return_value=""), \
                patch.object(pr_watch_repair, "run", side_effect=fake_run):
            comparison = pr_watch_repair.main_failure_comparison("example/one", pr(), self.root, "", failures=failures)
        self.assertFalse(comparison["reproduced"])

    def test_repair_budget_survives_new_head(self):
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key="old", repair_rounds=2)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(head=SHA_B)), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 2)

    def test_repair_exhausted_is_terminal_without_repeated_comments(self):
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               repair_exhausted=True, failure_posted=True)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        comment.assert_not_called()

    def test_dry_run_never_pushes_or_comments_repairs(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING")), \
                patch.object(pr_watch_repair, "update_branch_with_main") as update, \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        update.assert_not_called()
        comment.assert_not_called()

    def test_all_lanes_finish_before_reporting_failure(self):
        config = {"profile": "commands", "commands": [["false"], ["true"]]}
        with patch.object(pr_watch_verify, "run_check", side_effect=[
                {"name": "one", "command": ["false"], "ok": False, "summary": "bad", "stdout": "bad", "stderr": ""},
                {"name": "two", "command": ["true"], "ok": True, "summary": "", "stdout": "", "stderr": ""}]) as check:
            with self.assertRaises(pr_watch_common.VerificationError):
                pr_watch_verify.verify("example/one", pr(), ".", config)
        self.assertEqual(check.call_count, 2)

    def test_repair_scanner_preserves_all_trusted_config_adapters(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True, capture_output=True)
            config = root / "config.json"
            config.write_text(json.dumps({"hub": {"url": "https://synthetic-private-host.internal"}}))
            marker = "JA" + "RVIS_CONFIG"
            (root / "fixture.txt").write_text("synthetic-private-host.internal\n")
            subprocess.run(["git", "add", "fixture.txt"], cwd=root, check=True, capture_output=True)
            with patch.dict(os.environ, {marker: str(config), "GH_TOKEN": "not-for-scanner"}):
                with self.assertRaises(pr_watch_common.WatchError):
                    pr_watch_repair.privacy_scanner_environment(root)
                (root / "fixture.txt").unlink()
                subprocess.run(["git", "rm", "--cached", "-q", "fixture.txt"], cwd=root, check=True, capture_output=True)
                env = pr_watch_repair.privacy_scanner_environment(root)
            self.assertEqual(env[marker], str(config))
            self.assertNotIn("GH_TOKEN", env)
            self.assertNotEqual(env["HOME"], os.environ["HOME"])

    def test_repair_worker_has_no_github_credentials(self):
        calls = []
        def repair_checked(argv, **kwargs):
            calls.append(argv)
            if argv[:3] == ["git", "rev-parse", "HEAD"]:
                return SHA_A + "\n"
            if argv[:4] == ["git", "diff", "--cached", "--stat"]:
                return "" if len(calls) <= 2 else "repair.py | 1 +\n"
            return ""
        with patch.dict(os.environ, {"GH_TOKEN": "not-for-worker", "PR_WATCH_WEBHOOK_SECRET": "not-for-worker",
                                     "ANTHROPIC_API_KEY": "model-only"}), \
                patch.object(pr_watch_repair.shutil, "which", return_value="/usr/bin/bwrap"), \
                patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 0, "ok", "")) as worker_run, \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch_repair, "repair_repository_copy", return_value=nullcontext(self.root)), \
                patch.object(pr_watch_repair, "checked", side_effect=repair_checked):
            pr_watch_repair.repair_with_model("example/one", pr(), self.root, "failure", {})
        env = worker_run.call_args.kwargs["environ"]
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("PR_WATCH_WEBHOOK_SECRET", env)
        self.assertIn("ANTHROPIC_API_KEY", env)
        with patch.object(pr_watch_repair.shutil, "which", return_value="/usr/bin/bwrap"):  # a machine without bwrap too
            argv = pr_watch_repair.repair_worker_argv(["claude", "--version"], self.root)
        home = argv[argv.index("HOME") + 1]
        self.assertNotEqual(Path(home), self.root)
        self.assertFalse(Path(home).is_relative_to(self.root))
        self.assertLess(argv.index("--chdir"), argv.index("--bind"))
        self.assertIn("--proc", argv)
        self.assertIn("--dev-bind", argv)
        self.assertNotIn(["--dev-bind", "/dev", "/dev"], [argv[i:i + 3] for i in range(len(argv) - 2)])
        self.assertNotIn("/sys", argv)

    def test_failed_base_merge_aborts_the_owned_conflicted_index(self):
        with tempfile.TemporaryDirectory(prefix="pr-watch-conflict-abort-") as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", "-b", "main"], cwd=root, check=True, capture_output=True)
            subprocess.run(["git", "config", "user.name", "Test"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.email", "test@example.com"], cwd=root, check=True)
            (root / "file.txt").write_text("base\n")
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "base"], cwd=root, check=True)
            subprocess.run(["git", "checkout", "-qb", "pr"], cwd=root, check=True)
            (root / "file.txt").write_text("pr\n")
            subprocess.run(["git", "commit", "-qam", "pr"], cwd=root, check=True)
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            subprocess.run(["git", "checkout", "-q", "main"], cwd=root, check=True)
            (root / "file.txt").write_text("main\n")
            subprocess.run(["git", "commit", "-qam", "main"], cwd=root, check=True)
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            subprocess.run(["git", "checkout", "-q", "pr"], cwd=root, check=True)
            current = pr(head=head, baseRefOid=base)
            with patch.object(pr_watch_repair, "fetch_current_main", return_value=base):
                with self.assertRaises(pr_watch_common.WatchError):
                    pr_watch_repair.merge_base_in_isolation(current, root, base)
            self.assertEqual(subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(), head)
            self.assertEqual(subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True), "")

    def test_repair_worker_refuses_without_bwrap(self):
        with patch.object(pr_watch_repair.shutil, "which", return_value=None):
            with self.assertRaisesRegex(pr_watch_common.WatchError, "bwrap"):
                pr_watch_repair.repair_worker_argv(["claude", "--version"], self.root)

    def test_dry_run_repair_failure_never_comments(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("failure")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("worker failed")), \
                patch.object(self.watcher, "comment") as comment:
            self.watcher.process("example/one", 7)
        comment.assert_not_called()
    def test_verification_does_not_import_pr_selector_or_receive_gh_credentials(self):
        with patch.object(pr_watch_verify, "checked", return_value="scripts/ci/paths.py\n"), \
                patch.object(pr_watch_verify, "load_paths") as selector, \
                patch.dict(os.environ, {"GH_TOKEN": "not-for-builds", "HOME": "/maintainer-home",
                                        "PR_WATCH_WEBHOOK_SECRET": "not-for-builds", "GRADLE_USER_HOME": "/trusted-gradle"}):
            os.environ.pop("GOFLAGS", None)  # the default below; patch.dict restores the caller's value
            selector.return_value.select.return_value = {"scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
            env = pr_watch_common.verification_env()
        self.assertEqual(selector.call_args.args[0], HERE.parent.parent)
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("PR_WATCH_WEBHOOK_SECRET", env)
        self.assertEqual(env["GRADLE_USER_HOME"], "/trusted-gradle")
        self.assertNotEqual(env["HOME"], "/maintainer-home")
        self.assertEqual(env["GOFLAGS"], "-gcflags=all=-dwarf=false -ldflags=-w -trimpath")

    def test_verification_keeps_explicit_go_flags(self):
        # -trimpath lets PR worktrees share compiled packages (#10614); an explicit setting of it, false too, stays.
        for explicit, expected in (("-mod=readonly", "-mod=readonly -trimpath"),
                                   ("-mod=readonly -trimpath", "-mod=readonly -trimpath"),
                                   ("-trimpath=false", "-trimpath=false"),
                                   ("--trimpath=true -mod=mod", "--trimpath=true -mod=mod")):
            with self.subTest(explicit=explicit), patch.dict(os.environ, {"GOFLAGS": explicit}):
                env = pr_watch_common.verification_env()
                self.assertEqual(env["GOFLAGS"], expected)

    def test_go_vet_clears_unused_linker_flag(self):
        names = ["server/internal/agents/cache_test.go"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test_file = root / names[0]
            test_file.parent.mkdir(parents=True)
            test_file.write_text("package agents\n")
            with patch.object(pr_watch_verify, "checked", return_value=names[0] + "\n"):
                commands = pr_watch_verify.persona_checks(str(root), pr(title="test: refine relevant tests"), 1, {})
        self.assertIn(("Changed Go packages vet", ["go", "-C", "server", "vet", "-ldflags=", "./internal/agents"]), commands)

    def test_semantic_only_skips_full_ci_and_discloses_it(self):
        with patch.object(pr_watch_verify, "persona_checks") as full, \
                patch.object(pr_watch_verify, "run_check", return_value={"name": "Exact-head privacy scan", "ok": True, "command": []}) as check:
            results = pr_watch_verify.verify("nwverse/persona", pr(), ".", {"profile": "semantic_only"})
        full.assert_not_called()
        self.assertEqual([result["name"] for result in results], ["Exact-head privacy scan"])
        self.assertIn("--range", check.call_args.args[1])
        watcher = pr_watch.Watcher(self.config, verbose=False)
        watcher.repositories["example/one"]["verify"]["profile"] = "semantic_only"
        with patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as run:
            watcher.success_review("example/one", 7, pr(), results)
        self.assertIn("no full build/vet", run.call_args.args[0][-1])
        self.assertNotIn("all tests passed", run.call_args.args[0][-1])

    def test_semantic_only_extensions_keep_repository_guards(self):
        with patch.object(pr_watch_verify, "extension_checks") as full, \
                patch.object(pr_watch_verify, "run_check", return_value={"name": "Privacy and documentation guards", "ok": True}) as check:
            results = pr_watch_verify.verify("nwverse/persona-extensions", pr(), ".", {"profile": "semantic_only"})
        full.assert_not_called()
        self.assertEqual(len(results), 1)
        argv = check.call_args.args[1]
        self.assertIn("--repository-guards", argv)
        self.assertEqual("nwverse/persona-extensions", argv[argv.index("--repository") + 1])
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_verify.verify("example/other", pr(), "/tmp/other-wt", {"profile": "semantic_only"})

    def test_verification_policy_version_covers_semantic_only(self):
        self.assertGreaterEqual(pr_watch.VERIFICATION_POLICY_VERSION, 3)

    def test_persona_wrapper_owns_lock_and_expects_reviewed_head(self):
        repo = "nwverse/persona"
        config = dict(self.config)
        config["repositories"] = [{"name": repo, "checkout": str(self.root / "checkout"), "base": "main",
                                  "verify": {}, "merge": True, "privacy": None, "notify_command": None}]
        watcher = pr_watch.Watcher(config, verbose=False)
        with patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as run, \
                patch.object(pr_watch, "require_local_gate") as local_gate, \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(state="MERGED")):
            watcher.merge(config["repositories"][0], pr())
        local_gate.assert_called_once_with(repo, SHA_A, SHA_BASE)
        argv = run.call_args.args[0]
        self.assertEqual(argv[2:6], ["--expect-head", SHA_A, "--expect-base", SHA_BASE])
        self.assertEqual(argv[6], "7")

    def test_new_sha_reviews_again(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(), pr(SHA_B), pr(SHA_B)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)

    def test_repair_infrastructure_failure_retries_without_consuming_round(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(title="feat: an example")), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.VerificationError("real test failed")), \
                patch.object(self.watcher, "repair_verification", side_effect=pr_watch_common.WatchError("worker unavailable")):
            self.watcher.process("example/one", 7)
        state = self.watcher.state.get("example/one", 7)
        self.assertEqual(state["outcome"], "retry")
        self.assertEqual(state.get("repair_rounds", 0), 0)
        self.assertEqual(len(self.comments), 1)
        self.assertIn("worker unavailable", self.comments[0])

    def test_terminal_infrastructure_failure_is_published_once_and_retried_later(self):
        clock = [1000.0]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.WatchError("infrastructure unavailable")) as verify, \
                patch.object(pr_watch.time, "time", side_effect=lambda: clock[0]):
            for now in (1000.0, 1301.0, 1602.0):
                clock[0] = now
                self.watcher.process("example/one", 7)
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
            self.assertEqual(len(self.comments), 1)

            for now in (1903.0, 1904.0):
                clock[0] = now
                self.watcher.process("example/one", 7)

        terminal = [body for body in self.comments if "could not complete" in body]
        self.assertEqual(1, len(terminal), "an identical infrastructure failure was posted again")
        self.assertEqual(len(self.comments), 1)
        self.assertEqual(self.watcher.state.get("example/one", 7)["failure"], "infrastructure unavailable")
        self.assertFalse(self.watcher.state.get("example/one", 7).get("failure_posted", False))
        self.assertEqual(verify.call_count, 3)
        self.assertEqual(review.call_count, 1)

    def test_semantic_failure_retries_with_backoff_without_comment_spam(self):
        for attempt in range(3):
            with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                    patch.object(pr_watch, "semantic_review", side_effect=pr_watch_common.WatchError("invalid output")), \
                    patch.object(self.watcher, "merge") as merge:
                self.watcher.process("example/one", 7)
            merge.assert_not_called()
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
            self.assertEqual(self.watcher.state.get("example/one", 7)["review_attempts"], 3)
            self.assertEqual(self.watcher.state.get("example/one", 7)["failure"], "invalid output")
            self.watcher.state.data["prs"]["example/one#7"]["retry_after"] = 0
        self.assertEqual(self.comments, [])

    def test_conflicting_pr_enters_bounded_base_repair(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", return_value=False) as update, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        update.assert_called_once()
        with patch.object(pr_watch, "gh_json", return_value=[{"number": 7, "headRefOid": SHA_A, "isDraft": False,
                                                               "baseRefName": "main", "mergeable": "CONFLICTING"}]), \
                patch.object(self.watcher, "enqueue") as enqueue:
            self.watcher.discover()
        enqueue.assert_called_once()

    def test_conflict_repair_consumes_one_round_and_verifies_before_push(self):
        calls = []
        def update(repo, current, worktree, base, reviewer, before_conflict):
            self.assertEqual(reviewer, self.config["reviewer"])
            before_conflict()
            return SHA_B
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", side_effect=update), \
                patch.object(pr_watch_repair, "merged_main", return_value=SHA_BASE), \
                patch.object(pr_watch, "verify", side_effect=lambda *args, **kwargs: calls.append("verify") or []), \
                patch.object(pr_watch, "checked", side_effect=lambda *args, **kwargs: calls.append("privacy") or ""), \
                patch.object(pr_watch_repair, "push_updated_head", side_effect=lambda *args: calls.append("push")), \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        self.assertEqual(calls, ["verify", "privacy", "push"])
        merge.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 1)
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)

    def test_failed_conflict_attempt_still_consumes_its_round(self):
        def update(repo, current, worktree, base, reviewer, before_conflict):
            before_conflict()
            raise pr_watch_common.WatchError("worker could not resolve the conflict")
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", side_effect=update), \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        merge.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["repair_rounds"], 1)

    def test_clean_merge_does_not_consume_a_conflict_round(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch_repair, "update_branch_with_main", return_value=False) as update, \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        update.assert_called_once()
        self.assertEqual(self.watcher.state.get("example/one", 7).get("repair_rounds", 0), 0)

    def test_conflict_resolution_requires_a_successful_privacy_scan_before_commit(self):
        calls = []
        def checked(argv, **kwargs):
            calls.append(argv)
            if argv[:2] == ["git", "rev-parse"]:
                if argv[2] == "MERGE_HEAD":
                    return SHA_BASE + "\n"
                if argv[2] == "HEAD":
                    return SHA_A + "\n"
                return SHA_B + "\n"
            if argv[:3] == ["git", "diff", "--name-only"] and "--diff-filter=U" in argv:
                return "conflicted.txt\0" if len([x for x in calls if x[:2] == ["git", "write-tree"]]) == 0 else ""
            return ""
        with patch.object(pr_watch_repair, "repair_repository_copy", return_value=nullcontext(self.root)), \
                patch.object(pr_watch_repair, "run", return_value=subprocess.CompletedProcess([], 1, "", "conflict")), \
                patch.object(pr_watch_repair, "repair_worker_argv", return_value=["worker"]), \
                patch.object(pr_watch_repair, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch_repair, "index_entries", return_value=[]), \
                patch.object(pr_watch_repair, "copy_conflict_resolution"), \
                patch.object(pr_watch_repair, "checked", side_effect=checked):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_repair.repair_merge_conflict("example/one", pr(), self.root, SHA_BASE, {})
        self.assertFalse(any("commit" in argv for argv in calls if argv and argv[0] == "git"))

    def test_failure_summary_retains_actual_errors(self):
        result = subprocess.CompletedProcess([], 1, "PASS earlier lane\n" + "\n".join("ordinary" for _ in range(200)) +
                                               "\n--- FAIL: TestRealDefect\npanic: actual cause\n", "")
        summary = pr_watch_verify.result_summary(result)
        self.assertIn("--- FAIL: TestRealDefect", summary)
        self.assertIn("panic: actual cause", summary)

    def test_failure_summary_keeps_unknown_diagnostics_and_final_test_names(self):
        plain = subprocess.CompletedProcess([], 1, "slot order: generated row is absent from the baseline", "")
        self.assertIn("absent from the baseline", pr_watch_verify.result_summary(plain))
        passing = "\n".join("PASS: handles no error and client errors %d" % n for n in range(100))
        failed = passing + "\nFailed tests:\n--- FAIL: TestActualAssertion\n    got 1, want 2\n"
        summary = pr_watch_verify.result_summary(subprocess.CompletedProcess([], 1, failed, ""))
        self.assertIn("TestActualAssertion", summary)
        self.assertIn("got 1, want 2", summary)
        self.assertNotIn("handles no error and client errors 0", summary)
        python_failure = "Traceback (most recent call last):\n  File \"test_x.py\", line 2\n    self.assertEqual(1, 2)\nAssertionError: 1 != 2\nFAILED (failures=1)"
        summary = pr_watch_verify.result_summary(subprocess.CompletedProcess([], 1, python_failure, ""))
        self.assertIn("AssertionError: 1 != 2", summary)
        other = subprocess.CompletedProcess([], 1, "output before\nError: compiler rejected input\n1 tests failed.", "")
        summary = pr_watch_verify.result_summary(other)
        self.assertIn("compiler rejected input", summary)

    def test_unknown_mergeability_waits(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="UNKNOWN")), \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()

    def test_moved_head_discards_passed_checks(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(SHA_B)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        merge.assert_not_called()

    def test_another_persons_review_never_skips_verification(self):
        # The service reads only its own versioned state, not an arbitrary GitHub review.
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(reviewDecision="COMMENTED")), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()

    def test_inline_review_uses_stdin_payload_and_falls_back(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P0", "summary": "wrong", "suggestion": "fix"}]
        requests = []
        def fake_run(argv, **kwargs):
            requests.append((argv, kwargs))
            if argv[:2] == ["gh", "api"]:
                return subprocess.CompletedProcess(argv, 422, "", "bad anchor")
            return subprocess.CompletedProcess(argv, 0, "{}", "")
        with patch.object(pr_watch, "run", side_effect=fake_run):
            self.watcher.comment_findings("example/one", 7, pr(), findings)
        payload = json.loads(requests[0][1]["input_text"])
        self.assertEqual(payload["comments"][0]["line"], 2)
        self.assertEqual(payload["commit_id"], SHA_A)
        self.assertEqual(requests[1][0][:2], ["gh", "pr"])

    def test_lock_excludes_a_second_process(self):
        first = pr_watch.Watcher(self.config, verbose=False)
        second = pr_watch.Watcher(self.config, verbose=False)
        first.single_instance()
        self.addCleanup(first.instance.close)
        with self.assertRaises(pr_watch_common.WatchError):
            second.single_instance()
        second.instance.close()

    def test_superseded_verification_dequeues_before_fresh_review(self):
        self.watcher.state.put("example/one", 8, SHA_B,
                               evidence_key="example/one#8@" + SHA_BASE + ".." + SHA_B,
                               base=SHA_BASE, format=1, outcome="failed", semantic="passed")
        self.watcher.enqueue("example/one", 7, SHA_A, "poll")
        self.watcher.enqueue("example/one", 8, SHA_B, "poll")
        self.assertEqual(self.watcher.tasks.get()[2], ("example/one", 8))
        self.watcher.tasks.task_done()
        self.assertEqual(self.watcher.tasks.get()[2], ("example/one", 7))
        self.watcher.tasks.task_done()

    def test_enqueue_marks_active_and_queues_under_one_lock(self):
        calls = []
        class RecordingLock:
            def __init__(self):
                self.depth = 0
            def __enter__(self):
                self.depth += 1
                calls.append("enter")
            def __exit__(self, *args):
                calls.append("exit")
                self.depth -= 1
        lock = RecordingLock()
        self.watcher.queue_lock = lock
        self.watcher.queue_priority = lambda *args: calls.append("priority") or 0
        self.watcher.tasks.put = lambda item: calls.append(("put", item))
        self.watcher.enqueue("example/one", 7, SHA_A, "poll")
        self.assertEqual(calls, ["enter", "priority", ("put", (0, 0, ("example/one", 7))), "exit"])
        self.assertEqual(lock.depth, 0)

    def test_priority_queue_accepts_shutdown_sentinel_after_queued_work(self):
        self.watcher.enqueue("example/one", 7, SHA_A, "poll")
        self.watcher.tasks.put((2, -1, ("", 0)))
        self.watcher.tasks.put((2, -1, ("", 0)))
        workers = [threading.Thread(target=self.watcher.worker) for _ in range(2)]
        with patch.object(self.watcher, "process") as process:
            for worker in workers:
                worker.start()
            for worker in workers:
                worker.join(timeout=5)
                self.assertFalse(worker.is_alive())
        process.assert_called_once_with("example/one", 7)
        self.assertTrue(self.watcher.tasks.empty())

    def test_findings_and_exhausted_repairs_do_not_get_migration_priority(self):
        for outcome, fields in (("findings", {"semantic": "findings"}),
                                ("repair_exhausted", {"repair_exhausted": True})):
            self.watcher.state.put("example/one", 8, SHA_B, outcome=outcome, format=1, **fields)
            self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_B), 1)

    def test_queue_priority_requires_exact_migratable_head(self):
        evidence_key = "example/one#8@" + SHA_BASE + ".." + SHA_B
        self.watcher.state.put("example/one", 8, SHA_B, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="failed")
        self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_B), 0)
        self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_A), 1)
        for fields in ({"retry_after": 10**12}, {"outcome": "merge_failed"},
                       {"outcome": "merge_failed", "blocking": False}):
            with self.subTest(fields=fields):
                self.watcher.state.data["prs"]["example/one#8"].update(fields)
                self.assertEqual(self.watcher.queue_priority("example/one", 8, SHA_B), 1)
                self.watcher.state.data["prs"]["example/one#8"].update(
                    {"retry_after": 0, "outcome": "failed", "blocking": False})

    def test_portable_two_repository_example_has_no_private_configuration(self):
        path = HERE / "pr-watch.persona.example.json"
        config = pr_watch.load_config(path)
        self.assertEqual(config["workers"], 10)
        self.assertEqual(config["reviewer"]["timeout_seconds"], 3600)
        self.assertEqual([item["name"] for item in config["repositories"]],
                         ["nwverse/persona", "nwverse/persona-extensions"])
        self.assertEqual([item["verify"]["profile"] for item in config["repositories"]],
                         ["persona", "extensions"])
        self.assertTrue(all(item["merge"] is False for item in config["repositories"]))
        text = path.read_text()
        self.assertNotIn("/home/", text)
        self.assertNotIn("/data00/", text)
        self.assertNotIn("token", text.lower())
        service = (HERE / "persona-pr-watch.example.service").read_text()
        self.assertIn("EnvironmentFile=-%h/.config/persona/pr-watch.env", service)
        self.assertNotIn("Environment=ANTHROPIC", service)
        self.assertNotIn("/home/", service)

    def test_configured_workers_start_and_stop_cleanly(self):
        for count in (10, 20):
            with self.subTest(workers=count):
                self.config["workers"] = count
                watcher = pr_watch.Watcher(self.config, verbose=False)
                watcher.start_workers()
                self.addCleanup(watcher.stopping.set)
                self.assertEqual(len(watcher.workers), count)
                for _ in watcher.workers:
                    watcher.tasks.put((2, -1, ("", 0)))
                for worker in watcher.workers:
                    worker.join(timeout=5)
                    self.assertFalse(worker.is_alive())
                self.addCleanup(watcher.review_executor.shutdown, wait=True, cancel_futures=True)
                self.assertEqual(watcher.review_executor._max_workers, 16)

    def test_reviewer_concurrency_is_bounded_and_refills_ready_work(self):
        self.config["reviewer_concurrency"] = 16
        watcher = pr_watch.Watcher(self.config, verbose=False)
        watcher.start_workers()
        self.addCleanup(watcher.stopping.set)
        self.addCleanup(watcher.review_executor.shutdown, wait=True, cancel_futures=True)
        for _ in watcher.workers:
            watcher.tasks.put((2, -1, ("", 0)))
        for worker in watcher.workers:
            worker.join(timeout=5)
        active = 0
        peak = 0
        completed = []
        lock = threading.Lock()
        gate = threading.Event()
        release = threading.Event()

        def review(repo, current, worktree, config):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
                if active == 16:
                    gate.set()
            release.wait(timeout=5)
            with lock:
                active -= 1
                completed.append(current["number"])
            return []

        with patch.object(pr_watch, "semantic_review", side_effect=review):
            futures = [watcher.review_executor.submit(
                pr_watch.semantic_review, "example/one", {"number": number}, ".", self.config["reviewer"])
                for number in range(18)]
            self.assertTrue(gate.wait(timeout=5), "the first sixteen reviewers were not filled")
            release.set()
            for future in futures:
                future.result(timeout=5)
        self.assertEqual(peak, 16)
        self.assertEqual(len(completed), 18)
        for count in (0, 21):
            self.config["reviewer_concurrency"] = count
            with self.assertRaisesRegex(pr_watch_common.WatchError, "reviewer concurrency"):
                pr_watch.Watcher(self.config, verbose=False).start_workers()

    def test_discovery_rate_limit_retains_the_resident_service(self):
        watcher = pr_watch.Watcher(self.config, verbose=False)
        with patch.object(pr_watch, "gh_json", side_effect=pr_watch_common.WatchError("API rate limit already exceeded")):
            watcher.discover()
        self.assertIsNone(watcher.fatal)
        self.assertFalse(watcher.stopping.is_set())

    def test_worker_count_is_bounded(self):
        for count in (-1, 0, 21):
            with self.subTest(workers=count):
                self.config["workers"] = count
                watcher = pr_watch.Watcher(self.config, verbose=False)
                with self.assertRaisesRegex(pr_watch_common.WatchError,
                                            "workers must be between 1 and 20"):
                    watcher.start_workers()
                self.assertEqual(watcher.workers, [])

    def test_reviewer_default_does_not_change_the_evidence_fingerprint(self):
        path = self.root / "config.json"
        raw = {"repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {}}],
               "reviewer": {}, "workers": 20, "poll_seconds": 60, "state": str(self.root / "state.json")}
        path.write_text(json.dumps(raw))
        baseline = pr_watch.fingerprint("configuration", 0, "0" * 40, "config", pr_watch.load_config(path))
        raw["reviewer"]["concurrency"] = 16
        path.write_text(json.dumps(raw))
        configured = pr_watch.load_config(path)
        self.assertNotIn("concurrency", configured["reviewer"])
        self.assertEqual(configured["reviewer_concurrency"], 16)
        self.assertEqual(pr_watch.fingerprint("configuration", 0, "0" * 40, "config", configured), baseline)

    def test_config_has_no_implicit_merge_or_repositories(self):
        path = self.root / "config.json"
        path.write_text(json.dumps({"repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {}}]}))
        config = pr_watch.load_config(path)
        self.assertFalse(config["repositories"][0]["merge"])
        self.assertFalse(config["repositories"][0]["close_stale"])
        self.assertEqual(len(config["repositories"]), 1)

    def enable_human_reviews(self):
        self.config["repositories"][0]["respect_human_reviews"] = True
        self.watcher.repositories["example/one"]["respect_human_reviews"] = True

    def human_decision(self, kind, review_id=101, login="human-reviewer", head=SHA_A):
        if kind == "none":
            return {"decision": "none"}
        states = {"approved": "APPROVED", "changes_requested": "CHANGES_REQUESTED", "commented": "COMMENTED"}
        return {"decision": kind,
                "review": {"id": review_id, "login": login, "state": states[kind],
                           "commit_id": head, "submitted_at": "2026-11-20T10:00:00Z"}}

    def test_respect_human_reviews_defaults_off_and_rejects_non_boolean(self):
        path = self.root / "hr-config.json"
        path.write_text(json.dumps({"repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {}}],
                                    "state": str(self.root / "hr-state.json")}))
        self.assertFalse(pr_watch.load_config(path)["repositories"][0]["respect_human_reviews"])
        raw = json.loads(path.read_text())
        raw["repositories"][0]["respect_human_reviews"] = "yes"
        path.write_text(json.dumps(raw))
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch.load_config(path)

    def test_issue_link_requirement_defaults_on_and_rejects_non_boolean(self):
        """A repository opts out of the linked-issue gate explicitly; the default keeps it (#16751)."""
        path = self.root / "il-config.json"
        path.write_text(json.dumps({"repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {}}],
                                    "state": str(self.root / "il-state.json")}))
        loaded = pr_watch.load_config(path)
        self.assertTrue(loaded["repositories"][0]["require_issue_link"])
        # A merge-time gate only: setting it must not reset exact-head review or verification evidence.
        self.assertNotIn("require_issue_link", loaded["repository_policy"][0])
        raw = json.loads(path.read_text())
        raw["repositories"][0]["require_issue_link"] = False
        path.write_text(json.dumps(raw))
        self.assertFalse(pr_watch.load_config(path)["repositories"][0]["require_issue_link"])
        raw["repositories"][0]["require_issue_link"] = "no"
        path.write_text(json.dumps(raw))
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch.load_config(path)

    def test_without_the_flag_human_reviews_are_never_read(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision") as human, \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]), \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
        human.assert_not_called()
        review.assert_called_once()

    def test_human_approval_skips_the_reviewer_and_all_publication_but_keeps_every_gate(self):
        self.enable_human_reviews()
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision",
                             return_value=self.human_decision("approved")) as human, \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]) as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        human.assert_called_once_with("example/one", 7, SHA_A)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_called_once()
        self.github.assert_not_called()
        self.assertEqual(self.comments, [])
        record = self.watcher.state.get("example/one", 7)
        self.assertEqual(record["semantic"], "passed")
        self.assertEqual(record["semantic_source"], "human")
        self.assertEqual(record["human_review"]["id"], 101)
        self.assertEqual(record["human_review"]["commit_id"], SHA_A)
        self.assertEqual(record["outcome"], "merged")

    def test_human_change_request_parks_then_a_same_head_approval_resolves_it(self):
        self.enable_human_reviews()
        decisions = [self.human_decision("changes_requested", review_id=202),
                     self.human_decision("approved", review_id=203)]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision", side_effect=decisions), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]) as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
            parked = self.watcher.state.get("example/one", 7)
            self.assertEqual(parked["outcome"], "human_changes_requested")
            self.assertEqual(parked["human_review"]["id"], 202)
            verify.assert_not_called()
            merge.assert_not_called()
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_called_once()
        resolved = self.watcher.state.get("example/one", 7)
        self.assertEqual(resolved["outcome"], "merged")
        self.assertEqual(resolved["human_review"]["id"], 203)

    def test_same_head_comment_after_human_approval_parks_instead_of_using_stored_pass(self):
        self.enable_human_reviews()
        decisions = [self.human_decision("approved", review_id=301),
                     self.human_decision("commented", review_id=302)]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision", side_effect=decisions) as human, \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]) as verify, \
                patch.object(self.watcher, "merge", return_value=False) as merge:
            self.watcher.process("example/one", 7)
            approved = self.watcher.state.get("example/one", 7)
            self.assertEqual(approved["outcome"], "passed")
            self.assertEqual(approved["semantic_source"], "human")
            self.watcher.process("example/one", 7)
        self.assertEqual(human.call_count, 2)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_called_once()
        parked = self.watcher.state.get("example/one", 7)
        self.assertEqual(parked["outcome"], "human_commented")
        self.assertEqual(parked["human_review"]["id"], 302)

    def test_same_head_dismissal_after_human_approval_restores_normal_ai_review(self):
        self.enable_human_reviews()
        decisions = [self.human_decision("approved", review_id=303),
                     self.human_decision("none")]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision", side_effect=decisions) as human, \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.assertEqual(self.watcher.state.get("example/one", 7)["semantic_source"], "human")
            self.watcher.process("example/one", 7)
        self.assertEqual(human.call_count, 2)
        review.assert_called_once()
        record = self.watcher.state.get("example/one", 7)
        self.assertNotIn("semantic_source", record)
        self.assertEqual(record["semantic"], "passed")

    def test_restart_with_human_semantic_evidence_resumes_verification_and_merge(self):
        self.enable_human_reviews()
        decision = self.human_decision("approved", review_id=304)
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="reviewed",
                               semantic="passed", findings=[], semantic_source="human",
                               human_review=decision["review"], review_attempts=0,
                               review_posted=True, success_posted=True)
        resumed = pr_watch.Watcher(self.config, verbose=False)
        resumed.remove_worktree = lambda *args: None
        resumed.worktree = lambda *args: self.root / "checkout"
        resumed.comment = lambda repo, number, body: self.comments.append(body)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(resumed, "human_review_decision", return_value=decision), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]) as verify, \
                patch.object(resumed, "merge", return_value=True) as merge:
            resumed.process("example/one", 7)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_called_once()
        self.github.assert_not_called()
        self.assertEqual(self.comments, [])
        record = resumed.state.get("example/one", 7)
        self.assertEqual(record["verification"], "passed")
        self.assertEqual(record["semantic_source"], "human")
        self.assertEqual(record["outcome"], "merged")

    def test_same_head_comment_after_merge_failure_parks_instead_of_retrying_merge(self):
        self.enable_human_reviews()
        approval = self.human_decision("approved", review_id=305)
        comment = self.human_decision("commented", review_id=306)
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="merge_failed",
                               semantic="passed", findings=[], semantic_source="human",
                               human_review=approval["review"], verification="passed",
                               verification_policy=pr_watch.VERIFICATION_POLICY_VERSION,
                               checks=[{"name": "privacy", "ok": True}], review_posted=True,
                               success_posted=True, retry_after=0, blocking=False)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision", return_value=comment) as human, \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        human.assert_called_once_with("example/one", 7, SHA_A)
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_not_called()
        record = self.watcher.state.get("example/one", 7)
        self.assertEqual(record["outcome"], "human_commented")
        self.assertEqual(record["human_review"]["id"], 306)

    def test_human_review_read_failure_during_merge_retry_stays_retryable(self):
        self.enable_human_reviews()
        approval = self.human_decision("approved", review_id=308)
        comment = self.human_decision("commented", review_id=309)
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="merge_failed",
                               semantic="passed", findings=[], semantic_source="human",
                               human_review=approval["review"], verification="passed",
                               verification_policy=pr_watch.VERIFICATION_POLICY_VERSION,
                               checks=[{"name": "privacy", "ok": True}], review_posted=True,
                               success_posted=True, retry_after=0, blocking=False)
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision",
                             side_effect=[pr_watch_common.WatchError("reviews unavailable"), comment]) as human, \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
            self.watcher.state.annotate("example/one", 7, retry_after=0)
            self.watcher.process("example/one", 7)
        self.assertEqual(human.call_count, 2)
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_not_called()
        record = self.watcher.state.get("example/one", 7)
        self.assertEqual(record["outcome"], "human_commented")
        self.assertEqual(record["human_review"]["id"], 309)

    def test_failed_or_repairing_human_evidence_does_not_restart_verification(self):
        self.enable_human_reviews()
        decision = self.human_decision("approved", review_id=307)
        for number, outcome in ((7, "failed"), (8, "repairing")):
            with self.subTest(outcome=outcome):
                evidence_key = "example/one#" + str(number) + "@" + SHA_BASE + ".." + SHA_A
                self.watcher.state.put("example/one", number, SHA_A, evidence_key=evidence_key,
                                       base=SHA_BASE, format=1, outcome=outcome,
                                       semantic="passed", findings=[], semantic_source="human",
                                       human_review=decision["review"], review_posted=True,
                                       verification_policy=pr_watch.VERIFICATION_POLICY_VERSION,
                                       success_posted=True, retry_after=0)
                with patch.object(pr_watch, "pr_snapshot", return_value=pr(number=number)), \
                        patch.object(self.watcher, "human_review_decision", return_value=decision) as human, \
                        patch.object(pr_watch, "verify", return_value=[]) as verify, \
                        patch.object(self.watcher, "merge", return_value=False) as merge:
                    self.watcher.process("example/one", number)
                human.assert_not_called()
                verify.assert_not_called()
                merge.assert_not_called()
                self.assertEqual(self.watcher.state.get("example/one", number)["outcome"], outcome)

    def test_human_comment_only_parks_without_approval_or_merge(self):
        self.enable_human_reviews()
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision",
                             return_value=self.human_decision("commented")), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
            parked = self.watcher.state.get("example/one", 7)
            writes = []
            original_put = self.watcher.state.put
            def counting_put(*args, **kwargs):
                writes.append(args)
                return original_put(*args, **kwargs)
            with patch.object(self.watcher.state, "put", side_effect=counting_put):
                self.watcher.process("example/one", 7)
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_not_called()
        self.assertEqual(self.comments, [])
        record = self.watcher.state.get("example/one", 7)
        self.assertEqual(record["outcome"], "human_commented")
        self.assertEqual(record["human_review"]["id"], parked["human_review"]["id"])
        self.assertEqual(writes, [])

    def test_head_without_a_human_review_gets_the_normal_ai_review(self):
        self.enable_human_reviews()
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision",
                             return_value=self.human_decision("none")) as human, \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]), \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
        human.assert_called_once()
        review.assert_called_once()
        self.assertNotIn("semantic_source", self.watcher.state.get("example/one", 7))

    def test_new_head_after_a_park_is_re_evaluated_and_reviewed_fresh(self):
        self.enable_human_reviews()
        decisions = [self.human_decision("changes_requested", head=SHA_A),
                     self.human_decision("none", head=SHA_B)]
        with patch.object(pr_watch, "pr_snapshot",
                          side_effect=[pr(head=SHA_A)] + [pr(head=SHA_B)] * 5), \
                patch.object(self.watcher, "human_review_decision", side_effect=decisions) as human, \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "privacy", "ok": True}]), \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "human_changes_requested")
            self.watcher.process("example/one", 7)
        self.assertEqual(human.call_args_list[1].args[2], SHA_B)
        self.assertEqual(review.call_args.args[1]["headRefOid"], SHA_B)
        record = self.watcher.state.get("example/one", 7)
        self.assertEqual(record["head"], SHA_B)
        self.assertNotIn("human_review", record)

    def test_human_approval_does_not_skip_the_verification_gate(self):
        self.enable_human_reviews()
        dry = pr_watch.Watcher(self.config, dry_run=True, verbose=False)
        dry.remove_worktree = lambda *args: None
        dry.worktree = lambda *args: self.root / "checkout"
        dry.comment = lambda *args: None
        dry.recover_infrastructure = lambda *args: None
        failure = pr_watch_common.VerificationError("privacy scan failed", checks=[])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(dry, "human_review_decision", return_value=self.human_decision("approved")), \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify", side_effect=failure) as verify, \
                patch.object(dry, "merge") as merge:
            dry.process("example/one", 7)
        review.assert_not_called()
        verify.assert_called_once()
        merge.assert_not_called()
        record = dry.state.get("example/one", 7)
        self.assertEqual(record["semantic"], "passed")
        self.assertNotIn(record.get("verification"), ("passed", "base_failure"))
        self.assertEqual(record["outcome"], "retry")

    def test_completed_watcher_evidence_is_not_replaced_by_human_reviews(self):
        self.enable_human_reviews()
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        self.watcher.state.put("example/one", 7, SHA_A, evidence_key=evidence_key,
                               base=SHA_BASE, format=1, outcome="passed_with_findings",
                               semantic="passed", findings=[], review_posted=True,
                               success_posted=True, verification="passed",
                               checks=[{"name": "privacy", "ok": True}])
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(self.watcher, "human_review_decision") as human, \
                patch.object(pr_watch, "semantic_review") as review, \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        human.assert_not_called()
        review.assert_not_called()
        verify.assert_not_called()
        merge.assert_called_once()
        self.github.assert_not_called()
        self.assertEqual(self.comments, [])

    def test_human_flag_is_outside_the_policy_fingerprint_but_moves_the_config_digest(self):
        def write_config(flag):
            path = self.root / ("policy-%s.json" % flag)
            path.write_text(json.dumps({
                "repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {},
                                  "respect_human_reviews": flag}],
                "state": str(self.root / ("policy-state-%s.json" % flag))}))
            return pr_watch.load_config(path)
        off, on = write_config(False), write_config(True)
        policy = lambda config: pr_watch.fingerprint("repository-policy", 0, "0" * 40, "config",
                                                     config["repository_policy"])
        digest = lambda config: pr_watch.fingerprint("configuration", 0, "0" * 40, "config", config)
        self.assertEqual(policy(off), policy(on))
        self.assertNotEqual(digest(off), digest(on))

    def test_toggling_the_human_flag_migrates_without_clearing_exact_head_evidence(self):
        def loaded_config(flag):
            path = self.root / ("migration-%s.json" % flag)
            path.write_text(json.dumps({
                "repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                                  "verify": {"profile": "commands", "commands": [["true"]]},
                                  "merge": True, "respect_human_reviews": flag}],
                "state": str(self.root / "migration-state.json")}))
            return pr_watch.load_config(path)
        first = pr_watch.Watcher(loaded_config(False), verbose=False)
        first.single_instance()
        first.instance.close()
        evidence_key = "example/one#7@" + SHA_BASE + ".." + SHA_A
        first.state.put("example/one", 7, SHA_A, evidence_key=evidence_key, base=SHA_BASE, format=1,
                        outcome="passed", semantic="passed", verification="passed",
                        review_posted=True, success_posted=True, findings=[])
        migrated = pr_watch.Watcher(loaded_config(True), allow_config_migration=True, verbose=False)
        migrated.single_instance()
        migrated.instance.close()
        record = pr_watch.State(str(self.root / "migration-state.json")).data["prs"]["example/one#7"]
        self.assertEqual(record["semantic"], "passed")
        self.assertEqual(record["verification"], "passed")
        self.assertEqual(record["outcome"], "passed")
        self.assertTrue(record["review_posted"])


class ReviewTests(unittest.TestCase):
    def test_schema_and_changed_line_validation(self):
        envelope = {"subtype": "success", "is_error": False, "permission_denials": [],
                    "structured_output": {"findings": [{"path": "a.py", "line": 2, "severity": "P1",
                                                        "summary": "wrong", "suggestion": "fix"}]}}
        self.assertEqual(len(pr_watch_review.parse_result(json.dumps(envelope), {"a.py": {2}})), 1)
        for anchors in ({"a.py": {1}}, {"other.py": {2}}):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.parse_result(json.dumps(envelope), anchors)
        envelope["permission_denials"] = [{"tool": "Bash"}]
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_review.parse_result(json.dumps(envelope), {"a.py": {2}})
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_review.parse_result("not json", {"a.py": {2}})

    def test_diff_anchors_include_only_added_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                subprocess.run(["git"] + list(args), cwd=root, env=env, check=True, capture_output=True)
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("one\ntwo\n")
            git("add", "a.py")
            git("commit", "-qm", "base")
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "a.py").write_text("one\nchanged\nthree\n")
            git("commit", "-qam", "head")
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            self.assertEqual(pr_watch_review.changed_lines(root, base, head), {"a.py": {2, 3}})


class EventTests(unittest.TestCase):
    def test_webhook_authentication_repository_and_callback(self):
        secret = "x" * 32
        body = json.dumps({"action": "synchronize", "repository": {"full_name": "example/one"},
                           "pull_request": {"number": 7, "draft": False, "head": {"sha": SHA_A}}}).encode()
        signature = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
        event = pr_watch_events.parse_pull_request(body, signature, "pull_request", secret, {"example/one"})
        self.assertEqual(event, ("example/one", 7, SHA_A))
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_events.parse_pull_request(body, "sha256=bad", "pull_request", secret, {"example/one"})
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_events.parse_pull_request(body, signature, "pull_request", secret, {"example/other"})


class VerifyTests(unittest.TestCase):
    def test_apple_change_does_not_require_macos_verifier(self):
        path = "apple/Persona/App/View.swift"
        with patch.object(pr_watch_verify, "checked", side_effect=[path + "\n", "+import SwiftUI"]), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat: an example"), 1,
                                                       {"ui_evidence_command": ["visual", "{worktree}"]})
        self.assertIn(("Apple semantic and portable verification policy", ["true"]), commands)
        self.assertIn(("visual evidence", ["visual", "."]), commands)
        self.assertFalse(any(command[0] == "trusted Apple verification" for command in commands))

    def test_non_ui_apple_paths_are_explicit_and_diff_is_checked(self):
        path = "apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift"
        with patch.object(pr_watch_verify, "checked", return_value="+chunks.removeFirst()\n"):
            self.assertTrue(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, [path]))
        for changed in ("+import SwiftUI\n+struct ChunkView: View {}", "+Text(localized: \"Ready\")",
                        "+NavigationStack { Content() }", "+.sheet(isPresented: $shown) { Content() }",
                        "+.padding(8)", "+Image(systemName: \"checkmark\")"):
            with patch.object(pr_watch_verify, "checked", return_value=changed):
                self.assertFalse(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, [path]))
        for changed in ("+import SwiftUI", "+NavigationStack { Content() }"):
            with patch.object(pr_watch_verify, "checked", return_value=changed):
                self.assertFalse(pr_watch_verify.non_ui_apple_change(
                    ".", SHA_BASE, SHA_A, ["apple/Persona/Views/Session/FilesPane.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+.padding(8)"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/PersonaUITests/MinutesTests.swift", "apple/Persona/Views/Session/FilesPane.swift"]))
        for names in ([path, "web/src/views/ChatView.tsx"],
                      ["apple/Shared/Localization/Localizable.xcstrings"]):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, names))
        with patch.object(pr_watch_verify, "checked", return_value="+import SwiftUI"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/App/TestsSupport/Feature.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+Text(ready)"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/Recording/RecordingMinutesView.swift"]))

        self.assertTrue(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["apple/PersonaUITests/MinutesTests.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+chunks.removeFirst()\n"):
            self.assertTrue(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/App/TasksStore.swift"]))
        self.assertTrue(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-runtime/src/lib.rs",
                                    "android/core/rustcore/src/main/kotlin/persona/core/rustcore/Codec.kt",
                                    "schema/extension/manifest.json"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-ffi/src/lib.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-typegen/src/main.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-runtime/src/wire.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["web/src/views/ChatView.tsx"]))

    def test_eligible_non_ui_apple_change_runs_remaining_lanes(self):
        names = "apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "non_ui_apple_change", return_value=True) as classifier, \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True, "server": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="chore: an example"), 1, {})
        self.assertEqual(commands, [("CI server", ["bash", "scripts/ci/server.sh"]),
                                    ("Exact-head privacy scan", commands[1][1]),
                                    ("Apple semantic and portable verification policy", ["true"])])
        classifier.assert_called_once()

    def test_shared_core_lane_is_not_dropped(self):
        names = "native/app-core/crates/persona-core-runtime/src/lib.rs\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "non_ui_apple_change", return_value=True), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "core": True, "android": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
        self.assertEqual(commands[:2], [("CI android", ["bash", "scripts/ci/android.sh"]),
                                    ("CI core", ["bash", "scripts/ci/core.sh"])])
        self.assertEqual(commands[-1], ("Apple semantic and portable verification policy", ["true"]))

    def test_apple_ffi_keeps_semantic_and_portable_checks_without_macos(self):
        names = "native/app-core/crates/persona-core-ffi/src/lib.rs\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "core": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(title="feat: an example"), 1, {})
        self.assertIn(("CI core", ["bash", "scripts/ci/core.sh"]), commands)
        self.assertIn(("Apple semantic and portable verification policy", ["true"]), commands)

    def test_extension_shared_changes_use_full_ci(self):
        with patch.object(pr_watch_verify, "extension_modules", return_value=None), \
                patch.object(pr_watch_verify, "checked", return_value="scripts/ci.sh\n"):
            self.assertEqual(pr_watch_verify.extension_checks(".", pr(), 1),
                             [("extension repository CI", ["bash", "scripts/ci.sh"])])


class MergeTests(unittest.TestCase):
    def test_generic_merge_retains_exact_refs_for_the_privacy_scanner(self):
        commands = []
        def fake_checked(argv, **kwargs):
            commands.append(argv)
            if argv[:2] == ["git", "rev-parse"]:
                return (SHA_BASE if "bases" in argv[2] else SHA_A) + "\n"
            return ""
        with patch.object(pr_watch_merge, "pr_snapshot", side_effect=[pr(), pr(state="MERGED")]), \
                patch.object(pr_watch_merge, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_merge, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            pr_watch_merge.merge("example/one", 7, ".", privacy="scanner.py",
                                 expected_head=SHA_A, expected_base=SHA_BASE)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/heads/7"], commands)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/heads/7"], commands)
        self.assertIn(["git", "fetch", "-q", "origin", "+refs/heads/main:refs/pr-watch/bases/7"], commands)
        scan = execute.call_args_list[0].args[0]
        self.assertIn(SHA_BASE + ".." + SHA_A, scan)

    def test_issue_link_parser_ignores_code_and_other_repositories(self):
        self.assertEqual(pr_watch_merge.linked_issue("```\nFixes #1\n```\nRefs #2", "example/one"), [2])
        self.assertEqual(pr_watch_merge.linked_issue("Fixes example/two#3", "example/one"), [])
        self.assertEqual(pr_watch_merge.linked_issue("See https://github.com/example/one/issues/4", "example/one"), [4])

    def test_generic_merge_honours_a_repository_that_requires_no_issue_link(self):
        unlinked = pr(body="Summary only, no issue reference.")
        def fake_checked(argv, **kwargs):
            if argv[:2] == ["git", "rev-parse"]:
                return (SHA_BASE if "bases" in argv[2] else SHA_A) + "\n"
            return ""
        with patch.object(pr_watch_merge, "pr_snapshot", side_effect=[unlinked, pr(state="MERGED")]), \
                patch.object(pr_watch_merge, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_merge, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            pr_watch_merge.merge("example/one", 7, ".", expected_head=SHA_A, expected_base=SHA_BASE,
                                 require_issue_link=False)
        self.assertIn("merge", execute.call_args.args[0])
        # The default still refuses an unlinked PR before any fetch or merge command.
        with patch.object(pr_watch_merge, "pr_snapshot", side_effect=[unlinked]), \
                patch.object(pr_watch_merge, "checked") as fetch, \
                patch.object(pr_watch_merge, "run") as nothing:
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_merge.merge("example/one", 7, ".", expected_head=SHA_A, expected_base=SHA_BASE)
        fetch.assert_not_called()
        nothing.assert_not_called()


class PrepareWorktreeTests(unittest.TestCase):
    """The checkout's FETCH_HEAD is shared with every other fetch in the same clone (#16759)."""

    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="pr-watch-prepare-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        (self.root / "checkout").mkdir()
        self.config = {"repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                                        "base": "main", "verify": {"profile": "commands", "commands": [["true"]]},
                                        "merge": False, "privacy": None, "notify_command": None}],
                       "reviewer": {}, "webhook": {}, "poll_seconds": 60, "workers": 1,
                       "state": str(self.root / "state.json")}
        self.watcher = pr_watch.Watcher(self.config, verbose=False)

    def test_each_fetch_lands_in_its_own_ref_not_the_shared_fetch_head(self):
        calls = []
        def fake_checked(argv, **kwargs):
            calls.append(argv)
            if argv[:3] == ["gh", "repo", "view"]:
                return "example/one\n"
            if argv[:2] == ["git", "rev-parse"]:
                return (SHA_BASE if argv[2].endswith("/base") else SHA_A) + "\n"
            return ""
        with patch.object(pr_watch, "checked", side_effect=fake_checked):
            worktree = self.watcher.prepare_worktree("example/one", 7, pr())
        self.assertEqual(worktree.parent, pr_watch_recovery.owned_root(self.config["state"]))
        self.assertIn(["git", "fetch", "-q", "origin", "+refs/heads/main:refs/pr-watch/prepare/7/base"], calls)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/prepare/7/head"], calls)
        self.assertIn(["git", "rev-parse", "refs/pr-watch/prepare/7/base"], calls)
        self.assertIn(["git", "rev-parse", "refs/pr-watch/prepare/7/head"], calls)
        self.assertEqual([argv for argv in calls if "FETCH_HEAD" in argv], [])

    def test_another_fetch_in_the_checkout_does_not_invalidate_the_preparation(self):
        """Reproduce the race: merge-pr.sh fetches the PR head between the watcher's two fetches."""
        env = {k: v for k, v in os.environ.items() if not k.startswith(("GIT_", "GH_", "PYTHON"))}
        env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        def git(*args, cwd=None):
            result = subprocess.run(("git",) + args, cwd=cwd or self.root / "checkout", env=env,
                                    capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            return result.stdout.strip()
        git("init", "-q", "--bare", str(self.root / "origin.git"))
        git("init", "-q", "-b", "main")
        git("config", "user.name", "Test User")
        git("config", "user.email", "test@example.com")
        git("config", "commit.gpgSign", "false")
        (self.root / "checkout" / "a.txt").write_text("base\n")
        git("add", "a.txt")
        git("commit", "-q", "-m", "Base")
        base = git("rev-parse", "HEAD")
        git("remote", "add", "origin", str(self.root / "origin.git"))
        git("push", "-q", "origin", "main")
        git("checkout", "-q", "-b", "feature")
        (self.root / "checkout" / "b.txt").write_text("head\n")
        git("add", "b.txt")
        git("commit", "-q", "-m", "Head")
        head = git("rev-parse", "HEAD")
        git("push", "-q", "origin", "+HEAD:refs/pull/7/head")
        git("checkout", "-q", "main")
        real = pr_watch_common.checked
        def racing_checked(argv, **kwargs):
            if argv[:3] == ["gh", "repo", "view"]:
                return "example/one\n"
            result = real(argv, **kwargs)
            if argv[:2] == ["git", "fetch"] and "refs/heads/main" in argv[4]:
                # Another process (scripts/hooks/merge-pr.sh) fetches its PR head into FETCH_HEAD right now.
                real(["git", "fetch", "-q", "origin", "pull/7/head"], cwd=kwargs["cwd"], timeout=60)
            return result
        with patch.object(pr_watch, "checked", side_effect=racing_checked):
            worktree = self.watcher.prepare_worktree("example/one", 7, pr(head=head, baseRefOid=base))
        try:
            self.assertEqual(git("rev-parse", "HEAD", cwd=worktree), head)
        finally:
            self.watcher.remove_worktree("example/one", worktree)


if __name__ == "__main__":
    unittest.main()
