package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func provenanceHerdr(n *Node) {
	n.eng.name = proto.EngineHerdr
	n.eng.set = &proto.EngineChoice{Hosts: []string{proto.EngineHerdr}, Default: proto.EngineHerdr}
}

func TestStopRPCPersistsItsProvenance(t *testing.T) {
	t.Run("gateway", testGatewayStoppedProvenance)
	for _, tc := range []struct {
		name, method string
		params       map[string]any
		want         proto.StopCause
	}{
		{"idle", "session.stop", map[string]any{"retire": true, "stop_cause": proto.StopIdle}, proto.StopIdle},
		{"completed", "session.stop", map[string]any{"retire": true, "stop_cause": proto.StopTaskCompleted}, proto.StopTaskCompleted},
		{"user stop", "session.stop", map[string]any{}, proto.StopUser},
		{"user close", "session.close", map[string]any{}, proto.StopUser},
		{"archive and close", "session.retire", map[string]any{"close_terminal": true}, proto.StopUser},
		{"old retire has no provenance", "session.retire", map[string]any{}, proto.StopUnknown},
		{"confirmed maintenance stop", "session.stop", map[string]any{"retire": true, "stop_cause": proto.StopRestart}, proto.StopRestart},
		{"supervision timeout stop", "session.stop", map[string]any{"stop_cause": proto.StopTimeout}, proto.StopTimeout},
		{"non-retire stop ignores idle cause", "session.stop", map[string]any{"stop_cause": proto.StopIdle}, proto.StopUser},
		{"close terminal ignores timeout cause", "session.retire", map[string]any{"close_terminal": true, "stop_cause": proto.StopTimeout}, proto.StopUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			provenanceHerdr(n)
			withStop(t, 1, 50*time.Millisecond)
			cwd := mkdir(t, filepath.Join(home, "project"))
			n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, Pane: "w1:p1", AppMode: "plan",
				StartedAt: time.Now().Add(-time.Hour)})
			status := proto.StatusIdle
			if tc.name == "supervision timeout stop" {
				status = proto.StatusWorking
			}
			n.agents[sidA] = host.Agent{Agent: "claude", PaneID: "w1:p1", TabID: "w1:t1",
				WorkspaceID: "w1", AgentStatus: status}
			quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
			tc.params["session_id"] = sidA
			if _, err := request(t, n, tc.method, tc.params); err != nil {
				t.Fatal(err)
			}
			st, _ := n.state.get(sidA)
			if st.StopCause != tc.want || st.AppMode != "plan" || st.Launcher != "claude" {
				t.Fatalf("stop lost its provenance or launch constraints: %+v", st)
			}
			if tc.want == proto.StopTimeout {
				if err := n.supervisorResumeAllowed(sidA, "", ""); err != nil {
					t.Fatalf("supervisor resume refused after timeout stop: %v", err)
				}
			}
		})
	}
}

func TestStopChangesNeedDurableProvenanceBeforeTouchingTerminal(t *testing.T) {
	for _, tc := range []struct {
		name              string
		failWrite, resume bool
	}{
		{"saved user close", false, false},
		{"user close store failed", true, false},
		{"resume consume store failed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			provenanceHerdr(n)
			n.claudeStart, n.claudeReady = 20*time.Millisecond, 20*time.Millisecond
			cwd := mkdir(t, filepath.Join(home, "project"))
			initial := sessionState{Launcher: "claude", Cwd: cwd, Pane: "w1:p1", StopCause: proto.StopIdle, StopCauseAt: 1}
			n.state.put(sidA, initial)
			n.agents[sidA] = host.Agent{Agent: "claude", PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1"}
			left, right := net.Pipe()
			n.peer = rpc.New("node", left)
			manager := rpc.New("manager", right)
			var mu sync.Mutex
			saved := initial
			manager.OnEvent = func(event string, data json.RawMessage) {
				if event != "state.put" || tc.failWrite {
					return
				}
				var put struct {
					State sessionState `json:"state"`
				}
				_ = json.Unmarshal(data, &put)
				mu.Lock()
				saved = put.State
				mu.Unlock()
			}
			manager.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
				mu.Lock()
				defer mu.Unlock()
				return map[string]any{"sessions": map[string]sessionState{sidA: saved}}, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{}, 2)
			go func() { _ = n.peer.Serve(ctx, left); done <- struct{}{} }()
			go func() { _ = manager.Serve(ctx, right); done <- struct{}{} }()
			t.Cleanup(func() {
				cancel()
				_ = left.Close()
				_ = right.Close()
				for range 2 {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("state peer did not stop")
					}
				}
			})
			n.state.persist = func(id string, st sessionState) {
				_ = n.peer.Notify("state.put", map[string]any{"session_id": id, "state": st})
			}
			f.reset()
			var err error
			if tc.resume {
				delete(n.agents, sidA)
				_, err = request(t, n, "session.start", map[string]any{
					"resume_session_id": sidA, "resume_automatic": true,
				})
			} else {
				err = n.close(t.Context(), sidA)
			}
			if tc.failWrite {
				if err == nil {
					t.Fatal("started/closed after a failed provenance write")
				}
				if code, _ := refusalOf(t, err); code != "stop_not_saved" {
					t.Fatalf("unexpected persistence failure: %v", err)
				}
				if len(f.trace("pane.send_input", "pane.send_key", "pane.close", "tab.close", "tab.create")) != 0 {
					t.Fatal("failed provenance write still touched the terminal")
				}
			} else if err != nil || len(f.trace("tab.close", "pane.close")) != 1 {
				t.Fatalf("saved user stop did not close the terminal: %v", err)
			}
		})
	}
}

