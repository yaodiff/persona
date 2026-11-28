package recordingspeakers

import "github.com/nwverse/persona/server/internal/domainkit/defect"

// State is a state as recordingspeakers knows it: waiting, ready, failed or cleared. The zero State is no state; only
// its constructors make one, and Accept raises a defect on it.
type State struct{ state uint8 }

// StateWaiting is the waiting state.
func StateWaiting() State { return State{state: 1} }

// StateReady is the ready state.
func StateReady() State { return State{state: 2} }

// StateFailed is the failed state.
func StateFailed() State { return State{state: 3} }

// StateCleared is the cleared state.
func StateCleared() State { return State{state: 4} }

// StateVisitor is told which state it is.
type StateVisitor interface {
	VisitWaiting()
	VisitReady()
	VisitFailed()
	VisitCleared()
}

// Accept tells v which state it is; the zero State is a defect.
func (x State) Accept(v StateVisitor) {
	switch x.state {
	case 1:
		v.VisitWaiting()
	case 2:
		v.VisitReady()
	case 3:
		v.VisitFailed()
	case 4:
		v.VisitCleared()
	default:
		defect.Raise("recordingspeakers.State.Accept", "the zero State")
	}
}

// FailReason is a fail reason as recordingspeakers knows it: engine unavailable, no audio, too long, timeout, gave up,
// audio gone or error. The zero FailReason is no fail reason; only its constructors make one, and Accept raises a
// defect on it.
type FailReason struct{ failReason uint8 }

// FailReasonEngineUnavailable is the engine unavailable fail reason.
func FailReasonEngineUnavailable() FailReason { return FailReason{failReason: 1} }

// FailReasonNoAudio is the no audio fail reason.
func FailReasonNoAudio() FailReason { return FailReason{failReason: 2} }

// FailReasonTooLong is the too long fail reason.
func FailReasonTooLong() FailReason { return FailReason{failReason: 3} }

// FailReasonTimeout is the timeout fail reason.
func FailReasonTimeout() FailReason { return FailReason{failReason: 4} }

// FailReasonGaveUp is the gave up fail reason.
func FailReasonGaveUp() FailReason { return FailReason{failReason: 5} }

// FailReasonAudioGone is the audio gone fail reason.
func FailReasonAudioGone() FailReason { return FailReason{failReason: 6} }

// FailReasonError is the error fail reason.
func FailReasonError() FailReason { return FailReason{failReason: 7} }

// FailReasonVisitor is told which fail reason it is.
type FailReasonVisitor interface {
	VisitEngineUnavailable()
	VisitNoAudio()
	VisitTooLong()
	VisitTimeout()
	VisitGaveUp()
	VisitAudioGone()
	VisitError()
}

// Accept tells v which fail reason it is; the zero FailReason is a defect.
func (x FailReason) Accept(v FailReasonVisitor) {
	switch x.failReason {
	case 1:
		v.VisitEngineUnavailable()
	case 2:
		v.VisitNoAudio()
	case 3:
		v.VisitTooLong()
	case 4:
		v.VisitTimeout()
	case 5:
		v.VisitGaveUp()
	case 6:
		v.VisitAudioGone()
	case 7:
		v.VisitError()
	default:
		defect.Raise("recordingspeakers.FailReason.Accept", "the zero FailReason")
	}
}

// Quality is a quality as recordingspeakers knows it: ok or low. The zero Quality is no quality; only its constructors
// make one, and Accept raises a defect on it.
type Quality struct{ quality uint8 }

// QualityOk is the ok quality.
func QualityOk() Quality { return Quality{quality: 1} }

// QualityLow is the low quality.
func QualityLow() Quality { return Quality{quality: 2} }

// QualityVisitor is told which quality it is.
type QualityVisitor interface {
	VisitOk()
	VisitLow()
}

// Accept tells v which quality it is; the zero Quality is a defect.
func (q Quality) Accept(v QualityVisitor) {
	switch q.quality {
	case 1:
		v.VisitOk()
	case 2:
		v.VisitLow()
	default:
		defect.Raise("recordingspeakers.Quality.Accept", "the zero Quality")
	}
}

// QualityReason is a quality reason as recordingspeakers knows it: too many unlabelled, single speaker dominant, too
// little speech, low coverage or too many attendees. The zero QualityReason is no quality reason; only its constructors
// make one, and Accept raises a defect on it.
type QualityReason struct{ qualityReason uint8 }

// QualityReasonTooManyUnlabelled is the too many unlabelled quality reason.
func QualityReasonTooManyUnlabelled() QualityReason { return QualityReason{qualityReason: 1} }

// QualityReasonSingleSpeakerDominant is the single speaker dominant quality reason.
func QualityReasonSingleSpeakerDominant() QualityReason { return QualityReason{qualityReason: 2} }

// QualityReasonTooLittleSpeech is the too little speech quality reason.
func QualityReasonTooLittleSpeech() QualityReason { return QualityReason{qualityReason: 3} }

