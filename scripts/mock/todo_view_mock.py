"""The To-dos view as a hub with the redesign's stage 1 serves it (#10226, #10465; proposal
docs/design/proposals/todos-redesign.md 3.3, 3.4, 3.7 and 6), for mock_hub.py's MOCK_TODO_VIEW: the design prototype's
to-dos (T01 to T14 of docs/design/prototypes/todos/data/todos.js) as wire rows, each with its `view`, so an app's
screenshot lines up with the V1 frame it draws.

MOCK_TODO_VIEW=1      features todo_view and todo_places. GET /api/v1/todos: every row with `view`, the list's
                      `counts`, ETag W/"t1-…" and 304 for it; GET /api/v1/todos/{id} → {"todo": row + view}; GET
                      /api/v1/todos/places → the places and the counts; PUT / DELETE …/someday and PUT …/restored as
                      the hub answers them. Today's answers work on these to-dos as the hub takes them, whatever
                      MOCK_TASKS says: POST …/accept (T01's decision: an option, the plain press or a fixed answer),
                      …/decision-dismiss and …/decision-undo-dismiss (its review round), …/retry (T03's failed start
                      starts again on the MacBook). PATCH, DELETE and …/seen stay mock_hub.py's; once a PATCH is taken,
                      the hub's rules for a tick on a failed start (closed now) and a reopen (out of Someday, no longer
                      failed) are applied. The Widget run's plan is a card (GET /api/v1/interactions); answering it
                      sets the run working. The views follow every write. With the hello's todo_asks (unless
                      MOCK_TODO_VIEW_ASKS=0), POST /api/v1/todos/{id}/asks/{ask}/answer answers the asks the views list
                      as the hub does: T01's decision (an option, a fixed answer, later, decline and its undo with the
                      receipt; 400 for no answer at all) and T03's retry; 409 ask_stale with the row once an ask no longer waits,
                      answer_in_session for a run's ask (answered through its card), 404 ask_not_found for an id of no
                      known form. Each ask says its risk and lock-screen rule (T01's decision and T03's retry safe, the
                      Widget's plan open, with its card's id).
MOCK_TODO_VIEW=old    a hub from before the view: the same to-dos with neither `view` nor `counts` (nor
                      `someday_until` or `restored_at`), 405 for the item and places routes (PATCH and DELETE
                      /api/v1/todos/{id} match their path, as docs/protocol/protocol.md says), 404 for someday and
                      restored.
MOCK_TODO_VIEW=late   as 1, except the hello names todo_view and todo_places only from the second connection on, as a
                      hub whose manager reconnects after it: once an app has connected, the mock closes every socket
                      three times, MOCK_TODO_VIEW_LATE_SECONDS (default 5) apart, as a restarting hub does, so the
                      hellos read: without, with, without, with. /mock/reset starts the sequence again (and closes the
                      sockets, so the next hello lacks the view). The routes answer as under 1 throughout.
MOCK_TODO_MANAGERS=2  the Weather App's to-dos are the second manager's (the first other one the roster lists, which
                      has the MacBook where their runs are): every row is stamped with its manager, the counts are
                      added up and the places merged as the hub merges them (server/internal/hub/todo_view.go).
Send receipts         (docs/protocol/protocol.md "Message feedback (#5258)") under every mode the hello carries
                      message_send_state: 1, and under 1 and late also todo_send_state: 1, as the hub writes them. A
                      session's message with a client_msg_id says message_send_state processing to the session's
                      subscribers before it is relayed, then delivered, kept, failed or unknown as it is answered; a
                      message on a to-do's conversation route (served with MOCK_TASKS=1), and an answer on the asks
                      route, with X-Persona-Op-Id says todo_send_state the same way to the full devices that declared it
                      (client_hello or ?features=; not under old). Only the request that claimed the id says anything: the same op id waits up to 10 s for
                      the first answer and gets it back (202 {"state": "pending"} after that), the same client_msg_id
                      waits for the first answer, and an op id that is not a UUID is 400. MOCK_SEND_DELAY_MS holds
                      the to-do's message as long as a session's.
GET /mock/churn?todos=1[&n=20&ms=3000]  n times, ms apart, the newsletter's working line moves on and `todos` is
                      pushed, for measuring a push's cost on the list (/mock/churned counts the rounds).
GET /mock/todo-view   the mode, whether the hello names the view now, the late phase and the managers.
GET /mock/todo-view?seen_hours=25  until /mock/reset, a finished to-do's seen mark counts as that many hours old, so a
                      row seen now reads as seen a day ago: To do with finished_seen (the list's Mark Done).
GET /mock/todo-view?done_hours=t08:50  until /mock/reset, the closed to-do t08 closed that many hours before now (and
                      `todos` is pushed), so a search finds it under an earlier day whatever the hour.

Time: the moment the mock starts plays the prototype's now (Wednesday 18 November 2026, 10:14), and every time is
placed from it as the prototype places it, so a row the prototype shows as 16 minutes old is 16 minutes old when the
mock starts, and a due day keeps its distance in days from the start day (in this machine's zone).

The views: a row whose state comes from runs, asks or parks (T01, T04 to T07, T09, T10, T13, and T02's Widget and
Release notes steps) keeps the prototype's view while the facts that placed it are as they were (open, the same runs
linked in the same states, T01's decision neither answered nor put aside with Don't handle); everything else follows
the hub's rules for what the writes change (spec/fixtures/todo-view/README.md): a failed start, a closed to-do (Just
finished until seen or for 24 hours, then Done), Someday, `seen` against the last activity (and that of the steps on
the row), a to-do with a run linked (Working while its session works, else Starting) or none (To do), and a parent led
by its most urgent open step (T02). T01 answered with a choice reads as the prototype draws it: Working, Persona booking
the choice, its step Pick a ryokan done; a fixed answer other than explain_first ends the round, and Don't handle puts
it aside, both leaving a To do.

Where it is not a hub: ranks keep the prototype's order (a row that moved into a section, or is new, comes first in it,
newest first, as the prototype's Data.view re-ranks), not the hub's (server/internal/assistant/todo/projection/list.go:
Waiting by the soonest wake, To do by due day, then priority, then newest), so never hold an app's order against this
mock as the hub's. /mock/churn pushes `todos` on a line-only change, which the hub never does (a worst case), and
nothing is pushed when a row's next_change_at passes. The Widget run's ask says `plan`, its card's kind, where a stage-1
hub says `question`. MOCK_TODO_VIEW answers as one manager whatever the roster lists; the mock's default roster lists
several, so an app takes its several-managers path (counts from its rows), and MOCK_MANAGERS=1 gives the one-manager
path that reads the hub's counts and places as sent. Standard library only; every name and number is made up, as the
prototype's are.
"""
import copy
import hashlib
import json
import os
import re
import socket
import sys
import threading
import time
from datetime import date, datetime, timedelta, timezone
from urllib.parse import parse_qs, unquote, urlparse

MODE = os.environ.get("MOCK_TODO_VIEW", "")
MODES = ("1", "old", "late")
TWO_MANAGERS = os.environ.get("MOCK_TODO_MANAGERS") == "2"
LATE_SECONDS = float(os.environ.get("MOCK_TODO_VIEW_LATE_SECONDS") or 5)

PROTO_NOW = datetime(2026, 11, 18, 10, 14)  # todos.js NOW
NO_DATE = datetime(9999, 12, 31, tzinfo=timezone.utc)  # db.SomedayNoDate: Someday with no date
SECTIONS = ("needs_you", "can_wait", "working", "waiting", "todo", "just_finished", "someday", "done")
VIEW_KEYS = ("phase", "section", "reason", "line", "since", "next_change_at", "rank", "seen", "inactive", "can_wait",
             "asks", "steps")  # TodoSummary's order
WEATHER = "/Users/me/code/weather-app"
PICTURES = "/Users/me/Pictures"
NEWSLETTER = ["writing part 2 of 3", "writing part 3 of 3", "reading it through once more"]  # /mock/churn?todos=1
RESPONSES = ("decide_yourself", "not_what_i_asked", "explain_first")  # the accept route's fixed answers (decision.go)
PLAN_CARD = "ix_todo_widget_plan"

