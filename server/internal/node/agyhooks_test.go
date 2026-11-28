package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

const (
	hookConvA = "11111111-1111-4111-8111-111111111111"
	hookConvB = "22222222-2222-4222-8222-222222222222"
)

func agyRow(pane, herdrConv string) host.Agent {
	a := host.Agent{Agent: agyAgent, PaneID: pane}
	if herdrConv != "" {
		a.AgentSession = &host.AgentSession{Source: "herdr:antigravity_cli", Agent: agyAgent, Kind: "id", Value: herdrConv}
	}
	return a
}

// agyNode is a node whose engines list the given rows (by host kind).
func agyNode(t *testing.T, rows map[string][]host.Agent) *Node {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", filepath.Join(root, "antigravity-cli"))
	n := &Node{home: root, dir: filepath.Join(root, "node"), bin: "/opt/personad", hostAgents: rows, agents: map[string]host.Agent{}}
	for _, as := range rows {
		for _, a := range as {
			n.agents[otherSession(a)] = a
		}
	}
	return n
}

func (n *Node) hookReport(pane, event, conv string) map[string]any {
	in, _ := json.Marshal(nodeclient.AgyReport{Event: event, Conversation: conv})
	return n.agyHookKind(sockRequest{Kind: nodeclient.AgyHookKind, Pane: pane, Input: in})
}

func TestAgyHookReportBindsEveryEngine(t *testing.T) {
	panes := []struct{ kind, pane string }{
		{host.Herdr, "w1:p1"},
		{host.HerdrCore, "herdr-core:t7"},
		{host.Orca, "orca:tab1:leaf1"},
		{host.Tern, "tern:b3"},
	}
	for _, p := range panes {
		row := agyRow(p.pane, "")
		n := agyNode(t, map[string][]host.Agent{p.kind: {row}})
		id := otherSession(row)
		if got := n.rawAgyConversationLocked(id, p.pane); got != "" {
			t.Fatalf("%s: conversation before any report: %q", p.pane, got)
		}
		if r := n.hookReport(p.pane, "PreInvocation", hookConvA); r["ok"] != true {
			t.Fatalf("%s: report refused", p.pane)
		}
		if got := n.rawAgyConversationLocked(id, p.pane); got != hookConvA {
			t.Errorf("%s: conversation = %q", p.pane, got)
		}
	}
}

func TestAgyHookReportConflicts(t *testing.T) {
	a, b := agyRow("w1:p1", ""), agyRow("w1:p2", "")
	n := agyNode(t, map[string][]host.Agent{host.Herdr: {a, b}})
	n.hookReport("w1:p1", "PreInvocation", hookConvA)
	n.hookReport("w1:p2", "PreInvocation", hookConvA)
	for _, row := range []host.Agent{a, b} {
		if got := n.rawAgyConversationLocked(otherSession(row), row.PaneID); got != "" {
			t.Errorf("two panes in one conversation: %s reads %q", row.PaneID, got)
		}
	}
	n.hookReport("w1:p2", "PreInvocation", hookConvB) // the second pane moves on: the first is alone again
	if got := n.rawAgyConversationLocked(otherSession(a), "w1:p1"); got != hookConvA {
		t.Errorf("after the other pane moved: %q", got)
	}
	if !n.agyConversationClaimedElsewhereLocked("w1:p1", hookConvB) {
		t.Error("the conversation of pane 2 is not claimed against pane 1")
	}
}

func TestAgyHookAndHerdrReportsMustAgree(t *testing.T) {
	row := agyRow("w1:p1", hookConvA)
	n := agyNode(t, map[string][]host.Agent{host.Herdr: {row}})
	id := otherSession(row)
	if got := n.rawAgyConversationLocked(id, "w1:p1"); got != hookConvA {
		t.Fatalf("herdr alone: %q", got)
	}
	n.hookReport("w1:p1", "PreInvocation", hookConvA)
	if got := n.rawAgyConversationLocked(id, "w1:p1"); got != hookConvA {
		t.Errorf("both agree: %q", got)
	}
	n.hookReport("w1:p1", "PreInvocation", hookConvB)
	if got := n.rawAgyConversationLocked(id, "w1:p1"); got != "" {
		t.Errorf("both disagree: %q", got)
	}
}

