package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const deployedReply = "已合入 https://github.com/o/r/pull/9 并已部署到 n37。打开 https://hub.example.com/settings?tab=feishu 能看到新开关"

// These acceptance fixtures start with a recorded task PR. The deploy checker tests exercise the node proof separately.
func turnWithRecordedDeployment(t *testing.T, s *Service, f *fakeHub) {
	t.Helper()
	if todo, ok := s.openTodoOfAgent("box", "s1"); ok && todo.PRURL == "" {
		s.setTaskStage(t.Context(), todo.ID, "", "https://github.com/o/r/pull/9")
	}
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
}

// acceptService is reportService with the small model answering the "Please verify" lines, the cards' updates and notes kept.
func acceptService(t *testing.T, stores ...Store) (*Service, *fakeHub, func() []string) {
	t.Helper()
	s, f, posts := reportService(t, true, stores...)
	s.ownerVerifiesAll = true // these tests cover the owner's "Please verify" (a big feature's; ownerVerifies)
	f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置 › 飞书机器人，应看到「任务进展发到飞书」"}`
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	return s, f, posts
}

// askOf is task id's "Please verify" ask as it is now (nil: none), read under the lock the hub's goroutines write it under.
func askOf(s *Service, id string) *acceptAsk {
	s.accept.mu.Lock()
	defer s.accept.mu.Unlock()
	a, ok := s.accept.asks[id]
	if !ok {
		return nil
	}
	cp := *a
	return &cp
}

func acceptCardOf(posts []string) string {
	for _, p := range posts {
		if strings.Contains(p, "请验收") {
			return p
		}
	}
	return ""
}

func countAcceptCards(posts []string) (n int) {
	for _, p := range posts {
		if strings.Contains(p, "请验收") {
			n++
		}
	}
	return n
}

// waitAcceptCard waits for the final ask (MessageID stored only after botPost), so ask reads do not race it under -race.
func waitAcceptCard(t *testing.T, s *Service, id string, posts func() []string) {
	t.Helper()
	waitFor(t, "the 请验收 card", func() bool { a := askOf(s, id); return a != nil && a.MessageID != "" && acceptCardOf(posts()) != "" })
}

// A turn that says the work is deployed gets a "Please verify" card: what changed, a link straight to the page (one from the
// reply, never made up), how to check, and "Pass". "Pass" marks the task verified.
func TestDeployedTaskAsksForAcceptance(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := acceptService(t)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	card := acceptCardOf(posts())
	for _, want := range []string{"改了什么", "飞书机器人多了任务进展开关", "去哪看", "https://hub.example.com/settings?tab=feishu", "怎么验", "通过", `"a":"accept"`} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	mid := askOf(s, todo.ID).MessageID
	if !s.acceptReply(context.Background(), "om_reply", "通过", false, mid, "") {
		t.Fatal("a reply to the card was not taken as one")
	}
	if got := onlyTodo(s); got.Stage != "verify" {
		t.Fatalf("stage %q", got.Stage)
	}
	// Another turn that says deployed again does not ask twice.
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	time.Sleep(6 * testReportDelay)
	n := 0
	for _, p := range posts() {
		if strings.Contains(p, "请验收") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d 请验收 cards", n)
	}
}

// Anything else said to the card goes to the task's agent (and the todo's decisions with a store); the next deploy
// asks again. A model that makes up a link is not believed.
func TestAcceptanceFeedbackGoesToTheAgent(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := acceptService(t)
	f.judge = `{"changed":"改了","link":"https://evil.example.com/x","how":"看看"}`
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	if card := acceptCardOf(posts()); strings.Contains(card, "evil.example.com") || !strings.Contains(card, "/todos/"+todo.ID) {
		t.Fatalf("link:\n%s", card)
	}
	mid := askOf(s, todo.ID).MessageID
	before := len(f.agentSent)
	if !s.acceptReply(context.Background(), "om_r2", "开关名字改成「任务进展」", false, "", mid) {
		t.Fatal("not taken")
	}
	f.mu.Lock()
	sent := f.agentSent[before:]
	f.mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0]["text"].(string), "开关名字改成「任务进展」") {
		t.Fatalf("to the agent: %v", sent)
	}
	if askOf(s, todo.ID) != nil {
		t.Fatal("a change asked for leaves the ask in place: the next deploy would not ask again")
	}
	if s.acceptReply(context.Background(), "om_r3", "随便聊聊", false, "om_other", "") {
		t.Fatal("a message about something else was taken")
	}
}

