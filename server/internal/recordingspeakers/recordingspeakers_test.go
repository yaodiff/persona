package recordingspeakers_test

import (
	"math"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/domaintest"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/recordingspeakers"
)

// --- Helpers & black-box visitors ---

func mustSpeakerID(t *testing.T, raw string) recordingspeakers.SpeakerID {
	t.Helper()
	return domaintest.Ok(t, recordingspeakers.ParseSpeakerID(raw))
}

func mustClusterID(t *testing.T, raw string) recordingspeakers.ClusterID {
	t.Helper()
	return domaintest.Ok(t, recordingspeakers.ParseClusterID(raw))
}

func mustName(t *testing.T, raw string) recordingspeakers.SpeakerName {
	t.Helper()
	return domaintest.Ok(t, recordingspeakers.ParseSpeakerName(raw))
}

func mustSeconds(t *testing.T, raw float64) recordingspeakers.Seconds {
	t.Helper()
	return domaintest.Ok(t, recordingspeakers.ParseSeconds(raw))
}

func mustSpan(t *testing.T, a, b float64) recordingspeakers.Span {
	t.Helper()
	return domaintest.Ok(t, recordingspeakers.NewSpan(mustSeconds(t, a), mustSeconds(t, b)))
}

func mustPerson(t *testing.T, id string, name string, owner bool, mergedInto string, minor bool) recordingspeakers.Person {
	t.Helper()
	nOpt := opt.None[recordingspeakers.SpeakerName]()
	if name != "" {
		nOpt = opt.Some(mustName(t, name))
	}
	om := recordingspeakers.OwnerMarkOther()
	if owner {
		om = recordingspeakers.OwnerMarkOwner()
	}
	mOpt := opt.None[recordingspeakers.SpeakerID]()
	if mergedInto != "" {
		mOpt = opt.Some(mustSpeakerID(t, mergedInto))
	}
	mm := recordingspeakers.MinorMarkNormal()
	if minor {
		mm = recordingspeakers.MinorMarkMinor()
	}
	return domaintest.Ok(t, recordingspeakers.NewPersonBuilder(mustSpeakerID(t, id)).
		Naming(nOpt, om).
		Status(mOpt, mm).
		Build())
}

func expectDefect(t *testing.T, fn func()) {
	t.Helper()
	var caught string
	func() {
		defer defect.Catch(func(d defect.Defect) { caught = d.Error() })
		fn()
	}()
	if caught == "" {
		t.Fatalf("expected a defect, got none")
	}
}

type labelSpy struct {
	assigned  bool
	speaker   recordingspeakers.SpeakerID
	certainty recordingspeakers.Certainty
	skipped   bool
	reason    recordingspeakers.SkipReason
}

func (s *labelSpy) VisitAssigned(speaker recordingspeakers.SpeakerID, certainty recordingspeakers.Certainty) {
	s.assigned, s.speaker, s.certainty = true, speaker, certainty
}

func (s *labelSpy) VisitSkipped(reason recordingspeakers.SkipReason) {
	s.skipped, s.reason = true, reason
}

type alignmentSpy struct {
	overlaps recordingspeakers.Overlaps
	label    recordingspeakers.LineLabel
}

func (s *alignmentSpy) VisitLineAlignment(overlaps recordingspeakers.Overlaps, label recordingspeakers.LineLabel) {
	s.overlaps, s.label = overlaps, label
}

type editedSpy struct {
	roster recordingspeakers.Roster
	undo   recordingspeakers.EditOps
}

func (s *editedSpy) VisitEdited(roster recordingspeakers.Roster, undo recordingspeakers.EditOps) {
	s.roster, s.undo = roster, undo
}

type rosterPeopleSpy struct {
	next   recordingspeakers.SpeakerNumber
	people []recordingspeakers.Person
	edits  recordingspeakers.EditCounts
}

