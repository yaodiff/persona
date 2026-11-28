package proto

// AgentPreferenceEventType identifies which preferences a client should reload.
type AgentPreferenceEventType string

const (
	PinsEventType  AgentPreferenceEventType = "pins"
	MutesEventType AgentPreferenceEventType = "mutes"
)

// AgentPreferenceEvent tells clients to reload their pins or mutes.
type AgentPreferenceEvent struct {
	Type AgentPreferenceEventType `json:"type"`
}
