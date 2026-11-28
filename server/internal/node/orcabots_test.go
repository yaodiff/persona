package node

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Orca bots on this computer (#16891): one bot whose Home Chat is a structured session in its home folder, another
// structured session there that the bot ran on its own (a Look around), and the items it keeps.
const (
	orcaBotID          = "bot-c3"
	fakeBotHome        = "orca_home_a1"
	fakeBotHomeUUID    = "6f1e2d3c-4b5a-4c69-8d7e-0a1b2c3d4e5f"
	orcaBotRun         = "orca_run_b2"
	orcaBotRunUUID     = "7a2b3c4d-5e6f-4a7b-9c8d-1e2f3a4b5c6d"
	fakeBotNewHome     = "orca_home_new"
	fakeBotNewHomeUUID = "8b3c4d5e-6f7a-4b8c-8d9e-2f3a4b5c6d7e"
	// fakeBotTabHome is a Home Chat a reset has just opened: no turn yet, so only Orca's tabs list it.
	fakeBotTabHome     = "orca_home_tab"
	fakeBotTabHomeUUID = "9c4d5e6f-7a8b-4c9d-8e0f-3a4b5c6d7e8f"
	orcaBotFolder      = "/home/example/.orca/bots/helper"
	orcaBotWorktree    = "folder:e5"
	orcaBotItemUID     = "item-d4"
	orcaBotQuestion    = "Which first?"
	orcaBotQuestion2   = "**Ship** it today?\nMore words."
)

// orcaBots is a fake Orca with the bot: its roster, chats, items and the two structured sessions' rows and history.
type orcaBots struct {
	f *orcarttest.Fake

	mu       sync.Mutex
	home     string // the open Home Chat's Orca session
	status   string // the item's status
	question string // the item question's event ("": none)
	twoAsks  bool   // the question has two decisions
	closed   bool   // Orca closed the first Home Chat (a reset): worktree.ps no longer lists it
	tabs     []any  // the chats among the tabs (session.tabs.listAll) beyond worktree.ps's
	refuse   int    // how many answers Orca refuses before it takes one
}

func newOrcaBots(t *testing.T) *orcaBots {
	t.Helper()
	t.Setenv("ORCA_USER_DATA_PATH", t.TempDir())
	o := &orcaBots{f: orcarttest.New(t), home: fakeBotHome, status: "needs_you", question: "e4", tabs: []any{}}
	o.trees()
	providers := map[string]string{fakeBotHome: fakeBotHomeUUID, orcaBotRun: orcaBotRunUUID, fakeBotNewHome: fakeBotNewHomeUUID,
		fakeBotTabHome: fakeBotTabHomeUUID}
	o.f.Handle("agentSession.history", func(p map[string]any) any {
		session, _ := p["sessionId"].(string)
		provider, ok := providers[session]
		if !ok {
			return orcarttest.Fail{Code: "agent_session_identity_required"}
		}
		items := []any{map[string]any{"itemId": session + "-reply", "revision": 1, "sequence": 3, "observedAt": 1795756238978,
			"body": map[string]any{"kind": "message", "role": "assistant", "blocks": []any{map[string]any{"type": "text", "text": "Reply from " + session}}}}}
		return map[string]any{"ok": true, "page": map[string]any{"sessionId": session, "epoch": "epoch-1", "fence": 7,
			"working": false, "items": items, "hasOlder": false, "hasNewer": false, "window": map[string]any{}},
			"providerSession": map[string]any{"key": "session_id", "id": provider}}
	})
	o.f.Handle("agentSession.options", func(map[string]any) any { return map[string]any{"current": map[string]any{}} })
	o.f.Handle("agentSession.currentRequests", func(map[string]any) any {
		return map[string]any{"marker": map[string]any{"version": "v1"}, "json": "[]", "nextCursor": nil}
	})
	o.f.Handle("agentSession.send", func(map[string]any) any {
		return map[string]any{"ok": true, "replayed": false, "value": map[string]any{}}
	})
	o.f.Handle("session.tabs.listAll", func(map[string]any) any {
		o.mu.Lock()
		defer o.mu.Unlock()
		return map[string]any{"snapshots": []any{map[string]any{"worktree": orcaBotWorktree, "tabs": o.tabs}}}
	})
	o.f.Handle("bots.roster", func(map[string]any) any {
		return map[string]any{"bots": []any{map[string]any{"id": orcaBotID, "name": "Helper", "agent": "claude", "paused": false,
			"linked": []any{}, "specialty": "Tidies the workspace."}}}
	})
	o.f.Handle("bots.chats", func(p map[string]any) any {
		if p["botId"] != orcaBotID {
			return orcarttest.Fail{Code: "bot_not_found"}
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		chats := []any{map[string]any{"id": o.home, "title": "Home", "lastActivityAt": 1795756238978, "home": true, "closed": false}}
		if o.home != fakeBotHome {
			chats = append(chats, map[string]any{"id": fakeBotHome, "title": "Home", "lastActivityAt": 1795756000000, "home": true, "closed": true})
		}
		return map[string]any{"bot": "Helper", "chats": chats}
	})
	o.f.Handle("bots.items.list", func(p map[string]any) any {
		o.mu.Lock()
		defer o.mu.Unlock()
		item := map[string]any{"uid": orcaBotItemUID, "number": 1, "title": "Find latency wins", "status": o.status,
			"chat": map[string]any{"id": fakeBotHome, "kind": "home", "title": "Home"}, "lastActivityAt": 1795756224333}
		if o.question != "" {
			decisions := []any{map[string]any{"question": orcaBotQuestion, "multi": true, "replies": []any{
				map[string]any{"label": "Front end", "detail": "items 1, 2 and 4", "recommended": true},
				map[string]any{"label": "Not now", "recommended": false}}}}
			text := "**Conclusion:** room left.\n\n" + orcaBotQuestion + "\n   - Front end (recommended): items 1, 2 and 4\n   - Not now"
			if o.twoAsks {
				decisions = append(decisions, map[string]any{"question": orcaBotQuestion2, "replies": []any{
					map[string]any{"label": "Yes", "recommended": false}, map[string]any{"label": "No", "recommended": false}}})
				text = "**Conclusion:** room left.\n\n1. " + orcaBotQuestion + "\n   - Front end\n   - Not now\n\n2. " + orcaBotQuestion2
			}
			item["question"] = map[string]any{"eventId": o.question, "gist": "Conclusion: room left.", "text": text, "decisions": decisions}
		}
		var items []any
		if statuses, _ := p["statuses"].([]any); len(statuses) == 0 || statuses[0] == o.status {
			items = append(items, item)
		}
		return map[string]any{"botId": orcaBotID, "botName": "Helper", "items": items}
	})
	o.f.Handle("bots.items.owner.comment", func(map[string]any) any {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.refuse > 0 {
			o.refuse--
			return orcarttest.Fail{Code: "item_busy"}
		}
		return map[string]any{"eventId": "e5"}
	})
	return o
}

// trees lists the chats in the bot's folder in worktree.ps: its first Home Chat (until Orca closed it), its own run and
// the chat a reset makes its Home Chat.
func (o *orcaBots) trees() {
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	agents := []any{}
	if !closed {
		agents = append(agents, map[string]any{"paneKey": "structured-agent-session-" + fakeBotHome + ":aa", "agentType": "claude",
			"state": "done", "conversationName": "Helper", "updatedAt": 1795756239063})
	}
	agents = append(agents,
		map[string]any{"paneKey": "structured-agent-session-" + orcaBotRun + ":bb", "agentType": "claude", "state": "done",
			"conversationName": "Look around", "updatedAt": 1795751550629},
		map[string]any{"paneKey": "structured-agent-session-" + fakeBotNewHome + ":cc", "agentType": "claude", "state": "done",
			"conversationName": "Helper", "updatedAt": 1795751550000})
	o.f.SetWorktrees(map[string]any{"worktreeId": orcaBotWorktree, "path": orcaBotFolder, "branch": "", "hostId": "local",
		"agents": agents})
}

// set changes the fake's bot under its lock, then lists its chats again.
func (o *orcaBots) set(fn func(o *orcaBots)) {
	o.mu.Lock()
	fn(o)
	o.mu.Unlock()
	o.trees()
}

// botNode is a node whose Orca client is the fake (no hub, no bridge): the manager's agentbot.* calls.
func botNode(t *testing.T, o *orcaBots) *Node {
	t.Helper()
	saved := orcaClient
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: o.f.MetaPath} }
	t.Cleanup(func() { orcaClient = saved })
	return &Node{}
}

