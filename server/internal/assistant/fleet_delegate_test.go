package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// InteractionRecord is fleetMem's interaction with its outcome (the "Change answer" route reads it).
func (m *fleetMem) InteractionRecord(_ context.Context, id string) (*db.InteractionRecord, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	for _, r := range m.recs {
		if r.Interaction.ID == id {
			r.Status, r.Resolution = m.status[id], m.res[id]
			return &r, nil
		}
	}
	return nil, nil
}

// delegateFleet is a manager with agents.delegate on and one agent session on "mac", whose assistant.delegate decides
// with decide; calls counts how often it was asked.
type delegateFleet struct {
	*fleetTest
	mu    sync.Mutex
	calls int
}

func newDelegateFleet(t *testing.T, decide func(DelegateParams) (DelegateResult, error)) *delegateFleet {
	t.Helper()
	ft := newFleetTest(t)
	setSettings(t, ft.s, func(v *Settings) { v.Main = "" }) // exercise the manual node-question fallback
	on := true
	ft.f.setHello(func(h *Hello) { h.Agents.Delegate.Enabled = &on }) // off by default
	list := []proto.Session{{SessionID: "s1", Title: "修登录", Cwd: "/code/app", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()}}
	ft.d.setSessions("mac", list)
	ft.f.setSessions("mac", list...) // where messages to agents go (session.send through the hub)
	df := &delegateFleet{fleetTest: ft}
	ft.s.delegateHook = func(_ context.Context, p DelegateParams) (DelegateResult, error) {
		df.mu.Lock()
		df.calls++
		df.mu.Unlock()
		return decide(p)
	}
	return df
}

func (df *delegateFleet) delegateCalls() int {
	df.mu.Lock()
	defer df.mu.Unlock()
	return df.calls
}

func delegateQuestion(id string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}, {Label: "feat/x"}}}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
}

// ask raises ix as a hook would; the channel has its answer once it has one.
func (df *delegateFleet) ask(ix proto.Interaction) <-chan proto.Resolution {
	out := make(chan proto.Resolution, 1)
	go func() {
		res, _ := df.s.waitInteraction(context.Background(), ix)
		out <- res
	}()
	return out
}

func answerOf(t *testing.T, ch <-chan proto.Resolution) proto.Resolution {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("the hook got no answer")
	}
	return proto.Resolution{}
}

// pushedFor is the push for interaction id; nil when there was none.
func (df *delegateFleet) pushedFor(id string) *proto.Interaction {
	for _, n := range df.f.pushed() {
		if n.Interaction != nil && n.Interaction.ID == id {
			return n.Interaction
		}
	}
	return nil
}

// shownAs is each time the hub was told of interaction id (fleet.interaction), in order.
func (df *delegateFleet) shownAs(id string) []proto.Interaction {
	var out []proto.Interaction
	for _, raw := range df.toldOf(assistantwire.EventFleetInteraction) {
		var p assistantwire.FleetInteraction
		if json.Unmarshal(raw, &p) == nil && p.Interaction.ID == id {
			out = append(out, p.Interaction)
		}
	}
	return out
}

func (df *delegateFleet) resolvedAs(id string) *assistantwire.FleetResolved {
	for _, raw := range df.toldOf(assistantwire.EventFleetResolved) {
		var p assistantwire.FleetResolved
		if json.Unmarshal(raw, &p) == nil && p.ID == id {
			return &p
		}
	}
	return nil
}

