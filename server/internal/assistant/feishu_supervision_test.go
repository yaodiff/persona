package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

func namedDirection(t *testing.T, e *routeEnv) {
	t.Helper()
	ctx := context.Background()
	e.s.directionsSync(ctx)
	if err := e.s.setDirectionName(ctx, "lead1", "Field guides", directionByUser); err != nil {
		t.Fatal(err)
	}
}

func TestLarkSupervisedTerminalDialog(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	const terminal = "  Which edition should we use?\n\n› 1. Pocket\n  2. Standard\n\n  Press enter to confirm or esc to go back\n"
	dialog := screen.ParseDialog(terminal)
	if dialog == nil || len(dialog.Options) != 2 {
		t.Fatalf("terminal fixture: %+v", dialog)
	}
	e := newRouteEnv(t, nil, true)
	namedDirection(t, e)
	ctx := context.Background()
	sess := proto.Session{SessionID: "editor", Title: "Reviewing page proofs", Cwd: "/code/guides", Agent: "claude", Status: proto.StatusBlocked}
	if err := e.s.linkTodoAgent(ctx, e.todo.ID, "mac", sess.SessionID, sess.Title, sess.Cwd, sess.Agent); err != nil {
		t.Fatal(err)
	}
	e.d.mu.Lock()
	e.d.machines["mac"].call = func(method string, params json.RawMessage) (any, error) {
		if method == "session.screen" && strings.Contains(string(params), sess.SessionID) {
			return map[string]any{"text": terminal, "dialog_rev": dialog.Rev}, nil
		}
		return nil, errNode
	}
	e.d.mu.Unlock()
	e.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_test", Features: FeishuFeatures{Cards: true, AllCards: true}}, nil
	})
	e.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		return map[string]any{"message_id": "om_terminal"}, nil
	})
	e.f.setSessions("mac", sess)
	e.sessions("mac", sess)
	id := "scr_mac_" + sess.SessionID + "_" + dialog.Rev
	waitFor(t, "terminal card on the apps", func() bool { _, ok := e.shown(id); return ok })
	if !e.lastShown(id).Delegating {
		t.Fatal("a supervised terminal dialog reached the owner before its supervisor")
	}
	notice := e.noticeWith("lead1", "interaction_id="+id)
	for _, want := range []string{dialog.Question, "Pocket", "Standard", sess.Title, "Mac"} {
		if !strings.Contains(notice, want) {
			t.Errorf("supervisor notice lacks %q: %s", want, notice)
		}
	}
	// The same waiting dialog comes back after its node reconnects, still
	// held by the supervisor, with no owner posts on either connection.
	first := e.lastShown(id).CreatedAt
	e.s.NodeDetached("mac")
	waitFor(t, "terminal route restored", func() bool {
		e.s.fleet.mu.Lock()
		defer e.s.fleet.mu.Unlock()
		p := e.s.fleet.interactions[id]
		return p != nil && p.ix.CreatedAt.After(first) && p.route.at() == "lead1"
	})
	e.f.withQueue(e.lastShown(id))
	// The supervisor could pick Pocket itself (ask_route_screen_test.go); here it hands the choice to the owner.
	if _, err := runJSON(t, e.s, "lead2", "interaction_answer", map[string]any{"machine": "mac", "session_id": sess.SessionID, "interaction_id": id, "answers": map[string]string{dialog.Question: "Pocket"}}); err == nil || !strings.Contains(err.Error(), "only the user") || len(e.d.callsTo("mac", "session.keys")) != 0 {
		t.Fatalf("a conversation the dialog was not handed to answered it: %v", err)
	}
	e.s.askTick(time.Now().Add(24 * time.Hour))
	e.s.askTick(time.Now().Add(48 * time.Hour))
	if len(e.f.appParams(AppFeishu, FeishuPost)) != 0 || e.pushesOf(id) != 0 {
		t.Fatal("the owner got the terminal card without an escalation")
	}
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "The reader needs to choose the edition."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owner's Lark card", func() bool { return len(e.f.appParams(AppFeishu, FeishuPost)) > 0 })
	posts := e.f.appParams(AppFeishu, FeishuPost)
	if len(posts) != 1 || e.pushesOf(id) != 1 {
		t.Fatalf("after escalation: %d cards, %d pushes", len(posts), e.pushesOf(id))
	}
	raw := posts[0]["card"].(string)
	var card botcard.Card
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Field guides", e.todo.Title} {
		if !strings.Contains(card.Header.Title.Content, want) {
			t.Errorf("title lacks %q: %s", want, card.Header.Title.Content)
		}
	}
	if card.Config == nil || card.Config.Summary == nil {
		t.Fatal("missing chat preview")
	}
	summary := card.Config.Summary.Content
	if !strings.Contains(summary, dialog.Question) {
		t.Errorf("chat preview lacks question: %q", summary)
	}
	t.Logf("Lark title %q, preview %q, fallback %q", card.Header.Title.Content, summary, posts[0]["fallback"])
	for _, want := range []string{sess.Title, "Mac", dialog.Question, "Pocket", "Standard"} {
		if !strings.Contains(raw, want) || !strings.Contains(posts[0]["fallback"].(string), want) {
			t.Errorf("card or fallback lacks %q: %v", want, posts[0])
		}
	}
	e.settle()
}

