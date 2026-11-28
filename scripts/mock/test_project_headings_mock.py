"""The project-heading mock fixture replaces the Agent list only when its domain switch is on."""
import os
from pathlib import Path
import runpy
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
PROJECTS = {
    "45e3b8c7-0000-4000-8000-000000000001",
    "45e3b8c7-0000-4000-8000-000000000002",
    "45e3b8c7-0000-4000-8000-000000000003",
    "45e3b8c7-0000-4000-8000-000000000004",
}


class ProjectHeadingsMockTests(unittest.TestCase):
    def load(self, enabled):
        env = {key: value for key, value in os.environ.items() if not key.startswith("MOCK_")}
        if enabled:
            env["MOCK_PROJECT_HEADINGS"] = "1"
        with patch.dict(os.environ, env, clear=True):
            return runpy.run_path(str(HERE / "mock_hub.py"))

    def test_the_fixture_has_same_named_projects_and_no_pending_cards(self):
        mock = self.load(True)
        rows = {row["session_id"]: row for row in mock["SESSIONS"]}
        self.assertEqual(set(rows), PROJECTS)
        self.assertEqual(rows["45e3b8c7-0000-4000-8000-000000000001"]["workspace_cwd"], "/Users/me/Desktop/site")
        self.assertEqual(rows["45e3b8c7-0000-4000-8000-000000000002"]["space_name"], "site")
        self.assertIsNone(rows["45e3b8c7-0000-4000-8000-000000000004"]["space_id"])
        self.assertEqual(mock["mock_interactions"](), [])

    def test_the_fixture_is_off_by_default(self):
        mock = self.load(False)
        self.assertTrue(mock["SESSIONS"])
        self.assertFalse(PROJECTS & {row["session_id"] for row in mock["SESSIONS"]})
        self.assertIn("MOCK_PROJECT_HEADINGS=1", "\n".join(mock["MOCK_HELP"]))


if __name__ == "__main__":
    unittest.main()
