package writers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// setupHubFinalMeeting creates a hub-transcribed meeting with final timing=turn segments and marks transcription final.
func setupHubFinalMeeting(t *testing.T, e *testEngine, id string, start, now time.Time, lang string, segs []RecordingSegment, cal *recordingwire.RecordingCalendar) {
	t.Helper()
	dur := 0.0
	for _, sg := range segs {
		if sg.End > dur {
			dur = sg.End
		}
	}
	if dur < 60 {
		dur = 60
	}
	u := RecordingUpload{
		Revision:  1,
		StartedAt: start,
		Duration:  dur,
		Language:  lang,
		Device:    "phone",
		Title:     "Export cap review",
		Complete:  true,
		Kind:      RecordingMeeting,
		Calendar:  cal,
		Segments:  segs,
	}
	u.SetTranscription(TranscriptionHub, nil)
	if p, err := e.PutRecording(id, u, now); err != nil || p.Status != RecordingStored {
		t.Fatalf("put hub meeting: %+v %v", p, err)
	}
	if _, err := e.HubTranscriptPutWithTiming(id, HubPassFinal, TimingTurn, segs); err != nil {
		t.Fatalf("hub transcript put: %v", err)
	}
	if _, err := e.TranscriptionUpdate(id, func(tr *MeetingTranscription, _ Recording) error {
		tr.State = recordingwire.TranscriptionFinal
		tr.FinalAt = now
		return nil
	}); err != nil {
		t.Fatalf("transcription final: %v", err)
	}
	if err := e.FinalTranscriptStored(id); err != nil {
		t.Fatalf("final transcript stored: %v", err)
	}
}

func standardDebateSegments() []RecordingSegment {
	return []RecordingSegment{
		{Start: 0, End: 25, Text: "Let us review the export size cap today."},
		{Start: 26, End: 52, Text: "We should count the cap per item so one big entry never blocks the rest."},
		{Start: 53, End: 80, Text: "Counting per recording is simpler to explain to users."},
		{Start: 81, End: 108, Text: "I strongly back counting per item for safety."},
		{Start: 109, End: 135, Text: "Decided to count the cap per item for this release."},
	}
}

func standardDebateTurns() SpeakerTurnsImportRequest {
	return SpeakerTurnsImportRequest{
		Run:     "run-1",
		Engine:  "test-engine 1.0",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 140}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 25, Cluster: "c1"},
			{Start: 26, End: 52, Cluster: "c1"},
			{Start: 53, End: 80, Cluster: "c2"},
			{Start: 81, End: 108, Cluster: "c3"},
			{Start: 109, End: 135, Cluster: "c1"},
		},
	}
}

const standardSpeakersDebateReply = `{
	"title": "Export cap review",
	"headline": "Export cap counts per item for this release",
	"conclusions": [{"text": "Count the cap **per item**", "line": 5}],
	"topics": [{
		"title": "Export",
		"icon": "progress",
		"points": [{"text": "Reviewed the export cap rule", "line": 1}],
		"decisions": [{"text": "Count the cap **per item** for this release", "line": 5}]
	}],
	"debates": [{
		"question": "Should the export cap count per item or per recording?",
		"topic": "Export",
		"status": "settled",
		"positions": [
			{"stance": "Count per item so one large entry does not block export", "line": 2, "holder": "S1", "also": [{"line": 4, "holder": "S3"}]},
			{"stance": "Count per recording because it is simpler to explain", "line": 3, "holder": "S2"}
		],
		"outcome": {"text": "Count per item for this release", "line": 5}
	}]
}`

// TestMinutesSpeakersPromptGoldens pins the numbered-transcript prompt wording and rules in zh-Hans, en, ja, and the
// merge prompt/input, while verifying owner-chosen speaker names never enter the model's prompts.
func TestMinutesSpeakersPromptGoldens(t *testing.T) {
	// System prompts with Speakers=true in zh-Hans (default), en, and ja.
	golden(t, "minutes.speakers.system", prompts.RenderKBMinutes(prompts.KBOwner{Owner: "示例用户", Speakers: true}))
	golden(t, "minutes.speakers.system.en", prompts.RenderKBMinutes(prompts.KBOwner{Owner: "示例用户", Language: "English", Speakers: true}))
	golden(t, "minutes.speakers.system.ja", prompts.RenderKBMinutes(prompts.KBOwner{Owner: "示例用户", Language: "日本語", Speakers: true}))
	golden(t, "minutes_merge.speakers.system", prompts.RenderKBMinutesMerge(prompts.KBOwner{Owner: "示例用户", Speakers: true}))
	golden(t, "minutes_merge.speakers.system.en", prompts.RenderKBMinutesMerge(prompts.KBOwner{Owner: "示例用户", Language: "English", Speakers: true}))

	// User prompt with numbered transcript (merged speaker numbers, unsure marker ?, unlabelled line) and never owner names.
	m := &captureModel{reply: standardSpeakersDebateReply}
	e, _ := hubEngine(t, m)
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	segs := []RecordingSegment{
		{Start: 0, End: 25, Text: "Let us review the export size cap today."},
		{Start: 26, End: 52, Text: "Per item keeps one large entry from blocking the rest."},
		{Start: 53, End: 70, Text: "Per recording is simpler to explain."},
		{Start: 71, End: 95, Text: "Decided to count per item."},
	}
	setupHubFinalMeeting(t, e, recID, start, start.Add(time.Hour), "en-US", segs, nil)
	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run:     "golden-run",
		Engine:  "test-engine 1.0",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 70}}, // line 4 (71..95) is in a hole -> unlabelled
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 25, Cluster: "c1"},  // s1
			{Start: 26, End: 52, Cluster: "c3"}, // s2 (will be merged into s1 -> shown as S1)
			{Start: 53, End: 62, Cluster: "c1"}, // s1
			{Start: 62, End: 70, Cluster: "c2"}, // s3 (not merged -> line 3 split 9s s1 / 8s s3 -> S1?)
		},
	}, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Name s1 "Zelda" (with me:true) and merge s2 into s1 -> model MUST see S1 (merged), never "Zelda" or "me".
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Zelda", Me: true},
		{Op: SpeakerOpMerge, From: "s2", Into: "s1"},
	}, "Zelda"); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 || len(m.calls) != 1 {
		t.Fatalf("wrote %d in %d calls", n, len(m.calls))
	}
	sys, user := m.calls[0][0], m.calls[0][1]
	if strings.Contains(sys, "Zelda") || strings.Contains(user, "Zelda") {
		t.Fatalf("owner-chosen speaker name leaked into model prompt:\nsys=%s\nuser=%s", sys, user)
	}
	if !strings.Contains(user, "[1] 14:00:00 〔S1〕 ") || !strings.Contains(user, "[2] 14:00:26 〔S1〕 ") ||
		!strings.Contains(user, "[3] 14:00:53 〔S1?〕 ") || strings.Contains(user, "[4] 14:01:11 〔") {
		t.Fatalf("unexpected numbered transcript formatting:\n%s", user)
	}
	golden(t, "minutes.speakers.user", user)

	// Merge input golden with speaker markers, holder and also.
	mergeIn := prompts.KBMinutesMergeInput{
		Meeting: prompts.KBMinutesInput{Title: "Export cap review", Start: start, End: start.Add(time.Hour), Weekday: "周二", Minutes: 60},
		Parts: []prompts.KBMinutesPart{{
			Part: 1, From: "14:00:00", To: "14:30:00",
			Points: []prompts.KBMinutesNote{{Line: 1, Speaker: "S1", Topic: "Export", Text: "Reviewed export cap"}},
			Debates: []prompts.KBMinutesDebate{{
				Question: "Per item or per recording?",
				Topic:    "Export",
				Positions: []prompts.KBMinutesDebatePosition{{
					Line: 2, Speaker: "S1", Holder: "S1", Stance: "Count per item",
					Also: []prompts.KBMinutesDebateAlso{{Line: 4, Speaker: "S3", Holder: "S3"}},
				}},
			}},
		}},
	}
	golden(t, "minutes_merge.speakers.user", prompts.RenderKBMinutesMergeInput(mergeIn))
}

