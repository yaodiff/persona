package nodeupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

func waitDrainDuration(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func TestDrainHoldsWatchdogUntilHandoverAndThenConfirmsOrRollsBack(t *testing.T) {
	t.Run("long-drain-then-confirm", func(t *testing.T) {
		m, data, _ := testManager(t)
		m.SetTimeoutForTest(300 * time.Millisecond)
		targetHash := spec(data, "next").SHA256
		id := receive(t, m, data, spec(data, "next"))
		if _, err := m.Commit(t.Context(), id, nil); err != nil {
			t.Fatal(err)
		}
		holder := startHolder(t)
		start, err := processStart(holder.PID)
		if err != nil {
			t.Fatal(err)
		}
		if err := locked(m.exe, func() error {
			p, err := loadPending(markerPath(m.exe))
			if err != nil {
				return err
			}
			p.PID = holder.PID
			p.ProcessStart = start
			p.ProcessPrecise = osproc.Identify(holder.PID).Start
			return savePending(p)
		}); err != nil {
			t.Fatal(err)
		}
		if err := m.BeginDrain(id); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		watchDone := make(chan error, 1)
		go func() { watchDone <- Watch(ctx, markerPath(m.exe), id, nil) }()

		// Drain 2.6x past the 300ms watchdog timeout: draining resident stays alive and unrolled-back.
		if err := waitDrainDuration(ctx, 800*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-watchDone:
			t.Fatalf("watchdog exited prematurely during drain: %v", err)
		default:
		}
		select {
		case err := <-holder.Done:
			t.Fatalf("watchdog killed live draining process: %v", err)
		default:
		}
		p, err := loadPending(markerPath(m.exe))
		if err != nil || !p.Draining {
			t.Fatalf("marker during drain = %+v, err=%v", p, err)
		}
		if hash(t, m.exe) != targetHash {
			t.Fatal("watchdog rolled back binary during legitimate drain")
		}

		// Handover clears Draining and resets Deadline to a fresh post-exec confirmation window.
		if err := m.ArmHandover(id); err != nil {
			t.Fatal(err)
		}
		p, err = loadPending(markerPath(m.exe))
		if err != nil || p.Draining {
			t.Fatalf("marker after ArmHandover = %+v, err=%v", p, err)
		}
		if remaining := time.Until(p.Deadline); remaining < 100*time.Millisecond {
			t.Fatalf("post-handover deadline too short: %s", remaining)
		}

		fresh := &Manager{exe: m.exe, version: "next", timeout: 300 * time.Millisecond, startWatch: func(string, string, string) error { return nil }}
		if err := fresh.recover(); err != nil {
			t.Fatal(err)
		}
		if err := fresh.Confirm("next"); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-watchDone:
			if err != nil {
				t.Fatalf("watchdog after confirm: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("watchdog did not exit after confirm: %v", ctx.Err())
		}
		if hash(t, m.exe) != targetHash {
			t.Fatal("confirmed target binary was not kept")
		}
		select {
		case err := <-holder.Done:
			t.Fatalf("holder process was unexpectedly killed: %v", err)
		default:
		}
	})

	t.Run("long-drain-then-post-handover-hang-rolls-back", func(t *testing.T) {
		m, data, _ := testManager(t)
		oldHash := hash(t, m.exe)
		m.SetTimeoutForTest(300 * time.Millisecond)
		id := receive(t, m, data, spec(data, "next"))
		if _, err := m.Commit(t.Context(), id, nil); err != nil {
			t.Fatal(err)
		}
		holder := startHolder(t)
		start, err := processStart(holder.PID)
		if err != nil {
			t.Fatal(err)
		}
		if err := locked(m.exe, func() error {
			p, err := loadPending(markerPath(m.exe))
			if err != nil {
				return err
			}
			p.PID = holder.PID
			p.ProcessStart = start
			p.ProcessPrecise = osproc.Identify(holder.PID).Start
			return savePending(p)
		}); err != nil {
			t.Fatal(err)
		}
		if err := m.BeginDrain(id); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		watchDone := make(chan error, 1)
		go func() { watchDone <- Watch(ctx, markerPath(m.exe), id, nil) }()

		// Survive a drain 2.5x longer than the post-exec confirmation timeout.
		if err := waitDrainDuration(ctx, 750*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-holder.Done:
			t.Fatalf("draining process killed before handover: %v", err)
		default:
		}

		// Arm handover and never confirm: watchdog must roll back and terminate the hung process.
		if err := m.ArmHandover(id); err != nil {
			t.Fatal(err)
		}
		waitKilled(t, ctx, holder.Done)
		select {
		case err := <-watchDone:
			if err != nil {
				t.Fatalf("watchdog rollback error: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("watchdog did not complete rollback: %v", ctx.Err())
		}
		if hash(t, m.exe) != oldHash {
			t.Fatal("old executable was not restored after post-handover hang")
		}
	})

	t.Run("unexpected-exit-during-drain-rolls-back", func(t *testing.T) {
		m, data, _ := testManager(t)
		oldHash := hash(t, m.exe)
		m.SetTimeoutForTest(250 * time.Millisecond)
		id := receive(t, m, data, spec(data, "next"))
		if _, err := m.Commit(t.Context(), id, nil); err != nil {
			t.Fatal(err)
		}
		holder := startHolder(t)
		start, err := processStart(holder.PID)
		if err != nil {
			t.Fatal(err)
		}
		if err := locked(m.exe, func() error {
			p, err := loadPending(markerPath(m.exe))
			if err != nil {
				return err
			}
			p.PID = holder.PID
			p.ProcessStart = start
			p.ProcessPrecise = osproc.Identify(holder.PID).Start
			return savePending(p)
		}); err != nil {
			t.Fatal(err)
		}
		if err := m.BeginDrain(id); err != nil {
			t.Fatal(err)
		}
		// Stop the manager's keepalive before simulating an abrupt resident crash so the marker
		// mirrors a dead resident whose process exited while Draining was true.
		m.stopDrain()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		watchDone := make(chan error, 1)
		go func() { watchDone <- Watch(ctx, markerPath(m.exe), id, nil) }()

		// Kill the draining process abruptly.
		if proc, err := os.FindProcess(holder.PID); err == nil {
			_ = proc.Kill()
		}
		<-holder.Done

		select {
		case err := <-watchDone:
			if err != nil {
				t.Fatalf("watchdog rollback after crash during drain: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("watchdog did not roll back after draining process died: %v", ctx.Err())
		}
		if hash(t, m.exe) != oldHash {
			t.Fatal("old executable was not restored after draining process crashed")
		}
	})
}

func TestRecoverClearsDrainingAndArmsFreshConfirmDeadline(t *testing.T) {
	m, data, _ := testManager(t)
	m.SetTimeoutForTest(300 * time.Millisecond)
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	m.ResponseWritten(id)
	if err := m.BeginDrain(id); err != nil {
		t.Fatal(err)
	}
	m.stopDrain()

	// Even if Deadline had already passed while Draining was set, new-binary startup recovery
	// clears Draining and arms a fresh post-exec confirmation window instead of rolling back.
	if err := locked(m.exe, func() error {
		p, err := loadPending(markerPath(m.exe))
		if err != nil {
			return err
		}
		p.Deadline = time.Now().Add(-time.Second)
		return savePending(p)
	}); err != nil {
		t.Fatal(err)
	}

	fresh := &Manager{exe: m.exe, version: "next", timeout: 5 * time.Second, startWatch: func(string, string, string) error { return nil }}
	if err := fresh.recover(); err != nil {
		t.Fatal(err)
	}
	if fresh.RestartRequired() {
		t.Fatal("recover unexpectedly rolled back a healthy new binary that started from a draining marker")
	}
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if p.Draining {
		t.Fatal("recover left Draining=true on the new binary's marker")
	}
	if time.Until(p.Deadline) < 2*time.Second {
		t.Fatalf("recover did not arm a fresh confirmation deadline: %s", time.Until(p.Deadline))
	}
	if err := fresh.Confirm("next"); err != nil {
		t.Fatal(err)
	}
}

// Regression test for #15708: long drain on an externally installed binary preserves adoption
// semantics without ever creating a backup, starting a watchdog, expiring the adoption record,
// or rolling back the shared executable.
func TestLongDrainPreservesAdoptInstalledWithoutBackupOrWatchdog(t *testing.T) {
	m, data, _ := testManager(t)
	refuseWatch(t, m)
	m.SetTimeoutForTest(250 * time.Millisecond)
	target := spec(data, "next")
	id := receive(t, m, data, target)
	installed := installExternally(t, m, data)

	if _, err := m.Commit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	m.ResponseWritten(id)
	if err := m.BeginDrain(id); err != nil {
		t.Fatal(err)
	}
	// Drain >2.5x longer than the adoption timeout while the keepalive refreshes AdoptionDeadline.
	if err := waitDrainDuration(t.Context(), 650*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.expireLocked()
	stillHeld := m.upload != nil && m.upload.ID == id
	m.mu.Unlock()
	if !stillHeld {
		t.Fatal("long drain expired the adopted upload before handover")
	}
	if _, err := os.Stat(backupPath(m.exe, id)); !os.IsNotExist(err) {
		t.Fatalf("adopted drain unexpectedly created a backup: %v", err)
	}
	p, err := loadPending(markerPath(m.exe))
	if err != nil || !p.Adopted || p.Draining {
		t.Fatalf("adopted marker during drain = %+v, err=%v", p, err)
	}

	if err := m.ArmHandover(id); err != nil {
		t.Fatal(err)
	}
	fresh := &Manager{exe: m.exe, version: "next", startWatch: m.startWatch}
	if err := fresh.recover(); err != nil || fresh.RestartRequired() {
		t.Fatalf("adopted recover failed: err=%v restart=%v", err, fresh.RestartRequired())
	}
	if err := fresh.Confirm("next"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
		t.Fatal("adopted marker was not removed on confirm")
	}
	assertInstalledUntouched(t, m.exe, installed, target.SHA256)
}

func TestPreflightFailedStopsDrainAndArmsRollback(t *testing.T) {
	m, data, _ := testManager(t)
	oldHash := hash(t, m.exe)
	m.SetTimeoutForTest(500 * time.Millisecond)
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	m.ResponseWritten(id)
	if err := m.BeginDrain(id); err != nil {
		t.Fatal(err)
	}
	reason := errors.New("exit status 23")
	m.PreflightFailed(id, reason)

	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if p.Draining {
		t.Fatal("PreflightFailed left Draining=true, which would stall watchdog rollback")
	}
	if hash(t, p.Backup) != oldHash {
		t.Fatal("PreflightFailed disturbed backup")
	}
	if _, err := m.Commit(t.Context(), id, nil); !errors.Is(err, reason) {
		t.Fatalf("retry commit = %v, want %v", err, reason)
	}
}

func TestDeclaredSupervisorLongDrainConfirmsAndPostExecCrashRollsBack(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("a declared supervisor counts on Linux and macOS only")
	}
	old, next := fixtures(t)
	data, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	nextHash := spec(data, "next").SHA256

	for _, crash := range []bool{false, true} {
		name := "long-drain-confirms"
		if crash {
			name = "long-drain-then-post-exec-crash-rolls-back"
		}
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "nu-drain-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			dir, err = filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, "node")
			if err := copyFile(old, exe); err != nil {
				t.Fatal(err)
			}
			oldHash := hash(t, exe)
			proof := filepath.Join(dir, "proof")
			gate := filepath.Join(dir, "release-drain")
			env := append(os.Environ(),
				"HOME="+dir,
				"PERSONA_HOME="+dir,
				"INVOCATION_ID=",
				"XPC_SERVICE_NAME=",
				"FIXTURE_PROOF="+proof,
				"FIXTURE_TIMEOUT_MS=600",
				"FIXTURE_DRAIN_GATE="+gate,
			)
			if crash {
				env = append(env, "FIXTURE_CRASH=next")
			}

			s := superviseFixture(t, exe, env)
			waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
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
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			_ = peer.Call(ctx, "response-written", proto.NodeUpdateCommitParams{ID: begin.ID}, nil)
			cancel()
			_ = c.Close()
			peer.Close(io.EOF)

			// Wait for the resident to enter drain, then hold the drain 2.5x longer than the 600ms timeout.
			waitFor(t, 5*time.Second, func() bool { _, err := os.Stat(gate + ".draining"); return err == nil })
			if err := waitDrainDuration(t.Context(), 1500*time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if got, ok := proofOf(proof); !ok || got.Version != "old" || got.PID != firstPID {
				t.Fatalf("draining process disturbed before handover: proof=%+v ok=%v firstPID=%d", got, ok, firstPID)
			}
			if pid, starts := s.current(); pid != firstPID || starts != 1 {
				t.Fatalf("watchdog/supervisor restarted draining process: pid=%d starts=%d", pid, starts)
			}
			if hash(t, exe) != nextHash {
				t.Fatal("watchdog rolled back installed binary while draining")
			}

			// Release the drain gate so the resident arms handover and execs into next.
			if err := os.WriteFile(gate, []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			if !crash {
				waitFor(t, 8*time.Second, func() bool { p, ok := proofOf(proof); return ok && p.Version == "next" && p.PID == firstPID })
				peer, c := fixturePeer(t, filepath.Join(dir, "fixture.sock"))
				call(t, peer, "confirm", proto.NodeUpdateConfirmParams{Version: "next"}, nil)
				_ = c.Close()
				peer.Close(io.EOF)
				if _, err := os.Stat(markerPath(exe)); !os.IsNotExist(err) {
					t.Fatal("marker remained after confirm")
				}
				if hash(t, exe) != nextHash {
					t.Fatal("installed binary mismatch after confirm")
				}
				if pid, starts := s.current(); pid != firstPID || starts != 1 {
					t.Fatalf("unexpected supervisor restart: pid=%d starts=%d", pid, starts)
				}
			} else {
				// Next crashes on startup; watchdog rolls back after the fresh 600ms post-exec deadline.
				waitFor(t, 10*time.Second, func() bool {
					p, ok := proofOf(proof)
					pid, _ := s.current()
					return ok && p.Version == "old" && p.PID == pid && pid != firstPID
				})
				waitFor(t, 5*time.Second, func() bool { _, err := os.Stat(markerPath(exe)); return os.IsNotExist(err) })
				if hash(t, exe) != oldHash {
					t.Fatal("old executable was not restored after post-exec crash")
				}
			}
		})
	}
}
