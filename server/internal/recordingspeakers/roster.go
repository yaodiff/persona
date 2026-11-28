package recordingspeakers

import (
	"slices"

	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
)

// Person is one speaker entry in a meeting's roster. The zero Person is no person; only PersonBuilder.Build makes one,
// and every method raises a defect on it.
type Person struct {
	id         SpeakerID
	name       opt.Option[SpeakerName]
	owner      OwnerMark
	mergedInto opt.Option[SpeakerID]
	minor      MinorMark
}

// PersonVisitor is told every part of a Person.
type PersonVisitor interface {
	VisitPerson(id SpeakerID, name opt.Option[SpeakerName], owner OwnerMark, mergedInto opt.Option[SpeakerID])
	VisitMinor(minor MinorMark)
}

// ID is the speaker's stable identifier.
func (p Person) ID() SpeakerID {
	p.known("recordingspeakers.Person.ID")
	return p.id
}

// IsMerged says whether p is merged into another speaker.
func (p Person) IsMerged() bool {
	p.known("recordingspeakers.Person.IsMerged")
	return !p.mergedInto.IsNone()
}

// IsMinor says whether p is marked minor.
func (p Person) IsMinor() bool {
	p.known("recordingspeakers.Person.IsMinor")
	return p.minor == MinorMarkMinor()
}

// IsOwner says whether p is marked as the meeting owner.
func (p Person) IsOwner() bool {
	p.known("recordingspeakers.Person.IsOwner")
	return p.owner == OwnerMarkOwner()
}

// Accept tells v every part of p; the zero Person is a defect.
func (p Person) Accept(v PersonVisitor) {
	p.known("recordingspeakers.Person.Accept")
	v.VisitPerson(p.id, p.name, p.owner, p.mergedInto)
	v.VisitMinor(p.minor)
}

// known raises a defect when p is the zero Person.
func (p Person) known(op string) {
	if p.id == (SpeakerID{}) || p.owner == (OwnerMark{}) || p.minor == (MinorMark{}) {
		defect.Raise(op, "the zero Person")
	}
}

// PersonNamingStep is the person builder's first stage: owner-chosen name and owner mark. The zero PersonNamingStep is
// a skipped stage, and Build refuses what it leaves out.
type PersonNamingStep struct{ personNamingStep Person }

// PersonStatusStep is the person builder's second stage: merge target and minor mark. The zero PersonStatusStep is a
// skipped stage, and Build refuses what it leaves out.
type PersonStatusStep struct{ personStatusStep Person }

// PersonBuilder is the person builder's last stage. The zero PersonBuilder is a skipped stage, and Build refuses what
// it leaves out.
type PersonBuilder struct{ personBuilder Person }

// NewPersonBuilder starts building a Person with its stable speaker id.
func NewPersonBuilder(id SpeakerID) PersonNamingStep {
	return PersonNamingStep{personNamingStep: Person{id: id}}
}

// Naming sets the person's optional owner-chosen name and whether this speaker is the owner.
func (s PersonNamingStep) Naming(name opt.Option[SpeakerName], owner OwnerMark) PersonStatusStep {
	p := s.personNamingStep
	p.name, p.owner = name, owner
	return PersonStatusStep{personStatusStep: p}
}

// Status sets the person's optional merge target and minor mark.
func (s PersonStatusStep) Status(mergedInto opt.Option[SpeakerID], minor MinorMark) PersonBuilder {
	p := s.personStatusStep
	p.mergedInto, p.minor = mergedInto, minor
	return PersonBuilder{personBuilder: p}
}

