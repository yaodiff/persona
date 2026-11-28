package node

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/osproc"
)

// Before SessionStart, Claude Code reads its settings, its skills, commands and agents (following the links among
// them) and the working directory. On a Mac, a read in a privacy-protected folder (TCC: Desktop, Documents,
// Downloads) by a program that is not allowed there is refused, or waits on a consent prompt nobody answers for a node
// started by launchd or ssh: claude stops at "Loading commands and agents", no hook fires, and on the phone the
// session never starts. The preflight (`node doctor`'s path-access check, #5944) reads the same paths the same way,
// in a child of its own (this program again, `node doctor --preflight-child`: own process group, cwd /) that is killed
// whole once a single read has taken preflightStall, and says which path is blocked and the two ways out. It never
// starts claude. The child is a Go program rather than a shell script running ls for each path: on a Mac whose
// security software inspects every exec, a hundred linked skills took 5 s that way.

const pathAccessCheck = "path-access"

// preflightStall: a read that has not returned after this long is blocked.
const preflightStall = 3 * time.Second

// preflightStartDefault is how long the child may take to say its first line (its exec, its runtime coming up):
// starting is not a read. One stall, so a whole run is still preflightMaxStalls stalls.
const preflightStartDefault = preflightStall

// preflightStart is preflightStartDefault but in tests, whose stalls are short (TestMain): under the whole server
// suite's load the test binary took longer than one of those to come up (#1110).
var preflightStart = preflightStartDefault

// preflightMaxStalls caps a whole run at this many stalls: the child's start (preflightStart) is the first, then the
// reads, ones that each return but slowly (a sleeping network disk) included; what was not read by then is counted,
// not blamed.
const preflightMaxStalls = 5

// preflightLinger is what stopping the child may add after that: SweepGroup's second and the two seconds runPreflight
// gives it to exit.
const preflightLinger = 3 * time.Second

// preflightAnswerWait is how long `node doctor` waits for the node's answer: longer than the node's longest run
// (its start, preflightMaxStalls-1 more stalls and preflightLinger), so a slow run is never taken for a node that
// does not answer.
const preflightAnswerWait = preflightStartDefault + (preflightMaxStalls-1)*preflightStall + preflightLinger + 5*time.Second

// preflightMaxDirs caps the working directories read: the most recently used.
const preflightMaxDirs = 20

// preflightItem is one path the preflight reads.
type preflightItem struct {
	kind   string // "dir": list it; "file": read a byte; "links": list it, then read through each symlink in it
	path   string
	role   string // what it is to claude, in the report
	global bool   // every claude start reads it (Claude Code's own directory), not only the starts in one directory
}

// Stable role IDs stay independent of the display language. Tests and older internal fixtures may supply an opaque
// role, which is preserved as data rather than parsed.
const (
	preflightRoleClaudeDir      = "claude_dir"
	preflightRoleClaudeSettings = "claude_settings"
	preflightRoleWorkDir        = "work_dir"
)

// preflightItems are the paths a claude start reads: Claude Code's directory (claudeDir), its settings and
// instructions, the skills, commands and agents there; then each working directory, the assistant's first, with its
// own .claude/skills, commands and agents. Paths that are not absolute are left out.
func preflightItems(claudeDir string, workDirs []string) []preflightItem {
	var items []preflightItem
	seen := map[string]bool{}
	add := func(kind, path, role string, global bool) {
		if path == "" || !filepath.IsAbs(path) || seen[kind+"\x00"+path] {
			return
		}
		seen[kind+"\x00"+path] = true
		items = append(items, preflightItem{kind: kind, path: filepath.Clean(path), role: role, global: global})
	}
	add("dir", claudeDir, preflightRoleClaudeDir, true)
	for _, f := range []string{"settings.json", "settings.local.json", "CLAUDE.md"} {
		add("file", filepath.Join(claudeDir, f), preflightRoleClaudeSettings, true)
	}
	for _, d := range []string{"skills", "commands", "agents"} {
		add("links", filepath.Join(claudeDir, d), preflightRoleClaudeSettings, true)
	}
	for _, w := range workDirs {
		add("dir", w, preflightRoleWorkDir, false)
		for _, d := range []string{"skills", "commands", "agents"} {
			add("links", filepath.Join(w, ".claude", d), preflightRoleWorkDir, false)
		}
	}
	return items
}

