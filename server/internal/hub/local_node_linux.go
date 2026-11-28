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
)

func syncLocalNode(ctx context.Context, root string, services serviceFinder, want *update.Manifest) error {
	if want.Version == "" || want.Build <= 0 {
		return nil
	}
	binDir := resolvedPath(filepath.Join(root, "bin"))
	var first error
	for _, scope := range services.scopes {
		units := scope.units()
		for _, name := range []string{"personad-node.service", "persona-node.service", compat.HubService + "-node.service"} {
			unit, ok := units[name]
			if !ok {
				continue
			}
			programs := unitPrograms(unit.path, unit.dropins, services.home)
			if len(programs) != 1 {
				continue
			}
			target, program := residentProgram(binDir, programs[0])
			if target == "" {
				continue
			}
			pid := activeMainPID(ctx, scope.user, name)
			if pid <= 1 {
				continue
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				continue
			}
			err = reloadResidentPID(ctx, root, target, program, pid, proc, want)
			_ = proc.Release()
			if err != nil {
				first = errors.Join(first, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	return first
}

func activeMainPID(ctx context.Context, user bool, name string) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"show", "-p", "ActiveState", "-p", "MainPID", "--", name}
	if user {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return 0
	}
	pid := 0
	active := false
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ActiveState":
			active = value == "active"
		case "MainPID":
			pid, _ = strconv.Atoi(value)
		}
	}
	if !active {
		return 0
	}
	return pid
}

func reloadResidentPID(ctx context.Context, root, target, program string, pid int, proc *os.Process, want *update.Manifest) error {
	start := procinfo.StartMark(pid)
	if start == "" {
		return nil
	}
	base := filepath.Join("/proc", strconv.Itoa(pid))
	running := filepath.Join(base, "exe")
	exe, err := os.Readlink(running)
	if err != nil {
		return nil
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	if exe != target && !(program == "personad" && exe == filepath.Join(filepath.Dir(target), compat.DaemonBinary)) {
		return nil // The unit file could have changed since this process started.
	}
	if !runningResident(base, root) {
		return nil
	}
	if update.ProgramOf(running) != program {
		return nil // preserve the program the resident process is using
	}
	before, err := os.Stat(running)
	if err != nil {
		return nil
	}
	if !update.IsDeviceFile(target) {
		if current, err := os.Stat(target); err == nil && os.SameFile(before, current) {
			return nil
		}
	}
	return reconcileResident(root, target, proc, func() (string, error) {
		if err := prepareResidentTarget(ctx, target, program, want); err != nil {
			return "", err
		}
		current, err := os.Stat(target)
		if err != nil {
			return "", err
		}
		after, err := os.Stat(running)
		if err != nil || procinfo.StartMark(pid) != start || !os.SameFile(before, after) || os.SameFile(after, current) || !catchesHUP(base) {
			return "", nil
		}
		old, okOld := after.Sys().(*syscall.Stat_t)
		new, okNew := current.Sys().(*syscall.Stat_t)
		if !okOld || !okNew {
			return "", errors.New("cannot identify resident executable inodes")
		}
		return fmt.Sprintf("%d %s %d:%d %d:%d %d\n", pid, start, old.Dev, old.Ino, new.Dev, new.Ino, current.ModTime().UnixNano()), nil
	})
}

func catchesHUP(proc string) bool {
	status, err := os.ReadFile(filepath.Join(proc, "status"))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if mask, ok := strings.CutPrefix(line, "SigCgt:"); ok {
			value, err := strconv.ParseUint(strings.TrimSpace(mask), 16, 64)
			return err == nil && value&1 != 0
		}
	}
	return false
}

func runningResident(proc, root string) bool {
	args, err := os.ReadFile(filepath.Join(proc, "cmdline"))
	if err != nil {
		return false
	}
	argv := strings.Split(strings.TrimRight(string(args), "\x00"), "\x00")
	if len(argv) < 3 || argv[1] != "node" || !slices.Contains(argv[2:], "--attach") {
		return false
	}
	// Managed units export the state root. Check only the two root values.
	// Two unrelated services may otherwise run the very same executable.
	env, err := os.ReadFile(filepath.Join(proc, "environ"))
	if err != nil {
		return false
	}
	canonical, legacy := compat.EnvNames("PERSONA_HOME")
	var previous string
	for pair := range strings.SplitSeq(string(env), "\x00") {
		key, value, _ := strings.Cut(pair, "=")
		switch key {
		case canonical:
			return filepath.IsAbs(value) && resolvedPath(value) == resolvedPath(root)
		case legacy:
			previous = value
		}
	}
	return filepath.IsAbs(previous) && resolvedPath(previous) == resolvedPath(root)
}
