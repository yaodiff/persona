package node

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionhistory"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Sessions active in a time window, archived, finished and offline ones too (sessions.history; docs/protocol/protocol.md
// "Session history"). The live list keeps only the recent: Claude transcripts of the last 7 days (the 80 newest),
// Codex rollouts of the last 14, and what each runtime chooses to push (the SDK's rt.recent: 7 days, 80). An
// extension reading the user's past sessions sees nothing older that way, so this lists by the window instead: the
// node reads its own Claude Code and Codex records with no age or count limit, and asks every runtime that declares
// caps.history. History rows are only read: they never enter the live list.

// runtimeHistoryTimeout bounds one runtime's answer to one sessions.history page.
var runtimeHistoryTimeout = 30 * time.Second

type historyParams struct {
	Since   string `json:"since"`
	Until   string `json:"until"`
	Cursor  string `json:"cursor"`
	Limit   int    `json:"limit"`
	Source  string `json:"source"`
	Runtime string `json:"runtime"`
}

func init() { nodeRPC("sessions.history", (*Node).sessionsHistory) } // sessions active in a time window, inactive ones too

// sessionsHistory answers sessions.history: one page of every source's sessions in the window (or of the one source
// asked), merged newest first, and how each source answered.
func (n *Node) sessionsHistory(ctx context.Context, raw json.RawMessage) (sessionhistory.Answer, error) {
	var p historyParams
	if len(raw) > 0 && json.Unmarshal(raw, &p) != nil {
		return sessionhistory.Answer{}, errors.New("sessions.history: malformed params")
	}
	q, err := sessionhistory.Parse(p.Since, p.Until, p.Cursor, p.Limit, time.Now())
	if err != nil {
		return sessionhistory.Answer{}, err
	}
	switch p.Source {
	case "", sessionhistory.SourceClaude, sessionhistory.SourceCodex, sessionhistory.SourceRuntime:
	default:
		return sessionhistory.Answer{}, errors.Join(sessionhistory.ErrInvalid, errors.New("unknown source "+p.Source))
	}
	after, _ := sessionhistory.DecodeCursor(q.Cursor) // Parse checked it
	wants := func(source string) bool { return p.Source == "" || p.Source == source }
	ans := sessionhistory.Answer{Rows: []sessionhistory.Row{}, Complete: true}
	var parts []sessionhistory.Part[sessionhistory.Row]
	if wants(sessionhistory.SourceClaude) {
		parts = append(parts, sessionhistory.Part[sessionhistory.Row]{Items: sessionhistory.After(n.claudeHistory(q), after)})
		ans.Sources = append(ans.Sources, sessionhistory.SourceState{Source: sessionhistory.SourceClaude, Honored: true, Complete: true})
	}
	if wants(sessionhistory.SourceCodex) {
		parts = append(parts, sessionhistory.Part[sessionhistory.Row]{Items: sessionhistory.After(n.codexHistory(q), after)})
		ans.Sources = append(ans.Sources, sessionhistory.SourceState{Source: sessionhistory.SourceCodex, Honored: true, Complete: true})
	}
	if wants(sessionhistory.SourceRuntime) && n.runtimes != nil {
		for _, h := range n.runtimes.ready() {
			id := h.id()
			if p.Runtime != "" && p.Runtime != id {
				continue
			}
			part, state := runtimeHistory(ctx, h, q, after, n.listBlocked)
			parts = append(parts, part)
			ans.Sources = append(ans.Sources, state)
		}
	}
	for _, s := range ans.Sources {
		ans.Complete = ans.Complete && s.Honored && s.Complete
	}
	page, next := sessionhistory.Merge(parts, q.Limit)
	for _, it := range page {
		it.V.Session.Title = transcript.ListTitle(it.V.Session.Title) // as the live list's rows (sessionListIn, #16343)
		ans.Rows = append(ans.Rows, it.V)
	}
	if next != nil {
		ans.NextCursor = sessionhistory.EncodeCursor(*next)
	}
	return ans, nil
}

// historyItem is a row of this node's with its key in the node's order.
func historyItem(r sessionhistory.Row) sessionhistory.Item[sessionhistory.Row] {
	return sessionhistory.Item[sessionhistory.Row]{Key: sessionhistory.KeyOf(r.At(), r.NodeKey()), V: r}
}

