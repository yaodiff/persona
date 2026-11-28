package writers

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// The recordings the module gates start their hubs on (scripts/ci/core-only-smoke.py, scripts/ci/module-e2e.py):
// written by the knowledge base and this module's writers, as a hub with the recording module leaves them, so the gates can check
// that a hub without the module still lists, opens and finds them and leaves their files as they are.
const (
	// fixtureRecording is a meeting with its transcript and minutes; fixturePendingRecording one whose minutes were
	// still to be written. scripts/ci/core-only-smoke.py names both.
	fixtureRecording        = "6f1c2a3b-0000-4000-8000-00000000c0de"
	fixturePendingRecording = "6f1c2a3b-0000-4000-8000-00000000beef"
	// fixtureAudioRecording is a hub-transcribed meeting still in flight, with two seconds of audio held
	// (attachments/audio/0.pcm); fixtureRecording holds one photo (attachments/p1.jpg).
	fixtureAudioRecording = "6f1c2a3b-0000-4000-8000-00000000a0d1"
	// fixtureSpeakersRecording is a hub-transcribed meeting with timing "turn", a stored speaker timeline, and
	// synthetic speaker names ("Ada", "Grace") so Core module-off gates verify detail.speakers and segment labels.
	fixtureSpeakersRecording = "6f1c2a3b-0000-4000-8000-000000005bea"
)

type fixtureMinutesModel struct{}

func (fixtureMinutesModel) JSONMax(_ context.Context, _, user string, _ int, out any) error {
	if strings.Contains(user, "Ada proposed keeping speaker timelines") {
		return json.Unmarshal([]byte(`{"title": "Kept recording, speakers",
			"points": [{"text": "Speaker timelines stay readable without the module", "line": 1}],
			"decisions": [{"text": "Stored speaker names survive module switches", "line": 3}],
			"debates": [{"question": "Keep timelines in the knowledge base or only in memory?", "status": "settled",
				"positions": [
					{"stance": "Keep timelines in the knowledge base", "line": 1, "holder": "S1"},
					{"stance": "Keep names readable without the module", "line": 2, "holder": "S2"}
				],
				"outcome": {"text": "Stored speaker names survive module switches", "line": 3}}]}`), out)
	}
	return json.Unmarshal([]byte(`{"title": "Kept recording review", "points": [{"text": "The wombat checklist stays readable without the module", "line": 2}],
		"decisions": [{"text": "Stored recordings are never hidden by a switch", "line": 3}]}`), out)
}