// TestMinutesSpeakersImportNameMergeImmediateNoModelNoPush verifies:
//   - Importing a timeline with timing=turn attributes disagreement positions automatically without manual action.
//   - Naming and merging update minutes.json and minutes.md immediately (<2s) with zero extra model calls, zero new
//     pushes, unchanged minutes.version, and emit PartSpeakers + summary notifications.
func TestMinutesSpeakersImportNameMergeImmediateNoModelNoPush(t *testing.T) {
	model := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
	e, _ := hubEngine(t, model)
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
	now := start.Add(time.Hour)
	setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)

	var pushes int
	e.minutesWritten = func(context.Context, Recording, Minutes) { pushes++ }
	var meetingPartsSeen []string
	e.MeetingChanged = func(_ string, parts []string) { meetingPartsSeen = append(meetingPartsSeen, parts...) }
	var summaryNotifies int
	e.Changed = func(c kb.Change) {
		if c.Kind == KindRecording && c.ID == recID && c.What == "summary" {
			summaryNotifies++
		}
	}
	e.ObserveChanges()

	if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 {
		t.Fatalf("WriteMinutes = %d, want 1", n)
	}
	if len(model.calls) != 1 || pushes != 1 {
		t.Fatalf("after initial write: calls=%d pushes=%d", len(model.calls), pushes)
	}
	m0, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if m0.Version != 1 || len(m0.Debates) != 1 {
		t.Fatalf("v0 minutes: v=%d debates=%+v", m0.Version, m0.Debates)
	}
	p0, p1 := m0.Debates[0].Positions[0], m0.Debates[0].Positions[1]
	if p0.Speaker != "s1" || p0.SpeakerUnsure || len(p0.Also) != 1 || p0.Also[0].Speaker != "s3" || p0.Also[0].SpeakerUnsure ||
		p1.Speaker != "s2" || p1.SpeakerUnsure {
		t.Fatalf("auto attribution mismatch: p0=%+v p1=%+v", p0, p1)
	}
	if !strings.Contains(m0.Markdown, "(Speaker 1, Speaker 3)") || !strings.Contains(m0.Markdown, "(Speaker 2)") {
		t.Fatalf("unnamed markdown mismatch:\n%s", m0.Markdown)
	}

	// Name s1=Ada, s2=Grace, s3=Cy -> updates minutes.json and minutes.md immediately (<2s), no model call, no push, version stays 1.
	meetingPartsSeen = nil
	beforeSummary := summaryNotifies
	t0 := time.Now()
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace", Me: true},
		{Op: SpeakerOpName, ID: "s3", Name: "Cy"},
	}, "Grace"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(t0); elapsed >= 2*time.Second {
		t.Errorf("EditSpeakers took %v, want <2s", elapsed)
	}
	if len(model.calls) != 1 || pushes != 1 {
		t.Fatalf("naming triggered model or push: calls=%d pushes=%d", len(model.calls), pushes)
	}
	if summaryNotifies <= beforeSummary || !containsString(meetingPartsSeen, PartSpeakers) {
		t.Errorf("expected summary notify and PartSpeakers: summaries=%d->%d parts=%v", beforeSummary, summaryNotifies, meetingPartsSeen)
	}
	m1, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Version != 1 {
		t.Errorf("version bumped on speaker edit: got %d, want 1", m1.Version)
	}
	if !strings.Contains(m1.Markdown, "(Ada, Cy)") || !strings.Contains(m1.Markdown, "(Grace)") {
		t.Errorf("named markdown mismatch:\n%s", m1.Markdown)
	}
	if diskMD := readFile(t, e, recID, "minutes.md"); !strings.Contains(diskMD, "(Ada, Cy)") || !strings.Contains(diskMD, "(Grace)") {
		t.Errorf("minutes.md not updated:\n%s", diskMD)
	}

	// Merge s3 into s1 -> position's main+also deduplicate to just (Ada), no model call, no push, version stays 1.
	t1 := time.Now()
	if _, err := e.EditSpeakers(recID, 2, []SpeakerEditOp{{Op: SpeakerOpMerge, From: "s3", Into: "s1"}}, ""); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(t1); elapsed >= 2*time.Second {
		t.Errorf("merge took %v, want <2s", elapsed)
	}
	if len(model.calls) != 1 || pushes != 1 {
		t.Fatalf("merge triggered model or push: calls=%d pushes=%d", len(model.calls), pushes)
	}
	m2, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Version != 1 || m2.Debates[0].Positions[0].Also[0].Speaker != "s1" || m2.Debates[0].Positions[0].Also[0].SpeakerUnsure {
		t.Fatalf("merged also attribution mismatch: v=%d pos=%+v", m2.Version, m2.Debates[0].Positions[0])
	}
	if !strings.Contains(m2.Markdown, "block export (Ada)\n") || strings.Contains(m2.Markdown, "Cy") {
		t.Errorf("merged markdown should deduplicate Ada and drop Cy:\n%s", m2.Markdown)
	}
}

// TestMinutesSpeakersAutoMergeAndUnmergeBeforeAfterTrim verifies:
// - Naming two speakers with the same name auto-merges less-spoken into more-spoken.
// - Unmerging returns the split-out speaker unnamed.
// - Unmerging before trim and after trim produce identical attribution on minutes.json and minutes.md.
func TestMinutesSpeakersAutoMergeAndUnmergeBeforeAfterTrim(t *testing.T) {
	runCase := func(trimBeforeUnmerge bool) (MinutesDebatePosition, string) {
		t.Helper()
		model := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
		e, _ := hubEngine(t, model)
		e.RecordingDaysFunc = func() int { return 14 }
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
		oldStart := now.AddDate(0, 0, -20)
		setupHubFinalMeeting(t, e, recID, oldStart, oldStart.Add(time.Hour), "en-US", standardDebateSegments(), nil)
		if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), oldStart.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if n := e.WriteMinutes(context.Background(), 1); n != 1 {
			t.Fatalf("WriteMinutes = %d", n)
		}
		// Name s1=Ada, then name s3=Ada -> auto-merges s3 (27s) into s1 (77s).
		if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s1", Name: "Ada"}}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.EditSpeakers(recID, 2, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s3", Name: "Ada"}}, ""); err != nil {
			t.Fatal(err)
		}
		dMerged, _ := e.Get(KindRecording, recID, 0)
		if dMerged.Speakers.People[2].MergedInto != "s1" {
			t.Fatalf("expected s3 auto-merged into s1: %+v", dMerged.Speakers.People)
		}
		if trimBeforeUnmerge {
			if n := e.PruneRecordings(now); n != 0 {
				t.Fatalf("PruneRecordings deleted %d, want 0 (trimmed)", n)
			}
			if _, err := os.Stat(e.Store.Path(KindRecording, recID, speakerTurnsFile)); !os.IsNotExist(err) {
				t.Fatalf("speaker_turns.json still present after trim: %v", err)
			}
		}
		// Unmerge s3 -> comes back unnamed (Speaker 3).
		if _, err := e.EditSpeakers(recID, 3, []SpeakerEditOp{{Op: SpeakerOpUnmerge, ID: "s3"}}, ""); err != nil {
			t.Fatalf("unmerge (trimmed=%v): %v", trimBeforeUnmerge, err)
		}
		dUnmerged, _ := e.Get(KindRecording, recID, 0)
		if dUnmerged.Speakers.People[2].MergedInto != "" || dUnmerged.Speakers.People[2].Name != "" {
			t.Fatalf("unmerged s3 should be unmerged and unnamed: %+v", dUnmerged.Speakers.People[2])
		}
		m, err := e.Store.ReadMinutes(recID)
		if err != nil {
			t.Fatal(err)
		}
		return m.Debates[0].Positions[0], m.Markdown
	}

	posBefore, mdBefore := runCase(false)
	posAfter, mdAfter := runCase(true)
	a, _ := json.Marshal(posBefore)
	b, _ := json.Marshal(posAfter)
	if string(a) != string(b) {
		t.Fatalf("unmerge attribution differs before vs after trim:\nbefore: %s\nafter:  %s", a, b)
	}
	if mdBefore != mdAfter {
		t.Fatalf("unmerge markdown differs before vs after trim:\nbefore:\n%s\nafter:\n%s", mdBefore, mdAfter)
	}
	if !strings.Contains(mdAfter, "(Ada, Speaker 3)") {
		t.Errorf("expected (Ada, Speaker 3) after unmerge:\n%s", mdAfter)
	}
}

