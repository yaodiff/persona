"""Question-only cancellation restores both skipped cards in every long-question mode (#7330)."""
from contextlib import contextmanager
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from urllib.error import URLError

sys.path.insert(0, str(Path(__file__).parent))
from test_todo_decisions_mock import MockHub  # noqa: E402

IDS = ("ix_tests", "ix_ask_long")
QUESTION_MODES = ("1", "actions", "swipe", "review")


class CleaningMockHub(MockHub):
    """The shared helper starts before its health check; clean up if that check raises."""
    def __init__(self, **env):
        try:
            super().__init__(**env)
        except Exception:
            if hasattr(self, "process"):
                self.stop()
            elif hasattr(self, "log"):
                self.log.close()
            raise


@contextmanager
def question_hub(mode, cancel):
    hub = CleaningMockHub(MOCK_AGENTS="1", MOCK_ASK_LONG=mode, MOCK_QUESTION_CANCEL=cancel, MOCK_RESOLVE="ok")
    try:
        yield hub
    finally:
        hub.stop()


class QuestionCancelMockTests(unittest.TestCase):
    def records(self, hub, path, key):
        return [row for row in hub.request(path)[key] if row["id"] in IDS]

    def pending(self, hub):
        return self.records(hub, "/api/v1/interactions", "interactions")

    def resolved(self, hub):
        return self.records(hub, "/mock/resolved", "resolved")

    def history(self, hub):
        return self.records(hub, "/api/v1/interactions/history", "interactions")

    def assert_ids(self, rows, expected):
        self.assertEqual(sorted(row["id"] for row in rows), sorted(expected))

    def assert_pending(self, hub, expected, can_cancel):
        rows = self.pending(hub)
        self.assert_ids(rows, expected)
        for row in rows:
            if can_cancel:
                self.assertIs(row.get("can_cancel"), True)
            else:
                self.assertNotIn("can_cancel", row)
            self.assertNotIn("resolution", row)
            self.assertNotIn("resolved_at", row)

    def assert_resolved(self, hub, expected):
        rows = self.resolved(hub)
        self.assert_ids(rows, expected)
        for row in rows:
            self.assertEqual(row["resolution"], {"cancel": True})

    def skip_both(self, hub):
        for interaction_id in IDS:
            result = hub.request(f"/api/v1/interactions/{interaction_id}/resolve", "POST", {"cancel": True})
            self.assertEqual(result, {"ok": True})
        self.assertEqual(self.pending(hub), [])
        self.assert_resolved(hub, IDS)
        self.assert_ids(self.history(hub), IDS)

    def test_cancel_reset_restores_both_questions_twice(self):
        for mode in QUESTION_MODES:
            with self.subTest(mode=mode), question_hub(mode, "1") as hub:
                self.assert_pending(hub, IDS, True)
                for _ in range(2):
                    self.skip_both(hub)
                    hub.request("/mock/reset")
                    self.assert_pending(hub, IDS, True)
                    self.assert_resolved(hub, ())
                    self.assertEqual(self.history(hub), [])

    def test_plain_mode_one_reset_keeps_current_behavior(self):
        with question_hub("1", "") as hub:
            self.assert_pending(hub, IDS, False)
            self.skip_both(hub)
            hub.request("/mock/reset")
            self.assert_pending(hub, (), False)
            self.assert_resolved(hub, IDS)
            self.assert_ids(self.history(hub), IDS)

    def test_plain_actions_reset_restores_only_the_long_question(self):
        with question_hub("actions", "") as hub:
            self.assert_pending(hub, IDS, False)
            self.skip_both(hub)
            hub.request("/mock/reset")
            self.assert_pending(hub, ("ix_ask_long",), False)
            self.assert_resolved(hub, ("ix_tests",))
            self.assert_ids(self.history(hub), ("ix_tests",))

    def test_startup_failure_stops_the_process(self):
        created = []
        original_init = CleaningMockHub.__init__

        def capture(hub, **env):
            created.append(hub)
            original_init(hub, **env)

        with patch("test_todo_decisions_mock.urlopen", side_effect=URLError("forced health check")), \
                patch("test_todo_decisions_mock.time.sleep", return_value=None), \
                patch.object(CleaningMockHub, "__init__", capture):
            with self.assertRaisesRegex(RuntimeError, "the mock hub did not start"):
                CleaningMockHub(MOCK_ASK_LONG="1")
        self.assertEqual(len(created), 1)
        hub = created[0]
        try:
            self.assertIsNotNone(hub.process.poll())
            self.assertTrue(hub.log.closed)
        finally:
            hub.stop()  # A regression must not leak the process this test started.


if __name__ == "__main__":
    unittest.main()
