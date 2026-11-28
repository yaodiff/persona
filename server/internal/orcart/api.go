package orcart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
)

// Terminal is one terminal of terminal.list (the fields Persona reads).
type Terminal struct {
	// Handle is the runtime's id for the terminal ("term_<uuid>"), what terminal.read / send / close take.
	Handle string `json:"handle"`
	// IncarnationID names the process behind the handle; a call fenced with it (Target) fails instead of reaching a
	// new process that took the same handle. "" from hosts that do not know it.
	IncarnationID string `json:"incarnationId"`
	// TabID and LeafID place it: a tab of a worktree, split into leaves (panes). PaneKey joins them.
	TabID        string `json:"tabId"`
	LeafID       string `json:"leafId"`
	WorktreeID   string `json:"worktreeId"`
	WorktreePath string `json:"worktreePath"`
	Branch       string `json:"branch"`
	Title        string `json:"title"`
	Connected    bool   `json:"connected"`
	Writable     bool   `json:"writable"`
	Orphaned     bool   `json:"orphaned"`
	Verdict      string `json:"verdict"`
	// ExecutionHostID is where the terminal runs: "local" for this machine (others are SSH targets).
	ExecutionHostID string `json:"executionHostId"`
	// AgentIdentity is the agent Orca sees in the terminal ("claude", "codex"…), "" for none.
	AgentIdentity string `json:"agentIdentity"`
	LastOutputAt  int64  `json:"lastOutputAt"`
	// ProcessID is the local PTY root from terminal.create's receipt, not a
	// field older runtimes necessarily include. AcceptedCreateEnv is client-side.
	ProcessID         int  `json:"processId"`
	AcceptedCreateEnv bool `json:"-"`
}

// PaneKey is the terminal's pane as Orca names it in worktree.ps and in the processes it runs (ORCA_PANE_KEY).
func (t Terminal) PaneKey() string { return t.TabID + ":" + t.LeafID }

// Local: the terminal runs on this machine.
func (t Terminal) Local() bool { return t.ExecutionHostID == "" || t.ExecutionHostID == "local" }

// IsLocal: alias for Local.
func (t Terminal) IsLocal() bool { return t.Local() }

// Target is the terminal a call acts on: its handle, fenced to the process it had when listed.
func (t Terminal) Target() Target { return Target{Handle: t.Handle, Incarnation: t.IncarnationID} }

// Target is a terminal handle and the incarnation it must still have.
type Target struct {
	Handle      string
	Incarnation string
}

func (t Target) params() map[string]any {
	p := map[string]any{"terminal": t.Handle}
	if t.Incarnation != "" {
		p["expectedIncarnationId"] = t.Incarnation
	}
	return p
}

// Tab is one tab as Orca's layout shows it.
type Tab struct {
	ID         string
	Title      string
	WorktreeID string
	Active     bool
	FocusKnown bool
	PaneCount  int
}

// TerminalList is terminal.list: the terminals, the tabs of each worktree's layout in order (only when asked for),
// and each worktree's layout revision (it changes when tabs or panes are added, moved, closed).
type TerminalList struct {
	Terminals []Terminal
	Tabs      []Tab
	// HasTabs: the layouts were asked for (Tabs is complete, possibly empty).
	HasTabs           bool
	TopologyRevisions map[string]int64
	// Truncated: Orca left terminals out (more than its list limit).
	Truncated bool
	// InventoryIncomplete: the list cannot prove a terminal absent, including
	// runtimes that omit the truncation flag.
	InventoryIncomplete bool
}

// Terminals is terminal.list, with the layouts (costly for Orca to build) only when layouts is true.
func (c *Client) Terminals(ctx context.Context, layouts bool) (*TerminalList, error) {
	return c.terminals(ctx, layouts, false)
}

// CloseInventory bypasses the runtime's cached PTY liveness for a close decision.
func (c *Client) CloseInventory(ctx context.Context, layouts bool) (*TerminalList, error) {
	return c.terminals(ctx, layouts, true)
}

