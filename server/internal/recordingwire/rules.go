package recordingwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// The rules of a recording's stored state that both the knowledge base (its readers, retention and the trim) and the
// recording module's writers apply: pure functions of the types of this package, moved here from internal/kb so that
// one definition serves both sides of the storage port (docs/plans/plan-kb-recording-port.md, section 3.5).

const (
	// MinutesTries: unusable answers for one revision before the minutes (or a day recap) are failed.
	MinutesTries = 3
	// MinutesTimeoutTries: tries of one revision that ran out of time before the minutes are failed.
	MinutesTimeoutTries = 4
	// MinutesMinCalendar, seconds: shorter recordings are not meetings just because a calendar item overlaps.
	MinutesMinCalendar = 5 * 60
	// MinutesLineRunes: a transcript line, or a photo's text, longer than this is cut where the model reads it.
	MinutesLineRunes = 4000
	// MinutesHubWait is how long a hub-mode meeting's minutes wait for its final transcript after the complete upload.
	MinutesHubWait = 45 * time.Minute
	// SpeakersFinalWait is the minimum wait for speaker separation after the final transcript was stored (#17674).
	SpeakersFinalWait = 10 * time.Minute
	// SpeakersAudioDivisor and SpeakersAudioPad define the audio-proportional speaker wait limit: duration / 5 + 2 min.
	SpeakersAudioDivisor = 5
	SpeakersAudioPad     = 2 * time.Minute
	// PhotoLabel marks a photo's line in photos.md and in the minutes' input.
	PhotoLabel = "photo:"
	// The kinds of knowledge-base entry the recordings' writers name.
	KindRecording = "recordings"
	KindMeeting   = "meetings"
	KindEvent     = "events"
	// DirAudio is the subdirectory under DirAttachments holding a hub-mode
	// meeting's ephemeral audio (recordings/<id>/attachments/audio/); backups
	// omit it so audio never outlives post-minutes deletion.
	DirAudio = "audio"
	// The day recap's files next to a day recording's meta.json.
	RecapFile      = "recap.json"
	RecapMarkdown  = "recap.md"
	RecapNotesFile = "recap_notes.json"
	// AITitleMinutes is Recording.AITitleFrom of a title the minutes gave.
	AITitleMinutes = "minutes"
)

// IsMeetingAudioPath reports whether kbRelPath (slash-separated, relative to a
// knowledge-base root) is a recording's ephemeral hub-transcribed audio
// directory or a path inside it: recordings/<id>/attachments/audio[/...].
func IsMeetingAudioPath(kbRelPath string) bool {
	clean := path.Clean(strings.TrimPrefix(kbRelPath, "/"))
	parts := strings.Split(clean, "/")
	return len(parts) >= 4 &&
		parts[0] == KindRecording &&
		parts[1] != "" && parts[1] != "." && parts[1] != ".." &&
		parts[2] == string(DirAttachments) &&
		parts[3] == DirAudio
}

func ClockAt(start time.Time, sec float64, zone *time.Location) string {
	return start.Add(time.Duration(sec * float64(time.Second))).In(zone).Format("15:04:05")
}

// RecordingEnd is when r ended: ended_at, else its start plus what was recorded (at least a minute).
func RecordingEnd(r *Recording) time.Time {
	end := r.StartedAt.Add(time.Duration(r.Duration * float64(time.Second)))
	if r.EndedAt != nil && r.EndedAt.After(end) {
		end = *r.EndedAt
	}
	if !end.After(r.StartedAt.Add(time.Minute)) {
		end = r.StartedAt.Add(time.Minute)
	}
	return end
}

// MinutesSpan is the recording's start and end in the user's zone, to the second: the span minutes are about.
func MinutesSpan(r *Recording, zone *time.Location) (time.Time, time.Time) {
	return r.StartedAt.In(zone).Truncate(time.Second), RecordingEnd(r).In(zone).Truncate(time.Second)
}

// CoverageTo is how far r's text reaches, in seconds since started_at: its coverage's to, its duration without one
// (the recap reads up to its last segment then, recapLines).
func CoverageTo(r *Recording) float64 {
	if r.Coverage != nil {
		return r.Coverage.To
	}
	return r.Duration
}

