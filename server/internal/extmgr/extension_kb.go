package extmgr

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// extensionKBCall exposes only the public knowledge-base methods. Internal kb.* calls (notably kb.forget and
// kb.posts_*) never pass this allowlist, even with the old full-hub grant.
func (ps *extensions) extensionKBCall(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	var scope string
	switch method {
	case assistantwire.MethodKBList, assistantwire.MethodKBGet, assistantwire.MethodKBDay, assistantwire.MethodKBSearch, assistantwire.MethodKBOwner, assistantwire.MethodKBRecent:
		scope = extension.ScopeKBRead
	case assistantwire.MethodKBUpdate, assistantwire.MethodKBPutRecording:
		scope = extension.ScopeKBWrite
	default:
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
	if ps.reg == nil || ps.reg.GetExact(id) == nil || !ps.reg.HubScopeAllowed(id, scope) {
		data, _ := json.Marshal(map[string]string{"scope": scope})
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + scope, Data: data}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	call := assistantCaller(ps.h.kbAsk)
	var out json.RawMessage
	var err error
	switch method {
	case assistantwire.MethodKBList:
		out, err = callExtensionKBRead(ctx, call, assistantwire.KBListMethod, params)
	case assistantwire.MethodKBRecent:
		out, err = callExtensionKBRead(ctx, call, assistantwire.KBRecentMethod, params)
	case assistantwire.MethodKBGet:
		out, err = callExtensionKBRead(ctx, call, assistantwire.KBGetMethod, params)
	case assistantwire.MethodKBSearch:
		out, err = callExtensionKBRead(ctx, call, assistantwire.KBSearchMethod, params)
	case assistantwire.MethodKBDay:
		return extensionKBDay(ctx, call, params, ps.reg.HubScopeAllowed(id, extension.ScopeSentRead))
	case assistantwire.MethodKBOwner:
		err = call(ctx, method, params, &out)
	case assistantwire.MethodKBUpdate, assistantwire.MethodKBPutRecording: // the write is the extension's: say whose (#6100)
		storageID, identityErr := ps.assistantStorageID(ctx, ps.reg.SnapshotExact(id))
		if identityErr != nil {
			return nil, identityErr
		}
		envelope := assistantwire.KBExtensionUpdate{Extension: id, StorageID: storageID, Params: params}
		if method == assistantwire.MethodKBPutRecording {
			out, err = callAssistantRaw(ctx, call, assistantwire.KBExtensionPutRecordingMethod, envelope)
		} else {
			out, err = callAssistantRaw(ctx, call, assistantwire.KBExtensionUpdateMethod, envelope)
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// callExtensionKBRead keeps the extension's JSON for strict validation in the assistant and preserves newer reply fields.
func callExtensionKBRead[P, R any](ctx context.Context, call assistantCaller, method buswire.Method[P, R], params json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := call(ctx, method.Name, params, &out)
	return out, err
}

func extensionKBDay(ctx context.Context, call assistantCaller, params json.RawMessage, sentRead bool) (any, error) {
	var method buswire.Method[assistantwire.KBDayParams, assistantwire.KBDayResult] = assistantwire.KBDayMethod
	if sentRead {
		method = assistantwire.KBExtensionDayMethod
	}
	out, err := callExtensionKBRead(ctx, call, method, params)
	var re *rpc.Error
	oldAssistant := sentRead && errors.As(err, &re) && re.Code == extension.CodeUnknownMethod
	if oldAssistant {
		// An older assistant can still supply the archive, but cannot attest to its origin.
		out, err = callExtensionKBRead(ctx, call, assistantwire.KBDayMethod, params)
	}
	if err != nil {
		return nil, err
	}
	var result assistantwire.KBDayResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	if !sentRead {
		result.SentOrigin = "missing_scope"
	} else if oldAssistant || result.SentOrigin == "" {
		result.SentOrigin = "unavailable"
	}
	if result.SentOrigin != "available" {
		for i := range result.Messages {
			result.Messages[i].SentBy = ""
			result.Messages[i].SentLedgerID = 0
			result.Messages[i].RecalledAt = nil
		}
	}
	return result, nil
}