func waitDelegate(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A question the assistant may answer is answered for the user, with why, and nothing is pushed: the apps see the card
// as delegating meanwhile, then answered by the assistant with its reason and answers.
func TestDelegateAnswersForTheUser(t *testing.T) {
	df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
		return DelegateResult{Answer: true, Answers: map[string]string{"用哪个分支？": "feat/x"}, Reason: "任务就在 feat/x 上"}, nil
	})
	ix := delegateQuestion("ix_d1")
	res := answerOf(t, df.ask(ix))
	if res.Answers["用哪个分支？"] != "feat/x" || res.AnsweredBy != proto.AnsweredByAssistant || res.Reason != "任务就在 feat/x 上" {
		t.Fatalf("resolution %+v", res)
	}
	waitDelegate(t, "the resolved event", func() bool { return df.resolvedAs(ix.ID) != nil })
	if ev := df.resolvedAs(ix.ID); ev.AnsweredBy != "assistant" || ev.Reason != "任务就在 feat/x 上" || ev.Answers["用哪个分支？"] != "feat/x" {
		t.Fatalf("resolved event %+v", ev)
	}
	if shown := df.shownAs(ix.ID); len(shown) != 1 || !shown[0].Delegating {
		t.Fatalf("shown %+v", shown)
	}
	if df.pushedFor(ix.ID) != nil {
		t.Fatal("pushed a question the assistant answered")
	}
	if st, kept, _ := df.st.InteractionOutcome(context.Background(), ix.ID); st != "resolved" || kept == nil || kept.Reason == "" {
		t.Fatalf("history %q %+v", st, kept)
	}
	if got := df.delegateCalls(); got != 1 {
		t.Fatalf("delegated %d times", got)
	}
}

// A question the assistant leaves to the user is pushed then, with the context it filled in.
func TestDelegateLeavesItToTheUser(t *testing.T) {
	df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
		return DelegateResult{Ask: &proto.Delegate{What: "修登录页崩溃", Recommend: "main", Reason: "要合并到主分支"}}, nil
	})
	ix := delegateQuestion("ix_d2")
	answered := df.ask(ix)
	waitDelegate(t, "the push", func() bool { return df.pushedFor(ix.ID) != nil })
	if p := df.pushedFor(ix.ID); p.Delegating || p.Delegate == nil || p.Delegate.Recommend != "main" {
		t.Fatalf("pushed %+v", p)
	}
	shown := df.shownAs(ix.ID)
	if len(shown) != 2 || !shown[0].Delegating || shown[1].Delegating || shown[1].Delegate == nil || shown[1].Delegate.What != "修登录页崩溃" {
		t.Fatalf("shown %+v", shown)
	}
	if list := df.s.pendingInteractions(); len(list) != 1 || list[0].Delegating || list[0].Delegate == nil {
		t.Fatalf("pending %+v", list)
	}
	if err := df.s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{"用哪个分支？": "main"}}); err != nil {
		t.Fatal(err)
	}
	if res := answerOf(t, answered); res.AnsweredBy != "" || res.Answers["用哪个分支？"] != "main" {
		t.Fatalf("resolution %+v", res)
	}
}

// The assistant not deciding in time, or failing, leaves the question to the user as it came: it never waits on the
// assistant.
func TestDelegateTimesOut(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
		<-release
		return DelegateResult{}, nil
	})
	df.f.setHello(func(h *Hello) { h.Agents.Delegate.TimeoutSeconds = 1 })
	ix := delegateQuestion("ix_d3")
	df.ask(ix)
	time.Sleep(300 * time.Millisecond)
	if df.pushedFor(ix.ID) != nil {
		t.Fatal("pushed before the assistant had its time")
	}
	waitDelegate(t, "the push after the timeout", func() bool { return df.pushedFor(ix.ID) != nil })
	if list := df.s.pendingInteractions(); len(list) != 1 || list[0].Delegating || list[0].Delegate != nil {
		t.Fatalf("pending %+v", list)
	}

	failing := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
		return DelegateResult{}, context.DeadlineExceeded
	})
	failing.ask(delegateQuestion("ix_d3b"))
	waitDelegate(t, "the push after a failure", func() bool { return failing.pushedFor("ix_d3b") != nil })
}

// An answer the user gives while the assistant thinks wins: the assistant's comes too late and nothing is pushed.
func TestDelegateUserAnswersFirst(t *testing.T) {
	release := make(chan struct{})
	df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
		<-release
		return DelegateResult{Answer: true, Answers: map[string]string{"用哪个分支？": "feat/x"}}, nil
	})
	ix := delegateQuestion("ix_d4")
	answered := df.ask(ix)
	waitDelegate(t, "the assistant asked", func() bool { return df.delegateCalls() == 1 })
	if err := df.s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{"用哪个分支？": "main"}}); err != nil {
		t.Fatal(err)
	}
	if res := answerOf(t, answered); res.Answers["用哪个分支？"] != "main" || res.AnsweredBy != "" {
		t.Fatalf("resolution %+v", res)
	}
	close(release)
	time.Sleep(300 * time.Millisecond)
	if df.pushedFor(ix.ID) != nil {
		t.Fatal("pushed a question the user answered")
	}
}

