"""The mock hub's several managers (docs/assistant/managers.md "Clients (step 4)"), served the way the real hub serves them.

Starts scripts/mock/mock_hub.py on a free port with a HOME of its own (MOCK_EXTENSIONS=1 MOCK_MANAGER_NAS=1) and checks:
- a POST with a body that is not JSON (an image upload) is answered as before: only the move routes read JSON;
- the merged extensions list has the built-ins under every installed manager (each manager's extmgr adds them);
- a computer of an offline manager is offline with the hub's words and no error_code, its session (there is one) in state
  unknown;
- each manager keeps its own inbox mutes and rules: one set for another manager covers only its items, and an offline
  one answers 503 assistant_offline;
- the roster's inbox_needs_you is what the merged inbox lists open under "Needs you" for each online manager, absent
  for an offline or installing one, and follows an item being handled; the merged inbox leaves out the offline one's
  items, as the hub leaves out a manager that doesn't answer;
- a computer added with add.manager (MOCK_ADD_COMPUTER=1) is that manager's in the reply and in /machines (an offline
  manager's is not listed until a manager that is online has it); a manager the roster lacks is 404 no_manager, one
  being installed 409 installing, and neither adds a computer.

    python3 scripts/test/test_mock_hub_managers.py
"""
from concurrent.futures import ThreadPoolExecutor
import json
import os
import socket
import subprocess
import sys
import tempfile
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


class MockHubManagersTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.port = free_port()
        cls.home = tempfile.mkdtemp(prefix="mock-managers-")
        env = {k: v for k, v in os.environ.items() if not k.lower().endswith("_proxy")}
        env.update(HOME=cls.home, PORT=str(cls.port), MOCK_EXTENSIONS="1", MOCK_MANAGER_NAS="1", MOCK_ADD_COMPUTER="1",
                   MOCK_ADD_COMPUTER_JOIN="0", MOCK_CHANNELS="1")
        cls.proc = subprocess.Popen([sys.executable, str(ROOT / "scripts/mock/mock_hub.py")], env=env,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + 30
        while time.time() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", cls.port), timeout=0.5):
                    return
            except OSError:
                time.sleep(0.2)
        cls.proc.kill()
        raise RuntimeError("the mock hub did not start within 30 s")

    @classmethod
    def tearDownClass(cls):
        cls.proc.terminate()
        try:
            cls.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.proc.kill()

    def call(self, method, path, body=None, content_type="application/json", manager=None):
        headers = {"Authorization": "Bearer mock", "Content-Type": content_type}
        if manager:
            headers["X-Persona-Manager"] = manager
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}{path}", data=body, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, json.loads(resp.read() or b"null")
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"null")

    def test_personal_agent_entry_states_are_consistent(self):
        for state in ("configured", "empty", "missing", "disabled", "no_accounts", "none"):
            with self.subTest(state=state):
                status, answer = self.call("GET", "/mock/personal-agents?reset=1&state=" + state)
                self.assertEqual(status, 200, answer)
                _, sources = self.call("GET", "/api/v1/agents")
                _, sessions = self.call("GET", "/api/v1/agents/sessions")
                self.assertEqual(bool(sessions["sessions"]), state == "configured")
                if state == "none":
                    self.assertEqual(sources["sources"], [])
                else:
                    source = sources["sources"][0]
                    self.assertEqual(bool(source["accounts"]), state in ("configured", "empty"))
                    self.assertEqual(source.get("setup"), state if state in ("missing", "disabled", "no_accounts") else None)
        self.call("GET", "/mock/personal-agents?reset=1")

    def test_integrations_conflicts_and_channel_prefs_are_manager_scoped(self):
        manager, other = "m-3fa21c", "assistant"
        profile = "/api/v1/assistant"
        channel = "/api/v1/channels/wechat"

        def put(path, **fields):
            return self.call("PUT", path, json.dumps(fields).encode(), manager=manager)

        self.assertIs(self.call("GET", profile, manager=manager)[1]["integrations_v2"], False)
        with ThreadPoolExecutor(max_workers=2) as pool:
            writers = [pool.submit(put, profile, integrations_v2=True, expected_integrations_v2=False) for _ in range(2)]
        replies = dict(writer.result() for writer in writers)
        self.assertIs(replies[200]["integrations_v2"], True)
        self.assertEqual(replies[409]["code"], "configuration_conflict")
        self.assertIs(self.call("GET", profile, manager=other)[1]["integrations_v2"], False)

        status, detail = put(channel, thread_mode="per_group_and_topic", thread_idle_hours=24)
        self.assertEqual(status, 200)
        self.assertEqual(detail["target_override"], {"mode": "groups_and_topics"})
        self.assertEqual(self.call("GET", channel, manager=other)[1]["thread_mode"], "channel_default")
        for invalid in ({"thread_mode": "group"}, {"thread_idle_hours": -1}, {"thread_idle_hours": 87601},
                        {"thread_idle_hours": True}):
            self.assertEqual(put(channel, **invalid)[0], 400)
        self.assertEqual(put(profile, integrations_v2=False, expected_integrations_v2=True)[0], 200)
        detail = self.call("GET", channel, manager=manager)[1]
        self.assertNotIn("target_override", detail)
        self.assertEqual((detail["thread_mode"], detail["thread_idle_hours"]), ("per_group_and_topic", 24))
        self.assertNotIn("thread_idle_hours", put(channel, thread_idle_hours=0)[1])

        builtin = "/api/v1/channels/feishu"
        for key, value in (("thread_mode", "per_topic"), ("thread_idle_hours", 24)):
            self.assertNotIn(key, self.call("GET", builtin)[1])
            self.assertEqual(put(builtin, **{key: value})[0], 400)

    def test_a_raw_body_post_is_answered(self):
        image = bytes(range(256)) * 4  # not JSON
        status, answer = self.call("POST", "/api/v1/lark/conversations/f1/files?client_id=c1&name=a.png", image, "image/png")
        self.assertEqual(status, 200, answer)
        self.assertTrue(answer.get("message_id"), answer)

    def test_the_move_routes_read_json_only(self):
        status, _ = self.call("POST", "/api/v1/managers/m-3fa21c/machines", b"\x89PNG not json", "image/png")
        self.assertEqual(status, 400)
        status, answer = self.call("POST", "/api/v1/managers/m-3fa21c/machines", json.dumps({"machine_id": "studio"}).encode())
        self.assertEqual((status, answer.get("manager"), answer.get("from")), (200, "m-3fa21c", "assistant"), answer)
        status, _ = self.call("POST", "/api/v1/managers/assistant/machines", json.dumps({"machine_id": "studio"}).encode())
        self.assertEqual(status, 200)

    def test_built_ins_are_every_managers(self):
        status, answer = self.call("GET", "/api/v1/extensions")
        self.assertEqual(status, 200, answer)
        items = answer["extensions"]
        builtins = sorted({x["id"] for x in items if x.get("source") == "builtin"})
        self.assertTrue(builtins, "the mock lists built-in extensions")
        installed = sorted(["assistant", "m-3fa21c", "m-8b04d2"])
        for ext in builtins:
            self.assertEqual(sorted(x["manager"] for x in items if x["id"] == ext), installed, ext)
        # the default's answer first, every item stamped, the others' own ones under them
        self.assertEqual(items[0]["manager"], "assistant")
        self.assertTrue(all(x.get("manager") for x in items))
        for mid in ("m-3fa21c", "m-8b04d2"):
            self.assertTrue([x for x in items if x["manager"] == mid and x.get("source") != "builtin"], mid)

    def test_an_offline_managers_computer(self):
        _, answer = self.call("GET", "/api/v1/machines")
        nas = next(m for m in answer["machines"] if m["id"] == "nas")
        self.assertEqual((nas["manager"], nas["online"], nas.get("error_code")), ("m-8b04d2", False, None))
        self.assertTrue(nas.get("error"))
        _, answer = self.call("GET", "/api/v1/sessions")
        theirs = [s for s in answer["sessions"] if s.get("manager") == "m-8b04d2"]
        self.assertTrue(theirs, "the offline manager's computer has a session to check")
        for s in theirs:
            self.assertEqual((s.get("machine_id"), s.get("state")), ("nas", "unknown"), s)

    def test_a_computer_added_to_a_manager(self):
        def machines():
            _, answer = self.call("GET", "/api/v1/machines")
            return {m["id"]: m for m in answer["machines"]}
        body = lambda name, manager: json.dumps({"add": {"name": name, "os": "linux", "manager": manager}}).encode()
        status, answer = self.call("POST", "/api/v1/auth/pair-codes", body("Work Box", "m-3fa21c"))
        self.assertEqual((status, answer.get("machine"), answer.get("manager"), answer.get("added")), (200, "work-box", "m-3fa21c", True), answer)
        self.assertEqual(machines()["work-box"]["manager"], "m-3fa21c")
        # the same name again (pending, never connected) for another manager: the same computer, moved there
        status, answer = self.call("POST", "/api/v1/auth/pair-codes", body("Work Box", "assistant"))
        self.assertEqual((status, answer.get("machine"), answer.get("manager"), answer.get("added")), (200, "work-box", "assistant", None), answer)
        self.assertEqual(machines()["work-box"]["manager"], "assistant")
        # an offline manager's: accepted, but not listed until that manager reports it (the real hub lists what a
        # manager reports); the same name again for an online one lists it there
        status, answer = self.call("POST", "/api/v1/auth/pair-codes", body("Home Box", "m-8b04d2"))
        self.assertEqual((status, answer.get("machine"), answer.get("manager"), answer.get("added")), (200, "home-box", "m-8b04d2", True), answer)
        self.assertNotIn("home-box", machines())
        status, answer = self.call("POST", "/api/v1/auth/pair-codes", json.dumps({"add": {"name": "Home Box"}}).encode())
        self.assertEqual((status, answer.get("machine"), answer.get("manager"), answer.get("added")), (200, "home-box", "m-8b04d2", None), answer)
        self.assertNotIn("home-box", machines())
        status, answer = self.call("POST", "/api/v1/auth/pair-codes", body("Home Box", "m-3fa21c"))
        self.assertEqual((status, answer.get("machine"), answer.get("manager")), (200, "home-box", "m-3fa21c"), answer)
        self.assertEqual(machines()["home-box"]["manager"], "m-3fa21c")
        before = set(machines())
        for manager, want in (("m-ffffff", (404, "no_manager")), ("m-c1e9f0", (409, "installing"))):
            status, answer = self.call("POST", "/api/v1/auth/pair-codes", body("Nowhere Box", manager))
            self.assertEqual((status, answer.get("code")), want, answer)
        self.assertEqual(set(machines()), before, "a refused add added a computer")

    def test_sessions_of_one_manager(self):
        _, everyone = self.call("GET", "/api/v1/sessions")
        owners = {s["manager"] for s in everyone["sessions"]}
        self.assertGreater(len(owners), 1, "the mock has sessions of several managers")
        for mid in sorted(owners):
            _, answer = self.call("GET", "/api/v1/sessions?manager=" + mid)
            self.assertEqual([(s["machine_id"], s["session_id"]) for s in answer["sessions"]],
                             [(s["machine_id"], s["session_id"]) for s in everyone["sessions"] if s["manager"] == mid], mid)
        status, answer = self.call("GET", "/api/v1/sessions?manager=m-ffffff")
        self.assertEqual((status, answer.get("code")), (404, "no_manager"))
        # the header alone filters nothing: this list was never routed by it
        _, answer = self.call("GET", "/api/v1/sessions", manager="m-3fa21c")
        self.assertEqual(len(answer["sessions"]), len(everyone["sessions"]))

    def test_the_roster_counts_each_inbox(self):
        def roster():
            _, answer = self.call("GET", "/api/v1/managers")
            return {m["id"]: m for m in answer["managers"]}

        def needs_you(x):  # as the apps count: an open item under "Needs you" (the hub's status, else the tier)
            status = x.get("status") or (x["tier"] if x.get("tier") in ("fyi", "noise") else "action")
            return not x.get("done_at") and status == "action"

        def listed():
            _, answer = self.call("GET", "/api/v1/inbox?sources=all")
            counts = {}
            for x in answer["items"]:
                if needs_you(x):
                    counts[x["manager"]] = counts.get(x["manager"], 0) + 1
            return counts, answer["items"]

        managers, (counts, items) = roster(), listed()
        for mid in ("assistant", "m-3fa21c"):
            self.assertEqual(managers[mid].get("inbox_needs_you"), counts.get(mid, 0), mid)
        self.assertGreater(managers["assistant"]["inbox_needs_you"], 0)
        self.assertGreater(managers["m-3fa21c"]["inbox_needs_you"], 0)  # the other manager's badge has something to show
        for mid in ("m-8b04d2", "m-c1e9f0"):  # offline, installing
            self.assertNotIn("inbox_needs_you", managers[mid], mid)
        self.assertNotIn("m-8b04d2", {x["manager"] for x in items})  # the offline one's items aren't merged in
        mine = next(x for x in items if x["manager"] == "assistant" and needs_you(x))
        body = json.dumps({"done": True, "items": [{"id": mine["id"]}]}).encode()
        status, _ = self.call("POST", "/api/v1/inbox/done", body)
        self.assertEqual(status, 200)
        try:
            self.assertEqual(roster()["assistant"]["inbox_needs_you"], counts["assistant"] - 1)
        finally:
            self.call("POST", "/api/v1/inbox/done", json.dumps({"done": False, "items": [{"id": mine["id"]}]}).encode())
        self.assertEqual(roster()["assistant"]["inbox_needs_you"], counts["assistant"])

    def test_each_managers_mutes_and_rules(self):
        _, listed = self.call("GET", "/api/v1/inbox?sources=all")
        theirs = next(x for x in listed["items"] if x["manager"] == "m-3fa21c" and x.get("mutes"))
        tag = theirs["mutes"][0]["tag"]
        status, answer = self.call("POST", "/api/v1/inbox/mutes", json.dumps({"item_id": theirs["id"], "tag": tag}).encode(), manager="m-3fa21c")
        self.assertEqual(status, 200, answer)
        _, mine = self.call("GET", "/api/v1/inbox/mutes")
        _, work = self.call("GET", "/api/v1/inbox/mutes", manager="m-3fa21c")
        self.assertNotIn(answer["mute"]["id"], [m["id"] for m in mine["mutes"]])
        self.assertIn(answer["mute"]["id"], [m["id"] for m in work["mutes"]])
        status, _ = self.call("DELETE", "/api/v1/inbox/mutes/" + answer["mute"]["id"], manager="m-3fa21c")
        self.assertEqual(status, 200)
        status, answer = self.call("POST", "/api/v1/inbox/policies", json.dumps({"item_id": theirs["id"], "tag": tag, "tier": "fyi"}).encode(), manager="m-3fa21c")
        self.assertEqual(status, 200, answer)
        _, mine = self.call("GET", "/api/v1/inbox/policies")
        self.assertNotIn(answer["policy"]["id"], [p["id"] for p in mine["policies"]])
        status, _ = self.call("DELETE", "/api/v1/inbox/policies/" + answer["policy"]["id"], manager="m-3fa21c")
        self.assertEqual(status, 200)
        for path in ("/api/v1/inbox/mutes", "/api/v1/inbox/policies"):
            status, answer = self.call("GET", path, manager="m-8b04d2")
            self.assertEqual((status, answer.get("code")), (503, "assistant_offline"), path)


