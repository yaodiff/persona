package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexRemoteSharedNotifications(t *testing.T) {
	n := remoteTestNode(t)
	c := &codexRemote{n: n, ready: true, threads: map[string]remoteThread{
		remoteRaw: {publicID: remoteRaw, turn: "t1", partial: "streaming", buffer: &strings.Builder{}},
	}}
	c.message(codexMessage{Method: "item/started", Params: json.RawMessage(`{"threadId":"` + remoteRaw + `","item":{"type":"plan"}}`)})
	for _, tc := range []struct {
		method, fields string
		check          func(remoteThread) bool
	}{
		{"thread/status/changed", `,"status":{"type":"active","activeFlags":[]}`, func(th remoteThread) bool { return th.status == proto.StatusWorking }},
		{"thread/status/changed", `,"status":{"type":"active","activeFlags":["waitingOnApproval"]}`, func(th remoteThread) bool { return th.status == proto.StatusBlocked }},
		{"thread/status/changed", `,"status":{"type":"idle"}`, func(th remoteThread) bool { return th.status == proto.StatusDone }},
		{"thread/status/changed", `,"status":{"type":"active","activeFlags":["waitingOnUserInput"]}`, func(th remoteThread) bool { return th.status == proto.StatusBlocked }},
		{"thread/name/updated", `,"threadName":"New title"`, func(th remoteThread) bool { return th.title == "New title" }},
		{"thread/name/updated", `,"threadName":null`, func(th remoteThread) bool { return th.title == "New title" }},
		{"thread/queue/changed", "", nil},
		{"item/plan/delta", `,"itemId":"plan1","delta":"Plan"`, func(th remoteThread) bool { return th.partial == "Plan" }},
		{"item/plan/delta", `,"itemId":"plan1","delta":" text"`, func(th remoteThread) bool { return th.partial == "Plan text" }},
		{"turn/plan/updated", `,"plan":[{"step":"Verify","status":"inProgress"}]`, nil},
		{"thread/closed", "", func(th remoteThread) bool {
			return th.status == proto.StatusDone && th.turn == "" && th.partial == "" && th.buffer == nil
		}},
	} {
		c.message(codexMessage{Method: tc.method, Params: json.RawMessage(`{"threadId":"` + remoteRaw + `"` + tc.fields + `}`)})
		if got := threadOf(c, remoteRaw); tc.check != nil && !tc.check(got) {
			t.Fatalf("%s%s did not update and notify: %+v", tc.method, tc.fields, got)
		}
		select {
		case <-n.codices.kick:
		default:
			t.Fatalf("%s did not notify", tc.method)
		}
	}
}

func TestCodexRemoteRefreshPreservesBlocked(t *testing.T) {
	n := remoteTestNode(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "shared", remoteRaw, remoteOther)
	f.mu.Lock()
	f.active = true // Both thread/read snapshots now report Working.
	f.mu.Unlock()
	c.mu.Lock()
	th := c.threads[remoteRaw]
	th.status, th.turn = proto.StatusBlocked, "t1"
	c.threads[remoteRaw] = th
	c.asks["request"] = &remoteAsk{thread: remoteOther, cancel: func() {}}
	c.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{remoteRaw, remoteOther} {
		if got := threadOf(c, raw).status; got != proto.StatusBlocked {
			t.Errorf("%s: got %s, want Blocked", raw, got)
		}
	}
}