// DayStart is the start of day (YYYY-MM-DD) in zone, and of the day after.
func DayStart(day string, zone *time.Location) (time.Time, time.Time, bool) {
	d, err := time.ParseInLocation(time.DateOnly, day, zone)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return d, time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, zone), true
}

// GetsMinutes: the recording gets minutes. A day recording never does (it gets a recap, day_recap.go).
func GetsMinutes(r *Recording) bool {
	if r.Kind == RecordingDay {
		return false
	}
	return r.Kind == RecordingMeeting || r.MinutesAsked || r.Meeting != nil && r.Duration >= MinutesMinCalendar
}

// PlanMinutes sets MinutesState from the rest: pending until minutes of the current revision are written, failed after
// MinutesTries unusable answers for it or MinutesTimeoutTries tries that ran out of time; minutes of an older revision
// keep showing meanwhile. A meeting that completed with no text gets none ("", docs/assistant/meeting-minutes.md
// "Attendance fields"): an attended one is uploaded before anything was said, and may end so.
func PlanMinutes(r *Recording) {
	switch {
	case !GetsMinutes(r):
		r.MinutesState = "" // minutes made while it was one stay readable; nothing more is made
	case r.Trimmed():
		r.MinutesState = MinutesReady // the transcript is gone: the minutes kept are all there will be
	case r.Complete && r.Chars == 0:
		r.MinutesState = ""
	case r.MinutesRev > 0 && r.MinutesRev >= r.Revision:
		r.MinutesState = MinutesReady
	case r.MinutesFailRev == r.Revision && (r.MinutesFails >= MinutesTries || r.MinutesTimeouts >= MinutesTimeoutTries):
		r.MinutesState = MinutesFailed
	default:
		r.MinutesState = MinutesPending
	}
}

// PlanRecap sets a day recording's RecapState from the rest: pending while a recap is due, failed once the model could
// not write it, ready once one was written and none is due, absent before any. A trimmed one is due no more: its
// recap kept is all there will be.
func PlanRecap(r *Recording) {
	if r.Kind != RecordingDay {
		return
	}
	if r.Trimmed() {
		r.RecapDue = nil
	}
	switch {
	case r.RecapDue != nil && RecapFailed(r.RecapDue):
		r.RecapState = MinutesFailed
	case r.RecapDue != nil:
		r.RecapState = MinutesPending
	case r.RecapVersion > 0:
		r.RecapState = MinutesReady
	default:
		r.RecapState = ""
	}
}

// RecapFailed: the model could not write it after the minutes' tries.
func RecapFailed(t *RecapTarget) bool {
	return t.Fails >= MinutesTries || t.Timeouts >= MinutesTimeoutTries
}

// PhotoLine is a photo as photos.md and the minutes' input have it: "[P3] 14:32:05 photo: caption · text · note".
func PhotoLine(n int, clock, words string) string {
	return strings.TrimRight("[P"+strconv.Itoa(n)+"] "+clock+" "+PhotoLabel+" "+words, " ")
}

// MinutesPhotos are the photos m cites (all four sections, and the overview's bullets and conclusions, which cite only
// photos an item cites too); empty without minutes.
func MinutesPhotos(m *Minutes) map[int]bool {
	out := map[int]bool{}
	if m == nil {
		return out
	}
	add := func(n int) {
		if n > 0 {
			out[n] = true
		}
	}
	for _, p := range m.Points {
		add(p.Photo)
	}
	for _, d := range m.Decisions {
		add(d.Photo)
	}
	for _, a := range m.Actions {
		add(a.Photo)
	}
	for _, o := range m.Open {
		add(o.Photo)
	}
	if o := m.Overview; o != nil {
		for _, c := range o.Conclusions {
			add(c.Photo)
		}
		for _, g := range o.Groups {
			for _, t := range g.Topics {
				for _, b := range t.Bullets {
					add(b.Photo)
				}
			}
		}
	}
	return out
}

// HubTranscription is r's hub-mode transcription, nil when r is not transcribed on the hub.
func HubTranscription(r *Recording) *MeetingTranscription {
	if !r.HubTranscribed() {
		return nil
	}
	return r.Conv.Transcription
}

