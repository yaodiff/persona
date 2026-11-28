package assistant

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The refusals are agent_start's own coded errors. A launcher the caller names that is not the tier's takes nothing
// from it and is not checked here; the tier's launcher is, and an agent that takes no effort drops the tier's.
func TestAgentStartTierResolvesAsAgentStart(t *testing.T) {
	s, h, _, _ := presetsA2AFixture(t)
	ctx := t.Context()
	h.mu.Lock()
	h.zshrc["box"] = append(h.zshrc["box"], proto.Launcher{Name: "worker", Source: "zshrc", Agent: "pi"})
	h.mu.Unlock()
	saveTiers(t, s, map[string]ModelTier{
		"review": {Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast", PermissionMode: "bypassPermissions"},
		"light":  {Launcher: "worker", Effort: "high"},
	})
	box, ok := s.machineByName(ctx, "box")
	if !ok {
		t.Fatal("no box")
	}
	var m *i18n.Message
	if _, err := s.agentStartTier(ctx, box, "nope", ""); !errors.As(err, &m) || m.Key != "assistant-model_profiles.err_tier_not_found" {
		t.Fatalf("an unknown tier: %v", err)
	}
	if run, err := s.agentStartTier(ctx, box, " review ", ""); err != nil || run != (a2aRun{Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast"}) {
		t.Fatalf("review on box: %+v %v", run, err)
	}
	if run, err := s.agentStartTier(ctx, box, "review", "codex"); err != nil || run != (a2aRun{}) {
		t.Fatalf("a launcher of the caller's own took from the tier: %+v %v", run, err)
	}
	if run, err := s.agentStartTier(ctx, box, "light", ""); err != nil || run != (a2aRun{Launcher: "worker"}) {
		t.Fatalf("an agent that takes no effort: %+v %v", run, err)
	}
	h.mu.Lock()
	h.zshrc["box"] = []proto.Launcher{{Name: "other", Source: "zshrc"}}
	h.mu.Unlock()
	s.invalidateLauncherListCache("box") // box's launchers changed (launcher_list_cache.go)
	if _, err := s.agentStartTier(ctx, box, "review", "gateway"); !errors.As(err, &m) || m.Key != "assistant-tasks.launcher_not_on_machine" {
		t.Fatalf("a tier whose launcher box lacks: %v", err)
	}
}

// A launch stored before tiers (no tier, no run) still decodes, matches its retry and starts on its own launcher in
// plan; a launch without a tier is written as before, so an older manager reads it unchanged.
func TestA2ALaunchRowsWithoutATierStillWork(t *testing.T) {
	s, h, st, parent := presetsA2AFixture(t)
	ctx := t.Context()
	body := `{"wake":true,"events":[{"seq":1,"message_id":"old-1","kind":"message","text":"Inspect the build.","state":"submitted",` +
		`"at":"2026-11-20T00:00:00Z","delivery":"queued"}],"launch":{"cwd":"/code/app","launcher":"codex","pending":true,"subagent":true}}`
	if _, err := st.A2ACreate(ctx, db.A2ARow{ID: "t-old", Owner: parent.Owner, Request: "old-1", Context: "c-old",
		TargetMachine: "box", TargetSession: newStartSessionID(), SourceMachine: parent.Ref.Machine, SourceSession: parent.Ref.Session,
		State: a2aSubmitted, Body: json.RawMessage(body)}); err != nil {
		t.Fatal(err)
	}
	task, err := decodeA2A(st.A2AGet(ctx, "t-old"))
	if err != nil || task == nil || task.Launch == nil || task.Launch.Launcher != "codex" || task.Launch.Tier != "" || task.Launch.Run != (a2aRun{}) {
		t.Fatalf("an old launch row: %+v %v", task, err)
	}
	raw, err := json.Marshal(task.a2aBody)
	if err != nil || strings.Contains(string(raw), `"tier"`) || strings.Contains(string(raw), `"run"`) {
		t.Fatalf("a launch without a tier is written differently: %s %v", raw, err)
	}
	retry := presetsCall(ctx, s, parent, "a2a_send", `{"machine_id":"box","cwd":"/code/app","launcher":"codex","message_id":"old-1","text":"Inspect the build."}`)
	if id := presetsTaskID(t, retry); id != "t-old" {
		t.Fatalf("the old launch's retry made task %s", id)
	}
	processMemA2A(t, s, st, s.a2aConfig(ctx), "t-old")
	waitFor(t, "the old launch", func() bool { return len(startsOf(h.fakeHub)) == 1 })
	if got := startsOf(h.fakeHub)[0]; got["launcher"] != "codex" || !unset(got["model"]) || !unset(got["speed"]) || got["permission_mode"] != "plan" {
		t.Fatalf("an old launch row started %v", got)
	}
}
