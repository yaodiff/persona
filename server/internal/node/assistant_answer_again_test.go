package node

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestAssistantAnswerAgainRequiresTheSameIdleTurn(t *testing.T) {
	f := cliTest(t)
	n := f.n
	ctx := t.Context()
	id, err := n.assistantSend(ctx, "", t.TempDir(), "claude", "Check today's usage.", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)

	path := filepath.Join(n.home, ".claude", "projects", "-assistant", id+".jsonl")
	lines := (&roundLines{}).user("u1", "", "Check today's usage.").reply("a1", "u1", "end_turn")
	lines.write(t, path)
	read := func(turns int) []proto.Item {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"session_id": id, "turns": turns, "lazy_images": true})
		out, err := n.handle(ctx, "session.items", raw)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["items"].([]proto.Item)
	}
	items := read(1)
	if len(items) != 2 {
		t.Fatalf("fixture did not read one turn: %+v", items)
	}
	prompt := prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: "English"})
	send := func(after, onceID string) error {
		raw, _ := json.Marshal(map[string]any{
			"session_id": id, "text": prompt, "is_main": false, "answer_again_after": after, "once_id": onceID,
		})
		_, err := n.handle(ctx, assistantwire.DeviceAssistantAnswerAgain, raw)
		return err
	}
	assertChanged := func(err error) {
		t.Helper()
		var refusal *rpc.Error
		if !errors.As(err, &refusal) || refusal.Code != assistantwire.CodeAnswerAgainChanged {
			t.Fatalf("want the changed-turn refusal, got %v", err)
		}
	}
	last := items[len(items)-1].ID
	p := f.proc(id)
	p.mu.Lock()
	p.busy = true
	p.mu.Unlock()
	assertChanged(send(last, "busy"))
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	lines.user("u2", "a1", "A new question.").reply("a2", "u2", "end_turn").write(t, path)
	assertChanged(send(last, "stale"))
	if strings.Contains(f.file("stdin.jsonl"), "persona-answer-again") {
		t.Fatal("a busy or stale recovery reached the model")
	}
	items = read(1)
	for range 2 {
		if err := send(items[len(items)-1].ID, "same-turn"); err != nil {
			t.Fatal(err)
		}
	}
	cliWait(t, "recovery prompt written", func() bool { return strings.Contains(f.file("stdin.jsonl"), "persona-answer-again") })
	f.idle(t, id)
	if got := strings.Count(f.file("stdin.jsonl"), "persona-answer-again"); got != 1 {
		t.Fatalf("recovery prompts written = %d, want one", got)
	}

	lines.user("u3", "a2", prompt).reply("a3", "u3", "end_turn").write(t, path)
	items = read(1)
	if len(items) != 4 || items[0].Text != "A new question." || prompts.ParseInjected(items[2].Text) == nil {
		t.Fatalf("reloaded turn lost its original input or the Persona follow-up: %+v", items)
	}
}

func TestAssistantAnswerAgainUsesTheSameIdleRuntimeTurn(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	const id = "session-answer-again"
	var pageMu sync.Mutex
	items := []proto.Item{
		{ID: "1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "Check today's usage."},
		{ID: "2#0", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "No response requested."},
	}
	host.mu.Lock()
	host.answer = func(method string, raw json.RawMessage) (any, error) {
		if method == "session.items" {
			var params struct {
				Limit int `json:"limit"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			pageMu.Lock()
			defer pageMu.Unlock()
			page := items
			if params.Limit == 1 {
				page = items[len(items)-1:]
			}
			return map[string]any{"items": page, "has_more": params.Limit == 1}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	host.mu.Unlock()

	var rev int64
	setRow := func(status string, queued ...string) {
		t.Helper()
		rev++
		row := proto.Session{SessionID: id, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: status, Queued: queued, Rev: rev}
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{row}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "runtime's session update", func() bool {
			s, _, ok := n.runtimes.rowOf(id)
			return ok && s.Rev == row.Rev
		})
	}
	setRow(proto.StatusDone)
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": id, "turns": 1})
	if err != nil {
		t.Fatal(err)
	}
	var observed struct{ Items []proto.Item }
	if err := json.Unmarshal(raw, &observed); err != nil || len(observed.Items) != 2 {
		t.Fatalf("runtime page = %s, err = %v", raw, err)
	}
	after := observed.Items[len(observed.Items)-1].ID
	prompt := prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: "English"})
	send := func(after, onceID string) error {
		_, err := nodeCall(t, n, assistantwire.DeviceAssistantAnswerAgain, map[string]any{
			"session_id": id, "text": prompt, "answer_again_after": after, "once_id": onceID,
		})
		return err
	}
	assertChanged := func(err error) {
		t.Helper()
		var refused *rpc.Error
		if !errors.As(err, &refused) || refused.Code != assistantwire.CodeAnswerAgainChanged {
			t.Fatalf("want the changed-turn refusal, got %v", err)
		}
	}
	setRow(proto.StatusWorking)
	assertChanged(send(after, "working"))
	setRow(proto.StatusIdle, "A queued question.")
	assertChanged(send(after, "queued"))
	setRow(proto.StatusIdle)
	pageMu.Lock()
	items = append(items, proto.Item{ID: "3#0", Seq: 3, Role: "user", Kind: proto.KindText, Text: "A newer question."})
	pageMu.Unlock()
	assertChanged(send(after, "stale"))
	if got := len(host.got("session.send")); got != 0 {
		t.Fatalf("a working, queued, or stale turn sent %d recovery prompts", got)
	}
	pageMu.Lock()
	items = append(items, proto.Item{ID: "4#0", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "No response requested."})
	pageMu.Unlock()
	for range 2 {
		if err := send("4#0", "same-runtime-turn"); err != nil {
			t.Fatal(err)
		}
	}
	sent := host.got("session.send")
	if len(sent) != 1 || !strings.Contains(string(sent[0]), "persona-answer-again") {
		t.Fatalf("runtime recovery sends = %s, want one follow-up", sent)
	}
}
