package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// When an unconfirmed supervision relay exhausts relayLookLimit and recheckQueuedDigest refuses to rewrite it because
// one of its tasks changed (tried && next.Text != msg.Text), bounded follow-up confirmation checks retire the stuck
// head, alert the owner, and unblock later supervision signals instead of deadlocking forever across restarts.
func TestUnconfirmedSupervisionTriedMismatchRetiresAndUnblocksPending(t *testing.T) {
	oldLimit := supervisionConfirmRetryLimit
	supervisionConfirmRetryLimit = 2
	t.Cleanup(func() { supervisionConfirmRetryLimit = oldLimit })

	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	var logMu sync.Mutex
	var logs []string
	s.logf = func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	ctx := context.Background()
	busy := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", busy)

	var tasks []Todo
	var signals []superviseSignal
	for _, title := range []string{"Finished task", "Still open task"} {
		task, err := s.createTodo(ctx, title, "verify "+title, "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, task.ID, "conv1", []string{"Verified"}, 0)
		if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
			v.State = reviewPending
			return true
		}); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		g := superviseSignal{Todo: task.ID, Kind: sigReview, Key: "review-" + task.ID, Detail: "Needs review", At: time.Now().UTC()}
		signals = append(signals, g)
	}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for _, g := range signals {
			pending[g.id()] = g
		}
	})
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", signals, "", time.Now(), time.Time{}) {
		t.Fatal("initial digest was not queued")
	}

	// Simulate an unconfirmed send whose original transcript entry is no longer visible (e.g. pushed past turns:3)
	// and one of its tasks has closed since the send, so recheckQueuedDigest hits triedMismatch.
	s.relays.mu.Lock()
	head := s.relays.st.Queue["conv1"][0]
	head.TriedAt = time.Now().Add(-5 * time.Minute).UTC()
	head.DoubtedAt = head.TriedAt
	head.Looks = relayLookLimit - 1
	s.relays.st.Queue["conv1"][0] = head
	if err := s.saveRelayLocked(); err != nil {
		s.relays.mu.Unlock()
		t.Fatal(err)
	}
	s.relays.mu.Unlock()
	closeTodo(t, s, tasks[0].ID)
	f.setSessions("server", idle)

	// Pass 1: Looks transitions relayLookLimit-1 -> relayLookLimit, preserving the exact uncertain head.
	drainRelayTest(t, s, idle)
	stopRelayTimer(s, "conv1")
	if got := savedRelay(t, store, "conv1"); got.Looks != relayLookLimit || got.ConfirmFails != 0 {
		t.Fatalf("after first limit hit: got looks=%d confirm_fails=%d, want %d and 0", got.Looks, got.ConfirmFails, relayLookLimit)
	}

	// Pass 2: still unconfirmed after budget; ConfirmFails advances to 1 and persists across restart.
	advanceRelayLook(t, s, "conv1", relayLookLimit)
	drainRelayTest(t, s, idle)
	stopRelayTimer(s, "conv1")
	if got := savedRelay(t, store, "conv1"); got.ConfirmFails != 1 {
		t.Fatalf("after first post-budget miss: confirm_fails=%d, want 1", got.ConfirmFails)
	}

	// Restart manager from durable store (carrying the same task ledger since memStore stores settings only)
	// and run final check: ConfirmFails hits limit (2), retiring the stuck head.
	restarted, f2 := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(restarted, "conv1") })
	restarted.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	restarted.logf = s.logf
	s.todos.mu.RLock()
	restarted.todos.mu.Lock()
	restarted.todos.todos = map[string]*Todo{}
	for id, td := range s.todos.todos {
		cp := copyTodoLocked(td)
		restarted.todos.todos[id] = &cp
	}
	restarted.todos.loaded = true
	restarted.todos.mu.Unlock()
	s.todos.mu.RUnlock()
	f2.setSessions("server", idle)
	advanceRelayLook(t, restarted, "conv1", relayLookLimit)
	drainRelayTest(t, restarted, idle)
	stopRelayTimer(restarted, "conv1")

	if q := restarted.queuedFor("conv1"); q != 0 {
		t.Fatalf("deadlocked supervision head remained queued after retry limit: %d", q)
	}
	if restarted.relayHasUnresolvedSupervision("conv1") {
		t.Fatal("unresolved supervision flag stayed true after retiring stuck head")
	}
	wantBody := i18n.T(restarted.lang(ctx), "assistant-conversations.supervision_delivery_unconfirmed")
	waitFor(t, "unconfirmed supervision owner alert", func() bool {
		f2.mu.Lock()
		defer f2.mu.Unlock()
		for _, p := range f2.pushes {
			if p.SessionID == "conv1" && p.Body == wantBody {
				return true
			}
		}
		return false
	})
	logMu.Lock()
	joined := strings.Join(logs, "\n")
	logMu.Unlock()
	if !strings.Contains(joined, "retiring stuck queue head") {
		t.Fatalf("expected retirement warning in logs, got:\n%s", joined)
	}

	// A new supervision signal for another completed task must now deliver cleanly without being blocked.
	freshTask, err := restarted.createTodo(ctx, "New completed task", "deliver after recovery", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	restarted.superviseTodo(ctx, freshTask.ID, "conv1", []string{"Done"}, 0)
	freshSig := superviseSignal{Todo: freshTask.ID, Kind: sigReport, Report: upReportDone, Key: "done-" + freshTask.ID, Detail: "Task finished", At: time.Now().UTC()}
	restarted.updateSupervisePending(func(pending map[string]superviseSignal) { pending[freshSig.id()] = freshSig })
	if !restarted.deliverDigest(ctx, restarted.settings(ctx), "conv1", []superviseSignal{freshSig}, "", time.Now(), time.Time{}) {
		t.Fatal("new supervision digest failed to deliver after stuck head retired")
	}
	if sent := f2.sentTo("conv1"); sent != 1 {
		t.Fatalf("sent %d supervision message(s) after recovery, want 1", sent)
	}
}

