package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	cwd := filepath.Join(n.home, ".persona", "assistant")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := n.assistantSend(ctx, "", cwd, "claude", "Check today's usage.", nil, "", "")
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

	last = items[len(items)-1].ID
	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": id}); err != nil {
		t.Fatal(err)
	}
	assertChanged(send(last, "explicit-stop"))

	const question = "How many requests are left?"
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id": id, "text": question, "client_msg_id": "owner-after-stop",
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	lines.user("u4", "a3", question).add(map[string]any{
		"type": "assistant", "uuid": "a4", "parentUuid": "u4",
		"message": map[string]any{"role": "assistant", "stop_reason": "end_turn", "content": []any{
			map[string]any{"type": "text", "text": "No response requested."},
		}},
	}).write(t, path)
	items = read(1)
	last = items[len(items)-1].ID
	if err := send(last, "after-stop"); err != nil {
		t.Fatalf("a missing reply to the new input must allow a follow-up: %v", err)
	}
	cliWait(t, "the new turn's recovery prompt", func() bool {
		return strings.Count(f.file("stdin.jsonl"), "persona-answer-again") == 2
	})
	f.idle(t, id)

	n.stopAssistantProcess(id)
	err = send(last, "process-gone")
	var refused *rpc.Error
	if err == nil || (errors.As(err, &refused) && refused.Code == assistantwire.CodeAnswerAgainChanged) {
		t.Fatalf("a missing process on an unchanged turn must allow a manual retry notice: %v", err)
	}
}
