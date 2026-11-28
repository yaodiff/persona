package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Orca's structured sessions (docs/integrations/orca.md "Structured sessions"): Orca's own chats around Claude
// Code, Codex and its other providers, with no terminal, so not the Orca host's (hostorca.go lists terminals). While
// the machine's engine is Orca the node runs a bridge for them in its own process: a runtime (runtimes.go) on one end
// of a pipe into the runtime host, Orca's agentSession.* on the other. The bridge lists every local chat (worktree.ps,
// and the chat tabs for a chat with no turn yet), a Claude chat's content being its Claude transcript here
// (runtimetranscript.go), a Codex chat a Codex thread (runtimecodex.go) and every other chat's content Orca's journal
// (orcabridge_items.go); it sends, interrupts, closes, sets modes, puts Orca's pending approvals and questions on the
// phone as cards, and keeps listing the chats closed in Orca from its book (orcabridge_book.go).

// orcaRuntimeID is the bridge's runtime id: its sessions' space kind, the Orca host's too, so a worktree's chats and
// terminals share one space.
const orcaRuntimeID = proto.EngineOrca

var (
	// orcaBridgePoll is how often the bridge reads worktree.ps (unchanged answers are a few bytes) and, for sessions
	// waiting for an answer, their pending requests.
	orcaBridgePoll = 1500 * time.Millisecond
	// orcaBridgeRestart is the wait before the bridge runs again after its connection ended.
	orcaBridgeRestart = 2 * time.Second
	// orcaHeadRetry: how long a session whose history could not be read waits before the next try.
	orcaHeadRetry = 10 * time.Second
	// orcaOptionsMaxAge: a session's options are read again at most this often, and only after it changed.
	orcaOptionsMaxAge = 10 * time.Second
	// orcaAskRetry: a card the node could not take (no hub) is offered again after this.
	orcaAskRetry = 5 * time.Second
	// orcaTabsPoll: how often the bridge reads the chat tabs, for chats with no turn yet (worktree.ps lists the rest).
	orcaTabsPoll = 5 * time.Second
)

// orcaBridgeState is the bridge the runtime host runs (setOrcaBridge).
type orcaBridgeState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// instance is the runtime.hello instance of the bridge's current connection ("" between connections): the update
	// inventory tells the built-in bridge from any other runtime by it (orcaBridgeInstance).
	instance string
}

// orcaBridgeInstance is the hello instance of the built-in Orca bridge's current connection, "" when none runs.
func (rh *RuntimeHost) orcaBridgeInstance() string {
	if rh == nil {
		return ""
	}
	rh.orca.mu.Lock()
	defer rh.orca.mu.Unlock()
	return rh.orca.instance
}

// setOrcaBridge runs the bridge (on) or stops it (off). Called whenever a hub connection says which engine the
// machine uses.
func (rh *RuntimeHost) setOrcaBridge(on bool) {
	b := &rh.orca
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == (b.cancel != nil) {
		return
	}
	if !on {
		b.cancel()
		b.cancel = nil
		done := b.done
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		rh.logger.Printf("orca bridge: stopped (the engine is not Orca any more)")
		return
	}
	base := rh.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	b.cancel, b.done = cancel, make(chan struct{})
	go rh.orcaBridgeLoop(ctx, b.done)
}

// orcaBridgeLoop runs the bridge until ctx ends, again after its connection ends.
func (rh *RuntimeHost) orcaBridgeLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	c := orcaClient()
	for ctx.Err() == nil {
		err := rh.runOrcaBridge(ctx, c)
		if ctx.Err() != nil {
			return
		}
		rh.logger.Printf("orca bridge: %v (again in %s)", err, orcaBridgeRestart)
		select {
		case <-ctx.Done():
		case <-time.After(orcaBridgeRestart):
		}
	}
}

// runOrcaBridge connects one bridge to the runtime host through a pipe and runs it until either end goes.
func (rh *RuntimeHost) runOrcaBridge(ctx context.Context, c *orcart.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hostEnd, bridgeEnd := net.Pipe()
	go func() {
		rh.serveConn(ctx, hostEnd, "")
		cancel()
	}()
	b := newOrcaBridge(c, bridgeEnd, rh.logger.Printf)
	b.onBotHomes = rh.setOrcaBotHomes
	defer rh.setOrcaBotHomes(nil) // the bridge's connection ended: nothing folds until it reads the bots again
	rh.orca.mu.Lock()
	rh.orca.instance = b.instance
	rh.orca.mu.Unlock()
	defer func() {
		rh.orca.mu.Lock()
		if rh.orca.instance == b.instance {
			rh.orca.instance = ""
		}
		rh.orca.mu.Unlock()
	}()
	return b.run(ctx, rh.version)
}

