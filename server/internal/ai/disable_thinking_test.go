package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureRequestBody serves one protocol-neutral OK reply and records the decoded request body.
func captureRequestBody(t *testing.T, api string) (body <-chan map[string]any, url string) {
	t.Helper()
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		got <- m
		switch api {
		case "chat":
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "responses":
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
		default:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return got, srv.URL
}

func shortRequest(disable bool) Request {
	return Request{
		System:          "name it",
		Messages:        []Message{{Role: RoleUser, Content: "do the thing"}},
		MaxTokens:       200,
		DisableThinking: disable,
	}
}

// Every protocol gets its own standard "thinking off" encoding when Request.DisableThinking is set. This is the
// wire guarantee the title callers rely on; dropping the request-level override turns these red.
func TestRequestDisableThinkingWire(t *testing.T) {
	cases := []struct {
		api   string
		model string
		check func(t *testing.T, body map[string]any)
	}{
		{"messages", "thinking-model", func(t *testing.T, body map[string]any) {
			thinking, _ := body["thinking"].(map[string]any)
			if thinking["type"] != "disabled" {
				t.Fatalf("messages thinking = %#v, want type disabled; body %v", thinking, body)
			}
		}},
		{"chat", "thinking-model", func(t *testing.T, body map[string]any) {
			if body["reasoning_effort"] != "none" {
				t.Fatalf("chat reasoning_effort = %#v, want none; body %v", body["reasoning_effort"], body)
			}
		}},
		{"responses", "thinking-model", func(t *testing.T, body map[string]any) {
			reasoning, _ := body["reasoning"].(map[string]any)
			if reasoning["effort"] != "none" {
				t.Fatalf("responses reasoning = %#v, want effort none; body %v", reasoning, body)
			}
		}},
		// Models whose Messages API rejects thinking.type=disabled keep the repository's existing compatibility
		// encoding rather than sending a field the protocol endpoint refuses.
		{"messages", "claude-sonnet-5-5", func(t *testing.T, body map[string]any) {
			thinking, _ := body["thinking"].(map[string]any)
			if thinking["type"] != "between_tools" {
				t.Fatalf("sonnet-5-5 thinking = %#v, want between_tools", thinking)
			}
			output, _ := body["output_config"].(map[string]any)
			if output["effort"] != "low" {
				t.Fatalf("sonnet-5-5 output_config = %#v, want effort low", output)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.api+"/"+tc.model, func(t *testing.T) {
			got, baseURL := captureRequestBody(t, tc.api)
			c := New(Config{BaseURL: baseURL, APIKey: "test-key", Model: tc.model, API: tc.api})
			if _, err := c.Chat(context.Background(), shortRequest(true)); err != nil {
				t.Fatal(err)
			}
			tc.check(t, <-got)
		})
	}
}

// Without the switch, an empty configured effort leaves the wire body untouched, so every existing caller keeps
// its provider default.
func TestRequestDisableThinkingOffByDefault(t *testing.T) {
	for _, api := range []string{"messages", "chat", "responses"} {
		t.Run(api, func(t *testing.T) {
			got, baseURL := captureRequestBody(t, api)
			c := New(Config{BaseURL: baseURL, APIKey: "test-key", Model: "thinking-model", API: api})
			if _, err := c.Chat(context.Background(), shortRequest(false)); err != nil {
				t.Fatal(err)
			}
			body := <-got
			if _, ok := body["thinking"]; ok && api == "messages" {
				t.Fatalf("messages unexpectedly sent thinking: %v", body["thinking"])
			}
			if _, ok := body["reasoning_effort"]; ok {
				t.Fatalf("chat unexpectedly sent reasoning_effort: %v", body["reasoning_effort"])
			}
			if _, ok := body["reasoning"]; ok {
				t.Fatalf("responses unexpectedly sent reasoning: %v", body["reasoning"])
			}
		})
	}
}

// The request switch wins over a connection-level high effort: a title call must not think hard because the
// provider connection defaults to high.
func TestRequestDisableThinkingWinsOverConfigEffort(t *testing.T) {
	got, baseURL := captureRequestBody(t, "chat")
	c := New(Config{BaseURL: baseURL, APIKey: "test-key", Model: "thinking-model", API: "chat", ReasoningEffort: "high"})
	if _, err := c.Chat(context.Background(), shortRequest(true)); err != nil {
		t.Fatal(err)
	}
	if body := <-got; body["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %#v, want none despite high config; body %v", body["reasoning_effort"], body)
	}
}
