package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Live non-terminal external runtime sessions (for example opencode-* or hermes-*) answer no_terminal to
// session.stop; continues must fall back to session.close (matching supervise_leader.go) while staying fail-closed
// on any close or non-no_terminal refusal and never relaxing #16119's stopErrSaysSessionGone guards.
func TestStopContinuedTaskAgentsNoTerminalCloseFallback(t *testing.T) {
	newFixture := func(t *testing.T, sid, agent string, listed []proto.Session, nodeFn func(method string, params json.RawMessage) (any, error)) (*threadEnv, Todo) {
		t.Helper()
		e := newThreadEnv(t, nil)
		ctx := context.Background()
		e.f.devices = []Device{
			{ID: "node-a", Name: "Node A", Online: true, Reachable: true},
			{ID: "node-b", Name: "Node B", Online: true, Reachable: true},
		}
		e.sessions("node-a", listed...)
		e.sessions("node-b")
		todo, err := e.s.createTaskTodo(ctx, "Continue task", "Fix bug.", "m1", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.s.linkTodoAgent(ctx, todo.ID, "node-a", sid, todo.Title, "/code/app", agent); err != nil {
			t.Fatal(err)
		}
		if nodeFn != nil {
			e.f.nodes = map[string]nodeAnswer{
				deviceSessionStop: func(_ string, p json.RawMessage) (any, error) { return nodeFn(deviceSessionStop, p) },
				"session.close":   func(_ string, p json.RawMessage) (any, error) { return nodeFn("session.close", p) },
			}
		}
		return e, todoNow(e.s, todo.ID)
	}

	t.Run("no_terminal_falls_back_to_session_close_for_live_external_runtime", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			sid   string
			agent string
		}{
			{"opencode", "opencode-ses_live_01", "opencode"},
			{"hermes", "hermes-worker-01", "hermes"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var methods []string
				var targets []string
				e, todo := newFixture(t, tc.sid, tc.agent, []proto.Session{
					{SessionID: tc.sid, MachineID: "node-a", Agent: tc.agent, Status: proto.StatusWorking},
				}, func(method string, params json.RawMessage) (any, error) {
					methods = append(methods, method)
					var p struct {
						SessionID string `json:"session_id"`
					}
					_ = json.Unmarshal(params, &p)
					targets = append(targets, p.SessionID)
					if method == deviceSessionStop {
						return nil, &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"}
					}
					return map[string]any{"ok": true}, nil
				})
				stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
				if err != nil {
					t.Fatalf("expected no_terminal -> session.close fallback to succeed, got: %v", err)
				}
				if len(stopped) != 1 || stopped[0].SessionID != tc.sid {
					t.Fatalf("stopped=%+v, want [%s]", stopped, tc.sid)
				}
				if len(methods) != 2 || methods[0] != deviceSessionStop || methods[1] != "session.close" {
					t.Fatalf("methods=%v, want [session.stop session.close]", methods)
				}
				if len(targets) != 2 || targets[0] != tc.sid || targets[1] != tc.sid {
					t.Fatalf("targets=%v, want [%s %s]", targets, tc.sid, tc.sid)
				}
			})
		}
	})

	t.Run("no_terminal_close_errors_fail_closed", func(t *testing.T) {
		const sid = "opencode-ses_live_fail"
		cases := []struct {
			name     string
			closeErr error
		}{
			{"offline_error", apierr.ErrOffline},
			{"no_device_error", ErrNoDevice},
			{"timeout_error", context.DeadlineExceeded},
			{"rpc_closed", rpc.ErrClosed},
			{"runtime_away", &rpc.Error{Code: "runtime_away", Message: "OpenCode is not connected"}},
			{"not_stopped", &rpc.Error{Code: "not_stopped", Message: "the agent did not quit"}},
			{"not_retirable", &rpc.Error{Code: "not_retirable", Message: "assistant conversations are not stopped"}},
			{"codex_read_only", &rpc.Error{Code: "codex_read_only", Message: "read-only session"}},
			{"agent_busy", &rpc.Error{Code: "agent_busy", Message: "agent is busy"}},
			{"plain_non_rpc_error", errors.New("close failed")},
			{"not_found_while_listed_live", &rpc.Error{Code: codeSessionNotFound, Message: "session not found"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var methods []string
				e, todo := newFixture(t, sid, "opencode", []proto.Session{
					{SessionID: sid, MachineID: "node-a", Agent: "opencode", Status: proto.StatusWorking},
				}, func(method string, _ json.RawMessage) (any, error) {
					methods = append(methods, method)
					if method == deviceSessionStop {
						return nil, &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"}
					}
					return nil, tc.closeErr
				})
				stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
				if err == nil || !strings.Contains(err.Error(), "cannot stop agent") {
					t.Fatalf("%s: expected fail-closed stop error, got: %v", tc.name, err)
				}
				if len(stopped) != 0 {
					t.Fatalf("%s: expected no released agents, got: %+v", tc.name, stopped)
				}
				if len(methods) != 2 || methods[0] != deviceSessionStop || methods[1] != "session.close" {
					t.Fatalf("%s: methods=%v, want [session.stop session.close]", tc.name, methods)
				}
			})
		}
	})

	t.Run("non_no_terminal_stop_errors_do_not_call_session_close", func(t *testing.T) {
		const sid = "opencode-ses_refused"
		cases := []struct {
			name    string
			stopErr error
		}{
			{"offline_error", apierr.ErrOffline},
			{"timeout_error", context.DeadlineExceeded},
			{"runtime_away", &rpc.Error{Code: "runtime_away", Message: "OpenCode is not connected"}},
			{"not_stopped", &rpc.Error{Code: "not_stopped", Message: "the agent did not quit"}},
			{"not_retirable", &rpc.Error{Code: "not_retirable", Message: "still starting"}},
			{"legacy_invalid_id_for_non_agy", &rpc.Error{Message: `invalid session id "` + sid + `"`}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var methods []string
				e, todo := newFixture(t, sid, "opencode", nil, func(method string, _ json.RawMessage) (any, error) {
					methods = append(methods, method)
					return nil, tc.stopErr
				})
				stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
				if err == nil || !strings.Contains(err.Error(), "cannot stop agent") {
					t.Fatalf("%s: expected fail-closed stop error, got: %v", tc.name, err)
				}
				if len(stopped) != 0 {
					t.Fatalf("%s: expected no released agents, got: %+v", tc.name, stopped)
				}
				if len(methods) != 1 || methods[0] != deviceSessionStop {
					t.Fatalf("%s: methods=%v, want only [session.stop]", tc.name, methods)
				}
			})
		}
	})
}
