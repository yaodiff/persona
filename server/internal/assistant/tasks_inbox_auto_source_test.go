package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskPlanInboxExplicitSourcePersistsAndKeepsItsExactHandoff(t *testing.T) {
	const conversation = "inbox-explicit-source"
	st := todoTestDB(t)
	s, _ := newTaskServiceOn(t, st)
	it := taskInboxItem(t, s, "example:source", "box", conversation)
	// Reload the manager to validate against the persisted handoff ledger.
	reloaded, f := newTaskServiceOn(t, st)
	if _, err := reloaded.toolTaskPlanFrom(t.Context(), "box", conversation, taskInboxArgs(it.ID)); err != nil {
		t.Fatal(err)
	}
	plan := planOf(t, reloaded, conversation)
	if task := plan.Tasks[0]; task.InboxItem != it.ID || task.InboxHandoff != namedClientID(it.ID) {
		t.Fatalf("explicit source did not retain the exact recorded handoff: %+v", task)
	}
	// A later handoff must not silently retarget a card that already has a validated source.
	taskInboxItem(t, reloaded, "example:later", "box", conversation)
	started, err := reloaded.startPlan(t.Context(), plan.ID, nil, false)
	if err != nil || started.Tasks[0].Error != "" {
		t.Fatalf("plan with an explicit source could not start: %+v, %v", started, err)
	}
	id := started.Tasks[0].TodoID
	waitFor(t, "the task's Agent", func() bool {
		todo, _ := reloaded.todoNow(id)
		return len(todo.Agents) == 1
	})
	if len(f.started()) != 1 {
		t.Fatal("the plan did not dispatch exactly one Agent")
	}
	again, _ := newTaskServiceOn(t, st)
	if err := again.ensureTodosLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := again.inboxReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	todo, ok := again.todoNow(id)
	if !ok || todo.InboxItem != it.ID || len(todo.Agents) != 1 {
		t.Fatalf("reload lost the to-do's source or Agent: %+v", todo)
	}
	again.inbox.mu.Lock()
	defer again.inbox.mu.Unlock()
	source := again.inbox.items[it.ID]
	if source.Agent == nil || source.Agent.TodoID != id || !source.open() {
		t.Fatalf("reload lost the Inbox-to-task back-reference: %+v", source)
	}
}

func TestTaskPlanInboxOmittedSourceLeavesUnrelatedHandoffsUnlinked(t *testing.T) {
	const conversation = "inbox-unrelated-handoffs"
	st := todoTestDB(t)
	s, f := newTaskServiceOn(t, st)
	a := taskInboxItem(t, s, "example:a", "box", conversation)
	b := taskInboxItem(t, s, "example:b", "box", conversation)
	if _, err := s.toolTaskPlanFrom(t.Context(), "box", conversation, taskInboxArgs(a.ID, "", b.ID)); err != nil {
		t.Fatal("unrelated handoffs blocked planning:", err)
	}
	plan := planOf(t, s, conversation)
	if len(plan.Tasks) != 3 || plan.Tasks[0].InboxItem != a.ID || plan.Tasks[2].InboxItem != b.ID {
		t.Fatal("plan lost an explicit selection", plan)
	}
	if omitted := plan.Tasks[1]; omitted.InboxItem != "" || omitted.InboxHandoff != "" || omitted.InboxMatch != "" {
		t.Fatalf("omitted source picked a historical handoff: %+v", omitted)
	}
	if todoCount(s) != 0 || len(f.started()) != 0 {
		t.Fatal("planning started work")
	}
}

func TestTaskPlanInboxAutoSourceIgnoresOtherContexts(t *testing.T) {
	for _, why := range []string{"none", "filed", "merged", "conversation", "machine", "no operation", "another manager", "text only"} {
		t.Run(why, func(t *testing.T) {
			const conversation = "inbox-auto-isolation"
			s, _ := newTaskService(t)
			if why != "none" && why != "text only" {
				target := s
				if why == "another manager" {
					target, _ = newTaskService(t)
				}
				it := taskInboxItem(t, target, "example:source", "box", conversation)
				target.inbox.mu.Lock()
				switch why {
				case "filed":
					it.DoneAt = new(time.Now())
				case "merged":
					it.MergedInto = "example:other"
				case "conversation":
					it.Handoffs[0].SessionID = "another-conversation"
				case "machine":
					it.Handoffs[0].MachineID = "another-computer"
				case "no operation":
					it.Handoffs[0].Op = ""
				}
				target.inbox.mu.Unlock()
			}
			raw := taskInboxArgs("")
			if why == "text only" {
				raw = json.RawMessage(strings.Replace(string(raw), "Inspect the reported defect and verify the result",
					"<persona-context>Inbox item: example:source</persona-context>", 1))
			}
			if _, err := s.toolTaskPlanFrom(t.Context(), "box", conversation, raw); err != nil {
				t.Fatal(err)
			}
			if task := planOf(t, s, conversation).Tasks[0]; task.InboxItem != "" || task.InboxHandoff != "" {
				t.Fatalf("unrelated context became an Inbox source: %+v", task)
			}
		})
	}
}