// preflightChildArg, as `node doctor`'s first argument, makes it the preflight's child (preflightChild).
const preflightChildArg = "--preflight-child"

// preflightHello is the child's first line: a program that is not one (an older binary at the same path) says nothing.
const preflightHello = "preflight 1"

// preflightChild reads each "<d|f|l>:<path>" of args and says on out what happened, a line each, tab-separated:
// "start <id>" before a read (so the read that hangs is the last one started), then "ok <id>", "none <id>" (not there)
// or "fail <id> <error>"; "link <id> <path> <target>" for a symlink found in an "l" directory, whose read follows as
// <id> (the directory's id, a dot and a number). Every line is written at once: the parent takes silence for a hang.
func preflightChild(args []string, out io.Writer) {
	fmt.Fprintln(out, preflightHello)
	for i, a := range args {
		kind, path, ok := strings.Cut(a, ":")
		if !ok {
			continue
		}
		id := strconv.Itoa(i + 1)
		if kind == "l" {
			childLinks(out, id, path)
		} else {
			childRead(out, id, path)
		}
	}
}

// childRead reads path the way claude does: a directory's first entry, a file's first byte. Whether it is there is
// not asked first: macOS refuses stat in a protected folder too.
func childRead(out io.Writer, id, path string) {
	fmt.Fprintf(out, "start\t%s\n", id)
	childSay(out, id, readOne(path))
}

func readOne(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		_, err = f.Readdirnames(1)
	} else {
		_, err = f.Read(make([]byte, 1))
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// childLinks lists dir and reads through each symlink in it.
func childLinks(out io.Writer, id, dir string) {
	fmt.Fprintf(out, "start\t%s\n", id)
	ents, err := os.ReadDir(dir)
	if childSay(out, id, err); err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		n++
		sub, link := id+"."+strconv.Itoa(n), filepath.Join(dir, e.Name())
		target, _ := os.Readlink(link)
		fmt.Fprintf(out, "link\t%s\t%s\t%s\n", sub, oneField(link), oneField(target))
		childRead(out, sub, link)
	}
}

func childSay(out io.Writer, id string, err error) {
	switch {
	case err == nil:
		fmt.Fprintf(out, "ok\t%s\n", id)
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		fmt.Fprintf(out, "none\t%s\n", id)
	default:
		fmt.Fprintf(out, "fail\t%s\t%s\n", id, oneField(err.Error()))
	}
}

// oneField keeps s on its line and in its field.
func oneField(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}

// preflightResult is what reading one path came to.
type preflightResult struct {
	item   int    // the item it was read for (for a link: the directory the link is in)
	path   string // the item's path, or the link's
	target string // a link's target as written
	link   bool
	state  string // ok | missing | denied | error | hung
	msg    string
}

// preflightOutcome is a whole run: the results in the order read, how many items were never reached (a read before
// them hung, or the run took too long), and an error when the child could not run or did not do it.
type preflightOutcome struct {
	results     []preflightResult
	unchecked   int
	err         error
	unsupported string // an older child program's base name; wording is chosen by the parent display path
}

// runPreflight reads items in a child process (env.child) and kills its whole process group as soon as one read has
// taken env.stall (or the child has said nothing env.start, one stall if unset, after it was started, or the run has
// taken that start and preflightMaxStalls-1 more stalls, or ctx ends).
func runPreflight(ctx context.Context, items []preflightItem, env preflightEnv) preflightOutcome {
	if len(items) == 0 {
		return preflightOutcome{}
	}
	if len(env.child) == 0 {
		return preflightOutcome{err: errors.New("no program to read with")}
	}
	stall, start := env.stall, cmp.Or(env.start, env.stall)
	args := slices.Clone(env.child[1:])
	for _, it := range items {
		args = append(args, it.kind[:1]+":"+it.path)
	}
	cmd := exec.Command(env.child[0], args...)
	cmd.Dir = "/" // not inside a folder the reads are about
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "LC_ALL=C"}, env.childEnv...)
	cmd.SysProcAttr = osproc.GroupAttr()
	out := &progressBuffer{moved: make(chan struct{}, 1)}
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return preflightOutcome{err: err}
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait() // a read that fails is in the output; the exit status says nothing more
		close(done)
	}()
	idle := time.NewTimer(start) // until its first line: starting is not a read
	defer idle.Stop()
	all := time.NewTimer(start + (preflightMaxStalls-1)*stall)
	defer all.Stop()
	finished, hung := false, false
