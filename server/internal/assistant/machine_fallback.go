package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Automatic cross-machine failover (#15119, docs/assistant/agent-work.md "Offline computer: automatic machine failover"):
// when the computer hosting the main conversation, an active direction leader, or unbound default scheduled routines
// stays offline past assistant.machine_fallback_minutes, Persona moves them to the first eligible backup computer in
// assistant.machine_fallbacks (online, non-work, with a healthy compatible launcher supporting SupervisionPolicy).
// Empty assistant.machine_fallbacks (the default) disables failover completely.

const (
	defaultMachineFallbackMinutes = 10
	minMachineFallbackMinutes     = 1
	maxMachineFallbackMinutes     = 1440
	maxMachineFallbacks           = 8
	machineFallbackKeep           = 7 * 24 * time.Hour
	machineFallbackSweepTimeout   = 2 * time.Minute
)

// conversationSwitchMachineFallback marks a batch opened by automatic cross-machine failover.
type conversationSwitchMachineFallback struct {
	SourceMachine string `json:"source_machine"`
	TargetMachine string `json:"target_machine"`
	Drained       bool   `json:"drained,omitempty"`
}

func machineFallbacksOf(v Settings) []string {
	return cleanMachineFallbacks(v.MachineFallbacks)
}

func cleanMachineFallbacks(names []string) []string {
	var out []string
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func machineFallbackMinutesOf(v Settings) int {
	if v.MachineFallbackMinutes != nil && *v.MachineFallbackMinutes >= minMachineFallbackMinutes && *v.MachineFallbackMinutes <= maxMachineFallbackMinutes {
		return *v.MachineFallbackMinutes
	}
	return defaultMachineFallbackMinutes
}

func machineFallbackThreshold(v Settings) time.Duration {
	return time.Duration(machineFallbackMinutesOf(v)) * time.Minute
}

func (s *Service) machinePhysicallyOnline(ctx context.Context, machine string) bool {
	if machine == "" {
		return false
	}
	if pd := s.physicalDevices(); pd != nil {
		if m, ok := pd.Summary(machine); ok {
			return m.Online
		}
	}
	if d, ok := s.device(ctx, machine); ok {
		return d.Online || d.Reachable
	}
	return false
}

func (s *Service) machinePhysicallyOffline(ctx context.Context, machine string) bool {
	if machine == "" {
		return false
	}
	if pd := s.physicalDevices(); pd != nil {
		if m, ok := pd.Summary(machine); ok {
			return !m.Online
		}
	}
	if d, ok := s.device(ctx, machine); ok {
		return !d.Online && !d.Reachable
	}
	return false
}

// isFailoverEligibleOffline reports whether (machine, sid) is a protected assistant conversation
// (main, an active leader, or an already adopted logical conversation) allowed to switch while its
// source machine is physically offline.
func (s *Service) isFailoverEligibleOffline(ctx context.Context, machine, sid string) bool {
	if machine == "" || sid == "" {
		return false
	}
	cfg := s.settings(ctx)
	if sid == cfg.Main && (machine == cfg.mainRouteMachine() || machine == cfg.MachineID) {
		return true
	}
	if slices.Contains(cfg.Leaders, sid) && machine == cfg.MachineID {
		return true
	}
	if st, ok := s.store().(logicalConversationStore); ok {
		if c, err := st.LogicalConversation(ctx, machine, sid); err == nil && c != nil && !c.Deleted {
			return true
		}
	}
	return false
}

// offlineHistoryTailSeq returns the last persisted sequence number in Postgres ConversationHistory
// for (machine, sid) when the source machine is offline.
func (s *Service) offlineHistoryTailSeq(ctx context.Context, machine, sid string) (int, bool) {
	hs, ok := s.store().(conversationHistoryStore)
	if !ok {
		return 0, false
	}
	h, err := hs.ConversationHistory(ctx, machine, sid)
	if err != nil || h == nil || h.History == "" {
		return 0, false
	}
	if h.TailSeq != nil {
		return *h.TailSeq, true
	}
	if len(h.Coverage) > 0 {
		return h.Coverage[len(h.Coverage)-1].Through, true
	}
	return 0, true
}

// machineFallbackConversations returns the logical (machine, session) pairs protected by automatic
// cross-machine failover: the main conversation and active direction leaders.
func (s *Service) machineFallbackConversations(ctx context.Context) [][2]string {
	cfg := s.settings(ctx)
	var out [][2]string
	add := func(machine, sid string) {
		if machine == "" || sid == "" {
			return
		}
		pair := [2]string{machine, sid}
		if !slices.Contains(out, pair) {
			out = append(out, pair)
		}
	}
	if cfg.Main != "" {
		add(cfg.mainRouteMachine(), cfg.Main)
	}
	for _, leader := range cfg.Leaders {
		add(cfg.MachineID, leader)
	}
	return out
}

// machineFallbackCandidate picks the first backup computer and healthy launcher from
// assistant.machine_fallbacks that is physically online, not the current computer, not a work
// computer, and supports SupervisionPolicy.
func (s *Service) machineFallbackCandidate(ctx context.Context, current conversationExecution) (targetMachine, targetLauncher, passed string) {
	cfg := s.settings(ctx)
	fallbacks := machineFallbacksOf(cfg)
	if len(fallbacks) == 0 {
		return "", "", "none configured"
	}
	var skipped []string
	for _, cand := range fallbacks {
		if cand == "" || cand == current.Machine {
			continue
		}
		if s.isWorkMachine(cand) {
			skipped = append(skipped, cand+": work machine")
			continue
		}
		if !s.machinePhysicallyOnline(ctx, cand) {
			skipped = append(skipped, cand+": offline")
			continue
		}
		launcher, reason := s.machineFallbackLauncher(ctx, cand, current.Launcher, cfg)
		if launcher == "" {
			skipped = append(skipped, cand+": "+cmp.Or(reason, "no compatible launcher"))
			continue
		}
		return cand, launcher, strings.Join(skipped, "; ")
	}
	return "", "", strings.Join(skipped, "; ")
}

func (s *Service) machineFallbackLauncher(ctx context.Context, machine, preferred string, cfg Settings) (launcher, passed string) {
	var candidates []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || proto.IsExternalLauncher(name) || slices.Contains(candidates, name) {
			return
		}
		candidates = append(candidates, name)
	}
	add(preferred)
	add(cfg.Launcher)
	add(s.launcherNow(ctx))
	for _, fb := range limitFallbacksOf(cfg) {
		add(fb)
	}
	if list, known := s.launchersHere(ctx, machine); known {
		for _, l := range list {
			if l.Source != proto.ExternalLauncherSource {
				add(l.Name)
			}
		}
	}
	add("claude")

	var skipped []string
	for _, name := range candidates {
		if h, found := s.launcherHealth(ctx, machine, name); found && h.unusable() {
			skipped = append(skipped, name+": "+cmp.Or(h.Kind, h.State))
			continue
		}
		target, err := s.logicalTarget(ctx, machine, name)
		if err != nil {
			skipped = append(skipped, name+": "+err.Error())
			continue
		}
		if !target.SupervisionPolicy {
			skipped = append(skipped, name+": no supervision policy")
			continue
		}
		return name, strings.Join(skipped, "; ")
	}
	return "", strings.Join(skipped, "; ")
}

// machineFallbackSweep checks whether the main conversation or any active direction leader is
// hosted on a computer that has been physically offline past assistant.machine_fallback_minutes,
// and starts a durable cross-machine switch batch to the first eligible backup computer.
func (s *Service) machineFallbackSweep(ctx context.Context, now time.Time) {
	cfg := s.settings(ctx)
	if len(machineFallbacksOf(cfg)) == 0 || s.store() == nil {
		return
	}
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return
	}
	if !s.segmentsEnabled(ctx) {
		return
	}
	targets := s.machineFallbackConversations(ctx)
	if len(targets) == 0 {
		return
	}
	threshold := machineFallbackThreshold(cfg)
	sweepCtx, cancel := context.WithTimeout(ctx, machineFallbackSweepTimeout)
	defer cancel()

	for _, pair := range targets {
		logicalMachine, sid := pair[0], pair[1]
		c, err := st.LogicalConversation(sweepCtx, logicalMachine, sid)
		if err != nil {
			continue
		}
		if c != nil && (c.Deleted || c.Switch != nil) {
			continue
		}
		row, known := s.session(sweepCtx, logicalMachine, sid)
		if known && row.IsDeleted() {
			continue
		}
		if !known {
			row = proto.Session{
				MachineID:      logicalMachine,
				SessionID:      sid,
				Assistant:      true,
				Launcher:       cfg.Launcher,
				PermissionMode: cmp.Or(cfg.DefaultPermissionMode, "default"),
			}
		}
		current := conversationExecutionNow(logicalMachine, row, c)
		if s.machinePhysicallyOnline(sweepCtx, current.Machine) {
			s.noteNodeOnline(current.Machine, now)
			continue
		}
		if !s.machinePhysicallyOffline(sweepCtx, current.Machine) {
			continue
		}
		offlineAt, tracked := s.nodeOfflineSince(current.Machine)
		if !tracked {
			s.noteNodeOffline(current.Machine, now)
			continue
		}
		if now.Sub(offlineAt) < threshold {
			continue
		}
		s.machineFallbackStart(sweepCtx, st, logicalMachine, sid, current, c, now)
	}
}

