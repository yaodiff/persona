package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/prompts"
)

func pendingPRMerged(s *Service, id, url string) (superviseSignal, bool) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.pending[(superviseSignal{Todo: id, Kind: sigPRMerged, PRURL: url}).id()]
	return g, ok
}

func TestGoldenPRMergedWakeLanguages(t *testing.T) {
	const pr1 = "https://github.com/example/app/pull/42"
	const pr2 = "https://github.com/example/app/pull/41"
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		single := prompts.RenderPRMergedWake(prompts.PRMergedWake{
			Language: answerLanguage(locale),
			PRURL:    pr1,
		})
		if !strings.Contains(single, pr1) || !strings.Contains(single, "done_when") || !strings.Contains(single, "task_review(accepted)") {
			t.Fatalf("%s single PR wake lost key guidance: %s", locale, single)
		}
		golden(t, "pr_merged_wake.single."+locale, single)

		multi := prompts.RenderPRMergedWake(prompts.PRMergedWake{
			Language: answerLanguage(locale),
			PRURL:    pr1,
			OtherPRs: []string{pr2},
		})
		if !strings.Contains(multi, pr1) || !strings.Contains(multi, pr2) {
			t.Fatalf("%s multi PR wake lost PR URLs: %s", locale, multi)
		}
		golden(t, "pr_merged_wake.multi."+locale, multi)
	}
}

func TestPRMergedWakeUnparkedOpenTaskTransition(t *testing.T) {
	st := &memStore{}
	s, f, task := newPRConflictTask(t, st)
	ctx := context.Background()
	now := time.Now()

	// Observe open first, then transition to merged.
	forge := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "head-a", Checks: "success"}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)
	if _, ok := pendingPRMerged(s, task.ID, task.PRURL); ok {
		t.Fatal("open PR prematurely queued pr_merged wake")
	}

	beforeState := todoNow(s, task.ID).Supervision.State
	forge.status.State = "merged"
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))

	got := todoNow(s, task.ID)
	if got.DoneAt != nil {
		t.Fatal("merged PR blindly auto-closed the open task")
	}
	if got.Supervision.State != beforeState || got.Supervision.State == reviewPending {
		t.Fatalf("pr_merged mutated unparked supervision state %q -> %q", beforeState, got.Supervision.State)
	}
	if pendingReviewSignal(s, task.ID) {
		t.Fatal("unparked PR merge emitted sigReview instead of sigPRMerged")
	}
	g, ok := pendingPRMerged(s, task.ID, task.PRURL)
	if !ok {
		t.Fatal("unparked PR transition to merged did not queue sigPRMerged")
	}
	if !g.urgent() || !supervisionBatchImmediate(g) {
		t.Fatalf("sigPRMerged should be urgent and batch-immediate: %+v", g)
	}

	sendPendingPRConflicts(s)
	notices := f.notices()
	if len(notices) != 1 {
		t.Fatalf("got %d notices, want 1: %q", len(notices), notices)
	}
	for _, want := range []string{"signal=" + sigPRMerged, task.PRURL, "done_when", "task_review(accepted)"} {
		if !strings.Contains(notices[0], want) {
			t.Errorf("delivered pr_merged notice missing %q:\n%s", want, notices[0])
		}
	}

	// Durable marker persisted per (todo_id, pr_url, merged).
	var markers map[string]prMergeWakeMarker
	if found, err := st.Setting(ctx, prMergeWakesKey, &markers); err != nil || !found {
		t.Fatalf("expected persisted pr_merge_wakes marker: found=%v err=%v", found, err)
	}
	if _, ok := markers[prMergeWakeMarkerKey(task.ID, task.PRURL)]; !ok {
		t.Fatalf("missing canonical (todo_id, pr_url, merged) marker in %+v", markers)
	}
}