func (c *Client) terminals(ctx context.Context, layouts, fresh bool) (*TerminalList, error) {
	var res struct {
		Terminals         []Terminal       `json:"terminals"`
		Truncated         *bool            `json:"truncated"`
		TopologyRevisions map[string]int64 `json:"topologyRevisions"`
		VisualLayouts     []struct {
			WorktreeID string     `json:"worktreeId"`
			Root       layoutNode `json:"root"`
		} `json:"visualLayouts"`
	}
	params := map[string]any{"includeVisualLayouts": layouts, "includePreview": false}
	if fresh {
		params["requireFreshPtyLiveness"] = true
	}
	if err := c.Call(ctx, ListTimeout, "terminal.list", params, &res); err != nil {
		return nil, err
	}
	truncated := res.Truncated != nil && *res.Truncated
	out := &TerminalList{Terminals: res.Terminals, HasTabs: layouts, TopologyRevisions: res.TopologyRevisions,
		Truncated: truncated, InventoryIncomplete: res.Terminals == nil || res.Truncated == nil || truncated}
	for _, l := range res.VisualLayouts {
		l.Root.tabs(l.WorktreeID, &out.Tabs)
	}
	return out, nil
}

// layoutNode is a node of a worktree's layout: a group of tabs, or a split of two nodes (first, then second).
type layoutNode struct {
	Type        string `json:"type"`
	ActiveTabID string `json:"activeTabId"`
	Tabs        []struct {
		TabID string      `json:"tabId"`
		Title string      `json:"title"`
		Panes *layoutNode `json:"panes"`
	} `json:"tabs"`
	First  *layoutNode `json:"first"`
	Second *layoutNode `json:"second"`
}

// tabs appends the node's tabs in reading order: a group's own, a split's first side, then its second.
func (n *layoutNode) tabs(worktree string, out *[]Tab) {
	if n == nil {
		return
	}
	focusKnown := false
	for _, t := range n.Tabs {
		focusKnown = focusKnown || n.ActiveTabID != "" && t.TabID == n.ActiveTabID
	}
	for _, t := range n.Tabs {
		if t.TabID != "" {
			*out = append(*out, Tab{ID: t.TabID, Title: t.Title, WorktreeID: worktree,
				Active: t.TabID == n.ActiveTabID, FocusKnown: focusKnown, PaneCount: t.Panes.paneCount()})
		}
	}
	n.First.tabs(worktree, out)
	n.Second.tabs(worktree, out)
}

// paneCount is zero when layout did not supply a complete terminal pane tree.
func (n *layoutNode) paneCount() int {
	if n == nil {
		return 0
	}
	switch n.Type {
	case "terminal":
		return 1
	case "pane-split":
		first, second := n.First.paneCount(), n.Second.paneCount()
		if first > 0 && second > 0 {
			return first + second
		}
	}
	return 0
}

// Worktree is one worktree of worktree.ps.
type Worktree struct {
	ChildWorktreeIDs []string `json:"childWorktreeIds"`
	WorkspaceLineage *struct {
		ParentWorkspaceKey string `json:"parentWorkspaceKey"`
	} `json:"workspaceLineage"`
	InstanceID    string `json:"worktreeInstanceId"`
	Removing      bool   `json:"removing"`
	WorktreeID    string `json:"worktreeId"`
	HostID        string `json:"hostId"`
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	DisplayName   string `json:"displayName"`
	Repo          string `json:"repo"`
	RepoID        string `json:"repoId"`
	IsMain        *bool  `json:"isMainWorktree"`
	Unread        *bool  `json:"unread"`
	SidebarStatus string `json:"sidebarStatus"`
	WorkspaceKind string `json:"workspaceKind"`
	// Active: the worktree Orca's window is showing. Orca's UI adopts a new terminal only there; one made in another
	// worktree becomes an orphaned background terminal under a new pane key (#5547).
	Active bool `json:"isActive"`
	// ParentWorktreeID is the worktree this one is a child of in Orca's sidebar (its "N children"); empty for a top-level one.
	ParentWorktreeID string `json:"parentWorktreeId"`
	// Agents are the agents Orca's own hooks reported in the worktree's panes.
	Agents []AgentRow `json:"agents"`
}

