package assistant

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// transferPendingAsks moves supervisor-held cards and untried queued signals to the current assignment.
// Owner-held cards keep their state; in-doubt relays retain their identity and cannot be replayed to the old recipient.
// routingMu is held by the caller.
func (s *Service) transferPendingAsks(ctx context.Context, t Todo) error {
	b, r := &s.supervise, &s.relays
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	cfg := s.settings(ctx)
	target := t.Supervision.Supervisor
	normalize := func(g superviseSignal) (superviseSignal, bool) {
		if g.Todo != t.ID {
			return g, false
		}
		if g.Kind == sigAsk {
			next := s.currentSupervisorSignal(g)
			return next, next.To != g.To
		}
		if s.digestTo(ctx, cfg, g) == target {
			retargeted := g.To != target
			g.To = target
			if g.Kind == sigReview && g.Recipient != "" && g.Recipient != target {
				g.Recipient = target
				retargeted = true
			}
			g.batchTo, g.batchUntil = "", time.Time{}
			return g, retargeted
		}
		return g, false
	}
	// Release reservations before routes change, while old ask entries can still be reconstructed.
	r.mu.Lock()
	if !s.relayLocked(ctx) {
		r.mu.Unlock()
		return fmt.Errorf("supervisor saved; pending card queue unavailable: retry the transfer")
	}
	var release []superviseSignal
	for to, queue := range r.st.Queue {
		for _, msg := range queue {
			if msg.Tool != superviseTool || msg.Withdrawn {
				continue
			}
			signals, _ := decodeDigestSignals(msg.Signals)
			for _, g := range signals {
				if g.Todo != t.ID {
					continue
				}
				if !msg.TriedAt.IsZero() {
					continue
				}

				if to != target && (g.Kind == sigAsk || s.digestTo(ctx, cfg, g) == target) {
					release = append(release, g)
				}
			}
		}
	}
	r.mu.Unlock()
	if err := s.forgetSupervision(ctx, release, s.webBase(ctx)); err != nil {
		return err
	}
	previous := map[string]askRoute{}
	var fresh []superviseSignal
	now := time.Now()
	s.fleet.mu.Lock()
	for id, p := range s.fleet.interactions {
		route := p.route
		if p.claimed || p.told || route == nil || route.todo != t.ID || route.at() == "" || route.at() == target {
			continue
		}
		previous[id] = *route
		route.ladder, route.step = []string{target}, 0
		route.since, route.held = now, now
		route.delivered, route.reminded, route.parked = false, false, false
		fresh = append(fresh, superviseSignal{Todo: t.ID, Kind: sigAsk, To: target, Machine: p.ix.MachineID,
			Session: p.ix.SessionID, Key: fmt.Sprintf("%s@0", id), At: now})
	}
	s.fleet.mu.Unlock()
	b.mu.Lock()
	b.init()
	r.mu.Lock()
	locked := true
	defer func() {
		if locked {
			r.mu.Unlock()
			b.mu.Unlock()
		}
	}()
	state := r.st
	state.Queue = maps.Clone(state.Queue)
	state.SupervisePending = maps.Clone(state.SupervisePending)
	if state.SupervisePending == nil {
		state.SupervisePending = map[string]superviseSignal{}
	}
	var moved []superviseSignal
	var retargeted []superviseSignal
	retargetedReview := false
	for to, queue := range state.Queue {
		next := make([]relayMsg, 0, len(queue))
		for _, msg := range queue {
			if msg.Tool != superviseTool || !msg.TriedAt.IsZero() || msg.Withdrawn {
				next = append(next, msg)
				continue
			}
			signals, ok := decodeDigestSignals(msg.Signals)
			if !ok {
				next = append(next, msg)
				continue
			}
			var kept []superviseSignal
			for _, g := range signals {
				n, changed := normalize(g)
				if changed && n.Kind == sigReview {
					retargetedReview = true
				}
				if g.Todo == t.ID && n.To == target && to != target {
					moved = mergeSignals(moved, []superviseSignal{n})
				} else {
					kept = append(kept, n)
				}
			}
			if len(kept) > 0 {
				msg.Signals = encodeDigestSignals(kept)
				next = append(next, msg)
			}
		}
		if len(next) == 0 {
			delete(state.Queue, to)
		} else {
			state.Queue[to] = next
		}
	}
	// The pending outbox is authoritative over older queued stretches. One checkpoint retires the old
	// destination and admits the new one; an idle new supervisor need not wait for the old one's turn.
	pending := mergeSignals(moved, fresh)
	for _, g := range state.SupervisePending {
		n, changed := normalize(g)
		if changed {
			retargeted = append(retargeted, n)
			if n.Kind == sigReview {
				retargetedReview = true
			}
		}
		pending = mergeSignals(pending, []superviseSignal{n})
	}
	for _, g := range b.pending {
		n, changed := normalize(g)
		if changed {
			retargeted = append(retargeted, n)
			if n.Kind == sigReview {
				retargetedReview = true
			}
		}
		pending = mergeSignals(pending, []superviseSignal{n})
	}
	state.SupervisePending = map[string]superviseSignal{}
	for _, g := range pending {
		state.SupervisePending[g.id()] = g
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, relayKey, state); err != nil {
			s.fleet.mu.Lock()
			for id, route := range previous {
				if p := s.fleet.interactions[id]; p != nil && p.route != nil {
					*p.route = route
				}
			}
			s.fleet.mu.Unlock()
			return fmt.Errorf("supervisor saved; pending card checkpoint failed: retry the transfer: %w", err)
		}
	}
	r.st = state
	b.pending = maps.Clone(state.SupervisePending)
	for _, g := range moved {
		delete(b.sent, g.id())
	}
	for _, g := range retargeted {
		delete(b.sent, g.id())
	}
	for _, g := range fresh {
		delete(b.sent, g.id())
	}
	s.scheduleDigestLocked(now)
	locked = false
	r.mu.Unlock()
	b.mu.Unlock()
	if retargetedReview {
		s.retargetTurnSignalRecipient(ctx, t.ID, target)
	}
	return nil
}

