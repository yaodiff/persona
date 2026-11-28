package node

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// When herdr's probe times out (busy host), herdrRunning must report known=false instead of falsely
// declaring "herdr is not running" (known=true, running=false) (#13578).
func TestHerdrProbeDistinguishesTimeoutFromMissing(t *testing.T) {
	n, sock := downHerdrNode(t)

	// 1. Genuinely missing socket -> running=false, known=true.
	if running, known := n.herdrRunning(context.Background()); running || !known {
		t.Fatalf("missing socket: running=%v known=%v, want false, true", running, known)
	}

	// 2. Listening socket with healthy context -> running=true, known=true.
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	serveSocket(t, sock)
	if running, known := n.herdrRunning(context.Background()); !running || !known {
		t.Fatalf("live socket: running=%v known=%v, want true, true", running, known)
	}

	// 3. Timed-out / deadline-exceeded probe -> running=false, known=false (inconclusive, not missing).
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if running, known := n.herdrRunning(expired); running || known {
		t.Fatalf("timed-out probe: running=%v known=%v, want false, false", running, known)
	}
}

// When probing herdr times out or hits EAGAIN under load, ensureHerdr returns errHerdrBusy
// without spawning a duplicate herdr server (#13578).
func TestEnsureHerdrDoesNotSpawnDuplicateOnTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "deadline exceeded", err: context.DeadlineExceeded},
		{name: "eagain", err: syscall.EAGAIN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := downHerdrNode(t)
			fl := &fakeLaunch{}
			withLaunchHerdr(t, fl)
			prevDial := dialHerdr
			dialHerdr = func(context.Context, string) (net.Conn, error) {
				return nil, tc.err
			}
			t.Cleanup(func() { dialHerdr = prevDial })

			if running, known := n.herdrRunning(context.Background()); running || known {
				t.Fatalf("herdrRunning under %s: running=%v known=%v, want false, false", tc.name, running, known)
			}
			err := n.ensureHerdr(context.Background(), "busy probe test")
			if !errors.Is(err, errHerdrBusy) {
				t.Fatalf("ensureHerdr error = %v, want errHerdrBusy", err)
			}
			if got := fl.starts.Load(); got != 0 {
				t.Fatalf("ensureHerdr spawned %d duplicate server(s) during busy probe", got)
			}
		})
	}
}
