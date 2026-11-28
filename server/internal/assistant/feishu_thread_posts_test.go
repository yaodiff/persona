package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// A todo bound to a chat topic tells it what happens (the lark module's thread_posts.go, #5279): a decision, an Agent's turn
// ending or needing the user, the todo accepted or closed, in one short line a minute at most. All ids are placeholders.

// fakeClock is the poster's clock and timer: nothing fires until advance runs it, in order.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	f  func()
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_800_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration, f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), f})
}

// advance moves the clock by d, running the timers that fall due on the way, each with the clock at its own time.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		i := slices.IndexFunc(c.timers, func(t fakeTimer) bool { return !t.at.After(target) })
		if i < 0 {
			c.now = target
			c.mu.Unlock()
			return
		}
		t := c.timers[i]
		c.timers = slices.Delete(c.timers, i, i+1)
		if t.at.After(c.now) {
			c.now = t.at
		}
		c.mu.Unlock()
		t.f()
	}
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// waitPending waits (real time) for n timers: for the flows that run on goroutines.
func (c *fakeClock) waitPending(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for c.pending() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d timers waiting, want %d", c.pending(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (c *fakeClock) install(s *Service) { s.tposts.now, s.tposts.after = c.Now, c.After }

const testWeb = "https://hub.example"

// postEnv is a paired bot whose chats include testChat, on the test database, with the poster on a fake clock.
type postEnv struct {
	*threadEnv
	database *db.DB
	clk      *fakeClock
	chats    []string
}

func newPostEnv(t *testing.T) *postEnv {
	t.Helper()
	database := todoTestDB(t)
	e := &postEnv{threadEnv: newThreadEnv(t, database), database: database, clk: newFakeClock(), chats: []string{"oc_private", testChat}}
	e.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0), Features: e.features,
			PublicURL: testWeb, Chats: e.chats, TopicTodoModes: e.topicModes}, nil
	})
	e.clk.install(e.s)
	return e
}

// bound is a todo bound to a topic of its own (the thread is the todo's; the chat is the test chat), with a message of
// the topic known to reply to.
func (e *postEnv) bound(title, thread string) string {
	e.t.Helper()
	id := e.create(title).ID
	seedTopicAnchor(e.t, e.s, e.f, testChat, thread, "om_"+thread)
	e.must("PUT", "/api/v1/todos/"+id+"/thread", `{"channel":"feishu","chat_id":"`+testChat+`","thread_id":"`+thread+`"}`)
	return id
}

func (e *postEnv) decide(id, choice string) {
	e.t.Helper()
	e.must("POST", "/api/v1/todos/"+id+"/decisions", fmt.Sprintf(`{"question":"发哪个平台","choice":%q}`, choice))
}

// posts are the lines the bot said (feishu.text) so far.
func (e *postEnv) posts() []map[string]any { return topicNotes(e.f) }

func (e *postEnv) onlyPost(t *testing.T) map[string]any {
	t.Helper()
	got := e.posts()
	if len(got) != 1 {
		t.Fatalf("%d lines said, want 1: %v", len(got), got)
	}
	return got[0]
}

func textOf(p map[string]any) string { s, _ := p["text"].(string); return s }

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Fatalf("%s missing %q: %q", what, w, got)
		}
	}
}

// 1. A decision noted on a bound todo is said in its topic, with the link back to the todo, a moment later.
func TestThreadPostsANewDecision(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.bound("发版", testThread)
	e.decide(id, "先发安卓")
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said before the settle time: %v", got)
	}
	e.clk.advance(threadPostSettle)
	text := textOf(e.onlyPost(t))
	mustContain(t, "line", text, "新决策", "先发安卓", testWeb+"/todos/"+id)
	if strings.Contains(text, "\n") {
		t.Fatalf("not one line: %q", text)
	}
}

