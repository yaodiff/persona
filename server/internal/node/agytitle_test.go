package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// agyTestMetadata writes agy's conversation metadata (cache/conversation_metadata.json) under root, in the shape agy
// writes it, with one summary per conversation titled as titles says, and dates it at.
func agyTestMetadata(t *testing.T, root string, titles map[string]string, at time.Time) string {
	t.Helper()
	conversations := map[string]any{}
	for id, title := range titles {
		conversations[id] = map[string]any{
			"summary":            map[string]any{"ID": id, "Title": title, "Preview": "", "NumSteps": 4, "WorkspaceURIs": []string{}},
			"is_internal":        false,
			"last_modified_time": at.Format(time.RFC3339),
		}
	}
	raw, err := json.Marshal(map[string]any{"conversations": conversations})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, agyMetadataFile)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, file, string(raw))
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal(err)
	}
	return file
}

// agyTestPaneRow is the test pane's row in a fresh list.
func agyTestPaneRow(t *testing.T, n *Node) proto.Session {
	t.Helper()
	for _, row := range listSessions(t, n) {
		if row.SessionID == agyTestRow {
			return row
		}
	}
	t.Fatalf("no row %s", agyTestRow)
	return proto.Session{}
}

func agyTestTitle(t *testing.T, n *Node, title string, source proto.TitleSource) {
	t.Helper()
	if row := agyTestPaneRow(t, n); row.Title != title || row.TitleSource != source || row.TitleManual {
		t.Fatalf("title %q (source %q, manual %v), want %q (source %q)", row.Title, row.TitleSource, row.TitleManual, title, source)
	}
}

