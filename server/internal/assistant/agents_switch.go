package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// agents_switch (#6008): the assistant moves many Claude Code agents to another launcher in one call, when the
// user asks ("move everything on the default launcher to <launcher>"). Each agent goes as its state allows:
//
//   - stopped on a usage limit: switched now and sent the usage-limit continuation (agent_switch's switch_now);
//   - idle: switched now with nothing sent (node session.switch): it waits on the new launcher for the next message;
//   - stopped (a Persona-started agent nothing runs now): its record is pointed at the new launcher and model (node
//     session.switch with the launcher), so its next Resume, or the message that resumes it, comes up there;
//   - working, waiting for the user, or with background work: queued here (switchQueue, persisted under a settings
//     key) and carried out when its turn ends, with the continuation when the turn ended on a usage limit;
//   - refused by the checks here or the node's, or a switch that failed (rolled back by the node): reported, nothing
//     changed, not tried again.
//
// A queued switch is the manager's, kept apart from the user's own pick in the agent's chat (the node's pending_switch,
// carried out by the user's message with apply_switch): that one behaves as before. A queued switch is dropped when the
// agent moved to another launcher meanwhile (from), went, or after switchQueueMaxAge; one that stopped meanwhile has its
// Resume pointed there instead (retargetStopped).

const (
	// switchQueueKey is the settings key the queued switches are kept under.
	switchQueueKey = "agents.switch_queue"
	// switchQueueMaxAge: a queued switch not carried out by then is dropped (as the node drops a pick a day old).
	switchQueueMaxAge = 24 * time.Hour
	// switchRetryAfter: after the node found a queued agent busy, the next try waits at least this long (the pick runs
	// the node's checks: a login shell, the launcher's models).
	switchRetryAfter = 30 * time.Second
	// switchParallel: how many agents of one call are switched at once.
	switchParallel = 4
)

// Outcomes of agents_switch, per agent.
const (
	switchContinued = "switched_and_continued"
	switchDone      = "switched"
	switchOnResume  = "switched_on_resume" // a stopped agent: its next Resume comes up on the new launcher
	switchQueued    = "queued"
	switchRefused   = "refused"
)

