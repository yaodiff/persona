"""The knowledge base's project tree in the mock hub (#12927, docs/assistant/knowledge.md "Project parents and
milestones"), a domain of scripts/mock/mock_domains.py. MOCK_KB_TREE=1 replaces the knowledge base's projects with
fictional ones nested under parents, with milestone flags and tags, and answers as the hub does:
GET /api/v1/kb/projects (each row's `parent` and `milestones`), GET /api/v1/kb/projects/{id} (its `tree`: parent,
ancestors, children, milestones, depth) and PUT /api/v1/kb/projects/{id} with `parent`, `milestone` (add, change,
remove), `milestones` (replace) or `name`, refusing the same parents (parent_self, parent_cycle, parent_not_found) and
answering with the `undo` and the new `tree`. scripts/mock/test_project_tree_mock.py drives it.
"""
import copy
import json
import os
from datetime import datetime, timedelta

import mock_domains

MODE = os.environ.get("MOCK_KB_TREE", "")
HELP = "MOCK_KB_TREE=1  the knowledge base's projects as a tree with milestone flags (fictional; #12927)"
HUB = None
PROJECTS = []  # the rows, as GET /api/v1/kb/projects answers them
LOGS = {}  # slug -> timeline lines
SEQ = {"n": 0}


def fixtures():
    def row(id, name, one, parent="", status="active", imp="P2", tags=(), ms=(), days=1):
        r = {"kind": "projects", "id": id, "name": name, "one_line": one, "importance": imp, "relevance": 60,
             "tags": list(tags), "updated": HUB.ago(days=days), "extra": "short · " + status}
        if parent:
            r["parent"] = parent
        if ms:
            r["milestones"] = [dict(m) for m in ms]
        return r
    return [
        row("garden-app", "Garden app", "Plan, water and photograph a small garden", imp="P0", tags=("mobile", "q4"), days=0,
            ms=[{"id": "m-garden-1", "title": "Public beta", "target": "2026-12-15", "state": "open"}]),
        row("seed-planner", "Seed planner", "Sowing calendar by region", "garden-app", tags=("mobile",),
            ms=[{"id": "m-seed-1", "title": "Calendar for 12 regions", "target": "2026-11-30", "state": "accepted"}]),
        row("watering-reminders", "Watering reminders", "Reminders that follow the weather", "garden-app", imp="P1", tags=("mobile", "q4"),
            ms=[{"id": "m-water-1", "title": "Rain skips a reminder", "target": "2026-12-05", "state": "open"}]),
        row("plant-photos", "Plant photos", "A photo a week per bed", "garden-app", status="paused", tags=("mobile",)),
        row("q4-launch", "Q4 launch", "Everything the December launch needs", imp="P0", tags=("q4",), days=0),
        row("store-listing", "Store listing", "Screenshots and text for the stores", "q4-launch", imp="P1", tags=("q4",),
            ms=[{"id": "m-store-1", "title": "Listing approved", "target": "2026-12-01", "state": "not_accepted"}]),
        row("press-kit", "Press kit", "Logo, short story and three photos", "store-listing", tags=("q4",)),
        row("beta-feedback", "Beta feedback", "Sort what testers send", "q4-launch", status="done", tags=("q4",)),
        row("docs-site", "Docs site", "Guides and the help center", tags=("web",)),
        row("reading-list", "Reading list", "Books for the quarter", status="done", imp="P3"),
        row("home-move", "Home move", "Boxes, the van and the new address", status="paused", imp="P3"),
    ]


def yesterday_lines():
    y = (datetime.now() - timedelta(days=1)).strftime("%Y-%m-%d")
    return {
        "garden-app": [f"- {y} 09:40 · Persona · Beta build sent to 20 testers", f"- {y} 17:05 · Persona · Two crash reports on older phones, fix planned"],
        "watering-reminders": [f"- {y} 11:20 · Persona · Weather lookup now skips a reminder when rain is forecast"],
        "q4-launch": [f"- {y} 15:00 · Persona · Launch date set for December 10"],
    }


def reset():
    PROJECTS[:] = fixtures()
    LOGS.clear()
    LOGS.update(yesterday_lines())
    HUB.KB["projects"] = PROJECTS


def by_id():
    return {r["id"]: r for r in PROJECTS}


def parents():
    rows = by_id()
    return {r["id"]: r["parent"] for r in PROJECTS if r.get("parent") in rows}


def status(r):
    return (r.get("extra") or "short · active").split(" · ")[1]


