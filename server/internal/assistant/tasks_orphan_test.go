package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Open tasks nobody works on (tasks_orphan.go).

// closedAgent is s1 on box with its tab closed, last active at.
func closedAgent(at time.Time) proto.Session {
	return proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusOffline,
		State: proto.StateOffline, StartedByPersona: true, UpdatedAt: at, LastText: "API Error: 400"}
}

func TestAgentGone(t *testing.T) {
	now := time.Now()
	wake, reset := now.Add(time.Hour), now.Add(2*time.Hour)
	cases := []struct {
		name string
		edit func(*orphanAgent)
		want string
	}{
		{"closed", func(*orphanAgent) {}, orphanClosed},
		{"closed after a model error", func(a *orphanAgent) { a.c.TurnError = "API Error: 400 invalid request" }, orphanError},
		// The hub's own word on a turn with no output is no error that ended it (#10187).
		{"closed after a turn with no output", func(a *orphanAgent) {
			a.c.TurnError, a.c.TurnErrorKind = "This turn had no output", proto.TurnErrorNoOutput
		}, orphanClosed},
		{"closed after Persona's Stop", func(a *orphanAgent) {
			a.c.TurnError, a.c.TurnErrorKind = "Stopped with its conversation", proto.TurnErrorStopped
		}, orphanClosed},
		{"computer offline", func(a *orphanAgent) { a.reachable = false }, orphanOffline},
		{"no longer listed", func(a *orphanAgent) { a.listed = false }, orphanClosed},
		{"open in a tab, stopped", func(a *orphanAgent) { a.c.Status, a.c.PaneID = proto.StatusIdle, "w1:p1" }, ""},
		{"in a host without a pane", func(a *orphanAgent) { a.c.Status, a.c.State, a.c.Host = proto.StatusIdle, proto.StateIdle, "paseo" }, ""},
		{"state unknown in a pane", func(a *orphanAgent) {
			a.c.Status, a.c.State, a.c.PaneID = proto.StatusUnknown, proto.StateUnknown, "w1:p1"
		}, ""},
		// Closed is no host and no pane, whatever the status says (sessionClosed).
		{"hostless, interrupted / unknown", func(a *orphanAgent) { a.c.Status, a.c.State = proto.StatusInterrupted, proto.StateUnknown }, orphanClosed},
		{"hostless, idle / idle", func(a *orphanAgent) { a.c.Status, a.c.State = proto.StatusIdle, proto.StateIdle }, orphanClosed},
		{"hostless, unknown / unknown", func(a *orphanAgent) { a.c.Status, a.c.State = proto.StatusUnknown, proto.StateUnknown }, orphanClosed},
		{"hostless, interrupted, after a model error", func(a *orphanAgent) {
			a.c.Status, a.c.State, a.c.TurnError = proto.StatusInterrupted, proto.StateUnknown, "API Error: 400 invalid request"
		}, orphanError},
		{"hostless, interrupted, a wake is due", func(a *orphanAgent) {
			a.c.Status, a.c.State = proto.StatusInterrupted, proto.StateUnknown
			a.c.Work = &proto.WorkState{Phase: proto.WorkDone, WakeAt: &wake}
		}, ""},
		{"hostless, interrupted, a continue is pending", func(a *orphanAgent) {
			a.c.Status, a.c.State, a.c.TurnError = proto.StatusInterrupted, proto.StateUnknown, "overloaded"
			a.c.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, WakeAt: &wake}
		}, ""},
		{"working", func(a *orphanAgent) { a.c.Status, a.c.State = proto.StatusWorking, proto.StateWorking }, ""},
		{"starting", func(a *orphanAgent) { a.c.Starting = true }, ""},
		{"assistant conversation", func(a *orphanAgent) { a.c.Assistant = true }, ""},
		{"a card waits", func(a *orphanAgent) { a.c.PendingInteractions = 1 }, ""},
		{"needs you", func(a *orphanAgent) { a.c.State = proto.StateNeedsYou }, ""},
		{"queued input", func(a *orphanAgent) { a.c.Queued = []string{"go on"} }, ""},
		{"a wake is due", func(a *orphanAgent) { a.c.Work = &proto.WorkState{Phase: proto.WorkDone, WakeAt: &wake} }, ""},
		{"a continue is pending", func(a *orphanAgent) {
			a.c.TurnError = "overloaded"
			a.c.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, WakeAt: &wake}
		}, ""},
		{"waits for CI", func(a *orphanAgent) { a.c.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal} }, ""},
		{"waits out a usage limit", func(a *orphanAgent) {
			a.c.TurnError, a.c.TurnErrorKind, a.c.TurnErrorResetsAt = "usage limit", proto.TurnErrorUsageLimit, &reset
		}, ""},
		{"work done, nothing pending", func(a *orphanAgent) { a.c.Work = &proto.WorkState{Phase: proto.WorkDone} }, orphanClosed},
		// A node that connected again a moment ago may not list the agent yet, or list it as it is.
		{"not listed, its node just connected again", func(a *orphanAgent) { a.listed, a.connected = false, now.Add(-30*time.Second) }, ""},
		{"closed, its node just connected again", func(a *orphanAgent) { a.connected = now.Add(-30 * time.Second) }, ""},
		{"not listed, its node connected a while ago", func(a *orphanAgent) {
			a.listed, a.connected = false, now.Add(-orphanReconnectWait-time.Minute)
		}, orphanClosed},
		{"offline, its node connected a moment before", func(a *orphanAgent) { a.reachable, a.connected = false, now.Add(-30*time.Second) }, orphanOffline},
	}
	for _, c := range cases {
		a := orphanAgent{machine: "box", session: "s1", reachable: true, listed: true, c: closedAgent(now.Add(-time.Hour))}
		c.edit(&a)
		if got := agentGone(a, now); got != c.want {
			t.Errorf("%s: agentGone = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTaskOrphan(t *testing.T) {
	now := time.Now()
	done := now.Add(-time.Hour)
	gone := func(session string, edit func(*orphanAgent)) orphanAgent {
		a := orphanAgent{machine: "box", session: session, reachable: true, listed: true, c: closedAgent(now.Add(-time.Hour))}
		a.c.SessionID = session
		if edit != nil {
			edit(&a)
		}
		return a
	}
	failed := gone("s2", func(a *orphanAgent) { a.c.TurnError = "API Error: 400 invalid request" })
	away := gone("s3", func(a *orphanAgent) { a.reachable = false })
	open := gone("s4", func(a *orphanAgent) { a.c.Status, a.c.PaneID = proto.StatusIdle, "w1:p1" })
	cases := []struct {
		name   string
		todo   Todo
		agents []orphanAgent
		want   string // "": not orphaned
	}{
		{"its only agent is closed", Todo{}, []orphanAgent{gone("s1", nil)}, orphanClosed},
		{"its only agent ended on an error", Todo{}, []orphanAgent{failed}, orphanError},
		{"its computer is away", Todo{}, []orphanAgent{away}, orphanOffline},
		{"the error is said before the rest", Todo{}, []orphanAgent{gone("s1", nil), away, failed}, orphanError},
		{"away is said before closed", Todo{}, []orphanAgent{gone("s1", nil), away}, orphanOffline},
		{"one agent is still open", Todo{}, []orphanAgent{failed, open}, ""},
		{"no agent yet", Todo{}, nil, ""},
		{"done", Todo{DoneAt: &done}, []orphanAgent{failed}, ""},
		{"waits for its review", Todo{Supervision: &Supervision{State: reviewPending}}, []orphanAgent{failed}, ""},
		{"waits for the user's decision", Todo{Supervision: &Supervision{State: reviewNeedsUser}}, []orphanAgent{failed}, ""},
		{"timed out", Todo{Supervision: &Supervision{State: reviewTimedOut}}, []orphanAgent{failed}, ""},
		{"closing", Todo{Supervision: &Supervision{State: reviewClosing}}, []orphanAgent{failed}, ""},
		{"in rework", Todo{Supervision: &Supervision{State: reviewRework}}, []orphanAgent{failed}, orphanError},
		// The owner's check waits on the owner, not on purpose for its agents: a gone one is still an orphan (#17878).
		{"owner's check, its agent closed", Todo{Supervision: &Supervision{State: reviewUserCheck}}, []orphanAgent{gone("s1", nil)}, orphanClosed},
		{"owner's check, its agent ended on an error", Todo{Supervision: &Supervision{State: reviewUserCheck}}, []orphanAgent{failed}, orphanError},
		{"owner's check, its computer is away", Todo{Supervision: &Supervision{State: reviewUserCheck}}, []orphanAgent{away}, orphanOffline},
	}
	for _, c := range cases {
		o, ok := taskOrphan(c.todo, c.agents, now)
		if ok != (c.want != "") || o.Reason != c.want {
			t.Errorf("%s: taskOrphan = %q %v, want %q", c.name, o.Reason, ok, c.want)
		}
	}

	// The stretch is named by the agents and their last step, not by why they are gone: a closed agent whose
	// computer then goes away is the same stretch, a new step is another.
	a, _ := taskOrphan(Todo{}, []orphanAgent{gone("s1", nil)}, now)
	b, _ := taskOrphan(Todo{}, []orphanAgent{gone("s1", func(a *orphanAgent) { a.c.TurnError = "API Error: 400" })}, now)
	moved, _ := taskOrphan(Todo{}, []orphanAgent{gone("s1", func(a *orphanAgent) { a.c.UpdatedAt = now })}, now)
	if a.key == "" || a.key != b.key || a.key == moved.key {
		t.Fatalf("keys: %q %q %q", a.key, b.key, moved.key)
	}
	if b.detail == "" || b.session != "s1" {
		t.Fatalf("the error's agent: %+v", b)
	}
}

// orphanTask is a supervised task of conv1 with its agent s1 on box.
func orphanTask(t *testing.T, s *Service) Todo {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Example task", "Collect task evidence.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"Tests pass"}, 30)
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Example task", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	return todoNow(s, todo.ID)
}

