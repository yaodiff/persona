package recordingspeakers

import (
	"cmp"
	"maps"
	"math"
	"slices"

	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
)

const (
	// minorThresholdSeconds is the speaking duration below which an unmerged speaker is marked minor.
	minorThresholdSeconds = 20.0
	// sureMinShare is the minimum fraction of total line overlap (strictly greater than) required to be sure.
	sureMinShare = 0.70
	// sureMinSeconds is the minimum overlap in seconds required for a sure speaker assignment.
	sureMinSeconds = 1.0
	// pairMinShare is the minimum fraction of either the new cluster's or the old speaker's speaking time required to
	// pair a later timeline's cluster to an existing speaker id.
	pairMinShare = 0.50
	// coveredMinLineShare is the minimum covered fraction of a line's duration to not count as being in a hole.
	coveredMinLineShare = 0.50
)

// mergeSpans sorts and merges overlapping or touching spans with positive duration.
func mergeSpans(in []Span) []Span {
	if len(in) == 0 {
		return nil
	}
	cp := make([]Span, 0, len(in))
	for _, iv := range in {
		if iv.end.seconds > iv.start.seconds {
			cp = append(cp, iv)
		}
	}
	if len(cp) == 0 {
		return nil
	}
	slices.SortFunc(cp, func(a, b Span) int {
		if c := cmp.Compare(a.start.seconds, b.start.seconds); c != 0 {
			return c
		}
		return cmp.Compare(a.end.seconds, b.end.seconds)
	})
	out := []Span{cp[0]}
	for _, iv := range cp[1:] {
		last := &out[len(out)-1]
		if iv.start.seconds <= last.end.seconds {
			if iv.end.seconds > last.end.seconds {
				last.end = iv.end
			}
		} else {
			out = append(out, iv)
		}
	}
	return out
}

// spansDuration is the total duration of merged disjoint spans.
func spansDuration(merged []Span) float64 {
	total := 0.0
	for _, iv := range merged {
		total += iv.end.seconds - iv.start.seconds
	}
	return total
}

// spansOverlap is the total overlap duration between two sorted, disjoint span slices.
func spansOverlap(a, b []Span) float64 {
	total := 0.0
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		lo := math.Max(a[i].start.seconds, b[j].start.seconds)
		hi := math.Min(a[i].end.seconds, b[j].end.seconds)
		if hi > lo {
			total += hi - lo
		}
		if a[i].end.seconds < b[j].end.seconds {
			i++
		} else {
			j++
		}
	}
	return total
}

// coveredSpans merges covered spans into sorted disjoint spans.
func coveredSpans(c Covered) []Span {
	return mergeSpans(c.covered)
}

// pointCovered says whether point t falls inside any span of merged (endpoints inclusive).
func pointCovered(t float64, merged []Span) bool {
	for _, iv := range merged {
		if t+floatEps >= iv.start.seconds && t <= iv.end.seconds+floatEps {
			return true
		}
	}
	return false
}

// lineInHole says whether span lies in a hole of covered.
func lineInHole(span Span, mergedCovered []Span) bool {
	if len(mergedCovered) == 0 {
		return true
	}
	dur := span.duration()
	if dur <= 0 {
		return !pointCovered(span.start.seconds, mergedCovered)
	}
	ov := spansOverlap([]Span{span}, mergedCovered)
	return ov+floatEps < dur*coveredMinLineShare
}

// Number assigns stable hub speaker ids ("s1", "s2", ...) to a first timeline's engine cluster labels in order of first
// speech, returning the numbered turns and the initial speaker roster with next set past the last assigned id.
func Number(raw RawTurns) Numbered {
	return Pair(Turns{}, Roster{}, raw)
}

