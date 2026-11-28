package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/agentprompt"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgyProjectContextFollowsWorkflowCommand(t *testing.T) {
	s, _, todo := outcomeEnv(t, nil, outcomeReply)
	if err := kbEng(t, s).Store.AppendLog("天气-app", time.Now(), kb.TodoLogSource+kb.TodoLogTitle(todo.Title), "结论：项目背景已加载"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got := s.withProjectContextForAgent(ctx, "box", "/code/weather", "", agentprompt.Antigravity, " \t/plan ship\nkeep the body")
	if !strings.HasPrefix(got, "/plan ship\nkeep the body\n\n<persona-context") || !strings.Contains(got, "项目背景已加载") {
		t.Fatalf("agy project prompt = %q", got)
	}
	other := s.withProjectContextForAgent(ctx, "box", "/code/weather", "", "claude", "/plan ship")
	if !strings.HasPrefix(other, "<persona-context") {
		t.Fatalf("non-agy prompt order changed: %q", other)
	}
}

func TestAgyTodoHandoffKeepsDecisionsAfterWorkflowCommand(t *testing.T) {
	store := &phaseOutcomeStore{ctxAwareLinkStore: newCtxAwareLinkStore()}
	now := time.Now().UTC()
	store.rows = []db.TodoRow{{ID: "todo-agy", Title: "Ship", CreatedAt: now.Add(-time.Hour)}}
	store.decisions = []db.TodoDecisionRow{{ID: "dec-agy", TodoID: "todo-agy", Question: "Which target?", Choice: "production", Source: db.DecisionManual, CreatedAt: now}}
	s := newServiceOn(t, newFakeHub().withMachine(), store)
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: "todo-agy", Text: " \t/swarm split the work\nkeep the body", Agent: agentprompt.Antigravity})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, "/swarm split the work\nkeep the body\n\n<persona-context") || !strings.Contains(res.Text, "Which target?") {
		t.Fatalf("agy todo handoff = %q", res.Text)
	}
	other, err := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: "todo-agy", Text: "/swarm split the work"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(other.Text, "<persona-context") {
		t.Fatalf("non-agy todo handoff order changed: %q", other.Text)
	}
}

func TestAgyTodoHandoffTrimsContextlessWorkflow(t *testing.T) {
	store := &phaseOutcomeStore{ctxAwareLinkStore: newCtxAwareLinkStore()}
	store.rows = []db.TodoRow{{ID: "todo-bare", Title: "Ship", CreatedAt: time.Now().UTC().Add(-time.Hour)}}
	s := newServiceOn(t, newFakeHub().withMachine(), store)
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: "todo-bare", Text: " \t/goal ship it", Agent: agentprompt.Antigravity})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "/goal ship it" {
		t.Fatalf("contextless agy todo handoff = %q", res.Text)
	}
}

func TestAgyMainHandoffKeepsContextAfterWorkflowCommand(t *testing.T) {
	s := newServiceOn(t, newFakeHub().withMachine(), &memStore{})
	handoff := mainHandoff{From: "old-main", Summary: "old work", Until: time.Now().UTC()}
	got := s.renderHandoff(context.Background(), handoff, " /goal ship it\nkeep the body", agentprompt.Antigravity)
	if !strings.HasPrefix(got, "/goal ship it\nkeep the body\n\n<persona-context") || !strings.Contains(got, "old-main") {
		t.Fatalf("agy main handoff = %q", got)
	}
	other := s.renderHandoff(context.Background(), handoff, "/goal ship it", "codex")
	if !strings.HasPrefix(other, "<persona-context") {
		t.Fatalf("non-agy main handoff order changed: %q", other)
	}
}

func TestAgyAgentStartKeepsProjectMemoryAfterWorkflowCommand(t *testing.T) {
	s, f, todo := outcomeEnv(t, nil, outcomeReply)
	if err := kbEng(t, s).Store.AppendLog("天气-app", time.Now(), kb.TodoLogSource+kb.TodoLogTitle(todo.Title), "结论：启动背景"); err != nil {
		t.Fatal(err)
	}
	f.launcherList = []proto.Launcher{{Name: "test-runner", Agent: agentprompt.Antigravity, Source: "zshrc"}}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.test-runner"})
	args := json.RawMessage(`{"machine":"box","cwd":"/code/weather","prompt":" \t/boost fix the race\nkeep the body"}`)
	if _, err := s.runTool(context.Background(), "server", "conv1", "agent_start", args); err != nil {
		t.Fatal(err)
	}
	prompt := startsOf(f)[0]["prompt"].(string)
	if !strings.HasPrefix(prompt, "/boost fix the race\nkeep the body\n\n<persona-context") || !strings.Contains(prompt, "启动背景") {
		t.Fatalf("agy agent_start prompt = %q", prompt)
	}
}

func TestAgyTaskSuccessorKeepsWorkflowCommandBeforeHandoff(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["box"] = nil
	s := newStorelessService(t, f)
	todo := Todo{ID: "todo-agy-successor", Title: "Ship"}
	putTodo(s, todo)
	last := proto.Session{MachineID: "box", SessionID: "old", Cwd: "/code", Launcher: "agy",
		Agent: agentprompt.Antigravity, LastText: "half done"}
	if _, err := s.startTaskSuccessor(context.Background(), todo, last, " \t/goal finish it\nkeep the body"); err != nil {
		t.Fatal(err)
	}
	starts := startsOf(f)
	if len(starts) != 1 {
		t.Fatalf("got %d starts, want 1", len(starts))
	}
	prompt, _ := starts[0]["prompt"].(string)
	if !strings.HasPrefix(prompt, "/goal finish it\nkeep the body\n\n") ||
		!strings.Contains(prompt, "(Persona: this task was worked on") || !strings.Contains(prompt, "half done") {
		t.Fatalf("agy task successor prompt = %q", prompt)
	}
}

// A workflow command sent to the main conversation keeps byte zero ahead of what the old main conversation left when
// the conversation is known only by its launcher, an Antigravity one: the launcher's agent is still looked up for a
// workflow command (#16591), while an ordinary message skips that lookup and keeps the handoff first (#17005).
func TestAgyMainSendKeepsWorkflowCommandBeforeHandoff(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "agy-rt", Source: "agy", Agent: agentprompt.Antigravity, Assistant: true}}
	s := newServiceOn(t, f, &memStore{})
	setSettings(t, s, func(v *Settings) { v.Main, v.MainMachineID, v.MachineID = "main-empty", "server", "server" })
	s.originSet(ctx, "server", "main-empty", OriginUser, "", "")
	send := func(text string) string {
		t.Helper()
		if err := s.saveHandoff(ctx, mainHandoff{From: "old-main", For: "main-empty", MachineID: "server", Summary: "old work", Until: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		out, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "main-empty", Human: true,
			Params: map[string]any{"client_msg_id": "c-" + text[:5], "text": text, "launcher": "agy-rt"}})
		if err != nil {
			t.Fatal(err)
		}
		sent, _ := out["text"].(string)
		return sent
	}
	if got := send(" \t/boost fix the race\nkeep the body"); !strings.HasPrefix(got, "/boost fix the race\nkeep the body\n\n<persona-context") || !strings.Contains(got, "old-main") {
		t.Fatalf("agy main workflow command = %q", got)
	}
	if got := send("fix the race"); !strings.HasPrefix(got, "<persona-context") || !strings.Contains(got, "old-main") || !strings.HasSuffix(strings.TrimSpace(got), "fix the race") {
		t.Fatalf("ordinary main message with a handoff = %q", got)
	}
}
