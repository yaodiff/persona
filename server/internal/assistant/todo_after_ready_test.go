package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// todoReadyRepoStore augments memStore with in-memory todoRepositoryStore bindings for offline delivery-role tests.
type todoReadyRepoStore struct {
	*memStore
	mu    sync.Mutex
	repos map[string]repository.TaskSet
	err   error
}

func (m *todoReadyRepoStore) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *todoReadyRepoStore) setRepo(id string, set repository.TaskSet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repos == nil {
		m.repos = map[string]repository.TaskSet{}
	}
	m.repos[id] = set
}

func (m *todoReadyRepoStore) TodoRepositories(_ context.Context, id string) (repository.TaskSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return repository.TaskSet{}, m.err
	}
	if set, ok := m.repos[id]; ok {
		return set, nil
	}
	return repository.TaskSet{TodoID: id}, nil
}

func (m *todoReadyRepoStore) CreateTodoWithRepositories(context.Context, db.TodoRow, repository.CreateBindings) error {
	return errors.New("not implemented")
}

func (m *todoReadyRepoStore) SetTodoRepositories(context.Context, string, repository.SetInput) (repository.TaskSet, error) {
	return repository.TaskSet{}, errors.New("not implemented")
}

func (m *todoReadyRepoStore) SetTodoRepositoryLinks(context.Context, string, string, repository.LinksInput) (repository.TaskSet, error) {
	return repository.TaskSet{}, errors.New("not implemented")
}

func newTodoReadyService(t *testing.T, st Store) (*Service, *fakeHub) {
	t.Helper()
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	s, f := newQuietServiceOn(t, st)
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{PublicURL: "https://persona.example"}, nil
	})
	return s, f
}

