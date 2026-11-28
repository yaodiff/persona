package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// shortAgentStartTimeout makes the start wait and adoption pause expire in milliseconds, not the production bounds.
func shortAgentStartTimeout(t *testing.T) { agentStartTimeoutOf(t, 30*time.Millisecond) }

// agentStartTimeoutOf sets the start wait for one test. A test whose start records its task in Postgres takes a
// longer one: the task record shares the wait, and on a busy machine 30 ms runs out before the slow start does.
func agentStartTimeoutOf(t *testing.T, d time.Duration) {
	t.Helper()
	oldTimeout, oldRetry := agentStartTimeout, agentStartAdoptRetry
	agentStartTimeout, agentStartAdoptRetry = d, 5*time.Millisecond
	t.Cleanup(func() { agentStartTimeout, agentStartAdoptRetry = oldTimeout, oldRetry })
}

// The node built the session and launched the agent but its answer never came back: not a failure, and the model
// must not start a second copy. The start itself is attempted exactly once.
func TestAgentStartSlowAnswerIsUnknownNotFailure(t *testing.T) {
	s, f := newTaskService(t)
	shortAgentStartTimeout(t)
	f.slowStart = true
	res, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil {
		t.Fatalf("unknown start result must not be an error: %v", err)
	}
	if !strings.Contains(res, "may already be starting") || !strings.Contains(res, "Do NOT start it again") ||
		!strings.Contains(res, "Agents tab") {
		t.Fatalf("neutral text missing guidance:\n%s", res)
	}
	if len(f.starts) != 1 {
		t.Fatalf("the start was attempted %d times, want 1 (no automatic retry)", len(f.starts))
	}
}

// A late node whose session list shows the new session has the start confirmed and linked like an answered one.
func TestAgentStartAdoptsTheSessionThatStartedLate(t *testing.T) {
	s, f := newQuietService(t)
	shortAgentStartTimeout(t)
	f.slowStart = true
	f.onStart = func(f *fakeHub, a map[string]any) { // the node adds the session (with its launcher) while handling the late start
		f.sessions["box"] = []proto.Session{{SessionID: "s9", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Launcher: a["launcher"].(string), StartedByPersona: true, Status: proto.StatusWorking,
			State: proto.StateWorking, UpdatedAt: time.Now().UTC()}}
	}
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil {
		t.Fatalf("adopting the started session must not error: %v", err)
	}
	if !strings.Contains(res, "s9") || !strings.Contains(res, "confirmed from the machine's own list") {
		t.Fatalf("result does not confirm the adopted session:\n%s", res)
	}
	if !strings.Contains(res, "a task of this conversation") {
		t.Fatalf("an adopted quiet task must be described like a normal quiet start:\n%s", res)
	}
	todo := onlyTodo(s)
	if len(todo.Agents) != 1 || todo.Agents[0].SessionID != "s9" {
		t.Fatalf("adopted session not linked as a quiet task: %+v", todo)
	}
}

// The bounded adoption heuristic is also a confirmed identity: an ordinary task-link failure must not turn it back
// into an unknown start, hide the session id, or arm start-unknown for an Agent the hub has already found (#5732).
func TestAgentStartAdoptedSessionLinkFailureKeepsConfirmedResult(t *testing.T) {
	st := newCtxAwareLinkStore()
	st.failSession = "s-adopt-1"
	s, f := newQuietServiceOn(t, st)
	shortAgentStartTimeout(t)
	f.slowStart = true
	f.onStart = func(f *fakeHub, a map[string]any) {
		launcher, _ := a["launcher"].(string)
		f.sessions["box"] = []proto.Session{{SessionID: "s-adopt-1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Launcher: launcher, StartedByPersona: true, Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now().UTC()}}
	}
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"check CI"}`))
	if err != nil {
		t.Fatalf("the adopted Agent started; its link failure is a warning, not a start failure: %v", err)
	}
	if !strings.Contains(res, "s-adopt-1") || !strings.Contains(res, "confirmed from the machine's own list") ||
		!strings.Contains(res, "task link could not be recorded") || !strings.Contains(res, "Do NOT start it again") ||
		strings.Contains(res, "result is unknown") {
		t.Fatalf("adopted-session link failure answer = %q", res)
	}
	if st.linkCount() != 1 {
		t.Fatalf("link attempts = %d, want only the adopted session", st.linkCount())
	}
	if p := onlyStoredPlacement(t, st); p.TaskID != onlyTodo(s).ID || p.Choice != "box" || p.SessionID != "s-adopt-1" {
		t.Fatalf("placement lost the adopted session: %+v", p)
	}
	requireStartUnknownState(t, s, onlyTodo(s).ID, false)
}

// Once a creation id identifies the started session, a link failure cannot fall back to a heuristic and bind another
// Agent. The answer keeps that exact session's identity and warns the caller not to start a duplicate (#5732).
func TestAgentStartExactCreationIDLinkFailureDoesNotAdoptAnotherSession(t *testing.T) {
	st := newCtxAwareLinkStore()
	st.failSession = "s-exact-1"
	s, f := newQuietServiceOn(t, st)
	shortAgentStartTimeout(t)
	f.slowStart = true
	f.onStart = func(f *fakeHub, a map[string]any) {
		id, _ := a["creation_id"].(string)
		launcher, _ := a["launcher"].(string)
		now := time.Now().UTC()
		f.sessions["box"] = []proto.Session{
			{SessionID: "s-exact-1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Launcher: launcher,
				Hierarchy: &proto.SessionHierarchy{CreationID: id}, StartedByPersona: true, Status: proto.StatusWorking, UpdatedAt: now},
			{SessionID: "s-other-1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Launcher: launcher,
				StartedByPersona: true, Status: proto.StatusWorking, UpdatedAt: now.Add(time.Second)},
		}
	}
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"check CI"}`))
	if err != nil {
		t.Fatalf("the Agent started; its link failure is a warning, not a start failure: %v", err)
	}
	if !strings.Contains(res, "s-exact-1") || !strings.Contains(res, "task link could not be recorded") ||
		!strings.Contains(res, "Do NOT start it again") || strings.Contains(res, "s-other-1") {
		t.Fatalf("exact-session link failure answer = %q", res)
	}
	todo := onlyTodo(s)
	if p := onlyStoredPlacement(t, st); p.TaskID != todo.ID || p.Choice != "box" || p.SessionID != "s-exact-1" {
		t.Fatalf("placement lost the session confirmed by creation ID: %+v", p)
	}
	if len(todo.Agents) != 0 {
		t.Fatalf("another session was linked after the exact link failed: %+v", todo.Agents)
	}
	if st.linkCount() != 1 {
		t.Fatalf("link attempts = %d, want only the exact session", st.linkCount())
	}
}

