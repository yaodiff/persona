import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import android_shots_diff as shots


class ScreenshotComparisonTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.before = Path(temp.name) / "before"
        self.after = Path(temp.name) / "after"
        self.before.mkdir()
        self.after.mkdir()

    def compare(self, *args):
        script = Path(shots.__file__).resolve()
        return subprocess.run([sys.executable, "-I", "-B", str(script), str(self.before), str(self.after), *args],
                              capture_output=True, text=True, timeout=5)

    def test_identical_unreadable_pair_does_not_pass(self):
        for directory in (self.before, self.after):
            (directory / "broken.png").write_bytes(b"not a screenshot")
            shots.write_png(directory / "valid.png", 1, 1, b"\x00\x11\x22\xff")
        result = self.compare()
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("读不了  broken.png", result.stdout)
        self.assertIn("相同  valid.png", result.stdout)

    def test_failed_input_read_does_not_abort_other_pairs(self):
        (self.before / "broken.png").symlink_to("missing")
        shots.write_png(self.after / "broken.png", 1, 1, b"\x00\x11\x22\xff")
        for directory in (self.before, self.after):
            shots.write_png(directory / "valid.png", 1, 1, b"\x00\x11\x22\xff")
        result = self.compare()
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("相同  valid.png", result.stdout)

    def test_valid_pairs_keep_equality_and_difference_statuses(self):
        for directory in (self.before, self.after):
            shots.write_png(directory / "frame.png", 1, 1, b"\x00\x11\x22\xff")
        self.assertEqual(self.compare().returncode, 0)
        shots.write_png(self.after / "frame.png", 1, 1, b"\x33\x11\x22\xff")
        result = self.compare()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("不同  frame.png", result.stdout)

    def test_diff_directory_cannot_alias_an_input(self):
        before = self.before / "frame.png"
        after = self.after / "frame.png"
        shots.write_png(before, 1, 1, b"\x00\x00\x00\xff")
        shots.write_png(after, 1, 1, b"\xff\xff\xff\xff")
        originals = before.read_bytes(), after.read_bytes()
        alias = self.before.parent / "alias"
        alias.symlink_to(self.after, target_is_directory=True)
        for target in (self.before, self.after, alias):
            with self.subTest(target=target.name):
                result = self.compare("--diff", str(target))
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertEqual((before.read_bytes(), after.read_bytes()), originals)

    def test_diff_replaces_link_without_writing_through_it(self):
        before = self.before / "frame.png"
        after = self.after / "frame.png"
        shots.write_png(before, 1, 1, b"\x00\x00\x00\xff")
        shots.write_png(after, 1, 1, b"\xff\xff\xff\xff")
        originals = before.read_bytes(), after.read_bytes()
        for link in ("hard", "symbolic"):
            with self.subTest(link=link):
                destination = self.before.parent / link
                destination.mkdir()
                output = destination / "frame.png"
                if link == "hard":
                    os.link(before, output)
                else:
                    output.symlink_to(after)
                result = self.compare("--diff", str(destination))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual((before.read_bytes(), after.read_bytes()), originals)
                self.assertFalse(output.is_symlink())
                self.assertEqual(shots.decode_png(output.read_bytes()).rgba, shots.MAGENTA)
                self.assertEqual(list(destination.iterdir()), [output])


if __name__ == "__main__":
    unittest.main()
