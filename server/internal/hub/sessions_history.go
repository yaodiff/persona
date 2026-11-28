package hub

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/sessionhistory"
	"github.com/nwverse/persona/server/internal/transcript"
)

// GET /api/v1/sessions?include_inactive=1&since=…: sessions active in a time window, archived, finished and offline
// ones too (docs/protocol/protocol.md "Session history"). The hub only relays: each machine's node lists its sources
// (sessions.history) and the hub merges the machines' pages newest first. History rows are only read: they never
// enter the live list, never make anything unread and never wake supervision.

// historyMachineTimeout bounds one machine's answer to one page.
var historyMachineTimeout = 45 * time.Second

// An extension's or scoped token reads only the conversations the live list has (extensionReadable). A history row it
// was given is readable too, for historyReadKeep after it was last given: the hub remembers which, in memory only and
// at most historyReadMax (a restart forgets them; listing the window again restores them).
const (
	historyReadKeep = 24 * time.Hour
	historyReadMax  = 100_000
)

type historyReads struct {
	mu sync.Mutex
	at map[string]time.Time // machine + "\x00" + session id → when last given
}

func (r *historyReads) remember(machine, sid string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at == nil {
		r.at = map[string]time.Time{}
	}
	if len(r.at) >= historyReadMax {
		for k, at := range r.at {
			if now.Sub(at) > historyReadKeep {
				delete(r.at, k)
			}
		}
		if len(r.at) >= historyReadMax {
			r.at = map[string]time.Time{} // all fresh: start over rather than grow
		}
	}
	r.at[machine+"\x00"+sid] = now
}

func (r *historyReads) readable(machine, sid string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.at[machine+"\x00"+sid]
	return ok && now.Sub(at) <= historyReadKeep
}

// historyMode reports whether a list request asks for history; an include_inactive that is neither on nor off is an
// error, as the indexed list refuses a malformed parameter rather than answering something else.
func historyMode(r *http.Request) (bool, error) {
	v, ok := r.URL.Query()["include_inactive"]
	if !ok {
		return false, nil
	}
	if len(v) != 1 {
		return false, errors.New("include_inactive must have exactly one value")
	}
	switch v[0] {
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	}
	return false, errors.New("include_inactive must be 1 or 0")
}

type historyResponse struct {
	Sessions []sessionhistory.Row `json:"sessions"`
	History  historyMeta          `json:"history"`
}

type historyMeta struct {
	Since      string                       `json:"since"`
	Until      string                       `json:"until"`
	Complete   bool                         `json:"complete"`
	NextCursor string                       `json:"next_cursor"`
	Sources    []sessionhistory.SourceState `json:"sources"`
}

func (h *Hub) historySessions(w http.ResponseWriter, r *http.Request, named string) {
	v := r.URL.Query()
	for _, k := range []string{"since", "until", "cursor", "limit", "machine_id"} {
		if len(v[k]) > 1 {
			sessionListErr(w, errors.New(k+" must have exactly one value"))
			return
		}
	}
	limit := 0
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			sessionListErr(w, errors.New("limit must be a positive integer"))
			return
		}
		limit = n
	}
	q, err := sessionhistory.Parse(v.Get("since"), v.Get("until"), v.Get("cursor"), limit, time.Now())
	if err != nil {
		sessionListErr(w, err)
		return
	}
	mid := v.Get("machine_id")
	em := extensionManager(r) // an extension's token: its manager's machines only
	if em != "" && mid != "" && h.machineOwner(mid) != em {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
		return
	}
	var machines []*Machine
	for _, m := range h.machinesNow() {
		owner := h.machineOwner(m.cfg.ID)
		if mid != "" && m.cfg.ID != mid || em != "" && owner != em || named != "" && owner != named {
			continue
		}
		machines = append(machines, m)
	}
	after, _ := sessionhistory.DecodeCursor(q.Cursor) // Parse checked it
	parts := make([]sessionhistory.Part[sessionhistory.Row], len(machines))
	states := make([][]sessionhistory.SourceState, len(machines))
	var wg sync.WaitGroup
	for i, m := range machines {
		wg.Go(func() { parts[i], states[i] = h.machineHistory(r.Context(), m, q, after, h.spacesHidden(r)) })
	}
	wg.Wait()
	resp := historyResponse{Sessions: []sessionhistory.Row{}, History: historyMeta{
		Since: q.Since.UTC().Format(time.RFC3339Nano), Until: q.Until.UTC().Format(time.RFC3339Nano),
		Complete: true, Sources: []sessionhistory.SourceState{}}}
	for _, list := range states {
		for _, s := range list {
			resp.History.Complete = resp.History.Complete && s.Honored && s.Complete
			resp.History.Sources = append(resp.History.Sources, s)
		}
	}
	page, next := sessionhistory.Merge(parts, q.Limit)
	restricted, now := restrictedAgentData(r), time.Now()
	for _, it := range page {
		resp.Sessions = append(resp.Sessions, it.V)
		if restricted { // space rows never reach here (machineHistory)
			h.historyReads.remember(it.V.Session.MachineID, it.V.Session.SessionID, now)
		}
	}
	if next != nil {
		resp.History.NextCursor = sessionhistory.EncodeCursor(*next)
	}
	writeJSON(w, resp)
}

