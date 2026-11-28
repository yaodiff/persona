package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
)

const (
	defaultIdleAutoCompactMinutes = assistantwire.DefaultAssistantIdleAutoCompactMinutes
	minIdleAutoCompactMinutes     = 1
	maxIdleAutoCompactMinutes     = 1440
	defaultIdleAutoCompactTokens  = assistantwire.DefaultAssistantIdleAutoCompactTokens
	minIdleAutoCompactTokens      = 10000
	maxIdleAutoCompactTokens      = 10000000

	// The main conversation's idle process is let go this long after its last activity (#12008). Unset keeps the
	// node's own 20 minutes; the range stays below the prompt cache's one-hour lifetime (assistantwire).
	defaultIdleReleaseMinutes = 20
	minIdleReleaseMinutes     = assistantwire.MinReleaseMinutes
	maxIdleReleaseMinutes     = assistantwire.MaxReleaseMinutes
)

func (s *Service) idleAutoCompact(ctx context.Context) bool {
	v := s.settings(ctx).IdleAutoCompact
	return v == nil || *v
}

func (s *Service) idleAutoCompactMinutes(ctx context.Context) int {
	v := s.settings(ctx).IdleAutoCompactMinutes
	if v == nil || *v < minIdleAutoCompactMinutes || *v > maxIdleAutoCompactMinutes {
		return defaultIdleAutoCompactMinutes
	}
	return *v
}

func (s *Service) idleAutoCompactTokens(ctx context.Context) int {
	v := s.settings(ctx).IdleAutoCompactTokens
	if v == nil || *v < minIdleAutoCompactTokens || *v > maxIdleAutoCompactTokens {
		return defaultIdleAutoCompactTokens
	}
	return *v
}

func (s *Service) idleReleaseMinutes(ctx context.Context) int {
	v := s.settings(ctx).IdleReleaseMinutes
	if v == nil || *v < minIdleReleaseMinutes || *v > maxIdleReleaseMinutes {
		return defaultIdleReleaseMinutes
	}
	return *v
}

// idleCompactionPolicy is what the main conversation's sends carry (idle_compaction). The release fields go only once
// saved, so a hub where neither was set keeps the node's own release rule: 20 minutes, and compaction first at 75% of
// the window. A saved idle_auto_compact_tokens is also the release-time threshold, whatever idle_auto_compact says:
// that switch turns off only the compaction before unattended messages, and compaction before a release ran before it.
func (s *Service) idleCompactionPolicy(ctx context.Context) assistantwire.IdleCompaction {
	cfg := s.settings(ctx)
	c := assistantwire.IdleCompaction{
		Enabled: s.idleAutoCompact(ctx), IdleMinutes: s.idleAutoCompactMinutes(ctx), ContextTokens: s.idleAutoCompactTokens(ctx),
	}
	if v := cfg.IdleReleaseMinutes; v != nil && assistantwire.ReleaseMinutesValid(*v) {
		c.ReleaseMinutes = *v
	}
	if v := cfg.IdleAutoCompactTokens; v != nil && *v >= minIdleAutoCompactTokens && *v <= maxIdleAutoCompactTokens {
		c.ReleaseTokens = *v
	}
	return c
}

func (s *Service) agyIdleAutoCompact(ctx context.Context) bool {
	v := s.settings(ctx).AgyIdleAutoCompact
	return v == nil || *v
}

func (s *Service) agyIdleAutoCompactMinutes(ctx context.Context) int {
	v := s.settings(ctx).AgyIdleAutoCompactMinutes
	if v == nil || !assistantwire.AgyIdleMinutesValid(*v) {
		return assistantwire.DefaultAgyIdleAutoCompactMinutes
	}
	return *v
}

func (s *Service) agyIdleAutoCompactTokens(ctx context.Context) int {
	v := s.settings(ctx).AgyIdleAutoCompactTokens
	if v == nil || !assistantwire.AgyIdleTokensValid(*v) {
		return assistantwire.DefaultAgyIdleAutoCompactTokens
	}
	return *v
}

// agyIdleCompactionPolicy is what node.configure carries (agy_idle_compaction). The agy_idle_auto_compact_* settings
// hold the task Agents to their thresholds; Antigravity assistant conversations (the main chat and the leaders' among
// them) follow the assistant's own idle_auto_compact_* settings, as Claude Code main chats do (#17260), and the agy
// switch still turns all of it off.
func (s *Service) agyIdleCompactionPolicy(ctx context.Context) assistantwire.AgyIdleCompaction {
	return assistantwire.AgyIdleCompaction{
		Enabled:                s.agyIdleAutoCompact(ctx),
		IdleMinutes:            s.agyIdleAutoCompactMinutes(ctx),
		ContextTokens:          s.agyIdleAutoCompactTokens(ctx),
		AssistantOff:           !s.idleAutoCompact(ctx),
		AssistantIdleMinutes:   s.idleAutoCompactMinutes(ctx),
		AssistantContextTokens: s.idleAutoCompactTokens(ctx),
	}
}

