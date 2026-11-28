package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestReadonlyTaskReportDoesNotAskUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := readonlyTaskService(t)
		setSettings(t, s, func(v *Settings) { v.Main = "readonly-chat" })
		if _, err := s.toolTaskPlan(context.Background(), "readonly-chat", readonlyTaskArgs(t, readonlyTestTask(""))); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		todo := onlyTodo(s)
		if len(todo.Agents) != 1 || todo.Supervision == nil || todo.Supervision.Supervisor != "readonly-chat" {
			t.Fatalf("current supervised task not dispatched: %+v", todo)
		}
		agent := todo.Agents[0]
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		args := json.RawMessage(`{"kind":"done","text":"Inspection complete; no files changed."}`)
		ix := proto.Interaction{ID: "readonly-report", MachineID: agent.MachineID, SessionID: agent.SessionID,
			Kind: proto.IxPermission, ToolName: "mcp__persona__report_to_supervisor", ToolInput: args}
		done := make(chan proto.Resolution, 1)
		go func() {
			r, err := s.nodeInteraction(ctx, ix)
			if err != nil {
				t.Error(err)
			}
			done <- r
		}()
		synctest.Wait()
		select {
		case r := <-done:
			if raw, _ := json.Marshal(r); string(raw) != `{"allow":true}` {
				t.Fatalf("report must be allowed once without a permission grant or mode change: %+v", r)
			}
		default:
			s.fleet.mu.Lock()
			pending := s.fleet.interactions[ix.ID]
			s.fleet.mu.Unlock()
			if pending != nil {
				t.Fatalf("current readonly report is waiting on a pending permission interaction: kind=%s tool=%s", pending.ix.Kind, pending.ix.ToolName)
			}
			t.Fatal("report did not return directly")
		}
		for _, resumable := range []bool{true, true, false} {
			ix.Resumable = resumable
			assertReadonlyReportAllowed(t, s, ix)
		}
		ix.ToolName = "mcp__jarvis__report_to_supervisor"
		assertReadonlyReportAllowed(t, s, ix)
		res := s.mcpCall(ctx, agent.MachineID, agent.SessionID, "report_to_supervisor", args, false)
		if res.IsError {
			t.Fatalf("completion report failed: %+v", res)
		}
		cur, _ := s.todoNow(todo.ID)
		if cur.Supervision.Progress == nil || cur.Supervision.Progress.Kind != "done" || cur.Supervision.Progress.Text != "Inspection complete; no files changed." {
			t.Fatalf("completion report not delivered to the task: %+v", cur.Supervision.Progress)
		}
		if cards := s.workCards(); len(cards) != 0 {
			t.Fatalf("pending cards: %+v", cards)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, event := range f.told {
			if event.event == assistantwire.EventFleetInteraction {
				t.Fatal("completion report created a user permission card")
			}
		}
	})
}

func assertReadonlyReportAllowed(t *testing.T, s *Service, ix proto.Interaction) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := s.nodeInteraction(ctx, ix)
	raw, _ := json.Marshal(r)
	if err != nil || string(raw) != `{"allow":true}` {
		t.Fatalf("report must return a single-call allow, got %s, error = %v", raw, err)
	}
}

