package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A "Please verify" card that does not go out when the ask is made (the bot's status unreadable, the send failed or timed
// out) is kept as an ask without a card yet, listed like a card-less one, and posted by a later tick of acceptLoop
// (retryAcceptance), under the round's own key, within acceptPostWindow, never for a round that has ended.

// botReady is feishu.status with the bot paired and its task reports on.
var botReady = FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", Features: FeishuFeatures{Main: true, Cards: true, Reports: true}}

// acceptSend is how one "Please verify" send ends: err without lost did not land; lost landed, but its answer was lost
// (a timeout), and err is what the caller sees. during, when set, runs while the send is under way (the book unlocked).
type acceptSend struct {
	err    error
	lost   bool
	during func()
}

// acceptLark answers feishu.post as Feishu does for a send with a key (uuid): a card whose key already landed does not
// land again, and the answer is the first one's id. The "Please verify" sends (or those of the cards it was made for,
// newCardLark) end as next says, in order, then land.
type acceptLark struct {
	mu     sync.Mutex
	next   []acceptSend
	landed map[string]string // by key: the message id of the card that landed under it
	cards  []string          // the "Please verify" cards that landed
	keys   []string          // the key of every "Please verify" send
}

// newAcceptLark answers f's feishu.post with a fresh acceptLark; any other card (a task report) just lands.
func newAcceptLark(f *fakeHub, next ...acceptSend) *acceptLark {
	return newCardLark(f, "请验收", next...)
}

// newCardLark is newAcceptLark for the cards that say mark (a conclusion's "结论：") instead of "Please verify".
func newCardLark(f *fakeHub, mark string, next ...acceptSend) *acceptLark {
	l := &acceptLark{next: next, landed: map[string]string{}}
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
			UUID string `json:"uuid"`
		}
		_ = json.Unmarshal(p, &m)
		if !strings.Contains(m.Card, mark) {
			return map[string]any{"message_id": "om_other"}, nil
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		l.keys = append(l.keys, m.UUID)
		var end acceptSend
		if len(l.next) > 0 {
			end, l.next = l.next[0], l.next[1:]
		}
		if end.during != nil {
			end.during()
		}
		if end.err != nil && !end.lost {
			return nil, end.err
		}
		id, seen := l.landed[m.UUID]
		if !seen || m.UUID == "" {
			id = fmt.Sprintf("om_pv%d", len(l.cards)+1)
			l.cards = append(l.cards, m.Card)
			if m.UUID != "" {
				l.landed[m.UUID] = id
			}
		}
		if end.lost {
			return nil, end.err
		}
		return map[string]any{"message_id": id}, nil
	})
	return l
}

// then queues how the next sends end.
func (l *acceptLark) then(next ...acceptSend) {
	l.mu.Lock()
	l.next = append(l.next, next...)
	l.mu.Unlock()
}

// sent are the cards that landed and the keys of all the sends so far.
func (l *acceptLark) sent() (cards, keys []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.cards...), append([]string(nil), l.keys...)
}

var errLarkDown = errors.New("feishu: 503")

// retryService is cardlessService with the bot ready and Feishu as given; the task is working on box/s1.
func retryService(t *testing.T, next ...acceptSend) (*Service, *fakeHub, *acceptLark, string) {
	t.Helper()
	s, f, _, id := cardlessService(t, botReady)
	return s, f, newAcceptLark(f, next...), id
}

// waitingForCard checks a's ask waits for its card: asked, no card, not card-less, and listed by the board, the
// snapshot and the briefing as a card-less one is.
func waitingForCard(t *testing.T, s *Service, id string) *acceptAsk {
	t.Helper()
	ctx := context.Background()
	a := askOf(s, id)
	if a == nil || a.AskedAt.IsZero() || a.MessageID != "" || len(a.Messages) != 0 || a.NoCard {
		t.Fatalf("not an ask waiting for its card: %+v", a)
	}
	if rows := s.awaitingAcceptance(ctx); len(rows) != 1 || rows[0].todo != id || !rows[0].since.Equal(a.AskedAt) {
		t.Fatalf("board: %+v", rows)
	}
	if got, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 1 || len(got) != 1 || got[0].Changed != a.Changed {
		t.Fatalf("snapshot: %+v total=%d", got, total)
	}
	if s.briefingOn() {
		if facts := s.briefFactsAt(ctx, time.Now(), ""); len(facts.Accept) != 1 || facts.Accept[0].ID != id {
			t.Fatalf("briefing: %+v", facts.Accept)
		}
	}
	return a
}

