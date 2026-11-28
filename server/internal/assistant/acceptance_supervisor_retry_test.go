package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The supervisor's pass settles the work, not the telling (#9253). A done or cannot conclusion its supervisor checked
// is passed as it is asked (BySupervisor), before its card goes out, and a "Please verify" it passes may still be
// waiting for its card: either way a card that failed to go out is a card for later, posted by the next tick under the
// round's key, once, within acceptPostWindow, and never for an ask that has no card due or whose acceptance no longer
// stands.

// supervisorConclusion has the main conversation accept todo, done, with every criterion met, and returns its ask:
// passed by the supervisor and, its first card having failed, waiting for one.
func supervisorConclusion(t *testing.T, s *Service, todo string) *acceptAsk {
	t.Helper()
	reviewTask(t, s, todo, map[string]any{"verdict": "accepted", "outcome": "done", "reason": "Review complete",
		"checks": completeReviewChecks(todoNow(s, todo))})
	a := askOf(s, todo)
	if todoNow(s, todo).DoneAt == nil || a == nil || !a.Conclusion || a.AcceptedAt.IsZero() || !a.BySupervisor ||
		a.MessageID != "" || len(a.Messages) != 0 || a.NoCard {
		t.Fatalf("not an accepted result awaiting delivery: %+v", a)
	}
	return a
}

// The reporter's regression (#9253, TestSupervisorConclusionRetryAfterSendFailure there): the task is accepted and
// closed through task_review, its conclusion's first send fails, and a sweep within the window posts it once, under
// the round's key, saying the supervisor checked it, and keeps its message id. A later sweep posts nothing more, and
// the card is not redrawn: it said so as it went out.
func TestSupervisorCheckedConclusionRetriedAfterAFailedSend(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, _ := conclusionService(t, true)
	updated := updatedCards(f)
	lark := newCardLark(f, "结论：", acceptSend{err: errLarkDown})
	a := supervisorConclusion(t, s, todo.ID)
	if cards, keys := lark.sent(); len(cards) != 0 || len(keys) != 1 {
		t.Fatalf("first send: cards %d, sends %d", len(cards), len(keys))
	}
	redrawn := len(updated())
	s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
	s.retryAcceptance(a.AskedAt.Add(20 * time.Minute))
	cards, keys := lark.sent()
	if len(cards) != 1 || len(keys) != 2 || keys[1] != acceptCardKey(todo.ID, a.AskedAt) || keys[0] != keys[1] {
		t.Fatalf("cards %d, keys %q: want one card, two sends under the round's key", len(cards), keys)
	}
	if !strings.Contains(cards[0], "督办已核对证据") || strings.Contains(cards[0], `"a":"accept"`) {
		t.Fatalf("the card does not say the supervisor checked it, or still asks: %s", cards[0])
	}
	got := askOf(s, todo.ID)
	if got.MessageID != "om_pv1" || len(got.Messages) != 1 || got.NoCard || !got.BySupervisor || !got.AcceptedAt.Equal(a.AcceptedAt) {
		t.Fatalf("ask after the retry: %+v", got)
	}
	if n := len(updated()) - redrawn; n != 0 {
		t.Fatalf("the retried card was redrawn %d times", n)
	}
}

// A conclusion whose card went out at once is not sent again by any sweep.
func TestSupervisorCheckedConclusionSentOnceIsNotSentAgain(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, _ := conclusionService(t, true)
	lark := newCardLark(f, "结论：")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "done", "reason": "Review complete",
		"checks": completeReviewChecks(todoNow(s, todo.ID))})
	a := askOf(s, todo.ID)
	if a == nil || !a.BySupervisor || a.MessageID != "om_pv1" {
		t.Fatalf("ask: %+v", a)
	}
	s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
	s.retryAcceptance(a.AskedAt.Add(20 * time.Minute))
	if cards, keys := lark.sent(); len(cards) != 1 || len(keys) != 1 {
		t.Fatalf("cards %d, sends %d: want the first one only", len(cards), len(keys))
	}
	if got := askOf(s, todo.ID); got.MessageID != "om_pv1" || len(got.Messages) != 1 {
		t.Fatalf("ask: %+v", got)
	}
}

