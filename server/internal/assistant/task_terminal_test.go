package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func dispatchTerminalTask(t *testing.T, optIn, receipt bool) (*Service, *fakeHub, Todo) {
	t.Helper()
	return dispatchTerminalTaskOn(t, nil, optIn, receipt)
}

func dispatchTerminalTaskOn(t *testing.T, store Store, optIn, receipt bool) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, store)
	f.answerNode(DeviceSessionStart, func(machine string, raw json.RawMessage) (any, error) {
		var in struct {
			Token string `json:"task_terminal_token"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		row := proto.Session{SessionID: "task-worker", MachineID: machine, Status: proto.StatusIdle,
			PaneID: "pane", TabID: "tab", WorkspaceID: "workspace", Agent: "claude", StartedByPersona: true}
		f.sessions[machine] = []proto.Session{row}
		out := map[string]any{"session": row}
		if in.Token != "" && receipt {
			out["task_terminal"] = devicewire.TaskTerminal{
				Token: in.Token, SessionID: row.SessionID, PaneID: row.PaneID, TabID: row.TabID,
				WorkspaceID: row.WorkspaceID, PID: 123, Process: "fixture",
			}
		}
		return out, nil
	})
	const args = `{"tasks":[{"title":"Finish a synthetic task","prompt":"Write a task receipt","machine":"box","cwd":"/work",
		"close_tab_on_accept":true,"terminal_cleanup":"closed","terminal_target":{"machine":"claimed"}}]}`
	res, err := s.toolTaskPlan(t.Context(), "conv1", json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	// The plan this call made, by its id: on Postgres, task_plans is not emptied between tests, so conv1 also has the
	// plans earlier tests made there (feed_plans_test.go).
	p, err := s.loadPlan(t.Context(), planID(t, res))
	if err != nil {
		t.Fatal(err)
	}
	if p.Tasks[0].CloseTabOnAccept || p.Tasks[0].TerminalCleanup != "" || p.Tasks[0].TerminalTarget.Machine != "" {
		t.Fatal("tool arguments supplied owner consent or a terminal receipt")
	}
	body := `{}`
	if optIn {
		body = `{"tasks":[{"close_tab_on_accept":true}]}`
	}
	if r := request(s, "POST", "/api/v1/task-plans/"+p.ID+"/start", body); r.Code != http.StatusOK {
		t.Fatalf("owner Start: %d %s", r.Code, r.Body)
	}
	waitFor(t, "task Agent linked", func() bool { return len(onlyTodo(s).Agents) == 1 })
	task := onlyTodo(s)
	pt, _, err := s.taskTerminalPlan(t.Context(), task)
	if err != nil || pt.CloseTabOnAccept != optIn || (pt.TerminalTarget.Machine != "") != (optIn && receipt) {
		t.Fatalf("saved consent and receipt = %+v, %v", pt, err)
	}
	return s, f, task
}

func TestTaskTerminalReviewOnlyAfterAcceptance(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		optIn       bool
		noReceipt   bool
		withdraw    bool
		outcome     string
		userCheck   bool
		change      func(*proto.Session)
		shouldClose bool
		closeErr    error
		unreadable  bool
	}{
		{name: "accepted", optIn: true, shouldClose: true},
		{name: "clean profile"},
		{name: "uncertain ownership", optIn: true, noReceipt: true},
		{name: "withdrawn", optIn: true, withdraw: true},
		{name: "cannot", optIn: true, outcome: outcomeCannot},
		{name: "owner verification", optIn: true, userCheck: true},
		{name: "owner viewing", optIn: true, change: func(s *proto.Session) { s.SeenAt = time.Now() }},
		{name: "queued", optIn: true, change: func(s *proto.Session) { s.Queued = []string{"More work"} }},
		{name: "interaction", optIn: true, change: func(s *proto.Session) { s.PendingInteractions = 1 }},
		{name: "working", optIn: true, change: func(s *proto.Session) { s.Status = proto.StatusWorking }},
		{name: "replaced pane", optIn: true, change: func(s *proto.Session) { s.PaneID = "replacement" }},
		{name: "disconnect after dispatch", optIn: true, shouldClose: true, closeErr: rpc.ErrClosed},
		{name: "deadline across the bus", optIn: true, shouldClose: true, closeErr: relayedDeadline},
		{name: "node failed after close", optIn: true, shouldClose: true, closeErr: nodeAfterWrite},
		{name: "unreadable close receipt", optIn: true, shouldClose: true, unreadable: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, f, task := dispatchTerminalTask(t, scenario.optIn, !scenario.noReceipt)
			path := "/api/v1/todos/" + task.ID + "/terminal-cleanup"
			r := httptest.NewRequest("DELETE", path, nil)
			r.Header.Set(RoleHeader, roleExtension)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("extension withdrew the owner's consent: %d", w.Code)
			}
			if scenario.withdraw {
				if r := request(s, "DELETE", path, ""); r.Code != http.StatusOK {
					t.Fatalf("withdraw: %d %s", r.Code, r.Body)
				}
			}
			var closes int
			f.mu.Lock()
			if scenario.change != nil {
				scenario.change(&f.sessions["box"][0])
			}
			f.nodes[devicewire.MethodCloseTaskTerminal] = func(machine string, raw json.RawMessage) (any, error) {
				closes++
				cur := todoNow(s, task.ID)
				if cur.DoneAt == nil || cur.DoneBy != db.DoneByReview {
					t.Error("close requested before the task was marked done")
				}
				pt, _, err := s.taskTerminalPlan(context.Background(), cur)
				if err != nil || pt.TerminalCleanup != devicewire.TaskTerminalPending ||
					!strings.Contains(string(raw), pt.TerminalTarget.Token) {
					t.Errorf("close lacks recorded one-shot claim or identity: %+v, %v", pt, err)
				}
				var req devicewire.TaskTerminalCloseRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				f.mu.Unlock()
				authorized := s.authorizeTaskTerminal(context.Background(), machine, req)
				repeated := s.authorizeTaskTerminal(context.Background(), machine, req)
				f.mu.Lock()
				if !authorized || repeated {
					t.Errorf("authorization = %t, repeated = %t", authorized, repeated)
				}
				f.sessions["box"] = nil
				if scenario.closeErr != nil {
					return nil, scenario.closeErr
				}
				if scenario.unreadable {
					return 42, nil
				}
				return map[string]string{"status": devicewire.TaskTerminalClosed}, nil
			}
			f.mu.Unlock()
			args := map[string]any{"todo_id": task.ID, "verdict": "accepted", "outcome": scenario.outcome, "user_check": scenario.userCheck}
			want := devicewire.TaskTerminalSkipped
			if scenario.shouldClose {
				want = devicewire.TaskTerminalClosed
			}
			if scenario.closeErr != nil || scenario.unreadable {
				want = devicewire.TaskTerminalPending
			}
			review := func() {
				t.Helper()
				res, err := runJSON(t, s, "conv1", "task_review", args)
				if err != nil || !strings.Contains(res, "Terminal cleanup: "+want) {
					t.Fatalf("review = %q, %v, want %s", res, err, want)
				}
				if want == devicewire.TaskTerminalPending && !strings.Contains(res, "may have reached the node") {
					t.Errorf("the review did not explain why terminal cleanup is pending: %s", res)
				}
			}
			review()
			if scenario.userCheck {
				review()
				got := todoNow(s, task.ID)
				if got.DoneAt != nil || got.Supervision.state(got) != reviewUserCheck || askOf(s, task.ID) == nil {
					t.Fatalf("personal verification closed or cleaned up a waiting task: %+v", got)
				}
			}
			if scenario.optIn && !scenario.userCheck {
				before := feedKinds(s, "conv1")
				review()
				if after := feedKinds(s, "conv1"); after != before {
					t.Fatalf("repeated acceptance wrote another feed item: %s → %s", before, after)
				}
			}
			if (closes == 1) != scenario.shouldClose || closes > 1 {
				t.Fatalf("close requests = %d", closes)
			}
			s.orphanWatch(t.Context(), time.Now())
			if orphan, ok := s.orphanOf(task.ID); ok {
				t.Fatalf("a reviewed task became orphaned: %+v", orphan)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, call := range f.calls {
				if call.Method == deviceSessionRetire || call.Method == "session.close" {
					t.Errorf("review used ordinary archive/close: %s", call.Method)
				}
			}
		})
	}
}

func TestTaskTerminalOwnerMarksDoneWithoutReview(t *testing.T) {
	s, f, task := dispatchTerminalTask(t, true, true)
	if r := request(s, "PATCH", "/api/v1/todos/"+task.ID, `{"done":true}`); r.Code != http.StatusOK {
		t.Fatalf("mark done: %d %s", r.Code, r.Body)
	}
	res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": task.ID, "verdict": "accepted"})
	if err != nil || !strings.Contains(res, "Terminal cleanup: skipped") {
		t.Fatalf("review of already done task = %q, %v", res, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == devicewire.MethodCloseTaskTerminal {
			t.Fatal("manually completed task acquired a cleanup claim")
		}
	}
}

func TestTaskTerminalDurableAcceptanceAndWithdrawalDuringProbe(t *testing.T) {
	database := todoTestDB(t)
	s, f, task := dispatchTerminalTaskOn(t, database, true, true)
	f.mu.Lock()
	f.nodes[devicewire.MethodCloseTaskTerminal] = func(machine string, raw json.RawMessage) (any, error) {
		f.mu.Unlock()
		defer f.mu.Lock()
		ctx := context.Background()
		var req devicewire.TaskTerminalCloseRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		row, err := database.GetTodo(ctx, task.ID)
		if err != nil || row.DoneAt == nil || !row.DoneAt.Equal(req.AcceptedAt) || row.DoneBy != db.DoneByReview {
			t.Errorf("node received attempt before durable acceptance: %+v, %v", row, err)
		}
		if r := request(s, "DELETE", "/api/v1/todos/"+task.ID+"/terminal-cleanup", ""); r.Code != http.StatusOK {
			t.Errorf("withdraw during node probe: %d %s", r.Code, r.Body)
		}
		if s.authorizeTaskTerminal(ctx, machine, req) {
			t.Error("node received permission after the owner withdrew it")
		}
		return map[string]string{"status": devicewire.TaskTerminalSkipped}, nil
	}
	f.mu.Unlock()
	res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": task.ID, "verdict": "accepted"})
	if err != nil || !strings.Contains(res, "Terminal cleanup: skipped") {
		t.Fatalf("review = %q, %v", res, err)
	}
}

func TestTaskTerminalDisappearanceBeforeAcceptanceStillOrphans(t *testing.T) {
	s, f, task := dispatchTerminalTask(t, true, true)
	f.setSessions("box")
	s.orphanWatch(t.Context(), time.Now())
	sig, ok := s.orphanSignal(todoNow(s, task.ID))
	if !ok || sig.Kind != sigOrphaned || sig.Orphan != orphanClosed {
		t.Fatalf("original worker gone before acceptance: %+v, %t", sig, ok)
	}
}

func TestTaskTerminalKeepsAgentWhenOwnerOpensItDuringProbe(t *testing.T) {
	s, f, task := dispatchTerminalTask(t, true, true)
	var probed bool
	f.mu.Lock()
	f.nodes[devicewire.MethodCloseTaskTerminal] = func(machine string, raw json.RawMessage) (any, error) {
		f.mu.Unlock()
		defer f.mu.Lock()
		var req devicewire.TaskTerminalCloseRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		probed = true
		setTaskViewer(t, s, machine, req.Terminal.SessionID, true)
		if s.authorizeTaskTerminal(t.Context(), machine, req) {
			t.Error("node received permission to close the session the owner just opened")
		}
		return map[string]string{"status": devicewire.TaskTerminalSkipped}, nil
	}
	f.mu.Unlock()
	res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": task.ID, "verdict": "accepted"})
	if err != nil || !probed || !strings.Contains(res, "Terminal cleanup: skipped") {
		t.Fatalf("review with owner activity during probe = %q, %v (probed: %t)", res, err, probed)
	}
}

// runtimeTaskRow is an idle, finished runtime-backed (Antigravity) task session Persona started, which declares that
// it can be closed; terminal says whether it also declares a terminal (its pane not listed yet).
func runtimeTaskRow(id string, terminal bool, at time.Time) proto.Session {
	canClose := true
	return proto.Session{SessionID: id, MachineID: "box", Status: proto.StatusDone, Agent: "agy",
		SourceKind: proto.SourceRuntime, SourceInstanceID: "agy", StartedByPersona: true, UpdatedAt: at,
		Capabilities: &proto.SessionCapabilities{Close: &canClose, Terminal: terminal}}
}

// stopsAndCloses are the node calls that stop, retire or close session id, whatever the RPC.
func stopsAndCloses(f *fakeHub, id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		switch c.Method {
		case deviceSessionStop, nodeproto.StopIfIdle, deviceSessionRetire, deviceSessionRetireGuarded, "session.close", "session.close_task_terminal":
		default:
			continue
		}
		if strings.Contains(string(c.Params), id) {
			out = append(out, c.Method)
		}
	}
	return out
}

// #17918: the owner's close_ended_terminals switch releases a runtime-backed task session only through
// closeEndedTerminal's session.retire close_terminal, never the runtime's raw session.close.
func TestRuntimeTaskSessionStaysOpenOnAccept(t *testing.T) {
	for _, tc := range []struct {
		name       string
		terminal   bool
		oldNode    bool
		wantRetire bool
		wantStatus string
	}{
		{name: "no terminal closes through retire", wantRetire: true, wantStatus: devicewire.TaskTerminalClosed},
		{name: "declared terminal stays refused", terminal: true, wantStatus: devicewire.TaskTerminalSkipped},
		{name: "old node refusal is not a close", oldNode: true, wantRetire: true, wantStatus: devicewire.TaskTerminalSkipped},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietServiceOn(t, nil)
			closeOnEnd(t, s, true)
			row := runtimeTaskRow("agy-runtime-task", tc.terminal, time.Now().Add(-time.Hour))
			f.mu.Lock()
			f.sessions["box"] = []proto.Session{row}
			f.mu.Unlock()
			todo := Todo{ID: fmt.Sprintf("todo_rt_accept_%t", tc.terminal), Origin: "conv1", Title: "Runtime task",
				Agents: []TodoAgent{{MachineID: "box", SessionID: row.SessionID, Cwd: "/work"}}}
			putTodo(s, todo)
			var retireCalls, closeCalls int
			f.answerNode(deviceSessionRetire, func(machine string, raw json.RawMessage) (any, error) {
				retireCalls++
				var in struct {
					SessionID     string `json:"session_id"`
					CloseTerminal bool   `json:"close_terminal"`
					AfterExit     bool   `json:"after_exit"`
					StopCause     string `json:"stop_cause"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, err
				}
				if in.SessionID != row.SessionID || !in.CloseTerminal || !in.AfterExit || in.StopCause != string(proto.StopTaskCompleted) {
					t.Errorf("session.retire params = %+v, want %s close_terminal=true after_exit=true stop_cause=%s", in, row.SessionID, proto.StopTaskCompleted)
				}
				if tc.oldNode {
					return nil, &rpc.Error{Code: "unknown_method", Message: "unknown method session.retire"}
				}
				return map[string]any{"closed": true}, nil
			})
			f.answerNode("session.close", func(machine string, raw json.RawMessage) (any, error) {
				closeCalls++
				return map[string]any{"ok": true}, nil
			})
			res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted"})
			if err != nil {
				t.Fatalf("task_review: %v", err)
			}
			if !strings.Contains(res, "Terminal cleanup: "+tc.wantStatus+".") {
				t.Fatalf("task_review = %q, want Terminal cleanup: %s.", res, tc.wantStatus)
			}
			if (retireCalls == 1) != tc.wantRetire || retireCalls > 1 || closeCalls != 0 {
				t.Fatalf("session.retire calls = %d, session.close calls = %d, want retire=%t close=0", retireCalls, closeCalls, tc.wantRetire)
			}
			if got := stopsAndCloses(f, row.SessionID); tc.wantRetire && (len(got) != 1 || got[0] != deviceSessionRetire) || !tc.wantRetire && len(got) != 0 {
				t.Fatalf("runtime task calls = %v, want retire=%t", got, tc.wantRetire)
			}
		})
	}
}

