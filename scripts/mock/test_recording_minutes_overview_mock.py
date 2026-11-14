"""Mock hub: a meeting's minutes as the summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)").

MOCK_RECORDING_MINUTES_SHAPE (and POST /mock/recording_minutes {"shape"}) is legacy, overview or malformed. legacy has no
`overview`; overview has one the hub would write (present means usable, every point and decision one bullet, the
groups, order, limits and icons as guaranteed); malformed has one the contract's "Clients" rules clean down to nothing,
beside the legacy minutes unchanged. Every shape has `markdown` (the legacy layout byte for byte as the hub renders it,
or the summary card's, with no checkboxes), and action ids, confirmation and 409 minutes_stale work the same.
"""
from datetime import datetime, timedelta
import importlib.util
import json
import math
import os
from pathlib import Path
import time
import unittest
import uuid

from test_meeting_attendance_mock import Hub, start
from test_meeting_photos_mock import photo

HERE = Path(__file__).resolve().parent
FIXTURE = HERE.parents[1] / "spec" / "fixtures" / "minutes-overview" / "cases.json"
SHAPES = ("legacy", "overview", "malformed")
ICONS = ("progress", "schedule", "release", "performance", "metrics", "tooling", "quality", "design", "people",
         "customer", "budget", "risk", "general")
STARTED = "2026-11-10T14:00:00+08:00"
CALENDAR = {"title": "Sprint review", "location": "Room 3", "attendees": ["Ada", "Bo"]}
# Four minutes of a meeting, each about one subject: four topics, so two titled groups. The mock's keywords make the
# decisions (决定), the action item (负责, by 周五前) and the open question (没定).
LINES = [
    (5, "Export: entries over 4 MB are dropped."),
    (30, "Export: the size-limit message is done."),
    (50, "决定：Export keeps the 4 MB cap."),
    (70, "Release: the 20th is at risk."),
    (100, "决定：the release moves a week unless the cache bug is fixed."),
    (130, "Load testing: QPS raised to 40K."),
    (190, "Cache bug: reproduced once on a weak network."),
    (200, "Bo负责 the regression test 周五前."),
    (220, "还没定: whether the cap is per entry."),
]


def segments(lines):
    return [{"start": at, "end": at + 9, "text": text} for at, text in lines]


def clock(sec):
    """A clock time as the hub's Markdown writes it: the recording's start in the local zone, plus `sec`."""
    return "[" + (datetime.fromisoformat(STARTED).astimezone() + timedelta(seconds=sec)).strftime("%H:%M:%S") + "] "


def view(overview):
    """What a client shows of an overview by the contract's "Clients" rules, in the shared fixture's terms
    (spec/fixtures/minutes-overview: texts as bold and plain segments, a bad `at`, `photo` or `who` None): None when it
    shows the legacy minutes."""
    if not isinstance(overview, dict) or not isinstance(overview.get("groups"), list):
        return None

    def words(x):
        return isinstance(x, dict) and isinstance(x.get("text"), str) and x["text"].strip() != ""

    def segments_of(x):
        text, strong, out, i = x["text"], x.get("strong"), [], 0
        for s in strong if isinstance(strong, list) else []:
            j = text.find(s, i) if isinstance(s, str) and s else -1
            if j < 0:
                continue
            out += ([{"text": text[i:j], "bold": False}] if j > i else []) + [{"text": s, "bold": True}]
            i = j + len(s)
        return out + ([{"text": text[i:], "bold": False}] if i < len(text) else [])

    def whole(v):
        """A positive whole number however JSON wrote it (2 or 2.0); never a boolean."""
        return type(v) in (int, float) and math.isfinite(v) and v > 0 and v == int(v)

    def cited(x):
        at, photo = x.get("at"), x.get("photo")
        return {"segments": segments_of(x), "at": at if type(at) in (int, float) and at >= 0 else None,
                "photo": photo if whole(photo) else None}

    groups = []
    for g in overview["groups"]:
        if not isinstance(g, dict) or not isinstance(g.get("topics"), list):
            continue
        topics = []
        for t in g["topics"]:
            if not isinstance(t, dict) or not isinstance(t.get("bullets"), list) or not isinstance(t.get("title"), str) \
                    or not t["title"].strip():
                continue
            bullets = [dict(cited(b), who=b["who"] if isinstance(b.get("who"), str) else None, decision=b.get("decision") is True)
                       for b in t["bullets"] if words(b)]
            if bullets:
                topics.append({"title": t["title"], "icon": t.get("icon") if t.get("icon") in ICONS else "general", "bullets": bullets})
        if topics:
            groups.append({"title": g["title"] if isinstance(g.get("title"), str) else "", "topics": topics})
    if not groups:
        return None
    headline, conclusions = overview.get("headline"), overview.get("conclusions")
    return {"headline": headline if isinstance(headline, str) and headline.strip() else None,
            "conclusions": [cited(c) for c in conclusions if words(c)] if isinstance(conclusions, list) else [],
            "groups": groups}