// Pair maps a later timeline's engine cluster labels onto an existing meeting's speaker ids by one-to-one overlap
// pairing (largest overlap first, each old id used at most once, requiring overlap of at least half of the new cluster's
// or of the old speaker's speaking time). Unpaired clusters receive new ids from roster.next in order of first speech,
// and existing names and merges follow their ids.
func Pair(prev Turns, roster Roster, next RawTurns) Numbered {
	// Build merged spans per old speaker id.
	oldRaw := make(map[SpeakerID][]Span)
	for _, t := range prev.turns {
		oldRaw[t.speaker] = append(oldRaw[t.speaker], t.span)
	}
	oldMerged := make(map[SpeakerID][]Span, len(oldRaw))
	oldDur := make(map[SpeakerID]float64, len(oldRaw))
	for _, id := range slices.SortedFunc(maps.Keys(oldRaw), compareSpeakerID) {
		m := mergeSpans(oldRaw[id])
		oldMerged[id] = m
		oldDur[id] = spansDuration(m)
	}

	// Build merged spans and earliest start time per new cluster id.
	newRaw := make(map[ClusterID][]Span)
	firstStart := make(map[ClusterID]float64)
	for _, t := range next.rawTurns {
		if _, ok := firstStart[t.cluster]; !ok || t.span.start.seconds < firstStart[t.cluster] {
			firstStart[t.cluster] = t.span.start.seconds
		}
		newRaw[t.cluster] = append(newRaw[t.cluster], t.span)
	}
	clusters := slices.SortedFunc(maps.Keys(newRaw), func(a, b ClusterID) int {
		if c := cmp.Compare(firstStart[a], firstStart[b]); c != 0 {
			return c
		}
		return cmp.Compare(a.cluster, b.cluster)
	})
	newMerged := make(map[ClusterID][]Span, len(clusters))
	newDur := make(map[ClusterID]float64, len(clusters))
	for _, cid := range clusters {
		m := mergeSpans(newRaw[cid])
		newMerged[cid] = m
		newDur[cid] = spansDuration(m)
	}

	// Build all eligible candidate pairs.
	type pairCandidate struct {
		cluster ClusterID
		oldID   SpeakerID
		overlap Seconds
	}
	var candidates []pairCandidate
	oldIDs := slices.SortedFunc(maps.Keys(oldMerged), compareSpeakerID)
	for _, cid := range clusters {
		nd := newDur[cid]
		if nd <= 0 {
			continue
		}
		for _, oid := range oldIDs {
			od := oldDur[oid]
			if od <= 0 {
				continue
			}
			ov := spansOverlap(newMerged[cid], oldMerged[oid])
			if ov <= 0 {
				continue
			}
			if ov+floatEps >= pairMinShare*nd || ov+floatEps >= pairMinShare*od {
				candidates = append(candidates, pairCandidate{cluster: cid, oldID: oid, overlap: Seconds{seconds: ov}})
			}
		}
	}
	slices.SortFunc(candidates, func(a, b pairCandidate) int {
		if math.Abs(a.overlap.seconds-b.overlap.seconds) > floatEps {
			if a.overlap.seconds > b.overlap.seconds {
				return -1
			}
			return 1
		}
		if c := compareSpeakerID(a.oldID, b.oldID); c != 0 {
			return c
		}
		if c := cmp.Compare(firstStart[a.cluster], firstStart[b.cluster]); c != 0 {
			return c
		}
		return cmp.Compare(a.cluster.cluster, b.cluster.cluster)
	})

	// Greedy 1-to-1 matching.
	assigned := make(map[ClusterID]SpeakerID, len(clusters))
	usedOld := make(map[SpeakerID]bool, len(oldIDs))
	for _, cand := range candidates {
		if _, ok := assigned[cand.cluster]; ok || usedOld[cand.oldID] {
			continue
		}
		assigned[cand.cluster] = cand.oldID
		usedOld[cand.oldID] = true
	}

	// Determine next available speaker number.
	nextNum := 1
	if roster.next.speakerNumber > nextNum {
		nextNum = roster.next.speakerNumber
	}
	for _, p := range roster.people {
		if n := p.id.num("recordingspeakers.Pair"); n >= nextNum {
			nextNum = n + 1
		}
	}
	for _, oid := range oldIDs {
		if n := oid.num("recordingspeakers.Pair"); n >= nextNum {
			nextNum = n + 1
		}
	}

	// Assign new ids to unpaired clusters in order of first speech.
	people := slices.Clone(roster.people)
	inPeople := make(map[SpeakerID]bool, len(people))
	for _, p := range people {
		inPeople[p.id] = true
	}
	for _, cid := range clusters {
		id, matched := assigned[cid]
		if !matched {
			id = speakerIDFromNumber(nextNum)
			nextNum++
			assigned[cid] = id
		}
		if !inPeople[id] {
			people = append(people, Person{
				id:    id,
				owner: OwnerMarkOther(),
				minor: MinorMarkNormal(),
			})
			inPeople[id] = true
		}
	}

	// Map raw turns to stable turns, preserving input order.
	outTurns := make([]Turn, 0, len(next.rawTurns))
	for _, rt := range next.rawTurns {
		outTurns = append(outTurns, Turn{span: rt.span, speaker: assigned[rt.cluster]})
	}

	return Numbered{
		turns: Turns{turns: outTurns},
		roster: Roster{
			next:   SpeakerNumber{speakerNumber: nextNum},
			people: people,
			edits:  roster.edits,
		},
	}
}

