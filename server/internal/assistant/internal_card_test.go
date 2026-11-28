package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	preparedapproval "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// The source contract is the same for a waiting tool and a durable asynchronous
// question, including one raised by a direction lead or the main conversation.
func TestInternalAskUserSourceMatrix(t *testing.T) {
	for _, wait := range []bool{true, false} {
		for _, source := range []string{"worker-1", "lead1", "conv1"} {
			t.Run(fmt.Sprintf("wait=%t/%s", wait, source), func(t *testing.T) {
				e := newRouteEnv(t, nil, true)
				setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
				e.f.mu.Lock()
				e.f.known["bg1"] = true
				e.f.mu.Unlock()
				convs := []proto.Session{{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
					{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
					{SessionID: "bg1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle}}
				e.sessions("server", convs...)
				e.f.setSessions("server", convs...)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := e.s.toolAskUser(ctx, "mac", source, json.RawMessage(fmt.Sprintf(`{"decision_scope":"internal","wait":%t,"questions":[{"question":"Which verification shard?","options":[{"label":"Affected"},{"label":"All"}]}]}`, wait)))
					done <- err
				}()
				var ix proto.Interaction
				waitFor(t, "question source published", func() bool {
					for _, q := range e.s.pendingQuestions() {
						if q.SessionID == source {
							ix = q
							_, up := e.s.pendingIx(q.ID)
							return up
						}
					}
					return false
				})
				to := "conv1"
				if source == "worker-1" {
					to = "lead1"
				}
				if source == "conv1" {
					to = "bg1"
				}
				waitFor(t, "active internal route", func() bool {
					return hasRelayFrom(e.s, to, cardAskUser, "interaction_id="+ix.ID)
				})
				e.noticeWith(to, "interaction_id="+ix.ID)
				if !wait {
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					e.s.askLaters.mu.Lock()
					kept := e.s.askLaters.list[0].Interaction
					e.s.askLaters.mu.Unlock()
					if !kept.Resumable || !strings.Contains(string(kept.ToolInput), `"decision_scope":"internal"`) {
						t.Fatal("async rights were not persisted")
					}
				}
				args := map[string]any{"machine": "mac", "session_id": source, "interaction_id": ix.ID,
					"answers": map[string]string{"Which verification shard?": "Affected"}, "reason": "Use the affected shards"}
				if _, err := runJSON(t, e.s, source, "interaction_answer", args); err == nil {
					t.Fatal("source answered its own internal question")
				}
				if _, err := runJSON(t, e.s, to, "interaction_answer", args); err != nil {
					t.Fatal(err)
				}
				if wait {
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				waitFor(t, "question reached terminal state", func() bool { return len(e.s.pendingQuestions()) == 0 })
				if e.pushesOf(ix.ID) != 0 {
					t.Fatal("internal source pushed to owner")
				}
			})
		}
	}
}

func TestAskUserOwnerDecisionSources(t *testing.T) {
	for _, wait := range []bool{true, false} {
		for _, tc := range []struct {
			meta   string
			source string
		}{
			{`"owner_action":"personal_unlock"`, cardSecretForm},
			{`"owner_action":"login"`, cardSecretForm},
			{`"owner_action":"spending"`, cardPlanMoney},
			{`"why_owner":"visible_to_others"`, cardDecisionOutward},
			{`"why_owner":"irreversible"`, cardDecisionIrreversible},
			{`"why_owner":"priority"`, cardDecisionDirection},
			{`"why_owner":"only_owner_knows"`, cardDecisionOwnerKnows},
		} {
			t.Run(fmt.Sprintf("wait=%t/%s", wait, tc.meta), func(t *testing.T) {
				e := newRouteEnv(t, nil, true)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				args := json.RawMessage(fmt.Sprintf(`{%s,"wait":%t,"questions":[{"question":"Owner action required","options":[{"label":"Done"},{"label":"Cancel"}]}]}`, tc.meta, wait))
				done := make(chan error, 1)
				go func() {
					_, err := e.s.toolAskUser(ctx, "server", "lead1", args)
					done <- err
				}()
				var ix proto.Interaction
				waitFor(t, "owner action remains visible", func() bool {
					qs := e.s.pendingQuestions()
					if len(qs) != 1 {
						return false
					}
					ix = qs[0]
					return e.pushesOf(ix.ID) == 1
				})
				if got := e.s.inputCardSource(ix, false); got != tc.source || e.s.internalCard(ix) {
					t.Fatalf("owner source = %q, want %q", got, tc.source)
				}
				if err := e.s.resolve(ix.ID, proto.Resolution{Cancel: true}); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				waitFor(t, "owner cancellation delivered", func() bool { return len(e.s.pendingQuestions()) == 0 })
			})
		}
	}
	e := newRouteEnv(t, nil, true)
	if _, err := e.s.toolAskUser(context.Background(), "server", "lead1", json.RawMessage(`{"questions":[{"question":"Unclassified"}]}`)); err == nil {
		t.Fatal("unclassified ask_user reached a card")
	}
}

func TestSupervisorMaintenanceHasActiveWakeAndAnswer(t *testing.T) {
	for _, source := range []string{"", "lead1"} {
		t.Run(source, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			ix := askPermission("ix_maintenance", source)
			ix.MachineID, ix.SessionTitle, ix.ToolName = "server", "Persona", "enkeep_rerun"
			ix.ToolInput = json.RawMessage(`{"from":"2026-10-20","to":"2026-10-22","force":true}`)
			done := e.raise(ix)
			e.noticeWith("conv1", "interaction_id="+ix.ID)
			if e.pushesOf(ix.ID) != 0 {
				t.Fatal("internal force rerun pushed to owner")
			}
			if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{"machine": "server", "session_id": source,
				"interaction_id": ix.ID, "allow": true, "reason": "Reviewed force and duplicate execution constraints"}); err != nil {
				t.Fatal(err)
			}
			if got := answerOf(t, done); !proto.MaintenanceAllowed(ix, got, time.Now()) {
				t.Fatalf("maintenance answer lost its authority: %+v", got)
			}
		})
	}
}

func TestInternalRouteRestoresAndRejectsStaleRelay(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.supervisorState(proto.StatusWorking, proto.StatusIdle)
	ix := askPermission("ix_lead_restore", "lead1")
	ix.MachineID = "server"
	ix.ToolInput = json.RawMessage(`{"command":"ls /tmp | head"}`)
	done := e.raise(ix)
	var old relayMsg
	waitFor(t, "first active relay queued", func() bool {
		for _, msg := range queuedRelays(e.s, "conv1") {
			if msg.Card == ix.ID {
				old = msg
				return true
			}
		}
		return false
	})
	e.s.fleet.mu.Lock()
	e.s.fleet.interactions[ix.ID].route = nil
	e.s.fleet.mu.Unlock()
	if !e.s.restoreRoute(context.Background(), ix) {
		t.Fatal("restored supervisor permission lost its route")
	}
	if _, ok := e.s.pendingStandalonePermission("conv1", old, time.Now()); ok {
		t.Fatal("pre-restart relay can still act on new route")
	}
	e.supervisorState(proto.StatusIdle, proto.StatusIdle)
	e.noticeWith("conv1", "interaction_id="+ix.ID)
	if err := e.s.resolve(ix.ID, proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}); err != nil {
		t.Fatal(err)
	}
	got := answerOf(t, done)
	if got.Allow != nil || got.Outcome() != proto.OutcomeExpired || e.pushesOf(ix.ID) != 0 {
		t.Fatalf("expiry authorized or pushed: %+v", got)
	}
}

func TestInternalAsyncTerminalReceiptNeverClaimsOwnerAnswer(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	for _, terminal := range []string{"answered", "cancelled", "expired"} {
		ix, _ := e.askLater(`{"decision_scope":"internal","wait":false,"questions":[{"question":"Which shard?","options":[{"label":"Affected"},{"label":"All"}]}]}`)
		r := proto.Resolution{AnsweredBy: proto.AnsweredByAssistant, Answers: map[string]string{"Which shard?": "Affected"}}
		if terminal == "cancelled" {
			r = proto.Resolution{Cancel: true}
		}
		if terminal == "expired" {
			r = proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}
		}
		text := e.s.renderAskLater(context.Background(), ix, r)
		if !strings.Contains(text, "internal receipt") {
			t.Fatalf("dishonest internal receipt: %s", text)
		}
		if terminal != "answered" && !strings.Contains(text, "Nothing was authorized") {
			t.Fatalf("unanswered question authorized a default: %s", text)
		}
		e.mustEnd(ix.ID, `{"cancel":true}`)
	}
}

