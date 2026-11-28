"""To-dos waiting for different reasons, each with the hub's typed `wait` (#16168), a domain of mock_domains.py.

MOCK_TODO_WAIT_REASONS=1: synthetic waiting and parked to-dos carrying server-computed strongly-typed `wait` payloads
(person reply with contact, merge review, PR merge with blockers, date wait, agent wait, usage limit) plus one active
working to-do for contrast, in place of the other to-dos. Everyday made-up examples only. GET /mock/reset copies them
back, as it does every to-do fixture.
"""
import copy
import os

HELP = ("MOCK_TODO_WAIT_REASONS=1  the to-dos wait for different reasons, each with the hub's typed `wait` "
        "(person reply, PR merge, agent, date, usage limit), plus one working to-do (#16168)")


def register(d):
    d.help(HELP)
    if os.environ.get("MOCK_TODO_WAIT_REASONS") != "1":
        return
    hub = d.hub
    _wr_work_sid = "c3d5e7f9-0000-4000-8000-161680000001"
    hub.SESSIONS.append(hub.sess("macbook", _wr_work_sid, "Build release notes", "/Users/me/code/site", "working",
                                 "working", "site", 9, "1", 1, 3, "Drafting the changelog summary."))

    def _wr_todo(tid, title, mins, **kw):
        t = {"id": tid, "title": title, "notes": "", "done_at": None, "created_at": hub.ago(hours=6),
             "updated_at": hub.ago(minutes=mins), "origin": "conv_demo", "spaces": [], "agents": [], "priority": ""}
        t.update(kw)
        return t

    todos = [
        _wr_todo("twr_working", "Draft November release notes", 3,
                 agents=[{"machine_id": "macbook", "session_id": _wr_work_sid, "linked_at": hub.ago(minutes=25),
                          "released_at": None, "title": "Build release notes", "cwd": "/Users/me/code/site",
                          "agent": "claude", "start_error": "", "source": "spawned", "via": ""}]),
        _wr_todo("twr_reply", "Confirm conference venue booking", 12,
                 wait={
                     "kind": "person_reply",
                     "waiting_on": "Alex Chen",
                     "reason": "Waiting for Alex Chen to confirm room availability and catering quote",
                     "wake_condition": "Alex replies in the booking email thread",
                     "until": hub.ago(hours=-30),
                     "since": hub.ago(hours=3),
                     "url": "https://mail.example.com/threads/venue-booking-42",
                     "contact": {
                         "channel": "email",
                         "sender": "Persona",
                         "sent_at": hub.ago(hours=3),
                         "url": "https://mail.example.com/threads/venue-booking-42",
                         "request": "Asked whether Room B is open on Friday afternoon and requested the final quote.",
                     },
                 }),
        _wr_todo("twr_pr", "Fix CSV export escaping", 18,
                 pr_url="https://github.com/example/app/pull/418",
                 wait={
                     "kind": "pr_merge",
                     "waiting_on": "PR #418",
                     "reason": "Escape commas and quotes in exported invoices",
                     "url": "https://github.com/example/app/pull/418",
                     "pr_blockers": ["review_required", "checks_pending"],
                 }),
        _wr_todo("twr_agent", "Connect login screen to new auth endpoint", 25,
                 wait={
                     "kind": "agent",
                     "waiting_on": "Auth API Worker",
                     "reason": "Waiting for backend auth PR to merge before rebasing",
                     "wake_condition": "Rebase branch and run end-to-end login checks",
                     "wake_at": hub.ago(minutes=-45),
                     "since": hub.ago(minutes=25),
                 }),
        _wr_todo("twr_date", "Publish holiday store banner", 40,
                 wait={
                     "kind": "date",
                     "waiting_on": "Launch morning",
                     "reason": "Hold banner update until campaign launch time",
                     "wake_condition": "Publish banner automatically at launch time",
                     "wake_at": hub.ago(hours=-48),
                     "until": hub.ago(hours=-48),
                     "since": hub.ago(hours=2),
                 }),
        _wr_todo("twr_limit", "Summarize quarterly customer feedback", 55,
                 wait={
                     "kind": "usage_limit",
                     "waiting_on": "Feedback Worker",
                     "wake_at": hub.ago(hours=-2),
                     "since": hub.ago(minutes=55),
                 }),
    ]
    # Domains load after mock_hub.py copied TODOS_INIT into TODOS; /mock/reset copies TODOS_INIT again.
    hub.TODOS_INIT[:] = todos
    hub.TODOS[:] = copy.deepcopy(todos)
