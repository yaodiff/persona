package extmgr

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Transcription for extensions (#8485, docs/extensions/extensions.md "Transcription for extensions"): an extension the
// owner granted the transcription scope hands its own audio to the transcription chosen for meetings; each call goes
// to the assistant, which runs the job, with the extension's id (which the extension cannot set). Like model calls, the
// legacy full-hub grant does not include it, and there is no budget (the owner's decision).

// extensionTranscriptionCall is one of transcription.start / audio / finish / cancel from extension id.
func (ps *extensions) extensionTranscriptionCall(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	if ps.reg == nil || ps.reg.GetExact(id) == nil || !ps.reg.HubScopeGranted(id, extension.ScopeTranscription) {
		data, _ := json.Marshal(map[string]string{"scope": extension.ScopeTranscription})
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + extension.ScopeTranscription, Data: data}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	out, err := callAssistantRaw(ctx, ps.h.kbAsk, assistantwire.TranscriptionExtensionMethod,
		assistantwire.ExtensionTranscription{Extension: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	return out, nil
}
