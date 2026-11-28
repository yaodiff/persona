package recordingwire

import (
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// An extension's own recording (kb.put_recording, docs/extensions/extensions.md "Writing recordings and minutes"): a
// meeting it took notes of elsewhere, stored as a recording of the knowledge base with its minutes and, optionally,
// its transcript. The extension picks the id (a UUID; one derived from its own source id makes a retry land on the
// same entry), the hub keeps the revision, and the recording is the extension's (Recording.Writer): no other
// extension may write it, nor any extension a recording the phone uploaded.

// ErrNotRecordingWriter: the recording exists and is not the writing extension's (the phone's, or another
// extension's).
var ErrNotRecordingWriter = errors.New("this recording is not the extension's own")

// Bounds of one RecordingWrite (the RPC's params are at most 1 MiB in all).
const (
	RecordingWriteMaxSegments = 20000
	RecordingWriteMaxProjects = 20
	RecordingWriteMaxItems    = 200 // points, decisions, actions, open questions, attendees: each
)

// RecordingWrite is kb.put_recording's params.
type RecordingWrite struct {
	ID        ID        `json:"id"` // a UUID
	Title     string    `json:"title,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Language  string    `json:"language,omitempty"` // an i18n code (zh-Hans, en, ja)
	// Minutes are written as minutes.json and minutes.md: a recording with minutes is kept past recording_days, its
	// minutes until minutes_days (forever by default).
	Minutes *RecordingWriteMinutes `json:"minutes,omitempty"`
	// Transcript is optional; without it the recording has its minutes only.
	Transcript []kbwire.RecordingSegment `json:"transcript,omitempty"`
	// Projects are slugs of existing projects to link it to (added, never removed by a later write).
	Projects []string `json:"projects,omitempty"`
	// Meeting is the calendar item ("events") or imported meeting ("meetings") it was, which must exist.
	Meeting *RecordingWriteMeeting `json:"meeting,omitempty"`
}

// RecordingWriteMinutes is the minutes the extension wrote; the hub sets version, revision, generated_at, start, end
// and when. Markdown is required: it is minutes.md, what searches and kb.get read.
type RecordingWriteMinutes struct {
	Title     string          `json:"title,omitempty"`
	Where     string          `json:"where,omitempty"`
	Attendees []string        `json:"attendees,omitempty"`
	Points    []MinutesPoint  `json:"points,omitempty"`
	Decisions []MinutesItem   `json:"decisions,omitempty"`
	Actions   []MinutesAction `json:"actions,omitempty"`
	Open      []MinutesItem   `json:"open,omitempty"`
	Language  string          `json:"language,omitempty"`
	Markdown  string          `json:"markdown"`
}

// RecordingWriteMeeting names a calendar item or an imported meeting of the knowledge base.
type RecordingWriteMeeting struct {
	Kind CalendarKind `json:"kind"` // "events" or "meetings"
	ID   ItemID       `json:"id"`
}

// RecordingWriteResult is kb.put_recording's result.
type RecordingWriteResult struct {
	ID       ID    `json:"id"`
	Revision int64 `json:"revision"`
	Created  bool  `json:"created,omitempty"`
	// Unchanged: the write was the same as the last one; nothing was written.
	Unchanged      bool `json:"unchanged,omitempty"`
	MinutesVersion int  `json:"minutes_version,omitempty"`
}
