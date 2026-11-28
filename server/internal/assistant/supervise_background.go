package assistant

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/supervisionrole"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The legacy background supervisor (#4927, docs/assistant/unified-entry.md "Background supervisor"). Since #5732 the
// planning lead or main supervises newly dispatched work. An already configured background conversation may
// finish notices explicitly queued for it, but is never a default owner for new work and needs no replacement.
// Pending and queued legacy notices are regathered or moved to main intact rather than dropped.
//
// The ledger keeps its assigned supervisor and the dispatch chat as origin. Task cards, receipts, the direction
// tree and review permissions keep one stable owner; the background conversation is only a compatibility endpoint for
// old work (isBackground, backgroundSupervises, backgroundLost). Each of its rounds starts from its first turn, with
// only that round's facts and tools (#7315, supervise_round.go), unless background_supervisor_keeps_context is on.

// backgroundBook serializes legacy background selection and holds what its rounds answered (supervise_round.go).
type backgroundBook struct {
	mu     sync.Mutex
	rounds superviseRounds
}

// purposeSupervise is the model purpose the background supervisor runs under; it inherits the main chat's.
const purposeSupervise = "assistant.supervise"

// backgroundGrace is how long after its start a legacy background conversation counts as there without its machine
// listing it yet.
var backgroundGrace = 2 * time.Minute

// backgroundOn reports whether the legacy background setting is enabled. New supervision stays with main regardless.
func (s *Service) backgroundOn(cfg Settings) bool {
	return !s.noBackground && (cfg.MainSupervisesOwnTasks == nil || !*cfg.MainSupervisesOwnTasks)
}

// mainSupervisesOwnTasks is the compatibility projection of assistant.main_supervises_own_tasks. Since #5732 main
// supervises new work even when this legacy setting is off.
func (s *Service) mainSupervisesOwnTasks(ctx context.Context) bool {
	return !s.backgroundOn(s.settings(ctx))
}

// ownSupervisor is the main conversation for task supervision (#5732). The background supervisor remains available
// for work already queued for it, but no new task signal is routed away from the main conversation.
func (s *Service) ownSupervisor(cfg Settings) string {
	return cfg.Main
}

// isBackground: conversation id is the one kept as the background supervisor, on or off. It stays listed, is neither
// archived nor made a leader, and may finish what it was sent before the setting was switched off.
func (s *Service) isBackground(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	cfg := s.settings(ctx)
	return id != cfg.Main && cfg.BackgroundSupervisor == id
}

// backgroundSupervises: self is the legacy background supervisor and has durable evidence for task t. A pre-upgrade
// ledger may name it directly; a notice delivered or queued for it is also persisted in relayState. Merely being
// configured is never enough: new main-supervised work cannot be accepted here, including after a restart.
func (s *Service) backgroundSupervises(ctx context.Context, self string, t Todo) bool {
	self = s.supervisionHoldKey(ctx, self) // an attested round fork uses its base's legacy evidence, never its own grant
	if t.Origin == "" || t.Supervision == nil || !s.isBackground(ctx, self) {
		return false
	}
	sup := t.Supervision.Supervisor
	if s.isLeaderRef(sup) {
		return false
	}
	if sup == self || s.legacyBackgroundNotice(ctx, self, t.ID) {
		return true
	}
	// A leader conversation on a runtime without supervision_policy cannot be told anything (digestTarget): its tasks are this route's to review.
	return s.isLeader(ctx, sup) && s.runtimeWithoutSupervisionPolicy(ctx, s.settings(ctx).MachineID, sup)
}

// legacyBackgroundKey is safe as a Postgres JSON object key: session and todo IDs cannot contain '|'. The former NUL
// separator made every durable relay write fail with SQLSTATE 22P05; reads still accept that old in-memory encoding.
func legacyBackgroundKey(conversation, todoID string) string { return conversation + "|" + todoID }

func legacyBackgroundGrant(grants map[string]bool, conversation, todoID string) bool {
	return grants[legacyBackgroundKey(conversation, todoID)] || grants[conversation+"\x00"+todoID]
}

