package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
	"github.com/nwverse/persona/server/internal/sessionorigin"
)

const (
	featureSessionsList       = "sessions_list"
	featureSessionsLookup     = "sessions_lookup"
	featureSessionsPaneSource = "sessions_pane_source"
	// featureSessionsManager: GET /api/v1/sessions takes ?manager=<id> (sessionsManager). An older hub ignores the
	// key and answers with every manager's sessions, so an app sends it only to a hub that says this.
	featureSessionsManager = "sessions_manager"
	// featureSessionsPageCounts: a paged GET /api/v1/sessions takes with_counts=1 and answers with the page plus the
	// counts ?counts=1 would give for the same filters (sessionlist.PageWithCounts), one read where a refresh took two (#4858).
	featureSessionsPageCounts = "sessions_page_counts"
	// featureAgentsPageCounts: the same for a paged GET /api/v1/agents/sessions, the personal Agents the extension
	// manager serves (extmgr/agents.go, #6496).
	featureAgentsPageCounts = "agents_page_counts"
	// featureAgentsProviderOR: a paged GET /api/v1/agents/sessions takes provider repeatedly (1 to 64 values) for an
	// exact, case-sensitive OR set. Order and duplicates do not change matching, counts or cursor binding. An older hub
	// rejects repetition on a paged request, so clients send it only when this feature is advertised (#994).
	featureAgentsProviderOR = "agents_provider_or"
	// featureSessionsListChanges: an app that lists it in the WebSocket URL's ?features= gets, on a sessions_list hint
	// that follows a change of the index, the revision before the change (`since`) and the ids changed after it
	// (`changed`), so an open chat does not read its row again for another session's change (sessionListHint, #6685).
	featureSessionsListChanges = "sessions_list_changes"
	// featureSessionsHierarchy: a paged GET /api/v1/sessions takes roots=1 (the child sessions folded under a listed
	// parent left out of the rows and counts, the parents counted by their folded state) and parent_session_id=<sid>
	// with machine_id (the children folded under that session), docs/protocol/protocol.md "Session hierarchy" (#5120).
	// An older hub reads neither key, so an app sends them only to a hub that says this.
	featureSessionsHierarchy = "sessions_hierarchy"
	// featureSessionsCrossMachineHierarchy: a paged GET /api/v1/sessions takes parent_machine_id with parent_session_id
	// (and machine_id with parent_session_id) to return both same-machine and cross-machine children folded under that
	// parent, carries folded_under_machine_id / parent_machine_id / a2a_task_id / subagent_origin on rows and
	// folded_under_machine_id on interactions, and excludes orphan is_subagent rows under roots=1.
	featureSessionsCrossMachineHierarchy = "sessions_cross_machine_hierarchy"
	// featureSessionsSideFold: the rows carry the side-session fold (folded_as "side", folded_side, the children's
	// sides rollup; docs/protocol/protocol.md "Session hierarchy", #7944), and a client folds side sessions into their
	// parent's chat. The fold is the managers' (assistant/fleet_fold.go): the hub lists the feature only while every
	// manager that carries Agents names it (sessionsSideFoldFeatures).
	featureSessionsSideFold = assistantwire.CapabilitySessionsSideFold
	// featureSessionsCatalog: a paged GET /api/v1/sessions takes catalog=1 (the projects and spaces of the filtered
	// list, a repository's directories on every computer as one project: sessions_catalog.go), and
	// place=["<machine>","<project>"] and space=["<machine>","<group>"], both repeatable (the sessions at those
	// directories, in those spaces), docs/protocol/protocol.md "Projects and spaces". An older hub reads none of the
	// keys, so an app sends them only to a hub that says this.
	featureSessionsCatalog = "sessions_catalog"
	// featureSessionsAgentsOnly scopes pages, counts and deltas to the Agents sidebar view.
	featureSessionsAgentsOnly = "sessions_agents_only"
	// featureSessionsTodo: a paged GET /api/v1/sessions takes todo=<id> (the sessions on that open to-do) and
	// todo=none (on none), and its counts have a todo map (by to-do id, "" for none), docs/protocol/protocol.md
	// "Sessions by to-do" (#6662). An older hub ignores the key and lists every session, so an app sends it only to a
	// hub that says this.
	featureSessionsTodo = "sessions_todo"
	// featureSessionsQuickFilters: a paged GET /api/v1/sessions takes waiting=0|1 (working rows that only wait in the
	// background, or the others) and exclude_pane_source=<origin> (all but that origin's rows): the web sidebar's quick
	// filters on the paged list, docs/protocol/protocol.md "Quick filters" (#6662). An older hub ignores both keys.
	featureSessionsQuickFilters = "sessions_quick_filters"
	// featureSessionsJustFinished: a paged GET /api/v1/sessions takes just_finished=0|1 (seen rows whose done turn
	// nobody had looked at while it is new, or the others), and its counts have just_finished, docs/protocol/protocol.md
	// "Just finished rows" (#9751). An older hub ignores the key and lists every seen row, so an app sends it only to a
	// hub that says this.
	featureSessionsJustFinished = "sessions_just_finished"
)

// sessionsSideFoldFeatures is sessions_side_fold while every manager that carries Agents (the default one, each
// connected one and each computer's owner) is connected and folds side sessions, as agent_session_archives is: a
// hub-wide feature cannot promise the fold for part of the fleet, so mixed versions keep side sessions as rows.
func (h *Hub) sessionsSideFoldFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ids := h.archiveManagers(ctx)
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if h.archiveManagerHas(id, assistantwire.CapabilitySessionsSideFold) != http.StatusOK {
			return nil
		}
	}
	return []string{featureSessionsSideFold}
}

// runtimeList is a transport cache of the manager's already-decorated rows. It
// does not derive agent state or store business data. sessionsMu orders updates
// with legacy list pushes, while the index serves readers independently.
type runtimeList struct {
	once     sync.Once
	index    sessionlist.Index
	machines map[string]bool // guarded by sessionsMu
	// unaligned: machines whose last pass had a row not encoded under an id of its own (indexSessionsLocked); guarded
	// by sessionsMu.
	unaligned map[string]bool
	// crossFolded: parentMachine -> set of childMachines carrying rows with FoldedUnderMachineID == parentMachine, so
	// parent machine updates or removals re-evaluate cross-machine children's roots; guarded by sessionsMu.
	crossFolded     map[string]map[string]bool
	reindexingCross bool
}

