package recordingspeakers

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
)

const (
	// maxNameRunes is the maximum length of an owner-chosen speaker name in Unicode code points.
	maxNameRunes = 100
	// substantiveMinSeconds is the minimum line duration (in seconds) for a substantive line.
	substantiveMinSeconds = 2.0
	// substantiveMinCJK is the minimum CJK unified ideograph count for a substantive line.
	substantiveMinCJK = 8
	// floatEps absorbs IEEE 754 representation rounding at exact decimal boundaries.
	floatEps = 1e-9
)

// SpeakerID is a stable hub-assigned speaker identifier of the form "s1", "s2", and so on. It is comparable, so it keys
// maps. The zero SpeakerID is no speaker id; ParseSpeakerID never makes one.
type SpeakerID struct{ speaker string }

// ParseSpeakerID is the speaker id raw names: "s" followed by a positive decimal integer without leading zeros.
func ParseSpeakerID(raw string) (out outcome.Of[SpeakerID, Fail]) {
	if parts := speakerIDRule(nil, raw); len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(SpeakerID{speaker: raw})
}

// speakerIDRule adds what is wrong with raw as a speaker id to parts.
func speakerIDRule(parts []fail.Invalid, raw string) []fail.Invalid {
	if strings.TrimSpace(raw) == "" {
		return wrong(parts, false, "recordingspeakers.speaker.empty", "speaker")
	}
	return wrong(parts, validSpeakerDigits(raw), "recordingspeakers.speaker.format", "speaker")
}

// validSpeakerDigits says whether raw is "s<N>" with N >= 1 and no leading zero.
func validSpeakerDigits(raw string) bool {
	n, ok := parseSpeakerDigits(raw)
	return ok && n > 0
}

