package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const softTestWords = "Please investigate the upload retry regression in release candidate seven"

func softTestItem(id string, now time.Time) *InboxItem {
	return &InboxItem{ID: id, Source: "example", Account: "primary", Kind: KindMention, Where: "Example Review", Title: "Upload failure", Sender: "Alice Example", Tier: TierAction, Last: "message-7", UpdatedAt: now, ArrivedAt: now,
		Chain: []InboxChainEntry{{Kind: ChainReceived, Messages: []InboxChainMessage{{Sender: "Alice Example", Text: softTestWords, At: &now}}}}}
}
func softTestService(t *testing.T) (*Service, *fakeHub, *InboxItem) {
	t.Helper()
	s, f := newTaskService(t)
	f.inbox = newFakeInbox()
	if err := s.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := softTestItem("example:one", time.Now().UTC())
	s.inbox.mu.Lock()
	s.inbox.items[it.ID] = it
	s.inbox.mu.Unlock()
	return s, f, it
}
func softTestArgs(title, prompt, item string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": title, "prompt": prompt, "role": "worker", "machine": "box", "cwd": "/code/app", "inbox_item": item, "done_when": []string{"Regression checked"}}}})
	return raw
}
func TestInboxSoftMatchBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, title, prompt string
		change              func(*Service, *InboxItem)
		want                bool
	}{
		{name: "independent", title: "Example Review", prompt: "Ask Alice Example to investigate", want: true},
		{name: "normalized group", title: "  EXAMPLE   Review ", prompt: softTestWords, want: true},
		{name: "group only", title: "Example Review", prompt: "Investigate"},
		{name: "rewritten topic is not group identity", title: "Upload failure", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { it.Where = ""; it.TitleBy = TitleByModel }},
		{name: "partial group", title: "Example Review Extended", prompt: "Alice Example"},
		{name: "short public text", title: "Example Review", prompt: "Thanks everyone", change: func(s *Service, it *InboxItem) { it.Sender = "Other"; it.Preview = "Thanks everyone" }},
		{name: "full URL", title: "Example Review", prompt: "Inspect https://example.com/issues/7", change: func(s *Service, it *InboxItem) { it.Preview = "https://example.com/issues/7" }, want: true},
		{name: "URL path punctuation is identity", title: "Example Review", prompt: "Inspect https://example.com/issues/7", change: func(s *Service, it *InboxItem) { it.Preview = "https://example.com/issues/7)" }},
		{name: "URL query is identity", title: "Example Review", prompt: "Inspect https://example.com/issues/7?version=2", change: func(s *Service, it *InboxItem) { it.Preview = "https://example.com/issues/7?version=1" }},
		{name: "same domain", title: "Example Review", prompt: "Inspect https://example.com/issues/8", change: func(s *Service, it *InboxItem) { it.Preview = "https://example.com/issues/7" }},
		{name: "two topics", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) {
			other := softTestItem("example:two", it.ArrivedAt)
			other.Last = "message-8"
			other.Chain[0].Messages[0].Text = "A different request about billing errors and missing account records"
			s.inbox.items[other.ID] = other
		}},
		{name: "cross source equivalent", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) {
			other := softTestItem("other:two", it.ArrivedAt)
			other.Source = "other"
			s.inbox.items[other.ID] = other
		}, want: true},
		{name: "shared context insufficient", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) {
			other := softTestItem("other:two", it.ArrivedAt)
			other.Source = "other"
			other.Chain = nil
			other.Context = softTestWords
			s.inbox.items[other.ID] = other
		}},
		{name: "cross source time incompatible", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) {
			other := softTestItem("other:two", it.ArrivedAt.Add(-time.Hour))
			other.Source = "other"
			s.inbox.items[other.ID] = other
		}},
		{name: "bound elsewhere", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { it.Agent = &InboxAgent{TodoID: "other-todo"} }},
		{name: "expired", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { it.ArrivedAt = time.Now().Add(-25 * time.Hour) }},
		{name: "legacy updated time", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { it.ArrivedAt = time.Time{} }, want: true},
		{name: "hidden", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { it.VisibilityExcluded = true }},
		{name: "merged child", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { it.MergedInto = "other" }},
		{name: "filed", title: "Example Review", prompt: "Alice Example", change: func(s *Service, it *InboxItem) { now := time.Now(); it.DoneAt = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, it := softTestService(t)
			if tc.change != nil {
				s.inbox.mu.Lock()
				tc.change(s, it)
				s.inbox.mu.Unlock()
			}
			task := PlanTask{Title: tc.title, Prompt: tc.prompt}
			if err := s.matchPlannedInbox(context.Background(), "box", "chat", &task); err != nil {
				t.Fatal(err)
			}
			if (task.InboxItem != "") != tc.want {
				t.Fatalf("link=%q candidates=%v want bound=%v", task.InboxItem, task.InboxCandidates, tc.want)
			}
			if tc.want && task.InboxMatch == "" {
				t.Fatal("missing persisted provenance")
			}
		})
	}
}
func TestInboxSoftMatchPlanAndStart(t *testing.T) {
	for _, source := range []string{"", "none"} {
		t.Run("source="+source, func(t *testing.T) {
			s, _, it := softTestService(t)
			ctx := context.Background()
			if _, err := s.toolTaskPlanFrom(ctx, "box", "chat", softTestArgs("Example Review", "Alice Example: investigate the report", source)); err != nil {
				t.Fatal(err)
			}
			p := planOf(t, s, "chat")
			want := it.ID
			if source == "none" {
				want = ""
			}
			if p.Tasks[0].InboxItem != want {
				t.Fatalf("planned=%+v", p.Tasks[0])
			}
			started, err := s.startPlan(ctx, p.ID, nil, false)
			if err != nil || started.Tasks[0].Error != "" {
				t.Fatalf("start=%+v err=%v", started, err)
			}
			todo, _ := s.todoNow(started.Tasks[0].TodoID)
			if todo.InboxItem != want {
				t.Fatalf("todo=%+v", todo)
			}
			s.inbox.mu.Lock()
			defer s.inbox.mu.Unlock()
			if want != "" && (it.Agent == nil || it.Agent.TodoID != todo.ID) {
				t.Fatal("missing reverse link")
			}
			if it.DoneAt != nil {
				t.Fatal("planning filed source")
			}
		})
	}
}
func TestInboxSoftMatchDelayedStart(t *testing.T) {
	for _, change := range []string{"filed", "version", "ambiguity", "claimed"} {
		t.Run(change, func(t *testing.T) {
			s, _, it := softTestService(t)
			ctx := context.Background()
			if _, err := s.toolTaskPlanFrom(ctx, "box", "chat", softTestArgs("Example Review", "Alice Example", "")); err != nil {
				t.Fatal(err)
			}
			p := planOf(t, s, "chat")
			if p.Tasks[0].InboxItem == "" {
				t.Fatal("regression: no initial link")
			}
			s.inbox.mu.Lock()
			switch change {
			case "filed":
				now := time.Now()
				it.DoneAt = &now
			case "version":
				it.Last = "new-message"
			case "ambiguity":
				other := softTestItem("other:two", it.ArrivedAt)
				other.Source = "other"
				other.Chain = nil
				s.inbox.items[other.ID] = other
			case "claimed":
				it.Agent = &InboxAgent{TodoID: "winner"}
			}
			s.inbox.mu.Unlock()
			started, err := s.startPlan(ctx, p.ID, nil, false)
			if err != nil || started.Tasks[0].Error != "" {
				t.Fatalf("start=%+v err=%v", started, err)
			}
			got := started.Tasks[0]
			todo, _ := s.todoNow(got.TodoID)
			if got.InboxItem != "" || todo.InboxItem != "" || got.InboxNotice == "" {
				t.Fatalf("stale link retained or no notice: %+v", got)
			}
		})
	}
}
func TestInboxSoftMatchOriginalRequest(t *testing.T) {
	s, f, it := softTestService(t)
	f.setItems(proto.Item{ID: "request-1", Role: "user", Kind: proto.KindText, Text: "Example Review: " + softTestWords})
	if _, err := s.toolTaskPlanFrom(context.Background(), "box", "chat", softTestArgs("Investigate upload", "Verify the regression", "")); err != nil {
		t.Fatal(err)
	}
	if p := planOf(t, s, "chat"); p.Tasks[0].InboxItem != it.ID {
		t.Fatalf("owner request was not matched: %+v", p.Tasks[0])
	}
}
func TestInboxSoftMatchConcurrentAdmission(t *testing.T) {
	s, _, it := softTestService(t)
	ctx := context.Background()
	task := PlanTask{Title: "Example Review", Prompt: "Alice Example"}
	if err := s.matchPlannedInbox(ctx, "box", "chat", &task); err != nil {
		t.Fatal(err)
	}
	todos := make([]Todo, 2)
	for i := range todos {
		var err error
		todos[i], err = s.createTodo(ctx, "Investigate", "", "chat", "")
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range todos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := task
			copy.ReservedTodoID = todos[i].ID
			_, errs[i] = s.linkPlannedInbox(ctx, "chat", copy, todos[i])
		}(i)
	}
	wg.Wait()
	wins := 0
	for i, err := range errs {
		td, _ := s.todoNow(todos[i].ID)
		if err == nil {
			wins++
			if td.InboxItem != it.ID {
				t.Fatal("winner lost source")
			}
		} else if td.InboxItem != "" {
			t.Fatal("loser retained source")
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d errors=%v", wins, errs)
	}
	s.inbox.mu.Lock()
	winner := it.Agent.TodoID
	s.inbox.mu.Unlock()
	td, _ := s.todoNow(winner)
	task.ReservedTodoID = winner
	if _, err := s.linkPlannedInbox(ctx, "chat", task, td); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if todoCount(s) != 2 {
		t.Fatal("retry created todo")
	}
}
func TestInboxSoftMatchUntrustedContext(t *testing.T) {
	s, _, _ := softTestService(t)
	task := PlanTask{Title: "Investigate", Prompt: "Investigate", Context: "Example Review Alice Example"}
	if err := s.matchPlannedInbox(context.Background(), "box", "chat", &task); err != nil {
		t.Fatal(err)
	}
	if task.InboxItem != "" {
		t.Fatal("matched model context")
	}
	if s.inboxPlanNotes(context.Background(), []PlanTask{{InboxNotice: "automatic_link_removed"}}) == "" {
		t.Fatal("missing removal explanation")
	}
}

