package assistant

import (
	"cmp"
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Context is the <persona-context> block a message to conversation p.SessionID starts with (the node asks on every
// message, node/assistant_context.go): who the user is, whether this is the main conversation, and the snapshot; for a
// conversation in a track other than the main space, its track's own short context instead (space_context.go).
// With p.Ack it only confirms what a delivered message carried: its view errors (context_view_errors.go) and the
// background supervisor's announcement.
func (s *Service) Context(ctx context.Context, p ContextParams) ContextResult {
	if len(p.Ack) > 0 {
		// The background supervisor's announcement is confirmed the same way (supervise_background.go).
		if p.Ack = s.announcedBackground(ctx, p.SessionID, p.Ack); len(p.Ack) > 0 {
			s.ackViewErrors(ctx, p)
		}
		return ContextResult{}
	}
	if p.RecoveryOnly {
		return s.compactRecoveryContext(ctx, p)
	}
	background := s.isBackgroundSession(ctx, p.SessionID)
	snapshot := ""
	cleanRound := s.cleanRoundSession(p.SessionID)
	if !cleanRound { // a granted schedule's round reads nothing from outside (schedule_grant.go)
		if track, suppressMain := s.contextTrack(ctx, p, background); track != "" || suppressMain {
			// a conversation in a track other than the main space: its own short context, not the snapshot (space_context.go)
			text := ""
			if track != "" {
				text = s.spaceContext(ctx, track)
			}
			return s.contextResult(ctx, p, text, "", false)
		}
		snapshot = s.contextSnapshot(ctx, s.snapshotScope(ctx, p.SessionID), background)
		// The memory extension's standing entries this conversation may use, a section of its own (memory_extension_source.go).
		snapshot = s.withStandingMemory(ctx, p.MachineID, p.SessionID, snapshot)
		// Main chat: deterministically attach <=1536B of non-sensitive local memory for high-discriminability entities mentioned in this turn.
		if section := s.mainEntityMemorySection(ctx, p.MachineID, p.SessionID, p.Query); section != "" {
			snapshot = appendSnapshotSection(snapshot, section)
		}
		// Leader sessions: attach bound-project extension memory and active scoped user decisions on start/resume/compaction.
		snapshot = s.withLeaderMemory(ctx, p.MachineID, p.SessionID, snapshot)
	}
	c := prompts.AssistantContext{ScheduledWorkPlan: cleanRound && s.settings(ctx).workGuideOn(), Snapshot: snapshot, Owner: s.owner(ctx),
		Main: s.isMain(ctx, p.SessionID), Leader: s.isLeader(ctx, p.SessionID), Background: background}
	switch {
	case c.Main:
		cfg := s.settings(ctx)
		c.BackgroundStarted = s.announceBackground(ctx) // until a message carrying it is delivered: its tasks are followed up in the background now (supervise_background.go)
		names := s.leaderNames(ctx, cfg.Leaders)
		for _, id := range cfg.Leaders {
			if id != "" && id != cfg.Main {
				c.Directions = append(c.Directions, prompts.ContextDirection{ID: id, Name: names[id]})
			}
		}
	case c.Leader:
		c.LeaderName = s.leaderNames(ctx, []string{p.SessionID})[p.SessionID]
		c.LeaderStatus = s.directionStatus(ctx, p.SessionID)
	}
	return s.contextResult(ctx, p, prompts.RenderAssistantContext(c), c.BackgroundStarted, cleanRound)
}

// contextResult is Context's answer with text as its <persona-context> text: with the view errors to report, the
// background supervisor's announcement (backgroundStarted, "" for none) to confirm, the compaction instructions and,
// unless this is a clean round, the compaction recovery.
func (s *Service) contextResult(ctx context.Context, p ContextParams, text, backgroundStarted string, cleanRound bool) ContextResult {
	errs, receipts := s.viewErrors(ctx, p)
	if backgroundStarted != "" {
		receipts = append(receipts, assistantwire.ViewErrorReceipt{ItemID: backgroundAnnounceReceipt, Digest: backgroundStarted})
	}
	out := ContextResult{Text: text, ViewErrors: errs, Receipts: receipts, CompactInstructions: prompts.RenderCompactInstructions(answerLanguage(s.hello(ctx).Locale))}
	if !cleanRound {
		if p.PrepareRecovery && p.RecoveryID == "" {
			s.prepareCompactRecovery(ctx, p)
		}
		recovery := s.compactRecoveryContext(ctx, p)
		out.RecoveryID, out.Recovery = recovery.RecoveryID, recovery.Recovery
	}
	return out
}

// contextState is the last snapshot taken for the live context.
type contextState struct {
	mu       sync.Mutex
	snap     *prompts.Snapshot
	at       time.Time
	took     time.Duration // how long the read snap came from took
	snapping chan struct{} // closed when the snapshot being taken is in
}

// contextSnapshot is the live snapshot each message to the assistant starts with: the time, which machines are up,
// agents waiting for the user, unread Feishu chats and the rest of today's meetings. It is taken on every message, so
// it must not hold the message up: the sources are read at most every snapshotFresh, a message waits for a new read
// at most snapshotWait and otherwise goes with the last one (the time is always now). While reads take longer than
// snapshotWait (the last one did), a message does not wait at all: the read it starts would come too late for it
// anyway, and is there for the next message (#8948). Each source has a short deadline; a slow one is left out. The
// sources are read once for every conversation: scope ("" for all of them, snapshotScope) picks the tasks this one's
// lists show, and background leaves out what only the main conversation acts on (backgroundSnapshot).
func (s *Service) contextSnapshot(ctx context.Context, scope string, background bool) string {
	cs := &s.live
	cs.mu.Lock()
	snap, at, took := cs.snap, cs.at, cs.took
	var wait chan struct{}
	if snap == nil || time.Since(at) >= snapshotFresh {
		if wait = cs.snapping; wait == nil {
			wait = make(chan struct{})
			cs.snapping = wait
			go s.takeSnapshot(wait)
		}
	}
	cs.mu.Unlock()
	if wait != nil {
		limit := snapshotWait
		switch {
		case snap == nil || time.Since(at) > snapshotStale:
			limit = snapshotSlowest // nothing usable yet: wait for the sources' own deadlines
		case took > snapshotWait:
			limit = 0 // the last read came too late for a message: this one goes with the snapshot it read
		}
		if limit > 0 {
			select {
			case <-wait:
			case <-time.After(limit):
			case <-ctx.Done():
			}
		}
		cs.mu.Lock()
		snap = cs.snap
		cs.mu.Unlock()
	}
	if snap == nil {
		return ""
	}
	out := *snap
	out.Now = time.Now().In(s.location(ctx))
	out.ModelProfile = s.modelProfileContext(ctx)
	out = scopeSnapshot(out, scope)
	if background {
		out = backgroundSnapshot(out)
	}
	return fitSnapshot(out)
}

// backgroundSnapshot is snap for the background supervisor (#7315): without what only the main conversation acts on,
// its plan cards waiting for Start (only it withdraws them, toolTaskPlanWithdraw), the owner's unread chats and the
// iPhone line. Their tools are not among a round's (supervise_round.go roundToolNames), and its supervision needs none
// of them.
func backgroundSnapshot(snap prompts.Snapshot) prompts.Snapshot {
	snap.Plans, snap.PlansTotal, snap.Chats, snap.NoPhone = nil, 0, nil, true
	return snap
}

// snapshotScope is whose tasks conversation id's snapshot lists (#8666): "" for every task in the main conversation,
// the background supervisor that follows its tasks up (supervise_background.go), a scheduled task's own conversation
// (a briefing goes over all of them) and while there is no main conversation; else id, for only the tasks it handed
// out or supervises, as agent_control.go othersTask counts them. A leader is such a conversation. The design gives the
// whole list to the main conversation (docs/assistant/todos.md); another conversation that got it took the user's
// question for one of the others' tasks, which crowded its own out of the first rows.
func (s *Service) snapshotScope(ctx context.Context, id string) string {
	if id == "" || s.settings(ctx).Main == "" || s.isMain(ctx, id) || s.isBackgroundSession(ctx, id) || s.runsScheduledTask(ctx, id) {
		return ""
	}
	return id
}

// runsScheduledTask: session is a scheduled task's own conversation (Task.RunSessionID).
func (s *Service) runsScheduledTask(ctx context.Context, session string) bool {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.RunSessionID == session {
			return true
		}
	}
	return false
}

// scopeSnapshot is snap for a conversation (scope "" for all, snapshotScope): its task lists cut to what one message
// shows, after leaving out, for a scoped one, the tasks that are neither handed out nor supervised by it, so another
// conversation's tasks never crowd its own out. The lists are new: snap shares its own with the cached snapshot.
func scopeSnapshot(snap prompts.Snapshot, scope string) prompts.Snapshot {
	// A personal to-do has neither an origin nor a supervisor, so only an unscoped snapshot lists it.
	mine := func(o prompts.SnapshotOwner) bool { return scope == "" || o.Origin == scope || o.Supervisor == scope }
	snap.Scoped = scope != ""
	snap.Tasks, _ = firstOf(snap.Tasks, snapshotTaskLimit, func(t prompts.SnapshotTask) bool { return mine(t.Owner) })
	if snap.Scoped {
		for i := range snap.Tasks {
			if snap.Tasks[i].Owner.Supervisor == scope || snap.Tasks[i].Owner.Origin == scope {
				snap.Tasks[i].OriginID, snap.Tasks[i].OriginTitle, snap.Tasks[i].OriginLink = "", "", ""
				snap.Tasks[i].SupervisedElsewhere = false
			}
		}
	}
	snap.Approvals, _ = firstOf(snap.Approvals, snapshotApprovalLimit, func(a prompts.SnapshotApproval) bool { return mine(a.Owner) })
	var accepts int
	snap.Unaccepted, accepts = firstOf(snap.Unaccepted, snapshotAcceptLimit, func(a prompts.SnapshotAccept) bool { return mine(a.Owner) })
	if snap.Scoped {
		snap.UnacceptedTotal = accepts
	}
	shown := map[string]bool{} // a scoped index lists only the open ones its details do not
	for _, t := range snap.Tasks {
		shown[t.ID] = snap.Scoped
	}
	snap.OpenTodos, _ = firstOf(snap.OpenTodos, snapshotOpenTodoLimit, func(t prompts.SnapshotOpenTodo) bool { return mine(t.Owner) && !shown[t.ID] })
	return snap
}

// snapshotApprovalLimit, snapshotAcceptLimit: how many tasks waiting for the user's nod, and for the user to verify,
// one conversation's snapshot lists (the oldest; the count says how many more wait to be verified).
const (
	snapshotApprovalLimit = 10
	snapshotAcceptLimit   = 8
)

// firstOf is a new list of the first limit items of list that keep takes, and how many it takes in all.
func firstOf[T any](list []T, limit int, keep func(T) bool) ([]T, int) {
	var out []T
	n := 0
	for _, x := range list {
		if keep(x) {
			if n++; len(out) < limit {
				out = append(out, x)
			}
		}
	}
	return out, n
}

const (
	snapshotFresh   = 45 * time.Second
	snapshotWait    = 1500 * time.Millisecond
	snapshotStale   = 30 * time.Minute
	snapshotSlowest = 5 * time.Second

	// snapshotBudget: characters of rendered snapshot one message may carry (#3203). The block stays in the
	// conversation, so its size is what every message adds to the context until the next compaction; the node sends
	// only the sections that changed, but after a compaction or a restart it sends all of them again.
	snapshotBudget = 6000
)

// snapshotOmits are the sections fitSnapshot leaves out, the least needed first. Never left out: the agents waiting
// for the user (needs you, awaiting a reply), the plan cards waiting for "Start" and the failed scheduled runs (no
// tool lists them), the calendar and the chats that @ the user.
var snapshotOmits = []func(*prompts.SnapshotOmit){
	func(o *prompts.SnapshotOmit) { o.Waiting = true },
	func(o *prompts.SnapshotOmit) { o.Routines = true },
	func(o *prompts.SnapshotOmit) { o.Following = true },
	func(o *prompts.SnapshotOmit) { o.Followups = true },
	func(o *prompts.SnapshotOmit) { o.OtherChats = true },
	func(o *prompts.SnapshotOmit) { o.KB = true },
	func(o *prompts.SnapshotOmit) { o.Asked = true },
	func(o *prompts.SnapshotOmit) { o.Unaccepted = true },
	func(o *prompts.SnapshotOmit) { o.OpenTodos = true },
	func(o *prompts.SnapshotOmit) { o.Tasks = true },
	func(o *prompts.SnapshotOmit) { o.Unread = true },
	func(o *prompts.SnapshotOmit) { o.Approvals = true },
}

// fitSnapshot is s rendered within snapshotBudget: as it is when it fits, else with sections left out in
// snapshotOmits' order until it does (each keeps its heading and count: prompts.SnapshotOmit), else cut short. It
// changes nothing but s.Omit: s shares its lists with the cached snapshot other messages render at the same time,
// and the free text in them was clipped where the snapshot was taken.
func fitSnapshot(s prompts.Snapshot) string {
	text := prompts.RenderSnapshot(s)
	for _, omit := range snapshotOmits {
		if utf8.RuneCountInString(text) <= snapshotBudget {
			return text
		}
		omit(&s.Omit)
		text = prompts.RenderSnapshot(s)
	}
	if utf8.RuneCountInString(text) <= snapshotBudget {
		return text
	}
	return cutSnapshot(text)
}

// cutSnapshot is text cut short, with the line saying so, within snapshotBudget: for what no section can be left out
// of. The agents waiting for the user come first in the template, so they are what stays; the cut is at a line break
// unless that would lose more than half the block (one agent list is one line).
func cutSnapshot(text string) string {
	tail := "\n" + prompts.SnapshotCut()
	head := textfmt.Cut(text, snapshotBudget-utf8.RuneCountInString(tail))
	if i := strings.LastIndexByte(head, '\n'); i > len(head)/2 {
		head = head[:i]
	}
	return head + tail
}

// takeSnapshot reads the sources and keeps the result for contextSnapshot; done is closed when it is in.
func (s *Service) takeSnapshot(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotSlowest)
	defer cancel()
	start := time.Now()
	loc := s.location(ctx)
	now := time.Now().In(loc)
	devices, phone, apps := s.devicesAndPhone(ctx)
	snap := prompts.Snapshot{Now: now, Zone: loc.String(), PhoneOnline: phone, Apps: snapshotApps(apps)}
	var wg sync.WaitGroup
	var connectors time.Duration
	wg.Add(1)
	go func() { // one ask for both sections: each connector builds its chats and meetings in one answer
		defer wg.Done()
		t := time.Now()
		live := s.connectorSnapshots(ctx)
		snap.Chats, snap.Calendar = snapshotChats(live), snapshotCalendar(live, now)
		connectors = time.Since(t)
	}()
	for _, m := range devices {
		snap.Machines = append(snap.Machines, prompts.SnapshotMachine{Name: m.Name, ID: m.ID, Online: m.Online})
	}
	web := s.webBase(ctx) // the agents' and tasks' links (#2194)
	snap.Needs, snap.Unread, snap.Awaiting, snap.Waiting, snap.Done, snap.Muted, snap.Working = s.snapshotAgents(ctx, devices, web)
	snap.Tasks = s.snapshotTasks(ctx, web)
	snap.OpenTodos, snap.OpenTodosTotal = s.snapshotOpenTodos(ctx, web)
	snap.OpenTodosLoaded = snap.OpenTodos != nil
	snap.Approvals = s.snapshotApprovals(ctx, web)
	snap.Unaccepted, snap.UnacceptedTotal = s.snapshotUnaccepted(ctx, loc)
	snap.Plans, snap.PlansTotal = s.snapshotPlans(ctx, loc, now)
	snap.Asked = s.snapshotAsked(ctx)
	if e, err := s.kbGet(ctx); err == nil {
		snap.KB = e.Prompt(6, 8)
	}
	snap.FollowingEnabled = s.inboxConfig(ctx).Agent.Enabled && s.inboxReady(ctx) == nil
	if snap.FollowingEnabled {
		snap.Following, snap.FollowingTotal = s.snapshotFollowing(ctx, loc)
	}
	snap.Followups, snap.FollowupsTotal, snap.Routines, snap.RoutinesTotal, snap.Missed, snap.MissedTotal = s.snapshotSchedule(ctx, loc, now)
	wg.Wait()
	took := time.Since(start)
	if took > snapshotWait {
		// Messages go with the snapshot before this one meanwhile (contextSnapshot); the connectors' share says
		// whether a connector's live state is what is slow.
		s.logf("assistant: live snapshot: the read took %s (connectors %s), longer than a message waits (%s): messages go with the last one",
			took.Round(time.Millisecond), connectors.Round(time.Millisecond), snapshotWait)
	}

	cs := &s.live
	cs.mu.Lock()
	cs.snap, cs.at, cs.took, cs.snapping = &snap, time.Now(), took, nil
	cs.mu.Unlock()
	close(done)
}

