package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestModelBindingPermissionModeEditInheritAndValidate(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.hello.Models.AI.API = "messages"
	f.hello.Models.AI.BaseURL = "https://api.example.invalid"
	f.hello.Models.AI.Model = "legacy-model"
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "codex", Agent: "codex", Source: "zshrc"},
			{Name: "agy", Agent: "agy", Source: "agy", Assistant: true},
			{Name: "omp", Agent: "omp", Source: "zshrc"},
			{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions")},
		},
	}
	f.mu.Unlock()
	putLaunchers(t, s, apiProvider(t, s, "direct", "messages"), storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cc-alt", Model: "alt-model"}})

	// Launcher purposes expose permission_mode; direct API purposes do not.
	for _, id := range []string{"assistant", "agent.new", purposeSupervise, purposeMeetingRoom, purposeAgent, purposeLarkAgent} {
		p := purposeOf(t, s, id)
		if !slices.Contains(p.Fields, "permission_mode") {
			t.Fatalf("launcher purpose %s missing permission_mode in fields: %v", id, p.Fields)
		}
	}
	for _, id := range []string{"kb", "inbox.triage", purposeMeetingMinutes} {
		p := purposeOf(t, s, id)
		if slices.Contains(p.Fields, "permission_mode") {
			t.Fatalf("API purpose %s unexpectedly exposes permission_mode in fields: %v", id, p.Fields)
		}
		if w := modelRequest(s, http.MethodPatch, id, `{"revision":0,"permission_mode":"acceptEdits"}`); w.Code != http.StatusBadRequest {
			t.Fatalf("API purpose %s accepted permission_mode: %d %s", id, w.Code, w.Body.String())
		}
	}

	// Setting permission_mode on assistant is inherited by assistant.supervise and does not alter fingerprint.
	fpBefore, err := s.modelFingerprint(ctx, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"permission_mode":"acceptEdits"}`); w.Code != http.StatusOK {
		t.Fatalf("save assistant permission_mode: %d %s", w.Code, w.Body.String())
	}
	fpAfter, err := s.modelFingerprint(ctx, "assistant")
	if err != nil || fpAfter != fpBefore {
		t.Fatalf("permission_mode changed fingerprint: before=%q after=%q err=%v", fpBefore, fpAfter, err)
	}
	if p := purposeOf(t, s, purposeSupervise); p.Effective.PermissionMode != "acceptEdits" || p.Override.PermissionMode != "" {
		t.Fatalf("supervisor did not inherit assistant permission_mode: %+v", p)
	}

	// Child override wins over parent; changing provider resets permission_mode; null restores inheritance.
	if w := modelRequest(s, http.MethodPatch, purposeSupervise, `{"revision":0,"permission_mode":"plan"}`); w.Code != http.StatusOK {
		t.Fatalf("save supervisor permission_mode: %d %s", w.Code, w.Body.String())
	}
	if p := purposeOf(t, s, purposeSupervise); p.Effective.PermissionMode != "plan" || p.Override.PermissionMode != "plan" {
		t.Fatalf("supervisor override: %+v", p)
	}
	if w := modelRequest(s, http.MethodPatch, purposeSupervise, `{"revision":1,"provider":"launcher.cc-alt"}`); w.Code != http.StatusOK {
		t.Fatalf("change supervisor provider: %d %s", w.Code, w.Body.String())
	}
	if p := purposeOf(t, s, purposeSupervise); p.Effective.PermissionMode != "" || p.Override.PermissionMode != "" {
		t.Fatalf("changing provider did not reset permission_mode: %+v", p)
	}
	if w := modelRequest(s, http.MethodPatch, purposeSupervise, `{"revision":2,"provider":null,"permission_mode":null}`); w.Code != http.StatusOK {
		t.Fatalf("reset supervisor: %d %s", w.Code, w.Body.String())
	}
	if p := purposeOf(t, s, purposeSupervise); p.Effective.PermissionMode != "acceptEdits" {
		t.Fatalf("reset supervisor did not re-inherit permission_mode: %+v", p)
	}

	// Capability validation across claude, codex, agy, omp, and restricted runtime provider.
	if w := modelRequest(s, http.MethodPatch, "agent.new", `{"revision":0,"permission_mode":"yolo"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid permission_mode accepted: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, purposeAgent, `{"revision":0,"permission_mode":"plan"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("Inbox accepted Plan, which rejects its required Persona MCP calls: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "agent.new", `{"revision":0,"provider":"launcher.omp","permission_mode":"acceptEdits"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("omp accepted permission_mode: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "agent.new", `{"revision":0,"provider":"launcher.grok","permission_mode":"plan"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("restricted runtime provider accepted unsupported mode: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "agent.new", `{"revision":0,"provider":"launcher.grok","permission_mode":"bypassPermissions"}`); w.Code != http.StatusOK {
		t.Fatalf("restricted runtime provider rejected supported mode: %d %s", w.Code, w.Body.String())
	}
	rev := purposeOf(t, s, "agent.new").Revision
	for _, tc := range []struct{ provider, mode string }{
		{"launcher.codex", "acceptEdits"},
		{"launcher.agy", "auto"},
		{"launcher.claude", "plan"},
	} {
		body := `{"revision":` + string(rune('0'+rev)) + `,"provider":"` + tc.provider + `","permission_mode":"` + tc.mode + `"}`
		if w := modelRequest(s, http.MethodPatch, "agent.new", body); w.Code != http.StatusOK {
			t.Fatalf("%s/%s rejected: %d %s", tc.provider, tc.mode, w.Code, w.Body.String())
		}
		rev++
	}
}

func TestModelProfileAndTierPermissionMode(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "omp", Agent: "omp", Source: "zshrc"}, {Name: "codex", Agent: "codex", Source: "zshrc"}}}
	f.mu.Unlock()
	putLaunchers(t, s, apiProvider(t, s, "direct", "messages"))

	// Reject permission_mode on API purpose in profile and on omp tier.
	badAPI := ModelProfile{ID: "bad-api", Name: "Bad API", Purposes: map[string]ModelBinding{
		"kb": {Provider: "launcher.direct", PermissionMode: "acceptEdits"},
	}}
	if err := checkModelProfile(&badAPI, s.storedLaunchers(ctx)); err == nil {
		t.Fatal("API purpose in profile accepted permission_mode")
	}
	badOmp := ModelProfile{ID: "bad-omp", Name: "Bad OMP", Tiers: map[string]ModelTier{
		"fast": {Launcher: "omp", PermissionMode: "acceptEdits"},
	}}
	if err := checkModelProfile(&badOmp, s.storedLaunchers(ctx)); err == nil {
		t.Fatal("omp tier accepted permission_mode")
	}

	// Save, switch, and rollback a profile carrying permission_mode on purposes and tiers.
	storeProfiles(t, st, ModelProfile{
		ID:   "safe",
		Name: "Safe",
		Purposes: map[string]ModelBinding{
			"assistant": {Provider: "launcher.claude", PermissionMode: "acceptEdits"},
			"agent.new": {Provider: "launcher.codex", PermissionMode: "plan"},
		},
		Tiers: map[string]ModelTier{
			"review": {Launcher: "claude", Model: "sonnet", PermissionMode: "plan", Description: "Read and review"},
		},
	})
	if _, err := s.applyModelProfile(ctx, "safe", ProfileActor{Kind: "hub"}); err != nil {
		t.Fatalf("apply safe profile: %v", err)
	}
	if b, err := s.resolveModel(ctx, "assistant"); err != nil || b.PermissionMode != "acceptEdits" {
		t.Fatalf("assistant binding after switch: %+v %v", b, err)
	}
	if b, err := s.resolveModel(ctx, "agent.new"); err != nil || b.PermissionMode != "plan" {
		t.Fatalf("agent.new binding after switch: %+v %v", b, err)
	}
	snap := s.modelProfileContext(ctx)
	if snap == nil || len(snap.Tiers) != 1 || snap.Tiers[0].PermissionMode != "plan" {
		t.Fatalf("snapshot tiers missing permission_mode: %+v", snap)
	}
	rendered := prompts.RenderSnapshot(prompts.Snapshot{ModelProfile: snap, Now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), Zone: "UTC"})
	if !strings.Contains(rendered, "review = claude · sonnet · plan（Read and review）") {
		t.Fatalf("snapshot did not render tier permission_mode:\n%s", rendered)
	}

	// Rollback restores prior empty permission_mode.
	if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if b, err := s.resolveModel(ctx, "assistant"); err != nil || b.PermissionMode != "" {
		t.Fatalf("assistant binding after rollback: %+v %v", b, err)
	}

	// editTiers supports setting permission_mode and clears it when launcher changes.
	ps, _, err := s.readModelProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mode := "auto"
	changes, err := editTiers(&ps, tierEdit{Tier: "review", PermissionMode: &mode, Profiles: []string{"safe"}}, time.Now().UTC())
	if err != nil || len(changes) != 1 || changes[0].After.PermissionMode != "auto" {
		t.Fatalf("editTiers permission_mode: changes=%+v err=%v", changes, err)
	}
	nextLauncher := "codex"
	changes, err = editTiers(&ps, tierEdit{Tier: "review", Launcher: &nextLauncher, Profiles: []string{"safe"}}, time.Now().UTC())
	if err != nil || len(changes) != 1 || changes[0].After.PermissionMode != "" {
		t.Fatalf("editTiers launcher switch did not clear permission_mode: changes=%+v err=%v", changes, err)
	}
}

