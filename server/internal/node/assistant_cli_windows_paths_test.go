package node

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The scripts the node hands Git Bash as an argument cross the native process → Git Bash boundary on Windows, which
// eats the backslashes shellQuote escapes and leaves an unmatched quote (#5487). Their native paths go with forward
// slashes and no backslash in the quoting, and a POSIX shell reads them back as the intended paths: sh here, Git Bash
// started natively on Windows (shellRun).

// windowsScriptNode is a node building its scripts for Windows, with bin as its personad and managed launchers.
func windowsScriptNode(t *testing.T, bin string, managed ...string) *Node {
	t.Helper()
	old := shellScriptGOOS
	shellScriptGOOS = "windows"
	t.Cleanup(func() { shellScriptGOOS = old })
	n := &Node{bin: bin, home: t.TempDir(), dir: t.TempDir(), logger: log.New(io.Discard, "", 0)}
	n.managed.byID = map[string]proto.ManagedLauncher{}
	for _, name := range managed {
		n.managed.byID[name] = proto.ManagedLauncher{Name: name}
	}
	return n
}

// slashed is a native path as the scripts must hand it to Git Bash.
func slashed(path string) string { return strings.ReplaceAll(path, `\`, "/") }

// wordsProbe prefixes a launch script with a function printing each of its words, so a shell reports how it parsed the
// script instead of running the launcher.
const wordsProbe = `words() { printf '%s\n' "$@"; }; words `

// checkScriptWords fails unless script (a command line of the scripts above) has no backslash and the shell parses it
// into want.
func checkScriptWords(t *testing.T, script string, want ...string) {
	t.Helper()
	if strings.Contains(script, `\`) {
		t.Fatalf("backslash in the Git Bash script: %s", script)
	}
	out, err := shellRun(t, wordsProbe+script)
	if err != nil {
		t.Fatalf("the shell rejected %s: %v\n%s", script, err, out)
	}
	if got := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); !slices.Equal(got, want) {
		t.Fatalf("the shell read %q, want %q (script %s)", got, want, script)
	}
}

// icScript is the -ic (-lic on macOS, shellFlag) script of cmd's arguments.
func icScript(t *testing.T, args []string) string {
	t.Helper()
	i := slices.Index(args, shellFlag(args[0]))
	if i < 0 || i+2 >= len(args) || args[i+2] != "persona" {
		t.Fatalf("not a -ic launch: %q", args)
	}
	prefix := "echo " + assistantShellReady + "; "
	return strings.TrimPrefix(args[i+1], prefix)
}

func TestAssistantCLICommandWindowsPathsUseForwardSlashes(t *testing.T) {
	for _, c := range []struct {
		name, launcher, profile, bin string
		managed                      bool
	}{
		{"profile", "claude", `C:\Users\Example\.persona\profiles\claude`, `C:\Persona\personad.exe`, false},
		{"profile with a space", "claude", `C:\Users\Example User\.persona\profiles\claude`, `C:\Persona\personad.exe`, false},
		{"profile with an apostrophe", "claude", `C:\Users\O'Example\.persona\profiles\claude`, `C:\Persona\personad.exe`, false},
		{"managed launcher", "work", `C:\Users\Example User\.persona\profiles\work`, `C:\Program Files\Persona\personad.exe`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var n *Node
			if c.managed {
				n = windowsScriptNode(t, c.bin, c.launcher)
			} else {
				n = windowsScriptNode(t, c.bin)
			}
			cmd := n.assistantCLICommand("bash", c.launcher, n.dir, false, []string{"-p"}, []string{"CLAUDE_CONFIG_DIR=" + c.profile})
			script, ok := strings.CutSuffix(icScript(t, cmd.Args), ` "$@"`)
			if !ok {
				t.Fatalf("no \"$@\": %q", cmd.Args)
			}
			want := []string{"CLAUDE_CONFIG_DIR=" + slashed(c.profile), c.launcher}
			if c.managed {
				want = []string{want[0], slashed(c.bin), "launch", c.launcher}
			}
			checkScriptWords(t, script, want...)
		})
	}
}

// The Codex assistant's app-server starts a managed launcher's personad from the same kind of script.
func TestCodexAssistantCommandWindowsPathsUseForwardSlashes(t *testing.T) {
	const bin = `C:\Program Files\Persona\personad.exe`
	n := windowsScriptNode(t, bin, "box")
	cmd := n.codexAssistantCommand("bash", "box", []string{"--listen", "stdio://"})
	launcher, ok := strings.CutPrefix(icScript(t, cmd.Args), codexShell(""))
	if !ok {
		t.Fatalf("not codexShell: %q", cmd.Args)
	}
	checkScriptWords(t, launcher, slashed(bin), "launch", "box")
}

