package recordingspeakers

import "github.com/nwverse/persona/server/internal/domainkit/fail"

// Fail is every failure recordingspeakers returns. The zero Fail is nil; no operation returns it as a failure.
type Fail interface {
	fail.Fail
	recordingspeakersFail()
}

// invalid is input recordingspeakers refuses: every part that broke a rule, in order.
type invalid struct{ fail.Invalids }

func (invalid) recordingspeakersFail() {}

// refused is an operation the speaker roster's rules turn down.
type refused struct{ fail.Refusal }

func (refused) recordingspeakersFail() {}

// missing is a speaker id an operation names that is not in the roster.
type missing struct{ fail.Absence }

func (missing) recordingspeakersFail() {}

// wrong adds the part to parts unless ok holds: each rule adds its own, so a caller reports every wrong part at once. A
// part names the input in this package's words, never a wire field's.
func wrong(parts []fail.Invalid, ok bool, code, part string) []fail.Invalid {
	if ok {
		return parts
	}
	return append(parts, fail.NewInvalid(fail.NewCode(code), fail.NewPart(part)))
}

// invalidParts is the failure for the parts a rule refused; parts is never empty.
func invalidParts(parts []fail.Invalid) Fail {
	all := fail.NewInvalids(parts[0])
	for _, p := range parts[1:] {
		all = all.With(p)
	}
	return invalid{all}
}

// refusedCode is a refusal failure for code.
func refusedCode(code string) Fail {
	return refused{fail.NewRefusal(fail.NewCode(code))}
}

// missingCode is a missing failure for code.
func missingCode(code string) Fail {
	return missing{fail.NewAbsence(fail.NewCode(code))}
}
