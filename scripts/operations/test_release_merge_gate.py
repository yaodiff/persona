"""A release PR can merge only after its exact proposed merge result passes every required platform check."""
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_merge_gate as gate

COMMIT = "a" * 40
MERGE = "b" * 40


class ReleaseMergeGateTests(unittest.TestCase):
    def pr(self):
        return {
            "state": "open",
            "draft": False,
            "base": {"ref": "main"},
            "head": {"sha": COMMIT, "repo": {"full_name": "nwverse/persona"}},
            "user": {"login": "chyroc"},
            "title": "release: v0.9.2-beta.2",
            "mergeable": True,
            "mergeable_state": "clean",
            "merge_commit_sha": MERGE,
        }

    def successful(self, command, **kwargs):
        if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
            return json.dumps(self.pr())
        if "check-runs" in command[2]:
            return json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                               "status": "completed", "conclusion": "success"}]})
        return json.dumps({"artifacts": [{"name": "release-pr-verified-7-" + MERGE, "expired": False}]})

    def test_non_release_pr_keeps_the_existing_merge_path(self):
        with patch.object(gate.subprocess, "check_output") as run:
            gate.require_verified("nwverse/persona", 7, COMMIT, "fix: a bug", run)
            run.assert_not_called()

    def test_exact_successful_merge_result_and_artifact_are_required(self):
        with patch.object(gate.subprocess, "check_output", side_effect=self.successful):
            gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2")

    def test_failed_missing_or_stale_verification_blocks_merge(self):
        for change in ("failed", "pending", "missing", "expired", "fork", "actor", "head"):
            def response(command, **kwargs):
                if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
                    data = self.pr()
                    if change == "fork":
                        data["head"]["repo"]["full_name"] = "fork/persona"
                    elif change == "actor":
                        data["user"]["login"] = "other"
                    elif change == "head":
                        data["head"]["sha"] = "c" * 40
                    return json.dumps(data)
                if "check-runs" in command[2]:
                    if change == "missing":
                        return json.dumps({"check_runs": []})
                    return json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                                       "status": "completed" if change == "failed" else "in_progress",
                                                       "conclusion": "failure" if change == "failed" else None}]})
                return json.dumps({"artifacts": [{"name": "release-pr-verified-7-" + MERGE, "expired": change == "expired"}]})
            with self.subTest(change=change), patch.object(gate.subprocess, "check_output", side_effect=response):
                with self.assertRaises(gate.ReleaseMergeError):
                    gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2")

    def test_paginated_check_runs_and_artifacts_are_parsed_across_pages(self):
        commands = []

        def paged(command, **kwargs):
            commands.append(command)
            if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
                return json.dumps(self.pr())
            if "check-runs" in command[2]:
                return (
                    json.dumps({"check_runs": [{"name": "Other check", "head_sha": MERGE,
                                                "status": "completed", "conclusion": "success"}]})
                    + json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                                  "status": "completed", "conclusion": "success"}]})
                )
            return (
                json.dumps({"total_count": 101, "artifacts": [{"name": "other-artifact", "expired": False}]})
                + "\n"
                + json.dumps({"total_count": 101, "artifacts": [{"name": "release-pr-verified-7-" + MERGE, "expired": False}]})
            )

        gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=paged)
        check_cmd = next(cmd for cmd in commands if "check-runs" in cmd[2])
        artifact_cmd = next(cmd for cmd in commands if "actions/artifacts" in cmd[2])
        self.assertIn("--paginate", check_cmd)
        self.assertIn("per_page=100", check_cmd[2])
        self.assertIn("--paginate", artifact_cmd)
        self.assertIn("per_page=100", artifact_cmd[2])
        self.assertIn("name=release-pr-verified-7-" + MERGE, artifact_cmd[2])

    def test_main_resolves_repository_when_env_missing(self):
        seen = []

        def runner(command, **kwargs):
            seen.append(command)
            if command[:3] == ["gh", "repo", "view"]:
                return "nwverse/persona\n"
            return self.successful(command, **kwargs)

        gate.main(["release_merge_gate.py", "7", COMMIT, "release: v0.9.2-beta.2"], env={}, runner=runner)
        self.assertEqual(seen[0][:3], ["gh", "repo", "view"])

    def test_main_uses_env_repository_skips_non_release_and_validates_inputs(self):
        seen = []

        def runner(command, **kwargs):
            seen.append(command)
            return self.successful(command, **kwargs)

        gate.main(["release_merge_gate.py", "7", COMMIT, "release: v0.9.2-beta.2"],
                  env={"GITHUB_REPOSITORY": "nwverse/persona"}, runner=runner)
        self.assertFalse(any(cmd[:3] == ["gh", "repo", "view"] for cmd in seen))

        seen.clear()
        gate.main(["release_merge_gate.py", "7", COMMIT, "fix: ordinary change"], env={}, runner=runner)
        self.assertEqual(seen, [])

        for bad_number in ("0", "-1", "7a", None, True):
            with self.subTest(bad_number=bad_number):
                seen.clear()
                with self.assertRaisesRegex(gate.ReleaseMergeError, "positive integer"):
                    gate.main(["release_merge_gate.py", bad_number, COMMIT, "release: v0.9.2-beta.2"], env={}, runner=runner)
                self.assertEqual(seen, [])

        with self.assertRaises(gate.ReleaseMergeError):
            gate.main(["release_merge_gate.py", "7"], env={}, runner=runner)

        with self.assertRaisesRegex(gate.ReleaseMergeError, "title is required"):
            gate.main(["release_merge_gate.py", "7", COMMIT, None], env={}, runner=runner)

    def test_resolve_repository_rejects_errors_and_invalid_names(self):
        for bad_repo in ("", "   ", "onlyone", "../evil", "owner/..", "./persona"):
            with self.subTest(bad_repo=bad_repo):
                with self.assertRaisesRegex(gate.ReleaseMergeError, "owner/name"):
                    gate.resolve_repository(env={}, runner=lambda cmd, **kw: bad_repo)

        for exc in (subprocess.CalledProcessError(1, "gh"), OSError("gh missing")):
            with self.subTest(exc=type(exc).__name__):
                def fail(cmd, **kw):
                    raise exc
                with self.assertRaisesRegex(gate.ReleaseMergeError, "could not determine"):
                    gate.resolve_repository(env={}, runner=fail)

    def test_unmergeable_or_stale_merge_state_blocks_merge(self):
        cases = (
            {"mergeable": None},
            {"mergeable": False},
            {"mergeable_state": "unknown"},
            {"mergeable_state": "dirty"},
            {"drop_mergeable": True},
        )
        for patch_fields in cases:
            def response(command, **kwargs):
                data = self.pr()
                if patch_fields.get("drop_mergeable"):
                    data.pop("mergeable", None)
                else:
                    data.update(patch_fields)
                return json.dumps(data)

            with self.subTest(patch_fields=patch_fields):
                with self.assertRaisesRegex(gate.ReleaseMergeError, "clean up-to-date merge result"):
                    gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=response)

    def test_paginated_failed_check_on_later_page_blocks_merge(self):
        def paged_failure(command, **kwargs):
            if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
                return json.dumps(self.pr())
            if "check-runs" in command[2]:
                return (
                    json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                                "status": "completed", "conclusion": "success"}]})
                    + json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                                  "status": "completed", "conclusion": "failure"}]})
                )
            return json.dumps({"artifacts": [{"name": "release-pr-verified-7-" + MERGE, "expired": False}]})

        with self.assertRaises(gate.ReleaseMergeError):
            gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=paged_failure)

    def test_deleted_head_repo_null_and_null_merge_commit_raise_release_merge_error(self):
        def null_repo(command, **kwargs):
            data = self.pr()
            data["head"]["repo"] = None
            return json.dumps(data)

        with self.assertRaisesRegex(gate.ReleaseMergeError, "protected repository"):
            gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=null_repo)

        def null_merge_commit(command, **kwargs):
            data = self.pr()
            data["merge_commit_sha"] = None
            return json.dumps(data)

        with self.assertRaisesRegex(gate.ReleaseMergeError, "no proposed merge result"):
            gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=null_merge_commit)

    def test_malformed_or_corrupt_check_runs_and_artifacts_fail_closed(self):
        valid_check = {"name": "All available release-PR checks passed", "head_sha": MERGE,
                       "status": "completed", "conclusion": "success"}
        valid_artifact = {"name": "release-pr-verified-7-" + MERGE, "expired": False}

        for payload in (
            "",
            "   ",
            "[]",
            "{}",
            '{"check_runs": "bad"}',
            json.dumps({"check_runs": [valid_check, None]}),
            json.dumps({"check_runs": [valid_check]}) + json.dumps({"message": "error"}),
            json.dumps({"check_runs": [valid_check]}) + " garbage",
        ):
            def bad_checks(command, **kwargs):
                if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
                    return json.dumps(self.pr())
                if "check-runs" in command[2]:
                    return payload
                return json.dumps({"artifacts": [valid_artifact]})

            with self.subTest(endpoint="check-runs", payload=payload):
                with self.assertRaisesRegex(gate.ReleaseMergeError, "could not verify"):
                    gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=bad_checks)

        for payload in (
            "",
            "   ",
            "[]",
            "{}",
            '{"artifacts": "bad"}',
            json.dumps({"artifacts": [valid_artifact, None]}),
            json.dumps({"artifacts": [valid_artifact]}) + json.dumps({"message": "error"}),
            json.dumps({"artifacts": [valid_artifact]}) + " garbage",
        ):
            def bad_artifacts(command, **kwargs):
                if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
                    return json.dumps(self.pr())
                if "check-runs" in command[2]:
                    return json.dumps({"check_runs": [valid_check]})
                return payload

            with self.subTest(endpoint="artifacts", payload=payload):
                with self.assertRaisesRegex(gate.ReleaseMergeError, "could not verify"):
                    gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2", runner=bad_artifacts)


if __name__ == "__main__":
    unittest.main()
