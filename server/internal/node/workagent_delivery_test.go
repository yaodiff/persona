package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// stubAgyNativePrompt makes the herdr double behave like an agy that submits each prompt: its own hook identifies
// the conversation and agent.prompt appends the same native USER_INPUT shape observed in a real agy 1.5.16 transcript.
func stubAgyNativePrompt(t *testing.T, n *Node, f *fakeHerdr, pane, conversation string) {
	t.Helper()
	n.mu.Lock()
	if n.agyReports == nil {
		n.agyReports = map[string]agyHookReport{}
	}
	n.agyReports[pane] = agyHookReport{conversation: conversation, event: "PreInvocation", at: time.Now()}
	n.mu.Unlock()
	file := agyTestFile(t, n.agyDataRoot(), conversation, "")
	f.handle("agent.prompt", func(p map[string]any) any {
		previous, err := os.ReadFile(file)
		if err != nil {
			return herdrFail{Code: "test_read_failed", Message: err.Error()}
		}
		record, err := json.Marshal(map[string]any{
			"step_index": bytes.Count(previous, []byte("\n")),
			"source":     "USER_EXPLICIT",
			"type":       "USER_INPUT",
			"status":     "DONE",
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
			"content":    "<USER_REQUEST>\n" + str(p["text"]) + "\n</USER_REQUEST>",
		})
		if err == nil {
			err = os.WriteFile(file, append(previous, append(record, '\n')...), 0o600)
		}
		if err != nil {
			return herdrFail{Code: "test_write_failed", Message: err.Error()}
		}
		return map[string]any{}
	})
}

// Terminal acceptance is not conversation delivery for an agy work Agent (#14623).
func TestAgyFollowUpSendDelivery(t *testing.T) {
	const prompt = "Check the delivery acknowledgement and report the result"
	old := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"<USER_REQUEST>\nAn earlier request\n</USER_REQUEST>"}` + "\n"
	for _, tc := range []struct {
		name       string
		transcript string
		record     bool
		wantErr    bool
	}{
		{name: "native user input recorded", transcript: old, record: true},
		{name: "terminal accepted but native transcript unchanged", transcript: old, wantErr: true},
		{name: "old duplicate is not a new acknowledgement", transcript: `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"<USER_REQUEST>\n` + prompt + `\n</USER_REQUEST>"}` + "\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
			seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
			file := agyTestFile(t, n.agyDataRoot(), agyTestID, tc.transcript)
			if tc.record {
				f.handle("agent.prompt", func(map[string]any) any {
					line := fmt.Sprintf(`{"step_index":1,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>\n/plan %s\n</USER_REQUEST>"}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), prompt)
					out, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
					if err != nil {
						return herdrFail{Code: "test_write_failed", Message: err.Error()}
					}
					_, err = out.WriteString(line)
					if closeErr := out.Close(); err == nil {
						err = closeErr
					}
					if err != nil {
						return herdrFail{Code: "test_write_failed", Message: err.Error()}
					}
					return map[string]any{}
				})
			} else {
				f.reply("agent.prompt", map[string]any{})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := requestCtx(t, ctx, n, "session.send", map[string]any{"session_id": agyTestRow, "text": prompt})
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("native USER_INPUT was recorded: %v", err)
				}
				return
			}
			var re *rpc.Error
			if !errors.As(err, &re) || deliveredOf(re) != deliveredUnknown {
				t.Fatalf("terminal success without native USER_INPUT returned err=%v, want delivered=unknown", err)
			}
			if got := len(f.trace("agent.prompt")); got != 1 {
				t.Fatalf("agent.prompt calls=%d, want exactly one (no ambiguous retype)", got)
			}
		})
	}
}

