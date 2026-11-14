"""Delayed mock copies and archives report progress and cancellation over the real HTTP routes.

Run with MOCK_FILES=1 MOCK_FILE_OPS=1 MOCK_FILE_OP_DELAY=3 on a disposable mock hub:

    python3 scripts/test/test_mock_file_operations.py
"""
import http.client
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class MockFileOperationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.port = free_port()
        cls.home = tempfile.mkdtemp(prefix="mock-file-operations-")
        env = {k: v for k, v in os.environ.items() if not k.lower().endswith("_proxy")}
        env.update(HOME=cls.home, PORT=str(cls.port), MOCK_FILES="1", MOCK_FILE_OPS="1", MOCK_FILE_OP_DELAY="3")
        cls.proc = subprocess.Popen([sys.executable, str(ROOT / "scripts/mock/mock_hub.py")], env=env,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", cls.port), timeout=0.5):
                    return
            except OSError:
                time.sleep(0.1)
        cls.proc.kill()
        raise RuntimeError("the mock hub did not start within 30 s")

    @classmethod
    def tearDownClass(cls):
        cls.proc.terminate()
        try:
            cls.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.proc.kill()

    def call(self, path):
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}{path}", headers={"Authorization": "Bearer mock"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, json.loads(resp.read() or b"null")
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"null")

    def start_post(self, operation, body):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        result = {}

        def run():
            try:
                conn.request("POST", f"/api/v1/machines/macbook/files/{operation}", json.dumps(body),
                             {"Authorization": "Bearer mock", "Content-Type": "application/json"})
                resp = conn.getresponse()
                result.update(status=resp.status, answer=json.loads(resp.read() or b"null"))
            except (OSError, http.client.HTTPException) as e:
                result["disconnected"] = type(e).__name__

        thread = threading.Thread(target=run, daemon=True)
        thread.start()
        return conn, thread, result

    def wait_operation(self, oid, predicate):
        deadline = time.monotonic() + 3
        last = None
        while time.monotonic() < deadline:
            status, last = self.call(f"/api/v1/machines/macbook/files/operations/{oid}")
            if status == 200 and predicate(last):
                return last
            time.sleep(0.05)
        self.fail(f"operation {oid} did not reach the expected state: {last}")

    def names(self):
        status, answer = self.call("/api/v1/machines/macbook/fs?path=~")
        self.assertEqual(status, 200, answer)
        return {entry["name"] for entry in answer["entries"]}

    def test_cancelled_copy_has_no_output(self):
        oid = "synthetic-copy-cancel"
        conn, thread, result = self.start_post("copy", {
            "operation_id": oid, "paths": ["/Users/me/notes.txt"], "cwd": "", "dir": "~", "conflict": "keep",
        })
        self.wait_operation(oid, lambda op: op["state"] == "running" and op["bytes_done"] > 0)
        if conn.sock:
            conn.sock.shutdown(socket.SHUT_RDWR)
        conn.close()
        self.wait_operation(oid, lambda op: op["state"] == "cancelled")
        thread.join(timeout=3)
        self.assertFalse(thread.is_alive(), "the cancelled POST did not stop")
        self.assertIn("disconnected", result)
        self.assertNotIn("notes 2.txt", self.names())

    def test_archive_progress_finishes_with_the_original_post(self):
        oid = "synthetic-zip-done"
        conn, thread, result = self.start_post("zip", {
            "operation_id": oid, "paths": ["/Users/me/notes.txt"], "cwd": "", "dir": "~", "name": "progress.zip",
        })
        try:
            self.wait_operation(oid, lambda op: op["state"] == "running" and op["bytes_done"] > 0)
            thread.join(timeout=6)
            self.assertFalse(thread.is_alive(), "the archive POST did not finish")
            self.assertEqual(result.get("status"), 200, result)
            self.assertEqual(result["answer"]["name"], "progress.zip")
            done = self.wait_operation(oid, lambda op: op["state"] == "done")
            self.assertEqual((done["files_done"], done["files_total"]), (1, 1))
            self.assertEqual((done["bytes_done"], done["bytes_total"]), (100, 100))
            self.assertIn("progress.zip", self.names())
        finally:
            conn.close()


if __name__ == "__main__":
    unittest.main()
