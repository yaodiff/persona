#!/usr/bin/env python3
"""The mock hub's domain registry (scripts/mock/mock_domains.py): what a domain registers, and that mock_hub.py
consults it at its central points (HTTP methods, WebSocket messages and close, features, --help, /mock/reset), and that
the blocks moved into domain modules stay out of mock_hub.py (MOVED).

    python3 scripts/mock/test_mock_domains.py

The end-to-end class starts the mock with one extra probe domain on a free port the kernel picks, and stops it.
"""
import ast
import base64
import contextlib
import http.client
import importlib
import io
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from unittest.mock import patch
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request as HTTPRequest, build_opener

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import mock_domains  # noqa: E402

LOOPBACK = "127.0.0.1"
OPENER = build_opener(ProxyHandler({}))  # loopback, never through a proxy in the shell's environment


class FakeHandler:
    """What a route handler is given by mock_hub.py, enough for the registry: path, send_json and bad_network (True:
    the hub is "down"; each call counted)."""

    def __init__(self, path, down=False):
        self.path, self.sent, self.down, self.network_checks = path, [], down, 0
        self.response_headers = {}

    def send_json(self, obj, status=200, headers=None):
        self.sent.append((status, obj))
        self.response_headers = headers or {}

    def bad_network(self):
        self.network_checks += 1
        return self.down


def registry(namespace=None):
    r = mock_domains.Registry({} if namespace is None else namespace)
    return r, mock_domains.Domain(r, "test")


