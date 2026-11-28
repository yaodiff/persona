package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The direction tree (#4352, docs/assistant/unified-entry.md "The direction tree"): each current direction (a leader
// conversation) with the open tasks it supervises, each task with its state and its Agents, each Agent with the
// Agents under it; then the secretary group (the main conversation's) with every other open task. The sidebar, the
// To-dos page's direction grouping and the Agents page's direction tree on every client read this one tree
// (GET /api/v1/assistant/directions/tree): no client works out the grouping or a task's state itself.
//
//   - Which tasks: the ones the To-dos page counts open (triageCountsAsOpen, the hub's copy of the SOP, quiet tasks
//     included as the page includes them), so the four counts of every group add up to the page's open count; a
//     worker task nested under its leader's Agent is the one exception (it is not listed or counted on its own).
//   - Whose: the task's supervisor. A current leader's tasks are its direction's; the main conversation's, no
//     supervisor's, an ended or unknown conversation's are the secretary's. A worker task (its supervisor a task
//     leader's Agent, task_leader.go) puts its Agents under that leader Agent inside the leader's task; when the
//     leader's task is not listed it is a task of the conversation above the leader (supervisorAboveTask).
//   - Its state, first match wins: needs_you (the SOP's Waiting on you), review (waits for acceptance, nothing of it
//     running), planned (no agent has ever worked on it and it is not a concern: a piece of a direction a leader
//     recorded with todo_create, a Lark topic's to-do, a to-do the user made; #5846), stuck (taskStuck, the leader
//     reports' rule), working. A direction lists them needs_you, review, stuck, working, planned.
//   - Running: whether anything under a direction is at work right now, an Agent of one of its listed tasks or one
//     under it whose session is working (the state its row shows), whatever the task's state. Not the working count:
//     a working task may have every Agent stopped, and one that needs the user may still have another Agent at work.
//     It is the one rule for a direction row's activity mark on every client (#6626); the leader conversation's own
//     turn is its own row's state, which the clients have.
//
// The apps are told {"type":"directions"} whenever the tree's content changes (directionTreeSync), and read the tree
// again then.

// The tree's task states, in the order a direction lists them.
const (
	treeNeedsYou = "needs_you"
	treeReview   = "review"
	treeStuck    = "stuck"
	treeWorking  = "working"
	treePlanned  = "planned"
)

var treeStates = []string{treeNeedsYou, treeReview, treeStuck, treeWorking, treePlanned}

// sourceSpawned: a to-do Agent linked because another Agent of the to-do spawned it (todo_sessions.go); its via is
// "<machine>/<session>" of that Agent.
const sourceSpawned = "spawned"

// directionTreeSettle is how long a change waits for the rest before the tree is built again and compared.
const directionTreeSettle = time.Second

// directionTree is GET /api/v1/assistant/directions/tree.
type directionTree struct {
	Directions []treeDirection `json:"directions"`
}

// treeDirection is one direction, or the secretary group (Secretary; ID the main conversation's, Name "").
type treeDirection struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Secretary bool               `json:"secretary,omitempty"`
	External  bool               `json:"external,omitempty"`
	MachineID string             `json:"machine_id,omitempty"`
	SessionID string             `json:"session_id,omitempty"`
	Counts    treeCounts         `json:"counts"`
	Running   bool               `json:"running"` // sent false too, so a client tells a hub without it (it reads counts.working then)
	Tasks     []treeTask         `json:"tasks"`
	Sessions  []directionSession `json:"sessions,omitempty"`
}

// treeCounts are how many of a direction's listed tasks are in each state. UserCheck is the subset of Review tasks
// accepted by their supervisor awaiting owner verification (omitted when 0 so wire shapes stay backward-compatible).
type treeCounts struct {
	Working   int `json:"working"`
	NeedsYou  int `json:"needs_you"`
	Stuck     int `json:"stuck"`
	Review    int `json:"review"`
	UserCheck int `json:"user_check,omitempty"`
	Planned   int `json:"planned"`
}

func (c *treeCounts) add(state string) {
	switch state {
	case treeNeedsYou:
		c.NeedsYou++
	case treeReview:
		c.Review++
	case treeStuck:
		c.Stuck++
	case treePlanned:
		c.Planned++
	default:
		c.Working++
	}
}

// treeTask is one open task of a direction.
type treeTask struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	State       string           `json:"state"`
	Agents      []treeAgent      `json:"agents"`
	Responsible *TodoResponsible `json:"responsible,omitempty"`
	last        time.Time        // its latest activity (the SOP's), for the order
}

