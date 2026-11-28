package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// stuckHistoryStore is a database whose interaction history never answers: every read and write waits for its
// context.
type stuckHistoryStore struct{ *fleetMem }

func (stuckHistoryStore) InsertInteraction(ctx context.Context, _ proto.Interaction) error {
	<-ctx.Done()
	return ctx.Err()
}

func (stuckHistoryStore) FinishInteraction(ctx context.Context, _, _ string, _ *proto.Resolution) error {
	<-ctx.Done()
	return ctx.Err()
}

func (stuckHistoryStore) InteractionOutcome(ctx context.Context, _ string) (string, *proto.Resolution, error) {
	<-ctx.Done()
	return "", nil, ctx.Err()
}

// stuckAlwaysStore is a database whose assistant_always table never answers.
type stuckAlwaysStore struct{ *fleetMem }

func (stuckAlwaysStore) AllowAlways(ctx context.Context, _, _ string, _ []string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (stuckAlwaysStore) AlwaysAllowed(ctx context.Context, _ string, _ []string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func shortHistoryTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := historyTimeout
	historyTimeout = d
	t.Cleanup(func() { historyTimeout = old })
}

// newFleetTestOn is newFleetTest on the store st.
func newFleetTestOn(t *testing.T, st Store) *fleetTest {
	t.Helper()
	f := newFakeHub()
	d := newFleetDevs()
	d.add("mac", "Mac")
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: t.Logf, Devices: d})
	runCtx, cancel := context.WithCancel(context.Background())
	s.running.Store(&runCtx)
	ft := &fleetTest{t: t, s: s, d: d, f: f, cancel: cancel}
	t.Cleanup(func() { drainFleetBackground(t, ft.s, cancel) })
	return ft
}

// waitShown waits for the apps to be told about interaction id (through the hub).
func (ft *fleetTest) waitShown(id string, within time.Duration) {
	ft.t.Helper()
	deadline := time.Now().Add(within)
	for {
		for _, b := range ft.toldOf(assistantwire.EventFleetInteraction) {
			var p assistantwire.FleetInteraction
			if json.Unmarshal(b, &p) == nil && p.Interaction.ID == id {
				return
			}
		}
		if time.Now().After(deadline) {
			ft.t.Fatalf("the apps were not shown %s within %v", id, within)
		}
		time.Sleep(time.Millisecond)
	}
}

func permission(id string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true,
		ExpiresAt: time.Now().Add(time.Hour)}
}

// A database that does not answer holds a new card and its answer back by historyTimeout at most, not until it does.
func TestInteractionHistoryStuckDatabase(t *testing.T) {
	shortHistoryTimeout(t, 100*time.Millisecond)
	ft := newFleetTestOn(t, stuckHistoryStore{newFleetMem()})
	s := ft.s

	start := time.Now()
	got := make(chan proto.Resolution, 1)
	go func() { r, _ := s.waitInteraction(context.Background(), permission("ix_stuck")); got <- r }()
	ft.waitShown("ix_stuck", 2*time.Second)
	if d := time.Since(start); d < historyTimeout {
		t.Fatalf("card shown after %v, before the history write gave up", d) // the order stays: history first
	}

	yes := true
	start = time.Now()
	resolved := make(chan error, 1)
	go func() { resolved <- s.resolve("ix_stuck", proto.Resolution{Allow: &yes}) }()
	select {
	case r := <-got:
		if r.Allow == nil || !*r.Allow {
			t.Fatalf("hook got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hook never got the answer")
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	t.Logf("answer delivered after %v", time.Since(start))
}

// "Always allow in this session" on a conversation is kept before the hook hears the answer: a database that does not answer holds it
// back by historyTimeout at most, and the hook still gets the answer.
func TestKeepAlwaysStuckDatabase(t *testing.T) {
	shortHistoryTimeout(t, 100*time.Millisecond)
	ft := newFleetTestOn(t, stuckAlwaysStore{newFleetMem()})
	s := ft.s

	got := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.waitInteraction(context.Background(), alwaysIx("ix_always", "conv", "tool:Bash"))
		got <- r
	}()
	ft.waitShown("ix_always", 2*time.Second)

	yes := true
	start := time.Now()
	resolved := make(chan error, 1)
	go func() { resolved <- s.resolve("ix_always", proto.Resolution{Allow: &yes, Always: true}) }()
	select {
	case r := <-got:
		if r.Allow == nil || !*r.Allow || !r.Always {
			t.Fatalf("hook got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hook never got the 本会话总是允许 answer")
	}
	if d := time.Since(start); d < historyTimeout {
		t.Fatalf("answer delivered after %v, before the grant write gave up", d) // the order stays: grant first
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	t.Logf("answer delivered after %v", time.Since(start))
}

// A resolve for a card that is over answers within historyTimeout, and at once when the request is gone.
func TestWriteEndedStuckDatabase(t *testing.T) {
	shortHistoryTimeout(t, 100*time.Millisecond)
	s := newFleetTestOn(t, stuckHistoryStore{newFleetMem()}).s

	start := time.Now()
	w := httptest.NewRecorder()
	s.writeEnded(context.Background(), w, "ix_gone", proto.Resolution{}, errNotPending, false)
	if w.Code != http.StatusGone || time.Since(start) > 2*time.Second {
		t.Fatalf("status %d after %v", w.Code, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	w = httptest.NewRecorder()
	s.writeEnded(ctx, w, "ix_gone", proto.Resolution{}, errNotPending, false)
	if w.Code != http.StatusGone || time.Since(start) >= historyTimeout {
		t.Fatalf("status %d after %v with the request gone", w.Code, time.Since(start))
	}
}

// Against Postgres: another session holds the interaction's row locked; the answer still reaches the hook.
func TestInteractionResolveRowLockedPostgres(t *testing.T) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	database, err := db.Open(t.Context(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	ft := newFleetTestOn(t, database)
	s := ft.s

	// The normal history timeout also applies to the insert. Let it complete
	// before locking the row, and keep the row locked until both calls return.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ix := permission("ix_lock_" + randomHex(6))
	ready, done := make(chan error, 1), make(chan struct{})
	var got proto.Resolution
	var waitErr error
	go func() {
		defer close(done)
		got, waitErr = s.waitInteractionReadyResult(ctx, ix, nil, ready)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("interaction registration failed: %v", err)
		}
	case <-done:
		t.Fatalf("interaction ended before registration: %+v, %v", got, waitErr)
	case <-ctx.Done():
		t.Fatal("the interaction was not registered")
	}

	locker, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close(context.Background()) })
	tx, err := locker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// On failure the deferred rollback releases the lock before the cleanup
	// joins the resolver, while the store is still open.
	defer tx.Rollback(context.Background())
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM interactions WHERE id = $1 FOR UPDATE`, ix.ID).Scan(&one); err != nil {
		t.Fatalf("lock the row: %v", err)
	}

	yes := true
	start := time.Now()
	resolved := make(chan struct{})
	var resolveErr error
	go func() {
		defer close(resolved)
		resolveErr = s.resolve(ix.ID, proto.Resolution{Allow: &yes})
	}()
	t.Cleanup(func() { <-resolved })
	select {
	case <-done:
		if waitErr != nil || got.Allow == nil || !*got.Allow {
			t.Fatalf("hook got %+v, %v", got, waitErr)
		}
	case <-ctx.Done():
		t.Fatal("the hook never got the answer while the row was locked")
	}
	select {
	case <-resolved:
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
	case <-ctx.Done():
		t.Fatal("resolve did not return while the row was locked")
	}
	t.Logf("answer delivered after %v with the row locked", time.Since(start))
}
