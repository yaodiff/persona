package recordingspeakersadapt

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingspeakers"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// maxSpeakerSamples is the maximum number of longest sure-line start times returned per unmerged speaker in
// detail.speakers.people[].samples.
const maxSpeakerSamples = 3

// ImportTimeline numbers (first timeline) or pairs (later timeline) incoming engine turns against existing meeting
// state, computes per-original-id stats, updates minor marks, and evaluates meeting separation quality.
func ImportTimeline(in recordingwire.SpeakerImportInput) (recordingwire.SpeakerImportOutput, error) {
	rawTurns, err := parseWireRawTurns(in.RawTurns)
	if err != nil {
		return recordingwire.SpeakerImportOutput{}, err
	}
	covered, err := parseWireCovered(in.Covered)
	if err != nil {
		return recordingwire.SpeakerImportOutput{}, err
	}
	segs := parseWireSegments(in.Segments)
	timing := parseTimingWord(string(in.Timing.Effective()))
	attendees, f := outcome.Unpack(recordingspeakers.ParseAttendeeCount(max(0, in.Attendees)))
	if f != nil {
		return recordingwire.SpeakerImportOutput{}, wrapBadSpeakers(f)
	}
	durSec, f := outcome.Unpack(recordingspeakers.ParseSeconds(sanitizeDuration(in.Duration)))
	if f != nil {
		return recordingwire.SpeakerImportOutput{}, wrapBadSpeakers(f)
	}

	var numbered recordingspeakers.Numbered
	if in.PrevSpeakers == nil && len(in.PrevTurns) == 0 {
		numbered = recordingspeakers.Number(rawTurns)
	} else {
		prevTurns, err := parseWireTurns(in.PrevTurns)
		if err != nil {
			return recordingwire.SpeakerImportOutput{}, err
		}
		var prevRoster recordingspeakers.Roster
		if in.PrevSpeakers != nil {
			prevRoster, err = restoreWireRoster(*in.PrevSpeakers)
			if err != nil {
				return recordingwire.SpeakerImportOutput{}, err
			}
		}
		numbered = recordingspeakers.Pair(prevTurns, prevRoster, rawTurns)
	}

	var numRec numberedCollector
	numbered.Accept(&numRec)

	stats := recordingspeakers.ComputeStats(numRec.turns, segs, timing, covered)
	roster := recordingspeakers.MarkMinor(stats, numRec.roster)

	env, f := outcome.Unpack(recordingspeakers.NewAlignEnv(numRec.turns, covered, timing))
	if f != nil {
		return recordingwire.SpeakerImportOutput{}, wrapBadSpeakers(f)
	}
	labels := recordingspeakers.AlignSegments(segs, env, roster, recordingspeakers.QualityOk())
	qIn, f := outcome.Unpack(recordingspeakers.NewQualityInputBuilder(segs, labels).
		Coverage(covered, durSec).
		Context(stats, roster, attendees).
		Build())
	if f != nil {
		return recordingwire.SpeakerImportOutput{}, wrapBadSpeakers(f)
	}
	verdict := recordingspeakers.EvaluateQuality(qIn)
	quality, qualityWhy := extractQualityVerdict(verdict)

	next, people, edits := extractWireRoster(roster)
	return recordingwire.SpeakerImportOutput{
		Turns:      extractWireTurns(numRec.turns),
		Covered:    extractWireCovered(covered),
		Next:       next,
		People:     people,
		Edits:      edits,
		Stats:      extractWireStats(stats),
		Quality:    quality,
		QualityWhy: qualityWhy,
	}, nil
}

// RecomputeAfterTranscriptChange trims turns and covered spans (when TrimTo > 0), then recomputes stats, minor marks
// and quality against the current transcript of record.
func RecomputeAfterTranscriptChange(in recordingwire.SpeakerRecomputeInput) (recordingwire.SpeakerRecomputeOutput, error) {
	turns, err := parseWireTurns(in.Turns)
	if err != nil {
		return recordingwire.SpeakerRecomputeOutput{}, err
	}
	covered, err := parseWireCovered(in.Covered)
	if err != nil {
		return recordingwire.SpeakerRecomputeOutput{}, err
	}
	roster, err := restoreWireRoster(in.Speakers)
	if err != nil {
		return recordingwire.SpeakerRecomputeOutput{}, err
	}
	if in.TrimTo > 0 {
		limit, f := outcome.Unpack(recordingspeakers.ParseSeconds(sanitizeDuration(in.TrimTo)))
		if f != nil {
			return recordingwire.SpeakerRecomputeOutput{}, wrapBadSpeakers(f)
		}
		turns = recordingspeakers.TrimTurns(turns, limit)
		covered = recordingspeakers.TrimCovered(covered, limit)
	}
	segs := parseWireSegments(in.Segments)
	timing := parseTimingWord(string(in.Timing.Effective()))
	attendees, f := outcome.Unpack(recordingspeakers.ParseAttendeeCount(max(0, in.Attendees)))
	if f != nil {
		return recordingwire.SpeakerRecomputeOutput{}, wrapBadSpeakers(f)
	}
	durSec, f := outcome.Unpack(recordingspeakers.ParseSeconds(sanitizeDuration(in.Duration)))
	if f != nil {
		return recordingwire.SpeakerRecomputeOutput{}, wrapBadSpeakers(f)
	}

	stats := recordingspeakers.ComputeStats(turns, segs, timing, covered)
	roster = recordingspeakers.MarkMinor(stats, roster)

	env, f := outcome.Unpack(recordingspeakers.NewAlignEnv(turns, covered, timing))
	if f != nil {
		return recordingwire.SpeakerRecomputeOutput{}, wrapBadSpeakers(f)
	}
	labels := recordingspeakers.AlignSegments(segs, env, roster, recordingspeakers.QualityOk())
	qIn, f := outcome.Unpack(recordingspeakers.NewQualityInputBuilder(segs, labels).
		Coverage(covered, durSec).
		Context(stats, roster, attendees).
		Build())
	if f != nil {
		return recordingwire.SpeakerRecomputeOutput{}, wrapBadSpeakers(f)
	}
	verdict := recordingspeakers.EvaluateQuality(qIn)
	quality, qualityWhy := extractQualityVerdict(verdict)

	_, people, _ := extractWireRoster(roster)
	return recordingwire.SpeakerRecomputeOutput{
		Turns:      extractWireTurns(turns),
		Covered:    extractWireCovered(covered),
		People:     people,
		Stats:      extractWireStats(stats),
		Quality:    quality,
		QualityWhy: qualityWhy,
	}, nil
}

