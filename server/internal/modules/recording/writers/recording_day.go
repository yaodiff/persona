package writers

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// All-day recordings (docs/assistant/day-recap.md, #4741): kind day, one recording per local day (or a few parts of
// one), created and repaired by the ordinary full-state PUT and grown by appends (POST /api/v1/recordings/{id}/segments)
// by position, as the phone transcribes what the watch records. A PUT sets the recording's base (its revision), which
// every append names, and the hub's count next: the positions it holds, empty segments included. Each stored segment
// keeps its position as N (its line number for the recap, day_recap.go), so notes cited by line number stay valid as
// text arrives; segments.json stays ordered by start. An append goes through the same undo-journalled commit as a PUT
// (recording_commit.go), so readers never see half of one. Its times are wall-clock seconds since started_at, pauses
// included, and its day, hours and recap time are in its own zone (tz). It is never a meeting: no calendar match, no
// minutes, no attending, no summary of its own; matched_rev and summarized_rev stay at its revision so an older
// manager after a rollback does neither either.

// Re-exported from recordingwire.
const (
	MaxAppendSegments = recordingwire.MaxAppendSegments
)

// Limits of a day recording's coverage.
const (
	maxCoverageGaps = 500
	maxGapReason    = 32 // bytes
)

// Re-exported from recordingwire.
const (
	GapPaused      = recordingwire.GapPaused
	GapInterrupted = recordingwire.GapInterrupted
	GapStopped     = recordingwire.GapStopped
	GapPending     = recordingwire.GapPending
	GapLost        = recordingwire.GapLost
	GapErased      = recordingwire.GapErased
)

// RecordingCoverage is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type RecordingCoverage = recordingwire.RecordingCoverage

// CoverageGap is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type CoverageGap = recordingwire.CoverageGap

// Re-exported from recordingwire.
var (
	ErrKindChanged = recordingwire.ErrKindChanged
	ErrNotDay      = recordingwire.ErrNotDay
	ErrBaseChanged = recordingwire.ErrBaseChanged
	ErrSegmentsGap = recordingwire.ErrSegmentsGap
	ErrDayFull     = recordingwire.ErrDayFull
)

// BaseChangedError is declared in recordingwire.
type BaseChangedError = recordingwire.BaseChangedError

// SegmentsGapError is declared in recordingwire.
type SegmentsGapError = recordingwire.SegmentsGapError

// DayFullError is declared in recordingwire.
type DayFullError = recordingwire.DayFullError

// RecordingAppend and RecordingAppended are declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type (
	RecordingAppend   = recordingwire.RecordingAppend
	RecordingAppended = recordingwire.RecordingAppended
)

// cleanUploadDay checks a day upload's own fields: day a real YYYY-MM-DD, tz an IANA zone (the owner's when empty), part
// 1–99 (1 when absent), the coverage mended. The calendar hint is dropped: a day recording is never a meeting.
func cleanUploadDay(u *RecordingUpload) error {
	u.Day, u.TZ = strings.TrimSpace(u.Day), strings.TrimSpace(u.TZ)
	if !validDay(u.Day) {
		return badRecording("day must be a date, YYYY-MM-DD")
	}
	if u.TZ != "" {
		if _, err := dayLocation(u.TZ); err != nil {
			return badRecording("tz must be an IANA time zone")
		}
	}
	switch {
	case u.Part == 0:
		u.Part = 1
	case u.Part < 1 || u.Part > 99:
		return badRecording("part must be 1 to 99")
	}
	c, err := mendCoverage(u.Coverage)
	if err != nil {
		return err
	}
	u.Coverage, u.Calendar = c, nil
	return nil
}

// validDay: s is a calendar date written YYYY-MM-DD.
func validDay(s string) bool {
	d, err := time.Parse(time.DateOnly, s)
	return err == nil && d.Format(time.DateOnly) == s
}

// dropUploadRoom ignores a day upload's room, private mark and timeline, as attending is: a role asked for is off with
// AttendanceNotMeeting (problem, normalizeAttendance's, wins when it has one). Its transcription mode is never stored
// (putTranscriptionFields): hub asked for is answered device with TranscriptionNotMeeting.
func dropUploadRoom(u *RecordingUpload, problem string) string {
	if u.Room != nil {
		if _, p := uploadRoomRole(u); problem == "" {
			problem = p
		}
		u.Room = nil
	}
	u.Private, u.Timeline = nil, nil
	return problem
}

