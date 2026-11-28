package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestTaskPlanTodoIDReleasesConfirmedMissingAgentOnly(t *testing.T) {
	const missingID = "agy-99999999-8888-4777-8666-555555555555"

	for _, tc := range []struct {
		name    string
		infoErr error
	}{
		{
			name:    "runtime confirmed_missing",
			infoErr: &rpc.Error{Code: codeSessionNotFound, Message: "Antigravity session not found", Data: json.RawMessage(`{"confirmed_missing":true}`)},
		},
		{
			name:    "node unknown_session",
			infoErr: &rpc.Error{Code: codeSessionNotFound, Message: "transcript not found", Data: json.RawMessage(`{"reason":"unknown_session"}`)},
		},
	} {
		t.Run("releases/"+tc.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			e.sessions("box") // live list succeeded; missingID is absent
			e.f.answerNode("session.info", func(_ string, raw json.RawMessage) (any, error) {
				if !strings.Contains(string(raw), missingID) {
					t.Errorf("unexpected session.info target: %s", raw)
				}
				return nil, tc.infoErr
			})
			putTodo(e.s, Todo{
				ID:     "todo_missing",
				Title:  "Follow up",
				Origin: "m1",
				Agents: []TodoAgent{{MachineID: "box", SessionID: missingID}},
			})

			if _, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_missing")); err != nil {
				t.Fatalf("task_plan(todo_id) should release confirmed missing agent, got: %v", err)
			}
			waitFor(t, "replacement agent linked and missing agent released", func() bool {
				got := todoNow(e.s, "todo_missing")
				var releasedOld, hasNew bool
				for _, a := range got.Agents {
					if a.SessionID == missingID && a.ReleasedAt != nil {
						releasedOld = true
					}
					if a.SessionID != missingID && a.active() {
						hasNew = true
					}
				}
				return releasedOld && hasNew
			})
		})
	}

	// Safety semantics: anything short of an authoritative missing-session answer stays fail-closed.
	for _, tc := range []struct {
		name    string
		infoRes any
		infoErr error
	}{
		{name: "aged-out session still exists on node", infoRes: map[string]any{"session_id": missingID, "agent": "agy"}},
		{name: "known session without transcript", infoErr: &rpc.Error{Code: codeSessionNotFound, Message: "transcript not found", Data: json.RawMessage(`{"reason":"no_transcript"}`)}},
		{name: "disconnected runtime plain not_found", infoErr: &rpc.Error{Code: codeSessionNotFound, Message: "transcript not found"}},
		{name: "runtime away", infoErr: &rpc.Error{Code: "runtime_away", Message: "runtime offline"}},
		{name: "legacy uncoded error", infoErr: errors.New("transcript not found")},
		{name: "timeout", infoErr: context.DeadlineExceeded},
	} {
		t.Run("stays_blocked/"+tc.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			e.sessions("box") // unlisted
			e.f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
				return tc.infoRes, tc.infoErr
			})
			putTodo(e.s, Todo{
				ID:     "todo_keep",
				Title:  "Follow up",
				Origin: "m1",
				Agents: []TodoAgent{{MachineID: "box", SessionID: missingID}},
			})

			_, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_keep"))
			if err == nil || !strings.Contains(err.Error(), "an agent is already working on it") {
				t.Fatalf("expected occupation refusal for %s, got: %v", tc.name, err)
			}
			if !todoNow(e.s, "todo_keep").Agents[0].active() || len(e.f.starts) != 0 {
				t.Fatalf("agent should stay active and no replacement should start")
			}
		})
	}

	t.Run("live_alias_never_probes_or_releases", func(t *testing.T) {
		e := newThreadEnv(t, nil)
		live := proto.Session{SessionID: "agy-w1-p1", Status: proto.StatusWorking}
		live.SetAliases([]string{missingID})
		e.sessions("box", live)
		var infoCalls atomic.Int32
		e.f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
			infoCalls.Add(1)
			return nil, &rpc.Error{Code: codeSessionNotFound, Data: json.RawMessage(`{"confirmed_missing":true}`)}
		})
		putTodo(e.s, Todo{
			ID:     "todo_alias",
			Title:  "Follow up",
			Origin: "m1",
			Agents: []TodoAgent{{MachineID: "box", SessionID: missingID}},
		})

		_, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_alias"))
		if err == nil || !strings.Contains(err.Error(), "an agent is already working on it") {
			t.Fatalf("live aliased session must block reuse, got: %v", err)
		}
		if infoCalls.Load() != 0 {
			t.Fatalf("session.info calls = %d, want 0 when alias is live", infoCalls.Load())
		}
	})
}

func TestStopContinuedTaskAgentsReleasesConfirmedMissingAndNotFound(t *testing.T) {
	const missingID = "agy-88888888-7777-4666-8555-444444444444"

	t.Run("confirmed_missing_via_session_info_skips_stop", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		e.sessions("server") // unlisted
		var stopCalls atomic.Int32
		e.f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: codeSessionNotFound, Message: "Antigravity session not found", Data: json.RawMessage(`{"confirmed_missing":true}`)}
		})
		e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
			stopCalls.Add(1)
			return nil, &rpc.Error{Message: `invalid session id "` + missingID + `"`}
		})
		todo := Todo{ID: "todo_cont_missing", Agents: []TodoAgent{{MachineID: "server", SessionID: missingID}}}

		stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
		if err != nil || len(stopped) != 1 {
			t.Fatalf("confirmed missing agent must release cleanly: stopped=%d err=%v", len(stopped), err)
		}
		if stopCalls.Load() != 0 {
			t.Fatalf("session.stop calls = %d, want 0 after session.info confirmed missing", stopCalls.Load())
		}
	})

	t.Run("typed_not_found_on_stop_releases_when_unlisted", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		e.sessions("server") // unlisted
		e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: codeSessionNotFound, Message: "no such session"}
		})
		todo := Todo{ID: "todo_cont_stop_notfound", Agents: []TodoAgent{{MachineID: "server", SessionID: missingID}}}

		stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
		if err != nil || len(stopped) != 1 {
			t.Fatalf("typed not_found on session.stop must release unlisted link: stopped=%d err=%v", len(stopped), err)
		}
	})

	t.Run("live_listed_not_found_on_stop_stays_hard_error", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		e.sessions("server", proto.Session{SessionID: missingID, Status: proto.StatusWorking})
		e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: codeSessionNotFound, Message: "no such session"}
		})
		todo := Todo{ID: "todo_cont_live_notfound", Agents: []TodoAgent{{MachineID: "server", SessionID: missingID}}}

		stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
		if err == nil || len(stopped) != 0 {
			t.Fatalf("live listed agent must stay fail-closed even on not_found: stopped=%d err=%v", len(stopped), err)
		}
	})

	t.Run("uncertain_stop_error_stays_hard_error", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		e.sessions("server") // unlisted; fake session.info returns {} (inconclusive)
		e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: "not_retirable", Message: "still starting"}
		})
		todo := Todo{ID: "todo_cont_uncertain", Agents: []TodoAgent{{MachineID: "server", SessionID: missingID}}}

		stopped, err := e.s.stopContinuedTaskAgents(context.Background(), todo)
		if err == nil || len(stopped) != 0 {
			t.Fatalf("uncertain stop refusal must stay fail-closed: stopped=%d err=%v", len(stopped), err)
		}
	})
}
