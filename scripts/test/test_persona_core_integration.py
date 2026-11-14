#!/usr/bin/env python3
"""PersonaCore is a generated source package plus a locally built, never-committed C framework."""
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class PersonaCoreIntegrationTests(unittest.TestCase):
    def test_no_binary_framework_or_library_is_tracked(self):
        paths = subprocess.check_output(["git", "-C", str(ROOT), "ls-files"], text=True).splitlines()
        for path in paths:
            self.assertNotIn(".xcframework/", path)
            self.assertFalse(path.endswith((".a", ".dylib", ".so", ".dll")), path)
        self.assertIn("apple/Packages/PersonaCore/Sources/PersonaCoreWire/PersonaCoreWire.swift", paths)

    def test_excluded_apple_sources_never_import_the_core(self):
        for folder in ["Shared", "Mac", "Vision", "PersonaWatch", "PersonaWidgets", "LiveActivity", "PersonaLink"]:
            for path in (ROOT / "apple" / folder).rglob("*.swift"):
                self.assertNotIn("import PersonaCore", path.read_text(), str(path))


if __name__ == "__main__":
    unittest.main()
