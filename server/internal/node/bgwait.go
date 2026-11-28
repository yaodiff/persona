package node

// Background commands that only wait (#6045, docs/protocol/agent-states.md "Unified states"). An agent whose turn
// ended is Working while its background commands run (#2935); many of them only wait for something: a sleep, a loop
// that checks CI every half minute, gh … --watch, tail -f. Those show apart from real work. Claude Code runs each
// background command in a shell of its own under the session's claude, sourcing its shell snapshot
// (shell-snapshots/snapshot-…); the processes under those shells say what the commands do.
//
// One ps of the whole process table, at most every bgWaitEvery and only while an agent with background commands has
// ended its turn; CPU is what the processes used between two of them.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	bgWaitEvery = 20 * time.Second // one ps at most this often
	// bgProbeAge: a process younger than this is a check a waiting loop runs now and then (gh pr checks, curl, ssh
	// test -f …); a build or a download that goes on is seen at the next look.
	bgProbeAge = 10 * time.Second
	// bgBusyCPU: the share of one core the background commands may use between two looks and still be waiting.
	bgBusyCPU = 0.02
	// bgOutputQuiet: a process nothing here knows waits only when no background command wrote output for this long.
	bgOutputQuiet = 2 * time.Minute
	// bgStallAfter: a known worker (a build, tests, a download) that used no CPU at any look for this long, while no
	// background command wrote output, looks stalled (a test hung on a lock, an esbuild service nobody talks to any more,
	// #13744), and Session.BackgroundStalledSince says so; the manager no longer keeps a finished turn Working for
	// stalled background commands alone (#16418). A build waiting on the network or a test on a database for a few
	// minutes is not reported.
	bgStallAfter = 10 * time.Minute
	// bgZombieStallAfter: the same for a process nothing here knows (a cmp reading a stdin nobody writes to), or a
	// command Claude Code's timeout moved to the background with no waiter under it (#16418). Those are no build that
	// waits on the network: no CPU and no output for this long and they are reported stalled, not waiting. A waiter
	// (sleep, watch, tail -f, gh … --watch) the agent started is never stalled.
	bgZombieStallAfter = 3 * time.Minute
	// bgMissingShellAfter: background commands the transcript still has running, while the session's claude runs and no
	// shell of theirs was under it at any look for this long and none wrote output (a shell that ended with no
	// notification, #16418): stalled. Only background commands: a Monitor or a subagent is not judged by its shell.
	bgMissingShellAfter = 2 * time.Minute
)

type bgVerdict int

const (
	bgUnknown bgVerdict = iota // no look to compare with yet, or no background command found: Working, as before
	bgWorking
	bgWaiting
)

// psRow is one process: its parent, the CPU it used so far, how long it has run, its command line.
type psRow struct {
	ppid int
	cpu  time.Duration
	age  time.Duration
	args string
}

// bgWatch keeps the last two looks at the process table and whose claude each session is.
type bgWatch struct {
	mu        sync.Mutex
	at        time.Time
	now, prev map[int]psRow
	window    time.Duration        // between prev and now
	claudes   map[string]int       // session id → its claude's pid
	busyAt    map[string]time.Time // session id → the latest look its background commands used CPU at, or first seen
	missingAt map[string]time.Time // session id → the first of the looks in a row that found none of its shells
	ps        func() map[int]psRow // a variable for tests
}

// setBackgroundWait says whether s, whose turn has ended, only waits on its background commands, and since when its
// background work looks stalled (BackgroundWaiting, BackgroundStalledSince).
func (n *Node) setBackgroundWait(s *proto.Session) {
	s.BackgroundWaiting, s.BackgroundStalledSince = false, 0
	if s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked {
		n.bgWait.shellSeen(s.SessionID) // a missing shell is counted from looks after a turn, not across one
		return
	}
	if !waitableTasks(s.Background) {
		return
	}
	waiting, since := n.bgWait.judge(s.SessionID, s.Background, n.liveClaudes)
	s.BackgroundWaiting = waiting
	s.BackgroundStalledSince = proto.UnixTime32Of(since)
}