func (s *Service) machineFallbackStart(ctx context.Context, st logicalSwitchStore, logicalMachine, sid string, current conversationExecution, c *db.LogicalConversation, now time.Time) {
	targetMachine, targetLauncher, passed := s.machineFallbackCandidate(ctx, current)
	if targetMachine == "" {
		s.logf("machine fallback %s/%s: no backup computer available (%s)", logicalMachine, short(sid), cmp.Or(passed, "none eligible"))
		return
	}

	book := &s.conversationSwitches
	book.mu.Lock()
	defer book.mu.Unlock()
	state, _, err := s.loadConversationSwitches(ctx)
	if err != nil {
		s.logf("machine fallback %s/%s: switch state: %v", logicalMachine, short(sid), err)
		return
	}
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			s.logf("machine fallback %s/%s: switch state: %v", logicalMachine, short(sid), err)
			return
		}
	}
	defer func() { book.settle(state) }()

	if conversationSwitchPending(state, logicalMachine, sid) {
		return
	}
	// Re-read the logical conversation under book.mu so epoch fencing reflects the latest state.
	c, err = st.LogicalConversation(ctx, logicalMachine, sid)
	if err != nil || (c != nil && (c.Deleted || c.Switch != nil)) {
		return
	}
	epoch := int64(1)
	if c != nil {
		epoch = c.Epoch
		if active, ok := c.Active(); ok && active.MachineID != current.Machine {
			return
		}
	}
	item := conversationSwitchItem{
		Machine:        logicalMachine,
		ConversationID: sid,
		ExpectedEpoch:  epoch,
		Before:         current,
		Target: conversationExecution{
			Machine:        targetMachine,
			Launcher:       targetLauncher,
			PermissionMode: cmp.Or(current.PermissionMode, "default"),
		},
		OperationID: randomHex(16),
		Outcome:     conversationSwitchQueued,
	}
	batch := conversationSwitchBatch{
		ID:        randomHex(16),
		CreatedAt: now.UTC(),
		Items:     []conversationSwitchItem{item},
		FirstTurn: false,
		MachineFallback: &conversationSwitchMachineFallback{
			SourceMachine: current.Machine,
			TargetMachine: targetMachine,
		},
	}
	state.Batches = append(state.Batches, batch)
	if err := s.saveConversationSwitches(ctx, state); err != nil {
		s.logf("machine fallback %s/%s: save batch: %v", logicalMachine, short(sid), err)
		return
	}
	s.logf("machine fallback %s/%s: %s offline past threshold → %s/%s (batch %s)",
		logicalMachine, short(sid), current.Machine, targetMachine, targetLauncher, batch.ID)
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			s.logf("machine fallback %s/%s: save batch progress: %v", logicalMachine, short(sid), err)
		}
	}
}