// treeAgent is one Agent of a task, or one under another Agent: State is the session's state as the apps get it on
// its row ("" when the session was not found).
type treeAgent struct {
	MachineID string      `json:"machine_id"`
	SessionID string      `json:"session_id"`
	Title     string      `json:"title"`
	Agent     string      `json:"agent"`
	State     string      `json:"state"`
	Children  []treeAgent `json:"children,omitempty"`
}

// directionTreeBook is what the apps were last told the tree is (its digest), and the scheduled compare.
type directionTreeBook struct {
	mu     sync.Mutex
	armed  bool
	told   bool
	digest [sha256.Size]byte
	after  func(d time.Duration, f func()) // tests: a fake clock
	run    sync.Mutex                      // one build and compare at a time
	timer  *time.Timer                     // a real-clock comparison not yet run (tests can stop it during teardown)
	runs   sync.WaitGroup                  // scheduled real-clock comparisons; fixtures wait before closing their store
	// wake, wakeTimer: the build directionTreeAt scheduled for when a computer stops being shown as before.
	wake      time.Time
	wakeTimer *time.Timer
	// sessions and machines: the directions' sessions in the tree told last, and their computers (directionSessionsOn).
	// A build that cannot place sessions keeps those (directionTree).
	sessions map[string][]directionSession
	machines map[string]bool
}

// directionTreeRoutes: GET /api/v1/assistant/directions/tree is the tree as it is now.
func (s *Service) directionTreeRoutes() {
	s.handle("GET /api/v1/assistant/directions/tree", func(w http.ResponseWriter, r *http.Request) {
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		apierr.WriteJSON(w, s.directionTree(r.Context(), time.Now()))
	})
}

// directionTreeSoon schedules building the tree again and telling the apps when it changed, once the changes of the
// next directionTreeSettle are in. Called when the snapshot is published (todos, titles, settings, the leaders), when a
// to-do changes, and when the sessions of a machine a task's Agent is on change.
func (s *Service) directionTreeSoon() {
	b := &s.dtree
	b.mu.Lock()
	if b.armed || s.runCtx().Err() != nil {
		b.mu.Unlock()
		return
	}
	b.armed = true
	after := b.after
	if after != nil {
		b.mu.Unlock()
		after(directionTreeSettle, func() { s.directionTreeSync(context.Background()) })
		return
	}
	b.runs.Add(1)
	b.timer = time.AfterFunc(directionTreeSettle, func() {
		defer b.runs.Done()
		b.mu.Lock()
		b.timer = nil
		b.mu.Unlock()
		s.directionTreeSync(context.Background())
	})
	b.mu.Unlock()
}

// directionTreeSessions: the sessions of machine d changed, or its computer went away (NodeDetached); the tree is built
// again when that can change it: an open task's Agent is there, or a direction's session may be (directionSessionsOn).
func (s *Service) directionTreeSessions(d DeviceSessions) {
	s.todos.mu.RLock()
	on := false
	for k := range s.todos.activeBySession {
		if k[0] == d.MachineID {
			on = true
			break
		}
	}
	s.todos.mu.RUnlock()
	if on || s.directionSessionsOn(context.Background(), d.MachineID, d.Sessions) {
		s.directionTreeSoon()
	}
}

// directionSessionsOn: a session of a current direction's may be on machine: the tree told last lists one there, the
// owner filed one there, or a session of list (the machine's) was opened by a current direction's leader conversation.
func (s *Service) directionSessionsOn(ctx context.Context, machine string, list []proto.Session) bool {
	machine = s.canonical(machine)
	b := &s.dtree
	b.mu.Lock()
	told := b.machines[machine]
	b.mu.Unlock()
	if told {
		return true
	}
	cfg := s.settings(ctx)
	current := func(id string) bool { return id != "" && id != cfg.Main && slices.Contains(cfg.Leaders, id) }
	if records, err := s.directionSessionRecords(ctx); err == nil {
		for k, e := range s.directionEntries(records) {
			if k[0] == machine && e.live() && current(e.Direction) {
				return true
			}
		}
	}
	if len(list) == 0 {
		return false
	}
	var opened []string // the computers the sessions of list a current direction opened were opened on
	o := &s.origins
	o.mu.Lock()
	if s.originsLocked(ctx) {
		for i := range list {
			if origin := o.m[list[i].SessionID]; origin != nil && origin.Origin == OriginSpawned && current(origin.Ref) {
				opened = append(opened, cmp.Or(origin.MachineID, cfg.MachineID))
			}
		}
	}
	o.mu.Unlock()
	return slices.ContainsFunc(opened, func(m string) bool { return s.canonical(m) == machine })
}

