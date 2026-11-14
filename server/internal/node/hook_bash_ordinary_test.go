package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeAgentTranscript writes an ordinary agent's transcript of lines user / assistant messages of about 1 KB each.
func writeAgentTranscript(t testing.TB, dir string, lines int) string {
	t.Helper()
	path := filepath.Join(dir, "projects", "-work-repo", decidedSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("some words of an answer ", 40)
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		if i%2 == 0 {
			fmt.Fprintf(&sb, `{"type":"user","sessionId":%q,"cwd":"/work/repo","message":{"role":"user","content":"question %d %s"}}`+"\n", decidedSession, i, text)
		} else {
			fmt.Fprintf(&sb, `{"type":"assistant","sessionId":%q,"cwd":"/work/repo","message":{"role":"assistant","content":[{"type":"text","text":"answer %d %s"}]}}`+"\n", decidedSession, i, text)
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bashPreToolUse(path string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": decidedSession, "tool_name": "Bash", "cwd": "/work/repo",
		"transcript_path": path, "tool_input": map[string]any{"command": "go test ./..."},
	})
	return raw
}

// Every Bash call of every agent waits on its PreToolUse hook, and only the assistant's are gated: an ordinary agent's
// hook answers before it looks up a card's title (a re-read of the transcript's tail) or watches its claude (#2474).
func TestHookBashOfAnOrdinaryAgentSkipsTheCardWork(t *testing.T) {
	n := codexTestNode(t)
	withCancelHub(t, n)
	var watched atomic.Int32
	oldGone := agentGone
	agentGone = func(int, string) bool { watched.Add(1); return false }
	t.Cleanup(func() { agentGone = oldGone })
	path := writeAgentTranscript(t, t.TempDir(), 4)

	out, err := n.handleHook(context.Background(), sockRequest{Kind: "hook", Input: bashPreToolUse(path), Attempt: "a1", AgentPID: 4242, AgentStart: "linux:7"})
	if out != nil || err != nil {
		t.Fatalf("an ordinary agent's Bash got a decision: %v, %v", out, err)
	}
	n.mu.Lock()
	_, read := n.metas[path]
	n.mu.Unlock()
	if read {
		t.Fatal("read the transcript's meta for a card that is never shown")
	}
	if watched.Load() != 0 {
		t.Fatal("watched the claude of a hook that never waits")
	}
}

// BenchmarkHookBashOrdinaryAgent: an ordinary agent's Bash PreToolUse whose transcript's meta was not read within
// metaMinInterval (a command that ran longer, or the first one).
func BenchmarkHookBashOrdinaryAgent(b *testing.B) {
	n := codexTestNode(b)
	withCancelHub(b, n)
	path := writeAgentTranscript(b, b.TempDir(), 3000)
	raw := bashPreToolUse(path)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.mu.Lock()
		delete(n.metas, path)
		n.mu.Unlock()
		if out, _ := n.handleHook(context.Background(), sockRequest{Kind: "hook", Input: raw, Attempt: "a1", AgentPID: os.Getpid(), AgentStart: "x"}); out != nil {
			b.Fatal(out)
		}
	}
}
