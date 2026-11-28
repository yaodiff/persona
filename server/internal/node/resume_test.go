package node

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
)

func TestInteractionIDStableAcrossAttempts(t *testing.T) {
	in := hookInput{SessionID: "s1", HookEventName: "PermissionRequest", ToolName: "Bash", ToolUseID: "toolu_1"}
	if a, b := interactionID(in, "x"), interactionID(in, "y"); a != b {
		t.Fatalf("a tool call keeps its id: %s %s", a, b)
	}
	pre := in
	pre.HookEventName = "PreToolUse"
	if interactionID(pre, "x") == interactionID(in, "x") {
		t.Fatal("two hook events of one tool call are two interactions")
	}
	in.ToolUseID = ""
	if a, b := interactionID(in, "x"), interactionID(in, "x"); a != b {
		t.Fatalf("without a tool use id the attempt decides: %s %s", a, b)
	}
	if interactionID(in, "x") == interactionID(in, "y") {
		t.Fatal("two hook processes are two interactions")
	}
	if interactionID(in, "") == interactionID(in, "") {
		t.Fatal("an older hook (no attempt) gets a random id")
	}
}

// fakeNodes serves the node socket: each connection gets the next behaviour (nil: close without answering, as a
// node killed mid-wait does).
func fakeNodes(t *testing.T, replies ...map[string]any) chan sockRequest {
	t.Helper()
	home, err := os.MkdirTemp("", "jh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	_ = os.MkdirAll(filepath.Join(home, ".persona"), 0o700)
	ln, err := net.Listen("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan sockRequest, len(replies)+1)
	go func() {
		for _, reply := range replies {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req sockRequest
			_ = json.Unmarshal(line, &req)
			got <- req
			if reply != nil {
				b, _ := json.Marshal(reply)
				_, _ = c.Write(append(b, '\n'))
			}
			c.Close()
		}
		ln.Close() // no node after these: connecting fails, as with a dead node's socket file
	}()
	return got
}

func TestHookResumesOnNextNode(t *testing.T) {
	defer func(d time.Duration) { nodeclient.ResumeRetry = d }(nodeclient.ResumeRetry)
	nodeclient.ResumeRetry = 10 * time.Millisecond
	got := fakeNodes(t,
		nil,                           // the node dies while the phone decides
		map[string]any{"retry": true}, // the next one is shutting down too
		map[string]any{"output": map[string]any{"ok": 1}},
	)
	out := nodeclient.Hook(context.Background(), json.RawMessage(`{"session_id":"s1"}`), "w1:p1")
	if string(out) != `{"ok":1}` {
		t.Fatalf("output %s", out)
	}
	first, second, third := <-got, <-got, <-got
	if first.Attempt == "" || first.Attempt != second.Attempt || second.Attempt != third.Attempt {
		t.Fatalf("attempts %q %q %q", first.Attempt, second.Attempt, third.Attempt)
	}
	if first.Resume || !second.Resume || !third.Resume {
		t.Fatal("only reconnects are resumes")
	}
	occurrence := func(req sockRequest) string {
		raw, _ := json.Marshal(req)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		at, _ := fields["created_at"].(string)
		return at
	}
	firstAt := occurrence(first)
	if firstAt == "" || occurrence(second) != firstAt || occurrence(third) != firstAt {
		t.Fatalf("occurrence timestamps %q %q %q, want one nonzero timestamp preserved across reconnects", firstAt, occurrence(second), occurrence(third))
	}
	if _, err := time.Parse(time.RFC3339Nano, firstAt); err != nil {
		t.Fatalf("occurrence timestamp %q: %v", firstAt, err)
	}
}

func TestHookWithoutNodeLeavesItToTheTUI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	start := time.Now()
	if out := nodeclient.Hook(context.Background(), json.RawMessage(`{}`), ""); out != nil {
		t.Fatalf("output %s", out)
	}
	if time.Since(start) > time.Second {
		t.Fatal("no node: no waiting")
	}
}

func TestHookGivesUpAfterResumeWait(t *testing.T) {
	defer func(d, w time.Duration) { nodeclient.ResumeRetry, nodeclient.ResumeWait = d, w }(nodeclient.ResumeRetry, nodeclient.ResumeWait)
	nodeclient.ResumeRetry, nodeclient.ResumeWait = 10*time.Millisecond, 50*time.Millisecond
	fakeNodes(t, nil) // dies, and no node comes back
	if out := nodeclient.Hook(context.Background(), json.RawMessage(`{}`), ""); out != nil {
		t.Fatalf("output %s", out)
	}
}

func TestHookNullOutput(t *testing.T) {
	fakeNodes(t, map[string]any{"output": nil})
	if out := nodeclient.Hook(context.Background(), json.RawMessage(`{}`), ""); out != nil {
		t.Fatalf("output %s", out)
	}
}
