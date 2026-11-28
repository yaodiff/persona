import importlib.util
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
SPEC = importlib.util.spec_from_file_location("android_shards", HERE / "android-shards.py")
shards = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(shards)
SETTINGS = (ROOT / "android/settings.gradle.kts").read_text()
CI_LANES = shards.LANES[1:]


def five_lanes(source):
    """The five lanes CI ran before #3056; the two lanes must run exactly their tasks."""
    projects = shards.modules(source)
    features = [project + ":test" for project in projects if project.startswith(":feature:")]
    return {
        "lint-build": ["lint", ":app:assembleDebug", ":app:moduleGuard"],
        "test-core": [project + ":test" for project in projects if project.startswith(":core:")],
        "test-feature-0": features[0::2],
        "test-feature-1": features[1::2],
        "test-platform-app": [project + ":test" for project in projects
                              if not project.startswith((":core:", ":feature:"))],
    }


class ShardTests(unittest.TestCase):
    def test_two_lanes_run_exactly_the_five_lanes_tasks(self):
        for source in (SETTINGS, 'include(":app", ":core:new", ":feature:a", ":feature:b", ":feature:c", ":platform:new", ":new")'):
            with self.subTest(source=source[:40]):
                selected = [task for lane in CI_LANES for task in shards.tasks(source, lane)]
                old = [task for lane in five_lanes(source).values() for task in lane]
                self.assertEqual(len(selected), len(set(selected)))
                self.assertEqual(sorted(selected), sorted(old))

    def test_every_test_lint_and_build_task_lands_in_exactly_one_lane(self):
        projects = shards.modules(SETTINGS)
        lanes = {lane: shards.tasks(SETTINGS, lane) for lane in CI_LANES}
        self.assertEqual(len(lanes), 2)
        for task in [project + ":test" for project in projects] + ["lint", ":app:assembleDebug", ":app:moduleGuard"]:
            self.assertEqual(sum(task in selected for selected in lanes.values()), 1, task)
        self.assertTrue(all(lanes.values()))
        self.assertIn(":core:contracts:test", lanes["test-1"])  # JVM test lifecycle stays covered.
        self.assertIn(":app:test", lanes["test-1"])  # Debug and Release, not testDebugUnitTest.
        self.assertIn("lint", lanes["lint-build-test-0"])
        self.assertIn(":module:voice:test", lanes["lint-build-test-0"])  # The modules' own tests (#13433).
        self.assertIn(":app:moduleGuard", lanes["lint-build-test-0"])

    def test_literals_multiline_comments_duplicates_and_included_builds(self):
        source = '''
        includeBuild("build-logic")
        // include(":ignored")
        /* outer /* include(":ignored:too") */ comment */
        val text = "include(\\\":ignored\\\") // not a comment"
        val raw = """include(":ignored:raw")"""
        include(":app", /* inline */ ":core:domain",)
        include(
            ":feature:new", ":core:domain"
        )
        '''
        self.assertEqual(shards.modules(source), [":app", ":core:domain", ":feature:new"])

    def test_new_modules_and_nested_paths_have_a_fallback(self):
        source = 'include(":core:new", ":feature:new:deep", ":platform:new", ":new", ":core", ":module:new")'
        self.assertEqual(shards.tasks(source, "lint-build-test-0"),
                         ["lint", ":app:assembleDebug", ":app:moduleGuard", ":feature:new:deep:test", ":module:new:test"])
        self.assertEqual(shards.tasks(source, "test-1"), [":core:new:test", ":core:test", ":new:test", ":platform:new:test"])

    def test_full_and_lint_keep_original_all_module_selectors(self):
        self.assertEqual(shards.tasks(SETTINGS, "full"), ["lint", "test", ":app:assembleDebug", ":app:moduleGuard"])
        self.assertEqual(shards.tasks(SETTINGS, "lint-build-test-0")[:3], ["lint", ":app:assembleDebug", ":app:moduleGuard"])
        self.assertEqual(shards.tasks('include(":app")', "lint-build-test-0"), ["lint", ":app:assembleDebug", ":app:moduleGuard"])

    def test_computed_or_malformed_declarations_fail_closed(self):
        for source in ('include(projects)', 'include(":core:$name")', 'include(":core:" + name)',
                       'include(":a" ":b")', 'include()', 'include(":a",', 'include(":../bad")',
                       'includeBuild("build-logic")', '/* unterminated', '"""unterminated',
                       'listOf(":new").forEach(::include)', 'include(":a"); include(extra)'):
            with self.subTest(source=source), self.assertRaises(ValueError):
                shards.modules(source)
        with self.assertRaises(ValueError):
            shards.tasks(SETTINGS, "typo")

    def test_cli_settings_errors_are_nonzero_without_task_output(self):
        result = subprocess.run(["python3", "-B", str(HERE / "android-shards.py"),
                                 "--settings", str(HERE / "missing-settings.gradle.kts"),
                                 "--lane", "test-1"], capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("FAILED", result.stderr)

    def test_workflow_lanes_and_cache_write_policy(self):
        workflow = (ROOT / ".github/workflows/android-test.yml").read_text()
        self.assertIn("permissions:\n  actions: read\n  contents: read", workflow)
        for caller in ("ci.yml", "release-pr.yml"):
            with self.subTest(caller=caller):
                source = (ROOT / ".github/workflows" / caller).read_text()
                self.assertRegex(source, r"(?m)^permissions:\n  actions: read\n  contents: read\n")
        match = re.search(r"lane: \[([^\]]+)\]", workflow)
        self.assertIsNotNone(match)
        self.assertEqual([value.strip() for value in match[1].split(",")], list(CI_LANES))
        self.assertIn("fail-fast: false", workflow)
        self.assertIn("--lane '${{ matrix.lane }}' --profile", workflow)
        self.assertIn("id: checks", workflow)
        self.assertIn("id: evidence", workflow)
        self.assertIn("CHECKS_OUTCOME: ${{ steps.checks.outcome }}", workflow)
        self.assertIn("EVIDENCE_OUTCOME: ${{ steps.evidence.outcome }}", workflow)
        self.assertIn("scripts/ci/android_result.py bind", workflow)
        self.assertIn("cache-read-only: ${{ github.event_name != 'workflow_dispatch' || github.ref != 'refs/heads/main' }}", workflow)
        restore = workflow.split("- name: Restore public Robolectric runtime dependencies", 1)[1].split("- name:", 1)[0]
        save = workflow.split("- name: Save public Robolectric runtime dependencies", 1)[1].split("- name: Upload", 1)[0]
        # Both lanes run unit tests: neither skips the Robolectric cache, and a new lane name still restores an entry.
        self.assertNotIn("matrix.lane !=", restore + save)
        self.assertIn("${{ runner.os }}-robolectric-v1-${{ matrix.lane }}-\n            ${{ runner.os }}-robolectric-v1-\n", restore)
        self.assertIn("github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'", save)
        self.assertIn("actions/cache/save@v4", save)
        cached_paths = re.findall(r"^\s+(~/.m2/\S+)$", workflow, re.M)
        self.assertEqual(set(cached_paths), {"~/.m2/repository/org/robolectric/android-all",
                                            "~/.m2/repository/org/robolectric/android-all-instrumented",
                                            "~/.m2/repository/org/robolectric/nativeruntime-dist-compat"})
        # Both steps include the native runtime: Maven Central's HTTP 429 must not make each uncached run fail (#14693).
        for step in (restore, save):
            self.assertIn("~/.m2/repository/org/robolectric/nativeruntime-dist-compat", step)
        self.assertNotIn("continue-on-error", workflow)
        self.assertIn("timeout-minutes: 90", workflow)
        aggregate = workflow.split("\n  result:\n", 1)[1]
        self.assertIn("needs: android", aggregate)
        self.assertIn("if: always()", aggregate)
        self.assertIn("gh run download", aggregate)
        self.assertIn("--pattern", aggregate)
        self.assertIn("GH_TOKEN: ${{ github.token }}", aggregate)
        self.assertNotIn("continue-on-error", aggregate)
        self.assertIn("scripts/ci/android_result.py verify", aggregate)
        self.assertIn("name: android-source-result-${{ steps.source.outputs.commit }}-${{ github.run_id }}-${{ github.run_attempt }}", aggregate)
        self.assertIn("if-no-files-found: error", aggregate)


class ShellLaneTests(unittest.TestCase):
    """Mock toolchain checks only; these tests do not claim an Android build passed."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="persona-android-shards-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for directory in ("scripts/ci", "scripts/app", "android/gradle/wrapper", "jdk/bin",
                          "sdk/platforms/android-37.0", "sdk/build-tools/37.0.0", "sdk/ndk/29.0.14206865"):
            (self.root / directory).mkdir(parents=True, exist_ok=True)
        for name in ("android.sh", "android-shards.py", "common.sh"):
            shutil.copyfile(HERE / name, self.root / "scripts/ci" / name)
        # The shared Rust core's pinned toolchain (core:rustcore's tests build it), with a stand-in rustup.
        (self.root / "native/app-core").mkdir(parents=True)
        (self.root / "native/app-core/rust-toolchain.toml").write_text('[toolchain]\nchannel = "1.98.0"\n')
        (self.root / "bin").mkdir()
        rustup = self.root / "bin/rustup"
        rustup.write_text('#!/usr/bin/env bash\nprintf \'%s\\n\' "$*" >> "$MOCK_RUSTUP_LOG"\n'
                          'if [[ "$1 $2 $3" == "target list --installed" ]]; then printf \'aarch64-linux-android\\nx86_64-linux-android\\n\'; fi\n')
        rustup.chmod(0o700)
        (self.root / "scripts/ci/test_android_shards.py").write_text("pass\n")
        # Lane 0's real-hub driver is outside this mock toolchain test; record its invocation and sanitized evidence.
        (self.root / "scripts/ci/android-core-only-hub.py").write_text(
            "import os, pathlib, sys\n"
            "pathlib.Path(os.environ['MOCK_ANDROID_HUB_LOG']).write_text(' '.join(sys.argv[1:]) + '\\n')\n"
            "out = pathlib.Path(sys.argv[sys.argv.index('--evidence') + 1])\n"
            "out.parent.mkdir(parents=True, exist_ok=True)\n"
            "out.write_text('{}\\n')\n"
            "print('PASS Android hub editions: mock toolchain')\n")
        self.hub_log = self.root / "android-hub-driver-arguments"
        for name in ("test_refract_about_version.py", "test_refract_about_build.py", "test_android_i18n_check.py", "test_android_token_baseline.py",
                     "test_android_motion_baseline.py", "test_android_slot_order.py", "android_i18n_check.py",
                     "android_token_baseline.py", "android_motion_baseline.py", "android_slot_order.py",
                     "test_android_compose_budget.py", "test_android_nested_throwables.py",
                     "android_compose_budget.py", "android_nested_throwables.py"):
            (self.root / "scripts/app" / name).write_text("pass\n")
        (self.root / "scripts/app/refract_about_version.py").write_text("pass\n")  # pinned About Version regeneration (#9694)
        (self.root / "scripts/app/refract_about_build.py").write_text("pass\n")  # independent Build probe (#13001)
        (self.root / "scripts/assets").mkdir()
        (self.root / "scripts/assets/gen_tokens.py").write_text("pass\n")  # the generated-tokens check (#4209)
        (self.root / "scripts/assets/gen_icon_meanings.py").write_text("pass\n")  # the icon meanings check (#4209)
        (self.root / "scripts/test").mkdir()
        # The client domain guard's Android scan (docs/server/architecture.md "Domain model", 'Client modules').
        (self.root / "scripts/test/test_client_domain_modules.py").write_text("pass\n")
        (self.root / "android/settings.gradle.kts").write_text(SETTINGS)
        (self.root / "android/gradle/libs.versions.toml").write_text('[versions]\njdk = "21"\ncompileSdk = "37"\nbuildTools = "37.0.0"\n')
        (self.root / "android/gradle/wrapper/gradle-wrapper.jar").touch()
        (self.root / "sdk/platforms/android-37.0/android.jar").touch()
        java = self.root / "jdk/bin/java"
        java.write_text('#!/usr/bin/env bash\nprintf \'openjdk version "21"\\n\' >&2\n')
        java.chmod(0o700)
        shutil.copyfile(java, self.root / "jdk/bin/javac")
        (self.root / "jdk/bin/javac").chmod(0o700)
        (self.root / "android/gradlew").write_text('#!/usr/bin/env bash\nprintf \'%s\\n\' "$@" "--end-of-call--" >> "$MOCK_GRADLE_LOG"\nexit "${MOCK_GRADLE_STATUS:-0}"\n')
        self.log = self.root / "gradle-arguments"
        self.env = dict(os.environ, JAVA_HOME=str(self.root / "jdk"),
                        ANDROID_HOME=str(self.root / "sdk"), ANDROID_SDK_ROOT=str(self.root / "sdk"),
                        ANDROID_CI_TIMEOUT_SECONDS="10", ANDROID_CI_MAX_WORKERS="2",
                        MOCK_GRADLE_LOG=str(self.log), MOCK_RUSTUP_LOG=str(self.root / "rustup-arguments"),
                        MOCK_ANDROID_HUB_LOG=str(self.hub_log),
                        PATH=str(self.root / "bin") + os.pathsep + os.environ.get("PATH", ""))

    def calls(self):
        """Each Gradle invocation's arguments, in order (the mock appends one record per call)."""
        out, current = [], []
        for line in self.log.read_text().splitlines():
            if line == "--end-of-call--":
                out.append(current)
                current = []
            else:
                current.append(line)
        return out

    def run_lane(self, *args):
        self.log.unlink(missing_ok=True)
        self.hub_log.unlink(missing_ok=True)
        return subprocess.run(["bash", str(self.root / "scripts/ci/android.sh"), *args],
                              env=self.env, capture_output=True, text=True, timeout=30)

    def test_default_full_and_every_lane_use_expected_tasks(self):
        for lane in shards.LANES:
            with self.subTest(lane=lane):
                result = self.run_lane(*([] if lane == "full" else ["--lane", lane]))
                self.assertEqual(result.returncode, 0, result.stderr)
                calls = self.calls()
                arguments = next(call for call in calls if "-Ppersona.releaseUnitTests=true" in call)
                expected = shards.tasks(SETTINGS, lane)
                self.assertEqual(arguments[-len(expected):], expected)
                # Persona Core alone (#13433): the app's unit tests and lint with an empty module list, in the lint lane.
                alone = [call for call in calls if "-Ppersona.modules=" in call]
                if lane in ("full", "lint-build-test-0"):
                    self.assertEqual(len(alone), 1, calls)
                    self.assertEqual(alone[0][-2:], [":app:testDebugUnitTest", ":app:lintDebug"])
                    self.assertIn("--max-workers=2", alone[0])
                else:
                    self.assertEqual(alone, [])
                self.assertIn("--max-workers=2", arguments)
                # Each module's test runs its release unit tests too, which a local build leaves out (#10712).
                self.assertIn("-Ppersona.releaseUnitTests=true", arguments)
                self.assertNotIn("--profile", arguments)
                self.assertEqual("translation checker tests" in result.stdout, lane in ("full", "lint-build-test-0"))
                self.assertEqual("Refract About Version checker tests" in result.stdout,
                                 lane in ("full", "lint-build-test-0"))
                self.assertEqual("Refract About Build checker tests" in result.stdout,
                                 lane in ("full", "lint-build-test-0"))
                # Motion ratchet + its unit tests run in the same lint/build lane as the token checks (#9897).
                lintish = lane in ("full", "lint-build-test-0")
                self.assertEqual("motion checker tests" in result.stdout, lintish)
                self.assertEqual("motion literal baseline" in result.stdout, lintish)
                self.assertEqual("Android client on core-only and full hubs" in result.stdout, lintish)
                self.assertEqual(self.hub_log.exists(), lintish)
                if lintish:
                    self.assertIn("--android-dir", self.hub_log.read_text())
                    self.assertIn("--evidence", self.hub_log.read_text())
                self.assertIn("run 1.98.0 cargo --version", (self.root / "rustup-arguments").read_text())

    def test_profile_and_gradle_failure_are_preserved(self):
        self.env["MOCK_GRADLE_STATUS"] = "1"
        result = self.run_lane("--profile", "--lane", "test-1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--profile", self.log.read_text().splitlines())
        self.assertIn("FAILED (Gradle test-1", result.stderr)
        self.env["MOCK_GRADLE_STATUS"] = "124"
        result = self.run_lane("--lane", "test-1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("TIMEOUT", result.stderr)

    def test_invalid_lane_and_parse_failure_never_launch_gradle(self):
        for args in (["--lane"], ["--lane", "typo"], ["--lane", "test-core"], ["--typo"]):
            self.assertEqual(self.run_lane(*args).returncode, 2)
        (self.root / "android/settings.gradle.kts").write_text('include(projects)')
        result = self.run_lane("--lane", "test-1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Cannot enumerate", result.stderr)
        self.assertFalse(self.log.exists())

    def test_empty_lane_never_runs_default_gradle_tasks(self):
        (self.root / "android/settings.gradle.kts").write_text('include(":feature:only")')
        result = self.run_lane("--lane", "test-1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no modules", result.stdout)
        self.assertFalse(self.log.exists())


if __name__ == "__main__":
    unittest.main()
