package node

import (
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/osproc"
)

// Orca's stale-session cases need to reap the process while preserving its on-disk
// session entry. The task-terminal close cases keep their process alive through cleanup.
func idleTerminalTestProcess(t *testing.T, env ...string) (int, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "NODE_TEST_TASK_TERMINAL_IDLE=1")
	cmd.Env = append(cmd.Env, env...)
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	stop := sync.OnceFunc(func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(stop)
	return cmd.Process.Pid, stop
}
