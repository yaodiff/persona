//go:build unix

package supervise

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// env.logf is called concurrently by supervised program goroutines; it must be race-free even when
// callers pass a standard non-thread-safe io.Writer such as *bytes.Buffer to Run().
func TestEnvLogfConcurrentSafeOnPlainBuffer(t *testing.T) {
	var buf bytes.Buffer
	e := &env{out: &buf}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 50 {
				e.logf("worker %d line %d", id, i)
			}
		}(g)
	}
	wg.Wait()
	if buf.Len() == 0 {
		t.Fatal("expected log output")
	}
	// Nil out writer must not panic.
	(&env{}).logf("ignored %d", 1)
}

// External caller deadline or custom cancel cause must not be misclassified as ErrStopped.
func TestSuperviseCallerDeadlineNotErrStopped(t *testing.T) {
	f := newFixture(t, `{"assistant":{"mode":"service"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := f.e.supervise(ctx, []*program{{
		name: "mgr",
		before: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}})
	if err != nil {
		t.Fatalf("supervise on caller deadline = %v, want nil (not ErrStopped)", err)
	}

	parentCtx, parentCancel := context.WithCancelCause(context.Background())
	parentCancel(errors.New("external shutdown signal"))
	err = f.e.supervise(parentCtx, []*program{{name: "mgr"}})
	if err != nil {
		t.Fatalf("supervise on caller custom cancel cause = %v, want nil (not ErrStopped)", err)
	}
}

// waitHealthy must honor its `most` budget even when the server hangs without responding,
// and must not follow HTTP redirects to other endpoints.
func TestWaitHealthyBoundsHungRequestAndDoesNotFollowRedirect(t *testing.T) {
	// Part A: hung server must not stall waitHealthy for the 5s client timeout when most is 60ms.
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hung.Close()

	start := time.Now()
	err := waitHealthy(context.Background(), hung.URL, 60*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("waitHealthy on hung server returned nil")
	}
	if elapsed > time.Second {
		t.Fatalf("waitHealthy took %s for 60ms budget (exceeded budget on hung request)", elapsed)
	}

	// Part B: 302 redirect to a 200 OK target must not be followed.
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/v1/health", http.StatusFound)
	}))
	defer redir.Close()

	if err := waitHealthy(context.Background(), redir.URL, 60*time.Millisecond); err == nil {
		t.Fatal("waitHealthy followed redirect and returned nil")
	}
	if n := targetHits.Load(); n != 0 {
		t.Fatalf("waitHealthy followed redirect to target %d times, want 0", n)
	}
}

// If bindBuiltin invokes the mint callback more than once, every un-traded code must be voided.
func TestBindBuiltinVoidsAllUntradedMintedCodes(t *testing.T) {
	defer func(m func(context.Context) (string, error), v func(context.Context, string) error,
		b func(context.Context, string, string, func(context.Context) (string, error)) (bool, error)) {
		mintCode, voidCode, bindBuiltin = m, v, b
	}(mintCode, voidCode, bindBuiltin)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := &env{home: t.TempDir(), out: &bytes.Buffer{}}
	var seq int
	mintCode = func(context.Context) (string, error) {
		seq++
		return "mi_c" + string(rune('0'+seq)), nil
	}
	var voided []string
	voidCode = func(_ context.Context, c string) error {
		voided = append(voided, c)
		return nil
	}

	// Case 1: two codes minted, neither traded -> both voided.
	bindBuiltin = func(ctx context.Context, _, _ string, code func(context.Context) (string, error)) (bool, error) {
		_, _ = code(ctx)
		_, _ = code(ctx)
		return false, errors.New("trade failed")
	}
	if err := e.bindBuiltin(context.Background(), srv.URL); err == nil {
		t.Fatal("expected bindBuiltin error")
	}
	if !slices.Equal(voided, []string{"mi_c1", "mi_c2"}) {
		t.Fatalf("voided on failure = %v, want [mi_c1 mi_c2]", voided)
	}

	// Case 2: two codes minted, only second traded -> first still voided, second kept.
	voided = nil
	bindBuiltin = func(ctx context.Context, _, _ string, code func(context.Context) (string, error)) (bool, error) {
		_, _ = code(ctx)
		_, _ = code(ctx)
		return true, nil
	}
	if err := e.bindBuiltin(context.Background(), srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(voided, []string{"mi_c3"}) {
		t.Fatalf("voided when second traded = %v, want [mi_c3]", voided)
	}
}