class RegistryTests(unittest.TestCase):
    def test_starred_backup_preview_tracks_legacy_saves_and_reimport_is_idempotent(self):
        import starred_mock

        timestamp = "2026-11-20T00:00:00Z"
        events = []
        r, d = registry({
            "MANAGERS": {"default": "assistant"},
            "other_manager": lambda h: ("m-work", None) if "manager=m-work" in h.path else None,
            "is_read_only": lambda _h: False,
            "broadcast": events.append,
            "ago": lambda: timestamp,
        })
        with patch.dict(os.environ, {"MOCK_STARRED": "1", "MOCK_STARRED_BACKUP": "1"}):
            starred_mock.register(d)

        def send(method, suffix="", body=None, status=200):
            h = FakeHandler(starred_mock.BASE + suffix)
            payload = b"" if body is None else json.dumps(body).encode()
            self.assertTrue(r.http(h, method, h.path.partition("?")[0], payload))
            got, result = h.sent.pop()
            self.assertEqual(got, status, result)
            return result

        message = {"key": "sm" + "a" * 32, "conversation": "agent", "role": "assistant",
                   "title": "Old conversation", "speaker": "Agent", "text": "Saved words", "starred_at": timestamp}
        bundle = {"format": "persona.starred", "version": 1, "messages": [message]}
        self.assertEqual(send("GET", "/import"), {"can_import": True})
        first = send("POST", "/import/preview", bundle)
        self.assertEqual(first, {"revision": 0, "total": 1, "to_add": 1, "already_saved": 0, "available": 300})
        old_message = dict(message, title="a" * 199 + " ", text=" " * 4000,
                           omitted_chars=1, written_at="0001-01-01T00:00:00Z")
        self.assertEqual(send("POST", "/import/preview", dict(bundle, messages=[old_message])), first)
        self.assertEqual(send("GET")["stars"], [])
        self.assertEqual(events, [])
        send("POST", "/import/preview", dict(bundle, messages=[message, message]), status=400)
        item = {"machine_id": "box", "session_id": "example", "item_id": "example-message",
                "conversation": "agent", "role": "user", "text": "Newer saved words"}
        send("POST", body=item)
        send("POST", "/import", dict(bundle, revision=first["revision"]), status=409)

        fresh = send("POST", "/import/preview", bundle)
        imported = send("POST", "/import", dict(bundle, revision=fresh["revision"]))
        self.assertEqual(imported, {"revision": 2, "added": 1, "already_saved": 0})
        archive = next(star for star in send("GET")["stars"] if star["machine_id"] == starred_mock.BACKUP_MACHINE)
        self.assertEqual(archive["text"], message["text"])
        self.assertEqual(archive["starred_at"], timestamp)
        self.assertEqual(archive["session_id"], "backup." + message["key"])
        self.assertEqual(archive["item_id"], "line0#0")
        self.assertNotIn("source_fingerprint", archive)
        self.assertNotIn("source_manager", archive)
        retry = send("POST", "/import", dict(bundle, revision=imported["revision"]))
        self.assertEqual(retry, {"revision": 2, "added": 0, "already_saved": 1})
        other = send("POST", "/import/preview?manager=m-work", bundle)
        self.assertEqual(other, first)
        self.assertEqual(events, [{"type": "assistant_starred"}] * 2)

    def test_starred_backup_discovery_preserves_the_old_snapshot_routes(self):
        import starred_mock

        r, d = registry({"MANAGERS": {"default": "assistant"}, "other_manager": lambda _h: None})
        with patch.dict(os.environ, {"MOCK_STARRED": "1", "MOCK_STARRED_BACKUP": "old"}):
            starred_mock.register(d)
        for method, path, want in (
            ("GET", starred_mock.BASE, 200), ("GET", starred_mock.BACKUP, 405),
            ("POST", starred_mock.BACKUP, 405), ("POST", starred_mock.BACKUP + "/preview", 404),
        ):
            h = FakeHandler(path)
            self.assertTrue(r.http(h, method, path))
            self.assertEqual(h.sent[0][0], want)
            self.assertEqual(h.response_headers.get("Allow"), "DELETE" if want == 405 else None)

    def test_extension_map_mock_serves_page_card_downgrade_and_tool_views(self):
        import extension_map_mock

        seq = [0]

        def fake_it(role, kind, ts_min, **kw):
            seq[0] += 1
            return {"id": f"m{seq[0]}", "seq": seq[0], "ts": ts_min, "role": role, "kind": kind, **kw}

        def fake_tool(name, inp, result, ts_min):
            tid = f"call_{seq[0] + 1}"
            return [fake_it("assistant", "tool_use", ts_min, tool_use_id=tid, tool_name=name, tool_input=inp),
                    fake_it("user", "tool_result", ts_min, tool_use_id=tid, text=result)]

        def fake_fold(items):
            run = items[1:-1]
            return [items[0], {"id": "fold:m2", "seq": run[0]["seq"], "role": "assistant", "kind": "fold",
                                "fold": {"items": len(run), "through_seq": run[-1]["seq"]}}, items[-1]]

        ns = {
            "PLUGINS_MODE": "1",
            "EXTENSION_UI_EXTRA": [],
            "ITEMS": {},
            "TASK_FEED": {},
            "ASSISTANT": [],
            "it": fake_it,
            "tool": fake_tool,
            "assistant_sess": lambda sid, title, minutes, last: {"session_id": sid, "title": title, "last_text": last},
            "fold_work": fake_fold,
        }
        r, d = registry(ns)
        with patch.dict(os.environ, {"MOCK_EXTENSION_MAP": "1"}):
            importlib.reload(extension_map_mock)
            extension_map_mock.register(d)
        try:
            self.assertIn("tool_views", r.features([]))
            self.assertEqual([u["id"] for u in ns["EXTENSION_UI_EXTRA"]], ["route-planner", "route-planner-card"])
            h = FakeHandler(extension_map_mock.PAGE)
            self.assertTrue(r.http(h, "POST", extension_map_mock.PAGE, json.dumps({"context": {"ui_version": 12}}).encode()))
            view = h.sent.pop()[1]["view"]
            comp = next(item for item in view["sections"][0]["items"] if item["type"] == "map")
            self.assertEqual([rt["id"] for rt in comp["routes"]], ["r-fastest", "r-alt"])

            hc = FakeHandler(extension_map_mock.PAGE + "-card")
            self.assertTrue(r.http(hc, "POST", extension_map_mock.PAGE + "-card", json.dumps({"context": {"ui_version": 12}}).encode()))
            card_item = hc.sent.pop()[1]["view"]["sections"][0]["items"][0]
            self.assertEqual(card_item["aspect"], "wide")

            hp = FakeHandler(extension_map_mock.PAGE)
            self.assertTrue(r.http(hp, "POST", extension_map_mock.PAGE,
                                   json.dumps({"action": "open-stop", "values": {"id": "waypoint"}}).encode()))
            self.assertEqual(hp.sent.pop()[1]["view"]["title"], "Coffee Stop")

            for payload in ({"context": {"ui_version": 11}}, {"context": {"ui_version": 12, "unsupported": ["map"]}}):
                hd = FakeHandler(extension_map_mock.PAGE)
                self.assertTrue(r.http(hd, "POST", extension_map_mock.PAGE, json.dumps(payload).encode()))
                items = hd.sent.pop()[1]["view"]["sections"][0]["items"]
                self.assertFalse(any(i.get("type") == "map" for i in items))

            folded = ns["fold_work"](ns["ITEMS"][extension_map_mock.ASK_MAP])
            fold_row = next(row for row in folded if row.get("kind") == "fold")
            self.assertEqual(fold_row["fold"]["tool_views"][0]["tool"], "mcp__amap__maps_direction_driving")
        finally:
            importlib.reload(extension_map_mock)

    def test_route_matches_method_and_whole_path(self):
        r, d = registry()
        seen = []
        d.route("POST", r"/api/v1/things/(?P<id>[^/]+)", lambda h, req: seen.append((req.match["id"], req.json(), req.query)))
        self.assertFalse(r.http(FakeHandler("/api/v1/things/a"), "GET", "/api/v1/things/a"))
        self.assertFalse(r.http(FakeHandler("/api/v1/things/a/b"), "POST", "/api/v1/things/a/b"))
        self.assertTrue(r.http(FakeHandler("/api/v1/things/a?x=1"), "POST", "/api/v1/things/a", b'{"n": 2}'))
        self.assertEqual(seen, [("a", {"n": 2}, {"x": ["1"]})])

    def test_several_methods_and_unknown_one(self):
        r, d = registry()
        d.route(["PUT", "DELETE"], "/x", lambda h, req: h.send_json({"method": req.method}))
        for method in ("PUT", "DELETE"):
            h = FakeHandler("/x")
            self.assertTrue(r.http(h, method, "/x"))
            self.assertEqual(h.sent, [(200, {"method": method})])
        self.assertFalse(r.http(FakeHandler("/x"), "PATCH", "/x"))
        with self.assertRaises(ValueError):
            d.route("HEAD", "/x", lambda h, req: None)

    def test_false_hands_the_request_on(self):
        r, d = registry()
        order = []
        d.route("GET", "/x", lambda h, req: order.append("first") or False)
        d.route("GET", "/x", lambda h, req: order.append("second"))
        d.route("GET", "/x", lambda h, req: order.append("third"))
        self.assertTrue(r.http(FakeHandler("/x"), "GET", "/x"))
        self.assertEqual(order, ["first", "second"])
        r2, d2 = registry()
        d2.route("GET", "/x", lambda h, req: False)
        self.assertFalse(r2.http(FakeHandler("/x"), "GET", "/x"))  # to mock_hub.py's own routes
        self.assertEqual(mock_domains.Request("GET", "/x", {}, None, b"").json(), {})

    def test_a_route_outside_mock_gets_the_network_faults_first(self):
        # As mock_hub.py's own /api routes: /mock/down closes the connection unanswered, /mock/delay waits (#11250).
        r, d = registry()
        seen = []
        d.route("GET", "/api/v1/x", lambda h, req: seen.append("x"))
        d.route(["GET", "POST"], "/api/v1/pass", lambda h, req: seen.append("pass") or False)
        d.route("GET", "/api/v1/pass", lambda h, req: seen.append("pass again") or False)
        d.route("GET", "/mock/x", lambda h, req: seen.append("mock"))
        h = FakeHandler("/api/v1/x")
        self.assertTrue(r.http(h, "GET", "/api/v1/x"))
        self.assertEqual((seen, h.network_checks), (["x"], 1))
        h = FakeHandler("/api/v1/x", down=True)
        self.assertTrue(r.http(h, "GET", "/api/v1/x"))  # answered by closing: nothing else may answer it
        self.assertEqual((seen, h.network_checks), (["x"], 1))
        h = FakeHandler("/api/v1/pass")
        self.assertFalse(r.http(h, "GET", "/api/v1/pass"))  # handed on to mock_hub.py, checked once on the way
        self.assertEqual((seen[1:], h.network_checks), (["pass", "pass again"], 1))
        for path in ("/mock/x", "/api/v1/none"):  # a test's control, and a path no domain has
            with self.subTest(path=path):
                h = FakeHandler(path, down=True)
                r.http(h, "GET", path)
                self.assertEqual(h.network_checks, 0)
        self.assertEqual(seen[-1], "mock")

    def test_a_close_handler_that_raises_does_not_stop_the_others(self):
        r = mock_domains.Registry({})
        closed = []

        def broken(ws):
            raise RuntimeError("broken on purpose")

        mock_domains.Domain(r, "first").on_ws_close(broken)
        mock_domains.Domain(r, "second").on_ws_close(lambda ws: closed.append(ws.connection))
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            r.ws_closed(r.socket("conn", lambda obj: None, {"role": "full"}, []))
        self.assertEqual(closed, ["conn"])
        self.assertIn("mock: domain first: its WebSocket close handler failed", err.getvalue())
        self.assertIn("RuntimeError: broken on purpose", err.getvalue())

    def test_websocket_messages_and_close(self):
        r, d = registry()
        got, closed = [], []
        d.ws("a", lambda ws, msg: False)
        d.ws("a", lambda ws, msg: got.append((ws.client["role"], msg)))
        d.on_ws_close(lambda ws: closed.append(ws.connection))
        ws = r.socket("conn", lambda obj: None, {"role": "full"}, ["managers"])
        self.assertTrue(r.ws_message(ws, {"type": "a", "n": 1}))
        self.assertFalse(r.ws_message(ws, {"type": "b"}))
        self.assertFalse(r.ws_message(ws, {"type": ["a"]}))  # not a type: the hub's own handlers decide
        self.assertFalse(r.ws_message(ws, {}))
        r.ws_closed(ws)
        self.assertEqual(got, [("full", {"type": "a", "n": 1})])
        self.assertEqual(closed, ["conn"])

    def test_features_add_once_drop_and_ask_each_time(self):
        r, d = registry()
        on = {"old": False}
        d.feature("x", "base")
        d.drop_feature("gone", when=lambda: on["old"])
        d.feature("late", when=lambda: on["old"])
        self.assertEqual(r.features(["base", "gone"]), ["base", "gone", "x"])
        on["old"] = True
        self.assertEqual(r.features(["base", "gone"]), ["base", "x", "late"])

    def test_help_and_reset_in_order(self):
        r, d = registry()
        calls = []
        d.help("MOCK_A=1  a", "MOCK_B=1  b")
        d.on_reset(lambda: calls.append(1))
        d.on_reset(lambda: calls.append(2))
        r.reset()
        self.assertEqual(r.help_lines, ["MOCK_A=1  a", "MOCK_B=1  b"])
        self.assertEqual(calls, [1, 2])

    def test_hub_reads_globals_when_used(self):
        namespace = {"SESSIONS": [1]}
        hub = mock_domains.Registry(namespace).hub
        namespace["SESSIONS"] = [2]  # rebound after the domain was given the hub
        self.assertEqual(hub.SESSIONS, [2])
        hub.EXTRA = "x"
        self.assertEqual(namespace["EXTRA"], "x")
        with self.assertRaises(AttributeError):
            hub.MISSING

    def test_load_in_order_and_every_listed_domain_registers(self):
        with tempfile.TemporaryDirectory() as tmp:
            for name, line in (("domain_one", "d.help('one')"), ("domain_two", "d.help('two')"), ("domain_bad", "x = 1")):
                body = "def register(d):\n    " + line + "\n" if name != "domain_bad" else line + "\n"
                Path(tmp, name + ".py").write_text(body)
            sys.path.insert(0, tmp)
            try:
                r = mock_domains.load({}, ["domain_two", "domain_one"])
                self.assertEqual((r.loaded, r.help_lines), (["domain_two", "domain_one"], ["two", "one"]))
                with self.assertRaises(TypeError):
                    mock_domains.load({}, ["domain_bad"])
                # A second load runs the module again, as a second run of mock_hub.py makes its globals anew (runpy).
                Path(tmp, "domain_state.py").write_text("SEEN = []\n\ndef register(d):\n    SEEN.append(d.name)\n")
                # Earlier imports may have cached this directory before the new module existed.
                importlib.invalidate_caches()
                mock_domains.load({}, ["domain_state"])
                mock_domains.load({}, ["domain_state"])
                self.assertEqual(sys.modules["domain_state"].SEEN, ["domain_state"])
            finally:
                sys.path.remove(tmp)
        # Every listed domain is a module with register(d). A domain may read the hub's fixtures as it registers, so it
        # registers on mock_hub.py's own globals in EndToEndTests (the mock would not start otherwise).
        for name in mock_domains.DOMAINS:
            self.assertTrue(callable(getattr(importlib.import_module(name), "register", None)), name)
        self.assertEqual(len(set(mock_domains.DOMAINS)), len(mock_domains.DOMAINS))


