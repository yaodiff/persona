"""The mock hub keeps recordings and meeting minutes as the hub does (#3780, docs/assistant/meeting-minutes.md "Privacy"):
GET / PUT /api/v1/assistant `recording_audio_days` (old name `recording_days`), `recording_text_days` and `minutes_days`
(0 means forever; finite minutes retention is never shorter than the text's, #16412), and a trimmed recording (POST /mock/recording_trim) that keeps its minutes, takes no upload and makes no
minutes again."""
import time
import unittest
import uuid

from test_meeting_attendance_mock import Hub, start


class RecordingRetentionMockTests(Hub, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({"MOCK_RECORDING_MINUTES": "ready"})
        cls.addClassCleanup(stop)

    def test_retention_settings(self):
        status, state = self.call("GET", "/api/v1/assistant")
        self.assertEqual((status, state["recording_days"], state["recording_audio_days"], state["recording_text_days"], state["minutes_days"]),
                         (200, 14, 14, 0, 0))
        for body in ({"minutes_days": -1}, {"minutes_days": 3651}, {"minutes_days": "30"}, {"minutes_days": True},
                     {"minutes_days": 1.5}, {"minutes_days": None}, {"recording_days": 0}, {"recording_days": 3651},
                     {"recording_text_days": -1}, {"recording_audio_days": 3651}, {"recording_days": 30, "minutes_days": -1}):
            self.assertEqual(self.call("PUT", "/api/v1/assistant", body)[0], 400, body)
        self.assertEqual(self.call("GET", "/api/v1/assistant")[1]["recording_days"], 14)  # a refused request changes nothing
        # The text kept forever (the default) keeps the minutes forever too.
        self.assertEqual(self.call("PUT", "/api/v1/assistant", {"minutes_days": 30})[1]["minutes_days"], 0)
        state = self.call("PUT", "/api/v1/assistant", {"recording_text_days": 60})[1]
        self.assertEqual((state["recording_text_days"], state["minutes_days"]), (60, 60))
        # The old name sets the audio's days: the phone's window.
        state = self.call("PUT", "/api/v1/assistant", {"recording_days": 1000})[1]
        self.assertEqual((state["recording_days"], state["recording_audio_days"], state["recording_text_days"]), (1000, 1000, 60))
        listed = self.call("GET", "/api/v1/recordings")[1]
        self.assertEqual((listed["days"], listed["audio_days"], listed["text_days"]), (365, 1000, 60))  # phones take 1–365
        state = self.call("PUT", "/api/v1/assistant", {"recording_audio_days": 0, "recording_text_days": 14, "minutes_days": 365})[1]
        self.assertEqual((state["recording_audio_days"], state["recording_text_days"], state["minutes_days"]), (0, 14, 365))
        listed = self.call("GET", "/api/v1/recordings")[1]
        self.assertEqual((listed["days"], listed["audio_days"]), (365, 0))
        state = self.call("PUT", "/api/v1/assistant", {"recording_audio_days": 14, "recording_text_days": 0, "minutes_days": 0})[1]
        self.assertEqual((state["recording_days"], state["recording_text_days"], state["minutes_days"]), (14, 0, 0))
        # Another manager is one from before these settings: no field, so the web shows no Recordings group.
        status, other = self.call("GET", "/api/v1/assistant", headers={"X-Persona-Manager": "m-3fa21c"})
        self.assertEqual((status, other["manager"]), (200, "m-3fa21c"))
        for key in ("minutes_days", "recording_days", "recording_audio_days", "recording_text_days"):
            self.assertNotIn(key, other)

    def test_trimmed_recording(self):
        rid = str(uuid.uuid4())
        segments = [{"start": 0, "end": 9, "text": "Keep the cap"}, {"start": 10, "end": 19, "text": "Bo writes the test"}]
        self.assertEqual(self.upload(rid, complete=True, segments=segments)[1]["status"], "stored")
        self.assertEqual(self.call("POST", "/mock/recording_trim", {"id": str(uuid.uuid4())})[0], 404)
        status, out = self.call("POST", "/mock/recording_trim", {"id": rid.upper()})
        self.assertEqual(status, 200)
        trimmed_at = out["trimmed_at"]
        detail = self.call("GET", "/api/v1/kb/recordings/" + rid)[1]["detail"]
        self.assertEqual((detail["meta"]["trimmed_at"], detail["meta"]["minutes_state"], detail["summary"]), (trimmed_at, "ready", ""))
        self.assertNotIn("segments", detail)
        self.assertTrue(detail["minutes"]["points"])
        status, refused = self.call("POST", f"/api/v1/recordings/{rid}/minutes")
        self.assertEqual((status, refused["code"]), (409, "transcript_expired"))
        self.assertEqual(self.upload(rid, revision=int(time.time() * 1000) + 1, complete=True, segments=segments)[1]["status"], "expired")
        listed = {r["id"]: r for r in self.call("GET", "/api/v1/recordings")[1]["recordings"]}
        self.assertEqual(listed[rid].get("trimmed_at"), trimmed_at)
        self.assertIn(rid, self.call("GET", "/mock/recordings")[1]["trimmed"])
        # The user's delete still removes it.
        self.assertEqual(self.call("DELETE", f"/api/v1/recordings/{rid}")[1], {"deleted": True})
        self.assertNotIn(rid, self.call("GET", "/mock/recordings")[1]["trimmed"])


if __name__ == "__main__":
    unittest.main()
