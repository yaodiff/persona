//go:build !windows

package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadShellProxyMacBashProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS login bash uses Directory Services (macLoginShell) and -lic")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "SHELL", "ZDOTDIR", "ENV", "BASH_ENV")
	oldMac := macLoginShell
	t.Cleanup(func() { macLoginShell = oldMac })
	macLoginShell = func() string { return "bash" }

	// Even with a ~/.zshrc present, a macOS account whose Directory Services shell is bash runs bash -lic and reads
	// ~/.bash_profile (#16217).
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export HTTPS_PROXY=http://127.0.0.1:9999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("export HTTPS_PROXY=http://127.0.0.1:7899\nexport NO_PROXY=.corp.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := readShellProxy(context.Background())
	want := shellProxy{
		HTTPSProxy: "http://127.0.0.1:7899",
		NoProxy:    ".corp.example.com",
	}
	if got != want {
		t.Fatalf("readShellProxy(macOS bash_profile) =\n  %+v\nwant\n  %+v", got, want)
	}
}
