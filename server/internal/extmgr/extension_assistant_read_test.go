package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// assistant.conversations / assistant.messages (extension_assistant_read.go, #1288): only with assistant.read as
// granted (or the legacy full grant, as for kb.read), forwarded to the manager in assistant.extension_read, which names
// the extension.
func TestExtensionAssistantReadNeedsItsGrant(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-reader", extension.Permissions{Hub: []string{extension.ScopeAssistantRead}})
	kbOnly := kbExtensionSpec(t, h, "example-kb", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	askOnly := kbExtensionSpec(t, h, "example-ask", extension.Permissions{Hub: []string{extension.ScopeAssistantAsk}})
	full := kbExtensionSpec(t, h, "example-full", extension.Permissions{HubAPI: true})
	var mu sync.Mutex
	var asked []string
	var got []string
	const reply = `{"messages":[],"future_field": {"count":9007199254740993}}`
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, method)
		got = append(got, string(params))
		return json.RawMessage(reply), nil
	}
	params := json.RawMessage(`{"id":"c1","limit":5}`)
	for name, spec := range map[string]func(context.Context, string, json.RawMessage) (any, error){"kb.read": kbOnly.Handle, "assistant.ask": askOnly.Handle} {
		for _, method := range []string{assistantwire.MethodAssistantConversations, assistantwire.MethodAssistantMessages} {
			_, err := spec(context.Background(), method, params)
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"assistant.read"`) {
				t.Fatalf("%s %s: %v", name, method, err)
			}
		}
	}
	if len(asked) != 0 {
		t.Fatalf("a denied call reached the manager: %v", asked)
	}
	for _, spec := range []func(context.Context, string, json.RawMessage) (any, error){granted.Handle, full.Handle} {
		out, err := spec(context.Background(), assistantwire.MethodAssistantMessages, params)
		if raw, ok := out.(json.RawMessage); err != nil || !ok || string(raw) != reply {
			t.Fatalf("extension received %T %s (%v), want %s", out, raw, err, reply)
		}
	}
	// The manager is told which extension reads (#13246): what it is shown follows that extension's trust and grants.
	if len(asked) != 2 || asked[0] != assistantwire.MethodAssistantExtensionRead ||
		got[0] != `{"extension":"example-reader","method":"assistant.messages","params":{"id":"c1","limit":5}}` {
		t.Fatalf("the manager got %v %v", asked, got)
	}
	h.extensions.reg.Get("example-reader").Record.Enabled = false
	if _, err := granted.Handle(context.Background(), assistantwire.MethodAssistantConversations, params); err == nil {
		t.Fatal("a switched-off extension still reads")
	}
}

// assistant.memory: only with assistant.memory.read (assistant.read does not include it), forwarded naming the
// extension.
func TestExtensionAssistantMemoryNeedsItsGrant(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-memory", extension.Permissions{Hub: []string{extension.ScopeAssistantMemoryRead}})
	reader := kbExtensionSpec(t, h, "example-reader", extension.Permissions{Hub: []string{extension.ScopeAssistantRead, extension.ScopeKBRead}})
	var asked []string
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		asked = append(asked, method+" "+string(params))
		return json.RawMessage(`{"entries":[]}`), nil
	}
	params := json.RawMessage(`{"include_content":true}`)
	_, err := reader.Handle(context.Background(), assistantwire.MethodAssistantMemory, params)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"assistant.memory.read"`) {
		t.Fatalf("assistant.read alone: %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("a denied call reached the manager: %v", asked)
	}
	if _, err := granted.Handle(context.Background(), assistantwire.MethodAssistantMemory, params); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != `assistant.extension_read {"extension":"example-memory","method":"assistant.memory","params":{"include_content":true}}` {
		t.Fatalf("the manager got %v", asked)
	}
	// The memory grant opens no conversation.
	if _, err := granted.Handle(context.Background(), assistantwire.MethodAssistantMessages, json.RawMessage(`{"id":"c1"}`)); !errors.As(err, &re) || re.Code != extension.CodePermissionDenied {
		t.Fatalf("assistant.messages with the memory grant: %v", err)
	}
}

// An assistant from before assistant.extension_read answers "unknown method": the plain method is asked, as a third
// party's read with no grant.
func TestExtensionAssistantReadFallsBackForAnOlderAssistant(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-reader", extension.Permissions{Hub: []string{extension.ScopeAssistantRead}})
	var asked []string
	const reply = `{"items":[],"total":0,"future":true}`
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		asked = append(asked, method+" "+string(params))
		if method == assistantwire.MethodAssistantExtensionRead {
			return nil, fmt.Errorf("assistant bridge: %w", &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method})
		}
		return json.RawMessage(reply), nil
	}
	params := json.RawMessage(`{"since":"2026-01-02T00:00:00Z"}`)
	out, err := granted.Handle(context.Background(), assistantwire.MethodAssistantConversations, params)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(out); err != nil || string(raw) != reply {
		t.Fatalf("legacy reply: %s, %v", raw, err)
	}
	wantFirst := `assistant.extension_read {"extension":"example-reader","method":"assistant.conversations","params":` + string(params) + `}`
	if len(asked) != 2 || asked[0] != wantFirst || asked[1] != "assistant.conversations "+string(params) {
		t.Fatalf("the manager got %v", asked)
	}
}

