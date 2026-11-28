package kb

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Without a speaker block in meeting.json, detail.speakers is omitted and segments carry neither speaker nor
// speaker_unsure — byte for byte as before (#17674 "Without a timeline nothing changes").
func TestRecordingDetailByteIdenticalWithoutSpeakers(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 10, 16, 0, 0, 0, goldenZone)
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)

	putRecording(t, e, recID, meetingUpload(1, start, "First sentence", "Second sentence"), now)
	d, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers != nil {
		t.Fatalf("expected nil detail.speakers without speaker data, got %+v", d.Speakers)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{`"speakers"`, `"speaker"`, `"speaker_unsure"`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("detail JSON without speakers must not contain %s: %s", forbidden, text)
		}
	}
}

// speaker_turns.json (and any temporary replacement file) is excluded from knowledge-base search (#17674).
func TestSpeakerTurnsExcludedFromSearch(t *testing.T) {
	if searchable("recordings/" + recID + "/speaker_turns.json") {
		t.Error("searchable() returned true for speaker_turns.json")
	}
	if searchable("recordings/" + recID + "/speaker_turns.json.tmp") {
		t.Error("searchable() returned true for speaker_turns.json temporary file")
	}

	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 10, 16, 0, 0, 0, goldenZone)
	putRecording(t, e, recID, meetingUpload(1, now.Add(-time.Hour), "Ordinary meeting line"), now)
	turns := SpeakerTurnsFile{
		Run:     "search-exclusion-run",
		Source:  recordingwire.SpeakerSourceHub,
		Engine:  "unique_unsearchable_diarizer_token_98765",
		Covered: []recordingwire.SpeakerSpan{{0, 60}},
		Turns:   []recordingwire.SpeakerTurn{{Start: 0, End: 60, Speaker: "s1"}},
	}
	b, _ := json.Marshal(turns)
	if err := os.WriteFile(e.Store.Path(KindRecording, recID, speakerTurnsFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := e.Find("unique_unsearchable_diarizer_token_98765", 10); len(hits) != 0 {
		t.Fatalf("Find returned hits from speaker_turns.json: %+v", hits)
	}
	if hits := e.Search("unique_unsearchable_diarizer_token_98765", false, 10); len(hits) != 0 {
		t.Fatalf("Search returned hits from speaker_turns.json: %+v", hits)
	}
}

// detail.speakers computes candidates on read from owner, calendar attendees, minutes who, and names used in recent
// related meetings (overlapping calendar attendees or same title), respecting scan and return caps.
func TestSpeakerCandidatesComputedOnReadAndCapped(t *testing.T) {
	e := goldenEngine(t, nil)
	e.Owner = "Grace"
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)

	// Create 25 earlier meetings with the same title; only the most recent maxSpeakerCandidateMeetings (20) are scanned.
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("11111111-1111-4111-8111-%012d", i+1)
		start := base.Add(-time.Duration(25-i) * time.Hour)
		u := meetingUpload(1, start, "Weekly planning sync line")
		u.Title = "Weekly Sync"
		putRecording(t, e, id, u, base)
		m := Meeting{
			Transcription: &recordingwire.MeetingTranscription{Mode: recordingwire.TranscriptionHub, State: recordingwire.TranscriptionFinal},
			Speakers: &recordingwire.MeetingSpeakers{
				State:  recordingwire.SpeakerStateReady,
				Next:   2,
				People: []recordingwire.SpeakerPerson{{ID: "s1", Name: fmt.Sprintf("Member%02d", i+1)}},
			},
		}
		raw, _ := json.Marshal(m)
		if err := os.WriteFile(e.Store.Path(KindRecording, id, meetingFile), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Create an unrelated meeting whose speaker name must never appear in candidates.
	unrelatedID := "22222222-2222-4222-8222-222222222222"
	uOther := meetingUpload(1, base.Add(-10*time.Minute), "Unrelated lunch chat")
	uOther.Title = "Unrelated Topic"
	putRecording(t, e, unrelatedID, uOther, base)
	mOther := Meeting{
		Transcription: &recordingwire.MeetingTranscription{Mode: recordingwire.TranscriptionHub, State: recordingwire.TranscriptionFinal},
		Speakers: &recordingwire.MeetingSpeakers{
			State:  recordingwire.SpeakerStateReady,
			Next:   2,
			People: []recordingwire.SpeakerPerson{{ID: "s1", Name: "Outsider"}},
		},
	}
	rawOther, _ := json.Marshal(mOther)
	if err := os.WriteFile(e.Store.Path(KindRecording, unrelatedID, meetingFile), rawOther, 0o600); err != nil {
		t.Fatal(err)
	}

	// Target meeting with calendar attendee, minutes who, and speaker block.
	u := meetingUpload(1, base, "Ada discussed the weekly sync.")
	u.Title = "Weekly Sync"
	u.Calendar = &recordingwire.RecordingCalendar{Title: "Weekly Sync", Attendees: []string{"Ada", "Grace"}}
	putRecording(t, e, recID, u, base)
	m := Meeting{
		Transcription: &recordingwire.MeetingTranscription{Mode: recordingwire.TranscriptionHub, State: recordingwire.TranscriptionFinal},
		Speakers: &recordingwire.MeetingSpeakers{
			State:   recordingwire.SpeakerStateWaiting,
			Done:    45,
			Quality: "",
			Next:    1,
		},
		Revs: map[string]int64{PartSpeakers: 1},
	}
	rawM, _ := json.Marshal(m)
	if err := os.WriteFile(e.Store.Path(KindRecording, recID, meetingFile), rawM, 0o600); err != nil {
		t.Fatal(err)
	}
	putMeetingMinutes(t, e, recID, Minutes{
		Version: 1, Revision: 1, Title: "Weekly Sync",
		Points:  []MinutesPoint{{Text: "Lin summarized milestones", At: 5, Who: "Lin"}},
		Actions: []MinutesAction{{ID: "a1", Who: "Sam", What: "Share notes"}},
	})

	d, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers == nil {
		t.Fatal("expected detail.speakers")
	}
	if d.Speakers.State != recordingwire.SpeakerStateWaiting || d.Speakers.Done == nil || *d.Speakers.Done != 45 || d.Speakers.End == nil {
		t.Fatalf("waiting progress missing: %+v", d.Speakers)
	}
	cands := d.Speakers.Candidates
	if len(cands) != maxSpeakerCandidates {
		t.Fatalf("got %d candidates (%v), want cap %d", len(cands), cands, maxSpeakerCandidates)
	}
	joined := strings.Join(cands, ",")
	for _, want := range []string{"Grace", "Ada", "Lin", "Sam", "Member25"} {
		if !strings.Contains(joined, want) {
			t.Errorf("candidates %v missing %q", cands, want)
		}
	}
	for _, skip := range []string{"Outsider", "Member01"} {
		if strings.Contains(joined, skip) {
			t.Errorf("candidates %v should not include %q", cands, skip)
		}
	}
}

// Low separation quality marks every labelled segment as speaker_unsure and leaves samples empty.
func TestLowQualityMarksAllSegmentsUnsureAndOmitsSamples(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 10, 16, 0, 0, 0, goldenZone)
	u := meetingUpload(1, now.Add(-time.Hour), "Short clip line one", "Short clip line two")
	u.Duration = 40
	u.Segments[0].Start, u.Segments[0].End = 0, 18
	u.Segments[1].Start, u.Segments[1].End = 20, 38
	putRecording(t, e, recID, u, now)

	meeting := Meeting{
		Transcription: &recordingwire.MeetingTranscription{Mode: recordingwire.TranscriptionHub, State: recordingwire.TranscriptionFinal},
		Speakers: &recordingwire.MeetingSpeakers{
			State:      recordingwire.SpeakerStateReady,
			Quality:    recordingwire.SpeakerQualityLow,
			QualityWhy: recordingwire.SpeakerQualityTooLittleSpeech,
			Next:       3,
			People:     []recordingwire.SpeakerPerson{{ID: "s1"}, {ID: "s2"}},
			Stats: map[recordingwire.SpeakerID]recordingwire.SpeakerStat{
				"s1": {Seconds: 18, Lines: 1},
				"s2": {Seconds: 18, Lines: 1},
			},
		},
		Revs: map[string]int64{PartSpeakers: 1},
	}
	rawM, _ := json.Marshal(meeting)
	_ = os.WriteFile(e.Store.Path(KindRecording, recID, meetingFile), rawM, 0o600)
	hub := HubTranscript{
		Rev:    1,
		Pass:   recordingwire.HubPassFinal,
		Timing: recordingwire.TimingTurn,
		Segments: []RecordingSegment{
			{Start: 0, End: 18, Text: "Short clip line one", Source: recordingwire.SourceHub},
			{Start: 20, End: 38, Text: "Short clip line two", Source: recordingwire.SourceHub},
		},
	}
	rawH, _ := json.Marshal(hub)
	_ = os.WriteFile(e.Store.Path(KindRecording, recID, hubSegmentsFile), rawH, 0o600)
	turns := SpeakerTurnsFile{
		Run:     "low-q-1",
		Source:  recordingwire.SpeakerSourceHub,
		Engine:  "test-engine 1.0",
		Covered: []recordingwire.SpeakerSpan{{0, 40}},
		Turns: []recordingwire.SpeakerTurn{
			{Start: 0, End: 18, Speaker: "s1"},
			{Start: 20, End: 38, Speaker: "s2"},
		},
	}
	rawT, _ := json.Marshal(turns)
	_ = os.WriteFile(e.Store.Path(KindRecording, recID, speakerTurnsFile), rawT, 0o600)

	d, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers == nil || d.Speakers.Quality != recordingwire.SpeakerQualityLow {
		t.Fatalf("detail.speakers: %+v", d.Speakers)
	}
	for i, sg := range d.Segments {
		if sg.Speaker == "" || !sg.SpeakerUnsure {
			t.Errorf("segment %d under low quality: want labelled+unsure, got %+v", i, sg)
		}
	}
	for _, p := range d.Speakers.People {
		if len(p.Samples) != 0 {
			t.Errorf("person %s should have no sure samples under low quality, got %v", p.ID, p.Samples)
		}
	}
}