// TestWriteRecordingFixture writes the gates' recordings into the knowledge base at PERSONA_RECORDING_FIXTURE_DIR (a
// data directory no hub has open); skipped without it.
func TestWriteRecordingFixture(t *testing.T) {
	dir := os.Getenv("PERSONA_RECORDING_FIXTURE_DIR")
	if dir == "" {
		t.Skip("PERSONA_RECORDING_FIXTURE_DIR is not set: only the module gates' scripts write this fixture")
	}
	st, err := kb.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(&kb.Engine{Store: st, Model: fixtureMinutesModel{}, Owner: "Owner", Logf: func(string, ...any) {}})
	now := time.Now().Truncate(time.Second)
	put := func(id, title string, start time.Time, texts ...string) {
		u := recordingwire.RecordingUpload{Revision: 1, StartedAt: start, Duration: float64(10 * len(texts)), Language: "en-US", Device: "phone",
			Title: title, Complete: true, Kind: recordingwire.RecordingMeeting}
		for i, text := range texts {
			u.Segments = append(u.Segments, kbwire.RecordingSegment{Start: float64(10 * i), End: float64(10*i + 9), Text: text})
		}
		if p, err := e.PutRecording(id, u, now); err != nil || p.Status != recordingwire.RecordingStored {
			t.Fatalf("put %s: %+v %v", id, p, err)
		}
	}
	put(fixtureRecording, "Kept recording", now.Add(-2*time.Hour), "This made-up meeting is the module gate's fixture",
		"The quokka transcript stays readable without the module", "Stored recordings are never hidden by a switch")
	photo := recordingwire.PhotoUpload{ID: "6f1c2a3b-0000-4000-8000-0000000000f1", At: 12, Mime: recordingwire.PhotoJPEG, Width: 1600, Height: 1200,
		Data: []byte("\xff\xd8 the module gate's made-up photo \xff\xd9"), Text: "whiteboard", Caption: "Plan", Digest: "fixture-photo"}
	if _, created, err := e.PhotoAdd(fixtureRecording, photo, false); err != nil || !created {
		t.Fatalf("add the photo: %v (created %v)", err, created)
	}
	// Hub-transcribed meeting with timing "turn", speaker timeline, and synthetic names (Ada, Grace).
	e.TranscriptionReady = func() bool { return true }
	spUpload := recordingwire.RecordingUpload{
		Revision: 1, StartedAt: now.Add(-90 * time.Minute), Duration: 90, Language: "en-US", Device: "phone",
		Title: "Kept recording, speakers", Complete: true, Kind: recordingwire.RecordingMeeting,
		Segments: []kbwire.RecordingSegment{
			{Start: 0, End: 28, Text: "Ada proposed keeping speaker timelines in the knowledge base."},
			{Start: 29, End: 58, Text: "Grace agreed and noted that names stay readable without the module."},
			{Start: 59, End: 86, Text: "Ada confirmed stored speaker names survive module switches."},
		},
	}
	spUpload.SetTranscription(recordingwire.TranscriptionHub, nil)
	if p, err := e.PutRecording(fixtureSpeakersRecording, spUpload, now); err != nil || p.Status != recordingwire.RecordingStored {
		t.Fatalf("put speakers fixture: %+v %v", p, err)
	}
	if _, err := e.HubTranscriptPutWithTiming(fixtureSpeakersRecording, HubPassFinal, TimingTurn, []kbwire.RecordingSegment{
		{Start: 0, End: 28, Text: "Ada proposed keeping speaker timelines in the knowledge base."},
		{Start: 29, End: 58, Text: "Grace agreed and noted that names stay readable without the module."},
		{Start: 59, End: 86, Text: "Ada confirmed stored speaker names survive module switches."},
	}); err != nil {
		t.Fatalf("put hub transcript with timing: %v", err)
	}
	if _, err := e.TranscriptionUpdate(fixtureSpeakersRecording, func(t *MeetingTranscription, _ Recording) error {
		t.State = recordingwire.TranscriptionFinal
		t.FinalAt = now
		return nil
	}); err != nil {
		t.Fatalf("mark transcription final: %v", err)
	}
	if err := e.FinalTranscriptStored(fixtureSpeakersRecording); err != nil {
		t.Fatalf("mark final transcript stored: %v", err)
	}
	if _, err := e.ImportSpeakerTurns(fixtureSpeakersRecording, SpeakerTurnsImportRequest{
		Run:     "fixture-run-1",
		Engine:  "fixture-engine 1.0",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 90}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 28, Cluster: "c1"},
			{Start: 29, End: 58, Cluster: "c2"},
			{Start: 59, End: 86, Cluster: "c1"},
		},
	}, now); err != nil {
		t.Fatalf("import speaker turns: %v", err)
	}
	if _, err := e.EditSpeakers(fixtureSpeakersRecording, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace", Me: true},
	}, "Grace"); err != nil {
		t.Fatalf("name fixture speakers: %v", err)
	}
	if n := e.WriteMinutes(context.Background(), 2); n != 2 {
		t.Fatalf("wrote %d minutes, want 2", n)
	}
	put(fixturePendingRecording, "Kept recording, minutes pending", now.Add(-time.Hour), "A second made-up meeting", "Its minutes were never written")
	if r, ok := st.Recording(fixturePendingRecording); !ok || r.MinutesState != MinutesPending {
		t.Fatalf("the pending fixture's minutes state: %+v", r)
	}
	live := recordingwire.RecordingUpload{Revision: 1, StartedAt: now.Add(-30 * time.Minute), Duration: 10, Language: "en-US", Device: "phone",
		Title: "Kept recording, audio held", Kind: recordingwire.RecordingMeeting, Segments: []kbwire.RecordingSegment{{Start: 0, End: 9, Text: "Audio is still held"}}}
	live.SetTranscription(recordingwire.TranscriptionHub, nil)
	if p, err := e.PutRecording(fixtureAudioRecording, live, now); err != nil || p.Status != recordingwire.RecordingStored {
		t.Fatalf("put the in-flight recording: %+v %v", p, err)
	}
	pcm := make([]byte, 4*recordingwire.AudioRate)
	for i := range pcm {
		pcm[i] = byte(i*7 + 1)
	}
	if _, err := e.AudioPut(fixtureAudioRecording, 0, pcm); err != nil {
		t.Fatalf("hold the audio: %v", err)
	}
	if _, err := st.ReadMinutes(fixtureRecording); err != nil {
		t.Fatal(err)
	}
	spMin, err := st.ReadMinutes(fixtureSpeakersRecording)
	if err != nil {
		t.Fatal(err)
	}
	if len(spMin.Debates) != 1 || len(spMin.Debates[0].Positions) != 2 ||
		spMin.Debates[0].Positions[0].Speaker != "s1" || spMin.Debates[0].Positions[0].SpeakerUnsure ||
		spMin.Debates[0].Positions[1].Speaker != "s2" || spMin.Debates[0].Positions[1].SpeakerUnsure {
		t.Fatalf("speakers fixture debates not attributed: %+v", spMin.Debates)
	}
	if !strings.Contains(spMin.Markdown, "(Ada)") || !strings.Contains(spMin.Markdown, "(Grace)") {
		t.Fatalf("speakers fixture markdown lacks speaker names:\n%s", spMin.Markdown)
	}
}
