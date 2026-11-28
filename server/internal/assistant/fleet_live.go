package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/assistant/fleetview"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	// Named apart: the package's tests declare a helper called outcome.
	kitoutcome "github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The "Fleet" Live Activity's content (docs/apple/live-activity.md): one activity per phone, on the lock screen and in the
// Dynamic Island, summing up every agent that needs you, is working, or finished unseen. The manager works it out
// (FleetState) from the session lists and the pending interactions, with the island's numbers (FleetStats), and
// tells the hub when it changes (fleet.live); the hub pushes it to each device's activity token at the pace Apple
// allows and gives each device its own part (its island's sides, its lock screen details, its persona).

type (
	FleetState    = assistantwire.FleetState
	FleetStats    = assistantwire.FleetStats
	FleetLoad     = assistantwire.FleetLoad
	FleetItem     = assistantwire.FleetItem
	FleetProgress = assistantwire.FleetProgress
)

// fleetMaxItems is how many agents the Live Activity lists; fleetDetailMax, fleetTitleMax and fleetPlaceMax how long
// an item's line, title and place get (fleetview, which the needs-you rows are written with too).
const (
	fleetMaxItems  = fleetview.MaxItems
	fleetDetailMax = fleetview.DetailMax
	fleetTitleMax  = fleetview.TitleMax
	fleetPlaceMax  = fleetview.PlaceMax
)

// liveState is what was told the hub last, and the needs-you arrays fleetSnapshot and linkCovers reuse (liveLoop
// keeps its own).
type liveState struct {
	mu         sync.Mutex
	sent       string
	countsTold bool // the hub has the counts view liveLoop made last (counts_view.go)
	signal     chan struct{}

	roundMu sync.Mutex
	round   fleetRound
	counts  countsTeller // liveLoop's own
}

func (l *liveState) ch() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.signal == nil {
		l.signal = make(chan struct{}, 1)
	}
	return l.signal
}

// liveChanged: what the fleet is made of may have changed; force: tell the hub even when it did not (it may have
// restarted).
func (s *Service) liveChanged(force bool) {
	if !s.fleetOn() {
		return
	}
	if force {
		s.fleet.live.mu.Lock()
		s.fleet.live.sent, s.fleet.live.countsTold = "", false
		s.fleet.live.mu.Unlock()
	}
	select {
	case s.fleet.live.ch() <- struct{}{}:
	default:
	}
}

// liveLoop tells the hub the fleet when it changed, at most every 100 ms, and looks at the numbers every 15 s; the
// devices get Persona Link's summary of the same agents, and the hub this manager's counts view of them.
func (s *Service) liveLoop(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	sig := s.fleet.live.ch()
	// Each machine's list as the last round decorated it. While the fleet keeps changing, the next round copies the
	// rows over them instead of into new arrays: a copy of every machine's list up to ten times a second was half of
	// what the manager allocated under churn (#3197). The 15 s look takes new arrays and gives these back, so a
	// fleet gone quiet holds no second copy of its lists.
	var lists map[string][]proto.Session
	var quiet []fleetInput
	round := fleetRound{logf: s.logf}
	for {
		select {
		case <-ctx.Done():
			return
		case <-sig:
			time.Sleep(100 * time.Millisecond)
			if lists == nil {
				lists = map[string][]proto.Session{}
			}
		case <-tick.C:
			lists = nil
		}
		l := s.lang(context.Background())
		agents, ixs, told := s.fleetInputsForRound(lists, &quiet)
		s.tellLive(round.buildFleet(l, agents, ixs))
		attention, counts := round.linkAndCounts(l, agents, ixs, told, quiet, s.fleet.live.counts.todos.kept())
		clear(quiet)
		quiet = quiet[:0]
		s.devs.SetAttention(attention) // sent on only when it changed
		s.tellCounts(counts)
	}
}

func (s *Service) tellLive(st FleetState) {
	msg := assistantwire.FleetLive{State: st, Stats: s.fleetStats()}
	b, _ := json.Marshal(msg)
	l := &s.fleet.live
	l.mu.Lock()
	same := l.sent == string(b)
	l.mu.Unlock()
	if same {
		return
	}
	if s.hub.Tell(assistantwire.EventFleetLive, rpc.Marshaled(b)) == nil { // json.Marshal's bytes: into the line as they are
		l.mu.Lock()
		l.sent = string(b)
		l.mu.Unlock()
	}
}