func TestAgyFollowUpSendBindsLateConversation(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
	const prompt = "Create the conversation with this follow-up"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, "")
	f.handle("agent.prompt", func(map[string]any) any {
		n.mu.Lock()
		n.agyReports = map[string]agyHookReport{agyTestPane: {conversation: agyTestID, event: "PreInvocation", at: time.Now()}}
		n.mu.Unlock()
		line := fmt.Sprintf(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>\n%s\n</USER_REQUEST>"}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), prompt)
		if err := os.WriteFile(file, []byte(line), 0o600); err != nil {
			return herdrFail{Code: "test_write_failed", Message: err.Error()}
		}
		return map[string]any{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := requestCtx(t, ctx, n, "session.send", map[string]any{"session_id": agyTestRow, "text": prompt}); err != nil {
		t.Fatalf("late conversation report and native USER_INPUT: %v", err)
	}
}

func TestAgySendWatchCompleteLinesAndReplacement(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const prompt = "Watch only complete native input lines"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, "")
	line := fmt.Sprintf(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>\n%s\n</USER_REQUEST>"}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), prompt)

	watch := n.watchAgySend(agyTestRow, agyTestPane, prompt, time.Now())
	agyTestAppend(t, file, line[:len(line)-1])
	if watch.took() {
		t.Fatal("an incomplete JSONL record acknowledged the send")
	}
	agyTestAppend(t, file, "\n")
	if !watch.took() {
		t.Fatal("the completed native USER_INPUT did not acknowledge the send")
	}

	watch = n.watchAgySend(agyTestRow, agyTestPane, prompt, time.Now())
	replacement := file + ".replacement"
	agyTestWrite(t, replacement, line)
	if err := os.Rename(replacement, file); err != nil {
		t.Fatal(err)
	}
	if watch.took() || !watch.lost {
		t.Fatal("a replacement transcript was treated as the append-only file being watched")
	}
}

func TestAgyPaneSendTagsClientMsgIDOnItemsAndWatch(t *testing.T) {
	const (
		tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jB9sAAAAASUVORK5CYII="
		prompt1 = "Inspect the failing screen layout"
		prompt2 = "Verify the proposed fix plan"
		prompt3 = "Retry after dismissing the dialog"
		prompt4 = "Check the final status via companion route"
	)
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))

	// Reading session.items before the conversation binds returns the placeholder notice (Seq: 1).
	// That placeholder must never run echo matching or advance scanned past step_index 0.
	notice, _ := agyTestItems(t, n, agyTestRow, nil)
	if len(notice) != 1 || notice[0].Kind != proto.KindNotice {
		t.Fatalf("unbound session.items = %+v, want placeholder notice", notice)
	}

	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": 0})
	t.Cleanup(func() { n.unwatch(agyTestRow) })

	file := agyTestFile(t, n.agyDataRoot(), agyTestID, "")
	appendUser := func(step int, body string) error {
		line := fmt.Sprintf(`{"step_index":%d,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>\n%s\n</USER_REQUEST>"}`+"\n",
			step, time.Now().UTC().Format(time.RFC3339Nano), body)
		out, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = out.WriteString(line)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		return err
	}

	// 1. First send carries an image attachment and binds the conversation late; transcript omits media.
	f.handle("agent.prompt", func(map[string]any) any {
		n.mu.Lock()
		n.agyReports = map[string]agyHookReport{agyTestPane: {conversation: agyTestID, event: "PreInvocation", at: time.Now()}}
		n.mu.Unlock()
		if err := appendUser(0, prompt1); err != nil {
			return herdrFail{Code: "test_write_failed", Message: err.Error()}
		}
		return map[string]any{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := requestCtx(t, ctx, n, "session.send", map[string]any{
		"session_id":    agyTestRow,
		"text":          prompt1,
		"client_msg_id": "msg-launch-1",
		"images":        []map[string]any{{"mime": "image/png", "data": tinyPNG}},
	}); err != nil {
		t.Fatalf("first agy send: %v", err)
	}
	waitFor(t, "watch push with first client_msg_id", func() bool {
		for _, it := range hub.got() {
			if it.ClientMsgID == "msg-launch-1" {
				return true
			}
		}
		return false
	})
	items, _ := agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 1 || items[0].ClientMsgID != "msg-launch-1" {
		t.Fatalf("first session.items = %+v, want single item tagged msg-launch-1", items)
	}

	// 2. Plan-mode follow-up written by agy with a "/plan " prefix tags the new item without touching step 0.
	f.handle("agent.prompt", func(map[string]any) any {
		if err := appendUser(1, "/plan "+prompt2); err != nil {
			return herdrFail{Code: "test_write_failed", Message: err.Error()}
		}
		return map[string]any{}
	})
	if _, err := requestCtx(t, ctx, n, "session.send", map[string]any{
		"session_id":    agyTestRow,
		"text":          prompt2,
		"client_msg_id": "msg-plan-2",
	}); err != nil {
		t.Fatalf("plan-mode agy send: %v", err)
	}
	waitFor(t, "watch push with plan-mode client_msg_id", func() bool {
		for _, it := range hub.got() {
			if it.ClientMsgID == "msg-plan-2" {
				return true
			}
		}
		return false
	})
	items, _ = agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 2 || items[0].ClientMsgID != "msg-launch-1" || items[1].ClientMsgID != "msg-plan-2" {
		t.Fatalf("plan-mode session.items = %+v, want [msg-launch-1, msg-plan-2]", items)
	}

	// 3. A definite host refusal forgets its echo so a later identical prompt is not claimed by the refused id.
	f.handle("agent.prompt", func(map[string]any) any {
		return herdrFail{Code: "agent_blocked", Message: "interactive input required"}
	})
	_, err := requestCtx(t, ctx, n, "session.send", map[string]any{
		"session_id":    agyTestRow,
		"text":          prompt3,
		"client_msg_id": "msg-refused-3",
	})
	var re *rpc.Error
	if !errors.As(err, &re) || deliveredOf(re) != deliveredNone {
		t.Fatalf("refused send err = %v, want delivered=none", err)
	}
	if err := appendUser(2, prompt3); err != nil {
		t.Fatal(err)
	}
	items, _ = agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 3 || items[2].ClientMsgID != "" {
		t.Fatalf("refused send wrongly tagged later user input: %+v", items)
	}

	// 4. A send to the companion session id agy-<conversation> routed to the live pane preserves client_msg_id and source.
	f.handle("agent.prompt", func(map[string]any) any {
		if err := appendUser(3, prompt4); err != nil {
			return herdrFail{Code: "test_write_failed", Message: err.Error()}
		}
		return map[string]any{}
	})
	if _, err := requestCtx(t, ctx, n, "session.send", map[string]any{
		"session_id":     agyAgent + "-" + agyTestID,
		"text":           prompt4,
		"client_msg_id":  "msg-companion-4",
		"message_source": map[string]any{"kind": proto.InjectedTaskNotice},
	}); err != nil {
		t.Fatalf("companion-routed agy send: %v", err)
	}
	items, _ = agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 4 || items[3].ClientMsgID != "msg-companion-4" || items[3].Source == nil || items[3].Source.Kind != proto.InjectedTaskNotice {
		t.Fatalf("companion-routed session.items = %+v, want msg-companion-4 with task_notice source", items)
	}
}
