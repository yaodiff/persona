package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func runConfig(t *testing.T) Config {
	cfg := Config{ViaMachine: "node", RunLauncher: "cli", Model: t.Name()}
	t.Cleanup(func() { gates.Delete(runKey(cfg)) })
	return cfg
}

// Each call resolves a fresh client, as independent purposes on one launcher do.
func runResult(ctx context.Context, cfg Config, result error) error {
	c := NewRun(cfg, func(context.Context, Request) (Reply, error) { return Reply{}, result })
	_, err := c.Chat(ctx, Request{})
	return err
}

func TestRunTimeoutBudgetAndConsecutiveFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := runConfig(t)
		c := NewRun(cfg, func(ctx context.Context, r Request) (Reply, error) {
			<-ctx.Done()
			return Reply{}, fmt.Errorf("model.run: %w", ctx.Err())
		})
		timeout := func(budget time.Duration) error {
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			_, err := c.Chat(ctx, Request{})
			return err
		}
		firstTimeout := func(budget time.Duration) {
			t.Helper()
			err := timeout(budget)
			if !errors.Is(err, context.DeadlineExceeded) || Held(cfg) != nil {
				t.Fatalf("budget %s: error = %v, pause = %v", budget, err, Held(cfg))
			}
		}
		firstTimeout(3 * time.Second)
		firstTimeout(3 * time.Second)
		firstTimeout(30 * time.Second)
		for _, result := range []error{nil, errors.New("offline"), errors.New("logged_out")} {
			if err := runResult(t.Context(), cfg, result); err != result {
				t.Fatalf("run returned %v, want %v", err, result)
			}
			firstTimeout(30 * time.Second) // the intervening outcome broke the streak
		}
		firstTimeout(3 * time.Second) // neither trips nor erases the normal timeout
		if err := runResult(t.Context(), cfg, context.Canceled); err != context.Canceled {
			t.Fatalf("canceled run = %v", err)
		}
		if err := timeout(180 * time.Second); !IsPaused(err) {
			t.Fatalf("second normal timeout = %v", err)
		}
		for _, wait := range []time.Duration{busyFirst, 2 * busyFirst, 4 * busyFirst} {
			p := Held(cfg)
			if p == nil || p.Reason != ReasonBusy || time.Until(p.Until) != wait {
				t.Fatalf("pause = %v, want busy for %v", p, wait)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := runResult(ctx, cfg, nil); err != context.Canceled {
				t.Fatalf("canceled caller during pause = %v", err)
			}
			<-time.After(wait)
			err := timeout(30 * time.Second)
			if !IsPaused(err) {
				t.Fatalf("probe timeout = %v", err)
			}
		}
		<-time.After(time.Until(Held(cfg).Until))
		firstPause := *Held(cfg)
		if err := timeout(3 * time.Second); !errors.Is(err, context.DeadlineExceeded) || *Held(cfg) != firstPause {
			t.Fatalf("best-effort probe = %v, pause = %v", err, Held(cfg))
		}
		for _, result := range []error{context.Canceled, errors.New("offline"), errors.New("logged_out")} {
			if err := runResult(t.Context(), cfg, result); err != result {
				t.Fatalf("probe returned %v, want %v", err, result)
			}
		}
		if err := runResult(t.Context(), cfg, nil); err != nil || Held(cfg) != nil {
			t.Fatalf("success after failed probes = %v, held = %v", err, Held(cfg))
		}
		firstTimeout(30 * time.Second)
		if err := timeout(30 * time.Second); !IsPaused(err) || time.Until(Held(cfg).Until) != busyFirst {
			t.Fatalf("first hold after recovery = %v, pause = %v", err, Held(cfg))
		}
	})
}

func TestRunGateIdentity(t *testing.T) {
	cfg := Config{ViaMachine: "a/b", RunLauncher: "c", Model: t.Name()}
	t.Cleanup(func() { gates.Delete(runKey(cfg)) })
	other := cfg
	other.ReasoningEffort = "high"
	if err := runResult(t.Context(), cfg, context.DeadlineExceeded); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if err := runResult(t.Context(), other, context.DeadlineExceeded); !IsPaused(err) {
		t.Fatalf("same CLI tuple in a different client: %v", err)
	}
	if p := Held(cfg); p == nil || p.Reason != ReasonBusy {
		t.Fatalf("held = %v", p)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.ViaMachine = "another" },
		func(c *Config) { c.RunLauncher = "another" },
		func(c *Config) { c.Model = "" },
		func(c *Config) { c.ViaMachine, c.RunLauncher = "a", "b/c" },
	} {
		separate := cfg
		mutate(&separate)
		t.Cleanup(func() { gates.Delete(runKey(separate)) })
		if err := runResult(t.Context(), separate, nil); err != nil || Held(separate) != nil {
			t.Fatalf("separate tuple %+v: %v, pause = %v", runKey(separate), err, Held(separate))
		}
	}
	c := newUpstream(t, ok).client("key")
	if err := call(c); err != nil || Held(c.Config()) != nil {
		t.Fatalf("HTTP connection: %v, pause = %v", err, Held(c.Config()))
	}
	ungated := cfg
	ungated.RunLauncher = ""
	for range 3 {
		if err := runResult(t.Context(), ungated, context.DeadlineExceeded); err != context.DeadlineExceeded {
			t.Fatalf("callback without a launcher = %v", err)
		}
	}
}

