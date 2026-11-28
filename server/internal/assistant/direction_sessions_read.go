package assistant

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	directionEndedTasksPage    = 20
	directionEndedTasksMaxPage = 100
)

// Where a placed session stands when its own state cannot be shown (directionLook.standing). A session's own states
// are the session rows' (proto.StateWorking …, "offline" among them: a stopped session on a connected computer).
const (
	// standingComputerOffline: its computer is not connected now; the session is shown as filed, its state unknown.
	standingComputerOffline = "computer_offline"
	// standingNotListed: its computer is connected and does not list the session: archived, quiet enough to have left
	// the list, or no longer on that computer.
	standingNotListed = "not_listed"
	// standingComputerRemoved: its computer is no longer one of this manager's computers.
	standingComputerRemoved = "computer_removed"
	// standingListUnknown: its computer is connected and has not given its list on this connection (yet, or giving it
	// failed): whether it lists the session is not known.
	standingListUnknown = "list_unknown"
)

// How a session got into a direction: the owner filed it, or the direction's leader conversation opened it.
const (
	directionSourceOwner  = "owner"
	directionSourceLeader = "leader"
)

// directionSession is one owner-filed or leader-opened session. State is its own state when its computer is connected
// and lists it, else one of the standings above.
type directionSession struct {
	MachineID string     `json:"machine_id"`
	SessionID string     `json:"session_id"`
	Title     string     `json:"title"`
	Source    string     `json:"source"`
	At        *time.Time `json:"at"`
	State     string     `json:"state"`
}