func sendsToConversation(f *fakeHub, sessionID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.sends {
		if a["session_id"] == sessionID {
			text, _ := a["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

func createRequestAndPiece(t *testing.T, s *Service, origin, title, notes, partOf string, after []string) Todo {
	t.Helper()
	ctx := context.Background()
	var plan db.TodoEdit
	if partOf != "" {
		plan.PartOf = &partOf
	}
	if len(after) > 0 {
		a := append([]string(nil), after...)
		plan.After = &a
	}
	var (
		todo Todo
		err  error
	)
	if origin != "" {
		todo, err = s.createPieceTodo(ctx, origin, title, notes, plan)
	} else {
		todo, err = s.createPlainTodo(ctx, title, notes, plan)
	}
	if err != nil {
		t.Fatalf("create todo %q: %v", title, err)
	}
	return todo
}

func TestTodoAfterReadySinglePrereqTodoCompleteRoutesToMainNotBackground(t *testing.T) {
	st := &memStore{}
	s, f := newTodoReadyService(t, st)
	ctx := context.Background()

	// Enable background supervisor to verify todo_ready wakes Main chat directly rather than BackgroundSupervisor.
	s.noBackground = false
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor = "bg1"
		v.BackgroundSupervisorMachineID = "server"
		now := time.Now().UTC()
		v.BackgroundSupervisorAt = &now
	})
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	f.setHello(func(h *Hello) { h.Assistant.CompleteAgentlessTodos = true })

	root := createRequestAndPiece(t, s, "conv1", "Batch rollout", "", "", nil)
	pre := createRequestAndPiece(t, s, "conv1", "Batch 1 schema", "", root.ID, nil)
	dep := createRequestAndPiece(t, s, "conv1", "Batch 2 migration", "Launch on box:/code/app with readonly=false", root.ID, []string{pre.ID})

	raw, _ := json.Marshal(map[string]any{"id": pre.ID})
	if _, err := s.toolTodo(ctx, "server", "conv1", "todo_complete", raw); err != nil {
		t.Fatalf("todo_complete: %v", err)
	}

	waitFor(t, "todo_ready wake to main", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
	msg := sendsToConversation(f, "conv1")[0]
	for _, want := range []string{
		"signal=todo_ready",
		dep.ID,
		dep.Title,
		"https://persona.example/todos/" + dep.ID,
		pre.ID,
		pre.Title,
		"Launch on box:/code/app with readonly=false",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("wake message missing %q:\n%s", want, msg)
		}
	}
	if got := len(sendsToConversation(f, "bg1")); got != 0 {
		t.Fatalf("background supervisor got %d wakes, want 0", got)
	}
	// Safety: dependent to-do must remain open with no auto-started Agent.
	afterDep := todoNow(s, dep.ID)
	if afterDep.DoneAt != nil || todoHasActiveAgent(afterDep) {
		t.Fatalf("dependent to-do was silently closed or started: %+v", afterDep)
	}
	if len(f.starts) != 0 {
		t.Fatalf("expected 0 auto-started agents, got %d", len(f.starts))
	}
}

func TestTodoAfterReadyMultiPrereqPartialAndTaskReviewAccepted(t *testing.T) {
	st := &memStore{}
	s, f := newTodoReadyService(t, st)
	ctx := context.Background()

	root := createRequestAndPiece(t, s, "conv1", "Release train", "", "", nil)
	p1 := createRequestAndPiece(t, s, "conv1", "Prereq 1", "", root.ID, nil)
	p2 := createRequestAndPiece(t, s, "conv1", "Prereq 2", "", root.ID, nil)
	p3 := createRequestAndPiece(t, s, "conv1", "Prereq 3 (cancelled)", "", root.ID, nil)
	dep := createRequestAndPiece(t, s, "conv1", "Batch 2 task", "Start after p1 and p2", root.ID, []string{p1.ID, p2.ID})
	depCancelled := createRequestAndPiece(t, s, "conv1", "Depends on cancelled", "", root.ID, []string{p3.ID})

	s.superviseTodo(ctx, p1.ID, "conv1", nil, 0)
	s.superviseTodo(ctx, p2.ID, "conv1", nil, 0)
	s.superviseTodo(ctx, p3.ID, "conv1", nil, 0)

	// Cancelling p3 must NOT satisfy depCancelled.
	if closed, err := s.reviewDoneTodo(ctx, p3.ID, "cancelled", false); err != nil || !closed {
		t.Fatalf("cancel p3: closed=%v err=%v", closed, err)
	}
	// Completing only 1 of 2 prerequisites must NOT emit a premature wake.
	if closed, err := s.reviewDoneTodo(ctx, p1.ID, "accepted", true); err != nil || !closed {
		t.Fatalf("accept p1: closed=%v err=%v", closed, err)
	}
	requireNoTodoReadyQueued(t, s, "premature wake after partial satisfaction / cancellation")
	if got := len(sendsToConversation(f, "conv1")); got != 0 {
		t.Fatalf("premature wake after partial satisfaction / cancellation: %v", sendsToConversation(f, "conv1"))
	}

	// Completing the second prerequisite satisfies all after edges of dep (and still not depCancelled).
	if closed, err := s.reviewDoneTodo(ctx, p2.ID, "accepted", true); err != nil || !closed {
		t.Fatalf("accept p2: closed=%v err=%v", closed, err)
	}
	waitFor(t, "multi-prereq todo_ready wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
	msg := sendsToConversation(f, "conv1")[0]
	for _, want := range []string{"signal=todo_ready", dep.ID, p1.ID, p1.Title, p2.ID, p2.Title, "Start after p1 and p2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("multi-prereq wake missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, depCancelled.ID) {
		t.Fatalf("cancelled prerequisite unexpectedly woke %s:\n%s", depCancelled.ID, msg)
	}
}

func TestTodoAfterReadyPRMergeExactDelivery(t *testing.T) {
	t.Run("single delivery PR open and closed do not satisfy; merged satisfies once", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Feature", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Backend PR", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Frontend follow-up", "Rebase onto main after backend merge", root.ID, []string{pre.ID})

		prURL := "https://github.com/example/app/pull/101"
		s.setTaskStage(ctx, pre.ID, "review", prURL)
		now := time.Now().UTC()

		// Merely opening a PR must NOT satisfy a merge dependency.
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: prURL, State: "open", CheckedAt: now}); err != nil {
			t.Fatalf("apply open PR: %v", err)
		}
		// Closing without merging must NOT satisfy either.
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: prURL, State: "closed", CheckedAt: now.Add(time.Second)}); err != nil {
			t.Fatalf("apply closed PR: %v", err)
		}
		requireNoTodoReadyQueued(t, s, "non-merged PR emitted premature wake")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("non-merged PR emitted premature wake: %v", sendsToConversation(f, "conv1"))
		}

		// Merging the delivery PR emits todo_ready with the merged PR URL.
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: prURL, State: "merged", CheckedAt: now.Add(2 * time.Second)}); err != nil {
			t.Fatalf("apply merged PR: %v", err)
		}
		waitFor(t, "PR merge todo_ready wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		msg := sendsToConversation(f, "conv1")[0]
		for _, want := range []string{"signal=todo_ready", dep.ID, pre.ID, prURL, "PR 已合并", "Rebase onto main after backend merge"} {
			if !strings.Contains(msg, want) {
				t.Errorf("PR merge wake missing %q:\n%s", want, msg)
			}
		}

		// Later accepting/completing pre must NOT emit a duplicate todo_ready wake for dep.
		if closed, err := s.reviewDoneTodo(ctx, pre.ID, "accepted after merge", true); err != nil || !closed {
			t.Fatalf("reviewDoneTodo after merge: closed=%v err=%v", closed, err)
		}
		requireNoTodoReadyQueued(t, s, "completing already-merged prerequisite re-emitted wake")
		if got := len(sendsToConversation(f, "conv1")); got != 1 {
			t.Fatalf("completing already-merged prerequisite re-emitted wake: got %d wakes", got)
		}
	})

	t.Run("multi-PR prerequisite waits until all bound delivery PRs merge", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Multi-PR feature", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Two-PR backend", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Dependent task", "Notes", root.ID, []string{pre.ID})

		pr1 := "https://github.com/example/app/pull/201"
		pr2 := "https://github.com/example/app/pull/202"
		s.setTaskStage(ctx, pre.ID, "review", pr1)
		s.setTaskStage(ctx, pre.ID, "review", pr2) // pr1 moves to pre.PRs, pr2 is pre.PRURL
		now := time.Now().UTC()

		// Merge pr1 while pr2 is still open -> no wake.
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: pr1, State: "merged", CheckedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: pr2, State: "open", CheckedAt: now}); err != nil {
			t.Fatal(err)
		}
		requireNoTodoReadyQueued(t, s, "partial PR merge emitted premature wake")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("partial PR merge emitted premature wake: %v", sendsToConversation(f, "conv1"))
		}

		// Merge pr2 -> now all bound delivery PRs are merged.
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: pr2, State: "merged", CheckedAt: now.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "all-PRs-merged wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		msg := sendsToConversation(f, "conv1")[0]
		for _, want := range []string{dep.ID, pr1, pr2} {
			if !strings.Contains(msg, want) {
				t.Errorf("multi-PR wake missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("reference-role PR is ignored while unmerged delivery-role repo PR blocks", func(t *testing.T) {
		repoStore := &todoReadyRepoStore{
			memStore: &memStore{},
			repos:    map[string]repository.TaskSet{},
		}
		s, f := newTodoReadyService(t, repoStore)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Repo roles", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq with repo bindings", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Next piece", "", root.ID, []string{pre.ID})

		prDelivery1 := "https://github.com/example/app/pull/301"
		prDelivery2 := "https://github.com/example/app/pull/302"
		prRef := "https://github.com/example/app/pull/999"
		s.setTaskStage(ctx, pre.ID, "review", prDelivery1)

		// Bind prDelivery2 as delivery (unmerged) and prRef as reference.
		repoStore.repos[pre.ID] = repository.TaskSet{
			TodoID: pre.ID,
			Repositories: []repository.TaskRepository{{
				RepoID: "repo_1",
				Links: []repository.TaskLink{
					{Kind: "pull_request", CanonicalURL: prDelivery1, Role: repository.LinkRoleDelivery},
					{Kind: "pull_request", CanonicalURL: prDelivery2, Role: repository.LinkRoleDelivery},
					{Kind: "pull_request", CanonicalURL: prRef, Role: repository.LinkRoleReference},
				},
			}},
		}
		now := time.Now().UTC()
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: prDelivery1, State: "merged", CheckedAt: now}); err != nil {
			t.Fatal(err)
		}
		requireNoTodoReadyQueued(t, s, "unmerged delivery repo PR failed to block wake")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("unmerged delivery repo PR failed to block wake: %v", sendsToConversation(f, "conv1"))
		}

		// Downgrade prDelivery2 to reference (only prDelivery1 is a delivery PR, and it is already merged).
		repoStore.repos[pre.ID] = repository.TaskSet{
			TodoID: pre.ID,
			Repositories: []repository.TaskRepository{{
				RepoID: "repo_1",
				Links: []repository.TaskLink{
					{Kind: "pull_request", CanonicalURL: prDelivery1, Role: repository.LinkRoleDelivery},
					{Kind: "pull_request", CanonicalURL: prDelivery2, Role: repository.LinkRoleReference},
					{Kind: "pull_request", CanonicalURL: prRef, Role: repository.LinkRoleReference},
				},
			}},
		}
		s.sweepTodoAfterReady(ctx)
		waitFor(t, "reference-only remaining PR unblocks", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		msg := sendsToConversation(f, "conv1")[0]
		if !strings.Contains(msg, dep.ID) || !strings.Contains(msg, prDelivery1) || strings.Contains(msg, prRef) {
			t.Fatalf("unexpected wake payload:\n%s", msg)
		}
	})
}

