package assistant

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const runtimeReportWait = `{"kind":"progress","text":"Deployment was verified. Waiting on CI is unrelated.","wait_state":{"waiting_on":"dependency","wait_target":{"kind":"todo","ref":"todo_translation","summary":"Translated copy"},"reason":"The translation is still in progress","until":"2027-02-08T12:00:00+02:00"}}`

func TestReportTaskRuntimeContract(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	a := todo.Agents[0]
	for _, bad := range []string{
		`{"kind":"progress","text":"Invalid phase","phase":"deploy"}`,
		`{"kind":"blocked","text":"Invalid wait kind","wait_state":{"waiting_on":"ci","wait_target":{"ref":"Checks"},"reason":"Checks are running"}}`,
		`{"kind":"progress","text":"Conflicting phase","phase":"working","wait_state":{"waiting_on":"external","wait_target":{"ref":"Publisher"},"reason":"The publisher is processing it"}}`,
		`{"kind":"blocked","text":"Missing target","wait_state":{"waiting_on":"user","wait_target":{"ref":" "},"reason":"Waiting for permission"}}`,
		`{"kind":"blocked","text":"Invalid deadline","wait_state":{"waiting_on":"date","wait_target":{"ref":"Tomorrow"},"reason":"Publication window","until":"tomorrow"}}`,
	} {
		if got := s.agentTool(t.Context(), a.MachineID, a.SessionID, "report_to_supervisor", json.RawMessage(bad)); !got.IsError {
			t.Fatalf("invalid report was accepted: %s", bad)
		}
	}
	if _, exists := runtimeTodoJSON(t, todoNow(s, todo.ID))["phase"]; exists {
		t.Fatal("invalid reports changed the task's phase")
	}
	if got := s.agentTool(t.Context(), a.MachineID, a.SessionID, "report_to_supervisor", json.RawMessage(runtimeReportWait)); got.IsError {
		t.Fatalf("structured report: %+v", got)
	}
	after := todoNow(s, todo.ID)
	fields := runtimeTodoJSON(t, after)
	if fields["phase"] != "waiting" || after.Stage != todo.Stage || after.Delivery != nil {
		t.Fatalf("structured wait was replaced by delivery prose: phase=%v, stage=%q, delivery=%+v", fields["phase"], after.Stage, after.Delivery)
	}
	row, found, err := s.viewedTodo(t.Context(), todo.ID)
	if err != nil || !found {
		t.Fatalf("read reported wait: found=%t, err=%v", found, err)
	}
	if row.View == nil || row.View.Phase != assistantwire.TodoPhaseWaiting || row.Wait == nil ||
		row.Wait.Kind != assistantwire.TodoWaitKindTodoHandoff || row.Wait.HandoffTodoID != "todo_translation" ||
		row.Wait.WaitingOn != "Translated copy" || !row.Wait.Until.Equal(after.WaitState.Until) ||
		row.Supervision.Parked != nil {
		t.Fatalf("structured dependency did not project without a park: view=%+v wait=%+v", row.View, row.Wait)
	}
	s.advanceTaskReply(t.Context(), todo, a.MachineID, a.SessionID, "Merged, deployed and verified.\n交付：已上线并实测")
	if after := todoNow(s, todo.ID); !reflect.DeepEqual(runtimeTodoJSON(t, after)["wait_state"], fields["wait_state"]) ||
		after.Stage != todo.Stage || after.Delivery != nil {
		t.Fatalf("a later reply replaced the structured wait: %+v", after)
	}
	if got := s.agentTool(t.Context(), a.MachineID, a.SessionID, "report_to_supervisor", json.RawMessage(
		`{"kind":"progress","text":"Back at work","phase":"working"}`)); got.IsError {
		t.Fatalf("resuming report: %+v", got)
	}
	fields = runtimeTodoJSON(t, todoNow(s, todo.ID))
	if fields["phase"] != "working" || fields["wait_state"] != nil {
		t.Fatalf("resuming did not clear the wait: %v", fields)
	}
}

