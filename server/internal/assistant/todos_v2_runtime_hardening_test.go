package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/mergetrain"
	"github.com/nwverse/persona/server/internal/db"
)

func runMergeTrainRestartPersistenceScenario(t *testing.T, newBoot1, newRestarted func() *Service, st todoBlockerStore, requireTodoReload bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	s1 := newBoot1()
	setMergeTrainTodosV2Flag(t, s1, true, false)
	const repo = "github.com/example/hardening-repo"
	const prURL = "https://github.com/example/hardening-repo/pull/101"
	task := newMergeTrainTask(t, s1, "Persistent merge train PR", prURL)

	// Legacy rows were keyed by repository and counted conflicting heads as attempts.
	const legacyID = "mt_legacy"
	legacy := db.TodoBlockerRow{
		ID: legacyID, TodoID: task.ID, Kind: todoBlockerKindMergeTrain, TargetRef: repo, Attempts: 2,
		ConditionJSON: `{"repo_key":"github.com/example/hardening-repo","pr_url":"https://GitHub.com/Example/Hardening-Repo/pull/101/","last_head_oid":"old-head","max_attempts":2}`,
	}
	if _, err := st.UpsertTodoBlocker(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	recSum, err := s1.ReconcileTodoBlockers(ctx, "")
	if err != nil || slices.Contains(recSum.ResolvedIDs, legacyID) || slices.Contains(recSum.EscalatedIDs, legacyID) {
		t.Fatalf("legacy conflict evidence was cleared or escalated: %+v, %v", recSum, err)
	}

	s1.stopSuperviseTimers()
	s2 := newRestarted()
	if !s2.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2=true to persist across Service restart")
	}
	if err := s2.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	reloadedTask := todoNow(s2, task.ID)
	if reloadedTask.ID == "" {
		if requireTodoReload {
			t.Fatalf("restarted Service did not reload task %s", task.ID)
		}
		reloadedTask = task
	}

	out := s2.onPRStatusMergeTrain(ctx, reloadedTask, TodoPRStatus{
		URL: prURL, State: "open", HeadOID: "new-head", Conflicts: true, CheckedAt: now.Add(time.Second),
	})
	repoSnap, _ := findRepoSnapshot(s2.mergeTrainSnapshot(ctx), repo)
	if out.Decision.Action != mergetrain.ActionWaitQueue || repoSnap.Head == nil || repoSnap.Head.RebaseFailures != 0 {
		t.Fatalf("legacy attempts became rebase failures after restart: head=%+v outcome=%+v", repoSnap.Head, out)
	}
	rows, err := st.ListTodoBlockersByTodo(ctx, task.ID)
	if err != nil || len(rows) != 1 || rows[0].ID != legacyID || rows[0].State != db.TodoBlockerStateActive || rows[0].Attempts != 2 {
		t.Fatalf("legacy blocker was overwritten or duplicated: %+v, %v", rows, err)
	}
	var cond mergeTrainBlockerCondition
	if err := json.Unmarshal([]byte(rows[0].ConditionJSON), &cond); err != nil || cond.LastHeadOID != "new-head" {
		t.Fatalf("legacy blocker did not record its current observation: %+v, %v", cond, err)
	}

	// Clearing conflicts resolves the persistent merge_train blocker row.
	_ = s2.onPRStatusMergeTrain(ctx, reloadedTask, TodoPRStatus{
		URL:       prURL,
		State:     "open",
		HeadOID:   "head-3",
		Review:    "approved",
		Checks:    "success",
		Conflicts: false,
		CheckedAt: now.Add(4 * time.Second),
	})
	rows, err = st.ListTodoBlockersByTodo(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTodoBlockersByTodo after conflict clear: %v", err)
	}
	if len(rows) != 1 || rows[0].State != db.TodoBlockerStateResolved || rows[0].ResolvedAt == nil {
		t.Fatalf("expected resolved blocker after conflicts cleared, got %+v", rows)
	}

	// Verify PR merge after a restart also resolves an active merge_train blocker row.
	prURL2 := "https://github.com/example/hardening-repo/pull/102"
	task2 := newMergeTrainTask(t, s2, "Merged after restart PR", prURL2)
	_ = s2.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL:       prURL2,
		State:     "open",
		HeadOID:   "m-head-1",
		Review:    "approved",
		Checks:    "failure",
		Conflicts: true,
		CheckedAt: now.Add(5 * time.Second),
	})

	newRows, err := st.ListTodoBlockersByTodo(ctx, task2.ID)
	if err != nil || len(newRows) != 1 || newRows[0].ID != mergeTrainBlockerID(prURL2) || newRows[0].TargetRef != prURL2 || newRows[0].Attempts != 0 {
		t.Fatalf("new conflict row is not keyed by PR or counted as an attempt: %+v, %v", newRows, err)
	}

	s2.stopSuperviseTimers()
	s3 := newRestarted()
	if err := s3.ensureTodosLoaded(ctx); err != nil {
		t.Fatalf("s3.ensureTodosLoaded: %v", err)
	}
	reloadedTask2 := todoNow(s3, task2.ID)
	if reloadedTask2.ID == "" {
		if requireTodoReload {
			t.Fatalf("restarted Service did not reload task %s", task2.ID)
		}
		reloadedTask2 = task2
	}
	_ = s3.onPRStatusMergeTrain(ctx, reloadedTask2, TodoPRStatus{
		URL:       prURL2,
		State:     "merged",
		HeadOID:   "m-head-1",
		CheckedAt: now.Add(6 * time.Second),
	})
	rows2, err := st.ListTodoBlockersByTodo(ctx, task2.ID)
	if err != nil {
		t.Fatalf("ListTodoBlockersByTodo after merge: %v", err)
	}
	if len(rows2) != 1 || rows2[0].State != db.TodoBlockerStateResolved || rows2[0].ResolvedAt == nil {
		t.Fatalf("expected resolved blocker after PR merged across restart, got %+v", rows2)
	}
}