// The user can close the pre-created card while session.start is in flight. The node has still launched the Agent:
// return started-with-warning, keep it linked to the closed card for cleanup, and never invite a duplicate start.
func TestAgentStartReportsStartedWhenTaskClosesDuringLaunch(t *testing.T) {
	s, f := newQuietService(t)
	f.onStart = func(f *fakeHub, a map[string]any) {
		id, _ := a["creation_id"].(string)
		now := time.Now().UTC()
		s.todos.mu.Lock()
		if todo := s.todos.todos[id]; todo != nil {
			todo.DoneAt = &now
		}
		s.todos.mu.Unlock()
	}
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil {
		t.Fatalf("a successfully started Agent must not read as a start failure: %v", err)
	}
	if !strings.Contains(res, "Started on ") || !strings.Contains(res, "Warning") || !strings.Contains(res, "Do NOT start it again") {
		t.Fatalf("started-with-warning result = %q", res)
	}
	if len(f.starts) != 1 {
		t.Fatalf("the start was attempted %d times, want 1", len(f.starts))
	}
	todo := onlyTodo(s)
	if todo.DoneAt == nil || len(todo.Agents) != 1 {
		t.Fatalf("the started Agent is not linked to the closed card for cleanup: %+v", todo)
	}
}

func requireStartUnknownState(t *testing.T, s *Service, todoID string, want bool) {
	t.Helper()
	key := "start-unknown@" + todoID
	s.supervise.mu.Lock()
	pending := s.supervise.pending[todoID+"|"+sigUnjudged]
	s.supervise.mu.Unlock()
	durable := storedSupervisePending(t, s)[todoID+"|"+sigUnjudged]
	s.tasks.turns.mu.Lock()
	var ledger *turnSignal
	if sig := s.tasks.turns.doc.Turns[todoID].Signal; sig != nil {
		copy := *sig
		ledger = &copy
	}
	s.tasks.turns.mu.Unlock()
	if want {
		if pending.Key != key || durable.Key != key {
			t.Fatalf("start-unknown is not pending everywhere: memory=%+v durable=%+v", pending, durable)
		}
		return
	}
	if pending.Key == key || durable.Key == key {
		t.Fatalf("start-unknown survived confirmation: memory=%+v durable=%+v", pending, durable)
	}
	if ledger != nil && ledger.Kind == sigUnjudged && ledger.Key == key && ledger.Told == nil {
		t.Fatalf("start-unknown can still replay from the turn ledger: %+v", ledger)
	}
}

// Exact creation-id confirmation can arrive after the acknowledged outbox was cleared but before the first
// post-restart ledger load. The ledger-only signal must be told without being gathered into a new outbox (#5732).
func TestExactCreationConfirmationRetractsLedgerOnlyStartUnknownAfterRestart(t *testing.T) {
	ctx := context.Background()
	s, _ := newQuietServiceOn(t, &memStore{})
	todo, err := s.createTodo(ctx, "Ledger-only start confirmation", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"confirm the exact start"}, 30)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "provisional start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	s.signal(g)
	s.ackSupervisePending([]superviseSignal{g}) // queue admission acknowledged the outbox before the crash
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != "" {
		t.Fatalf("the acknowledged outbox fixture still contains the signal: %+v", got)
	}

	simulateSupervisionRestart(t, s)
	turns := &s.tasks.turns
	turns.mu.Lock()
	turns.loaded, turns.doc = false, taskTurnsDoc{}
	turns.mu.Unlock()
	session := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusDone,
		State: proto.StateUnread, UpdatedAt: time.Now()}
	s.autoLinkTodos("box", []proto.Session{session})

	// Read the loaded memory without calling turnOf: watchTurn itself must perform the first post-restart ledger load.
	turns.mu.Lock()
	var confirmed *turnSignal
	if sig := turns.doc.Turns[todo.ID].Signal; sig != nil {
		copy := *sig
		confirmed = &copy
	}
	turns.mu.Unlock()
	s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{session})
	if confirmed == nil || confirmed.Told == nil {
		t.Errorf("start-unknown survived exact creation-id confirmation in the turn ledger: %+v", confirmed)
	}
	if got := storedSupervisePending(t, s)[g.id()]; got.Key == g.Key {
		t.Error("the confirmed start-unknown replayed into the durable supervision outbox")
	}
	entry, ok := s.turnOf(ctx, todo.ID)
	if !ok || entry.Signal == nil || entry.Signal.Told == nil {
		t.Errorf("the exact signal's told mark was not persisted: ok=%v entry=%+v", ok, entry)
	}
}

// A manually linked or workspace-linked session is not evidence that the prepared start got its own Agent. Keep the
// unknown-start warning until the current list contains the exact creation id; then retract every durable form.
func TestWatchTurnKeepsStartUnknownUntilExactCreationConfirmation(t *testing.T) {
	oldRecoverMost := turnRecoverMost
	turnRecoverMost = 0 // no transcript recovery: this control exercises only the confirmation gate
	t.Cleanup(func() { turnRecoverMost = oldRecoverMost })
	ctx := context.Background()
	s, _ := newQuietServiceOn(t, &memStore{})
	todo, err := s.createTodo(ctx, "Wait for the exact start", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"confirm the exact start"}, 30)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "provisional start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	s.signal(g)
	manual := proto.Session{SessionID: "manual", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		StartedByPersona: true, Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: time.Now()}
	if err := s.linkTodoAgent(ctx, todo.ID, manual.MachineID, manual.SessionID, manual.Title, manual.Cwd, manual.Agent); err != nil {
		t.Fatal(err)
	}
	s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{manual})
	requireStartUnknownState(t, s, todo.ID, true)

	exact := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusDone,
		State: proto.StateUnread, UpdatedAt: manual.UpdatedAt.Add(time.Second)}
	s.autoLinkTodos("box", []proto.Session{exact})
	s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{manual, exact})
	requireStartUnknownState(t, s, todo.ID, false)
}

func TestAgentStartConfirmationRetriesIncompleteLedgerRetraction(t *testing.T) {
	for _, mode := range []string{"read", "write"} {
		t.Run(mode, func(t *testing.T) {
			st := &turnsFailureStore{Store: &memStore{}}
			s, _ := newQuietServiceOn(t, st)
			ctx := context.Background()
			todo, err := s.createTodo(ctx, "Retry incomplete start retraction", "", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"confirm the exact start"}, 30)
			g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "provisional start", At: time.Now()}
			markLeaderTurnSignalled(t, s, g)
			s.signal(g)
			simulateSupervisionRestart(t, s)
			turns := &s.tasks.turns
			turns.mu.Lock()
			turns.loaded, turns.doc = false, taskTurnsDoc{}
			turns.mu.Unlock()
			if mode == "read" {
				st.readFailure.Store(true)
			} else {
				st.writeFailure.Store(true)
			}
			session := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
				Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusDone,
				State: proto.StateUnread, UpdatedAt: time.Now()}
			s.autoLinkTodos("box", []proto.Session{session})
			if got := todoNow(s, todo.ID); len(got.Agents) != 1 {
				t.Fatalf("the exact session link was not retained: %+v", got.Agents)
			}
			if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
				t.Errorf("an incomplete %s-failure retraction discarded the durable signal: %+v", mode, got)
			}

			st.readFailure.Store(false)
			st.writeFailure.Store(false)
			simulateSupervisionRestart(t, s)
			turns.mu.Lock()
			turns.loaded, turns.doc = false, taskTurnsDoc{}
			turns.mu.Unlock()
			s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{session})
			if got := storedSupervisePending(t, s)[g.id()]; got.Key == g.Key {
				t.Errorf("the confirmed start-unknown replayed after the %s failure recovered", mode)
			}
			entry, ok := s.turnOf(ctx, todo.ID)
			if !ok || entry.Signal == nil || entry.Signal.Told == nil {
				t.Errorf("the recovered %s-failure retraction did not persist told: ok=%v entry=%+v", mode, ok, entry)
			}
		})
	}
}