// Repeated unconfirmed retries of an unchanged supervision relay stop after supervisionConfirmRetryLimit cycles,
// retire the stuck head with an owner alert, and never loop indefinitely.
func TestUnconfirmedSupervisionRetryLoopBoundsAndAlerts(t *testing.T) {
	oldLimit, oldDelays := supervisionConfirmRetryLimit, supervisionConfirmDelays
	supervisionConfirmRetryLimit, supervisionConfirmDelays = 2, nil
	t.Cleanup(func() {
		supervisionConfirmRetryLimit = oldLimit
		supervisionConfirmDelays = oldDelays
	})

	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv2") })
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		conv2,
	)
	_, g := directSupervisionTask(t, s, f, "conv2", "Permanently unconfirmed supervision")
	tries := relayTries(f, "conv2")
	f.mu.Lock()
	f.omitSentItems = true // every send succeeds at RPC level but never appears in transcript
	f.mu.Unlock()

	if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("initial unconfirmed supervision was not queued")
	}
	stopRelayTimer(s, "conv2")

	// Run two retry cycles (retry #1 -> ConfirmFails=1, retry #2 -> ConfirmFails=2 == limit -> retired).
	for cycle := range supervisionConfirmRetryLimit {
		advanceRelayLook(t, s, "conv2", 1)
		drainRelayTest(t, s, conv2)
		stopRelayTimer(s, "conv2")
		advanceRelayLook(t, s, "conv2", 2)
		drainRelayTest(t, s, conv2)
		stopRelayTimer(s, "conv2")
		if cycle < supervisionConfirmRetryLimit-1 && s.queuedFor("conv2") != 1 {
			t.Fatalf("cycle %d retired early: queued=%d", cycle+1, s.queuedFor("conv2"))
		}
	}

	if q := s.queuedFor("conv2"); q != 0 {
		t.Fatalf("unconfirmed supervision stayed queued after %d failed retry cycles: queued=%d", supervisionConfirmRetryLimit, q)
	}
	got := triesOf(f, tries)
	wantTries := 1 + supervisionConfirmRetryLimit
	if len(got) != wantTries {
		t.Fatalf("send attempts = %d (%q), want %d (1 direct + %d bounded retries)", len(got), got, wantTries, supervisionConfirmRetryLimit)
	}
	wantBody := i18n.T(s.lang(ctx), "assistant-conversations.supervision_delivery_unconfirmed")
	waitFor(t, "unconfirmed supervision retry-limit alert", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range f.pushes {
			if p.SessionID == "conv2" && p.Body == wantBody {
				return true
			}
		}
		return false
	})
}

