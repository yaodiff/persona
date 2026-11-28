package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestDispatchRefusesAssistantWorkspace(t *testing.T) {
	for _, tool := range []string{"task_plan", "agent_start"} {
		for _, cwd := range []string{"~/.persona/assistant", "~/" + compat.StateDir + "/assistant", "/home/me/.persona/assistant/.", "/home/me/" + compat.StateDir + "/assistant/../assistant", "/home/me/.persona/assistant-m-abc", "/state/assistant", "/state/assistant-m-abc"} {
			t.Run(tool+cwd, func(t *testing.T) {
				s, f := newTaskService(t)
				f.devices = []Device{{ID: "box", Home: "/home/me", DataDir: "/state", Online: true, Reachable: true}}
				args := map[string]any{"role": "worker", "title": "Inspect project", "prompt": "Inspect the source", "machine": "box", "cwd": cwd, "worktree": map[string]string{"branch": "fix/example"}}
				var input any = args
				if tool == "task_plan" {
					input = map[string]any{"tasks": []any{args}}
				}
				raw, _ := json.Marshal(input)
				_, err := s.runTool(context.Background(), "server", "conv9", tool, raw)
				var message *i18n.Message
				if !errors.As(err, &message) || message.Key != "rpc-errors.agent_assistant_workspace" {
					t.Fatalf("expected readable workspace refusal, got %v", err)
				}
				if len(f.starts) != 0 || len(f.worktrees) != 0 {
					t.Fatalf("dispatch had side effects: starts=%v worktrees=%v", f.starts, f.worktrees)
				}
			})
		}
	}
}

func TestAutoTaskRefusesAssistantWorkspaceBeforeQueue(t *testing.T) {
	for _, cwd := range []string{"/home/me/.persona/assistant", "/state/assistant", "~/.persona/assistant"} {
		t.Run(cwd, func(t *testing.T) {
			s, f := newTaskService(t)
			f.devices = []Device{{ID: "box", Home: "/home/me", DataDir: "/state", Reachable: true}}
			args, err := json.Marshal(map[string]any{"tasks": []map[string]any{
				{"title": "Read the source", "prompt": "Read the source", "goal": "Summarize the source", "readonly": true, "cwd": cwd},
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.toolTaskPlan(t.Context(), "conv9", args)
			var message *i18n.Message
			if !errors.As(err, &message) || message.Key != "rpc-errors.agent_assistant_workspace" {
				t.Fatalf("expected a submission error, got %v", err)
			}
			if got := feedKinds(s, "conv9"); got != "" || len(s.todosOf(t.Context(), "conv9")) != 0 {
				t.Fatalf("invalid plan created a feed item or to-do: %s", got)
			}
		})
	}
}
