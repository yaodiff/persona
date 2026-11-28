package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// flushThenHang answers 200 + text/event-stream, writes events, flushes them and then blocks until the client's
// context cuts the connection: a usage event arrives first, the read then fails with the deadline. The returned stop
// unblocks the handler and closes the server so teardown never waits on the safety timer.
func flushThenHang(t *testing.T, events string) (srv *httptest.Server, stop func()) {
	t.Helper()
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, events)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done(): // the test's deadline canceled the request
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	return srv, func() { close(release); srv.Close() }
}

// When a stream is cut by the context deadline after a usage-bearing event, Chat must fail with "read stream" while
// keeping the partial Usage, and the WithResult hook must receive exactly that usage with the error. This is what lets
// model_usage tell a long reply cut off late from a call that produced nothing.
func TestChatStreamDeadlineKeepsPartialUsageAndResultHook(t *testing.T) {
	cases := []struct {
		name   string
		api    string
		events string
		model  string
		want   Usage
	}{
		{
			name:   "messages",
			api:    "", // default Messages API
			events: sse(evStart),
			model:  "model-x",
			want:   Usage{InputTokens: 120, OutputTokens: 1, CacheReadInputTokens: 30, CacheCreationInputTokens: 5},
		},
		{
			name: "responses",
			api:  "responses",
			// Some compatible servers already report (partial) usage on an early response event; respond keeps it.
			events: `data: {"type":"response.created","response":{"model":"resp-partial",` +
				`"usage":{"input_tokens":50,"output_tokens":9,"input_tokens_details":{"cached_tokens":8}}}}` + "\n\n",
			model: "resp-partial",
			want:  Usage{InputTokens: 42, OutputTokens: 9, CacheReadInputTokens: 8},
		},
		{
			name: "chat completions",
			api:  "chat",
			events: `data: {"model":"chat-partial","choices":[{"index":0,"delta":{"content":"half"}}],` +
				`"usage":{"prompt_tokens":20,"completion_tokens":11,"prompt_tokens_details":{"cached_tokens":6}}}` + "\n\n",
			model: "chat-partial",
			want:  Usage{InputTokens: 14, OutputTokens: 11, CacheReadInputTokens: 6},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, stop := flushThenHang(t, tc.events)
			defer stop()

			var hookReply Reply
			var hookErr error
			hookCalls := 0
			c := New(Config{API: tc.api, BaseURL: srv.URL, APIKey: "k", Model: "asked-model"}).
				WithResult(func(r Reply, e error) { hookCalls++; hookReply, hookErr = r, e })

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			reply, err := c.Chat(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "a"}}, MaxTokens: 64})

			if err == nil || !strings.Contains(err.Error(), "read stream") {
				t.Fatalf("err=%v, want an ai: read stream error", err)
			}
			if !strings.Contains(err.Error(), "deadline exceeded") {
				t.Fatalf("err=%v, want the context deadline in the wrapped error", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v does not wrap context.DeadlineExceeded", err)
			}
			if reply.Usage != tc.want {
				t.Fatalf("reply.Usage=%+v, want %+v (reply=%+v)", reply.Usage, tc.want, reply)
			}
			if reply.Model != tc.model {
				t.Fatalf("reply.Model=%q, want %q", reply.Model, tc.model)
			}
			if reply.Text != "" {
				t.Fatalf("reply.Text=%q: a read error returns usage metadata but no partial answer text", reply.Text)
			}
			if hookCalls != 1 || hookErr == nil {
				t.Fatalf("onResult calls=%d err=%v, want exactly one error report", hookCalls, hookErr)
			}
			if hookReply.Usage != tc.want {
				t.Fatalf("onResult usage=%+v, want %+v", hookReply.Usage, tc.want)
			}
			if hookReply.Model != tc.model {
				t.Fatalf("onResult model=%q, want %q", hookReply.Model, tc.model)
			}
		})
	}
}

// A read failure after usage was parsed keeps that usage at the parser level too, with the original read error
// reachable through errors.Is (a reset / unexpected EOF family, not only a deadline).
func TestMessageStreamReadErrorKeepsItsUsage(t *testing.T) {
	r := io.MultiReader(strings.NewReader(sse(evStart)), failingReader{})
	reply, answered, err := readMessageStream(r)
	if err == nil || !strings.Contains(err.Error(), "read stream") || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want read stream wrapping io.ErrUnexpectedEOF", err)
	}
	if !answered {
		t.Fatal("a failure after the 200 is answered")
	}
	want := Usage{InputTokens: 120, OutputTokens: 1, CacheReadInputTokens: 30, CacheCreationInputTokens: 5}
	if reply.Usage != want || reply.Model != "model-x" {
		t.Fatalf("reply=%+v, want usage %+v and model model-x", reply, want)
	}
}

// If the read fails before any usage event, the returned Usage stays zero while the error is unchanged.
func TestMessageStreamReadErrorBeforeUsageIsZeroUsage(t *testing.T) {
	r := io.MultiReader(strings.NewReader(sse(evBlock(0, "text"), evText(0, "hi"))), failingReader{})
	reply, _, err := readMessageStream(r)
	if err == nil || !strings.Contains(err.Error(), "read stream") || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want read stream wrapping io.ErrUnexpectedEOF", err)
	}
	if reply.Usage != (Usage{}) {
		t.Fatalf("usage=%+v, want zero before any usage event", reply.Usage)
	}
}

func TestCompletionsStreamReadErrorKeepsItsUsage(t *testing.T) {
	chunk := `data: {"model":"chat-x","choices":[{"index":0,"delta":{"content":"hi"}}],` +
		`"usage":{"prompt_tokens":20,"completion_tokens":11,"prompt_tokens_details":{"cached_tokens":6}}}` + "\n\n"
	r := io.MultiReader(strings.NewReader(chunk), failingReader{})
	reply, err := readCompletions(r)
	if err == nil || !strings.Contains(err.Error(), "read stream") || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want read stream wrapping io.ErrUnexpectedEOF", err)
	}
	want := Usage{InputTokens: 14, OutputTokens: 11, CacheReadInputTokens: 6}
	if reply.Usage != want || reply.Model != "chat-x" {
		t.Fatalf("reply=%+v, want usage %+v and model chat-x", reply, want)
	}
}
