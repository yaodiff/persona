package recordingwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// Closed speaker wire types shared with kbwire.Detail and kbwire.RecordingSegment
// (docs/assistant/meeting-minutes.md "Speakers (#17674)").
type (
	SpeakerID            = kbwire.SpeakerID
	SpeakerState         = kbwire.SpeakerState
	SpeakerFailReason    = kbwire.SpeakerFailReason
	SpeakerQuality       = kbwire.SpeakerQuality
	SpeakerQualityReason = kbwire.SpeakerQualityReason
	SpeakerSource        = kbwire.SpeakerSource
	SpeakerSpan          = kbwire.SpeakerSpan
	SpeakerTurn          = kbwire.SpeakerTurn
	DetailSpeakerPerson  = kbwire.DetailSpeakerPerson
	DetailSpeakers       = kbwire.DetailSpeakers
)

const (
	SpeakerStateWaiting = kbwire.SpeakerStateWaiting
	SpeakerStateReady   = kbwire.SpeakerStateReady
	SpeakerStateFailed  = kbwire.SpeakerStateFailed
	SpeakerStateCleared = kbwire.SpeakerStateCleared

	SpeakerFailEngineUnavailable = kbwire.SpeakerFailEngineUnavailable
	SpeakerFailNoAudio           = kbwire.SpeakerFailNoAudio
	SpeakerFailTooLong           = kbwire.SpeakerFailTooLong
	SpeakerFailTimeout           = kbwire.SpeakerFailTimeout
	SpeakerFailGaveUp            = kbwire.SpeakerFailGaveUp
	SpeakerFailAudioGone         = kbwire.SpeakerFailAudioGone
	SpeakerFailError             = kbwire.SpeakerFailError

	SpeakerQualityOk  = kbwire.SpeakerQualityOk
	SpeakerQualityLow = kbwire.SpeakerQualityLow

	SpeakerQualityTooManyUnlabelled     = kbwire.SpeakerQualityTooManyUnlabelled
	SpeakerQualitySingleSpeakerDominant = kbwire.SpeakerQualitySingleSpeakerDominant
	SpeakerQualityTooLittleSpeech       = kbwire.SpeakerQualityTooLittleSpeech
	SpeakerQualityLowCoverage           = kbwire.SpeakerQualityLowCoverage
	SpeakerQualityTooManyAttendees      = kbwire.SpeakerQualityTooManyAttendees

	SpeakerSourceHub = kbwire.SpeakerSourceHub
)

// TranscriptTiming describes how reliable a hub transcript's segment timestamps are
// (hub_segments.json "timing"; absent reads as TimingSpread).
type TranscriptTiming string

const (
	TimingExact  TranscriptTiming = "exact"
	TimingTurn   TranscriptTiming = "turn"
	TimingSpread TranscriptTiming = "spread"
)

// Valid reports whether t is a known transcript timing value.
func (t TranscriptTiming) Valid() bool {
	return t == TimingExact || t == TimingTurn || t == TimingSpread
}

// Effective returns t when valid, or TimingSpread when absent or unrecognized.
func (t TranscriptTiming) Effective() TranscriptTiming {
	if t.Valid() {
		return t
	}
	return TimingSpread
}

// SpeakerRunID identifies one speaker separation attempt or debug import run.
type SpeakerRunID string

// SpeakerClusterID is a raw engine-assigned speaker cluster label before hub numbering (e.g. "c1").
type SpeakerClusterID string

// SpeakerEditOpKind is one of the four speaker edit operations.
type SpeakerEditOpKind string

const (
	SpeakerOpName    SpeakerEditOpKind = "name"
	SpeakerOpMerge   SpeakerEditOpKind = "merge"
	SpeakerOpUnmerge SpeakerEditOpKind = "unmerge"
	SpeakerOpClear   SpeakerEditOpKind = "clear"
)

// Valid reports whether k is a known speaker edit operation kind.
func (k SpeakerEditOpKind) Valid() bool {
	switch k {
	case SpeakerOpName, SpeakerOpMerge, SpeakerOpUnmerge, SpeakerOpClear:
		return true
	}
	return false
}

// Speaker route limits.
const (
	MaxSpeakerTurns     = 20000
	MaxSpeakerTurnsBody = 2 << 20
	MaxSpeakerEditBody  = 64 << 10
)

// Speaker errors returned by writers and mapped to HTTP status codes by the recording module.
var (
	ErrSpeakersStale    = errors.New("speakers changed; reload and try again")
	ErrSpeakersCleared  = errors.New("speakers were cleared for this meeting")
	ErrBadSpeakers      = errors.New("invalid speaker request")
	ErrSpeakersTooLarge = errors.New("too many speaker turns")
)

