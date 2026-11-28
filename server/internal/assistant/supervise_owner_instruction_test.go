package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func ownerInstructionTask(t *testing.T) (*Service, *fakeHub, Todo, time.Time) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "The original result.", 0)
	waitFor(t, "the task to await review", func() bool {
		cur := todoNow(s, todo.ID)
		return cur.Supervision != nil && cur.Supervision.State == reviewPending
	})
	linked := todoNow(s, todo.ID).Agents[0].LinkedAt
	at := time.Now().UTC()
	if !at.After(linked) {
		at = linked.Add(time.Millisecond)
	}
	return s, f, todo, at
}

func ownerReport(status, missing, waiting string) string {
	var b strings.Builder
	b.WriteString(`<closeout status="` + status + `" merged="n/a" deployed="n/a">` + "\n")
	b.WriteString("did: addressed the owner's instruction\n")
	if missing != "" {
		b.WriteString("missing: " + missing + "\n")
	}
	if waiting != "" {
		b.WriteString("waiting: " + waiting + "\n")
	}
	b.WriteString("</closeout>")
	return b.String()
}

func ownerReportItem(at time.Time, text string) proto.Item {
	return proto.Item{Role: "assistant", Kind: proto.KindText, Text: text, TS: at}
}

func TestOwnerInstructionKeepsLatestPerSession(t *testing.T) {
	v := &Supervision{}
	first := time.Now().UTC()
	second := first.Add(-time.Minute)
	long := strings.Repeat("x", ownerInstructionExcerptMax+50)
	if !v.recordOwnerInstruction("box", "s1", assistantwire.OwnerInputApp, "m1", "first", first) ||
		!v.recordOwnerInstruction("box", "s1", assistantwire.OwnerInputTerminal, "m2", long, second) ||
		!v.recordOwnerInstruction("box", "s2", assistantwire.OwnerInputApp, "m3", "other session", first) {
		t.Fatal("owner instructions were not recorded")
	}
	if len(v.OwnerWork) != 2 {
		t.Fatalf("owner work = %+v", v.OwnerWork)
	}
	work := v.ownerSessionWork("box", "s1")
	if work == nil || work.Instruction == nil || work.Instruction.Excerpt != long[:ownerInstructionExcerptMax]+"…" ||
		!work.Instruction.At.Equal(second) || work.Instruction.Source != assistantwire.OwnerInputTerminal ||
		work.Instruction.ClientMsgID != "m2" || !work.Instruction.NeedsReport {
		t.Fatalf("latest clipped instruction = %+v", work)
	}
	other := v.ownerSessionWork("box", "s2")
	if other == nil || other.Instruction == nil || other.Instruction.Excerpt != "other session" {
		t.Fatalf("other session = %+v", other)
	}
}

func TestOwnerInstructionRedeliveryDoesNotRearmAfterAssignment(t *testing.T) {
	for _, tc := range []struct {
		name, source, clientMsgID string
		redeliveredAt             time.Time
	}{
		{name: "app message id", source: assistantwire.OwnerInputApp, clientMsgID: "m1"},
		{name: "terminal without an id", source: assistantwire.OwnerInputTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &Supervision{}
			at := time.Now().UTC()
			if !v.recordOwnerInstruction("box", "s1", tc.source, tc.clientMsgID, "Keep the check open.", at) {
				t.Fatal("owner instruction was not recorded")
			}
			if !v.recordAssignment("box", "s1", at.Add(time.Minute)) {
				t.Fatal("Persona assignment was not recorded")
			}
			redeliveredAt := at
			if tc.clientMsgID != "" {
				redeliveredAt = at.Add(-time.Hour)
			}
			if v.recordOwnerInstruction("box", "s1", tc.source, tc.clientMsgID, "Keep the check open.", redeliveredAt) {
				t.Fatal("a redelivery of the superseded instruction rearmed the guard")
			}
			work := v.ownerSessionWork("box", "s1")
			if work == nil || work.Instruction == nil || work.Instruction.NeedsReport {
				t.Fatalf("redelivery changed the superseded instruction: %+v", work)
			}
		})
	}
}

