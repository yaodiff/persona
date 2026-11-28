package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func dispatchSupervisionNow(t *testing.T, s *Service, signals ...superviseSignal) {
	t.Helper()
	// pendingDecisionReply keeps automatic delivery asleep; this explicit dispatch uses an expired gather window.
	s.superviseTimes.gather = time.Millisecond
	b := &s.supervise
	pending := make(map[string]superviseSignal, len(signals))
	for _, signal := range signals {
		pending[signal.id()] = superviseSignal{Todo: signal.Todo, Kind: signal.Kind, Key: signal.Key,
			At: time.Now().Add(-time.Minute), Machine: signal.Machine, Session: signal.Session, Detail: signal.Detail}
	}
	b.dedupMu.Lock()
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.timerAt = time.Time{}
	b.pending = pending
	b.sent = map[string]string{}
	b.last = time.Time{}
	b.mu.Unlock()
	b.dedupMu.Unlock()
	t.Cleanup(func() {
		b.dedupMu.Lock()
		b.mu.Lock()
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		b.mu.Unlock()
		b.dedupMu.Unlock()
	})
	s.sendDigest(context.Background())
}

func pendingDecisionReply(t *testing.T, s *Service, todo Todo) superviseSignal {
	t.Helper()
	a := askOf(s, todo.ID)
	if a == nil {
		t.Fatal("decision ask is missing")
	}
	// Keep this fixture's signal pending until dispatchSupervisionNow explicitly releases it. Acquire the same lock
	// order as sendDigest so an already scheduled digest cannot race the metadata-only owner event.
	s.superviseTimes.gather = time.Hour
	b := &s.supervise
	b.dedupMu.Lock()
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.timerAt = time.Time{}
	b.pending = map[string]superviseSignal{}
	b.mu.Unlock()
	b.dedupMu.Unlock()
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	waitFor(t, "pending decision reply", func() bool {
		return strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply)
	})
	s.supervise.mu.Lock()
	if s.supervise.timer != nil {
		s.supervise.timer.Stop()
		s.supervise.timer = nil
	}
	s.supervise.timerAt = time.Time{}
	g := s.supervise.pending[todo.ID+"|"+sigDecisionReply]
	s.supervise.mu.Unlock()
	if g.Kind != sigDecisionReply {
		t.Fatalf("pending signal = %+v", g)
	}
	return g
}

func setSupervisorWorking(f *fakeHub) {
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
}

func TestDecisionReplyInterjectsIntoWorkingSupervisor(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	setSupervisorWorking(f)
	// The owner is present in the supervisor conversation; the decision reply still reaches its live turn.
	s.UserSpoke(context.Background(), SessionRef{MachineID: "server", SessionID: "conv1"})
	before := f.sent()
	dispatchSupervisionNow(t, s, g)
	if got := f.sent(); got != before+1 {
		t.Fatalf("assistant sends = %d, want %d", got, before+1)
	}
	p := f.lastSend(t)
	if p["session_id"] != "conv1" || p["interject_background"] != true || p["once_id"] == "" || !strings.Contains(p["text"].(string), "task_decided") {
		t.Fatalf("priority dispatch params = %#v", p)
	}
	a := askOf(s, todo.ID)
	if a == nil || a.AgentReply == nil || a.AgentReply.ToldAt.IsZero() {
		t.Fatalf("accepted dispatch did not mark the reply told: %+v", a)
	}
	if s.queuedFor("conv1") != 0 {
		t.Fatalf("decision reply stayed queued: %d", s.queuedFor("conv1"))
	}
}

func TestDecisionReplyInterjectDoesNotSendAnsweredRound(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	setSupervisorWorking(f)
	if _, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量", "said": "Choose the full rollout.", "told_agent": true}); isErr {
		t.Fatal("task_decided rejected the explicit choice")
	}
	before := f.sent()
	ordinary := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "fresh-stall", Detail: "no recent progress"}
	dispatchSupervisionNow(t, s, g, ordinary)
	if got := f.sent(); got != before {
		t.Fatalf("stale decision sent %d assistant messages", got-before)
	}
	if a := askOf(s, todo.ID); a == nil || a.AgentReply == nil || !a.AgentReply.ToldAt.IsZero() {
		t.Fatalf("answered round was marked told by stale dispatch: %+v", a)
	}
	if s.queuedFor("conv1") != 1 {
		t.Fatalf("the still-valid ordinary signal did not retain queue behavior: %d", s.queuedFor("conv1"))
	}
}