func newRestartedMergeTrainService(t *testing.T, st Store) *Service {
	t.Helper()
	f := newFakeHub()
	f.inbox = newFakeInbox()
	f.sessions["box"] = nil
	f.hello.Assistant.MachineID = "server"
	f.known["conv1"] = true
	// Leave f.settings == nil so the restarted Service loads persisted Settings (including todos_v2=true) from st.
	s := newServiceOn(t, f, st)
	t.Cleanup(s.stopQuietTimers)
	s.stopSuperviseTimers()
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	return s
}

func TestTodosV2RuntimeHardeningMergeTrainRestartPersistence(t *testing.T) {
	store := newMemBlockerStore()
	newBoot1 := func() *Service {
		s, _ := newQuietServiceOn(t, store)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		return s
	}
	newRestarted := func() *Service {
		return newRestartedMergeTrainService(t, store)
	}
	runMergeTrainRestartPersistenceScenario(t, newBoot1, newRestarted, store, false)
}

func TestTodosV2RuntimeHardeningMergeTrainRestartPersistencePostgres(t *testing.T) {
	dsn := newMigratedDatabase(t, db.ManagerSet)
	ctx := t.Context()

	database, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	newBoot1 := func() *Service {
		s, _ := newQuietServiceOn(t, database)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		return s
	}
	newRestarted := func() *Service {
		return newRestartedMergeTrainService(t, database)
	}
	runMergeTrainRestartPersistenceScenario(t, newBoot1, newRestarted, database, true)
}

func TestTodosV2RuntimeHardeningBypassWhenFlagOff(t *testing.T) {
	ctx := context.Background()
	store := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, store)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	if s.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2 default false")
	}

	prURL := "https://github.com/example/off-repo/pull/55"
	task := newMergeTrainTask(t, s, "Off flag task", prURL)
	_ = s.linkTodoAgent(ctx, task.ID, "box", "sess-off", "Off flag task", "/code/app", "claude")
	task = todoNow(s, task.ID)

	// Merge-train conflicts under todos_v2=false must write zero rows to todo_blockers.
	now := time.Now().UTC()
	for i, head := range []string{"off-1", "off-2"} {
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{
			URL:       prURL,
			State:     "open",
			HeadOID:   head,
			Review:    "approved",
			Checks:    "failure",
			Conflicts: true,
			CheckedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListTodoBlockersByTodo(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("todos_v2=false wrote todo_blockers rows: %+v", rows)
	}
}
