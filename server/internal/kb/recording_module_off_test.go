package kb

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// The owner's rule for a module that is off (docs/architecture/core-and-modules.md "A module that is off keeps what
// the user has"; docs/plans/plan-kb-recording-port.md): what the recording module wrote stays listed, readable and
// searchable, byte for byte, and only its writing and its rounds are gone. A knowledge base without the module is one
// whose engine was never given a provider (the module's hook does that): these tests open one on the files a
// knowledge base with the module wrote.

// recordingFiles reads every file under the data directory's recordings/, by its path there.
func recordingFilesOnDisk(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(dir, KindRecording)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// writtenMinutes leaves recording id as the module's minutes round leaves it: minutes.json and minutes.md of its
// current revision, and meta.json saying they are ready.
func writtenMinutes(t *testing.T, e *Engine, id string) {
	t.Helper()
	r := recordingNow(t, e, id)
	m := Minutes{Version: 1, Revision: r.Revision, Title: "发布评审", When: "2026-11-10 14:00", Where: "三号会议室",
		Points:    []MinutesPoint{{Text: "导出超过 4 MB 的条目会被丢掉", At: 10, Who: "Ada"}},
		Decisions: []MinutesItem{{Text: "这一版先保留上限", At: 20}},
		Actions:   []MinutesAction{{ID: "a1", Who: "Bo", What: "补上回归测试", By: "周五前", At: 30}},
		Open:      []MinutesItem{{Text: "上限按条还是按录音", At: 40}}}
	m.Markdown = "# 发布评审\n\n## 要点\n\n- [14:00:10] 导出超过 4 MB 的条目会被丢掉（Ada）\n\n## 决定\n\n- [14:00:20] 这一版先保留上限\n\n" +
		"## 行动项\n\n- [14:00:30] Bo：补上回归测试（周五前）\n\n## 未决问题\n\n- [14:00:40] 上限按条还是按录音"
	putMeetingMinutes(t, e, id, m)
	changeRecording(t, e, id, func(r *Recording) {
		r.MinutesVersion, r.MinutesRev = 1, r.Revision
		planMinutes(r)
	})
}

// moduleOffFixture is a knowledge base the recording module wrote into, then opened again without the module: recID
// is a meeting with its transcript and minutes, recID2 a meeting whose minutes were still to be written when the
// module went. on is the engine with the module, off the one without it, on a store of its own over the same files.
func moduleOffFixture(t *testing.T) (on, off *Engine, m *captureModel, now time.Time) {
	t.Helper()
	m = &captureModel{reply: minutesReply}
	on = goldenEngine(t, m)
	on.SetRecordingProvider(hookedWriters(on))
	now = time.Date(2026, 11, 10, 16, 0, 0, 0, goldenZone)
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	putRecording(t, on, recID, meetingUpload(1, start, "大家好，今天评审导出", "Ada 说导出超过 4 MB 的条目会被丢掉", "那这一版先保留上限", "Bo 你周五前补上回归测试", "上限按条还是按录音算还没定"), now)
	writtenMinutes(t, on, recID)
	putRecording(t, on, recID2, meetingUpload(1, start.Add(time.Hour), "第二个会议讨论排期", "下周三之前给出方案"), now)
	if r := recordingNow(t, on, recID2); r.MinutesState != MinutesPending {
		t.Fatalf("second meeting's minutes are %q, want pending", r.MinutesState)
	}
	m.calls = nil
	st, err := NewStore(on.Store.Dir) // the hub started again, without the module
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rezone(goldenZone); err != nil {
		t.Fatal(err)
	}
	off = &Engine{Store: st, Model: m, Owner: on.Owner, Logf: func(string, ...any) {}}
	return on, off, m, now
}

func TestRecordingsStayReadableWithoutTheModule(t *testing.T) {
	on, off, m, now := moduleOffFixture(t)
	before := recordingFilesOnDisk(t, on.Store.Dir)
	for _, f := range []string{"meta.json", "transcript.md", "minutes.json", "minutes.md"} {
		if before[filepath.Join(recID, f)] == "" {
			t.Fatalf("the fixture has no %s", f)
		}
	}
	if _, ok := off.RecordingProvider().(recordingwire.NoProvider); !ok {
		t.Fatalf("without the module the provider is %T, want the no-op", off.RecordingProvider())
	}

	// Listed.
	var ids []string
	for _, en := range off.List(KindRecording, "") {
		ids = append(ids, en.ID)
	}
	if len(ids) != 2 || !strings.Contains(strings.Join(ids, " "), recID) || !strings.Contains(strings.Join(ids, " "), recID2) {
		t.Errorf("listed %v, want both recordings", ids)
	}
	// Opened: the detail is what the engine with the module answers, field for field.
	want, err := on.Get(KindRecording, recID, 20000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := off.Get(KindRecording, recID, 20000)
	if err != nil {
		t.Fatalf("detail without the module: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		t.Errorf("detail differs without the module:\n with    %s\n without %s", a, b)
	}
	if len(got.Segments) != 5 || len(got.Minutes) == 0 || !strings.Contains(got.Recent, "Ada 说导出超过 4 MB") || got.Paused != "" {
		t.Errorf("detail: %d segments, %d bytes of minutes, recent %q, paused %q", len(got.Segments), len(got.Minutes), got.Recent, got.Paused)
	}
	mm, err := off.Store.ReadMinutes(recID)
	if err != nil || mm.Title != "发布评审" {
		t.Errorf("minutes without the module: %+v, %v", mm, err)
	}
	// Found by search: a line of the transcript and a line of the minutes.
	for _, q := range []string{"保留上限", "补上回归测试"} {
		var files []string
		for _, h := range off.Search(q, false, 20) {
			files = append(files, h.Path)
		}
		if !strings.Contains(strings.Join(files, " "), filepath.Join(KindRecording, recID)) {
			t.Errorf("search %q without the module found %v, want the recording", q, files)
		}
	}
	// The module's rounds and its part of retention do nothing: no model call, no file touched.
	ctx := context.Background()
	p := off.RecordingProvider()
	if n := p.WriteMinutes(ctx, 2) + p.TitleRecordings(ctx, 2) + p.WriteDayRecaps(ctx, 1) + p.SummarizeRecordings(ctx, 3); n != 0 {
		t.Errorf("the rounds wrote %d without the module", n)
	}
	p.AfterPrune(now)
	p.AudioDropped(recID, ErrRecordingDeleted)
	p.AudioClosed(recID)
	if n := off.PruneRecordings(now); n != 0 { // within recording_days: nothing is due
		t.Errorf("pruned %d within recording_days", n)
	}
	if len(m.calls) != 0 {
		t.Errorf("%d model calls without the module", len(m.calls))
	}
	if after := recordingFilesOnDisk(t, off.Store.Dir); !reflect.DeepEqual(after, before) {
		for name, raw := range before {
			if after[name] != raw {
				t.Errorf("%s changed without the module", name)
			}
		}
		for name := range after {
			if _, ok := before[name]; !ok {
				t.Errorf("%s was written without the module", name)
			}
		}
	}
}

// What was in flight when the module went is said in the response and never written: the stored state stays
// pending, and the module takes it up from there when it is back.
func TestRecordingInFlightIsPausedWithoutTheModule(t *testing.T) {
	on, off, _, _ := moduleOffFixture(t)
	before := recordingFilesOnDisk(t, on.Store.Dir)
	paused := func(e *Engine) (detail string, list map[string]string) {
		d, err := e.Get(KindRecording, recID2, 0)
		if err != nil {
			t.Fatal(err)
		}
		list = map[string]string{}
		for _, en := range e.List(KindRecording, "") {
			list[en.ID] = en.Paused
		}
		return d.Paused, list
	}
	if d, list := paused(off); d != PausedModuleOff || list[recID2] != PausedModuleOff || list[recID] != "" {
		t.Errorf("without the module: detail paused %q, list %v; want the pending one paused, the written one not", d, list)
	}
	if d, list := paused(on); d != "" || list[recID2] != "" {
		t.Errorf("with the module: detail paused %q, list %v; want none", d, list)
	}
	if r := recordingNow(t, off, recID2); r.MinutesState != MinutesPending {
		t.Errorf("the stored state became %q", r.MinutesState)
	}
	raw, _ := os.ReadFile(off.Store.Path(KindRecording, recID2, "meta.json"))
	if strings.Contains(string(raw), "paused") {
		t.Errorf("paused was stored: %s", raw)
	}
	if after := recordingFilesOnDisk(t, off.Store.Dir); !reflect.DeepEqual(after, before) {
		t.Error("reading the paused recording changed its files")
	}
	// An upload that never completed is in flight too; a read-only knowledge base says nothing (nothing settles there
	// with the module either).
	r := recordingNow(t, off, recID)
	r.Complete = false
	if got := off.recordingPaused(r); got != PausedModuleOff {
		t.Errorf("an incomplete upload: paused %q", got)
	}
	ro, err := OpenReadOnly(off.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := (&Engine{Store: ro}).Get(KindRecording, recID2, 0); err != nil || d.Paused != "" {
		t.Errorf("a read-only knowledge base: paused %q, %v", d.Paused, err)
	}
	// The module is back: nothing is paused, and the stored state is still the one its round takes up (pending; that
	// the round then writes the minutes is the module's test, TestMinutesResumeWhenTheModuleIsBack).
	back := &Engine{Store: off.Store, Model: off.Model, Owner: off.Owner, Logf: off.Logf}
	back.SetRecordingProvider(hookedWriters(back))
	if d, list := paused(back); d != "" || list[recID2] != "" || recordingNow(t, back, recID2).MinutesState != MinutesPending {
		t.Errorf("back with the module: paused %q %v, state %q", d, list, recordingNow(t, back, recID2).MinutesState)
	}
}

// Delete is the user's own action and retention the user's own setting: both are the knowledge base's and work
// without the module (retention: RetentionWithoutRecordingModule).
func TestRecordingDeleteAndRetentionWithoutTheModule(t *testing.T) {
	_, off, m, now := moduleOffFixture(t)
	var unindexed []string
	off.Unindex = func(_ string, ids []string) { unindexed = append(unindexed, ids...) }
	if !off.retentionRuns() {
		t.Fatal("retention does not run without the module")
	}
	// Past recording_days: the meeting with minutes is trimmed to them, the one without goes.
	later := now.AddDate(0, 0, DefaultRecordingDays+1)
	if n := off.PruneRecordings(later); n != 1 {
		t.Errorf("retention removed %d recordings without the module, want 1", n)
	}
	if _, ok := off.Store.Recording(recID2); ok {
		t.Error("the recording without minutes outlived recording_days")
	}
	r, ok := off.Store.Recording(recID)
	if !ok || !r.Trimmed() {
		t.Fatalf("the recording with minutes: there %v, trimmed %v", ok, ok && r.Trimmed())
	}
	if _, err := os.Stat(off.Store.Path(KindRecording, recID, "transcript.md")); !os.IsNotExist(err) {
		t.Errorf("the trimmed recording kept its transcript: %v", err)
	}
	if mm, err := off.Store.ReadMinutes(recID); err != nil || mm.Title != "发布评审" {
		t.Errorf("the trimmed recording's minutes: %+v, %v", mm, err)
	}
	// Deleted by the user.
	if deleted, err := off.DeleteRecording(recID, later); err != nil || !deleted {
		t.Fatalf("delete without the module: %v, %v", deleted, err)
	}
	if _, ok := off.Store.Recording(recID); ok || len(off.List(KindRecording, "")) != 0 {
		t.Error("the deleted recording is still there")
	}
	if _, err := off.Get(KindRecording, recID, 0); err != ErrNotFound {
		t.Errorf("detail of the deleted recording: %v", err)
	}
	if _, ok := off.Store.tombstones()[recID]; !ok {
		t.Error("the delete left no tombstone")
	}
	if strings.Join(unindexed, " ") != recID2+" "+recID {
		t.Errorf("unindexed %v", unindexed)
	}
	if again, err := off.DeleteRecording(recID, later); err != nil || again {
		t.Errorf("a second delete: %v, %v", again, err)
	}
	if len(m.calls) != 0 {
		t.Errorf("%d model calls without the module", len(m.calls))
	}
}

// The no-op provider is a value: a knowledge base that was never hooked, on a store that was never hooked, answers
// it and its audio side, never nil.
func TestRecordingProviderIsNeverNil(t *testing.T) {
	e := goldenEngine(t, nil)
	if e.RecordingProvider() == nil || e.Store.recordingAudio() == nil {
		t.Fatal("a nil provider")
	}
	e.Store.recordingAudio().AudioDropped(recID, ErrNotFound)
	e.Store.recordingAudio().AudioClosed(recID)
	e.SetRecordingProvider(hookedWriters(e))
	if _, ok := e.Store.recordingAudio().(hookedProvider); !ok {
		t.Errorf("the store's audio side is %T after the module's hook", e.Store.recordingAudio())
	}
}

// hookedProvider stands for the recording module's provider in these tests (the module's own is
// internal/modules/recording/writers, which this package cannot import): a knowledge base that was hooked, whose
// summary round is this package's and whose other rounds write nothing here.
type hookedProvider struct {
	recordingwire.NoProvider
	rest recordingwire.WritersInCore
}

func (p hookedProvider) SummarizeRecordings(ctx context.Context, max int) int {
	return p.rest.SummarizeRecordings(ctx, max)
}

func hookedWriters(e *Engine) recordingwire.Provider {
	return hookedProvider{rest: RecordingWriters(e)}
}

// A hub-transcribed meeting with a speaker timeline and owner-chosen names remains fully readable (detail.speakers,
// speaker names, and segments[].speaker) when the recording module is off, without changing any file on disk (#17674).
func TestRecordingSpeakersStayReadableWithoutTheModule(t *testing.T) {
	on := goldenEngine(t, nil)
	on.SetRecordingProvider(hookedWriters(on))
	now := time.Date(2026, 11, 10, 16, 0, 0, 0, goldenZone)
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)

	u := meetingUpload(1, start,
		"Ada explained the storage contract for speaker timelines.",
		"Grace confirmed the module-off read path stays in Core.")
	u.Duration = 90
	u.Segments[0].Start, u.Segments[0].End = 0, 35
	u.Segments[1].Start, u.Segments[1].End = 36, 72
	putRecording(t, on, recID, u, now)

	// Write meeting.json (hub transcription + speakers), hub_segments.json (timing: turn), and speaker_turns.json.
	meeting := Meeting{
		Transcription: &recordingwire.MeetingTranscription{Mode: recordingwire.TranscriptionHub, State: recordingwire.TranscriptionFinal},
		Speakers: &recordingwire.MeetingSpeakers{
			State:   recordingwire.SpeakerStateReady,
			Since:   now,
			Tries:   1,
			Quality: recordingwire.SpeakerQualityOk,
			Next:    3,
			People: []recordingwire.SpeakerPerson{
				{ID: "s1", Name: "Ada"},
				{ID: "s2", Name: "Grace", Me: true},
			},
			Stats: map[recordingwire.SpeakerID]recordingwire.SpeakerStat{
				"s1": {Seconds: 35, Lines: 1},
				"s2": {Seconds: 36, Lines: 1},
			},
		},
		Revs: map[string]int64{PartSpeakers: 2},
	}
	mRaw, err := json.MarshalIndent(meeting, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(on.Store.Path(KindRecording, recID, meetingFile), append(mRaw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	hub := HubTranscript{
		Rev:    1,
		Pass:   recordingwire.HubPassFinal,
		Timing: recordingwire.TimingTurn,
		Segments: []RecordingSegment{
			{Start: 0, End: 35, Text: u.Segments[0].Text, Source: recordingwire.SourceHub},
			{Start: 36, End: 72, Text: u.Segments[1].Text, Source: recordingwire.SourceHub},
		},
	}
	hRaw, err := json.MarshalIndent(hub, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(on.Store.Path(KindRecording, recID, hubSegmentsFile), append(hRaw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	turns := SpeakerTurnsFile{
		Run:     "modoff-run-1",
		Source:  recordingwire.SpeakerSourceHub,
		Engine:  "fixture-engine 1.0",
		Covered: []recordingwire.SpeakerSpan{{0, 75}},
		Turns: []recordingwire.SpeakerTurn{
			{Start: 0, End: 35, Speaker: "s1"},
			{Start: 36, End: 72, Speaker: "s2"},
		},
	}
	tRaw, err := json.MarshalIndent(turns, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(on.Store.Path(KindRecording, recID, speakerTurnsFile), append(tRaw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	writtenMinutes(t, on, recID)

	before := recordingFilesOnDisk(t, on.Store.Dir)

	st, err := NewStore(on.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rezone(goldenZone); err != nil {
		t.Fatal(err)
	}
	off := &Engine{Store: st, Owner: on.Owner, Logf: func(string, ...any) {}}

	want, err := on.Get(KindRecording, recID, 20000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := off.Get(KindRecording, recID, 20000)
	if err != nil {
		t.Fatalf("detail without the module: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		t.Fatalf("speaker detail differs without the module:\n with    %s\n without %s", a, b)
	}
	if got.Speakers == nil || got.Speakers.State != recordingwire.SpeakerStateReady || len(got.Speakers.People) != 2 {
		t.Fatalf("detail.speakers without module: %+v", got.Speakers)
	}
	if got.Speakers.People[0].Name != "Ada" || got.Speakers.People[1].Name != "Grace" || !got.Speakers.People[1].Me {
		t.Fatalf("speaker names without module: %+v", got.Speakers.People)
	}
	if len(got.Segments) != 2 || got.Segments[0].Speaker != "s1" || got.Segments[1].Speaker != "s2" || got.Segments[0].SpeakerUnsure || got.Segments[1].SpeakerUnsure {
		t.Fatalf("segment speaker labels without module: %+v", got.Segments)
	}
	if after := recordingFilesOnDisk(t, off.Store.Dir); !reflect.DeepEqual(after, before) {
		t.Fatal("reading speakers without the module changed files on disk")
	}
}
