"""entitlements.py check and the apps' entitlements: time-sensitive notifications on the iPhone and
Apple Vision Pro (#1572, #1655, #2435).

    python3 scripts/release/test_entitlements.py
"""
import os
from pathlib import Path
import plistlib
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
APPLE = HERE.parent.parent / "apple"
sys.path.insert(0, str(HERE))
import entitlements as ents  # noqa: E402

BUNDLE = "com.example.persona"


class AppsAskForTimeSensitive(unittest.TestCase):
    """The entitlements files xcodegen writes from project.yml (tracked): without the key the export drops it and a push
    the hub marks time-sensitive shows as active, under Focus too."""

    def entitlements(self, path):
        with open(APPLE / path, "rb") as f:
            return plistlib.load(f)

    def test_the_iphone_app(self):
        self.assertIs(self.entitlements("Persona/Resources/Persona.entitlements").get(ents.TIME_SENSITIVE), True)

    def test_the_vision_app(self):
        self.assertIs(self.entitlements("Vision/Resources/PersonaVision.entitlements").get(ents.TIME_SENSITIVE), True)


class CheckAMacApp(unittest.TestCase):
    """`check` on a Mac app (what a .pkg holds): a target that asks for time-sensitive must be signed with it."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.app = root / "Persona.app"
        (self.app / "Contents").mkdir(parents=True)
        with open(self.app / "Contents" / "Info.plist", "wb") as f:
            plistlib.dump({"CFBundleIdentifier": BUNDLE, "DTPlatformName": "macosx"}, f)
        with open(root / "PersonaMac.entitlements", "wb") as f:
            plistlib.dump({ents.TIME_SENSITIVE: True, "com.apple.developer.aps-environment": "development"}, f)
        self.targets = {(BUNDLE, "macosx"): {"CODE_SIGN_ENTITLEMENTS": "PersonaMac.entitlements", "PROJECT_DIR": str(root),
                                              "DEVELOPMENT_TEAM": "TEAM", "TARGET_NAME": "PersonaMac"}}

    def tearDown(self):
        self.tmp.cleanup()

    def check(self, signed):
        with patch.object(ents, "targets", return_value=self.targets), patch.object(ents, "signed", return_value=signed):
            ents.check(str(self.app))

    def test_signed_without_it_is_a_problem(self):
        with self.assertRaises(SystemExit) as stop:
            self.check({"application-identifier": "TEAM." + BUNDLE, "com.apple.developer.aps-environment": "development"})
        self.assertIn("1 problem", str(stop.exception))

    def test_signed_with_it_is_ok(self):
        self.check({"application-identifier": "TEAM." + BUNDLE, "com.apple.developer.aps-environment": "development",
                    ents.TIME_SENSITIVE: True})


if __name__ == "__main__":
    unittest.main()