func acceptOwnerInstructionTask(t *testing.T, s *Service, todo Todo, extra map[string]any) (string, error) {
	t.Helper()
	fields := map[string]any{
		"todo_id": todo.ID,
		"verdict": reviewAccepted,
		"note":    "Verified against the owner's instruction.",
		"checks":  completeReviewChecks(todoNow(s, todo.ID)),
	}
	for k, v := range extra {
		fields[k] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return s.runTool(context.Background(), "server", "conv1", "task_review", raw)
}

func TestOwnerInstructionRequiresLaterSettledReport(t *testing.T) {
	s, f, todo, at := ownerInstructionTask(t)
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "Please add the migration safety check before closing this.", assistantwire.OwnerInputApp, "", at)

	f.setItems(ownerReportItem(at.Add(-time.Minute), ownerReport("done", "", "")))
	if _, err := acceptOwnerInstructionTask(t, s, todo, nil); err == nil ||
		!strings.Contains(err.Error(), "owner instruction") || !strings.Contains(err.Error(), "no assistant reply or work report followed") {
		t.Fatalf("an old report was accepted after a newer owner instruction: %v", err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.State != reviewPending {
		t.Fatalf("a refused owner-instruction guard changed the task: %+v", got.Supervision)
	}

	s.notePersonaAssignment(context.Background(), "box", "s1", at.Add(time.Minute))
	if _, err := acceptOwnerInstructionTask(t, s, todo, nil); err != nil {
		t.Fatalf("an assignment newer than the instruction did not clear the guard: %v", err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatal("the task did not close after the later assignment")
	}
}

func TestOwnerInstructionUsesAssignmentOrderAcrossClockSkew(t *testing.T) {
	s, f, todo, at := ownerInstructionTask(t)
	s.notePersonaAssignment(context.Background(), "box", "s1", at.Add(time.Minute))
	if work := todoNow(s, todo.ID).Supervision.ownerSessionWork("box", "s1"); work == nil || work.AssignedAt.IsZero() {
		t.Fatalf("prior Persona assignment was not recorded: %+v", work)
	}

	// The linked node's clock is a day ahead of the hub. That must not make the owner's later event look older than
	// Persona's assignment.
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Agents[0].LinkedAt = at.Add(24 * time.Hour)
	s.todos.mu.Unlock()
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "Keep the migration check open.", assistantwire.OwnerInputApp, "owner-message-clock-skew", at)
	if work := todoNow(s, todo.ID).Supervision.ownerSessionWork("box", "s1"); work == nil || work.Instruction == nil || !work.Instruction.NeedsReport {
		t.Fatalf("owner instruction after assignment did not require a report: %+v", work)
	}

	f.setItems(ownerReportItem(at.Add(-time.Minute), ownerReport("done", "", "")))
	if _, err := acceptOwnerInstructionTask(t, s, todo, nil); err == nil ||
		!strings.Contains(err.Error(), "owner instruction") || !strings.Contains(err.Error(), "no assistant reply or work report followed") {
		t.Fatalf("clock skew bypassed the owner-instruction guard: %v", err)
	}

	s.notePersonaAssignment(context.Background(), "box", "s1", at.Add(2*time.Minute))
	if work := todoNow(s, todo.ID).Supervision.ownerSessionWork("box", "s1"); work == nil || work.Instruction == nil || work.Instruction.NeedsReport {
		t.Fatalf("later Persona assignment did not clear the report requirement: %+v", work)
	}
	if _, err := acceptOwnerInstructionTask(t, s, todo, nil); err != nil {
		t.Fatalf("later Persona assignment did not clear the guard: %v", err)
	}
}

func TestOwnerInstructionReportDispositions(t *testing.T) {
	for _, tc := range []struct {
		name, report, want string
	}{
		{"no report", "plain progress without a closeout", "latest reply after the instruction has no work report"},
		{"not done", ownerReport("blocked", "", "the migration owner"), "not done"},
		{"waiting", ownerReport("done", "", "the release owner"), "Waiting"},
		{"missing", ownerReport("done", "the release owner", ""), "Missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			agentTurn(s, f, proto.StatusWorking, "", 0)
			agentTurn(s, f, proto.StatusDone, tc.report, 0)
			waitFor(t, "the task to await review", func() bool {
				cur := todoNow(s, todo.ID)
				return cur.Supervision != nil && cur.Supervision.State == reviewPending
			})
			linked := todoNow(s, todo.ID).Agents[0].LinkedAt
			at := linked.Add(time.Second)
			s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "Please verify the owner requirement.", assistantwire.OwnerInputTerminal, "", at)
			f.setItems(ownerReportItem(at.Add(time.Second), tc.report))
			if _, err := acceptOwnerInstructionTask(t, s, todo, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("accepted with %q: %v", tc.report, err)
			}
		})
	}
}

func TestOwnerInstructionCleanLaterReportAllowsAcceptance(t *testing.T) {
	s, f, todo, at := ownerInstructionTask(t)
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "Please add the migration safety check.", assistantwire.OwnerInputApp, "owner-message-1", at)
	f.setItems(
		proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: "owner-message-1", Text: "Please add the migration safety check."},
		proto.Item{Seq: 2, Role: "assistant", Kind: proto.KindText, Text: ownerReport("done", "", "")},
	)
	if res, err := acceptOwnerInstructionTask(t, s, todo, nil); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("a clean later report was not accepted: %s %v", res, err)
	}
}

