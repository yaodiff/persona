//go:build linux || darwin

package hub

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Keep one receipt per installed program. An alias unit can report the same
// PID, and an active node can keep draining after the first caller returns.
func reconcileResident(root, target string, proc *os.Process, drift func() (string, error)) error {
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return err
	}
	name := "resident-" + filepath.Base(target) + ".reload"
	fd, err := unix.Open(filepath.Join(runDir, name), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return errors.New("invalid resident reload receipt")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	key, err := drift()
	if err != nil || key == "" {
		return err
	}
	prev, err := io.ReadAll(io.LimitReader(f, 512))
	if err != nil || string(prev) == key {
		return err
	}
	// On Linux Go pins the process with a pidfd where available. No process
	// group or service cgroup is signaled on either platform.
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	n, err := f.WriteAt([]byte(key), 0)
	if err != nil {
		return err
	}
	return f.Truncate(int64(n))
}
