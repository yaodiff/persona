package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A task planned in the same directory as an active agent on the planner's own open to-do returns the agent's exact
// ids even when the titles are too different for planDuplicates' similarity thresholds (#13192). The plan still
// dispatches: the hint never blocks.
func TestTaskPlanReuseHintForSameDirectoryAgent(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "1081 整明白：逐题查清失分真假与原因", "long investigation", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "same0001", todo.Title, "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "离满分差距与记忆能帮什么", "prompt": "A differently worded same-topic follow-up", "machine": "box",
		"cwd": "/code/app/sub", "readonly": true, "why": "same repository", "permission": "plan",
	}}})
	res, err := s.toolTaskPlan(ctx, "conv1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "An Agent is already active on related work") ||
		!strings.Contains(res, todo.ID) || !strings.Contains(res, "same0001") {
		t.Fatalf("missing the related-agent reuse hint: %s", res)
	}
	waitFor(t, "the planned task still dispatches", func() bool { return todoCount(s) == 2 })
}

// No hint for another conversation's agents, nor outside the working directory.
func TestReuseHintsBoundary(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	other, err := s.createTodo(ctx, "Another direction's work", "notes", "conv2", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, other.ID, "box", "other001", other.Title, "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	if hints := s.reuseHints(ctx, "conv1", "/code/app", 4); len(hints) != 0 {
		t.Fatalf("another conversation's agent must not be hinted: %+v", hints)
	}
	mine, err := s.createTodo(ctx, "My work elsewhere", "notes", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, mine.ID, "box", "mine001", mine.Title, "/other/repo", "claude"); err != nil {
		t.Fatal(err)
	}
	if hints := s.reuseHints(ctx, "conv1", "/code/app", 4); len(hints) != 0 {
		t.Fatalf("a different directory must not match: %+v", hints)
	}
	if hints := s.reuseHints(ctx, "conv1", "/other/repo/nested", 4); len(hints) != 1 || hints[0].sessionID != "mine001" {
		t.Fatalf("nested directory did not match own agent: %+v", hints)
	}
	releaseBoxLink(t, s, mine.ID, "mine001")
	if hints := s.reuseHints(ctx, "conv1", "/other/repo", 4); len(hints) != 0 {
		t.Fatalf("a released agent is no active reuse target: %+v", hints)
	}
}

// todo_create in a conversation that already runs agents names them, so the lead records the follow-up to-do without
// losing the reuse path (#13192).
func TestTodoCreateNamesActiveReuseAgents(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Original investigation", "notes", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "create001", todo.Title, "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	res, err := runJSON(t, s, "conv1", "todo_create", map[string]any{"title": "A follow-up worth recording"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "An Agent is already active on related work") ||
		!strings.Contains(res, short("create001")) || !strings.Contains(res, todo.ID) {
		t.Fatalf("todo_create did not name the active agent: %s", res)
	}
}