// A claim (an ask with no time yet: its card is going out) is never posted by a sweep, nor touched by it. While the
// conclusion's first send is under way the sweep sends nothing, and once that send fails the next sweep posts the card,
// once; a new round claimed while a sweep looks at the passed one is neither posted as it nor given up.
func TestSupervisorCheckedConclusionNotRetriedWhileAClaimIsInFlight(t *testing.T) {
	needsLarkLayer(t)
	t.Run("its first send under way", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, true)
		checks := completeReviewChecks(todoNow(s, todo.ID))
		entered, release := make(chan struct{}, 1), make(chan error, 1)
		t.Cleanup(func() { close(release) }) // a test that failed early still lets the held send end
		var mu sync.Mutex
		sends := 0
		f.answerAppBlocking(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
			var m struct {
				Card string `json:"card"`
			}
			_ = json.Unmarshal(raw, &m)
			if !strings.Contains(m.Card, "结论：") {
				return map[string]any{"message_id": "om_other"}, nil
			}
			mu.Lock()
			sends++
			first := sends == 1
			mu.Unlock()
			if first { // held until the test lets it end
				entered <- struct{}{}
				if err := <-release; err != nil {
					return nil, err
				}
			}
			return map[string]any{"message_id": "om_conclusion"}, nil
		})
		count := func() int { mu.Lock(); defer mu.Unlock(); return sends }
		reviewed := make(chan error, 1)
		go func() {
			raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "outcome": "done", "reason": "Review complete", "checks": checks})
			_, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw)
			reviewed <- err
		}()
		select {
		case <-entered:
		case err := <-reviewed:
			t.Fatalf("the review ended before its conclusion was sent: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the conclusion's first send")
		}
		if a := askOf(s, todo.ID); a == nil || !a.AskedAt.IsZero() || a.NoCard {
			t.Fatalf("not a claim in flight: %+v", a)
		}
		s.retryAcceptance(time.Now().Add(10 * time.Minute))
		if n := count(); n != 1 {
			t.Fatalf("%d sends while the first was in flight", n)
		}
		if a := askOf(s, todo.ID); a == nil || !a.AskedAt.IsZero() || a.NoCard || a.MessageID != "" {
			t.Fatalf("the sweep touched the claim: %+v", a)
		}
		release <- errLarkDown
		select {
		case err := <-reviewed:
			if err != nil {
				t.Fatalf("task_review: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for task_review")
		}
		a := askOf(s, todo.ID)
		if a == nil || !a.BySupervisor || a.MessageID != "" || a.NoCard {
			t.Fatalf("after the failed send: %+v", a)
		}
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		s.retryAcceptance(a.AskedAt.Add(20 * time.Minute))
		if n := count(); n != 2 {
			t.Fatalf("%d sends, want the first and one retry", n)
		}
		if got := askOf(s, todo.ID); got.MessageID != "om_conclusion" {
			t.Fatalf("ask: %+v", got)
		}
	})
	t.Run("a new round claimed while the sweep looks", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, true)
		lark := newCardLark(f, "结论：", acceptSend{err: errLarkDown})
		a := supervisorConclusion(t, s, todo.ID)
		claim := &acceptAsk{Earlier: []string{"om_old"}}
		s.acceptSweepHook = func(id string) { // the sweep chose the passed ask and read its todo; the book is unlocked
			if id != todo.ID {
				return
			}
			s.acceptSweepHook = nil
			s.accept.mu.Lock()
			s.accept.asks[id] = claim
			s.accept.mu.Unlock()
		}
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		if _, keys := lark.sent(); len(keys) != 1 {
			t.Fatalf("%d sends: the sweep posted the round the claim replaced", len(keys))
		}
		if got := askOf(s, todo.ID); got == nil || !got.AskedAt.IsZero() || got.NoCard || got.MessageID != "" || len(got.Earlier) != 1 {
			t.Fatalf("the sweep touched the claim: %+v", got)
		}
	})
}

// Every retry is bounded by acceptPostWindow from the ask, as a "Please verify" card's is: past it nothing is sent and
// the conclusion stays card-less (NoCard).
func TestSupervisorCheckedConclusionRetryIsBounded(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, _ := conclusionService(t, true)
	lark := newCardLark(f, "结论：", acceptSend{err: errLarkDown}, acceptSend{err: errLarkDown}, acceptSend{err: errLarkDown})
	a := supervisorConclusion(t, s, todo.ID)
	s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
	s.retryAcceptance(a.AskedAt.Add(acceptPostWindow - time.Minute))
	if got := askOf(s, todo.ID); got.NoCard || got.MessageID != "" {
		t.Fatalf("given up within the window: %+v", got)
	}
	if _, keys := lark.sent(); len(keys) != 3 {
		t.Fatalf("%d sends within the window, want 3", len(keys))
	}
	s.retryAcceptance(a.AskedAt.Add(acceptPostWindow + time.Minute))
	s.retryAcceptance(a.AskedAt.Add(acceptPostWindow + 11*time.Minute))
	if cards, keys := lark.sent(); len(cards) != 0 || len(keys) != 3 {
		t.Fatalf("past the window: cards %d, sends %d", len(cards), len(keys))
	}
	if got := askOf(s, todo.ID); !got.NoCard || got.MessageID != "" || !got.BySupervisor {
		t.Fatalf("ask past the window: %+v", got)
	}
}