# What each domain module took out of mock_hub.py, by name, with the issue of the move. A branch written before a move
# that edited the old block can resolve its modify/delete or content conflict by keeping it (#11476, the MCP tool
# goldens): mock_hub.py then has the domain a second time, the module's routes answer first, the old copy's state goes
# its own way, and nothing fails. A domain that moves out of mock_hub.py lists its names here.
MOVED = {
    "health_intake_mock": ("#13176", ("HEALTH_INTAKE_ENABLED", "HEALTH_INTAKE_LOCK", "HEALTH_INTAKE_ENTRIES",
                                      "health_intake_seed", "health_intake_snapshot", "health_intake_route")),
    "desktops_mock": ("#11053", ("DESKTOPS", "DESKTOP_LOG", "DESKTOP_LOCK", "desktop_registration", "desktops_state",
                                 "_features_without_desktops")),
    "github_mock": ("#11543", ("GITHUB_DEVICE", "GH_FULL", "GH_MISSING", "GH_NARROW", "GH_MACHINES", "GITHUB_AUTH",
                               "github_auth_view", "set_github_auth", "GH_AVATAR_COLORS", "GH_SENT", "gh_thread_page",
                               "github_thread_area", "github_area")),
    "kb_selection_mock": ("#11543", ("KB_SELECTION_MESSAGES", "KB_SELECTION_DOCS", "KB_SELECTION", "kb_selection_defaults",
                                     "kb_selection_problem", "kb_selection_route")),
    "mail_mock": ("#11543", ("MAIL_ACCOUNTS", "MAIL_LOG", "MAIL_FULL", "MS_CONFIG", "mail_area")),
    "hub_update_mock": ("#11649", ("HUB_UPDATE_MODE", "HUB_UPDATE", "hub_update_state", "HELD_REASON", "hub_update_history",
                                   "set_hub_update_mode", "HUB_UPDATE_HISTORY")),
    # Not the made-up people's shorthands (_ZHANG, _LI, _ALICE): names that short may come back for something else.
    "imessage_mock": ("#11649", ("_imessage_status", "_im_person", "IMESSAGE_CHATS", "IMESSAGE_MESSAGES")),
    "saved_prompts_mock": ("#11649", ("SAVED_PROMPTS", "saved_prompt_name_ok", "push_saved_prompts", "save_saved_prompt",
                                      "saved_prompts_route")),
    "page_counts_mock": ("#12929", ("PAGE_COUNTS", "_paged_sessions_alone", "_features_without_page_counts")),
    "dock_mock": ("#12831", ("DOCK_INTERACTIONS",)),
    "side_unread_mock": ("#13496", ("MOCK_SIDE_UNREAD",)),
    "bg_stalled_mock": ("#13744", ()),
    "meeting_finishing_mock": ("#13537", ("MEETING_FINISHING",)),
    "extension_scene_mock": ("#12544", ("SCENE_MODE", "_HOME_DRAWS", "_home_scene_component", "_home_scene_downgrade",
                                        "_home_scene_view", "mock_home_scene")),
    "todo_wait_reasons_mock": ("#16168", ("_wr_work_sid", "_wr_todo")),
    "command_input_mock": ("#14527", ("COMMAND_INPUT", "COMMAND_INPUT_PROMPT", "COMMAND_INPUT_CARD", "MOCK_COMMAND_INPUT")),
}


