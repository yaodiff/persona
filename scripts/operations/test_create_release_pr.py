"""Bounded, idempotent release-PR creation."""
import io
import json
import subprocess
import sys
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import create_release_pr as subject


def make_pr(
    number=42,
    title="release: v1.0.0-beta.1",
    branch="release/1.0.0-beta.1",
    repo="nwverse/persona",
    login="chyroc",
    state="open",
    draft=False,
    base_ref="main",
    html_url=None,
):
    head = {"ref": branch, "repo": {"full_name": repo} if repo is not None else None}
    pr = {
        "number": number,
        "state": state,
        "title": title,
        "base": {"ref": base_ref},
        "head": head,
        "user": {"login": login} if login is not None else None,
        "html_url": html_url if html_url is not None else f"https://github.com/nwverse/persona/pull/{number}",
    }
    if draft is not ...:
        pr["draft"] = draft
    return pr


class CreateReleasePRTests(unittest.TestCase):
    def test_empty_token_does_not_touch_github(self):
        with patch.object(subject, "gh") as call:
            with self.assertRaisesRegex(subject.ReleasePRError, "RELEASE_PR_TOKEN"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "")
            call.assert_not_called()

    def test_rejects_invalid_attempts_wait_and_branch_before_touching_github(self):
        with patch.object(subject, "gh") as call:
            with self.assertRaisesRegex(subject.ReleasePRError, "attempt"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=0)
            with self.assertRaisesRegex(subject.ReleasePRError, "negative"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", wait=-1)
            for bad_branch in (
                "",
                "   ",
                "attacker:release/1.0.0-beta.1",
                " release/1.0.0-beta.1",
                "release/1.0.0-beta.1 ",
                "release/1.0.0 beta.1",
                "release/1.0.0\nbeta.1",
                "refs/heads/release/1.0.0-beta.1",
                None,
            ):
                with self.subTest(bad_branch=bad_branch):
                    with self.assertRaisesRegex(subject.ReleasePRError, "branch"):
                        subject.create("nwverse/persona", "release: v1.0.0-beta.1", bad_branch, "body", "token")
            call.assert_not_called()

    def test_reuses_the_exact_existing_release_pr(self):
        response = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(number=41, title="unrelated", branch="fix/unrelated"),
            make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1"),
        ]), "")
        out = io.StringIO()
        with patch.object(subject, "gh", return_value=response) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 1)
            self.assertEqual(call.call_args.args[0], ["api", "repos/nwverse/persona/pulls?state=open&base=main&per_page=100", "--paginate"])
        self.assertEqual(out.getvalue().strip(), "https://github.com/nwverse/persona/pull/42")

    def test_matches_repository_and_admin_ascii_case_insensitively_and_open_uppercase(self):
        response = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(
                number=42,
                title="release: v1.0.0-beta.1",
                branch="release/1.0.0-beta.1",
                repo="Nwverse/Persona",
                login="Chyroc",
                state="OPEN",
                html_url="https://github.com/Nwverse/Persona/pull/42",
            ),
        ]), "")
        out = io.StringIO()
        with patch.object(subject, "gh", return_value=response) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 1)
        self.assertEqual(out.getvalue().strip(), "https://github.com/Nwverse/Persona/pull/42")

    def test_unrelated_open_prs_do_not_own_the_release_branch(self):
        listing = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(number=1, title="other", branch="fix/other"),
            make_pr(number=2, title="another", branch="fix/another"),
            make_pr(number=3, title="closed", branch="release/1.0.0-beta.1", state="closed"),
            make_pr(number=4, title="wrong base", branch="release/1.0.0-beta.1", base_ref="develop"),
            make_pr(number=5, title="null user", branch="release/1.0.0-beta.1", login=None),
            make_pr(number=6, title="wrong case ref", branch="Release/1.0.0-beta.1"),
        ]), "")
        created = subprocess.CompletedProcess([], 0, '{"html_url":"https://github.com/nwverse/persona/pull/44"}\n', "")
        with patch.object(subject, "gh", side_effect=[listing, created]) as call:
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 2)

    def test_ignores_fork_pr_spoofing_the_release_branch_and_title(self):
        listing = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(number=99, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1", repo="attacker/persona", login="chyroc"),
        ]), "")
        created = subprocess.CompletedProcess([], 0, '{"html_url":"https://github.com/nwverse/persona/pull/100"}\n', "")
        out = io.StringIO()
        with patch.object(subject, "gh", side_effect=[listing, created]) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 2)
        self.assertEqual(out.getvalue().strip(), "https://github.com/nwverse/persona/pull/100")

    def test_ignores_fork_pr_with_different_title_instead_of_blocking_creation(self):
        listing = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(number=99, title="spoofed title", branch="release/1.0.0-beta.1", repo="attacker/persona", login="attacker"),
            make_pr(number=98, title="deleted fork", branch="release/1.0.0-beta.1", repo=None, login="attacker"),
            make_pr(number=97, title="non-admin", branch="release/1.0.0-beta.1", repo="nwverse/persona", login="someone-else"),
        ]), "")
        created = subprocess.CompletedProcess([], 0, '{"html_url":"https://github.com/nwverse/persona/pull/101"}\n', "")
        out = io.StringIO()
        with patch.object(subject, "gh", side_effect=[listing, created]) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 2)
        self.assertEqual(out.getvalue().strip(), "https://github.com/nwverse/persona/pull/101")

    def test_reuses_real_release_pr_when_fork_pr_coexists(self):
        listing = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(number=99, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1", repo="attacker/persona", login="attacker"),
            make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1", repo="nwverse/persona", login="chyroc"),
        ]), "")
        out = io.StringIO()
        with patch.object(subject, "gh", return_value=listing) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 1)
        self.assertEqual(out.getvalue().strip(), "https://github.com/nwverse/persona/pull/42")

    def test_paginated_open_prs_finds_existing_release_pr_on_second_page(self):
        page1 = json.dumps([make_pr(number=i, title=f"pr {i}", branch=f"fix/{i}") for i in range(1, 101)])
        page2 = json.dumps([make_pr(number=142, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1")])
        listing = subprocess.CompletedProcess([], 0, page1 + page2, "")
        out = io.StringIO()
        with patch.object(subject, "gh", return_value=listing) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 1)
        self.assertEqual(out.getvalue().strip(), "https://github.com/nwverse/persona/pull/142")

    def test_paginated_duplicate_pr_number_across_pages_is_deduplicated(self):
        pr = make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1")
        listing = subprocess.CompletedProcess([], 0, json.dumps([pr]) + "\n" + json.dumps([pr]), "")
        out = io.StringIO()
        with patch.object(subject, "gh", return_value=listing) as call, redirect_stdout(out):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 1)
        self.assertEqual(out.getvalue().strip(), "https://github.com/nwverse/persona/pull/42")

    def test_rejects_draft_or_unknown_draft_state_on_existing_pr(self):
        for bad_draft in (True, None, "true", "false", ...):
            with self.subTest(bad_draft=bad_draft):
                pr = make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1", draft=bad_draft)
                listing = subprocess.CompletedProcess([], 0, json.dumps([pr]), "")
                with patch.object(subject, "gh", return_value=listing):
                    with self.assertRaisesRegex(subject.ReleasePRError, "draft"):
                        subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

    def test_rejects_two_distinct_maintainer_prs_for_same_branch(self):
        listing = subprocess.CompletedProcess([], 0, json.dumps([
            make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1"),
            make_pr(number=43, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1"),
        ]), "")
        with patch.object(subject, "gh", return_value=listing):
            with self.assertRaisesRegex(subject.ReleasePRError, "more than one"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

    def test_rejects_invalid_pr_numbers_and_bad_pages(self):
        for bad_raw in ("", "   ", '{"message":"Not Found"}', '[{"number":1}]{"message":"rate limit"}', "[] garbage", '["not-a-dict"]'):
            with self.subTest(bad_raw=bad_raw):
                listing = subprocess.CompletedProcess([], 0, bad_raw, "")
                with patch.object(subject, "gh", return_value=listing):
                    with self.assertRaises(subject.ReleasePRError):
                        subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

        for bad_number in (None, True, 0, -1, "42"):
            with self.subTest(bad_number=bad_number):
                pr = make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1")
                pr["number"] = bad_number
                listing = subprocess.CompletedProcess([], 0, json.dumps([pr]), "")
                with patch.object(subject, "gh", return_value=listing):
                    with self.assertRaisesRegex(subject.ReleasePRError, "number"):
                        subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

    def test_rejects_invalid_repository_and_malformed_urls_or_created_payloads(self):
        with patch.object(subject, "gh") as call:
            with self.assertRaisesRegex(subject.ReleasePRError, "owner/name"):
                subject.create("../evil", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            call.assert_not_called()

        for bad_url in (
            "",
            "https://example/pr/42",
            "http://github.com/nwverse/persona/pull/42",
            "https://github.com/attacker/persona/pull/42",
            "https://github.com/nwverse/persona/pull/99",
            "https://github.com/nwverse/persona/pull/42\n::error::injected",
            " https://github.com/nwverse/persona/pull/42",
            "https://github.com/nwverse/persona/pull/042",
        ):
            with self.subTest(existing_bad_url=bad_url):
                listing = subprocess.CompletedProcess([], 0, json.dumps([
                    make_pr(number=42, title="release: v1.0.0-beta.1", branch="release/1.0.0-beta.1", html_url=bad_url),
                ]), "")
                with patch.object(subject, "gh", return_value=listing):
                    with self.assertRaisesRegex(subject.ReleasePRError, "url"):
                        subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

        empty = subprocess.CompletedProcess([], 0, "[]", "")
        for bad_created in (
            "not-json",
            "[]",
            '{"html_url":"https://evil.example/nwverse/persona/pull/43"}',
            '{"html_url":"https://github.com/nwverse/persona/pull/43\\n::error::boom"}',
            '{"number":44,"html_url":"https://github.com/nwverse/persona/pull/43"}',
        ):
            with self.subTest(bad_created=bad_created):
                created = subprocess.CompletedProcess([], 0, bad_created, "")
                with patch.object(subject, "gh", side_effect=[empty, created]):
                    with self.assertRaises(subject.ReleasePRError):
                        subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

    def test_throttled_creation_uses_bounded_backoff_then_succeeds(self):
        listing = subprocess.CompletedProcess([], 0, "[]", "")
        limited = subprocess.CompletedProcess([], 1, "", "GraphQL: was submitted too quickly")
        created = subprocess.CompletedProcess([], 0, '{"html_url":"https://github.com/nwverse/persona/pull/43"}\n', "")
        waits = []
        with patch.object(subject, "gh", side_effect=[listing, limited, limited, created]):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=3, wait=2, sleeper=waits.append)
            self.assertEqual(waits, [2, 4])

    def test_retry_budget_does_not_hide_a_failure(self):
        listing = subprocess.CompletedProcess([], 0, "[]", "")
        limited = subprocess.CompletedProcess([], 1, "", "was submitted too quickly")
        waits = []
        with patch.object(subject, "gh", side_effect=[listing] + [limited] * 3):
            with self.assertRaisesRegex(subject.ReleasePRError, "bounded retry budget"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=3, wait=2, sleeper=waits.append)
            self.assertEqual(waits, [2, 4])

    def test_a_permanent_failure_is_not_retried(self):
        listing = subprocess.CompletedProcess([], 0, "[]", "")
        denied = subprocess.CompletedProcess([], 1, "", "Resource not accessible by integration")
        with patch.object(subject, "gh", side_effect=[listing, denied]):
            with self.assertRaises(subject.ReleasePRError):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=5, wait=2, sleeper=lambda _: self.fail("permanent failure was retried"))


if __name__ == "__main__":
    unittest.main()