// When it needs the shell, the model list quotes the Codex app-server launch or plain Claude with claude_start_dir.
func TestAgentModelsCommandWindowsPathsUseForwardSlashes(t *testing.T) {
	const bin = `C:\Program Files\Persona\personad.exe`
	n := windowsScriptNode(t, bin, "box")
	if runtime.GOOS != "windows" {
		binDir := t.TempDir()
		writeExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\nexit 1\n")
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if _, err := interactiveShell(); err != nil {
		t.Skip(err)
	}
	cmd, err := n.agentModelsCommand(context.Background(), "box", "codex", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if !slices.Equal(cmd.Args, []string{bin, "launch", "box", "app-server"}) {
			t.Fatalf("not a direct Windows launch: %q", cmd.Args)
		}
	} else {
		launcher, ok := strings.CutPrefix(icScript(t, cmd.Args), codexShell(""))
		if !ok {
			t.Fatalf("not codexShell: %q", cmd.Args)
		}
		checkScriptWords(t, launcher, slashed(bin), "launch", "box", "app-server")
	}

	start := `C:\Users\Example User\.claude-work`
	n.claudeStartDir.Store(&start)
	cmd, err = n.agentModelsCommand(context.Background(), "claude", "claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script, ok := strings.CutSuffix(icScript(t, cmd.Args), ` "$@"`)
	if !ok {
		t.Fatalf("no \"$@\": %q", cmd.Args)
	}
	checkScriptWords(t, script, "CLAUDE_CONFIG_DIR="+slashed(start), "claude")
}

// Other platforms keep their paths exactly as shellQuote has them, backslashes included.
func TestScriptPathsUnchangedOffWindows(t *testing.T) {
	old := shellScriptGOOS
	shellScriptGOOS = "linux"
	t.Cleanup(func() { shellScriptGOOS = old })
	const profile, bin = `/home/a\b/O'profile`, `/opt/a\b/personad`
	n := &Node{bin: bin, home: t.TempDir(), dir: t.TempDir(), logger: log.New(io.Discard, "", 0)}
	n.managed.byID = map[string]proto.ManagedLauncher{"work": {Name: "work"}}
	cmd := n.assistantCLICommand("/bin/sh", "work", n.dir, false, nil, []string{"CLAUDE_CONFIG_DIR=" + profile})
	if got, want := icScript(t, cmd.Args), "CLAUDE_CONFIG_DIR="+shellQuote(profile)+" "+shellQuote(bin)+` launch work "$@"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := n.codexAssistantCommand("/bin/sh", "work", nil).Args[2], codexShell(shellQuote(bin)+" launch work"); got != want {
		t.Fatalf("codex: got %q, want %q", got, want)
	}
}

// scriptPath on Windows: forward slashes, and an apostrophe as '"'"' so no backslash is left in the quoting.
func TestScriptPathWindowsQuoting(t *testing.T) {
	old := shellScriptGOOS
	shellScriptGOOS = "windows"
	t.Cleanup(func() { shellScriptGOOS = old })
	for in, want := range map[string]string{
		`C:\Persona\personad.exe`:    `'C:/Persona/personad.exe'`,
		`C:\Program Files\Persona`:   `'C:/Program Files/Persona'`,
		`C:\Users\O'Example\profile`: `'C:/Users/O'"'"'Example/profile'`,
		`C:\Users\O'Ex$am"p!e`:       `'C:/Users/O'"'"'Ex$am"p!e'`,
	} {
		if got := scriptPath(in); got != want {
			t.Errorf("scriptPath(%q) = %s, want %s", in, got, want)
		}
		checkScriptWords(t, scriptPath(in), slashed(in))
	}
}

// The launcher and sign-in checks run `cd <dir> && <launcher> --settings <file> …` in the login shell: the temporary
// directory, the settings file and a managed launcher's personad are all native paths.
func TestLauncherInDirScriptWindowsPaths(t *testing.T) {
	const bin = `C:\Program Files\Persona\personad.exe`
	const dir = `C:\Users\O'Example\AppData\Local\Temp\persona-auth-1`
	settings := dir + `\auth-settings.json`
	n := windowsScriptNode(t, bin, "work")
	script, ok := strings.CutSuffix(n.launcherInDirScript("work", dir, scriptSettingsFlag(settings)+" auth status --json"), " </dev/null")
	if !ok {
		t.Fatalf("not reading from /dev/null: %s", script)
	}
	cd, run, ok := strings.Cut(script, " && ")
	if !ok {
		t.Fatalf("no cd: %s", script)
	}
	checkScriptWords(t, cd, "cd", slashed(dir))
	checkScriptWords(t, run, slashed(bin), "launch", "work", "--settings", slashed(settings), "auth", "status", "--json")
}

// On Windows the assistant's claude names its profile with forward slashes and every other start with backslashes:
// read back from the running claudes, they are one directory.
func TestScannedConfigDirsWindowsAreOneDirectory(t *testing.T) {
	old := claudeScanGOOS
	claudeScanGOOS = "windows"
	t.Cleanup(func() { claudeScanGOOS = old })
	const native = `C:\Users\Example User\.persona\profiles\claude`
	n := &Node{home: t.TempDir(), paths: map[string]string{}, cdirs: newClaudeDirs()}
	scanned := map[int]procEnv{1: {ConfigDir: native}, 2: {ConfigDir: slashed(native)}}
	if runtime.GOOS == "windows" { // filepath.Clean drops a trailing \ only there
		scanned[3] = procEnv{ConfigDir: slashed(native) + "/"}
	}
	n.cdirs.scan = func() map[int]procEnv { return scanned }
	n.refreshClaudeDirs()
	if !slices.Equal(n.cdirs.found, []string{native}) {
		t.Fatalf("found %q, want only %q", n.cdirs.found, native)
	}
	if got := scannedConfigDir("darwin", "/Users/a/b/"); got != "/Users/a/b/" {
		t.Fatalf("off Windows the directory changed: %q", got)
	}
}