func TestCodexRemoteInterruptWaitsForLiveTurn(t *testing.T) {
	n, f, c, _ := daemonPane(t)
	c.message(codexMessage{Method: "turn/started", Params: json.RawMessage(`{"threadId":"` + remoteRaw + `","turn":{"id":"t1"}}`)})
	f.mu.Lock()
	f.response = func(method string, _ map[string]any) (any, bool) {
		if method == "turn/interrupt" {
			c.message(codexMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"` + remoteRaw + `","turn":{"id":"t1","status":"interrupted"}}`)})
		}
		return nil, false
	}
	f.mu.Unlock()
	start := time.Now()
	if !n.interruptCodexActiveTurn(context.Background(), remoteRaw) || time.Since(start) >= 350*time.Millisecond {
		t.Fatalf("interrupt should return as soon as the completed turn is observed, took %v", time.Since(start))
	}
}

func TestCodexRemotePendingReplayWhileJoining(t *testing.T) {
	n := remoteTestNode(t)
	hub := withFakeHub(t, n)
	home := t.TempDir()
	f, _ := connectRemoteFixture(t, n, home, "one", remoteRaw)
	f.mu.Lock()
	f.resumeRequest = true
	f.mu.Unlock()
	id := n.registerCodex(home, remoteRaw, "")
	if err := n.attachCodex(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replayed approval answered", func() bool { return f.replyCount() == 1 })
	f.push(map[string]any{"id": 71, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": remoteRaw, "itemId": "cmd-1", "command": "printf synthetic"}})
	time.Sleep(80 * time.Millisecond)
	if f.replyCount() != 1 || len(hub.interactions()) != 1 {
		t.Fatal("replayed request was answered twice")
	}
}

func TestCodexRemoteThreadDeltasAndWrappedItem(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw, remoteOther)
	for raw, text := range map[string]string{remoteRaw: "第一线程", remoteOther: "第二线程"} {
		f.push(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": raw, "delta": text}})
	}
	waitFor(t, "separate thread deltas", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.threads[remoteRaw].partial == "第一线程" && c.threads[remoteOther].partial == "第二线程"
	})
	f.push(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": remoteOther, "item": map[string]any{"payload": map[string]any{"type": "agentMessage", "id": "message2", "text": "第二线程完成"}}}})
	waitFor(t, "wrapped item decoded", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.threads[remoteOther].partial == "第二线程完成"
	})
	c.mu.Lock()
	first := c.threads[remoteRaw].partial
	c.mu.Unlock()
	if first != "第一线程" {
		t.Fatal("second thread overwrote first")
	}
}

func TestCodexStdioOpenRolloutIsVisibleButNeverWritable(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "01")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-synthetic-"+remoteRaw+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n.codices.scan = func() []codexProcess { return []codexProcess{{pid: os.Getpid(), home: home, endpoint: "stdio://"}} }
	n.discoverCodex()
	id := n.registerCodex(home, remoteRaw, path)
	list := n.codexSessions(map[string]bool{})
	for _, s := range list {
		if s.SessionID == id {
			if s.Status != proto.StatusUnknown || s.Capabilities.Send {
				t.Fatalf("stdio runtime capability: %+v", s)
			}
			return
		}
	}
	t.Fatal("open stdio rollout omitted")
}

// 2026-11-02: a task at work in the Codex desktop App showed on the phone under "Idle Spaces". The App's own app-server
// talks stdio, so node only sees which rollouts it holds open and called each unknown; the rollout says whether a
// turn runs, even when one turn has written more than the tail node reads.
func TestCodexStdioOpenRolloutStatusFollowsItsTurn(t *testing.T) {
	output := `{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"` + strings.Repeat("x", 64<<10) + `"}}`
	long := []string{event("task_started")}
	for range 8 { // 512 KB, more than readQuickMeta's 384 KB tail: the tail holds no task_started
		long = append(long, output)
	}
	cases := []struct {
		name   string
		events []string
		want   string
	}{
		{"in a turn", []string{event("task_started"), event("task_complete"), event("task_started"), output}, proto.StatusWorking},
		{"in a turn longer than the tail", long, proto.StatusWorking},
		{"after a turn longer than the tail", append(long[:len(long):len(long)], event("task_complete")), proto.StatusDone},
		{"after an interrupted turn", []string{event("task_started"), event("turn_aborted")}, proto.StatusDone},
		{"before the first turn", nil, proto.StatusUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := remoteTestNode(t)
			home := t.TempDir()
			dir := filepath.Join(home, "sessions", "2026", "11", "02")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			lines := append([]string{`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + remoteRaw + `","cwd":"/work"}}`}, c.events...)
			path := filepath.Join(dir, "rollout-synthetic-"+remoteRaw+".jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path) // held open like the App's app-server holds it
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			n.codices.scan = func() []codexProcess { return []codexProcess{{pid: os.Getpid(), home: home, endpoint: "stdio://"}} }
			n.discoverCodex()
			id := n.registerCodex(home, remoteRaw, path)
			for _, s := range n.codexSessions(map[string]bool{}) {
				if s.SessionID == id {
					if s.Status != c.want || s.Capabilities.Send || s.ReadOnlyReason == "" {
						t.Fatalf("status %q, want %q (still read-only): %+v", s.Status, c.want, s)
					}
					return
				}
			}
			t.Fatal("open stdio rollout omitted")
		})
	}
}

// #2425: a bridge kept stdio app-servers for months, holding open the rollouts of threads they once loaded; each
// showed "Unread" ("app-server is running") all along and stayed after the user looked. Untouched for a day, a held
// rollout is history again; a fresh one still follows its turn, and a codex TUI holding one is not called app-server.
func TestCodexHeldOpenRolloutGoesOfflineWhenUntouched(t *testing.T) {
	cases := []struct {
		name       string
		age        time.Duration
		endpoint   string
		wantStatus string
		wantReason string
	}{
		{"stdio app-server, months old", 145 * 24 * time.Hour, "stdio://", proto.StatusOffline, "仅查看：未发现可共享的 app-server 入口，请在电脑上打开会话"},
		{"stdio app-server, just written", 0, "stdio://", proto.StatusDone, "app-server 正在运行，但未提供共享入口；只能同步本地历史"},
		{"codex TUI, just written", 0, "", proto.StatusDone, "Codex 正在运行，但未提供共享入口；只能同步本地历史"},
		{"codex TUI, two days old", 48 * time.Hour, "", proto.StatusOffline, "仅查看：未发现可共享的 app-server 入口，请在电脑上打开会话"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := remoteTestNode(t)
			home := t.TempDir()
			dir := filepath.Join(home, "sessions", "2026", "06", "13")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-c.age).UTC()
			stamp := func(d time.Duration) string { return at.Add(d).Format(time.RFC3339) }
			lines := []string{
				`{"timestamp":"` + stamp(-2*time.Second) + `","type":"session_meta","payload":{"id":"` + remoteRaw + `","cwd":"/work"}}`,
				`{"timestamp":"` + stamp(-time.Second) + `","type":"event_msg","payload":{"type":"task_started"}}`,
				`{"timestamp":"` + stamp(0) + `","type":"event_msg","payload":{"type":"task_complete"}}`,
			}
			path := filepath.Join(dir, "rollout-synthetic-"+remoteRaw+".jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, at, at); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path) // held open by the process
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			n.codices.scan = func() []codexProcess { return []codexProcess{{pid: os.Getpid(), home: home, endpoint: c.endpoint}} }
			n.discoverCodex()
			id := n.registerCodex(home, remoteRaw, path)
			for _, s := range n.codexSessions(map[string]bool{}) {
				if s.SessionID == id {
					if s.Status != c.wantStatus || s.ReadOnlyReason != c.wantReason || s.Capabilities.Send {
						t.Fatalf("status %q reason %q, want %q %q", s.Status, s.ReadOnlyReason, c.wantStatus, c.wantReason)
					}
					return
				}
			}
			t.Fatal("held rollout omitted")
		})
	}
}

