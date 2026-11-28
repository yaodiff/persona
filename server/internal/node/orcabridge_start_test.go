package node

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// orcaStartHubID is the id the hub names for a chat New Agent starts (a session id as the hub makes them).
const orcaStartHubID = "0d000000-0000-4000-8000-00000000c0de"

// orcaChatStartNode is orcaBridgeNode on an Orca that starts chats (agent.launch's chat surface) and found Claude Code
// and Codex installed (and an agent it does not run as a chat), whose launches the fake takes.
func orcaChatStartNode(t *testing.T) (*Node, *orcaChats) {
	t.Helper()
	o := newOrcaChats(t)
	o.f.SetStatus(map[string]any{"capabilities": append(append([]string{}, orcaControlsCaps...), orcart.CapChatLaunch)})
	o.f.Handle("preflight.installedAgents", func(map[string]any) any {
		return map[string]any{"agents": []any{"claude", "codex", "a-terminal-only-tool"}}
	})
	o.f.Handle("agentSession.options", func(map[string]any) any {
		return map[string]any{"current": map[string]any{"permissionMode": "ask", "model": "gpt-6.2-sol"},
			"models": []any{map[string]any{"id": "gpt-6.2-sol", "label": "Sol", "isDefault": true, "efforts": []any{"low", "high", "ultra"}},
				map[string]any{"id": "gpt-6.2-mini", "label": "Mini", "efforts": []any{}}}}
	})
	o.f.Handle("agent.launch", func(p map[string]any) any { return map[string]any{"sessionId": p["sessionId"]} })
	n, _ := orcaBridgeNodeOn(t, o)
	waitFor(t, "the chat listed", func() bool { _, ok := listed(t, n)[orcaChatUUID]; return ok })
	return n, o
}

// orcaLaunchersOf are the Orca bridge's entries in launchers.list.
func orcaLaunchersOf(t *testing.T, n *Node) map[string]proto.Launcher {
	t.Helper()
	raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var res struct{ Launchers []proto.Launcher }
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	out := map[string]proto.Launcher{}
	for _, l := range res.Launchers {
		if strings.HasPrefix(l.Name, orcaLauncherPrefix) {
			out[l.Name] = l
		}
	}
	return out
}