def moved_back(source):
    """[(line, what, module, issue)]: each moved name mock_hub.py's source defines or uses (its first line), and each of
    its strings that starts like a moved domain's help line ("MOCK_X=…  what it does")."""
    owner = {name: (module, issue) for module, (issue, names) in MOVED.items() for name in names}
    switches = {}
    for module, (issue, _) in MOVED.items():
        lines = getattr(importlib.import_module(module), "HELP", ())
        for line in [lines] if isinstance(lines, str) else lines:
            switches[line.split("=", 1)[0] + "="] = (module, issue)
    first, out = {}, []
    for node in ast.walk(ast.parse(source)):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            names = [node.name]
        elif isinstance(node, ast.Name):
            names = [node.id]
        elif isinstance(node, (ast.Global, ast.Nonlocal)):
            names = node.names
        elif isinstance(node, (ast.Import, ast.ImportFrom)):
            names = [alias.asname or alias.name.split(".")[0] for alias in node.names]
        elif isinstance(node, ast.Constant) and isinstance(node.value, str):
            out += [(node.lineno, "the %s help line" % switch[:-1]) + switches[switch] for switch in switches
                    if node.value.startswith(switch) and "  " in node.value]
            continue
        else:
            continue
        for name in names:
            if name in owner and (name not in first or node.lineno < first[name]):
                first[name] = node.lineno
    out += [(line, name) + owner[name] for name, line in first.items()]
    return sorted(out)