// TranscriptionDone: r is transcribed on the hub and its final pass has run or failed.
func TranscriptionDone(r *Recording) bool {
	t := HubTranscription(r)
	return t != nil && t.Done()
}

// TranscriptionHolds: r's minutes wait for its final transcript: hub mode, the state not final nor failed, at most
// MinutesHubWait after the complete upload.
func TranscriptionHolds(r *Recording, now time.Time) bool {
	t := HubTranscription(r)
	return t != nil && !t.Done() && !t.CompleteAt.IsZero() && now.Before(t.CompleteAt.Add(MinutesHubWait))
}

// SpeakersWaitDeadline is the persisted UTC instant past which minutes stop waiting for speaker separation (#17674):
// the later of FinalAt + SpeakersFinalWait and Since + (duration / SpeakersAudioDivisor + SpeakersAudioPad). Zero when
// neither timestamp is recorded or r is not in waiting state after hub transcription ended. Because every input is
// stored in meeting.json / meta.json, a restart never extends the deadline.
func SpeakersWaitDeadline(r *Recording) time.Time {
	if r == nil || r.Conv == nil || r.Conv.Speakers == nil || r.Conv.Speakers.State != SpeakerStateWaiting {
		return time.Time{}
	}
	t := HubTranscription(r)
	if t == nil || !t.Done() {
		return time.Time{}
	}
	sp := r.Conv.Speakers
	var d1, d2 time.Time
	if !t.FinalAt.IsZero() {
		d1 = t.FinalAt.Add(SpeakersFinalWait)
	}
	if !sp.Since.IsZero() {
		dur := r.Duration
		if dur <= 0 && r.EndedAt != nil && !r.StartedAt.IsZero() && r.EndedAt.After(r.StartedAt) {
			dur = r.EndedAt.Sub(r.StartedAt).Seconds()
		}
		if dur < 0 || math.IsNaN(dur) || math.IsInf(dur, 0) {
			dur = 0
		}
		audioWait := time.Duration(dur/float64(SpeakersAudioDivisor)*float64(time.Second)) + SpeakersAudioPad
		d2 = sp.Since.Add(audioWait)
	}
	return later(d1, d2)
}

// SpeakersHolds: r's minutes wait for speaker separation: speakers.state is waiting, hub transcription has ended
// (final or failed), and now is before SpeakersWaitDeadline(r) (#17674).
func SpeakersHolds(r *Recording, now time.Time) bool {
	deadline := SpeakersWaitDeadline(r)
	return !deadline.IsZero() && now.Before(deadline)
}

// HourStart is the start of t's clock hour in zone, by the zone's offset at t, so every hour is an hour of absolute
// time: the hour a fall-back repeats is two of them, never one of two hours.
func HourStart(t time.Time, zone *time.Location) time.Time {
	_, off := t.In(zone).Zone()
	shift := time.Duration(off) * time.Second
	return t.Add(shift).Truncate(time.Hour).Add(-shift).In(zone)
}

// SortDaySegments orders a day recording's segments as segments.json keeps them: by start, then position.
func SortDaySegments(segs []kbwire.RecordingSegment) {
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].Start != segs[j].Start {
			return segs[i].Start < segs[j].Start
		}
		return segs[i].N < segs[j].N
	})
}

// MinutesTaskKey is what makes an action the same task in minutes made again: who and what.
func MinutesTaskKey(a MinutesAction) string { return a.Who + "\x00" + a.What }

// ErrRecordingDeleted: the recording was deleted (its tombstone is there); ErrBadRecording wraps what is wrong with a
// request about one.
var (
	ErrRecordingDeleted = errors.New("this recording was deleted")
	ErrBadRecording     = errors.New("invalid recording")
)

// ErrTranscriptExpired: the recording was trimmed; only its minutes are kept.
var ErrTranscriptExpired = errors.New("the transcript expired; only the minutes are kept")

// The model purposes of the meeting recordings' calls (docs/assistant/model-config.md): the minutes job, a question
// asked during a meeting, an attended meeting's room conversation.
const (
	PurposeMeetingMinutes = "meeting.minutes"
	PurposeMeetingAnswer  = "meeting.answer"
	PurposeMeetingRoom    = "meeting.room"
	// PurposeMeetingTitle is the live title's: Information processing's, as chat titles are.
	PurposeMeetingTitle = "meeting.title"
)

