package proc

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func newPipeConn(t *testing.T, onReq func(ctx context.Context, method string, params json.RawMessage) (any, error)) *Conn {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	clientPeer := rpc.New("c", clientW)
	serverPeer := rpc.New("s", serverW)
	serverPeer.OnRequest = onReq

	serveCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = clientPeer.Serve(serveCtx, clientR)
	}()
	go func() {
		defer wg.Done()
		_ = serverPeer.Serve(serveCtx, serverR)
	}()
	t.Cleanup(func() {
		cancel()
		_ = clientW.Close()
		_ = serverW.Close()
		_ = clientR.Close()
		_ = serverR.Close()
		wg.Wait()
	})

	c := New(Config{
		ID:   "example",
		Name: "Example",
		Manifest: connector.Manifest{
			Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools},
		},
	})
	c.mu.Lock()
	c.peer = clientPeer
	c.sent = true
	c.mu.Unlock()
	return c
}

func TestCanceledAgainRoundDoesNotPoisonNextHealthyRefresh(t *testing.T) {
	call1Entered, releaseCall1 := make(chan struct{}), make(chan struct{})
	var statusCalls atomic.Int32
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseCall1) }) }
	defer unblock()

	c := newPipeConn(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case serve.MStatus:
			n := statusCalls.Add(1)
			if n == 1 {
				close(call1Entered)
				<-releaseCall1
				return connector.Status{State: connector.StateOK, Account: "first"}, nil
			}
			return connector.Status{State: connector.StateOK, Account: "second"}, nil
		case serve.MTools:
			return serve.ToolsResult{}, nil
		}
		return map[string]any{}, nil
	})

	r1 := c.kick(t.Context())
	select {
	case <-call1Entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first status call did not start")
	}

	// Caller A queues the pending follow-up round (c.again), then cancels before round 1 finishes.
	ctxA, cancelA := context.WithCancel(t.Context())
	rA := c.kick(ctxA)
	c.mu.Lock()
	againA := c.again
	c.mu.Unlock()
	if againA == nil {
		t.Fatal("expected pending c.again round")
	}
	cancelA()
	select {
	case <-againA.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("canceled caller did not cancel pending round")
	}
	// When detached cleanly, rA closes immediately without waiting for round 1.
	select {
	case <-rA:
	case <-time.After(2 * time.Second):
		t.Fatal("detached c.again done channel did not close before round 1 finished")
	}

	// Already-canceled context when refreshing != nil && again == nil must return a closed channel
	// without allocating c.again.
	canceledWhileBusy, cancelBusy := context.WithCancel(t.Context())
	cancelBusy()
	select {
	case <-c.kick(canceledWhileBusy):
	case <-time.After(time.Second):
		t.Fatal("kick(canceled) while refreshing did not return immediately")
	}
	c.mu.Lock()
	spuriousAgain := c.again
	c.mu.Unlock()
	if spuriousAgain != nil {
		t.Fatal("kick(canceled) created c.again while refreshing")
	}

	// Two askers sharing c.again: canceling the first keeps it live; canceling the second detaches and closes it.
	ctx1, cancel1 := context.WithCancel(t.Context())
	ctx2, cancel2 := context.WithCancel(t.Context())
	rShared1 := c.kick(ctx1)
	rShared2 := c.kick(ctx2)
	if rShared1 != rShared2 {
		t.Fatal("concurrent askers did not share c.again")
	}
	cancel1()
	// Wait until cancel1's AfterFunc has decremented live to 1, while c.again remains attached and open.
	c.mu.Lock()
	sharedRound := c.again
	c.mu.Unlock()
	if sharedRound == nil {
		t.Fatal("expected shared c.again round")
	}
	select {
	case <-rShared1:
		t.Fatal("canceling first of two shared askers closed c.again prematurely")
	default:
	}
	cancel2()
	select {
	case <-rShared1:
	case <-time.After(2 * time.Second):
		t.Fatal("canceling last shared asker did not detach and close c.again")
	}
	c.mu.Lock()
	stillAttached := c.again
	c.mu.Unlock()
	if stillAttached != nil {
		t.Fatal("c.again remained attached after all shared askers canceled")
	}

	// Caller B now arrives with a healthy context while round 1 is still running.
	// It must NOT inherit any canceled round context.
	rB := c.kick(t.Context())
	unblock()

	for _, ch := range []<-chan struct{}{r1, rB} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("refresh round did not finish")
		}
	}

	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("status calls = %d, want 2 (healthy caller B was skipped due to poisoned c.again)", got)
	}
	if st := c.Status(t.Context()); st.State != connector.StateOK || st.Account != "second" {
		t.Fatalf("status = %+v, want Account=\"second\"", st)
	}

	// Already-canceled context when idle must return a closed channel without invoking status.
	canceled, cancelNow := context.WithCancel(t.Context())
	cancelNow()
	select {
	case <-c.kick(canceled):
	case <-time.After(time.Second):
		t.Fatal("kick(canceled) did not return immediately")
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("kick(canceled) unexpectedly triggered status call: calls=%d", got)
	}
}

