package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func orcaShellMatchesTerminal(pid int, pane string) bool {
	// KERN_PROCARGS2 has no named OID on some macOS versions.
	const kernProcArgs2 = 49
	raw, err := unix.SysctlRaw("kern", kernProcArgs2, pid)
	if err != nil || len(raw) < 4 {
		return false
	}
	env, ok := orcaDarwinProcessEnv(raw)
	// macOS hides the environment of protected system shells, including the
	// default /bin/zsh. In that case the caller's creation PID, birth stamp,
	// PTY ancestry and foreground evidence establish the shell's identity.
	return ok && (len(env) == 0 || orcaCloseEnvMatches(string(env), pane))
}

// KERN_PROCARGS2 returns argc, the executable path, NUL padding, then argc
// arguments and the environment. Skip the arguments so they cannot act as marks.
func orcaDarwinProcessEnv(raw []byte) ([]byte, bool) {
	if len(raw) < 4 {
		return nil, false
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc == 0 || argc > len(raw)-4 {
		return nil, false
	}
	_, argv, ok := bytes.Cut(raw[4:], []byte{0})
	if !ok {
		return nil, false
	}
	argv = bytes.TrimLeft(argv, "\x00")
	for range argc {
		_, argv, ok = bytes.Cut(argv, []byte{0})
		if !ok {
			return nil, false
		}
	}
	// Only the first environment block belongs to the current exec.
	env, _, _ := bytes.Cut(argv, []byte{0, 0})
	return env, true
}

func orcaCloseForeground(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	// SRUN, SSLEEP or SIDL. In particular, a stopped or zombie process is not
	// the running foreground job even when the group ids happen to match.
	return err == nil && kp.Proc.P_stat > 0 && kp.Proc.P_stat <= 3 && kp.Eproc.Pgid > 0 && kp.Eproc.Pgid == kp.Eproc.Tpgid
}

func orcaCloseCwd(ctx context.Context, pid int) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn0").Output()
	if err != nil {
		return ""
	}
	for field := range strings.SplitSeq(string(out), "\x00") {
		if cwd, ok := strings.CutPrefix(field, "n"); ok {
			return cwd
		}
	}
	return ""
}
