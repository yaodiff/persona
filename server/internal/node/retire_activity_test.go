package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAutomaticStopDoesNotOvertakeInputDuringStop(t *testing.T) {
	for _, closeTerminal := range []bool{false, true} {
		t.Run(fmt.Sprint("close_terminal=", closeTerminal), func(t *testing.T) {
			testAutomaticStopDoesNotOvertakeInput(t, closeTerminal)
		})
	}
}

func testAutomaticStopDoesNotOvertakeInput(t *testing.T, closeTerminal bool) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	n.state.put(sidA, sessionState{Launcher: "cc", Cwd: cwd})
	idle := claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")
	f.reply("session.snapshot", snapshot(nil, nil, []any{idle}, nil))
	listSessions(t, n)
	f.reset()
	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
	entered := make(chan struct{})
	var once sync.Once
	f.after(func(c herdrCall) {
		if c.Method == "session.snapshot" {
			once.Do(func() { close(entered) })
		}
	})
	held := n.typing.lock(sidA)
	defer func() {
		if held != nil {
			held()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	args, err := json.Marshal(map[string]any{"session_id": sidA, "retire": true, "stop_cause": "task_completed", "close_terminal": closeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := n.handle(ctx, nodeproto.StopIfIdle, args)
		stopped <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("automatic stop did not refresh before its irreversible boundary")
	}
	input := make(chan error, 1)
	go func() {
		unlock, err := n.inputTurn(ctx, sidA)
		if err == nil {
			unlock()
		}
		input <- err
	}()
	waitFor(t, "the incoming message waiting behind the typing lock", func() bool {
		n.typing.mu.Lock()
		defer n.typing.mu.Unlock()
		return n.typing.inputs[sidA].pending > 0
	})
	held()
	held = nil
	if err := <-input; err != nil {
		t.Fatal(err)
	}
	if code, _ := refusalOf(t, <-stopped); code != notRetirable {
		t.Fatalf("automatic stop overtook the queued message: %s", code)
	}
	if calls := f.trace("pane.send_keys"); len(calls) != 0 {
		t.Fatalf("incoming input was interrupted before the stop boundary: %v", calls)
	}
	if _, running := n.agent(sidA); !running {
		t.Fatal("the Agent was stopped while input was queued")
	}
	if st, _ := n.state.get(sidA); st.StopCause.AllowsSupervisorResume() {
		t.Fatal("a refused stop granted automatic recovery")
	}
	closesNothing(t, f)
}

func TestAutomaticStopRechecksFreshRuntimeAndInputGrace(t *testing.T) {
	for _, change := range []string{"working", "recent input", "expired input", "explicit stop", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "project"))
			idle := claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")
			n.state.put(sidA, sessionState{Launcher: "cc", Cwd: cwd})
			f.reply("session.snapshot", snapshot(nil, nil, []any{idle}, nil))
			listSessions(t, n)
			f.reset()
			quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			method := nodeproto.StopIfIdle
			if change == "working" {
				working := claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, "")
				f.reply("session.snapshot", snapshot(nil, nil, []any{working}, nil))
			} else {
				unlock, err := n.inputTurn(ctx, sidA)
				if err != nil {
					t.Fatal(err)
				}
				unlock()
				if change == "expired input" {
					n.typing.mu.Lock()
					v := n.typing.inputs[sidA]
					v.last = time.Now().Add(-stopInputIdle - time.Second)
					n.typing.inputs[sidA] = v
					n.typing.mu.Unlock()
				} else if change == "explicit stop" {
					method = "session.stop"
				} else if change == "cancelled" {
					cancel()
				}
			}
			args := `{"session_id":"` + sidA + `","retire":true,"stop_cause":"task_completed"}`
			if method == "session.stop" {
				args = `{"session_id":"` + sidA + `"}`
			}
			_, err := n.handle(ctx, method, json.RawMessage(args))
			wantStop := change == "expired input" || change == "explicit stop"
			if wantStop {
				if err != nil || len(f.trace("pane.send_keys")) != 2 {
					t.Fatalf("allowed stop: %v, keys=%v", err, f.trace("pane.send_keys"))
				}
				wantCause := proto.StopTaskCompleted
				if change == "explicit stop" {
					wantCause = proto.StopUser
				}
				if st, _ := n.state.get(sidA); st.StopCause != wantCause {
					t.Fatalf("confirmed stop lost its provenance: %v, want %v", st.StopCause, wantCause)
				}
			} else {
				if err == nil || len(f.trace("pane.send_keys")) != 0 {
					t.Fatalf("refused stop sent an interrupt: %v, keys=%v", err, f.trace("pane.send_keys"))
				}
				if change == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled stop: %v", err)
				}
			}
			closesNothing(t, f)
		})
	}
}