// parseSpeakerDigits parses "s<N>" with N >= 1 and no leading zero.
func parseSpeakerDigits(raw string) (int, bool) {
	if len(raw) < 2 || raw[0] != 's' || raw[1] < '1' || raw[1] > '9' {
		return 0, false
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(raw[1:])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// speakerIDFromNumber builds the canonical "s<N>" identifier for n >= 1.
func speakerIDFromNumber(n int) SpeakerID {
	if n <= 0 {
		defect.Raise("recordingspeakers.speakerIDFromNumber", "non-positive speaker number")
	}
	return SpeakerID{speaker: "s" + strconv.Itoa(n)}
}

// num returns the positive integer speaker number of x; the zero SpeakerID is a defect.
func (x SpeakerID) num(op string) int {
	n, ok := parseSpeakerDigits(x.speaker)
	if !ok {
		defect.Raise(op, "the zero SpeakerID")
	}
	return n
}

// Wire is the speaker id as the wire carries it. Adapters only.
func (x SpeakerID) Wire() string { return x.speaker }

// SpeakerNumber is a positive speaker number (1 for "s1", 2 for "s2", and so on). The zero SpeakerNumber is no speaker
// number; ParseSpeakerNumber never makes one.
type SpeakerNumber struct{ speakerNumber int }

// ParseSpeakerNumber is the speaker number raw counts; zero or less is invalid.
func ParseSpeakerNumber(raw int) (out outcome.Of[SpeakerNumber, Fail]) {
	if parts := speakerNumberRule(nil, raw); len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(SpeakerNumber{speakerNumber: raw})
}

// speakerNumberRule adds what is wrong with raw as a speaker number to parts.
func speakerNumberRule(parts []fail.Invalid, raw int) []fail.Invalid {
	return wrong(parts, raw > 0, "recordingspeakers.speaker_number.not_positive", "speakerNumber")
}

// Wire is the speaker number as the wire carries it. Adapters only.
func (x SpeakerNumber) Wire() int { return x.speakerNumber }

// SpeakerNumberOf is the positive integer number carried by id ("s2" -> 2); the zero SpeakerID is a defect.
func SpeakerNumberOf(id SpeakerID) SpeakerNumber {
	return SpeakerNumber{speakerNumber: id.num("recordingspeakers.SpeakerNumberOf")}
}

// ClusterID is a raw engine-assigned speaker cluster label before hub numbering (such as "c1"). It is comparable, so it
// keys maps. The zero ClusterID is no cluster id; ParseClusterID never makes one.
type ClusterID struct{ cluster string }

// ParseClusterID is the cluster id raw names; empty or whitespace-only text is invalid.
func ParseClusterID(raw string) (out outcome.Of[ClusterID, Fail]) {
	if parts := clusterIDRule(nil, raw); len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(ClusterID{cluster: strings.TrimSpace(raw)})
}

// clusterIDRule adds what is wrong with raw as a cluster id to parts.
func clusterIDRule(parts []fail.Invalid, raw string) []fail.Invalid {
	return wrong(parts, strings.TrimSpace(raw) != "", "recordingspeakers.cluster.empty", "cluster")
}

// Wire is the cluster id as the wire carries it. Adapters only.
func (c ClusterID) Wire() string { return c.cluster }

// SpeakerName is an owner-chosen speaker name: trimmed, on one line, non-empty, and at most 100 Unicode code points.
// The zero SpeakerName is no speaker name; ParseSpeakerName never makes one.
type SpeakerName struct{ speakerName string }

// ParseSpeakerName validates and trims raw as an owner-chosen speaker name: one line, non-empty after trimming, and at
// most 100 Unicode code points.
func ParseSpeakerName(raw string) (out outcome.Of[SpeakerName, Fail]) {
	var parts []fail.Invalid
	if strings.ContainsAny(raw, "\n\r") {
		parts = wrong(parts, false, "recordingspeakers.name.multiline", "name")
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		parts = wrong(parts, false, "recordingspeakers.name.empty", "name")
	}
	if utf8.RuneCountInString(trimmed) > maxNameRunes {
		parts = wrong(parts, false, "recordingspeakers.name.too_long", "name")
	}
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(SpeakerName{speakerName: trimmed})
}

// Wire is the speaker name as the wire carries it. Adapters only.
func (x SpeakerName) Wire() string { return x.speakerName }

// Seconds is a finite non-negative offset or duration in recording seconds. The zero Seconds is zero seconds;
// ParseSeconds returns it for 0.
type Seconds struct{ seconds float64 }

// ParseSeconds is raw in recording seconds; NaN, infinity or negative values are invalid, and 0 is the zero Seconds.
func ParseSeconds(raw float64) (out outcome.Of[Seconds, Fail]) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.seconds.invalid", "seconds")))
	}
	if raw == 0 {
		raw = 0 // normalize -0.0
	}
	return out.Ok(Seconds{seconds: raw})
}

// Wire is the seconds value as the wire carries it. Adapters only.
func (x Seconds) Wire() float64 { return x.seconds }

// LineCount is a non-negative count of transcript lines. The zero LineCount is zero lines; ParseLineCount returns it
// for 0.
type LineCount struct{ lineCount int }

// ParseLineCount is raw as a line count; negative counts are invalid, and 0 is the zero LineCount.
func ParseLineCount(raw int) (out outcome.Of[LineCount, Fail]) {
	if raw < 0 {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.line_count.negative", "lineCount")))
	}
	return out.Ok(LineCount{lineCount: raw})
}

// Wire is the line count as the wire carries it. Adapters only.
func (x LineCount) Wire() int { return x.lineCount }

// EditCount is a non-negative count of applied speaker edits of one kind. The zero EditCount is zero edits;
// ParseEditCount returns it for 0.
type EditCount struct{ editCount int }

// ParseEditCount is raw as an edit count; negative counts are invalid, and 0 is the zero EditCount.
func ParseEditCount(raw int) (out outcome.Of[EditCount, Fail]) {
	if raw < 0 {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.edit_count.negative", "editCount")))
	}
	return out.Ok(EditCount{editCount: raw})
}