func TestUserCloseEntryPointsHoldRecoveryLock(t *testing.T) {
	for _, action := range []string{"session.close", "panes.close", "session.retire"} {
		t.Run(action, func(t *testing.T) {
			n, _, cwd := actionNode(t)
			provenanceHerdr(n)
			n.state.put(actionSession, sessionState{Launcher: "claude", Cwd: cwd, Pane: "w1:p1", StopCause: proto.StopIdle})
			observed := false
			n.state.persist = func(id string, st sessionState) {
				if id != actionSession || st.StopCause != proto.StopUser {
					return
				}
				observed = true
				if release, free := n.recovery.tryLock(id); free {
					release()
					t.Error("user closure did not serialize with guarded recovery")
				}
			}
			if _, err := request(t, n, action, map[string]any{
				"session_id": actionSession, "pane": "w1:p1", "close_terminal": true,
			}); err != nil {
				t.Fatal(err)
			}
			if !observed {
				t.Fatal("user closure did not record its stop cause")
			}
		})
	}
}

func TestUnconfirmedAutomaticStopDoesNotGrantRecovery(t *testing.T) {
	n, f, home := herdrNode(t)
	provenanceHerdr(n)
	withStop(t, 1, 20*time.Millisecond)
	cwd := mkdir(t, filepath.Join(home, "project"))
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, Pane: "w1:p1", StopCause: proto.StopIdle})
	n.agents[sidA] = host.Agent{Agent: "claude", PaneID: "w1:p1", TabID: "w1:t1",
		WorkspaceID: "w1", AgentStatus: proto.StatusIdle}
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""),
	}, nil))
	_, err := request(t, n, "session.stop", map[string]any{
		"session_id": sidA, "retire": true, "stop_cause": proto.StopIdle,
	})
	if err == nil {
		t.Fatal("scripted Agent unexpectedly stopped")
	}
	if code, _ := refusalOf(t, err); code != notStopped {
		t.Fatalf("unexpected stop failure: %v", err)
	}
	if n.stopCause(sidA) != proto.StopUnknown {
		t.Fatalf("unconfirmed stop retained authorization: %s", n.stopCause(sidA))
	}
	closesNothing(t, f)
}

func TestRestartStatusDoesNotAuthorizeUnknownStopCause(t *testing.T) {
	for _, cause := range []proto.StopCause{proto.StopUnknown, proto.StopUser, proto.StopIdle, proto.StopRestart, proto.StopCause("future")} {
		t.Run(cause.String(), func(t *testing.T) {
			n, _, home := herdrNode(t)
			provenanceHerdr(n)
			cwd := mkdir(t, filepath.Join(home, "project"))
			n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, Pane: "w1:p1", AppMode: "plan",
				StartedAt: n.bootAt.Add(-time.Hour), StopCause: cause})
			rows := []proto.Session{{SessionID: sidA, Agent: "claude", Host: proto.EngineHerdr, Status: proto.StatusOffline}}
			n.markInterrupted(rows, map[string]bool{})
			n.markStopped(&rows[0])
			if rows[0].Status != proto.StatusInterrupted || rows[0].Capabilities == nil || rows[0].Capabilities.StopCause != cause {
				t.Fatalf("restart display invented provenance: %+v", rows[0].Capabilities)
			}
			if st, _ := n.state.get(sidA); st.StopCause != cause || st.AppMode != "plan" {
				t.Fatalf("restart classification changed authorization: %+v", st)
			}
		})
	}
}

