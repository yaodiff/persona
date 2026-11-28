package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/wireids"
)

const (
	kbImportCheckpoint = "kb.import_checkpoint"
	kbSentMarkers      = "kb.sent_markers"
)

// The provider grant is attached to one installation and runtime. Neither a
// generic KB reader nor a legacy hub token receives these import-specific calls.
func (ps *extensions) kbProviderCall(ctx context.Context, p *extension.Extension, hp *hubExtension, epoch *uint64, method string, params json.RawMessage) (any, error) {
	check := func() error {
		if ps.reg == nil || p == nil || hp == nil || epoch == nil || p.Record.Source != extension.SourceInstalled ||
			p.Record.InstalledAt.IsZero() || p.Record.AttachedPackage() || p.Record.Remote != nil ||
			ps.reg.SettingsMoved(p.Record.ID, *epoch) || ps.get(p.Record.ID) != hp || hp.proc == nil || !hp.proc.OwnsInvocation(ctx) {
			return &rpc.Error{Code: extension.CodePermissionDenied, Message: "current installed provider required"}
		}
		current := ps.reg.SnapshotExact(p.Record.ID)
		if current == nil || current.Manifest == nil || current.Record.Source != extension.SourceInstalled ||
			!current.Record.Enabled || current.Problem != "" || current.Record.StorageKey() != p.Record.StorageKey() ||
			!current.Record.InstalledAt.Equal(p.Record.InstalledAt) || current.Manifest != p.Manifest {
			return &rpc.Error{Code: extension.CodePermissionDenied, Message: "current installed provider required"}
		}
		scopes := []string{extension.ScopeKBImportStatus}
		if method == kbSentMarkers {
			scopes = append(scopes, extension.ScopeSentRead)
		}
		for _, scope := range scopes {
			if !slices.Contains(p.Record.Granted.Hub, scope) || !slices.Contains(current.Manifest.Permissions.Hub, scope) ||
				!slices.Contains(current.Record.Granted.Hub, scope) {
				data, _ := json.Marshal(map[string]string{"scope": scope})
				return &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + scope, Data: data}
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	if len(params) == 0 && method == kbImportCheckpoint {
		params = json.RawMessage(`{}`)
	}
	if len(params) > 1024 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) || wire.ValidateParams(method, params) != nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "invalid import query"}
	}
	var query assistantwire.KBImportQuery
	if err := json.Unmarshal(params, &query); err != nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "invalid import query"}
	}
	request := assistantwire.KBProviderRequest{
		Extension: wireids.ExtensionID(p.Record.ID), StorageID: wireids.ExtensionID(p.Record.StorageKey()),
		InstalledAt: p.Record.InstalledAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), Params: query,
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result json.RawMessage
	var err error
	if method == kbSentMarkers {
		result, err = callAssistantRaw(call, ps.h.kbAsk, assistantwire.KBProviderSentMarkersMethod, request)
	} else {
		result, err = callAssistantRaw(call, ps.h.kbAsk, assistantwire.KBProviderCheckpointMethod, request)
	}
	if revoked := check(); revoked != nil {
		return nil, revoked
	}
	if err != nil {
		return nil, err
	}
	if len(result) == 0 || len(result) > 32<<10 || wire.ValidateResult(method, result) != nil {
		return nil, &rpc.Error{Code: assistantwire.CodeKBUnavailable, Message: "invalid import status response"}
	}
	return result, nil
}