// Wire is the edit count as the wire carries it. Adapters only.
func (x EditCount) Wire() int { return x.editCount }

// AttendeeCount is a non-negative count of calendar attendees (0 when calendar attendees are unknown or absent).
// The zero AttendeeCount is zero attendees; ParseAttendeeCount returns it for 0.
type AttendeeCount struct{ attendeeCount int }

// ParseAttendeeCount is raw as an attendee count; negative counts are invalid, and 0 is the zero AttendeeCount.
func ParseAttendeeCount(raw int) (out outcome.Of[AttendeeCount, Fail]) {
	if raw < 0 {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.attendees.negative", "attendees")))
	}
	return out.Ok(AttendeeCount{attendeeCount: raw})
}

// Wire is the attendee count as the wire carries it. Adapters only.
func (x AttendeeCount) Wire() int { return x.attendeeCount }

// LineText is the text of a transcript segment, used to check whether a line is substantive. The zero LineText is empty
// text; ParseLineText returns it for "".
type LineText struct{ lineText string }

// ParseLineText wraps raw transcript line text.
func ParseLineText(raw string) LineText {
	return LineText{lineText: raw}
}

// Wire is the line text as the wire carries it. Adapters only.
func (x LineText) Wire() string { return x.lineText }

// Span is a recording-seconds interval [start, end] with start <= end. The zero Span is [0, 0].
type Span struct {
	start Seconds
	end   Seconds
}

// SpanVisitor is told a span's start and end seconds.
type SpanVisitor interface {
	VisitSpan(start, end Seconds)
}

// NewSpan builds [start, end]; end before start is invalid.
func NewSpan(start, end Seconds) (out outcome.Of[Span, Fail]) {
	if !validSeconds(start) || !validSeconds(end) {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.span.invalid_seconds", "span")))
	}
	if end.seconds < start.seconds {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.span.inverted", "span")))
	}
	return out.Ok(Span{start: start, end: end})
}

// Accept tells v the span's start and end.
func (s Span) Accept(v SpanVisitor) {
	v.VisitSpan(s.start, s.end)
}

// duration returns end - start (clamped at 0).
func (s Span) duration() float64 {
	if s.end.seconds <= s.start.seconds {
		return 0
	}
	return s.end.seconds - s.start.seconds
}

// validSeconds checks that s is finite and non-negative.
func validSeconds(s Seconds) bool {
	return !math.IsNaN(s.seconds) && !math.IsInf(s.seconds, 0) && s.seconds >= 0
}

// RawTurn is one engine-labelled speaking interval before hub numbering. The zero RawTurn is no turn; NewRawTurn never
// makes one, and Accept raises a defect on it.
type RawTurn struct {
	span    Span
	cluster ClusterID
}

// RawTurnVisitor is told a raw turn's span and engine cluster id.
type RawTurnVisitor interface {
	VisitRawTurn(span Span, cluster ClusterID)
}

// NewRawTurn builds a raw turn; a zero cluster or non-positive span duration is invalid.
func NewRawTurn(span Span, cluster ClusterID) (out outcome.Of[RawTurn, Fail]) {
	var parts []fail.Invalid
	parts = wrong(parts, span.end.seconds > span.start.seconds, "recordingspeakers.turn.empty", "span")
	parts = wrong(parts, cluster != (ClusterID{}), "recordingspeakers.turn.no_cluster", "cluster")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(RawTurn{span: span, cluster: cluster})
}

// Accept tells v the raw turn's span and cluster; the zero RawTurn is a defect.
func (t RawTurn) Accept(v RawTurnVisitor) {
	if t.cluster == (ClusterID{}) {
		defect.Raise("recordingspeakers.RawTurn.Accept", "the zero RawTurn")
	}
	v.VisitRawTurn(t.span, t.cluster)
}

