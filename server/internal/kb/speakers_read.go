package kb

// The read path of meeting speakers (#17674, docs/assistant/meeting-minutes.md "Speakers (#17674)"): Core, with or
// without the recording module.

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingspeakers/recordingspeakersadapt"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// SpeakerTurnsFile is declared in recordingwire.
type SpeakerTurnsFile = recordingwire.SpeakerTurnsFile

const (
	speakerTurnsFile = string(recordingwire.FileSpeakerTurns)

	// maxSpeakerCandidateMeetings bounds how many recent meetings are scanned when assembling candidate speaker names.
	maxSpeakerCandidateMeetings = 20
	// maxSpeakerCandidates bounds how many candidate names detail.speakers.candidates returns.
	maxSpeakerCandidates = 12
	// maxSpeakerCandidateNameRunes matches recordingspeakers' speaker name limit so candidates are always valid names.
	maxSpeakerCandidateNameRunes = 100
)

// readSpeakerTurnsLocked reads recording id's speaker_turns.json; nil, nil when absent. The caller holds recFilesMu.
func (s *Store) readSpeakerTurnsLocked(id string) (*recordingwire.SpeakerTurnsFile, error) {
	raw, err := os.ReadFile(s.path(KindRecording, id, speakerTurnsFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tf recordingwire.SpeakerTurnsFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return nil, fmt.Errorf("speaker turns of %s: %w", id, err)
	}
	return &tf, nil
}

// recordSnapshotWithSpeakers reads a recording's committed files together with meeting.json, hub_segments.json and
// speaker_turns.json under one recFilesMu read lock.
func (s *Store) recordSnapshotWithSpeakers(id string) (map[string][]byte, *Meeting, HubTranscript, *recordingwire.SpeakerTurnsFile, error) {
	if s.recordingReadWait != nil {
		s.recordingReadWait()
	}
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	undo, err := s.readRecordingUndo(id)
	if err != nil {
		return nil, nil, HubTranscript{}, nil, err
	}
	var files map[string][]byte
	if undo != nil {
		files = undo.Files
	} else if files, err = s.recordingDiskSnapshot(id); err != nil {
		return nil, nil, HubTranscript{}, nil, err
	}
	m, err := s.readMeeting(id)
	if err != nil {
		m = nil
	}
	h, err := s.readHubLocked(id)
	if err != nil {
		h = HubTranscript{Segments: []RecordingSegment{}}
	}
	turns, err := s.readSpeakerTurnsLocked(id)
	if err != nil {
		turns = nil
	}
	return files, m, h, turns, nil
}

// SpeakerDetail returns detail.speakers for recording rawID (nil when the recording has no speaker block);
// ErrRecordingDeleted after a delete, ErrNotFound when absent. Served by Core, with or without the recording module.
func (e *Engine) SpeakerDetail(rawID string) (*kbwire.DetailSpeakers, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil, ErrNotFound
	}
	if _, gone := e.Store.tombstones()[id]; gone {
		return nil, ErrRecordingDeleted
	}
	files, meeting, hub, turns, err := e.Store.recordSnapshotWithSpeakers(id)
	if err != nil {
		return nil, err
	}
	rawMeta := files["meta.json"]
	if rawMeta == nil {
		return nil, ErrNotFound
	}
	if meeting == nil || meeting.Speakers == nil {
		return nil, nil
	}
	var r Recording
	if err := json.Unmarshal(rawMeta, &r); err != nil {
		return nil, err
	}
	if r.ID == "" {
		r.ID = id
	}
	segs := nonNilSegments(recordOf(files, meeting, hub))
	minutesRaw, _ := os.ReadFile(e.Store.path(KindRecording, id, "minutes.json"))
	speakers, _ := e.speakerDetail(&r, meeting, hub, turns, segs, minutesRaw)
	return speakers, nil
}

// speakerDetail computes detail.speakers and aligned detail.segments when meeting has a speakers block. When meeting
// has no speakers block, it returns nil and segs unchanged.
func (e *Engine) speakerDetail(r *Recording, meeting *Meeting, hub HubTranscript, turns *recordingwire.SpeakerTurnsFile, segs []RecordingSegment, minutesRaw []byte) (*kbwire.DetailSpeakers, []RecordingSegment) {
	if meeting == nil || meeting.Speakers == nil {
		return nil, segs
	}
	rev := int64(0)
	if meeting.Revs != nil {
		rev = meeting.Revs[recordingwire.PartSpeakers]
	}
	endSec := recordingDurationSeconds(r, segs)
	out, err := recordingspeakersadapt.ProjectDetail(recordingwire.SpeakerDetailInput{
		Rev:        rev,
		Speakers:   *meeting.Speakers,
		Turns:      turns,
		Segments:   segs,
		Timing:     hub.EffectiveTiming(),
		EndSeconds: endSec,
		Trimmed:    r != nil && r.Trimmed(),
		Candidates: e.speakerCandidates(r, minutesRaw),
	})
	if err != nil {
		e.logf("recording %s: speakers detail: %v", r.ID, err)
		return nil, segs
	}
	return out.Speakers, out.Segments
}

