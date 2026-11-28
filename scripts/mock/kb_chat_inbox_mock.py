"""A chat's Inbox items in the mock hub (#15356, docs/assistant/knowledge.md "A chat's Inbox items"), a domain of
scripts/mock/mock_domains.py: with MOCK_KB_CHAT_INBOX=1 the example project group (oc_team) has two Inbox items, one open
and one done, its knowledge page (GET /api/v1/kb/chats/oc_team) lists them as `inbox` with the rows of its last days
each holds, and each item's own read (GET /api/v1/inbox/{id}) names the chat as `kb_chat`. Made-up data for the apps'
screenshots; scripts/mock/test_kb_chat_inbox_mock.py drives it.
"""
import os

HELP = "MOCK_KB_CHAT_INBOX=1  the example project group's knowledge page lists its two Inbox items, and each names it (#15356)"
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register

CHAT = "oc_team"
OPEN_ID = "lark:fteam:410"
DONE_ID = "lark:fteam:400"


def items():
    """The group's Inbox items as the list has them: a question waiting for the owner, and a done note."""
    lark = {"feed_id": "fteam", "is_group": True}
    return [
        {"id": OPEN_ID, "source": "lark", "kind": "group", "account": "lark", "account_name": "飞书", "title": "天气 App 项目群",
         "where": "天气 App 项目群", "sender": "韩梅梅", "preview": "周五前要一版能装的", "count": 1,
         "started_at": HUB.ago(minutes=50), "updated_at": HUB.ago(minutes=45), "tier": "action", "status": "action",
         "summary": "韩梅梅问周五前能不能出一版能装的", "reason": "在等你回复", "read": False,
         "lark": dict(lark, start_position="410")},
        {"id": DONE_ID, "source": "lark", "kind": "group", "account": "lark", "account_name": "飞书", "title": "天气 App 项目群",
         "where": "天气 App 项目群", "sender": "韩梅梅", "preview": "灰度名单我来拉", "count": 1,
         "started_at": HUB.ago(hours=6), "updated_at": HUB.ago(hours=6), "tier": "fyi", "summary": "灰度名单由韩梅梅来拉",
         "read": True, "done_at": HUB.ago(hours=5), "done_by": "user", "lark": dict(lark, start_position="400")},
    ]


# The rows of the chat's archive each item holds, as the hub matched them ({day, line}, the line as `recent` has it).
HELD = {OPEN_ID: [("2026-10-30", "- 10:15 韩梅梅: 周五前要一版能装的")], DONE_ID: [("2026-10-29", "- 10:05 韩梅梅: 灰度名单我来拉")]}


def recent():
    """The chat's last days as the hub's detail writes them: a heading per day, then its rows."""
    out = []
    for day, lines in sorted(HUB.KB_DAYS.get(CHAT, {}).items()):
        out += ["## " + day] + lines
    return "\n".join(out)


def listed():
    """The group's items in the hub's list now, open first, newest first."""
    newest = sorted((x for x in HUB.INBOX if x["id"] in HELD), key=lambda x: x["updated_at"], reverse=True)
    return sorted(newest, key=lambda x: x.get("done_at") is not None)


def entry(x):
    """An item as a chat's page lists it (kb_inbox.go kbChatInboxItem)."""
    out = {k: x[k] for k in ("id", "title", "tier", "status", "summary", "preview", "updated_at", "done_at") if x.get(k)}
    out["messages"] = [{"day": d, "line": l} for d, l in HELD[x["id"]]]
    return out


def chat_page(h, req):
    """GET /api/v1/kb/chats/oc_team, a search's (`q`) left to the hub."""
    if req.query.get("q"):
        return False
    e = next((x for x in HUB.KB.get("chats", []) if x["id"] == CHAT), None)
    if e is None:
        return False
    d = dict({"kind": "chats", "id": CHAT, "path": "/home/me/.persona/data/chats/" + CHAT, "meta": HUB.kb_base_meta("chats", e),
              "summary": e.get("one_line")}, **HUB.KB_DETAIL.get(("chats", CHAT), {}))
    d["recent"] = recent()
    out = {"detail": d}
    inbox = [entry(x) for x in listed()]
    if inbox:
        out["inbox"] = inbox
    return h.send_json(out)


def item_read(h, req):
    """GET /api/v1/inbox/{id} of one of the group's items: as the hub's, with `kb_chat`."""
    x = next((x for x in HUB.INBOX if x["id"] == req.match.group(1)), None)
    if x is None or x["id"] not in HELD:
        return False
    return h.send_json(dict(x, context=x["preview"], kb_chat={"id": CHAT, "path": "/kb/chats/" + CHAT}))


def add_items():
    HUB.INBOX[:] = [x for x in HUB.INBOX if x["id"] not in HELD] + items()


def register(d):
    global HUB
    HUB = d.hub
    d.help(HELP)
    if os.environ.get("MOCK_KB_CHAT_INBOX") != "1":
        return
    add_items()
    d.route("GET", "/api/v1/kb/chats/" + CHAT, chat_page)
    d.route("GET", r"/api/v1/inbox/(lark:fteam:\d+)", item_read)
    d.on_reset(add_items)
