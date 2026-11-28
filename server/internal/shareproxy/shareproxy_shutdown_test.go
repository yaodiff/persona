package shareproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func freeLoopbackAddr(t *testing.T) (addr, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = ln.Addr().String()
	_ = ln.Close()
	_, port, _ = net.SplitHostPort(addr)
	return addr, port
}

func waitProxyReady(t *testing.T, addr, port string, serveDone <-chan error) {
	t.Helper()
	client := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/share/"+testToken, nil)
		req.Host = "share.example.test:" + port
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("shareproxy listener did not become ready")
		}
		select {
		case err := <-serveDone:
			t.Fatalf("Serve exited before becoming ready: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Serve must wait for server.Shutdown to drain in-flight HTTP requests before returning
// rather than returning immediately on http.ErrServerClosed and aborting them via defer server.Close().
func TestServeDrainsInFlightRequestBeforeReturning(t *testing.T) {
	slowToken := strings.Repeat("B", 64)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	up := httptest.NewServer(acknowledged(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, slowToken) {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"drained":true}`)
	})))
	defer up.Close()
	defer unblock()

	addr, port := freeLoopbackAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- Serve(ctx, Config{
			Listen:       addr,
			Upstream:     up.URL,
			PublicOrigin: "http://share.example.test:" + port,
			IngressKey:   testIngressKey,
		})
	}()

	waitProxyReady(t, addr, port, serveDone)

	type respResult struct {
		status int
		body   string
		err    error
	}
	reqDone := make(chan respResult, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/public/agent-shares/"+slowToken, nil)
		req.Host = "share.example.test:" + port
		client := &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		}
		resp, err := client.Do(req)
		if err != nil {
			reqDone <- respResult{err: err}
			return
		}
		b, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		reqDone <- respResult{status: resp.StatusCode, body: string(b), err: rerr}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not reach upstream")
	}

	// Cancel Serve while the proxied request is in flight.
	cancel()

	// Serve must NOT return while the in-flight request is still draining.
	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned (%v) before in-flight request finished draining", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Let the upstream answer finish; the client must receive the full 200 response
	// and Serve must then exit cleanly with nil.
	unblock()

	select {
	case res := <-reqDone:
		if res.err != nil {
			t.Fatalf("in-flight request failed during graceful shutdown: %v", res.err)
		}
		if res.status != http.StatusOK || res.body != `{"drained":true}` {
			t.Fatalf("in-flight response = %d %q, want 200 {\"drained\":true}", res.status, res.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete after release")
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned error after clean drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after in-flight request drained")
	}
}

// When an in-flight upstream handler stalls past shutdownGrace, Serve must force-close
// the server and return promptly rather than hanging indefinitely.
func TestServeForcesCloseAfterShutdownGrace(t *testing.T) {
	oldGrace := shutdownGrace
	shutdownGrace = 100 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = oldGrace })

	stuckToken := strings.Repeat("C", 64)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	up := httptest.NewServer(acknowledged(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, stuckToken) {
			enterOnce.Do(func() { close(entered) })
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	})))
	defer up.Close()
	defer unblock()

	addr, port := freeLoopbackAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- Serve(ctx, Config{
			Listen:       addr,
			Upstream:     up.URL,
			PublicOrigin: "http://share.example.test:" + port,
			IngressKey:   testIngressKey,
		})
	}()

	waitProxyReady(t, addr, port, serveDone)

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/public/agent-shares/"+stuckToken, nil)
		req.Host = "share.example.test:" + port
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stuck request did not reach upstream")
	}

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned error after forced shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve hung past shutdownGrace with a stuck upstream request")
	}

	unblock()
	select {
	case <-reqDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client request goroutine did not exit")
	}
}