// Build validates and constructs the Person, reporting every missing or conflicting part in order.
func (b PersonBuilder) Build() (out outcome.Of[Person, Fail]) {
	p := b.personBuilder
	var parts []fail.Invalid
	parts = wrong(parts, p.id != (SpeakerID{}), "recordingspeakers.person.no_id", "speaker")
	parts = wrong(parts, p.owner != (OwnerMark{}), "recordingspeakers.person.no_owner", "owner")
	parts = wrong(parts, p.minor != (MinorMark{}), "recordingspeakers.person.no_minor", "minor")
	p.name.Match(func(n SpeakerName) {
		parts = wrong(parts, n != (SpeakerName{}), "recordingspeakers.person.zero_name", "name")
	}, func() {})
	p.mergedInto.Match(func(target SpeakerID) {
		parts = wrong(parts, target != (SpeakerID{}), "recordingspeakers.person.zero_target", "mergedInto")
		parts = wrong(parts, target != p.id, "recordingspeakers.person.self_merge", "mergedInto")
		parts = wrong(parts, p.name.IsNone(), "recordingspeakers.person.merged_with_name", "name")
		parts = wrong(parts, p.owner == OwnerMarkOther(), "recordingspeakers.person.merged_as_owner", "owner")
		parts = wrong(parts, p.minor == MinorMarkNormal(), "recordingspeakers.person.merged_as_minor", "minor")
	}, func() {})
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(p)
}

// EditCounts records how many speaker edits of each kind have been applied to a meeting. The zero EditCounts is zero
// edits of every kind.
type EditCounts struct {
	name    EditCount
	merge   EditCount
	unmerge EditCount
	clear   EditCount
}

// EditCountsVisitor is told the edit counts for each edit kind.
type EditCountsVisitor interface {
	VisitEditCounts(name, merge, unmerge, clear EditCount)
}

// NewEditCounts constructs an EditCounts from each kind's count.
func NewEditCounts(name, merge, unmerge, clear EditCount) EditCounts {
	return EditCounts{name: name, merge: merge, unmerge: unmerge, clear: clear}
}

// Accept tells v the four edit counts.
func (e EditCounts) Accept(v EditCountsVisitor) {
	v.VisitEditCounts(e.name, e.merge, e.unmerge, e.clear)
}

// Roster is a meeting's speaker list, next number to assign, and cumulative edit counts. It upholds the roster
// invariants: unique speaker ids, next strictly greater than every speaker number, and no merge chains.
// The zero Roster is an empty roster with next speaker 1 and zero edits.
type Roster struct {
	next   SpeakerNumber
	people []Person
	edits  EditCounts
}

// RosterVisitor is told a roster's next speaker number, each person in order, and its edit counts.
type RosterVisitor interface {
	VisitNext(next SpeakerNumber)
	VisitPerson(person Person)
	VisitEdits(edits EditCounts)
}

// NewRoster validates and constructs a Roster.
func NewRoster(next SpeakerNumber, people []Person, edits EditCounts) (out outcome.Of[Roster, Fail]) {
	return buildRoster(next, people, edits)
}

// RestoreRoster rebuilds a stored Roster, validating all roster invariants.
func RestoreRoster(next SpeakerNumber, people []Person, edits EditCounts) (out outcome.Of[Roster, Fail]) {
	return buildRoster(next, people, edits)
}

func buildRoster(next SpeakerNumber, people []Person, edits EditCounts) (out outcome.Of[Roster, Fail]) {
	var parts []fail.Invalid
	parts = wrong(parts, next != (SpeakerNumber{}), "recordingspeakers.roster.no_next", "next")
	byID := make(map[SpeakerID]Person, len(people))
	maxNum := 0
	for i := range people {
		p := people[i]
		if p.id == (SpeakerID{}) || p.owner == (OwnerMark{}) || p.minor == (MinorMark{}) {
			parts = wrong(parts, false, "recordingspeakers.roster.zero_person", "people")
			continue
		}
		if _, dup := byID[p.id]; dup {
			parts = wrong(parts, false, "recordingspeakers.roster.duplicate_speaker", "people")
			continue
		}
		byID[p.id] = p
		if n := p.id.num("recordingspeakers.buildRoster"); n > maxNum {
			maxNum = n
		}
	}
	if next != (SpeakerNumber{}) && next.speakerNumber <= maxNum {
		parts = wrong(parts, false, "recordingspeakers.roster.next_too_small", "next")
	}
	for i := range people {
		p := people[i]
		p.mergedInto.Match(func(target SpeakerID) {
			t, exists := byID[target]
			if !exists {
				parts = wrong(parts, false, "recordingspeakers.roster.missing_merge_target", "mergedInto")
				return
			}
			if !t.mergedInto.IsNone() {
				parts = wrong(parts, false, "recordingspeakers.roster.merge_chain", "mergedInto")
			}
		}, func() {})
	}
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(Roster{next: next, people: slices.Clone(people), edits: edits})
}

