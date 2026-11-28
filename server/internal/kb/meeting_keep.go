package kb

// The writes of meeting.go that delete, retention and recovery need: Core, with or without the recording module (docs/plans/plan-kb-recording-port.md,
// section 1; docs/architecture/core-and-modules.md "Recording").

import (
	"encoding/json"
	"os"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Re-exported from recordingwire.
const (
	PartRoom       = recordingwire.PartRoom
	PartAsks       = recordingwire.PartAsks
	PartPrivate    = recordingwire.PartPrivate
	PartFollowup   = recordingwire.PartFollowup
	PartPhotos     = recordingwire.PartPhotos
	PartLines      = recordingwire.PartLines
	PartTopics     = recordingwire.PartTopics
	PartHighlights = recordingwire.PartHighlights
	PartSpeakers   = recordingwire.PartSpeakers
)

// MeetingRoomLean is declared in recordingwire (docs/plans/plan-kb-recording-port.md).
type MeetingRoomLean = recordingwire.MeetingRoomLean

// putMeeting writes recording id's meeting.json, not when the recording was deleted meanwhile (false then); a trimmed
// recording keeps it (its private mark and its room's end). The caller holds meetMu.
func (s *Store) putMeeting(id string, m *Meeting) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, err
	}
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	if _, gone := s.tombstones()[id]; gone {
		return false, nil
	}
	if _, err := os.Stat(s.path(KindRecording, id)); err != nil {
		return false, nil
	}
	return true, writeRecordingFile(s.path(KindRecording, id, meetingFile), append(raw, '\n'))
}

// The rules of meeting.json's parts and revisions, which the upload (the recording module's) applies too
// (recordingwire/rules.go).
func meetingParts(before, after Meeting) []string { return recordingwire.MeetingParts(before, after) }
func meetingSame(a, b Meeting) bool               { return recordingwire.MeetingSame(a, b) }
func bumpRevs(m *Meeting, parts []string)         { recordingwire.BumpRevs(m, parts) }

// meetingChanged tells the engine's MeetingChanged of recording id's changed parts.
func (e *Engine) meetingChanged(id string, parts []string) {
	if len(parts) > 0 && e.MeetingChanged != nil {
		e.MeetingChanged(id, parts)
	}
}

func (e *Engine) meetingUpdateLocked(id string, fn func(m *Meeting, r Recording) error) (Meeting, []string, error) {
	st := e.Store
	if _, gone := st.tombstones()[id]; gone {
		return Meeting{}, nil, ErrRecordingDeleted
	}
	r, ok := st.Recording(id)
	if !ok {
		return Meeting{}, nil, ErrNotFound
	}
	if st.ReadOnly() {
		return Meeting{}, nil, ErrReadOnly
	}
	var before Meeting
	if r.Conv != nil {
		before = r.Conv.Clone()
	}
	m := before.Clone()
	if err := fn(&m, *r); err != nil {
		return Meeting{}, nil, err
	}
	if meetingSame(before, m) {
		return m, nil, nil
	}
	parts := meetingParts(before, m)
	bumpRevs(&m, parts)
	if ok, err := st.putMeeting(id, &m); err != nil {
		return Meeting{}, nil, err
	} else if !ok {
		return Meeting{}, nil, ErrNotFound
	}
	return m, parts, nil
}