// When direct delivery is blocked (e.g. main conversation is working), a single revalidation-only signal
// (sigPRConflict) in the pending batch must not poison ordinary signals from queueing and merging into one summary.
func TestMixedBusySupervisionQueuesOrdinaryWhileKeepingRevalidationPending(t *testing.T) {
	store := &memStore{}
	s, f, prTask := newPRConflictTask(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	ctx := context.Background()
	busy := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", busy)

	if err := s.applyPRStatus(ctx, prTask, TodoPRStatus{
		URL: prTask.PRURL, State: "open", HeadOID: "head-mixed-busy", Conflicts: true, CheckedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	prSig, ok := pendingPRConflict(s, prTask.ID)
	if !ok {
		t.Fatal("missing pending PR conflict signal")
	}

	doneTask1, err := s.createTodo(ctx, "Completed task A", "ship A", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, doneTask1.ID, "conv1", []string{"Merged"}, 0)
	doneSig1 := superviseSignal{Todo: doneTask1.ID, Kind: sigReport, Report: upReportDone, Key: "done-a", Detail: "Task A done", At: time.Now().UTC()}

	doneTask2, err := s.createTodo(ctx, "Completed task B", "ship B", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, doneTask2.ID, "conv1", []string{"Merged"}, 0)
	doneSig2 := superviseSignal{Todo: doneTask2.ID, Kind: sigReport, Report: upReportDone, Key: "done-b", Detail: "Task B done", At: time.Now().UTC()}

	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		pending[doneSig1.id()] = doneSig1
	})

	// First mixed digest while main is working: doneSig1 must queue while prSig stays pending without dedup.
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{prSig, doneSig1}, "", time.Now(), time.Time{}) {
		t.Fatal("mixed busy digest rejected ordinary signal instead of queueing it")
	}
	if q := s.queuedFor("conv1"); q != 1 {
		t.Fatalf("ordinary signal not queued behind busy main: queued=%d", q)
	}
	if _, stillPending := pendingPRConflict(s, prTask.ID); !stillPending {
		t.Fatal("PR conflict signal was dropped instead of staying pending for revalidation")
	}
	if _, ordPending := storedSupervisePending(t, s)[doneSig1.id()]; ordPending {
		t.Fatal("queued ordinary signal remained in SupervisePending")
	}

	// Second mixed digest while main is still working: doneSig2 merges into the single queued summary.
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		pending[doneSig2.id()] = doneSig2
	})
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{prSig, doneSig2}, "", time.Now(), time.Time{}) {
		t.Fatal("second mixed busy digest did not merge ordinary signal into queued summary")
	}
	q := queuedRelays(s, "conv1")
	if len(q) != 1 || !strings.Contains(q[0].Text, "Completed task A") || !strings.Contains(q[0].Text, "Completed task B") {
		t.Fatalf("want one merged summary with both completed tasks, got %+v", q)
	}
	if strings.Contains(q[0].Text, prTask.Title) {
		t.Fatalf("revalidation-only PR conflict leaked into queued summary: %q", q[0].Text)
	}

	// When main finishes its turn, the merged summary drains as one message, and prSig can still deliver afterwards.
	f.setSessions("server", idle)
	drainRelayTest(t, s, idle)
	if n := f.noticesTo("conv1"); len(n) != 1 || !strings.Contains(n[0], "Completed task A") || !strings.Contains(n[0], "Completed task B") {
		t.Fatalf("merged summary not delivered on idle gap: %q", n)
	}
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{prSig}, "", time.Now(), time.Time{}) {
		t.Fatal("pending PR conflict signal failed to deliver once main became idle (dedup leaked?)")
	}
	if n := f.noticesTo("conv1"); len(n) != 2 || !strings.Contains(n[1], prTask.Title) {
		t.Fatalf("PR conflict did not deliver after queue drained: %q", n)
	}
}