// Permissions, plans, the assistant's own ask_user and forms are not the assistant's to answer: pushed at once, the
// assistant not asked. With agents.delegate off, or unset (the default), a question is pushed at once too.
func TestDelegateOnlyQuestions(t *testing.T) {
	df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) { return DelegateResult{Answer: true}, nil })
	perm := proto.Interaction{ID: "ix_p", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	plan := proto.Interaction{ID: "ix_plan", MachineID: "mac", SessionID: "s1", Kind: proto.IxPlan, Plan: "1. 改",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	own := delegateQuestion("ix_own")
	own.ToolName = "ask_user"
	own.ToolInput = json.RawMessage(`{"why_owner":"priority"}`)
	form := delegateQuestion("ix_form")
	form.Fields = []proto.Field{{Key: "pw", Label: "密码", Secret: true}}
	for _, ix := range []proto.Interaction{perm, plan, own, form} {
		df.ask(ix)
		waitDelegate(t, ix.ID+" pushed", func() bool { return df.pushedFor(ix.ID) != nil })
	}
	off := false
	df.f.setHello(func(h *Hello) { h.Agents.Delegate.Enabled = &off })
	df.ask(delegateQuestion("ix_off"))
	waitDelegate(t, "pushed with delegate off", func() bool { return df.pushedFor("ix_off") != nil })
	df.f.setHello(func(h *Hello) { h.Agents.Delegate.Enabled = nil })
	df.ask(delegateQuestion("ix_unset"))
	waitDelegate(t, "pushed with delegate unset", func() bool { return df.pushedFor("ix_unset") != nil })
	if got := df.delegateCalls(); got != 0 {
		t.Fatalf("delegated %d times", got)
	}
}

// "Change answer": the user's answer to a question the assistant answered goes to the agent as a message (with what the
// assistant had said), once; one the user answered themselves is refused.
func TestReanswer(t *testing.T) {
	df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
		return DelegateResult{Answer: true, Answers: map[string]string{"用哪个分支？": "feat/x"}, Reason: "任务在 feat/x"}, nil
	})
	answerOf(t, df.ask(delegateQuestion("ix_re")))
	h := df.s.fleetHandler(http.NotFoundHandler())
	post := func(id, body string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/interactions/"+id+"/reanswer", strings.NewReader(body)))
		return w.Code
	}
	if code := post("ix_re", `{"answers":{"用哪个分支？":"main"}}`); code != 200 {
		t.Fatalf("reanswer: %d", code)
	}
	sent := df.f.sentToAgents()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "s1: ") || !strings.Contains(sent[0], "用哪个分支？ → main（助手答的是：feat/x）") {
		t.Fatalf("sent %q", sent)
	}
	if !strings.Contains(df.f.eventTypes(), "interaction_reanswered") {
		t.Fatalf("the apps were not told: %s", df.f.eventTypes())
	}
	if code := post("ix_re", `{"answers":{"用哪个分支？":"main"}}`); code != 200 || len(df.f.sentToAgents()) != 1 {
		t.Fatalf("again: %d, sent %q", code, df.f.sentToAgents())
	}
	if code := post("ix_re", `{"answers":{}}`); code != 400 {
		t.Fatalf("no answers: %d", code)
	}

	off := false
	df.f.setHello(func(h *Hello) { h.Agents.Delegate.Enabled = &off })
	mine := delegateQuestion("ix_mine")
	answered := df.ask(mine)
	df.waitPending(1)
	if err := df.s.resolve(mine.ID, proto.Resolution{Answers: map[string]string{"用哪个分支？": "main"}}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, answered)
	if code := post("ix_mine", `{"answers":{"用哪个分支？":"feat/x"}}`); code != 409 {
		t.Fatalf("reanswer of the user's own answer: %d", code)
	}
}

