package sessionsearch

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// The same corpus is replayed by agent-search.parity.test.ts through the real web
// matcher. It specifies metadata search, not historical transcript search.
func TestSearchSharedGrammar(t *testing.T) {
	raw, err := os.ReadFile("../../../spec/fixtures/session-search/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Now          time.Time                   `json:"now"`
		Machines     []struct{ ID, Name string } `json:"machines"`
		Pins         struct{ Sessions []string } `json:"pins"`
		Interactions []proto.Interaction         `json:"interactions"`
		Sessions     []proto.Session             `json:"sessions"`
		Cases        []struct {
			Name, Query string
			Expected    []struct {
				Machine string `json:"machine_id"`
				Session string `json:"session_id"`
				Score   int
			}
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	ctx := &sessionlist.SearchContext{Document: Document, Now: fixture.Now, MachineNames: map[string]string{}, Pinned: map[string]bool{}, Errors: map[string]bool{}}
	key := func(mid, sid string) string { b, _ := json.Marshal([2]string{mid, sid}); return string(b) }
	for _, m := range fixture.Machines {
		ctx.MachineNames[m.ID] = m.Name
	}
	var items []sessionlist.Item
	for _, s := range fixture.Sessions {
		raw, _ := json.Marshal(s)
		id := key(s.MachineID, s.SessionID)
		items = append(items, sessionlist.Item{ID: id, Machine: s.MachineID, State: s.State, UpdatedAt: s.UpdatedAt, Full: json.RawMessage(raw), Summary: json.RawMessage(raw)})
		ctx.Pinned[id] = slices.Contains(fixture.Pins.Sessions, s.MachineID+"/"+s.SessionID)
	}
	for _, ix := range fixture.Interactions {
		sid := ix.SessionID
		if ix.FoldedUnder != "" {
			sid = ix.FoldedUnder
		}
		ctx.Errors[key(ix.MachineID, sid)] = true
	}
	var index sessionlist.Index
	index.Replace("fixture", items)
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			page, err := index.Query(sessionlist.Options{Search: test.Query, SearchContext: ctx, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Sessions) != len(test.Expected) {
				t.Fatalf("%q got %d want %d, hits=%+v", test.Query, len(page.Sessions), len(test.Expected), page.SearchHits)
			}
			for n, want := range test.Expected {
				var got proto.Session
				if err := json.Unmarshal(page.Sessions[n].(json.RawMessage), &got); err != nil {
					t.Fatal(err)
				}
				if got.MachineID != want.Machine || got.SessionID != want.Session || page.SearchHits[key(want.Machine, want.Session)].Score != want.Score {
					t.Fatalf("row%d got%s/%s score%d want%+v", n, got.MachineID, got.SessionID, page.SearchHits[key(got.MachineID, got.SessionID)].Score, want)
				}
			}
		})
	}
}

func TestDocumentUsesFullFieldsAndDoesNotInventTranscript(t *testing.T) {
	var row proto.Session
	raw := []byte(`{"title":"Row","cwd":"C:\\projects\\parser","workspace_cwd":"C:\\projects\\parser","agent":"codex","asks":"user request","goal":{"objective":"finish objective"},"todo":{"title":"task title"},"last_text":"An **answer** with [label](https://example.com)","activity":"active now","attention_reason":"approval reason","failure_reason":"failure reason","turn_error":"turn reason","model":"gpt-5.5","work":{"phase":"blocked","line":"work reason"}}`)
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	doc, err := Document(sessionlist.Item{Full: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"user request", "finish objective", "task title"} {
		if !strings.Contains(doc.UserText, needle) {
			t.Fatal(doc)
		}
	}
	if doc.Project != "parser" || !strings.Contains(doc.AgentText, "answer") || !strings.Contains(doc.AgentText, "label") || strings.Contains(doc.AgentText, "**") || !doc.HasErrors {
		t.Fatal(doc)
	}
	if _, err := Document(sessionlist.Item{Full: json.RawMessage(`{`)}); err == nil {
		t.Fatal("malformed transport must not silently become no matches")
	}
}

func BenchmarkRuntimeSearch8000(b *testing.B) {
	var items []sessionlist.Item
	for n := range 8000 {
		s := proto.Session{MachineID: "computer", SessionID: fmt.Sprint(n), Title: fmt.Sprintf("Workspace %d", n), Cwd: "/work/project", LastText: strings.Repeat("ordinary answer ", 20), State: "idle", UpdatedAt: time.Unix(int64(n), 0)}
		if n%20 == 0 {
			s.LastText += " matching needle"
		}
		raw, _ := json.Marshal(s)
		items = append(items, sessionlist.Item{ID: fmt.Sprint(n), Machine: "computer", State: "idle", UpdatedAt: s.UpdatedAt, Full: json.RawMessage(raw), Summary: json.RawMessage(raw)})
	}
	ctx := &sessionlist.SearchContext{Document: Document, MachineNames: map[string]string{"computer": "Example computer"}}
	b.Run("cold_projection", func(b *testing.B) {
		for b.Loop() {
			b.StopTimer()
			var index sessionlist.Index
			index.Replace("computer", items)
			b.StartTimer()
			if _, err := index.Query(sessionlist.Options{Search: "needle", SearchContext: ctx, Summary: true, Limit: 40}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm_query", func(b *testing.B) {
		var index sessionlist.Index
		index.Replace("computer", items)
		_, _ = index.Query(sessionlist.Options{Search: "needle", SearchContext: ctx, Summary: true, Limit: 40})
		b.ResetTimer()
		for b.Loop() {
			if _, err := index.Query(sessionlist.Options{Search: "needle", SearchContext: ctx, Summary: true, Limit: 40}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