// ApplyEdit applies a batch of speaker edit operations atomically through recordingspeakers.Edit and returns the
// updated roster fields and server-computed undo operations.
func ApplyEdit(in recordingwire.SpeakerEditInput) (recordingwire.SpeakerEditOutput, error) {
	roster, err := restoreWireRoster(in.Speakers)
	if err != nil {
		return recordingwire.SpeakerEditOutput{}, err
	}
	stats, err := parseWireStats(in.Speakers.Stats)
	if err != nil {
		return recordingwire.SpeakerEditOutput{}, err
	}
	rawOps := make([]editOpWire, len(in.Ops))
	hasClear := false
	for i, op := range in.Ops {
		if op.Op == recordingwire.SpeakerOpClear {
			hasClear = true
		}
		rawOps[i] = editOpWire{
			Op:   string(op.Op),
			ID:   string(op.ID),
			Name: op.Name,
			Me:   op.Me,
			From: string(op.From),
			Into: string(op.Into),
		}
	}
	domOps, f := outcome.Unpack(parseEditOps(rawOps))
	if f != nil {
		return recordingwire.SpeakerEditOutput{}, wrapBadSpeakers(f)
	}
	edited, ef := outcome.Unpack(recordingspeakers.Edit(roster, stats, domOps))
	if ef != nil {
		return recordingwire.SpeakerEditOutput{}, wrapBadSpeakers(ef)
	}
	var rec editedCollector
	edited.Accept(&rec)
	next, people, edits := extractWireRoster(rec.roster)
	undo := extractWireEditOps(rec.undo)
	return recordingwire.SpeakerEditOutput{
		Next:    next,
		People:  people,
		Edits:   edits,
		Undo:    undo,
		Cleared: hasClear,
	}, nil
}

// ProjectDetail computes detail.speakers (with merged stats, sample start times, spans and covered) and labels
// detail.segments using recordingspeakers alignment rules.
func ProjectDetail(in recordingwire.SpeakerDetailInput) (recordingwire.SpeakerDetailOutput, error) {
	outSegs := slices.Clone(in.Segments)
	candidates := slices.Clone(in.Candidates)
	if candidates == nil {
		candidates = []string{}
	}

	ds := &kbwire.DetailSpeakers{
		Rev:        in.Rev,
		State:      in.Speakers.State,
		Reason:     in.Speakers.Reason,
		Quality:    in.Speakers.Quality,
		People:     []kbwire.DetailSpeakerPerson{},
		Candidates: candidates,
		Spans:      []kbwire.SpeakerTurn{},
		Covered:    []kbwire.SpeakerSpan{},
		Trimmed:    in.Trimmed,
	}
	if in.Turns != nil && in.Turns.Source != "" {
		ds.Source = in.Turns.Source
	} else if in.Speakers.State == recordingwire.SpeakerStateReady || in.Speakers.State == recordingwire.SpeakerStateWaiting {
		ds.Source = recordingwire.SpeakerSourceHub
	}
	if in.Speakers.State == recordingwire.SpeakerStateWaiting {
		done := in.Speakers.Done
		end := in.EndSeconds
		ds.Done = &done
		ds.End = &end
	}
	if in.Speakers.State == recordingwire.SpeakerStateCleared {
		return recordingwire.SpeakerDetailOutput{Speakers: ds, Segments: outSegs}, nil
	}

	roster, err := restoreWireRoster(in.Speakers)
	if err != nil {
		return recordingwire.SpeakerDetailOutput{}, err
	}
	rawStats, err := parseWireStats(in.Speakers.Stats)
	if err != nil {
		return recordingwire.SpeakerDetailOutput{}, err
	}
	summed := extractWireStats(recordingspeakers.SumStats(rawStats, roster))

	var samplesBySpeaker map[kbwire.SpeakerID][]float64
	if !in.Trimmed && in.Speakers.State == recordingwire.SpeakerStateReady && in.Turns != nil {
		if len(in.Turns.Turns) > 0 {
			ds.Spans = slices.Clone(in.Turns.Turns)
		}
		if len(in.Turns.Covered) > 0 {
			ds.Covered = slices.Clone(in.Turns.Covered)
		}
		domTurns, err := parseWireTurns(in.Turns.Turns)
		if err != nil {
			return recordingwire.SpeakerDetailOutput{}, err
		}
		domCovered, err := parseWireCovered(in.Turns.Covered)
		if err != nil {
			return recordingwire.SpeakerDetailOutput{}, err
		}
		timing := parseTimingWord(string(in.Timing.Effective()))
		env, f := outcome.Unpack(recordingspeakers.NewAlignEnv(domTurns, domCovered, timing))
		if f != nil {
			return recordingwire.SpeakerDetailOutput{}, wrapBadSpeakers(f)
		}
		quality := parseQualityWord(string(in.Speakers.Quality))
		var zeroQuality recordingspeakers.Quality
		if quality == zeroQuality {
			quality = recordingspeakers.QualityOk()
		}
		domSegs := parseWireSegments(outSegs)
		labels := recordingspeakers.AlignSegments(domSegs, env, roster, quality)
		samplesBySpeaker = applyLabelsAndSamples(outSegs, labels)
	}

	ds.People = make([]kbwire.DetailSpeakerPerson, 0, len(in.Speakers.People))
	for _, p := range in.Speakers.People {
		dp := kbwire.DetailSpeakerPerson{
			ID:         p.ID,
			Name:       p.Name,
			Me:         p.Me,
			MergedInto: p.MergedInto,
			Minor:      p.Minor,
		}
		if p.MergedInto == "" {
			if st, ok := summed[p.ID]; ok {
				dp.Seconds = st.Seconds
				dp.Lines = st.Lines
			}
			if samples := samplesBySpeaker[p.ID]; len(samples) > 0 {
				dp.Samples = samples
			}
		}
		ds.People = append(ds.People, dp)
	}

	return recordingwire.SpeakerDetailOutput{Speakers: ds, Segments: outSegs}, nil
}

