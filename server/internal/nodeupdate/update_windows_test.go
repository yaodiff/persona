package nodeupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

// On Windows a node takes updates only when persona-link's supervisor says it restarts it (node --attach --supervised);
// without that it tells the user to install Persona Link again, not about launchd or systemd.
func TestWindowsNodeTakesUpdatesOnlyWhenSupervised(t *testing.T) {
	declared := New("old", func() {}, Options{Supervised: true})
	t.Cleanup(declared.Close)
	if ok, reason := declared.Capability(); !ok {
		t.Fatalf("a node persona-link's supervisor restarts cannot take updates: %s", reason)
	}
	undeclared := New("old", func() {}, Options{})
	t.Cleanup(undeclared.Close)
	if ok, reason := undeclared.Capability(); ok || reason != windowsUnsupervised {
		t.Fatalf("got %v %q, want the Windows reason", ok, reason)
	}
	// The hub shows the node's reason in the app's language by matching this original text.
	if got := i18n.T(i18n.ZhHans, "devices-deploy.node_windows_unsupervised"); got != windowsUnsupervised {
		t.Fatalf("hub catalog has %q for the node's %q", got, windowsUnsupervised)
	}
}

// Windows will not replace the file of a running program, but lets it be renamed: replaceFile moves it aside, and the
// retired file goes once nothing runs from it.
func TestReplaceFileMovesARunningProgramAside(t *testing.T) {
	old, next := fixtures(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "node.exe")
	if err := copyFile(old, exe); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "fixture-hold")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil { // "ready": it runs from exe now
		t.Fatal(err)
	}
	stage := filepath.Join(dir, ".node-update-stage-1.exe")
	if err := copyFile(next, stage); err != nil {
		t.Fatal(err)
	}
	if err := renameStill(stage, exe, hash(t, old)); err != nil {
		t.Fatalf("the running program's file was not replaced: %v", err)
	}
	if hash(t, exe) != hash(t, next) {
		t.Fatal("the new file is not in place")
	}
	retired, _ := filepath.Glob(filepath.Join(dir, retiredPrefix+"*"))
	if len(retired) != 1 || hash(t, retired[0]) != hash(t, old) {
		t.Fatalf("the running program's file was not moved aside: %v", retired)
	}
	removeRetired(dir)
	if _, err := os.Stat(retired[0]); err != nil {
		t.Fatal("removed the file of a program still running from it")
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		removeRetired(dir)
		_, err := os.Stat(retired[0])
		return os.IsNotExist(err)
	})
}

// discard moves aside a backup the watchdog still runs from, so a confirmed update does not leave it behind for good.
func TestDiscardRetiresAFileInUse(t *testing.T) {
	old, _ := fixtures(t)
	dir := t.TempDir()
	backup := filepath.Join(dir, "node.exe.node-update-backup-1.exe")
	if err := copyFile(old, backup); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(backup, "fixture-hold")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	discard(backup)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("the backup is still under its name")
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	waitFor(t, 5*time.Second, func() bool {
		removeRetired(dir)
		left, _ := filepath.Glob(filepath.Join(dir, "*"))
		return len(left) == 0
	})
}

// superviseWindowsFixture runs the fixture as persona-link's supervisor runs the node: `node --attach --supervised`,
// started again at once when it exits with nodeproto.RestartExitCode, shortly after any other exit.
func superviseWindowsFixture(t *testing.T, exe string, env []string) *fixtureSupervisor {
	t.Helper()
	s := &fixtureSupervisor{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				return
			}
			cmd := exec.Command(exe, "node", "--attach", "--supervised")
			cmd.Env = env
			if err := cmd.Start(); err != nil {
				s.mu.Unlock()
				t.Error(err)
				return
			}
			s.cmd, s.starts = cmd, s.starts+1
			s.mu.Unlock()
			_ = cmd.Wait()
			if cmd.ProcessState.ExitCode() != nodeproto.RestartExitCode {
				time.Sleep(50 * time.Millisecond) // sleep-ok: the supervisor's own restart delay, not a wait for state
			}
		}
	}()
	t.Cleanup(func() {
		s.mu.Lock()
		s.stopped = true
		if s.cmd != nil {
			_ = s.cmd.Process.Kill()
		}
		s.mu.Unlock()
		<-s.done
	})
	return s
}

