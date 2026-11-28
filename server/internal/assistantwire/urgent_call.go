package assistantwire

import "github.com/nwverse/persona/server/internal/buswire"

// MethodCallRing is call.ring {call} → CallRingResult: a manager has the hub ring the owner's phones for an urgent call
// (docs/assistant/urgent-call.md). The manager decided the call may ring; the hub only delivers it, as a VoIP push to
// every phone registered for calls, and answers once APNs or the relay took or refused each one.
const MethodCallRing = "call.ring"

// CallRingMethod binds an urgent call ring to its delivery result.
var CallRingMethod = buswire.Method[CallRingRequest, CallRingResult]{Name: MethodCallRing}

// EventToolsChanged is assistant.tools_changed (one way, a manager → the hub): its MCP tools changed (urgent_call comes
// and goes with the owner's switch), so the hub asks for them again. Conversations started after that list the new
// ones.
const EventToolsChanged = "assistant.tools_changed"

// CallRing is one ring of an urgent call. The phone gets it sealed and names the call from it.
type CallRing struct {
	ID             string `json:"id"`              // the call's id: the phone asks the manager for the matter by it
	Title          string `json:"title"`           // what the lock screen shows, a few words
	ConversationID string `json:"conversation_id"` // the matter's conversation, where the voice call talks
	Attempt        int    `json:"attempt"`         // 1 the first ring, 2 the retry after a missed one
	// TTL is how many seconds APNs keeps trying to deliver it (0: the relay's default, 30); a phone that was offline
	// longer is not rung.
	TTL int `json:"ttl,omitempty"`
}

// CallRingRequest is call.ring's params.
type CallRingRequest struct {
	Call CallRing `json:"call"`
}

// CallRingResult is call.ring's answer: how many phones APNs (or the relay) took the ring for. With none, Reason says
// why: CallNoPhone (no phone registered for calls) or CallRefused (every push was refused or failed); the manager
// then sends the matter as a P0 push instead.
type CallRingResult struct {
	Devices int    `json:"devices"`
	Reason  string `json:"reason,omitempty"`
}

// Reasons a call.ring rang no phone.
const (
	CallNoPhone = "no_phone"
	CallRefused = "refused"
)

// urgentCallRoutes are the urgent calls' routes (docs/assistant/urgent-call.md): the settings, a test call, and a
// call as the phone reads and reports it. The manager keeps them; the hub relays them like Routes. The apps learn where
// a call is from the urgent_call message (ws.broadcast).
var urgentCallRoutes = []string{
	"GET /api/v1/assistant/urgent-call",
	"PUT /api/v1/assistant/urgent-call",
	"POST /api/v1/assistant/urgent-call/test",
	"GET /api/v1/assistant/calls",
	"GET /api/v1/assistant/calls/{id}",
	"POST /api/v1/assistant/calls/{id}/state",
}

func init() { Routes = append(Routes, urgentCallRoutes...) }
