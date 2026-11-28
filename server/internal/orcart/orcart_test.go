package orcart_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

// logs collects a client's log lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logs) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func client(f *orcarttest.Fake, l *logs) *orcart.Client {
	c := &orcart.Client{MetaPath: f.MetaPath}
	if l != nil {
		c.Log = l.logf
	}
	return c
}

// A call checks the runtime's protocol once (status.get), then lists; keepalive lines before an answer are skipped.
func TestCallChecksCompatOnceAndSkipsKeepalives(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "refs/heads/main", "x", "claude"))
	f.Keepalives(2)
	c := client(f, nil)
	for range 3 {
		l, err := c.Terminals(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Terminals) != 1 || l.Terminals[0].PaneKey() != "tab1:leaf1" || !l.Terminals[0].Local() {
			t.Fatalf("terminals: %+v", l.Terminals)
		}
		if len(l.Tabs) != 1 || l.Tabs[0].ID != "tab1" || l.Tabs[0].WorktreeID != "wt1" || l.Tabs[0].Title != "tab tab1" {
			t.Fatalf("tabs: %+v", l.Tabs)
		}
	}
	if n := f.Count("status.get"); n != 1 {
		t.Errorf("status.get %d times", n)
	}
	if tr := f.Trace("terminal.list"); len(tr) != 3 || !strings.Contains(tr[0], `"includePreview":false`) ||
		!strings.Contains(tr[0], `"includeVisualLayouts":true`) {
		t.Errorf("list calls: %v", tr)
	}
}

// Orca without its metadata file, or whose socket is gone, is not running: quiet, ErrNotRunning.
func TestNotRunning(t *testing.T) {
	l := &logs{}
	c := &orcart.Client{MetaPath: filepath.Join(t.TempDir(), "orca-runtime.json"), Log: l.logf}
	if _, err := c.Terminals(context.Background(), true); !errors.Is(err, orcart.ErrNotRunning) {
		t.Fatalf("no metadata: %v", err)
	}
	f := orcarttest.New(t)
	f.Stop()
	c = client(f, l)
	if _, err := c.Terminals(context.Background(), true); !errors.Is(err, orcart.ErrNotRunning) {
		t.Fatalf("socket gone: %v", err)
	}
	if got := l.all(); got != "" {
		t.Errorf("logged for an Orca never seen: %q", got)
	}
	if v := c.Version(context.Background()); v != "" {
		t.Errorf("version of a stopped Orca: %q", v)
	}
}