class MockHubHiddenBuiltinTests(MockHubManagersTests):
    """MOCK_MANAGER_HIDDEN=1 (#9055): 工作 the default with every computer, the built-in assistant kept out of the
    switcher; hiding is refused while it is the default or has a computer, showing it always works."""

    @classmethod
    def setUpClass(cls):
        os.environ["MOCK_MANAGER_HIDDEN"] = "1"
        try:
            super().setUpClass()
        finally:
            del os.environ["MOCK_MANAGER_HIDDEN"]

    def builtin(self):
        status, roster = self.call("GET", "/api/v1/managers")
        self.assertEqual(status, 200)
        return next(m for m in roster["managers"] if m.get("builtin")), roster["default"]

    def test_hide_and_show_the_built_in(self):
        m, default = self.builtin()
        self.assertEqual((default, m.get("hide"), m.get("hidden")), ("m-3fa21c", True, True))
        self.assertEqual(self.call("PATCH", "/api/v1/managers/m-3fa21c", json.dumps({"hide": True}).encode())[0], 400)
        status, answer = self.call("PATCH", "/api/v1/managers/assistant", json.dumps({"hide": False}).encode())
        self.assertEqual((status, answer["manager"].get("hide"), answer["manager"].get("hidden")), (200, None, None))
        status, answer = self.call("PATCH", "/api/v1/managers/assistant", json.dumps({"hide": True, "default": True}).encode())
        self.assertEqual((status, answer.get("code")), (409, "builtin_in_use"))
        status, answer = self.call("PATCH", "/api/v1/managers/assistant", json.dumps({"hide": True}).encode())
        self.assertEqual((status, answer["manager"].get("hidden")), (200, True))
        # made the default: shown again, the choice kept
        self.call("PATCH", "/api/v1/managers/assistant", json.dumps({"default": True}).encode())
        m, _ = self.builtin()
        self.assertEqual((m.get("hide"), m.get("hidden")), (True, None))
        self.call("PATCH", "/api/v1/managers/m-3fa21c", json.dumps({"default": True}).encode())

    # the inherited tests run against the default seed only
    test_personal_agent_entry_states_are_consistent = test_a_raw_body_post_is_answered = None
    test_the_move_routes_read_json_only = test_built_ins_are_every_managers = test_an_offline_managers_computer = None
    test_a_computer_added_to_a_manager = test_sessions_of_one_manager = test_the_roster_counts_each_inbox = None
    test_each_managers_mutes_and_rules = None


if __name__ == "__main__":
    unittest.main()
