package writers

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

func setupHubSpeakersMeeting(t *testing.T, e *testEngine, id string, now time.Time) {
	t.Helper()
	u := RecordingUpload{
		Revision:  1,
		StartedAt: now.Add(-2 * time.Hour),
		Duration:  120,
		Language:  "en-US",
		Device:    "phone",
		Title:     "Design Review",
		Complete:  true,
		Kind:      RecordingMeeting,
		Segments: []RecordingSegment{
			{Start: 0, End: 35, Text: "Ada opened the design review for speaker timelines."},
			{Start: 36, End: 72, Text: "Grace proposed storing only anonymous numbers in turns."},
			{Start: 73, End: 110, Text: "Ada summarized the remaining integration tasks."},
		},
	}
	u.SetTranscription(TranscriptionHub, nil)
	if p, err := e.PutRecording(id, u, now); err != nil || p.Status != RecordingStored {
		t.Fatalf("put hub meeting: %+v %v", p, err)
	}
	if _, err := e.HubTranscriptPutWithTiming(id, HubPassFinal, TimingTurn, []RecordingSegment{
		{Start: 0, End: 35, Text: "Ada opened the design review for speaker timelines."},
		{Start: 36, End: 72, Text: "Grace proposed storing only anonymous numbers in turns."},
		{Start: 73, End: 110, Text: "Ada summarized the remaining integration tasks."},
	}); err != nil {
		t.Fatalf("hub transcript put: %v", err)
	}
}

// Meeting.Clone copies Speakers deeply so mutating the clone never mutates the original.
func TestMeetingCloneCopiesSpeakersDeeply(t *testing.T) {
	orig := Meeting{
		Speakers: &MeetingSpeakers{
			State:   SpeakerStateReady,
			Quality: SpeakerQualityOk,
			Next:    3,
			People:  []SpeakerPerson{{ID: "s1", Name: "Ada"}, {ID: "s2"}},
			Stats:   map[SpeakerID]SpeakerStat{"s1": {Seconds: 80, Lines: 3}, "s2": {Seconds: 40, Lines: 2}},
			Edits:   SpeakerEdits{Name: 1},
		},
		Revs: map[string]int64{PartSpeakers: 2},
	}
	cloned := orig.Clone()
	cloned.Speakers.People[0].Name = "Changed"
	cloned.Speakers.Stats["s1"] = SpeakerStat{Seconds: 1, Lines: 1}
	cloned.Revs[PartSpeakers] = 99
	if orig.Speakers.People[0].Name != "Ada" || orig.Speakers.Stats["s1"].Seconds != 80 || orig.Revs[PartSpeakers] != 2 {
		t.Fatalf("mutating clone affected original: %+v", orig.Speakers)
	}
}

