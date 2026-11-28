package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The hub hands the node (and through it a runtime) only the paging the app gave: an empty or missing before_seq,
// limit or turns is left out rather than sent as 0, which a runtime may refuse as a bad value on every page.
func TestSessionItemsForwardsOnlyTheGivenPaging(t *testing.T) {
	for _, c := range []struct {
		query string
		want  map[string]any
	}{
		{"before_seq=&turns=2&lazy_images=1", map[string]any{"session_id": "s1", "turns": 2.0, "lazy_images": true}},
		{"", map[string]any{"session_id": "s1"}},
		{"user_inputs=1", map[string]any{"session_id": "s1", "user_inputs": true}},
		{"user_inputs=0", map[string]any{"session_id": "s1"}},
		{"before_seq=40&limit=50", map[string]any{"session_id": "s1", "before_seq": 40.0, "limit": 50.0}},
		{"before_seq=0", map[string]any{"session_id": "s1", "before_seq": 0.0}},
	} {
		var got map[string]any
		m := wireNode(t, func(p json.RawMessage) any {
			_ = json.Unmarshal(p, &got)
			if got["user_inputs"] == true {
				return map[string]any{"total": 0, "inputs": []any{}, "history": "fixture"}
			}
			return map[string]any{"items": []any{}, "has_more": false}
		})
		req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?"+c.query, nil)
		req.SetPathValue("sid", "s1")
		w := httptest.NewRecorder()
		(&Hub{}).sessionItems(w, req, m)
		if w.Code != 200 {
			t.Fatalf("?%s: %d %s", c.query, w.Code, w.Body)
		}
		g, _ := json.Marshal(got)
		e, _ := json.Marshal(c.want)
		if string(g) != string(e) {
			t.Errorf("?%s: the node got %s, want %s", c.query, g, e)
		}
	}
}

func TestSessionItemsNotifiesManagerOfInlineViewPage(t *testing.T) {
	const card = "```persona-view\n{\"fallback\":\"Choose\",\"sections\":[]}\n```"
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	items := []proto.Item{
		{ID: "card#0", Role: "assistant", Kind: proto.KindText, Text: card, TS: at},
		{ID: "long#0", Role: "assistant", Kind: proto.KindText, Text: "truncated preview", FullSize: 4096},
		{ID: "user#0", Role: "user", Kind: proto.KindText, Text: card},
	}
	m := wireNode(t, func(json.RawMessage) any {
		return map[string]any{"items": items, "strict_read": true}
	})
	h := newTestHub(t)
	var notices bytes.Buffer
	h.bus.current = map[string]*busConn{assistantwire.AssistantID: {peer: rpc.New("manager", &notices)}}

	for _, tt := range []struct {
		name, query string
		wantNotice  bool
	}{
		{name: "strict export", query: "?strict=1"},
		{name: "history page", query: "?before_seq=20", wantNotice: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/m1/s1/items"+tt.query, nil)
			req.SetPathValue("sid", "s1")
			w := httptest.NewRecorder()
			h.sessionItems(w, req, m)
			if w.Code != http.StatusOK {
				t.Fatalf("items status %d: %s", w.Code, w.Body)
			}
			if !tt.wantNotice {
				if notices.Len() != 0 {
					t.Fatalf("strict read notified the manager: %s", &notices)
				}
				return
			}
			var notice struct {
				Event  string                     `json:"event"`
				Params assistantwire.SessionItems `json:"params"`
			}
			if err := json.Unmarshal(notices.Bytes(), &notice); err != nil {
				t.Fatalf("expected one inline view page notification: %v", err)
			}
			p := notice.Params
			if notice.Event != assistantwire.EventInlineViewPage || p.MachineID != m.cfg.ID || p.SessionID != "s1" || len(p.Items) != 2 {
				t.Fatalf("unexpected inline view notification: %+v", notice)
			}
			if inline := p.Items[0]; inline.ID != "card#0" || inline.Role != "assistant" || inline.Kind != proto.KindText || inline.Text != card || !inline.TS.Equal(at) {
				t.Fatalf("unexpected inline card: %+v", inline)
			}
			if full := p.Items[1]; full.ID != "long#0" || full.Role != "assistant" || full.Kind != proto.KindText || full.FullSize != 4096 || full.Text != "" {
				t.Fatalf("expected full item reference without its preview: %+v", full)
			}
		})
	}
}

