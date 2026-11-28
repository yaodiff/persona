package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// "Start new Agents as Orca chats" (orca_chat_default.go, #17650).

// fakeOrcaChats is the devices' switches: the computers it is on for (Orca in their engine set).
type fakeOrcaChats map[string]bool

func (f fakeOrcaChats) OrcaChatStarts(_ context.Context, id string) bool { return f[id] }

// orcaChatHub: box runs Orca, whose bridge offers Claude Code's and Codex's chats beside the plain CLIs and a Claude
// Code wrapper; plain has no Orca chat launcher; paseobox starts in Paseo by default. Orca's Claude Code chats list
// opus (low and high) and sonnet (their default).
func orcaChatHub(t *testing.T) (*Service, *fakeHub, fakeOrcaChats) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.ownModels = true
	for _, id := range []string{"box", "plain", "paseobox"} {
		f.sessions[id] = nil
	}
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0", Engine: proto.EngineOrca},
		{ID: "plain", Name: "plain", Home: "/home/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0", Engine: proto.EngineHerdr},
		{ID: "paseobox", Name: "paseobox", Home: "/home/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0", Engine: proto.EnginePaseo},
	}
	chats := []proto.Launcher{
		{Name: "orca-claude", Source: proto.EngineOrca, DisplayName: "Orca chat · Claude Code"},
		{Name: "orca-codex", Source: proto.EngineOrca, Agent: "codex", DisplayName: "Orca chat · Codex",
			PermissionModes: proto.PermissionModeSetOf("default", "auto", "bypassPermissions")},
	}
	cli := []proto.Launcher{
		{Name: "claude", Source: "builtin"},
		{Name: "codex", Source: "zshrc", Agent: "codex"},
		{Name: "glm", Source: "zshrc"},
	}
	f.zshrc = map[string][]proto.Launcher{
		"server":   {{Name: "claude", Source: "builtin"}},
		"box":      append(append([]proto.Launcher{}, cli...), chats...),
		"plain":    cli,
		"paseobox": append(append([]proto.Launcher{}, cli...), chats...),
	}
	f.agentModels = map[string][]proto.AgentModel{
		"orca-claude": {{ID: "opus", Name: "Opus", Efforts: []string{"low", "high"}}, {ID: "sonnet", Name: "Sonnet", Default: true}},
	}
	s := newServiceOn(t, f, &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}})
	on := fakeOrcaChats{}
	s.orcaChats = on
	return s, f, on
}

// startOn is a person's New Agent start on machine with params, as the hub asks for it, and its launcher note.
func startOn(t *testing.T, s *Service, machine string, params map[string]any) (map[string]any, string) {
	t.Helper()
	ctx, note := withLauncherNote(context.Background())
	out, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: machine, Params: params})
	if err != nil {
		t.Fatalf("%s %v: %v", machine, params, err)
	}
	return out, *note
}