func TestAgentStartConfirmationCleansOutboxWhenToldPredatesRestart(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Told before the cleanup crash", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"confirm the exact start"}, 30)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "provisional start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	s.signal(g)
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", todo.Title, "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	if !s.turnsToldDurably(ctx, []superviseSignal{g}) {
		t.Fatal("the crash-boundary fixture did not persist told")
	}
	// The process stopped after the ledger receipt but before the outbox cleanup. The exact link remains confirmed.
	simulateSupervisionRestart(t, s)
	turns := &s.tasks.turns
	turns.mu.Lock()
	turns.loaded, turns.doc = false, taskTurnsDoc{}
	turns.mu.Unlock()
	session := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, UpdatedAt: time.Now()}
	s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{session})
	if got := storedSupervisePending(t, s)[g.id()]; got.Key == g.Key {
		t.Fatal("the already-told confirmation left a deliverable outbox entry after restart")
	}
}

// The production session-list entry retracts a confirmed start before it drains an idle supervisor's queue. A crash may
// leave the told receipt and the queued notice behind together; a later ledger failure must hold the drain for a retry,
// not let the stale start result escape first (#5732).
func TestSessionsChangedRetractsToldStartUnknownBeforeRelayDrain(t *testing.T) {
	st := &turnsFailureStore{Store: &memStore{}}
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Retract before the production relay drain", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: "stale unknown start " + todo.ID, At: time.Now()},
		[]superviseSignal{g}, time.Now(), time.Time{}) {
		t.Fatal("the unknown start was not admitted to the durable relay queue")
	}
	if !s.turnsToldDurably(ctx, []superviseSignal{g}) {
		t.Fatal("the crash-boundary fixture did not persist told")
	}
	simulateSupervisionRestart(t, s)
	turns := &s.tasks.turns
	turns.mu.Lock()
	turns.loaded, turns.doc = false, taskTurnsDoc{}
	turns.mu.Unlock()
	idle := proto.Session{SessionID: "conv1", MachineID: "box", Assistant: true, Status: proto.StatusIdle}
	confirmed := proto.Session{SessionID: "s-confirmed", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusDone, UpdatedAt: time.Now()}
	d := DeviceSessions{MachineID: "box", Sessions: []proto.Session{idle, confirmed}}

	st.readFailure.Store(true)
	s.SessionsChanged(ctx, d)
	st.readFailure.Store(false)
	s.SessionsChanged(ctx, d)
	waitFor(t, "the confirmed queue head to be withdrawn", func() bool { return s.queuedFor("conv1") == 0 })
	if text := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(text, "stale unknown start") {
		t.Fatalf("SessionsChanged drained the confirmed start-unknown before retracting it: %s", text)
	}
}

// A failed first relay read cannot be treated as an empty queue: the drain's second read may succeed and expose the
// stale head. The session-list entry must hold that drain until both relay and ledger reads succeed (#5732).
func TestSessionsChangedDoesNotDrainAfterARelayReadFailure(t *testing.T) {
	turns := &turnsFailureStore{Store: &memStore{}}
	st := &relayFirstReadFailureStore{turnsFailureStore: turns}
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Hold the drain after a relay read failure", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: "stale unknown start " + todo.ID, At: time.Now()},
		[]superviseSignal{g}, time.Now(), time.Time{}) {
		t.Fatal("the unknown start was not admitted to the durable relay queue")
	}
	if !s.turnsToldDurably(ctx, []superviseSignal{g}) {
		t.Fatal("the crash-boundary fixture did not persist told")
	}
	simulateSupervisionRestart(t, s)
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded, s.tasks.turns.doc = false, taskTurnsDoc{}
	s.tasks.turns.mu.Unlock()
	idle := proto.Session{SessionID: "conv1", MachineID: "box", Assistant: true, Status: proto.StatusIdle}
	confirmed := proto.Session{SessionID: "s-confirmed", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusDone, UpdatedAt: time.Now()}
	d := DeviceSessions{MachineID: "box", Sessions: []proto.Session{idle, confirmed}}
	hub := &stalledAssistantSendHub{fakeHub: f, started: make(chan struct{}), release: make(chan struct{}), attempts: make(chan string, 2)}
	setHub(s, hub)

	turns.readFailure.Store(true)
	st.failRelayRead.Store(true)
	s.relays.mu.Lock()
	s.relays.loaded = false
	s.relays.mu.Unlock()
	s.SessionsChanged(ctx, d)
	select {
	case <-hub.started:
		t.Error("the drain started after the first relay read failed")
	case <-time.After(100 * time.Millisecond):
	}
	close(hub.release)
	turns.readFailure.Store(false)
	s.SessionsChanged(ctx, d)
	waitFor(t, "the confirmed queue head to be withdrawn", func() bool { return s.queuedFor("conv1") == 0 })
	if text := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(text, "stale unknown start") {
		t.Fatalf("the drain's second relay read delivered the confirmed start-unknown: %s", text)
	}
}

// An empty relay queue does not make a told outbox entry harmless: the next digest restores it before watchTurn runs.
// Reconcile that durable form at the session-list boundary too, even without a current creation-id row (#5732).
func TestSessionsChangedRetractsToldOutboxBeforeDigest(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Retract the outbox before its digest", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	s.signal(g)
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
		t.Fatalf("the outbox fixture was not persisted: %+v", got)
	}
	if !s.turnsToldDurably(ctx, []superviseSignal{g}) {
		t.Fatal("the crash-boundary fixture did not persist told")
	}
	simulateSupervisionRestart(t, s)
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded, s.tasks.turns.doc = false, taskTurnsDoc{}
	s.tasks.turns.mu.Unlock()
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}

	s.SessionsChanged(ctx, DeviceSessions{MachineID: "server", Sessions: []proto.Session{idle}})
	s.supervise.mu.Lock()
	pending := s.supervise.pending[g.id()]
	s.supervise.mu.Unlock()
	if pending.Key == g.Key {
		t.Errorf("the confirmed signal remained in memory after session-list reconciliation: %+v", pending)
	}
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(ctx)
	s.drainRelays("server", []proto.Session{idle})
	if got := storedSupervisePending(t, s)[g.id()]; got.Key == g.Key {
		t.Error("the confirmed outbox entry survived the session-list reconciliation")
	}
	if text := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(text, "stale unknown start") {
		t.Fatalf("the empty-queue path let the digest deliver the confirmed start-unknown: %s", text)
	}
	for _, msg := range queuedRelays(s, "conv1") {
		if strings.Contains(msg.Text, "stale unknown start") {
			t.Fatalf("the empty-queue path let the digest queue the confirmed start-unknown: %+v", msg)
		}
	}
}