func TestStageTaskRuntimeLandedDoesNotWaitForDeploy(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := t.Context()
	if _, err := s.applyScope(ctx, task, scopeChange{
		Change:       "Accept once merged",
		taskContract: taskContract{WorkMode: assistantwire.WorkModeCoordinate, CompletionMode: assistantwire.CompletionModeLanded},
	}); err != nil {
		t.Fatal(err)
	}
	const pr = "https://github.com/example/app/pull/12"
	s.setTaskStage(ctx, task.ID, "review", pr)
	a := task.Agents[0]
	if result := s.agentTool(ctx, a.MachineID, a.SessionID, "report_to_supervisor", json.RawMessage(
		`{"kind":"progress","text":"The change is in review","phase":"reviewing"}`)); result.IsError {
		t.Fatalf("report runtime: %+v", result)
	}
	at := time.Now().UTC()
	if err := s.applyPRStatus(ctx, todoNow(s, task.ID), TodoPRStatus{URL: pr, State: "merged", CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyRuntimeDeploy(ctx, todoNow(s, task.ID), pr, deployResult{State: deployOut, Hub: oldHub}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := todoNow(s, task.ID)
	if got.Phase != assistantwire.TaskPhaseReviewing || got.WaitState != nil || got.DoneAt != nil ||
		got.Delivery == nil || got.Delivery.State != deliveryMerged || got.Delivery.Gate != "" {
		t.Fatalf("merged landed task should await review without a deployment hold: phase=%s wait=%+v delivery=%+v done=%v",
			got.Phase, got.WaitState, got.Delivery, got.DoneAt)
	}
	_, p, err := s.todosViewed(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if fact := p.facts[0]; fact.WorkModeNow != assistantwire.WorkModeCoordinate ||
		fact.CompletionModeNow != assistantwire.CompletionModeLanded {
		t.Fatalf("projection did not read the current brief modes: work=%q completion=%q", fact.WorkModeNow, fact.CompletionModeNow)
	}
}

func TestReportTaskRuntimeSurvivesToggleAndReload(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f, todo := newSupervisedServiceOn(t, st)
	a := todo.Agents[0]
	setSettings(t, s, func(v *Settings) { v.TodosV2 = boolPtr(true) })
	if got := s.agentTool(t.Context(), a.MachineID, a.SessionID, "report_to_supervisor", json.RawMessage(runtimeReportWait)); got.IsError {
		t.Fatalf("structured report: %+v", got)
	}
	read := func(s *Service) map[string]any {
		t.Helper()
		got := (&todoEnv{t: t, s: s, f: f}).get(todo.ID)
		if got == nil {
			t.Fatal("task is missing from the to-do list")
		}
		return runtimeTodoJSON(t, *got)
	}
	want := read(s)
	deadline, _ := time.Parse(time.RFC3339, "2027-02-08T10:00:00Z")
	wait, ok := want["wait_state"].(map[string]any)
	if !ok || want["phase"] != "waiting" || wait["until"] != deadline.Format(time.RFC3339) ||
		wait["waiting_on"] != "dependency" || wait["reason"] != "The translation is still in progress" {
		t.Fatalf("runtime state was not kept: %v", want)
	}
	for _, on := range []bool{false, true} {
		setSettings(t, s, func(v *Settings) { v.TodosV2 = boolPtr(on) })
		// Start without applying the fake hub's default settings over the stored toggle.
		s = New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})
		if err := s.ensureTodosLoaded(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := s.settings(t.Context()).todosV2On(); got != on {
			t.Fatalf("reloaded todos_v2 = %v, want %v", got, on)
		}
		after := read(s)
		for _, key := range []string{"phase", "wait_state", "stage", "delivery"} {
			if !reflect.DeepEqual(after[key], want[key]) {
				t.Fatalf("todos_v2=%v after reload: %s=%v, want %v", on, key, after[key], want[key])
			}
		}
	}
}

func TestReportTypedWorkModeUsesReportKind(t *testing.T) {
	s, _, task := newSupervisedService(t)
	brief, ok := s.committedBriefOfTodo(task.ID)
	if !ok || !brief.WorkModeNow.Valid() {
		t.Fatal("the fixture has no typed work mode")
	}
	a := task.Agents[0]
	raw := json.RawMessage(`{"kind":"progress","text":"The site was deployed and verified. Build #1234."}`)
	if got := s.agentTool(t.Context(), a.MachineID, a.SessionID, "report_to_supervisor", raw); got.IsError {
		t.Fatalf("report: %+v", got)
	}
	if got := todoNow(s, task.ID); got.Phase != assistantwire.TaskPhaseWorking || got.Stage != "develop" || got.Delivery != nil {
		t.Fatalf("typed task did not use the progress report: phase=%s, stage=%s, delivery=%+v", got.Phase, got.Stage, got.Delivery)
	}
}

func TestDoneStructuredDeliveryIgnoresProseAndModelHints(t *testing.T) {
	reply := doneReply("Merged, deployed and verified. Build #1234. You need to approve the rollout.")
	reply.structured = true
	in := taskDoneInput{Delivery: true, Replies: []taskDoneReply{reply}, RuntimeDelivery: &TodoDelivery{State: deliveryPR}}
	model := &cannedJSON{answer: `{"verdict":"done","delivery":"live"}`}
	got, err := judgeTaskDoneWith(t.Context(), model, in)
	if err != nil || got.Verdict != verdictUndelivered || got.Delivery == nil || got.Delivery.State != deliveryPR ||
		got.Signals.MergedPR || got.Signals.Build {
		t.Fatalf("conflicting prose or model hints won: %+v, %v", got, err)
	}
}

func TestStageTaskRuntimeFromPRAndDeployment(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := t.Context()
	const pr = "https://github.com/example/app/pull/12"
	s.setTaskStage(ctx, task.ID, "develop", pr)
	a := task.Agents[0]
	report := func(raw string) {
		t.Helper()
		if got := s.agentTool(ctx, a.MachineID, a.SessionID, "report_to_supervisor", json.RawMessage(raw)); got.IsError {
			t.Fatalf("report rejected: %+v", got)
		}
	}
	report(`{"kind":"progress","text":"A change is ready for review","phase":"reviewing","delivery_pr":"https://github.com/example/app/pull/12"}`)

	hub := &runningHub{commit: oldHub}
	forge := &statusForge{status: TodoPRStatus{State: "open", Draft: true, HeadOID: "head", Checks: "pending"}}
	forge.sha = mergeSHA
	forge.contains = map[string]bool{oldHub: false, newHub: true}
	s.deploy.forge, s.deploy.hubBuild, s.deploy.listPaths = forge, hub.build, []string{}
	at := time.Now().UTC()
	want := func(phase assistantwire.TaskPhase, stage, delivery, waitRef string) {
		t.Helper()
		got := todoNow(s, task.ID)
		ref := ""
		if got.WaitState != nil {
			ref = got.WaitState.Target.Ref
		}
		if got.Phase != phase || got.Stage != stage || got.Delivery == nil || got.Delivery.State != delivery ||
			ref != waitRef || got.DoneAt != nil {
			t.Fatalf("runtime phase=%s wait=%+v stage=%s delivery=%+v done_at=%v", got.Phase, got.WaitState, got.Stage, got.Delivery, got.DoneAt)
		}
	}
	s.sweepPRStatuses(ctx, at)
	want(assistantwire.TaskPhaseWorking, "review", deliveryPR, "")
	forge.status.Draft = false
	at = at.Add(deployRecheck)
	s.sweepPRStatuses(ctx, at)
	want(assistantwire.TaskPhaseWaiting, "review", deliveryPR, pr)
	forge.status.Checks = "success"
	at = at.Add(deployRecheck)
	s.sweepPRStatuses(ctx, at)
	want(assistantwire.TaskPhaseReviewing, "review", deliveryPR, "")

	// A document dependency survives merge and deployment observations for the PR.
	report(runtimeReportWait)
	forge.status.State, forge.merged = "merged", true
	at = at.Add(deployRecheck)
	s.sweepPRStatuses(ctx, at)
	want(assistantwire.TaskPhaseWaiting, "merge", deliveryMerged, "todo_translation")
	at = at.Add(deployRecheck)
	s.sweepDeploys(at)
	want(assistantwire.TaskPhaseWaiting, "merge", deliveryMerged, "todo_translation")

	report(`{"kind":"progress","text":"Translation complete","phase":"working"}`)
	// The same merge still needs the new commit in the running build.
	hub.deploy(newHub)
	at = at.Add(deployRecheck)
	s.sweepDeploys(at)
	want(assistantwire.TaskPhaseReviewing, "deploy", deliveryLive, "")
	hub.deploy(oldHub)
	at = at.Add(deployRecheck)
	s.sweepDeploys(at)
	want(assistantwire.TaskPhaseWaiting, "deploy", deliveryMerged, pr)

	// A delayed successful comparison cannot replace the newer observation.
	if err := s.applyRuntimeDeploy(ctx, task, pr, deployResult{State: deployIn, Hub: newHub}, at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	want(assistantwire.TaskPhaseWaiting, "deploy", deliveryMerged, pr)
}

func TestCloseoutParkUsesReportedWait(t *testing.T) {
	s, f, task := newSupervisedService(t)
	a := task.Agents[0]
	now := time.Now().UTC()
	at := now.Add(time.Hour)
	wait := assistantwire.TaskWaitState{WaitingOn: assistantwire.WaitUser, Target: assistantwire.TaskWaitTarget{Ref: "Publishing permission"},
		Reason: "Confirm the publication", Until: at}
	report := func() {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"kind": "progress", "text": "Waiting for release", "wait_state": wait})
		if err != nil {
			t.Fatal(err)
		}
		if got := s.agentTool(t.Context(), a.MachineID, a.SessionID, "report_to_supervisor", raw); got.IsError {
			t.Fatalf("report: %+v", got)
		}
	}
	c := proto.Session{MachineID: a.MachineID, SessionID: a.SessionID, Status: proto.StatusDone, UpdatedAt: now}
	f.setSessions(a.MachineID, c)
	report()
	if s.parkCloseout(t.Context(), task, c, now) {
		t.Fatal("owner-only decision was automatically parked")
	}
	got, err := s.judgeTaskDone(t.Context(), task)
	if err != nil || got.Verdict != verdictNeedsUser || got.Reason != wait.Reason {
		t.Fatalf("done judge ignored the reported owner wait: %+v, %v", got, err)
	}
	wait.WaitingOn, wait.Target.Ref, wait.Reason = assistantwire.WaitDate, "Publishing window", "The window opens in an hour"
	report()
	if !s.parkCloseout(t.Context(), task, c, now) {
		t.Fatal("structured date did not park without a prose closeout")
	}
	p := todoNow(s, task.ID).Supervision.parking()
	if p == nil || p.WaitingOn != parkDate || p.Reason != wait.Reason || p.Evidence.Target != wait.Target.Ref {
		t.Fatalf("park did not use the reported wait: %+v", p)
	}
}

func TestCIWatchRequiresExactStructuredWaitAndProviderChecks(t *testing.T) {
	const pr = "https://github.com/example/app/pull/12"
	now := time.Now().UTC()
	fallback := now.Add(10 * time.Minute)
	fact := TodoPRStatus{URL: pr, State: "open", HeadOID: "head", Checks: "pending", CheckedAt: now}
	task := Todo{Phase: assistantwire.TaskPhaseWaiting, PRURL: pr, PRStatus: &fact}
	for _, tc := range []struct {
		on     assistantwire.WaitKind
		target string
		checks string
		watch  bool
	}{
		{assistantwire.WaitExternal, pr, "pending", true},
		{assistantwire.WaitExternal, pr, "success", false},
		{assistantwire.WaitExternal, "https://github.com/example/app/pull/13", "pending", false},
		{assistantwire.WaitUser, pr, "pending", false},
		{assistantwire.WaitDependency, pr, "pending", false},
	} {
		fact.Checks = tc.checks
		task.WaitState = &assistantwire.TaskWaitState{WaitingOn: tc.on, Reason: "Waiting for a manual review",
			Target: assistantwire.TaskWaitTarget{Kind: assistantwire.WaitTargetPR, Ref: tc.target}}
		e := db.SessionWork{Work: *taskRuntimeWork(task, "")}
		startRuntimeCIWatch(task, &e, now, fallback)
		if w := e.Work.CI; (w != nil) != tc.watch || w != nil && (w.Number != 12 || w.Repo != "example/app" || !w.FallbackAt.Equal(fallback)) {
			t.Errorf("%+v: CI watch = %+v", tc, w)
		}
	}
}

func runtimeTodoJSON(t *testing.T, todo Todo) map[string]any {
	t.Helper()
	raw, err := json.Marshal(todo)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
