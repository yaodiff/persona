package proto

// SessionLive is what a working Claude's status line says right now ("✻ Frosting… (16m 15s · ↓ 22.8k tokens ·
// thinking with xhigh effort)"), or the main-loop compaction Persona's plugin reports for a pane-less session. The
// node sends it while a client watches the session as the notification "session.live" {session_id, live}; the hub
// passes it to subscribers that asked for "session_live" as {type: "live", machine_id, session_id, live}. A nil live
// means the turn is over, compaction ended, or the line is gone (docs/protocol/protocol.md "Live status").
type SessionLive struct {
	// Verb is the spinner's word without the ellipsis ("Frosting").
	Verb string `json:"verb,omitempty"`
	// ElapsedS is how long this turn, or this compaction, has run in seconds.
	ElapsedS int `json:"elapsed_s,omitempty"`
	// TokensDown is the output tokens so far this turn ("↓ 22.8k tokens" is 22800).
	TokensDown int `json:"tokens_down,omitempty"`
	// Thinking: it is thinking now ("thinking with … effort").
	Thinking bool `json:"thinking,omitempty"`
	// ThoughtS: it just thought for this many seconds ("thought for 3s").
	ThoughtS int `json:"thought_s,omitempty"`
	// RetryError is the model request error Claude says it is retrying, without its countdown or attempt suffix.
	RetryError string `json:"retry_error,omitempty"`
	// RetryAttempt is the current model request attempt, as shown on screen.
	RetryAttempt int `json:"retry_attempt,omitempty"`
	// RetryMax is the maximum number of attempts, as shown on screen.
	RetryMax int `json:"retry_max,omitempty"`
	// At is when the node read it (RFC 3339).
	At string `json:"at,omitempty"`
}

// FeatureSessionLive is the client feature (the WebSocket's features=…) that asks for live pushes.
const FeatureSessionLive = "session_live"

// LiveTool is a provider-recorded invocation awaiting a result. Its ID belongs to
// the provider call, which may wrap several commands with independent item IDs.
type LiveTool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const FeatureSessionLiveTools = "session_live_tools"

// LiveToolState carries public activity for one watched turn. An empty state clears the previous one.
type LiveToolState struct {
	Tools        []LiveTool `json:"tools"`
	Summary      string     `json:"summary,omitempty"`
	Thinking     bool       `json:"thinking,omitempty"`
	Active       bool       `json:"active,omitempty"`
	RetryAttempt int        `json:"retry_attempt,omitempty"`
	RetryMax     int        `json:"retry_max,omitempty"`
	Compacting   bool       `json:"compacting,omitempty"`
}
