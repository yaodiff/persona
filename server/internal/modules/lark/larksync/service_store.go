package larksync

import (
	"context"
	"sync"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// runStagingIndex is the index a run reads through: lookups see the store and what the run staged, and nothing is
// written until the service commits the run (CommitLarkSyncRun), together with its cursors and its outbox rows. So a
// run whose hand-over fails leaves the index as it was, and the next run reads the same content again.
type runStagingIndex struct {
	store  larksyncwire.Store
	mu     sync.Mutex
	staged map[string]larksyncwire.IndexRecord
	order  []string
}

// Lookup is the records of keys: the staged ones first, then the store's.
func (x *runStagingIndex) Lookup(ctx context.Context, keys []string) (map[string]larksyncwire.IndexRecord, error) {
	out := map[string]larksyncwire.IndexRecord{}
	var ask []string
	x.mu.Lock()
	for _, k := range keys {
		if r, ok := x.staged[k]; ok {
			out[k] = r
		} else {
			ask = append(ask, k)
		}
	}
	x.mu.Unlock()
	if len(ask) == 0 {
		return out, nil
	}
	got, err := x.store.LarkSyncIndex(ctx, ask)
	if err != nil {
		return nil, err
	}
	for k, r := range got {
		out[k] = r
	}
	return out, nil
}

// Put stages recs like Stage: a reader that does not know Stage still writes nothing before the commit.
func (x *runStagingIndex) Put(_ context.Context, recs []larksyncwire.IndexRecord) error {
	x.Stage(recs)
	return nil
}

// Stage keeps recs for the commit; a key staged twice keeps the later record (its sources merged).
func (x *runStagingIndex) Stage(recs []larksyncwire.IndexRecord) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.staged == nil {
		x.staged = map[string]larksyncwire.IndexRecord{}
	}
	for _, r := range recs {
		if r.Key == "" {
			continue
		}
		if have, ok := x.staged[r.Key]; ok {
			r.Sources = larksyncwire.MergeSources(have.Sources, r.Sources)
		} else {
			x.order = append(x.order, r.Key)
		}
		x.staged[r.Key] = r
	}
}

// records are the staged records in the order they were first staged.
func (x *runStagingIndex) records() []larksyncwire.IndexRecord {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]larksyncwire.IndexRecord, 0, len(x.order))
	for _, k := range x.order {
		out = append(out, x.staged[k])
	}
	return out
}
