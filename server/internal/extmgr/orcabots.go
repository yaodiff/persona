package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Orca bots found on the computers (docs/integrations/orca.md "Bots found on a computer", #16891). Every
// orcaBotsEvery the manager asks each connected computer whose node announces nodeproto.OrcaBots for its bots
// (agentbot.roster: the node reads bots.roster and bots.chats on its local Orca socket; nothing goes through Orca Cloud)
// and keeps one Agents → Bots source per bot, bound to the bot and not to a session, so a new Home Chat is followed. A
// bot an endpoint in manager.json already registers (its bot, or its Home Chat's session) is left to that endpoint. A
// computer that does not answer keeps its bots as they were; one whose Orca is not running has none. manager.json's
// bots.discovery "off" finds none. The assistant's orca_bot_* tools read and send through the same node calls.

// orcaBotsEvery is how often the computers are asked; orcaBotsFirst is the wait after the manager starts.
var (
	orcaBotsEvery = 30 * time.Second
	orcaBotsFirst = 5 * time.Second
)

// orcaBotsTimeout bounds one computer's answer (its node reads Orca's store and each bot's chats and items).
const orcaBotsTimeout = 20 * time.Second

// The node calls.
const (
	orcaRosterMethod  = "agentbot.roster"
	orcaItemsMethod   = "agentbot.items"
	orcaHistoryMethod = "agentbot.history"
)

// orcaBotDiscovery is what the manager found: the registry the sources share (one operation book) and the sources by
// name.
type orcaBotDiscovery struct {
	mu       sync.Mutex
	registry *agents.BotRegistry
	found    map[string]foundOrcaBot
}

// foundOrcaBot is one bot found on a computer: its registration and the roster's row of it.
type foundOrcaBot struct {
	reg agents.BotRegistration
	bot proto.OrcaBot
}

func (d *orcaBotDiscovery) setRegistry(r *agents.BotRegistry) {
	d.mu.Lock()
	d.registry = r
	d.mu.Unlock()
}

// orcaBotsOn: manager.json does not switch discovery off.
func (h *Manager) orcaBotsOn() bool {
	return !strings.EqualFold(strings.TrimSpace(h.cfg.Bots.Discovery), "off")
}

// orcaBotsCapable: m's node answers agentbot.roster (it announced nodeproto.OrcaBots).
func (m *Machine) orcaBotsCapable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.Protocol > 0 && slices.Contains(m.info.Capabilities, nodeproto.OrcaBots)
}

// orcaBotsLoop finds the computers' bots until ctx ends.
func (h *Manager) orcaBotsLoop(ctx context.Context) {
	if !h.orcaBotsOn() {
		return
	}
	t := time.NewTimer(orcaBotsFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.discoverOrcaBots(ctx)
		t.Reset(orcaBotsEvery)
	}
}

// discoverOrcaBots asks every connected capable computer once, at the same time, and brings the sources in step.
func (h *Manager) discoverOrcaBots(ctx context.Context) {
	if h.agents == nil {
		return
	}
	machines := h.machinesNow()
	listed := make(map[string]bool, len(machines))
	var wg sync.WaitGroup
	for _, m := range machines {
		listed[m.cfg.ID] = true
		if !m.isOnline() || !m.orcaBotsCapable() {
			continue
		}
		wg.Add(1)
		go func(m *Machine) {
			defer wg.Done()
			roster, err := h.orcaRoster(ctx, m)
			if err != nil {
				return // not answering now: its bots stay as they were
			}
			h.keepOrcaBots(m.cfg.ID, roster)
		}(m)
	}
	wg.Wait()
	// A computer no longer in the fleet takes its bots along.
	d := &h.orcaBots
	d.mu.Lock()
	var gone []string
	for name, f := range d.found {
		if !listed[f.reg.MachineID] {
			gone = append(gone, name)
			delete(d.found, name)
		}
	}
	d.mu.Unlock()
	for _, name := range gone {
		h.agents.Remove(name)
	}
}

// orcaRoster is m's agentbot.roster.
func (h *Manager) orcaRoster(ctx context.Context, m *Machine) (*proto.OrcaBotRoster, error) {
	ctx, cancel := context.WithTimeout(ctx, orcaBotsTimeout)
	defer cancel()
	var roster proto.OrcaBotRoster
	if err := m.call(ctx, orcaRosterMethod, map[string]any{}, &roster); err != nil {
		return nil, err
	}
	return &roster, nil
}

// keepOrcaBots makes machine's sources the roster's bots: new ones added, renamed ones replaced, gone ones removed.
func (h *Manager) keepOrcaBots(machine string, roster *proto.OrcaBotRoster) {
	h.foundOrcaBots(machine, roster, true)
}