func (s *rosterPeopleSpy) VisitNext(next recordingspeakers.SpeakerNumber) { s.next = next }
func (s *rosterPeopleSpy) VisitPerson(p recordingspeakers.Person)         { s.people = append(s.people, p) }
func (s *rosterPeopleSpy) VisitEdits(e recordingspeakers.EditCounts)      { s.edits = e }

type displaySpy struct {
	kind   string
	name   recordingspeakers.SpeakerName
	number recordingspeakers.SpeakerNumber
}

func (s *displaySpy) VisitNamed(name recordingspeakers.SpeakerName) { s.kind, s.name = "named", name }
func (s *displaySpy) VisitNumbered(num recordingspeakers.SpeakerNumber) {
	s.kind, s.number = "numbered", num
}
func (s *displaySpy) VisitWho(who recordingspeakers.SpeakerName) { s.kind, s.name = "who", who }
func (s *displaySpy) VisitNone()                                 { s.kind = "none" }

type qualitySpy struct {
	quality recordingspeakers.Quality
	reason  opt.Option[recordingspeakers.QualityReason]
}

func (s *qualitySpy) VisitQualityVerdict(q recordingspeakers.Quality, reason opt.Option[recordingspeakers.QualityReason]) {
	s.quality, s.reason = q, reason
}

// --- Value parsing & zero-value defect tests ---

func TestParseSpeakerIDRules(t *testing.T) {
	id := mustSpeakerID(t, "s12")
	if recordingspeakers.SpeakerNumberOf(id) != domaintest.Ok(t, recordingspeakers.ParseSpeakerNumber(12)) {
		t.Fatalf("SpeakerNumberOf mismatch")
	}
	for _, bad := range []struct {
		raw  string
		want string
	}{
		{"", "invalid recordingspeakers.speaker.empty speaker"},
		{"   ", "invalid recordingspeakers.speaker.empty speaker"},
		{"s0", "invalid recordingspeakers.speaker.format speaker"},
		{"s01", "invalid recordingspeakers.speaker.format speaker"},
		{"S1", "invalid recordingspeakers.speaker.format speaker"},
		{"c1", "invalid recordingspeakers.speaker.format speaker"},
		{"s-1", "invalid recordingspeakers.speaker.format speaker"},
		{" s1 ", "invalid recordingspeakers.speaker.format speaker"},
	} {
		if got := domaintest.Fails(t, recordingspeakers.ParseSpeakerID(bad.raw)); got != bad.want {
			t.Errorf("ParseSpeakerID(%q) = %q, want %q", bad.raw, got, bad.want)
		}
	}
	var zeroID recordingspeakers.SpeakerID
	expectDefect(t, func() { _ = recordingspeakers.SpeakerNumberOf(zeroID) })
}

func TestParseSpeakerNameRules(t *testing.T) {
	trimmed := mustName(t, "  Ada  ")
	exact := mustName(t, "Ada")
	if trimmed != exact {
		t.Fatalf("expected trimmed name to equal exact name")
	}
	// Exactly 100 CJK runes passes; 101 fails.
	_ = mustName(t, strings.Repeat("陈", 100))
	if got := domaintest.Fails(t, recordingspeakers.ParseSpeakerName(strings.Repeat("陈", 101))); got != "invalid recordingspeakers.name.too_long name" {
		t.Errorf("101 runes = %q", got)
	}
	if got := domaintest.Fails(t, recordingspeakers.ParseSpeakerName("Ada\nGrace")); got != "invalid recordingspeakers.name.multiline name" {
		t.Errorf("multiline = %q", got)
	}
	if got := domaintest.Fails(t, recordingspeakers.ParseSpeakerName("   ")); got != "invalid recordingspeakers.name.empty name" {
		t.Errorf("empty = %q", got)
	}
}