// mendCoverage is c as the hub keeps it, mended rather than refused: to and the gaps clamped to 0 and above, a gap's
// ends put in order, empty gaps dropped, the rest sorted; overlapping gaps (and touching ones) of the same reason are
// made one, and where reasons differ the gap that starts first keeps the overlap. Past maxCoverageGaps the closest
// gaps of the same reason are joined (joinGaps); erased gaps, what the recap's withdrawal reads, are never joined.
// Reasons are trimmed to maxGapReason bytes, unknown ones kept. Only a number that is not finite is refused (JSON has
// none: Go callers).
func mendCoverage(c *RecordingCoverage) (*RecordingCoverage, error) {
	if c == nil {
		return nil, nil
	}
	if !finite(c.To) {
		return nil, badRecording("coverage.to must be a number")
	}
	out := &RecordingCoverage{To: max(0, c.To)}
	gaps := make([]CoverageGap, 0, len(c.Gaps))
	for _, g := range c.Gaps {
		if !finite(g.From) || !finite(g.To) {
			return nil, badRecording("coverage gaps must be numbers")
		}
		g.From, g.To = max(0, g.From), max(0, g.To)
		if g.To < g.From {
			g.From, g.To = g.To, g.From
		}
		if g.To == g.From {
			continue
		}
		g.Reason = cutBytes(strings.TrimSpace(strings.ToValidUTF8(g.Reason, "")), maxGapReason)
		gaps = append(gaps, g)
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].From != gaps[j].From {
			return gaps[i].From < gaps[j].From
		}
		return gaps[i].To < gaps[j].To
	})
	for _, g := range gaps {
		if n := len(out.Gaps); n > 0 {
			last := &out.Gaps[n-1]
			switch {
			case g.Reason == last.Reason && g.From <= last.To:
				last.To = max(last.To, g.To)
				continue
			case g.From < last.To:
				if g.From = last.To; g.To <= g.From {
					continue // all of it inside the one before
				}
			}
		}
		out.Gaps = append(out.Gaps, g)
	}
	out.Gaps = joinGaps(out.Gaps, func(a, b CoverageGap) bool { return a.Reason == b.Reason && a.Reason != GapErased })
	// Only a phone gone wrong alternates reasons this often: the closest of any reason then, erased still kept exactly,
	// so what the hub keeps stays bounded.
	out.Gaps = joinGaps(out.Gaps, func(a, b CoverageGap) bool { return a.Reason != GapErased && b.Reason != GapErased })
	return out, nil
}

// joinGaps joins the closest neighbours among gaps (sorted, apart) that may be joined, the first one's reason kept,
// until at most maxCoverageGaps are left or no pair may be.
func joinGaps(gaps []CoverageGap, may func(a, b CoverageGap) bool) []CoverageGap {
	extra := len(gaps) - maxCoverageGaps
	if extra <= 0 {
		return gaps
	}
	var pairs []int // i: gaps[i] and gaps[i+1]
	for i := range len(gaps) - 1 {
		if may(gaps[i], gaps[i+1]) {
			pairs = append(pairs, i)
		}
	}
	// Whether a pair may be joined is the same once its neighbours are: the closest extra pairs, in one pass.
	dist := func(i int) float64 { return gaps[i+1].From - gaps[i].To }
	slices.SortStableFunc(pairs, func(a, b int) int { return cmp.Compare(dist(a), dist(b)) })
	join := make([]bool, len(gaps))
	for _, i := range pairs[:min(extra, len(pairs))] {
		join[i] = true
	}
	out := gaps[:1]
	for i, g := range gaps[1:] {
		if join[i] {
			out[len(out)-1].To = g.To
			continue
		}
		out = append(out, g)
	}
	return out
}

