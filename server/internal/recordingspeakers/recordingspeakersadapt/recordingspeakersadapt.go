package recordingspeakersadapt

import (
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/recordingspeakers"
)

// Record is the stored speaker roster wire shape: plain and JSON-tagged, owned by this adapter until PR 2-3 adds the
// contract types to recordingwire.
type Record struct {
	Next   int            `json:"next"`
	People []personRecord `json:"people,omitempty"`
	Edits  editsRecord    `json:"edits,omitempty"`
}

type personRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Me         bool   `json:"me,omitempty"`
	MergedInto string `json:"merged_into,omitempty"`
	Minor      bool   `json:"minor,omitempty"`
}

type editsRecord struct {
	Name    int `json:"name,omitempty"`
	Merge   int `json:"merge,omitempty"`
	Unmerge int `json:"unmerge,omitempty"`
	Clear   int `json:"clear,omitempty"`
}

// Restore rebuilds a recordingspeakers.Roster from its stored wire Record.
func Restore(r Record) (out outcome.Of[recordingspeakers.Roster, fail.Fail]) {
	nextVal := r.Next
	if nextVal == 0 && len(r.People) == 0 {
		nextVal = 1
	}
	next, f := outcome.Unpack(recordingspeakers.ParseSpeakerNumber(nextVal))
	if f != nil {
		return out.Err(f)
	}
	people := make([]recordingspeakers.Person, 0, len(r.People))
	for _, pr := range r.People {
		p, pf := outcome.Unpack(restorePerson(pr))
		if pf != nil {
			return out.Err(pf)
		}
		people = append(people, p)
	}
	edits, ef := outcome.Unpack(restoreEdits(r.Edits))
	if ef != nil {
		return out.Err(ef)
	}
	return outcome.Widen(recordingspeakers.RestoreRoster(next, people, edits))
}

func restorePerson(pr personRecord) (out outcome.Of[recordingspeakers.Person, fail.Fail]) {
	id, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(pr.ID))
	if f != nil {
		return out.Err(f)
	}
	name := opt.None[recordingspeakers.SpeakerName]()
	if pr.Name != "" {
		n, nf := outcome.Unpack(recordingspeakers.ParseSpeakerName(pr.Name))
		if nf != nil {
			return out.Err(nf)
		}
		name = opt.Some(n)
	}
	owner := recordingspeakers.OwnerMarkOther()
	if pr.Me {
		owner = recordingspeakers.OwnerMarkOwner()
	}
	mergedInto := opt.None[recordingspeakers.SpeakerID]()
	if pr.MergedInto != "" {
		t, tf := outcome.Unpack(recordingspeakers.ParseSpeakerID(pr.MergedInto))
		if tf != nil {
			return out.Err(tf)
		}
		mergedInto = opt.Some(t)
	}
	minor := recordingspeakers.MinorMarkNormal()
	if pr.Minor {
		minor = recordingspeakers.MinorMarkMinor()
	}
	return outcome.Widen(recordingspeakers.NewPersonBuilder(id).
		Naming(name, owner).
		Status(mergedInto, minor).
		Build())
}

func restoreEdits(er editsRecord) (out outcome.Of[recordingspeakers.EditCounts, fail.Fail]) {
	n, f := outcome.Unpack(recordingspeakers.ParseEditCount(er.Name))
	if f != nil {
		return out.Err(f)
	}
	m, f := outcome.Unpack(recordingspeakers.ParseEditCount(er.Merge))
	if f != nil {
		return out.Err(f)
	}
	u, f := outcome.Unpack(recordingspeakers.ParseEditCount(er.Unmerge))
	if f != nil {
		return out.Err(f)
	}
	c, f := outcome.Unpack(recordingspeakers.ParseEditCount(er.Clear))
	if f != nil {
		return out.Err(f)
	}
	return out.Ok(recordingspeakers.NewEditCounts(n, m, u, c))
}

// Store renders a Roster into its plain Record wire shape.
func Store(roster recordingspeakers.Roster) Record {
	var v rosterRecorder
	roster.Accept(&v)
	return v.record
}

