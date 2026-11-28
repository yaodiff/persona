package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// nonOwnerCardTarget resolves who handles a non-six-class card instead of pushing it to the owner:
// the task's supervisor when the session belongs to an open supervised to-do, otherwise the main
// conversation (or the background supervisor when the main conversation itself raised the card).
// An empty target means no conversation is configured. Standalone means the source session has no open to-do.
func (s *Service) nonOwnerCardTarget(ctx context.Context, machine, sessionID string) (target string, standalone bool) {
	cfg := s.settings(ctx)
	if machine != "" && sessionID != "" && s.ensureTodosLoaded(ctx) == nil {
		t, found := s.todoOfSession(ctx, s.canonical(machine), sessionID)
		standalone = !found
		if found && t.Supervision != nil {
			switch t.Supervision.state(t) {
			case reviewAccepted, reviewCancelled:
			default:
				if sup := s.effectiveSupervisor(ctx, cfg, t); sup != "" && sup != sessionID {
					return sup, false
				}
			}
		}
	}
	if cfg.Main != "" && sessionID == cfg.Main && cfg.BackgroundSupervisor != "" && cfg.BackgroundSupervisor != cfg.Main {
		return cfg.BackgroundSupervisor, false
	}
	return cfg.Main, standalone
}

// scheduleFailureTarget resolves who handles a failed scheduled run instead of pushing it to the
// owner: the supervisor of the task's originating session (if any), the originating leader/main
// conversation itself, or the main conversation. Returns ("", false) when none is configured.
func (s *Service) scheduleFailureTarget(ctx context.Context, t Task) (string, bool) {
	cfg := s.settings(ctx)
	if t.SessionID != "" {
		machine := cmp.Or(t.MachineID, cfg.MachineID)
		if machine != "" {
			canon := machine
			if s.devs != nil {
				canon = s.devs.Canonical(machine)
			}
			if td, ok := s.todoOfSession(ctx, canon, t.SessionID); ok && td.Supervision != nil {
				switch td.Supervision.state(td) {
				case reviewAccepted, reviewCancelled:
				default:
					if sup := s.effectiveSupervisor(ctx, cfg, td); sup != "" {
						return sup, true
					}
				}
			}
		}
		if t.SessionID == cfg.Main || slices.Contains(cfg.Leaders, t.SessionID) {
			return t.SessionID, true
		}
	}
	if cfg.Main != "" {
		return cfg.Main, true
	}
	return "", false
}