wait:
	for {
		select {
		case <-done:
			finished = true
			break wait
		case <-out.moved:
			idle.Reset(stall)
		case <-idle.C:
			hung = true
			break wait
		case <-all.C: // still going, only slowly: no single read is to blame
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	if finished {
		o := parsePreflight(out.String(), items, true, false)
		if !strings.HasPrefix(out.String(), preflightHello+"\n") {
			o.unsupported = filepath.Base(env.child[0])
		}
		return o
	}
	// What was said before the kill: nothing written while it dies counts.
	said := out.String()
	pid := cmd.Process.Pid
	_ = osproc.StopGroup(pid, true)
	osproc.SweepGroup(pid, time.Second) // with the wait below: preflightLinger
	select {
	case <-done:
	case <-time.After(preflightLinger - time.Second): // a read stuck in the kernel can outlive SIGKILL for a while: not waited for
	}
	return parsePreflight(said, items, false, hung)
}

// progressBuffer keeps what the child writes and signals each write.
type progressBuffer struct {
	mu    sync.Mutex
	b     bytes.Buffer
	moved chan struct{}
}

func (p *progressBuffer) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.b.Write(b)
	p.mu.Unlock()
	select {
	case p.moved <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (p *progressBuffer) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b.String()
}

// parsePreflight turns the child's lines into results. When it was stopped for a read that hung, the read it last
// started is that one.
func parsePreflight(out string, items []preflightItem, finished, hung bool) preflightOutcome {
	var o preflightOutcome
	byID := map[string]int{} // id -> index in o.results
	itemOf := func(id string) (int, bool) {
		top, _, _ := strings.Cut(id, ".")
		i, err := strconv.Atoi(top)
		return i - 1, err == nil && i >= 1 && i <= len(items)
	}
	inflight, started := "", 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		id := f[1]
		item, ok := itemOf(id)
		if !ok {
			continue
		}
		switch f[0] {
		case "link":
			if len(f) >= 4 {
				byID[id] = len(o.results)
				o.results = append(o.results, preflightResult{item: item, path: f[2], target: f[3], link: true})
			}
		case "start":
			if _, ok := byID[id]; !ok {
				byID[id] = len(o.results)
				o.results = append(o.results, preflightResult{item: item, path: items[item].path})
			}
			if !strings.Contains(id, ".") {
				started = item + 1
			}
			inflight = id
		case "ok", "fail", "none":
			i, ok := byID[id]
			if !ok {
				continue
			}
			r := &o.results[i]
			r.state = "ok"
			if f[0] == "none" {
				r.state = "missing"
			}
			if f[0] == "fail" {
				r.msg = strings.TrimSpace(strings.Join(f[2:], " "))
				r.state = failState(r.msg)
			}
			if inflight == id {
				inflight = ""
			}
		}
	}
	if !finished {
		if i, ok := byID[inflight]; ok && inflight != "" && hung {
			o.results[i].state = "hung"
		}
		o.unchecked = len(items) - started
	}
	// Whatever was started but never said how it went (the child killed between lines) is left out.
	o.results = slices.DeleteFunc(o.results, func(r preflightResult) bool { return r.state == "" })
	return o
}

// failReason is the end of an error that names the path first ("open /a/b: operation not permitted"): the error.
func failReason(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return strings.TrimSpace(msg[i+2:])
	}
	return msg
}

// failState reads an error the child gave.
func failState(msg string) string {
	switch msg := strings.ToLower(msg); {
	case strings.Contains(msg, "no such file"), strings.Contains(msg, "not a directory"):
		return "missing"
	case strings.Contains(msg, "operation not permitted"), strings.Contains(msg, "permission denied"):
		return "denied"
	}
	return "error"
}

// tccRefused says whether msg is macOS privacy (EPERM) rather than file permissions (EACCES).
func tccRefused(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "operation not permitted")
}