// TrimTurns cuts turns that extend past length (dropping any that start at or after length).
func TrimTurns(turns Turns, length Seconds) Turns {
	if !validSeconds(length) {
		defect.Raise("recordingspeakers.TrimTurns", "invalid Seconds")
	}
	var out []Turn
	for _, t := range turns.turns {
		if t.span.start.seconds >= length.seconds {
			continue
		}
		end := t.span.end.seconds
		if end > length.seconds {
			end = length.seconds
		}
		if end > t.span.start.seconds {
			out = append(out, Turn{
				span:    Span{start: t.span.start, end: Seconds{seconds: end}},
				speaker: t.speaker,
			})
		}
	}
	return Turns{turns: out}
}

// TrimCovered cuts covered audio spans that extend past length (dropping any that start at or after length).
func TrimCovered(covered Covered, length Seconds) Covered {
	if !validSeconds(length) {
		defect.Raise("recordingspeakers.TrimCovered", "invalid Seconds")
	}
	var out []Span
	for _, s := range covered.covered {
		if s.start.seconds >= length.seconds {
			continue
		}
		end := s.end.seconds
		if end > length.seconds {
			end = length.seconds
		}
		if end > s.start.seconds {
			out = append(out, Span{start: s.start, end: Seconds{seconds: end}})
		}
	}
	return Covered{covered: out}
}

// CheckLine says whether seg is eligible for speaker labelling given timing and covered audio spans. It returns none
// when the line may be aligned, or the reason it is left unlabelled (echo, unreliable_timing, or hole). A zero segment
// or timing is a defect.
func CheckLine(seg Segment, timing Timing, covered Covered) opt.Option[SkipReason] {
	seg.known("recordingspeakers.CheckLine")
	if timing == (Timing{}) {
		defect.Raise("recordingspeakers.CheckLine", "the zero Timing")
	}
	return checkLineWithMerged(seg, timing, coveredSpans(covered))
}

func checkLineWithMerged(seg Segment, timing Timing, mergedCovered []Span) opt.Option[SkipReason] {
	if seg.echo == EchoPersona() {
		return opt.Some(SkipReasonEcho())
	}
	if seg.source == LineSourceHub() && timing == TimingSpread() {
		return opt.Some(SkipReasonUnreliableTiming())
	}
	if lineInHole(seg.span, mergedCovered) {
		return opt.Some(SkipReasonHole())
	}
	return opt.None[SkipReason]()
}

// OverlapLine computes layer-one alignment for span against turns: the overlap in seconds with each original speaker
// id, ignoring merges.
func OverlapLine(turns Turns, span Span) Overlaps {
	return overlapLineWithIndex(indexTurnsBySpeaker(turns), span)
}