func TestTodoAfterReadySkipsAlreadyStartedDependent(t *testing.T) {
	st := &memStore{}
	s, f := newTodoReadyService(t, st)
	ctx := context.Background()

	root := createRequestAndPiece(t, s, "conv1", "Skip started", "", "", nil)
	pre := createRequestAndPiece(t, s, "conv1", "Prereq", "", root.ID, nil)
	dep := createRequestAndPiece(t, s, "conv1", "Already started dependent", "", root.ID, []string{pre.ID})

	// Link an active Agent to dep before pre completes.
	if err := s.linkTodoAgent(ctx, dep.ID, "box", "agent_dep", "Working on dep", "/code/app", "claude"); err != nil {
		t.Fatalf("linkTodoAgent: %v", err)
	}
	done := true
	if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatalf("complete pre: %v", err)
	}
	requireNoTodoReadyQueued(t, s, "already-started dependent received unexpected wake")
	if got := len(sendsToConversation(f, "conv1")); got != 0 {
		t.Fatalf("already-started dependent received unexpected wake: %v", sendsToConversation(f, "conv1"))
	}

	// Also verify delivery-time revalidation: if a dependent gains an active Agent while the signal is gathered,
	// digestEntry drops the stale wake.
	pre2 := createRequestAndPiece(t, s, "conv1", "Prereq 2", "", root.ID, nil)
	dep2 := createRequestAndPiece(t, s, "conv1", "Started during gather", "", root.ID, []string{pre2.ID})
	s.superviseTimes.gather = 5 * time.Second
	if _, err := s.editTodo(ctx, pre2.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatalf("complete pre2: %v", err)
	}
	if err := s.linkTodoAgent(ctx, dep2.ID, "box", "agent_dep2", "Started meanwhile", "/code/app", "claude"); err != nil {
		t.Fatalf("linkTodoAgent dep2: %v", err)
	}
	if _, ok := s.digestEntry(ctx, superviseSignal{Todo: dep2.ID, Kind: sigTodoReady, Key: todoReadyKey(dep2), To: "conv1"}, "https://persona.example"); ok {
		t.Fatal("digestEntry did not drop stale todo_ready after dependent gained an active Agent")
	}
}

