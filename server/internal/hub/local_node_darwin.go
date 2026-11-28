package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/procinfo"
	"github.com/nwverse/persona/server/internal/update"
	"golang.org/x/sys/unix"
)

func syncLocalNode(ctx context.Context, root string, services serviceFinder, want *update.Manifest) error {
	if want.Version == "" || want.Build <= 0 {
		return nil
	}
	domain := "gui/" + strconv.Itoa(os.Getuid()) + "/"
	binDir := resolvedPath(filepath.Join(root, "bin"))
	var first error
	// Persona installs the resident in the current user's login session.
	for _, label := range []string{"com.persona.node", compat.NodeLabel} {
		job, ok := readLaunchdJob(filepath.Join(services.home, "Library", "LaunchAgents", label+".plist"))
		if !ok || job.disabled || job.label != label || len(job.args) < 3 ||
			job.args[1] != "node" || !slices.Contains(job.args[2:], "--attach") {
			continue
		}
		if job.stateRoot != "" && resolvedPath(job.stateRoot) != resolvedPath(root) {
			continue
		}
		entry := homePath(job.args[0], services.home)
		target, program := residentProgram(binDir, entry)
		if target == "" {
			continue
		}
		pid := launchdMainPID(ctx, domain+label, entry)
		if pid <= 1 {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		err = reconcileResident(root, target, proc, func() (string, error) {
			start := procinfo.StartMark(pid)
			if start == "" {
				return "", nil
			}
			dev, ino := launchdExecutable(ctx, pid, entry, target)
			if ino == 0 {
				return "", nil
			}
			if err := prepareResidentTarget(ctx, target, program, want); err != nil {
				return "", err
			}
			info, err := os.Stat(target)
			if err != nil {
				return "", err
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || dev == uint64(uint32(st.Dev)) && ino == st.Ino || procinfo.StartMark(pid) != start {
				return "", nil
			}
			if newDev, newIno := launchdExecutable(ctx, pid, entry, target); newIno != ino || newDev != dev {
				return "", nil
			}
			p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
			if err != nil || p.Proc.P_sigcatch&1 == 0 || procinfo.StartMark(pid) != start {
				return "", nil
			}
			return fmt.Sprintf("%d %s %d:%d %d:%d %d\n", pid, start, dev, ino, st.Dev, st.Ino, info.ModTime().UnixNano()), nil
		})
		_ = proc.Release()
		if err != nil {
			first = errors.Join(first, fmt.Errorf("%s: %w", label, err))
		}
	}
	return first
}

func launchdMainPID(ctx context.Context, service, program string) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", "print", service).Output()
	if err != nil {
		return 0
	}
	active, matches, pid := false, false, 0
	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " = ")
		switch key {
		case "state":
			active = active || value == "running"
		case "program":
			matches = matches || value == program
		case "pid":
			pid, _ = strconv.Atoi(value)
		}
	}
	if active && matches {
		return pid
	}
	return 0
}

// Darwin exposes mapped text files through lsof. Compare the running
// executable's device and inode with stat on the verified replacement.
func launchdExecutable(ctx context.Context, pid int, paths ...string) (dev, ino uint64) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-F0fiDn").Output()
	if err != nil {
		return 0, 0
	}
	for file := range strings.SplitSeq(string(out), "\n") {
		var name string
		dev, ino = 0, 0
		for field := range strings.SplitSeq(file, "\x00") {
			if len(field) < 2 {
				continue
			}
			switch field[0] {
			case 'D':
				dev, _ = strconv.ParseUint(field[1:], 0, 64)
			case 'i':
				ino, _ = strconv.ParseUint(field[1:], 10, 64)
			case 'n':
				name = strings.TrimSuffix(field[1:], " (deleted)")
			}
		}
		if dev != 0 && ino != 0 && slices.Contains(paths, name) {
			return dev, ino
		}
	}
	return 0, 0
}