// Orca restarting (new socket, new token) is picked up by reading the metadata again: on a refused connection and on
// unauthorized. The token never shows in the log.
func TestRestartReadsTheMetadataAgain(t *testing.T) {
	f := orcarttest.New(t)
	l := &logs{}
	c := client(f, l)
	if _, _, _, err := c.Worktrees(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := f.Restart("test-token-2"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.Worktrees(context.Background(), ""); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	// The same socket, a new token (the metadata file rewritten in place).
	f.Stop()
	if err := f.Restart("test-token-3"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.Worktrees(context.Background(), ""); err != nil {
		t.Fatalf("after a token change: %v", err)
	}
	for _, tok := range []string{"test-token-1", "test-token-2", "test-token-3"} {
		if strings.Contains(l.all(), tok) {
			t.Errorf("token in the log: %q", l.all())
		}
	}
}

// A wrong token in a metadata file that does not change stays an error (unauthorized), tried once more only.
func TestUnauthorized(t *testing.T) {
	f := orcarttest.New(t)
	b, _ := os.ReadFile(f.MetaPath)
	if err := os.WriteFile(f.MetaPath, []byte(strings.Replace(string(b), f.Token(), "wrong-token", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	c := client(f, nil)
	if _, err := c.Terminals(context.Background(), true); !orcart.IsCode(err, "unauthorized") {
		t.Fatalf("wrong token: %v", err)
	}
}

// A runtime whose protocol range does not include this client's is incompatible: every call fails with a reason.
func TestIncompatible(t *testing.T) {
	for _, st := range []map[string]any{
		{"runtimeProtocolVersion": 2, "minCompatibleRuntimeClientVersion": 1},
		{"runtimeProtocolVersion": 9, "minCompatibleRuntimeClientVersion": 7},
	} {
		f := orcarttest.New(t)
		f.SetStatus(st)
		c := client(f, nil)
		if _, err := c.Terminals(context.Background(), true); !errors.Is(err, orcart.ErrIncompatible) {
			t.Fatalf("%v: %v", st, err)
		}
		if f.Count("terminal.list") != 0 {
			t.Errorf("listed an incompatible runtime")
		}
		s, err := c.Probe(context.Background())
		if !errors.Is(err, orcart.ErrIncompatible) || s == nil || s.AppVersion != "1.4.245" {
			t.Errorf("probe: %+v %v", s, err)
		}
		if v := c.Version(context.Background()); v != "" {
			t.Errorf("version of an incompatible runtime: %q", v)
		}
	}
}

// Sends: raw text without enter, an agent prompt with enter; a send Orca turns down is a Refusal; an error answer is
// an *Error with its code.
func TestSend(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	c := client(f, nil)
	ctx := context.Background()
	tg := orcart.Target{Handle: "term_1", Incarnation: "inc-term_1"}
	if err := c.Send(ctx, tg, "\x1b[Z"); err != nil {
		t.Fatal(err)
	}
	if sent, err := c.Prompt(ctx, tg, "hello"); err != nil || !sent.Prompt {
		t.Fatalf("prompt: %+v %v", sent, err)
	}
	if inv := f.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}
	if w := f.Writes(); len(w) != 2 || w[0].Text != "\x1b[Z" || w[0].Enter || w[0].Pasted || !w[1].Pasted || !w[1].Enter {
		t.Errorf("writes %+v", w)
	}
	want := []string{
		orcarttest.SendCall("term_1", map[string]any{"text": "\x1b[Z"}),
		orcarttest.SendCall("term_1", map[string]any{"text": "hello", "enter": true, "agentPrompt": true}),
	}
	if got := f.Trace("terminal.send"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sends:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	f.Handle("terminal.send", func(map[string]any) any {
		return map[string]any{"send": map[string]any{"handle": "term_1", "accepted": false, "bytesWritten": 0,
			"refusedReason": "permission"}}
	})
	var r *orcart.Refusal
	if _, err := c.Prompt(ctx, tg, "hello"); !errors.As(err, &r) || r.Reason != "permission" ||
		!errors.Is(err, orcart.ErrRefused) {
		t.Errorf("refused: %v", err)
	}
	f.Handle("terminal.send", nil)
	if err := c.Send(ctx, orcart.Target{Handle: "term_gone"}, "x"); !orcart.IsCode(err, "terminal_handle_stale") || !orcart.Refused(err) {
		t.Errorf("stale handle: %v", err)
	}
}

// The screen comes back as text with soft-wrapped rows joined; a screen Orca cannot give is an error.
func TestRead(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	f.SetScreenRows("term_1", []string{"a long li", "ne", "❯ next"}, []bool{true, false, false})
	c := client(f, nil)
	s, err := c.Read(context.Background(), orcart.Target{Handle: "term_1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Text(); got != "a long line\n❯ next\n" || s.Source != "screen" {
		t.Errorf("text %q source %q", got, s.Source)
	}
	if tr := f.Trace("terminal.read"); len(tr) != 1 || tr[0] != `terminal.read {"screen":true,"terminal":"term_1"}` {
		t.Errorf("read: %v", tr)
	}
	f.Handle("terminal.read", func(map[string]any) any {
		return map[string]any{"terminal": map[string]any{"handle": "term_1", "tail": []string{}, "source": "screen-unavailable"}}
	})
	if _, err := c.Read(context.Background(), orcart.Target{Handle: "term_1"}); !errors.Is(err, orcart.ErrNoScreen) {
		t.Errorf("no screen: %v", err)
	}
}

// The output tail is the read without screen or cursor; the log pages by cursor, from the oldest line Orca keeps when
// the cursor is older. An Orca that answers without cursors reads no log.
func TestReadTailAndLog(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	log := make([]string, 0, 2000)
	for i := 501; i <= 2500; i++ {
		log = append(log, fmt.Sprintf("line-%d", i))
	}
	f.SetOutput("term_1", append(slices.Clone(log), "$ "), true, log, 2500)
	c := client(f, nil)
	ctx := context.Background()
	tg := orcart.Target{Handle: "term_1", Incarnation: "inc-term_1"}
	tail, truncated, limited, source, err := c.ReadTail(ctx, tg, 5)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"line-2497", "line-2498", "line-2499", "line-2500", "$ "}; !slices.Equal(tail, want) ||
		!truncated || !limited || source != "stream" {
		t.Errorf("tail = %q truncated %v limited %v source %q", tail, truncated, limited, source)
	}
	lines, oldest, err := c.ReadLog(ctx, tg, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"line-501", "line-502", "line-503"}; !slices.Equal(lines, want) || oldest != 500 {
		t.Errorf("log = %q from %d", lines, oldest)
	}
	if lines, _, err := c.ReadLog(ctx, tg, 1<<53, 10); err != nil || len(lines) != 0 {
		t.Errorf("past the newest: %q, %v", lines, err)
	}
	if got := f.Trace("terminal.read"); !slices.Equal(got, []string{
		`terminal.read {"limit":5,"terminal":"term_1"}`,
		`terminal.read {"cursor":0,"limit":3,"terminal":"term_1"}`,
		`terminal.read {"cursor":9007199254740992,"limit":10,"terminal":"term_1"}`,
	}) {
		t.Errorf("reads: %q", got)
	}
	if inv := f.Invalid(); len(inv) != 0 {
		t.Errorf("invalid requests: %v", inv)
	}
	f.SetOldOutput("term_1", []string{"$ "})
	if _, _, err := c.ReadLog(ctx, tg, 0, 10); !errors.Is(err, orcart.ErrNoCursor) {
		t.Errorf("no cursors: %v", err)
	}
}

// Orca 1.4.277 lifts the text of an agent's input box out of the screen rows into draft, leaving the prompt row as
// its bare glyph (seen live with Claude Code 2.1.331, for a typed draft and for Claude's grey suggestion alike). Text
// puts it back where it is on screen, so the node reads the box as drawn; without a draft the rows stay as sent.
func TestReadPutsTheDraftBack(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	c := client(f, nil)
	rule := strings.Repeat("─", 20)
	read := func() *orcart.Screen {
		t.Helper()
		s, err := c.Read(context.Background(), orcart.Target{Handle: "term_1"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	f.SetBoxScreen("term_1", "● done\n"+rule+"\n❯\n"+rule+"\n  ? for shortcuts", "fix the flaky test")
	if s := read(); s.Draft != "fix the flaky test" ||
		s.Text() != "● done\n"+rule+"\n❯ fix the flaky test\n"+rule+"\n  ? for shortcuts\n" {
		t.Errorf("one line: draft %q text %q", s.Draft, s.Text())
	}
	f.SetBoxScreen("term_1", "❯ an old prompt\n"+rule+"\n❯\n"+rule, "line one\nline two")
	if got := read().Text(); got != "❯ an old prompt\n"+rule+"\n❯ line one\n  line two\n"+rule+"\n" {
		t.Errorf("two lines: %q", got)
	}
	f.SetBoxScreen("term_1", rule+"\n❯\n"+rule, "")
	if s := read(); s.Draft != "" || s.Text() != rule+"\n❯\n"+rule+"\n" {
		t.Errorf("empty box: draft %q text %q", s.Draft, s.Text())
	}
	f.SetBoxScreen("term_1", rule+"\n❯ drawn in the row\n"+rule, "")
	if got := read().Text(); got != rule+"\n❯ drawn in the row\n"+rule+"\n" {
		t.Errorf("older runtime: %q", got)
	}
}

// A wedged Orca (accepts, never answers) costs a call its timeout, not more; the failure is logged once per episode.
func TestTimeoutAndEpisodeLog(t *testing.T) {
	f := orcarttest.New(t)
	l := &logs{}
	c := client(f, l)
	if _, _, _, err := c.Worktrees(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	f.Handle("worktree.ps", func(map[string]any) any { <-block; return nil })
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		start := time.Now()
		_, _, _, err := c.Worktrees(ctx, "")
		cancel()
		if err == nil || time.Since(start) > 2*time.Second {
			t.Fatalf("wedged: %v after %s", err, time.Since(start))
		}
	}
	// A caller giving up is not Orca failing: nothing logged.
	if got := l.all(); got != "" {
		t.Errorf("logged a caller's timeout: %q", got)
	}
}

// A read-only client never sends a method that could change Orca.
func TestReadOnly(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	c := &orcart.Client{MetaPath: f.MetaPath, ReadOnly: true}
	ctx := context.Background()
	if _, err := c.Terminals(ctx, false); err != nil {
		t.Fatal(err)
	}
	tg := orcart.Target{Handle: "term_1"}
	if _, err := c.Read(ctx, tg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentStatus(ctx, tg); err != nil {
		t.Fatal(err)
	}
	f.SetProcess("term_1", orcarttest.ShellAtRest(7))
	if _, err := c.InspectProcess(ctx, tg); err != nil {
		t.Fatal(err)
	}
	_, perr := c.Prompt(ctx, tg, "x")
	for _, err := range []error{c.Send(ctx, tg, "x"), perr, c.Close(ctx, tg)} {
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("read-only client: %v", err)
		}
	}
	if n := f.Count("terminal.send") + f.Count("terminal.close"); n != 0 {
		t.Errorf("%d writes reached Orca", n)
	}
}

// The fake refuses what Orca's schemas refuse: a send naming a client without its id (Persona's first send bug).
func TestFakeValidatesLikeOrca(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	c := client(f, nil)
	if err := c.Call(context.Background(), time.Second, "terminal.send",
		map[string]any{"terminal": "term_1", "text": "x", "client": map[string]any{"type": "desktop"}}, nil); !orcart.IsCode(err, "invalid_argument") {
		t.Errorf("client without id: %v", err)
	}
	if err := c.Call(context.Background(), time.Second, "terminal.read",
		map[string]any{"terminal": "term_1", "screen": 1}, nil); !orcart.IsCode(err, "invalid_argument") {
		t.Errorf("screen not true: %v", err)
	}
	// A fenced call to a terminal that runs another process now is stale.
	if err := c.Send(context.Background(), orcart.Target{Handle: "term_1", Incarnation: "inc-old"}, "x"); !orcart.IsCode(err, "terminal_handle_stale") {
		t.Errorf("old incarnation: %v", err)
	}
}

// worktree.ps after a snapshot id: unchanged when nothing changed, the worktrees again when something did.
func TestWorktreeSnapshots(t *testing.T) {
	f := orcarttest.New(t)
	f.SetWorktrees(orcarttest.Worktree("wt1", "/tmp/p", "main", orcarttest.Agent("a:b", "claude", "working")))
	c := client(f, nil)
	ctx := context.Background()
	trees, id, unchanged, err := c.Worktrees(ctx, "")
	if err != nil || len(trees) != 1 || id == "" || unchanged {
		t.Fatalf("first: %v %q %v %v", trees, id, unchanged, err)
	}
	trees, id2, unchanged, err := c.Worktrees(ctx, id)
	if err != nil || !unchanged || id2 != id || len(trees) != 0 {
		t.Fatalf("again: %v %q %v %v", trees, id2, unchanged, err)
	}
	f.SetWorktrees(orcarttest.Worktree("wt1", "/tmp/p", "main", orcarttest.Agent("a:b", "claude", "done")))
	trees, id3, unchanged, err := c.Worktrees(ctx, id)
	if err != nil || unchanged || id3 == id || len(trees) != 1 || trees[0].Agents[0].State != "done" {
		t.Fatalf("changed: %v %q %v %v", trees, id3, unchanged, err)
	}
	if tr := f.Trace("worktree.ps"); len(tr) != 3 || tr[0] != `worktree.ps {"afterSnapshotId":null}` {
		t.Errorf("calls %v", tr)
	}
}

// A list Orca cut short at its limit says so: terminal.list's Truncated, AllWorktrees' ErrPartial (whole answers only,
// outside a snapshot series).
func TestListsCutShortSaySo(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "refs/heads/main", "x", ""))
	f.SetWorktrees(orcarttest.Worktree("wt1", "/tmp/p", "main"))
	c := client(f, nil)
	ctx := context.Background()
	l, err := c.Terminals(ctx, true)
	if err != nil || l.Truncated || len(l.Terminals) != 1 {
		t.Fatalf("whole terminal list: %+v %v", l, err)
	}
	trees, err := c.AllWorktrees(ctx)
	if err != nil || len(trees) != 1 {
		t.Fatalf("whole worktree list: %v %v", trees, err)
	}
	if tr := f.Trace("worktree.ps"); len(tr) != 1 || tr[0] != `worktree.ps {"afterSnapshotId":null}` {
		t.Errorf("calls %v", tr)
	}
	f.Handle("terminal.list", func(map[string]any) any {
		return map[string]any{"terminals": []any{}, "totalCount": 201, "truncated": true}
	})
	f.Handle("worktree.ps", func(map[string]any) any {
		return map[string]any{"worktrees": []any{}, "totalCount": 201, "truncated": true}
	})
	if l, err := c.Terminals(ctx, false); err != nil || !l.Truncated {
		t.Fatalf("cut terminal list: %+v %v", l, err)
	}
	if _, err := c.AllWorktrees(ctx); !errors.Is(err, orcart.ErrPartial) {
		t.Fatalf("cut worktree list: %v", err)
	}
}

// Tabs come in the layout's reading order: a split's first side, then its second — the same every time.
func TestTabsInLayoutOrder(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tabA", "leaf1", "wt1", "/tmp/p", "", "x", "claude"))
	group := func(ids ...string) map[string]any {
		var tabs []any
		for _, id := range ids {
			tabs = append(tabs, map[string]any{"tabId": id, "title": id, "activeLeafId": nil,
				"panes": map[string]any{"type": "terminal", "handle": "h", "tabId": id, "leafId": "l", "title": nil,
					"connected": true, "active": true}})
		}
		return map[string]any{"type": "group", "groupId": "g", "activeTabId": nil, "tabs": tabs}
	}
	f.Handle("terminal.list", func(map[string]any) any {
		return map[string]any{"terminals": []any{}, "totalCount": 0, "truncated": false, "visualLayouts": []any{
			map[string]any{"worktreeId": "wt1", "worktreePath": "/tmp/p", "root": map[string]any{"type": "split",
				"direction": "horizontal", "first": group("tabA", "tabB"),
				"second": map[string]any{"type": "split", "direction": "vertical", "first": group("tabC"), "second": group("tabD")}}}}}
	})
	c := client(f, nil)
	for range 20 {
		l, err := c.Terminals(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tab := range l.Tabs {
			got = append(got, tab.ID)
		}
		if strings.Join(got, ",") != "tabA,tabB,tabC,tabD" {
			t.Fatalf("tabs %v", got)
		}
	}
}

// A request Orca could not read is answered under the id "unknown": that is this request's refusal, not a hang.
func TestUnknownIDRefusal(t *testing.T) {
	dir := orcarttest.TempDir(t)
	kind, sock := orcarttest.Endpoint(dir)
	ln, err := orcarttest.Listen(kind, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				if strings.Contains(line, `"status.get"`) {
					fmt.Fprintln(conn, `{"id":"j1","ok":true,"result":{"runtimeProtocolVersion":3,"minCompatibleRuntimeClientVersion":2}}`)
					return
				}
				fmt.Fprintln(conn, `{"id":"unknown","ok":false,"error":{"code":"request_too_large","message":"too large"}}`)
			}()
		}
	}()
	meta := filepath.Join(dir, "orca-runtime.json")
	b, _ := json.Marshal(map[string]any{"runtimeId": "rt", "authToken": "t",
		"transports": []any{map[string]any{"kind": kind, "endpoint": sock}}})
	if err := os.WriteFile(meta, b, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &orcart.Client{MetaPath: meta}
	start := time.Now()
	err = c.Send(context.Background(), orcart.Target{Handle: "term_1"}, "x")
	if !orcart.IsCode(err, "request_too_large") || !orcart.Refused(err) || time.Since(start) > 2*time.Second {
		t.Errorf("unknown id: %v after %s", err, time.Since(start))
	}
}

// orcaEnv clears the variables MetaPaths reads (the test may itself run inside Orca) and points the home at a fresh
// directory.
func orcaEnv(t *testing.T) (home string) {
	home = t.TempDir()
	for _, k := range []string{"ORCA_USER_DATA_PATH", "ORCA_USER_DATA", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// The desktop app's file comes first, orcad's (~/.orca, the runtime another computer's Orca starts over SSH) after;
// Orca's own overrides move them.
func TestMetaPathsDesktopThenOrcad(t *testing.T) {
	home := orcaEnv(t)
	paths := orcart.MetaPaths()
	if len(paths) == 0 || paths[len(paths)-1] != filepath.Join(home, ".orca", "orca-runtime.json") {
		t.Fatalf("orcad's file is not last: %v", paths)
	}
	if runtime.GOOS == "linux" && paths[0] != filepath.Join(home, ".config", "orca", "orca-runtime.json") {
		t.Errorf("desktop file on linux: %v", paths)
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	if got := orcart.MetaPaths(); got[len(got)-1] != filepath.Join(data, "Orca", "orca-runtime.json") {
		t.Errorf("XDG_DATA_HOME: %v", got)
	}
	t.Setenv("ORCA_USER_DATA", "/srv/orcad")
	if got := orcart.MetaPaths(); got[len(got)-1] != filepath.Join("/srv/orcad", "orca-runtime.json") {
		t.Errorf("ORCA_USER_DATA: %v", got)
	}
	t.Setenv("ORCA_USER_DATA_PATH", "/srv/desktop")
	if got := orcart.MetaPaths(); len(got) != 1 || got[0] != filepath.Join("/srv/desktop", "orca-runtime.json") {
		t.Errorf("ORCA_USER_DATA_PATH is the only place: %v", got)
	}
}

// Of the files there, the one written last is read (an earlier runtime may have left its own behind); with none, the
// desktop app's path (Orca not running).
func TestMetaPathNewestFile(t *testing.T) {
	orcaEnv(t)
	paths := orcart.MetaPaths()
	if got := orcart.MetaPath(); got != paths[0] {
		t.Errorf("no file: %s, want %s", got, paths[0])
	}
	old := time.Now().Add(-time.Hour)
	for i, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := old
		if i == len(paths)-1 {
			at = time.Now()
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if got := orcart.MetaPath(); got != paths[len(paths)-1] {
		t.Errorf("newest: %s, want %s", got, paths[len(paths)-1])
	}
}

// A client from New finds a runtime that starts after it, in orcad's directory.
func TestNewFindsOrcadRuntimeStartedLater(t *testing.T) {
	home := orcaEnv(t)
	f := orcarttest.New(t)
	f.Stop()
	c := orcart.New()
	if _, err := c.Probe(context.Background()); !errors.Is(err, orcart.ErrNotRunning) {
		t.Fatalf("before Orca runs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".orca"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.MetaPath = filepath.Join(home, ".orca", "orca-runtime.json")
	if err := f.Restart("t2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatalf("after orcad started: %v", err)
	}
}

// terminal.inspectProcess, as Orca 1.4.277 answers it: a shell at rest (no foreground, no children, proved idle, the
// shell's pid in the fence), a program in front, and an unverifiable answer whose foreground is only a guess. The call
// is fenced to the terminal's incarnation; an Orca without it answers method_not_found.
func TestInspectProcess(t *testing.T) {
	f := orcarttest.New(t)
	f.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "wt1", "/tmp/p", "", "x", ""))
	c := &orcart.Client{MetaPath: f.MetaPath}
	ctx := context.Background()
	tg := orcart.Target{Handle: "term_1", Incarnation: "inc-term_1"}
	if _, err := c.InspectProcess(ctx, tg); !orcart.IsCode(err, "method_not_found") {
		t.Fatalf("an Orca without the call: %v", err)
	}
	type process struct {
		foreground     string
		children, idle bool
		shellPID       int
		known          bool
		unverified     string
	}
	for _, tc := range []struct {
		name string
		in   map[string]any
		want process
	}{
		{"shell at rest", orcarttest.ShellAtRest(41), process{idle: true, shellPID: 41, known: true}},
		{"shell with jobs", orcarttest.ShellWithJobs(41), process{shellPID: 41, known: true}},
		{"program", orcarttest.Running("sleep", 41), process{foreground: "sleep", children: true, shellPID: 41, known: true}},
		{"unverifiable", orcarttest.Unverifiable("incarnation_mismatch"),
			process{foreground: "bash", unverified: "incarnation_mismatch"}},
		{"no evidence", map[string]any{"foregroundProcess": nil, "hasChildProcesses": true, "childProcessEvidence": "unverifiable"},
			process{children: true, unverified: "unverifiable"}},
		{"evidence without a verdict", map[string]any{"foregroundProcess": nil, "hasChildProcesses": false,
			"foregroundProcessEvidence": map[string]any{}}, process{unverified: "unverifiable"}},
	} {
		f.SetProcess("term_1", tc.in)
		p, err := c.InspectProcess(ctx, tg)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		got := process{p.Foreground(), p.HasChildren(), p.IdleShell(), p.ShellPID(), p.Known(), p.Unverified()}
		if got != tc.want {
			t.Errorf("%s: %+v; want %+v", tc.name, got, tc.want)
		}
	}
	for _, p := range f.Params("terminal.inspectProcess") {
		if p["terminal"] != "term_1" || p["expectedIncarnationId"] != "inc-term_1" {
			t.Errorf("params %v", p)
		}
	}
	if inv := f.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}
}