// directionTreeSync builds the tree and tells the apps {"type":"directions"} when it is not the one they were last told
// of. Nothing is told before the to-dos are loaded.
func (s *Service) directionTreeSync(ctx context.Context) {
	b := &s.dtree
	b.mu.Lock()
	b.armed = false
	b.mu.Unlock()
	if !s.todosLoaded() {
		return
	}
	b.run.Lock()
	defer b.run.Unlock()
	tree, wake := s.directionTreeWake(ctx, time.Now())
	raw, err := json.Marshal(tree)
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	sessions, machines := map[string][]directionSession{}, map[string]bool{}
	for _, d := range tree.Directions {
		if len(d.Sessions) > 0 {
			sessions[d.ID] = d.Sessions
		}
		for _, session := range d.Sessions {
			machines[session.MachineID] = true
		}
	}
	b.mu.Lock()
	same := b.told && b.digest == sum
	b.digest, b.told, b.sessions, b.machines = sum, true, sessions, machines
	b.mu.Unlock()
	if !same {
		s.event(ctx, map[string]any{"type": "directions"})
	}
	if !wake.IsZero() {
		s.directionTreeAt(wake)
	}
}

// currentLeaders are the current leaders (assistant.leaders without the main conversation) in the order
// GET /api/v1/assistant/leaders lists them (by when each became a direction, then id; one not recorded yet last), and
// their names as it gives them.
func (s *Service) currentLeaders(ctx context.Context, cfg Settings) ([]string, map[string]string) {
	titles, _ := s.conversationTitles()
	pnames, pok := s.projectNames(ctx)
	type lead struct {
		id   string
		rec  bool
		at   time.Time
		name string
	}
	var list []lead
	b := &s.directions
	b.mu.Lock()
	loaded := s.directionsLocked(ctx)
	for _, id := range cfg.Leaders {
		if id == "" || id == cfg.Main || slices.ContainsFunc(list, func(l lead) bool { return l.id == id }) {
			continue
		}
		l := lead{id: id, name: directionName(nil, titles[id].title, "")}
		if d := b.m[id]; loaded && d != nil {
			l.rec, l.at, l.name = true, d.At, nameOf(d, titles[id].title, pnames, pok)
		}
		list = append(list, l)
	}
	b.mu.Unlock()
	slices.SortFunc(list, func(a, b lead) int {
		if a.rec != b.rec {
			return cond(a.rec, -1, 1)
		}
		return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.id, b.id))
	})
	ids := make([]string, 0, len(list))
	names := make(map[string]string, len(list))
	for _, l := range list {
		ids = append(ids, l.id)
		names[l.id] = l.name
	}
	return ids, names
}

// directionTree builds the tree from the to-dos and their Agents' sessions now (each machine asked once).
func (s *Service) directionTree(ctx context.Context, now time.Time) directionTree {
	out, _ := s.directionTreeWake(ctx, now)
	return out
}

// directionTreeWake is directionTree, and when a computer of its directions' sessions stops being shown as before
// (zero: none will): the tree can change then, and nothing tells the manager.
func (s *Service) directionTreeWake(ctx context.Context, now time.Time) (directionTree, time.Time) {
	tb, cfg, names := s.directionTreeParts(ctx, now)
	out := tb.tree(names)
	for i := range out.Directions {
		if d := s.directionOf(out.Directions[i].ID); d != nil && d.Outside != nil {
			out.Directions[i].External = true
			out.Directions[i].MachineID = d.Outside.MachineID
			out.Directions[i].SessionID = d.Outside.SessionID
		}
	}
	groups, wake, err := tb.directionSessionGroups(ctx, cfg)
	if err != nil {
		// What places sessions could not be read for now: the sessions told last stay, rather than every direction
		// losing its sessions until the next build, less those that follow a task now (rule 1) or are under one in
		// this build's tasks (directionTreeSessionRows below).
		s.logf("direction sessions: %v", err)
		groups = tb.directionLastSessions(s.taskFollowers())
	}
	tb.directionTreeSessionRows(groups, out)
	for i := range out.Directions {
		if !out.Directions[i].Secretary {
			out.Directions[i].Sessions = groups[out.Directions[i].ID]
		}
	}
	return out, wake
}

// directionLastSessions are the directions' sessions in the tree told last, copied, without those that follow a task
// now (followers, a sub-agent session by its parent as this build lists it).
func (tb *treeBuild) directionLastSessions(followers taskFollowers) map[string][]directionSession {
	b := &tb.s.dtree
	b.mu.Lock()
	last := b.sessions
	b.mu.Unlock()
	groups := make(map[string][]directionSession, len(last))
	for id, sessions := range last {
		for _, session := range sessions {
			k := [2]string{session.MachineID, session.SessionID}
			if !followers.follows(k, func(sid string) string { return tb.sessions[[2]string{k[0], sid}].ParentSessionID }) {
				groups[id] = append(groups[id], session)
			}
		}
	}
	return groups
}