// foundOrcaBots adds the roster's bots on machine as sources (one renamed is replaced) and, when complete (the roster
// is all of the computer's bots), removes those it no longer has. Nothing while discovery is off.
func (h *Manager) foundOrcaBots(machine string, roster *proto.OrcaBotRoster, complete bool) {
	if !h.orcaBotsOn() || h.agents == nil {
		return
	}
	want := map[string]foundOrcaBot{}
	for _, b := range roster.Bots {
		if b.BotID == "" || h.registeredByHand(machine, b) {
			continue
		}
		reg := agents.DiscoveredBot(machine, string(b.BotID), b.Name)
		want["bot_"+reg.ID] = foundOrcaBot{reg: reg, bot: b}
	}
	d := &h.orcaBots
	d.mu.Lock()
	if d.found == nil {
		d.found = map[string]foundOrcaBot{}
	}
	registry := d.registry
	var remove []string
	var add []agents.BotRegistration
	for name, f := range d.found {
		if f.reg.MachineID != machine {
			continue
		}
		w, ok := want[name]
		if !ok && complete || ok && w.reg != f.reg {
			remove = append(remove, name)
			delete(d.found, name)
		}
	}
	for name, w := range want {
		if f, ok := d.found[name]; ok {
			f.bot = w.bot
			d.found[name] = f
			continue
		}
		d.found[name] = w
		add = append(add, w.reg)
	}
	d.mu.Unlock()
	for _, name := range remove {
		h.agents.Remove(name)
	}
	if registry == nil {
		return
	}
	added := false
	for _, reg := range add {
		src, err := registry.Source(reg)
		if err != nil {
			h.logf("agents: Orca bot %q on %s: %v", reg.Title, machine, err)
			continue
		}
		if h.agents.Add(src) {
			added = true
		} else {
			h.logf("agents: Orca bot %q on %s duplicates another source", reg.Title, machine)
		}
	}
	if added {
		h.agentsChanged([]string{})
	}
}

// registeredByHand: an endpoint in manager.json binds the bot on machine already, by its bot or its Home Chat's session.
func (h *Manager) registeredByHand(machine string, b proto.OrcaBot) bool {
	for _, e := range h.cfg.Bots.Endpoints {
		if e.MachineID != machine {
			continue
		}
		if e.BotID != "" && e.BotID == string(b.BotID) || e.SessionID != "" && e.SessionID == string(b.HomeSessionID) {
			return true
		}
	}
	return false
}

// ---- the assistant's tools ----

// The assistant's Orca bot tools: list the bots, read one's open items and recent conversation, send it a message.
const (
	orcaBotListTool = "orca_bot_list"
	orcaBotReadTool = "orca_bot_read"
	orcaBotSendTool = "orca_bot_send"
)

var orcaBotToolNames = []string{orcaBotListTool, orcaBotReadTool, orcaBotSendTool}

func isOrcaBotTool(name string) bool { return slices.Contains(orcaBotToolNames, name) }

// orcaBotTools are the assistant's while bots were found on a computer.
func (h *Manager) orcaBotTools() []proto.Tool {
	d := &h.orcaBots
	d.mu.Lock()
	none := len(d.found) == 0
	d.mu.Unlock()
	if none {
		return nil
	}
	out := make([]proto.Tool, 0, len(orcaBotToolNames))
	for _, n := range orcaBotToolNames {
		out = append(out, mcpTool(n))
	}
	return out
}

// orcaBotRow is one bot as the tools answer it. Session is its Agents → Bots session, where the user opens it.
type orcaBotRow struct {
	Machine     string `json:"machine"`
	MachineName string `json:"machine_name,omitempty"`
	Bot         string `json:"bot"`
	Name        string `json:"name"`
	Agent       string `json:"agent,omitempty"`
	Paused      bool   `json:"paused,omitempty"`
	Specialty   string `json:"specialty,omitempty"`
	HomeChat    string `json:"home_chat,omitempty"`
	LastActive  string `json:"last_active,omitempty"`
	NeedsYou    int    `json:"needs_you"`
	Open        int    `json:"open_items"`
	Session     string `json:"session"`
}