// fleetSnapshot computes the fleet from every machine's sessions and the pending interactions.
func (s *Service) fleetSnapshot() FleetState {
	agents, ixs, _ := s.fleetInputs()
	l := s.lang(context.Background())
	s.fleet.live.roundMu.Lock()
	defer s.fleet.live.roundMu.Unlock()
	return s.fleet.live.round.buildFleet(l, agents, ixs)
}

// fleetInputs is every machine's agents as the fleet sees them, the pending interactions (oldest first), and which of
// those are the user's (pendingCards). Each agent points into its machine's decorated list, a copy of its own that
// nothing else holds and the fleet only reads: liveLoop builds the fleet up to ten times a second, and a copy of every
// session per agent (1.5 KB each) was most of what that allocated.
func (s *Service) fleetInputs() ([]fleetInput, []proto.Interaction, map[string]bool) {
	return s.fleetInputsInto(nil)
}

// fleetInputsInto is fleetInputs with each machine's rows copied over the list kept for it in kept (liveLoop's own,
// from its last round), which then holds this round's lists; nil takes new arrays, as fleetInputs. Every round still
// copies and decorates every list whole, only into the arrays of the last one. The agents point into kept's lists
// until the next round: what the fleet makes of them (buildFleetIn, linkAttention) keeps strings and numbers, never a
// row or a pointer to one.
func (s *Service) fleetInputsInto(kept map[string][]proto.Session) ([]fleetInput, []proto.Interaction, map[string]bool) {
	return s.fleetInputsForRound(kept, nil)
}

// fleetInputsForRound also appends quiet tasks' agents to quiet when supplied, for Persona's card count. Keeping them
// outside agents preserves the Live Activity's side folds and the Agents badge's keys and held rows.
func (s *Service) fleetInputsForRound(kept map[string][]proto.Session, quiet *[]fleetInput) ([]fleetInput, []proto.Interaction, map[string]bool) {
	machines := s.fleetMachines()
	lists := make([][]proto.Session, len(machines))
	n := 0
	for i, m := range machines {
		// A machine listed twice would decorate into the same array twice: only its first list is kept.
		if kept == nil || slices.ContainsFunc(machines[:i], func(o fleetMachine) bool { return o.ID == m.ID }) {
			lists[i] = s.decorated(m.ID)
		} else {
			lists[i] = s.decoratedInto(m.ID, kept[m.ID])
			keepList(kept, m.ID, lists[i])
		}
		n += len(lists[i])
	}
	if len(kept) > 0 {
		maps.DeleteFunc(kept, func(id string, _ []proto.Session) bool { // the machines that went
			return !slices.ContainsFunc(machines, func(m fleetMachine) bool { return m.ID == id })
		})
	}
	s.pruneRemovedFoldMachines()
	agents := make([]fleetInput, 0, n)
	turns := s.turnsBook()
	for i, m := range machines {
		list := lists[i]
		for j := range list {
			sess := &list[j]
			visibility := fleetcountadapt.Visibility(sess)
			if visibility.LeftOut() {
				if quiet != nil && visibility == fleetcount.VisibilityQuiet() {
					*quiet = append(*quiet, fleetInput{Session: sess, Muted: s.mutedAs(m.ID, sess.SessionID, sess.SpaceName)})
				}
				continue
			}
			agents = append(agents, fleetInput{
				// Its own space, not muted's lookup of the node's row: that scanned the machine's list once per session.
				Machine: m.Name, Session: sess, Muted: s.mutedAs(m.ID, sess.SessionID, sess.SpaceName),
				TurnStart: turns.startOf(m.ID + "/" + sess.SessionID),
			})
		}
	}
	ixs, told := s.visiblePendingCards()
	return agents, ixs, told
}

// keepList keeps list as the machine's for the next round. A list shorter than the last one is in the same array
// (it did not outgrow it): the rows it left behind are cleared, so they hold nothing of the node's older lists.
func keepList(kept map[string][]proto.Session, machine string, list []proto.Session) {
	if old := kept[machine]; len(list) < len(old) {
		clear(old[len(list):])
	}
	kept[machine] = list
}

