"""An explicit release PR is the only new CI publication input."""
import json
import gzip
import hashlib
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import io

sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_pr as release

COMMIT = "a" * 40
OTHER = "b" * 40


class ReleasePRTests(unittest.TestCase):
    def pr(self, **changes):
        value = {"state": "OPEN", "isDraft": False, "baseRefName": "main", "headRefOid": COMMIT,
                 "headRepository": {"nameWithOwner": "nwverse/persona"}, "author": {"login": "chyroc"},
                 "title": "release: v0.9.2-beta.2"}
        value.update(changes)
        return value

    def test_only_an_admin_same_repository_release_pr_can_publish(self):
        self.assertEqual(release.verify_pr(self.pr(), "nwverse/persona", True), "0.9.2-beta.2")
        for change in ({"state": "MERGED"}, {"isDraft": True}, {"baseRefName": "other"},
                       {"headRepository": {"nameWithOwner": "fork/persona"}}, {"author": {"login": "other"}},
                       {"title": "feat: a release-looking title"}, {"title": "release: latest"}):
            with self.subTest(change=change), self.assertRaises(release.ReleaseError):
                release.verify_pr(self.pr(**change), "nwverse/persona", True)

    def test_stable_and_beta_titles_cannot_swap_channels(self):
        self.assertEqual(release.verify_pr(self.pr(title="release: v0.9.2"), "nwverse/persona", False), "0.9.2")
        for title, beta in (("release: v0.9.2", True), ("release: v0.9.2-beta.2", False),
                            ("release: v0.9.2-rc.1", False), ("release: v0.9.2-beta.2 extra", True)):
            with self.subTest(title=title), self.assertRaises(release.ReleaseError):
                release.verify_pr(self.pr(title=title), "nwverse/persona", beta)

    def test_version_allocation_never_reuses_an_immutable_tag(self):
        def remote(command, **kwargs):
            return ("a" * 40 + "\trefs/tags/v0.9.1\n" + "b" * 40 + "\trefs/tags/v0.9.1-beta.1\n"
                    + "c" * 40 + "\trefs/tags/v0.9.1-beta.2\n")
        with patch.object(release, "tags", return_value={"v0.9.1": "a" * 40, "v0.9.1-beta.1": "b" * 40, "v0.9.1-beta.2": "c" * 40}):
            self.assertEqual(release.next_version("nwverse/persona", True, stable_tag="v0.9.1"), "0.9.1-beta.3")
            self.assertEqual(release.next_version("nwverse/persona", False, "0.9.2"), "0.9.2")
        with patch.object(release, "tags", return_value={"v0.9.2": "a" * 40}):
            with self.assertRaises(release.ReleaseError):
                release.next_version("nwverse/persona", False, "0.9.2")

    def test_plan_binds_the_merge_result_not_just_a_pr_head(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.json"
            with patch.object(release.subprocess, "check_output", return_value="1794790000\n"):
                data = release.write_plan(path, "nwverse/persona", "beta", "0.9.1-beta.3", COMMIT, 12, OTHER, build=202611191200)
            self.assertEqual(data["tag"], "v0.9.1-beta.3")
            self.assertEqual(data["merge_commit"], OTHER)
            self.assertEqual(release.load_plan(path, "nwverse/persona", "beta", COMMIT)["pr"], 12)
            for commit in (OTHER, "not-a-sha"):
                with self.subTest(commit=commit), self.assertRaises(release.ReleaseError):
                    release.load_plan(path, "nwverse/persona", "beta", commit)
            with self.assertRaises(release.ReleaseError):
                release.load_plan(path, "nwverse/persona", "release", COMMIT)

    def test_artifact_receipt_hashes_the_exact_files_and_rejects_a_missing_platform(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = root / "artifacts"
            artifacts.mkdir()
            (artifacts / "latest.json").write_text('{"version":"0.9.1-beta.3"}')
            (artifacts / "Persona-test.apk").write_bytes(b"signed fixture")
            plan = root / "release.json"
            release.write_plan(plan, "nwverse/persona", "beta", "0.9.1-beta.3", COMMIT, 12, COMMIT, build=202611191200)
            data = release.verify_artifacts(plan, "nwverse/persona", "beta", COMMIT, (artifacts,))
            self.assertEqual(len(data["artifacts"]), 2)
            self.assertNotIn("release.json", {item["name"] for item in data["artifacts"]})
            with self.assertRaises((release.ReleaseError, OSError)):
                release.verify_artifacts(plan, "nwverse/persona", "beta", COMMIT, (root / "missing",))


    def test_merged_pr_and_actor_must_match_the_actual_merge_commit(self):
        pr = self.pr(state="MERGED", mergeCommit={"oid": COMMIT}, mergedBy={"login": "chyroc"}, title="release: v0.9.2-beta.2")
        self.assertEqual(release.verify_merged_pr(pr, "nwverse/persona", "beta", COMMIT, "chyroc"), "0.9.2-beta.2")
        for change in ({"state": "OPEN"}, {"mergeCommit": {"oid": OTHER}}, {"mergedBy": {"login": "other"}},
                       {"author": {"login": "other"}}, {"headRepository": {"nameWithOwner": "fork/persona"}}):
            with self.subTest(change=change), self.assertRaises(release.ReleaseError):
                release.verify_merged_pr(self.pr(**{**pr, **change}), "nwverse/persona", "beta", COMMIT, "chyroc")
        for merger in (None, "other", "github-actions[bot]", "github-actions"):
            with self.subTest(merger=merger), self.assertRaises(release.ReleaseError):
                release.verify_merged_pr({**pr, "mergedBy": None if merger is None else {"login": merger}},
                                         "nwverse/persona", "beta", COMMIT, "chyroc")
        with self.assertRaises(release.ReleaseError):
            release.verify_merged_pr(pr, "nwverse/persona", "beta", COMMIT, "other")

    def test_cli_web_receipt_binds_the_same_manifest_and_every_download(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            downloads = root / "downloads"
            downloads.mkdir()
            plan_path = root / "plan.json"
            release.write_plan(plan_path, "nwverse/persona", "beta", "0.9.1-beta.3", COMMIT, 12, COMMIT, build=202611191200)
            manifest = {"version": "0.9.1-beta.3", "commit": COMMIT, "build": release.load_plan(plan_path, "nwverse/persona", "beta", COMMIT)["build"],
                        "channel": "beta", "files": {}, "links": {}, "nodes": {}}
            for group, name in (("files", "personad-linux-amd64.gz"), ("links", "persona-link-windows-amd64.exe.gz"),
                                ("nodes", "persona-node-windows-amd64.exe.gz")):
                raw = name.encode()
                packed = gzip.compress(raw, mtime=0)
                entry = {"name": name, "size": len(packed), "sha256": hashlib.sha256(packed).hexdigest(),
                         "binary_sha256": hashlib.sha256(raw).hexdigest()}
                if group == "files":
                    manifest[group]["linux-amd64"] = entry
                elif group == "links":
                    manifest[group]["windows-amd64"] = entry
                else:
                    manifest[group].setdefault("persona-node", {})["windows-amd64"] = entry
                (downloads / name).write_bytes(packed)
            (downloads / "latest.json").write_text(json.dumps(manifest))
            for name in ("install.sh", "VERSION", "SHA256SUMS"):
                (downloads / name).write_text(name)
            for name in ("install.sh", "VERSION", "SHA256SUMS", "latest.json"):
                path = downloads / name
                content = path.read_bytes()
                for kind in ("missing", "empty", "symlink"):
                    path.unlink()
                    if kind == "empty":
                        path.write_bytes(b"")
                    elif kind == "symlink":
                        target = root / "bootstrap-target"
                        target.write_bytes(content)
                        path.symlink_to(target)
                    with self.subTest(name=name, kind=kind), self.assertRaisesRegex(release.ReleaseError, "bootstrap"):
                        release.bind_cli_web(plan_path, downloads)
                    if path.is_symlink() or path.exists():
                        path.unlink()
                    path.write_bytes(content)
            data = release.bind_cli_web(plan_path, downloads)
            self.assertEqual(len(data["artifacts"]), 7)
            release.verify_artifacts(plan_path, "nwverse/persona", "beta", COMMIT)
            android = root / "android"
            android.mkdir()
            (android / "Persona-test.apk").write_bytes(b"signed fixture")
            (android / "android.json").write_text('{"version":"0.9.1-beta.3"}')
            data = release.verify_artifacts(plan_path, "nwverse/persona", "beta", COMMIT, (android,))
            self.assertEqual(len(data["artifacts"]), 9)
            self.assertIn("android.json", {item["name"] for item in data["artifacts"]})
            # A platform job sees the CLI/Web install.sh in its download directory as well as in the plan. Presenting
            # the same bytes again is idempotent, not an ownership conflict; changed bytes still fail closed.
            repeated = root / "repeated"
            repeated.mkdir()
            for item in data["artifacts"]:
                target = repeated / item["name"]
                target.write_bytes(Path(item["path"]).read_bytes())
            count = len(data["artifacts"])
            self.assertEqual(len(release.verify_artifacts(plan_path, "nwverse/persona", "beta", COMMIT, (repeated,))["artifacts"]), count)
            (repeated / "install.sh").write_text("different bytes")
            with self.assertRaises(release.ReleaseError):
                release.verify_artifacts(plan_path, "nwverse/persona", "beta", COMMIT, (repeated,))
            (repeated / "install.sh").write_text("install.sh")
            self.assertEqual(len(release.verify_artifacts(plan_path, "nwverse/persona", "beta", COMMIT, (repeated,))["artifacts"]), count)
            (downloads / "personad-linux-amd64.gz").write_bytes(b"changed")
            with self.assertRaises(release.ReleaseError):
                release.verify_artifacts(plan_path, "nwverse/persona", "beta", COMMIT)
            (downloads / "persona-node-windows-amd64.exe.gz").unlink()
            with self.assertRaises(release.ReleaseError):
                release.bind_cli_web(plan_path, downloads)


    def test_schedule_skips_the_same_source_and_selects_stable_version_by_change(self):
        with patch.object(release, "tags", return_value={"v0.9.1": "a" * 40, "v0.9.1-beta.1": "b" * 40}):
            self.assertEqual(release.schedule_version("nwverse/persona", "beta", "v0.9.1", COMMIT, COMMIT, []), None)
            self.assertEqual(release.schedule_version("nwverse/persona", "beta", "v0.9.1", COMMIT, OTHER, []), "0.9.1-beta.2")
            self.assertEqual(release.schedule_version("nwverse/persona", "release", "v0.9.1", COMMIT, COMMIT, []), None)
            self.assertEqual(release.schedule_version("nwverse/persona", "release", "v0.9.1", COMMIT, OTHER, ["fix: repair a bug"]), "0.9.2")
            self.assertEqual(release.schedule_version("nwverse/persona", "release", "v0.9.1", COMMIT, OTHER, ["feat(ui): a feature"]), "0.10.0")
            for subject in ("Add phone-approved encrypted secret storage from local files (#13059)",
                            "Android: add starred messages support and Starred sidebar page (#12925)",
                            "iOS: saved prompts in the / list and the assistant's Saved Prompts page (#9436) (#10581)",
                            "Watch: support a new complication",
                            "feature(iPhone): share a recording"):
                with self.subTest(minor_subject=subject):
                    self.assertTrue(release.feature_subject(subject))
                    self.assertEqual(release.stable_version("v0.9.1", ["fix: repair a bug", subject]), "0.10.0")
            for subject in ("fix(android): repair a retry", "test: add an Android fixture",
                            "docs: document the new iOS feature", "Android: fix an offline border",
                            "iOS: remove the unused page", "Web: refactor the settings view",
                            "refactor: add no behavior", "Co-authored-by: Example <me@example.com>"):
                with self.subTest(patch_subject=subject):
                    self.assertFalse(release.feature_subject(subject))
                    self.assertEqual(release.stable_version("v0.9.1", [subject]), "0.9.2")


    def test_orphaned_release_branches_reserve_their_versions(self):
        with patch.object(release, "tags", return_value={"v0.9.1": "a" * 40, "v0.9.1-beta.1": "b" * 40, "v0.9.1-beta.8": "c" * 40}):
            self.assertEqual(release.next_version("nwverse/persona", True, stable_tag="v0.9.1",
                reserved=("0.9.1-beta.9",)), "0.9.1-beta.10")
            self.assertEqual(release.next_version("nwverse/persona", True, stable_tag="v0.9.1",
                reserved=("0.9.1-beta.9", "0.9.1-beta.10")), "0.9.1-beta.11")
            self.assertEqual(release.schedule_version("nwverse/persona", "beta", "v0.9.1", COMMIT, OTHER, [],
                reserved=("0.9.1-beta.10",)), "0.9.1-beta.11")


    def test_manifest_digest_is_compatible_with_system_python_and_unchanged(self):
        self.assertEqual(release.sha256_stream(io.BytesIO(b"release bytes")),
                         "ff7a5e6429d2c8511521e4abf41cd54a3e525ef4a1f24f8d1c67ede9d17874dd")
        with patch.object(release.hashlib, "file_digest", create=True, side_effect=AssertionError("Python 3.11+ API used")):
            release.sha256_stream(io.BytesIO(b"release bytes"))

    def test_manifest_size_and_both_digests_must_match_the_download(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw = b"exact executable bytes"
            packed = gzip.compress(raw, mtime=0)
            (root / "personad-linux-amd64.gz").write_bytes(packed)
            good = {"name": "personad-linux-amd64.gz", "size": len(packed),
                    "sha256": hashlib.sha256(packed).hexdigest(), "binary_sha256": hashlib.sha256(raw).hexdigest()}
            release.check_manifest_bytes(root, good)
            for key, value in (("size", len(packed) + 1), ("sha256", "0" * 64), ("binary_sha256", "0" * 64)):
                with self.subTest(key=key), self.assertRaises(release.ReleaseError):
                    release.check_manifest_bytes(root, {**good, key: value})

    def test_build_numbers_are_unique_and_monotonic_not_commit_minutes(self):
        with patch.object(release.subprocess, "check_output", return_value="202611191201\n"):
            self.assertEqual(release.next_build(202611191200), 202611191201)
            self.assertEqual(release.next_build(202611191201), 202611191202)
        with self.assertRaises(release.ReleaseError):
            release.next_build(999999999999)

    def test_beta_allocation_skips_all_existing_versions_even_with_pending_stable(self):
        with patch.object(release, "tags", return_value={"v0.9.1": "a" * 40, "v0.9.2": "b" * 40, "v0.9.1-beta.2": "c" * 40}):
            self.assertEqual(release.next_version("nwverse/persona", True), "0.9.2-beta.3")
            self.assertEqual(release.next_version("nwverse/persona", True, stable_tag="v0.9.1"), "0.9.1-beta.3")

    def test_resolve_target_commit_accepts_pinned_ancestor_and_rejects_tampering(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            env = {
                "GIT_AUTHOR_NAME": "Test",
                "GIT_AUTHOR_EMAIL": "test@example.com",
                "GIT_COMMITTER_NAME": "Test",
                "GIT_COMMITTER_EMAIL": "test@example.com",
            }
            subprocess.run(["git", "init", "-b", "main"], cwd=repo, check=True, capture_output=True)

            def commit(name):
                (repo / "f.txt").write_text(name + "\n")
                subprocess.run(["git", "add", "f.txt"], cwd=repo, check=True)
                subprocess.run(["git", "commit", "-m", name], cwd=repo, env=env, check=True, capture_output=True)
                return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo, text=True).strip()

            green = commit("green")
            merge = commit("merge")
            plan_dir = repo / ".github/release-prs"
            plan_dir.mkdir(parents=True)

            # Absent descriptor falls back to merge_commit for backward compatibility.
            self.assertEqual(
                release.resolve_target_commit(plan_dir, "0.9.1-beta.3", "beta", merge, cwd=repo),
                merge,
            )

            descriptor = plan_dir / "0.9.1-beta.3.json"
            descriptor.write_text(json.dumps({
                "version": "0.9.1-beta.3",
                "commit": green,
                "ci_run_id": 123456,
                "channel": "beta",
            }))
            self.assertEqual(
                release.resolve_target_commit(plan_dir, "0.9.1-beta.3", "beta", merge, cwd=repo),
                green,
            )

            # Non-ancestor target fails closed.
            descriptor.write_text(json.dumps({
                "version": "0.9.1-beta.3",
                "commit": merge,
                "ci_run_id": 123456,
                "channel": "beta",
            }))
            with self.assertRaises(release.ReleaseError):
                release.resolve_target_commit(plan_dir, "0.9.1-beta.3", "beta", green, cwd=repo)

            # Channel, version, SHA, and ci_run_id mismatches all fail closed.
            for bad in (
                {"version": "0.9.1-beta.4", "commit": green, "channel": "beta"},
                {"version": "0.9.1-beta.3", "commit": green, "channel": "release"},
                {"version": "0.9.1-beta.3", "commit": "not-a-sha", "channel": "beta"},
                {"version": "0.9.1-beta.3", "commit": green, "channel": "beta", "ci_run_id": 0},
                {"version": "0.9.1-beta.3", "commit": green, "channel": "beta", "ci_run_id": "123"},
            ):
                descriptor.write_text(json.dumps(bad))
                with self.subTest(bad=bad), self.assertRaises(release.ReleaseError):
                    release.resolve_target_commit(plan_dir, "0.9.1-beta.3", "beta", merge, cwd=repo)

    def test_verify_artifacts_supports_distinct_pinned_target_and_merge_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = root / "artifacts"
            artifacts.mkdir()
            (artifacts / "latest.json").write_text('{"version":"0.9.1-beta.3"}')
            plan = root / "plan.json"
            release.write_plan(plan, "nwverse/persona", "beta", "0.9.1-beta.3", COMMIT, 12, OTHER, build=202611191200)
            # Without merge_commit, mismatch fails closed (backward-compatible default).
            with self.assertRaises(release.ReleaseError):
                release.verify_artifacts(plan, "nwverse/persona", "beta", COMMIT, (artifacts,))
            # With matching merge_commit, verification succeeds; wrong merge_commit fails closed.
            data = release.verify_artifacts(plan, "nwverse/persona", "beta", COMMIT, (artifacts,), merge_commit=OTHER)
            self.assertEqual(data["commit"], COMMIT)
            self.assertEqual(data["merge_commit"], OTHER)
            with self.assertRaises(release.ReleaseError):
                release.verify_artifacts(plan, "nwverse/persona", "beta", COMMIT, (artifacts,), merge_commit="c" * 40)


if __name__ == "__main__":
    unittest.main()