// 2. What happens within a minute is one line; the next line is not before the minute is over.
func TestThreadPostsJoinWithinAMinute(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.bound("发版", testThread)
	e.decide(id, "先发安卓")
	e.decide(id, "再发 iOS")
	e.decide(id, "最后发网页")
	if n := e.clk.pending(); n != 1 {
		t.Fatalf("%d timers for one todo's burst, want 1", n)
	}
	e.clk.advance(threadPostSettle)
	mustContain(t, "joined line", textOf(e.onlyPost(t)), "3 条新决策", "先发安卓", "再发 iOS", "最后发网页")

	e.decide(id, "第四条")
	e.clk.advance(30 * time.Second)
	if got := e.posts(); len(got) != 1 {
		t.Fatalf("a second line inside the minute: %v", got)
	}
	e.clk.advance(30 * time.Second)
	got := e.posts()
	if len(got) != 2 {
		t.Fatalf("%d lines once the minute is over, want 2: %v", len(got), got)
	}
	if text := textOf(got[1]); !strings.Contains(text, "第四条") || strings.Contains(text, "先发安卓") {
		t.Fatalf("second line: %q", text)
	}
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 2 {
		t.Fatalf("a line with nothing new: %v", got)
	}
}

// Many decisions are counted, and the line quotes only the first few.
func TestThreadPostsQuoteFewDecisions(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.bound("发版", testThread)
	for i := 1; i <= 5; i++ {
		e.decide(id, fmt.Sprintf("决定%d", i))
	}
	e.clk.advance(threadPostSettle)
	text := textOf(e.onlyPost(t))
	mustContain(t, "line", text, "5 条新决策", "决定1", "决定3", "…")
	if strings.Contains(text, "决定4") {
		t.Fatalf("quoted more than %d: %q", threadPostShown, text)
	}
}

// 3. What the user said in the topic is already there: not said again (and a bot's own line would not come back as a
// reply). The other sources are said.
func TestThreadPostsNotWhatTheTopicSaid(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.bound("发版", testThread)
	e.deliver(topicEvent("om_t1", "先发安卓"))
	if list := decisionsOf(t, e.database, id); len(list) != 1 || list[0].Source != db.DecisionThread {
		t.Fatalf("decisions %+v", list)
	}
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("a thread decision was said back: %v", got)
	}

	// Every other source goes.
	store := e.s.todoDB()
	for i, src := range []string{db.DecisionManual, db.DecisionCard, db.DecisionAgent, db.DecisionOutcome} {
		id := e.bound("源"+src, fmt.Sprintf("omt_src%d", i))
		row := db.TodoDecisionRow{ID: "dec_" + src, TodoID: id, Question: "问", Choice: "答" + src, Source: src, CreatedAt: decisionNow()}
		if ok, err := e.s.insertDecision(context.Background(), store, row); err != nil || !ok {
			t.Fatalf("%s: %v %v", src, ok, err)
		}
		e.clk.advance(threadPostEvery)
		got := e.posts()
		if len(got) != i+1 || !strings.Contains(textOf(got[i]), "答"+src) {
			t.Fatalf("%s not said: %v", src, got)
		}
	}
	// The same word from an Agent through its own tool.
	id = e.bound("工具", "omt_tool")
	e.must("PUT", "/api/v1/todos/"+id+"/agents/box/agent01", "")
	args, _ := json.Marshal(map[string]string{"question": "用哪个库", "choice": "pgx"})
	if _, err := e.s.agentDecisionTool(context.Background(), "box", "agent01", "todo_decision_add", args, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.clk.advance(threadPostEvery)
	if got := e.posts(); len(got) != 5 || !strings.Contains(textOf(got[4]), "pgx") {
		t.Fatalf("an Agent's decision not said: %v", got)
	}
}

// The bot's own line in the topic is no reply: it is not from the owner (a bot's messages carry no user sender), so the
// topic path drops it before anything is recorded or handed on.
func TestThreadBotsOwnLineIsNoReply(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newPostEnv(t)
	id := e.bound("发版", testThread)
	e.must("PUT", "/api/v1/todos/"+id+"/agents/box/agent01", "")
	ev := topicEvent("om_bot", "新决策：先发安卓 "+testWeb+"/todos?id="+id)
	ev.Sender.SenderType, ev.Sender.SenderID.OpenID = "app", "ou_bot"
	e.deliver(ev)
	if list := decisionsOf(t, e.database, id); len(list) != 0 {
		t.Fatalf("recorded %+v", list)
	}
	if got := handoffs(e.f); len(got) != 0 {
		t.Fatalf("handed on %+v", got)
	}
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said %v", got)
	}
}

