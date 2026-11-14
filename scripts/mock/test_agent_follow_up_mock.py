"""Tests mock_hub's Agent Follow-Up settings (#7005): the fields `GET /api/v1/assistant` sends with the hub's defaults, and
`PUT` checked as the hub's profile_write.go checks it (the session-name settings only from a request naming its manager)."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

FIELDS = ("start_requested_tasks", "task_workspace_preference", "main_supervises_own_tasks", "agent_tasks", "idle_close_hours",
          "archive_closed_days", "agent_auto_naming", "agent_auto_naming_max", "agent_auto_naming_interval_hours")


def start(env):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    log = tempfile.TemporaryFile()
    process = subprocess.Popen(["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
                               env=dict(os.environ, PORT=str(port), **env), stdout=log, stderr=log)
    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError("mock hub did not start")
        try:
            with urlopen(base + "/api/v1/health", timeout=1):
                return base, process, log
        except (URLError, TimeoutError):
            time.sleep(0.1)
    raise RuntimeError("mock hub start timed out")


def stop(process, log):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)
    log.close()


def call(base, path, body=None, manager=None):
    headers = {"Authorization": "Bearer mock", "Content-Type": "application/json"}
    if manager:
        headers["X-Persona-Manager"] = manager
    request = Request(base + path, data=None if body is None else json.dumps(body).encode(),
                      method="GET" if body is None else "PUT", headers=headers)
    with urlopen(request, timeout=3) as response:
        return json.load(response)


def status(base, path, body, manager=None):
    try:
        call(base, path, body, manager)
        return 200
    except HTTPError as e:
        e.close()
        return e.code


class AgentFollowUpMockTests(unittest.TestCase):
    def serve(self, **env):
        base, process, log = start(env)
        self.addCleanup(stop, process, log)
        return base

    def test_the_hubs_defaults_and_sparse_writes(self):
        base = self.serve()
        a = call(base, "/api/v1/assistant")
        self.assertEqual({k: a[k] for k in FIELDS}, {
            "start_requested_tasks": True, "task_workspace_preference": "prefer-existing", "main_supervises_own_tasks": False,
            "agent_tasks": False, "idle_close_hours": 12, "archive_closed_days": 3, "agent_auto_naming": True,
            "agent_auto_naming_max": 1, "agent_auto_naming_interval_hours": 0})
        self.assertEqual(a["manager"], "assistant")
        after = call(base, "/api/v1/assistant", {"start_requested_tasks": False})
        self.assertIs(after["start_requested_tasks"], False)
        self.assertEqual(after["idle_close_hours"], 12, "only the field sent changes")
        self.assertEqual(call(base, "/api/v1/assistant", {"task_workspace_preference": "prefer-new"})["task_workspace_preference"], "prefer-new")
        self.assertEqual(status(base, "/api/v1/assistant", {"task_workspace_preference": "somewhere"}), 400)

    def test_session_names_need_the_manager_named_and_stay_in_range(self):
        base = self.serve()
        self.assertEqual(status(base, "/api/v1/assistant", {"agent_auto_naming_max": 5}), 400, "no manager named")
        self.assertEqual(call(base, "/api/v1/assistant", {"agent_auto_naming_max": 1000}, manager="assistant")["agent_auto_naming_max"], 1000)
        self.assertEqual(status(base, "/api/v1/assistant", {"agent_auto_naming_max": 1001}, manager="assistant"), 400)
        self.assertEqual(status(base, "/api/v1/assistant", {"agent_auto_naming_interval_hours": 8761}, manager="assistant"), 400)
        on = call(base, "/api/v1/assistant", {"agent_auto_naming_interval_hours": 24}, manager="assistant")
        self.assertEqual((on["agent_auto_naming_interval_hours"], on["agent_auto_naming_max"]), (24, 1000))

    def test_an_older_hub_has_none_of_them(self):
        base = self.serve(MOCK_LEGACY_HUB="1")
        a = call(base, "/api/v1/assistant")
        self.assertFalse([k for k in FIELDS if k in a])


if __name__ == "__main__":
    unittest.main()
