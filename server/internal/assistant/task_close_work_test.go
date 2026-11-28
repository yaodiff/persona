package assistant

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func closeTestLoop(t *testing.T, now time.Time, kind string) *proto.LoopInfo {
	t.Helper()
	name, input := "CronCreate", `{"cron":"*/30 * * * *","prompt":"Check the synthetic build","recurring":true}`
	if kind == "wakeup" {
		name, input = "ScheduleWakeup", `{"delaySeconds":1800,"prompt":"Check the synthetic build"}`
	}
	var meta transcript.Meta
	transcript.ParseLine(1, []byte(fmt.Sprintf(
		`{"type":"assistant","uuid":"a1","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"loop1","name":%q,"input":%s}]}}`,
		now.Format(time.RFC3339Nano), name, input)), &meta)
	if kind == "wakeup" {
		transcript.ParseLine(2, []byte(fmt.Sprintf(
			`{"type":"user","uuid":"u1","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"loop1","content":"Next wakeup scheduled for 12:00:00 (in 1800s)."}]}}`,
			now.Format(time.RFC3339Nano))), &meta)
	}
	if meta.Loop == nil || meta.Loop.Kind != kind {
		t.Fatalf("parsed %s: %+v", kind, meta.Loop)
	}
	return meta.Loop
}

func closeTestTodo(t *testing.T) (*fleetTest, Todo, proto.Session) {
	t.Helper()
	e := newRouteEnv(t, nil, false)
	row := e.agents[0]
	e.d.setSessions("mac", []proto.Session{row})
	return e.fleetTest, todoNow(e.s, e.todo.ID), row
}

func TestTaskCloseWorkRefusesToolsAndDefersJudge(t *testing.T) {
	for _, kind := range []string{"cron", "wakeup"} {
		t.Run(kind, func(t *testing.T) {
			ft, todo, row := closeTestTodo(t)
			s, ctx := ft.s, t.Context()
			row.Loop = closeTestLoop(t, time.Now(), kind)
			ft.d.setSessions("mac", []proto.Session{row})
			const quote = "Watch the build and tell me when it is green."
			raw, _ := json.Marshal(assistantwire.UserSpokeParams{
				SessionRef: SessionRef{MachineID: "mac", SessionID: row.SessionID},
				Text:       quote + strings.Repeat("x", 220),
			})
			if err := (&BusHub{svc: s}).event(ctx, busEvent{name: assistantwire.EventUserSpoke, params: raw}); err != nil {
				t.Fatal(err)
			}
			args := json.RawMessage(fmt.Sprintf(`{"id":%q}`, todo.ID))
			check := func(entry string, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "loop ("+kind+")") ||
					!strings.Contains(err.Error(), quote) || strings.Contains(err.Error(), strings.Repeat("x", 200)) ||
					!strings.Contains(err.Error(), "needs_user") {
					t.Fatalf("%s did not report runtime work and a clipped owner excerpt: %v", entry, err)
				}
				t.Logf("%s blocked: %s", entry, strings.Split(err.Error(), "\n")[0])
			}
			_, err := s.toolTodoDone(ctx, "server", "conv1", "todo_complete", args)
			check("todo_complete", err)
			for _, verdict := range []string{reviewAccepted, reviewCancelled} {
				_, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": verdict})
				check(verdict, err)
			}
			if close, err := s.autoDoneTodo(ctx, todo.ID, "Done", time.Now()); close || err == nil || !strings.Contains(err.Error(), "loop ("+kind+")") || strings.Contains(err.Error(), quote) {
				t.Fatalf("automatic close was not held without disclosing the excerpt in logs: %v %v", close, err)
			}
			s.applyTaskDone(ctx, todo, time.Time{}, false, taskDoneCheck{Verdict: verdictDone}, nil)
			after := todoNow(s, todo.ID)
			if after.DoneAt != nil || after.Supervision.state(after) != todo.Supervision.state(todo) || len(s.pendingQuestions()) != 0 {
				t.Fatalf("guard closed the task, recorded a review, or asked the owner: %+v", after)
			}
			t.Logf("judge deferred: done=false, review=%q, confirmation cards=0", after.Supervision.state(after))
		})
	}
}