func TestLinkedFetchDoesNotOverwriteStatusAfterProcStop(t *testing.T) {
	dir, err := os.MkdirTemp("", "hst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "plugin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	toolsEntered, releaseTools := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseTools) }) }
	defer unblock()

	serveCtx, stopServe := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(nc net.Conn) {
				defer wg.Done()
				defer nc.Close()
				_ = pserve.Serve(serveCtx, nc, nc, pserve.Program{
					ID:      "example-ext",
					Version: "1.0.0",
					Namespaces: map[string]pserve.Handler{
						"connector": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
							if method == serve.MStatus {
								return connector.Status{State: connector.StateOK, Account: "stale-gen1"}, nil
							}
							if method == serve.MTools {
								enterOnce.Do(func() { close(toolsEntered) })
								<-releaseTools
								return serve.ToolsResult{}, nil
							}
							return map[string]any{}, nil
						},
					},
				})
			}(conn)
		}
	}()
	t.Cleanup(func() {
		unblock()
		stopServe()
		_ = ln.Close()
		wg.Wait()
	})

	p := host.Start(serveCtx, host.Spec{
		ID:        "example-ext",
		Name:      "Example",
		RPC:       true,
		RPCSocket: sock,
		Startup:   true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: extension.Protocol}, nil
		},
	})
	t.Cleanup(func() { p.Stop() })

	wctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := p.Wait(wctx); err != nil {
		t.Fatalf("p.Wait: %v", err)
	}

	c := New(Config{
		ID:   "example",
		Name: "Example",
		Manifest: connector.Manifest{
			Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools},
		},
		Link: &Link{Proc: p, ID: "example"},
	})
	c.mu.Lock()
	c.sent = true
	c.mu.Unlock()
	c.setDown("Example 已停止")

	done := c.kick(t.Context())
	select {
	case <-toolsEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("linked tools RPC did not start")
	}

	// Stop the linked plugin process (generation drops to 0) while fetch already received status=StateOK
	// and is blocked in tools. When tools fails with connection closed, stale gen-1 fetch must NOT commit
	// StateOK over the stopped status.
	p.Stop()
	unblock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not finish after Proc.Stop")
	}

	if st := c.Status(t.Context()); st.State != connector.StateError || st.Error != "Example 已停止" {
		t.Fatalf("stale linked fetch overwrote stopped status: got %+v, want Error=%q", st, "Example 已停止")
	}
}

func TestFetchDoesNotOverwriteSetDownEvenWhenRPCSucceeds(t *testing.T) {
	toolsEntered, releaseTools := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseTools) }) }
	defer unblock()

	c := newPipeConn(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case serve.MStatus:
			return connector.Status{State: connector.StateOK, Account: "stale"}, nil
		case serve.MTools:
			enterOnce.Do(func() { close(toolsEntered) })
			<-releaseTools
			return serve.ToolsResult{Tools: []proto.Tool{{Name: "example_stale"}}}, nil
		}
		return map[string]any{}, nil
	})

	done := c.kick(t.Context())
	select {
	case <-toolsEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("tools RPC did not start")
	}

	// Simulate setDown running while an earlier fetch is still in flight; even if the in-flight RPC
	// returns a successful response, c.gen fencing must preserve setDown's error state.
	c.setDown("Example 已停止")
	unblock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not finish")
	}

	if st := c.Status(t.Context()); st.State != connector.StateError || st.Error != "Example 已停止" {
		t.Fatalf("stale fetch overwrote setDown: got %+v, want Error=%q", st, "Example 已停止")
	}
	if tools := c.Tools(); len(tools) != 0 {
		t.Fatalf("stale fetch overwrote tools after setDown: %+v", tools)
	}
}

func TestLinkedFetchDoesNotOverwriteSetDownWithConstantGeneration(t *testing.T) {
	dir, err := os.MkdirTemp("", "hst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "plugin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	toolsEntered, releaseTools := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseTools) }) }
	defer unblock()

	serveCtx, stopServe := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(nc net.Conn) {
				defer wg.Done()
				defer nc.Close()
				_ = pserve.Serve(serveCtx, nc, nc, pserve.Program{
					ID:      "example-ext",
					Version: "1.0.0",
					Namespaces: map[string]pserve.Handler{
						"connector": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
							if method == serve.MStatus {
								return connector.Status{State: connector.StateOK, Account: "stale-linked"}, nil
							}
							if method == serve.MTools {
								enterOnce.Do(func() { close(toolsEntered) })
								<-releaseTools
								return serve.ToolsResult{Tools: []proto.Tool{{Name: "example_stale"}}}, nil
							}
							return map[string]any{}, nil
						},
					},
				})
			}(conn)
		}
	}()
	t.Cleanup(func() {
		unblock()
		stopServe()
		_ = ln.Close()
		wg.Wait()
	})

	p := host.Start(serveCtx, host.Spec{
		ID:        "example-ext",
		Name:      "Example",
		RPC:       true,
		RPCSocket: sock,
		Startup:   true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: extension.Protocol}, nil
		},
	})
	t.Cleanup(func() { p.Stop() })

	wctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := p.Wait(wctx); err != nil {
		t.Fatalf("p.Wait: %v", err)
	}
	genBefore := p.Generation()

	c := New(Config{
		ID:   "example",
		Name: "Example",
		Manifest: connector.Manifest{
			Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools},
		},
		Link: &Link{Proc: p, ID: "example"},
	})
	c.mu.Lock()
	c.sent = true
	c.mu.Unlock()

	done := c.kick(t.Context())
	select {
	case <-toolsEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("linked tools RPC did not start")
	}

	// Process stays running at the same generation, but Conn.setDown ran concurrently.
	// Conn.gen fencing must preserve setDown even though p.Generation() is unchanged.
	c.setDown("Example 已停止")
	unblock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("linked fetch did not finish")
	}
	if p.Generation() != genBefore {
		t.Fatalf("expected constant host generation %d, got %d", genBefore, p.Generation())
	}
	if st := c.Status(t.Context()); st.State != connector.StateError || st.Error != "Example 已停止" {
		t.Fatalf("stale linked fetch overwrote setDown: got %+v, want Error=%q", st, "Example 已停止")
	}
	if tools := c.Tools(); len(tools) != 0 {
		t.Fatalf("stale linked fetch overwrote tools after setDown: %+v", tools)
	}
}
