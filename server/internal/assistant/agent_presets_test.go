package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent presets for Agents (#14672, agent_presets.go): an Agent starts another on a tier of the profile in use, as
// the assistant does with agent_start, and reads beforehand which presets each computer can start.

// presetsA2AFixture is memA2AFixture (an Agent on mac, A2A on its default scope) with Agent dispatch on and the starts
// answered: mac has Claude Code and box a Codex wrapper, gateway. The profile in use has review, which runs gateway
// and asks for Allow all, and plain, which runs claude.
func presetsA2AFixture(t *testing.T) (*Service, *a2aTestHub, *memA2AStore, a2aCaller) {
	t.Helper()
	s, h, st, parent, _ := memA2AFixture(t)
	h.fakeModel(t)
	on := true
	h.mu.Lock()
	h.hello.Agents.Dispatch.Enabled = &on
	h.startReply = func(args map[string]any, row *proto.Session) *proto.StartInstead {
		row.SessionID = args["session_id"].(string)
		return nil
	}
	h.devices = []Device{
		{ID: "mac", Name: "mac", Home: "/Users/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0"},
		{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true},
	}
	h.zshrc = map[string][]proto.Launcher{
		"mac": {{Name: "notes", Source: "zshrc"}},
		"box": {{Name: "gateway", Source: "zshrc", Agent: "codex"}},
	}
	h.mu.Unlock()
	saveTiers(t, s, map[string]ModelTier{
		"review": {Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast", PermissionMode: "bypassPermissions", Description: "Reviews a change before merge"},
		"plain":  {Launcher: "claude", Description: "Everyday work"},
	})
	return s, h, st, parent
}

// presetsCall is the Agent's call of an Agent tool (A2A or dispatch).
func presetsCall(ctx context.Context, s *Service, parent a2aCaller, tool, args string) proto.ToolResult {
	return s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, tool, json.RawMessage(args), false)
}

// presetsTaskID is the A2A task an a2a_send answer is about.
func presetsTaskID(t *testing.T, res proto.ToolResult) string {
	t.Helper()
	var view a2aTaskView
	if res.IsError || json.Unmarshal([]byte(resultText(res)), &view) != nil || view.ID == "" {
		t.Fatalf("a2a_send: %s", resultText(res))
	}
	return view.ID
}

// A launch on a tier runs the tier's launcher, model, effort and speed; the tier's permission mode is left out, so the
// launch keeps plan, and dispatch_start keeps its own rule. A launcher named with a tier that is not the tier's wins,
// and the tier gives nothing.
func TestAgentStartsAnotherOnATier(t *testing.T) {
	s, h, st, parent := presetsA2AFixture(t)
	ctx := t.Context()
	id := presetsTaskID(t, presetsCall(ctx, s, parent, "a2a_send",
		`{"machine_id":"box","cwd":"/code/app","tier":"review","message_id":"m-review","text":"Review the change."}`))
	processMemA2A(t, s, st, s.a2aConfig(ctx), id)
	waitFor(t, "the launch on the tier", func() bool { return len(startsOf(h.fakeHub)) == 1 })
	if got := startsOf(h.fakeHub)[0]; got["launcher"] != "gateway" || got["model"] != "gw-big" || got["effort"] != "max" ||
		got["speed"] != "fast" || got["permission_mode"] != "plan" {
		t.Fatalf("the launch did not run the preset in plan: %v", got)
	}

	id = presetsTaskID(t, presetsCall(ctx, s, parent, "a2a_send",
		`{"machine_id":"box","cwd":"/code/app","launcher":"codex","tier":"review","message_id":"m-own","text":"Review it with Codex."}`))
	processMemA2A(t, s, st, s.a2aConfig(ctx), id)
	waitFor(t, "the launch on its own launcher", func() bool { return len(startsOf(h.fakeHub)) == 2 })
	if got := startsOf(h.fakeHub)[1]; got["launcher"] != "codex" || !unset(got["model"]) || !unset(got["speed"]) || got["permission_mode"] != "plan" {
		t.Fatalf("a launcher of the caller's own took from the tier: %v", got)
	}

	res := presetsCall(ctx, s, parent, "dispatch_start",
		`{"machine":"box","cwd":"/code/app","prompt":"Review the change.","tier":"review","message_id":"d-review"}`)
	if res.IsError || !strings.Contains(resultText(res), "Preset review: runs on gateway · gw-big · max · fast.") {
		t.Fatalf("dispatch_start on a tier: %s", resultText(res))
	}
	got := startsOf(h.fakeHub)[2]
	if got["launcher"] != "gateway" || got["model"] != "gw-big" || got["effort"] != "max" || got["speed"] != "fast" {
		t.Fatalf("dispatch_start did not run the preset: %v", got)
	}
	if got["permission_mode"] == "bypassPermissions" {
		t.Fatalf("dispatch_start took the tier's permission mode: %v", got)
	}
}

// An unknown tier, and one whose launcher the computer lacks, are refused before anything is recorded or started, as
// agent_start refuses them: the refusal names the tiers there are, or the launchers the computer has.
func TestAgentStartOnAnUnknownOrMissingTierStartsNothing(t *testing.T) {
	s, h, st, parent := presetsA2AFixture(t)
	ctx := t.Context()
	h.mu.Lock()
	h.zshrc["box"] = []proto.Launcher{{Name: "other", Source: "zshrc", Agent: "codex"}} // gateway is not on box now
	h.mu.Unlock()
	for _, c := range []struct{ tool, args, id string }{
		{"a2a_send", `{"machine_id":"box","cwd":"/code/app","tier":"nope","message_id":"m-nope","text":"Review."}`, "m-nope"},
		{"dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review.","tier":"nope","message_id":"d-nope"}`, "d-nope"},
	} {
		res := presetsCall(ctx, s, parent, c.tool, c.args)
		if text := resultText(res); !res.IsError || !strings.Contains(text, "has no tier nope") || !strings.Contains(text, "plain, review") ||
			!strings.Contains(text, "nothing was started") {
			t.Errorf("%s on an unknown tier: %s", c.tool, text)
		}
		if row, err := st.A2ARequest(ctx, parent.Owner, c.id); err != nil || row != nil {
			t.Errorf("%s on an unknown tier recorded a task: %+v %v", c.tool, row, err)
		}
	}
	for _, c := range []struct{ tool, args, id string }{
		{"a2a_send", `{"machine_id":"box","cwd":"/code/app","tier":"review","message_id":"m-missing","text":"Review."}`, "m-missing"},
		{"dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review.","tier":"review","message_id":"d-missing"}`, "d-missing"},
	} {
		res := presetsCall(ctx, s, parent, c.tool, c.args)
		if text := resultText(res); !res.IsError || !strings.Contains(text, "gateway") || !strings.Contains(text, "other") ||
			!strings.Contains(text, "nothing was started") {
			t.Errorf("%s on a tier whose launcher box lacks: %s", c.tool, text)
		}
		if row, err := st.A2ARequest(ctx, parent.Owner, c.id); err != nil || row != nil {
			t.Errorf("%s on a tier whose launcher box lacks recorded a task: %+v %v", c.tool, row, err)
		}
	}
	if starts := startsOf(h.fakeHub); len(starts) != 0 {
		t.Fatalf("a refused tier started an Agent: %v", starts)
	}
}