func callBot(t *testing.T, n *Node, method string, params, out any) error {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := n.handleMethod(context.Background(), method, raw)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

// agentbot.roster lists the computer's bots from bots.roster with each one's open Home Chat (bots.chats), home folder
// (the worktree its Home Chat runs in) and open items; an Orca that is not running is no bots, not a failure.
func TestAgentBotRosterFindsBotsAndTheirHomeChats(t *testing.T) {
	o := newOrcaBots(t)
	n := botNode(t, o)
	var roster proto.OrcaBotRoster
	if err := callBot(t, n, "agentbot.roster", map[string]any{}, &roster); err != nil {
		t.Fatal(err)
	}
	if !roster.Running || len(roster.Bots) != 1 {
		t.Fatalf("roster %+v", roster)
	}
	b := roster.Bots[0]
	if b.BotID != orcaBotID || b.Name != "Helper" || b.Agent != "claude" || b.Specialty != "Tidies the workspace." ||
		b.HomeSessionID != fakeBotHome || b.HomeTitle != "Home" || b.Folder != orcaBotFolder || b.NeedsYou != 1 || b.Open != 1 {
		t.Fatalf("bot %+v", b)
	}
	o.f.Stop()
	if err := callBot(t, n, "agentbot.roster", map[string]any{}, &roster); err != nil {
		t.Fatalf("Orca not running: %v", err)
	}
	if roster.Running || len(roster.Bots) != 0 {
		t.Fatalf("roster without Orca %+v", roster)
	}
}

// A bot bound by its id is read from its open Home Chat, and after the bot started a new Home Chat from the new one,
// never from the closed one; agentbot.items reads its items.
func TestAgentBotHistoryFollowsHomeChatReset(t *testing.T) {
	o := newOrcaBots(t)
	n := botNode(t, o)
	var page agents.BotPage
	if err := callBot(t, n, "agentbot.history", agents.BotHistoryRequest{BotID: orcaBotID, Limit: 5}, &page); err != nil {
		t.Fatal(err)
	}
	if page.SessionID != fakeBotHome || len(page.Messages) != 1 || page.Messages[0].Blocks[0].Text != "Reply from "+fakeBotHome {
		t.Fatalf("page %+v", page)
	}
	o.set(func(o *orcaBots) { o.home = fakeBotNewHome })
	if err := callBot(t, n, "agentbot.history", agents.BotHistoryRequest{BotID: orcaBotID, Limit: 5}, &page); err != nil {
		t.Fatal(err)
	}
	if page.SessionID != fakeBotNewHome || page.Messages[0].Blocks[0].Text != "Reply from "+fakeBotNewHome {
		t.Fatalf("after the reset the page came from %q: %+v", page.SessionID, page)
	}
	var items proto.OrcaBotItems
	if err := callBot(t, n, "agentbot.items", map[string]any{"bot_id": orcaBotID}, &items); err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 1 || items.Items[0].ItemID != orcaBotItemUID || items.Items[0].Question == nil {
		t.Fatalf("items %+v", items)
	}
	var rerr *rpc.Error
	if err := callBot(t, n, "agentbot.history", agents.BotHistoryRequest{BotID: "bot-gone"}, &page); err == nil ||
		!asRPC(err, &rerr) || rerr.Code != "not_found" {
		t.Fatalf("an unknown bot: %v", err)
	}
	if err := callBot(t, n, "agentbot.history", agents.BotHistoryRequest{BotID: orcaBotID, SessionID: fakeBotHome}, &page); err == nil ||
		!asRPC(err, &rerr) || rerr.Code != "invalid" {
		t.Fatalf("a request naming both a bot and a session: %v", err)
	}
}

// A send naming a bot goes to the exact chat the manager resolved while it is still the bot's Home Chat; once the bot
// started a new one, the send is refused with nothing written, never re-targeted.
func TestAgentBotSendIsPinnedToTheResolvedHomeChat(t *testing.T) {
	o := newOrcaBots(t)
	n := botNode(t, o)
	var receipt map[string]any
	req := agents.BotSendRequest{BotID: orcaBotID, SessionID: fakeBotHome, OperationID: orcart.OperationID(time.UnixMilli(1795756300000), "k1"), Text: "Reply with one word."}
	if err := callBot(t, n, "agentbot.send", req, &receipt); err != nil || receipt["accepted"] != true {
		t.Fatalf("send %v %v", receipt, err)
	}
	sends := o.f.Params("agentSession.send")
	if len(sends) != 1 {
		t.Fatalf("%d writes", len(sends))
	}
	if env, _ := sends[0]["envelope"].(map[string]any); env["sessionId"] != fakeBotHome {
		t.Fatalf("written to %v", env["sessionId"])
	}
	o.set(func(o *orcaBots) { o.home = fakeBotNewHome })
	req.OperationID = orcart.OperationID(time.UnixMilli(1795756400000), "k2")
	var rerr *rpc.Error
	if err := callBot(t, n, "agentbot.send", req, &receipt); err == nil || !asRPC(err, &rerr) || rerr.Code != "conflict" {
		t.Fatalf("a send after the reset: %v", err)
	}
	if got := len(o.f.Params("agentSession.send")); got != 1 {
		t.Fatalf("the send after the reset wrote (%d writes)", got)
	}
}

func asRPC(err error, out **rpc.Error) bool {
	e, ok := err.(*rpc.Error)
	if ok {
		*out = e
	}
	return ok
}

// orcaBotsBridge is a node with a fake hub whose Orca bridge runs against the fake bots, set up by setup before it
// starts; the hub answers cards with resolve (nil: it never answers while the test runs).
func orcaBotsBridge(t *testing.T, resolve func(proto.Interaction) proto.Resolution, setup ...func(o *orcaBots)) (*Node, *runtimeHub, *orcaBots) {
	t.Helper()
	o := newOrcaBots(t)
	for _, fn := range setup {
		o.set(fn)
	}
	saved, savedPoll, savedTabs, savedBots := orcaClient, orcaBridgePoll, orcaTabsPoll, orcaBotsPoll
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: o.f.MetaPath} }
	orcaBridgePoll, orcaTabsPoll, orcaBotsPoll = 50*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond
	n, hub := runtimeNode(t)
	release := make(chan struct{})
	if resolve == nil {
		resolve = func(proto.Interaction) proto.Resolution { <-release; return proto.Resolution{Dismiss: true} }
	}
	hub.mu.Lock()
	hub.resolve = resolve
	hub.mu.Unlock()
	n.runtimeHost.setOrcaBridge(true)
	t.Cleanup(func() {
		close(release)
		n.runtimeHost.setOrcaBridge(false)
		orcaClient, orcaBridgePoll, orcaTabsPoll, orcaBotsPoll = saved, savedPoll, savedTabs, savedBots
	})
	waitFor(t, "the Home Chat listed", func() bool { _, ok := listed(t, n)[fakeBotHomeUUID]; return ok })
	return n, hub, o
}