// snapshotStale makes the next message take a new snapshot (what it holds just changed).
func (s *Service) snapshotStale() {
	cs := &s.live
	cs.mu.Lock()
	cs.at = time.Time{}
	cs.mu.Unlock()
}

// snapshotOld makes the next message take a new snapshot, waiting for it no longer than for any snapshot past
// snapshotFresh: what changed (a scheduled run's outcome) is news, but the snapshot held is not wrong to read meanwhile.
func (s *Service) snapshotOld() {
	cs := &s.live
	cs.mu.Lock()
	if old := time.Now().Add(-snapshotFresh); cs.at.After(old) {
		cs.at = old
	}
	cs.mu.Unlock()
}

// ProfileChanged makes the next message take a new snapshot (the time zone changed), and the settings be read again
// (the hub's defaults for them changed, or someone changed them behind the assistant's back).
func (s *Service) ProfileChanged(context.Context) {
	s.mcpChanges.notify() // language/settings changes invalidate the model's tool descriptions too
	cs := &s.live
	cs.mu.Lock()
	cs.snap, cs.at = nil, time.Time{}
	cs.mu.Unlock()
	s.prefs.mu.Lock()
	s.prefs.at = time.Time{}
	s.prefs.mu.Unlock()
	s.invalidateLauncherListCache("")
	// A manager.json pin may have changed the supervision interval even though the saved settings did not.
	s.rescheduleSupervision()
}

