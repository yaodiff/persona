package node

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
)

// The node knows its own program two ways (#10365). exe is the file itself, links followed: what an update replaces
// (never a link to it) and what macOS attributes Full Disk Access to. bin is how the processes the node starts are
// told to run it: a managed launcher's typed command, codex-start and tab-start lines, the generated hooks, settings
// and MCP configs, and the companions it supervises (`--personad-bin`). A terminal can run somewhere that sees the
// home only as $HOME names it, such as a container that
// mounts the home at /home/me while the host reaches it through /home/me -> /data/home/me: there the resolved file's
// path does not exist ("No such file or directory"), though the data directory, where the start files are, does.
// So bin is the data directory's spelling of the same file, else its $HOME spelling, else exe as it is.

// agentProgram is the spelling of exe handed to the processes the node starts: dir/bin/<exe's name> (the data
// directory, which PERSONA_HOME names and the start files already live in), then exe under home's own spelling when
// home is a link and exe lies under its target. The first that is the same file as exe wins; exe when none is (the
// file is not there, nothing is a link, or the data directory holds another build).
func agentProgram(exe, home, dir string) string {
	st, err := os.Stat(exe)
	if err != nil {
		return exe
	}
	var candidates []string
	if dir != "" {
		candidates = append(candidates, filepath.Join(dir, "bin", filepath.Base(exe)))
	}
	candidates = append(candidates, homeSpelling(exe, home))
	for _, c := range candidates {
		if c == "" || c == exe {
			continue
		}
		if cst, err := os.Stat(c); err == nil && os.SameFile(st, cst) {
			return c
		}
	}
	return exe
}

// homeSpelling is p with the home's real path (realHome) written as home itself, when home is a link and p lies
// under its target; "" otherwise. It is the same place by construction: home resolves to that target.
func homeSpelling(p, home string) string {
	real := realHome(home)
	if real == "" || !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	if p == real {
		return filepath.Clean(home)
	}
	if rest, ok := strings.CutPrefix(p, real+string(filepath.Separator)); ok {
		return filepath.Join(home, rest)
	}
	return ""
}

// visiblePath is p as a terminal on this computer opens it: homeSpelling when that is the same file or directory
// as p, else p unchanged. Paths the node hands out for an Agent to start in, and git's links between a worktree and
// its repository, use it; comparisons keep using realPath.
func (n *Node) visiblePath(p string) string {
	h := homeSpelling(p, n.home)
	if h == "" {
		return p
	}
	a, aerr := os.Stat(p)
	b, berr := os.Stat(h)
	if aerr != nil || berr != nil || !os.SameFile(a, b) {
		return p
	}
	return h
}

// selfProgram is this program's file, links followed (exe), and agentProgram's spelling of it (bin) for the data
// directory dir of home. The node and `personad node doctor` both ask it, so the generated files the doctor
// compares are the ones the node wrote. Tests replace it.
var selfProgram = func(home, dir string) (exe, bin string, err error) {
	exe, err = os.Executable()
	if err != nil {
		return "", "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, agentProgram(exe, home, dir), nil
}

// ownProgram is bin for this machine's home and data directory, worked out as the node and `personad node doctor` do:
// what the Antigravity companion a RuntimeHost starts is told to run for Persona's tools (`--personad-bin`, #14582),
// whether or not a hub connection's Node is there yet. "" when it cannot be told.
func ownProgram() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	_, bin, err := selfProgram(home, identity.DataDir(home))
	if err != nil {
		return ""
	}
	return bin
}