// RawTurns is a sequence of engine-labelled turns before hub numbering. The zero RawTurns is empty.
type RawTurns struct{ rawTurns []RawTurn }

// RawTurnsVisitor is told each raw turn in order.
type RawTurnsVisitor interface {
	VisitRawTurn(turn RawTurn)
}

// NewRawTurns copies list into RawTurns; any zero RawTurn in list is a defect.
func NewRawTurns(list []RawTurn) RawTurns {
	for i := range list {
		if list[i].cluster == (ClusterID{}) {
			defect.Raise("recordingspeakers.NewRawTurns", "the zero RawTurn")
		}
	}
	return RawTurns{rawTurns: slices.Clone(list)}
}

// Accept tells v each raw turn in order.
func (t RawTurns) Accept(v RawTurnsVisitor) {
	for i := range t.rawTurns {
		v.VisitRawTurn(t.rawTurns[i])
	}
}

// Turn is one hub-numbered speaking interval with an original SpeakerID. The zero Turn is no turn; NewTurn never makes
// one, and Accept raises a defect on it.
type Turn struct {
	span    Span
	speaker SpeakerID
}

// TurnVisitor is told a turn's span and original speaker id.
type TurnVisitor interface {
	VisitTurn(span Span, speaker SpeakerID)
}

// NewTurn builds a hub-numbered turn; a zero speaker or non-positive span duration is invalid.
func NewTurn(span Span, speaker SpeakerID) (out outcome.Of[Turn, Fail]) {
	var parts []fail.Invalid
	parts = wrong(parts, span.end.seconds > span.start.seconds, "recordingspeakers.turn.empty", "span")
	parts = wrong(parts, speaker != (SpeakerID{}), "recordingspeakers.turn.no_speaker", "speaker")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(Turn{span: span, speaker: speaker})
}

// Accept tells v the turn's span and speaker id; the zero Turn is a defect.
func (t Turn) Accept(v TurnVisitor) {
	if t.speaker == (SpeakerID{}) {
		defect.Raise("recordingspeakers.Turn.Accept", "the zero Turn")
	}
	v.VisitTurn(t.span, t.speaker)
}

// Turns is a speaker timeline of hub-numbered turns. The zero Turns is empty.
type Turns struct{ turns []Turn }

// TurnsVisitor is told each turn in order.
type TurnsVisitor interface {
	VisitTurn(turn Turn)
}

// NewTurns copies list into Turns; any zero Turn in list is a defect.
func NewTurns(list []Turn) Turns {
	for i := range list {
		if list[i].speaker == (SpeakerID{}) {
			defect.Raise("recordingspeakers.NewTurns", "the zero Turn")
		}
	}
	return Turns{turns: slices.Clone(list)}
}

// Accept tells v each turn in order.
func (t Turns) Accept(v TurnsVisitor) {
	for i := range t.turns {
		v.VisitTurn(t.turns[i])
	}
}

// Covered is the list of recording-second spans the hub held audio for. The zero Covered is empty (no audio covered).
type Covered struct{ covered []Span }

// CoveredVisitor is told each covered span in order.
type CoveredVisitor interface {
	VisitSpan(span Span)
}

// NewCovered copies list into Covered; any span with end before start is invalid.
func NewCovered(list []Span) (out outcome.Of[Covered, Fail]) {
	for i := range list {
		if !validSeconds(list[i].start) || !validSeconds(list[i].end) || list[i].end.seconds < list[i].start.seconds {
			return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.covered.invalid_span", "covered")))
		}
	}
	return out.Ok(Covered{covered: slices.Clone(list)})
}

// Accept tells v each covered span in order.
func (c Covered) Accept(v CoveredVisitor) {
	for i := range c.covered {
		v.VisitSpan(c.covered[i])
	}
}

// Segment is one transcript line for speaker alignment and quality evaluation. The zero Segment is no segment;
// NewSegment never makes one, and every method raises a defect on it.
type Segment struct {
	span   Span
	source LineSource
	echo   Echo
	text   LineText
}

