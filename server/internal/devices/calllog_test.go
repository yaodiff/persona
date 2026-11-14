package devices

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// logLines collects what d logs.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// with is the lines that contain every one of parts.
func (l *logLines) with(parts ...string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
next:
	for _, line := range l.lines {
		for _, p := range parts {
			if !strings.Contains(line, p) {
				continue next
			}
		}
		out = append(out, line)
	}
	return out
}

func TestCallResult(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, callOK},
		{context.DeadlineExceeded, callDeadline},
		{fmt.Errorf("start: %w", context.DeadlineExceeded), callDeadline},
		{&rpc.Error{Message: "context deadline exceeded"}, callDeadline},
		{context.Canceled, callCanceled},
		{fmt.Errorf("m1: %w", errOffline), callOffline},
		{rpc.ErrClosed, callClosed},
		{rpc.ErrDropped, callClosed},
		{rpc.ErrSuspended, callClosed},
		{&rpc.Error{Message: "rpc: connection closed"}, callClosed},
		{&rpc.Error{Code: "terminal_not_ready", Message: "no"}, callError},
		{errors.New("boom"), callError},
	} {
		if got := callResult(c.err); got != c.want {
			t.Errorf("callResult(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}

// A failed call always leaves a line with its machine, method, time and kind of failure; a quick one that worked
// does not (unless sampled), a session start always does (#7509).
func TestMachineCallLogsFailures(t *testing.T) {
	d := newTestDevices(t)
	var log logLines
	d.logf = log.logf
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	m.firstTried()
	err := m.call(context.Background(), "session.start", map[string]any{}, nil)
	if !errors.Is(err, errOffline) {
		t.Fatalf("call to an unconnected machine: %v", err)
	}
	lines := log.with("[m1] rpc call method=session.start result=offline took=", `err="`)
	if len(lines) != 1 {
		t.Fatalf("log: %q", log.lines)
	}

	// Offline calls: the first in a minute is logged, the rest not (a start always is); the next line after the minute
	// counts them.
	m.logCall("node.configure", errOffline, time.Millisecond, 1)
	m.logCall("usage.read", errOffline, time.Millisecond, 1)
	m.logCall("session.start", errOffline, time.Millisecond, 1)
	if len(log.with("result=offline")) != 3 || len(log.with("method=session.start result=offline")) != 2 {
		t.Fatalf("offline calls within the minute: %q", log.lines)
	}
	m.callLogMu.Lock()
	m.offlineLogged = time.Now().Add(-callOfflineEvery)
	m.callLogMu.Unlock()
	m.logCall("node.configure", errOffline, time.Millisecond, 1)
	if len(log.with("method=node.configure result=offline", "offline_not_logged=1")) != 1 {
		t.Fatalf("the offline line after the minute: %q", log.lines)
	}

	m.logCall("ping", nil, time.Millisecond, 1)
	if got := log.with("method=ping"); len(got) != 0 {
		t.Fatalf("a quick ping that worked was logged: %q", got)
	}
	m.logCall("session.start", nil, time.Second, 1)
	m.logCall("ping", nil, 3*time.Second, 1)
	if len(log.with("method=session.start result=ok took=1s")) != 1 || len(log.with("method=ping result=ok took=3s")) != 1 {
		t.Fatalf("a start or a slow call that worked was not logged: %q", log.lines)
	}
	m.logCall("session.start", rpc.ErrDropped, time.Minute, 2)
	if len(log.with("method=session.start result=closed took=1m0s sent=true tries=2", `err="rpc: connection closed"`)) != 1 {
		t.Fatalf("dropped start: %q", log.lines)
	}
	m.logCall("session.watch", context.Canceled, time.Millisecond, 1)
	if len(log.with("method=session.watch")) != 0 {
		t.Fatalf("a quick cancel was logged: %q", log.lines)
	}
	m.logCall("session.send", &rpc.Error{Code: "busy", Message: "the agent is busy"}, time.Millisecond, 1)
	if len(log.with("method=session.send result=error", "code=busy")) != 1 {
		t.Fatalf("an answered error: %q", log.lines)
	}
}

// A connection that ends says how long it lasted and why, the calls it took with it, and the next one says it is a
// reconnect and why the last one ended (#7509).
func TestReconnectLogsSayWhy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH subprocess wrapper requires a POSIX shell")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexec \"$PERSONA_TEST_PEER_BINARY\" -test.run '^TestMachinePeerHelper$'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PERSONA_TEST_PEER_BINARY", bin)
	t.Setenv("PERSONA_TEST_MACHINE_PEER", "1")
	d := newTestDevices(t)
	var log logLines
	d.logf = log.logf
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "test-node"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	eventually(t, 5*time.Second, "first connection", func() bool { return len(log.with("[m1] connected over ssh", "(trigger: first)")) == 1 })
	m.mu.Lock()
	peer := m.peer
	m.mu.Unlock()
	peer.OnClose(errors.New("reconnect requested"), []string{"session.start"}) // what Close reports, as it would with a call out
	m.reconnect()
	eventually(t, 5*time.Second, "reconnect", func() bool {
		return len(log.with("[m1] connected over ssh", "(trigger: reconnect after", "the ssh connection ended: reconnect requested)")) == 1
	})
	if len(log.with("[m1] connection ended: via=ssh lasted=", `reason="reconnect requested"`)) != 1 {
		t.Fatalf("no line for the connection's end: %q", log.lines)
	}
	if len(log.with(`[m1] connection over ssh closed with 1 call(s) out: session.start (reason: "reconnect requested")`)) != 1 {
		t.Fatalf("no line for the calls cut off: %q", log.lines)
	}
}
