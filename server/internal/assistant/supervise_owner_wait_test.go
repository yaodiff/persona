package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ownerDecision is a needs_user review a decision the owner can answer (#3804).
func ownerDecision(todoID string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"todo_id": todoID, "verdict": "needs_user", "why_owner": "only_owner_knows",
		"reason": "两版公告选哪一版", "recommend": "A 版", "cost_of_waiting": "公告晚一天发",
		"options": []map[string]string{{"label": "A 版", "leads_to": "今天发 A 版"}, {"label": "B 版", "leads_to": "今天发 B 版"}}})
	return raw
}

// stopOnOwner makes the task's agent stop with drafts for the owner to pick from, and the judge say so (needs_user).
func stopOnOwner(t *testing.T, s *Service, f *fakeHub, todoID string) {
	t.Helper()
	f.judge = `{"verdict":"needs_user","reason":"两版草稿等用户挑一版再发"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "两版公告草稿都写好了，等你挑一版我再发。", 0)
	waitFor(t, "the owner wait on the ledger", func() bool { return todoNow(s, todoID).Supervision.Waits != nil })
}

// ownerWaitHeldBySupervisor: the judge's needs_user is a wait the supervisor holds, not the user's: no push, no
// needs_user state (no Needs you, no badge), the wait kept so the work cannot be accepted (decision rights).
func ownerWaitHeldBySupervisor(t *testing.T, s *Service, f *fakeHub, todoID string) {
	t.Helper()
	cur := todoNow(s, todoID)
	if cur.DoneAt != nil || cur.Supervision.Waits == nil || cur.Supervision.State == reviewNeedsUser {
		t.Fatalf("owner wait = done %v %+v, want a wait the supervisor holds", cur.DoneAt, cur.Supervision)
	}
	if w := cur.Supervision.Waits; w.PushPending || w.NotifiedAt != nil {
		t.Fatalf("owner wait marked for a push: %+v", w)
	}
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("an agent's owner wait went to the user: %+v", got)
	}
}

// An agent that stops for the user goes to its supervisor, which decides whether it is the user's to answer
// (decision rights). The user is not pushed, and the task is not in Needs you; judging the same stretch again does
// not tell the supervisor twice.
func TestNeedsUserJudgmentGoesToTheSupervisorNotTheUser(t *testing.T) {
	s, f := newQuietService(t)
	withWebBot(f, testWeb)
	f.known["side1"] = true
	if _, err := s.toolTaskPlan(context.Background(), "side1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the side task's agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if todo.Quiet || todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
		t.Fatalf("side task = %+v", todo)
	}
	f.judge = `{"verdict":"needs_user","reason":"需要你本人操作"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Spec 已完成，下一步由你发起实现。", 0)
	waitFor(t, "the owner wait", func() bool { return todoNow(s, todo.ID).Supervision.Waits != nil })
	ownerWaitHeldBySupervisor(t, s, f, todo.ID)
	if health, _ := taskHealth(todoNow(s, todo.ID), nil, time.Now()); !strings.Contains(health, "先判断是不是用户的事") {
		t.Fatalf("snapshot health = %q", health)
	}
	if !strings.Contains(ownerWaitDetail("需要你本人操作"), "用户还没收到") || !strings.Contains(ownerWaitDetail("x"), "why_owner") {
		t.Fatalf("the supervisor is not told to weigh it: %q", ownerWaitDetail("需要你本人操作"))
	}
	cur := todoNow(s, todo.ID)
	if s.noteOwnerWait(context.Background(), cur, cur.Supervision.Waits.Work, "需要你本人操作"); len(f.pushed()) != 0 {
		t.Fatal("the same wait pushed the user")
	}
}

// Reopened accepted or cancelled work: a later owner wait is the supervisor's again, not a push (#5732).
func TestReopenedTerminalNeedsUserGoesToTheSupervisor(t *testing.T) {
	for _, terminal := range []string{reviewAccepted, reviewCancelled} {
		t.Run(terminal, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			withWebBot(f, testWeb)
			reopened := todo
			sup := *todo.Supervision
			sup.State = terminal
			reopened.Supervision = &sup
			putTodo(s, reopened)
			work := time.Now().UTC()
			s.superviseJudged(context.Background(), reopened, work, taskDoneCheck{Verdict: verdictNeedsUser, Reason: "需要你本人操作"}, nil)
			ownerWaitHeldBySupervisor(t, s, f, todo.ID)
			if st := todoNow(s, todo.ID).Supervision.State; st == terminal {
				t.Fatalf("reopened %s work kept its terminal state %q", terminal, st)
			}
		})
	}
}