// legacyBackgroundNotice reports whether a real old notice reached this background conversation. The in-memory grant
// covers the current turn; relayState keeps it across restarts and reconstructs it for pre-upgrade queued messages.
func (s *Service) legacyBackgroundNotice(ctx context.Context, self, todoID string) bool {
	self = s.supervisionHoldKey(ctx, self)
	b := &s.supervise
	// deliverDigest publishes the grant after a visible send or queue admission, before sendDigest releases dedupMu.
	// Serialize the check with that boundary so an immediate review cannot be rejected while its notice is in flight.
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	b.mu.Lock()
	granted := legacyBackgroundGrant(b.legacyBackground, self, todoID)
	b.mu.Unlock()
	if granted {
		return true
	}
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	return s.relayLocked(ctx) && legacyBackgroundGrant(r.st.LegacyBackground, self, todoID)
}

// noMainOriginFallback identifies only the new delivery fallback that must not become closeout evidence. conversation
// is the actual target, whereas g.To may still name an ask's old, now unreachable rung (routeTo). A current Supervisor
// is not historical delivery evidence: main may have been restored and removed while the signal waited.
func (s *Service) noMainOriginFallback(cfg Settings, conversation string, g superviseSignal) bool {
	if cfg.Main != "" || conversation != cfg.BackgroundSupervisor || g.Todo == "" {
		return false
	}
	t, ok := s.todoNow(g.Todo)
	return ok && t.Origin == conversation
}

// restoredLegacyBackgroundNotice says whether a structured queue entry is itself evidence of an old background route.
// Only its explicit destination is immutable per-delivery evidence. Origin and Supervisor can move during handover,
// and a brief can be shared by a continuation, so neither can safely reconstruct authority for an ambiguous empty-To
// entry. Such an entry stays queued for main instead of inventing a background grant. Persisted positive grants and
// successful direct empty-To legacy deliveries retain their closeout permission.
func (s *Service) restoredLegacyBackgroundNotice(cfg Settings, conversation string, g superviseSignal) bool {
	return g.Todo != "" && !g.OriginOnly && g.To == conversation && !s.noMainOriginFallback(cfg, conversation, g)
}

// rememberLegacyBackground records the tasks whose old notices were delivered or queued for this background
// conversation. Only explicit legacy signals can route there now (digestTo), so this is also the closeout permission.
func (s *Service) rememberLegacyBackground(conversation string, list []superviseSignal) {
	keys := make([]string, 0, len(list))
	b := &s.supervise
	b.mu.Lock()
	b.init()
	for _, g := range list {
		if g.Todo != "" {
			key := legacyBackgroundKey(conversation, g.Todo)
			b.legacyBackground[key] = true
			keys = append(keys, key)
		}
	}
	b.mu.Unlock()
	if len(keys) == 0 {
		return
	}
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	if r.st.LegacyBackground == nil {
		r.st.LegacyBackground = map[string]bool{}
	}
	for _, key := range keys {
		r.st.LegacyBackground[key] = true
	}
	s.saveRelayLocked()
}

// followUpWords tells the caller who follows a newly handed-out task: the caller when it supervises the task itself
// (main, a direction lead, or a live side conversation), else the main conversation (#5732, #13657).
func (s *Service) followUpWords(ctx context.Context, self, supervisor string) string {
	if supervisor != "" && supervisor != self {
		return "the main conversation gets a supervision message (📋) when it needs review, and closes it with task_review"
	}
	return "you get a supervision message (📋) when it needs you, and close it with task_review"
}

// ensureBackground returns the already registered background conversation when it can still finish old work. Since
// #5732 it never starts a replacement: new supervision stays with main, and notices for a conversation that is gone
// move to main intact (backgroundLost). A legacy conversation whose rounds cannot start fresh is retired the same way:
// its unread work moves to main without archiving the conversation or creating another one (#7315).
func (s *Service) ensureBackground(ctx context.Context) (id, gone string) {
	b := &s.background
	b.mu.Lock()
	defer b.mu.Unlock()
	cfg := s.settings(ctx)
	if !s.backgroundOn(cfg) || cfg.Main == "" || cfg.MachineID == "" {
		return "", ""
	}
	id = cfg.BackgroundSupervisor
	if id == "" || id == cfg.Main {
		return "", ""
	}
	if cfg.BackgroundSupervisorMachineID == cfg.MachineID && s.backgroundThere(ctx, cfg, id) {
		if why := s.backgroundOutgrown(ctx, cfg, id); why != "" {
			s.logf("supervise: the legacy background supervisor %s cannot start another clean round (%s); its unread work moves to main", short(id), why)
			return "", id
		}
		return id, ""
	}
	return "", id
}