// AlignTranscript aligns transcript segments against the stored speaker timeline and roster for the minutes model.
func AlignTranscript(in recordingwire.SpeakerAlignInput) (recordingwire.SpeakerAlignOutput, error) {
	roster, err := restoreWireRoster(in.Speakers)
	if err != nil {
		return recordingwire.SpeakerAlignOutput{}, err
	}
	domTurns, err := parseWireTurns(in.Turns)
	if err != nil {
		return recordingwire.SpeakerAlignOutput{}, err
	}
	domCovered, err := parseWireCovered(in.Covered)
	if err != nil {
		return recordingwire.SpeakerAlignOutput{}, err
	}
	timing := parseTimingWord(string(in.Timing.Effective()))
	env, f := outcome.Unpack(recordingspeakers.NewAlignEnv(domTurns, domCovered, timing))
	if f != nil {
		return recordingwire.SpeakerAlignOutput{}, wrapBadSpeakers(f)
	}
	quality := parseQualityWord(string(in.Speakers.Quality))
	var zeroQuality recordingspeakers.Quality
	if quality == zeroQuality {
		quality = recordingspeakers.QualityOk()
	}
	domSegs := parseWireSegments(in.Segments)
	labels := recordingspeakers.AlignSegments(domSegs, env, roster, quality)
	var col lineLabelsCollector
	labels.Accept(&col)
	out := make([]recordingwire.AlignedSpeakerLine, len(in.Segments))
	for i := range out {
		if i >= len(col.items) {
			break
		}
		if col.items[i].assigned {
			out[i] = recordingwire.AlignedSpeakerLine{
				Speaker: col.items[i].speaker,
				Unsure:  col.items[i].unsure,
			}
		}
	}
	return recordingwire.SpeakerAlignOutput{Lines: out}, nil
}

