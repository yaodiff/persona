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

	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/quota"
)

// serveUsageExtensionForTest is a usage plugin: it reads the gateway's /v1/usage with the key placeholder in its
// Authorization and answers with one row of its own; the row's headline says what the plugin saw of the key.
func serveUsageExtensionForTest() {
	var hub *pserve.Hub
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-usage", Version: "1.0.0",
		Init: func(_ context.Context, _ extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			hub = h
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{"usage": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "query" {
				return nil, pserve.UnknownMethod(method)
			}
			var p struct {
				QueryID string `json:"query_id"`
			}
			_ = json.Unmarshal(params, &p)
			var probe quota.Response
			stolen := hub.Call(ctx, "usage.http", map[string]any{"query_id": "not-mine", "url": "/v1/usage"}, &probe)
			var resp quota.Response
			if err := hub.Call(ctx, "usage.http", map[string]any{"query_id": p.QueryID, "method": "GET", "url": "/v1/usage",
				"headers": map[string]string{"Authorization": "Bearer " + quota.KeyPlaceholder}}, &resp); err != nil {
				return nil, err
			}
			return map[string]any{"accounts": []map[string]any{{"provider": "claude", "name": "Pool", "headline": fmt.Sprintf("stolen=%v body=%s", stolen != nil, resp.Body),
				"windows": []map[string]any{{"label": "5h", "used_percent": 40, "group": "5h", "title": "Pool"}}}}}, nil
		}}})
}

func usagePackage(t *testing.T, hosts string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "usage"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=usage exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-usage", "name": "Example usage", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/usage"}},
		"contributes": {"usage": [{"id": "example-pool", "name": "Example pool", "hosts": %s}]},
		"permissions": {"launcher_keys": true}}`, runtime.GOOS+"-"+runtime.GOARCH, hosts)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A usage extension reads a launcher's gateway without seeing its key: the manager sends the requests with the key put
// in, only while the query runs and only for the extension it asked. A gateway the extension's hosts do not name is not
// asked at all.
func TestUsageThroughAnExtension(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	const key = "sk-example-launcher-key"
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer gw.Close()
	b1Launcher(h, "pool", gw.URL, key)

	if _, err := h.UsageQuery(context.Background(), "example-usage", "pool"); err == nil {
		t.Fatal("not installed yet")
	}
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+usagePackage(t, `["127.0.0.1"]`)+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	raw, err := h.UsageQuery(context.Background(), "example-usage", "pool")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, key) || !strings.Contains(s, "stolen=true") || !strings.Contains(s, `ok`) || !strings.Contains(s, `"group":"5h"`) {
		t.Fatalf("answer %s", s)
	}
	if _, err := h.UsageQuery(context.Background(), "example-usage", "no-such-launcher"); err == nil {
		t.Fatal("a launcher that does not exist")
	}
}

// The extension's hosts limit which gateways it is asked about.
func TestUsageHostsLimitTheGateways(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	gw := httptest.NewServer(http.NotFoundHandler())
	defer gw.Close()
	b1Launcher(h, "pool", gw.URL, "sk-example")
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+usagePackage(t, `["pool.example.com"]`)+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if _, err := h.UsageQuery(context.Background(), "example-usage", "pool"); err == nil || !strings.Contains(err.Error(), "does not read") {
		t.Fatalf("%v", err)
	}
}

func TestDirectUsageKeyFollowsHTTPSOrigin(t *testing.T) {
	m := &extension.Manifest{Contributes: map[string]json.RawMessage{
		"usage": json.RawMessage(`[{"id":"direct","name":"Direct","direct":true,"remote_address":"address","remote_key":"read_key"}]`),
	}}
	p := &extension.Extension{Full: m}
	first := json.RawMessage(`{"address":"https://one.example.invalid/prefix"}`)
	same := json.RawMessage(`{"address":"https://one.example.invalid:443/other"}`)
	other := json.RawMessage(`{"address":"https://two.example.invalid"}`)
	local := json.RawMessage(`{"address":"http://127.0.0.1:3425"}`)
	if keys := directUsageKeysToClear(p, first, same); len(keys) != 0 {
		t.Fatalf("same HTTPS origin cleared the key: %v", keys)
	}
	for _, cfg := range []json.RawMessage{other, local, json.RawMessage(`{}`)} {
		if keys := directUsageKeysToClear(p, first, cfg); len(keys) != 1 || keys[0] != "read_key" {
			t.Fatalf("changed or missing HTTPS origin did not clear the key: %v", keys)
		}
	}
	if directUsageKeyNeedsURL(p, "read_key", local) == nil || directUsageKeyNeedsURL(p, "read_key", first) != nil {
		t.Fatal("read key was not restricted to a saved HTTPS origin")
	}
}