// round2 keeps two significant digits: 12345 → 12000, 987 → 990. The island shows "12k", so finer changes would
// only spend the push budget.
func round2(n int64) int64 {
	if n < 100 {
		return n
	}
	p := int64(1)
	for n/p >= 100 {
		p *= 10
	}
	return (n + p/2) / p * p
}

// fleetStats turns the usage summary into the island's numbers.
func (s *Service) fleetStats() FleetStats {
	if s.devs == nil {
		return FleetStats{}
	}
	u := s.devs.Usage()
	st := FleetStats{TPM: round2(u.TPM), Tokens: round2(u.Tokens), Requests: int(round2(int64(u.Requests)))}
	for _, m := range u.Machines {
		st.Loads = append(st.Loads, FleetLoad{ID: m.ID, Name: s.machineName(m.ID), Percent: loadPercent(m.Load)})
	}
	return st
}

// loadPercent is a machine's load per core as the island's percent, rounded to the tenth of a core the apps print
// ("1.5 per core" is 150, #12519). In steps of 5, 1.46 per core went out as 145 and showed "1.4" beside a card's
// "1.5", and half the pushes for a 5-step change drew the same text.
func loadPercent(perCore float64) int {
	return int(math.Round(perCore*10)) * 10
}

// fleetInput is one agent as the fleet sees it. Session is read, never written: it points into a list the fleet's
// caller owns.
type fleetInput struct {
	Machine   string // display name
	Session   *proto.Session
	Muted     bool
	TurnStart time.Time // zero: unknown
}

// buildFleet sums agents up the way the Agents list does (docs/apple/ios.md): needs you first (oldest request first), then
// working (longest first), then finished unseen. Muted agents, idle / offline ones, and /loop agents waiting for
// their next tick are left out; the assistant only counts when it asks something. Which agents need you, and by which
// card, is fleetcount's (fleetcount.AgentsNeedsYou, the Live Activity's surface).
func buildFleet(agents []fleetInput, ixs []proto.Interaction) FleetState {
	return buildFleetIn(i18n.Default, agents, ixs)
}

// agentKey is an agent's machine and session, what its cards name it by.
type agentKey = [2]string

func keyOf(sess *proto.Session) agentKey { return agentKey{sess.MachineID, sess.SessionID} }

// liveCounted says whether an agent is on the Live Activity at all, by fleetcount's rule (Agent.Counts): muted agents
// and subtasks are not (Codex sub-agents, and a runtime's session another one started, which its parent's row stands
// for until it needs the user), nor quiet tasks' and directory-hidden agents, which fleetInputsInto leaves out first.
// A session fleetcount refuses counts nowhere. A side session folded under its parent is not either: buildFleetIn
// counts it under that row first (foldSidesIn, #7944). foldSidesIn asks it of the rows it folds; a round's own rows
// read it from what the round built (fleetRound.onLive).
func liveCounted(a fleetInput) bool {
	agent, failed := kitoutcome.Unpack(fleetcountadapt.Agent(a.Session, a.Muted, 0))
	return failed == nil && agent.Counts(fleetcount.SurfaceLiveActivity())
}

// fleetRound is what the needs-you rows keep from one round to the next: each surface's agents and cards as the
// adapter holds them (their arrays reused, not the counting core's), the sessions the last Live Activity found waiting
// and Persona Link's cards in their order. liveLoop keeps one; the zero fleetRound is a round over new arrays. logf,
// when set, says when the number of sessions and cards fleetcount refused changes: they are left out of the rows,
// never filled in.
type fleetRound struct {
	live, link fleetcountadapt.Fleet
	waiting    []*proto.Session
	cards      []proto.Interaction
	onLive     []bool // by place in the last agents filled: fleetcount counts the agent on the Live Activity at all
	logf       func(string, ...any)
	refused    int
}