func TestPermissionModeLaunchPropagationAndPrecedence(t *testing.T) {
	ctx := context.Background()

	// 1. AgentStartParams (New Agent): explicit > tier > agent.new > CLI default; omp drops inherited mode.
	t.Run("AgentStartParams precedence and capability filtering", func(t *testing.T) {
		s, f := newQuietServiceOn(t, &memStore{})
		f.launcherList = []proto.Launcher{
			{Name: "claude", Agent: "claude"},
			{Name: "codex", Agent: "codex"},
			{Name: "agy", Agent: "agy"},
			{Name: "omp", Agent: "omp"},
		}
		setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"})
		putTierProfiles(t, s, "performance", nil, map[string]ModelTier{
			"guarded":  {Launcher: "claude", PermissionMode: "plan"},
			"agy-auto": {Launcher: "agy", PermissionMode: "auto"},
		})

		// Inherits agent.new permission_mode
		got, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{}})
		if err != nil || got["permission_mode"] != "acceptEdits" {
			t.Fatalf("agent.new default: %v err=%v", got, err)
		}
		// Tier overrides agent.new
		got, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{"tier": "guarded"}})
		if err != nil || got["permission_mode"] != "plan" {
			t.Fatalf("tier override: %v err=%v", got, err)
		}
		// Explicit caller overrides tier
		got, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{"tier": "guarded", "permission_mode": "default"}})
		if err != nil || got["permission_mode"] != "default" {
			t.Fatalf("explicit override: %v err=%v", got, err)
		}
		// AGY tier propagates permission_mode
		got, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{"tier": "agy-auto"}})
		if err != nil || got["permission_mode"] != "auto" || got["agent_type"] != "agy" {
			t.Fatalf("agy tier: %v err=%v", got, err)
		}
		// Switching to omp drops inherited mode without error
		setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.omp"})
		got, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{}})
		if err != nil || got["permission_mode"] != nil {
			t.Fatalf("omp inherited mode: %v err=%v", got, err)
		}
	})

	// 2. agent_start: explicit > tier > agent.new > allowAll
	t.Run("agent_start precedence across Claude, Codex and AGY", func(t *testing.T) {
		runStart := func(t *testing.T, binding ModelBinding, tiers map[string]ModelTier, input map[string]any) map[string]any {
			t.Helper()
			s, f := newQuietServiceOn(t, &memStore{})
			f.launcherList = []proto.Launcher{
				{Name: "claude", Agent: "claude"},
				{Name: "codex", Agent: "codex"},
				{Name: "agy", Agent: "agy"},
			}
			setModel(t, s, "agent.new", binding)
			for name, tier := range tiers {
				saveTier(t, s, name, tier)
			}
			input["machine"], input["cwd"], input["prompt"] = "box", "/code/app", "Run check"
			args, _ := json.Marshal(input)
			if _, err := s.runTool(ctx, "server", "conv1", "agent_start", args); err != nil {
				t.Fatalf("agent_start failed: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.starts) != 1 {
				t.Fatalf("starts = %v", f.starts)
			}
			return f.starts[0]
		}

		// Purpose binding applies to Claude, Codex, AGY
		if start := runStart(t, ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"}, nil, map[string]any{}); start["permission_mode"] != "acceptEdits" {
			t.Fatalf("claude agent.new permission_mode: %v", start)
		}
		if start := runStart(t, ModelBinding{Provider: "launcher.codex", PermissionMode: "bypassPermissions"}, nil, map[string]any{}); start["permission_mode"] != "bypassPermissions" {
			t.Fatalf("codex agent.new permission_mode: %v", start)
		}
		if start := runStart(t, ModelBinding{Provider: "launcher.agy", PermissionMode: "auto"}, nil, map[string]any{}); start["permission_mode"] != "auto" {
			t.Fatalf("agy agent.new permission_mode: %v", start)
		}
		// AGY with no mode anywhere runs in Allow all as Claude Code does: its worker would otherwise stop at agy's own
		// approval prompt in the pane. A mode the call names stays.
		if start := runStart(t, ModelBinding{Provider: "launcher.agy"}, nil, map[string]any{}); start["permission_mode"] != "bypassPermissions" {
			t.Fatalf("agy default permission_mode: %v", start)
		}
		if start := runStart(t, ModelBinding{Provider: "launcher.agy"}, nil, map[string]any{"permission_mode": "acceptEdits"}); start["permission_mode"] != "acceptEdits" {
			t.Fatalf("agy explicit permission_mode: %v", start)
		}
		// Tier overrides agent.new
		if start := runStart(t,
			ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"},
			map[string]ModelTier{"careful": {Launcher: "claude", PermissionMode: "plan"}},
			map[string]any{"tier": "careful"},
		); start["permission_mode"] != "plan" {
			t.Fatalf("tier override: %v", start)
		}
		// Explicit overrides tier
		if start := runStart(t,
			ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"},
			map[string]ModelTier{"careful": {Launcher: "claude", PermissionMode: "plan"}},
			map[string]any{"tier": "careful", "permission_mode": "default"},
		); start["permission_mode"] != "default" {
			t.Fatalf("explicit override: %v", start)
		}
	})

	// 3. task_plan & startTaskAgent: writable honors precedence; readonly Codex NEVER weakened.
	t.Run("task dispatch honors precedence and protects readonly", func(t *testing.T) {
		s, f := noModesService(t, "codex")
		f.launcherList = []proto.Launcher{
			{Name: "claude", Agent: "claude"},
			{Name: "codex", Agent: "codex"},
			{Name: "agy", Agent: "agy"},
		}
		setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"})
		putTierProfiles(t, s, "performance", nil, map[string]ModelTier{
			"codex-bypass": {Launcher: "codex", PermissionMode: "bypassPermissions"},
			"agy-auto":     {Launcher: "agy", PermissionMode: "auto"},
		})

		// Writable task on Claude inherits agent.new permission_mode
		wTask := readonlyTestTask("")
		wTask.Readonly = false
		if _, _, err := s.startTaskAgent(ctx, &wTask, false); err != nil {
			t.Fatal(err)
		}
		if got := f.starts[len(f.starts)-1]["permission_mode"]; got != "acceptEdits" {
			t.Fatalf("writable claude task got %v, want acceptEdits", got)
		}

		// Writable task on AGY tier propagates auto
		agyTask := readonlyTestTask("")
		agyTask.Readonly, agyTask.Tier = false, "agy-auto"
		if _, _, err := s.startTaskAgent(ctx, &agyTask, false); err != nil {
			t.Fatal(err)
		}
		if got := f.starts[len(f.starts)-1]["permission_mode"]; got != "auto" {
			t.Fatalf("writable agy task got %v, want auto", got)
		}

		// Read-only task on Codex tier with bypassPermissions MUST remain plan!
		roCodex := readonlyTestTask("")
		roCodex.Readonly, roCodex.Tier = true, "codex-bypass"
		if _, _, err := s.startTaskAgent(ctx, &roCodex, false); err != nil {
			t.Fatal(err)
		}
		if got := f.starts[len(f.starts)-1]["permission_mode"]; got != "plan" || roCodex.Permission != "plan" {
			t.Fatalf("readonly codex task weakened: launch=%v task=%+v", got, roCodex)
		}
	})

	// 4. Headless assistant, supervisor, lark.agent, inbox.agent (safety boundary!), and scheduled task
	t.Run("headless purposes and safety boundaries", func(t *testing.T) {
		s, f, _ := modelService(t)
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"})
		setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", PermissionMode: "auto"})
		setModel(t, s, purposeAgent, ModelBinding{Provider: "launcher.claude", PermissionMode: "bypassPermissions"})

		// Main chat inherits assistant permission_mode
		sess, err := s.startOn(ctx, "server", StartParams{Text: "hi"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if sess.PermissionMode != "acceptEdits" || f.sends[len(f.sends)-1]["permission_mode"] != "acceptEdits" {
			t.Fatalf("assistant start mode: sess=%q send=%v", sess.PermissionMode, f.sends[len(f.sends)-1])
		}

		// Background supervisor inherits assistant permission_mode
		sess, err = s.startOn(ctx, "server", StartParams{Text: "review", Purpose: purposeSupervise}, false)
		if err != nil {
			t.Fatal(err)
		}
		if sess.PermissionMode != "acceptEdits" || f.sends[len(f.sends)-1]["permission_mode"] != "acceptEdits" {
			t.Fatalf("supervisor start mode: sess=%q send=%v", sess.PermissionMode, f.sends[len(f.sends)-1])
		}

		// Explicit caller mode beats purpose binding
		sess, err = s.startOn(ctx, "server", StartParams{Text: "hi", PermissionMode: "plan"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if sess.PermissionMode != "plan" || f.sends[len(f.sends)-1]["permission_mode"] != "plan" {
			t.Fatalf("explicit startOn mode: sess=%q send=%v", sess.PermissionMode, f.sends[len(f.sends)-1])
		}

		// Lark /agent inherits agent.new permission_mode
		sess, err = s.startOn(ctx, "server", StartParams{Text: "slash", Purpose: purposeLarkAgent}, false)
		if err != nil {
			t.Fatal(err)
		}
		if sess.PermissionMode != "auto" || f.sends[len(f.sends)-1]["permission_mode"] != "auto" {
			t.Fatalf("lark.agent start mode: sess=%q send=%v", sess.PermissionMode, f.sends[len(f.sends)-1])
		}

		// OriginInbox uses only the AI twin's own saved override, never an inherited Agents mode.
		sess, err = s.startOn(ctx, "server", StartParams{Text: "triage", Purpose: purposeAgent, Origin: OriginInbox}, false)
		if err != nil {
			t.Fatal(err)
		}
		if sess.PermissionMode != proto.ModeBypass || f.sends[len(f.sends)-1]["permission_mode"] != proto.ModeBypass {
			t.Fatalf("OriginInbox lost its own saved mode: sess=%q send=%v", sess.PermissionMode, f.sends[len(f.sends)-1])
		}

		// Guest conversation MUST never inherit or receive a non-empty permission_mode
		sess, err = s.startOn(ctx, "server", StartParams{Text: "room", Purpose: purposeMeetingRoom, GuestTools: []string{"foo"}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode, _ := f.sends[len(f.sends)-1]["permission_mode"].(string); sess.PermissionMode != "" || gotMode != "" {
			t.Fatalf("guest session received permission_mode: sess=%q send=%v", sess.PermissionMode, f.sends[len(f.sends)-1])
		}

		// Ongoing supervisor message (backgroundModel) must NOT inject permission_mode
		msg := map[string]any{"session_id": "bg-1", "text": "update"}
		s.backgroundModel(ctx, msg)
		if _, has := msg["permission_mode"]; has {
			t.Fatalf("backgroundModel injected permission_mode into existing chat: %v", msg)
		}

		// Scheduled task inherits assistant permission_mode unless task explicitly sets PermissionMode
		ex, err := s.taskExecution(ctx, Task{ID: "t1"}, false, ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"})
		if err != nil || ex.PermissionMode != "acceptEdits" {
			t.Fatalf("scheduled task inherited mode: %+v err=%v", ex, err)
		}
		exExplicit, err := s.taskExecution(ctx, Task{ID: "t1", PermissionMode: "plan"}, false, ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"})
		if err != nil || exExplicit.PermissionMode != "plan" {
			t.Fatalf("scheduled task explicit mode: %+v err=%v", exExplicit, err)
		}

		// sendOnPick with empty pick.Model must NOT inject assistant purpose's permission_mode into an existing conversation.
		if err := s.setModelPick(ctx, "conv-pick", &modelPick{MachineID: "server", Launcher: "claude"}); err != nil {
			t.Fatal(err)
		}
		pickParams := map[string]any{"session_id": "conv-pick", "text": "follow up"}
		if err := s.sendModel(ctx, "server", "conv-pick", pickParams, i18n.En); err != nil {
			t.Fatalf("sendModel on saved pick: %v", err)
		}
		if _, has := pickParams["permission_mode"]; has {
			t.Fatalf("sendOnPick injected permission_mode into existing chat: %v", pickParams)
		}
		pickExplicit := map[string]any{"session_id": "conv-pick", "text": "follow up", "permission_mode": "plan"}
		if err := s.sendModel(ctx, "server", "conv-pick", pickExplicit, i18n.En); err != nil {
			t.Fatalf("sendModel on saved pick with explicit mode: %v", err)
		}
		if pickExplicit["permission_mode"] != "plan" {
			t.Fatalf("sendOnPick clobbered explicit permission_mode: %v", pickExplicit)
		}
		_ = i18n.En
	})
}

func TestPermissionModePerMachineResolution(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.sessions["mac"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "grok", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions")},
			{Name: "custom-cli", Agent: "omp", Source: "zshrc"},
		},
		"mac": {
			{Name: "grok", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf("default", "plan")},
			{Name: "custom-cli", Agent: "claude", Source: "zshrc"},
		},
	}
	f.mu.Unlock()

	unionGrok := &ModelProvider{
		ID:              "launcher.grok",
		Kind:            "launcher",
		Agent:           "paseo",
		PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions", "plan"),
	}
	if modes := s.permissionModesForLauncher(ctx, "server", "grok", unionGrok); slices.Contains(modes, "plan") || !slices.Contains(modes, "bypassPermissions") {
		t.Fatalf("server grok modes = %v, want only server's [default bypassPermissions]", modes)
	}
	if modes := s.permissionModesForLauncher(ctx, "mac", "grok", unionGrok); slices.Contains(modes, "bypassPermissions") || !slices.Contains(modes, "plan") {
		t.Fatalf("mac grok modes = %v, want only mac's [default plan]", modes)
	}

	unionCustom := &ModelProvider{ID: "launcher.custom-cli", Kind: "launcher", Agent: "claude"}
	if modes := s.permissionModesForLauncher(ctx, "server", "custom-cli", unionCustom); len(modes) != 0 {
		t.Fatalf("server custom-cli (omp) modes = %v, want empty", modes)
	}
	if modes := s.permissionModesForLauncher(ctx, "mac", "custom-cli", unionCustom); !slices.Contains(modes, "acceptEdits") {
		t.Fatalf("mac custom-cli (claude) modes = %v, want standard modes", modes)
	}

	// assistant is bound to "server": saving grok with mac-only "plan" must fail with 400, while server's "bypassPermissions" succeeds.
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.grok","permission_mode":"plan"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("assistant on server accepted mac-only grok mode: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.grok","permission_mode":"bypassPermissions"}`); w.Code != http.StatusOK {
		t.Fatalf("assistant on server rejected server-supported grok mode: %d %s", w.Code, w.Body.String())
	}
}

func TestPermissionModeManagedShadowingAndHubOverride(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.sessions["mac"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			// Same-named Claude-like local definition shadowed by an in-scope managed omp provider.
			{Name: "shadow-omp", Agent: "claude", Source: "zshrc"},
			// Reverse collision: local omp definition shadowed by an in-scope managed Claude provider.
			{Name: "shadow-claude", Agent: "omp", Source: "zshrc"},
			// Scoped managed omp provider is only active on mac, so server keeps its local Claude launcher.
			{Name: "scoped-omp", Agent: "claude", Source: "zshrc"},
			// Discovered omp launcher with a partial hub.json override (AgentType/Agent unset).
			{Name: "override-zshrc-omp", Agent: "omp", Source: "zshrc"},
			// Local omp launcher shadowed by an in-scope managed Messages API provider + partial hub.json override.
			{Name: "shadow-api", Agent: "omp", Source: "zshrc"},
			// Runtime launcher whose AgentType is explicitly overridden to omp in hub.json.
			{Name: "override-to-omp", Agent: "paseo", Source: "paseo", PermissionModes: proto.PermissionModeSetOf("default", "plan")},
		},
		"mac": {
			{Name: "scoped-omp", Agent: "claude", Source: "zshrc"},
		},
	}
	f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
		"server": {
			{Launcher: proto.Launcher{Name: "shadow-omp", BaseURL: "https://override.example"}},
			{Launcher: proto.Launcher{Name: "override-zshrc-omp", Model: "custom-model"}},
			{Launcher: proto.Launcher{Name: "shadow-api", BaseURL: "https://api-override.example"}},
			{Launcher: proto.Launcher{Name: "override-to-omp"}, AgentType: "omp"},
		},
	}
	f.mu.Unlock()

	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "shadow-omp", Agent: "omp", BaseURL: "https://omp.example"}, Share: "all"},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "shadow-claude", Agent: "claude", BaseURL: "https://claude.example"}, Share: "all"},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "scoped-omp", Agent: "omp", BaseURL: "https://scoped.example"}, Share: "selected", ScopeMachines: []string{"mac"}},
		apiProvider(t, s, "shadow-api", "messages"),
	)

	// In-scope managed omp provider shadows local Claude launcher AND preserves omp across partial hub.json override.
	if modes := s.permissionModesForLauncher(ctx, "server", "shadow-omp", &ModelProvider{ID: "launcher.shadow-omp", Kind: "launcher", Agent: "omp"}); len(modes) != 0 {
		t.Fatalf("managed shadow-omp with local claude + partial hub.json override modes = %v, want empty", modes)
	}
	// Reverse collision: in-scope managed Claude provider shadows local omp launcher.
	if modes := s.permissionModesForLauncher(ctx, "server", "shadow-claude", &ModelProvider{ID: "launcher.shadow-claude", Kind: "launcher", Agent: "claude"}); !slices.Contains(modes, "acceptEdits") {
		t.Fatalf("managed shadow-claude shadowing local omp modes = %v, want standard Claude modes", modes)
	}
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.shadow-claude","permission_mode":"acceptEdits"}`); w.Code != http.StatusOK {
		t.Fatalf("assistant rejected valid Claude mode on managed shadow-claude: %d %s", w.Code, w.Body.String())
	}
	// Out-of-scope managed omp provider does not shadow server's local Claude launcher, but does shadow mac's.
	if modes := s.permissionModesForLauncher(ctx, "server", "scoped-omp", &ModelProvider{ID: "launcher.scoped-omp", Kind: "launcher", Agent: "omp"}); !slices.Contains(modes, "acceptEdits") {
		t.Fatalf("server out-of-scope scoped-omp modes = %v, want local Claude modes", modes)
	}
	if modes := s.permissionModesForLauncher(ctx, "mac", "scoped-omp", &ModelProvider{ID: "launcher.scoped-omp", Kind: "launcher", Agent: "omp"}); len(modes) != 0 {
		t.Fatalf("mac in-scope scoped-omp modes = %v, want empty", modes)
	}
	// Partial hub.json override (AgentType and Agent unset) preserves discovered omp agent.
	if modes := s.permissionModesForLauncher(ctx, "server", "override-zshrc-omp", nil); len(modes) != 0 {
		t.Fatalf("override-zshrc-omp modes = %v, want empty", modes)
	}
	// Managed Messages API provider shadows local omp launcher and survives partial hub.json override.
	if modes := s.permissionModesForLauncher(ctx, "server", "shadow-api", nil); !slices.Contains(modes, "acceptEdits") {
		t.Fatalf("shadow-api modes = %v, want standard Claude modes", modes)
	}
	// Explicit hub.json AgentType override to omp clears inherited runtime permission modes.
	if modes := s.permissionModesForLauncher(ctx, "server", "override-to-omp", nil); len(modes) != 0 {
		t.Fatalf("override-to-omp modes = %v, want empty", modes)
	}
}