// Accept tells v the roster's next number (1 for the zero Roster), each person in order, and the edit counts.
func (r Roster) Accept(v RosterVisitor) {
	next := r.next
	if next == (SpeakerNumber{}) {
		next = SpeakerNumber{speakerNumber: 1}
	}
	v.VisitNext(next)
	for i := range r.people {
		v.VisitPerson(r.people[i])
	}
	v.VisitEdits(r.edits)
}

// SpeakerStat is one speaker's speaking duration and attributed line count. The zero SpeakerStat is no stat;
// NewSpeakerStat never makes one, and Accept raises a defect on it.
type SpeakerStat struct {
	speaker SpeakerID
	seconds Seconds
	lines   LineCount
}

// SpeakerStatVisitor is told a speaker's id, speaking seconds, and line count.
type SpeakerStatVisitor interface {
	VisitSpeakerStat(speaker SpeakerID, seconds Seconds, lines LineCount)
}

// NewSpeakerStat builds a SpeakerStat; a zero speaker id is invalid.
func NewSpeakerStat(speaker SpeakerID, seconds Seconds, lines LineCount) (out outcome.Of[SpeakerStat, Fail]) {
	var parts []fail.Invalid
	parts = wrong(parts, speaker != (SpeakerID{}), "recordingspeakers.stat.no_speaker", "speaker")
	parts = wrong(parts, validSeconds(seconds), "recordingspeakers.stat.invalid_seconds", "seconds")
	parts = wrong(parts, lines.lineCount >= 0, "recordingspeakers.stat.negative_lines", "lines")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(SpeakerStat{speaker: speaker, seconds: seconds, lines: lines})
}

// Accept tells v the speaker stat; the zero SpeakerStat is a defect.
func (s SpeakerStat) Accept(v SpeakerStatVisitor) {
	if s.speaker == (SpeakerID{}) {
		defect.Raise("recordingspeakers.SpeakerStat.Accept", "the zero SpeakerStat")
	}
	v.VisitSpeakerStat(s.speaker, s.seconds, s.lines)
}

// Stats is a map-like set of speaker statistics ordered by speaker number. The zero Stats is empty.
type Stats struct{ stats []SpeakerStat }

// StatsVisitor is told each speaker stat in speaker-number order.
type StatsVisitor interface {
	VisitSpeakerStat(stat SpeakerStat)
}

// NewStats validates list (no zero or duplicate speaker ids) and stores it sorted by speaker number.
func NewStats(list []SpeakerStat) (out outcome.Of[Stats, Fail]) {
	seen := make(map[SpeakerID]bool, len(list))
	var parts []fail.Invalid
	for i := range list {
		if list[i].speaker == (SpeakerID{}) {
			parts = wrong(parts, false, "recordingspeakers.stats.zero_stat", "stats")
			continue
		}
		if seen[list[i].speaker] {
			parts = wrong(parts, false, "recordingspeakers.stats.duplicate_speaker", "stats")
			continue
		}
		seen[list[i].speaker] = true
	}
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	cp := slices.Clone(list)
	slices.SortFunc(cp, func(a, b SpeakerStat) int {
		return a.speaker.num("recordingspeakers.NewStats") - b.speaker.num("recordingspeakers.NewStats")
	})
	return out.Ok(Stats{stats: cp})
}

// Accept tells v each speaker stat in speaker-number order.
func (s Stats) Accept(v StatsVisitor) {
	for i := range s.stats {
		v.VisitSpeakerStat(s.stats[i])
	}
}

