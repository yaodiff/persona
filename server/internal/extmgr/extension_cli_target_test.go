package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

const extensionTargetToken = "extension-target-test-token"

// targetFakeHub answers the CLI's preview and write calls without touching a real hub or packages. It counts every
// authenticated request so a refused target can prove that nothing was sent.
func targetFakeHub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extensionTargetToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/extensions/-/preview":
			_, _ = io.WriteString(w, `{"preview_id":"p1","permissions_digest":"d1","manifest":{"id":"example","name":"Example","version":"1.0.0"}}`)
		case r.URL.Path == "/api/v1/extensions/-/install", r.URL.Path == "/api/v1/extensions/example/-/upgrade":
			_, _ = io.WriteString(w, `{"id":"example","version":"1.0.0","status":"running"}`)
		case r.URL.Path == "/api/v1/extensions/example/-/auth/step":
			_, _ = io.WriteString(w, `{"done":true,"account":"example"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/-/") &&
			strings.HasPrefix(r.URL.Path, "/api/v1/extensions/"):
			_, _ = io.WriteString(w, `{"id":"example","config":{}}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func writeTargetHub(t *testing.T, home, listen string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]string{"listen": listen, "token": extensionTargetToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runTargetCommand(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := ExtensionCommand(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), err
}

func requireTarget(t *testing.T, out, home, hub string) {
	t.Helper()
	want := "PERSONA_HOME: " + home + "\nHub: " + hub + "\n"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("target prefix:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(out, extensionTargetToken) {
		t.Fatalf("target output exposed the hub token:\n%s", out)
	}
}

type targetWriteCase struct {
	name, stdin string
	args        []string
	matching    bool // dev needs a real project, so its matching path is covered by the command's own tests
}

func targetWriteCases() []targetWriteCase {
	return []targetWriteCase{
		{name: "install", args: []string{"install", "dir:/tmp/example", "--yes"}, matching: true},
		{name: "upgrade", args: []string{"upgrade", "example", "dir:/tmp/example", "--yes"}, matching: true},
		{name: "rollback", args: []string{"rollback", "example"}, matching: true},
		{name: "attach", args: []string{"attach", "example", "remote:rpc=/p.sock"}, matching: true},
		{name: "detach", args: []string{"detach", "example"}, matching: true},
		{name: "enable", args: []string{"enable", "example"}, matching: true},
		{name: "disable", args: []string{"disable", "example"}, matching: true},
		{name: "sandbox", args: []string{"sandbox", "example", "on"}, matching: true},
		{name: "auth login", args: []string{"auth", "example", "login"}, matching: true},
		{name: "auth logout", args: []string{"auth", "example", "logout"}, matching: true},
		{name: "config", args: []string{"config", "example", "a=1"}, matching: true},
		{name: "secret", stdin: "s3cret\n", args: []string{"secret", "example", "api_key"}, matching: true},
		{name: "uninstall", args: []string{"uninstall", "example"}, matching: true},
		{name: "auto-upgrade", args: []string{"auto-upgrade", "on"}, matching: true},
		{name: "contribution", args: []string{"contribution", "example", "connector/notes", "on"}, matching: true},
		{name: "market add", args: []string{"market", "add", "github:example/plugins"}, matching: true},
		{name: "market set-spec", args: []string{"market", "set-spec", "example", "github:example/moved"}, matching: true},
		{name: "market remove", args: []string{"market", "remove", "example"}, matching: true},
		{name: "market refresh", args: []string{"market", "refresh", "example"}, matching: true},
		{name: "market token", args: []string{"market", "token", "example", "--clear"}, matching: true},
		{name: "dev", args: []string{"dev", "/tmp/example"}},
	}
}

func TestExtensionTargetInheritedHome(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv("PERSONA_LISTEN", "")
	srv, requests := targetFakeHub(t)
	home := t.TempDir()
	writeTargetHub(t, home, strings.TrimPrefix(srv.URL, "http://"))
	otherHome := t.TempDir()
	t.Setenv("HOME", otherHome)
	writeTargetHub(t, filepath.Join(otherHome, ".persona"), strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("PERSONA_HOME", home)

	out, err := runTargetCommand(t, "", "install", "dir:/tmp/example", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	requireTarget(t, out, home, srv.URL)
	if strings.Contains(out, "Continue?") {
		t.Fatalf("--yes asked for confirmation:\n%s", out)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want preview + install", requests.Load())
	}
}

func TestExtensionTargetHomeOnly(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv("PERSONA_LISTEN", "")
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("JARVIS_HOME", "")
	srv, _ := targetFakeHub(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".persona")
	writeTargetHub(t, state, strings.TrimPrefix(srv.URL, "http://"))

	out, err := runTargetCommand(t, "", "install", "dir:/tmp/example", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	requireTarget(t, out, state, srv.URL)
}

func TestExtensionTargetLegacyHomeName(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv("PERSONA_LISTEN", "")
	t.Setenv("PERSONA_HOME", "")
	if err := os.Unsetenv("PERSONA_HOME"); err != nil {
		t.Fatal(err)
	}
	srv, _ := targetFakeHub(t)
	home := t.TempDir()
	t.Setenv("JARVIS_HOME", home)
	writeTargetHub(t, home, strings.TrimPrefix(srv.URL, "http://"))

	out, err := runTargetCommand(t, "", "install", "dir:/tmp/example", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	requireTarget(t, out, home, srv.URL)
}

func TestExtensionTargetMatchingFlags(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv("PERSONA_LISTEN", "")
	srv, requests := targetFakeHub(t)
	home := t.TempDir()
	writeTargetHub(t, home, strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("PERSONA_HOME", home)
	hub := strings.TrimPrefix(srv.URL, "http://")

	out, err := runTargetCommand(t, "", "install", "dir:/tmp/example", "--yes",
		"--expect-home", home, "--expect-hub="+hub)
	if err != nil {
		t.Fatal(err)
	}
	requireTarget(t, out, home, srv.URL)
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want preview + install", requests.Load())
	}
}

func TestExtensionTargetMismatchStopsEveryWrite(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv("PERSONA_LISTEN", "")
	srv, requests := targetFakeHub(t)
	home := t.TempDir()
	writeTargetHub(t, home, strings.TrimPrefix(srv.URL, "http://"))
	otherHome := t.TempDir()
	t.Setenv("HOME", otherHome)
	otherState := filepath.Join(otherHome, ".persona")
	writeTargetHub(t, otherState, strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("PERSONA_HOME", home)

	for _, tc := range targetWriteCases() {
		for _, mismatch := range []struct {
			name, flag, value string
		}{
			{"home", "--expect-home", otherState},
			{"hub", "--expect-hub", "127.0.0.1:1"},
		} {
			t.Run(tc.name+"/"+mismatch.name, func(t *testing.T) {
				requests.Store(0)
				before := targetDirNames(t, home)
				args := append(append([]string{}, tc.args...), mismatch.flag, mismatch.value)
				out, err := runTargetCommand(t, tc.stdin, args...)
				if err == nil || !strings.Contains(err.Error(), mismatch.value) {
					t.Fatalf("error = %v, want %s", err, mismatch.value)
				}
				if !strings.Contains(out, "PERSONA_HOME: "+home) || !strings.Contains(out, "Hub: "+srv.URL) {
					t.Fatalf("target line missing:\n%s", out)
				}
				if got := requests.Load(); got != 0 {
					t.Fatalf("requested the hub %d times after target mismatch", got)
				}
				if after := targetDirNames(t, home); !slices.Equal(after, before) {
					t.Fatalf("target mismatch changed %s: %v -> %v", home, before, after)
				}
			})
		}
	}
}

func targetDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestExtensionTargetFlagsOnWrites(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv("PERSONA_LISTEN", "")
	srv, _ := targetFakeHub(t)
	home := t.TempDir()
	writeTargetHub(t, home, strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("PERSONA_HOME", home)
	hub := strings.TrimPrefix(srv.URL, "http://")
	flags := []string{"--expect-home=" + home, "--expect-hub", hub}

	for _, tc := range targetWriteCases() {
		if !tc.matching {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			out, err := runTargetCommand(t, tc.stdin, append(append([]string{}, tc.args...), flags...)...)
			if err != nil {
				t.Fatal(err)
			}
			requireTarget(t, out, home, srv.URL)
		})
	}
}