// A card the manager made from a turn that ended on a statement asks nothing: it goes to the user, not the assistant.
// One whose question has a question mark, or options of its own, is a question and is delegated as before.
func TestDelegateLeavesAStatementToTheUser(t *testing.T) {
	df := newDelegateFleet(t, func(p DelegateParams) (DelegateResult, error) {
		q := p.Interaction.Questions[0]
		return DelegateResult{Answer: true, Answers: map[string]string{q.Question: q.Options[0].Label}}, nil
	})
	statement := delegateQuestion("work_stmt")
	statement.ToolName = workCardTool
	statement.Questions = []proto.Question{{Question: "等 runner 恢复和 0.14.0 发版",
		Options: []proto.QuestionOption{{Label: answerOK}, {Label: answerSkip}}}}
	df.ask(statement)
	waitDelegate(t, "the push", func() bool { return df.pushedFor(statement.ID) != nil })
	if got := df.delegateCalls(); got != 0 {
		t.Fatalf("a statement was delegated %d times", got)
	}

	asked := delegateQuestion("work_q")
	asked.ToolName = workCardTool
	asked.Questions = []proto.Question{{Question: "PR 还是 Draft，下一步？",
		Options: []proto.QuestionOption{{Label: answerOK}, {Label: answerSkip}}}}
	if res := answerOf(t, df.ask(asked)); res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("a question with a question mark: %+v", res)
	}
	options := delegateQuestion("work_opts")
	options.ToolName = workCardTool
	options.Questions = []proto.Question{{Question: "下一步走哪条路",
		Options: []proto.QuestionOption{{Label: "先做 A"}, {Label: "先做 B"}, {Label: answerSkip}}}}
	if res := answerOf(t, df.ask(options)); res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("a question with options of its own: %+v", res)
	}
}

// The assistant answers a question of an agent's once: the same one again (a new card, worded a little differently)
// goes to the user, and the assistant is not asked.
func TestDelegateDoesNotAnswerTheSameQuestionTwice(t *testing.T) {
	df := newDelegateFleet(t, func(p DelegateParams) (DelegateResult, error) {
		q := p.Interaction.Questions[0]
		return DelegateResult{Answer: true, Answers: map[string]string{q.Question: q.Options[0].Label}}, nil
	})
	first := delegateQuestion("work_r1")
	first.ToolName = workCardTool
	first.Questions = []proto.Question{{Question: "等 runner 恢复 + 0.14.0 发版，可以吗？",
		Options: []proto.QuestionOption{{Label: answerOK}, {Label: answerSkip}}}}
	if res := answerOf(t, df.ask(first)); res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("first: %+v", res)
	}
	again := first
	again.ID = "work_r2"
	again.Questions = []proto.Question{{Question: "等 runner 恢复和 0.14.0 发版，可以吗？",
		Options: []proto.QuestionOption{{Label: answerOK}, {Label: answerSkip}}}}
	answered := df.ask(again)
	waitDelegate(t, "the push", func() bool { return df.pushedFor(again.ID) != nil })
	if got := df.delegateCalls(); got != 1 {
		t.Fatalf("asked the assistant %d times", got)
	}
	// Another agent's same question, or another question of this one's, is the assistant's to answer.
	other := delegateQuestion("ix_r3")
	if res := answerOf(t, df.ask(other)); res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("another question: %+v", res)
	}
	if err := df.s.resolve(again.ID, proto.Resolution{Answers: map[string]string{again.Questions[0].Question: answerSkip}}); err != nil {
		t.Fatal(err)
	}
	if res := answerOf(t, answered); res.AnsweredBy != "" {
		t.Fatalf("the repeat: %+v", res)
	}
}