type agyNodeConfigPush struct {
	mu          sync.Mutex
	pushMu      sync.Mutex
	lastPolicy  *assistantwire.AgyIdleCompaction
	pendingByID map[string][]string
}

func (s *Service) pendingInteractionSessionsFor(machine string) []string {
	if machine == "" {
		return []string{}
	}
	s.fleet.mu.Lock()
	seen := map[string]struct{}{}
	for _, p := range s.fleet.interactions {
		if p == nil || p.claimed || p.ix.MachineID != machine || p.ix.SessionID == "" {
			continue
		}
		seen[p.ix.SessionID] = struct{}{}
	}
	s.fleet.mu.Unlock()
	out := make([]string, 0, len(seen))
	for sid := range seen {
		out = append(out, sid)
	}
	sort.Strings(out)
	return out
}

func (s *Service) agyNodeConfigureCall(ctx context.Context, machine string) (devicewire.NodeCall, bool) {
	policy := s.agyIdleCompactionPolicy(ctx)
	pending := s.pendingInteractionSessionsFor(machine)
	raw, err := json.Marshal(map[string]any{
		"agy_idle_compaction":          policy,
		"pending_interaction_sessions": pending,
	})
	if err != nil {
		return devicewire.NodeCall{}, false
	}
	s.agyNodePush.mu.Lock()
	if s.agyNodePush.pendingByID == nil {
		s.agyNodePush.pendingByID = map[string][]string{}
	}
	s.agyNodePush.pendingByID[machine] = slices.Clone(pending)
	s.agyNodePush.mu.Unlock()
	return devicewire.NodeCall{MachineID: machine, Method: "node.configure", Params: raw}, true
}

func (s *Service) syncAgyIdleCompactionToNodes(ctx context.Context) {
	d := s.devs
	if d == nil {
		return
	}
	policy := s.agyIdleCompactionPolicy(ctx)
	s.agyNodePush.mu.Lock()
	if s.agyNodePush.lastPolicy != nil && *s.agyNodePush.lastPolicy == policy {
		s.agyNodePush.mu.Unlock()
		return
	}
	pCopy := policy
	s.agyNodePush.lastPolicy = &pCopy
	s.agyNodePush.mu.Unlock()
	raw, err := json.Marshal(map[string]any{"agy_idle_compaction": policy})
	if err != nil {
		return
	}
	for _, m := range d.List() {
		if !m.Online || s.machinePending(ctx, m.ID) {
			continue
		}
		machineID := m.ID
		go func() {
			s.agyNodePush.pushMu.Lock()
			defer s.agyNodePush.pushMu.Unlock()
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := d.CallNode(cctx, machineID, "node.configure", raw); err != nil {
				s.logf("[%s] agy idle compaction configure: %v", machineID, err)
			}
		}()
	}
}

func (s *Service) syncPendingInteractionsToNode(machine string) {
	d := s.devs
	if d == nil || machine == "" || !d.Has(machine) {
		return
	}
	pending := s.pendingInteractionSessionsFor(machine)
	s.agyNodePush.mu.Lock()
	prev, seen := s.agyNodePush.pendingByID[machine]
	if seen && slices.Equal(prev, pending) {
		s.agyNodePush.mu.Unlock()
		return
	}
	if !seen && len(pending) == 0 {
		if s.agyNodePush.pendingByID == nil {
			s.agyNodePush.pendingByID = map[string][]string{}
		}
		s.agyNodePush.pendingByID[machine] = []string{}
		s.agyNodePush.mu.Unlock()
		return
	}
	if s.agyNodePush.pendingByID == nil {
		s.agyNodePush.pendingByID = map[string][]string{}
	}
	s.agyNodePush.pendingByID[machine] = slices.Clone(pending)
	s.agyNodePush.mu.Unlock()
	go func() {
		s.agyNodePush.pushMu.Lock()
		defer s.agyNodePush.pushMu.Unlock()
		latest := s.pendingInteractionSessionsFor(machine)
		raw, err := json.Marshal(map[string]any{"pending_interaction_sessions": latest})
		if err != nil {
			return
		}
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := d.CallNode(cctx, machine, "node.configure", raw); err != nil {
			s.logf("[%s] pending interactions configure: %v", machine, err)
		}
	}()
}