func TestParseNumericValues(t *testing.T) {
	var zeroSec recordingspeakers.Seconds
	if domaintest.Ok(t, recordingspeakers.ParseSeconds(0)) != zeroSec {
		t.Fatalf("ParseSeconds(0) must be zero Seconds")
	}
	if domaintest.Ok(t, recordingspeakers.ParseSeconds(math.Copysign(0, -1))) != zeroSec {
		t.Fatalf("ParseSeconds(-0.0) must normalize to zero Seconds")
	}
	for _, bad := range []float64{-0.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := domaintest.Fails(t, recordingspeakers.ParseSeconds(bad)); got != "invalid recordingspeakers.seconds.invalid seconds" {
			t.Errorf("ParseSeconds(%v) = %q", bad, got)
		}
	}
	if got := domaintest.Fails(t, recordingspeakers.ParseLineCount(-1)); got != "invalid recordingspeakers.line_count.negative lineCount" {
		t.Errorf("ParseLineCount(-1) = %q", got)
	}
	if got := domaintest.Fails(t, recordingspeakers.ParseEditCount(-1)); got != "invalid recordingspeakers.edit_count.negative editCount" {
		t.Errorf("ParseEditCount(-1) = %q", got)
	}
	if got := domaintest.Fails(t, recordingspeakers.ParseAttendeeCount(-1)); got != "invalid recordingspeakers.attendees.negative attendees" {
		t.Errorf("ParseAttendeeCount(-1) = %q", got)
	}
	if got := domaintest.Fails(t, recordingspeakers.ParseSpeakerNumber(0)); got != "invalid recordingspeakers.speaker_number.not_positive speakerNumber" {
		t.Errorf("ParseSpeakerNumber(0) = %q", got)
	}
	if got := domaintest.Fails(t, recordingspeakers.NewSpan(mustSeconds(t, 10), mustSeconds(t, 5))); got != "invalid recordingspeakers.span.inverted span" {
		t.Errorf("inverted span = %q", got)
	}
}

func TestPersonBuilderSkippedStageListsMissingParts(t *testing.T) {
	var skipped recordingspeakers.PersonBuilder
	got := domaintest.Fails(t, skipped.Build())
	want := "invalid recordingspeakers.person.no_id speaker; invalid recordingspeakers.person.no_owner owner; invalid recordingspeakers.person.no_minor minor"
	if got != want {
		t.Errorf("Build = %q, want %q", got, want)
	}
}

func TestZeroValuesRaiseDefects(t *testing.T) {
	var zeroState recordingspeakers.State
	var zeroQuality recordingspeakers.Quality
	var zeroTiming recordingspeakers.Timing
	var zeroPerson recordingspeakers.Person
	var zeroSeg recordingspeakers.Segment
	var zeroEnv recordingspeakers.AlignEnv
	var zeroVerdict recordingspeakers.QualityVerdict

	expectDefect(t, func() { zeroState.Accept(nil) })
	expectDefect(t, func() { zeroQuality.Accept(nil) })
	expectDefect(t, func() { zeroTiming.Accept(nil) })
	expectDefect(t, func() { _ = zeroPerson.ID() })
	expectDefect(t, func() { _ = zeroSeg.IsSubstantive() })
	expectDefect(t, func() { zeroEnv.Accept(nil) })
	expectDefect(t, func() { _ = zeroVerdict.IsOk() })
}

func TestSegmentIsSubstantiveRules(t *testing.T) {
	mk := func(a, b float64, text string) recordingspeakers.Segment {
		return domaintest.Ok(t, recordingspeakers.NewSegment(
			mustSpan(t, a, b),
			recordingspeakers.LineSourceHub(),
			recordingspeakers.EchoSpeech(),
			recordingspeakers.ParseLineText(text),
		))
	}
	if !mk(0, 2.0, "ok").IsSubstantive() {
		t.Errorf("2.0s line should be substantive")
	}
	if mk(0, 1.9, "many english words in a short utterance").IsSubstantive() {
		t.Errorf("1.9s English line should not be substantive")
	}
	if !mk(0, 1.0, "我们今天先讨论这").IsSubstantive() {
		t.Errorf("1.0s line with 8 CJK chars should be substantive")
	}
	if mk(0, 1.0, "我们今天先讨论").IsSubstantive() {
		t.Errorf("1.0s line with 7 CJK chars should not be substantive")
	}
}

