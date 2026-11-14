"""The mock hub's Feishu chat lists decode in the app: every chat carries the keys the app's LarkConversation requires,
which the hub always sends (server/internal/lark/feed.go, no omitempty). Settings › Translation › Lark chats failed with "The data couldn't be read
because it is missing." on the mock while they were missing."""
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

# LarkConversation's non-optional properties (apple/Persona/Views/Inbox/Lark/LarkModels.swift), by their JSON keys.
REQUIRED = {"id", "name", "is_group", "unread", "at_me", "muted", "preview", "is_mine"}


class MockHub:
    def __init__(self, **env):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.log = tempfile.TemporaryFile()
        self.process = subprocess.Popen(["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
                                        env=dict(os.environ, PORT=str(self.port), **env), stdout=self.log, stderr=self.log)
        for _ in range(60):
            if self.process.poll() is not None:
                raise RuntimeError("mock hub exited before startup")
            try:
                with urlopen(self.url("/api/v1/health"), timeout=1):
                    return
            except (URLError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("mock hub startup timed out")

    def url(self, path):
        return f"http://127.0.0.1:{self.port}{path}"

    def get(self, path):
        with urlopen(self.url(path), timeout=3) as response:
            return json.load(response)

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.log.close()


class LarkChatListTests(unittest.TestCase):
    def test_list_performance_fixture_has_real_pagination(self):
        hub = MockHub(MOCK_LARK="1", MOCK_LARK_LIST="180")
        self.addCleanup(hub.stop)
        first = hub.get("/api/v1/lark/conversations?scope=chats&limit=60")
        second = hub.get("/api/v1/lark/conversations?scope=chats&limit=60&cursor=" + first["next_cursor"])
        self.assertEqual(len(first["conversations"]), 60)
        self.assertEqual(len(second["conversations"]), 60)
        self.assertTrue(second["next_cursor"])
        self.assertFalse({c["id"] for c in first["conversations"]} & {c["id"] for c in second["conversations"]})

    def test_unread_fixture_has_matches_beyond_a_read_only_page(self):
        hub = MockHub(MOCK_LARK="1", MOCK_LARK_UNREAD="1")
        self.addCleanup(hub.stop)
        first = hub.get("/api/v1/lark/conversations?scope=chats&limit=60")
        self.assertEqual(len(first["conversations"]), 60)
        self.assertFalse(any(c["unread"] or c.get("marked_unread") for c in first["conversations"]))
        second = hub.get("/api/v1/lark/conversations?scope=chats&limit=60&cursor=" + first["next_cursor"])
        unread = hub.get("/api/v1/lark/conversations?scope=unread&limit=60")
        self.assertEqual(unread["conversations"], second["conversations"])
        self.assertTrue(any(c["unread"] == 0 and c.get("marked_unread") for c in unread["conversations"]))
        self.assertTrue(any(c.get("pinned") for c in unread["conversations"]))
        self.assertEqual(unread["next_cursor"], "")
        self.assertEqual(unread["account_id"], first["account_id"])

    def check(self, **env):
        hub = MockHub(**env)
        self.addCleanup(hub.stop)
        chats = hub.get("/api/v1/lark/conversations?scope=chats&limit=60")["conversations"]
        chats += hub.get("/api/v1/lark/pinned")["conversations"]
        self.assertTrue(chats)
        for chat in chats:
            self.assertEqual(REQUIRED - chat.keys(), set(), chat.get("name"))

    def test_the_default_chats_decode(self):
        self.check()

    def test_the_feishu_page_chats_decode(self):
        self.check(MOCK_LARK="1")

    def test_the_topic_group_chats_decode(self):
        self.check(MOCK_LARK_TOPIC="1")


class LarkTopicGroupTests(unittest.TestCase):
    """MOCK_LARK_TOPIC=1 (#1087): the topic group the topic cards are drawn from."""

    def setUp(self):
        self.hub = MockHub(MOCK_LARK_TOPIC="1")
        self.addCleanup(self.hub.stop)

    def test_the_topic_group_lists_its_topics(self):
        chat = next(c for c in self.hub.get("/api/v1/lark/conversations")["conversations"] if c["id"] == "f4")
        self.assertTrue(chat["is_topic"])
        self.assertEqual(REQUIRED - chat.keys(), set())
        item = next(i for i in self.hub.get("/api/v1/inbox")["items"] if i["id"] == "lark:f4:405")
        self.assertTrue(item["lark"]["is_topic"])
        page = self.hub.get("/api/v1/lark/conversations/f4/messages?topic=1")
        self.assertTrue(page["topic"])
        # Three in a row from the bot, minutes apart: the chat's rules would run them together.
        self.assertEqual([m["position"] for m in page["messages"] if m.get("sender_type") == "bot"], ["401", "402", "403"])

    def test_the_topic_group_has_recalled_and_edited_topics(self):
        # The gateway's message display on topic cards: an edited topic with an old version, recalled topics kept and not kept by the Lark gateway, and
        # a recalled reply (kept) as the edited topic's newest.
        page = self.hub.get("/api/v1/lark/conversations/f4/messages?topic=1")
        by = {m["position"]: m for m in page["messages"]}
        self.assertTrue(by["397"]["edited"])
        self.assertEqual([v["text"] for v in by["397"]["edit_history"]], ["周三上架"])
        self.assertEqual((by["398"]["recalled"], by["398"]["retained"]), (True, True))
        self.assertEqual((by["399"]["recalled"], by["399"]["retained"]), (True, False))
        newest = self.hub.get("/api/v1/lark/messages/m397/thread?limit=3")["messages"][-1]
        self.assertEqual((newest["id"], newest["recalled"], newest["retained"]), ("m397-r2", True, True))

    def test_a_thread_answers_with_the_topic_then_the_newest_replies(self):
        page = self.hub.get("/api/v1/lark/messages/m402/thread?limit=3")
        self.assertEqual([m["id"] for m in page["messages"]], ["m402", "m402-r4", "m402-r5", "m402-r6"])
        self.assertTrue(page["has_older"])
        older = self.hub.get("/api/v1/lark/messages/m402/thread?limit=3&before=" + page["older_cursor"])
        self.assertEqual([m["id"] for m in older["messages"]], ["m402", "m402-r1", "m402-r2", "m402-r3"])
        self.assertEqual([m["id"] for m in self.hub.get("/api/v1/lark/messages/m403/thread?limit=3")["messages"]], ["m403", "m403-r1"])
        self.assertEqual([m["id"] for m in self.hub.get("/api/v1/lark/messages/m401/thread?limit=3")["messages"]], ["m401"])

    def test_without_the_switch_there_is_no_topic_group(self):
        hub = MockHub()
        self.addCleanup(hub.stop)
        self.assertNotIn("f4", [c["id"] for c in hub.get("/api/v1/lark/conversations")["conversations"]])


class LarkDeliveryProofTests(unittest.TestCase):
    def test_exact_message_read_preserves_identity_and_ownership(self):
        hub = MockHub()
        self.addCleanup(hub.stop)
        messages = hub.get("/api/v1/lark/conversations/f2/messages")["messages"]
        for mine in (False, True):
            message = next(m for m in messages if m["is_mine"] == mine)
            proof = hub.get("/api/v1/lark/raw/messages/" + message["id"])["item"]
            self.assertEqual(proof, {"id": message["id"], "feedId": "f2", "isMine": mine, "localState": "synced"})


class LarkMeasuringTests(unittest.TestCase):
    """The switches docs/operations/testing.md "Jank measurement" measures the Feishu pages with (#674)."""

    def test_rich_history_has_cards_pictures_and_posts(self):
        hub = MockHub(MOCK_LARK_RICH="1")
        self.addCleanup(hub.stop)
        kinds = {m["type"] for m in hub.get("/api/v1/lark/conversations/f3/messages?around=300")["messages"]}
        self.assertTrue({"CARD", "IMAGE", "POST", "TEXT"} <= kinds, kinds)

    def test_lark_churn_adds_messages_round_by_round(self):
        hub = MockHub()
        self.addCleanup(hub.stop)
        before = hub.get("/api/v1/lark/conversations/f3/messages")["messages"][-1]["position"]
        hub.get("/mock/lark_churn?feed=f3&n=3&ms=50&new=1")
        for _ in range(50):
            if hub.get("/mock/lark_churned")["rounds"] == 3:
                break
            time.sleep(0.05)
        self.assertEqual(hub.get("/mock/lark_churned")["rounds"], 3)
        after = hub.get("/api/v1/lark/conversations/f3/messages")["messages"][-1]["position"]
        self.assertEqual(int(after), int(before) + 3)

    def test_a_test_can_change_how_long_a_send_takes(self):
        hub = MockHub(MOCK_LARK_SEND_SECONDS="3")
        self.addCleanup(hub.stop)
        self.assertEqual(hub.get("/mock/lark_send_delay?s=0.1"), {"seconds": 0.1})
        start = time.monotonic()
        request = Request(hub.url("/api/v1/lark/conversations/f3/messages"), data=json.dumps({"text": "快"}).encode(),
                          headers={"Content-Type": "application/json", "Authorization": "Bearer mock"})
        with urlopen(request, timeout=3) as response:
            self.assertTrue(json.load(response)["message_id"])
        self.assertLess(time.monotonic() - start, 2)
        self.assertEqual(hub.get("/mock/lark_send_delay"), {"seconds": 3.0}, "no s puts the start value back")


if __name__ == "__main__":
    unittest.main()
