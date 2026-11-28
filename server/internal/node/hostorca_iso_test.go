//go:build orcaiso

package node

// Live write tests against an isolated Orca runtime (#17058), Linux only:
//
//	PERSONA_ORCA_ISO_SERVER=<Orca server install> go test -tags orcaiso -run OrcaIsolated ./internal/node/ -v
//
// PERSONA_ORCA_ISO_SERVER is an Orca server install: the directory holding orcad/orcad-server.js and
// runtimes/node-*/bin/node (Orca 1.4.277 checked). Each test starts its own headless Orca server with a temporary HOME,
// profile and loopback port, and drives Persona's own Orca host and client against it: a terminal opened for a start,
// a program started in it, text typed and keys pressed, the terminal closed; a pane split and the new one closed, a tab
// renamed, a tab closed whole, the worktree renamed; a worktree made and removed. Nothing reaches the user's Orca: the
// client is pinned to the temporary runtime's metadata, nothing is written unless that metadata, the runtime that
// answers and the process it names are the temporary ones, the process scan keeps only processes with the temporary
// HOME, and at the end only processes with that HOME are stopped, each by PID after checking its environment, never by
// name. docs/operations/testing.md, "Isolated Orca runtime (#17058)".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
)

// orcaIso is one isolated Orca server: its temporary root (HOME is root/home), the metadata the client is pinned to,
// the process the test started, Persona's client and host on it, and a throwaway repository Orca holds as a worktree.
type orcaIso struct {
	root, home, meta string
	cmd              *exec.Cmd
	exited           chan struct{} // closed once the server this test started has exited
	c                *orcart.Client
	h                *orcaHost
	repo, space      string // the repository and its worktree id
}

