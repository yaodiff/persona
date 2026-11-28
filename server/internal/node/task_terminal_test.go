package node

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

const taskTerminalTestToken = "838e2748-4eee-46e2-9bcb-69bc5f760512"

func TestTaskTerminalFixtureProcess(t *testing.T) {
	if os.Getenv("PERSONA_TEST_IDLE_TASK_TERMINAL") == "1" {
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
	}
}

// The pane owns an idle foreground process. The package test runner may have
// children from unrelated tests, which are live work to the real cleanup check.
func idleTaskTerminalPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTaskTerminalFixtureProcess$")
	cmd.Env = append(os.Environ(), "PERSONA_TEST_IDLE_TASK_TERMINAL=1")
	cmd.SysProcAttr = osproc.DetachedAttr()
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	})
	return cmd.Process.Pid
}

func taskTerminalFixture(t *testing.T) (*Node, *fakeHerdr, devicewire.TaskTerminal, string, string) {
	t.Helper()
	n, f, home := herdrNode(t, true)
	cwd := mkdir(t, filepath.Join(home, "project"))
	writeTranscript(t, home, cwd, sidA, time.Now().Add(-time.Hour),
		`{"type":"user","message":{"role":"user","content":"Finish fixture"},"uuid":"u1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fixture finished."}]},"uuid":"a1"}`,
		`{"type":"system","subtype":"turn_duration","durationMs":1000}`)
	pid := idleTaskTerminalPID(t)
	target := devicewire.TaskTerminal{SessionID: sidA, PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1",
		Token: taskTerminalTestToken, PID: pid, Process: procStartMark(pid)}
	if target.Process == "" {
		t.Skip("the platform does not expose a process start identity")
	}
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, Pane: target.PaneID})
	n.bindPane(target.PaneID, sidA)
	f.reply("session.snapshot", taskTerminalSnapshot(target, cwd, "eligible"))
	taskTerminalForeground(f, target.PID)
	f.setScreen(target.PaneID, "Fixture finished.\n\n❯ \n")
	p := n.transcriptPath(sidA)
	if p == "" {
		t.Fatal("fixture transcript unavailable")
	}
	return n, f, target, cwd, p
}

func taskTerminalForeground(f *fakeHerdr, pid int) {
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
		map[string]any{"pid": pid, "name": "claude"},
	}}})
}

func taskTerminalSnapshot(target devicewire.TaskTerminal, cwd, scenario string) map[string]any {
	a := claudeAgent(target.SessionID, proto.StatusIdle, target.PaneID, target.TabID, target.WorkspaceID, cwd, "")
	p := shellPane(target.PaneID, target.TabID, target.WorkspaceID, cwd, target.SessionID)
	p["tokens"] = map[string]any{personaTabToken: target.SessionID, taskTerminalToken: target.Token}
	otherTab := shellPane("w1:p2", "w1:t2", "w1", cwd, "")
	panes := []any{p, otherTab}
	switch scenario {
	case "replacement":
		p["tokens"] = map[string]any{personaTabToken: target.SessionID}
	case "different session":
		a["agent_session"] = map[string]any{"kind": "id", "value": sidB}
	case "split":
		panes = append(panes, shellPane("w1:p3", target.TabID, target.WorkspaceID, cwd, ""))
	case "last tab":
		panes = []any{p, shellPane("w2:p2", "w2:t2", "w2", cwd, "")}
	case "working":
		a["agent_status"] = proto.StatusWorking
	case "blocked":
		a["agent_status"] = proto.StatusBlocked
	}
	state := snapshot(nil, nil, []any{a}, panes)
	s := state["snapshot"].(map[string]any)
	s["focused_pane_id"], s["focused_tab_id"] = "w1:p2", "w1:t2"
	if scenario == "focused" {
		s["focused_pane_id"], s["focused_tab_id"] = target.PaneID, target.TabID
	}
	if scenario == "unknown focus" {
		delete(s, "focused_pane_id")
	}
	return state
}

func askTaskTerminalClose(t *testing.T, n *Node, target devicewire.TaskTerminal) string {
	t.Helper()
	return n.closeTaskTerminal(t.Context(), target, func() bool { return true })
}

