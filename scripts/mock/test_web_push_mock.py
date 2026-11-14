"""mock_hub.py's MOCK_WEB_PUSH answers as the hub does (docs/protocol/protocol.md "Web Push", server/internal/hub/webpush_routes.go):
the key, a subscription and its status, the refusals, Send Test, and an older hub without it. Run:
python3 -m unittest scripts/mock/test_web_push_mock.py (or from scripts/mock)."""
import json
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import web_push_mock as m  # noqa: E402


class Fake:
    """The part of mock_hub.py's handler route() uses."""

    def __init__(self):
        self.json, self.status = None, None

    def send_json(self, obj, status=200):
        self.json, self.status = obj, status


def call(method, path, body=None):
    h = Fake()
    answered = m.route(h, method, path, json.dumps(body).encode() if body is not None else b"")
    return answered, h.status, h.json


SUB = {"endpoint": "https://fcm.googleapis.com/fcm/send/abc", "keys": {"p256dh": "pk", "auth": "au"}, "key": m.EXAMPLE_KEY,
       "locale": "en", "browser": "Chrome · macOS", "features": ["elicitation"]}


class WebPushMock(unittest.TestCase):
    def setUp(self):
        m.SUBS.clear()
        patcher = mock.patch.dict(os.environ, {"MOCK_WEB_PUSH": "1"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_subscribe_status_test_and_delete(self):
        self.assertEqual(call("GET", "/api/v1/push/web"), (True, 200, {"key": m.EXAMPLE_KEY}))
        self.assertEqual(call("GET", "/api/v1/push/web/web-1")[2], {"subscribed": False})
        _, status, st = call("PUT", "/api/v1/push/web/web-1", SUB)
        self.assertEqual(status, 200)
        self.assertEqual((st["subscribed"], st["service"], st["host"], st["current"], st["browser"]),
                         (True, "FCM", "fcm.googleapis.com", True, "Chrome · macOS"))
        _, status, out = call("POST", "/api/v1/push/web/web-1/test")
        self.assertEqual((status, out), (200, {"ok": True, "push_status": 201}))
        self.assertIn("last_ok_at", call("GET", "/api/v1/push/web/web-1")[2])
        self.assertEqual(call("DELETE", "/api/v1/push/web/web-1")[2], {"ok": True, "removed": True})
        self.assertEqual(call("POST", "/api/v1/push/web/web-1/test")[1], 404)

    def test_refusals_as_the_hub(self):
        for body, code, status in (
            ({**SUB, "endpoint": "https://example.com/push"}, "unknown_push_service", 400),
            ({**SUB, "keys": {}}, "bad_keys", 400),
            ({**SUB, "key": "BOld"}, "key_changed", 409),
        ):
            _, got, out = call("PUT", "/api/v1/push/web/web-2", body)
            self.assertEqual((got, out["code"]), (status, code))
        self.assertEqual(call("PUT", "/api/v1/push/web/web-2", {**SUB, "key": "BOld"})[2]["key"], m.EXAMPLE_KEY)

    def test_a_failing_test(self):
        call("PUT", "/api/v1/push/web/web-3", SUB)
        with mock.patch.dict(os.environ, {"MOCK_WEB_PUSH_TEST": "fail"}):
            _, status, out = call("POST", "/api/v1/push/web/web-3/test")
        self.assertEqual((status, out["code"], out["push_status"]), (502, "push_failed", 410))
        st = call("GET", "/api/v1/push/web/web-3")[2]
        self.assertEqual((st["status"], "failed_at" in st), (410, True))

    def test_an_older_hub_answers_none_of_it(self):
        with mock.patch.dict(os.environ, {"MOCK_WEB_PUSH": ""}):
            self.assertEqual(call("GET", "/api/v1/push/web"), (False, None, None))

    def test_services_by_host(self):
        self.assertEqual([m.service(h) for h in ("fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com",
                                                 "wns2-par02p.notify.windows.com", "notfcm.googleapis.com")],
                         ["FCM", "Mozilla", "Apple", "Windows", ""])


if __name__ == "__main__":
    unittest.main()
