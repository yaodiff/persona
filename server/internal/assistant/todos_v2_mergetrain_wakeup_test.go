package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/mergetrain"
)

func getMergeTrainHTTP(t *testing.T, s *Service, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/todos/mergetrain", nil)
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func pendingMergeTrainTurn(s *Service, todoID string) (superviseSignal, bool) {
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	for _, g := range s.supervise.pending {
		if g.Todo == todoID && g.Kind == sigMergeTrainTurn {
			return g, true
		}
	}
	return superviseSignal{}, false
}

func TestTodosV2MergeTrainFollowerWakeupOnHeadMergeAndDedup(t *testing.T) {
	s, _ := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	now := time.Now().UTC()

	task1 := newMergeTrainTask(t, s, "Head PR 1", "https://github.com/example/wakeup-repo/pull/101")
	task2 := newMergeTrainTask(t, s, "Follower PR 2", "https://github.com/example/wakeup-repo/pull/102")
	task3 := newMergeTrainTask(t, s, "Follower PR 3", "https://github.com/example/wakeup-repo/pull/103")
	task4 := newMergeTrainTask(t, s, "Follower PR 4", "https://github.com/example/wakeup-repo/pull/104")

	for i, tc := range []struct {
		todo Todo
		sha  string
	}{
		{task1, "sha-101"},
		{task2, "sha-102"},
		{task3, "sha-103"},
		{task4, "sha-104"},
	} {
		if err := s.applyPRStatus(ctx, tc.todo, TodoPRStatus{
			URL:       tc.todo.PRURL,
			State:     "open",
			HeadOID:   tc.sha,
			Review:    "approved",
			Checks:    "pending",
			CheckedAt: now.Add(time.Duration(i+1) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Closing a non-head queued PR (task4) must NOT emit a spurious promotion wakeup for head task1.
	if err := s.applyPRStatus(ctx, task4, TodoPRStatus{
		URL:       task4.PRURL,
		State:     "closed",
		HeadOID:   "sha-104",
		CheckedAt: now.Add(5 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if g, ok := pendingMergeTrainTurn(s, task1.ID); ok {
		t.Fatalf("closing non-head queued PR unexpectedly emitted mergetrain_turn for head: %+v", g)
	}

	// Verify queue snapshot before head merges: task1 is position 1, task2 is position 2 behind task1.
	w := getMergeTrainHTTP(t, s, "full")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos/mergetrain: code=%d body=%s", w.Code, w.Body.String())
	}
	var before mergeTrainSnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(before.Repos) != 1 || before.Repos[0].Head == nil || before.Repos[0].Head.TodoID != task1.ID || before.Repos[0].Head.Position != 1 {
		t.Fatalf("unexpected repos before merge: %+v", before.Repos)
	}
	if len(before.Repos[0].Queue) != 2 || before.Repos[0].Queue[0].TodoID != task2.ID || before.Repos[0].Queue[0].Position != 2 {
		t.Fatalf("unexpected queue before merge: %+v", before.Repos[0].Queue)
	}
	if badge := before.ByTodo[task2.ID]; badge.Position != 2 || badge.AheadPRURL != task1.PRURL || badge.RepoKey != "github.com/example/wakeup-repo" {
		t.Fatalf("by_todo[task2] before merge = %+v", badge)
	}

	// Merging head task1 promotes task2 to head (position 1) and emits exactly one deduplicated mergetrain_turn.
	if err := s.applyPRStatus(ctx, task1, TodoPRStatus{
		URL:       task1.PRURL,
		State:     "merged",
		HeadOID:   "sha-101",
		CheckedAt: now.Add(6 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	wantKey := task1.PRURL + "@merged@" + now.Add(6*time.Second).Format(time.RFC3339Nano)
	sig, ok := pendingMergeTrainTurn(s, task2.ID)
	if !ok {
		t.Fatalf("expected mergetrain_turn wakeup for promoted follower %s", task2.ID)
	}
	if sig.Key != wantKey {
		t.Fatalf("wakeup Key = %q, want %q", sig.Key, wantKey)
	}
	if sig.To != "conv1" {
		t.Fatalf("wakeup To = %q, want conv1", sig.To)
	}
	if sig.MergeTrainTurn == nil || sig.MergeTrainTurn.PreviousPRURL != task1.PRURL ||
		sig.MergeTrainTurn.RepoKey != "github.com/example/wakeup-repo" || sig.PRURL != task2.PRURL {
		t.Fatalf("wakeup missing promotion context: %+v", sig)
	}

	// Repeated polls after task1 merged must NOT re-emit duplicate wakeup signals.
	s.supervise.mu.Lock()
	delete(s.supervise.pending, sig.id())
	s.supervise.mu.Unlock()
	for i := range 3 {
		out := s.onPRStatusMergeTrain(ctx, task1, TodoPRStatus{
			URL:       task1.PRURL,
			State:     "merged",
			HeadOID:   "sha-101",
			CheckedAt: now.Add(time.Duration(7+i) * time.Second),
		})
		if out.turnWakeup.Todo != "" {
			t.Fatalf("repeat poll %d unexpectedly re-emitted turn wakeup: %+v", i, out)
		}
	}
	if g, dup := pendingMergeTrainTurn(s, task2.ID); dup {
		t.Fatalf("repeat polls re-queued duplicate mergetrain_turn: %+v", g)
	}

	// Closing head task2 promotes task3 and emits a deduplicated wakeup mentioning task2's PR URL.
	if err := s.applyPRStatus(ctx, task2, TodoPRStatus{
		URL:       task2.PRURL,
		State:     "closed",
		HeadOID:   "sha-102",
		CheckedAt: now.Add(11 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	sig3, ok3 := pendingMergeTrainTurn(s, task3.ID)
	if !ok3 {
		t.Fatalf("expected mergetrain_turn wakeup for promoted follower %s after head closed", task3.ID)
	}
	wantKey3 := task2.PRURL + "@closed@" + now.Add(11*time.Second).Format(time.RFC3339Nano)
	if sig3.Key != wantKey3 || sig3.MergeTrainTurn == nil || sig3.MergeTrainTurn.PreviousState != "closed" {
		t.Fatalf("unexpected wakeup for task3 after head closed: %+v", sig3)
	}
}

func TestTodosV2MergeTrainHumanReviewFollowerNotifiedWithoutAutoMerge(t *testing.T) {
	s, f := newQuietServiceOn(t, nil)
	f.setHello(func(h *Hello) { h.Locale = "en" })
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setMergeTrainTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	now := time.Now().UTC()

	task1 := newMergeTrainTask(t, s, "Approved head PR", "https://github.com/example/review-wakeup/pull/1")
	task2 := newMergeTrainTask(t, s, "Human review follower PR", "https://github.com/example/review-wakeup/pull/2")

	if err := s.applyPRStatus(ctx, task1, TodoPRStatus{
		URL: task1.PRURL, State: "open", HeadOID: "h1", Review: "approved", Checks: "success", CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyPRStatus(ctx, task2, TodoPRStatus{
		URL: task2.PRURL, State: "open", HeadOID: "h2", Review: "required", Checks: "success", CheckedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	// Head merges -> task2 is promoted and woken, with explicit human-review note and zero auto-merge.
	if err := s.applyPRStatus(ctx, task1, TodoPRStatus{
		URL: task1.PRURL, State: "merged", HeadOID: "h1", CheckedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	sig, ok := pendingMergeTrainTurn(s, task2.ID)
	if !ok {
		t.Fatalf("expected mergetrain_turn for PolicyHumanReviewRequired follower %s", task2.ID)
	}
	entry, ok := s.digestEntry(ctx, sig, "")
	if !ok || !strings.Contains(entry.Detail, task1.PRURL) || !strings.Contains(entry.Detail, "Human approval is still required") {
		t.Fatalf("expected human approval notice in wakeup detail, got %q", entry.Detail)
	}

	// Evaluating promoted task2 with green checks but unapproved review must NEVER auto-merge.
	out := s.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL: task2.PRURL, State: "open", HeadOID: "h2", Review: "required", Checks: "success", CheckedAt: now.Add(3 * time.Second),
	})
	if out.Decision.Action == mergetrain.ActionRebaseAndMerge {
		t.Fatalf("PolicyHumanReviewRequired follower unexpectedly auto-merged: %+v", out.Decision)
	}
	if out.Decision.Action != mergetrain.ActionAwaitHumanReview || out.Decision.State != mergetrain.StateAwaitingApproval {
		t.Fatalf("promoted follower decision = %+v, want ActionAwaitHumanReview / StateAwaitingApproval", out.Decision)
	}

	repo, ok := findRepoSnapshot(s.mergeTrainSnapshot(ctx), "github.com/example/review-wakeup")
	if !ok || repo.Head == nil || repo.Head.TodoID != task2.ID || repo.Head.Approved || repo.Head.Policy != mergetrain.PolicyHumanReviewRequired {
		t.Fatalf("promoted follower head unexpectedly mutated: %+v", repo.Head)
	}
}

func TestTodosV2MergeTrainEndpointSnapshotGateAndScope(t *testing.T) {
	e := newTodoEnv(t, nil)
	s := e.s
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	e.sessions("server", scopeListed()...)

	// todos_v2=false must reject GET /api/v1/todos/mergetrain with 409 Conflict.
	wOff := getMergeTrainHTTP(t, s, "full")
	if wOff.Code != http.StatusConflict {
		t.Fatalf("todos_v2=false GET /api/v1/todos/mergetrain: code=%d want=409 body=%s", wOff.Code, wOff.Body.String())
	}

	// Enable todos_v2=true.
	setMergeTrainTodosV2Flag(t, s, true, false)
	now := time.Now().UTC()

	// Hidden todo originates from health conversation "h0"; visible todo originates from "conv1".
	hiddenTodo, err := s.createTodo(ctx, "Private health PR", "Hidden from extensions.", "h0", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, hiddenTodo.ID, "h0", []string{"Merged."}, 0)
	s.setTaskStage(ctx, hiddenTodo.ID, "review", "https://github.com/example/scoped-repo/pull/10")
	hiddenTodo = todoNow(s, hiddenTodo.ID)

	visibleTodo, err := s.createTodo(ctx, "Public work PR", "Visible to extensions.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, visibleTodo.ID, "conv1", []string{"Merged."}, 0)
	s.setTaskStage(ctx, visibleTodo.ID, "review", "https://github.com/example/scoped-repo/pull/11")
	visibleTodo = todoNow(s, visibleTodo.ID)

	if err := s.applyPRStatus(ctx, hiddenTodo, TodoPRStatus{
		URL: hiddenTodo.PRURL, State: "open", HeadOID: "priv-1", Review: "approved", Checks: "pending", CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyPRStatus(ctx, visibleTodo, TodoPRStatus{
		URL: visibleTodo.PRURL, State: "open", HeadOID: "pub-1", Review: "required", Checks: "success", CheckedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	// Full role sees both head and queued follower plus ahead_pr_url.
	wFull := getMergeTrainHTTP(t, s, "full")
	if wFull.Code != http.StatusOK {
		t.Fatalf("full role GET /api/v1/todos/mergetrain: code=%d body=%s", wFull.Code, wFull.Body.String())
	}
	var fullResp mergeTrainSnapshotResponse
	if err := json.Unmarshal(wFull.Body.Bytes(), &fullResp); err != nil {
		t.Fatalf("unmarshal full response: %v", err)
	}
	if len(fullResp.Repos) != 1 || fullResp.Repos[0].Head == nil || fullResp.Repos[0].Head.TodoID != hiddenTodo.ID {
		t.Fatalf("full role repos = %+v", fullResp.Repos)
	}
	if len(fullResp.Repos[0].Queue) != 1 || fullResp.Repos[0].Queue[0].TodoID != visibleTodo.ID || fullResp.Repos[0].Queue[0].Position != 2 {
		t.Fatalf("full role queue = %+v", fullResp.Repos[0].Queue)
	}
	if badge := fullResp.ByTodo[visibleTodo.ID]; badge.Position != 2 || badge.AheadPRURL != hiddenTodo.PRURL || badge.State != string(mergetrain.StateQueued) {
		t.Fatalf("full role by_todo[visible] = %+v", badge)
	}

	// Extension role must NOT see the hidden todo or its PR URL anywhere in repos or by_todo.
	wExt := getMergeTrainHTTP(t, s, roleExtension)
	if wExt.Code != http.StatusOK {
		t.Fatalf("extension role GET /api/v1/todos/mergetrain: code=%d body=%s", wExt.Code, wExt.Body.String())
	}
	noneOf(t, "extension GET /api/v1/todos/mergetrain", wExt.Body.Bytes(), hiddenTodo.ID, hiddenTodo.PRURL)
	var extResp mergeTrainSnapshotResponse
	if err := json.Unmarshal(wExt.Body.Bytes(), &extResp); err != nil {
		t.Fatalf("unmarshal extension response: %v", err)
	}
	if len(extResp.Repos) != 1 || extResp.Repos[0].Head != nil {
		t.Fatalf("extension role should not see hidden head: %+v", extResp.Repos)
	}
	if len(extResp.Repos[0].Queue) != 1 || extResp.Repos[0].Queue[0].TodoID != visibleTodo.ID || extResp.Repos[0].Queue[0].Position != 2 {
		t.Fatalf("extension role queue = %+v", extResp.Repos[0].Queue)
	}
	if _, leaked := extResp.ByTodo[hiddenTodo.ID]; leaked {
		t.Fatalf("extension role by_todo leaked hidden todo %s", hiddenTodo.ID)
	}
	if badge := extResp.ByTodo[visibleTodo.ID]; badge.Position != 2 || badge.AheadPRURL != "" {
		t.Fatalf("extension role by_todo[visible] leaked ahead_pr_url or wrong position: %+v", badge)
	}
}