// AttributeDebates computes speaker attribution on every disagreement position and its supporting `also` lines:
//   - when untrimmed (turns present): layer 1 overlap from stored span + turns, layer 2 from merges/minor/quality;
//   - when trimmed: layer 2 from stored over + current merges/minor/quality without reading a timeline or transcript;
//   - followed by all three cross-checks (model said_by mismatch, same person on two positions of the same debate,
//     and model who mismatching an owner-chosen name).
//
// Positions written without a span (Phase 1 minutes) are left untouched until regenerated.
func AttributeDebates(in recordingwire.SpeakerDebatesAttrInput) (recordingwire.SpeakerDebatesAttrOutput, error) {
	debates := cloneMinutesDebates(in.Debates)
	if len(debates) == 0 {
		return recordingwire.SpeakerDebatesAttrOutput{Debates: debates}, nil
	}
	roster, err := restoreWireRoster(in.Speakers)
	if err != nil {
		return recordingwire.SpeakerDebatesAttrOutput{}, err
	}
	quality := parseQualityWord(string(in.Speakers.Quality))
	var zeroQuality recordingspeakers.Quality
	if quality == zeroQuality {
		quality = recordingspeakers.QualityOk()
	}

	var domTurns recordingspeakers.Turns
	var domCovered recordingspeakers.Covered
	var domTiming recordingspeakers.Timing
	if !in.Trimmed {
		domTurns, err = parseWireTurns(in.Turns)
		if err != nil {
			return recordingwire.SpeakerDebatesAttrOutput{}, err
		}
		domCovered, err = parseWireCovered(in.Covered)
		if err != nil {
			return recordingwire.SpeakerDebatesAttrOutput{}, err
		}
		domTiming = parseTimingWord(string(in.Timing.Effective()))
	}

	canonByID, nameByCanon := buildRosterLookups(in.Speakers.People)
	canonOf := func(id recordingwire.SpeakerID) recordingwire.SpeakerID {
		if c, ok := canonByID[id]; ok {
			return c
		}
		return id
	}

	attributeCitation := func(span *recordingwire.SpeakerSpan, est *bool, over *map[recordingwire.SpeakerID]float64,
		saidBy recordingwire.SpeakerID, who string, speaker *recordingwire.SpeakerID, unsure *bool) {
		if span == nil {
			return // Phase 1 position without span: untouched until regenerated
		}
		var domOver recordingspeakers.Overlaps
		if !in.Trimmed {
			// Unreliable timing (timing=spread on hub lines) was captured in *est when minutes were built;
			// check covered holes against the current timeline for all non-spread-hub lines.
			if in.Timing.Effective() == recordingwire.TimingSpread && *est {
				*over = nil
				*speaker = ""
				*unsure = false
				return
			}
			domSpan, ok := parseWireSpan(*span)
			if !ok {
				*over = nil
				*speaker = ""
				*unsure = false
				return
			}
			seg, f := outcome.Unpack(recordingspeakers.NewSegment(domSpan, recordingspeakers.LineSourceDevice(),
				recordingspeakers.EchoSpeech(), recordingspeakers.ParseLineText("")))
			if f != nil || !recordingspeakers.CheckLine(seg, domTiming, domCovered).IsNone() {
				*est = true
				*over = nil
				*speaker = ""
				*unsure = false
				return
			}
			*est = false
			domOver = recordingspeakers.OverlapLine(domTurns, domSpan)
			*over = extractWireOverlaps(domOver)
		} else {
			if *est {
				*speaker = ""
				*unsure = false
				return
			}
			domOver = parseWireOverlaps(*over)
		}

		label := recordingspeakers.AlignOverlap(domOver, roster, quality)
		var col lineLabelCollector
		label.Accept(&col)
		if !col.item.assigned {
			*speaker = ""
			*unsure = false
			return
		}
		*speaker = col.item.speaker
		*unsure = col.item.unsure

		// Cross-check 1: model holder (said_by) disagrees with computed canonical speaker (skipped when absent).
		if saidBy != "" && canonOf(saidBy) != *speaker {
			*unsure = true
		}
		// Cross-check 3: model wrote who, computed speaker has an owner-chosen name, and the two differ.
		if who != "" {
			if ownerName := nameByCanon[*speaker]; ownerName != "" && !strings.EqualFold(strings.TrimSpace(who), strings.TrimSpace(ownerName)) {
				*unsure = true
			}
		}
	}

	for di := range debates {
		positions := debates[di].Positions
		for pi := range positions {
			pos := &positions[pi]
			attributeCitation(pos.Span, &pos.Est, &pos.Over, pos.SaidBy, pos.Who, &pos.Speaker, &pos.SpeakerUnsure)
			for ai := range pos.Also {
				al := &pos.Also[ai]
				attributeCitation(al.Span, &al.Est, &al.Over, al.SaidBy, "", &al.Speaker, &al.SpeakerUnsure)
			}
		}
		// Cross-check 2: the same canonical person appears on two or more distinct positions of the same debate (also included).
		posCount := map[recordingwire.SpeakerID]int{}
		for pi := range positions {
			seenInPos := map[recordingwire.SpeakerID]bool{}
			if id := positions[pi].Speaker; id != "" {
				seenInPos[id] = true
			}
			for _, al := range positions[pi].Also {
				if al.Speaker != "" {
					seenInPos[al.Speaker] = true
				}
			}
			for id := range seenInPos {
				posCount[id]++
			}
		}
		for pi := range positions {
			if id := positions[pi].Speaker; id != "" && posCount[id] >= 2 {
				positions[pi].SpeakerUnsure = true
			}
			for ai := range positions[pi].Also {
				if id := positions[pi].Also[ai].Speaker; id != "" && posCount[id] >= 2 {
					positions[pi].Also[ai].SpeakerUnsure = true
				}
			}
		}
	}
	return recordingwire.SpeakerDebatesAttrOutput{Debates: debates}, nil
}

// ResolveDebateDisplays applies recordingspeakers.ResolvePositionDisplay to one disagreement position so Markdown
// rendering uses the exact same four-row display-name rule and `also` deduplication as clients and fixtures.
func ResolveDebateDisplays(in recordingwire.SpeakerDisplayInput) []recordingwire.SpeakerDisplayLabel {
	var roster recordingspeakers.Roster
	if in.Speakers != nil {
		if r, err := restoreWireRoster(*in.Speakers); err == nil {
			roster = r
		}
	}
	mainClaim, fallbackWho := buildWireClaim(in.Position.Speaker, in.Position.SpeakerUnsure, in.Position.Who)
	var alsoClaims []recordingspeakers.Claim
	for _, al := range in.Position.Also {
		c, _ := buildWireClaim(al.Speaker, al.SpeakerUnsure, "")
		alsoClaims = append(alsoClaims, c)
	}
	displays := recordingspeakers.ResolvePositionDisplay(mainClaim, recordingspeakers.NewAlsoClaims(alsoClaims), roster)
	var col displayLabelsCollector
	displays.Accept(&col)
	if len(col.out) == 0 && fallbackWho != "" {
		return []recordingwire.SpeakerDisplayLabel{{Kind: recordingwire.SpeakerDisplayWho, Value: fallbackWho}}
	}
	return col.out
}

// --- unexported conversion helpers ---

type failMessageCollector struct {
	parts []string
}

func (c *failMessageCollector) VisitInvalid(code fail.Code, part fail.Part) {
	c.parts = append(c.parts, part.Wire()+": "+code.Wire())
}

func (c *failMessageCollector) VisitRefused(code fail.Code) {
	c.parts = append(c.parts, code.Wire())
}

func (c *failMessageCollector) VisitMissing(code fail.Code) {
	c.parts = append(c.parts, code.Wire())
}

