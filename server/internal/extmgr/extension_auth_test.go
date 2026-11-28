package extmgr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
)

const testAuthExpiry = "2030-01-02T03:04:05Z"

// serveAuthExtensionForTest signs in with a device code: auth.step "" gives a link, the same step answers done once
// the file "approved" is in its data directory; its status says what its tool (hello-tool, from manifest.tools)
// printed, so the test sees the tool is on its PATH.
func serveAuthExtensionForTest() {
	data := os.Getenv("PERSONA_EXTENSION_DATA")
	if data == "" {
		data = os.Getenv("PERSONA_PLUGIN_DATA")
	}
	signedIn := func() bool { _, err := os.Stat(filepath.Join(data, "session")); return err == nil }
	var hub *pserve.Hub
	status := func() map[string]any {
		out, err := exec.Command("hello-tool").Output()
		msg := strings.TrimSpace(string(out))
		if err != nil {
			msg = "no tool: " + err.Error()
		}
		if signedIn() {
			return map[string]any{"state": "signed_in", "account": "me@example.com", "message": msg, "expires_at": testAuthExpiry}
		}
		if _, err := os.Stat(filepath.Join(data, "pending")); err == nil {
			return map[string]any{"state": "pending", "message": msg}
		}
		return map[string]any{"state": "signed_out", "message": msg}
	}
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-auth", Version: "1.0.0",
		Init: func(_ context.Context, _ extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			hub = h
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{
			"auth": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				var p struct {
					Step   string            `json:"step"`
					Values map[string]string `json:"values"`
				}
				_ = json.Unmarshal(params, &p)
				switch method {
				case "status":
					return status(), nil
				case "step":
					if p.Step == "" {
						_ = os.WriteFile(filepath.Join(data, "pending"), nil, 0o600)
						return map[string]any{"step": "wait", "kind": "device_code", "url": "https://login.example.com/device", "user_code": "ABCD-1234", "interval": 2}, nil
					}
					if _, err := os.Stat(filepath.Join(data, "approved")); err == nil {
						_ = os.Remove(filepath.Join(data, "pending"))
						_ = os.WriteFile(filepath.Join(data, "session"), []byte("secret-session"), 0o600)
						return map[string]any{"done": true, "account": "me@example.com"}, nil
					}
					return map[string]any{"step": "wait", "kind": "device_code", "url": "https://login.example.com/device", "user_code": "ABCD-1234", "interval": 2}, nil
				case "logout":
					_ = os.Remove(filepath.Join(data, "session"))
					_ = os.Remove(filepath.Join(data, "approved"))
					st := status()
					st["expires_at"] = nil
					return st, nil
				}
				return nil, pserve.UnknownMethod("auth." + method)
			},
			"ui": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				if method == "changed" { // the page's button: tell the hub the status changed
					return nil, hub.Notify("auth.changed", map[string]any{})
				}
				return map[string]any{"title": "登录", "sections": []any{map[string]any{"items": []any{map[string]any{"type": "text", "text": "x"}}}}}, nil
			},
		}})
}

