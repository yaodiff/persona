package kb

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

func extMinutesWrite(id string, at time.Time, markdown string) RecordingWrite {
	return RecordingWrite{ID: recordingwire.ID(id), Title: "Weekly sync", StartedAt: at, EndedAt: at.Add(45 * time.Minute), Language: "en",
		Minutes: &recordingwire.RecordingWriteMinutes{Markdown: markdown, Attendees: []string{"A", "B"},
			Points:  []recordingwire.MinutesPoint{{Text: "Shipped the importer"}},
			Actions: []recordingwire.MinutesAction{{ID: "a1", What: "Write the release notes"}}}}
}

// An extension creates its recording with minutes, the same write again changes nothing, and a changed one is the
// next revision with the next minutes version; the to-do made of an action stays on it.
func TestPutExtensionRecordingCreatesAndUpdates(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	id := "55555555-5555-4555-8555-555555555555"
	if err := e.Store.PutProject(&Project{Slug: "alpha", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutItem(&Item{Kind: KindEvent, ID: "ev-1", Title: "Weekly sync", At: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	w := extMinutesWrite(id, now.Add(-2*time.Hour), "# Weekly sync\n\n- Shipped the importer")
	w.Projects = []string{"alpha"}
	w.Meeting = &recordingwire.RecordingWriteMeeting{Kind: recordingwire.CalendarEvents, ID: recordingwire.ItemID("ev-1")}
	res, err := e.PutExtensionRecording(w, "minutes-ext", now)
	if err != nil || !res.Created || res.Revision != 1 || res.MinutesVersion != 1 {
		t.Fatalf("create: %+v %v", res, err)
	}
	r, ok := e.Store.Recording(id)
	if !ok || r.Writer == nil || r.Writer.By != "ext:minutes-ext" || r.Kind != RecordingMeeting || !r.Complete {
		t.Fatalf("stored %+v", r)
	}
	if r.MinutesState != MinutesReady || r.MinutesRev != r.Revision || r.Meeting == nil || r.Meeting.ID != "ev-1" ||
		!slices.Contains(r.Projects, "alpha") || !slices.Contains(r.Items, "events/ev-1") {
		t.Fatalf("minutes state %q rev %d meeting %+v projects %v items %v", r.MinutesState, r.MinutesRev, r.Meeting, r.Projects, r.Items)
	}
	if p, _ := e.Store.Project("alpha"); !slices.Contains(p.Items, "recordings/"+id) {
		t.Fatalf("project items %v", p.Items)
	}
	m, err := e.Store.ReadMinutes(id)
	if err != nil || m.Version != 1 || m.Revision != 1 || len(m.Actions) != 1 || m.Start.IsZero() {
		t.Fatalf("minutes %+v %v", m, err)
	}
	if md, _ := os.ReadFile(e.Store.path(KindRecording, id, "minutes.md")); !strings.Contains(string(md), "Shipped the importer") {
		t.Fatalf("minutes.md %q", md)
	}
	if hits := e.Find("importer", 10); len(hits) == 0 {
		t.Fatal("the minutes are not found by a search")
	}

	again, err := e.PutExtensionRecording(w, "minutes-ext", now.Add(time.Minute))
	if err != nil || !again.Unchanged || again.Revision != 1 {
		t.Fatalf("the same write again: %+v %v", again, err)
	}

	// The user made a to-do of the action; the next write keeps it.
	m.Actions[0].TodoID = "todo-1"
	putMeetingMinutes(t, e, id, *m)
	w2 := extMinutesWrite(id, now.Add(-2*time.Hour), "# Weekly sync\n\n- Shipped the importer\n- Planned the next release")
	upd, err := e.PutExtensionRecording(w2, "minutes-ext", now.Add(2*time.Minute))
	if err != nil || upd.Created || upd.Unchanged || upd.Revision != 2 || upd.MinutesVersion != 2 {
		t.Fatalf("update: %+v %v", upd, err)
	}
	m2, _ := e.Store.ReadMinutes(id)
	if m2.Version != 2 || m2.Actions[0].TodoID != "todo-1" {
		t.Fatalf("updated minutes %+v", m2)
	}
	if r, _ := e.Store.Recording(id); !slices.Contains(r.Projects, "alpha") {
		t.Fatalf("a later write without projects took the link away: %v", r.Projects)
	}
}

// Another extension cannot write it, no extension can write a recording the phone uploaded, and one the user deleted
// is not made again.
func TestPutExtensionRecordingRefusesOthers(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	own := "66666666-6666-4666-8666-666666666666"
	if _, err := e.PutExtensionRecording(extMinutesWrite(own, now.Add(-time.Hour), "# Mine"), "minutes-ext", now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutExtensionRecording(extMinutesWrite(own, now.Add(-time.Hour), "# Theirs"), "other-ext", now); !errors.Is(err, ErrNotRecordingWriter) {
		t.Fatalf("another extension's write: %v", err)
	}
	if m, _ := e.Store.ReadMinutes(own); m.Markdown != "# Mine" {
		t.Fatalf("minutes after the refused write: %q", m.Markdown)
	}

	phone := "77777777-7777-4777-8777-777777777777"
	if _, err := e.PutRecording(phone, recUpload(1, now.Add(-3*time.Hour), true, "the user's meeting"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutExtensionRecording(extMinutesWrite(phone, now.Add(-3*time.Hour), "# Over it"), "minutes-ext", now); !errors.Is(err, ErrNotRecordingWriter) {
		t.Fatalf("a write over the phone's recording: %v", err)
	}
	if _, err := e.Store.ReadMinutes(phone); err == nil {
		t.Fatal("minutes were written into the phone's recording")
	}

	if _, err := e.DeleteRecording(own, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutExtensionRecording(extMinutesWrite(own, now.Add(-time.Hour), "# Mine"), "minutes-ext", now); !errors.Is(err, ErrRecordingDeleted) {
		t.Fatalf("a write of a deleted recording: %v", err)
	}
}

// Past recording_days an extension's recording with minutes is trimmed and kept with them; one with only a
// transcript goes.
func TestPutExtensionRecordingKeptPastRetention(t *testing.T) {
	e := goldenEngine(t, nil)
	e.RecordingDaysFunc = func() int { return 14 }
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	withMinutes, without := "88888888-8888-4888-8888-888888888888", "99999999-9999-4999-8999-999999999999"
	w := extMinutesWrite(withMinutes, now.AddDate(0, 0, -100), "# An old meeting")
	w.Transcript = []RecordingSegment{{Start: 0, End: 4, Text: "hello there"}}
	if _, err := e.PutExtensionRecording(w, "minutes-ext", now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutExtensionRecording(RecordingWrite{ID: recordingwire.ID(without), StartedAt: now.AddDate(0, 0, -100),
		Transcript: []RecordingSegment{{Start: 0, End: 2, Text: "no minutes"}}}, "minutes-ext", now); err != nil {
		t.Fatal(err)
	}
	if n := e.PruneRecordings(now); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	r, ok := e.Store.Recording(withMinutes)
	if !ok || !r.Trimmed() || r.MinutesState != MinutesReady {
		t.Fatalf("the recording with minutes: %+v %v", r, ok)
	}
	if m, err := e.Store.ReadMinutes(withMinutes); err != nil || m.Markdown != "# An old meeting" {
		t.Fatalf("minutes past the retention: %+v %v", m, err)
	}
	if _, ok := e.Store.Recording(without); ok {
		t.Fatal("the recording without minutes was kept")
	}
	if n := e.PruneRecordings(now.AddDate(1, 0, 0)); n != 0 {
		t.Fatalf("a year later pruned %d", n)
	}
	if _, ok := e.Store.Recording(withMinutes); !ok {
		t.Fatal("the minutes went a year later")
	}
}

func TestPutExtensionRecordingChecksTheWrite(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	at := now.Add(-time.Hour)
	for name, w := range map[string]RecordingWrite{
		"not a uuid":        extMinutesWrite("meeting-1", at, "# x"),
		"no start":          {ID: recordingwire.ID(id), Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "# x"}},
		"ends before":       {ID: recordingwire.ID(id), StartedAt: at, EndedAt: at.Add(-time.Minute), Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "# x"}},
		"nothing":           {ID: recordingwire.ID(id), StartedAt: at},
		"empty markdown":    {ID: recordingwire.ID(id), StartedAt: at, Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "  "}},
		"no project":        {ID: recordingwire.ID(id), StartedAt: at, Projects: []string{"nope"}, Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "# x"}},
		"no meeting":        {ID: recordingwire.ID(id), StartedAt: at, Meeting: &recordingwire.RecordingWriteMeeting{Kind: recordingwire.CalendarEvents, ID: "nope"}, Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "# x"}},
		"meeting not event": {ID: recordingwire.ID(id), StartedAt: at, Meeting: &recordingwire.RecordingWriteMeeting{Kind: recordingwire.CalendarKind(KindChat), ID: "c"}, Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "# x"}},
		"action without id": {ID: recordingwire.ID(id), StartedAt: at, Minutes: &recordingwire.RecordingWriteMinutes{Markdown: "# x", Actions: []recordingwire.MinutesAction{{What: "w"}}}},
	} {
		if _, err := e.PutExtensionRecording(w, "minutes-ext", now); !errors.Is(err, ErrBadRecording) {
			t.Errorf("%s: %v, want a bad recording", name, err)
		}
	}
	if _, ok := e.Store.Recording(id); ok {
		t.Fatal("a refused write stored a recording")
	}
}

// Extension writes drop hub-owned segment fields including speaker and speaker_unsure (#17674).
func TestPutExtensionRecordingDropsSpeakerFields(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	id := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	w := RecordingWrite{
		ID:        recordingwire.ID(id),
		StartedAt: now.Add(-time.Hour),
		Transcript: []RecordingSegment{
			{Start: 0, End: 5, Text: "Hello everyone", N: 9, Source: "hub", Echo: true, Speaker: "s1", SpeakerUnsure: true},
		},
	}
	if _, err := e.PutExtensionRecording(w, "minutes-ext", now); err != nil {
		t.Fatal(err)
	}
	d, err := e.Get(KindRecording, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(d.Segments))
	}
	sg := d.Segments[0]
	if sg.N != 0 || sg.Source != "" || sg.Echo || sg.Speaker != "" || sg.SpeakerUnsure {
		t.Fatalf("hub-owned segment fields were not dropped: %+v", sg)
	}
}
