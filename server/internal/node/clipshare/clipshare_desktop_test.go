package clipshare

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// fakeLinuxTools puts fake wl-paste / wl-copy / xclip in a directory: types and content are files (the clipboard),
// and env is the session's environment.
func fakeLinuxTools(t *testing.T, env map[string]string, tools ...string) (dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir = t.TempDir()
	types, content := filepath.Join(dir, "types"), filepath.Join(dir, "content")
	scripts := map[string]string{
		"wl-paste": "case \"$1\" in\n  --list-types) [ -f '" + types + "' ] && cat '" + types + "' || { echo 'No selection' >&2; exit 1; } ;;\n" +
			"  *) cat '" + content + "' ;;\nesac\n",
		"wl-copy": "echo \"$2\" > '" + types + "'; cat > '" + content + "'\n",
		"xclip": "case \"$3\" in\n  -o) if [ \"$5\" = TARGETS ]; then [ -f '" + types + "' ] && cat '" + types + "' || exit 1; else cat '" + content + "'; fi ;;\n" +
			"  -i) echo \"$5\" > '" + types + "'; cat > '" + content + "' ;;\nesac\n",
	}
	for _, name := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+scripts[name]), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldEnv, oldLook, oldOS := clipGetenv, clipLookPath, installOS
	clipGetenv = func(k string) string { return env[k] }
	clipLookPath = func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			return "", exec.ErrNotFound
		}
		return p, nil
	}
	installOS = "linux"
	t.Cleanup(func() { clipGetenv, clipLookPath, installOS = oldEnv, oldLook, oldOS })
	return dir
}

// No graphical session, or no tool for it: unsupported, with a reason the app can show.
func TestLinuxClipSupport(t *testing.T) {
	fakeLinuxTools(t, nil, "xclip")
	if s := Support(); s.Supported || s.Reason != proto.ClipboardReasonNoDisplay {
		t.Fatalf("headless: %+v", s)
	}
	_, err := Set(context.Background(), json.RawMessage(`{"text":"x"}`))
	if rpcCode(err) != proto.CodeClipboardUnsupported || err.Error() == "" {
		t.Fatalf("set headless: %v", err)
	}
	fakeLinuxTools(t, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})
	if s := Support(); s.Supported || s.Reason != proto.ClipboardReasonNoTool {
		t.Fatalf("no tool: %+v", s)
	}
	// Wayland without wl-clipboard but with XWayland's DISPLAY and xclip: xclip.
	fakeLinuxTools(t, map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, "xclip")
	if b, ok := clipboardBackend().(linuxClip); !ok || b.wayland {
		t.Fatalf("xwayland: %#v", clipboardBackend())
	}
}

func TestLinuxClipRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		tools []string
	}{
		{"wayland", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, []string{"wl-paste", "wl-copy"}},
		{"x11", map[string]string{"DISPLAY": ":0"}, []string{"xclip"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeLinuxTools(t, tc.env, tc.tools...)
			if c := getClip(t); c.Kind != proto.ClipboardKindEmpty {
				t.Fatalf("empty: %+v", c)
			}
			if _, err := Set(context.Background(), json.RawMessage(`{"text":"从手机来的 · ok"}`)); err != nil {
				t.Fatal(err)
			}
			if c := getClip(t); c.Kind != proto.ClipboardKindText || *c.Text != "从手机来的 · ok" {
				t.Fatalf("text: %+v", c)
			}
			if _, err := Set(context.Background(), mustJSON(proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/png", Data: tinyPNG}})); err != nil {
				t.Fatal(err)
			}
			if c := getClip(t); c.Kind != proto.ClipboardKindImage || string(c.Image.Data) != string(tinyPNG) {
				t.Fatalf("image: %+v", c)
			}
			_ = os.WriteFile(filepath.Join(dir, "types"), []byte("text/plain\nx-kde-passwordManagerHint\n"), 0o600)
			if c := getClip(t); c.Kind != proto.ClipboardKindConcealed || c.Text != nil {
				t.Fatalf("concealed: %+v", c)
			}
		})
	}
}