func TestRunningObservationRevokesOldResumeEligibility(t *testing.T) {
	n, _, _ := herdrNode(t)
	for _, cause := range []proto.StopCause{proto.StopIdle, proto.StopTaskCompleted, proto.StopRestart, proto.StopUser} {
		n.state.put(sidA, sessionState{Launcher: "claude", Cwd: "/work/project", AppMode: "plan", StopCause: cause})
		n.observedRunning(sidA)
		st, _ := n.state.get(sidA)
		if st.StopCause != proto.StopUnknown || st.AppMode != "plan" {
			t.Fatalf("running observation left stale eligibility: %+v", st)
		}
	}
}

func TestRunningObservationCannotEraseAnInFlightUserClose(t *testing.T) {
	n, _, _ := herdrNode(t)
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: "/work/project", StopCause: proto.StopUser})
	release := n.recovery.lock(sidA)
	n.observedRunning(sidA)
	cause := n.stopCause(sidA)
	release()
	if cause != proto.StopUser {
		t.Fatal("the still-visible process erased the user's close intent")
	}
}

func TestAcceptedAssistantInputKeepsNewerStop(t *testing.T) {
	n, _, _ := herdrNode(t)
	n.state.put(sidA, sessionState{Launcher: "claude"})
	n.state.stopped(sidA, proto.StopExit)
	before, _ := n.state.get(sidA)
	n.state.stopped(sidA, proto.StopUser)
	stopped, _ := n.state.get(sidA)

	before.AppMode = "plan"
	n.state.put(sidA, before)
	n.state.clearStopAfterAssistantInput(sidA, before.StopCauseAt)
	after, _ := n.state.get(sidA)
	if after.StopCause != proto.StopUser || after.StopCauseAt != stopped.StopCauseAt || after.AppMode != "plan" {
		t.Fatalf("accepting the input lost the newer Stop or its updated mode: %+v", after)
	}
}

func TestGuardedResumeRejectsStaleRowsAndPermissionOverrides(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cause    proto.StopCause
		mode     string
		launcher string
	}{
		{"user stopped", proto.StopUser, "", ""},
		{"exit", proto.StopExit, "", ""},
		{"future cause", proto.StopCause("future"), "", ""},
		{"permission override", proto.StopIdle, "bypassPermissions", ""},
		{"launcher override", proto.StopIdle, "", "another-launcher"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _ := keptAgent(t)
			provenanceHerdr(n)
			n.claudeStart, n.claudeReady = 20*time.Millisecond, 20*time.Millisecond
			n.state.stopped(sidA, tc.cause)
			_, err := request(t, n, "session.start", map[string]any{
				"resume_session_id": sidA, "resume_automatic": true, "prompt": "Continue the assigned task",
				"permission_mode": tc.mode, "launcher": tc.launcher,
			})
			if err == nil {
				t.Fatal("guarded resume succeeded")
			}
			if code, _ := refusalOf(t, err); code != "resume_not_allowed" {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if calls := f.trace("pane.send_input", "tab.create", "workspace.create"); len(calls) != 0 {
				t.Fatalf("a refused resume touched the terminal: %v", calls)
			}
		})
	}

	t.Run("owner closed after the assistant listed the row", func(t *testing.T) {
		n, f, _ := keptAgent(t)
		provenanceHerdr(n)
		n.claudeStart, n.claudeReady = 20*time.Millisecond, 20*time.Millisecond
		n.state.setPane(sidA, "w1:p1")
		n.state.stopped(sidA, proto.StopIdle)
		cached := proto.Session{SessionID: sidA, Agent: "claude", Host: proto.EngineHerdr, Status: proto.StatusOffline,
			Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: n.stopCause(sidA)}}
		if err := n.close(t.Context(), sidA); err != nil {
			t.Fatal(err)
		}
		f.reset()
		_, err := request(t, n, "session.start", map[string]any{
			"resume_session_id": sidA, "resume_automatic": true, "prompt": "Continue",
		})
		if err == nil {
			t.Fatal("a cached eligible row undid the user's close")
		}
		if code, _ := refusalOf(t, err); code != "resume_not_allowed" || cached.Capabilities.StopCause != proto.StopIdle {
			t.Fatalf("did not independently recheck node state: %v", err)
		}
		if len(f.trace("pane.send_input", "tab.create")) != 0 {
			t.Fatal("stale resume reached the terminal")
		}
	})
}