// One not accepted by the next morning is asked again, once.
func TestAcceptanceReminderNextMorning(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := acceptService(t)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	loc := s.location(context.Background())
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].AskedAt = time.Now().Add(-30 * time.Hour)
	s.accept.mu.Unlock()
	now := time.Now().In(loc)
	morning := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc)
	waitReportQueue(t, s) // report cards go out on their own timer: count only after them (#14138)
	n := len(posts())
	s.remindAcceptance(morning)
	s.remindAcceptance(morning.Add(time.Hour))
	if got := posts()[n:]; len(got) != 1 || !strings.Contains(got[0], "还没验收") {
		t.Fatalf("reminders %q", got)
	}
}

// A card not passed yet is in the snapshot the morning briefing reads, with what changed and where to look; the
// reminder waits for the briefing's own time (08:30 here), not a fixed 9:00.
func TestUnacceptedInTheMorningBriefing(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := acceptService(t)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	ctx := context.Background()
	loc := s.location(ctx)
	got, total := s.snapshotUnaccepted(ctx, loc)
	if len(got) != 1 || total != 1 || got[0].Changed != "飞书机器人多了任务进展开关" || got[0].Link != "https://hub.example.com/settings?tab=feishu" {
		t.Fatalf("unaccepted %+v", got)
	}
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range sc.tasks {
		if sc.tasks[i].ID == "morning" {
			sc.tasks[i].Time, sc.tasks[i].Enabled = "08:30", true
			sc.tasks[i].Facts = false // a briefing with facts waits an hour more for itself (briefing_card_test.go)
		}
	}
	sc.mu.Unlock()
	if hh, mm := s.briefingClock(ctx); hh != 8 || mm != 30 {
		t.Fatalf("briefing at %d:%02d", hh, mm)
	}
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].AskedAt = time.Now().Add(-30 * time.Hour)
	s.accept.mu.Unlock()
	now := time.Now().In(loc)
	waitReportQueue(t, s) // report cards go out on their own timer: count only after them (#14138)
	n := len(posts())
	s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 8, 20, 0, 0, loc))
	if len(posts()) != n {
		t.Fatalf("reminded before the briefing")
	}
	s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 8, 40, 0, 0, loc))
	if got := posts()[n:]; len(got) != 1 || !strings.Contains(got[0], "还没验收") {
		t.Fatalf("reminders %q", got)
	}
	s.accepted(ctx, todo.ID, "")
	if got, _ := s.snapshotUnaccepted(ctx, loc); len(got) != 0 {
		t.Fatalf("still listed after 通过: %+v", got)
	}
}

// A change asked for on the card of a task the hub closed meanwhile reopens it: while its agent works on the change
// the task is open, and the agent's next turn is followed again — the next deploy gets a new "Please verify" card (#3457).
func TestAcceptanceFeedbackReopensTheClosedTask(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := acceptService(t)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	ctx := context.Background()
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	mid := askOf(s, todo.ID).MessageID
	// The agent may take the change up at once: by the time it is sent, the task must follow the agent again.
	followedAtSend := false
	f.mu.Lock()
	f.onSend = func(*fakeHub, map[string]any) { _, followedAtSend = s.taskOf(ctx, "box", "s1") }
	f.mu.Unlock()
	if !s.acceptReply(ctx, "om_r2", "按钮改成蓝色", false, mid, "") {
		t.Fatal("not taken")
	}
	f.mu.Lock()
	f.onSend = nil
	f.mu.Unlock()
	if !followedAtSend {
		t.Fatal("the change was sent while the task was still closed: the agent's turn would not be followed")
	}
	got := onlyTodo(s)
	if got.DoneAt != nil || got.ReopenedAt == nil || got.DoneBy != "" {
		t.Fatalf("still closed after a change was asked for: %+v", got)
	}
	if _, ok := s.taskOf(ctx, "box", "s1"); !ok {
		t.Fatal("the agent no longer counts for the task")
	}
	n := 0
	for _, p := range posts() {
		if strings.Contains(p, "请验收") {
			n++
		}
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "a new 请验收 card", func() bool {
		m := 0
		for _, p := range posts() {
			if strings.Contains(p, "请验收") {
				m++
			}
		}
		return m == n+1
	})
}

