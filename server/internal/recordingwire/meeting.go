package recordingwire

import (
	"cmp"
	"errors"
	"maps"
	"slices"
	"time"
)

// MeetingHandoff is one hand-off of a meeting to an Agent (#8852): when, to which session (and the title the app
// showed for it), whether the transcript went too, and its state: HandoffSent, or HandoffFailed with Problem.
type MeetingHandoff struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	MachineID  string    `json:"machine_id"`
	SessionID  string    `json:"session_id"`
	Title      string    `json:"title,omitempty"`
	Transcript bool      `json:"transcript,omitempty"`
	State      string    `json:"state"`
	Problem    string    `json:"problem,omitempty"`
}

// MeetingRoom is a room meeting's room conversation.
type MeetingRoom struct {
	// Attendance is the role the room attends in: AttendanceNotes, AttendanceListener, AttendanceParticipant, or ""
	// (off).
	Attendance string `json:"attendance,omitempty"`
	State      string `json:"state"`
	Problem    string `json:"problem,omitempty"`
	MachineID  string `json:"machine_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	// Pending: the room's creation is (or is about to be) asked of the node under SessionID and is not settled yet
	// ("Made once"): after a restart the manager looks there first.
	Pending bool `json:"pending,omitempty"`
	// StartingAt is when the room last turned starting (a start not ready within 2 minutes failed), RetryAt when an
	// unavailable room is tried again.
	StartingAt time.Time `json:"starting_at,omitzero"`
	RetryAt    time.Time `json:"retry_at,omitzero"`
	// EndingAt: the meeting's end batch went to the room then; it turns ended once that turn ends, at the latest a
	// little later.
	EndingAt time.Time `json:"ending_at,omitzero"`
	// Started: the room came up (it was ready once): its ids are shown from then on. A start that never got there
	// keeps them here only, for the deletion.
	Started bool `json:"started,omitempty"`
	// Batching is how the room is fed (docs/assistant/meeting-minutes.md "Long meetings: lean batches and compaction
	// (#8253)"): RoomBatchingLean for a room a manager with lean batches started; 0 for an older room, fed a batch a
	// minute with the rules and the lists in each until it is deleted. An older manager after a rollback drops it when
	// it rewrites meeting.json, which makes the room an older one from then on.
	Batching int `json:"batching,omitempty"`
	// Lean is a lean room's bookkeeping: the lists' boundaries and its scheduled compaction. The apps never see it, so
	// its changes raise no revision.
	Lean MeetingRoomLean `json:"lean,omitzero"`
}

// MeetingRoomLean is what the manager keeps of a lean room across a restart (#8253).
type MeetingRoomLean struct {
	// ListsAt is when the last batch that showed the topics and highlights so far went; ListsDue: the next batch
	// shows them (the room's conversation was compacted since).
	ListsAt  time.Time `json:"lists_at,omitzero"`
	ListsDue bool      `json:"lists_due,omitempty"`
	// Tokens is the most context the room's row reported since its conversation was last compacted, as far as the
	// manager knows: a drop below half of it is Claude Code compacting the room on its own. TokensStale is what the row
	// reported when the manager saw a compaction end: the context of the reply before it (a compaction writes none),
	// not read as the room's until the row reports another.
	Tokens      int32 `json:"tokens,omitempty"`
	TokensStale int32 `json:"tokens_stale,omitempty"`
	// SpanAt is the live log's feed position (live.json's fed) when the room started or was last compacted: what the
	// batches given since cover is read off the live log's lines from there to its feed position now, so a restart
	// between a batch's two writes loses none of it.
	SpanAt int `json:"span_at,omitempty"`
	// CompactedAt is when the last scheduled compaction ended done, Compactions how many did.
	CompactedAt time.Time `json:"compacted_at,omitzero"`
	Compactions int       `json:"compactions,omitempty"`
	// CompactStartedAt: a compaction was asked of the node then and has not ended as far as the manager knows;
	// CompactFed is the feed position then (the lines given before it are what it compacts).
	CompactStartedAt time.Time `json:"compact_started_at,omitzero"`
	CompactFed       int       `json:"compact_fed,omitempty"`
	// CompactSeen is the start of the latest compaction of the room's session the manager has taken into account (its
	// own, or one the owner asked for), to tell one that ended since from it.
	CompactSeen time.Time `json:"compact_seen,omitzero"`
	// Attempts are the compactions refused or failed for the current span, RetryAt when the next may start; Never:
	// the node cannot compact the room (too old for it, or it would start the room's process without its restrictions).
	Attempts int       `json:"attempts,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
	Never    bool      `json:"never,omitempty"`
}

// MeetingFollowup is the meeting's follow-up conversation.
type MeetingFollowup struct {
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Pending: its creation was asked of the node by the call OpID and is not settled yet. OpID stays once it is made:
	// that call repeated is answered as made, with nothing sent again.
	Pending bool   `json:"pending,omitempty"`
	OpID    string `json:"op_id,omitempty"`
}