// Even when the owner keeps chatting continuously (refreshing ownerPresentFor on every turn), a non-decision
// supervision digest (such as completed task reports merged into one summary) is bounded by relaySupervisionMax and
// delivers in the next gap between owner turns once the active owner turn ends.
func TestBusyMainChatDeliversNonDecisionSupervisionInTurnGap(t *testing.T) {
	presenceTimes(t, time.Hour, time.Hour)
	ownerTurnTimes(t, time.Hour, time.Hour)
	oldSupMax := relaySupervisionMax
	relaySupervisionMax = 200 * time.Millisecond
	t.Cleanup(func() { relaySupervisionMax = oldSupMax })

	s, f := newQuietService(t)
	stopHolds(t, s)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	session := func(status string) proto.Session {
		return proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: status}
	}
	f.setSessions("server", session(proto.StatusIdle))

	task1, err := s.createTodo(ctx, "Worker report 1", "finish 1", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	task2, err := s.createTodo(ctx, "Worker report 2", "finish 2", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task1.ID, "conv1", []string{"Done"}, 0)
	s.superviseTodo(ctx, task2.ID, "conv1", []string{"Done"}, 0)

	// Owner starts chatting continuously; two non-decision done reports arrive and merge into one queued summary.
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	sig1 := superviseSignal{Todo: task1.ID, Kind: sigReport, Report: upReportDone, Key: "done-1", Detail: "First finished", At: time.Now().UTC()}
	sig2 := superviseSignal{Todo: task2.ID, Kind: sigReport, Report: upReportDone, Key: "done-2", Detail: "Second finished", At: time.Now().UTC()}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		pending[sig1.id()] = sig1
		pending[sig2.id()] = sig2
	})
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{sig1}, "", time.Now(), time.Time{}) {
		t.Fatal("first done report was not queued")
	}
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{sig2}, "", time.Now(), time.Time{}) {
		t.Fatal("second done report did not merge into queued summary")
	}
	q := queuedRelays(s, "conv1")
	if len(q) != 1 || q[0].Urgent || f.sentTo("conv1") != 0 {
		t.Fatalf("want 1 non-urgent merged supervision message queued, got %+v (sent=%d)", q, f.sentTo("conv1"))
	}

	// Age QueuedAt past relaySupervisionMax while owner's turn is still running: must still wait for the owner's turn.
	s.relays.mu.Lock()
	s.relays.st.Queue["conv1"][0].QueuedAt = time.Now().Add(-time.Second).UTC()
	s.relays.mu.Unlock()
	s.setSessions("server", []proto.Session{session(proto.StatusWorking)})
	waitFor(t, "owner turn seen working", func() bool {
		s.relays.mu.Lock()
		defer s.relays.mu.Unlock()
		return s.relays.ownerTurns["conv1"].working
	})
	if sent := f.sentTo("conv1"); sent != 0 {
		t.Fatalf("supervision interrupted active owner turn: sent=%d", sent)
	}

	// Owner's turn ends (turn gap), while ownerPresentFor (1 hour) is still active: merged summary delivers now.
	s.setSessions("server", []proto.Session{session(proto.StatusIdle)})
	waitFor(t, "merged supervision delivered in turn gap", func() bool {
		return f.sentTo("conv1") == 1 && s.queuedFor("conv1") == 0
	})
	n := f.noticesTo("conv1")
	if len(n) != 1 || !strings.Contains(n[0], "Worker report 1") || !strings.Contains(n[0], "Worker report 2") {
		t.Fatalf("unexpected delivered summary: %q", n)
	}

	// Verify ConfirmFails JSON omitempty compatibility for clean messages.
	raw, err := json.Marshal(relayMsg{Tool: superviseTool, Text: "clean"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "confirm_fails") {
		t.Fatalf("zero ConfirmFails should be omitted from JSON: %s", raw)
	}
}