// judge looks at a session's background commands (claudes: the live claudes, for a new look): whether they only wait,
// and, when they look stalled, the time of the last sign of life (zero when they do not). Stalled: a known worker that
// used no CPU at any look and wrote no output for bgStallAfter; a process nothing here knows, or a command moved from
// the foreground with no waiter under it, for bgZombieStallAfter; background commands alone whose shells were missing
// from under the live claude for bgMissingShellAfter. A stalled session is never waiting.
func (w *bgWatch) judge(session string, tasks []proto.BackgroundTask, claudes func() []claudeProc) (waiting bool, stalledSince time.Time) {
	now, prev, window, pid := w.look(session, claudes)
	if pid == 0 || prev == nil || len(now) == 0 {
		return false, time.Time{}
	}
	tree := scanBgTree(pid, now, prev, window)
	idle := w.idleFor(session, tree.cpu > 0)
	if !tree.found {
		return false, w.missingSince(session, tasks, window > 0)
	}
	w.shellSeen(session)
	var after time.Duration
	switch {
	case tree.worker:
		after = bgStallAfter
	case tree.unknown, allMovedFromForeground(tasks) && !tree.waiter:
		after = bgZombieStallAfter
	}
	if after > 0 {
		if last := w.quietSince(w.at.Add(-idle), tasks); w.at.Sub(last) >= after {
			return false, last
		}
	}
	return tree.verdict(window, outputQuiet(tasks, w.at)) == bgWaiting, time.Time{}
}

// quietSince is the later of from and the last output of tasks: since when nothing showed a sign of life.
func (w *bgWatch) quietSince(from time.Time, tasks []proto.BackgroundTask) time.Time {
	if out := lastOutput(tasks); out.After(from) {
		return out
	}
	return from
}

// missingSince is for a look that found no shell of the session's background commands under its claude: the time
// since which none was there and none wrote output, once that lasted bgMissingShellAfter, and zero before then, or
// when a Monitor, a subagent or a workflow is among them (only a background command's shell is looked for). counted:
// the look compares with one before it.
func (w *bgWatch) missingSince(session string, tasks []proto.BackgroundTask, counted bool) time.Time {
	if !counted || !shellsOnly(tasks) {
		w.shellSeen(session)
		return time.Time{}
	}
	w.mu.Lock()
	if w.missingAt == nil {
		w.missingAt = map[string]time.Time{}
	}
	first, seen := w.missingAt[session]
	if !seen || first.After(w.at) {
		first = w.at
		w.missingAt[session] = first
	}
	at := w.at
	w.mu.Unlock()
	if at.Sub(first) < bgMissingShellAfter {
		return time.Time{}
	}
	if last := w.quietSince(first, tasks); at.Sub(last) >= bgMissingShellAfter {
		return last
	}
	return time.Time{}
}

// shellSeen ends the session's run of looks that found none of its shells.
func (w *bgWatch) shellSeen(session string) {
	w.mu.Lock()
	delete(w.missingAt, session)
	w.mu.Unlock()
}

// shellsOnly: every background task is a background command (no Monitor, subagent or workflow), and there is one.
func shellsOnly(tasks []proto.BackgroundTask) bool {
	return len(tasks) > 0 && !slices.ContainsFunc(tasks, func(t proto.BackgroundTask) bool { return t.Kind != "shell" })
}

// allMovedFromForeground: the running background commands are all ones Claude Code's timeout moved there (#16418).
func allMovedFromForeground(tasks []proto.BackgroundTask) bool {
	shells := 0
	for _, t := range tasks {
		if t.Kind != "shell" {
			continue
		}
		if !t.MovedFromForeground {
			return false
		}
		shells++
	}
	return shells > 0
}

// idleFor is how long the session's background commands have used no CPU at any look, as of the current look: busy
// says whether they used some since the previous one. The first time a session is asked about counts as busy, so a
// stall is only ever judged on what this node saw.
func (w *bgWatch) idleFor(session string, busy bool) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busyAt == nil {
		w.busyAt = map[string]time.Time{}
	}
	last, seen := w.busyAt[session]
	if busy || !seen || last.After(w.at) {
		w.busyAt[session] = w.at
		return 0
	}
	return w.at.Sub(last)
}