// machineFallbacksLocked wakes queued undelivered and relay messages once a failover batch
// finishes switching, and prunes old settled failover batches.
func (s *Service) machineFallbacksLocked(ctx context.Context, state *conversationSwitchState, now time.Time) bool {
	changed := false
	for i := range state.Batches {
		b := &state.Batches[i]
		mf := b.MachineFallback
		if mf == nil || mf.Drained {
			continue
		}
		allSettled := true
		anySwitched := false
		for _, item := range b.Items {
			switch item.Outcome {
			case conversationSwitchSwitched:
				anySwitched = true
			case conversationSwitchRefused:
			default:
				allSettled = false
			}
		}
		if !allSettled {
			continue
		}
		if anySwitched {
			for _, item := range b.Items {
				if item.Outcome == conversationSwitchSwitched {
					s.wakeUndeliveredFor(item.Machine, item.ConversationID)
					s.wakeRelaysFor(item.Machine, item.ConversationID)
				}
			}
		}
		mf.Drained = true
		changed = true
	}
	if pruneMachineFallbacks(state, now) {
		changed = true
	}
	return changed
}

func pruneMachineFallbacks(state *conversationSwitchState, now time.Time) bool {
	before := len(state.Batches)
	state.Batches = slices.DeleteFunc(state.Batches, func(b conversationSwitchBatch) bool {
		if b.MachineFallback == nil || !b.MachineFallback.Drained || now.Sub(b.CreatedAt) <= machineFallbackKeep {
			return false
		}
		for _, item := range b.Items {
			if item.Outcome != conversationSwitchSwitched && item.Outcome != conversationSwitchRefused {
				return false
			}
		}
		return true
	})
	return len(state.Batches) != before
}