type snapshotAgentGroupKind string

const (
	snapshotAgentOutside  snapshotAgentGroupKind = ""
	snapshotAgentNeedsYou snapshotAgentGroupKind = "needs_you"
	snapshotAgentWorking  snapshotAgentGroupKind = "working"
	snapshotAgentAwaiting snapshotAgentGroupKind = "awaiting"
	snapshotAgentWaiting  snapshotAgentGroupKind = "waiting"
	snapshotAgentDone     snapshotAgentGroupKind = "done"
	snapshotAgentUnread   snapshotAgentGroupKind = "unread"
)

// snapshotAgentGroup is the live snapshot category a session belongs in before muted sessions are separated. The
// empty value means it is not in the snapshot; done and the time-limited categories use UpdatedAt, as the snapshot
// always has (not Work.TurnAt).
func snapshotAgentGroup(c proto.Session, now time.Time) snapshotAgentGroupKind {
	recent := now.Sub(c.UpdatedAt) < 24*time.Hour
	w := c.Work
	switch {
	case c.State == proto.StateNeedsYou, silentRow(c), upstreamRow(c):
		return snapshotAgentNeedsYou // a turn with no output or a persistent upstream stall needs visibility (#10187, #13372)
	case unverifiedAgyTurn(c):
		return snapshotAgentOutside
	case c.State == proto.StateWorking:
		return snapshotAgentWorking
	case c.AwaitsReply && c.State == proto.StateIdle && recent:
		return snapshotAgentAwaiting // seen, its question unanswered (#5096)
	// A decision with no card up (cards off, muted, "Not now") is not "Needs you": the lists, the badges and the Live
	// Activity go by the state (docs/protocol/agent-states.md), so it stays "Unread" with what it waits for, or is seen.
	case w != nil && w.Phase == proto.WorkBlocked && w.On != proto.WaitUser && c.State != proto.StateUnread && recent:
		return snapshotAgentWaiting // the hub waits it out (an unread one is left for the user)
	case w != nil && w.Phase == proto.WorkDone && recent:
		return snapshotAgentDone
	case c.State == proto.StateUnread && recent:
		return snapshotAgentUnread
	default:
		return snapshotAgentOutside
	}
}