// A retry by message_id matches only with the tier the launch named, and is not resolved again: with the tier gone
// from the profile it still finds its task.
func TestAgentStartRetryMatchesOnTheTier(t *testing.T) {
	s, h, _, parent := presetsA2AFixture(t)
	ctx := t.Context()
	send := `{"machine_id":"box","cwd":"/code/app","tier":"review","message_id":"m-retry","text":"Review."}`
	id := presetsTaskID(t, presetsCall(ctx, s, parent, "a2a_send", send))
	if again := presetsTaskID(t, presetsCall(ctx, s, parent, "a2a_send", send)); again != id {
		t.Fatalf("a retry on the same tier made task %s, want %s", again, id)
	}
	for _, other := range []string{
		`{"machine_id":"box","cwd":"/code/app","tier":"plain","message_id":"m-retry","text":"Review."}`,
		`{"machine_id":"box","cwd":"/code/app","message_id":"m-retry","text":"Review."}`,
	} {
		if res := presetsCall(ctx, s, parent, "a2a_send", other); !res.IsError || !strings.Contains(resultText(res), "message_id") {
			t.Fatalf("a retry naming another tier matched: %s", resultText(res))
		}
	}

	dispatch := `{"machine":"box","cwd":"/code/app","prompt":"Review the change.","tier":"review","message_id":"d-retry"}`
	first := presetsCall(ctx, s, parent, "dispatch_start", dispatch)
	if first.IsError {
		t.Fatalf("dispatch_start: %s", resultText(first))
	}
	started := len(startsOf(h.fakeHub))
	if again := presetsCall(ctx, s, parent, "dispatch_start", dispatch); again.IsError || len(startsOf(h.fakeHub)) != started {
		t.Fatalf("a dispatch retry on the same tier: %s, starts %d", resultText(again), len(startsOf(h.fakeHub)))
	}
	other := strings.Replace(dispatch, `"tier":"review"`, `"tier":"plain"`, 1)
	if res := presetsCall(ctx, s, parent, "dispatch_start", other); !res.IsError || !strings.Contains(resultText(res), "message_id") ||
		len(startsOf(h.fakeHub)) != started {
		t.Fatalf("a dispatch retry naming another tier matched: %s", resultText(res))
	}

	saveTiers(t, s, map[string]ModelTier{"plain": {Launcher: "claude"}})
	if again := presetsTaskID(t, presetsCall(ctx, s, parent, "a2a_send", send)); again != id {
		t.Fatalf("a retry after the tier was removed made task %s, want %s", again, id)
	}
	if again := presetsCall(ctx, s, parent, "dispatch_start", dispatch); again.IsError || len(startsOf(h.fakeHub)) != started {
		t.Fatalf("a dispatch retry after the tier was removed: %s", resultText(again))
	}
}