// backgroundLost takes back what was still queued for legacy background supervisor old, which is gone or cannot start
// another clean round. Known signals are regathered with any explicit target to old cleared, so they route to main rather
// than back to the retired conversation. When the signals are not known (the manager restarted since the message was
// queued), the message itself moves to conversation to. A hand-over of the owner's words follows its message; with no
// conversation to move to it is not delivered (relay_handover.go). Ordinary relays with no destination stay queued
// for a later reconciliation. A conversation reserved by backgroundOutgrown is released here: no replacement starts.
// dedupMu is held.
func (s *Service) backgroundLost(ctx context.Context, old, to, web string) []superviseSignal {
	if old == "" || old == to {
		return nil
	}
	b := &s.supervise
	b.mu.Lock()
	lost := b.queued[old]
	b.setQueuedLocked(old, nil)
	b.mu.Unlock()
	for i := range lost {
		if lost[i].To == old {
			lost[i].To = "" // regather for main; never route unread work back to the retired conversation
		}
	}
	moved := 0
	title := "" // the name of to, for a hand-over of the owner's words that moves there (relay_handover.go)
	if to != "" {
		title = short(to)
		if cfg := s.settings(ctx); cfg.MachineID != "" {
			if c, ok := s.session(ctx, cfg.MachineID, to); ok {
				title = conversationName(c, cfg.Main)
			}
		}
	}
	h := &s.handovers
	h.mu.Lock() // before the relay lock (handoverBook): the hand-overs change before the queue that moves them is saved
	r := &s.relays
	r.mu.Lock()
	keepOld := to == "" // without a destination, ordinary relays wait for a later reconciliation (#5732)
	if s.relayLocked(ctx) && len(r.st.Queue[old]) > 0 && !r.draining[old] {
		var handovers []string
		var retained []relayMsg
		for _, msg := range r.st.Queue[old] {
			if msg.Tool == superviseTool && len(lost) > 0 {
				if to == "" {
					retained = append(retained, msg)
				}
				continue // told again from its signals when a destination exists
			}
			if msg.Handover != "" {
				handovers = append(handovers, msg.Handover)
			}
			if to != "" {
				msg.OnceID = ""                                                     // another destination is a separate delivery
				msg.TriedAt, msg.Looks, msg.LookAfter = time.Time{}, 0, time.Time{} // the earlier try was to another conversation
				moved++
			} else if msg.Handover == "" {
				retained = append(retained, msg)
			}
		}
		s.handoversMovedLocked(handovers, to, title) // an owner-word hand-over with nowhere to go ends as not delivered
		if to != "" {
			for _, msg := range r.st.Queue[old] {
				if msg.Tool == superviseTool && len(lost) > 0 {
					continue
				}
				r.st.Queue[to] = append(r.st.Queue[to], msg)
			}
			delete(r.st.Queue, old)
		} else {
			keepOld = len(retained) > 0
			if keepOld {
				r.st.Queue[old] = retained
			} else {
				delete(r.st.Queue, old)
			}
		}
		s.saveRelayLocked()
	}
	if !keepOld {
		delete(r.replacing, old)
	}
	r.mu.Unlock()
	h.mu.Unlock()
	if moved > 0 {
		s.logf("supervise: %d message(s) queued for the background supervisor %s, which is gone, wait for %s now", moved, short(old), short(to))
		if cfg := s.settings(ctx); cfg.MachineID != "" {
			if c, ok := s.session(ctx, cfg.MachineID, to); ok {
				s.drainRelays(cfg.MachineID, []proto.Session{c})
			}
		}
		s.publish(ctx)
	}
	if len(lost) > 0 {
		s.logf("supervise: %d signal(s) queued for the background supervisor %s, which is gone, are told again", len(lost), short(old))
		s.forgetSupervision(ctx, lost, web)
	}
	return lost
}

