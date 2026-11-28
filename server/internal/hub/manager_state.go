package hub

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the hub keeps of each manager (docs/assistant/managers.md "What the hub keeps of each manager"): its snapshot (its
// conversations, tasks, profile, todo links), its tools, its extensions' report and its Live Activity content. The
// assistant the hub starts keeps its state where it always did (h.assistant, h.ext.report, h.reported.live); every
// other manager has one of its own here, made when it first reports and dropped when it is removed. Nothing a manager
// reports replaces another's. Where the hub needs one of a thing for itself:
//
//   - its profile (the user's time zone, where skills go): the requested manager's, else the default's, falling back
//     to the built-in's until it reports one. Only the built-in's is kept in the database (assistant_profile.go);
//   - the Live Activity's content: the default manager's, else the assistant's;
//   - the extensions' report (features, prefixes, looks, each machine's extras): every manager's merged, a machine's
//     extras from its own manager, a prefix served by the first manager that reports it (the assistant's first).

// managerStates are the managers' states but the assistant's, by id.
type managerStates struct {
	mu sync.Mutex
	by map[string]*assistantState
}

// assistantOf is manager id's state ("" and the assistant's id: h.assistant).
func (h *Hub) assistantOf(id string) *assistantState {
	if id == "" || id == assistantwire.AssistantID {
		return &h.assistant
	}
	s := &h.others
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]*assistantState{}
	}
	a := s.by[id]
	if a == nil {
		a = &assistantState{}
		s.by[id] = a
	}
	return a
}

// managerStateIDs are the managers with a state: the assistant first, then the others by id.
func (h *Hub) managerStateIDs() []string {
	s := &h.others
	s.mu.Lock()
	out := make([]string, 0, len(s.by)+1)
	for id := range s.by {
		out = append(out, id)
	}
	s.mu.Unlock()
	sort.Strings(out)
	return append([]string{assistantwire.AssistantID}, out...)
}

// forgetManagerState drops what the hub keeps of removed manager id (its conversations, tools, report, live and
// pending cards: the apps are told those are over, as nothing will ever answer them).
func (h *Hub) forgetManagerState(id string) {
	if id == "" || id == assistantwire.AssistantID {
		return
	}
	s := &h.others
	s.mu.Lock()
	delete(s.by, id)
	s.mu.Unlock()
	h.takeTodoReport(id, &assistantwire.TodoReport{})
	h.ext.reportsMu.Lock()
	delete(h.ext.reports, id)
	h.ext.reportsMu.Unlock()
	f := &h.reported
	f.mu.Lock()
	delete(f.lives, id)
	var gone []proto.Interaction
	for ixID, ix := range f.interactions {
		if ix.Manager == id {
			gone = append(gone, ix)
			delete(f.interactions, ixID)
		}
	}
	f.mu.Unlock()
	for _, ix := range gone {
		h.broadcastInteractionResolved(ix, interactionResolvedEvent(ix))
	}
	h.forgetMachineEntries(id) // what it reported of its machines (machine_entries.go)
}

// conversation is the manager whose own conversation sid is, and how its snapshot lists it; "" for none. A session
// is its machine's manager's (docs/assistant/managers.md "Ownership"): a snapshot counts only on a machine of its own manager's,
// so listing another manager's session takes nothing, and a view naming no machine (an older row) counts for none.
func (h *Hub) conversation(sid string) (string, assistantwire.SessionView) {
	if sid == "" {
		return "", assistantwire.SessionView{}
	}
	for _, id := range h.managerStateIDs() {
		a := h.assistantOf(id)
		a.mu.RLock()
		v, ok := a.sessions[sid]
		a.mu.RUnlock()
		if !ok {
			continue
		}
		if h.mayClaim(id, v.MachineID) {
			return id, v
		}
	}
	return "", assistantwire.SessionView{}
}

// mayClaim: manager id may count a session on machine mid as its own conversation: the machine is its own. Nothing a
// manager reports about itself makes another's machine or session its.
func (h *Hub) mayClaim(id, mid string) bool {
	return mid != "" && h.machineOwner(mid) == id
}

// mainOf: session on machine is some manager's main conversation, as it reported (the assistant's as the hub kept
// it), on a machine of that manager's own.
func (h *Hub) mainOf(machine, session string) bool {
	if p := h.assistantProfile(); p.Main != "" && p.Main == session && p.MainRouteMachine() == machine && h.mayClaim(assistantwire.AssistantID, machine) {
		return true
	}
	for _, id := range h.managerStateIDs()[1:] {
		a := h.assistantOf(id)
		a.mu.RLock()
		p := a.profile
		a.mu.RUnlock()
		if p != nil && p.Main != "" && p.Main == session && p.MainRouteMachine() == machine && h.mayClaim(id, machine) {
			return true
		}
	}
	return false
}