// #17918: once a runtime task session is done, the same closeEndedTerminal path retires it exactly once.
func TestCloseDoneTaskAgentsLeavesRuntimeSessions(t *testing.T) {
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	for _, tc := range []struct {
		name       string
		terminal   bool
		wantRetire bool
	}{
		{name: "no terminal retires once", wantRetire: true},
		{name: "declared terminal stays refused", terminal: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietServiceOn(t, nil)
			closeOnEnd(t, s, true)
			doneAt := time.Now().Add(-5 * time.Minute)
			row := runtimeTaskRow("agy-done-task", tc.terminal, doneAt)
			f.mu.Lock()
			f.sessions["box"] = []proto.Session{row}
			f.mu.Unlock()
			var retireCalls, closeCalls int
			f.answerNode(deviceSessionRetire, func(machine string, raw json.RawMessage) (any, error) {
				retireCalls++
				var in struct {
					SessionID     string `json:"session_id"`
					CloseTerminal bool   `json:"close_terminal"`
					AfterExit     bool   `json:"after_exit"`
					StopCause     string `json:"stop_cause"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, err
				}
				if in.SessionID != row.SessionID || !in.CloseTerminal || !in.AfterExit || in.StopCause != string(proto.StopTaskCompleted) {
					t.Errorf("session.retire params = %+v, want %s close_terminal=true after_exit=true stop_cause=%s", in, row.SessionID, proto.StopTaskCompleted)
				}
				return map[string]any{"closed": true}, nil
			})
			f.answerNode("session.close", func(machine string, raw json.RawMessage) (any, error) {
				closeCalls++
				return map[string]any{"ok": true}, nil
			})
			putTodo(s, Todo{ID: fmt.Sprintf("todo_rt_done_%t", tc.terminal), Origin: "conv1", Title: "Done runtime task",
				DoneAt: &doneAt, DoneBy: db.DoneByReview,
				Agents: []TodoAgent{{MachineID: "box", SessionID: row.SessionID, Cwd: "/work"}}})
			seen := doneSeen{}
			s.closeDoneTaskAgents(t.Context(), taskCloseRefused{}, seen)
			s.closeDoneTaskAgents(t.Context(), taskCloseRefused{}, seen)
			if (retireCalls == 1) != tc.wantRetire || retireCalls > 1 || closeCalls != 0 {
				t.Fatalf("session.retire calls = %d, session.close calls = %d, want retire=%t close=0", retireCalls, closeCalls, tc.wantRetire)
			}
			if got := stopsAndCloses(f, row.SessionID); tc.wantRetire && (len(got) != 1 || got[0] != deviceSessionRetire) || !tc.wantRetire && len(got) != 0 {
				t.Fatalf("runtime task calls = %v, want retire=%t", got, tc.wantRetire)
			}
		})
	}
}
