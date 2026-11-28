package recordingwire

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// RecordingBookmark is a mark the user set while recording: seconds from the start.
type RecordingBookmark struct {
	At    float64 `json:"at"`
	Label string  `json:"label,omitempty"`
}

// RecordingCalendar is the event of the phone's own calendar a recording belongs to: a hint for its name and the
// minutes' attendees and place (docs/assistant/meeting-minutes.md "Field contract").
type RecordingCalendar struct {
	Title     string   `json:"title"`
	Attendees []string `json:"attendees,omitempty"`
	Location  string   `json:"location,omitempty"`
}

// MeetingRef is the knowledge base's calendar item (meetings or events) a recording overlaps.
type MeetingRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RecordingState is a recording as the phone reconciles against it (GET /api/v1/recordings).
type RecordingState struct {
	ID           string    `json:"id"`
	Revision     int64     `json:"revision"`
	Complete     bool      `json:"complete"`
	StartedAt    time.Time `json:"started_at"`
	MinutesState string    `json:"minutes_state,omitempty"`
	// TrimmedAt: only its minutes are kept (Recording.TrimmedAt). Listed all the same, at the revision it had, so a phone
	// that still holds it sees the hub has it and sends nothing (an upload of it would be expired).
	TrimmedAt *time.Time `json:"trimmed_at,omitempty"`
	// An all-day recording's only (recording_day.go): Next and Base always, 0 when lost (an older manager rewrote its
	// meta.json; the phone sends a PUT), so an entry of kind day without base is an older manager's.
	Kind         string     `json:"kind,omitempty"`
	Day          string     `json:"day,omitempty"`
	Part         int        `json:"part,omitempty"`
	Next         *int       `json:"next,omitempty"`
	Base         *int64     `json:"base,omitempty"`
	RecapState   string     `json:"recap_state,omitempty"`
	RecapFinal   bool       `json:"recap_final,omitempty"`
	RecapFinalAt *time.Time `json:"recap_final_at,omitempty"`
	// Name is what the hub calls it and NameSource where that came from (#8873, NameSource); both absent while it is
	// called only by when it started.
	Name       string `json:"name,omitempty"`
	NameSource string `json:"name_source,omitempty"`
	// Space is the track a recording was saved into instead (#13501, RecordingUpload.Space): set by the manager on the
	// ones it lists beside these, never by RecordingStates.
	Space string `json:"space,omitempty"`
}

// Recording kinds: a meeting gets minutes (minutes.go); a note (or none) only the summary; an all-day recording
// (recording_day.go) a recap of its day and never minutes nor a summary.
const (
	RecordingNote    = "note"
	RecordingMeeting = "meeting"
	RecordingDay     = "day"
)

// Minutes states (meta.json's minutes_state; "" when the recording is not a meeting).
const (
	MinutesPending = "pending"
	MinutesReady   = "ready"
	MinutesFailed  = "failed"
)

// What PutRecording did.
const (
	RecordingStored  = "stored"
	RecordingStale   = "stale"
	RecordingExpired = "expired"
)

// Where a recording's name comes from (RecordingState.NameSource, #8873): an open set, "" when it is named only by
// when it started (or, an all-day recording, by its day).
const (
	NamedByRename   = "renamed"  // the owner's name given on the web (Renamed, #8877)
	NamedByTitle    = "title"    // the upload's title, not its calendar hint's
	NamedByCalendar = "calendar" // the phone's calendar hint, or the knowledge base's calendar item it overlaps
	NamedByModel    = "model"    // AITitle
)