// Full re-uploads keep the speaker list, names and merges ("Uploads keep the list"), and shortening a recording cuts
// turns past its new length.
func TestUploadsKeepSpeakersListAndShorteningCutsTurns(t *testing.T) {
	e, _ := hubEngine(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	setupHubSpeakersMeeting(t, e, recID, now)

	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run:     "run-1",
		Engine:  "test-engine 1.0",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 120}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 35, Cluster: "c1"},
			{Start: 36, End: 72, Cluster: "c2"},
			{Start: 73, End: 110, Cluster: "c3"},
		},
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace", Me: true},
		{Op: SpeakerOpMerge, From: "s3", Into: "s1"},
	}, "Grace"); err != nil {
		t.Fatal(err)
	}

	// Re-upload rev 2 with same length -> speaker list, names, and merges stay intact.
	u2 := RecordingUpload{
		Revision:  2,
		StartedAt: now.Add(-2 * time.Hour),
		Duration:  120,
		Language:  "en-US",
		Device:    "phone",
		Title:     "Design Review Updated",
		Complete:  true,
		Kind:      RecordingMeeting,
		Segments: []RecordingSegment{
			{Start: 0, End: 35, Text: "Ada opened the design review for speaker timelines.", Speaker: "s9", SpeakerUnsure: true},
		},
	}
	u2.SetTranscription(TranscriptionHub, nil)
	if p, err := e.PutRecording(recID, u2, now.Add(time.Minute)); err != nil || p.Status != RecordingStored {
		t.Fatalf("re-upload rev 2: %+v %v", p, err)
	}
	d, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers == nil || len(d.Speakers.People) != 3 {
		t.Fatalf("speakers lost after upload: %+v", d.Speakers)
	}
	if d.Speakers.People[0].Name != "Ada" || d.Speakers.People[1].Name != "Grace" || d.Speakers.People[2].MergedInto != "s1" {
		t.Fatalf("names/merges lost after upload: %+v", d.Speakers.People)
	}

	// Re-upload rev 3 shortened to 75s -> turns and covered past 75s are cut, names kept.
	u3 := u2
	u3.Revision = 3
	u3.Duration = 75
	if p, err := e.PutRecording(recID, u3, now.Add(2*time.Minute)); err != nil || p.Status != RecordingStored {
		t.Fatalf("shortened upload rev 3: %+v %v", p, err)
	}
	d3, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d3.Speakers == nil || d3.Speakers.People[0].Name != "Ada" {
		t.Fatalf("names lost after shortening: %+v", d3.Speakers)
	}
	for _, sp := range d3.Speakers.Spans {
		if sp.End > 75.0001 {
			t.Errorf("turn not cut to shortened length 75: %+v", sp)
		}
	}
	for _, cov := range d3.Speakers.Covered {
		if cov[1] > 75.0001 {
			t.Errorf("covered span not cut to shortened length 75: %+v", cov)
		}
	}
}