// Startup recovery reaches the same outbox restoration before any session list arrives. A told receipt from the old
// process must therefore be reconciled by restoration itself, not only by SessionsChanged (#5732).
func TestRecoverTaskStartsRetractsToldOutboxBeforeDigest(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Retract the outbox during startup recovery", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	s.signal(g)
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
		t.Fatalf("the outbox fixture was not persisted: %+v", got)
	}
	if !s.turnsToldDurably(ctx, []superviseSignal{g}) {
		t.Fatal("the crash-boundary fixture did not persist told")
	}
	simulateSupervisionRestart(t, s)
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded, s.tasks.turns.doc = false, taskTurnsDoc{}
	s.tasks.turns.mu.Unlock()
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()

	// Service.Run calls this before superviseLoop and before the first SessionsChanged.
	if err := s.recoverTaskStarts(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.supervise.mu.Lock()
	pending := s.supervise.pending[g.id()]
	s.supervise.mu.Unlock()
	if pending.Key == g.Key {
		t.Errorf("startup restored the already-told signal into memory: %+v", pending)
	}
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(ctx)
	if got := storedSupervisePending(t, s)[g.id()]; got.Key == g.Key {
		t.Error("startup recovery left the confirmed outbox entry deliverable")
	}
	if text := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(text, "stale unknown start") {
		t.Fatalf("startup recovery delivered the confirmed start-unknown: %s", text)
	}
	for _, msg := range queuedRelays(s, "conv1") {
		if strings.Contains(msg.Text, "stale unknown start") {
			t.Fatalf("startup recovery queued the confirmed start-unknown: %+v", msg)
		}
	}
}

// turnsFailureStore injects a task-ledger failure only after the fixture has been persisted.
type turnsFailureStore struct {
	Store
	readFailure, writeFailure atomic.Bool
}

func (s *turnsFailureStore) Setting(ctx context.Context, key string, v any) (bool, error) {
	if key == taskTurnsKey && s.readFailure.Load() {
		return false, errors.New("injected task-ledger read failure")
	}
	return s.Store.Setting(ctx, key, v)
}

func (s *turnsFailureStore) PutSetting(ctx context.Context, key string, v any) error {
	if key == taskTurnsKey && s.writeFailure.Load() {
		return errors.New("injected task-ledger write failure")
	}
	return s.Store.PutSetting(ctx, key, v)
}

// relayFirstReadFailureStore fails only the next relay-state read, leaving the drain's immediate second read healthy.
type relayFirstReadFailureStore struct {
	*turnsFailureStore
	failRelayRead atomic.Bool
}

func (s *relayFirstReadFailureStore) Setting(ctx context.Context, key string, v any) (bool, error) {
	if key == relayKey && s.failRelayRead.CompareAndSwap(true, false) {
		return false, errors.New("injected first relay read failure")
	}
	return s.turnsFailureStore.Setting(ctx, key, v)
}

func TestDelayedCreationRecognitionWithoutUnknownDoesNotWrite(t *testing.T) {
	st := &relayWriteWatch{Store: &memStore{}}
	s, _ := newQuietServiceOn(t, st)
	todo, err := s.createTodo(context.Background(), "Already linked creation", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.todos.mu.Lock()
	stored := s.todos.todos[todo.ID]
	stored.DoneAt = &now
	stored.Agents = []TodoAgent{{MachineID: "box", SessionID: "s-known", LinkedAt: now.Add(-time.Hour)}}
	s.todos.activeBySession[[2]string{"box", "s-known"}] = todo.ID
	s.todos.mu.Unlock()
	session := proto.Session{SessionID: "s-known", MachineID: "box", Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}}
	before := len(st.writes)
	for range 2 {
		handled, linked := s.linkDelayedDoneCreation("box", session, todo.ID, "")
		if !handled || linked {
			t.Fatalf("handled=%v linked=%v, want an existing recognized link", handled, linked)
		}
	}
	if got := len(st.writes); got != before {
		t.Fatalf("recognition without start-unknown wrote relay %d time(s), want zero", got-before)
	}
}

// stalledAssistantSendHub blocks a relay send without fakeHub's mutex held, so the test can link the confirmed session
// while that send is genuinely in flight.
type stalledAssistantSendHub struct {
	*fakeHub
	started  chan struct{}
	release  chan struct{}
	attempts chan string
	calls    atomic.Int32
	succeed  bool
	inDoubt  bool
}

func (h *stalledAssistantSendHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method != DeviceAssistantSend {
		return h.fakeHub.DeviceCall(ctx, p)
	}
	var params map[string]any
	_ = json.Unmarshal(p.Params, &params)
	text, _ := params["text"].(string)
	h.attempts <- text
	if h.calls.Add(1) == 1 {
		close(h.started)
		<-h.release
	}
	if h.succeed {
		return json.RawMessage(`{}`), nil
	}
	if h.inDoubt {
		return nil, context.DeadlineExceeded
	}
	return nil, errors.New("send failed")
}

// An unknown start result can be confirmed from either the node's creation identity or its bounded session adoption.
// If the card closed during the same launch, both paths retain the Agent on that done card and report the confirmed session.
func TestAgentStartUnknownReportsStartedWhenTaskClosesDuringLaunch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		creationID bool
		want       string
	}{
		{"creation id", true, "confirmed by its creation id"},
		{"adopted session", false, "confirmed from the machine's own list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietServiceOn(t, &memStore{})
			shortAgentStartTimeout(t)
			f.slowStart = true
			f.onStart = func(f *fakeHub, a map[string]any) {
				id, _ := a["creation_id"].(string)
				now := time.Now().UTC()
				s.todos.mu.Lock()
				if todo := s.todos.todos[id]; todo != nil {
					todo.DoneAt = &now
				}
				s.todos.mu.Unlock()
				// fakeHub invokes onStart with its mutex held. The production batch scheduler reads settings through Hello,
				// so let that nested hub call take the mutex while the signal is still raised synchronously during this start.
				f.mu.Unlock()
				s.signal(superviseSignal{Todo: id, Kind: sigUnjudged, Key: "start-unknown@" + id, Detail: "earlier unknown start"})
				f.mu.Lock()
				sess := proto.Session{SessionID: "s-late", MachineID: "box", Cwd: "/code/app", Agent: "claude",
					Launcher: a["launcher"].(string), StartedByPersona: true, Status: proto.StatusWorking,
					State: proto.StateWorking, UpdatedAt: now}
				if tc.creationID {
					sess.Hierarchy = &proto.SessionHierarchy{CreationID: id}
				}
				f.sessions["box"] = []proto.Session{sess}
			}
			res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
				json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
			if err != nil {
				t.Fatalf("a confirmed Agent must not read as a start failure: %v", err)
			}
			if !strings.Contains(res, "Started on ") || !strings.Contains(res, tc.want) ||
				!strings.Contains(res, "task closed while the Agent started") || !strings.Contains(res, "Do NOT start it again") {
				t.Fatalf("confirmed started-with-warning result = %q", res)
			}
			if len(f.starts) != 1 {
				t.Fatalf("the start was attempted %d times, want 1", len(f.starts))
			}
			todo := onlyTodo(s)
			if todo.DoneAt == nil || len(todo.Agents) != 1 || todo.Agents[0].SessionID != "s-late" {
				t.Fatalf("the confirmed Agent is not linked to the closed card for cleanup: %+v", todo)
			}
			requireStartUnknownState(t, s, todo.ID, false)
		})
	}
}