// Index mode has a narrow wire shape, including when no inputs exist. A node that ignores the flag
// must not silently become a zero count: older nodes keep the client's loaded-only compatibility rail.
func TestSessionItemsUserInputsWireShape(t *testing.T) {
	for _, c := range []struct {
		name     string
		response any
		status   int
		want     string
	}{
		{"empty", map[string]any{"total": 0, "inputs": []any{}, "history": "fixture"}, 200,
			`{"total":0,"inputs":[],"history":"fixture"}`},
		{"positions only", map[string]any{
			"total": 1, "inputs": []any{map[string]any{"id": "input-1#0", "seq": 7, "text": "synthetic body"}},
			"history": "fixture", "items": []any{map[string]any{"text": "synthetic body"}},
		}, 200, `{"total":1,"inputs":[{"id":"input-1#0","seq":7}],"history":"fixture"}`},
		{"excerpts and count", map[string]any{
			"total": 2, "history": "fixture", "inputs": []any{
				map[string]any{"id": "input-1#0", "seq": 7, "preview": "Question", "answer": "Answer", "text": "synthetic body"},
				map[string]any{"id": "input-2#0", "seq": 9, "attachments": 2, "image": "synthetic image"},
			},
		}, 200, `{"total":2,"inputs":[{"id":"input-1#0","seq":7,"preview":"Question","answer":"Answer"},{"id":"input-2#0","seq":9,"attachments":2}],"history":"fixture"}`},
		{"older node", map[string]any{"items": []any{}, "has_more": false}, 502, ""},
		{"partial index", map[string]any{"total": 2, "inputs": []any{map[string]any{"id": "input-1#0", "seq": 7}}, "history": "fixture"}, 502, ""},
		{"null index", map[string]any{"total": 0, "inputs": nil, "history": "fixture"}, 502, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := wireNode(t, func(p json.RawMessage) any {
				var params map[string]any
				if err := json.Unmarshal(p, &params); err != nil || params["user_inputs"] != true || params["before_seq"] != nil {
					t.Errorf("unexpected projection request: %s, %v", p, err)
				}
				return c.response
			})
			req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?user_inputs=1&before_seq=1", nil)
			req.SetPathValue("sid", "s1")
			w := httptest.NewRecorder()
			(&Hub{}).sessionItems(w, req, m)
			if w.Code != c.status {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, c.status)
			}
			if c.status == 200 {
				var got any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(got)
				var want any
				if err := json.Unmarshal([]byte(c.want), &want); err != nil {
					t.Fatal(err)
				}
				expected, _ := json.Marshal(want)
				if string(body) != string(expected) {
					t.Fatalf("got %s, want %s", body, expected)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("the input index must not be cached")
				}
			}
		})
	}
}

// The private export can distinguish a strict local read from a cached reply or
// a terminal-only runtime's otherwise valid placeholder page.
func TestStrictExportTranscriptHubContract(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, nodeAck := range []bool{false, true} {
			t.Run(fmt.Sprintf("strict=%t/node-ack=%t", strict, nodeAck), func(t *testing.T) {
				m := wireNodeRPC(t, func(_ context.Context, method string, raw json.RawMessage) (any, error) {
					var request map[string]any
					if err := json.Unmarshal(raw, &request); err != nil {
						return nil, err
					}
					if (request["strict_read"] == true) != strict || request["session_id"] != "s1" {
						t.Errorf("incorrect strict request: %s %v", method, request)
					}
					item := proto.Item{ID: "i#0", Seq: 1, Role: "assistant", Kind: "text", Text: "complete"}
					if method == "session.item" {
						if request["item_id"] != item.ID {
							t.Errorf("wrong full item id: %v", request)
						}
						return struct {
							proto.Item
							StrictRead      bool `json:"strict_read,omitempty"`
							SourceTextBytes int  `json:"source_text_bytes,omitempty"`
						}{item, nodeAck, len(item.Text)}, nil
					}
					return map[string]any{"items": []proto.Item{item}, "has_more": false,
						"history": "fixture", "from_seq": 0, "to_seq": 1, "strict_read": nodeAck}, nil
				})
				h := &Hub{}
				for _, target := range []struct {
					path string
					read func(http.ResponseWriter, *http.Request, *Machine)
				}{
					{"/items", h.sessionItems},
					{"/items/i%230/full", h.sessionItemFull},
				} {
					req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1"+target.path, nil)
					if strict {
						req.URL.RawQuery = "strict=1"
					}
					req.SetPathValue("sid", "s1")
					req.SetPathValue("iid", "i#0")
					w := httptest.NewRecorder()
					target.read(w, req, m)
					if strict && !nodeAck {
						if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "node_outdated") {
							t.Fatalf("unsupported read yielded %d: %s", w.Code, w.Body)
						}
						continue
					}
					if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "complete") ||
						(strict && !strings.Contains(w.Body.String(), `"strict_read":true`)) {
						t.Fatalf("unexpected wire response %d: %s", w.Code, w.Body)
					}
				}
			})
		}
	}
}
