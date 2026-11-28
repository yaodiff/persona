package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Drive the real digest synchronously so facts can change after emission and before delivery.
func sendV2WakeupDigest(t *testing.T, s *Service) {
	t.Helper()
	setSuperviseTimingsForTest(s, shortSuperviseTimings)
	s.sendDigest(t.Context())
	setSuperviseTimingsForTest(s, heldSuperviseTimings())
}

func wakeupDigests(f *fakeHub, to, todoID, kind string) []string {
	var result []string
	for _, msg := range sendsToConversation(f, to) {
		if strings.Contains(msg, todoID+"，signal="+kind) {
			result = append(result, msg)
		}
	}
	return result
}

func TestTodosV2WakeupUnblockedDigest(t *testing.T) {
	run := func(t *testing.T, store Store) {
		s, f := newQuietServiceTimed(t, store, heldSuperviseTimings())
		setBlockersTodosV2Flag(t, s, true, false)
		setSettings(t, s, func(v *Settings) { v.AutoSupervisionMode = supervisionModeOutcomes })
		waiter := createRequestAndPiece(t, s, "conv1", "Verify downstream changes", "", "", nil)
		upstreams := map[string]Todo{}
		for i, url := range []string{
			"https://github.com/example/app/pull/461",
			"https://github.com/example/app/pull/462",
			"https://github.com/example/app/pull/462", // the same edge can be added again in a later wait
		} {
			if _, err := s.AddTodoBlocker(t.Context(), waiter.ID, todoBlockerKindExternalPR, url, ""); err != nil {
				t.Fatal(err)
			}
			upstream, ok := upstreams[url]
			if !ok {
				upstream = newMergeTrainTask(t, s, "Upstream change", url)
				upstreams[url] = upstream
			}
			if err := s.applyPRStatus(t.Context(), upstream, TodoPRStatus{URL: url, State: "merged", CheckedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			sendV2WakeupDigest(t, s)
			got := wakeupDigests(f, "conv1", waiter.ID, "blockers_unblocked")
			if len(got) != i+1 || !strings.Contains(got[i], url) {
				t.Fatalf("unblock %d for to-do without After: want one new digest mentioning %s, got %q", i+1, url, got)
			}
		}
	}

	t.Run("memory", func(t *testing.T) { run(t, newMemBlockerStore()) })
	t.Run("postgres", func(t *testing.T) {
		st, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(st.Close)
		run(t, st)
	})
}

func TestTodosV2WakeupAcceptancePreservesAfterDigest(t *testing.T) {
	store := newMemBlockerStore()
	s, f := newQuietServiceTimed(t, store, heldSuperviseTimings())
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := t.Context()
	root := createRequestAndPiece(t, s, "conv1", "Release", "", "", nil)
	pre := createRequestAndPiece(t, s, "conv1", "Verify build", "", root.ID, nil)
	dep := createRequestAndPiece(t, s, "conv1", "Publish build", "Publish the verified build", root.ID, []string{pre.ID})
	if _, err := s.AddTodoBlocker(ctx, dep.ID, todoBlockerKindTodoDone, pre.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, pre.ID, "conv1", nil, 0); err != nil {
		t.Fatal(err)
	}
	checkedAtUnblock := false
	s.setTodoBlockerUnblockedHook(func(_ context.Context, id string) {
		if id != dep.ID {
			return
		}
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		after := s.supervise.pending[dep.ID+"|"+sigTodoReady]
		unblocked := s.supervise.pending[dep.ID+"|"+sigBlockersUnblocked]
		checkedAtUnblock = strings.HasPrefix(after.Key, "after:") && unblocked.Key != ""
	})
	if closed, err := s.reviewDoneTodo(ctx, pre.ID, "accepted", true); err != nil || !closed {
		t.Fatalf("accept prerequisite: closed=%v, err=%v", closed, err)
	}
	if !checkedAtUnblock {
		t.Fatal("the blocker wake must preserve the after wake already pending during acceptance")
	}
	sendV2WakeupDigest(t, s)
	ready := wakeupDigests(f, "conv1", dep.ID, "todo_ready")
	unblocked := wakeupDigests(f, "conv1", dep.ID, "blockers_unblocked")
	if len(ready) != 1 || len(unblocked) != 1 || ready[0] != unblocked[0] || !strings.Contains(ready[0], "Publish the verified build") {
		t.Fatalf("acceptance must deliver both wakes for %s in the same digest, got %q", dep.ID, sendsToConversation(f, "conv1"))
	}
}

func TestTodosV2WakeupMergeTrainDigestWithActiveAgent(t *testing.T) {
	s, f := newQuietServiceTimed(t, newMemBlockerStore(), heldSuperviseTimings())
	setMergeTrainTodosV2Flag(t, s, true, false)
	ctx := t.Context()
	head := newMergeTrainTask(t, s, "Head of merge train", "https://github.com/example/app/pull/471")
	next := newMergeTrainTask(t, s, "Next PR on merge train", "https://github.com/example/app/pull/472")
	if err := s.linkTodoAgent(ctx, next.ID, "box", "worker-472", "Next PR", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	if !todoHasActiveAgent(todoNow(s, next.ID)) {
		t.Fatal("the promoted to-do must already have an agent")
	}
	for _, task := range []Todo{head, next} {
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{URL: task.PRURL, State: "open", Review: "required", CheckedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.applyPRStatus(ctx, head, TodoPRStatus{URL: head.PRURL, State: "closed", CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	sendV2WakeupDigest(t, s)
	got := wakeupDigests(f, "conv1", next.ID, "mergetrain_turn")
	if len(got) != 1 || !strings.Contains(got[0], head.PRURL) || !strings.Contains(got[0], next.PRURL) {
		t.Fatalf("promoted to-do with an active agent must receive a merge-train digest, got %q", sendsToConversation(f, "conv1"))
	}

	// The following task takes a turn and then leaves the train before the digest can go out.
	follower := newMergeTrainTask(t, s, "PR leaving the train", "https://github.com/example/app/pull/473")
	if err := s.applyPRStatus(ctx, follower, TodoPRStatus{URL: follower.PRURL, State: "open", CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []Todo{next, follower} {
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{URL: task.PRURL, State: "closed", CheckedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	sendV2WakeupDigest(t, s)
	if got := wakeupDigests(f, "conv1", follower.ID, "mergetrain_turn"); len(got) != 0 {
		t.Fatalf("a former merge-train head no longer has a turn, got %q", got)
	}
}

func TestTodosV2WakeupWriteConflictDigest(t *testing.T) {
	s, f := transferEnv(t)
	setSuperviseTimingsForTest(s, heldSuperviseTimings())
	setTodosV2ForTest(t, s, true, false)
	peer := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("First explicit write scope", map[string]any{
		"write_prefixes": []string{"src/shared"},
	}))
	out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Overlapping explicit write scope", map[string]any{
		"write_prefixes": []string{"src/shared/button"},
		"owner_step":     "irreversible",
	}))
	if bad || !strings.Contains(out, "needs the user's Start") {
		t.Fatalf("expected a plan waiting for the owner: %s", out)
	}
	plans, err := s.conversationPlans(t.Context(), "lead0001")
	if err != nil {
		t.Fatal(err)
	}
	var pending *TaskPlan
	for _, p := range plans {
		if p.State == planOpen {
			pending = p
			break
		}
	}
	if pending == nil {
		t.Fatal("missing the plan that needs owner approval")
	}
	started, err := s.startPlan(ownerStart(t.Context()), pending.ID, nil, false)
	if err != nil || started.Tasks[0].Error != "" || started.Tasks[0].TodoID == "" {
		t.Fatalf("owner Start did not dispatch the plan: plan=%+v, err=%v", started, err)
	}
	waitFor(t, "the owner-approved dispatch", func() bool { return s.taskStarting.Load() == 0 })
	taskID := started.Tasks[0].TodoID
	sendV2WakeupDigest(t, s)
	got := wakeupDigests(f, "lead0001", taskID, "write_conflict")
	if len(got) != 1 || !strings.Contains(got[0], peer.ID) || !strings.Contains(got[0], "src/shared/button") {
		t.Fatalf("the owning supervisor must receive the declared overlap, got %q", sendsToConversation(f, "lead0001"))
	}

	later := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Write scope that becomes clear", map[string]any{
		"write_prefixes": []string{"src/shared/flags"},
	}))
	if closed, err := s.reviewDoneTodo(t.Context(), peer.ID, "accepted", true); err != nil || !closed {
		t.Fatalf("close conflict peer before digest: closed=%v, err=%v", closed, err)
	}
	sendV2WakeupDigest(t, s)
	if got := wakeupDigests(f, "lead0001", later.ID, "write_conflict"); len(got) != 0 {
		t.Fatalf("a closed peer cannot conflict, got %q", got)
	}
}

func TestTodosV2WakeupQueuedUnblockRechecksCurrentBlockers(t *testing.T) {
	s, f := newQuietServiceTimed(t, newMemBlockerStore(), heldSuperviseTimings())
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := t.Context()
	idle := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusIdle}
	busy := idle
	busy.Status = proto.StatusWorking
	f.setSessions("server", busy)
	defer stopRelayTimer(s, "conv1")

	waiter := createRequestAndPiece(t, s, "conv1", "Wait for a delivery PR", "", "", nil)
	url := "https://github.com/example/app/pull/481"
	if _, err := s.AddTodoBlocker(ctx, waiter.ID, todoBlockerKindExternalPR, url, ""); err != nil {
		t.Fatal(err)
	}
	upstream := newMergeTrainTask(t, s, "Upstream change", url)
	if err := s.applyPRStatus(ctx, upstream, TodoPRStatus{URL: url, State: "merged", CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	sendV2WakeupDigest(t, s)
	if len(s.queuedDigestSignals("conv1")) == 0 || len(sendsToConversation(f, "conv1")) != 0 {
		t.Fatal("the busy supervisor must have a queued digest awaiting delivery")
	}
	if _, err := s.AddTodoBlocker(ctx, waiter.ID, todoBlockerKindExternalPR, "https://github.com/example/app/pull/482", ""); err != nil {
		t.Fatal(err)
	}
	f.setSessions("server", idle)
	drainRelayTest(t, s, idle)
	if got := wakeupDigests(f, "conv1", waiter.ID, "blockers_unblocked"); len(got) != 0 || s.queuedFor("conv1") != 0 {
		t.Fatalf("a new active blocker must retract the waiting digest entry, got %q", got)
	}
}
