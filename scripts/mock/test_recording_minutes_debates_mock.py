#!/usr/bin/env python3
"""Mock hub domain tests for disagreements in meeting minutes (#16728).

Checks:
- POST/GET /mock/recording_minutes_debates ("off", "on", "malformed") and /mock/reset, with no MOCK_* switch;
- summary-card ("overview") and legacy shapes with debates on/off/malformed;
- shared fixture spec/fixtures/minutes-debates/cases.json: client cleaning rules and hub Markdown byte for byte.
"""
import importlib.util
import json
import os
from pathlib import Path
import sys
import time
import unittest
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import recording_minutes_debates_mock as debates_mod  # noqa: E402
from test_meeting_attendance_mock import Hub, start  # noqa: E402
from test_recording_minutes_overview_mock import CALENDAR, LINES, clock, segments  # noqa: E402

FIXTURE = HERE.parents[1] / "spec" / "fixtures" / "minutes-debates" / "cases.json"


class RecordingMinutesDebatesMockTests(Hub, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({"MOCK_RECORDING_MINUTES": "ready", "MOCK_RECORDING_MINUTES_SHAPE": "overview"})
        cls.addClassCleanup(stop)

    def tearDown(self):
        self.call("POST", "/mock/recording_minutes_debates", {"mode": "off"})

    def set_shape(self, shape):
        status, out = self.call("POST", "/mock/recording_minutes", {"mode": "ready", "shape": shape})
        self.assertEqual((status, out), (200, {"mode": "ready", "shape": shape}))

    def set_debates(self, mode):
        status, out = self.call("POST", "/mock/recording_minutes_debates", {"mode": mode})
        self.assertEqual((status, out), (200, {"mode": mode}))

    def meeting(self, lines=LINES, **fields):
        rid = str(uuid.uuid4())
        body = {"complete": True, "duration": 240, "calendar": CALENDAR, "segments": segments(lines), **fields}
        self.assertEqual(self.upload(rid, **body)[1]["status"], "stored")
        return rid

    def minutes(self, rid):
        status, out = self.call("GET", f"/api/v1/kb/recordings/{rid}")
        self.assertEqual(status, 200, out)
        return out["detail"]["minutes"]

    def test_control_route_validation_and_reset(self):
        self.assertEqual(self.call("GET", "/mock/recording_minutes_debates"), (200, {"mode": "off"}))
        self.assertEqual(self.call("POST", "/mock/recording_minutes_debates", {"mode": "invalid"})[0], 400)
        self.assertEqual(self.call("POST", "/mock/recording_minutes_debates", ["not-dict"])[0], 400)
        self.set_debates("on")
        self.assertEqual(self.call("GET", "/mock/recording_minutes_debates"), (200, {"mode": "on"}))
        self.assertEqual(self.call("GET", "/mock/reset")[0], 200)
        self.assertEqual(self.call("GET", "/mock/recording_minutes_debates"), (200, {"mode": "off"}))

    def test_overview_shape_on_off_and_malformed(self):
        self.set_shape("overview")
        self.set_debates("off")
        plain = self.minutes(self.meeting())
        self.assertNotIn("debates", plain)
        self.assertNotIn("## Disagreements", plain["markdown"])

        self.set_debates("on")
        m = self.minutes(self.meeting())
        self.assertEqual(len(m["debates"]), 2)
        d0, d1 = m["debates"]
        self.assertEqual((d0["status"], d0["at"], d0["topic"]), ("settled", 5, "Export"))
        self.assertEqual(d0["outcome"]["decision"], 0)
        self.assertEqual(d0["outcome"]["at"], m["decisions"][0]["at"])
        self.assertEqual([p["at"] for p in d0["positions"]], [5, 30])
        self.assertEqual((d1["status"], d1["at"]), ("open", 50))
        self.assertNotIn("outcome", d1)
        # Disagreements sits after Core conclusions and before the first group heading.
        md = m["markdown"]
        self.assertLess(md.index("## Core conclusions"), md.index("## Disagreements"))
        self.assertLess(md.index("## Disagreements"), md.index("## First half"))
        self.assertIn("- " + clock(5) + d0["positions"][0]["stance"] + " (Ada)", md)
        self.assertIn("- " + clock(50) + "Outcome: " + d0["outcome"]["text"], md)

        # Malformed attaches a block that cleans down to [] while leaving Markdown identical to plain.
        self.set_debates("malformed")
        bad = self.minutes(self.meeting())
        raw_debates = bad.pop("debates")
        self.assertEqual(debates_mod.clean_debates(raw_debates, len(bad.get("decisions") or [])), [])
        self.assertEqual(bad, plain)

    def test_legacy_shape_on_and_zh_hans(self):
        self.set_shape("legacy")
        self.set_debates("on")
        m = self.minutes(self.meeting())
        self.assertEqual(len(m["debates"]), 2)
        md = m["markdown"]
        # Legacy layout: Disagreements sits between Action items and Open questions.
        self.assertLess(md.index("## Action items"), md.index("## Disagreements"))
        self.assertLess(md.index("## Disagreements"), md.index("## Open questions"))

        # zh-Hans recording produces Chinese headings and status words.
        zh = self.minutes(self.meeting(language="zh-CN"))
        self.assertIn("## 分歧\n", zh["markdown"])  # i18n-ignore: fixture words
        self.assertIn("（已谈拢）\n", zh["markdown"])  # i18n-ignore: fixture words
        self.assertIn("（仍有分歧）\n", zh["markdown"])  # i18n-ignore: fixture words
        self.assertLess(zh["markdown"].index("## 待办"), zh["markdown"].index("## 分歧"))  # i18n-ignore: fixture words
        self.assertLess(zh["markdown"].index("## 分歧"), zh["markdown"].index("## 未决问题"))  # i18n-ignore: fixture words


class MinutesDebatesFixtureTests(unittest.TestCase):
    """Verifies spec/fixtures/minutes-debates/cases.json against clean_debates and wrapped minutes_markdown."""

    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("minutes_debates_mock_hub", HERE / "mock_hub.py")
        cls.hub = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.hub)

    def test_shared_fixture(self):
        cases = json.loads(FIXTURE.read_text(encoding="utf-8"))["cases"]
        self.assertGreaterEqual(len(cases), 12)
        zone = os.environ.get("TZ")
        os.environ["TZ"] = "Asia/Shanghai"
        time.tzset()
        try:
            for c in cases:
                with self.subTest(case=c["name"]):
                    m, want = c["input"]["minutes"], c["expected"]
                    decs = m.get("decisions") or []
                    self.assertEqual(debates_mod.clean_debates(m.get("debates"), len(decs)), want["debates"])
                    if want["markdown"] is not None:
                        ov = m.get("overview") if isinstance(m.get("overview"), dict) else None
                        got = self.hub.minutes_markdown(m, {"started_at": m["start"]}, m.get("language") or "zh-Hans", ov)
                        self.assertEqual(got, want["markdown"])
        finally:
            if zone is None:
                os.environ.pop("TZ", None)
            else:
                os.environ["TZ"] = zone
            time.tzset()


if __name__ == "__main__":
    unittest.main()