func TestPermissionModeStaleCacheFallsBackToLiveTargetDiscovery(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.sessions["mac"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "claude", Source: "zshrc"},
		},
		"mac": {
			{Name: "newly-added", Agent: "claude", Source: "zshrc"},
			{Name: "fresh-grok", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf("default", "plan")},
		},
	}
	f.mu.Unlock()

	// Populate the hub's cached zshrc snapshot for both machines while "server" lacks the newly added launchers.
	_ = s.zshrcLaunchers(ctx, false)

	// Now the live "server" node advertises both launchers with different capabilities than "mac", plus a partial
	// hub.json override on "newly-added" (AgentType/Agent unset) that must still preserve live omp.
	f.mu.Lock()
	f.zshrc["server"] = []proto.Launcher{
		{Name: "claude", Source: "zshrc"},
		{Name: "newly-added", Agent: "omp", Source: "zshrc"},
		{Name: "fresh-grok", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions")},
	}
	f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
		"server": {
			{Launcher: proto.Launcher{Name: "newly-added", Model: "custom-model"}},
		},
	}
	f.mu.Unlock()

	unionNewlyAdded := &ModelProvider{ID: "launcher.newly-added", Kind: "launcher", Agent: "claude"}
	if modes := s.permissionModesForLauncher(ctx, "server", "newly-added", unionNewlyAdded); len(modes) != 0 {
		t.Fatalf("stale cache missed live server omp for newly-added: got %v, want empty", modes)
	}

	unionFreshGrok := &ModelProvider{
		ID:              "launcher.fresh-grok",
		Kind:            "launcher",
		Agent:           "paseo",
		PermissionModes: proto.PermissionModeSetOf("default", "plan", "bypassPermissions"),
	}
	if modes := s.permissionModesForLauncher(ctx, "server", "fresh-grok", unionFreshGrok); slices.Contains(modes, "plan") || !slices.Contains(modes, "bypassPermissions") {
		t.Fatalf("stale cache fell back to cross-machine union for fresh-grok: got %v, want [default bypassPermissions]", modes)
	}

	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.fresh-grok","permission_mode":"plan"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("assistant on server accepted mac-only plan mode via stale cache: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.fresh-grok","permission_mode":"bypassPermissions"}`); w.Code != http.StatusOK {
		t.Fatalf("assistant on server rejected live bypassPermissions mode: %d %s", w.Code, w.Body.String())
	}
}