func TestTodoAfterReadyRoutesToOwningLeader(t *testing.T) {
	st := &memStore{}
	s, f := newTodoReadyService(t, st)
	ctx := context.Background()

	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	f.known["lead1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)

	root, err := s.createDirectionTodo(ctx, "lead1", "Leader project", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	partOf := root.ID
	pre, err := s.createDirectionTodo(ctx, "lead1", "Leader step 1", "", db.TodoEdit{PartOf: &partOf})
	if err != nil {
		t.Fatal(err)
	}
	after := []string{pre.ID}
	dep, err := s.createDirectionTodo(ctx, "lead1", "Leader step 2", "Run leader step 2 on box", db.TodoEdit{PartOf: &partOf, After: &after})
	if err != nil {
		t.Fatal(err)
	}

	if closed, err := s.reviewDoneTodo(ctx, pre.ID, "accepted", true); err != nil || !closed {
		t.Fatalf("accept pre: closed=%v err=%v", closed, err)
	}
	waitFor(t, "leader todo_ready wake", func() bool { return len(sendsToConversation(f, "lead1")) == 1 })
	msg := sendsToConversation(f, "lead1")[0]
	for _, want := range []string{"signal=todo_ready", dep.ID, dep.Title, pre.ID, "Run leader step 2 on box"} {
		if !strings.Contains(msg, want) {
			t.Errorf("leader wake missing %q:\n%s", want, msg)
		}
	}
	if got := len(sendsToConversation(f, "conv1")); got != 0 {
		t.Fatalf("main chat received %d wakes for leader-owned dependent, want 0", got)
	}
}

func TestTodoAfterReadyRestartAndResweepIdempotency(t *testing.T) {
	st := &memStore{}
	s1, f1 := newTodoReadyService(t, st)
	ctx := context.Background()

	root := createRequestAndPiece(t, s1, "conv1", "Restart test", "", "", nil)
	pre := createRequestAndPiece(t, s1, "conv1", "Prereq A", "", root.ID, nil)
	dep := createRequestAndPiece(t, s1, "conv1", "Dependent B", "Launch instructions", root.ID, []string{pre.ID})

	// Complete pre -> emits exactly one wake and persists durable marker.
	done := true
	if _, err := s1.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first wake", func() bool { return len(sendsToConversation(f1, "conv1")) == 1 })

	waitForTodoReadyReceipt(t, st, dep)

	// Repeated sweeps on the same Service must NOT re-emit.
	s1.sweepTodoAfterReady(ctx)
	s1.superviseWatch(ctx, time.Now())
	s1.evaluateTodoAfterDependents(ctx, pre.ID)
	requireNoTodoReadyQueued(t, s1, "repeated sweeps emitted")
	if got := len(sendsToConversation(f1, "conv1")); got != 1 {
		t.Fatalf("repeated sweeps emitted %d wakes, want 1", got)
	}

	// Simulate Hub restart: brand-new Service sharing the same persistent store and reloaded todos.
	s2, f2 := newTodoReadyService(t, st)
	rootCopy, preCopy, depCopy := todoNow(s1, root.ID), todoNow(s1, pre.ID), todoNow(s1, dep.ID)
	s2.todos.mu.Lock()
	s2.todos.loaded = true
	s2.todos.todos = map[string]*Todo{
		root.ID: &rootCopy,
		pre.ID:  &preCopy,
		dep.ID:  &depCopy,
	}
	s2.todos.mu.Unlock()

	s2.sweepTodoAfterReady(ctx)
	s2.superviseWatch(ctx, time.Now())
	requireNoTodoReadyQueued(t, s2, "restarted service re-emitted")
	if got := len(sendsToConversation(f2, "conv1")); got != 0 {
		t.Fatalf("restarted service re-emitted %d duplicate wakes, want 0", got)
	}

	// Crash-before-delivery recovery: a second dependent whose marker was never persisted IS recovered on sweep once.
	dep2 := createRequestAndPiece(t, s2, "conv1", "Dependent C (unwoken before crash)", "Recover me", root.ID, []string{pre.ID})
	s2.sweepTodoAfterReady(ctx)
	waitFor(t, "recovered wake for C", func() bool { return len(sendsToConversation(f2, "conv1")) == 1 })
	if msg := sendsToConversation(f2, "conv1")[0]; !strings.Contains(msg, dep2.ID) || !strings.Contains(msg, "Recover me") {
		t.Fatalf("recovered wake mismatch:\n%s", msg)
	}
	s2.sweepTodoAfterReady(ctx)
	requireNoTodoReadyQueued(t, s2, "recovered dependent re-emitted wake")
	if got := len(sendsToConversation(f2, "conv1")); got != 1 {
		t.Fatalf("recovered dependent re-emitted wake: got %d", got)
	}
}

func TestTodoAfterReadyPromptRendering(t *testing.T) {
	entry := prompts.SuperviseEntry{
		ID:    "todo_dep1",
		Title: "Batch 2 task",
		Kind:  sigTodoReady,
		Link:  "https://persona.example/todos/todo_dep1",
		TodoReady: &prompts.SuperviseTodoReady{
			PartOf: "todo_root1",
			Notes:  "Run migration script on box",
			Satisfied: []prompts.SupervisePrerequisite{
				{ID: "todo_pre1", Title: "Schema PR", Reason: "pr_merged", MergedPRs: []string{"https://github.com/example/app/pull/42"}},
			},
		},
	}
	got := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{entry}})
	for _, want := range []string{
		"signal=todo_ready",
		"todo_dep1",
		"Batch 2 task",
		"https://persona.example/todos/todo_dep1",
		"todo_root1",
		"todo_pre1",
		"Schema PR",
		"PR 已合并",
		"https://github.com/example/app/pull/42",
		"Run migration script on box",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderSupervise missing %q:\n%s", want, got)
		}
	}
	worker := prompts.RenderWorkerSupervise([]prompts.SuperviseEntry{entry})
	for _, want := range []string{"todo_dep1", "Batch 2 task", "todo_pre1", "https://github.com/example/app/pull/42", "Run migration script on box"} {
		if !strings.Contains(worker, want) {
			t.Errorf("RenderWorkerSupervise missing %q:\n%s", want, worker)
		}
	}
}