def moved_back_report(found):
    return "\n".join(
        "mock_hub.py:%d has %s again, which moved to scripts/mock/%s.py (%s): a branch from before the move kept the "
        "old block when it resolved its conflict. Delete the old copy from mock_hub.py and make the change in "
        "scripts/mock/%s.py instead." % (line, what, module, issue, module) for line, what, module, issue in found)


class MovedBlocksTests(unittest.TestCase):
    """A block that moved into a domain module stays out of mock_hub.py (MOVED)."""

    def test_every_moved_domain_is_listed(self):
        self.assertEqual([m for m in MOVED if m not in mock_domains.DOMAINS], [])

    def test_mock_hub_has_none_of_the_moved_blocks(self):
        found = moved_back((HERE / "mock_hub.py").read_text())
        if found:
            self.fail("\n" + moved_back_report(found))

    def test_an_old_block_is_found_with_its_module(self):
        # desktop registration as mock_hub.py had it before #11053, and Knowledge selection's help line and call site.
        old = textwrap.dedent('''
            DESKTOPS = {}  # socket -> device_name
            DESKTOP_LOCK = threading.Lock()


            def desktop_registration(connection, msg):
                with DESKTOP_LOCK:
                    DESKTOPS[connection] = msg


            MOCK_HELP = [
                "MOCK_KB_SELECTION=missing|future|fail  Settings › Knowledge selection: a hub from before it (404)",
            ]


            def do_GET(self, p):
                if kb_selection_route(self, "GET", p, b""): return
        ''')
        found = moved_back(old)
        self.assertEqual([(line, what, module) for line, what, module, _ in found], [
            (2, "DESKTOPS", "desktops_mock"), (3, "DESKTOP_LOCK", "desktops_mock"),
            (6, "desktop_registration", "desktops_mock"),
            (12, "the MOCK_KB_SELECTION help line", "kb_selection_mock"),
            (17, "kb_selection_route", "kb_selection_mock")])
        report = moved_back_report(found)
        self.assertIn("mock_hub.py:6 has desktop_registration again, which moved to scripts/mock/desktops_mock.py (#11053)",
                      report)
        self.assertIn("make the change in scripts/mock/kb_selection_mock.py instead", report)
        self.assertEqual(moved_back("DESKTOP_APPS = []\ndef desktops(): return DESKTOP_APPS\nx = 'MOCK_DESKTOPS'\n"), [])