// --- Acceptance (2-2): Merges ---
// "Giving two speakers one name merges them; they can be unmerged; the unmerged one has no name;
// unmerging before and after the transcript expired gives the same attribution (2-2, 2-4)."
func TestAcceptanceMergesAndUnmergeBeforeAndAfterTrimGiveSameAttribution(t *testing.T) {
	// Timeline where s1 speaks [0, 4], s2 speaks [4, 8], s3 speaks [8, 10] on a line [0, 10].
	// And s1 has 120s total, s2 has 60s total, s3 has 50s total.
	turns := recordingspeakers.NewTurns([]recordingspeakers.Turn{
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 0, 4), mustSpeakerID(t, "s1"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 4, 8), mustSpeakerID(t, "s2"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 8, 10), mustSpeakerID(t, "s3"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 20, 136), mustSpeakerID(t, "s1"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 140, 196), mustSpeakerID(t, "s2"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 200, 248), mustSpeakerID(t, "s3"))),
	})
	cov := domaintest.Ok(t, recordingspeakers.NewCovered([]recordingspeakers.Span{mustSpan(t, 0, 260)}))
	env := domaintest.Ok(t, recordingspeakers.NewAlignEnv(turns, cov, recordingspeakers.TimingExact()))
	line := domaintest.Ok(t, recordingspeakers.NewSegment(
		mustSpan(t, 0, 10),
		recordingspeakers.LineSourceHub(),
		recordingspeakers.EchoSpeech(),
		recordingspeakers.ParseLineText("Per item is easier to cap"),
	))
	stats := recordingspeakers.ComputeStats(turns, recordingspeakers.NewSegments([]recordingspeakers.Segment{line}), recordingspeakers.TimingExact(), cov)

	var zeroEdits recordingspeakers.EditCounts
	initialRoster := domaintest.Ok(t, recordingspeakers.NewRoster(
		domaintest.Ok(t, recordingspeakers.ParseSpeakerNumber(4)),
		[]recordingspeakers.Person{
			mustPerson(t, "s1", "Ada", false, "", false),
			mustPerson(t, "s2", "Grace", false, "", false),
			mustPerson(t, "s3", "", false, "", false),
		},
		zeroEdits,
	))

	// Step 1: Give s2 the same name as s1 ("Ada") -> s2 (60s) auto-merges into s1 (120s).
	mergeOps := recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpName(mustSpeakerID(t, "s2"), opt.Some(mustName(t, "Ada")), recordingspeakers.OwnerMarkOther()),
	})
	var mergedEd editedSpy
	domaintest.Ok(t, recordingspeakers.Edit(initialRoster, stats, mergeOps)).Accept(&mergedEd)

	// Align with live transcript (stores layer-1 over = {s1: 4, s2: 4, s3: 2}; layer-2 sums s1+s2 = 8/10 = 80% sure!).
	var liveMerged alignmentSpy
	recordingspeakers.AlignLine(line, env, mergedEd.roster, recordingspeakers.QualityOk()).Accept(&liveMerged)
	var liveMergedLabel labelSpy
	liveMerged.label.Accept(&liveMergedLabel)
	if !liveMergedLabel.assigned || liveMergedLabel.speaker != mustSpeakerID(t, "s1") || liveMergedLabel.certainty != recordingspeakers.CertaintySure() {
		t.Fatalf("merged attribution expected sure s1")
	}

	// Step 2: Unmerge s2 -> s2 comes back unnamed.
	unmergeOps := recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpUnmerge(mustSpeakerID(t, "s2")),
	})
	var unmergedEd editedSpy
	domaintest.Ok(t, recordingspeakers.Edit(mergedEd.roster, stats, unmergeOps)).Accept(&unmergedEd)

	var rp rosterPeopleSpy
	unmergedEd.roster.Accept(&rp)
	if rp.people[1].IsMerged() {
		t.Fatalf("s2 should be unmerged")
	}
	// Verify s2 has no name (row 2 display returns SpeakerNumber(2)).
	var disp displaySpy
	recordingspeakers.ResolveDisplay(
		domaintest.Ok(t, recordingspeakers.NewClaim(opt.Some(mustSpeakerID(t, "s2")), recordingspeakers.CertaintySure(), opt.None[recordingspeakers.SpeakerName]())),
		unmergedEd.roster,
	).Accept(&disp)
	if disp.kind != "numbered" || disp.number != domaintest.Ok(t, recordingspeakers.ParseSpeakerNumber(2)) {
		t.Fatalf("unmerged s2 must be unnamed (numbered 2), got kind=%q", disp.kind)
	}

	// Step 3: Compare attribution after unmerging BEFORE trim (live AlignLine from turns) vs AFTER trim (AlignOverlap from stored layer-1 over).
	var beforeTrim alignmentSpy
	recordingspeakers.AlignLine(line, env, unmergedEd.roster, recordingspeakers.QualityOk()).Accept(&beforeTrim)
	afterTrimLabel := recordingspeakers.AlignOverlap(liveMerged.overlaps, unmergedEd.roster, recordingspeakers.QualityOk())

	var beforeSpy, afterSpy labelSpy
	beforeTrim.label.Accept(&beforeSpy)
	afterTrimLabel.Accept(&afterSpy)
	if beforeSpy != afterSpy {
		t.Fatalf("attribution before trim differs from attribution after trim")
	}
	// Specifically: s1=4, s2=4, s3=2 -> tie at 4/10 (40% <= 70%), so s1 unsure in both!
	if !beforeSpy.assigned || beforeSpy.speaker != mustSpeakerID(t, "s1") || beforeSpy.certainty != recordingspeakers.CertaintyUnsure() {
		t.Fatalf("expected unsure s1 after unmerge, got assigned=%v", beforeSpy.assigned)
	}
}