type turnIndex struct {
	ids   []SpeakerID
	spans map[SpeakerID][]Span
}

func indexTurnsBySpeaker(turns Turns) turnIndex {
	raw := make(map[SpeakerID][]Span)
	for _, t := range turns.turns {
		raw[t.speaker] = append(raw[t.speaker], t.span)
	}
	ids := slices.SortedFunc(maps.Keys(raw), compareSpeakerID)
	merged := make(map[SpeakerID][]Span, len(ids))
	for _, id := range ids {
		merged[id] = mergeSpans(raw[id])
	}
	return turnIndex{ids: ids, spans: merged}
}

func overlapLineWithIndex(idx turnIndex, span Span) Overlaps {
	if span.end.seconds <= span.start.seconds || len(idx.ids) == 0 {
		return Overlaps{}
	}
	line := []Span{span}
	var out []Overlap
	for _, id := range idx.ids {
		ov := spansOverlap(line, idx.spans[id])
		if ov > 0 {
			out = append(out, Overlap{speaker: id, seconds: Seconds{seconds: ov}})
		}
	}
	return Overlaps{overlaps: out}
}

// AlignOverlap computes layer-two alignment from layer-one overlaps, the current roster merges and minor marks, and the
// meeting quality. A speaker is sure only when quality is ok, the merged person holds over 70% of all overlap, overlaps
// at least 1 second, and is not minor. A zero quality is a defect.
func AlignOverlap(over Overlaps, roster Roster, quality Quality) LineLabel {
	if quality == (Quality{}) {
		defect.Raise("recordingspeakers.AlignOverlap", "the zero Quality")
	}
	if len(over.overlaps) == 0 {
		return LineLabelSkipped(SkipReasonNoOverlap())
	}
	byCanon := make(map[SpeakerID]float64, len(over.overlaps))
	total := 0.0
	for _, o := range over.overlaps {
		canon := canonicalID(roster.people, o.speaker)
		byCanon[canon] += o.seconds.seconds
		total += o.seconds.seconds
	}
	if total <= 0 {
		return LineLabelSkipped(SkipReasonNoOverlap())
	}
	ids := slices.SortedFunc(maps.Keys(byCanon), compareSpeakerID)
	bestID := ids[0]
	bestSec := byCanon[bestID]
	for _, id := range ids[1:] {
		sec := byCanon[id]
		if sec > bestSec+floatEps {
			bestID, bestSec = id, sec
		}
	}
	minor := isMinorInRoster(roster.people, bestID)
	sure := quality == QualityOk() &&
		bestSec > total*sureMinShare+floatEps &&
		bestSec+floatEps >= sureMinSeconds &&
		!minor
	if sure {
		return LineLabelAssigned(bestID, CertaintySure())
	}
	return LineLabelAssigned(bestID, CertaintyUnsure())
}

// AlignLine aligns a single transcript segment against env, roster and quality, returning both layer-one overlaps and
// the layer-two label (or skip reason). A zero segment, env or quality is a defect.
func AlignLine(seg Segment, env AlignEnv, roster Roster, quality Quality) LineAlignment {
	seg.known("recordingspeakers.AlignLine")
	if env.timing == (Timing{}) {
		defect.Raise("recordingspeakers.AlignLine", "the zero AlignEnv")
	}
	if quality == (Quality{}) {
		defect.Raise("recordingspeakers.AlignLine", "the zero Quality")
	}
	return alignSegment(seg, indexTurnsBySpeaker(env.turns), coveredSpans(env.covered), env.timing, roster, quality)
}

// AlignSegments aligns every segment in segs against env, roster and quality, returning labels in matching order. A
// zero env or quality is a defect.
func AlignSegments(segs Segments, env AlignEnv, roster Roster, quality Quality) LineLabels {
	if env.timing == (Timing{}) {
		defect.Raise("recordingspeakers.AlignSegments", "the zero AlignEnv")
	}
	if quality == (Quality{}) {
		defect.Raise("recordingspeakers.AlignSegments", "the zero Quality")
	}
	idx := indexTurnsBySpeaker(env.turns)
	cov := coveredSpans(env.covered)
	out := make([]LineLabel, len(segs.segments))
	for i, seg := range segs.segments {
		out[i] = alignSegment(seg, idx, cov, env.timing, roster, quality).label
	}
	return LineLabels{lineLabels: out}
}