// #2425: the process that held a rollout open went away: the next scan puts the session back to offline.
func TestCodexRolloutNoLongerHeldGoesOffline(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "05")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-synthetic-"+remoteRaw+".jsonl")
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"`+remoteRaw+`","cwd":"/work"}}`+"\n"+event("task_started")+"\n"+event("task_complete")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	held := true
	n.codices.scan = func() []codexProcess {
		if !held {
			return nil
		}
		return []codexProcess{{pid: os.Getpid(), home: home, endpoint: "stdio://"}}
	}
	status := func() string {
		n.discoverCodex()
		id := n.registerCodex(home, remoteRaw, path)
		for _, s := range n.codexSessions(map[string]bool{}) {
			if s.SessionID == id {
				return s.Status
			}
		}
		return "omitted"
	}
	if got := status(); got != proto.StatusDone {
		t.Fatalf("held: %q", got)
	}
	held = false
	if got := status(); got != proto.StatusOffline {
		t.Fatalf("let go: %q", got)
	}
}

// #2425 on a real machine: two months-old sessions said "app-server is running" while the only app-server there was
// another tool's, under its own CODEX_HOME, holding nothing. Their rollouts were held by two codex TUIs left open in
// tmux, whose home is the user's, written as a symlink (/home → /data00/home). The rollout is the TUI's, not the
// app-server's: named as Codex, and history once it has sat untouched for a day.
func TestCodexHeldRolloutIsTheHoldersNotAnUnrelatedAppServers(t *testing.T) {
	for _, c := range []struct {
		name       string
		age        time.Duration
		wantStatus string
		wantReason string
	}{
		{"just written", 0, proto.StatusDone, "Codex 正在运行，但未提供共享入口；只能同步本地历史"},
		{"months old", 160 * 24 * time.Hour, proto.StatusOffline, "仅查看：未发现可共享的 app-server 入口，请在电脑上打开会话"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := remoteTestNode(t)
			real := t.TempDir()
			home := filepath.Join(t.TempDir(), "home")
			if err := os.Symlink(real, home); err != nil {
				t.Fatal(err)
			}
			other := t.TempDir() // the other tool's CODEX_HOME: no rollouts of the user's
			dir := filepath.Join(real, "sessions", "2026", "05", "23")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-c.age).UTC()
			lines := []string{
				`{"timestamp":"` + at.Add(-2*time.Second).Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"` + remoteRaw + `","cwd":"/work"}}`,
				`{"timestamp":"` + at.Add(-time.Second).Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"task_started"}}`,
				`{"timestamp":"` + at.Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"task_complete"}}`,
			}
			path := filepath.Join(dir, "rollout-2026-05-23T13-03-27-"+remoteRaw+".jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, at, at); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path) // held by the TUI (this process), opened by its real path
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			n.codices.scan = func() []codexProcess {
				return []codexProcess{
					{pid: os.Getppid(), home: other, endpoint: "stdio://"}, // an app-server holding none of these
					{pid: os.Getpid(), home: home},                         // a codex TUI, its home through the symlink
				}
			}
			n.discoverCodex()
			id := n.registerCodex(home, remoteRaw, path)
			if alias := n.registerCodex(real, remoteRaw, path); alias != id {
				t.Fatalf("one rollout, two ids through the alias: %s %s", id, alias)
			}
			for _, s := range n.codexSessions(map[string]bool{}) {
				if s.SessionID == id {
					if s.Status != c.wantStatus || s.ReadOnlyReason != c.wantReason {
						t.Fatalf("status %q reason %q, want %q %q", s.Status, s.ReadOnlyReason, c.wantStatus, c.wantReason)
					}
					return
				}
			}
			t.Fatal("held rollout omitted")
		})
	}
}
