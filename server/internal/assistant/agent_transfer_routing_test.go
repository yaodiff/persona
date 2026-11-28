package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func transferPlannedTask(t *testing.T, s *Service, f *fakeHub, task Todo, destination string) {
	t.Helper()
	a := task.Agents[0]
	f.setSessions(a.MachineID, proto.Session{SessionID: a.SessionID, MachineID: a.MachineID, Status: proto.StatusWorking})
	if _, err := s.toolAgentTransfer(t.Context(), "conv1", transferArgs(a.MachineID, a.SessionID, destination)); err != nil {
		t.Fatal(err)
	}
}

// A lead owns its new plan. Moving it through main and back must not make its saved plan look like a legacy
// assignment or undo the explicit move on reload.
func TestAgentTransferPlanSignalsAndReload(t *testing.T) {
	database := todoTestDB(t)
	s, f, task := newLeaderServiceFromAgentNumberOn(t, database, 999999)
	if task.Origin != "lead1" || task.PlanID == "" || task.Supervision.Supervisor != "lead1" || task.Supervision.SupervisorSource != "plan" {
		t.Fatalf("new lead plan changed its default: %+v", task)
	}
	settleService(t, s)
	transferPlannedTask(t, s, f, task, "conv1")
	transferPlannedTask(t, s, f, task, "lead1")
	transferPlannedTask(t, s, f, task, "lead1") // same assignment also persists its provenance
	kinds := append(slices.Clone(backgroundSignals), sigEstimateDue, sigStarted, sigFailed, sigTodoReady, sigParentCloseout, sigHandoffResult, sigLeaderStuck)
	kinds = slices.DeleteFunc(kinds, func(kind string) bool { return kind == sigAsk })
	check := func(s *Service) {
		t.Helper()
		for _, state := range []string{"", reviewNeedsUser, reviewPending} {
			if _, _, err := s.updateSupervision(t.Context(), task.ID, func(v *Supervision) bool { v.State = state; return true }); err != nil {
				t.Fatal(err)
			}
			for _, kind := range kinds {
				for _, pinned := range []string{"", "conv1"} {
					g := superviseSignal{Todo: task.ID, Kind: kind, To: pinned}
					cfg := s.settings(t.Context())
					if got := s.digestTarget(t.Context(), cfg, g); got != "lead1" {
						t.Errorf("immediate %s/%s pinned=%s: %s", state, kind, pinned, got)
					}
					if got := s.routeTo(t.Context(), cfg, g, func(string) bool { return false }); got != "lead1" {
						t.Errorf("unavailable explicit supervisor for %s: %s", kind, got)
					}
					if got := s.recheckDigestTarget(t.Context(), cfg, g); got != "lead1" {
						t.Errorf("queued %s/%s pinned=%s: %s", state, kind, pinned, got)
					}
				}
			}
		}
		for _, kind := range []string{sigEscalated} {
			if got := s.digestTo(t.Context(), s.settings(t.Context()), superviseSignal{Todo: task.ID, Kind: kind}); got != "conv1" {
				t.Errorf("explicit escalation %s went to %s", kind, got)
			}
		}
	}
	check(s)
	again, _ := newQuietServiceOn(t, database)
	if err := again.ensureTodosLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(again, task.ID).Supervision; got.Supervisor != "lead1" || got.SupervisorSource != "explicit" {
		t.Fatalf("reload overwrote explicit assignment: %+v", got)
	}
	check(again)
}