def tree(slug):
    rows, ps = by_id(), parents()
    kids = {}
    for c, p in ps.items():
        kids.setdefault(p, []).append(c)

    def ref(i):
        r = rows[i]
        return {"id": i, "name": r["name"], "status": status(r), "one_line": r.get("one_line", ""), "updated": r["updated"],
                **({"children": len(kids[i])} if kids.get(i) else {}), **({"milestones": len(r["milestones"])} if r.get("milestones") else {})}
    way, at, seen = [], ps.get(slug), {slug}
    while at and at not in seen:
        seen.add(at)
        way.insert(0, ref(at))
        at = ps.get(at)
    r = rows[slug]
    out = {"ancestors": way, "children": sorted((ref(k) for k in kids.get(slug, [])), key=lambda x: x["name"]),
           "milestones": r.get("milestones") or [], "depth": 1 + len(way)}
    if way:
        out["parent"] = way[-1]
    if r.get("parent_locked"):
        out["parent_locked"] = True
    return out


def refuse(h, code, text):
    return h.send_json({"error": text, "code": code}, 400)


def detail(h, req):
    slug = req.match.group(1)
    r = by_id().get(slug)
    if r is None:
        return False
    meta = {k: r[k] for k in ("name", "importance", "relevance", "tags") if k in r}
    meta.update(kind="short", status=status(r))
    for k in ("parent", "milestones"):
        if r.get(k):
            meta[k] = r[k]
    d = {"kind": "projects", "id": slug, "path": "/home/me/.persona/data/projects/" + slug, "meta": meta,
         "summary": r.get("one_line", ""), "log": LOGS.get(slug, [])}
    return h.send_json({"detail": d, "tree": tree(slug)})


def update(h, req):
    slug = req.match.group(1)
    rows = by_id()
    r = rows.get(slug)
    if r is None:
        return False
    try:
        body = req.json()
    except ValueError:
        return h.send_json({"error": "bad json"}, 400)
    if not any(k in body for k in ("parent", "milestone", "milestones", "name")):
        return False  # tagging and the rest: mock_hub.py's own PUT
    undo = {}
    if "parent" in body:
        want = str(body["parent"] or "").strip()
        p = rows.get(want) or next((x for x in PROJECTS if x["name"] == want), None) if want else None
        if want and (p is None or status(p) in ("merged", "ignored")):
            return refuse(h, "parent_not_found", "That parent project doesn’t exist, was merged into another or isn’t a project.")
        if p is not None and p["id"] == slug:
            return refuse(h, "parent_self", "A project can’t sit under itself.")
        ps = parents()
        at, seen = p["id"] if p else "", set()
        while at and at not in seen:
            if at == slug:
                return refuse(h, "parent_cycle", "That project is already under this one, so this one can’t go under it.")
            seen.add(at)
            at = ps.get(at)
        undo["parent"] = r.get("parent", "")
        if p:
            r["parent"] = p["id"]
        else:
            r.pop("parent", None)
        r["parent_locked"] = True
    if "milestone" in body or "milestones" in body:
        before = copy.deepcopy(r.get("milestones") or [])
        undo["milestones"] = before
        if "milestones" in body:
            ms = [dict(m) for m in body["milestones"] or []]
        else:
            m, ms = body["milestone"] or {}, copy.deepcopy(before)
            i = next((i for i, x in enumerate(ms) if x["id"] == m.get("id")), -1)
            if m.get("id") and i < 0:
                return refuse(h, "milestone_not_found", "That milestone isn’t on this project any more.")
            if m.get("remove"):
                del ms[i]
            elif i < 0:
                SEQ["n"] += 1
                ms.append({"id": "m-new-%d" % SEQ["n"], "title": "", "state": "open"})
                i = len(ms) - 1
            if not m.get("remove"):
                for k in ("title", "target", "state"):
                    if k in m:
                        ms[i][k] = m[k]
        for m in ms:
            m.setdefault("state", "open")
            if not str(m.get("title", "")).strip() or m["state"] not in ("open", "accepted", "not_accepted"):
                return refuse(h, "milestone_invalid", "That milestone isn’t valid.")
            if not m.get("target"):
                m.pop("target", None)
            if not m.get("id"):
                SEQ["n"] += 1
                m["id"] = "m-new-%d" % SEQ["n"]
        if ms:
            r["milestones"] = ms
        else:
            r.pop("milestones", None)
    if str(body.get("name") or "").strip():
        r["name"] = body["name"].strip()
    HUB.KB_UPDATES.append({"kind": "projects", "id": slug, **body})
    return h.send_json({"id": slug, "undo": undo, "tree": tree(slug)})


def register(d):
    global HUB
    HUB = d.hub
    d.help(HELP)
    if not MODE:
        return
    d.route("GET", r"/api/v1/kb/projects/([^/]+)", detail)
    d.route("PUT", r"/api/v1/kb/projects/([^/]+)", update)
    d.on_reset(reset)
    reset()