// runtimeHistory asks one runtime for the page: not at all when it does not declare caps.history. A session the live
// list leaves out for a blocked folder (blocked) is in no page either; it is dropped before paging, so a page stays full.
func runtimeHistory(ctx context.Context, h *runtimeConn, q sessionhistory.Query, after *sessionhistory.Key, blocked func(sid, cwd string) bool) (sessionhistory.Part[sessionhistory.Row], sessionhistory.SourceState) {
	h.mu.Lock()
	id, supported := h.hello.Runtime, h.hello.Caps.History
	h.mu.Unlock()
	state := sessionhistory.SourceState{Source: sessionhistory.SourceRuntime, Runtime: id}
	if !supported {
		state.Reason = sessionhistory.ReasonUnsupported
		return sessionhistory.Part[sessionhistory.Row]{}, state
	}
	fetch := func(ctx context.Context, sub sessionhistory.Query) (sessionhistory.Page[sessionhistory.Row], error) {
		cctx, cancel := context.WithTimeout(ctx, runtimeHistoryTimeout)
		defer cancel()
		var res struct {
			Rows       []json.RawMessage `json:"rows"`
			NextCursor string            `json:"next_cursor"`
			Complete   bool              `json:"complete"`
		}
		if err := h.peer.Call(cctx, "sessions.history", sub.Params(), &res); err != nil {
			return sessionhistory.Page[sessionhistory.Row]{}, err
		}
		page := sessionhistory.Page[sessionhistory.Row]{NextCursor: res.NextCursor, Complete: res.Complete}
		// Keyed by the runtime's own ids and times, before normalizing: its order goes by them. A deleted session is
		// in no history; a row that does not read is dropped, as a list drops it.
		rows := map[string]sessionhistory.Row{}
		var sessions []proto.Session
		for _, raw := range res.Rows {
			r, deleted, err := sessionhistory.RuntimeRow(raw)
			if err != nil || deleted || r.Session.SessionID == "" {
				continue
			}
			r.Session.PaneID = "" // a pane is the live list's (runtimes_events.go), never a past session's
			r.Source, r.Runtime = sessionhistory.SourceRuntime, id
			if _, dup := rows[r.Session.SessionID]; dup {
				continue
			}
			rows[r.Session.SessionID] = r
			sessions = append(sessions, r.Session)
		}
		// This snapshot can predate the live list, so only its own text belongs in the page.
		preview := func(_ string, text string) string { return transcript.WithoutBotmuxSentinel(text) }
		for _, s := range normalizeRuntimeRows(h, sessions, preview) {
			r := rows[s.SessionID]
			key := sessionhistory.KeyOf(r.At(), r.NodeKey())
			if page.Reached == nil || page.Reached.Before(key) {
				page.Reached = &key // read, left out or not: the page goes on past it (Merge)
			}
			if blocked(s.SessionID, s.Cwd) {
				continue
			}
			s.Archived = r.Session.Archived
			r.Session = s
			page.Items = append(page.Items, sessionhistory.Item[sessionhistory.Row]{Key: key, V: r})
		}
		return page, nil
	}
	part, complete, err := sessionhistory.Collect(ctx, fetch, q, after, q.Limit)
	if err != nil {
		state.Reason = sessionhistory.ReasonFailed
		return sessionhistory.Part[sessionhistory.Row]{}, state
	}
	state.Honored, state.Complete = true, complete
	return part, state
}