// New Agent offers one launcher per provider Orca runs as a chat and found installed: Claude Code's with Claude
// Code's choices, Codex's with the permission modes Orca takes for it and the models its chats list (the efforts
// Persona can send). Every open chat names its provider's launcher, so its model picker lists Orca's models.
func TestOrcaBridgeOffersChatLaunchers(t *testing.T) {
	n, _ := orcaChatStartNode(t)
	waitFor(t, "the launchers", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
	ls := orcaLaunchersOf(t, n)
	claude, codex := ls["orca-claude"], ls["orca-codex"]
	if claude.DisplayName != "Orca chat · Claude Code" || claude.Agent != "" || claude.PermissionModes != 0 || claude.Source != orcaRuntimeID {
		t.Fatalf("claude launcher %+v", claude)
	}
	if codex.DisplayName != "Orca chat · Codex" || codex.Agent != "codex" ||
		strings.Join(codex.PermissionModes.List(), ",") != "default,auto,bypassPermissions" {
		t.Fatalf("codex launcher %+v", codex)
	}
	models, err := nodeCall(t, n, "launchers.models", map[string]any{"name": "orca-codex"})
	if err != nil || !strings.Contains(string(models), `"id":"gpt-6.2-sol"`) || !strings.Contains(string(models), `"efforts":["low","high"]`) ||
		!strings.Contains(string(models), `"id":"gpt-6.2-mini"`) {
		t.Fatalf("codex models %s %v", models, err)
	}
	waitFor(t, "the rows' launchers", func() bool {
		rows := listed(t, n)
		return rows[orcaChatUUID].Launcher == "orca-claude" && rows[orcaCodexID].Launcher == "orca-codex"
	})
}

// An Orca whose agent.launch has no chat surface (an older one) offers no launcher, nor does one that is not running.
func TestOrcaBridgeOffersNoLaunchersWithoutChatStarts(t *testing.T) {
	o := newOrcaChats(t)
	o.f.Handle("preflight.installedAgents", func(map[string]any) any { return map[string]any{"agents": []any{"claude", "codex"}} })
	n, _ := orcaBridgeNodeOn(t, o)
	waitFor(t, "the chat listed", func() bool { _, ok := listed(t, n)[orcaChatUUID]; return ok })
	if ls := orcaLaunchersOf(t, n); len(ls) != 0 {
		t.Fatalf("launchers %+v", ls)
	}
	if l := listed(t, n)[orcaChatUUID].Launcher; l != "" {
		t.Fatalf("row launcher %q", l)
	}
	if ls := orcaLaunchers([]string{"claude"}, nil, nil); ls != nil {
		t.Fatalf("launchers of an Orca not running: %v", ls)
	}
}

// A start from New Agent on an Orca chat launcher opens a chat of that provider through agent.launch's chat surface,
// in the background, in the Orca worktree holding the folder, with the first message and the form's choices in Orca's
// words; the chat is "persona-<the hub's id>" in Orca and the hub's id in Persona, listed at once and started by
// Persona. A start asked for again (its answer lost) finds that chat and launches nothing.
func TestOrcaBridgeStartsAChat(t *testing.T) {
	n, o := orcaChatStartNode(t)
	waitFor(t, "the launchers", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
	start := func() (proto.Session, error) {
		raw, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app/server", "prompt": "Fix the flaky test",
			"launcher": "orca-codex", "model": "gpt-6.2-sol", "permission_mode": "auto", "effort": "high",
			"session_id": orcaStartHubID, "client_msg_id": "m-1"})
		var res struct{ Session proto.Session }
		if err == nil {
			err = json.Unmarshal(raw, &res)
		}
		return res.Session, err
	}
	s, err := start()
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != orcaStartHubID || s.Cwd != "/w/app" || s.SpaceID != "repo::/w/app" || s.Agent != "codex" ||
		s.Status != proto.StatusWorking || !s.StartedByPersona {
		t.Fatalf("started %+v", s)
	}
	launches := o.writes("agent.launch")
	if len(launches) != 1 {
		t.Fatalf("launches %v", launches)
	}
	l := launches[0]
	target, _ := l["target"].(map[string]any)
	prompt, _ := l["prompt"].(map[string]any)
	options, _ := l["chatOptions"].(map[string]any)
	if l["agent"] != "codex" || l["sessionId"] != orcaStartPrefix+orcaStartHubID || l["surface"] != "chat" ||
		l["presentation"] != "background" || target["kind"] != "existing" || target["worktree"] != "id:repo::/w/app" ||
		prompt["text"] != "Fix the flaky test" || prompt["delivery"] != "submit" || options["permissionMode"] != "auto" ||
		options["model"] != "gpt-6.2-sol" || options["effort"] != "high" {
		t.Fatalf("launch %v", l)
	}
	if bad := o.f.Invalid(); len(bad) > 0 {
		t.Fatalf("Orca's schema refused %v", bad)
	}
	if r, ok := listed(t, n)[orcaStartHubID]; !ok || !r.StartedByPersona {
		t.Fatalf("not listed at once: %+v", r)
	}
	if again, err := start(); err != nil || again.SessionID != orcaStartHubID {
		t.Fatalf("again: %+v %v", again, err)
	}
	if launches := o.writes("agent.launch"); len(launches) != 1 {
		t.Fatalf("launched twice: %v", launches)
	}
}

// A start whose chat Orca already has (made before the node restarted, the hub asking again) launches nothing and
// answers with that chat.
func TestOrcaBridgeStartFindsTheChatAgain(t *testing.T) {
	n, o := orcaChatStartNode(t)
	waitFor(t, "the launchers", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
	o.f.Handle("agentSession.history", func(p map[string]any) any {
		return map[string]any{"ok": true, "page": map[string]any{"fence": 1, "working": false, "epoch": "ep-9", "items": []any{},
			"liveCursor": map[string]any{"epoch": "ep-9", "sequence": 0}}}
	})
	raw, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "prompt": "Fix the flaky test",
		"launcher": "orca-claude", "session_id": orcaStartHubID})
	if err != nil || !strings.Contains(string(raw), orcaStartHubID) {
		t.Fatalf("start %s %v", raw, err)
	}
	if launches := o.writes("agent.launch"); len(launches) != 0 {
		t.Fatalf("launched again: %v", launches)
	}
}

