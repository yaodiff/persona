package node

import (
	"context"
	"os"
	"strconv"
)

func orcaShellMatchesTerminal(pid int, pane string) bool {
	env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return err == nil && orcaCloseEnvMatches(string(env), pane)
}

func orcaCloseForeground(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return err == nil && procInFront(string(stat))
}

func orcaCloseCwd(_ context.Context, pid int) string {
	cwd, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	return cwd
}
