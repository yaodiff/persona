package node

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

func TestCodexSendSubmission(t *testing.T) {
	const text = "Check the synthetic example."
	for _, route := range []string{"host prompt", "blocked fallback"} {
		t.Run(route, func(t *testing.T) {
			for _, outcome := range []string{
				"unchanged composer", "consumed", "consumed later", "changed draft",
				"dialog", "unknown screen", "read failure", "canceled",
			} {
				t.Run(outcome, func(t *testing.T) {
					n, f, _ := codexNode(t, proto.StatusBlocked)
					// Unconfirmed outcomes wait the whole window out, so theirs is short. The two confirmed ones return
					// as soon as the box clears; "consumed later" needs several screen reads first, and a busy machine
					// must not end its window before they are done (#16008).
					success := outcome == "consumed" || outcome == "consumed later"
					codexConfirm = 50 * time.Millisecond
					if success {
						codexConfirm = 10 * time.Second
					}
					f.setScreen(cxPane, cxEmpty)
					fallback := route == "blocked fallback"
					if fallback {
						f.reply("agent.prompt", herdrFail{Code: "agent_blocked", Message: "interactive input required"})
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					submitted, reads := false, 0
					f.after(func(c herdrCall) {
						if fallback && c.Method == "pane.send_text" {
							f.setScreen(cxPane, cxBox(unpaste(str(c.Params["text"]))))
						}
						if (!fallback && c.Method == "agent.prompt") || (fallback && isKeys("enter")(c)) {
							submitted = true
							f.setScreen(cxPane, cxBox(text))
							switch outcome {
							case "consumed":
								f.setScreen(cxPane, cxEmpty)
							case "changed draft":
								f.setScreen(cxPane, cxBox("Keep this new draft."))
							case "dialog":
								f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
							case "unknown screen":
								f.setScreen(cxPane, "")
							case "read failure":
								f.reply("pane.read", herdrFail{Code: "unavailable", Message: "synthetic read failure"})
							case "canceled":
								cancel()
							}
						}
						if submitted && c.Method == "pane.read" {
							reads++
							if outcome == "consumed later" && reads == 2 {
								f.setScreen(cxPane, cxEmpty)
							}
						}
					})
					params := map[string]any{
						"session_id": cxID, "text": text, "client_msg_id": "synthetic-codex-submission",
					}
					_, err := n.handle(ctx, "session.send", mustJSON(params))
					if success {
						if err != nil {
							t.Fatalf("consumed message failed: %v", err)
						}
						if outcome == "consumed later" && reads < 3 {
							t.Fatalf("send returned before consumption was observed: %d reads", reads)
						}
					} else if !maybeDelivered(err) {
						t.Fatalf("unconfirmed submission must be uncertain, got %v", err)
					}
					pastes, enters := 0, 0
					if fallback {
						pastes, enters = 1, 1
					}
					if got := len(f.trace("pane.send_text")); got != pastes {
						t.Errorf("paste count = %d, want %d", got, pastes)
					}
					if got := len(f.trace("pane.send_keys")); got != enters {
						t.Errorf("key count = %d, want %d; must not retry Enter or clear input", got, enters)
					}
					if outcome == "unchanged composer" || outcome == "changed draft" {
						s, readErr := n.codexScreen(t.Context(), cxPane)
						want := text
						if outcome == "changed draft" {
							want = "Keep this new draft."
						}
						if readErr != nil || s.View != screen.CodexComposer || s.Draft != want {
							t.Fatalf("draft changed: screen %+v, err %v", s, readErr)
						}
					}
					// The same uncertain request must not paste or submit again.
					before := len(f.trace("agent.prompt", "pane.send_text", "pane.send_keys"))
					_, retryErr := request(t, n, "session.send", params)
					if (!success && !maybeDelivered(retryErr)) || (success && retryErr != nil) {
						t.Errorf("retry changed delivery outcome: %v", retryErr)
					}
					if got := len(f.trace("agent.prompt", "pane.send_text", "pane.send_keys")); got != before {
						t.Errorf("retry typed again: calls %d -> %d", before, got)
					}
				})
			}
		})
	}
}

func TestCodexSendPreservesMatchingDraft(t *testing.T) {
	const text = "An earlier synthetic assistant message."
	n, f, _ := codexNode(t, proto.StatusIdle)
	f.setScreen(cxPane, cxBox(text))
	n.typing.typed(cxPane, text)
	_, err := request(t, n, "session.send", map[string]any{"session_id": cxID, "text": text})
	if refusalCode(err) != screen.InputDraft {
		t.Fatalf("matching prior text is not permission to submit a draft: %v", err)
	}
	if calls := f.trace("agent.prompt", "pane.send_text", "pane.send_keys"); len(calls) != 0 {
		t.Fatalf("pre-existing draft was touched: %v", calls)
	}
}
