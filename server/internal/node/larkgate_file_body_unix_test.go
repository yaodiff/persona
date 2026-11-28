//go:build !windows

package node

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadBodyFileRejectsFIFOAndResolvedVirtualFS(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "body.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		done <- readBodyFile(fifo, dir)
	}()
	select {
	case got := <-done:
		if !strings.HasPrefix(got, "（读不到 ") {
			t.Fatalf("FIFO read = %q, want unreadable marker", got)
		}
	case <-time.After(2 * time.Second):
		// Unblock any goroutine still stuck in open(O_RDONLY) so the test binary can exit cleanly.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("readBodyFile blocked on FIFO")
	}

	if _, err := os.Stat("/proc/self/environ"); err == nil {
		linkDir := filepath.Join(dir, "procdir")
		if err := os.Symlink("/proc/self", linkDir); err != nil {
			t.Fatalf("symlink /proc/self: %v", err)
		}
		if got := readBodyFile(filepath.Join(linkDir, "environ"), dir); !strings.HasPrefix(got, "（读不到 ") {
			t.Fatalf("symlinked /proc parent = %q, want unreadable marker", got)
		}
	}

	// --url-query @fifo must also fail immediately without blocking.
	qDone := make(chan string, 1)
	go func() {
		v := classifyLark(`ex GET /health --url-query @`+fifo, olGate(""), dir, nil)
		qDone <- v.deny
	}()
	select {
	case deny := <-qDone:
		if deny == "" {
			t.Fatal("--url-query @fifo was not denied")
		}
	case <-time.After(2 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("--url-query blocked on FIFO")
	}
}