// Log renders a Roster summary for a log line, and only for one.
func Log(roster recordingspeakers.Roster) string {
	var v rosterRecorder
	roster.Accept(&v)
	return fmt.Sprintf("speakers(next=%d,people=%d)", v.record.Next, len(v.record.People))
}

type rosterRecorder struct {
	record Record
	person personRecorder
	edits  editsRecorder
}

func (r *rosterRecorder) VisitNext(next recordingspeakers.SpeakerNumber) {
	r.record.Next = next.Wire()
}

func (r *rosterRecorder) VisitPerson(p recordingspeakers.Person) {
	r.person.out = personRecord{}
	p.Accept(&r.person)
	r.record.People = append(r.record.People, r.person.out)
}

func (r *rosterRecorder) VisitEdits(e recordingspeakers.EditCounts) {
	r.edits.out = editsRecord{}
	e.Accept(&r.edits)
	r.record.Edits = r.edits.out
}

type personRecorder struct {
	out   personRecord
	owner ownerBool
	minor minorBool
}

func (p *personRecorder) VisitPerson(id recordingspeakers.SpeakerID, name opt.Option[recordingspeakers.SpeakerName], owner recordingspeakers.OwnerMark, mergedInto opt.Option[recordingspeakers.SpeakerID]) {
	p.out.ID = id.Wire()
	name.Match(func(n recordingspeakers.SpeakerName) { p.out.Name = n.Wire() }, func() {})
	owner.Accept(&p.owner)
	p.out.Me = p.owner.isOwner
	mergedInto.Match(func(t recordingspeakers.SpeakerID) { p.out.MergedInto = t.Wire() }, func() {})
}

func (p *personRecorder) VisitMinor(minor recordingspeakers.MinorMark) {
	minor.Accept(&p.minor)
	p.out.Minor = p.minor.isMinor
}

type editsRecorder struct{ out editsRecord }

func (e *editsRecorder) VisitEditCounts(name, merge, unmerge, clear recordingspeakers.EditCount) {
	e.out = editsRecord{
		Name:    name.Wire(),
		Merge:   merge.Wire(),
		Unmerge: unmerge.Wire(),
		Clear:   clear.Wire(),
	}
}

type ownerBool struct{ isOwner bool }

func (o *ownerBool) VisitOther() { o.isOwner = false }
func (o *ownerBool) VisitOwner() { o.isOwner = true }

type minorBool struct{ isMinor bool }

func (m *minorBool) VisitNormal() { m.isMinor = false }
func (m *minorBool) VisitMinor()  { m.isMinor = true }

// Unexported wire helpers for shared fixture replay (used by fixtures_test.go).

func parseTimingWord(word string) recordingspeakers.Timing {
	switch word {
	case "exact":
		return recordingspeakers.TimingExact()
	case "turn":
		return recordingspeakers.TimingTurn()
	case "spread":
		return recordingspeakers.TimingSpread()
	}
	var unknown recordingspeakers.Timing
	return unknown
}

func parseQualityWord(word string) recordingspeakers.Quality {
	switch word {
	case "ok":
		return recordingspeakers.QualityOk()
	case "low":
		return recordingspeakers.QualityLow()
	}
	var unknown recordingspeakers.Quality
	return unknown
}

func parseSourceWord(word string) recordingspeakers.LineSource {
	switch word {
	case "hub":
		return recordingspeakers.LineSourceHub()
	case "device":
		return recordingspeakers.LineSourceDevice()
	}
	var unknown recordingspeakers.LineSource
	return unknown
}

type qualityWord struct{ word string }

func (q *qualityWord) VisitOk()  { q.word = "ok" }
func (q *qualityWord) VisitLow() { q.word = "low" }

type qualityReasonWord struct{ word string }

func (q *qualityReasonWord) VisitTooManyUnlabelled()     { q.word = "too_many_unlabelled" }
func (q *qualityReasonWord) VisitSingleSpeakerDominant() { q.word = "single_speaker_dominant" }
func (q *qualityReasonWord) VisitTooLittleSpeech()       { q.word = "too_little_speech" }
func (q *qualityReasonWord) VisitLowCoverage()           { q.word = "low_coverage" }
func (q *qualityReasonWord) VisitTooManyAttendees()      { q.word = "too_many_attendees" }

type skipReasonWord struct{ word string }

