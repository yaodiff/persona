package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFsWatchStaleRunCtxDoneDoesNotKillNewerRun(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	n.fsw.every, n.fsw.gitEvery = time.Hour, time.Hour

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	n.fsw.start(ctx2, func(fsChanged) {})
	defer n.fsw.close()
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{{Path: dir, Cwd: dir}}})

	n.fsw.mu.Lock()
	stop2, exited2 := n.fsw.stop, n.fsw.exited
	n.fsw.mu.Unlock()
	if stop2 == nil || exited2 == nil {
		t.Fatal("newer run did not start")
	}

	assertRun2Alive := func(label string) {
		t.Helper()
		n.fsw.mu.Lock()
		running, curStop := n.fsw.running, n.fsw.stop
		n.fsw.mu.Unlock()
		if !running || curStop != stop2 {
			t.Fatalf("%s: newer run was clobbered (running=%v, sameStop=%v)", label, running, curStop == stop2)
		}
		select {
		case <-stop2:
			t.Fatalf("%s: newer run's stop channel was closed", label)
		default:
		}
		select {
		case <-exited2:
			t.Fatalf("%s: newer run exited unexpectedly", label)
		default:
		}
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	cancel1()

	// Deterministic: stale run takes case <-ctx.Done() while run 2 is active.
	exited1 := make(chan struct{})
	n.fsw.run(n, ctx1, make(chan struct{}), exited1, time.Hour, time.Hour)
	assertRun2Alive("open stale stop")

	// Also exercise the close()+cancel() case where both <-ctx1.Done() and <-stop1 are ready.
	for range 32 {
		closedStop := make(chan struct{})
		close(closedStop)
		ex := make(chan struct{})
		n.fsw.run(n, ctx1, closedStop, ex, time.Hour, time.Hour)
		assertRun2Alive("closed stale stop")
	}
}

func TestFsWatchStartEndsPreviousRun(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	n.fsw.every, n.fsw.gitEvery = time.Hour, time.Hour

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	n.fsw.start(ctx1, func(fsChanged) {})
	d := fsWatchParams{Dirs: []fsWatchDir{{Path: dir, Cwd: dir}}}
	n.fsw.set(n, d)

	n.fsw.mu.Lock()
	exited1 := n.fsw.exited
	n.fsw.mu.Unlock()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ev2 := newEvents()
	n.fsw.start(ctx2, ev2.emit)
	defer n.fsw.close()

	select {
	case <-exited1:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not end the previous run")
	}

	cancel1()
	n.fsw.set(n, d)

	n.fsw.mu.Lock()
	running, stop2, exited2 := n.fsw.running, n.fsw.stop, n.fsw.exited
	n.fsw.mu.Unlock()
	if !running || exited2 == exited1 {
		t.Fatalf("new connection did not spawn a fresh run (running=%v, sameExited=%v)", running, exited2 == exited1)
	}

	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.fsw.poll(n, ctx2, stop2, time.Hour, time.Hour)
	if c := ev2.next(t); len(c.Dirs) != 1 || c.Dirs[0] != dir {
		t.Fatalf("new run event = %+v", c)
	}
}

func TestFsWatchSetAfterCloseDoesNotResurrectRun(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	n.fsw.every, n.fsw.gitEvery = time.Hour, time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := newEvents()
	n.fsw.start(ctx, ev.emit)
	n.fsw.close()

	// Matches Node.Serve LIFO defers: close() runs before cancel(), while an RPC handler may still call set().
	n.fsw.set(n, fsWatchParams{
		Dirs: []fsWatchDir{{Path: dir, Cwd: dir}},
		Git:  []string{dir},
	})

	n.fsw.mu.Lock()
	running, dirsLen, gitLen := n.fsw.running, len(n.fsw.dirs), len(n.fsw.git)
	n.fsw.mu.Unlock()
	if running || dirsLen != 0 || gitLen != 0 {
		t.Fatalf("set after close resurrected watcher: running=%v dirs=%d git=%d", running, dirsLen, gitLen)
	}
	select {
	case c := <-ev.ch:
		t.Fatalf("unexpected event after close: %+v", c)
	default:
	}
}