func (c *failMessageCollector) VisitConflict(code fail.Code) {
	c.parts = append(c.parts, code.Wire())
}

func wrapBadSpeakers(f fail.Fail) error {
	var c failMessageCollector
	f.Accept(&c)
	return fmt.Errorf("%w: %s", recordingwire.ErrBadSpeakers, strings.Join(c.parts, "; "))
}

func sanitizeDuration(d float64) float64 {
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 {
		return 0
	}
	return d
}

func restoreWireRoster(s recordingwire.MeetingSpeakers) (recordingspeakers.Roster, error) {
	var zero recordingspeakers.Roster
	rec := Record{
		Next: s.Next,
		Edits: editsRecord{
			Name:    s.Edits.Name,
			Merge:   s.Edits.Merge,
			Unmerge: s.Edits.Unmerge,
			Clear:   s.Edits.Clear,
		},
	}
	if len(s.People) > 0 {
		rec.People = make([]personRecord, len(s.People))
		for i, p := range s.People {
			rec.People[i] = personRecord{
				ID:         string(p.ID),
				Name:       p.Name,
				Me:         p.Me,
				MergedInto: string(p.MergedInto),
				Minor:      p.Minor,
			}
		}
	}
	r, f := outcome.Unpack(Restore(rec))
	if f != nil {
		return zero, wrapBadSpeakers(f)
	}
	return r, nil
}

func extractWireRoster(roster recordingspeakers.Roster) (int, []recordingwire.SpeakerPerson, recordingwire.SpeakerEdits) {
	rec := Store(roster)
	people := make([]recordingwire.SpeakerPerson, 0, len(rec.People))
	for _, p := range rec.People {
		people = append(people, recordingwire.SpeakerPerson{
			ID:         recordingwire.SpeakerID(p.ID),
			Name:       p.Name,
			Me:         p.Me,
			MergedInto: recordingwire.SpeakerID(p.MergedInto),
			Minor:      p.Minor,
		})
	}
	edits := recordingwire.SpeakerEdits{
		Name:    rec.Edits.Name,
		Merge:   rec.Edits.Merge,
		Unmerge: rec.Edits.Unmerge,
		Clear:   rec.Edits.Clear,
	}
	return rec.Next, people, edits
}

func parseWireRawTurns(in []recordingwire.RawSpeakerTurn) (recordingspeakers.RawTurns, error) {
	var zero recordingspeakers.RawTurns
	list := make([]recordingspeakers.RawTurn, 0, len(in))
	for _, rt := range in {
		s, f := outcome.Unpack(recordingspeakers.ParseSeconds(rt.Start))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		e, f := outcome.Unpack(recordingspeakers.ParseSeconds(rt.End))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		sp, f := outcome.Unpack(recordingspeakers.NewSpan(s, e))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		cid, f := outcome.Unpack(recordingspeakers.ParseClusterID(string(rt.Cluster)))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		turn, f := outcome.Unpack(recordingspeakers.NewRawTurn(sp, cid))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		list = append(list, turn)
	}
	return recordingspeakers.NewRawTurns(list), nil
}

func parseWireTurns(in []recordingwire.SpeakerTurn) (recordingspeakers.Turns, error) {
	var zero recordingspeakers.Turns
	list := make([]recordingspeakers.Turn, 0, len(in))
	for _, t := range in {
		s, f := outcome.Unpack(recordingspeakers.ParseSeconds(t.Start))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		e, f := outcome.Unpack(recordingspeakers.ParseSeconds(t.End))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		sp, f := outcome.Unpack(recordingspeakers.NewSpan(s, e))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		id, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(string(t.Speaker)))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		turn, f := outcome.Unpack(recordingspeakers.NewTurn(sp, id))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		list = append(list, turn)
	}
	return recordingspeakers.NewTurns(list), nil
}

func parseWireCovered(in []recordingwire.SpeakerSpan) (recordingspeakers.Covered, error) {
	var zero recordingspeakers.Covered
	spans := make([]recordingspeakers.Span, 0, len(in))
	for _, c := range in {
		s, f := outcome.Unpack(recordingspeakers.ParseSeconds(c[0]))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		e, f := outcome.Unpack(recordingspeakers.ParseSeconds(c[1]))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		sp, f := outcome.Unpack(recordingspeakers.NewSpan(s, e))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		spans = append(spans, sp)
	}
	cov, f := outcome.Unpack(recordingspeakers.NewCovered(spans))
	if f != nil {
		return zero, wrapBadSpeakers(f)
	}
	return cov, nil
}

func parseWireSegments(in []kbwire.RecordingSegment) recordingspeakers.Segments {
	list := make([]recordingspeakers.Segment, 0, len(in))
	for _, sg := range in {
		st := sanitizeDuration(sg.Start)
		en := sanitizeDuration(sg.End)
		if en < st {
			en = st
		}
		s, fs := outcome.Unpack(recordingspeakers.ParseSeconds(st))
		if fs != nil {
			continue
		}
		e, fe := outcome.Unpack(recordingspeakers.ParseSeconds(en))
		if fe != nil {
			continue
		}
		sp, fsp := outcome.Unpack(recordingspeakers.NewSpan(s, e))
		if fsp != nil {
			continue
		}
		src := recordingspeakers.LineSourceDevice()
		if strings.EqualFold(sg.Source, recordingwire.SourceHub) {
			src = recordingspeakers.LineSourceHub()
		}
		echo := recordingspeakers.EchoSpeech()
		if sg.Echo {
			echo = recordingspeakers.EchoPersona()
		}
		seg, fseg := outcome.Unpack(recordingspeakers.NewSegment(sp, src, echo, recordingspeakers.ParseLineText(sg.Text)))
		if fseg != nil {
			continue
		}
		list = append(list, seg)
	}
	return recordingspeakers.NewSegments(list)
}