// The supervisor of a main-chat task still sees a terminal question when there
// are no named directions. An ordinary session and the owner's protected input
// keep their existing paths.
func TestLarkTerminalRoutingWithoutLeaders(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newRouteEnv(t, nil, false)
	screenAsk := func(id, session string) proto.Interaction {
		ix := askQuestion(id, session)
		ix.Kind, ix.Questions = proto.IxScreen, nil
		ix.Dialog = &screen.Dialog{Question: "Which edition?", Options: []screen.Option{{Key: "1", Label: "Pocket"}, {Key: "2", Label: "Standard"}}}
		return ix
	}
	plain := screenAsk("ordinary", "unrelated-session")
	e.raise(plain)
	e.noticeWith("conv1", "interaction_id="+plain.ID)
	if !e.lastShown(plain.ID).Delegating || e.pushesOf(plain.ID) != 0 {
		t.Fatal("ordinary non-owner terminal pushed to the owner instead of routing to main")
	}
	supervised := screenAsk("supervised", "worker-1")
	e.raise(supervised)
	e.noticeWith("conv1", "interaction_id="+supervised.ID)
	if e.pushesOf(supervised.ID) != 0 || !e.lastShown(supervised.ID).Delegating {
		t.Fatal("supervised terminal did not wait for its owning conversation")
	}
	args := map[string]any{"todo_id": e.todo.ID, "reason": "The reader must choose."}
	if _, err := runJSON(t, e.s, "unrelated", "ask_escalate", args); err == nil {
		t.Fatal("unrelated conversation escalated this task")
	}
	if _, err := runJSON(t, e.s, "conv1", "ask_escalate", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "explicitly escalated terminal sent to owner", func() bool { return e.pushesOf(supervised.ID) == 1 })
	// lead1 exists but is not a configured direction. A side conversation which
	// actually supervises the next question must receive and be able to escalate it.
	e.s.superviseTodo(t.Context(), e.todo.ID, "lead1", []string{"Review the edition"}, 30)
	side := screenAsk("side", "worker-2")
	e.raise(side)
	e.noticeWith("lead1", "interaction_id="+side.ID)
	if !e.lastShown(side.ID).Delegating || e.pushesOf(side.ID) != 0 {
		t.Fatal("side conversation's supervised terminal reached the owner")
	}
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "side conversation's explicit escalation", func() bool { return e.pushesOf(side.ID) == 1 })
	e.settle()
}

func TestLarkTerminalProtectedInput(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	const privateText = "SYNTHETIC-PRIVATE-VALUE"
	ix := proto.Interaction{Kind: proto.IxScreen, Screen: privateText, Dialog: &screen.Dialog{
		Question: "Which edition?", Detail: privateText,
		Options: []screen.Option{{Key: "1", Label: "Pocket", Detail: privateText}, {Key: "2", Kind: screen.KindTypeSomething, Label: "Other", Text: privateText}},
	}}
	if !routable(ix) || strings.Contains(terminalQuestionText(ix), privateText) {
		t.Fatal("only a choice's question and plain labels can go to its supervisor")
	}
	for name, change := range map[string]func(*proto.Interaction){
		"raw":     func(v *proto.Interaction) { v.Dialog = nil },
		"secret":  func(v *proto.Interaction) { v.Secret = true },
		"private": func(v *proto.Interaction) { v.Private = true },
		"form":    func(v *proto.Interaction) { v.Fields = []proto.Field{{}} },
	} {
		v, d := ix, *ix.Dialog
		v.Dialog = &d
		change(&v)
		if routable(v) {
			t.Errorf("%s prompt went to a supervisor", name)
		}
	}
	// A command approval, a warning and a countdown are the supervisor's to decide (#13752's rule for permissions);
	// the plain-question wording (terminalQuestion) is for the ordinary choice only.
	for name, change := range map[string]func(*proto.Interaction){
		"command":  func(v *proto.Interaction) { v.Dialog.Command = "example" },
		"warning":  func(v *proto.Interaction) { v.Dialog.Warning = "warning" },
		"deadline": func(v *proto.Interaction) { v.Dialog.DeadlineAction = screen.DeadlineDeny },
	} {
		v, d := ix, *ix.Dialog
		v.Dialog = &d
		change(&v)
		if !routable(v) || terminalQuestion(v) || userScreen(v) != "" {
			t.Errorf("%s prompt: routable %v, ordinary %v, user's %q", name, routable(v), terminalQuestion(v), userScreen(v))
		}
	}
	for _, fixture := range []string{"claude-2.1.322-bypass-confirm-160.txt", "claude-2.1.312-trust-162.txt"} {
		plain, err := os.ReadFile("../screen/testdata/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		ix.Dialog = screen.ParseDialog(string(plain))
		// Held by the supervisor (never pushed on its own), but only the owner answers it: ask_escalate.
		if ix.Dialog == nil || !routable(ix) || userScreen(ix) == "" || terminalQuestion(ix) {
			t.Errorf("%s permission should remain the owner's to answer", fixture)
		}
	}
}

func TestLarkSupervisionCardTitles(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newRouteEnv(t, nil, true)
	namedDirection(t, e)
	ctx := context.Background()
	it := &reportItem{todo: e.todo.ID, state: reportFailed}
	reports, _ := e.s.reportCards(ctx, []*reportItem{it})
	stuck, _ := e.s.goalCard(ctx, []*reportItem{it})
	cards := map[string]*botcard.Card{
		"progress": reports[0], "stuck": stuck,
		"conclusion": e.s.conclusionCard(ctx, e.todo, acceptAsk{Conclusion: true, Outcome: outcomeDone}),
		"verify":     e.s.acceptCard(ctx, e.todo, acceptAsk{}, false),
		"waiting":    e.s.interactionCard(ctx, askQuestion("q1", "worker-1")),
	}
	for label, c := range cards {
		if title := c.Header.Title.Content; !strings.Contains(title, "Field guides") || !strings.Contains(title, e.todo.Title) {
			t.Errorf("%s title = %q; want direction and task", label, title)
		}
	}
}