// A folder no Orca worktree holds (and no Git repository to add) is refused in the user's words, with nothing
// started in an unrelated workspace; so is a permission mode Orca does not take for the provider.
func TestOrcaBridgeStartRefusals(t *testing.T) {
	n, o := orcaChatStartNode(t)
	waitFor(t, "the launchers", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
	_, err := nodeCall(t, n, "session.start", map[string]any{"cwd": t.TempDir(), "prompt": "hi", "launcher": "orca-claude",
		"session_id": orcaStartHubID})
	if rpcCode(err) != "unsupported" || !strings.Contains(err.Error(), "Orca") {
		t.Fatalf("folder outside Orca: %v", err)
	}
	_, err = nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "prompt": "hi", "launcher": "orca-codex",
		"permission_mode": "acceptEdits", "session_id": orcaStartHubID})
	if err == nil || !strings.Contains(err.Error(), "acceptEdits") {
		t.Fatalf("accept-edits on Codex: %v", err)
	}
	if launches := o.writes("agent.launch"); len(launches) != 0 {
		t.Fatalf("launched %v", launches)
	}
}

// On an Orca whose agent.launch has no chat surface, a start goes through agentSession.create, its envelope naming no
// fence and its first message in the chat's body.
func TestOrcaBridgeStartCreatesOnAnOlderOrca(t *testing.T) {
	o := newOrcaChats(t)
	o.f.SetStatus(map[string]any{"capabilities": orcaControlsCaps}) // no launch surface
	o.f.Handle("agentSession.create", func(map[string]any) any { return map[string]any{"ok": true, "value": map[string]any{}} })
	b := newOrcaBridge(&orcart.Client{MetaPath: o.f.MetaPath}, nil, t.Logf)
	params, _ := json.Marshal(map[string]any{"cwd": "/w/app", "prompt": "Fix the flaky test", "launcher": "orca-claude",
		"session_id": orcaStartHubID, "client_msg_id": "m-1"})
	res, err := b.start(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if row, _ := res.(map[string]any)["session"].(map[string]any); row["session_id"] != orcaStartHubID || row["started_by_persona"] != true {
		t.Fatalf("started %v", res)
	}
	creates := o.writes("agentSession.create")
	if len(creates) != 1 {
		t.Fatalf("creates %v (launches %v)", creates, o.writes("agent.launch"))
	}
	first, _ := creates[0]["firstMessage"].(map[string]any)
	if creates[0]["agent"] != "claude" || creates[0]["worktree"] != "id:repo::/w/app" || first["clientMessageId"] != "m-1" {
		t.Fatalf("create %v", creates[0])
	}
	if bad := o.f.Invalid(); len(bad) > 0 {
		t.Fatalf("Orca's schema refused %v", bad)
	}
}

// Orca 1.4.277 names the providers it runs as chats (agentSession.agents): an installed agent it does not run as one gets
// no launcher. An Orca without the method offers every installed one (TestOrcaBridgeOffersChatLaunchers).
func TestOrcaBridgeOffersOnlyTheChatsOrcaRuns(t *testing.T) {
	o := newOrcaChats(t)
	o.f.SetStatus(map[string]any{"capabilities": append(append([]string{}, orcaControlsCaps...), orcart.CapChatLaunch)})
	o.f.Handle("preflight.installedAgents", func(map[string]any) any { return map[string]any{"agents": []any{"claude", "codex"}} })
	o.f.Handle("agentSession.agents", func(map[string]any) any {
		return map[string]any{"agents": []any{map[string]any{"agent": "claude", "managedRuntime": true},
			map[string]any{"agent": "pi"}}}
	})
	n, _ := orcaBridgeNodeOn(t, o)
	waitFor(t, "Claude Code's chat launcher", func() bool { return len(orcaLaunchersOf(t, n)) == 1 })
	if ls := orcaLaunchersOf(t, n); ls["orca-claude"].Name == "" {
		t.Fatalf("launchers %+v", ls)
	}
}

// orcaChatRuntimeMissing is Orca 1.4.277's refusal of a chat whose runtime it has not installed on the computer.
const orcaChatRuntimeMissing = "Codex can't open as a chat here because its chat runtime isn't installed on this host " +
	"(install it from a new chat tab in Orca). Start it in a terminal instead."

// orcaChatRefusal is the bridge's refusal err as the hub gets it: its code, data reason and the provider it names.
func orcaChatRefusal(t *testing.T, err error) (code, reason, program string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("not a refusal: %v", err)
	}
	var data struct{ Reason, Program, Delivered string }
	if json.Unmarshal(re.Data, &data) != nil || data.Delivered != "none" {
		t.Fatalf("data %s", re.Data)
	}
	return re.Code, data.Reason, data.Program
}

