package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSnapshotLiveConcernDoesNotAwaitReview(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		want bool
	}{
		{name: "open concern", kind: db.KindConcern, want: false},
		{name: "ordinary live delivery", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			todo := Todo{Kind: tc.kind, Delivery: &TodoDelivery{State: deliveryLive, At: time.Now()}}
			if got := deliveryAwaitsReview(todo, nil); got != tc.want {
				t.Fatalf("deliveryAwaitsReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The real snapshot projection, not a hand-built template value: delivered work and owner waits use the same words as
// the to-do pages, including a task with no attached Agent. Accepted work is completed, not awaiting review (#5732).
func TestSnapshotTasksUseDeliveryAndOwnerWaitStates(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	delivered, err := s.createTodo(ctx, "Snapshot delivery", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, delivered.ID, "conv1", []string{"result verified"}, 30)
	if _, _, err := s.updateSupervision(ctx, delivered.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	s.setTaskDelivery(ctx, delivered.ID, TodoDelivery{State: deliveryLive, At: time.Now()})
	live, err := s.createTodo(ctx, "Live without a ledger", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.setTaskDelivery(ctx, live.ID, TodoDelivery{State: deliveryLive, At: time.Now()})
	s.superviseTodo(ctx, live.ID, "conv1", []string{"result verified"}, 30)

	byID := func() map[string]struct{ State, Health string } {
		out := map[string]struct{ State, Health string }{}
		for _, task := range s.snapshotTasks(ctx, "https://hub.example.com") {
			out[task.ID] = struct{ State, Health string }{task.State, task.Health}
		}
		return out
	}
	got := byID()
	if got[delivered.ID].State != "已交付·待看" || got[delivered.ID].Health != "已交付·待看" {
		t.Fatalf("pending review projection = %+v", got[delivered.ID])
	}
	if got[live.ID].State != "已交付·待看" || got[live.ID].Health != "已交付·待看" {
		t.Fatalf("unaccepted live projection = %+v", got[live.ID])
	}

	if _, _, err := s.updateSupervision(ctx, delivered.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
		t.Fatal(err)
	}
	if got := byID()[delivered.ID]; got.State != "等你处理" || got.Health != "等你处理" {
		t.Fatalf("owner-wait projection = %+v", got)
	}
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": delivered.ID, "verdict": "accepted", "note": "verified",
		"checks": completeReviewChecks(todoNow(s, delivered.ID))}); err != nil {
		t.Fatal(err)
	}
	if got := byID()[delivered.ID]; got.State != "已完成" || got.Health != "已验收" {
		t.Fatalf("accepted projection = %+v", got)
	}
}

// The assistant snapshot uses the same decision dismissal as the to-do projection. An independent delivery approval is
// not that dismissed review and must remain an owner wait (#5732).
func TestSnapshotDecisionDismissalKeepsIndependentDeliveryApproval(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)
	e := dismissalEnv(t, &memStore{}, at, true)
	e.s.todos.todos["task"].Origin = "conv1"
	ctx := context.Background()
	if got := snapshotStatesForTest(e.s, ctx)["task"]; got.State != "等你处理" {
		t.Fatalf("undismissed control = %+v, want owner wait", got)
	}
	dismissReview(t, e, at)
	if got := snapshotStatesForTest(e.s, ctx)["task"]; got.State != "还没开始" || got.Health != "你选了不处理这个决定" {
		t.Fatalf("dismissed review projection = %+v", got)
	}
	e.s.todos.mu.Lock()
	e.s.todos.todos["task"].Delivery = &TodoDelivery{State: deliveryPR, Gate: "Approve the release", Unblock: unblockUser, At: time.Now()}
	e.s.todos.mu.Unlock()
	if got := snapshotStatesForTest(e.s, ctx)["task"]; got.State != "等你处理" || got.Health != "等你处理" {
		t.Fatalf("independent delivery approval was dismissed with the review: %+v", got)
	}
}

func TestDeliveryAwaitsReviewExcludesLiveConcern(t *testing.T) {
	live := Todo{Delivery: &TodoDelivery{State: deliveryLive, At: time.Now()}}
	if !deliveryAwaitsReview(live, nil) {
		t.Fatal("an ordinary live delivery at rest must await review")
	}
	concern := live
	concern.Kind = db.KindConcern
	if deliveryAwaitsReview(concern, nil) {
		t.Fatal("an open concern with live delivery was projected as awaiting review")
	}
}

// An open concern never files Done merely because an accepted supervision review is still stored. Only its
// secretary report closes it; the ordinary reopened-accepted rule must not override that boundary (#3582, #5732).
func TestSnapshotAcceptedOpenConcernDoesNotComplete(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Accepted but still open concern", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo.ID, "conv1", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewAccepted
		v.Reviews = []TaskReview{{Verdict: reviewAccepted, At: time.Now()}}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryLive, At: time.Now()})
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Kind = db.KindConcern
	s.todos.mu.Unlock()
	if !acceptanceStands(todoNow(s, todo.ID)) {
		t.Fatal("the accepted open-concern fixture does not have standing acceptance")
	}
	for _, task := range s.snapshotTasks(ctx, "") {
		if task.ID == todo.ID {
			if task.State != "跟进中" || task.Health != "正常" {
				t.Fatalf("accepted open concern projection = %+v, want State 跟进中 and Health 正常", task)
			}
			return
		}
	}
	t.Fatal("accepted open concern is missing from the real snapshot")
}

// Acceptance still stands after the owner reopens a task until a newer review changes it. Live delivery must not send
// that accepted work back to awaiting review in either State or Health (#5732).
func TestSnapshotReopenedAcceptedLiveDoesNotAwaitReview(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	accepted, err := s.createTodo(ctx, "Accepted then reopened", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: time.Now()})
	if err := s.linkTodoAgent(ctx, accepted.ID, "box", "s1", accepted.Title, "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, accepted.ID, "conv1", []string{"result verified"}, 30)
	s.setTaskDelivery(ctx, accepted.ID, TodoDelivery{State: deliveryLive, At: time.Now()})
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": accepted.ID, "verdict": "accepted", "note": "verified",
		"checks": completeReviewChecks(todoNow(s, accepted.ID))}); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, accepted.ID); got.DoneAt == nil {
		t.Fatal("accepted review did not close the task before the reopen")
	}
	if err := s.reopenTodo(ctx, accepted.ID); err != nil {
		t.Fatal(err)
	}
	s.setTaskDelivery(ctx, accepted.ID, TodoDelivery{State: deliveryLive, At: time.Now()})
	if got := todoNow(s, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil || !acceptanceStands(got) {
		t.Fatalf("reopened acceptance fixture = %+v", got)
	}
	ordinary, err := s.createTodo(ctx, "Ordinary unaccepted live", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, ordinary.ID, "conv1", []string{"result verified"}, 30)
	s.setTaskDelivery(ctx, ordinary.ID, TodoDelivery{State: deliveryLive, At: time.Now()})

	got := snapshotStatesForTest(s, ctx)
	if got[accepted.ID].State != "已完成" || got[accepted.ID].Health != "正常" {
		t.Fatalf("reopened accepted live projection = %+v, want completed/normal", got[accepted.ID])
	}
	if got[ordinary.ID].State != "已交付·待看" || got[ordinary.ID].Health != "已交付·待看" {
		t.Fatalf("ordinary unaccepted live control = %+v", got[ordinary.ID])
	}

	// Acceptance files only work at rest; a new working stretch stays active.
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	if got := snapshotStatesForTest(s, ctx)[accepted.ID]; got.State != "在做" || got.Health != "正常" {
		t.Fatalf("reopened accepted working control = %+v", got)
	}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: time.Now()})

	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": accepted.ID, "verdict": "rework", "note": "new evidence needs work"}); err != nil {
		t.Fatal(err)
	}
	if acceptanceStands(todoNow(s, accepted.ID)) {
		t.Fatal("a newer review left the old acceptance standing")
	}
	if got := snapshotStatesForTest(s, ctx)[accepted.ID]; got.State != "已交付·待看" || got.Health != "已交付·待看" {
		t.Fatalf("newer-review live control = %+v", got)
	}
}