func TestOwnerInstructionScopeUpdateUsesCriteriaGuard(t *testing.T) {
	s, f, todo, at := ownerInstructionTask(t)
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "Please add the migration safety check.", assistantwire.OwnerInputApp, "", at)
	f.setItems(ownerReportItem(at.Add(-time.Minute), ownerReport("done", "", "")))

	if _, err := s.toolTaskScope(context.Background(), "", "conv1", json.RawMessage(`{"todo_id":"`+todo.ID+`","change":"Add the migration safety check to acceptance","why":"The owner asked for it in the Agent session","done_when":["PR 合并到 master","浅色 / 深色截图贴进 PR","The migration is safe"]}`)); err != nil {
		t.Fatalf("scope update: %v", err)
	}
	cur := todoNow(s, todo.ID)
	work := cur.Supervision.ownerSessionWork("box", "s1")
	if work == nil || !work.AssignedAt.After(at) {
		t.Fatalf("scope update did not record a newer Persona assignment: %+v", work)
	}
	if _, err := acceptOwnerInstructionTask(t, s, todo, map[string]any{"checks": completeReviewChecks(cur)[:2]}); err == nil ||
		!strings.Contains(err.Error(), "has no check") {
		t.Fatalf("the new criterion was not evaluated by the existing guard: %v", err)
	}
	if _, err := acceptOwnerInstructionTask(t, s, todo, nil); err != nil {
		t.Fatalf("criteria-covered owner instruction was refused: %v", err)
	}
}

func TestOwnerInstructionUserCheckIsExplicitOwnerConfirmation(t *testing.T) {
	s, _, todo, at := ownerInstructionTask(t)
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "Please check the migration before closing.", assistantwire.OwnerInputApp, "", at)
	if res, err := acceptOwnerInstructionTask(t, s, todo, map[string]any{"user_check": true}); err != nil ||
		!strings.Contains(res, "user_check") {
		t.Fatalf("explicit owner verification was refused: %s %v", res, err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.State != reviewUserCheck {
		t.Fatalf("owner confirmation did not keep the task for the owner: %+v", got.Supervision)
	}
}

func TestTaskLeaderAcceptsOnlyAfterNewerOwnerInstructionIsResolved(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	a := w.Agents[0]
	at := a.LinkedAt.Add(time.Second)
	s.noteOwnerInstruction(ctx, SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, "Please cover the owner's new case.", assistantwire.OwnerInputApp, "", at)
	before := todoNow(s, w.ID)
	res := s.mcpCall(ctx, "box", "s1", "task_review", mustJSON(map[string]any{
		"todo_id": w.ID, "verdict": "accepted", "checks": completeReviewChecks(before),
	}), false)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "owner instruction") ||
		!strings.Contains(res.Content[0].Text, "conversation above") {
		t.Fatalf("a task leader accepted through a newer owner instruction: %+v", res)
	}
	after := todoNow(s, w.ID)
	if after.DoneAt != nil || len(after.Supervision.Reviews) != len(before.Supervision.Reviews) {
		t.Fatalf("the refused leader acceptance changed the worker: %+v", after.Supervision)
	}
}

func TestOwnerInstructionIgnoresUnverifiedInput(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, ownerReport("not_done", "still working", ""), 0)
	waitFor(t, "the task to await review", func() bool {
		cur := todoNow(s, todo.ID)
		return cur.Supervision != nil && cur.Supervision.State == reviewPending
	})
	linked := todoNow(s, todo.ID).Agents[0].LinkedAt
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, "This is a generated note.", "", "", linked.Add(time.Second))
	if work := todoNow(s, todo.ID).Supervision.ownerSessionWork("box", "s1"); work != nil && work.Instruction != nil {
		t.Fatalf("unverified input was recorded as owner-authored: %+v", work.Instruction)
	}
}

func TestOwnerInstructionSurvivesRestart(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "No later report.", 0)
	waitFor(t, "the task to await review", func() bool {
		cur := todoNow(s, todo.ID)
		return cur.Supervision != nil && cur.Supervision.State == reviewPending
	})
	at := todoNow(s, todo.ID).Agents[0].LinkedAt.Add(time.Second)
	text := "Keep the owner's migration check open."
	s.noteOwnerInstruction(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, text, assistantwire.OwnerInputApp, "", at)

	reloaded := newServiceOn(t, f, database)
	reloaded.id = s.id
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	work := todoNow(reloaded, todo.ID).Supervision.ownerSessionWork("box", "s1")
	if work == nil || work.Instruction == nil || !work.Instruction.NeedsReport || work.Instruction.Excerpt != text || work.Instruction.Source != assistantwire.OwnerInputApp {
		t.Fatalf("owner instruction did not survive restart: %+v", work)
	}
}

func TestTodoSupervisionPersistsOwnerWork(t *testing.T) {
	database := todoTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := database.CreateTodo(ctx, db.TodoRow{ID: "owner-work", Title: "Owner work", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]ownerSessionWork{{
		MachineID: "box", SessionID: "s1", AssignedAt: now.Add(time.Minute),
		Instruction: &ownerInstruction{At: now, Source: assistantwire.OwnerInputTerminal, Excerpt: "keep this instruction", NeedsReport: true},
	}})
	if err := database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: "owner-work", Supervisor: "conv1", Reviews: json.RawMessage("[]"), OwnerWork: raw, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListTodoSupervision(ctx)
	if err != nil || len(rows) != 1 || !strings.Contains(string(rows[0].OwnerWork), "keep this instruction") {
		t.Fatalf("owner work after reload: %+v (%v)", rows, err)
	}
}
