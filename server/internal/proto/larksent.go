package proto

import (
	"encoding/json"
	"strings"
)

// MethodLarkSent is the node's request that tells the manager of a write its gate proxy forwarded and the API accepted
// (node/gateproxy.go), so the manager keeps it in the sent-as-user ledger (docs/extensions/connectors.md "Sent as the user").
const MethodLarkSent = "lark.sent"

// How the node's gate let a write through (LarkSent.LetThrough.Kind), in the ledger's terms.
const (
	LarkThroughRule   = "rule"                // a rule of the gate allowed it
	LarkThroughAlways = "conversation_always" // "Always allow in this conversation" covered it, no card was shown
	LarkThroughCard   = "card"                // the user (or the assistant for them) allowed it on a card
	LarkThroughBypass = "permission_mode"     // the assistant conversation runs in bypassPermissions
)

// agentAPIWriteGETRoutes are the GET/HEAD routes that mint or hand out
// credentials, export the local database, or sign in as the user. The node gate
// and the sent-as-user ledger share this contract so neither calls them reads.
var agentAPIWriteGETRoutes = []string{
	"/conversations/*/qr-code",
	"/calendar/event/*/share-link",
	"/contacts/my-link",
	"/documents/share/password",
	"/links/open",
	"/meetings/participants/*/audio",
	"/maintenance/export",
}

// AgentAPIWriteGET reports whether method/path is one of the Agent API's
// side-effecting GET/HEAD routes. Other methods have their ordinary HTTP
// classification; other GET/HEAD routes are reads.
func AgentAPIWriteGET(method, path string) bool {
	if !strings.EqualFold(method, "GET") && !strings.EqualFold(method, "HEAD") {
		return false
	}
	for _, route := range agentAPIWriteGETRoutes {
		pattern := strings.Split(strings.Trim(route, "/"), "/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(pattern) != len(parts) {
			continue
		}
		match := true
		for i := range pattern {
			if pattern[i] != "*" && pattern[i] != parts[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// LarkSent is one write the gate proxy forwarded and the API answered with a 2xx. Which machine it happened on is not
// in it: the manager takes that from the connection the request came in on.
type LarkSent struct {
	// Command is the gated command the call went through (the gate's own name): the ledger names the gate by it.
	Command string `json:"command,omitempty"`
	Method  string `json:"method"` // the HTTP method, upper case
	Path    string `json:"path"`   // without the gate's path prefix and the query string
	// Text is what the request body said (WrittenText), cut to a few thousand characters; empty when it said none.
	Text string `json:"text,omitempty"`
	// SessionID is the conversation the grant belongs to, when the node knew it: the process that made the call was
	// started for one.
	SessionID  string         `json:"session_id,omitempty"`
	LetThrough LarkLetThrough `json:"let_through"`
	// MessageRef is the id the API's answer gave for the message the write made, as it said it (CleanMessageRef);
	// empty when the answer had none, could not be read or had one longer than MessageRefMost. It is the answer's word
	// for whatever the write was, so the manager keeps it only for a route that creates one message.
	MessageRef string `json:"message_ref,omitempty"`
}

// LarkLetThrough is how the gate let a write through.
type LarkLetThrough struct {
	Kind string `json:"kind"`
	// Rule is the rule that allowed it (LarkThroughRule).
	Rule *LarkRule `json:"rule,omitempty"`
	// InteractionID is the card the user was asked on and AnsweredBy who answered it (LarkThroughCard): AnsweredBy
	// is empty when the user did, AnsweredByAssistant when the assistant did for them.
	InteractionID string `json:"interaction_id,omitempty"`
	AnsweredBy    string `json:"answered_by,omitempty"`
	// EditedFrom is the card's original outgoing text when Allow carried edited_text; Text keeps the sent version.
	EditedFrom string `json:"edited_from,omitempty"`
}

// writtenFields are the fields of a write's arguments or body that connectors usually put their text in, in the
// order WrittenText looks at them.
var writtenFields = []string{"body", "text", "message", "content", "comment"}

// WrittenText is the words a write puts out, as its JSON arguments or body say them: the first of the fields
// connectors name their text by (GitHub's body, mail's and the chats' text) that has any, cut to most characters.
func WrittenText(args json.RawMessage, most int) string {
	var fields map[string]any
	_ = json.Unmarshal(args, &fields)
	for _, k := range writtenFields {
		if s, _ := fields[k].(string); strings.TrimSpace(s) != "" {
			if r := []rune(s); len(r) > most {
				return string(r[:most])
			}
			return s
		}
	}
	return ""
}
