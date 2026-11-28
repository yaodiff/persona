package extmgr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestMaintenanceApprovalRequiresFreshExplicitAnswer(t *testing.T) {
	for _, source := range []string{"", "direction-lead"} {
		for _, mode := range []string{"allow", "cancel", "expired", "late", "remembered", "default", "terminal", "owner_remembered"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				allow := true
				answer := proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant}
				switch mode {
				case "cancel":
					answer.Cancel = true
				case "expired":
					answer.Dismiss, answer.Reason = true, proto.DismissedExpired
				case "late":
					answer.Late = true
				case "remembered":
					answer.Remembered = true
				case "default":
					answer.AnsweredBy = proto.AnsweredByDefault
				case "terminal":
					answer.AnsweredBy = proto.AnsweredByTerminal
				case "owner_remembered":
					answer.AnsweredBy, answer.Remembered = "", true
				}
				h := &Manager{opts: Options{Ask: func(_ context.Context, ix proto.Interaction) (proto.Resolution, error) {
					if ix.SessionID != source || !proto.InternalMaintenanceApproval(ix) {
						t.Fatalf("maintenance authority lost: %+v", ix)
					}
					return answer, nil
				}}}
				now := time.Now().UTC()
				ix := proto.Interaction{ID: "ix_test_maintenance", MachineID: "machine", SessionID: source,
					SessionTitle: "Persona", Kind: proto.IxPermission, ToolName: "enkeep_rerun",
					ToolInput: json.RawMessage(`{"from":"2026-10-20","to":"2026-10-22","force":true}`),
					CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
				calls := 0
				op := approval.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
					calls++
					return "applied once", nil
				}, Unanswered: "nothing executed"}
				if _, err := h.waitPrepared(context.Background(), ix, op); err != nil {
					t.Fatal(err)
				}
				want := 0
				if mode == "allow" {
					want = 1
				}
				if calls != want {
					t.Fatalf("Apply called %d times, want %d", calls, want)
				}
				ix.ExpiresAt = now.Add(-time.Second)
				if _, err := h.waitPrepared(context.Background(), ix, op); err != nil {
					t.Fatal(err)
				}
				if calls != want {
					t.Fatal("expired rerun executed")
				}
			})
		}
	}
}

func TestMaintenanceNamespaceCannotAuthorizeOwnerOperations(t *testing.T) {
	allow := true
	h := &Manager{opts: Options{Ask: func(context.Context, proto.Interaction) (proto.Resolution, error) {
		return proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant}, nil
	}}}
	for _, tool := range []string{"enkeep_delete", "mail_send", "subscription_purchase"} {
		ix := proto.Interaction{Kind: proto.IxPermission, ToolName: tool, ExpiresAt: time.Now().Add(time.Minute),
			ToolInput: json.RawMessage(`{"force":true}`)}
		r, err := h.waitInteraction(context.Background(), ix)
		if err != nil || r.Allow != nil {
			t.Fatalf("namespace authorized owner operation %s: %+v %v", tool, r, err)
		}
	}
	for _, flag := range []string{"secret", "outward", "owner", "form"} {
		ix := proto.Interaction{Kind: proto.IxPermission, ToolName: "enkeep_rerun", ExpiresAt: time.Now().Add(time.Minute),
			ToolInput: json.RawMessage(`{"force":true}`)}
		switch flag {
		case "secret":
			ix.Secret = true
		case "outward":
			ix.OutwardWrite = true
		case "owner":
			ix.OwnerProposal = true
		case "form":
			ix.Kind = proto.IxElicitation
		}
		if proto.InternalMaintenanceApproval(ix) {
			t.Fatalf("maintenance bypassed %s boundary", flag)
		}
	}
}