// directionTreeAt builds the tree again at at: a computer of a direction's session stops being shown as before then
// (its reconnect grace or the warm start after a restart ends), and the devices tell the manager nothing of it. One
// wake is kept, the earliest; the build itself waits directionTreeSettle more, past the devices' own change.
func (s *Service) directionTreeAt(at time.Time) {
	b := &s.dtree
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.wake.IsZero() && !b.wake.After(at) || s.runCtx().Err() != nil {
		return
	}
	b.wake = at
	fire := func() {
		b.mu.Lock()
		if b.wake.Equal(at) {
			b.wake, b.wakeTimer = time.Time{}, nil
		}
		b.mu.Unlock()
		if s.runCtx().Err() == nil {
			s.directionTreeSoon()
		}
	}
	if b.after != nil {
		b.after(time.Until(at), fire)
		return
	}
	if b.wakeTimer != nil {
		b.wakeTimer.Stop()
	}
	b.wakeTimer = time.AfterFunc(time.Until(at), fire)
}

// directionTreeParts builds the open tree. To-do attribution includes historical directions. directionOf restricts
// the tree's groups to current leaders.
func (s *Service) directionTreeParts(ctx context.Context, now time.Time) (*treeBuild, Settings, map[string]string) {
	cfg := s.settings(ctx)
	leads, names := s.currentLeaders(ctx, cfg)
	all := s.treeTodos()
	if err := s.listDecisionReviews(ctx, all); err != nil {
		s.logf("direction tree decision reviews: %v", err)
	}
	if cfg.todosV2On() {
		s.populateTodoActiveBlockers(ctx, all)
	}
	asks := s.todoAsks(ctx) // once the to-dos are let go: it reads them itself
	var undone []Todo
	wantCards := map[string]map[string]bool{}
	for i := range all {
		if a, ok := asks[all[i].ID]; ok {
			all[i].Ask = &a
		}
		if all[i].DoneAt == nil {
			undone = append(undone, all[i].Todo)
		}
		if all[i].DoneAt == nil || all[i].StartError != "" {
			linked := hasParkInteractionLink(all[i].Supervision)
			for _, a := range all[i].Agents {
				if a.ReleasedAt != nil && !linked || a.DismissedAt != nil {
					continue
				}
				m := s.canonical(a.MachineID)
				if wantCards[m] == nil {
					wantCards[m] = map[string]bool{}
				}
				wantCards[m][a.SessionID] = true
			}
		}
	}
	lists, reached := s.agentSessionLists(ctx, undone)
	cards := s.todoRunCards(wantCards)
	heldOnly := s.heldOnlySessions()
	tb := newTreeBuild(s, cfg.Main, leads, all, lists, cards, heldOnly, reached, now, s.location(ctx), cfg)
	if cfg.todosV2On() {
		tb.effectiveDirs = tb.resolveEffectiveDirections(all, s.todoDirectionLabels(ctx, cfg))
	}
	return tb, cfg, names
}

// treeTodos are every to-do as the tree reads them, by id.
func (s *Service) treeTodos() []listedTodo {
	s.todos.mu.RLock()
	all := make([]listedTodo, 0, len(s.todos.todos))
	for _, t := range s.todos.todos {
		raw := copyTodoLocked(t)
		shown := formatTodo(raw)
		// formatTodo uses "review" for older clients' cancellation labels. Keep the stored marker in this internal
		// projection so the single-direction read can distinguish cancellation from ordinary completion.
		shown.DoneBy = raw.DoneBy
		all = append(all, listedTodo{Todo: shown})
	}
	s.todos.mu.RUnlock()
	slices.SortFunc(all, func(a, b listedTodo) int { return cmp.Compare(a.ID, b.ID) })
	return all
}

// directionOwners indexes the to-dos and their task-leader Agents for attribution. It asks no computer.
func (s *Service) directionOwners(all []listedTodo) *treeBuild {
	tb := &treeBuild{s: s, todos: map[string]listedTodo{}, leaderAgent: map[[2]string][]string{}}
	for _, t := range all {
		tb.index(t)
	}
	return tb
}

