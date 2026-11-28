package recording

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

const (
	speakerRecID      = "6f1c2a3b-0000-4000-8000-000000005101"
	speakerNoteRecID  = "6f1c2a3b-0000-4000-8000-000000005102"
	speakerDevRecID   = "6f1c2a3b-0000-4000-8000-000000005103"
	speakerTrackRecID = "6f1c2a3b-0000-4000-8000-000000005104"
)

func speakersPath(id string) string     { return "/api/v1/recordings/" + id + "/speakers" }
func speakerTurnsPath(id string) string { return "/api/v1/recordings/" + id + "/speakers/turns" }

// setupHubMeetingForSpeakers uploads a hub-transcribed meeting with a final turn-timed transcript so speaker
// timeline import and editing routes can exercise the full contract.
func setupHubMeetingForSpeakers(t *testing.T, m *meetingFixture, id string) {
	t.Helper()
	m.engine().TranscriptionReady = func() bool { return true }
	m.upload(id, 1, map[string]any{
		"title":         "Speaker Route Review",
		"duration":      120,
		"complete":      true,
		"transcription": "hub",
		"segments": []map[string]any{
			{"start": 0, "end": 35, "text": "Ada opened the sprint review."},
			{"start": 36, "end": 72, "text": "Grace walked through the speaker API."},
			{"start": 73, "end": 110, "text": "Ada closed with the release checklist."},
		},
	})
	if _, err := writersOf(m.engine()).HubTranscriptPutWithTiming(id, recordingwire.HubPassFinal, recordingwire.TimingTurn, []kbwire.RecordingSegment{
		{Start: 0, End: 35, Text: "Ada opened the sprint review."},
		{Start: 36, End: 72, Text: "Grace walked through the speaker API."},
		{Start: 73, End: 110, Text: "Ada closed with the release checklist."},
	}); err != nil {
		t.Fatalf("HubTranscriptPutWithTiming: %v", err)
	}
}

