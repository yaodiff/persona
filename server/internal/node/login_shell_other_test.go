//go:build !windows

package node

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// pinTestLoginShell is TestMain's: on macOS zsh, whatever the developer's login shell (hostorca_test.go). Only in the
// developer's own environment (the account's home): a helper process a test starts with a home and SHELL of its own
// keeps them.
func pinTestLoginShell() {
	if runtime.GOOS != "darwin" {
		return
	}
	macLoginShell = func() string { return "zsh" }
	if u, err := user.Current(); err != nil || os.Getenv("HOME") != u.HomeDir {
		return
	}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		os.Setenv("SHELL", zsh)
	}
}

// The shell the node runs scripts in, for an OS, the account's login shell, whether zsh is installed and whether there
// is a ~/.zshrc (#16217).
func TestPickLoginShell(t *testing.T) {
	for _, tc := range []struct {
		name, goos, login string
		zsh, zshrc        bool
		want              string
	}{
		// The measured Mac: bash in Directory Services, nvm only in ~/.bash_profile, a small ~/.zshrc for other things.
		{"mac bash user with a zshrc", "darwin", "bash", true, true, "bash"},
		{"mac bash user without a zshrc", "darwin", "bash", true, false, "bash"},
		{"mac zsh user", "darwin", "zsh", true, false, "zsh"},
		{"mac login shell unknown", "darwin", "", true, false, "zsh"},
		{"mac fish user", "darwin", "fish", true, true, "zsh"},
		// Linux keeps its rule: a bash user's ~/.zshrc may be the zsh their ~/.bashrc execs into.
		{"linux bash user with a zshrc", "linux", "bash", true, true, "zsh"},
		{"linux bash user without a zshrc", "linux", "bash", true, false, "bash"},
		{"linux zsh user", "linux", "zsh", true, false, "zsh"},
		{"no zsh", "linux", "zsh", false, true, "bash"},
		{"mac without zsh", "darwin", "", false, true, "bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installed := func(name string) bool { return name == "bash" || (name == "zsh" && tc.zsh) }
			if got := pickLoginShell(tc.goos, tc.login, installed, func() bool { return tc.zshrc }); got != tc.want {
				t.Fatalf("pickLoginShell(%s, %q, zsh=%v, zshrc=%v) = %q, want %q", tc.goos, tc.login, tc.zsh, tc.zshrc, got, tc.want)
			}
		})
	}
}

func TestShellFlagFor(t *testing.T) {
	yes, no := func() bool { return true }, func() bool { return false }
	for _, tc := range []struct {
		goos, shell string
		login       func() bool
		want        string
	}{
		{"darwin", "zsh", no, "-lic"},
		{"darwin", "/bin/zsh", yes, "-lic"},
		{"darwin", "/bin/bash", yes, "-lic"},
		// No ~/.bash_profile, ~/.bash_login or ~/.profile: a login bash would read nothing of the user's, ~/.bashrc does.
		{"darwin", "bash", no, "-ic"},
		{"linux", "bash", yes, "-ic"},
		{"linux", "zsh", yes, "-ic"},
	} {
		if got := shellFlagFor(tc.goos, tc.shell, tc.login); got != tc.want {
			t.Errorf("shellFlagFor(%s, %s, login file %v) = %q, want %q", tc.goos, tc.shell, tc.login(), got, tc.want)
		}
	}
	if got := bashRCFile("-lic"); got != "~/.bash_profile" {
		t.Fatalf("login bash reads %q", got)
	}
	if got := bashRCFile("-ic"); got != "~/.bashrc" {
		t.Fatalf("interactive bash reads %q", got)
	}
}

func TestHasBashLoginFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".bashrc"), "true\n")
	if hasBashLoginFile() {
		t.Fatal("~/.bashrc is no login file")
	}
	for _, f := range []string{".profile", ".bash_login", ".bash_profile"} {
		writeFile(t, filepath.Join(home, f), "true\n")
		if !hasBashLoginFile() {
			t.Fatalf("%s is a login file", f)
		}
		if err := os.Remove(filepath.Join(home, f)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseDsclShell(t *testing.T) {
	for out, want := range map[string]string{
		"UserShell: /bin/bash\n":                      "bash",
		"UserShell: /opt/homebrew/bin/zsh\n":          "zsh",
		"UserShell:\n /Users/Shared/my shells/fish\n": "fish",
		"":                         "",
		"No such key: UserShell\n": "",
		"RealName: Example\nUserShell: /bin/zsh\n": "zsh",
		"UserShell:\n": "",
		"<dscl_cmd> DS Error: -14136 (eDSRecordNotFound)": "",
	} {
		if got := parseDsclShell(out); got != want {
			t.Errorf("parseDsclShell(%q) = %q, want %q", out, got, want)
		}
	}
}

// dsclUserShell asks `dscl . -read /Users/<user> UserShell`; a dscl that fails says nothing.
func TestDsclUserShellRunsDscl(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "dscl")
	writeFile(t, fake, `#!/bin/sh
[ "$1 $2 $3 $4" = ". -read /Users/example UserShell" ] || { echo "unexpected: $*" >&2; exit 2; }
echo "UserShell: /bin/bash"
`)
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := dsclUserShell(fake, "example"); got != "bash" {
		t.Fatalf("dsclUserShell = %q, want bash", got)
	}
	if got := dsclUserShell(fake, "someone-else"); got != "" {
		t.Fatalf("a failing dscl gave %q", got)
	}
	if got := dsclUserShell(filepath.Join(dir, "missing"), "example"); got != "" {
		t.Fatalf("a missing dscl gave %q", got)
	}
	if got := dsclUserShell(fake, ""); got != "" {
		t.Fatalf("no user gave %q", got)
	}
}

// A Mac node under launchd has no $SHELL: the account's shell comes from Directory Services, not /etc/passwd.
func TestUserLoginShellWithoutSHELLOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Directory Services is macOS's")
	}
	t.Setenv("SHELL", "")
	old := macLoginShell
	t.Cleanup(func() { macLoginShell = old })
	macLoginShell = func() string { return "bash" }
	if got := userLoginShell(); got != "bash" {
		t.Fatalf("userLoginShell() = %q, want bash", got)
	}
	t.Setenv("SHELL", "/bin/zsh")
	if got := userLoginShell(); got != "zsh" {
		t.Fatalf("$SHELL should come first: %q", got)
	}
}

// bashProfileHome is a home whose CLI is on PATH only through ~/.bash_profile (nvm's setup on a Mac), with a ~/.zshrc
// that does not set it up, and the launchd PATH: a fake claude that prints marker and its arguments.
func bashProfileHome(t *testing.T, marker string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".nvm", "versions", "node", "v0", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(bin, "claude")
	writeFile(t, claude, "#!/bin/sh\necho "+marker+" \"$@\"\n")
	if err := os.Chmod(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".bash_profile"), "export PATH="+shellQuote(bin)+":$PATH\n")
	writeFile(t, filepath.Join(home, ".zshrc"), "setopt no_beep\n")
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "")
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	t.Setenv("BASH_ENV", "")
	t.Setenv("ENV", "")
	return home
}

// macOS's flag finds a CLI set up in ~/.bash_profile, which bash -ic does not read; on any machine with bash.
func TestLoginBashReadsBashProfile(t *testing.T) {
	bashProfileHome(t, "found")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	flag := shellFlagFor("darwin", "bash", hasBashLoginFile)
	if flag != "-lic" {
		t.Fatalf("a bash with ~/.bash_profile runs as %q on macOS", flag)
	}
	out, err := exec.CommandContext(ctx, "bash", flag, "claude ok").Output()
	if err != nil || !strings.Contains(string(out), "found ok") {
		t.Fatalf("bash %s: %q, %v", flag, out, err)
	}
	if out, err := exec.CommandContext(ctx, "bash", "-ic", "command -v claude").Output(); err == nil {
		t.Fatalf("bash -ic should not read ~/.bash_profile, found %q", out)
	}
}

// The measured Mac end to end: no $SHELL, bash in Directory Services, the CLI only in ~/.bash_profile, a ~/.zshrc that
// does not set it up. The model list's command runs bash as a login shell and finds claude (#16217).
func TestAgentModelsCommandFindsCLIFromBashProfileOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the login shell from Directory Services, and -lic, are macOS's")
	}
	const marker = "persona-fake-claude"
	bashProfileHome(t, marker)
	old := macLoginShell
	t.Cleanup(func() { macLoginShell = old })
	macLoginShell = func() string { return "bash" }

	n, _ := testNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := n.agentModelsCommand(ctx, "claude", "claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Args[0]) != "bash" || !slices.Contains(cmd.Args, "-lic") {
		t.Fatalf("model list command %q, want bash -lic", cmd.Args)
	}
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), marker+" -p") {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("claude not found through ~/.bash_profile: %q, %v, %s", out, err, stderr)
	}
}
