package devices

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Re-splitting the last week by directory (docs/server/machines.md "Token usage by project", #16530): usage stored
// before a node metered by directory is "not split". Once, a node that does re-reads its transcripts from a week ago to
// where its live window starts, by the ten minutes, and the manager splits each stored ten minutes, model, launcher
// and agent in the re-read shares, keeping its sums (db.ResplitUsage): the calendar does not change, the projects fill
// in. What the transcripts no longer hold stays not split. Asked once per connection until done.

// A second pass (#16827): a node that names a temporary folder by its path (usage.features' temp_dirs) and says so in
// usage.resplit.status re-reads the week once more, and the manager splits what it stored under proto.UsageDirTemp
// among those folders (db.ResplitUsage from proto.UsageDirTemp), so a worktree made in /tmp moves to its repository's project. An older node
// is never asked: its re-read still says :temp.

// usageResplitStore keeps the re-split (Postgres).
type usageResplitStore interface {
	UsageResplit(ctx context.Context, machine, from string) (db.UsageResplitRecord, error)
	UsageNotSplit(ctx context.Context, machine, dir string, since, until time.Time) (bool, error)
	ResplitUsage(ctx context.Context, machine, from string, buckets []proto.UsageBucket, through time.Time, done bool) (int, error)
}

// usageResplitPasses are the re-splits by the pass a node names on the wire ("" the first, "temp" the temporary
// folders'): the directory each splits the stored rows of.
var usageResplitPasses = map[string]string{"": "", "temp": proto.UsageDirTemp}

// usageResplitRange is what a re-split may read at now: from a week ago (the ten-minute rows the projects' week is
// summed from) to where a node's whole window starts (usageNodeWindow: that the node sends split already).
func usageResplitRange(now time.Time) (since, before time.Time) {
	step := time.Duration(db.UsageResplitStep) * time.Second
	return now.Add(-7 * 24 * time.Hour).Truncate(step).Add(step), now.Add(-usageNodeWindow).Truncate(step)
}

// usageResplitStatus answers usage.resplit.status: done (nothing to split, or split already), else the range to read,
// from the ten minutes after the last one sent, and the pass ("pass": "temp" for the temporary folders' one, which a
// node that says temp_dirs is asked for once the first is done).
func (d *Devices) usageResplitStatus(ctx context.Context, machine string, params json.RawMessage, now time.Time) (map[string]any, error) {
	st, ok := d.opts.Store.(usageResplitStore)
	if !ok {
		return map[string]any{"done": true}, nil
	}
	var p struct {
		TempDirs bool `json:"temp_dirs"`
	}
	_ = json.Unmarshal(params, &p) // an older node's {} (or nothing): the first pass only
	passes := []string{""}
	if p.TempDirs {
		passes = append(passes, "temp")
	}
	for _, pass := range passes {
		from := usageResplitPasses[pass]
		rec, err := st.UsageResplit(ctx, machine, from)
		if err != nil {
			return map[string]any{"done": true}, err
		}
		if !rec.Done.IsZero() {
			continue
		}
		since, before := usageResplitRange(now)
		if !rec.Through.IsZero() && !rec.Through.Before(since) {
			since = rec.Through.Add(time.Duration(db.UsageResplitStep) * time.Second)
		}
		pending, err := st.UsageNotSplit(ctx, machine, from, since, before)
		if err != nil {
			return nil, err
		}
		if !pending || !since.Before(before) {
			if _, err := st.ResplitUsage(ctx, machine, from, nil, time.Time{}, true); err != nil {
				return map[string]any{"done": true}, err
			}
			continue
		}
		out := map[string]any{"done": false, "since": since.Unix(), "before": before.Unix()}
		if pass != "" {
			out["pass"] = pass
		}
		return out, nil
	}
	return map[string]any{"done": true}, nil
}

// putUsageResplit takes a usage.resplit request: its directories' resolutions, then its buckets (only those in the
// range a re-split may read now; in the temporary folders' pass only a directory's or :temp's) split into what is
// stored.
func (d *Devices) putUsageResplit(ctx context.Context, machine string, params json.RawMessage, now time.Time) (map[string]any, error) {
	var p struct {
		Buckets []proto.UsageBucket `json:"buckets"`
		Dirs    []proto.UsageDir    `json:"dirs"`
		Through int64               `json:"through"`
		Done    bool                `json:"done"`
		Pass    string              `json:"pass"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	from, known := usageResplitPasses[p.Pass]
	if !known {
		return nil, errors.New("no such re-split")
	}
	st, ok := d.opts.Store.(usageResplitStore)
	if !ok {
		return map[string]any{"moved": 0}, nil
	}
	if ds, ok := d.opts.Store.(usageDirStore); ok && len(p.Dirs) > 0 {
		if err := ds.PutUsageDirs(ctx, machine, usableDirs(p.Dirs)); err != nil {
			return nil, err
		}
	}
	since, before := usageResplitRange(now)
	keep := p.Buckets[:0]
	for _, b := range p.Buckets {
		if t := time.Unix(b.Minute, 0); t.Before(since) || !t.Before(before) || !usableDir(b.Dir) {
			continue
		}
		if from == proto.UsageDirTemp && b.Dir != proto.UsageDirTemp && strings.HasPrefix(b.Dir, ":") {
			continue // the assistant's, an unknown one: never a temporary folder's
		}
		keep = append(keep, b)
	}
	var through time.Time
	if p.Through > 0 {
		through = time.Unix(p.Through, 0)
	}
	moved, err := st.ResplitUsage(ctx, machine, from, keep, through, p.Done)
	if err != nil {
		return nil, err
	}
	switch {
	case p.Done && from == proto.UsageDirTemp:
		d.logf("[%s] usage re-split of temporary folders done", machine)
	case p.Done:
		d.logf("[%s] usage re-split by directory done", machine)
	}
	return map[string]any{"moved": moved}, nil
}
