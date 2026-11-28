#!/usr/bin/env python3
"""Mock hub domain tests for meeting speakers (#17674).

Checks:
- feature flag `meeting_speakers` in /api/v1/health with no MOCK_* switch;
- all 5 pre-seeded synthetic sample meetings (ready, waiting, low quality, failed, trimmed);
- POST /api/v1/recordings/{id}/speakers: all 4 ops, rev / 409 speakers_stale, 409 speakers_cleared,
  auto-merge, unmerge returning unnamed, minor recomputation, undo round-trips, detail updates;
- replays every `edit` case from spec/fixtures/speakers-names/cases.json over real HTTP.
"""
import json
from pathlib import Path
import sys
import unittest
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import recording_speakers_mock as speakers_mod  # noqa: E402
from test_meeting_attendance_mock import Hub, start  # noqa: E402

FIXTURE = HERE.parents[1] / "spec" / "fixtures" / "speakers-names" / "cases.json"


def _strip_person_view_fields(people):
    """Strip computed detail fields (seconds, lines, samples) to compare stored roster shape against fixtures."""
    out = []
    for p in people:
        row = {"id": p["id"]}
        if p.get("name"):
            row["name"] = p["name"]
        if p.get("me"):
            row["me"] = True
        if p.get("merged_into"):
            row["merged_into"] = p["merged_into"]
        if p.get("minor"):
            row["minor"] = True
        out.append(row)
    return out


