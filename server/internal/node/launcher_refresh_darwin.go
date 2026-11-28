package node

import "golang.org/x/sys/unix"

func managedLauncherProcessEnv(pid int) map[string]string {
	const kernProcArgs2 = 49
	raw, err := unix.SysctlRaw("kern", kernProcArgs2, pid)
	if err != nil {
		return nil
	}
	env, ok := orcaDarwinProcessEnv(raw)
	if !ok {
		return nil
	}
	return parseProcessEnviron(env)
}