// backgroundStranded, from the watch loop: a message still queued for a legacy background supervisor that is gone would
// wait for ever when no new signal comes for it. Its signals go back among the pending ones, and the next digest sends
// them to main; a message whose signals are not known (queued before a restart) waits for the main conversation instead.
func (s *Service) backgroundStranded(ctx context.Context) {
	cfg := s.settings(ctx)
	id := cfg.BackgroundSupervisor
	if id == "" || id == cfg.Main {
		return
	}
	b := &s.supervise
	b.mu.Lock()
	waiting := len(b.queued[id]) > 0
	b.mu.Unlock()
	if !waiting && s.queuedFor(id) == 0 || cfg.BackgroundSupervisorMachineID == cfg.MachineID && s.backgroundThere(ctx, cfg, id) {
		return
	}
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	lost := s.backgroundLost(ctx, id, cfg.Main, s.webBase(ctx))
	mode := s.supervisionMode(ctx)
	lost = slices.DeleteFunc(lost, func(g superviseSignal) bool { return !supervisionSignalAllowed(mode, g) })
	if len(lost) == 0 {
		return // what the mode drops (all of it while off) is not told again
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	for _, g := range lost {
		if b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
		if _, newer := b.pending[g.id()]; !newer {
			b.pending[g.id()] = g
		}
	}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for _, g := range lost {
			if cur, ok := b.pending[g.id()]; ok {
				pending[g.id()] = cur
			}
		}
	})
	s.retryDigestLocked(s.superviseTimings().urgent)
}

// backgroundThere: background supervisor id is still a conversation of the assistant's machine. One started within
// backgroundGrace is (its machine may not list it yet), and so is one whose machine cannot say now: what is sent to it
// queues until the machine is back. It is gone only when the machine is connected and lists its assistant
// conversations without it. A list with no assistant conversation at all is not that: the manager answers session.get
// and sessions.list from what the node last listed, which is empty from a manager's start until the node has
// connected and listed, and calling it gone then would move a live legacy conversation's work to main by mistake.
func (s *Service) backgroundThere(ctx context.Context, cfg Settings, id string) bool {
	if s.backgroundFresh(cfg, id) {
		return true
	}
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(ctx, cfg.MachineID, DeviceSessionGet, map[string]any{"session_id": id}, &res); err != nil {
		return true
	}
	if res.OK {
		return res.Session.Assistant && !runtimeSessionLacksSupervisionPolicy(res.Session) && !s.backgroundSignedOut(cfg, res.Session)
	}
	if d, ok := s.device(ctx, cfg.MachineID); !ok || !d.Online || !d.Reachable {
		return true
	}
	list, err := s.sessions(ctx, cfg.MachineID)
	if err != nil {
		return true
	}
	listed := false // the node listed since this manager started: an assistant conversation is in its list
	for _, c := range list {
		if c.SessionID == id {
			return c.Assistant && !runtimeSessionLacksSupervisionPolicy(c) && !s.backgroundSignedOut(cfg, c)
		}
		listed = listed || c.Assistant
	}
	return !listed
}

// backgroundSignedOut: background supervisor c, started before this manager, ended its last turn "Not logged in". It was
// made in a new private profile with no login, as every conversation the hub started was until #6015, and a conversation
// cannot move to another profile: it counts as gone, and its queued legacy work moves to main rather than starting a
// replacement (#5732). One this manager started is already in the signed-in profile (startConversation): treating it as
// gone would only move its work at every signal.
func (s *Service) backgroundSignedOut(cfg Settings, c proto.Session) bool {
	at := cfg.BackgroundSupervisorAt
	return signedOut(c) && c.SessionID == cfg.BackgroundSupervisor && at != nil && at.Before(s.born)
}

// backgroundFresh: conversation id is the background supervisor, started within backgroundGrace.
func (s *Service) backgroundFresh(cfg Settings, id string) bool {
	at := cfg.BackgroundSupervisorAt
	return id != "" && id == cfg.BackgroundSupervisor && at != nil && time.Since(*at) < backgroundGrace
}