// An unanswered start whose prepared card closes in flight must not arm a new supervision stream or claim the card is
// open. A later exact creation-id confirmation still links the Agent to the done card for cleanup.
func TestAgentStartUnknownDoesNotRearmTaskClosedDuringLaunch(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	shortAgentStartTimeout(t)
	f.slowStart = true
	f.onStart = func(f *fakeHub, a map[string]any) {
		id, _ := a["creation_id"].(string)
		now := time.Now().UTC()
		s.todos.mu.Lock()
		if todo := s.todos.todos[id]; todo != nil {
			todo.DoneAt = &now
		}
		s.todos.mu.Unlock()
	}
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res, "stays open") || !strings.Contains(res, "closed while the start was in flight") ||
		!strings.Contains(res, "Do NOT start it again") {
		t.Fatalf("unknown result after the owner closed the card = %q", res)
	}
	todo := onlyTodo(s)
	if todo.DoneAt == nil {
		t.Fatal("the prepared card did not close")
	}
	requireStartUnknownState(t, s, todo.ID, false)

	s.autoLinkTodos("box", []proto.Session{{SessionID: "s-late", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusWorking, UpdatedAt: time.Now()}})
	got := todoNow(s, todo.ID)
	if got.DoneAt == nil || len(got.Agents) != 1 || got.Agents[0].SessionID != "s-late" {
		t.Fatalf("the delayed session is not linked to the closed card for cleanup: %+v", got)
	}
}

// An unanswered start still has its task from before the request. When the session appears later, its persisted
// creation id links it back to that same card without guessing from cwd or adopting a manual session (#5732).
func TestAgentStartUnknownKeepsPendingTaskAndLinksByCreationID(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	shortAgentStartTimeout(t)
	f.slowStart = true
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil || !strings.Contains(res, "may already be starting") {
		t.Fatalf("unknown start: res=%q err=%v", res, err)
	}
	todo := onlyTodo(s)
	if todo.Origin != "conv1" || todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" || len(todo.Agents) != 0 {
		t.Fatalf("pending task = %+v", todo)
	}
	if len(f.starts) != 1 || f.starts[0]["creation_id"] != todo.ID || f.starts[0]["incarnation"] == "" {
		t.Fatalf("start lacks task identity: %+v", f.starts)
	}
	requireStartUnknownState(t, s, todo.ID, true)

	// The session list arrives later with the node's persisted creation identity. It joins the pending card directly.
	s.autoLinkTodos("box", []proto.Session{{SessionID: "s-late", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, StartedByPersona: true, Status: proto.StatusWorking, UpdatedAt: time.Now()}})
	got := todoNow(s, todo.ID)
	if len(got.Agents) != 1 || got.Agents[0].SessionID != "s-late" || got.Agents[0].Source != "assistant_start" {
		t.Fatalf("late session not linked: %+v", got)
	}
	requireStartUnknownState(t, s, todo.ID, false)
	// A later, genuinely unjudged stop has another key and is not part of this retraction.
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "later-real-stop", Detail: "a later judge failure"})
	if _, err := s.linkStartedAgentTask(context.Background(), got, Device{ID: "box"}, "/code/app", "s-other", ""); err != nil {
		t.Fatal(err)
	}
	requireStartUnknownState(t, s, todo.ID, false)
	s.supervise.mu.Lock()
	pending := s.supervise.pending[todo.ID+"|"+sigUnjudged]
	s.supervise.mu.Unlock()
	if pending.Key != "later-real-stop" || storedSupervisePending(t, s)[todo.ID+"|"+sigUnjudged].Key != "later-real-stop" {
		t.Fatalf("confirmation deleted the later unjudged stop: memory=%+v durable=%+v", pending, storedSupervisePending(t, s)[todo.ID+"|"+sigUnjudged])
	}
}

// A creation-id confirmation can arrive after the start wait has ended and the user has closed the prepared card.
// That exact session still belongs on the done card for cleanup; an unrelated manual session gains no link (#5732).
func TestAgentStartUnknownLinksDelayedCreationIDAfterTaskClosed(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	shortAgentStartTimeout(t)
	f.slowStart = true
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil || !strings.Contains(res, "may already be starting") {
		t.Fatalf("unknown start: res=%q err=%v", res, err)
	}
	todo := onlyTodo(s)
	requireStartUnknownState(t, s, todo.ID, true)
	now := time.Now().UTC()
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].DoneAt = &now
	s.todos.mu.Unlock()

	linked := s.autoLinkTodos("box", []proto.Session{
		{SessionID: "s-late", MachineID: "box", Cwd: "/code/app", Agent: "claude", StartedByPersona: true, Status: proto.StatusWorking,
			Hierarchy: &proto.SessionHierarchy{CreationID: todo.ID}, UpdatedAt: now},
		{SessionID: "manual", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, UpdatedAt: now},
	})
	if !linked {
		t.Fatal("a retained delayed creation-id link was not reported to the session-list caller")
	}
	got := todoNow(s, todo.ID)
	if got.DoneAt == nil || len(got.Agents) != 1 || got.Agents[0].SessionID != "s-late" || got.Agents[0].Source != "assistant_start" {
		t.Fatalf("the delayed creation-id session is not retained on the closed card: %+v", got)
	}
	if active := s.todos.activeBySession[[2]string{"box", "s-late"}]; active != "" {
		t.Fatalf("the done card became active again as %q", active)
	}
	for _, a := range got.Agents {
		if a.SessionID == "manual" {
			t.Fatalf("an unrelated manual session was auto-linked: %+v", got.Agents)
		}
	}
	requireStartUnknownState(t, s, todo.ID, false)
}

// Exact done creation identity retires the provisional result even when membership was already known or dismissed.
// Neither path inserts or reactivates a link, and neither may leave start-unknown armed for a later replay (#5732).
func TestDelayedCreationRecognitionRetractsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dismissed bool
	}{
		{name: "already linked"},
		{name: "dismissed stays dismissed", dismissed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newQuietServiceOn(t, &memStore{})
			todo, err := s.createTodo(context.Background(), "Recognize the done creation", "", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(context.Background(), todo.ID, "box", "s-late", todo.Title, "/code/app", "claude"); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			key := [2]string{"box", "s-late"}
			s.todos.mu.Lock()
			current := s.todos.todos[todo.ID]
			current.DoneAt = &now
			if tc.dismissed {
				current.Agents[0].DismissedAt = &now
				delete(s.todos.activeBySession, key)
			}
			s.todos.mu.Unlock()
			s.signal(superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "provisional start"})
			requireStartUnknownState(t, s, todo.ID, true)

			handled, linked := s.linkDelayedDoneCreation("box", proto.Session{SessionID: "s-late", MachineID: "box"}, todo.ID, "")
			if !handled || linked {
				t.Fatalf("linkDelayedDoneCreation() = handled %v, linked %v; want true, false", handled, linked)
			}
			requireStartUnknownState(t, s, todo.ID, false)
			got := todoNow(s, todo.ID)
			if len(got.Agents) != 1 || got.Agents[0].SessionID != "s-late" || (got.Agents[0].DismissedAt != nil) != tc.dismissed {
				t.Fatalf("exact recognition changed membership: %+v", got.Agents)
			}
		})
	}
}

