package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// codexProfileKey matches a line that selects a Codex profile (`profile = "…"`, quoted or not), in any table.
var codexProfileKey = lazyre.New(`(?m)^[ \t]*"?profile"?[ \t]*=`)

// codexOwnEffort is e without an effort the Codex that argv starts in cwd already runs (codexEffortInherited).
func (n *Node) codexOwnEffort(ctx context.Context, argv, pane []string, cwd string, e modelExecution) modelExecution {
	if e.Effort != "" && n.codexEffortInherited(ctx, argv, pane, cwd, e.Effort) {
		e.Effort = ""
	}
	return e
}

// codexEffortInherited: the Codex that argv starts in cwd already runs at effort without the flag. True only when sure:
//   - argv runs codex itself (not a managed launcher), changes no home or shell lookup settings, and would join the shared
//     app-server (no own-process flag, no --remote); a bare codex is not a shell function (#4876);
//   - the codex the pane's shell finds (pane holds the engine's pane variables) is the Codex CLI, not a wrapper script
//     that may set its own CODEX_HOME or profile (codexCLIExecutable);
//   - this node's Codex home sets effort at the top level of config.toml, selects no profile and keeps Codex's own
//     project markers;
//   - no project .codex/config.toml Codex may read for cwd names an effort or a profile.
//
// Between the user's file and a -c override, Codex has only the profile and project layers; its managed layers come
// after both and apply either way (codex-rs config_layer_source.rs).
func (n *Node) codexEffortInherited(ctx context.Context, argv, pane []string, cwd, effort string) bool {
	words := argv
	for len(words) > 0 && shellcmd.AssignRe.MatchString(words[0]) {
		key, _, _ := strings.Cut(words[0], "=")
		switch key {
		case "CODEX_HOME", "HOME", "ZDOTDIR", "SHELL", "PATH", "PWD":
			// These can change which config, executable or shell startup files the pane reads.
			return false
		}
		words = words[1:]
	}
	if len(words) == 0 || filepath.Base(words[0]) != "codex" || codexOnRemote(words) || codexOwnProcess(words) != "" {
		return false
	}
	b, err := os.ReadFile(n.codexConfigPath())
	if err != nil || codexProfileKey.Match(b) || bytes.Contains(b, []byte("project_root_markers")) {
		return false
	}
	if v, _ := tomlTopLevel(string(b))["model_reasoning_effort"].(string); v != effort {
		return false
	}
	dirs := []string{cwd}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil && resolved != cwd {
		dirs = append(dirs, resolved) // Codex walks up from the directory as the shell resolved it
	}
	for _, dir := range dirs {
		if !n.noProjectEffort(dir) {
			return false
		}
	}
	if runtime.GOOS == "windows" {
		return true
	}
	exe, home := n.codexStartCLI(ctx, words, pane, argv[:len(argv)-len(words)], cwd)
	return exe != "" && sameDir(home, n.codexHome())
}

// codexStartCLI resolves the native executable and home under the same rc files as codex-start.
// Opaque wrappers can change provider, home or permissions before invoking the CLI.
func (n *Node) codexStartCLI(ctx context.Context, words, pane, assignments []string, cwd string) (string, string) {
	exe := words[0]
	home := firstNonEmpty(os.Getenv("CODEX_HOME"), n.codexHome())
	if runtime.GOOS == "windows" {
		path, err := exec.LookPath(exe)
		if err != nil {
			return "", ""
		}
		path, _, err = codexStartProgram(path, true, nil)
		if err != nil || !strings.EqualFold(filepath.Ext(path), ".exe") {
			return "", ""
		}
		return path, home
	}
	if exe == "codex" {
		// codex-start preserves a same-name shell function. Its flags are not in argv and may select another effort.
		// Otherwise its shell runs the first codex on its PATH, which the pane's rc files may change.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		shell := codexStartShellProgram()
		cmd := exec.CommandContext(ctx, shell, shellFlag(shell), `
if [ -n "${ZSH_VERSION-}" ]; then
  (( $+functions[codex] )) && exit 0
  p=$(whence -p codex) || exit 0
elif [ -n "${BASH_VERSION-}" ]; then
  builtin declare -F codex >/dev/null && exit 0
  p=$(type -P codex) || exit 0
else
  exit 0
fi
printf '\001persona-codex-unshadowed\001%s\001%s\001' "$p" "${CODEX_HOME:-$HOME/.codex}"
`)
		cmd.Dir = cwd
		cmd.Env = append(append(cmd.Environ(), pane...), assignments...)
		cmd.Env = append(cmd.Env, codexStartMarker)
		killGroupOnCancel(cmd)
		out, err := cmd.Output()
		_, found, ok := bytes.Cut(out, []byte("\x01persona-codex-unshadowed\x01"))
		if err != nil || !ok {
			return "", ""
		}
		path, rest, ok := bytes.Cut(found, []byte("\x01"))
		if !ok {
			return "", ""
		}
		homeBytes, _, ok := bytes.Cut(rest, []byte("\x01"))
		if !ok {
			return "", ""
		}
		exe, home = string(path), string(homeBytes)
	}
	if !filepath.IsAbs(exe) {
		exe = filepath.Join(cwd, exe)
	}
	if !codexCLIExecutable(exe) {
		return "", ""
	}
	return exe, home
}