// Import handles numbering, pairing on re-import, idempotency (same run + same body vs different body), limits,
// segment labelling with timing=turn, and Clear is final.
func TestImportPairingIdempotencyAndClearIsFinal(t *testing.T) {
	e, _ := hubEngine(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	setupHubSpeakersMeeting(t, e, recID, now)

	var hookCalls []string
	e.Writers.OnSpeakersChanged(func(id string) { hookCalls = append(hookCalls, id) })

	req1 := SpeakerTurnsImportRequest{
		Run:     "run-alpha",
		Engine:  "test-engine 1.0 / model q8",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 120}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 35, Cluster: "c1"},
			{Start: 36, End: 72, Cluster: "c2"},
			{Start: 73, End: 110, Cluster: "c1"},
		},
		Signals: map[string]SpeakerSignal{
			"slots": {Number: 2},
			"mode":  {Enum: "offline", IsEnum: true},
		},
	}
	changed, err := e.ImportSpeakerTurns(recID, req1, now)
	if err != nil || !changed {
		t.Fatalf("first import: changed=%v err=%v", changed, err)
	}
	if len(hookCalls) != 1 || hookCalls[0] != recID {
		t.Fatalf("post-commit hook calls: %v", hookCalls)
	}

	// Detail shows ready + ok + segment labels (No action needed).
	d, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers == nil || d.Speakers.State != SpeakerStateReady || d.Speakers.Quality != SpeakerQualityOk || d.Speakers.Rev != 1 {
		t.Fatalf("unexpected detail.speakers after import: %+v", d.Speakers)
	}
	if len(d.Segments) != 3 || d.Segments[0].Speaker != "s1" || d.Segments[1].Speaker != "s2" || d.Segments[2].Speaker != "s1" {
		t.Fatalf("segments not labelled on read: %+v", d.Segments)
	}

	// Idempotent retry: same run + same body -> changed=false, rev stays 1.
	changedAgain, err := e.ImportSpeakerTurns(recID, req1, now.Add(time.Second))
	if err != nil || changedAgain {
		t.Fatalf("idempotent retry: changed=%v err=%v", changedAgain, err)
	}
	dRetry, _ := e.Get(KindRecording, recID, 0)
	if dRetry.Speakers.Rev != 1 {
		t.Fatalf("idempotent retry bumped rev to %d", dRetry.Speakers.Rev)
	}

	// Same run + different body -> ErrOpIDReused.
	reqConflict := req1
	reqConflict.Engine = "other-engine 2.0"
	if _, err := e.ImportSpeakerTurns(recID, reqConflict, now.Add(2*time.Second)); !errors.Is(err, ErrOpIDReused) {
		t.Fatalf("same run + different body: got %v, want ErrOpIDReused", err)
	}

	// Name s1=Ada, s2=Grace, then re-import with swapped cluster labels (c9 matches old s1, c8 matches old s2).
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	req2 := SpeakerTurnsImportRequest{
		Run:     "run-beta",
		Engine:  "test-engine 1.1",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 120}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 35, Cluster: "c9"},
			{Start: 36, End: 72, Cluster: "c8"},
			{Start: 73, End: 110, Cluster: "c9"},
		},
	}
	if changed, err := e.ImportSpeakerTurns(recID, req2, now.Add(3*time.Second)); err != nil || !changed {
		t.Fatalf("pairing re-import: changed=%v err=%v", changed, err)
	}
	dPaired, _ := e.Get(KindRecording, recID, 0)
	if dPaired.Speakers.People[0].Name != "Ada" || dPaired.Speakers.People[1].Name != "Grace" {
		t.Fatalf("pairing dropped speaker names: %+v", dPaired.Speakers.People)
	}
	if dPaired.Segments[0].Speaker != "s1" || dPaired.Segments[1].Speaker != "s2" {
		t.Fatalf("pairing swapped speaker ids: %+v", dPaired.Segments)
	}

	// Clear deletes speaker_turns.json and marks state cleared; late import is refused (Clear is final).
	undo, err := e.EditSpeakers(recID, dPaired.Speakers.Rev, []SpeakerEditOp{{Op: SpeakerOpClear}}, "")
	if err != nil || len(undo) != 0 {
		t.Fatalf("clear: undo=%v err=%v", undo, err)
	}
	if _, err := os.Stat(e.Store.Path(KindRecording, recID, speakerTurnsFile)); !os.IsNotExist(err) {
		t.Fatalf("speaker_turns.json survived clear: %v", err)
	}
	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run:     "run-after-clear",
		Engine:  "test-engine 1.1",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 120}},
		Turns:   []RawSpeakerTurn{{Start: 0, End: 35, Cluster: "c1"}},
	}, now.Add(4*time.Second)); !errors.Is(err, ErrSpeakersCleared) {
		t.Fatalf("import after clear: got %v, want ErrSpeakersCleared", err)
	}
	if _, err := e.EditSpeakers(recID, dPaired.Speakers.Rev+1, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s1", Name: "Ada"}}, ""); !errors.Is(err, ErrSpeakersCleared) {
		t.Fatalf("edit after clear: got %v, want ErrSpeakersCleared", err)
	}

	// Over 20000 turns limit -> ErrSpeakersTooLarge.
	tooMany := make([]RawSpeakerTurn, MaxSpeakerTurns+1)
	for i := range tooMany {
		tooMany[i] = RawSpeakerTurn{Start: float64(i), End: float64(i) + 0.5, Cluster: "c1"}
	}
	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run: "run-huge", Engine: "e", Source: SpeakerSourceHub, Covered: []SpeakerSpan{{0, 30000}}, Turns: tooMany,
	}, now); !errors.Is(err, ErrSpeakersTooLarge) {
		t.Fatalf("too many turns: got %v, want ErrSpeakersTooLarge", err)
	}
}

