"""MOCK_INBOX_SHOWCASE serves the shared inbox screenshot fixture without changing its items."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import URLError
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
SHOWCASE_PATH = HERE.parent.parent / "spec" / "fixtures" / "inbox" / "showcase.json"


class MockHub:
    def __init__(self, **env):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.base = f"http://127.0.0.1:{port}"
        self.home = tempfile.TemporaryDirectory(prefix="inbox-showcase-mock-")
        self.log = tempfile.TemporaryFile(dir=self.home.name)
        clean = {key: value for key, value in os.environ.items() if not key.startswith("MOCK_")}
        persona_home = Path(self.home.name) / "persona-home"
        persona_home.mkdir()
        self.process = subprocess.Popen(
            ["python3", "-I", str(HERE / "mock_hub.py")],
            env=dict(clean, PORT=str(port), HOME=self.home.name, PERSONA_HOME=str(persona_home), **env),
            cwd=self.home.name,
            stdout=self.log,
            stderr=self.log,
        )
        for _ in range(100):
            if self.process.poll() is not None:
                self.log.seek(0)
                output = self.log.read().decode(errors="replace")[-2000:]
                self.stop()
                raise RuntimeError(f"the mock hub exited: {output}")
            try:
                with urlopen(self.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError, ConnectionError):
                time.sleep(0.1)
        self.stop()
        raise RuntimeError("the mock hub did not start")

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.log.close()
        self.home.cleanup()

    def request(self, path, body=None):
        request = Request(self.base + path,
                          data=None if body is None else json.dumps(body).encode(),
                          method="GET" if body is None else "POST",
                          headers={"Authorization": "Bearer mock", "Content-Type": "application/json"})
        with urlopen(request, timeout=3) as response:
            return json.load(response)


class InboxShowcaseMockTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.showcase = json.loads(SHOWCASE_PATH.read_text(encoding="utf-8"))
        cls.items = cls.showcase["items"]
        cls.sessions = cls.showcase["sessions"]

    def serve(self, **env):
        hub = MockHub(**env)
        self.addCleanup(hub.stop)
        return hub

    def test_the_list_is_the_fixture_with_existing_source_parameters(self):
        hub = self.serve(MOCK_INBOX_SHOWCASE="1")
        for path in ("/api/v1/inbox", "/api/v1/inbox?sources=all"):
            with self.subTest(path=path):
                items = hub.request(path)["items"]
                self.assertEqual([item["id"] for item in items], [item["id"] for item in self.items])
                self.assertEqual(items, self.items)
        first = hub.request("/api/v1/inbox")["items"][0]
        self.assertEqual(first["id"], "sc-1")
        self.assertEqual(len(first["chain"]), len(self.items[0]["chain"]))
        self.assertEqual(first["chain"], self.items[0]["chain"])
        for field in ("where", "follow", "now"):
            self.assertEqual(first[field], self.items[0][field])

    def test_each_detail_is_the_same_fixture_item(self):
        hub = self.serve(MOCK_INBOX_SHOWCASE="1")
        for expected in self.items:
            with self.subTest(item=expected["id"]):
                self.assertEqual(hub.request("/api/v1/inbox/" + expected["id"]), expected)

    def test_showcase_sessions_join_the_session_list_with_complete_rows(self):
        hub = self.serve(MOCK_INBOX_SHOWCASE="1")
        listed = hub.request("/api/v1/sessions")["sessions"]
        for expected in self.sessions:
            with self.subTest(session=expected["session_id"]):
                actual = next(
                    row for row in listed
                    if row["machine_id"] == expected["machine_id"] and row["session_id"] == expected["session_id"]
                )
                for field, value in expected.items():
                    self.assertEqual(actual[field], value, field)
                for field in ("agent", "launcher", "model", "space_kind", "space_id", "slot_id", "pane_id", "last_text"):
                    self.assertIn(field, actual)
                self.assertEqual(actual["status"], {"unread": "done", "idle": "idle"}[expected["state"]])
        self.assertEqual(next(row for row in listed if row["session_id"] == "s-agent")["state"], "unread")

    def test_showcase_machine_is_online_with_complete_fields(self):
        hub = self.serve(MOCK_INBOX_SHOWCASE="1")
        machine = next(row for row in hub.request("/api/v1/machines")["machines"] if row["id"] == "m1")
        for field, value in {"name": "示例电脑", "kind": "ssh", "icon": "desktop", "ssh_host": "example-computer", "online": True,
                             "hostname": "example-computer", "os": "linux", "arch": "amd64", "home": "/home/user"}.items():
            self.assertEqual(machine[field], value, field)

    def test_showcase_takes_precedence_over_inbox_events(self):
        hub = self.serve(MOCK_INBOX_SHOWCASE="1", MOCK_INBOX_EVENTS="legacy")
        self.assertEqual(hub.request("/api/v1/inbox")["items"], self.items)
        self.assertEqual(hub.request("/api/v1/inbox/sc-1"), self.items[0])

    def test_item_writes_remain_visible_in_the_list_and_detail(self):
        hub = self.serve(MOCK_INBOX_SHOWCASE="1")
        changed = hub.request("/api/v1/inbox/sc-4/done", {"done": True})
        listed = next(item for item in hub.request("/api/v1/inbox")["items"] if item["id"] == "sc-4")
        detail = hub.request("/api/v1/inbox/sc-4")
        for item in (listed, detail):
            self.assertEqual(item["done_at"], changed["done_at"])
            self.assertEqual(item["done_by"], "user")

    def test_the_default_inbox_is_unchanged_without_the_flag(self):
        hub = self.serve()
        ids = [item["id"] for item in hub.request("/api/v1/inbox")["items"]]
        self.assertNotEqual(ids, [item["id"] for item in self.items])
        self.assertTrue(all(not item_id.startswith("sc-") for item_id in ids))
        session_keys = {(row["machine_id"], row["session_id"]) for row in hub.request("/api/v1/sessions")["sessions"]}
        self.assertTrue(all((row["machine_id"], row["session_id"]) not in session_keys for row in self.sessions))
        machine_ids = {row["id"] for row in hub.request("/api/v1/machines")["machines"]}
        self.assertNotIn("m1", machine_ids)


if __name__ == "__main__":
    unittest.main()
