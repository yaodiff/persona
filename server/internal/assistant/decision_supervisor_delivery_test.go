package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exact choices typed in a supervisor / direction conversation settle only once the hub says their send was
// delivered (#13041). These tests drive the manager the way the hub does: session.user_spoke as a bus event (raw
// JSON, so a client_msg_id an older manager does not know is simply ignored), then session.assistant_sent.

// spokeOnBus delivers session.user_spoke as the hub sends it; id "" leaves client_msg_id out, as an older hub does.
func spokeOnBus(t *testing.T, s *Service, r SessionRef, text, id string) {
	t.Helper()
	params := map[string]any{"machine_id": r.MachineID, "session_id": r.SessionID, "text": text}
	if id != "" {
		params["client_msg_id"] = id
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	b := &BusHub{svc: s}
	if err := b.event(context.Background(), busEvent{name: assistantwire.EventUserSpoke, params: raw}); err != nil {
		t.Fatal(err)
	}
}

// sentNotice is the hub's session.assistant_sent for the message id sent to r.
func sentNotice(s *Service, r SessionRef, id, outcome string) {
	s.ConversationSent(context.Background(), ConversationSent{MachineID: r.MachineID, SessionID: r.SessionID,
		Outcome: outcome, ClientMsgID: id})
}

// deliveredSupervisorChoice is an owner's choice typed in conversation r whose send was delivered.
func deliveredSupervisorChoice(t *testing.T, s *Service, r SessionRef, text, id string) {
	t.Helper()
	spokeOnBus(t, s, r, text, id)
	sentNotice(s, r, id, assistantwire.ConversationDelivered)
}

// stillWaiting fails unless to-do id's decision is still open: not accepted, nothing relayed since sent, needs_user.
func stillWaiting(t *testing.T, s *Service, f *fakeHub, id string, sent int, when string) {
	t.Helper()
	if got := askOf(s, id); got == nil || !got.AcceptedAt.IsZero() || got.Chosen != "" || got.ByDefault {
		t.Fatalf("%s: the decision closed: %+v", when, got)
	}
	if delivered := workerSends(f)[sent:]; len(delivered) != 0 {
		t.Fatalf("%s: the worker was told a choice: %q", when, delivered)
	}
	if todoNow(s, id).Supervision.State != reviewNeedsUser {
		t.Fatalf("%s: the decision left needs_user", when)
	}
}

// workerSends are the texts sent to Agents other than the owner's own kept messages (which carry their client_msg_id):
// what the worker was told.
func workerSends(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.agentSent {
		if id, _ := a["client_msg_id"].(string); id != "" {
			continue
		}
		text, _ := a["text"].(string)
		out = append(out, text)
	}
	return out
}

// The spoke alone settles nothing; the matching delivered notice settles the owner's option and relays it.
func TestSupervisorChoiceSettlesOnlyWhenDelivered(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	want := askOf(s, todo.ID).Options[1].Label
	sent := len(sentToAgent(f))
	spokeOnBus(t, s, supervisorRef(), want, "msg-delivered")
	stillWaiting(t, s, f, todo.ID, sent, "before delivery")
	if got := askOf(s, todo.ID); got.AgentReply != nil {
		t.Fatalf("an exact choice waiting for delivery was handed back: %+v", got.AgentReply)
	}
	// Another message's outcome, or the same id in another conversation, is not this one's.
	sentNotice(s, supervisorRef(), "msg-other", assistantwire.ConversationDelivered)
	sentNotice(s, SessionRef{MachineID: "server", SessionID: "conv2"}, "msg-delivered", assistantwire.ConversationDelivered)
	stillWaiting(t, s, f, todo.ID, sent, "after another message's outcome")
	sentNotice(s, supervisorRef(), "msg-delivered", assistantwire.ConversationDelivered)
	got := askOf(s, todo.ID)
	if got == nil || got.AcceptedAt.IsZero() || got.Chosen != want || got.ByDefault {
		t.Fatalf("the delivered choice did not settle: %+v", got)
	}
	if delivered := sentToAgent(f)[sent:]; len(delivered) != 1 || !strings.Contains(delivered[0], want) {
		t.Fatalf("the delivered choice was not relayed to the worker: %q", delivered)
	}
	if s.supervisorSettlePending(todo.ID, time.Now()) {
		t.Fatal("the settled choice still waits")
	}
}

// A failed or unknown send the hub never keeps (a refusal for good, or a manager that did not take it) leaves the card
// waiting: the choice holds the default only until the keep could still arrive, then the default works as before, and
// a late delivered notice for the dropped message settles nothing.
func TestSupervisorChoiceUndeliveredLeavesCardWaiting(t *testing.T) {
	for _, outcome := range []string{assistantwire.ConversationFailed, assistantwire.ConversationUnknown} {
		t.Run(outcome, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			want := askOf(s, todo.ID).Options[1].Label
			sent := len(workerSends(f))
			spokeOnBus(t, s, supervisorRef(), want, "msg-"+outcome)
			sentNotice(s, supervisorRef(), "msg-"+outcome, outcome)
			stillWaiting(t, s, f, todo.ID, sent, outcome)
			if !s.supervisorSettlePending(todo.ID, time.Now()) {
				t.Fatal("the choice stopped waiting before the hub could keep its message")
			}
			if s.supervisorSettlePending(todo.ID, time.Now().Add(supervisorKeepGrace+time.Second)) {
				t.Fatal("a choice whose message was never kept still holds the default")
			}
			sentNotice(s, supervisorRef(), "msg-"+outcome, assistantwire.ConversationDelivered)
			stillWaiting(t, s, f, todo.ID, sent, "a late delivered notice after "+outcome)
			pastDeadline(t, s, todo.ID)
			s.sweepDecisionDeadlines(time.Now())
			if got := askOf(s, todo.ID); got == nil || !got.ByDefault {
				t.Fatalf("the default no longer works after an undelivered choice: %+v", got)
			}
		})
	}
}

