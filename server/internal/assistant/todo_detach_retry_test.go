package assistant

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTodoDetachRetiresTransientRetry(t *testing.T) {
	t.Run("memory", func(t *testing.T) { checkTodoDetachRetiresTransientRetry(t, &workMemStore{}) })
	t.Run("postgres", func(t *testing.T) { checkTodoDetachRetiresTransientRetry(t, todoTestDB(t)) })
}

func checkTodoDetachRetiresTransientRetry(t *testing.T, st Store) {
	t.Helper()
	ctx := t.Context()
	e := newTodoEnv(t, st)
	failed := recentTodoRetryFailure()
	e.f.setSessions("box", failed)
	url := e.todoAgentURL("Retry on another computer")
	triage(t, e.s, failed.SessionID)
	pending := entryOf(t, e.s, failed.SessionID)
	if pending.Work.On != proto.WaitTransient || pending.WakeAt == nil {
		t.Fatalf("setup: failed turn did not schedule a retry: %+v", pending)
	}

	if w := e.doExt("scheduler", http.MethodDelete, url, ""); w.Code != http.StatusOK {
		t.Fatalf("extension detach: %d %s", w.Code, w.Body)
	}
	assertTodoRetryRetired(t, e.s, failed.SessionID)

	due := pending.WakeAt.Add(time.Second)
	e.s.workTurnSettled(ctx, "box", failed.SessionID)
	e.s.workTick(ctx, due)
	assertTodoRetryRetired(t, e.s, failed.SessionID)
	e.s.waitWorkEvents()

	restarted := newServiceOn(t, e.f, st)
	restarted.workTick(ctx, due)
	restarted.workStartedObserved(ctx, "box", failed.SessionID, due, false)
	restarted.workTurnSettled(ctx, "box", failed.SessionID)
	restarted.workTick(ctx, due.Add(30*time.Minute))
	assertTodoRetryRetired(t, restarted, failed.SessionID)
	if sent := e.f.sentToAgents(); len(sent) != 0 {
		t.Fatalf("the detached turn sent messages: %v", sent)
	}

	restarted.workStarted(ctx, "box", failed.SessionID, due)
	failed.UpdatedAt = failed.UpdatedAt.Add(5 * time.Second)
	e.f.setSessions("box", failed)
	restarted.workTurnSettled(ctx, "box", failed.SessionID)
	next := entryOf(t, restarted, failed.SessionID)
	if next.WakeAt == nil || next.Work.RetiredAt != nil {
		t.Fatalf("new turn did not schedule a retry: %+v", next)
	}
	if w := serve(restarted, http.MethodDelete, url, ""); w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		t.Fatalf("replayed detach: %d %s", w.Code, w.Body)
	}
	restarted.workTick(ctx, next.WakeAt.Add(time.Second))
	assertTodoRetrySent(t, e.f)
	defer restarted.waitWorkEvents()
}

func TestTodoDetachKeepsRetryOnAnotherOpenTodo(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.f.setSessions("box", recentTodoRetryFailure())
	first := e.todoAgentURL("First attempt")
	second := e.todoAgentURL("Active follow-up")
	triage(t, e.s, "s1")
	pending := entryOf(t, e.s, "s1")
	if pending.WakeAt == nil {
		t.Fatal("setup: no pending retry")
	}

	e.must(http.MethodDelete, first, "")
	if got := entryOf(t, e.s, "s1"); got.WakeAt == nil || !got.WakeAt.Equal(*pending.WakeAt) || got.Work.RetiredAt != nil {
		t.Fatalf("detaching the historical link changed the active retry: %+v", got)
	}
	e.s.workTick(t.Context(), pending.WakeAt.Add(time.Second))
	assertTodoRetrySent(t, e.f)
	if w := e.do(http.MethodDelete, second, ""); w.Code != http.StatusOK {
		t.Fatalf("detach active link: %d %s", w.Code, w.Body)
	}
	assertTodoRetryRetired(t, e.s, "s1")
}

func TestTodoDetachWithdrawsLegacyRetryCard(t *testing.T) {
	st := &workMemStore{}
	s, f := retryDecisionFixture(t, st)
	e := &todoEnv{t: t, s: s, f: f}
	url := e.todoAgentURL("Replace failed attempt")
	card := s.workCards()[0].ID
	before := f.sentToAgents()
	e.must(http.MethodDelete, url, "")
	assertTodoRetryRetired(t, s, "s1")
	if handled, err := s.workAnswer(t.Context(), card, proto.Resolution{}); !handled || err == nil {
		t.Fatalf("withdrawn card still accepts an answer: handled %v, error %v", handled, err)
	}
	s.waitWorkEvents()

	restarted := newServiceOn(t, f, st)
	restarted.workTick(t.Context(), time.Now().Add(30*time.Minute))
	restarted.workTurnSettled(t.Context(), "box", "s1")
	assertTodoRetryRetired(t, restarted, "s1")
	if sent := f.sentToAgents(); !slices.Equal(sent, before) {
		t.Fatalf("the detached turn sent more messages: before %v, after %v", before, sent)
	}
}

