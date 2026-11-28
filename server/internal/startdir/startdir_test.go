package startdir_test

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/startdir"
)

// folders is a fake file system for check: each path answers at once, fails with EINTR at once ("eintr"), or hangs
// until the test ends ("hang"), as a stalled folder does. No real folder is involved.
type folders struct {
	mu      sync.Mutex
	paths   map[string]string // path -> "ok", "eintr", "hang", or "late" (answers after lateBy)
	lateBy  time.Duration
	release chan struct{}
	moves   []string // chdir and setenv calls, in order
	chdirOK bool
}

func newFolders(t *testing.T, paths map[string]string) *folders {
	f := &folders{paths: paths, release: make(chan struct{}), chdirOK: true}
	t.Cleanup(func() { close(f.release) })
	return f
}

func (f *folders) answers(path string) bool {
	switch f.paths[path] {
	case "eintr":
		return false
	case "hang":
		<-f.release
		return false
	case "late":
		time.Sleep(f.lateBy) // sleep-ok: a folder that answers after a short delay is the case under test
	}
	return true
}

func (f *folders) chdir(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, "chdir "+dir)
	if !f.chdirOK {
		return errors.New("no")
	}
	return nil
}

func (f *folders) setenv(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, "setenv "+key+"="+value)
	return nil
}

func (f *folders) moved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.moves)
}

const work = "/work/project" // the folder $PWD names in these tests

// A folder that answers is left as it is; one that, with the folder $PWD names, does not answer within the wait (it
// hangs, or fails with EINTR as a stalled folder does) is left for "/", and check gives the folder it left.
func TestCheck(t *testing.T) {
	moved := []string{"chdir /", "setenv PWD=/"}
	for _, c := range []struct {
		name      string
		dot, pwd  string // how "." and work answer
		want      string
		wantMoves []string
	}{
		{"both answer", "ok", "ok", "", nil},
		{"the folder answers, $PWD hangs", "ok", "hang", "", nil},
		{"both hang", "hang", "hang", work, moved},
		{"both fail with EINTR", "eintr", "eintr", work, moved},
		{"the folder fails with EINTR, $PWD hangs", "eintr", "hang", work, moved},
		// $PWD names a folder that answers, so not this one: there is no name to come back by, and nothing is moved.
		{"the folder hangs, $PWD answers", "hang", "ok", "", nil},
		{"the folder answers late, within the wait", "late", "hang", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFolders(t, map[string]string{".": c.dot, work: c.pwd})
			f.lateBy = 20 * time.Millisecond
			if got := startdir.Check(f.answers, work, 200*time.Millisecond, f.chdir, f.setenv); got != c.want {
				t.Fatalf("check = %q, want %q", got, c.want)
			}
			if got := f.moved(); !slices.Equal(got, c.wantMoves) {
				t.Fatalf("moves %q, want %q", got, c.wantMoves)
			}
		})
	}
}

// Two calls that fail with EINTR are an answer already: the process moves at once, without waiting out the wait.
func TestCheckDoesNotWaitOutEINTR(t *testing.T) {
	f := newFolders(t, map[string]string{".": "eintr", work: "eintr"})
	start := time.Now()
	if got := startdir.Check(f.answers, work, time.Minute, f.chdir, f.setenv); got != work {
		t.Fatalf("check = %q, want %q", got, work)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("check took %v for two calls that failed at once", took)
	}
}

// Without an absolute $PWD there is no folder to go back to (package settle): nothing is asked or moved, and os's start
// waits for the folder as before. A move that fails leaves everything as it was.
func TestCheckNeedsAWayBack(t *testing.T) {
	for _, pwd := range []string{"", "project", "./project"} {
		f := newFolders(t, map[string]string{".": "hang", pwd: "hang"})
		if got := startdir.Check(f.answers, pwd, 10*time.Millisecond, f.chdir, f.setenv); got != "" || len(f.moved()) != 0 {
			t.Fatalf("PWD %q: check = %q, moves %q", pwd, got, f.moved())
		}
	}
	f := newFolders(t, map[string]string{".": "hang", work: "hang"})
	f.chdirOK = false
	if got := startdir.Check(f.answers, work, 10*time.Millisecond, f.chdir, f.setenv); got != "" || !slices.Equal(f.moved(), []string{"chdir /"}) {
		t.Fatalf("a failed move: check = %q, moves %q", got, f.moved())
	}
}

// This test's own folder answered at start: the process was not moved.
func TestLeftAfterAStartThatAnswered(t *testing.T) {
	if startdir.Left() != "" {
		t.Fatalf("Left() = %q in a process whose folder answered", startdir.Left())
	}
}

const pkgPath = "github.com/nwverse/persona/server/internal/startdir"

// This package starts before package os (see the package doc): this test binary, run again with GODEBUG=inittrace=1,
// lists its packages' initialization in order. If it ever started after os, os's start would already have waited for
// the folder.
func TestStartsBeforeOS(t *testing.T) {
	if os.Getenv("STARTDIR_INITTRACE") == "1" {
		return // the run under inittrace: its packages are initialized by now
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartsBeforeOS$", "-test.count=1")
	cmd.Env = append(os.Environ(), "STARTDIR_INITTRACE=1", "GODEBUG=inittrace=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	at := func(pkg string) int {
		for i, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "init "+pkg+" @") {
				return i
			}
		}
		t.Fatalf("no initialization of %s in:\n%s", pkg, out)
		return -1
	}
	if us, osInit := at(pkgPath), at("os"); us > osInit {
		t.Fatalf("%s starts after os (lines %d and %d of the trace): it must import nothing that imports os", pkgPath, us, osInit)
	}
}

// Nothing this package imports imports os, on any platform Persona's programs are built for: Go would start os first.
func TestImportsNothingThatImportsOS(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	for _, target := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64"} {
		goos, goarch, _ := strings.Cut(target, "/")
		cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps (%s): %v", target, err)
		}
		if deps := strings.Fields(string(out)); slices.Contains(deps, "os") {
			t.Errorf("%s (%s) depends on package os, so it would start after os: %v", pkgPath, target, deps)
		}
	}
}