func TestReadonlyReportKeepsOtherApprovals(t *testing.T) {
	for _, toolName := range []string{"mcp__persona__report_to_supervisor", "mcp__jarvis__report_to_supervisor"} {
		t.Run(toolName, func(t *testing.T) {
			for _, name := range []string{"writable task", "other machine", "other session", "other task", "unsupervised task",
				"Bash", "other Persona tool", "other legacy tool", "other MCP server", "unqualified report", "report suffix", "other interaction kind", "unstarted plan"} {
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						s, _ := readonlyTaskService(t)
						if name != "unsupervised task" {
							setSettings(t, s, func(v *Settings) { v.Main = "readonly-chat" })
						}
						task := readonlyTestTask("bypassPermissions")
						task.Readonly = name != "writable task"
						if _, err := s.toolTaskPlan(context.Background(), "readonly-chat", readonlyTaskArgs(t, task)); err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
						if !task.Readonly {
							plan := readonlyFeedPlan(t, s, "readonly-chat")
							if _, err := s.startPlan(context.Background(), plan.ID, nil, false); err != nil {
								t.Fatal(err)
							}
							synctest.Wait()
						}
						todo := onlyTodo(s)
						agent := todo.Agents[0]
						ix := proto.Interaction{ID: "other-approval", MachineID: agent.MachineID, SessionID: agent.SessionID,
							Kind: proto.IxPermission, OutwardWrite: true, ToolName: toolName}
						switch name {
						case "other machine":
							ix.MachineID = "other"
						case "other session":
							ix.SessionID = "unrelated"
						case "other task":
							e := &todoEnv{t: t, s: s}
							other, err := s.createTodo(context.Background(), "Unrelated task", "", "readonly-chat", "")
							if err != nil {
								t.Fatal(err)
							}
							e.must("PUT", "/api/v1/todos/"+other.ID+"/agents/"+agent.MachineID+"/"+agent.SessionID, "")
							s.superviseTodo(context.Background(), other.ID, "readonly-chat", nil, 0)
						case "Bash":
							ix.ToolName = "Bash"
						case "other Persona tool":
							ix.ToolName = "mcp__persona__todo_decision_add"
						case "other legacy tool":
							ix.ToolName = "mcp__jarvis__todo_decision_add"
						case "report suffix":
							ix.ToolName += "_extra"
						case "unstarted plan":
							plan := readonlyFeedPlan(t, s, "readonly-chat")
							plan.State = planOpen
							if err := s.savePlan(context.Background(), plan); err != nil {
								t.Fatal(err)
							}
						case "other MCP server":
							ix.ToolName = "mcp__extension__report_to_supervisor"
						case "unqualified report":
							ix.ToolName = "report_to_supervisor"
						case "other interaction kind":
							ix.Kind = proto.IxPlan
						}
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						done := make(chan proto.Resolution, 1)
						go func() { r, _ := s.nodeInteraction(ctx, ix); done <- r }()
						synctest.Wait()
						select {
						case r := <-done:
							t.Fatalf("unrelated approval returned directly: %+v", r)
						default:
						}
						s.fleet.mu.Lock()
						pending := s.fleet.interactions[ix.ID] != nil
						s.fleet.mu.Unlock()
						if !pending {
							t.Fatal("unrelated approval card was not registered")
						}
						cancel()
						synctest.Wait()
						if r := <-done; r.Allow != nil || r.Approve != nil {
							t.Fatalf("cancellation granted permissions: %+v", r)
						}
					})
				})
			}
		})
	}
}

func TestReadonlyWorkerReportReachesLeader(t *testing.T) {
	for _, toolName := range []string{"mcp__persona__report_to_supervisor", "mcp__jarvis__report_to_supervisor"} {
		t.Run(toolName, func(t *testing.T) {
			s, f := newQuietService(t)
			startTaskLeader(t, s, f)
			worker := planWorker(t, s, f, map[string]any{"readonly": true})
			agent := worker.Agents[0]
			args := json.RawMessage(`{"kind":"done","text":"Inspection complete; no files changed."}`)
			assertReadonlyReportAllowed(t, s, proto.Interaction{ID: "worker-report", MachineID: agent.MachineID,
				SessionID: agent.SessionID, Kind: proto.IxPermission, OutwardWrite: true, ToolName: toolName, ToolInput: args, Resumable: true})
			res := s.mcpCall(context.Background(), agent.MachineID, agent.SessionID, "report_to_supervisor", args, false)
			if res.IsError || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
				t.Fatalf("worker report failed: %+v", res)
			}
			gotMessage(t, f, "Inspection complete; no files changed.")
			if notices := f.notices(); len(notices) != 0 {
				t.Fatalf("worker report went to the main conversation: %v", notices)
			}
			if cards := s.workCards(); len(cards) != 0 {
				t.Fatalf("worker report created permission cards: %+v", cards)
			}
		})
	}
}