func TestRunGateCapsConcurrencyAndRecoversAfterOneProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := runConfig(t)
		if err := runResult(t.Context(), cfg, context.DeadlineExceeded); err != context.DeadlineExceeded {
			t.Fatal(err)
		}
		started := make(chan struct{}, 8)
		release := make(chan error)
		done := make(chan error, 8)
		call := func(ctx context.Context) {
			go func() {
				c := NewRun(cfg, func(context.Context, Request) (Reply, error) {
					started <- struct{}{}
					return Reply{}, <-release
				})
				_, err := c.Chat(ctx, Request{})
				done <- err
			}()
		}
		checkStarts := func(want int) {
			t.Helper()
			synctest.Wait()
			if n := len(started); n != want {
				t.Fatalf("%d new launcher calls, want %d", n, want)
			}
			for range want {
				<-started
			}
		}
		for range 6 {
			call(t.Context())
		}
		checkStarts(6)
		release <- context.DeadlineExceeded
		if err := <-done; !IsPaused(err) {
			t.Fatalf("second timeout = %v", err)
		}
		for range 5 {
			release <- nil
			<-done
		}
		call(t.Context())
		if err := <-done; !IsPaused(err) {
			t.Fatalf("call during hold = %v", err)
		}
		checkStarts(0)
		<-time.After(busyFirst)
		for range 5 {
			call(t.Context())
		}
		checkStarts(1) // the cap is three, but only one caller may probe
		ctx, cancel := context.WithCancel(t.Context())
		call(ctx)
		synctest.Wait()
		cancel()
		if err := <-done; err != context.Canceled {
			t.Fatalf("canceled while queued = %v", err)
		}
		release <- nil
		if err := <-done; err != nil || Held(cfg) != nil {
			t.Fatalf("probe success = %v, pause = %v", err, Held(cfg))
		}
		checkStarts(3)
		release <- nil
		<-done
		checkStarts(1)
		for range 3 {
			release <- nil
			<-done
		}
		g := gateForRun(cfg)
		for successes := 4; ; successes++ {
			_, _, inflight, out, cap := gateState(g)
			want := 3 + successes/growAfter
			if want > capLift {
				want = 0
			}
			if inflight != 0 || out != 0 || cap != want {
				t.Fatalf("after %d successes: inflight = %d, out = %d, cap = %d, want %d", successes, inflight, out, cap, want)
			}
			if want == 0 {
				break
			}
			if err := runResult(t.Context(), cfg, nil); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestRunGateInFlightTimeoutsShareTheHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := runConfig(t)
		if err := runResult(t.Context(), cfg, context.DeadlineExceeded); err != context.DeadlineExceeded {
			t.Fatal(err)
		}
		started := make(chan struct{}, 2)
		release := make(chan struct{})
		done := make(chan error, 2)
		for range 2 {
			go func() {
				c := NewRun(cfg, func(context.Context, Request) (Reply, error) {
					started <- struct{}{}
					<-release
					return Reply{}, context.DeadlineExceeded
				})
				_, err := c.Chat(t.Context(), Request{})
				done <- err
			}()
		}
		synctest.Wait()
		if len(started) != 2 {
			t.Fatalf("%d calls started, want 2", len(started))
		}
		close(release)
		for range 2 {
			if err := <-done; !IsPaused(err) {
				t.Fatalf("concurrent timeout = %v, want PausedError", err)
			}
		}
		p := Held(cfg)
		if p == nil || p.Reason != ReasonBusy || time.Until(p.Until) != busyFirst {
			t.Fatalf("pause = %v, want one busy strike for %v", p, busyFirst)
		}
		if _, _, inflight, out, cap := gateState(gateForRun(cfg)); inflight != 0 || out != 0 || cap != 1 {
			t.Fatalf("after hold: inflight = %d, out = %d, cap = %d", inflight, out, cap)
		}
	})
}

func TestLauncherProbePanicReturnsSlot(t *testing.T) {
	clk := fakeClock(t)
	cfg := runConfig(t)
	_ = runResult(t.Context(), cfg, context.DeadlineExceeded)
	_ = runResult(t.Context(), cfg, context.DeadlineExceeded)
	clk.add(busyFirst)
	c := NewRun(cfg, func(context.Context, Request) (Reply, error) { panic("run failed") })
	if !recovering(func() { _, _ = c.Chat(t.Context(), Request{}) }) {
		t.Fatal("expected callback panic")
	}
	if held, probing, inflight, out, _ := gateState(gateForRun(cfg)); !held || probing || inflight != 0 || out != 0 {
		t.Fatalf("after panic: held = %v, probing = %v, inflight = %d, out = %d", held, probing, inflight, out)
	}
	if err := runResult(t.Context(), cfg, nil); err != nil || Held(cfg) != nil {
		t.Fatalf("next probe = %v, pause = %v", err, Held(cfg))
	}
}

func TestRunBudgetAfterWaitingForSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := runConfig(t)
		gateForRun(cfg).cap = 1
		c := NewRun(cfg, func(ctx context.Context, _ Request) (Reply, error) {
			<-ctx.Done()
			return Reply{}, ctx.Err()
		})
		start := func(budget time.Duration) <-chan error {
			done := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(t.Context(), budget)
				defer cancel()
				_, err := c.Chat(ctx, Request{})
				done <- err
			}()
			synctest.Wait()
			return done
		}
		holder := start(27 * time.Second)
		waiter := start(30 * time.Second)
		if err := <-holder; err != context.DeadlineExceeded {
			t.Fatalf("holder = %v", err)
		}
		if err := <-waiter; err != context.DeadlineExceeded || Held(cfg) != nil {
			t.Fatalf("three seconds left for execution: error = %v, pause = %v", err, Held(cfg))
		}
		if err := runResult(t.Context(), cfg, context.DeadlineExceeded); !IsPaused(err) {
			t.Fatalf("second normal-budget timeout = %v", err)
		}
	})
}