// keptChoice is an owner's exact choice typed in conversation conv1 whose send did not go in at once: the hub's
// failed notice, then the hub keeping the same message for the manager to send later (hub/send.go).
func keptChoice(t *testing.T, s *Service, todo Todo, id string) string {
	t.Helper()
	want := askOf(s, todo.ID).Options[1].Label
	spokeOnBus(t, s, supervisorRef(), want, id)
	sentNotice(s, supervisorRef(), id, assistantwire.ConversationFailed) // e.g. 409 conversation_busy
	keepFor(t, s, id, want)
	return want
}

// settledTo fails unless to-do id's decision settled on the owner's option want, relayed to the worker once.
func settledTo(t *testing.T, s *Service, f *fakeHub, id, want string, sent int) {
	t.Helper()
	if got := askOf(s, id); got == nil || got.AcceptedAt.IsZero() || got.Chosen != want || got.ByDefault {
		t.Fatalf("the delivered choice did not settle: %+v", got)
	}
	if delivered := workerSends(f)[sent:]; len(delivered) != 1 || !strings.Contains(delivered[0], want) {
		t.Fatalf("the delivered choice was not relayed to the worker: %q", delivered)
	}
	if s.supervisorSettlePending(id, time.Now()) {
		t.Fatal("the settled choice still waits")
	}
}

// Review on #13189: a failed send the hub keeps goes in later from the manager's own queue, which tells no
// session.assistant_sent. The choice keeps holding the default while the message is kept (past the keep grace and
// past the deadline), and settles on the owner's option when the kept message goes in.
func TestSupervisorChoiceKeptThenSentSettles(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	if askOf(s, todo.ID).Options[1].Label == askOf(s, todo.ID).Recommend {
		t.Fatal("fixture: option 1 is the recommendation")
	}
	sent := len(workerSends(f))
	want := keptChoice(t, s, todo, "msg-kept")
	if !s.supervisorSettlePending(todo.ID, time.Now().Add(supervisorKeepGrace+time.Minute)) {
		t.Fatal("a choice whose message is kept stopped holding the default")
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	stillWaiting(t, s, f, todo.ID, sent, "the deadline passing while the message is kept")
	s.undeliveredSend(context.Background(), "conv1")
	if got := ownerSends(f); len(got) != 1 || got[0] != "msg-kept" {
		t.Fatalf("the kept message was not sent once: %q", got)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept: %+v", q)
	}
	settledTo(t, s, f, todo.ID, want, sent)
	s.sweepDecisionDeadlines(time.Now())
	if got := askOf(s, todo.ID); got.ByDefault || got.Chosen != want {
		t.Fatalf("the default replaced the owner's delivered choice: %+v", got)
	}
}

// The keep can reach the manager before the notice (they travel separately), and a message kept behind older ones
// is kept with no notice at all: either way the choice waits on the kept message and settles when it goes in.
func TestSupervisorChoiceKeptBeforeNoticeSettles(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	sent := len(workerSends(f))
	want := askOf(s, todo.ID).Options[1].Label
	spokeOnBus(t, s, supervisorRef(), want, "msg-early-keep")
	keepFor(t, s, "msg-early-keep", want)
	sentNotice(s, supervisorRef(), "msg-early-keep", assistantwire.ConversationFailed)
	if !s.supervisorSettlePending(todo.ID, time.Now().Add(supervisorSettleTTL+time.Minute)) {
		t.Fatal("a kept choice stopped holding the default after the plain wait")
	}
	s.undeliveredSend(context.Background(), "conv1")
	settledTo(t, s, f, todo.ID, want, sent)
}

// An unknown outcome is kept to be looked for, never sent again by itself: found in the conversation, it is taken as
// delivered, and so is the owner's choice.
func TestSupervisorChoiceKeptInDoubtFoundSettles(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	sent := len(workerSends(f))
	want := askOf(s, todo.ID).Options[1].Label
	started := time.Now().Add(-time.Minute)
	spokeOnBus(t, s, supervisorRef(), want, "msg-in-doubt")
	sentNotice(s, supervisorRef(), "msg-in-doubt", assistantwire.ConversationUnknown)
	keepUnknown(t, s, "msg-in-doubt", want, started)
	stillWaiting(t, s, f, todo.ID, sent, "while the in-doubt message is looked for")
	f.mu.Lock()
	f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: want, ClientMsgID: "msg-in-doubt", TS: time.Now()}}
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 0 {
		t.Fatalf("an in-doubt message was sent again: %q", got)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept after it was found: %+v", q)
	}
	settledTo(t, s, f, todo.ID, want, sent)
}

// A kept message that does not go in (given up after its day, the conversation gone, not found after a try in doubt,
// or deleted by the owner) drops the choice: the card keeps waiting, nothing is relayed, the default holds no more and
// works as before, and the book is empty.
func TestSupervisorChoiceKeptButNotDeliveredDrops(t *testing.T) {
	cases := map[string]func(t *testing.T, s *Service, f *fakeHub){
		"given up": func(t *testing.T, s *Service, f *fakeHub) {
			s.undelivered.mu.Lock()
			s.updateKeptLocked("conv1", "msg-kept", func(m *keptMessage) { m.At = time.Now().Add(-undeliveredGiveUp - time.Minute) })
			s.undelivered.mu.Unlock()
			s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
		},
		"conversation gone": func(t *testing.T, s *Service, f *fakeHub) {
			failSends(f, &rpc.Error{Code: "conversation_deleted", Message: "deleted"})
			s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
			failSends(f, nil) // only the kept message's conversation is gone; the worker still takes the default
		},
		"not found after a try in doubt": func(t *testing.T, s *Service, f *fakeHub) {
			tryInDoubt(t, s, f, "msg-kept")
			endLook(s, "msg-kept")
			s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
			failedMaybeIn(t, s, "msg-kept")
		},
		"deleted by the owner": func(t *testing.T, s *Service, f *fakeHub) {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assistant/conversations/conv1/undelivered/msg-kept", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", rec.Code, rec.Body)
			}
		},
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			sent := len(workerSends(f))
			keptChoice(t, s, todo, "msg-kept")
			end(t, s, f)
			stillWaiting(t, s, f, todo.ID, sent, name)
			if s.supervisorSettlePending(todo.ID, time.Now()) {
				t.Fatal("a choice whose kept message did not go in still holds the default")
			}
			s.supervisorSettles.mu.Lock()
			n := len(s.supervisorSettles.pending)
			s.supervisorSettles.mu.Unlock()
			if n != 0 {
				t.Fatalf("%d entries left in the book", n)
			}
			pastDeadline(t, s, todo.ID)
			s.sweepDecisionDeadlines(time.Now())
			if got := askOf(s, todo.ID); got == nil || !got.ByDefault {
				t.Fatalf("the default no longer works after a kept message failed: %+v", got)
			}
		})
	}
}