func (h *Hub) sessionListIndex() *sessionlist.Index {
	h.listCache.once.Do(func() {
		h.sessionsMu.Lock()
		defer h.sessionsMu.Unlock()
		h.listCache.machines = map[string]bool{}
		for _, m := range h.machinesNow() {
			h.indexKeptLocked(m)
		}
	})
	return &h.listCache.index
}

// indexSessionsLocked consumes the same encoded whole rows used by the legacy
// push path. Bounded rows add the scalar origin; summaries encode once per update.
// A row whose encoded bytes the index already holds keeps its indexed payloads
// (indexedRow): usually one row of a list changed since the last update.
func (h *Hub) indexSessionsLocked(mid string, list []proto.Session, encoded []sentSession) {
	h.indexStampedLocked(mid, list, false, "", encoded)
}

// indexKeptLocked indexes m's kept list as the apps get it (shown, stamped with its manager) from the rows a push
// sends: those whose bytes, rev and manager are unchanged are the kept rows themselves (encodeKept), so neither a copy
// of the list nor an encoding of every row is made. sessionsMu is held; encodeKept then takes rowsMu, in the order
// snapshotSessions takes them.
func (h *Hub) indexKeptLocked(m *Machine) {
	kept := m.keptSessions()
	owner := h.machineOwner(m.cfg.ID)
	shown := h.shown(m.cfg.ID, kept)
	h.indexStampedLocked(m.cfg.ID, shown, true, owner, m.encodeKept(shown, owner, m.rawsFor(kept)))
}

// indexStampedLocked is indexSessionsLocked of list as stamped with manager when stamp (stamped), without the copy
// of the list: of what the index reads only a summary has the manager, and indexedRow puts it there.
func (h *Hub) indexStampedLocked(mid string, list []proto.Session, stamp bool, manager string, encoded []sentSession) {
	if h.listCache.machines == nil {
		return // no paginated reader yet; legacy users pay no indexing overhead
	}
	full := make(map[string]json.RawMessage, len(encoded))
	for _, row := range encoded {
		full[row.id] = row.raw
	}
	// A row's summary is read from the row and its full row from the bytes under its id: the same session only while
	// every row encoded under an id of its own. A duplicate id takes the last one's bytes and the first one's summary,
	// so after a pass that is not so, and in the pass after it, every row is computed again.
	aligned := len(encoded) == len(list) && len(full) == len(encoded)
	reuse := aligned && !h.listCache.unaligned[mid]
	if aligned {
		delete(h.listCache.unaligned, mid)
	} else {
		if h.listCache.unaligned == nil {
			h.listCache.unaligned = map[string]bool{}
		}
		h.listCache.unaligned[mid] = true
	}
	rows := make([]int, 0, len(list))
	ids := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i := range list {
		sid := list[i].SessionID
		if sid == "" || seen[sid] || len(full[sid]) == 0 {
			continue
		}
		seen[sid] = true
		rows = append(rows, i)
		ids = append(ids, runtimeListID(mid, sid))
	}
	var items []sessionlist.Item
	if reuse {
		items = h.listCache.index.Items(ids) // what each row was indexed as, to compare
	} else {
		items = make([]sessionlist.Item, len(ids))
	}
	var crossParents map[string]bool
	var parentsShown shownSessions // read once per parent machine for this pass (#15899)
	var localKids map[string]proto.SessionChildren
	for _, i := range rows {
		if list[i].Children().Total > 0 {
			localKids = map[string]proto.SessionChildren{}
			break
		}
	}
	if localKids != nil {
		for _, i := range rows {
			r := list[i].FoldedUnder()
			rm := list[i].FoldedUnderMachineID()
			if r == "" || (rm != "" && rm != mid) || !seen[r] {
				continue
			}
			c := localKids[r]
			c.Total++
			switch list[i].State {
			case proto.StateWorking:
				c.Working++
			case proto.StateNeedsYou:
				c.NeedsYou++
			}
			if list[i].FoldedAs() == proto.FoldedAsSide && list[i].State == proto.StateUnread {
				c.SidesUnread++
			}
			localKids[r] = c
		}
	}
	for k, i := range rows {
		root := list[i].FoldedUnder() // the paged list folds it only under a row it lists (roots, parent_session_id)
		rootMachine := list[i].FoldedUnderMachineID()
		if rootMachine == mid {
			rootMachine = ""
		}
		if root != "" {
			if rootMachine == "" {
				if !seen[root] {
					root = ""
				}
			} else {
				if crossParents == nil {
					crossParents = map[string]bool{}
				}
				crossParents[rootMachine] = true
				if !parentsShown.lists(h, rootMachine, root) {
					root, rootMachine = "", ""
				}
			}
		}
		var local proto.SessionChildren
		if localKids != nil {
			local = localKids[list[i].SessionID]
		}
		items[k] = indexedRow(mid, ids[k], &list[i], stamp, manager, root, rootMachine, local, full[list[i].SessionID], items[k])
	}
	// The machine's manager goes with its batch (?manager=<id>, sessionsManager): the one a kept list is stamped with,
	// else the one its rows came stamped with (stamped: every row of a machine carries its manager).
	// Premise: all rows of one machine's list share one manager (a machine has one owner), so the first row names it.
	if !stamp && len(list) > 0 {
		manager = list[0].Manager
	}
	h.listCache.index.ReplaceOwned(mid, manager, items)
	h.listCache.machines[mid] = true
	if len(crossParents) > 0 || len(h.listCache.crossFolded) > 0 {
		h.updateCrossFoldedLocked(mid, crossParents)
		h.reindexCrossDependentsLocked(mid)
	}
}

// shownSessions answers, for one index pass, whether the hub lists a session of another machine as a shown session:
// each machine's shown session ids are read once, on first use, so the pass walks a parent machine's list once rather
// than once for every row folded under it (#15899). The zero value is ready. It lives for one pass and is never kept:
// a parent machine's update re-indexes the machines folded under it (reindexCrossDependentsLocked), whose passes read
// the parent's list again. Caller holds h.sessionsMu.
type shownSessions map[string]map[string]struct{}

// lists reports whether the hub lists (mid, sid) as a shown session (Hub.shown of mid's kept list).
func (p *shownSessions) lists(h *Hub, mid, sid string) bool {
	if mid == "" || sid == "" {
		return false
	}
	ids, read := (*p)[mid]
	if !read {
		if m := h.localMachine(mid); m != nil {
			shown := h.shown(mid, m.keptSessions())
			ids = make(map[string]struct{}, len(shown))
			for i := range shown {
				ids[shown[i].SessionID] = struct{}{}
			}
		}
		if *p == nil {
			*p = shownSessions{}
		}
		(*p)[mid] = ids // nil when the hub has no such machine: it lists none of its sessions
	}
	_, ok := ids[sid]
	return ok
}

