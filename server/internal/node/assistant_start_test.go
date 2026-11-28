package node

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
)

// fakeClaudeHome puts a fake claude on PATH, run through the interactive zsh the assistant starts in.
func fakeClaudeHome(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	fakeClaudeHomeFiles(t, script)
}

// fakeClaudeHomeBash uses the assistant's production bash fallback for a regression that must execute without zsh.
func fakeClaudeHomeBash(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("no bash: %v", err)
	}
	fakeClaudeHomeFiles(t, script)
	// Only here: the launcher profile probe dates every shell file it reads (assistant_profile_probe.go), and the
	// zsh fixtures settle only ~/.zshrc, so a fresh ~/.bashrc beside it would read as a profile still being edited.
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), ".bashrc"), nil, 0o600)
}

func fakeClaudeHomeFiles(t *testing.T, script string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".zshrc"), nil, 0o600)
	// The fake claude is meant to work, so mark its first-run setup finished: the launcher check's onboarding gate
	// (claudeonboarding.go) otherwise stops before it ever runs.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	// The launcher assertions are about what the launcher declares, not what the developer's own shell exports (a
	// gateway of its own included: tool search follows it, #10365, and resumeModelOf follows claudeModelVars).
	for _, k := range append([]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "ENABLE_TOOL_SEARCH", "ANTHROPIC_BASE_URL"}, claudeModelVars...) {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// Nor the machine's managed Claude Code policy (claudeEndpoint and resumeModelOf read it first).
	managed := claudeManagedSettingsFiles
	managedPaths, managedDirs := claudeManagedSettings, claudeManagedSettingsDirs
	claudeManagedSettingsFiles = func() []string { return nil }
	claudeManagedSettings, claudeManagedSettingsDirs = nil, nil
	t.Cleanup(func() {
		claudeManagedSettingsFiles = managed
		claudeManagedSettings, claudeManagedSettingsDirs = managedPaths, managedDirs
	})
}

// A launcher that exits before reading the message (its node socket gone: "no persona node") lost it: the send says
// so with what it printed, instead of answering ok and leaving the message at "Delivering" for good.
func TestAssistantSendFailsWhenTheLauncherDoesNotStart(t *testing.T) {
	fakeClaudeHome(t, "printf '\\033[31mERROR\\033[0m: gitstatus failed\\n\\n' >&2\necho 'personad launch opus: no persona node: dial unix node.sock: connect: connection refused' >&2\nexit 1\n")
	n := codexTestNode(t)
	withFakeHub(t, n)
	_, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "")
	if err == nil {
		t.Fatal("a launcher that did not start answered ok")
	}
	if msg := err.Error(); !strings.Contains(msg, "没有启动起来") || !strings.Contains(msg, "no persona node") || strings.Contains(msg, "\x1b") {
		t.Fatalf("error: %q", msg)
	}
	n.mu.Lock()
	left := len(n.procs)
	n.mu.Unlock()
	if left != 0 {
		t.Fatal("the dead process is still kept")
	}
}

// One that starts answers as before, as soon as its first line comes out.
func TestAssistantSendReturnsOnceTheProcessStarted(t *testing.T) {
	fakeClaudeHome(t, `case " $* " in *" auth status "*) echo '{"loggedIn":true}'; exit 0 ;; esac
read line
echo '{"type":"system","subtype":"init"}'
sleep 30
`)
	n := codexTestNode(t)
	withFakeHub(t, n)
	start := time.Now()
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= assistantStartWait {
		t.Fatalf("waited the whole start wait (%v) for a process that started", time.Since(start))
	}
	n.stopAssistant(id)
}

// The node socket replaced by a file nothing answers on is re-created; one a newer node listens on is left alone.
func TestSockGoneTellsADeadReplacementFromALiveOne(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ps") // a unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	clearPersonaHome(t)
	if err := os.MkdirAll(filepath.Dir(nodeclient.SocketPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Node{}
	if _, err := n.listenOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if why := n.sockGone(); why != "" {
		t.Fatalf("its own socket: %q", why)
	}

	// Another process listens on the path and exits without unlinking.
	_ = os.Remove(nodeclient.SocketPath())
	other, err := net.Listen("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	other.(*net.UnixListener).SetUnlinkOnClose(false)
	other.Close()
	if why := n.sockGone(); !strings.Contains(why, "nothing answers") {
		t.Fatalf("a dead replacement: %q", why)
	}
	if _, err := n.listenOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if c, err := net.Dial("unix", nodeclient.SocketPath()); err != nil {
		t.Fatalf("not re-created: %v", err)
	} else {
		c.Close()
	}

	// A newer node's live socket stays its own.
	_ = os.Remove(nodeclient.SocketPath())
	newer, err := net.Listen("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	go func() {
		for {
			c, err := newer.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if why := n.sockGone(); why != "" {
		t.Fatalf("a live newer socket: %q", why)
	}

	_ = os.Remove(nodeclient.SocketPath())
	if why := n.sockGone(); why != "vanished" {
		t.Fatalf("a removed socket: %q", why)
	}
}

// A conversation's process read its directory's CLAUDE.md when it started: once the hub writes a new one (new prompts
// after an upgrade), the next message resumes it in a fresh process; while it is unchanged the process is kept.
func TestAssistantSendRestartsForNewInstructions(t *testing.T) {
	fakeClaudeHome(t, "echo '{\"type\":\"system\",\"subtype\":\"init\"}'\nwhile read line; do echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\"}'; done\n")
	n := codexTestNode(t)
	withFakeHub(t, n)
	cwd := t.TempDir()
	md := filepath.Join(cwd, "CLAUDE.md")
	if err := os.WriteFile(md, []byte("stacked_bar"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := n.assistantSend(ctx, "", cwd, "claude", "hello", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { n.stopAssistant(id) }()
	proc := func() *assistantProc {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.procs[id]
	}
	idle := func(p *assistantProc) {
		t.Helper()
		waitFor(t, "the turn to end", func() bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			return !p.busy
		})
	}
	first := proc()
	idle(first)
	if _, err := n.assistantSend(ctx, id, "", "", "again", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if proc() != first {
		t.Fatal("restarted with the same CLAUDE.md")
	}
	idle(first)
	if err := os.WriteFile(md, []byte("ring"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.assistantSend(ctx, id, "", "", "today's calories", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if p := proc(); p == first || p == nil || p.instructions != instructionsDigest(cwd) {
		t.Fatal("kept the process that read the old CLAUDE.md")
	}
}