func TestTaskPlanInboxExplicitSourceRespectsHandoffPolicy(t *testing.T) {
	for _, kinds := range [][]string{{KindBot}, {"bot_handoff"}, {KindBot, "bot_handoff"}} {
		for _, eligible := range []string{"none", "human", "legacy human"} {
			t.Run(strings.Join(kinds, "+")+"/"+eligible, func(t *testing.T) {
				const conversation = "inbox-auto-policy"
				s, f := newTaskService(t)
				var excluded []*InboxItem
				for _, kind := range kinds {
					it := taskInboxItem(t, s, "example:"+kind, "box", conversation)
					s.inbox.mu.Lock()
					it.Source, it.Kind = "larkcli", kind
					it.Connector = taskNotification("cli_example")
					s.inbox.mu.Unlock()
					excluded = append(excluded, it)
				}
				var want string
				if eligible != "none" {
					it := taskInboxItem(t, s, "example:human", "box", conversation)
					want = it.ID
					if eligible == "legacy human" {
						s.inbox.mu.Lock()
						it.Source, it.Kind, it.Connector = "larkcli", KindBot, taskNotification("ou_example")
						s.inbox.mu.Unlock()
					}
				}
				if _, err := s.toolTaskPlanFrom(t.Context(), "box", conversation, taskInboxArgs(want)); err != nil {
					t.Fatal("ineligible handoff blocked ordinary work or an eligible source:", err)
				}
				plan := planOf(t, s, conversation)
				if task := plan.Tasks[0]; task.InboxItem != want || (want == "" && task.InboxHandoff != "") {
					t.Fatalf("planned the wrong source: %+v, want %q", task, want)
				}
				for _, it := range excluded {
					if _, err := s.toolTaskPlanFrom(t.Context(), "box", conversation, taskInboxArgs(it.ID)); err == nil {
						t.Fatal("explicit ineligible source bypassed link policy")
					}
				}
				started, err := s.startPlan(t.Context(), plan.ID, nil, false)
				if err != nil || started.Tasks[0].Error != "" || started.Tasks[0].TodoID == "" {
					t.Fatalf("eligible or unlinked work could not start: %+v, %v", started, err)
				}
				waitFor(t, "work after filtering ineligible handoffs", func() bool { return len(f.started()) == 1 })
				todo, _ := s.todoNow(started.Tasks[0].TodoID)
				if todo.InboxItem != want {
					t.Fatalf("admission changed the chosen source: %+v", todo)
				}
			})
		}
	}
}

func TestTaskPlanInboxExplicitSourceRetainsStartChecks(t *testing.T) {
	for _, why := range []string{"filed", "excluded"} {
		t.Run(why, func(t *testing.T) {
			const conversation = "inbox-auto-recheck"
			s, f := newTaskService(t)
			it := taskInboxItem(t, s, "example:source", "box", conversation)
			if _, err := s.toolTaskPlanFrom(t.Context(), "box", conversation, taskInboxArgs(it.ID)); err != nil {
				t.Fatal(err)
			}
			s.inbox.mu.Lock()
			if why == "filed" {
				it.DoneAt = new(time.Now())
			} else {
				it.Source, it.Kind = "larkcli", "bot_handoff"
			}
			s.inbox.mu.Unlock()
			started, err := s.startPlan(t.Context(), planOf(t, s, conversation).ID, nil, false)
			if err != nil || started.Tasks[0].Error == "" || started.Tasks[0].TodoID != "" {
				t.Fatalf("an explicit source bypassed the Start recheck: %+v, %v", started, err)
			}
			if todoCount(s) != 0 || len(f.started()) != 0 {
				t.Fatal("source rejection created work")
			}
		})
	}
}
