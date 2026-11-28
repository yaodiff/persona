package node

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// shortAllowWatch makes the reads after an allow come at once and often, for a test.
func shortAllowWatch(t *testing.T) {
	t.Helper()
	settle, every, watch := allowSettle, allowFollowEvery, allowWatchFor
	allowSettle, allowFollowEvery, allowWatchFor = 0, 20*time.Millisecond, 5*time.Second
	t.Cleanup(func() { allowSettle, allowFollowEvery, allowWatchFor = settle, every, watch })
}

func sessionRow(t *testing.T, n *Node, id string) proto.Session {
	t.Helper()
	for _, s := range n.sessionList(context.Background(), false) {
		if s.SessionID == id {
			return s
		}
	}
	t.Fatal("no row")
	return proto.Session{}
}

func waitSession(t *testing.T, n *Node, id, status, reason string) {
	t.Helper()
	waitWithin(t, 3*time.Second, status+" "+strconv.Quote(reason), func() bool {
		s := sessionRow(t, n, id)
		return s.Status == status && s.AttentionReason == reason
	})
}

// #3752: Claude Code 2.1.322 runs the PermissionRequest hooks for its own safety checks but ignores an allow for them.
// The phone allowed a "Dangerous rm operation", the card was over, herdr still called the agent working, and the
// dialog held its terminal with nothing on the phone to answer it. After the hook is handed an allow, a dialog still on
// the pane lists the session as blocked by it (the manager then makes the screen card), until the dialog is gone.
func TestAnAllowClaudeDoesNotTakeListsItsDialog(t *testing.T) {
	shortAllowWatch(t)
	const id = "0d000000-0000-4000-8000-000000003752"
	f := &fakePane{screens: []string{screenFixture(t, "claude-2.1.316-danger-rm-120.txt"),
		screenFixture(t, "claude-2.1.312-empty-162.txt")}}
	n := codexTestNode(t)
	fh := withFakeHub(t, n) // its answer: allow
	n.hosts = herdrHosts(f.serve(t))
	n.panes = map[string]string{}
	n.agents = map[string]host.Agent{id: {Agent: "claude", PaneID: "w1:p1", Cwd: "/work/repo", AgentStatus: proto.StatusWorking}}

	raw, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: id, ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"rm -f $LAB_SCRATCH/*","description":"Delete all files directly inside the lab scratch directory"}`)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1", Pane: "w1:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(out); !strings.Contains(string(b), `"behavior":"allow"`) || len(fh.interactions()) != 1 {
		t.Fatalf("the hook was not handed the card's allow: %s", b)
	}
	// Claude kept its dialog: blocked by it, whatever herdr says.
	waitSession(t, n, id, proto.StatusBlocked, "Bash command")

	// Keys went into the pane (the screen card's Yes): what was read is dropped at once, the pane still followed.
	n.forgetDialog("w1:p1")
	if got := n.allowDialog(id); got != "" {
		t.Fatalf("kept %q after keys went in", got)
	}
	f.mu.Lock()
	f.state = 1 // answered: the dialog is gone
	f.mu.Unlock()
	waitSession(t, n, id, proto.StatusWorking, "")
	waitFor(t, "the watch to end", func() bool {
		n.allowed.mu.Lock()
		defer n.allowed.mu.Unlock()
		return len(n.allowed.m) == 0
	})
}

// An allow Claude takes leaves no dialog: the row keeps herdr's state and the pane is read once.
func TestAnAllowClaudeTakesChangesNothing(t *testing.T) {
	shortAllowWatch(t)
	const id = "0d000000-0000-4000-8000-000000003753"
	f := &fakePane{screens: []string{screenFixture(t, "claude-2.1.312-empty-162.txt")}}
	n, _ := testNode(t)
	n.hosts = herdrHosts(f.serve(t))
	n.agents = map[string]host.Agent{id: {Agent: "claude", PaneID: "w1:p1", Cwd: "/work/repo", AgentStatus: proto.StatusWorking}}
	n.watchAllow(id, "w1:p1")
	waitFor(t, "the watch to end", func() bool {
		n.allowed.mu.Lock()
		defer n.allowed.mu.Unlock()
		return len(n.allowed.m) == 0
	})
	if s := sessionRow(t, n, id); s.Status != proto.StatusWorking || s.AttentionReason != "" {
		t.Fatalf("%s %q", s.Status, s.AttentionReason)
	}
	f.mu.Lock()
	reads := len(f.readLines)
	f.mu.Unlock()
	if reads != 2 {
		t.Fatalf("read the pane %d times", reads)
	}
}

func TestAllowWatchFindsDialogAfterReadFailureAndBlankScreen(t *testing.T) {
	shortAllowWatch(t)
	const id = "0d000000-0000-4000-8000-000000003755"
	f := &fakePane{readFailures: 1, clearAfter: 1, screens: []string{
		"", screenFixture(t, "claude-2.1.316-danger-rm-120.txt"),
	}}
	n, _ := testNode(t)
	n.hosts = herdrHosts(f.serve(t))
	n.agents = map[string]host.Agent{id: {Agent: "claude", PaneID: "w1:p1", Cwd: "/work/repo", AgentStatus: proto.StatusWorking}}
	n.watchAllow(id, "w1:p1")
	waitSession(t, n, id, proto.StatusBlocked, "Bash command")
	f.mu.Lock()
	f.screens[1] = ""
	f.mu.Unlock()
	waitSession(t, n, id, proto.StatusWorking, "")
}

// A newer allow on the same session takes over from the one before; the watch never outlives allowWatchFor.
func TestAllowWatchEnds(t *testing.T) {
	shortAllowWatch(t)
	allowWatchFor = 100 * time.Millisecond
	const id = "0d000000-0000-4000-8000-000000003754"
	f := &fakePane{screens: []string{screenFixture(t, "claude-2.1.316-danger-rm-120.txt")}} // stays on screen
	n, _ := testNode(t)
	n.hosts = herdrHosts(f.serve(t))
	n.agents = map[string]host.Agent{id: {Agent: "claude", PaneID: "w1:p1", Cwd: "/work/repo", AgentStatus: proto.StatusWorking}}
	n.watchAllow(id, "w1:p1")
	n.watchAllow(id, "w1:p1")
	waitSession(t, n, id, proto.StatusBlocked, "Bash command")
	waitFor(t, "the watch to end", func() bool { return n.allowDialog(id) == "" })
	if s := sessionRow(t, n, id); s.Status != proto.StatusWorking {
		t.Fatalf("still %s after the watch", s.Status)
	}
}
