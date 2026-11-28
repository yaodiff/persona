package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The "Please verify" ask is a fact the hub keeps whenever a to-do's work is deployed; the Lark card is one way of
// showing it. These tests cover an ask that has no card (NoCard): the bot is not set up, or its task reports are off.

const cardlessWeb = "https://hub.example.com"

// cardlessService is acceptService with the bot's status as given, and a task started and working on box/s1.
func cardlessService(t *testing.T, st FeishuStatus) (*Service, *fakeHub, func() []string, string) {
	t.Helper()
	s, f, posts := acceptService(t)
	st.PublicURL = cardlessWeb
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return st, nil })
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	return s, f, posts, todo.ID
}

// closeAndReopen is a to-do the hub's judge closed and the owner (or a change asked for) reopened.
func closeAndReopen(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	if ok, err := s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	if err := s.reopenTodo(ctx, id); err != nil {
		t.Fatal(err)
	}
	if onlyTodo(s).ReopenedAt == nil {
		t.Fatal("not reopened")
	}
}

// botNotSetUp is what feishu.status says before the bot is switched on and paired.
var botNotSetUp = FeishuStatus{}

// deployedCardless runs the deploying turn and waits for the ask the hub keeps.
func deployedCardless(t *testing.T, s *Service, f *fakeHub, id string) *acceptAsk {
	t.Helper()
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "the ask", func() bool { a := askOf(s, id); return a != nil && !a.AskedAt.IsZero() })
	return askOf(s, id)
}

// Bot not set up: a deployed to-do gets an ask with the three lines and no card; the board's rows and the snapshot list it.
func TestAcceptanceIsKeptWithoutABot(t *testing.T) {
	ctx := context.Background()
	s, f, posts, id := cardlessService(t, botNotSetUp)
	a := deployedCardless(t, s, f, id)
	if a.MessageID != "" || len(a.Messages) != 0 || !a.NoCard {
		t.Fatalf("a card-less ask has no card and says so: %+v", a)
	}
	if a.Changed != "飞书机器人多了任务进展开关" || a.Link != "https://hub.example.com/settings?tab=feishu" || a.MachineID != "box" || a.SessionID != "s1" {
		t.Fatalf("lines: %+v", a)
	}
	if got := posts(); len(got) != 0 { // the ask is stored after the decision to post or not
		t.Fatalf("posted with the bot not set up: %q", got)
	}
	if rows := s.awaitingAcceptance(ctx); len(rows) != 1 || rows[0].todo != id || !rows[0].since.Equal(a.AskedAt) {
		t.Fatalf("awaiting: %+v", rows)
	}
	if got, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 1 || len(got) != 1 || got[0].Changed != a.Changed {
		t.Fatalf("snapshot: %+v total=%d", got, total)
	}
	// Another turn that says deployed does not ask twice.
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	time.Sleep(6 * testReportDelay)
	if b := askOf(s, id); !b.AskedAt.Equal(a.AskedAt) {
		t.Fatalf("asked again: %+v then %+v", a, b)
	}
}

// The briefing's "To accept" section lists the card-less ask with the round the Accept button is made for.
func TestAcceptanceWithoutABotIsInTheBriefing(t *testing.T) {
	needsBriefingModule(t)
	ctx := context.Background()
	s, f, _, id := cardlessService(t, botNotSetUp)
	a := deployedCardless(t, s, f, id)
	facts := s.briefFactsAt(ctx, time.Now(), "")
	if len(facts.Accept) != 1 || facts.Accept[0].ID != id || facts.Changed[id] != a.Changed {
		t.Fatalf("facts: %+v changed=%v", facts.Accept, facts.Changed)
	}
	body := s.briefBodyOf(facts, "Morning")
	if len(body.Accept) != 1 || body.Accept[0].ID != id || !body.Accept[0].AskedAt.Equal(a.AskedAt) || body.Accept[0].Title != "修登录崩溃" {
		t.Fatalf("brief item: %+v", body.Accept)
	}
	if !strings.Contains(s.briefModelText(facts), a.Changed) {
		t.Fatalf("the model is not told what changed:\n%s", s.briefModelText(facts))
	}
}

