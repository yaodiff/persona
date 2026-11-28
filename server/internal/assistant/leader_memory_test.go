package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestLeaderMemoryInjectsProjectExtensionAndUserDecisions(t *testing.T) {
	ctx := context.Background()
	s, f := notesEnv(t)
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	future := now.Add(24 * time.Hour)

	// Configure prompts/projects extension block for the direction's bound KB project (天气-app).
	f.answerApp(AppPlugins, PluginsProjectContext, func(params json.RawMessage) (any, error) {
		var p assistantwire.ProjectContextParams
		_ = json.Unmarshal(params, &p)
		var blocks []assistantwire.ExtensionContextBlock
		for _, slug := range p.Projects {
			if slug == "天气-app" {
				blocks = append(blocks, assistantwire.ExtensionContextBlock{
					Extension: "qa-proj-ext",
					Name:      "ProjectExt",
					Project:   slug,
					Text:      "- Weather release branch freezes every Friday",
				})
			}
		}
		return assistantwire.ExtensionContextResult{Blocks: blocks}, nil
	})

	// Configure memory_sources extension with standing, project-bound, unrelated, and sensitive entries.
	s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatchOf([]wire.MemoryEntriesResult{{
		Tagged: true,
		Entries: []wire.MemoryEntry{
			{ID: "qa-stand", Text: "qa: standing preference", Subjects: []string{"天气-app"}, Standing: true, Rank: 950, Status: "active"},
			{ID: "qa-proj-rule", Text: "qa: 天气 App 打包前先检查签名证书", Subjects: []string{"天气-app"}, Rank: 850, Status: "active"},
			{ID: "qa-other-proj", Text: "qa: unrelated project note", Subjects: []string{"other-app"}, Rank: 900, Status: "active"},
			{ID: "qa-secret", Text: "qa: weather private tax info", Subjects: []string{"天气-app"}, Categories: []string{categoryFinance}, OriginSpace: "qa-finance", Rank: 999, Status: "active"},
		},
	}})}

	// Configure unified decision ledger entries: global user decision, direction-project user decision,
	// other-project user decision, expired user decision, and agent decision.
	s.memoryEntries = &memEntrySource{entries: []proto.AssistantEntry{
		{ID: "d-global", Type: "decision", By: "user", Trust: "user_verbatim", Status: "active", Text: "先出方案再动手", Quote: "先出方案再动手", At: now, Scope: proto.AssistantEntryScope{Executor: "assistant", Personal: true}},
		{ID: "d-weather", Type: "decision", By: "user", Trust: "user_verbatim", Status: "active", Text: "天气版暂缓上线", Quote: "天气版暂缓上线", At: now, ExpiresAt: &future, Scope: proto.AssistantEntryScope{Executor: "assistant", Project: []string{"天气-app"}, Personal: true}},
		{ID: "d-other", Type: "decision", By: "user", Trust: "user_verbatim", Status: "active", Text: "别的项目决定", Quote: "别的项目决定", At: now, Scope: proto.AssistantEntryScope{Executor: "assistant", Project: []string{"other-app"}, Personal: true}},
		{ID: "d-expired", Type: "decision", By: "user", Trust: "user_verbatim", Status: "active", Text: "已过期的暂停令", Quote: "已过期的暂停令", At: now, ExpiresAt: &expired, Scope: proto.AssistantEntryScope{Executor: "assistant", Personal: true}},
		{ID: "d-agent", Type: "decision", By: "agent", Trust: "agent", Status: "active", Text: "AGENT_INFERRED_DECISION", At: now, Scope: proto.AssistantEntryScope{Executor: "assistant", Project: []string{"天气-app"}}},
	}}

	// Leader context on start / resume / compaction carries both project extension memory and scoped user decisions.
	got := s.Context(ctx, ContextParams{MachineID: "server", SessionID: "conv_lead"}).Text
	for _, want := range []string{
		"Weather release branch freezes every Friday",
		"qa: 天气 App 打包前先检查签名证书",
		"先出方案再动手",
		"天气版暂缓上线",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("leader context missing %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"qa: unrelated project note", "weather private tax info", "别的项目决定", "已过期的暂停令", "AGENT_INFERRED_DECISION"} {
		if strings.Contains(got, bad) {
			t.Errorf("leader context leaked %q:\n%s", bad, got)
		}
	}
	if strings.Count(got, "qa: standing preference") != 1 {
		t.Errorf("standing entry duplicated in leader context:\n%s", got)
	}

	// Compaction recovery for leader deterministically restores direction notes, project extension block, and scoped user decisions.
	s.directions.mu.Lock()
	s.directions.m["conv_lead"].Notes = "DIRECTION_REDLINE_RULE"
	s.directions.mu.Unlock()
	rec := s.Context(ctx, ContextParams{MachineID: "server", SessionID: "conv_lead", RecoveryID: "leader-compact-1"}).Recovery
	for _, want := range []string{
		"DIRECTION_REDLINE_RULE",
		"Weather release branch freezes every Friday",
		"qa: 天气 App 打包前先检查签名证书",
		"先出方案再动手",
		"天气版暂缓上线",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("leader compact recovery missing %q:\n%s", want, rec)
		}
	}
	for _, bad := range []string{"别的项目决定", "已过期的暂停令", "AGENT_INFERRED_DECISION", "weather private tax info"} {
		if strings.Contains(rec, bad) {
			t.Errorf("leader compact recovery leaked %q:\n%s", bad, rec)
		}
	}

	// Wrong machine gets neither leader memory section nor recovery.
	if wrong := s.Context(ctx, ContextParams{MachineID: "wrong", SessionID: "conv_lead", RecoveryID: "leader-compact-2"}); strings.Contains(wrong.Text, "Weather release branch") || wrong.Recovery != "" {
		t.Fatalf("wrong machine received leader memory: %+v", wrong)
	}
}
