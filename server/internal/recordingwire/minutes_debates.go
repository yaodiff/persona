package recordingwire

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

// DebateStatus is whether a disagreement recorded in meeting minutes was settled or left open (#16728,
// docs/assistant/meeting-minutes.md "Disagreements (#16728)"). A closed type on the wire; clients show an unknown
// value with no status word.
type DebateStatus string

const (
	// DebateOpen: people put opposing claims and the meeting ended without deciding between them.
	DebateOpen DebateStatus = "open"
	// DebateSettled: people put opposing claims and the meeting later decided between them.
	DebateSettled DebateStatus = "settled"
)

// Valid reports whether s is one of the two statuses this hub writes.
func (s DebateStatus) Valid() bool { return s == DebateOpen || s == DebateSettled }

// maxDebateAlso is the maximum number of additional supporting lines (`also`) kept on a disagreement position.
const maxDebateAlso = 2

// MinutesDebateAlso is an additional transcript line where another speaker backs the same disagreement position
// (#17674, docs/assistant/meeting-minutes.md "Speakers (#17674)" -> "The minutes").
type MinutesDebateAlso struct {
	At            float64               `json:"at"`
	Span          *SpeakerSpan          `json:"span,omitempty"`
	Est           bool                  `json:"est,omitempty"`
	Over          map[SpeakerID]float64 `json:"over,omitempty"`
	SaidBy        SpeakerID             `json:"said_by,omitempty"`
	Speaker       SpeakerID             `json:"speaker,omitempty"`
	SpeakerUnsure bool                  `json:"speaker_unsure,omitempty"`
}

// MinutesDebatePosition is one side of a disagreement: Stance with no names in it, At the start of the transcript line
// where its holder says it (never a photo), Who only when the transcript or the calendar supports it, plus optional
// speaker-timeline attribution fields (Span, Est, Over, SaidBy, Speaker, SpeakerUnsure, Also).
type MinutesDebatePosition struct {
	Stance        string                `json:"stance"`
	At            float64               `json:"at"`
	Span          *SpeakerSpan          `json:"span,omitempty"`
	Est           bool                  `json:"est,omitempty"`
	Over          map[SpeakerID]float64 `json:"over,omitempty"`
	SaidBy        SpeakerID             `json:"said_by,omitempty"`
	Speaker       SpeakerID             `json:"speaker,omitempty"`
	SpeakerUnsure bool                  `json:"speaker_unsure,omitempty"`
	Also          []MinutesDebateAlso   `json:"also,omitempty"`
	Who           string                `json:"who,omitempty"`
}

// MinutesDebateOutcome is how a settled disagreement ended: Text, At the transcript line where it was decided, and
// Decision the 0-based index in Minutes.Decisions of the kept decision citing the same line (nil when none matches).
type MinutesDebateOutcome struct {
	Text     string  `json:"text"`
	At       float64 `json:"at"`
	Decision *int    `json:"decision,omitempty"`
}

// MinutesDebate is a real disagreement in a meeting: Question, optional Topic hint, Status, At (the earliest
// position's time), 2 to 4 Positions, and Outcome when settled.
type MinutesDebate struct {
	Question  string                  `json:"question"`
	Topic     string                  `json:"topic,omitempty"`
	Status    DebateStatus            `json:"status"`
	At        float64                 `json:"at"`
	Positions []MinutesDebatePosition `json:"positions"`
	Outcome   *MinutesDebateOutcome   `json:"outcome,omitempty"`
}