func parseWireStats(in map[recordingwire.SpeakerID]recordingwire.SpeakerStat) (recordingspeakers.Stats, error) {
	var zero recordingspeakers.Stats
	if len(in) == 0 {
		return zero, nil
	}
	list := make([]recordingspeakers.SpeakerStat, 0, len(in))
	for id, st := range in {
		sid, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(string(id)))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		sec, f := outcome.Unpack(recordingspeakers.ParseSeconds(st.Seconds))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		lc, f := outcome.Unpack(recordingspeakers.ParseLineCount(st.Lines))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		item, f := outcome.Unpack(recordingspeakers.NewSpeakerStat(sid, sec, lc))
		if f != nil {
			return zero, wrapBadSpeakers(f)
		}
		list = append(list, item)
	}
	stats, f := outcome.Unpack(recordingspeakers.NewStats(list))
	if f != nil {
		return zero, wrapBadSpeakers(f)
	}
	return stats, nil
}

func extractWireTurns(turns recordingspeakers.Turns) []recordingwire.SpeakerTurn {
	var rec turnsCollector
	turns.Accept(&rec)
	if rec.out == nil {
		return []recordingwire.SpeakerTurn{}
	}
	return rec.out
}

func extractWireCovered(covered recordingspeakers.Covered) []recordingwire.SpeakerSpan {
	var rec coveredCollector
	covered.Accept(&rec)
	if rec.out == nil {
		return []recordingwire.SpeakerSpan{}
	}
	return rec.out
}

func extractWireStats(stats recordingspeakers.Stats) map[recordingwire.SpeakerID]recordingwire.SpeakerStat {
	rec := statsCollector{out: map[recordingwire.SpeakerID]recordingwire.SpeakerStat{}}
	stats.Accept(&rec)
	return rec.out
}

func extractQualityVerdict(verdict recordingspeakers.QualityVerdict) (recordingwire.SpeakerQuality, recordingwire.SpeakerQualityReason) {
	var rec qualityVerdictCollector
	verdict.Accept(&rec)
	return rec.quality, rec.reason
}

func extractWireEditOps(ops recordingspeakers.EditOps) []recordingwire.SpeakerEditOp {
	var rec editOpsRecorder
	ops.Accept(&rec)
	out := make([]recordingwire.SpeakerEditOp, 0, len(rec.ops))
	for _, op := range rec.ops {
		out = append(out, recordingwire.SpeakerEditOp{
			Op:   recordingwire.SpeakerEditOpKind(op.Op),
			ID:   recordingwire.SpeakerID(op.ID),
			Name: op.Name,
			Me:   op.Me,
			From: recordingwire.SpeakerID(op.From),
			Into: recordingwire.SpeakerID(op.Into),
		})
	}
	return out
}

type sampleCandidate struct {
	start float64
	dur   float64
}

func applyLabelsAndSamples(segs []kbwire.RecordingSegment, labels recordingspeakers.LineLabels) map[kbwire.SpeakerID][]float64 {
	var col lineLabelsCollector
	labels.Accept(&col)
	bySpeaker := map[kbwire.SpeakerID][]sampleCandidate{}
	for i := range segs {
		if i >= len(col.items) {
			break
		}
		item := col.items[i]
		if !item.assigned {
			segs[i].Speaker = ""
			segs[i].SpeakerUnsure = false
			continue
		}
		segs[i].Speaker = item.speaker
		segs[i].SpeakerUnsure = item.unsure
		if !item.unsure {
			dur := segs[i].End - segs[i].Start
			if dur < 0 {
				dur = 0
			}
			bySpeaker[item.speaker] = append(bySpeaker[item.speaker], sampleCandidate{start: segs[i].Start, dur: dur})
		}
	}
	samples := make(map[kbwire.SpeakerID][]float64, len(bySpeaker))
	for id, cands := range bySpeaker {
		slices.SortFunc(cands, func(a, b sampleCandidate) int {
			if c := cmp.Compare(b.dur, a.dur); c != 0 {
				return c
			}
			return cmp.Compare(a.start, b.start)
		})
		if len(cands) > maxSpeakerSamples {
			cands = cands[:maxSpeakerSamples]
		}
		starts := make([]float64, len(cands))
		for i, c := range cands {
			starts[i] = c.start
		}
		slices.Sort(starts)
		samples[id] = starts
	}
	return samples
}

// --- visitors ---

type numberedCollector struct {
	turns  recordingspeakers.Turns
	roster recordingspeakers.Roster
}

func (n *numberedCollector) VisitNumbered(turns recordingspeakers.Turns, roster recordingspeakers.Roster) {
	n.turns, n.roster = turns, roster
}

type editedCollector struct {
	roster recordingspeakers.Roster
	undo   recordingspeakers.EditOps
}

func (e *editedCollector) VisitEdited(roster recordingspeakers.Roster, undo recordingspeakers.EditOps) {
	e.roster, e.undo = roster, undo
}

type spanCollector struct{ start, end float64 }

func (s *spanCollector) VisitSpan(start, end recordingspeakers.Seconds) {
	s.start, s.end = start.Wire(), end.Wire()
}

type turnCollector struct {
	span spanCollector
	out  recordingwire.SpeakerTurn
}