// snapshotAgents sorts the agents by what they need: the work state (agents_work.go, docs/assistant/agent-work.md) says what a stopped one
// waits for or concluded. Each has its page in the web app at web ("" for none). The muted ones (the agent or its
// space) are in none of the others: the Live Activity, the Agents badge and the inbox leave them out
// (docs/apple/live-activity.md), so "Needs you" here does too; they are told apart, for when the user asks about them (#4562).
func (s *Service) snapshotAgents(ctx context.Context, devices []Device, web string) (needs, unread, awaiting, waiting, done, muted []prompts.SnapshotAgent,
	working []prompts.SnapshotAgent) {
	now := time.Now()
	lang := s.lang(ctx)
	for _, m := range devices {
		list, off, _ := s.sessionsMuted(ctx, m.ID)
		for _, c := range list {
			if c.Assistant || c.HiddenByDirectory() || c.FoldedUnder() != "" || c.IsSubagent || c.IsCodexSubagent() {
				continue
			}
			c.State = c.FoldedState()
			a := prompts.SnapshotAgent{Label: agentLabel(c), Machine: m.ID, Session: short(c.SessionID),
				Link: sessionLink(web, m.ID, c.SessionID, false), Ultracode: ultracodeState(c.Ultracode), Plan: snapshotPlan(c)}
			w := c.Work
			if w != nil && w.Phase != proto.WorkWorking {
				a.Line, a.Closeout = w.Line, snapshotCloseout(w)
			}
			if a.Line == "" && c.AwaitsReply {
				a.Line = c.Asks // what it asks (#5096)
			}
			if silentRow(c) {
				a.Line = c.TurnError // why it is the owner's: no output, or Persona's Stop (#10187)
			} else if upstreamRow(c) {
				a.Line = s.upstreamSnapshotLine(lang, m.ID, c) // persistent upstream 5xx / pool outage (#13372)
			}
			a.Line = clip(oneLine(a.Line), 160)
			group := snapshotAgentGroup(c, now)
			if group == snapshotAgentOutside {
				continue
			}
			if off[c.SessionID] {
				muted = append(muted, a)
				continue
			}
			switch group {
			case snapshotAgentNeedsYou:
				needs = append(needs, a)
			case snapshotAgentWorking:
				working = append(working, a)
			case snapshotAgentAwaiting:
				awaiting = append(awaiting, a)
			case snapshotAgentWaiting:
				waiting = append(waiting, a)
			case snapshotAgentDone:
				done = append(done, a)
			case snapshotAgentUnread:
				unread = append(unread, a)
			}
		}
	}
	return needs, unread, awaiting, waiting, done, muted, working
}

