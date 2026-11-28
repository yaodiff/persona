package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/update"
)

func TestCoLocatedManagerFollowsOnlyConfirmedHubExecutable(t *testing.T) {
	home, err := os.MkdirTemp("", "pmgr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	control := filepath.Join(home, "run", "hub-edge.sock")
	for _, dir := range []string{"run", "bin", "update"} {
		if err := os.Mkdir(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("unix", control)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var active atomic.Int64
	active.Store(1)
	path := filepath.Join(home, "bin", "persona-manager")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'v1\\nchannel release, build 1\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(t.TempDir(), "manager")
	if err := os.WriteFile(temp, []byte("#!/bin/sh\nprintf 'v2\\nchannel release, build 2\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := update.InstallPrepared(path, temp); err != nil {
		t.Fatal(err)
	}
	watch := &binaryWatch{hostDataDir: home, path: path, seen: before, out: &syncBuffer{}}
	watch.setHubVersion(func() string { return fmt.Sprint("v", active.Load()) })
	if watch.prepareExitForNew(t.Context()) {
		t.Fatal("manager followed a replacement while the old Hub still serves")
	}
	active.Store(2)
	pending := update.PendingPath(filepath.Join(home, "update"))
	if err := update.Save(pending, update.Pending{ToBuild: 2}); err != nil {
		t.Fatal(err)
	}
	if watch.prepareExitForNew(t.Context()) {
		t.Fatal("manager followed the Hub before self-update confirmation")
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if !watch.prepareExitForNew(t.Context()) {
		t.Fatal("manager refused to follow the confirmed replacement")
	}
}

func TestShouldWatchBinary(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"service mode", map[string]string{"PERSONA_ASSISTANT_DEPLOY": assistant.DeployService}, true},
		{"unset (manual/test)", map[string]string{}, false},
		{"external", map[string]string{"PERSONA_ASSISTANT_DEPLOY": "external"}, false},
		{"docker", map[string]string{"PERSONA_ASSISTANT_DEPLOY": "docker"}, false},
		{"child", map[string]string{"PERSONA_ASSISTANT_DEPLOY": "child"}, false},
		{"development worker child", map[string]string{
			"PERSONA_ASSISTANT_DEPLOY":           assistant.DeployService,
			"PERSONA_DEVELOPMENT_MANAGER_WORKER": "/tmp/worker-1",
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldWatchBinary(func(k string) string { return c.env[k] })
			if got != c.want {
				t.Fatalf("shouldWatchBinary(%v) = %v, want %v", c.env, got, c.want)
			}
		})
	}
}

// boundBinaryEnv sets up an isolated independent manager directory and fake running executable for testing
// runBoundManager's co-located binary replacement detection without real Postgres or timers.
func boundBinaryEnv(t *testing.T) (dir, bin string, write func(script string, mode os.FileMode)) {
	t.Helper()
	home := isolate(t)
	dir = filepath.Join(home, ".persona", "managers", "m-0a1b2c")
	if err := assistant.WriteManagerConfig(dir, assistant.ManagerConfig{
		ID: "m-0a1b2c", Name: "work", Hub: "http://127.0.0.1:1", Token: "jb_tok",
		DatabaseURL: "postgres://persona:pw@127.0.0.1:25432/persona?sslmode=disable",
	}); err != nil {
		t.Fatal(err)
	}
	type saved struct {
		check, idle, most, loopWin time.Duration
		now                        func() time.Time
		running                    func() (string, os.FileInfo, bool)
		runMgr                     func(context.Context, update.Build, string, assistant.MainHooks) error
	}
	old := saved{binaryCheck, idleCheck, idleMost, restartLoopWindow, binaryWatchNow, runningBinary, runManagerWith}
	t.Cleanup(func() {
		binaryCheck, idleCheck, idleMost, restartLoopWindow = old.check, old.idle, old.most, old.loopWin
		binaryWatchNow, runningBinary, runManagerWith = old.now, old.running, old.runMgr
	})
	binaryCheck, idleCheck = 5*time.Millisecond, 5*time.Millisecond
	bin = filepath.Join(home, "personad")
	var seq atomic.Int64
	write = func(script string, mode os.FileMode) {
		tmp := fmt.Sprintf("%s.new%d", bin, seq.Add(1))
		if err := os.WriteFile(tmp, []byte(script), mode); err != nil {
			t.Error(err)
		}
		if err := os.Rename(tmp, bin); err != nil {
			t.Error(err)
		}
	}
	write(goodBinary, 0o755)
	runningBinary = func() (string, os.FileInfo, bool) {
		fi, err := os.Stat(bin)
		return bin, fi, err == nil
	}
	return dir, bin, write
}

func runBoundInTest(t *testing.T, dir string, envMap map[string]string, idle assistant.Idle, onPending func(bool), during func(run int), done <-chan struct{}) (string, int, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	guard := time.AfterFunc(15*time.Second, func() {
		t.Errorf("runBoundManager did not end")
		cancel()
	})
	defer guard.Stop()

	var runs atomic.Int64
	runManagerWith = func(run context.Context, _ update.Build, _ string, h assistant.MainHooks) error {
		n := int(runs.Add(1))
		if h.PendingRestart != nil && onPending != nil {
			h.PendingRestart(onPending)
		}
		if h.Ready != nil {
			h.Ready(idle)
		}
		if during != nil {
			go during(n)
		}
		<-run.Done()
		return nil
	}
	var out syncBuffer
	e := &env{
		stdout: &out,
		getenv: func(k string) string { return envMap[k] },
	}
	err := e.runBoundManager(ctx, update.Build{}, dir, filepath.Dir(filepath.Dir(dir)), nil, &out)
	return out.String(), int(runs.Load()), err
}

// Idle supervised manager detects atomic binary replacement, verifies it, writes the loop guard, and exits cleanly
// without reporting pending_restart. Starting a fresh watch on the new binary clears the matching guard file.
func TestManagerRunFollowsRedeployWhenIdle(t *testing.T) {
	dir, bin, write := boundBinaryEnv(t)
	var pendingCalls atomic.Int64
	out, runs, err := runBoundInTest(
		t, dir,
		map[string]string{"PERSONA_ASSISTANT_DEPLOY": assistant.DeployService},
		func(context.Context) (bool, string) { return true, "" },
		func(bool) { pendingCalls.Add(1) },
		func(int) { write(goodBinary+"# v2\n", 0o755) },
		nil,
	)
	if err != nil || runs != 1 || pendingCalls.Load() != 0 || !strings.Contains(out, "exiting gracefully to restart on the new") {
		t.Fatalf("err=%v runs=%d pending=%d\n%s", err, runs, pendingCalls.Load(), out)
	}
	guardPath := filepath.Join(dir, binaryRestartGuardFile)
	b, err := os.ReadFile(guardPath)
	if err != nil {
		t.Fatalf("missing restart guard %s: %v", guardPath, err)
	}
	var rec binaryRestartRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("decode guard: %v", err)
	}
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Target != fileIdentityOf(fi) {
		t.Fatalf("guard target = %+v, want %+v", rec.Target, fileIdentityOf(fi))
	}
	// Starting the new binary cleans up its matching guard record.
	if w := newBinaryWatchWithGuard(&syncBuffer{}, guardPath); w == nil {
		t.Fatal("newBinaryWatchWithGuard returned nil")
	}
	if _, err := os.Stat(guardPath); !os.IsNotExist(err) {
		t.Fatalf("matching guard file was not removed on new binary start: %v", err)
	}
}

// Busy supervised manager immediately reports pending_restart: true, never interrupts the active turn even past
// idleMost, and exits cleanly as soon as it becomes idle.
func TestManagerRunBusyReportsPendingRestartThenExitsOnIdle(t *testing.T) {
	dir, _, write := boundBinaryEnv(t)
	idleMost = 20 * time.Millisecond // prove runBoundManager does NOT force-restart past idleMost
	var busy atomic.Bool
	busy.Store(true)
	var busyChecks atomic.Int64
	reachedFive := make(chan struct{})
	var fiveOnce sync.Once
	pendingTrue := make(chan struct{}, 1)
	var pendingMu sync.Mutex
	var pendingHistory []bool
	onPending := func(v bool) {
		pendingMu.Lock()
		pendingHistory = append(pendingHistory, v)
		pendingMu.Unlock()
		if v {
			select {
			case pendingTrue <- struct{}{}:
			default:
			}
		}
	}
	idle := func(context.Context) (bool, string) {
		if busy.Load() {
			if busyChecks.Add(1) >= 6 {
				fiveOnce.Do(func() { close(reachedFive) })
			}
			return false, "its conversation c1 is on a turn"
		}
		return true, ""
	}
	out, runs, err := runBoundInTest(
		t, dir,
		map[string]string{"PERSONA_ASSISTANT_DEPLOY": assistant.DeployService},
		idle,
		onPending,
		func(int) {
			write(goodBinary+"# v2\n", 0o755)
			select {
			case <-pendingTrue:
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for pending_restart=true")
				return
			}
			select {
			case <-reachedFive:
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for multiple busy checks")
				return
			}
			busy.Store(false)
		},
		nil,
	)
	if err != nil || runs != 1 || !strings.Contains(out, "exiting gracefully to restart on the new") || strings.Contains(out, "anyway") {
		t.Fatalf("err=%v runs=%d checks=%d\n%s", err, runs, busyChecks.Load(), out)
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if len(pendingHistory) != 1 || !pendingHistory[0] {
		t.Fatalf("pendingHistory = %v, want [true]", pendingHistory)
	}
}

// If the replaced file is reverted back to the startup binary while waiting on busy work, pending_restart clears to
// false and the manager keeps running without exiting.
func TestManagerRunBusyRevertedClearsPendingRestart(t *testing.T) {
	dir, bin, write := boundBinaryEnv(t)
	if err := os.Link(bin, bin+".start"); err != nil {
		t.Fatal(err)
	}
	pendingCh := make(chan bool, 4)
	release := make(chan struct{})
	out, runs, err := runBoundInTest(
		t, dir,
		map[string]string{"PERSONA_ASSISTANT_DEPLOY": assistant.DeployService},
		func(context.Context) (bool, string) { return false, "busy" },
		func(v bool) { pendingCh <- v },
		func(int) {
			write(goodBinary+"# v2\n", 0o755)
			select {
			case v := <-pendingCh:
				if !v {
					t.Error("expected pending_restart=true first")
				}
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for pending_restart=true")
				close(release)
				return
			}
			if err := os.Rename(bin+".start", bin); err != nil {
				t.Error(err)
			}
			select {
			case v := <-pendingCh:
				if v {
					t.Error("expected pending_restart=false after revert")
				}
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for pending_restart=false after revert")
			}
			close(release)
		},
		release,
	)
	if err != nil || runs != 1 || !strings.Contains(out, "cancelled (file reverted or gone)") || strings.Contains(out, "exiting gracefully") {
		t.Fatalf("err=%v runs=%d\n%s", err, runs, out)
	}
}

// Invalid replacement binaries (non-executable or failing version -v) are rejected once without setting pending_restart
// or exiting; a subsequent valid binary triggers graceful exit.
func TestManagerRunRejectsInvalidReplacementBinary(t *testing.T) {
	dir, bin, write := boundBinaryEnv(t)
	checkOrig := checkBinary
	t.Cleanup(func() { checkBinary = checkOrig })
	var badChecks atomic.Int64
	badChecked := make(chan struct{}, 2)
	checkBinary = func(ctx context.Context, path string) error {
		err := checkOrig(ctx, path)
		if err != nil {
			badChecks.Add(1)
			select {
			case badChecked <- struct{}{}:
			default:
			}
		}
		return err
	}
	var pendingCalls atomic.Int64
	out, runs, err := runBoundInTest(
		t, dir,
		map[string]string{"PERSONA_ASSISTANT_DEPLOY": assistant.DeployService},
		func(context.Context) (bool, string) { return true, "" },
		func(bool) { pendingCalls.Add(1) },
		func(int) {
			write(goodBinary+"# non-exec\n", 0o644)
			select {
			case <-badChecked:
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for non-exec rejection")
				return
			}
			write("#!/bin/sh\nexit 1\n", 0o755)
			select {
			case <-badChecked:
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for exit-1 rejection")
				return
			}
			write(goodBinary+"# valid\n", 0o755)
		},
		nil,
	)
	if err != nil || runs != 1 || pendingCalls.Load() != 0 || badChecks.Load() != 2 ||
		!strings.Contains(out, "is not an executable file") || !strings.Contains(out, "exiting gracefully") {
		t.Fatalf("err=%v runs=%d pending=%d badChecks=%d bin=%s\n%s", err, runs, pendingCalls.Load(), badChecks.Load(), bin, out)
	}
}

// Restart-loop guard: if a previous exit already attempted restarting onto the exact same target binary within
// restartLoopWindow (e.g. supervisor relaunched the old binary), the manager refuses to exit again until a newer binary
// replaces it.
func TestManagerRunRestartLoopGuard(t *testing.T) {
	dir, bin, write := boundBinaryEnv(t)
	// Keep the running binary as v1 (already written by boundBinaryEnv), capture its FileInfo, then pre-stage v2.
	v1Path := bin + ".v1"
	if err := os.Link(bin, v1Path); err != nil {
		t.Fatal(err)
	}
	runningBinary = func() (string, os.FileInfo, bool) {
		fi, err := os.Stat(v1Path)
		return bin, fi, err == nil
	}
	// Replace bin on disk with v2 and pre-write the restart-loop guard recording v2 as already attempted.
	write(goodBinary+"# v2\n", 0o755)
	v2Info, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(dir, binaryRestartGuardFile)
	recBytes, _ := json.Marshal(binaryRestartRecord{Target: fileIdentityOf(v2Info), At: time.Now().UTC()})
	if err := os.WriteFile(guardPath, recBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	guardedCh := make(chan struct{}, 1)
	var out syncBuffer
	origRun := runManagerWith
	t.Cleanup(func() { runManagerWith = origRun })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guardTimer := time.AfterFunc(15*time.Second, func() {
		t.Errorf("TestManagerRunRestartLoopGuard did not end")
		cancel()
	})
	defer guardTimer.Stop()

	var pendingCalls atomic.Int64
	runManagerWith = func(run context.Context, _ update.Build, _ string, h assistant.MainHooks) error {
		if h.PendingRestart != nil {
			h.PendingRestart(func(bool) { pendingCalls.Add(1) })
		}
		if h.Ready != nil {
			h.Ready(func(context.Context) (bool, string) { return true, "" })
		}
		go func() {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-run.Done():
					return
				case <-ticker.C:
					if strings.Contains(out.String(), "restart-loop guard") {
						select {
						case guardedCh <- struct{}{}:
						default:
						}
						return
					}
				}
			}
		}()
		go func() {
			select {
			case <-guardedCh:
				// v2 was blocked by loop guard; replacing with v3 (new inode) must trigger graceful exit.
				write(goodBinary+"# v3\n", 0o755)
			case <-run.Done():
			}
		}()
		<-run.Done()
		return nil
	}
	e := &env{
		stdout: &out,
		getenv: func(k string) string {
			if k == "PERSONA_ASSISTANT_DEPLOY" {
				return assistant.DeployService
			}
			return ""
		},
	}
	if err := e.runBoundManager(ctx, update.Build{}, dir, filepath.Dir(filepath.Dir(dir)), nil, &out); err != nil {
		t.Fatalf("runBoundManager: %v\n%s", err, out.String())
	}
	if pendingCalls.Load() != 0 || !strings.Contains(out.String(), "restart-loop guard") || !strings.Contains(out.String(), "exiting gracefully") {
		t.Fatalf("pending=%d\n%s", pendingCalls.Load(), out.String())
	}
}

// A standalone manager follows its replaced launcher in place after draining.
func TestManagerRunUnsupervisedReexecsWhenIdle(t *testing.T) {
	dir, _, write := boundBinaryEnv(t)
	release := make(chan struct{})
	old := restart
	t.Cleanup(func() { restart = old })
	var reexecs atomic.Int64
	restart = func([]string) error {
		reexecs.Add(1)
		close(release)
		return nil
	}
	out, runs, err := runBoundInTest(
		t, dir,
		map[string]string{}, // PERSONA_ASSISTANT_DEPLOY unset
		func(context.Context) (bool, string) { return true, "" },
		nil,
		func(run int) {
			if run == 1 {
				write(goodBinary+"# v2\n", 0o755)
			}
		},
		release,
	)
	if err != nil || reexecs.Load() != 1 || !strings.Contains(out, "replaced") {
		t.Fatalf("err=%v runs=%d\n%s", err, runs, out)
	}
}

// If the replaced binary becomes invalid between becoming idle and stopping, runBoundManager resumes running on the
// current binary instead of exiting and does not retry that broken file.
func TestManagerRunReverifyFailureAfterStopResumes(t *testing.T) {
	dir, _, write := boundBinaryEnv(t)
	var swapped atomic.Bool
	release := make(chan struct{})
	out, runs, err := runBoundInTest(
		t, dir,
		map[string]string{"PERSONA_ASSISTANT_DEPLOY": assistant.DeployService},
		func(context.Context) (bool, string) {
			if swapped.CompareAndSwap(false, true) {
				// Corrupt the binary right before signaling idle, so post-stop re-verification fails.
				write("#!/bin/sh\nexit 3\n", 0o755)
			}
			return true, ""
		},
		nil,
		func(run int) {
			switch run {
			case 1:
				write(goodBinary+"# v2\n", 0o755)
			case 2:
				time.AfterFunc(40*time.Millisecond, func() { close(release) })
			}
		},
		release,
	)
	if err != nil || runs != 2 || !strings.Contains(out, "does not run any more") || strings.Contains(out, "exiting gracefully") {
		t.Fatalf("err=%v runs=%d\n%s", err, runs, out)
	}
}