func TestAgyConflictingPaneStillBlocksOtherClaims(t *testing.T) {
	a := agyRow("w1:p1", hookConvA)
	b := agyRow("w1:p2", hookConvA)
	c := agyRow("w1:p3", hookConvB)
	n := agyNode(t, map[string][]host.Agent{host.Herdr: {a, b, c}})
	n.hookReport(b.PaneID, "PreInvocation", hookConvB)
	for _, row := range []host.Agent{a, b, c} {
		if got := n.rawAgyConversationLocked(otherSession(row), row.PaneID); got != "" {
			t.Errorf("pane %s read a contested conversation", row.PaneID)
		}
	}
	n.hookReport(b.PaneID, "PreInvocation", hookConvA)
	if got := n.rawAgyConversationLocked(otherSession(c), c.PaneID); got != hookConvB {
		t.Error("the unique report should read again")
	}
}

func TestAgyHookReportValidation(t *testing.T) {
	row := agyRow("w1:p1", "")
	n := agyNode(t, map[string][]host.Agent{host.Herdr: {row}})
	for name, in := range map[string]nodeclient.AgyReport{
		"not a uuid":     {Event: "PreInvocation", Conversation: "../etc"},
		"unknown event":  {Event: "PostToolUse", Conversation: hookConvA},
		"path outside":   {Event: "Stop", Conversation: hookConvA, TranscriptPath: "/etc/passwd"},
		"path traversal": {Event: "Stop", Conversation: hookConvA, TranscriptPath: filepath.Join(n.agyDataRoot(), "..", "x")},
		"empty conv":     {Event: "Stop"},
	} {
		b, _ := json.Marshal(in)
		if r := n.agyHookKind(sockRequest{Pane: "w1:p1", Input: b}); r["ok"] != false {
			t.Errorf("%s: accepted", name)
		}
	}
	if r := n.agyHookKind(sockRequest{Input: []byte(`{}`)}); r["ok"] != false {
		t.Error("no pane: accepted")
	}
	good, _ := json.Marshal(nodeclient.AgyReport{Event: "Stop", Conversation: hookConvA,
		TranscriptPath: filepath.Join(n.agyDataRoot(), "brain", hookConvA, ".system_generated", "logs", "transcript.jsonl")})
	if r := n.agyHookKind(sockRequest{Pane: "w1:p1", Input: good}); r["ok"] != true {
		t.Error("a transcript in the data directory was refused")
	}
}

func TestAgyHookReportOnlyForAgyRows(t *testing.T) {
	row := host.Agent{Agent: "claude", PaneID: "w1:p1"}
	n := agyNode(t, map[string][]host.Agent{host.Herdr: {row}})
	n.hookReport("w1:p1", "PreInvocation", hookConvA)
	if got := n.agyReported(row); got != "" {
		t.Errorf("a claude row took a report: %q", got)
	}
}

func TestAgyHooksInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config", "hooks.json")
	other := `{
  "herdr": {
    "PreInvocation": [{"command": "bash x", "timeout": 10, "type": "command"}]
  },
  "moshi-hook": {"Stop": [{"command": "m", "timeout": 5, "type": "command"}], "enabled": true}
}
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := agyHooksWanted("/opt/my tools/personad", "/data/persona", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := agyHooksState(path, want); st != "missing" {
		t.Fatalf("state before = %q", st)
	}
	changed, err := installAgyHooks(path, want)
	if err != nil || !changed {
		t.Fatalf("install: %v %v", changed, err)
	}
	if changed, _ = installAgyHooks(path, want); changed {
		t.Error("second install rewrote the file")
	}
	var top map[string]map[string]json.RawMessage
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top["herdr"] == nil || top["moshi-hook"]["enabled"] == nil || top["persona"]["PreInvocation"] == nil || top["persona"]["Stop"] == nil {
		t.Fatalf("groups after install: %s", b)
	}
	cmd := string(top["persona"]["Stop"])
	for _, part := range []string{"'/opt/my tools/personad' hook --persona-home /data/persona --agy Stop", `"timeout": 5`} {
		if !strings.Contains(cmd, part) {
			t.Errorf("Stop entry %s lacks %q", cmd, part)
		}
	}
	if st, _ := agyHooksState(path, want); st != "installed" {
		t.Errorf("state = %q", st)
	}
	moved, _ := agyHooksWanted("/elsewhere/personad", "/data/persona", "/home/u")
	if st, _ := agyHooksState(path, moved); st != "stale" {
		t.Errorf("state for a moved binary = %q", st)
	}
	if changed, _ = installAgyHooks(path, moved); !changed {
		t.Error("a stale entry was not rewritten")
	}
	if changed, err = uninstallAgyHooks(path); err != nil || !changed {
		t.Fatalf("uninstall: %v %v", changed, err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "persona") && strings.Contains(string(b), "--agy") {
		t.Errorf("entries left: %s", b)
	}
	if !strings.Contains(string(b), `"herdr"`) || !strings.Contains(string(b), `"moshi-hook"`) {
		t.Errorf("others lost: %s", b)
	}
	if changed, _ = uninstallAgyHooks(path); changed {
		t.Error("uninstall of nothing rewrote the file")
	}
}

func TestAgyHooksDefaultDirOmitsHomeFlag(t *testing.T) {
	cmd := agyHookCommand("/bin/personad", "", "/h", "Stop")
	if cmd != "/bin/personad hook --agy Stop" {
		t.Errorf("cmd = %q", cmd)
	}
}

func TestEnsureAgyHooksOnlyWhenAgyIsHere(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", filepath.Join(root, ".gemini", "antigravity-cli"))
	t.Setenv("PATH", t.TempDir())
	n := &Node{home: root, dir: filepath.Join(root, ".persona"), bin: "/opt/personad"}
	file := n.agyHooksFile()
	n.ensureAgyHooks()
	if _, err := os.Stat(file); err == nil {
		t.Fatal("hooks written without agy")
	}
	if err := os.MkdirAll(n.agyDataRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(n.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	n.ensureAgyHooks()
	if st, _ := agyHooksState(file, mustWanted(t, n)); st != "installed" {
		t.Fatalf("state with agy = %q", st)
	}
	var out strings.Builder
	t.Setenv("HOME", root)
	t.Setenv("PERSONA_HOME", n.dir)
	if err := AgyHooksCommand([]string{"uninstall"}, &out); err != nil {
		t.Fatal(err)
	}
	if !n.agyHooksOptedOut() {
		t.Error("uninstall left no opt-out")
	}
	n.ensureAgyHooks()
	if st, _ := agyHooksState(file, mustWanted(t, n)); st != "missing" {
		t.Errorf("the node put the entries back: %q", st)
	}
}

// Starting an agy work agent installs Persona's agy-hooks even if agy was not present when the node started,
// while honoring an explicit opt-out (#13950).
func TestAgyStartEnsuresAgyHooksBeforeLaunch(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", filepath.Join(home, ".gemini", "antigravity-cli"))
	t.Setenv("PATH", t.TempDir()) // agy not on daemon PATH, data dir absent
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})

	file := n.agyHooksFile()
	if _, err := os.Stat(file); err == nil {
		t.Fatal("hooks already existed before start")
	}
	var hooksAtLaunch string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=agy") {
			hooksAtLaunch, _ = agyHooksState(file, mustWanted(t, n))
			cancel()
		}
	})
	_, _ = n.startWorkAgent(ctx, n.hosts.Get(host.Herdr).(*herdrHost), home, agyAgent, agyAgent, "", "", "", "do it", "w1", "")
	if hooksAtLaunch != "installed" {
		t.Fatalf("agy-hooks state when agy launched = %q, want installed", hooksAtLaunch)
	}

	// Opt-out file prevents re-installing hooks on subsequent launches.
	if _, err := uninstallAgyHooks(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.dir, agyHooksOffFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n.ensureAgyHooksForLaunch()
	if st, _ := agyHooksState(file, mustWanted(t, n)); st != "missing" {
		t.Errorf("opted-out node rewrote hooks on launch: %q", st)
	}
}

// Recording an agy pane's resume command also ensures Persona's agy-hooks are installed (#13950).
func TestAgyHoldResumeEnsuresAgyHooks(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", filepath.Join(home, ".gemini", "antigravity-cli"))
	t.Setenv("PATH", t.TempDir())
	f.reply("pane.report_agent", map[string]any{})
	file := n.agyHooksFile()
	n.holdResume(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), "w1:p2", "agy-w1-p2", claudeLaunch{Agent: agyAgent, Launcher: agyAgent})
	if st, _ := agyHooksState(file, mustWanted(t, n)); st != "installed" {
		t.Fatalf("agy-hooks state after holdResume = %q, want installed", st)
	}
}

func mustWanted(t *testing.T, n *Node) json.RawMessage {
	t.Helper()
	w, err := agyHooksWanted(n.bin, n.dir, n.home)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCheckAgyHooksLines(t *testing.T) {
	root := t.TempDir()
	opts := DoctorOptions{Dir: filepath.Join(root, "node"), Bin: "/opt/personad", AgyHooksFile: filepath.Join(root, "config", "hooks.json")}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if c := checkAgyHooks(opts); c.Status != DoctorWarn || c.Hint == "" {
		t.Errorf("missing: %+v", c)
	}
	opts.Fix = true
	if !fixAgyHooks(opts) {
		t.Fatal("--fix did not install")
	}
	if c := checkAgyHooks(opts); c.Status != DoctorOK {
		t.Errorf("installed: %+v", c)
	}
	opts.Bin = "/moved/personad"
	if c := checkAgyHooks(opts); c.Status != DoctorWarn {
		t.Errorf("stale: %+v", c)
	}
	if _, err := uninstallAgyHooks(opts.AgyHooksFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.Dir, agyHooksOffFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if c := checkAgyHooks(opts); c.Status != DoctorOK {
		t.Errorf("opted out: %+v", c)
	}
	if fixAgyHooks(opts) {
		t.Error("--fix put back what was removed on purpose")
	}
}

// The hook's own code against the node's real socket: an Orca pane's PreInvocation names its conversation, and the
// node reads it back, with the hook printing {} as agy needs.
func TestAgyHookOverTheSocket(t *testing.T) {
	home := launchKeyHome(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", filepath.Join(home, ".gemini", "antigravity-cli"))
	row := agyRow("orca:tab1:leaf1", "")
	n := &Node{home: home, hostAgents: map[string][]host.Agent{host.Orca: {row}}, agents: map[string]host.Agent{otherSession(row): row}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := n.listenOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var out strings.Builder
	input := `{"conversationId":"` + hookConvA + `","transcriptPath":"` + filepath.Join(n.agyDataRoot(), "brain", hookConvA, ".system_generated", "logs", "transcript.jsonl") + `","extra":true}`
	env := map[string]string{nodeclient.OrcaPaneEnv: "tab1:leaf1"}
	nodeclient.RunAgyHook(ctx, []string{"PreInvocation"}, strings.NewReader(input), &out, func(k string) string { return env[k] })
	if out.String() != "{}\n" {
		t.Errorf("hook printed %q", out.String())
	}
	if got := n.agyReported(row); got != hookConvA {
		t.Errorf("node's conversation = %q", got)
	}
}