// Trimming deletes speaker_turns.json while keeping meeting.json's speaker list; detail reports trimmed=true with
// empty spans, editing still works, and late timeline import is refused with ErrTranscriptExpired.
func TestTrimDeletesTurnsKeepsListAndAllowsEdits(t *testing.T) {
	e, _ := hubEngine(t, nil)
	e.RecordingDaysFunc = func() int { return 14 }
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	old := now.AddDate(0, 0, -20)

	// Upload a hub meeting that started 20 days ago? PutRecording rejects past retention, so upload at old+1h then prune at now!
	setupHubSpeakersMeeting(t, e, recID, old.Add(2*time.Hour))
	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run:     "trim-run-1",
		Engine:  "test-engine 1.0",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 120}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 35, Cluster: "c1"},
			{Start: 36, End: 72, Cluster: "c2"},
			{Start: 73, End: 110, Cluster: "c3"},
		},
	}, old.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace", Me: true},
	}, "Grace"); err != nil {
		t.Fatal(err)
	}
	// Write minutes so PruneRecordings trims instead of deleting.
	e.Model = fixtureMinutesModel{}
	if _, err := e.TranscriptionUpdate(recID, func(tr *MeetingTranscription, _ Recording) error {
		tr.State = recordingwire.TranscriptionFinal
		tr.FinalAt = old.Add(2 * time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.FinalTranscriptStored(recID); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 {
		t.Fatalf("WriteMinutes wrote %d, want 1", n)
	}

	if n := e.PruneRecordings(now); n != 0 {
		t.Fatalf("PruneRecordings deleted %d recordings, want 0 (trimmed instead)", n)
	}
	if _, err := os.Stat(e.Store.Path(KindRecording, recID, speakerTurnsFile)); !os.IsNotExist(err) {
		t.Fatalf("speaker_turns.json was not removed by trim: %v", err)
	}
	d, err := e.Get(KindRecording, recID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Speakers == nil || !d.Speakers.Trimmed || len(d.Speakers.Spans) != 0 || len(d.Speakers.Covered) != 0 {
		t.Fatalf("trimmed detail.speakers: %+v", d.Speakers)
	}
	if len(d.Speakers.People) != 3 || d.Speakers.People[0].Name != "Ada" || d.Speakers.People[1].Name != "Grace" {
		t.Fatalf("trimmed speakers list lost names: %+v", d.Speakers.People)
	}

	// Name, merge, unmerge, clear all still work after trim.
	rev := d.Speakers.Rev
	if _, err := e.EditSpeakers(recID, rev, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s3", Name: "Lin"}}, ""); err != nil {
		t.Fatalf("name after trim: %v", err)
	}
	if _, err := e.EditSpeakers(recID, rev+1, []SpeakerEditOp{{Op: SpeakerOpMerge, From: "s3", Into: "s1"}}, ""); err != nil {
		t.Fatalf("merge after trim: %v", err)
	}
	if _, err := e.EditSpeakers(recID, rev+2, []SpeakerEditOp{{Op: SpeakerOpUnmerge, ID: "s3"}}, ""); err != nil {
		t.Fatalf("unmerge after trim: %v", err)
	}
	// Import after trim -> ErrTranscriptExpired.
	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run: "trim-late", Engine: "e", Source: SpeakerSourceHub, Covered: []SpeakerSpan{{0, 60}},
		Turns: []RawSpeakerTurn{{Start: 0, End: 30, Cluster: "c1"}},
	}, now); !errors.Is(err, ErrTranscriptExpired) {
		t.Fatalf("import after trim: got %v, want ErrTranscriptExpired", err)
	}
	if _, err := e.EditSpeakers(recID, rev+3, []SpeakerEditOp{{Op: SpeakerOpClear}}, ""); err != nil {
		t.Fatalf("clear after trim: %v", err)
	}
	// Verify meeting_live notifications include PartSpeakers.
	var partsSeen []string
	e.MeetingChanged = func(_ string, parts []string) { partsSeen = append(partsSeen, parts...) }
	// Check another meeting emits PartSpeakers.
	setupHubSpeakersMeeting(t, e, recID2, now)
	if _, err := e.ImportSpeakerTurns(recID2, SpeakerTurnsImportRequest{
		Run: "ev-1", Engine: "e", Source: SpeakerSourceHub, Covered: []SpeakerSpan{{0, 120}},
		Turns: []RawSpeakerTurn{{Start: 0, End: 35, Cluster: "c1"}, {Start: 36, End: 72, Cluster: "c2"}},
	}, now); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(partsSeen, PartSpeakers) {
		t.Fatalf("MeetingChanged did not emit PartSpeakers: %v", partsSeen)
	}
}