PROBE = textwrap.dedent('''
    """A domain that records what reaches it, for test_mock_domains.py."""
    SEEN = {"closed": 0, "resets": 0, "machines": 0}
    HUB = None


    def register(d):
        global HUB
        HUB = d.hub
        d.help("MOCK_PROBE=1  the test's probe domain")
        d.feature("probe_feature")
        d.drop_feature("machine_rename")
        d.route("GET", "/mock/probe", lambda h, req: h.send_json(dict(SEEN, sessions=len(d.hub.SESSIONS))))
        d.route("GET", "/mock/hello", lambda h, req: h.send_json({"probe": "first"}))  # a built-in path
        d.route("GET", "/api/v1/machines", count_machines)
        d.route(["POST", "PUT", "PATCH", "DELETE"], r"/api/v1/probe/(?P<id>[^/]+)",
                lambda h, req: h.send_json({"method": req.method, "id": req.match["id"], "body": req.json()}, 201))
        d.ws("probe", lambda ws, msg: ws.send({"type": "probe_ack", "n": msg.get("n"), "role": ws.client.get("role")}))
        d.ws("ping", lambda ws, msg: False)  # seen, then the hub's own pong
        d.on_ws_close(broken)  # logged; the next one still runs
        d.on_ws_close(closed)
        d.on_reset(reset)


    def count_machines(h, req):
        SEEN["machines"] += 1
        return False  # mock_hub.py answers it


    def broken(ws):
        raise RuntimeError("the probe's broken close handler")


    def closed(ws):
        SEEN["closed"] += 1


    def reset():
        SEEN["resets"] += 1
        HUB.LN["on"] = True  # a fixture the built-in reset sets back: the hook runs after it, so this stays
''')

LAUNCH = textwrap.dedent('''
    import runpy, sys
    mock_dir, probe_dir = sys.argv[1], sys.argv[2]
    sys.path[:0] = [probe_dir, mock_dir]
    import mock_domains
    mock_domains.DOMAINS = mock_domains.DOMAINS + ["probe_domain"]
    sys.argv = [mock_dir + "/mock_hub.py"] + sys.argv[3:]
    runpy.run_path(mock_dir + "/mock_hub.py", run_name="__main__")
''')


def free_port():
    with socket.socket() as sock:
        sock.bind((LOOPBACK, 0))
        return sock.getsockname()[1]


def listening(port):
    try:
        with socket.create_connection((LOOPBACK, port), timeout=0.5):
            return True
    except OSError:
        return False


