"""The server's "Start new Agents as Orca chats" switch (#17650, server/internal/devices/orca_chat_starts.go).

MOCK_ORCA_CHAT_STARTS=1 puts Orca in the server's engine set, so its machine carries the switch (`orca_chat_starts`, "off"),
and answers GET / PUT /api/v1/machines/server/orca-chat-starts as the manager does (the machine list is pushed after a
change). While it is on, New Agent's launchers for the server (GET /api/v1/machines/server/launchers, no other ?host=)
carry `default_opens`: the start that keeps the default opens as "Orca chat · Claude Code". MOCK_ORCA_CHAT_STARTS=on
starts with it on. Use it with MOCK_MODEL_DEFAULTS=1 (New Agent then follows Model settings, as the switch needs) and
MOCK_ORCA_CHAT_LAUNCHERS=1; web/e2e/orca-chat-starts.mjs takes its screenshots with them. Unset: nothing changes.
"""
import os

MODE = os.environ.get("MOCK_ORCA_CHAT_STARTS", "")
MID = "server"
OPENS = {"launcher": "orca-claude", "display_name": "Orca chat · Claude Code",
         "permission_modes": ["default", "acceptEdits", "auto", "bypassPermissions"]}


class _Captured:
    """A request handler stand-in that keeps the answer a built-in route sends, so this module can add to it."""

    def __init__(self, h):
        self._h = h
        self.answer = None

    def __getattr__(self, name):
        return getattr(self._h, name)

    def send_json(self, obj, status=200, *args, **kwargs):
        self.answer = (obj, status)


def register(d):
    d.help("MOCK_ORCA_CHAT_STARTS=1  the server's \"Start new Agents as Orca chats\" switch and New Agent's line (#17650); =on starts it on")
    if not MODE:
        return
    hub = d.hub
    state = {"on": MODE == "on"}
    machine = next(m for m in hub.MACHINES if m["id"] == MID)
    hosts = list(machine.get("engine_hosts") or [machine.get("engine") or "herdr"])
    if "orca" not in hosts:
        hosts.append("orca")
    machine["engine_hosts"] = hosts
    machine.setdefault("engine_default", hosts[0])
    machine["orca_chat_starts"] = "on" if state["on"] else "off"

    def answer(h):
        h.send_json({"on": state["on"], "applies": True})

    def put(h, req):
        try:
            on = req.json().get("on")
        except ValueError:
            on = None
        if not isinstance(on, bool):
            return h.send_json({"error": "on is required.", "code": "bad_request"}, 400)
        state["on"] = on
        machine["orca_chat_starts"] = "on" if on else "off"
        hub.broadcast({"type": "machines", "machines": hub.out_machines()})
        answer(h)

    def launchers(h, req):
        if not state["on"] or req.query.get("purpose", ["agent.new"])[0] != "agent.new" or req.query.get("host", ["orca"])[0] != "orca":
            return False
        captured = _Captured(h)
        if not hub.model_defaults_launchers(captured, req.path, req.query):
            return False  # no Model settings default here (MOCK_MODEL_DEFAULTS unset): the built-in answer, unchanged
        obj, status = captured.answer
        if status == 200 and isinstance(obj, dict):
            obj = dict(obj, default_opens=dict(OPENS))
        h.send_json(obj, status)

    def reset():
        state["on"] = MODE == "on"
        machine["orca_chat_starts"] = "on" if state["on"] else "off"

    d.route("GET", r"/api/v1/machines/server/orca-chat-starts", lambda h, req: answer(h))
    d.route("PUT", r"/api/v1/machines/server/orca-chat-starts", put)
    d.route("GET", r"/api/v1/machines/server/launchers", launchers)
    d.on_reset(reset)