func alignSegment(seg Segment, idx turnIndex, cov []Span, timing Timing, roster Roster, quality Quality) LineAlignment {
	var skipped opt.Option[SkipReason]
	skipped = checkLineWithMerged(seg, timing, cov)
	if !skipped.IsNone() {
		var reason SkipReason
		skipped.Match(func(r SkipReason) { reason = r }, func() {})
		return LineAlignment{label: LineLabelSkipped(reason)}
	}
	over := overlapLineWithIndex(idx, seg.span)
	return LineAlignment{
		label:    AlignOverlap(over, roster, quality),
		overlaps: over,
	}
}

// ComputeStats computes per-original-id speaking seconds (from turns) and attributed line counts (from eligible
// segments via layer-one max overlap). A zero timing is a defect.
func ComputeStats(turns Turns, segs Segments, timing Timing, covered Covered) Stats {
	if timing == (Timing{}) {
		defect.Raise("recordingspeakers.ComputeStats", "the zero Timing")
	}
	idx := indexTurnsBySpeaker(turns)
	cov := coveredSpans(covered)
	secs := make(map[SpeakerID]float64, len(idx.ids))
	lines := make(map[SpeakerID]int, len(idx.ids))
	for _, id := range idx.ids {
		secs[id] = spansDuration(idx.spans[id])
	}
	for _, seg := range segs.segments {
		if !checkLineWithMerged(seg, timing, cov).IsNone() {
			continue
		}
		over := overlapLineWithIndex(idx, seg.span)
		if len(over.overlaps) == 0 {
			continue
		}
		// Pick original id with largest overlap; ties broken by smaller speaker number (overlaps are already sorted).
		best := over.overlaps[0]
		for _, o := range over.overlaps[1:] {
			if o.seconds.seconds > best.seconds.seconds+floatEps {
				best = o
			}
		}
		lines[best.speaker]++
	}
	out := make([]SpeakerStat, 0, len(idx.ids))
	for _, id := range idx.ids {
		out = append(out, SpeakerStat{
			speaker: id,
			seconds: Seconds{seconds: secs[id]},
			lines:   LineCount{lineCount: lines[id]},
		})
	}
	return Stats{stats: out}
}

// SumStats aggregates per-original-id stats by canonical merged person in roster.
func SumStats(stats Stats, roster Roster) Stats {
	secs := make(map[SpeakerID]float64, len(stats.stats))
	lines := make(map[SpeakerID]int, len(stats.stats))
	for _, st := range stats.stats {
		canon := canonicalID(roster.people, st.speaker)
		secs[canon] += st.seconds.seconds
		lines[canon] += st.lines.lineCount
	}
	ids := slices.SortedFunc(maps.Keys(secs), compareSpeakerID)
	out := make([]SpeakerStat, 0, len(ids))
	for _, id := range ids {
		out = append(out, SpeakerStat{
			speaker: id,
			seconds: Seconds{seconds: secs[id]},
			lines:   LineCount{lineCount: lines[id]},
		})
	}
	return Stats{stats: out}
}

// MarkMinor returns roster with each speaker's minor mark updated from stats: an unmerged speaker with under 20 seconds
// of combined (post-merge) speaking time is minor; merged speakers are never minor.
func MarkMinor(stats Stats, roster Roster) Roster {
	people := slices.Clone(roster.people)
	applyMinor(stats, people)
	next := roster.next
	if next == (SpeakerNumber{}) {
		next = SpeakerNumber{speakerNumber: 1}
	}
	return Roster{next: next, people: people, edits: roster.edits}
}