func (t *turnCollector) VisitTurn(span recordingspeakers.Span, speaker recordingspeakers.SpeakerID) {
	span.Accept(&t.span)
	t.out = recordingwire.SpeakerTurn{
		Start:   t.span.start,
		End:     t.span.end,
		Speaker: recordingwire.SpeakerID(speaker.Wire()),
	}
}

type turnsCollector struct {
	turn turnCollector
	out  []recordingwire.SpeakerTurn
}

func (t *turnsCollector) VisitTurn(turn recordingspeakers.Turn) {
	turn.Accept(&t.turn)
	t.out = append(t.out, t.turn.out)
}

type coveredCollector struct {
	span spanCollector
	out  []recordingwire.SpeakerSpan
}

func (c *coveredCollector) VisitSpan(span recordingspeakers.Span) {
	span.Accept(&c.span)
	c.out = append(c.out, recordingwire.SpeakerSpan{c.span.start, c.span.end})
}

type statCollector struct {
	id   recordingwire.SpeakerID
	stat recordingwire.SpeakerStat
}

func (s *statCollector) VisitSpeakerStat(speaker recordingspeakers.SpeakerID, seconds recordingspeakers.Seconds, lines recordingspeakers.LineCount) {
	s.id = recordingwire.SpeakerID(speaker.Wire())
	s.stat = recordingwire.SpeakerStat{Seconds: seconds.Wire(), Lines: lines.Wire()}
}

type statsCollector struct {
	item statCollector
	out  map[recordingwire.SpeakerID]recordingwire.SpeakerStat
}

func (s *statsCollector) VisitSpeakerStat(stat recordingspeakers.SpeakerStat) {
	stat.Accept(&s.item)
	s.out[s.item.id] = s.item.stat
}

type qualityVerdictCollector struct {
	qw      qualityWord
	rw      qualityReasonWord
	quality recordingwire.SpeakerQuality
	reason  recordingwire.SpeakerQualityReason
}

func (q *qualityVerdictCollector) VisitQualityVerdict(quality recordingspeakers.Quality, reason opt.Option[recordingspeakers.QualityReason]) {
	q.qw.word = ""
	quality.Accept(&q.qw)
	q.quality = recordingwire.SpeakerQuality(q.qw.word)
	q.reason = ""
	reason.Match(func(r recordingspeakers.QualityReason) {
		q.rw.word = ""
		r.Accept(&q.rw)
		q.reason = recordingwire.SpeakerQualityReason(q.rw.word)
	}, func() {})
}

type lineLabelItem struct {
	assigned bool
	speaker  kbwire.SpeakerID
	unsure   bool
}

type lineLabelCollector struct {
	cert certaintyBool
	item lineLabelItem
}

func (l *lineLabelCollector) VisitAssigned(speaker recordingspeakers.SpeakerID, certainty recordingspeakers.Certainty) {
	certainty.Accept(&l.cert)
	l.item = lineLabelItem{
		assigned: true,
		speaker:  kbwire.SpeakerID(speaker.Wire()),
		unsure:   l.cert.unsure,
	}
}

func (l *lineLabelCollector) VisitSkipped(recordingspeakers.SkipReason) {
	l.item = lineLabelItem{}
}

type lineLabelsCollector struct {
	one   lineLabelCollector
	items []lineLabelItem
}

func (l *lineLabelsCollector) VisitLineLabel(label recordingspeakers.LineLabel) {
	l.one.item = lineLabelItem{}
	label.Accept(&l.one)
	l.items = append(l.items, l.one.item)
}

func cloneMinutesDebates(in []recordingwire.MinutesDebate) []recordingwire.MinutesDebate {
	if len(in) == 0 {
		return nil
	}
	out := make([]recordingwire.MinutesDebate, len(in))
	for i, d := range in {
		cp := d
		if d.Outcome != nil {
			o := *d.Outcome
			if d.Outcome.Decision != nil {
				idx := *d.Outcome.Decision
				o.Decision = &idx
			}
			cp.Outcome = &o
		}
		if len(d.Positions) > 0 {
			cp.Positions = make([]recordingwire.MinutesDebatePosition, len(d.Positions))
			for j, p := range d.Positions {
				pp := p
				if p.Span != nil {
					sp := *p.Span
					pp.Span = &sp
				}
				if p.Over != nil {
					pp.Over = make(map[recordingwire.SpeakerID]float64, len(p.Over))
					for k, v := range p.Over {
						pp.Over[k] = v
					}
				}
				if len(p.Also) > 0 {
					pp.Also = make([]recordingwire.MinutesDebateAlso, len(p.Also))
					for k, al := range p.Also {
						ap := al
						if al.Span != nil {
							sp := *al.Span
							ap.Span = &sp
						}
						if al.Over != nil {
							ap.Over = make(map[recordingwire.SpeakerID]float64, len(al.Over))
							for oid, v := range al.Over {
								ap.Over[oid] = v
							}
						}
						pp.Also[k] = ap
					}
				}
				cp.Positions[j] = pp
			}
		}
		out[i] = cp
	}
	return out
}

