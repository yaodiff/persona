package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Lark card of a routine with facts (modules/briefing/card.go). All ids are placeholders.

// briefRunEnv is a morning routine that has run once on a fake hub, with Feishu paired and its pushes on.
type briefRunEnv struct {
	t     *testing.T
	s     *Service
	f     *fakeHub
	sess  proto.Session
	mu    sync.Mutex
	posts []map[string]any
	feat  *FeishuFeatures // what the owner has switched on (nil: main, pushes, reports)
}

const briefSummary = "今天先把登录崩溃验收掉。"

// newBriefRunEnv starts the morning routine (facts as given) and gives its conversation the model's answer; the
// board has two waiting cards and one task to verify.
func newBriefRunEnv(t *testing.T, facts bool) *briefRunEnv { return newBriefRunEnvWith(t, facts, nil) }

// newBriefRunEnvWith is newBriefRunEnv with more on the board when the run starts (its facts are worked out then).
func newBriefRunEnvWith(t *testing.T, facts bool, more func(e *briefRunEnv)) *briefRunEnv {
	t.Helper()
	if facts {
		needsBriefingModule(t)
	}
	return briefRunEnvWith(t, facts, more)
}

// briefRunEnvWith is newBriefRunEnvWith whether or not this build has the briefing module (modules_test.go).
func briefRunEnvWith(t *testing.T, facts bool, more func(e *briefRunEnv)) *briefRunEnv {
	t.Helper()
	f := newFakeHub().withMachine()
	e := &briefRunEnv{t: t, f: f}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		feat := FeishuFeatures{Main: true, Pushes: true, Reports: true}
		e.mu.Lock()
		if e.feat != nil {
			feat = *e.feat
		}
		e.mu.Unlock()
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: testWeb, Features: feat}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		e.mu.Lock()
		e.posts = append(e.posts, m)
		n := len(e.posts)
		e.mu.Unlock()
		return map[string]any{"message_id": fmt.Sprintf("om_brief_%d", n)}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	e.s = newStorelessService(t, f)
	e.s.fleet.interactions = map[string]*pending{}
	e.s.todos.loaded, e.s.todos.todos, e.s.todos.activeBySession = true, map[string]*Todo{}, map[[2]string]string{}
	e.s.accept.loaded, e.s.accept.asks = true, map[string]*acceptAsk{}
	setTasks(e.s, Task{ID: "morning", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatDaily, Time: "09:00", Enabled: true, Facts: facts})

	e.waiting("ix_old", "旧的问题", 90*time.Minute)
	e.waiting("ix_new", "新的问题", 5*time.Minute)
	e.verify("todo_v1", "修登录崩溃", "登录不再崩溃", 30*time.Hour)
	if more != nil {
		more(e)
	}

	sess, err := runNow(t, e.s, "morning")
	if err != nil {
		t.Fatal(err)
	}
	e.sess = sess
	f.setSessions("server", proto.Session{SessionID: sess.SessionID, Assistant: true})
	f.setItems(proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ 早报"}, proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: briefSummary})
	return e
}

func (e *briefRunEnv) waiting(id, question string, age time.Duration) {
	e.s.fleet.mu.Lock()
	defer e.s.fleet.mu.Unlock()
	e.s.fleet.interactions[id] = &pending{ix: proto.Interaction{ID: id, MachineID: "server", SessionID: "ag_" + id, Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: question}}, CreatedAt: time.Now().Add(-age)}}
}

func (e *briefRunEnv) verify(id, title, changed string, age time.Duration) {
	e.s.todos.todos[id] = &Todo{ID: id, Title: title, Origin: "conv1"}
	e.s.accept.asks[id] = &acceptAsk{MessageID: "om_" + id, Messages: []string{"om_" + id}, AskedAt: time.Now().Add(-age), MachineID: "server",
		SessionID: "ag_" + id, Changed: changed}
}

// features changes what the bot's owner has switched on.
func (e *briefRunEnv) features(f FeishuFeatures) {
	e.mu.Lock()
	e.feat = &f
	e.mu.Unlock()
	e.s.larkModule().ForgetStatus()
}

// mirror ends the routine's turn: the answer goes to Feishu.
func (e *briefRunEnv) mirror() {
	e.t.Helper()
	e.s.feishuMirror(context.Background(), e.sess.MachineID, e.sess.SessionID)
}

func (e *briefRunEnv) cards() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, p := range e.posts {
		c, _ := p["card"].(string)
		out = append(out, c)
	}
	return out
}