// fill puts a round's agents and cards into f; told says which cards the owner was told of (nil: none). onLive
// records, for each agent, whether fleetcount counts it on the Live Activity at all (Agent.Counts), which the working
// and finished rows follow too.
func (r *fleetRound) fill(f *fleetcountadapt.Fleet, agents []fleetInput, ixs []proto.Interaction, told map[string]bool, quiet []fleetInput) {
	f.Reset()
	r.onLive = r.onLive[:0]
	refused := 0
	var first fail.Fail
	var which string
	for i := range agents {
		a := &agents[i]
		agent, failed := kitoutcome.Unpack(f.Agent(a.Machine, a.Session, a.Muted))
		r.onLive = append(r.onLive, failed == nil && agent.Counts(fleetcount.SurfaceLiveActivity()))
		if failed != nil {
			if refused++; first == nil {
				first, which = failed, "session "+a.Session.MachineID+"/"+a.Session.SessionID
			}
		}
	}
	for i := range quiet {
		a := &quiet[i]
		if _, failed := kitoutcome.Unpack(f.PersonaAgent(a.Session, a.Muted)); failed != nil {
			if refused++; first == nil {
				first, which = failed, "session "+a.Session.MachineID+"/"+a.Session.SessionID
			}
		}
	}
	for i := range ixs {
		ix := &ixs[i]
		if _, failed := kitoutcome.Unpack(f.Card(ix, told[ix.ID])); failed != nil {
			if refused++; first == nil {
				first, which = failed, "card "+ix.ID
			}
		}
	}
	if refused != r.refused && r.logf != nil {
		if first == nil {
			r.logf("fleet: every session and card is counted again")
		} else {
			r.logf("fleet: %d sessions and cards left out of Needs you, the first %s: %s", refused, which, fleetcountadapt.Log(first))
		}
	}
	r.refused = refused
}

// foldSidesIn counts each side session the Live Activity would count on its own, and each row folded through one
// (folded_as "side" or folded_side set, fleet_fold.go), under the row it folds under, as the lists do (#7944): that
// row is counted once, in the state it shows with them (needs you when one of them does or has a card, else working
// when one works unless the row has an unseen result or awaits the user's reply, else finished unseen when a side is
// and the row does not work itself, else its own), and the cards of those rows stand for it. A row folded under one the
// fleet does not have (a quiet task's agent) counts on its own. A muted or subtask row folded through a side counts
// nowhere, as it did. Agents and ixs are returned as they are when no row folds so; otherwise new slices, the rows
// counted for others left out and each such parent a copy of its own row in that state.
func foldSidesIn(agents []fleetInput, ixs []proto.Interaction) ([]fleetInput, []proto.Interaction) {
	throughSide := func(s *proto.Session) bool {
		return s.FoldedUnder() != "" && (s.FoldedAs() == proto.FoldedAsSide || s.FoldedSide() != "")
	}
	if !slices.ContainsFunc(agents, func(a fleetInput) bool { return throughSide(a.Session) }) {
		return agents, ixs // most fleets: built ten times a second, so nothing is allocated for them
	}
	var into map[agentKey]int // a row folded through a side: the index of the agent it folds under
	at := make(map[agentKey]int, len(agents))
	for i, a := range agents {
		at[keyOf(a.Session)] = i
	}
	for _, a := range agents {
		if s := a.Session; throughSide(s) {
			if j, ok := at[agentKey{s.MachineID, s.FoldedUnder()}]; ok {
				if into == nil {
					into = map[agentKey]int{}
				}
				into[keyOf(s)] = j
			}
		}
	}
	if into == nil {
		return agents, ixs
	}
	type merged struct {
		needs, working, unread bool
		since, updated         time.Time // the earliest working row's start, the latest unseen side's update
	}
	folded := map[int]*merged{}
	for i := range agents {
		a := &agents[i]
		j, ok := into[keyOf(a.Session)]
		if !ok || !liveCounted(*a) {
			continue
		}
		m := folded[j]
		if m == nil {
			m = &merged{}
			folded[j] = m
		}
		switch s := a.Session; s.State {
		case proto.StateNeedsYou:
			m.needs = true
		case proto.StateWorking:
			if t := workingSince(a); !m.working || t.Before(m.since) {
				m.since = t
			}
			m.working = true
		case proto.StateUnread:
			if s.FoldedAs() == proto.FoldedAsSide {
				m.unread = true
				if s.UpdatedAt.After(m.updated) {
					m.updated = s.UpdatedAt
				}
			}
		}
	}
	outIxs := make([]proto.Interaction, len(ixs))
	for i, ix := range ixs {
		k := agentKey{ix.MachineID, ix.SessionID}
		if j, ok := into[k]; ok && liveCounted(agents[at[k]]) {
			if _, has := folded[j]; !has {
				folded[j] = &merged{}
			}
			p := agents[j].Session
			ix.MachineID, ix.SessionID = p.MachineID, p.SessionID // it stands for the row it folds under
		}
		outIxs[i] = ix
	}
	out := make([]fleetInput, 0, len(agents)-len(into))
	for i, a := range agents {
		if _, gone := into[keyOf(a.Session)]; gone {
			continue
		}
		if m := folded[i]; m != nil {
			s := *a.Session // a copy of the row: the list it points into is not the fleet's to change
			switch {
			case s.State == proto.StateNeedsYou || m.needs:
				s.State = proto.StateNeedsYou
			case m.working && s.State != proto.StateUnread && !(s.State == proto.StateIdle && s.AwaitsReply):
				if s.State != proto.StateWorking {
					s.State, a.TurnStart = proto.StateWorking, m.since
				}
			case m.unread && s.State != proto.StateWorking:
				if s.State != proto.StateUnread {
					s.State, s.UpdatedAt = proto.StateUnread, m.updated
				}
			}
			a.Session = &s
		}
		out = append(out, a)
	}
	return out, outIxs
}