// defaultOpens is default_opens of New Agent's launchers on machine (?host= when host is set); nil when absent.
func defaultOpens(t *testing.T, s *Service, machine, host string) map[string]any {
	t.Helper()
	path := "/api/v1/machines/" + machine + "/launchers"
	if host != "" {
		path += "?host=" + host
	}
	r := request(s, "GET", path, "")
	var out struct {
		Opens map[string]any `json:"default_opens"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &out) != nil {
		t.Fatalf("%s: %d %s", machine, r.Code, r.Body.String())
	}
	return out.Opens
}

// Off (the default), nothing changes: New Agent's default opens a terminal and the form names no chat.
func TestOrcaChatDefaultOffChangesNothing(t *testing.T) {
	s, _, _ := orcaChatHub(t)
	params, note := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""})
	if params["launcher"] != "claude" || note != "" {
		t.Fatalf("off: %v %q", params, note)
	}
	if o := defaultOpens(t, s, "box", ""); o != nil {
		t.Fatalf("off, the form says %v", o)
	}
	s.orcaChats = nil // a manager without the devices
	if params, _ := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""}); params["launcher"] != "claude" {
		t.Fatalf("no devices: %v", params)
	}
}

// On: a start that keeps the default Claude Code opens as its Orca chat, with the permission mode it was given; the
// form says so. One the person points elsewhere stays in a terminal.
func TestOrcaChatDefaultRoutesPersonStarts(t *testing.T) {
	s, _, on := orcaChatHub(t)
	on["box"], on["plain"], on["paseobox"] = true, true, true

	params, note := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": "", "permission_mode": "acceptEdits"})
	if params["launcher"] != "orca-claude" || params["agent_type"] != "claude" || params["permission_mode"] != "acceptEdits" ||
		params["model"] != nil || params["model_fingerprint"] != nil || note != "" {
		t.Fatalf("routed: %v %q", params, note)
	}
	o := defaultOpens(t, s, "box", "")
	if o["launcher"] != "orca-claude" || o["display_name"] != "Orca chat · Claude Code" || o["dropped"] != nil {
		t.Fatalf("the form: %v", o)
	}
	if modes, _ := json.Marshal(o["permission_modes"]); string(modes) != `["default","acceptEdits","auto","bypassPermissions"]` {
		t.Fatalf("the form's modes: %s", modes)
	}
	// An Orca host picked is still Orca.
	if params, _ := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": "", "host": "orca"}); params["launcher"] != "orca-claude" {
		t.Fatalf("host orca: %v", params)
	}
	if o := defaultOpens(t, s, "box", "orca"); o["launcher"] != "orca-claude" {
		t.Fatalf("the form, host orca: %v", o)
	}

	stays := map[string]map[string]any{
		"a launcher picked":         {"launcher": "claude"},
		"a model picked":            {"launcher": "", "model": "opus"},
		"another host":              {"launcher": "", "host": "herdr"},
		"plan mode":                 {"launcher": "", "permission_mode": "plan"},
		"ultracode":                 {"launcher": "", "ultracode": true},
		"a goal":                    {"launcher": "", "goal": "tests pass"},
		"a faster speed":            {"launcher": "", "service_tier": "priority"},
		"a resume":                  {"resume_session_id": "11111111-2222-4333-8444-555555555555"},
		"model aliases":             {"launcher": "", "model_aliases": map[string]any{"opus": "opus"}},
		"a work session (explicit)": {"launcher": "claude", "work_session": true},
	}
	for why, extra := range stays {
		p := map[string]any{"cwd": "/home/me/code"}
		for k, v := range extra {
			p[k] = v
		}
		ctx, _ := withLauncherNote(context.Background())
		out, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: p})
		if err == nil && strings.HasPrefix(stringOf(out["launcher"]), orcaChatLauncherPrefix) {
			t.Errorf("%s: routed to %v", why, out["launcher"])
		}
	}
	if o := defaultOpens(t, s, "box", "herdr"); o != nil {
		t.Fatalf("the form, host herdr: %v", o)
	}
	// No Orca chat launcher there, or an engine runtime as the computer's host: a terminal (or the runtime), as before.
	for _, m := range []string{"plain", "paseobox"} {
		if params, _ := startOn(t, s, m, map[string]any{"cwd": "/home/me/code", "launcher": ""}); params["launcher"] == "orca-claude" {
			t.Errorf("%s: routed %v", m, params)
		}
		if o := defaultOpens(t, s, m, ""); o != nil {
			t.Errorf("%s: the form says %v", m, o)
		}
	}
}

func stringOf(v any) string { s, _ := v.(string); return s }

// Codex's default goes to Orca's Codex chat, unless the mode is one Orca's Codex chats don't take; a default that runs
// another provider's model (a wrapper of Claude Code) stays in a terminal.
func TestOrcaChatDefaultCodexAndWrappers(t *testing.T) {
	s, _, on := orcaChatHub(t)
	on["box"] = true
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex"})
	if params, _ := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""}); params["launcher"] != "orca-codex" || params["agent_type"] != "codex" {
		t.Fatalf("codex: %v", params)
	}
	if params, _ := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": "", "permission_mode": "acceptEdits"}); params["launcher"] != "codex" {
		t.Fatalf("codex, a mode its chats don't take: %v", params)
	}
	o := defaultOpens(t, s, "box", "")
	if modes, _ := json.Marshal(o["permission_modes"]); o["launcher"] != "orca-codex" || string(modes) != `["default","auto","bypassPermissions"]` {
		t.Fatalf("the form, codex: %v", o)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.glm"})
	if params, _ := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""}); params["launcher"] != "glm" {
		t.Fatalf("a wrapper: %v", params)
	}
	if o := defaultOpens(t, s, "box", ""); o != nil {
		t.Fatalf("the form, a wrapper: %v", o)
	}
}

// A chat launcher the computer's Orca no longer offers (Orca refused Codex's chat there, its chat runtime not installed,
// #17684) is not routed to: Codex's default start opens a terminal and the form names no chat, while Claude Code's
// default, whose chat Orca still offers, goes on opening as one.
func TestOrcaChatDefaultStaysInATerminalWithoutTheChatLauncher(t *testing.T) {
	s, f, on := orcaChatHub(t)
	on["box"] = true
	f.zshrc["box"] = slices.DeleteFunc(slices.Clone(f.zshrc["box"]), func(l proto.Launcher) bool { return l.Name == "orca-codex" })
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex"})
	if params, note := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""}); params["launcher"] != "codex" || note != "" {
		t.Fatalf("codex without its chat launcher: %v %q", params, note)
	}
	if o := defaultOpens(t, s, "box", ""); o != nil {
		t.Fatalf("the form, codex without its chat launcher: %v", o)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude"})
	if params, _ := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""}); params["launcher"] != "orca-claude" {
		t.Fatalf("claude, its chat launcher still offered: %v", params)
	}
}

// The default's model goes with it when Orca's chats list it (its effort when the model lists that); else Orca's own
// runs, and the note and the form say so.
func TestOrcaChatDefaultModel(t *testing.T) {
	s, _, on := orcaChatHub(t)
	on["box"] = true
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", Model: "opus", Effort: "high"})
	params, note := startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""})
	if params["launcher"] != "orca-claude" || params["model"] != "opus" || params["effort"] != "high" || note != "" {
		t.Fatalf("listed: %v %q", params, note)
	}
	if o := defaultOpens(t, s, "box", ""); o["model"] != "opus" || o["effort"] != "high" || o["dropped"] != nil {
		t.Fatalf("the form, listed: %v", o)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", Model: "haiku"})
	params, note = startOn(t, s, "box", map[string]any{"cwd": "/home/me/code", "launcher": ""})
	want := i18n.T(s.lang(context.Background()), "assistant-tasks.orca_chat_option_dropped", i18n.Args{"to": "Orca chat · Claude Code", "option": "haiku"})
	if params["launcher"] != "orca-claude" || params["model"] != nil || note != want {
		t.Fatalf("not listed: %v %q", params, note)
	}
	if o := defaultOpens(t, s, "box", ""); o["model"] != nil || o["dropped"] != true {
		t.Fatalf("the form, not listed: %v", o)
	}
}

// The assistant's own starts on the same computer never open as an Orca chat.
func TestOrcaChatDefaultLeavesTheAssistantsStarts(t *testing.T) {
	s, f, on := orcaChatHub(t)
	on["box"] = true
	if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start", json.RawMessage(`{"machine":"box","cwd":"/home/me/code","prompt":"Reply with pong"}`)); err != nil {
		t.Fatal(err)
	}
	starts := startsOf(f)
	if len(starts) != 1 || starts[0]["launcher"] != "claude" {
		t.Fatalf("agent_start: %v", starts)
	}
}