// orcaBridge is one connection of the bridge.
type orcaBridge struct {
	c    *orcart.Client
	conn io.ReadWriteCloser
	peer *rpc.Peer
	logf func(format string, args ...any)
	// instance is its runtime.hello instance (orcaBridgeState.instance).
	instance string

	mu      sync.Mutex
	treesID string
	trees   []orcart.Worktree
	orcaOf  map[string]string    // Persona session id → Orca's session id, of every chat listed (closed ones too)
	closed  map[string]bool      // Persona session ids of the chats listed closed (from the book)
	heads   map[string]*orcaHead // by Orca session id
	asks    map[string]*orcaAsk  // by orcaPromptKey
	sends   map[string]time.Time // client_msg_id → when it was first sent (its operation id's time: a retry replays)
	pushed  string               // the rows last pushed
	tabs    []orcart.Worktree    // the chats among the tabs last read (orcaTabsPoll), by worktree
	tabsAt  time.Time            // when they were read; zero: read them next refresh
	noTabs  bool                 // the host has no session.tabs.listAll (an older Orca)
	book    *orcaBook
	st      *orcart.Status // the runtime's status.get last read (orcabridge_controls.go status), and when
	stAt    time.Time
	// Starting chats (orcabridge_start.go): the agents Orca found installed and runs as chats and when, the launchers
	// last pushed, the chats Persona started that Orca does not list yet (by Orca session id), and whether the rows go
	// to the node again on the next refresh even if unchanged (a start's answer stood in for one); the providers whose
	// chat Orca refused for a missing chat runtime (noChat), and the chats open now by provider (Orca session id → its
	// row's updatedAt), which tell when to offer them again.
	agents   []string
	agentsAt time.Time
	launches string
	starts   map[string]orcaStarted
	repush   bool
	noChat   map[string]map[string]int64
	openRevs map[string]map[string]int64

	// bots is the bridge's part for Orca's bots (orcabots.go): their item cards and what folds under their Home Chats;
	// onBotHomes hears of a fold that changed (nil: nobody listens).
	bots       orcaBotsState
	onBotHomes func(*orcaBotHomes)
}

// orcaHead is what the bridge knows of one Orca session beyond its worktree.ps row.
type orcaHead struct {
	provider string // the agent's own session: Claude's session id (its transcript), Codex's thread; "" until it has one
	fence    int64
	readAt   time.Time // when the history head was read
	readRev  int64     // the row's updatedAt then
	failedAt time.Time
	opts     *orcart.Options
	optsAt   time.Time
	optsRev  int64 // the row's updatedAt when opts was read
}

// orcaAsk is one of Orca's prompts put on the phone.
type orcaAsk struct {
	orca    string
	cancel  context.CancelFunc
	retryAt time.Time // the node could not take it, or Orca did not take its answer: offer it again then
	// failed is why Orca did not take the last answer to it ("" when none failed): the card offered again says so.
	failed  string
	attempt int // how many times it was put on the phone; each time under its own card id
}

func newOrcaBridge(c *orcart.Client, conn io.ReadWriteCloser, logf func(string, ...any)) *orcaBridge {
	return &orcaBridge{c: c, conn: conn, logf: logf, instance: newUUID(), orcaOf: map[string]string{}, closed: map[string]bool{},
		heads: map[string]*orcaHead{}, asks: map[string]*orcaAsk{}, sends: map[string]time.Time{}, book: loadOrcaBook("")}
}