// Queue admission is another durable form of the provisional result. A later exact confirmation must keep it from
// replaying after a restart, not only clear the outbox and turn ledger (#5732).
func TestAgentStartConfirmationRetractsQueuedStartUnknown(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	todo, err := s.createTodo(context.Background(), "Confirm the start", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(context.Background(), todo.ID, "conv1", nil, 0)
	todo = todoNow(s, todo.ID)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
	other := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "later-real-stop", Detail: "a later genuine stop", At: time.Now()}
	signals := []superviseSignal{g, other}
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: "stale unknown start", At: time.Now()}, signals, time.Now(), time.Time{}) {
		t.Fatal("the unknown start was not admitted to the durable relay queue")
	}
	if _, err := s.linkStartedAgentTask(context.Background(), todo, Device{ID: "box"}, "/code/app", "s-confirmed", ""); err != nil {
		t.Fatal(err)
	}
	s.supervise.mu.Lock()
	queued := len(s.supervise.queued["conv1"])
	s.supervise.mu.Unlock()
	if queued != 0 {
		t.Fatal("the confirmed start-unknown remained in the in-memory queue")
	}
	simulateSupervisionRestart(t, s)
	if got := queuedRelays(s, "conv1"); len(got) != 0 {
		t.Fatalf("the confirmed start-unknown remained queued: %+v", got)
	}
	if got := storedSupervisePending(t, s)[other.id()]; got.Key != other.Key {
		t.Fatalf("the queued retraction deleted the later genuine stop: %+v", got)
	}
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.drainRelays("server", []proto.Session{idle})
}

// A pre-structured queue has only the provisional start's diagnostic text. Exact confirmation withdraws that
// unambiguous legacy notice without touching another todo's still-live start result (#5732).
func TestAgentStartConfirmationWithdrawsLegacyTextStartUnknown(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Withdraw legacy start text", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	other, err := s.createTodo(ctx, "Unrelated start still unknown", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, other.ID, "conv1", nil, 0)
	const diagnostic = "启动结果还没确认；待办会按 creation_id 补联，不要重复开 Agent" // i18n-ignore: legacy fixture
	s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: diagnostic + " " + todo.ID, At: time.Now()})
	s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: diagnostic + " " + other.ID, At: time.Now()})
	if _, err := s.linkStartedAgentTask(ctx, todoNow(s, todo.ID), Device{ID: "box"}, "/code/app", "s-confirmed", ""); err != nil {
		t.Fatal(err)
	}
	if got := queuedRelays(s, "conv1"); len(got) != 1 || !strings.Contains(got[0].Text, other.ID) {
		t.Errorf("exact confirmation retained the wrong legacy messages: %+v", got)
	}
	simulateSupervisionRestart(t, s)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the unrelated legacy start-unknown to drain", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), other.ID)
	})
	text := strings.Join(f.noticesTo("conv1"), "\n")
	if strings.Contains(text, todo.ID) {
		t.Errorf("the confirmed legacy start-unknown was delivered: %s", text)
	}
	if !strings.Contains(text, other.ID) {
		t.Errorf("the unrelated legacy start-unknown did not drain: %s", text)
	}
}

// A send already in flight owns its original text. Once that try fails, however, the confirmed start must not be sent
// again from the retained queue head, including after a restart (#5732).
func TestAgentStartConfirmationWithdrawsDrainingStartUnknown(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	todo, err := s.createTodo(context.Background(), "Confirm during a send", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(context.Background(), todo.ID, "conv1", nil, 0)
	todo = todoNow(s, todo.ID)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
	other := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "later-real-stop", Detail: "a later genuine stop", At: time.Now()}
	signals := []superviseSignal{g, other}
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: "stale unknown start", At: time.Now()}, signals, time.Now(), time.Time{}) {
		t.Fatal("the unknown start was not admitted to the durable relay queue")
	}
	started, release := make(chan struct{}), make(chan struct{})
	hub := &stalledAssistantSendHub{fakeHub: f, started: started, release: release, attempts: make(chan string, 4)}
	setHub(s, hub)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.drainRelays("server", []proto.Session{idle})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the queue head did not enter its first send")
	}
	if text := <-hub.attempts; !strings.Contains(text, "stale unknown start") {
		t.Fatalf("the first send fixture = %q", text)
	}
	if _, err := s.linkStartedAgentTask(context.Background(), todo, Device{ID: "box"}, "/code/app", "s-confirmed", ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitFor(t, "the failed first send to settle", func() bool { return !drainingNow(s, "conv1") })
	simulateSupervisionRestart(t, s)
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the withdrawn head to leave the queue", func() bool { return s.queuedFor("conv1") == 0 })
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(context.Background())
	select {
	case text := <-hub.attempts:
		if strings.Contains(text, "stale unknown start") {
			t.Fatalf("the failed queue head replayed the confirmed start-unknown: %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("the other queued signal was not regathered after withdrawing the old head")
	}
}

// The delivered side of the same in-flight mixed digest: its original text already carried the other signals, so
// withdrawing the confirmed start must not leave retry evidence that delivers those signals a second time. This holds
// both for an acknowledged send and for an in-doubt send whose original line becomes visible before the next drain.
func TestAgentStartConfirmationAcknowledgesOtherSignalsAfterSuccessfulHead(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		inDoubt, restart, confirmAfter bool
	}{
		{name: "acknowledged"},
		{name: "in doubt", inDoubt: true},
		{name: "in doubt across restart", inDoubt: true, restart: true},
		{name: "confirmation after in-doubt send", inDoubt: true, confirmAfter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inDoubt := tc.inDoubt
			s, f := newQuietServiceOn(t, &memStore{})
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			presenceTimes(t, 0, 0)
			ownerTurnTimes(t, 0, 0)
			todo, err := s.createTodo(context.Background(), "Confirm during a successful send", "", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			s.superviseTodo(context.Background(), todo.ID, "conv1", nil, 0)
			todo = todoNow(s, todo.ID)
			g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "start-unknown@" + todo.ID, Detail: "stale unknown start", At: time.Now()}
			other := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "later-real-stop", Detail: "a later genuine stop", At: time.Now()}
			signals := []superviseSignal{g, other}
			if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: "mixed supervision", At: time.Now()}, signals, time.Now(), time.Time{}) {
				t.Fatal("the mixed digest was not admitted to the durable relay queue")
			}
			hub := &stalledAssistantSendHub{fakeHub: f, started: make(chan struct{}), release: make(chan struct{}), attempts: make(chan string, 2), succeed: !inDoubt, inDoubt: inDoubt}
			setHub(s, hub)
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			s.drainRelays("server", []proto.Session{idle})
			select {
			case <-hub.started:
			case <-time.After(time.Second):
				t.Fatal("the mixed queue head did not enter its send")
			}
			confirm := func() {
				if _, err := s.linkStartedAgentTask(context.Background(), todo, Device{ID: "box"}, "/code/app", "s-confirmed", ""); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.confirmAfter {
				confirm()
			}
			close(hub.release)
			waitFor(t, "the original mixed send to settle", func() bool { return !drainingNow(s, "conv1") })
			if tc.confirmAfter {
				confirm()
			}
			if inDoubt {
				// The first look found no line, so the head stayed doubtful and withdrawn. Its original text then became
				// visible: the next drain must acknowledge that delivery rather than replay the other signal.
				sent := <-hub.attempts
				if tc.restart {
					simulateSupervisionRestart(t, s)
					s.relays.mu.Lock()
					s.relays.doubted = nil // a real process restart loses the in-memory doubt book too
					s.relays.mu.Unlock()
				}
				f.mu.Lock()
				f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: sent, TS: time.Now()})
				f.mu.Unlock()
				s.drainRelays("server", []proto.Session{idle})
				waitFor(t, "the landed withdrawn head to leave the queue", func() bool { return s.queuedFor("conv1") == 0 })
			}
			if got := storedSupervisePending(t, s); len(got) != 0 {
				t.Fatalf("the delivered original digest left its other signal for a duplicate: %+v", got)
			}
			if calls := hub.calls.Load(); calls != 1 {
				t.Fatalf("the delivered mixed digest sent %d times", calls)
			}
		})
	}
}