// Agents read the presets on request: a2a_sessions with presets for every computer a launch can go to (its own
// included), dispatch_read's overview for every computer a task can go to. Each lists only the presets whose launcher
// that computer has, with what it runs and its description; without presets a2a_sessions answers as before.
func TestAgentsSeeThePresetsEachComputerCanStart(t *testing.T) {
	s, h, _, parent := presetsA2AFixture(t)
	ctx := t.Context()
	h.mu.Lock()
	h.devices = append(h.devices,
		Device{ID: "l4", Name: "l4", Home: "/home/me", Online: true, Reachable: true},       // only a launcher no tier runs
		Device{ID: "flaky", Name: "flaky", Home: "/home/me", Online: true, Reachable: true}, // its list cannot be read
		Device{ID: "off", Name: "off", Home: "/home/me"})
	h.sessions["l4"], h.sessions["flaky"] = nil, nil
	h.zshrc["l4"] = []proto.Launcher{{Name: "other", Source: "zshrc"}}
	h.mu.Unlock()
	saveTiers(t, s, map[string]ModelTier{
		"review": {Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast", Description: "Reviews a change before merge"},
		"plain":  {Launcher: "claude", Description: "Everyday work"},
		"cloud":  {Launcher: "nowhere", Description: "On no computer"},
	})

	var peers []map[string]any
	if res := presetsCall(ctx, s, parent, "a2a_sessions", `{}`); res.IsError || json.Unmarshal([]byte(resultText(res)), &peers) != nil {
		t.Fatalf("a2a_sessions without presets changed its answer: %s", resultText(res))
	}
	type preset struct{ Name, Launcher, Model, Effort, Speed, Description string }
	var listed struct {
		Sessions  []map[string]any `json:"sessions"`
		Computers []struct {
			Machine string   `json:"machine_id"`
			Online  bool     `json:"online"`
			Presets []preset `json:"presets"`
			Unread  bool     `json:"presets_unread"`
		} `json:"computers"`
	}
	res := presetsCall(ctx, s, parent, "a2a_sessions", `{"presets":true}`)
	if res.IsError || json.Unmarshal([]byte(resultText(res)), &listed) != nil || len(listed.Sessions) != len(peers) {
		t.Fatalf("a2a_sessions with presets: %s", resultText(res))
	}
	byMachine := map[string]int{}
	for i, c := range listed.Computers {
		byMachine[c.Machine] = i
	}
	want := map[string][]preset{
		"mac": {{Name: "plain", Launcher: "claude", Description: "Everyday work"}},
		"box": {{Name: "review", Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast", Description: "Reviews a change before merge"}},
		"l4":  nil,
	}
	for machine, presets := range want {
		i, ok := byMachine[machine]
		if !ok || !listed.Computers[i].Online || listed.Computers[i].Unread || len(listed.Computers[i].Presets) != len(presets) {
			t.Fatalf("%s: %s", machine, resultText(res))
		}
		for j, p := range presets {
			if listed.Computers[i].Presets[j] != p {
				t.Fatalf("%s's presets: %+v, want %+v", machine, listed.Computers[i].Presets, presets)
			}
		}
	}
	if i, ok := byMachine["flaky"]; !ok || !listed.Computers[i].Unread || len(listed.Computers[i].Presets) != 0 {
		t.Fatalf("an unreadable list is not said: %s", resultText(res))
	}
	if i, ok := byMachine["off"]; !ok || listed.Computers[i].Online || listed.Computers[i].Unread {
		t.Fatalf("an offline computer: %s", resultText(res))
	}
	if strings.Contains(resultText(res), "cloud") {
		t.Fatalf("a preset no computer can start is listed: %s", resultText(res))
	}
	one := presetsCall(ctx, s, parent, "a2a_sessions", `{"machine_id":"box","presets":true}`)
	if text := resultText(one); one.IsError || !strings.Contains(text, `"machine_id":"box"`) || strings.Contains(text, `"machine_id":"mac"`) {
		t.Fatalf("a2a_sessions with presets on one computer: %s", text)
	}

	out := resultText(presetsCall(ctx, s, parent, "dispatch_read", `{}`))
	for _, line := range []string{
		"- box (id box): online, /home/me\n  - tier review: gateway · gw-big · max · fast (Reviews a change before merge)\n- l4 (id l4): online",
		"- l4 (id l4): online, /home/me\n- flaky (id flaky): online, /home/me\n  - its Agent presets could not be read this time\n- off (id off): offline",
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("dispatch_read's overview lacks %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, "tier plain") || strings.Contains(out, "tier cloud") {
		t.Fatalf("the overview lists a preset no computer a task can go to has:\n%s", out)
	}
	s.recordHealth(ctx, "box", "gateway", healthResult{err: "authentication failed", source: healthSourceStart, kind: errKindAuth})
	if out = resultText(presetsCall(ctx, s, parent, "dispatch_read", `{}`)); !strings.Contains(out,
		"  - tier review: gateway · gw-big · max · fast (Reviews a change before merge); unavailable now: its launcher's latest check failed\n") {
		t.Fatalf("a preset whose launcher fails is not marked:\n%s", out)
	}
}
