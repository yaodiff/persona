package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
)

func startInProcessAuthExtension(t *testing.T, id string, handler pserve.Handler) (*Manager, *host.Proc) {
	t.Helper()
	h, _ := extensionTestHub(t)
	sock := filepath.Join(shortTempDir(t), "auth.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
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
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				_ = pserve.Serve(serveCtx, c, c, pserve.Program{
					ID:         id,
					Version:    "1.0.0",
					Namespaces: map[string]pserve.Handler{"auth": handler},
				})
			}(conn)
		}
	}()
	t.Cleanup(func() {
		stopServe()
		_ = ln.Close()
		wg.Wait()
	})

	devDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(devDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "bin", "noop"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	manifestJSON, err := json.Marshal(map[string]any{
		"manifest_version": extension.ManifestVersion,
		"id":               id,
		"name":             "Test Auth",
		"version":          "1.0.0",
		"engines":          map[string]any{"plugin_protocol": extension.Protocol},
		"runtime":          map[string]any{"command": map[string]string{platform: "bin/noop"}},
		"contributes": map[string]any{
			"ui": []map[string]any{{"id": "login", "title": "Login", "placement": "plugin_page"}},
		},
		"auth": map[string]any{"title": "Test Sign-In", "description": "Sign in", "page": "login"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, extension.ManifestFile), manifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.extensions.reg.AddDev(devDir, nil, nil); err != nil {
		t.Fatal(err)
	}
	proc := host.Start(serveCtx, host.Spec{
		ID:        id,
		Name:      "Test Auth",
		RPC:       true,
		RPCSocket: sock,
		Startup:   true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: extension.Protocol}, nil
		},
	})
	t.Cleanup(func() { proc.Stop() })
	wctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := proc.Wait(wctx); err != nil {
		t.Fatalf("proc.Wait: %v", err)
	}
	h.extensions.mu.Lock()
	old := h.extensions.byID[id]
	h.extensions.byID[id] = &hubExtension{id: id, proc: proc}
	h.extensions.mu.Unlock()
	if old != nil && old.proc != nil {
		old.proc.Stop()
	}
	return h, proc
}

type blockingBodyReader struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	buf     *bytes.Reader
}

func (b *blockingBodyReader) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.buf.Read(p)
}

func (b *blockingBodyReader) Close() error { return nil }

func TestExtensionAuthRoutesDoNotPanicOnTOCTOUNilProcOrUninstall(t *testing.T) {
	t.Run("step_proc_removed_during_body_read", func(t *testing.T) {
		h, _ := startInProcessAuthExtension(t, "example-auth", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			return map[string]any{"done": true}, nil
		})
		mux := h.routes()
		body := &blockingBodyReader{
			entered: make(chan struct{}),
			release: make(chan struct{}),
			buf:     bytes.NewReader([]byte(`{"step":""}`)),
		}
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(body.release) }) }
		defer unblock()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/extensions/example-auth/-/auth/step", body)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		rec := httptest.NewRecorder()
		done := make(chan any, 1)
		go func() {
			defer func() {
				done <- recover()
			}()
			mux.ServeHTTP(rec, req)
		}()

		select {
		case <-body.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not start reading body")
		}
		// Simulate extension stopping/uninstalling after running() passed and before proc.Call().
		h.extensions.mu.Lock()
		delete(h.extensions.byID, "example-auth")
		h.extensions.mu.Unlock()
		unblock()

		select {
		case p := <-done:
			if p != nil {
				t.Fatalf("auth/step panicked when proc disappeared: %v", p)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("auth/step did not return")
		}
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusConflict)
		}
	})

	t.Run("logout_uninstalled_during_rpc", func(t *testing.T) {
		logoutEntered, releaseLogout := make(chan struct{}), make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(releaseLogout) }) }
		defer unblock()

		h, _ := startInProcessAuthExtension(t, "example-auth", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "logout" {
				enterOnce.Do(func() { close(logoutEntered) })
				<-releaseLogout
				return map[string]any{"state": "signed_out"}, nil
			}
			return map[string]any{"state": "signed_in"}, nil
		})
		mux := h.routes()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/extensions/example-auth/-/auth/logout", io.NopCloser(bytes.NewReader(nil)))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		rec := httptest.NewRecorder()
		done := make(chan any, 1)
		go func() {
			defer func() {
				done <- recover()
			}()
			mux.ServeHTTP(rec, req)
		}()

		select {
		case <-logoutEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("auth.logout RPC did not start")
		}
		// Simulate extension removal while auth.logout is in flight.
		h.extensions.mu.Lock()
		delete(h.extensions.byID, "example-auth")
		h.extensions.mu.Unlock()
		if err := h.extensions.reg.RemoveDev("example-auth"); err != nil {
			t.Fatalf("RemoveDev: %v", err)
		}
		unblock()

		select {
		case p := <-done:
			if p != nil {
				t.Fatalf("auth/logout panicked when extension was uninstalled mid-RPC: %v", p)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("auth/logout did not return")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusOK)
		}
		if got := h.extensions.authView(nil); got != nil {
			t.Fatalf("authView(nil) = %+v, want nil", got)
		}
	})
}