func (s *skipReasonWord) VisitUnreliableTiming() { s.word = "unreliable_timing" }
func (s *skipReasonWord) VisitHole()             { s.word = "hole" }
func (s *skipReasonWord) VisitEcho()             { s.word = "echo" }
func (s *skipReasonWord) VisitNoOverlap()        { s.word = "no_overlap" }

type certaintyBool struct{ unsure bool }

func (c *certaintyBool) VisitSure()   { c.unsure = false }
func (c *certaintyBool) VisitUnsure() { c.unsure = true }

type editOpWire struct {
	Op   string `json:"op"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Me   bool   `json:"me,omitempty"`
	From string `json:"from,omitempty"`
	Into string `json:"into,omitempty"`
}

func parseEditKindWord(word string) recordingspeakers.EditKind {
	switch word {
	case "name":
		return recordingspeakers.EditKindName()
	case "merge":
		return recordingspeakers.EditKindMerge()
	case "unmerge":
		return recordingspeakers.EditKindUnmerge()
	case "clear":
		return recordingspeakers.EditKindClear()
	}
	var unknown recordingspeakers.EditKind
	return unknown
}

func parseEditOps(ops []editOpWire) (out outcome.Of[recordingspeakers.EditOps, fail.Fail]) {
	list := make([]recordingspeakers.EditOp, 0, len(ops))
	for _, raw := range ops {
		_, kf := outcome.Unpack(recordingspeakers.CheckEditKind(parseEditKindWord(raw.Op)))
		if kf != nil {
			return out.Err(kf)
		}
		switch raw.Op {
		case "name":
			id, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(raw.ID))
			if f != nil {
				return out.Err(f)
			}
			name := opt.None[recordingspeakers.SpeakerName]()
			if strings.ContainsAny(raw.Name, "\n\r") || strings.TrimSpace(raw.Name) != "" {
				n, nf := outcome.Unpack(recordingspeakers.ParseSpeakerName(raw.Name))
				if nf != nil {
					return out.Err(nf)
				}
				name = opt.Some(n)
			}
			owner := recordingspeakers.OwnerMarkOther()
			if raw.Me {
				owner = recordingspeakers.OwnerMarkOwner()
			}
			list = append(list, recordingspeakers.EditOpName(id, name, owner))
		case "merge":
			from, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(raw.From))
			if f != nil {
				return out.Err(f)
			}
			into, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(raw.Into))
			if f != nil {
				return out.Err(f)
			}
			list = append(list, recordingspeakers.EditOpMerge(from, into))
		case "unmerge":
			id, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(raw.ID))
			if f != nil {
				return out.Err(f)
			}
			list = append(list, recordingspeakers.EditOpUnmerge(id))
		case "clear":
			list = append(list, recordingspeakers.EditOpClear())
		}
	}
	return out.Ok(recordingspeakers.NewEditOps(list))
}

type editOpsRecorder struct {
	ops   []editOpWire
	op    editOpRecorder
	owner ownerBool
}

func (e *editOpsRecorder) VisitEditOp(op recordingspeakers.EditOp) {
	e.op.out = editOpWire{}
	op.Accept(&e.op)
	e.ops = append(e.ops, e.op.out)
}

type editOpRecorder struct {
	out   editOpWire
	owner ownerBool
}

func (e *editOpRecorder) VisitName(id recordingspeakers.SpeakerID, name opt.Option[recordingspeakers.SpeakerName], owner recordingspeakers.OwnerMark) {
	owner.Accept(&e.owner)
	n := ""
	name.Match(func(sn recordingspeakers.SpeakerName) { n = sn.Wire() }, func() {})
	e.out = editOpWire{Op: "name", ID: id.Wire(), Name: n, Me: e.owner.isOwner}
}

func (e *editOpRecorder) VisitMerge(from, into recordingspeakers.SpeakerID) {
	e.out = editOpWire{Op: "merge", From: from.Wire(), Into: into.Wire()}
}

func (e *editOpRecorder) VisitUnmerge(id recordingspeakers.SpeakerID) {
	e.out = editOpWire{Op: "unmerge", ID: id.Wire()}
}

func (e *editOpRecorder) VisitClear() {
	e.out = editOpWire{Op: "clear"}
}