// --- Acceptance (2-2): Low quality ---
// "With quality low the minutes attribute no position to a speaker (a transcript who still shows)
// and every number in the transcript carries '?' (2-2, 2-4)."
func TestAcceptanceLowQualityUnsureTranscriptAndNoSpeakerAttributionInMinutes(t *testing.T) {
	turns := recordingspeakers.NewTurns([]recordingspeakers.Turn{
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 0, 40), mustSpeakerID(t, "s1"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 40, 80), mustSpeakerID(t, "s2"))),
	})
	cov := domaintest.Ok(t, recordingspeakers.NewCovered([]recordingspeakers.Span{mustSpan(t, 0, 100)}))
	env := domaintest.Ok(t, recordingspeakers.NewAlignEnv(turns, cov, recordingspeakers.TimingExact()))
	var zeroEdits recordingspeakers.EditCounts
	roster := domaintest.Ok(t, recordingspeakers.NewRoster(
		domaintest.Ok(t, recordingspeakers.ParseSpeakerNumber(3)),
		[]recordingspeakers.Person{
			mustPerson(t, "s1", "Ada", false, "", false),
			mustPerson(t, "s2", "Grace", false, "", false),
		},
		zeroEdits,
	))
	seg1 := domaintest.Ok(t, recordingspeakers.NewSegment(mustSpan(t, 5, 15), recordingspeakers.LineSourceHub(), recordingspeakers.EchoSpeech(), recordingspeakers.ParseLineText("First position")))
	seg2 := domaintest.Ok(t, recordingspeakers.NewSegment(mustSpan(t, 45, 55), recordingspeakers.LineSourceHub(), recordingspeakers.EchoSpeech(), recordingspeakers.ParseLineText("Second position")))
	segs := recordingspeakers.NewSegments([]recordingspeakers.Segment{seg1, seg2})

	// EvaluateQuality flags >7 attendees as low.
	okLabels := recordingspeakers.AlignSegments(segs, env, roster, recordingspeakers.QualityOk())
	stats := recordingspeakers.ComputeStats(turns, segs, recordingspeakers.TimingExact(), cov)
	qi := domaintest.Ok(t, recordingspeakers.NewQualityInputBuilder(segs, okLabels).
		Coverage(cov, mustSeconds(t, 100)).
		Context(stats, roster, domaintest.Ok(t, recordingspeakers.ParseAttendeeCount(8))).
		Build())
	verdict := recordingspeakers.EvaluateQuality(qi)
	if verdict.IsOk() {
		t.Fatalf("expected low quality for 8 attendees")
	}
	var qs qualitySpy
	verdict.Accept(&qs)
	if qs.quality != recordingspeakers.QualityLow() {
		t.Fatalf("expected QualityLow")
	}

	// Align with low quality: every labelled transcript line is marked unsure ("?").
	var a1, a2 alignmentSpy
	recordingspeakers.AlignLine(seg1, env, roster, qs.quality).Accept(&a1)
	recordingspeakers.AlignLine(seg2, env, roster, qs.quality).Accept(&a2)
	var l1, l2 labelSpy
	a1.label.Accept(&l1)
	a2.label.Accept(&l2)
	if !l1.assigned || l1.certainty != recordingspeakers.CertaintyUnsure() {
		t.Fatalf("seg1 under low quality must be unsure")
	}
	if !l2.assigned || l2.certainty != recordingspeakers.CertaintyUnsure() {
		t.Fatalf("seg2 under low quality must be unsure")
	}

	// Display resolution: position with transcript `who` shows `who`; without `who` shows nothing (never speaker).
	var dWithWho, dWithoutWho displaySpy
	recordingspeakers.ResolveDisplay(
		domaintest.Ok(t, recordingspeakers.NewClaim(opt.Some(l1.speaker), l1.certainty, opt.Some(mustName(t, "Grace")))),
		roster,
	).Accept(&dWithWho)
	if dWithWho.kind != "who" || dWithWho.name != mustName(t, "Grace") {
		t.Fatalf("low quality position with who should show who, got kind=%q", dWithWho.kind)
	}
	recordingspeakers.ResolveDisplay(
		domaintest.Ok(t, recordingspeakers.NewClaim(opt.Some(l2.speaker), l2.certainty, opt.None[recordingspeakers.SpeakerName]())),
		roster,
	).Accept(&dWithoutWho)
	if dWithoutWho.kind != "none" {
		t.Fatalf("low quality position without who should show none, got kind=%q", dWithoutWho.kind)
	}
}

