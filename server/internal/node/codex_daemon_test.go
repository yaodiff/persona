package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// A Codex pane on a shared app-server (docs/integrations/codex.md "Sessions on the daemon"): joined on its own, then its messages, async
// answers and approvals go through the protocol; against the synthetic app-server of codex_remote_test.go.

// daemonPane is a node with a live Codex pane whose thread (remoteRaw) a connected app-server runs.
func daemonPane(t *testing.T) (*Node, *remoteFixture, *codexRemote, *fakeHub) {
	t.Helper()
	n := remoteTestNode(t)
	fh := withFakeHub(t, n)
	home := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home) // the default home: thread ids stay as they are
	f, c := connectRemoteFixture(t, n, home, "daemon", remoteRaw)
	if id := n.registerCodex(home, remoteRaw, ""); id != remoteRaw {
		t.Fatalf("registered as %q", id)
	}
	n.mu.Lock()
	n.agents[remoteRaw] = host.Agent{Agent: "codex", PaneID: "w1:p1", Cwd: home, AgentStatus: proto.StatusIdle}
	n.mu.Unlock()
	return n, f, c, fh
}

func (f *remoteFixture) sent(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, m := range f.got {
		if m.Method == method {
			var p map[string]any
			_ = json.Unmarshal(m.Params, &p)
			out = append(out, p)
		}
	}
	return out
}