// SpeakerStat is one original speaker's speaking seconds and attributed line count, serialized as [seconds, lines].
type SpeakerStat struct {
	Seconds float64
	Lines   int
}

// MarshalJSON encodes s as [seconds, lines].
func (s SpeakerStat) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]any{s.Seconds, s.Lines})
}

// UnmarshalJSON decodes [seconds, lines] into s.
func (s *SpeakerStat) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 2 {
		return fmt.Errorf("speaker stat must have 2 elements, got %d", len(raw))
	}
	var sec float64
	var lines int
	if err := json.Unmarshal(raw[0], &sec); err != nil {
		return fmt.Errorf("speaker stat seconds: %w", err)
	}
	if err := json.Unmarshal(raw[1], &lines); err != nil {
		return fmt.Errorf("speaker stat lines: %w", err)
	}
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 || lines < 0 {
		return fmt.Errorf("invalid speaker stat values")
	}
	*s = SpeakerStat{Seconds: sec, Lines: lines}
	return nil
}

// SpeakerPerson is one speaker entry stored in meeting.json's speakers.people.
type SpeakerPerson struct {
	ID         SpeakerID `json:"id"`
	Name       string    `json:"name,omitempty"`
	Me         bool      `json:"me,omitempty"`
	MergedInto SpeakerID `json:"merged_into,omitempty"`
	Minor      bool      `json:"minor,omitempty"`
}

// SpeakerEdits counts how many speaker edits of each kind have been applied to a meeting.
type SpeakerEdits struct {
	Name    int `json:"name,omitempty"`
	Merge   int `json:"merge,omitempty"`
	Unmerge int `json:"unmerge,omitempty"`
	Clear   int `json:"clear,omitempty"`
}

// MeetingSpeakers is meeting.json's speakers block (docs/assistant/meeting-minutes.md "Speakers (#17674)").
type MeetingSpeakers struct {
	State      SpeakerState              `json:"state"`
	Since      time.Time                 `json:"since,omitzero"`
	Tries      int                       `json:"tries,omitempty"`
	Done       float64                   `json:"done,omitempty"`
	Reason     SpeakerFailReason         `json:"reason,omitempty"`
	Quality    SpeakerQuality            `json:"quality,omitempty"`
	QualityWhy SpeakerQualityReason      `json:"quality_why,omitempty"`
	Next       int                       `json:"next,omitempty"`
	People     []SpeakerPerson           `json:"people,omitempty"`
	Stats      map[SpeakerID]SpeakerStat `json:"stats,omitempty"`
	Edits      SpeakerEdits              `json:"edits,omitzero"`
}

// Clone returns a deep copy of s (nil stays nil).
func (s *MeetingSpeakers) Clone() *MeetingSpeakers {
	if s == nil {
		return nil
	}
	cp := *s
	cp.People = slices.Clone(s.People)
	cp.Stats = maps.Clone(s.Stats)
	return &cp
}

// RawSpeakerTurn is an incoming engine-labelled turn serialized as [start, end, cluster].
type RawSpeakerTurn struct {
	Start   float64
	End     float64
	Cluster SpeakerClusterID
}

// MarshalJSON encodes t as [start, end, cluster].
func (t RawSpeakerTurn) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{t.Start, t.End, t.Cluster})
}

// UnmarshalJSON decodes [start, end, cluster] into t.
func (t *RawSpeakerTurn) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 3 {
		return fmt.Errorf("raw speaker turn must have 3 elements, got %d", len(raw))
	}
	var start, end float64
	var cluster SpeakerClusterID
	if err := json.Unmarshal(raw[0], &start); err != nil {
		return fmt.Errorf("raw speaker turn start: %w", err)
	}
	if err := json.Unmarshal(raw[1], &end); err != nil {
		return fmt.Errorf("raw speaker turn end: %w", err)
	}
	if err := json.Unmarshal(raw[2], &cluster); err != nil {
		return fmt.Errorf("raw speaker turn cluster: %w", err)
	}
	if math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) {
		return fmt.Errorf("raw speaker turn times must be finite")
	}
	*t = RawSpeakerTurn{Start: start, End: end, Cluster: cluster}
	return nil
}

// SpeakerSignal is one value in speaker_turns.json's signals map: a finite number or a single-line enum string.
type SpeakerSignal struct {
	Number float64
	Enum   string
	IsEnum bool
}