func TestModelProfilePermissionModeValidationAndCapture(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions")},
			{Name: "alias-omp", Agent: "omp", Source: "zshrc"},
		},
	}
	f.mu.Unlock()
	putLaunchers(t, s, apiProvider(t, s, "direct", "messages"))

	// HTTP PUT /api/v1/model-profiles/{id} returns 400 Bad Request (not 500) for launcher-only and unsupported permission modes.
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "api purpose with permission_mode",
			body: `{"revision":0,"name":"Bad API","purposes":{"kb":{"provider":"launcher.direct","permission_mode":"acceptEdits"}}}`,
		},
		{
			name: "purpose with runtime-unsupported permission_mode",
			body: `{"revision":0,"name":"Bad Grok","purposes":{"agent.new":{"provider":"launcher.grok","permission_mode":"plan"}}}`,
		},
		{
			name: "purpose with discovered omp alias",
			body: `{"revision":0,"name":"Bad OMP Alias","purposes":{"agent.new":{"provider":"launcher.alias-omp","permission_mode":"acceptEdits"}}}`,
		},
		{
			name: "tier with runtime-unsupported permission_mode",
			body: `{"revision":0,"name":"Bad Grok Tier","tiers":{"fast":{"launcher":"grok","permission_mode":"plan"}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := request(s, http.MethodPut, "/api/v1/model-profiles/custom", tc.body); w.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400; body = %s", tc.name, w.Code, w.Body.String())
			}
		})
	}

	// Supported mode on discovered runtime succeeds.
	if w := request(s, http.MethodPut, "/api/v1/model-profiles/custom", `{"revision":0,"name":"Good Grok","purposes":{"agent.new":{"provider":"launcher.grok","permission_mode":"bypassPermissions"}}}`); w.Code != http.StatusOK {
		t.Fatalf("supported runtime permission_mode rejected: %d %s", w.Code, w.Body.String())
	}

	// Capture preserves purpose permission_mode and leaves the generated presets' permission modes empty.
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", PermissionMode: "bypassPermissions"})
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude", PermissionMode: "acceptEdits"})
	purposes, _, tiers, err := s.captureModels(ctx)
	if err != nil {
		t.Fatalf("captureModels: %v", err)
	}
	if purposes["assistant"].PermissionMode != "bypassPermissions" || purposes["agent.new"].PermissionMode != "acceptEdits" {
		t.Fatalf("captured purposes missing permission_mode: %+v", purposes)
	}
	if len(tiers) != 2 || tiers["core"].PermissionMode != "" || tiers["dev"].PermissionMode != "" {
		t.Fatalf("captureModels leaked permission_mode into captured tiers: %+v", tiers)
	}
	if !slices.Equal(tiers["core"].Aliases, []string{"strong"}) || !slices.Equal(tiers["dev"].Aliases, []string{"standard"}) {
		t.Fatalf("captured tiers do not answer to their older names: %+v", tiers)
	}
}

func TestHeadlessStartDropsUnsupportedPerMachinePermissionMode(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.sessions["mac"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"mac": {
			{Name: "claude", Source: "zshrc"},
			{Name: "grok", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf("default", "plan", "bypassPermissions")},
		},
		"server": {
			{Name: "claude", Source: "zshrc"},
			{Name: "grok", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions")},
		},
	}
	f.mu.Unlock()
	// agent.new is machine-agnostic so "plan" is valid via mac's advertised capabilities.
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.grok", PermissionMode: "plan"})

	// Headless start on server (where grok only supports default + bypassPermissions) must drop inherited "plan".
	if _, err := s.startOn(ctx, "server", StartParams{Text: "handle lark", Purpose: purposeLarkAgent}, false); err != nil {
		t.Fatalf("lark.agent startOn server: %v", err)
	}
	last := f.sends[len(f.sends)-1]
	if _, exists := last["permission_mode"]; exists {
		t.Fatalf("headless start forwarded unsupported per-machine mode: %v", last)
	}

	// Supported inherited mode on server is preserved.
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.grok", PermissionMode: "bypassPermissions"})
	if _, err := s.startOn(ctx, "server", StartParams{Text: "handle lark 2", Purpose: purposeLarkAgent}, false); err != nil {
		t.Fatalf("lark.agent supported startOn server: %v", err)
	}
	if got := f.sends[len(f.sends)-1]["permission_mode"]; got != "bypassPermissions" {
		t.Fatalf("headless start dropped supported per-machine mode: got %v, want bypassPermissions", got)
	}

	// Explicit caller override is preserved.
	if _, err := s.startOn(ctx, "server", StartParams{Text: "explicit", Purpose: purposeLarkAgent, PermissionMode: "plan"}, false); err != nil {
		t.Fatalf("lark.agent explicit startOn server: %v", err)
	}
	if got := f.sends[len(f.sends)-1]["permission_mode"]; got != "plan" {
		t.Fatalf("headless start dropped explicit caller permission_mode: got %v, want plan", got)
	}

	// Inbox uses its own saved mode only when the resolved runtime supports it; otherwise it returns to safe default.
	setModel(t, s, purposeAgent, ModelBinding{Provider: "launcher.grok", PermissionMode: "plan"})
	if _, err := s.startOn(ctx, "server", StartParams{Text: "inbox unsupported", Purpose: purposeAgent, Origin: OriginInbox}, false); err != nil {
		t.Fatalf("inbox.agent unsupported startOn server: %v", err)
	}
	if got := f.sends[len(f.sends)-1]["permission_mode"]; got != proto.ModeDefault {
		t.Fatalf("inbox.agent forwarded unsupported saved mode: got %v, want default", got)
	}
	setModel(t, s, purposeAgent, ModelBinding{Provider: "launcher.grok", PermissionMode: "bypassPermissions"})
	if _, err := s.startOn(ctx, "server", StartParams{Text: "inbox supported", Purpose: purposeAgent, Origin: OriginInbox}, false); err != nil {
		t.Fatalf("inbox.agent supported startOn server: %v", err)
	}
	if got := f.sends[len(f.sends)-1]["permission_mode"]; got != "bypassPermissions" {
		t.Fatalf("inbox.agent dropped supported saved mode: got %v, want bypassPermissions", got)
	}
}

func TestLauncherViewsMergeUnrestrictedPermissionModes(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	restricted := proto.PermissionModeSetOf("default", "bypassPermissions")

	// When any machine reports zero (unrestricted / older node), the cross-machine merged view stays unrestricted (0)
	// regardless of machine order.
	for _, tc := range []struct {
		name  string
		zshrc []zshrcLaunchers
	}{
		{
			name: "unrestricted first then restricted",
			zshrc: []zshrcLaunchers{
				{MachineID: "old-node", Launchers: []proto.Launcher{{Name: "grok", Agent: "paseo", Source: "zshrc"}}},
				{MachineID: "new-node", Launchers: []proto.Launcher{{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: restricted}}},
			},
		},
		{
			name: "restricted first then unrestricted",
			zshrc: []zshrcLaunchers{
				{MachineID: "new-node", Launchers: []proto.Launcher{{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: restricted}}},
				{MachineID: "old-node", Launchers: []proto.Launcher{{Name: "grok", Agent: "paseo", Source: "zshrc"}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			views := s.launcherViewsFrom(ctx, tc.zshrc, nil)
			if len(views) != 1 || views[0].Name != "grok" {
				t.Fatalf("views = %+v", views)
			}
			if views[0].PermissionModes != 0 {
				t.Fatalf("merged PermissionModes = %v, want 0 (unrestricted)", views[0].PermissionModes.List())
			}
		})
	}

	// Machine-agnostic agent.new accepts a standard mode when one machine reports unrestricted (0).
	f.mu.Lock()
	f.sessions["mac"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: restricted}},
		"mac":    {{Name: "grok", Agent: "paseo", Source: "zshrc"}},
	}
	f.mu.Unlock()
	if w := modelRequest(s, http.MethodPatch, "agent.new", `{"revision":0,"provider":"launcher.grok","permission_mode":"plan"}`); w.Code != http.StatusOK {
		t.Fatalf("agent.new rejected mode valid on unrestricted machine: %d %s", w.Code, w.Body.String())
	}

	// When every machine declares an explicit set, the merged view carries their union.
	unionViews := s.launcherViewsFrom(ctx, []zshrcLaunchers{
		{MachineID: "mac", Launchers: []proto.Launcher{{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf("default", "plan")}}},
		{MachineID: "server", Launchers: []proto.Launcher{{Name: "grok", Agent: "paseo", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf("default", "bypassPermissions")}}},
	}, nil)
	if len(unionViews) != 1 || unionViews[0].PermissionModes != proto.PermissionModeSetOf("default", "plan", "bypassPermissions") {
		t.Fatalf("explicit union = %+v", unionViews)
	}
}

func TestAgentStartConfirmationShowsPermissionMode(t *testing.T) {
	f := healthHub()
	f.launcherList = []proto.Launcher{
		{Name: "claude", Agent: "claude"},
		{Name: "codex", Agent: "codex"},
	}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	answerOutbound(t, s, "sleep-000-0000", "agent_start", " · bypassPermissions", false)
	out, _ := spaceTool(t, s, "sleep-000-0000", "agent_start", `{"machine":"server","cwd":"/home/me/src/app","prompt":"Check config","launcher":"codex","permission_mode":"bypassPermissions"}`)
	if !strings.Contains(out, "declined") || len(f.starts) != 0 {
		t.Fatalf("agent_start: %s, starts %v", out, f.starts)
	}
}

func TestAgyUnavailableAutoModeDegradesAndHidesOption(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	noAuto := proto.PermissionModeSetOf("default", "acceptEdits", "plan", "bypassPermissions")
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "agy", Agent: "agy", Source: "agy", Assistant: true, PermissionModes: noAuto},
		},
	}
	f.assistantAnswer = func(params map[string]any) map[string]any {
		return map[string]any{"session_id": "agy-degraded-conv", "permission_mode": proto.ModeBypass}
	}
	f.mu.Unlock()

	// Model configuration rejects auto once agy advertises permission_modes without auto, while accepting bypassPermissions.
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.agy","permission_mode":"auto"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("assistant on restricted agy accepted auto: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":0,"provider":"launcher.agy","permission_mode":"bypassPermissions"}`); w.Code != http.StatusOK {
		t.Fatalf("assistant on restricted agy rejected bypassPermissions: %d %s", w.Code, w.Body.String())
	}

	// Older client explicitly requesting permission_mode=auto still succeeds with the degraded mode returned by the node.
	sess, err := s.startOn(ctx, "server", StartParams{Text: "hi", Launcher: "agy", PermissionMode: "auto"}, false)
	if err != nil {
		t.Fatalf("startOn explicit auto on agy: %v", err)
	}
	if sess.SessionID != "agy-degraded-conv" || sess.PermissionMode != proto.ModeBypass {
		t.Fatalf("degraded session = %+v, want session_id=agy-degraded-conv permission_mode=%q", sess, proto.ModeBypass)
	}

	// Implicit DefaultPermissionMode=auto is dropped for restricted agy and returns the settled mode.
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = "auto" })
	if w := modelRequest(s, http.MethodPatch, "assistant", `{"revision":1,"provider":"launcher.agy","permission_mode":null}`); w.Code != http.StatusOK {
		t.Fatalf("clear assistant permission_mode: %d %s", w.Code, w.Body.String())
	}
	sess, err = s.startOn(ctx, "server", StartParams{Text: "hi default", Launcher: "agy"}, false)
	if err != nil {
		t.Fatalf("startOn implicit auto default on agy: %v", err)
	}
	lastSend := f.sends[len(f.sends)-1]
	if _, forwarded := lastSend["permission_mode"]; forwarded {
		t.Fatalf("restricted agy forwarded unsupported implicit auto mode: %v", lastSend)
	}
	if sess.PermissionMode != proto.ModeBypass {
		t.Fatalf("implicit default degraded session = %+v, want permission_mode=%q", sess, proto.ModeBypass)
	}
}