// The agent side: a finished turn and a card for the user, of an agent working on a bound todo, each said.
type fleetPost struct {
	ft  *fleetTest
	clk *fakeClock
}

func newFleetPost(t *testing.T) *fleetPost {
	t.Helper()
	ft := newFleetTest(t)
	fastDone(ft)
	empty := func(json.RawMessage) (any, error) { return map[string]any{}, nil }
	ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: testWeb, Chats: []string{testChat},
			Features: FeishuFeatures{Main: true, Cards: true, AllCards: true, Sync: true, Pushes: true}}, nil
	})
	ft.f.answerApp(AppFeishu, FeishuText, empty)
	ft.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return map[string]any{"message_id": "om_x"}, nil })
	clk := newFakeClock()
	clk.install(ft.s)
	seedTopicAnchor(t, ft.s, ft.f, testChat, testThread, "om_anchor")
	putTodo(ft.s, Todo{ID: "todo_f", Title: "修登录", Thread: &TodoThread{Channel: "feishu", ChatID: testChat, ThreadID: testThread},
		Agents: []TodoAgent{{MachineID: "mac", SessionID: "s1"}}})
	return &fleetPost{ft, clk}
}

func (p *fleetPost) lines() []map[string]any { return p.ft.f.appParams(AppFeishu, FeishuText) }

func (p *fleetPost) ask(t *testing.T, id, summary string) {
	p.askKind(t, id, proto.IxQuestion, summary)
}

// askKind puts a card of that kind to the user and waits until the topic's poster has decided what to say of it (it
// decides on a goroutine of its own): the line is queued by then, or it is known there is none.
func (p *fleetPost) askKind(t *testing.T, id, kind, summary string) {
	t.Helper()
	decided := make(chan struct{}, 4)
	p.ft.s.tposts.asked = func() { decided <- struct{}{} }
	ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: kind, OutwardWrite: kind == proto.IxPermission, Summary: summary, SessionTitle: "修登录"}
	go func() { _, _ = p.ft.s.waitInteraction(context.Background(), ix) }()
	p.ft.waitPending(1)
	select {
	case <-decided:
	case <-time.After(5 * time.Second):
		t.Fatal("the poster never decided about the card")
	}
}

// 4. The agent's turn ends: a line; it needs the user: a line.
func TestThreadPostsAgentFinishedAndNeedsYou(t *testing.T) {
	needsLarkLayer(t)
	p := newFleetPost(t)
	driveAgentDone(p.ft, agentSession())
	p.clk.waitPending(t, 1)
	p.clk.advance(threadPostSettle)
	got := p.lines()
	if len(got) != 1 {
		t.Fatalf("%d lines for the finished turn: %v", len(got), got)
	}
	mustContain(t, "finished line", textOf(got[0]), "这一轮结束", "修登录", testWeb+"/todos/todo_f")
	if g := got[0]; g["chat_id"] != testChat || g["in_thread"] != true || g["reply_to"] != "om_anchor" || g["no_detour"] != true {
		t.Fatalf("where it went: %v", g)
	}

	p.clk.advance(threadPostEvery)
	p.ask(t, "ix_1", "要不要删除缓存")
	p.clk.waitPending(t, 1)
	p.clk.advance(threadPostSettle)
	got = p.lines()
	if len(got) != 2 {
		t.Fatalf("%d lines after the card: %v", len(got), got)
	}
	// The card is in the DM (no run asked it in the topic): the topic is told so, not what the card asks.
	mustContain(t, "needs line", textOf(got[1]), "Agent 修登录 需要你，已发到私聊", testWeb+"/todos/todo_f")
	if strings.Contains(textOf(got[1]), "要不要删除缓存") {
		t.Fatalf("the card's question went to the group: %q", textOf(got[1]))
	}
}

// With the cards switch off there is no card anywhere: the line is the notice, and carries what is asked.
func TestThreadPostsNeedsYouWithNoCardCarriesTheAsk(t *testing.T) {
	needsLarkLayer(t)
	p := newFleetPost(t)
	p.ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: testWeb, Chats: []string{testChat},
			Features: FeishuFeatures{Main: true, Cards: false}}, nil
	})
	p.ask(t, "ix_4", "要不要删除缓存")
	p.clk.waitPending(t, 1)
	p.clk.advance(threadPostSettle)
	got := p.lines()
	if len(got) != 1 {
		t.Fatalf("%d lines: %v", len(got), got)
	}
	mustContain(t, "line", textOf(got[0]), "Agent 修登录 需要你：要不要删除缓存")
}