func TestClosingStoppedTerminalsRecordsUserIntent(t *testing.T) {
	for _, action := range []string{"pane", "finished terminals", "exit"} {
		t.Run(action, func(t *testing.T) {
			n, _, _ := keptAgent(t)
			provenanceHerdr(n)
			n.state.stopped(sidA, proto.StopIdle)
			switch action {
			case "pane":
				if err := n.closePane(t.Context(), "w1:p1"); err != nil {
					t.Fatal(err)
				}
			case "finished terminals":
				if _, err := request(t, n, "panes.close_finished", map[string]any{"preview": true}); err != nil {
					t.Fatal(err)
				}
				if n.stopCause(sidA) != proto.StopIdle {
					t.Fatal("preview changed stop provenance")
				}
				if _, err := request(t, n, "panes.close_finished", map[string]any{"panes": "w1:p1"}); err != nil {
					t.Fatal(err)
				}
			case "exit":
				if err := n.exitAgent(t.Context(), sidA); err != nil {
					t.Fatal(err)
				}
			}
			want := proto.StopUser
			if action == "exit" {
				want = proto.StopExit // the Agent was told to quit: an end of its session, never resume-eligible (#10355)
			}
			if got := n.stopCause(sidA); got != want || got.AllowsSupervisorResume() {
				t.Fatalf("user closure: stop cause %s, want %s", got, want)
			}
		})
	}
}

func TestOwnerResumeStillUsesItsExplicitPathAfterUserStop(t *testing.T) {
	n, f, _ := keptAgent(t)
	provenanceHerdr(n)
	n.state.stopped(sidA, proto.StopUser)
	n.claudeStart, n.claudeReady = 20*time.Millisecond, 20*time.Millisecond
	// The scripted shell does not launch Claude; reaching its resume command is the boundary under test.
	_, _ = request(t, n, "session.start", map[string]any{"resume_session_id": sidA})
	var command string
	for _, call := range f.calls() {
		if call.Method == "pane.send_input" {
			command += str(call.Params["text"])
		}
	}
	if !strings.Contains(command, "--resume "+sidA) {
		t.Fatal("owner Resume did not reach the existing terminal path")
	}
}

// An Agent stopped for no recorded reason (it quit by itself, a machine restart longer than the restart window cut it
// off) is not the user's stop: the assistant's resume reaches its terminal as the owner's does (#11528).
func TestSupervisorResumesAnUnknownStop(t *testing.T) {
	n, f, _ := keptAgent(t)
	provenanceHerdr(n)
	n.state.stopped(sidA, proto.StopUnknown)
	n.claudeStart, n.claudeReady = 20*time.Millisecond, 20*time.Millisecond
	// The scripted shell does not launch Claude; reaching its resume command is the boundary under test.
	_, err := request(t, n, "session.start", map[string]any{
		"resume_session_id": sidA, "resume_automatic": true, "prompt": "Continue the assigned task",
	})
	if re := (*rpc.Error)(nil); errors.As(err, &re) && re.Code == "resume_not_allowed" {
		t.Fatalf("an unknown stop was refused: %v", err)
	}
	var command string
	for _, call := range f.calls() {
		if call.Method == "pane.send_input" {
			command += str(call.Params["text"])
		}
	}
	if !strings.Contains(command, "--resume "+sidA) {
		t.Fatal("the assistant's resume of an unknown stop did not reach the terminal")
	}
}