// Approval with a completion particle (the words below, "verify: passed" too) still marks the task
// verified. Questions (a question particle or mark) and negations ("not passed", "no good") stay feedback for the agent.
func TestPassWordsParticles(t *testing.T) {
	pass := []string{
		"通过", "通过。", "通过了。", "通过啦！", "验收通过了",
		"可以了", "没问题了", "ok了", "OK啦",
		"通过呀", "通过咯", "通过喽", "可以呗", "好哒", "可以啊", "没问题啊",
		"通过了呀",    // two completion particles, still an approval
		"好", "好的", // bare "ok" already matched before this change; the behavior is kept
	}
	for _, in := range pass {
		if !passWords.MatchString(in) {
			t.Errorf("%q must count as approval", in)
		}
	}
	notPass := []string{
		"可以了吗", "通过了？", "通过了?", "这能通过？",
		"不通过", "没通过", "不行", "还不行", "通过不了", "不可以了",
		"开关名字改成「任务进展」",
	}
	for _, in := range notPass {
		if passWords.MatchString(in) {
			t.Errorf("%q must not count as approval", in)
		}
	}
}

// A "Please verify" card whose task ends without the hub's auto-judge — the owner closes it in the todos, or a supervision
// review does — can never be passed: it is neither reminded nor counted in the morning briefing, and is dropped.
func TestAcceptanceAskEndedElsewhere(t *testing.T) {
	needsLarkLayer(t)
	cases := []struct {
		name string
		end  func(ctx context.Context, s *Service, id string)
		kept bool
	}{
		{"closed manually", func(_ context.Context, s *Service, id string) {
			done := time.Now()
			s.todos.mu.Lock()
			s.todos.todos[id].DoneAt, s.todos.todos[id].DoneBy, s.todos.todos[id].DoneReason = &done, "", ""
			s.todos.mu.Unlock()
		}, false},
		{"closed by a review", func(ctx context.Context, s *Service, id string) {
			if ok, err := s.reviewDoneTodo(ctx, id, "验收通过", true); err != nil || !ok {
				t.Fatalf("review done: %v %v", ok, err)
			}
		}, false},
		{"closed by the auto judge", func(ctx context.Context, s *Service, id string) {
			if ok, err := s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
				t.Fatalf("auto done: %v %v", ok, err)
			}
		}, true},
		{"still open", func(context.Context, *Service, string) {}, true},
	}
	// setup deploys a task until its "Please verify" card is out; the case then ends it.
	setup := func(t *testing.T) (*Service, *fakeHub, func() []string, string) {
		s, f, posts := acceptService(t)
		todo := startedTask(t, s, f)
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		// Wait for the final ask (MessageID is set only after botPost returns): backdating AskedAt must not race the
		// post-botPost replacement of the claimed placeholder.
		waitAcceptCard(t, s, todo.ID, posts)
		return s, f, posts, todo.ID
	}
	for _, c := range cases {
		t.Run(c.name+" remind", func(t *testing.T) {
			ctx := context.Background()
			s, _, posts, id := setup(t)
			c.end(ctx, s, id)
			s.accept.mu.Lock()
			s.accept.asks[id].AskedAt = time.Now().Add(-30 * time.Hour)
			s.accept.mu.Unlock()
			now := time.Now().In(s.location(ctx))
			waitReportQueue(t, s) // report cards go out on their own timer: count only after them (#14138)
			n := len(posts())
			s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, s.location(ctx)))
			got := posts()[n:]
			if c.kept {
				if len(got) != 1 || !strings.Contains(got[0], "还没验收") || askOf(s, id) == nil {
					t.Fatalf("reminders %q, ask kept=%v", got, askOf(s, id) != nil)
				}
			} else if td := onlyTodo(s); td.DoneAt == nil || td.DoneBy == db.DoneByAuto ||
				len(got) != 0 || askOf(s, id) != nil || s.accepted(ctx, id, "") {
				t.Fatalf("ended task was not cleaned: task=%+v reminded=%q ask=%v", td, got, askOf(s, id) != nil)
			}
		})
		t.Run(c.name+" snapshot", func(t *testing.T) {
			ctx := context.Background()
			s, _, _, id := setup(t)
			c.end(ctx, s, id)
			_, total := s.snapshotUnaccepted(ctx, s.location(ctx))
			if (total == 1) != c.kept || (askOf(s, id) != nil) != c.kept {
				t.Fatalf("total=%d kept=%v ask=%v", total, c.kept, askOf(s, id) != nil)
			}
		})
	}
}