func TestInboxSoftMatchDualSourcePreservesRoutes(t *testing.T) {
	s, _, it := softTestService(t)
	ctx := context.Background()
	other := softTestItem("other:two", it.ArrivedAt)
	other.Source = "other"
	other.Connector = &InboxConnector{Thread: "original-thread", URL: "https://example.com/message/7"}
	s.inbox.mu.Lock()
	s.inbox.items[other.ID] = other
	s.inbox.mu.Unlock()
	if _, err := s.toolTaskPlanFrom(ctx, "box", "chat", softTestArgs("Example Review", "Alice Example", "")); err != nil {
		t.Fatal(err)
	}
	p, err := s.startPlan(ctx, planOf(t, s, "chat").ID, nil, false)
	if err != nil || p.Tasks[0].Error != "" {
		t.Fatalf("start=%+v %v", p, err)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if len(it.Related) != 1 || it.Related[0].ID != other.ID || len(other.Related) != 1 || other.Related[0].ID != it.ID {
		t.Fatal("dual source navigation missing")
	}
	if other.Agent != nil || other.DoneAt != nil || it.DoneAt != nil || other.Connector.Thread != "original-thread" {
		t.Fatal("secondary route or processing state changed")
	}
}

func TestInboxSoftMatchFailedSaveRollsBack(t *testing.T) {
	st := &taskInboxFailStore{DB: todoTestDB(t)}
	s, f := newTaskServiceOn(t, st)
	f.inbox = newFakeInbox()
	ctx := context.Background()
	conversation := t.Name() + "-" + randomHex(4)
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	it := softTestItem("example:one", time.Now().UTC())
	s.inbox.mu.Lock()
	s.inbox.items[it.ID] = it
	s.inbox.mu.Unlock()
	if s.saveInbox(ctx, []*InboxItem{it})[it.ID] {
		t.Fatal("save fixture")
	}
	if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, softTestArgs("Example Review", "Alice Example", "")); err != nil {
		t.Fatal(err)
	}
	p := planOf(t, s, conversation)
	if p.Tasks[0].InboxItem != it.ID {
		t.Fatal("regression: missing link")
	}
	st.failInbox.Store(true)
	started, err := s.startPlan(ctx, p.ID, nil, false)
	if err != nil || started.Tasks[0].Error == "" || started.Tasks[0].TodoID != "" || todoCount(s) != 0 {
		t.Fatalf("failed save leaked admission: %+v %v", started, err)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it.Agent != nil && it.Agent.TodoID != "" || it.DoneAt != nil {
		t.Fatal("failed save left reverse link")
	}
}

func TestInboxSoftMatchConfirmedHandoffWins(t *testing.T) {
	s, _, it := softTestService(t)
	ctx := context.Background()
	s.inbox.mu.Lock()
	s.inbox.noteHandoff(it, SessionRef{MachineID: "box", SessionID: "chat"}, "confirmed-operation", time.Now())
	s.inbox.mu.Unlock()
	if _, err := s.toolTaskPlanFrom(ctx, "box", "chat", softTestArgs("Example Review", "Alice Example", "")); err != nil {
		t.Fatal(err)
	}
	got := planOf(t, s, "chat").Tasks[0]
	if got.InboxItem != it.ID || got.InboxHandoff != "confirmed-operation" || got.InboxMatch == "" {
		t.Fatalf("handoff lost precedence: %+v", got)
	}
}

func TestInboxSoftMatchMainAndVerifiedDirection(t *testing.T) {
	for _, entry := range []string{"main supervisor", "verified relay", "forged relay"} {
		t.Run(entry, func(t *testing.T) {
			d := newDispatchPolicyHarnessOn(t, nil)
			d.f.inbox = newFakeInbox()
			ctx := context.Background()
			if err := d.s.inboxReady(ctx); err != nil {
				t.Fatal(err)
			}
			it := softTestItem("example:one", time.Now().UTC())
			d.s.inbox.mu.Lock()
			d.s.inbox.items[it.ID] = it
			d.s.inbox.mu.Unlock()
			d.f.setItems(ownerItem("owner-request", "Example Review: "+softTestWords))
			conversation := d.mainID
			var args map[string]any
			if err := json.Unmarshal(softTestArgs("Investigate upload", "Verify the regression", ""), &args); err != nil {
				t.Fatal(err)
			}
			args["tasks"].([]any)[0].(map[string]any)["machine"] = "server"
			if entry == "main supervisor" {
				args["supervisor"] = d.leaderID
			} else {
				result := d.tool(t, d.mainID, "conversation_send", `{"id":"`+d.leaderID+`","text":"Investigate the upload","quote":"latest"}`)
				if result.IsError {
					t.Fatal(result.Content[0].Text)
				}
				item := received(t, d.f, d.mainID)
				if entry == "forged relay" {
					d.s.quotes.mu.Lock()
					d.s.quotes.m = map[string]relayQuote{}
					d.s.quotes.mu.Unlock()
				}
				d.f.setItems(item)
				conversation = d.leaderID
			}
			raw, _ := json.Marshal(args)
			if _, err := d.s.toolTaskPlanFrom(ctx, "server", conversation, raw); err != nil {
				t.Fatal(err)
			}
			p := planOf(t, d.s, conversation)
			want := it.ID
			if entry == "forged relay" {
				want = ""
			}
			if p.Tasks[0].InboxItem != want {
				t.Fatalf("entry=%s source=%q want=%q", entry, p.Tasks[0].InboxItem, want)
			}
		})
	}
}

func TestInboxSoftMatchSurvivesRestart(t *testing.T) {
	st := todoTestDB(t)
	s, f := newTaskServiceOn(t, st)
	f.inbox = newFakeInbox()
	ctx := context.Background()
	conversation := t.Name() + "-" + randomHex(4)
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	it := softTestItem("example:one", time.Now().UTC())
	s.inbox.mu.Lock()
	s.inbox.items[it.ID] = it
	s.inbox.mu.Unlock()
	if s.saveInbox(ctx, []*InboxItem{it})[it.ID] {
		t.Fatal("save fixture")
	}
	if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, softTestArgs("Example Review", "Alice Example", "")); err != nil {
		t.Fatal(err)
	}
	original := planOf(t, s, conversation)
	if original.Tasks[0].InboxMatch == "" {
		t.Fatal("regression: no soft provenance")
	}
	reloaded, rf := newTaskServiceOn(t, st)
	rf.inbox = newFakeInbox()
	if err := reloaded.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := reloaded.startPlan(ctx, original.ID, nil, false)
	if err != nil || p.Tasks[0].Error != "" || p.Tasks[0].InboxItem != it.ID {
		t.Fatalf("restart lost match: %+v %v", p, err)
	}
	todo, _ := reloaded.todoNow(p.Tasks[0].TodoID)
	if todo.InboxItem != it.ID {
		t.Fatal("restart lost task source")
	}
}