// codexPaneEnv is what a pane of launch's engine holds before its shell reads the rc files, which may choose another
// codex by it: herdr's pane variables and the HERDR_AGENT codex-start runs with.
func codexPaneEnv(launch host.Launcher) []string {
	if _, ok := launch.(*herdrHost); !ok {
		return nil
	}
	return []string{"HERDR_ENV=1", "TERM_PROGRAM=herdr", herdrAgentEnv + "=" + herdrAgentFor(launch, "codex")}
}

// codexCLIExecutable: path is the Codex CLI itself, a native executable, or one of the official launchers that only
// exec it: npm's bin/codex.js, or the /bin/sh shim a desktop app bundles in codex-cli/bin, which execs
// ../CodexCLI.app/Contents/MacOS/codex. Leaving that shim out would take every start through it off the shared
// app-server (#4860). Any other script may set its own CODEX_HOME or profile (#10432).
func codexCLIExecutable(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	if nativeExecutable(real) {
		return true
	}
	if strings.HasSuffix(filepath.ToSlash(real), "/node_modules/@openai/codex/bin/codex.js") {
		return true
	}
	bin := filepath.Dir(real)
	if filepath.Base(real) != "codex" || filepath.Base(bin) != "bin" || filepath.Base(filepath.Dir(bin)) != "codex-cli" {
		return false
	}
	st, err := os.Stat(real)
	if err != nil || st.Size() > 4096 {
		return false
	}
	b, err := os.ReadFile(real)
	return err == nil && bytes.HasPrefix(b, []byte("#!/bin/sh\n")) &&
		bytes.Contains(b, []byte("/../CodexCLI.app/Contents/MacOS/codex\" \"$@\"")) &&
		!bytes.Contains(b, []byte("CODEX_HOME")) && !bytes.Contains(b, []byte("profile")) &&
		nativeExecutable(filepath.Join(filepath.Dir(bin), "CodexCLI.app", "Contents", "MacOS", "codex"))
}

// nativeExecutable: path is a regular file holding a Mach-O or ELF program.
func nativeExecutable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return false
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	switch string(magic[:]) {
	case "\x7fELF",
		"\xfe\xed\xfa\xce", "\xce\xfa\xed\xfe", "\xfe\xed\xfa\xcf", "\xcf\xfa\xed\xfe", // Mach-O, 32 and 64 bit
		"\xca\xfe\xba\xbe", "\xca\xfe\xba\xbf": // universal
		return true
	}
	return false
}

// noProjectEffort: none of the project configs Codex may read for dir names an effort or a profile. Codex reads
// .codex/config.toml in dir and each directory above it up to the project's root, the nearest one holding a .git,
// leaving out the Codex home's own folder (codex-rs config/src/loader/mod.rs, discover_project_layers). Without a
// root Codex reads dir's alone; this then looks at every directory above too, which can only keep the flag more
// often. A file that cannot be read counts as naming one.
func (n *Node) noProjectEffort(dir string) bool {
	home := n.codexHome()
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if dot := filepath.Join(d, ".codex"); !sameDir(dot, home) {
			b, err := os.ReadFile(filepath.Join(dot, "config.toml"))
			switch {
			case err == nil && (bytes.Contains(b, []byte("model_reasoning_effort")) || codexProfileKey.Match(b)):
				return false
			case err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
				return false
			}
		}
		if codexRootMarker(d) || filepath.Dir(d) == d {
			return true
		}
	}
}

// codexRootMarker: dir has Codex's default project marker, a .git directory holding HEAD or a .git file (a linked
// worktree or a submodule).
func codexRootMarker(dir string) bool {
	git := filepath.Join(dir, ".git")
	st, err := os.Stat(git)
	if err != nil {
		return false
	}
	if !st.IsDir() {
		return true
	}
	_, err = os.Stat(filepath.Join(git, "HEAD"))
	return err == nil
}