// updateCrossFoldedLocked tracks which parent machines child machine mid references across machine boundaries.
// Caller holds h.sessionsMu.
func (h *Hub) updateCrossFoldedLocked(childMid string, parentMachines map[string]bool) {
	for pm, children := range h.listCache.crossFolded {
		if !parentMachines[pm] {
			delete(children, childMid)
			if len(children) == 0 {
				delete(h.listCache.crossFolded, pm)
			}
		}
	}
	if len(parentMachines) == 0 {
		return
	}
	if h.listCache.crossFolded == nil {
		h.listCache.crossFolded = map[string]map[string]bool{}
	}
	for pm := range parentMachines {
		if h.listCache.crossFolded[pm] == nil {
			h.listCache.crossFolded[pm] = map[string]bool{}
		}
		h.listCache.crossFolded[pm][childMid] = true
	}
}

// reindexCrossDependentsLocked refreshes machines whose rows fold under sessions on parentMid.
// Caller holds h.sessionsMu.
func (h *Hub) reindexCrossDependentsLocked(parentMid string) {
	if h.listCache.reindexingCross {
		return
	}
	dependents := h.listCache.crossFolded[parentMid]
	if len(dependents) == 0 {
		return
	}
	h.listCache.reindexingCross = true
	defer func() { h.listCache.reindexingCross = false }()
	var mids []string
	for childMid := range dependents {
		if childMid != parentMid && h.listCache.machines[childMid] {
			mids = append(mids, childMid)
		}
	}
	for _, childMid := range mids {
		if m := h.localMachine(childMid); m != nil {
			h.indexKeptLocked(m)
		}
	}
}

// indexedRow is row's item in the paged index (row stamped with manager when stamp), folded under (rootMachine, root)
// ("" for none; empty rootMachine means mid, sessionlist.Fold.Root / RootMachine), raw its encoding and indexed what
// the index holds under id (the zero Item for none). When indexed's Full is raw with the same pane_source, its Summary
// is what row's is too: the summary is cut from the fields raw encodes. Both are then kept, and the item is unchanged
// for Replace, rather than marshalled again. The scalars are always read from row.
func indexedRow(mid, id string, row *proto.Session, stamp bool, manager, root, rootMachine string, localKids proto.SessionChildren, raw json.RawMessage, indexed sessionlist.Item) sessionlist.Item {
	project := row.RepoRoot
	if project == "" {
		project = row.WorkspaceCwd
	}
	if project == "" {
		project = row.Cwd
	}
	group := row.SpaceID
	if group == "" {
		group = row.WorkspaceID
	}
	pane := sessionorigin.Of(*row)
	// the open to-do it is on: the manager sets row.Todo for an active link only (a done one is completed_todo)
	todo := ""
	if row.Todo != nil {
		todo = row.Todo.ID
	}
	item := sessionlist.Item{
		ID: id, Machine: mid, Project: project, Group: group, PaneSource: pane, HiddenByDirectory: row.HiddenByDirectory(),
		State: row.State, BackgroundWaiting: row.BackgroundWaiting, JustFinished: row.Work != nil && row.Work.JustFinished,
		Source: row.SourceKind, Provider: row.Agent, UpdatedAt: row.UpdatedAt,
		Fold: indexedFold(mid, row, root, rootMachine, todo, localKids, indexed.Fold), Private: spaceSession(row), Assistant: row.Assistant,
		Subagent: row.IsSubagent || row.IsCodexSubagent(),
	}
	// What q finds a row by: the fields the apps' own searches read, the branch too (#4770).
	text := [...]string{row.Title, row.Cwd, row.RepoRoot, row.Branch, row.SpaceName, row.SlotName, row.AgentName, row.LastText}
	if was, ok := indexed.Full.(json.RawMessage); ok && sameIndexedFull(was, raw, pane) {
		item.Summary, item.Full = indexed.Summary, indexed.Full
		if item.SearchText = indexed.SearchText; !joinedBy(item.SearchText, text[:], '\n') {
			item.SearchText = strings.Join(text[:], "\n")
		}
		return item
	}
	cut := runtimeSummary(*row)
	if stamp {
		cut.Manager = manager // what runtimeSummary copies from a stamped row; nothing else of it reads the manager
	}
	summary, _ := json.Marshal(cut)
	item.Summary = json.RawMessage(summary)
	// Reuse the exact legacy encoding, including compatibility keys.
	item.Full = runtimeListFields(raw, struct {
		PaneSource string `json:"pane_source"`
	}{pane})
	item.SearchText = strings.Join(text[:], "\n")
	return item
}

// indexedFold is machine mid's row's optional sessionlist.Fold: its fold under (rootMachine, root) and open to-do, nil
// when it has neither and shows its own state and background wait (a row without children or a to-do), else was when
// that is the same metadata, else a new one. A side session folded under root is searched as root's row (SearchAs,
// #7944). Folds are shared between copies of an item and never written in place.
func foldedStateWith(s *proto.Session, c proto.SessionChildren) string {
	switch {
	case s.State == proto.StateNeedsYou || c.NeedsYou > 0:
		return proto.StateNeedsYou
	case c.Working > 0 && s.State != proto.StateUnread && !(s.State == proto.StateIdle && s.AwaitsReply):
		return proto.StateWorking
	case c.SidesUnread > 0 && s.State != proto.StateWorking:
		return proto.StateUnread
	}
	return s.State
}

