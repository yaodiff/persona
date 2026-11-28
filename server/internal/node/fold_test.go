package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// session.items: with fold_work the latest page folds the agent's work (older callers still get all of it); a fold
// opens with from_seq / through_seq, a thousand items at a time.
func TestSessionItemsFoldWork(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var lines []string
	add := func(item map[string]any) {
		b, _ := json.Marshal(map[string]any{"timestamp": t0.Add(time.Duration(len(lines)) * time.Second).Format(time.RFC3339Nano),
			"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": item}})
		lines = append(lines, string(b))
	}
	lines = append(lines, `{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/work/project","cli_version":"0.163.0"}}`)
	add(map[string]any{"type": "UserMessage", "id": "u1", "content": []map[string]any{{"type": "text", "text": "build it"}}})
	for i := 0; i < 700; i++ {
		add(map[string]any{"type": "CommandExecution", "id": fmt.Sprintf("c%d", i), "command": []string{"/bin/zsh", "-lc", "make"},
			"status": "completed", "aggregated_output": strings.Repeat("x", 500), "exit_code": 0})
	}
	add(map[string]any{"type": "AgentMessage", "id": "a1", "phase": "final_answer", "content": []map[string]any{{"type": "Text", "text": "built"}}})
	path := filepath.Join(t.TempDir(), "rollout-2026-01-01T00-00-00-s1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sid = "s1"
	n := &Node{home: t.TempDir(), paths: map[string]string{}, files: map[string]*transcript.File{sid: transcript.NewFile(path)},
		procs: map[string]*assistantProc{}, agents: map[string]host.Agent{}, state: &stateFile{Sessions: map[string]sessionState{}}}
	call := func(params string) (res struct {
		Items   []proto.Item `json:"items"`
		More    bool         `json:"more"`
		HasMore bool         `json:"has_more"`
		History string       `json:"history"`
		FromSeq *int         `json:"from_seq"`
		ToSeq   *int         `json:"to_seq"`
	}) {
		t.Helper()
		out, err := n.handle(context.Background(), "session.items", json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if all := call(`{"session_id":"s1","turns":2}`); len(all.Items) != 1402 {
		t.Fatalf("without fold_work the page should hold every item, got %d", len(all.Items))
	}
	page := call(`{"session_id":"s1","turns":2,"fold_work":true}`)
	var kinds []string
	for _, it := range page.Items {
		kinds = append(kinds, it.Kind)
	}
	if strings.Join(kinds, " ") != "text fold text" || page.Items[1].Fold.Tools != 700 || page.Items[1].Fold.Items != 1400 {
		t.Fatalf("folded page: %v %+v", kinds, page.Items[1].Fold)
	}
	fo := page.Items[1]
	if page.History == "" {
		t.Fatal("folded page omitted history generation")
	}
	var opened []proto.Item
	for from := fo.Seq; ; {
		r := call(fmt.Sprintf(`{"session_id":"s1","from_seq":%d,"through_seq":%d}`, from, fo.Fold.ThroughSeq))
		if r.History != page.History {
			t.Fatalf("fold range history = %q, want %q", r.History, page.History)
		}
		if r.FromSeq == nil || *r.FromSeq != from-1 {
			t.Fatalf("fold range from_seq = %v, want %d", r.FromSeq, from-1)
		}
		if r.ToSeq == nil || (r.More && *r.ToSeq != r.Items[len(r.Items)-1].Seq) || (!r.More && *r.ToSeq != fo.Fold.ThroughSeq) {
			t.Fatalf("fold range to_seq = %v (more=%v, through=%d)", r.ToSeq, r.More, fo.Fold.ThroughSeq)
		}
		opened = append(opened, r.Items...)
		if !r.More {
			break
		}
		if len(opened) > 5000 {
			t.Fatal("the range never ends")
		}
		from = r.Items[len(r.Items)-1].Seq + 1
	}
	if len(opened) != 1400 || opened[0].ID != strings.TrimPrefix(fo.ID, "fold:") || opened[len(opened)-1].ID != fo.Fold.ThroughID {
		t.Fatalf("the fold opened into %d items (%s … %s), want 1400 (%s … %s)", len(opened), opened[0].ID, opened[len(opened)-1].ID, fo.ID, fo.Fold.ThroughID)
	}
}

// herdr read a Codex agent as working for minutes although its turn had finished two days earlier (the app showed
// "working for 45 hours"): a rollout that ends with the turn complete and has not been written for a while wins. A turn
// that has just started, or one whose start is still being written, stays working.
func TestCodexTurnOverOverridesWorking(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, events ...string) string {
		lines := []string{`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"x","cwd":"/work/project"}}`}
		for _, e := range events {
			lines = append(lines, `{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"`+e+`"}}`)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	n := &Node{paths: map[string]string{}, metas: map[string]quickMeta{}, bgScans: map[string]*bgScan{}}
	now := time.Now()
	for _, tc := range []struct {
		events []string
		age    time.Duration
		over   bool
	}{
		{[]string{"task_started", "task_complete"}, 2 * time.Hour, true},
		{[]string{"task_started", "turn_aborted"}, 2 * time.Hour, true},
		{[]string{"task_started", "task_complete"}, 5 * time.Second, false}, // herdr may be a moment ahead
		{[]string{"task_complete", "task_started"}, 2 * time.Hour, false},   // a long turn, nothing written lately
		// a new turn after the last one completed, compacting its context: nothing written for a while either
		{[]string{"task_started", "task_complete", "task_started", "context_compacted"}, 2 * time.Hour, false},
		{nil, 2 * time.Hour, false}, // the tail says nothing about turns
	} {
		id := fmt.Sprintf("%08d-0000-0000-0000-000000000000", len(n.paths))
		p := write(id+".jsonl", tc.events...)
		if err := os.Chtimes(p, now.Add(-tc.age), now.Add(-tc.age)); err != nil {
			t.Fatal(err)
		}
		n.paths[id] = p
		if got := n.codexTurnOver(id, now); got != tc.over {
			t.Fatalf("%v written %v ago: turn over %v, want %v", tc.events, tc.age, got, tc.over)
		}
	}
}