func TestTaskCloseWorkSignals(t *testing.T) {
	ft, todo, row := closeTestTodo(t)
	now := time.Now()
	expired, future := now.Add(-time.Second), now.Add(time.Hour)
	row.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: expired}
	row.Background = []proto.BackgroundTask{{Kind: "monitor", ExpiresAt: &expired}}
	row.Work = &proto.WorkState{WakeAt: &expired}
	ft.d.setSessions("mac", []proto.Session{row})
	if got := ft.s.taskCloseWorkSnapshot(todo, now); got != nil {
		t.Fatalf("expired work blocked closure: %+v", got)
	}
	row.Background = append(row.Background, proto.BackgroundTask{Kind: "monitor", ExpiresAt: &future}, proto.BackgroundTask{Kind: "shell"})
	row.Work.WakeAt = &future
	ft.d.setSessions("mac", []proto.Session{row})
	if got := ft.s.taskCloseWorkSnapshot(todo, now); got == nil || got.items != "background work, monitor, scheduled wake" {
		t.Fatalf("active work: %+v", got)
	}
	row.Status = proto.StatusOffline
	ft.d.setSessions("mac", []proto.Session{row})
	if got := ft.s.taskCloseWorkSnapshot(todo, now); got != nil {
		t.Fatalf("offline session blocked closure: %+v", got)
	}
}

func TestTaskCloseDeclineSurvivesRestartUntilAgentTurn(t *testing.T) {
	store := todoTestDB(t)
	e := newRouteEnv(t, store, false)
	ft, todo, row := e.fleetTest, todoNow(e.s, e.todo.ID), e.agents[0]
	s, ctx := ft.s, t.Context()
	setTaskViewer(t, s, "mac", row.SessionID, true)
	args := json.RawMessage(fmt.Sprintf(`{"id":%q}`, todo.ID))
	if _, err := s.toolTodoDone(ctx, "server", "conv1", "todo_complete", args); err == nil || !strings.Contains(err.Error(), "viewing") {
		t.Fatalf("todo_complete missed activity: %v", err)
	}
	// Empty client presence without marking a just-ended viewing session.
	s.fleet.mu.Lock()
	s.fleet.clients.Viewing = nil
	s.fleet.mu.Unlock()
	shown := make(chan proto.Interaction, 1)
	go answerArchiveCard(t, s, false, shown)
	if result, err := s.toolTodoDone(ctx, "server", "conv1", "todo_complete", args); err != nil || !strings.Contains(result, "declined") {
		t.Fatalf("completion decline: %s, %v", result, err)
	}
	<-shown
	restart := New(Options{Hub: ft.f, Store: func() Store { return store }, Devices: ft.d, Logf: testLogf(t)})
	if err := restart.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	review := map[string]any{"todo_id": todo.ID, "verdict": reviewAccepted, "checks": completeReviewChecks(todo)}
	if _, err := runJSON(t, restart, "conv1", "task_review", review); err == nil || !strings.Contains(err.Error(), "user declined") {
		t.Fatalf("accepted after phone refusal: %v", err)
	}
	restart.taskWorking(ctx, "mac", row.SessionID, startRow{})
	if err := restart.todoCloseDeclineError(ctx, todoNow(restart, todo.ID)); err != nil {
		t.Fatalf("next turn did not release decline: %v", err)
	}
}