// While the choice is on its way, the deadline default does not run: the owner answered, the send just has not
// finished. Delivery then settles the owner's option, not the recommendation.
func TestSupervisorChoiceHoldsDefaultUntilDelivered(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	want := a.Options[1].Label
	if want == a.Recommend {
		t.Fatalf("fixture: option %q is the recommendation", want)
	}
	sent := len(sentToAgent(f))
	spokeOnBus(t, s, supervisorRef(), want, "msg-slow")
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	stillWaiting(t, s, f, todo.ID, sent, "the deadline passing while the send runs")
	sentNotice(s, supervisorRef(), "msg-slow", assistantwire.ConversationDelivered)
	if got := askOf(s, todo.ID); got == nil || got.Chosen != want || got.ByDefault {
		t.Fatalf("delivery did not settle the owner's choice: %+v", got)
	}
}

// A choice whose notice never comes (a refused or kept send has none) stops holding the default after its time, and
// the book never grows past its bound.
func TestSupervisorChoiceExpiresAndStaysBounded(t *testing.T) {
	s, _, todo := decisionAgentReplyService(t)
	spokeOnBus(t, s, supervisorRef(), askOf(s, todo.ID).Options[1].Label, "msg-never-told")
	if !s.supervisorSettlePending(todo.ID, time.Now()) {
		t.Fatal("the exact choice is not waiting for its send")
	}
	later := time.Now().Add(supervisorSettleTTL + time.Second)
	if s.supervisorSettlePending(todo.ID, later) {
		t.Fatal("the choice holds the default past its time")
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(later)
	if got := askOf(s, todo.ID); got == nil || !got.ByDefault {
		t.Fatalf("an expired choice kept holding the default: %+v", got)
	}

	for i := range supervisorSettleMax + 10 {
		s.deferSupervisorSettle(context.Background(), supervisorRef(), fmt.Sprintf("flood-%d", i), "todo_other", time.Now(), time.Now(), "x")
	}
	s.supervisorSettles.mu.Lock()
	n := len(s.supervisorSettles.pending)
	s.supervisorSettles.mu.Unlock()
	if n > supervisorSettleMax {
		t.Fatalf("the book grew to %d entries", n)
	}
}

// An older hub's spoke has no client_msg_id: the outcome can never be matched, so an exact label is handed back like
// free text (the default pauses, the supervisor confirms) instead of closing the card on a send that may fail.
func TestSupervisorChoiceFromOlderHubHandsBack(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	sent := len(sentToAgent(f))
	spokeOnBus(t, s, supervisorRef(), askOf(s, todo.ID).Options[1].Label, "")
	stillWaiting(t, s, f, todo.ID, sent, "an id-less spoke")
	if got := askOf(s, todo.ID); got.AgentReply == nil || got.DueAt.IsZero() {
		t.Fatalf("an id-less exact choice did not pause the default: %+v", got)
	}
	waitFor(t, "the id-less hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
	sentNotice(s, supervisorRef(), "", assistantwire.ConversationDelivered)
	stillWaiting(t, s, f, todo.ID, sent, "an id-less delivered notice")
}

// The Agent-chat path keeps pausing the default on the spoke alone, before any delivery result, whether or not the
// hub names the message.
func TestAgentChatSpokePausesDefaultBeforeDelivery(t *testing.T) {
	for _, id := range []string{"", "agent-msg-1"} {
		t.Run("id="+id, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			a := askOf(s, todo.ID)
			sent := len(sentToAgent(f))
			spokeOnBus(t, s, SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, "let me check the dashboards first", id)
			if got := askOf(s, todo.ID); got.AgentReply == nil || got.DueAt.IsZero() {
				t.Fatalf("the Agent-chat spoke did not pause the default: %+v", got)
			}
			pastDeadline(t, s, todo.ID)
			s.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
			stillWaiting(t, s, f, todo.ID, sent, "the deadline after an Agent-chat reply")
		})
	}
}

// A manager restart while a choice waits for its send: nothing was written, the reloaded manager has no record of
// it, and the delivered notice reaching the new process settles nothing. The card stays waiting.
func TestSupervisorChoiceRestartWhileWaitingKeepsCard(t *testing.T) {
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two rollout options.")
	reviewTask(t, s, todo.ID, rolloutDecision())
	spokeOnBus(t, s, supervisorRef(), askOf(s, todo.ID).Options[1].Label, "msg-restart")
	var stored map[string]*acceptAsk
	if _, err := database.Setting(context.Background(), acceptanceKey, &stored); err != nil {
		t.Fatal(err)
	}
	if a := stored[todo.ID]; a == nil || !a.AcceptedAt.IsZero() || a.Chosen != "" {
		t.Fatalf("a choice waiting for delivery was persisted as settled: %+v", a)
	}
	reloaded, nextHub := newQuietServiceOn(t, database)
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, ok := reloaded.askCopy(todo.ID); !ok || !a.AcceptedAt.IsZero() { // loads the persisted ledger
		t.Fatalf("the reloaded ledger lost the waiting decision: %+v", a)
	}
	sent := len(sentToAgent(nextHub))
	sentNotice(reloaded, supervisorRef(), "msg-restart", assistantwire.ConversationDelivered)
	stillWaiting(t, reloaded, nextHub, todo.ID, sent, "after a restart")
}

// A manager restart while the choice waits on a kept message: the kept message survives (it is saved), the choice does
// not (memory only, as for any waiting choice). The reloaded manager sends the kept message, and nothing settles: the
// card stays waiting and the owner answers it again.
func TestSupervisorChoiceRestartWhileKeptKeepsCard(t *testing.T) {
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two rollout options.")
	reviewTask(t, s, todo.ID, rolloutDecision())
	keptChoice(t, s, todo, "msg-kept-restart")
	reloaded, nextHub := newQuietServiceOn(t, database)
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.askCopy(todo.ID); !ok {
		t.Fatal("the reloaded ledger lost the waiting decision")
	}
	sent := len(workerSends(nextHub))
	reloaded.undeliveredSend(context.Background(), "conv1")
	if got := ownerSends(nextHub); len(got) != 1 || got[0] != "msg-kept-restart" {
		t.Fatalf("the reloaded manager did not send the kept message once: %q", got)
	}
	stillWaiting(t, reloaded, nextHub, todo.ID, sent, "a kept message sent after a restart")
}

// Second review on #13189.

// settleEntry is the waiting choice of message id in conversation conv1, as the book has it.
func settleEntry(s *Service, id string) (supervisorSettle, bool) {
	s.supervisorSettles.mu.Lock()
	defer s.supervisorSettles.mu.Unlock()
	p, ok := s.supervisorSettles.pending[supervisorSettleKey(supervisorRef(), id)]
	return p, ok
}

// settlesAged lets d pass for every waiting choice: what the book finds that much later.
func settlesAged(s *Service, d time.Duration) {
	s.supervisorSettles.mu.Lock()
	defer s.supervisorSettles.mu.Unlock()
	for k, p := range s.supervisorSettles.pending {
		p.expires = p.expires.Add(-d)
		if !p.keepBy.IsZero() {
			p.keepBy = p.keepBy.Add(-d)
		}
		s.supervisorSettles.pending[k] = p
	}
}

// The sweep looks for a choice on its way before it holds the card's action lock. One that arrives right after that
// look, before the default is taken, still wins: the default looks again under the lock the choice is registered
// under. Reversible: the recommendation is not taken; one that cannot be undone: its deadline is not marked handled
// and no reminder goes out. Delivery then settles the owner's option.
func TestSupervisorChoiceArrivingAfterTheSweepLookedBeatsTheDefault(t *testing.T) {
	irreversible := dropTableDecision()
	irreversible["deadline"], irreversible["reversible"] = "1h", false
	for name, review := range map[string]map[string]any{"reversible": rolloutDecision(), "irreversible": irreversible} {
		t.Run(name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			presenceTimes(t, 0, 0)
			ownerTurnTimes(t, 0, 0)
			waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two options.")
			if _, err := reviewErr(s, todo.ID, review); err != nil {
				t.Fatal(err)
			}
			a := askOf(s, todo.ID)
			want := a.Options[1].Label
			if want == a.Recommend || a.Reversible != (name == "reversible") {
				t.Fatalf("fixture: %+v", a)
			}
			sent, pushes := len(workerSends(f)), deadlinePushes(f, todo.ID)
			pastDeadline(t, s, todo.ID)
			arrived := 0
			s.deadlineDefaultHook = func(id string) { // the sweep found nothing on its way and is about to act
				if id != todo.ID {
					return
				}
				arrived++
				spokeOnBus(t, s, supervisorRef(), want, "msg-raced")
			}
			s.sweepDecisionDeadlines(time.Now())
			s.deadlineDefaultHook = nil
			if arrived != 1 {
				t.Fatalf("the sweep reached the default %d times, want 1", arrived)
			}
			stillWaiting(t, s, f, todo.ID, sent, "a choice that arrived after the sweep looked")
			if got := askOf(s, todo.ID); !got.DueAt.IsZero() {
				t.Fatalf("the deadline was handled over the owner's choice on its way: %+v", got)
			}
			if n := deadlinePushes(f, todo.ID); n != pushes {
				t.Fatalf("%d overdue reminders while the owner's choice was on its way", n-pushes)
			}
			sentNotice(s, supervisorRef(), "msg-raced", assistantwire.ConversationDelivered)
			settledTo(t, s, f, todo.ID, want, sent)
		})
	}
}