func TestTrimCoveredAndEmptyOpsAndErrorPaths(t *testing.T) {
	cov := domaintest.Ok(t, recordingspeakers.NewCovered([]recordingspeakers.Span{
		mustSpan(t, 0, 30),
		mustSpan(t, 40, 70),
		mustSpan(t, 80, 100),
	}))
	trimmed := recordingspeakers.TrimCovered(cov, mustSeconds(t, 50))
	// Line at [45, 49] stays covered; line at [51, 60] is now in a hole.
	segIn := domaintest.Ok(t, recordingspeakers.NewSegment(mustSpan(t, 45, 49), recordingspeakers.LineSourceHub(), recordingspeakers.EchoSpeech(), recordingspeakers.ParseLineText("in")))
	segOut := domaintest.Ok(t, recordingspeakers.NewSegment(mustSpan(t, 51, 60), recordingspeakers.LineSourceHub(), recordingspeakers.EchoSpeech(), recordingspeakers.ParseLineText("out")))
	if !recordingspeakers.CheckLine(segIn, recordingspeakers.TimingExact(), trimmed).IsNone() {
		t.Errorf("line inside trimmed covered span should be eligible")
	}
	if recordingspeakers.CheckLine(segOut, recordingspeakers.TimingExact(), trimmed).IsNone() {
		t.Errorf("line past trimmed length should be in a hole")
	}

	var zeroEdits recordingspeakers.EditCounts
	// Empty ops fails invalid.
	roster := domaintest.Ok(t, recordingspeakers.NewRoster(
		domaintest.Ok(t, recordingspeakers.ParseSpeakerNumber(3)),
		[]recordingspeakers.Person{
			mustPerson(t, "s1", "Ada", false, "", false),
			mustPerson(t, "s2", "", false, "s1", false),
		},
		zeroEdits,
	))
	var emptyStats recordingspeakers.Stats
	if got := domaintest.Fails(t, recordingspeakers.Edit(roster, emptyStats, recordingspeakers.NewEditOps(nil))); got != "invalid recordingspeakers.ops.empty ops" {
		t.Errorf("empty ops = %q", got)
	}
	// Naming a merged speaker is refused.
	if got := domaintest.Fails(t, recordingspeakers.Edit(roster, emptyStats, recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpName(mustSpeakerID(t, "s2"), opt.Some(mustName(t, "Grace")), recordingspeakers.OwnerMarkOther()),
	}))); got != "refused recordingspeakers.speaker.merged" {
		t.Errorf("name merged speaker = %q", got)
	}
	// Unmerging an unmerged speaker is refused.
	if got := domaintest.Fails(t, recordingspeakers.Edit(roster, emptyStats, recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpUnmerge(mustSpeakerID(t, "s1")),
	}))); got != "refused recordingspeakers.speaker.not_merged" {
		t.Errorf("unmerge unmerged = %q", got)
	}
	// Self-merge is invalid.
	if got := domaintest.Fails(t, recordingspeakers.Edit(roster, emptyStats, recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpMerge(mustSpeakerID(t, "s1"), mustSpeakerID(t, "s1")),
	}))); got != "invalid recordingspeakers.merge.same_speaker into" {
		t.Errorf("self merge = %q", got)
	}
	// Unknown speaker is missing.
	if got := domaintest.Fails(t, recordingspeakers.Edit(roster, emptyStats, recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpUnmerge(mustSpeakerID(t, "s9")),
	}))); got != "missing recordingspeakers.speaker.not_found" {
		t.Errorf("missing speaker = %q", got)
	}
	// CheckEditKind accepts every valid kind and rejects the zero EditKind.
	for _, k := range []recordingspeakers.EditKind{
		recordingspeakers.EditKindName(),
		recordingspeakers.EditKindMerge(),
		recordingspeakers.EditKindUnmerge(),
		recordingspeakers.EditKindClear(),
	} {
		domaintest.Ok(t, recordingspeakers.CheckEditKind(k))
	}
	var zeroEditKind recordingspeakers.EditKind
	if got := domaintest.Fails(t, recordingspeakers.CheckEditKind(zeroEditKind)); got != "invalid recordingspeakers.op.unknown op" {
		t.Errorf("zero EditKind = %q", got)
	}
}

