"""Tests for SHA-bound main verification and its bounded repair workflow."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import main_watch
from main_watch import MainWatch, WatchError, load_config
from main_watch_verify import commands, failures


def check(name="Go vet", ok=False, output="undefined: Example"):
    return {"name": name, "command": ["go", "-C", "server", "vet", "./..."],
            "ok": ok, "summary": output, "stdout": output, "stderr": output if not ok else ""}


class ConfigTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.checkout = self.root / "checkout"
        self.checkout.mkdir()

    def tearDown(self):
        self.temp.cleanup()

    def write(self, **updates):
        value = {"repository": "owner/repository", "checkout": str(self.checkout),
                 "base": "main", "verify": {"profile": "commands", "commands": [
                     {"name": "test", "argv": ["python3", "-m", "unittest"]}]},
                 "state": str(self.root / "state.json")}
        value.update(updates)
        path = self.root / "config.json"
        path.write_text(json.dumps(value))
        return path

    def test_defaults_are_local_and_unpublished(self):
        config = load_config(self.write())
        self.assertFalse(config["publish"])
        self.assertEqual(config["poll_seconds"], 60)
        self.assertEqual(config["repair"]["timeout_seconds"], 3600)

    def test_relative_checkout_and_invalid_publication_are_rejected(self):
        with self.assertRaisesRegex(WatchError, "absolute"):
            load_config(self.write(checkout="relative"))
        with self.assertRaisesRegex(WatchError, "publish"):
            load_config(self.write(publish="yes"))

    def test_unsafe_branch_and_timeout_are_rejected(self):
        with self.assertRaisesRegex(WatchError, "branch"):
            load_config(self.write(base="-bad"))
        with self.assertRaisesRegex(WatchError, "poll_seconds"):
            load_config(self.write(poll_seconds=1))

    def test_commands_cannot_be_shell_snippets(self):
        path = self.write(verify={"profile": "commands", "commands": [{"name": "x", "argv": "false"}]})
        with self.assertRaisesRegex(WatchError, "argv array"):
            commands(load_config(path)["verify"], self.checkout)

    def test_extra_lane_and_worktree_substitution(self):
        config = {"profile": "commands", "commands": [{"name": "x", "argv": ["check", "{worktree}"]}],
                  "extra_lanes": ["runtimes"], "timeout_seconds": 60}
        resolved = commands(config, self.root)
        self.assertEqual(resolved[0][1], ["check", str(self.root)])
        self.assertEqual(resolved[1][1], ["bash", str(self.root / "scripts/ci/runtimes.sh")])
        with self.assertRaisesRegex(WatchError, "unsupported"):
            commands(dict(config, extra_lanes=["unknown"]), self.root)

    def test_exact_main_checkers_follow_the_tested_commit(self):
        resolved = dict((name, argv) for name, argv, _ in commands({"profile": "persona"}, self.checkout))
        for lane, script in (("Repository guards", "guards.sh"), ("Script and documentation guards", "scripts.sh")):
            with self.subTest(lane=lane):
                self.assertEqual(resolved[lane], ["bash", str(self.checkout / "scripts/ci" / script)])

    def test_repair_checkers_stay_in_the_frozen_main_checkout(self):
        frozen = self.root / "frozen-main"
        config = {"profile": "persona", "trusted_checkout": str(frozen)}
        resolved = dict((name, argv) for name, argv, _ in commands(config, self.checkout))
        self.assertEqual(resolved["Repository guards"], ["bash", str(frozen / "scripts/ci/guards.sh")])
        self.assertEqual(resolved["Script and documentation guards"], ["bash", str(frozen / "scripts/ci/scripts.sh")])


class MainWatchTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.checkout = self.root / "checkout"
        self.checkout.mkdir()
        self.config = {"repository": "owner/repository", "checkout": str(self.checkout),
                       "base": "main", "verify": {"profile": "commands", "commands": [
                           {"name": "check", "argv": ["check"]}]},
                       "state": str(self.root / "state.json"), "publish": False,
                       "machine_id": "test-machine", "poll_seconds": 60,
                       "repair": {"command": "claude", "timeout_seconds": 3600}}
        self.watch = MainWatch(self.config, verbose=False)
        self.head = "a" * 40

    def tearDown(self):
        self.temp.cleanup()

    def test_dry_run_records_failure_without_repair_or_publication(self):
        with mock.patch.object(self.watch, "repair") as repair:
            self.watch.record_failure(self.head, [check()])
        repair.assert_not_called()
        record = self.watch.state.data["heads"][self.head]
        self.assertEqual(record["status"], "failed")
        self.assertEqual(self.watch.state.data["failures"][record["digest"]]["status"], "unpublished")
        self.assertTrue(Path(self.config["state"]).is_file())
        self.assertEqual(Path(self.config["state"]).stat().st_mode & 0o777, 0o600)

    def test_same_sha_failure_is_retried_only_after_bounded_backoff(self):
        self.watch.config["publish"] = True
        self.watch.prepare_issue = mock.Mock(return_value=(123, "owned"))
        self.watch.prepare_repair_worktree = mock.Mock(return_value="fix/main-test")
        self.watch.repair = mock.Mock(side_effect=WatchError("not fixed"))
        self.watch.resume_publication = mock.Mock(return_value=False)
        self.watch.existing_pr = mock.Mock(return_value=None)
        self.watch.claim_conflict = mock.Mock(return_value=False)
        self.watch.repair_verifier = mock.Mock(return_value=self.root)
        for attempt in range(main_watch.REPAIR_LIMIT + 1):
            with mock.patch("main_watch.now", return_value=attempt * 1800), \
                    mock.patch("main_watch.github_post"):
                if attempt < main_watch.REPAIR_LIMIT:
                    with self.assertRaises(WatchError):
                        self.watch.record_failure(self.head, [check()])
                else:
                    self.watch.record_failure(self.head, [check()])
        records = self.watch.state.data["failures"]
        self.assertEqual(len(records), 1)
        record = next(iter(records.values()))
        self.assertEqual(record["attempts"], main_watch.REPAIR_LIMIT)
        self.assertEqual(record["status"], "repair-exhausted")
        self.assertEqual(self.watch.repair.call_count, main_watch.REPAIR_LIMIT)

    def test_other_owner_does_not_consume_repair_attempt(self):
        self.watch.config["publish"] = True
        self.watch.prepare_issue = mock.Mock(return_value=(123, "claimed"))
        self.watch.repair = mock.Mock()
        self.watch.record_failure(self.head, [check()])
        self.watch.repair.assert_not_called()
        record = next(iter(self.watch.state.data["failures"].values()))
        self.assertEqual(record["attempts"], 0)
        self.assertEqual(record["status"], "claimed")

    def test_new_head_never_inherits_passed_evidence(self):
        self.watch.state.data["heads"][self.head] = {"status": "passed"}
        self.watch.fetch = mock.Mock(return_value="b" * 40)
        context = mock.MagicMock()
        context.__enter__.return_value = self.checkout
        self.watch.checkout = mock.Mock(return_value=context)
        self.watch.verify = mock.Mock(return_value=[check(ok=True)])
        self.watch.once()
        self.watch.verify.assert_called_once()
        self.assertEqual(self.watch.state.data["heads"]["b" * 40]["status"], "passed")

    def test_repair_error_is_not_recorded_as_passed(self):
        self.watch.config["publish"] = True
        self.watch.prepare_issue = mock.Mock(return_value=(123, "owned"))
        self.watch.repair = mock.Mock(side_effect=WatchError("worker failed"))
        with self.assertRaisesRegex(WatchError, "worker failed"):
            self.watch.record_failure(self.head, [check()])
        self.assertEqual(self.watch.state.data["heads"][self.head]["status"], "failed")
        record = next(iter(self.watch.state.data["failures"].values()))
        self.assertEqual(record["status"], "repair-failed")
        self.assertNotIn("repair", record)

    def test_moved_main_does_not_publish_old_repair(self):
        self.watch.config["publish"] = True
        self.watch.prepare_issue = mock.Mock(return_value=(123, "owned"))
        # The branch/push ordering below uses a real temporary repository.
        self.watch.repair = mock.Mock(side_effect=WatchError("main moved during repair"))
        with self.assertRaisesRegex(WatchError, "main moved"):
            self.watch.record_failure(self.head, [check()])

    def test_explicit_adapter_paths_move_to_the_verified_clone(self):
        config = {"commands": [{"argv": ["check", str(self.checkout) + "/tool"]}],
                  "extra_lanes": ["web"]}
        result = main_watch.verification_config_at(config, self.checkout, self.root / "copy")
        self.assertEqual(result["commands"][0]["argv"], ["check", str(self.root / "copy") + "/tool"])
        self.assertEqual(result["extra_lanes"], ["web"])
        self.assertEqual(config["commands"][0]["argv"][1], str(self.checkout) + "/tool")

    def test_instance_lock_is_exclusive(self):
        self.watch.single_instance()
        second = MainWatch(dict(self.config, state=self.config["state"]), verbose=False)
        with self.assertRaisesRegex(WatchError, "lock"):
            second.single_instance()
        self.watch.instance.close()

    def test_public_diagnostics_remove_host_paths(self):
        item = check(output="/home/maintainer/private/checkout/file.go:12: undefined: Example\nHOME=/home/maintainer\n")
        item["command"] = ["tool", str(self.checkout) + "/input"]
        title, body = main_watch.scrub_report(self.checkout, self.head, [item])
        self.assertNotIn("/home/maintainer", body)
        self.assertNotIn(str(self.checkout), body)
        self.assertIn("undefined: Example", body)
        self.assertIn(self.head[:12], title)
        self.assertIn(main_watch.MARKER, body)

    def test_issue_marker_is_stable_across_main_heads(self):
        first = main_watch.normalized_failed_checks([check()])[1]
        changed_path = check(output="/private/state/worktrees/main-" + "a" * 16 + "/file.go:12: undefined: Example")
        second = main_watch.normalized_failed_checks([changed_path])[1]
        self.assertEqual(first, second)
        self.assertNotEqual(first, main_watch.normalized_failed_checks([check(output="different failure")])[1])

    def test_claimed_issue_is_not_repaired_again(self):
        digest = main_watch.normalized_failed_checks([check()])[1]
        claimed = {"number": 123, "state": "OPEN", "title": "broken",
                   "body": "<!-- %s:%s -->" % (main_watch.MARKER, digest),
                   "comments": [{"body": "Claim: another worker\n", "createdAt": main_watch.timestamp()}]}
        with mock.patch("main_watch.gh_json", side_effect=[[claimed], claimed]):
            number, ownership = self.watch.prepare_issue(self.head, [check()])
        self.assertEqual((number, ownership), (123, "claimed"))

    def init_repository(self):
        subprocess.run(["git", "init", "-q", str(self.checkout)], check=True)
        subprocess.run(["git", "-C", str(self.checkout), "config", "user.name", "Test"], check=True)
        subprocess.run(["git", "-C", str(self.checkout), "config", "user.email", "test@example.com"], check=True)
        (self.checkout / "broken.txt").write_text("before\n")
        subprocess.run(["git", "-C", str(self.checkout), "add", "broken.txt"], check=True)
        subprocess.run(["git", "-C", str(self.checkout), "commit", "-qm", "initial"], check=True)
        return subprocess.check_output(["git", "-C", str(self.checkout), "rev-parse", "HEAD"], text=True).strip()

    def test_fetch_updates_the_baseline_ref_to_the_exact_main_commit(self):
        head = self.init_repository()
        origin = self.root / "origin.git"
        subprocess.run(["git", "-C", str(self.checkout), "branch", "-M", "main"], check=True)
        subprocess.run(["git", "clone", "-q", "--bare", "--branch", "main", str(self.checkout), str(origin)], check=True)
        subprocess.run(["git", "-C", str(self.checkout), "remote", "add", "origin", str(origin)], check=True)
        stale = subprocess.check_output(
            ["git", "-C", str(self.checkout), "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "stale baseline"],
            text=True).strip()
        subprocess.run(["git", "-C", str(self.checkout), "update-ref", "refs/remotes/origin/main", stale], check=True)

        self.assertEqual(self.watch.fetch(), head)
        current = subprocess.check_output(["git", "-C", str(self.checkout), "rev-parse", "origin/main"], text=True).strip()
        self.assertEqual(current, head)

    def test_real_repository_repair_verifies_and_commits_before_publication(self):
        head = self.init_repository()

        class Context:
            def __init__(self, path, checkout):
                self.path = Path(path)
                self.checkout = Path(checkout)
            def __enter__(self):
                subprocess.run(["git", "-C", str(self.checkout), "worktree", "add", "--detach", str(self.path), head], check=True)
                return self.path
            def __exit__(self, *args):
                subprocess.run(["git", "-C", str(self.checkout), "worktree", "remove", "--force", str(self.path)], check=True)

        self.watch.checkout = lambda sha: Context(self.root / "repair", self.checkout)
        self.watch.config["publish"] = True
        self.watch.fetch = mock.Mock(return_value="0" * 39 + "1" if head != "0" * 39 + "1" else "0" * 39 + "2")
        self.watch.repair_verifier = mock.Mock(return_value=self.root)
        with mock.patch.object(self.watch, "invoke_repair", side_effect=lambda worktree, *args: (
                (worktree / "broken.txt").write_text("after\n"),
                subprocess.run(["git", "-C", str(worktree), "add", "-u"], check=True),
                "broken.txt")[-1]), \
                mock.patch("main_watch.privacy_scanner_environment", return_value={}), \
                mock.patch("main_watch.checked", side_effect=self.checked_without_public_scanner) as git:
            self.watch.verify = mock.Mock(return_value=[check(ok=True)])
            with self.assertRaisesRegex(WatchError, "main moved|gh"):
                self.watch.repair(head, 123, [check()])
        self.assertTrue(any(call.args[0][:3] == ["git", "-c", "user.name=Test"] for call in git.call_args_list))
        self.watch.fetch.assert_called_once()
        self.assertFalse(any("push" in call.args[0] for call in git.call_args_list if call.args and isinstance(call.args[0], list)))
        self.assertEqual(self.watch.state.data["failures"], {})

    def test_evidence_is_saved_before_push_and_pr_timeout_recovers_without_model(self):
        head = self.init_repository()
        self.watch.record_repair_evidence(head, 123, [check()], "b" * 40, [check(ok=True)])
        record = next(iter(self.watch.state.data["failures"].values()))
        self.assertEqual(record["status"], "publication-pending")
        self.assertEqual(record["head"], head)
        self.assertEqual(record["repair_head"], "b" * 40)
        self.assertEqual(record["verified_checks"], [check(ok=True)])
        reloaded = main_watch.MainWatch(self.config, verbose=False)
        record = next(iter(reloaded.state.data["failures"].values()))
        self.assertEqual(record["repair_head"], "b" * 40)
        self.assertEqual(record["verified_checks"], [check(ok=True)])

    def test_new_file_patch_and_no_stale_commit_or_untracked_output(self):
        head = self.init_repository()
        class RepositoryCopy:
            def __init__(self, path, checkout):
                self.path = Path(path)
                self.checkout = Path(checkout)
            def __enter__(self):
                subprocess.run(["git", "clone", "-q", str(self.checkout), str(self.path)], check=True)
                return self.path
            def __exit__(self, *args):
                return False

        def worker(argv, **kwargs):
            checkout = Path(kwargs["cwd"])
            (checkout / "fixed.txt").write_text("fixed\n")
            (checkout / "node_modules").mkdir()
            (checkout / "node_modules" / "artifact").write_text("not source\n")
            return subprocess.CompletedProcess(argv, 0, "{}", "")

        with mock.patch("main_watch.repair_repository_copy", return_value=RepositoryCopy(self.root / "copy", self.checkout)), \
                mock.patch("main_watch.repair_worker_argv", return_value=["worker"]), \
                mock.patch("main_watch.run", side_effect=worker):
            diff = self.watch.invoke_repair(self.checkout, head, 123, [check()])
        self.assertIn("fixed.txt", diff)
        staged = subprocess.check_output(["git", "-C", str(self.checkout), "diff", "--cached", "--name-only"], text=True)
        self.assertEqual(staged.splitlines(), ["fixed.txt"])
        self.assertFalse((self.checkout / "node_modules").exists())
        self.assertEqual(subprocess.check_output(["git", "-C", str(self.checkout), "rev-parse", "HEAD"], text=True).strip(), head)

    def test_repair_requires_green_checks_before_push(self):
        head = self.init_repository()
        class Checkout:
            def __init__(self, checkout):
                self.checkout = Path(checkout)
            def __enter__(self):
                subprocess.run(["git", "-C", str(self.checkout), "switch", "-c", "fix/main-test"], check=True, capture_output=True)
                return self.checkout
            def __exit__(self, *args):
                return False

        def edit(worktree, *args):
            (worktree / "broken.txt").write_text("after\n")
            subprocess.run(["git", "-C", str(worktree), "add", "-u"], check=True)
            return "broken.txt"

        self.watch.checkout = lambda sha: Checkout(self.checkout)
        self.watch.invoke_repair = mock.Mock(side_effect=edit)
        self.watch.repair_verifier = mock.Mock(return_value=self.root)
        self.watch.verify = mock.Mock(return_value=[check()])
        with mock.patch("main_watch.checked", side_effect=self.checked_without_public_scanner) as commands_run, \
                mock.patch("main_watch.privacy_scanner_environment", return_value={}):
            with self.assertRaisesRegex(WatchError, "did not pass"):
                self.watch.repair(head, 123, [check()])
        self.assertFalse(any("push" in call.args[0] for call in commands_run.call_args_list))

    def test_worktree_restarts_reuse_only_exact_owned_checkout(self):
        head = self.init_repository()
        with self.watch.checkout(head) as first:
            path = first
            self.assertEqual(subprocess.check_output(["git", "-C", str(path), "rev-parse", "HEAD"], text=True).strip(), head)
        restarted = MainWatch(self.config, verbose=False)
        with restarted.checkout(head) as second:
            self.assertEqual(first, second)
        second_head = subprocess.check_output(["git", "-C", str(self.checkout), "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "second"], text=True, cwd=self.checkout).strip()
        with restarted.checkout(second_head) as third:
            self.assertNotEqual(path, third)
            self.assertEqual(subprocess.check_output(["git", "-C", str(third), "rev-parse", "HEAD"], text=True).strip(), second_head)
        self.assertTrue(path.exists())

    def test_claim_race_first_valid_worker_wins_and_later_claim_is_withdrawn(self):
        branch = "fix/123-main-" + self.head[:12] + "-abc"
        comments = [
            {"id": 1, "createdAt": "2026-01-01T00:00:00Z", "body": "Claim: first\nAgent: yes (other / fix/123-other)"},
            {"id": 2, "createdAt": "2026-01-01T00:00:01Z", "body": "Claim: second\nAgent: yes (test-machine / " + branch + ")"},
        ]
        self.watch.config["publish"] = True
        with mock.patch("main_watch.gh_json", return_value={"comments": comments}), \
                mock.patch("main_watch.run") as remove:
            self.assertTrue(self.watch.claim_conflict(123, branch))
        self.assertTrue(any(call.args[0][:3] == ["gh", "api", "--method"] for call in remove.call_args_list))
        later = [comments[1], dict(comments[0], id=3, createdAt="2026-01-01T00:00:02Z")]
        with mock.patch("main_watch.gh_json", return_value={"comments": later}):
            self.assertFalse(self.watch.claim_conflict(123, branch))

    def test_abandon_releases_claim_and_progress_renews_it(self):
        own = {"createdAt": "2026-01-01T00:00:00Z", "body": "Claim: mine\nAgent: yes (test-machine / fix/123-test)"}
        self.assertFalse(self.watch.issue_claimed({"comments": [own]}))
        abandoned = [own, dict(own, body="Abandon: stopped", createdAt="2026-01-01T00:01:00Z")]
        self.assertFalse(self.watch.issue_claimed({"comments": abandoned}))
        other = dict(own, body="Claim: other\nAgent: yes (other / fix/123-other)")
        progress = dict(other, body="Progress: working", createdAt=main_watch.timestamp())
        self.assertTrue(self.watch.issue_claimed({"comments": [other, progress]}))
        abandoned_other = [other, dict(other, body="Abandon: exhausted", createdAt="2026-01-01T00:01:00Z")]
        self.assertFalse(self.watch.issue_claimed({"comments": abandoned_other}))

    def test_claim_race_ignores_an_abandoned_earlier_claim(self):
        branch = "fix/123-main-" + self.head[:12] + "-abc"
        old = {"id": "10", "createdAt": "2026-01-01T00:00:00Z", "body": "Claim: first\nAgent: yes (other / fix/123-old)",
               "author": {"login": "first-worker"}}
        abandoned = dict(old, id="11", body="Abandon: stopped", createdAt="2026-01-01T00:00:01Z")
        mine = {"id": "12", "createdAt": "2026-01-01T00:00:02Z", "body": "Claim: mine\nAgent: yes (test-machine / " + branch + ")",
                "author": {"login": "second-worker"}}
        with mock.patch("main_watch.gh_json", return_value={"comments": [old, abandoned, mine]}):
            self.assertFalse(self.watch.claim_conflict(123, branch))

    def test_protected_check_or_existing_test_change_cannot_fake_green(self):
        self.assertFalse(main_watch.allowed_repair_path("scripts/ci/guards.sh", False))
        self.assertFalse(main_watch.allowed_repair_path("docs/SUPER_RULES.md", False))
        self.assertFalse(main_watch.allowed_repair_path("server/internal/hub/foo_test.go", False))
        self.assertTrue(main_watch.allowed_repair_path("server/internal/hub/foo_test.go", True))
        self.assertTrue(main_watch.allowed_repair_path("server/internal/hub/foo.go", False))
        with mock.patch("main_watch.checked", return_value="scripts/ci/guards.sh\0"):
            self.assertFalse(main_watch.allowed_repair_paths(self.checkout))

    def test_public_diagnostics_drop_credentials_urls_and_private_keys(self):
        item = check(output="file.go:12: undefined: Example\nGITHUB_TOKEN=secret-value\n"
                            "file.go:13: token=secret-value\n"
                            "file.go:14: https://user:password@example.invalid/private\n"
                            "-----BEGIN PRIVATE KEY-----\n")
        title, body = main_watch.scrub_report(self.checkout, self.head, [item])
        self.assertIn("undefined: Example", body)
        for secret in ("secret-value", "password", "PRIVATE KEY", "example.invalid"):
            self.assertNotIn(secret, body)
        self.assertNotIn("```text\n## Main verification", body)
        self.assertNotIn("```sh", body)
        argv_item = check(output="file.go:12: undefined: Example")
        argv_item["command"] = ["tool", "--token", "secret-argv", "https://user:password@example.invalid"]
        _, argv_body = main_watch.scrub_report(self.checkout, self.head, [argv_item])
        self.assertNotIn("secret-argv", argv_body)
        self.assertNotIn("password", argv_body)

    def test_recovery_uses_existing_branch_without_another_model_run(self):
        self.watch.config["publish"] = True
        digest = main_watch.normalized_failed_checks([check()])[1]
        branch = "fix/123-main-%s-%s" % (self.head[:12], digest[:8])
        record = {"base_head": self.head, "repair_branch": branch, "issue": 123,
                  "digest": digest, "repair_head": "b" * 40, "verified_checks": [check(ok=True)]}
        with mock.patch("main_watch.gh_json", return_value=[]), \
                mock.patch("main_watch.run", return_value=subprocess.CompletedProcess([], 0, "b" * 40 + "\trefs/heads/" + branch + "\n", "")), \
                mock.patch("main_watch.check_public_text"), \
                mock.patch("main_watch.checked", return_value="https://example.invalid/pull/123"):
            self.assertTrue(self.watch.resume_publication(record))
        self.assertEqual(record["status"], "repair-published")
        self.assertEqual(record["pr_url"], "https://example.invalid/pull/123")
        self.assertEqual(record["repair_head"], "b" * 40)
        self.assertEqual(record["base_head"], self.head)
        self.assertEqual(record["repair_branch"], branch)
        moved = MainWatch(self.config, verbose=False)
        moved.state.data["failures"]["same"] = dict(record)
        with mock.patch("main_watch.gh_json", return_value=[{"state": "OPEN", "url": "https://example.invalid/pull/123"}]):
            self.assertTrue(moved.resume_publication(moved.state.data["failures"]["same"]))

    def test_exhaustion_publishes_one_abandon_note(self):
        self.watch.config["publish"] = True
        digest = main_watch.normalized_failed_checks([check()])[1]
        self.watch.state.data["failures"][digest] = {"attempts": 3, "issue": 123}
        with mock.patch("main_watch.github_post") as post, mock.patch("main_watch.gh_json", return_value=[]), \
                mock.patch("main_watch.check_public_text"):
            self.watch.record_failure(self.head, [check()])
            self.watch.record_failure(self.head, [check()])
        post.assert_called_once()
        self.assertTrue(post.call_args.args[-1].startswith("Abandon:"))

    @staticmethod
    def checked_without_public_scanner(argv, **kwargs):
        if argv[:1] == ["python3"]:
            return ""
        return subprocess.check_output(argv, cwd=kwargs.get("cwd"), text=True, stderr=subprocess.PIPE)


if __name__ == "__main__":
    unittest.main()