// With facts on, the routine's answer goes to Lark as one card made by Persona: the model's summary on top, under it the
// waiting cards, the last twelve hours and the task to verify with its Accept button.
func TestBriefRoutineSendsOneCard(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnv(t, true)
	e.mirror()
	cards := e.cards()
	if len(cards) != 1 {
		t.Fatalf("%d posts, want the one card", len(cards))
	}
	card := cards[0]
	at := func(want string) int {
		t.Helper()
		i := strings.Index(card, want)
		if i < 0 {
			t.Fatalf("the card has no %q:\n%s", want, card)
		}
		return i
	}
	order := []int{at(briefSummary), at("等你处理（2）"), at("旧的问题"), at("新的问题"), at("过去 12 小时"), at("待验收（1）"), at("请验收：修登录崩溃"), at("登录不再崩溃")}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("out of order %v:\n%s", order, card)
		}
	}
	if !strings.Contains(card, `"a":"accept"`) || !strings.Contains(card, `"id":"todo_v1"`) {
		t.Fatalf("the task to verify has no Accept button:\n%s", card)
	}
}

// With facts off the routine sends its answer as before: the model's own text, no sections of Persona's.
func TestBriefRoutineWithoutFactsSendsTheAnswerAsBefore(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnv(t, false)
	e.mirror()
	cards := e.cards()
	if len(cards) != 1 || !strings.Contains(cards[0], briefSummary) {
		t.Fatalf("posts: %q", cards)
	}
	for _, not := range []string{"等你处理", "待验收（", `"a":"accept"`, "旧的问题"} {
		if strings.Contains(cards[0], not) {
			t.Fatalf("a routine without facts says %q:\n%s", not, cards[0])
		}
	}
}

// Too many rows are not cut off in the middle of the card: each section lists briefRowsMax of them and says how many more,
// and the card stays one card inside the size Lark takes.
func TestBriefRoutineCountsWhatDoesNotFit(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		for i := 0; i < 11; i++ { // 13 waiting in all, 12 to verify
			e.waiting(fmt.Sprintf("ix_more_%02d", i), fmt.Sprintf("再来一个问题%02d", i), time.Duration(i+1)*time.Minute)
		}
		for i := 0; i < 11; i++ {
			e.verify(fmt.Sprintf("todo_more_%02d", i), fmt.Sprintf("另一件事%02d", i), "改了点什么", time.Duration(i+2)*time.Hour)
		}
	})
	e.mirror()
	cards := e.cards()
	if len(cards) != 1 {
		t.Fatalf("%d posts, want the one card", len(cards))
	}
	card := cards[0]
	if n := strings.Count(card, `"a":"accept"`); n != briefRowsMax {
		t.Errorf("%d Accept buttons, want %d (the rest are counted)", n, briefRowsMax)
	}
	for _, want := range []string{"等你处理（13）", "待验收（12）", "还有 3 项", "还有 2 项"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card does not say %q:\n%s", want, card)
		}
	}
	if len(card) > reportCardMaxBytes {
		t.Errorf("the card is %d bytes, past the %d it may take", len(card), reportCardMaxBytes)
	}
}

// at is hour:minute today in the owner's zone.
func (e *briefRunEnv) at(hour, minute int) time.Time {
	loc := e.s.location(context.Background())
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
}

// reminded is how many "still not verified" cards were sent.
func (e *briefRunEnv) reminded() int {
	n := 0
	for _, c := range e.cards() {
		if strings.Contains(c, "还没验收") {
			n++
		}
	}
	return n
}

