package kb

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// The upload is the recording module's (internal/modules/recording/writers, where its tests are). The tests of what
// the knowledge base does itself with a stored recording (the summary round, delete, retention, the commit and its
// recovery, lists, search, a hand edit) store one with PutRecording below: a plain upload of a note or a meeting
// through the same commit, with none of the module's parts (no day recording, room, attendance, hub transcription,
// calendar hint or echo rule).

type (
	RecordingUpload   = recordingwire.RecordingUpload
	RecordingPut      = recordingwire.RecordingPut
	RecordingState    = recordingwire.RecordingState
	RecordingBookmark = recordingwire.RecordingBookmark
)

const (
	RecordingStored  = recordingwire.RecordingStored
	RecordingStale   = recordingwire.RecordingStale
	RecordingExpired = recordingwire.RecordingExpired
	AttendanceOff    = recordingwire.AttendanceOff
)

// PutRecording stores upload u as revision u.Revision of recording rawID, as the module's upload does for a plain
// note or meeting.
func (e *Engine) PutRecording(rawID string, u RecordingUpload, now time.Time) (RecordingPut, error) {
	put := RecordingPut{Attendance: AttendanceOff, Transcription: TranscriptionDevice}
	if e.Store.ReadOnly() {
		return put, ErrReadOnly
	}
	id, ok := RecordingID(rawID)
	if !ok {
		return put, badRecording("id must be a UUID")
	}
	if u.Revision <= 0 || u.StartedAt.IsZero() {
		return put, badRecording("revision and started_at are required")
	}
	segs := make([]RecordingSegment, 0, len(u.Segments))
	for _, sg := range u.Segments {
		if sg.Text, sg.N, sg.Source = strings.TrimSpace(sg.Text), 0, ""; sg.Text != "" {
			segs = append(segs, sg)
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	u.Segments, u.Title, u.Language = segs, oneLine(u.Title), strings.TrimSpace(u.Language)
	if u.Kind = strings.ToLower(strings.TrimSpace(u.Kind)); u.Kind == "" {
		u.Kind = RecordingNote
	}
	put.Segments, put.Revision = u.Segments, u.Revision
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if _, gone := st.tombstones()[id]; gone {
		return put, ErrRecordingDeleted
	}
	r, exists := st.Recording(id)
	if exists {
		put.Revision = r.Revision
	}
	if pastRetention(u.StartedAt, now, e.RecordingDays()) || exists && r.Trimmed() {
		put.Status = RecordingExpired
		return put, nil
	}
	if exists && r.Revision > u.Revision {
		put.Status = RecordingStale
		return put, nil
	}
	advanced := !exists || u.Revision > r.Revision
	if !exists {
		r = &Recording{ID: id}
	}
	r.Revision, r.StartedAt, r.EndedAt, r.Duration = u.Revision, u.StartedAt.UTC(), u.EndedAt, u.Duration
	if r.EndedAt != nil {
		t := r.EndedAt.UTC()
		r.EndedAt = &t
	}
	r.Language, r.Device, r.Title, r.Bookmarks, r.Complete = u.Language, u.Device, u.Title, u.Bookmarks, u.Complete
	r.Kind, r.Calendar = u.Kind, u.Calendar
	r.Segments, r.Chars = len(u.Segments), recordingwire.RecordChars(u.Segments)
	if advanced {
		r.MinutesQuiet = 0
	}
	recordingwire.PlanMinutes(r)
	recordingwire.PlanRecap(r)
	r.Name = e.recordingName(r)
	r.Updated = now.UTC()
	if err := st.commitRecording(r, u.Segments); err != nil {
		return put, err
	}
	put.Status, put.Revision = RecordingStored, r.Revision
	st.notify(KindRecording, id, "metadata")
	st.notify(KindRecording, id, "messages")
	e.index(KindRecording, id, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	return put, nil
}

// RecordingStates is every recording kept and every tombstone, as the module's reconcile route lists them.
func (e *Engine) RecordingStates() (kept []RecordingState, deleted []string) {
	kept, deleted = []RecordingState{}, []string{}
	for _, r := range e.Store.Recordings() {
		kept = append(kept, RecordingState{ID: r.ID, Revision: r.Revision, Complete: r.Complete, StartedAt: r.StartedAt,
			MinutesState: r.MinutesState, TrimmedAt: r.TrimmedAt})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].StartedAt.After(kept[j].StartedAt) })
	for id := range e.Store.tombstones() {
		deleted = append(deleted, id)
	}
	sort.Strings(deleted)
	return kept, deleted
}

// putMeetingMinutes stores minutes for recording id as the minutes round would: minutes.json and minutes.md next to
// its meta.json. The round itself is the recording module's (internal/modules/recording/writers); this is the files
// it leaves, for the tests of what reads, trims and deletes them.
func putMeetingMinutes(t *testing.T, e *Engine, id string, m Minutes) {
	t.Helper()
	raw, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	st := e.Store
	st.recFilesMu.Lock()
	defer st.recFilesMu.Unlock()
	st.metaMu.Lock()
	defer st.metaMu.Unlock()
	if _, err := os.Stat(st.path(KindRecording, id, "meta.json")); err != nil {
		t.Fatalf("minutes of %s: %v", id, err)
	}
	if err := writeFile(st.path(KindRecording, id, "minutes.json"), append(raw, '\n')); err != nil {
		t.Fatalf("minutes of %s: %v", id, err)
	}
	if err := writeFile(st.path(KindRecording, id, "minutes.md"), []byte(strings.TrimSpace(m.Markdown)+"\n")); err != nil {
		t.Fatalf("minutes of %s: %v", id, err)
	}
}

// changeRecording rewrites recording id's meta.json with fn applied.
func changeRecording(t *testing.T, e *Engine, id string, fn func(r *Recording)) {
	t.Helper()
	changeRecordingMeta(t, e, id, fn)
}

func changeRecordingMeta(t *testing.T, e *Engine, id string, fn func(r *Recording)) {
	t.Helper()
	r := recordingNow(t, e, id)
	fn(r)
	if err := e.Store.putEntity(KindRecording, id, r, &r.Tagging, false); err != nil {
		t.Fatal(err)
	}
}

// A recording's bookkeeping files are never searched: the live log (and its temporary file) and meeting.json, next
// to a meta.json that is. Their writers are the recording module's, whose tests asserted this while they were here.
func TestRecordingBookkeepingIsNotSearched(t *testing.T) {
	dir := "recordings/" + recID + "/"
	for _, rel := range []string{dir + liveFile, dir + liveFile + ".tmp", dir + meetingFile} {
		if searchable(rel) {
			t.Errorf("%s is searchable", rel)
		}
	}
	if !searchable(dir + "meta.json") {
		t.Error("meta.json is not searchable")
	}
}