// MarshalJSON encodes s as a JSON number or string.
func (s SpeakerSignal) MarshalJSON() ([]byte, error) {
	if s.IsEnum {
		return json.Marshal(s.Enum)
	}
	return json.Marshal(s.Number)
}

// UnmarshalJSON accepts only a finite JSON number or a non-empty single-line JSON string.
func (s *SpeakerSignal) UnmarshalJSON(data []byte) error {
	var num float64
	if err := json.Unmarshal(data, &num); err == nil {
		if math.IsNaN(num) || math.IsInf(num, 0) {
			return fmt.Errorf("signal number must be finite")
		}
		*s = SpeakerSignal{Number: num}
		return nil
	}
	var word string
	if err := json.Unmarshal(data, &word); err == nil {
		trimmed := strings.TrimSpace(word)
		if trimmed == "" || strings.ContainsAny(word, "\n\r") {
			return fmt.Errorf("signal enum must be a non-empty single line")
		}
		*s = SpeakerSignal{Enum: trimmed, IsEnum: true}
		return nil
	}
	return fmt.Errorf("signal value must be a number or enum string")
}

// SpeakerTurnsFile is speaker_turns.json stored beside meta.json (part of the transcript: expires with it and
// excluded from search).
type SpeakerTurnsFile struct {
	Run       SpeakerRunID             `json:"run"`
	Source    SpeakerSource            `json:"source"`
	Engine    string                   `json:"engine"`
	Covered   []SpeakerSpan            `json:"covered"`
	Turns     []SpeakerTurn            `json:"turns"`
	Signals   map[string]SpeakerSignal `json:"signals,omitempty"`
	RunDigest string                   `json:"run_digest,omitempty"`
}

// Clone returns a deep copy of f.
func (f SpeakerTurnsFile) Clone() SpeakerTurnsFile {
	cp := f
	cp.Covered = slices.Clone(f.Covered)
	cp.Turns = slices.Clone(f.Turns)
	cp.Signals = maps.Clone(f.Signals)
	return cp
}

// SpeakerEditOp is one edit operation in POST /api/v1/recordings/{id}/speakers and its undo response.
type SpeakerEditOp struct {
	Op   SpeakerEditOpKind `json:"op"`
	ID   SpeakerID         `json:"id,omitempty"`
	Name string            `json:"name,omitempty"`
	Me   bool              `json:"me,omitempty"`
	From SpeakerID         `json:"from,omitempty"`
	Into SpeakerID         `json:"into,omitempty"`
}

// SpeakerEditRequest is the request body of POST /api/v1/recordings/{id}/speakers.
type SpeakerEditRequest struct {
	Rev int64           `json:"rev"`
	Ops []SpeakerEditOp `json:"ops"`
}

// SpeakerEditResponse is the 200 response body of POST /api/v1/recordings/{id}/speakers.
type SpeakerEditResponse struct {
	Speakers *DetailSpeakers `json:"speakers"`
	Undo     []SpeakerEditOp `json:"undo"`
}

// SpeakerTurnsImportRequest is the request body of PUT /api/v1/recordings/{id}/speakers/turns.
type SpeakerTurnsImportRequest struct {
	Run     SpeakerRunID             `json:"run"`
	Engine  string                   `json:"engine"`
	Covered []SpeakerSpan            `json:"covered"`
	Turns   []RawSpeakerTurn         `json:"turns"`
	Source  SpeakerSource            `json:"source"`
	Signals map[string]SpeakerSignal `json:"signals,omitempty"`
}

// SpeakerTurnsImportResponse is the 200 response body of PUT /api/v1/recordings/{id}/speakers/turns.
type SpeakerTurnsImportResponse struct {
	Speakers *DetailSpeakers `json:"speakers"`
}

// Contract inputs and outputs for the recordingspeakers domain adapter (recordingspeakersadapt).

// SpeakerImportInput bundles the wire data needed to number or pair an imported timeline and judge its quality.
type SpeakerImportInput struct {
	PrevTurns    []SpeakerTurn
	PrevSpeakers *MeetingSpeakers
	RawTurns     []RawSpeakerTurn
	Covered      []SpeakerSpan
	Duration     float64
	Segments     []kbwire.RecordingSegment
	Timing       TranscriptTiming
	Attendees    int
}

// SpeakerImportOutput is the numbered timeline, updated roster fields, stats and quality verdict from ImportTimeline.
type SpeakerImportOutput struct {
	Turns      []SpeakerTurn
	Covered    []SpeakerSpan
	Next       int
	People     []SpeakerPerson
	Edits      SpeakerEdits
	Stats      map[SpeakerID]SpeakerStat
	Quality    SpeakerQuality
	QualityWhy SpeakerQualityReason
}