// Waits recorded by an earlier version as still to be pushed are never pushed now: not when the to-dos load, not
// when the settings change (main restored).
func TestLegacyPendingOwnerPushIsNeverSent(t *testing.T) {
	s, f := newQuietService(t)
	work := time.Now().UTC()
	putTodo(s, Todo{ID: "legacy-pending", Title: "旧版待推送", Origin: "box/s1",
		Supervision: &Supervision{Supervisor: "box/s1", State: reviewNeedsUser, Waits: &OwnerWait{Work: work, At: work, Reason: "需要用户本人处理", PushPending: true}}})
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	s.settingsChanged(context.Background())
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	s.settingsChanged(context.Background())
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("a legacy pending owner push was sent: %+v", got)
	}
}

type flakySupervisionStore struct {
	*db.DB
	mu       sync.Mutex
	failures int
	failed   chan struct{}
}

func (s *flakySupervisionStore) failNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 1
}

func (s *flakySupervisionStore) PutTodoSupervision(ctx context.Context, row db.TodoSupervisionRow) error {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		s.failed <- struct{}{}
		return errors.New("injected owner-wait write failure")
	}
	s.mu.Unlock()
	return s.DB.PutTodoSupervision(ctx, row)
}

// A supervision-write outage must not strand an already-judged turn with no owner wait. Leaving the turn unjudged lets
// the ordinary recovery path apply the same work again after the store returns (#5732).
func TestOwnerWaitWriteFailureLeavesTurnRecoverable(t *testing.T) {
	st := &flakySupervisionStore{DB: todoTestDB(t), failed: make(chan struct{}, 1)}
	s, f, todo := newSupervisedServiceOn(t, st)
	withWebBot(f, testWeb)
	f.judge = `{"verdict":"needs_user","reason":"需要你本人操作"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	s.tasks.mu.Lock()
	work := s.tasks.working[todo.ID]
	s.tasks.mu.Unlock()
	st.failNext()
	agentTurn(s, f, proto.StatusDone, "Spec 已完成，下一步由你发起实现。", 0)
	select {
	case <-st.failed:
	case <-time.After(2 * time.Second):
		t.Fatal("the owner-wait write was not attempted")
	}
	s.tasks.turns.mu.Lock()
	turn := s.tasks.turns.doc.Turns[todo.ID]
	s.tasks.turns.mu.Unlock()
	if turn.Judged != nil {
		t.Error("a failed owner-wait write left the turn finally judged, so recovery would skip it")
	}
	last := proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusDone, State: proto.StateUnread,
		LastText: "Spec 已完成，下一步由你发起实现。", UpdatedAt: time.Now()}
	s.turnAgain(context.Background(), todoNow(s, todo.ID), turn, last, last.LastText, work, true)
	ownerWaitHeldBySupervisor(t, s, f, todo.ID)
}

// A main-dispatched (quiet) task's owner wait goes to main like any other: quiet or not, the user is not pushed.
func TestMainQuietNeedsUserJudgmentGoesToMain(t *testing.T) {
	s, f := newQuietService(t)
	withWebBot(f, testWeb)
	if _, err := s.toolTaskPlan(context.Background(), "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the main task's agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if !todo.Quiet || todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
		t.Fatalf("main quiet task = %+v", todo)
	}
	f.judge = `{"verdict":"needs_user","reason":"需要你本人操作"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Spec 已完成，下一步由你发起实现。", 0)
	waitFor(t, "the main quiet task's owner wait", func() bool { return todoNow(s, todo.ID).Supervision.Waits != nil })
	ownerWaitHeldBySupervisor(t, s, f, todo.ID)
}

// The explicit owner-action safety net still becomes the visible wait and one push when the model itself failed.
func TestOwnerWaitSurvivesJudgeFailure(t *testing.T) {
	s, f := newQuietService(t)
	f.known["side1"] = true
	if _, err := s.toolTaskPlan(context.Background(), "side1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the side task's agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	work := time.Now().UTC()
	s.applyTaskDone(context.Background(), todo, work, true,
		taskDoneCheck{Verdict: verdictNeedsUser, Reason: "Should I proceed with the implementation?"}, errors.New("model unavailable"))
	waitFor(t, "the wait despite the judge failure", func() bool { return todoNow(s, todo.ID).Supervision.Waits != nil })
	ownerWaitHeldBySupervisor(t, s, f, todo.ID)
}

// A missing judge model does not erase the deterministic owner-action verdict before it reaches the visible wait.
func TestOwnerWaitSurvivesAnUnconfiguredJudge(t *testing.T) {
	s, f, _ := modelService(t)
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Prepare the release", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	todo = todoNow(s, todo.ID)
	check, judgeErr := judgeTaskDoneWith(ctx, s.purposeModel("inbox.judge"), taskDoneInput{
		Replies: []taskDoneReply{{TaskDoneReply: prompts.TaskDoneReply{Text: "Spec 已完成，下一步由你发起实现。"}}},
	})
	if !ai.IsNotConfigured(judgeErr) || check.Verdict != verdictNeedsUser {
		t.Fatalf("the deterministic owner action was not kept before apply: %+v, %v", check, judgeErr)
	}
	work := time.Now().UTC()
	s.applyTaskDone(ctx, todo, work, true, check, judgeErr)
	ownerWaitHeldBySupervisor(t, s, f, todo.ID)
}

// pick from) was accepted as done, and the decision was lost with it. The judge's needs_user is kept on the ledger, and
// accepting is refused while it holds; a decision for the owner is what the review has to be.
func TestAcceptedIsRefusedWhileTheWorkWaitsOnTheOwner(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	stopOnOwner(t, s, f, todo.ID)
	w := todoNow(s, todo.ID).Supervision.Waits
	if w.Reason != "两版草稿等用户挑一版再发" || w.At.IsZero() || w.Work.IsZero() {
		t.Fatalf("waits = %+v", w)
	}
	if health, _ := taskHealth(todoNow(s, todo.ID), nil, time.Now()); !strings.Contains(health, "先判断是不是用户的事") {
		t.Fatalf("snapshot health = %q", health)
	}

	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "草稿写好了"})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "only the owner can make") || !strings.Contains(err.Error(), "needs_user") ||
		!strings.Contains(err.Error(), "两版草稿等用户挑一版再发") {
		t.Fatalf("accepted while the work waits on the owner: %v", err)
	}
	now := todoNow(s, todo.ID)
	if now.DoneAt != nil || len(now.Supervision.Reviews) != 0 || now.Supervision.Waits == nil {
		t.Fatalf("a refused review changed the task: done %v, %+v", now.DoneAt, now.Supervision)
	}
	// user_check does not get round it either.
	raw, _ = json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "user_check": true})
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil {
		t.Fatal("accepted with user_check while the work waits on the owner")
	}

	// The decision goes to the owner: allowed, and the wait is answered.
	if res, err := s.runTool(ctx, "server", "conv1", "task_review", ownerDecision(todo.ID)); err != nil || !strings.Contains(res, "waits for the user") {
		t.Fatalf("needs_user: %s %v", res, err)
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewNeedsUser || v.Waits != nil {
		t.Fatalf("after the decision went to the owner: %+v", v)
	}
}

