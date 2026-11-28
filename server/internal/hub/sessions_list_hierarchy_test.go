package hub

import (
	"encoding/json"
	"net/http/httptest"
	neturl "net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// hierarchyHub is a hub whose machine "demo" lists a parent with three children folded under it (two levels), a row
// on its own and a child folded under a session the hub does not list; machine "other" lists a child of a session of
// the same id on "demo".
func hierarchyHub(t *testing.T) (*Hub, *Machine) {
	t.Helper()
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	other := newMachine(h, MachineConfig{ID: "other"})
	h.machines, h.byID["demo"], h.byID["other"] = []*Machine{m, other}, m, other
	at := func(n int) time.Time { return time.Unix(int64(1000+n), 0) }
	row := func(id, state string, n int, under string, kids *proto.SessionChildren) proto.Session {
		s := proto.Session{MachineID: "demo", SessionID: id, Title: id, State: state, Cwd: "/demo/app", UpdatedAt: at(n)}
		s.SetFold(under, "", "", kids)
		return s
	}
	m.sessions = []proto.Session{
		row("lead", proto.StateIdle, 1, "", &proto.SessionChildren{Total: 3, Working: 1, NeedsYou: 1}),
		row("mid", proto.StateWorking, 2, "lead", nil),
		row("leaf", proto.StateNeedsYou, 3, "lead", nil),
		row("leaf2", proto.StateUnread, 4, "lead", nil),
		row("solo", proto.StateWorking, 5, "", nil),
		row("stray", proto.StateIdle, 6, "hidden", nil), // its parent is not listed: a row of its own
	}
	x := row("x", proto.StateIdle, 7, "lead", nil)
	x.MachineID = "other"
	other.sessions = []proto.Session{x}
	return h, m
}

func pageIDs(page listTestPage) []string {
	var out []string
	for _, s := range page.Sessions {
		out = append(out, s.MachineID+"/"+s.SessionID)
	}
	return out
}

func hierarchyCounts(t *testing.T, h *Hub, query string) sessionlist.Counts {
	t.Helper()
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?counts=1&"+query, nil))
	var c sessionlist.Counts
	if err := json.Unmarshal(w.Body.Bytes(), &c); w.Code != 200 || err != nil {
		t.Fatalf("counts %s: %d %s %v", query, w.Code, w.Body, err)
	}
	return c
}

// roots=1 leaves the children folded under a listed parent out of the rows, the total and every count, and counts
// and filters the parent by its folded state; parent_session_id lists one parent's children, all depths (#5120).
func TestRuntimeListHierarchy(t *testing.T) {
	h, _ := hierarchyHub(t)
	roots := runtimePage(t, h, "summary=1&limit=50&roots=1")
	if got := pageIDs(roots); !slices.Equal(got, []string{"other/x", "demo/stray", "demo/solo", "demo/lead"}) || roots.Total != 4 {
		t.Fatalf("roots: %v (total %d)", got, roots.Total)
	}
	lead := roots.Sessions[3]
	if lead.Children() != (proto.SessionChildren{Total: 3, Working: 1, NeedsYou: 1}) || lead.State != proto.StateIdle {
		t.Fatalf("the parent's summary row: state %s, children %+v", lead.State, lead.Children())
	}
	c := hierarchyCounts(t, h, "roots=1")
	if c.Total != 4 || c.State[proto.StateNeedsYou] != 1 || c.State[proto.StateWorking] != 1 || c.State[proto.StateIdle] != 2 ||
		c.State[proto.StateUnread] != 0 || c.Machine["demo"] != 3 || c.Project["/demo/app"] != 4 {
		t.Fatalf("roots counts: %+v", c)
	}
	if page := runtimePage(t, h, "summary=1&roots=1&state=needs_you"); !slices.Equal(pageIDs(page), []string{"demo/lead"}) {
		t.Fatalf("roots, needs you: %v", pageIDs(page))
	}
	if page := runtimePage(t, h, "summary=1&roots=1&state=idle&machine_id=demo"); !slices.Equal(pageIDs(page), []string{"demo/stray"}) {
		t.Fatalf("roots, idle: %v", pageIDs(page))
	}
	under := runtimePage(t, h, "summary=1&machine_id=demo&parent_session_id=lead")
	if got := pageIDs(under); !slices.Equal(got, []string{"demo/leaf2", "demo/leaf", "demo/mid"}) || under.Total != 3 {
		t.Fatalf("under lead: %v (total %d)", got, under.Total)
	}
	if c := hierarchyCounts(t, h, "machine_id=demo&parent_session_id=lead"); c.Total != 3 || c.State[proto.StateNeedsYou] != 1 {
		t.Fatalf("under lead counts: %+v", c)
	}
	if page := runtimePage(t, h, "summary=1&machine_id=other&parent_session_id=lead"); page.Total != 0 {
		t.Fatalf("another machine's session of the same id: %v", pageIDs(page))
	}
	// with_counts gives the same view's counts.
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?summary=1&roots=1&with_counts=1", nil))
	var both struct {
		Total  int                `json:"total"`
		Counts sessionlist.Counts `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &both); err != nil || both.Total != 4 || both.Counts.Total != 4 || both.Counts.State[proto.StateNeedsYou] != 1 {
		t.Fatalf("with_counts: %s %v", w.Body, err)
	}
	if !slices.Contains(h.features(), featureSessionsHierarchy) || slices.Contains(proto.Features, featureSessionsHierarchy) {
		t.Error("sessions_hierarchy must be advertised by the hub")
	}
}

func TestRuntimeListActiveAgentsByMachine(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	parent := proto.Session{MachineID: "demo", SessionID: "parent", State: proto.StateOffline}
	parent.SetFold("", "", "", &proto.SessionChildren{Total: 1, Working: 1})
	child := proto.Session{MachineID: "demo", SessionID: "child", State: proto.StateWorking}
	child.SetFold("parent", proto.FoldedAsWorker, "", nil)
	m.sessions = []proto.Session{
		parent, child,
		{MachineID: "demo", SessionID: "another", State: proto.StateIdle},
		{MachineID: "demo", SessionID: "history", State: proto.StateOffline},
		{MachineID: "demo", SessionID: "conversation", State: proto.StateWorking, Assistant: true},
	}

	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?roots=1&limit=1&with_counts=1", nil))
	var answer sessionlist.PageWithCounts
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil || w.Code != 200 ||
		!answer.HasMore || len(answer.Sessions) != 1 || answer.Total != 4 ||
		len(answer.Counts.ActiveAgentsByMachine) != 1 || answer.Counts.ActiveAgentsByMachine["demo"] != 2 {
		t.Fatalf("expected one page row and two active Agent roots: %d %s (%v)", w.Code, w.Body, err)
	}
	offline := hierarchyCounts(t, h, "roots=1&state=offline")
	if offline.Total != 1 || offline.ActiveAgentsByMachine == nil || len(offline.ActiveAgentsByMachine) != 0 {
		t.Fatalf("expected known zero active Agents: %+v", offline)
	}
}

// A tab's workers come folded under its lead (folded_as worker, children.workers, #5180): roots=1 leaves them out of
// the rows and every count and counts the lead by its folded state, parent_session_id=<lead> lists them, both kinds of
// row carrying the keys; the tab's offline row and a tab of peers stay rows of their own.
func TestRuntimeListTabTeam(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	row := func(id, state string, n int, under, as string, kids *proto.SessionChildren) proto.Session {
		s := proto.Session{MachineID: "demo", SessionID: id, Title: id, State: state, Cwd: "/demo/app", UpdatedAt: time.Unix(int64(1000+n), 0),
			SpaceKind: "herdr", SpaceID: "w1", SlotID: "t1"}
		s.SetFold(under, as, "", kids)
		return s
	}
	m.sessions = []proto.Session{
		row("lead", proto.StateIdle, 1, "", "", &proto.SessionChildren{Total: 2, Working: 1, NeedsYou: 1, Workers: 2}),
		row("busy", proto.StateWorking, 2, "lead", proto.FoldedAsWorker, nil),
		row("asks", proto.StateNeedsYou, 3, "lead", proto.FoldedAsWorker, nil),
		row("stopped", proto.StateOffline, 4, "", "", nil),
		row("peer", proto.StateWorking, 5, "", "", nil),
	}
	m.sessions[4].SlotID = "t2"
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?summary=1&limit=50&roots=1", nil))
	var roots listTestPage
	if err := json.Unmarshal(w.Body.Bytes(), &roots); err != nil || w.Code != 200 {
		t.Fatalf("roots: %d %s %v", w.Code, w.Body, err)
	}
	if got := pageIDs(roots); !slices.Equal(got, []string{"demo/peer", "demo/stopped", "demo/lead"}) || roots.Total != 3 {
		t.Fatalf("roots: %v (total %d)", got, roots.Total)
	}
	if !strings.Contains(w.Body.String(), `"children":{"total":2,"working":1,"needs_you":1,"workers":2}`) {
		t.Fatalf("the lead's summary row has no workers: %s", w.Body)
	}
	if lead := roots.Sessions[2]; lead.Children().Workers != 2 || lead.FoldedState() != proto.StateNeedsYou {
		t.Fatalf("the lead: %+v shows %s", lead.Children(), lead.FoldedState())
	}
	c := hierarchyCounts(t, h, "roots=1")
	if c.Total != 3 || c.State[proto.StateNeedsYou] != 1 || c.State[proto.StateWorking] != 1 || c.State[proto.StateOffline] != 1 ||
		c.State[proto.StateIdle] != 0 || c.Machine["demo"] != 3 {
		t.Fatalf("roots counts: %+v", c)
	}
	if page := runtimePage(t, h, "summary=1&roots=1&state=needs_you"); !slices.Equal(pageIDs(page), []string{"demo/lead"}) {
		t.Fatalf("roots, needs you: %v", pageIDs(page))
	}
	for _, summary := range []string{"1", "0"} {
		under := runtimePage(t, h, "summary="+summary+"&machine_id=demo&parent_session_id=lead")
		if got := pageIDs(under); !slices.Equal(got, []string{"demo/asks", "demo/busy"}) || under.Total != 2 {
			t.Fatalf("summary=%s, under lead: %v (total %d)", summary, got, under.Total)
		}
		for _, s := range under.Sessions {
			if s.FoldedUnder() != "lead" || s.FoldedAs() != proto.FoldedAsWorker {
				t.Fatalf("summary=%s, worker %s: under %q as %q", summary, s.SessionID, s.FoldedUnder(), s.FoldedAs())
			}
		}
	}
	if c := hierarchyCounts(t, h, "machine_id=demo&parent_session_id=lead"); c.Total != 2 || c.State[proto.StateNeedsYou] != 1 || c.State[proto.StateWorking] != 1 {
		t.Fatalf("under lead counts: %+v", c)
	}
	if all := runtimePage(t, h, "summary=1&limit=50"); all.Total != 5 {
		t.Fatalf("without roots: %v", pageIDs(all))
	}
}

// Without the keys nothing changes: every row is listed and counted by its own state.
func TestRuntimeListHierarchyOlderQueries(t *testing.T) {
	h, _ := hierarchyHub(t)
	all := runtimePage(t, h, "summary=1&limit=50")
	if all.Total != 7 || len(all.Sessions) != 7 {
		t.Fatalf("all: %v (total %d)", pageIDs(all), all.Total)
	}
	c := hierarchyCounts(t, h, "")
	if c.Total != 7 || c.State[proto.StateIdle] != 3 || c.State[proto.StateWorking] != 2 || c.State[proto.StateNeedsYou] != 1 || c.State[proto.StateUnread] != 1 {
		t.Fatalf("counts: %+v", c)
	}
	if page := runtimePage(t, h, "summary=1&state=needs_you"); !slices.Equal(pageIDs(page), []string{"demo/leaf"}) {
		t.Fatalf("needs you: %v", pageIDs(page))
	}
	for _, q := range []string{"roots=0", "roots=false"} {
		if page := runtimePage(t, h, "summary=1&limit=50&"+q); page.Total != 7 {
			t.Fatalf("%s: %v", q, pageIDs(page))
		}
	}
	// A list with no hierarchy says nothing of one.
	plain := newTestHub(t)
	m := newMachine(plain, MachineConfig{ID: "demo"})
	plain.machines, plain.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "a", State: proto.StateIdle, UpdatedAt: time.Unix(1, 0)}}
	for _, q := range []string{"summary=1&limit=50", "summary=0&limit=50"} {
		w := httptest.NewRecorder()
		plain.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+q, nil))
		for _, key := range []string{"folded_under", "folded_as", "children", "started_at", "asking_parent_session_id"} {
			if strings.Contains(w.Body.String(), `"`+key+`"`) {
				t.Fatalf("%s writes %s: %s", q, key, w.Body)
			}
		}
	}
}

// parent_session_id needs machine_id, both keys take one value, roots a boolean; a cursor and a delta are bound to them.
func TestRuntimeListHierarchyValidationAndBinding(t *testing.T) {
	h, m := hierarchyHub(t)
	for _, query := range []string{"parent_session_id=lead", "parent_session_id=&machine_id=demo", "roots=yes",
		"roots=1&roots=0", "machine_id=demo&parent_session_id=a&parent_session_id=b", "roots=1&machine_id=demo&parent_session_id=lead",
		"counts=1&roots=1&machine_id=demo&parent_session_id=lead"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
	}
	first := runtimePage(t, h, "summary=1&limit=1&roots=1")
	if !first.HasMore || first.Cursor == "" {
		t.Fatalf("first: %+v", first)
	}
	for _, query := range []string{"summary=1&limit=1&cursor=", "summary=1&limit=1&machine_id=demo&parent_session_id=lead&cursor="} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query+neturl.QueryEscape(first.Cursor), nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Fatalf("a roots cursor read %s: %d %s", query, w.Code, w.Body)
		}
	}
	if next := runtimePage(t, h, "summary=1&limit=1&roots=1&cursor="+neturl.QueryEscape(first.Cursor)); next.Total != 4 {
		t.Fatalf("next: %+v", next)
	}
	// mid comes out of its parent (its parent let it go): the roots delta adds it, the parent's delta removes it.
	under := runtimePage(t, h, "summary=1&machine_id=demo&parent_session_id=lead")
	m.mu.Lock()
	m.sessions = slices.Clone(m.sessions)
	m.sessions[1].SetFold("", "", "", nil)
	m.sessions[0].SetFold("", "", "", &proto.SessionChildren{Total: 2, NeedsYou: 1})
	m.mu.Unlock()
	h.sessionsChanged(m)
	delta := runtimePage(t, h, "summary=1&roots=1&since="+neturl.QueryEscape(first.Revision))
	if got := pageIDs(delta); !slices.Contains(got, "demo/mid") || delta.Total != 5 || len(delta.Remove) != 0 {
		t.Fatalf("roots delta: %v remove %v total %d", got, delta.Remove, delta.Total)
	}
	gone := runtimePage(t, h, "summary=1&machine_id=demo&parent_session_id=lead&since="+neturl.QueryEscape(under.Revision))
	if !slices.Equal(gone.Remove, []string{runtimeListID("demo", "mid")}) || gone.Total != 2 {
		t.Fatalf("under delta: %v remove %v total %d", pageIDs(gone), gone.Remove, gone.Total)
	}
}

func TestPagedCountsHeldNeeds(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	now := time.Unix(2000, 0)
	lead := proto.Session{MachineID: "demo", SessionID: "lead", Title: "lead", State: proto.StateIdle, UpdatedAt: now}
	lead.SetFold("", "", "", &proto.SessionChildren{Total: 1, NeedsYou: 1})
	leaf := proto.Session{MachineID: "demo", SessionID: "leaf", Title: "leaf", State: proto.StateNeedsYou, UpdatedAt: now}
	leaf.SetFold("lead", "", "", nil)
	m.sessions = []proto.Session{
		lead,
		leaf,
		{MachineID: "demo", SessionID: "solo-p1", Title: "solo-p1", State: proto.StateNeedsYou, UpdatedAt: now},
		{MachineID: "demo", SessionID: "mix-p0", Title: "mix-p0", State: proto.StateNeedsYou, UpdatedAt: now},
		{MachineID: "demo", SessionID: "asst-p1", Title: "asst-p1", Assistant: true, State: proto.StateNeedsYou, UpdatedAt: now},
	}
	h.reported.mu.Lock()
	h.reported.interactions = map[string]proto.Interaction{
		"ix-leaf":  {ID: "ix-leaf", MachineID: "demo", SessionID: "leaf", FoldedUnder: "lead", Kind: proto.IxPermission, PushLevel: assistantwire.LevelP1},
		"ix-solo":  {ID: "ix-solo", MachineID: "demo", SessionID: "solo-p1", Kind: proto.IxPermission, PushLevel: assistantwire.LevelP1},
		"ix-mix-1": {ID: "ix-mix-1", MachineID: "demo", SessionID: "mix-p0", Kind: proto.IxPermission, PushLevel: assistantwire.LevelP1},
		"ix-mix-0": {ID: "ix-mix-0", MachineID: "demo", SessionID: "mix-p0", Kind: proto.IxQuestion, PushLevel: assistantwire.LevelP0},
		"ix-asst":  {ID: "ix-asst", MachineID: "demo", SessionID: "asst-p1", Kind: proto.IxQuestion, PushLevel: assistantwire.LevelP1},
	}
	h.reported.mu.Unlock()

	c := hierarchyCounts(t, h, "agents_only=1&roots=1")
	if c.State[proto.StateNeedsYou] != 3 || c.HeldNeeds != 2 {
		t.Fatalf("counts = %+v, want needs_you=3 held_needs=2", c)
	}
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?summary=1&limit=10&agents_only=1&roots=1&with_counts=1", nil))
	var pc sessionlist.PageWithCounts
	if err := json.Unmarshal(w.Body.Bytes(), &pc); w.Code != 200 || err != nil {
		t.Fatalf("with_counts: %d %s %v", w.Code, w.Body, err)
	}
	if pc.Counts.State[proto.StateNeedsYou] != 3 || pc.Counts.HeldNeeds != 2 {
		t.Fatalf("with_counts = %+v, want needs_you=3 held_needs=2", pc.Counts)
	}
}

// A session whose pending cards are all held by its task's supervisor (a screen, a permission or a plan marked
// delegating) is still needs_you, but supervisor_held counts it so no client counts it as the user's. A delegating
// question (the assistant deciding on it), or any other card beside a held one, leaves the session counted; none of
// them is counted twice with held_needs.
func TestPagedCountsSupervisorHeld(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	now := time.Unix(2000, 0)
	needs := func(id string) proto.Session {
		return proto.Session{MachineID: "demo", SessionID: id, Title: id, State: proto.StateNeedsYou, UpdatedAt: now}
	}
	m.sessions = []proto.Session{needs("screen"), needs("perm"), needs("mixed"), needs("deciding"), needs("plain"), needs("held-p1")}
	h.reported.mu.Lock()
	h.reported.interactions = map[string]proto.Interaction{
		"ix-screen":        {ID: "ix-screen", MachineID: "demo", SessionID: "screen", Kind: proto.IxScreen, Delegating: true},
		"ix-screen-mirror": {ID: "ix-screen-mirror", MachineID: "demo", SessionID: "screen", Kind: proto.IxScreen, MirrorOf: "ix-screen"},
		"ix-perm":          {ID: "ix-perm", MachineID: "demo", SessionID: "perm", Kind: proto.IxPermission, Delegating: true},
		"ix-mixed-1":       {ID: "ix-mixed-1", MachineID: "demo", SessionID: "mixed", Kind: proto.IxScreen, Delegating: true},
		"ix-mixed-2":       {ID: "ix-mixed-2", MachineID: "demo", SessionID: "mixed", Kind: proto.IxPermission},
		"ix-decide":        {ID: "ix-decide", MachineID: "demo", SessionID: "deciding", Kind: proto.IxQuestion, Delegating: true},
		"ix-plain":         {ID: "ix-plain", MachineID: "demo", SessionID: "plain", Kind: proto.IxScreen},
		"ix-held-1":        {ID: "ix-held-1", MachineID: "demo", SessionID: "held-p1", Kind: proto.IxScreen, Delegating: true},
		"ix-held-2":        {ID: "ix-held-2", MachineID: "demo", SessionID: "held-p1", Kind: proto.IxPermission, PushLevel: assistantwire.LevelP1},
	}
	h.reported.mu.Unlock()
	c := hierarchyCounts(t, h, "agents_only=1")
	if c.State[proto.StateNeedsYou] != 6 || c.SupervisorHeld != 2 || c.HeldNeeds != 1 {
		t.Fatalf("counts = %+v, want needs_you=6 supervisor_held=2 held_needs=1", c)
	}
	held, supervised := h.heldNeedsIDs(false)
	slices.Sort(supervised)
	if want := []string{runtimeListID("demo", "perm"), runtimeListID("demo", "screen")}; !slices.Equal(supervised, want) {
		t.Fatalf("supervised = %v, want %v", supervised, want)
	}
	if want := []string{runtimeListID("demo", "held-p1")}; !slices.Equal(held, want) {
		t.Fatalf("held = %v, want %v", held, want)
	}
}

// Cross-machine hierarchy:
// - hub advertises sessions_cross_machine_hierarchy;
// - roots=1 filters folded cross-machine subagents AND orphan is_subagent rows (e.g. Codex archived parent or no-parent review);
// - parent_machine_id+parent_session_id and machine_id+parent_session_id return both same-machine and cross-machine children;
// - combining parent_machine_id with machine_id narrows children to one child machine;
// - heldNeedsIDs credits cross-machine folded child P1 cards to the parent on the parent machine;
// - removing or hiding the parent re-evaluates cross-machine children.
func TestRuntimeListCrossMachineHierarchyAndOrphanSubagents(t *testing.T) {
	h := newTestHub(t)
	mac := newMachine(h, MachineConfig{ID: "mac"})
	box := newMachine(h, MachineConfig{ID: "box"})
	h.machines, h.byID["mac"], h.byID["box"] = []*Machine{mac, box}, mac, box
	at := func(n int) time.Time { return time.Unix(int64(3000+n), 0) }

	lead := proto.Session{MachineID: "mac", SessionID: "lead", Title: "Mac lead", State: proto.StateIdle, Cwd: "/code/app", UpdatedAt: at(1)}
	lead.SetFold("", "", "", &proto.SessionChildren{Total: 2, Working: 1, NeedsYou: 1})
	localKid := proto.Session{MachineID: "mac", SessionID: "local-kid", Title: "Local codex kid", State: proto.StateWorking, Cwd: "/code/app",
		Agent: "codex", IsSubagent: true, ParentSessionID: "lead", UpdatedAt: at(2)}
	localKid.SetFold("lead", "", "", nil)
	codexOrphan := proto.Session{MachineID: "mac", SessionID: "codex-orphan", Title: "Archived parent subagent", State: proto.StateIdle, Cwd: "/code/app",
		Agent: "codex", IsSubagent: true, ParentSessionID: "archived-lead", UpdatedAt: at(3)}
	reviewOrphan := proto.Session{MachineID: "mac", SessionID: "review-orphan", Title: "No-parent review subagent", State: proto.StateWorking, Cwd: "/code/app",
		Agent: "codex", IsSubagent: true, UpdatedAt: at(4)}
	mac.sessions = []proto.Session{lead, localKid, codexOrphan, reviewOrphan}

	remoteKid := proto.Session{MachineID: "box", SessionID: "remote-kid", Title: "Box A2A subagent", State: proto.StateNeedsYou, Cwd: "/code/app",
		Agent: "codex", IsSubagent: true, ParentSessionID: "lead", UpdatedAt: at(5)}
	remoteKid.SetSubagentProvenance("mac", "task-cross-1", proto.SubagentOriginA2A)
	remoteKid.SetFoldWithMachine("mac", "lead", "", "", nil)
	boxSolo := proto.Session{MachineID: "box", SessionID: "box-solo", Title: "Independent box session", State: proto.StateIdle, Cwd: "/code/app", UpdatedAt: at(6)}
	box.sessions = []proto.Session{remoteKid, boxSolo}

	if !slices.Contains(h.features(), featureSessionsCrossMachineHierarchy) {
		t.Fatal("sessions_cross_machine_hierarchy must be advertised by the hub")
	}

	// New client: roots=1&cross_machine_hierarchy=1 returns only mac/lead and box/box-solo:
	// local kid, remote kid, and both orphan subagents are excluded.
	roots := runtimePage(t, h, "summary=1&limit=50&roots=1&cross_machine_hierarchy=1")
	if got := pageIDs(roots); !slices.Equal(got, []string{"box/box-solo", "mac/lead"}) || roots.Total != 2 {
		t.Fatalf("roots+cross = %v (total %d), want [box/box-solo mac/lead]", got, roots.Total)
	}
	// Legacy client: roots=1 WITHOUT cross_machine_hierarchy=1 still hides same-machine folded children and
	// non-blocking orphan subagents, while keeping cross-machine folded box/remote-kid reachable so its
	// approval card is never stranded on older clients.
	legacyRoots := runtimePage(t, h, "summary=1&limit=50&roots=1")
	if got := pageIDs(legacyRoots); !slices.Equal(got, []string{"box/box-solo", "box/remote-kid", "mac/lead"}) || legacyRoots.Total != 3 {
		t.Fatalf("legacy roots = %v (total %d), want [box/box-solo box/remote-kid mac/lead]", got, legacyRoots.Total)
	}

	// parent_machine_id + parent_session_id and machine_id + parent_session_id + cross_machine_hierarchy=1
	// return BOTH local-kid and remote-kid with provenance preserved.
	for _, query := range []string{
		"summary=1&parent_machine_id=mac&parent_session_id=lead",
		"summary=1&machine_id=mac&parent_session_id=lead&cross_machine_hierarchy=1",
		"summary=0&parent_machine_id=mac&parent_session_id=lead",
	} {
		kids := runtimePage(t, h, query)
		if got := pageIDs(kids); !slices.Equal(got, []string{"box/remote-kid", "mac/local-kid"}) || kids.Total != 2 {
			t.Fatalf("%s: got %v (total %d), want [box/remote-kid mac/local-kid]", query, got, kids.Total)
		}
		if rk := kids.Sessions[0]; rk.FoldedUnder() != "lead" || rk.FoldedUnderMachineID() != "mac" ||
			rk.ParentMachineID() != "mac" || rk.A2ATaskID() != "task-cross-1" || rk.SubagentOrigin() != proto.SubagentOriginA2A {
			t.Fatalf("%s: remote kid lost hierarchy metadata: %+v", query, rk.Hierarchy)
		}
	}
	// Legacy machine_id=mac&parent_session_id=lead (without cross_machine_hierarchy=1) returns same-machine children only.
	if legacyKids := runtimePage(t, h, "summary=1&machine_id=mac&parent_session_id=lead"); !slices.Equal(pageIDs(legacyKids), []string{"mac/local-kid"}) {
		t.Fatalf("legacy machine_id+parent_session_id = %v, want [mac/local-kid]", pageIDs(legacyKids))
	}
	// Filtering child machine with parent_machine_id + machine_id=box returns only box/remote-kid.
	boxKids := runtimePage(t, h, "summary=1&parent_machine_id=mac&parent_session_id=lead&machine_id=box")
	if got := pageIDs(boxKids); !slices.Equal(got, []string{"box/remote-kid"}) || boxKids.Total != 1 {
		t.Fatalf("boxKids = %v (total %d)", got, boxKids.Total)
	}
	// Archived parent's Session Overview query still finds codex-orphan via DirectParent.
	archivedKids := runtimePage(t, h, "summary=1&parent_machine_id=mac&parent_session_id=archived-lead")
	if got := pageIDs(archivedKids); !slices.Equal(got, []string{"mac/codex-orphan"}) || archivedKids.Total != 1 {
		t.Fatalf("archivedKids = %v (total %d)", got, archivedKids.Total)
	}

	// Cross-machine held P1 interaction credits mac/lead when cross_machine_hierarchy=1, and credits
	// box/remote-kid (the visible row) on legacy clients without cross_machine_hierarchy=1.
	h.reported.mu.Lock()
	h.reported.interactions = map[string]proto.Interaction{
		"ix-remote": {ID: "ix-remote", MachineID: "box", SessionID: "remote-kid", FoldedUnder: "lead", FoldedUnderMachineID: "mac", Kind: proto.IxPermission, PushLevel: assistantwire.LevelP1},
	}
	h.reported.mu.Unlock()
	c := hierarchyCounts(t, h, "agents_only=1&roots=1&cross_machine_hierarchy=1")
	if c.State[proto.StateNeedsYou] != 1 || c.HeldNeeds != 1 {
		t.Fatalf("cross-machine held counts = %+v, want needs_you=1 held_needs=1", c)
	}
	cLegacy := hierarchyCounts(t, h, "agents_only=1&roots=1")
	if cLegacy.State[proto.StateNeedsYou] != 1 || cLegacy.HeldNeeds != 1 || cLegacy.State[proto.StateWorking] != 1 {
		t.Fatalf("legacy held counts = %+v, want needs_you=1 (remote-kid only, not double-counted on mac/lead), held_needs=1, working=1 (mac/lead via local-kid)", cLegacy)
	}

	// Validation: parent_machine_id without parent_session_id returns 400 invalid_query.
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?parent_machine_id=mac", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
		t.Fatalf("parent_machine_id without parent_session_id: %d %s", w.Code, w.Body)
	}

	// Removing mac/lead re-evaluates box/remote-kid: it drops out of folded roots, stays excluded
	// from roots=1&cross_machine_hierarchy=1 (orphan IsSubagent), and remains queryable via DirectParent.
	mac.mu.Lock()
	mac.sessions = []proto.Session{localKid, codexOrphan, reviewOrphan}
	mac.mu.Unlock()
	h.sessionsChanged(mac)
	if rootsAfter := runtimePage(t, h, "summary=1&limit=50&roots=1&cross_machine_hierarchy=1"); !slices.Equal(pageIDs(rootsAfter), []string{"box/box-solo"}) {
		t.Fatalf("roots after parent removal = %v, want [box/box-solo]", pageIDs(rootsAfter))
	}
	if kidsAfter := runtimePage(t, h, "summary=1&parent_machine_id=mac&parent_session_id=lead"); !slices.Equal(pageIDs(kidsAfter), []string{"box/remote-kid", "mac/local-kid"}) {
		t.Fatalf("children still queryable by parent after parent unlisted = %v", pageIDs(kidsAfter))
	}
}