// Actual sends and queue drains must agree with the route table and must not repeat a queued card on restore.
func TestAgentTransferPendingCardsBeforeAfterAndRestart(t *testing.T) {
	for _, kind := range []string{proto.IxQuestion, proto.IxPermission, proto.IxPlan} {
		t.Run(kind, func(t *testing.T) {
			database := todoTestDB(t)
			e := newRouteEnv(t, database, true)
			s := e.s
			// Start with a main-owned task; hold its digest while main is busy.
			if _, _, err := s.updateSupervision(t.Context(), e.todo.ID, func(v *Supervision) bool { v.Supervisor = "conv1"; return true }); err != nil {
				t.Fatal(err)
			}
			e.supervisorState(proto.StatusWorking, proto.StatusWorking)
			card := func(id string) proto.Interaction {
				ix := askQuestion(id, "worker-1")
				if kind != proto.IxQuestion {
					ix = askPermission(id, "worker-1")
					ix.Kind = kind
				}
				ix.Resumable = true
				return ix
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			waits := make(chan struct{}, 2)
			raise := func(ix proto.Interaction) {
				go func() { _, _ = s.waitInteraction(ctx, ix); waits <- struct{}{} }()
				e.waitPendingID(ix.ID)
			}
			raise(card("before"))
			waitFor(t, "main's queued card", func() bool { return s.queuedFor("conv1") == 1 })
			transferPlannedTask(t, s, e.f, todoNow(s, e.todo.ID), "lead1")
			transferPlannedTask(t, s, e.f, todoNow(s, e.todo.ID), "lead1")
			if _, ok := s.routedTo("before", "lead1", false); !ok {
				t.Fatal("card pending before transfer kept its old route")
			}
			if _, _, err := s.updateSupervision(t.Context(), e.todo.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
				t.Fatal(err)
			}
			raise(card("after"))
			waitFor(t, "new card's route", func() bool { _, ok := s.routedTo("after", "lead1", false); return ok })
			// Let any gather finish while both supervisors are still busy, then restart with the durable queue.
			e.settle()
			cancel()
			<-waits
			<-waits
			s2 := newRestartedService(t, Options{Hub: e.f, Store: func() Store { return database }, Devices: e.d, Logf: t.Logf})
			shortSupervise(t, s2)
			noDirectionsTimers(s2)
			s2.FleetUp(t.Context())
			for _, id := range []string{"before", "after"} {
				if _, ok := s2.routedTo(id, "lead1", false); !ok {
					t.Fatalf("restored %s did not keep the transferred supervisor", id)
				}
			}
			drain := func(id string) {
				idle := proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusIdle}
				e.f.setSessions("server", idle, proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
				s2.drainRelays("server", []proto.Session{idle})
				waitFor(t, "drain "+id, func() bool { s2.relays.mu.Lock(); defer s2.relays.mu.Unlock(); return !s2.relays.draining[id] })
			}
			drain("conv1")
			drain("lead1")
			settleService(t, s2)
			drain("conv1")
			drain("lead1")
			if got := e.f.noticesTo("conv1"); len(got) != 0 {
				t.Fatalf("old supervisor received transferred cards: %q", got)
			}
			notices := strings.Join(e.f.noticesTo("lead1"), "\n")
			for _, id := range []string{"before", "after"} {
				if n := strings.Count(notices, "interaction_id="+id); n != 1 {
					t.Fatalf("card %s delivered %d times: %s", id, n, notices)
				}
			}
			if n := e.pushesOf("before", "after"); n != 0 {
				t.Fatalf("transfer pushed %d cards to the owner", n)
			}
		})
	}
}

func TestAgentTransferMovesDeliveredPendingCardsOnceAndKeepsOwnerEscalation(t *testing.T) {
	for _, kind := range []string{proto.IxQuestion, proto.IxPermission, proto.IxPlan} {
		t.Run(kind, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			ix := askQuestion("delivered", "worker-1")
			if kind != proto.IxQuestion {
				ix = askPermission("delivered", "worker-1")
				ix.Kind = kind
			}
			e.raise(ix)
			e.noticeWith("lead1", "interaction_id=delivered")
			e.settle()
			e.s.fleet.mu.Lock()
			deadline := e.s.fleet.interactions[ix.ID].route.routedAt
			e.s.fleet.mu.Unlock()
			transferPlannedTask(t, e.s, e.f, todoNow(e.s, e.todo.ID), "conv1")
			e.noticeWith("conv1", "interaction_id=delivered")
			transferPlannedTask(t, e.s, e.f, todoNow(e.s, e.todo.ID), "conv1")
			e.settle()
			if _, ok := e.s.routedTo(ix.ID, "conv1", false); !ok {
				t.Fatal("pending delivered card kept its former supervisor")
			}
			if n := strings.Count(strings.Join(e.f.noticesTo("conv1"), "\n"), "interaction_id=delivered"); n != 1 {
				t.Fatalf("transferred card delivered %d times", n)
			}
			e.s.fleet.mu.Lock()
			if !e.s.fleet.interactions[ix.ID].route.routedAt.Equal(deadline) {
				t.Error("transfer extended absolute approval deadline")
			}
			e.s.fleet.mu.Unlock()
			e.s.ringAsks(e.todo.ID, []string{ix.ID}, nil, escalation{})
			transferPlannedTask(t, e.s, e.f, todoNow(e.s, e.todo.ID), "lead1")
			if _, ok := e.s.routedTo(ix.ID, "lead1", false); ok {
				t.Fatal("owner-held card was taken back")
			}
			e.settle()
		})
	}
}

