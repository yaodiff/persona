package larksync

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

func namesWorld(t *testing.T) (w *serviceWorld, key string) {
	t.Helper()
	w = newServiceWorld(t)
	ctx := context.Background()
	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierFull)
	key, acc := w.adopt(t, "bsync", "ou_owner", "acc-1", "ref-1", 24*time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		b := st.binding(key)
		b.Tier = larksyncwire.TierFull
		b.BackfillVersion = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at := w.now().Add(-time.Hour)
	raw, err := json.Marshal(connectorwire.ChatBatch{
		Chat: kbwire.SourceChat{ID: "oc_big", Name: "Big Group", Kind: "group"},
		Messages: []kbwire.SourceMessage{
			{ID: "om_a", SenderID: "ou_a", Text: "one", At: at},
			{ID: "om_b", SenderID: "ou_b", Text: "two", At: at.Add(time.Second)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.CommitLarkSyncRun(ctx, key, nil, []larksyncwire.IndexRecord{
		{Key: larksyncwire.KeyMessage + "om_a", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: key, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_b", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: key, Entry: larksyncwire.EntryChat}}},
	}, []larksyncwire.OutboxRow{{Kind: larksyncwire.OutboxChat, CanonicalKey: "chat:oc_big", Payload: raw}}); err != nil {
		t.Fatal(err)
	}
	w.script("bsync", acc, func(context.Context, larksyncwire.Binding, func(context.Context) (string, error), map[string]string, larksyncwire.Index) (larksyncwire.Result, error) {
		return larksyncwire.Result{}, nil
	})
	return w, key
}

func (w *serviceWorld) onResolve(fn func(larksyncwire.Binding, larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resolveNamesWithFallback = func(_ context.Context, b larksyncwire.Binding, _ func(context.Context) (string, error), req larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
		return fn(b, req)
	}
}

func TestLarkSyncSenderNamesTimeoutDegradesHealthOnly(t *testing.T) {
	for name, resolveErr := range map[string]error{
		"deadline":     context.DeadlineExceeded,
		"timeout":      &larksyncwire.Error{Kind: larksyncwire.ErrKindOther, Op: "chat members", Msg: "i/o timeout"},
		"rate limited": &larksyncwire.Error{Kind: larksyncwire.ErrKindRateLimited, Op: "chat members", Msg: "too many requests"},
		"transient":    &larksyncwire.Error{Kind: larksyncwire.ErrKindTransient, Op: "chat members", Msg: "HTTP 502"},
	} {
		t.Run(name, func(t *testing.T) {
			w, key := namesWorld(t)
			w.onResolve(func(larksyncwire.Binding, larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
				return larksyncwire.NameResolveResult{UserNames: map[string]string{"ou_a": "Alpha"}, Progress: `{"scans":{}}`}, resolveErr
			})
			if err := w.svc.runMode(context.Background(), key, larksyncwire.ModeIM); err != nil {
				t.Fatalf("the run failed on a names error: %v", err)
			}
			b := w.binding(t, key)
			if b.Health != larksyncwire.HealthOK || b.LastError != "" || b.Failures != 0 {
				t.Fatalf("health = %q, error = %q, failures = %d; want ok, none, 0", b.Health, b.LastError, b.Failures)
			}
			if b.NamesWarning == "" || b.NamesRounds != 1 || b.BackfillVersion != 2 {
				t.Fatalf("warning = %q, rounds = %d, backfill = %d; want a warning, 1, still v2", b.NamesWarning, b.NamesRounds, b.BackfillVersion)
			}
			if !b.NextIMRunAt.Equal(w.now().Add(imEvery)) {
				t.Errorf("next IM run = %v, want the regular cadence", b.NextIMRunAt.Sub(w.now()))
			}
			cur, _ := w.store.LarkSyncCursors(context.Background(), key)
			if cur[larksyncwire.SenderNamesProgressCursor] != `{"scans":{}}` {
				t.Errorf("progress = %q, want it kept even when the round erred", cur[larksyncwire.SenderNamesProgressCursor])
			}
		})
	}
}

func TestLarkSyncSenderNamesScopeStillShows(t *testing.T) {
	w, key := namesWorld(t)
	w.onResolve(func(larksyncwire.Binding, larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
		return larksyncwire.NameResolveResult{}, &larksyncwire.Error{Kind: larksyncwire.ErrKindScope, Op: "user name", Msg: "no permission"}
	})
	if err := w.svc.runMode(context.Background(), key, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if b := w.binding(t, key); b.Health != larksyncwire.HealthScopeMissing || b.NamesWarning != "" {
		t.Fatalf("health = %q, warning = %q; want scope_missing and no warning", b.Health, b.NamesWarning)
	}
}

func TestLarkSyncSenderNamesResumeAcrossRuns(t *testing.T) {
	w, key := namesWorld(t)
	ctx := context.Background()
	var reqs []larksyncwire.NameResolveRequest
	var budgets []time.Duration
	w.onResolve(func(b larksyncwire.Binding, req larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
		reqs = append(reqs, req)
		budgets = append(budgets, b.Budget)
		if len(reqs) == 1 {
			return larksyncwire.NameResolveResult{
				UserNames: map[string]string{"ou_a": "Alpha"},
				Partial:   true,
				Progress:  `{"scans":{"oc_big":{"next":"p150","pages":150,"at":1}}}`,
				NameBook:  `{"ou_a":{"n":"Alpha","t":1}}`,
			}, nil
		}
		return larksyncwire.NameResolveResult{UserNames: map[string]string{"ou_b": "Beta"}}, nil
	})

	if err := w.svc.runMode(ctx, key, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	b := w.binding(t, key)
	if b.Health != larksyncwire.HealthOK || b.BackfillVersion != 2 || b.NamesRounds != 1 || b.NamesWarning != "" {
		t.Fatalf("after round 1: health %q, backfill %d, rounds %d, warning %q", b.Health, b.BackfillVersion, b.NamesRounds, b.NamesWarning)
	}
	cur, _ := w.store.LarkSyncCursors(ctx, key)
	if cur[larksyncwire.SenderNamesProgressCursor] == "" || cur[larksyncwire.SenderNamesCursor] != `{"ou_a":{"n":"Alpha","t":1}}` {
		t.Fatalf("cursors after round 1 = %v", cur)
	}

	if err := w.svc.runMode(ctx, key, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("rounds = %d, want 2", len(reqs))
	}
	if reqs[1].Progress != `{"scans":{"oc_big":{"next":"p150","pages":150,"at":1}}}` || reqs[1].NameBook != `{"ou_a":{"n":"Alpha","t":1}}` {
		t.Fatalf("round 2 was handed progress %q, book %q", reqs[1].Progress, reqs[1].NameBook)
	}
	if !slices.Equal(reqs[1].ChatSenders["oc_big"], []string{"ou_b"}) {
		t.Fatalf("round 2 asked for %v, want only the sender still unnamed", reqs[1].ChatSenders)
	}
	for i, d := range budgets {
		if d != namesBudget {
			t.Errorf("round %d budget = %v, want its own %v", i+1, d, namesBudget)
		}
	}
	b = w.binding(t, key)
	if b.BackfillVersion != backfillVersion || b.NamesRounds != 0 || b.Health != larksyncwire.HealthOK {
		t.Fatalf("after round 2: backfill %d, rounds %d, health %q", b.BackfillVersion, b.NamesRounds, b.Health)
	}

	if err := w.svc.runMode(ctx, key, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("a completed backfill ran again (%d rounds)", len(reqs))
	}
}
