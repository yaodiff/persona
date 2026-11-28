package manager

import (
	"os"
	"strconv"
	"strings"
)

// The installed path can already point to a different inode when a
// manager finishes starting. procfs still identifies the one in memory.
func runningBinaryInfo(_ string) (os.FileInfo, error) { return os.Stat("/proc/self/exe") }

func launcherBinary() (string, os.FileInfo) {
	proc := "/proc/" + strconv.Itoa(os.Getppid()) + "/exe"
	path, err := os.Readlink(proc)
	if err != nil {
		return "", nil
	}
	fi, err := os.Stat(proc)
	if err != nil {
		return "", nil
	}
	return strings.TrimSuffix(path, " (deleted)"), fi
}
