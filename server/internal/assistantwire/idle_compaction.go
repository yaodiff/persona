package assistantwire

// IdleCompaction applies only to unattended messages in the main conversation.
// A missing policy keeps older managers' delivery behavior unchanged.
//
// ReleaseMinutes and ReleaseTokens are for the node letting the main conversation's idle process go (#12008): how long
// it is kept after its last activity, and the context at which it is compacted first. 0 (left out) keeps the node's own
// rule: 20 minutes, and 75% of the conversation's window. An older node ignores both.
type IdleCompaction struct {
	Enabled        bool `json:"enabled"`
	IdleMinutes    int  `json:"idle_minutes"`
	ContextTokens  int  `json:"context_tokens"`
	ReleaseMinutes int  `json:"release_minutes,omitempty"`
	ReleaseTokens  int  `json:"release_tokens,omitempty"`
}

// The ranges ReleaseMinutes and ReleaseTokens are taken in; a value outside them is treated as 0. Releases stay below
// the prompt cache's one-hour lifetime, so the /compact before a release still reads the cache; the reaper looks once a
// minute, which the margin covers.
const (
	MinReleaseMinutes = 5
	MaxReleaseMinutes = 55
	MinReleaseTokens  = 10000
	MaxReleaseTokens  = 10000000
)

// ReleaseMinutesValid and ReleaseTokensValid say whether a value is in its range.
func ReleaseMinutesValid(m int) bool { return m >= MinReleaseMinutes && m <= MaxReleaseMinutes }
func ReleaseTokensValid(t int) bool  { return t >= MinReleaseTokens && t <= MaxReleaseTokens }

// AgyIdleCompaction controls proactive idle auto-compaction for Persona-managed Antigravity sessions (node.configure
// agy_idle_compaction; agy_idle_compact.go in the node). Enabled is the switch for all of them. IdleMinutes and
// ContextTokens are the task Agents' thresholds (assistant.agy_idle_auto_compact_*).
//
// Assistant conversations (the main chat, the leaders' and the other assistant chats) follow the assistant's own idle
// compaction settings instead (#17260): AssistantOff is assistant.idle_auto_compact turned off, and
// AssistantIdleMinutes and AssistantContextTokens are assistant.idle_auto_compact_minutes and _tokens. They are left out
// while unset on the wire (an older manager): the node then keeps the same 30 minutes and 300,000 tokens the assistant
// defaults to, never the task Agents' tighter ones, and an older node ignores them.
type AgyIdleCompaction struct {
	Enabled                bool `json:"enabled"`
	IdleMinutes            int  `json:"idle_minutes"`
	ContextTokens          int  `json:"context_tokens"`
	AssistantOff           bool `json:"assistant_off,omitempty"`
	AssistantIdleMinutes   int  `json:"assistant_idle_minutes,omitempty"`
	AssistantContextTokens int  `json:"assistant_context_tokens,omitempty"`
}

// AgyIdleThresholds is what one session is held to: AgyIdleCompaction.For picks the assistant's or the task Agents'.
type AgyIdleThresholds struct {
	Enabled       bool
	IdleMinutes   int
	ContextTokens int
}

// Defaults and bounds for idle auto-compaction. Task agents and assistant conversations share the same bounds.
const (
	DefaultAgyIdleAutoCompactMinutes = 10
	MinAgyIdleAutoCompactMinutes     = 1
	MaxAgyIdleAutoCompactMinutes     = 1440
	DefaultAgyIdleAutoCompactTokens  = 200000
	MinAgyIdleAutoCompactTokens      = 10000
	MaxAgyIdleAutoCompactTokens      = 10000000

	// assistant.idle_auto_compact_minutes and assistant.idle_auto_compact_tokens when unset.
	DefaultAssistantIdleAutoCompactMinutes = 30
	DefaultAssistantIdleAutoCompactTokens  = 300000
)

// DefaultAgyIdleCompaction returns the default policy when no override has been configured.
func DefaultAgyIdleCompaction() AgyIdleCompaction {
	return AgyIdleCompaction{
		Enabled:                true,
		IdleMinutes:            DefaultAgyIdleAutoCompactMinutes,
		ContextTokens:          DefaultAgyIdleAutoCompactTokens,
		AssistantIdleMinutes:   DefaultAssistantIdleAutoCompactMinutes,
		AssistantContextTokens: DefaultAssistantIdleAutoCompactTokens,
	}
}

// AgyIdleMinutesValid and AgyIdleTokensValid report whether an idle compaction threshold is within range.
func AgyIdleMinutesValid(m int) bool {
	return m >= MinAgyIdleAutoCompactMinutes && m <= MaxAgyIdleAutoCompactMinutes
}
func AgyIdleTokensValid(t int) bool {
	return t >= MinAgyIdleAutoCompactTokens && t <= MaxAgyIdleAutoCompactTokens
}

// Normalize replaces any out-of-range or missing thresholds with their defaults while preserving the switches.
func (c AgyIdleCompaction) Normalize() AgyIdleCompaction {
	if !AgyIdleMinutesValid(c.IdleMinutes) {
		c.IdleMinutes = DefaultAgyIdleAutoCompactMinutes
	}
	if !AgyIdleTokensValid(c.ContextTokens) {
		c.ContextTokens = DefaultAgyIdleAutoCompactTokens
	}
	if !AgyIdleMinutesValid(c.AssistantIdleMinutes) {
		c.AssistantIdleMinutes = DefaultAssistantIdleAutoCompactMinutes
	}
	if !AgyIdleTokensValid(c.AssistantContextTokens) {
		c.AssistantContextTokens = DefaultAssistantIdleAutoCompactTokens
	}
	return c
}

// For returns the thresholds an assistant conversation (assistant true) or a task Agent is held to.
func (c AgyIdleCompaction) For(assistant bool) AgyIdleThresholds {
	c = c.Normalize()
	if assistant {
		return AgyIdleThresholds{Enabled: c.Enabled && !c.AssistantOff, IdleMinutes: c.AssistantIdleMinutes, ContextTokens: c.AssistantContextTokens}
	}
	return AgyIdleThresholds{Enabled: c.Enabled, IdleMinutes: c.IdleMinutes, ContextTokens: c.ContextTokens}
}