func TestTaskCloseOwnerAppConfirmation(t *testing.T) {
	for _, action := range []string{"complete", "pass"} {
		t.Run(action, func(t *testing.T) {
			ft, todo, row := closeTestTodo(t)
			s, ctx := ft.s, t.Context()
			path, method, payload := "/api/v1/todos/"+todo.ID, http.MethodPatch, `{"done":true}`
			if action == "pass" {
				_, err := runJSON(t, s, "conv1", "task_review", map[string]any{
					"todo_id": todo.ID, "verdict": "accepted", "user_check": true,
					"note": "Ready for the owner", "checks": completeReviewChecks(todo),
				})
				if err != nil {
					t.Fatal(err)
				}
				waitFor(t, "a verification ask", func() bool { return askOf(s, todo.ID) != nil })
				a := askOf(s, todo.ID)
				path, method = path+"/accept", http.MethodPost
				payload = fmt.Sprintf(`{"asked_at":%q}`, a.AskedAt.UTC().Format(timeKeyLayout))
			}
			row.Loop = closeTestLoop(t, time.Now(), "cron")
			ft.d.setSessions("mac", []proto.Session{row})
			for _, allow := range []bool{false, true} {
				res := httptest.NewRecorder()
				done := make(chan struct{})
				go func() {
					defer close(done)
					s.ServeHTTP(res, httptest.NewRequest(method, path, strings.NewReader(payload)).WithContext(ctx))
				}()
				waitFor(t, "the second confirmation", func() bool { return len(s.pendingQuestions()) == 1 })
				card := s.pendingQuestions()[0]
				if got := todoNow(s, todo.ID); got.DoneAt != nil {
					t.Fatal("first app press closed the to-do")
				}
				if !strings.Contains(card.Summary, row.Title) {
					t.Fatalf("confirmation does not name the Agent: %s", card.Summary)
				}
				raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
				if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: raw}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("confirmation did not finish the app request")
				}
				got := todoNow(s, todo.ID)
				if (got.DoneAt != nil) != allow || (res.Code == http.StatusOK) != allow {
					t.Fatalf("confirm=%v: status=%d done=%v: %s", allow, res.Code, got.DoneAt != nil, res.Body.String())
				}
				t.Logf("%s confirm=%v status=%d done=%v", action, allow, res.Code, got.DoneAt != nil)
			}
		})
	}
}

func TestTaskCloseWaitingReport(t *testing.T) {
	if got, _ := acceptGuardBlock("- Status: done\n- Waiting: merge of the change"); got != "closeout_waiting" {
		t.Fatalf("Waiting was not recognized: %q", got)
	}
	for _, text := range []string{"- Waiting:\n- Verified: done", "- Waiting: none", "Waiting for the tests finished."} {
		if got, _ := acceptGuardBlock(text); got != "" {
			t.Fatalf("no waiting should be inferred from %q: %q", text, got)
		}
	}
}

func TestTaskCloseWorkRefusesWithdrawal(t *testing.T) {
	ft, todo, row := closeTestTodo(t)
	s, ctx := ft.s, t.Context()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	}); err != nil {
		t.Fatal(err)
	}
	row.Loop = closeTestLoop(t, time.Now(), "cron")
	ft.d.setSessions("mac", []proto.Session{row})
	_, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": todo.ID, "verdict": reviewAccepted, "withdraw_needs_user": true,
		"note": "The supervisor wants to withdraw its request.",
	})
	if got := todoNow(s, todo.ID); err == nil || !strings.Contains(err.Error(), "loop (cron)") ||
		got.DoneAt != nil || got.Supervision.State != reviewNeedsUser {
		t.Fatalf("withdrawal lost the open decision: done=%v review=%q error=%v", got.DoneAt, got.Supervision.State, err)
	}
}

func TestTaskCloseWorkRefusesLeaderAndRehome(t *testing.T) {
	s, f := newQuietService(t)
	ctx := t.Context()
	lead := startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, nil)
	d := newFleetDevs()
	d.add("box", "Box")
	row := proto.Session{MachineID: "box", SessionID: worker.Agents[0].SessionID, Status: proto.StatusIdle,
		Loop: closeTestLoop(t, time.Now(), "cron")}
	d.setSessions("box", []proto.Session{row})
	s.devs = d
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		raw, _ := json.Marshal(map[string]any{"todo_id": worker.ID, "verdict": verdict})
		if _, err := s.leaderReview(ctx, lead.Agents[0], raw); err == nil || !strings.Contains(err.Error(), "loop (cron)") {
			t.Fatalf("leader %s did not refuse active work: %v", verdict, err)
		}
	}
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	if _, _, err := s.updateSupervision(ctx, lead.ID, func(v *Supervision) bool { v.Supervisor = ""; return true }); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.autoDoneTodo(ctx, lead.ID, "Finished", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader: %v %v", ok, err)
	}
	got, cancelled := s.rehomeWorker(ctx, todoNow(s, worker.ID))
	if cancelled || got.DoneAt != nil || got.Supervision.state(got) != worker.Supervision.state(worker) {
		t.Fatalf("rehome cancelled or changed the worker: cancelled=%v done=%v state=%q", cancelled, got.DoneAt, got.Supervision.state(got))
	}
}
