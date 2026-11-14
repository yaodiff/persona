#!/usr/bin/env python3
"""The interface stops growing at xxxLarge (docs/design/DESIGN_SYSTEM.md, "Dynamic Type"), so a view outside a reading
container never sees an accessibility size: `isAccessibilitySize` and `.accessibility1…5` there are dead branches, or a new
AX-only layout. Only the files below may mention them."""
import re
import unittest
from pathlib import Path

APPLE = Path(__file__).resolve().parents[2] / "apple"
SCAN = ["Persona", "Shared", "PersonaWatch", "Vision"]

ALLOWED = {
    "Persona/Recording/MeetingThreadView.swift": "stacks a transcript row's stamp and words at the reading size",
    "Persona/Recording/RecordingMinutesSummary.swift": "the same for minutes' points and transcript lines",
    "Persona/Recording/RecordingMinutesOverview.swift": "puts a topic's icon tile above its title at the reading size (#6563)",
    "Persona/Recording/RecordingMinutesView.swift": "the same for the minutes' points",
    "Persona/Views/Chat/BubbleFold.swift": "the user bubble folds by the reading size it is given",
    "Shared/Design/RecordingTokens.swift": "the recording timer's own cap",
    "PersonaWatch/PersonaWatchApp.swift": "the watch's showLargeText launch switch",
}

USE = re.compile(r"isAccessibilitySize|\.accessibility[1-5]\b")


class AccessibilityBranchesTest(unittest.TestCase):
    def test_only_listed_files_branch_on_accessibility_sizes(self):
        found = set()
        for top in SCAN:
            for path in (APPLE / top).rglob("*.swift"):
                if USE.search(path.read_text(encoding="utf-8")):
                    found.add(str(path.relative_to(APPLE)))
        self.assertEqual(sorted(found - set(ALLOWED)), [],
                         "the interface is capped at xxxLarge: delete the AX-only layout, or, for a reading container, "
                         "add the file to ALLOWED with its reason")

    def test_listed_files_exist(self):
        self.assertEqual([f for f in ALLOWED if not (APPLE / f).exists()], [])


if __name__ == "__main__":
    unittest.main()