// A choice that arrives while the default is being taken waits for the card's action lock (the default holds it
// across its send to the Agent), then finds the round answered: nothing is registered, so nothing waits to settle
// and its delivery changes nothing.
func TestSupervisorChoiceWaitsForADefaultUnderWay(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	want := a.Options[1].Label
	pastDeadline(t, s, todo.ID)
	actions := &s.reviewDismissals.actions
	actions.acquire(todo.ID) // the default, as handleTaskDeadline takes it
	spoke := make(chan struct{})
	go func() {
		defer close(spoke)
		spokeOnBus(t, s, supervisorRef(), want, "msg-late")
	}()
	waitFor(t, "the choice to wait for the card's action lock", func() bool {
		actions.mu.Lock()
		defer actions.mu.Unlock()
		l := actions.m[todo.ID]
		return l != nil && l.n == 2
	})
	if _, ok := settleEntry(s, "msg-late"); ok {
		t.Fatal("the choice was registered outside the card's action lock")
	}
	result, err := s.acceptTryAsDismissalLocked(context.Background(), todo.ID, "", time.Time{}, true)
	actions.release(todo.ID)
	if err != nil || !result.taken {
		t.Fatalf("the default was not taken: %+v %v", result, err)
	}
	<-spoke
	if _, ok := settleEntry(s, "msg-late"); ok || s.supervisorSettlePending(todo.ID, time.Now()) {
		t.Fatal("a choice that came after the default waits to settle an answered round")
	}
	sent := len(workerSends(f))
	sentNotice(s, supervisorRef(), "msg-late", assistantwire.ConversationDelivered)
	if got := askOf(s, todo.ID); got == nil || !got.ByDefault || got.Chosen == want {
		t.Fatalf("the late choice changed the default taken before it: %+v", got)
	}
	if got := workerSends(f)[sent:]; len(got) != 0 {
		t.Fatalf("the late choice was relayed after the default: %q", got)
	}
}