func TestExtensionAuthAskDoesNotResurrectStaleCacheAfterForgetAuth(t *testing.T) {
	statusEntered, releaseStatus := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseStatus) }) }
	defer unblock()

	h, _ := startInProcessAuthExtension(t, "example-auth", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "status" {
			enterOnce.Do(func() { close(statusEntered) })
			<-releaseStatus
			return map[string]any{"state": "signed_in", "account": "stale@example.com"}, nil
		}
		return map[string]any{}, nil
	})

	askDone := make(chan error, 1)
	go func() {
		_, err := h.extensions.askAuth(t.Context(), "example-auth")
		askDone <- err
	}()

	select {
	case <-statusEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("auth.status RPC did not start")
	}

	// Extension config/lifecycle changed while auth.status was in flight (even with empty cache before).
	h.extensions.forgetAuth("example-auth")
	unblock()

	select {
	case err := <-askDone:
		if err != nil {
			t.Fatalf("askAuth error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("askAuth did not return")
	}

	if st, _, cached := h.extensions.auth.get("example-auth"); cached {
		t.Fatalf("stale auth status resurrected into cache after forgetAuth: %+v", st)
	}
}

func TestExtensionAuthChangedDuringInFlightRefreshTriggersFollowUp(t *testing.T) {
	call1Entered, releaseCall1 := make(chan struct{}), make(chan struct{})
	call2Done := make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseCall1) }) }
	defer unblock()

	h, _ := startInProcessAuthExtension(t, "example-auth", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "status" {
			return map[string]any{}, nil
		}
		n := calls.Add(1)
		if n == 1 {
			close(call1Entered)
			<-releaseCall1
			return map[string]any{"state": "pending"}, nil
		}
		if n == 2 {
			defer close(call2Done)
		}
		return map[string]any{"state": "signed_in", "account": "fresh@example.com"}, nil
	})

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		h.extensions.refreshAuth(t.Context(), "example-auth")
	}()

	select {
	case <-call1Entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first auth.status RPC did not start")
	}

	// Simulate an auth.changed notification arriving while the first refresh is still in flight.
	h.extensions.refreshAuth(t.Context(), "example-auth")
	unblock()

	select {
	case <-call2Done:
	case <-time.After(2 * time.Second):
		t.Fatal("follow-up auth.status refresh was lost while prior refresh was in flight")
	}
	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshAuth did not finish")
	}

	st, _, ok := h.extensions.auth.get("example-auth")
	if !ok || st.State != authSignedIn || st.Account != "fresh@example.com" {
		t.Fatalf("cached auth = (%+v, %v), want signed_in fresh@example.com", st, ok)
	}
}

func TestExtensionAuthDropDoesNotSwallowQueuedFollowUpRefresh(t *testing.T) {
	call1Entered, releaseCall1 := make(chan struct{}), make(chan struct{})
	call2Done := make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseCall1) }) }
	defer unblock()

	h, _ := startInProcessAuthExtension(t, "example-auth", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "status" {
			return map[string]any{}, nil
		}
		n := calls.Add(1)
		if n == 1 {
			close(call1Entered)
			<-releaseCall1
			return map[string]any{"state": "pending"}, nil
		}
		if n == 2 {
			defer close(call2Done)
		}
		return map[string]any{"state": "signed_in", "account": "after-drop@example.com"}, nil
	})

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		h.extensions.refreshAuth(t.Context(), "example-auth")
	}()

	select {
	case <-call1Entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first auth.status RPC did not start")
	}

	// Queue a follow-up refresh (e.g. auth.changed from replacement proc), then run forgetAuth/drop.
	h.extensions.refreshAuth(t.Context(), "example-auth")
	h.extensions.forgetAuth("example-auth")
	unblock()

	select {
	case <-call2Done:
	case <-time.After(2 * time.Second):
		t.Fatal("drop() swallowed queued follow-up refresh")
	}
	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshAuth did not finish")
	}

	st, _, ok := h.extensions.auth.get("example-auth")
	if !ok || st.State != authSignedIn || st.Account != "after-drop@example.com" {
		t.Fatalf("cached auth = (%+v, %v), want signed_in after-drop@example.com", st, ok)
	}
}

func TestExtensionAuthSlowAskDoesNotOverwriteLogout(t *testing.T) {
	statusEntered, releaseStatus := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseStatus) }) }
	defer unblock()

	h, _ := startInProcessAuthExtension(t, "example-auth", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "status":
			enterOnce.Do(func() { close(statusEntered) })
			<-releaseStatus
			return map[string]any{"state": "signed_in", "account": "stale@example.com"}, nil
		case "logout":
			return map[string]any{"state": "signed_out"}, nil
		}
		return map[string]any{}, nil
	})

	askDone := make(chan error, 1)
	go func() {
		_, err := h.extensions.askAuth(t.Context(), "example-auth")
		askDone <- err
	}()

	select {
	case <-statusEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow auth.status RPC did not start")
	}

	mux := h.routes()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/extensions/example-auth/-/auth/logout", io.NopCloser(bytes.NewReader(nil)))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}

	unblock()
	select {
	case err := <-askDone:
		if err != nil {
			t.Fatalf("askAuth error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("askAuth did not return")
	}

	st, _, ok := h.extensions.auth.get("example-auth")
	if !ok || st.State != authSignedOut {
		t.Fatalf("slow askAuth overwrote logout state: got (%+v, %v), want signed_out", st, ok)
	}
}