// QualityReasonLowCoverage is the low coverage quality reason.
func QualityReasonLowCoverage() QualityReason { return QualityReason{qualityReason: 4} }

// QualityReasonTooManyAttendees is the too many attendees quality reason.
func QualityReasonTooManyAttendees() QualityReason { return QualityReason{qualityReason: 5} }

// QualityReasonVisitor is told which quality reason it is.
type QualityReasonVisitor interface {
	VisitTooManyUnlabelled()
	VisitSingleSpeakerDominant()
	VisitTooLittleSpeech()
	VisitLowCoverage()
	VisitTooManyAttendees()
}

// Accept tells v which quality reason it is; the zero QualityReason is a defect.
func (q QualityReason) Accept(v QualityReasonVisitor) {
	switch q.qualityReason {
	case 1:
		v.VisitTooManyUnlabelled()
	case 2:
		v.VisitSingleSpeakerDominant()
	case 3:
		v.VisitTooLittleSpeech()
	case 4:
		v.VisitLowCoverage()
	case 5:
		v.VisitTooManyAttendees()
	default:
		defect.Raise("recordingspeakers.QualityReason.Accept", "the zero QualityReason")
	}
}

// Timing is a timing as recordingspeakers knows it: exact, turn or spread. The zero Timing is no timing; only its
// constructors make one, and Accept raises a defect on it.
type Timing struct{ timing uint8 }

// TimingExact is the exact timing.
func TimingExact() Timing { return Timing{timing: 1} }

// TimingTurn is the turn timing.
func TimingTurn() Timing { return Timing{timing: 2} }

// TimingSpread is the spread timing.
func TimingSpread() Timing { return Timing{timing: 3} }

// TimingVisitor is told which timing it is.
type TimingVisitor interface {
	VisitExact()
	VisitTurn()
	VisitSpread()
}

// Accept tells v which timing it is; the zero Timing is a defect.
func (x Timing) Accept(v TimingVisitor) {
	switch x.timing {
	case 1:
		v.VisitExact()
	case 2:
		v.VisitTurn()
	case 3:
		v.VisitSpread()
	default:
		defect.Raise("recordingspeakers.Timing.Accept", "the zero Timing")
	}
}

// LineSource is a line source as recordingspeakers knows it: hub or device. The zero LineSource is no line source; only
// its constructors make one, and Accept raises a defect on it.
type LineSource struct{ lineSource uint8 }

// LineSourceHub is the hub line source.
func LineSourceHub() LineSource { return LineSource{lineSource: 1} }

// LineSourceDevice is the device line source.
func LineSourceDevice() LineSource { return LineSource{lineSource: 2} }

// LineSourceVisitor is told which line source it is.
type LineSourceVisitor interface {
	VisitHub()
	VisitDevice()
}

// Accept tells v which line source it is; the zero LineSource is a defect.
func (l LineSource) Accept(v LineSourceVisitor) {
	switch l.lineSource {
	case 1:
		v.VisitHub()
	case 2:
		v.VisitDevice()
	default:
		defect.Raise("recordingspeakers.LineSource.Accept", "the zero LineSource")
	}
}

// Echo is an echo as recordingspeakers knows it: speech or persona. The zero Echo is no echo; only its constructors
// make one, and Accept raises a defect on it.
type Echo struct{ echo uint8 }

// EchoSpeech is the speech echo.
func EchoSpeech() Echo { return Echo{echo: 1} }

// EchoPersona is the persona echo.
func EchoPersona() Echo { return Echo{echo: 2} }

// EchoVisitor is told which echo it is.
type EchoVisitor interface {
	VisitSpeech()
	VisitPersona()
}

// Accept tells v which echo it is; the zero Echo is a defect.
func (x Echo) Accept(v EchoVisitor) {
	switch x.echo {
	case 1:
		v.VisitSpeech()
	case 2:
		v.VisitPersona()
	default:
		defect.Raise("recordingspeakers.Echo.Accept", "the zero Echo")
	}
}

// OwnerMark is an owner mark as recordingspeakers knows it: other or owner. The zero OwnerMark is no owner mark; only
// its constructors make one, and Accept raises a defect on it.
type OwnerMark struct{ ownerMark uint8 }

// OwnerMarkOther is the other owner mark.
func OwnerMarkOther() OwnerMark { return OwnerMark{ownerMark: 1} }

// OwnerMarkOwner is the owner owner mark.
func OwnerMarkOwner() OwnerMark { return OwnerMark{ownerMark: 2} }

// OwnerMarkVisitor is told which owner mark it is.
type OwnerMarkVisitor interface {
	VisitOther()
	VisitOwner()
}

// Accept tells v which owner mark it is; the zero OwnerMark is a defect.
func (o OwnerMark) Accept(v OwnerMarkVisitor) {
	switch o.ownerMark {
	case 1:
		v.VisitOther()
	case 2:
		v.VisitOwner()
	default:
		defect.Raise("recordingspeakers.OwnerMark.Accept", "the zero OwnerMark")
	}
}