// An uncertain resume holds off the assistant's next one for resumeSettle, unless the Agent was seen running since or
// a stop was recorded after it; a resume that ran nothing for sure does not.
func TestResumeStartsSettle(t *testing.T) {
	var r resumeStarts
	at := time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)
	r.begin("uncertain", at)(errors.New("no answer"))
	if !r.settling("uncertain", at.Add(resumeSettle-time.Second)) || r.settling("uncertain", at.Add(resumeSettle)) {
		t.Fatal("an uncertain resume did not settle after resumeSettle")
	}
	r.begin("ran", at)(nil)
	if !r.settling("ran", at.Add(time.Second)) {
		t.Fatal("a resume that returned held nothing off until its Agent was seen")
	}
	r.seen("ran")
	if r.settling("ran", at.Add(time.Second)) {
		t.Fatal("an Agent seen running still held the next resume off")
	}
	r.begin("refused", at)(refusal("start_refused", "refused", map[string]any{"delivered": deliveredNone}))
	if r.settling("refused", at.Add(time.Second)) {
		t.Fatal("a start that ran nothing held the next resume off")
	}

	n, _, _ := keptAgent(t)
	provenanceHerdr(n)
	n.uncertainResumes.begin(sidA, time.Now())(errors.New("no answer"))
	n.state.stopped(sidA, proto.StopUnknown)
	if err := n.supervisorResumeAllowed(sidA, "", ""); err == nil {
		t.Fatal("the assistant's resume went while an uncertain one was settling")
	}
	n.state.stopped(sidA, proto.StopCompactFailed) // the resumed Agent quit while it was compacted: known since
	if err := n.supervisorResumeAllowed(sidA, "", ""); err != nil {
		t.Fatalf("a stop recorded after the resume was held off: %v", err)
	}
}

func TestResumeConsumesItsPreviousStopEvenWhenStartFails(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "supervisor"}[automatic], func(t *testing.T) {
			n, f, _ := keptAgent(t)
			provenanceHerdr(n)
			n.claudeStart, n.claudeReady = 20*time.Millisecond, 20*time.Millisecond
			n.state.stopped(sidA, proto.StopIdle)
			_, _ = request(t, n, "session.start", map[string]any{
				"resume_session_id": sidA, "resume_automatic": automatic,
			})
			if n.stopCause(sidA) != proto.StopUnknown {
				t.Fatal("a failed/uncertain start retained the previous idle stop's authorization")
			}
			f.reset()
			_, err := request(t, n, "session.start", map[string]any{
				"resume_session_id": sidA, "resume_automatic": true,
			})
			if err == nil {
				t.Fatal("an uncertain previous start was resumed automatically")
			}
			if code, _ := refusalOf(t, err); code != "resume_not_allowed" {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if len(f.trace("pane.send_input", "tab.create", "workspace.create")) != 0 {
				t.Fatal("the second resume touched the terminal")
			}
		})
	}
}

func TestGuardedCodexResumeKeepsItsOriginalReadOnlyMode(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	n, f, home := herdrNode(t)
	provenanceHerdr(n)
	cwd := mkdir(t, filepath.Join(home, "project"))
	day := time.Now()
	dir := mkdir(t, filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02")))
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": startThread, "cwd": cwd}})
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-02T10-00-00-"+startThread+".jsonl"), append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, Model: "gpt-7", AppMode: "plan", StopCause: proto.StopIdle})
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "project"}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	var mu sync.Mutex
	var file codexStartFile
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		if m := startFileRE.FindStringSubmatch(str(c.Params["text"])); m != nil {
			b, _ := os.ReadFile(m[1])
			mu.Lock()
			_ = json.Unmarshal(b, &file)
			mu.Unlock()
			f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", "", proto.StatusWorking, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
		}
	})
	if _, err := request(t, n, "session.start", map[string]any{
		"resume_session_id": startThread, "prompt": "Continue", "resume_automatic": true,
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	args := strings.Join(file.Argv, " ")
	mu.Unlock()
	if !strings.Contains(args, "--sandbox read-only") || strings.Contains(args, "--dangerously-bypass") ||
		!strings.Contains(args, "--model gpt-7") || !strings.Contains(args, "resume "+startThread) {
		t.Fatalf("guarded resume changed the original constraints: %s", args)
	}
	if st, _ := n.state.get(startThread); st.AppMode != "plan" || st.Launcher != "cx" {
		t.Fatalf("stored permissions changed: %+v", st)
	}
}
