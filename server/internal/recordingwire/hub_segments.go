package recordingwire

import (
	"github.com/nwverse/persona/server/internal/kbwire"
)

// HubTranscript is hub_segments.json: the hub's own sentences, with their own revision.
type HubTranscript struct {
	Rev      int64                     `json:"rev"`
	Pass     string                    `json:"pass"` // HubPassLive | HubPassFinal
	Timing   TranscriptTiming          `json:"timing,omitempty"`
	Segments []kbwire.RecordingSegment `json:"segments"`
}

// EffectiveTiming returns h.Timing when known, or TimingSpread for older transcripts without timing.
func (h HubTranscript) EffectiveTiming() TranscriptTiming {
	return h.Timing.Effective()
}