// askedToAccept is acceptService with a task deployed until its "Please verify" card is out; notes are the bot's text
// replies so far.
func askedToAccept(t *testing.T) (s *Service, f *fakeHub, posts, notes func() []string, id string) {
	t.Helper()
	s, f, posts = acceptService(t)
	var mu sync.Mutex
	var said []string
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]string
		json.Unmarshal(p, &m)
		mu.Lock()
		said = append(said, m["text"])
		mu.Unlock()
		return map[string]any{}, nil
	})
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	return s, f, posts, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), said...) }, todo.ID
}

// A reply to the card that carries a picture or a file is told that only words go on (#5107): a picture alone is answered
// with just that and the card still waits; a picture with words sends the words to the agent as words alone do.
func TestAcceptanceReplyWithMedia(t *testing.T) {
	needsLarkLayer(t)
	for _, c := range []struct {
		name, msgType, content string
		routed, dropped        bool
	}{
		{"a picture alone", "image", `{"image_key":"img_1"}`, false, true},
		{"a picture with words", "post", `{"title":"","content":[[{"tag":"text","text":"按钮改成蓝色"}],[{"tag":"img","image_key":"img_1"}]]}`, true, true},
		{"words alone", "text", `{"text":"按钮改成蓝色"}`, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f, _, notes, id := askedToAccept(t)
			card := askOf(s, id).MessageID
			ev := dmReply("om_reply", "", card, card)
			ev.Message.MessageType, ev.Message.Content = c.msgType, c.content
			f.mu.Lock()
			before := len(f.agentSent)
			f.mu.Unlock()
			feishuSay(t, s, f, nil, ev, "")
			f.mu.Lock()
			sent := f.agentSent[before:]
			f.mu.Unlock()
			got := notes()
			routed := len(sent) == 1 && strings.Contains(sent[0]["text"].(string), "按钮改成蓝色")
			if routed != c.routed || (askOf(s, id) == nil) != c.routed || len(got) != 1 ||
				strings.Contains(got[0], "图片和文件没交给 Agent") != c.dropped || c.routed != strings.Contains(got[0], "交给原来的 Agent 改了") {
				t.Fatalf("to the agent %v, ask kept %v, notes %q", sent, askOf(s, id) != nil, got)
			}
		})
	}
}

// A reply "Pass" to a "Please verify" card whose round was already handled is answered, as its button is: the reply is
// taken as an answer (it is not sent on to the Agent either), so one that gets nothing back reads as the bot ignoring
// the owner while the same press on the card says "already handled" (the lark module's card_actions.go).
func TestAcceptancePassReplyWhenAlreadyHandled(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	s, f, _, notes, id := askedToAccept(t)
	mid := askOf(s, id).MessageID
	if ok, err := s.acceptTry(ctx, id, mid); !ok.taken || err != nil { // the card's button
		t.Fatalf("the card was not accepted: %v", err)
	}
	before, sent := len(notes()), len(f.agentSent)
	if !s.acceptReply(ctx, "om_pass_again", "通过", false, mid) {
		t.Fatal("the reply to the card was not taken as an answer")
	}
	got := notes()
	if len(f.agentSent) != sent {
		t.Fatalf("the reply went to the Agent: %v", f.agentSent[sent:])
	}
	if len(got)-before != 1 || !strings.Contains(got[before], "已经处理过了") {
		t.Fatalf("notes %q, want one more saying it was already handled", got)
	}
}

