package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// With herdr installed and no server running, the socket is not there and the CLI says so: a node polling twice a
// second ran herdr twice a second. The CLI now runs once per backoff step (1 s, 2 s, … 30 s), every call still gets
// its answer, and a server the CLI reaches (or the socket) ends the backoff at once.
func TestCLIFallbackBacksOffWithoutAServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in herdr is a shell script")
	}
	dir := t.TempDir()
	execs, up := filepath.Join(dir, "execs"), filepath.Join(dir, "up")
	const noServer = `no herdr server is running at /tmp/h/herdr.sock; run ` + "`herdr`" + ` to start or attach it`
	script := "#!/bin/sh\necho x >> " + execs + "\nif [ -e " + up + " ]; then echo '{\"result\":{\"agents\":[]}}'; exit 0; fi\n" +
		`echo '{"id":"cli:agent:list","error":{"code":"server_not_running","message":"` + noServer + `"}}' >&2` + "\nexit 1\n"
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	c := &Client{Bin: bin, Socket: filepath.Join(dir, "none.sock"), now: func() time.Time { return now }}
	ran := func() int {
		b, _ := os.ReadFile(execs)
		return strings.Count(string(b), "x")
	}
	poll := func() error { return c.call(context.Background(), "agent.list", nil, &struct{}{}, "agent", "list") }

	// 60 s of polls, two calls every half second: the CLI runs at 0, 1, 3, 7, 15 and 31 s.
	var at []time.Duration
	for step := time.Duration(0); step < time.Minute; step += 500 * time.Millisecond {
		now = time.Unix(1_000_000, 0).Add(step)
		for range 2 {
			before := ran()
			if err := poll(); err == nil || err.Error() != "server_not_running: "+noServer {
				t.Fatalf("at %v: %v, want the CLI's own answer", step, err)
			}
			if ran() > before {
				at = append(at, step)
			}
		}
	}
	want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second, 31 * time.Second}
	if len(at) != len(want) || !equalDurations(at, want) {
		t.Fatalf("the CLI ran at %v, want %v (240 calls)", at, want)
	}

	// A server only the CLI reaches: found once the step is over, then never backed off.
	if err := os.WriteFile(up, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(cliBackoffMax)
	for i := range 4 {
		before := ran()
		if err := poll(); err != nil || ran() != before+1 {
			t.Fatalf("call %d with a server the CLI reaches: %v, ran %d times", i, err, ran()-before)
		}
	}

	// The socket answering ends a backoff too.
	if err := os.Remove(up); err != nil {
		t.Fatal(err)
	}
	_ = poll() // no server again: backed off
	if before := ran(); poll() == nil || ran() != before {
		t.Fatal("not backed off")
	}
	sock, missing := fakeServer(t, func(string, map[string]any) string { return `"result":{"agents":[]}` }), c.Socket
	c.Socket = sock
	if err := poll(); err != nil {
		t.Fatal(err)
	}
	c.Socket = missing
	if before := ran(); poll() == nil || ran() != before+1 {
		t.Fatal("a socket that answered did not end the backoff")
	}
}

// A caller whose context ended before herdr's socket answered gets that context's error back, and the CLI is not
// tried under it: the fallback could only report a failure of its own (with no CLI configured, as in the node's tests,
// "exec: no command") in place of the caller's deadline (#16008). Each context has ended before the call, so nothing
// here depends on timing. A live caller without a socket still falls back to the CLI.
func TestEndedCallerSkipsCLIFallback(t *testing.T) {
	sock := fakeServer(t, func(string, map[string]any) string { return `"result":{"agents":[],"read":{"text":"hi"}}` })
	missing := filepath.Join(t.TempDir(), "missing.sock")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(1_000_000, 0))
	defer cancelExpired()
	for _, tc := range []struct {
		name string
		do   func(*Client, context.Context) error
	}{
		{"call", func(c *Client, ctx context.Context) error { _, err := c.Agents(ctx); return err }},
		{"pane read", func(c *Client, ctx context.Context) error {
			_, err := c.PaneRead(ctx, "w1:p1", "recent", 60)
			return err
		}},
		{"colored pane read", func(c *Client, ctx context.Context) error {
			_, err := c.PaneReadANSI(ctx, "w1:p1", "recent", 60)
			return err
		}},
	} {
		// No Bin: a CLI fallback fails with "exec: no command".
		if err := tc.do(&Client{Socket: sock}, context.Background()); err != nil {
			t.Fatalf("%s with a live caller and a socket: %v", tc.name, err)
		}
		for _, socket := range []string{sock, missing} {
			for _, ctx := range []context.Context{canceled, expired} {
				if err := tc.do(&Client{Socket: socket}, ctx); !errors.Is(err, ctx.Err()) {
					t.Errorf("%s on %s after the caller gave up: %v, want %v", tc.name, filepath.Base(socket), err, ctx.Err())
				}
			}
		}
		if err := tc.do(&Client{Socket: missing}, context.Background()); err == nil || !strings.Contains(err.Error(), "no command") {
			t.Errorf("%s with a live caller and no socket: %v, want the CLI fallback's failure", tc.name, err)
		}
	}
}

func equalDurations(a, b []time.Duration) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return len(a) == len(b)
}
