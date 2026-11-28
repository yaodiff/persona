package kb

// The read path of recordings.go: Core, with or without the recording module (docs/plans/plan-kb-recording-port.md,
// section 1; docs/architecture/core-and-modules.md "Recording").

import (
	"cmp"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Recordings (docs/assistant/knowledge.md "Recordings", docs/protocol/protocol.md "Recording sync"): the phone uploads each recording's transcript,
// keyed by its UUID, the whole state every time (revision orders them), so replays, reordering and a transcript
// completed later all land on one entry. Only text: the audio stays on the phone, except a meeting transcribed on the
// hub, whose audio the hub keeps until the minutes from its final transcript are written (recording_audio.go). A
// directory per recording: meta.json, segments.json, transcript.md (what the searches read: the transcript of record,
// hub_segments.go), summary.md (the model's, once it is complete), and for an attended meeting live.json (live.go),
// for a meeting with photos photos.json, photos.md and attachments/ (recording_photos.go), for one transcribed on the
// hub hub_segments.json and attachments/audio/ (transcription.go). Uploads journal the accepted files until all
// replacements commit (recording_commit.go). Past recording_days a recording with minutes keeps only meta.json, its
// minutes and the photos they cite until minutes_days (recording_trim.go); any other goes.
// recordings/.deleted.json is the tombstones: a late upload of a deleted one is refused. Kept AudioDays.

// KindRecording is the recordings' kind and directory.
const KindRecording = recordingwire.KindRecording

// DefaultRecordingDays is how long a recording's audio is kept unless the user says otherwise
// (assistant.recording_audio_days, formerly recording_days): the phone deletes its copies at it.
const DefaultRecordingDays = 14

// DefaultRecordingTextDays is how long a recording's text (transcript, summary, live log) stays in the knowledge base
// unless the user says otherwise (assistant.recording_text_days): 0 means forever.
const DefaultRecordingTextDays = 0

// DefaultMinutesDays is how long a meeting's minutes are kept unless the user says otherwise (assistant.minutes_days);
// 0 means forever; a finite window is never shorter than the recording itself (MinutesDaysFor).
const DefaultMinutesDays = 0

// Defined in the contract package kbwire (docs/server/architecture.md "Contract packages"); the old names stay.
type RecordingSegment = kbwire.RecordingSegment

// Re-exported from recordingwire.
const (
	RecordingNote    = recordingwire.RecordingNote
	RecordingMeeting = recordingwire.RecordingMeeting
	RecordingDay     = recordingwire.RecordingDay
)

// Recording is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type Recording = recordingwire.Recording

var (
	// ErrRecordingDeleted: the recording was deleted (a tombstone); the phone must not upload it again.
	ErrRecordingDeleted = recordingwire.ErrRecordingDeleted
	// ErrBadRecording wraps what is wrong with an upload.
	ErrBadRecording = recordingwire.ErrBadRecording
)

// RecordingID is id as stored (lowercase), false when it is not a UUID.
func RecordingID(id string) (string, bool) { return recordingwire.RecordingID(id) }

func badRecording(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRecording, fmt.Sprintf(f, a...))
}

// RetentionCut is the start before which something kept days is past its retention at now; false when days is 0
// (or less): kept forever.
func RetentionCut(now time.Time, days int) (time.Time, bool) {
	if days <= 0 {
		return time.Time{}, false
	}
	return now.UTC().AddDate(0, 0, -days), true
}

// pastRetention: a recording that started at start is past a retention of days at now (never when days is 0).
func pastRetention(start, now time.Time, days int) bool {
	cut, ok := RetentionCut(now, days)
	return ok && start.Before(cut)
}

func (s *Store) Recording(id string) (*Recording, bool) {
	var r Recording
	raw, err := s.recordingRead(id, "meta.json")
	if err != nil || raw == nil || json.Unmarshal(raw, &r) != nil {
		return nil, false
	}
	if r.Kind == RecordingDay && r.Chars > 0 && r.TextRev == 0 {
		r.TextRev = 1 // a day recording's text stored without its count: what no recap or note read yet
	}
	r.Persisted = true
	s.recFilesMu.RLock()
	m, err := s.readMeeting(id)
	s.recFilesMu.RUnlock()
	if err != nil {
		// Unreadable: as a room meeting whose role is off (it feeds nothing, takes no question), and private.
		m = &Meeting{Room: &MeetingRoom{State: RoomNone}}
	}
	r.Conv = m
	return &r, true
}

// Recordings lists them all (the tombstones file is not one).
func (s *Store) Recordings() []*Recording {
	var out []*Recording
	for _, id := range s.IDs(KindRecording) {
		if strings.HasPrefix(id, ".") {
			continue
		}
		if r, ok := s.Recording(id); ok {
			out = append(out, r)
		}
	}
	return out
}

