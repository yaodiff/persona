package assistantwire

// WorkMode describes how a task is carried out. The zero value is absent on a legacy brief.
// A value from a newer peer round-trips as unknown, with Valid returning false.
type WorkMode string

const (
	WorkModeExecute    WorkMode = "execute"
	WorkModeResearch   WorkMode = "research"
	WorkModeCoordinate WorkMode = "coordinate"
)

// Valid reports whether m is a supported work mode.
func (m WorkMode) Valid() bool {
	switch m {
	case WorkModeExecute, WorkModeResearch, WorkModeCoordinate:
		return true
	default:
		return false
	}
}

// CompletionMode describes the required depth of delivery. The zero value keeps legacy closeout rules.
// A value from a newer peer round-trips as unknown, with Valid returning false.
type CompletionMode string

const (
	CompletionModeInherit  CompletionMode = "inherit"
	CompletionModeDraft    CompletionMode = "draft"
	CompletionModeReady    CompletionMode = "ready"
	CompletionModeLanded   CompletionMode = "landed"
	CompletionModeVerified CompletionMode = "verified"
)

// Valid reports whether m is a supported completion mode.
func (m CompletionMode) Valid() bool {
	switch m {
	case CompletionModeInherit, CompletionModeDraft, CompletionModeReady, CompletionModeLanded, CompletionModeVerified:
		return true
	default:
		return false
	}
}