// The bot's own run in its home folder folds under its Home Chat as a side session, and its Home Chat stays a row of
// its own.
func TestOrcaBotRunsFoldUnderTheHomeChat(t *testing.T) {
	n, _, _ := orcaBotsBridge(t, nil)
	waitFor(t, "the run folded", func() bool { run := listed(t, n)[orcaBotRunUUID]; return run.ParentAgentID() == fakeBotHomeUUID })
	rows := listed(t, n)
	if home := rows[fakeBotHomeUUID]; home.ParentAgentID() != "" {
		t.Fatalf("the Home Chat folds under %q", home.ParentAgentID())
	}
}

// A question of an item waiting on the owner is one card on the Home Chat with the bot's decisions and quick replies;
// it leaves the phone when the item no longer waits on that question.
func TestOrcaBotItemQuestionIsOneCardTakenDownWhenAnswered(t *testing.T) {
	_, hub, o := orcaBotsBridge(t, nil)
	waitFor(t, "the item's card", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.asked) > 0 })
	reads := o.f.Count("bots.items.list")
	waitFor(t, "two more reads of the items", func() bool { return o.f.Count("bots.items.list") >= reads+2 }) // still one card
	hub.mu.Lock()
	asked := append([]proto.Interaction(nil), hub.asked...)
	hub.mu.Unlock()
	if len(asked) != 1 {
		t.Fatalf("%d cards for one question", len(asked))
	}
	ix := asked[0]
	if ix.Kind != proto.IxQuestion || ix.SessionID != fakeBotHomeUUID || !strings.HasPrefix(ix.ID, "orcaitem_") || ix.Format != "markdown" ||
		len(ix.Questions) != 1 || !ix.Questions[0].MultiSelect || ix.Questions[0].Recommend != "Front end" ||
		len(ix.Questions[0].Options) != 2 || ix.Questions[0].Options[0].Description != "items 1, 2 and 4" {
		t.Fatalf("card %+v", ix)
	}
	if q := ix.Questions[0].Question; !strings.HasPrefix(q, "**Find latency wins**\n\n**Conclusion:** room left.") ||
		!strings.HasSuffix(q, orcaBotQuestion) || strings.Contains(q, "- Not now") {
		t.Fatalf("question text %q", q)
	}
	o.set(func(o *orcaBots) { o.status, o.question = "working", "" }) // answered in Orca
	waitFor(t, "the card withdrawn", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.cancels) == 1 && hub.cancels[0] == ix.ID
	})
	if n := o.f.Count("bots.items.owner.comment"); n != 0 {
		t.Fatalf("answered %d times", n)
	}
}