func doRawStatus(t *testing.T, m *meetingFixture, method, path, role string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(method, m.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	resp, err := m.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// Debug import switch off -> 404; switch on -> imports timeline, labels detail.segments ("No action needed"),
// enforces idempotency vs op_id_reused, rejects non-full credentials, read-only KB, non-meeting/device/track
// recordings, oversized body/turns, and emits meeting_live with changed: ["speakers"].
func TestSpeakerTurnsImportRoute(t *testing.T) {
	m := newMeetingFixture(t)
	setupHubMeetingForSpeakers(t, m, speakerRecID)

	importBody := map[string]any{
		"run":     "run-1",
		"engine":  "test-engine 1.0",
		"source":  "hub",
		"covered": [][]float64{{0, 120}},
		"turns":   [][]any{{0, 35, "c1"}, {36, 72, "c2"}, {73, 110, "c1"}},
	}
	rawImport, _ := json.Marshal(importBody)

	// Switch off by default -> 404.
	if got := doRawStatus(t, m, "PUT", speakerTurnsPath(speakerRecID), "", rawImport); got != http.StatusNotFound {
		t.Fatalf("import with switch off: got %d, want 404", got)
	}

	// Enable debug switch on the fake host.
	m.s.mu.Lock()
	m.s.speakerTurnsImport = true
	m.s.mu.Unlock()

	// Non-full credentials -> 403.
	m.do("PUT", speakerTurnsPath(speakerRecID), "answer", importBody, http.StatusForbidden)

	// Read-only KB -> 403 kb_read_only.
	m.s.mu.Lock()
	m.s.readOnly = true
	m.s.mu.Unlock()
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerRecID), "", importBody, http.StatusForbidden), "kb_read_only")
	m.s.mu.Lock()
	m.s.readOnly = false
	m.s.mu.Unlock()

	// Track recording -> 409 not_meeting.
	m.s.mu.Lock()
	m.s.inSpace = map[string]bool{speakerTrackRecID: true}
	m.s.mu.Unlock()
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerTrackRecID), "", importBody, http.StatusConflict), codeNotMeeting)

	// Non-meeting (note) recording -> 409 not_meeting.
	m.upload(speakerNoteRecID, 1, map[string]any{
		"kind": "note", "duration": 30, "complete": true,
		"segments": []map[string]any{{"start": 0, "end": 20, "text": "A voice memo."}},
	})
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerNoteRecID), "", importBody, http.StatusConflict), codeNotMeeting)

	// Device-transcribed meeting -> 409 not_meeting.
	m.upload(speakerDevRecID, 1, map[string]any{
		"kind": "meeting", "duration": 30, "complete": true,
		"segments": []map[string]any{{"start": 0, "end": 20, "text": "Device meeting."}},
	})
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerDevRecID), "", importBody, http.StatusConflict), codeNotMeeting)

	// Body > 2 MiB -> 413.
	huge := bytes.Repeat([]byte(" "), recordingwire.MaxSpeakerTurnsBody+16)
	if got := doRawStatus(t, m, "PUT", speakerTurnsPath(speakerRecID), "", huge); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("body > 2 MiB: got %d, want 413", got)
	}

	// Valid import -> 200; detail carries speakers and labelled segments without further edits ("No action needed").
	res := m.do("PUT", speakerTurnsPath(speakerRecID), "", importBody, http.StatusOK)
	sp, _ := res["speakers"].(map[string]any)
	if sp == nil || sp["state"] != "ready" || sp["rev"] != float64(1) {
		t.Fatalf("import response speakers: %v", res)
	}
	d, err := m.engine().Get(recordingwire.KindRecording, speakerRecID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers == nil || len(d.Speakers.People) != 2 || len(d.Segments) != 3 {
		t.Fatalf("detail after import: speakers=%+v segs=%+v", d.Speakers, d.Segments)
	}
	if d.Segments[0].Speaker != "s1" || d.Segments[1].Speaker != "s2" || d.Segments[2].Speaker != "s1" {
		t.Fatalf("segment labels after import: %+v", d.Segments)
	}

	// GET .../live carries revs.speakers and ?parts=speakers returns only revs.speakers.
	live := m.do("GET", livePath(speakerRecID), "", nil, http.StatusOK)
	if live["revs"].(map[string]any)["speakers"] != float64(1) {
		t.Fatalf("GET live revs: %v", live["revs"])
	}
	only := m.do("GET", livePath(speakerRecID)+"?parts=speakers", "", nil, http.StatusOK)
	if raw, _ := json.Marshal(only); string(raw) != `{"revs":{"speakers":1}}` {
		t.Fatalf("parts=speakers: %s", raw)
	}

	// meeting_live event carries changed: ["speakers"] and revs.speakers.
	waitFor(t, "meeting_live event for speakers", func() bool {
		m.f.mu.Lock()
		defer m.f.mu.Unlock()
		for _, ev := range m.f.liveEvents {
			changed, _ := ev["changed"].([]any)
			revs, _ := ev["revs"].(map[string]any)
			if ev["recording_id"] == speakerRecID && slices.Contains(changed, any("speakers")) && revs["speakers"] != nil {
				return true
			}
		}
		return false
	})

	// Same run + same body is idempotent -> 200 without rev bump.
	res2 := m.do("PUT", speakerTurnsPath(speakerRecID), "", importBody, http.StatusOK)
	if res2["speakers"].(map[string]any)["rev"] != float64(1) {
		t.Fatalf("idempotent import bumped rev: %v", res2)
	}

	// Same run + different body -> 409 op_id_reused.
	diffBody := map[string]any{
		"run":     "run-1",
		"engine":  "test-engine 1.0",
		"source":  "hub",
		"covered": [][]float64{{0, 120}},
		"turns":   [][]any{{0, 35, "c1"}, {36, 110, "c2"}},
	}
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerRecID), "", diffBody, http.StatusConflict), codeOpIDReused)

	// > 20000 turns -> 413.
	tooMany := make([][]any, recordingwire.MaxSpeakerTurns+1)
	for i := range tooMany {
		tooMany[i] = []any{float64(i), float64(i) + 0.5, "c1"}
	}
	m.do("PUT", speakerTurnsPath(speakerRecID), "", map[string]any{
		"run": "run-huge", "engine": "e", "source": "hub",
		"covered": [][]float64{{0, 30000}}, "turns": tooMany,
	}, http.StatusRequestEntityTooLarge)
}