// decodeMinutesDebates reads detail.minutes.debates leniently: a missing, null, non-array or unreadable block is nil,
// malformed entries are dropped, and a debate left with fewer than two valid positions is dropped, so a broken debates
// block never prevents the rest of minutes.json from decoding.
func decodeMinutesDebates(raw json.RawMessage, decisions int) []MinutesDebate {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var list []any
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	nonBlank := func(v any) (string, bool) {
		s, ok := v.(string)
		return s, ok && strings.TrimSpace(s) != ""
	}
	nonNeg := func(v any) (float64, bool) {
		f, ok := v.(float64)
		return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0
	}
	decodeSpan := func(v any) *SpeakerSpan {
		pair, ok := v.([]any)
		if !ok || len(pair) != 2 {
			return nil
		}
		s, okS := nonNeg(pair[0])
		e, okE := nonNeg(pair[1])
		if !okS || !okE || e < s {
			return nil
		}
		sp := SpeakerSpan{s, e}
		return &sp
	}
	decodeID := func(v any) SpeakerID {
		s, ok := v.(string)
		if !ok || !validStoredSpeakerID(s) {
			return ""
		}
		return SpeakerID(s)
	}
	decodeOver := func(v any) map[SpeakerID]float64 {
		rawOver, ok := v.(map[string]any)
		if !ok || len(rawOver) == 0 {
			return nil
		}
		out := make(map[SpeakerID]float64, len(rawOver))
		for k, val := range rawOver {
			if !validStoredSpeakerID(k) {
				continue
			}
			f, ok := val.(float64)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
				continue
			}
			out[SpeakerID(k)] = f
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	decodeAlso := func(v any) []MinutesDebateAlso {
		rawAlso, ok := v.([]any)
		if !ok || len(rawAlso) == 0 {
			return nil
		}
		var out []MinutesDebateAlso
		for _, ae := range rawAlso {
			if len(out) == maxDebateAlso {
				break
			}
			ao, ok := ae.(map[string]any)
			if !ok {
				continue
			}
			at, ok := nonNeg(ao["at"])
			if !ok {
				continue
			}
			entry := MinutesDebateAlso{
				At:      at,
				Span:    decodeSpan(ao["span"]),
				Over:    decodeOver(ao["over"]),
				SaidBy:  decodeID(ao["said_by"]),
				Speaker: decodeID(ao["speaker"]),
			}
			if est, ok := ao["est"].(bool); ok && est {
				entry.Est = true
			}
			if unsure, ok := ao["speaker_unsure"].(bool); ok && unsure && entry.Speaker != "" {
				entry.SpeakerUnsure = true
			}
			out = append(out, entry)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	var out []MinutesDebate
	for _, entry := range list {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		q, ok := nonBlank(obj["question"])
		if !ok {
			continue
		}
		rawPos, ok := obj["positions"].([]any)
		if !ok {
			continue
		}
		var positions []MinutesDebatePosition
		for _, pe := range rawPos {
			po, ok := pe.(map[string]any)
			if !ok {
				continue
			}
			stance, ok := nonBlank(po["stance"])
			if !ok {
				continue
			}
			pos := MinutesDebatePosition{
				Stance:  stance,
				Span:    decodeSpan(po["span"]),
				Over:    decodeOver(po["over"]),
				SaidBy:  decodeID(po["said_by"]),
				Speaker: decodeID(po["speaker"]),
				Also:    decodeAlso(po["also"]),
			}
			if at, ok := nonNeg(po["at"]); ok {
				pos.At = at
			}
			if est, ok := po["est"].(bool); ok && est {
				pos.Est = true
			}
			if unsure, ok := po["speaker_unsure"].(bool); ok && unsure && pos.Speaker != "" {
				pos.SpeakerUnsure = true
			}
			if who, ok := nonBlank(po["who"]); ok {
				pos.Who = who
			}
			positions = append(positions, pos)
		}
		if len(positions) < 2 {
			continue
		}
		d := MinutesDebate{Question: q, Positions: positions}
		if topic, ok := nonBlank(obj["topic"]); ok {
			d.Topic = topic
		}
		if st, ok := nonBlank(obj["status"]); ok {
			d.Status = DebateStatus(st)
		}
		if at, ok := nonNeg(obj["at"]); ok {
			d.At = at
		}
		if d.Status == DebateSettled {
			if oo, ok := obj["outcome"].(map[string]any); ok {
				if text, ok := nonBlank(oo["text"]); ok {
					outcome := &MinutesDebateOutcome{Text: text}
					if at, ok := nonNeg(oo["at"]); ok {
						outcome.At = at
					}
					if f, ok := nonNeg(oo["decision"]); ok && math.Trunc(f) == f {
						if idx := int(f); idx >= 0 && idx < decisions {
							outcome.Decision = &idx
						}
					}
					d.Outcome = outcome
				}
			}
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validStoredSpeakerID reports whether s is a canonical hub speaker id ("s" followed by a positive decimal integer).
func validStoredSpeakerID(s string) bool {
	if len(s) < 2 || len(s) > 10 || s[0] != 's' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