// TestMinutesSpeakersConcurrentNameDuringGeneration verifies that naming speakers while the model call is in flight
// lands in the stored minutes.json and minutes.md without being overwritten.
func TestMinutesSpeakersConcurrentNameDuringGeneration(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := &funcModel{f: func(int, string, string) (string, error) {
		close(started)
		<-release
		return standardSpeakersDebateReply, nil
	}}
	e, _ := hubEngine(t, model)
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
	now := start.Add(time.Hour)
	setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)
	if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	wrote := 0
	go func() {
		defer wg.Done()
		wrote = e.WriteMinutes(context.Background(), 1)
	}()

	<-started
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()

	if wrote != 1 {
		t.Fatalf("WriteMinutes = %d, want 1", wrote)
	}
	m, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Markdown, "(Ada, Speaker 3)") || !strings.Contains(m.Markdown, "(Grace)") {
		t.Fatalf("concurrent name lost on storeMinutes:\n%s", m.Markdown)
	}
}

// TestMinutesSpeakersRegenerateRenameClearIsolationAndSafeguard verifies:
// - Name -> regenerate -> rename -> clear name updates positions and Markdown cleanly.
// - Owner-chosen names NEVER appear in points/bullets/actions who, transcript.md, highlights, or EarlierMaterial.
// - The 〔S…〕 safeguard strips leaked markers from every text field in minutes.json and minutes.md.
func TestMinutesSpeakersRegenerateRenameClearIsolationAndSafeguard(t *testing.T) {
	leakedReply := `{
		"title": "〔S1〕 Export cap review",
		"headline": "〔S1〕 Export cap counts per item",
		"conclusions": [{"text": "〔S2?〕 Count the cap **per item**", "line": 5}],
		"topics": [{
			"title": "〔S1〕 Export",
			"group": "〔S2〕 Release",
			"icon": "progress",
			"points": [{"text": "〔S1〕 Reviewed the export cap", "line": 1}],
			"decisions": [{"text": "〔S2〕 Count the cap **per item**", "line": 5}]
		}],
		"debates": [{
			"question": "〔S1〕 Count per item or per recording?",
			"topic": "〔S1〕 Export",
			"status": "settled",
			"positions": [
				{"stance": "〔S1〕 Count per item", "line": 2, "holder": "S1"},
				{"stance": "〔S2?〕 Count per recording", "line": 3, "holder": "S2"}
			],
			"outcome": {"text": "〔S1〕 Count per item", "line": 5}
		}],
		"actions": [{"who": "", "what": "〔S2〕 Write export regression test", "by": "", "line": 5}],
		"open": [{"text": "〔S3〕 Check archive speed", "line": 4}]
	}`
	model := &funcModel{f: func(int, string, string) (string, error) { return leakedReply, nil }}
	e, _ := hubEngine(t, model)
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
	now := start.Add(time.Hour)
	setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)
	if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now); err != nil {
		t.Fatal(err)
	}
	// Unique owner-chosen name never spoken in transcript or calendar.
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s1", Name: "Zelda"}}, ""); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 {
		t.Fatalf("initial WriteMinutes = %d", n)
	}
	// Regenerate minutes while named Zelda.
	if err := e.AskMinutes(recID); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 {
		t.Fatalf("regenerate WriteMinutes = %d", n)
	}
	m, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || !strings.Contains(m.Markdown, "(Zelda)") {
		t.Fatalf("after regenerate: v=%d md=\n%s", m.Version, m.Markdown)
	}
	// Safeguard: ZERO 〔S…〕 markers in minutes.json or minutes.md.
	rawMin := readFile(t, e, recID, "minutes.json")
	if strings.Contains(rawMin, "〔S") || strings.Contains(rawMin, "〔s") || strings.Contains(m.Markdown, "〔S") {
		t.Fatalf("leaked speaker marker survived safeguard:\njson=%s\nmd=%s", rawMin, m.Markdown)
	}
	if m.Title != "Export cap review" || m.Overview.Headline != "Export cap counts per item" ||
		m.Debates[0].Question != "Count per item or per recording?" || m.Debates[0].Positions[0].Stance != "Count per item" ||
		m.Debates[0].Outcome.Text != "Count per item" || m.Actions[0].What != "Write export regression test" ||
		m.Open[0].Text != "Check archive speed" {
		t.Errorf("safeguard mangled cleaned fields: title=%q overview=%+v debate=%+v action=%+v open=%+v",
			m.Title, m.Overview, m.Debates[0], m.Actions[0], m.Open[0])
	}

	// Isolation: owner-chosen name "Zelda" MUST NOT appear in non-debate who (points, overview bullets, actions),
	// transcript.md, highlights, or EarlierMaterial.
	for _, pt := range m.Points {
		if strings.Contains(pt.Who, "Zelda") {
			t.Errorf("owner name leaked into point.who: %+v", pt)
		}
	}
	if m.Overview != nil {
		for _, g := range m.Overview.Groups {
			for _, tp := range g.Topics {
				for _, b := range tp.Bullets {
					if strings.Contains(b.Who, "Zelda") {
						t.Errorf("owner name leaked into overview bullet.who: %+v", b)
					}
				}
			}
		}
	}
	for _, act := range m.Actions {
		if strings.Contains(act.Who, "Zelda") {
			t.Errorf("owner name leaked into action.who: %+v", act)
		}
	}
	if trMD := readFile(t, e, recID, "transcript.md"); strings.Contains(trMD, "Zelda") {
		t.Errorf("owner name leaked into transcript.md:\n%s", trMD)
	}
	if lv, _, err := e.LiveRead(recID); err == nil {
		lvJSON, _ := json.Marshal(lv)
		if strings.Contains(string(lvJSON), "Zelda") {
			t.Errorf("owner name leaked into live/highlights: %s", lvJSON)
		}
	}
	// EarlierMaterial for a later meeting must NOT contain "Zelda".
	e.EarlierMeetingsInAnswers = true
	if _, err := e.SetPrivate(recID, false); err != nil {
		t.Fatal(err)
	}
	laterStart := start.Add(24 * time.Hour)
	setupHubFinalMeeting(t, e, recID2, laterStart, laterStart.Add(time.Hour), "en-US", standardDebateSegments(), nil)
	earlier, err := e.EarlierMaterial(recID2, "export cap")
	if err != nil {
		t.Fatal(err)
	}
	earlierJSON, _ := json.Marshal(earlier)
	if len(earlier) == 0 || strings.Contains(string(earlierJSON), "Zelda") {
		t.Fatalf("EarlierMaterial empty or leaked owner name: %s", earlierJSON)
	}

	// Rename Zelda -> Yara, then clear name -> reverts to Speaker 1.
	if _, err := e.EditSpeakers(recID, 2, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s1", Name: "Yara"}}, ""); err != nil {
		t.Fatal(err)
	}
	mYara, _ := e.Store.ReadMinutes(recID)
	if !strings.Contains(mYara.Markdown, "(Yara)") || strings.Contains(mYara.Markdown, "Zelda") {
		t.Errorf("rename did not update markdown:\n%s", mYara.Markdown)
	}
	if _, err := e.EditSpeakers(recID, 3, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s1", Name: ""}}, ""); err != nil {
		t.Fatal(err)
	}
	mUnnamed, _ := e.Store.ReadMinutes(recID)
	if strings.Contains(mUnnamed.Markdown, "Yara") || !strings.Contains(mUnnamed.Markdown, "(Speaker 1)") {
		t.Errorf("clear name did not revert to Speaker 1:\n%s", mUnnamed.Markdown)
	}
}