// A provider whose chat Orca refuses to open for a missing chat runtime (a fresh Orca 1.4.277) is refused in those words,
// nothing started, and its launcher is withdrawn at once, so New Agent offers it no more and its default start stays in
// a terminal; the other provider's stays. Orca showing a new chat of it (the person installed the runtime from a new
// chat tab in Orca) offers it again, and so does Orca starting again.
func TestOrcaBridgeWithdrawsAChatWhoseRuntimeOrcaLacks(t *testing.T) {
	n, o := orcaChatStartNode(t)
	o.f.Handle("agent.launch", func(p map[string]any) any {
		if p["agent"] == "codex" {
			return orcarttest.Refuse("agent_launch_surface_unavailable", orcaChatRuntimeMissing)
		}
		return map[string]any{"sessionId": p["sessionId"]}
	})
	waitFor(t, "the launchers", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
	start := func(id string) error {
		_, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "prompt": "Fix the flaky test",
			"launcher": "orca-codex", "session_id": id})
		return err
	}
	err := start(orcaStartHubID)
	words := i18n.T(i18n.Default, "rpc-errors.orca_chat_runtime_missing", i18n.Args{"program": "Codex"})
	if code, reason, program := orcaChatRefusal(t, err); code != "unsupported" || reason != "orca_chat_runtime_missing" ||
		program != "Codex" || !strings.Contains(err.Error(), words) {
		t.Fatalf("refusal %v (%s %s %s)", err, code, reason, program)
	}
	if _, ok := listed(t, n)[orcaStartHubID]; ok {
		t.Fatal("a refused start is listed")
	}
	waitFor(t, "Codex's chat launcher withdrawn", func() bool {
		ls := orcaLaunchersOf(t, n)
		return len(ls) == 1 && ls["orca-claude"].Name != ""
	})
	waitFor(t, "the Codex chat no longer naming it", func() bool { return listed(t, n)[orcaCodexID].Launcher == "" })

	// The person opens a new Codex chat in Orca: Orca may have its runtime now.
	o.with(func(o *orcaChats) {
		o.tabs = []any{map[string]any{"type": "agent-session", "id": "agent-session:codex_new_chat", "title": "Codex Chat",
			"sessionId": "codex_new_chat", "agent": "codex"}}
	})
	waitFor(t, "Codex's chat launcher offered again", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })

	// Refused again, then Orca quits and starts again: offered again.
	if _, reason, _ := orcaChatRefusal(t, start("0d000000-0000-4000-8000-00000000c0df")); reason != "orca_chat_runtime_missing" {
		t.Fatalf("second refusal %q", reason)
	}
	waitFor(t, "withdrawn again", func() bool { return len(orcaLaunchersOf(t, n)) == 1 })
	o.f.Stop()
	waitFor(t, "none while Orca is not running", func() bool { return len(orcaLaunchersOf(t, n)) == 0 })
	if err := o.f.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both once Orca runs again", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
}

// Another reason Orca won't open the chat (a project under WSL) is worded as such, naming the provider, and keeps the
// launcher: it may hold for this folder only. An Orca with no chat start at all keeps its own words.
func TestOrcaBridgeChatUnavailableForAnotherReason(t *testing.T) {
	n, o := orcaChatStartNode(t)
	o.f.Handle("agent.launch", func(map[string]any) any {
		return orcarttest.Refuse("agent_launch_surface_unavailable",
			"Claude can't open as a chat here because this workspace runs under WSL. Start it in a terminal instead.")
	})
	waitFor(t, "the launchers", func() bool { return len(orcaLaunchersOf(t, n)) == 2 })
	_, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "prompt": "hi", "launcher": "orca-claude",
		"session_id": orcaStartHubID})
	if code, reason, program := orcaChatRefusal(t, err); code != "unsupported" || reason != "orca_chat_unavailable" || program != "Claude Code" {
		t.Fatalf("refusal %v (%s %s %s)", err, code, reason, program)
	}
	polls := o.f.Count("worktree.ps")
	waitFor(t, "two refreshes after the refusal", func() bool { return o.f.Count("worktree.ps") >= polls+2 })
	if ls := orcaLaunchersOf(t, n); len(ls) != 2 {
		t.Fatalf("launchers %+v", ls)
	}
	for _, code := range []string{"agent_launch_unsupported", "method_not_found"} {
		re, ok := orcaStartErr(&orcart.Error{Code: code}, "claude").(*rpc.Error)
		if !ok || !strings.Contains(string(re.Data), `"reason":"orca_start_unsupported"`) {
			t.Errorf("%s: %#v", code, orcaStartErr(&orcart.Error{Code: code}, "claude"))
		}
	}
}
