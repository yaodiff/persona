package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Directions (docs/design/proposals/owner-at-a-glance.md "Stored", #3484): one record per leader conversation, kept in
// the assistant's store as assistant.directions (like assistant.titles), listed by GET /api/v1/assistant/leaders and
// carried on the snapshot's session views (SessionView.Direction; not yet on the apps' session rows: every field there
// costs each list copy, W1 decides).
// Who is a leader is still only assistant.leaders, and which task is whose still supervision.supervisor: a record only
// says which project the direction is filed under, who filed it, since when, and when it ended.
//
// The extensions granted leaders.read are told of each leader once per change (leader.changed): made, retitled, filed
// under another project, ended (it left assistant.leaders; nothing is deleted) or made a leader again. What they were
// last told is kept with the record, so a restart or a replay of the same state tells nothing again.

const directionsKey = "assistant.directions"

// Who filed a direction.
const (
	directionByAssistant = "assistant" // the hub, from the leader's own tasks or (H3) its routing
	directionByUser      = "user"      // the owner's choice: never redone by the hub
)

// directionsSettle is how long a change waits for the rest before the leaders are compared again.
const directionsSettle = time.Second

// directionOutside identifies a direction without a Persona conversation and, when both ids are present, the outside
// coding Agent it stands for. It is persisted in assistant_outside_directions, not in the legacy JSON direction record.
type directionOutside struct {
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// direction is one leader's record. Told is what the extensions were last told of it (nil: nothing yet).
type direction struct {
	MachineID string            `json:"machine_id,omitempty"`
	Outside   *directionOutside `json:"-"`
	Project   string            `json:"project,omitempty"`
	By        string            `json:"by"`
	At        time.Time         `json:"at"`
	EndedAt   *time.Time        `json:"ended_at,omitempty"`
	Told      *leaderTold       `json:"told,omitempty"`
	// Name: the name the owner or the secretary gave the direction (#3679, "" none: directionName falls back); NameBy
	// who gave it (directionByUser or directionByAssistant).
	Name   string `json:"name,omitempty"`
	NameBy string `json:"name_by,omitempty"`
	// Progress: the leader's latest own summary (leader_progress, #3679).
	Progress *leaderProgress `json:"progress,omitempty"`
	// Notes: what the owner wrote for this direction (#10086, direction_notes.go), "" none; NotesAt when, at
	// millisecond precision (older records: finer). NotesLastAt: the last NotesAt issued, kept when the notes are
	// cleared, so a later save never issues it again (nil on older records: NotesAt is).
	Notes       string     `json:"notes,omitempty"`
	NotesAt     *time.Time `json:"notes_at,omitempty"`
	NotesLastAt *time.Time `json:"notes_last_at,omitempty"`
	// NotesInEffect: the notes block the conversation was last given (notesInEffect, #11392): set by a send that found
	// it idle (its leader's block, or "" once it is no leader) and, before any did, by the first notes save while it
	// leads (the block of the notes before that save); nil while unknown. RoleInEffect is the role policy sent with it.
	// A direction ended or made again keeps both until an idle send, so a working process never gets a new fingerprint.
	NotesInEffect *string `json:"notes_in_effect,omitempty"`
	RoleInEffect  string  `json:"role_in_effect,omitempty"`
}

type leaderTold struct {
	Title   string `json:"title"`
	Name    string `json:"name,omitempty"`
	Project string `json:"project,omitempty"`
	Ended   bool   `json:"ended,omitempty"`
}

// titlePrefix is an assistant conversation title's "MMDD | kind | " (titles.go), which a direction's name leaves out.
var titlePrefix = lazyre.New(`^\s*\d{4}\s*\|[^|]*\|\s*`)

// titleRole is the wrapping a leader's title often has when its first message set its role ("你是「基建」这条线的主管…",
// i18n-ignore: matches the owner's own words): the name is what the brackets hold (#3744).
var titleRole = lazyre.New(`^你是「([^」]+)」`) // i18n-ignore

// directionName is what a direction is called (#3679): the name the owner or the secretary gave it, else its
// knowledge-base project's name, else its conversation's title without the "MMDD | kind | " prefix and, when what is
// left starts 你是「X」 (a role given in the first message), just X (the title as it is when nothing is left).
func directionName(d *direction, title, projectName string) string {
	if d != nil && d.Name != "" {
		return d.Name
	}
	if projectName != "" {
		return projectName
	}
	if t := strings.TrimSpace(titlePrefix.ReplaceAllString(title, "")); t != "" {
		if m := titleRole.FindStringSubmatch(t); m != nil && strings.TrimSpace(m[1]) != "" {
			return strings.TrimSpace(m[1])
		}
		return t
	}
	return title
}

// projectNames are the knowledge base's project names by slug; false when it could not be read.
func (s *Service) projectNames(ctx context.Context) (map[string]string, bool) {
	e, err := s.kbGet(ctx)
	if err != nil || e == nil {
		return nil, false
	}
	out := map[string]string{}
	for _, p := range e.Store.Projects() {
		out[p.Slug] = p.Name
	}
	return out, true
}

// nameOf is direction d's name with the project names pn (ok false: unreadable; then a project's name last told
// stands, rather than telling the extensions another name only for want of the knowledge base).
func nameOf(d *direction, title string, pn map[string]string, ok bool) string {
	if d != nil && d.Name == "" && d.Project != "" && !ok && d.Told != nil && d.Told.Name != "" {
		return d.Told.Name
	}
	return directionName(d, title, pn[d.Project])
}

type directionBook struct {
	mu         sync.Mutex
	loaded     bool
	m          map[string]*direction // by leader conversation id
	todoLabels map[string]string     // last labels observed by directionsSync, including fallbacks
	armed      bool                  // a compare is scheduled
	after      func(d time.Duration, f func())
	now        func() time.Time // tests: the clock notes are saved and the owner's messages are aged by (nil: time.Now)
	// inflight: by conversation, the moves of its notes block in effect and the owner's messages whose outcomes are
	// not yet known (direction_notes.go notesSendOutcome); ownerSeen: the owner's latest message ids it gave parameters
	// for (notesOwnerCarry); namedOn: one more than the newest connection to the hub on which a notice named its
	// message (hubConnOf; 0 none), so its notices name their messages (notesOwnerOutcome). In memory only.
	inflight  map[string]*notesFlight
	ownerSeen map[string][]string
	namedOn   uint64
}

// clock is now on b's clock; b.mu is held.
func (b *directionBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// outsideDirectionStore is the manager database's conversationless-direction persistence. It is separate from Store's
// base settings contract so lightweight assistant stores keep their existing behavior; outside creation requires it.
type outsideDirectionStore interface {
	OutsideDirections(context.Context) (map[string]db.OutsideDirection, error)
	CreateOutsideDirection(context.Context, string, db.OutsideDirection, map[string]any) error
}

// directionsLocked reads the book once; b.mu is held. A failed read leaves it unloaded: nothing is told or saved over it.
func (s *Service) directionsLocked(ctx context.Context) bool {
	b := &s.directions
	if b.loaded {
		return true
	}
	m := map[string]*direction{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, directionsKey, &m); err != nil {
			s.logf("load assistant directions: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*direction{}
	}
	if st, ok := s.store().(outsideDirectionStore); ok {
		outside, err := st.OutsideDirections(ctx)
		if err != nil {
			s.logf("load outside directions: %v", err)
			return false
		}
		for id, ref := range outside {
			if d := m[id]; d != nil {
				d.Outside = &directionOutside{MachineID: ref.MachineID, SessionID: ref.SessionID}
			}
		}
	}
	b.m, b.loaded = m, true
	return true
}

func (s *Service) saveDirectionsLocked(ctx context.Context) {
	if err := s.saveDirectionsCheckedLocked(ctx); err != nil {
		s.logf("save assistant directions: %v", err)
	}
}

// saveDirectionsCheckedLocked durably saves the book, returning a failure an authoritative writer must not hide.
func (s *Service) saveDirectionsCheckedLocked(ctx context.Context) error {
	st := s.store()
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return st.PutSetting(ctx, directionsKey, s.directions.m)
}

// directionsSoon schedules comparing the leaders with what the extensions were told, once the changes of the next
// directionsSettle are in. It is called whenever the snapshot is published (titles, settings and todos all publish),
// so no writer of a leader, a title or a project has to remember it.
func (s *Service) directionsSoon() {
	b := &s.directions
	b.mu.Lock()
	if b.armed {
		b.mu.Unlock()
		return
	}
	b.armed = true
	after := b.after
	b.mu.Unlock()
	f := func() {
		s.directionsSync(context.Background())
		s.leaderReportsSync(context.Background(), time.Now(), false) // a review just written (leader_reports.go)
		// A name or a record changed without a publish (setDirectionName, a new leader's project) changes the
		// direction tree's names too; an unchanged tree is not told again (direction_tree.go).
		s.directionTreeSoon()
	}
	if after != nil {
		after(directionsSettle, f)
		return
	}
	time.AfterFunc(directionsSettle, f)
}

// directionsSync brings the records up to assistant.leaders and tells the extensions what changed: a leader without a
// record gets one; one that left the list is ended; one made a leader again is no longer ended. Each leader's title and
// project are compared with what was last told; a difference is told once and remembered.
func (s *Service) directionsSync(ctx context.Context) {
	b := &s.directions
	b.mu.Lock()
	b.armed = false
	b.mu.Unlock()
	if !s.todosLoaded() {
		return // the projects come from the todos: the next publish after they load compares again
	}
	cfg := s.settings(ctx)
	titles, ok := s.conversationTitles()
	if !ok {
		return
	}
	projects := s.leaderProjects()
	pnames, pok := s.projectNames(ctx)
	now := time.Now().UTC()
	tell := s.todoEventsOn()

	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return
	}
	changed := false
	var notices []assistantwire.LeaderChangedEvent
	leaders := map[string]bool{}
	for _, id := range cfg.Leaders {
		if id == "" || id == cfg.Main {
			continue
		}
		leaders[id] = true
		d := b.m[id]
		if d == nil {
			d = &direction{MachineID: cfg.MachineID, By: directionByAssistant, At: now}
			b.m[id], changed = d, true
		}
		if d.EndedAt != nil {
			d.EndedAt, changed = nil, true
		}
		if d.Project == "" && d.By == directionByAssistant && projects[id] != "" { // never one the owner filed
			d.Project, changed = projects[id], true
		}
	}
	ids := make([]string, 0, len(b.m))
	for id := range b.m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	labels := make(map[string]string, len(ids))
	for _, id := range ids {
		d := b.m[id]
		if !leaders[id] && d.EndedAt == nil {
			d.EndedAt, changed = &now, true
		}
		curName := nameOf(d, titles[id].title, pnames, pok)
		if id != "" && id != cfg.Main {
			labels[id] = curName
		}
		curTitle := titles[id].title
		if d.Outside != nil {
			curTitle = curName // no conversation title exists; event consumers still get a useful non-empty title
		}
		cur := leaderTold{Title: curTitle, Name: curName, Project: d.Project, Ended: d.EndedAt != nil}
		if d.Told != nil && *d.Told == cur || !tell {
			continue
		}
		if d.Told == nil && cur.Ended {
			d.Told, changed = &cur, true // ended before anyone was told of it: nothing to end
			continue
		}
		d.Told, changed = &cur, true
		notices = append(notices, assistantwire.LeaderChangedEvent{ID: id, Title: cur.Title, Name: cur.Name, Project: cur.Project, Ended: cur.Ended})
	}
	if changed {
		s.saveDirectionsLocked(ctx) // before telling: a crash between the two loses an event rather than repeating one
	}
	for _, n := range notices {
		s.todoQueue(todoNotice{assistantwire.EventLeaderChanged, n})
	}
	labelsChanged := !maps.Equal(b.todoLabels, labels)
	b.todoLabels = labels
	b.mu.Unlock()
	if labelsChanged {
		s.todoRevisionChanged()
	}
	if changed {
		go s.publish(context.WithoutCancel(ctx)) // the snapshot carries the records (SessionView.Direction)
	}
}

// leaderProjects is, for each conversation that supervises tasks, the knowledge-base project most of its tasks are
// filed under (ties: the most recent task's); "" when none is. A direction the hub files itself starts there.
func (s *Service) leaderProjects() map[string]string {
	type tally struct {
		n    int
		last time.Time
	}
	counts := map[string]map[string]*tally{}
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.Supervision == nil || t.Supervision.Supervisor == "" || t.Project == "" {
			continue
		}
		by := counts[t.Supervision.Supervisor]
		if by == nil {
			by = map[string]*tally{}
			counts[t.Supervision.Supervisor] = by
		}
		c := by[t.Project]
		if c == nil {
			c = &tally{}
			by[t.Project] = c
		}
		c.n++
		if t.CreatedAt.After(c.last) {
			c.last = t.CreatedAt
		}
	}
	s.todos.mu.RUnlock()
	out := map[string]string{}
	for lead, by := range counts {
		best, bestTally := "", tally{}
		for p, c := range by {
			if c.n > bestTally.n || c.n == bestTally.n && (c.last.After(bestTally.last) || c.last.Equal(bestTally.last) && p < best) {
				best, bestTally = p, *c
			}
		}
		out[lead] = best
	}
	return out
}

// directionViews are the records as the session lists carry them, by leader conversation id (ended ones too: their
// conversation stays, and says it was a direction); false while they could not be read.
func (s *Service) directionViews(ctx context.Context) (map[string]*proto.Direction, bool) {
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.directionsLocked(ctx) {
		return nil, false
	}
	out := make(map[string]*proto.Direction, len(b.m))
	for id, d := range b.m {
		if d.Outside != nil {
			continue // a direction id is not proof that a Persona conversation exists
		}
		out[id] = &proto.Direction{Project: d.Project, By: d.By, At: d.At, EndedAt: d.EndedAt}
	}
	return out, true
}

// leaderView is a leader as GET /api/v1/assistant/leaders lists it: what leader.changed says of it, and its record.
type leaderView struct {
	assistantwire.LeaderChangedEvent
	// External marks a direction with no Persona conversation. MachineID and SessionID together name its optional
	// outside Agent; both are empty for a standalone direction. The direction ID is never that Agent's session ID.
	External  bool   `json:"external,omitempty"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Progress: the leader's latest own summary (leader_progress), nil before its first.
	Progress *leaderProgress `json:"progress,omitempty"`
	// Notes: what the owner wrote for the direction (direction_notes.go), NotesAt when (null with no notes: the
	// value a save sends back as notes_at, #11392); the leader and the direction's new Agents are given them.
	Notes   string     `json:"notes,omitempty"`
	NotesAt *time.Time `json:"notes_at"`
	By      string     `json:"by"`
	At      time.Time  `json:"at"`
	EndedAt *time.Time `json:"ended_at,omitempty"`
}

// directionLeaderView is one direction's read model, shared by creation, progress writes and GET leaders.
func directionLeaderView(id string, d *direction, title string, pnames map[string]string, pok bool) leaderView {
	name := nameOf(d, title, pnames, pok)
	if d.Outside != nil {
		title = name
	}
	v := leaderView{LeaderChangedEvent: assistantwire.LeaderChangedEvent{ID: id, Title: title, Name: name,
		Project: d.Project, Ended: d.EndedAt != nil}, Progress: d.Progress, Notes: d.Notes, NotesAt: d.NotesAt,
		By: d.By, At: d.At, EndedAt: d.EndedAt}
	if d.Outside != nil {
		v.External, v.MachineID, v.SessionID = true, d.Outside.MachineID, d.Outside.SessionID
	}
	return v
}

// directionRoutes: GET /api/v1/assistant/leaders lists every direction, current leaders first, then ended ones, each as
// leader.changed would tell it now. An extension granted leaders.read reads it once to learn the leaders it was not
// told of (installed after they were made); the events keep it up to date from then on.
func (s *Service) directionRoutes() {
	s.handle("GET /api/v1/assistant/leaders", func(w http.ResponseWriter, r *http.Request) {
		titles, ok := s.conversationTitles()
		pnames, pok := s.projectNames(r.Context())
		b := &s.directions
		b.mu.Lock()
		ok = ok && s.directionsLocked(r.Context())
		if !ok {
			b.mu.Unlock()
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("the leaders could not be read"))
			return
		}
		list := make([]leaderView, 0, len(b.m))
		for id, d := range b.m {
			list = append(list, directionLeaderView(id, d, titles[id].title, pnames, pok))
		}
		b.mu.Unlock()
		slices.SortFunc(list, func(a, b leaderView) int {
			if a.Ended != b.Ended {
				return cond(a.Ended, 1, -1)
			}
			return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID))
		})
		raw, _ := json.Marshal(map[string]any{"leaders": list})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(raw)
	})
}