// What an upload and the hub's transcript hold at most: segments, and bytes of text.
const (
	MaxRecordingSegments = 20000
	MaxRecordingText     = 4 << 20
)

// RecordingID is id as stored (lowercase), false when it is not a UUID (8-4-4-4-12 hexadecimal digits).
func RecordingID(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) != 36 {
		return id, false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return id, false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return id, false
			}
		}
	}
	return id, true
}

var dayZones sync.Map // IANA name → *time.Location, loaded once

// DayLocation is the IANA zone name ("Local" is not one).
func DayLocation(name string) (*time.Location, error) {
	if l, ok := dayZones.Load(name); ok {
		return l.(*time.Location), nil
	}
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	dayZones.Store(name, l)
	return l, nil
}

// RecordingZone is the zone r's clock times are in: a day recording's own (tz), else (and when it cannot be loaded)
// user, the knowledge base's zone.
func RecordingZone(r *Recording, user *time.Location) *time.Location {
	if r.Kind == RecordingDay && r.TZ != "" {
		if l, err := DayLocation(r.TZ); err == nil {
			return l
		}
	}
	return user
}

// DaySpan is the span day recording r covers as of now: from its start to the end of what it holds, or to its end
// once it has one; one still being recorded reaches now, at most the midnight that ends its day (in its zone; user is
// the knowledge base's zone, RecordingZone).
func DaySpan(r *Recording, now time.Time, user *time.Location) (time.Time, time.Time) {
	end := r.StartedAt.Add(time.Duration(CoverageTo(r) * float64(time.Second)))
	switch {
	case r.EndedAt != nil:
		end = later(end, *r.EndedAt)
	case !r.Complete:
		if _, midnight, ok := DayStart(r.Day, RecordingZone(r, user)); ok {
			end = later(end, earlier(now, midnight))
		}
	}
	return r.StartedAt, end
}

