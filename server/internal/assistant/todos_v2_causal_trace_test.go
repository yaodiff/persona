package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

type memCausalTraceStore struct {
	*memBlockerQueueStore
	decMu     sync.Mutex
	decisions map[string][]db.TodoDecisionRow
}

func newMemCausalTraceStore() *memCausalTraceStore {
	return &memCausalTraceStore{
		memBlockerQueueStore: newMemBlockerQueueStore(),
		decisions:            make(map[string][]db.TodoDecisionRow),
	}
}

func (m *memCausalTraceStore) addDecision(row db.TodoDecisionRow) {
	m.decMu.Lock()
	defer m.decMu.Unlock()
	m.decisions[row.TodoID] = append(m.decisions[row.TodoID], row)
}

func (m *memCausalTraceStore) ListTodoDecisions(_ context.Context, todoID string, _ bool) ([]db.TodoDecisionRow, error) {
	m.decMu.Lock()
	defer m.decMu.Unlock()
	rows := m.decisions[todoID]
	out := make([]db.TodoDecisionRow, len(rows))
	copy(out, rows)
	return out, nil
}

func (m *memCausalTraceStore) FeedByRef(_ context.Context, _ string, _ []string, _ int) ([]db.FeedRow, error) {
	return nil, nil
}

func newCausalTraceTestEnv(t *testing.T, todosV2 bool) (*Service, *fakeHub, *memCausalTraceStore) {
	t.Helper()
	st := newMemCausalTraceStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	if todosV2 {
		setBlockersTodosV2Flag(t, s, true, false)
	}
	return s, f, st
}