func TestExtensionAssistantReadReturnsAssistantDenial(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-reader", extension.Permissions{Hub: []string{extension.ScopeAssistantRead}})
	denied := &rpc.Error{Code: extension.CodePermissionDenied, Message: "read denied"}
	h.opts.Assistant = func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
		if method != assistantwire.MethodAssistantExtensionRead {
			t.Fatalf("assistant denial led to a call to %q", method)
		}
		return nil, denied
	}
	if _, err := granted.Handle(context.Background(), assistantwire.MethodAssistantMessages, json.RawMessage(`{"id":"c1"}`)); err != denied {
		t.Fatalf("assistant denial = %v, want %v", err, denied)
	}
}

func TestExtensionEntriesNeedTheirOwnGrant(t *testing.T) {
	h := newTestManager(t)
	granted := kbExtensionSpec(t, h, "example-entries", extension.Permissions{Hub: []string{extension.ScopeAssistantEntriesRead}})
	memory := kbExtensionSpec(t, h, "example-memory", extension.Permissions{Hub: []string{extension.ScopeAssistantMemoryRead, extension.ScopeAssistantRead}})
	const params = `{"after":"example","by":"user","future_filter":true}`
	const reply = `{"entries":[],"after":"example","has_more":false,"future_field":{"count":9007199254740993}}`
	calls := 0
	h.opts.Assistant = func(ctx context.Context, method string, p json.RawMessage) (json.RawMessage, error) {
		calls++
		if reader := assistantwire.EntriesReader(ctx); reader != "example-entries" || method != assistantwire.MethodAssistantEntriesList || string(p) != params {
			t.Fatalf("forwarded %s %s for %q", method, p, reader)
		}
		return json.RawMessage(reply), nil
	}
	p := json.RawMessage(params)
	_, err := memory.Handle(context.Background(), assistantwire.MethodAssistantEntriesList, p)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"assistant.entries.read"`) || calls != 0 {
		t.Fatalf("denied: %v calls=%d", err, calls)
	}
	out, err := granted.Handle(context.Background(), assistantwire.MethodAssistantEntriesList, p)
	if err != nil || calls != 1 {
		t.Fatalf("granted: %v calls=%d", err, calls)
	}
	if raw, err := json.Marshal(out); err != nil || string(raw) != reply {
		t.Fatalf("forwarded reply = %s, %v, want %s", raw, err, reply)
	}
}

func TestEntriesHTTPTokenDoesNotCacheARevokedGrant(t *testing.T) {
	h := newTestManager(t)
	kbExtensionSpec(t, h, "example-entries", extension.Permissions{Hub: []string{extension.ScopeAssistantEntriesRead}})
	tok := h.extTokens.issue("example-entries", "Example", []string{extension.ScopeAssistantEntriesRead})
	request := extwire.Authorize{Hash: hashToken(tok), Pattern: "GET /api/v1/assistant/entries", Method: "GET", Path: "/api/v1/assistant/entries"}
	if out := h.authorize(request); !out.OK || out.Extension != "example-entries" {
		t.Fatalf("approved: %+v", out)
	}
	if err := h.extensions.reg.SetEnabled(context.Background(), "example-entries", false, "test"); err != nil {
		t.Fatal(err)
	}
	if out := h.authorize(request); out.OK {
		t.Fatalf("unchanged credential survived revoke: %+v", out)
	}
}
