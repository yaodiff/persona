#!/usr/bin/env python3
"""Code that moved out of apple/Shared into a Swift package does not flow back (#7891, stage 1; #7895).

Each group of files that moves adds its names here: the file may not reappear in apple/Shared, and no Swift file
outside the package may define the moved types again. There is no count of the folder's files or lines.

  python3 scripts/test/test_apple_shared_loose.py
"""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
APPLE = ROOT / "apple"

# package -> (files that left apple/Shared, directories that left it, types the package now owns)
MOVED = {
    "PersonaWire": (["JSONValue.swift"], ["Generated"], ["JSONValue", "JSON"]),
}


def swift_files():
    for f in APPLE.rglob("*.swift"):
        parts = f.relative_to(APPLE).parts
        if parts[0] != "Packages" and "build" not in parts:  # other packages are separate modules
            yield f


class MovedCodeStaysMoved(unittest.TestCase):
    def test_files_do_not_reappear_in_shared(self):
        for package, (files, dirs, _) in MOVED.items():
            for name in files:
                self.assertFalse((APPLE / "Shared" / name).exists(), f"{name} lives in {package}")
            for name in dirs:
                self.assertFalse((APPLE / "Shared" / name).exists(), f"{name}/ lives in {package}")

    def test_types_are_not_defined_again(self):
        for package, (_, _, types) in MOVED.items():
            for f in swift_files():
                text = f.read_text(encoding="utf-8")
                for t in types:
                    found = re.search(rf"^\s*(?:\w+\s+)*(?:enum|struct|class|actor)\s+{t}\b", text, re.M)
                    self.assertIsNone(found, f"{f.relative_to(ROOT)} defines {t}, which {package} owns")


if __name__ == "__main__":
    unittest.main()