func TestAgentTransferDoesNotWaitForBusyFormerSupervisor(t *testing.T) {
	e := newRouteEnv(t, todoTestDB(t), true)
	if _, _, err := e.s.updateSupervision(t.Context(), e.todo.ID, func(v *Supervision) bool { v.Supervisor = "conv1"; return true }); err != nil {
		t.Fatal(err)
	}
	e.supervisorState(proto.StatusWorking, proto.StatusIdle)
	e.raise(askQuestion("queued", "worker-1"))
	waitFor(t, "old supervisor queue", func() bool { return e.s.queuedFor("conv1") == 1 })
	transferPlannedTask(t, e.s, e.f, todoNow(e.s, e.todo.ID), "lead1")
	e.noticeWith("lead1", "interaction_id=queued")
	e.settle()
	if e.s.queuedFor("conv1") != 0 || len(e.f.noticesTo("conv1")) != 0 {
		t.Fatal("old supervisor retained transferred queue")
	}
}

func TestAgentTransferQueuedOrdinarySignalOnlyReachesNewSupervisor(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Transferred task", "Unchanged task")
	f.known["lead1"] = true
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	if _, _, err := s.updateSupervision(t.Context(), tasks[0].ID, func(v *Supervision) bool {
		v.Supervisor, v.SupervisorSource = "lead1", "explicit"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	drainQueuedDigest(t, s, f)
	if got := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(got, "Transferred task") || !strings.Contains(got, "Unchanged task") {
		t.Fatalf("old supervisor's mixed digest: %s", got)
	}
	idle := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", idle)
	for range 2 {
		drainRelayTest(t, s, idle)
	}
	got := strings.Join(f.noticesTo("lead1"), "\n")
	if strings.Count(got, "Transferred task") != 2 || strings.Contains(got, "Unchanged task") {
		// The fixture has two distinct signals for each task, carried in one message.
		t.Fatalf("new supervisor's signals: %s", got)
	}
	if len(f.noticesTo("lead1")) != 1 {
		t.Fatal("queued signals were delivered more than once")
	}
}

// Persisted provenance has a stable JSON name and is carried by the database row, not a process-local flag.
func TestAgentTransferAssignmentRowRoundTrip(t *testing.T) {
	v := &Supervision{Supervisor: "lead1", SupervisorSource: "explicit", UpdatedAt: time.Now()}
	back := supervisionFromRow(v.row("task"))
	raw, err := json.Marshal(back)
	if err != nil || back.SupervisorSource != "explicit" || !strings.Contains(string(raw), `"supervisor_source":"explicit"`) {
		t.Fatalf("assignment lost: %s %v", raw, err)
	}
}

func TestAgentTransferDeliversIssueSignalsOnlyOnceAcrossRestart(t *testing.T) {
	for _, mode := range []string{"planned_by_lead", "transferred_from_main"} {
		for _, kind := range []string{sigEstimateDue, sigReport, sigIdle, sigReview, sigUndecidable, sigAllStopped, sigOrphaned, sigFailed, sigStarted} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				database := todoTestDB(t)
				caller := "lead1"
				if mode == "transferred_from_main" {
					caller = "conv1"
				}
				s, f, task := newConversationPlanServiceOn(t, database, caller, 999999)
				settleService(t, s)
				if mode == "transferred_from_main" {
					transferPlannedTask(t, s, f, task, "lead1")
				}
				g := superviseSignal{Todo: task.ID, Kind: kind, Key: "transferred-" + kind, Detail: "Evidence for the transferred supervisor"}
				state := reviewNeedsUser
				switch kind {
				case sigEstimateDue:
					state = ""
					brief, ok := s.briefOfTodo(task.ID)
					if !ok {
						t.Fatal("plan has no estimate brief")
					}
					g.Key, g.Minutes = estimateDueKey(brief), brief.ExpectNow
				case sigReport:
					g.Report = upReportQuestion
				case sigAllStopped:
					now := time.Now()
					_, agents := stoppedFixture(now.Add(-time.Minute))
					agents = agents[:1]
					agents[0].SessionID, agents[0].MachineID = task.Agents[0].SessionID, task.Agents[0].MachineID
					f.setSessions(task.Agents[0].MachineID, agents...)
					var ok bool
					g, ok = allStoppedSignal(todoNow(s, task.ID), nil, agents, now)
					if !ok {
						t.Fatal("planned task did not produce all_stopped")
					}
				case sigIdle:
					state = ""
				case sigOrphaned:
					state = ""
					s.orphans.mu.Lock()
					s.orphans.now = map[string]TodoOrphan{task.ID: {Reason: orphanOffline, key: g.Key}}
					s.orphans.mu.Unlock()
				case sigReview:
					state = reviewPending
				}
				if _, _, err := s.updateSupervision(t.Context(), task.ID, func(v *Supervision) bool { v.State = state; return true }); err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				f.sends = nil
				f.mu.Unlock()
				s.signal(g)
				waitFor(t, "transferred "+kind, func() bool { return len(f.noticesTo("lead1")) == 1 })
				settleService(t, s)
				if got := f.noticesTo("conv1"); len(got) != 0 {
					t.Fatalf("main received %s after transfer: %q", kind, got)
				}
				again := newRestartedService(t, Options{Hub: f, Store: func() Store { return database }, Logf: t.Logf})
				shortSupervise(t, again)
				noDirectionsTimers(again)
				if err := again.ensureTodosLoaded(t.Context()); err != nil {
					t.Fatal(err)
				}
				if kind == sigOrphaned {
					again.orphans.mu.Lock()
					again.orphans.now = map[string]TodoOrphan{task.ID: {Reason: orphanOffline, key: g.Key}}
					again.orphans.mu.Unlock()
				}
				again.signal(g)
				settleService(t, again)
				if len(f.noticesTo("lead1")) != 1 || len(f.noticesTo("conv1")) != 0 {
					t.Fatalf("%s repeated or changed recipients after restart: main=%q lead=%q", kind, f.noticesTo("conv1"), f.noticesTo("lead1"))
				}
			})
		}
	}
}

