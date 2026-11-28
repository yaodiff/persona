package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/mergetrain"
)

func setMergeTrainTodosV2Flag(t *testing.T, s *Service, want, expected bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"todos_v2": want, "expected_todos_v2": expected})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT todos_v2=%v: code=%d body=%s", want, w.Code, w.Body.String())
	}
}

func newMergeTrainTask(t *testing.T, s *Service, title, prURL string) Todo {
	t.Helper()
	ctx := context.Background()
	task, err := s.createTodo(ctx, title, "Track merge train PR status.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", []string{"The PR is merged."}, 0)
	s.setTaskStage(ctx, task.ID, "review", prURL)
	return todoNow(s, task.ID)
}

func findRepoSnapshot(snap mergetrain.Snapshot, repoKey string) (mergetrain.RepoSnapshot, bool) {
	for _, r := range snap.Repos {
		if r.RepoKey == repoKey {
			return r, true
		}
	}
	return mergetrain.RepoSnapshot{}, false
}

func TestTodosV2MergeTrainBypassWhenFlagOff(t *testing.T) {
	s, _, task := newPRConflictTask(t, nil)
	ctx := context.Background()
	if s.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2 default to be false")
	}

	now := time.Now().UTC()
	for i, head := range []string{"head-1", "head-2"} {
		err := s.applyPRStatus(ctx, task, TodoPRStatus{
			URL:       task.PRURL,
			State:     "open",
			HeadOID:   head,
			Review:    "required",
			Checks:    "success",
			Conflicts: true,
			CheckedAt: now.Add(time.Duration(i+1) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if snap := s.mergeTrainSnapshot(ctx); len(snap.Repos) != 0 {
		t.Fatalf("todos_v2=false populated merge train: %+v", snap)
	}
	g, ok := pendingPRConflict(s, task.ID)
	if !ok {
		t.Fatal("expected existing sigPRConflict behavior to remain intact when todos_v2=false")
	}
	if strings.Contains(g.Detail, "circuit breaker") {
		t.Fatalf("todos_v2=false leaked merge train escalation detail: %q", g.Detail)
	}
}

func TestTodosV2MergeTrainSameRepoSerialAndCrossRepoParallel(t *testing.T) {
	s, _ := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	now := time.Now().UTC()

	taskA1 := newMergeTrainTask(t, s, "Repo A first PR", "https://github.com/example/repo-a/pull/10")
	taskA2 := newMergeTrainTask(t, s, "Repo A second PR", "https://github.com/example/repo-a/pull/11")
	taskB1 := newMergeTrainTask(t, s, "Repo B first PR", "https://github.com/example/repo-b/pull/20")

	if err := s.applyPRStatus(ctx, taskA1, TodoPRStatus{
		URL: taskA1.PRURL, State: "open", HeadOID: "a1-sha", Review: "approved", Checks: "pending", CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyPRStatus(ctx, taskA2, TodoPRStatus{
		URL: taskA2.PRURL, State: "open", HeadOID: "a2-sha", Review: "approved", Checks: "success", CheckedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyPRStatus(ctx, taskB1, TodoPRStatus{
		URL: taskB1.PRURL, State: "open", HeadOID: "b1-sha", Review: "approved", Checks: "success", CheckedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	snap := s.mergeTrainSnapshot(ctx)
	repoA, ok := findRepoSnapshot(snap, "github.com/example/repo-a")
	if !ok {
		t.Fatalf("missing repo-a in snapshot: %+v", snap)
	}
	if repoA.Head == nil || repoA.Head.TodoID != taskA1.ID || repoA.Head.State != mergetrain.StateActive {
		t.Fatalf("repo-a head = %+v, want %s active", repoA.Head, taskA1.ID)
	}
	if len(repoA.Queue) != 1 || repoA.Queue[0].TodoID != taskA2.ID || repoA.Queue[0].State != mergetrain.StateQueued {
		t.Fatalf("repo-a queue = %+v, want [%s queued]", repoA.Queue, taskA2.ID)
	}

	repoB, ok := findRepoSnapshot(snap, "github.com/example/repo-b")
	if !ok {
		t.Fatalf("missing repo-b in snapshot: %+v", snap)
	}
	if repoB.Head == nil || repoB.Head.TodoID != taskB1.ID || repoB.Head.State != mergetrain.StateActive {
		t.Fatalf("repo-b head = %+v, want %s active independently", repoB.Head, taskB1.ID)
	}

	// Merging taskA1 automatically promotes taskA2 to active head in repo-a.
	if err := s.applyPRStatus(ctx, taskA1, TodoPRStatus{
		URL: taskA1.PRURL, State: "merged", HeadOID: "a1-sha", CheckedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	snap = s.mergeTrainSnapshot(ctx)
	repoA, ok = findRepoSnapshot(snap, "github.com/example/repo-a")
	if !ok {
		t.Fatalf("missing repo-a after merge: %+v", snap)
	}
	if repoA.Head == nil || repoA.Head.TodoID != taskA2.ID || repoA.Head.State != mergetrain.StateActive {
		t.Fatalf("repo-a head after merge = %+v, want %s promoted to active", repoA.Head, taskA2.ID)
	}
	if len(repoA.Queue) != 0 {
		t.Fatalf("repo-a queue after promotion = %+v, want empty", repoA.Queue)
	}
	if len(repoA.Terminal) != 1 || repoA.Terminal[0].TodoID != taskA1.ID || repoA.Terminal[0].State != mergetrain.StateMerged {
		t.Fatalf("repo-a terminal = %+v, want [%s merged]", repoA.Terminal, taskA1.ID)
	}
}

func TestTodosV2MergeTrainHumanReviewGuardNeverAutoMerges(t *testing.T) {
	s, _ := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	now := time.Now().UTC()
	task := newMergeTrainTask(t, s, "Human review gated PR", "https://github.com/example/guarded/pull/7")

	// Even when CI checks are green, PolicyHumanReviewRequired must pause at StateAwaitingApproval.
	if err := s.applyPRStatus(ctx, task, TodoPRStatus{
		URL:       task.PRURL,
		State:     "open",
		HeadOID:   "sha-1",
		Review:    "required",
		Checks:    "success",
		Conflicts: false,
		CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	snap := s.mergeTrainSnapshot(ctx)
	repo, ok := findRepoSnapshot(snap, "github.com/example/guarded")
	if !ok || repo.Head == nil {
		t.Fatalf("missing guarded repo head: %+v", snap)
	}
	if repo.Head.Policy != mergetrain.PolicyHumanReviewRequired {
		t.Fatalf("policy = %q, want %q", repo.Head.Policy, mergetrain.PolicyHumanReviewRequired)
	}
	if repo.Head.Approved {
		t.Fatal("entry was unexpectedly approved without human review")
	}
	if repo.Head.State != mergetrain.StateAwaitingApproval {
		t.Fatalf("head state = %q, want %q", repo.Head.State, mergetrain.StateAwaitingApproval)
	}

	// Re-checking with changes_requested also stays blocked and unapproved.
	out := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
		URL:       task.PRURL,
		State:     "open",
		HeadOID:   "sha-1",
		Review:    "changes_requested",
		Checks:    "success",
		CheckedAt: now.Add(time.Second),
	})
	if out.Decision.Action == mergetrain.ActionRebaseAndMerge {
		t.Fatalf("PolicyHumanReviewRequired unexpectedly allowed auto-merge: %+v", out.Decision)
	}
	if out.Decision.Action != mergetrain.ActionAwaitHumanReview || out.Decision.State != mergetrain.StateAwaitingApproval {
		t.Fatalf("decision = %+v, want await_human_review / awaiting_approval", out.Decision)
	}
}

func TestTodosV2MergeTrainConflictObservationsKeepFailureCountAndNotify(t *testing.T) {
	s, _ := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)

	ctx := t.Context()
	now := time.Now()
	task := newMergeTrainTask(t, s, "Conflicting PR", "https://github.com/example/conflict-repo/pull/42")
	for i, head := range []string{"head-a", "head-a", "head-b", "head-c"} {
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: head, Review: "approved", Checks: "failure",
			Conflicts: true, CheckedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		repo, _ := findRepoSnapshot(s.mergeTrainSnapshot(ctx), "github.com/example/conflict-repo")
		if repo.Head == nil || repo.Head.RebaseFailures != 0 || repo.Head.State != mergetrain.StateActive {
			t.Fatalf("conflict poll was counted as a rebase failure: %+v", repo.Head)
		}
	}
	sig, ok := pendingPRConflict(s, task.ID)
	if !ok || sig.Key != task.PRURL+"@head-c" || !strings.Contains(sig.Detail, "merge conflicts") {
		t.Fatalf("ordinary conflict wake missing: %+v", sig)
	}
}

func TestTodosV2MergeTrainSiblingPRGetsItsOwnTurnWake(t *testing.T) {
	s, _ := newQuietServiceOn(t, newMemBlockerStore())
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)
	ctx := t.Context()
	now := time.Now()
	const first, second = "https://github.com/example/repo/pull/15", "https://github.com/example/repo/pull/16"
	task := newMergeTrainTask(t, s, "Two PRs for one task", first)
	s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{URL: first, State: "open", CheckedAt: now})
	s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{URL: second, State: "open", CheckedAt: now})
	snap := buildMergeTrainSnapshotResponse(s.mergeTrainSnapshot(ctx), nil)
	if b := snap.ByTodo[task.ID]; b.Position != 1 {
		t.Fatalf("sibling PR overwrote the head badge: %+v", b)
	}
	out := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{URL: first, State: "merged", CheckedAt: now.Add(time.Second)})
	if out.turnWakeup.Todo != task.ID || out.turnWakeup.PRURL != second {
		t.Fatalf("second PR did not get its own turn wake: %+v", out.turnWakeup)
	}
	if _, valid := s.mergeTrainHeadForWake(out.turnWakeup); !valid {
		t.Fatalf("turn wake could not be revalidated: %+v", out.turnWakeup)
	}
}

func TestTodosV2MergeTrainRollbackClearsStateAndConcurrentIsolation(t *testing.T) {
	s1, _ := newQuietServiceOn(t, nil)
	s2, _ := newQuietServiceOn(t, nil)
	setMergeTrainTodosV2Flag(t, s1, true, false)
	setMergeTrainTodosV2Flag(t, s2, true, false)

	ctx := context.Background()
	now := time.Now().UTC()
	task1 := newMergeTrainTask(t, s1, "Service 1 PR", "https://github.com/example/shared/pull/1")
	task2 := newMergeTrainTask(t, s2, "Service 2 PR", "https://github.com/example/shared/pull/2")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 5 {
			_ = s1.applyPRStatus(ctx, task1, TodoPRStatus{
				URL: task1.PRURL, State: "open", HeadOID: "s1-head", Review: "required", Checks: "success", CheckedAt: now.Add(time.Duration(i) * time.Second),
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 5 {
			_ = s2.applyPRStatus(ctx, task2, TodoPRStatus{
				URL: task2.PRURL, State: "open", HeadOID: "s2-head", Review: "approved", Checks: "success", CheckedAt: now.Add(time.Duration(i) * time.Second),
			})
		}
	}()
	wg.Wait()

	// Both Service instances must remain completely isolated.
	r1, ok1 := findRepoSnapshot(s1.mergeTrainSnapshot(ctx), "github.com/example/shared")
	r2, ok2 := findRepoSnapshot(s2.mergeTrainSnapshot(ctx), "github.com/example/shared")
	if !ok1 || r1.Head == nil || r1.Head.TodoID != task1.ID || len(r1.Queue) != 0 {
		t.Fatalf("s1 snapshot polluted: %+v", r1)
	}
	if !ok2 || r2.Head == nil || r2.Head.TodoID != task2.ID || len(r2.Queue) != 0 {
		t.Fatalf("s2 snapshot polluted: %+v", r2)
	}

	// Disabling todos_v2 at runtime immediately clears and bypasses s1's merge train without affecting s2.
	setMergeTrainTodosV2Flag(t, s1, false, true)
	if snap := s1.mergeTrainSnapshot(ctx); len(snap.Repos) != 0 {
		t.Fatalf("disabling todos_v2 did not clear merge train: %+v", snap)
	}
	if err := s1.applyPRStatus(ctx, task1, TodoPRStatus{
		URL: task1.PRURL, State: "open", HeadOID: "s1-head-2", Review: "required", Checks: "success", CheckedAt: now.Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if snap := s1.mergeTrainSnapshot(ctx); len(snap.Repos) != 0 {
		t.Fatalf("merge train remained active after todos_v2=false: %+v", snap)
	}
	if r2After, ok := findRepoSnapshot(s2.mergeTrainSnapshot(ctx), "github.com/example/shared"); !ok || r2After.Head == nil || r2After.Head.TodoID != task2.ID {
		t.Fatalf("s2 was unexpectedly affected by s1 rollback: %+v", r2After)
	}
}

func TestTodosV2MergeTrainQueuedEscalatedEntryDoesNotResetOrCorruptHead(t *testing.T) {
	s, _ := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	now := time.Now().UTC()

	// task1 is active head with failing CI; task2 is queued behind it in the same repo.
	task1 := newMergeTrainTask(t, s, "Head PR failing CI", "https://github.com/example/serial/pull/1")
	task2 := newMergeTrainTask(t, s, "Queued PR with conflicts", "https://github.com/example/serial/pull/2")

	out1 := s.onPRStatusMergeTrain(ctx, task1, TodoPRStatus{
		URL: task1.PRURL, State: "open", HeadOID: "t1-sha", Review: "approved", Checks: "failure", CheckedAt: now,
	})
	if out1.Decision.Action != mergetrain.ActionWaitQueue || out1.Decision.TodoID != task1.ID {
		t.Fatalf("initial task1 decision = %+v, want ActionWaitQueue for %s", out1.Decision, task1.ID)
	}

	s.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL: task2.PRURL, State: "open", HeadOID: "conflicting", Conflicts: true, CheckedAt: now.Add(time.Second),
	})
	// The state machine still accepts explicit results from rebase attempts.
	_, _ = s.mergeTrain.train.RecordRebaseFailure(task2.PRURL, "first attempt failed")
	out, err := s.mergeTrain.train.RecordRebaseFailure(task2.PRURL, "second attempt failed")
	if err != nil || out.Action != mergetrain.ActionEscalateNeedsUser {
		t.Fatalf("real rebase failures did not escalate: %+v, %v", out, err)
	}

	// Clearing conflicts on queued escalated task2 with green checks MUST NOT evaluate task1 (head)
	// with task2's checks and MUST return ActionEscalateNeedsUser for task2 itself.
	out2Cleared := s.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL: task2.PRURL, State: "open", HeadOID: "t2-clean", Review: "approved", Checks: "success", Conflicts: false,
		CheckedAt: now.Add(3 * time.Second),
	})
	if out2Cleared.Decision.TodoID != task2.ID || out2Cleared.Decision.Action != mergetrain.ActionEscalateNeedsUser || out2Cleared.Decision.State != mergetrain.StateEscalated {
		t.Fatalf("queued escalated task2 cleared conflicts = %+v, want %s ActionEscalateNeedsUser/StateEscalated", out2Cleared.Decision, task2.ID)
	}

	// Merging task1 promotes task2 to head, but task2 must remain StateEscalated and never reset/auto-merge.
	mergedOut := s.onPRStatusMergeTrain(ctx, task1, TodoPRStatus{
		URL: task1.PRURL, State: "merged", HeadOID: "t1-sha", CheckedAt: now.Add(4 * time.Second),
	})
	if mergedOut.Decision.TodoID != task2.ID || mergedOut.Decision.Action != mergetrain.ActionEscalateNeedsUser || mergedOut.Decision.State != mergetrain.StateEscalated {
		t.Fatalf("merging task1 decision = %+v, want promoted %s ActionEscalateNeedsUser/StateEscalated", mergedOut.Decision, task2.ID)
	}

	out2AfterPromote := s.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL: task2.PRURL, State: "open", HeadOID: "t2-clean", Review: "approved", Checks: "success", Conflicts: false,
		CheckedAt: now.Add(5 * time.Second),
	})
	if out2AfterPromote.Decision.Action != mergetrain.ActionEscalateNeedsUser || out2AfterPromote.Decision.State != mergetrain.StateEscalated {
		t.Fatalf("promoted escalated task2 with green checks = %+v, want ActionEscalateNeedsUser/StateEscalated", out2AfterPromote.Decision)
	}

	repo, ok := findRepoSnapshot(s.mergeTrainSnapshot(ctx), "github.com/example/serial")
	if !ok || repo.Head == nil || repo.Head.TodoID != task2.ID || repo.Head.State != mergetrain.StateEscalated || repo.Head.RebaseFailures != 2 {
		t.Fatalf("repo head after promotion = %+v, want %s StateEscalated RebaseFailures=2", repo.Head, task2.ID)
	}
}

func TestTodosV2MergeTrainNonApprovedReviewBlocksAutoMerge(t *testing.T) {
	t.Run("auto_merge_entry_approval_dismissed_to_pending_or_empty", func(t *testing.T) {
		s, _ := newQuietServiceOn(t, nil)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		setMergeTrainTodosV2Flag(t, s, true, false)

		ctx := context.Background()
		now := time.Now().UTC()
		task := newMergeTrainTask(t, s, "Approved then dismissed PR", "https://github.com/example/review-guard/pull/10")

		// Poll 1: approved while checks pending -> enqueued with PolicyAutoMerge.
		first := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "sha-1", Review: "approved", Checks: "pending", CheckedAt: now,
		})
		if first.Decision.Action != mergetrain.ActionWaitQueue {
			t.Fatalf("poll 1 decision = %+v, want ActionWaitQueue", first.Decision)
		}

		// Poll 2: approval dismissed (or truncated review history) -> Review is "pending" with green checks.
		for i, review := range []string{"pending", ""} {
			out := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
				URL: task.PRURL, State: "open", HeadOID: "sha-2", Review: review, Checks: "success",
				CheckedAt: now.Add(time.Duration(i+1) * time.Second),
			})
			if out.Decision.Action == mergetrain.ActionRebaseAndMerge {
				t.Fatalf("Review=%q unexpectedly allowed ActionRebaseAndMerge: %+v", review, out.Decision)
			}
			if out.Decision.Action != mergetrain.ActionWaitQueue {
				t.Fatalf("Review=%q decision = %+v, want ActionWaitQueue", review, out.Decision)
			}
		}

		// Poll 4: once re-approved with green checks, rebase_and_merge is allowed again.
		reapproved := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "sha-2", Review: "approved", Checks: "success",
			CheckedAt: now.Add(3 * time.Second),
		})
		if reapproved.Decision.Action != mergetrain.ActionRebaseAndMerge {
			t.Fatalf("re-approved decision = %+v, want ActionRebaseAndMerge", reapproved.Decision)
		}
	})

	t.Run("human_review_entry_approved_then_dismissed", func(t *testing.T) {
		s, _ := newQuietServiceOn(t, nil)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		setMergeTrainTodosV2Flag(t, s, true, false)

		ctx := context.Background()
		now := time.Now().UTC()
		task := newMergeTrainTask(t, s, "Human review approved then dismissed", "https://github.com/example/review-guard/pull/11")

		// Poll 1: required -> PolicyHumanReviewRequired / StateAwaitingApproval.
		p1 := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "sha-1", Review: "required", Checks: "success", CheckedAt: now,
		})
		if p1.Decision.Action != mergetrain.ActionAwaitHumanReview {
			t.Fatalf("poll 1 = %+v, want ActionAwaitHumanReview", p1.Decision)
		}

		// Poll 2: approved while checks pending -> Approved=true.
		p2 := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "sha-2", Review: "approved", Checks: "pending", CheckedAt: now.Add(time.Second),
		})
		if p2.Decision.Action != mergetrain.ActionWaitQueue {
			t.Fatalf("poll 2 = %+v, want ActionWaitQueue", p2.Decision)
		}

		// Poll 3: approval dismissed to "pending" when checks turn green -> MUST NOT auto-merge.
		p3 := s.onPRStatusMergeTrain(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "sha-2", Review: "pending", Checks: "success", CheckedAt: now.Add(2 * time.Second),
		})
		if p3.Decision.Action == mergetrain.ActionRebaseAndMerge {
			t.Fatalf("dismissed approval unexpectedly allowed ActionRebaseAndMerge: %+v", p3.Decision)
		}
		if p3.Decision.Action != mergetrain.ActionWaitQueue {
			t.Fatalf("poll 3 = %+v, want ActionWaitQueue", p3.Decision)
		}
	})
}