// symlinkLoop says whether msg is a circular symlink error (ELOOP).
func symlinkLoop(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "too many levels of symbolic links")
}

// launchedVia says what started this process, for macOS privacy (TCC) wording: TCC holds a read to account against
// the "responsible" program, which is sshd-keygen-wrapper for anything started over ssh, the program itself for a
// LaunchAgent's (launchd sets XPC_SERVICE_NAME to the agent's label), and the terminal or app otherwise.
// "ssh", "launchd" or "".
func launchedVia(getenv func(string) string) string {
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" {
		return "ssh"
	}
	if x := getenv("XPC_SERVICE_NAME"); x != "" && x != "0" && !strings.HasPrefix(x, "application.") {
		return "launchd"
	}
	return ""
}

// fullDiskAccessProgram is the program TCC holds this node's reads to when it was started under launchd (via): the
// program of the agent it runs under (label, its XPC_SERVICE_NAME; launchdProgram), which is the node itself under
// the node's own agent and the built-in manager's program under the manager's agent; exe otherwise, or when the agent's
// plist says none.
func fullDiskAccessProgram(via, home, label, exe string) string {
	if via == "launchd" {
		if p := launchdProgram(home, label); p != "" {
			return p
		}
	}
	return exe
}

// fullDiskAccessFor is, in the user's words, the program to give Full Disk Access to.
func fullDiskAccessFor(via, exe string, lang i18n.Lang) string {
	switch via {
	case "launchd":
		if exe == "" {
			exe = "personad"
		}
		return i18n.T(lang, "node-doctor.full_disk_access_launchd", i18n.Args{"program": exe})
	case "ssh":
		return i18n.T(lang, "node-doctor.full_disk_access_ssh")
	}
	return i18n.T(lang, "node-doctor.full_disk_access_terminal")
}

// protectedFolderTop is the protected folder's stable directory name (Desktop, Documents, Downloads), "" when none.
// The display name is chosen only at the report boundary.
func protectedFolderTop(path, home string) string {
	rel, err := filepath.Rel(home, path)
	if home == "" || err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	top, _, _ := strings.Cut(rel, string(filepath.Separator))
	if _, ok := protectedDirs[top]; !ok {
		return ""
	}
	return top
}

// protectedFolderOf names the protected folder (Desktop, Documents, Downloads) path is in, "" when none.
// Its legacy Chinese result also serves explainDenied in files.go, so that shared behavior stays unchanged.
func protectedFolderOf(path, home string) string {
	return protectedDirs[protectedFolderTop(path, home)]
}

// preflightEnv is how the preflight runs and what its words depend on.
type preflightEnv struct {
	goos, home string
	via, exe   string // launchedVia, os.Executable
	fda        string // the program to give Full Disk Access to (fullDiskAccessProgram); exe when ""
	// stall: a read taking this long is blocked; start: how long the child has to say its first line (its exec, its
	// runtime coming up).
	stall, start time.Duration
	// child is the program and first arguments that run preflightChild (the items follow); childEnv adds to its
	// environment.
	child, childEnv []string
}

// preflightChildCmd is how the preflight's child is started: this program (exe) in child mode. Tests start the test
// binary instead (TestMain in hostorca_test.go).
var preflightChildCmd = func(exe string) (args, env []string) {
	return []string{exe, "node", "doctor", preflightChildArg}, nil
}

func thisPreflightEnv(home string) preflightEnv {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	via := launchedVia(os.Getenv)
	env := preflightEnv{goos: runtime.GOOS, home: home, via: via, exe: exe, stall: preflightStall, start: preflightStart,
		fda: fullDiskAccessProgram(via, home, os.Getenv("XPC_SERVICE_NAME"), exe)}
	env.child, env.childEnv = preflightChildCmd(exe)
	return env
}

// pathPreflight runs the preflight on items and reports it as the path-access check.
func pathPreflight(ctx context.Context, items []preflightItem, env preflightEnv) DoctorCheck {
	return pathPreflightIn(ctx, items, env, i18n.Default)
}

func pathPreflightIn(ctx context.Context, items []preflightItem, env preflightEnv, lang i18n.Lang) DoctorCheck {
	if env.goos == "windows" {
		return DoctorCheck{Name: pathAccessCheck, Status: DoctorOK, Detail: i18n.T(lang, "node-doctor.preflight_windows"), Items: []string{}}
	}
	return preflightCheckIn(runPreflight(ctx, items, env), items, env, lang)
}

