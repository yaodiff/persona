package recordingspeakers

import (
	"slices"

	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
)

// Claim is the speaker attribution inputs on one disagreement citation: optional speaker id, its certainty (when a
// speaker is present), and optional transcript/calendar who name written by the model. The zero Claim is no speaker
// and no who.
type Claim struct {
	speaker   opt.Option[SpeakerID]
	certainty Certainty
	who       opt.Option[SpeakerName]
}

// ClaimVisitor is told a claim's speaker, certainty and who.
type ClaimVisitor interface {
	VisitClaim(speaker opt.Option[SpeakerID], certainty Certainty, who opt.Option[SpeakerName])
}

// NewClaim validates and constructs a Claim. When speaker is present, certainty must be non-zero (sure or unsure); when
// speaker is absent, certainty is ignored and stored as CertaintyUnsure.
func NewClaim(speaker opt.Option[SpeakerID], certainty Certainty, who opt.Option[SpeakerName]) (out outcome.Of[Claim, Fail]) {
	var parts []fail.Invalid
	effCertainty := CertaintyUnsure()
	speaker.Match(func(id SpeakerID) {
		parts = wrong(parts, id != (SpeakerID{}), "recordingspeakers.claim.zero_speaker", "speaker")
		parts = wrong(parts, certainty != (Certainty{}), "recordingspeakers.claim.no_certainty", "certainty")
		effCertainty = certainty
	}, func() {})
	who.Match(func(w SpeakerName) {
		parts = wrong(parts, w != (SpeakerName{}), "recordingspeakers.claim.zero_who", "who")
	}, func() {})
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(Claim{speaker: speaker, certainty: effCertainty, who: who})
}

// Accept tells v the claim's parts.
func (c Claim) Accept(v ClaimVisitor) {
	cert := c.certainty
	if cert == (Certainty{}) {
		cert = CertaintyUnsure()
	}
	v.VisitClaim(c.speaker, cert, c.who)
}

// AlsoClaims is the ordered list of additional supporting claims (`also`) on a disagreement position.
// The zero AlsoClaims is empty.
type AlsoClaims struct{ alsoClaims []Claim }

// AlsoClaimsVisitor is told each supporting claim in order.
type AlsoClaimsVisitor interface {
	VisitClaim(claim Claim)
}

// NewAlsoClaims copies list into AlsoClaims.
func NewAlsoClaims(list []Claim) AlsoClaims {
	return AlsoClaims{alsoClaims: slices.Clone(list)}
}

// Accept tells v each supporting claim in order.
func (a AlsoClaims) Accept(v AlsoClaimsVisitor) {
	for i := range a.alsoClaims {
		v.VisitClaim(a.alsoClaims[i])
	}
}

// Display is how a disagreement position's speaker is shown, following the four-row display-name rule: an owner-chosen
// name, a speaker number N (localized by the caller as "Speaker N" / "发言人 N" / "話者 N"), the model's `who`, or no
// attribution at all. The zero Display is nil; only its constructors make one.
type Display interface {
	Accept(v DisplayVisitor)
	display()
}

// DisplayVisitor is told which display form applies.
type DisplayVisitor interface {
	VisitNamed(name SpeakerName)
	VisitNumbered(number SpeakerNumber)
	VisitWho(who SpeakerName)
	VisitNone()
}

type displayNamed struct{ name SpeakerName }
type displayNumbered struct{ number SpeakerNumber }
type displayWho struct{ who SpeakerName }
type displayNone struct{}

// DisplayNamed is row 1: a sure speaker the owner named. A zero name is a defect.
func DisplayNamed(name SpeakerName) Display {
	if name == (SpeakerName{}) {
		defect.Raise("recordingspeakers.DisplayNamed", "the zero SpeakerName")
	}
	return displayNamed{name: name}
}

// DisplayNumbered is row 2: a sure speaker not named by the owner, returning its positive speaker number N. A zero
// number is a defect.
func DisplayNumbered(number SpeakerNumber) Display {
	if number == (SpeakerNumber{}) {
		defect.Raise("recordingspeakers.DisplayNumbered", "the zero SpeakerNumber")
	}
	return displayNumbered{number: number}
}

// DisplayWho is row 3: the model's `who` when there is no sure speaker. A zero name is a defect.
func DisplayWho(who SpeakerName) Display {
	if who == (SpeakerName{}) {
		defect.Raise("recordingspeakers.DisplayWho", "the zero SpeakerName")
	}
	return displayWho{who: who}
}

// DisplayNone is row 4: no attribution and no hint.
func DisplayNone() Display { return displayNone{} }

