//go:build !windows

package node

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// readPaneEnv reads the engine pane variables from the process's environment: /proc/<pid>/environ on Linux, ps -E
// on macOS (as paneOfPID does).
func readPaneEnv(pid int) map[string]string {
	if pid <= 0 {
		return nil
	}
	var env []byte
	if runtime.GOOS == "linux" {
		env, _ = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		env, _ = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		cancel()
	}
	if len(env) == 0 {
		return nil
	}
	return pickedEnv(string(env))
}

// procEnvBytes is pid's environment as it was started, as readPaneEnv reads it: NUL-separated on Linux, the ps -E line
// on macOS; nil when it cannot be read.
func procEnvBytes(pid int) []byte {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "linux" {
		env, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		return env
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	env, _ := exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return env
}