// The Accept button of the routine's card verifies the round it was asked in: the button as the card wrote it works, and
// once the task has been asked again (another round) the old card's button is answered "already handled" and verifies
// nothing (#4853).
func TestBriefAcceptButtonIsForItsRoundOnly(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnv(t, true)
	e.mirror()
	var card struct {
		Body struct {
			Elements []struct {
				Columns []struct {
					Elements []struct {
						Behaviors []struct {
							Value map[string]any `json:"value"`
						} `json:"behaviors"`
					} `json:"elements"`
				} `json:"columns"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(e.cards()[0]), &card); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	for _, el := range card.Body.Elements {
		for _, c := range el.Columns {
			for _, b := range c.Elements {
				for _, v := range b.Behaviors {
					if v.Value["a"] == "accept" {
						value = v.Value
					}
				}
			}
		}
	}
	if value == nil || value["id"] != "todo_v1" || value["asked_at"] == "" {
		t.Fatalf("no Accept button with its round on the card: %v", value)
	}
	press := func(v map[string]any) *botcard.CardAction {
		var a botcard.CardAction
		a.Action.Value = v
		a.Context.MessageID = "om_brief_1"
		return &a
	}
	ctx := context.Background()

	// The task is asked again: this card's button is an old round's.
	e.s.accept.mu.Lock()
	e.s.accept.asks["todo_v1"].AskedAt = time.Now().Add(-time.Minute)
	e.s.accept.mu.Unlock()
	if r := e.s.larkModule().Action(ctx, press(value), ""); r == nil || r.Toast == nil || !strings.Contains(r.Toast.Content, "已经处理过了") {
		t.Fatalf("an old round's button was not answered as handled: %+v", r)
	}
	if !askOf(e.s, "todo_v1").AcceptedAt.IsZero() {
		t.Fatal("an old round's button verified the new round")
	}

	// The button of the round that waits now works.
	now := map[string]any{"a": "accept", "id": "todo_v1", "asked_at": askOf(e.s, "todo_v1").AskedAt.UTC().Format(timeKeyLayout)}
	if r := e.s.larkModule().Action(ctx, press(now), ""); r == nil || r.Toast == nil || !strings.Contains(r.Toast.Content, "已验收") {
		t.Fatalf("the current round's button did not verify it: %+v", r)
	}
	if askOf(e.s, "todo_v1").AcceptedAt.IsZero() {
		t.Fatal("the task is not verified")
	}
}

// The briefing card carries the tasks to verify, so the "still not verified" cards are not sent again that day.
func TestBriefCardReplacesThatDaysAcceptanceReminders(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnv(t, true)
	e.mirror()
	n := len(e.cards())
	e.s.remindAcceptance(e.at(23, 0))
	if got := len(e.cards()); got != n || e.reminded() != 0 {
		t.Fatalf("%d cards after the day's reminder time, was %d: the task was asked again", got, n)
	}
}

// If the briefing has not gone out an hour after its time, the reminders go as they did before.
func TestBriefReminderComesWhenTheBriefingDoesNot(t *testing.T) {
	needsLarkLayer(t)
	e := newBriefRunEnv(t, true)
	e.s.remindAcceptance(e.at(9, 30)) // the briefing's hour is not over
	if e.reminded() != 0 {
		t.Fatal("reminded within the hour the briefing has")
	}
	e.s.remindAcceptance(e.at(10, 5))
	if e.reminded() != 1 {
		t.Fatalf("%d reminders an hour after the briefing's time, want 1: %q", e.reminded(), e.cards())
	}
}

// The model is given the facts the card lists, in the message that starts the run; without facts it is not.
func TestBriefRoutineGivesTheModelTheSameFacts(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	text := newBriefRunEnv(t, true)
	sent, _ := text.f.lastSend(t)["text"].(string)
	for _, want := range []string{"Persona 算好的事实", "等用户处理的 2 件", "旧的问题", "等用户验收的 1 件", "修登录崩溃", "登录不再崩溃"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the message to the model lacks %q:\n%s", want, sent)
		}
	}
	plain, _ := newBriefRunEnv(t, false).f.lastSend(t)["text"].(string)
	if strings.Contains(plain, "算好的事实") || strings.Contains(plain, "旧的问题") {
		t.Errorf("a routine without facts is given them:\n%s", plain)
	}
}

// A long list of what finished does not push out what did not go through: the failed rows come first in the last twelve
// hours' section (#4124), the finished ones after them up to the section's ten rows.
func TestBriefShowsFailuresBeforeALongListOfDoneTasks(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		for i := 0; i < 12; i++ {
			at := time.Now().Add(-time.Duration(i+1) * time.Minute)
			e.s.todos.todos[fmt.Sprintf("todo_done_%02d", i)] = &Todo{ID: fmt.Sprintf("todo_done_%02d", i), Title: fmt.Sprintf("做完的事%02d", i), Origin: "conv1", DoneAt: &at}
		}
		at := time.Now().Add(-3 * time.Hour)
		e.s.todos.todos["todo_fail"] = &Todo{ID: "todo_fail", Title: "没开起来的事", Origin: "conv1", DoneAt: &at, StartError: "machine offline"}
	})
	e.mirror()
	card := e.cards()[0]
	for _, want := range []string{"做完 12 · 没成 1", "没开起来的事", "没开起来", "machine offline", "做完的事00", "还有 3 项"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q:\n%s", want, card)
		}
	}
	if strings.Index(card, "没开起来的事") > strings.Index(card, "做完的事00") {
		t.Errorf("the failed row is after the finished ones:\n%s", card)
	}
}

// Only the turn of the run itself gets the normal card. A message the owner sends into the routine's conversation later is
// answered as any other. A failed model turn does not mirror its raw error: the scheduled-run watcher sends the fixed-facts
// fallback once instead (briefing_fallback_test.go), and a later successful retry has facts of its own.
func TestBriefCardIsForTheRunsOwnTurnOnly(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefRunEnv(t, true)
	setSession := func(s proto.Session) { e.f.setSessions("server", s) }

	setSession(proto.Session{SessionID: e.sess.SessionID, Assistant: true, TurnError: "API Error: 529"})
	e.mirror()
	if cards := e.cards(); len(cards) != 0 {
		t.Fatalf("a failed turn mirrored its raw error instead of waiting for the facts fallback: %q", cards)
	}

	setSession(proto.Session{SessionID: e.sess.SessionID, Assistant: true})
	e.f.setItems(proto.Item{ID: "u2", Role: "user", Kind: proto.KindText, Text: "第二条是什么意思？"}, proto.Item{ID: "a2", Role: "assistant", Kind: proto.KindText, Text: "是说登录页崩溃。"})
	e.mirror()
	if cards := e.cards(); len(cards) != 1 || strings.Contains(cards[0], "等你处理") {
		t.Fatalf("a message of the owner's got the briefing: %q", cards)
	}

	e.f.setItems(proto.Item{ID: "u3", Role: "user", Kind: proto.KindText, Text: "⏰ 早报"}, proto.Item{ID: "a3", Role: "assistant", Kind: proto.KindText, Text: briefSummary})
	e.mirror()
	if cards := e.cards(); len(cards) != 2 || !strings.Contains(cards[1], "等你处理（2）") {
		t.Fatalf("the run's own turn lost its briefing to the others: %q", cards)
	}
}

// The reminders wait an hour for the briefing only when its card can go out: not with the Lark bot's pushes off, not on a
// day the routine does not run, and never past midnight.
func TestBriefReminderGraceOnlyWhenTheCardCanGoOut(t *testing.T) {
	needsLarkLayer(t)
	setMorning := func(e *briefRunEnv, change func(t *Task)) {
		sc := e.s.loadSchedule(context.Background())
		sc.mu.Lock()
		for i := range sc.tasks {
			if sc.tasks[i].ID == "morning" {
				change(&sc.tasks[i])
			}
		}
		sc.mu.Unlock()
	}

	t.Run("a day it does not run", func(t *testing.T) {
		e := newBriefRunEnv(t, true)
		today := int(time.Now().In(e.s.location(context.Background())).Weekday())
		if today == 0 {
			today = 7
		}
		setMorning(e, func(t *Task) {
			for d := 1; d <= 7; d++ {
				if d != today {
					t.Weekdays = append(t.Weekdays, d)
				}
			}
		})
		e.s.remindAcceptance(e.at(9, 30))
		if e.reminded() != 1 {
			t.Fatalf("%d reminders at 09:30 on a day with no briefing, want 1", e.reminded())
		}
	})
	t.Run("a late briefing", func(t *testing.T) {
		e := newBriefRunEnv(t, true)
		setMorning(e, func(t *Task) { t.Time = "23:30" })
		e.s.remindAcceptance(e.at(23, 59))
		if e.reminded() != 1 {
			t.Fatalf("%d reminders at 23:59 for a briefing at 23:30, want 1", e.reminded())
		}
	})
	t.Run("pushes off", func(t *testing.T) {
		e := newBriefRunEnv(t, true)
		e.features(FeishuFeatures{Main: true, Reports: true})
		e.s.remindAcceptance(e.at(9, 30))
		if e.reminded() != 1 {
			t.Fatalf("%d reminders at 09:30 with pushes off, want 1", e.reminded())
		}
	})
}

// Without the Lark card (the bot's pushes are off) the model is told to list the rows itself; the web app has the item, the
// phone apps do not yet.
func TestBriefRoutineWithoutACardTellsTheModelToListTheRows(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	sent, _ := newBriefRunEnvWith(t, true, func(e *briefRunEnv) { e.features(FeishuFeatures{Main: true}) }).f.lastSend(t)["text"].(string)
	if !strings.Contains(sent, "这次不会发飞书卡片") || strings.Contains(sent, "你不用再逐条列") || !strings.Contains(sent, "旧的问题") {
		t.Errorf("a run without a card:\n%s", sent)
	}
}

const briefRowsMax = 10
const briefKeep = 3 * time.Hour

func (s *Service) briefCard(f *briefFacts, label, answer string) (*botcard.Card, string, []string) {
	pages, plain, shown := s.scheduleCards(f, label, answer, "", "", "")
	return (*botcard.Card)(pages[0].Card), plain, shown
}
