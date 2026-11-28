package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// orcaBotsPeer is a computer's node for the Orca bot calls: its roster, one bot's items and Home Chat, and the sends.
type orcaBotsPeer struct {
	nodeCaller
	mu     sync.Mutex
	roster proto.OrcaBotRoster
	fail   bool // agentbot.roster fails (the computer does not answer)
	calls  []string
	sends  []agents.BotSendRequest
}

func (p *orcaBotsPeer) Done() <-chan struct{} { return nil }

func (p *orcaBotsPeer) Call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, method+" "+string(raw))
	var res any
	switch method {
	case "agentbot.roster":
		if p.fail {
			return &rpc.Error{Code: "unavailable", Message: "bots are unavailable"}
		}
		res = p.roster
	case "agentbot.items":
		res = proto.OrcaBotItems{BotID: "bot-1", Name: "Helper", Items: []proto.OrcaBotItem{{ItemID: "item-1", Number: 1,
			Title: "Find latency wins", Status: proto.OrcaItemNeedsYou, ChatTitle: "Home", Question: &proto.OrcaItemQuestion{
				EventID: "e4", Gist: "Room left.", Text: "Room left.\n\nWhich first?", Decisions: []proto.OrcaItemDecision{
					{Question: "Which first?", Replies: []proto.OrcaItemReply{{Label: "Front end", Recommended: true}, {Label: "Not now"}}}}}}}}
	case "agentbot.describe", "agentbot.history":
		var req agents.BotHistoryRequest
		_ = json.Unmarshal(raw, &req)
		if req.BotID != "bot-1" {
			return &rpc.Error{Code: "not_found", Message: "bot is not on this computer"}
		}
		res = agents.BotPage{CanSend: true, SessionID: "chat-home", Messages: []agents.Message{
			{ExternalID: "m1", Role: "user", CreateTimeMs: 1795756000000, Blocks: []agents.Block{{Type: "text", Text: "Look into the latency."}}},
			{ExternalID: "m2", Role: "reasoning", CreateTimeMs: 1795756100000, Blocks: []agents.Block{{Type: "text", Text: "Thinking it over."}}},
			{ExternalID: "m3", Role: "assistant", CreateTimeMs: 1795756200000, Blocks: []agents.Block{
				{Type: "tool-call", Name: "Bash"}, {Type: "text", Text: "Done: room left."}}},
		}}
	case "agentbot.send":
		var req agents.BotSendRequest
		_ = json.Unmarshal(raw, &req)
		p.sends = append(p.sends, req)
		res = map[string]any{"accepted": true}
	default:
		return &rpc.Error{Code: "invalid", Message: "unexpected " + method}
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func (p *orcaBotsPeer) set(fn func(p *orcaBotsPeer)) {
	p.mu.Lock()
	fn(p)
	p.mu.Unlock()
}

func helperRoster() proto.OrcaBotRoster {
	return proto.OrcaBotRoster{Running: true, Bots: []proto.OrcaBot{{BotID: "bot-1", Name: "Helper", Agent: "claude",
		Specialty: "Tidies the workspace.", HomeSessionID: "chat-home", HomeTitle: "Home", LastActiveMs: 1795756238978,
		NeedsYou: 1, Open: 1}}}
}

// orcaBotsManager is a manager with one computer (machine-b) whose node announces the Orca bot calls.
func orcaBotsManager(t *testing.T, capable bool) (*Manager, *orcaBotsPeer) {
	t.Helper()
	needsLarkModule(t)
	h := newTestManager(t)
	peer := &orcaBotsPeer{roster: helperRoster()}
	m := &Machine{cfg: MachineConfig{ID: "machine-b", Name: "Machine B"}, peer: peer, online: true}
	m.info.Protocol = nodeproto.Version
	if capable {
		m.info.Capabilities = []string{nodeproto.Sessions, nodeproto.OrcaBots}
	}
	h.machines, h.byID = []*Machine{m}, map[string]*Machine{"machine-b": m}
	return h, peer
}

// A computer's bots become Agents → Bots sources bound to the bot (never a session): added when found, kept while the
// computer does not answer, removed when its roster no longer has them.
func TestOrcaBotDiscoveryKeepsOneSourcePerBot(t *testing.T) {
	h, peer := orcaBotsManager(t, true)
	h.initAgents()
	ctx := context.Background()
	h.discoverOrcaBots(ctx)
	reg := agents.DiscoveredBot("machine-b", "bot-1", "Helper")
	name := "bot_" + reg.ID
	if h.agents.Get(name) == nil {
		t.Fatalf("no source for the bot (found %v)", h.orcaBots.found)
	}
	list, err := h.agents.Sessions(ctx, name, "")
	if err != nil || len(list) != 1 || list[0].ID != name+":endpoint" || list[0].Title != "Helper" {
		t.Fatalf("the bot's directory %+v %v", list, err)
	}
	for _, call := range peer.calls {
		if strings.HasPrefix(call, "agentbot.describe") && (!strings.Contains(call, `"bot_id":"bot-1"`) || strings.Contains(call, "session_id")) {
			t.Fatalf("read by session, not by the bot: %s", call)
		}
	}
	peer.set(func(p *orcaBotsPeer) { p.fail = true })
	h.discoverOrcaBots(ctx)
	if h.agents.Get(name) == nil {
		t.Fatal("a computer that did not answer lost its bot")
	}
	peer.set(func(p *orcaBotsPeer) { p.fail, p.roster = false, proto.OrcaBotRoster{Running: false} })
	h.discoverOrcaBots(ctx)
	if h.agents.Get(name) != nil {
		t.Fatal("the bot of an Orca that is not running is still listed")
	}
}

// An endpoint registered by hand keeps its bot: no second source for the same Home Chat or the same bot. A node that
// does not announce the calls is never asked, and discovery "off" finds nothing.
func TestOrcaBotDiscoveryKeepsHandRegistrationsAndOlderNodes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		reg  agents.BotRegistration
	}{
		{"by session", agents.BotRegistration{ID: "helper", Provider: "orca", Title: "Helper", MachineID: "machine-b", SessionID: "chat-home"}},
		{"by bot", agents.BotRegistration{ID: "helper", Provider: "orca", Title: "Helper", MachineID: "machine-b", BotID: "bot-1"}},
	} {
		h, _ := orcaBotsManager(t, true)
		h.cfg.Bots.Endpoints = []agents.BotRegistration{tc.reg}
		h.initAgents()
		h.discoverOrcaBots(ctx)
		if h.agents.Get("bot_helper") == nil || len(h.orcaBots.found) != 0 {
			t.Fatalf("%s: hand registration %v, found %v", tc.name, h.agents.Get("bot_helper") != nil, h.orcaBots.found)
		}
	}

	h, peer := orcaBotsManager(t, false)
	h.initAgents()
	h.discoverOrcaBots(ctx)
	if len(peer.calls) != 0 || len(h.orcaBots.found) != 0 {
		t.Fatalf("an older node was asked: %v", peer.calls)
	}

	h, peer = orcaBotsManager(t, true)
	h.cfg.Bots.Discovery = "off"
	h.initAgents()
	runCtx, cancel := context.WithCancel(ctx)
	cancel()
	h.orcaBotsLoop(runCtx) // returns at once: off
	if _, err := h.toolOrcaBotList(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if len(h.orcaBots.found) != 0 {
		t.Fatalf("discovery off still found %v", h.orcaBots.found)
	}
	if len(h.orcaBotTools()) != 0 {
		t.Fatal("the bot tools are listed with discovery off")
	}
	_ = peer
}

