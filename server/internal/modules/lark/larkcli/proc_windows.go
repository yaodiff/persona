//go:build windows

package larkcli

import (
	"os"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/osproc"
)

func stopProcessGroup(proc *os.Process, procID *atomic.Pointer[osproc.ProcIdentity]) error {
	if proc == nil {
		return os.ErrProcessDone
	}
	id := procID.Load()
	if id == nil || id.Start == "" {
		return proc.Kill()
	}
	pid := proc.Pid
	if !id.StillIs(pid) {
		return os.ErrProcessDone
	}
	if err := osproc.StopGroup(pid, true); err != nil {
		if !id.StillIs(pid) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// cleanupWaitDelayGroup is a no-op on Windows because taskkill /PID after leader exit risks hitting a reused PID.
func cleanupWaitDelayGroup(*os.Process, *atomic.Pointer[osproc.ProcIdentity]) {}