func TestMixedDecisionDigestInterjectsWithOnlyCurrentEntries(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	setSupervisorWorking(f)
	active, err := s.createTodo(t.Context(), "Active task", "Check the example", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(t.Context(), active.ID, "conv1", []string{"Verified"}, 0)
	ordinary := superviseSignal{Todo: active.ID, Kind: sigStalled, Key: "fresh-stall", Detail: "no recent progress"}
	before := f.sent()
	dispatchSupervisionNow(t, s, g, ordinary)
	if f.sent() != before+1 {
		t.Fatalf("mixed digest assistant sends = %d, want %d", f.sent(), before+1)
	}
	p := f.lastSend(t)
	text, _ := p["text"].(string)
	if p["interject_background"] != true || !strings.Contains(text, "task_decided") || !strings.Contains(text, "no recent progress") {
		t.Fatalf("mixed digest was not rendered and interjected together: %#v", p)
	}
}

func TestDecisionReplyInterjectKeepsOlderQueuedSupervision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	setSupervisorWorking(f)
	older := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "older-review"}
	s.queueRelay("conv1", relayMsg{From: superviseTool, FromTitle: "督办", Tool: superviseTool, Text: "older supervision", At: time.Now().Add(-time.Minute)})
	s.supervise.mu.Lock()
	s.supervise.setQueuedLocked("conv1", []superviseSignal{older})
	s.supervise.mu.Unlock()
	dispatchSupervisionNow(t, s, g)
	if queued := queuedRelays(s, "conv1"); len(queued) != 1 || queued[0].Text != "older supervision" {
		t.Fatalf("older relay was lost while the decision interjected: %+v", queued)
	}
	s.supervise.mu.Lock()
	tracked := append([]superviseSignal(nil), s.supervise.queued["conv1"]...)
	s.supervise.mu.Unlock()
	if len(tracked) != 1 || tracked[0].Key != older.Key {
		t.Fatalf("older relay tracking was cleared: %+v", tracked)
	}
}

func TestDecisionReplyInterjectRefusalRemainsRecoverable(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	setSupervisorWorking(f)
	f.mu.Lock()
	f.failAssistant = errors.New("node refused assistant.send")
	f.mu.Unlock()
	before := f.sent()
	dispatchSupervisionNow(t, s, g)
	if a := askOf(s, todo.ID); a == nil || a.AgentReply == nil || !a.AgentReply.ToldAt.IsZero() || a.DueAt.IsZero() {
		t.Fatalf("refused dispatch lost the pending pause: %+v", a)
	}
	if !strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply) {
		t.Fatalf("refused dispatch did not restore the decision reply: %v", pendingKinds(s, todo.ID))
	}
	if f.sent() != before {
		t.Fatalf("refused dispatch recorded an assistant send: %d -> %d", before, f.sent())
	}
	if s.queuedFor("conv1") != 0 {
		t.Fatalf("refused decision left a rendered notice queued: %d", s.queuedFor("conv1"))
	}
}

func TestNonDecisionSupervisionStillQueuesBehindWorkingSupervisor(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	setSupervisorWorking(f)
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "ordinary-stall", Detail: "no recent progress"}
	before := f.sent()
	dispatchSupervisionNow(t, s, g)
	if s.queuedFor("conv1") != 1 || f.sent() != before {
		t.Fatalf("ordinary signal bypassed the running turn: queued=%d sends=%d before=%d", s.queuedFor("conv1"), f.sent(), before)
	}
}

// A conversation reserved for its replacement (supervise_round.go backgroundOutgrown) takes nothing straight, a decision
// reply's interjection included: it would run in a conversation archived moments later.
func TestDecisionReplyInterjectRespectsAReplacement(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	s.relays.mu.Lock()
	s.relayLocked(context.Background())
	s.relays.replacing["conv1"] = true
	s.relays.mu.Unlock()
	before := f.sent()
	dispatchSupervisionNow(t, s, g)
	if got := f.sent(); got != before {
		t.Fatalf("%d assistant messages went into a conversation being replaced", got-before)
	}
}

// A decision reply's entry carries the task's last review, the needs_user that put the decision to the owner, which a
// Fresh message (a background round, supervise_round.go) shows and any other leaves out as before.
func TestFreshDecisionReplyShowsTheLastReview(t *testing.T) {
	s, _, todo := decisionAgentReplyService(t)
	g := pendingDecisionReply(t, s, todo)
	e, ok := s.digestEntry(context.Background(), g, "")
	if !ok || e.Kind != sigDecisionReply || e.AgentReply == nil {
		t.Fatalf("the decision reply's entry: %+v %v", e, ok)
	}
	if e.LastReview == nil || e.LastReview.Verdict != reviewNeedsUser {
		t.Fatalf("the entry's last review: %+v", e.LastReview)
	}
	last := "上次结论（" + e.LastReview.At + "）：" + reviewNeedsUser // i18n-ignore: the supervision message's own words
	render := func(fresh bool) string {
		return prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}, Leader: fresh, Background: fresh, Fresh: fresh})
	}
	if text := render(true); !strings.Contains(text, last) || !strings.Contains(text, "task_decided") {
		t.Fatalf("a Fresh decision reply lacks its last review %q:\n%s", last, text)
	}
	if text := render(false); strings.Contains(text, "上次结论") { // i18n-ignore: the supervision message's own words
		t.Fatalf("a decision reply that is not Fresh shows a last review:\n%s", text)
	}
}