// RecordingUpload is PUT /api/v1/recordings/{id}'s body.
type RecordingUpload struct {
	Revision  int64                     `json:"revision"`
	StartedAt time.Time                 `json:"started_at"`
	EndedAt   *time.Time                `json:"ended_at,omitempty"`
	Duration  float64                   `json:"duration"`
	Language  string                    `json:"language"`
	Device    string                    `json:"device"`
	Title     string                    `json:"title"`
	Bookmarks []RecordingBookmark       `json:"bookmarks"`
	Segments  []kbwire.RecordingSegment `json:"segments"`
	Complete  bool                      `json:"complete"`
	Kind      string                    `json:"kind"`
	Calendar  *RecordingCalendar        `json:"calendar,omitempty"`
	// Spoken is where Persona spoke in the meeting and what it said (#5905, echo.go): clean marks the segments that are
	// its echo by it, beside the phone's own marks. Read leniently (UnmarshalJSON, readSpoken); never stored.
	Spoken []SpokenSpan `json:"spoken,omitempty"`
	// Attendance is the role Persona attends the meeting in ("" or AttendanceOff: none) and AttendingSession the
	// session that attends (nil: the main chat); see normalizeAttendance and live.go.
	Attendance       string      `json:"attendance,omitempty"`
	AttendingSession *SessionRef `json:"attending_session,omitempty"`
	// Private is the meeting's private mark as it starts (meeting.go): taken only while the recording has none.
	Private *bool `json:"private,omitempty"`
	// An all-day recording's (kind day, recording_day.go): its date and IANA zone, its part and the phone's coverage;
	// ignored on other kinds.
	Day      string             `json:"day,omitempty"`
	TZ       string             `json:"tz,omitempty"`
	Part     int                `json:"part,omitempty"`
	Coverage *RecordingCoverage `json:"coverage,omitempty"`
	// Space is the track the recording goes to instead of the knowledge base (#13501, docs/assistant/health.md
	// "Tracks and kinds"): the manager writes its transcript into that track's recordings/ and never hands the upload
	// to PutRecording. "" (and absent): the knowledge base, as ever. Never stored here.
	Space string `json:"space,omitempty"`
	// The fields below are the knowledge base's reading of the upload, never part of its JSON. Next is how many
	// positions a day upload carried (empty segments included); DayFull: it is over the limits.
	Next    int  `json:"-"`
	DayFull bool `json:"-"`
	// BadRole, BadSession: the JSON held a value of the wrong type for these two fields (UnmarshalJSON), which
	// turns attending off rather than failing the upload.
	BadRole    bool `json:"-"`
	BadSession bool `json:"-"`
	// Room is the upload's room (meeting.go), nil without one; RoomProblem what was wrong with its role.
	Room        *RoomUpload `json:"-"`
	RoomProblem string      `json:"-"`
	// Transcription is the mode the upload names ("" when none, or a value that names none) and Timeline its timeline
	// (nil without one, or when it was malformed or over the limits); TranscriptionProblem why the mode it named was
	// not taken (transcription.go).
	Transcription        string    `json:"-"`
	Timeline             *Timeline `json:"-"`
	TranscriptionProblem string    `json:"-"`
}

