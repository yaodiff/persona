"""MOCK_TODO_WAIT_REASONS=1 (todo_wait_reasons_mock.py, #16168): the to-dos are the waiting fixtures, each with its typed
`wait`, plus one working to-do; GET /mock/reset copies them back. Without the switch none of them is served."""
import socket
import unittest

from test_mock_hub_mesh import Mock

WAITS = {"twr_reply": "person_reply", "twr_pr": "pr_merge", "twr_agent": "agent", "twr_date": "date",
         "twr_limit": "usage_limit"}


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class TodoWaitReasonsMockTests(unittest.TestCase):
    def hub(self, **env):
        hub = Mock(free_port(), **env)
        self.addCleanup(hub.stop)
        return hub

    def kinds(self, hub):
        return {t["id"]: (t.get("wait") or {}).get("kind") for t in hub.get("/api/v1/todos")["todos"]}

    def test_the_to_dos_wait_for_each_reason_and_come_back_after_a_reset(self):
        hub = self.hub(MOCK_TODO_WAIT_REASONS="1")
        want = dict(WAITS, twr_working=None)
        self.assertEqual(self.kinds(hub), want)
        hub.get("/mock/reset")
        self.assertEqual(self.kinds(hub), want)
        pr = next(t for t in hub.get("/api/v1/todos")["todos"] if t["id"] == "twr_pr")
        self.assertEqual(pr["wait"]["pr_blockers"], ["review_required", "checks_pending"])

    def test_without_the_switch_no_waiting_fixture_is_served(self):
        hub = self.hub()
        self.assertFalse([tid for tid in self.kinds(hub) if tid.startswith("twr_")])


if __name__ == "__main__":
    unittest.main()