func TestTodoAfterReadyFailedStartAndRequireAcceptedGateAndUpdatedAfterSet(t *testing.T) {
	t.Run("failed-start prerequisite never satisfies dependent", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Fail gate", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Failed start prereq", "", root.ID, nil)
		_ = createRequestAndPiece(t, s, "conv1", "Waiting dep", "", root.ID, []string{pre.ID})

		if err := s.failTodo(ctx, pre.ID, "launcher unavailable"); err != nil {
			t.Fatalf("failTodo: %v", err)
		}
		s.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s, "failed-start prerequisite emitted wake")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("failed-start prerequisite emitted wake: %v", sendsToConversation(f, "conv1"))
		}
	})

	t.Run("RequireAcceptedAfter root requires accepted review, not PR merge alone", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		enforced := true
		root, err := s.createPieceTodo(ctx, "conv1", "Strict root", "", db.TodoEdit{RequireAcceptedAfter: &enforced})
		if err != nil {
			t.Fatal(err)
		}
		pre := createRequestAndPiece(t, s, "conv1", "Strict prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Strict dependent", "Strict notes", root.ID, []string{pre.ID})
		s.superviseTodo(ctx, pre.ID, "conv1", nil, 0)

		prURL := "https://github.com/example/app/pull/501"
		s.setTaskStage(ctx, pre.ID, "review", prURL)
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: prURL, State: "merged", CheckedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		requireNoTodoReadyQueued(t, s, "PR merge bypassed RequireAcceptedAfter")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("PR merge bypassed RequireAcceptedAfter: %v", sendsToConversation(f, "conv1"))
		}

		// Record accepted review and close via reviewDoneTodo.
		if _, _, err := s.updateSupervision(ctx, pre.ID, func(v *Supervision) bool {
			v.State = reviewAccepted
			v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted})
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if closed, err := s.reviewDoneTodo(ctx, pre.ID, "accepted", true); err != nil || !closed {
			t.Fatalf("reviewDoneTodo: closed=%v err=%v", closed, err)
		}
		waitFor(t, "strict accepted wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		if msg := sendsToConversation(f, "conv1")[0]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, prURL) {
			t.Fatalf("strict wake missing dep/PR:\n%s", msg)
		}
	})

	t.Run("adding a new prerequisite later emits a fresh wake once it completes", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Dynamic deps", "", "", nil)
		p1 := createRequestAndPiece(t, s, "conv1", "First prereq", "", root.ID, nil)
		p2 := createRequestAndPiece(t, s, "conv1", "Second prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Dynamic dependent", "", root.ID, []string{p1.ID})

		done := true
		if _, err := s.editTodo(ctx, p1.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "first wake for [p1]", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })

		// Edit dep to also wait on p2, then complete p2 -> canonical key changes and emits a fresh wake.
		both := []string{p1.ID, p2.ID}
		if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{After: &both}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.editTodo(ctx, p2.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "second wake for [p1, p2]", func() bool { return len(sendsToConversation(f, "conv1")) == 2 })
		if msg := sendsToConversation(f, "conv1")[1]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, p2.ID) {
			t.Fatalf("second wake missing p2:\n%s", msg)
		}
	})
}

func TestTodoAfterReadyFallbackWithoutSupervisionPolicyGoesToMainNotBackground(t *testing.T) {
	st := &memStore{}
	s, f := newTodoReadyService(t, st)
	ctx := context.Background()

	s.noBackground = false
	now := time.Now().UTC()
	setSettings(t, s, func(v *Settings) {
		v.Leaders = []string{"lead_no_policy"}
		v.BackgroundSupervisor = "bg1"
		v.BackgroundSupervisorMachineID = "server"
		v.BackgroundSupervisorAt = &now
	})
	f.known["lead_no_policy"] = true
	f.known["bg1"] = true
	// lead_no_policy is hosted by a runtime lacking supervision_policy; digestTarget must fall back to Main, not bg1.
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead_no_policy", MachineID: "server", Assistant: true, Status: proto.StatusIdle,
			SourceKind: proto.SourceRuntime},
	)

	root, err := s.createDirectionTodo(ctx, "lead_no_policy", "Leader project", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	partOf := root.ID
	pre, err := s.createDirectionTodo(ctx, "lead_no_policy", "Prereq", "", db.TodoEdit{PartOf: &partOf})
	if err != nil {
		t.Fatal(err)
	}
	after := []string{pre.ID}
	dep, err := s.createDirectionTodo(ctx, "lead_no_policy", "Dependent", "Main fallback notes", db.TodoEdit{PartOf: &partOf, After: &after})
	if err != nil {
		t.Fatal(err)
	}

	cfg := s.settings(ctx)
	sig := superviseSignal{Todo: dep.ID, Kind: sigTodoReady, Key: todoReadyKey(dep), To: "lead_no_policy"}
	if got := s.digestTarget(ctx, cfg, sig); got != "conv1" {
		t.Fatalf("digestTarget(todo_ready without supervision_policy) = %q, want conv1 (not bg1)", got)
	}

	if closed, err := s.reviewDoneTodo(ctx, pre.ID, "accepted", true); err != nil || !closed {
		t.Fatalf("accept pre: closed=%v err=%v", closed, err)
	}
	waitFor(t, "fallback todo_ready wake to main", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
	if msg := sendsToConversation(f, "conv1")[0]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, "Main fallback notes") {
		t.Fatalf("unexpected fallback wake:\n%s", msg)
	}
	if got := len(sendsToConversation(f, "bg1")); got != 0 {
		t.Fatalf("background supervisor got %d todo_ready wakes, want 0", got)
	}
	if got := len(sendsToConversation(f, "lead_no_policy")); got != 0 {
		t.Fatalf("unsupported runtime leader got %d todo_ready wakes, want 0", got)
	}
}