// POST /api/v1/recordings/{id}/speakers handles name (including me:true owner fill), merge, unmerge, clear,
// undo round-trip, rev conflict (409 speakers_stale), clear lock (409 speakers_cleared), validation errors (400),
// non-full credentials (403), read-only KB (403 kb_read_only), and track/non-meeting refusal (409 not_meeting).
func TestSpeakersEditRoute(t *testing.T) {
	m := newMeetingFixture(t)
	setSettings(t, m.s, func(v *Settings) { v.OwnerName = "Grace" })
	m.s.mu.Lock()
	m.s.speakerTurnsImport = true
	m.s.inSpace = map[string]bool{speakerTrackRecID: true}
	m.s.mu.Unlock()

	setupHubMeetingForSpeakers(t, m, speakerRecID)
	m.do("PUT", speakerTurnsPath(speakerRecID), "", map[string]any{
		"run":     "edit-run-1",
		"engine":  "test-engine 1.0",
		"source":  "hub",
		"covered": [][]float64{{0, 120}},
		"turns":   [][]any{{0, 35, "c1"}, {36, 72, "c2"}, {73, 110, "c3"}},
	}, http.StatusOK)

	// Non-full credentials -> 403.
	m.do("POST", speakersPath(speakerRecID), "answer", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada"}},
	}, http.StatusForbidden)

	// Read-only -> 403 kb_read_only.
	m.s.mu.Lock()
	m.s.readOnly = true
	m.s.mu.Unlock()
	wantCode(t, m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada"}},
	}, http.StatusForbidden), "kb_read_only")
	m.s.mu.Lock()
	m.s.readOnly = false
	m.s.mu.Unlock()

	// Track recording -> 409 not_meeting.
	wantCode(t, m.do("POST", speakersPath(speakerTrackRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada"}},
	}, http.StatusConflict), codeNotMeeting)

	// Bad JSON / unknown op / multiline name / over 100 runes / unknown speaker -> 400.
	m.do("POST", speakersPath(speakerRecID), "", "{bad json", http.StatusBadRequest)
	m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "bogus", "id": "s1"}},
	}, http.StatusBadRequest)
	m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada\nLovelace"}},
	}, http.StatusBadRequest)
	m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s1", "name": strings.Repeat("a", 101)}},
	}, http.StatusBadRequest)
	m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s99", "name": "Nobody"}},
	}, http.StatusBadRequest)

	// Stale rev -> 409 speakers_stale.
	wantCode(t, m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 99, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada"}},
	}, http.StatusConflict), codeSpeakersStale)

	// Name s1 = "Ada", s2 with me: true (fills owner "Grace") -> 200 with undo.
	named := m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1,
		"ops": []map[string]any{
			{"op": "name", "id": "s1", "name": "Ada"},
			{"op": "name", "id": "s2", "me": true},
		},
	}, http.StatusOK)
	sp := named["speakers"].(map[string]any)
	if sp["rev"] != float64(2) {
		t.Fatalf("after name rev: %v", sp)
	}
	people := sp["people"].([]any)
	if people[0].(map[string]any)["name"] != "Ada" || people[1].(map[string]any)["name"] != "Grace" || people[1].(map[string]any)["me"] != true {
		t.Fatalf("named people: %v", people)
	}

	// Merge s3 into s1, then undo with the returned undo ops.
	merged := m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 2,
		"ops": []map[string]any{{"op": "merge", "from": "s3", "into": "s1"}},
	}, http.StatusOK)
	mergedPeople := merged["speakers"].(map[string]any)["people"].([]any)
	if len(mergedPeople) != 3 || mergedPeople[2].(map[string]any)["merged_into"] != "s1" || mergedPeople[0].(map[string]any)["lines"] != float64(2) {
		t.Fatalf("after merge people: %v", merged)
	}
	undone := m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 3,
		"ops": merged["undo"],
	}, http.StatusOK)
	undonePeople := undone["speakers"].(map[string]any)["people"].([]any)
	if len(undonePeople) != 3 || undonePeople[2].(map[string]any)["merged_into"] != nil || undonePeople[0].(map[string]any)["lines"] != float64(1) {
		t.Fatalf("after undo people: %v", undone)
	}

	// Clear -> 200; subsequent edit and import -> 409 speakers_cleared ("Clear is final").
	cleared := m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 4,
		"ops": []map[string]any{{"op": "clear"}},
	}, http.StatusOK)
	if cleared["speakers"].(map[string]any)["state"] != "cleared" {
		t.Fatalf("after clear: %v", cleared)
	}
	wantCode(t, m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 5, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada"}},
	}, http.StatusConflict), codeSpeakersCleared)
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerRecID), "", map[string]any{
		"run": "after-clear", "engine": "e", "source": "hub",
		"covered": [][]float64{{0, 120}}, "turns": [][]any{{0, 35, "c1"}},
	}, http.StatusConflict), codeSpeakersCleared)
}

