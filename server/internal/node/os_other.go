//go:build !windows

package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// progName is a command line's program as the scans compare it (isClaudeCmd, agentOfCmd): its base name.
func progName(argv0 string) string { return filepath.Base(argv0) }

// msysCmdLine: only Windows passes a command line to a program as one string (shell_windows.go).
func msysCmdLine(*exec.Cmd) {}

// scanOrcaProcsOS and scanClaudeProcessesOS are Windows' scans; macOS and Linux use ps and /proc.
func scanOrcaProcsOS() (map[string][]orcaProc, error) { return nil, errors.New("no process scan here") }

func scanClaudeProcessesOS() map[int]procEnv { return map[int]procEnv{} }

// scanCodexProcessesOS and scanOpenRolloutsOS are Windows' (codex_procs_windows.go); macOS and Linux use ps, lsof and
// /proc.
func scanCodexProcessesOS() ([]codexProcess, bool) { return nil, false }

func scanOpenRolloutsOS([]codexProcess) (map[int][]string, bool) { return nil, false }

// codexStartProgram: a launcher off Windows is run as it is (codex_procs_windows.go).
func codexStartProgram(path string, _ bool, env []string) (string, []string, error) {
	return path, env, nil
}

// codexStartShellProgram loads a native launcher's functions from the user's supported login shell, even if another
// shell's rc file exists. Unknown or unavailable shells retain the general launcher's supported-shell fallback.
func codexStartShellProgram() string {
	if sh := userLoginShell(); sh == "bash" || sh == "zsh" {
		if _, err := exec.LookPath(sh); err == nil {
			return sh
		}
	}
	return loginShellProgram()
}

// loginShellProgram is the shell loginShell and interactiveShell run scripts and launchers in, the way the user's own
// terminal opens one (shellFlag says how it is started):
//   - on macOS, the account's login shell when it is bash or zsh (userLoginShell), whatever rc files the other shell
//     has: Terminal opens that shell, and a bash user's nvm or PATH is often only in ~/.bash_profile while a small
//     ~/.zshrc exists for other reasons (#16217);
//   - elsewhere zsh, so ~/.zshrc functions and PATH are there, but bash for a user whose login shell is bash and who has
//     no ~/.zshrc (a Linux server with zsh around but everything in ~/.bashrc). A Linux bash user with a ~/.zshrc keeps
//     zsh: an account that cannot change its login shell often starts zsh from ~/.bashrc instead;
//   - bash without zsh.
func loginShellProgram() string {
	return pickLoginShell(runtime.GOOS, userLoginShell(), shellInstalled, hasZshrc)
}

// pickLoginShell is loginShellProgram's choice for an OS, the user's login shell, which shells are installed and
// whether there is a ~/.zshrc (asked only when it matters).
func pickLoginShell(goos, login string, installed func(string) bool, zshrc func() bool) string {
	if !installed("zsh") {
		return "bash"
	}
	if goos == "darwin" && login == "bash" && installed("bash") {
		return "bash"
	}
	if login == "bash" && !zshrc() {
		return "bash"
	}
	return "zsh"
}

func shellInstalled(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// hasZshrc: the user's home has a ~/.zshrc, or its existence cannot be told.
func hasZshrc() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	_, err = os.Stat(filepath.Join(home, ".zshrc"))
	return !errors.Is(err, os.ErrNotExist)
}

// shellFlag is how shell, loginShellProgram's or codexStartShellProgram's, runs a script (`<shell> <flag> <script>`).
func shellFlag(shell string) string { return shellFlagFor(runtime.GOOS, shell, hasBashLoginFile) }

// shellFlagFor: on macOS a login, interactive shell (-lic), as Terminal opens every window: zsh then reads
// /etc/zprofile (path_helper), ~/.zprofile (Homebrew's shellenv) and ~/.zshrc, and bash ~/.bash_profile (or
// ~/.bash_login, ~/.profile), where nvm's installer and most Mac setups put PATH. A node under launchd starts with
// PATH=/usr/bin:/bin:/usr/sbin:/sbin, so without them a CLI the terminal finds is "command not found" (#16217). A bash
// with none of those files would read nothing of the user's as a login shell, so it stays interactive and reads
// ~/.bashrc. Elsewhere interactive only (-ic): terminals open non-login shells, which read ~/.zshrc / ~/.bashrc.
func shellFlagFor(goos, shell string, bashLogin func() bool) string {
	if goos != "darwin" {
		return "-ic"
	}
	if filepath.Base(shell) == "bash" && !bashLogin() {
		return "-ic"
	}
	return "-lic"
}

// hasBashLoginFile: the user's home has a file a login bash reads (~/.bash_profile, ~/.bash_login or ~/.profile), or
// that cannot be told.
func hasBashLoginFile() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	for _, f := range []string{".bash_profile", ".bash_login", ".profile"} {
		if _, err := os.Stat(filepath.Join(home, f)); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// userLoginShell is the base name of the user's login shell: $SHELL, else the account's record (a node under systemd
// or launchd has no $SHELL): Directory Services on macOS, whose /etc/passwd lists only system accounts, /etc/passwd
// elsewhere; "" when neither says.
func userLoginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return filepath.Base(sh)
	}
	if runtime.GOOS == "darwin" {
		return macLoginShell()
	}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	return passwdShell(string(b), os.Getuid())
}

// macLoginShell is the account's login shell in Directory Services, asked once: it changes only with chsh (or Users &
// Groups), which a new terminal, like a restarted node, picks up. A variable for tests.
var macLoginShell = sync.OnceValue(func() string { return dsclUserShell("/usr/bin/dscl", currentUserName()) })

// dsclUserShell asks dscl for user's UserShell (`dscl . -read /Users/<user> UserShell`): the node builds without cgo,
// so it cannot call getpwuid. "" when dscl fails or says nothing.
func dsclUserShell(dscl, user string) string {
	if user == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, dscl, ".", "-read", "/Users/"+user, "UserShell")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseDsclShell(string(out))
}

// parseDsclShell is the base name of the shell in dscl's `UserShell: /bin/zsh`, or in its two-line form for a value
// with spaces (`UserShell:` then ` /path`); "" when there is none.
func parseDsclShell(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, "UserShell:")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if v == "" && i+1 < len(lines) {
			v = strings.TrimSpace(lines[i+1])
		}
		if v == "" {
			return ""
		}
		return filepath.Base(v)
	}
	return ""
}

// currentUserName is the account the node runs as: os/user (a system call on macOS, no cgo needed), else $USER.
func currentUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