// A review writes its terminal ledger before it closes the todo. A late needs_user judgment from the reviewed work
// must not reopen that ledger in the gap, emit another signal, or leave a closed task waiting on the owner (#5732).
func TestLateOwnerWaitDoesNotOverwriteTerminalReview(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		t.Run(verdict, func(t *testing.T) {
			s, _, todo := newSupervisedService(t)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			work := time.Now().Add(-time.Minute).UTC()
			if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
				v.State = verdict
				v.Waits = nil
				v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: verdict})
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if s.noteOwnerWait(context.Background(), todoNow(s, todo.ID), work, "late owner action") {
				t.Error("a late owner wait was admitted after the terminal review")
			}
			current := todoNow(s, todo.ID)
			if current.Supervision.State != verdict || current.Supervision.Waits != nil {
				t.Errorf("the late judgment replaced the terminal ledger: %+v", current.Supervision)
			}
			s.supervise.mu.Lock()
			_, signalled := s.supervise.pending[todo.ID+"|"+sigIdle]
			s.supervise.mu.Unlock()
			if signalled {
				t.Error("the rejected owner wait emitted an idle signal")
			}
			closed, err := s.reviewDoneTodo(context.Background(), todo.ID, "review finished", verdict == reviewAccepted)
			if err != nil || !closed {
				t.Fatalf("reviewDoneTodo: closed=%t err=%v", closed, err)
			}
			current = todoNow(s, todo.ID)
			if current.DoneAt == nil || current.Supervision.State != verdict || current.Supervision.Waits != nil {
				t.Errorf("the review close left a late owner wait behind: %+v %+v", current, current.Supervision)
			}
			if s.noteOwnerWait(context.Background(), current, work, "after close") {
				t.Error("a closed todo admitted an owner wait")
			}
		})
	}
}