// runningWorkflows are c's background workflow runs still running, as the snapshot tells them (#2935).
func runningWorkflows(c proto.Session, now time.Time) []prompts.SnapshotWorkflow {
	var out []prompts.SnapshotWorkflow
	for _, w := range c.Workflows {
		if !w.Running {
			continue
		}
		sw := prompts.SnapshotWorkflow{Name: cmp.Or(w.Name, w.Summary, w.TaskID), Phase: w.Phase, PhaseIndex: w.PhaseIndex,
			Phases: len(w.Phases), Done: w.AgentsDone, Started: w.AgentsStarted}
		if d := now.Sub(w.StartedAt); !w.StartedAt.IsZero() && d >= time.Minute {
			sw.For = humanDuration(d)
		}
		out = append(out, sw)
	}
	return out
}

// connectorSnapshots are the live states of the hub's signed-in connectors that keep one (connector.Snapshot: a chat
// service through a plugin; app.call connectors.snapshots), waited for 3 seconds; those that do not answer in time are
// left out.
func (s *Service) connectorSnapshots(ctx context.Context) []connector.SnapshotData {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var res struct {
		Snapshots []connector.SnapshotData `json:"snapshots"`
	}
	if err := s.appCall(ctx, AppConnectors, ConnectorsSnapshots, map[string]any{"tz": s.location(ctx).String()}, &res); err != nil {
		return nil
	}
	return res.Snapshots
}

