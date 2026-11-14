//go:build !windows

package nodeupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// exeSuffix is what a file needs at the end of its name to run: nothing here.
const exeSuffix = ""

// Restart runs exe in place of this process (exec: the same pid, arguments and environment), what the resident node
// does once an update or a rollback put another file there. It returns only when the exec fails.
func Restart(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}

// replaceFile puts src at dst in one rename: a running program's file can be replaced here.
func replaceFile(src, dst string) error { return os.Rename(src, dst) }

// removeRetired has nothing to remove: replaceFile never moves a file aside here.
func removeRetired(string) {}

// discard deletes path: a file a process runs from can be deleted here.
func discard(path string) { _ = os.Remove(path) }

// supervisorOf is nothing here: an update execs the new program in the node's own process, so the recorded pid is the
// one the watchdog stops.
func supervisorOf() (int, string) { return 0, "" }

// stopRestarted has nothing to stop: killRecorded already stopped the process the new program runs in.
func stopRestarted(pending, time.Time) {}

// syncFile flushes the staged file to disk. Reopening an already executed signed Mach-O file for writing can make
// macOS kill a subsequent self-check, so it is opened for reading only: here that is enough to flush it.
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// processStart is pid's start time as ps reports it, to the second: the portable identity of a process instance.
func processStart(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-p", fmt.Sprint(pid), "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
