package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nwverse/persona/server/internal/proto"
)

// The second manager acts after the first has read its startup inventory, at
// the point where pgx is about to send the stale-card update to Postgres.
type beforeStaleInteractionUpdate func(context.Context)

func (f beforeStaleInteractionUpdate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == cancelPendingSQL {
		f(ctx)
	}
	return ctx
}

func (beforeStaleInteractionUpdate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestInteractionStartupKeepsNewAndReclaimedCards(t *testing.T) {
	writer, ctx := open(t), t.Context()
	at := time.Now().UTC()
	put := func(id string, resumable bool) {
		t.Helper()
		ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "source", Kind: proto.IxQuestion,
			CreatedAt: at, ExpiresAt: at.Add(time.Hour), Resumable: resumable}
		if err := writer.InsertInteraction(ctx, ix); err != nil {
			t.Fatal(err)
		}
	}
	put("already-resumable", true)
	put("stale", false)
	put("reclaimed", false)
	put("answered", false)

	called := 0
	cfg := writer.pool.Config()
	cfg.ConnConfig.Tracer = beforeStaleInteractionUpdate(func(ctx context.Context) {
		called++
		put("new-review", true)
		put("new-question", false)
		if ok, err := writer.MarkInteractionResumable(ctx, "reclaimed"); err != nil || !ok {
			t.Fatalf("reclaim existing card: %t, %v", ok, err)
		}
		if err := writer.FinishInteraction(ctx, "answered", "dismissed", &proto.Resolution{Dismiss: true}); err != nil {
			t.Fatal(err)
		}
	})
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	restarting := &DB{pool: pool}
	kept, cancelled, err := restarting.ExpireStaleInteractions(ctx)
	if err != nil || called != 1 || len(kept) != 1 || kept[0].ID != "already-resumable" || len(cancelled) != 1 || cancelled[0].ID != "stale" {
		t.Fatalf("restart kept=%+v cancelled=%v concurrent updates=%d err=%v", kept, cancelled, called, err)
	}
	for _, id := range []string{"already-resumable", "reclaimed", "new-review", "new-question"} {
		status, outcome, err := writer.InteractionOutcome(ctx, id)
		if err != nil || status != "pending" || outcome != nil {
			t.Fatalf("%s outcome = %q, %+v, %v", id, status, outcome, err)
		}
	}
	if status, r, err := writer.InteractionOutcome(ctx, "answered"); err != nil || status != "dismissed" || r == nil || !r.Dismiss {
		t.Fatalf("owner's answer = %q, %+v, %v", status, r, err)
	}
}
