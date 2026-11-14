package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

// env is what the commands that look after persona-link (install, status, logs, uninstall) need from this computer;
// tests fill it with their own.
type env struct {
	home, goos, user string
	uid              int
	stdin            io.Reader
	stdout, stderr   io.Writer
	terminal         bool // stdin is a terminal
	getenv           func(string) string
	exe              func() (string, error)
	lookPath         func(string) (string, error)
	exists           func(string) bool
	// command runs a program and returns its combined output; passthrough runs one on this terminal.
	command     func(name string, args ...string) (string, error)
	passthrough func(name string, args ...string) error
	now         func() time.Time
	sleep       func(time.Duration)
	// wait is how long install waits for the link to come up (a setting).
	wait time.Duration
	// sshBin overrides the system ssh executable in tests; "" uses "ssh" from PATH.
	sshBin string
}

func newEnv() *env {
	home, _ := os.UserHomeDir()
	e := &env{home: home, goos: runtime.GOOS, uid: os.Getuid(), stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		getenv: identity.Getenv, exe: os.Executable, lookPath: exec.LookPath, now: time.Now, sleep: time.Sleep,
		exists: func(p string) bool { _, err := os.Stat(p); return err == nil }, wait: 20 * time.Second}
	if u, err := user.Current(); err == nil {
		e.user = u.Username
	}
	if fi, err := os.Stdin.Stat(); err == nil {
		e.terminal = fi.Mode()&os.ModeCharDevice != 0
	}
	e.command = func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = e.commandEnv()
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	e.passthrough = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Env = e.commandEnv()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	return e
}

// commandEnv is this process's environment, plus where the systemd user instance is when a session did not say: over
// `su`, `sudo -u` or `docker exec` XDG_RUNTIME_DIR is unset and `systemctl --user` cannot find it.
func (e *env) commandEnv() []string {
	env := os.Environ()
	if e.getenv("XDG_RUNTIME_DIR") == "" {
		if dir := "/run/user/" + strconv.Itoa(e.uid); e.exists(dir) {
			env = append(env, "XDG_RUNTIME_DIR="+dir)
		}
	}
	return env
}

func (e *env) stdinIsTerminal() bool { return e.terminal }

func (e *env) personaDir() string { return identity.DataDir(e.home) }
func (e *env) configPath() string { return filepath.Join(e.personaDir(), "link.json") }
func (e *env) logPath() string    { return filepath.Join(e.personaDir(), "link.log") }
func (e *env) binPath() string {
	dir := filepath.Join(e.personaDir(), "bin")
	return existingPath(filepath.Join(dir, e.exeName("persona-link")), filepath.Join(dir, e.exeName(legacyLinkBinary)))
}

// interruptible is a context that ends on Ctrl-C (logs -f).
func (e *env) interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// installBinary puts this persona-link at ~/.persona/bin/persona-link, where the service runs it from, unless it is
// that file already. copied says whether it had to.
func (e *env) installBinary() (path string, copied bool, err error) {
	self, err := e.exe()
	if err != nil {
		return "", false, err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	dst := e.binPath()
	if a, err := os.Stat(self); err == nil {
		if b, err := os.Stat(dst); err == nil && os.SameFile(a, b) {
			return dst, false, nil
		}
	}
	if _, err := identity.PrepareHome(e.home); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", false, err
	}
	src, err := os.Open(self)
	if err != nil {
		return "", false, err
	}
	defer src.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", false, err
	}
	_, err = io.Copy(out, src)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst) // over a running copy too: that one keeps its file until it exits
	}
	if err != nil {
		os.Remove(tmp)
		return "", false, err
	}
	return dst, true, nil
}

// firstLine is the first line of a command's output, for a one-line reason.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