// machineHistory reads one machine's rows for the page, keyed by the machine before the node's own key, and how its
// sources answered. Sessions the user deleted in Persona are in no history; archived ones are. noSpaces: the caller
// is an extension or scoped client, which never sees a space's conversations (extension_private.go).
func (h *Hub) machineHistory(ctx context.Context, m *Machine, q sessionhistory.Query, after *sessionhistory.Key, noSpaces bool) (sessionhistory.Part[sessionhistory.Row], []sessionhistory.SourceState) {
	id, manager := m.cfg.ID, h.machineOwner(m.cfg.ID)
	deleted := h.deletedAt(id)
	var sources []sessionhistory.SourceState
	fetch := func(ctx context.Context, sub sessionhistory.Query) (sessionhistory.Page[sessionhistory.Row], error) {
		cctx, cancel := context.WithTimeout(ctx, historyMachineTimeout)
		defer cancel()
		var ans sessionhistory.Answer
		if err := m.call(cctx, "sessions.history", sub.Params(), &ans); err != nil {
			return sessionhistory.Page[sessionhistory.Row]{}, err
		}
		if sources == nil {
			sources = ans.Sources
		}
		page := sessionhistory.Page[sessionhistory.Row]{NextCursor: ans.NextCursor, Complete: ans.Complete}
		if k, err := sessionhistory.DecodeCursor(ans.NextCursor); err == nil && k != nil {
			// The node read up to its cursor, rows it left out included (a blocked folder's): an empty page of it
			// still moves the merge on (sessionhistory.Merge).
			page.Reached = &sessionhistory.Key{T: k.T, K: id + "\x00" + k.K}
		}
		for _, r := range ans.Rows {
			if r.Session.IsDeleted() || sessionHiddenByDeletion(r.Session, deleted) || noSpaces && spaceSession(&r.Session) {
				continue
			}
			key := sessionhistory.KeyOf(r.At(), id+"\x00"+r.NodeKey())
			r.Session.MachineID, r.Session.Manager = id, manager
			// A node from before titles were cut sends its agents' titles whole, an opening prompt among them (#16343).
			r.Session.Title = transcript.ListTitle(r.Session.Title)
			page.Items = append(page.Items, sessionhistory.Item[sessionhistory.Row]{Key: key, V: r})
		}
		return page, nil
	}
	part, complete, err := sessionhistory.Collect(ctx, fetch, q, after, q.Limit)
	if err != nil {
		reason := sessionhistory.ReasonFailed
		switch {
		case errors.Is(err, errOffline):
			reason = sessionhistory.ReasonOffline
		case strings.Contains(err.Error(), "unknown method"):
			reason = sessionhistory.ReasonNodeUnsupported
		}
		return sessionhistory.Part[sessionhistory.Row]{}, []sessionhistory.SourceState{{Machine: id, Reason: reason}}
	}
	out := make([]sessionhistory.SourceState, 0, len(sources))
	for _, s := range sources {
		s.Machine = id
		out = append(out, s)
	}
	if len(out) == 0 { // a node that names no source: what it gave is all it has
		out = append(out, sessionhistory.SourceState{Machine: id, Honored: true, Complete: complete})
	}
	return part, out
}