func TestAcceptanceReopenRound(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	setup := askedToAccept
	closeReopen := func(s *Service, id string) {
		s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute))
		s.reopenTodo(ctx, id)
	}
	t.Run("the next deploy asks with a fresh card", func(t *testing.T) {
		s, f, posts, _, id := setup(t)
		first := askOf(s, id)
		closeReopen(s, id)
		reopened := *onlyTodo(s).ReopenedAt
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "a new 请验收 card", func() bool { // the post fake reuses one message id: rounds tell apart by AskedAt
			a := askOf(s, id)
			return a != nil && a.MessageID != "" && a.AskedAt.After(reopened)
		})
		second := askOf(s, id)
		if !second.AskedAt.After(first.AskedAt) || len(second.Messages) != 1 {
			t.Fatalf("second-round ask %+v, first %+v", second, first)
		}
		if n := countAcceptCards(posts()); n != 2 {
			t.Fatalf("%d 请验收 cards, want 2", n)
		}
		if !s.acceptReply(ctx, second.MessageID, "通过", false, second.MessageID) || onlyTodo(s).Stage != "verify" {
			t.Fatal("the new card did not accept the new round")
		}
	})

	t.Run("the old card stays dead once the next round has its card", func(t *testing.T) { // #4853
		s, f, _, notes, id := setup(t)
		old := askOf(s, id).MessageID
		f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return map[string]any{"message_id": "om_round2"}, nil })
		closeReopen(s, id)
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the second round's card", func() bool { a := askOf(s, id); return a != nil && a.MessageID == "om_round2" })
		press := func(mid string) *botcard.CardAction {
			var a botcard.CardAction
			a.Action.Value = map[string]any{"a": "accept", "id": id}
			a.Context.MessageID = mid
			return &a
		}
		s.larkModule().Action(ctx, press(old), "")
		if a := askOf(s, id); !a.AcceptedAt.IsZero() || onlyTodo(s).Stage == "verify" {
			t.Fatalf("the first round's button verified the second round: %+v stage=%q", a, onlyTodo(s).Stage)
		}
		before := len(f.agentSent)
		for _, text := range []string{"通过", "再改一下这个开关"} {
			if !s.acceptReply(ctx, "om_reply", text, false, old) {
				t.Fatalf("a reply %q to the first round's card went on to the chat", text)
			}
		}
		if a, got := askOf(s, id), notes(); len(f.agentSent) != before || !a.AcceptedAt.IsZero() || onlyTodo(s).Stage == "verify" ||
			len(got) != 2 || !strings.Contains(got[0], "已经处理过了") || !strings.Contains(got[1], "已经处理过了") {
			t.Fatalf("routed=%d notes=%q ask=%+v stage=%q", len(f.agentSent)-before, got, a, onlyTodo(s).Stage)
		}
		s.larkModule().Action(ctx, press("om_round2"), "")
		if askOf(s, id).AcceptedAt.IsZero() || onlyTodo(s).Stage != "verify" {
			t.Fatal("the second round's own button did not accept it")
		}
	})

	t.Run("the old card is dead after reopen", func(t *testing.T) {
		for _, c := range []struct{ name, text string }{
			{"pass reply", "通过"}, {"change reply", "再改一下这个开关"},
		} {
			t.Run(c.name, func(t *testing.T) {
				s, f, _, notes, id := setup(t)
				mid := askOf(s, id).MessageID
				closeReopen(s, id)
				before := len(f.agentSent)
				if !s.acceptReply(ctx, "om_old", c.text, false, mid) {
					t.Fatal("the old-card reply was not consumed")
				}
				if a, got := askOf(s, id), notes(); len(f.agentSent)-before != 0 || a == nil || !a.AcceptedAt.IsZero() ||
					onlyTodo(s).Stage == "verify" || len(got) != 1 || !strings.Contains(got[0], "已经处理过了") {
					t.Fatalf("routed=%d notes=%q accepted=%v stage=%q", len(f.agentSent)-before, got, a != nil, onlyTodo(s).Stage)
				}
			})
		}
		t.Run("button", func(t *testing.T) {
			s, _, _, _, id := setup(t)
			closeReopen(s, id)
			if s.accepted(ctx, id, "") || !askOf(s, id).AcceptedAt.IsZero() {
				t.Fatal("the old card's button verified the reopened task")
			}
			s.todos.mu.Lock()
			delete(s.todos.todos, id)
			s.todos.mu.Unlock()
			if !s.accepted(ctx, id, "") || askOf(s, id).AcceptedAt.IsZero() {
				t.Fatal("a deleted todo's button changed its old accept behavior")
			}
		})
		t.Run("reminder", func(t *testing.T) {
			s, _, posts, _, id := setup(t)
			closeReopen(s, id)
			s.accept.mu.Lock()
			s.accept.asks[id].AskedAt = time.Now().Add(-30 * time.Hour)
			s.accept.mu.Unlock()
			now := time.Now().In(s.location(ctx))
			waitReportQueue(t, s) // report cards go out on their own timer: count only after them (#14138)
			n := len(posts())
			s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, s.location(ctx)))
			if got := posts()[n:]; len(got) != 0 || askOf(s, id) != nil {
				t.Fatalf("the stale card was reminded %q, ask kept %v", got, askOf(s, id) != nil)
			}
		})
		t.Run("snapshot", func(t *testing.T) {
			s, _, _, _, id := setup(t)
			closeReopen(s, id)
			if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 0 || askOf(s, id) != nil {
				t.Fatalf("total=%d ask kept=%v", total, askOf(s, id) != nil)
			}
		})
		t.Run("reply racing a sweep", func(t *testing.T) {
			s, _, _, _, id := setup(t)
			mid := askOf(s, id).MessageID
			closeReopen(s, id)
			var stop atomic.Bool
			var wg sync.WaitGroup
			loop := func(fn func()) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for !stop.Load() {
						fn()
					}
				}()
			}
			for i := 0; i < 4; i++ {
				loop(func() { s.acceptReply(ctx, "om_race", "通过", false, mid) })
			}
			loop(func() {
				s.accept.mu.Lock()
				if s.accept.asks[id] == nil {
					s.accept.asks[id] = &acceptAsk{MessageID: mid, Messages: []string{mid}, AskedAt: time.Now().Add(-time.Hour)}
				}
				s.accept.mu.Unlock()
				s.snapshotUnaccepted(ctx, s.location(ctx))
			})
			time.Sleep(150 * time.Millisecond)
			stop.Store(true)
			wg.Wait()
		})
	})

	t.Run("a round never reopened is unchanged", func(t *testing.T) {
		s, _, _, _, id := setup(t)
		s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute))
		if !s.acceptReply(ctx, "om_fresh", "通过", false, askOf(s, id).MessageID) || onlyTodo(s).Stage != "verify" {
			t.Fatal("the ordinary card was not accepted")
		}
	})
}