// handNonOwnerCard gives an internal decision an accountable reviewer and active,
// durable delivery. The pending interaction owns its answer and expiry; stale
// relays cannot authorize another occurrence. Owner-only cards stay out of it.
func (s *Service) handNonOwnerCard(ix proto.Interaction) bool {
	// Preserve the existing private standalone permission's App-only boundary:
	// it is neither supervisor-readable nor an alert on another surface.
	if ix.Private && !secretOrForm(ix) && ix.Kind == proto.IxPermission && s.inputCardSource(ix, false) == cardAgentPermission {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, standalone := s.nonOwnerCardTarget(ctx, ix.MachineID, ix.SessionID); standalone && !s.settings(ctx).supervisorConversation(ix.SessionID) {
			s.fleet.mu.Lock()
			p := s.fleet.interactions[ix.ID]
			if p == nil || p.claimed || !p.ix.CreatedAt.Equal(ix.CreatedAt) {
				s.fleet.mu.Unlock()
				return true
			}
			p.route, p.ix.Delegating = nil, false
			shown := p.ix
			s.fleet.mu.Unlock()
			s.show(shown)
			return true
		}
	}
	if !s.internalCard(ix) {
		return false
	}
	src := s.inputCardSource(ix, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg := s.settings(ctx)
	// Keep the explicit bot write-approval route; supervising read-only approvals
	// use the same accountable internal route as every other internal source.
	if src == cardAgentPermission && !cfg.supervisorConversation(ix.SessionID) && ix.Kind == proto.IxPermission && s.nonMainAssistantPermission(ctx, ix) && s.larkModule().AskWanted(ctx, ix) {
		return false
	}
	to, home := s.internalReviewer(ctx, ix)
	if to == "" || home == "" {
		// Legacy node questions outside the five manager-owned sources keep
		// their manual-session fallback when Assistant has not been configured.
		if src == cardAgentQuestion && ix.ToolName != "ask_user" {
			return false
		}
		s.workEvent(ix.MachineID, ix.SessionID, "ask_internal_unavailable", map[string]any{"id": ix.ID, "source": src})
		_ = s.resolveRemembered(ix.ID, proto.Resolution{Dismiss: true, Reason: "internal_reviewer_unavailable"}, false)
		return true
	}
	_, standalone := s.nonOwnerCardTarget(ctx, ix.MachineID, ix.SessionID)
	todo := ""
	if !cfg.supervisorConversation(ix.SessionID) {
		if t, found := s.taskOf(ctx, s.canonical(ix.MachineID), ix.SessionID); found && t.Supervision != nil {
			if state := t.Supervision.state(t); state != reviewAccepted && state != reviewCancelled {
				todo = t.ID
			}
		}
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	if p == nil || p.claimed || p.told || !p.ix.CreatedAt.Equal(ix.CreatedAt) {
		s.fleet.mu.Unlock()
		return true // a stale internal announcement must never fall through to an owner push
	}
	if p.route != nil {
		s.fleet.mu.Unlock()
		return true
	}
	now := time.Now()
	until := ix.ExpiresAt
	if standalone && src == cardAgentPermission && !cfg.supervisorConversation(ix.SessionID) {
		remaining := ix.ExpiresAt.Sub(now)
		if remaining <= 2*superviseTick {
			p.ix.Delegating = false
			shown := p.ix
			s.fleet.mu.Unlock()
			s.show(shown)
			return true
		}
		until = now.Add(min(askHoldFor, remaining/2))
	}
	p.route = &askRoute{
		todo:            todo,
		machine:         home,
		ladder:          []string{to},
		routedAt:        now,
		since:           now,
		held:            now,
		standaloneUntil: until,
	}
	p.ix.Delegating = true
	shown := p.ix
	s.fleet.mu.Unlock()
	s.show(shown)

	var b strings.Builder
	fmt.Fprintf(&b, "Non-owner card (%s) routed to you instead of pushing to the user.\n", src)
	fmt.Fprintf(&b, "interaction_id=%s kind=%s", ix.ID, ix.Kind)
	if ix.MachineID != "" {
		fmt.Fprintf(&b, " machine=%s", ix.MachineID)
	}
	if ix.SessionID != "" {
		fmt.Fprintf(&b, " session_id=%s", ix.SessionID)
	}
	if ix.ToolName != "" {
		fmt.Fprintf(&b, " tool=%s", ix.ToolName)
	}
	if ask := clip(oneLine(noticeAsk(ix)), 300); ask != "" {
		fmt.Fprintf(&b, "\n%s", ask)
	}
	msg := relayMsg{
		From:      src,
		FromTitle: cmp.Or(ix.SessionTitle, ix.ToolName, src),
		Tool:      src,
		Text:      b.String(),
		At:        now.UTC(),
		Internal:  true,
	}
	msg.Card = ix.ID
	if _, err := s.queueRelayDurable(to, msg); err == nil {
		s.drainAfter(home, to, 0)
		s.publish(ctx)
	} else {
		s.workEvent(ix.MachineID, ix.SessionID, "ask_internal_delivery_failed", map[string]any{"id": ix.ID, "to": to, "reason": "queue_not_durable"})
		_ = s.resolveRemembered(ix.ID, proto.Resolution{Dismiss: true, Reason: "internal_queue_unavailable"}, false)
	}
	s.workEvent(ix.MachineID, ix.SessionID, "ask_routed_nonowner", map[string]any{
		"to":     to,
		"source": src,
		"kind":   ix.Kind,
		"id":     ix.ID,
	})
	return true
}

var errStandalonePermissionStale = errors.New("the main conversation no longer holds this permission")

func (msg relayMsg) standalonePermission() bool {
	return msg.Card != "" && (msg.Tool == cardAgentPermission || msg.Tool == cardAgentQuestion || msg.Tool == cardExtensionApproval || msg.Tool == cardToolPermission || msg.Tool == cardAskUser || msg.Tool == cardConfirmOutbound)
}

// pendingStandalonePermission binds a relay to the same card and route that queued it.
func (s *Service) pendingStandalonePermission(to string, msg relayMsg, now time.Time) (proto.Interaction, bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[msg.Card]
	if p == nil || p.claimed || p.route == nil || p.route.at() != to || !p.route.routedAt.Equal(msg.At) ||
		!now.Before(p.route.standaloneUntil) || !now.Before(p.ix.ExpiresAt) {
		return proto.Interaction{}, false
	}
	return p.ix, true
}