class RecordingSpeakersMockTests(Hub, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({})
        cls.addClassCleanup(stop)

    def tearDown(self):
        self.call("GET", "/mock/reset")

    def detail(self, rid):
        status, out = self.call("GET", f"/api/v1/kb/recordings/{rid}")
        self.assertEqual(status, 200, out)
        return out["detail"]

    def test_feature_flag_and_samples(self):
        status, health = self.call("GET", "/api/v1/health")
        self.assertEqual(status, 200)
        self.assertIn("meeting_speakers", health["features"])

        # 1. Ready sample: named, unnamed, merged, minor, 2 debates with speaker/speaker_unsure/also, segments with speaker.
        ready = self.detail(speakers_mod.SAMPLE_READY)
        sp = ready["speakers"]
        self.assertEqual((sp["state"], sp["quality"], sp["source"], sp["trimmed"]), ("ready", "ok", "hub", False))
        by_id = {p["id"]: p for p in sp["people"]}
        self.assertEqual(by_id["s1"]["name"], "Ada")
        self.assertEqual(by_id["s1"]["seconds"], 320.0)
        self.assertEqual(by_id["s1"]["lines"], 4)
        self.assertTrue(by_id["s1"].get("samples"))
        self.assertEqual((by_id["s2"]["name"], by_id["s2"]["me"]), ("Grace", True))
        self.assertNotIn("name", by_id["s3"])
        self.assertEqual(by_id["s4"]["merged_into"], "s1")
        self.assertTrue(by_id["s5"]["minor"])
        self.assertEqual(len(ready["minutes"]["debates"]), 2)
        d0, d1 = ready["minutes"]["debates"]
        self.assertEqual(d0["positions"][0]["speaker"], "s1")
        self.assertEqual(d0["positions"][0]["also"][0]["speaker"], "s2")
        self.assertTrue(d1["positions"][1]["speaker_unsure"])
        self.assertTrue(any(sg.get("speaker") == "s1" and not sg.get("speaker_unsure") for sg in ready["segments"]))
        self.assertTrue(any(sg.get("speaker") == "s5" and sg.get("speaker_unsure") for sg in ready["segments"]))

        # 2. Waiting sample.
        waiting = self.detail(speakers_mod.SAMPLE_WAITING)["speakers"]
        self.assertEqual(waiting["state"], "waiting")
        self.assertEqual((waiting["done"], waiting["end"]), (180.0, 600.0))

        # 3. Low-quality sample: every labelled segment is speaker_unsure.
        low = self.detail(speakers_mod.SAMPLE_LOW_QUALITY)
        self.assertEqual((low["speakers"]["state"], low["speakers"]["quality"]), ("ready", "low"))
        self.assertTrue(all(sg.get("speaker") and sg.get("speaker_unsure") for sg in low["segments"]))

        # 4. Failed sample (too_long).
        failed = self.detail(speakers_mod.SAMPLE_FAILED)["speakers"]
        self.assertEqual((failed["state"], failed["reason"]), ("failed", "too_long"))

        # 5. Trimmed sample: spans empty, no segments, edit still works.
        trimmed = self.detail(speakers_mod.SAMPLE_TRIMMED)
        self.assertNotIn("segments", trimmed)
        self.assertTrue(trimmed["speakers"]["trimmed"])
        self.assertEqual(trimmed["speakers"]["spans"], [])
        st, edited = self.call("POST", f"/api/v1/recordings/{speakers_mod.SAMPLE_TRIMMED}/speakers", {
            "rev": trimmed["speakers"]["rev"],
            "ops": [{"op": "name", "id": "s3", "name": "Lin"}],
        })
        self.assertEqual(st, 200, edited)
        self.assertEqual({p["id"]: p.get("name") for p in edited["speakers"]["people"]}["s3"], "Lin")

    def test_edit_stale_clear_and_detail_sync(self):
        rid = speakers_mod.SAMPLE_READY
        rev = self.detail(rid)["speakers"]["rev"]

        # Stale revision -> 409 speakers_stale.
        st, err = self.call("POST", f"/api/v1/recordings/{rid}/speakers", {
            "rev": rev + 99,
            "ops": [{"op": "name", "id": "s3", "name": "Lin"}],
        })
        self.assertEqual((st, err.get("code")), (409, "speakers_stale"))

        # Unmerge s4 -> returns unnamed, detail updates, segments for s4 stop mapping to s1.
        st, out = self.call("POST", f"/api/v1/recordings/{rid}/speakers", {
            "rev": rev,
            "ops": [{"op": "unmerge", "id": "s4"}],
        })
        self.assertEqual(st, 200, out)
        self.assertEqual(out["speakers"]["rev"], rev + 1)
        self.assertEqual(out["undo"], [{"op": "merge", "from": "s4", "into": "s1"}])
        s4 = next(p for p in out["speakers"]["people"] if p["id"] == "s4")
        self.assertNotIn("name", s4)
        self.assertNotIn("merged_into", s4)
        refreshed = self.detail(rid)
        self.assertEqual(refreshed["speakers"]["rev"], rev + 1)
        self.assertTrue(any(sg.get("speaker") == "s4" for sg in refreshed["segments"]))

        # Clear empties list, sets state cleared, and subsequent non-clear edit returns 409 speakers_cleared.
        st, cleared = self.call("POST", f"/api/v1/recordings/{rid}/speakers", {
            "rev": rev + 1,
            "ops": [{"op": "clear"}],
        })
        self.assertEqual(st, 200, cleared)
        self.assertEqual(cleared["speakers"]["state"], "cleared")
        self.assertEqual(cleared["speakers"]["people"], [])
        self.assertEqual(cleared["speakers"]["spans"], [])
        self.assertEqual(cleared["undo"], [])
        self.assertTrue(all("speaker" not in sg for sg in self.detail(rid)["segments"]))

        st, err = self.call("POST", f"/api/v1/recordings/{rid}/speakers", {
            "rev": cleared["speakers"]["rev"],
            "ops": [{"op": "name", "id": "s1", "name": "Ada"}],
        })
        self.assertEqual((st, err.get("code")), (409, "speakers_cleared"))

    def test_shared_fixture_edit_cases(self):
        cases = [c for c in json.loads(FIXTURE.read_text(encoding="utf-8"))["cases"] if c.get("kind") == "edit"]
        self.assertGreaterEqual(len(cases), 10)
        for c in cases:
            with self.subTest(case=c["name"]):
                rid = str(uuid.uuid4())
                inp = c["input"]
                want = c["expected"]
                st, seeded = self.call("POST", "/mock/recording_speakers/seed", {
                    "id": rid,
                    "rev": 1,
                    "next": inp["next"],
                    "people": inp["people"],
                    "stats": inp["stats"],
                    "edits": inp.get("edits", {}),
                })
                self.assertEqual(st, 200, seeded)
                st, out = self.call("POST", f"/api/v1/recordings/{rid}/speakers", {
                    "rev": 1,
                    "ops": inp["ops"],
                })
                if "error" in want:
                    self.assertEqual(st, 400, f"expected 400 for {c['name']}, got {st}: {out}")
                    if want.get("error_code"):
                        self.assertEqual(out.get("code"), want["error_code"])
                    continue
                self.assertEqual(st, 200, f"unexpected error for {c['name']}: {out}")
                self.assertEqual(_strip_person_view_fields(out["speakers"]["people"]), want["people"])
                self.assertEqual(out["undo"], want["undo"])
                if want.get("verify_undo_roundtrip"):
                    st2, undone = self.call("POST", f"/api/v1/recordings/{rid}/speakers", {
                        "rev": out["speakers"]["rev"],
                        "ops": out["undo"],
                    })
                    self.assertEqual(st2, 200, f"undo failed for {c['name']}: {undone}")
                    self.assertEqual(_strip_person_view_fields(undone["speakers"]["people"]), inp["people"])


if __name__ == "__main__":
    unittest.main()