// Seen files only an ordinary finished turn. Pending review, supervision and the otherwise identical unseen turn stay
// awaiting review through the real snapshot projection (#5707, #5732).
func TestSnapshotOrdinarySeenFinishedTurnIsDone(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	makeTask := func(title, sid string) Todo {
		t.Helper()
		todo, err := s.createTodo(ctx, title, "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, todo.ID, "box", sid, title, "/code/app", "claude"); err != nil {
			t.Fatal(err)
		}
		return todo
	}
	seen := makeTask("Ordinary seen", "s-seen")
	unseen := makeTask("Ordinary unseen", "s-unseen")
	pending := makeTask("Pending review seen", "s-pending")
	supervised := makeTask("Supervised seen", "s-supervised")
	stale := makeTask("Seen before later activity", "s-stale")
	live := makeTask("Unaccepted live seen", "s-live")
	for _, todo := range []Todo{pending, supervised} {
		s.superviseTodo(ctx, todo.ID, "conv1", []string{"result verified"}, 30)
	}
	if _, _, err := s.updateSupervision(ctx, pending.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	s.setTaskDelivery(ctx, live.ID, TodoDelivery{State: deliveryLive, At: time.Now()})
	for _, todo := range []Todo{seen, pending, supervised, stale, live} {
		if _, err := s.seeTodo(ctx, todo.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	moved := time.Now().Add(-time.Hour)
	sessions := make([]proto.Session, 0, 6)
	for _, sid := range []string{"s-seen", "s-unseen", "s-pending", "s-supervised", "s-stale", "s-live"} {
		at := moved
		if sid == "s-stale" {
			at = time.Now().Add(time.Minute)
		}
		sessions = append(sessions, proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusDone, State: proto.StateUnread, LastText: "finished", StateSince: at, UpdatedAt: at})
	}
	f.setSessions("box", sessions...)

	got := snapshotStatesForTest(s, ctx)
	if got[seen.ID].State != "已完成" || got[seen.ID].Health != "" {
		t.Fatalf("ordinary Seen projection = %+v, want completed", got[seen.ID])
	}
	for _, todo := range []Todo{unseen, pending, supervised, stale, live} {
		if got[todo.ID].State != "已交付·待看" {
			t.Fatalf("%s control = %+v, want awaiting review", todo.Title, got[todo.ID])
		}
	}
}

func snapshotStatesForTest(s *Service, ctx context.Context) map[string]struct{ State, Health string } {
	out := map[string]struct{ State, Health string }{}
	for _, task := range s.snapshotTasks(ctx, "https://hub.example.com") {
		out[task.ID] = struct{ State, Health string }{task.State, task.Health}
	}
	return out
}

func TestSnapshotOpenPRParkedAndConcernStates(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	st := &fakeTodoRepoStore{
		planMemStore: &planMemStore{memStore: &memStore{}},
		repos:        map[string][]repository.TaskRepository{},
	}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	ctx := context.Background()

	makeRepo := func(repoID, path string, links []repository.TaskLink) repository.TaskRepository {
		return repository.TaskRepository{
			RepoID: repoID,
			SourceSnapshot: repository.SourceSnapshot{
				Identity: repository.Identity{
					Provider:     "github",
					InstanceURL:  "https://github.com",
					Path:         path,
					CanonicalURL: "https://github.com/" + path,
				},
			},
			Links: links,
		}
	}

	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		"pr_merge": {
			ID: "pr_merge", Title: "Ready PR", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/1",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/1", State: "open", Draft: false,
				Review: "approved", Checks: "success", CheckedAt: now,
			},
		},
		"pr_review": {
			ID: "pr_review", Title: "Review PR", CreatedAt: now.Add(-9 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/2",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/2", State: "open", Draft: false,
				Review: "required", Checks: "success", CheckedAt: now,
			},
		},
		"pr_conflict": {
			ID: "pr_conflict", Title: "Conflict PR", CreatedAt: now.Add(-8 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/3",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/3", State: "open", Draft: false,
				Review: "changes_requested", Checks: "failure", Conflicts: true, CheckedAt: now,
			},
		},
		"pr_changes": {
			ID: "pr_changes", Title: "Changes PR", CreatedAt: now.Add(-7 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/4",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/4", State: "open", Draft: false,
				Review: "changes_requested", Checks: "success", CheckedAt: now,
			},
		},
		"pr_ci_fail": {
			ID: "pr_ci_fail", Title: "Failing CI PR", CreatedAt: now.Add(-6 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/5",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/5", State: "open", Draft: false,
				Review: "approved", Checks: "failure", CheckedAt: now,
			},
		},
		"pr_ci_pending": {
			ID: "pr_ci_pending", Title: "Pending CI PR", CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/6",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/6", State: "open", Draft: false,
				Review: "pending", Checks: "pending", CheckedAt: now,
			},
		},
		"pr_draft": {
			ID: "pr_draft", Title: "Draft PR", CreatedAt: now.Add(-4 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/7",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/7", State: "open", Draft: true,
				Review: "pending", Checks: "none", CheckedAt: now,
			},
		},
		"pr_stale": {
			ID: "pr_stale", Title: "Stale PR", CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/8",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/8", State: "open", Draft: false,
				Review: "approved", Checks: "success", CheckedAt: now.Add(-time.Hour),
			},
		},
		"pr_repo_linked": {
			ID: "pr_repo_linked", Title: "Repo linked open PR", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now,
		},
		"pr_ref_only": {
			ID: "pr_ref_only", Title: "Reference only repo PR", CreatedAt: now.Add(-90 * time.Second), UpdatedAt: now,
		},
		"parked_only": {
			ID: "parked_only", Title: "Parked task", CreatedAt: now.Add(-60 * time.Second), UpdatedAt: now,
			Supervision: &Supervision{
				Supervisor: "conv1",
				State:      reviewParked,
				Reviews: []TaskReview{{
					At:        now,
					Verdict:   reviewParked,
					Reason:    "Wait for partner",
					Until:     now.Add(24 * time.Hour),
					WaitingOn: "someone_else",
				}},
			},
		},
		"concern_open": {
			ID: "concern_open", Title: "Main chat concern", Kind: db.KindConcern, CreatedAt: now.Add(-45 * time.Second), UpdatedAt: now,
		},
		"concern_with_pr": {
			ID: "concern_with_pr", Title: "Concern with PR", Kind: db.KindConcern, CreatedAt: now.Add(-30 * time.Second), UpdatedAt: now,
			PRURL: "https://github.com/org/app/pull/9",
			PRStatus: &TodoPRStatus{
				URL: "https://github.com/org/app/pull/9", State: "open", Draft: false,
				Review: "required", Checks: "success", CheckedAt: now,
			},
		},
		"concern_working": {
			ID: "concern_working", Title: "Working concern", Kind: db.KindConcern, CreatedAt: now.Add(-20 * time.Second), UpdatedAt: now,
			Agents: []TodoAgent{{MachineID: "box", SessionID: "s-work", LinkedAt: now}},
		},
		"ordinary": {
			ID: "ordinary", Title: "Ordinary unstarted", CreatedAt: now.Add(-10 * time.Second), UpdatedAt: now,
		},
	}
	s.todos.mu.Unlock()

	st.repos["pr_repo_linked"] = []repository.TaskRepository{
		makeRepo("repo_app", "org/app", []repository.TaskLink{
			{Kind: "pull_request", Number: "42", CanonicalURL: "https://github.com/org/app/pull/42", Role: repository.LinkRoleDelivery},
		}),
	}
	st.repos["pr_ref_only"] = []repository.TaskRepository{
		makeRepo("repo_ref", "org/ref", []repository.TaskLink{
			{Kind: "pull_request", Number: "43", CanonicalURL: "https://github.com/org/ref/pull/43", Role: repository.LinkRoleReference},
		}),
	}
	f.setSessions("box", proto.Session{
		SessionID: "s-work", MachineID: "box", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: now,
	})

	got := snapshotStatesForTest(s, ctx)
	want := map[string]string{
		"pr_merge":        "等合入",
		"pr_review":       "PR 评审中",
		"pr_conflict":     "PR 有冲突",
		"pr_changes":      "PR 待修改",
		"pr_ci_fail":      "PR 检查失败",
		"pr_ci_pending":   "PR 检查中",
		"pr_draft":        "还没开始",
		"pr_stale":        "还没开始",
		"pr_repo_linked":  "等合入",
		"pr_ref_only":     "还没开始",
		"parked_only":     "已搁置",
		"concern_open":    "跟进中",
		"concern_with_pr": "PR 评审中",
		"concern_working": "在做",
		"ordinary":        "还没开始",
	}
	for id, wantState := range want {
		if got[id].State != wantState {
			t.Errorf("%s: State = %q, want %q", id, got[id].State, wantState)
		}
	}
}
