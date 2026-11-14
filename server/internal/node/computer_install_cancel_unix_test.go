//go:build !windows

package node

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallCuaCancellationKillsInstallerTree(t *testing.T) {
	home := t.TempDir()
	childPath := filepath.Join(home, "child-pid")
	fakeInstaller(t, home, map[string]string{
		"install.sh": "#!/bin/bash\nsleep 10 &\necho $! > " + strconv.Quote(childPath) + "\nwait\n",
	})
	var child int
	killChild := func() {
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	}
	t.Cleanup(killChild)
	if err := startJob("install", installCua); err != nil {
		t.Fatal(err)
	}
	if !holdsWithin(3*time.Second, func() bool {
		data, _ := os.ReadFile(childPath)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	}) {
		t.Fatal("installer did not start its child")
	}
	if !cancelJob() {
		t.Fatal("running install was not cancellable")
	}
	cancelled := func() bool { j := currentJob(); return j != nil && j.State == "cancelled" }
	if !holdsWithin(750*time.Millisecond, cancelled) {
		// Clean up the descendant that the production cancellation left behind.
		killChild()
		holdsWithin(2*time.Second, cancelled)
		t.Fatal("install job remained running until the installer shell's child was killed separately")
	}
}

// holdsWithin reports whether ok holds within d, checking it every 10 ms.
func holdsWithin(d time.Duration, ok func() bool) bool {
	deadline := time.After(d)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !ok() {
		select {
		case <-deadline:
			return ok()
		case <-tick.C:
		}
	}
	return true
}
