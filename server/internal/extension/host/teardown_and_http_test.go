package host

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunOnceClearsReadyAndSessionBeforeEndTeardown(t *testing.T) {
	t.Run("Stop clears ready, generation, result, and Notify before end waits for child exit", func(t *testing.T) {
		inTeardown := make(chan struct{})
		releaseTeardown := make(chan struct{})
		var disposeOnce sync.Once

		s := spec(t, "rpc")
		s.Startup = true
		s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
			return command, args, nil, nil, func() {
				disposeOnce.Do(func() {
					close(inTeardown)
					<-releaseTeardown
				})
			}, nil
		}

		p := Start(context.Background(), s)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.Wait(ctx); err != nil {
			close(releaseTeardown)
			p.Stop()
			t.Fatalf("initial Wait failed: %v", err)
		}
		if p.Generation() == 0 || !p.Ready() {
			close(releaseTeardown)
			p.Stop()
			t.Fatalf("expected ready Proc with non-zero generation, got ready=%v gen=%d", p.Ready(), p.Generation())
		}

		stopped := make(chan struct{})
		go func() {
			p.Stop()
			close(stopped)
		}()

		select {
		case <-inTeardown:
		case <-ctx.Done():
			close(releaseTeardown)
			t.Fatal("timed out waiting to enter teardown dispose")
		}

		// While p.end is blocked waiting for teardown to complete, the dying run must not look ready or usable.
		if p.Ready() {
			close(releaseTeardown)
			<-stopped
			t.Fatal("Proc.Ready() stayed true during end() teardown")
		}
		if gen := p.Generation(); gen != 0 {
			close(releaseTeardown)
			<-stopped
			t.Fatalf("Proc.Generation() = %d during end() teardown, want 0", gen)
		}
		if p.Link().Online {
			close(releaseTeardown)
			<-stopped
			t.Fatal("Proc.Link().Online stayed true during end() teardown")
		}
		if _, ok := p.Result(); ok {
			close(releaseTeardown)
			<-stopped
			t.Fatal("Proc.Result() returned ok=true during end() teardown")
		}
		if err := p.Notify("demo.ev", struct{}{}); !errors.Is(err, ErrNotRunning) {
			close(releaseTeardown)
			<-stopped
			t.Fatalf("Proc.Notify during end() teardown = %v, want ErrNotRunning", err)
		}

		waitErr := make(chan error, 1)
		go func() {
			waitErr <- p.Wait(ctx)
		}()

		close(releaseTeardown)
		<-stopped

		if err := <-waitErr; !errors.Is(err, ErrNotRunning) {
			t.Fatalf("Wait started during Stop teardown = %v, want ErrNotRunning", err)
		}
	})

	t.Run("lazy Call and BeginHTTP arriving during idle teardown wait for fresh generation", func(t *testing.T) {
		inTeardown := make(chan struct{})
		releaseTeardown := make(chan struct{})
		var runs atomic.Int32

		s := spec(t, "rpc")
		s.Startup = false
		s.OnCall = true
		s.OnHTTP = true
		s.Idle = time.Hour
		s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
			n := runs.Add(1)
			return command, args, nil, nil, func() {
				if n == 1 {
					close(inTeardown)
					<-releaseTeardown
				}
			}, nil
		}

		p := Start(context.Background(), s)
		defer func() {
			select {
			case <-releaseTeardown:
			default:
				close(releaseTeardown)
			}
			p.Stop()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		release := p.Hold()
		if err := p.Wait(ctx); err != nil {
			release()
			t.Fatalf("first Wait failed: %v", err)
		}
		gen1 := p.Generation()
		if gen1 == 0 {
			release()
			t.Fatal("expected non-zero first generation")
		}

		// Drop the only hold with lastUse still zero so wantedLocked() becomes false and runOnce enters teardown.
		release()

		select {
		case <-inTeardown:
		case <-ctx.Done():
			t.Fatal("timed out waiting for idle teardown")
		}

		callErr := make(chan error, 1)
		httpDone := make(chan error, 1)
		go func() {
			var out map[string]any
			callErr <- p.Call(ctx, "demo.echo", map[string]any{"hello": "world"}, &out)
		}()
		go func() {
			end, err := p.BeginHTTP(ctx)
			if end != nil {
				end()
			}
			httpDone <- err
		}()

		// Wait deterministically until both Call and BeginHTTP have entered during teardown.
		for {
			p.mu.Lock()
			inFlight := p.calls == 1 && p.openHTTP == 1
			p.mu.Unlock()
			if inFlight {
				break
			}
			select {
			case err := <-callErr:
				t.Fatalf("Call returned before teardown finished: %v", err)
			case err := <-httpDone:
				t.Fatalf("BeginHTTP returned before teardown finished: %v", err)
			case <-ctx.Done():
				t.Fatal("timed out waiting for Call and BeginHTTP to enter during teardown")
			default:
				runtime.Gosched()
			}
		}

		// Release teardown so the host finishes stopping run 1 and starts run 2 for the waiting callers.
		close(releaseTeardown)

		if err := <-callErr; err != nil {
			t.Fatalf("lazy Call during teardown = %v, want nil on restarted generation", err)
		}
		if err := <-httpDone; err != nil {
			t.Fatalf("lazy BeginHTTP during teardown = %v, want nil on restarted generation", err)
		}
		if gen2 := p.Generation(); gen2 <= gen1 {
			t.Fatalf("expected generation to advance past %d, got %d", gen1, gen2)
		}
	})
}

func TestBeginHTTPEndIsIdempotentAndRejectsStoppedProc(t *testing.T) {
	t.Run("calling end twice never underflows openHTTP", func(t *testing.T) {
		p := Start(context.Background(), Spec{
			ID:      "demo",
			Name:    "Demo",
			Startup: true,
			HTTP:    true,
			OnHTTP:  false,
			Check: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			Logf: t.Logf,
		})
		defer p.Stop()

		end, err := p.BeginHTTP(context.Background())
		if err != nil {
			t.Fatalf("BeginHTTP = %v, want nil", err)
		}
		if p.Idle() {
			t.Fatal("Proc reported idle while HTTP request is open")
		}

		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				end()
			}()
		}
		wg.Wait()

		if !p.Idle() {
			t.Fatal("Proc not idle after repeated end() calls (openHTTP underflowed)")
		}
		end2, err := p.BeginHTTP(context.Background())
		if err != nil {
			t.Fatalf("second BeginHTTP = %v, want nil", err)
		}
		if p.Idle() {
			end2()
			t.Fatal("Proc reported idle while second HTTP request is open")
		}
		end2()
		if !p.Idle() {
			t.Fatal("Proc not idle after second end()")
		}
	})

	t.Run("BeginHTTP returns ErrNotRunning after Stop even when OnHTTP is false", func(t *testing.T) {
		p := Start(context.Background(), Spec{
			ID:      "demo",
			Name:    "Demo",
			Startup: true,
			HTTP:    true,
			OnHTTP:  false,
			Check: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			Logf: t.Logf,
		})
		p.Stop()

		end, err := p.BeginHTTP(context.Background())
		if !errors.Is(err, ErrNotRunning) {
			if end != nil {
				end()
			}
			t.Fatalf("BeginHTTP after Stop (OnHTTP=false) = %v, want ErrNotRunning", err)
		}
		if end != nil {
			t.Fatal("BeginHTTP returned non-nil end on error")
		}
		if !p.Idle() {
			t.Fatal("Proc not idle after rejected BeginHTTP")
		}
	})
}