// putDay sets a day recording's own fields from upload u (cleaned, a new base): its count, and the revisions an
// older manager would otherwise match and summarize it at. Its text is another (TextRev): every finished chunk is
// looked at again, and noted again when the PUT changed its lines.
func putDay(r *Recording, u *RecordingUpload) {
	r.Day, r.TZ, r.Part, r.Coverage = u.Day, u.TZ, u.Part, u.Coverage
	r.Base, r.Next = u.Revision, u.Next
	r.Calendar, r.Meeting, r.MinutesAsked = nil, nil, false
	dayGuard(r)
	r.TextRev++
	if !r.Complete {
		r.RecapComplete = false // complete again later: another final recap
	}
}

// dayGuard keeps a day recording's matched_rev and summarized_rev at its revision: an older manager after a rollback
// then neither matches it to a calendar item nor summarizes the whole day.
func dayGuard(r *Recording) {
	r.MatchedRev, r.SummarizedRev = r.Revision, r.Revision
}

// checkAppend refuses what is wrong with an append whatever the recording (400): no base, a revision not above it, a
// missing or negative from, too many segments, a time that is not a number. The coverage is mended.
func checkAppend(a *RecordingAppend) error {
	switch {
	case a.Base <= 0:
		return badRecording("base is required")
	case a.Revision <= a.Base:
		return badRecording("revision must be above base")
	case a.From == nil || *a.From < 0:
		return badRecording("from must be 0 or more")
	case len(a.Segments) > MaxAppendSegments:
		return badRecording("at most %d segments a request", MaxAppendSegments)
	case a.Duration != nil && !finite(*a.Duration):
		return badRecording("duration must be a number")
	}
	for _, sg := range a.Segments {
		if !finite(sg.Start) || !finite(sg.End) {
			return badRecording("segment times must be numbers")
		}
	}
	if a.Bookmarks != nil {
		for _, b := range *a.Bookmarks {
			if !finite(b.At) {
				return badRecording("bookmark times must be numbers")
			}
		}
	}
	c, err := mendCoverage(a.Coverage)
	a.Coverage = c
	return err
}