// What no retry posts: an ask with no card due, at the ask or at the retry; one the owner passed themselves; and a
// supervisor's pass that no longer stands, the review cancelled or the task reopened since (dropped, as a reminder
// drops an ended round).
func TestSupervisorCheckedConclusionRetryExclusions(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	reportsOff := FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
		Features: FeishuFeatures{Main: true, Cards: true}}
	setStatus := func(s *Service, f *fakeHub, st FeishuStatus) {
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return st, nil })
		s.larkModule().ForgetStatus()
	}
	t.Run("no card due when it was asked", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, true)
		lark := newCardLark(f, "结论：")
		setStatus(s, f, reportsOff)
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "done", "reason": "Review complete",
			"checks": completeReviewChecks(todoNow(s, todo.ID))})
		a := askOf(s, todo.ID)
		if a == nil || !a.BySupervisor || !a.NoCard || a.MessageID != "" {
			t.Fatalf("ask: %+v", a)
		}
		r := reportsOff
		r.Features.Reports = true // the reports turned on later: a card-less ask stays card-less
		setStatus(s, f, r)
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		if _, keys := lark.sent(); len(keys) != 0 {
			t.Fatalf("%d sends for a card-less ask", len(keys))
		}
	})
	t.Run("no card due when it is tried again", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, true)
		lark := newCardLark(f, "结论：", acceptSend{err: errLarkDown})
		a := supervisorConclusion(t, s, todo.ID)
		setStatus(s, f, reportsOff)
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		s.retryAcceptance(a.AskedAt.Add(20 * time.Minute))
		if _, keys := lark.sent(); len(keys) != 1 {
			t.Fatalf("%d sends, want only the first", len(keys))
		}
		if got := askOf(s, todo.ID); !got.NoCard || got.MessageID != "" {
			t.Fatalf("ask: %+v", got)
		}
	})
	t.Run("passed by the owner", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		waitingForCard(t, s, id)
		if res, err := s.acceptTry(ctx, id, ""); err != nil || res == (acceptResult{}) { // the web app's Accept
			t.Fatalf("accept: %+v %v", res, err)
		}
		a := askOf(s, id)
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		if _, keys := lark.sent(); len(keys) != 1 {
			t.Fatalf("%d sends for an ask the owner passed", len(keys))
		}
		if got := askOf(s, id); got.MessageID != "" || got.AcceptedAt.IsZero() || got.BySupervisor {
			t.Fatalf("ask: %+v", got)
		}
	})
	t.Run("the review cancelled since", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, true)
		lark := newCardLark(f, "结论：", acceptSend{err: errLarkDown})
		a := supervisorConclusion(t, s, todo.ID)
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "cancelled", "note": "不做了"})
		if cur := todoNow(s, todo.ID); cur.Supervision.state(cur) != reviewCancelled {
			t.Fatalf("state %q", cur.Supervision.state(cur))
		}
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		if _, keys := lark.sent(); len(keys) != 1 {
			t.Fatalf("%d sends: the cancelled task's done conclusion went out", len(keys))
		}
		if got := askOf(s, todo.ID); got != nil {
			t.Fatalf("the ask was kept: %+v", got)
		}
	})
	t.Run("the task reopened since", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, true)
		lark := newCardLark(f, "结论：", acceptSend{err: errLarkDown})
		a := supervisorConclusion(t, s, todo.ID)
		if err := s.reopenTodo(ctx, todo.ID); err != nil {
			t.Fatal(err)
		}
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		if _, keys := lark.sent(); len(keys) != 1 {
			t.Fatalf("%d sends: the reopened task's conclusion went out", len(keys))
		}
		if got := askOf(s, todo.ID); got != nil {
			t.Fatalf("the ask was kept: %+v", got)
		}
	})
}