// A node persona-link's supervisor runs takes an update the way it does under a container's entry script, except
// that it cannot exec: it replaces its own running file (moved aside), exits asking to be started again, and the new
// program confirms; when it never confirms, the detached watchdog puts the old program back and stops it, and the
// supervisor's restart runs the old one again.
func TestWindowsSupervisedUpdateConfirmsOrRollsBack(t *testing.T) {
	old, next := fixtures(t)
	data, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"confirmed", "unconfirmed", "startup-crash", "legacy-build"} {
		t.Run(outcome, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "nu-win-*") // short: the fixture's unix socket path must stay under 108 bytes
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { removeEventually(dir) })
			exe := filepath.Join(dir, "node.exe")
			if err := copyFile(old, exe); err != nil {
				t.Fatal(err)
			}
			oldHash := hash(t, exe)
			proof := filepath.Join(dir, "proof")
			env := append(os.Environ(), "HOME="+dir, "PERSONA_HOME="+dir, "FIXTURE_PROOF="+proof)
			switch outcome {
			case "startup-crash":
				env = append(env, "FIXTURE_CRASH=next")
			case "legacy-build": // the new program runs but cannot take updates here: it never records its new pid
				env = append(env, "FIXTURE_LEGACY=next")
			}
			s := superviseWindowsFixture(t, exe, env)
			waitFor(t, 10*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
			firstPID, _ := s.current()

			peer, c := fixturePeer(t, filepath.Join(dir, "fixture.sock"))
			var begin proto.NodeUpdateBeginResult
			call(t, peer, "begin", spec(data, "next"), &begin)
			for offset := int64(0); offset < int64(len(data)); {
				end := min(offset+MaxChunk, int64(len(data)))
				var got proto.NodeUpdateChunkResult
				call(t, peer, "chunk", proto.NodeUpdateChunkParams{ID: begin.ID, Offset: offset, Data: data[offset:end]}, &got)
				offset = got.Offset
			}
			var accepted proto.NodeUpdateCommitResult
			call(t, peer, "commit", proto.NodeUpdateCommitParams{ID: begin.ID}, &accepted)
			if !accepted.Accepted {
				t.Fatal("commit not accepted")
			}
			p, err := loadPending(markerPath(exe))
			if err != nil {
				t.Fatal(err)
			}
			if p.WatchPID < 2 || p.WatchStart == "" || !aliveIs(p.WatchPID, p.WatchStart, p.WatchPrecise) {
				t.Fatal("the detached watchdog is not running as it recorded itself")
			}
			if outcome != "confirmed" {
				if err := locked(exe, func() error {
					p, err := loadPending(markerPath(exe))
					if err != nil {
						return err
					}
					p.Deadline = time.Now().Add(1500 * time.Millisecond)
					return savePending(p)
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = peer.Call(ctx, "response-written", proto.NodeUpdateCommitParams{ID: begin.ID}, nil)
			cancel()
			_ = c.Close()
			peer.Close(io.EOF)

			switch outcome {
			case "confirmed":
				// No exec: the old process exited asking to be started again, and the supervisor started the new file.
				waitFor(t, 10*time.Second, func() bool { p, ok := proofOf(proof); return ok && p.Version == "next" })
				if got, _ := proofOf(proof); got.PID == firstPID {
					t.Fatal("the new version runs in the old process")
				}
				if _, starts := s.current(); starts != 2 {
					t.Fatalf("%d starts, want the first and the one after the update", starts)
				}
				peer, c := fixturePeer(t, filepath.Join(dir, "fixture.sock"))
				call(t, peer, "confirm", proto.NodeUpdateConfirmParams{Version: "next"}, nil)
				_ = c.Close()
				peer.Close(io.EOF)
				if _, err := os.Stat(markerPath(exe)); !os.IsNotExist(err) {
					t.Fatal("pending transaction not confirmed")
				}
				if sum := sha256.Sum256(data); hash(t, exe) != hex.EncodeToString(sum[:]) {
					t.Fatal("installed file is not the new version")
				}
				// The watchdog exits once it sees the marker gone; the backup it ran from and the old program's file
				// go with the next cleanup.
				waitFor(t, 5*time.Second, func() bool { return !aliveIs(p.WatchPID, p.WatchStart, p.WatchPrecise) })
				waitFor(t, 5*time.Second, func() bool {
					removeRetired(dir)
					left, _ := filepath.Glob(filepath.Join(dir, ".node-update-*"))
					backups, _ := filepath.Glob(exe + ".node-update-backup-*")
					return len(left) == 0 && len(backups) == 0
				})
			default:
				// Nobody confirms: the watchdog restores the old file (the new program's is moved aside while it
				// runs) and stops the node; the supervisor's restart runs the old program, which clears the transaction.
				var legacyPID int
				if outcome == "unconfirmed" || outcome == "legacy-build" {
					waitFor(t, 10*time.Second, func() bool { p, ok := proofOf(proof); return ok && p.Version == "next" })
					got, _ := proofOf(proof)
					legacyPID = got.PID
				}
				waitFor(t, 15*time.Second, func() bool {
					p, ok := proofOf(proof)
					pid, _ := s.current()
					return ok && p.Version == "old" && p.PID == pid && pid != firstPID
				})
				waitFor(t, 5*time.Second, func() bool { _, err := os.Stat(markerPath(exe)); return os.IsNotExist(err) })
				if hash(t, exe) != oldHash {
					t.Fatal("old executable was not restored")
				}
				if legacyPID != 0 && osproc.AliveMine(legacyPID) {
					t.Fatalf("the new program (pid %d) still runs after the rollback", legacyPID)
				}
			}
		})
	}
}

// removeEventually removes dir once the processes that ran from it are gone: Windows keeps a program's file until then.
func removeEventually(dir string) {
	for deadline := time.Now().Add(5 * time.Second); os.RemoveAll(dir) != nil && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond) // sleep-ok: polling for the last process to let go of its file
	}
}