// wakeRelaysFor clears delivery backoff for sid and immediately drains its queued relay messages
// after a cross-machine failover rebind.
func (s *Service) wakeRelaysFor(machine, sid string) {
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) || len(b.st.Queue[sid]) == 0 {
		b.mu.Unlock()
		return
	}
	delete(b.failedAt, sid)
	delete(b.msgFailedAt, sid)
	delete(b.stuck, sid)
	delete(b.roleChange, sid)
	b.mu.Unlock()
	var list []proto.Session
	if s.devs != nil {
		if rows, ok := s.devs.Sessions(machine); ok {
			list = rows
		}
	}
	if !slices.ContainsFunc(list, func(c proto.Session) bool { return c.SessionID == sid }) {
		list = append(list, proto.Session{
			MachineID: machine,
			SessionID: sid,
			Assistant: true,
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
	}
	s.drainRelays(machine, list)
}

// machineFallbackScheduleMachine returns a backup computer for an unbound default scheduled
// routine when defMachine has been offline past assistant.machine_fallback_minutes.
func (s *Service) machineFallbackScheduleMachine(ctx context.Context, defMachine string, now time.Time) string {
	cfg := s.settings(ctx)
	fallbacks := machineFallbacksOf(cfg)
	if len(fallbacks) == 0 || defMachine == "" {
		return defMachine
	}
	if !s.machinePhysicallyOffline(ctx, defMachine) {
		return defMachine
	}
	offlineAt, tracked := s.nodeOfflineSince(defMachine)
	if !tracked || now.Sub(offlineAt) < machineFallbackThreshold(cfg) {
		return defMachine
	}
	for _, cand := range fallbacks {
		if cand == "" || cand == defMachine || s.isWorkMachine(cand) {
			continue
		}
		if s.machinePhysicallyOnline(ctx, cand) {
			return cand
		}
	}
	return defMachine
}

// ---- assistant.machine_fallbacks and assistant.machine_fallback_minutes in persona_settings ----

func (s *Service) getMachineFallbacks(ctx context.Context) (string, error) {
	var b strings.Builder
	cfg := s.settings(ctx)
	if now := machineFallbacksOf(cfg); len(now) == 0 {
		b.WriteString("now: [] (off: automatic cross-machine failover is disabled)\n")
	} else {
		raw, _ := json.Marshal(now)
		fmt.Fprintf(&b, "now: %s (threshold: %d minutes)\n", raw, machineFallbackMinutesOf(cfg))
	}
	devs := s.devices(ctx)
	if len(devs) > 0 {
		b.WriteString("available non-work computers:\n")
		for _, d := range devs {
			if s.isWorkMachine(d.ID) {
				continue
			}
			status := "offline"
			if d.Online || d.Reachable {
				status = "online"
			}
			fmt.Fprintf(&b, "- %s (%s, %s)\n", d.ID, cmp.Or(d.Name, d.ID), status)
		}
	}
	return b.String(), nil
}

func (s *Service) setMachineFallbacks(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, i18n.Err("assistant-settings_tool.bad_machine_fallbacks")
	}
	value := cleanMachineFallbacks(names)
	if len(value) > maxMachineFallbacks {
		return nil, i18n.Err("assistant-settings_tool.bad_machine_fallbacks")
	}
	if len(value) > 0 {
		devs := s.devices(ctx)
		known := map[string]bool{}
		for _, d := range devs {
			known[d.ID] = true
		}
		for _, id := range value {
			if s.isWorkMachine(id) {
				return nil, i18n.Err("assistant-settings_tool.machine_fallback_work_machine")
			}
			if len(known) > 0 && !known[id] {
				return nil, i18n.Err("assistant-settings_tool.machine_fallback_unknown_machine", i18n.Args{"machine": id})
			}
		}
	}
	before := machineFallbacksOf(s.settings(ctx))
	if slices.Equal(before, value) {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.machine_fallbacks_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	list := func(l i18n.Lang, names []string) string {
		if len(names) == 0 {
			return i18n.T(l, "assistant-settings_tool.none")
		}
		return strings.Join(names, " → ")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_machine_fallbacks", i18n.Args{"before": list(l, before), "after": list(l, value)})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MachineFallbacks = value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.machine_fallbacks_saved", i18n.Args{"value": list(s.lang(ctx), value)}), nil
	}}, nil
}

func (s *Service) setMachineFallbackMinutes(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var minutes *int
	if err := json.Unmarshal(raw, &minutes); err != nil || minutes == nil || *minutes < minMachineFallbackMinutes || *minutes > maxMachineFallbackMinutes {
		return nil, i18n.Err("assistant-settings_tool.bad_machine_fallback_minutes")
	}
	value := *minutes
	before := machineFallbackMinutesOf(s.settings(ctx))
	if before == value {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.machine_fallback_minutes_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_machine_fallback_minutes", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		vCopy := value
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MachineFallbackMinutes = &vCopy; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.machine_fallback_minutes_saved", i18n.Args{"value": value}), nil
	}}, nil
}