// SpeakerRecomputeInput bundles the wire data needed to trim (when TrimTo > 0) and recompute stats, minor marks and
// quality after the transcript of record or recording duration changes.
type SpeakerRecomputeInput struct {
	Turns     []SpeakerTurn
	Covered   []SpeakerSpan
	Speakers  MeetingSpeakers
	TrimTo    float64
	Duration  float64
	Segments  []kbwire.RecordingSegment
	Timing    TranscriptTiming
	Attendees int
}

// SpeakerRecomputeOutput is the updated timeline, minor marks, stats and quality verdict from RecomputeAfterTranscriptChange.
type SpeakerRecomputeOutput struct {
	Turns      []SpeakerTurn
	Covered    []SpeakerSpan
	People     []SpeakerPerson
	Stats      map[SpeakerID]SpeakerStat
	Quality    SpeakerQuality
	QualityWhy SpeakerQualityReason
}

// SpeakerEditInput bundles the stored speakers block and the edit ops to apply atomically.
type SpeakerEditInput struct {
	Speakers MeetingSpeakers
	Ops      []SpeakerEditOp
}

// SpeakerEditOutput is the updated roster fields and server-computed undo operations from ApplyEdit.
type SpeakerEditOutput struct {
	Next    int
	People  []SpeakerPerson
	Edits   SpeakerEdits
	Undo    []SpeakerEditOp
	Cleared bool
}

// SpeakerDetailInput bundles everything Core needs to project detail.speakers and label detail.segments on read.
type SpeakerDetailInput struct {
	Rev        int64
	Speakers   MeetingSpeakers
	Turns      *SpeakerTurnsFile
	Segments   []kbwire.RecordingSegment
	Timing     TranscriptTiming
	EndSeconds float64
	Trimmed    bool
	Candidates []string
}

// SpeakerDetailOutput holds the projected detail.speakers and aligned detail.segments.
type SpeakerDetailOutput struct {
	Speakers *DetailSpeakers
	Segments []kbwire.RecordingSegment
}

// SpeakerAlignInput bundles the stored speakers roster, timeline, timing reliability and transcript segments to align
// before rendering numbered transcript lines for the minutes model.
type SpeakerAlignInput struct {
	Speakers MeetingSpeakers
	Turns    []SpeakerTurn
	Covered  []SpeakerSpan
	Timing   TranscriptTiming
	Segments []kbwire.RecordingSegment
}

// AlignedSpeakerLine is the aligned merged speaker id and certainty for one transcript segment (Speaker empty when unlabelled).
type AlignedSpeakerLine struct {
	Speaker SpeakerID
	Unsure  bool
}

// SpeakerAlignOutput holds one AlignedSpeakerLine per input segment in order.
type SpeakerAlignOutput struct {
	Lines []AlignedSpeakerLine
}

// SpeakerDebatesAttrInput bundles the current debates, speaker roster, and (when untrimmed) timeline + timing to
// compute layer-1 overlaps, layer-2 attributions and cross-checks on every disagreement position and its also entries.
type SpeakerDebatesAttrInput struct {
	Debates  []MinutesDebate
	Speakers MeetingSpeakers
	Turns    []SpeakerTurn
	Covered  []SpeakerSpan
	Timing   TranscriptTiming
	Trimmed  bool
}

// SpeakerDebatesAttrOutput holds the attributed disagreements returned by AttributeDebateSpeakers.
type SpeakerDebatesAttrOutput struct {
	Debates []MinutesDebate
}

// SpeakerDisplayKind is one of the display-name outcomes for a disagreement position (docs/assistant/meeting-minutes.md
// "Aligning lines to speakers" -> "Display names").
type SpeakerDisplayKind string

const (
	SpeakerDisplayName     SpeakerDisplayKind = "name"
	SpeakerDisplayNumbered SpeakerDisplayKind = "numbered"
	SpeakerDisplayWho      SpeakerDisplayKind = "who"
)

// Valid reports whether k is a known speaker display label kind.
func (k SpeakerDisplayKind) Valid() bool {
	return k == SpeakerDisplayName || k == SpeakerDisplayNumbered || k == SpeakerDisplayWho
}

// SpeakerDisplayLabel is one resolved attribution label for a disagreement position.
type SpeakerDisplayLabel struct {
	Kind   SpeakerDisplayKind
	Value  string
	Number int
}

// SpeakerDisplayInput bundles the speaker roster (nil when none) and one disagreement position to resolve display labels for.
type SpeakerDisplayInput struct {
	Speakers *MeetingSpeakers
	Position MinutesDebatePosition
}
