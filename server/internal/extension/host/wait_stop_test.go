package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitCallAndBeginHTTPReturnAfterStop(t *testing.T) {
	p := Start(context.Background(), Spec{
		ID:     "demo",
		Name:   "Demo",
		RPC:    true,
		HTTP:   true,
		OnCall: true,
		OnHTTP: true,
		Logf:   t.Logf,
	})
	p.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if err := p.Wait(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Wait after Stop = %v, want ErrNotRunning", err)
	} else if got := callCode(context.Background(), err); got != CodeNotRunning {
		t.Fatalf("callCode(Wait after Stop) = %q, want %q", got, CodeNotRunning)
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := p.Wait(canceled); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Wait(canceled) after Stop = %v, want ErrNotRunning", err)
	}

	if err := p.Call(ctx, "demo.echo", struct{}{}, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Call after Stop = %v, want ErrNotRunning", err)
	}
	if !p.Idle() {
		t.Fatal("Proc not idle after Call failed on stopped Proc")
	}

	if end, err := p.BeginHTTP(ctx); !errors.Is(err, ErrNotRunning) {
		if end != nil {
			end()
		}
		t.Fatalf("BeginHTTP after Stop = %v, want ErrNotRunning", err)
	}
	if !p.Idle() {
		t.Fatal("Proc not idle after BeginHTTP failed on stopped Proc")
	}
}

func TestWaitUnblocksWhenStoppedInFlight(t *testing.T) {
	t.Run("stop while blocked in Wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := Start(context.Background(), Spec{
				ID:      "demo",
				Name:    "Demo",
				Startup: true,
				RPC:     true,
				HTTP:    true,
				OnHTTP:  true,
				Check: func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				},
				Logf: t.Logf,
			})
			defer p.Stop()

			waitErr := make(chan error, 1)
			callErr := make(chan error, 1)
			httpErr := make(chan error, 1)
			go func() { waitErr <- p.Wait(context.Background()) }()
			go func() { callErr <- p.Call(context.Background(), "demo.echo", struct{}{}, nil) }()
			go func() {
				end, err := p.BeginHTTP(context.Background())
				if end != nil {
					end()
				}
				httpErr <- err
			}()

			// Wait until p.loop is blocked in Check and all three callers are parked in Proc.Wait's select.
			synctest.Wait()
			if p.Idle() {
				t.Fatal("expected in-flight Call and BeginHTTP before Stop")
			}
			select {
			case err := <-waitErr:
				t.Fatalf("Wait returned before Stop: %v", err)
			case err := <-callErr:
				t.Fatalf("Call returned before Stop: %v", err)
			case err := <-httpErr:
				t.Fatalf("BeginHTTP returned before Stop: %v", err)
			default:
			}

			p.Stop()
			synctest.Wait()

			if err := <-waitErr; !errors.Is(err, ErrNotRunning) {
				t.Fatalf("in-flight Wait after Stop = %v, want ErrNotRunning", err)
			}
			if err := <-callErr; !errors.Is(err, ErrNotRunning) {
				t.Fatalf("in-flight Call after Stop = %v, want ErrNotRunning", err)
			}
			if err := <-httpErr; !errors.Is(err, ErrNotRunning) {
				t.Fatalf("in-flight BeginHTTP after Stop = %v, want ErrNotRunning", err)
			}
			if !p.Idle() {
				t.Fatal("Proc not idle after in-flight Call and BeginHTTP unblocked")
			}
		})
	})

	t.Run("parent context already canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			cancelParent()

			p := Start(parent, Spec{
				ID:     "demo",
				Name:   "Demo",
				RPC:    true,
				OnCall: true,
				Logf:   t.Logf,
			})
			<-p.done

			if err := p.Wait(context.Background()); !errors.Is(err, ErrNotRunning) {
				t.Fatalf("Wait after parent cancel = %v, want ErrNotRunning", err)
			}
		})
	})

	t.Run("StateError still reported while running and becomes ErrNotRunning after Stop", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := Start(context.Background(), Spec{
				ID:      "demo",
				Name:    "Demo",
				Startup: true,
				Check: func(context.Context) error {
					return errors.New("check boom")
				},
				Logf: t.Logf,
			})
			defer p.Stop()

			synctest.Wait()

			err := p.Wait(context.Background())
			if err == nil || errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "check boom") {
				t.Fatalf("Wait in StateError = %v, want host failure containing %q", err, "check boom")
			}

			p.Stop()

			if err := p.Wait(context.Background()); !errors.Is(err, ErrNotRunning) {
				t.Fatalf("Wait after stopping StateError Proc = %v, want ErrNotRunning", err)
			}
		})
	})
}