// The Accept button of the web `brief` item verifies a card-less ask: stage verify; a button of an earlier round is 409.
func TestAcceptButtonVerifiesACardlessAsk(t *testing.T) {
	s, f, _, id := cardlessService(t, botNotSetUp)
	a := deployedCardless(t, s, f, id)
	post := func(askedAt time.Time) int {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+id+"/accept", strings.NewReader(`{"asked_at":"`+askedAt.UTC().Format(timeKeyLayout)+`"}`)))
		return rec.Code
	}
	if code := post(a.AskedAt.Add(-time.Hour)); code != http.StatusConflict || onlyTodo(s).Stage == "verify" || !askOf(s, id).AcceptedAt.IsZero() {
		t.Fatalf("a button of another round: %d", code)
	}
	// The to-do is reopened after the ask: the button made for that ask is an earlier round's now.
	closeAndReopen(t, s, id)
	if code := post(a.AskedAt); code != http.StatusConflict || onlyTodo(s).Stage == "verify" {
		t.Fatalf("a button from before the reopen: %d", code)
	}
	// The next deploy asks again, card-less; its button verifies.
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "the next round's ask", func() bool { b := askOf(s, id); return b != nil && b.AskedAt.After(a.AskedAt) })
	b := askOf(s, id)
	if code := post(b.AskedAt); code != http.StatusOK {
		t.Fatalf("the round that waits: %d", code)
	}
	if got := askOf(s, id); got.AcceptedAt.IsZero() || onlyTodo(s).Stage != "verify" {
		t.Fatalf("not verified: %+v stage=%q", got, onlyTodo(s).Stage)
	}
	if code := post(b.AskedAt); code != http.StatusConflict {
		t.Fatalf("pressed again: %d", code)
	}
	if rows := s.awaitingAcceptance(context.Background()); len(rows) != 0 {
		t.Fatalf("still awaiting: %+v", rows)
	}
}

// Bot ready but task reports off: the same, no card.
func TestAcceptanceWithReportsOffHasNoCard(t *testing.T) {
	s, f, posts, id := cardlessService(t, FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", Features: FeishuFeatures{Main: true, Cards: true}})
	a := deployedCardless(t, s, f, id)
	if a.MessageID != "" || !a.NoCard {
		t.Fatalf("ask: %+v", a)
	}
	if got := posts(); len(got) != 0 {
		t.Fatalf("posted with reports off: %q", got)
	}
	if rows := s.awaitingAcceptance(context.Background()); len(rows) != 1 {
		t.Fatalf("awaiting: %+v", rows)
	}
}

// Bot ready: unchanged, the ask has its card and is not marked card-less.
func TestAcceptanceWithABotKeepsItsCard(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts, id := cardlessService(t, FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", Features: FeishuFeatures{Main: true, Cards: true, Reports: true}})
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, id, posts)
	if a := askOf(s, id); a.NoCard || len(a.Messages) != 1 {
		t.Fatalf("ask: %+v", a)
	}
	if raw, _ := json.Marshal(askOf(s, id)); strings.Contains(string(raw), "no_card") {
		t.Fatalf("a carded ask is stored with no_card: %s", raw)
	}
}

// A bot status that cannot be read (the manager away) is a card for later, not a card never due: the ask is kept
// waiting for its card, not card-less, and the next tick posts it once the status reads (acceptance_retry_test.go). A
// card whose send failed is the same (TestAcceptanceCardRetriedAfterAFailedSend).
func TestAcceptanceStatusErrorIsNotNoCard(t *testing.T) {
	needsLarkLayer(t)
	waiting := func(t *testing.T, s *Service, id string, posts func() []string) {
		t.Helper()
		if a := askOf(s, id); a == nil || a.AskedAt.IsZero() || a.MessageID != "" || a.NoCard {
			t.Fatalf("not kept waiting for its card: %+v", a)
		}
		if got := posts(); len(got) != 0 {
			t.Fatalf("posted: %q", got)
		}
	}
	t.Run("status unreadable", func(t *testing.T) {
		s, f, posts, id := cardlessService(t, botNotSetUp)
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return nil, errors.New("manager away") })
		s.larkModule().ForgetStatus()
		s.askAcceptance(onlyTodo(s), "box", "s1", deployedReply)
		waiting(t, s, id, posts)
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return botReady, nil })
		s.larkModule().ForgetStatus()
		s.retryAcceptance(time.Now())
		if a := askOf(s, id); a == nil || a.MessageID == "" || countAcceptCards(posts()) != 1 {
			t.Fatalf("the card for later did not go out: %+v, %d cards", a, countAcceptCards(posts()))
		}
	})
}

// The morning reminder is a card: a card-less ask gets none and nothing fails.
func TestCardlessAskIsNotReminded(t *testing.T) {
	ctx := context.Background()
	s, f, posts, id := cardlessService(t, botNotSetUp)
	deployedCardless(t, s, f, id)
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { // the bot is set up later
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: cardlessWeb, Features: FeishuFeatures{Main: true, Cards: true, Reports: true}}, nil
	})
	s.larkModule().ForgetStatus()
	s.accept.mu.Lock()
	s.accept.asks[id].AskedAt = time.Now().Add(-30 * time.Hour)
	s.accept.mu.Unlock()
	loc := s.location(ctx)
	now := time.Now().In(loc)
	s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc))
	if got := posts(); len(got) != 0 {
		t.Fatalf("a card for a card-less ask: %q", got)
	}
	if a := askOf(s, id); a == nil || !a.RemindedAt.IsZero() || !a.NoCard {
		t.Fatalf("ask after the sweep: %+v", a)
	}
	// It is still listed, for the briefing and the board.
	if rows := s.awaitingAcceptance(ctx); len(rows) != 1 {
		t.Fatalf("awaiting: %+v", rows)
	}
}