// MinorMark is a minor mark as recordingspeakers knows it: normal or minor. The zero MinorMark is no minor mark; only
// its constructors make one, and Accept raises a defect on it.
type MinorMark struct{ minorMark uint8 }

// MinorMarkNormal is the normal minor mark.
func MinorMarkNormal() MinorMark { return MinorMark{minorMark: 1} }

// MinorMarkMinor is the minor minor mark.
func MinorMarkMinor() MinorMark { return MinorMark{minorMark: 2} }

// MinorMarkVisitor is told which minor mark it is.
type MinorMarkVisitor interface {
	VisitNormal()
	VisitMinor()
}

// Accept tells v which minor mark it is; the zero MinorMark is a defect.
func (m MinorMark) Accept(v MinorMarkVisitor) {
	switch m.minorMark {
	case 1:
		v.VisitNormal()
	case 2:
		v.VisitMinor()
	default:
		defect.Raise("recordingspeakers.MinorMark.Accept", "the zero MinorMark")
	}
}

// Certainty is a certainty as recordingspeakers knows it: sure or unsure. The zero Certainty is no certainty; only its
// constructors make one, and Accept raises a defect on it.
type Certainty struct{ certainty uint8 }

// CertaintySure is the sure certainty.
func CertaintySure() Certainty { return Certainty{certainty: 1} }

// CertaintyUnsure is the unsure certainty.
func CertaintyUnsure() Certainty { return Certainty{certainty: 2} }

// CertaintyVisitor is told which certainty it is.
type CertaintyVisitor interface {
	VisitSure()
	VisitUnsure()
}

// Accept tells v which certainty it is; the zero Certainty is a defect.
func (c Certainty) Accept(v CertaintyVisitor) {
	switch c.certainty {
	case 1:
		v.VisitSure()
	case 2:
		v.VisitUnsure()
	default:
		defect.Raise("recordingspeakers.Certainty.Accept", "the zero Certainty")
	}
}

// SkipReason is a skip reason as recordingspeakers knows it: unreliable timing, hole, echo or no overlap.
// The zero SkipReason is no skip reason; only its constructors make one, and Accept raises a defect on it.
type SkipReason struct{ skipReason uint8 }

// SkipReasonUnreliableTiming is the unreliable timing skip reason.
func SkipReasonUnreliableTiming() SkipReason { return SkipReason{skipReason: 1} }

// SkipReasonHole is the hole skip reason.
func SkipReasonHole() SkipReason { return SkipReason{skipReason: 2} }

// SkipReasonEcho is the echo skip reason.
func SkipReasonEcho() SkipReason { return SkipReason{skipReason: 3} }

// SkipReasonNoOverlap is the no overlap skip reason.
func SkipReasonNoOverlap() SkipReason { return SkipReason{skipReason: 4} }

// SkipReasonVisitor is told which skip reason it is.
type SkipReasonVisitor interface {
	VisitUnreliableTiming()
	VisitHole()
	VisitEcho()
	VisitNoOverlap()
}

// Accept tells v which skip reason it is; the zero SkipReason is a defect.
func (x SkipReason) Accept(v SkipReasonVisitor) {
	switch x.skipReason {
	case 1:
		v.VisitUnreliableTiming()
	case 2:
		v.VisitHole()
	case 3:
		v.VisitEcho()
	case 4:
		v.VisitNoOverlap()
	default:
		defect.Raise("recordingspeakers.SkipReason.Accept", "the zero SkipReason")
	}
}

// EditKind is an edit kind as recordingspeakers knows it: name, merge, unmerge or clear. The zero EditKind is no edit
// kind; only its constructors make one, and Accept raises a defect on it.
type EditKind struct{ editKind uint8 }

// EditKindName is the name edit kind.
func EditKindName() EditKind { return EditKind{editKind: 1} }

// EditKindMerge is the merge edit kind.
func EditKindMerge() EditKind { return EditKind{editKind: 2} }

// EditKindUnmerge is the unmerge edit kind.
func EditKindUnmerge() EditKind { return EditKind{editKind: 3} }

// EditKindClear is the clear edit kind.
func EditKindClear() EditKind { return EditKind{editKind: 4} }

// EditKindVisitor is told which edit kind it is.
type EditKindVisitor interface {
	VisitName()
	VisitMerge()
	VisitUnmerge()
	VisitClear()
}

// Accept tells v which edit kind it is; the zero EditKind is a defect.
func (x EditKind) Accept(v EditKindVisitor) {
	switch x.editKind {
	case 1:
		v.VisitName()
	case 2:
		v.VisitMerge()
	case 3:
		v.VisitUnmerge()
	case 4:
		v.VisitClear()
	default:
		defect.Raise("recordingspeakers.EditKind.Accept", "the zero EditKind")
	}
}
