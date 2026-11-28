package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentStartRejectsInvalidModelAliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		aliases map[string]string
	}{
		{"unknown key", map[string]string{"tiny_model": "vendor/tiny"}},
		{"tier alias", map[string]string{"sonnet_model": "sonnet"}},
		{"empty value", map[string]string{"haiku_model": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			args, _ := json.Marshal(map[string]any{
				"machine": "box", "cwd": "/code/app", "prompt": "Inspect", "model_aliases": tc.aliases,
			})
			_, err := s.runTool(t.Context(), "server", "conv9", "agent_start", args)
			if err == nil || !strings.Contains(err.Error(), "model_aliases") {
				t.Fatalf("err = %v, want a model_aliases validation error", err)
			}
			if len(f.starts) != 0 {
				t.Fatalf("an agent started: %v", f.starts)
			}
		})
	}
}

func TestTaskPlanRejectsInvalidModelAliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		aliases map[string]string
	}{
		{"unknown key", map[string]string{"tiny_model": "vendor/tiny"}},
		{"tier alias", map[string]string{"sonnet_model": "sonnet"}},
		{"empty value", map[string]string{"haiku_model": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			args, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
				"title": "Fix login", "prompt": "Fix the login crash and add tests", "machine": "box", "cwd": "/code/app",
				"permission": "bypassPermissions", "user_requested": true, "why": "the repository is on box",
				"model_aliases": tc.aliases,
			}}})
			_, err := s.toolTaskPlan(t.Context(), "conv1", args)
			if err == nil || !strings.Contains(err.Error(), "model_aliases") {
				t.Fatalf("err = %v, want a model_aliases validation error", err)
			}
			if len(f.starts) != 0 {
				t.Fatalf("a task agent started: %v", f.starts)
			}
		})
	}
}