func TestAgentTransferInDoubtCardKeepsRelayIdentityAndMovesPendingQuestion(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	if _, _, err := e.s.updateSupervision(t.Context(), e.todo.ID, func(v *Supervision) bool { v.Supervisor = "conv1"; return true }); err != nil {
		t.Fatal(err)
	}
	e.supervisorState(proto.StatusWorking, proto.StatusIdle)
	e.raise(askQuestion("in-doubt", "worker-1"))
	waitFor(t, "queued question", func() bool { return e.s.queuedFor("conv1") == 1 })
	e.s.relays.mu.Lock()
	original := e.s.relays.st.Queue["conv1"][0]
	original.TriedAt = time.Now()
	e.s.relays.st.Queue["conv1"][0] = original
	e.s.relays.mu.Unlock()
	transferPlannedTask(t, e.s, e.f, todoNow(e.s, e.todo.ID), "lead1")
	e.noticeWith("lead1", "interaction_id=in-doubt")
	e.settle()
	e.s.relays.mu.Lock()
	queue := slices.Clone(e.s.relays.st.Queue["conv1"])
	e.s.relays.mu.Unlock()
	if len(queue) != 1 || queue[0] != original {
		t.Fatal("transfer changed an uncertain relay's identity")
	}
	if _, stale, keep := e.s.recheckQueuedDigest(t.Context(), "conv1", original); !stale || keep {
		t.Fatal("old uncertain card could be replayed after the current supervisor received it")
	}
	if len(e.f.noticesTo("conv1")) != 0 {
		t.Fatal("old supervisor received another card after transfer")
	}
}

func TestAgentTransferQueuedReportDoesNotRepeatAfterRestart(t *testing.T) {
	database := todoTestDB(t)
	s, f, task := newConversationPlanServiceOn(t, database, "conv1", 999999)
	settleService(t, s)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}, proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	g := superviseSignal{Todo: task.ID, Kind: sigReport, Report: upReportQuestion, Key: "queued-report", Detail: "A pending question for the task supervisor"}
	s.signal(g)
	waitFor(t, "main's pending report", func() bool { return s.queuedFor("conv1") == 1 })
	transferPlannedTask(t, s, f, task, "lead1")
	waitFor(t, "transferred queued report", func() bool { return len(f.noticesTo("lead1")) == 1 })
	settleService(t, s)
	again := newRestartedService(t, Options{Hub: f, Store: func() Store { return database }, Logf: t.Logf})
	shortSupervise(t, again)
	noDirectionsTimers(again)
	if err := again.ensureTodosLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	again.signal(g)
	settleService(t, again)
	if len(f.noticesTo("lead1")) != 1 || len(f.noticesTo("conv1")) != 0 {
		t.Fatal("queued report changed destination or repeated after restart")
	}
}
