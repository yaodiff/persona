"""Exercise the mock's chat Inbox items (#15356, MOCK_KB_CHAT_INBOX): the example group's knowledge page lists its
items with the rows each holds, each item's read names the chat, and without the switch the hub answers as before."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import URLError
from urllib.request import ProxyHandler, build_opener

# The mock is on loopback: a proxy from the environment must not get in the way.
urlopen = build_opener(ProxyHandler({})).open


class KBChatInboxMockTests(unittest.TestCase):
    mode = "1"

    @classmethod
    def setUpClass(cls):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        cls.base = f"http://127.0.0.1:{port}"
        cls.log = tempfile.TemporaryFile()
        cls.process = subprocess.Popen(
            ["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
            env=dict(os.environ, PORT=str(port), MOCK_KB_CHAT_INBOX=cls.mode), stdout=cls.log, stderr=cls.log)
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

    def get(self, path):
        with urlopen(self.base + path, timeout=3) as response:
            return json.load(response)


class WithItems(KBChatInboxMockTests):
    def test_the_page_lists_the_items_with_their_rows(self):
        page = self.get("/api/v1/kb/chats/oc_team")
        self.assertEqual([x["id"] for x in page["inbox"]], ["lark:fteam:410", "lark:fteam:400"])
        self.assertNotIn("done_at", page["inbox"][0])
        self.assertIn("done_at", page["inbox"][1])
        recent = page["detail"]["recent"].split("\n")
        for x in page["inbox"]:
            for m in x["messages"]:
                # each row is in the page's text, under its day
                self.assertIn(m["line"], recent)
                self.assertLess(recent.index("## " + m["day"]), recent.index(m["line"]))
        # a search's page is the hub's own
        self.assertNotIn("inbox", self.get("/api/v1/kb/chats/oc_team?q=%E7%81%B0%E5%BA%A6"))

    def test_the_items_are_listed_and_name_their_chat(self):
        listed = {x["id"] for x in self.get("/api/v1/inbox?sources=all")["items"]}
        self.assertTrue({"lark:fteam:410", "lark:fteam:400"} <= listed)
        item = self.get("/api/v1/inbox/lark:fteam:410")
        self.assertEqual(item["kb_chat"], {"id": "oc_team", "path": "/kb/chats/oc_team"})
        self.assertNotIn("kb_chat", self.get("/api/v1/inbox/lark:f1:100"))
        with urlopen(self.base + "/mock/reset", timeout=3):
            pass
        self.assertIn("lark:fteam:410", {x["id"] for x in self.get("/api/v1/inbox?sources=all")["items"]})


class WithoutTheSwitch(KBChatInboxMockTests):
    mode = ""

    def test_the_hub_answers_as_before(self):
        self.assertNotIn("inbox", self.get("/api/v1/kb/chats/oc_team"))
        self.assertNotIn("lark:fteam:410", {x["id"] for x in self.get("/api/v1/inbox?sources=all")["items"]})


del KBChatInboxMockTests

if __name__ == "__main__":
    unittest.main()
