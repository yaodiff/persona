package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// #14653: a turn of the assistant's in which a call is moved to the background (it waited past 120 s on an approval
// card nobody answered) still ends with its final reply, and that reply reaches the chat the same two ways any other
// does: the live text while it is written, then the transcript's item (pushed to a watching app, and in session.items,
// which conversation_read reads). The stream-json events and transcript lines are hand-built in the shape of Claude
// Code's; no real Claude Code ran.
func TestAssistantBackgroundedCallKeepsTheFinalReply(t *testing.T) {
	n := assistantNode(t)
	all, pw := hubPushes(n)
	t.Cleanup(func() { pw.Close() })
	const id = "11111111-2222-3333-4444-555555555514"
	const final = "The doc update waits for your approval on the card; once you approve it, it goes through."
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/home/u/.persona/assistant"})
	path := filepath.Join(n.home, "transcript.jsonl")
	n.files[id] = transcript.NewFile(path)
	n.watches = map[string]*watch{id: {cancel: func() {}}}
	p := &assistantProc{id: id}

	env := `"isSidechain":false,"userType":"external","entrypoint":"sdk-cli","cwd":"/home/u/.persona/assistant","sessionId":"` + id + `","version":"2.1.293"`
	lines := []string{
		`{"parentUuid":null,` + env + `,"type":"user","promptId":"p1","message":{"role":"user","content":[{"type":"text","text":"Put the summary into the doc."}]},"uuid":"u1","timestamp":"2026-11-24T08:00:00.000Z"}`,
		`{"parentUuid":"u1",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"Writing the summary into the doc now."}],"stop_reason":null},"uuid":"a1","timestamp":"2026-11-24T08:00:04.000Z"}`,
		`{"parentUuid":"a1",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_01","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_bg1","name":"Bash","input":{"command":"lark-cli docs +update --doc DOC_ID","description":"Update the doc"}}],"stop_reason":"tool_use"},"uuid":"a2","timestamp":"2026-11-24T08:00:05.000Z"}`,
		`{"parentUuid":"a2",` + env + `,"type":"user","promptId":"p1","message":{"role":"user","content":[{"tool_use_id":"toolu_bg1","type":"tool_result","content":"Command did not complete within its 120s timeout and was moved to the background (ID: b7q2k9x1m). Output is being written to: /work/tasks/b7q2k9x1m.output.","is_error":false}]},"uuid":"u2","timestamp":"2026-11-24T08:02:05.000Z","toolUseResult":{"stdout":"","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false,"backgroundTaskId":"b7q2k9x1m","timedOutAfterMs":120000},"sourceToolAssistantUUID":"a2"}`,
		`{"parentUuid":"u2",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_02","type":"message","role":"assistant","content":[{"type":"text","text":"` + final + `"}],"stop_reason":"end_turn"},"uuid":"a3","timestamp":"2026-11-24T08:02:09.000Z"}`,
		`{"parentUuid":"a3",` + env + `,"type":"system","subtype":"turn_duration","durationMs":129000,"uuid":"d1","timestamp":"2026-11-24T08:02:10.000Z","isMeta":false}`,
	}
	write := func(k int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(lines[:k], "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ev := func(e string) string { return `{"type":"stream_event","event":` + e + `,"parent_tool_use_id":null}` }
	text := func(s string) []string {
		b, _ := json.Marshal(s)
		return []string{
			ev(`{"type":"message_start","message":{"id":"msg","model":"claude-opus-5-5","usage":{"input_tokens":5}}}`),
			ev(`{"type":"content_block_delta","delta":{"type":"text_delta","text":` + string(b) + `}}`),
			ev(`{"type":"message_stop"}`),
		}
	}
	stream := func(lines ...string) { n.readAssistant(p, strings.NewReader(strings.Join(lines, "\n")+"\n")) }

	// The words before the call, the call, and its placeholder result once it was moved to the background.
	write(4)
	stream(append(text("Writing the summary into the doc now."),
		`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b7q2k9x1m","task_type":"local_bash","description":"Update the doc"}]}`)...)
	// The final reply, its line, the turn's end.
	write(len(lines))
	stream(append(text(final), `{"type":"result","subtype":"success","is_error":false,"result":`+jsonString(t, final)+`}`)...)

	var live, pushed bool
	for deadline := time.After(2 * time.Second); !live || !pushed; {
		select {
		case m := <-all:
			switch m.Event {
			case "session.delta":
				var d struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(m.Params, &d)
				live = live || d.Text == final
			case "session.items":
				var b struct {
					Items []proto.Item `json:"items"`
				}
				_ = json.Unmarshal(m.Params, &b)
				pushed = pushed || hasFinalReply(b.Items, final)
			}
		case <-deadline:
			t.Fatalf("final reply: live text %v, pushed item %v", live, pushed)
		}
	}

	raw, _ := json.Marshal(map[string]any{"session_id": id, "turns": 2, "lazy_images": true})
	out, err := n.handle(t.Context(), "session.items", raw)
	if err != nil {
		t.Fatal(err)
	}
	if items := out.(map[string]any)["items"].([]proto.Item); !hasFinalReply(items, final) {
		t.Fatalf("session.items (conversation_read) has no final reply: %+v", items)
	}
}

func hasFinalReply(items []proto.Item, text string) bool {
	for _, it := range items {
		if it.Role == "assistant" && it.Kind == proto.KindText && it.Text == text {
			return true
		}
	}
	return false
}