// currentSupervisorSignal uses the same persisted ordinary recipient at admission, dispatch and queue recheck.
// Ask rungs and owner escalations retain their distinct state. It also repairs a supervisor-write/queue-checkpoint crash.
func (s *Service) currentSupervisorSignal(g superviseSignal) superviseSignal {
	if g.Kind != sigAsk {
		if t, ok := s.todoNow(g.Todo); ok && t.Supervision.assigned() && g.Kind != sigEscalated {
			to := t.Supervision.Supervisor
			if g.todoV2Wake() && s.agentSupervised(t) {
				to = "" // the worker signal goes through leaderRoute to the assigned Agent session
			}
			if g.To != to {
				g.batchTo, g.batchUntil = "", time.Time{}
			}
			g.To = to
		}
		return g
	}
	id, step, _ := strings.Cut(g.Key, "@")
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if p := s.fleet.interactions[id]; p != nil && !p.claimed && !p.told && p.route != nil &&
		p.route.todo == g.Todo && p.route.step == 0 && !p.route.delivered {
		g.To = p.route.at()
		g.batchTo, g.batchUntil = "", time.Time{}
	} else if s.fleet.interactions[id] == nil && step == "0" {
		if t, ok := s.todoNow(g.Todo); ok && t.Supervision != nil && t.Supervision.assigned() {
			g.To = t.Supervision.Supervisor
		}
	}
	return g
}

func (s *Service) askRelayDelivered(to string, msg relayMsg) {
	signals, _ := decodeDigestSignals(msg.Signals)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, g := range signals {
		if g.Kind != sigAsk {
			continue
		}
		for _, p := range s.fleet.interactions {
			if r := p.route; r != nil && r.todo == g.Todo && r.at() == to && (len(g.Cards) == 0 || slices.ContainsFunc(g.Cards, func(card prompts.SuperviseAsk) bool { return card.ID == p.ix.ID })) {
				r.delivered = true
			}
		}
	}
}

// A restored card already represented by a durable queued ask must not create a second notification while that
// queue is being retargeted. The structured ask is rendered from the current held cards at the last boundary.
func (s *Service) askAlreadyQueued(todo, to string) bool {
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	for _, queue := range r.st.Queue {
		for _, msg := range queue {
			if msg.Tool != superviseTool || !msg.TriedAt.IsZero() || msg.Withdrawn {
				continue
			}
			list, _ := decodeDigestSignals(msg.Signals)
			for _, g := range list {
				if g.Todo == todo && g.Kind == sigAsk && s.currentSupervisorSignal(g).To == to {
					return true
				}
			}
		}
	}
	return false
}
