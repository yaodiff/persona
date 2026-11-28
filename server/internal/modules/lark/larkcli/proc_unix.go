//go:build !windows

package larkcli

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

func stopProcessGroup(proc *os.Process, procID *atomic.Pointer[osproc.ProcIdentity]) error {
	if proc == nil {
		return os.ErrProcessDone
	}
	pid := proc.Pid
	if id := procID.Load(); id != nil && id.Start != "" && osproc.Alive(pid) && !id.StillIs(pid) {
		return os.ErrProcessDone
	}
	if err := osproc.StopGroup(pid, true); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// On macOS a child forked while SIGKILL is delivered can miss the first signal and hold
	// stdout/stderr open until WaitDelay; sweep briefly so the group drains promptly (#5942).
	osproc.SweepGroup(pid, 250*time.Millisecond)
	return nil
}

// cleanupWaitDelayGroup stops orphaned grandchildren still holding stdio open after a clean leader exit.
func cleanupWaitDelayGroup(proc *os.Process, procID *atomic.Pointer[osproc.ProcIdentity]) {
	if proc == nil {
		return
	}
	pid := proc.Pid
	if id := procID.Load(); osproc.Alive(pid) && (id == nil || id.Start == "" || !id.StillIs(pid)) {
		return
	}
	_ = osproc.StopGroup(pid, true)
}