func TestPRMergedWakeUnparkedStageMergeAndNeedsUser(t *testing.T) {
	for _, mode := range []string{"stage_merge", "needs_user"} {
		t.Run(mode, func(t *testing.T) {
			st := &memStore{}
			s, f, task := newPRConflictTask(t, st)
			ctx := context.Background()
			switch mode {
			case "stage_merge":
				s.setTaskStage(ctx, task.ID, "merge", task.PRURL)
			case "needs_user":
				if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
					v.State = reviewNeedsUser
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			beforeState := todoNow(s, task.ID).Supervision.State
			s.deploy.forge = &statusForge{status: TodoPRStatus{State: "merged", HeadOID: "head-a"}}
			s.sweepPRStatuses(ctx, time.Now())

			got := todoNow(s, task.ID)
			if got.DoneAt != nil {
				t.Fatalf("%s: merged PR blindly closed the task", mode)
			}
			if got.Supervision.State != beforeState {
				t.Fatalf("%s: supervision state changed %q -> %q", mode, beforeState, got.Supervision.State)
			}
			if _, ok := pendingPRMerged(s, task.ID, task.PRURL); !ok {
				t.Fatalf("%s: did not queue sigPRMerged", mode)
			}
			sendPendingPRConflicts(s)
			if notices := f.notices(); len(notices) != 1 || !strings.Contains(notices[0], "signal="+sigPRMerged) {
				t.Fatalf("%s: expected 1 sigPRMerged notice, got %q", mode, notices)
			}
		})
	}
}

func TestPRMergedWakeMultiPRWaitsUntilAllDeliveryPRsTerminal(t *testing.T) {
	st := &todoReadyRepoStore{memStore: &memStore{}}
	s, f, task := newPRConflictTask(t, st)
	ctx := context.Background()
	now := time.Now()
	const pr1 = "https://github.com/example/app/pull/11"
	const pr2 = "https://github.com/example/app/pull/12"
	const refPR = "https://github.com/example/app/pull/99"

	s.setTaskStage(ctx, task.ID, "review", pr1)
	s.setTaskStage(ctx, task.ID, "review", pr2)
	st.setRepo(task.ID, repository.TaskSet{
		TodoID: task.ID,
		Repositories: []repository.TaskRepository{{
			Links: []repository.TaskLink{
				{Kind: "pull_request", CanonicalURL: pr1, Role: repository.LinkRoleDelivery},
				{Kind: "pull_request", CanonicalURL: pr2, Role: repository.LinkRoleDelivery},
				{Kind: "pull_request", CanonicalURL: refPR, Role: repository.LinkRoleReference},
			},
		}},
	})

	forge := &prsForge{byNumber: map[int]TodoPRStatus{
		11: {State: "open", HeadOID: "h11"},
		12: {State: "open", HeadOID: "h12"},
	}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)

	// PR 11 merges while delivery PR 12 is still open -> must NOT wake yet.
	forge.byNumber[11] = TodoPRStatus{State: "merged", HeadOID: "h11"}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, pr1); ok {
		t.Fatal("woke on PR 11 merge while delivery PR 12 was still open")
	}

	// PR 12 now merges (even though reference PR 99 is unobserved/open) -> wakes once naming both delivery PRs.
	forge.byNumber[12] = TodoPRStatus{State: "merged", HeadOID: "h12"}
	s.sweepPRStatuses(ctx, now.Add(2*deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, pr2); !ok {
		t.Fatal("did not queue sigPRMerged after last open delivery PR merged")
	}
	sendPendingPRConflicts(s)
	notices := f.notices()
	if len(notices) != 1 || !strings.Contains(notices[0], pr2) || !strings.Contains(notices[0], pr1) {
		t.Fatalf("multi-PR merge wake = %q; want 1 notice naming %s and %s", notices, pr2, pr1)
	}
}

func TestPRMergedWakeReferenceOnlyPRDoesNotWake(t *testing.T) {
	st := &todoReadyRepoStore{memStore: &memStore{}}
	s, _, task := newPRConflictTask(t, st)
	ctx := context.Background()
	now := time.Now()
	st.setRepo(task.ID, repository.TaskSet{
		TodoID: task.ID,
		Repositories: []repository.TaskRepository{{
			Links: []repository.TaskLink{
				{Kind: "pull_request", CanonicalURL: task.PRURL, Role: repository.LinkRoleReference},
			},
		}},
	})
	forge := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)
	forge.status.State = "merged"
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, task.PRURL); ok {
		t.Fatal("reference-role PR merge unexpectedly queued sigPRMerged")
	}
}

