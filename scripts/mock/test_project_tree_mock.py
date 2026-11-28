"""Exercise the project tree mock (MOCK_KB_TREE: parents and milestones on /api/v1/kb/projects…) without a browser."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request, build_opener

# The mock is on loopback: a proxy from the environment must not get in the way.
urlopen = build_opener(ProxyHandler({})).open


class ProjectTreeMockTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        cls.base = f"http://127.0.0.1:{port}"
        cls.log = tempfile.TemporaryFile()
        cls.process = subprocess.Popen(
            ["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
            env=dict(os.environ, PORT=str(port), MOCK_KB_TREE="1"), stdout=cls.log, stderr=cls.log)
        cls.addClassCleanup(cls.stop)
        for _ in range(60):
            if cls.process.poll() is not None:
                raise RuntimeError("mock hub exited before startup")
            try:
                with urlopen(cls.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("mock hub startup timed out")

    @classmethod
    def stop(cls):
        cls.process.terminate()
        try:
            cls.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.process.kill()
            cls.process.wait(timeout=5)
        cls.log.close()

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = Request(self.base + path, data=data, method=method, headers={"Content-Type": "application/json"})
        try:
            with urlopen(req, timeout=5) as r:
                return r.status, json.loads(r.read())
        except HTTPError as e:
            return e.code, json.loads(e.read())

    def test_tree(self):
        self.call("GET", "/mock/reset")
        _, listed = self.call("GET", "/api/v1/kb/projects")
        parents = {x["id"]: x.get("parent", "") for x in listed["items"]}
        self.assertEqual(parents["press-kit"], "store-listing")
        self.assertEqual(parents["garden-app"], "")
        _, d = self.call("GET", "/api/v1/kb/projects/press-kit")
        self.assertEqual([a["id"] for a in d["tree"]["ancestors"]], ["q4-launch", "store-listing"])
        self.assertEqual(d["tree"]["depth"], 3)
        for target, code in (("press-kit", "parent_self"), ("store-listing", "parent_cycle"), ("nowhere", "parent_not_found")):
            status, out = self.call("PUT", "/api/v1/kb/projects/q4-launch" if target != "press-kit" else "/api/v1/kb/projects/press-kit", {"parent": target})
            self.assertEqual((status, out["code"]), (400, code))
        status, out = self.call("PUT", "/api/v1/kb/projects/docs-site", {"parent": "press-kit"})
        self.assertEqual((status, out["undo"], out["tree"]["depth"]), (200, {"parent": ""}, 4))
        status, out = self.call("PUT", "/api/v1/kb/projects/docs-site", {"milestone": {"title": "Search works", "target": "2026-12-20"}})
        self.assertEqual(out["tree"]["milestones"][0]["state"], "open")
        self.assertEqual(out["undo"], {"milestones": []})
        status, out = self.call("PUT", "/api/v1/kb/projects/docs-site", {"milestones": []})
        self.assertEqual(out["tree"]["milestones"], [])


if __name__ == "__main__":
    unittest.main()