func applyMinor(stats Stats, people []Person) {
	secs := mergedSecondsMap(stats, people)
	for i := range people {
		if !people[i].mergedInto.IsNone() {
			people[i].minor = MinorMarkNormal()
			continue
		}
		if secs[people[i].id]+floatEps < minorThresholdSeconds {
			people[i].minor = MinorMarkMinor()
		} else {
			people[i].minor = MinorMarkNormal()
		}
	}
}

func mergedSecondsMap(stats Stats, people []Person) map[SpeakerID]float64 {
	secs := make(map[SpeakerID]float64, len(stats.stats))
	for _, st := range stats.stats {
		canon := canonicalID(people, st.speaker)
		secs[canon] += st.seconds.seconds
	}
	return secs
}

// Edit applies ops atomically to roster using stats to break automatic merges (less spoken into more spoken) and to
// recompute minor marks. It returns the updated roster and the server-computed undo list (empty after clear).
func Edit(roster Roster, stats Stats, ops EditOps) (out outcome.Of[Edited, Fail]) {
	if len(ops.editOps) == 0 {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.ops.empty", "ops")))
	}
	people := slices.Clone(roster.people)
	edits := roster.edits
	next := roster.next
	if next == (SpeakerNumber{}) {
		next = SpeakerNumber{speakerNumber: 1}
	}
	var undoGroups [][]EditOp
	for _, rawOp := range ops.editOps {
		switch op := rawOp.(type) {
		case opName:
			ug, f := applyNameOp(people, stats, op)
			if f != nil {
				return out.Err(f)
			}
			if len(ug) > 0 {
				undoGroups = append(undoGroups, ug)
			}
			edits.name = EditCount{editCount: edits.name.editCount + 1}
		case opMerge:
			ug, f := applyMergeOp(people, op)
			if f != nil {
				return out.Err(f)
			}
			if len(ug) > 0 {
				undoGroups = append(undoGroups, ug)
			}
			edits.merge = EditCount{editCount: edits.merge.editCount + 1}
		case opUnmerge:
			ug, f := applyUnmergeOp(people, op)
			if f != nil {
				return out.Err(f)
			}
			if len(ug) > 0 {
				undoGroups = append(undoGroups, ug)
			}
			edits.unmerge = EditCount{editCount: edits.unmerge.editCount + 1}
		case opClear:
			people = nil
			undoGroups = nil
			edits.clear = EditCount{editCount: edits.clear.editCount + 1}
		default:
			defect.Raise("recordingspeakers.Edit", "unknown EditOp")
		}
	}
	applyMinor(stats, people)
	var undo []EditOp
	for i := len(undoGroups) - 1; i >= 0; i-- {
		undo = append(undo, undoGroups[i]...)
	}
	return out.Ok(Edited{
		roster: Roster{next: next, people: people, edits: edits},
		undo:   EditOps{editOps: undo},
	})
}