// TestMinutesSpeakersLateTimelineAndConcurrent verifies:
//   - Timeline arriving AFTER minutes are written attributes positions immediately with zero extra model calls, zero new
//     pushes, and unchanged minutes.version (skipping cross-check 1 because said_by is absent).
//   - Timeline arriving DURING generation lands in the stored minutes with only one push.
func TestMinutesSpeakersLateTimelineAndConcurrent(t *testing.T) {
	noHolderReply := `{
		"title": "Export cap review",
		"points": [{"text": "Reviewed export cap", "line": 1}],
		"decisions": [{"text": "Count per item", "line": 5}],
		"debates": [{
			"question": "Per item or per recording?",
			"status": "settled",
			"positions": [
				{"stance": "Count per item", "line": 2},
				{"stance": "Count per recording", "line": 3}
			],
			"outcome": {"text": "Count per item", "line": 5}
		}]
	}`

	t.Run("after_minutes_written", func(t *testing.T) {
		model := &funcModel{f: func(int, string, string) (string, error) { return noHolderReply, nil }}
		e, _ := hubEngine(t, model)
		start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
		now := start.Add(time.Hour)
		setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)
		pushes := 0
		e.minutesWritten = func(context.Context, Recording, Minutes) { pushes++ }

		if n := e.WriteMinutes(context.Background(), 1); n != 1 {
			t.Fatalf("WriteMinutes = %d", n)
		}
		mBefore, _ := e.Store.ReadMinutes(recID)
		if mBefore.Debates[0].Positions[0].Span == nil || mBefore.Debates[0].Positions[0].Speaker != "" {
			t.Fatalf("before late timeline: %+v", mBefore.Debates[0].Positions[0])
		}
		if strings.Contains(mBefore.Markdown, "Speaker ") {
			t.Fatalf("markdown before late timeline should have no speaker:\n%s", mBefore.Markdown)
		}

		// Late timeline arrives -> immediate attribution, no model call, no push, version unchanged.
		if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if len(model.calls) != 1 || pushes != 1 {
			t.Fatalf("late timeline triggered model or push: calls=%d pushes=%d", len(model.calls), pushes)
		}
		mAfter, _ := e.Store.ReadMinutes(recID)
		if mAfter.Version != mBefore.Version {
			t.Errorf("version changed: %d -> %d", mBefore.Version, mAfter.Version)
		}
		if mAfter.Debates[0].Positions[0].Speaker != "s1" || mAfter.Debates[0].Positions[0].SpeakerUnsure ||
			mAfter.Debates[0].Positions[1].Speaker != "s2" || mAfter.Debates[0].Positions[1].SpeakerUnsure {
			t.Fatalf("late timeline attribution failed: %+v", mAfter.Debates[0].Positions)
		}
		if !strings.Contains(mAfter.Markdown, "(Speaker 1)") || !strings.Contains(mAfter.Markdown, "(Speaker 2)") {
			t.Errorf("late timeline markdown missing speakers:\n%s", mAfter.Markdown)
		}
	})

	t.Run("during_generation", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		model := &funcModel{f: func(int, string, string) (string, error) {
			close(started)
			<-release
			return noHolderReply, nil
		}}
		e, _ := hubEngine(t, model)
		start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
		now := start.Add(time.Hour)
		setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)
		pushes := 0
		e.minutesWritten = func(context.Context, Recording, Minutes) { pushes++ }

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.WriteMinutes(context.Background(), 1)
		}()

		<-started
		if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		close(release)
		wg.Wait()

		if pushes != 1 || len(model.calls) != 1 {
			t.Fatalf("concurrent late timeline pushes=%d calls=%d, want 1 and 1", pushes, len(model.calls))
		}
		m, _ := e.Store.ReadMinutes(recID)
		if m.Debates[0].Positions[0].Speaker != "s1" || m.Debates[0].Positions[0].SpeakerUnsure ||
			!strings.Contains(m.Markdown, "(Speaker 1)") {
			t.Fatalf("concurrent late timeline not reflected in stored minutes: pos=%+v md=\n%s",
				m.Debates[0].Positions[0], m.Markdown)
		}
	})
}

// TestMinutesSpeakersQualityLowAndRecompute verifies:
//   - When quality is low on import, the model sees no speaker numbers and minutes attribute no position to a speaker
//     (transcript who still shows).
//   - When replacing the transcript flips quality from ok to low, stored minutes drop speaker attribution immediately
//     without a model call.
func TestMinutesSpeakersQualityLowAndRecompute(t *testing.T) {
	t.Run("low_on_import", func(t *testing.T) {
		reply := `{
			"title": "Short review",
			"points": [{"text": "Reviewed export cap", "line": 1}],
			"debates": [{
				"question": "Per item or per recording?",
				"status": "open",
				"positions": [
					{"stance": "Count per item", "line": 1, "who": "Ada"},
					{"stance": "Count per recording", "line": 2}
				]
			}]
		}`
		model := &funcModel{f: func(int, string, string) (string, error) { return reply, nil }}
		e, _ := hubEngine(t, model)
		start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
		now := start.Add(time.Hour)
		segs := []RecordingSegment{
			{Start: 0, End: 15, Text: "Ada says count per item."},
			{Start: 16, End: 35, Text: "Count per recording instead."},
		}
		setupHubFinalMeeting(t, e, recID, start, now, "en-US", segs, &recordingwire.RecordingCalendar{Attendees: []string{"Ada"}})
		// Under 60s total speech -> quality is low (too_short).
		if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
			Run:     "low-1",
			Engine:  "test-engine 1.0",
			Source:  SpeakerSourceHub,
			Covered: []SpeakerSpan{{0, 60}},
			Turns: []RawSpeakerTurn{
				{Start: 0, End: 15, Cluster: "c1"},
				{Start: 16, End: 35, Cluster: "c2"},
			},
		}, now); err != nil {
			t.Fatal(err)
		}
		d, _ := e.Get(KindRecording, recID, 0)
		if d.Speakers.Quality != SpeakerQualityLow {
			t.Fatalf("expected low quality, got %q (%q)", d.Speakers.Quality, d.Speakers.Reason)
		}
		if n := e.WriteMinutes(context.Background(), 1); n != 1 {
			t.Fatalf("WriteMinutes = %d", n)
		}
		// Model must NOT see speaker numbers when quality is low.
		if strings.Contains(model.calls[0][1], "〔S") || !strings.Contains(model.calls[0][0], "没有说话人信息") {
			t.Fatalf("low-quality meeting exposed numbers to model:\nsys=%s\nuser=%s", model.calls[0][0], model.calls[0][1])
		}
		m, _ := e.Store.ReadMinutes(recID)
		if !m.Debates[0].Positions[0].SpeakerUnsure || !m.Debates[0].Positions[1].SpeakerUnsure {
			t.Fatalf("low quality positions should be unsure: %+v", m.Debates[0].Positions)
		}
		// who ("Ada") still shows on position 0; position 1 shows no speaker label.
		if !strings.Contains(m.Markdown, "Count per item (Ada)\n") || strings.Contains(m.Markdown, "Speaker ") {
			t.Errorf("low quality markdown wrong:\n%s", m.Markdown)
		}
	})

	t.Run("recompute_flips_ok_to_low", func(t *testing.T) {
		model := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
		e, _ := hubEngine(t, model)
		e.Logf = t.Logf
		start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
		now := start.Add(time.Hour)
		setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)
		if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now); err != nil {
			t.Fatal(err)
		}
		if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
			{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
			{Op: SpeakerOpName, ID: "s2", Name: "Grace"},
			{Op: SpeakerOpName, ID: "s3", Name: "Cy"},
		}, ""); err != nil {
			t.Fatal(err)
		}
		if n := e.WriteMinutes(context.Background(), 1); n != 1 {
			t.Fatalf("WriteMinutes = %d", n)
		}
		mOk, _ := e.Store.ReadMinutes(recID)
		if !strings.Contains(mOk.Markdown, "(Ada, Cy)") || !strings.Contains(mOk.Markdown, "(Grace)") {
			t.Fatalf("expected named speakers before recompute:\n%s", mOk.Markdown)
		}

		// Replace hub transcript with many unlabelled lines (outside turns) -> quality flips to low!
		badSegs := []RecordingSegment{
			{Start: 0, End: 25, Text: "Line 1"},
			{Start: 26, End: 52, Text: "Line 2"},
			{Start: 53, End: 80, Text: "Line 3"},
			{Start: 136, End: 140, Text: "Unlabelled line 4 inside covered"},
			{Start: 136, End: 140, Text: "Unlabelled line 5 inside covered"},
			{Start: 136, End: 140, Text: "Unlabelled line 6 inside covered"},
		}
		if _, err := e.HubTranscriptPutWithTiming(recID, HubPassFinal, TimingTurn, badSegs); err != nil {
			t.Fatal(err)
		}
		dAfter, _ := e.Get(KindRecording, recID, 0)
		if dAfter.Speakers.Quality != SpeakerQualityLow {
			t.Fatalf("expected quality low after transcript replacement, got %q (speakers=%+v segs=%+v)", dAfter.Speakers.Quality, dAfter.Speakers, dAfter.Segments)
		}
		if len(model.calls) != 1 {
			t.Fatalf("recompute called model: %d calls", len(model.calls))
		}
		mLow, _ := e.Store.ReadMinutes(recID)
		if mLow.Version != mOk.Version || !mLow.Debates[0].Positions[0].SpeakerUnsure || !mLow.Debates[0].Positions[1].SpeakerUnsure {
			t.Fatalf("minutes not downgraded on quality=low: v=%d pos=%+v", mLow.Version, mLow.Debates[0].Positions)
		}
		if strings.Contains(mLow.Markdown, "(Ada") || strings.Contains(mLow.Markdown, "(Grace") || strings.Contains(mLow.Markdown, "Speaker ") {
			t.Errorf("speaker attribution remained in markdown after quality=low:\n%s", mLow.Markdown)
		}
	})
}

