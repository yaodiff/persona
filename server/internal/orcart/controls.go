package orcart

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// A chat's controls beyond sending (#17046): its name (agentSession.rename), going back to before one of the user's
// messages (agentSession.rewind), and its unread mark (agentSession.acknowledgeAttention). Each is a capability of
// status.get that an older Orca does not have. Checked on Orca 1.4.277 (docs/integrations/orca.md "Structured
// sessions").

// The capabilities (Status.Capabilities) of a chat's controls: a host without one has no such method.
const (
	CapChatRename   = "agent-session.structured.rename.v1"
	CapChatRewind   = "agent-session.rewind.v1"
	CapAttentionAck = "agent-session.attention-ack.v1"
)

// Has: the runtime's status says it has capability c.
func (s *Status) Has(c string) bool { return s != nil && slices.Contains(s.Capabilities, c) }

// Rename is agentSession.rename: the chat's name, as the user gave it.
func (c *Client) Rename(ctx context.Context, sessionID, name string) error {
	return c.Call(ctx, WriteTimeout, "agentSession.rename", map[string]any{"sessionId": sessionID, "name": name}, nil)
}

// Rewind is agentSession.rewind: the chat goes back to before the user's message itemID, in the journal epoch the
// caller read it in. Orca refuses one whose epoch moved on (RewindRefusal "stale-epoch") and one it cannot do now
// ("busy"), and nothing changed then.
func (c *Client) Rewind(ctx context.Context, w Write, itemID, epoch string) error {
	return c.mutate(ctx, "agentSession.rewind", "agentSession.rewind", w,
		map[string]any{"itemId": itemID, "expectedEpoch": epoch}, nil)
}

// RewindRefusal is why Orca refused a rewind ("stale-epoch", "busy"…, from its refusal message
// "agent_session_rewind:<reason>"); "" for any other error.
func RewindRefusal(err error) string {
	var e *Error
	if !errors.As(err, &e) {
		return ""
	}
	reason, _ := strings.CutPrefix(e.Message, "agent_session_rewind:")
	if reason == e.Message {
		return ""
	}
	return reason
}

// Seen is agentSession.acknowledgeAttention: the chat was seen up to sequence seq of its journal's epoch, so Orca
// clears its unread mark.
func (c *Client) Seen(ctx context.Context, sessionID, epoch string, seq int64) error {
	return c.Call(ctx, CallTimeout, "agentSession.acknowledgeAttention", map[string]any{"sessionId": sessionID,
		"observedCursor": HistoryCursor{Epoch: epoch, Sequence: seq}}, nil)
}

// effortsOf reads a model's efforts, which Orca lists as strings or as {value, label}.
func effortsOf(raw []json.RawMessage) []string {
	var out []string
	for _, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil && s != "" {
			out = append(out, s)
			continue
		}
		var o struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(r, &o) == nil && o.Value != "" {
			out = append(out, o.Value)
		}
	}
	return out
}