func TestQuietRemindNeverConstructsOwnerNotification(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		for _, nth := range []int{1, 2} {
			e := newRouteEnv(t, nil, true)
			e.s.pushQuiet.mu.Lock()
			e.s.pushQuiet.force = &quiet
			e.s.pushQuiet.mu.Unlock()
			ix := askQuestion("ix_quiet_internal", "worker-1")
			e.s.quietRemind(e.todo.ID, "lead1", ix, nth)
			e.settle()
			if len(workEventsOf(e.s, "ask_quiet_reminded")) != 1 {
				t.Fatal("supervisor reminder audit was lost")
			}
			if len(e.f.pushed()) != 0 || cardsToday(t, e.s)[cardAskHeld].N != 0 || cardsToday(t, e.s)[cardAskHeld].Held != 0 {
				t.Fatal("internal held reminder constructed an owner notification")
			}
		}
	}
}

func TestInternalAsyncQuestionRestoresFromDurableRecord(t *testing.T) {
	for _, terminal := range []string{"answer", "cancel", "expire"} {
		t.Run(terminal, func(t *testing.T) {
			ctx := t.Context()
			e := newRouteEnv(t, nil, true)
			sending := make(chan struct{})
			send, resume := context.WithCancel(ctx)
			defer resume()
			e.f.mu.Lock()
			e.f.beforeAssistantSend = func() {
				close(sending)
				<-send.Done()
			}
			e.f.mu.Unlock()

			if _, err := e.s.toolAskUser(ctx, "mac", "worker-1", json.RawMessage(`{"decision_scope":"internal","wait":false,"questions":[{"question":"Which shard after restart?","options":[{"label":"Affected"},{"label":"All"}]}]}`)); err != nil {
				t.Fatal(err)
			}
			qs := e.s.pendingQuestions()
			if len(qs) != 1 {
				t.Fatal("async source did not persist a question")
			}
			ix := qs[0]
			t.Cleanup(func() { _ = e.s.resolve(ix.ID, proto.Resolution{Cancel: true}) })

			select {
			case <-sending:
			case <-time.After(5 * time.Second):
				t.Fatal("original supervisor wake did not reach the transport")
			}
			if msg, ok := storedInternalQuestionWake(t, e.st, ix.ID); !ok || msg.TriedAt.IsZero() {
				t.Fatalf("no durable in-flight supervisor wake for %s", ix.ID)
			}
			inFlight := snapshotInternalQuestion(t, e.st, ix.ID)
			resume()
			e.noticeWith("lead1", "interaction_id="+ix.ID)
			// Observing the send does not mean the durable queue head has left yet.
			waitFor(t, "original supervisor wake durably dequeued", func() bool {
				_, queued := storedInternalQuestionWake(t, e.st, ix.ID)
				return !queued
			})
			drained := snapshotInternalQuestion(t, e.st, ix.ID)
			t.Run("in-flight", func(t *testing.T) {
				checkRestoredInternalQuestion(t, e, inFlight, ix.ID, terminal)
			})
			t.Run("drained", func(t *testing.T) {
				checkRestoredInternalQuestion(t, e, drained, ix.ID, terminal)
			})
		})
	}
}

