package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A call moved to the background while it waits (#14653: a command whose approval card nobody answered, past 120 s)
// gets a placeholder result at once, and the turn goes on to its final reply and ends. Later the task's notification
// wakes the conversation for a turn of its own. The final reply of the first turn is an ordinary reply: it is in the
// conversation's items (what conversation_read and the apps read, the latest two exchanges), it is the turn's last
// words, and the woken turn after it does not take its place.
//
// The lines are hand-built in the shape of Claude Code's (the envelope of claude-tool-outcomes.jsonl); no transcript of
// the issue's Claude Code (2.1.293) was at hand. The results' wording is Claude Code 2.1.331's own for a Bash command
// past its timeout and for an MCP call moved to the background after 120 s, which is what the issue quotes ("still
// running after 120s … moved to the background as task").
func TestBackgroundedCallKeepsTheTurnsFinalReply(t *testing.T) {
	const final = "The doc update waits for your approval on the card; once you approve it, it goes through."
	cases := []struct {
		name, task, call, result, record string
	}{
		{
			name:   "bash past its timeout",
			task:   "b7q2k9x1m",
			call:   `{"type":"tool_use","id":"toolu_bg1","name":"Bash","input":{"command":"lark-cli docs +update --doc DOC_ID","description":"Update the doc"}}`,
			result: `Command did not complete within its 120s timeout and was moved to the background (ID: b7q2k9x1m). Output is being written to: /work/tasks/b7q2k9x1m.output.`,
			record: `,"toolUseResult":{"stdout":"","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false,"backgroundTaskId":"b7q2k9x1m","timedOutAfterMs":120000}`,
		},
		{
			name:   "mcp call still running after 120s",
			task:   "m4k8p2r6t",
			call:   `{"type":"tool_use","id":"toolu_bg1","name":"mcp__persona__docs_update","input":{"doc":"DOC_ID"}}`,
			result: `MCP tool \"persona - docs_update\" is still running after 120s. It was moved to the background as task m4k8p2r6t and keeps running. You can keep working in the meantime. If it completes while you are still working, you'll receive a notification with the result.`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := `"isSidechain":false,"userType":"external","entrypoint":"sdk-cli","cwd":"/work/assistant","sessionId":"00000000-0000-4000-8000-0000000000aa","version":"2.1.293"`
			note := `<task-notification>\n<task-id>` + c.task + `</task-id>\n<tool-use-id>toolu_bg1</tool-use-id>\n<status>completed</status>\n<summary>The call completed</summary>\n</task-notification>`
			lines := []string{
				`{"parentUuid":null,` + env + `,"type":"user","promptId":"p1","message":{"role":"user","content":[{"type":"text","text":"Put the summary into the doc."}]},"uuid":"u1","timestamp":"2026-11-24T08:00:00.000Z"}`,
				`{"parentUuid":"u1",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"Writing the summary into the doc now."}],"stop_reason":null},"uuid":"a1","timestamp":"2026-11-24T08:00:04.000Z"}`,
				`{"parentUuid":"a1",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_01","type":"message","role":"assistant","content":[` + c.call + `],"stop_reason":"tool_use"},"uuid":"a2","timestamp":"2026-11-24T08:00:05.000Z"}`,
				`{"parentUuid":"a2",` + env + `,"type":"user","promptId":"p1","message":{"role":"user","content":[{"tool_use_id":"toolu_bg1","type":"tool_result","content":"` + c.result + `","is_error":false}]},"uuid":"u2","timestamp":"2026-11-24T08:02:05.000Z"` + c.record + `,"sourceToolAssistantUUID":"a2"}`,
				`{"parentUuid":"u2",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_02","type":"message","role":"assistant","content":[{"type":"text","text":"` + final + `"}],"stop_reason":"end_turn"},"uuid":"a3","timestamp":"2026-11-24T08:02:09.000Z"}`,
				`{"parentUuid":"a3",` + env + `,"type":"system","subtype":"turn_duration","durationMs":129000,"uuid":"d1","timestamp":"2026-11-24T08:02:10.000Z","isMeta":false}`,
				`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-24T08:30:00.000Z","sessionId":"00000000-0000-4000-8000-0000000000aa","content":"` + note + `"}`,
				`{"parentUuid":"d1",` + env + `,"type":"user","promptId":"p2","message":{"role":"user","content":"` + note + `"},"uuid":"u3","timestamp":"2026-11-24T08:30:00.000Z"}`,
				`{"parentUuid":"u3",` + env + `,"type":"assistant","message":{"model":"claude-opus-5-5","id":"msg_03","type":"message","role":"assistant","content":[{"type":"text","text":"The doc is updated."}],"stop_reason":"end_turn"},"uuid":"a4","timestamp":"2026-11-24T08:30:04.000Z"}`,
				`{"parentUuid":"a4",` + env + `,"type":"system","subtype":"turn_duration","durationMs":4000,"uuid":"d2","timestamp":"2026-11-24T08:30:05.000Z","isMeta":false}`,
			}
			path := filepath.Join(t.TempDir(), "s.jsonl")
			write := func(n int) {
				t.Helper()
				if err := os.WriteFile(path, []byte(strings.Join(lines[:n], "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f := NewFile(path)
			// The first turn has ended: its final reply is in the items and is the session's last words.
			write(6)
			if _, err := f.Refresh(); err != nil {
				t.Fatal(err)
			}
			checkFinalReply(t, f, final, false, "after the turn ended")
			if m := f.Meta(); m.LastText != final || !m.TurnClosed {
				t.Fatalf("after the turn ended: last text %q, turn closed %v", m.LastText, m.TurnClosed)
			}
			// The task's notification woke a turn of its own: the reply before it stays, read again cold or not.
			write(len(lines))
			if _, err := f.Refresh(); err != nil {
				t.Fatal(err)
			}
			checkFinalReply(t, f, final, true, "after the woken turn")
			cold := NewFile(path)
			if _, err := cold.Refresh(); err != nil {
				t.Fatal(err)
			}
			checkFinalReply(t, cold, final, true, "read cold")
		})
	}
}

// checkFinalReply: the latest two exchanges (conversation_read's default) hold final as an assistant text after the
// placeholder result, with no words between them, and before the wake that started the next turn (woken: there is one).
func checkFinalReply(t *testing.T, f *File, final string, woken bool, when string) {
	t.Helper()
	items, _, _ := f.PageTurns(0, 2, 2000)
	result, reply, wake := -1, -1, len(items)
	for i, it := range items {
		switch {
		case it.Kind == proto.KindToolResult && it.ToolUseID == "toolu_bg1":
			result = i
		case it.Role == "assistant" && it.Kind == proto.KindText && it.Text == final:
			reply = i
		case it.Wake && wake == len(items):
			wake = i
		}
	}
	if result < 0 || reply < result || reply > wake || woken != (wake < len(items)) {
		t.Fatalf("%s: placeholder result at %d, final reply at %d, wake at %d in %+v", when, result, reply, wake, items)
	}
	for _, it := range items[result+1 : reply] {
		if it.Role == "assistant" && it.Kind == proto.KindText {
			t.Fatalf("%s: words between the result and the final reply: %q", when, it.Text)
		}
	}
}