// TestMinutesSpeakersAfterTrim verifies that after trimming removes the transcript and speaker_turns.json,
// speaker names survive and name/merge/unmerge/clear update minutes.json and minutes.md without reading any transcript.
func TestMinutesSpeakersAfterTrim(t *testing.T) {
	model := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
	e, _ := hubEngine(t, model)
	e.RecordingDaysFunc = func() int { return 14 }
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	oldStart := now.AddDate(0, 0, -20)
	setupHubFinalMeeting(t, e, recID, oldStart, oldStart.Add(time.Hour), "en-US", standardDebateSegments(), nil)
	if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), oldStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Grace"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 {
		t.Fatalf("WriteMinutes = %d", n)
	}
	if n := e.PruneRecordings(now); n != 0 {
		t.Fatalf("PruneRecordings deleted %d, want 0 (trimmed)", n)
	}
	for _, gone := range []string{"segments.json", "transcript.md", speakerTurnsFile} {
		if _, err := os.Stat(e.Store.Path(KindRecording, recID, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after trim: %v", gone, err)
		}
	}
	// Names survive trim.
	m0, _ := e.Store.ReadMinutes(recID)
	if !strings.Contains(m0.Markdown, "(Ada, Speaker 3)") || !strings.Contains(m0.Markdown, "(Grace)") {
		t.Fatalf("names did not survive trim:\n%s", m0.Markdown)
	}
	// Name s3 -> Cy.
	if _, err := e.EditSpeakers(recID, 2, []SpeakerEditOp{{Op: SpeakerOpName, ID: "s3", Name: "Cy"}}, ""); err != nil {
		t.Fatal(err)
	}
	m1, _ := e.Store.ReadMinutes(recID)
	if m1.Version != m0.Version || !strings.Contains(m1.Markdown, "(Ada, Cy)") {
		t.Fatalf("name after trim failed: v=%d md=\n%s", m1.Version, m1.Markdown)
	}
	// Merge s3 into s1.
	if _, err := e.EditSpeakers(recID, 3, []SpeakerEditOp{{Op: SpeakerOpMerge, From: "s3", Into: "s1"}}, ""); err != nil {
		t.Fatal(err)
	}
	m2, _ := e.Store.ReadMinutes(recID)
	if !strings.Contains(m2.Markdown, "block export (Ada)\n") || strings.Contains(m2.Markdown, "Cy") {
		t.Fatalf("merge after trim failed:\n%s", m2.Markdown)
	}
	// Unmerge s3.
	if _, err := e.EditSpeakers(recID, 4, []SpeakerEditOp{{Op: SpeakerOpUnmerge, ID: "s3"}}, ""); err != nil {
		t.Fatal(err)
	}
	m3, _ := e.Store.ReadMinutes(recID)
	if !strings.Contains(m3.Markdown, "(Ada, Speaker 3)") {
		t.Fatalf("unmerge after trim failed:\n%s", m3.Markdown)
	}
	// Clear removes all speaker attribution from minutes.json and minutes.md.
	if _, err := e.EditSpeakers(recID, 5, []SpeakerEditOp{{Op: SpeakerOpClear}}, ""); err != nil {
		t.Fatal(err)
	}
	m4, _ := e.Store.ReadMinutes(recID)
	if m4.Version != m0.Version || m4.Debates[0].Positions[0].Speaker != "" || m4.Debates[0].Positions[0].Span != nil ||
		len(m4.Debates[0].Positions[0].Also) != 0 || strings.Contains(m4.Markdown, "Ada") || strings.Contains(m4.Markdown, "Grace") {
		t.Fatalf("clear after trim left attribution: pos=%+v md=\n%s", m4.Debates[0].Positions[0], m4.Markdown)
	}
	if len(model.calls) != 1 {
		t.Fatalf("trimmed edits called model: %d", len(model.calls))
	}
}