// shownAgent is a working or finished agent the Live Activity may list, and the time it is ordered by.
type shownAgent struct {
	agent *fleetInput
	since float64
}

// buildFleetIn is buildFleet with the texts for people in l (the user's language), over arrays of its own.
func buildFleetIn(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction) FleetState {
	var r fleetRound
	return r.buildFleet(l, agents, ixs)
}

// buildFleet is buildFleetIn over the arrays the last round kept. Every agent is counted, but only the items shown
// (fleetMaxItems) are written out: their titles, places and lines are most of what a fleet costs.
func (r *fleetRound) buildFleet(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction) FleetState {
	var st FleetState
	agents, ixs = foldSidesIn(agents, ixs)
	r.fill(&r.live, agents, ixs, nil, nil)
	r.waiting = r.waiting[:0]
	r.live.LiveActivity(string(l), &st, &r.waiting, sessionTitle)
	asked := make(map[agentKey]bool, len(r.waiting))
	for _, sess := range r.waiting {
		asked[keyOf(sess)] = true
	}
	clear(r.waiting) // they point into the round's lists
	var working, unread []shownAgent
	for i := range agents {
		a := &agents[i]
		sess := a.Session
		if !r.onLive[i] || len(asked) > 0 && asked[keyOf(sess)] {
			continue
		}
		switch sess.State {
		case proto.StateWorking:
			if sess.Assistant {
				st.Assistant = true
				continue
			}
			working = append(working, shownAgent{a, fleetview.UnixSeconds(workingSince(a))})
		case proto.StateUnread:
			if sess.Assistant || sess.Loop != nil {
				continue
			}
			unread = append(unread, shownAgent{a, fleetview.UnixSeconds(sess.UpdatedAt)})
		}
	}
	sort.SliceStable(working, func(i, j int) bool { return working[i].since < working[j].since })
	sort.SliceStable(unread, func(i, j int) bool { return unread[i].since > unread[j].since })
	st.Working, st.Unread = len(working), len(unread)
	switch {
	case st.NeedsYou > 0:
		st.Phase = "needsYou"
	case st.Working > 0:
		st.Phase = "working"
	case st.Unread > 0:
		st.Phase = "done"
	}
	if len(working) > 0 {
		st.WorkingSince = working[0].since
	}
	// The round's lists are let go until the next one.
	r.live.Reset()
	if st.Items == nil { // none needs you: LiveActivity made no room for its rows
		st.Items = make([]FleetItem, 0, min(fleetMaxItems, len(working)+len(unread)))
	}
	for _, w := range working[:min(len(working), fleetMaxItems-len(st.Items))] {
		st.Items = append(st.Items, workingItem(l, w))
	}
	for _, u := range unread[:min(len(unread), fleetMaxItems-len(st.Items))] {
		sess := u.agent.Session
		st.Items = append(st.Items, FleetItem{ID: sess.MachineID + "/" + sess.SessionID, Title: fleetview.AgentTitle(sessionTitle(sess), fleetTitleMax),
			Place: fleetview.AgentPlace(u.agent.Machine, sess, fleetPlaceMax), Kind: "unread",
			Detail: clip(oneLine(firstLine(sess.LastText)), fleetDetailMax), Since: u.since})
	}
	return st
}