class EndToEndTests(unittest.TestCase):
    """mock_hub.py with the probe domain appended to DOMAINS."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        Path(cls.tmp.name, "probe_domain.py").write_text(PROBE)
        cls.launch = Path(cls.tmp.name, "launch.py")
        cls.launch.write_text(LAUNCH)
        cls.port = free_port()
        cls.base = "http://%s:%d" % (LOOPBACK, cls.port)
        cls.log = tempfile.TemporaryFile()
        env = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        cls.process = subprocess.Popen(cls.command(), env=dict(env, PORT=str(cls.port)), stdout=cls.log, stderr=cls.log)
        deadline = time.monotonic() + 60
        while True:
            if cls.process.poll() is not None:
                cls.log.seek(0)
                raise RuntimeError("mock hub exited: " + cls.log.read().decode(errors="replace")[-2000:])
            try:
                cls.get("/api/v1/health")
                break
            except (URLError, OSError):
                if time.monotonic() > deadline:
                    cls.tearDownClass()
                    raise RuntimeError("mock hub did not answer in 60 s")
                time.sleep(0.1)

    @classmethod
    def command(cls, *args):
        return [sys.executable, "-I", str(cls.launch), str(HERE), cls.tmp.name, *args]

    @classmethod
    def tearDownClass(cls):
        cls.process.terminate()
        try:
            cls.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.process.kill()
            cls.process.wait(timeout=5)
        cls.log.close()
        cls.tmp.cleanup()
        deadline = time.monotonic() + 5
        while listening(cls.port) and time.monotonic() < deadline:
            time.sleep(0.1)

    @classmethod
    def get(cls, path):
        with OPENER.open(cls.base + path, timeout=10) as response:
            return json.load(response)

    def call(self, method, path, body):
        request = HTTPRequest(self.base + path, method=method, data=json.dumps(body).encode(),
                              headers={"Content-Type": "application/json"})
        try:
            with OPENER.open(request, timeout=10) as response:
                return response.status, json.load(response)
        except HTTPError as e:
            with e:
                return e.code, json.loads(e.read() or b"null")

    def test_routes_run_first_and_hand_on(self):
        self.assertEqual(self.get("/mock/hello"), {"probe": "first"})
        before = self.get("/mock/probe")["machines"]
        self.assertTrue(self.get("/api/v1/machines")["machines"])  # still mock_hub.py's answer
        probe = self.get("/mock/probe")
        self.assertEqual(probe["machines"], before + 1)
        self.assertGreater(probe["sessions"], 0)  # the hub's fixtures, through d.hub
        for method in ("POST", "PUT", "PATCH", "DELETE"):
            self.assertEqual(self.call(method, "/api/v1/probe/p1", {"n": 1}),
                             (201, {"method": method, "id": "p1", "body": {"n": 1}}))

    def test_features(self):
        features = self.get("/api/v1/health")["features"]
        self.assertIn("probe_feature", features)
        self.assertNotIn("machine_rename", features)
        self.assertIn("client_hello", features)

    def test_reset_hook(self):
        before = self.get("/mock/probe")["resets"]
        try:
            self.assertEqual(self.get("/mock/reset"), {"ok": True})
            self.assertEqual(self.get("/mock/probe")["resets"], before + 1)
            # The built-in reset turns low-distraction mode off; the hook runs after it and turns it back on (#11250).
            self.assertTrue(self.get("/mock/ln")["on"])
        finally:
            self.get("/mock/ln?on=0")

    def timed(self, *request):
        start = time.monotonic()
        answer = self.call(*request) if len(request) == 3 else self.get(*request)
        return time.monotonic() - start, answer

    def test_simulated_faults_reach_the_domain_routes(self):
        # /mock/delay and /mock/down apply to a domain's /api route as to the built-in ones, once a request even when
        # the domain hands it on to mock_hub.py; its /mock/ routes, like the built-in ones, never see them (#11250).
        delay = 1.5
        try:
            self.get("/mock/delay?s=%s" % delay)
            took, answer = self.timed("POST", "/api/v1/probe/p1", {"n": 1})
            self.assertEqual(answer[0], 201)
            self.assertGreaterEqual(took, delay)
            took, machines = self.timed("/api/v1/machines")  # the probe hands it on to the built-in route
            self.assertTrue(machines["machines"])
            self.assertGreaterEqual(took, delay)
            self.assertLess(took, 2 * delay - 0.3)  # waited once, not again in the built-in route
            took, _ = self.timed("/mock/probe")
            self.assertLess(took, delay)
            self.get("/mock/delay?s=0")
            self.get("/mock/down?s=30")
            with self.assertRaises((URLError, OSError, http.client.HTTPException)):
                self.call("POST", "/api/v1/probe/p1", {"n": 1})
            self.assertIn("resets", self.get("/mock/probe"))
        finally:
            self.get("/mock/delay?s=0")
            self.get("/mock/down?s=0")
        self.assertEqual(self.call("POST", "/api/v1/probe/p1", {"n": 1})[0], 201)

    def test_websocket_messages_and_close(self):
        before = self.get("/mock/probe")["closed"]
        sock = socket.create_connection((LOOPBACK, self.port), timeout=10)
        stream = sock.makefile("rb")
        try:
            key = base64.b64encode(os.urandom(16)).decode()
            sock.sendall(("GET /api/v1/ws HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                          "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n" % (LOOPBACK, self.port, key)).encode())
            self.assertIn(b" 101 ", stream.readline())
            while stream.readline() not in (b"\r\n", b""):
                pass
            hello = json.loads(frame(stream))
            self.assertIn("probe_feature", hello["features"])
            send(sock, {"type": "probe", "n": 7})
            send(sock, {"type": "ping", "id": "p-1"})
            wanted, deadline = {"probe_ack", "pong"}, time.monotonic() + 10
            got = {}
            while wanted - set(got) and time.monotonic() < deadline:
                obj = json.loads(frame(stream))
                got[obj["type"]] = obj
            self.assertEqual(got["probe_ack"], {"type": "probe_ack", "n": 7, "role": "full"})
            self.assertEqual(got["pong"], {"type": "pong", "id": "p-1"})
        finally:
            stream.close()  # the socket closes only once its file does too
            sock.close()
        deadline = time.monotonic() + 5
        while self.get("/mock/probe")["closed"] == before and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertEqual(self.get("/mock/probe")["closed"], before + 1)

    def test_help_lists_the_domains(self):
        env = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        out = subprocess.run(self.command("--help"), env=env, capture_output=True, text=True, timeout=120).stdout
        self.assertIn("MOCK_PROBE=1  the test's probe domain", out)
        self.assertIn("MOCK_DESKTOPS=1  features list desktops", out)
        self.assertIn("MOCK_KB_SELECTION=missing|future|fail  Settings › Knowledge selection", out)
        self.assertIn("MOCK_SAVED_PROMPTS=1|old  Settings › Saved Prompts", out)

    def test_the_moved_domains_answer(self):
        # Every module in DOMAINS registered on mock_hub.py's own globals (the mock would not have started otherwise),
        # and these routes are the modules' now (#11543, #11649).
        self.assertEqual(self.get("/api/v1/kb/selection")["version"], 1)
        self.assertEqual(sorted(self.get("/mock/mail")), ["accounts_added", "deleted", "flags", "sent", "tested"])
        self.assertEqual([a["id"] for a in self.get("/api/v1/mail/accounts")["accounts"]], ["acc1"])
        # Without MOCK_HUB_UPDATE a hub from before the update page: no state, its history all the same.
        with self.assertRaises(HTTPError) as gone:
            self.get("/api/v1/hub/update")
        with gone.exception:
            self.assertEqual(gone.exception.code, 404)
        self.assertEqual([x["result"] for x in self.get("/api/v1/hub/update/history")["history"]], ["updated", "rolled_back"])
        self.assertEqual(self.get("/mock/hub_update?mode=held"), {"ok": True})
        try:
            self.assertIn("held", self.get("/api/v1/hub/update")["latest"])
            self.assertEqual(self.get("/api/v1/hub/update/history")["history"][0]["result"], "held")
        finally:
            self.get("/mock/hub_update")
        # Messages' reader, from the connector mock_hub.py keeps (IMESSAGE_STATUS).
        self.assertEqual(self.get("/api/v1/imessage")["status"], "ok")
        chats = self.get("/api/v1/imessage/chats")["chats"]
        self.assertEqual(len(chats), 8)
        self.assertEqual(len(self.get("/api/v1/imessage/chats/%s/messages" % chats[0]["id"])["messages"]), 9)
        self.assertEqual(self.call("DELETE", "/api/v1/imessage/data", {})[0], 409)  # "Give to Persona" is on
        # Saved prompts: none without MOCK_SAVED_PROMPTS; a request naming a manager the hub would not reach is
        # mock_hub.py's still (404 no_manager).
        self.assertEqual(self.get("/api/v1/assistant/saved-prompts"), {"prompts": []})
        with self.assertRaises(HTTPError) as unknown:
            self.get("/api/v1/assistant/saved-prompts?manager=m-nope")
        with unknown.exception:
            self.assertEqual((unknown.exception.code, json.load(unknown.exception)["code"]), (404, "no_manager"))


def frame(stream):
    """The next text frame's payload (the hub does not mask)."""
    while True:
        head = stream.read(2)
        if len(head) < 2:
            raise EOFError
        n = head[1] & 0x7F
        if n == 126:
            n = struct.unpack(">H", stream.read(2))[0]
        elif n == 127:
            n = struct.unpack(">Q", stream.read(8))[0]
        data = stream.read(n)
        if head[0] & 0x0F == 1:
            return data


def send(sock, obj):
    data = json.dumps(obj).encode()
    mask = os.urandom(4)
    assert len(data) < 126
    sock.sendall(bytes([0x81, 0x80 | len(data)]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))


if __name__ == "__main__":
    unittest.main()