// ... but what a permission asks (a command, a path) is not for a group: the line only says the Agent needs the user.
func TestThreadPostsNeedsYouWithNoCardNeverQuotesAPermission(t *testing.T) {
	needsLarkLayer(t)
	p := newFleetPost(t)
	p.ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: testWeb, Chats: []string{testChat},
			Features: FeishuFeatures{Main: true, Cards: false}}, nil
	})
	p.askKind(t, "ix_6", proto.IxPermission, "rm -rf /srv/data/cache")
	p.clk.waitPending(t, 1)
	p.clk.advance(threadPostSettle)
	got := p.lines()
	if len(got) != 1 {
		t.Fatalf("%d lines: %v", len(got), got)
	}
	mustContain(t, "line", textOf(got[0]), "Agent 修登录 需要你")
	if strings.Contains(textOf(got[0]), "rm -rf") || strings.Contains(textOf(got[0]), "需要你：") {
		t.Fatalf("a permission's ask went to the group: %q", textOf(got[0]))
	}
}

// The card is where the run was asked, and the topic is compared by chat and thread: a topic run of another thread of the
// same chat does not hide the line of this one.
func TestThreadPostsNeedsYouNotHiddenByAnotherTopicsRun(t *testing.T) {
	needsLarkLayer(t)
	p := newFleetPost(t)
	c := &p.ft.s.chans
	c.mu.Lock()
	p.ft.s.loadChannelStateLocked(context.Background())
	c.st.Runs = append(c.st.Runs, &channel.Run{ID: "run_o", Channel: channel.Feishu, MachineID: "mac", SessionID: "s1",
		Started: time.Now(), ReplyTo: "om_other", Ext: map[string]string{"chat": testChat, "thread": "omt_other"}})
	c.mu.Unlock()
	p.ask(t, "ix_5", "要不要删除缓存")
	p.clk.waitPending(t, 1)
	p.clk.advance(threadPostSettle)
	got := p.lines()
	if len(got) != 1 {
		t.Fatalf("%d lines: %v", len(got), got)
	}
	// The card is in the other topic: this one is told an agent needs the user, nothing of what it asks.
	mustContain(t, "line", textOf(got[0]), "Agent 修登录 需要你")
	if strings.Contains(textOf(got[0]), "要不要删除缓存") || strings.Contains(textOf(got[0]), "私聊") {
		t.Fatalf("line: %q", textOf(got[0]))
	}
}

// An agent with no name is "an agent", not two spaces: a bare directory mark is no name (the line's wording is the lark
// module's, TestThreadPostTextWithoutAnAgentName there).
func TestThreadNameOfAnAgentWithoutOne(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, name := range []string{"", ".", "/", "  "} {
		if got := threadName(name); got != "" {
			t.Fatalf("threadName(%q) = %q", name, got)
		}
	}
}

// A card asked in the topic itself (its run was asked there) is right there already: no line about it. One of an agent
// whose todo has no thread is not said anywhere.
func TestThreadPostsNoLineForACardInTheTopicItself(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	p := newFleetPost(t)
	c := &p.ft.s.chans
	c.mu.Lock()
	p.ft.s.loadChannelStateLocked(context.Background())
	c.st.Runs = append(c.st.Runs, &channel.Run{ID: "run_t", Channel: channel.Feishu, MachineID: "mac", SessionID: "s1",
		Started: time.Now(), Ext: map[string]string{"chat": testChat, "thread": testThread}})
	c.mu.Unlock()
	p.ask(t, "ix_2", "要不要删除缓存")
	p.clk.advance(time.Hour)
	if got := p.lines(); len(got) != 0 {
		t.Fatalf("said %v", got)
	}
}