func storedInternalQuestionWake(t *testing.T, store *fleetMem, id string) (relayMsg, bool) {
	t.Helper()
	var state relayState
	if found, err := store.Setting(t.Context(), relayKey, &state); err != nil || !found {
		t.Fatalf("read persisted relay queue: found=%t, err=%v", found, err)
	}
	for _, msg := range state.Queue["lead1"] {
		if msg.Card == id {
			return msg, true
		}
	}
	return relayMsg{}, false
}

func snapshotInternalQuestion(t *testing.T, store *fleetMem, id string) *restoreMem {
	t.Helper()
	ctx := t.Context()
	restarted := &restoreMem{newFleetMem()}
	values, err := store.SettingsPrefix(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for key, raw := range values {
		restarted.setRawSetting(key, raw)
	}
	rec, err := store.InteractionRecord(ctx, id)
	if err != nil || rec == nil || rec.Status != "pending" || !rec.Interaction.Resumable {
		t.Fatalf("source question was not durable: %+v, %v", rec, err)
	}
	if err := restarted.InsertInteraction(ctx, rec.Interaction); err != nil {
		t.Fatal(err)
	}
	return restarted
}

func checkRestoredInternalQuestion(t *testing.T, e *routeEnv, restarted *restoreMem, id, terminal string) {
	t.Helper()
	ctx := t.Context()
	next := &routeEnv{fleetTest: newFleetTestOn(t, restarted)}
	noDirectionsTimers(next.s)
	next.f.withMachine()
	next.f.known["conv1"], next.f.known["lead1"] = true, true
	next.d.add("server", "Server")
	for _, machine := range []string{"server", "mac"} {
		list, _ := e.d.Sessions(machine)
		next.d.setSessions(machine, list)
		next.f.setSessions(machine, list...)
	}
	// fleetMem keeps to-dos in memory. Seed the existing task and its worker links before
	// recovery, when a real manager can already read them from the database.
	putTodo(next.s, todoNow(e.s, e.todo.ID))
	next.s.restoreInteractions(ctx)
	next.s.loadAskLater(ctx)
	next.s.publish(ctx)
	waitFor(t, "durable internal question restored", func() bool {
		qs := next.s.pendingQuestions()
		p, ok := next.s.pendingIx(id)
		return len(qs) == 1 && qs[0].ID == id && qs[0].Resumable && ok && next.s.internalCard(p)
	})
	// The idle supervisor should wake on startup, before the periodic recovery sweep.
	next.noticeWith("lead1", "interaction_id="+id)

	r := proto.Resolution{Answers: map[string]string{"Which shard after restart?": "Affected"}, AnsweredBy: proto.AnsweredByAssistant}
	if terminal == "cancel" {
		r = proto.Resolution{Cancel: true}
	}
	if terminal == "expire" {
		r = proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}
	}
	if err := next.s.resolve(id, r); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restored terminal result returned", func() bool { return len(next.s.pendingQuestions()) == 0 })
	next.s.askLaters.mu.Lock()
	kept := len(next.s.askLaters.list)
	next.s.askLaters.mu.Unlock()
	if kept != 0 || next.pushesOf(id) != 0 {
		t.Fatal("terminal question retained or pushed to owner")
	}
	if err := next.s.resolve(id, r); err == nil {
		t.Fatal("terminal question accepted a second answer")
	}
}