// The repro: the first card fails to go out; the ask is kept, and the next tick posts its one card, under the same key.
// Later ticks post nothing more, and the card passes the task as one sent at once does.
func TestAcceptanceCardRetriedAfterAFailedSend(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
	deployedCardless(t, s, f, id)
	a := waitingForCard(t, s, id)
	if a.Changed != "飞书机器人多了任务进展开关" || a.MachineID != "box" || a.SessionID != "s1" {
		t.Fatalf("lines: %+v", a)
	}
	if cards, keys := lark.sent(); len(cards) != 0 || len(keys) != 1 || keys[0] == "" {
		t.Fatalf("first send: cards %d, keys %q", len(cards), keys)
	}
	s.retryAcceptance(time.Now())
	s.retryAcceptance(time.Now())
	cards, keys := lark.sent()
	if len(cards) != 1 || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("cards %d, keys %q: want one card, two sends under one key", len(cards), keys)
	}
	got := askOf(s, id)
	if got.MessageID != "om_pv1" || len(got.Messages) != 1 || got.NoCard || !got.AskedAt.Equal(a.AskedAt) {
		t.Fatalf("ask after the retry: %+v", got)
	}
	if !s.acceptReply(ctx, "om_reply", "通过", false, got.MessageID) || onlyTodo(s).Stage != "verify" {
		t.Fatal("the retried card did not pass the task")
	}
}

// A send that timed out may have landed: the retry goes under the same key, which Feishu takes as the same send, so the
// ask gets that card's id and there is still one card.
func TestAcceptanceRetryAfterATimeoutPostsOneCard(t *testing.T) {
	needsLarkLayer(t)
	s, f, lark, id := retryService(t, acceptSend{err: context.DeadlineExceeded, lost: true})
	deployedCardless(t, s, f, id)
	waitingForCard(t, s, id)
	if cards, _ := lark.sent(); len(cards) != 1 {
		t.Fatalf("the lost send did not land in the fake: %d cards", len(cards))
	}
	s.retryAcceptance(time.Now())
	cards, keys := lark.sent()
	if len(cards) != 1 || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("cards %d, keys %q: a second card after a timeout", len(cards), keys)
	}
	if a := askOf(s, id); a.MessageID != "om_pv1" || len(a.Messages) != 1 {
		t.Fatalf("ask: %+v", a)
	}
}

// Every try is bounded by acceptPostWindow from the ask: within it a failing send waits for the next tick; past it no
// send is made and the ask is kept card-less (NoCard), still listed. An unreadable status the whole time ends the same.
func TestAcceptanceRetryIsBounded(t *testing.T) {
	needsLarkLayer(t)
	t.Run("sends that keep failing", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		a := waitingForCard(t, s, id)
		lark.then(acceptSend{err: errLarkDown}, acceptSend{err: errLarkDown})
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		s.retryAcceptance(a.AskedAt.Add(acceptPostWindow - time.Minute))
		waitingForCard(t, s, id)
		if _, keys := lark.sent(); len(keys) != 3 {
			t.Fatalf("%d sends within the window, want 3", len(keys))
		}
		s.retryAcceptance(a.AskedAt.Add(acceptPostWindow + time.Minute))
		s.retryAcceptance(a.AskedAt.Add(acceptPostWindow + 11*time.Minute))
		if cards, keys := lark.sent(); len(cards) != 0 || len(keys) != 3 {
			t.Fatalf("past the window: cards %d, sends %d", len(cards), len(keys))
		}
		cardlessAndListed(t, s, id)
	})
	t.Run("a status that cannot be read", func(t *testing.T) {
		s, f, lark, id := retryService(t)
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return nil, errors.New("manager away") })
		s.larkModule().ForgetStatus()
		deployedCardless(t, s, f, id)
		a := askOf(s, id)
		s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
		if a := askOf(s, id); a.NoCard || a.MessageID != "" {
			t.Fatalf("an unreadable status gave the ask up within the window: %+v", a)
		}
		s.retryAcceptance(a.AskedAt.Add(acceptPostWindow + time.Minute))
		if _, keys := lark.sent(); len(keys) != 0 {
			t.Fatalf("%d sends with the status unreadable", len(keys))
		}
		cardlessAndListed(t, s, id)
	})
}

// cardlessAndListed checks id's ask is card-less for good and still listed by the board and the snapshot.
func cardlessAndListed(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	if a := askOf(s, id); a == nil || !a.NoCard || a.MessageID != "" {
		t.Fatalf("ask: %+v", a)
	}
	if rows := s.awaitingAcceptance(ctx); len(rows) != 1 {
		t.Fatalf("board: %+v", rows)
	}
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 1 {
		t.Fatalf("snapshot total %d", total)
	}
}