// The owner's answer goes to the item in Orca's format, naming the question, with the card's id as the request id;
// it is posted once.
func TestOrcaBotItemAnswerGoesBackToOrca(t *testing.T) {
	_, hub, o := orcaBotsBridge(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: "Front end, Not now"}}
	})
	waitFor(t, "the answer in Orca", func() bool { return o.f.Count("bots.items.owner.comment") > 0 })
	reads := o.f.Count("bots.items.list")
	waitFor(t, "two more reads of the items", func() bool { return o.f.Count("bots.items.list") >= reads+2 }) // still one answer
	got := o.f.Params("bots.items.owner.comment")
	if len(got) != 1 {
		t.Fatalf("answered %d times", len(got))
	}
	hub.mu.Lock()
	id := hub.asked[0].ID
	hub.mu.Unlock()
	if got[0]["item"] != orcaBotItemUID || got[0]["questionId"] != "e4" || got[0]["text"] != "Front end, Not now" || got[0]["requestId"] != id {
		t.Fatalf("comment %v", got[0])
	}
}

// An answer Orca did not take is never dropped: while the item still waits on the question, it goes on the phone
// again as a card of its own, and the answer to that one lands, under that card's id.
func TestOrcaBotItemAnswerOrcaRefusedIsAskedAgain(t *testing.T) {
	_, hub, o := orcaBotsBridge(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: "Front end"}}
	}, func(o *orcaBots) { o.refuse = 1 })
	waitFor(t, "the answer to the second card in Orca", func() bool { return o.f.Count("bots.items.owner.comment") >= 2 })
	reads := o.f.Count("bots.items.list")
	waitFor(t, "two more reads of the items", func() bool { return o.f.Count("bots.items.list") >= reads+2 }) // no third card
	hub.mu.Lock()
	asked := append([]proto.Interaction(nil), hub.asked...)
	hub.mu.Unlock()
	got := o.f.Params("bots.items.owner.comment")
	if len(asked) != 2 || len(got) != 2 {
		t.Fatalf("%d cards and %d answers, want 2 and 2", len(asked), len(got))
	}
	key := orcaBotItemUID + "/e4"
	if asked[0].ID != orcaItemCardID(key, 0) || asked[1].ID != orcaItemCardID(key, 1) || asked[0].ID == asked[1].ID {
		t.Fatalf("card ids %q, %q", asked[0].ID, asked[1].ID)
	}
	if got[0]["requestId"] != asked[0].ID || got[1]["requestId"] != asked[1].ID || got[1]["text"] != "Front end" {
		t.Fatalf("answers %v", got)
	}
}