type agentsSwitchArgs struct {
	Launcher string `json:"launcher"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	// Agents: the agents to move; empty: every running Claude Code agent the filters keep.
	Agents []struct {
		Machine   string `json:"machine"`
		SessionID string `json:"session_id"`
	} `json:"agents"`
	Machine      string `json:"machine"`       // filter: only this computer's
	FromLauncher string `json:"from_launcher"` // filter: only those on this launcher ("claude": the default)
	Preview      bool   `json:"preview"`
}

// switchItem is one agent of a call: what is planned for it, and how it ended.
type switchItem struct {
	m       Device
	c       proto.Session
	plan    string // switchContinued / switchDone / switchQueued, or switchRefused with why
	why     string
	outcome string
}

func (s *Service) toolAgentsSwitch(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a agentsSwitchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	t := switchTarget{Launcher: strings.TrimSpace(a.Launcher), Model: strings.TrimSpace(a.Model), Effort: strings.TrimSpace(a.Effort)}
	if t.Launcher == "" {
		return "", errors.New("launcher is required: the name of the launcher to move the agents to")
	}
	items, err := s.switchItems(ctx, a, t)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "No running Claude Code agent matches: nothing to move.", nil
	}
	if a.Preview {
		return switchReport("Preview, nothing done yet", t, items, true), nil
	}
	var lines []string
	for _, it := range items {
		if it.plan != switchRefused {
			lines = append(lines, fmt.Sprintf("%s · %s (%s)", agentLabel(it.c), it.m.Name, it.plan))
		}
	}
	if len(lines) == 0 {
		return switchReport("Nothing could be moved", t, items, false), nil
	}
	// One confirmation for the whole batch (in the health space: one card), never one per agent.
	summary := fmt.Sprintf("→ %s\n\n%s", t.Launcher, strings.Join(lines, "\n"))
	if ok, err := s.confirmOutbound(ctx, machine, self, "agents_switch", summary, args); err != nil {
		return "", err
	} else if !ok {
		return "The user declined on the phone: no agent was moved and nothing was sent.", nil
	}
	sem := make(chan struct{}, switchParallel)
	var wg sync.WaitGroup
	for i := range items {
		it := &items[i]
		if it.plan == switchRefused {
			it.outcome = switchRefused
			continue
		}
		if it.plan == switchQueued {
			it.outcome = s.queueSwitch(ctx, it, t, self)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.switchNowOrQueue(ctx, it, t, self)
		}()
	}
	wg.Wait()
	return switchReport("Done", t, items, false), nil
}

// switchItems are the agents a call names, or those its filters keep, each with its plan.
func (s *Service) switchItems(ctx context.Context, a agentsSwitchArgs, t switchTarget) ([]switchItem, error) {
	var out []switchItem
	if len(a.Agents) > 0 {
		seen := map[[2]string]bool{}
		for _, ag := range a.Agents {
			m, c, err := s.findAgent(ctx, ag.Machine, ag.SessionID)
			if err != nil {
				return nil, fmt.Errorf("%s: %w. Nothing was done", ag.SessionID, err)
			}
			if k := [2]string{m.ID, c.SessionID}; !seen[k] {
				seen[k] = true
				out = append(out, s.switchPlan(ctx, m, c, t))
			}
		}
		return out, nil
	}
	from := strings.TrimSpace(a.FromLauncher)
	for _, m := range s.devices(ctx) {
		if a.Machine != "" {
			if want, ok := s.machineByName(ctx, a.Machine); !ok || want.ID != m.ID {
				continue
			}
		}
		list, _ := s.sessions(ctx, m.ID)
		for _, c := range list {
			// The filter takes the running Claude Code agents in terminals; the rest is not what "all of them" means.
			// A stopped one only when Persona started it: its record says how it resumes.
			if c.Assistant || c.SourceKind != "" || c.Archived || (c.Agent != "" && c.Agent != "claude") ||
				switchStopped(c) && !c.StartedByPersona {
				continue
			}
			own := cmp.Or(c.Launcher, "claude")
			if own == t.Launcher || from != "" && own != from {
				continue
			}
			out = append(out, s.switchPlan(ctx, m, c, t))
		}
	}
	slices.SortStableFunc(out, func(x, y switchItem) int { return cmp.Compare(x.m.Name+x.c.SessionID, y.m.Name+y.c.SessionID) })
	return out, nil
}

// switchPlan is what a call does with agent c on m: why not (refused), or how it moves by its state now.
func (s *Service) switchPlan(ctx context.Context, m Device, c proto.Session, t switchTarget) switchItem {
	it := switchItem{m: m, c: c}
	refuse := func(err error) switchItem {
		it.plan, it.why = switchRefused, err.Error()
		return it
	}
	stopped := switchStopped(c)
	if stopped && !c.Assistant && c.SourceKind == "" && (c.Agent == "" || c.Agent == "claude") {
		if cmp.Or(c.Launcher, "claude") == t.Launcher {
			return refuse(fmt.Errorf("resumes on %s already", t.Launcher))
		}
		if err := s.switchReachable(ctx, m, t.Launcher, nodeproto.LauncherSwitchIdle); err != nil {
			return refuse(err)
		}
		it.plan = switchOnResume
		return it
	}
	if err := switchUnsupported(c, t.Launcher); errors.Is(err, errOnLauncherAlready) {
		return refuse(fmt.Errorf("runs on %s already", t.Launcher))
	} else if err != nil {
		return refuse(err)
	}
	capability := nodeproto.LauncherSwitchIdle // the idle ones go with session.switch, and a queued one may too
	if err := s.switchReachable(ctx, m, t.Launcher, capability); err != nil {
		return refuse(err)
	}
	switch {
	case limitStopped(c):
		it.plan = switchContinued
	case switchWaits(c):
		it.plan = switchQueued
	default:
		it.plan = switchDone
	}
	return it
}

// switchStopped: nothing runs agent c now (it stopped, or its terminal went).
func switchStopped(c proto.Session) bool {
	return stoppedAgent(c) || c.Status == proto.StatusOffline || c.Status == proto.StatusInterrupted
}

// limitStopped: c's last turn ended on a subscription's usage limit and nothing runs it now.
func limitStopped(c proto.Session) bool {
	return c.TurnErrorKind == proto.TurnErrorUsageLimit && c.TurnError != "" && c.Status != proto.StatusWorking && !c.Starting
}

// switchWaits: c is busy as the node would refuse a switch now (working, waiting for the user, starting, background
// work its quit would end): its switch is queued for the end of its turn.
func switchWaits(c proto.Session) bool {
	return c.Status == proto.StatusWorking || c.Starting || c.State == proto.StateWorking || c.State == proto.StateNeedsYou ||
		c.PendingInteractions > 0 || c.BackgroundWorking || len(c.Background) > 0 || c.Loop != nil
}

// switchNowOrQueue switches it now (with the continuation when its usage limit stopped it); one the node finds busy
// (switch_busy) is queued instead.
func (s *Service) switchNowOrQueue(ctx context.Context, it *switchItem, t switchTarget, self string) {
	if it.plan == switchOnResume {
		if err := s.retargetStopped(ctx, it.m, it.c, t); err != nil {
			it.outcome, it.why = switchRefused, err.Error()
			return
		}
		it.outcome = switchOnResume
		s.logf("agents_switch %s on %s: stopped; resumes on %s now (from %s)", short(it.c.SessionID), it.m.ID, t.Launcher, short(self))
		return
	}
	text := ""
	if it.plan == switchContinued {
		text = prompts.RenderAgentLimitContinue()
		if _, theirs, err := s.supervisedElsewhere(ctx, self, it.m, it.c); err == nil && theirs {
			text = prompts.RenderSideConversation(prompts.SideConversation{From: self, Text: text})
		}
	}
	err := s.moveAgent(ctx, it.m, it.c, t, text)
	switch {
	case err == nil:
		it.outcome = it.plan
		s.logf("agents_switch %s on %s: %s → %s (%s, from %s)", short(it.c.SessionID), it.m.ID, cmp.Or(it.c.Launcher, "claude"), t.Launcher, it.plan, short(self))
	case switchBusy(err):
		it.outcome = s.queueSwitch(ctx, it, t, self)
	default:
		it.outcome, it.why = switchRefused, err.Error()
	}
}

// retargetStopped points agent c, which nothing runs now, at launcher t (node session.switch with the launcher): its
// next Resume, or the message that resumes it, comes up there. Nothing starts now.
func (s *Service) retargetStopped(ctx context.Context, m Device, c proto.Session, t switchTarget) error {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentSwitchWait)
	defer cancel()
	p := map[string]any{"session_id": c.SessionID, "launcher": t.Launcher, "from": cmp.Or(c.Launcher, "claude")}
	if t.Model != "" {
		p["model"] = t.Model
	}
	return s.call(wctx, m.ID, DeviceSessionSwitch, p, nil)
}

// switchBusy: the node refused a switch because the agent is busy.
func switchBusy(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeSwitchBusy
}

// switchReport is a call's answer: one line per agent.
func switchReport(head string, t switchTarget, items []switchItem, preview bool) string {
	counts := map[string]int{}
	var b strings.Builder
	for _, it := range items {
		what := it.outcome
		if preview {
			what = it.plan
		}
		counts[what]++
		fmt.Fprintf(&b, "- %s · machine=%s session_id=%s · launcher=%s: ", agentLabel(it.c), it.m.ID, it.c.SessionID, cmp.Or(it.c.Launcher, "claude"))
		switch what {
		case switchContinued:
			b.WriteString(map[bool]string{true: "will switch now and continue (its usage limit stopped it)", false: "switched and continued"}[preview])
		case switchDone:
			b.WriteString(map[bool]string{true: "will switch now, nothing sent (idle)", false: "switched, nothing sent; it waits there for the next message"}[preview])
		case switchOnResume:
			b.WriteString(map[bool]string{true: "stopped: will switch, takes effect when it is resumed", false: "switched (takes effect when resumed): its next Resume comes up there"}[preview])
		case switchQueued:
			b.WriteString(map[bool]string{true: "will be queued: busy now, switches when its turn ends", false: "queued: busy now, switches when its turn ends"}[preview])
		default:
			fmt.Fprintf(&b, "refused, nothing changed: %s", it.why)
		}
		b.WriteString("\n")
	}
	parts := []string{}
	for _, k := range []string{switchContinued, switchDone, switchOnResume, switchQueued, switchRefused} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}
	return fmt.Sprintf("%s (→ launcher %s): %s\n%s", head, t.Launcher, strings.Join(parts, ", "), b.String())
}

// ---- the queue ----

// queuedSwitch is a switch waiting for the end of its agent's turn.
type queuedSwitch struct {
	Machine  string    `json:"machine"`
	Session  string    `json:"session"`
	Launcher string    `json:"launcher"`
	Model    string    `json:"model,omitempty"`
	Effort   string    `json:"effort,omitempty"`
	From     string    `json:"from"` // the launcher it ran on when queued: one moved meanwhile is left alone
	At       time.Time `json:"at"`
	By       string    `json:"by,omitempty"` // the conversation that asked
}

// switchQueue is the manager's queued switches by machine/session, read from the store once.
type switchQueue struct {
	mu      sync.Mutex
	loaded  bool
	items   map[string]queuedSwitch
	running map[string]bool      // being carried out now
	retry   map[string]time.Time // not tried again before (the node found it busy)
}

func switchKey(machine, session string) string { return machine + "/" + session }

// loadSwitchQueue reads the queued switches once; without a store they live in memory only.
func (s *Service) loadSwitchQueue(ctx context.Context) {
	q := &s.switchQ
	q.mu.Lock()
	loaded := q.loaded
	q.mu.Unlock()
	if loaded {
		return
	}
	items := map[string]queuedSwitch{}
	if st := s.archiveStore(); st != nil {
		if _, err := st.Setting(ctx, switchQueueKey, &items); err != nil {
			s.logf("switch queue: read: %v", err)
			return // tried again on the next look
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.loaded {
		return
	}
	if items == nil {
		items = map[string]queuedSwitch{}
	}
	maps.Copy(items, q.items) // queued while it was read
	q.items, q.running, q.retry, q.loaded = items, map[string]bool{}, map[string]time.Time{}, true
}

// saveSwitchQueue writes the queue as it is now.
func (s *Service) saveSwitchQueue(ctx context.Context) {
	st := s.archiveStore()
	if st == nil {
		return
	}
	q := &s.switchQ
	q.mu.Lock()
	items := maps.Clone(q.items)
	q.mu.Unlock()
	if err := st.PutSetting(ctx, switchQueueKey, items); err != nil {
		s.logf("switch queue: write: %v", err)
	}
}

// queueSwitch queues it for the end of its turn; switchQueued, or switchRefused when it cannot be kept.
func (s *Service) queueSwitch(ctx context.Context, it *switchItem, t switchTarget, self string) string {
	s.loadSwitchQueue(ctx)
	q := &s.switchQ
	q.mu.Lock()
	if q.items == nil {
		q.items = map[string]queuedSwitch{}
	}
	q.items[switchKey(it.m.ID, it.c.SessionID)] = queuedSwitch{Machine: it.m.ID, Session: it.c.SessionID, Launcher: t.Launcher,
		Model: t.Model, Effort: t.Effort, From: cmp.Or(it.c.Launcher, "claude"), At: time.Now().UTC(), By: self}
	q.mu.Unlock()
	s.saveSwitchQueue(ctx)
	s.logf("agents_switch %s on %s: queued → %s for the end of its turn", short(it.c.SessionID), it.m.ID, t.Launcher)
	return switchQueued
}

// queuedSwitchOf is the launcher session id on machine is queued to switch to, "" when none.
func (s *Service) queuedSwitchOf(machine, id string) string {
	q := &s.switchQ
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.items[switchKey(machine, id)].Launcher
}

// switchQueueListed follows machine's new session list: a queued agent whose turn ended is switched now.
func (s *Service) switchQueueListed(machine string) {
	q := &s.switchQ
	q.mu.Lock()
	loaded := q.loaded
	idle := loaded && !slices.ContainsFunc(slices.Collect(maps.Values(q.items)), func(w queuedSwitch) bool { return w.Machine == machine })
	q.mu.Unlock()
	if idle || s.archiveStore() == nil && !loaded {
		return // nothing queued there (or nothing ever queued without a store)
	}
	go s.runSwitchQueue(context.Background(), machine)
}

// runSwitchQueue carries out machine's queued switches whose agents are no longer busy, and drops the ones that no
// longer apply.
func (s *Service) runSwitchQueue(ctx context.Context, machine string) {
	s.loadSwitchQueue(ctx)
	q := &s.switchQ
	now := time.Now()
	q.mu.Lock()
	var mine []queuedSwitch
	for k, w := range q.items {
		if w.Machine == machine && !q.running[k] {
			mine = append(mine, w)
		}
	}
	q.mu.Unlock()
	type due struct {
		w queuedSwitch
		c proto.Session
	}
	var run []due
	changed := false
	for _, w := range mine {
		c, ok := s.decoratedSession(machine, w.Session) // outside the lock: it may read the archives
		drop := ""
		switch {
		case now.Sub(w.At) > switchQueueMaxAge:
			drop = "queued over a day ago"
		case !ok || switchStopped(c) && !c.StartedByPersona:
			drop = "it stopped or is gone"
		case cmp.Or(c.Launcher, "claude") == w.Launcher:
			drop = "it runs there already"
		case cmp.Or(c.Launcher, "claude") != w.From:
			drop = "it moved to another launcher meanwhile"
		case switchStopped(c): // stopped meanwhile: its Resume is pointed there instead
		case !limitStopped(c) && switchWaits(c):
			continue
		}
		k := switchKey(w.Machine, w.Session)
		q.mu.Lock()
		later := now.Before(q.retry[k])
		q.mu.Unlock()
		if drop == "" && later { // the node found it busy a moment ago
			continue
		}
		q.mu.Lock()
		cur, still := q.items[k]
		if !still || cur != w || q.running[k] { // changed meanwhile: the next look takes it
			q.mu.Unlock()
			continue
		}
		if drop != "" {
			delete(q.items, k)
			changed = true
		} else {
			q.running[k] = true
			run = append(run, due{w, c})
		}
		q.mu.Unlock()
		if drop != "" {
			s.logf("agents_switch %s on %s: queued switch to %s dropped: %s", short(w.Session), machine, w.Launcher, drop)
		}
	}
	if changed {
		s.saveSwitchQueue(ctx)
	}
	for _, d := range run {
		go s.runQueuedSwitch(ctx, d.w, d.c)
	}
}

// runQueuedSwitch carries out one queued switch now: kept for a later try when the node finds the agent busy again,
// dropped otherwise (done or refused).
func (s *Service) runQueuedSwitch(ctx context.Context, w queuedSwitch, c proto.Session) {
	k := switchKey(w.Machine, w.Session)
	q := &s.switchQ
	m, ok := s.device(ctx, w.Machine)
	if !ok {
		m = Device{ID: w.Machine, Name: w.Machine}
	}
	t := switchTarget{Launcher: w.Launcher, Model: w.Model, Effort: w.Effort}
	text := ""
	var err error
	if switchStopped(c) {
		err = s.retargetStopped(ctx, m, c, t)
	} else {
		if limitStopped(c) {
			text = prompts.RenderAgentLimitContinue()
		}
		err = s.moveAgent(ctx, m, c, t, text)
	}
	q.mu.Lock()
	delete(q.running, k)
	keep := switchBusy(err)
	if keep {
		q.retry[k] = time.Now().Add(switchRetryAfter)
	} else {
		delete(q.items, k)
		delete(q.retry, k)
	}
	q.mu.Unlock()
	switch {
	case keep:
		s.logf("agents_switch %s on %s: queued switch to %s waits: busy again", short(w.Session), w.Machine, w.Launcher)
		return
	case err != nil:
		s.logf("agents_switch %s on %s: queued switch to %s refused, dropped: %v", short(w.Session), w.Machine, w.Launcher, err)
	default:
		s.logf("agents_switch %s on %s: queued switch %s → %s carried out (continued: %v)", short(w.Session), w.Machine, w.From, w.Launcher, text != "")
	}
	s.saveSwitchQueue(ctx)
}
