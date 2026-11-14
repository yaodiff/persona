package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/proto"
)

// modelExecution is non-secret per-turn execution metadata supplied by the hub.
type modelExecution struct {
	Model  string
	Effort string
	// ServiceTier is a one-shot native speed request for a new Codex conversation. It is consumed before the first turn,
	// never kept as process state or included in the execution fingerprint.
	ServiceTier string `json:"-"`
	UseEffort   bool
	Fingerprint string
	Ultracode   *bool `json:"-"` // explicit TUI start request; never a default or execution fingerprint
	// PlanThen: the permission mode a start in plan mode goes on in once its plan is approved (planthen.go); never
	// part of a fingerprint.
	PlanThen string `json:"-"`
	// Runtime: the assistant's runtime, proto.AssistantRuntimeAgentSDK or "" (the CLI; "cli" reads as ""). Omitted
	// when empty so a fingerprint from before #2455 stays the same.
	Runtime   string `json:",omitempty"`
	IsMain    bool   `json:",omitempty"`
	ConfigDir string `json:"-"` // a conversation's directory, not its model; retained across process restarts
	// Interject: a message this one carries, sent while the conversation answers, moves its foreground work to the
	// background (assistant_interject.go). Per message, never part of the fingerprint.
	Interject bool `json:"-"`
	// SecretTicket: the hand-off a new Claude Code start puts in its environment (secretenv.go). Per start, never
	// part of a fingerprint, never kept for a resume.
	SecretTicket string `json:"-"`
}

// check refuses a model that is not a model id (proto.ValidModelID, #1644) before anything runs; the error's text is
// what the manager reads as a refused request (assistant startRequestErr), so it stays "invalid model". The effort is
// checked against the launcher's own levels: a codex one takes ultra (validModesFor).
func (m modelExecution) check(launcher string) error {
	if m.Model != "" && !proto.ValidModelID(m.Model) {
		return errors.New("invalid model")
	}
	return validModesFor(launcher, "", m.Effort)
}

// executionFingerprint also detects a managed provider changed before the next turn (any of its fields, the #2455 ones
// included), and the runtime changed: another process, resumed on the same conversation.
func (n *Node) executionFingerprint(launcher string, m modelExecution) string {
	if m.Runtime == proto.AssistantRuntimeCLI {
		m.Runtime = ""
	}
	raw, _ := json.Marshal(m)
	if l, ok := n.managedCatalog(launcher); ok { // as named: the same before and after a withheld one's pull (its Rev)
		v, _ := json.Marshal(l)
		raw = append(raw, v...)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func executionOption(options []modelExecution) modelExecution {
	if len(options) > 0 {
		return options[0]
	}
	return modelExecution{}
}

// assistantModel is the launcher and model the hub runs conversation id's next message on (as a message from the phone
// would be), for a message the node sends it by itself (recoverAssistantTasks). Started on the model it last ran, the
// conversation's next message from the phone would need another process (#1554). Nothing when the hub can't say (an
// older hub): the conversation's own then.
func (n *Node) assistantModel(ctx context.Context, id string) (string, modelExecution) {
	var res struct {
		Launcher         string `json:"launcher"`
		Model            string `json:"model"`
		Effort           string `json:"effort"`
		ModelFingerprint string `json:"model_fingerprint"`
		Runtime          string `json:"runtime"`
		IsMain           bool   `json:"is_main"`
	}
	if n.peer == nil || n.peer.Call(ctx, "assistant.model", map[string]any{"session_id": id}, &res) != nil {
		return "", modelExecution{}
	}
	return res.Launcher, modelExecution{Model: res.Model, Effort: res.Effort, UseEffort: res.ModelFingerprint != "", Fingerprint: res.ModelFingerprint, Runtime: res.Runtime, IsMain: res.IsMain}
}