func TestPRMergedWakePreservesParkedTasksAndUnrelatedParks(t *testing.T) {
	t.Run("merge park uses sigReview without duplicate sigPRMerged", func(t *testing.T) {
		s, _, task := newPRConflictTask(t, &memStore{})
		ctx := context.Background()
		if _, err := reviewErr(s, task.ID, mergePark(task.PRURL)); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		forge := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
		s.deploy.forge = forge
		s.sweepPRStatuses(ctx, now)
		forge.status.State = "merged"
		s.sweepPRStatuses(ctx, now.Add(deployRecheck))
		got := todoNow(s, task.ID)
		if got.Supervision.State != reviewPending || !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
			t.Fatalf("merge-parked task state=%q signals=%v, want reviewPending + [review]", got.Supervision.State, pendingKinds(s, task.ID))
		}
	})

	t.Run("unrelated active park stays parked and still hears the merge (#14049)", func(t *testing.T) {
		s, _, task := newPRConflictTask(t, &memStore{})
		ctx, now := context.Background(), time.Now()
		if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
			t.Fatal(err)
		}
		forge := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
		s.deploy.forge = forge
		s.sweepPRStatuses(ctx, now)
		forge.status.State = "merged"
		s.sweepPRStatuses(ctx, now.Add(deployRecheck))
		got := todoNow(s, task.ID)
		if got.Supervision.State != reviewParked {
			t.Fatalf("unrelated park was ended: state=%q", got.Supervision.State)
		}
		if _, ok := pendingPRMerged(s, task.ID, task.PRURL); !ok || pendingReviewSignal(s, task.ID) {
			t.Fatalf("signals = %v; want one pr_merged and no review", pendingKinds(s, task.ID))
		}
	})

	t.Run("user_check task is excluded", func(t *testing.T) {
		s, _, task := newPRConflictTask(t, &memStore{})
		ctx, now := context.Background(), time.Now()
		if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
			v.State = reviewUserCheck
			return true
		}); err != nil {
			t.Fatal(err)
		}
		forge := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
		s.deploy.forge = forge
		s.sweepPRStatuses(ctx, now)
		forge.status.State = "merged"
		s.sweepPRStatuses(ctx, now.Add(deployRecheck))
		if _, ok := pendingPRMerged(s, task.ID, task.PRURL); ok {
			t.Fatal("user_check task unexpectedly queued sigPRMerged")
		}
	})
}

func TestPRMergedWakeIdempotentAcrossSweepsAndRestart(t *testing.T) {
	st := &memStore{}
	s1, f1, task := newPRConflictTask(t, st)
	ctx := context.Background()
	now := time.Now()
	const prURL = "https://github.com/example/app/pull/55"
	s1.setTaskStage(ctx, task.ID, "review", prURL)

	forge := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h55"}}
	s1.deploy.forge = forge
	s1.sweepPRStatuses(ctx, now)
	forge.status.State = "merged"
	s1.sweepPRStatuses(ctx, now.Add(deployRecheck))
	sendPendingPRConflicts(s1)
	if len(f1.notices()) != 1 || !strings.Contains(f1.notices()[0], "signal="+sigPRMerged) {
		t.Fatalf("initial merge wake failed: %q", f1.notices())
	}

	// Repeated sweep on the same service must not re-emit.
	s1.sweepPRStatuses(ctx, now.Add(2*deployRecheck))
	sendPendingPRConflicts(s1)
	if len(f1.notices()) != 1 {
		t.Fatalf("repeated sweep emitted duplicate wake: %q", f1.notices())
	}

	// Restart service sharing the same persisted store and reloaded open task -> still 0 new wakes.
	s2, f2 := newTodoReadyService(t, st)
	s2.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	reloaded := todoNow(s1, task.ID)
	s2.todos.mu.Lock()
	s2.todos.loaded = true
	s2.todos.todos = map[string]*Todo{task.ID: &reloaded}
	s2.todos.mu.Unlock()
	s2.deploy.forge = &statusForge{status: TodoPRStatus{State: "merged", HeadOID: "h55"}}
	s2.sweepPRStatuses(ctx, now.Add(3*deployRecheck))
	sendPendingPRConflicts(s2)
	if len(f2.notices()) != 0 {
		t.Fatalf("restarted service re-emitted duplicate pr_merged wake: %q", f2.notices())
	}

	// Closing the task allows prunePRMergeWakes to clean up the stored marker.
	if _, err := s2.editTodo(ctx, task.ID, dbTodoDoneEdit(true)); err != nil {
		t.Fatal(err)
	}
	s2.prunePRMergeWakes(ctx)
	var markers map[string]prMergeWakeMarker
	if _, err := st.Setting(ctx, prMergeWakesKey, &markers); err != nil {
		t.Fatal(err)
	}
	if _, ok := markers[prMergeWakeMarkerKey(task.ID, prURL)]; ok {
		t.Fatalf("closed task marker was not pruned: %+v", markers)
	}
}
