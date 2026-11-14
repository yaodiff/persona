//go:build !windows

package node

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
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
	waitFor(t, "the installer child to start", func() bool {
		data, _ := os.ReadFile(childPath)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	})
	if child <= 0 {
		t.Fatal("installer did not start its child")
	}
	if !cancelJob() {
		t.Fatal("running install was not cancellable")
	}
	cancelled := false
	waitFor(t, "the install job to cancel", func() bool {
		if j := currentJob(); j != nil && j.State == "cancelled" {
			cancelled = true
			return true
		}
		return false
	})
	if !cancelled {
		// Clean up the descendant that the production cancellation left behind.
		killChild()
		waitFor(t, "the installer job to remain cancelled", func() bool {
			return currentJob() != nil && currentJob().State == "cancelled"
		})
		t.Fatal("install job remained running until the installer shell's child was killed separately")
	}
}
