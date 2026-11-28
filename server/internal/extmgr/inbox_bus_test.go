package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestInboxCollectedWire(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	a := withAssistant(h)
	a.on(assistantwire.MethodInboxCollected, func(json.RawMessage) (any, error) { return json.RawMessage(`{}`), nil })
	if err := h.collectInbox(ctx, roundPoll); err != nil {
		t.Fatalf("collect with an empty acknowledgment: %v", err)
	}
	want := fmt.Sprintf(`{"epoch":%q,"seq":1,"round":"poll","sources":[]}`, h.inbox.epoch)
	if got := string(lastParams(a, assistantwire.MethodInboxCollected)); got != want {
		t.Errorf("collected params = %s, want %s", got, want)
	}
	refused := &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "the store is down"}
	a.on(assistantwire.MethodInboxCollected, func(json.RawMessage) (any, error) { return nil, refused })
	if err := h.collectInbox(ctx, roundPoll); err != refused {
		t.Errorf("collect error = %v, want the original RPC error", err)
	}
}

func TestInboxItemAndForgetWire(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	a := withAssistant(h)
	const item = `{"id":"example:1","from_a_newer_peer":true}`
	a.on(assistantwire.MethodInboxItem, func(json.RawMessage) (any, error) {
		return json.RawMessage(`{"item":` + item + `,"ok":true}`), nil
	})
	if got, ok := h.inboxItemRaw(ctx, "example:1"); !ok || string(got) != item {
		t.Errorf("item = %s, %v, want the full item JSON", got, ok)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxItem)); got != `{"id":"example:1"}` {
		t.Errorf("item params = %s", got)
	}
	a.on(assistantwire.MethodInboxForget, func(json.RawMessage) (any, error) { return json.RawMessage(`{"deleted":3}`), nil })
	if got, err := h.forgetInbox(ctx, "example"); err != nil || got != 3 {
		t.Errorf("forget = %d, %v, want 3 deleted", got, err)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxForget)); got != `{"source":"example"}` {
		t.Errorf("forget params = %s", got)
	}
	refused := &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "the store is down", Data: json.RawMessage(`{"retry":true}`)}
	a.on(assistantwire.MethodInboxForget, func(json.RawMessage) (any, error) { return nil, refused })
	if got, err := h.forgetInbox(ctx, "example"); err != refused || got != 0 {
		t.Errorf("forget = %d, %v, want the original RPC error", got, err)
	}
}

func TestInboxRepoMutesWire(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	a := withAssistant(h)
	a.on(assistantwire.MethodInboxRepoMutes, func(json.RawMessage) (any, error) {
		return json.RawMessage(`{"repos":["example/site"]}`), nil
	})
	if got := h.withGitHubMutes(ctx, json.RawMessage(`{"ci":"all"}`)); string(got) != `{"ci":"all","muted_repos":["example/site"]}` {
		t.Errorf("GitHub options with mutes = %s", got)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxRepoMutes)); got != `{"source":"github"}` {
		t.Errorf("repo mutes read params = %s", got)
	}
	clear := json.RawMessage(`{"muted_repos":[]}`)
	if _, err := h.setGitHubMutes(ctx, clear); err != nil {
		t.Fatalf("clear repo mutes: %v", err)
	}
	if got := string(lastParams(a, assistantwire.MethodInboxRepoMutes)); got != `{"source":"github","set":true}` {
		t.Errorf("clear repo mutes params = %s", got)
	}
	refused := &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "the store is down"}
	a.on(assistantwire.MethodInboxRepoMutes, func(json.RawMessage) (any, error) { return nil, refused })
	if _, err := h.setGitHubMutes(ctx, clear); err != refused {
		t.Errorf("set repo mutes error = %v, want the original RPC error", err)
	}
}