HELP = ["MOCK_TODO_VIEW=1|old|late  the To-dos view from the design prototype's to-dos (#10465, "
        "scripts/mock/todo_view_mock.py): rows with view, counts and an ETag, the item and places routes, Someday and "
        "restore, today's answers to its asks and the asks route (feature todo_asks), and the send receipts "
        "(message_send_state, and except under old todo_send_state under X-Persona-Op-Id, with the hello's markers); "
        "old: a hub from before it (405); late: the hello names it only after a reconnect (MOCK_TODO_VIEW_LATE_SECONDS "
        "apart, default 5; /mock/reset starts it again)",
        "MOCK_TODO_VIEW_ASKS=0  with MOCK_TODO_VIEW=1: the hello leaves out todo_asks and the asks route is not there "
        "(404), a hub whose managers answer only through today's accept and retry routes",
        "MOCK_TODO_MANAGERS=2  with MOCK_TODO_VIEW: the Weather App's to-dos are a second manager's (stamped rows, "
        "counts added up, places merged)"]

_hub = {}  # mock_hub.py's globals (install)
_state = {"phase": 0, "late_run": 0, "churn": 0, "bases": {}, "homes": {}, "order": [], "owners": {}, "legacy": {},
          "settles": {}, "answers": {}, "sessions": {}, "card": None, "seen_hours": 0.0, "ops": {}, "flying": {},
          "taken": set()}