// A card-less ask whose to-do the owner closed, a review closed, or that was reopened is dropped as a carded one is.
func TestCardlessAskEndsLikeACardedOne(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		end  func(s *Service, id string)
	}{
		{"closed by the owner", func(s *Service, id string) {
			done := time.Now()
			s.todos.mu.Lock()
			s.todos.todos[id].DoneAt, s.todos.todos[id].DoneBy, s.todos.todos[id].DoneReason = &done, "", ""
			s.todos.mu.Unlock()
		}},
		{"closed by a review", func(s *Service, id string) {
			if ok, err := s.reviewDoneTodo(ctx, id, "验收通过", true); err != nil || !ok {
				t.Fatalf("review done: %v %v", ok, err)
			}
		}},
		{"reopened", func(s *Service, id string) {
			closeAndReopen(t, s, id)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, f, _, id := cardlessService(t, botNotSetUp)
			deployedCardless(t, s, f, id)
			c.end(s, id)
			if rows := s.awaitingAcceptance(ctx); len(rows) != 0 {
				t.Fatalf("still awaiting: %+v", rows)
			}
			if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 0 || askOf(s, id) != nil {
				t.Fatalf("snapshot total=%d, ask kept=%v", total, askOf(s, id) != nil)
			}
		})
	}
	t.Run("closed by the hub's judge stays", func(t *testing.T) {
		s, f, _, id := cardlessService(t, botNotSetUp)
		deployedCardless(t, s, f, id)
		if ok, err := s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
			t.Fatalf("auto done: %v %v", ok, err)
		}
		if td := onlyTodo(s); td.DoneAt == nil || td.DoneBy != db.DoneByAuto {
			t.Fatalf("todo: %+v", td)
		}
		if rows := s.awaitingAcceptance(ctx); len(rows) != 1 {
			t.Fatalf("the judge's close is the normal state, the owner still verifies: %+v", rows)
		}
	})
}

// Verifying a card-less ask says so in the to-do's Lark topic, as verifying on a card does.
func TestCardlessAcceptSaysSoInTheTopic(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	e := newPostEnv(t)
	id := e.bound("验收", testThread)
	e.s.accept.mu.Lock()
	e.s.loadAcceptLocked(ctx)
	e.s.accept.asks[id] = &acceptAsk{AskedAt: time.Now(), NoCard: true}
	e.s.accept.mu.Unlock()
	if !e.s.accepted(ctx, id, "") {
		t.Fatal("not accepted")
	}
	e.clk.advance(threadPostSettle)
	mustContain(t, "accepted line", textOf(e.onlyPost(t)), "已验收", testWeb+"/todos/"+id)
}

// Verifying a card-less ask plans the report back to where the request came from, as verifying on a card does.
func TestCardlessAcceptPlansTheSourceReport(t *testing.T) {
	needsLarkLayer(t)
	forStores(t, func(t *testing.T, e *askEnv, database *db.DB) {
		ctx := context.Background()
		e.ask(ownerAsk("om_a1", "/ai 修登录崩溃"))
		id := onlyTodo(e.s).ID
		e.outcome(id, "已修好")
		done := time.Now().UTC()
		if _, ok := e.s.applyTodoRow(ctx, id, db.TodoRow{ID: id, Title: "修登录崩溃", DoneAt: &done, UpdatedAt: done}, nil); !ok {
			t.Fatal("not applied")
		}
		e.s.accept.mu.Lock()
		e.s.loadAcceptLocked(ctx)
		if e.s.accept.asks == nil {
			e.s.accept.asks = map[string]*acceptAsk{}
		}
		e.s.accept.asks[id] = &acceptAsk{AskedAt: time.Now(), NoCard: true}
		e.s.accept.mu.Unlock()
		if !e.s.accepted(ctx, id, "") {
			t.Fatal("not accepted")
		}
		e.clock.advance(sourceReportWait + time.Second)
		if got := e.notices(); len(got) != 1 {
			t.Fatalf("notices %q", got)
		}
	})
}

// The claim a deploy turn puts in the book while it works out the lines and posts is not an ask yet: it has no time, so it
// is neither listed nor accepted. (A card-less ask has no message id either; the time is what tells them apart.)
func TestAcceptanceClaimInFlightIsNotAnAsk(t *testing.T) {
	ctx := context.Background()
	s, _, _, id := cardlessService(t, botNotSetUp)
	s.accept.mu.Lock()
	s.loadAcceptLocked(ctx)
	s.accept.asks[id] = &acceptAsk{}
	s.accept.mu.Unlock()
	if rows := s.awaitingAcceptance(ctx); len(rows) != 0 {
		t.Fatalf("awaiting: %+v", rows)
	}
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 0 {
		t.Fatalf("snapshot total %d", total)
	}
	if s.accepted(ctx, id, "") || onlyTodo(s).Stage == "verify" {
		t.Fatal("a claim in flight was verified")
	}
}