// TestMinutesSpeakersWithoutTimelineNoNewFields verifies that device-mode meetings without a timeline write zero new
// speaker fields on debate positions and render identically in both overview and legacy layouts.
func TestMinutesSpeakersWithoutTimelineNoNewFields(t *testing.T) {
	reply := `{
		"title": "Export sync",
		"points": [{"text": "Reviewed cap", "line": 1}],
		"debates": [{
			"question": "Per item or per recording?",
			"status": "open",
			"positions": [
				{"stance": "Per item", "line": 1, "who": "Ada"},
				{"stance": "Per recording", "line": 2, "who": "Bo"}
			]
		}]
	}`
	e, m, _ := writeOne(t, reply)
	raw := readFile(t, e, recID, "minutes.json")
	for _, forbidden := range []string{`"span"`, `"est"`, `"over"`, `"said_by"`, `"speaker"`, `"speaker_unsure"`, `"also"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("device-mode minutes.json contains %s:\n%s", forbidden, raw)
		}
	}
	if want := renderMinutes(m, m.Start, m.Start.Location()); m.Markdown != want {
		t.Errorf("legacy markdown differs from no-timeline render:\ngot:\n%s\nwant:\n%s", m.Markdown, want)
	}
}

// TestMinutesSpeakersHoldsGate verifies the SpeakersHolds gate end-to-end:
// holds while waiting before deadline, releases on timeline import, writes failed/timeout at deadline without
// overwriting a just-stored ready, and does not extend across a restart.
func TestMinutesSpeakersHoldsGate(t *testing.T) {
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
	t0 := start.Add(time.Hour)

	setWaiting := func(t *testing.T, e *testEngine, id string, since time.Time, tries int) {
		t.Helper()
		if _, err := e.MeetingUpdate(id, func(m *Meeting, _ Recording) error {
			m.Speakers = &MeetingSpeakers{State: SpeakerStateWaiting, Since: since.UTC(), Tries: tries}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Holds before deadline, releases when timeline is imported, and fires wake hook.
	t.Run("holds_then_releases_on_import", func(t *testing.T) {
		model := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
		e, _ := hubEngine(t, model)
		now := t0
		e.SetClock(func() time.Time { return now })
		setupHubFinalMeeting(t, e, recID, start, t0, "en-US", standardDebateSegments(), nil)
		setWaiting(t, e, recID, t0, 1)

		woken := 0
		e.Writers.OnSpeakersChanged(func(string) { woken++ })

		now = t0.Add(5 * time.Minute)
		if n := e.WriteMinutes(context.Background(), 1); n != 0 || len(model.calls) != 0 {
			t.Fatalf("gate failed to hold: wrote=%d calls=%d", n, len(model.calls))
		}
		if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now); err != nil {
			t.Fatal(err)
		}
		if woken != 1 {
			t.Fatalf("OnSpeakersChanged wake count = %d, want 1", woken)
		}
		if n := e.WriteMinutes(context.Background(), 1); n != 1 || len(model.calls) != 1 {
			t.Fatalf("gate failed to release after ready: wrote=%d calls=%d", n, len(model.calls))
		}
		if !strings.Contains(model.calls[0][1], "〔S1〕") {
			t.Errorf("released minutes call lacked speaker numbers:\n%s", model.calls[0][1])
		}
	})

	// 2. At deadline writes failed/timeout, does not overwrite ready, and does not extend across restart.
	t.Run("timeout_no_overwrite_and_restart", func(t *testing.T) {
		dir := t.TempDir()
		newEng := func(clk *time.Time, m kb.Model) *testEngine {
			st, err := kb.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Rezone(goldenZone); err != nil {
				t.Fatal(err)
			}
			te := newTestEngine(&kb.Engine{Store: st, Model: m, Owner: "Owner", Logf: func(string, ...any) {}})
			te.TranscriptionReady = func() bool { return true }
			te.SetClock(func() time.Time { return *clk })
			return te
		}

		clk := t0
		model1 := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
		e1 := newEng(&clk, model1)
		setupHubFinalMeeting(t, e1, recID, start, t0, "en-US", standardDebateSegments(), nil)
		setWaiting(t, e1, recID, t0, 1)

		// Still held at +9m.
		clk = t0.Add(9 * time.Minute)
		if n := e1.WriteMinutes(context.Background(), 1); n != 0 {
			t.Fatalf("should still hold at +9m, wrote %d", n)
		}

		// Simulate restart at +10m: reopen fresh store & engine on same dir; deadline must NOT extend.
		clk = t0.Add(10 * time.Minute)
		model2 := &funcModel{f: func(int, string, string) (string, error) { return standardSpeakersDebateReply, nil }}
		e2 := newEng(&clk, model2)
		if n := e2.WriteMinutes(context.Background(), 1); n != 1 {
			t.Fatalf("after restart at deadline, wrote %d, want 1", n)
		}
		d, err := e2.Get(KindRecording, recID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if d.Speakers == nil || d.Speakers.State != SpeakerStateFailed || d.Speakers.Reason != SpeakerFailTimeout {
			t.Fatalf("expected failed/timeout after deadline, got %+v", d.Speakers)
		}
		if strings.Contains(model2.calls[0][1], "〔S") {
			t.Errorf("timed-out minutes call should not have speaker numbers:\n%s", model2.calls[0][1])
		}

		// expireSpeakersWaiting must NOT overwrite a ready state stored moments earlier.
		setupHubFinalMeeting(t, e2, recID2, start, t0, "en-US", standardDebateSegments(), nil)
		setWaiting(t, e2, recID2, t0, 1)
		if _, err := e2.ImportSpeakerTurns(recID2, standardDebateTurns(), t0.Add(9*time.Minute)); err != nil {
			t.Fatal(err)
		}
		updated, err := e2.eng().expireSpeakersWaiting(recID2, t0.UTC(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if updated == nil || updated.Speakers == nil || updated.Speakers.State != SpeakerStateReady {
			t.Fatalf("expireSpeakersWaiting overwrote ready state: %+v", updated)
		}
	})
}

// TestMinutesSpeakersLongMeetingChunked verifies the chunked two-part path with speaker numbers:
// chunkLines weights markers, holder and also enter merge notes, also lines join notedLines, cross-part disagreements
// keep full attribution, and a maximal valid answer stays well within the 6000-token cap.
func TestMinutesSpeakersLongMeetingChunked(t *testing.T) {
	// Verify chunkLines weights include the 〔S…〕 marker.
	plainLine := minutesLine{N: 1, At: 0, Text: "Short sentence"}
	taggedLine := minutesLine{N: 1, At: 0, Text: "Short sentence", Speaker: "s12", Unsure: true}
	if minutesLineWeight(taggedLine) <= minutesLineWeight(plainLine) {
		t.Fatalf("tagged weight %d should exceed plain weight %d", minutesLineWeight(taggedLine), minutesLineWeight(plainLine))
	}

	// Verify notedLines collects also lines even when no point/decision cites them.
	part := prompts.KBMinutesPart{
		Part: 1,
		Debates: []prompts.KBMinutesDebate{{
			Question: "Format?",
			Positions: []prompts.KBMinutesDebatePosition{{
				Line: 2, Speaker: "S1", Holder: "S1", Stance: "Zip",
				Also: []prompts.KBMinutesDebateAlso{{Line: 3, Speaker: "S3", Holder: "S3"}},
			}},
		}},
	}
	kept := notedLines([]minutesLine{{N: 1}, {N: 2}, {N: 3}, {N: 4}}, []prompts.KBMinutesPart{part})
	if len(kept) != 2 || kept[0].N != 2 || kept[1].N != 3 {
		t.Fatalf("notedLines did not collect also line 3: %+v", kept)
	}

	// End-to-end forced 2-part meeting with cross-part debate + also + attribution.
	model := &funcModel{f: func(n int, system, user string) (string, error) {
		switch n {
		case 1: // part 1 (lines 1..4)
			return `{"title":"Architecture review","points":[{"text":"Reviewed backup format","line":1}],
				"debates":[{"question":"Zip or plain folders?","topic":"Backups","status":"open",
					"positions":[{"stance":"Use zip archives","line":2,"holder":"S1","also":[{"line":3,"holder":"S2"}]}]}]}`, nil
		case 2: // part 2 (lines 5..7)
			return `{"title":"Architecture review","points":[{"text":"Checked sync latency","line":5}],
				"decisions":[{"text":"Use zip archives","line":7}],
				"debates":[{"question":"Zip or plain folders?","topic":"Backups","status":"settled",
					"positions":[{"stance":"Use plain folders","line":6,"holder":"S3"}],
					"outcome":{"text":"Use zip archives","line":7}}]}`, nil
		case 3: // merge
			return `{"title":"Architecture review","headline":"Backups use zip archives",
				"conclusions":[{"text":"Use **zip archives**","line":7}],
				"topics":[{"title":"Backups","icon":"tooling",
					"points":[{"text":"Reviewed backup format","line":1},{"text":"Checked sync latency","line":5}],
					"decisions":[{"text":"Use **zip archives**","line":7}]}],
				"debates":[{"question":"Zip or plain folders?","topic":"Backups","status":"settled",
					"positions":[
						{"stance":"Use zip archives","line":2,"holder":"S1","also":[{"line":3,"holder":"S2"}]},
						{"stance":"Use plain folders","line":6,"holder":"S3"}
					],
					"outcome":{"text":"Use zip archives","line":7}}]}`, nil
		default:
			return "", fmt.Errorf("unexpected call %d", n)
		}
	}}
	e, _ := hubEngine(t, model)
	e.minutesCall, e.minutesPart = 350, 350
	start := time.Date(2026, 11, 20, 14, 0, 0, 0, goldenZone)
	now := start.Add(time.Hour)
	segs := []RecordingSegment{
		{Start: 0, End: 25, Text: "First let us review the backup storage format today."},
		{Start: 26, End: 52, Text: "Using zip archives saves disk space and speeds transfers."},
		{Start: 53, End: 80, Text: "I agree, zip archives are much better for storage."},
		{Start: 81, End: 105, Text: "Next we check staging search latency numbers carefully."},
		{Start: 106, End: 130, Text: "Sync latency stayed under twenty milliseconds all week."},
		{Start: 131, End: 158, Text: "Plain folders are better because incremental sync is easy."},
		{Start: 159, End: 185, Text: "Decided to use zip archives for backups this release."},
	}
	setupHubFinalMeeting(t, e, recID, start, now, "en-US", segs, nil)
	if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
		Run:     "chunk-run",
		Engine:  "test-engine 1.0",
		Source:  SpeakerSourceHub,
		Covered: []SpeakerSpan{{0, 190}},
		Turns: []RawSpeakerTurn{
			{Start: 0, End: 52, Cluster: "c1"},    // s1 (lines 1, 2)
			{Start: 53, End: 80, Cluster: "c2"},   // s2 (line 3 -> also)
			{Start: 81, End: 105, Cluster: "c1"},  // s1 (line 4)
			{Start: 106, End: 158, Cluster: "c3"}, // s3 (lines 5, 6 -> opposing side)
			{Start: 159, End: 185, Cluster: "c1"}, // s1 (line 7)
		},
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EditSpeakers(recID, 1, []SpeakerEditOp{
		{Op: SpeakerOpName, ID: "s1", Name: "Ada"},
		{Op: SpeakerOpName, ID: "s2", Name: "Cy"},
		{Op: SpeakerOpName, ID: "s3", Name: "Grace"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 || len(model.calls) != 3 {
		t.Fatalf("wrote=%d calls=%d, want 1 and 3", n, len(model.calls))
	}
	mergeUser := model.calls[2][1]
	if !strings.Contains(mergeUser, "(holder: S1)") || !strings.Contains(mergeUser, "[also: [3] 〔S2〕 (holder: S2)]") ||
		!strings.Contains(mergeUser, "(holder: S3)") {
		t.Fatalf("merge input missing holder/also:\n%s", mergeUser)
	}
	m, _ := e.Store.ReadMinutes(recID)
	if len(m.Debates) != 1 || len(m.Debates[0].Positions) != 2 {
		t.Fatalf("chunked debates: %+v", m.Debates)
	}
	p0, p1 := m.Debates[0].Positions[0], m.Debates[0].Positions[1]
	if p0.Speaker != "s1" || p0.SpeakerUnsure || len(p0.Also) != 1 || p0.Also[0].Speaker != "s2" || p0.Also[0].SpeakerUnsure ||
		p1.Speaker != "s3" || p1.SpeakerUnsure {
		t.Fatalf("chunked attribution mismatch: p0=%+v p1=%+v", p0, p1)
	}
	if !strings.Contains(m.Markdown, "(Ada, Cy)") || !strings.Contains(m.Markdown, "(Grace)") {
		t.Errorf("chunked markdown mismatch:\n%s", m.Markdown)
	}

	// Maximal valid answer with holder + 2 also entries on all 6x4 debate positions stays within the 6000-token cap.
	var maxTopics, maxDebates []string
	for i := range 12 {
		maxTopics = append(maxTopics, fmt.Sprintf(
			`{"title":"Topic %02d","group":"Group %d","icon":"progress","points":[{"text":"Point %d about release schedule","line":1},{"text":"Second point %d on rollout","line":2}],"decisions":[{"text":"Decision %d kept","line":7}]}`,
			i+1, (i%3)+1, i+1, i+1, i+1))
	}
	for i := range minutesDebatesMax {
		maxDebates = append(maxDebates, fmt.Sprintf(
			`{"question":"Policy question %d?","topic":"Topic 01","status":"settled","positions":[`+
				`{"stance":"Position A on policy %d","line":1,"holder":"S1","also":[{"line":2,"holder":"S3"},{"line":3,"holder":"S4"}]},`+
				`{"stance":"Position B on policy %d","line":4,"holder":"S2","also":[{"line":5,"holder":"S5"},{"line":6,"holder":"S6"}]},`+
				`{"stance":"Position C on policy %d","line":2,"holder":"S3","also":[{"line":3,"holder":"S4"}]},`+
				`{"stance":"Position D on policy %d","line":5,"holder":"S5"}`+
				`],"outcome":{"text":"Adopted position A for release","line":7}}`, i+1, i+1, i+1, i+1, i+1))
	}
	maxReply := `{"title":"Quarterly review","where":"","attendees":["Ada","Grace"],"headline":"Adopted policies across twelve topics",` +
		`"conclusions":[{"text":"Policy **A** adopted","line":7},{"text":"Keep **4 MB** cap","line":1},{"text":"Sync **<20 ms**","line":5},{"text":"Rollout **Monday**","line":2}],` +
		`"topics":[` + strings.Join(maxTopics, ",") + `],"debates":[` + strings.Join(maxDebates, ",") + `],` +
		`"actions":[{"who":"","what":"Add regression tests","by":"Friday","due_date":"2026-11-27","line":7}],` +
		`"open":[{"text":"Check archive speed next quarter","line":4}]}`
	var parsed minutesAnswer
	if err := json.Unmarshal([]byte(maxReply), &parsed); err != nil {
		t.Fatalf("maximal reply unmarshal: %v", err)
	}
	if len(maxReply) >= 16000 {
		t.Errorf("maximal valid answer with holder+also is %d bytes (too close to 6000-token cap)", len(maxReply))
	}
	t.Logf("maximal valid answer with holder+also: %d bytes (~%d tokens at 3 chars/token, cap 6000)",
		len(maxReply), (len(maxReply)+2)/3)
}

// TestMinutesSpeakersMalformedHolderAlso verifies that malformed holder and also entries in the model answer are
// dropped cleanly without preventing minutes from being written.
func TestMinutesSpeakersMalformedHolderAlso(t *testing.T) {
	badReply := `{
		"title": "Export cap review",
		"points": [{"text": "Reviewed export cap", "line": 1}],
		"debates": [{
			"question": "Per item or per recording?",
			"status": "open",
			"positions": [
				{"stance": "Count per item", "line": 2, "holder": "Speaker 1", "also": ["bad", null, {"line": 999, "holder": "S3"}, {"line": 2, "holder": "S1"}, {"line": 4, "holder": "not-a-speaker"}, {"line": 5, "holder": "S1?"}, {"line": 1, "holder": "S1"}]},
				{"stance": "Count per recording", "line": 3, "holder": 42, "also": "not-a-list"}
			]
		}]
	}`
	model := &funcModel{f: func(int, string, string) (string, error) { return badReply, nil }}
	e, _ := hubEngine(t, model)
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
	now := start.Add(time.Hour)
	setupHubFinalMeeting(t, e, recID, start, now, "en-US", standardDebateSegments(), nil)
	if _, err := e.ImportSpeakerTurns(recID, standardDebateTurns(), now); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteMinutes(context.Background(), 1); n != 1 {
		t.Fatalf("WriteMinutes failed on malformed holder/also: %d", n)
	}
	m, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Debates) != 1 || len(m.Debates[0].Positions) != 2 {
		t.Fatalf("debates dropped: %+v", m.Debates)
	}
	p0, p1 := m.Debates[0].Positions[0], m.Debates[0].Positions[1]
	if p0.SaidBy != "" || p1.SaidBy != "" || len(p1.Also) != 0 {
		t.Errorf("malformed holder/also kept: p0=%+v p1=%+v", p0, p1)
	}
	// p0.Also kept only 2 valid distinct lines (line 4 and line 5; duplicate line 2, invalid line 999, and 3rd entry line 1 dropped).
	if len(p0.Also) != 2 || p0.Also[0].At != 81 || p0.Also[0].SaidBy != "" || p0.Also[1].At != 109 || p0.Also[1].SaidBy != "s1" {
		t.Errorf("p0.Also unexpected: %+v", p0.Also)
	}
}

// TestMinutesSpeakersCrossChecksAndMarkdownLanguages verifies all three attribution cross-checks, the Phase 1 no-span
// rule, and the four-row display table in Markdown across en, zh-Hans, and ja (both overview and legacy layouts).
func TestMinutesSpeakersCrossChecksAndMarkdownLanguages(t *testing.T) {
	e, _ := hubEngine(t, nil)
	turns := &SpeakerTurnsFile{
		Covered: []SpeakerSpan{{0, 140}},
		Turns: []SpeakerTurn{
			{Start: 0, End: 30, Speaker: "s1"},
			{Start: 31, End: 60, Speaker: "s2"},
			{Start: 61, End: 90, Speaker: "s3"},
			{Start: 91, End: 120, Speaker: "s4"},
		},
	}
	speakers := &MeetingSpeakers{
		State:   SpeakerStateReady,
		Quality: SpeakerQualityOk,
		Next:    5,
		People: []SpeakerPerson{
			{ID: "s1", Name: "Ada"},
			{ID: "s2"},
			{ID: "s3", MergedInto: "s1"},
			{ID: "s4", Name: "Grace"},
		},
	}

	sp1, sp2, sp3, sp4 := SpeakerSpan{5, 25}, SpeakerSpan{35, 55}, SpeakerSpan{65, 85}, SpeakerSpan{95, 115}
	m := &Minutes{
		Title: "Cross-check test",
		Debates: []MinutesDebate{
			// 1a. Cross-check 1: said_by mismatch (s2 != s1) -> unsure; matching s2 stays sure.
			{
				Question: "Check 1 mismatch", Status: DebateOpen, At: 5,
				Positions: []MinutesDebatePosition{
					{Stance: "Mismatch", At: 5, Span: &sp1, SaidBy: "s2"},
					{Stance: "Match", At: 35, Span: &sp2, SaidBy: "s2"},
				},
			},
			// 1b. Cross-check 1: said_by s3 merged into s1 matches computed canonical s1 -> sure.
			{
				Question: "Check 1 merged", Status: DebateOpen, At: 35,
				Positions: []MinutesDebatePosition{
					{Stance: "Other side", At: 35, Span: &sp2, SaidBy: "s2"},
					{Stance: "Merged match", At: 65, Span: &sp3, SaidBy: "s3"},
				},
			},
			// 2. Cross-check 2: same canonical person (s1 and s3->s1) on two positions (via also!) -> both unsure.
			{
				Question: "Check 2", Status: DebateOpen, At: 5,
				Positions: []MinutesDebatePosition{
					{Stance: "Side A", At: 5, Span: &sp1},
					{Stance: "Side B", At: 35, Span: &sp2, Also: []MinutesDebateAlso{{At: 65, Span: &sp3}}},
				},
			},
			// 3. Cross-check 3: who="Bo" disagrees with owner name "Ada" -> unsure; who="grace" matches "Grace" -> sure.
			{
				Question: "Check 3", Status: DebateOpen, At: 5,
				Positions: []MinutesDebatePosition{
					{Stance: "Who conflict", At: 5, Span: &sp1, Who: "Bo"},
					{Stance: "Who case match", At: 95, Span: &sp4, Who: "grace"},
				},
			},
			// 4. Phase 1 position without span stays untouched.
			{
				Question: "Phase 1", Status: DebateOpen, At: 5,
				Positions: []MinutesDebatePosition{
					{Stance: "Legacy A", At: 5, Who: "Ada"},
					{Stance: "Legacy B", At: 35},
				},
			},
		},
	}
	e.eng().attributeDebates(m, speakers, turns, TimingTurn, false)

	// Check 1a assertions.
	if m.Debates[0].Positions[0].Speaker != "s1" || !m.Debates[0].Positions[0].SpeakerUnsure ||
		m.Debates[0].Positions[1].Speaker != "s2" || m.Debates[0].Positions[1].SpeakerUnsure {
		t.Errorf("check 1 mismatch: %+v", m.Debates[0].Positions)
	}
	// Check 1b assertions.
	if m.Debates[1].Positions[1].Speaker != "s1" || m.Debates[1].Positions[1].SpeakerUnsure {
		t.Errorf("check 1 merged match should be sure: %+v", m.Debates[1].Positions[1])
	}
	// Check 2 assertions: s1 appears on pos 0 and via pos 1's also -> both s1 entries unsure, pos 1's s2 still sure.
	if !m.Debates[2].Positions[0].SpeakerUnsure || m.Debates[2].Positions[1].SpeakerUnsure ||
		!m.Debates[2].Positions[1].Also[0].SpeakerUnsure {
		t.Errorf("check 2 same person across positions: %+v", m.Debates[2].Positions)
	}
	// Check 3 assertions.
	if !m.Debates[3].Positions[0].SpeakerUnsure || m.Debates[3].Positions[1].SpeakerUnsure {
		t.Errorf("check 3 who mismatch/match: %+v", m.Debates[3].Positions)
	}
	// Phase 1 untouched.
	if m.Debates[4].Positions[0].Speaker != "" || m.Debates[4].Positions[0].Over != nil {
		t.Errorf("phase 1 position should stay untouched: %+v", m.Debates[4].Positions[0])
	}

	// Four-row display table across en, zh-Hans, ja in both overview and legacy layouts.
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, goldenZone)
	mdSpeakers := &MeetingSpeakers{
		State:   SpeakerStateReady,
		Quality: SpeakerQualityOk,
		Next:    4,
		People:  []SpeakerPerson{{ID: "s1", Name: "Ada"}, {ID: "s2"}, {ID: "s3"}},
	}
	baseDebates := []MinutesDebate{{
		Question: "Which policy?", Status: DebateOpen, At: 10,
		Positions: []MinutesDebatePosition{
			{Stance: "Row1 named+also", At: 10, Speaker: "s1", Also: []MinutesDebateAlso{{At: 12, Speaker: "s2"}}},
			{Stance: "Row2 unnamed", At: 20, Speaker: "s2"},
			{Stance: "Row3 unsure+who", At: 30, Speaker: "s1", SpeakerUnsure: true, Who: "Bo"},
			{Stance: "Row4 unsure-no-who", At: 40, Speaker: "s1", SpeakerUnsure: true},
		},
	}}
	for _, tc := range []struct {
		lang i18n.Lang
		want []string
	}{
		{i18n.En, []string{"Row1 named+also (Ada, Speaker 2)\n", "Row2 unnamed (Speaker 2)\n", "Row3 unsure+who (Bo)\n", "Row4 unsure-no-who\n"}},
		{i18n.ZhHans, []string{"Row1 named+also（Ada、发言人 2）\n", "Row2 unnamed（发言人 2）\n", "Row3 unsure+who（Bo）\n", "Row4 unsure-no-who\n"}},
		{i18n.Ja, []string{"Row1 named+also（Ada、話者 2）\n", "Row2 unnamed（話者 2）\n", "Row3 unsure+who（Bo）\n", "Row4 unsure-no-who\n"}},
	} {
		for _, overview := range []bool{false, true} {
			mm := &Minutes{Title: "Display", When: "2026-11-20 10:00–10:10", Language: string(tc.lang), Debates: baseDebates}
			if overview {
				mm.Overview = &MinutesOverview{Groups: []MinutesGroup{{Topics: []MinutesTopic{{Title: "T", Bullets: []MinutesBullet{{Text: "P", At: 10}}}}}}}
			}
			out := e.eng().renderMinutes(mm, start, goldenZone, mdSpeakers) + "\n"
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("lang=%s overview=%v missing %q in:\n%s", tc.lang, overview, w, out)
				}
			}
		}
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