// The bot found off (or its reports off) when the card is tried again: no card is due, so the ask is card-less (NoCard),
// as one made with the bot off is, and nothing is sent.
func TestAcceptanceRetryWithTheBotOffIsCardless(t *testing.T) {
	needsLarkLayer(t)
	for _, c := range []struct {
		name string
		st   FeishuStatus
	}{
		{"bot not set up", botNotSetUp},
		{"reports off", FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", Features: FeishuFeatures{Main: true, Cards: true}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
			deployedCardless(t, s, f, id)
			waitingForCard(t, s, id)
			st := c.st
			st.PublicURL = cardlessWeb
			f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return st, nil })
			s.larkModule().ForgetStatus()
			s.retryAcceptance(time.Now())
			if _, keys := lark.sent(); len(keys) != 1 {
				t.Fatalf("%d sends, want only the first", len(keys))
			}
			cardlessAndListed(t, s, id)
		})
	}
}

// The round guards: a retry never posts an ask from before the task's latest reopen or one whose task ended without the
// hub's judge (both are dropped); a task the judge closed is the normal state and gets its card. The rounds before keep
// their cards dead (Earlier) through a failed send and its retry, and a new round asked while a sweep looks is not
// posted as the old one.
func TestAcceptanceRetryRoundGuards(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	dropped := func(t *testing.T, s *Service, lark *acceptLark, id string) {
		t.Helper()
		s.retryAcceptance(time.Now())
		if cards, keys := lark.sent(); len(cards) != 0 || len(keys) != 1 {
			t.Fatalf("posted for an ended round: cards %d, sends %d", len(cards), len(keys))
		}
		if a := askOf(s, id); a != nil {
			t.Fatalf("the ended round's ask was kept: %+v", a)
		}
	}
	t.Run("reopened before the retry", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		closeAndReopen(t, s, id)
		dropped(t, s, lark, id)
		// The next deploy is a round of its own, with its own card.
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the next round's card", func() bool { a := askOf(s, id); return a != nil && a.MessageID != "" })
		if cards, keys := lark.sent(); len(cards) != 1 || len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("cards %d, keys %q: the new round needs its own card and key", len(cards), keys)
		}
	})
	t.Run("closed by the owner", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		done := time.Now()
		s.todos.mu.Lock()
		s.todos.todos[id].DoneAt, s.todos.todos[id].DoneBy = &done, ""
		s.todos.mu.Unlock()
		dropped(t, s, lark, id)
	})
	t.Run("closed by the hub's judge is posted", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		if ok, err := s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
			t.Fatalf("auto done: %v %v", ok, err)
		}
		s.retryAcceptance(time.Now())
		if cards, _ := lark.sent(); len(cards) != 1 || askOf(s, id).MessageID == "" {
			t.Fatalf("cards %d, ask %+v", len(cards), askOf(s, id))
		}
	})
	t.Run("earlier rounds stay dead through a retry", func(t *testing.T) {
		s, f, lark, id := retryService(t)
		deployedCardless(t, s, f, id)
		first := askOf(s, id).MessageID
		closeAndReopen(t, s, id)
		lark.then(acceptSend{err: errLarkDown})
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the second round's ask", func() bool { a := askOf(s, id); return a != nil && a.MessageID == "" && !a.AskedAt.IsZero() })
		if a := askOf(s, id); len(a.Earlier) != 1 || a.Earlier[0] != first {
			t.Fatalf("the failed send's ask lost the earlier round's card: %+v", a)
		}
		s.retryAcceptance(time.Now())
		a := askOf(s, id)
		if a.MessageID == "" || a.MessageID == first || len(a.Earlier) != 1 || a.Earlier[0] != first {
			t.Fatalf("after the retry: %+v", a)
		}
		if got, earlier := s.acceptFor(first); got != id || !earlier {
			t.Fatalf("the first round's card is not dead: %q %v", got, earlier)
		}
	})
	t.Run("a new round asked during the sweep", func(t *testing.T) {
		// The owner closed the task, so the sweep's read of it says the round ended; while the book is unlocked the task
		// is reopened and deployed again, and that round's send fails too. The sweep judged the round before: the new one
		// is neither dropped nor posted by it, and the next tick posts it under its own key.
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown}, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		old := askOf(s, id)
		done := time.Now()
		s.todos.mu.Lock()
		s.todos.todos[id].DoneAt, s.todos.todos[id].DoneBy = &done, ""
		s.todos.mu.Unlock()
		fired := false
		s.acceptSweepHook = func(hid string) {
			if fired || hid != id {
				return
			}
			fired = true
			if err := s.reopenTodo(ctx, id); err != nil {
				t.Errorf("reopen: %v", err)
			}
			s.askAcceptance(onlyTodo(s), "box", "s1", deployedReply)
		}
		s.retryAcceptance(time.Now())
		s.acceptSweepHook = nil
		if _, keys := lark.sent(); len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("keys %q: want the two rounds' first sends only", keys)
		}
		if a := askOf(s, id); !acceptCardDue(a) || !a.AskedAt.After(old.AskedAt) {
			t.Fatalf("the new round's ask: %+v", a)
		}
		s.retryAcceptance(time.Now())
		if cards, keys := lark.sent(); len(cards) != 1 || len(keys) != 3 || keys[2] != keys[1] {
			t.Fatalf("cards %d, keys %q: want the new round's card under its key", len(cards), keys)
		}
	})
	t.Run("a card that lands for a round replaced meanwhile is dead", func(t *testing.T) {
		s, f, lark, id := retryService(t, acceptSend{err: errLarkDown})
		deployedCardless(t, s, f, id)
		old := askOf(s, id)
		next := &acceptAsk{MessageID: "om_next", Messages: []string{"om_next"}, AskedAt: old.AskedAt.Add(time.Minute)}
		lark.then(acceptSend{during: func() { // the next round, asked while the card goes out
			s.accept.mu.Lock()
			s.accept.asks[id] = next
			s.accept.mu.Unlock()
		}})
		s.retryAcceptance(time.Now())
		cards, _ := lark.sent()
		a := askOf(s, id)
		if len(cards) != 1 || a.MessageID != "om_next" || len(a.Earlier) != 1 || a.Earlier[0] != "om_pv1" {
			t.Fatalf("cards %d, ask %+v: the landed card must be an earlier round's", len(cards), a)
		}
		if got, earlier := s.acceptFor("om_pv1"); got != id || !earlier {
			t.Fatalf("the landed card is not dead: %q %v", got, earlier)
		}
	})
}