type directionEndedTask struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	DoneAt      *time.Time `json:"done_at,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	end         time.Time
}

type directionReadResult struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	EndedAt    *time.Time           `json:"ended_at,omitempty"`
	Notes      string               `json:"notes"`
	NotesAt    *time.Time           `json:"notes_at,omitempty"`
	Progress   *leaderProgress      `json:"progress,omitempty"`
	Sessions   []directionSession   `json:"sessions"`
	EndedTasks []directionEndedTask `json:"ended_tasks"`
	More       bool                 `json:"more"`
	// SessionsUnavailable: what places sessions (the filing book, the recorded openers, the deletion record) could not
	// be read, so Sessions is empty rather than partial; the ended tasks do not depend on it.
	SessionsUnavailable bool `json:"sessions_unavailable,omitempty"`
}

type directionCandidate struct {
	direction string
	machine   string
	session   string
	title     string
	parent    string // an owner filing's Parent
	source    string
	at        time.Time
}

func (s *Service) serveDirectionRead(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if err := s.ensureTodosLoaded(ctx); err != nil {
		directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
		return
	}
	cfg := s.settings(ctx)
	current := id != "" && id != cfg.Main && slices.Contains(cfg.Leaders, id)
	record, known, ok := s.directionSnapshot(ctx, id)
	if !ok {
		directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
		return
	}
	if !current && !known {
		directionFilingRefusal(w, r, http.StatusNotFound, "direction_not_found", errDirectionNotFound)
		return
	}
	titles, titlesOK := s.conversationTitles()
	if !titlesOK {
		directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
		return
	}
	projects, projectsOK := s.projectNames(ctx)
	name := directionName(nil, titles[id].title, "")
	if known {
		name = nameOf(&record, titles[id].title, projects, projectsOK)
	}
	out := directionReadResult{ID: id, Name: name, EndedAt: record.EndedAt, Notes: record.Notes, NotesAt: record.NotesAt,
		Progress: record.Progress, Sessions: []directionSession{}, EndedTasks: []directionEndedTask{}}
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	if limit <= 0 {
		limit = directionEndedTasksPage
	}
	all := s.treeTodos()
	tb := s.directionOwners(all)
	tb.todosV2 = cfg.todosV2On()
	if tb.todosV2 {
		tb.effectiveDirs = tb.resolveEffectiveDirections(all, s.directionNames(ctx, cfg, titles, true))
	}
	tasks, more, cursorOK := tb.directionEndedTasks(id, query.Get("title"), query.Get("before"), min(limit, directionEndedTasksMaxPage))
	if !cursorOK {
		directionFilingRefusal(w, r, http.StatusConflict, "ended_task_not_found", errDirectionEndedTaskCursor)
		return
	}
	out.EndedTasks, out.More = tasks, more
	if sessions, err := s.directionReadSessions(ctx, cfg, id); err != nil {
		s.logf("direction %s sessions: %v", id, err)
		out.SessionsUnavailable = true
	} else if sessions != nil {
		out.Sessions = sessions
	}
	apierr.WriteJSON(w, out)
}

// directionReadSessions are direction id's sessions, quiet ones included: only its own are placed, and only their
// computers are asked.
func (s *Service) directionReadSessions(ctx context.Context, cfg Settings, id string) ([]directionSession, error) {
	rules, err := s.directionRulesNow(ctx, cfg)
	if err != nil {
		return nil, err
	}
	records, err := s.directionSessionRecords(ctx)
	if err != nil {
		return nil, err
	}
	look := newDirectionLook(s, nil)
	placed, err := s.directionPlacements(ctx, rules, records, func(d string) bool { return d == id }, look)
	if err != nil {
		return nil, err
	}
	return look.sessionRows(ctx, placed)[id], nil
}

// directionSessionGroups are the current directions' sessions, placed as the read places them, on the computers this
// build listed and on the others, asked once; and when one of those computers stops being shown as before (wake).
func (tb *treeBuild) directionSessionGroups(ctx context.Context, cfg Settings) (map[string][]directionSession, time.Time, error) {
	s := tb.s
	rules, err := s.directionRulesNow(ctx, cfg)
	if err != nil {
		return nil, time.Time{}, err
	}
	records, err := s.directionSessionRecords(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	look := newDirectionLook(s, tb)
	placed, err := s.directionPlacements(ctx, rules, records, func(d string) bool { return slices.Contains(tb.leads, d) }, look)
	if err != nil {
		return nil, time.Time{}, err
	}
	groups := look.sessionRows(ctx, placed)
	return groups, look.wake(tb.now), nil
}

// directionRules is what places sessions besides the book and the recorded openers, read once per answer: the
// conversations never placed (directionSpecialSessions), the ids that are or were directions, and the Agents rule 1
// gives to their task. The to-dos are loaded.
type directionRules struct {
	cfg       Settings
	special   map[[2]string]bool
	known     map[string]bool
	followers taskFollowers
}

func (s *Service) directionRulesNow(ctx context.Context, cfg Settings) (directionRules, error) {
	special, known, err := s.directionSpecialSessions(ctx, cfg)
	if err != nil {
		return directionRules{}, err
	}
	return directionRules{cfg: cfg, special: special, known: known, followers: s.taskFollowers()}, nil
}

// directionPlacements places the filed and leader-opened sessions of the directions keep accepts, by the rules of
// #11391, first match wins, never by folder or project and never by a model: (1) an Agent on an open supervised task,
// or a sub-agent session under one, follows its task and is placed here in no direction; (2) the owner's filing (the
// session's one entry, directionEntries; an empty one keeps it on its own); (3) a conversation a direction's leader
// conversation opened. A leader conversation, the main conversation, the background supervisor and a deleted
// conversation are never placed. The read, the write's limit and the tree all place sessions here; look lists the
// computers of the placed sessions, once each.
func (s *Service) directionPlacements(ctx context.Context, rules directionRules, records map[string]directionFiling,
	keep func(direction string) bool, look *directionLook) ([]directionCandidate, error) {
	origins, err := s.directionOriginRecords(ctx)
	if err != nil {
		return nil, err
	}
	entries := s.directionEntries(records)
	var candidates []directionCandidate
	for _, k := range slices.SortedFunc(maps.Keys(entries), directionKeyOrder) {
		e := entries[k]
		if !e.live() || e.Direction == "" || !keep(e.Direction) || rules.special[k] {
			continue
		}
		candidates = append(candidates, directionCandidate{direction: e.Direction, machine: k[0], session: k[1], title: e.Title,
			parent: e.Parent, source: directionSourceOwner, at: e.At})
	}
	for _, sid := range slices.Sorted(maps.Keys(origins)) {
		origin := origins[sid]
		if origin.Origin != OriginSpawned || !rules.known[origin.Ref] || !keep(origin.Ref) {
			continue
		}
		k := [2]string{s.canonical(cmp.Or(origin.MachineID, rules.cfg.MachineID)), sid}
		if e, filed := entries[k]; filed && e.live() || rules.special[k] {
			continue
		}
		candidates = append(candidates, directionCandidate{direction: origin.Ref, machine: k[0], session: sid,
			source: directionSourceLeader, at: origin.At})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	deleted, err := s.directionDeleted(ctx)
	if err != nil {
		return nil, err
	}
	placed := candidates[:0]
	for _, c := range candidates {
		k := [2]string{c.machine, c.session}
		if deleted[k] {
			continue
		}
		look.list(ctx, c.machine)
		if rules.followers.follows(k, look.parentOf(c)) {
			continue
		}
		placed = append(placed, c)
	}
	return placed, nil
}

func directionKeyOrder(a, b [2]string) int {
	return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]))
}

// directionDeleted are the deleted assistant conversations, by canonical computer and id: the deletion record read
// once, whichever computer a conversation was on.
func (s *Service) directionDeleted(ctx context.Context) (map[[2]string]bool, error) {
	b := &s.deletedConversations
	b.mu.Lock()
	if err := s.loadDeletedConversations(ctx); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	rows := slices.Collect(maps.Values(b.rows))
	b.mu.Unlock()
	out := make(map[[2]string]bool, len(rows))
	for _, row := range rows {
		out[[2]string{s.canonical(row.Machine), row.ID}] = true
	}
	return out, nil
}

// directionHeld is how many sessions the owner filed direction holds besides session except, as the read places them
// (the write's limit, with the book as records has it and the computers as look has them).
func (s *Service) directionHeld(ctx context.Context, rules directionRules, records map[string]directionFiling, direction string,
	except [2]string, look *directionLook) (int, error) {
	placed, err := s.directionPlacements(ctx, rules, records, func(d string) bool { return d == direction }, look)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range placed {
		if c.source == directionSourceOwner && [2]string{c.machine, c.session} != except {
			n++
		}
	}
	return n, nil
}

// directionLook is what placing and standing ask of the computers, each computer once: its sessions as listed now
// (shared with a tree build's), and how it stands (directionComputer).
type directionLook struct {
	s        *Service
	rows     map[[2]string]proto.Session
	listed   map[string]bool
	failed   map[string]error
	computer map[string]directionComputer
	// frozen: asks no computer (a write holds the book's lock); one not asked yet is noted in missing instead.
	frozen  bool
	missing map[string]bool
}

// directionComputer is how a computer stands for the sessions on it. state is "" when it is one of this manager's
// computers and connected, standingComputerOffline or standingComputerRemoved; listed: its list is its node's
// (standingListUnknown when not); until: it is shown as before until then while its node is away (zero: it is not).
type directionComputer struct {
	state  string
	listed bool
	until  time.Time
}

func newDirectionLook(s *Service, tb *treeBuild) *directionLook {
	l := &directionLook{s: s, rows: map[[2]string]proto.Session{}, listed: map[string]bool{}, failed: map[string]error{},
		computer: map[string]directionComputer{}}
	if tb != nil {
		l.rows, l.listed = tb.sessions, tb.listed
	}
	return l
}

// list reads machine's sessions, once.
func (l *directionLook) list(ctx context.Context, machine string) {
	if l.listed[machine] || l.failed[machine] != nil {
		return
	}
	if l.frozen {
		if l.missing == nil {
			l.missing = map[string]bool{}
		}
		l.missing[machine] = true
		return
	}
	rows, err := l.s.sessions(ctx, machine)
	if err != nil {
		l.failed[machine] = err
		return
	}
	l.listed[machine] = true
	for _, row := range rows {
		l.rows[[2]string{machine, row.SessionID}] = row
	}
}

// wake is the earliest time after now a computer asked stops being shown as before (zero: none will): what places
// sessions there may read otherwise then, and nothing tells the manager (directionTreeAt).
func (l *directionLook) wake(now time.Time) time.Time {
	var at time.Time
	for _, c := range l.computer {
		if c.until.After(now) && (at.IsZero() || c.until.Before(at)) {
			at = c.until
		}
	}
	return at
}

// parentOf is placement rule 1's parent of a session on c's computer: its row's, else for c itself the parent its
// filing kept.
func (l *directionLook) parentOf(c directionCandidate) func(sid string) string {
	return func(sid string) string {
		if row, ok := l.rows[[2]string{c.machine, sid}]; ok {
			return row.ParentSessionID
		}
		return cond(sid == c.session, c.parent, "")
	}
}

func (l *directionLook) computerState(machine string) directionComputer {
	if c, ok := l.computer[machine]; ok {
		return c
	}
	c := l.s.computerState(machine)
	l.computer[machine] = c
	return c
}

// computerState is how machine stands, from the devices: connected or not is the computer's own connection (online or
// reachable, the readiness check's rule), never whether its list could be read, since the manager answers a computer
// that left from what it last listed. While the devices still show a computer as before (its reconnect grace, a warm
// start, a resident's reload) it counts as connected with its last list, as everywhere else in the fleet, and the tree
// is built again when that ends (wake). Without the manager's own fleet (a hub answers the lists) its list decides
// (standing).
func (s *Service) computerState(machine string) directionComputer {
	c := directionComputer{listed: true}
	if s.fleetOn() {
		d, ok := s.devs.Summary(machine)
		switch {
		case !ok:
			c.state = standingComputerRemoved
		case !d.Online && !d.Reachable:
			c.state = standingComputerOffline
		}
		if shown, ok := s.devs.Shown(machine); ok {
			c.listed, c.until = shown.Listed, shown.Until
		}
	}
	return c
}

// standing is where placed session c stands (directionSession.State), its row when its computer listed one (for its
// title), and whether its computer shows it deleted since, which leaves it out.
func (l *directionLook) standing(ctx context.Context, c directionCandidate) (state string, row proto.Session, found, deleted bool) {
	l.list(ctx, c.machine)
	row, found = l.rows[[2]string{c.machine, c.session}]
	if found && row.IsDeleted() {
		return "", row, found, true
	}
	computer := l.computerState(c.machine)
	if computer.state != "" {
		return computer.state, row, found, false
	}
	switch err := l.failed[c.machine]; {
	case errors.Is(err, ErrNoDevice):
		return standingComputerRemoved, row, found, false
	case err != nil:
		return standingComputerOffline, row, found, false
	case !found && !computer.listed:
		return standingListUnknown, row, found, false
	case !found || row.Archived:
		return standingNotListed, row, found, false
	}
	return triageStateOf(row), row, found, false
}

// sessionRows are the placed sessions' rows by direction, in computer and session order.
func (l *directionLook) sessionRows(ctx context.Context, placed []directionCandidate) map[string][]directionSession {
	titles, _ := l.s.conversationTitles()
	groups := map[string][]directionSession{}
	for _, c := range placed {
		state, live, found, deleted := l.standing(ctx, c)
		if deleted {
			continue
		}
		at := c.at.UTC()
		row := directionSession{MachineID: c.machine, SessionID: c.session, Title: c.title, Source: c.source, At: &at, State: state}
		if found && (row.Title == "" || c.source == directionSourceLeader) {
			row.Title = live.Title
		}
		if row.Title == "" {
			row.Title = titles[c.session].title
		}
		groups[c.direction] = append(groups[c.direction], row)
	}
	for direction := range groups {
		slices.SortFunc(groups[direction], func(a, b directionSession) int {
			return cmp.Or(cmp.Compare(a.MachineID, b.MachineID), cmp.Compare(a.SessionID, b.SessionID))
		})
	}
	return groups
}

// directionEndedTasks uses v2 board attribution, including historical directions. A failed start still waiting for
// its owner is open.
func (tb *treeBuild) directionEndedTasks(direction, title, before string, limit int) (page []directionEndedTask, more, ok bool) {
	filter := strings.ToLower(strings.TrimSpace(title))
	var all []directionEndedTask
	for _, id := range slices.Sorted(maps.Keys(tb.todos)) {
		t := tb.todos[id].Todo
		if tb.attributedDirection(t) != direction || t.DoneAt == nil && t.CancelledAt == nil || !t.closedForGood() && t.CancelledAt == nil {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(t.Title), filter) {
			continue
		}
		end := t.DoneAt
		state := "done"
		if t.Cancelled() {
			state = "cancelled"
		}
		if t.CancelledAt != nil {
			end = t.CancelledAt
		}
		row := directionEndedTask{ID: t.ID, Title: t.Title, State: state, DoneAt: t.DoneAt, CancelledAt: t.CancelledAt}
		if end != nil {
			row.end = *end
		}
		all = append(all, row)
	}
	slices.SortFunc(all, func(a, b directionEndedTask) int {
		return cmp.Or(b.end.Compare(a.end), cmp.Compare(a.ID, b.ID))
	})
	start := 0
	if before != "" {
		start = slices.IndexFunc(all, func(t directionEndedTask) bool { return t.ID == before }) + 1
		if start == 0 {
			return nil, false, false
		}
	}
	end := min(len(all), start+limit)
	page = append([]directionEndedTask{}, all[start:end]...)
	return page, end < len(all), true
}

// directionTreeSessionRows keeps a direction's sessions that are at work or wait on the owner, and none the tree lists
// under a task already: an Agent of a task, one under it (its sub-agent sessions included) and an owner's own,
// unsupervised to-do's. Quiet sessions, and those whose computer is offline or that are not listed, are left to the
// single-direction read.
func (tb *treeBuild) directionTreeSessionRows(groups map[string][]directionSession, tree directionTree) {
	onTask := map[[2]string]bool{}
	var mark func(agents []treeAgent)
	mark = func(agents []treeAgent) {
		for _, a := range agents {
			onTask[[2]string{tb.s.canonical(a.MachineID), a.SessionID}] = true
			mark(a.Children)
		}
	}
	for _, d := range tree.Directions {
		for _, t := range d.Tasks {
			mark(t.Agents)
		}
	}
	for direction, sessions := range groups {
		active := sessions[:0]
		for _, session := range sessions {
			if !onTask[[2]string{session.MachineID, session.SessionID}] && (session.State == proto.StateWorking || session.State == proto.StateNeedsYou) {
				active = append(active, session)
			}
		}
		groups[direction] = active
	}
}
