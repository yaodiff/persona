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
        "lint-build": ["lint", ":app:assembleDebug"],
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
        for task in [project + ":test" for project in projects] + ["lint", ":app:assembleDebug"]:
            self.assertEqual(sum(task in selected for selected in lanes.values()), 1, task)
        self.assertTrue(all(lanes.values()))
        self.assertIn(":core:contracts:test", lanes["test-1"])  # JVM test lifecycle stays covered.
        self.assertIn(":app:test", lanes["test-1"])  # Debug and Release, not testDebugUnitTest.
        self.assertIn("lint", lanes["lint-build-test-0"])

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
        source = 'include(":core:new", ":feature:new:deep", ":platform:new", ":new", ":core")'
        self.assertEqual(shards.tasks(source, "lint-build-test-0"), ["lint", ":app:assembleDebug", ":feature:new:deep:test"])
        self.assertEqual(shards.tasks(source, "test-1"), [":core:new:test", ":core:test", ":new:test", ":platform:new:test"])

    def test_full_and_lint_keep_original_all_module_selectors(self):
        self.assertEqual(shards.tasks(SETTINGS, "full"), ["lint", "test", ":app:assembleDebug"])
        self.assertEqual(shards.tasks(SETTINGS, "lint-build-test-0")[:2], ["lint", ":app:assembleDebug"])
        self.assertEqual(shards.tasks('include(":app")', "lint-build-test-0"), ["lint", ":app:assembleDebug"])

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
        match = re.search(r"lane: \[([^\]]+)\]", workflow)
        self.assertIsNotNone(match)
        self.assertEqual([value.strip() for value in match[1].split(",")], list(CI_LANES))
        self.assertIn("fail-fast: false", workflow)
        self.assertIn("--lane '${{ matrix.lane }}' --profile", workflow)
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
                                            "~/.m2/repository/org/robolectric/android-all-instrumented"})
        self.assertNotIn("continue-on-error", workflow)


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
        for name in ("test_android_i18n_check.py", "test_android_token_baseline.py", "test_android_slot_order.py",
                     "android_i18n_check.py", "android_token_baseline.py", "android_slot_order.py"):
            (self.root / "scripts/app" / name).write_text("pass\n")
        (self.root / "scripts/assets").mkdir()
        (self.root / "scripts/assets/gen_tokens.py").write_text("pass\n")  # the generated-tokens check (#4209)
        (self.root / "scripts/assets/gen_icon_meanings.py").write_text("pass\n")  # the icon meanings check (#4209)
        (self.root / "android/settings.gradle.kts").write_text(SETTINGS)
        (self.root / "android/gradle/libs.versions.toml").write_text('[versions]\njdk = "21"\ncompileSdk = "37"\nbuildTools = "37.0.0"\n')
        (self.root / "android/gradle/wrapper/gradle-wrapper.jar").touch()
        (self.root / "sdk/platforms/android-37.0/android.jar").touch()
        java = self.root / "jdk/bin/java"
        java.write_text('#!/usr/bin/env bash\nprintf \'openjdk version "21"\\n\' >&2\n')
        java.chmod(0o700)
        shutil.copyfile(java, self.root / "jdk/bin/javac")
        (self.root / "jdk/bin/javac").chmod(0o700)
        (self.root / "android/gradlew").write_text('#!/usr/bin/env bash\nprintf \'%s\\n\' "$@" > "$MOCK_GRADLE_LOG"\nexit "${MOCK_GRADLE_STATUS:-0}"\n')
        self.log = self.root / "gradle-arguments"
        self.env = dict(os.environ, JAVA_HOME=str(self.root / "jdk"),
                        ANDROID_HOME=str(self.root / "sdk"), ANDROID_SDK_ROOT=str(self.root / "sdk"),
                        ANDROID_CI_TIMEOUT_SECONDS="10", ANDROID_CI_MAX_WORKERS="2",
                        MOCK_GRADLE_LOG=str(self.log), MOCK_RUSTUP_LOG=str(self.root / "rustup-arguments"),
                        PATH=str(self.root / "bin") + os.pathsep + os.environ.get("PATH", ""))

    def run_lane(self, *args):
        return subprocess.run(["bash", str(self.root / "scripts/ci/android.sh"), *args],
                              env=self.env, capture_output=True, text=True, timeout=30)

    def test_default_full_and_every_lane_use_expected_tasks(self):
        for lane in shards.LANES:
            with self.subTest(lane=lane):
                result = self.run_lane(*([] if lane == "full" else ["--lane", lane]))
                self.assertEqual(result.returncode, 0, result.stderr)
                arguments = self.log.read_text().splitlines()
                expected = shards.tasks(SETTINGS, lane)
                self.assertEqual(arguments[-len(expected):], expected)
                self.assertIn("--max-workers=2", arguments)
                self.assertNotIn("--profile", arguments)
                self.assertEqual("translation checker tests" in result.stdout, lane in ("full", "lint-build-test-0"))
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