// A conversation with a private run and a topic's: the card goes to the DM and the topic gets its own line saying so
// (feishu_cards.go); no second line about the same card.
func TestThreadPostsNoLineWhenTheCardWentToTheDM(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	p := newFleetPost(t)
	c := &p.ft.s.chans
	c.mu.Lock()
	p.ft.s.loadChannelStateLocked(context.Background())
	c.st.Runs = append(c.st.Runs,
		&channel.Run{ID: "run_p", Channel: channel.Feishu, MachineID: "mac", SessionID: "s1", Started: time.Now(), ReplyTo: "om_dm"},
		&channel.Run{ID: "run_t", Channel: channel.Feishu, MachineID: "mac", SessionID: "s1", Started: time.Now(), ReplyTo: "om_topic",
			Ext: map[string]string{"chat": testChat, "thread": testThread}})
	c.mu.Unlock()
	p.ask(t, "ix_3", "要不要删除缓存")
	p.clk.advance(time.Hour)
	for _, l := range p.lines() {
		if strings.Contains(textOf(l), "需要你：") {
			t.Fatalf("a second line about the card: %v", l)
		}
	}
}

// Two lines that read the same under one anchor are two messages: every line has its own key (Feishu drops a send whose
// key it has seen, and the reply's usual key is made of the parent and the words). The stub does what Feishu does.
func TestThreadPostsSameWordsTwiceBothArrive(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	seen := map[string]bool{}
	var landed []string
	var keys []string
	id := e.bound("发版", testThread) // its anchor seeded before the lines are counted
	e.f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		reply, _ := m["reply_to"].(string)
		text, _ := m["text"].(string)
		key, _ := m["uuid"].(string)
		keys = append(keys, key)
		if key == "" { // what the hub makes of it: parent and words
			key = "r:" + reply + ":" + text
		}
		if !seen[key] {
			seen[key] = true
			landed = append(landed, text)
		}
		return map[string]any{"message_id": "om_x"}, nil
	})
	for range [3]int{} {
		e.decide(id, "先发安卓")
		e.clk.advance(threadPostEvery)
	}
	if len(landed) != 3 || landed[0] != landed[1] || landed[1] != landed[2] {
		t.Fatalf("landed %q: three identical lines should all arrive", landed)
	}
	for _, k := range keys {
		if k == "" || len(k) > 50 {
			t.Fatalf("key %q", k)
		}
	}
	if keys[0] == keys[1] || keys[1] == keys[2] || keys[0] == keys[2] {
		t.Fatalf("keys repeat: %v", keys)
	}
}

// The anchor message was withdrawn: the hub fails the call (it does not drop the line into the group). The assistant
// forgets that anchor, says nothing until the owner writes again in the topic, and then replies under the new message.
func TestThreadPostsAnchorGone(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	gone := true
	e.f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		if m["reply_to"] == "om_"+testThread && gone {
			return nil, fmt.Errorf("feishu reply: 230011 The message has been withdrawn")
		}
		return map[string]any{"message_id": "om_x"}, nil
	})
	id := e.bound("发版", testThread)
	e.decide(id, "先发安卓")
	e.clk.advance(threadPostEvery)
	if got := e.s.larkModule().TopicAnchor(context.Background(), testChat, testThread); got != "" {
		t.Fatalf("the withdrawn anchor %q is still remembered", got)
	}
	n := len(e.posts()) // the one failed call
	e.decide(id, "再发 iOS")
	e.clk.advance(threadPostEvery)
	if got := e.posts(); len(got) != n {
		t.Fatalf("a call with no anchor to reply under: %v", got[n:])
	}
	e.deliver(topicEvent("om_new", "好的"))
	e.decide(id, "最后发网页")
	e.clk.advance(threadPostEvery)
	got := e.posts()
	if len(got) != n+1 || got[n]["reply_to"] != "om_new" || !strings.Contains(textOf(got[n]), "最后发网页") {
		t.Fatalf("after the owner wrote again: %v", got[n:])
	}
	// A newer anchor that came meanwhile is not forgotten with the old one: the module's
	// TestTopicAnchorForgetKeepsANewerOne.
}

