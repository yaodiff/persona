package devices

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Token usage backfill (docs/server/machines.md "Backfill", #9484): once a machine and agent, its node reads the
// records from before the history began and sends the hours (node/usage_backfill.go). The manager says where to read
// (usage.backfill.status), stores the hours that have no usage of the agent yet and records how far it got
// (usage.backfill), a record per agent (#9984).

// usageBackfillStore keeps the backfill (db.UsageBackfill, db.PutUsageBackfill), a record per agent
// (db.UsageBackfillRecord).
type usageBackfillStore interface {
	UsageBackfill(ctx context.Context, machine, agent string) (db.UsageBackfill, error)
	PutUsageBackfill(ctx context.Context, machine string, agents []string, buckets []proto.UsageBucket, through time.Time, done bool) (int, error)
}

// The manager's store keeps the usage, its history and the backfill: a change of their methods must not quietly
// leave one of them out (they are found by type assertion).
var (
	_ usageStore         = (*db.DB)(nil)
	_ usageBatchStore    = (*db.DB)(nil)
	_ usageHistoryStore  = (*db.DB)(nil)
	_ usageBackfillStore = (*db.DB)(nil)
)

// usageBackfillStatus answers a node's usage.backfill.status: done, or the hours to read, from where the history
// begins (or the hour after the last one stored) to the start of the node's live window. A node that names the agents
// it reads ({"agents": [...]}) gets each one's too (agents): Claude Code's is the machine's first record, which the
// answer's own fields are (what an older node reads); every other agent has its own (db.UsageBackfillRecord).
func (d *Devices) usageBackfillStatus(ctx context.Context, machine string, params json.RawMessage, now time.Time) (map[string]any, error) {
	st, ok := d.opts.Store.(usageBackfillStore)
	if !ok { // nowhere to keep the history
		return map[string]any{"done": true}, nil
	}
	var p struct {
		Agents []string `json:"agents"`
	}
	_ = json.Unmarshal(params, &p) // an older node sends {}
	status := func(agent string) (map[string]any, error) {
		b, err := st.UsageBackfill(ctx, machine, agent)
		if err != nil {
			return nil, err
		}
		since, before := usageBackfillRange(b, now)
		if !b.Done.IsZero() || !since.Before(before) {
			return map[string]any{"done": true}, nil
		}
		return map[string]any{"done": false, "since": since.Unix(), "before": before.Unix()}, nil
	}
	out, err := status("")
	if err != nil || len(p.Agents) == 0 {
		return out, err
	}
	agents := map[string]any{}
	for _, a := range p.Agents {
		if agents[a], err = status(a); err != nil {
			return nil, err
		}
	}
	out["agents"] = agents
	return out, nil
}

// usageBackfillRange is what a backfill reads: from the history's first hour (or the one after the last stored) to
// the hour the node's live window starts in.
func usageBackfillRange(b db.UsageBackfill, now time.Time) (since, before time.Time) {
	since = now.Add(-usageHistoryKeep).Truncate(time.Hour).Add(time.Hour)
	if next := b.Through.Add(time.Hour); !b.Through.IsZero() && next.After(since) {
		since = next
	}
	return since, now.Add(-usageNodeWindow).Truncate(time.Hour)
}

// putUsageBackfill stores a node's usage.backfill request: its hours within the range (others are dropped), how far
// it got, and whether it is done.
func (d *Devices) putUsageBackfill(ctx context.Context, machine string, params json.RawMessage, now time.Time) (map[string]any, error) {
	var p struct {
		Buckets []proto.UsageBucket `json:"buckets"`
		Through int64               `json:"through"`
		Done    bool                `json:"done"`
		// Agents: what the request read through Through (an older node's: none, the legacy record); a bucket of
		// another agent is not stored.
		Agents []string `json:"agents"`
		// Dirs: what the node resolved the buckets' directories to (usage_projects.go), kept first.
		Dirs []proto.UsageDir `json:"dirs"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	st, ok := d.opts.Store.(usageBackfillStore)
	if !ok {
		return map[string]any{"stored": 0}, nil
	}
	if ds, ok := d.opts.Store.(usageDirStore); ok && len(p.Dirs) > 0 {
		if err := ds.PutUsageDirs(ctx, machine, usableDirs(p.Dirs)); err != nil {
			return nil, err
		}
	}
	since, before := usageBackfillRange(db.UsageBackfill{}, now)
	keep := p.Buckets[:0]
	for _, b := range p.Buckets {
		if t := time.Unix(b.Minute, 0); !t.Before(since) && t.Before(before) && (p.Agents == nil || slices.Contains(p.Agents, b.Agent)) {
			keep = append(keep, b)
		}
	}
	var through time.Time
	if p.Through > 0 {
		through = time.Unix(p.Through, 0)
	}
	stored, err := st.PutUsageBackfill(ctx, machine, p.Agents, keep, through, p.Done)
	if err != nil {
		return nil, err
	}
	if p.Done {
		d.logf("[%s] usage backfill done %v", machine, p.Agents)
	}
	return map[string]any{"stored": stored}, nil
}