func TestAutoMergeWithChildrenAndOwnerMarkUndoRoundTrip(t *testing.T) {
	// s1 ("Ada", me=true, 300s), s2 ("Grace", 100s), s3 (merged_into s2, 50s), s4 (20s).
	// Rename s2 to "Ada": s2+s3 (150s) < s1 (300s), so s2 and s3 both merge into s1.
	var zeroEdits recordingspeakers.EditCounts
	roster := domaintest.Ok(t, recordingspeakers.NewRoster(
		domaintest.Ok(t, recordingspeakers.ParseSpeakerNumber(5)),
		[]recordingspeakers.Person{
			mustPerson(t, "s1", "Ada", true, "", false),
			mustPerson(t, "s2", "Grace", false, "", false),
			mustPerson(t, "s3", "", false, "s2", false),
			mustPerson(t, "s4", "", false, "", false),
		},
		zeroEdits,
	))
	stats := domaintest.Ok(t, recordingspeakers.NewStats([]recordingspeakers.SpeakerStat{
		domaintest.Ok(t, recordingspeakers.NewSpeakerStat(mustSpeakerID(t, "s1"), mustSeconds(t, 300), domaintest.Ok(t, recordingspeakers.ParseLineCount(20)))),
		domaintest.Ok(t, recordingspeakers.NewSpeakerStat(mustSpeakerID(t, "s2"), mustSeconds(t, 100), domaintest.Ok(t, recordingspeakers.ParseLineCount(8)))),
		domaintest.Ok(t, recordingspeakers.NewSpeakerStat(mustSpeakerID(t, "s3"), mustSeconds(t, 50), domaintest.Ok(t, recordingspeakers.ParseLineCount(4)))),
		domaintest.Ok(t, recordingspeakers.NewSpeakerStat(mustSpeakerID(t, "s4"), mustSeconds(t, 20), domaintest.Ok(t, recordingspeakers.ParseLineCount(2)))),
	}))
	ops := recordingspeakers.NewEditOps([]recordingspeakers.EditOp{
		recordingspeakers.EditOpName(mustSpeakerID(t, "s2"), opt.Some(mustName(t, "Ada")), recordingspeakers.OwnerMarkOther()),
	})
	var ed editedSpy
	domaintest.Ok(t, recordingspeakers.Edit(roster, stats, ops)).Accept(&ed)
	if ed.undo.IsEmpty() {
		t.Fatalf("expected non-empty undo")
	}
	var undone editedSpy
	domaintest.Ok(t, recordingspeakers.Edit(ed.roster, stats, ed.undo)).Accept(&undone)

	var beforeRP, afterRP rosterPeopleSpy
	roster.Accept(&beforeRP)
	undone.roster.Accept(&afterRP)
	if len(beforeRP.people) != len(afterRP.people) {
		t.Fatalf("people count changed across undo: %d vs %d", len(beforeRP.people), len(afterRP.people))
	}
	for i := range beforeRP.people {
		if beforeRP.people[i] != afterRP.people[i] {
			t.Errorf("person[%d] mismatch after undo round-trip", i)
		}
	}
}