// AppendRecording appends segments to day recording id by position, a.Segments[0] at position *a.From: positions the
// hub holds already are skipped (a retry stores nothing twice), the rest stored after them, in any time order
// (segments.json stays ordered by start). Its other fields apply only from a request whose revision is at least the
// stored one, and only when present; ended_at and complete only ever get set here. The stored revision becomes the
// larger of the two; TextRev counts the append whatever its revision. Errors in the contract's order:
// ErrRecordingDeleted, ErrNotFound, ErrReadOnly, ErrBadRecording, then RecordingExpired (no error: before
// recording_days or trimmed; nothing written), ErrNotDay, a BaseChangedError, a SegmentsGapError, a DayFullError
// (nothing of the request stored). The stored segments that cannot be read are an error too, with nothing written:
// the append never takes the place of the day.
func (e *engine) AppendRecording(rawID string, a RecordingAppend) (RecordingAppended, error) {
	id, ok := RecordingID(rawID)
	out := RecordingAppended{ID: id}
	if !ok {
		return out, ErrNotFound
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if gone := st.tombstoned(id); gone {
		return out, ErrRecordingDeleted
	}
	r, ok := st.Recording(id)
	if !ok {
		return out, ErrNotFound
	}
	out.Revision, out.Next, out.Complete = r.Revision, r.Next, r.Complete
	if st.ReadOnly() {
		return out, ErrReadOnly
	}
	if err := checkAppend(&a); err != nil {
		return out, err
	}
	now := e.now()
	if r.Trimmed() || pastRetention(r.StartedAt, now, e.RecordingDays()) {
		out.Status = RecordingExpired
		return out, nil
	}
	switch {
	case r.Kind != RecordingDay:
		return out, ErrNotDay
	case r.Base != a.Base:
		return out, BaseChangedError{Base: r.Base, Next: r.Next}
	case *a.From > r.Next:
		return out, SegmentsGapError{Next: r.Next}
	}
	segs, err := st.daySegments(r)
	if err != nil {
		return out, err
	}
	stored := 0
	for _, sg := range segs {
		stored += len(sg.Text)
	}
	fresh := a.Segments[min(r.Next-*a.From, len(a.Segments)):]
	add, size := make([]RecordingSegment, 0, len(fresh)), 0
	for i, sg := range fresh {
		sg.Text, sg.N, sg.Source = strings.TrimSpace(sg.Text), r.Next+i+1, "" // only the hub sets a source
		sg.Echo = false                                                       // Persona never speaks in an all-day recording (as clean does for the PUT)
		if sg.Text == "" {
			continue // its position is taken, and it is left out of the transcript
		}
		size += len(sg.Text)
		add = append(add, sg)
	}
	if r.Next+len(fresh) > maxRecordingSegments || stored+size > maxRecordingText {
		return out, DayFullError{Next: r.Next}
	}
	before, _ := json.Marshal(r)
	zone := st.RecordingZone(r)
	wasComplete, oldTo := r.Complete, coverageTo(r)
	if a.Revision >= r.Revision {
		if a.Duration != nil {
			r.Duration = *a.Duration
		}
		if a.EndedAt != nil {
			t := a.EndedAt.UTC()
			r.EndedAt = &t
		}
		if a.Title != nil {
			r.Title = oneLine(*a.Title)
		}
		if a.Bookmarks != nil {
			b := slices.Clone(*a.Bookmarks)
			sort.SliceStable(b, func(i, j int) bool { return b[i].At < b[j].At })
			r.Bookmarks = b
		}
		if a.Coverage != nil {
			r.Coverage = a.Coverage
		}
		if a.Complete != nil && *a.Complete {
			r.Complete = true
		}
	}
	r.Revision = max(r.Revision, a.Revision)
	r.Next += len(fresh)
	if after, _ := json.Marshal(r); len(fresh) == 0 && string(before) == string(after) {
		out.Status = RecordingStored // a retry of what is stored already
		return out, nil
	}
	all := append(segs, add...)
	sortDaySegments(all)
	r.Segments = len(all)
	r.TextRev++
	for _, sg := range add {
		r.Chars += len([]rune(sg.Text))
	}
	dayGuard(r)
	planMinutes(r)
	planRecap(r)
	r.Name = e.recordingName(r)
	r.Updated = now.UTC()
	if err := st.commitRecording(r, all); err != nil {
		return out, err
	}
	out.Status, out.Revision, out.Next, out.Complete = RecordingStored, r.Revision, r.Next, r.Complete
	hour := func(sec float64) time.Time { return hourStart(r.StartedAt.Add(seconds(sec)), zone) }
	out.Wake = r.Complete && !wasComplete || hour(coverageTo(r)).After(hour(oldTo))
	for _, sg := range add {
		if hour(sg.Start).Before(hour(oldTo)) {
			out.Wake = true // a late sentence in an hour read already: that hour is read again
		}
	}
	st.notify(KindRecording, id, "metadata")
	st.notify(KindRecording, id, "messages")
	e.index(KindRecording, id, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	return out, nil
}

// daySegments are day recording r's stored segments (segments.json, read through a pending undo journal like every
// read), refused when they cannot be read, do not parse, or are not as many as r says it holds: an append must never
// commit its own batch as the whole day.
func (s *store) daySegments(r *Recording) ([]RecordingSegment, error) {
	raw, err := s.recordingRead(r.ID, "segments.json")
	if err != nil {
		return nil, fmt.Errorf("recording %s: segments: %w", r.ID, err)
	}
	var segs []RecordingSegment
	if raw != nil {
		if err := json.Unmarshal(raw, &segs); err != nil {
			return nil, fmt.Errorf("recording %s: segments: %w", r.ID, err)
		}
	}
	if len(segs) != r.Segments {
		return nil, fmt.Errorf("recording %s: segments: %d stored, %d expected", r.ID, len(segs), r.Segments)
	}
	return segs, nil
}

// sortDaySegments orders a day recording's segments as segments.json keeps them: by start, then position.
func sortDaySegments(segs []RecordingSegment) { recordingwire.SortDaySegments(segs) }

// hourStart is the start of t's clock hour in zone, by the zone's offset at t, so every hour is an hour of absolute
// time: the hour a fall-back repeats is two of them, never one of two hours.
func hourStart(t time.Time, zone *time.Location) time.Time { return recordingwire.HourStart(t, zone) }