// Overlap is one original speaker's overlap duration with a transcript line (layer one). The zero Overlap is no
// overlap; NewOverlap never makes one, and Accept raises a defect on it.
type Overlap struct {
	speaker SpeakerID
	seconds Seconds
}

// OverlapVisitor is told an original speaker id and its overlap duration in seconds.
type OverlapVisitor interface {
	VisitOverlap(speaker SpeakerID, seconds Seconds)
}

// NewOverlap builds a positive overlap entry; a zero speaker or non-positive seconds is invalid.
func NewOverlap(speaker SpeakerID, seconds Seconds) (out outcome.Of[Overlap, Fail]) {
	var parts []fail.Invalid
	parts = wrong(parts, speaker != (SpeakerID{}), "recordingspeakers.overlap.no_speaker", "speaker")
	parts = wrong(parts, validSeconds(seconds) && seconds.seconds > 0, "recordingspeakers.overlap.not_positive", "seconds")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(Overlap{speaker: speaker, seconds: seconds})
}

// Accept tells v the overlap entry; the zero Overlap is a defect.
func (o Overlap) Accept(v OverlapVisitor) {
	if o.speaker == (SpeakerID{}) {
		defect.Raise("recordingspeakers.Overlap.Accept", "the zero Overlap")
	}
	v.VisitOverlap(o.speaker, o.seconds)
}

// Overlaps is a line's layer-one overlap seconds per original speaker id, ordered by speaker number.
// The zero Overlaps is empty (no overlap).
type Overlaps struct{ overlaps []Overlap }

// OverlapsVisitor is told each overlap entry in speaker-number order.
type OverlapsVisitor interface {
	VisitOverlap(overlap Overlap)
}

// NewOverlaps validates list (no zero or duplicate speaker ids) and stores it sorted by speaker number.
func NewOverlaps(list []Overlap) (out outcome.Of[Overlaps, Fail]) {
	seen := make(map[SpeakerID]bool, len(list))
	var parts []fail.Invalid
	for i := range list {
		if list[i].speaker == (SpeakerID{}) {
			parts = wrong(parts, false, "recordingspeakers.overlaps.zero_overlap", "overlaps")
			continue
		}
		if seen[list[i].speaker] {
			parts = wrong(parts, false, "recordingspeakers.overlaps.duplicate_speaker", "overlaps")
			continue
		}
		seen[list[i].speaker] = true
	}
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	cp := slices.Clone(list)
	slices.SortFunc(cp, func(a, b Overlap) int {
		return a.speaker.num("recordingspeakers.NewOverlaps") - b.speaker.num("recordingspeakers.NewOverlaps")
	})
	return out.Ok(Overlaps{overlaps: cp})
}

// Accept tells v each overlap entry in speaker-number order.
func (o Overlaps) Accept(v OverlapsVisitor) {
	for i := range o.overlaps {
		v.VisitOverlap(o.overlaps[i])
	}
}

// CheckEditKind ensures kind is a known EditKind; the zero EditKind is invalid.
func CheckEditKind(kind EditKind) (out outcome.Of[EditKind, Fail]) {
	if kind == (EditKind{}) {
		return out.Err(invalidParts(wrong(nil, false, "recordingspeakers.op.unknown", "op")))
	}
	return out.Ok(kind)
}

// EditOp is one edit operation on a meeting's speaker roster: name, merge, unmerge or clear. The zero EditOp is nil;
// only its constructors make one.
type EditOp interface {
	Accept(v EditOpVisitor)
	editOp()
}

// EditOpVisitor is told which edit operation it is and its operands.
type EditOpVisitor interface {
	VisitName(id SpeakerID, name opt.Option[SpeakerName], owner OwnerMark)
	VisitMerge(from, into SpeakerID)
	VisitUnmerge(id SpeakerID)
	VisitClear()
}

type opName struct {
	id    SpeakerID
	name  opt.Option[SpeakerName]
	owner OwnerMark
}