// SegmentVisitor is told a segment's parts.
type SegmentVisitor interface {
	VisitSegment(span Span, source LineSource, echo Echo, text LineText)
}

// NewSegment builds a transcript segment; a zero source or echo, or an inverted span, is invalid.
func NewSegment(span Span, source LineSource, echo Echo, text LineText) (out outcome.Of[Segment, Fail]) {
	var parts []fail.Invalid
	parts = wrong(parts, validSeconds(span.start) && validSeconds(span.end) && span.end.seconds >= span.start.seconds,
		"recordingspeakers.segment.invalid_span", "span")
	parts = wrong(parts, source != (LineSource{}), "recordingspeakers.segment.no_source", "source")
	parts = wrong(parts, echo != (Echo{}), "recordingspeakers.segment.no_echo", "echo")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(Segment{span: span, source: source, echo: echo, text: text})
}

// Accept tells v the segment's parts; the zero Segment is a defect.
func (s Segment) Accept(v SegmentVisitor) {
	s.known("recordingspeakers.Segment.Accept")
	v.VisitSegment(s.span, s.source, s.echo, s.text)
}

// IsSubstantive says whether s is a substantive line for quality checks: duration at least 2 seconds, or at least 8 CJK
// unified ideographs (English lines are judged by duration only). The zero Segment is a defect.
func (s Segment) IsSubstantive() bool {
	s.known("recordingspeakers.Segment.IsSubstantive")
	if s.span.duration()+floatEps >= substantiveMinSeconds {
		return true
	}
	return countCJKIdeographs(s.text.lineText) >= substantiveMinCJK
}

// known raises a defect when s is the zero Segment.
func (s Segment) known(op string) {
	if s.source == (LineSource{}) || s.echo == (Echo{}) {
		defect.Raise(op, "the zero Segment")
	}
}

// countCJKIdeographs counts CJK unified ideographs (Unicode Han ideographs) in s.
func countCJKIdeographs(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}

// Segments is a sequence of transcript lines in recording order. The zero Segments is empty.
type Segments struct{ segments []Segment }

// SegmentsVisitor is told each segment in order.
type SegmentsVisitor interface {
	VisitSegment(segment Segment)
}

// NewSegments copies list into Segments; any zero Segment in list is a defect.
func NewSegments(list []Segment) Segments {
	for i := range list {
		list[i].known("recordingspeakers.NewSegments")
	}
	return Segments{segments: slices.Clone(list)}
}

// Accept tells v each segment in order.
func (s Segments) Accept(v SegmentsVisitor) {
	for i := range s.segments {
		v.VisitSegment(s.segments[i])
	}
}

// AlignEnv bundles the timeline inputs shared across all segments of a meeting: its turns, covered audio spans, and
// transcript timing reliability. The zero AlignEnv is no environment; NewAlignEnv never makes one.
type AlignEnv struct {
	turns   Turns
	covered Covered
	timing  Timing
}

// AlignEnvVisitor is told the turns, covered spans and timing of an AlignEnv.
type AlignEnvVisitor interface {
	VisitAlignEnv(turns Turns, covered Covered, timing Timing)
}

// NewAlignEnv builds an alignment environment; a zero timing is invalid.
func NewAlignEnv(turns Turns, covered Covered, timing Timing) (out outcome.Of[AlignEnv, Fail]) {
	if timing == (Timing{}) {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.env.no_timing", "timing")))
	}
	return out.Ok(AlignEnv{turns: turns, covered: covered, timing: timing})
}

// Accept tells v the environment's parts; the zero AlignEnv is a defect.
func (e AlignEnv) Accept(v AlignEnvVisitor) {
	if e.timing == (Timing{}) {
		defect.Raise("recordingspeakers.AlignEnv.Accept", "the zero AlignEnv")
	}
	v.VisitAlignEnv(e.turns, e.covered, e.timing)
}
