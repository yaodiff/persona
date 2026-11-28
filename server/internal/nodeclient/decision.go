package nodeclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// DecisionRaiseRequest is the local socket's raise envelope. Cwd and Pane only
// help the node attribute the request to the script or externally started
// session; the manager derives the authenticated caller from the connection.
type DecisionRaiseRequest struct {
	Decision assistantwire.OutsideDecisionRequest `json:"decision"`
	Cwd      string                               `json:"cwd,omitempty"`
}

// DecisionRaiseNode raises a durable owner decision and returns without waiting
// for the owner. Unlike AskNode it never uses DialResumable: a transport failure
// is reported as unknown, and the caller may retry with the same stable ref.
func DecisionRaiseNode(ctx context.Context, dial Dialer, in DecisionRaiseRequest, pane string) (assistantwire.OutsideDecisionReply, error) {
	return decisionNode(ctx, dial, assistantwire.MethodDecisionRaise, in, pane)
}

func DecisionGetNode(ctx context.Context, dial Dialer, id, pane string) (assistantwire.OutsideDecisionReply, error) {
	return decisionNode(ctx, dial, assistantwire.MethodDecisionGet, assistantwire.OutsideDecisionIDParams{ID: assistantwire.OutsideDecisionID(id)}, pane)
}

func DecisionWithdrawNode(ctx context.Context, dial Dialer, id, pane string) (assistantwire.OutsideDecisionReply, error) {
	return decisionNode(ctx, dial, assistantwire.MethodDecisionWithdraw, assistantwire.OutsideDecisionIDParams{ID: assistantwire.OutsideDecisionID(id)}, pane)
}

func decisionNode(ctx context.Context, dial Dialer, kind string, input any, pane string) (assistantwire.OutsideDecisionReply, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return assistantwire.OutsideDecisionReply{}, err
	}
	line, err := dial(ctx, Request{Kind: kind, Input: raw, Pane: pane})
	if err != nil {
		return assistantwire.OutsideDecisionReply{}, err
	}
	var reply assistantwire.OutsideDecisionReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return assistantwire.OutsideDecisionReply{}, fmt.Errorf("bad answer from the Persona node: %w", err)
	}
	return reply, nil
}
