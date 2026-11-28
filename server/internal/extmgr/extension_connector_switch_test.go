package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
)

// serveLiveExtensionForTest has a connector of its own (examplelive), a channel bound to it and a page. Each start
// writes the connectors its initialize lists as active, one line a start, to LIVE_LOG; its sign-in's first step says
// it came from the program.
func serveLiveExtensionForTest() {
	logPath := os.Getenv("LIVE_LOG")
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-live", Version: "1.0.0",
		Init: func(_ context.Context, p extension.InitializeParams, _ *pserve.Hub) (map[string]any, error) {
			f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			_, err = fmt.Fprintln(f, strings.Join(p.Active["connectors"], ","))
			return nil, err
		},
		Namespaces: map[string]pserve.Handler{
			"connector": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				switch method {
				case "status":
					return map[string]string{"state": "ok"}, nil
				case "login.step":
					return map[string]string{"step": "code", "hint": "from the program"}, nil
				}
				return nil, pserve.UnknownMethod(method)
			},
			"ui": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				if method == "render" {
					return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "text", "text": "live page"}}}}}, nil
				}
				return nil, pserve.UnknownMethod(method)
			}}})
}

// livePackage is the extension serveLiveExtensionForTest runs, started with the hub, its starts written to log.
func livePackage(t *testing.T, log string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "live"), []byte(fmt.Sprintf("#!/bin/sh\nPERSONA_TEST_PLUGIN=live LIVE_LOG=%q exec %s \"$@\"\n", log, self)), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-live", "name": "Example Live", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/live"}}, "activation_events": ["on_startup"],
		"contributes": {
			"connectors": [{"id": "examplelive", "name": "Example Live", "capabilities": ["status", "login"]}],
			"channel": [{"id": "examplelive", "name": "Example Live", "kind": "im", "connector": "examplelive", "capabilities": {}}],
			"ui": [{"id": "panel", "title": "Panel", "placement": "extension_page"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// Switching an extension's connector restarts the extension, so its initialize lists only the connectors that are on
// (#10467): off from its contribution switch (the channel bound to it, which is the connector's switch), on from
// Settings › Connectors, on again by binding the channel, whose sign-in then reaches the program that runs now. Its page
// works throughout. A switch that changes nothing, and the hub's own connector, restart nothing.
func TestConnectorSwitchRestartsItsExtension(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()
	log := filepath.Join(t.TempDir(), "active.log")
	installForTest(t, h, livePackage(t, log))

	starts := func() []string { // what each start's initialize listed, the last line only once it is whole
		raw, _ := os.ReadFile(log)
		lines := strings.Split(string(raw), "\n")
		return lines[:len(lines)-1]
	}
	up := func(n int, active string) *hubExtension {
		t.Helper()
		waitLong(t, fmt.Sprintf("start %d", n), func() bool {
			hp := h.extensions.get("example-live")
			return len(starts()) >= n && hp != nil && hp.proc != nil && hp.proc.Ready()
		})
		if got := starts(); len(got) != n || got[n-1] != active {
			t.Fatalf("started %q, want %d starts, the last with %q active", got, n, active)
		}
		return h.extensions.get("example-live")
	}
	page := func() {
		t.Helper()
		code, out := call(t, mux, "POST", "/api/v1/extensions/example-live/-/ui/panel", master, `{}`)
		if code != 200 || !strings.Contains(fmt.Sprint(out["view"]), "live page") {
			t.Fatalf("the page: %d %v", code, out)
		}
	}
	hp := up(1, "examplelive")
	page()

	// A switch that changes nothing.
	if err := h.setConnectorEnabled(ctx, "examplelive", true); err != nil {
		t.Fatal(err)
	}
	if h.extensions.get("example-live") != hp {
		t.Fatal("restarted for a switch that changed nothing")
	}

	// Off from its contribution switch: restarted once, without it; the page still works.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/example-live/-/contributions/channel/examplelive", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if h.connectors.Enabled("examplelive") {
		t.Fatal("the connector is off")
	}
	if h.extensions.get("example-live") == hp {
		t.Fatal("not restarted")
	}
	hp = up(2, "")
	page()

	// The hub's own connector, switched off and on: nothing restarts.
	if err := h.addConnector(&exampleConnector{}); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		if err := h.setConnectorEnabled(ctx, "example", on); err != nil {
			t.Fatal(err)
		}
	}
	if h.extensions.get("example-live") != hp {
		t.Fatal("restarted for the hub's own connector")
	}

	// On from Settings › Connectors: restarted with it, and the connector registered again stays on.
	if code, out := call(t, mux, "PUT", "/api/v1/connectors/examplelive/settings", master, `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	up(3, "examplelive")
	if !h.connectors.Enabled("examplelive") {
		t.Fatal("the connector is on")
	}

	// Off again, and on by binding its channel: the sign-in goes to the program that runs now, not the stopped one.
	if err := h.setConnectorEnabled(ctx, "examplelive", false); err != nil {
		t.Fatal(err)
	}
	up(4, "")
	code, out := call(t, mux, "POST", "/api/v1/channels/examplelive/bind", master, `{}`)
	if code != 200 || out["hint"] != "from the program" || out["error"] != nil {
		t.Fatalf("bind: %d %v", code, out)
	}
	up(5, "examplelive")
	page()
}

// Regression test: real HTTP requests (whose r.Context() is canceled by net/http as soon as the handler returns) and
// direct calls with short-lived canceled contexts must never bind the restarted extension process's lifetime to the
// request context. The process must stay running and serve follow-up RPC/UI calls after request cancellation, and must
// still exit cleanly when the manager's own lifecycle context is canceled.
func TestConnectorSwitchRestartSurvivesRequestCancel(t *testing.T) {
	_ = t.TempDir()
	h := newTestManager(t)
	h.store = newExtensionStore()
	h.connectors = connector.NewRegistry(nil)
	h.extensions = newExtensionsWith(h.cfg.dir, nil, h.logf, h.modules())
	h.extensions.sandboxes.noDefault = true
	mgrCtx, stopMgr := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stopMgr()
		for _, p := range h.extensions.reg.List() {
			if hp := h.extensions.get(p.Record.ID); hp != nil && hp.proc != nil {
				hp.proc.Stop()
			}
		}
	})
	h.startTokenRecord(mgrCtx)
	h.startConnectors(mgrCtx)
	h.startExtensions(mgrCtx)

	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	client := srv.Client()
	master := h.cfg.Token

	httpCall := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+master)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	log := filepath.Join(t.TempDir(), "active.log")
	installForTest(t, h, livePackage(t, log))

	starts := func() []string {
		raw, _ := os.ReadFile(log)
		lines := strings.Split(string(raw), "\n")
		return lines[:len(lines)-1]
	}
	assertRunning := func(n int, active string) *hubExtension {
		t.Helper()
		waitLong(t, fmt.Sprintf("start %d", n), func() bool {
			hp := h.extensions.get("example-live")
			return len(starts()) >= n && hp != nil && hp.proc != nil && hp.proc.Ready()
		})
		if got := starts(); len(got) != n || got[n-1] != active {
			t.Fatalf("started %q, want %d starts, the last with %q active", got, n, active)
		}
		hp := h.extensions.get("example-live")
		// Watch proc.Changes() across an observation window so an async cancellation shutdown cannot slip past.
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		for {
			state, why := hp.proc.Status()
			if state != host.StateRunning || why != "" || !hp.proc.Ready() {
				t.Fatalf("start %d exited after request ended: state=%q err=%q ready=%v", n, state, why, hp.proc.Ready())
			}
			select {
			case <-hp.proc.Changes():
			case <-timer.C:
				code, out := httpCall("POST", "/api/v1/extensions/example-live/-/ui/panel", `{}`)
				if code != http.StatusOK || !strings.Contains(fmt.Sprint(out["view"]), "live page") {
					t.Fatalf("follow-up UI call after start %d failed: %d %v", n, code, out)
				}
				return hp
			}
		}
	}

	assertRunning(1, "examplelive")

	// 1. Real HTTP contribution toggle off (net/http cancels r.Context() when ServeHTTP returns).
	if code, out := httpCall("PUT", "/api/v1/extensions/example-live/-/contributions/channel/examplelive", `{"enabled": false}`); code != http.StatusOK {
		t.Fatalf("contribution off: %d %v", code, out)
	}
	assertRunning(2, "")

	// 2. Real HTTP Settings › Connectors toggle on.
	if code, out := httpCall("PUT", "/api/v1/connectors/examplelive/settings", `{"enabled": true}`); code != http.StatusOK {
		t.Fatalf("connector settings on: %d %v", code, out)
	}
	assertRunning(3, "examplelive")

	// 3. Direct setConnectorEnabled with an immediately canceled request context.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	if err := h.setConnectorEnabled(reqCtx, "examplelive", false); err != nil {
		cancelReq()
		t.Fatal(err)
	}
	cancelReq()
	assertRunning(4, "")

	// 4. Real HTTP channel bind (switches connector on, restarts extension, calls login.step).
	if code, out := httpCall("POST", "/api/v1/channels/examplelive/bind", `{}`); code != http.StatusOK || out["hint"] != "from the program" || out["error"] != nil {
		t.Fatalf("channel bind: %d %v", code, out)
	}
	assertRunning(5, "examplelive")

	// 5. Direct connectorSwitched with an immediately canceled request context.
	reqCtx, cancelReq = context.WithCancel(context.Background())
	h.extensions.connectorSwitched(reqCtx, "examplelive", true)
	cancelReq()
	assertRunning(6, "examplelive")

	// 6. Direct roleChanged with an immediately canceled request context.
	reqCtx, cancelReq = context.WithCancel(context.Background())
	h.extensions.roleChanged(reqCtx)
	cancelReq()
	hp := assertRunning(7, "examplelive")

	// 7. Canceling the manager's own lifecycle context must still stop the restarted extension cleanly.
	stopMgr()
	waitLong(t, "restarted extension stops when manager context is canceled", func() bool {
		state, _ := hp.proc.Status()
		return state == host.StateStopped && !hp.proc.Ready()
	})
}

func TestExtensionRuntimeContextFallbackAndExplicitDisable(t *testing.T) {
	type ctxKey struct{}

	t.Run("manager context governs lifecycle", func(t *testing.T) {
		mgrCtx, stopMgr := context.WithCancel(context.Background())
		defer stopMgr()
		reqCtx, cancelReq := context.WithCancel(context.Background())

		ps := &extensions{ctx: mgrCtx}
		got := ps.runtimeContext(reqCtx)
		cancelReq()
		if got != mgrCtx || got.Err() != nil {
			t.Fatalf("runtimeContext followed canceled request instead of ps.ctx: err=%v", got.Err())
		}
		stopMgr()
		if got.Err() != context.Canceled {
			t.Fatalf("runtimeContext outlived canceled ps.ctx: err=%v", got.Err())
		}

		mgrCtx2, stopMgr2 := context.WithCancel(context.Background())
		defer stopMgr2()
		psFromManager := &extensions{h: &Manager{ctx: mgrCtx2}}
		reqCtx2, cancelReq2 := context.WithCancel(context.Background())
		got2 := psFromManager.runtimeContext(reqCtx2)
		cancelReq2()
		if got2 != mgrCtx2 || got2.Err() != nil {
			t.Fatalf("runtimeContext did not fall back to h.ctx: err=%v", got2.Err())
		}
		stopMgr2()
		if got2.Err() != context.Canceled {
			t.Fatalf("runtimeContext outlived canceled h.ctx: err=%v", got2.Err())
		}
	})

	t.Run("detached fallback without manager context", func(t *testing.T) {
		ps := &extensions{}
		reqCtx, cancelReq := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "keep"))
		got := ps.runtimeContext(reqCtx)
		cancelReq()
		if got.Err() != nil {
			t.Fatalf("fallback runtimeContext canceled with request: %v", got.Err())
		}
		if got.Value(ctxKey{}) != "keep" {
			t.Fatalf("fallback runtimeContext lost context value: %v", got.Value(ctxKey{}))
		}
		if bg := ps.runtimeContext(nil); bg == nil || bg.Err() != nil {
			t.Fatalf("runtimeContext(nil) returned %v", bg)
		}
	})

	t.Run("isolated apply survives canceled request and stops on disable", func(t *testing.T) {
		h, _ := extensionTestHub(t)
		mux := h.routes()
		master := h.cfg.Token
		log := filepath.Join(t.TempDir(), "active.log")
		installForTest(t, h, livePackage(t, log))

		waitLong(t, "initial start", func() bool {
			hp := h.extensions.get("example-live")
			return hp != nil && hp.proc != nil && hp.proc.Ready()
		})

		// Simulate a unit-test boundary where Run/startExtensions has not stored ps.ctx or h.ctx.
		h.extensions.mu.Lock()
		savedCtx := h.extensions.ctx
		h.extensions.ctx = nil
		h.extensions.mu.Unlock()
		h.ctx = nil
		t.Cleanup(func() {
			h.extensions.mu.Lock()
			h.extensions.ctx = savedCtx
			h.extensions.mu.Unlock()
		})

		reqCtx, cancelReq := context.WithCancel(context.Background())
		h.extensions.apply(reqCtx, "example-live")
		cancelReq()

		hp := h.extensions.get("example-live")
		waitLong(t, "restarted extension ready without manager ctx", func() bool {
			return hp != nil && hp.proc != nil && hp.proc.Ready()
		})
		state, why := hp.proc.Status()
		if state != host.StateRunning || why != "" {
			t.Fatalf("isolated apply exited after request cancel: state=%q err=%q", state, why)
		}
		if code, out := call(t, mux, "POST", "/api/v1/extensions/example-live/-/ui/panel", master, `{}`); code != http.StatusOK || !strings.Contains(fmt.Sprint(out["view"]), "live page") {
			t.Fatalf("UI call failed after isolated apply: %d %v", code, out)
		}

		// Explicit disable must still stop the process cleanly.
		if err := h.extensions.reg.SetEnabled(context.Background(), "example-live", false, "test"); err != nil {
			t.Fatal(err)
		}
		waitLong(t, "disabled extension stops", func() bool {
			cur := h.extensions.get("example-live")
			return cur != nil && cur.proc == nil && cur.status == extensionDisabled
		})
	})
}