// Recording is a recording's meta.json.
type Recording struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`            // the user's title, else the model's, else the start time
	Title     string              `json:"title,omitempty"` // the user's
	Revision  int64               `json:"revision"`
	StartedAt time.Time           `json:"started_at"`
	EndedAt   *time.Time          `json:"ended_at,omitempty"`
	Duration  float64             `json:"duration"`
	Language  string              `json:"language,omitempty"`
	Device    string              `json:"device"`
	Bookmarks []RecordingBookmark `json:"bookmarks,omitempty"`
	Complete  bool                `json:"complete"`
	Segments  int                 `json:"segments"`
	Chars     int                 `json:"chars"`
	Projects  []string            `json:"projects,omitempty"`
	OneLine   string              `json:"one_line,omitempty"`
	// AITitle is the model's title (#8873, recording_title.go) and AITitleFrom what wrote it: AITitleLive while it
	// records, AITitleSummary (or "", from before it was said) once complete, AITitleMinutes from a meeting's minutes,
	// which the summary never replaces. LiveTitles counts the live title's calls, the last at LiveTitleAt, and
	// LiveTitleChars is the meeting's text (Chars) its last title was written from.
	// Renamed is the owner's name given on the web (#8877, RenameRecording), RenamedAt when: it comes before the upload's
	// title until an upload changes that title (the phone renamed it since), and is not the phone's to replace.
	Renamed        string     `json:"renamed,omitempty"`
	RenamedAt      *time.Time `json:"renamed_at,omitempty"`
	AITitle        string     `json:"ai_title,omitempty"`
	AITitleFrom    string     `json:"ai_title_from,omitempty"`
	LiveTitles     int        `json:"live_titles,omitempty"`
	LiveTitleAt    time.Time  `json:"live_title_at,omitzero"`
	LiveTitleChars int        `json:"live_title_chars,omitempty"`
	// SummarizedRev is the revision summary.md was written from (0: none yet). SummarizedFinal: in hub mode, it was
	// written from the final transcript (the transcription final or failed when it was read): one written from the
	// live text, also one whose call was still running when the final pass ended, is made again (summaryDue).
	SummarizedRev   int64     `json:"summarized_rev,omitempty"`
	SummarizedFinal bool      `json:"summarized_final,omitempty"`
	Updated         time.Time `json:"updated"`
	// Meeting minutes (minutes.go). Kind and Calendar are the phone's, replaced by every upload; the rest is the hub's.
	Kind     string             `json:"kind,omitempty"`
	Calendar *RecordingCalendar `json:"calendar,omitempty"`
	// Attending the meeting (live.go): the phone's too, replaced by every upload. Attendance is "" when off (never
	// AttendanceOff on disk); AttendingSession is nil for the main chat, and always when attendance is off.
	Attendance       string      `json:"attendance,omitempty"`
	AttendingSession *SessionRef `json:"attending_session,omitempty"`
	// Meeting is the overlapping calendar item, matched once per revision (MatchedRev); Items links it ("events/<id>").
	Meeting *MeetingRef `json:"meeting,omitempty"`
	// ImportedMeeting is the overlapping meeting imported from a connected app (a meetings item), by the same rule
	// among meetings only (#8872): Meeting's event wins for the minutes, while the lists show this recording and that
	// meeting as one row. Matched with Meeting, and looked for again until minutesImportWait after the end, as an
	// imported meeting comes after the call. Items links it too.
	ImportedMeeting *MeetingRef `json:"imported_meeting,omitempty"`
	Items           []string    `json:"items,omitempty"`
	MatchedRev      int64       `json:"matched_rev,omitempty"`
	MinutesState    string      `json:"minutes_state,omitempty"`
	MinutesAsked    bool        `json:"minutes_asked,omitempty"` // the user asked for minutes: a meeting whatever its kind
	// MinutesRev is the revision minutes.json was made from (0: none, or asked to be made again); MinutesVersion counts
	// the minutes written; MinutesFails the unusable answers for MinutesFailRev (failed after minutesTries), and
	// MinutesTimeouts its tries that ran out of time, the next not before MinutesRetryAt (minutes_timeout.go).
	MinutesRev      int64     `json:"minutes_rev,omitempty"`
	MinutesVersion  int       `json:"minutes_version,omitempty"`
	MinutesFails    int       `json:"minutes_fails,omitempty"`
	MinutesFailRev  int64     `json:"minutes_fail_rev,omitempty"`
	MinutesTimeouts int       `json:"minutes_timeouts,omitempty"`
	MinutesRetryAt  time.Time `json:"minutes_retry_at,omitzero"`
	// MinutesAsks counts the requests to make them again (the user's, and a photo added or deleted): minutes made from
	// before the latest one don't settle it. MinutesQuiet is the MinutesAsks of minutes made again only for a photo,
	// which are not pushed (photosRemake); 0 once they are written.
	MinutesAsks  int `json:"minutes_asks,omitempty"`
	MinutesQuiet int `json:"minutes_quiet,omitempty"`
	// TrimmedAt is the hub's: when its transcript went with recording_days and only meta.json and the minutes stayed
	// (recording_trim.go); nil while it is whole.
	TrimmedAt *time.Time `json:"trimmed_at,omitempty"`
	// An all-day recording (recording_day.go). Day, TZ, Part and Coverage are the phone's; Base is the revision of the
	// last PUT, which appends must name, and Next how many positions the hub holds on it (empty segments included).
	Day      string             `json:"day,omitempty"`
	TZ       string             `json:"tz,omitempty"`
	Part     int                `json:"part,omitempty"`
	Coverage *RecordingCoverage `json:"coverage,omitempty"`
	Next     int                `json:"next,omitempty"`
	Base     int64              `json:"base,omitempty"`
	// TextRev counts the uploads that changed its text or coverage (every PUT, every append that stored anything),
	// whatever their revision: an append from an older request stores its positions without raising Revision, so what
	// the recap read is measured by it.
	TextRev int64 `json:"text_rev,omitempty"`
	// Its recap (day_recap.go), the hub's: the state, whether the newest recap is final and when the first final one
	// was written; RecapRev the revision the newest recap was made from, RecapVersion how many were written; RecapDue
	// the due recap not written yet or failed (nil without one), RecapTargets how many became due so far (each one's
	// number), RecapAsks the user's requests; RecapText the TextRev the newest recap read all of (0: it left some out
	// for want of time), so newer text is what it did not read.
	RecapState   string       `json:"recap_state,omitempty"`
	RecapFinal   bool         `json:"recap_final,omitempty"`
	RecapFinalAt *time.Time   `json:"recap_final_at,omitempty"`
	RecapRev     int64        `json:"recap_rev,omitempty"`
	RecapText    int64        `json:"recap_text,omitempty"`
	RecapVersion int          `json:"recap_version,omitempty"`
	RecapDue     *RecapTarget `json:"recap_due,omitempty"`
	RecapTargets int          `json:"recap_targets,omitempty"`
	RecapAsks    int          `json:"recap_asks,omitempty"`
	// Which targets were set already: RecapEvening the owner's day whose recap time set one, RecapComplete once
	// complete (a PUT that is not complete clears it), RecapCutoff after the cutoff.
	RecapEvening  string `json:"recap_evening,omitempty"`
	RecapComplete bool   `json:"recap_complete,omitempty"`
	RecapCutoff   bool   `json:"recap_cutoff,omitempty"`
	// RecapNotedText: every finished hour of that TextRev has its notes (or gave up on them).
	RecapNotedText int64 `json:"recap_noted_text,omitempty"`
	// The day's one push (day_recap_write.go "The push"), across its parts: RecapWait, its recap time came with no
	// recap ready, so the day's first recap pushes; RecapPush, the push is due from this recording's recap (set in the
	// same write as that recap, or as its recap time, RecapPushSince) and DayRecapWritten has not taken it yet
	// (RecapPushTries calls failed, the next not before RecapPushAt; RecapPushTook the destinations that took it so
	// far, which it is not sent to again); RecapPushed, it has, or was given up on (RecapPushFailed: dayPushGiveUp
	// after it became due, some destination never took it).
	RecapWait       bool      `json:"recap_wait,omitempty"`
	RecapPush       bool      `json:"recap_push,omitempty"`
	RecapPushed     bool      `json:"recap_pushed,omitempty"`
	RecapPushSince  time.Time `json:"recap_push_since,omitzero"`
	RecapPushTries  int       `json:"recap_push_tries,omitempty"`
	RecapPushAt     time.Time `json:"recap_push_at,omitzero"`
	RecapPushTook   []string  `json:"recap_push_took,omitempty"`
	RecapPushFailed bool      `json:"recap_push_failed,omitempty"`
	// RecapTodos are the to-dos made of its recaps' actions, whatever the version (keepRecapTodos).
	RecapTodos []RecapTodo `json:"recap_todos,omitempty"`
	// Writer is the extension that wrote this recording through kb.put_recording (docs/extensions/extensions.md
	// "Writing recordings and minutes"); nil for one the phone uploaded. Only that extension may write it again.
	Writer *RecordingWriter `json:"writer,omitempty"`
	kbwire.Tagging

	// Conv is the recording's meeting.json (meeting.go: its room, private mark and follow-up), read with it; nil
	// without one. Never written to meta.json.
	Conv *Meeting `json:"-"`
}

// RecordingWriter is who wrote an extension's recording and the digest of what it last wrote, so the same write again
// changes nothing.
type RecordingWriter struct {
	By  string    `json:"by"`  // "ext:<extension id>"
	Sum string    `json:"sum"` // sha256 of the last write's params, hex
	At  time.Time `json:"at"`
}

// Trimmed: the transcript went with recording_days; only the metadata and the minutes are left.
func (r *Recording) Trimmed() bool { return r.TrimmedAt != nil }

// RecordingPut is PutRecording's result: Status is RecordingStored, RecordingStale or RecordingExpired; Attendance the
// role in effect after it (AttendanceOff, AttendanceListener or AttendanceParticipant: the stored recording's, so the
// one already there for a stale upload and off for an expired one), and AttendanceProblem what was wrong with this
// upload's attendance fields ("" or an Attendance* problem), whatever Status is.
type RecordingPut struct {
	Status            string
	Attendance        string
	AttendanceProblem string
	// Revision is the stored revision after it (this upload's when there is none); for an all-day recording Base and
	// Next are its base and the hub's count after it (recording_day.go).
	Revision int64
	Base     int64
	Next     int
	// Room: the recording is a room meeting (meeting.go), or this upload asked for one; Meeting is its meeting.json
	// after it (nil without one).
	Room    bool
	Meeting *Meeting
	// Name and NameSource are the stored recording's name and where it came from ("" while it is named only by when
	// it started; #8873); TitleDue: its live title is due now (recording_title.go).
	Name       string
	NameSource string
	TitleDue   bool
	// Transcription is the transcription mode in effect after the upload (TranscriptionDevice, TranscriptionHub: the
	// 200's transcription), and TranscriptionProblem why it is not the one asked for (TranscriptionUnavailable,
	// TranscriptionNotMeeting).
	Transcription        string
	TranscriptionProblem string
	// Segments are the upload's segments as stored: trimmed, empty ones dropped, in order (the upload itself is never
	// changed). Nil when the upload was refused before they were read.
	Segments []kbwire.RecordingSegment
}

// NameSource is where r's name comes from: the owner's name from the web, the upload's title (the phone's calendar when
// it says the same), the phone's calendar hint, the matched calendar item, the model's title, else "".
func (r *Recording) NameSource() string {
	switch {
	case r.Renamed != "":
		return NamedByRename
	case r.Title != "":
		if r.Calendar != nil && r.Calendar.Title == r.Title {
			return NamedByCalendar
		}
		return NamedByTitle
	case r.Kind == RecordingDay:
		return ""
	case r.CalendarTitle() != "":
		return NamedByCalendar
	case r.AITitle != "":
		return NamedByModel
	}
	return ""
}

// AttendanceRole is the role r is attended in: AttendanceOff, AttendanceNotes (a room meeting's only),
// AttendanceListener or AttendanceParticipant. A room
// meeting's is its room's (meeting.json), never meta.json's; a trimmed recording is attended no more.
func (r Recording) AttendanceRole() string {
	role := r.Attendance
	if r.RoomMeeting() {
		role = r.Conv.Room.Attendance
	}
	if role == "" || r.TrimmedAt != nil {
		return AttendanceOff
	}
	return role
}

// UnmarshalJSON reads an upload like the default does, except that a value of the wrong type for attendance or
// attending_session does not fail it (the same request carries the transcript): normalizeAttendance turns attending
// off for it. Nor does one for room, private, transcription or timeline (readRoomAndPrivate, readTranscription), nor
// a malformed spoken (readSpoken).
func (u *RecordingUpload) UnmarshalJSON(data []byte) error {
	type plain RecordingUpload
	aux := struct {
		*plain
		Attendance       json.RawMessage `json:"attendance"`
		AttendingSession json.RawMessage `json:"attending_session"`
		Room             json.RawMessage `json:"room"`
		Private          json.RawMessage `json:"private"`
		Transcription    json.RawMessage `json:"transcription"`
		Timeline         json.RawMessage `json:"timeline"`
		Spoken           json.RawMessage `json:"spoken"`
	}{plain: (*plain)(u)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	u.Spoken = ReadSpoken(aux.Spoken)
	u.readRoomAndPrivate(aux.Room, aux.Private)
	u.readTranscription(aux.Transcription, aux.Timeline)
	u.Attendance, u.AttendingSession, u.BadRole, u.BadSession = "", nil, false, false
	if raw := aux.Attendance; !jsonNull(raw) {
		u.BadRole = json.Unmarshal(raw, &u.Attendance) != nil
	}
	if raw := aux.AttendingSession; !jsonNull(raw) {
		var keys map[string]json.RawMessage
		if json.Unmarshal(raw, &keys) != nil {
			u.BadSession = true
			return nil
		}
		ref := &SessionRef{}
		for key, dst := range map[string]*string{"machine_id": &ref.MachineID, "session_id": &ref.SessionID} {
			if v := keys[key]; !jsonNull(v) && json.Unmarshal(v, dst) != nil {
				u.BadSession = true
			}
		}
		if !u.BadSession {
			u.AttendingSession = ref
		}
	}
	return nil
}

// RoomMeeting: an upload of r carried room once: its room conversation attends it, never another session.
func (r Recording) RoomMeeting() bool { return r.Conv != nil && r.Conv.Room != nil }

// Attending: r is attended now (its role is not off): the phone sends its live sentences and a room meeting's room is
// fed them, notes included.
func (r Recording) Attending() bool { return r.AttendanceRole() != AttendanceOff }

// Answering: Persona answers in r's meeting now (listener or participant): not while it is off, nor while its room only
// takes notes (#8851), when …/ask answers not_attending.
func (r Recording) Answering() bool {
	role := r.AttendanceRole()
	return role == AttendanceListener || role == AttendanceParticipant
}

// IsMeeting: r is a meeting as #2981 counts them: uploaded as one, or its minutes asked for.
func (r Recording) IsMeeting() bool { return r.Kind == RecordingMeeting || r.MinutesAsked }

// MarkedNotPrivate: r's private mark is false, chosen. A meeting with no mark counts as private.
func (r Recording) MarkedNotPrivate() bool {
	return r.Conv != nil && r.Conv.Private != nil && !*r.Conv.Private
}

// RoomUpload is an upload's room: the role its room conversation attends in.
type RoomUpload struct {
	Attendance string
	Bad        bool // attendance was not a string
}

// readRoomAndPrivate reads an upload's room and private (unmarshalRecordingUpload): a room that is not an object
// counts as absent, a private that is not a boolean is ignored.
func (u *RecordingUpload) readRoomAndPrivate(room, private json.RawMessage) {
	u.Room, u.Private = nil, nil
	if !jsonNull(room) {
		var keys map[string]json.RawMessage
		if json.Unmarshal(room, &keys) == nil && keys != nil {
			ru := &RoomUpload{}
			if v := keys["attendance"]; !jsonNull(v) && json.Unmarshal(v, &ru.Attendance) != nil {
				ru.Bad = true
			}
			u.Room = ru
		}
	}
	if !jsonNull(private) {
		var b bool
		if json.Unmarshal(private, &b) == nil {
			u.Private = &b
		}
	}
}

// SetRoom gives an upload a room in role (tests and the HTTP layer build uploads in Go).
func (u *RecordingUpload) SetRoom(role string) { u.Room = &RoomUpload{Attendance: role} }

// HasRoom: the upload carries room.
func (u *RecordingUpload) HasRoom() bool { return u.Room != nil }

// HubTranscribed: r's meeting.json has mode hub (an unreadable meeting.json: false).
func (r Recording) HubTranscribed() bool { return r.Conv != nil && r.Conv.Transcription.Hub() }

// readTranscription reads an upload's transcription and timeline (unmarshalRecordingUpload): a transcription that is
// not "device" or "hub" (another string, another type, null) names no mode; a timeline that ParseTimeline does not
// take is none. Neither fails the upload.
func (u *RecordingUpload) readTranscription(mode, timeline json.RawMessage) {
	u.Transcription, u.Timeline, u.TranscriptionProblem = "", nil, ""
	var s string
	if !jsonNull(mode) && json.Unmarshal(mode, &s) == nil {
		u.Transcription = TranscriptionMode(s)
	}
	if t, ok := ParseTimeline(timeline); ok {
		u.Timeline = &t
	}
}

// TranscriptionMode is s as a mode ("" when it names none).
func TranscriptionMode(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case TranscriptionDevice, TranscriptionHub:
		return s
	}
	return ""
}

// SetTranscription gives an upload a transcription mode and a timeline (tests and the HTTP layer build uploads in Go).
func (u *RecordingUpload) SetTranscription(mode string, t *Timeline) {
	u.Transcription, u.Timeline = mode, t
}

// CalendarTitle is the title of r's calendar item: the phone's hint, else the knowledge base's matched item ("" without
// either).
func (r *Recording) CalendarTitle() string {
	if r.Calendar != nil && r.Calendar.Title != "" {
		return r.Calendar.Title
	}
	if r.Meeting != nil {
		return r.Meeting.Name
	}
	return ""
}

// UploadCleaner checks an upload and puts its segments in order (empty ones dropped, Persona's echo marked) before it
// is stored: the recording module's rule of an upload, which the core's track recordings run on an upload the module's
// route hands them (recordinghost.Host.PutSpaceRecordingRoute).
type UploadCleaner func(u *RecordingUpload) error
