package node

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func (c *codexConn) loadSpeedModels(ctx context.Context) {
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, err := readCodexModels(call, c.call)
	if err != nil {
		c.p.ownerOr(c.n).logf("assistant codex: model/list: %v", err)
		return
	}
	c.mu.Lock()
	c.models = models
	c.mu.Unlock()
}

func (c *codexConn) speedRow(s *proto.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.ServiceTier = c.serviceTier
	tiers := c.models[firstNonEmpty(c.speedModel, s.Model)].tiers
	s.ServiceTiers = codexSpeedChoices(tiers)
}

func (c *codexConn) setSpeed(ctx context.Context, tier string) error {
	c.mu.Lock()
	thread, revision := c.threadID, c.serviceTierRevision
	tiers := c.models[c.speedModel].tiers
	c.mu.Unlock()
	native := codexNativeSpeed(tiers, tier)
	if thread == "" || native == "" {
		return fmt.Errorf("this model does not advertise the requested Codex speed")
	}
	call, cancel := context.WithTimeout(ctx, codexEffortTimeout)
	defer cancel()
	if err := c.call(call, "thread/settings/update", map[string]any{"threadId": thread, "serviceTier": native}, nil); err != nil {
		return fmt.Errorf("couldn't change Codex speed: %w", err)
	}
	c.mu.Lock()
	if c.serviceTierRevision == revision {
		c.serviceTier = tier
		c.serviceTierRevision++
	}
	c.mu.Unlock()
	c.p.ownerOr(c.n).notifyCodex()
	return nil
}