// The same question of an agent's coming again while the assistant still decides on the first goes to the user: the
// assistant is asked once, the user gets one push, and the first is still answered for the user.
func TestDelegateSameQuestionWhileDeciding(t *testing.T) {
	release := make(chan struct{})
	df := newDelegateFleet(t, func(p DelegateParams) (DelegateResult, error) {
		<-release // a slow call
		q := p.Interaction.Questions[0]
		return DelegateResult{Answer: true, Answers: map[string]string{q.Question: q.Options[0].Label}}, nil
	})
	first := delegateQuestion("ix_w1")
	firstAnswered := df.ask(first)
	waitDelegate(t, "the first call", func() bool { return df.delegateCalls() == 1 })
	again := delegateQuestion("ix_w2")
	againAnswered := df.ask(again)
	waitDelegate(t, "the push or a second call", func() bool { return df.pushedFor(again.ID) != nil || df.delegateCalls() > 1 })
	close(release)
	if got := df.delegateCalls(); got != 1 {
		t.Fatalf("asked the assistant %d times", got)
	}
	if res := answerOf(t, firstAnswered); res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("first: %+v", res)
	}
	pushes := 0
	for _, n := range df.f.pushed() {
		if n.Interaction != nil && (n.Interaction.ID == first.ID || n.Interaction.ID == again.ID) {
			pushes++
		}
	}
	if pushes != 1 || df.pushedFor(again.ID) == nil {
		t.Fatalf("%d pushes", pushes)
	}
	if err := df.s.resolve(again.ID, proto.Resolution{Answers: map[string]string{again.Questions[0].Question: "main"}}); err != nil {
		t.Fatal(err)
	}
	if res := answerOf(t, againAnswered); res.AnsweredBy != "" {
		t.Fatalf("again: %+v", res)
	}
}

// A call that fails takes its note back: the same question again is the assistant's to answer.
func TestDelegateFailedCallForgets(t *testing.T) {
	fail := true
	var mu sync.Mutex
	df := newDelegateFleet(t, func(p DelegateParams) (DelegateResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			fail = false
			return DelegateResult{}, context.DeadlineExceeded
		}
		q := p.Interaction.Questions[0]
		return DelegateResult{Answer: true, Answers: map[string]string{q.Question: q.Options[0].Label}}, nil
	})
	first := delegateQuestion("ix_f1")
	// The app can receive the escalated card before the goroutine that sent it returns.
	// Check the failed claim at that handoff as well as actually submitting the next question.
	claimedAtHandoff := make(chan bool, 1)
	df.f.mu.Lock()
	df.f.onTell = func() {
		shown := df.shownAs(first.ID)
		if len(shown) == 0 || shown[len(shown)-1].Delegating {
			return
		}
		df.s.work.mu.Lock()
		claimed := slices.ContainsFunc(df.s.work.delegated[[2]string{first.MachineID, first.SessionID}],
			func(ask delegatedAsk) bool { return ask.id == first.ID })
		df.s.work.mu.Unlock()
		select {
		case claimedAtHandoff <- claimed:
		default:
		}
	}
	df.f.mu.Unlock()
	firstAnswered := df.ask(first)
	waitDelegate(t, "the push", func() bool { return df.pushedFor(first.ID) != nil })
	select {
	case claimed := <-claimedAtHandoff:
		if claimed {
			t.Error("the failed call still owns the question as the app receives its escalation")
		}
	default:
		t.Error("the app was not told the question had escalated")
	}
	if res := answerOf(t, df.ask(delegateQuestion("ix_f2"))); res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("after a failed call: %+v", res)
	}
	if err := df.s.resolve(first.ID, proto.Resolution{Answers: map[string]string{first.Questions[0].Question: "main"}}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, firstAnswered)
}

func TestSameAsk(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"等 runner 恢复 + im-extension 0.14.0 发版", "等 runner 恢复和 im-extension 0.14.0 发版", true},
		{"用哪个分支？", "用哪个分支?", true},
		{"用哪个分支？", "要不要合并？", false},
		{"PR #100 还是 Draft，下一步？", "PR #3407 的 CI 一直排队，接下来怎么处理？", false},
	} {
		a := delegateText(proto.Interaction{Questions: []proto.Question{{Question: c.a}}})
		b := delegateText(proto.Interaction{Questions: []proto.Question{{Question: c.b}}})
		if got := sameAsk(a, b); got != c.same {
			t.Errorf("sameAsk(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