// RoomView is a room as the apps read it (GET …/live, detail.meta, the upload's 200): the ids once it has started,
// never with none or deleted; the problem only while unavailable.
type RoomView struct {
	State     string `json:"state"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Problem   string `json:"problem,omitempty"`
}

// ConvView is a conversation of the meeting (the follow-up) as the apps read it.
type ConvView struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// Room states (MeetingRoom.State, an open set for the apps).
const (
	RoomNone        = "none"
	RoomStarting    = "starting"
	RoomReady       = "ready"
	RoomUnavailable = "unavailable"
	RoomEnded       = "ended"
	RoomDeleted     = "deleted"
)

// Why a room is unavailable (MeetingRoom.Problem, an open set).
const (
	RoomRuntimeUnsupported = "runtime_unsupported"
	RoomNodeTooOld         = "node_too_old"
	RoomMachineOffline     = "machine_offline"
	RoomStartFailed        = "start_failed"
)

// The parts of a meeting the apps read again when they change (GET …/live?parts=, the meeting_live event). PartPhotos'
// revision is kept in photos.json (recording_photos.go); PartAsks', PartTopics' and PartHighlights' in live.json, and
// PartLines' is the live log's length (live.go); the others' in meeting.json.
const (
	PartRoom     = "room"
	PartAsks     = "asks"
	PartPrivate  = "private"
	PartFollowup = "followup"
	PartPhotos   = "photos"
	PartLines    = "lines"
	PartTopics   = "topics"
	// PartHighlights is the room's highlights of the meeting so far (phase 2b, live.go HighlightsSet).
	PartHighlights = "highlights"
	// PartSpeakers is the meeting's speaker list, state and timeline (#17674, meeting.json).
	PartSpeakers = "speakers"
)

// Hand-off states and how many a meeting keeps.
const (
	HandoffSent         = "sent"
	HandoffFailed       = "failed"
	MeetingHandoffsKept = 20
)

// RoomBatchingLean marks a room started with lean batches (MeetingRoom.Batching).
const RoomBatchingLean = 2

var (
	// ErrNotMeeting: the recording is not a meeting as #2981 counts them (neither kind meeting nor minutes asked).
	ErrNotMeeting = errors.New("this recording is not a meeting")
)

// Meeting is meeting.json.
type Meeting struct {
	// Room is the room meeting's: nil when no upload ever carried room (a #2981 meeting, or not attended).
	Room *MeetingRoom `json:"room,omitempty"`
	// Private is the private mark: nil until chosen.
	Private  *bool            `json:"private,omitempty"`
	Followup *MeetingFollowup `json:"followup,omitempty"`
	// Transcription is the meeting's transcription mode and, in hub mode, its state and timeline (#3862 phase 5,
	// transcription.go): nil until an upload named a mode.
	Transcription *MeetingTranscription `json:"transcription,omitempty"`
	// Handoffs are the meeting's hand-offs to Agents after it (#8852, POST …/handoff), oldest first, the last
	// MeetingHandoffsKept: whether each went, and why not. No part of their own: the apps read them with the detail and
	// from the hand-off's answer.
	Handoffs []MeetingHandoff `json:"handoffs,omitempty"`
	// Speakers is the meeting's speaker list, state and quality (#17674): nil until speaker separation runs or a
	// timeline is imported. Kept when a transcript is trimmed.
	Speakers *MeetingSpeakers `json:"speakers,omitempty"`
	// Revs are the parts' revisions (PartRoom, PartPrivate, PartFollowup, PartTranscription, PartSpeakers): each only goes up.
	Revs map[string]int64 `json:"revs,omitempty"`
}

// AddHandoff appends h, keeping the last MeetingHandoffsKept.
func (m *Meeting) AddHandoff(h MeetingHandoff) {
	m.Handoffs = append(m.Handoffs, h)
	if n := len(m.Handoffs); n > MeetingHandoffsKept {
		m.Handoffs = slices.Clone(m.Handoffs[n-MeetingHandoffsKept:])
	}
}

// Clone is a deep copy of m.
func (m Meeting) Clone() Meeting {
	out := Meeting{Private: m.Private, Revs: maps.Clone(m.Revs)}
	if m.Room != nil {
		r := *m.Room
		out.Room = &r
	}
	if m.Private != nil {
		p := *m.Private
		out.Private = &p
	}
	if m.Followup != nil {
		f := *m.Followup
		out.Followup = &f
	}
	out.Transcription = m.Transcription.Clone()
	out.Handoffs = slices.Clone(m.Handoffs)
	out.Speakers = m.Speakers.Clone()
	return out
}

// RoomView is m's room as the apps read it; nil when the recording is not a room meeting.
func (m Meeting) RoomView() *RoomView {
	if m.Room == nil {
		return nil
	}
	rm := m.Room
	v := &RoomView{State: rm.State}
	if v.State == "" {
		v.State = RoomNone
	}
	if v.State == RoomUnavailable {
		v.Problem = rm.Problem
	}
	if v.State != RoomNone && v.State != RoomDeleted && rm.Started && rm.SessionID != "" {
		v.MachineID, v.SessionID = rm.MachineID, rm.SessionID
	}
	return v
}

// FollowupView is m's follow-up as the apps read it; nil before it is made.
func (m Meeting) FollowupView() *ConvView {
	f := m.Followup
	if f == nil || f.Pending || f.SessionID == "" {
		return nil
	}
	return &ConvView{MachineID: f.MachineID, SessionID: f.SessionID}
}

// TranscriptionView is m's transcription as the apps read it; nil while no mode was decided. gaps are the spans the
// hub holds no audio for now; nil takes those meeting.json keeps (the audio writes keep them up to date). Once the
// final pass has started, or the audio is gone, they are the ones meeting.json keeps whatever gaps says (a trimmed
// recording's: none).
func (m Meeting) TranscriptionView(gaps [][2]float64) *TranscriptionView {
	t := m.Transcription
	if t == nil || t.Mode == "" {
		return nil
	}
	if !t.Hub() {
		return &TranscriptionView{Mode: t.Mode}
	}
	if gaps == nil || t.AudioClosed() {
		gaps = t.Gaps
	}
	shown := append([][2]float64{}, gaps...)
	state := cmp.Or(t.State, TranscriptionLive)
	v := &TranscriptionView{Mode: t.Mode, State: state, Gaps: &shown, Problem: t.Problem, Fallback: t.Fallback}
	if done, end, ok := t.FinalProgress(); ok {
		v.FinalDone, v.End = &done, &end
	}
	return v
}