func applyNameOp(people []Person, stats Stats, op opName) ([]EditOp, Fail) {
	idx := indexOfPerson(people, op.id)
	if idx < 0 {
		return nil, missingCode("recordingspeakers.speaker.not_found")
	}
	if !people[idx].mergedInto.IsNone() {
		return nil, refusedCode("recordingspeakers.speaker.merged")
	}
	oldName := people[idx].name
	oldOwner := people[idx].owner

	// Find another unmerged speaker with the same non-empty name (triggers automatic merge).
	otherIdx := -1
	op.name.Match(func(target SpeakerName) {
		for i := range people {
			if i == idx || !people[i].mergedInto.IsNone() {
				continue
			}
			people[i].name.Match(func(n SpeakerName) {
				if n == target {
					otherIdx = i
				}
			}, func() {})
			if otherIdx >= 0 {
				break
			}
		}
	}, func() {})

	// If marking as owner, record any third unmerged speaker currently marked owner so we can clear and undo it.
	prevOwnerIdx := -1
	if op.owner == OwnerMarkOwner() {
		for i := range people {
			if i != idx && i != otherIdx && people[i].mergedInto.IsNone() && people[i].owner == OwnerMarkOwner() {
				prevOwnerIdx = i
				break
			}
		}
	}

	if otherIdx < 0 {
		// Simple rename or clear-name.
		var undo []EditOp
		if prevOwnerIdx >= 0 {
			prevID := people[prevOwnerIdx].id
			prevName := people[prevOwnerIdx].name
			people[prevOwnerIdx].owner = OwnerMarkOther()
			undo = append(undo, EditOpName(prevID, prevName, OwnerMarkOwner()))
		}
		if oldName == op.name && oldOwner == op.owner {
			return undo, nil
		}
		people[idx].name = op.name
		people[idx].owner = op.owner
		undo = append([]EditOp{EditOpName(op.id, oldName, oldOwner)}, undo...)
		return undo, nil
	}

	// Automatic merge: less spoken merges into more spoken; ties merge the newly named speaker into the existing holder.
	secs := mergedSecondsMap(stats, people)
	pSec := secs[people[idx].id]
	otherSec := secs[people[otherIdx].id]
	fromIdx, intoIdx := idx, otherIdx
	if pSec > otherSec+floatEps {
		fromIdx, intoIdx = otherIdx, idx
	}
	fromID := people[fromIdx].id
	intoID := people[intoIdx].id
	otherOldOwner := people[otherIdx].owner

	var children []SpeakerID
	for i := range people {
		people[i].mergedInto.Match(func(target SpeakerID) {
			if target == fromID {
				children = append(children, people[i].id)
			}
		}, func() {})
	}

	mergedOwner := OwnerMarkOther()
	if op.owner == OwnerMarkOwner() || oldOwner == OwnerMarkOwner() || otherOldOwner == OwnerMarkOwner() {
		mergedOwner = OwnerMarkOwner()
	}

	var prevOwnerRestore opt.Option[EditOp]
	if prevOwnerIdx >= 0 {
		prevID := people[prevOwnerIdx].id
		prevName := people[prevOwnerIdx].name
		people[prevOwnerIdx].owner = OwnerMarkOther()
		prevOwnerRestore = opt.Some(EditOpName(prevID, prevName, OwnerMarkOwner()))
	}

	// Apply merge state (no chains).
	people[intoIdx].name = op.name
	people[intoIdx].owner = mergedOwner
	people[intoIdx].mergedInto = opt.None[SpeakerID]()
	people[fromIdx].name = opt.None[SpeakerName]()
	people[fromIdx].owner = OwnerMarkOther()
	people[fromIdx].mergedInto = opt.Some(intoID)
	people[fromIdx].minor = MinorMarkNormal()
	for i := range people {
		people[i].mergedInto.Match(func(target SpeakerID) {
			if target == fromID {
				people[i].mergedInto = opt.Some(intoID)
			}
		}, func() {})
	}

	// Compute exact inverse undo ops.
	var undo []EditOp
	if fromIdx == idx {
		// Newly named speaker merged into existing holder.
		undo = append(undo, EditOpUnmerge(fromID))
		for _, cid := range children {
			undo = append(undo, EditOpUnmerge(cid), EditOpMerge(cid, fromID))
		}
		if !oldName.IsNone() || oldOwner == OwnerMarkOwner() {
			undo = append(undo, EditOpName(fromID, oldName, oldOwner))
		}
		if mergedOwner != otherOldOwner {
			undo = append(undo, EditOpName(intoID, op.name, otherOldOwner))
		}
	} else {
		// Existing holder merged into newly named speaker: restore newly named speaker's old name first so restoring
		// existing holder's name does not re-trigger an automatic merge.
		undo = append(undo, EditOpName(intoID, oldName, oldOwner))
		undo = append(undo, EditOpUnmerge(fromID))
		for _, cid := range children {
			undo = append(undo, EditOpUnmerge(cid), EditOpMerge(cid, fromID))
		}
		undo = append(undo, EditOpName(fromID, op.name, otherOldOwner))
	}
	prevOwnerRestore.Match(func(restoreOp EditOp) {
		undo = append(undo, restoreOp)
	}, func() {})
	return undo, nil
}