// After a reset Orca closed the bot's first Home Chat, which the bridge now lists closed: an item raised there is
// asked on the bot's open Home Chat, where the owner talks to the bot.
func TestOrcaBotItemRaisedInAClosedChatIsAskedOnTheHomeChat(t *testing.T) {
	n, hub, o := orcaBotsBridge(t, nil, func(o *orcaBots) { o.question = "" })
	o.set(func(o *orcaBots) { o.home, o.closed = fakeBotNewHome, true })
	waitFor(t, "the first Home Chat listed closed", func() bool {
		s, ok := listed(t, n)[fakeBotHomeUUID]
		return ok && s.Status == proto.StatusOffline
	})
	o.set(func(o *orcaBots) { o.question = "e4" }) // the item, raised in the first Home Chat, asks now
	waitFor(t, "the item's card", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.asked) > 0 })
	hub.mu.Lock()
	ix := hub.asked[0]
	hub.mu.Unlock()
	if ix.SessionID != fakeBotNewHomeUUID {
		t.Fatalf("asked on %q, want the open Home Chat %q", ix.SessionID, fakeBotNewHomeUUID)
	}
}

// A Home Chat a reset has just opened has no turn yet, so only Orca's tabs list it: the bot's own run and its earlier
// Home Chat fold under it at once, not after its first turn.
func TestOrcaBotRunsFoldUnderAHomeChatOnlyTheTabsList(t *testing.T) {
	n, _, o := orcaBotsBridge(t, nil)
	waitFor(t, "the run folded", func() bool { run := listed(t, n)[orcaBotRunUUID]; return run.ParentAgentID() == fakeBotHomeUUID })
	o.set(func(o *orcaBots) {
		o.home = fakeBotTabHome
		o.tabs = []any{map[string]any{"type": "agent-session", "sessionId": fakeBotTabHome, "agent": "claude", "title": "Helper"}}
	})
	waitFor(t, "the run folded under the new Home Chat", func() bool {
		rows := listed(t, n)
		run, first := rows[orcaBotRunUUID], rows[fakeBotHomeUUID]
		return run.ParentAgentID() == fakeBotTabHomeUUID && first.ParentAgentID() == fakeBotTabHomeUUID
	})
	if home := listed(t, n)[fakeBotTabHomeUUID]; home.ParentAgentID() != "" {
		t.Fatalf("the new Home Chat folds under %q", home.ParentAgentID())
	}
}

