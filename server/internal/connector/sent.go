package connector

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// How the gate let a write through (LetThrough.Kind).
const (
	ThroughRule = "rule" // a connector_gate rule allowed it
	ThroughCard = "card" // the user allowed it on the phone
	// ThroughAutoConfirm is a gated write let through by guarded conditional auto-confirm: both a matching hub-side
	// policy (connector_auto_confirm) for a trusted extension installation and a valid short-lived credential returned by
	// actions.describe succeeded, so no phone confirmation card was shown. Distinct from ThroughCard and ThroughAuto.
	ThroughAutoConfirm = "auto_confirm"
	// ThroughAuto is a send an extension's connector made on its own, outside the gate (sent.record): no rule and no
	// card were involved.
	ThroughAuto = "auto"
)

// LetThrough is how the gate let a write through.
type LetThrough struct {
	Kind string
	// Rule is the rule that allowed it (ThroughRule).
	Rule *Rule
	// UserRule says the rule was one the user made from the ledger ("don't ask again in this chat", SetUserRules), not a
	// hand-written one (ThroughRule).
	UserRule bool
	// Summary is the words the card showed (ThroughCard), or Describe's words for ThroughAutoConfirm, cut to cardMost.
	Summary string
	// AutoConfirm carries non-secret policy and verified-extension attribution for ThroughAutoConfirm (never the token).
	AutoConfirm *AutoConfirmAudit
}

// Sent is a write that went out as the user: the gate let it through and the action ran without an error. A write the
// gate refused, the user declined or nobody answered the card of (proto.ToolErrorUnanswered), one that failed, and one
// the user sent by their own hand (Run) are not.
type Sent struct {
	At        time.Time
	Connector string          // the connector's id
	Action    string          // the tool's name
	Args      json.RawMessage // what the action was given
	Text      string          // what it wrote: the arguments' body / text / message…, cut to SentTextMost characters
	Result    string          // what it answered
	// MessageRef is the written message's id on the other side, when the connector said (RefActions): "" when it did
	// not know one. The ledger keeps it as it is, only trimmed and clean, and not at all when it is longer than
	// proto.MessageRefMost characters (proto.CleanMessageRef).
	MessageRef string
	Through    LetThrough
}

// SentTextMost is the most of a write's text that Sent keeps, in characters.
const SentTextMost = 4000

// MethodSentRecord is the call an extension's connector makes to put a send of its own in the ledger.
const MethodSentRecord = "sent.record"

// SentRecordParams are sent.record's params (schema/extension/connector.schema.json ConnectorSentRecordParams): an
// extension's connector that declares sends_as_user tells the hub about a message it sent in the user's name outside
// the write gate. Where it came from and how it was let through are the hub's to say, not these.
type SentRecordParams struct {
	Contribution string         `json:"contribution"`          // the connector's id: one of the caller's own
	Target       map[string]any `json:"target"`                // where it went: an object of short values (a chat, a thread, recipients…); nil: missing
	Text         *string        `json:"text"`                  // what it said, cut to SentTextMost characters (may be empty); nil: missing
	Summary      string         `json:"summary,omitempty"`     // one line saying what it was
	SessionID    string         `json:"session_id,omitempty"`  // the conversation that asked, when one did (the extension's word)
	MachineID    string         `json:"machine_id,omitempty"`  // the computer it ran on, when it names one (the extension's word)
	MessageRef   string         `json:"message_ref,omitempty"` // the sent message's id on the other side
	Setting      string         `json:"setting,omitempty"`     // the extension's setting that let it send without asking
}

// writtenText is the words a write puts out, as its arguments say them (proto.WrittenText, which the node's gate
// proxy reads a request body with too), cut to SentTextMost characters.
func writtenText(args json.RawMessage) string { return proto.WrittenText(args, SentTextMost) }

// SetSent sets what is told about every write that went out through Call (nil: nothing). It is called once the write
// is done, in the call's goroutine and with its ctx, so keep it quick: what it does cannot undo the write, and its
// failure is its own to deal with.
func (r *Registry) SetSent(f func(ctx context.Context, s Sent)) {
	r.mu.Lock()
	r.sent = f
	r.mu.Unlock()
}