// A "Please verify" whose card failed to go out and that its supervisor then passes (verifyBySupervisor) is posted by
// the next tick the same, saying the supervisor checked it; one passed while its retried card was going out is redrawn
// to say so, once.
func TestSupervisorPassedPleaseVerifyRetried(t *testing.T) {
	needsLarkLayer(t)
	t.Run("passed before the retry", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, false)
		updated := updatedCards(f)
		lark := newAcceptLark(f, acceptSend{err: errLarkDown})
		s.ownerVerifiesAll = true
		f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置看开关"}`
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the ask", func() bool { a := askOf(s, todo.ID); return a != nil && !a.AskedAt.IsZero() })
		f.judge = `{"verdict":"done","reason":"已部署"}`
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
		res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "上线了", "checks": completeReviewChecks(todoNow(s, todo.ID))})
		if !strings.Contains(res, "marked verified by you") {
			t.Fatalf("the review's answer: %s", res)
		}
		a := askOf(s, todo.ID)
		if a == nil || a.Conclusion || !a.BySupervisor || a.MessageID != "" || a.NoCard {
			t.Fatalf("not a passed ask waiting for its card: %+v", a)
		}
		redrawn := len(updated())
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		s.retryAcceptance(a.AskedAt.Add(20 * time.Minute))
		cards, keys := lark.sent()
		if len(cards) != 1 || len(keys) != 2 || keys[0] != keys[1] {
			t.Fatalf("cards %d, keys %q: want one card under the round's key", len(cards), keys)
		}
		if !strings.Contains(cards[0], "督办已核对证据，标为已验证") || strings.Contains(cards[0], `"a":"accept"`) {
			t.Fatalf("the card does not say the supervisor passed it, or still asks: %s", cards[0])
		}
		if got := askOf(s, todo.ID); got.MessageID != "om_pv1" || !got.BySupervisor {
			t.Fatalf("ask: %+v", got)
		}
		if n := len(updated()) - redrawn; n != 0 {
			t.Fatalf("the retried card was redrawn %d times", n)
		}
	})
	t.Run("passed while its card goes out", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		updated := updatedCards(f)
		deployedCardless(t, s, f, id)
		a := waitingForCard(t, s, id)
		lark.then(acceptSend{during: func() { // as the owner's Pass writes it, while the book is unlocked
			s.accept.mu.Lock()
			s.accept.asks[id].AcceptedAt = time.Now().UTC()
			s.accept.mu.Unlock()
		}})
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		cards, _ := lark.sent()
		if len(cards) != 1 || !strings.Contains(cards[0], `"a":"accept"`) {
			t.Fatalf("cards %q: want the card as it was asked", cards)
		}
		if got := updated(); len(got) != 1 || strings.Contains(got[0], `"a":"accept"`) {
			t.Fatalf("redrawn %q: want the card once, passed", got)
		}
	})
}

// acceptCardDue, ask by ask: a card is due for an ask waiting for its first card that nobody but the supervisor passed.
func TestAcceptCardDue(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		a    *acceptAsk
		want bool
	}{
		{"none", nil, false},
		{"waiting for its card", &acceptAsk{AskedAt: at}, true},
		{"a claim in flight", &acceptAsk{Earlier: []string{"om_old"}}, false},
		{"its card went out", &acceptAsk{AskedAt: at, MessageID: "om_1", Messages: []string{"om_1"}}, false},
		{"no card due", &acceptAsk{AskedAt: at, NoCard: true}, false},
		{"passed by the owner", &acceptAsk{AskedAt: at, AcceptedAt: at}, false},
		{"passed by the supervisor", &acceptAsk{AskedAt: at, AcceptedAt: at, BySupervisor: true}, true},
		{"a conclusion the supervisor checked", &acceptAsk{AskedAt: at, AcceptedAt: at, BySupervisor: true, Conclusion: true, Outcome: outcomeDone}, true},
		{"passed by the supervisor, card-less", &acceptAsk{AskedAt: at, AcceptedAt: at, BySupervisor: true, NoCard: true}, false},
		{"passed by the supervisor, its card out", &acceptAsk{AskedAt: at, AcceptedAt: at, BySupervisor: true, MessageID: "om_1"}, false},
		{"passed by the assistant", &acceptAsk{AskedAt: at, AcceptedAt: at, ByAssistant: true, NoCard: true}, false},
		{"a decision settled", &acceptAsk{AskedAt: at, AcceptedAt: at, Conclusion: true, Outcome: outcomeDecide}, false},
	} {
		if got := acceptCardDue(c.a); got != c.want {
			t.Errorf("%s: acceptCardDue = %v, want %v", c.name, got, c.want)
		}
	}
}