func inputText(p map[string]any) string {
	in, _ := p["input"].([]any)
	if len(in) == 0 {
		return ""
	}
	m, _ := in[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}

func TestCodexDaemonPaneJoinsAndSendsThroughTheProtocol(t *testing.T) {
	n, f, _, _ := daemonPane(t)
	if route, note := n.codexRoute(remoteRaw); route != codexRouteTerminal || note == "" {
		t.Fatalf("before joining: %q %q", route, note)
	}
	n.joinCodexPanes(context.Background())
	waitFor(t, "joined", func() bool { route, _ := n.codexRoute(remoteRaw); return route == codexRouteAppServer })
	resume := f.sent("thread/resume")
	if len(resume) != 1 || resume[0]["threadId"] != remoteRaw {
		t.Fatalf("resume %v", resume)
	}
	for k := range resume[0] {
		if k != "threadId" && k != "excludeTurns" {
			t.Errorf("join overrides the session: %s", k) // no model, cwd, sandbox, instructions
		}
	}
	n.joinCodexPanes(context.Background()) // joined: not again
	time.Sleep(50 * time.Millisecond)
	if f.count("thread/resume") != 1 {
		t.Errorf("joined twice")
	}
	// A message is a turn on the app-server: nothing goes near the terminal (this node has no host to type into).
	if err := n.send(context.Background(), remoteRaw, "hello from the phone", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); len(starts) != 1 || inputText(starts[0]) != "hello from the phone" {
		t.Fatalf("turn/start %v", starts)
	}
}

func TestCodexDaemonJoinsActiveThreadsOutsidePanes(t *testing.T) {
	n := remoteTestNode(t)
	withFakeHub(t, n)
	_, first := connectRemoteFixture(t, n, t.TempDir(), "first", remoteRaw)
	f, second := connectRemoteFixture(t, n, t.TempDir(), "second", remoteRaw, remoteOther)
	first.message(codexMessage{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"` + remoteRaw + `","status":{"type":"active"}}`)})
	external := threadOf(first, remoteRaw).publicID
	second.mu.Lock()
	child := second.threads[remoteRaw]
	child.status, child.parent = proto.StatusBlocked, remoteOther
	second.threads[remoteRaw] = child
	second.mu.Unlock()
	f.mu.Lock()
	f.resumeRequest = true
	f.mu.Unlock()
	n.joinCodexPanes(context.Background())
	waitFor(t, "external thread subscribed and child approval replayed", func() bool {
		return n.codexJoined(external) && f.replyCount() == 1
	})
	if n.codexJoined(threadOf(second, remoteOther).publicID) || len(n.codexInventory().Joined) != 0 {
		t.Fatal("automatic join subscribed to the idle thread or persisted a join")
	}
	if err := n.attachCodex(context.Background(), child.publicID); err == nil {
		t.Fatal("explicit join of a subagent succeeded")
	}
}

// A join the app-server refuses leaves the pane on the terminal, with the reason, and is tried again only later.
func TestCodexDaemonJoinFailure(t *testing.T) {
	n, f, _, _ := daemonPane(t)
	f.mu.Lock()
	f.reject = "thread/resume"
	f.mu.Unlock()
	n.joinCodexPanes(context.Background())
	waitFor(t, "the failure noted", func() bool {
		_, note := n.codexRoute(remoteRaw)
		return strings.Contains(note, "接入 app-server 失败")
	})
	n.joinCodexPanes(context.Background())
	time.Sleep(50 * time.Millisecond)
	if f.count("thread/resume") != 1 {
		t.Errorf("retried at once: %d", f.count("thread/resume"))
	}
}

// Approvals come as Codex cards with the reason and what "always" allows; a refusal with words declines, and the words go
// into the turn after.
func TestCodexDaemonApprovalCardsAndRefusal(t *testing.T) {
	n, f, _, fh := daemonPane(t)
	no := false
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Allow: &no, Message: "use the cache"}
	}
	fh.mu.Unlock()
	n.joinCodexPanes(context.Background())
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	f.push(map[string]any{"id": 9, "method": "item/commandExecution/requestApproval", "params": map[string]any{
		"threadId": remoteRaw, "itemId": "cmd-9", "command": "/bin/zsh -lc 'curl -sI https://example.com'", "reason": "needs network"}})
	waitFor(t, "the card", func() bool { return len(fh.interactions()) == 1 })
	ix := fh.interactions()[0]
	if ix.Agent != "codex" || ix.ToolName != "Bash" || ix.Summary != "curl -sI https://example.com" || ix.Reason != "needs network" ||
		ix.AlwaysLabel != "本会话不再问这个命令" {
		t.Errorf("card %+v", ix)
	}
	waitFor(t, "declined", func() bool { return f.replyCount() == 1 })
	f.mu.Lock()
	var decision struct {
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(f.replies[0].Result, &decision)
	f.mu.Unlock()
	if decision.Decision != "decline" {
		t.Errorf("decision %q", decision.Decision)
	}
	waitFor(t, "the reason passed on", func() bool { return len(f.sent("turn/start")) == 1 })
	if text := inputText(f.sent("turn/start")[0]); !strings.Contains(text, "use the cache") {
		t.Errorf("reason sent as %q", text)
	}
}

// An async question of a joined thread is answered with the reply envelope as a message on the app-server.
func TestCodexDaemonAsyncAnswer(t *testing.T) {
	n, f, _, _ := daemonPane(t)
	old := codexPoll
	codexPoll = 10 * time.Millisecond
	t.Cleanup(func() { codexPoll = old })
	day := time.Now()
	dir := filepath.Join(n.home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-02T10-00-00-"+remoteRaw+".jsonl")
	meta := strings.Replace(cxMeta, cxID, remoteRaw, 1)
	if err := os.WriteFile(path, []byte(cxToday(meta+"\n"+cxAsyncCall)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.registerCodex(filepath.Join(n.home, ".codex"), remoteRaw, path)
	n.joinCodexPanes(context.Background())
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	cards := n.codexCardsNow(context.Background(), "")
	if len(cards) != 1 || !cards[0].ask.Async {
		t.Fatalf("cards %+v", cards)
	}
	go func() { // the app-server records the reply as the user's message
		for i := 0; i < 200 && len(f.sent("turn/start")) == 0; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if s := f.sent("turn/start"); len(s) > 0 {
			appendLine(t, path, cxUserLine(inputText(s[0])))
		}
	}()
	err := n.codexAnswer(context.Background(), remoteRaw, cards[0].ix.ID, proto.Resolution{Answers: map[string]string{"Which color should the banner be?": "Blue"}})
	if err != nil {
		t.Fatal(err)
	}
	want := transcript.CodexReplyMessage([]transcript.CodexReply{{QuestionItemID: transcript.QuestionItemID("call_a1", 0), Question: "Which color should the banner be?", Answer: "Blue"}})
	if got := inputText(f.sent("turn/start")[0]); got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
}

// 0.163's control sockets are aliases: only the target Codex itself computes is followed.
func TestCodexSocketPathAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // as Codex makes app-server-control/ (a umask may leave it group-writable)
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "app-server-control.sock")
	parent, _ := filepath.EvalSymlinks(dir)
	tmp, _ := filepath.EvalSymlinks("/tmp")
	sum := sha256.Sum256([]byte(filepath.Join(parent, "app-server-control.sock")))
	want := filepath.Join(tmp, "codex-daemon-"+strconv.Itoa(os.Geteuid()), hex.EncodeToString(sum[:]))
	if err := os.Symlink(want, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := codexSocketPath(alias); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling alias: %v", err)
	}
	if err := os.Mkdir(filepath.Dir(want), 0o700); err == nil {
		t.Cleanup(func() { _ = os.Remove(filepath.Dir(want)) })
	} else if !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(want) })
	if got, err := codexSocketPath(alias); err != nil || got != want {
		t.Errorf("alias: %q %v, want %q", got, err, want)
	}
	other := filepath.Join(dir, "other.sock")
	if err := os.Symlink(filepath.Join(tmp, "somewhere-else"), other); err != nil {
		t.Fatal(err)
	}
	if _, err := codexSocketPath(other); err == nil {
		t.Error("a symlink to anywhere else was followed")
	}
	plain := filepath.Join(dir, "plain.sock")
	if got, err := codexSocketPath(plain); err != nil || got != plain {
		t.Errorf("plain: %q %v", got, err)
	}
}