def bold_places(text, strong):
    """Where the contract's rule bolds `strong` in text: each entry's first occurrence at or after the last one's end."""
    out, i = [], 0
    for s in strong:
        j = text.find(s, i)
        if j >= 0:
            out.append((j, j + len(s)))
            i = j + len(s)
    return out


class RecordingMinutesOverviewMockTests(Hub, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({"MOCK_RECORDING_MINUTES": "ready", "MOCK_RECORDING_MINUTES_SHAPE": "legacy"})
        cls.addClassCleanup(stop)

    def shape(self, shape):
        status, out = self.call("POST", "/mock/recording_minutes", {"mode": "ready", "shape": shape})
        self.assertEqual((status, out), (200, {"mode": "ready", "shape": shape}))

    def meeting(self, lines=LINES, **fields):
        rid = str(uuid.uuid4())
        body = {"complete": True, "duration": 240, "calendar": CALENDAR, "segments": segments(lines), **fields}
        self.assertEqual(self.upload(rid, **body)[1]["status"], "stored")
        return rid

    def minutes(self, rid):
        status, out = self.call("GET", f"/api/v1/kb/recordings/{rid}")
        self.assertEqual(status, 200, out)
        return out["detail"]["minutes"]

    def assertUsable(self, m):
        """The hub's guarantees for an overview ("The `overview` field")."""
        ov = m["overview"]
        groups = ov["groups"]
        self.assertGreaterEqual(len(groups), 1)
        self.assertTrue(all(g["topics"] and all(t["bullets"] for t in g["topics"]) for g in groups), ov)
        # One untitled group, or 2 to 4 with distinct titles of at most 24 characters.
        titles = [g["title"] for g in groups]
        if len(groups) == 1:
            self.assertEqual(titles, [""])
        else:
            self.assertTrue(2 <= len(groups) <= 4 and len({t.casefold() for t in titles}) == len(titles) and all(0 < len(t) <= 24 for t in titles), titles)
        topics = [t for g in groups for t in g["topics"]]
        self.assertLessEqual(len(topics), 12)
        self.assertEqual(len({t["title"].casefold() for t in topics}), len(topics))
        self.assertTrue(all(0 < len(t["title"]) <= 40 and t["icon"] in ICONS for t in topics), topics)
        # The bullets are the points and the decisions, one to one, with the same text, at, photo and who.
        bullets = [b for t in topics for b in t["bullets"]]
        want = [("point", x["text"], x["at"], x.get("photo"), x.get("who")) for x in m.get("points", [])]
        want += [("decision", x["text"], x["at"], x.get("photo"), None) for x in m.get("decisions", [])]
        got = [("decision" if b.get("decision") is True else "point", b["text"], b["at"], b.get("photo"), b.get("who")) for b in bullets]
        self.assertEqual(sorted(got, key=repr), sorted(want, key=repr))
        self.assertTrue(all("decision" not in b or b["decision"] is True for b in bullets), bullets)
        # Order: bullets by time, topics by their first bullet, groups by their first topic.
        for t in topics:
            self.assertEqual([b["at"] for b in t["bullets"]], sorted(b["at"] for b in t["bullets"]), t)
        firsts = [t["bullets"][0]["at"] for t in topics]
        self.assertEqual(firsts, sorted(firsts))
        # No Markdown; bold only where the rule finds it, never the whole text, at most 2 entries of 1 to 20 characters.
        conclusions = ov.get("conclusions", [])
        self.assertLessEqual(len(conclusions), 4)
        for x in bullets + conclusions:
            self.assertNotIn("**", x["text"])
            strong = x.get("strong", [])
            self.assertTrue(len(strong) <= 2 and all(1 <= len(s) <= 20 and s != x["text"] for s in strong), x)
            self.assertEqual(len(bold_places(x["text"], strong)), len(strong), x)
            self.assertIsInstance(x["at"], (int, float))
        self.assertTrue(all(len(b["text"]) <= 400 for b in bullets) and all(len(c["text"]) <= 200 for c in conclusions))
        # A conclusion cites a photo only when a bullet cites it.
        cited = {b["photo"] for b in bullets if "photo" in b}
        self.assertTrue(all(c["photo"] in cited for c in conclusions if "photo" in c), conclusions)
        if "headline" in ov:
            self.assertTrue(0 < len(ov["headline"]) <= 120 and "\n" not in ov["headline"] and ov["headline"] != m.get("title"), ov)
        # A client keeps all of it: nothing in it is cleaned away.
        seen = view(ov)
        self.assertEqual((seen["headline"], len(seen["conclusions"])), (ov.get("headline"), len(conclusions)))
        self.assertEqual(
            [(g["title"], [(t["title"], t["icon"], [("".join(s["text"] for s in b["segments"]), b["at"], b["photo"], b["decision"])
                                                   for b in t["bullets"]]) for t in g["topics"]]) for g in seen["groups"]],
            [(g["title"], [(t["title"], t["icon"], [(b["text"], b["at"], b.get("photo"), b.get("decision", False))
                                                   for b in t["bullets"]]) for t in g["topics"]]) for g in groups])
        self.assertNotIn("- [ ]", m["markdown"])

    def test_the_shape_switch(self):
        self.shape("legacy")
        rid = self.meeting()
        status, out = self.call("POST", "/mock/recording_minutes", {"mode": "ready", "shape": "summary"})
        self.assertEqual((status, "shape" in out["error"]), (400, True))
        self.assertEqual(self.call("POST", "/mock/recording_minutes", [1])[0], 400)
        self.assertNotIn("overview", self.minutes(rid), "a refused switch changes nothing: the recording stays")
        self.shape("overview")
        self.assertIn("overview", self.minutes(self.meeting()))
        # A fresh start without `shape` goes back to MOCK_RECORDING_MINUTES_SHAPE's.
        self.assertEqual(self.call("POST", "/mock/recording_minutes", {"mode": "ready"})[1], {"mode": "ready", "shape": "legacy"})
        self.assertNotIn("overview", self.minutes(self.meeting()))

    def test_the_variable_starts_the_shape(self):
        base, stop = start({"MOCK_RECORDING_MINUTES": "ready", "MOCK_RECORDING_MINUTES_SHAPE": "overview"})
        try:
            hub = Hub()
            hub.base = base
            self.assertEqual(hub.call("POST", "/mock/recording_minutes", {"mode": "ready"})[1]["shape"], "overview")
        finally:
            stop()
        with self.assertRaises(RuntimeError):
            start({"MOCK_RECORDING_MINUTES_SHAPE": "cards"})[1]()

    def test_legacy_has_no_overview_and_the_hubs_markdown(self):
        self.shape("legacy")
        m = self.minutes(self.meeting())
        self.assertNotIn("overview", m)
        self.assertEqual(m["language"], "en")
        self.assertEqual([x["text"] for x in m["points"]], [LINES[0][1], LINES[1][1]])
        self.assertEqual(m["markdown"], "\n".join([
            "# Sprint review", "", "- When: " + m["when"], "- Location: Room 3", "- Attendees: Ada, Bo", "",
            "## Key points", "", "- " + clock(5) + LINES[0][1], "- " + clock(30) + LINES[1][1], "",
            "## Decisions", "", "- " + clock(50) + LINES[2][1], "- " + clock(100) + LINES[4][1], "",
            "## Action items", "", "- " + clock(200) + "Bo: " + LINES[7][1] + " (周五前)", "",
            "## Open questions", "", "- " + clock(220) + LINES[8][1]]))

    def test_overview_present_means_usable(self):
        self.shape("overview")
        m = self.minutes(self.meeting())
        self.assertUsable(m)
        ov = m["overview"]
        self.assertEqual(ov["headline"], LINES[0][1])
        self.assertEqual(ov["conclusions"], [{"text": "Export keeps the 4 MB cap.", "strong": ["4 MB"], "at": 50},
                                             {"text": "the release moves a week unless the cache bug is fixed.", "at": 100}])
        self.assertEqual([(g["title"], [(t["title"], t["icon"], len(t["bullets"])) for t in g["topics"]]) for g in ov["groups"]], [
            ("First half", [("Export", "progress", 3), ("Release", "schedule", 2)]),
            ("Second half", [("Load testing", "release", 1), ("Cache bug", "performance", 1)])])
        self.assertEqual(ov["groups"][0]["topics"][0]["bullets"], [
            {"text": LINES[0][1], "strong": ["4 MB"], "at": 5},
            {"text": LINES[1][1], "strong": ["done"], "at": 30},
            {"text": "Export keeps the 4 MB cap.", "strong": ["4 MB"], "at": 50, "decision": True}])
        # Every sentence that decides, assigns and leaves nothing open is a point; actions and open questions stay.
        self.assertEqual([x["at"] for x in m["points"]], [5, 30, 70, 130, 190])
        self.assertEqual(([a["id"] for a in m["actions"]], [x["at"] for x in m["open"]]), (["a1"], [220]))

    def test_the_overviews_markdown(self):
        self.shape("overview")
        m = self.minutes(self.meeting())
        self.assertEqual(m["markdown"], "\n".join([
            "# Sprint review", "", "- When: " + m["when"], "- Location: Room 3", "- Attendees: Ada, Bo", "",
            "Export: entries over 4 MB are dropped.", "",
            "## Core conclusions", "", "- " + clock(50) + "Export keeps the **4 MB** cap.",
            "- " + clock(100) + "the release moves a week unless the cache bug is fixed.", "",
            "## First half", "", "### Export", "",
            "- " + clock(5) + "Export: entries over **4 MB** are dropped.",
            "- " + clock(30) + "Export: the size-limit message is **done**.",
            "- " + clock(50) + "Decided: Export keeps the **4 MB** cap.", "",
            "### Release", "", "- " + clock(70) + "Release: the **20**th is at risk.",
            "- " + clock(100) + "Decided: the release moves a week unless the cache bug is fixed.", "",
            "## Second half", "", "### Load testing", "", "- " + clock(130) + "Load testing: QPS raised to **40K**.", "",
            "### Cache bug", "", "- " + clock(190) + "Cache bug: reproduced once on a weak network.", "",
            "## Action items", "", "- " + clock(200) + "Bo: " + LINES[7][1] + " (周五前)", "",
            "## Open questions", "", "- " + clock(220) + LINES[8][1], "",
            "*AI-generated from the recording. Check anything important.*"]))

    def test_one_group_and_a_topic_to_fold(self):
        self.shape("overview")
        m = self.minutes(self.meeting([(5, "Export: the cap stays at 4 MB."), (65, "Release: the date is the 20th."), (125, "Cache bug: fixed.")]))
        self.assertUsable(m)
        ov = m["overview"]
        # Under four topics there is one untitled group, whose topics are the Markdown's sections.
        self.assertEqual([(g["title"], [t["title"] for t in g["topics"]]) for g in ov["groups"]], [("", ["Export", "Release", "Cache bug"])])
        self.assertNotIn("conclusions", ov)
        self.assertIn("\n\n## Export\n\n- " + clock(5) + "Export: the cap stays at **4 MB**.\n\n## Release\n\n- ", m["markdown"])
        self.assertNotIn("###", m["markdown"])
        # Six lines of one minute without a subject of their own are one topic of six bullets (a client folds it after 4).
        m = self.minutes(self.meeting([(2 + 9 * i, f"The export step {i} is done.") for i in range(6)]))
        self.assertUsable(m)
        self.assertEqual([(t["title"], len(t["bullets"])) for g in m["overview"]["groups"] for t in g["topics"]],
                         [("The export step 0 is done", 6)])

    def test_at_most_twelve_topics(self):
        self.shape("overview")
        lines = [(60 * i + 3, f"Subject {i}: item {i} moved.") for i in range(20)]
        m = self.minutes(self.meeting(lines, duration=1300))
        self.assertUsable(m)
        topics = [t for g in m["overview"]["groups"] for t in g["topics"]]
        self.assertEqual(len(topics), 10)  # two transcript minutes each
        self.assertEqual([len(t["bullets"]) for t in topics], [2] * 10)

    def test_no_point_or_decision_no_overview(self):
        self.shape("overview")
        m = self.minutes(self.meeting([(5, "张三负责发版说明。"), (20, "还没定的是发布日期。")]))  # i18n-ignore: the mock's keywords
        self.assertNotIn("overview", m)
        self.assertIn("## Action items", m["markdown"])
        self.assertNotIn("AI-generated", m["markdown"])

    def test_malformed_falls_back_with_the_legacy_minutes(self):
        self.shape("legacy")
        legacy = self.minutes(self.meeting())
        self.shape("malformed")
        m = self.minutes(self.meeting())
        ov = m.pop("overview")
        self.assertEqual(m, legacy)
        self.assertIsInstance(ov["groups"], list)
        self.assertTrue(any(not isinstance(g, dict) for g in ov["groups"]))
        self.assertIsNone(view(ov), "a client keeps something of the malformed overview")

    def test_todos_and_stale_in_every_shape(self):
        for shape in SHAPES:
            with self.subTest(shape=shape):
                self.shape(shape)
                rid = self.meeting([(5, "Export: the cap stays."), (20, "张三负责补测试。"), (40, "李四负责发版说明。")])  # i18n-ignore: the mock's keywords
                m = self.minutes(rid)
                self.assertEqual(("overview" in m, m["version"], [a["id"] for a in m["actions"]]), (shape != "legacy", 1, ["a1", "a2"]))
                path = f"/api/v1/recordings/{rid}/minutes/todos"
                status, out = self.call("POST", path, {"version": 1, "actions": ["a1"]})
                self.assertEqual((status, len(out["todos"]), out["todos"][0]["title"]), (200, 1, "张三: 张三负责补测试"))  # i18n-ignore
                self.assertEqual(out["minutes"]["actions"][0]["todo_id"], out["todos"][0]["id"])
                self.assertEqual(out["minutes"]["markdown"], m["markdown"], "linking a to-do keeps the Markdown")
                self.assertEqual(self.call("POST", path, {"version": 1, "actions": ["a1", "a2"]})[1]["todos"][0]["title"], "李四: 李四负责发版说明")  # i18n-ignore
                # The next version keeps the shape; the old one is stale.
                self.assertEqual(self.call("POST", "/mock/recording_minutes_rewrite", {"id": rid})[1]["version"], 2)
                again = self.minutes(rid)
                self.assertEqual(("overview" in again, again["markdown"]), ("overview" in m, m["markdown"]))
                status, out = self.call("POST", path, {"version": 1, "actions": ["a1"]})
                self.assertEqual((status, out.get("code")), (409, "minutes_stale"))
                if shape == "overview":
                    self.assertUsable(again)

    def test_items_without_words_are_dropped(self):
        """A line that is only marks, punctuation or spaces makes no point, so no empty bullet or untitled topic: the
        bullets stay the points and decisions one to one, as the hub drops an item left without words."""
        self.shape("overview")
        lines = [(5, "**"), (10, "。"), (15, "   "), (20, "**。**"), (65, "Export: the cap stays at **4 MB**."), (70, "** **"),
                 (125, "Release: the date is the 20th.")]
        m = self.minutes(self.meeting(lines))
        self.assertUsable(m)
        self.assertEqual([(x["text"], x["at"]) for x in m["points"]],
                         [("Export: the cap stays at 4 MB.", 65), ("Release: the date is the 20th.", 125)])
        self.assertEqual([t["title"] for g in m["overview"]["groups"] for t in g["topics"]], ["Export", "Release"])
        self.shape("legacy")
        m = self.minutes(self.meeting([(5, "。"), (10, "   "), (65, "Export: the cap stays."), (70, "Release: the 20th.")]))
        self.assertEqual([x["text"] for x in m["points"]], ["Export: the cap stays.", "Release: the 20th."])

    def test_texts_at_most_400(self):
        """An item's text is at most 400 characters, the "…" of a cut counted: 399 and 400 stay whole, 401 keeps 399 and
        "…"; a figure the cut ends at is not bold."""
        self.shape("overview")
        x = lambda n: "x" * n
        m = self.minutes(self.meeting([(5, x(399)), (65, x(400)), (125, x(401)), (185, x(397) + " **40K**")]))
        self.assertUsable(m)
        self.assertEqual([x["text"] for x in m["points"]], [x(399), x(400), x(399) + "…", x(397) + " 4…"])
        bullets = [b for g in m["overview"]["groups"] for t in g["topics"] for b in t["bullets"]]
        self.assertEqual([("strong" in b, len(b["text"])) for b in bullets], [(False, 399), (False, 400), (False, 400), (False, 400)])

    def test_bullets_cite_photos(self):
        self.shape("overview")
        self.call("POST", "/mock/meeting_photos", {"note": "ok", "note_seconds": 0.2, "on": True})
        rid = str(uuid.uuid4())
        lines = segments(LINES[:3])
        self.upload(rid, segments=lines, calendar=CALENDAR)
        self.assertEqual(self.call("POST", f"/api/v1/recordings/{rid}/photos", photo(at=150, text="Whiteboard: 4 MB cap\nOwner: Bo"))[0], 200)
        self.upload(rid, segments=lines, calendar=CALENDAR, ended_at="2026-11-10T14:05:00+08:00", complete=True)
        m = self.minutes(rid)
        self.assertUsable(m)
        topics = [t for g in m["overview"]["groups"] for t in g["topics"]]
        self.assertEqual(topics[-1], {"title": "Whiteboard", "icon": "schedule", "bullets": [
            {"text": "Whiteboard: 4 MB cap", "strong": ["4 MB"], "at": 150, "photo": 1}]})
        self.assertIn("- " + clock(150) + "Whiteboard: **4 MB** cap (photo 1)\n", m["markdown"])
        self.assertEqual([x["cited"] for x in self.call("GET", f"/api/v1/recordings/{rid}/photos")[1]["photos"]], [True])
        # Deleting it takes its bullet and its point out, and the topic left without bullets.
        self.assertEqual(self.call("DELETE", f"/api/v1/recordings/{rid}/photos/1"), (200, {"deleted": True}))
        m = self.minutes(rid)
        self.assertUsable(m)
        self.assertEqual([t["title"] for g in m["overview"]["groups"] for t in g["topics"]], ["Export"])
        self.assertNotIn("photo", m["markdown"])


class MinutesOverviewRulesTests(unittest.TestCase):
    """The mock's own rules, on its functions: bold, the Markdown's escapes, the photos the minutes cite."""

    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("minutes_overview_mock", HERE / "mock_hub.py")
        cls.hub = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.hub)

    def test_clip_and_bold_at_the_cut(self):
        clip, strong = self.hub.minutes_clip, self.hub.minutes_strong
        self.assertEqual([len(clip("x" * n, 400)) for n in (399, 400, 401, 900)], [399, 400, 400, 400])
        self.assertEqual(clip("abcdef", 4), "abc…")
        cut = clip("x" * 395 + " 40K more", 400)
        self.assertEqual((cut, strong(cut, 400), strong(cut)), ("x" * 395 + " 40K…", [], ["40K"]))
        self.assertEqual(strong(clip("x" * 390 + " 40K more", 400), 400), ["40K"])

    def test_bold_follows_the_contracts_rule(self):
        bold = self.hub.minutes_bold
        self.assertEqual(bold("from 20K to 40K", ["20K", "40K"]), "from **20K** to **40K**")
        # Each entry after the one before: the second 4 is the one bolded, an entry not found bolds nothing.
        self.assertEqual(bold("4 of 4 done", ["of", "4", "nope"]), "4 **of** **4** done")
        self.assertEqual(bold("a * b", ["b"]), "a \\* **b**")
        strong = self.hub.minutes_strong
        self.assertEqual((strong("QPS raised to 40K"), strong("v2 has 2 bugs"), strong("完成"), strong("abandoned plan done")),
                         (["40K"], ["2"], [], []))  # "done" is first found inside "abandoned": not where it was marked

    def test_the_shared_fixture(self):
        """spec/fixtures/minutes-overview: what a client keeps of each case (view, which checks the mock's overviews and
        its malformed one), and the hub's Markdown of each case the hub writes, which minutes_markdown writes the same."""
        cases = json.loads(FIXTURE.read_text(encoding="utf-8"))["cases"]
        self.assertGreaterEqual(len(cases), 10)
        zone = os.environ.get("TZ")
        os.environ["TZ"] = "Asia/Shanghai"  # the fixture's clock times are the minutes' +08:00
        time.tzset()
        try:
            for c in cases:
                with self.subTest(case=c["name"]):
                    m, want = c["input"]["minutes"], c["expected"]
                    self.assertEqual(view(m.get("overview")), want["overview"])
                    self.assertEqual(want["layout"], "legacy" if want["overview"] is None else "overview")
                    if want["markdown"] is not None:
                        ov = m["overview"] if want["layout"] == "overview" else None
                        got = self.hub.minutes_markdown(m, {"started_at": m["start"]}, m.get("language") or "zh-Hans", ov)
                        self.assertEqual(got, want["markdown"])
        finally:
            if zone is None:
                os.environ.pop("TZ", None)
            else:
                os.environ["TZ"] = zone
            time.tzset()

    def test_photos_cited_reads_the_summary_card(self):
        rid = "r1"
        self.hub.KB_DETAIL[("recordings", rid)] = {"minutes": {"points": [{"text": "a", "at": 1, "photo": 2}], "overview": {
            "conclusions": [{"text": "c", "at": 1, "photo": 3}, "x"],
            "groups": [{"title": "", "topics": [{"title": "t", "bullets": [{"text": "b", "at": 1, "photo": 4}, 5]}, "x"]}, None]}}}
        self.assertEqual(self.hub.photos_cited(rid), {2, 3, 4})
        self.hub.KB_DETAIL[("recordings", rid)]["minutes"]["overview"] = self.hub.MINUTES_OVERVIEW_MALFORMED
        self.assertEqual(self.hub.photos_cited(rid), {2})


if __name__ == "__main__":
    unittest.main()