// snapshotChats is the unread chats in the connectors' live states, nil when none answered.
func snapshotChats(live []connector.SnapshotData) *prompts.SnapshotChats {
	var out *prompts.SnapshotChats
	for _, d := range live {
		if d.Chats == nil {
			continue
		}
		if out == nil {
			out = &prompts.SnapshotChats{}
		}
		for _, c := range d.Chats.AtMe {
			out.AtMe = append(out.AtMe, prompts.SnapshotChat{Name: c.Name, Unread: c.Unread, Preview: clip(oneLine(c.Preview), 40)})
		}
		for _, c := range d.Chats.Other {
			out.Other = append(out.Other, prompts.SnapshotChat{Name: c.Name, Unread: c.Unread})
		}
	}
	return out
}

// snapshotCalendar is the rest of today's meetings in the connectors' live states, nil when none answered.
func snapshotCalendar(live []connector.SnapshotData, now time.Time) *prompts.SnapshotCalendar {
	var out *prompts.SnapshotCalendar
	for _, d := range live {
		if d.Meetings == nil {
			continue
		}
		if out == nil {
			out = &prompts.SnapshotCalendar{}
		}
		for _, m := range d.Meetings {
			if m.End.After(now) {
				out.Events = append(out.Events, prompts.SnapshotEvent{Start: m.Start.In(now.Location()), End: m.End.In(now.Location()),
					Title: clip(oneLine(m.Title), 40)})
			}
		}
	}
	if out != nil {
		sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].Start.Before(out.Events[j].Start) })
	}
	return out
}

