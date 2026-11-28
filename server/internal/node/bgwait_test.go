package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A synthetic process table: the claude (pid 100), its background commands' shells, what runs under them.
const taskShellArgs = "/bin/zsh -c source /home/me/.claude/shell-snapshots/snapshot-zsh-1-abc.sh 2>/dev/null || true && eval 'x'"

func table(rows ...psRow) map[int]psRow {
	m := map[int]psRow{100: {ppid: 1, age: time.Hour, args: "/usr/local/bin/claude --resume"}}
	for i, r := range rows {
		m[200+i] = r
	}
	return m
}

func row(ppid int, age, cpu time.Duration, args string) psRow {
	return psRow{ppid: ppid, age: age, cpu: cpu, args: args}
}

// What the background commands under a claude do (#6045): only waiting, or working.
func TestJudgeBackground(t *testing.T) {
	const window = 20 * time.Second
	shell := row(100, time.Hour, 0, taskShellArgs) // pid 200
	for _, tc := range []struct {
		name  string
		rows  []psRow
		cpu   map[int]time.Duration // CPU each pid used since the last look
		quiet bool                  // no output for a while
		want  bgVerdict
	}{
		{"a sleep loop", []psRow{shell, row(200, time.Hour, 0, "bash -c until test -f done; do sleep 30; done"), row(201, 12*time.Second, 0, "sleep 30")}, nil, true, bgWaiting},
		{"gh watching checks, printing as it goes", []psRow{shell, row(200, time.Hour, 0, "gh pr checks 12 --watch")}, nil, false, bgWaiting},
		{"gh run watch", []psRow{shell, row(200, time.Hour, 0, "gh run watch 99")}, nil, false, bgWaiting},
		{"tail -f into grep", []psRow{shell, row(200, time.Hour, 0, "tail -F /var/log/app.log"), row(200, time.Hour, 0, "grep --line-buffered ERROR")}, nil, false, bgWaiting},
		{"a loop's probe right now", []psRow{shell, row(200, time.Hour, 0, "bash -c while true; do curl -s example.com; sleep 20; done"), row(201, 3*time.Second, 0, "curl -s example.com")}, nil, true, bgWaiting},
		{"a build", []psRow{shell, row(200, time.Minute, 0, "make -j8"), row(201, 30*time.Second, 0, "cc -c x.c")}, nil, true, bgWorking},
		{"a download", []psRow{shell, row(200, time.Minute, 0, "curl -O https://example.com/big.tar")}, nil, true, bgWorking},
		{"tests running", []psRow{shell, row(200, time.Minute, 0, "go test ./...")}, nil, true, bgWorking},
		{"a script busy on the CPU", []psRow{shell, row(200, time.Hour, 0, "python3 crunch.py")}, map[int]time.Duration{201: 5 * time.Second}, true, bgWorking},
		{"a script asleep, output quiet", []psRow{shell, row(200, time.Hour, 0, "python3 poll.py")}, nil, true, bgWaiting},
		{"a script asleep, output still growing", []psRow{shell, row(200, time.Hour, 0, "python3 poll.py")}, nil, false, bgWorking},
		{"a loop that burns CPU", []psRow{shell, row(200, time.Hour, 0, "bash -c while :; do :; done")}, map[int]time.Duration{201: 15 * time.Second}, true, bgWorking},
		{"no background command: an MCP server alone", []psRow{row(100, time.Hour, 0, "node mcp-server.js")}, nil, true, bgUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := table(tc.rows...)
			prev := map[int]psRow{}
			for pid, r := range now {
				r.age -= window
				r.cpu -= tc.cpu[pid]
				if r.age > 0 {
					prev[pid] = r
				}
			}
			for pid, d := range tc.cpu {
				r := now[pid]
				r.cpu += d
				now[pid] = r
				p := prev[pid]
				p.cpu = 0
				prev[pid] = p
			}
			if got := judgeBackground(100, now, prev, window, tc.quiet); got != tc.want {
				t.Fatalf("%v, want %v", got, tc.want)
			}
		})
	}
}

func TestPSDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:00.05":     50 * time.Millisecond, // macOS CPU time
		"12:34.56":    12*time.Minute + 34560*time.Millisecond,
		"00:01:02":    62 * time.Second, // Linux
		"1-02:03:04":  26*time.Hour + 3*time.Minute + 4*time.Second,
		"08-15:01:03": 8*24*time.Hour + 15*time.Hour + time.Minute + 3*time.Second,
		"00:07":       7 * time.Second,
	} {
		if got := psDuration(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
	rows := parsePS([]byte("  100     1   0:01.50 01:00:00 /usr/local/bin/claude --resume\n  200   100   0:00.00    00:07 sleep 30\nnot a row\n"))
	if len(rows) != 2 || rows[200].ppid != 100 || rows[200].args != "sleep 30" || rows[100].cpu != 1500*time.Millisecond || rows[200].age != 7*time.Second {
		t.Fatalf("rows %+v", rows)
	}
}

// The session list asks only for an agent whose turn ended with background commands, and only from the second look on
// (CPU is what was used between two).
func TestBackgroundWaitsOnTheSession(t *testing.T) {
	out := filepath.Join(t.TempDir(), "task.output")
	if err := os.WriteFile(out, []byte("waiting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(out, old, old)
	looks := 0
	n := &Node{}
	n.bgWait.ps = func() map[int]psRow {
		looks++
		return table(row(100, time.Hour, 0, taskShellArgs), row(200, time.Hour, 0, "bash -c sleep 600"), row(201, time.Hour, 0, "sleep 600"))
	}
	claudes := func() []claudeProc { return []claudeProc{{pid: 100, SessionID: "s1"}} }
	s := proto.Session{SessionID: "s1", Status: proto.StatusIdle, Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell", OutputFile: out}}}
	wait := func() bool {
		now, prev, window, pid := n.bgWait.look(s.SessionID, claudes)
		return pid != 0 && prev != nil && judgeBackground(pid, now, prev, window, outputQuiet(s.Background, time.Now())) == bgWaiting
	}
	if wait() {
		t.Fatal("first look: nothing to compare with yet")
	}
	n.bgWait.at = n.bgWait.at.Add(-bgWaitEvery) // the next look is due
	if !wait() || looks != 2 {
		t.Fatalf("second look: waiting %v after %d looks", wait(), looks)
	}
	if wait(); looks != 2 {
		t.Fatalf("looked %d times within %v", looks, bgWaitEvery)
	}
	for _, other := range []proto.Session{
		{SessionID: "s1", Status: proto.StatusWorking, Background: s.Background},                         // its turn goes on
		{SessionID: "s1", Status: proto.StatusIdle, Background: []proto.BackgroundTask{{Kind: "agent"}}}, // a subagent is work
		{SessionID: "s1", Status: proto.StatusIdle},
	} {
		if n.setBackgroundWait(&other); other.BackgroundWaiting {
			t.Errorf("%+v waits", other)
		}
	}
	if !strings.Contains(taskShellArgs, "shell-snapshots/snapshot-") || !taskShell(taskShellArgs) || taskShell("zsh -c make") {
		t.Fatal("task shells are the ones that source Claude Code's snapshot")
	}
}

// A worker that hung (#13744): tests whose esbuild service nobody talks to any more, no CPU and no output for hours.
// The Agent stays Working, but once the worker used no CPU and wrote no output for bgStallAfter the session reports
// since when; CPU or output clears it at once.
func TestBackgroundStalledWorkerIsReported(t *testing.T) {
	out := filepath.Join(t.TempDir(), "task.output")
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(out, old, old)
	cpu := time.Duration(0)
	n := &Node{}
	n.bgWait.ps = func() map[int]psRow {
		return table(row(100, time.Hour, 0, taskShellArgs), row(200, time.Hour, 2*time.Second, "node --test tests/x.test.mjs"),
			row(201, time.Hour, cpu, "/w/node_modules/@esbuild/linux-x64/bin/esbuild --service=0.28.2 --ping"))
	}
	live := []claudeProc{{pid: 100, SessionID: "s1"}}
	tasks := []proto.BackgroundTask{{ID: "b1", Kind: "shell", OutputFile: out}}
	look := func() (bool, time.Time) {
		n.bgWait.at = n.bgWait.at.Add(-bgWaitEvery) // the next look is due
		return n.bgWait.judge("s1", tasks, func() []claudeProc { return live })
	}
	look() // the first look has nothing to compare with
	if waits, since := look(); waits || !since.IsZero() {
		t.Fatalf("a worker just seen idle is plain Working: %v %v", waits, since)
	}
	n.bgWait.busyAt["s1"] = n.bgWait.at.Add(-bgStallAfter - time.Minute) // no CPU at any look since then
	waits, since := look()
	if waits {
		t.Fatal("a stalled worker is still Working, not waiting")
	}
	if since.IsZero() || n.bgWait.at.Sub(since) < bgStallAfter {
		t.Fatalf("stalled since %v", since)
	}
	cpu = 3 * time.Second // it moves again
	if _, since := look(); !since.IsZero() {
		t.Fatal("a worker that uses CPU again is not stalled")
	}
	cpu = 3 * time.Second
	n.bgWait.busyAt["s1"] = n.bgWait.at.Add(-bgStallAfter - time.Minute)
	_ = os.Chtimes(out, time.Now(), time.Now()) // output again
	if _, since := look(); !since.IsZero() {
		t.Fatal("a worker writing output is not stalled")
	}
	live = nil
	look()
	if _, ok := n.bgWait.busyAt["s1"]; ok {
		t.Fatal("a claude that is gone keeps no history")
	}
}

// setBackgroundWait puts the stall on the session and takes it off again when the turn is working.
func TestSetBackgroundWaitStalledField(t *testing.T) {
	n := &Node{}
	s := proto.Session{SessionID: "s1", Status: proto.StatusWorking, BackgroundStalledSince: 5, BackgroundWaiting: true}
	n.setBackgroundWait(&s)
	if s.BackgroundStalledSince != 0 || s.BackgroundWaiting {
		t.Fatal("a turn that goes on has no background verdict")
	}
}

// quietOutput is an output file last written an hour ago.
func quietOutput(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "task.output")
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(out, old, old)
	return out
}

// stallWatch is a node whose process table is rows under the claude of session s1, and a look at it that is due.
func stallWatch(rows func() []psRow) (*Node, func(tasks []proto.BackgroundTask) (bool, time.Time)) {
	n := &Node{}
	n.bgWait.ps = func() map[int]psRow { return table(rows()...) }
	live := func() []claudeProc { return []claudeProc{{pid: 100, SessionID: "s1"}} }
	return n, func(tasks []proto.BackgroundTask) (bool, time.Time) {
		n.bgWait.at = n.bgWait.at.Add(-bgWaitEvery) // the next look is due
		return n.bgWait.judge("s1", tasks, live)
	}
}

// A command that hangs on its stdin (#16418): a foreground cmp | head that hit the timeout and was moved to the
// background, or any process nothing here knows. No CPU and no output: waiting at first, as before, and stalled once
// that lasted bgZombieStallAfter, so its finished Agent no longer stays Working. A waiter the agent started (sleep,
// watch, tail -f, gh … --watch) is never stalled, however long it waits; a known worker keeps the longer bgStallAfter.
func TestBackgroundZombieShellIsStalled(t *testing.T) {
	out := quietOutput(t)
	shell := row(100, time.Hour, 0, taskShellArgs) // pid 200
	for _, tc := range []struct {
		name    string
		rows    []psRow
		moved   bool
		idle    time.Duration // no CPU at any look for this long
		waits   bool
		stalled bool
	}{
		{"cmp on stdin, just quiet", []psRow{shell, row(200, time.Hour, 0, "cmp only.json"), row(200, time.Hour, 0, "head -2")}, true, 2 * time.Minute, true, false},
		{"cmp on stdin, quiet past the grace", []psRow{shell, row(200, time.Hour, 0, "cmp only.json"), row(200, time.Hour, 0, "head -2")}, true, bgZombieStallAfter + time.Minute, false, true},
		{"an unknown script started in the background", []psRow{shell, row(200, time.Hour, 0, "python3 poll.py")}, false, bgZombieStallAfter + time.Minute, false, true},
		{"a moved cat on stdin: filters only", []psRow{shell, row(200, time.Hour, 0, "cat")}, true, bgZombieStallAfter + time.Minute, false, true},
		{"a moved sleep still waits", []psRow{shell, row(200, time.Hour, 0, "sleep 900")}, true, time.Hour, true, false},
		{"a sleep loop started in the background", []psRow{shell, row(200, time.Hour, 0, "bash -c until test -f done; do sleep 30; done"), row(201, time.Hour, 0, "sleep 30")}, false, time.Hour, true, false},
		{"tail -f", []psRow{shell, row(200, time.Hour, 0, "tail -F /var/log/app.log")}, false, time.Hour, true, false},
		{"gh watching checks", []psRow{shell, row(200, time.Hour, 0, "gh pr checks 12 --watch")}, false, time.Hour, true, false},
		{"a quiet build before bgStallAfter", []psRow{shell, row(200, time.Hour, 0, "go test ./...")}, true, bgZombieStallAfter + time.Minute, false, false},
		{"a quiet build past bgStallAfter", []psRow{shell, row(200, time.Hour, 0, "go test ./...")}, false, bgStallAfter + time.Minute, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, look := stallWatch(func() []psRow { return tc.rows })
			tasks := []proto.BackgroundTask{{ID: "b1", Kind: "shell", OutputFile: out, MovedFromForeground: tc.moved}}
			look(tasks) // the first look has nothing to compare with
			look(tasks)
			n.bgWait.busyAt["s1"] = n.bgWait.at.Add(-tc.idle)
			waits, since := look(tasks)
			if waits != tc.waits || since.IsZero() == tc.stalled {
				t.Fatalf("waiting %v stalled since %v, want waiting %v stalled %v", waits, since, tc.waits, tc.stalled)
			}
		})
	}
}

// Background commands the transcript still has running, though no shell of theirs is under the live claude any more
// (it ended with no notification, #16418): stalled after bgMissingShellAfter of looks in a row that found none, and only
// for background commands alone; a shell that shows up again ends it, and a Monitor or a subagent is not judged so.
func TestBackgroundMissingShellIsStalled(t *testing.T) {
	out := quietOutput(t)
	shells := false
	n, look := stallWatch(func() []psRow {
		if shells {
			return []psRow{row(100, time.Hour, 0, taskShellArgs), row(200, time.Hour, 0, "sleep 900")}
		}
		return []psRow{row(100, time.Hour, 0, "node mcp-server.js")} // the claude's other children
	})
	tasks := []proto.BackgroundTask{{ID: "b1", Kind: "shell", OutputFile: out}}
	look(tasks)
	if waits, since := look(tasks); waits || !since.IsZero() {
		t.Fatalf("a shell missing at one look is no stall yet: %v %v", waits, since)
	}
	n.bgWait.missingAt["s1"] = n.bgWait.at.Add(-bgMissingShellAfter - time.Minute)
	waits, since := look(tasks)
	if waits || since.IsZero() || n.bgWait.at.Sub(since) < bgMissingShellAfter {
		t.Fatalf("missing for longer than the grace: waiting %v stalled since %v", waits, since)
	}
	withMonitor := append([]proto.BackgroundTask{{ID: "m1", Kind: "monitor", OutputFile: out}}, tasks...)
	if _, since := look(withMonitor); !since.IsZero() {
		t.Fatal("a Monitor's work is not judged by a missing shell")
	}
	n.bgWait.missingAt["s1"] = n.bgWait.at.Add(-bgMissingShellAfter - time.Minute)
	_ = os.Chtimes(out, time.Now(), time.Now())
	if _, since := look(tasks); !since.IsZero() {
		t.Fatal("output written meanwhile is a sign of life")
	}
	shells = true
	if waits, since := look(tasks); !waits || !since.IsZero() {
		t.Fatalf("its shell is back, sleeping: waiting %v stalled since %v", waits, since)
	}
	if _, ok := n.bgWait.missingAt["s1"]; ok {
		t.Fatal("a shell seen again ends the run of missing looks")
	}
	n.bgWait.missingAt["s1"] = n.bgWait.at.Add(-time.Hour)
	working := proto.Session{SessionID: "s1", Status: proto.StatusWorking, Background: tasks}
	if n.setBackgroundWait(&working); n.bgWait.missingAt["s1"] != (time.Time{}) {
		t.Fatal("a turn that goes on ends the run of missing looks")
	}
}
