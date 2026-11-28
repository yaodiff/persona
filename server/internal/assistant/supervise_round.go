package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Each round of the background supervisor starts clean (#7315, docs/assistant/unified-entry.md "Each round starts
// clean"). It was one conversation that every wake went on with: every round reread every earlier one, and the
// tools of every kind of work, though a round needs its own signals and what the tasks' ledgers keep. It
// is still one conversation, because the clients' row, its rights over the tasks (backgroundSupervises), the
// announcement's receipt and the persisted relay queue all key on its id; but each round of it resumes at the end of
// its first turn (the node restarts its idle process with --resume-session-at, node/assistant_round.go), so a round
// reads the start brief, the whole snapshot and its own supervision message; the handling rules are the static
// supervision instructions its process starts with (supervision_instructions.go), as every supervisor's. That message
// is rendered Fresh: a line saying what crosses rounds is on the tasks, the round marker and each entry's last review
// (prompts.Supervise.Fresh). The round's process lists only the tools supervision uses (roundToolNames,
// through mcp.tools' tool_set).
//
// assistant.background_supervisor_keeps_context (off by default) brings back the one long conversation from its next
// round: no round is asked for, the message is rendered as before (not Fresh), and a message that asks for no round needs every tool,
// so the node replaces a round's process, once it is idle, by a plain resume with every tool (node/assistant_round.go).
//
// The node says how each round went (superviseRoundAnswered). A legacy round it could not start from the first turn
// because that turn is no longer on the conversation's chain (no_anchor), and a restarted round whose transcript is
// over backgroundTranscriptLimit, make the legacy conversation unable to continue. At the next idle digest its unread
// work moves to main (backgroundOutgrown, ensureBackground), without archiving it or starting a replacement (#5732).

// superviseRoundParam marks an assistant.send to the background supervisor that is a round (node/assistant_round.go).
const superviseRoundParam = "supervise_round"

// toolSetSupervise is the round's tool set as its process asks for it (mcp.tools tool_set).
const toolSetSupervise = "supervise"

type toolSetKey struct{}

// withToolSet is ctx carrying the tool set an mcp.tools request asks for (its tool_set), for sessionMCPTools: ctx as
// it is when the request names none.
func withToolSet(ctx context.Context, params json.RawMessage) context.Context {
	var p struct {
		ToolSet string `json:"tool_set"`
	}
	if json.Unmarshal(params, &p) != nil || p.ToolSet == "" {
		return ctx
	}
	return context.WithValue(ctx, toolSetKey{}, p.ToolSet)
}

// toolSetOf is the tool set withToolSet put on ctx, "" for none.
func toolSetOf(ctx context.Context) string {
	set, _ := ctx.Value(toolSetKey{}).(string)
	return set
}

// roundToolNames are the tools a round lists, and why each is in: the ones its supervision work uses (follow-ups,
// computers and agents, an agent's cards, the knowledge base, the tasks' plans, reviews and scope, a conversation's
// words), and the ones its prompts point it to: ask_escalate for a card (the supervision instructions), task_decided for
// a decision_reply entry (its message asks it to confirm the owner's answer with it, and the tool lets the background
// supervisor record it), todo_get where its message says what crosses rounds is kept, and todo_list, agents_list,
// list_followups and kb_get where the snapshot leaves items out. lark_search, or larkcli_search from the lark-cli
// connector, is listed when the install gives it.
// roundTools keeps them in the order of the lists they come from, so the cached prefix stays the same from round to
// round. Every tool a round's own message names is here, and the descriptions of these tools and the background
// supervisor's context name the others only in passing (TestRoundListsTheToolsItsMessageNames, roundPassingMentions
// there); every other tool the supervision instructions name is in roundToolsWithheld with why, and the instructions'
// background line sends that work elsewhere too (TestRoundToolsAgreeWithSupervisionInstructions). Left out as well, and
// why: the phone's, mail, inbox, view, place and routine tools (the owner's own errands; an inbox signal leaves replies
// to the Inbox's own draft); the model, settings, secret, skill and concern tools (the owner's changes, made in the
// main conversation); the repository and other to-do writing tools (the background supervisor is not a direction and
// records no work of its own); and the other agent, conversation, leader and direction tools (a supervised replacement
// starts with task_plan on the same todo, and what needs another conversation is the main conversation's).
var roundToolNames = []string{"notify_user", "supervision_finish", "schedule_followup", "list_followups", "cancel_followup",
	"machines_list", "agents_list", "agent_read", "agent_send", "agent_interrupt", "agent_archive", "interaction_answer",
	"ask_escalate", "kb_search", "kb_get", "kb_update", "conversation_read", "task_plan", "task_review", "task_scope",
	"task_decided", "todo_get", "todo_list", "lark_search", "larkcli_search"}

// roundToolsWithheld are the tools the supervision instructions name that a round does not list, each with why the
// background supervisor has no use for it in a round. The instructions every supervisor reads also tell it so (their
// background line), so the supervision instructions never send a round to a tool it lacks.
var roundToolsWithheld = map[string]string{
	"ask_user":          "an owner decision from the background supervisor is a task_review needs_user conclusion card, never ask_user (the instructions' background line)",
	"conversation_send": "leader_stuck goes to the main conversation (digestTo)",
	"task_start_screen": "only the main conversation or the task's current supervising conversation can inspect a failed start (startRemedyAllowed)",
	"task_start_keys":   "only the main conversation or the task's current supervising conversation can answer a failed start (startRemedyAllowed)",
	"task_retry":        "only the main conversation or the task's current supervising conversation can retry a failed start (startRemedyAllowed)",
	"agent_supervise":   "only the main conversation and a direction's lead adopt an agent opened outside supervision (toolAgentSupervise)",
	"todo_create":       "a direction's own work: the background supervisor is not a direction and records no to-dos",
	"todo_complete":     "a direction's own work: the background supervisor is not a direction and records no to-dos",
	"leader_progress":   "a direction's own work: only a leader conversation reports progress (toolLeaderProgress)",
}

// roundTools are the tools of list a round lists, in list's order.
func roundTools(list []proto.Tool) []proto.Tool {
	out := make([]proto.Tool, 0, len(roundToolNames))
	for _, t := range list {
		if slices.Contains(roundToolNames, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// backgroundTranscriptLimit is how large a legacy background supervisor's transcript can be before its unread work
// moves to main rather than starting another clean round. The file only grows (a compaction appends to it too), and
// every round restarts the process, so Claude Code reads and parses all of it at each round's start, as the node does
// to find the anchor and the app's history after a node restart, though the round uses only the first turn. Measured
// with Claude Code 2.1.322 and a local mock of the model API on synthetic lines of about 4 KiB
// (node/assistant_round_live_test.go): a round asks the model 0.24 s after its process starts with a small file, 0.47 s
// at 32 MiB and about 0.8 s at 128 MiB, and the node's anchor scan takes 0.09 s and 0.35 s. Real transcripts that large
// were not measured and may parse slower. Only a round that restarted the process counts (superviseRoundAnswered).
const backgroundTranscriptLimit int64 = 32 << 20

// backgroundKeepsContext is assistant.background_supervisor_keeps_context in effect with settings cfg: the background
// supervisor is one long conversation, as before #7315 (off by default).
func backgroundKeepsContext(cfg Settings) bool {
	return cfg.BackgroundSupervisorKeepsContext != nil && *cfg.BackgroundSupervisorKeepsContext
}

// superviseRoundReply is the node's answer to a round's assistant.send (node/assistant_round.go): Context "fresh" (it
// started from the first turn) or "kept" (it went in as before) with Why, and the transcript's size. A node from before
// #7315 answers none of them, and so does any node to the same message asked again under its once_id.
type superviseRoundReply struct {
	SessionID       string `json:"session_id"`
	Context         string `json:"context"`
	Why             string `json:"why"`
	TranscriptBytes int64  `json:"transcript_bytes"`
}

// backgroundRoundForksKey persists manager-attested background supervision round forks across restarts (#12650).
const backgroundRoundForksKey = "assistant.background_round_forks"

// backgroundRoundFork records a manager-attested forked session id created by a background supervisor round (#12650).
type backgroundRoundFork struct {
	Base    string    `json:"base"`
	Machine string    `json:"machine,omitempty"`
	At      time.Time `json:"at"`
}

// maxBackgroundRoundForks caps the persisted round fork table so long-running hubs stay bounded.
const maxBackgroundRoundForks = 512

// superviseRounds is what the rounds' answers say about background supervisors, in memory: the ones to replace and why,
// the ones that are a replacement and have not started a round from their first turn yet, when a replacement was last
// tried, whether an old node was logged, any manager-attested forked round session ids, and the circuit breaker state.
type superviseRounds struct {
	mu          sync.Mutex
	outgrown    map[string]string // background supervisor → why it is replaced at the next chance (no_anchor, oversized)
	replacement map[string]bool   // a background supervisor started to replace another, until a round of it starts fresh
	replacedAt  time.Time
	oldNode     bool
	forks       map[string]backgroundRoundFork // manager-attested forked round session id → its base background supervisor
	breaker     superviseBreaker               // circuit breaker / backoff for auth-denied or repeated no-op all_stopped rounds
	forksLoaded bool
}

// backgroundForksLocked loads manager-attested round forks from the store once; r.mu is held.
func (s *Service) backgroundForksLocked(ctx context.Context) bool {
	r := &s.background.rounds
	if r.forksLoaded {
		return true
	}
	st := s.store()
	if st == nil {
		r.forksLoaded = true
		return true
	}
	var m map[string]backgroundRoundFork
	if _, err := st.Setting(ctx, backgroundRoundForksKey, &m); err != nil {
		s.logf("supervise: load background round forks: %v", err)
		return false
	}
	if len(m) > 0 {
		if r.forks == nil {
			r.forks = m
		} else {
			for k, v := range m {
				if _, exists := r.forks[k]; !exists {
					r.forks[k] = v
				}
			}
		}
	}
	r.forksLoaded = true
	return true
}

func (s *Service) saveBackgroundForks(ctx context.Context, forks map[string]backgroundRoundFork) {
	st := s.store()
	if st == nil {
		return
	}
	if forks == nil {
		forks = map[string]backgroundRoundFork{}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(sctx, backgroundRoundForksKey, forks); err != nil {
		s.logf("supervise: save background round forks: %v", err)
	}
}

// registerBackgroundRoundFork records a manager-attested forkID returned for baseID (the background supervisor) on
// machine, so it inherits assistant authorization and supervision tools and stays hidden from the Agents list.
func (s *Service) registerBackgroundRoundFork(ctx context.Context, machine, forkID, baseID string) {
	if forkID == "" || baseID == "" || forkID == baseID {
		return
	}
	cfg := s.settings(ctx)
	if forkID == cfg.Main || baseID == cfg.Main {
		return
	}
	if s.devs != nil && machine != "" {
		machine = s.devs.Canonical(machine)
	}
	r := &s.background.rounds
	r.mu.Lock()
	s.backgroundForksLocked(ctx)
	if r.forks == nil {
		r.forks = map[string]backgroundRoundFork{}
	}
	if len(r.forks) >= maxBackgroundRoundForks {
		oldestID := ""
		var oldestAt time.Time
		for id, f := range r.forks {
			if oldestID == "" || f.At.Before(oldestAt) {
				oldestID, oldestAt = id, f.At
			}
		}
		if oldestID != "" {
			delete(r.forks, oldestID)
		}
	}
	r.forks[forkID] = backgroundRoundFork{Base: baseID, Machine: machine, At: time.Now().UTC()}
	snapshot := maps.Clone(r.forks)
	r.mu.Unlock()
	s.saveBackgroundForks(ctx, snapshot)
}

// cachedBackgroundSettings returns the saved BackgroundSupervisor, BackgroundSupervisorMachineID, MachineID, and Main
// from the in-memory settings cache (or store when uncached) without calling s.hub.Hello, avoiding re-entrant hub
// lock deadlocks during in-flight DeviceCall callbacks and avoiding hot-path allocations.
func (s *Service) cachedBackgroundSettings(ctx context.Context) (base, bgMachine, cfgMachine, mainID string) {
	b := &s.prefs
	b.mu.Lock()
	loaded := b.cached != nil
	if loaded {
		base = b.cached.BackgroundSupervisor
		bgMachine = b.cached.BackgroundSupervisorMachineID
		cfgMachine = b.cached.MachineID
		mainID = b.cached.Main
	}
	b.mu.Unlock()
	if loaded {
		return base, bgMachine, cfgMachine, mainID
	}
	if s.store() == nil {
		return "", "", "", ""
	}
	v, ok := s.settingsLastGood(ctx)
	if !ok {
		return "", "", "", ""
	}
	return v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.MachineID, v.Main
}

// backgroundForkBase reports whether id (optionally scoped to machine) is a manager-attested forked round branch of
// the current background supervisor, returning the base background supervisor id. Unverified ParentSessionID /
// RootSessionID linkage from node session lists is never accepted as an authorization credential.
func (s *Service) backgroundForkBase(ctx context.Context, machine, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	base, _, _, mainID := s.cachedBackgroundSettings(ctx)
	if base == "" || id == mainID || id == base {
		return "", false
	}
	r := &s.background.rounds
	r.mu.Lock()
	s.backgroundForksLocked(ctx)
	entry, found := r.forks[id]
	r.mu.Unlock()
	if found && entry.Base == base {
		if machine == "" || entry.Machine == "" || s.sameMachine(machine, entry.Machine) {
			return base, true
		}
	}
	return "", false
}

// isBackgroundSession reports whether id is the current background supervisor or a manager-attested forked round branch of it.
func (s *Service) isBackgroundSession(ctx context.Context, id string) bool {
	if s.isBackground(ctx, id) {
		return true
	}
	_, ok := s.backgroundForkBase(ctx, "", id)
	return ok
}

// isBackgroundCallerOn reports whether session on machine is the current background supervisor or a manager-attested
// forked round branch of it running on the machine authorized for the background supervisor.
func (s *Service) isBackgroundCallerOn(ctx context.Context, machine, session string) bool {
	if machine == "" || session == "" || s.isWorkMachine(machine) {
		return false
	}
	cfg := s.settings(ctx)
	base := cfg.BackgroundSupervisor
	if base == "" || session == cfg.Main {
		return false
	}
	if sess, known := s.nodeSession(machine, session); known && sess.PaneID != "" {
		return false
	}
	if session != base {
		if _, ok := s.backgroundForkBase(ctx, machine, session); !ok {
			return false
		}
	}
	bgMachine := cmp.Or(cfg.BackgroundSupervisorMachineID, cfg.MachineID)
	if s.sameMachine(machine, bgMachine) {
		return true
	}
	s.origins.mu.Lock()
	defer s.origins.mu.Unlock()
	if !s.originsLocked(ctx) {
		return false
	}
	o := s.origins.m[base]
	return o != nil && o.By == originByStart && s.sameMachine(machine, o.MachineID)
}

// supervisionHoldKey maps a forked background supervisor round session id back to its base background supervisor id
// for supervision push-hold and supervision_finish checks.
func (s *Service) supervisionHoldKey(ctx context.Context, self string) string {
	if base, ok := s.backgroundForkBase(ctx, "", self); ok {
		return base
	}
	return self
}

// backgroundMatch holds a precomputed snapshot for matching the current background supervisor and its manager-attested
// forked round branches on one machine inside hot session-list loops with zero per-row settings/lock/session allocations.
type backgroundMatch struct {
	base  string
	main  string
	forks map[string]struct{}
}

func (s *Service) backgroundMatcher(ctx context.Context, machine string) backgroundMatch {
	if machine == "" {
		return backgroundMatch{}
	}
	base, bgMachine, cfgMachine, mainID := s.cachedBackgroundSettings(ctx)
	if base == "" || s.isWorkMachine(machine) {
		return backgroundMatch{}
	}
	if cfgMachine == "" && bgMachine == "" {
		cfg := s.settings(ctx)
		bgMachine = cfg.BackgroundSupervisorMachineID
		cfgMachine = cfg.MachineID
		if mainID == "" {
			mainID = cfg.Main
		}
	}
	authorized := s.sameMachine(machine, cmp.Or(bgMachine, cfgMachine))
	if !authorized {
		s.origins.mu.Lock()
		if s.originsLocked(ctx) {
			if o := s.origins.m[base]; o != nil && o.By == originByStart && s.sameMachine(machine, o.MachineID) {
				authorized = true
			}
		}
		s.origins.mu.Unlock()
	}
	if !authorized {
		return backgroundMatch{}
	}
	m := backgroundMatch{base: base, main: mainID}
	r := &s.background.rounds
	r.mu.Lock()
	s.backgroundForksLocked(ctx)
	for id, entry := range r.forks {
		if entry.Base == base && (entry.Machine == "" || s.sameMachine(machine, entry.Machine)) {
			if m.forks == nil {
				m.forks = map[string]struct{}{}
			}
			m.forks[id] = struct{}{}
		}
	}
	r.mu.Unlock()
	return m
}

func (m backgroundMatch) isFork(sess *proto.Session) bool {
	if m.base == "" || len(m.forks) == 0 || sess.PaneID != "" || sess.SessionID == "" || sess.SessionID == m.base || sess.SessionID == m.main {
		return false
	}
	_, ok := m.forks[sess.SessionID]
	return ok
}

func (m backgroundMatch) matches(sess *proto.Session) bool {
	if m.base == "" || sess.PaneID != "" || sess.SessionID == "" || sess.SessionID == m.main {
		return false
	}
	return sess.SessionID == m.base || m.isFork(sess)
}

// hiddenBackgroundRoundForks returns the session ids in list that are forked background supervisor round branches
// and must stay out of the ordinary Agents/session list.
func (s *Service) hiddenBackgroundRoundForks(ctx context.Context, machine string, list []proto.Session) map[[2]string]bool {
	m := s.backgroundMatcher(ctx, machine)
	if m.base == "" {
		return nil
	}
	var out map[[2]string]bool
	for i := range list {
		if m.isFork(&list[i]) {
			if out == nil {
				out = map[[2]string]bool{}
			}
			out[[2]string{machine, list[i].SessionID}] = true
		}
	}
	return out
}

// superviseRoundAnswered notes how round res of legacy background supervisor to went (deliverRelay).
func (s *Service) superviseRoundAnswered(to string, res superviseRoundReply) {
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	if res.Context == "" {
		if !r.oldNode {
			r.oldNode = true
			s.logf("supervise: a background round's answer says nothing of its context (a node from before #7315 keeps the whole conversation, or the same message was asked again)")
		}
		return
	}
	if res.Context == "fresh" {
		s.logf("supervise: background round %s: fresh", short(to))
	} else {
		s.logf("supervise: background round %s: kept (%s)", short(to), res.Why)
	}
	why := ""
	switch {
	case res.Context == "kept" && res.Why == "no_anchor":
		why = "no_anchor"
	case res.TranscriptBytes > backgroundTranscriptLimit && (res.Context == "fresh" || res.Why == "start_failed"):
		// Only where rounds restart the process: a conversation that keeps its context for good is neither restarted
		// per round nor spared idle compaction, so its size alone is no reason to move the work.
		why = "oversized"
	}
	if why == "" {
		return
	}
	if r.outgrown == nil {
		r.outgrown = map[string]string{}
	}
	r.outgrown[to] = why
}

// backgroundOutgrown reports why legacy background supervisor id can no longer start another clean round. It reserves
// the conversation in one step with the idle check, so no message goes into it while its unread work moves to main
// (backgroundLost). A busy or draining conversation stays eligible for a later digest. background.mu is held.
func (s *Service) backgroundOutgrown(ctx context.Context, cfg Settings, id string) string {
	if backgroundKeepsContext(cfg) {
		return ""
	}
	r := &s.background.rounds
	r.mu.Lock()
	why := r.outgrown[id]
	r.mu.Unlock()
	if why == "" {
		return ""
	}
	rl := &s.relays
	rl.mu.Lock()
	reserved := s.relayLocked(ctx) && !rl.draining[id]
	if reserved {
		rl.replacing[id] = true
	}
	rl.mu.Unlock()
	if !reserved {
		return ""
	}
	if c, ok := s.session(ctx, cfg.MachineID, id); !ok || c.Status == proto.StatusWorking || c.Starting {
		s.releaseBackground(ctx, cfg.MachineID, id)
		return ""
	}
	return why
}

// releaseBackground lets a legacy background supervisor whose move to main was deferred take messages again, and has
// what waits for it go in now that may: a drain passed it by meanwhile.
func (s *Service) releaseBackground(ctx context.Context, machine, id string) {
	rl := &s.relays
	rl.mu.Lock()
	delete(rl.replacing, id)
	rl.mu.Unlock()
	if c, ok := s.session(ctx, machine, id); ok {
		s.drainRelays(machine, []proto.Session{c})
	}
}

// lastReviewEntry is a task's last review as a Fresh supervision message repeats it (#7315): its verdict, when (local)
// and its reason or note in a clipped line, so a round that does not see the earlier ones neither asks again what was
// decided nor sends back what was sent back already. nil when it was never reviewed. Only Fresh messages show it.
func (s *Service) lastReviewEntry(ctx context.Context, v *Supervision) *prompts.SuperviseLastReview {
	if v == nil || len(v.Reviews) == 0 {
		return nil
	}
	r := v.Reviews[len(v.Reviews)-1]
	note := r.Reason
	if note == "" {
		note = r.Note
	}
	return &prompts.SuperviseLastReview{Verdict: r.Verdict, At: r.At.In(s.location(ctx)).Format("01-02 15:04"), Note: clip(oneLine(note), lastReviewClip)}
}

// lastReviewClip is how many characters of a last review's reason or note a Fresh entry keeps.
const lastReviewClip = 200
