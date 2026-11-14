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
	for deadline := time.Now().Add(3 * time.Second); child == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		data, _ := os.ReadFile(childPath)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if child <= 0 {
		t.Fatal("installer did not start its child")
	}
	if !cancelJob() {
		t.Fatal("running install was not cancellable")
	}
	cancelled := false
	for deadline := time.Now().Add(750 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if j := currentJob(); j != nil && j.State == "cancelled" {
			cancelled = true
			break
		}
	}
	if !cancelled {
		// Clean up the descendant that the production cancellation left behind.
		killChild()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if j := currentJob(); j != nil && j.State == "cancelled" {
				break
			}
		}
		t.Fatal("install job remained running until the installer shell's child was killed separately")
	}
}