// treeBuild is one build of the tree: the to-dos placed, the sessions by machine and id, and what is nested where.
type treeBuild struct {
	s             *Service
	main          string
	leads         []string
	todosV2       bool
	effectiveDirs map[string]string // todos_v2 effective direction_id by todo id
	todos         map[string]listedTodo
	placed        map[string]todoTriage
	views         map[string]assistantwire.TodoSummary
	carried       map[string]bool    // open PartOf steps already rolled up onto an open top-level parent
	heldOnly      map[[2]string]bool // canonical (machine, session) waiting only on Delegating cards
	open          []string           // the ids the To-dos page counts open, by id
	reached       map[string]bool
	now           time.Time

	sessions map[[2]string]proto.Session
	lists    map[string][]proto.Session    // the complete lists read once for this build, by machine
	listed   map[string]bool               // computers whose complete current list was read during this build
	codexKid map[[2]string][]proto.Session // a machine's Codex sub-agent sessions still going, by parent session
	// leaderAgent: every task leader's Agent (machine, session) and the to-dos it is the leader Agent of.
	leaderAgent map[[2]string][]string
	// workerAt: an open worker task listed under a leader Agent instead of on its own: the leader task and that Agent.
	workerAt map[string]treeAttach
	// workersUnder: the worker tasks under each (leader task, leader Agent).
	workersUnder map[treeAttach][]string
}

// index adds to-do t, and the leader Agents it has.
func (tb *treeBuild) index(t listedTodo) {
	tb.todos[t.ID] = t
	for _, a := range t.Agents {
		if a.Source == sourceLeader {
			k := [2]string{a.MachineID, a.SessionID}
			tb.leaderAgent[k] = append(tb.leaderAgent[k], t.ID)
		}
	}
}

type treeAttach struct {
	task  string
	agent [2]string
}

func newTreeBuild(s *Service, main string, leads []string, all []listedTodo, lists map[string][]proto.Session,
	cards map[[2]string][]proto.Interaction, heldOnly map[[2]string]bool, reached map[string]bool, now time.Time, loc *time.Location, cfg Settings) *treeBuild {
	tb := &treeBuild{s: s, main: main, leads: leads, todosV2: cfg.todosV2On(), todos: map[string]listedTodo{}, views: map[string]assistantwire.TodoSummary{},
		carried: map[string]bool{}, heldOnly: heldOnly, reached: reached, now: now,
		sessions: map[[2]string]proto.Session{}, lists: lists, listed: map[string]bool{}, codexKid: map[[2]string][]proto.Session{},
		leaderAgent: map[[2]string][]string{}, workerAt: map[string]treeAttach{}, workersUnder: map[treeAttach][]string{}}
	for machine, list := range lists {
		tb.listed[machine] = true
		for _, c := range list {
			c.MachineID = machine
			tb.sessions[[2]string{machine, c.SessionID}] = c
			if c.ParentSessionID != "" && treeGoing(c) {
				k := [2]string{machine, c.ParentSessionID}
				tb.codexKid[k] = append(tb.codexKid[k], c)
			}
		}
	}
	for k := range tb.codexKid {
		slices.SortFunc(tb.codexKid[k], func(a, b proto.Session) int { return cmp.Compare(a.SessionID, b.SessionID) })
	}
	look := func(machine, id string) (proto.Session, bool) {
		c, ok := tb.sessions[[2]string{machine, id}]
		return c, ok
	}
	facts := make([]projection.Facts, len(all))
	for i := range all {
		f := projectionFacts(all[i], tb.sessions, loc)
		applyTodosV2FactsConfig(&f, cfg)
		if all[i].DoneAt == nil || all[i].StartError != "" {
			f.Unreached = !reached[all[i].ID]
			linked := hasParkInteractionLink(all[i].Supervision)
			for _, a := range all[i].Agents {
				if a.ReleasedAt != nil && !linked || a.DismissedAt != nil {
					continue
				}
				f.Interactions = append(f.Interactions, cards[[2]string{s.canonical(a.MachineID), a.SessionID}]...)
			}
		}
		facts[i] = f
	}
	projViews := projection.Views(facts, now)
	if cfg.todosV2On() {
		tb.placed = make(map[string]todoTriage, len(all))
		for i := range all {
			tb.views[all[i].ID] = projViews[i]
			tb.placed[all[i].ID] = todoTriageFromProjectionView(all[i], projViews[i], look)
		}
		for i, t := range all {
			tb.index(t)
			if projectionViewCountsAsOpen(projViews[i]) {
				tb.open = append(tb.open, t.ID)
			}
		}
	} else {
		tb.placed = triagePlace(all, look, now, loc)
		for i := range all {
			tb.views[all[i].ID] = projViews[i]
		}
		for _, t := range all {
			tb.index(t)
			if triageCountsAsOpen(t.Todo, tb.placed[t.ID]) {
				tb.open = append(tb.open, t.ID)
			}
		}
	}
	for _, id := range tb.open {
		if at, ok := tb.leaderOf(tb.todos[id].Todo); ok {
			tb.workerAt[id] = at
		}
	}
	// A worker whose chain of leaders comes back to it is listed on its own, so nothing is lost in a loop. Each chain
	// is walked over the nesting as it was before any is undone, so which tasks come out does not depend on the order
	// they are looked at: every task in a loop, and only those (one leading into a loop stays nested).
	var loop []string
	for _, id := range tb.open {
		seen := map[string]bool{}
		for at, ok := tb.workerAt[id]; ok && !seen[at.task]; at, ok = tb.workerAt[at.task] {
			if at.task == id {
				loop = append(loop, id)
				break
			}
			seen[at.task] = true
		}
	}
	for _, id := range loop {
		delete(tb.workerAt, id)
	}
	for _, id := range tb.open {
		if at, ok := tb.workerAt[id]; ok {
			tb.workersUnder[at] = append(tb.workersUnder[at], id)
		}
	}
	// Match projection.Counts step roll-up: when a top-level open parent carries a PartOf step, projection.Views rolls
	// the step's NeedsYou onto the parent, so the step must not be double-counted in needs_you.
	topOpen := map[string]bool{}
	for _, id := range tb.open {
		if _, nested := tb.workerAt[id]; !nested && tb.views[id].Phase != assistantwire.TodoPhaseDone {
			topOpen[id] = true
		}
	}
	for _, id := range tb.open {
		t := tb.todos[id]
		if todoCarriedByParent(t.Todo, topOpen[t.PartOf]) {
			tb.carried[t.ID] = true
		}
	}
	return tb
}