// isMainForManager reports whether session on machine is manager's own main conversation
// on a machine owned by manager.
func (h *Hub) isMainForManager(manager, machine, session string) bool {
	if session == "" || machine == "" {
		return false
	}
	mgr := normalizeGradingManager(manager)
	var p *assistantwire.Profile
	if mgr == assistantwire.AssistantID {
		prof := h.assistantProfile()
		p = &prof
	} else {
		a := h.assistantOf(mgr)
		a.mu.RLock()
		p = a.profile
		a.mu.RUnlock()
	}
	return p != nil && p.Main != "" && p.Main == session && p.MainRouteMachine() == machine && h.mayClaim(mgr, machine)
}

// managerTools are manager id's tools (not the assistant's: assistantToolsNow), asked for the first time they are
// needed after it connected and kept while it is away.
func (h *Hub) managerTools(ctx context.Context, id string) []proto.Tool {
	a := h.assistantOf(id)
	a.toolsMu.Lock()
	tools, fresh := a.tools, a.toolsFresh
	a.toolsMu.Unlock()
	if fresh || h.busConnOf(id) == nil {
		return tools
	}
	res, err := buswire.Call(ctx, h.managerCaller(id), assistantwire.ToolsMethod, assistantwire.SessionRef{})
	if err != nil {
		h.logf("assistant: [%s] tools: %v", id, err)
		return tools
	}
	a.toolsMu.Lock()
	a.tools, a.toolsFresh = res.Tools, true
	a.toolsMu.Unlock()
	return res.Tools
}

// managerReconnected: manager id (not the assistant) said its handshake: its tools are asked for again when needed.
func (h *Hub) managerReconnected(id string) {
	a := h.assistantOf(id)
	a.toolsMu.Lock()
	a.toolsFresh = false
	a.toolsMu.Unlock()
}

// ---- the extensions' reports ----

// extReportOf is manager id's extensions' report (nil: none yet).
func (h *Hub) extReportOf(id string) *extwire.Report {
	if id == assistantwire.AssistantID {
		return h.ext.report.Load()
	}
	h.ext.reportsMu.Lock()
	defer h.ext.reportsMu.Unlock()
	return h.ext.reports[id]
}

// keepExtReport keeps manager id's report; the one it replaces.
func (h *Hub) keepExtReport(id string, r *extwire.Report) *extwire.Report {
	if id == assistantwire.AssistantID {
		return h.ext.report.Swap(r)
	}
	h.ext.reportsMu.Lock()
	defer h.ext.reportsMu.Unlock()
	if h.ext.reports == nil {
		h.ext.reports = map[string]*extwire.Report{}
	}
	old := h.ext.reports[id]
	h.ext.reports[id] = r
	return old
}

// extReport is every manager's extensions' report merged (see the top of the file).
func (h *Hub) extReport() extwire.Report {
	out := extwire.Report{Machines: map[string]extwire.MachineExtras{}}
	for _, id := range h.extReporters() {
		r := h.extReportOf(id)
		if r == nil {
			continue
		}
		for _, f := range r.Features {
			if !slices.Contains(out.Features, f) {
				out.Features = append(out.Features, f)
			}
		}
		for _, a := range r.APIs {
			if !slices.ContainsFunc(out.APIs, func(b extwire.APIPrefix) bool { return b.Prefix == a.Prefix }) {
				out.APIs = append(out.APIs, a)
			}
		}
		for mid, x := range r.Machines {
			if h.machineOwner(mid) == id { // a machine's extras are its manager's only (ownership.go)
				out.Machines[mid] = x
			}
		}
		out.ScreenKeepAwake = out.ScreenKeepAwake || r.ScreenKeepAwake
		before := slices.Clone(out.Looks) // an extension's looks come from the first manager that has it on
		for _, l := range r.Looks {
			if !slices.ContainsFunc(before, func(o extwire.LookPackage) bool { return o.Extension == l.Extension }) {
				out.Looks = append(out.Looks, l)
			}
		}
	}
	return out
}

// extReporters are the managers with a report: the assistant first, then the others by id.
func (h *Hub) extReporters() []string {
	h.ext.reportsMu.Lock()
	ids := make([]string, 0, len(h.ext.reports))
	for id := range h.ext.reports {
		ids = append(ids, id)
	}
	h.ext.reportsMu.Unlock()
	sort.Strings(ids)
	return append([]string{assistantwire.AssistantID}, ids...)
}

// extPrefixOwner is the manager whose extensions serve /api/v1/<prefix>/ ("": none). Several serve it: the one the
// request names (named), else the default manager (def), else the first that reports it (the assistant first).
func (h *Hub) extPrefixOwner(prefix, named, def string) string {
	serves := func(id string) bool {
		r := h.extReportOf(id)
		return r != nil && slices.ContainsFunc(r.APIs, func(a extwire.APIPrefix) bool { return a.Prefix == prefix })
	}
	for _, id := range []string{named, def} {
		if id != "" && serves(id) {
			return id
		}
	}
	for _, id := range h.extReporters() {
		if r := h.extReportOf(id); r != nil && slices.ContainsFunc(r.APIs, func(a extwire.APIPrefix) bool { return a.Prefix == prefix }) {
			return id
		}
	}
	return ""
}
