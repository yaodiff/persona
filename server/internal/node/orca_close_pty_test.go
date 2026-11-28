//go:build darwin || linux

package node

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/creack/pty"
)

func TestOrcaCloseInspectsRealMarkedShell(t *testing.T) {
	const pane = "fixture-tab:fixture-leaf"
	const original = "orca:" + pane
	cmd := exec.Command("/bin/sh", "-i")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + cmd.Dir,
		"PERSONA_AGENT=1", orcaPaneEnv + "=" + pane}
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = terminal.Close()
		_ = cmd.Wait()
	})
	cwd, err := filepath.EvalSymlinks(cmd.Dir)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the marked shell in the foreground", func() bool {
		pid := cmd.Process.Pid
		owner := orcaTerminalOwner{Pane: original, RootPID: pid, RootStart: procStartMark(pid)}
		got, ok := inspectOrcaCloseShell(t.Context(), owner, 0)
		return ok && got.pid == cmd.Process.Pid && got.start != "" && got.cwd == cwd
	})
}
