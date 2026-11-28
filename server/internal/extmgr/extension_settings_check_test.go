package extmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/egress"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

// runtimeCheckFetch is the source guest's bounded broker call in this test binary. The socket fixes the extension;
// only the check's credential accompanies this one request, and an empty proxy models an ordinary fetch.
func runtimeCheckFetch(ctx context.Context, socket, proxy, target string) (int, error) {
	raw, _ := json.Marshal(map[string]any{"url": target, "method": http.MethodGet, "headers": map[string]string{}, "body": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://source"+egress.RuntimeFetchPath, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.User == nil {
			return 0, fmt.Errorf("invalid check proxy %q", proxy)
		}
		cred, _ := u.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(egress.User+":"+cred)))
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// serveSettingsCheckExtensionForTest answers settings.check as a gateway extension would (#5906): a key with "echo"
// in it is refused with the key in the words (whole and cut short), "boom" as an RPC error quoting it, "slow" not in
// time; anything else works. The check's proxy (#8459) goes to the file CHECK_PROXY_OUT names, when it names one,
// and a gateway_url with "refuse" in it is refused with the proxy in the words.
func serveSettingsCheckExtensionForTest() {
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-gateway", Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{
			"settings": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				if method != "check" {
					return nil, pserve.UnknownMethod(method)
				}
				var in struct {
					Config  map[string]any    `json:"config"`
					Secrets map[string]string `json:"secrets"`
					Proxy   string            `json:"proxy"`
				}
				_ = json.Unmarshal(params, &in)
				if out := os.Getenv("CHECK_PROXY_OUT"); out != "" {
					_ = os.WriteFile(out, []byte(in.Proxy), 0o600)
				}
				if u, _ := in.Config["gateway_url"].(string); strings.Contains(u, "refuse") {
					return map[string]any{"ok": false, "error": "unreachable through " + in.Proxy}, nil
				}
				key := in.Secrets["key"]
				if socket, _ := in.Config["broker_socket"].(string); socket != "" {
					gateway, _ := in.Config["gateway_url"].(string)
					status, err := runtimeCheckFetch(context.Background(), socket, in.Proxy, gateway)
					if err != nil {
						return map[string]any{"ok": false, "error": "candidate fetch failed: " + err.Error()}, nil
					}
					if status != http.StatusOK {
						return map[string]any{"ok": false, "error": fmt.Sprintf("candidate returned %d for %s through %s", status, key, in.Proxy)}, nil
					}
				}
				switch {
				case strings.Contains(key, "echo"):
					return map[string]any{"ok": false, "error": "网关不认这把 Key " + key + "（前缀 " + key[:10] + "，后缀 " + key[len(key)-8:] + "）"}, nil
				case strings.Contains(key, "boom"):
					return nil, &rpc.Error{Code: "internal", Message: "crashed reading " + key}
				case strings.Contains(key, "slow"):
					time.Sleep(3 * time.Second)
				}
				return map[string]any{"ok": true}, nil
			}}})
}

func settingsCheckPackage(t *testing.T) string {
	return settingsCheckPackageWith(t, `{"name": "key", "title": "Key", "hint": "last4"}`)
}

func settingsCheckPackageWith(t *testing.T, secret string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "gw"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=settingscheck exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-gateway", "name": "Example Gateway", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/gw"}}, "activation_events": ["on_startup"],
		"contributes": {"ui": [{"id": "page", "title": "Gateway", "placement": "nav_page"}]},
		"configuration": {"type": "object", "properties": {"gateway_url": {"type": "string", "title": "网关地址", "format": "uri"}}},
		"secrets": [%s], "settings_check": true,
		"permissions": {"network": ["config:gateway_url"]}}`, runtime.GOOS+"-"+runtime.GOARCH, secret)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A secret is checked by the extension before it is saved: a refusal answers check_failed with the extension's words,
// the key taken out of them (whole and its long prefixes / suffixes), saves nothing; a check with no answer in time
// fails; too many checks are refused before reaching the extension; a key that works is kept and shown by its end.
func TestSettingsCheckRoutes(t *testing.T) {
	setVarFor(t, &settingsCheckTimeout, 500*time.Millisecond)
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+settingsCheckPackage(t)+`"}`)
	if !strings.Contains(fmt.Sprint(pv["permissions"]), "当前：未填") && !strings.Contains(fmt.Sprint(pv["permissions"]), "not set") {
		t.Fatalf("the consent names the address in the settings: %v", pv["permissions"])
	}
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("example-gateway")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	put := func(value string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"value": value})
		return call(t, mux, "PUT", "/api/v1/extensions/example-gateway/-/secrets/key", master, string(b))
	}
	key := "ag-echo-key-0123456789-zyxw"
	code, out := put(key)
	msg, _ := out["error"].(string)
	if code != 400 || out["code"] != "check_failed" || !strings.Contains(msg, "网关不认这把 Key") {
		t.Fatalf("refused: %d %v", code, out)
	}
	for _, part := range []string{key, key[:10], key[len(key)-8:]} {
		if strings.Contains(msg, part) {
			t.Fatalf("the refusal quotes the key (%q): %q", part, msg)
		}
	}
	if set := h.extensions.reg.SecretsSet(context.Background(), h.extensions.reg.Get("example-gateway")); set["key"] {
		t.Fatal("a refused key was saved")
	}
	if code, out := put("ag-boom-key-0123456789"); code != 400 || strings.Contains(fmt.Sprint(out), "ag-boom-key-0123456789") {
		t.Fatalf("an RPC error quoting the key: %d %v", code, out)
	}
	if code, out := put("ag-slow-key-0123456789"); code != 400 || out["code"] != "check_failed" {
		t.Fatalf("no answer in time: %d %v", code, out)
	}
	if code, out := put("ag-good-key-0123456789-7f3a"); code != 200 {
		t.Fatalf("a key that works: %d %v", code, out)
	} else if s := fmt.Sprint(out["secrets"]); !strings.Contains(s, "hint:7f3a") || strings.Contains(s, "ag-good") {
		t.Fatalf("shown by its last four characters only: %s", s)
	}
	// Six checks a minute (four so far): the next two reach it, the one after is refused before it.
	put("ag-good-key-0123456789-0001")
	put("ag-good-key-0123456789-0002")
	if code, out := put("ag-good-key-0123456789-0003"); code != 429 || out["code"] != "check_rate_limited" {
		t.Fatalf("too many checks: %d %v", code, out)
	}
}