// What the user wrote in the private chat as the answer to a "Please verify" card stays there; every other manual
// decision (a hand-written one) is posted.
func TestThreadPostsNotWhatWasSaidInThePrivateChat(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.bound("验收", testThread)
	store := e.s.todoDB()
	rows := []db.TodoDecisionRow{
		{ID: "dec_priv", TodoID: id, Question: "验收意见", Choice: "按钮颜色不对", Source: db.DecisionManual, SourceMessage: "feishu-om_priv", CreatedAt: decisionNow()},
		{ID: "dec_hand", TodoID: id, Question: "颜色", Choice: "用蓝色", Source: db.DecisionManual, CreatedAt: decisionNow()},
	}
	for _, r := range rows {
		if ok, err := e.s.insertDecision(context.Background(), store, r); err != nil || !ok {
			t.Fatalf("%s: %v %v", r.ID, ok, err)
		}
	}
	e.clk.advance(threadPostEvery)
	text := textOf(e.onlyPost(t))
	mustContain(t, "line", text, "用蓝色")
	if strings.Contains(text, "按钮颜色不对") {
		t.Fatalf("the private chat's words went to the group: %q", text)
	}
	if postable(rows[0]) || !postable(rows[1]) {
		t.Fatal("postable")
	}
}

// 5. The todo accepted, done, closed by the hub's judge, or closed because its agent never started: a line each.
func TestThreadPostsTodoAcceptedOrClosed(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	e := newPostEnv(t)
	said := func(n int) string {
		t.Helper()
		e.clk.advance(threadPostEvery)
		got := e.posts()
		if len(got) != n {
			t.Fatalf("%d lines, want %d: %v", len(got), n, got)
		}
		return textOf(got[n-1])
	}

	// Accepted on the "Please verify" card (the todo stays open).
	id := e.bound("验收", "omt_acc")
	e.s.accept.mu.Lock()
	e.s.loadAcceptLocked(ctx)
	e.s.accept.asks[id] = &acceptAsk{MessageID: "om_card", Messages: []string{"om_card"}, AskedAt: time.Now()}
	e.s.accept.mu.Unlock()
	if !e.s.accepted(ctx, id, "") {
		t.Fatal("not accepted")
	}
	mustContain(t, "accepted line", said(1), "已验收", testWeb+"/todos/"+id)

	// Done by the user.
	id = e.bound("手动", "omt_done")
	e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
	mustContain(t, "done line", said(2), "待办已完成")

	// Closed by the hub's judge, with why.
	id = e.bound("自动", "omt_auto")
	if ok, err := e.s.autoDoneTodo(ctx, id, "已合并 PR", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	mustContain(t, "auto line", said(3), "待办已完成：已合并 PR")

	// Its agent never started.
	id = e.bound("没起来", "omt_fail")
	if err := e.s.failTodo(ctx, id, "机器不在线"); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "failed line", said(4), "没有启动：机器不在线")

	// Done again (already done) says nothing more.
	e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 4 {
		t.Fatalf("said again: %v", got[4:])
	}
}

// Accepted and done together are one line, the done one.
func TestThreadPostsAcceptedThenDoneIsOneLine(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	e := newPostEnv(t)
	id := e.bound("验收", testThread)
	e.s.accept.mu.Lock()
	e.s.loadAcceptLocked(ctx)
	e.s.accept.asks[id] = &acceptAsk{MessageID: "om_card", Messages: []string{"om_card"}, AskedAt: time.Now()}
	e.s.accept.mu.Unlock()
	e.s.accepted(ctx, id, "")
	e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
	e.clk.advance(threadPostSettle)
	text := textOf(e.onlyPost(t))
	mustContain(t, "line", text, "待办已完成")
	if strings.Contains(text, "已验收") {
		t.Fatalf("both said: %q", text)
	}
}

// 6. A topic whose chat the bot may not send into gets nothing: what an agent bound (todo_bind_thread takes any chat_id)
// is not a place to write to.
func TestThreadPostsNothingToAChatNotInTheList(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.create("发版").ID
	seedTopicAnchor(t, e.s, e.f, "oc_made_up", "omt_x", "om_x")
	e.must("PUT", "/api/v1/todos/"+id+"/thread", `{"channel":"feishu","chat_id":"oc_made_up","thread_id":"omt_x"}`)
	e.decide(id, "先发安卓")
	e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said into a chat the bot may not use: %v", got)
	}

	// The same todo's topic, once its chat is on the list, is said into (the list is read when the line goes out).
	id2 := e.bound("在名单里", "omt_listed")
	e.chats = nil
	e.s.larkModule().ForgetStatus()
	e.decide(id2, "先发安卓")
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said with no chat on the list: %v", got)
	}
	e.chats = []string{testChat}
	e.s.larkModule().ForgetStatus()
	e.decide(id2, "再发 iOS")
	e.clk.advance(time.Hour)
	mustContain(t, "line", textOf(e.onlyPost(t)), "再发 iOS")
}