func indexedFold(mid string, row *proto.Session, root, rootMachine, todo string, localKids proto.SessionChildren, was *sessionlist.Fold) *sessionlist.Fold {
	if root == "" {
		rootMachine = ""
	}
	kids := row.Children()
	fold := sessionlist.Fold{Root: root, RootMachine: rootMachine, State: row.FoldedState(), Todo: todo, BackgroundWaiting: row.BackgroundWaiting && kids.Working == 0 && kids.NeedsYou == 0}
	if kids.Total > 0 && localKids.Total < kids.Total {
		// Parent rolls up at least one cross-machine child. Legacy roots=1 clients (without
		// cross_machine_hierarchy=1) keep cross-machine children listed separately, so compute
		// the parent's same-machine-only folded state to avoid double-counting needs_you/working.
		fold.HasLegacyFold = true
		fold.LegacyState = foldedStateWith(row, localKids)
		fold.LegacyBackgroundWaiting = row.BackgroundWaiting && localKids.Working == 0 && localKids.NeedsYou == 0
	}
	if root == "" && (row.IsSubagent || row.IsCodexSubagent()) {
		directParent := row.ParentSessionID
		if directParent == "" {
			directParent = row.AskingParentSessionID()
		}
		if directParent == "" && row.RootSessionID != "" && row.RootSessionID != row.SessionID {
			directParent = row.RootSessionID
		}
		if directParent != "" {
			fold.DirectParent = directParent
			if pm := row.ParentMachineID(); pm != "" && pm != mid {
				fold.DirectParentMachine = pm
			}
		}
	}
	if root != "" && rootMachine == "" && row.FoldedAs() == proto.FoldedAsSide {
		fold.SearchAs, fold.SearchSession = runtimeListID(mid, root), row.SessionID
	}
	switch {
	case fold == sessionlist.Fold{State: row.State, BackgroundWaiting: row.BackgroundWaiting}:
		return nil
	case was != nil && *was == fold:
		return was
	}
	kept := fold // only a new fold goes to the heap: &fold would put every row's there
	return &kept
}

// paneSourceKey is what runtimeListFields puts between a row's fields and its pane_source value.
const paneSourceKey = `,"pane_source":"`

// sameIndexedFull reports whether indexed is runtimeListFields(raw, pane_source) byte for byte, without building it.
// A pane source JSON would escape is never taken as the same.
func sameIndexedFull(indexed, raw []byte, pane string) bool {
	head := raw[:len(raw)-1]
	if len(indexed) != len(head)+len(paneSourceKey)+len(pane)+2 || !plainJSONString(pane) {
		return false
	}
	tail := indexed[len(head):]
	return string(tail[len(paneSourceKey):len(tail)-2]) == pane && string(tail[len(tail)-2:]) == `"}` &&
		string(tail[:len(paneSourceKey)]) == paneSourceKey && bytes.Equal(indexed[:len(head)], head)
}

// plainJSONString: json.Marshal writes s between quotes as it is (printable ASCII that is not escaped).
func plainJSONString(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

// joinedBy reports whether s is strings.Join(parts, string(sep)), without joining them.
func joinedBy(s string, parts []string, sep byte) bool {
	for i, p := range parts {
		if i > 0 {
			if s == "" || s[0] != sep {
				return false
			}
			s = s[1:]
		}
		if !strings.HasPrefix(s, p) {
			return false
		}
		s = s[len(p):]
	}
	return s == ""
}

// The JSON tuple is unambiguous even if a runtime id contains slashes. Clients
// form the same key from machine_id/session_id when applying delta removals.
func runtimeListID(mid, sid string) string {
	if plainJSONString(mid) && plainJSONString(sid) {
		return `["` + mid + `","` + sid + `"]` // json.Marshal's bytes, in one allocation rather than four
	}
	b, _ := json.Marshal([2]string{mid, sid})
	return string(b)
}

// startErrorRow is how much of s's start_error a row keeps: a coded refusal's whole sentence (the manager's, in the user's
// language, its remedy last: #1642), else a summary's.
func startErrorRow(s proto.Session) int {
	if s.StartErrorCode != "" {
		return proto.CodedStartErrorMax
	}
	return 180
}

func rowText(s string, max int) string {
	// A whole-list answer cuts every long row (http.go writeSessionLists): a rune copy of each whole text was most of
	// what that answer allocated, so cut without one whenever the bytes cannot hold more than max runes.
	if len(s) <= max || utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}

// legacyListCut bounds a whole-list answer's row in place (http.go writeSessionLists): the legacy keys only, none of
// the summary's additions (pane_source, the counts scalars), but bounded like one — the long texts cut and the task
// arrays (subagents, background, workflows, queued, ports) left out, what the chat routes serve. s's pointer fields
// stay untouched: only scalars and slices change, and writeSessionLists encodes through a scratch copy.
func legacyListCut(s *proto.Session) {
	s.Title = rowText(s.Title, proto.TitleMax)
	s.LastText = rowText(s.LastText, 180)
	s.Activity = rowText(s.Activity, 120)
	s.StartError = rowText(s.StartError, startErrorRow(*s))
	s.ReadOnlyReason = rowText(s.ReadOnlyReason, 180)
	s.TurnError = rowText(s.TurnError, 180)
	s.Suggestion = rowText(s.Suggestion, 180)
	if s.Work != nil {
		s.Work.Line = rowText(s.Work.Line, 180)
		s.Work.Wait = rowText(s.Work.Wait, 180)
		s.Work.Then = rowText(s.Work.Then, 180)
		if s.Work.Closeout != nil {
			s.Work.Closeout.Waiting = rowText(s.Work.Closeout.Waiting, 180)
			s.Work.Closeout.Missing = rowText(s.Work.Closeout.Missing, 180)
		}
	}
	if s.Todo != nil {
		s.Todo.Title = rowText(s.Todo.Title, 256)
	}
	if s.Plan != nil {
		s.Plan.Current = rowText(s.Plan.Current, 180)
	}
	if s.Goal != nil {
		s.Goal.Objective = rowText(s.Goal.Objective, 180)
		s.Goal.Reason = rowText(s.Goal.Reason, 180)
	}
	if s.Loop != nil {
		s.Loop.Prompt = ""
		s.Loop.Reason = rowText(s.Loop.Reason, 120)
	}
	s.Queued = nil
	s.Background = nil
	s.SetBackgroundEnded(nil)
	s.Subagents = nil
	s.Workflows = nil
	s.Ports = nil
}

// runtimeSummary deliberately excludes transcript/task arrays and chat-only
// configuration. The normal info/items routes remain the source for chat detail.
type runtimeRowSummary struct {
	proto.Session
	PaneSource       string `json:"pane_source"`
	Waiting          bool   `json:"waiting"`
	BackgroundCount  int    `json:"background_count,omitempty"`
	RunningSubagents int    `json:"running_subagents,omitempty"`
	RunningWorkflows int    `json:"running_workflows,omitempty"`
	// Independent count: background_count is the legacy raw count and can include workflows.
	BackgroundTasksCount int             `json:"background_tasks_count"`
	WorkflowProgress     *proto.Workflow `json:"workflow_progress,omitempty"`
}

// runtimeListFields joins two encoded objects. Embedding proto.Session in a
// wrapper directly would promote its compatibility MarshalJSON and hide all
// sibling fields. Both objects here are produced by our own JSON encoders.
func runtimeListFields(session json.RawMessage, fields any) json.RawMessage {
	extra, _ := json.Marshal(fields)
	out := make([]byte, 0, len(session)+len(extra))
	out = append(out, session[:len(session)-1]...)
	out = append(out, ',')
	out = append(out, extra[1:]...)
	return out
}

func (s runtimeRowSummary) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(s.Session)
	if err != nil {
		return nil, err
	}
	return runtimeListFields(raw, struct {
		PaneSource           string          `json:"pane_source"`
		Waiting              bool            `json:"waiting"`
		BackgroundCount      int             `json:"background_count,omitempty"`
		RunningSubagents     int             `json:"running_subagents,omitempty"`
		RunningWorkflows     int             `json:"running_workflows,omitempty"`
		BackgroundTasksCount int             `json:"background_tasks_count"`
		WorkflowProgress     *proto.Workflow `json:"workflow_progress,omitempty"`
	}{s.PaneSource, s.Waiting, s.BackgroundCount, s.RunningSubagents, s.RunningWorkflows, s.BackgroundTasksCount, s.WorkflowProgress}), nil
}

