package writers

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Limits of one upload.
const (
	maxRecordingSegments = recordingwire.MaxRecordingSegments
	maxRecordingText     = recordingwire.MaxRecordingText
)

// RecordingBookmark is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RecordingBookmark = recordingwire.RecordingBookmark

// RecordingCalendar is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RecordingCalendar = recordingwire.RecordingCalendar

// Limits of an upload's calendar hint.
const (
	maxCalendarTitle     = 200
	maxCalendarAttendees = 100
	maxCalendarName      = 100
)

// RecordingUpload is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RecordingUpload = recordingwire.RecordingUpload

// MeetingRef is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type MeetingRef = recordingwire.MeetingRef

// RecordingState is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RecordingState = recordingwire.RecordingState

// Re-exported from recordingwire.
const (
	RecordingStored  = recordingwire.RecordingStored
	RecordingStale   = recordingwire.RecordingStale
	RecordingExpired = recordingwire.RecordingExpired
)

// RecordingPut is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RecordingPut = recordingwire.RecordingPut

// cleanUpload checks an upload and puts its segments in order (empty ones dropped).
func cleanUpload(u *RecordingUpload) error {
	if u.Revision <= 0 {
		return badRecording("revision must be > 0")
	}
	if u.StartedAt.IsZero() {
		return badRecording("started_at is required")
	}
	if u.Device != "phone" && u.Device != "watch" {
		return badRecording("device must be phone or watch")
	}
	// A day upload over the limits is day_full, not refused here: the hub's count goes with it (recording_day.go).
	day := strings.EqualFold(strings.TrimSpace(u.Kind), RecordingDay)
	if len(u.Segments) > maxRecordingSegments {
		if !day {
			return badRecording("at most %d segments", maxRecordingSegments)
		}
		u.DayFull = true
	}
	u.Next = len(u.Segments)
	// A fresh slice: the caller's upload (its Segments' backing array) is never changed.
	segs := make([]RecordingSegment, 0, len(u.Segments))
	total := 0
	for i, sg := range u.Segments {
		sg.Text, sg.N, sg.Source, sg.Speaker, sg.SpeakerUnsure = strings.TrimSpace(sg.Text), 0, "", "", false // only the hub sets source and speaker labels
		if day {
			sg.N = i + 1    // its position, empty ones counted
			sg.Echo = false // Persona never speaks in an all-day recording
		}
		if sg.Text == "" {
			continue
		}
		total += len(sg.Text)
		segs = append(segs, sg)
	}
	if total > maxRecordingText {
		if !day {
			return badRecording("the transcript is over %d bytes", maxRecordingText)
		}
		u.DayFull = true
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	u.Segments = segs
	u.Bookmarks = slices.Clone(u.Bookmarks)
	if day {
		u.Spoken = nil
	}
	// Persona's echo: the phone's mark, or the same rule against the stretches it says Persona spoke in.
	markEcho(u.Segments, u.Spoken)
	sort.SliceStable(u.Bookmarks, func(i, j int) bool { return u.Bookmarks[i].At < u.Bookmarks[j].At })
	u.Title = oneLine(u.Title)
	u.Language = strings.TrimSpace(u.Language)
	switch u.Kind = strings.ToLower(strings.TrimSpace(u.Kind)); u.Kind {
	case "", RecordingNote:
		u.Kind = RecordingNote
	case RecordingMeeting:
	case RecordingDay:
		return cleanUploadDay(u)
	default:
		return badRecording("kind must be meeting, note or day")
	}
	u.Day, u.TZ, u.Part, u.Coverage = "", "", 0, nil // a day recording's only
	return cleanUploadCalendar(u)
}

// cleanUploadCalendar trims the calendar hint, dropping it when it says nothing.
func cleanUploadCalendar(u *RecordingUpload) error {
	c := u.Calendar
	if c == nil {
		return nil
	}
	c.Title, c.Location = strings.Join(strings.Fields(c.Title), " "), strings.Join(strings.Fields(c.Location), " ")
	if len([]rune(c.Title)) > maxCalendarTitle || len([]rune(c.Location)) > maxCalendarTitle {
		return badRecording("calendar title and location are at most %d characters", maxCalendarTitle)
	}
	if len(c.Attendees) > maxCalendarAttendees {
		return badRecording("at most %d calendar attendees", maxCalendarAttendees)
	}
	names := c.Attendees[:0]
	for _, n := range c.Attendees {
		n = strings.Join(strings.Fields(n), " ")
		if len([]rune(n)) > maxCalendarName {
			return badRecording("a calendar attendee is at most %d characters", maxCalendarName)
		}
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	c.Attendees = names
	if c.Title == "" && c.Location == "" && len(c.Attendees) == 0 {
		u.Calendar = nil
	}
	return nil
}

// PutRecording stores an upload (the whole state of recording id): RecordingStale when a newer revision is there,
// RecordingExpired when it started before the retention or was trimmed, ErrRecordingDeleted after a delete.
// Attendance fields that are wrong never fail it: attending is then off, and the result says why
// (normalizeAttendance). Engine.LiveChanged hears of a stored upload of a recording that attends or attended until now.
func (e *engine) PutRecording(rawID string, u RecordingUpload, now time.Time) (RecordingPut, error) {
	put, live, err := e.putRecording(rawID, u, now)
	if err == nil && live && e.LiveChanged != nil {
		e.LiveChanged(put.id)
	}
	return put.RecordingPut, err
}

type recordingPut struct {
	RecordingPut
	id string
}

// putRecording is PutRecording; live reports a stored upload the attending feeder should look at.
func (e *engine) putRecording(rawID string, u RecordingUpload, now time.Time) (put recordingPut, live bool, err error) {
	put.Attendance = AttendanceOff
	put.Room = u.Room != nil
	// Whatever the upload became (stored, stale, expired), the mode in effect after it.
	defer func() {
		put.Transcription, put.TranscriptionProblem = transcriptionOf(put.Meeting, u.Transcription, u.Kind, u.TranscriptionProblem)
	}()
	if e.Store.ReadOnly() {
		return put, false, ErrReadOnly
	}
	id, ok := RecordingID(rawID)
	if !ok {
		return put, false, badRecording("id must be a UUID")
	}
	put.id = id
	if err := cleanUpload(&u); err != nil {
		return put, false, err
	}
	put.Segments = u.Segments
	put.AttendanceProblem = normalizeAttendance(&u)
	day := u.Kind == RecordingDay
	if day {
		put.AttendanceProblem, put.Room = dropUploadRoom(&u, put.AttendanceProblem), false
	}
	put.Revision = u.Revision
	st := e.Store
	var changedParts []string
	defer func() {
		if len(changedParts) > 0 {
			e.meetingChanged(id, changedParts)
		}
	}()
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if gone := st.tombstoned(id); gone {
		return put, false, ErrRecordingDeleted
	}
	r, exists := st.Recording(id)
	if exists {
		put.Revision, put.Base, put.Next = r.Revision, r.Base, r.Next
	}
	if pastRetention(u.StartedAt, now, e.RecordingDays()) {
		put.Status = RecordingExpired
		return put, false, nil
	}
	if exists {
		put.Room, put.Meeting = put.Room || r.RoomMeeting(), r.Conv
	}
	if exists && r.Trimmed() {
		// Its transcript went with the retention (recording_days made longer since lets it back in the window): the
		// minutes stay what they are, and the hub takes no transcript back.
		put.Status = RecordingExpired
		return put, false, nil
	}
	if exists && (r.Kind == RecordingDay) != day {
		return put, false, ErrKindChanged
	}
	if exists && r.Revision > u.Revision {
		put.Status, put.Attendance = RecordingStale, r.AttendanceRole()
		putNamed(&put.RecordingPut, r)
		return put, false, nil
	}
	if u.DayFull {
		return put, false, DayFullError{Next: put.Next}
	}
	rebase, recapped := false, false
	if day && exists && r.Base != u.Revision {
		// A new base: a recap made from the old one loses at once what the new transcript no longer says.
		rebase = true
		if recapped, err = e.repairDayRecap(r, &u); err != nil {
			return put, false, err
		}
	}
	wasAttending := exists && r.Attending()
	meeting, parts, err := e.putMeetingFields(id, &u, now)
	if err != nil {
		return put, false, err
	}
	changedParts = append(changedParts, parts...)
	if meeting != nil && meeting.Room != nil {
		put.Room, put.AttendanceProblem = true, u.RoomProblem
	}
	put.Meeting = meeting
	// A newer transcript (not a retry of the accepted upload): its minutes are pushed as before.
	advanced := !exists || u.Revision > r.Revision
	prevDuration := 0.0
	if !exists {
		r = &Recording{ID: id}
	} else {
		prevDuration = r.Duration
	}
	r.Conv = meeting
	r.Revision, r.StartedAt, r.EndedAt, r.Duration = u.Revision, u.StartedAt.UTC(), u.EndedAt, u.Duration
	if r.EndedAt != nil {
		t := r.EndedAt.UTC()
		r.EndedAt = &t
	}
	// An ended hub-mode meeting is as long as the audio the hub holds when the upload says less (#9587): a phone killed
	// mid-segment loses the segment it was writing and ends the recording before it, while the hub got that audio.
	if r.EndedAt != nil && r.HubTranscribed() {
		if held := e.audioHeldEnd(id); held > r.Duration {
			r.Duration = held
			if end := r.StartedAt.Add(time.Duration(held * float64(time.Second))); end.After(*r.EndedAt) {
				r.EndedAt = &end
			}
		}
	}
	if r.Renamed != "" && u.Title != r.Title { // the phone renamed it since the web did: the newer name wins (#8877)
		r.Renamed, r.RenamedAt = "", nil
	}
	r.Language, r.Device, r.Title, r.Bookmarks, r.Complete = u.Language, u.Device, u.Title, u.Bookmarks, u.Complete
	r.Kind, r.Calendar = u.Kind, u.Calendar
	r.Attendance, r.AttendingSession = u.Attendance, u.AttendingSession // never a room meeting's (putMeetingFields)
	// chars counts the transcript of record without Persona's echo, which is not the meeting's text (a meeting of
	// only that has none, planMinutes): in hub mode the hub's sentences too (a phone that wrote nothing).
	r.Segments, r.Chars = len(u.Segments), recordChars(u.Segments)
	if r.HubTranscribed() {
		r.Chars = recordChars(st.TranscriptOfRecord(true, u.Segments, st.hubSegmentsOf(id)))
	}
	if day {
		putDay(r, &u)
		if rebase {
			rebaseRecap(r, recapped, now)
		}
	}
	if advanced {
		r.MinutesQuiet = 0 // a retry keeps a generation made again only for a photo quiet
	}
	planMinutes(r) // after Chars: a meeting that completed with no text gets none
	planRecap(r)
	r.Name = e.recordingName(r)
	r.Updated = now.UTC()
	if day && exists {
		// The notes of a chunk whose lines the upload changed (an erase above all) quote what it no longer says: they go
		// now, not with the next note written. Those of the others stay theirs.
		if err := st.pruneRecapNotes(r, u.Segments); err != nil {
			return put, false, err
		}
	}
	if err := st.commitRecording(r, u.Segments); err != nil {
		return put, false, err
	}
	if r.HubTranscribed() && r.Conv != nil && r.Conv.Speakers != nil && r.Conv.Speakers.State == SpeakerStateReady {
		trimTo := 0.0
		if exists && prevDuration > 0 && r.Duration > 0 && r.Duration < prevDuration {
			trimTo = r.Duration
		}
		if spParts, minChanged, err := e.recomputeSpeakersLocked(r, trimTo); err != nil {
			e.logf("recording %s: speakers recompute: %v", id, err)
		} else {
			for _, p := range spParts {
				if !slices.Contains(changedParts, p) {
					changedParts = append(changedParts, p)
				}
			}
			if minChanged {
				st.notify(KindRecording, id, "summary")
			}
		}
	}
	put.Status, put.Attendance = RecordingStored, r.AttendanceRole()
	put.Revision, put.Base, put.Next = r.Revision, r.Base, r.Next
	putNamed(&put.RecordingPut, r)
	put.TitleDue = liveTitleDue(r, now)
	st.notify(KindRecording, id, "metadata")
	st.notify(KindRecording, id, "messages")
	e.index(KindRecording, id, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	if r.EndedAt != nil {
		// The end batch is timed from when the hub first saw the end (live.go); the upload stands either way, and the
		// next one carrying ended_at tries again.
		if err := st.liveEnded(id, r.Attending() || r.RoomMeeting(), now); err != nil {
			e.logf("recording %s: live end: %v", id, err)
		}
	}
	return put, wasAttending || r.Attending() || r.RoomMeeting(), nil
}

// RecordingStates is every recording kept (trimmed ones too) and every tombstone, for the phone to reconcile.
func (e *engine) RecordingStates() (kept []RecordingState, deleted []string) {
	kept, deleted = []RecordingState{}, []string{}
	for _, r := range e.Store.Recordings() {
		s := RecordingState{ID: r.ID, Revision: r.Revision, Complete: r.Complete, StartedAt: r.StartedAt,
			MinutesState: r.MinutesState, TrimmedAt: r.TrimmedAt}
		if s.NameSource = r.NameSource(); s.NameSource != "" {
			s.Name = r.Name
		}
		if r.Kind == RecordingDay {
			next, base := r.Next, r.Base
			s.Kind, s.Day, s.Part, s.Next, s.Base = r.Kind, r.Day, r.Part, &next, &base
			s.RecapState, s.RecapFinal, s.RecapFinalAt = r.RecapState, r.RecapFinal, r.RecapFinalAt
		}
		kept = append(kept, s)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].StartedAt.After(kept[j].StartedAt) })
	for _, id := range e.Store.deleted() {
		deleted = append(deleted, id)
	}
	sort.Strings(deleted)
	return kept, deleted
}