// claudeHistory lists the Claude Code transcripts updated in the window, whatever their age, with the live list's
// filters on whose they are (scope, blocked directories, other platforms' and programs' runs, probes that never said
// anything). The assistant's conversations are not Agents' sessions: left out.
func (n *Node) claudeHistory(q sessionhistory.Query) []sessionhistory.Item[sessionhistory.Row] {
	type cand struct {
		path string
		st   os.FileInfo
	}
	n.mu.Lock()
	forked := maps.Clone(n.forked)
	n.mu.Unlock()
	own := n.assistantProject()
	persona := n.personaProjectDirs()
	unlisted := n.runtimes.unlistedTranscripts()
	newest := map[string]cand{} // an older copy in another config directory is the same session
	for _, p := range n.globProjects("*", "*.jsonl") {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if !validID(id) {
			continue
		}
		st, err := os.Stat(p)
		if err != nil || st.ModTime().UnixMilli() < q.Since.UnixMilli() || (st.Size() < 2048 && !forked[id]) {
			continue // a record last written before the window was not used in it
		}
		if isAssistantProject(filepath.Base(filepath.Dir(p)), own) {
			continue
		}
		if !forked[id] && (n.agentFiltered("claude", p) || !unlisted[id] && n.oneShotRun(id, filepath.Base(filepath.Dir(p)), p, st, persona)) {
			continue
		}
		if c, ok := newest[id]; ok && !st.ModTime().After(c.st.ModTime()) {
			continue
		}
		newest[id] = cand{p, st}
	}
	keep := n.keptSessions()
	var out []sessionhistory.Item[sessionhistory.Row]
	for id, c := range newest {
		m := n.meta(c.path, c.st)
		updated := lastUsed(m, c.st)
		if !q.In(updated) || isLauncherCheckRun(c.path, m) {
			continue
		}
		if !n.sessionScopeAllows(m.cwd) || n.restBlocked(id, m.cwd, keep) {
			continue
		}
		if m.lastText == "" && m.lastTextAt.IsZero() && m.title == "" && !forked[id] && c.st.Size() <= quickTail {
			continue
		}
		s := proto.Session{
			SessionID: id, Title: m.title, Cwd: m.cwd, Status: proto.StatusOffline, Model: m.model,
			Agent: "claude", AgentSurface: m.surface,
			LastText: snippet(m.lastText), UpdatedAt: updated, Rev: c.st.Size(),
			Capabilities: &proto.SessionCapabilities{Read: true},
		}
		untitled(&s, m)
		out = append(out, historyItem(sessionhistory.Row{Session: s, Source: sessionhistory.SourceClaude, ActiveAt: updated}))
	}
	return sessionhistory.Sorted(out)
}

// codexHistory lists the Codex rollouts (every configured home, archived ones too) updated in the window, whatever
// their age: the conversations only, not their subagents' threads or programs' one-shot runs (codex exec), with the
// live list's filters on whose they are. The assistant's conversations are left out.
func (n *Node) codexHistory(q sessionhistory.Query) []sessionhistory.Item[sessionhistory.Row] {
	if n.codices == nil {
		return nil
	}
	type cand struct {
		home, path, thread string
		st                 os.FileInfo
		archived           bool
	}
	newest := map[string]cand{}
	for _, home := range n.codexInventory().Homes {
		live, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"))
		archived, _ := filepath.Glob(filepath.Join(home, "archived_sessions", "rollout-*.jsonl"))
		for i, p := range append(live, archived...) {
			thread := rolloutID(p)
			if thread == "" {
				continue
			}
			st, err := os.Stat(p)
			if err != nil || st.ModTime().UnixMilli() < q.Since.UnixMilli() {
				continue
			}
			if meta := metaOf(home, p); meta.sub || identity.IsAssistantDir(meta.cwd) || readCodexOrigin(p).exec {
				continue
			}
			id := n.codexPublicID(home, thread)
			if c, ok := newest[id]; ok && !st.ModTime().After(c.st.ModTime()) {
				continue
			}
			newest[id] = cand{home, p, thread, st, i >= len(live)}
		}
	}
	keep := n.keptSessions()
	var out []sessionhistory.Item[sessionhistory.Row]
	for id, c := range newest {
		if n.isAssistantSession(id) || n.agentFiltered("codex", c.path) && !keep[id] || n.codexBlocked(id) {
			continue
		}
		m := n.meta(c.path, c.st)
		updated := lastUsed(m, c.st)
		if !q.In(updated) || !n.sessionScopeAllows(m.cwd) || n.restBlocked(id, m.cwd, keep) {
			continue
		}
		s := proto.Session{
			SessionID: id, Agent: "codex", SourceKind: "codex_history", SourceThreadID: c.thread, Status: proto.StatusOffline,
			Cwd: m.cwd, Model: m.model, LastText: snippet(m.lastText), UpdatedAt: updated, Rev: c.st.Size(),
			AgentSurface: m.surface, Title: threadTitle(c.home, c.thread), AgentName: filepath.Base(c.home),
			Archived: c.archived, Capabilities: &proto.SessionCapabilities{Read: true},
		}
		untitled(&s, m)
		out = append(out, historyItem(sessionhistory.Row{Session: s, Source: sessionhistory.SourceCodex, ActiveAt: updated}))
	}
	return sessionhistory.Sorted(out)
}
