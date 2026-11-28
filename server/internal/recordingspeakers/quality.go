package recordingspeakers

import (
	"math"

	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
)

const (
	// qualityMaxUnlabelledNum / qualityMaxUnlabelledDen represent the > 20% (1/5) threshold on unlabelled eligible
	// substantive lines.
	qualityMaxUnlabelledNum = 1
	qualityMaxUnlabelledDen = 5
	// qualityMaxDominantShare is the > 95% speaking-time share threshold for a single speaker.
	qualityMaxDominantShare = 0.95
	// qualityMinAttendeesForDominant is the minimum calendar attendee count for the dominant-speaker rule to apply.
	qualityMinAttendeesForDominant = 3
	// qualityMinSpeechSeconds is the minimum total speaking time in seconds.
	qualityMinSpeechSeconds = 60.0
	// qualityMinCoveredShare is the minimum fraction of recording duration the timeline must cover.
	qualityMinCoveredShare = 0.50
	// qualityMaxAttendees is the maximum calendar attendee count before separation is marked unreliable.
	qualityMaxAttendees = 7
)

// QualityInput holds every input EvaluateQuality needs. The zero QualityInput is no input; only
// QualityInputBuilder.Build makes one.
type QualityInput struct {
	segments  Segments
	labels    LineLabels
	covered   Covered
	duration  Seconds
	stats     Stats
	roster    Roster
	attendees AttendeeCount
}

// QualityCoverageStep is the quality input builder's first stage: covered audio spans and recording duration.
// The zero QualityCoverageStep is a skipped stage, and Build refuses what it leaves out.
type QualityCoverageStep struct{ qualityCoverageStep QualityInput }

// QualityContextStep is the quality input builder's second stage: stats, roster and calendar attendee count.
// The zero QualityContextStep is a skipped stage, and Build refuses what it leaves out.
type QualityContextStep struct{ qualityContextStep QualityInput }

// QualityInputBuilder is the quality input builder's last stage. The zero QualityInputBuilder is a skipped stage, and
// Build refuses what it leaves out.
type QualityInputBuilder struct{ qualityInputBuilder QualityInput }

// NewQualityInputBuilder starts a QualityInput with the meeting's segments and their matching alignment labels.
func NewQualityInputBuilder(segments Segments, labels LineLabels) QualityCoverageStep {
	return QualityCoverageStep{qualityCoverageStep: QualityInput{segments: segments, labels: labels}}
}

// Coverage sets the covered audio spans and total recording duration.
func (s QualityCoverageStep) Coverage(covered Covered, duration Seconds) QualityContextStep {
	q := s.qualityCoverageStep
	q.covered, q.duration = covered, duration
	return QualityContextStep{qualityContextStep: q}
}

// Context sets the per-original-id stats, roster (for merged speaking shares) and calendar attendee count.
func (s QualityContextStep) Context(stats Stats, roster Roster, attendees AttendeeCount) QualityInputBuilder {
	q := s.qualityContextStep
	q.stats, q.roster, q.attendees = stats, roster, attendees
	return QualityInputBuilder{qualityInputBuilder: q}
}

// Build validates the QualityInput; mismatched segment and label lengths or invalid duration/attendees are invalid.
func (b QualityInputBuilder) Build() (out outcome.Of[QualityInput, Fail]) {
	q := b.qualityInputBuilder
	var parts []fail.Invalid
	parts = wrong(parts, len(q.segments.segments) == len(q.labels.lineLabels),
		"recordingspeakers.quality.length_mismatch", "labels")
	parts = wrong(parts, validSeconds(q.duration), "recordingspeakers.quality.invalid_duration", "duration")
	parts = wrong(parts, q.attendees.attendeeCount >= 0, "recordingspeakers.quality.negative_attendees", "attendees")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(q)
}

// QualityVerdict is the result of EvaluateQuality: QualityOk with no reason, or QualityLow with the reason that
// triggered it. The zero QualityVerdict is no verdict; EvaluateQuality never returns one, and Accept raises a defect on
// it.
type QualityVerdict struct {
	quality Quality
	reason  opt.Option[QualityReason]
}

// QualityVerdictVisitor is told the quality and optional low-quality reason.
type QualityVerdictVisitor interface {
	VisitQualityVerdict(quality Quality, reason opt.Option[QualityReason])
}

// IsOk says whether the verdict is QualityOk; the zero QualityVerdict is a defect.
func (v QualityVerdict) IsOk() bool {
	if v.quality == (Quality{}) {
		defect.Raise("recordingspeakers.QualityVerdict.IsOk", "the zero QualityVerdict")
	}
	return v.quality == QualityOk()
}