func TestTaskTerminalClosePreservesTranscriptAndWorkspace(t *testing.T) {
	n, f, target, cwd, transcript := taskTerminalFixture(t)
	before, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(cwd, "fixture.txt")
	if err := os.WriteFile(artifact, []byte("task artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	f.after(func(c herdrCall) {
		if c.Method == "pane.close" {
			f.reply("session.snapshot", taskTerminalSnapshot(target, cwd, "replacement"))
		}
	})
	if result := askTaskTerminalClose(t, n, target); result != devicewire.TaskTerminalClosed {
		t.Fatalf("eligible cleanup = %s, calls: %v", result, f.trace())
	}
	if result := askTaskTerminalClose(t, n, target); result != devicewire.TaskTerminalSkipped {
		t.Fatalf("repeated cleanup of a replacement = %s", result)
	}
	if calls := f.trace("pane.close", "tab.close", "workspace.close", "pane.send_keys", "pane.send_input"); len(calls) != 1 ||
		calls[0] != `pane.close {"pane_id":"w1:p1"}` {
		t.Fatalf("cleanup affected more than the task's original pane: %v", calls)
	}
	after, err := os.ReadFile(transcript)
	if err != nil || string(after) != string(before) {
		t.Fatalf("transcript changed: %v", err)
	}
	if after, err := os.ReadFile(artifact); err != nil || string(after) != "task artifact" {
		t.Fatalf("task artifact changed: %v", err)
	}
}

func TestTaskTerminalCloseRefusesUnsafeState(t *testing.T) {
	for _, scenario := range []string{
		"replacement", "different session", "split", "last tab", "focused", "unknown focus", "working", "blocked",
		"process replaced", "live child", "draft", "viewer", "rebound", "split during check", "focus during check", "disconnect during check",
	} {
		t.Run(scenario, func(t *testing.T) {
			n, f, target, cwd, _ := taskTerminalFixture(t)
			switch scenario {
			case "process replaced":
				taskTerminalForeground(f, target.PID+1)
			case "live child":
				// The fixture process is a real child of this runner.
				target.PID = os.Getpid()
				target.Process = procStartMark(target.PID)
				taskTerminalForeground(f, target.PID)
			case "draft":
				f.setScreen(target.PaneID, "❯ Owner draft")
			case "viewer":
				n.mu.Lock()
				n.terms = map[string]*termStream{panePrefix + target.PaneID: {}}
				n.mu.Unlock()
			case "rebound":
				n.bindPane(target.PaneID, sidB)
			case "split during check", "focus during check", "disconnect during check":
				f.after(func(c herdrCall) {
					if c.Method != "pane.process_info" {
						return
					}
					switch scenario {
					case "split during check":
						f.reply("session.snapshot", taskTerminalSnapshot(target, cwd, "split"))
					case "focus during check":
						f.reply("session.snapshot", taskTerminalSnapshot(target, cwd, "focused"))
					case "disconnect during check":
						f.reply("session.snapshot", herdrFail{Code: "unavailable", Message: "fixture disconnected"})
					}
				})
			default:
				f.reply("session.snapshot", taskTerminalSnapshot(target, cwd, scenario))
			}
			if result := askTaskTerminalClose(t, n, target); result != devicewire.TaskTerminalSkipped {
				t.Fatalf("unsafe cleanup = %s", result)
			}
			closesNothing(t, f)
		})
	}
}

func TestTaskTerminalCloseKeepsInFlightOwnerInput(t *testing.T) {
	n, f, target, _, _ := taskTerminalFixture(t)
	unlock := n.typing.lock(target.SessionID)
	defer unlock()
	if result := askTaskTerminalClose(t, n, target); result != devicewire.TaskTerminalSkipped {
		t.Fatalf("cleanup with input in flight = %s", result)
	}
	closesNothing(t, f)
}

func TestTaskTerminalCloseChecksAuthorizationAtTheNode(t *testing.T) {
	n, f, target, _, _ := taskTerminalFixture(t)
	called := false
	result := n.closeTaskTerminal(t.Context(), target, func() bool {
		called = true
		return false
	})
	if !called || result != devicewire.TaskTerminalSkipped {
		t.Fatalf("close after authorization was withdrawn = %s (checked: %t)", result, called)
	}
	closesNothing(t, f)
}

func TestTaskTerminalReceiptRequiresSpecificNewPane(t *testing.T) {
	n, _, target, _, _ := taskTerminalFixture(t)
	s := &proto.Session{SessionID: sidA, PaneID: target.PaneID, TabID: target.TabID, WorkspaceID: target.WorkspaceID, Agent: "claude"}
	for _, pane := range []string{"", "replacement"} {
		if n.taskTerminalReceipt(t.Context(), s, taskTerminalStart{token: target.Token, pane: pane}) != nil {
			t.Fatal("issued a receipt for a pane this start did not create")
		}
	}
	if got := n.taskTerminalReceipt(t.Context(), s, taskTerminalStart{token: target.Token, pane: target.PaneID}); got == nil || *got != target {
		t.Fatalf("new pane receipt = %+v, want %+v", got, target)
	}
}