// MarshalJSONTo shadows the promoted (*proto.Session).MarshalJSONTo, which encoding/json prefers for an addressable
// summary (a pointer, a slice element) and which would drop the fields beside the session.
func (s *runtimeRowSummary) MarshalJSONTo(enc *jsontext.Encoder) error {
	b, err := s.MarshalJSON()
	if err != nil {
		return err
	}
	return enc.WriteValue(b)
}

func runtimeSummary(s proto.Session) runtimeRowSummary {
	out := proto.Session{
		Manager: s.Manager, MachineID: s.MachineID, SessionID: s.SessionID,
		Title: rowText(s.Title, proto.TitleMax), Cwd: s.Cwd, WorkspaceCwd: s.WorkspaceCwd, RepoRoot: s.RepoRoot, Worktree: s.Worktree, Branch: s.Branch,
		Status: s.Status, State: s.State, UpdatedAt: s.UpdatedAt, StateSince: s.StateSince, SeenAt: s.SeenAt,
		LastText: rowText(s.LastText, 180), Activity: rowText(s.Activity, 120), ActivityAt: s.ActivityAt,
		AwaitsReply: s.AwaitsReply, BackgroundWorking: s.BackgroundWorking, BackgroundWaiting: s.BackgroundWaiting, BackgroundStalledSince: s.BackgroundStalledSince,
		Agent: s.Agent, AgentLabel: s.AgentLabel, AgentName: s.AgentName, AgentSurface: s.AgentSurface,
		SourceKind: s.SourceKind, SourceThreadID: s.SourceThreadID, SourceInstanceID: s.SourceInstanceID,
		ParentThreadID: s.ParentThreadID, ParentSessionID: s.ParentSessionID, RootSessionID: s.RootSessionID, IsSubagent: s.IsSubagent,
		Capabilities: s.Capabilities, Attached: s.Attached, CanFork: s.CanFork, BypassPending: s.BypassPending,
		SpaceKind: s.SpaceKind, SpaceID: s.SpaceID, SpaceName: s.SpaceName, SpaceIndex: s.SpaceIndex,
		ParentSpaceID: s.ParentSpaceID, SlotID: s.SlotID, SlotName: s.SlotName, SlotIndex: s.SlotIndex, PaneIndex: s.PaneIndex,
		WorkspaceID: s.WorkspaceID, TabID: s.TabID, PaneID: s.PaneID,
		Assistant: s.Assistant, Archived: s.Archived, ArchiveChangedAt: s.ArchiveChangedAt, Space: s.Space, ModelPick: s.ModelPick,
		Starting: s.Starting, StartError: rowText(s.StartError, startErrorRow(s)), StartedByPersona: s.StartedByPersona,
		StartErrorCode: s.StartErrorCode, StartErrorArgs: s.StartErrorArgs,
		ReadOnlyReason: rowText(s.ReadOnlyReason, 180), Origin: s.Origin, OriginRef: s.OriginRef, OpenedBy: s.OpenedBy,
		TurnError: rowText(s.TurnError, 180), TurnErrorKind: s.TurnErrorKind, TurnErrorCode: s.TurnErrorCode,
		TurnErrorResetsAt: s.TurnErrorResetsAt, TurnErrorResumes: s.TurnErrorResumes,
		PendingInteractions: s.PendingInteractions, Rev: s.Rev, Quiet: s.Quiet,
		Model: s.Model, RequestedModel: s.RequestedModel, Effort: s.Effort, PendingEffort: s.PendingEffort, EffortChangeFailed: s.EffortChangeFailed,
		ContextTokens: s.ContextTokens, ContextWindow: s.ContextWindow,
		ContextRestartRecommended: s.ContextRestartRecommended,
		Hierarchy:                 s.Hierarchy, // folded_under, children, started_at: the child cards and the parent's state (#5120)
	}
	out.SetBackgroundEnded(nil)
	if s.Work != nil {
		out.Work = &proto.WorkState{Phase: s.Work.Phase, On: s.Work.On, Line: rowText(s.Work.Line, 180),
			Wait: rowText(s.Work.Wait, 180), Then: rowText(s.Work.Then, 180), WakeAt: s.Work.WakeAt, ResetsAt: s.Work.ResetsAt,
			Attempts: s.Work.Attempts, Max: s.Work.Max, Deferred: s.Work.Deferred, JustFinished: s.Work.JustFinished, Agent: s.Work.Agent}
		if c := s.Work.Closeout; c != nil { // the row's part of it: where it stands, not the account of what it did
			out.Work.Closeout = &proto.Closeout{Status: c.Status, Merged: c.Merged, Deployed: c.Deployed,
				Waiting: rowText(c.Waiting, 180), Missing: rowText(c.Missing, 180), Checked: c.Checked}
		}
	}
	if s.Todo != nil {
		out.Todo = &proto.TodoRef{ID: s.Todo.ID, Title: rowText(s.Todo.Title, 256)}
	}
	if done := s.CompletedTodo(); done != nil {
		out.SetCompletedTodo(&proto.TodoRef{ID: done.ID, Title: rowText(done.Title, 256)})
	}
	if s.Plan != nil {
		cp := *s.Plan
		cp.Current = rowText(cp.Current, 180)
		out.Plan = &cp
	}
	if s.Goal != nil {
		cp := *s.Goal
		cp.Objective = rowText(cp.Objective, 180)
		cp.Reason = rowText(cp.Reason, 180)
		out.Goal = &cp
	}
	if s.Loop != nil {
		cp := *s.Loop
		cp.Prompt = ""
		cp.Reason = rowText(cp.Reason, 120)
		out.Loop = &cp
	}
	out.Host = s.Host
	row := runtimeRowSummary{Session: out, PaneSource: sessionorigin.Of(s), BackgroundCount: len(s.Background)}
	for _, sub := range s.Subagents {
		if sub.Running {
			row.RunningSubagents++
		}
	}
	for _, task := range s.Background {
		if task.Kind != "workflow" && task.Kind != "agent" {
			row.BackgroundTasksCount++
		}
	}
	for _, flow := range s.Workflows {
		if flow.Running {
			row.RunningWorkflows++
			if row.WorkflowProgress == nil {
				// A single bounded projection, newest running run first: no agent, phase or tool arrays.
				row.WorkflowProgress = &proto.Workflow{
					TaskID: flow.TaskID, Name: rowText(flow.Name, 256), Summary: rowText(flow.Summary, 180),
					Status: flow.Status, Running: true, Phase: rowText(flow.Phase, 120),
					PhaseIndex: flow.PhaseIndex, PhaseCount: flow.PhaseCount,
					AgentsStarted: flow.AgentsStarted, AgentsDone: flow.AgentsDone, AgentsRunning: flow.AgentsRunning,
					StartedAt: flow.StartedAt,
				}
				if row.WorkflowProgress.PhaseCount == 0 {
					row.WorkflowProgress.PhaseCount = len(flow.Phases)
				}
			}
		}
	}
	// Legacy runtimes can report only a background entry for a workflow or subagent.
	if len(s.Workflows) == 0 {
		for _, task := range s.Background {
			if task.Kind == "workflow" {
				row.RunningWorkflows++
			}
		}
	}
	if len(s.Subagents) == 0 {
		for _, task := range s.Background {
			if task.Kind == "agent" {
				row.RunningSubagents++
			}
		}
	}
	row.Waiting = s.Loop != nil || row.BackgroundCount > 0 || row.RunningSubagents > 0 || row.RunningWorkflows > 0 ||
		(s.Work != nil && s.Work.Phase == "blocked" && s.Work.On != "user" &&
			(s.Work.WakeAt != nil || (s.Work.On == "external" && s.Work.Wait == "" && s.Work.Agent == nil)))
	return row
}