// Definite failures stay errors: a request the node refused before running anything, and an unreachable machine
// (nothing was started) — never a neutral "may be starting" result.
func TestAgentStartDefiniteFailuresStayErrors(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeHub)
		want func(error) bool
	}{
		{"refused", func(f *fakeHub) { f.failStart = &rpc.Error{Code: "open_elsewhere", Message: "open elsewhere"} },
			func(err error) bool { var re *rpc.Error; return errors.As(err, &re) && re.Code == "open_elsewhere" }},
		{"offline", func(f *fakeHub) { f.offline = map[string]bool{"box": true} },
			func(err error) bool { return errors.Is(err, apierr.ErrOffline) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			tc.set(f)
			res, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
				json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
			if err == nil || !tc.want(err) {
				t.Fatalf("%s: got res=%q err=%v", tc.name, res, err)
			}
		})
	}
}

// The same cancellation is an unknown result when the start answer never arrives: it stays neutral and open, not a
// definite failure. This is the release-side control for the detached failure write above.
func TestAgentStartCallerCancellationKeepsUnknownResultNeutral(t *testing.T) {
	s, f := newQuietServiceOn(t, todoTestDB(t))
	// The bound running out is what makes the start unknown here (the fake never answers, and the detached start
	// outlives the caller's cancel). It also covers the reads before the node is asked, the model configuration from
	// Postgres among them: one second was not always enough for those on a loaded machine (#14159).
	agentStartTimeoutOf(t, 3*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	f.slowStart = true
	f.onStart = func(*fakeHub, map[string]any) { cancel() }
	res, err := s.runTool(ctx, "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"check CI"}`))
	if err != nil || !strings.Contains(res, "may already be starting") {
		t.Fatalf("unknown start after caller cancellation = %q (%v)", res, err)
	}
	todo := onlyTodo(s)
	if todo.DoneAt != nil || todo.StartError != "" || len(todo.Agents) != 0 {
		t.Fatalf("unknown start was converted into a definite failure: %+v", todo)
	}
}

// Which failures leave the start's outcome unknown, and which are definite.
func TestStartResultUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deadline", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, true},
		{"closed", rpc.ErrClosed, true},
		{"dropped", rpc.ErrDropped, true},
		{"relay words", &rpc.Error{Message: rpc.ErrClosed.Error()}, true},
		{"wrapped deadline", fmt.Errorf("session.start: %w", context.DeadlineExceeded), true},
		{"coded refusal", &rpc.Error{Code: "open_elsewhere", Message: "open elsewhere"}, false},
		{"offline", apierr.ErrOffline, false},
		{"no device", ErrNoDevice, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := startResultUnknown(tc.err); got != tc.want {
				t.Fatalf("startResultUnknown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestTracksNodeWriteClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		want   bool
	}{
		{"session send", DeviceSessionSend, true},
		{"session start", DeviceSessionStart, true},
		{"task terminal close", devicewire.MethodCloseTaskTerminal, true},
		{"unrelated method", DeviceSessionItems, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tracksNodeWrite(tc.method); got != tc.want {
				t.Fatalf("tracksNodeWrite(%q) = %t, want %t", tc.method, got, tc.want)
			}
		})
	}
}

// Which failed sends may still have reached the agent (#7539): no answer came, or the node's error says delivered
// unknown or yes. A refusal, an error that says nothing of delivery, and a request that never went out are definite.
func TestSendUnknown(t *testing.T) {
	delivered := func(code, d string) error {
		return &rpc.Error{Code: code, Message: "send failed", Data: json.RawMessage(`{"delivered":"` + d + `"}`)}
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deadline", context.DeadlineExceeded, true},
		{"dropped", rpc.ErrDropped, true},
		{"inbox did not confirm it", delivered("peer_unconfirmed", "unknown"), true},
		{"deadline across the bus", relayedDeadline, true},
		{"only the deadline words", errors.New(context.DeadlineExceeded.Error()), true},
		{"device deadline", &rpc.Error{Code: devicewire.CodeDeadline, Message: "node timed out"}, true},
		{"device link dropped", &rpc.Error{Code: devicewire.CodeNodeClosed, Message: "node disconnected"}, true},
		{"node took nothing before its timeout", delivered(devicewire.CodeDeadline, "none"), false},
		{"cancellation across the bus", &rpc.Error{Message: context.Canceled.Error()}, true},
		{"different words", &rpc.Error{Message: "configuration: " + context.DeadlineExceeded.Error()}, false},
		{"coded refusal with deadline words", &rpc.Error{Code: "invalid_params", Message: context.DeadlineExceeded.Error()}, false},
		{"node typed nothing before the link closed", &rpc.Error{Message: rpc.ErrClosed.Error(), Data: json.RawMessage(`{"delivered":"none"}`)}, false},
		{"writer timed out after a partial frame", errors.Join(rpc.ErrNotWritten, context.DeadlineExceeded), true},
		{"timed out before writing any frame", rpc.NotSent(errors.Join(rpc.ErrNotWritten, context.DeadlineExceeded)), false},
		{"typing failed part way", delivered("", "unknown"), true},
		{"wrapped, typing failed part way", fmt.Errorf("session.send: %w", delivered("", "unknown")), true},
		{"delivered, then failed", delivered("internal", "yes"), true},
		{"refused, nothing typed", delivered("not_delivered", "none"), false},
		{"coded, no delivered", &rpc.Error{Code: "open_elsewhere", Message: "open elsewhere"}, false},
		{"data without delivered", &rpc.Error{Message: "send failed", Data: json.RawMessage(`{"draft":"hi"}`)}, false},
		{"plain error", errors.New("no such session"), false},
		{"never went out, its wait ran out", rpc.NotSent(context.DeadlineExceeded), false},
		{"never went out, closed", rpc.NotSent(rpc.ErrClosed), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sendUnknown(tc.err); got != tc.want {
				t.Fatalf("sendUnknown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestSendUnknownAcrossBus(t *testing.T) {
	clientConn, nodeConn := net.Pipe()
	client, node := rpc.New("client", clientConn), rpc.New("node", nodeConn)
	answers := map[string]error{"deadline": context.DeadlineExceeded, "written": nodeAfterWrite, "refused": nodeRefused}
	node.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) { return nil, answers[method] }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var servers sync.WaitGroup
	servers.Go(func() { _ = client.Serve(ctx, clientConn) })
	servers.Go(func() { _ = node.Serve(ctx, nodeConn) })
	t.Cleanup(func() {
		clientConn.Close()
		nodeConn.Close()
		servers.Wait()
	})
	for _, method := range []string{"deadline", "written", "refused"} {
		err := busError(client.Call(ctx, method, nil, nil))
		var re *rpc.Error
		if !errors.As(err, &re) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s did not cross the RPC boundary: %v", method, err)
		}
		if sendUnknown(err) != (method != "refused") {
			t.Errorf("%s: mistaken delivery result for %+v", method, re)
		}
	}
}

type expiredSendParams struct{}

func (expiredSendParams) MarshalJSON() ([]byte, error) { return nil, context.DeadlineExceeded }

func TestSendDeadlineBeforeDispatch(t *testing.T) {
	s, f := newTaskService(t)
	err := s.call(t.Context(), "box", DeviceSessionSend, expiredSendParams{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || sendUnknown(err) || sessionSends(f) != 0 {
		t.Fatalf("definitely unsent: %v, dispatched %d times", err, sessionSends(f))
	}
}

// The adoption filter: new, hub-started, non-assistant, same launcher and directory, fresh sign of life.
func TestAdoptStartedSessionFilters(t *testing.T) {
	s, f := newTaskService(t)
	now := time.Now().UTC()
	mk := func(id string, fn func(*proto.Session)) proto.Session {
		sess := proto.Session{SessionID: id, MachineID: "box", Agent: "claude", Launcher: "claude-code",
			Cwd: "/code/app", StartedByPersona: true, UpdatedAt: now}
		fn(&sess)
		return sess
	}
	known := map[string]bool{"old": true}
	dev := Device{ID: "box"}
	// Adopted: a new cwd session, and a new one whose node-verified WorkspaceCwd is the directory.
	for _, sess := range []proto.Session{
		mk("new", func(*proto.Session) {}),
		mk("new", func(s *proto.Session) { s.Cwd = ""; s.WorkspaceCwd = "/code/app" }),
	} {
		f.setSessions("box", mk("old", func(*proto.Session) {}), sess)
		if got, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", known); !ok || got.SessionID != "new" {
			t.Fatalf("got %+v ok=%v, want new adopted", got, ok)
		}
	}

	// Rejected: pre-existing, not hub-started, the assistant, other launcher/cwd, stale or never-seen sign of life.
	for _, tc := range []struct {
		name string
		fn   func(*proto.Session)
	}{
		{"not hub-started", func(s *proto.Session) { s.StartedByPersona = false }},
		{"the assistant itself", func(s *proto.Session) { s.Assistant = true }},
		{"other launcher", func(s *proto.Session) { s.Launcher = "codex" }},
		{"other directory", func(s *proto.Session) { s.Cwd = "/elsewhere" }},
		{"stale sign of life", func(s *proto.Session) { s.UpdatedAt = now.Add(-agentStartAdoptWindow - time.Minute) }},
		{"never seen", func(s *proto.Session) { s.UpdatedAt = time.Time{} }},
	} {
		f.setSessions("box", mk("old", func(*proto.Session) {}), mk("new", tc.fn))
		if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", known); ok {
			t.Fatalf("%s session was adopted", tc.name)
		}
	}

	// A pre-start list that failed means no guessing afterwards.
	f.setSessions("box", mk("new", func(*proto.Session) {}))
	if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", nil); ok {
		t.Fatal("adoption happened without a pre-start snapshot")
	}
}

// ask_user's answers come back keyed by question text: two questions worded alike (after trimming) are refused before
// any card goes on the phone, instead of one answer silently overwriting the other (#4517). Distinct wording still asks.
func TestAskUserRefusesDuplicateQuestionText(t *testing.T) {
	s := newTestService(t, newFakeHub())
	q := func(text string) map[string]any {
		return map[string]any{"question": text, "options": []map[string]any{{"label": "好"}, {"label": "不好"}}}
	}
	call := func(texts ...string) (proto.ToolResult, bool) {
		var qs []map[string]any
		for _, x := range texts {
			qs = append(qs, q(x))
		}
		raw, _ := json.Marshal(map[string]any{"why_owner": "priority", "questions": qs})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		carded := make(chan bool, 1)
		go func() {
			for ctx.Err() == nil {
				if qs := s.pendingQuestions(); len(qs) == 1 {
					yes := map[string]string{}
					for _, x := range qs[0].Questions {
						yes[x.Question] = "好"
					}
					raw, _ := json.Marshal(proto.Resolution{Answers: yes})
					_, _ = s.Action(context.Background(), Action{Op: "answer", InteractionID: qs[0].ID, Answer: raw})
					carded <- true
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			carded <- false
		}()
		r := s.Tool(ctx, ToolCall{Name: "ask_user", Arguments: raw, MachineID: "mac", SessionID: "conv-assistant"})
		cancel()
		return r, <-carded
	}
	text := func(r proto.ToolResult) string {
		var b strings.Builder
		for _, c := range r.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	}

	r, carded := call("发吗？", "  发吗？ ")
	if !r.IsError || text(r) != i18n.Err("hub-hub.duplicate_question").Error() {
		t.Fatalf("duplicate questions: error=%v %q", r.IsError, text(r))
	}
	if carded {
		t.Fatal("a card went on the phone for duplicate questions")
	}

	r, carded = call("发吗？", "现在发吗？")
	if r.IsError || !carded || !strings.Contains(text(r), "发吗？ → 好") || !strings.Contains(text(r), "现在发吗？ → 好") {
		t.Fatalf("distinct questions: carded=%v error=%v %q", carded, r.IsError, text(r))
	}
}

// agent_send's readonly refusal is original MCP model text, not a localized user-facing notification or card.
func TestAgentSendReadonlyPreservesModelResult(t *testing.T) {
	s, f := newTaskService(t)
	const reason = "Original source diagnostic / 原始原因 / 元の理由"
	for _, caps := range []*proto.SessionCapabilities{nil, {Read: true}} {
		f.setSessions("box", proto.Session{SessionID: "readonly-agent", MachineID: "box", Cwd: "/code/app",
			SourceKind: "external-history", Capabilities: caps, ReadOnlyReason: reason})
		for _, locale := range []string{"en", "zh-Hans", "ja", "en"} {
			f.mu.Lock()
			f.hello.Locale = locale
			f.mu.Unlock()
			res := s.Tool(i18n.With(context.Background(), i18n.Lang(locale)), ToolCall{
				MachineID: "server", SessionID: "conv9", Name: "agent_send",
				Arguments: json.RawMessage(`{"machine":"box","session_id":"readonly-agent","text":"Continue"}`),
			})
			if !res.IsError || len(res.Content) != 1 || res.Content[0].Text != "这个 Agent 仅支持查看："+reason {
				t.Fatalf("locale=%q capabilities=%+v: %+v", locale, caps, res)
			}
		}
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("readonly agent received messages: %v", sent)
	}
	if len(s.pendingQuestions()) != 0 || len(f.pushed()) != 0 {
		t.Fatal("model-only refusal created a user-facing card or push")
	}
}