func TestMainCreatesIndependentInternalReviewer(t *testing.T) {
	s, _ := newQuietService(t)
	s.noBackground = false
	cfg := s.settings(context.Background())
	ix := proto.Interaction{MachineID: cfg.MachineID, SessionID: cfg.Main, Kind: proto.IxPermission, ToolName: "Read"}
	to, home := s.internalReviewer(context.Background(), ix)
	if to == "" || to == cfg.Main || home == "" || s.settings(context.Background()).BackgroundSupervisor != to {
		t.Fatalf("root reviewer was not independently registered: to=%q home=%q", to, home)
	}
}

func TestInternalRequestWithoutReviewerEndsUnanswered(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	setSettings(t, e.s, func(v *Settings) {
		v.Main, v.BackgroundSupervisor, v.Leaders = "", "", nil
	})
	ix := askPermission("ix_no_reviewer", "standalone-agent")
	ix.ToolInput = json.RawMessage(`{"command":"ls /tmp | head"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := e.s.waitInteraction(ctx, ix)
	if err != nil || r.Allow != nil || !r.Dismiss || r.Reason != "internal_reviewer_unavailable" {
		t.Fatalf("unavailable reviewer did not close unanswered: %+v %v", r, err)
	}
	if e.pushesOf(ix.ID) != 0 || len(workEventsOf(e.s, "ask_internal_unavailable")) != 1 {
		t.Fatal("unavailable reviewer pushed to owner or lost its terminal audit")
	}
	text, err := e.s.toolAskUser(ctx, "server", "standalone-agent", json.RawMessage(`{"decision_scope":"internal","questions":[{"question":"Which verification?","options":[{"label":"Read"},{"label":"Wait"}]}]}`))
	if err != nil || !strings.Contains(text, "internal question") || !strings.Contains(text, "Nothing was authorized") || strings.Contains(text, "The user") {
		t.Fatalf("internal unavailable receipt blamed the owner: %q, %v", text, err)
	}
	if len(e.s.pendingQuestions()) != 0 || len(e.f.pushed()) != 0 {
		t.Fatal("unavailable internal question remained pending or pushed to the owner")
	}
}

func TestInternalQuestionRetainsCredentialConstraintWithoutRequestingIt(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	args := json.RawMessage(`{"decision_scope":"internal","wait":false,"questions":[{"question":"Which verification can proceed without private key access?","options":[{"label":"Local checks"},{"label":"Wait"}]}]}`)
	if _, err := e.s.toolAskUser(context.Background(), "server", "lead1", args); err != nil {
		t.Fatal(err)
	}
	qs := e.s.pendingQuestions()
	if len(qs) != 1 || questionOwnerSource(qs[0]) != "" || !strings.Contains(string(qs[0].ToolInput), "without private key access") {
		t.Fatal("credential constraint was discarded or reclassified as a personal action")
	}
	e.noticeWith("conv1", "interaction_id="+qs[0].ID)
	if e.pushesOf(qs[0].ID) != 0 {
		t.Fatal("internal credential constraint pushed to owner")
	}
	if err := e.s.resolve(qs[0].ID, proto.Resolution{Cancel: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "internal constraint reached terminal state", func() bool { return len(e.s.pendingQuestions()) == 0 })
}

func TestMainInternalReviewerIsIndependentOfItsSourceTask(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor = "bg1" })
	ctx := context.Background()
	if err := e.s.linkTodoAgent(ctx, e.todo.ID, "server", "conv1", "Source conversation", assistantHome, "claude"); err != nil {
		t.Fatal(err)
	}
	ix := askPermission("ix_main_on_task", "conv1")
	ix.MachineID = "server"
	to, home := e.s.internalReviewer(ctx, ix)
	if to != "bg1" || home != "server" {
		t.Fatalf("main approval followed a lower task supervisor: %q on %q", to, home)
	}
	setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisorMachineID = "review-machine" })
	to, home = e.s.internalReviewer(ctx, ix)
	if to != "bg1" || home != "review-machine" {
		t.Fatalf("background reviewer migration lost its machine: %q on %q", to, home)
	}
	setSettings(t, e.s, func(v *Settings) { v.MainMachineID = "main-machine" })
	ix.SessionID = "lead1"
	to, home = e.s.internalReviewer(ctx, ix)
	if to != "conv1" || home != "main-machine" {
		t.Fatalf("upward approval ignored main's confirmed machine: %q on %q", to, home)
	}
}

// The prepared entry must share one-shot maintenance authority and never turn an
// expired, remembered or synthetic answer into an Apply callback.
func TestPreparedMaintenanceRequiresFreshDecision(t *testing.T) {
	for _, mode := range []string{"owner", "assistant", "remembered", "default", "terminal", "late", "cancel", "expired"} {
		t.Run(mode, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			ix := askPermission("ix_prepared_maintenance", "lead1")
			ix.MachineID, ix.SessionTitle, ix.ToolName = "server", "Persona", "enkeep_rerun"
			ix.ToolInput = json.RawMessage(`{"force":true}`)
			applied := make(chan struct{}, 1)
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			go func() {
				_, err := e.s.waitPreparedCard(ctx, ix, preparedapproval.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
					applied <- struct{}{}
					return "applied", nil
				}, Unanswered: "nothing executed"})
				done <- err
			}()
			e.noticeWith("conv1", "interaction_id="+ix.ID)
			yes := true
			r := proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant}
			switch mode {
			case "owner":
				r.AnsweredBy = ""
			case "remembered":
				r.AnsweredBy, r.Remembered = "", true
			case "default":
				r.AnsweredBy = proto.AnsweredByDefault
			case "terminal":
				r.AnsweredBy = proto.AnsweredByTerminal
			case "late":
				r.Late = true
			case "cancel":
				r = proto.Resolution{Dismiss: true, Reason: proto.DismissedWithdrawn}
			case "expired":
				r.Dismiss, r.Reason = true, proto.DismissedExpired
			}
			if err := e.s.resolve(ix.ID, r); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "owner" || mode == "assistant" {
				want = 1
			}
			if len(applied) != want || e.pushesOf(ix.ID) != 0 {
				t.Fatalf("prepared maintenance applied %d times, want %d; owner pushes %d", len(applied), want, e.pushesOf(ix.ID))
			}
		})
	}
}

// A generic extension source is not an authorization claim. Only the explicit
// maintenance operation can share the supervisor execution gate.
func TestExtensionOwnerOperationCannotEnterMaintenanceRoute(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	for _, tool := range []string{"enkeep_delete", "enkeep_unknown", "subscription_purchase", "agent_autoname"} {
		ix := askPermission("ix_extension_owner", "lead1")
		ix.MachineID, ix.SessionTitle, ix.ToolName = "server", "Persona", tool
		ix.ToolInput = json.RawMessage(`{"force":true}`)
		if e.s.internalCard(ix) {
			t.Fatalf("extension label authorized %s for the supervisor", tool)
		}
	}
}

func TestInternalQueuePersistenceFailureEndsWithoutAuthorization(t *testing.T) {
	store := &relayWriteFailStore{fleetMem: newFleetMem()}
	e := newRouteEnv(t, store, true)
	store.fail.Store(true)
	defer store.fail.Store(false)
	ix := askPermission("ix_internal_queue_down", "lead1")
	ix.MachineID = "server"
	ix.ToolInput = json.RawMessage(`{"command":"ls /tmp | head"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := e.s.waitInteraction(ctx, ix)
	if err != nil || r.Allow != nil || !r.Dismiss || r.Reason != "internal_queue_unavailable" {
		t.Fatalf("non-durable delivery did not end unanswered: %+v, %v", r, err)
	}
	if e.pushesOf(ix.ID) != 0 || len(workEventsOf(e.s, "ask_internal_delivery_failed")) != 1 {
		t.Fatal("queue failure pushed to the owner or lost its failure receipt")
	}
}