// workingSince is when a working agent started what it is doing: its turn, or its oldest running workflow while it
// works in the background; its last update when neither is known.
func workingSince(a *fleetInput) time.Time {
	sess := a.Session
	since := a.TurnStart
	if sess.BackgroundWorking {
		var started time.Time
		for _, w := range sess.Workflows {
			if w.Running && !w.StartedAt.IsZero() && (started.IsZero() || w.StartedAt.Before(started)) {
				started = w.StartedAt
			}
		}
		if !started.IsZero() {
			since = started
		}
	}
	if since.IsZero() {
		since = sess.UpdatedAt
	}
	return since
}

// workingItem is the item of a working agent.
func workingItem(l i18n.Lang, w shownAgent) FleetItem {
	sess := w.agent.Session
	it := FleetItem{ID: sess.MachineID + "/" + sess.SessionID, Title: fleetview.AgentTitle(sessionTitle(sess), fleetTitleMax),
		Place: fleetview.AgentPlace(w.agent.Machine, sess, fleetPlaceMax), Kind: "working", Since: w.since}
	if sess.BackgroundWorking {
		it.Background, it.Detail = true, backgroundDetail(l, sess)
		it.Progress = backgroundProgress(sess)
	}
	return it
}

// sessionTitle is an agent's title for people before it is clipped (fleetview.AgentTitle): its session's, its
// project's folder when it has none.
func sessionTitle(sess *proto.Session) string {
	t := transcript.Title(sess.Title)
	if t == "" || t == "Claude Code" {
		t = path.Base(sess.Cwd)
	}
	return t
}

// shownWorkflow is the running workflow a background-working agent's item names: the first one (Workflows are newest
// first), as the web's workflow line picks it.
func shownWorkflow(sess *proto.Session) (proto.Workflow, bool) {
	for _, w := range sess.Workflows {
		if w.Running {
			return w, true
		}
	}
	return proto.Workflow{}, false
}

// backgroundDetail is what a background-working agent runs, in a line: its running workflow ("parity-audit · Verify ·
// 169/185"), else how many background tasks. A long name or phase is what gets clipped, never the counts at the end:
// they are what the bar shows (backgroundProgress), so the line changes whenever the bar does and the bar costs no
// push of its own (#3437).
func backgroundDetail(l i18n.Lang, sess *proto.Session) string {
	if w, ok := shownWorkflow(sess); ok {
		parts := []string{cmp.Or(w.Name, w.Summary, "workflow")}
		if w.Phase != "" {
			parts = append(parts, w.Phase)
		}
		counts := fmt.Sprintf(" · %d/%d", w.AgentsDone, w.AgentsStarted)
		return clip(strings.Join(parts, " · "), max(fleetDetailMax-utf8.RuneCountInString(counts), 0)) + counts
	}
	n := len(sess.Background)
	for _, a := range sess.Subagents {
		if a.Running {
			n++
		}
	}
	return clip(i18n.T(l, "hub-liveactivity.background", i18n.Args{"count": n}), fleetDetailMax)
}

// backgroundProgress is the bar under a background-working agent's item (#3437): the counts its detail prints (which
// clipping never cuts), of the same workflow, so the bar and the line beside it agree and the bar never changes the
// content by itself (the push budget, docs/live-activity.md). Nil without a running workflow, or before it started an
// agent.
func backgroundProgress(sess *proto.Session) *FleetProgress {
	w, ok := shownWorkflow(sess)
	if !ok || w.AgentsStarted <= 0 {
		return nil
	}
	return &FleetProgress{Done: min(max(w.AgentsDone, 0), w.AgentsStarted), Started: w.AgentsStarted}
}