func TestTodoDetachKeepsStandaloneTransientRetry(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.f.setSessions("box", recentTodoRetryFailure())
	id := e.create("Unrelated task").ID
	triage(t, e.s, "s1")
	pending := entryOf(t, e.s, "s1")
	if pending.WakeAt == nil {
		t.Fatal("setup: standalone retry not scheduled")
	}
	if w := e.do(http.MethodDelete, "/api/v1/todos/"+id+"/agents/box/s1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("detach a session that was never linked: %d %s", w.Code, w.Body)
	}
	e.s.workTick(t.Context(), pending.WakeAt.Add(time.Second))
	assertTodoRetrySent(t, e.f)
}

func TestTodoDetachDuringTransientTriage(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.f.setSessions("box", recentTodoRetryFailure())
	url := e.todoAgentURL("Replace before triage settles")
	e.f.mu.Lock()
	e.f.beforeSessionGet = func() { e.must(http.MethodDelete, url, "") }
	e.f.mu.Unlock()
	triage(t, e.s, "s1")
	assertTodoRetryRetired(t, e.s, "s1")
	e.s.workTick(t.Context(), time.Now().Add(time.Minute))
	if sent := e.f.sentToAgents(); len(sent) != 0 {
		t.Fatalf("in-flight triage armed a retry after detachment: %v", sent)
	}
}

func TestTodoDetachDuringDueRetry(t *testing.T) {
	e := newTodoEnv(t, nil)
	failed := recentTodoRetryFailure()
	e.f.setSessions("box", failed)
	url := e.todoAgentURL("Replace before the retry is sent")
	triage(t, e.s, failed.SessionID)
	pending := entryOf(t, e.s, failed.SessionID)
	if pending.WakeAt == nil {
		t.Fatal("setup: no pending retry")
	}
	e.f.mu.Lock()
	e.f.onDevicesList = func() {
		e.must(http.MethodDelete, url, "")
		e.f.setSessions("box")
	}
	e.f.mu.Unlock()
	e.s.workTick(t.Context(), pending.WakeAt.Add(time.Second))
	e.f.setSessions("box", failed)
	e.s.workTurnSettled(t.Context(), "box", failed.SessionID)
	assertTodoRetryRetired(t, e.s, failed.SessionID)
	if sent := e.f.sentToAgents(); len(sent) != 0 {
		t.Fatalf("in-flight retry sent after detachment: %v", sent)
	}
}

func TestTodoDetachKeepsAnUnrelatedWorkCard(t *testing.T) {
	s, f := workService(t)
	e := &todoEnv{t: t, s: s, f: f}
	failed := recentTodoRetryFailure()
	failed.TurnError = "Failed to authenticate. API Error: 401 invalid credentials"
	f.setSessions("box", failed)
	url := e.todoAgentURL("An ordinary decision")
	triage(t, s, failed.SessionID)
	cards := s.workCards()
	if len(cards) != 1 {
		t.Fatalf("setup: expected an ordinary decision card, got %+v", cards)
	}
	e.must(http.MethodDelete, url, "")
	if got := s.workCards(); len(got) != 1 || got[0].ID != cards[0].ID {
		t.Fatalf("detachment withdrew an unrelated card: %+v", got)
	}
}

func (e *todoEnv) todoAgentURL(title string) string {
	e.t.Helper()
	url := "/api/v1/todos/" + e.create(title).ID + "/agents/box/s1"
	e.must(http.MethodPut, url, "")
	return url
}

func recentTodoRetryFailure() proto.Session {
	failed := apiErrorTurn("s1", recoveryFirstError)
	failed.UpdatedAt = time.Now().UTC().Add(-time.Minute)
	return failed
}

func assertTodoRetryRetired(t *testing.T, s *Service, sid string) {
	t.Helper()
	got := entryOf(t, s, sid)
	if got.Work.RetiredAt == nil || got.WakeAt != nil || got.Work.WakeAt != nil || got.InteractionID != "" || got.Work.InteractionID != "" {
		t.Fatalf("detached retry left active work: %+v", got)
	}
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("detached retry still has a card: %+v", cards)
	}
	if w, shown := reportOf(t, s, sid); shown {
		t.Fatalf("detached retry still appears in the work report: %+v", w)
	}
}

func assertTodoRetrySent(t *testing.T, f *fakeHub) {
	t.Helper()
	if sent := f.sentToAgents(); !slices.Equal(sent, []string{"s1: " + autoContinueTextForTest(continueText)}) {
		t.Fatalf("want one automatic continuation, got %v", sent)
	}
}
