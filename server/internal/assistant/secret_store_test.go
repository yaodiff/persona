package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
)

type storeFake struct {
	*handoffSecretsFake
	mu     sync.Mutex
	phases []string
}

func (f *storeFake) StoreSecret(_ context.Context, _ extension.SecretProvider, in wire.SecretStoreParams, by string) (wire.SecretStoreResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phases = append(f.phases, in.Phase)
	out := wire.SecretStoreResult{Target: in.Target, VaultName: "Dev"}
	if in.Phase == "prepare" {
		out.Key = "recipient-key"
		out.KeyID = "single-use"
	}
	if in.Phase == "commit" {
		refs := map[string]string{}
		for _, field := range in.Target.Fields {
			if field.Delete {
				continue
			}
			refs[field.Name] = "op://vault/item/" + field.Name
		}
		out.References = refs
	}
	return out, nil
}
func TestSecretStorageRequiresExplicitVouchedCardAndConsumesApproval(t *testing.T) {
	for _, answer := range []string{`{"allow":false}`, `{"allow":false,"always":true}`, `{"allow":true,"always":true}`, `{"allow":true}`} {
		t.Run(answer, func(t *testing.T) {
			r := newHandoffRig(t)
			r.devs.setSessions("box", []proto.Session{{MachineID: "box", SessionID: "writer", Title: "Write test"}})
			f := &storeFake{handoffSecretsFake: r.secrets}
			r.s.refs = f
			target := wire.SecretStoreTarget{Operation: "create", Vault: "Dev", Title: "Test item", Category: "API_CREDENTIAL", Fields: []wire.SecretStoreField{{Name: "credential", Type: "CONCEALED"}}}
			args, _ := json.Marshal(storeToolArgs{Phase: "prepare", Target: target})
			done := make(chan proto.ToolResult, 1)
			go func() { done <- r.s.toolSecretStore(context.Background(), "box", "writer", args) }()
			ix := r.card()
			if ix.AlwaysLabel != "" || !strings.Contains(ix.Summary, "Test item") || !strings.Contains(string(ix.ToolInput), "credential") || !strings.Contains(string(ix.ToolInput), "store_operation") {
				t.Fatalf("incomplete storage card: %+v", ix)
			}
			if w := r.post(ix.ID, `{"allow":true}`, nil); w.Code == 200 {
				t.Fatal("unvouched approval accepted")
			}
			r.post(ix.ID, answer, &assistantwire.HandoffApprover{DeviceID: "phone"})
			var got proto.ToolResult
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("card did not finish")
			}
			approved := strings.HasPrefix(answer, `{"allow":true`)
			if got.IsError == approved {
				t.Fatalf("approval outcome %+v", got)
			}
			if !approved {
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.phases) != 1 {
					t.Fatal("provider prepared without explicit one-use approval")
				}
				return
			}
			// Another identity cannot use or invalidate this operation.
			commit, _ := json.Marshal(storeToolArgs{Phase: "commit", Target: target, KeyID: "single-use", Sealed: "encrypted"})
			r.devs.setSessions("box", []proto.Session{{MachineID: "box", SessionID: "writer", Title: "Write test"}, {MachineID: "box", SessionID: "other", Title: "Other"}})
			if !r.s.toolSecretStore(context.Background(), "box", "other", commit).IsError {
				t.Fatal("foreign session committed")
			}
			if result := r.s.toolSecretStore(context.Background(), "box", "writer", commit); result.IsError || !strings.Contains(result.Content[0].Text, "op://") {
				t.Fatalf("commit %+v", result)
			}
			if !r.s.toolSecretStore(context.Background(), "box", "writer", commit).IsError {
				t.Fatal("replay committed")
			}
		})
	}
}

func TestSecretStorageEditOTPAndDelete(t *testing.T) {
	r := newHandoffRig(t)
	r.devs.setSessions("box", []proto.Session{{MachineID: "box", SessionID: "writer", Title: "Edit test"}})
	f := &storeFake{handoffSecretsFake: r.secrets}
	r.s.refs = f
	target := wire.SecretStoreTarget{
		Operation: "edit",
		Vault:     "vaultid123",
		Item:      "itemid123",
		Fields: []wire.SecretStoreField{
			{Name: "totp_secret", Type: "OTP", KeepValue: true},
			{Name: "dup_field", Delete: true},
		},
	}
	args, _ := json.Marshal(storeToolArgs{Phase: "prepare", Target: target})
	done := make(chan proto.ToolResult, 1)
	go func() { done <- r.s.toolSecretStore(context.Background(), "box", "writer", args) }()
	ix := r.card()
	if !strings.Contains(string(ix.ToolInput), "totp_secret") || !strings.Contains(string(ix.ToolInput), "dup_field") {
		t.Fatalf("incomplete edit card: %+v", ix)
	}
	r.post(ix.ID, `{"allow":true}`, &assistantwire.HandoffApprover{DeviceID: "phone"})
	var got proto.ToolResult
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("card did not finish")
	}
	if got.IsError {
		t.Fatalf("prepare failed: %+v", got)
	}
	commit, _ := json.Marshal(storeToolArgs{Phase: "commit", Target: target, KeyID: "single-use", Sealed: "encrypted"})
	res := r.s.toolSecretStore(context.Background(), "box", "writer", commit)
	if res.IsError {
		t.Fatalf("commit failed: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, `"totp_secret":"op://vault/item/totp_secret"`) || !strings.Contains(text, `"deleted":["dup_field"]`) {
		t.Fatalf("unexpected commit result: %s", text)
	}
}