// recordingDurationSeconds returns the recording's total duration in seconds for progress display and quality checks.
func recordingDurationSeconds(r *Recording, segs []RecordingSegment) float64 {
	if r != nil {
		if r.Duration > 0 {
			return r.Duration
		}
		if r.EndedAt != nil && !r.StartedAt.IsZero() && r.EndedAt.After(r.StartedAt) {
			return r.EndedAt.Sub(r.StartedAt).Seconds()
		}
	}
	maxEnd := 0.0
	for _, sg := range segs {
		if sg.End > maxEnd {
			maxEnd = sg.End
		}
	}
	return maxEnd
}

// speakerCandidates computes candidate names on read (never matched by voice): owner name, calendar attendees, names
// heard in the meeting's minutes (attendees / who), and names used in recent meetings with overlapping calendar
// attendees or the same title. Scans at most maxSpeakerCandidateMeetings recent meetings and returns at most
// maxSpeakerCandidates deduplicated names.
func (e *Engine) speakerCandidates(r *Recording, minutesRaw []byte) []string {
	out := make([]string, 0, maxSpeakerCandidates)
	seen := map[string]bool{}
	add := func(raw string) {
		if len(out) >= maxSpeakerCandidates {
			return
		}
		if strings.ContainsAny(raw, "\n\r") {
			return
		}
		name := strings.TrimSpace(raw)
		if name == "" || utf8.RuneCountInString(name) > maxSpeakerCandidateNameRunes {
			return
		}
		key := fold(name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, name)
	}

	// 1. Owner name ("me" is a separate choice in UI).
	add(e.ownerName())

	// 2. Calendar attendees of this meeting.
	curAttendees := map[string]bool{}
	curTitles := map[string]bool{}
	if r != nil {
		if r.Calendar != nil {
			for _, a := range r.Calendar.Attendees {
				add(a)
				if k := fold(strings.TrimSpace(a)); k != "" {
					curAttendees[k] = true
				}
			}
			if t := fold(strings.TrimSpace(r.Calendar.Title)); t != "" {
				curTitles[t] = true
			}
		}
		if t := fold(strings.TrimSpace(r.Title)); t != "" {
			curTitles[t] = true
		}
		if t := fold(strings.TrimSpace(r.Renamed)); t != "" {
			curTitles[t] = true
		}
	}

	// 3. Names heard in the minutes (attendees and who fields).
	if len(minutesRaw) > 0 && len(out) < maxSpeakerCandidates {
		var m Minutes
		if json.Unmarshal(minutesRaw, &m) == nil {
			for _, a := range m.Attendees {
				add(a)
			}
			for _, p := range m.Points {
				add(p.Who)
			}
			for _, a := range m.Actions {
				add(a.Who)
			}
			for _, d := range m.Debates {
				for _, pos := range d.Positions {
					add(pos.Who)
				}
			}
		}
	}

	// 4. Names used in earlier meetings with overlapping calendar attendees or the same title.
	if r != nil && len(out) < maxSpeakerCandidates && (len(curAttendees) > 0 || len(curTitles) > 0) {
		all := e.Store.Recordings()
		slices.SortFunc(all, func(a, b *Recording) int {
			if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
				return c
			}
			return strings.Compare(b.ID, a.ID)
		})
		scanned := 0
		for _, other := range all {
			if other.ID == r.ID || !other.IsMeeting() {
				continue
			}
			scanned++
			if scanned > maxSpeakerCandidateMeetings {
				break
			}
			if other.Conv == nil || other.Conv.Speakers == nil || other.Conv.Speakers.State == recordingwire.SpeakerStateCleared {
				continue
			}
			if !meetingsRelatedForCandidates(curAttendees, curTitles, other) {
				continue
			}
			for _, p := range other.Conv.Speakers.People {
				if p.MergedInto == "" && p.Name != "" {
					add(p.Name)
				}
			}
			if len(out) >= maxSpeakerCandidates {
				break
			}
		}
	}

	return out
}

func meetingsRelatedForCandidates(curAttendees, curTitles map[string]bool, other *Recording) bool {
	if other == nil {
		return false
	}
	if other.Calendar != nil {
		for _, a := range other.Calendar.Attendees {
			if curAttendees[fold(strings.TrimSpace(a))] {
				return true
			}
		}
		if t := fold(strings.TrimSpace(other.Calendar.Title)); t != "" && curTitles[t] {
			return true
		}
	}
	if t := fold(strings.TrimSpace(other.Title)); t != "" && curTitles[t] {
		return true
	}
	if t := fold(strings.TrimSpace(other.Renamed)); t != "" && curTitles[t] {
		return true
	}
	return false
}