func getCausalTrace(s *Service, id string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/todos/"+id+"/trace", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestTodosV2CausalTrace_EndToEndChronologicalSynthesis(t *testing.T) {
	s, _, st := newCausalTraceTestEnv(t, true)
	ctx := context.Background()

	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	reopenedAt := t0.Add(15 * time.Minute)
	doneAt := t0.Add(60 * time.Minute)
	seenAt := t0.Add(65 * time.Minute)
	blockerAddedAt := t0.Add(5 * time.Minute)
	blockerResolvedAt := t0.Add(25 * time.Minute)
	cancelledBlockerAddedAt := t0.Add(7 * time.Minute)
	cancelledBlockerAt := t0.Add(20 * time.Minute)
	queuedAt := t0.Add(6 * time.Minute)
	checkpointAt := t0.Add(30 * time.Minute)
	recoveredAt := t0.Add(35 * time.Minute)
	reworkAt := t0.Add(40 * time.Minute)
	parkedAt := t0.Add(42 * time.Minute)
	needsUserAt := t0.Add(45 * time.Minute)
	decisionAt := t0.Add(48 * time.Minute)
	prConflictAt := t0.Add(50 * time.Minute)
	prObservedAt := t0.Add(52 * time.Minute)
	prMergedAt := t0.Add(55 * time.Minute)
	acceptedAt := t0.Add(58 * time.Minute)

	todo := &Todo{
		ID:         "todo_trace_1",
		Title:      "Ship causal audit trace",
		CreatedAt:  t0,
		UpdatedAt:  seenAt,
		ReopenedAt: &reopenedAt,
		DoneAt:     &doneAt,
		DoneBy:     "review",
		DoneReason: "all criteria verified",
		SeenAt:     &seenAt,
		Supervision: &Supervision{
			Supervisor: "conv1",
			State:      reviewAccepted,
			UpdatedAt:  acceptedAt,
			Reviews: []TaskReview{
				{At: reworkAt, Verdict: reviewRework, Round: 1, Note: "missing edge case test"},
				{At: parkedAt, Verdict: reviewParked, WaitingOn: "external PR", Reason: "waiting for upstream merge"},
				{At: needsUserAt, Verdict: reviewNeedsUser, Reason: "confirm rollout scope"},
				{At: acceptedAt, Verdict: reviewAccepted, By: "supervisor", Note: "ready to ship", Link: "https://github.com/org/repo/pull/42"},
			},
		},
		PRStatus: &TodoPRStatus{
			URL:       "https://github.com/org/repo/pull/42",
			Title:     "feat: causal trace",
			HeadOID:   "abc1234",
			State:     "merged",
			CheckedAt: prMergedAt,
		},
		OtherPRs: []TodoPRStatus{
			{
				URL:       "https://github.com/org/repo/pull/41",
				Title:     "wip: old attempt",
				HeadOID:   "def5678",
				State:     "open",
				Conflicts: true,
				CheckedAt: prConflictAt,
			},
			{
				URL:       "https://github.com/org/repo/pull/40",
				Title:     "prep: helper",
				State:     "open",
				Checks:    "passing",
				Review:    "approved",
				CheckedAt: prObservedAt,
			},
		},
	}

	s.todos.mu.Lock()
	s.todos.loaded = true
	if s.todos.todos == nil {
		s.todos.todos = make(map[string]*Todo)
	}
	s.todos.todos[todo.ID] = todo
	s.todos.mu.Unlock()

	// Seed active plan queue state.
	plan := TaskPlan{
		ID:           "plan_trace_1",
		Conversation: "conv1",
		State:        planStarted,
		UpdatedAt:    queuedAt,
		Tasks: []PlanTask{
			{
				TodoID: todo.ID,
				Title:  todo.Title,
				Queue: &TaskQueue{
					Key:      blockerWaitQueueKey,
					Reason:   "waiting for external PR: https://github.com/org/repo/pull/10",
					QueuedAt: queuedAt,
					NextAt:   queuedAt.Add(time.Minute),
					Blockers: []string{"https://github.com/org/repo/pull/10"},
				},
			},
		},
	}
	rawTasks, err := json.Marshal(plan.Tasks)
	if err != nil {
		t.Fatal(err)
	}
	st.planMu.Lock()
	st.plans[plan.ID] = db.TaskPlanRow{
		ID:           plan.ID,
		Conversation: plan.Conversation,
		State:        plan.State,
		Tasks:        rawTasks,
		CreatedAt:    queuedAt,
		UpdatedAt:    queuedAt,
	}
	st.planMu.Unlock()

	// Seed persistent blockers (one resolved, one cancelled).
	if _, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:         "blk_resolved",
		TodoID:     todo.ID,
		Kind:       todoBlockerKindExternalPR,
		TargetRef:  "https://github.com/org/repo/pull/10",
		State:      db.TodoBlockerStateResolved,
		Attempts:   2,
		CreatedAt:  blockerAddedAt,
		UpdatedAt:  blockerResolvedAt,
		ResolvedAt: &blockerResolvedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:         "blk_cancelled",
		TodoID:     todo.ID,
		Kind:       todoBlockerKindExternalPR,
		TargetRef:  "https://github.com/org/repo/pull/11",
		State:      db.TodoBlockerStateCancelled,
		Attempts:   1,
		CreatedAt:  cancelledBlockerAddedAt,
		UpdatedAt:  cancelledBlockerAt,
		ResolvedAt: &cancelledBlockerAt,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed restart recovery checkpoint + attempt.
	if err := s.todoRecoveryStore().RecordTodoActiveCheckpoint(ctx, db.TodoRecoveryState{
		TodoID:              todo.ID,
		MachineID:           "server",
		SessionID:           "sess-1",
		LastActiveBootEpoch: "boot-1",
		RecoveryAttempts:    1,
		LastRecoveredAt:     &recoveredAt,
		UpdatedAt:           checkpointAt,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed persisted decision.
	st.addDecision(db.TodoDecisionRow{
		ID:        "dec_trace_1",
		TodoID:    todo.ID,
		Question:  "Rollout scope?",
		Choice:    "Server only under todos_v2",
		Source:    db.DecisionManual,
		CreatedAt: decisionAt,
	})

	w := getCausalTrace(s, todo.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var resp todoCausalTraceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.TodoID != todo.ID || resp.Title != todo.Title {
		t.Fatalf("unexpected header fields: %+v", resp)
	}
	if resp.Count != len(resp.Events) {
		t.Fatalf("count %d != len(events) %d", resp.Count, len(resp.Events))
	}

	expectedKinds := []string{
		"created",
		"blocker_added",
		"queued_blocker",
		"blocker_added",
		"reopened",
		"blocker_cancelled",
		"blocker_resolved",
		"recovery_checkpoint",
		"recovery_attempted",
		"review_rework",
		"parked",
		"needs_user",
		"decision_recorded",
		"pr_conflict",
		"pr_observed",
		"pr_merged",
		"review_accepted",
		"completed",
		"seen",
	}
	if len(resp.Events) != len(expectedKinds) {
		t.Fatalf("expected %d events, got %d: %+v", len(expectedKinds), len(resp.Events), resp.Events)
	}
	for i, wantKind := range expectedKinds {
		ev := resp.Events[i]
		if ev.Kind != wantKind {
			t.Fatalf("event[%d]: expected kind %q, got %q (event=%+v)", i, wantKind, ev.Kind, ev)
		}
		if ev.ID == "" || ev.Category == "" || ev.Summary == "" {
			t.Fatalf("event[%d] missing required fields: %+v", i, ev)
		}
		if i > 0 {
			prev := resp.Events[i-1]
			if ev.At.Before(prev.At) || (ev.At.Equal(prev.At) && ev.ID < prev.ID) {
				t.Fatalf("events out of chronological/id order at %d: prev=%+v cur=%+v", i, prev, ev)
			}
		}
	}

	// Deterministic repeated read produces identical bytes.
	w2 := getCausalTrace(s, todo.ID, nil)
	if w2.Code != http.StatusOK || w2.Body.String() != w.Body.String() {
		t.Fatalf("expected deterministic output on repeat read: first=%s second=%s", w.Body.String(), w2.Body.String())
	}
}

func TestTodosV2CausalTrace_GateAndNotFound(t *testing.T) {
	// todos_v2=false returns 409 Conflict with zero side effects.
	sOff, _, _ := newCausalTraceTestEnv(t, false)
	sOff.todos.mu.Lock()
	sOff.todos.loaded = true
	sOff.todos.todos = map[string]*Todo{
		"todo_gate": {ID: "todo_gate", Title: "Gated todo", CreatedAt: time.Now().UTC()},
	}
	sOff.todos.mu.Unlock()

	wOff := getCausalTrace(sOff, "todo_gate", nil)
	if wOff.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when todos_v2=false, got %d: %s", wOff.Code, wOff.Body.String())
	}

	// Unknown todo under todos_v2=true returns 404 todo_not_found.
	sOn, _, _ := newCausalTraceTestEnv(t, true)
	sOn.todos.mu.Lock()
	sOn.todos.loaded = true
	sOn.todos.todos = map[string]*Todo{}
	sOn.todos.mu.Unlock()

	wMissing := getCausalTrace(sOn, "todo_does_not_exist", nil)
	if wMissing.Code != http.StatusNotFound || !strings.Contains(wMissing.Body.String(), `"todo_not_found"`) {
		t.Fatalf("expected 404 todo_not_found for unknown todo, got %d: %s", wMissing.Code, wMissing.Body.String())
	}
}

func TestTodosV2CausalTrace_ExtensionScopeHidesPrivateTodo(t *testing.T) {
	s, f, _ := newCausalTraceTestEnv(t, true)
	healthDir := identity.HealthDir("/home/owner")
	f.sessions["server"] = []proto.Session{
		scopeSession("conv1", "Persona", ""),
		scopeSession("h1", "Private health check", healthDir),
		scopeSession("s1", "Public repo task", "/work/app"),
	}

	now := time.Now().UTC()
	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		"todo_private": {
			ID:        "todo_private",
			Title:     "Private sleep clinic follow-up",
			CreatedAt: now,
			UpdatedAt: now,
			Agents: []TodoAgent{
				{MachineID: "server", SessionID: "h1", Cwd: healthDir, LinkedAt: now},
			},
		},
		"todo_public": {
			ID:        "todo_public",
			Title:     "Public task",
			CreatedAt: now,
			UpdatedAt: now,
			Agents: []TodoAgent{
				{MachineID: "server", SessionID: "s1", Cwd: "/work/app", LinkedAt: now},
			},
		},
	}
	s.todos.mu.Unlock()

	// Normal device can read the private todo's trace.
	wDevice := getCausalTrace(s, "todo_private", nil)
	if wDevice.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for device, got %d: %s", wDevice.Code, wDevice.Body.String())
	}

	// Extension token gets 404 todo_not_found for hidden private todo with no leaked title.
	wExtPrivate := getCausalTrace(s, "todo_private", map[string]string{RoleHeader: roleExtension})
	if wExtPrivate.Code != http.StatusNotFound || !strings.Contains(wExtPrivate.Body.String(), `"todo_not_found"`) {
		t.Fatalf("expected 404 todo_not_found for extension on hidden todo, got %d: %s", wExtPrivate.Code, wExtPrivate.Body.String())
	}
	if strings.Contains(wExtPrivate.Body.String(), "sleep clinic") {
		t.Fatalf("extension response leaked private todo title: %s", wExtPrivate.Body.String())
	}

	// Extension token can read a non-hidden todo's trace.
	wExtPublic := getCausalTrace(s, "todo_public", map[string]string{RoleHeader: roleExtension})
	if wExtPublic.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for extension on public todo, got %d: %s", wExtPublic.Code, wExtPublic.Body.String())
	}
}

func TestTodosV2CausalTrace_TimelineUnaffected(t *testing.T) {
	s, _, _ := newCausalTraceTestEnv(t, true)
	now := time.Now().UTC()
	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		"todo_tl": {
			ID:        "todo_tl",
			Title:     "Timeline compatibility check",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	s.todos.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/todos/todo_tl/timeline", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected /timeline to return 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var tl assistantwire.TodoTimeline
	if err := json.Unmarshal(w.Body.Bytes(), &tl); err != nil {
		t.Fatalf("unmarshal timeline: %v", err)
	}
	if tl.Rounds == nil {
		t.Fatalf("expected non-nil rounds slice in /timeline response")
	}
}
