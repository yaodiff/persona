import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

import release_candidate
import release_pr


class ReleaseCandidateTests(unittest.TestCase):
    def pull(self, number=42, title="release: v1.2.0", channel="release", state="open", **changes):
        ref_ver = title.removeprefix("release: v") if isinstance(title, str) else "unknown"
        value = {
            "number": number,
            "title": title,
            "state": state,
            "draft": False,
            "base": {"ref": "main"},
            "head": {"ref": "release/" + ref_ver + "-r3",
                     "repo": {"full_name": "nwverse/persona"}},
            "user": {"login": "chyroc"},
            "html_url": "https://example.invalid/pull/" + str(number),
        }
        value.update(changes)
        return value

    def test_one_existing_candidate_is_reused_including_a_retry_suffix(self):
        for state in ("open", "OPEN"):
            with self.subTest(state=state):
                existing = self.pull(state=state)
                self.assertIs(release_candidate.active_candidate([existing], "nwverse/persona", "release"), existing)
        beta = self.pull(number=55, title="release: v1.2.0-beta.4", state="open")
        self.assertIs(release_candidate.active_candidate([beta], "nwverse/persona", "beta"), beta)
        self.assertIsNone(release_candidate.active_candidate([], "nwverse/persona", "release"))

    def test_other_channel_and_non_release_prs_do_not_own_the_candidate(self):
        self.assertIsNone(release_candidate.active_candidate([
            self.pull(number=1, title="release: v1.2.0-beta.4"),
            self.pull(number=2, title="feat: an ordinary change"),
            self.pull(number=3, state="closed"),
            self.pull(number=4, state="CLOSED"),
        ], "nwverse/persona", "release"))

    def test_multiple_candidates_need_an_explicit_superseding_decision(self):
        for state in ("open", "OPEN"):
            with self.subTest(state=state):
                with self.assertRaisesRegex(release_pr.ReleaseError, "#42, #43"):
                    release_candidate.active_candidate(
                        [self.pull(42, state=state), self.pull(43, state=state)],
                        "nwverse/persona",
                        "release",
                    )

    def test_draft_fork_deleted_fork_and_other_authors_cannot_supply_a_release_candidate(self):
        for changes in (
            {"draft": True},
            {"user": {"login": "other"}},
            {"user": None},
            {"user": "chyroc"},
            {"head": {"ref": "patch", "repo": {"full_name": "fork/persona"}}},
            {"head": {"ref": "deleted-fork-branch", "repo": None}},
            {"head": {"ref": "bad-repo-type", "repo": "nwverse/persona"}},
            {"head": None},
            {"head": "main"},
            {"base": {"ref": "another-branch"}},
            {"base": None},
            {"base": "main"},
            {"title": None},
        ):
            with self.subTest(changes=changes):
                self.assertIsNone(release_candidate.active_candidate([
                    self.pull(**changes)
                ], "nwverse/persona", "release"))

    def test_invalid_candidate_number_raises_release_error(self):
        for bad_number in (True, False, 0, -1, "42", None):
            with self.subTest(bad_number=bad_number):
                with self.assertRaisesRegex(release_pr.ReleaseError, "valid PR number"):
                    release_candidate.active_candidate([self.pull(number=bad_number)], "nwverse/persona", "release")

    def test_deleted_fork_pr_does_not_crash_detection_of_active_rest_candidate(self):
        deleted_fork = self.pull(number=10, title="feat: external contribution", head={"ref": "patch", "repo": None})
        active = self.pull(number=42, state="open")
        self.assertIs(
            release_candidate.active_candidate([deleted_fork, active], "nwverse/persona", "release"),
            active,
        )

    def test_non_object_pull_entry_raises_release_error(self):
        with self.assertRaisesRegex(release_pr.ReleaseError, "object"):
            release_candidate.active_candidate(["not-an-object"], "nwverse/persona", "release")

    def test_main_prints_rest_candidate_empty_when_none_and_rejects_invalid_html_url(self):
        with tempfile.TemporaryDirectory() as tmp:
            pulls_path = Path(tmp) / "pulls.json"
            pulls_path.write_text(json.dumps([]), encoding="utf-8")
            empty_out = io.StringIO()
            with mock.patch.object(sys, "argv", [
                "release_candidate.py", "--repository", "nwverse/persona",
                "--channel", "release", "--pulls", str(pulls_path),
            ]), redirect_stdout(empty_out):
                release_candidate.main()
            self.assertEqual(empty_out.getvalue(), "")

            pulls_path.write_text(json.dumps([self.pull(number=99, state="open")]), encoding="utf-8")
            stdout = io.StringIO()
            with mock.patch.object(sys, "argv", [
                "release_candidate.py", "--repository", "nwverse/persona",
                "--channel", "release", "--pulls", str(pulls_path),
            ]), redirect_stdout(stdout):
                release_candidate.main()
            self.assertEqual(stdout.getvalue().splitlines(), ["99", "https://example.invalid/pull/99"])

            for bad_url in (None, "", "   "):
                with self.subTest(bad_url=bad_url):
                    bad_pull = self.pull(number=100, state="open")
                    if bad_url is None:
                        bad_pull.pop("html_url")
                    else:
                        bad_pull["html_url"] = bad_url
                    pulls_path.write_text(json.dumps([bad_pull]), encoding="utf-8")
                    with mock.patch.object(sys, "argv", [
                        "release_candidate.py", "--repository", "nwverse/persona",
                        "--channel", "release", "--pulls", str(pulls_path),
                    ]):
                        with self.assertRaises(SystemExit):
                            release_candidate.main()

    def test_load_pull_pages_parses_paginated_arrays_and_deduplicates_across_pages(self):
        page1 = json.dumps([self.pull(number=i, title=f"feat: {i}") for i in range(1, 101)])
        page2 = json.dumps([self.pull(number=142, title="release: v1.2.0", state="open")])
        pulls = release_candidate.load_pull_pages(page1 + page2)
        found = release_candidate.active_candidate(pulls, "nwverse/persona", "release")
        self.assertEqual(found["number"], 142)

        dup = self.pull(number=42, title="release: v1.2.0", state="open")
        pulls_dup = release_candidate.load_pull_pages(json.dumps([dup]) + "\n" + json.dumps([dup]))
        found_dup = release_candidate.active_candidate(pulls_dup, "nwverse/persona", "release")
        self.assertEqual(found_dup["number"], 42)

        self.assertEqual(release_candidate.load_pull_pages("[][]"), [])

        for bad in ("", "   ", '{"message":"Not Found"}', '[{"number":1}]{"message":"err"}', "[] garbage", '["x"]'):
            with self.subTest(bad=bad):
                with self.assertRaises(release_pr.ReleaseError):
                    release_candidate.load_pull_pages(bad)

        for bad_iterable in ("[]", b"[]", bytearray(b"[]"), {}):
            with self.subTest(bad_iterable=type(bad_iterable).__name__):
                with self.assertRaises(release_pr.ReleaseError):
                    release_candidate.active_candidate(bad_iterable, "nwverse/persona", "release")

    def test_matches_repo_and_admin_ascii_case_insensitively(self):
        pr = self.pull(
            number=77,
            title="release: v1.2.0",
            head={"ref": "release/1.2.0", "repo": {"full_name": "Nwverse/Persona"}},
            user={"login": "Chyroc"},
        )
        self.assertIs(release_candidate.active_candidate([pr], "nwverse/persona", "release"), pr)
        self.assertIs(release_candidate.active_branch_candidate([pr], "nwverse/persona", "release/1.2.0"), pr)

        # The spoofed names are built, as test_external_service_refs.py writes its samples: that guard reads any other
        # repository of the organisation written out as a reference to it.
        org = "nwverse"
        for non_ascii_repo, non_ascii_user in (
            (org + "/perßona", "chyroc"),
            ("nwverse/persona", "chyro\u212a"),
            (org + "/pers\u0130na", "chyroc"),
            ("nwverse/persona", "chyr\u0130c"),
        ):
            with self.subTest(repo=non_ascii_repo, user=non_ascii_user):
                spoof = self.pull(
                    number=78,
                    title="release: v1.2.0",
                    head={"ref": "release/1.2.0", "repo": {"full_name": non_ascii_repo}},
                    user={"login": non_ascii_user},
                )
                self.assertIsNone(release_candidate.active_candidate([spoof], "nwverse/persona", "release"))
                self.assertIsNone(release_candidate.active_branch_candidate([spoof], "nwverse/persona", "release/1.2.0"))

    def test_active_branch_candidate_ignores_forks_drafts_and_non_admins_and_deduplicates(self):
        spoofed = [
            self.pull(number=10, head={"ref": "release/1.2.0", "repo": {"full_name": "attacker/persona"}}, user={"login": "chyroc"}),
            self.pull(number=11, head={"ref": "release/1.2.0", "repo": None}, user={"login": "attacker"}),
            self.pull(number=12, head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}}, user={"login": "other"}),
            self.pull(number=13, draft=True, head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}}),
            self.pull(number=14, state="closed", head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}}),
            self.pull(number=15, base={"ref": "other"}, head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}}),
            self.pull(number=16, head={"ref": "Release/1.2.0", "repo": {"full_name": "nwverse/persona"}}),
        ]
        self.assertIsNone(release_candidate.active_branch_candidate(spoofed, "nwverse/persona", "release/1.2.0"))

        real = self.pull(number=88, head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}})
        self.assertIs(
            release_candidate.active_branch_candidate(spoofed + [real, real], "nwverse/persona", "release/1.2.0"),
            real,
        )
        other = self.pull(number=89, head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}})
        with self.assertRaisesRegex(release_pr.ReleaseError, "#88, #89"):
            release_candidate.active_branch_candidate([real, other], "nwverse/persona", "release/1.2.0")

    def test_main_branch_mode_and_url_and_branch_validation(self):
        with tempfile.TemporaryDirectory() as tmp:
            pulls_path = Path(tmp) / "pulls.json"
            page1 = json.dumps([self.pull(number=10, head={"ref": "release/1.2.0", "repo": {"full_name": "attacker/persona"}})])
            page2 = json.dumps([self.pull(number=88, head={"ref": "release/1.2.0", "repo": {"full_name": "nwverse/persona"}})])
            pulls_path.write_text(page1, encoding="utf-8")
            empty_branch_out = io.StringIO()
            with mock.patch.object(sys, "argv", [
                "release_candidate.py", "--repository", "nwverse/persona",
                "--branch", "release/1.2.0", "--pulls", str(pulls_path),
            ]), redirect_stdout(empty_branch_out):
                release_candidate.main()
            self.assertEqual(empty_branch_out.getvalue(), "")

            pulls_path.write_text(page1 + page2, encoding="utf-8")
            stdout = io.StringIO()
            with mock.patch.object(sys, "argv", [
                "release_candidate.py", "--repository", "nwverse/persona",
                "--branch", "release/1.2.0", "--pulls", str(pulls_path),
            ]), redirect_stdout(stdout):
                release_candidate.main()
            self.assertEqual(stdout.getvalue().splitlines(), ["88", "https://example.invalid/pull/88"])

            for bad_branch in (
                "", ".", "..", "release/./1.0", "release//1.0",
                "attacker:release/1.2.0", "refs/heads/release/1.2.0",
                "release/1.2.0\n", "release/1.2.0\u00a0", "release/1.2.0\x9b",
            ):
                with self.subTest(bad_branch=bad_branch):
                    with mock.patch.object(sys, "argv", [
                        "release_candidate.py", "--repository", "nwverse/persona",
                        "--branch", bad_branch, "--pulls", str(pulls_path),
                    ]):
                        with self.assertRaises(SystemExit):
                            release_candidate.main()

            for bad_url in (
                "http://example.invalid/pull/88",
                "https://example.invalid/pull/88\n::error::pwn",
                "https://user@example.invalid/pull/88",
                "https://@example.invalid/pull/88",
                "https://[::1/",
                "https://:443/",
                "https://example.invalid:abc/pull/88",
                "https://example.invalid\\foo/pull/88",
                "https://example.invalid/pull/88\x9b31m",
            ):
                with self.subTest(bad_url=bad_url):
                    pulls_path.write_text(json.dumps([self.pull(number=88, html_url=bad_url)]), encoding="utf-8")
                    with mock.patch.object(sys, "argv", [
                        "release_candidate.py", "--repository", "nwverse/persona",
                        "--channel", "release", "--pulls", str(pulls_path),
                    ]):
                        with self.assertRaises(SystemExit):
                            release_candidate.main()


if __name__ == "__main__":
    unittest.main()
