import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
GATE = ROOT / "scripts/ci/desktop-release-gate.sh"
PACKAGE = ROOT / "scripts/ci/desktop-package.sh"
MAC = ("PERSONA_DESKTOP_APP_ID", "MAC_CSC_LINK", "MAC_CSC_KEY_PASSWORD", "APPLE_API_KEY", "APPLE_API_KEY_ID", "APPLE_API_ISSUER")
WIN = ("PERSONA_DESKTOP_APP_ID", "WIN_CSC_LINK", "WIN_CSC_KEY_PASSWORD")


class DesktopReleaseGateTests(unittest.TestCase):
    """A version tag attaches desktop packages only when they can be signed, and says why when it does not."""

    def gate(self, tag, present=()):
        with tempfile.TemporaryDirectory() as directory:
            output, summary = Path(directory, "output"), Path(directory, "summary")
            env = {"PATH": os.environ["PATH"], "RELEASE_TAG": tag, "GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}
            env.update({f"HAS_{name}": "true" for name in present})
            result = subprocess.run(["bash", str(GATE)], cwd=ROOT, env=env, capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            return values, summary.read_text()

    def test_no_secrets_skips_both_platforms_and_names_what_is_missing(self):
        values, summary = self.gate("v1.2.0")
        self.assertEqual(values, {"version": "1.2.0", "mac": "false", "win": "false"})
        self.assertIn("macOS desktop packages skipped: Environment secrets not configured: " + " ".join(MAC), summary)
        self.assertIn("Windows desktop package skipped: Environment secrets not configured: " + " ".join(WIN), summary)

    def test_each_platform_needs_all_of_its_own_secrets(self):
        values, summary = self.gate("v1.2.0", MAC)
        self.assertEqual((values["mac"], values["win"]), ("true", "false"))
        self.assertIn("WIN_CSC_LINK WIN_CSC_KEY_PASSWORD", summary)
        values, _ = self.gate("v1.2.0", WIN)
        self.assertEqual((values["mac"], values["win"]), ("false", "true"))
        for absent in MAC:
            with self.subTest(absent=absent):
                values, summary = self.gate("v1.2.0", [name for name in MAC if name != absent])
                self.assertEqual(values["mac"], "false")
                self.assertIn(absent, summary)
        values, _ = self.gate("v1.2.0", set(MAC) | set(WIN))
        self.assertEqual(values, {"version": "1.2.0", "mac": "true", "win": "true"})

    def test_the_version_is_the_tag_and_must_be_a_stable_semver(self):
        self.assertEqual(self.gate("v10.20.30", set(MAC) | set(WIN))[0], {"version": "10.20.30", "mac": "true", "win": "true"})
        for tag in ("v1.2", "vnext", "v1.2.0+build", "v1.2.0 x"):
            with self.subTest(tag=tag):
                values, summary = self.gate(tag, set(MAC) | set(WIN))
                self.assertEqual((values["mac"], values["win"]), ("false", "false"))
                self.assertIn("is not v<major>.<minor>.<patch>", summary)
        values, summary = self.gate("v1.3.0-rc1", set(MAC) | set(WIN))
        self.assertEqual(values, {"version": "1.3.0-rc1", "mac": "false", "win": "false"})
        self.assertIn("is a prerelease tag", summary)

    def test_a_secret_value_never_reaches_the_gate(self):
        release = (ROOT / ".github/workflows/release.yml").read_text()
        gate = release.split("\n  desktop-gate:\n", 1)[1].split("\n  desktop-mac:\n", 1)[0]
        for line in gate.splitlines():
            if "secrets." in line:
                self.assertRegex(line, r"^\s+HAS_[A-Z_]+: \$\{\{ secrets\.[A-Z_]+ != '' \}\}$")


class DesktopPackageScriptTests(unittest.TestCase):
    def package(self, **env):
        return subprocess.run(["bash", str(PACKAGE), "win"], cwd=ROOT, env={"PATH": os.environ["PATH"], **env}, capture_output=True, text=True, check=False)

    def test_a_release_build_refuses_to_fall_back_to_unsigned(self):
        result = self.package(PERSONA_DESKTOP_REQUIRE_SIGNING="1")
        self.assertEqual(result.returncode, 2)
        self.assertIn("no certificate was supplied", result.stderr)

    def test_a_release_build_refuses_the_placeholder_app_id(self):
        for app_id in ("", "com.example.persona.desktop"):
            with self.subTest(app_id=app_id):
                result = self.package(PERSONA_DESKTOP_REQUIRE_SIGNING="1", CSC_LINK="placeholder", PERSONA_DESKTOP_APP_ID=app_id)
                self.assertEqual(result.returncode, 2)
                self.assertIn("not the registered app ID", result.stderr)

    def test_the_version_must_be_semver(self):
        result = self.package(PERSONA_DESKTOP_VERSION="1.2")
        self.assertEqual(result.returncode, 2)
        self.assertIn("not a semantic version", result.stderr)

    def test_no_registered_identity_is_written_into_the_repository(self):
        production = yaml.load((ROOT / "desktop/electron-builder.yml").read_text(), Loader=yaml.BaseLoader)
        self.assertEqual(production["appId"], "${env.PERSONA_DESKTOP_APP_ID}")
        self.assertEqual(production["publish"]["provider"], "github")
        self.assertNotIn("update-test", str(production["files"]))


class DesktopReleaseWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.load((ROOT / ".github/workflows/release.yml").read_text(), Loader=yaml.BaseLoader)["jobs"]

    def test_the_administrator_gate_stays_in_front_of_every_desktop_job(self):
        self.assertEqual(self.jobs["desktop-gate"]["needs"], "authorize")
        self.assertEqual(self.jobs["desktop-gate"]["if"], "github.event_name == 'push' && github.ref_type == 'tag'")
        # The gate reads only secret presence, but presence is environment-scoped too: without this binding the
        # documented environment-only configuration would always skip both signed platforms.
        self.assertEqual(self.jobs["desktop-gate"]["environment"], "persona-desktop-release")
        for name in ("desktop-mac", "desktop-win"):
            self.assertEqual(self.jobs[name]["needs"], ["authorize", "desktop-gate"])
            self.assertNotIn("permissions", self.jobs[name])
            self.assertEqual(self.jobs[name]["environment"], "persona-desktop-release")
        self.assertEqual(self.jobs["desktop-mac"]["if"], "needs.desktop-gate.outputs.mac == 'true'")
        self.assertEqual(self.jobs["desktop-win"]["if"], "needs.desktop-gate.outputs.win == 'true'")
        self.assertIn("publish", self.jobs["desktop-publish"]["needs"])
        self.assertEqual(self.jobs["desktop-publish"]["permissions"], {"contents": "write"})

    def test_release_packages_are_signed_versioned_from_the_tag_and_verified(self):
        for name, verify in (("desktop-mac", "spctl --assess"), ("desktop-win", "Get-AuthenticodeSignature")):
            steps = self.jobs[name]["steps"]
            build = next(step for step in steps if "desktop-package.sh" in step.get("run", ""))
            self.assertEqual(build["env"]["PERSONA_DESKTOP_REQUIRE_SIGNING"], "1")
            self.assertEqual(build["env"]["PERSONA_DESKTOP_VERSION"], "${{ needs.desktop-gate.outputs.version }}")
            self.assertEqual(build["env"]["PERSONA_DESKTOP_APP_ID"], "${{ secrets.PERSONA_DESKTOP_APP_ID }}")
            index = steps.index(build)
            self.assertIn(verify, steps[index + 1]["run"])
            self.assertIn("upload-artifact", steps[index + 2]["uses"])

    def test_missing_secrets_do_not_fail_the_release(self):
        # Skipped desktop jobs leave the image and Release jobs untouched, and nothing desktop is needed by them.
        for name in ("smoke", "publish"):
            self.assertNotIn("desktop", str(self.jobs[name]["needs"]))
        self.assertIn("needs.publish.result == 'success'", self.jobs["desktop-publish"]["if"])
        self.assertIn("!cancelled()", self.jobs["desktop-publish"]["if"])
        self.assertNotIn("always()", self.jobs["desktop-publish"]["if"])

    def test_update_metadata_is_uploaded_after_the_packages(self):
        script = self.jobs["desktop-publish"]["steps"][-1]["run"]
        self.assertLess(script.index('"${packages[@]}" --clobber'), script.index('"${metadata[@]}" --clobber'))
        for pattern in ("*.dmg", "*.zip", "*.exe", "*.blockmap", "*.yml"):
            self.assertIn(pattern, script)

    def test_documented_secrets_are_in_their_protected_environment(self):
        text = (ROOT / "docs/desktop/desktop.md").read_text()
        environment = re.search(r"create the `([a-z0-9-]+)` GitHub Environment", text).group(1)
        self.assertEqual(environment, "persona-desktop-release")
        release = yaml.load((ROOT / ".github/workflows/release.yml").read_text(), Loader=yaml.BaseLoader)
        for name in sorted(set(MAC) | set(WIN)):
            self.assertIn(f"`{name}`", text)
            self.assertIn(f"secrets.{name}", str(release))
        for name, job in release["jobs"].items():
            if any(f"secrets.{secret}" in str(job) for secret in set(MAC) | set(WIN)):
                self.assertEqual(job.get("environment"), environment,
                                 f"{name} must use the documented Environment")

    def test_desktop_ci_never_receives_signing_credentials(self):
        # Signing and notarization are release-only; a manually dispatched branch cannot sign arbitrary code.
        text = (ROOT / ".github/workflows/desktop-test.yml").read_text()
        self.assertNotIn("inputs.signed", text)
        for name in sorted(set(MAC) | set(WIN)):
            self.assertNotIn(f"secrets.{name}", text)


if __name__ == "__main__":
    unittest.main()
