package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestKBBusCallerWire(t *testing.T) {
	h, a, _ := postsManager(t)
	posts := h.kbFor("feed")
	ctx := t.Context()

	fixtures := []struct {
		method, request, reply string
	}{
		{"kb.posts_put", `{"connector":"feed","sources":["feed","posts"],"comments":null}`, `{"statuses":[]}`},
		{"kb.posts_comments", `{"connector":"feed","sources":["feed","posts"],"id":"p1","count":1,"comments":[]}`, `{}`},
		{"kb.posts_pending", `{"connector":"feed","sources":["feed","posts"],"comments":null,"limit":2}`, `{"posts":null}`},
		{"kb.archived_chats", `{"connector":"feed","sources":["feed","posts"],"comments":null,"source":"posts"}`, `{"chats":[]}`},
		{"kb.forget", `{"source":"feed","archive_sources":["posts"]}`, `{"chats":1,"people":2,"posts":3,"items":4}`},
	}
	for _, fixture := range fixtures {
		a.on(fixture.method, func(json.RawMessage) (any, error) { return json.RawMessage(fixture.reply), nil })
	}
	if got, err := posts.PutPosts(ctx, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("put statuses = %#v, %v, want an empty list", got, err)
	}
	if err := posts.PutComments(ctx, "p1", 1, nil); err != nil {
		t.Errorf("put comments with an empty acknowledgment: %v", err)
	}
	if got, err := posts.PendingPosts(ctx, 2); err != nil || got != nil {
		t.Errorf("pending posts = %#v, %v, want nil", got, err)
	}
	if got, err := posts.ArchivedChats(ctx, "posts"); err != nil || got == nil || len(got) != 0 {
		t.Errorf("archived chats = %#v, %v, want an empty list", got, err)
	}
	if got, err := h.forgetArchive(ctx, "feed"); err != nil || got != (assistantwire.KBForgotten{Chats: 1, People: 2, Posts: 3, Items: 4}) {
		t.Errorf("forgotten = %+v, %v, want all four counts", got, err)
	}
	for _, fixture := range fixtures {
		if got := a.got(fixture.method); len(got) != 1 || string(got[0]) != fixture.request {
			t.Errorf("%s requests = %s, want one request with %s", fixture.method, got, fixture.request)
		}
	}
}

func TestKBBusCallerErrors(t *testing.T) {
	h := &Manager{}
	posts := h.kbFor("feed")
	ctx := t.Context()
	refused := &rpc.Error{Code: assistantwire.CodeKBUnavailable, Message: "archive is still opening", Data: json.RawMessage(`{"retry":true}`)}

	for _, call := range []struct {
		method string
		run    func() error
	}{
		{"kb.posts_comments", func() error { return posts.PutComments(ctx, "p1", 1, nil) }},
		{"kb.forget", func() error { _, err := h.forgetArchive(ctx, "feed"); return err }},
	} {
		t.Run(call.method, func(t *testing.T) {
			h.opts.Assistant = nil
			var rpcErr *rpc.Error
			if err := call.run(); !errors.As(err, &rpcErr) || rpcErr.Code != assistantwire.CodeAssistantOffline || rpcErr.Message != errAssistantOffline.Error() {
				t.Errorf("offline error = %#v, want %s with the offline message", err, assistantwire.CodeAssistantOffline)
			}
			h.opts.Assistant = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, refused }
			if err := call.run(); err != refused {
				t.Errorf("RPC error = %#v, want the original error including its data", err)
			}
		})
	}
}