// waitableTasks: background commands or Monitors, the kinds that are processes here (agents and workflows are work).
func waitableTasks(tasks []proto.BackgroundTask) bool {
	return slices.ContainsFunc(tasks, func(t proto.BackgroundTask) bool { return t.Kind == "shell" || t.Kind == "monitor" })
}

// look takes a new look at the process table when the last one is bgWaitEvery old, and gives the last two and the
// pid of the session's claude. A look much older than that has nothing to compare with.
func (w *bgWatch) look(session string, claudes func() []claudeProc) (now, prev map[int]psRow, window time.Duration, pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t := time.Now(); w.now == nil || t.Sub(w.at) >= bgWaitEvery {
		take := w.ps
		if take == nil {
			take = psTable
		}
		w.prev, w.window = w.now, t.Sub(w.at)
		if t.Sub(w.at) > 3*bgWaitEvery {
			w.prev = nil
		}
		w.now, w.at = take(), t
		w.claudes = map[string]int{}
		for _, c := range claudes() {
			if c.SessionID != "" {
				w.claudes[c.SessionID] = c.pid
			}
		}
		for id := range w.busyAt { // a claude that is gone takes its history with it
			if _, ok := w.claudes[id]; !ok {
				delete(w.busyAt, id)
			}
		}
		for id := range w.missingAt {
			if _, ok := w.claudes[id]; !ok {
				delete(w.missingAt, id)
			}
		}
	}
	return w.now, w.prev, w.window, w.claudes[session]
}

// psTable is every process (ps -axo …); nil where there is no ps (Windows) or it fails.
func psTable() map[int]psRow {
	if runtime.GOOS == "windows" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,time=,etime=,args=").Output()
	if err != nil {
		return nil
	}
	return parsePS(out)
}

func parsePS(out []byte) map[int]psRow {
	rows := map[int]psRow{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		rows[pid] = psRow{ppid: ppid, cpu: psDuration(f[2]), age: psDuration(f[3]), args: strings.Join(f[4:], " ")}
	}
	return rows
}

// psDuration reads ps's times: "[[dd-]hh:]mm:ss[.ff]" (CPU time on macOS is "m:ss.ff").
func psDuration(s string) time.Duration {
	var d time.Duration
	if day, rest, ok := strings.Cut(s, "-"); ok {
		n, _ := strconv.Atoi(day)
		d, s = time.Duration(n)*24*time.Hour, rest
	}
	parts := strings.Split(s, ":")
	unit := []time.Duration{time.Second, time.Minute, time.Hour}
	for i := len(parts) - 1; i >= 0; i-- {
		k := len(parts) - 1 - i
		if k >= len(unit) {
			break
		}
		v, _ := strconv.ParseFloat(parts[i], 64)
		d += time.Duration(v * float64(unit[k]))
	}
	return d
}

// judgeBackground says what the background commands under the claude with that pid do: waiting when every process
// there waits (or is a shell or a filter in their pipes) and they used next to no CPU between the two looks; working
// when one is a known worker, or they used CPU, or one nothing here knows runs while their output still grows.
func judgeBackground(claude int, now, prev map[int]psRow, window time.Duration, quiet bool) bgVerdict {
	return scanBgTree(claude, now, prev, window).verdict(window, quiet)
}

// bgTree is what one look found under a claude's background commands.
type bgTree struct {
	found   bool          // at least one background command's shell
	cpu     time.Duration // the CPU they used between the two looks
	worker  bool          // a known worker runs there (a build, tests, a download)
	unknown bool          // a process nothing here knows runs there
	waiter  bool          // a process that only waits runs there (sleep, watch, tail -f, gh … --watch)
}

func scanBgTree(claude int, now, prev map[int]psRow, window time.Duration) bgTree {
	kids := map[int][]int{}
	for pid, r := range now {
		kids[r.ppid] = append(kids[r.ppid], pid)
	}
	var shells []int
	for _, c := range kids[claude] {
		if taskShell(now[c].args) {
			shells = append(shells, c)
		}
	}
	if len(shells) == 0 || window <= 0 {
		return bgTree{}
	}
	tree := bgTree{found: true}
	for queue := shells; len(queue) > 0; {
		pid := queue[0]
		queue = append(queue[1:], kids[pid]...)
		r := now[pid]
		if p, ok := prev[pid]; ok && r.age >= p.age && r.cpu >= p.cpu {
			tree.cpu += r.cpu - p.cpu
		} else if r.age <= window {
			tree.cpu += r.cpu // started since the last look
		}
		if r.age < bgProbeAge || slices.Contains(shells, pid) {
			continue
		}
		switch processKind(r.args) {
		case procWorker:
			tree.worker = true
		case procOther:
			tree.unknown = true
		case procWait:
			tree.waiter = true
		}
	}
	return tree
}

