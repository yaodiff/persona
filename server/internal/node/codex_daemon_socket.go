package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/osproc"
)

// A shared app-server daemon (0.163 and later) that is still running but whose control socket is gone takes no new
// client: every plain `codex` then exits with "failed to connect to …/app-server-control.sock: No such file or
// directory", printing the tail of the daemon's own log first (old lines, other ports), and a start from the phone
// waits its full minute for a thread that never comes. The socket lives in /tmp (codex-daemon-<uid>), where a cleanup
// can remove it under a daemon that runs for weeks (2026-11-14). The daemon keeps its existing clients, so a
// `codex exec` check and the node's own connection still work; only new TUIs fail.

// codexOnSharedDaemon: a start with argv runs its thread on the shared daemon (plain codex or an alias without the
// flags that keep it in its own process, and not on an app-server of its own). A managed launcher (personad launch)
// passes its provider with -c and runs in its own process.
func (n *Node) codexOnSharedDaemon(argv []string) bool {
	return len(argv) > 0 && argv[0] != n.bin && codexOwnProcess(argv) == "" && !codexOnRemote(argv)
}

// codexDaemonSocketGone says, for a Codex home, whether its daemon runs (daemon.pid names a live process of this
// user) while the socket its control alias points to is missing; the error then names the fix. nil otherwise: no
// daemon (0.153, a stopped one: codex starts a new one), an alias that resolves, or nothing to tell.
func codexDaemonSocketGone(home string) error {
	if home == "" {
		return nil
	}
	alias := filepath.Join(home, "app-server-control", "app-server-control.sock")
	st, err := os.Lstat(alias)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return nil // no alias: no 0.163 daemon here, or a plain socket whose absence codex reports itself
	}
	target, err := codexSocketPath(alias)
	if err != nil {
		return nil // not the alias codex makes: not ours to judge
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(home, "app-server-daemon", "daemon.pid"))
	if err != nil {
		return nil
	}
	var pid struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &pid) != nil || pid.PID <= 0 || !osproc.AliveMine(pid.PID) {
		return nil // no daemon running: codex starts one and makes the socket again
	}
	return refusal("codex_daemon_socket_gone",
		"这台电脑上 Codex 的共享 app-server 还在运行，但它的控制 socket（/tmp/codex-daemon-*）已经不在了（多半是 /tmp 被清理过），新开的 Codex 都连不上它。"+
			"在这台电脑上运行 `codex app-server daemon restart` 后再试（会断开挂在它上面的 Codex 会话，会话记录还在，可以恢复）。", nil)
}