// With several decisions the answer is a line each, "<the question's first line, plain> → <answer>".
func TestOrcaItemAnswerSeveralDecisions(t *testing.T) {
	it := proto.OrcaBotItem{ItemID: orcaBotItemUID, Title: "Find latency wins", Question: &proto.OrcaItemQuestion{EventID: "e4",
		Text:      "Context.\n\n1. " + orcaBotQuestion + "\n\n2. " + orcaBotQuestion2,
		Decisions: []proto.OrcaItemDecision{{Question: orcaBotQuestion, Multi: true}, {Question: orcaBotQuestion2}}}}
	ix := orcaItemInteraction(fakeBotHomeUUID, it)
	if len(ix.Questions) != 2 || ix.Questions[0].Question != "**Find latency wins**\n\nContext.\n\n"+orcaBotQuestion ||
		ix.Questions[1].Question != orcaBotQuestion2 {
		t.Fatalf("card questions %+v", ix.Questions)
	}
	res := proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: "Front end", ix.Questions[1].Question: "No"}}
	if got, want := orcaItemAnswer(ix, it, res), orcaBotQuestion+" → Front end\nShip it today? → No"; got != want {
		t.Fatalf("answer %q, want %q", got, want)
	}
	if got := orcaItemAnswer(ix, it, proto.Resolution{Text: "Ask me tomorrow"}); got != "Ask me tomorrow" {
		t.Fatalf("typed answer %q", got)
	}
}

// Only sessions in the bot's home folder fold, under a Home Chat that is listed; the bot's other open chats, sessions
// elsewhere, subagents and ones that already name a parent stay as they are.
func TestLinkOrcaBotRuns(t *testing.T) {
	homes := &orcaBotHomes{homes: []orcaBotHome{{folder: orcaBotFolder, session: "home"}}, keep: map[string]bool{"home": true, "side": true}}
	list := []proto.Session{
		{SessionID: "home", Cwd: orcaBotFolder},
		{SessionID: "run", Cwd: orcaBotFolder},
		{SessionID: "below", Cwd: orcaBotFolder + "/notes"},
		{SessionID: "side", Cwd: orcaBotFolder},
		{SessionID: "elsewhere", Cwd: "/home/example/project"},
		{SessionID: "prefix", Cwd: orcaBotFolder + "-other"},
		{SessionID: "sub", Cwd: orcaBotFolder, IsSubagent: true},
		{SessionID: "linked", Cwd: orcaBotFolder},
	}
	list[7].SetParentAgentID("someone")
	linkOrcaBotRuns(list, homes)
	want := map[string]string{"home": "", "run": "home", "below": "home", "side": "", "elsewhere": "", "prefix": "", "sub": "", "linked": "someone"}
	for _, s := range list {
		if got := s.ParentAgentID(); got != want[s.SessionID] {
			t.Errorf("%s folds under %q, want %q", s.SessionID, got, want[s.SessionID])
		}
	}
	unlisted := []proto.Session{{SessionID: "run", Cwd: orcaBotFolder}}
	linkOrcaBotRuns(unlisted, homes)
	if unlisted[0].ParentAgentID() != "" {
		t.Fatal("folded under a Home Chat that is not listed")
	}
}