// Accept tells vis the verdict's quality and optional reason; the zero QualityVerdict is a defect.
func (v QualityVerdict) Accept(vis QualityVerdictVisitor) {
	if v.quality == (Quality{}) {
		defect.Raise("recordingspeakers.QualityVerdict.Accept", "the zero QualityVerdict")
	}
	vis.VisitQualityVerdict(v.quality, v.reason)
}

// EvaluateQuality judges a meeting's speaker separation quality against the four layer-one rules:
//  1. calendar lists more than 7 attendees -> QualityReasonTooManyAttendees
//  2. under 60 seconds of total speech -> QualityReasonTooLittleSpeech
//  3. timeline covers under half of the recording -> QualityReasonLowCoverage
//  4. over a fifth (> 20%) of eligible substantive lines have no label -> QualityReasonTooManyUnlabelled
//  5. one merged speaker has over 95% of speaking time with 3+ attendees -> QualityReasonSingleSpeakerDominant
func EvaluateQuality(input QualityInput) QualityVerdict {
	if len(input.segments.segments) != len(input.labels.lineLabels) {
		defect.Raise("recordingspeakers.EvaluateQuality", "mismatched segments and labels")
	}

	// Rule 4: calendar lists more than seven attendees.
	if input.attendees.attendeeCount > qualityMaxAttendees {
		return QualityVerdict{quality: QualityLow(), reason: opt.Some(QualityReasonTooManyAttendees())}
	}

	// Compute merged speaking times and total speech from stats + roster.
	merged := SumStats(input.stats, input.roster)
	totalSpeech := 0.0
	maxSpeakerSpeech := 0.0
	for _, st := range merged.stats {
		totalSpeech += st.seconds.seconds
		if st.seconds.seconds > maxSpeakerSpeech {
			maxSpeakerSpeech = st.seconds.seconds
		}
	}

	// Rule 3a: under 60 seconds of speech.
	if totalSpeech+floatEps < qualityMinSpeechSeconds {
		return QualityVerdict{quality: QualityLow(), reason: opt.Some(QualityReasonTooLittleSpeech())}
	}

	// Rule 3b: timeline covers under half of the recording.
	if input.duration.seconds > 0 {
		covDur := coveredDurationWithin(input.covered, input.duration.seconds)
		if covDur+floatEps < input.duration.seconds*qualityMinCoveredShare {
			return QualityVerdict{quality: QualityLow(), reason: opt.Some(QualityReasonLowCoverage())}
		}
	}

	// Rule 1: over a fifth of eligible substantive lines have no label (excluding echo, unreliable_timing, hole).
	eligibleSubstantive := 0
	unlabelledSubstantive := 0
	for i, seg := range input.segments.segments {
		if !seg.IsSubstantive() {
			continue
		}
		switch lbl := input.labels.lineLabels[i].(type) {
		case labelSkipped:
			switch lbl.reason {
			case SkipReasonEcho(), SkipReasonUnreliableTiming(), SkipReasonHole():
				continue
			case SkipReasonNoOverlap():
				eligibleSubstantive++
				unlabelledSubstantive++
			}
		case labelAssigned:
			eligibleSubstantive++
		}
	}
	if eligibleSubstantive > 0 && unlabelledSubstantive*qualityMaxUnlabelledDen > eligibleSubstantive*qualityMaxUnlabelledNum {
		return QualityVerdict{quality: QualityLow(), reason: opt.Some(QualityReasonTooManyUnlabelled())}
	}

	// Rule 2: one speaker has over 95% of speaking time while calendar lists 3+ attendees.
	if input.attendees.attendeeCount >= qualityMinAttendeesForDominant &&
		totalSpeech > 0 &&
		maxSpeakerSpeech > totalSpeech*qualityMaxDominantShare+floatEps {
		return QualityVerdict{quality: QualityLow(), reason: opt.Some(QualityReasonSingleSpeakerDominant())}
	}

	return QualityVerdict{quality: QualityOk(), reason: opt.None[QualityReason]()}
}

func coveredDurationWithin(c Covered, limit float64) float64 {
	var clamped []Span
	for _, s := range c.covered {
		lo := math.Max(0, s.start.seconds)
		hi := math.Min(limit, s.end.seconds)
		if hi > lo {
			clamped = append(clamped, Span{start: Seconds{seconds: lo}, end: Seconds{seconds: hi}})
		}
	}
	return spansDuration(mergeSpans(clamped))
}