func applyMergeOp(people []Person, op opMerge) ([]EditOp, Fail) {
	fromIdx := indexOfPerson(people, op.from)
	if fromIdx < 0 {
		return nil, missingCode("recordingspeakers.speaker.not_found")
	}
	intoIdx := indexOfPerson(people, op.into)
	if intoIdx < 0 {
		return nil, missingCode("recordingspeakers.speaker.not_found")
	}
	if !people[fromIdx].mergedInto.IsNone() {
		return nil, refusedCode("recordingspeakers.speaker.already_merged")
	}
	targetID := canonicalID(people, op.into)
	if targetID == op.from {
		return nil, invalidParts(wrong(nil, false, "recordingspeakers.merge.same_speaker", "into"))
	}
	fromOldName := people[fromIdx].name
	fromOldOwner := people[fromIdx].owner
	var children []SpeakerID
	for i := range people {
		people[i].mergedInto.Match(func(target SpeakerID) {
			if target == op.from {
				children = append(children, people[i].id)
			}
		}, func() {})
	}
	people[fromIdx].mergedInto = opt.Some(targetID)
	people[fromIdx].name = opt.None[SpeakerName]()
	people[fromIdx].owner = OwnerMarkOther()
	people[fromIdx].minor = MinorMarkNormal()
	for i := range people {
		people[i].mergedInto.Match(func(target SpeakerID) {
			if target == op.from {
				people[i].mergedInto = opt.Some(targetID)
			}
		}, func() {})
	}
	undo := []EditOp{EditOpUnmerge(op.from)}
	for _, cid := range children {
		undo = append(undo, EditOpUnmerge(cid), EditOpMerge(cid, op.from))
	}
	if !fromOldName.IsNone() || fromOldOwner == OwnerMarkOwner() {
		undo = append(undo, EditOpName(op.from, fromOldName, fromOldOwner))
	}
	return undo, nil
}

func applyUnmergeOp(people []Person, op opUnmerge) ([]EditOp, Fail) {
	idx := indexOfPerson(people, op.id)
	if idx < 0 {
		return nil, missingCode("recordingspeakers.speaker.not_found")
	}
	if people[idx].mergedInto.IsNone() {
		return nil, refusedCode("recordingspeakers.speaker.not_merged")
	}
	var prevTarget SpeakerID
	people[idx].mergedInto.Match(func(t SpeakerID) { prevTarget = t }, func() {})
	people[idx].mergedInto = opt.None[SpeakerID]()
	people[idx].name = opt.None[SpeakerName]()
	people[idx].owner = OwnerMarkOther()
	return []EditOp{EditOpMerge(op.id, prevTarget)}, nil
}

func indexOfPerson(people []Person, id SpeakerID) int {
	for i := range people {
		if people[i].id == id {
			return i
		}
	}
	return -1
}

// canonicalID follows mergedInto (at most one hop in a valid roster, bounded defensively) to the canonical unmerged id.
func canonicalID(people []Person, id SpeakerID) SpeakerID {
	cur := id
	for range len(people) {
		idx := indexOfPerson(people, cur)
		if idx < 0 || people[idx].mergedInto.IsNone() {
			return cur
		}
		var next SpeakerID
		people[idx].mergedInto.Match(func(t SpeakerID) { next = t }, func() {})
		if next == cur {
			return cur
		}
		cur = next
	}
	return cur
}

func isMinorInRoster(people []Person, id SpeakerID) bool {
	idx := indexOfPerson(people, id)
	return idx >= 0 && people[idx].minor == MinorMarkMinor()
}

func compareSpeakerID(a, b SpeakerID) int {
	return cmp.Compare(a.num("recordingspeakers.compareSpeakerID"), b.num("recordingspeakers.compareSpeakerID"))
}
