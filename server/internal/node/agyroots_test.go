package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubAgyPaneRoots answers the pane scan with roots and counts the panes it was asked about.
func stubAgyPaneRoots(t *testing.T, roots map[string][]string) *[]string {
	t.Helper()
	var asked []string
	prev := agyPaneRoots
	agyPaneRoots = func(_ *Node, _ context.Context, pane string) []string {
		asked = append(asked, pane)
		return roots[pane]
	}
	t.Cleanup(func() { agyPaneRoots = prev })
	return &asked
}

func stubAgyRootsClock(t *testing.T, at *time.Time) {
	t.Helper()
	prev := agyRootsNow
	agyRootsNow = func() time.Time { return *at }
	t.Cleanup(func() { agyRootsNow = prev })
}

const agyRootsUser = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Hello from another account"}` + "\n" +
	`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Answered there."}` + "\n"

// A conversation in the default data directory is read there, and no pane process is read for it (#17097).
func TestAgyRootForDefaultNeedsNoScan(t *testing.T) {
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	agyTestFile(t, n.agyDataRoot(), agyTestID, agyRootsUser)
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))
	asked := stubAgyPaneRoots(t, nil)

	if got := n.agyRootFor(agyTestID); got != n.agyDataRoot() {
		t.Fatalf("agyRootFor = %q, want the default %q", got, n.agyDataRoot())
	}
	if len(*asked) != 0 {
		t.Fatalf("a conversation in the default directory read pane processes: %v", *asked)
	}
}

// An agy started by a launcher that gives each account its own HOME keeps its conversation under that HOME: the node
// finds the directory through the pane's processes and shows the conversation on the Agent page (#17097).
func TestAgyRootForAnotherAccountsDirectory(t *testing.T) {
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	other := filepath.Join(t.TempDir(), "account-2", ".gemini", "antigravity-cli")
	agyTestFile(t, other, agyTestID, agyRootsUser)
	// The stubs go in before the agent is seeded: listing the sessions already binds the pane, which scans.
	asked := stubAgyPaneRoots(t, map[string][]string{agyTestPane: {other}})
	at := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)
	stubAgyRootsClock(t, &at)
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))

	if got := n.agyRootFor(agyTestID); got != other {
		t.Fatalf("agyRootFor = %q, want the account's directory %q", got, other)
	}
	if strings.Join(*asked, ",") != agyTestPane {
		t.Fatalf("panes read: %v, want only the pane whose conversation was not found", *asked)
	}
	if got := n.agyRootKnownFor(agyTestID); got != other {
		t.Fatalf("agyRootKnownFor after the scan = %q, want %q", got, other)
	}

	items, _ := agyTestItems(t, n, agyTestRow, nil)
	var texts []string
	for _, it := range items {
		texts = append(texts, it.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "Hello from another account") || !strings.Contains(joined, "Answered there.") {
		t.Fatalf("the Agent page does not show the other directory's transcript: %q", joined)
	}
	if items[0].Role != "user" {
		t.Fatalf("first item %+v, want the user's message", items[0])
	}
}

// A conversation found nowhere asks the panes again only after agyRootsRescan, and a directory without brain/ is not
// taken (#17097).
func TestAgyRootsRescanLimit(t *testing.T) {
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	notAgy := t.TempDir()
	// The stubs go in before the agent is seeded: listing the sessions already binds the pane, which scans.
	asked := stubAgyPaneRoots(t, map[string][]string{agyTestPane: {notAgy, "relative/path"}})
	at := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)
	stubAgyRootsClock(t, &at)
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))

	if got := n.agyRootFor(agyTestID); got != n.agyDataRoot() {
		t.Fatalf("a conversation found nowhere: %q, want the default", got)
	}
	n.agyRootFor(agyTestID)
	if len(*asked) != 1 {
		t.Fatalf("panes read %d times within the rescan limit, want 1", len(*asked))
	}
	at = at.Add(agyRootsRescan)
	n.agyRootFor(agyTestID)
	if len(*asked) != 2 {
		t.Fatalf("panes read %d times after the limit, want 2", len(*asked))
	}
	n.agyRoots.mu.Lock()
	extra := n.agyRoots.extra
	n.agyRoots.mu.Unlock()
	if len(extra) != 0 {
		t.Fatalf("directories without brain/ or not absolute were kept: %v", extra)
	}
}

// The environment names the directory: ANTIGRAVITY_CLI_CONFIG_DIR first, else HOME's default (#17097).
func TestAgyRootOfEnvironment(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{"PATH=/bin\x00HOME=/home/u/profiles/a2\x00", "/home/u/profiles/a2/.gemini/antigravity-cli"},
		{"HOME=/home/u\x00ANTIGRAVITY_CLI_CONFIG_DIR=/data/agy-2\x00", "/data/agy-2"},
		{"PATH=/bin\x00", ""},
	} {
		if got := agyRootOfEnv([]byte(c.env)); got != c.want {
			t.Errorf("agyRootOfEnv(%q) = %q, want %q", c.env, got, c.want)
		}
	}
}