// 7. A todo with no thread says nothing, and queues nothing.
func TestThreadPostsNothingWithoutAThread(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newPostEnv(t)
	id := e.create("发版").ID
	e.decide(id, "先发安卓")
	e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
	if n := e.clk.pending(); n != 0 {
		t.Fatalf("%d timers for a todo with no thread", n)
	}
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said %v", got)
	}
	// Unbound while a line waits: it is not said.
	id = e.bound("解绑", "omt_unbound")
	e.decide(id, "先发安卓")
	e.must("DELETE", "/api/v1/todos/"+id+"/thread", "")
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said after the thread was let go: %v", got)
	}
}

// 8. The line goes into the topic: the chat, as a reply in its thread, to the latest message the owner said there.
func TestThreadPostsGoIntoTheTopicByItsLatestMessage(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	id := e.create("发版").ID
	e.must("PUT", "/api/v1/todos/"+id+"/thread", `{"channel":"feishu","chat_id":"`+testChat+`","thread_id":"`+testThread+`"}`)

	// No message of the topic is known yet: nothing to reply to, nothing said.
	e.decide(id, "先发安卓")
	e.clk.advance(time.Hour)
	if got := e.posts(); len(got) != 0 {
		t.Fatalf("said with no message to reply to: %v", got)
	}

	e.deliver(topicEvent("om_t1", "第一句"))
	e.deliver(topicEvent("om_t2", "第二句"))
	e.decide(id, "再发 iOS")
	e.clk.advance(time.Hour)
	p := e.onlyPost(t)
	if p["chat_id"] != testChat || p["in_thread"] != true || p["reply_to"] != "om_t2" || p["no_detour"] != true {
		t.Fatalf("where it went: %v", p)
	}

	// It survives a restart: the message is kept with the chat's state.
	e2 := e.s.larkModule().TopicAnchor(context.Background(), testChat, testThread)
	if e2 != "om_t2" {
		t.Fatalf("anchor %q", e2)
	}
	var stored botState
	if _, err := e.s.store().Setting(context.Background(), botStateKey, &stored); err != nil {
		t.Fatal(err)
	}
	if kept := stored.Topics[testChat+"/"+testThread]; kept == nil || kept.MessageID != "om_t2" {
		t.Fatalf("kept %+v", kept)
	}
}

// A topic's first message opens a to-do (the lark module's thread_open.go); that message is where the to-do's first line replies
// to: it is remembered before the opening path takes the message, so the first progress hangs under the topic's root.
func TestThreadPostsFirstProgressRepliesUnderTheRootMessage(t *testing.T) {
	needsLarkLayer(t)
	e := newPostEnv(t)
	e.deliver(rootEvent("om_root", "发天气 App 1.3 版"))
	todo := onlyTodo(e.s)
	if todo.ID == "" || todo.Thread == nil {
		t.Fatalf("no to-do opened for the topic: %+v", todo)
	}
	before := len(e.posts()) // the note saying it was opened
	e.decide(todo.ID, "先发安卓")
	e.clk.advance(threadPostSettle)
	got := e.posts()
	if len(got) != before+1 {
		t.Fatalf("%d lines said, want %d: %v", len(got), before+1, got)
	}
	p := got[before]
	if p["reply_to"] != "om_root" || p["chat_id"] != testChat || p["in_thread"] != true {
		t.Fatalf("the first line went as %v, want a reply to the root message in the topic", p)
	}
	mustContain(t, "line", textOf(p), "先发安卓", testWeb+"/todos/"+todo.ID)
}

// The lark module's topic-line pacing these tests run against (its thread_posts.go): a line a minute at most, quoting
// three decisions.
const (
	threadPostEvery = time.Minute
	threadPostShown = 3
)
