package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// codexDaemonHome is a Codex home whose 0.163 control alias points where Codex puts the daemon's socket (nothing is
// there: the test never makes it) and whose daemon.pid names pid.
func codexDaemonHome(t *testing.T, home string, pid int) {
	t.Helper()
	ctl := filepath.Join(home, ".codex", "app-server-control")
	if err := os.MkdirAll(ctl, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ctl, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, _ := filepath.EvalSymlinks(ctl)
	tmp, _ := filepath.EvalSymlinks("/tmp")
	sum := sha256.Sum256([]byte(filepath.Join(parent, "app-server-control.sock")))
	target := filepath.Join(tmp, "codex-daemon-"+strconv.Itoa(os.Geteuid()), hex.EncodeToString(sum[:]))
	if err := os.Symlink(target, filepath.Join(ctl, "app-server-control.sock")); err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(home, ".codex", "app-server-daemon")
	if err := os.MkdirAll(daemon, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemon, "daemon.pid"), fmt.Appendf(nil, `{"pid":%d,"processStartTime":"x"}`, pid), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A daemon that runs while its control socket is gone takes no new Codex: the start and the check say so and name
// the fix, instead of a minute's wait and the tail of the daemon's old log (2026-11-14). A stopped daemon (codex
// starts a new one), no daemon at all and a managed launcher (its own process) are left alone.
func TestCodexDaemonSocketGone(t *testing.T) {
	n, home := testNode(t)
	codex := filepath.Join(home, ".codex")
	if err := codexDaemonSocketGone(codex); err != nil {
		t.Fatalf("no daemon: %v", err)
	}
	codexDaemonHome(t, home, os.Getpid())
	err := codexDaemonSocketGone(codex)
	if err == nil || refusalCode(err) != "codex_daemon_socket_gone" || !strings.Contains(err.Error(), "codex app-server daemon restart") {
		t.Fatalf("running daemon without its socket: %v", err)
	}
	if c := n.launcherCheck(context.Background(), "codex"); c.OK || c.Agent != "codex" || !strings.Contains(c.Error, "codex app-server daemon restart") {
		t.Fatalf("check %+v", c)
	}
	if _, err := n.startCodex(context.Background(), nil, home, "codex", []string{"codex"}, "hi", "", "", ""); refusalCode(err) != "codex_daemon_socket_gone" {
		t.Fatalf("start: %v", err)
	}
	if n.codexOnSharedDaemon([]string{"codex", "-c", `model="x"`}) || n.codexOnSharedDaemon([]string{"codex", "--remote", "ws://127.0.0.1:1"}) {
		t.Fatal("a Codex in its own process or on its own app-server is not on the shared daemon")
	}
	n.bin = "/opt/persona/personad"
	if n.codexOnSharedDaemon([]string{n.bin, "launch", "gw"}) {
		t.Fatal("a managed launcher runs in its own process")
	}

	if err := os.WriteFile(filepath.Join(codex, "app-server-daemon", "daemon.pid"), []byte(`{"pid":2147483600}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := codexDaemonSocketGone(codex); err != nil {
		t.Fatalf("stopped daemon: %v", err)
	}
}