// RecordedWithin: day recording r's span as of now (DaySpan) overlaps from–to.
func RecordedWithin(r *Recording, now, from, to time.Time, user *time.Location) bool {
	start, end := DaySpan(r, now, user)
	return start.Before(to) && (end.After(from) || !start.Before(from))
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// SettleTranscription keeps t's own rules after a change: a hub-mode transcription has a state; once final or failed the final
// pass has started and ended; no empty gaps or timeline.
func SettleTranscription(t *MeetingTranscription, now time.Time) {
	if t.Mode != TranscriptionHub {
		*t = MeetingTranscription{Mode: t.Mode}
		return
	}
	if t.State == "" {
		t.State = TranscriptionLive
	}
	if t.Done() {
		if t.FinalAt.IsZero() {
			t.FinalAt = now.UTC()
		}
		if t.FinalFrom.IsZero() {
			t.FinalFrom = t.FinalAt
		}
		t.FinalDone = 0
	}
	if len(t.Gaps) == 0 {
		t.Gaps = nil
	}
	if t.Timeline != nil && len(t.Timeline.Roles) == 0 && len(t.Timeline.Speaking) == 0 {
		t.Timeline = nil
	}
}

// CleanLive is a sentence as the live log keeps it: a bad one is mended, never refused, since positions stay
// contiguous. A negative start is 0, an end before the start the start; the text is one line, cut to MaxLiveText bytes
// at a character boundary.
func CleanLive(sg LiveSegment) LiveSegment {
	if math.IsNaN(sg.Start) || math.IsInf(sg.Start, 0) || sg.Start < 0 {
		sg.Start = 0
	}
	if math.IsNaN(sg.End) || math.IsInf(sg.End, 0) || sg.End < sg.Start {
		sg.End = sg.Start
	}
	sg.Text = cutBytes(strings.Join(strings.Fields(strings.ToValidUTF8(sg.Text, "�")), " "), MaxLiveText)
	return sg
}

// MaxLiveText is the bytes of one sentence of the live log: longer ones are cut at a character boundary.
const MaxLiveText = 4000

// MeetingLines is segs without Persona's echo: what the summary, the minutes and the follow-up read.
func MeetingLines(segs []kbwire.RecordingSegment) []kbwire.RecordingSegment {
	out := make([]kbwire.RecordingSegment, 0, len(segs))
	for _, sg := range segs {
		if !sg.Echo {
			out = append(out, sg)
		}
	}
	return out
}

// RecordChars is how many characters segs have without Persona's echo, which is not the meeting's text (meta.json's
// chars: in hub mode the transcript of record's). A meeting of only Persona's echo has none, so it gets no minutes.
func RecordChars(segs []kbwire.RecordingSegment) int {
	n := 0
	for _, sg := range MeetingLines(segs) {
		n += utf8.RuneCountInString(sg.Text)
	}
	return n
}

// MeetingParts are the parts of m that differ from before, in a fixed order.
func MeetingParts(before, after Meeting) []string {
	var out []string
	same := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	if !same(roomShown(before.Room), roomShown(after.Room)) {
		out = append(out, PartRoom)
	}
	if !same(before.Private, after.Private) {
		out = append(out, PartPrivate)
	}
	if !same(before.Followup, after.Followup) {
		out = append(out, PartFollowup)
	}
	if !same(transcriptionShown(before.Transcription), transcriptionShown(after.Transcription)) {
		out = append(out, PartTranscription)
	}
	if !same(before.Speakers, after.Speakers) {
		out = append(out, PartSpeakers)
	}
	return out
}

// roomShown is room without what only the manager reads (its batching and a lean room's bookkeeping, #8253), whose
// changes raise no revision: the apps see nothing new of it.
func roomShown(room *MeetingRoom) *MeetingRoom {
	if room == nil {
		return nil
	}
	r := *room
	r.Batching, r.Lean = 0, MeetingRoomLean{}
	return &r
}

// MeetingSame: a and b are the same meeting.json (also where no part the apps read differs: the timeline, the final
// pass's bookkeeping, a lean room's bookkeeping).
func MeetingSame(a, b Meeting) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// BumpRevs raises the revisions of parts in m.
func BumpRevs(m *Meeting, parts []string) {
	if len(parts) == 0 {
		return
	}
	if m.Revs == nil {
		m.Revs = map[string]int64{}
	}
	for _, p := range parts {
		m.Revs[p]++
	}
}

// transcriptionShown is what of t the apps read (TranscriptionView): PartTranscription moves when it changes, not
// when only the timeline grows or the bookkeeping does.
func transcriptionShown(t *MeetingTranscription) any {
	if t == nil {
		return nil
	}
	var gaps [][2]float64
	if len(t.Gaps) > 0 {
		gaps = t.Gaps
	}
	done, end, _ := t.FinalProgress()
	return struct {
		Mode, State, Problem string
		Gaps                 [][2]float64
		Fallback             bool
		FinalDone, End       float64
	}{t.Mode, t.State, t.Problem, gaps, t.Fallback, done, end}
}

// TranscriptOfRecord is a recording's text of record: in device mode the upload's segments; in hub mode the hub's
// sentences, and each upload segment the hub's sentences, taken together (their union), cover no more than half of,
// in time order (the hub's first at the same start), with Source set (SourceHub, SourceDevice). The phone's text is
// made on the device in the recording's language only, so in hub mode it fills only where the hub has none. Persona's
// echo stays in it, marked (Echo: the hub's by the echo rule, the phone's as uploaded); what is built from the meeting
// reads it through MeetingLines.
func TranscriptOfRecord(hubMode bool, device, hub []kbwire.RecordingSegment) []kbwire.RecordingSegment {
	if !hubMode {
		return slices.Clone(device)
	}
	out := make([]kbwire.RecordingSegment, 0, len(hub)+len(device))
	spans := make([][2]float64, 0, len(hub))
	for _, sg := range hub {
		sg.Source = SourceHub
		out = append(out, sg)
		spans = append(spans, [2]float64{sg.Start, sg.End})
	}
	union := UnionSpans(spans)
	for _, sg := range device {
		if CoveredByUnion(sg.Start, sg.End, union) <= 0.5 {
			sg.Source = SourceDevice
			out = append(out, sg)
		}
	}
	slices.SortStableFunc(out, func(a, b kbwire.RecordingSegment) int {
		switch {
		case a.Start < b.Start:
			return -1
		case a.Start > b.Start:
			return 1
		}
		return 0
	})
	return out
}