func (d displayNamed) Accept(v DisplayVisitor)    { v.VisitNamed(d.name) }
func (d displayNumbered) Accept(v DisplayVisitor) { v.VisitNumbered(d.number) }
func (d displayWho) Accept(v DisplayVisitor)      { v.VisitWho(d.who) }
func (displayNone) Accept(v DisplayVisitor)       { v.VisitNone() }
func (displayNamed) display()                     {}
func (displayNumbered) display()                  {}
func (displayWho) display()                       {}
func (displayNone) display()                      {}

// Displays is the ordered list of display attributions for a disagreement position (main plus any deduplicated sure
// `also` supporters). Empty means no attribution is shown. The zero Displays is empty.
type Displays struct{ displays []Display }

// DisplaysVisitor is told each display entry in order.
type DisplaysVisitor interface {
	VisitDisplay(display Display)
}

// IsEmpty says whether d holds no display labels.
func (d Displays) IsEmpty() bool { return len(d.displays) == 0 }

// Accept tells v each display entry in order.
func (d Displays) Accept(v DisplaysVisitor) {
	for i := range d.displays {
		v.VisitDisplay(d.displays[i])
	}
}

// ResolveDisplay applies the four-row display-name table to one claim against roster:
//  1. a sure speaker the owner named -> DisplayNamed
//  2. a sure speaker not named -> DisplayNumbered(N)
//  3. otherwise, if who is present -> DisplayWho
//  4. otherwise -> DisplayNone
func ResolveDisplay(claim Claim, roster Roster) Display {
	if d, ok := resolveSureSpeaker(claim, roster); ok {
		return d
	}
	var out Display = DisplayNone()
	claim.who.Match(func(w SpeakerName) {
		out = DisplayWho(w)
	}, func() {})
	return out
}

// ResolvePositionDisplay resolves the display labels for a disagreement position with optional `also` supporters.
// Sure speakers from main and `also` are resolved via rows 1-2 and deduplicated by canonical speaker id; when main has
// no sure speaker, its `who` (row 3) is included unless an `also` sure speaker already carries that exact name.
func ResolvePositionDisplay(main Claim, also AlsoClaims, roster Roster) Displays {
	seenIDs := make(map[SpeakerID]bool)
	namedSeen := make(map[SpeakerName]bool)
	var out []Display

	// First resolve any sure speaker on main.
	mainHadSure := false
	if id, ok := sureCanonicalSpeaker(main, roster); ok {
		mainHadSure = true
		seenIDs[id] = true
		d, named := displayForSpeaker(id, roster)
		if named != (SpeakerName{}) {
			namedSeen[named] = true
		}
		out = append(out, d)
	}

	// Collect deduplicated sure speakers from `also`.
	var alsoOut []Display
	for _, ac := range also.alsoClaims {
		id, ok := sureCanonicalSpeaker(ac, roster)
		if !ok || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		d, named := displayForSpeaker(id, roster)
		if named != (SpeakerName{}) {
			namedSeen[named] = true
		}
		alsoOut = append(alsoOut, d)
	}

	// If main had no sure speaker, apply row 3 (who) unless already represented by a sure `also` speaker of the same name.
	if !mainHadSure {
		main.who.Match(func(w SpeakerName) {
			if !namedSeen[w] {
				out = append(out, DisplayWho(w))
			}
		}, func() {})
	}
	out = append(out, alsoOut...)
	return Displays{displays: out}
}

func resolveSureSpeaker(claim Claim, roster Roster) (Display, bool) {
	id, ok := sureCanonicalSpeaker(claim, roster)
	if !ok {
		return nil, false
	}
	d, named := displayForSpeaker(id, roster)
	if named == (SpeakerName{}) {
		return d, true
	}
	return d, true
}

func sureCanonicalSpeaker(claim Claim, roster Roster) (SpeakerID, bool) {
	if claim.speaker.IsNone() || claim.certainty != CertaintySure() {
		return SpeakerID{}, false
	}
	var rawID SpeakerID
	claim.speaker.Match(func(id SpeakerID) { rawID = id }, func() {})
	return canonicalID(roster.people, rawID), true
}

func displayForSpeaker(canon SpeakerID, roster Roster) (Display, SpeakerName) {
	idx := indexOfPerson(roster.people, canon)
	if idx >= 0 {
		var found SpeakerName
		roster.people[idx].name.Match(func(n SpeakerName) { found = n }, func() {})
		if found != (SpeakerName{}) {
			return DisplayNamed(found), found
		}
	}
	return DisplayNumbered(SpeakerNumberOf(canon)), SpeakerName{}
}