// WorktreeHeads reads Git identity in one catalog request, without inspecting terminals.
// Old runtimes may not provide this optional display metadata.
func (c *Client) WorktreeHeads(ctx context.Context) (map[string]string, error) {
	var res struct {
		Worktrees []struct {
			ID   string `json:"id"`
			Head string `json:"head"`
		} `json:"worktrees"`
	}
	if err := c.Call(ctx, ListTimeout, "worktree.list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(res.Worktrees))
	for _, w := range res.Worktrees {
		out[w.ID] = w.Head
	}
	return out, nil
}

// AgentRow is one agent of worktree.ps: its pane, kind and state from Orca's hooks.
type AgentRow struct {
	PaneKey   string `json:"paneKey"`
	AgentType string `json:"agentType"`
	// State: working | blocked | waiting | done, folding in the agent's children (a settled agent with live subagents
	// reads working); MainAgent.State is the agent's own, when Orca has it.
	State     string `json:"state"`
	MainAgent *struct {
		State string `json:"state"`
	} `json:"mainAgent"`
	HasLivePty bool `json:"hasLivePty"`
	// ConversationName, DisplayName and TaskTitle name the agent's conversation (Orca's sidebar: the first one set);
	// Prompt is the first message, LastAssistantMessage its latest answer.
	ConversationName     string `json:"conversationName"`
	DisplayName          string `json:"displayName"`
	TaskTitle            string `json:"taskTitle"`
	Prompt               string `json:"prompt"`
	LastAssistantMessage string `json:"lastAssistantMessage"`
	// UpdatedAt and StateStartedAt are epoch milliseconds.
	UpdatedAt      int64 `json:"updatedAt"`
	StateStartedAt int64 `json:"stateStartedAt"`
}

// OwnState is the pane's agent's own state: its main agent's when known, else the row's (Orca's mainAgentState).
func (a AgentRow) OwnState() string {
	if a.MainAgent != nil && a.MainAgent.State != "" {
		return a.MainAgent.State
	}
	return a.State
}

// PaneState is the state the pane's row is listed with: blocked (or waiting) when the row's state says so, as one of
// its agents — the main one or a subagent — waits on the user then; else the main agent's own (OwnState), so a settled
// agent whose background subagents still run reads done, not working.
func (a AgentRow) PaneState() string {
	if a.State == "blocked" || a.State == "waiting" {
		return a.State
	}
	return a.OwnState()
}

// Worktrees is worktree.ps. after is the snapshot id of the last answer ("" for none): when nothing changed since,
// Orca says so (unchanged true, no worktrees) instead of sending them all again. snapshot is the id to pass next time
// ("" from hosts without snapshots).
func (c *Client) Worktrees(ctx context.Context, after string) (trees []Worktree, snapshot string, unchanged bool, err error) {
	var res struct {
		Worktrees  []Worktree `json:"worktrees"`
		SnapshotID string     `json:"snapshotId"`
		Unchanged  bool       `json:"unchanged"`
	}
	var a any // null: the first poll of a snapshot series
	if after != "" {
		a = after
	}
	err = c.Call(ctx, ListTimeout, "worktree.ps", map[string]any{"afterSnapshotId": a}, &res)
	return res.Worktrees, res.SnapshotID, res.Unchanged && after != "", err
}

// ErrPartial: Orca answered a list with only part of it (more than its limit).
var ErrPartial = errors.New("orca answered with part of the list")

// AllWorktrees is worktree.ps answered whole, outside a snapshot series (Worktrees); ErrPartial when Orca left
// worktrees out.
func (c *Client) AllWorktrees(ctx context.Context) ([]Worktree, error) {
	var res struct {
		Worktrees []Worktree `json:"worktrees"`
		Truncated bool       `json:"truncated"`
	}
	if err := c.Call(ctx, ListTimeout, "worktree.ps", map[string]any{"afterSnapshotId": nil}, &res); err != nil {
		return nil, err
	}
	if res.Truncated {
		return nil, ErrPartial
	}
	return res.Worktrees, nil
}

// Screen is terminal.read with screen: the visible screen as plain text.
type Screen struct {
	Lines []string
	// Wrapped[i]: line i continues on the next one (a soft wrap).
	Wrapped []bool
	// Source is what was read ("screen"; "stream" is the output's tail, not the screen).
	Source string
	// Draft is the text in an agent's input box, which Orca lifts out of the rows: it leaves the prompt's row as its
	// bare glyph (❯ › ») and drops the rows the text wrapped onto (Orca 1.4.277; older runtimes leave the rows as
	// drawn and send no draft). Without colors it is whatever the box shows: typed text, and also Claude Code's grey
	// suggested next message (checked live on 1.4.277 with Claude Code 2.1.331). Empty: the box shows no text, or
	// only a placeholder Orca knows (Claude's `Try "…"` tip, Codex's).
	Draft string
}

// Text is the screen as text, soft-wrapped rows joined, one line per row, ending in a newline. The box's text Orca
// lifted out (Draft) is put back on the prompt's row, its further lines under it indented as Claude Code draws them:
// a reader of the text sees the box as it is on screen.
func (s Screen) Text() string {
	var b strings.Builder
	at := s.draftRow()
	for i, l := range s.Lines {
		if i == at {
			lines := strings.Split(s.Draft, "\n")
			b.WriteString(l + " " + lines[0])
			for _, r := range lines[1:] {
				b.WriteString("\n  " + r)
			}
		} else {
			b.WriteString(l)
		}
		if i < len(s.Wrapped) && s.Wrapped[i] && i+1 < len(s.Lines) {
			continue
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// draftRow is the row Orca left the box's prompt glyph on alone (its own lvo does the same: the last such row), -1
// without a draft or such a row.
func (s Screen) draftRow() int {
	if s.Draft == "" {
		return -1
	}
	for i := len(s.Lines) - 1; i >= 0; i-- {
		switch strings.TrimSpace(s.Lines[i]) {
		case "❯", "›", "»":
			return i
		}
	}
	return -1
}

// ErrNoScreen: Orca has no screen for the terminal right now (terminal.read's source "screen-unavailable").
var ErrNoScreen = errors.New("orca has no screen for the terminal")

// Read is terminal.read {screen: true}: the terminal's visible screen, plain text.
func (c *Client) Read(ctx context.Context, t Target) (*Screen, error) {
	var res struct {
		Terminal struct {
			Tail     []string `json:"tail"`
			TailRows []struct {
				Wrapped bool `json:"wrapped"`
			} `json:"tailRows"`
			Source string `json:"source"`
			Draft  string `json:"draft"`
		} `json:"terminal"`
	}
	p := t.params()
	p["screen"] = true
	if err := c.Call(ctx, CallTimeout, "terminal.read", p, &res); err != nil {
		return nil, err
	}
	if res.Terminal.Source == "screen-unavailable" {
		return nil, fmt.Errorf("orca terminal.read: %w", ErrNoScreen)
	}
	s := &Screen{Lines: res.Terminal.Tail, Source: res.Terminal.Source, Draft: res.Terminal.Draft}
	for _, r := range res.Terminal.TailRows {
		s.Wrapped = append(s.Wrapped, r.Wrapped)
	}
	return s, nil
}

// TailLimit is the most lines one tail or log read returns.
const TailLimit = 2000

// ReadTail is terminal.read {limit}: the last limit lines (at most TailLimit) of the terminal's output tail, which is
// the newest lines its program wrote, escapes stripped, rows it redrew in place kept as they ended, the line it is
// still writing last. truncated: Orca let older output go (its tail keeps at most 2000 lines and 256 KiB). limited:
// it keeps more than the lines sent (one read sends at most 2000 lines and 32 KiB; the first line may then be the end
// of a longer one). source is "stream" for the tail, "screen" when Orca answered with the screen instead (a program on
// the alternate screen: nothing is above it).
func (c *Client) ReadTail(ctx context.Context, t Target, limit int) (lines []string, truncated, limited bool, source string, err error) {
	var res struct {
		Terminal struct {
			Tail      []string `json:"tail"`
			Truncated bool     `json:"truncated"`
			Limited   bool     `json:"limited"`
			Source    string   `json:"source"`
		} `json:"terminal"`
	}
	p := t.params()
	p["limit"] = min(max(limit, 1), TailLimit)
	if err := c.Call(ctx, CallTimeout, "terminal.read", p, &res); err != nil {
		return nil, false, false, "", err
	}
	r := res.Terminal
	if r.Source == "screen-unavailable" {
		return nil, false, false, "", fmt.Errorf("orca terminal.read: %w", ErrNoScreen)
	}
	return r.Tail, r.Truncated, r.Limited, r.Source, nil
}

// ErrNoCursor: this Orca reads no output by cursor (an older one answers a cursor read without cursors).
var ErrNoCursor = errors.New("orca reads no output by cursor")

// ReadLog is terminal.read {cursor, limit}: up to limit lines (at most TailLimit) of the terminal's log from cursor
// on, starting at the oldest line Orca keeps when cursor is older; oldest is that line's cursor (0: Orca let none go).
// The log is every line the terminal's program completed, in order, escapes stripped: rows a program redrew in place
// come again each time (a spinner, an agent's input box), so it is a record of the output, not what the terminal
// shows (ReadTail is that). A cursor counts the completed lines since Orca began keeping the terminal's output.
func (c *Client) ReadLog(ctx context.Context, t Target, cursor int64, limit int) (lines []string, oldest int64, err error) {
	var res struct {
		Terminal struct {
			Tail   []string     `json:"tail"`
			Oldest outputCursor `json:"oldestCursor"`
			Latest outputCursor `json:"latestCursor"`
		} `json:"terminal"`
	}
	p := t.params()
	p["cursor"] = max(cursor, 0)
	p["limit"] = min(max(limit, 1), TailLimit)
	if err := c.Call(ctx, CallTimeout, "terminal.read", p, &res); err != nil {
		return nil, 0, err
	}
	r := res.Terminal
	if !r.Oldest.ok || !r.Latest.ok {
		return nil, 0, fmt.Errorf("orca terminal.read: %w", ErrNoCursor)
	}
	return r.Tail, r.Oldest.n, nil
}

// outputCursor is a cursor as Orca sends it: a decimal string ("501"), or a number.
type outputCursor struct {
	n  int64
	ok bool
}

func (c *outputCursor) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return nil // not a cursor Persona can page by: as if none came
	}
	c.n, c.ok = n, true
	return nil
}

// AgentStatus is terminal.agentStatus: whether an agent Orca recognises is running in front in the terminal, and its
// state when Orca knows it ("working", "permission", "idle"; "" unknown).
type AgentStatus struct {
	IsRunningAgent bool   `json:"isRunningAgent"`
	Status         string `json:"status"`
}

// AgentStatus is terminal.agentStatus.
func (c *Client) AgentStatus(ctx context.Context, t Target) (*AgentStatus, error) {
	var res struct {
		AgentStatus *AgentStatus `json:"agentStatus"`
	}
	if err := c.Call(ctx, CallTimeout, "terminal.agentStatus", t.params(), &res); err != nil {
		return nil, err
	}
	if res.AgentStatus == nil {
		return nil, fmt.Errorf("orca terminal.agentStatus: no status in the answer")
	}
	return res.AgentStatus, nil
}

// Process is terminal.inspectProcess: what runs in a terminal, as Orca reads its process table. Orca 1.4.277 answers a
// shell at rest with no foreground program, no children and its proof of an idle shell.
type Process struct {
	// foreground is the program in front of the terminal's shell ("" when Orca names none: the shell itself is in
	// front, or it could not tell); children: something other than the shell is in front.
	foreground string
	children   bool
	// childEvidence is how Orca knows children ("no-children", "children", "unverifiable"; "" when it does not say).
	childEvidence string
	// verdict is Orca's own verdict on its evidence ("live" when it read the terminal's process as it is now;
	// "unverifiable" when it could not, as for an incarnation that is not the terminal's any more; "" when the answer
	// carries no evidence, as from older runtimes); reason says why it could not ("incarnation_mismatch"…).
	verdict, reason string
	// idleShell: Orca proved the shell idle — in front, alone in its process group, no child, no stopped or
	// background job on its terminal, no output since it looked (idleShellProven).
	idleShell bool
	// shellPID is the terminal's shell process (the evidence's fence), 0 when not given.
	shellPID int
}

// Known: Orca could tell what runs in the terminal; an answer it marks unverifiable says nothing.
func (p Process) Known() bool {
	return p.childEvidence != "unverifiable" && (p.verdict == "" || p.verdict == "live")
}

// Foreground is the program Orca names in front of the terminal's shell, "" for none (or none it could name).
func (p Process) Foreground() string { return p.foreground }

// HasChildren: something other than the terminal's shell is in front.
func (p Process) HasChildren() bool { return p.children }

// IdleShell: Orca proved the shell at rest with nothing else in its terminal (idleShellProven).
func (p Process) IdleShell() bool { return p.idleShell }

// ShellPID is the terminal's shell process as Orca's evidence names it, 0 when it does not.
func (p Process) ShellPID() int { return p.shellPID }

// Unverified says why Orca could not tell ("incarnation_mismatch", "unverifiable"…), "" for a known answer.
func (p Process) Unverified() string {
	switch {
	case p.Known():
		return ""
	case p.reason != "":
		return p.reason
	}
	return "unverifiable"
}

// InspectProcess is terminal.inspectProcess, fenced to the terminal's incarnation: Orca answers for another process
// behind the handle with an unverifiable verdict (Process.Known false). Orca runtimes without the method answer
// method_not_found.
func (c *Client) InspectProcess(ctx context.Context, t Target) (*Process, error) {
	var res struct {
		Process *struct {
			ForegroundProcess *string `json:"foregroundProcess"`
			HasChildProcesses bool    `json:"hasChildProcesses"`
			ChildEvidence     string  `json:"childProcessEvidence"`
			IdleShellProven   bool    `json:"idleShellProven"`
			Evidence          *struct {
				Verdict string `json:"verdict"`
				Reason  string `json:"reason"`
				Fence   *struct {
					ShellPID int `json:"shellPid"`
				} `json:"fence"`
			} `json:"foregroundProcessEvidence"`
		} `json:"process"`
	}
	if err := c.Call(ctx, CallTimeout, "terminal.inspectProcess", t.params(), &res); err != nil {
		return nil, err
	}
	if res.Process == nil {
		return nil, fmt.Errorf("orca terminal.inspectProcess: no process in the answer")
	}
	r := res.Process
	p := &Process{children: r.HasChildProcesses, childEvidence: r.ChildEvidence, idleShell: r.IdleShellProven}
	if r.ForegroundProcess != nil {
		p.foreground = *r.ForegroundProcess
	}
	if e := r.Evidence; e != nil {
		p.verdict, p.reason = e.Verdict, e.Reason
		if p.verdict == "" {
			p.verdict = "unverifiable" // evidence without a verdict proves nothing
		}
		if e.Fence != nil {
			p.shellPID = e.Fence.ShellPID
		}
	}
	return p, nil
}

// ClientID is how Persona names itself to Orca as the driver of its input (terminal.send's client.id, required).
const ClientID = "persona"

// client is the driver terminal.send names: Persona as a desktop client (the only type Orca pastes agent prompts for).
func client() map[string]any { return map[string]any{"id": ClientID, "type": "desktop"} }

// ErrRefused: Orca accepted the request but wrote nothing (terminal.send's accepted false); Reason says why.
var ErrRefused = errors.New("orca refused the input")

// Refusal is a terminal.send Orca turned down with nothing written: Reason "no-agent" (no prompt-ready agent in
// front), "permission" (the agent waits on a permission dialog) or "not accepted" (input paused, a guard).
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return "orca: input refused: " + r.Reason }
func (r *Refusal) Unwrap() error { return ErrRefused }

// Sent is what terminal.send says it did.
type Sent struct {
	BytesWritten int
	// Prompt: Orca delivered the text as an agent prompt (its receipt); false when it wrote it raw.
	Prompt bool
}

// send is terminal.send; a refusal (nothing written) is a *Refusal.
func (c *Client) send(ctx context.Context, params map[string]any) (*Sent, error) {
	params["client"] = client()
	var res struct {
		Send struct {
			Accepted      *bool           `json:"accepted"`
			BytesWritten  int             `json:"bytesWritten"`
			RefusedReason string          `json:"refusedReason"`
			Prompt        json.RawMessage `json:"prompt"`
		} `json:"send"`
	}
	if err := c.Call(ctx, SendTimeout, "terminal.send", params, &res); err != nil {
		return nil, err
	}
	if a := res.Send.Accepted; a != nil && !*a {
		reason := res.Send.RefusedReason
		if reason == "" {
			reason = "not accepted"
		}
		return nil, &Refusal{Reason: reason}
	}
	p := res.Send.Prompt
	return &Sent{BytesWritten: res.Send.BytesWritten, Prompt: len(p) > 0 && string(p) != "null"}, nil
}

// Send is terminal.send of raw text, without enter: written to the terminal as is (key bytes too), no paste.
func (c *Client) Send(ctx context.Context, t Target, text string) error {
	p := t.params()
	p["text"] = text
	_, err := c.send(ctx, p)
	return err
}

// Prompt is terminal.send of text and enter as an agent prompt: Orca pastes it (bracketed) and submits it when the
// agent it recognises is in front; otherwise it writes the text raw followed by a carriage return (Sent.Prompt false).
func (c *Client) Prompt(ctx context.Context, t Target, text string) (*Sent, error) {
	p := t.params()
	p["text"], p["enter"], p["agentPrompt"] = text, true, true
	return c.send(ctx, p)
}

// Close is terminal.close: Orca closes the terminal's pane (its tab when it was the only one).
func (c *Client) Close(ctx context.Context, t Target) error {
	return c.Call(ctx, CloseTimeout, "terminal.close", t.params(), nil)
}

// CloseVerified requires a receipt for the live terminal just checked by the
// caller. Older or partial replies cannot be treated as proof of completion.
func (c *Client) CloseVerified(ctx context.Context, t Target) error {
	var res struct {
		Close struct {
			Handle    string `json:"handle"`
			PTYKilled bool   `json:"ptyKilled"`
		} `json:"close"`
	}
	if c.ReadOnly {
		return errors.New("orca terminal.close: refused, the client is read-only")
	}
	// Do not retry the write through a reconnected runtime. A retry would
	// need its own ownership and process checks.
	meta, err := c.currentMeta()
	if err != nil {
		return err
	}
	if err := c.callOnce(ctx, meta, CloseTimeout, "terminal.close", t.params(), &res); err != nil {
		return err
	}
	if res.Close.Handle != t.Handle || !res.Close.PTYKilled {
		return errors.New("orca: could not confirm that the terminal closed")
	}
	return nil
}

// CapShellSelection is the capability (Status.Capabilities) of a runtime whose terminal.create honours shell. One
// without it ignores the field and starts its default shell, so ask for a shell only when it is advertised.
const CapShellSelection = "terminal.create-shell-selection.v1"

// GitBash is terminal.create's shell for Git for Windows' bash.
const GitBash = "git-bash"

// CreateOption adds to a terminal.create request (CreateTerminal).
type CreateOption func(*createOptions)

type createOptions struct {
	env     map[string]string
	command string
}

// WithEnv adds env to the environment the terminal's shell starts with (terminal.create's env). An Orca that refuses
// the request with it (invalid_argument: a schema without env, answered before anything is made) is asked again
// without it, so the terminal still opens, its shell without those variables; one that does not know the field drops it
// the same way (zod strips unknown keys).
func WithEnv(env map[string]string) CreateOption {
	return func(o *createOptions) { o.env = env }
}

// WithCommand has Orca type line into the new shell once it is ready (terminal.create's command, delivered shell-ready):
// where Orca can tell, after the shell's startup files and never into a question they ask. It goes only into the
// terminal this request makes. terminal.create's cwd is not honoured for a create like this one (Orca 1.4.277), so a
// terminal for a folder deeper than its worktree is put there this way.
func WithCommand(line string) CreateOption {
	return func(o *createOptions) { o.command = line }
}

// CreateTerminal opens a fresh shell in an existing Orca worktree. The ordinary create RPC ignores cwd, so the
// launcher changes directory explicitly when it runs its command. It never uses the currently focused terminal.
// shell is the Windows shell the terminal runs (GitBash, "powershell.exe"…; only with CapShellSelection), "" for
// the user's default. opts add to the request (WithEnv, WithCommand).
func (c *Client) CreateTerminal(ctx context.Context, worktree, title, mutationID, shell string, opts ...CreateOption) (*Terminal, error) {
	var o createOptions
	for _, opt := range opts {
		opt(&o)
	}
	var res struct {
		Terminal struct {
			Terminal
			PaneKey string `json:"paneKey"`
		} `json:"terminal"`
	}
	params := map[string]any{
		"worktree": worktree, "title": title, "clientMutationId": mutationID,
		// Explicit "background" skips desktop tab registration entirely. Omit it to create an inactive tab;
		// Orca awaits the renderer's creation receipt before returning, without switching the user's active tab.
		"focus": false, "activate": false,
	}
	if shell != "" {
		params["shell"] = shell
	}
	if o.command != "" {
		params["command"], params["startupCommandDelivery"] = o.command, "shell-ready"
	}
	create := func(p map[string]any) error {
		res.Terminal.Terminal, res.Terminal.PaneKey = Terminal{}, ""
		return c.Call(ctx, SendTimeout, "terminal.create", p, &res)
	}
	var err error
	withEnv := len(o.env) > 0
	if withEnv {
		with := maps.Clone(params)
		with["env"] = maps.Clone(o.env)
		// invalid_argument is answered before anything is made, so the same request without env makes one terminal.
		if err = create(with); IsCode(err, "invalid_argument") {
			withEnv = false
			err = create(params)
		}
	} else {
		err = create(params)
	}
	if err != nil {
		return nil, err
	}
	t := res.Terminal.Terminal
	if t.Handle == "" || !t.IsLocal() {
		return nil, fmt.Errorf("orca terminal.create: no local terminal handle in the answer; creation may have completed")
	}
	// Orca 1.4.242/243 return paneKey instead of leafId, and may omit the incarnation in the create receipt.
	// Resolve only the exact returned handle; a newer terminal or a matching title is not proof of ownership.
	list, err := c.Terminals(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("orca terminal.create: created %s but could not resolve its identity: %w", t.Handle, err)
	}
	var found *Terminal
	for _, current := range list.Terminals {
		if current.Handle != t.Handle {
			continue
		}
		if current.Orphaned {
			return nil, fmt.Errorf("orca terminal.create: created %s but Orca did not register its tab; no command was sent", t.Handle)
		}
		if found != nil || !current.IsLocal() || current.IncarnationID == "" || current.TabID == "" || current.LeafID == "" ||
			t.IncarnationID != "" && current.IncarnationID != t.IncarnationID ||
			t.TabID != "" && current.TabID != t.TabID || t.LeafID != "" && current.LeafID != t.LeafID ||
			res.Terminal.PaneKey != "" && current.PaneKey() != res.Terminal.PaneKey ||
			t.WorktreeID != "" && current.WorktreeID != t.WorktreeID {
			return nil, fmt.Errorf("orca terminal.create: created %s but its identity changed; no command was sent", t.Handle)
		}
		found = &current
	}
	if found == nil {
		return nil, fmt.Errorf("orca terminal.create: created %s is absent from terminal.list; no command was sent", t.Handle)
	}
	found.ProcessID, found.AcceptedCreateEnv = t.ProcessID, withEnv
	return found, nil
}

// RenameTerminal sets the title of the terminal tab in Orca's layout.
func (c *Client) RenameTerminal(ctx context.Context, t Target, title string) error {
	p := t.params()
	p["title"] = title
	return c.Call(ctx, CallTimeout, "terminal.rename", p, nil)
}

// SetPaneTitle sets the pane title in Orca.
func (c *Client) SetPaneTitle(ctx context.Context, t Target, title string) error {
	p := t.params()
	p["title"] = title
	return c.Call(ctx, CallTimeout, "terminal.setPaneTitle", p, nil)
}

// RenameWorktree changes only the sidebar display name, leaving its branch, path and parent alone.
func (c *Client) RenameWorktree(ctx context.Context, id, name string) error {
	return c.Call(ctx, CallTimeout, "worktree.set", map[string]any{"worktree": "id:" + id, "displayName": name}, nil)
}

// RepoAdd registers a new git repository or folder workspace with Orca.
func (c *Client) RepoAdd(ctx context.Context, path, kind string) error {
	return c.Call(ctx, CallTimeout, "repo.add", map[string]any{"path": path, "kind": kind}, nil)
}

// WorktreeCreate asks worktree.create for a worktree of Repo (a repo selector, "id:<repo id>") named Name (its folder
// under Orca's workspace directory) on Branch: an existing local branch is checked out, else Branch is made from Base
// (Orca's base ref when empty). Parent (a worktree selector) is the worktree it is listed under in Orca's sidebar.
type WorktreeCreate struct {
	Repo, Name, Branch, Base, Parent string
	// Generated lets Orca retire collisions and keeps its first-work branch naming eligible.
	Generated bool
}

// CreatedWorktree is the worktree worktree.create made. Orca picks another folder and branch (a "-2" suffix) when the
// asked ones are taken, so these are what it is, not what was asked.
type CreatedWorktree struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// CreateWorktree is worktree.create: Orca makes the checkout and keeps it as one of its worktrees, without activating
// it (no focus change, no setup hooks).
func (c *Client) CreateWorktree(ctx context.Context, w WorktreeCreate) (*CreatedWorktree, error) {
	p := map[string]any{"repo": w.Repo, "name": w.Name, "activate": false}
	if w.Generated {
		p["nameWasGenerated"] = true
	} else {
		p["branchNameOverride"] = w.Branch
	}
	if w.Base != "" {
		p["baseBranch"] = w.Base
	}
	if w.Parent != "" {
		p["parentWorktree"] = w.Parent
	}
	var res struct {
		Worktree CreatedWorktree `json:"worktree"`
	}
	if err := c.Call(ctx, WorktreeTimeout, "worktree.create", p, &res); err != nil {
		return nil, err
	}
	if res.Worktree.ID == "" || res.Worktree.Path == "" {
		return nil, fmt.Errorf("orca worktree.create: no worktree in the answer")
	}
	return &res.Worktree, nil
}

// Probe is status.get without the compatibility gate: the runtime's status (nil when it did not answer) and whether
// this client can use it (ErrIncompatible says why not).
func (c *Client) Probe(ctx context.Context) (*Status, error) {
	var st Status
	err := c.call(ctx, CallTimeout, "status.get", nil, &st, false)
	if !gaveUp(ctx) {
		c.note("status.get", err)
	}
	if err != nil {
		return nil, err
	}
	return &st, st.compatible()
}

// Version is the running Orca's app version, "" when it does not answer or speaks a protocol this client does not.
func (c *Client) Version(ctx context.Context) string {
	st, err := c.Probe(ctx)
	if err != nil {
		return ""
	}
	return st.AppVersion
}