// An extension waiting for a required key has no program to check it: the first key goes in unchecked and starts it,
// and the ones after are checked by it (#6099).
func TestSettingsCheckWhileNeedsSetup(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+settingsCheckPackageWith(t, `{"name": "key", "title": "Key", "hint": "last4", "required": true}`)+`"}`)
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "waiting for its key", func() bool {
		hp := h.extensions.get("example-gateway")
		return hp != nil && hp.proc == nil && hp.status == extensionNeedsSetup
	})
	put := func(value string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"value": value})
		return call(t, mux, "PUT", "/api/v1/extensions/example-gateway/-/secrets/key", master, string(b))
	}
	if code, out := put("ag-good-key-0123456789-7f3a"); code != 200 {
		t.Fatalf("the first key: %d %v", code, out)
	}
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("example-gateway")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	if code, out := put("ag-echo-key-0123456789-zyxw"); code != 400 || out["code"] != "check_failed" {
		t.Fatalf("the next key is checked: %d %v", code, out)
	}
}

// An extension has no way to change its own settings: what its program asks of the hub cannot write them, so the
// address a "config:" entry lets it reach is only ever the user's (#5906).
func TestExtensionCannotWriteItsSettings(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+settingsCheckPackage(t)+`"}`)
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	p := h.extensions.reg.Get("example-gateway")
	if p == nil {
		t.Fatal("not installed")
	}
	if err := h.extensions.reg.SetConfig(context.Background(), p.Record.ID, json.RawMessage(`{"gateway_url":"https://gateway.example.com"}`), "app"); err != nil {
		t.Fatal(err)
	}
	before := string(h.extensions.reg.Config(p))
	s, err := h.extensions.spec(context.Background(), p, &hubExtension{})
	if err != nil {
		t.Fatal(err)
	}
	evil := json.RawMessage(`{"config":{"gateway_url":"https://evil.example.org"},"gateway_url":"https://evil.example.org"}`)
	for _, method := range []string{"config.set", "config.update", "settings.set", "settings.save", "extension.config", "extension.set_config",
		"plugin.config", "configuration.set", "hub.config", "secrets.set"} {
		_, err := s.Handle(context.Background(), method, evil)
		var re *rpc.Error
		if err == nil || !(asRPC(err, &re) && re.Code == extension.CodeUnknownMethod) {
			t.Errorf("%s: %v", method, err)
		}
	}
	if after := string(h.extensions.reg.Config(p)); after != before {
		t.Fatalf("the settings changed: %s → %s", before, after)
	}
	if ts := h.extensions.settingsTargets(p.Record.ID); len(ts) != 1 || ts[0].Host != "gateway.example.com" || ts[0].Port != "443" {
		t.Fatalf("targets %+v", ts)
	}
}

func asRPC(err error, re **rpc.Error) bool {
	e, ok := err.(*rpc.Error)
	if ok {
		*re = e
	}
	return ok
}

// scrubSecrets takes a secret out whole and by its long prefixes and suffixes; short ones and other words stay.
func TestScrubSecrets(t *testing.T) {
	secrets := map[string]string{"key": "sk-live-0123456789abcdef", "short": "abc"}
	got := scrubSecrets("bad key sk-live-0123456789abcdef; starts sk-live-0123 ends 89abcdef; abc stays", secrets)
	for _, part := range []string{"sk-live-0123456789abcdef", "sk-live-0123", "89abcdef"} {
		if strings.Contains(got, part) {
			t.Fatalf("%q left %q", got, part)
		}
	}
	if !strings.Contains(got, "abc stays") || !strings.Contains(got, "bad key ***") {
		t.Fatal(got)
	}
	if long := scrubSecrets(strings.Repeat("字", 400), nil); len([]rune(long)) != settingsCheckMessageMost+1 {
		t.Fatalf("cut to %d", len([]rune(long)))
	}
}

func TestCheckLimiter(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l := checkLimiter{now: func() time.Time { return now }}
	for i := 0; i < settingsChecksMost; i++ {
		if !l.allow("a") {
			t.Fatalf("check %d refused", i)
		}
	}
	if l.allow("a") {
		t.Fatal("one too many")
	}
	if !l.allow("b") {
		t.Fatal("another extension has its own count")
	}
	now = now.Add(settingsCheckWindow)
	if !l.allow("a") {
		t.Fatal("a new window")
	}
}

// setVarFor sets *v for the test.
func setVarFor[T any](t *testing.T, v *T, to T) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// An extension's page may open its own settings (persona://settings/extensions/<id>), not another's.
func TestOpenOwnSettings(t *testing.T) {
	page := extension.UIItem{ID: "page", Placement: "nav_page"}
	if err := checkOpenURL("example-gateway", nil, page, "", extension.SettingsURL("example-gateway")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{extension.SettingsURL("example-notes"), "persona://settings/extensions/example-gateway/../example-notes", "persona://settings"} {
		if err := checkOpenURL("example-gateway", nil, page, "", bad); err == nil {
			t.Errorf("%s opened", bad)
		}
	}
}