// preflightCheck says what a run found: each blocked path, and what to do about it.
func preflightCheck(o preflightOutcome, items []preflightItem, env preflightEnv) DoctorCheck {
	return preflightCheckIn(o, items, env, i18n.Default)
}

func preflightCheckIn(o preflightOutcome, items []preflightItem, env preflightEnv, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: pathAccessCheck, Items: []string{}}
	if o.unsupported != "" {
		c.Status = DoctorWarn
		reason := i18n.T(lang, "node-doctor.preflight_child_unsupported", i18n.Args{"program": o.unsupported})
		c.Detail = i18n.T(lang, "node-doctor.preflight_check_failed", i18n.Args{"error": shorten(reason, 80)})
		return c
	}
	if o.err != nil {
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.preflight_check_failed", i18n.Args{"error": shorten(o.err.Error(), 80)})
		return c
	}
	var problems []preflightResult
	read, dangling := 0, 0
	for _, r := range o.results {
		switch {
		case r.state == "ok":
			read++
		case r.state == "missing" && r.link:
			dangling++
			c.Items = append(c.Items, i18n.T(lang, "node-doctor.preflight_dangling_item", i18n.Args{"path": r.path, "target": r.target}))
		case r.state == "missing": // a working directory since removed, a settings file the user never made
		default:
			problems = append(problems, r)
		}
	}
	if len(problems) == 0 {
		c.Status = DoctorOK
		c.Detail = i18n.T(lang, "node-doctor.preflight_all_readable", i18n.Args{"count": read})
		if dangling > 0 {
			c.Detail = i18n.T(lang, "node-doctor.preflight_all_readable_with_dangling", i18n.Args{"count": read, "dangling": dangling})
		}
		if o.unchecked > 0 {
			c.Status = DoctorWarn
			c.Detail = i18n.T(lang, "node-doctor.preflight_partial_readable", i18n.Args{"count": read})
			c.Items = append(c.Items, uncheckedIn(o, lang))
			c.Hint = i18n.T(lang, "node-doctor.preflight_slow_hint")
		}
		return c
	}
	c.Status = DoctorWarn
	tcc := -1      // the first problem macOS privacy is behind, an index in problems
	onVolume := "" // the first hung path on a mounted volume
	var problemItems []string
	for i, r := range problems {
		it := items[r.item]
		if it.global {
			c.Status = DoctorFail
		}
		shown := preflightShown(r, it, lang)
		mac := env.goos == "darwin"
		switch r.state {
		case "hung":
			problemItems = append(problemItems, i18n.T(lang, "node-doctor.preflight_hung_item", i18n.Args{"wait": env.stall, "path": shown}))
			// A hang in a protected folder is macOS holding the read; anywhere else it is as likely a mount that
			// stopped answering (a network disk, FUSE).
			if mac && tcc < 0 && protectedFolderTop(blockedPath(r), env.home) != "" {
				tcc = i
			}
			if mac && onVolume == "" && strings.HasPrefix(blockedPath(r), "/Volumes/") {
				onVolume = blockedPath(r)
			}
		case "denied":
			problemItems = append(problemItems, i18n.T(lang, "node-doctor.preflight_denied_item", i18n.Args{"path": shown, "reason": failReason(r.msg)}))
			if mac && tcc < 0 && tccRefused(r.msg) {
				tcc = i
			}
		default:
			problemItems = append(problemItems, i18n.T(lang, "node-doctor.preflight_read_failed_item", i18n.Args{"path": shown, "reason": shorten(failReason(r.msg), 80)}))
		}
	}
	c.Items = append(problemItems, c.Items...)
	if o.unchecked > 0 {
		c.Items = append(c.Items, uncheckedIn(o, lang))
	}
	first := problems[0]
	where := first.path
	if first.link {
		where = first.path + " → " + first.target
	}
	who := i18n.T(lang, "node-doctor.preflight_who_here")
	if items[first.item].global {
		who = i18n.T(lang, "node-doctor.preflight_who_every")
	}
	if first.state == "hung" {
		c.Detail = i18n.T(lang, "node-doctor.preflight_hung_detail", i18n.Args{"path": where, "wait": env.stall, "who": who})
	} else {
		c.Detail = i18n.T(lang, "node-doctor.preflight_read_failed_detail", i18n.Args{"path": where, "reason": shorten(failReason(first.msg), 60), "who": who})
	}
	if len(problems) > 1 {
		c.Detail = i18n.T(lang, "node-doctor.preflight_detail_with_count", i18n.Args{"detail": c.Detail, "count": len(problems)})
	}
	if tcc < 0 {
		if symlinkLoop(first.msg) {
			c.Hint = i18n.T(lang, "node-doctor.preflight_symlink_loop_hint")
			return c
		}
		c.Hint = i18n.T(lang, "node-doctor.preflight_mount_hint")
		if onVolume != "" { // macOS asks before a program reads a network or removable volume, too
			c.Hint = i18n.T(lang, "node-doctor.preflight_volume_hint", i18n.Args{
				"hint": c.Hint, "program": fullDiskAccessFor(env.via, cmp.Or(env.fda, env.exe), lang),
			})
		}
		return c
	}
	folder := i18n.T(lang, "node-doctor.preflight_protected_folders_generic")
	if top := protectedFolderTop(blockedPath(problems[tcc]), env.home); top != "" {
		folder = i18n.T(lang, "node-doctor.preflight_protected_folder_named", i18n.Args{"folder": preflightFolderName(top, lang)})
	}
	target := i18n.T(lang, "node-doctor.preflight_it")
	if tcc > 0 { // not the path the detail names
		target = blockedPath(problems[tcc])
	}
	c.Hint = i18n.T(lang, "node-doctor.preflight_tcc_hint", i18n.Args{
		"target": target, "program": fullDiskAccessFor(env.via, cmp.Or(env.fda, env.exe), lang), "folder": folder,
	})
	return c
}

