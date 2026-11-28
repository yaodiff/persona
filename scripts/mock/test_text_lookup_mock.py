"""mock_hub.py's MOCK_TEXT_LOOKUP answers the text_lookup App API (#10977): the state, a lookup from the made-up
dictionary (a form to its lemma, an unknown word), save and remove, the unavailable refusal and /mock/text-lookup's
switches, each change announced as text_lookup_changed. Run: python3 -m unittest scripts/mock/test_text_lookup_mock.py
(or from scripts/mock)."""
import json
import os
import sys
import unittest

os.environ["MOCK_TEXT_LOOKUP"] = "1"
os.environ["MOCK_TEXT_LOOKUP_LATENCY"] = "0"
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import text_lookup_mock as m  # noqa: E402


class Fake:
    """The part of mock_hub.py's handler route() uses."""

    def __init__(self):
        self.json, self.status = None, None

    def send_json(self, obj, status=200):
        self.json, self.status = obj, status


class TextLookupMock(unittest.TestCase):
    def setUp(self):
        self.sent = []
        m.STATE.update(available=True, underline=False, fail=False, latency=0)
        m.SAVED.clear()
        m.SAVED.add("context")

    def call(self, method, path, body=None, q=None):
        h = Fake()
        answered = m.route(h, method, path, q or {}, json.dumps(body).encode() if body is not None else b"", self.sent.append)
        return answered, h.status, h.json

    def test_state_lookup_save_remove(self):
        _, status, st = self.call("GET", m.PATH)
        self.assertEqual(status, 200)
        self.assertEqual((st["available"], st["name"], st["underline_saved"], st["words"]), (True, "Relingo", False, ["context"]))
        _, _, e = self.call("POST", m.PATH + "/lookup", {"word": "Deployed", "context": "We deployed it."})
        self.assertEqual((e["found"], e["word"], e["lemma"], e["saved"]), (True, "Deployed", "deploy", False))
        self.assertTrue(e["phonetic"] and e["senses"][0]["pos"] == "v.")
        self.assertEqual(self.call("POST", m.PATH + "/lookup", {"word": "zyzzyva"})[2]["found"], False)
        self.assertEqual(self.call("POST", m.PATH + "/words", {"word": "deploy", "source": "chat"})[2], {"saved": True})
        self.assertIn("deploy", self.call("GET", m.PATH)[2]["words"])
        self.assertEqual(self.call("DELETE", m.PATH + "/words/deploy")[2], {"saved": False})
        self.assertNotIn("deploy", self.call("GET", m.PATH)[2]["words"])
        self.assertEqual(self.sent, [{"type": "text_lookup_changed"}] * 2)

    def test_unavailable_and_switches(self):
        self.call("GET", "/mock/text-lookup", q={"available": ["0"]})
        self.assertEqual(self.call("GET", m.PATH)[2], {"available": False, "underline_saved": False, "words": []})
        _, status, err = self.call("POST", m.PATH + "/lookup", {"word": "context"})
        self.assertEqual((status, err["code"]), (404, "text_lookup_unavailable"))
        self.call("GET", "/mock/text-lookup", q={"available": ["1"], "underline": ["1"], "fail": ["1"]})
        self.assertTrue(self.call("GET", m.PATH)[2]["underline_saved"])
        self.assertEqual(self.call("POST", m.PATH + "/lookup", {"word": "context"})[1], 502)
        self.assertEqual(len(self.sent), 2)

    def test_features_and_other_routes(self):
        self.assertEqual(m.features(["a"]), ["a", "text_lookup"])
        self.assertEqual(m.features(["text_lookup"]), ["text_lookup"])
        self.assertFalse(self.call("GET", "/api/v1/todos")[0])


if __name__ == "__main__":
    unittest.main()