// treeGoing: a Codex sub-agent session not finished (working, or waiting for the owner).
func treeGoing(c proto.Session) bool {
	st := triageStateOf(c)
	return st == proto.StateWorking || st == proto.StateNeedsYou
}

func (tb *treeBuild) isOpen(id string) bool {
	_, ok := slices.BinarySearch(tb.open, id)
	return ok
}

// leaderOf is where worker task t is listed under a leader Agent: the open leader task whose active leader Agent its
// supervisor names, else (a leader started again) the current leader Agent of the open leader task its worker row
// names (leaderSupervisor's rule). False for any other task, and for a worker whose leader's task is not open.
func (tb *treeBuild) leaderOf(t Todo) (treeAttach, bool) {
	if t.Supervision == nil {
		return treeAttach{}, false
	}
	machine, sid, ok := parseLeaderRef(t.Supervision.Supervisor)
	if !ok {
		return treeAttach{}, false
	}
	k := [2]string{tb.s.canonical(machine), sid}
	for _, id := range tb.leaderAgent[k] {
		if id != t.ID && tb.isOpen(id) && treeActiveLeader(tb.todos[id].Todo, k) {
			return treeAttach{task: id, agent: k}, true
		}
	}
	var best TodoAgent
	var at treeAttach
	found := false
	for _, a := range t.Agents {
		if a.Source != sourceWorker || a.Via == "" || a.Via == t.ID || !tb.isOpen(a.Via) {
			continue
		}
		for _, l := range tb.todos[a.Via].Agents {
			if l.Source == sourceLeader && l.active() && (!found || l.LinkedAt.After(best.LinkedAt)) {
				best, found = l, true
				at = treeAttach{task: a.Via, agent: [2]string{l.MachineID, l.SessionID}}
			}
		}
	}
	return at, found
}

// treeActiveLeader: k is an active leader Agent of t.
func treeActiveLeader(t Todo, k [2]string) bool {
	return slices.ContainsFunc(t.Agents, func(a TodoAgent) bool {
		return a.Source == sourceLeader && a.active() && a.MachineID == k[0] && a.SessionID == k[1]
	})
}

// taskOwner is t's supervisor, or its task leader's own supervisor for a worker listed on its own.
func (tb *treeBuild) taskOwner(t Todo) string {
	if t.Supervision == nil {
		return ""
	}
	sup := t.Supervision.Supervisor
	if machine, sid, ok := parseLeaderRef(sup); ok {
		sup = ""
		// Todo reads use different row orders. The lowest ID gives the same owner in each read.
		best := ""
		for _, id := range tb.leaderAgent[[2]string{tb.s.canonical(machine), sid}] {
			if l := tb.todos[id].Todo; l.Supervision != nil && (best == "" || id < best) {
				if _, _, ref := parseLeaderRef(l.Supervision.Supervisor); !ref {
					best, sup = id, l.Supervision.Supervisor
				}
			}
		}
	}
	return sup
}