// RecordingSegments reads a recording's transcript of record (TranscriptOfRecord): its upload's segments, in hub mode
// with the hub's sentences.
func (s *Store) RecordingSegments(id string) []RecordingSegment {
	files, m, h, err := s.recordSnapshot(id)
	if err != nil {
		return nil
	}
	return recordOf(files, m, h)
}

// recordingName is what a recording is called in lists: the user's title, the phone calendar's, the matched calendar
// item's, the model's, else when it started (NameSource says which).
func (s *Store) recordingName(r *Recording) string {
	switch r.NameSource() {
	case NamedByRename:
		return r.Renamed
	case NamedByTitle:
		return r.Title
	case NamedByCalendar:
		return calendarTitle(r)
	case NamedByModel:
		return r.AITitle
	case "":
		if r.Kind == RecordingDay {
			return "全天录音 " + r.Day // Engine.recordingName has it in the owner's language
		}
	}
	return "录音 " + r.StartedAt.In(s.Zone()).Format("2006-01-02 15:04")
}

// Re-exported from recordingwire.
const (
	NamedByRename   = recordingwire.NamedByRename
	NamedByTitle    = recordingwire.NamedByTitle
	NamedByCalendar = recordingwire.NamedByCalendar
	NamedByModel    = recordingwire.NamedByModel
)

func nonNilSegments(s []RecordingSegment) []RecordingSegment {
	if s == nil {
		return []RecordingSegment{}
	}
	return s
}

// renderTranscript is transcript.md: a line per segment (and bookmark) at its clock time in the user's zone; Persona's
// echo (#5905) keeps its line, marked.
func (s *Store) renderTranscript(r *Recording, segs []RecordingSegment) string {
	zone := s.RecordingZone(r)
	at := func(sec float64) string {
		return r.StartedAt.Add(time.Duration(sec * float64(time.Second))).In(zone).Format("15:04:05")
	}
	type line struct {
		sec  float64
		text string
	}
	var lines []line
	for _, sg := range segs {
		text := strings.Join(strings.Fields(sg.Text), " ")
		if sg.Echo {
			text = "[Persona 回声] " + text // i18n-ignore: knowledge-base data, like the bookmarks' mark
		}
		lines = append(lines, line{sg.Start, "- " + at(sg.Start) + " " + text})
	}
	for _, b := range r.Bookmarks {
		lines = append(lines, line{b.At, "- " + at(b.At) + " [书签] " + oneLine(b.Label)})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].sec < lines[j].sec })
	var b strings.Builder
	b.WriteString("# 录音 · ")
	if t := cmp.Or(r.Title, r.AITitle); t != "" {
		b.WriteString(t + " · ")
	}
	fmt.Fprintf(&b, "%s · %s\n\n", r.StartedAt.In(zone).Format("2006-01-02 15:04"), zone)
	for _, l := range lines {
		b.WriteString(strings.TrimRight(l.text, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// RecordingDays is how many days a recording's text stays in the knowledge base (Engine.RecordingDaysFunc, the
// assistant's recording_text_days): 0 means forever; DefaultRecordingDays without a func.
func (e *Engine) RecordingDays() int {
	if e.RecordingDaysFunc != nil {
		return max(e.RecordingDaysFunc(), 0)
	}
	return DefaultRecordingDays
}

// AudioDays is how many days a recording's audio is kept (Engine.AudioDaysFunc, the assistant's
// recording_audio_days): 0 means forever. The phone deletes its copies at it (the days of GET /api/v1/recordings), and
// tombstones keep it: no phone uploads a recording older than that. Without a func it is RecordingDays when finite, the
// default otherwise.
func (e *Engine) AudioDays() int {
	if e.AudioDaysFunc != nil {
		return max(e.AudioDaysFunc(), 0)
	}
	if d := e.RecordingDays(); d > 0 {
		return d
	}
	return DefaultRecordingDays
}

// MinutesDays is how many days a meeting's minutes are kept (Engine.MinutesDaysFunc, the assistant's setting, the
// default without one): 0 means forever; otherwise never fewer than RecordingDays (forever when the text is).
func (e *Engine) MinutesDays() int {
	d := 0
	if e.MinutesDaysFunc != nil {
		d = e.MinutesDaysFunc()
	}
	return MinutesDaysFor(d, e.RecordingDays())
}

// MinutesDaysFor is the minutes' retention in effect for the setting minutes (<= 0: DefaultMinutesDays) and the
// recordings' text days (0: forever): 0 means forever; minutes never go before the text they are made of.
func MinutesDaysFor(minutes, text int) int {
	if minutes <= 0 || text <= 0 {
		return DefaultMinutesDays
	}
	return max(minutes, text)
}
