//go:build !darwin && !windows

package node

import "runtime"

func managedLauncherProcessEnv(pid int) map[string]string {
	if runtime.GOOS != "linux" {
		return nil
	}
	return parseProcessEnviron(procEnvBytes(pid))
}