// startBackground starts an explicitly requested background supervisor on the assistant's machine with what it is for
// (prompts/assistant/supervisor_start), under its own model purpose, and keeps its id in the settings. New task work
// remains main-supervised: ensureBackground does not call this to replace a lost legacy supervisor (#5732).
func (s *Service) startBackground(ctx context.Context) (string, error) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Second)
	defer cancel()
	sctx = context.WithValue(sctx, supervisionStartup{}, supervisionrole.RoleBackground())
	c, err := s.startConversation(sctx, startParams{Text: prompts.RenderSupervisorStart(prompts.SupervisorStart{Owner: s.owner(ctx)}),
		Purpose: purposeSupervise, Origin: OriginTask, Source: &proto.MessageSource{Kind: proto.SourceSupervision}})
	if err != nil {
		return "", err
	}
	s.holdPushes(sctx, c.SessionID)
	now := time.Now().UTC()
	s.availStartedBy(c.MachineID, c.SessionID, proto.SourceSupervision, now) // its first round, as every later one (availability.go)
	if _, err := s.updateSettings(sctx, func(v *Settings) error {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.BackgroundSupervisorAt = c.SessionID, c.MachineID, &now
		return nil
	}); err != nil {
		return "", err
	}
	s.titleSet(c.MachineID, c.SessionID, i18n.T(s.lang(ctx), "assistant-conversations.background_supervisor"), titleByAssistant)
	s.logf("supervise: started the background supervisor %s on %s", short(c.SessionID), c.MachineID)
	s.settingsChanged(sctx)
	return c.SessionID, nil
}

// backgroundNeeded: one of the signals list explicitly targets the configured legacy background, unread legacy work
// needs its outgrown state reconciled even when this digest is main-only, or a signal would have used the pre-#5732
// automatic background route (the probe). Only the first is possible for new work under #5732.
func (s *Service) backgroundNeeded(ctx context.Context, cfg Settings, list []superviseSignal) bool {
	const probe = "\x00background"
	legacy := cfg.BackgroundSupervisor
	if legacy != "" && legacy != cfg.Main {
		b := &s.supervise
		b.mu.Lock()
		waiting := len(b.queued[legacy]) > 0
		b.mu.Unlock()
		if waiting || s.queuedFor(legacy) > 0 {
			return true
		}
	}
	cfg.BackgroundSupervisor, cfg.BackgroundSupervisorMachineID = probe, cfg.MachineID
	for _, g := range list {
		if legacy != "" && legacy != cfg.Main && g.To == legacy {
			return true
		}
		if _, handled, _ := s.leaderRoute(g); !handled && s.digestTarget(ctx, cfg, g) == probe {
			return true
		}
	}
	return false
}

// backgroundModel puts what the background supervisor runs on (purposeSupervise) on params, a message to it: a change
// of that purpose's model applies from its next message, as the main chat's does. params are left as they are when
// the purpose cannot be resolved: the conversation then goes on with what it ran last.
func (s *Service) backgroundModel(ctx context.Context, params map[string]any) {
	p := maps.Clone(params)
	if err := preservePermissionMode(p, func() error { return s.applyModel(ctx, purposeSupervise, p) }); err != nil {
		s.logf("supervise: the background supervisor's model: %v; it keeps what it ran last", err)
		return
	}
	maps.Copy(params, p)
}

// backgroundAnnounceReceipt names the announcement among assistant.context's receipts (ContextResult.Receipts): the
// node sends the receipts back once the message carrying the context is delivered, and only then is it said.
const backgroundAnnounceReceipt = "background_supervisor"

// announceBackground: the main conversation is told that the background supervisor follows up its tasks (a default
// never changes without a word): the background supervisor's id, with every message until one carrying it was
// delivered (announcedBackground), "" before there is one and after. Asking marks nothing: a message that fails to go
// out, or goes without its context, leaves it for the next one.
func (s *Service) announceBackground(ctx context.Context) string {
	cfg := s.settings(ctx)
	id := s.ownSupervisor(cfg)
	if id == cfg.Main || cfg.BackgroundSupervisorAnnounced {
		return ""
	}
	return id
}

// announcedBackground takes the announcement's receipt out of ack, what the node confirms of a message delivered to
// conversation: from the main conversation, for the background supervisor there is now, it is said for good.
func (s *Service) announcedBackground(ctx context.Context, conversation string, ack []assistantwire.ViewErrorReceipt) []assistantwire.ViewErrorReceipt {
	rest := ack[:0:0]
	for _, r := range ack {
		if r.ItemID != backgroundAnnounceReceipt {
			rest = append(rest, r)
			continue
		}
		cfg := s.settings(ctx)
		if conversation != cfg.Main || r.Digest == "" || r.Digest != cfg.BackgroundSupervisor || cfg.BackgroundSupervisorAnnounced {
			continue
		}
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.BackgroundSupervisorAnnounced = true; return nil }); err != nil {
			s.logf("supervise: note the background supervisor's announcement: %v; it is said again", err)
		}
	}
	return rest
}