type opMerge struct {
	from SpeakerID
	into SpeakerID
}

type opUnmerge struct{ id SpeakerID }

type opClear struct{}

// EditOpName names (or clears the name of, when name is none) speaker id, and marks whether they are the owner. A zero
// id or zero owner mark is a defect.
func EditOpName(id SpeakerID, name opt.Option[SpeakerName], owner OwnerMark) EditOp {
	if id == (SpeakerID{}) {
		defect.Raise("recordingspeakers.EditOpName", "the zero SpeakerID")
	}
	if owner == (OwnerMark{}) {
		defect.Raise("recordingspeakers.EditOpName", "the zero OwnerMark")
	}
	name.Match(func(n SpeakerName) {
		if n == (SpeakerName{}) {
			defect.Raise("recordingspeakers.EditOpName", "the zero SpeakerName")
		}
	}, func() {})
	return opName{id: id, name: name, owner: owner}
}

// EditOpMerge merges speaker from into speaker into. Either id zero is a defect.
func EditOpMerge(from, into SpeakerID) EditOp {
	if from == (SpeakerID{}) || into == (SpeakerID{}) {
		defect.Raise("recordingspeakers.EditOpMerge", "the zero SpeakerID")
	}
	return opMerge{from: from, into: into}
}

// EditOpUnmerge unmerges speaker id (returning it unnamed). A zero id is a defect.
func EditOpUnmerge(id SpeakerID) EditOp {
	if id == (SpeakerID{}) {
		defect.Raise("recordingspeakers.EditOpUnmerge", "the zero SpeakerID")
	}
	return opUnmerge{id: id}
}

// EditOpClear deletes the speaker list and timeline (cannot be undone).
func EditOpClear() EditOp { return opClear{} }

func (o opName) Accept(v EditOpVisitor)    { v.VisitName(o.id, o.name, o.owner) }
func (o opMerge) Accept(v EditOpVisitor)   { v.VisitMerge(o.from, o.into) }
func (o opUnmerge) Accept(v EditOpVisitor) { v.VisitUnmerge(o.id) }
func (opClear) Accept(v EditOpVisitor)     { v.VisitClear() }
func (opName) editOp()                     {}
func (opMerge) editOp()                    {}
func (opUnmerge) editOp()                  {}
func (opClear) editOp()                    {}

// EditOps is an ordered list of edit operations (a request batch or a server-computed undo list). The zero EditOps is
// empty.
type EditOps struct{ editOps []EditOp }

// EditOpsVisitor is told each edit operation in order.
type EditOpsVisitor interface {
	VisitEditOp(op EditOp)
}

// NewEditOps copies list into EditOps; any nil EditOp in list is a defect.
func NewEditOps(list []EditOp) EditOps {
	for i := range list {
		if list[i] == nil {
			defect.Raise("recordingspeakers.NewEditOps", "a nil EditOp")
		}
	}
	return EditOps{editOps: slices.Clone(list)}
}

// IsEmpty says whether ops contains no operations.
func (e EditOps) IsEmpty() bool { return len(e.editOps) == 0 }

// Accept tells v each edit operation in order.
func (e EditOps) Accept(v EditOpsVisitor) {
	for i := range e.editOps {
		v.VisitEditOp(e.editOps[i])
	}
}

// Numbered is the result of numbering a first timeline or pairing a later timeline: the hub-numbered turns and the
// updated speaker roster. The zero Numbered is empty turns with the zero Roster.
type Numbered struct {
	turns  Turns
	roster Roster
}

// NumberedVisitor is told the numbered turns and updated roster.
type NumberedVisitor interface {
	VisitNumbered(turns Turns, roster Roster)
}

// Accept tells v the numbered turns and updated roster.
func (n Numbered) Accept(v NumberedVisitor) {
	v.VisitNumbered(n.turns, n.roster)
}

// Edited is the result of applying EditOps to a roster: the new roster and the server-computed inverse ops to undo it
// (empty after clear). The zero Edited is the zero Roster with empty undo ops.
type Edited struct {
	roster Roster
	undo   EditOps
}