// callOrcaBotTool runs one of the tools for the assistant.
func (h *Manager) callOrcaBotTool(ctx context.Context, name string, args json.RawMessage) proto.ToolResult {
	var a struct {
		Machine  string `json:"machine"`
		Bot      string `json:"bot"`
		Text     string `json:"text"`
		Messages int    `json:"messages"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return proto.TextResult("invalid arguments: "+err.Error(), true)
		}
	}
	var out any
	var err error
	switch name {
	case orcaBotListTool:
		out, err = h.toolOrcaBotList(ctx, a.Machine)
	case orcaBotReadTool:
		out, err = h.toolOrcaBotRead(ctx, a.Machine, a.Bot, a.Messages)
	case orcaBotSendTool:
		out, err = h.toolOrcaBotSend(ctx, a.Machine, a.Bot, a.Text)
	default:
		return proto.TextResult("unknown tool "+name, true)
	}
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return proto.TextResult(string(b), false)
}

// toolOrcaBotList asks the connected computers (one, when machine names it) for their bots now.
func (h *Manager) toolOrcaBotList(ctx context.Context, machine string) (any, error) {
	var targets []*Machine
	if machine != "" {
		m := h.machineByName(machine)
		if m == nil {
			return nil, errors.New("no computer named " + machine)
		}
		targets = []*Machine{m}
	} else {
		targets = h.machinesNow()
	}
	rows := []orcaBotRow{}
	var unreachable []string
	for _, m := range targets {
		if !m.isOnline() || !m.orcaBotsCapable() {
			if machine != "" {
				return nil, errors.New(m.name() + " is offline or its Persona Link is too old to list Orca bots")
			}
			continue
		}
		roster, err := h.orcaRoster(ctx, m)
		if err != nil {
			unreachable = append(unreachable, m.name())
			continue
		}
		h.keepOrcaBots(m.cfg.ID, roster) // the tool's fresh answer is the sources' too
		for _, b := range roster.Bots {
			rows = append(rows, h.orcaBotRowOf(m, b))
		}
	}
	res := map[string]any{"bots": rows}
	if len(unreachable) > 0 {
		res["unreachable"] = unreachable
	}
	return res, nil
}

// orcaBotRowOf is b as the tools answer it.
func (h *Manager) orcaBotRowOf(m *Machine, b proto.OrcaBot) orcaBotRow {
	reg := agents.DiscoveredBot(m.cfg.ID, string(b.BotID), b.Name)
	session := "bot_" + reg.ID + ":" + agents.BotSessionKey
	for _, e := range h.cfg.Bots.Endpoints {
		if e.MachineID == m.cfg.ID && (e.BotID == string(b.BotID) || e.SessionID != "" && e.SessionID == string(b.HomeSessionID)) {
			session = "bot_" + e.ID + ":" + agents.BotSessionKey
		}
	}
	row := orcaBotRow{Machine: m.cfg.ID, MachineName: m.name(), Bot: string(b.BotID), Name: b.Name, Agent: b.Agent,
		Paused: b.Paused, Specialty: b.Specialty, HomeChat: b.HomeTitle, NeedsYou: b.NeedsYou, Open: b.Open, Session: session}
	if b.LastActiveMs > 0 {
		row.LastActive = time.UnixMilli(b.LastActiveMs).UTC().Format(time.RFC3339)
	}
	return row
}

// orcaBotTarget is the computer and the bot a read or a send names: the bot by its id or by its name (any case).
func (h *Manager) orcaBotTarget(ctx context.Context, machine, bot string) (*Machine, proto.OrcaBot, error) {
	bot = strings.TrimSpace(bot)
	if bot == "" {
		return nil, proto.OrcaBot{}, errors.New("bot is required: its id or name from orca_bot_list")
	}
	var targets []*Machine
	if machine != "" {
		m := h.machineByName(machine)
		if m == nil {
			return nil, proto.OrcaBot{}, errors.New("no computer named " + machine)
		}
		targets = []*Machine{m}
	} else {
		targets = h.machinesNow()
	}
	var found []*Machine
	var bots []proto.OrcaBot
	for _, m := range targets {
		if !m.isOnline() || !m.orcaBotsCapable() {
			continue
		}
		roster, err := h.orcaRoster(ctx, m)
		if err != nil {
			continue
		}
		for _, b := range roster.Bots {
			if string(b.BotID) == bot || strings.EqualFold(b.Name, bot) {
				found, bots = append(found, m), append(bots, b)
			}
		}
	}
	switch len(found) {
	case 0:
		return nil, proto.OrcaBot{}, errors.New("no Orca bot " + bot + " on a connected computer; orca_bot_list shows them")
	case 1:
		return found[0], bots[0], nil
	}
	return nil, proto.OrcaBot{}, errors.New("more than one bot is called " + bot + "; name the computer (machine) or the bot's id")
}

// orcaItemRow is one open item as orca_bot_read answers it.
type orcaItemRow struct {
	Number     int                      `json:"number,omitempty"`
	Title      string                   `json:"title"`
	Status     proto.OrcaItemStatus     `json:"status"`
	DoneMeans  string                   `json:"done_means,omitempty"`
	Chat       string                   `json:"chat,omitempty"`
	LastActive string                   `json:"last_active,omitempty"`
	Question   string                   `json:"question,omitempty"`
	Decisions  []proto.OrcaItemDecision `json:"decisions,omitempty"`
}

// orcaMessageRow is one message of the recent conversation as orca_bot_read answers it.
type orcaMessageRow struct {
	Role string `json:"role"`
	At   string `json:"at,omitempty"`
	Text string `json:"text,omitempty"`
}

// toolOrcaBotRead is the bot's open items and the last messages of its Home Chat (newest last): the user's and the
// bot's words, without its reasoning, tool calls and results.
func (h *Manager) toolOrcaBotRead(ctx context.Context, machine, bot string, messages int) (any, error) {
	m, b, err := h.orcaBotTarget(ctx, machine, bot)
	if err != nil {
		return nil, err
	}
	if messages <= 0 {
		messages = 10
	}
	messages = min(messages, 40)
	cctx, cancel := context.WithTimeout(ctx, orcaBotsTimeout)
	defer cancel()
	var items proto.OrcaBotItems
	if err := m.call(cctx, orcaItemsMethod, map[string]any{"bot_id": string(b.BotID)}, &items); err != nil {
		return nil, orcaToolErr("read the bot's items", err)
	}
	res := map[string]any{"bot": h.orcaBotRowOf(m, b)}
	rows := []orcaItemRow{}
	for _, it := range items.Items {
		row := orcaItemRow{Number: it.Number, Title: it.Title, Status: it.Status, DoneMeans: it.DoneMeans, Chat: it.ChatTitle}
		if it.LastActiveMs > 0 {
			row.LastActive = time.UnixMilli(it.LastActiveMs).UTC().Format(time.RFC3339)
		}
		if q := it.Question; q != nil {
			row.Question = strings.TrimSpace(q.Text)
			if row.Question == "" {
				row.Question = q.Gist
			}
			row.Decisions = q.Decisions
		}
		rows = append(rows, row)
	}
	res["items"] = rows
	if b.HomeSessionID == "" {
		res["messages"] = []orcaMessageRow{}
		res["note"] = "the bot has no open Home Chat"
		return res, nil
	}
	var page agents.BotPage
	if err := m.call(cctx, orcaHistoryMethod, agents.BotHistoryRequest{BotID: string(b.BotID), Limit: messages}, &page); err != nil {
		return nil, orcaToolErr("read the bot's conversation", err)
	}
	msgs := []orcaMessageRow{}
	for _, msg := range page.Messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue // the model's reasoning stays in the bot's own chat
		}
		var text []string
		for _, block := range msg.Blocks {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				text = append(text, strings.TrimSpace(block.Text))
			}
		}
		if len(text) == 0 {
			continue // tool calls and results stay in the bot's own chat
		}
		row := orcaMessageRow{Role: msg.Role, Text: strings.Join(text, "\n\n")}
		if msg.CreateTimeMs > 0 {
			row.At = time.UnixMilli(msg.CreateTimeMs).UTC().Format(time.RFC3339)
		}
		msgs = append(msgs, row)
	}
	res["messages"] = msgs
	res["working"] = page.Working
	return res, nil
}

// toolOrcaBotSend sends text to the bot's open Home Chat as the user, through the bot's Agents → Bots source (its
// operation book: a send is fixed to the chat it resolved and never follows a new Home Chat). It answers once Orca
// accepted the message, which is not the reply: orca_bot_read reads that later.
func (h *Manager) toolOrcaBotSend(ctx context.Context, machine, bot, text string) (any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text is required")
	}
	m, b, err := h.orcaBotTarget(ctx, machine, bot)
	if err != nil {
		return nil, err
	}
	if b.HomeSessionID == "" {
		return nil, errors.New(b.Name + " has no open Home Chat to send to")
	}
	h.foundOrcaBots(m.cfg.ID, &proto.OrcaBotRoster{Running: true, Bots: []proto.OrcaBot{b}}, false)
	session := h.orcaBotRowOf(m, b).Session
	if h.agents == nil || h.agents.Get(strings.TrimSuffix(session, ":"+agents.BotSessionKey)) == nil {
		return nil, errors.New(b.Name + " is not one of the Agents → Bots sources (bots.discovery is off?)")
	}
	sctx, cancel := context.WithTimeout(ctx, orcaBotsTimeout)
	defer cancel()
	sent, err := h.agents.Send(sctx, session, "", text, "assistant")
	if err != nil {
		return nil, orcaToolErr("send to the bot", err)
	}
	return map[string]any{"accepted": true, "bot": b.Name, "machine": m.name(), "home_chat": b.HomeTitle,
		"client_id": sent.Entry.ClientID,
		"next":      "the reply is not in yet: read it with orca_bot_read in a while; do not send again to retry"}, nil
}

// orcaToolErr says what failed in words the model can act on.
func orcaToolErr(what string, err error) error {
	var re *rpc.Error
	if errors.As(err, &re) {
		return errors.New("could not " + what + ": " + re.Message)
	}
	return errors.New("could not " + what + ": " + err.Error())
}