func (t bgTree) verdict(window time.Duration, quiet bool) bgVerdict {
	switch {
	case !t.found:
		return bgUnknown
	case float64(t.cpu) > bgBusyCPU*float64(window), t.worker, t.unknown && !quiet:
		return bgWorking
	}
	return bgWaiting
}

// taskShell: the shell Claude Code runs a command in (it sources its shell snapshot first).
func taskShell(args string) bool {
	return shellNames[procName(args)] && strings.Contains(args, "shell-snapshots/snapshot-")
}

type procKind int

const (
	procOther  procKind = iota // judged by its CPU and the output
	procWait                   // waits: sleep, a watch
	procPart                   // a shell, a wrapper or a filter in a pipe
	procWorker                 // does work: builds, tests, downloads, copies
)

var (
	shellNames = set("sh", "bash", "zsh", "dash", "ksh", "fish")
	waitNames  = set("sleep", "gsleep", "watch", "inotifywait", "fswatch", "wait4path")
	partNames  = set("timeout", "gtimeout", "nohup", "env", "nice", "stdbuf", "caffeinate", "xargs", "tee", "grep",
		"egrep", "fgrep", "rg", "awk", "gawk", "mawk", "sed", "gsed", "cat", "head", "cut", "tr", "wc", "sort", "uniq",
		"jq", "date", "true", "test", "pgrep", "ps")
	workerNames = set("go", "cargo", "rustc", "make", "gmake", "cmake", "ninja", "bazel", "gcc", "g++", "cc", "c++",
		"clang", "clang++", "ld", "swift", "swiftc", "swift-frontend", "swift-build", "xcodebuild", "gradle", "gradlew",
		"mvn", "javac", "kotlinc", "tsc", "esbuild", "webpack", "rollup", "jest", "vitest", "pytest", "npm", "npx",
		"pnpm", "yarn", "bun", "pip", "pip3", "uv", "poetry", "curl", "wget", "aria2c", "rsync", "scp", "sftp", "git",
		"git-remote-https", "docker", "podman", "ffmpeg", "tar", "zip", "unzip", "xz", "gzip", "zstd", "brew", "apt",
		"apt-get", "turbo")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func procName(args string) string {
	first, _, _ := strings.Cut(args, " ")
	return strings.TrimPrefix(filepath.Base(first), "-")
}

func processKind(args string) procKind {
	name := procName(args)
	f := strings.Fields(args)
	switch {
	case waitNames[name]:
		return procWait
	case name == "tail": // follows a file, or a filter in a pipe
		if slices.ContainsFunc(f[1:], func(a string) bool {
			return a == "-f" || a == "-F" || a == "--follow" || strings.HasPrefix(a, "--follow=")
		}) {
			return procWait
		}
		return procPart
	case name == "gh": // gh pr checks --watch, gh run watch
		if slices.Contains(f, "--watch") || len(f) > 2 && f[1] == "run" && f[2] == "watch" {
			return procWait
		}
		return procOther
	case shellNames[name] || partNames[name]:
		return procPart
	case workerNames[name]:
		return procWorker
	}
	return procOther
}

// outputQuiet: no background command wrote output for bgOutputQuiet (the files it writes to; true when none is known).
func outputQuiet(tasks []proto.BackgroundTask, now time.Time) bool {
	return now.Sub(lastOutput(tasks)) >= bgOutputQuiet
}

// lastOutput is when a background command last wrote to its output file (zero when none is known).
func lastOutput(tasks []proto.BackgroundTask) time.Time {
	var last time.Time
	for _, t := range tasks {
		if t.OutputFile == "" {
			continue
		}
		if st, err := os.Stat(t.OutputFile); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
		}
	}
	return last
}