// sessionsManager is the manager GET /api/v1/sessions is asked for with ?manager=<id> (featureSessionsManager): only
// the sessions on that manager's computers are listed and counted. "" when the request has no such key: every
// manager's, as before. Only the query names it here, not the X-Persona-Manager header an app may send with every
// request: this list was never routed by it. False once it answered: the key given twice or empty (400
// invalid_query), a manager the roster does not have (404 no_manager), an extension's token naming another manager
// than its own (403 other_manager).
func (h *Hub) sessionsManager(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, asked := r.URL.Query()[ManagerQuery]
	if !asked {
		return "", true
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		sessionListErr(w, errors.New("manager must have exactly one nonempty value"))
		return "", false
	}
	named := strings.TrimSpace(values[0])
	if em := extensionManager(r); em != "" && named != em {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
		return "", false
	}
	if !h.knownManager(r.Context(), named) {
		writeErrCode(w, http.StatusNotFound, "no_manager", fmt.Errorf("%w: %q", errUnknownManager, clip(named, 64)))
		return "", false
	}
	return named, true
}

func (h *Hub) pagedSessions(w http.ResponseWriter, r *http.Request, manager string) {
	opts, err := sessionlist.Parse(r.URL.Query())
	if err != nil {
		sessionListErr(w, err)
		return
	}
	opts.Manager = manager
	opts.NoPrivate = h.spacesHidden(r) // external Agent clients cannot see a space's conversations, but the user's own extension (#13246)
	if values, lookup := r.URL.Query()["session_id"]; lookup {
		if len(values) != 1 || values[0] == "" || opts.Machine == "" || opts.Since != "" || opts.Cursor != "" {
			sessionListErr(w, errors.New("exact session lookup requires one nonempty session_id and machine_id, without since or cursor"))
			return
		}
		opts.ID = runtimeListID(opts.Machine, values[0])
		// Match the legacy machine-specific ownership check before initializing
		// the index; missing/foreign machines must not weaken token boundaries.
		if em := extensionManager(r); em != "" && h.machineOwner(opts.Machine) != em {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
	}
	index := h.sessionListIndex()
	// Machine disappearance does not always carry a final sessions event.
	h.pruneSessionListMachines()
	// An extension's token: its manager's machines only (ownership.go), as if it had asked for those.
	own, limited := h.extensionMachines(r)
	if limited {
		if opts.Machine != "" && !slices.Contains(own, opts.Machine) {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		if opts.UnderMachine != "" && !slices.Contains(own, opts.UnderMachine) {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		childMachine := opts.Machine
		if opts.Under != "" && opts.UnderMachine == "" && opts.CrossMachineHierarchy {
			childMachine = ""
		}
		if childMachine == "" && len(own) == 1 {
			if opts.Under != "" && opts.UnderMachine == "" && opts.CrossMachineHierarchy {
				opts.UnderMachine = opts.Machine
			}
			opts.Machine = own[0]
			childMachine = own[0]
		}
		limited = childMachine == ""
	}
	if opts.Search != "" {
		// Search authorizes before matching and freezing totals/evidence. Never
		// apply ownPage after pagination: foreign hits would already be counted.
		if em := extensionManager(r); em != "" {
			opts.Manager = em
			opts.AllowedMachines = append([]string{}, own...)
		}
		if opts.Cursor == "" {
			opts.SearchContext, err = h.runtimeSearchContext(r, opts.Search)
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, err)
				return
			}
		}
		page, err := index.Query(opts)
		if err != nil {
			sessionListErr(w, err)
			return
		}
		// A successful empty search still identifies this contract to new clients.
		writeJSON(w, struct {
			sessionlist.Page
			Hits map[string]sessionlist.SearchHit `json:"search_hits"`
		}{page, page.SearchHits})
		return
	}
	if limited && opts.PaneSource != "" {
		// The legacy ownership projection recomputes totals outside the frozen view.
		// Require one authorized machine for this stricter origin contract.
		sessionListErr(w, errors.New("pane_source requires machine_id for a manager-scoped aggregate"))
		return
	}
	if catalogAsked(r) {
		catalog, err := h.sessionCatalogOf(r.Context(), index, opts, own, limited, !restrictedAgentData(r))
		if err != nil {
			sessionListErr(w, err)
			return
		}
		writeJSON(w, catalog)
		return
	}
	if counts := r.URL.Query().Get("counts"); counts == "1" || counts == "true" {
		counts, err := h.countsWithHeld(index, opts, own, limited)
		if err != nil {
			sessionListErr(w, err)
			return
		}
		writeJSON(w, counts)
		return
	}
	page, err := index.Query(opts)
	if err != nil {
		sessionListErr(w, err)
		return
	}
	if limited {
		if page, err = ownPage(index, opts, own, page); err != nil {
			sessionListErr(w, err)
			return
		}
	}
	if with := r.URL.Query().Get("with_counts"); with == "1" || with == "true" {
		o := opts
		o.Since, o.Cursor = "", "" // current membership, as the separate counts read asks for it
		counts, err := h.countsWithHeld(index, o, own, limited)
		if err != nil {
			sessionListErr(w, err)
			return
		}
		writeJSON(w, sessionlist.PageWithCounts{Page: page, Counts: counts})
		return
	}
	writeJSON(w, page)
}

// extensionMachines are the machines of the manager whose extension's token r carries, and whether that limits what
// it is shown: not for any other credential, nor while every machine is that manager's (one manager: as before).
func (h *Hub) extensionMachines(r *http.Request) (own []string, limited bool) {
	em := extensionManager(r)
	if em == "" {
		return nil, false
	}
	for _, m := range h.machinesNow() {
		if h.machineOwner(m.cfg.ID) == em {
			own = append(own, m.cfg.ID)
		} else {
			limited = true
		}
	}
	return own, limited
}

// supervisorHeldCard: a card its task's supervisor holds (assistant/ask_route.go routeAsk): marked delegating and no
// question (only a question is ever delegated to the assistant's own decision, which stays counted).
func supervisorHeldCard(ix proto.Interaction) bool {
	return ix.Delegating && ix.Kind != proto.IxQuestion
}

// heldNeedsIDs are the list ids of the sessions whose pending cards are all held: held, only P1/P2 cards for push
// grading (no supervisor's among them counted either way); supervised, only cards their supervisor holds.
// When crossMachine is false (legacy client without cross_machine_hierarchy=1), cross-machine folded children stay
// listed on their own machine, so their cards are credited to their own row rather than a parent on another machine.
func (h *Hub) heldNeedsIDs(crossMachine bool) (held, supervised []string) {
	interactions := h.pendingInteractions()
	if len(interactions) == 0 {
		return nil, nil
	}
	type flags struct {
		hasP0      bool
		hasHeld    bool
		supervisor bool // a card its supervisor holds
		other      bool // any card that is not
	}
	byHome := map[[2]string]flags{}
	for _, ix := range interactions {
		if ix.MirrorOf != "" {
			continue
		}
		home := ix.FoldedUnder
		homeMachine := ix.FoldedUnderMachineID
		if home == "" || (!crossMachine && homeMachine != "" && homeMachine != ix.MachineID) {
			home = ix.SessionID
			homeMachine = ix.MachineID
		} else if homeMachine == "" {
			homeMachine = ix.MachineID
		}
		if home == "" {
			continue
		}
		k := [2]string{homeMachine, home}
		st := byHome[k]
		switch {
		case supervisorHeldCard(ix):
			st.supervisor = true
		case ix.Delegating:
			st.other = true
		case ix.PushLevel == assistantwire.LevelP1 || ix.PushLevel == assistantwire.LevelP2:
			st.hasHeld, st.other = true, true
		default:
			st.hasP0, st.other = true, true
		}
		byHome[k] = st
	}
	for k, st := range byHome {
		switch {
		case st.supervisor && !st.other:
			supervised = append(supervised, runtimeListID(k[0], k[1]))
		case st.hasHeld && !st.hasP0:
			held = append(held, runtimeListID(k[0], k[1]))
		}
	}
	return held, supervised
}

func (h *Hub) countsWithHeld(index *sessionlist.Index, opts sessionlist.Options, own []string, limited bool) (sessionlist.Counts, error) {
	var (
		counts sessionlist.Counts
		err    error
	)
	if limited {
		counts, err = ownCounts(index, opts, own)
	} else {
		counts, err = index.Counts(opts)
	}
	if err != nil {
		return sessionlist.Counts{}, err
	}
	held, supervised := h.heldNeedsIDs(opts.CrossMachineHierarchy)
	if counts.State[proto.StateNeedsYou] == 0 {
		return counts, nil
	}
	count := func(ids []string) (int, error) {
		if len(ids) == 0 {
			return 0, nil
		}
		if !limited {
			return index.CountMatchingState(opts, proto.StateNeedsYou, ids)
		}
		n := 0
		for _, mid := range own {
			o := opts
			if o.Under != "" && o.UnderMachine == "" && o.CrossMachineHierarchy {
				o.UnderMachine = o.Machine
			}
			o.Machine, o.Cursor = mid, ""
			c, err := index.CountMatchingState(o, proto.StateNeedsYou, ids)
			if err != nil {
				return 0, err
			}
			n += c
		}
		return n, nil
	}
	if counts.HeldNeeds, err = count(held); err != nil {
		return sessionlist.Counts{}, err
	}
	if counts.SupervisorHeld, err = count(supervised); err != nil {
		return sessionlist.Counts{}, err
	}
	return counts, nil
}

// ownCounts are the counts of opts over the machines own only (each machine's, added up).
func ownCounts(index *sessionlist.Index, opts sessionlist.Options, own []string) (sessionlist.Counts, error) {
	out := sessionlist.Counts{Machine: map[string]int{}, Project: map[string]int{}, Group: map[string]int{}, State: map[string]int{}}
	if opts.Roots {
		out.ActiveAgentsByMachine = make(map[string]int)
	}
	for _, mid := range own {
		o := opts
		if o.Under != "" && o.UnderMachine == "" && o.CrossMachineHierarchy {
			o.UnderMachine = o.Machine
		}
		o.Machine, o.Cursor = mid, "" // current membership (a cursor's frozen view is of every machine)
		c, err := index.Counts(o)
		if err != nil {
			return sessionlist.Counts{}, err
		}
		out.Total += c.Total
		out.Revision = c.Revision
		for dst, src := range map[*map[string]int]map[string]int{&out.Machine: c.Machine, &out.ActiveAgentsByMachine: c.ActiveAgentsByMachine, &out.Project: c.Project, &out.Group: c.Group, &out.State: c.State} {
			for k, n := range src {
				(*dst)[k] += n
			}
		}
	}
	return out, nil
}

// ownPage is page without the sessions and removals of machines not in own; its total is own's.
func ownPage(index *sessionlist.Index, opts sessionlist.Options, own []string, page sessionlist.Page) (sessionlist.Page, error) {
	keep := func(mid string) bool { return slices.Contains(own, mid) }
	page.Sessions = slices.DeleteFunc(page.Sessions, func(v any) bool {
		raw, _ := json.Marshal(v)
		var s struct {
			MachineID string `json:"machine_id"`
		}
		return json.Unmarshal(raw, &s) != nil || !keep(s.MachineID)
	})
	page.Remove = slices.DeleteFunc(page.Remove, func(id string) bool {
		var k [2]string
		return json.Unmarshal([]byte(id), &k) != nil || !keep(k[0])
	})
	underMachine := opts.UnderMachine
	if opts.Under != "" && underMachine == "" && opts.CrossMachineHierarchy {
		underMachine = opts.Machine
	}
	counts, err := ownCounts(index, sessionlist.Options{Since: opts.Since, State: opts.State, Project: opts.Project, Group: opts.Group,
		Source: opts.Source, Provider: opts.Provider, Q: opts.Q, Manager: opts.Manager, Roots: opts.Roots, CrossMachineHierarchy: opts.CrossMachineHierarchy, Under: opts.Under, UnderMachine: underMachine, NoPrivate: opts.NoPrivate, AgentsOnly: opts.AgentsOnly, Places: opts.Places, Spaces: opts.Spaces}, own)
	if err != nil {
		return sessionlist.Page{}, err
	}
	page.Total = counts.Total
	return page, nil
}

// pruneSessionListMachines scans only machine ids, never session rows. Called
// before machine broadcasts as well as requests, so removals wake list clients.
func (h *Hub) pruneSessionListMachines() bool {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if h.listCache.machines == nil {
		return false
	}
	present := map[string]bool{}
	for _, m := range h.machinesNow() {
		present[m.cfg.ID] = true
	}
	changed := false
	for mid := range h.listCache.machines {
		if !present[mid] {
			h.listCache.index.RemoveSource(mid)
			delete(h.listCache.machines, mid)
			delete(h.listCache.unaligned, mid)
			if len(h.listCache.crossFolded) > 0 {
				h.updateCrossFoldedLocked(mid, nil)
				h.reindexCrossDependentsLocked(mid)
				delete(h.listCache.crossFolded, mid)
			}
			changed = true
		}
	}
	return changed
}

// reindexSessions refreshes m's paged rows after a change that sends no legacy
// list (sessions.revs): the "revs" broadcast updates whole-list apps, but paged
// readers only ever see what the index holds.
func (h *Hub) reindexSessions(m *Machine) {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if !h.listCache.machines[m.cfg.ID] {
		return // no paginated reader yet, or the machine was pruned
	}
	before := h.listCache.index.Revision()
	h.indexKeptLocked(m)
	revision := h.listCache.index.Revision()
	if revision == before {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var hint *listHint // worked out for the first app that lists, as broadcastSessions does: none may
	for c := range h.clients {
		if c.listMode {
			if hint == nil {
				x := h.sessionListHint(before, revision)
				hint = &x
			}
			hint.send(c)
		}
	}
}

// sessionListChangedMax bounds a hint's `changed`: past it the hint goes without, and an app reads as if it did not
// know which rows changed.
const sessionListChangedMax = 64

// listHint is one sessions_list hint, encoded once for every app it goes to: plain, and with `since` and `changed`
// (nil when the index's journal cannot say which ids changed, or there are too many).
type listHint struct{ plain, detailed []byte }

// sessionListHint is the hint for the index's change from revision before to revision: `changed` holds the ids changed
// after before up to revision, the ["<machine>","<session>"] tuples a page's `remove` holds. A change made by
// another pass in between is in it too, which only makes an app read once more.
func (h *Hub) sessionListHint(before, revision string) listHint {
	// Written by hand, keys in json.Marshal's order for a map: no encoder state to set up for each hint (#2809's
	// allocation ceilings, TestReindexOneRevAllocation).
	plain := appendJSONID(append(make([]byte, 0, 64), `{"revision":`...), revision)
	hint := listHint{plain: append(plain, `,"type":"sessions_list"}`...)}
	ids, ok := h.listCache.index.Changed(before, revision, sessionListChangedMax)
	if !ok {
		return hint
	}
	detailed := []byte(`{"changed":[`)
	for i, id := range ids {
		if !json.Valid([]byte(id)) {
			return hint
		}
		if i > 0 {
			detailed = append(detailed, ',')
		}
		detailed = append(detailed, id...)
	}
	detailed = appendJSONID(append(detailed, `],"revision":`...), revision)
	detailed = appendJSONID(append(detailed, `,"since":`...), before)
	hint.detailed = append(detailed, `,"type":"sessions_list"}`...)
	return hint
}

// send queues the hint for c: with since and changed when c asked for them in its URL and is not restricted from
// space conversations (which must not see a space conversation's id change either).
func (hint listHint) send(c *client) {
	if hint.detailed != nil && c.urlFeatures[featureSessionsListChanges] && c.role != roleExtension && c.role != roleReadOnly && c.role != roleScoped {
		c.queue(hint.detailed)
		return
	}
	c.queue(hint.plain)
}

func (h *Hub) broadcastSessionListNotice() {
	revision := h.listCache.index.Revision()
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.listMode {
			c.sendJSON(map[string]any{"type": "sessions_list", "revision": revision})
		}
	}
}

func sessionListErr(w http.ResponseWriter, err error) {
	code, status := "invalid_query", http.StatusBadRequest
	var typed *sessionlist.Error
	if errors.As(err, &typed) {
		code = typed.Code
		if code == "cursor_expired" || code == "revision_expired" {
			status = http.StatusConflict
		}
	}
	writeErrCode(w, status, code, i18n.Err("session-list."+code))
}

// A paginated subscriber gets no whole list, including on reconnect. The HTTP
// page/revision is authoritative; this event only wakes a delta/count request.
func (h *Hub) sessionListNotice(c *client) {
	c.sendJSON(map[string]any{"type": "sessions_list", "revision": h.sessionListIndex().Revision()})
}
