"""Tests mock_hub's fixtures for the clients' part of the main-chat availability follow-ups: the suggested standing rule
(#5802), a decision held for the briefing (#5803), the briefing's recommended setup (#5804) and the model catalog that
lists the background supervisor's purpose with descriptions (#5806)."""
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


def call(base, path, body=None, method=None):
    request = Request(base + path, data=None if body is None else json.dumps(body).encode(),
                      method=method or ("GET" if body is None else "POST"),
                      headers={"Authorization": "Bearer mock", "Content-Type": "application/json"})
    with urlopen(request, timeout=3) as response:
        return json.load(response)


def status(base, path, body=None, method=None):
    try:
        call(base, path, body, method)
        return 200
    except HTTPError as e:
        e.close()
        return e.code


def feed(base):
    conversation = call(base, "/api/v1/assistant")["conversations"][0]["session_id"]
    return call(base, f"/api/v1/assistant/conversations/{conversation}/feed")


class AvailabilityFollowupsMockTests(unittest.TestCase):
    def serve(self, **env):
        base, process, log = start(env)
        self.addCleanup(stop, process, log)
        return base

    def test_seeded_standing_authorizations_are_listed_and_revoked_as_on_the_hub(self):
        base = self.serve(MOCK_STANDING_AUTHORIZATIONS="1")
        listed = call(base, "/api/v1/assistant/standing-authorizations")
        self.assertEqual([g["id"] for g in listed["authorizations"]], ["sa_mock_weather", "sa_mock_blog"])
        self.assertEqual(listed["authorizations"][0]["actions"], ["merge", "deploy-test"])
        self.assertEqual(call(base, "/api/v1/assistant/standing-authorizations/sa_mock_blog", method="DELETE"), {"ok": True, "revision": 2})
        self.assertEqual([g["id"] for g in call(base, "/api/v1/assistant/standing-authorizations")["authorizations"]], ["sa_mock_weather"])
        call(base, "/mock/reset")
        self.assertEqual(len(call(base, "/api/v1/assistant/standing-authorizations")["authorizations"]), 2, "reset seeds them again")

    def test_standing_authorizations_without_full_access_or_with_failing_revokes(self):
        base = self.serve(MOCK_STANDING_AUTHORIZATIONS="forbidden")
        self.assertEqual(status(base, "/api/v1/assistant/standing-authorizations"), 403)
        base = self.serve(MOCK_STANDING_AUTHORIZATIONS="fail")
        self.assertEqual(status(base, "/api/v1/assistant/standing-authorizations/sa_mock_weather", method="DELETE"), 503)
        self.assertEqual(len(call(base, "/api/v1/assistant/standing-authorizations")["authorizations"]), 2, "a failed revoke keeps it")

    def test_a_suggested_rule_rides_on_each_kind_of_card_and_is_answered_once(self):
        base = self.serve(MOCK_RULE_SUGGESTIONS="1", MOCK_TODO_ASKS="1", MOCK_TASKS="1")
        todo = next(t for t in call(base, "/api/v1/todos")["todos"] if t["id"] == "todo_review")
        self.assertEqual(todo["ask"]["suggestion"]["creates"], "standing_authorization")
        self.assertEqual(todo["ask"]["suggestion"]["scope"]["conditions"], ["ci_green", "no_conflicts"])
        card = next(x for x in call(base, "/api/v1/interactions")["interactions"] if x["id"] == "ix_tests")
        self.assertEqual((card["suggestion"]["kind"], card["suggestion"]["setting"]), ("delegate", "agents.delegate"))
        plan = feed(base)["plans"]["plan_mock_3"]
        self.assertEqual((plan["state"], plan["suggestion"]["creates"]), ("open", "tasks.trust"))
        self.assertTrue(all("suggestion" not in p for i, p in feed(base)["plans"].items() if i != "plan_mock_3"), "open plans only")

        made = call(base, "/api/v1/assistant/rule-suggestions/rs_mock_merge/always", {})
        self.assertEqual((made["kind"], made["creates"], made["revision"]), ("standing_authorization", "standing_authorization", 2))
        todo = next(t for t in call(base, "/api/v1/todos")["todos"] if t["id"] == "todo_review")
        self.assertNotIn("suggestion", todo["ask"])
        # the grant is listed where the apps list standing authorizations, and can be revoked there
        grants = call(base, "/api/v1/assistant/standing-authorizations")
        self.assertEqual((grants["revision"], [g["actions"] for g in grants["authorizations"]]), (2, [["merge"]]))
        self.assertEqual(grants["authorizations"][0], made["authorization"])
        self.assertEqual(status(base, "/api/v1/assistant/rule-suggestions/rs_mock_merge/always", {}), 404, "answered already")
        self.assertEqual(status(base, "/api/v1/assistant/rule-suggestions/rs_mock_merge/not-now", {}), 404)

        self.assertEqual(call(base, "/api/v1/assistant/rule-suggestions/rs_mock_start/always", {})["creates"], "tasks.trust")
        self.assertNotIn("suggestion", feed(base)["plans"]["plan_mock_3"])
        self.assertIn("macbook|/Users/me/code/weather|edit", [k["key"] for k in call(base, "/api/v1/task-trust")["kinds"]])

        self.assertEqual(call(base, "/api/v1/assistant/rule-suggestions/rs_mock_delegate/not-now", {}), {"ok": True})
        card = next(x for x in call(base, "/api/v1/interactions")["interactions"] if x["id"] == "ix_tests")
        self.assertNotIn("suggestion", card)
        self.assertEqual(status(base, "/api/v1/assistant/rule-suggestions/rs_never/always", {}), 404, "never earned")

        # /mock/reset offers them all again (a run that answers them starts each pass from the same cards)
        gone = call(base, "/api/v1/assistant/standing-authorizations/" + made["authorization"]["id"], method="DELETE")
        self.assertEqual(gone, {"ok": True, "revision": 3}, "the hub's answer")
        self.assertEqual(call(base, "/api/v1/assistant/standing-authorizations")["authorizations"], [])
        self.assertEqual(call(base, "/api/v1/assistant/standing-authorizations/sa_never", method="DELETE"), {"ok": True, "revision": 3},
                         "one that is gone already is ok too, as on the hub")
        call(base, "/mock/reset")
        self.assertNotIn("macbook|/Users/me/code/weather|edit", [k["key"] for k in call(base, "/api/v1/task-trust")["kinds"]])
        todo = next(t for t in call(base, "/api/v1/todos")["todos"] if t["id"] == "todo_review")
        self.assertEqual(todo["ask"]["suggestion"]["id"], "rs_mock_merge")
        self.assertEqual(call(base, "/api/v1/assistant/rule-suggestions/rs_mock_delegate/not-now", {}), {"ok": True})

    def test_without_the_flags_no_card_carries_any_of_it(self):
        base = self.serve(MOCK_TODO_ASKS="1", MOCK_TASKS="1")
        todo = next(t for t in call(base, "/api/v1/todos")["todos"] if t["id"] == "todo_review")
        self.assertNotIn("suggestion", todo["ask"])
        self.assertNotIn("for_briefing", todo["ask"])
        self.assertTrue(all("suggestion" not in x for x in call(base, "/api/v1/interactions")["interactions"]))
        got = feed(base)
        self.assertTrue(all("suggestion" not in p for p in got["plans"].values()))
        brief = next(i for i in got["items"] if i["kind"] == "brief")
        self.assertNotIn("setup", brief["body"])
        self.assertEqual(status(base, "/api/v1/assistant/rule-suggestions/rs_mock_merge/always", {}), 404)
        catalog = call(base, "/api/v1/model-config")
        self.assertNotIn("assistant.supervise", [p["id"] for p in catalog["purposes"]])
        self.assertTrue(all("description" not in p for p in catalog["purposes"]))
        self.assertNotIn("description", call(base, "/api/v1/model-config/purposes/assistant.supervise")["purpose"])

    def test_a_decision_held_for_the_briefing(self):
        base = self.serve(MOCK_ASK_HELD="1", MOCK_TODO_ASKS="1")
        asks = {t["id"]: t["ask"] for t in call(base, "/api/v1/todos")["todos"] if "ask" in t}
        self.assertIs(asks["todo_review"]["for_briefing"], True)
        self.assertEqual(asks["todo_review"]["outcome"], "decide")
        self.assertTrue(all("for_briefing" not in a for i, a in asks.items() if i != "todo_review"))

    def test_the_briefing_carries_the_recommended_setup(self):
        base = self.serve(MOCK_BRIEF_SETUP="1", MOCK_TASKS="1")
        brief = next(i for i in feed(base)["items"] if i["kind"] == "brief")
        setup = brief["body"]["setup"]
        self.assertEqual([x["id"] for x in setup], ["triage_wake", "supervisor_tier", "task_trust", "standing_authorizations", "delegate"])
        self.assertTrue(all(x["text"] and isinstance(x["count"], int) and x["setting"] for x in setup))

    def test_the_catalog_lists_the_background_supervisor_with_descriptions(self):
        base = self.serve(MOCK_SUPERVISE_LISTED="1")
        purposes = call(base, "/api/v1/model-config")["purposes"]
        self.assertEqual([p["id"] for p in purposes].count("assistant.supervise"), 1)
        self.assertEqual(purposes[-1]["id"], "assistant.supervise", "the last built-in purpose")
        own = call(base, "/api/v1/model-config/purposes/assistant.supervise")["purpose"]
        self.assertEqual(purposes[-1], own, "the row it answers by its id")
        self.assertEqual((own["category_id"], own["parent"], own["fields"]), ("assistant", "assistant", ["provider", "model", "effort"]))
        self.assertTrue(all(p.get("description") for p in purposes))


if __name__ == "__main__":
    unittest.main()