// The hub tells session.user_spoke for every try of a message, and answers a retry of a kept one from its record
// with no session.assistant_sent. The retry must not start the choice over: it stays kept, with the kept message's
// lifetime, so it still holds the default past the plain wait and settles when the kept message goes in.
func TestSupervisorChoiceToldAgainKeepsItsKeptState(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	sent := len(workerSends(f))
	want := keptChoice(t, s, todo, "msg-retried")
	before, ok := settleEntry(s, "msg-retried")
	if !ok || !before.kept || !before.keptSeen {
		t.Fatalf("fixture: the kept choice is %+v, %v", before, ok)
	}
	for range 2 { // the app retries; the hub answers "kept" from its record
		spokeOnBus(t, s, supervisorRef(), want, "msg-retried")
	}
	after, ok := settleEntry(s, "msg-retried")
	if !ok || !after.kept || !after.keptSeen || !after.expires.Equal(before.expires) || !after.at.Equal(before.at) ||
		!after.asked.Equal(before.asked) {
		t.Fatalf("the retry replaced the kept choice:\nbefore %+v\nafter  %+v", before, after)
	}
	settlesAged(s, supervisorSettleTTL+time.Minute) // the kept message goes in after the plain wait is over
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	stillWaiting(t, s, f, todo.ID, sent, "the deadline passing after a retry of the kept message")
	s.undeliveredSend(context.Background(), "conv1")
	if got := ownerSends(f); len(got) != 1 || got[0] != "msg-retried" {
		t.Fatalf("the kept message was not sent once: %q", got)
	}
	settledTo(t, s, f, todo.ID, want, sent)
}

// A try told again while the first send still runs keeps the first entry's time: retries do not push the wait out.
func TestSupervisorChoiceToldAgainKeepsItsTime(t *testing.T) {
	s, _, todo := decisionAgentReplyService(t)
	want := askOf(s, todo.ID).Options[1].Label
	spokeOnBus(t, s, supervisorRef(), want, "msg-again")
	settlesAged(s, supervisorSettleTTL-time.Minute)
	before, _ := settleEntry(s, "msg-again")
	spokeOnBus(t, s, supervisorRef(), want, "msg-again")
	if after, ok := settleEntry(s, "msg-again"); !ok || !after.expires.Equal(before.expires) || !after.at.Equal(before.at) {
		t.Fatalf("the retry started the wait over:\nbefore %+v\nafter  %+v", before, after)
	}
	if s.supervisorSettlePending(todo.ID, time.Now().Add(2*time.Minute)) {
		t.Fatal("a retried choice holds the default past its first try's time")
	}
}

// A message the manager already keeps when its choice is registered (no entry is left of the first try) waits on
// that kept message from the start: no notice will come for a retry the hub answers from its record.
func TestSupervisorChoiceOfAnAlreadyKeptMessageWaitsOnIt(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	sent := len(workerSends(f))
	want := askOf(s, todo.ID).Options[1].Label
	keepFor(t, s, "msg-known", want)
	spokeOnBus(t, s, supervisorRef(), want, "msg-known")
	if p, ok := settleEntry(s, "msg-known"); !ok || !p.kept || !p.keptSeen {
		t.Fatalf("the choice does not wait on its kept message: %+v, %v", p, ok)
	}
	settlesAged(s, supervisorSettleTTL+time.Minute)
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	stillWaiting(t, s, f, todo.ID, sent, "the deadline passing while the message is kept")
	s.undeliveredSend(context.Background(), "conv1")
	settledTo(t, s, f, todo.ID, want, sent)
}

// A choice belongs to the round it answered. After a new needs_user review replaced that round, the earlier round's
// choice, in flight or kept, neither holds the new round's deadline default nor settles the new round when its
// message goes in.
func TestSupervisorChoiceOfAnEarlierRoundDoesNotHoldTheNextRound(t *testing.T) {
	for _, kept := range []bool{false, true} {
		name := "in flight"
		if kept {
			name = "kept"
		}
		t.Run(name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			first := askOf(s, todo.ID)
			want := first.Options[1].Label
			if kept {
				keptChoice(t, s, todo, "msg-old-round")
			} else {
				spokeOnBus(t, s, supervisorRef(), want, "msg-old-round")
			}
			if !s.supervisorSettlePending(todo.ID, time.Now()) {
				t.Fatal("fixture: the choice does not hold its own round's default")
			}
			if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "rework", "note": "Revise the rollout plan."}); err != nil {
				t.Fatal(err)
			}
			waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two rollout options.")
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			next := askOf(s, todo.ID)
			if next == nil || next.AskedAt.Equal(first.AskedAt) || !next.AcceptedAt.IsZero() {
				t.Fatalf("fixture: no new round: %+v", next)
			}
			sent := len(workerSends(f))
			if s.supervisorSettlePending(todo.ID, time.Now()) {
				t.Fatal("an earlier round's choice holds the new round's default")
			}
			// The earlier round's message goes in: it answers a question no longer asked.
			if kept {
				s.undeliveredSend(context.Background(), "conv1")
			} else {
				sentNotice(s, supervisorRef(), "msg-old-round", assistantwire.ConversationDelivered)
			}
			stillWaiting(t, s, f, todo.ID, sent, "the earlier round's message going in")
			pastDeadline(t, s, todo.ID)
			s.sweepDecisionDeadlines(time.Now())
			got := askOf(s, todo.ID)
			if got == nil || !got.ByDefault || got.Chosen == want {
				t.Fatalf("the new round did not take its default at the deadline: %+v", got)
			}
			if delivered := workerSends(f)[sent:]; len(delivered) != 1 || !strings.Contains(delivered[0], next.Recommend) {
				t.Fatalf("the new round's default was not relayed once: %q", delivered)
			}
		})
	}
}

// Third review on #13189: a delivered choice used to leave the book before its settlement held the card's action
// lock, so a deadline sweep in between found nothing on its way and acted. supervisorSettleHook runs in exactly that
// window: the choice is claimed, the lock not yet taken.