func authToolArchive(t *testing.T) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho hello-tool 3.1.0\n"
	_ = tw.WriteHeader(&tar.Header{Name: "hello/bin/hello-tool", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func authPackage(t *testing.T, archive []byte) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "auth"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=auth exec "+self+" \"$@\"\n"), 0o755)
	sum := sha256.Sum256(archive)
	platform := runtime.GOOS + "-" + runtime.GOARCH
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-auth", "name": "Example Auth", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/auth"}}, "activation_events": ["on_startup"],
		"contributes": {"ui": [{"id": "login", "title": "登录", "placement": "plugin_page"}]},
		"tools": [{"name": "hello-tool", "title": "Hello", "version": "3.1.0", "check": {"min_version": "3.0.0"},
			"archive": {%q: {"url": "https://dl.example.com/hello-3.1.0.tar.gz", "sha256": %q, "path": "hello/bin/hello-tool"}}}],
		"auth": {"title": "示例登录", "description": "用示例账号登录", "page": "login"}}`, platform, platform, hex.EncodeToString(sum[:]))
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// An extension with a tool and a sign-in, installed as an operation: the tools step installs the program the hub
// then puts on its PATH; the install's next step is signing in; the apps see the status, sign in with a device
// code (its link drawn as a QR code too), get extension_auth, and sign out.
func TestExtensionToolsAndAuth(t *testing.T) {
	archive := authToolArchive(t)
	oldFetch, oldLook := extension.Fetch, extension.LookTool
	t.Cleanup(func() { extension.Fetch, extension.LookTool = oldFetch, oldLook })
	extension.LookTool = func(string, string) string { return "" }
	extension.Fetch = func(_ context.Context, u string, w io.Writer) error {
		if u != "https://dl.example.com/hello-3.1.0.tar.gz" {
			return errors.New("HTTP 404")
		}
		_, err := w.Write(archive)
		return err
	}
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	_, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+authPackage(t, archive)+`"}`)
	if shown := fmt.Sprint(pv["permissions"]); !strings.Contains(shown, "安装命令行程序") || !strings.Contains(shown, "Hello（hello-tool） 3.1.0（dl.example.com）") {
		t.Fatalf("the tools are agreed to: %v", shown)
	}
	code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q, "async": true}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 202 {
		t.Fatalf("%d %v", code, out)
	}
	opID := out["operation"].(map[string]any)["id"].(string)
	var op map[string]any
	waitLong(t, "the install to end", func() bool {
		_, op = call(t, mux, "GET", "/api/v1/extensions/-/operations/"+opID, master, "")
		return op["state"] == "succeeded" || op["state"] == "failed"
	})
	if op["state"] != "succeeded" || op["next"] != "auth" {
		t.Fatalf("installed, signing in next: %v", op)
	}
	var tools map[string]any
	for _, s := range op["steps"].([]any) {
		if st := s.(map[string]any); st["id"] == "tools" {
			tools = st
		}
	}
	if tools == nil || tools["state"] != "done" || fmt.Sprint(tools["tools"]) != "[map[name:hello-tool state:done]]" {
		t.Fatalf("the tools step: %v", tools)
	}

	var auth map[string]any
	waitLong(t, "its status", func() bool {
		_, auth = call(t, mux, "GET", "/api/v1/extensions/example-auth/-/auth", master, "")
		return auth["state"] == "signed_out"
	})
	if auth["message"] != "hello-tool 3.1.0" || auth["title"] != "示例登录" || auth["page"] != "login" {
		t.Fatalf("its tool runs from its PATH: %v", auth)
	}
	if _, ok := auth["expires_at"]; ok {
		t.Fatalf("signed-out status should omit an absent expiration: %v", auth)
	}
	_, detail := call(t, mux, "GET", "/api/v1/extensions/example-auth/-/", master, "")
	if a, _ := detail["auth"].(map[string]any); a["state"] != "signed_out" || !strings.Contains(fmt.Sprint(detail["warnings"]), "还没登录（示例登录）") {
		t.Fatalf("the detail says so, for old apps too: %v %v", detail["auth"], detail["warnings"])
	}
	if fmt.Sprint(detail["tools"]) != "[map[name:hello-tool title:Hello version:3.1.0]]" {
		t.Fatalf("%v", detail["tools"])
	}

	_, step := call(t, mux, "POST", "/api/v1/extensions/example-auth/-/auth/step", master, `{"step": ""}`)
	if step["kind"] != "device_code" || step["user_code"] != "ABCD-1234" || step["qr"] == nil {
		t.Fatalf("a device code with its link's QR code: %v", step)
	}
	_, step = call(t, mux, "POST", "/api/v1/extensions/example-auth/-/auth/step", master, `{"step": "wait"}`)
	if step["done"] == true {
		t.Fatal("not approved yet")
	}
	_ = os.WriteFile(filepath.Join(h.extensions.reg.DataDir("example-auth"), "approved"), nil, 0o600)
	_, step = call(t, mux, "POST", "/api/v1/extensions/example-auth/-/auth/step", master, `{"step": "wait"}`)
	if step["done"] != true || step["account"] != "me@example.com" {
		t.Fatalf("%v", step)
	}
	_, auth = call(t, mux, "GET", "/api/v1/extensions/example-auth/-/auth", master, "")
	if auth["state"] != "signed_in" || auth["account"] != "me@example.com" || auth["expires_at"] != testAuthExpiry {
		t.Fatalf("%v", auth)
	}
	waitLong(t, "extension_auth signed_in", func() bool {
		return strings.Contains(fmt.Sprint(hubOf(h).broadcastsOf("extension_auth")), "state:signed_in")
	})
	if all := fmt.Sprint(hubOf(h).broadcastsOf("extension_auth")); strings.Contains(all, "secret-session") || !strings.Contains(all, "extension:example-auth") {
		t.Fatal("what signing in yields never leaves the extension:", all)
	}
	_, auth = call(t, mux, "POST", "/api/v1/extensions/example-auth/-/auth/logout", master, "")
	if auth["state"] != "signed_out" {
		t.Fatalf("%v", auth)
	}
	if _, ok := auth["expires_at"]; ok {
		t.Fatalf("logout should omit a null expiration: %v", auth)
	}
	if code, out := call(t, mux, "GET", "/api/v1/extensions/nope/-/auth", master, ""); code != 404 {
		t.Fatalf("%d %v", code, out)
	}

	// Uninstalled: its tools go with it.
	toolsDir := h.extensions.reg.ToolsDir("example-auth")
	if _, err := os.Stat(toolsDir); err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, mux, "DELETE", "/api/v1/extensions/example-auth/-/", master, ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if _, err := os.Stat(toolsDir); !os.IsNotExist(err) {
		t.Fatal("tools left behind", err)
	}
}