func TestNumberAndPairEmptyAndOverlappingOwnTurns(t *testing.T) {
	// Empty numbering yields next=1 and no people.
	emptyRes := recordingspeakers.Number(recordingspeakers.NewRawTurns(nil))
	var emptySpy numberedSpy
	emptyRes.Accept(&emptySpy)
	if len(emptySpy.people) != 0 {
		t.Fatalf("expected empty people from empty numbering, got %d", len(emptySpy.people))
	}

	// Overlapping turns of the same cluster are unioned before pairing math.
	prev := recordingspeakers.NewTurns([]recordingspeakers.Turn{
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 0, 10), mustSpeakerID(t, "s1"))),
		domaintest.Ok(t, recordingspeakers.NewTurn(mustSpan(t, 5, 15), mustSpeakerID(t, "s1"))), // union is [0, 15] = 15s
	})
	nextRaw := recordingspeakers.NewRawTurns([]recordingspeakers.RawTurn{
		domaintest.Ok(t, recordingspeakers.NewRawTurn(mustSpan(t, 0, 8), mustClusterID(t, "c1"))), // 8s overlap >= 7.5s (50% of 15s)
	})
	var emptyRoster recordingspeakers.Roster
	paired := recordingspeakers.Pair(prev, emptyRoster, nextRaw)
	var pr numberedSpy
	paired.Accept(&pr)
	if len(pr.people) != 1 || pr.people[0].ID() != mustSpeakerID(t, "s1") {
		t.Fatalf("expected c1 to pair with s1 via unioned 15s duration")
	}
}

type numberedSpy struct {
	turns  recordingspeakers.Turns
	people []recordingspeakers.Person
}

func (s *numberedSpy) VisitNumbered(turns recordingspeakers.Turns, roster recordingspeakers.Roster) {
	s.turns = turns
	var rp rosterPeopleSpy
	roster.Accept(&rp)
	s.people = rp.people
}