// settlingNow runs during is, once, in the window between a delivered choice of to-do id being claimed and its
// settlement taking the card's action lock, and reports how often the window was reached.
func settlingNow(t *testing.T, s *Service, id string, during func()) *int {
	t.Helper()
	reached := new(int)
	s.supervisorSettleHook = func(got string) {
		if got != id {
			return
		}
		if *reached++; *reached == 1 {
			during()
		}
	}
	t.Cleanup(func() { s.supervisorSettleHook = nil })
	return reached
}

// bookSize is how many choices wait in the book.
func bookSize(s *Service) int {
	s.supervisorSettles.mu.Lock()
	defer s.supervisorSettles.mu.Unlock()
	return len(s.supervisorSettles.pending)
}

// A deadline sweep that runs after the delivered notice claimed the choice and before its settlement holds the card's
// action lock still finds the choice. Reversible: the recommendation is not taken; one that cannot be undone: its
// deadline is not marked handled and no reminder goes out. The owner's option then settles.
func TestSupervisorChoiceBeingSettledBeatsTheDefault(t *testing.T) {
	irreversible := dropTableDecision()
	irreversible["deadline"], irreversible["reversible"] = "1h", false
	for name, review := range map[string]map[string]any{"reversible": rolloutDecision(), "irreversible": irreversible} {
		t.Run(name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			presenceTimes(t, 0, 0)
			ownerTurnTimes(t, 0, 0)
			waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two options.")
			if _, err := reviewErr(s, todo.ID, review); err != nil {
				t.Fatal(err)
			}
			a := askOf(s, todo.ID)
			want := a.Options[1].Label
			if want == a.Recommend || a.Reversible != (name == "reversible") {
				t.Fatalf("fixture: %+v", a)
			}
			sent, pushes := len(workerSends(f)), deadlinePushes(f, todo.ID)
			spokeOnBus(t, s, supervisorRef(), want, "msg-settling")
			pastDeadline(t, s, todo.ID)
			var during acceptAsk
			var held bool
			reached := settlingNow(t, s, todo.ID, func() {
				s.sweepDecisionDeadlines(time.Now())
				during = *askOf(s, todo.ID)
				p, ok := settleEntry(s, "msg-settling")
				held = ok && p.settling
			})
			sentNotice(s, supervisorRef(), "msg-settling", assistantwire.ConversationDelivered)
			if *reached != 1 {
				t.Fatalf("the settlement reached its window %d times, want 1", *reached)
			}
			if !held {
				t.Fatal("the delivered choice left the book before it was settled")
			}
			if !during.AcceptedAt.IsZero() || during.ByDefault || !during.DueAt.IsZero() {
				t.Fatalf("the sweep acted over a delivered choice about to settle: %+v", during)
			}
			if n := deadlinePushes(f, todo.ID); n != pushes {
				t.Fatalf("%d overdue reminders over a delivered choice about to settle", n-pushes)
			}
			settledTo(t, s, f, todo.ID, want, sent)
			if n := bookSize(s); n != 0 {
				t.Fatalf("%d entries left in the book after the settlement", n)
			}
		})
	}
}

// The same window on the kept-message paths: the manager sent the kept message, or found it in the conversation, and
// dropped it from the undelivered list; the choice is claimed and its settlement has not taken the card's action lock.
// The sweep, and the reap it runs (the kept message is gone, which otherwise ends a kept choice), leave it alone.
func TestSupervisorChoiceOfAKeptMessageBeingSettledBeatsTheDefault(t *testing.T) {
	cases := map[string]struct {
		keep func(t *testing.T, s *Service, f *fakeHub, todo Todo, want string)
		in   func(s *Service)
	}{
		"sent by the manager": {
			keep: func(t *testing.T, s *Service, f *fakeHub, todo Todo, want string) {
				keptChoice(t, s, todo, "msg-kept")
			},
			in: func(s *Service) { s.undeliveredSend(context.Background(), "conv1") },
		},
		"found in the conversation": {
			keep: func(t *testing.T, s *Service, f *fakeHub, todo Todo, want string) {
				spokeOnBus(t, s, supervisorRef(), want, "msg-kept")
				sentNotice(s, supervisorRef(), "msg-kept", assistantwire.ConversationUnknown)
				keepUnknown(t, s, "msg-kept", want, time.Now().Add(-time.Minute))
				f.mu.Lock()
				f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: want, ClientMsgID: "msg-kept", TS: time.Now()}}
				f.mu.Unlock()
			},
			in: func(s *Service) { s.undeliveredPass(context.Background(), time.Now().Add(time.Hour)) },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			sent := len(workerSends(f))
			want := askOf(s, todo.ID).Options[1].Label
			c.keep(t, s, f, todo, want)
			pastDeadline(t, s, todo.ID)
			var during acceptAsk
			var held bool
			var left int
			reached := settlingNow(t, s, todo.ID, func() {
				left = s.undeliveredCount("conv1")
				s.reapSupervisorSettles(time.Now())
				s.sweepDecisionDeadlines(time.Now())
				during = *askOf(s, todo.ID)
				p, ok := settleEntry(s, "msg-kept")
				held = ok && p.settling
			})
			c.in(s)
			if *reached != 1 {
				t.Fatalf("the settlement reached its window %d times, want 1", *reached)
			}
			if left != 0 {
				t.Fatalf("fixture: %d kept messages still listed in the window", left)
			}
			if !held {
				t.Fatal("the choice of a kept message that went in left the book before it was settled")
			}
			if !during.AcceptedAt.IsZero() || during.ByDefault || !during.DueAt.IsZero() {
				t.Fatalf("the sweep acted over a delivered choice about to settle: %+v", during)
			}
			settledTo(t, s, f, todo.ID, want, sent)
			if n := bookSize(s); n != 0 {
				t.Fatalf("%d entries left in the book after the settlement", n)
			}
		})
	}
}

