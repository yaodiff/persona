package extmgr

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// extensionAssistantCall is assistant.conversations / assistant.messages from extension id (#1288): the user's
// conversations with the assistant, read-only, for an extension granted assistant.read; and assistant.memory, the
// assistant's memory entries, read-only, for one granted assistant.memory.read. Each is checked against the grant, as
// kb.read is (the legacy full-hub grant covers them). The manager owns both and answers them.
func (ps *extensions) extensionAssistantCall(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	var scope string
	switch method {
	case assistantwire.MethodAssistantConversations, assistantwire.MethodAssistantMessages:
		scope = extension.ScopeAssistantRead
	case assistantwire.MethodAssistantEntriesList:
		scope = extension.ScopeAssistantEntriesRead
	case assistantwire.MethodAssistantMemory:
		scope = extension.ScopeAssistantMemoryRead
	default:
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
	if ps.reg == nil || !ps.reg.HubScopeAllowed(id, scope) {
		data, _ := json.Marshal(map[string]string{"scope": scope})
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + scope, Data: data}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	if method == assistantwire.MethodAssistantEntriesList {
		// The entries ledger names its reader itself (#13106). Forward both wire bodies as raw JSON.
		var out json.RawMessage
		if err := ps.h.kbAsk(assistantwire.WithEntriesReader(ctx, id), assistantwire.AssistantEntriesListMethod.Name, params, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	// Say which extension reads (#13246): what it is shown follows its trust and grants. An assistant from before that
	// answers the plain method, as a third party's read with no grant.
	out, err := callAssistantRaw(ctx, ps.h.kbAsk, assistantwire.AssistantExtensionReadMethod,
		assistantwire.AssistantExtensionRead{Extension: id, Method: method, Params: params})
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == extension.CodeUnknownMethod {
		err = ps.h.kbAsk(ctx, method, params, &out) // kbAsk: the manager's answer, assistant_offline coded
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