func preflightRoleText(role string, lang i18n.Lang) string {
	key := map[string]string{
		preflightRoleClaudeDir:      "node-doctor.role_claude_dir",
		preflightRoleClaudeSettings: "node-doctor.role_claude_settings",
		preflightRoleWorkDir:        "node-doctor.role_work_dir",
	}[role]
	if key == "" {
		return role
	}
	return i18n.T(lang, key)
}

func preflightShown(r preflightResult, it preflightItem, lang i18n.Lang) string {
	args := i18n.Args{"path": r.path, "target": r.target, "role": preflightRoleText(it.role, lang)}
	if r.link {
		return i18n.T(lang, "node-doctor.preflight_shown_link", args)
	}
	return i18n.T(lang, "node-doctor.preflight_shown_path", args)
}

func preflightFolderName(top string, lang i18n.Lang) string {
	key := map[string]string{
		"Desktop": "node-doctor.folder_desktop", "Documents": "node-doctor.folder_documents", "Downloads": "node-doctor.folder_downloads",
	}[top]
	if key == "" {
		return top
	}
	return i18n.T(lang, key)
}

// blockedPath is where r's read went: a link's target (made absolute), the item's path otherwise.
func blockedPath(r preflightResult) string {
	if !r.link {
		return r.path
	}
	if filepath.IsAbs(r.target) {
		return filepath.Clean(r.target)
	}
	return filepath.Join(filepath.Dir(r.path), r.target)
}

// unchecked says how many items were never read, and why.
func unchecked(o preflightOutcome) string {
	return uncheckedIn(o, i18n.Default)
}

func uncheckedIn(o preflightOutcome, lang i18n.Lang) string {
	key := "node-doctor.preflight_unchecked_slow"
	if slices.ContainsFunc(o.results, func(r preflightResult) bool { return r.state == "hung" }) {
		key = "node-doctor.preflight_unchecked_hung"
	}
	return i18n.T(lang, key, i18n.Args{"count": o.unchecked})
}