// A choice being settled is its settler's alone. While the settlement waits, the entry outlives its time and a full
// book (it is neither expired nor evicted), and the same message told again or reported delivered again starts no
// second settlement: the owner's option is recorded and relayed once.
func TestSupervisorChoiceBeingSettledIsNotDroppedOrSettledTwice(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	sent := len(workerSends(f))
	want := askOf(s, todo.ID).Options[1].Label
	spokeOnBus(t, s, supervisorRef(), want, "msg-once")
	pastDeadline(t, s, todo.ID)
	var during acceptAsk
	var held bool
	reached := settlingNow(t, s, todo.ID, func() {
		settlesAged(s, supervisorSettleTTL+time.Minute) // its time passes while the settlement waits
		for i := range supervisorSettleMax + 10 {       // and the book fills
			s.deferSupervisorSettle(context.Background(), supervisorRef(), fmt.Sprintf("flood-%d", i), "todo_other", time.Now(), time.Now(), "x")
		}
		spokeOnBus(t, s, supervisorRef(), want, "msg-once") // a retry of the same message
		s.supervisorSettleOutcome(context.Background(), ConversationSent{MachineID: supervisorRef().MachineID,
			SessionID: supervisorRef().SessionID, Outcome: assistantwire.ConversationDelivered, ClientMsgID: "msg-once"})
		s.sweepDecisionDeadlines(time.Now())
		during = *askOf(s, todo.ID)
		p, ok := settleEntry(s, "msg-once")
		held = ok && p.settling
	})
	sentNotice(s, supervisorRef(), "msg-once", assistantwire.ConversationDelivered)
	if *reached != 1 {
		t.Fatalf("the settlement reached its window %d times, want 1", *reached)
	}
	if !held {
		t.Fatal("a choice being settled was expired, evicted or replaced")
	}
	if !during.AcceptedAt.IsZero() || during.ByDefault || !during.DueAt.IsZero() {
		t.Fatalf("the sweep acted over a delivered choice about to settle: %+v", during)
	}
	settledTo(t, s, f, todo.ID, want, sent)
	if _, ok := settleEntry(s, "msg-once"); ok {
		t.Fatal("the settled choice is still in the book")
	}
	if n := bookSize(s); n > supervisorSettleMax {
		t.Fatalf("the book grew to %d entries", n)
	}
}

// The entry ends with its settlement whatever the outcome, so it never keeps holding a default it can no longer
// settle. Answered on the card while the settlement waited: the card's answer stands and nothing more is relayed. Not
// taken by the Agent: the card keeps waiting, and the owner's own option has already marked the deadline answered, so
// no default follows. An Agent that cannot take it now (#17431): the choice is recorded and the round ends.
func TestSupervisorChoiceBeingSettledEndsWhateverTheOutcome(t *testing.T) {
	t.Run("answered on the card meanwhile", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		a := askOf(s, todo.ID)
		want := a.Options[1].Label
		spokeOnBus(t, s, supervisorRef(), want, "msg-late")
		var sent int
		reached := settlingNow(t, s, todo.ID, func() {
			if result, err := s.chooseOption(context.Background(), todo.ID, "", a.Recommend); err != nil || !result.taken {
				t.Errorf("the card's own answer was refused while a choice waited to settle: %+v %v", result, err)
			}
			sent = len(workerSends(f))
		})
		sentNotice(s, supervisorRef(), "msg-late", assistantwire.ConversationDelivered)
		if *reached != 1 {
			t.Fatalf("the settlement reached its window %d times, want 1", *reached)
		}
		if got := askOf(s, todo.ID); got == nil || got.AcceptedAt.IsZero() || got.ByDefault || got.Chosen == want {
			t.Fatalf("the card's answer did not stand: %+v", got)
		}
		if got := workerSends(f)[sent:]; len(got) != 0 {
			t.Fatalf("the choice was relayed after the card was answered: %q", got)
		}
		if n := bookSize(s); n != 0 || s.supervisorSettlePending(todo.ID, time.Now()) {
			t.Fatalf("%d entries left in the book of an answered round", n)
		}
	})
	t.Run("not taken by the Agent", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		want := askOf(s, todo.ID).Options[1].Label
		spokeOnBus(t, s, supervisorRef(), want, "msg-refused")
		failSends(f, errors.New("the link dropped"))
		sent := len(workerSends(f))
		sentNotice(s, supervisorRef(), "msg-refused", assistantwire.ConversationDelivered)
		failSends(f, nil)
		stillWaiting(t, s, f, todo.ID, sent, "a choice the Agent did not take")
		if n := bookSize(s); n != 0 || s.supervisorSettlePending(todo.ID, time.Now()) {
			t.Fatalf("%d entries left in the book after a settlement that failed", n)
		}
		if got := askOf(s, todo.ID); got.DueAt.IsZero() {
			t.Fatalf("the owner's own answer did not mark the deadline answered: %+v", got)
		}
		pastDeadline(t, s, todo.ID)
		s.sweepDecisionDeadlines(time.Now())
		stillWaiting(t, s, f, todo.ID, sent, "the deadline after the owner's own answer failed to reach the Agent")
	})
	// A send that failed before anything was typed (the node restarting, #17431): the owner's choice is recorded and
	// handed to the supervisor to pass on, the round ends, and nothing follows at the deadline.
	t.Run("the Agent cannot take it now", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		want := askOf(s, todo.ID).Options[1].Label
		spokeOnBus(t, s, supervisorRef(), want, "msg-restarting")
		failSends(f, &rpc.Error{Code: "internal", Message: "the node is restarting"})
		sent := len(workerSends(f))
		sentNotice(s, supervisorRef(), "msg-restarting", assistantwire.ConversationDelivered)
		failSends(f, nil)
		if n := bookSize(s); n != 0 || s.supervisorSettlePending(todo.ID, time.Now()) {
			t.Fatalf("%d entries left in the book after a recorded settlement", n)
		}
		if got := askOf(s, todo.ID); got != nil && got.AcceptedAt.IsZero() {
			t.Fatalf("the owner's choice was not recorded: %+v", got)
		}
		if listedAsk(t, s, todo.ID) != nil {
			t.Fatal("the recorded choice still waits")
		}
		if askOf(s, todo.ID) != nil {
			pastDeadline(t, s, todo.ID)
		}
		s.sweepDecisionDeadlines(time.Now())
		if got := workerSends(f)[sent:]; len(got) != 0 {
			t.Fatalf("the hub sent it after all: %q", got)
		}
		s.waitWorkEvents()
	})
}