func buildRosterLookups(people []recordingwire.SpeakerPerson) (map[recordingwire.SpeakerID]recordingwire.SpeakerID, map[recordingwire.SpeakerID]string) {
	byID := make(map[recordingwire.SpeakerID]recordingwire.SpeakerPerson, len(people))
	for _, p := range people {
		byID[p.ID] = p
	}
	canon := make(map[recordingwire.SpeakerID]recordingwire.SpeakerID, len(people))
	names := make(map[recordingwire.SpeakerID]string, len(people))
	for _, p := range people {
		cur := p.ID
		for range len(people) + 1 {
			entry, ok := byID[cur]
			if !ok || entry.MergedInto == "" || entry.MergedInto == cur {
				break
			}
			cur = entry.MergedInto
		}
		canon[p.ID] = cur
		if p.MergedInto == "" && p.Name != "" {
			names[p.ID] = p.Name
		}
	}
	return canon, names
}

func parseWireSpan(sp recordingwire.SpeakerSpan) (recordingspeakers.Span, bool) {
	var zero recordingspeakers.Span
	st := sanitizeDuration(sp[0])
	en := sanitizeDuration(sp[1])
	if en < st {
		en = st
	}
	s, fs := outcome.Unpack(recordingspeakers.ParseSeconds(st))
	if fs != nil {
		return zero, false
	}
	e, fe := outcome.Unpack(recordingspeakers.ParseSeconds(en))
	if fe != nil {
		return zero, false
	}
	out, f := outcome.Unpack(recordingspeakers.NewSpan(s, e))
	if f != nil {
		return zero, false
	}
	return out, true
}

func parseWireOverlaps(in map[recordingwire.SpeakerID]float64) recordingspeakers.Overlaps {
	var zero recordingspeakers.Overlaps
	if len(in) == 0 {
		return zero
	}
	list := make([]recordingspeakers.Overlap, 0, len(in))
	for id, sec := range in {
		if math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 {
			continue
		}
		sid, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(string(id)))
		if f != nil {
			continue
		}
		s, f := outcome.Unpack(recordingspeakers.ParseSeconds(sec))
		if f != nil {
			continue
		}
		ov, f := outcome.Unpack(recordingspeakers.NewOverlap(sid, s))
		if f != nil {
			continue
		}
		list = append(list, ov)
	}
	if len(list) == 0 {
		return zero
	}
	overs, f := outcome.Unpack(recordingspeakers.NewOverlaps(list))
	if f != nil {
		return zero
	}
	return overs
}

func extractWireOverlaps(over recordingspeakers.Overlaps) map[recordingwire.SpeakerID]float64 {
	var col wireOverlapsCollector
	over.Accept(&col)
	if len(col.out) == 0 {
		return nil
	}
	return col.out
}

type wireOverlapItemCollector struct {
	id  recordingwire.SpeakerID
	sec float64
}

func (c *wireOverlapItemCollector) VisitOverlap(speaker recordingspeakers.SpeakerID, seconds recordingspeakers.Seconds) {
	c.id = recordingwire.SpeakerID(speaker.Wire())
	c.sec = seconds.Wire()
}

type wireOverlapsCollector struct {
	item wireOverlapItemCollector
	out  map[recordingwire.SpeakerID]float64
}

func (c *wireOverlapsCollector) VisitOverlap(o recordingspeakers.Overlap) {
	o.Accept(&c.item)
	if c.out == nil {
		c.out = map[recordingwire.SpeakerID]float64{}
	}
	c.out[c.item.id] = c.item.sec
}

func buildWireClaim(speaker recordingwire.SpeakerID, unsure bool, who string) (recordingspeakers.Claim, string) {
	spk := opt.None[recordingspeakers.SpeakerID]()
	cert := recordingspeakers.CertaintyUnsure()
	if speaker != "" {
		if sid, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(string(speaker))); f == nil {
			spk = opt.Some(sid)
			if !unsure {
				cert = recordingspeakers.CertaintySure()
			}
		}
	}
	whoOpt := opt.None[recordingspeakers.SpeakerName]()
	fallbackWho := ""
	if trimmed := strings.TrimSpace(who); trimmed != "" {
		if wn, f := outcome.Unpack(recordingspeakers.ParseSpeakerName(trimmed)); f == nil {
			whoOpt = opt.Some(wn)
		} else {
			fallbackWho = trimmed
		}
	}
	c, f := outcome.Unpack(recordingspeakers.NewClaim(spk, cert, whoOpt))
	if f != nil {
		var zero recordingspeakers.Claim
		return zero, fallbackWho
	}
	return c, fallbackWho
}

type displayLabelCollector struct {
	out   recordingwire.SpeakerDisplayLabel
	valid bool
}

func (c *displayLabelCollector) VisitNamed(name recordingspeakers.SpeakerName) {
	c.out = recordingwire.SpeakerDisplayLabel{Kind: recordingwire.SpeakerDisplayName, Value: name.Wire()}
	c.valid = true
}

func (c *displayLabelCollector) VisitNumbered(num recordingspeakers.SpeakerNumber) {
	c.out = recordingwire.SpeakerDisplayLabel{Kind: recordingwire.SpeakerDisplayNumbered, Number: num.Wire()}
	c.valid = true
}

func (c *displayLabelCollector) VisitWho(who recordingspeakers.SpeakerName) {
	c.out = recordingwire.SpeakerDisplayLabel{Kind: recordingwire.SpeakerDisplayWho, Value: who.Wire()}
	c.valid = true
}

func (c *displayLabelCollector) VisitNone() {
	c.valid = false
}

type displayLabelsCollector struct {
	item displayLabelCollector
	out  []recordingwire.SpeakerDisplayLabel
}

func (c *displayLabelsCollector) VisitDisplay(d recordingspeakers.Display) {
	c.item = displayLabelCollector{}
	d.Accept(&c.item)
	if c.item.valid {
		c.out = append(c.out, c.item.out)
	}
}