// run says hello and keeps the list current until the connection or ctx ends.
func (b *orcaBridge) run(ctx context.Context, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.conn.Close()
	b.book = loadOrcaBook(orcaBookPath())
	defer func() { b.book.flush(time.Now(), true) }()
	b.peer = rpc.New("o", b.conn)
	b.peer.OnRequest = b.request
	served := make(chan error, 1)
	go func() { served <- b.peer.Serve(ctx, b.conn) }()
	hello := map[string]any{"runtime": orcaRuntimeID, "name": proto.EngineNames[proto.EngineOrca], "agent": "claude",
		"version": version, "instance": b.instance,
		"caps": map[string]any{"send": true, "interrupt": true, "close": true, "modes": true, "model": true, "rename": true,
			"rewind": true, "seen": true, "start": true}}
	if err := b.peer.Call(ctx, "runtime.hello", hello, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	t := time.NewTicker(orcaBridgePoll)
	defer t.Stop()
	for {
		b.refresh(ctx)
		select {
		case err := <-served:
			if err == nil {
				err = errors.New("the runtime host closed the connection")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// refresh reads worktree.ps (and now and then the chat tabs), pushes the rows when they changed, and keeps the cards
// in step with Orca's prompts.
func (b *orcaBridge) refresh(ctx context.Context) {
	b.mu.Lock()
	after := b.treesID
	b.mu.Unlock()
	trees, id, unchanged, err := b.c.Worktrees(ctx, after)
	running := !errors.Is(err, orcart.ErrNotRunning)
	b.mu.Lock()
	switch {
	case !running:
		b.trees, b.treesID, b.tabs = nil, "", nil // Orca quit: its chats are listed closed
	case err != nil:
		b.treesID = "" // keep the last rows; ask for everything next time
	case unchanged:
	default:
		b.trees, b.treesID = trees, id
	}
	trees = b.trees
	b.mu.Unlock()
	tabs := b.chatTabs(ctx, running)
	chats := orcaOpenChats(trees, tabs)
	var st *orcart.Status
	var agents []string // the agents Orca found installed and runs as chats (orcabridge_start.go)
	if running {
		st = b.status(ctx)
		agents = b.installed(ctx)
	}
	agents = b.chatsOffered(running, agents, chats) // the providers it starts chats of: not one whose chat runtime it lacks
	launches := st.Has(orcart.CapChatLaunch)

	now := time.Now()
	rows := []map[string]any{}
	orcaOf := map[string]string{}
	waiting := map[string]string{}         // Orca session id → Persona session id, sessions with a prompt pending
	open := map[string]bool{}              // Orca session ids of the chats open in Orca
	opts := map[string][]*orcart.Options{} // the open chats' options, by provider: their launchers' models
	for _, c := range chats {
		open[c.orca] = true
		h := b.head(ctx, c.orca, c.rev)
		if h == nil {
			continue
		}
		e := b.book.note(orcaEntryOf(c, h), now)
		if orcaOf[e.Persona] != "" {
			continue // two chats on one conversation (Orca opened it again): the first listed
		}
		row := orcaRow(c, e, h)
		orcaControls(row, c.agent, h.opts, st)
		if launches && slices.Contains(agents, c.agent) && slices.Contains(orcaChatProviders, c.agent) {
			row["launcher"] = orcaLauncherPrefix + c.agent // its model picker lists Orca's models (orcaLaunchers)
		}
		if strings.HasPrefix(c.orca, orcaStartPrefix) {
			row["started_by_persona"] = true
		}
		rows = append(rows, row)
		orcaOf[e.Persona] = c.orca
		opts[c.agent] = append(opts[c.agent], h.opts)
		if row["status"] == proto.StatusBlocked {
			waiting[c.orca] = e.Persona
		}
	}
	started, startedOrca := b.startedRows(orcaOf, now)
	for i, row := range started {
		rows = append(rows, row)
		orcaOf[row["session_id"].(string)] = startedOrca[i]
	}
	b.pushLaunchers(orcaLaunchers(agents, st, opts))
	closed := map[string]bool{}
	for _, e := range b.book.closed(open, orcaOf, now) {
		rows = append(rows, orcaClosedRow(e))
		orcaOf[e.Persona] = e.Orca
		closed[e.Persona] = true
	}
	b.book.flush(now, false)
	b.mu.Lock()
	b.orcaOf, b.closed = orcaOf, closed
	if b.repush {
		b.pushed, b.repush = "", false
	}
	b.mu.Unlock()
	if raw, err := json.Marshal(rows); err == nil && string(raw) != b.pushed {
		// unlisted_transcripts: none. The bridge lists the chats Orca closed itself (orcaClosedRow), so none of their
		// Claude transcripts is left to the node as an ordinary session, which a Resume would open in a terminal.
		if b.peer.Notify("runtime.sessions", map[string]any{"sessions": rows, "unlisted_transcripts": []string{}}) == nil {
			b.pushed = string(raw)
		}
	}
	b.prompts(ctx, waiting)
	b.botsTick(ctx, chats, orcaOf, closed)
}

// chatTabs are the chat tabs, read again every orcaTabsPoll (the last ones meanwhile); none from a host without them.
func (b *orcaBridge) chatTabs(ctx context.Context, running bool) []orcart.Worktree {
	b.mu.Lock()
	tabs, due := b.tabs, running && !b.noTabs && time.Since(b.tabsAt) >= orcaTabsPoll
	b.mu.Unlock()
	if !due {
		return tabs
	}
	got, err := b.c.ChatTabs(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tabsAt = time.Now()
	switch {
	case orcart.IsCode(err, "method_not_found") || orcart.IsCode(err, "method_not_supported"):
		b.noTabs, b.tabs = true, nil
	case err == nil:
		b.tabs = got
	}
	return b.tabs
}

// orcaChat is one chat open in Orca: its worktree, and its worktree.ps row, else the row of its tab (orcart.ChatTabs).
type orcaChat struct {
	orca  string
	agent string // Orca's provider ("claude", "codex", "antigravity"…)
	tree  orcart.Worktree
	row   *orcart.AgentRow
	rev   int64 // its last change (epoch ms)
}

// orcaOpenChats are the chats open in Orca on this machine, in worktree.ps's order, then the chat tabs worktree.ps
// does not list (no turn yet). A worktree on an SSH target is that machine's node's.
func orcaOpenChats(trees, tabs []orcart.Worktree) []orcaChat {
	var out []orcaChat
	seen := map[string]bool{}
	byID := map[string]orcart.Worktree{}
	for _, w := range trees {
		if w.HostID != "" && w.HostID != "local" {
			continue
		}
		byID[w.WorktreeID] = w
		for i := range w.Agents {
			a := &w.Agents[i]
			orca, ok := orcart.SessionOf(a.PaneKey)
			if !ok || seen[orca] {
				continue
			}
			seen[orca] = true
			out = append(out, orcaChat{orca: orca, agent: firstOf(a.AgentType, "claude"), tree: w, row: a, rev: a.UpdatedAt})
		}
	}
	for _, t := range tabs {
		w, ok := byID[t.WorktreeID]
		if !ok {
			continue
		}
		for i := range t.Agents {
			a := &t.Agents[i]
			orca, ok := orcart.SessionOf(a.PaneKey)
			if !ok || seen[orca] {
				continue
			}
			seen[orca] = true
			out = append(out, orcaChat{orca: orca, agent: firstOf(a.AgentType, "claude"), tree: w, row: a, rev: a.UpdatedAt})
		}
	}
	return out
}

// orcaEntryOf is the book's entry of an open chat as the bridge sees it now (its Persona id is the book's to give).
func orcaEntryOf(c orcaChat, h *orcaHead) orcaBookEntry {
	e := orcaBookEntry{Orca: c.orca, Agent: c.agent, Provider: h.provider, Cwd: c.tree.Path, Space: c.tree.WorktreeID,
		Branch: c.tree.Branch, Parent: orcaParent(c.tree.WorktreeID, c.tree.ParentWorktreeID), Updated: c.rev}
	if a := c.row; a != nil {
		e.Title = firstOf(a.ConversationName, a.DisplayName, a.TaskTitle, firstLine(a.Prompt))
	}
	return e
}

// head is a session's provider session and fence (read from its history; again while it has no provider session and
// the row changed or orcaHeadRetry passed, and at most every orcaHeadRetry while the row changes, as a rewind or a
// cleared context moves a Claude chat to another Claude session) and options (read again after the row changed); nil
// while its history cannot be read.
func (b *orcaBridge) head(ctx context.Context, orca string, rev int64) *orcaHead {
	var h orcaHead // a copy: the stored one is replaced, never changed
	known := false
	b.mu.Lock()
	if old := b.heads[orca]; old != nil {
		h, known = *old, !old.readAt.IsZero()
	}
	b.mu.Unlock()
	changed, due := h.readRev != rev, time.Since(h.readAt) >= orcaHeadRetry
	read := !known || h.provider == "" && (changed || due) || changed && due
	if read && !known && !h.failedAt.IsZero() && time.Since(h.failedAt) < orcaHeadRetry {
		return nil
	}
	if read {
		hd, err := b.c.Head(ctx, orca)
		switch {
		case err != nil && !known:
			if ctx.Err() == nil {
				b.logf("orca bridge: history of %s: %v", orca, err)
			}
			b.mu.Lock()
			b.heads[orca] = &orcaHead{failedAt: time.Now()}
			b.mu.Unlock()
			return nil
		case err == nil:
			h.provider, h.fence = firstOf(hd.Provider, h.provider), hd.Fence
		}
		h.readAt, h.readRev = time.Now(), rev
	}
	if h.opts == nil || h.optsRev != rev && time.Since(h.optsAt) > orcaOptionsMaxAge {
		if o, err := b.c.Options(ctx, orca); err == nil {
			h.opts = o
		}
		h.optsAt, h.optsRev = time.Now(), rev
	}
	b.mu.Lock()
	b.heads[orca] = &h
	b.mu.Unlock()
	return &h
}

// orcaModes are Persona's permission modes in Orca's words (Claude Code's). Plan is not a permission mode in Orca but
// its interaction mode (orcaRowMode, setModes in orcabridge_controls.go).
var orcaModes = map[string]string{"default": "ask", "acceptEdits": "accept-edits", "auto": "auto",
	proto.ModeBypass: "full-access"}

// orcaAgentNames are what the apps call Orca's providers they do not know by their id. Claude Code and Codex they know.
var orcaAgentNames = map[string]string{"antigravity": "Antigravity", "gemini": "Gemini", "opencode": "OpenCode",
	"opencode2": "OpenCode", "omp": "Oh My Pi", "pi": "pi", "copilot": "Copilot", "cursor": "Cursor", "kimi": "Kimi",
	"kiro": "Kiro", "grok": "Grok", "hermes": "Hermes", "goose": "Goose", "kilo": "Kilo", "droid": "Droid",
	"qwen-code": "Qwen Code", "cline": "Cline", "devin": "Devin", "codebuddy": "CodeBuddy", "mimo-code": "MiMo Code",
	"aug": "Auggie"}

// orcaAgentFields are a row's agent (Persona's id where Persona knows the provider, else Orca's) and conversation:
// a Claude chat's Claude transcript, a Codex chat's Codex thread (runtimes.md "Runtimes that run Codex"), and the
// provider's name for one the apps do not know.
func orcaAgentFields(row map[string]any, agent, provider string) {
	row["agent"] = agent
	switch agent {
	case "claude":
		if validID(provider) {
			row["transcript"] = provider
		}
	case "codex":
		if provider != "" {
			row["codex_thread"] = provider
		}
	default:
		row["agent_label"] = firstOf(orcaAgentNames[agent], agent)
	}
}

// orcaRow is the runtime.sessions row of a chat open in Orca (docs/integrations/runtimes.md).
func orcaRow(c orcaChat, e orcaBookEntry, h *orcaHead) map[string]any {
	rev := c.rev
	if rev <= 0 {
		rev = e.Updated // a chat with no turn yet: when the bridge first saw it
	}
	updated := time.UnixMilli(rev).UTC()
	status := proto.StatusIdle
	if c.row != nil {
		status = orcaChatStatus(*c.row)
	}
	row := map[string]any{"session_id": e.Persona, "title": firstOf(e.Title, filepath.Base(c.tree.Path)), "cwd": c.tree.Path,
		"status": status, "updated_at": updated, "rev": rev, "space_id": c.tree.WorktreeID,
		"space_name": orcaSpaceLabel(c.tree.Branch, c.tree.Path)}
	orcaAgentFields(row, c.agent, h.provider)
	if e.Parent != "" {
		row["parent_space_id"] = e.Parent // folds under its parent worktree like the terminals' sessions (#2073)
	}
	if c.row != nil && c.row.LastAssistantMessage != "" {
		row["last_text"] = bridgePreview(c.row.LastAssistantMessage)
	}
	if o := h.opts; o != nil {
		if mode := orcaRowMode(o); mode != "" {
			row["permission_mode"] = mode
		}
		if o.Effort != "" {
			row["effort"] = o.Effort
		}
		if o.Model != "" {
			row["model"] = o.Model
		}
	}
	return row
}

// orcaClosedRow is the row of a chat closed in Orca, from the book: not running, its content read as when it was open,
// and a message to it opens it in Orca again (orcaBridge.request). Nothing else is offered on it.
func orcaClosedRow(e orcaBookEntry) map[string]any {
	updated := time.UnixMilli(max(e.Updated, e.Seen)).UTC()
	row := map[string]any{"session_id": e.Persona, "title": firstOf(e.Title, filepath.Base(e.Cwd)), "cwd": e.Cwd,
		"status": proto.StatusOffline, "updated_at": updated, "rev": max(e.Updated, e.Seen), "space_id": e.Space,
		"space_name":   orcaSpaceLabel(e.Branch, e.Cwd),
		"capabilities": map[string]any{"modes": false, "model": false, "rewind": false}}
	orcaAgentFields(row, e.Agent, e.Provider)
	if e.Parent != "" {
		row["parent_space_id"] = e.Parent
	}
	return row
}

// orcaChatStatus is a chat's status from its row (AgentRow.PaneState): its main agent's own state (the row's folds in
// its subagents), blocked while one of its subagents waits on the user.
func orcaChatStatus(a orcart.AgentRow) string {
	switch a.PaneState() {
	case "working":
		return proto.StatusWorking
	case "blocked", "waiting":
		return proto.StatusBlocked
	case "":
		return proto.StatusIdle
	}
	return proto.StatusDone
}

// orcaPromptKey names one revision of one prompt.
func orcaPromptKey(orca string, p orcart.Prompt) string {
	return fmt.Sprintf("%s/%s/%d", orca, p.ItemID, p.Revision)
}

// prompts puts the pending prompts of the waiting sessions on the phone and takes the cards of those gone (answered
// in Orca, withdrawn) off it.
func (b *orcaBridge) prompts(ctx context.Context, waiting map[string]string) {
	live := map[string]bool{}
	read := map[string]bool{}
	for orca, persona := range waiting {
		ps, err := b.c.Prompts(ctx, orca)
		if err != nil {
			if ctx.Err() == nil {
				b.logf("orca bridge: requests of %s: %v", orca, err)
			}
			continue // its cards stay as they are
		}
		read[orca] = true
		for _, p := range ps {
			key := orcaPromptKey(orca, p)
			live[key] = true
			b.mu.Lock()
			a := b.asks[key]
			due := a == nil || !a.retryAt.IsZero() && !time.Now().Before(a.retryAt)
			if due {
				next := &orcaAsk{orca: orca}
				if a != nil { // offered again: a new card, saying why when Orca did not take the last answer
					next.failed, next.attempt = a.failed, a.attempt
				}
				b.asks[key] = next
			}
			b.mu.Unlock()
			if due {
				b.ask(ctx, key, orca, persona, p)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, a := range b.asks {
		_, stillWaiting := waiting[a.orca]
		if live[key] || stillWaiting && !read[a.orca] {
			continue
		}
		if a.cancel != nil {
			a.cancel() // the goroutine takes the card off the phone
		}
		delete(b.asks, key)
	}
}

// ask puts one prompt on the phone and answers Orca with what comes back. An answer Orca did not take (it refused it,
// or it is not known whether it took it) is never dropped: the prompt, while Orca still waits on it, goes on the phone
// again as a new card, a permission card saying in Orca's words why the last answer was not taken.
func (b *orcaBridge) ask(ctx context.Context, key, orca, persona string, p orcart.Prompt) {
	ix := orcaInteraction(persona, p)
	if ix == nil {
		b.logf("orca bridge: a %s request Persona cannot show (left to Orca)", p.Body.Kind)
		return
	}
	askCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	attempt := 0
	if a := b.asks[key]; a != nil {
		a.cancel, a.retryAt = cancel, time.Time{}
		attempt = a.attempt
		if a.failed != "" && ix.Kind == proto.IxPermission {
			ix.Reason = a.failed
		}
	}
	b.mu.Unlock()
	ix.ID = orcaCardID(key, attempt)
	go func() {
		defer cancel()
		var res proto.Resolution
		err := b.peer.Call(askCtx, "interaction.create", map[string]any{"interaction": ix}, &res)
		if askCtx.Err() != nil {
			if ctx.Err() == nil {
				_ = b.peer.Notify("interaction.cancel", map[string]any{"id": ix.ID})
			}
			return
		}
		if err != nil {
			b.mu.Lock()
			if a := b.asks[key]; a != nil {
				a.retryAt = time.Now().Add(orcaAskRetry)
			}
			b.mu.Unlock()
			return
		}
		if res.Dismiss {
			return // handed back to the computer: Orca's own window answers it
		}
		from := ""
		b.mu.Lock()
		if head := b.heads[orca]; head != nil {
			from = orcaRowMode(head.opts)
		}
		b.mu.Unlock()
		if err := b.answer(ctx, orca, p, res); err != nil {
			b.logf("orca bridge: answering %s in %s: %v (offered again while Orca waits on it)", p.Body.Kind, orca, err)
			var oe *orcart.Error
			why := ""
			if errors.As(err, &oe) {
				why = strings.TrimSpace(oe.Message)
			}
			b.mu.Lock()
			if a := b.asks[key]; a != nil {
				a.failed, a.attempt, a.retryAt = why, a.attempt+1, time.Now()
			}
			b.mu.Unlock()
		} else if ix.Kind == proto.IxPlan && res.Approve != nil && *res.Approve && orcaModes[res.Mode] != "" && res.ModeActor != nil {
			_ = b.peer.Notify(proto.EventModeSet, proto.ModeSetEvent{SessionID: persona, From: from, To: res.Mode, Actor: res.ModeActor})
		}
	}()
}

// orcaCardID is the card id of a prompt's attempt-th time on the phone: the first one's is the prompt's alone (the
// same id after a node restart, so the hub's card stays one card), a later one's its own.
func orcaCardID(key string, attempt int) string {
	if attempt > 0 {
		key = fmt.Sprintf("%s#%d", key, attempt)
	}
	sum := sha256.Sum256([]byte(key))
	return "orca_" + hex.EncodeToString(sum[:8])
}

// orcaTitleTool reads the tool out of an approval's title ("Allow Bash?").
func orcaTitleTool(title string) string {
	t, ok := strings.CutPrefix(title, "Allow ")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(t), "?")
}

// orcaInteraction is the card of one of Orca's prompts; nil for one Persona has no card for.
func orcaInteraction(persona string, p orcart.Prompt) *proto.Interaction {
	body := p.Body
	ix := &proto.Interaction{SessionID: persona, AgentID: p.AgentID}
	switch body.Kind {
	case "approval":
		if body.Subject != nil && body.Subject.Kind == "plan" {
			ix.Kind, ix.Plan = proto.IxPlan, body.Subject.Text
			return ix
		}
		ix.Kind = proto.IxPermission
		ix.ToolName = orcaTitleTool(body.Title)
		if body.MatchedAskRule != nil && body.MatchedAskRule.ToolName != "" {
			ix.ToolName = body.MatchedAskRule.ToolName
		}
		if body.Detail != nil {
			ix.Summary = *body.Detail
			// Claude's tool calls come as their input in JSON: the card shows it as a hook's card does.
			var input map[string]any
			if d := strings.TrimSpace(*body.Detail); strings.HasPrefix(d, "{") && json.Unmarshal([]byte(d), &input) == nil {
				ix.ToolInput = json.RawMessage(d)
				ix.Summary = toolSummary(ix.ToolName, ix.ToolInput)
			}
		}
		if ix.Summary == "" {
			ix.Summary = firstOf(body.Description, body.Title)
		}
		ix.Reason = body.DecisionReason
		return ix
	case "question":
		for _, q := range orcaQuestions(p) {
			pq := proto.Question{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
			for _, o := range q.Options {
				pq.Options = append(pq.Options, proto.QuestionOption{Label: o.Label, Description: o.Description})
			}
			ix.Questions = append(ix.Questions, pq)
		}
		if len(ix.Questions) == 0 {
			return nil
		}
		ix.Kind = proto.IxQuestion
		return ix
	}
	return nil
}

// orcaQuestions are a question prompt's questions; an older one with a single question and no ids is one of them.
func orcaQuestions(p orcart.Prompt) []orcart.PromptQuestion {
	if len(p.Body.Questions) > 0 {
		return p.Body.Questions
	}
	if p.Body.Question == "" {
		return nil
	}
	return []orcart.PromptQuestion{{Question: p.Body.Question, Options: p.Body.Options}}
}

// orcaOption is the id of the first of the prompt's options named in ids ("" when it has none of them).
func orcaOption(p orcart.Prompt, ids ...string) string {
	for _, id := range ids {
		for _, o := range p.Body.Options {
			if o.ID == id {
				return id
			}
		}
	}
	return ""
}

var (
	orcaAllow  = []string{"allow", "accept", "yes"}
	orcaAlways = []string{"allowForSession", "acceptForSession"}
	orcaDeny   = []string{"deny", "decline", "no", "cancel"}
)

// answer gives Orca the phone's answer to a prompt: the option picked, the answers written; a refusal's words go into
// the conversation after it (Orca's approvals take no message).
func (b *orcaBridge) answer(ctx context.Context, orca string, p orcart.Prompt, res proto.Resolution) error {
	w := b.write(orca, "")
	switch p.Body.Kind {
	case "approval":
		plan := p.Body.Subject != nil && p.Body.Subject.Kind == "plan"
		allow := res.Allow
		if plan || allow == nil {
			allow = res.Approve
		}
		if allow == nil {
			return nil
		}
		var option string
		switch {
		case !*allow:
			option = orcaOption(p, orcaDeny...)
		case res.Always:
			option = orcaOption(p, append(append([]string{}, orcaAlways...), orcaAllow...)...)
		default:
			option = orcaOption(p, orcaAllow...)
		}
		if option == "" {
			return fmt.Errorf("no option to answer with among %d", len(p.Body.Options))
		}
		if err := b.c.Approve(ctx, w, p, option); err != nil {
			return err
		}
		if *allow && plan && orcaModes[res.Mode] != "" {
			return b.c.SetOption(ctx, b.write(orca, ""), "permissionMode", orcaModes[res.Mode])
		}
		if words := strings.TrimSpace(firstOf(res.Message, res.Feedback)); !*allow && words != "" {
			return b.c.SendSession(ctx, b.write(orca, ""), words, nil)
		}
		return nil
	case "question":
		qs := orcaQuestions(p)
		var answers []orcart.Answer
		legacy := ""
		for _, q := range qs {
			text := strings.TrimSpace(res.Answers[q.Question])
			if text == "" {
				continue
			}
			ids, other := orcaPicked(q, text)
			if q.ID == "" { // an older prompt: one option id, no written answer
				if len(ids) > 0 {
					legacy = ids[0]
				}
				continue
			}
			answers = append(answers, orcart.Answer{QuestionID: q.ID, OptionIDs: ids, Other: other})
		}
		// Every answer goes: Orca 1.4.277 takes at most four in one reply, and refuses more (nothing is answered then,
		// and the card comes back) rather than Persona answering some questions and dropping the rest.
		if len(answers) == 0 && legacy == "" {
			return errors.New("no answer Orca can take")
		}
		if answers == nil {
			return b.c.AnswerQuestion(ctx, w, p, nil, legacy)
		}
		for i := range answers {
			if answers[i].OptionIDs == nil {
				answers[i].OptionIDs = []string{}
			}
		}
		return b.c.AnswerQuestion(ctx, w, p, answers, "")
	}
	return nil
}

// orcaPicked is an answer from the phone as a question's options (the labels picked, a multi-select's joined with
// ", ") and the words that are none of them.
func orcaPicked(q orcart.PromptQuestion, text string) (ids []string, other string) {
	byLabel := map[string]string{}
	for _, o := range q.Options {
		byLabel[o.Label] = o.ID
	}
	if id, ok := byLabel[text]; ok {
		return []string{id}, ""
	}
	if !q.MultiSelect {
		return nil, text
	}
	var rest []string
	for _, part := range strings.Split(text, ", ") {
		if id, ok := byLabel[strings.TrimSpace(part)]; ok {
			ids = append(ids, id)
		} else if part = strings.TrimSpace(part); part != "" {
			rest = append(rest, part)
		}
	}
	return ids, strings.Join(rest, ", ")
}

// write is the envelope of a write to an Orca session: its fence and an operation id (the same one for the same
// key, so a retried send replays; key "" is a fresh one).
func (b *orcaBridge) write(orca, key string) orcart.Write {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := orcart.Write{SessionID: orca}
	if h := b.heads[orca]; h != nil {
		w.Fence = h.fence
	}
	at := time.Now()
	if key != "" {
		if first, ok := b.sends[key]; ok && time.Since(first) < 12*time.Hour {
			at = first
		} else {
			b.sends[key] = at
			if len(b.sends) > 500 {
				for k, t := range b.sends {
					if time.Since(t) > time.Hour {
						delete(b.sends, k)
					}
				}
			}
		}
	}
	w.OperationID = orcart.OperationID(at, key)
	return w
}

// request answers the node's calls about the bridge's sessions.
func (b *orcaBridge) request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID      string        `json:"session_id"`
		Text           string        `json:"text"`
		Images         []proto.Image `json:"images"`
		ClientMsgID    string        `json:"client_msg_id"`
		PermissionMode string        `json:"permission_mode"`
		Effort         string        `json:"effort"`
		orcaItemsParams
		orcaControlParams
	}
	_ = json.Unmarshal(params, &p)
	ok := map[string]any{"ok": true}
	switch method {
	case "session.watch", "session.unwatch":
		return ok, nil // a transcript is the node's to follow; a journal's rows come with the row's rev
	case "session.start": // a new chat: no Orca session yet (orcabridge_start.go)
		return b.start(ctx, params)
	}
	b.mu.Lock()
	orca, closed := b.orcaOf[p.SessionID], b.closed[p.SessionID]
	b.mu.Unlock()
	// Not one of Orca's chats: say so, also to session.items, which is how the node asks who owns a session it does
	// not know yet (runtimeFor). An empty page here claimed every such session for good, a new assistant conversation
	// whose transcript was not on disk yet included, and its next message was refused with the line below.
	if orca == "" {
		return nil, orcaChatErr("session_gone", "orca_session_gone", nil, "")
	}
	var err error
	switch method {
	case "session.items": // a chat whose content is not a transcript here, or a Claude chat's before its file is written
		return b.items(ctx, orca, p.orcaItemsParams)
	case "session.send":
		if closed { // closed in Orca: opened there again first, so the message goes to the chat and not to a terminal
			if err = b.reopen(ctx, orca); err != nil {
				break
			}
		}
		var paths []string
		if paths, err = orcaImages(p.Images); err == nil {
			key := p.ClientMsgID
			if key != "" {
				key = orca + "/" + key
			}
			err = b.c.SendSession(ctx, b.write(orca, key), p.Text, paths)
		}
	case "session.interrupt":
		if closed {
			return ok, nil // nothing runs in a closed chat
		}
		err = b.c.CancelSession(ctx, b.write(orca, ""))
	case "session.close":
		if closed { // already closed in Orca: closing it in Persona takes it off the list
			b.book.forget(orca)
			return ok, nil
		}
		err = b.c.CloseSession(ctx, orca)
	case "session.modes":
		if closed {
			return nil, orcaChatErr("unsupported", "orca_closed", nil, "")
		}
		agent := "claude"
		if e, ok := b.book.entry(orca); ok {
			agent = e.Agent
		}
		err = b.setModes(ctx, orca, agent, p.PermissionMode, p.Effort, p.Model)
	case "session.rename":
		if strings.TrimSpace(p.Title) == "" {
			return ok, nil
		}
		err = b.rename(ctx, orca, strings.TrimSpace(p.Title))
	case "session.rewind":
		if closed {
			return nil, rewindError(rewindBusy, "Send this chat a message to open it in Orca again before rewinding it.")
		}
		return b.rewind(ctx, orca, p.Prompt, p.Occurrence, p.PromptsAfter, p.Restore)
	case "session.seen":
		if closed {
			return ok, nil
		}
		err = b.seen(ctx, orca)
	default:
		// The words stay what they were; the hub words it by its reason (apierr's bridgeReasonKeys, #17057).
		return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: " + method, Data: json.RawMessage(`{"reason":"orca_unsupported"}`)}
	}
	if err != nil {
		return nil, orcaWriteErr(err)
	}
	return ok, nil
}

// reopen opens a chat closed in Orca again (agentSession.reveal), and reads its head, so a write names its fence. One
// Orca no longer has leaves the book.
func (b *orcaBridge) reopen(ctx context.Context, orca string) error {
	if err := b.c.Reveal(ctx, orca); err != nil {
		var e *orcart.Error
		if errors.As(err, &e) && strings.HasPrefix(e.Code, "agent_session_") && e.Code != "agent_session_operation_unknown" {
			b.book.forget(orca)
			return orcaChatErr("session_gone", "orca_session_gone", map[string]any{"delivered": "none"}, "")
		}
		return err
	}
	if hd, err := b.c.Head(ctx, orca); err == nil {
		b.mu.Lock()
		h := orcaHead{}
		if old := b.heads[orca]; old != nil {
			h = *old
		}
		h.fence, h.provider = hd.Fence, firstOf(hd.Provider, h.provider)
		b.heads[orca] = &h
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.tabsAt = time.Time{} // the next refresh reads the tabs again and lists the chat open
	b.mu.Unlock()
	return nil
}

// orcaChatErr is one of the bridge's refusals: the code the node and the apps act on, a reason the hub words in the
// user's language (apierr, rpc-errors.<reason>), and the node's own words for an older hub. detail is Orca's own words,
// shown beside.
func orcaChatErr(code, reason string, data map[string]any, detail string) *rpc.Error {
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = reason
	if detail != "" {
		data["detail"] = detail
	}
	raw, _ := json.Marshal(data)
	args := i18n.Args{"detail": detail}
	if program, _ := data["program"].(string); program != "" {
		args["program"] = program // the provider its words name (orca_chat_runtime_missing), as the hub's do
	}
	return &rpc.Error{Code: code, Message: i18n.T(i18n.Default, "rpc-errors."+reason, args), Data: raw}
}

// orcaWriteErr is a failed write in the user's words: Orca is not running, turned it down (nothing happened), or it is
// not known whether it happened.
func orcaWriteErr(err error) error {
	var e *orcart.Error
	var re *rpc.Error
	switch {
	case errors.As(err, &re):
		return err // the bridge's own
	case errors.Is(err, orcart.ErrNotRunning):
		return orcaChatErr("runtime_away", "orca_away", map[string]any{"delivered": "none"}, "")
	case errors.As(err, &e) && orcart.Refused(err):
		return orcaChatErr("refused", "orca_refused", map[string]any{"delivered": "none"}, firstOf(e.Message, e.Code))
	case errors.As(err, &e) && e.Code == "agent_session_operation_unknown":
		return orcaChatErr("unknown", "orca_unknown", map[string]any{"delivered": "unknown"}, "")
	}
	return err
}

// orcaImageKeep is how long an image written for Orca stays: it only has to last until Orca has read it.
const orcaImageKeep = 24 * time.Hour

// orcaImages writes the phone's images out as files for Orca (Orca takes images as files on this machine), named by
// their content, in a folder of this user only; images older than orcaImageKeep go first.
func orcaImages(images []proto.Image) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}
	dir, err := privateCacheDir("orca-images")
	if err != nil {
		return nil, fmt.Errorf("image: %w", err)
	}
	pruneOlder(dir, orcaImageKeep)
	var paths []string
	for _, img := range images {
		data := img.Data
		if _, after, ok := strings.Cut(data, ";base64,"); ok {
			data = after
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("image: %w", err)
		}
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[img.Mime]
		if ext == "" {
			ext = ".png"
		}
		sum := sha256.Sum256(raw)
		p := filepath.Join(dir, hex.EncodeToString(sum[:12])+ext)
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}
