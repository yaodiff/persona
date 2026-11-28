package writers

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Re-exported from recordingwire.
const (
	RoomRuntimeUnsupported = recordingwire.RoomRuntimeUnsupported
	RoomNodeTooOld         = recordingwire.RoomNodeTooOld
	RoomMachineOffline     = recordingwire.RoomMachineOffline
	RoomStartFailed        = recordingwire.RoomStartFailed
)

// MeetingHandoff is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type MeetingHandoff = recordingwire.MeetingHandoff

// Re-exported from recordingwire.
const (
	HandoffSent         = recordingwire.HandoffSent
	HandoffFailed       = recordingwire.HandoffFailed
	MeetingHandoffsKept = recordingwire.MeetingHandoffsKept
)

// Re-exported from recordingwire.
const (
	RoomBatchingLean = recordingwire.RoomBatchingLean
)

// MeetingFollowup is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type MeetingFollowup = recordingwire.MeetingFollowup

// Re-exported from recordingwire.
var (
	ErrNotMeeting = recordingwire.ErrNotMeeting
)

// MeetingRead is recording id's meeting.json (empty without one) and its stored upload, also on a read-only knowledge
// base: ErrRecordingDeleted after a delete, ErrNotFound with no upload of it.
func (e *engine) MeetingRead(rawID string) (Meeting, Recording, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return Meeting{}, Recording{}, ErrNotFound
	}
	if gone := e.Store.tombstoned(id); gone {
		return Meeting{}, Recording{}, ErrRecordingDeleted
	}
	r, ok := e.Store.Recording(id)
	if !ok {
		return Meeting{}, Recording{}, ErrNotFound
	}
	var m Meeting
	if r.Conv != nil {
		m = r.Conv.Clone()
	}
	return m, *r, nil
}

// MeetingUpdate changes recording id's meeting.json with fn (given it and the stored upload) and writes the result
// unless fn fails or changed nothing; the revisions of the parts that changed go up and MeetingChanged hears of them.
// ErrRecordingDeleted after a delete, ErrNotFound with no upload (or deleted meanwhile), ErrReadOnly on a read-only
// knowledge base. fn runs with meeting.json locked: it must be quick and call no Engine method of the recordings.
func (e *engine) MeetingUpdate(rawID string, fn func(m *Meeting, r Recording) error) (Meeting, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return Meeting{}, ErrNotFound
	}
	st := e.Store
	st.meetMu.Lock()
	m, parts, err := e.meetingUpdateLocked(id, fn)
	st.meetMu.Unlock()
	if err == nil {
		e.meetingChanged(id, parts)
	}
	return m, err
}

// SetPrivate sets recording id's private mark (PUT /api/v1/recordings/{id}/private): any time, a trimmed recording's
// too. Errors in the contract's order: ErrRecordingDeleted, ErrNotFound, ErrReadOnly, ErrNotMeeting.
func (e *engine) SetPrivate(rawID string, private bool) (Meeting, error) {
	return e.MeetingUpdate(rawID, func(m *Meeting, r Recording) error {
		if !r.IsMeeting() {
			return ErrNotMeeting
		}
		m.Private = &private
		return nil
	})
}

// uploadRoomRole is the role an upload's room asks for, normalized as #2981's attendance is (normalizeAttendance), with
// notes besides (#8851): a value that is not one of the four is off with AttendanceUnknownRole, a role on a recording
// that is not a meeting off with AttendanceNotMeeting. The upload is cleaned (its Kind set).
func uploadRoomRole(u *RecordingUpload) (role, problem string) {
	role = strings.ToLower(strings.TrimSpace(u.Room.Attendance))
	switch {
	case u.Room.Bad:
		return "", AttendanceUnknownRole
	case role == "" || role == AttendanceOff:
		return "", ""
	case role != AttendanceNotes && role != AttendanceListener && role != AttendanceParticipant:
		return "", AttendanceUnknownRole
	case u.Kind != RecordingMeeting:
		return "", AttendanceNotMeeting
	}
	return role, ""
}

// putMeetingFields stores what upload u says of recording id's meeting: its room (an upload with room makes it a room
// meeting for good; one without turns the role of a room meeting off and changes nothing else), its first private
// mark (only a meeting's, only while it has none) and its transcription (putTranscriptionFields: the first mode named,
// then in hub mode the timeline and the end). The caller holds recMu; meeting.json is written before meta.json, so a
// crash between leaves a role no older manager reads. The upload's top-level attendance fields are cleared for a room
// meeting (they are ignored beside room, and never written to its meta.json). The meeting after it, and the parts
// that changed.
func (e *engine) putMeetingFields(id string, u *RecordingUpload, now time.Time) (*Meeting, []string, error) {
	st := e.Store
	st.meetMu.Lock()
	defer st.meetMu.Unlock()
	// From the file, under meetMu: r was read before it, and a MeetingUpdate since (the private mark, the room, the
	// follow-up) would be written back over.
	var before Meeting
	if m, err := st.readMeeting(id); err != nil {
		return nil, nil, err
	} else if m != nil {
		before = m.Clone()
	}
	m := before.Clone()
	switch {
	case u.Room != nil:
		role, problem := uploadRoomRole(u)
		u.RoomProblem = problem
		if m.Room == nil {
			m.Room = &MeetingRoom{State: RoomNone}
		}
		m.Room.Attendance = role
	case m.Room != nil:
		m.Room.Attendance = "" // once a room meeting, always one: without room it is off
	}
	if m.Room != nil {
		u.Attendance, u.AttendingSession = "", nil
	}
	if u.Private != nil && m.Private == nil && u.Kind == RecordingMeeting {
		p := *u.Private
		m.Private = &p
	}
	e.putTranscriptionFields(id, &m, u, now)
	if meetingSame(before, m) {
		if m.Room == nil && m.Private == nil && m.Followup == nil && m.Transcription == nil && m.Speakers == nil {
			return nil, nil, nil
		}
		return &m, nil, nil
	}
	parts := meetingParts(before, m)
	bumpRevs(&m, parts)
	raw, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(st.path(KindRecording, id), 0o700); err != nil {
		return nil, nil, err
	}
	st.recFilesMu.Lock()
	err = st.writeRecordingFile(st.path(KindRecording, id, meetingFile), append(raw, '\n'))
	st.recFilesMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return &m, parts, nil
}

// RoomMeetings are the recordings that are room meetings, oldest first: what the room keeper looks at after a
// restart.
func (e *engine) RoomMeetings() []Recording {
	var out []Recording
	for _, r := range e.Store.Recordings() {
		if r.RoomMeeting() {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b Recording) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// RoomView is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RoomView = recordingwire.RoomView

// ConvView is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type ConvView = recordingwire.ConvView

// HasMinutes: recording id has minutes that can be read.
func (s *store) HasMinutes(id string) bool { return s.hasMinutes(id) }