func TestFsWatchStoppedPollDoesNotMutateOrEmit(t *testing.T) {
	dir := t.TempDir()
	watched := filepath.Join(dir, "watched")
	if err := os.Mkdir(watched, 0o700); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: dir}
	n.fsw.every, n.fsw.gitEvery = time.Hour, time.Hour

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	n.fsw.start(ctx1, func(fsChanged) {})
	d := fsWatchDir{Path: watched, Cwd: dir}
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{d}})

	n.fsw.mu.Lock()
	stop1 := n.fsw.stop
	entered, release := make(chan struct{}), make(chan struct{})
	n.fsw.beforePollCommit = func() {
		close(entered)
		<-release
	}
	n.fsw.mu.Unlock()

	// Modify watched while run1 is alive, then start run1's poll so it passes the entry guard and scans
	// the changed directory before pausing right before the locked commit check.
	if err := os.WriteFile(filepath.Join(watched, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.fsw.poll(n, ctx1, stop1, time.Hour, time.Hour)
	}()
	<-entered

	// Clear the test gate under mu, then close run1 and start run2 watching the same directory with a fresh baseline.
	n.fsw.mu.Lock()
	n.fsw.beforePollCommit = nil
	n.fsw.mu.Unlock()
	n.fsw.close()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ev2 := newEvents()
	n.fsw.start(ctx2, ev2.emit)
	defer n.fsw.close()
	if err := os.WriteFile(filepath.Join(watched, "b.txt"), []byte("bb"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{d}})

	n.fsw.mu.Lock()
	sig2 := n.fsw.dirs[d].sig
	n.fsw.mu.Unlock()

	// Resume run1's in-flight poll into the post-scan commit guard.
	close(release)
	<-done

	n.fsw.mu.Lock()
	gotSig := n.fsw.dirs[d].sig
	n.fsw.mu.Unlock()
	if gotSig != sig2 {
		t.Fatalf("stopped poll mutated newer run baseline: got %q, want %q", gotSig, sig2)
	}
	select {
	case <-ev2.ch:
		t.Fatal("stopped poll emitted an event on the newer connection")
	default:
	}
}

func TestFsWatchInFlightPollDoesNotClobberReaddedBaseline(t *testing.T) {
	dir := t.TempDir()
	keepDir := filepath.Join(dir, "keep")
	targetDir := filepath.Join(dir, "target")
	for _, p := range []string{keepDir, targetDir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n := &Node{home: dir}
	n.fsw.every, n.fsw.gitEvery = time.Hour, time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := newEvents()
	n.fsw.start(ctx, ev.emit)
	defer n.fsw.close()

	keep := fsWatchDir{Path: keepDir, Cwd: dir}
	target := fsWatchDir{Path: targetDir, Cwd: dir}
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{keep, target}})

	n.fsw.mu.Lock()
	stop := n.fsw.stop
	entered, release := make(chan struct{}), make(chan struct{})
	n.fsw.beforePollCommit = func() {
		close(entered)
		<-release
	}
	n.fsw.mu.Unlock()

	// Run poll on the same active run; it scans target before target is removed and re-added.
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.fsw.poll(n, ctx, stop, time.Hour, time.Hour)
	}()
	<-entered

	n.fsw.mu.Lock()
	n.fsw.beforePollCommit = nil
	n.fsw.mu.Unlock()

	// Keep the same run alive with keepDir while removing targetDir, modifying it, and re-adding it with a fresh baseline.
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{keep}})
	if err := os.WriteFile(filepath.Join(targetDir, "new.txt"), []byte("fresh"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{keep, target}})

	n.fsw.mu.Lock()
	freshSig := n.fsw.dirs[target].sig
	n.fsw.mu.Unlock()

	close(release)
	<-done

	n.fsw.mu.Lock()
	gotSig := n.fsw.dirs[target].sig
	n.fsw.mu.Unlock()
	if gotSig != freshSig {
		t.Fatalf("in-flight poll rolled back re-added baseline: got %q, want %q", gotSig, freshSig)
	}
	select {
	case <-ev.ch:
		t.Fatal("in-flight poll emitted a stale change for a re-added directory")
	default:
	}
}

func TestFsWatchStaleSetAcrossReconnectIgnored(t *testing.T) {
	dir := t.TempDir()
	oldDir, freshDir := filepath.Join(dir, "old"), filepath.Join(dir, "fresh")
	for _, p := range []string{oldDir, freshDir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n := &Node{home: dir}
	n.fsw.every, n.fsw.gitEvery = time.Hour, time.Hour

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	n.fsw.start(ctx1, func(fsChanged) {})

	entered, release := make(chan struct{}), make(chan struct{})
	n.fsw.mu.Lock()
	n.fsw.beforeSet = func() {
		select {
		case <-entered:
		default:
			close(entered)
			<-release
		}
	}
	n.fsw.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{{Path: oldDir, Cwd: dir}}, Generation: 5})
	}()
	<-entered

	n.fsw.close()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	n.fsw.start(ctx2, func(fsChanged) {})
	defer n.fsw.close()
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{{Path: freshDir, Cwd: dir}}, Generation: 1})

	close(release)
	<-done

	n.fsw.mu.Lock()
	defer n.fsw.mu.Unlock()
	if len(n.fsw.dirs) != 1 || n.fsw.generation != 1 {
		t.Fatalf("stale reconnect set corrupted watcher: dirs=%v gen=%d", n.fsw.dirs, n.fsw.generation)
	}
	for d := range n.fsw.dirs {
		if d.Path != freshDir {
			t.Fatalf("watched dir = %q, want %q", d.Path, freshDir)
		}
	}
}