def stamp(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse(s):
    try:
        return datetime.fromisoformat(str(s).replace("Z", "+00:00")) if s else None
    except ValueError:
        return None


def norm(s):
    """A time as the view writes it (RFC 3339, whole seconds, UTC); None for none."""
    t = parse(s)
    return stamp(t) if t else None


def line(key, **args):
    """A view's line: a key and its string arguments, an empty one left out."""
    args = {k: v for k, v in args.items() if v}
    return dict({"key": key}, **({"args": args} if args else {}))


def sid(n):
    """The session of the prototype's run n: 1 Widget, 2 Release notes, 3 Screenshots, 4 Login fix; 5 a retry."""
    return "e1a7c3d5-0000-4000-8000-%012d" % (10465000 + n)


RETRIED = sid(5)


class Clock:
    """The prototype's times from the mock's start: at(-2, 9, 12) is Monday 9:12, the start playing Wednesday 10:14."""

    def __init__(self, start):
        self.start = start

    def at(self, day, hour, minute):
        return stamp(self.start + timedelta(days=day, hours=hour - PROTO_NOW.hour, minutes=minute - PROTO_NOW.minute))

    def day(self, iso):
        n = (date.fromisoformat(iso) - PROTO_NOW.date()).days
        return (self.start.astimezone().date() + timedelta(days=n)).isoformat()


# ---------- the prototype's to-dos as the hub lists them ----------

def prototype(c, origin):
    """(rows, views, legacy asks, settles, sessions, card): the to-dos in Data.todos's order, then their steps; the
    views of the rows the prototype places by runs, asks or parks; T01's decision as today's list carries it (`ask`)
    and the step a choice settles; the runs' sessions; the Widget run's plan card."""
    rows = []

    def todo(tid, title, created, updated, **kw):
        t = {"id": tid, "title": title, "notes": "", "done_at": None, "created_at": created, "updated_at": updated,
             "spaces": [], "agents": [], "quiet": False}
        t.update(kw)
        rows.append(t)
        return t

    def agent(n, title, linked):
        return {"machine_id": "macbook", "session_id": sid(n), "linked_at": linked, "released_at": None, "title": title,
                "cwd": WEATHER, "agent": "claude", "start_error": "", "source": "spawned", "via": ""}

    asked, deadline = c.at(0, 10, 11), c.at(2, 18, 0)
    options = [{"label": "Ryokan Hanami", "leads_to": "¥58k a night, 4 min from Gion, late check-in"},
               {"label": "Kaede", "leads_to": "¥52k a night"}, {"label": "Tsuki", "leads_to": "¥59k a night"}]
    # The decision's review round (the hub's decision_review), so Don't handle has a round to put aside.
    review = {"supervisor": origin, "done_when": [], "state": "needs_user", "round": 0, "updated_at": asked,
              "reviews": [{"at": asked, "verdict": "needs_user", "round": 0, "checks": [], "note": "Which ryokan?"}]}
    todo("t01", "Plan the Kyoto trip", c.at(-2, 9, 12), c.at(0, 10, 11), origin=origin, project="kyoto",
         due_date=c.day("2027-04-03"), notes="Two of us. Ryokan near Gion, under ¥60k a night.", supervision=review)
    todo("t02", "Release Weather App 1.3", c.at(-1, 9, 40), c.at(0, 10, 12), origin=origin, project="weather",
         due_date=c.day("2026-11-20"))
    todo("t03", "Sort last year’s photos", c.at(0, 9, 50), c.at(0, 9, 58), origin=origin, project="home",
         done_at=c.at(0, 9, 58), start_error="Desk Mac is offline",
         notes="Group by trip, delete blurry duplicates, keep the originals.")
    todo("t13", "Renew the car insurance", c.at(-1, 16, 0), c.at(0, 9, 14), origin=origin, project="home")
    todo("t05", "Draft the school newsletter", c.at(-1, 20, 0), c.at(0, 10, 13), origin=origin, project="home",
         due_date=c.day("2026-11-19"))
    todo("t06", "Get quotes for the garden gate", c.at(0, 9, 30), c.at(0, 10, 11), origin=origin, project="home")
    todo("t07", "Book the dentist", c.at(0, 9, 40), c.at(0, 10, 6), origin=origin, project="home")
    todo("t08", "Fix widget refresh crash", c.at(-1, 11, 0), c.at(0, 8, 55), origin=origin, project="weather",
         done_at=c.at(0, 8, 55), done_by="auto", done_reason="Merged and live",
         delivery={"state": "live", "at": c.at(0, 8, 55)})
    todo("t04", "Fix login crash for SSO users", c.at(-2, 14, 5), c.at(0, 9, 30), origin=origin, project="weather",
         priority="high", agents=[agent(4, "Login fix", c.at(-2, 14, 5))], stage="review",
         pr_url="https://github.com/example/weather-app/pull/812")
    todo("t09", "Update the privacy policy page", c.at(-2, 16, 0), c.at(-1, 15, 0), origin=origin, project="weather")
    todo("t10", "Keep an eye on the app size after 1.3", c.at(-1, 17, 0), c.at(-1, 17, 5), origin=origin,
         project="weather", kind="concern")
    todo("t11", "Renew passport", c.at(0, 9, 5), c.at(0, 9, 5), due_date=c.day("2026-11-20"),
         notes="Old one expires in May. Photo booth at the post office.")
    todo("t12", "Buy groceries", c.at(-2, 18, 0), c.at(-2, 18, 30), project="home")
    todo("t14", "Learn to bake sourdough", c.at(-3, 20, 0), c.at(-3, 20, 0), someday_until=stamp(NO_DATE))
    # Steps: the to-dos whose part_of names their parent (proposal 3.4 "Steps roll up"), only where the prototype's list
    # counts them on line 3 (T01, T02, T12); T04's and T05's steps on their pages are their runs' plans, not to-dos.
    for n, (title, done_at, by) in enumerate([("Choose dates", c.at(-2, 9, 12), ""),
                                              ("Book flights", c.at(-2, 18, 40), "auto"), ("Pick a ryokan", None, ""),
                                              ("Get JR passes", None, ""), ("Plan each day", None, "")], 1):
        todo("t01-s%d" % n, title, c.at(-2, 9, 15), done_at or c.at(-2, 9, 15), part_of="t01", done_at=done_at,
             **({"done_by": by} if by else {}))
    todo("t02-widget", "Widget", c.at(-1, 9, 41), c.at(0, 10, 12), part_of="t02",
         agents=[agent(1, "Widget", c.at(0, 10, 6))])
    todo("t02-notes", "Release notes", c.at(-1, 9, 41), c.at(0, 10, 14), part_of="t02",
         agents=[agent(2, "Release notes", c.at(0, 10, 11))])
    todo("t02-shots", "Screenshots", c.at(-1, 9, 41), c.at(-1, 9, 52), part_of="t02", done_at=c.at(-1, 9, 52),
         done_by="auto", agents=[agent(3, "Screenshots", c.at(-1, 9, 40))])
    for n, title in enumerate(["Milk", "Eggs", "Miso", "Tofu"]):
        bought = c.at(-2, 18, 30) if n == 0 else None
        todo("t12-" + title.lower(), title, c.at(-2, 18, 0), bought or c.at(-2, 18, 0), part_of="t12", done_at=bought)

    widget_since = c.at(0, 10, 12)
    widget_ask = {"id": "run:macbook/%s@%s" % (sid(1), widget_since), "kind": "plan", "home": "run",
                  "run": {"machine_id": "macbook", "session_id": sid(1), "title": "Widget"},
                  "text": "Plan awaiting approval", "risk": "open", "lockscreen": "open_only", "interaction": PLAN_CARD}
    views = {
        "t01": {"phase": "needs_you", "section": "needs_you", "reason": "owner_decides",
                "line": line("decision", text="Which ryokan?", recommend="Ryokan Hanami"), "since": asked,
                "asks": [{"id": "todo:decision@" + asked, "kind": "decision", "home": "todo", "round": asked,
                          "text": "Which ryokan?", "recommend": "Ryokan Hanami", "options": options,
                          "default_at": deadline, "risk": "safe", "lockscreen": "answer"}]},
        "t13": {"phase": "needs_you", "section": "can_wait", "reason": "awaits_reply", "can_wait": True,
                "line": line("awaits_reply", run="Persona", text="Do you want roadside cover? It adds ¥4,800 a year."),
                "since": c.at(0, 9, 14)},
        "t05": {"phase": "working", "section": "working", "reason": "persona_working",
                "line": line("persona_working", text=NEWSLETTER[0]), "since": c.at(0, 10, 2)},
        "t06": {"phase": "working", "section": "working", "reason": "persona_working",
                "line": line("persona_working", text="comparing 2 quotes"), "since": c.at(0, 10, 11)},
        "t07": {"phase": "ready", "section": "just_finished", "reason": "finished_unseen", "line": line("finished"),
                "since": c.at(0, 10, 6)},
        "t04": {"phase": "waiting", "section": "waiting", "reason": "pr_open", "line": line("pr_open")},
        "t09": {"phase": "waiting", "section": "waiting", "reason": "someone_else",
                "line": line("parked", reason="A reviewer to merge"), "since": c.at(-1, 15, 0)},
        "t10": {"phase": "waiting", "section": "waiting", "reason": "parked",
                "line": line("parked", reason="Watching the next 3 builds", until=c.at(2, 9, 0)),
                "since": c.at(-1, 17, 5)},
        "t02-widget": {"phase": "needs_you", "section": "needs_you", "reason": "agent_asks",
                       "line": line("agent_asks", run="Widget", text="Plan awaiting approval"), "since": widget_since,
                       "asks": [widget_ask]},
        "t02-notes": {"phase": "working", "section": "working", "reason": "agent_running",
                      "line": line("working", run="Release notes", text="drafting What’s New"),
                      "since": c.at(0, 10, 11)},
    }
    legacy = {"t01": {"outcome": "decide", "verdict": "🤔 Needs your decision: Which ryokan?",
                      "recommend": "Ryokan Hanami", "asked_at": asked, "question": "Which ryokan?", "options": options,
                      "deadline": deadline, "default": "taken", "responses": list(RESPONSES)}}
    settles = {"t01": "t01-s3"}  # a choice of ryokan settles Pick a ryokan (the prototype's Data.view({answered}))
    # The runs' sessions on the MacBook: (id, title, status, state, last activity, state since, last text, cards)
    sessions = [(sid(1), "Widget", "blocked", "needs_you", widget_since, widget_since,
                 "Here is the plan: a timeline provider, small and medium layouts and snapshot tests. About 2 h.", 1),
                (sid(2), "Release notes", "working", "working", c.at(0, 10, 14), c.at(0, 10, 11),
                 "Drafting What’s New for 1.3.", 0),
                (sid(3), "Screenshots", "idle", "idle", c.at(-1, 9, 52), c.at(-1, 9, 52),
                 "Made 6 screenshots for the App Store page.", 0),
                (sid(4), "Login fix", "idle", "idle", c.at(-2, 14, 43), c.at(-2, 14, 43),
                 "Found it: the token refresh races the SSO redirect. The change is ready to review.", 0)]
    card = {"id": PLAN_CARD, "machine_id": "macbook", "session_id": sid(1), "session_title": "Widget", "cwd": WEATHER,
            "kind": "plan", "created_at": widget_since, "expires_at": c.at(1, 10, 12), "tool_name": "ExitPlanMode",
            "tool_use_id": "toolu_todo_widget_plan",
            "plan": "## The widget\n\n1. A timeline provider\n2. Small and medium layouts\n3. Snapshot tests\n\n"
                    "About 2 h."}
    return rows, views, legacy, settles, sessions, card


# ---------- the view of one to-do ----------

def closed(t):
    return bool(t.get("done_at")) and not t.get("start_error")


def live_agents(t):
    return [a for a in t.get("agents") or [] if not a.get("released_at") and not a.get("dismissed_at")]


def session(a):
    """A linked run's session as the mock lists it; None when it lists none."""
    return next((s for s in _hub["SESSIONS"]
                 if s["session_id"] == a["session_id"] and s["machine_id"] == a["machine_id"]), None)


def facts_key(t):
    """What placed a row the prototype places: open or not, a start error, the runs linked and their sessions'
    states."""
    runs = sorted((a["session_id"], (session(a) or {}).get("state", "")) for a in live_agents(t))
    return closed(t), bool(t.get("start_error")), tuple(runs)


def dismissed(t):
    """The owner chose Don't handle on its decision's round: decision_review with a receipt, as sorted_todos lists
    it."""
    return bool((t.get("decision_review") or {}).get("dismissal_id"))


def last_activity(t, now):
    """The last moment the to-do or one of its live runs moved (projection/runs.go lastActivity)."""
    sup = t.get("supervision") or {}
    times = [t.get(k) for k in ("updated_at", "created_at", "reopened_at", "done_at", "restored_at")]
    times += [sup.get("updated_at"), (sup.get("progress") or {}).get("at"), (sup.get("reviews") or [{}])[-1].get("at"),
              (t.get("delivery") or {}).get("at"), (t.get("ask") or {}).get("asked_at")]
    for a in live_agents(t):
        s = session(a) or {}
        times += [s.get("updated_at"), s.get("state_since")]
    out = [x for x in map(parse, times) if x]
    until = parse(t.get("someday_until"))
    if until and until <= now:
        out.append(until)
    return max(out, default=None)


def seen_since(t, at):
    """The owner looked at it at or after at."""
    seen_at = parse(t.get("seen_at"))
    return bool(seen_at) and (at is None or seen_at >= at)


def failed_start(t, done, now):
    """Row 1 for 48 hours from the failure (a retry ask only with an origin: retryTask refuses the others), then a To
    do that goes Inactive."""
    end = done + timedelta(hours=48)
    if now < end:
        v = {"phase": "needs_you", "section": "needs_you", "reason": "failed_start",
             "line": line("failed_start", error=t["start_error"]), "since": stamp(done), "next_change_at": stamp(end)}
        if t.get("origin"):
            v["asks"] = [{"id": "todo:retry@" + t["done_at"], "kind": "retry", "home": "todo", "risk": "safe",
                          "lockscreen": "answer"}]
        return v
    last = max(x for x in (done, parse(t.get("updated_at")), parse(t.get("restored_at"))) if x)
    v = {"phase": "todo", "section": "todo", "reason": "rest"}
    if now >= last + timedelta(hours=48):
        v["inactive"] = stamp(last)
    else:
        v["next_change_at"] = stamp(last + timedelta(hours=48))
    return v


def closed_view(t, done, now):
    """Rows 6 and 7: Just finished until seen or for 24 hours, then Done."""
    sup = t.get("supervision") or {}
    if t.get("cancelled_at") or (t.get("done_by") == "review" and sup.get("state") == "cancelled"):
        reason, said = "canceled", line("canceled", reason=(t.get("cancelled_reason") or "").split("\n")[0])
    elif t.get("kind") == "concern":
        reason, said = "reported", line("reported")
    else:
        by = t.get("done_by") if t.get("done_by") in ("auto", "review") else ""
        reason, said = "completed", line("completed", by=by)
    seen = seen_since(t, done)
    end = done + timedelta(hours=24)
    if seen or now >= end:
        return dict({"phase": "done", "section": "done", "reason": reason, "line": said, "since": stamp(done)},
                    **({"seen": True} if seen else {}))
    return {"phase": "done", "section": "just_finished", "reason": "closed_unseen", "line": said, "since": stamp(done),
            "next_change_at": stamp(end)}


def finished(t, v, now, seen):
    """Row 9: finished_unseen until the owner looks at it, finished_recent for 24 hours after, then To do (row 11)."""
    if not seen:
        return v
    end = parse(t["seen_at"]) - timedelta(hours=_state["seen_hours"]) + timedelta(hours=24)
    if now < end:
        return dict(v, reason="finished_recent", next_change_at=stamp(end))
    return {"phase": "todo", "section": "todo", "reason": "finished_seen",
            "line": line("finished_seen", at=v.get("since")), "since": v.get("since")}


def someday(t, v, now):
    """Row 12: put off, unless a row above it holds the to-do."""
    until = parse(t.get("someday_until"))
    if not until or until <= now or v["phase"] != "todo":
        return v
    dated = until < NO_DATE
    return dict({"phase": "someday", "section": "someday", "reason": "deferred",
                 "line": line("someday", until=stamp(until) if dated else "")},
                **({"next_change_at": stamp(until)} if dated else {}))


def run_view(t):
    """A to-do its live runs place and nothing else: Working, by its newest run (agent_running with what it does while
    its session works, starting until then)."""
    a = max(live_agents(t), key=lambda x: x.get("linked_at") or "")
    s = session(a) or {}
    since = norm(s.get("state_since") or s.get("updated_at") or a.get("linked_at"))
    if s.get("state") == "working":
        said = s.get("activity") or next((x.strip() for x in (s.get("last_text") or "").splitlines() if x.strip()), "")
        v = {"phase": "working", "section": "working", "reason": "agent_running",
             "line": line("working", run=a.get("title"), text=said)}
    else:
        v = {"phase": "working", "section": "working", "reason": "starting",
             "line": line("starting", run=a.get("title"))}
    return dict(v, since=since) if since else v


def open_view(t, now, seen):
    """An open to-do's view before Someday and its steps."""
    said = _state["answers"].get(t["id"]) or {}
    base = _state["bases"].get(t["id"])
    if said.get("label"):  # a choice: Persona books it, as the prototype draws a1 answered
        return {"phase": "working", "section": "working", "reason": "persona_working",
                "line": line("persona_working", text="booking " + said["label"]), "since": said["at"]}
    if base and facts_key(t) == base["facts"] and not said.get("ended") and not dismissed(t):
        v = copy.deepcopy(base["view"])
        if said:  # explain_first: the round waits, its default no longer taken
            for a in v.get("asks", []):
                a.pop("default_at", None)
        if t["id"] == "t05":
            v["line"] = line("persona_working", text=NEWSLETTER[_state["churn"] % len(NEWSLETTER)])
        if v["phase"] == "ready":
            v = finished(t, v, now, seen)
        return v
    if live_agents(t):
        return run_view(t)
    return {"phase": "todo", "section": "todo", "reason": "rest"}


def own_view(t, now, last):
    """The to-do's view from its own facts, before its steps; open ones seen when the owner looked since last."""
    done = parse(t.get("done_at"))
    if done and not t.get("start_error"):
        return closed_view(t, done, now)
    seen = seen_since(t, last)
    v = someday(t, failed_start(t, done, now) if done else open_view(t, now, seen), now)
    return dict(v, seen=True) if seen else v


LEADS = ("needs_you", "working", "waiting", "can_wait")


def roll_up(t, v, steps, lasts):
    """A parent led by its most urgent open step when that is ahead of its own section (proposal 3.4 "Steps roll
    up"): the step's state and line (with `step`), its own and its steps' asks, the first window of either; seen
    against the latest activity of the parent and the steps on its row."""
    at = {s: i for i, s in enumerate(SECTIONS)}

    def ahead(sv):
        if sv["section"] == "can_wait":
            return v["section"] in ("todo", "just_finished", "someday")
        return sv["section"] in LEADS and at[sv["section"]] < at[v["section"]]

    lead = min(((s, sv) for s, sv in steps if ahead(sv)), default=None,
               key=lambda x: (LEADS.index(x[1]["section"]), x[1].get("since") or "", x[0]["created_at"],
                              x[0]["id"]))
    out = dict(v)
    on_row = [lead[0]] if lead else []
    if lead:
        s, sv = lead
        out = {k: sv[k] for k in ("phase", "section", "reason", "since", "can_wait") if k in sv}
        if "line" in sv:
            out["line"] = line(sv["line"]["key"], **dict(sv["line"].get("args", {}), step=s["title"]))
    windows = [x for x in [v.get("next_change_at")] + [sv.get("next_change_at") for _, sv in steps] if x]
    out.pop("next_change_at", None)
    if windows:
        out["next_change_at"] = min(windows, key=parse)
    out.pop("asks", None)
    if out["section"] in ("needs_you", "can_wait"):
        parts = [(None, v)] + list(steps)
        asking = [(s, sv) for s, sv in parts if sv.get("asks") and sv["section"] in ("needs_you", "can_wait")]
        first = [p for p in asking if p[0] is (lead[0] if lead else None)]
        rest = sorted((p for p in asking if p not in first),
                      key=lambda p: (p[1]["section"] != "needs_you", p[1].get("since") or "", p[0] is not None))
        asks = []
        for s, sv in first + rest:
            asks += [a if s is None else dict(a, id="step:%s/%s" % (s["id"], a["id"])) for a in sv["asks"]]
        if asks:
            out["asks"] = asks
        on_row += [s for s, _ in asking if s is not None and s not in on_row]
    if on_row:
        out.pop("seen", None)
        if seen_since(t, max([x for x in [lasts[t["id"]]] + [lasts[s["id"]] for s in on_row] if x], default=None)):
            out["seen"] = True
    return out


def place(rows, now):
    """Every row's view at now, steps rolled up into their parents; ranks are rank's."""
    lasts = {t["id"]: last_activity(t, now) for t in rows}
    own = {t["id"]: own_view(t, now, lasts[t["id"]]) for t in rows}
    kids = {}
    for t in rows:
        if t.get("part_of") and t["part_of"] != t["id"] and t["part_of"] in own:
            kids.setdefault(t["part_of"], []).append(t)
    views = {}
    for t in rows:
        v, steps = own[t["id"]], kids.get(t["id"], [])
        if steps and v["phase"] != "done":
            v = roll_up(t, v, [(s, own[s["id"]]) for s in steps if not closed(s)], lasts)
        if steps:
            v = dict(v, steps={"done": sum(1 for s in steps if closed(s)), "total": len(steps)})
        views[t["id"]] = v
    return views


def rank(rows, views):
    """Ranks within each section of one manager's list: the prototype's order, a row that moved there (or is new)
    first, newest first."""
    order = {tid: i for i, tid in enumerate(_state["order"])}
    by = {}
    for t in rows:
        by.setdefault(views[t["id"]]["section"], []).append(t)
    for section, ts in by.items():
        def key(t):
            if _state["homes"].get(t["id"]) == section and t["id"] in order:
                return 0, 0.0, order[t["id"]], ""
            moment = parse(views[t["id"]].get("since") or t.get("done_at") or t.get("created_at"))
            return -1, -(moment.timestamp() if moment else 0.0), 0, t["id"]
        for i, t in enumerate(sorted(ts, key=key)):
            views[t["id"]]["rank"] = i
    for tid, v in views.items():
        views[tid] = {k: v[k] for k in VIEW_KEYS if k in v}
    return views


# ---------- counts and places (server/internal/assistant/todo/projection/list.go) ----------

def carried(t, open_ids):
    return bool(t.get("part_of")) and t["part_of"] != t["id"] and t["part_of"] in open_ids


def counts(rows, views):
    out = {"open": 0, "needs_you": 0, "in_progress": 0, "sections": {s: 0 for s in sorted(SECTIONS)}}
    open_ids = {t["id"] for t in rows if views[t["id"]]["phase"] != "done"}
    for t in rows:
        if carried(t, open_ids):
            continue
        v = views[t["id"]]
        out["sections"][v["section"]] = out["sections"].get(v["section"], 0) + 1
        out["open"] += v["phase"] != "done" and v["section"] != "someday"
        out["needs_you"] += v["section"] == "needs_you" and not t.get("quiet")
        out["in_progress"] += v["section"] in ("working", "waiting")
    return out


def places(rows, views, total):
    titles = {p.get("id"): p.get("name") for p in _hub["KB"].get("projects", [])}
    out = [{"id": "all", "kind": "all", "title": "", "needs_you": total["needs_you"]},
           {"id": "today", "kind": "today", "title": "", "needs_you": 0},
           {"id": "upcoming", "kind": "upcoming", "title": "", "needs_you": 0}]
    open_ids = {t["id"] for t in rows if views[t["id"]]["phase"] != "done"}
    need = {}
    for t in rows:
        v = views[t["id"]]
        if carried(t, open_ids) or not t.get("project") or v["phase"] == "done" or v["section"] == "someday":
            continue
        need[t["project"]] = need.get(t["project"], 0) + (v["section"] == "needs_you" and not t.get("quiet"))
    for slug in sorted(need):
        out.append({"id": "project:" + slug, "kind": "project", "title": titles.get(slug) or slug,
                    "project": slug, "needs_you": need[slug]})
    return out


def add_up(objs):
    """The hub's sumMembers: integers added up member by member, objects key by key."""
    out = {}
    for o in objs:
        for k, v in o.items():
            if k in out:
                continue
            if isinstance(v, dict):
                out[k] = add_up([x[k] for x in objs if isinstance(x.get(k), dict)])
            elif isinstance(v, int) and not isinstance(v, bool):
                out[k] = sum(x.get(k, 0) for x in objs if isinstance(x.get(k), int))
            else:
                out[k] = v
    return out


# ---------- the answers ----------

def managers():
    """The managers whose to-dos the list merges, the default's first; one unless MOCK_TODO_MANAGERS=2."""
    roster = _hub["MANAGERS"]
    first = roster["default"]
    others = sorted(m["id"] for m in roster["list"]
                    if m["id"] != first and m.get("online") and not m.get("installing"))
    return [first] + others[:1] if TWO_MANAGERS else [first]


def owner(t, ids):
    if len(ids) < 2:
        return ids[0]
    tid = t.get("part_of") or t["id"]
    return ids[1] if _state["owners"].get(tid) == "weather" else ids[0]


def legacy_ask(t):
    """T01's decision as today's list carries it (`ask`) while its round waits: the prototype's facts still hold and no
    answer ended it (after explain_first it waits with no default taken, as decision.go keeps it)."""
    ask, base = _state["legacy"].get(t["id"]), _state["bases"].get(t["id"])
    said = _state["answers"].get(t["id"]) or {}
    if not ask or not base or facts_key(t) != base["facts"] or said.get("ended"):
        return None
    ask = copy.deepcopy(ask)
    if said:
        ask.update(response=said["response"], default="never")
        ask.pop("deadline", None)
    return ask


def answers(now):
    """Each manager's answer as it sends it: [(manager, rows with views, counts, places)]."""
    rows = sorted(_hub["sorted_todos"](), key=lambda t: t["id"])
    rows.sort(key=lambda t: t.get("created_at") or "", reverse=True)  # newest first; equal times by id
    for t in rows:
        ask = None if "ask" in t else legacy_ask(t)
        if ask:
            t["ask"] = ask
    ids = managers()
    out = []
    for mid in ids:
        mine = [t for t in rows if owner(t, ids) == mid]
        views = rank(mine, place(mine, now))
        total = counts(mine, views)
        for t in mine:
            if MODE == "old":  # stage 1's fields (#10226): no hub from before the view lists them
                t.pop("someday_until", None)
                t.pop("restored_at", None)
            else:
                t["view"] = views[t["id"]]
        out.append((mid, mine, total, places(mine, views, total)))
    return out


def item(tid, now):
    """The to-do as the item route answers it (unstamped, as the hub forwards it); None when there is none."""
    return next((t for _, rows, _, _ in answers(now) for t in rows if t["id"] == tid), None)


def stamped(mid, row):
    return dict({"manager": mid}, **row) if _hub["_stamps"]() else row


def list_answer(now):
    got = answers(now)
    obj = {"todos": [stamped(mid, t) for mid, rows, _, _ in got for t in rows]}
    if MODE != "old":
        obj["counts"] = add_up([c for _, _, c, _ in got])
    return obj


def places_answer(now):
    """GET /api/v1/todos/places as the hub merges it (mergeTodoPlaces): each fixed place once, its needs_you added
    up, then every manager's projects stamped with it, the default's first; the counts added up."""
    got = answers(now)
    fixed, own = [], []
    for mid, _, _, pl in got:
        for p in pl:
            if p["kind"] == "project":
                own.append(dict({"manager": mid}, **p))
                continue
            same = next((f for f in fixed if f["id"] == p["id"]), None)
            if same:
                same["needs_you"] += p["needs_you"]
            else:
                fixed.append(dict(p))
    return {"places": fixed + own, "counts": add_up([c for _, _, c, _ in got])}


def tag_matches(values, tag):
    opaque = tag[2:] if tag.startswith("W/") else tag
    for v in values:
        for t in v.split(","):
            t = t.strip()
            if t == "*" or (t[2:] if t.startswith("W/") else t) == opaque:
                return True
    return False


def send_list(h):
    """GET /api/v1/todos with the hub's validator (#9590): ETag W/"t1-<128 bits of the body's SHA-256>", 304 for
    it."""
    body = json.dumps(list_answer(datetime.now(timezone.utc)), ensure_ascii=False).encode()
    tag = 'W/"t1-' + hashlib.sha256(body).hexdigest()[:32] + '"'
    if tag_matches(h.headers.get_all("If-None-Match") or [], tag):
        h.send_response(304)
        h.send_header("ETag", tag)
        h.end_headers()
        return True
    h.send_response(200)
    h.send_header("Content-Type", "application/json; charset=utf-8")
    h.send_header("Content-Length", str(len(body)))
    h.send_header("Cache-Control", "no-store")
    h.send_header("ETag", tag)
    h.end_headers()
    h.wfile.write(body)
    return True


# ---------- the writes ----------

def find(tid):
    return next((x for x in _hub["TODOS"] if x["id"] == tid), None)


def body_of(h):
    """The request's JSON object; None when it is not one."""
    try:
        body = json.loads(getattr(h, "last_body", b"") or b"{}")
    except ValueError:
        return None
    return body if isinstance(body, dict) else None


def refused(h, tid, what="change todos"):
    """403 for a read-only role, 404 for a to-do the mock does not have; None when the write may go on."""
    if _hub["is_read_only"](h):
        return h.send_json({"error": "read-only role cannot " + what}, 403) or True
    if not find(tid):
        return h.send_json({"error": "todo not found", "code": "todo_not_found"}, 404) or True
    return None


def after(h, then):
    """Run then() once mock_hub.py's own handler has taken this request (a 2xx), before its answer goes out; an object
    then() returns is the answer instead. A refused write changes nothing here."""
    own, request = h.send_json, h.requestline

    def send_json(obj, status=200, *args, **kw):
        h.__dict__.pop("send_json", None)  # this request only: a kept-alive connection's next one answers as before
        if h.requestline == request and 200 <= status < 300:
            obj = then() or obj
        return own(obj, status, *args, **kw)

    h.send_json = send_json


def patched(tid, body):
    """What db.TodoEdit.Apply does beyond mock_hub.py's PATCH: done on a failed start is the owner closing it now, no
    longer failed; not done also clears a start error and takes the to-do out of Someday (#10226)."""
    def then():
        t = find(tid)
        if not t or "done" not in body:
            return None
        if body["done"] and t.get("start_error"):
            t.update(done_at=stamp(datetime.now(timezone.utc)), start_error="")
            t.pop("done_by", None)
            t.pop("done_reason", None)
        elif not body["done"] and (t.get("start_error") or t.get("someday_until")):
            t.pop("start_error", None)
            t.pop("someday_until", None)
        else:
            return None
        _hub["push_todos"]()
        return {"todo": _hub["format_todo"](t)}
    return then


def plan_answered(approve):
    """The Widget run goes on once its plan card is answered: building the widget when approved, else replanning."""
    def then():
        now = stamp(datetime.now(timezone.utc))
        for s in _hub["SESSIONS"]:
            if s["session_id"] == sid(1):
                s.update(status="working", state="working", pending_interactions=0, updated_at=now, state_since=now,
                         last_text="Building the widget." if approve else "Reworking the plan.")
        _hub["push_machine"]("macbook")
        _hub["push_todos"]()
    return then


def mark(h, tid, what):
    """PUT / DELETE …/someday and PUT …/restored (server/internal/assistant/todo_marks.go): {"todo": Todo}; neither
    is an edit, so updated_at stays."""
    if refused(h, tid):
        return True
    t = find(tid)
    now = datetime.now(timezone.utc)
    if what == "restored":
        t["restored_at"] = stamp(now)
    elif h.command == "DELETE":
        t.pop("someday_until", None)
    else:
        body = body_of(h)
        if body is None:
            return h.send_json({"error": "invalid body"}, 400)
        raw = body.get("until")
        until = parse(raw) if raw else NO_DATE
        if until is None or until.tzinfo is None or until <= now or until > NO_DATE:
            return h.send_json({"error": "Pick a time ahead, no later than the year 9999",
                                "code": "someday_until"}, 400)
        if closed(t):
            return h.send_json({"error": "Only an open to-do can be put off to Someday", "code": "todo_closed"}, 409)
        t["someday_until"] = stamp(until)
    _hub["push_todos"]()
    return h.send_json({"todo": _hub["format_todo"](t)})


def accept(h, tid):
    """POST …/accept {asked_at[, option | response]} (todo_accept_route.go): today's answer to T01's decision. The round
    listed now or 409; an option (one of its labels) or the plain press takes it: Persona books the choice and Pick a
    ryokan is done; explain_first keeps the round waiting with no default; the other fixed answers end it."""
    if refused(h, tid):
        return
    body = body_of(h)
    if not body or not body.get("asked_at"):
        return h.send_json({"error": "asked_at is required"}, 400)
    option, response = str(body.get("option") or ""), str(body.get("response") or "")
    if option and response:
        return h.send_json({"error": "give option or response, not both"}, 400)
    if response and response not in RESPONSES:
        return h.send_json({"error": "response must be decide_yourself, not_what_i_asked or explain_first"}, 400)
    t = find(tid)
    ask = legacy_ask(t)
    if not ask or ask["asked_at"] != body["asked_at"]:
        return h.send_json({"error": "This was already handled"}, 409)
    if option and option not in [o["label"] for o in ask.get("options", [])]:
        return h.send_json({"error": "no such option: %s" % option}, 400)
    decide(t, ask, option, response)
    return h.send_json({"ok": True})


def decide(t, ask, option, response):
    """T01's decision answered: an option or the plain press takes it (Persona books the choice and Pick a ryokan is
    done); explain_first keeps the round waiting with no default; the other fixed answers end it."""
    tid = t["id"]
    now = stamp(datetime.now(timezone.utc))
    if response:
        said = {"response": response, "ended": response != "explain_first"}
    else:
        said = {"label": option or ask["recommend"], "at": now, "ended": True}
        step = find(_state["settles"].get(tid, ""))
        if step and not step.get("done_at"):
            step.update(done_at=now, updated_at=now)
    _state["answers"][tid] = said
    sup = t.get("supervision") or {}
    if said["ended"] and sup.get("state") == "needs_user":
        sup["state"] = ""  # the round is over: no review waits on the owner (ownerCannotAnswer, chooseOption)
    _hub["push_todos"]()


def retry(h, tid):
    """POST …/retry (tasks.go, tasks_start.go retryTaskStart): a failed start Persona handed out reopens without its
    error and its Agent starts on the MacBook in a session of its own, working; an open one answers as it is; one
    without an origin, or closed some other way, is 409."""
    if refused(h, tid, "start tasks"):
        return
    t = find(tid)
    if not t.get("origin") or (t.get("done_at") and not t.get("start_error")):
        return h.send_json({"error": "Only a to-do Persona handed out whose start failed can be started again"}, 409)
    start_again(t)
    return h.send_json({"todo": item(tid, datetime.now(timezone.utc))})


def start_again(t):
    """A failed start Persona handed out reopens without its error, and its Agent starts on the MacBook in a session of
    its own, working."""
    if t.get("start_error"):
        now = stamp(datetime.now(timezone.utc))
        t.update(done_at=None, reopened_at=now, updated_at=now)
        for k in ("start_error", "done_by", "done_reason"):
            t.pop(k, None)
        sessions = _hub["SESSIONS"]
        sessions[:] = [s for s in sessions if s["session_id"] != RETRIED]
        sessions.append(_hub["sess"]("macbook", RETRIED, t["title"], PICTURES, "working", "working", "home", 8,
                                     t["title"], 50, 0, "Grouping the photos by trip.", updated_at=now,
                                     state_since=now))
        t["agents"] = [a for a in t.get("agents") or [] if a["session_id"] != RETRIED] + [
            {"machine_id": "macbook", "session_id": RETRIED, "linked_at": now, "released_at": None, "title": t["title"],
             "cwd": PICTURES, "agent": "claude", "start_error": "", "source": "spawned", "via": ""}]
        _hub["push_machine"]("macbook")
        _hub["push_todos"]()


# ---------- the asks route (#10465, feature todo_asks; docs/protocol/protocol.md, its row in the REST table) ----------

ASKS = re.compile(r"/api/v1/todos/([^/]+)/asks/([^/]+)/answer")


def asks_listed():
    """Whether the hello names todo_asks now: with the view, unless MOCK_TODO_VIEW_ASKS=0 (a hub whose managers answer
    only through today's routes)."""
    return listed() and os.environ.get("MOCK_TODO_VIEW_ASKS") != "0"


def answer_ask(h, tid, ask):
    """POST /api/v1/todos/{id}/asks/{ask}/answer under the hub's todo_answer receipt: with X-Persona-Op-Id, Processing
    goes out at once (todo_send_state, to the full devices that declared it), the final state with the answer, and the
    same op id gets the first reply back (202 pending after 10 s), with nothing done again."""
    op = (h.headers.get("X-Persona-Op-Id") or "").lower()
    if op:
        if not UUID.fullmatch(op):
            return h.send_json({"error": "operation id must be a UUID"}, 400)
        flight, claimed = join(_state["ops"], "answer:" + op)
        if not claimed:
            if not flight.done.wait(10):
                return h.send_json({"state": "pending"}, 202)
            status, reply = flight.answer
            return h.send_json(reply, status)

        def then(status, reply):
            flight.answer = (status, reply)
            flight.done.set()
            to_full_wanting(receipt("todo_send_state", todo_outcome(status, reply), todo_id=tid, client_op_id=op))

        to_full_wanting(receipt("todo_send_state", "processing", todo_id=tid, client_op_id=op))
        answered(h, then)
    return answer_now(h, tid, ask)


def answer_now(h, tid, ask):
    """The answer itself, by the id the row's view lists: T01's decision (an option, none for the recommendation, a
    fixed answer, later, decline, undo with its receipt) and T03's retry (no answer: started again)."""
    if _hub["is_read_only"](h):
        return h.send_json({"error": "read-only role cannot answer to-dos", "delivered": "none"}, 403)
    t = find(tid)
    if not t:
        return h.send_json({"error": "todo not found", "code": "todo_not_found", "delivered": "none"}, 404)
    body = body_of(h)
    given = [k for k in ("option", "response", "text", "outcome") if (body or {}).get(k)]
    if body is None or len(given) > 1:
        return h.send_json({"error": "give one answer", "delivered": "none"}, 400)
    if ask.startswith("run:") or "/run:" in ask:
        return h.send_json({"error": "A run's ask is answered at its card", "code": "answer_in_session", "delivered": "none"}, 409)
    if not re.match(r"(todo:(decision|retry)@|step:)", ask):
        return h.send_json({"error": "no such ask", "code": "ask_not_found", "delivered": "none"}, 404)
    now = datetime.now(timezone.utc)
    row = item(tid, now)
    waiting = {a["id"]: a for a in ((row or {}).get("view") or {}).get("asks", [])}
    outcome = str(body.get("outcome") or "")
    if outcome == "undo":
        book = _hub["DECISION_DISMISSALS"].get(tid)
        if not book or book["undone"] or book["dismissal_id"] != str(body.get("receipt") or ""):
            return h.send_json({"error": "That was not declined", "code": "ask_stale", "delivered": "none", "todo": row}, 409)
        book["undone"] = True
        _hub["push_todos"]()
        return h.send_json({"ask": ask, "outcome": "undone", "todo": item(tid, now)})
    if ask not in waiting:
        return h.send_json({"error": "This was already handled", "code": "ask_stale", "delivered": "none", "todo": row}, 409)
    kind = waiting[ask]["kind"]
    if outcome == "later":
        return h.send_json({"ask": ask, "outcome": "later", "todo": row})
    if kind == "retry":
        if given:
            return h.send_json({"error": "A start that failed takes no answer", "delivered": "none"}, 400)
        start_again(t)
        return h.send_json({"ask": ask, "outcome": "answered", "todo": item(tid, datetime.now(timezone.utc))})
    legacy = legacy_ask(t)
    if not legacy:
        return h.send_json({"error": "This was already handled", "code": "ask_stale", "delivered": "none", "todo": row}, 409)
    if outcome == "decline":
        review = _hub["current_decision_review"](t)
        if not review:
            return h.send_json({"error": "No review waits on you", "code": "not_declinable", "delivered": "none"}, 409)
        receipt_id = "dsm_" + hashlib.sha256((tid + review["review_at"] + str(time.time())).encode()).hexdigest()[:8]
        _hub["DECISION_DISMISSALS"][tid] = {"review_at": review["review_at"], "dismissal_id": receipt_id, "undone": False}
        _hub["push_todos"]()
        return h.send_json({"ask": ask, "outcome": "declined", "receipt": receipt_id, "todo": item(tid, now)})
    if outcome:
        return h.send_json({"error": "outcome must be later, decline or undo", "delivered": "none"}, 400)
    option, response = str(body.get("option") or ""), str(body.get("response") or "")
    if not (option or response or body.get("text")):
        # As the hub's errAnswerNeeded: a decision has no one action, so it takes an option, a fixed answer or words.
        return h.send_json({"error": "a decision takes an option, a fixed answer or your words", "delivered": "none"}, 400)
    if response and response not in RESPONSES:
        return h.send_json({"error": "no such fixed answer", "delivered": "none"}, 400)
    if option and option not in [o["label"] for o in legacy.get("options", [])]:
        return h.send_json({"error": "no such option: %s" % option, "delivered": "none"}, 400)
    if body.get("text"):
        option = legacy["recommend"]  # the owner's words: the mock takes them as the recommendation
    decide(t, legacy, option, response)
    return h.send_json({"ask": ask, "outcome": "answered", "todo": item(tid, datetime.now(timezone.utc))})


# ---------- send receipts (#10465; docs/protocol/protocol.md "Message feedback (#5258)") ----------

ROUTE = re.compile(r"/api/v1/assistant/conversations/[^/]+/route")
SESSION_SEND = re.compile(r"/api/v1/sessions/([^/]+)/([^/]+)/messages")
UUID = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
_flights = threading.Lock()


class Flight:
    """One claimed write: a repeat of its id waits for its answer, as the hub's withClientOp and send flight do."""

    def __init__(self):
        self.done = threading.Event()
        self.answer = None  # (status, body), once answered


def join(flights, key, taken=()):
    """(key's flight, whether this request claimed it); (None, False) for a key already taken."""
    with _flights:
        if key in taken:
            return None, False
        if key in flights:
            return flights[key], False
        flight = flights[key] = Flight()
        return flight, True


def receipt_versions():
    """The hello's receipt markers, which only the hub writes (server/internal/hub/hello.go): message_send_state on
    every hub since #5258, todo_send_state from #11080 on, so none under old."""
    return {"message_send_state": 1} if MODE == "old" else {"message_send_state": 1, "todo_send_state": 1}


def receipt(kind, state, **ids):
    return dict({"type": kind}, **ids, state=state, at=datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"))


def to_full_wanting(event):
    """To the full devices that declared todo_send_state in client_hello or the socket's ?features= (the hub's
    toFullWanting and client.wants)."""
    with _hub["FAULT_LOCK"]:
        sends = [c["send"] for c in _hub["CLIENTS"].values()
                 if c.get("role") == "full" and "todo_send_state" in c["features"] | c.get("asked", set())]
    for send in sends:
        try:
            send(dict(event))
        except Exception:  # the socket went meanwhile
            pass


def to_subscribers(sid, event):
    """To the sockets subscribed to the session (the hub's toSubscribers)."""
    for send, subscribed in list(_hub["LIVE"]):
        if sid in subscribed:
            try:
                send(dict(event))
            except Exception:  # the socket went meanwhile
                pass


def todo_outcome(status, answer):
    """How a to-do write ended, read as the hub reads its relay (todo_receipts.go todoSendOutcome): failed only when
    nothing was taken."""
    answer = answer if isinstance(answer, dict) else {}
    if 200 <= status < 300:
        return "delivered"
    if answer.get("delivered") == "none" or answer.get("code") == "assistant_offline":
        return "failed"
    if not answer.get("delivered") and status in (400, 401, 403, 404):
        return "failed"
    return "unknown"


def message_outcome(status, answer):
    """How a session's message ended (send.go messageSendState)."""
    answer = answer if isinstance(answer, dict) else {}
    if status == 200:
        return "delivered"
    if status == 202 and answer.get("delivered") == "later":
        return "kept"
    return "unknown" if answer.get("delivered") == "unknown" else "failed"


def answered(h, then):
    """then(status, answer) as this request is answered, just before the answer goes out."""
    own, request = h.send_json, h.requestline

    def send_json(obj, status=200, *args, **kw):
        h.__dict__.pop("send_json", None)  # this request only: a kept-alive connection's next one answers as before
        if h.requestline == request:
            then(status, obj)
        return own(obj, status, *args, **kw)

    h.send_json = send_json


def receipts(h, path):
    """Processing at once, before mock_hub.py relays the write, and its final state as it is answered:
    todo_send_state for a message on a to-do's conversation route under X-Persona-Op-Id (the hub's todo_route
    receipt), and message_send_state for a session's message with a client_msg_id (the hub's send flight). Only the
    request that claimed the id says anything: the same op id waits up to 10 s for the first answer and gets it back,
    or 202 pending (withClientOp), with nothing done again; the same client_msg_id in flight waits for the first answer,
    and once taken mock_hub.py answers it as sent. What the hub refuses before it claims the id says nothing: an op id
    that is not a UUID (400), a read-only device's message to a to-do (403). MOCK_SEND_DELAY_MS holds the to-do's
    message as long as a session's. The asks route is not served here (no todo_asks), so it gets none. True when the
    request was answered here."""
    body = body_of(h) or {}
    op = (h.headers.get("X-Persona-Op-Id") or "").lower()
    if ROUTE.fullmatch(path) and op and MODE != "old":
        if not UUID.fullmatch(op):
            return h.send_json({"error": "operation id must be a UUID"}, 400) or True
        flight, claimed = join(_state["ops"], op)
        if not claimed:
            if not flight.done.wait(10):
                return h.send_json({"state": "pending"}, 202) or True
            status, answer = flight.answer
            return h.send_json(answer, status) or True
        todo = body.get("todo_id") if isinstance(body.get("todo_id"), str) else ""

        def then(status, answer):
            flight.answer = (status, answer)
            flight.done.set()
            if todo:  # the hub says nothing of a write that names no to-do
                to_full_wanting(receipt("todo_send_state", todo_outcome(status, answer), todo_id=todo, client_op_id=op))

        if todo:
            to_full_wanting(receipt("todo_send_state", "processing", todo_id=todo, client_op_id=op))
            time.sleep(int(os.environ.get("MOCK_SEND_DELAY_MS") or 0) / 1000)
        answered(h, then)
        return False
    m = SESSION_SEND.fullmatch(path)
    msg = body.get("client_msg_id")
    if not (m and isinstance(msg, str) and msg):
        return False
    if body.get("todo_id") and _hub["is_read_only"](h):
        return False  # refused before the hub's send flight (send.go)
    flight, claimed = join(_state["flying"], msg, _state["taken"])
    if flight is None:  # taken: mock_hub.py answers it as sent, as the hub's kept flight does
        return False
    if not claimed:  # the hub's send flight: the first answer, as long as the send takes
        flight.done.wait(600)
        status, answer = flight.answer or (502, {"error": "the first send never answered", "delivered": "unknown"})
        return h.send_json(answer, status) or True
    mid, sid = unquote(m[1]), unquote(m[2])

    def said(state):
        return receipt("message_send_state", state, machine_id=mid, session_id=sid, client_msg_id=msg)

    def then(status, answer):
        state = message_outcome(status, answer)
        flight.answer = (status, answer)
        with _flights:
            if state != "failed":  # nothing typed: the same id may go again
                _state["taken"].add(msg)
            if _state["flying"].get(msg) is flight:
                del _state["flying"][msg]
        flight.done.set()
        to_subscribers(sid, said(state))

    to_subscribers(sid, said("processing"))
    answered(h, then)
    return False


def reset():
    """/mock/reset, before mock_hub.py puts the to-dos back: the answers, the churn, the runs' sessions and the Widget's
    plan card as they started, no send receipts kept, and under late the hellos from the start of their sequence."""
    _state["churn"] = 0
    _state["seen_hours"] = 0.0
    _state["answers"].clear()
    _state["ops"].clear()
    _state["flying"].clear()
    _state["taken"].clear()
    first = _state["sessions"]
    _hub["SESSIONS"][:] = [copy.deepcopy(first[s["session_id"]]) if s["session_id"] in first else s
                           for s in _hub["SESSIONS"] if s["session_id"] != RETRIED]
    with _hub["REQUEST_LOCK"]:
        for name in ("INTERACTIONS", "RESOLVED", "INTERACTION_HISTORY"):
            _hub[name][:] = [x for x in _hub[name] if x.get("id") != PLAN_CARD]
        _hub["INTERACTIONS"].append(copy.deepcopy(_state["card"]))
    if MODE == "late":
        start_late()


def route(h, method, path):
    """The routes above; False for anything else, which mock_hub.py answers."""
    if method == "POST" and receipts(h, path):
        return True
    if method == "GET" and path == "/mock/todo-view":
        query = parse_qs(urlparse(h.path).query)
        hours = query.get("seen_hours")
        if hours:
            _state["seen_hours"] = float(hours[0])
        tid, _, back = (query.get("done_hours") or [""])[0].partition(":")
        closed = find(tid) if tid and back else None
        if closed and closed.get("done_at"):
            closed["done_at"] = stamp(datetime.now(timezone.utc) - timedelta(hours=float(back)))
            _hub["push_todos"]()
        return h.send_json({"mode": MODE, "listed": listed(), "phase": _state["phase"], "managers": managers(),
                            "churn": _state["churn"]}) or True
    query = parse_qs(urlparse(h.path).query) if path == "/mock/churn" else {}
    if method == "GET" and query.get("todos") == ["1"]:
        n, ms = int(query.get("n", ["20"])[0]), int(query.get("ms", ["3000"])[0])
        _hub["CHURN"]["rounds"] = 0
        threading.Thread(target=churn, args=(n, ms), daemon=True).start()
        return h.send_json({"ok": True}) or True
    if method == "GET" and path == "/mock/reset":
        reset()
        return False  # and mock_hub.py's reset puts the to-dos back
    if method == "POST" and path == "/api/v1/interactions/%s/resolve" % PLAN_CARD:
        after(h, plan_answered((body_of(h) or {}).get("approve") is True))
        return False  # mock_hub.py resolves the card
    m = ASKS.fullmatch(path)
    if m and method == "POST":
        if MODE == "old" or not asks_listed():  # a hub without todo_asks: no such route
            return h.send_json({"error": "not found"}, 404) or True
        return answer_ask(h, unquote(m[1]), unquote(m[2])) or True
    parts = path.split("/")  # ['', 'api', 'v1', 'todos', id, what]
    if parts[:4] != ["", "api", "v1", "todos"]:
        return False
    now = datetime.now(timezone.utc)
    if len(parts) == 4:
        return method == "GET" and send_list(h)
    tid = unquote(parts[4])
    if len(parts) == 5 and method == "GET":
        if MODE == "old":  # an older hub: the path matches its PATCH and DELETE routes only
            return h.send_json({"error": "method not allowed"}, 405) or True
        if tid == "places":
            return h.send_json(places_answer(now)) or True
        t = item(tid, now)
        return h.send_json({"todo": t} if t else {"error": "todo not found", "code": "todo_not_found"},
                           200 if t else 404) or True
    if len(parts) == 5 and method == "PATCH":
        body = body_of(h)
        if body is not None and "done" in body:
            after(h, patched(tid, body))
        return False  # mock_hub.py's PATCH writes it
    if len(parts) != 6:
        return False
    what = parts[5]
    if (what, method) in (("someday", "PUT"), ("someday", "DELETE"), ("restored", "PUT")):
        if MODE == "old":
            return h.send_json({"error": "not found"}, 404) or True
        return mark(h, tid, what) or True
    if method != "POST":
        return False
    if what == "accept":
        return accept(h, tid) or True
    if what in ("decision-dismiss", "decision-undo-dismiss"):
        if not refused(h, tid):
            _hub["decision_dismissal_route"](h, tid, what == "decision-undo-dismiss")
        return True
    if what == "retry":
        return retry(h, tid) or True
    return False


def churn(n, ms):
    for _ in range(n):
        time.sleep(ms / 1000)
        _state["churn"] += 1
        _hub["CHURN"]["rounds"] += 1
        _hub["push_todos"]()


# ---------- MOCK_TODO_VIEW=late ----------

def listed():
    """Whether the hub's hello names todo_view and todo_places now."""
    return MODE == "1" or (MODE == "late" and _state["phase"] in (1, 3))


def start_late():
    """(Re)start the late sequence: phase 0, every socket closed so its next hello lacks the view."""
    _state["late_run"] += 1
    flip(0)
    threading.Thread(target=late, args=(_state["late_run"],), daemon=True).start()


def late(run):
    """Once an app has connected, move the phase on three times, LATE_SECONDS apart; a newer run ends this one."""
    def wait(seconds):
        end = time.monotonic() + seconds
        while _state["late_run"] == run and time.monotonic() < end:
            time.sleep(0.05)
        return _state["late_run"] == run

    while not _hub["CLIENTS"]:
        if not wait(0.1):
            return
    for phase in (1, 2, 3):
        if not wait(LATE_SECONDS):
            return
        flip(phase)


def flip(phase):
    """Set the phase and close every socket as a restarting hub does, so each says hello again. A socket joins SOCKETS
    before its hello reads the phase, so the sockets taken once the phase moved include every one whose hello was built
    before, also one not in CLIENTS yet; one whose hello already read the new phase may be closed too and says the
    same again."""
    with _hub["FAULT_LOCK"]:
        _state["phase"] = phase
        socks = list(_hub["SOCKETS"])
    for conn in socks:
        close(conn)


def close(conn):
    """Close one socket with 1001 once it is in CLIENTS (a moment for one between its hello and that), else cut it."""
    end = time.monotonic() + 1
    while time.monotonic() < end:
        client = _hub["CLIENTS"].get(conn)
        if client:
            try:
                client["close"]()
            except Exception:  # gone meanwhile
                pass
            return
        if conn not in _hub["SOCKETS"]:
            return
        time.sleep(0.02)
    try:
        conn.shutdown(socket.SHUT_RDWR)
    except OSError:
        pass


def install(hub):
    """With MOCK_TODO_VIEW, put the prototype's to-dos, their runs' sessions, the Widget's plan card and the projects in
    place of the mock's own, and the view's features in the hello; mock_hub.py calls it once its other switches have
    run. False without it."""
    global _hub
    if not MODE:
        return False
    if MODE not in MODES:
        sys.exit("mock hub: MOCK_TODO_VIEW must be 1, old or late, not %r" % MODE)
    _hub = hub
    c = Clock(hub["NOW"].replace(second=0, microsecond=0))
    rows, views, legacy, settles, sessions, card = prototype(c, hub.get("ASK1", ""))
    hub["TODOS_INIT"][:] = rows
    hub["TODOS"][:] = copy.deepcopy(rows)
    for n, (run, title, status, state, updated, since, last, pending) in enumerate(sessions, 46):
        # in the weather-app space the mock's MacBook already has, a tab each
        s = hub["sess"]("macbook", run, title, WEATHER, status, state, "weather-app", 7, title, n, 0, last,
                        pending=pending, updated_at=updated, state_since=since)
        hub["SESSIONS"].append(s)
        _state["sessions"][run] = copy.deepcopy(s)
    hub["INTERACTIONS"].append(copy.deepcopy(card))
    hub["KB"].setdefault("projects", []).extend(
        {"kind": "projects", "id": pid, "name": name, "one_line": line_, "importance": "P2", "relevance": 50,
         "updated": c.at(0, 9, 0), "extra": "short · active"}
        for pid, name, line_ in (("kyoto", "Kyoto trip", "Apr 3–9, two of us, a ryokan near Gion"),
                                 ("weather", "Weather App", "1.3 ships Friday with the new forecast view"),
                                 ("home", "Home", "The house, the car and the school"),
                                 # no to-dos: a project's place with nothing open (its empty state)
                                 ("garden", "Garden", "Spring planting and the new beds")))
    by_id = {t["id"]: t for t in rows}
    _state.update(order=[t["id"] for t in rows], legacy=legacy, settles=settles, card=card,
                  owners={t["id"]: t.get("project") for t in rows if not t.get("part_of")},
                  bases={tid: {"view": v, "facts": facts_key(by_id[tid])} for tid, v in views.items()})
    _state["homes"] = {tid: v["section"] for tid, v in place(rows, c.start).items()}

    before = hub["features"]

    def features():
        f = before()
        if not listed():
            return f
        return f + [x for x in ("todo_view", "todo_places") + (("todo_asks",) if asks_listed() else ()) if x not in f]

    hub["features"] = features
    hub["send_state_hello"] = receipt_versions
    if MODE == "late":
        start_late()
    return True