// followingInboxItems returns listed copies of the open matters waiting on the other side or back with the user for a
// follow-up. The normal readiness path loads persisted items first, so a cold start is not mistaken for an empty inbox;
// an unavailable store leaves the section out instead. It deliberately does not use inboxList or inboxListAt: those
// stamp arrival times on the items they read. The lock covers both the copy and the state computation; the selection
// itself writes no item, database or chain.
func (s *Service) followingInboxItems(ctx context.Context) []InboxItem {
	if s.inboxReady(ctx) != nil {
		return nil
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	var out []InboxItem
	for _, it := range s.inbox.items {
		if !it.open() {
			continue
		}
		listed := it.listed()
		switch listed.Now {
		case NowWaiting, NowFollowUpAsk, NowCheckDue:
			out = append(out, listed)
		}
	}
	return out
}

// snapshotFollowing is the main conversation's short view of the matters being followed, earliest check first and
// those without a check time last. The total counts every selected matter, not only the five lines the snapshot shows.
func (s *Service) snapshotFollowing(ctx context.Context, loc *time.Location) ([]prompts.SnapshotFollowing, int) {
	items := s.followingInboxItems(ctx)
	checkAt := func(it InboxItem) *time.Time {
		if it.Follow == nil {
			return nil
		}
		return it.Follow.CheckAt
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := checkAt(items[i]), checkAt(items[j])
		switch {
		case a == nil && b != nil:
			return false
		case a != nil && b == nil:
			return true
		case a != nil && b != nil && !a.Equal(*b):
			return a.Before(*b)
		default:
			return items[i].ID < items[j].ID
		}
	})
	total := len(items)
	if len(items) > 5 {
		items = items[:5]
	}
	out := make([]prompts.SnapshotFollowing, 0, len(items))
	for _, it := range items {
		row := prompts.SnapshotFollowing{
			Title: clip(oneLine(it.Title), 60), Where: clip(oneLine(it.Where), 40),
			FollowUpAsk: it.Now == NowFollowUpAsk, CheckDue: it.Now == NowCheckDue,
		}
		if f := it.Follow; f != nil {
			row.Who, row.Next = clip(oneLine(f.Who), 40), clip(oneLine(f.Next), 80)
			if f.CheckAt != nil {
				row.CheckAt = f.CheckAt.In(loc)
			}
		}
		out = append(out, row)
	}
	return out, total
}

