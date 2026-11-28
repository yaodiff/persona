package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAutomaticRetirementRechecksRunningAgent(t *testing.T) {
	for _, tc := range []struct {
		closeTerminal bool
		afterKey      int32
		cause         proto.StopCause
	}{
		{true, 0, proto.StopTaskCompleted},
		{true, 1, proto.StopTaskCompleted},
		{true, 2, proto.StopTaskCompleted},
		{false, 1, proto.StopTaskCompleted},
		{true, 0, proto.StopIdle},
		{true, 1, proto.StopIdle},
	} {
		t.Run(fmt.Sprintf("%s/close_terminal=%t/input_after_key=%d", tc.cause, tc.closeTerminal, tc.afterKey), func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "project"))
			n.state.put(sidA, sessionState{Launcher: "cc", Cwd: cwd, Pane: "w1:p1"})
			f.reply("session.snapshot", snapshot(nil, nil, []any{
				claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""),
			}, nil))
			listSessions(t, n)
			f.reset()
			if !tc.closeTerminal {
				quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var keys atomic.Int32
			input := make(chan error, 1)
			inspected := make(chan struct{})
			f.after(func(c herdrCall) {
				if c.Method != "pane.send_keys" || keys.Add(1) != tc.afterKey {
					return
				}
				go func() {
					unlock, err := n.inputTurn(ctx, sidA)
					if err == nil {
						unlock()
					}
					input <- err
				}()
				waitFor(t, "input queued after an interrupt", func() bool {
					n.typing.mu.Lock()
					defer n.typing.mu.Unlock()
					return n.typing.inputs[sidA].pending > 0
				})
				close(inspected)
			})
			out, err := request(t, n, nodeproto.StopIfIdle, map[string]any{
				"session_id": sidA, "retire": true, "stop_cause": tc.cause, "close_terminal": tc.closeTerminal,
			})
			if tc.afterKey == 0 {
				if err != nil || out["closed"] != true || len(f.trace("tab.close")) != 1 {
					t.Fatalf("idle Agent's terminal was not closed: %v, %v", out, err)
				}
				return
			}
			select {
			case <-inspected:
			case <-ctx.Done():
				t.Fatal("cleanup did not reach the interrupted-input boundary")
			}
			select {
			case inputErr := <-input:
				if inputErr != nil {
					t.Fatal(inputErr)
				}
			case <-ctx.Done():
				t.Fatal("queued input did not get its turn")
			}
			if err == nil || len(f.trace("pane.send_keys")) != int(tc.afterKey) {
				t.Fatalf("cleanup passed renewed input: %v, %v", out, err)
			}
			if code, _ := refusalOf(t, err); code != notRetirable {
				t.Fatalf("cleanup reported the wrong refusal: %s", code)
			}
			if !tc.closeTerminal {
				if _, running := n.agent(sidA); !running {
					t.Fatal("the Agent was stopped with input pending")
				}
			}
			closesNothing(t, f)
			if st, _ := n.state.get(sidA); st.StopCause.AllowsSupervisorResume() {
				t.Fatal("a refused close granted automatic recovery")
			}
		})
	}
}

func TestAutomaticTerminalClosePreservesOrcaSetting(t *testing.T) {
	n, f, _ := orcaSession(t)
	out, err := request(t, n, nodeproto.StopIfIdle, map[string]any{
		"session_id": sidA, "retire": true, "stop_cause": "task_completed", "close_terminal": true,
	})
	if err != nil || out["closed"] != true || len(f.Trace("terminal.close")) != 1 {
		t.Fatalf("Orca close-on-end changed to a stop-only fallback: %v, %v, %v", out, err, f.Trace())
	}
}

func TestAutomaticTerminalCloseRechecksInputBeforeClosing(t *testing.T) {
	for _, change := range []string{"idle", "input", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			n, f, _ := closeAfterExitFixture(t)
			f.reset()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var once sync.Once
			var input chan error
			inspected := make(chan struct{})
			f.after(func(c herdrCall) {
				if c.Method != "pane.process_info" {
					return
				}
				once.Do(func() {
					switch change {
					case "input":
						input = make(chan error, 1)
						go func() {
							unlock, err := n.inputTurn(ctx, sidA)
							if err == nil {
								unlock()
							}
							input <- err
						}()
						waitFor(t, "input queued during terminal inspection", func() bool {
							n.typing.mu.Lock()
							defer n.typing.mu.Unlock()
							return n.typing.inputs[sidA].pending > 0
						})
					case "cancelled":
						cancel()
					}
					close(inspected)
				})
			})
			out, err := n.handle(ctx, nodeproto.StopIfIdle, json.RawMessage(`{"session_id":"`+sidA+`","retire":true,"stop_cause":"task_completed","close_terminal":true}`))
			select {
			case <-inspected:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not reach terminal inspection")
			}
			if input != nil {
				select {
				case inputErr := <-input:
					if inputErr != nil {
						t.Fatal(inputErr)
					}
				case <-ctx.Done():
					t.Fatal("queued input did not get its turn")
				}
			}
			if change == "idle" {
				b, _ := json.Marshal(out)
				var result struct {
					Closed bool `json:"closed"`
				}
				_ = json.Unmarshal(b, &result)
				if err != nil || !result.Closed || len(f.trace("pane.close")) != 1 {
					t.Fatalf("idle marked terminal was not closed: %v, %v", out, err)
				}
			} else {
				if err == nil {
					t.Fatal("terminal cleanup ignored activity at its irreversible boundary")
				}
				if change == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled cleanup: %v", err)
				}
				closesNothing(t, f)
			}
			if keys := f.trace("pane.send_keys"); len(keys) != 0 {
				t.Fatalf("an already exited Agent was interrupted: %v", keys)
			}
		})
	}
}
