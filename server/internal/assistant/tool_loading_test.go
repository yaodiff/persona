package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The tools that load up front, pinned: adding or dropping one is a reviewed change (#7317). Each is still a tool here
// under that name (a rename would silently defer it), but for the Lark extension's lark_*, which are not in this repository.
func TestCoreToolsPinned(t *testing.T) {
	want := []string{
		"agent_archive", "agent_interrupt", "agent_read", "agent_send", "agent_start", "agent_supervise", "agents_list",
		"ask_user", "cancel_followup", "conversation_read", "conversation_send", "conversations_list", "interaction_answer",
		"kb_get", "kb_list", "kb_search", "kb_update", "lark_chats", "lark_image", "lark_messages", "lark_search", "larkcli_chats",
		"larkcli_messages", "larkcli_search", "list_followups", "machines_list", "model_profiles_list", "notify_user",
		"persona_settings", "schedule_followup", "schedule_routine", "supervision_finish", "task_plan", "task_review",
		"task_scope", "todo_get", "todo_list", "todo_update",
	}
	if !slices.Equal(coreTools, want) {
		t.Fatalf("core tools changed:\n got %q\nwant %q", coreTools, want)
	}
	for _, name := range coreTools {
		if strings.HasPrefix(name, "lark_") {
			continue
		}
		func() {
			defer func() {
				if recover() != nil {
					t.Errorf("core tool %s is no MCP tool here: renamed or removed?", name)
				}
			}()
			prompts.Tool(name)
		}()
	}
}

// deferringExt is guestExt with every tool arriving deferred, as a connector program sending defer_loading would
// have it: the manager's rule alone decides.
type deferringExt struct{ *guestExt }

func (deferringExt) AgentTools(context.Context, bool) (agents, assistantOnly []proto.Tool) {
	return []proto.Tool{{Name: "conn_read", DeferLoading: true}},
		[]proto.Tool{{Name: "computer_run", DeferLoading: true}, {Name: "lark_chats", DeferLoading: true}, {Name: "lark_agent_reply", DeferLoading: true}}
}

// An assistant conversation's tool list has every tool but the core ones deferred; a guest's (no ToolSearch, whether
// the node or the manager says so), an agent's and every one while assistant.load_all_tools is on have none deferred,
// whatever the tools arrived with.
func TestToolLoadingMarksAssistantTools(t *testing.T) {
	ft := newFleetTest(t)
	ft.f.hello.Assistant.MachineID = "mac"
	s := ft.s
	s.SetExtensions(deferringExt{&guestExt{}}) // lark_chats (core), conn_read, lark_agent_reply and computer_run (not)
	ctx := context.Background()
	if err := s.guestSet(ctx, "mac", "g1", []string{"lark_agent_reply", "lark_chats"}); err != nil {
		t.Fatal(err)
	}
	// list is the tool list as the node gets it (JSON), by name: whether each is deferred.
	list := func(session string, assistant, guest bool) map[string]bool {
		raw, _ := json.Marshal(map[string]any{"assistant": assistant, "session_id": session, "guest": guest})
		out, handled, err := s.NodeRequest(ctx, "mac", "mcp.tools", raw)
		if err != nil || !handled {
			t.Fatal(err, handled)
		}
		b, _ := json.Marshal(out)
		var res struct {
			Tools []proto.Tool `json:"tools"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, tool := range res.Tools {
			got[tool.Name] = tool.DeferLoading
		}
		return got
	}
	got := list("o1", true, false)
	for name, deferred := range map[string]bool{"agent_read": false, "ask_user": false, "lark_chats": false, "task_plan": false,
		"inbox_move": true, "view_show": true, "task_plan_withdraw": true, "lark_agent_reply": true, "computer_run": true,
		"conn_read": true} {
		if d, ok := got[name]; !ok || d != deferred {
			t.Errorf("%s: listed %v, deferred %v; want deferred %v", name, ok, d, deferred)
		}
	}
	for name, deferred := range got {
		if deferred == slices.Contains(coreTools, name) {
			t.Errorf("%s: deferred %v", name, deferred)
		}
	}
	none := func(what string, got map[string]bool) {
		t.Helper()
		if len(got) == 0 {
			t.Fatalf("%s: no tools", what)
		}
		for name, deferred := range got {
			if deferred {
				t.Errorf("%s: %s deferred", what, name)
			}
		}
	}
	none("a guest", list("g1", true, true))
	none("a guest the node did not name", list("g1", true, false))
	none("an agent", list("a1", false, false))
	setSettings(t, s, func(v *Settings) { on := true; v.LoadAllTools = &on })
	none("load_all_tools on", list("o1", true, false))
}

// assistant.load_all_tools through persona_settings: off by default, a change asks on the phone with its own card, and
// the approved value is what the tool list reads.
func TestLoadAllToolsSetting(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	get := func() string { out, _ := tool(`{"action":"get","key":"assistant.load_all_tools"}`); return out }
	deferred := func() bool {
		return s.withToolLoading(ctx, "", []proto.Tool{{Name: "inbox_move"}}, true)[0].DeferLoading
	}
	if get() != "false" || !deferred() {
		t.Fatalf("default: %s, deferred %v", get(), deferred())
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.load_all_tools","value":false}`); isErr || !strings.Contains(out, "is unchanged") {
		t.Fatalf("set to what it is: %s", out)
	}
	word := map[bool]string{true: i18n.T(i18n.Default, "assistant-settings_tool.on"), false: i18n.T(i18n.Default, "assistant-settings_tool.off")}
	card := i18n.T(i18n.Default, "assistant-settings_tool.change_load_all_tools", i18n.Args{"before": word[false], "after": word[true]})
	go func() {
		q, ok := awaitCard(s)
		if !ok || q.Summary != card {
			t.Errorf("card %q (%v), want %q", q.Summary, ok, card)
		}
		allow := true
		raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
			t.Error(err)
		}
	}()
	if out, _ := tool(`{"action":"set","key":"assistant.load_all_tools","value":true}`); !strings.HasPrefix(out, "assistant.load_all_tools is true") {
		t.Fatalf("approved: %s", out)
	}
	if get() != fmt.Sprint(true) || deferred() {
		t.Fatalf("after the change: %s, deferred %v", get(), deferred())
	}
}

// Changing assistant.load_all_tools reaches a running conversation at once: the watch its personad mcp keeps (the
// revision it last listed with) is answered with the new list, without a task-link change or a process restart.
func TestLoadAllToolsReachesRunningConversations(t *testing.T) {
	ft := newFleetTest(t)
	ft.f.hello.Assistant.MachineID = "mac"
	s := ft.s
	ctx := context.Background()
	// poll is one call of the watch as personad mcp makes it: how many tools are listed and how many of them deferred,
	// and the revision to watch from next; listed is 0 when the call ran out of time unchanged.
	poll := func(revision string) (listed, deferred int, next string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, _ := json.Marshal(map[string]any{"assistant": true, "session_id": "o1", "revision": revision})
		out, handled, err := s.NodeRequest(ctx, "mac", "mcp.tools", raw)
		if err != nil || !handled {
			t.Fatal(err, handled)
		}
		b, _ := json.Marshal(out)
		var res struct {
			Tools    []proto.Tool `json:"tools"`
			Revision string       `json:"revision"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatal(err)
		}
		for _, tool := range res.Tools {
			if tool.DeferLoading {
				deferred++
			}
		}
		return len(res.Tools), deferred, res.Revision
	}
	set := func(on bool) {
		t.Helper()
		change, err := s.setLoadAllTools(ctx, json.RawMessage(fmt.Sprint(on)))
		if err != nil || change.apply == nil {
			t.Fatalf("set %v: %v, %+v", on, err, change)
		}
		if _, err := change.apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	listed, deferred, rev := poll("")
	if listed == 0 || deferred == 0 {
		t.Fatalf("first list: %d tools, %d deferred", listed, deferred)
	}
	set(true)
	if listed, deferred, rev = poll(rev); listed == 0 || deferred != 0 {
		t.Fatalf("load_all_tools on: %d tools, %d deferred (no tools: the running conversation was not told)", listed, deferred)
	}
	set(false)
	if listed, deferred, _ = poll(rev); listed == 0 || deferred == 0 {
		t.Fatalf("load_all_tools off: %d tools, %d deferred (no tools: the running conversation was not told)", listed, deferred)
	}
}

// withToolLoading copies: the list it was given, which may be an extension's cached one, keeps its flags.
func TestToolLoadingCopies(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	in := []proto.Tool{{Name: "ask_user", DeferLoading: true}, {Name: "inbox_move"}}
	out := s.withToolLoading(context.Background(), "", in, true)
	if out[0].DeferLoading || !out[1].DeferLoading {
		t.Fatalf("marked %+v", out)
	}
	if !in[0].DeferLoading || in[1].DeferLoading {
		t.Fatalf("the given list changed: %+v", in)
	}
}

// Exercise the initial node response, so the attested session id must reach loading
// classification. Membership changes tool loading, never a tool's caller permissions.
func TestToolLoadingUsesTheConversationRole(t *testing.T) {
	ft := newFleetTest(t)
	ft.f.hello.Assistant.MachineID = "mac"
	s := ft.s
	setSettings(t, s, func(v *Settings) {
		v.Main = "main0001"
		v.Leaders = []string{"lead0001", "main0001"} // main is still main even if present here
	})
	wantLeader := []string{"agent_transfer", "leader_progress", "todo_create", "ask_escalate", "direction_name"}
	if !slices.Equal(leaderCoreTools, wantLeader) {
		t.Fatalf("leader tool defaults changed: %v", leaderCoreTools)
	}
	for _, tc := range []struct {
		name, session string
		deferred      bool
	}{
		{"leader", "lead0001", false},
		{"main", "main0001", true},
		{"ordinary", "side0001", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"assistant": true, "session_id": tc.session})
			out, handled, err := s.NodeRequest(t.Context(), "mac", "mcp.tools", raw)
			if err != nil || !handled {
				t.Fatalf("tool list: %v, handled %v", err, handled)
			}
			b, _ := json.Marshal(out)
			var res struct {
				Tools []proto.Tool `json:"tools"`
			}
			if err := json.Unmarshal(b, &res); err != nil {
				t.Fatal(err)
			}
			for _, name := range wantLeader {
				i := slices.IndexFunc(res.Tools, func(tool proto.Tool) bool { return tool.Name == name })
				if i < 0 || res.Tools[i].DeferLoading != tc.deferred {
					t.Errorf("%s: tool absent or deferred incorrectly (index %d, want %v)", name, i, tc.deferred)
				}
			}
			for _, tool := range res.Tools {
				if slices.Contains(coreTools, tool.Name) && tool.DeferLoading {
					t.Errorf("shared core tool %s was deferred", tool.Name)
				}
			}
		})
	}
}

func TestWorkGuideLoadingScopeAndScheduledGrant(t *testing.T) {
	s := newFleetTest(t).s
	set := func(on, all bool) { setSettings(t, s, func(v *Settings) { v.WorkGuide = &on; v.LoadAllTools = &all }) }
	ctx := t.Context()
	tools := []proto.Tool{{Name: "todo_get"}, {Name: "task_plan"}, {Name: "todo_extension"}, {Name: "ask_user"}}
	set(false, false)
	baseline := s.withToolLoading(ctx, "ordinary", tools, true)
	s.noteCleanRound("scheduled", "task-1")
	set(true, false)
	for _, tc := range []struct {
		name                  string
		ctx                   context.Context
		self                  string
		assistant, todo, plan bool
	}{
		{"assistant", ctx, "ordinary", true, true, true},
		{"agent or guest", ctx, "ordinary", false, false, false},
		{"background supervision", withToolSet(ctx, json.RawMessage(`{"tool_set":"supervise"}`)), "ordinary", true, false, false},
		{"scheduled first plan", ctx, "scheduled", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.withToolLoading(tc.ctx, tc.self, tools, tc.assistant)
			if got[0].WorkGuideTool != tc.todo || got[1].WorkGuideTool != tc.plan || got[2].WorkGuideTool || got[3].WorkGuideTool {
				t.Fatal(got)
			}
		})
	}
	set(true, true)
	if !s.withToolLoading(ctx, "ordinary", tools, true)[0].WorkGuideTool {
		t.Fatal("full-loading preference overrode lab")
	}
	set(false, true)
	for _, tool := range s.withToolLoading(ctx, "ordinary", tools, true) {
		if tool.WorkGuideTool || tool.DeferLoading {
			t.Fatalf("rollback with load_all_tools: %+v", tool)
		}
	}
	set(false, false)
	b, _ := json.Marshal(baseline)
	off, _ := json.Marshal(s.withToolLoading(ctx, "ordinary", tools, true))
	if string(b) != string(off) || tools[0].WorkGuideTool {
		t.Fatal("rollback or copy", string(off), string(b))
	}
}

func TestWorkGuideScheduledPreflightKeepsOriginalTools(t *testing.T) {
	s := newFleetTest(t).s
	ctx := t.Context()
	s.noteCleanRound("grant", "task")
	tools := []proto.Tool{{Name: "task_plan"}}
	for name := range grantSafeTools {
		tools = append(tools, proto.Tool{Name: name})
	}
	before := s.withToolLoading(ctx, "grant", tools, true)
	setSettings(t, s, func(v *Settings) { v.WorkGuide = ptr(true) })
	after := s.withToolLoading(ctx, "grant", tools, true)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(b) != string(a) {
		t.Fatalf("grant preflight changed: %s -> %s", b, a)
	}
	if grantSafeTools["work_tools_search"] {
		t.Fatal("discovery should not authorize a scheduled dispatch")
	}
}