// listedOrphan is what GET /api/v1/todos says of to-do id's `orphaned`.
func listedOrphan(t *testing.T, s *Service, id string) *TodoOrphan {
	t.Helper()
	w := serve(s, "GET", "/api/v1/todos", "")
	var body struct {
		Todos []struct {
			ID       string      `json:"id"`
			Orphaned *TodoOrphan `json:"orphaned"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("todos: %d %s: %v", w.Code, w.Body, err)
	}
	for _, td := range body.Todos {
		if td.ID == id {
			return td.Orphaned
		}
	}
	t.Fatalf("todo %s not listed: %s", id, w.Body)
	return nil
}

// An open task whose only agent ended on a model error and was closed reads as orphaned on the to-do and in the
// snapshot, and its supervisor is told once for that stretch; an agent back at work ends it.
func TestOrphanedTaskIsFlaggedAndToldOnce(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour} // no message goes out while the test looks
	todo := orphanTask(t, s)
	now := time.Now()

	working := closedAgent(now.Add(-time.Hour))
	working.Status, working.State, working.PaneID = proto.StatusWorking, proto.StateWorking, "w1:p1"
	f.setSessions("box", working)
	s.orphanWatch(ctx, now)
	if o := listedOrphan(t, s, todo.ID); o != nil {
		t.Fatalf("a task with a working agent is orphaned: %+v", o)
	}

	failed := closedAgent(now.Add(-time.Hour))
	failed.TurnError = "API Error: 400 invalid request"
	f.setSessions("box", failed)
	s.orphanWatch(ctx, now)
	o := listedOrphan(t, s, todo.ID)
	if o == nil || o.Reason != orphanError || o.Since.IsZero() {
		t.Fatalf("orphaned = %+v", o)
	}
	var line prompts.SnapshotTask
	for _, task := range s.snapshotTasks(ctx, "") {
		if task.ID == todo.ID {
			line = task
		}
	}
	if line.Orphan != orphanError {
		t.Fatalf("snapshot task = %+v", line)
	}

	id := todo.ID + "|" + sigOrphaned
	pending := func() (superviseSignal, bool) {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[id]
		return g, ok
	}
	s.superviseWatch(ctx, now)
	g, ok := pending()
	if !ok || g.Orphan != orphanError || g.Session != "s1" || !strings.Contains(g.Detail, "400") {
		t.Fatalf("signal = %+v %v", g, ok)
	}
	e, ok := s.digestEntry(ctx, g, "")
	if !ok || e.Orphan != orphanError {
		t.Fatalf("digest entry = %+v %v", e, ok)
	}
	if text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}}); !strings.Contains(text, "没有 Agent 在做了") { // i18n-ignore: the supervisor's prompt
		t.Fatalf("the message does not say it:\n%s", text)
	}
	// Told: the same stretch is not told again, whatever the later passes find.
	s.supervise.mu.Lock()
	delete(s.supervise.pending, id)
	s.supervise.sent[id] = g.Key
	s.supervise.mu.Unlock()
	for range 3 {
		s.orphanWatch(ctx, now.Add(time.Minute))
		s.superviseWatch(ctx, now.Add(time.Minute))
	}
	if g, ok := pending(); ok {
		t.Fatalf("told again within the stretch: %+v", g)
	}

	// Resumed, it is no longer orphaned, and what waited no longer holds.
	f.setSessions("box", working)
	s.orphanWatch(ctx, now.Add(2*time.Minute))
	if o := listedOrphan(t, s, todo.ID); o != nil {
		t.Fatalf("still orphaned with its agent back: %+v", o)
	}
	if _, ok := s.digestEntry(ctx, g, ""); ok {
		t.Fatal("the signal still holds with its agent back")
	}
	// Closed again after new work: another stretch, told again.
	again := closedAgent(now.Add(3 * time.Minute))
	f.setSessions("box", again)
	s.orphanWatch(ctx, now.Add(4*time.Minute))
	s.superviseWatch(ctx, now.Add(4*time.Minute))
	if g2, ok := pending(); !ok || g2.Key == g.Key || g2.Orphan != orphanClosed {
		t.Fatalf("the next stretch: %+v %v", g2, ok)
	}
}

// A computer that drops for a moment orphans nothing; one away for long does. A task not supervised is flagged on the
// to-do and tells nobody.
func TestOrphanedByAnOfflineComputer(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	todo, err := s.createTodo(ctx, "Example task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Example task", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.devices = []Device{{ID: "box", Name: "box"}} // not reachable
	f.mu.Unlock()
	now := time.Now()
	s.orphanWatch(ctx, now)
	if o := listedOrphan(t, s, todo.ID); o != nil {
		t.Fatalf("orphaned the moment its computer dropped: %+v", o)
	}
	s.orphanWatch(ctx, now.Add(orphanOfflineAfter+time.Minute))
	if o := listedOrphan(t, s, todo.ID); o == nil || o.Reason != orphanOffline {
		t.Fatalf("orphaned = %+v", o)
	}
	s.superviseWatch(ctx, now.Add(orphanOfflineAfter+time.Minute))
	if n := pendingSignals(s); n != 0 {
		t.Fatalf("%d signals for a task nobody supervises", n)
	}
	// Done, it is not listed as orphaned even before the next pass.
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	if o := listedOrphan(t, s, todo.ID); o != nil {
		t.Fatalf("a done to-do is orphaned: %+v", o)
	}
}

// #5124: a supervised task whose agent waits on its background work reads "waiting". Its process is then killed (with
// its tab and its background command): the node lists it as cut off (interrupted, no pane, nothing running). Within one
// pass the task reads stopped, not waiting, and its supervisor is told nobody works on it.
func TestWaitingTaskWhoseAgentDiedIsToldStopped(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour} // nothing goes out while the test looks
	todo := orphanTask(t, s)
	now := time.Now()
	turnAt := now.Add(-time.Hour)
	// What triageRules reported when its turn ended with a background command running.
	reported := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Line: "1 background task running", TurnAt: turnAt}
	listed := func(c proto.Session) proto.Session { // as the hub's list decorates the node's row (fleet_sessions.go)
		c.Work = workOf(i18n.En, c, &reported, nil)
		return c
	}

	alive := listed(proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", StartedByPersona: true,
		Status: proto.StatusDone, State: proto.StateWorking, BackgroundWorking: true, PaneID: "w1:p1", UpdatedAt: turnAt,
		Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell", Command: "sleep 3600"}}})
	f.setSessions("box", alive)
	if st := taskState(todoNow(s, todo.ID), []proto.Session{alive}); st != "waiting" {
		t.Fatalf("alive, waiting on its background command: %q", st)
	}
	s.orphanWatch(ctx, now)
	if o := listedOrphan(t, s, todo.ID); o != nil {
		t.Fatalf("orphaned while its agent waits on its background command: %+v", o)
	}

	killed := listed(proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", StartedByPersona: true,
		Status: proto.StatusInterrupted, State: proto.StateUnknown, UpdatedAt: turnAt})
	if killed.Work != nil {
		t.Fatalf("a dead agent keeps a wait only its process could end: %+v", killed.Work)
	}
	f.setSessions("box", killed)
	if st := taskState(todoNow(s, todo.ID), []proto.Session{killed}); st != "followup" {
		t.Fatalf("killed: %q, want stopped (followup)", st)
	}
	s.orphanWatch(ctx, now.Add(time.Minute)) // one pass
	if o := listedOrphan(t, s, todo.ID); o == nil || o.Reason != orphanClosed {
		t.Fatalf("killed, orphaned = %+v", o)
	}
	s.superviseWatch(ctx, now.Add(time.Minute))
	s.supervise.mu.Lock()
	g, ok := s.supervise.pending[todo.ID+"|"+sigOrphaned]
	s.supervise.mu.Unlock()
	if !ok || g.Orphan != orphanClosed || g.Session != "s1" || !g.urgent() {
		t.Fatalf("supervision signal = %+v %v", g, ok)
	}
	e, ok := s.digestEntry(ctx, g, "")
	if !ok {
		t.Fatal("the signal does not make it into the message")
	}
	if text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}}); !strings.Contains(text, "没有 Agent 在做了") { // i18n-ignore: the supervisor's prompt
		t.Fatalf("the message does not say it:\n%s", text)
	}
}

// What the lists keep of a stopped turn's wait on an agent the node lists as cut off by a restart (#5124): a wait only
// its own process ends is dropped; one the manager wakes, or one that names what it waits for, stays.
func TestWorkOfInterrupted(t *testing.T) {
	now := time.Now()
	wake := now.Add(time.Hour)
	sess := proto.Session{SessionID: "s1", Status: proto.StatusInterrupted, State: proto.StateUnknown, UpdatedAt: now}
	for _, c := range []struct {
		name string
		w    proto.WorkState
		kept bool
	}{
		{"background running", proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal}, false},
		{"its CLI goes on after a limit", proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, ResetsAt: &wake, Line: "limit resets at 10:00"}, true}, // no TurnErrorResumes: names its wait
		{"the manager wakes it", proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &wake}, true},
		{"waits for CI", proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Wait: "CI"}, true},
		{"waits for the user", proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser}, true},
		{"done", proto.WorkState{Phase: proto.WorkDone}, true},
	} {
		c.w.TurnAt = now
		if got := workOf(i18n.En, sess, &c.w, nil); (got != nil) != c.kept {
			t.Errorf("%s: %+v, kept %v", c.name, got, c.kept)
		}
		alive := sess
		alive.Status = proto.StatusDone
		if got := workOf(i18n.En, alive, &c.w, nil); got == nil {
			t.Errorf("%s: dropped for a live agent", c.name)
		}
	}
}

// An ssh node whose link dropped starts over, and its first lists do not have the task's agent yet: the supervisor is
// not told nobody works on the task until the node has had a reconnect window to list it.
func TestOrphanWaitsForAReconnectedNodeToListItsAgents(t *testing.T) {
	ctx := context.Background()
	ft := newFleetTest(t)
	s := ft.s
	ft.f.devices = []Device{{ID: "mac", Name: "Mac", Online: true, Reachable: true}}
	todo, err := s.createTodo(ctx, "Example task", "Collect task evidence.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"Tests pass"}, 30)
	if err := s.linkTodoAgent(ctx, todo.ID, "mac", "s1", "Example task", "/code/app", "codex"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ft.d.mu.Lock()
	ft.d.machines["mac"].connected = now.Add(-20 * time.Second) // the node is back after a dropped link
	ft.d.mu.Unlock()
	ft.sessions("mac", proto.Session{SessionID: "other", Status: proto.StatusIdle, PaneID: "w1:p1"})

	s.orphanWatch(ctx, now)
	if g, ok := s.orphanSignal(todoNow(s, todo.ID)); ok {
		t.Fatalf("orphaned the moment its node connected again: %+v", g)
	}
	later := now.Add(orphanReconnectWait)
	s.orphanWatch(ctx, later)
	if g, ok := s.orphanSignal(todoNow(s, todo.ID)); !ok || g.Orphan != orphanClosed {
		t.Fatalf("still not listed a reconnect window later: %+v %v", g, ok)
	}
}

func TestAcceptedWorkersRearmOrphanedLeader(t *testing.T) {
	now := time.Now().UTC()
	root := &Todo{ID: "root"}
	a := &Todo{ID: "a", PartOf: "root", DoneAt: &now, Supervision: &Supervision{State: reviewAccepted}}
	b := &Todo{ID: "b", Agents: []TodoAgent{{Source: sourceWorker, Via: "root"}}, Supervision: &Supervision{State: reviewAccepted}}
	todos := map[string]*Todo{"root": root, "a": a, "b": b}
	key := func() string { return completedWorkerSets(todos)["root"].key() }
	if key() != "" {
		t.Fatal("woke before last worker finished")
	}
	b.DoneAt = &now
	first := key()
	if first == "" || first != key() {
		t.Fatal("accepted worker generation was not stable")
	}
	b.Supervision.State = reviewCancelled
	if key() != "" {
		t.Fatal("cancellation counted as acceptance")
	}
	b.Supervision.State = reviewAccepted
	later := now.Add(time.Minute)
	b.DoneAt = &later
	if key() == first {
		t.Fatal("new acceptance did not rearm the wake")
	}
	root.DoneAt = &now
	if key() != "" {
		t.Fatal("completed parent generated a wake")
	}
}

func TestOrphanedLeaderReceivesAcceptedWorkerGeneration(t *testing.T) {
	s, f := newQuietService(t)
	root := startTaskLeader(t, s, f)
	child := planWorker(t, s, f, nil)
	ctx := t.Context()
	now := time.Now().UTC()
	leader := proto.Session{SessionID: root.Agents[0].SessionID, MachineID: "box", Host: "herdr", Agent: "claude",
		Status: proto.StatusInterrupted, UpdatedAt: now.Add(-time.Hour),
		Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}}
	f.setSessions("box", leader)
	s.orphanWatch(ctx, now)
	before, ok := s.orphanSignal(root)
	if !ok || before.WorkerSet != "" {
		t.Fatalf("open worker: %+v", before)
	}
	if _, _, err := s.updateSupervision(ctx, child.ID, func(v *Supervision) bool { v.State = reviewAccepted; return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reviewDoneTodo(ctx, child.ID, "accepted", true); err != nil {
		t.Fatal(err)
	}
	s.orphanWatch(ctx, now.Add(time.Minute))
	after, ok := s.orphanSignal(root)
	if !ok || after.WorkerSet == "" || after.Key == before.Key {
		t.Fatalf("last worker did not rearm supervision: %+v", after)
	}
	if _, ok := s.digestEntry(ctx, after, ""); !ok {
		t.Fatal("completed generation cannot reach the supervisor")
	}
	if _, _, err := s.updateSupervision(ctx, root.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.digestEntry(ctx, after, ""); ok {
		t.Fatal("wake overrode an owner decision")
	}
}

func TestOrphanWatchCoversStalledStartupQueues(t *testing.T) {
	for _, kind := range []string{workspaceQueueKey, pendingPlacementQueueKey} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			s, f := newQuietService(t)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			now := time.Now()
			todo, err := s.createTodo(ctx, "Queued task", "Carry out the decision", "conv1", "plan_q")
			if err != nil {
				t.Fatal(err)
			}
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Finish the task"}, 30)
			at := now.Add(-orphanQueuedAfter)
			queue := &TaskQueue{Key: kind, Reason: "Waiting for the project", QueuedAt: at, NextAt: now.Add(time.Minute)}
			p := &TaskPlan{ID: "plan_q", Conversation: "conv1", State: planStarted, CreatedAt: at, UpdatedAt: now,
				Tasks: []PlanTask{{TodoID: todo.ID, Machine: "box", Queue: queue}}}
			if err := s.savePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			s.orphanWatch(ctx, now.Add(-time.Second))
			if o := listedOrphan(t, s, todo.ID); o != nil {
				t.Fatalf("a fresh queue was flagged: %+v", o)
			}
			s.orphanWatch(ctx, now)
			o := listedOrphan(t, s, todo.ID)
			if o == nil || o.Reason != orphanQueued || !o.Since.Equal(at) {
				t.Fatalf("stalled queue missing from to-do: %+v", o)
			}
			s.superviseWatch(ctx, now)
			s.supervise.mu.Lock()
			g := s.supervise.pending[todo.ID+"|"+sigOrphaned]
			s.supervise.mu.Unlock()
			if g.Orphan != orphanQueued || g.Detail != queue.Reason {
				t.Fatalf("stalled queue not escalated: %+v", g)
			}
			e, ok := s.digestEntry(ctx, g, "")
			if !ok || e.Orphan != orphanQueued {
				t.Fatalf("stalled queue dropped from digest: %+v", e)
			}
			if err := s.patchPlanTask(ctx, p.ID, 0, func(task *PlanTask) {
				next := *queue
				next.Attempt++
				task.Queue = &next
			}); err != nil {
				t.Fatal(err)
			}
			s.orphanWatch(ctx, now.Add(time.Minute))
			if again, ok := s.orphanSignal(todoNow(s, todo.ID)); !ok || again.Key != g.Key {
				t.Fatalf("queue retry changed the escalation generation: %+v", again)
			}
			if err := s.patchPlanTask(ctx, p.ID, 0, func(task *PlanTask) { task.Queue = nil }); err != nil {
				t.Fatal(err)
			}
			if e, ok := s.digestEntry(ctx, g, ""); ok {
				t.Fatalf("an old queue signal survived the plan starting: %+v", e)
			}
			f.setSessions("box", proto.Session{SessionID: "s1", Status: proto.StatusWorking})
			if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Working", "/code/app", "claude"); err != nil {
				t.Fatal(err)
			}
			s.orphanWatch(ctx, now.Add(2*time.Minute))
			if o := listedOrphan(t, s, todo.ID); o != nil {
				t.Fatalf("queue remained orphaned after start: %+v", o)
			}
		})
	}
}

// #17878: the owner's check hides neither a gone agent nor its reason. A to-do waiting for the owner's check whose
// agent's tab closed, whose agent ended on an error, or whose computer went away is orphaned and its supervisor's
// signal raised, so findings that went stale meanwhile can still be reviewed (#15578); when the digest tells a routine
// signal during the owner's check is the routine filter's call (supervise_routine.go). A start still queued for such
// a to-do is not orphaned: no start is due while the owner checks (#17090).
func TestUserCheckOrphansGoneAgentsButNotAQueuedStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gone  func(f *fakeHub, a TodoAgent, now time.Time)
		after time.Duration // when orphanWatch looks, after the agent is gone
		want  string
	}{
		{name: "tab closed", gone: func(f *fakeHub, a TodoAgent, now time.Time) {
			f.setSessions(a.MachineID) // its computer is there and no longer lists the agent
		}, after: orphanReconnectWait + time.Minute, want: orphanClosed},
		{name: "ended on an error", gone: func(f *fakeHub, a TodoAgent, now time.Time) {
			c := closedAgent(now.Add(-time.Hour))
			c.MachineID, c.SessionID, c.TurnError = a.MachineID, a.SessionID, "API Error: 400 invalid request"
			f.setSessions(a.MachineID, c)
		}, want: orphanError},
		{name: "computer offline", gone: func(f *fakeHub, a TodoAgent, now time.Time) {
			f.mu.Lock()
			f.devices = []Device{{ID: a.MachineID, Name: a.MachineID}} // not reachable
			f.mu.Unlock()
		}, after: orphanOfflineAfter + time.Minute, want: orphanOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, f, todo := acceptedForUserCheck(t)
			s.stopSuperviseTimers() // keep the supervisor's signal pending while the test reads it
			if got := todo.Supervision.state(todo); got != reviewUserCheck || len(todo.Agents) == 0 {
				t.Fatalf("fixture: state %q, agents %+v; want the owner's check of an agent's work", got, todo.Agents)
			}
			now := time.Now()
			tc.gone(f, todo.Agents[0], now)
			s.orphanWatch(ctx, now)
			s.orphanWatch(ctx, now.Add(tc.after))
			if o := listedOrphan(t, s, todo.ID); o == nil || o.Reason != tc.want {
				t.Fatalf("orphaned during the owner's check = %+v, want %q", o, tc.want)
			}
			s.superviseWatch(ctx, now.Add(tc.after))
			s.supervise.mu.Lock()
			g, ok := s.supervise.pending[todo.ID+"|"+sigOrphaned]
			s.supervise.mu.Unlock()
			if !ok || g.Orphan != tc.want {
				t.Fatalf("supervisor signal = %+v %v, want %q", g, ok, tc.want)
			}
		})
	}

	t.Run("queued start", func(t *testing.T) {
		ctx := t.Context()
		s, _ := newQuietService(t)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		now := time.Now()
		todo, err := s.createTodo(ctx, "Queued task", "Carry out the decision", "conv1", "plan_q")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, todo.ID, "conv1", []string{"Finish the task"}, 30)
		at := now.Add(-orphanQueuedAfter)
		queue := &TaskQueue{Key: workspaceQueueKey, Reason: "Waiting for the project", QueuedAt: at, NextAt: now.Add(time.Minute)}
		p := &TaskPlan{ID: "plan_q", Conversation: "conv1", State: planStarted, CreatedAt: at, UpdatedAt: now,
			Tasks: []PlanTask{{TodoID: todo.ID, Machine: "box", Queue: queue}}}
		if err := s.savePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		s.orphanWatch(ctx, now)
		s.superviseWatch(ctx, now)
		s.supervise.mu.Lock()
		g := s.supervise.pending[todo.ID+"|"+sigOrphaned]
		s.supervise.mu.Unlock()
		if g.Orphan != orphanQueued {
			t.Fatalf("fixture: the stalled queue was not escalated before the owner's check: %+v", g)
		}
		if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewUserCheck; return true }); err != nil {
			t.Fatal(err)
		}
		if e, ok := s.digestEntry(ctx, g, ""); ok {
			t.Fatalf("the queued-start signal reached the digest during the owner's check: %+v", e)
		}
		s.orphanWatch(ctx, now.Add(time.Minute))
		if o := listedOrphan(t, s, todo.ID); o != nil {
			t.Fatalf("a queued start is orphaned during the owner's check: %+v", o)
		}
	})
}