// A trimmed meeting still accepts speaker edits via HTTP, while late timeline import returns 409 transcript_expired.
func TestSpeakersRoutesAfterTrim(t *testing.T) {
	m := newMeetingFixture(t)
	m.s.mu.Lock()
	m.s.speakerTurnsImport = true
	m.s.meetingModel = &meetingModelFake{reply: `{"title":"Trimmed","points":[{"text":"Kept","line":1}]}`}
	m.s.mu.Unlock()
	m.engine().RecordingDaysFunc = func() int { return 14 }
	m.engine().TranscriptionReady = func() bool { return true }

	old := time.Now().UTC().AddDate(0, 0, -20)
	// Put the meeting at its own timestamp (20 days ago) via writers so retention pruning can trim it at time.Now().
	u := recordingwire.RecordingUpload{
		Revision: 1, StartedAt: old, Duration: 120, Language: "en-US", Device: "phone",
		Title: "Trimmed Speakers", Complete: true, Kind: recordingwire.RecordingMeeting,
		Segments: []kbwire.RecordingSegment{
			{Start: 0, End: 35, Text: "Ada opened the meeting."},
			{Start: 36, End: 72, Text: "Grace closed the meeting."},
		},
	}
	u.SetTranscription(recordingwire.TranscriptionHub, nil)
	if p, err := writersOf(m.engine()).PutRecording(speakerRecID, u, old.Add(time.Hour)); err != nil || p.Status != recordingwire.RecordingStored {
		t.Fatalf("PutRecording: %+v %v", p, err)
	}
	if _, err := writersOf(m.engine()).HubTranscriptPutWithTiming(speakerRecID, recordingwire.HubPassFinal, recordingwire.TimingTurn, u.Segments); err != nil {
		t.Fatalf("HubTranscriptPutWithTiming: %v", err)
	}
	if _, err := writersOf(m.engine()).TranscriptionUpdate(speakerRecID, func(tr *recordingwire.MeetingTranscription, _ recordingwire.Recording) error {
		tr.State = recordingwire.TranscriptionFinal
		tr.FinalAt = old.Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := writersOf(m.engine()).FinalTranscriptStored(speakerRecID); err != nil {
		t.Fatal(err)
	}
	m.do("PUT", speakerTurnsPath(speakerRecID), "", map[string]any{
		"run": "pre-trim", "engine": "e", "source": "hub",
		"covered": [][]float64{{0, 120}}, "turns": [][]any{{0, 35, "c1"}, {36, 72, "c2"}},
	}, http.StatusOK)
	if n := writersOf(m.engine()).WriteMinutes(t.Context(), 1); n != 1 {
		t.Fatalf("WriteMinutes wrote %d, want 1", n)
	}
	m.engine().PruneRecordings(time.Now().UTC())

	// Edits still work after trim.
	edited := m.do("POST", speakersPath(speakerRecID), "", map[string]any{
		"rev": 1, "ops": []map[string]any{{"op": "name", "id": "s1", "name": "Ada"}},
	}, http.StatusOK)
	sp := edited["speakers"].(map[string]any)
	if sp["trimmed"] != true {
		t.Fatalf("trimmed speaker detail: %v", sp)
	}
	// Late timeline import is refused with 409 transcript_expired.
	wantCode(t, m.do("PUT", speakerTurnsPath(speakerRecID), "", map[string]any{
		"run": "post-trim", "engine": "e", "source": "hub",
		"covered": [][]float64{{0, 120}}, "turns": [][]any{{0, 35, "c1"}},
	}, http.StatusConflict), codeTranscriptExpired)
}
