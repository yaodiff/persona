"""Release PR and protected publication workflow boundaries."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
PR = ROOT / ".github/workflows/release-pr.yml"
PUBLISH = ROOT / ".github/workflows/release-publish.yml"
SCHEDULE = ROOT / ".github/workflows/release-schedule.yml"


class ReleaseWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.pr = yaml.load(PR.read_text(), Loader=yaml.BaseLoader)
        self.publish = yaml.load(PUBLISH.read_text(), Loader=yaml.BaseLoader)

    def test_only_explicit_admin_same_repository_release_prs_run(self):
        text = PR.read_text()
        self.assertIn("pull_request:", text)
        self.assertNotIn("pull_request_target:", text)
        self.assertIn("startsWith(github.event.pull_request.title, 'release:')", text)
        self.assertIn("github.event.pull_request.head.repo.full_name == github.repository", text)
        self.assertIn("github.event.pull_request.user.login == 'chyroc'", text)
        self.assertIn("github.event.pull_request.merge_commit_sha", text)
        self.assertEqual(self.pr["permissions"], {"actions": "read", "contents": "read", "checks": "write"})

    def test_all_platforms_feed_the_one_verified_plan(self):
        jobs = self.pr["jobs"]
        self.assertEqual(jobs["verify"]["needs"], ["plan", "cli-web", "android", "apple", "windows"])
        self.assertEqual(jobs["apple"]["needs"], ["plan", "cli-web", "android"])
        self.assertEqual(jobs["windows"]["needs"], ["plan", "cli-web", "android"])
        self.assertEqual(jobs["android"]["environment"], "persona-release")
        self.assertEqual(jobs["apple"]["environment"], "persona-release")
        self.assertEqual(jobs["windows"]["environment"], "persona-release")
        for name in ("android", "apple", "windows"):
            self.assertIn("release-pr-" + name + "-", str(jobs[name]))
        self.assertNotIn("cli-web", jobs["cli-web"].get("environment", ""))
        self.assertNotIn("secrets.", str(jobs["cli-web"]))
        upload = next(step for step in jobs["cli-web"]["steps"] if step.get("uses") == "actions/upload-artifact@v4")
        self.assertEqual(upload["if"], "always()")
        self.assertIn("unverified artifacts are diagnostic only", str(jobs["cli-web"]))
        self.assertEqual(jobs["android"]["steps"][2]["with"]["packages"], "platform-tools")
        self.assertEqual(jobs["cli-web"]["steps"][5]["env"]["NODE_OPTIONS"], "--max-old-space-size=4096")
        self.assertIn('--receipt "$RUNNER_TEMP/android-receipt.json"', str(jobs["android"]))
        self.assertEqual(jobs["android"]["steps"][4]["with"]["path"], "${{ runner.temp }}/release-plan")
        self.assertIn('RUNNER_TEMP', str(jobs["android"]))
        self.assertNotIn("Path('release/plan.json')", str(jobs["android"]))
        self.assertIn('release-pr-plan-${{ needs.plan.outputs.pr }}', str(jobs))
        self.assertIn('release-pr-plan-${{ steps.source.outputs.number }}', str(jobs["plan"]))
        self.assertNotIn('release-pr-plan-${{ steps.plan.outputs.pr }}-${{ steps.plan.outputs.commit }}', str(jobs["plan"]))
        self.assertNotIn('release-pr-plan-${{ needs.plan.outputs.pr }}-${{ needs.plan.outputs.commit }}', str(jobs))

    def test_exact_merge_result_check_and_failure_propagation(self):
        jobs = self.pr["jobs"]
        self.assertEqual(jobs["plan"]["outputs"]["check_run_id"], "${{ steps.merge-check.outputs.id }}")
        plan_script = str(jobs["plan"]["steps"])
        self.assertIn("release_merge_check.py", plan_script)
        self.assertIn('test "$COMMIT" = "${{ github.event.pull_request.merge_commit_sha || github.sha }}"', plan_script)
        verify_script = str(jobs["verify"]["steps"])
        self.assertEqual(jobs["verify"]["steps"][0]["uses"], "actions/checkout@v4")
        gate = next(step for step in jobs["verify"]["steps"]
                    if step.get("name") == "Require every platform job to succeed before verification")
        self.assertEqual(gate["env"], {
            "CLI_WEB": "${{ needs.cli-web.result }}",
            "ANDROID": "${{ needs.android.result }}",
            "APPLE": "${{ needs.apple.result }}",
            "WINDOWS": "${{ needs.windows.result }}",
        })
        for failed in ("failure", "skipped", "cancelled"):
            with self.subTest(platform_result=failed):
                result = subprocess.run(["bash", "-c", gate["run"]],
                                        env=dict(os.environ, CLI_WEB=failed, ANDROID="success", APPLE="success", WINDOWS="success"),
                                        capture_output=True, text=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
        self.assertEqual(subprocess.run(["bash", "-c", gate["run"]],
                                        env=dict(os.environ, CLI_WEB="success", ANDROID="success", APPLE="success", WINDOWS="success"),
                                        capture_output=True, text=True, timeout=10).returncode, 0)
        self.assertIn('test "$result" = success', verify_script)
        self.assertIn("--conclusion success", verify_script)
        # GitHub skips the whole aggregate after a failed or skipped dependency by default. That would also skip
        # its failure-conclusion step and leave the exact merge-result check in_progress forever.
        self.assertEqual(jobs["verify"]["if"],
                         "always() && needs.plan.outputs.pr != '0' && needs.plan.outputs.version != ''")
        self.assertTrue(any(step.get("if") == "failure()" and "--conclusion failure" in step["run"]
                            for step in jobs["verify"]["steps"]))
        verified_upload = next(step for step in jobs["verify"]["steps"]
                               if step.get("uses") == "actions/upload-artifact@v4")
        self.assertEqual(verified_upload["if"], "success()")

    def test_cli_web_collection_uses_the_rendered_installer_and_stops_on_missing_outputs(self):
        step = next(step for step in self.pr["jobs"]["cli-web"]["steps"]
                    if step.get("name") == "Bind the verified downloads to the release plan")
        script = re.sub(r"\$\{\{.*?\}\}", "fixture", step["run"])
        required = ("install.sh", "bin/personad-linux-amd64.gz", "bin/VERSION",
                    "bin/SHA256SUMS", "bin/latest.json")
        for missing in (None, *required):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                build = root / "build-publish"
                (build / "bin").mkdir(parents=True)
                for name in required:
                    if name != missing:
                        (build / name).write_text("rendered installer" if name == "install.sh" else name)
                # A stale file in bin must never replace the rendered build-root installer.
                (build / "bin/install.sh").write_text("stale installer")
                commands = root / "commands"
                commands.mkdir()
                python = commands / "python3"
                python.write_text('#!/bin/sh\nprintf called > bind-called\nexit "${BIND_EXIT:-0}"\n')
                python.chmod(0o755)
                env = dict(os.environ, PATH=str(commands) + os.pathsep + os.environ["PATH"])
                result = subprocess.run(["bash", "-c", script], cwd=root, env=env,
                                        capture_output=True, text=True, timeout=20)
                if missing:
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertFalse((root / "bind-called").exists())
                    continue
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual((root / "release/downloads/install.sh").read_text(), "rendered installer")
                self.assertTrue((root / "bind-called").exists())
                failed = subprocess.run(["bash", "-c", script], cwd=root, env=dict(env, BIND_EXIT="37"),
                                        capture_output=True, text=True, timeout=20)
                self.assertEqual(failed.returncode, 37)
                self.assertIn("unverified artifacts are diagnostic only", failed.stderr)

    def test_android_release_reuses_exact_source_shards_before_signing(self):
        workflow = (ROOT / ".github/workflows/android-test.yml").read_text()
        release = PR.read_text()
        self.assertIn("required: true", workflow)
        self.assertEqual(yaml.load((ROOT / ".github/workflows/ci.yml").read_text(), Loader=yaml.BaseLoader)["jobs"]["android"]["with"]["commit"], "${{ github.sha }}")
        self.assertIn("ref: ${{ inputs.commit || github.sha }}", workflow)
        self.assertIn("lane: [lint-build-test-0, test-1]", workflow)
        self.assertIn("android-source-receipt-${{ inputs.commit || github.sha }}-${{ matrix.lane }}-${{ github.run_id }}-${{ github.run_attempt }}", workflow)
        self.assertIn("JOB_STATUS: ${{ job.status }}", workflow)
        self.assertIn("scripts/ci/android_result.py bind", workflow)
        self.assertIn("scripts/ci/android_result.py verify", workflow)
        self.assertIn("uses: ./.github/workflows/android-test.yml", release)
        self.assertIn("commit: ${{ needs.plan.outputs.commit }}", release)
        self.assertIn("name: android-source-result-${{ needs.plan.outputs.commit }}-${{ github.run_id }}-${{ github.run_attempt }}", release)
        self.assertIn("--release-only", release)
        self.assertIn("Android test evidence belongs to another commit", release)
        self.assertIn("Android test evidence belongs to another workflow run", release)
        self.assertIn("Android test evidence belongs to another workflow attempt", release)
        self.assertEqual(self.pr["jobs"]["android"]["needs"], ["plan", "android-tests"])

    def test_apple_export_keychain_is_visible_to_xcode(self):
        steps = self.pr["jobs"]["apple"]["steps"]
        install = next(step for step in steps if step.get("name") == "Install the Apple Distribution identity and provisioning profiles")
        script = install["run"]
        # Importing into a temporary file and finding the identity there is not enough: xcodebuild searches the
        # default keychain domain. Add the temporary keychain without discarding the user's existing search list.
        self.assertIn('subprocess.check_output(["security", "list-keychains", "-d", "user"], text=True).splitlines()', script)
        self.assertIn('subprocess.run(["security", "list-keychains", "-d", "user", "-s", sys.argv[1], *paths], check=True)', script)
        self.assertIn('security list-keychains -d user | grep -F "$PERSONA_SIGNING_KEYCHAIN"', script)
        self.assertNotIn('security list-keychains -d user -s "$PERSONA_SIGNING_KEYCHAIN" $existing', script)

    def test_apple_archive_stages_exact_source_and_resolved_input(self):
        text = PR.read_text()
        for required in ('scripts/ci/apple_release_source.py', '--source "$GITHUB_WORKSPACE"',
                         '--destination "$RUNNER_TEMP/apple-release-source"', '--meta "$meta"',
                         'PERSONA_RELEASE_SOURCE'):
            self.assertIn(required, text)
        release = (ROOT / 'scripts/release/release.sh').read_text()
        self.assertIn('cd "$PERSONA_RELEASE_SOURCE"', release)
        self.assertLess(release.index('cd "$PERSONA_RELEASE_SOURCE"'), release.index('python3 scripts/persona_config.py xcconfig'))

    def test_windows_requires_native_execution_and_authenticode(self):
        text = PR.read_text()
        for program in ("persona-link", "personad", "persona-node"):
            self.assertIn(program + "-windows-amd64.exe.gz", text)
        self.assertIn("PERSONA_DESKTOP_REQUIRE_SIGNING", text)
        self.assertIn("Get-AuthenticodeSignature", text)
        self.assertIn("beta.yml", text)
        self.assertIn("latest.yml", text)
        # Native Windows checks must still run when Apple signing fails. Apple is a separate dependency of the
        # final aggregate, not an input to the Windows executables or desktop package.
        windows = self.pr["jobs"]["windows"]
        self.assertNotIn("apple", windows["needs"])
        self.assertNotIn("release-pr-apple", str(windows))
        self.assertEqual(self.pr["jobs"]["verify"]["needs"], ["plan", "cli-web", "android", "apple", "windows"])

    def test_pr_metadata_survives_the_clean_source_checkout(self):
        text = PR.read_text()
        self.assertIn("PR_JSON: ${{ runner.temp }}/release-pr.json", text)
        self.assertIn("PR_LIST_JSON: ${{ runner.temp }}/release-pr-list.json", text)
        self.assertIn("PR_REST_JSON: ${{ runner.temp }}/release-pr-rest.json", text)
        self.assertNotIn("json.load(open(\"release-pr.json\"))", text)
        self.assertNotIn("Path('release-pr.json')", text)
        self.assertNotIn("> release-pr.json", text)
        self.assertNotIn("> release-pr-list.json", text)
        self.assertNotIn("> release-pr-rest.json", text)
        self.assertIn('verify-pr --pr-json "$PR_JSON"', text)
        self.assertIn("GIT_COMMIT: ${{ github.event.pull_request.merge_commit_sha || github.sha }}", text)
        self.assertIn("COMMIT=\"$commit\" python3", text)
        self.assertIn("--json number,title,state,isDraft,baseRefName", text)
        self.assertNotIn("open(os.environ['PR_JSON'])", text)
        self.assertIn('json.load(open(sys.argv[1]))', text)

    def test_main_without_a_merged_release_pr_has_no_build_plan(self):
        text = PR.read_text()
        self.assertIn("Path(os.environ['PR_JSON']).write_text('{}')", text)
        self.assertNotIn("raise SystemExit(78)", text)
        self.assertIn("steps.source.outputs.number != '0'", str(self.pr["jobs"]["plan"]["steps"]))
        for name, job in self.pr["jobs"].items():
            if name != "plan":
                self.assertIn("needs.plan.outputs.version != ''", str(job["if"]), name)

    def test_merge_publication_is_protected_and_binds_the_actual_merge(self):
        text = PUBLISH.read_text()
        self.assertIn("github.event.workflow_run.conclusion == 'success'", text)
        self.assertIn("github.event.workflow_run.head_sha", text)
        self.assertIn('git fetch --no-tags origin main', text)
        self.assertIn('git merge-base --is-ancestor "$COMMIT" FETCH_HEAD', text)
        self.assertNotIn('refs/heads/main | cut -f1', text)
        self.assertIn("release/merged-pr.json", text)
        self.assertIn("mergeCommit", text)
        self.assertIn("github.actor == 'chyroc'", text)
        self.assertIn("github.triggering_actor == 'chyroc'", text)
        self.assertIn("verify-merged-pr", text)
        self.assertIn("github.event.workflow_run.head_branch == 'main'", text)
        self.assertEqual(self.publish["jobs"]["authorize"]["environment"], "persona-release")
        self.assertEqual(self.publish["jobs"]["cli-web"]["environment"], "persona-release")
        source = self.publish["jobs"]["source"]
        self.assertNotIn("environment", source)
        self.assertIn("release:", str(source))
        self.assertIn("needs.source.outputs.pr != ''", self.publish["jobs"]["authorize"]["if"])
        self.assertIn("needs.source.outputs.pr", str(self.publish["jobs"]["authorize"]["outputs"]))
        self.assertNotIn("pull_request_target:", text)

    def test_immutable_tag_and_download_verification_precede_beta_pointer(self):
        text = PUBLISH.read_text()
        self.assertIn("refs/tags/$TAG^{}", text)
        self.assertIn("refusing to move or replace it", text)
        self.assertIn("Independently download every uploaded artifact", text)
        self.assertLess(text.index("independent_download_verified"), text.index("beta_channel_release.py"))
        self.assertIn("check_windows_artifacts", text)
        self.assertIn("check_android_index", text)
        self.assertNotIn("--no-verify", text)

    def test_schedule_opens_only_release_prs_and_never_publishes_directly(self):
        text = SCHEDULE.read_text()
        self.assertIn("- cron: '0 4 * * *'", text)
        self.assertIn("- cron: '0 */2 * * *'", text)
        self.assertNotIn("*/2 * * * *", text)
        self.assertIn("RELEASE_PR_TOKEN", text)
        self.assertIn("environment: persona-release", text)
        self.assertIn("create_release_pr.py", text)
        self.assertIn("RELEASE_PR_TOKEN", text)
        self.assertIn("missing RELEASE_PR_TOKEN", text)
        self.assertIn("git diff --cached --quiet", text)
        schedule = yaml.load(text, Loader=yaml.BaseLoader)
        self.assertEqual(schedule["concurrency"]["group"], "release-schedule-${{ inputs.channel || 'automatic' }}")
        self.assertEqual(schedule["concurrency"]["cancel-in-progress"], "${{ github.event_name == 'schedule' }}")
        self.assertEqual(schedule["jobs"]["plan"]["needs"], "channel")
        self.assertIn("release_candidate.py", text)
        select = next(step for step in schedule["jobs"]["plan"]["steps"]
                      if step.get("id") == "select")["run"]
        self.assertLess(select.index("release_candidate.py"), select.index("git fetch --no-tags origin main"))
        self.assertIn("An open release PR already owns this channel", select)
        self.assertNotIn("gh pr close", text)
        self.assertNotIn("gh pr edit", text)
        for step in schedule["jobs"]["plan"]["steps"]:
            if "env" in step and "CHANNEL" in step["env"]:
                self.assertEqual(step["env"]["CHANNEL"], "${{ needs.channel.outputs.name }}")
        channel_script = schedule["jobs"]["channel"]["steps"][0]["run"]
        self.assertIn('channel="$REQUESTED"', channel_script)
        self.assertIn('[[ "$EVENT" = schedule ]]', channel_script)
        self.assertNotIn("inputs.channel || (github.event.schedule", text)
        self.assertIn("git config user.email", text)
        self.assertIn('git switch -C "$BRANCH"', text)
        self.assertIn('candidate="$branch-r$suffix"', text)
        select = next(step for step in yaml.load(text, Loader=yaml.BaseLoader)["jobs"]["plan"]["steps"]
                      if step.get("id") == "select")["run"]
        self.assertLess(select.index("git fetch --no-tags origin main"), select.index('commit="$(git rev-parse HEAD)"'))
        self.assertLess(select.index("git switch --detach FETCH_HEAD"), select.index('commit="$(git rev-parse HEAD)"'))
        self.assertNotIn("gh pr create", text)
        self.assertIn("refs/heads/$branch", text)
        self.assertIn("preserve its failed source", text)
        self.assertIn("release-reserved.txt", text)
        self.assertIn("--reserved", text)
        self.assertLess(text.index('refs/heads/release/*'), text.index('schedule-version'))
        self.assertIn('releases/latest', text)
        self.assertIn('pulls?state=open&base=main&per_page=100" --paginate > release-open-prs.json', text)
        self.assertIn('pulls?state=open&base=main&per_page=100" --paginate > release-open-prs-current.json', text)
        self.assertIn('cp scripts/operations/release_candidate.py scripts/operations/release_pr.py "$RUNNER_TEMP/release-ops/"', text)
        self.assertLess(
            select.index('git switch --detach FETCH_HEAD'),
            select.index('cp scripts/operations/release_candidate.py scripts/operations/release_pr.py "$RUNNER_TEMP/release-ops/"'),
        )
        self.assertLess(
            select.index('cp scripts/operations/release_candidate.py scripts/operations/release_pr.py "$RUNNER_TEMP/release-ops/"'),
            select.index('git switch --detach "$green_commit"'),
        )
        self.assertIn('"$RUNNER_TEMP/release-ops/release_candidate.py" --repository "$REPOSITORY" --channel "$CHANNEL" --pulls release-open-prs-current.json', text)
        self.assertIn('"$RUNNER_TEMP/release-ops/release_candidate.py" --repository "$REPOSITORY" --branch "$branch" --pulls release-open-prs-current.json', text)
        self.assertNotIn('scripts/operations/release_candidate.py --repository "$REPOSITORY" --branch', text)
        self.assertNotIn("--jq --arg", text)
        self.assertNotIn("--force", text)
        self.assertNotIn('refs/tags/$TAG', text)
        self.assertNotIn("gh release create", text)

    def test_schedule_replacement_candidates_keep_the_old_source_and_allocate_a_fresh_branch(self):
        schedule = yaml.load(SCHEDULE.read_text(), Loader=yaml.BaseLoader)
        lines = schedule["jobs"]["plan"]["steps"][1]["run"].splitlines()
        start = next(index for index, line in enumerate(lines) if line.strip() == "suffix=0")
        end = next(index for index in range(start, len(lines)) if lines[index].strip() == 'branch="$candidate"')
        fragment = "\n".join(lines[start:end + 1]) + '\nprintf \'%s\\n\' "$branch"\n'
        for refs, expected in (("", "release/0.10.0"),
                               ("a" * 40 + "\trefs/heads/release/0.10.0\n", "release/0.10.0-r1"),
                               ("a" * 40 + "\trefs/heads/release/0.10.0\n"
                                + "b" * 40 + "\trefs/heads/release/0.10.0-r1\n", "release/0.10.0-r2")):
            with self.subTest(branch=expected), tempfile.TemporaryDirectory() as directory:
                fixture = Path(directory)
                fake = fixture / "git"
                fake.write_text("#!/bin/sh\n"
                                "[ \"$1\" = ls-remote ] || exit 2\n"
                                "case \"$REFS\" in *\"$4\"*) exit 0 ;; *) exit 1 ;; esac\n")
                fake.chmod(0o755)
                result = subprocess.run(["bash", "-c", fragment], cwd=fixture,
                                        env=dict(os.environ, PATH=str(fixture) + os.pathsep + os.environ["PATH"],
                                                 branch="release/0.10.0", REFS=refs, REPOSITORY="nwverse/persona"), capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), expected)
                # A failed candidate's old ref is never overwritten: -rN advances until no remote branch owns it.

    def test_schedule_reserved_versions_are_safe_and_remain_separate_arguments(self):
        text = SCHEDULE.read_text()
        schedule = yaml.load(text, Loader=yaml.BaseLoader)
        lines = schedule["jobs"]["plan"]["steps"][1]["run"].splitlines()
        first = next(index for index, line in enumerate(lines) if "version_command=(python3" in line)
        end = next(index for index in range(first, len(lines)) if lines[index].strip() == 'version="$("${version_command[@]}")"')
        fragment = "\n".join(lines[first:end + 1])
        prefix = ["scripts/operations/release_pr.py", "--repository", "nwverse/persona", "--channel", "beta",
                  "schedule-version", "--stable-tag", "v0.9.1", "--commit", "c" * 40, "--previous-commit",
                  "d" * 40, "--subjects", "release-subjects.txt", "--reserved"]
        for name, refs, expected in (("multiple", "0.9.1-beta.9\n0.9.1-beta.10\n", ["0.9.1-beta.9", "0.9.1-beta.10"]),
                                     ("unterminated", "0.9.1-beta.9\n0.9.1-beta.10", ["0.9.1-beta.9", "0.9.1-beta.10"]),
                                     ("empty", "", [])):
            with self.subTest(case=name), tempfile.TemporaryDirectory() as directory:
                fixture = Path(directory)
                (fixture / "release-reserved.txt").write_text(refs)
                (fixture / "release-subjects.txt").write_text("fix: example\n")
                python = fixture / "python3"
                python.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PYTHON_ARGV_LOG\"\nprintf '0.9.1-beta.11\\n'\n")
                python.chmod(0o755)
                argv_log = fixture / "python-argv.txt"
                env = {"PATH": str(fixture) + os.pathsep + os.environ["PATH"], "PYTHON_ARGV_LOG": str(argv_log),
                       "REPOSITORY": "nwverse/persona", "CHANNEL": "beta", "stable": "v0.9.1",
                       "commit": "c" * 40, "previous": "d" * 40}
                result = subprocess.run(["bash", "-c", "set -euo pipefail\n" + fragment], cwd=fixture, env=env,
                                        capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(argv_log.read_text().splitlines(), prefix + expected)
        reserved_command = next(line for line in lines if "--reserved" in line)
        self.assertNotRegex(reserved_command, r"--reserved\s+\$\(",
                            "reserved versions must not use an unquoted command substitution (SC2046)")

    def test_verified_pr_merges_then_main_is_reverified_before_publication(self):
        text = PR.read_text()
        self.assertIn("merge:\n    name: Merge the verified release PR", text)
        self.assertIn("needs: [plan, verify]", text)
        self.assertIn("needs.plan.outputs.source_commit", text)
        self.assertIn("--match-head-commit \"$HEAD\"", text)
        merge = self.pr["jobs"]["merge"]
        self.assertEqual(merge["environment"], "persona-release")
        self.assertEqual(merge["if"], "needs.plan.outputs.pr != '0' && needs.plan.outputs.version != '' && github.event_name == 'pull_request'")
        self.assertEqual(merge["steps"][1]["env"]["GH_TOKEN"], "${{ secrets.RELEASE_PR_TOKEN }}")
        self.assertIn("missing RELEASE_PR_TOKEN", merge["steps"][1]["run"])
        self.assertIn('merged_by != "chyroc"', (ROOT / 'scripts/operations/release_pr.py').read_text())
        self.assertIn("github.event.workflow_run.conclusion == 'success'", PUBLISH.read_text())
        self.assertIn("github.event.workflow_run.head_branch == 'main'", PUBLISH.read_text())
        self.assertIn("verify-merged-pr", PUBLISH.read_text())
        self.assertIn("Create the immutable tag only after the merged source passed every platform check", PUBLISH.read_text())
        tag = next(step for step in self.publish["jobs"]["authorize"]["steps"]
                   if step.get("name") == "Create the immutable tag only after the merged source passed every platform check")
        self.assertEqual(tag["env"]["GH_TOKEN"], "${{ secrets.RELEASE_PR_TOKEN }}")
        self.assertIn("missing RELEASE_PR_TOKEN", tag["run"])
        marker = next(step for step in self.publish["jobs"]["authorize"]["steps"]
                      if step.get("name") == "Mark the Release as owned by this verified release PR")
        self.assertEqual(marker["env"]["GH_TOKEN"], "${{ secrets.RELEASE_PR_TOKEN }}")

    def test_review_regressions_checkout_sources_and_upload_order(self):
        pr = PR.read_text()
        publish = PUBLISH.read_text()
        self.assertIn('ref: ${{ needs.plan.outputs.commit }}\n          fetch-depth: 0', pr)
        self.assertIn('ref: ${{ github.event.workflow_run.head_sha || github.sha }}', publish)
        self.assertLess(publish.index('actions/checkout@v4'), publish.index('verify-merged-pr'))
        self.assertLess(pr.index('actions/checkout@v4', pr.index('name: All available release-PR checks passed')), pr.index('verify-artifacts --plan', pr.index('name: All available release-PR checks passed')))
        self.assertIn('GITHUB_WORKSPACE', pr)
        self.assertIn('ci_release_config.py public', pr)
        self.assertIn('releases/latest/download', (ROOT / 'scripts/operations/ci_release_config.py').read_text())
        self.assertIn('targetCommitish', publish)
        self.assertLess(publish.index('for name in VERSION SHA256SUMS android.json latest.json'), publish.index('Independently download every uploaded artifact'))
        self.assertLess(publish.index('Wait for the tag-triggered container and Release workflow'), publish.index('Upload verified downloads'))
        self.assertIn('github.event.workflow_run.actor.login', publish)
        self.assertIn('PERSONA_APPLE_DISTRIBUTION_P12_BASE64', pr)
        self.assertIn('PERSONA_APPLE_PROFILE_6_BASE64', pr)

    def test_old_darwin_job_cannot_overwrite_release_pr_artifacts(self):
        text = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('release-pr: [0-9]+', text)
        self.assertIn("steps.owner.outputs.release_pr == 'false'", text)
        self.assertIn('release-publish owns its exact artifacts', text)
        self.assertIn('for name in VERSION SHA256SUMS android.json latest.json', PUBLISH.read_text())

    def test_stable_latest_promotion_follows_independent_artifact_verification(self):
        text = PUBLISH.read_text()
        self.assertLess(text.index('Independently download every uploaded artifact'), text.index('stable_release.py'))
        self.assertLess(text.index('stable_release.py'), text.index('beta_channel_release.py'))
        stable = next(step for step in self.publish['jobs']['cli-web']['steps']
                      if step.get('name') == 'Advance only the verified stable Latest pointer')
        self.assertEqual(stable['if'], "needs.authorize.outputs.channel == 'release'")
        self.assertIn('--manifest release/downloads/latest.json', stable['run'])

    def test_schedule_pins_newest_full_green_ci_commit_and_keeps_merge_vs_target_distinct(self):
        schedule_text = SCHEDULE.read_text()
        schedule = yaml.load(schedule_text, Loader=yaml.BaseLoader)
        self.assertEqual(schedule["permissions"]["actions"], "read")
        select = next(step for step in schedule["jobs"]["plan"]["steps"]
                      if step.get("id") == "select")
        self.assertEqual(select["env"]["ACTIONS_TOKEN"], "${{ secrets.GITHUB_TOKEN }}")
        self.assertIn("scripts/operations/select_green_commit.py", select["run"])
        self.assertIn('GH_TOKEN="$ACTIONS_TOKEN" python3 scripts/operations/select_green_commit.py', select["run"])
        self.assertIn('git switch --detach "$green_commit"', select["run"])
        self.assertLess(select["run"].index("select_green_commit.py"), select["run"].index('git switch --detach "$green_commit"'))
        self.assertLess(select["run"].index('git switch --detach "$green_commit"'), select["run"].index('\ncommit="$(git rev-parse HEAD)"'))
        self.assertIn("No newer full green CI commit found on main", select["run"])
        self.assertIn("'ci_run_id':int(os.environ['CI_RUN_ID'])", schedule_text)

        pr_text = PR.read_text()
        self.assertEqual(self.pr["jobs"]["plan"]["outputs"]["merge_commit"], "${{ steps.plan.outputs.merge_commit }}")
        self.assertIn("resolve-target --dir .github/release-prs", pr_text)
        merge_check = next(step for step in self.pr["jobs"]["plan"]["steps"]
                           if step.get("id") == "merge-check")
        self.assertEqual(merge_check["env"]["COMMIT"], "${{ steps.plan.outputs.merge_commit }}")
        self.assertIn('--merge-commit "$MERGE_COMMIT"', pr_text)
        self.assertIn("--merge-commit '${{ needs.plan.outputs.merge_commit }}'", pr_text)

        publish_text = PUBLISH.read_text()
        self.assertEqual(self.publish["jobs"]["authorize"]["outputs"]["merge_commit"], "${{ steps.source.outputs.merge_commit }}")
        self.assertIn("resolve-target --dir .github/release-prs", publish_text)
        self.assertIn('verify-merged-pr --pr-json release/merged-pr.json --commit "$MERGE_COMMIT"', publish_text)
        self.assertIn('git merge-base --is-ancestor "$MERGE_COMMIT" FETCH_HEAD', publish_text)
        self.assertIn('git merge-base --is-ancestor "$COMMIT" "$MERGE_COMMIT"', publish_text)
        self.assertIn('verify-artifacts --plan release/plan.json --commit "$COMMIT" --merge-commit "$MERGE_COMMIT"', publish_text)

    def test_workflows_lint(self):
        result = subprocess.run(["actionlint", str(PR), str(PUBLISH), str(SCHEDULE)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