// The assistant's tools: listed only once a bot was found; orca_bot_list and orca_bot_read read the computer's bots,
// the items waiting on the user and the Home Chat's words (no tool calls); orca_bot_send goes through the bot's source,
// pinned to the Home Chat it resolved, and says the reply is not in yet. The two reads change nothing.
func TestOrcaBotToolsListReadAndSend(t *testing.T) {
	h, peer := orcaBotsManager(t, true)
	h.initAgents()
	ctx := context.Background()
	if len(h.orcaBotTools()) != 0 {
		t.Fatal("tools listed before a bot was found")
	}
	h.discoverOrcaBots(ctx)
	var names []string
	for _, tool := range h.orcaBotTools() {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "orca_bot_list,orca_bot_read,orca_bot_send" {
		t.Fatalf("tools %v", names)
	}
	_, assistantOnly := h.AgentTools(ctx, true)
	listed := 0
	for _, tool := range assistantOnly {
		if isOrcaBotTool(tool.Name) {
			listed++
		}
	}
	agentsOnly, _ := h.AgentTools(ctx, false)
	for _, tool := range agentsOnly {
		if isOrcaBotTool(tool.Name) {
			t.Fatalf("%s is offered to every agent", tool.Name)
		}
	}
	if listed != 3 {
		t.Fatalf("the assistant gets %d of the three tools", listed)
	}
	for _, n := range orcaBotToolNames {
		known, readOnly := h.ToolAccess(n)
		if !known || readOnly != (n != orcaBotSendTool) {
			t.Errorf("%s: known %v read-only %v", n, known, readOnly)
		}
	}

	res, ok := h.CallTool(ctx, "machine-a", "conv-1", orcaBotListTool, json.RawMessage(`{}`), true)
	if !ok || res.IsError {
		t.Fatalf("list %+v", res)
	}
	var list struct {
		Bots []orcaBotRow `json:"bots"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &list); err != nil || len(list.Bots) != 1 {
		t.Fatalf("list %s %v", res.Content[0].Text, err)
	}
	b := list.Bots[0]
	if b.Machine != "machine-b" || b.Bot != "bot-1" || b.Name != "Helper" || b.NeedsYou != 1 || b.HomeChat != "Home" ||
		b.Session != "bot_"+agents.DiscoveredBot("machine-b", "bot-1", "Helper").ID+":endpoint" || b.LastActive == "" {
		t.Fatalf("row %+v", b)
	}

	res, ok = h.CallTool(ctx, "machine-a", "conv-1", orcaBotReadTool, json.RawMessage(`{"bot":"helper","messages":5}`), true)
	if !ok || res.IsError {
		t.Fatalf("read %+v", res)
	}
	var read struct {
		Items    []orcaItemRow    `json:"items"`
		Messages []orcaMessageRow `json:"messages"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Items) != 1 || read.Items[0].Status != proto.OrcaItemNeedsYou || len(read.Items[0].Decisions) != 1 ||
		!strings.Contains(read.Items[0].Question, "Which first?") {
		t.Fatalf("items %+v", read.Items)
	}
	if len(read.Messages) != 2 || read.Messages[1].Text != "Done: room left." || read.Messages[0].Role != "user" {
		t.Fatalf("messages %+v", read.Messages)
	}
	if len(peer.sends) != 0 {
		t.Fatal("a read sent something")
	}

	res, ok = h.CallTool(ctx, "machine-a", "conv-1", orcaBotSendTool, json.RawMessage(`{"bot":"Helper","text":"Reply with one word."}`), true)
	if !ok || res.IsError {
		t.Fatalf("send %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, `"accepted":true`) || !strings.Contains(res.Content[0].Text, "orca_bot_read") {
		t.Fatalf("send answer %s", res.Content[0].Text)
	}
	if len(peer.sends) != 1 || peer.sends[0].BotID != "bot-1" || peer.sends[0].SessionID != "chat-home" ||
		peer.sends[0].Text != "Reply with one word." || peer.sends[0].OperationID == "" {
		t.Fatalf("sends %+v", peer.sends)
	}

	res, _ = h.CallTool(ctx, "machine-a", "conv-1", orcaBotSendTool, json.RawMessage(`{"bot":"nobody","text":"hi"}`), true)
	if !res.IsError || len(peer.sends) != 1 {
		t.Fatalf("a send to an unknown bot: %+v", res)
	}
	if _, ok := h.CallTool(ctx, "machine-a", "conv-1", orcaBotSendTool, json.RawMessage(`{}`), false); ok {
		t.Fatal("an agent's call ran the assistant's tool")
	}
}
