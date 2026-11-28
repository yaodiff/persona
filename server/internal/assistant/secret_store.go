package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type secretStorage interface {
	SecretProviderOf(string) (extension.SecretProvider, bool)
	StoreSecret(context.Context, extension.SecretProvider, wire.SecretStoreParams, string) (wire.SecretStoreResult, error)
}
type storePending struct {
	target               wire.SecretStoreTarget
	provider             extension.SecretProvider
	machine, session, by string
	until                time.Time
}
type storeApprovals struct {
	mu      sync.Mutex
	pending map[string]storePending
}

// Tool arguments hold metadata or sealed bytes, never paths or plaintext. Source files are CLI-local.
type storeToolArgs struct {
	Phase  string                 `json:"phase"`
	Target wire.SecretStoreTarget `json:"target"`
	KeyID  string                 `json:"key_id,omitempty"`
	Sealed string                 `json:"sealed,omitempty"`
}

func decodeStore(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return errors.New("invalid secret storage request")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid secret storage request")
	}
	return nil
}
func (s *Service) toolSecretStore(ctx context.Context, machine, session string, raw json.RawMessage) proto.ToolResult {
	bad := func(msg string) proto.ToolResult { return proto.TextResult(msg, true) }
	if len(raw) > wire.SecretStoreSealedMost*2 {
		return bad("secret storage request too large")
	}
	var a storeToolArgs
	if decodeStore(raw, &a) != nil || a.Target.Validate() != nil {
		return bad("invalid secret storage metadata; use personad tool secret_store --from-file with field files")
	}
	hs, ok := s.refs.(secretStorage)
	if !ok || !s.hello(ctx).SecretHandoffGate || s.devs == nil || !s.devs.Has(machine) {
		return bad("secret storage is unavailable on this manager or computer")
	}
	row, known := s.listedSession(machine, session)
	if !known {
		return bad("secret storage requires a known local Agent session")
	}
	if _, guest, err := s.guestOf(ctx, machine, session); err != nil || guest {
		return bad("secret storage is unavailable in this session")
	}
	prov, on := hs.SecretProviderOf("op://store/target/field")
	if !on {
		return bad("no enabled op secret provider")
	}
	in := wire.SecretStoreParams{Phase: a.Phase, Target: a.Target, Machine: machine, Session: session, KeyID: a.KeyID, Sealed: a.Sealed}
	by := ""
	switch a.Phase {
	case "prepare":
		if a.KeyID != "" || len(a.Sealed) != 0 {
			return bad("invalid secret storage preparation")
		}
		in.Phase = "describe"
		desc, err := hs.StoreSecret(ctx, prov, in, "")
		if err != nil || desc.Target.Validate() != nil {
			return bad("could not describe the secret storage target")
		}
		in.Target = desc.Target
		agent := strings.TrimSpace(row.Title)
		if agent == "" {
			agent = session
		}
		vaultLabel := desc.VaultName + " (" + in.Target.Vault + ")"
		itemLabel := in.Target.Title
		if in.Target.Item != "" {
			itemLabel += " (" + in.Target.Item + ")"
		}
		l := s.lang(ctx)
		operation := i18n.T(l, "assistant-secret_store."+in.Target.Operation)
		input := proto.SecretHandoffInput{Agent: agent, MachineName: s.machineName(machine), StoreOperation: in.Target.Operation, Prompt: i18n.T(l, "assistant-secret_store.note")}
		for _, f := range in.Target.Fields {
			input.Variables = append(input.Variables, proto.SecretHandoffVariable{Name: f.Name, Provider: prov.Extension, ProviderName: prov.Name, Title: operation + " › " + vaultLabel + " › " + itemLabel})
		}
		cardRaw, _ := json.Marshal(input)
		now := time.Now().UTC()
		ix := proto.Interaction{ID: "ix_" + randomHex(12), MachineID: machine, SessionID: session, SessionTitle: row.Title, Kind: proto.IxPermission, ToolName: proto.SecretHandoffTool, ToolTitle: i18n.T(l, "assistant-secret_store.title"), ToolInput: cardRaw,
			Summary: i18n.T(l, "assistant-secret_store.summary", i18n.Args{"agent": agent, "machine": s.machineName(machine), "operation": operation, "item": itemLabel, "vault": vaultLabel, "count": len(in.Target.Fields)}), CreatedAt: now, ExpiresAt: now.Add(ownerCardMaxWait), SeenTTLSeconds: secondsCeil(handoffCardWait)}
		r, err := s.ask(withCard(ctx, cardSecretForm), ix)
		claim, vouched := s.takeHandoffClaim(ix.ID)
		if err != nil {
			return bad("secret storage approval interrupted; nothing was written")
		}
		if !vouched || !proto.SecretHandoffApproves(r) || time.Now().After(ix.ExpiresAt) {
			return bad("secret storage was not explicitly approved on its card; nothing was written")
		}
		by = "device:" + claim.approver.DeviceID
		in.Phase = "prepare"
		out, err := hs.StoreSecret(ctx, prov, in, by)
		if err != nil || out.KeyID == "" || out.Key == "" {
			return bad("secret storage preparation failed")
		}
		// A node cannot substitute a target after the card was approved.
		before, _ := json.Marshal(in.Target)
		after, _ := json.Marshal(out.Target)
		if !bytes.Equal(before, after) {
			return bad("secret storage target changed after approval")
		}
		book := &s.storeApprovals
		book.mu.Lock()
		defer book.mu.Unlock()
		if book.pending == nil {
			book.pending = map[string]storePending{}
		}
		for id, p := range book.pending {
			if time.Now().After(p.until) {
				delete(book.pending, id)
			}
		}
		if len(book.pending) >= 128 {
			return bad("too many pending secret writes")
		}
		book.pending[out.KeyID] = storePending{in.Target, prov, machine, session, by, time.Now().Add(2 * time.Minute)}
		b, _ := json.Marshal(out)
		return proto.TextResult(string(b), false)
	case "commit":
		book := &s.storeApprovals
		book.mu.Lock()
		p, found := book.pending[a.KeyID]
		// Another session cannot consume or invalidate the owner's pending operation.
		if found && p.machine == machine && p.session == session {
			delete(book.pending, a.KeyID)
		} else {
			found = false
		}
		book.mu.Unlock()
		before, _ := json.Marshal(a.Target)
		after, _ := json.Marshal(p.target)
		if !found || time.Now().After(p.until) || p.provider != prov || !bytes.Equal(before, after) || len(a.Sealed) == 0 || len(a.Sealed) > wire.SecretStoreSealedMost {
			return bad("secret storage approval is gone, changed or expired; nothing was written")
		}
		out, err := hs.StoreSecret(ctx, prov, in, p.by)
		if err != nil {
			return bad("secret storage failed; check item metadata before retrying")
		}
		// No arbitrary provider response can become a tool answer.
		refs := map[string]string{}
		var deleted []string
		for _, f := range p.target.Fields {
			if f.Delete {
				deleted = append(deleted, f.Name)
				continue
			}
			ref := out.References[f.Name]
			if scheme, valid := extension.ReferenceScheme(ref); !valid || scheme != "op" {
				return bad("secret storage completed without valid field references; do not retry blindly")
			}
			refs[f.Name] = ref
		}
		ans := map[string]any{"references": refs}
		if len(deleted) > 0 {
			ans["deleted"] = deleted
		}
		b, _ := json.Marshal(ans)
		return proto.TextResult(string(b), false)
	default:
		return bad("use personad tool secret_store --from-file; internal phase must be prepare or commit")
	}
}