// snapshotSchedule is the schedule as the snapshot says it: the follow-ups to come, the daily tasks with how their
// last run went, and the scheduled runs that failed or were skipped since yesterday 18:00 (#4124: the "Morning briefing" tells them
// without calling list_followups). Bounded: the next eight follow-ups, eight daily tasks and the six newest misses, with the totals. The words
// for a status are the template's (prompts/assistant/snapshot.md).
func (s *Service) snapshotSchedule(ctx context.Context, loc *time.Location, now time.Time) (follow []prompts.SnapshotFollowup,
	followTotal int, routines []prompts.SnapshotRoutine, routinesTotal int, misses []prompts.SnapshotMiss, missed int) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return nil, 0, nil, 0, nil, 0
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if !t.Enabled {
			continue
		}
		if t.calendar() {
			routinesTotal++
			if len(routines) < 8 {
				r := prompts.SnapshotRoutine{Label: clip(t.label(), 30), Rule: ruleText(t, loc)}
				if at := t.lastShown(); !at.IsZero() { // the outcome's own run, not one still starting (#10182)
					r.LastAt, r.LastStatus = at.In(loc), t.LastStatus
					if t.LastStatus == runFailed || t.LastStatus == runSkipped {
						r.LastWhy = clip(oneLine(t.LastError), 60)
					}
				}
				routines = append(routines, r)
			}
			continue
		}
		f := prompts.SnapshotFollowup{At: t.At.In(loc), Label: clip(oneLine(t.label()), 30)}
		if t.Repeat == repeatInterval {
			f.Every = everyText(t.EveryMinutes)
		}
		follow = append(follow, f)
	}
	n := now.In(loc)
	since := time.Date(n.Year(), n.Month(), n.Day()-1, 18, 0, 0, 0, loc) // as the "Today" card's "overnight" (today.go)
	// Runs are kept in the order they ended, At is when they started: a long run recorded late sits after a later
	// one, so the whole history (at most maxRuns) is looked through, not cut at the first run before since.
	var all []prompts.SnapshotMiss
	for _, r := range sc.missedRuns(since) {
		all = append(all, prompts.SnapshotMiss{At: r.At.In(loc), Label: clip(r.Title, 30), Why: clip(oneLine(r.Error), 60),
			Skipped: r.Status == runSkipped})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.After(all[j].At) }) // newest first
	missed = len(all)
	if len(all) > 6 {
		all = all[:6]
	}
	sort.SliceStable(follow, func(i, j int) bool { return follow[i].At.Before(follow[j].At) })
	followTotal = len(follow)
	if len(follow) > 8 {
		follow = follow[:8]
	}
	return follow, followTotal, routines, routinesTotal, all, missed
}

// snapshotPlans are the plan cards in the main conversation still waiting for the user's "Start" (#4453), newest first:
// at most five (total: all of them), of the last seven days. A plan has no todo until it starts, so nothing else in
// the snapshot shows it, and the "Morning briefing" could not bring it up.
func (s *Service) snapshotPlans(ctx context.Context, loc *time.Location, now time.Time) ([]prompts.SnapshotPlan, int) {
	main := s.settings(ctx).Main
	if main == "" {
		return nil, 0
	}
	items, plans, err := s.feed(ctx, main, now.Add(-7*24*time.Hour))
	if err != nil {
		return nil, 0
	}
	var out []prompts.SnapshotPlan
	seen := map[string]bool{}
	for i := len(items) - 1; i >= 0; i-- { // newest first
		it := items[i]
		p := plans[it.Ref]
		if it.Kind != feedPlan || seen[it.Ref] || p == nil || p.State != planOpen {
			continue
		}
		seen[it.Ref] = true
		sp := prompts.SnapshotPlan{ID: p.ID, At: p.CreatedAt.In(loc), Tasks: len(p.Tasks)}
		for _, t := range p.Tasks {
			if len(sp.Titles) == 3 {
				break
			}
			sp.Titles = append(sp.Titles, clip(oneLine(t.Title), 30))
		}
		out = append(out, sp)
	}
	total := len(out)
	if len(out) > 5 {
		out = out[:5]
	}
	return out, total
}

// ultracodeState preserves unknown separately from a switch explicitly read back as off.
func ultracodeState(v *bool) string {
	if v == nil {
		return ""
	}
	if *v {
		return "on"
	}
	return "off"
}