// attributedDirection is shared by the board, open tree and ended-task reads in v2. Legacy reads use the supervisor.
func (tb *treeBuild) attributedDirection(t Todo) string {
	if tb.todosV2 {
		return tb.effectiveDirs[t.ID]
	}
	return tb.taskOwner(t)
}

// directionOf places open work under a current leader or under the secretary.
func (tb *treeBuild) directionOf(t Todo) string {
	if id := tb.attributedDirection(t); id != "" && slices.Contains(tb.leads, id) {
		return id
	}
	return ""
}

// isUserCheck: an open task accepted by its supervisor awaiting optional owner verification (not blocking Needs you).
func isUserCheck(t listedTodo) bool {
	return t.DoneAt == nil && triageReviewState(t.Todo) == reviewUserCheck
}

// state is listed task t's state in the tree (first match wins). Needs you follows the authoritative to-do projection
// (todo/projection), so sidebar direction counts, GET /api/v1/todos counts.needs_you, and main chat Needs you agree on
// genuine owner-action items: supervisor-held (Delegating-only) cards, supervised failed starts and unescalated
// interrupted runs, deferred can_wait decisions, user_check verification, and dismissed decisions all stay out of
// needs_you. user_check sits in review (with counts.user_check) as the secondary "to verify" layer.
func (tb *treeBuild) state(t listedTodo) string {
	tr := tb.placed[t.ID]
	v, hasView := tb.views[t.ID]
	needsYou := tr.Section == triageWaiting
	if hasView {
		needsYou = projectionViewNeedsYou(v, t.Quiet) && !tb.carried[t.ID]
	}
	ask := triageOpenAsk(t.Ask)
	verifyAsk := ask != nil && (ask.Outcome == "" || ask.Outcome == outcomeDone)
	switch {
	case needsYou:
		return treeNeedsYou
	case isUserCheck(t):
		return treeReview
	case tr.Section != triageWorking && (t.Supervision.state(t.Todo) == reviewPending || verifyAsk):
		return treeReview
	case notStarted(t.Todo) && !(hasView && v.Section == assistantwire.TodoListSectionWorking):
		return treePlanned
	case taskStuck(t.Todo, tr, tb.reached[t.ID], tb.now):
		return treeStuck
	}
	return treeWorking
}

// tree is the directions in order with the secretary group last.
func (tb *treeBuild) tree(names map[string]string) directionTree {
	groups := make(map[string]*treeDirection, len(tb.leads)+1)
	out := directionTree{Directions: make([]treeDirection, 0, len(tb.leads)+1)}
	for _, id := range tb.leads {
		groups[id] = &treeDirection{ID: id, Name: names[id], Tasks: []treeTask{}}
	}
	sec := &treeDirection{ID: tb.main, Secretary: true, Tasks: []treeTask{}}
	for _, id := range tb.open {
		if _, nested := tb.workerAt[id]; nested {
			continue
		}
		t := tb.todos[id]
		g := sec
		if lead := tb.directionOf(t.Todo); lead != "" {
			g = groups[lead]
		}
		state := tb.state(t)
		g.Counts.add(state)
		if state == treeReview && isUserCheck(t) {
			g.Counts.UserCheck++
		}
		agents := tb.agents(t.ID, map[[2]string]bool{})
		g.Running = g.Running || treeRunning(agents)
		g.Tasks = append(g.Tasks, treeTask{ID: t.ID, Title: t.Title, State: state, Agents: agents,
			Responsible: cloneResponsible(t.Responsible), last: tb.placed[id].Last})
	}
	for _, g := range append(slicesOf(groups, tb.leads), sec) {
		slices.SortStableFunc(g.Tasks, func(a, b treeTask) int {
			return cmp.Or(cmp.Compare(slices.Index(treeStates, a.State), slices.Index(treeStates, b.State)),
				b.last.Compare(a.last), cmp.Compare(a.ID, b.ID))
		})
		out.Directions = append(out.Directions, *g)
	}
	return out
}

// treeRunning: one of these Agents, or one under them, is at work (its session working as its row shows it).
func treeRunning(agents []treeAgent) bool {
	return slices.ContainsFunc(agents, func(a treeAgent) bool { return a.State == proto.StateWorking || treeRunning(a.Children) })
}