func TestTodoAfterReadyRepoStoreUnavailableFailsClosedAndRetriesAfterRecovery(t *testing.T) {
	t.Run("store_unavailable fails closed instead of falling back to stage PRs", func(t *testing.T) {
		repoStore := &todoReadyRepoStore{
			memStore: &memStore{},
			repos:    map[string]repository.TaskSet{},
			err:      repository.Problem("store_unavailable"),
		}
		s, f := newTodoReadyService(t, repoStore)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Fail-closed root", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq with extra repo PR", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Blocked dependent", "Notes", root.ID, []string{pre.ID})

		pr1 := "https://github.com/example/app/pull/601"
		pr2 := "https://github.com/example/app/pull/602"
		s.setTaskStage(ctx, pre.ID, "review", pr1)

		// Even though pr1 is merged on stage, store_unavailable must fail closed because repo bindings may hold unmerged pr2.
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: pr1, State: "merged", CheckedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		requireNoTodoReadyQueued(t, s, "store_unavailable fell back to stage PR and emitted premature wake")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("store_unavailable fell back to stage PR and emitted premature wake: %v", sendsToConversation(f, "conv1"))
		}

		// Once store recovers showing unmerged pr2 as delivery, dependent stays blocked.
		repoStore.setRepo(pre.ID, repository.TaskSet{
			TodoID: pre.ID,
			Repositories: []repository.TaskRepository{{
				RepoID: "repo_1",
				Links: []repository.TaskLink{
					{Kind: "pull_request", CanonicalURL: pr1, Role: repository.LinkRoleDelivery},
					{Kind: "pull_request", CanonicalURL: pr2, Role: repository.LinkRoleDelivery},
				},
			}},
		})
		repoStore.setErr(nil)
		s.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s, "unmerged delivery PR failed to block after store recovery")
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("unmerged delivery PR failed to block after store recovery: %v", sendsToConversation(f, "conv1"))
		}

		// When only merged pr1 is a delivery PR, sweep emits todo_ready cleanly.
		repoStore.setRepo(pre.ID, repository.TaskSet{
			TodoID: pre.ID,
			Repositories: []repository.TaskRepository{{
				RepoID: "repo_1",
				Links: []repository.TaskLink{
					{Kind: "pull_request", CanonicalURL: pr1, Role: repository.LinkRoleDelivery},
				},
			}},
		})
		s.sweepTodoAfterReady(ctx)
		waitFor(t, "wake after store recovery", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		if msg := sendsToConversation(f, "conv1")[0]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, pr1) {
			t.Fatalf("unexpected recovery wake:\n%s", msg)
		}
	})

	t.Run("transient readiness error during deliverDigest does not permanently suppress wake", func(t *testing.T) {
		repoStore := &todoReadyRepoStore{
			memStore: &memStore{},
			repos:    map[string]repository.TaskSet{},
		}
		s, f := newTodoReadyService(t, repoStore)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Retry root", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Retry dependent", "Retry launch notes", root.ID, []string{pre.ID})

		pr1 := "https://github.com/example/app/pull/701"
		s.setTaskStage(ctx, pre.ID, "review", pr1)
		repoStore.setRepo(pre.ID, repository.TaskSet{
			TodoID: pre.ID,
			Repositories: []repository.TaskRepository{{
				RepoID: "repo_1",
				Links: []repository.TaskLink{
					{Kind: "pull_request", CanonicalURL: pr1, Role: repository.LinkRoleDelivery},
				},
			}},
		})

		// Pause auto-timer so we can inject a transient store outage right at sendDigest delivery time.
		s.superviseTimes.gather = 5 * time.Second
		if err := s.applyPRStatus(ctx, todoNow(s, pre.ID), TodoPRStatus{URL: pr1, State: "merged", CheckedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		// Make the pending signal immediately due, then inject transient store outage before sendDigest.
		s.supervise.mu.Lock()
		if s.supervise.timer != nil {
			s.supervise.timer.Stop()
			s.supervise.timer, s.supervise.timerAt = nil, time.Time{}
		}
		sigID := dep.ID + "|" + sigTodoReady
		g, ok := s.supervise.pending[sigID]
		if !ok {
			s.supervise.mu.Unlock()
			t.Fatalf("expected pending signal %s", sigID)
		}
		g.At = time.Now().Add(-10 * time.Second)
		s.supervise.pending[sigID] = g
		s.supervise.mu.Unlock()

		s.superviseTimes.gather = shortSuperviseTimings.gather
		s.superviseTimes.urgent = shortSuperviseTimings.urgent
		repoStore.setErr(repository.Problem("store_unavailable"))
		s.sendDigest(ctx)

		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("outage during sendDigest unexpectedly delivered wake: %v", sendsToConversation(f, "conv1"))
		}
		// Signal must be restored to pending and b.sent cleared so recovery delivers without waiting for restart.
		s.supervise.mu.Lock()
		_, stillPending := s.supervise.pending[sigID]
		_, stuckInSent := s.supervise.sent[sigID]
		s.supervise.mu.Unlock()
		if !stillPending || stuckInSent {
			t.Fatalf("transient delivery failure poisoned signal state: pending=%v sent=%v", stillPending, stuckInSent)
		}

		// Store recovers: retry timer (or sweep) delivers the wake cleanly.
		repoStore.setErr(nil)
		waitFor(t, "retried todo_ready wake after recovery", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		if msg := sendsToConversation(f, "conv1")[0]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, pr1) || !strings.Contains(msg, "Retry launch notes") {
			t.Fatalf("unexpected retried wake:\n%s", msg)
		}
	})

	t.Run("moot todo_ready drop clears sent cache so later re-completion still wakes", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Reopen root", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Dependent", "Reopen notes", root.ID, []string{pre.ID})

		s.superviseTimes.gather = 5 * time.Second
		done := true
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		// Reopen pre before sendDigest runs so the queued todo_ready becomes moot at delivery time.
		open := false
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &open}); err != nil {
			t.Fatal(err)
		}
		s.supervise.mu.Lock()
		if s.supervise.timer != nil {
			s.supervise.timer.Stop()
			s.supervise.timer, s.supervise.timerAt = nil, time.Time{}
		}
		sigID := dep.ID + "|" + sigTodoReady
		g := s.supervise.pending[sigID]
		g.At = time.Now().Add(-10 * time.Second)
		s.supervise.pending[sigID] = g
		s.supervise.mu.Unlock()

		s.superviseTimes.gather = shortSuperviseTimings.gather
		s.sendDigest(ctx)
		if got := len(sendsToConversation(f, "conv1")); got != 0 {
			t.Fatalf("moot wake was delivered after prereq reopened: %v", sendsToConversation(f, "conv1"))
		}

		// Re-complete pre: b.sent must have been cleared on moot drop so the wake is delivered now.
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "wake after prereq re-completed", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		if msg := sendsToConversation(f, "conv1")[0]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, "Reopen notes") {
			t.Fatalf("unexpected wake after re-completion:\n%s", msg)
		}
	})
}