// After a restart the book is what was stored: a claim saved mid-flight (no time) and a null entry are dropped on load,
// never dereferenced, and no longer keep their task from being asked; an ask waiting for its card is posted by the
// next tick; one with its card is left alone.
func TestAcceptanceReloadedAsks(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	st := &memStore{}
	s := newServiceOn(t, f, st)
	s.ownerVerifiesAll = true
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		r := botReady
		r.PublicURL = cardlessWeb
		return r, nil
	})
	lark := newAcceptLark(f)
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	for _, id := range []string{"todo_wait", "todo_card", "todo_claim", "todo_nil"} {
		s.todos.todos[id] = &Todo{ID: id, Title: "修登录崩溃", Origin: "conv1"}
	}
	s.todos.mu.Unlock()
	asked := time.Now().UTC().Add(-5 * time.Minute)
	stored := map[string]*acceptAsk{
		"todo_nil":   nil,
		"todo_claim": {Earlier: []string{"om_old"}},
		"todo_wait":  {MachineID: "box", SessionID: "s1", Changed: "登录页不崩了", How: "横屏打开登录页", AskedAt: asked},
		"todo_card":  {MessageID: "om_c", Messages: []string{"om_c"}, Changed: "改了", AskedAt: asked.Add(-time.Hour)},
	}
	if err := st.PutSetting(ctx, acceptanceKey, stored); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	raw := st.vals[acceptanceKey]
	st.mu.Unlock()
	if !strings.Contains(string(raw), `"todo_nil":null`) {
		t.Fatalf("the null entry was not stored: %s", raw)
	}
	// The first look loads the book.
	if rows := s.awaitingAcceptance(ctx); len(rows) != 2 || rows[0].todo != "todo_card" || rows[1].todo != "todo_wait" {
		t.Fatalf("board after the load: %+v", rows)
	}
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 2 {
		t.Fatalf("snapshot total %d", total)
	}
	if askOf(s, "todo_claim") != nil || askOf(s, "todo_nil") != nil {
		t.Fatal("a claim left in flight or a null entry was kept")
	}
	if got, _ := s.acceptFor("om_old"); got != "" {
		t.Fatalf("acceptFor matched a dropped claim: %q", got)
	}
	s.retryAcceptance(time.Now())
	cards, keys := lark.sent()
	if len(cards) != 1 || len(keys) != 1 || keys[0] != acceptCardKey("todo_wait", asked) {
		t.Fatalf("cards %d, keys %q: want the waiting ask's card under its round's key", len(cards), keys)
	}
	if a := askOf(s, "todo_wait"); a.MessageID != "om_pv1" {
		t.Fatalf("waiting ask: %+v", a)
	}
	if a := askOf(s, "todo_card"); a.MessageID != "om_c" || len(a.Messages) != 1 {
		t.Fatalf("carded ask: %+v", a)
	}
	var saved map[string]*acceptAsk
	if _, err := st.Setting(ctx, acceptanceKey, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved["todo_claim"]; ok || len(saved) != 2 {
		t.Fatalf("saved after the retry: %v", saved)
	}
	// The task whose claim was dropped is asked again by its next deploy.
	td, _ := s.todoNow("todo_claim")
	s.askAcceptance(td, "box", "s1", deployedReply)
	if a := askOf(s, "todo_claim"); a == nil || a.AskedAt.IsZero() || a.MessageID == "" {
		t.Fatalf("the task with a dropped claim was not asked: %+v", a)
	}
}
