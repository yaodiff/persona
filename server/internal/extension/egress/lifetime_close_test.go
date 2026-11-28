package egress

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// unclosedParentCtx is a custom Context whose Done channel never closes. Any context.WithCancel / WithDeadline
// derived from it without invoking its cancel func leaves a propagation goroutine durably blocked inside a
// synctest.Test bubble, which synctest deterministically fails on exit.
type unclosedParentCtx struct {
	done chan struct{}
}

func (unclosedParentCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c unclosedParentCtx) Done() <-chan struct{}     { return c.done }
func (unclosedParentCtx) Err() error                  { return nil }
func (unclosedParentCtx) Value(any) any               { return nil }

func TestRequestLifetimeCheckDoesNotLeakParentContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &Proxy{}
		p.Register("ext", Rule{Hosts: []string{"example.com"}})
		cred, revoke, ok := p.Check("ext", nil, time.Minute)
		if !ok {
			t.Fatal("Check failed")
		}
		defer revoke()

		p.mu.Lock()
		chk := p.checks[cred]
		p.mu.Unlock()

		parent := unclosedParentCtx{done: make(chan struct{})}
		for range 20 {
			ctx, release, ok := p.requestLifetime(parent, "ext", chk)
			if !ok {
				t.Fatal("requestLifetime refused live check")
			}
			if _, hasDeadline := ctx.Deadline(); !hasDeadline {
				t.Fatal("check requestLifetime missing deadline")
			}
			release()
		}
		synctest.Wait()
	})
}

func TestProxyCloseRevokesChecksAndRejectsNewLeases(t *testing.T) {
	p := &Proxy{}
	p.Register("ext", Rule{Hosts: []string{"example.com"}})
	cred, _, ok := p.Check("ext", nil, time.Minute)
	if !ok {
		t.Fatal("Check failed")
	}

	p.mu.Lock()
	chk := p.checks[cred]
	p.mu.Unlock()

	ctx1, release1, ok := p.requestLifetime(context.Background(), "ext", chk)
	if !ok {
		t.Fatal("requestLifetime failed before Close")
	}
	defer release1()

	p.Close()
	p.Close() // idempotent

	if ctx1.Err() == nil {
		t.Fatal("in-flight lease context was not canceled by Close")
	}
	if err := p.Start(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Start after Close = %v, want net.ErrClosed", err)
	}
	if _, _, ok := p.requestLifetime(context.Background(), "ext", nil); ok {
		t.Fatal("requestLifetime granted a lease after Close")
	}
	if _, _, ok := p.requestLifetime(context.Background(), "ext", chk); ok {
		t.Fatal("requestLifetime granted a check lease after Close")
	}
	if _, _, ok := p.Check("ext", nil, time.Minute); ok {
		t.Fatal("Check issued a credential after Close")
	}
	if got := p.Register("ext", Rule{Hosts: []string{"example.com"}}); got != "" {
		t.Fatalf("Register after Close = %q, want empty", got)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(User+":"+cred)))
	rec := httptest.NewRecorder()
	p.handler("").ServeHTTP(rec, req)
	if rec.Code != http.StatusProxyAuthRequired {
		t.Fatalf("handler accepted revoked check credential after Close: status=%d", rec.Code)
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "u.sock")
	if err := p.ListenUnix("ext", sock); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ListenUnix after Close = %v, want net.ErrClosed", err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ListenUnix after Close left socket file on disk: stat err=%v", err)
	}
	if err := p.RelayUnix("ext", filepath.Join(dir, "r.sock"), filepath.Join(dir, "t.sock")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("RelayUnix after Close = %v, want net.ErrClosed", err)
	}

	p.mu.Lock()
	remRules, remCreds, remChecks := len(p.rules), len(p.creds), len(p.checks)
	p.mu.Unlock()
	if remRules != 0 || remCreds != 0 || remChecks != 0 {
		t.Fatalf("state remained after Close: rules=%d creds=%d checks=%d", remRules, remCreds, remChecks)
	}
}