func TestTodoAfterReadyReopenDependentClearsWakeStateAndAllowsNextWake(t *testing.T) {
	t.Run("closing and reopening a woken dependent without intermediate prune wakes again on next prerequisite completion", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Reopen cycle root", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Dependent", "Reopen cycle notes", root.ID, []string{pre.ID})

		done, open := true, false
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "initial todo_ready wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })

		waitForTodoReadyReceipt(t, st, dep)
		// Confirm the initial wake's dedup fingerprint was persisted.
		initialKey := todoReadyKey(dep)
		dedupKey, _ := digestFingerprint(prompts.SuperviseEntry{}, superviseSignal{Todo: dep.ID, Kind: sigTodoReady})
		dedup := map[string]superviseFingerprint{}
		if found, err := st.Setting(ctx, superviseDedupKey, &dedup); err != nil || !found {
			t.Fatalf("initial dedup = %+v (found=%v err=%v)", dedup, found, err)
		}
		if _, ok := dedup[dedupKey]; !ok {
			t.Fatalf("expected initial todo_ready dedup entry %s in %+v", dedupKey, dedup)
		}

		// Close and reopen dep (and reopen pre) without running any prune sweep in between.
		if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &open}); err != nil {
			t.Fatal(err)
		}
		reopenedDep, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Done: &open})
		if err != nil {
			t.Fatal(err)
		}
		if reopenedDep.ReopenedAt == nil {
			t.Fatalf("expected ReopenedAt set on reopened dependent")
		}
		if reopenedKey := todoReadyKey(reopenedDep); reopenedKey == initialKey {
			t.Fatalf("reopened todoReadyKey stayed %q, want new generation key", reopenedKey)
		}

		// Durable wake marker, todo_ready dedup fingerprint, and in-memory pending/sent must be cleared on reopen.
		markersAfterReopen := map[string]todoReadyWakeMarker{}
		if _, err := st.Setting(ctx, todoReadyWakesKey, &markersAfterReopen); err != nil {
			t.Fatal(err)
		}
		if _, exists := markersAfterReopen[dep.ID]; exists {
			t.Fatalf("reopen left stale wake marker for %s: %+v", dep.ID, markersAfterReopen)
		}
		dedupAfterReopen := map[string]superviseFingerprint{}
		if _, err := st.Setting(ctx, superviseDedupKey, &dedupAfterReopen); err != nil {
			t.Fatal(err)
		}
		if _, exists := dedupAfterReopen[dedupKey]; exists {
			t.Fatalf("reopen left stale todo_ready dedup entry %s: %+v", dedupKey, dedupAfterReopen)
		}
		sigID := dep.ID + "|" + sigTodoReady
		s.supervise.mu.Lock()
		_, stillPending := s.supervise.pending[sigID]
		_, stillSent := s.supervise.sent[sigID]
		s.supervise.mu.Unlock()
		if stillPending || stillSent {
			t.Fatalf("reopen left stale signal state: pending=%v sent=%v", stillPending, stillSent)
		}

		// While pre is open, sweep must not wake dep prematurely.
		s.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s, "premature wake while reopened prereq is open")
		if got := len(sendsToConversation(f, "conv1")); got != 1 {
			t.Fatalf("premature wake while reopened prereq is open: got %d, want 1", got)
		}

		// Re-completing pre must emit a fresh wake for the reopened dependent.
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "second todo_ready wake after reopen", func() bool { return len(sendsToConversation(f, "conv1")) == 2 })
		if msg := sendsToConversation(f, "conv1")[1]; !strings.Contains(msg, dep.ID) || !strings.Contains(msg, "Reopen cycle notes") {
			t.Fatalf("unexpected second wake:\n%s", msg)
		}

		waitForTodoReadyReceipt(t, st, reopenedDep)

		// Repeated sweeps and a simulated restart (with Postgres microsecond ReopenedAt truncation) must not duplicate.
		s.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s, "re-sweep duplicated reopened wake")
		if got := len(sendsToConversation(f, "conv1")); got != 2 {
			t.Fatalf("re-sweep duplicated reopened wake: got %d, want 2", got)
		}

		s2, f2 := newTodoReadyService(t, st)
		rootCopy, preCopy, depCopy := todoNow(s, root.ID), todoNow(s, pre.ID), todoNow(s, dep.ID)
		if depCopy.ReopenedAt != nil {
			trunc := depCopy.ReopenedAt.UTC().Truncate(time.Microsecond)
			depCopy.ReopenedAt = &trunc
		}
		s2.todos.mu.Lock()
		s2.todos.loaded = true
		s2.todos.todos = map[string]*Todo{
			root.ID: &rootCopy,
			pre.ID:  &preCopy,
			dep.ID:  &depCopy,
		}
		s2.todos.mu.Unlock()

		s2.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s2, "restarted service re-emitted")
		if got := len(sendsToConversation(f2, "conv1")); got != 0 {
			t.Fatalf("restarted service re-emitted %d duplicate wakes for reopened dependent, want 0", got)
		}
	})

	t.Run("closing and reopening a woken dependent while prerequisite remains done wakes once on sweep", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "Reopen sweep root", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Dependent", "Sweep notes", root.ID, []string{pre.ID})

		done, open := true, false
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "initial wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		waitForTodoReadyReceipt(t, st, dep)

		if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Done: &open}); err != nil {
			t.Fatal(err)
		}

		s.sweepTodoAfterReady(ctx)
		waitFor(t, "reopened dependent sweep wake", func() bool { return len(sendsToConversation(f, "conv1")) == 2 })
		s.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s, "second sweep duplicated reopened wake")
		if got := len(sendsToConversation(f, "conv1")); got != 2 {
			t.Fatalf("second sweep duplicated reopened wake: got %d, want 2", got)
		}
	})

	t.Run("no-op reopen on an already-open woken dependent preserves wake deduplication", func(t *testing.T) {
		st := &memStore{}
		s, f := newTodoReadyService(t, st)
		ctx := context.Background()

		root := createRequestAndPiece(t, s, "conv1", "No-op reopen root", "", "", nil)
		pre := createRequestAndPiece(t, s, "conv1", "Prereq", "", root.ID, nil)
		dep := createRequestAndPiece(t, s, "conv1", "Dependent", "No-op notes", root.ID, []string{pre.ID})

		done, open := true, false
		if _, err := s.editTodo(ctx, pre.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "initial wake", func() bool { return len(sendsToConversation(f, "conv1")) == 1 })
		waitForTodoReadyReceipt(t, st, dep)

		if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Done: &open}); err != nil {
			t.Fatal(err)
		}
		s.sweepTodoAfterReady(ctx)
		requireNoTodoReadyQueued(t, s, "no-op reopen caused duplicate wake")
		if got := len(sendsToConversation(f, "conv1")); got != 1 {
			t.Fatalf("no-op reopen caused duplicate wake: got %d, want 1", got)
		}
	})
}

// Delivery records the send before the digest persists its wake marker.
func waitForTodoReadyReceipt(t *testing.T, st *memStore, dep Todo) {
	t.Helper()
	key := todoReadyKey(dep)
	waitFor(t, "persisted wake marker "+key, func() bool {
		var markers map[string]todoReadyWakeMarker
		found, err := st.Setting(t.Context(), todoReadyWakesKey, &markers)
		if err != nil {
			t.Fatal(err)
		}
		return found && markers[dep.ID].Key == key
	})
}

// requireNoTodoReadyQueued checks, right after the call that could have raised one, that no todo_ready wake waits to
// go out. The call checks the dependents' prerequisites itself (evaluateTodoAfterDependents, sweepTodoAfterReady) and
// queues a wake on the spot (s.signal), while the digest takes the queue only a gather later: a wake raised by the call
// is still in the queue here, and one delivered before shows in the sends the caller counts next.
func requireNoTodoReadyQueued(t *testing.T, s *Service, why string) {
	t.Helper()
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range b.pending {
		if g.Kind == sigTodoReady {
			t.Fatalf("%s: a todo_ready wake for %s is queued", why, g.Todo)
		}
	}
}