func slicesOf(m map[string]*treeDirection, ids []string) []*treeDirection {
	out := make([]*treeDirection, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out
}

// agents are task id's active Agents as nodes: one spawned by another Agent of the task under it, the rest at the top
// (in the order the task has them); seen keeps each Agent to one place in the task's subtree.
func (tb *treeBuild) agents(id string, seen map[[2]string]bool) []treeAgent {
	t := tb.todos[id].Todo
	var active []TodoAgent
	mine := map[[2]string]bool{}
	for _, a := range t.Agents {
		if a.active() {
			active = append(active, a)
			mine[[2]string{a.MachineID, a.SessionID}] = true
		}
	}
	under := func(a TodoAgent) ([2]string, bool) { // the Agent of this task that spawned a
		if a.Source != sourceSpawned {
			return [2]string{}, false
		}
		m, sid, ok := parseLeaderRef(a.Via)
		k := [2]string{tb.s.canonical(m), sid}
		return k, ok && mine[k] && k != [2]string{a.MachineID, a.SessionID}
	}
	var node func(a TodoAgent) treeAgent
	node = func(a TodoAgent) treeAgent {
		k := [2]string{a.MachineID, a.SessionID}
		seen[k] = true
		n := tb.sessionNode(a.MachineID, a.SessionID, a.Title, a.Agent)
		var kids []treeAgent
		for _, c := range active {
			if p, ok := under(c); ok && p == k && !seen[[2]string{c.MachineID, c.SessionID}] {
				kids = append(kids, node(c))
			}
		}
		for _, w := range tb.workersUnder[treeAttach{task: id, agent: k}] {
			kids = append(kids, tb.agents(w, seen)...)
		}
		n.Children = append(kids, tb.sessionKids(a.MachineID, a.SessionID, a.Agent, seen)...)
		return n
	}
	out := []treeAgent{}
	for _, a := range active {
		if _, ok := under(a); !ok && !seen[[2]string{a.MachineID, a.SessionID}] {
			out = append(out, node(a))
		}
	}
	for _, a := range active { // spawned by one another in a loop: at the top, so none is lost
		if !seen[[2]string{a.MachineID, a.SessionID}] {
			out = append(out, node(a))
		}
	}
	return out
}

// sessionNode is the node of session machine/id: its title and state as the apps get them (title the to-do's own
// when the session has none or was not found). A session whose pending cards are all held by a supervisor is working,
// not waiting on the user.
func (tb *treeBuild) sessionNode(machine, id, title, agent string) treeAgent {
	n := treeAgent{MachineID: machine, SessionID: id, Title: title, Agent: agent}
	if c, ok := tb.sessions[[2]string{machine, id}]; ok {
		st := triageStateOf(c)
		if st == proto.StateNeedsYou && tb.heldOnly[[2]string{tb.s.canonical(machine), id}] {
			st = proto.StateWorking
		}
		n.Title, n.Agent, n.State = cmp.Or(c.Title, title), cmp.Or(agent, c.Agent), st
	}
	return n
}

// sessionKids are the Agents under session machine/id that are not to-do Agents: its reported in-session sub-agents
// (nested by the sub-agent that launched each) and its Codex sub-agent sessions still going.
func (tb *treeBuild) sessionKids(machine, id, agent string, seen map[[2]string]bool) []treeAgent {
	var out []treeAgent
	if c, ok := tb.sessions[[2]string{machine, id}]; ok {
		reported := map[string]bool{}
		byParent := map[string][]int{}
		for i, a := range c.Subagents {
			reported[a.ID] = true
			byParent[a.ParentID] = append(byParent[a.ParentID], i)
		}
		var appendNode func([]treeAgent, proto.Subagent) []treeAgent
		appendNode = func(nodes []treeAgent, a proto.Subagent) []treeAgent {
			sid := cmp.Or(a.SessionID, id+"@"+a.ID)
			key := [2]string{machine, sid}
			if seen[key] {
				return nodes
			}
			seen[key] = true
			n := treeAgent{MachineID: machine, SessionID: sid, Title: cmp.Or(a.Description, a.Type),
				Agent: cmp.Or(agent, c.Agent), State: cond(a.Running, proto.StateWorking, proto.StateIdle)}
			for _, i := range byParent[a.ID] {
				n.Children = appendNode(n.Children, c.Subagents[i])
			}
			return append(nodes, n)
		}
		for _, a := range c.Subagents {
			if a.ParentID == "" || a.ParentID == a.ID || !reported[a.ParentID] {
				out = appendNode(out, a)
			}
		}
		for _, a := range c.Subagents { // launched by one another in a loop: at the top, so none is lost
			out = appendNode(out, a)
		}
	}
	for _, kid := range tb.codexKid[[2]string{machine, id}] {
		k := [2]string{machine, kid.SessionID}
		if seen[k] {
			continue
		}
		seen[k] = true
		n := tb.sessionNode(machine, kid.SessionID, "", kid.Agent)
		n.Children = tb.sessionKids(machine, kid.SessionID, kid.Agent, seen)
		out = append(out, n)
	}
	return out
}