// A sweep reads each todo with the book unlocked; a new round's card that lands in that window (the task reopened and
// deployed again) is not the card the sweep judged, so it stays (#4649).
func TestAcceptanceSweepKeepsANewRound(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	// setup deploys a task until its "Please verify" card is out and returns a hook that, the first time it runs, asks the next
	// round's card the way the next deploy does (after reopen, when given).
	setup := func(t *testing.T, reopen bool) (*Service, func() []string, string, func(string)) {
		s, f, posts := acceptService(t)
		todo := startedTask(t, s, f)
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitAcceptCard(t, s, todo.ID, posts)
		first := askOf(s, todo.ID)
		fired := false
		hook := func(id string) {
			if fired || id != todo.ID {
				return
			}
			fired = true
			if reopen {
				if err := s.reopenTodo(ctx, id); err != nil {
					t.Errorf("reopen: %v", err)
				}
			}
			s.askAcceptance(onlyTodo(s), first.MachineID, first.SessionID, deployedReply)
		}
		return s, posts, todo.ID, hook
	}
	newRound := func(t *testing.T, s *Service, id string, since time.Time) {
		t.Helper()
		if a := askOf(s, id); a == nil || a.MessageID == "" || !a.AskedAt.After(since) {
			t.Fatalf("the new round's ask was dropped: %+v", a)
		}
	}

	t.Run("reminder sweep, task closed manually then reopened", func(t *testing.T) {
		s, posts, id, hook := setup(t, true)
		done := time.Now()
		s.todos.mu.Lock()
		s.todos.todos[id].DoneAt, s.todos.todos[id].DoneBy, s.todos.todos[id].DoneReason = &done, "", ""
		s.todos.mu.Unlock()
		s.accept.mu.Lock()
		s.accept.asks[id].AskedAt = time.Now().Add(-30 * time.Hour)
		s.accept.mu.Unlock()
		s.acceptSweepHook = hook
		start := time.Now()
		now := time.Now().In(s.location(ctx))
		s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, s.location(ctx)))
		newRound(t, s, id, start)
		for _, p := range posts() {
			if strings.Contains(p, "还没验收") {
				t.Fatalf("the new round was reminded on the day it was asked: %q", p)
			}
		}
	})

	t.Run("snapshot sweep, card from before the reopen", func(t *testing.T) {
		s, _, id, hook := setup(t, false)
		s.autoDoneTodo(ctx, id, "做完了", time.Now().Add(-time.Minute))
		s.reopenTodo(ctx, id)
		s.acceptSweepHook = hook
		start := time.Now()
		s.snapshotUnaccepted(ctx, s.location(ctx))
		newRound(t, s, id, start)
		if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 1 {
			t.Fatalf("the new round is not listed: total=%d", total)
		}
	})
}