// workDirs are the working directories of this node's sessions, most recently used first, at most `most` of them: the
// assistant's first (what the node starts claude in most), then the ones sessions ran in.
func (n *Node) workDirs(most int) []string {
	used := n.dirsByUse()
	dirs := make([]string, 0, len(used))
	for p := range used {
		if !identity.IsAssistantDir(p) {
			dirs = append(dirs, p)
		}
	}
	slices.SortFunc(dirs, func(a, b string) int {
		if c := used[b].Compare(used[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	out := append([]string{identity.AssistantDir(n.home)}, dirs...)
	if len(out) > most {
		out = out[:most]
	}
	return out
}

// preflight is the path-access check run in this node's own process: macOS holds the reads to the program that runs
// the node, as it does the claude the node starts.
func (n *Node) preflight(ctx context.Context) DoctorCheck {
	return n.preflightIn(ctx, i18n.Default)
}

func (n *Node) preflightIn(ctx context.Context, lang i18n.Lang) DoctorCheck {
	return pathPreflightIn(ctx, preflightItems(claudeDirOf(n.home), n.workDirs(preflightMaxDirs)), thisPreflightEnv(n.home), lang)
}

// preflightFromNode is `personad node doctor`'s path-access check: asked of the node listening on sock, which reads
// with its own permissions and knows its sessions' working directories. Without one (none running, one in standby,
// an older one) it runs here, on Claude Code's own directory only, and says so: on a Mac the result is then this
// program's (a terminal's, ssh's), which need not be the node's.
func preflightFromNode(sock, home, claudeDir string) func(context.Context, i18n.Lang) DoctorCheck {
	return func(ctx context.Context, lang i18n.Lang) DoctorCheck {
		c, why := askNodePreflightIn(ctx, sock, preflightAnswerWait, lang)
		if why.kind == "" {
			// An old node ignores locale and returns its legacy report. It is already display text; pass it through.
			return c
		}
		c = pathPreflightIn(ctx, preflightItems(claudeDir, []string{identity.AssistantDir(home)}), thisPreflightEnv(home), lang)
		c.Detail = i18n.T(lang, "node-doctor.preflight_fallback_detail", i18n.Args{"detail": c.Detail, "reason": why.text(lang)})
		if runtime.GOOS == "darwin" {
			c.Detail = i18n.T(lang, "node-doctor.preflight_fallback_detail_mac", i18n.Args{"detail": c.Detail})
		}
		return c
	}
}

type preflightFallback struct {
	kind string
	wait time.Duration
	err  error
}

func (f preflightFallback) text(lang i18n.Lang) string {
	switch f.kind {
	case "no_node":
		return i18n.T(lang, "node-doctor.preflight_fallback_no_node")
	case "timeout":
		return i18n.T(lang, "node-doctor.preflight_fallback_timeout", i18n.Args{"wait": f.wait})
	case "error":
		return i18n.T(lang, "node-doctor.preflight_fallback_error", i18n.Args{"error": shorten(f.err.Error(), 60)})
	case "old_node":
		return i18n.T(lang, "node-doctor.preflight_fallback_old_node")
	default:
		return ""
	}
}

// askNodePreflight keeps the legacy no-locale request and zh-Hans reason for older internal callers.
func askNodePreflight(ctx context.Context, sock string, wait time.Duration) (c DoctorCheck, why string) {
	c, reason := askNodePreflightIn(ctx, sock, wait, "")
	return c, reason.text(i18n.Default)
}

// askNodePreflightIn asks the node on sock for its path-access check, waiting at most wait. The returned reason is
// structured so the parent display path chooses its words.
func askNodePreflightIn(ctx context.Context, sock string, wait time.Duration, lang i18n.Lang) (c DoctorCheck, why preflightFallback) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	line, err := nodeclient.DialAt(ctx, sock, sockRequest{Kind: "preflight", Locale: string(lang)})
	switch {
	case errors.Is(err, nodeclient.ErrNoNode): // nothing listens, or a resident node in standby (no hub connected)
		return c, preflightFallback{kind: "no_node"}
	case err != nil && (ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded)):
		return c, preflightFallback{kind: "timeout", wait: wait}
	case err != nil:
		return c, preflightFallback{kind: "error", err: err}
	}
	if json.Unmarshal(line, &c) != nil || c.Name != pathAccessCheck {
		return DoctorCheck{}, preflightFallback{kind: "old_node"} // {"error":"unknown kind"}
	}
	if c.Items == nil {
		c.Items = []string{}
	}
	return c, preflightFallback{}
}