// An agy pane's row takes its conversation's title once the node knows the conversation (#13639): the companion's,
// else agy's own, else the first message typed into it; herdr's terminal title only while none is known.
func TestAgyPaneTitle(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	root := n.agyDataRoot()
	n.runtimes = newRuntimeRegistry()
	runtime := n.runtimes.add(nil)
	runtime.ready, runtime.hello.Runtime = true, agyAgent
	companion := func(title string) {
		n.runtimes.setSessions(runtime, []proto.Session{
			{SessionID: "agy-" + agyTestID, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle, Title: title},
		})
	}
	agyTestFile(t, root, agyTestID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"<USER_REQUEST>\n<persona-context>\nsnapshot\n</persona-context>\nFix the nightly build and tell me what broke\n</USER_REQUEST>"}
{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","content":"Looking."}
{"step_index":2,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"<USER_REQUEST>\nAnd the flaky test\n</USER_REQUEST>"}
`)

	// While herdr reports no conversation for the pane, the terminal's title stands, whatever agy has.
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, "")}, nil))
	agyTestTitle(t, n, "agy", proto.TitleSourceUnknown)

	// Known, untitled by agy and the companion: the first message typed, without Persona's context, clipped.
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
	agyTestTitle(t, n, "Fix the nightly build and tell me what b…", proto.TitleSourcePrompt)

	// agy's own title wins over the first message, with no provenance claimed; the companion's stand-ins for an
	// untitled conversation (its folder's name, its label) do not count.
	old := time.Now().Add(-time.Hour)
	agyTestMetadata(t, root, map[string]string{agyTestID: "  Nightly Build\nFailure  ", agyOtherTestID: "Another"}, old)
	agyTestTitle(t, n, "Nightly Build Failure", proto.TitleSourceUnknown)
	for _, standIn := range []string{filepath.Base(home), agyStandIn} {
		companion(standIn)
		agyTestTitle(t, n, "Nightly Build Failure", proto.TitleSourceUnknown)
	}

	// A rewritten metadata file is read again; an empty title from the companion is none.
	agyTestMetadata(t, root, map[string]string{agyTestID: "Nightly Build Fixed"}, old.Add(time.Minute))
	companion("")
	agyTestTitle(t, n, "Nightly Build Fixed", proto.TitleSourceUnknown)

	// The companion's title for the conversation comes first, as for its own row, with its provenance.
	companion("Release Checklist")
	agyTestTitle(t, n, "Release Checklist", proto.TitleSourceUnknown)
	n.runtimes.setSessions(runtime, []proto.Session{{SessionID: "agy-" + agyTestID, Agent: agyAgent, Cwd: home,
		Status: proto.StatusIdle, Title: "Named By Hand", TitleManual: true, TitleSource: proto.TitleSourceManual}})
	if row := agyTestPaneRow(t, n); row.Title != "Named By Hand" || !row.TitleManual || row.TitleSource != proto.TitleSourceManual {
		t.Fatalf("companion's title provenance not kept: %q %q %v", row.Title, row.TitleSource, row.TitleManual)
	}
}

// agy's metadata is parsed again only when its file changes; a file it cannot use gives no title, until it changes.
func TestAgyNativeTitles(t *testing.T) {
	n, _ := testNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", t.TempDir())
	root := n.agyDataRoot()
	if got := n.agyNativeTitle(agyTestID); got != "" {
		t.Fatalf("title without metadata: %q", got)
	}

	at := time.Now().Add(-time.Hour)
	file := agyTestMetadata(t, root, map[string]string{agyTestID: "First Title"}, at)
	if got := n.agyNativeTitle(agyTestID); got != "First Title" {
		t.Fatalf("title %q, want First Title", got)
	}
	// The same file, size and time is the file already read (a test of the cache: agy never rewrites it this way).
	agyTestMetadata(t, root, map[string]string{agyTestID: "Other Title"}, at)
	if got := n.agyNativeTitle(agyTestID); got != "First Title" {
		t.Fatalf("an unchanged file was parsed again: %q", got)
	}
	at = at.Add(time.Second)
	agyTestMetadata(t, root, map[string]string{agyTestID: "Third Title"}, at)
	if got := n.agyNativeTitle(agyTestID); got != "Third Title" {
		t.Fatalf("a changed file was not read again: %q", got)
	}

	for name, contents := range map[string]string{
		"truncated":  `{"conversations":{"` + agyTestID + `":{"summary":{"Title":"Cut`,
		"no summary": `{"conversations":{"` + agyTestID + `":{"is_internal":false}}}`,
		"untitled":   `{"conversations":{"` + agyTestID + `":{"summary":{"Title":"   "}}}}`,
		"other":      `[]`,
	} {
		agyTestWrite(t, file, contents)
		at = at.Add(time.Second)
		if err := os.Chtimes(file, at, at); err != nil {
			t.Fatal(err)
		}
		if got := n.agyNativeTitle(agyTestID); got != "" {
			t.Errorf("%s: title %q", name, got)
		}
	}
}

// A Persona-started agy pane never displays the sourced agent-start-*.sh shell command as its title; before agy's
// conversation or metadata title is available, the row shows the start's first prompt instead.
func TestAgyPaneDropsStartScriptTitleAndUsesFirstPrompt(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{". /home/user/.persona/agent-start-3733190294.sh", true},
		{". '/home/user/.persona/agent-start-123.sh' && true abc123", true},
		{"source /home/user/.persona/agent-start-9.sh", true},
		{". /Users/u/.persona-nas/agent-start-3456784508.sh && true", true},
		{". /Users/u/.persona-nas/agent-start-3456784508.sh && tr", true},
		{". /Users/u/.persona-nas/agent-start-3456784508.sh && tr…", true},
		{". '/Users/u/Persona Data/agent-start-123.sh' && true", true},
		{`source "/Users/u/Persona Data/agent-start-123.sh" && tru`, true},
		{"Fix agent-start-123.sh bug", false},
		{"source /home/user/start.sh && true", false},
		{"echo /home/user/.persona/agent-start-9.sh && true", false},
		{". /home/user/.persona/agent-start-9.sh && trash", false},
		{". /home/user/.persona/agent-start-9.sh && true && ls", false},
		{". /home/user/.persona/agent-start-custom.sh && true", false},
		{`. '/home/user/.persona/agent-start-9.sh" && true`, false},
		{"agy", false},
		{"", false},
	} {
		if got := isStartScriptTitle(tc.title); got != tc.want {
			t.Errorf("isStartScriptTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}

	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	const startScriptTitle = ". /home/user/.persona/agent-start-3733190294.sh && true"
	const prompt = "Fix the agy placeholder title bug"

	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	paneAgent := agentOf(agyAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, startScriptTitle)
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" {
			f.reply("session.snapshot", snapshot(nil, nil, []any{paneAgent}, nil))
			f.setScreen("w1:p2", agyIdleScreen)
		}
	})
	f.reply("agent.get", map[string]any{"type": "agent_info", "agent": agentOf(agyAgent, "", proto.StatusWorking, "w1:p2", "w1:t2", "w1", home, startScriptTitle)})

	started, err := n.startWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), home, agyAgent, agyAgent, "", "", "", prompt, "w1", "")
	if err != nil {
		t.Fatalf("startWorkAgent: %v", err)
	}
	if started.Title != prompt || started.TitleSource != proto.TitleSourcePrompt {
		t.Fatalf("started session title = %q (source %q), want %q (%q)", started.Title, started.TitleSource, prompt, proto.TitleSourcePrompt)
	}
	var listed *proto.Session
	for _, row := range listSessions(t, n) {
		if row.SessionID == started.SessionID {
			cp := row
			listed = &cp
			break
		}
	}
	if listed == nil {
		t.Fatalf("started row %s not in session list", started.SessionID)
	}
	if listed.Title != prompt || listed.TitleSource != proto.TitleSourcePrompt {
		t.Fatalf("listed agy pane title = %q (source %q), want %q (%q)", listed.Title, listed.TitleSource, prompt, proto.TitleSourcePrompt)
	}

	// Even a pane with no recorded start prompt never displays Persona's agent-start-*.sh command as its title.
	const orphanTitle = ". /home/user/.persona/agent-start-99999.sh && tr"
	orphan := agentOf(agyAgent, "", proto.StatusIdle, agyTestPane, "w1:t1", "w1", home, orphanTitle)
	f.reply("session.snapshot", snapshot(nil, nil, []any{orphan}, nil))
	row := agyTestPaneRow(t, n)
	if row.Title == orphanTitle {
		t.Fatalf("orphan agy pane kept start script title %q", row.Title)
	}
}