// settleClock stands in for the book's clock: advance moves it on, without waiting.
type settleClock struct{ at time.Time }

func (c *settleClock) now() time.Time { return c.at }

func (c *settleClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// withSettleClock gives s's book of waiting choices a clock the test moves.
func withSettleClock(s *Service) *settleClock {
	c := &settleClock{at: time.Now()}
	s.supervisorSettles.mu.Lock()
	s.supervisorSettles.clock = c.now
	s.supervisorSettles.mu.Unlock()
	return c
}

// retryDeliveredSettles: the retry of the owner's choice, on its way past the keep grace, holds the default (also
// past the deadline), and its delivered notice settles the owner's option.
func retryDeliveredSettles(t *testing.T, s *Service, f *fakeHub, c *settleClock, todo Todo, id, want string, sent int) {
	t.Helper()
	if !s.supervisorSettlePending(todo.ID, c.now()) {
		t.Fatal("the retried choice stopped holding the default on its way")
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(c.now())
	stillWaiting(t, s, f, todo.ID, sent, "the deadline passing while the retry is on its way")
	sentNotice(s, supervisorRef(), id, assistantwire.ConversationDelivered)
	settledTo(t, s, f, todo.ID, want, sent)
}

// Review on #13189 (P1, kept never reset): a failed send the hub does not keep is forgotten by the hub, and the app
// retries the same id as a new send, told by session.user_spoke again. The failed notice's kept mark must not stay on
// the entry of that retry: once the keep grace is over the reap would drop it before the retry's delivered notice,
// and the default would replace the owner's delivered choice.
func TestSupervisorChoiceRetriedAfterAnUnkeptFailureSettles(t *testing.T) {
	for name, retryAfter := range map[string]time.Duration{
		"within the keep grace": 5 * time.Second,
		"after the keep grace":  supervisorKeepGrace + 15*time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			c := withSettleClock(s)
			sent := len(workerSends(f))
			want := askOf(s, todo.ID).Options[1].Label
			spokeOnBus(t, s, supervisorRef(), want, "msg-retry")
			sentNotice(s, supervisorRef(), "msg-retry", assistantwire.ConversationFailed) // e.g. 409, not kept
			c.advance(retryAfter)
			spokeOnBus(t, s, supervisorRef(), want, "msg-retry") // the hub forgot the id: a new send
			if p, ok := settleEntry(s, "msg-retry"); !ok || p.kept || p.keptSeen || !p.keepBy.IsZero() {
				t.Fatalf("the retry kept the failed try's kept mark: %+v, %v", p, ok)
			}
			c.advance(supervisorKeepGrace + time.Second) // the retry takes a while (a busy or starting session)
			retryDeliveredSettles(t, s, f, c, todo, "msg-retry", want, sent)
		})
	}
}

// The same class, the other way round: notices are handled apart from spokes, so the first try's failed notice can
// be handled after the retry's spoke. It is an earlier try's outcome and must not mark the retry kept; the retry's
// own failed notice still does, and the entry then goes when the hub does not keep it either.
func TestSupervisorChoiceRetryIsNotMarkedKeptByAnEarlierTrysNotice(t *testing.T) {
	t.Run("the retry is delivered", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		c := withSettleClock(s)
		sent := len(workerSends(f))
		want := askOf(s, todo.ID).Options[1].Label
		spokeOnBus(t, s, supervisorRef(), want, "msg-late-notice")
		spokeOnBus(t, s, supervisorRef(), want, "msg-late-notice")                          // the retry's spoke
		sentNotice(s, supervisorRef(), "msg-late-notice", assistantwire.ConversationFailed) // the first try's, late
		if p, ok := settleEntry(s, "msg-late-notice"); !ok || p.kept {
			t.Fatalf("the first try's notice marked the retry kept: %+v, %v", p, ok)
		}
		c.advance(supervisorKeepGrace + time.Second)
		retryDeliveredSettles(t, s, f, c, todo, "msg-late-notice", want, sent)
	})
	t.Run("the retry fails too", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		c := withSettleClock(s)
		sent := len(workerSends(f))
		want := askOf(s, todo.ID).Options[1].Label
		spokeOnBus(t, s, supervisorRef(), want, "msg-late-notice")
		spokeOnBus(t, s, supervisorRef(), want, "msg-late-notice")
		sentNotice(s, supervisorRef(), "msg-late-notice", assistantwire.ConversationFailed)
		sentNotice(s, supervisorRef(), "msg-late-notice", assistantwire.ConversationFailed) // the retry's own
		if p, ok := settleEntry(s, "msg-late-notice"); !ok || !p.kept {
			t.Fatalf("the retry's own failed notice did not mark it kept: %+v, %v", p, ok)
		}
		c.advance(supervisorKeepGrace + time.Second)
		if s.supervisorSettlePending(todo.ID, c.now()) {
			t.Fatal("a retry that failed and was never kept still holds the default")
		}
		stillWaiting(t, s, f, todo.ID, sent, "a retry that failed too")
		pastDeadline(t, s, todo.ID)
		s.sweepDecisionDeadlines(c.now())
		if got := askOf(s, todo.ID); got == nil || !got.ByDefault {
			t.Fatalf("the default no longer works after a failed retry: %+v", got)
		}
	})
}

// A kept mark whose kept message is gone (the owner deleted it, or it failed) is stale too: a send of the same id
// told after that, before the reap ran, starts over on its way instead of being dropped with the gone message.
func TestSupervisorChoiceRetriedAfterItsKeptMessageWentSettles(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	c := withSettleClock(s)
	sent := len(workerSends(f))
	want := keptChoice(t, s, todo, "msg-gone")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assistant/conversations/conv1/undelivered/msg-gone", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	spokeOnBus(t, s, supervisorRef(), want, "msg-gone")
	if p, ok := settleEntry(s, "msg-gone"); !ok || p.kept || p.keptSeen {
		t.Fatalf("the new send kept the gone message's kept mark: %+v, %v", p, ok)
	}
	c.advance(supervisorKeepGrace + time.Second)
	retryDeliveredSettles(t, s, f, c, todo, "msg-gone", want, sent)
}