func TestReadonlyTaskReportAfterRestart(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuse=%v", reuse), func(t *testing.T) {
			s, _ := newQuietServiceOn(t, database)
			conversation := fmt.Sprintf("report-restart-%v", reuse)
			setSettings(t, s, func(v *Settings) { v.Main = conversation })
			task := readonlyTestTask("")
			if reuse {
				todo, err := s.createTodo(ctx, "Inspect", "", conversation, "")
				if err != nil {
					t.Fatal(err)
				}
				task.ReuseTodo = todo.ID
			}
			_, err := s.toolTaskPlan(ctx, conversation, readonlyTaskArgs(t, task))
			if err != nil {
				t.Fatal(err)
			}
			plan := readonlyFeedPlan(t, s, conversation)
			var todo Todo
			waitFor(t, "read-only report task dispatch", func() bool {
				p, err := s.loadPlan(ctx, plan.ID)
				if err != nil || p.Tasks[0].TodoID == "" {
					return false
				}
				todo, _ = s.todoNow(p.Tasks[0].TodoID)
				return len(todo.Agents) == 1 && todo.Supervision != nil
			})
			if reuse && (todo.ID != task.ReuseTodo || todo.PlanID != "") {
				t.Fatalf("expected a reused topic without PlanID: %+v", todo)
			}
			agent := todo.Agents[0]
			ix := proto.Interaction{ID: "restart-report-" + todo.ID, MachineID: agent.MachineID, SessionID: agent.SessionID,
				Kind: proto.IxPermission, ToolName: "mcp__persona__report_to_supervisor", Resumable: true}
			for attempt := 0; attempt < 2; attempt++ {
				restarted, _ := newTaskServiceOn(t, database)
				for _, toolName := range []string{"mcp__persona__report_to_supervisor", "mcp__jarvis__report_to_supervisor"} {
					ix.ToolName = toolName
					assertReadonlyReportAllowed(t, restarted, ix)
				}
				res := restarted.mcpCall(ctx, agent.MachineID, agent.SessionID, "report_to_supervisor",
					json.RawMessage(`{"kind":"done","text":"Persistent inspection complete."}`), false)
				if res.IsError || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
					t.Fatalf("report after restart failed: %+v", res)
				}
				items, _, err := restarted.feed(ctx, conversation, time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				delivered := false
				for _, item := range items {
					var body reportBody
					if item.Kind == feedReport && item.Ref == todo.ID && json.Unmarshal(item.Body, &body) == nil &&
						body.Kind == "done" && body.Text == "Persistent inspection complete." {
						delivered = true
					}
				}
				if !delivered {
					t.Fatal("completion report was not persisted in the supervisor's feed")
				}
				if cards := restarted.workCards(); len(cards) != 0 {
					t.Fatalf("restart created report permission cards: %+v", cards)
				}
			}
			if status, _, err := database.InteractionOutcome(ctx, ix.ID); err != nil || status != "" {
				t.Fatalf("report registered a persistent permission interaction: status=%s, error=%v", status, err)
			}
		})
	}
}

func TestReadonlyLeaderReportAfterRestart(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, f := newQuietServiceOn(t, database)
	startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, map[string]any{"readonly": true})
	agent := worker.Agents[0]
	restarted, hub := newQuietServiceOn(t, database)
	devices := newFleetDevs()
	devices.add("box", "Computer")
	devices.setSessions("box", []proto.Session{
		{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle},
		{SessionID: agent.SessionID, MachineID: "box", Agent: "claude", Status: proto.StatusWorking},
	})
	restarted.devs = devices
	for _, toolName := range []string{"mcp__persona__report_to_supervisor", "mcp__jarvis__report_to_supervisor"} {
		t.Run(toolName, func(t *testing.T) {
			todo, ok := restarted.taskOf(ctx, agent.MachineID, agent.SessionID)
			if !ok || todo.ID != worker.ID || todo.Supervision == nil || todo.Supervision.Supervisor != "box/s1" {
				t.Fatalf("worker lost its persisted leader binding: %+v", todo)
			}
			ix := proto.Interaction{ID: "leader-report-" + toolName, SessionID: agent.SessionID,
				Kind: proto.IxPermission, OutwardWrite: true, ToolName: toolName, Resumable: true}
			params, _ := json.Marshal(map[string]any{"interaction": ix})
			callCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			resolution, handled, err := restarted.NodeRequest(callCtx, agent.MachineID, "interaction.create", params)
			raw, _ := json.Marshal(resolution)
			if err != nil || !handled || string(raw) != `{"allow":true}` {
				t.Fatalf("runtime report must return a single-call allow, got %s, handled=%v, error=%v", raw, handled, err)
			}
			if status, _, err := database.InteractionOutcome(ctx, ix.ID); err != nil || status != "" {
				t.Fatalf("report registered a persistent permission interaction: status=%s, error=%v", status, err)
			}
		})
	}
	res := restarted.mcpCall(ctx, agent.MachineID, agent.SessionID, "report_to_supervisor",
		json.RawMessage(`{"kind":"done","text":"Persistent leader inspection complete."}`), false)
	if res.IsError {
		t.Fatalf("worker report failed: %+v", res)
	}
	gotMessage(t, hub, "Persistent leader inspection complete.")
	if notices := hub.notices(); len(notices) != 0 {
		t.Fatalf("worker report went to the main conversation: %v", notices)
	}
	if cards := restarted.workCards(); len(cards) != 0 {
		t.Fatalf("worker report created permission cards: %+v", cards)
	}
}