// EditedVisitor is told the updated roster and its inverse undo operations.
type EditedVisitor interface {
	VisitEdited(roster Roster, undo EditOps)
}

// Accept tells v the updated roster and undo operations.
func (e Edited) Accept(v EditedVisitor) {
	v.VisitEdited(e.roster, e.undo)
}

// LineLabel is the alignment outcome for one transcript line: either assigned to a canonical speaker with certainty, or
// skipped with a reason. The zero LineLabel is nil; only its constructors make one.
type LineLabel interface {
	Accept(v LineLabelVisitor)
	lineLabel()
}

// LineLabelVisitor is told whether a line was assigned a speaker or skipped.
type LineLabelVisitor interface {
	VisitAssigned(speaker SpeakerID, certainty Certainty)
	VisitSkipped(reason SkipReason)
}

type labelAssigned struct {
	speaker   SpeakerID
	certainty Certainty
}

type labelSkipped struct{ reason SkipReason }

// LineLabelAssigned marks a line as assigned to speaker with certainty; a zero speaker or certainty is a defect.
func LineLabelAssigned(speaker SpeakerID, certainty Certainty) LineLabel {
	if speaker == (SpeakerID{}) {
		defect.Raise("recordingspeakers.LineLabelAssigned", "the zero SpeakerID")
	}
	if certainty == (Certainty{}) {
		defect.Raise("recordingspeakers.LineLabelAssigned", "the zero Certainty")
	}
	return labelAssigned{speaker: speaker, certainty: certainty}
}

// LineLabelSkipped marks a line as unlabelled for reason; a zero reason is a defect.
func LineLabelSkipped(reason SkipReason) LineLabel {
	if reason == (SkipReason{}) {
		defect.Raise("recordingspeakers.LineLabelSkipped", "the zero SkipReason")
	}
	return labelSkipped{reason: reason}
}

func (l labelAssigned) Accept(v LineLabelVisitor) { v.VisitAssigned(l.speaker, l.certainty) }
func (l labelSkipped) Accept(v LineLabelVisitor)  { v.VisitSkipped(l.reason) }
func (labelAssigned) lineLabel()                  {}
func (labelSkipped) lineLabel()                   {}

// LineLabels is a sequence of line alignment outcomes corresponding positionally to Segments. The zero LineLabels is
// empty.
type LineLabels struct{ lineLabels []LineLabel }

// LineLabelsVisitor is told each line label in order.
type LineLabelsVisitor interface {
	VisitLineLabel(label LineLabel)
}

// NewLineLabels copies list into LineLabels; any nil LineLabel in list is a defect.
func NewLineLabels(list []LineLabel) LineLabels {
	for i := range list {
		if list[i] == nil {
			defect.Raise("recordingspeakers.NewLineLabels", "a nil LineLabel")
		}
	}
	return LineLabels{lineLabels: slices.Clone(list)}
}

// Accept tells v each line label in order.
func (l LineLabels) Accept(v LineLabelsVisitor) {
	for i := range l.lineLabels {
		v.VisitLineLabel(l.lineLabels[i])
	}
}

// LineAlignment is both layers of alignment for one transcript line: layer-one overlaps per original id and layer-two
// label (assigned or skipped). The zero LineAlignment is no alignment; AlignLine never returns one.
type LineAlignment struct {
	label    LineLabel
	overlaps Overlaps
}

// LineAlignmentVisitor is told a line's layer-one overlaps and layer-two label.
type LineAlignmentVisitor interface {
	VisitLineAlignment(overlaps Overlaps, label LineLabel)
}

// Accept tells v the line's overlaps and label; a zero LineAlignment is a defect.
func (a LineAlignment) Accept(v LineAlignmentVisitor) {
	if a.label == nil {
		defect.Raise("recordingspeakers.LineAlignment.Accept", "the zero LineAlignment")
	}
	v.VisitLineAlignment(a.overlaps, a.label)
}