// startOrcaIso starts the Orca server PERSONA_ORCA_ISO_SERVER names with a fresh HOME, profile and port, waits until
// it is ready, checks it is the temporary one, and registers a throwaway repository (repo.add). Without the variable,
// or off Linux, the test is skipped.
func startOrcaIso(t *testing.T) *orcaIso {
	t.Helper()
	server := os.Getenv("PERSONA_ORCA_ISO_SERVER")
	if server == "" {
		t.Skip("PERSONA_ORCA_ISO_SERVER is not set: the Orca server install to run in isolation")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the isolation checks read /proc")
	}
	nodes, _ := filepath.Glob(filepath.Join(server, "runtimes", "node-*", "bin", "node"))
	script := filepath.Join(server, "orcad", "orcad-server.js")
	if len(nodes) != 1 {
		t.Fatalf("PERSONA_ORCA_ISO_SERVER: want one runtimes/node-*/bin/node, found %d", len(nodes))
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("PERSONA_ORCA_ISO_SERVER: %v", err)
	}
	// A short root: the server's unix socket lives under it and its path must fit in sun_path.
	root, err := os.MkdirTemp("", "orca-iso-")
	if err != nil {
		t.Fatal(err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	o := &orcaIso{root: root, home: filepath.Join(root, "home")}
	o.meta = filepath.Join(o.home, ".local", "share", "Orca", "orca-runtime.json")
	t.Cleanup(func() { o.stop(t) })
	for _, d := range []string{filepath.Join(o.home, ".config"), filepath.Join(o.home, ".local", "share"), filepath.Join(o.home, ".local", "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	logFile, err := os.Create(filepath.Join(root, "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	o.cmd = exec.Command(nodes[0], script, "--bind", "127.0.0.1", "--port", strconv.Itoa(freeLoopbackPort(t)), "--no-pairing", "--refuse-public")
	o.cmd.Dir = o.home
	// An environment of its own: no ORCA_* variable of the user's Orca, no HOME or XDG directory of theirs.
	o.cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + o.home, "USER=" + os.Getenv("USER"), "LANG=C.UTF-8", "SHELL=/bin/bash",
		"XDG_CONFIG_HOME=" + filepath.Join(o.home, ".config"), "XDG_DATA_HOME=" + filepath.Join(o.home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(o.home, ".local", "state"), "ORCA_USER_DATA_PATH=" + filepath.Join(o.home, ".config", "orca"),
		"GIT_CONFIG_NOSYSTEM=1"}
	o.cmd.Stdout, o.cmd.Stderr = logFile, logFile
	if err := o.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o.exited = make(chan struct{})
	go func() { _ = o.cmd.Wait(); close(o.exited) }()
	waitWithin(t, 90*time.Second, "the isolated Orca server ready", func() bool {
		b, _ := os.ReadFile(filepath.Join(root, "serve.log"))
		_, err := os.Stat(o.meta)
		return bytes.Contains(b, []byte("Orca server ready")) && err == nil
	})
	// Anything in the test process that looks Orca up by ORCA_USER_DATA_PATH (TestMain points it at nothing) finds the
	// temporary runtime.
	t.Setenv("ORCA_USER_DATA_PATH", filepath.Dir(o.meta))
	o.c = &orcart.Client{MetaPath: o.meta, Log: t.Logf}
	o.h = newOrcaHost(o.c)
	o.h.scan = o.scan
	o.guard(t)

	o.repo = filepath.Join(root, "proj")
	o.git(t, root, "init", "-q", "-b", "main", o.repo)
	if err := os.WriteFile(filepath.Join(o.repo, "README.md"), []byte("isolated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.git(t, o.repo, "add", "README.md")
	o.git(t, o.repo, "commit", "-q", "-m", "first")
	ctx := isoCtx(t)
	if err := o.c.RepoAdd(ctx, o.repo, "git"); err != nil {
		t.Fatalf("repo.add: %v", err)
	}
	trees, err := o.c.AllWorktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range trees {
		if (w.HostID == "" || w.HostID == "local") && realPath(filepath.Clean(w.Path)) == o.repo {
			o.space = w.WorktreeID
		}
	}
	if o.space == "" {
		t.Fatalf("repo.add made no worktree for the repository (%d worktrees)", len(trees))
	}
	return o
}

// guard refuses to go on unless the client talks to the temporary runtime (checkMeta), and the runtime that answers is
// the one the metadata names.
func (o *orcaIso) guard(t *testing.T) {
	t.Helper()
	runtimeID, err := o.checkMeta()
	if err != nil {
		t.Fatal(err)
	}
	st, err := o.c.Probe(isoCtx(t))
	if err != nil {
		t.Fatalf("status.get: %v", err)
	}
	if id := o.c.RuntimeID(); runtimeID != "" && id != "" && id != runtimeID {
		t.Fatalf("refusing: runtime %s answered, the metadata names %s", id, runtimeID)
	}
	t.Logf("isolated Orca %s, runtime protocol %d", st.AppVersion, st.RuntimeProtocolVersion)
}

// checkMeta is why the metadata the client is pinned to is not the temporary runtime's, nil when it is: it must be
// under the temporary root and not under the real home, every local socket it lists (what the client dials) must be
// under the temporary root, and a process it names must be the server this test started or have the temporary HOME.
// It returns the runtime id the metadata names.
func (o *orcaIso) checkMeta() (string, error) {
	under := func(path, dir string) bool {
		return strings.HasPrefix(filepath.Clean(path), dir+string(filepath.Separator))
	}
	if !under(o.meta, o.root) {
		return "", fmt.Errorf("refusing: the metadata %s is not under the temporary root", o.meta)
	}
	if real, err := os.UserHomeDir(); err == nil && real != "" && real != "/" && under(o.meta, real) {
		return "", fmt.Errorf("refusing: the metadata %s is under the real home", o.meta)
	}
	b, err := os.ReadFile(o.meta)
	if err != nil {
		return "", fmt.Errorf("refusing: %w", err)
	}
	var m struct {
		PID        int    `json:"pid"`
		RuntimeID  string `json:"runtimeId"`
		Transports []struct {
			Kind     string `json:"kind"`
			Endpoint string `json:"endpoint"`
		} `json:"transports"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", fmt.Errorf("refusing: the metadata does not read: %w", err)
	}
	local := 0
	for _, tr := range m.Transports {
		if tr.Kind != "unix" {
			continue
		}
		local++
		if !under(tr.Endpoint, o.root) {
			return "", fmt.Errorf("refusing: the metadata's socket %s is not under the temporary root", tr.Endpoint)
		}
	}
	if local == 0 {
		return "", errors.New("refusing: the metadata lists no local socket")
	}
	if m.PID != 0 && (o.cmd == nil || m.PID != o.cmd.Process.Pid) && !o.ours(m.PID) {
		return "", fmt.Errorf("refusing: the metadata names pid %d, not a process of the temporary HOME", m.PID)
	}
	return m.RuntimeID, nil
}

// ours: process pid runs with the temporary HOME (its /proc/<pid>/environ holds exactly HOME=<home>).
func (o *orcaIso) ours(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false
	}
	return slices.Contains(strings.Split(string(b), "\x00"), "HOME="+o.home)
}

// processes are the PIDs of every process with the temporary HOME, this test's own process aside.
func (o *orcaIso) processes() []int {
	entries, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid != os.Getpid() && o.ours(pid) {
			out = append(out, pid)
		}
	}
	return out
}

// scan is the host's process scan with only the processes of the temporary HOME: a user's agents in their own Orca
// are never looked at.
func (o *orcaIso) scan() (map[string][]orcaProc, error) {
	all, err := scanOrcaProcs()
	out := map[string][]orcaProc{}
	for key, procs := range all {
		for _, p := range procs {
			if o.ours(p.pid) {
				out[key] = append(out[key], p)
			}
		}
	}
	return out, err
}

// stop ends the server this test started and every process with its temporary HOME (the terminal daemon, the shells,
// what ran in them), each by PID after checking its environment again, never by name; it fails the test when one is
// left. Then it removes the temporary root.
func (o *orcaIso) stop(t *testing.T) {
	t.Helper()
	running := func() bool {
		if o.exited == nil {
			return false
		}
		select {
		case <-o.exited:
			return false
		default:
			return true
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		pids := o.processes()
		if running() && !slices.Contains(pids, o.cmd.Process.Pid) {
			pids = append(pids, o.cmd.Process.Pid) // the server itself, by the PID it was started with
		}
		if len(pids) == 0 {
			break
		}
		for _, pid := range pids {
			if running() && pid == o.cmd.Process.Pid || o.ours(pid) {
				_ = syscall.Kill(pid, sig)
			}
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && (running() || len(o.processes()) > 0); {
			time.Sleep(100 * time.Millisecond) // sleep-ok: polls for the processes to exit, bounded by the deadline
		}
	}
	if left := o.processes(); len(left) > 0 || running() {
		t.Errorf("processes of the temporary HOME still running: %v (the server: %v)", left, running())
	}
	if t.Failed() {
		if b, err := os.ReadFile(filepath.Join(o.root, "serve.log")); err == nil {
			t.Logf("the server's log, its end:\n%s", lastLines(string(b), 40))
		}
	}
	_ = os.RemoveAll(o.root)
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func isoCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// git runs git in dir with only the temporary HOME's configuration and a placeholder identity.
func (o *orcaIso) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + o.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Persona Test",
		"GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Persona Test", "GIT_COMMITTER_EMAIL=test@example.com"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// shows waits for the pane's screen to show want at least n times.
func (o *orcaIso) shows(t *testing.T, pane host.PaneRef, want string, n int) {
	t.Helper()
	waitWithin(t, 20*time.Second, strconv.Itoa(n)+"× "+strconv.Quote(want)+" on the screen", func() bool {
		s, err := o.h.Read(isoCtx(t), pane, host.ReadOpts{Source: "visible", Lines: 60})
		return err == nil && strings.Count(s, want) >= n
	})
}

// inFront waits until Orca names program in front of the pane (want) or no longer does (!want).
func (o *orcaIso) inFront(t *testing.T, pane host.PaneRef, program string, want bool) {
	t.Helper()
	waitWithin(t, 20*time.Second, program+" in front: "+strconv.FormatBool(want), func() bool {
		names, err := o.h.PaneForeground(isoCtx(t), pane)
		return err == nil && len(names) > 0 && slices.Contains(names, program) == want
	})
}

// terminalGone waits until Orca lists no terminal for which gone is true.
func (o *orcaIso) terminalGone(t *testing.T, what string, gone func(orcart.Terminal) bool) {
	t.Helper()
	waitWithin(t, 20*time.Second, what, func() bool {
		terms, err := o.c.Terminals(isoCtx(t), false)
		return err == nil && !slices.ContainsFunc(terms.Terminals, gone)
	})
}

// A terminal opened for a start, a program started in it, text typed and keys pressed, then the terminal closed.
func TestOrcaIsolatedStartSendKeysClose(t *testing.T) {
	o := startOrcaIso(t)
	ctx := isoCtx(t)
	created, err := o.h.CreateSlot(ctx, o.space, o.repo, "iso-start")
	if err != nil {
		t.Fatalf("open a terminal for a start: %v", err)
	}
	pane := created.Pane
	o.inFront(t, pane, "cat", false) // the shell is up
	// A shell at rest takes no typed text: it would run it.
	if err := o.h.SendText(ctx, pane, "refused"); !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("typing into a shell at rest = %v, want refused with nothing typed", err)
	}
	if err := o.h.Run(ctx, pane, "cat"); err != nil {
		t.Fatalf("start a program: %v", err)
	}
	if err := o.h.Run(ctx, pane, "cat"); !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("a second start in the same terminal = %v, want refused", err)
	}
	o.inFront(t, pane, "cat", true)
	if err := o.h.SendText(ctx, pane, "persona-iso-line"); err != nil {
		t.Fatalf("type: %v", err)
	}
	if err := o.h.SendKeys(ctx, pane, "enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	o.shows(t, pane, "persona-iso-line", 2) // the line as typed, and cat's copy of it
	if err := o.h.SendKeys(ctx, pane, "ctrl+c"); err != nil {
		t.Fatalf("ctrl+c: %v", err)
	}
	o.inFront(t, pane, "cat", false)
	if err := o.h.Close(ctx, pane, created.SpaceID, created.SlotID); err != nil {
		t.Fatalf("close: %v", err)
	}
	o.terminalGone(t, "the terminal closed", func(term orcart.Terminal) bool { return term.PaneKey() == pane.ID })
}

// A pane split in two and the new one closed, the tab renamed, a second tab closed whole, the worktree renamed.
func TestOrcaIsolatedSplitRenameCloseTab(t *testing.T) {
	o := startOrcaIso(t)
	ctx := isoCtx(t)
	first, err := o.h.OpenShell(ctx, o.space, o.repo, "iso-tab")
	if err != nil {
		t.Fatalf("open a shell: %v", err)
	}
	o.inFront(t, first.Pane, "cat", false)
	second, err := o.h.Split(ctx, first.Pane, "right", o.repo)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if second.ID == "" || second.ID == first.Pane.ID {
		t.Fatalf("the split made %v", second)
	}
	if !o.h.CanSplit() || !o.h.CanCloseSlot() {
		t.Fatalf("after a split the host offers split %v, close tab %v", o.h.CanSplit(), o.h.CanCloseSlot())
	}
	if err := o.h.Close(ctx, second, first.SpaceID, first.SlotID); err != nil {
		t.Fatalf("close the new pane: %v", err)
	}
	o.terminalGone(t, "the new pane closed", func(term orcart.Terminal) bool { return term.PaneKey() == second.ID })
	if err := o.h.RenameSlot(ctx, first.SlotID, "iso-renamed"); err != nil {
		t.Fatalf("rename the tab: %v", err)
	}
	waitWithin(t, 20*time.Second, "the tab's new name", func() bool {
		_, custom, err := o.c.TabNames(isoCtx(t), "id:"+o.space)
		return err == nil && custom[first.SlotID] == "iso-renamed"
	})
	other, err := o.h.OpenShell(ctx, o.space, o.repo, "iso-other")
	if err != nil {
		t.Fatalf("open a second tab: %v", err)
	}
	o.inFront(t, other.Pane, "cat", false)
	if err := o.h.CloseSlot(ctx, other.SlotID); err != nil {
		t.Fatalf("close the second tab: %v", err)
	}
	o.terminalGone(t, "the second tab closed", func(term orcart.Terminal) bool { return term.TabID == other.SlotID })
	if err := o.h.RenameSpace(ctx, o.space, "iso-space"); err != nil {
		t.Fatalf("rename the worktree: %v", err)
	}
	waitWithin(t, 20*time.Second, "the worktree's new name", func() bool {
		spaces, err := o.h.Spaces(isoCtx(t))
		return err == nil && slices.ContainsFunc(spaces, func(s host.Space) bool { return s.ID == o.space && s.DisplayName == "iso-space" })
	})
}

// A worktree made through Orca (New worktree…), then removed as an Agent's worktree is (worktree.rm), its branch kept.
func TestOrcaIsolatedWorktreeCreateRemove(t *testing.T) {
	o := startOrcaIso(t)
	ctx := isoCtx(t)
	created, err := o.h.CreateWorktree(ctx, o.space, "iso-wt", filepath.Join(o.h.WorktreesDir(o.home), "proj", "iso-wt"))
	if err != nil {
		t.Fatalf("create the worktree: %v", err)
	}
	if created.Branch != "iso-wt" || created.Path == "" || created.SpaceID == "" {
		t.Fatalf("created %+v", created)
	}
	if fi, err := os.Stat(created.Path); err != nil || !fi.IsDir() {
		t.Fatalf("the worktree's folder: %v", err)
	}
	w := o.h.agentWorktreeAt(ctx, created.Path)
	if w == nil {
		t.Fatalf("Orca does not list the new worktree at %s as one Persona may remove", created.Path)
	}
	n := &Node{home: o.home, paths: map[string]string{}, agents: map[string]host.Agent{}, panes: map[string]string{},
		panePIDs: map[string]panePID{}, state: newState(), cdirs: newClaudeDirs()}
	n.hosts = host.NewRegistry(o.h)
	res, done, err := n.orcaAgentWorktreeRemove(ctx, o.h, w, false, false)
	if err != nil || !done || !res.Removed || res.BranchDeleted {
		t.Fatalf("remove = %+v, done %v, err %v", res, done, err)
	}
	if _, err := os.Stat(created.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the worktree's folder is still there: %v", err)
	}
	if out := o.git(t, o.repo, "branch", "--list", "iso-wt"); !strings.Contains(out, "iso-wt") {
		t.Fatalf("the branch was not kept: %q", out)
	}
}

// The tests refuse to write to any Orca but the temporary one: metadata outside the temporary root (a user's profile),
// a socket outside it, none at all, or a process that is not of the temporary HOME. No server is needed for this one.
func TestOrcaIsolatedRefusesAnotherProfile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the isolation checks read /proc")
	}
	root, profile := t.TempDir(), t.TempDir()
	o := &orcaIso{root: root, home: filepath.Join(root, "home")}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unix := func(socket string) string {
		return `{"runtimeId":"r1","transports":[{"kind":"unix","endpoint":"` + socket + `"}]}`
	}
	elsewhere := filepath.Join(profile, ".config", "orca", "orca-runtime.json") // a profile that is not the test's
	write(elsewhere, unix(filepath.Join(profile, "o.sock")))
	inside := filepath.Join(o.home, ".local", "share", "Orca", "orca-runtime.json")
	for _, tc := range []struct{ name, meta, body string }{
		{"metadata of another profile", elsewhere, ""},
		{"a socket outside the temporary root", inside, unix(filepath.Join(profile, "o.sock"))},
		{"no local socket", inside, `{"runtimeId":"r1","transports":[{"kind":"websocket","endpoint":"ws://127.0.0.1:1"}]}`},
		{"a process of another HOME", inside, `{"pid":` + strconv.Itoa(os.Getpid()) + `,"transports":[{"kind":"unix","endpoint":"` +
			filepath.Join(root, "o.sock") + `"}]}`},
	} {
		if tc.body != "" {
			write(tc.meta, tc.body)
		}
		o.meta = tc.meta
		if _, err := o.checkMeta(); err == nil || !strings.HasPrefix(err.Error(), "refusing") {
			t.Errorf("%s: checkMeta = %v, want a refusal", tc.name, err)
		}
	}
	write(inside, unix(filepath.Join(root, "o.sock")))
	o.meta = inside
	if id, err := o.checkMeta(); err != nil || id != "r1" {
		t.Errorf("the temporary runtime's metadata: %q, %v", id, err)
	}
}
