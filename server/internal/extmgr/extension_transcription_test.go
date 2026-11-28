package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// transcription.* (extension_transcription.go, #8485): only with the transcription scope itself (not the legacy full
// grant, not another scope), handed to the assistant with the extension's id, which the params cannot change.
func TestExtensionTranscriptionNeedsItsScopeAndIsAttributed(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-video", extension.Permissions{Hub: []string{extension.ScopeTranscription}})
	models := kbExtensionSpec(t, h, "example-models", extension.Permissions{Hub: []string{extension.ScopeModels}})
	full := kbExtensionSpec(t, h, "example-full", extension.Permissions{HubAPI: true})
	var mu sync.Mutex
	var asked []string
	var got []json.RawMessage
	const reply = `{"job":"j1","future_field": {"receipt":9007199254740993}}`
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, method)
		got = append(got, append(json.RawMessage{}, params...))
		return json.RawMessage(reply), nil
	}
	params := json.RawMessage(`{"language":"zh-CN","extension":"someone-else"}`)
	for name, call := range map[string]func(context.Context, string, json.RawMessage) (any, error){
		"models only": models.Handle, "legacy full grant": full.Handle,
	} {
		for _, m := range []string{assistantwire.MethodTranscriptionStart, assistantwire.MethodTranscriptionAudio,
			assistantwire.MethodTranscriptionFinish, assistantwire.MethodTranscriptionCancel} {
			_, err := call(context.Background(), m, params)
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"transcription"`) {
				t.Errorf("%s: %s: %v", name, m, err)
			}
		}
	}
	if len(asked) != 0 {
		t.Fatalf("refused calls reached the assistant: %v", asked)
	}
	out, err := granted.Handle(context.Background(), assistantwire.MethodTranscriptionStart, params)
	if raw, ok := out.(json.RawMessage); err != nil || !ok || string(raw) != reply {
		t.Fatalf("extension received %T %s (%v), want %s", out, raw, err, reply)
	}
	var env assistantwire.ExtensionTranscription
	if len(asked) != 1 || asked[0] != assistantwire.MethodTranscriptionExtension || json.Unmarshal(got[0], &env) != nil ||
		env.Extension != "example-video" || env.Method != assistantwire.MethodTranscriptionStart || string(env.Params) != string(params) {
		t.Fatalf("handed over: %v %s", asked, got)
	}
	h.opts.Assistant = nil
	_, err = granted.Handle(context.Background(), assistantwire.MethodTranscriptionStart, params)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeAssistantOffline || re.Message != errAssistantOffline.Error() {
		t.Fatalf("assistant offline: %v", err)
	}
}