func TestOwnerWaitAfterReopenedWorkIsNotSuppressedByAnOlderReview(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	work := time.Now().UTC()
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
		v.State = reviewAccepted
		v.Reviews = append(v.Reviews, TaskReview{At: work.Add(-time.Hour), Verdict: reviewAccepted})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !s.noteOwnerWait(context.Background(), todoNow(s, todo.ID), work, "new work needs the owner") {
		t.Fatal("an older review suppressed a new work stretch's owner wait")
	}
	if got := todoNow(s, todo.ID).Supervision; got.State == reviewNeedsUser || got.State == reviewAccepted || got.Waits == nil {
		t.Fatalf("the new work stretch's wait is not the supervisor's: %+v", got)
	}
}

// The owner's answer reaches the agent and it goes on: the wait is over, a later not-done verdict still reaches main,
// and once the judge finds the work done the task is accepted as usual.
func TestOwnerWaitEndsWhenTheAgentGoesOn(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	stopOnOwner(t, s, f, todo.ID)

	agentTurn(s, f, proto.StatusWorking, "", 0)
	waitFor(t, "the visible owner wait cleared by work", func() bool {
		v := todoNow(s, todo.ID).Supervision
		return v.Waits == nil && v.State != reviewNeedsUser
	})
	f.judge = `{"verdict":"not_done","reason":"还没发"}`
	agentTurn(s, f, proto.StatusDone, "我按选定的 A 版改了，但还没发", 0)
	waitFor(t, "the following not-done verdict reaches main", func() bool {
		for _, text := range f.noticesTo("conv1") {
			if strings.Contains(text, "还没发") {
				return true
			}
		}
		return false
	})

	f.judge = `{"verdict":"done","reason":"按用户选的 A 版发了"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "按你选的 A 版发出去了，PR https://github.com/o/r/pull/7 已合并", 0)
	waitFor(t, "waits for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })

	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "A 版已发", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if res, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("accepted after the agent went on: %s %v", res, err)
	}
}

// A task the owner no longer wants can still be cancelled while its work waits on them.
func TestCancelledWhileTheWorkWaitsOnTheOwner(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	stopOnOwner(t, s, f, todo.ID)
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "cancelled", "note": "用户说不发公告了"})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatalf("cancelled: %v", err)
	}
	if now := todoNow(s, todo.ID); now.DoneAt == nil || now.Supervision.Waits != nil {
		t.Fatalf("after cancelling: done %v, %+v", now.DoneAt, now.Supervision)
	}
}

// The wait is kept in the database with the ledger, so a manager restart does not let the task be accepted.
func TestOwnerWaitSurvivesTheLedgerRow(t *testing.T) {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	notified := at.Add(time.Minute)
	v := &Supervision{Supervisor: "conv1", DoneWhen: []string{}, Waits: &OwnerWait{Work: at.Add(-time.Hour), At: at, Reason: "等用户挑一版", NotifiedAt: &notified}}
	back := supervisionFromRow(v.row("todo_x"))
	if back.Waits == nil || !back.Waits.At.Equal(at) || !back.Waits.Work.Equal(at.Add(-time.Hour)) || back.Waits.Reason != "等用户挑一版" ||
		back.Waits.NotifiedAt == nil || !back.Waits.NotifiedAt.Equal(notified) {
		t.Fatalf("waits back from its row = %+v", back.Waits)
	}
	pending := supervisionFromRow((&Supervision{Waits: &OwnerWait{PushPending: true}}).row("todo_pending"))
	if pending.Waits == nil || !pending.Waits.PushPending {
		t.Fatalf("pending delivery did not round-trip: %+v", pending.Waits)
	}
	c := v.clone()
	c.Waits.Reason = "changed"
	if v.Waits.Reason != "等用户挑一版" {
		t.Fatal("clone shares the wait")
	}
	if none := (&Supervision{}).row("todo_y"); none.Waits != nil {
		t.Fatalf("no wait is written as %s", none.Waits)
	}
}
