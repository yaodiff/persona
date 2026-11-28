package kbwire

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// SpeakerID is a stable hub-assigned speaker identifier ("s1", "s2", ...; docs/assistant/meeting-minutes.md
// "Speakers (#17674)").
type SpeakerID string

// Valid reports whether id is a valid hub speaker id ("s" followed by a positive decimal integer without leading zeros).
func (id SpeakerID) Valid() bool {
	s := string(id)
	if len(s) < 2 || s[0] != 's' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s[1:])
	return err == nil && n > 0
}

// SpeakerState is the lifecycle state of a meeting's speaker separation block in meeting.json and detail.speakers.
type SpeakerState string

const (
	SpeakerStateWaiting SpeakerState = "waiting"
	SpeakerStateReady   SpeakerState = "ready"
	SpeakerStateFailed  SpeakerState = "failed"
	SpeakerStateCleared SpeakerState = "cleared"
)

// Valid reports whether s is a known speaker state.
func (s SpeakerState) Valid() bool {
	switch s {
	case SpeakerStateWaiting, SpeakerStateReady, SpeakerStateFailed, SpeakerStateCleared:
		return true
	}
	return false
}

// SpeakerFailReason says why speaker separation failed (only set when SpeakerState is SpeakerStateFailed).
type SpeakerFailReason string

const (
	SpeakerFailEngineUnavailable SpeakerFailReason = "engine_unavailable"
	SpeakerFailNoAudio           SpeakerFailReason = "no_audio"
	SpeakerFailTooLong           SpeakerFailReason = "too_long"
	SpeakerFailTimeout           SpeakerFailReason = "timeout"
	SpeakerFailGaveUp            SpeakerFailReason = "gave_up"
	SpeakerFailAudioGone         SpeakerFailReason = "audio_gone"
	SpeakerFailError             SpeakerFailReason = "error"
)

// Valid reports whether r is a known failure reason.
func (r SpeakerFailReason) Valid() bool {
	switch r {
	case SpeakerFailEngineUnavailable, SpeakerFailNoAudio, SpeakerFailTooLong, SpeakerFailTimeout,
		SpeakerFailGaveUp, SpeakerFailAudioGone, SpeakerFailError:
		return true
	}
	return false
}

// SpeakerQuality is the hub's verdict on a meeting's speaker separation reliability.
type SpeakerQuality string

const (
	SpeakerQualityOk  SpeakerQuality = "ok"
	SpeakerQualityLow SpeakerQuality = "low"
)

// Valid reports whether q is a known speaker quality verdict.
func (q SpeakerQuality) Valid() bool {
	return q == SpeakerQualityOk || q == SpeakerQualityLow
}

// SpeakerQualityReason says which rule marked a meeting's speaker separation quality as low.
type SpeakerQualityReason string

const (
	SpeakerQualityTooManyUnlabelled     SpeakerQualityReason = "too_many_unlabelled"
	SpeakerQualitySingleSpeakerDominant SpeakerQualityReason = "single_speaker_dominant"
	SpeakerQualityTooLittleSpeech       SpeakerQualityReason = "too_little_speech"
	SpeakerQualityLowCoverage           SpeakerQualityReason = "low_coverage"
	SpeakerQualityTooManyAttendees      SpeakerQualityReason = "too_many_attendees"
)

// Valid reports whether r is a known low-quality reason.
func (r SpeakerQualityReason) Valid() bool {
	switch r {
	case SpeakerQualityTooManyUnlabelled, SpeakerQualitySingleSpeakerDominant, SpeakerQualityTooLittleSpeech,
		SpeakerQualityLowCoverage, SpeakerQualityTooManyAttendees:
		return true
	}
	return false
}

// SpeakerSource names where a speaker timeline came from.
type SpeakerSource string

const (
	SpeakerSourceHub SpeakerSource = "hub"
)

// Valid reports whether s is a known speaker timeline source.
func (s SpeakerSource) Valid() bool {
	return s == SpeakerSourceHub
}

// SpeakerSpan is a recording-seconds interval [start, end].
type SpeakerSpan [2]float64

// SpeakerTurn is one hub-numbered speaking interval serialized as a 3-element JSON array [start, end, id].
type SpeakerTurn struct {
	Start   float64
	End     float64
	Speaker SpeakerID
}

// MarshalJSON encodes t as [start, end, speaker].
func (t SpeakerTurn) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{t.Start, t.End, t.Speaker})
}

// UnmarshalJSON decodes [start, end, speaker] into t.
func (t *SpeakerTurn) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 3 {
		return fmt.Errorf("speaker turn must have 3 elements, got %d", len(raw))
	}
	var start, end float64
	var id SpeakerID
	if err := json.Unmarshal(raw[0], &start); err != nil {
		return fmt.Errorf("speaker turn start: %w", err)
	}
	if err := json.Unmarshal(raw[1], &end); err != nil {
		return fmt.Errorf("speaker turn end: %w", err)
	}
	if err := json.Unmarshal(raw[2], &id); err != nil {
		return fmt.Errorf("speaker turn id: %w", err)
	}
	if math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) {
		return fmt.Errorf("speaker turn times must be finite")
	}
	*t = SpeakerTurn{Start: start, End: end, Speaker: id}
	return nil
}

// DetailSpeakerPerson is one speaker entry in detail.speakers.people.
type DetailSpeakerPerson struct {
	ID         SpeakerID `json:"id"`
	Name       string    `json:"name,omitempty"`
	Me         bool      `json:"me,omitempty"`
	MergedInto SpeakerID `json:"merged_into,omitempty"`
	Minor      bool      `json:"minor,omitempty"`
	Seconds    float64   `json:"seconds,omitempty"`
	Lines      int       `json:"lines,omitempty"`
	Samples    []float64 `json:"samples,omitempty"`
}

// DetailSpeakers is detail.speakers on GET /api/v1/kb/recordings/{id} and speaker write responses
// (docs/assistant/meeting-minutes.md "Speakers (#17674)").
type DetailSpeakers struct {
	Rev        int64                 `json:"rev"`
	State      SpeakerState          `json:"state"`
	Reason     SpeakerFailReason     `json:"reason"`
	Quality    SpeakerQuality        `json:"quality"`
	Source     SpeakerSource         `json:"source"`
	Done       *float64              `json:"done,omitempty"`
	End        *float64              `json:"end,omitempty"`
	People     []DetailSpeakerPerson `json:"people"`
	Candidates []string              `json:"candidates"`
	Spans      []SpeakerTurn         `json:"spans"`
	Covered    []SpeakerSpan         `json:"covered"`
	Trimmed    bool                  `json:"trimmed"`
}