func TestInboxSoftMatchMuteAndUnavailableConfig(t *testing.T) {
	s, f, it := softTestService(t)
	ctx := context.Background()
	s.inbox.mu.Lock()
	it.Lark = &InboxLark{FeedID: "example-group", IsGroup: true}
	s.inbox.mutes = []InboxMute{{Source: it.Source, Tag: "chat", Value: "example-group"}}
	s.inbox.mu.Unlock()
	task := PlanTask{Title: "Example Review", Prompt: "Alice Example"}
	if err := s.matchPlannedInbox(ctx, "box", "chat", &task); err != nil || task.InboxItem != "" || len(task.InboxCandidates) != 0 {
		t.Fatalf("muted item matched: %+v %v", task, err)
	}
	f.inbox.mu.Lock()
	f.inbox.configErr = context.DeadlineExceeded
	f.inbox.mu.Unlock()
	if err := s.matchPlannedInbox(ctx, "box", "chat", &task); err != nil || task.InboxItem != "" {
		t.Fatal("unavailable optional lookup blocked a task or linked a source")
	}
}

func TestInboxInferredHandoffCannotOverwriteConcurrentTaskLink(t *testing.T) {
	s, _, it := softTestService(t)
	s.inbox.mu.Lock()
	s.inbox.noteHandoff(it, SessionRef{MachineID: "box", SessionID: "chat"}, "confirmed-operation", time.Now())
	s.inbox.mu.Unlock()
	task := PlanTask{Title: "Example Review", Prompt: "Alice Example"}
	if err := s.matchPlannedInbox(t.Context(), "box", "chat", &task); err != nil {
		t.Fatal(err)
	}
	if task.InboxItem != it.ID {
		t.Fatalf("matching handoff not selected: %+v", task)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	it.Agent = &InboxAgent{TodoID: "another-task"}
	if err := s.inbox.checkTaskInbox("chat", task); err == nil {
		t.Fatal("inferred handoff accepted a task link acquired after matching")
	}
}
func TestInboxOmittedSourceDoesNotSelectHistoricalHandoff(t *testing.T) {
	for _, continuation := range []string{"", "existing-task"} {
		t.Run("continues="+continuation, func(t *testing.T) {
			s, _, it := softTestService(t)
			s.inbox.mu.Lock()
			s.inbox.noteHandoff(it, SessionRef{MachineID: "box", SessionID: "chat"}, "confirmed-operation", time.Now())
			s.inbox.mu.Unlock()
			task := PlanTask{Title: "Unrelated maintenance", Prompt: "Check the sample output", Continues: continuation}
			if continuation != "" {
				task.Title, task.Prompt = "Example Review", "Alice Example"
			}
			if err := s.planInboxSource(t.Context(), "box", "chat", &task); err != nil {
				t.Fatal(err)
			}
			if err := s.matchPlannedInbox(t.Context(), "box", "chat", &task); err != nil {
				t.Fatal(err)
			}
			if task.InboxItem != "" || task.InboxHandoff != "" {
				t.Fatalf("omitted source acquired historical handoff: %+v", task)
			}
		})
	}
}

func TestInboxInferredHandoffKeepsEquivalentTopicSelection(t *testing.T) {
	s, _, it := softTestService(t)
	other := softTestItem("example:earlier", it.ArrivedAt)
	other.Source = "other"
	s.inbox.mu.Lock()
	s.inbox.items[other.ID] = other
	s.inbox.noteHandoff(it, SessionRef{MachineID: "box", SessionID: "chat"}, "confirmed-operation", time.Now())
	s.inbox.mu.Unlock()
	task := PlanTask{Title: "Example Review", Prompt: "Alice Example"}
	if err := s.matchPlannedInbox(t.Context(), "box", "chat", &task); err != nil {
		t.Fatal(err)
	}
	if task.InboxItem != it.ID || task.InboxMatch == "" {
		t.Fatalf("matching handoff lost: %+v", task)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if err := s.inbox.checkTaskInbox("chat", task); err != nil {
		t.Fatalf("equivalent topic rejected: %v", err)
	}
}

func TestInboxInferredHandoffDoesNotBypassCompetingClaim(t *testing.T) {
	s, _, it := softTestService(t)
	other := softTestItem("example:claimed", it.ArrivedAt)
	other.Agent = &InboxAgent{TodoID: "another-task"}
	s.inbox.mu.Lock()
	s.inbox.items[other.ID] = other
	s.inbox.noteHandoff(it, SessionRef{MachineID: "box", SessionID: "chat"}, "confirmed-operation", time.Now())
	s.inbox.mu.Unlock()
	task := PlanTask{Title: "Example Review", Prompt: "Alice Example"}
	if err := s.matchPlannedInbox(t.Context(), "box", "chat", &task); err != nil {
		t.Fatal(err)
	}
	if task.InboxItem != "" {
		t.Fatalf("competing ownership bypassed by inferred handoff: %+v", task)
	}
}
