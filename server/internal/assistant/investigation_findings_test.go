package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/proto"
)

func findingTask(t *testing.T, st Store) (*Service, Todo) {
	t.Helper()
	s, _, todo := findingTaskWithHub(t, st)
	return s, todo
}

func findingTaskWithHub(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	holdDigest(s)
	raw := json.RawMessage(`{"tasks":[{"title":"Investigate request problems","prompt":"Find and classify each error.","goal":"A list of findings","machine":"box","cwd":"/code/app","readonly":true,"multi_issue_report":true}]}`)
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "investigation Agent linked", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if todo.Supervision == nil || todo.Supervision.Investigation == nil || todo.Supervision.Investigation.Version != 0 {
		t.Fatalf("explicit plan did not opt in: %+v", todo.Supervision)
	}
	return s, f, todo
}

func reportFindings(t *testing.T, s *Service, todo Todo, findings []investigationFinding) *investigationFindings {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": "done", "text": "Investigation report is ready.", "findings": findings})
	if err != nil {
		t.Fatal(err)
	}
	worker := todo.Agents[0]
	if _, err := s.reportToSupervisor(context.Background(), worker.MachineID, worker.SessionID, raw, time.Now()); err != nil {
		t.Fatal(err)
	}
	return todoNow(s, todo.ID).Supervision.Investigation
}

func confirmTestFindings(s *Service, id string, version uint64, dispositions ...findingDisposition) (string, error) {
	return reviewErr(s, id, map[string]any{
		"verdict": reviewFindings, "findings_review": findingReview{Version: version, Dispositions: dispositions},
	})
}

func setFindingTestCriterion(t *testing.T, s *Service, id, criterion string) {
	t.Helper()
	if _, _, err := s.updateSupervision(context.Background(), id, func(v *Supervision) bool {
		v.DoneWhen = append(v.DoneWhen, criterion)
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

func completeTestFinding(t *testing.T, s *Service, id string, version uint64, finding investigationFinding, evidence string) {
	t.Helper()
	setFindingTestCriterion(t, s, id, finding.Next)
	if _, err := reviewErr(s, id, map[string]any{
		"verdict":         reviewFindings,
		"findings_review": findingReview{Version: version, Dispositions: []findingDisposition{{ID: finding.ID, Done: evidence}}},
		"checks":          []ReviewCheck{{Criterion: finding.Next, Met: true, Evidence: evidence}},
	}); err != nil {
		t.Fatal(err)
	}
}

var (
	investigateRollout = investigationFinding{ID: "rollout", Summary: "External rollout", Next: "Check rollout result", Source: "report#rollout"}
	investigateLogs    = investigationFinding{ID: "logs", Summary: "Missing detailed log", Next: "Diagnose the missing log", Source: "report#logs"}
)

func TestInvestigationFindingsParkAndAcceptRequireEveryDisposition(t *testing.T) {
	ctx := context.Background()
	s, todo := findingTask(t, todoTestDB(t))
	park := waitingPark()
	accept := map[string]any{"verdict": reviewAccepted, "note": "The report is delivered."}
	mustBlock := func(args map[string]any, reason string) {
		t.Helper()
		if _, err := reviewErr(s, todo.ID, args); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("review should fail with %q, got %v", reason, err)
		}
		got := todoNow(s, todo.ID)
		if got.DoneAt != nil || got.Supervision.State != "" || len(got.Supervision.Reviews) != 0 {
			t.Fatalf("refused review wrote state: %+v", got.Supervision)
		}
	}
	mustBlock(park, "Review the current investigation")
	r := reportFindings(t, s, todo, []investigationFinding{investigateRollout, investigateLogs})
	if again := reportFindings(t, s, todo, []investigationFinding{investigateRollout}); again.Version != r.Version || len(again.Items) != 2 {
		t.Fatalf("replayed subset changed the list: %+v", again)
	}
	mustBlock(park, "Review the current investigation")
	if _, err := confirmTestFindings(s, todo.ID, r.Version+1); err == nil || !strings.Contains(err.Error(), "current version") {
		t.Fatalf("stale review result: %v", err)
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "rollout", WaitTarget: "design team"}); err != nil {
		t.Fatal(err)
	}
	mustBlock(park, "logs (Missing detailed log)")
	follow, err := s.newTodo(ctx, "Investigate logs", "", "conv1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "logs", FollowUp: follow.ID}); err == nil {
		t.Fatal("unrelated follow-up was accepted")
	}
	if err := s.superviseTodo(ctx, follow.ID, "conv1", []string{investigateLogs.Next}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "logs", FollowUp: follow.ID}); err != nil {
		t.Fatal(err)
	}
	mustBlock(accept, "rollout (External rollout)")
	if _, err := reviewErr(s, todo.ID, park); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, todo.ID); got.Supervision.state(got) != reviewParked || got.DoneAt != nil {
		t.Fatalf("valid park did not stick: %+v", got.Supervision)
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "rollout", Done: "Done."}); err == nil {
		t.Fatal("free-form text was accepted as completion evidence")
	}
	completeTestFinding(t, s, todo.ID, r.Version, investigateRollout, "Observed the new rollout result.")
	accept["checks"] = completeReviewChecks(todoNow(s, todo.ID))
	if _, err := reviewErr(s, todo.ID, accept); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatal("investigation did not close after disposition")
	}
	rows, err := s.supervisionDB().ListTodoSupervision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.TodoID == todo.ID {
			found = true
			got := supervisionFromRow(row).Investigation
			if got == nil || len(got.Items) != 2 || got.Items[1].Disposition.FollowUp != follow.ID {
				t.Fatalf("durable handoff was lost: %+v", got)
			}
		}
	}
	if !found {
		t.Fatal("investigation ledger did not survive a database read")
	}
	if _, err := reviewErr(s, follow.ID, map[string]any{"verdict": reviewCancelled, "note": "Cancel"}); err == nil {
		t.Fatal("cancelling an unresolved receiver concealed the handoff")
	}
	if _, err := reviewErr(s, follow.ID, map[string]any{"verdict": reviewAccepted, "checks": completeReviewChecks(todoNow(s, follow.ID))}); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, follow.ID); got.DoneAt == nil {
		t.Fatal("a completed handoff could not close")
	}
}

func TestInvestigationFindingsOwnerPassRemainsAnswerable(t *testing.T) {
	ctx := context.Background()
	s, _, todo := acceptedForUserCheck(t)
	r := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
	ask := askOf(s, todo.ID)
	if ask == nil {
		t.Fatal("no verification request")
	}
	if result, err := s.acceptTry(ctx, todo.ID, ask.MessageID); err == nil || result.taken {
		t.Fatalf("unconfirmed findings were passed: %+v, %v", result, err)
	}
	if current := askOf(s, todo.ID); current == nil || !current.AcceptedAt.IsZero() {
		t.Fatal("blocked Pass consumed the verification request")
	}
	completeTestFinding(t, s, todo.ID, r.Version, investigateLogs, "Verified request log exists.")
	if result, err := s.acceptTry(ctx, todo.ID, ask.MessageID); err != nil || !result.taken {
		t.Fatalf("owner could not Pass after disposition: %+v, %v", result, err)
	}
}

func TestInvestigationFindingsAutomaticParkAndClosure(t *testing.T) {
	ctx := context.Background()
	s, _, todo, c, at := automaticFixture(t, automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil))
	_, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.Supervisor = "conv1"
		v.Investigation = &investigationFindings{Version: 1, Confirmed: 1, ReportedAt: time.Now().UTC(),
			Items: []recordedFinding{{investigationFinding: investigateLogs}}}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	todo = todoNow(s, todo.ID)
	if s.parkCloseout(ctx, todo, c, at) {
		t.Fatal("automatic park hid an unresolved finding")
	}
	if closed, err := s.autoDoneTodo(ctx, todo.ID, "complete", at); err == nil || closed {
		t.Fatalf("automatic completion ignored finding: %t %v", closed, err)
	}
	yes := true
	if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Done: &yes}); err == nil {
		t.Fatal("manual completion ignored finding")
	}
	setFindingTestCriterion(t, s, todo.ID, investigateLogs.Next)
	evidence := "Found and verified the trace."
	review := &findingReview{Version: 1, Dispositions: []findingDisposition{{ID: "logs", Done: evidence}}}
	checks := []ReviewCheck{{Criterion: investigateLogs.Next, Met: true, Evidence: evidence}}
	if _, err := s.reviewInvestigationFindings(ctx, todo, review, checks, "conv1", func(Todo) bool { return true }); err != nil {
		t.Fatal(err)
	}
	todo = todoNow(s, todo.ID)
	if !s.parkCloseout(ctx, todo, c, at) {
		t.Fatal("resolved investigation could not park on documented external wait")
	}
}

func TestInvestigationFindingsFreshReportAndOwnerDrop(t *testing.T) {
	ctx := context.Background()
	d := todoTestDB(t)
	s, todo := findingTask(t, d)
	first := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
	if _, err := confirmTestFindings(s, todo.ID, first.Version); err != nil {
		t.Fatal(err)
	}
	s.supervisedWorking(ctx, todoNow(s, todo.ID))
	next := reportFindings(t, s, todo, []investigationFinding{})
	if next.Version <= first.Version || len(next.Items) != 1 {
		t.Fatalf("new report lost its earlier unresolved finding: %+v", next)
	}
	if _, err := confirmTestFindings(s, todo.ID, first.Version); err == nil {
		t.Fatal("confirmed a superseded version after a new report was recorded")
	}
	writeDrop := func(id, source string) {
		t.Helper()
		if _, err := s.insertDecision(ctx, d, db.TodoDecisionRow{
			ID: id, TodoID: todo.ID, Question: "Drop investigation finding logs?", Choice: "Drop",
			Reason: "This sample is no longer used.", Source: source, SourceMachine: "box", SourceSession: "conv1", CreatedAt: decisionNow(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	writeDrop("dec_finding_by_agent", db.DecisionAgent)
	if _, err := confirmTestFindings(s, todo.ID, next.Version, findingDisposition{ID: "logs", Drop: "dec_finding_by_agent"}); err == nil {
		t.Fatal("an agent's decision dropped the finding")
	}
	writeDrop("dec_relayed_as_manual", db.DecisionManual)
	if _, err := confirmTestFindings(s, todo.ID, next.Version, findingDisposition{ID: "logs", Drop: "dec_relayed_as_manual"}); err == nil {
		t.Fatal("a model-attributed manual record dropped the finding")
	}
	request := httptest.NewRequest("POST", "/api/v1/todos/"+todo.ID+"/decisions",
		strings.NewReader(`{"question":"Drop investigation finding logs?","choice":"Drop","reason":"This sample is no longer used."}`))
	request.Header.Set(RoleHeader, roleFull)
	request.Header.Set(assistantwire.PersonHeader, "1")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	var owner struct {
		Decision db.TodoDecisionRow `json:"decision"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &owner) != nil || owner.Decision.ID == "" {
		t.Fatalf("owner manual decision was not recorded: %d %s", response.Code, response.Body)
	}
	if _, err := confirmTestFindings(s, todo.ID, next.Version, findingDisposition{ID: "logs", Drop: owner.Decision.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewAccepted, "note": "Report accepted."}); err != nil {
		t.Fatal(err)
	}
}

func TestInvestigationFindingsExplicitEmptyAndLegacy(t *testing.T) {
	s, todo := findingTask(t, nil)
	r := reportFindings(t, s, todo, []investigationFinding{})
	if _, err := confirmTestFindings(s, todo.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewErr(s, todo.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	legacy, _, old := newSupervisedService(t)
	if old.Supervision.Investigation != nil {
		t.Fatal("ordinary task unexpectedly requires an investigation report")
	}
	if _, err := reviewErr(legacy, old.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
}

func TestInvestigationFindingsStaleDeadWorkerRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("orphaned_stale_findings_allow_review_and_brief_accept_422_then_200", func(t *testing.T) {
		s, _, todo := acceptedForUserCheck(t)
		r := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
		s.supervisedWorking(ctx, todoNow(s, todo.ID))
		if got := todoNow(s, todo.ID).Supervision.Investigation; !got.Stale {
			t.Fatal("expected findings to be marked stale")
		}
		ask := askOf(s, todo.ID)
		if ask == nil {
			t.Fatal("missing user_check acceptance ask")
		}
		// Before orphan watch, worker is not known gone, so HTTP Pass must still fail with 422 (never 502).
		reqBody := `{"asked_at":"` + ask.AskedAt.UTC().Format(timeKeyLayout) + `"}`
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/todos/"+todo.ID+"/accept", strings.NewReader(reqBody)))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("HTTP /accept with pending findings = %d %s, want 422", rec.Code, rec.Body.String())
		}
		var errBody map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
			t.Fatalf("decode 422 body: %v", err)
		}
		if errBody["code"] != "investigation_findings_pending" || errBody["delivered"] != "none" {
			t.Fatalf("unexpected 422 body: %+v", errBody)
		}

		// Once orphanWatch marks the unlisted agent as orphanClosed, the supervisor can review and dispose of stale findings.
		s.orphanWatch(ctx, time.Now().Add(orphanReconnectWait+time.Minute))
		if o, ok := s.orphanOf(todo.ID); !ok || o.Reason != orphanClosed {
			t.Fatalf("expected orphanClosed, got %+v ok=%v", o, ok)
		}
		completeTestFinding(t, s, todo.ID, r.Version, investigateLogs, "Verified follow-up coverage.")
		got := todoNow(s, todo.ID).Supervision.Investigation
		if got.Stale || got.Confirmed != r.Version {
			t.Fatalf("stale review did not restore current confirmed findings: %+v", got)
		}

		rec2 := httptest.NewRecorder()
		s.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/v1/todos/"+todo.ID+"/accept", strings.NewReader(reqBody)))
		if rec2.Code != http.StatusOK {
			t.Fatalf("HTTP /accept after stale findings disposition = %d %s, want 200", rec2.Code, rec2.Body.String())
		}
		if todoNow(s, todo.ID).DoneAt == nil {
			t.Fatal("to-do did not close after owner Pass")
		}
	})

	t.Run("stopped_worker_allows_stale_cancel_while_handoff_receiver_stays_protected", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, todoTestDB(t))
		r := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
		follow, err := s.newTodo(ctx, "Follow up logs", "", "conv1", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.superviseTodo(ctx, follow.ID, "conv1", []string{investigateLogs.Next}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "logs", FollowUp: follow.ID}); err != nil {
			t.Fatal(err)
		}
		s.supervisedWorking(ctx, todoNow(s, todo.ID))
		// Mark the worker archived so todoAgentStopped confirms no live worker remains.
		worker := todoNow(s, todo.ID).Agents[0]
		f.mu.Lock()
		f.sessions[worker.MachineID] = []proto.Session{{SessionID: worker.SessionID, Archived: true}}
		f.mu.Unlock()

		if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewCancelled, "note": "Findings already tracked in follow-up"}); err != nil {
			t.Fatalf("cancelling stale investigation with dead worker failed: %v", err)
		}
		closed := todoNow(s, todo.ID)
		if closed.DoneAt == nil || closed.DoneBy != db.DoneByCancelled {
			t.Fatalf("expected cancelled closed todo, got done_at=%v done_by=%q", closed.DoneAt, closed.DoneBy)
		}
		// Handoff receiver still cannot be cancelled without completion evidence.
		if _, err := reviewErr(s, follow.ID, map[string]any{"verdict": reviewCancelled, "note": "Drop receiver"}); err == nil {
			t.Fatal("cancelling unresolved handoff receiver unexpectedly succeeded")
		}
	})

}

func TestInvestigationFindingsStaleLiveWorkerReview(t *testing.T) {
	s, todo, report := staleFindingsWithLiveWorker(t)
	accept := map[string]any{"verdict": reviewAccepted, "note": "Accept reviewed findings"}
	if _, err := reviewErr(s, todo.ID, accept); err == nil || !strings.Contains(err.Error(), "Review the current investigation") {
		t.Fatalf("accepting unreviewed stale findings: %v", err)
	}
	if _, err := confirmTestFindings(s, todo.ID, report.Version+1); err == nil || !strings.Contains(err.Error(), "current version") {
		t.Fatalf("review with a mismatched version: %v", err)
	}
	if got := todoNow(s, todo.ID).Supervision.Investigation; !got.Stale || got.Confirmed == report.Version {
		t.Fatalf("mismatched review changed findings: %+v", got)
	}
	const evidence = "Verified the log source."
	completeTestFinding(t, s, todo.ID, report.Version, investigateLogs, evidence)
	conflicting := findingDisposition{ID: investigateLogs.ID, Done: evidence, WaitTarget: "another team"}
	if _, err := confirmTestFindings(s, todo.ID, report.Version, conflicting); err == nil ||
		!strings.Contains(err.Error(), "only one disposition") {
		t.Fatalf("review with an invalid disposition: %v", err)
	}
	got := todoNow(s, todo.ID).Supervision.Investigation
	if got.Stale || got.Confirmed != report.Version || got.Items[0].Disposition.Done != evidence {
		t.Fatalf("expected confirmed findings with a disposition: %+v", got)
	}
}

func TestInvestigationFindingsStaleLiveWorkerCancel(t *testing.T) {
	s, todo, _ := staleFindingsWithLiveWorker(t)
	yes := true
	if _, err := s.editTodo(context.Background(), todo.ID, db.TodoEdit{Done: &yes}); err == nil {
		t.Fatal("manual completion bypassed the stale findings guard")
	}
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewCancelled, "note": "Stop this investigation"}); err != nil {
		t.Fatalf("cancelling stale investigation with a live worker: %v", err)
	}
	closed := todoNow(s, todo.ID)
	if closed.DoneAt == nil || closed.DoneBy != db.DoneByCancelled || !closed.Supervision.Investigation.Stale {
		t.Fatalf("expected cancelled to-do preserving stale findings, got done_at=%v done_by=%q findings=%+v",
			closed.DoneAt, closed.DoneBy, closed.Supervision.Investigation)
	}
}

func staleFindingsWithLiveWorker(t *testing.T) (*Service, Todo, *investigationFindings) {
	t.Helper()
	ctx := context.Background()
	s, f, todo := findingTaskWithHub(t, todoTestDB(t))
	worker := todo.Agents[0]
	f.mu.Lock()
	f.sessions[worker.MachineID] = []proto.Session{{SessionID: worker.SessionID, Status: proto.StatusWorking}}
	f.mu.Unlock()
	report := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
	cancel := map[string]any{"verdict": reviewCancelled, "note": "Stop this investigation"}
	if _, err := reviewErr(s, todo.ID, cancel); err == nil || !strings.Contains(err.Error(), "Review the current investigation") {
		t.Fatalf("cancelling a current unreviewed report: %v", err)
	}
	s.supervisedWorking(ctx, todoNow(s, todo.ID))
	todo = todoNow(s, todo.ID)
	if got := todo.Supervision.Investigation; !got.Stale || got.Version != report.Version {
		t.Fatalf("expected the recorded version to become stale: %+v", got)
	}
	if _, gone := s.investigationWorkerGone(ctx, todo); gone {
		t.Fatal("test requires a worker that is still live")
	}
	return s, todo, report
}

func TestInvestigationAcceptanceRequiresFindingDecisionDestination(t *testing.T) {
	s, todo := findingTask(t, nil)
	finding := investigateLogs
	r := reportFindings(t, s, todo, []investigationFinding{finding})
	accept := func() error {
		_, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewAccepted, "user_check": true})
		return err
	}
	if err := accept(); err == nil {
		t.Fatal("unreviewed investigation accepted for verification")
	}
	decision := createRequestAndPiece(t, s, "conv1", "Decide investigation follow-up", "", todo.ID, nil)
	disposition := findingDisposition{ID: finding.ID, NeedsUser: decision.ID}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, disposition); err == nil {
		t.Fatal("recorded a destination without a decision card")
	}
	if err := accept(); err == nil {
		t.Fatal("a to-do without an owner decision card satisfied the disposition")
	}
	if _, err := reviewErr(s, decision.ID, map[string]any{
		"verdict": "needs_user", "why_owner": "irreversible", "reason": finding.Next, "recommend": "Proceed",
		"options":         []DecisionOption{{Label: "Proceed", LeadsTo: "Start the follow-up"}, {Label: "Drop", LeadsTo: "Leave it as is"}},
		"cost_of_waiting": "The follow-up remains open",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, disposition); err != nil {
		t.Fatal(err)
	}
	if err := accept(); err != nil {
		t.Fatalf("valid owner decision did not satisfy finding: %v", err)
	}
	ask := askOf(s, todo.ID)
	if ask == nil {
		t.Fatal("no verification request")
	}
	if got, err := s.acceptTry(t.Context(), todo.ID, ask.MessageID); err != nil || !got.taken {
		t.Fatalf("owner could not verify investigation: %+v %v", got, err)
	}
	child := todoNow(s, decision.ID)
	if ask, ok := s.askCopy(decision.ID); !ok || !decisionOpen(&ask, child) {
		t.Fatal("accepting the investigation retired the separate decision")
	}
}

func TestInvestigationFindingsResumedOwnerPassStillChecksFindings(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		state string
	}{
		{name: "resumed_pass_while_user_check_and_done", state: reviewUserCheck},
		{name: "resumed_pass_while_accepted_unverified_and_done", state: reviewAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, todo := acceptedForUserCheck(t)
			r := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
			ask := askOf(s, todo.ID)
			if ask == nil {
				t.Fatal("no verification request")
			}
			now := time.Now().UTC()
			s.accept.mu.Lock()
			s.accept.asks[todo.ID].AcceptedAt = now
			s.accept.mu.Unlock()
			s.todos.mu.Lock()
			s.todos.todos[todo.ID].DoneAt = &now
			s.todos.todos[todo.ID].Supervision.State = tc.state
			s.todos.todos[todo.ID].stageVerified = false
			s.todos.mu.Unlock()

			result, err := s.acceptTry(ctx, todo.ID, "")
			if err == nil || !isInvestigationGuardError(err) || result.taken {
				t.Fatalf("resumed Pass bypassed investigation check: result=%+v err=%v", result, err)
			}
			afterBlocked := todoNow(s, todo.ID)
			if afterBlocked.Supervision.State != tc.state || afterBlocked.stageVerified {
				t.Fatalf("blocked resumed Pass mutated task state: state=%q stageVerified=%v", afterBlocked.Supervision.State, afterBlocked.stageVerified)
			}

			completeTestFinding(t, s, todo.ID, r.Version, investigateLogs, "Verified request log exists.")
			result, err = s.acceptTry(ctx, todo.ID, "")
			if err != nil || !result.taken {
				t.Fatalf("resumed Pass failed after findings disposition: result=%+v err=%v", result, err)
			}
			afterOK := todoNow(s, todo.ID)
			if afterOK.Supervision.State != reviewAccepted || !afterOK.stageVerified {
				t.Fatalf("resumed Pass did not finish verification: state=%q stageVerified=%v", afterOK.Supervision.State, afterOK.stageVerified)
			}
			// Already fully processed Pass stays idempotent.
			again, err := s.acceptTry(ctx, todo.ID, "")
			if err != nil || again.taken {
				t.Fatalf("completed Pass was not idempotent: result=%+v err=%v", again, err)
			}
		})
	}
}

func TestInvestigationFindingsParentDeleteBlockedUntilFollowUpResolved(t *testing.T) {
	ctx := context.Background()
	s, todo := findingTask(t, todoTestDB(t))
	r := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
	follow, err := s.newTodo(ctx, "Follow up missing log", "", "conv1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, follow.ID, "conv1", []string{investigateLogs.Next}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "logs", FollowUp: follow.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewAccepted, "note": "Handed off"}); err != nil {
		t.Fatal(err)
	}

	if err := s.deleteTodo(ctx, todo.ID); err == nil || !isInvestigationGuardError(err) {
		t.Fatalf("deleting parent with unresolved follow-up succeeded or wrong error: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/todos/"+todo.ID, nil)
	req.Header.Set(RoleHeader, roleFull)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE parent with unresolved follow-up = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if _, ok := s.todoNow(todo.ID); !ok {
		t.Fatal("parent todo was deleted while follow-up remained unresolved")
	}
	if _, err := reviewErr(s, follow.ID, map[string]any{"verdict": reviewCancelled, "note": "Drop follow-up"}); err == nil {
		t.Fatal("cancelling unresolved follow-up unexpectedly succeeded")
	}

	if _, err := reviewErr(s, follow.ID, map[string]any{"verdict": reviewAccepted, "checks": completeReviewChecks(todoNow(s, follow.ID))}); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteTodo(ctx, todo.ID); err != nil {
		t.Fatalf("deleting parent after follow-up resolved failed: %v", err)
	}
}

type investigationConnectedDevices struct {
	*fakeDevices
	conn map[string]time.Time
}

func (d *investigationConnectedDevices) ConnectedAt(id string) time.Time { return d.conn[id] }
func (d *investigationConnectedDevices) List() []devices.Summary {
	out := make([]devices.Summary, 0, len(d.conn))
	for id := range d.conn {
		out = append(out, devices.Summary{ID: id, Name: id, Online: true, Reachable: true})
	}
	return out
}
func (d *investigationConnectedDevices) Summary(id string) (devices.Summary, bool) {
	if _, ok := d.conn[id]; !ok {
		return devices.Summary{}, false
	}
	return devices.Summary{ID: id, Name: id, Online: true, Reachable: true}, true
}

func TestInvestigationFindingsWorkerGoneFailClosedOnUnknownAndReconnect(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown_machine_stays_fail_closed", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, nil)
		worker := todo.Agents[0]
		f.mu.Lock()
		delete(f.sessions, worker.MachineID)
		f.mu.Unlock()

		if !s.todoAgentLiveListed(ctx, worker) {
			t.Fatal("todoAgentLiveListed treated ErrNoDevice as dead")
		}
		if _, gone := s.investigationWorkerGone(ctx, todo); gone {
			t.Fatal("investigationWorkerGone treated ErrNoDevice as dead")
		}
	})

	t.Run("unlisted_session_without_orphan_stays_fail_closed", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, nil)
		worker := todo.Agents[0]
		f.mu.Lock()
		f.sessions[worker.MachineID] = []proto.Session{}
		f.mu.Unlock()

		if !s.todoAgentLiveListed(ctx, worker) {
			t.Fatal("todoAgentLiveListed treated unlisted session without orphan/confirmation as dead")
		}
		if _, gone := s.investigationWorkerGone(ctx, todo); gone {
			t.Fatal("investigationWorkerGone marked worker gone without orphan or confirmation")
		}
	})

	t.Run("recent_reconnect_grace_blocks_stale_orphan_recovery", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, nil)
		worker := todo.Agents[0]
		now := time.Now().UTC()
		f.mu.Lock()
		f.sessions[worker.MachineID] = []proto.Session{}
		f.mu.Unlock()
		s.devs = &investigationConnectedDevices{fakeDevices: &fakeDevices{}, conn: map[string]time.Time{worker.MachineID: now.Add(-30 * time.Second)}}
		s.orphans.mu.Lock()
		s.orphans.now = map[string]TodoOrphan{
			todo.ID: {Reason: orphanClosed, Since: now.Add(-10 * time.Minute), key: "orphaned:" + worker.MachineID + "/" + worker.SessionID + "@2026-11-28T00:00:00Z"},
		}
		s.orphans.mu.Unlock()

		if !s.todoAgentLiveListed(ctx, worker) {
			t.Fatal("todoAgentLiveListed ignored reconnect grace")
		}
		if _, gone := s.investigationWorkerGone(ctx, todo); gone {
			t.Fatal("investigationWorkerGone ignored reconnect grace after node restart")
		}
	})

	t.Run("cached_orphan_predating_reconnect_blocks_recovery", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, nil)
		worker := todo.Agents[0]
		now := time.Now().UTC()
		f.mu.Lock()
		f.sessions[worker.MachineID] = []proto.Session{}
		f.mu.Unlock()
		s.devs = &investigationConnectedDevices{fakeDevices: &fakeDevices{}, conn: map[string]time.Time{worker.MachineID: now.Add(-5 * time.Minute)}}
		s.orphans.mu.Lock()
		s.orphans.now = map[string]TodoOrphan{
			todo.ID: {Reason: orphanClosed, Since: now.Add(-10 * time.Minute), key: "orphaned:" + worker.MachineID + "/" + worker.SessionID + "@2026-11-28T00:00:00Z"},
		}
		s.orphans.mu.Unlock()

		if _, gone := s.investigationWorkerGone(ctx, todo); gone {
			t.Fatal("investigationWorkerGone trusted cached orphan predating node reconnect")
		}
	})

	t.Run("stopped_alias_row_does_not_hide_live_row", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, nil)
		worker := todo.Agents[0]
		now := time.Now().UTC()
		archivedAlias := proto.Session{SessionID: "old-alias", Archived: true}
		archivedAlias.SetAliases([]string{worker.SessionID})
		f.mu.Lock()
		f.sessions[worker.MachineID] = []proto.Session{
			archivedAlias,
			{SessionID: worker.SessionID, Status: proto.StatusWorking},
		}
		f.mu.Unlock()
		s.orphans.mu.Lock()
		s.orphans.now = map[string]TodoOrphan{
			todo.ID: {Reason: orphanClosed, Since: now.Add(-10 * time.Minute), key: "orphaned:" + worker.MachineID + "/" + worker.SessionID + "@2026-11-28T00:00:00Z"},
		}
		s.orphans.mu.Unlock()

		if !s.todoAgentLiveListed(ctx, worker) {
			t.Fatal("stopped alias row hid live session row")
		}
		if _, gone := s.investigationWorkerGone(ctx, todo); gone {
			t.Fatal("investigationWorkerGone marked live session with stopped alias as gone")
		}
	})

	t.Run("fresh_post_connect_orphan_allows_recovery", func(t *testing.T) {
		s, f, todo := findingTaskWithHub(t, nil)
		worker := todo.Agents[0]
		now := time.Now().UTC()
		f.mu.Lock()
		f.sessions[worker.MachineID] = []proto.Session{}
		f.mu.Unlock()
		s.devs = &investigationConnectedDevices{fakeDevices: &fakeDevices{}, conn: map[string]time.Time{worker.MachineID: now.Add(-10 * time.Minute)}}
		s.orphanWatch(ctx, now.Add(orphanReconnectWait+time.Minute))
		if o, ok := s.orphanOf(todo.ID); !ok || o.Reason != orphanClosed {
			t.Fatalf("expected fresh orphanClosed, got %+v ok=%v", o, ok)
		}
		if _, gone := s.investigationWorkerGone(ctx, todo); !gone {
			t.Fatal("fresh post-connect orphanClosed should allow stale recovery")
		}
	})
}

func TestInvestigationFindingsOwnerDropRequiresOwnerAppIdentity(t *testing.T) {
	s, todo := findingTask(t, todoTestDB(t))
	r := reportFindings(t, s, todo, []investigationFinding{investigateLogs})
	body := `{"question":"Drop investigation finding logs?","choice":"Drop","reason":"No longer needed."}`
	manualBody := `{"question":"Drop investigation finding logs?","choice":"Drop","reason":"No longer needed.","source":"manual"}`

	postDecision := func(payload string, headers map[string]string) (*httptest.ResponseRecorder, db.TodoDecisionRow) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/todos/"+todo.ID+"/decisions", strings.NewReader(payload))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var out struct {
			Decision db.TodoDecisionRow `json:"decision"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out.Decision
	}

	for _, tc := range []struct {
		name       string
		payload    string
		headers    map[string]string
		wantReject bool
	}{
		{
			name:       "full_role_with_extension_header",
			payload:    body,
			headers:    map[string]string{RoleHeader: roleFull, ExtensionHeader: "ext-attacker"},
			wantReject: true,
		},
		{
			name:       "full_role_person_with_extension_header",
			payload:    body,
			headers:    map[string]string{RoleHeader: roleFull, assistantwire.PersonHeader: "1", ExtensionHeader: "ext-attacker"},
			wantReject: true,
		},
		{
			name:       "empty_role_with_extension_header",
			payload:    body,
			headers:    map[string]string{ExtensionHeader: "ext-attacker"},
			wantReject: true,
		},
		{
			name:       "extension_role_manual_drop",
			payload:    manualBody,
			headers:    map[string]string{RoleHeader: roleExtension, ExtensionHeader: "ext-attacker"},
			wantReject: true,
		},
		{
			name:    "extension_role_default_outcome",
			payload: body,
			headers: map[string]string{RoleHeader: roleExtension, ExtensionHeader: "ext-attacker"},
		},
		{
			name:    "full_role_without_person_header",
			payload: body,
			headers: map[string]string{RoleHeader: roleFull},
		},
		{
			name:    "no_headers_direct_call",
			payload: body,
			headers: map[string]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, dec := postDecision(tc.payload, tc.headers)
			if tc.wantReject {
				if rec.Code == http.StatusOK {
					t.Fatalf("expected non-200 rejection, got 200 decision=%+v", dec)
				}
				return
			}
			if rec.Code == http.StatusOK && dec.ID != "" {
				if strings.HasPrefix(dec.SourceMessage, findingOwnerDecisionPrefix) {
					t.Fatalf("non-owner-app request minted owner-drop provenance: %+v", dec)
				}
				if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "logs", Drop: dec.ID}); err == nil {
					t.Fatalf("non-owner-app decision %s dropped the finding", dec.ID)
				}
			}
		})
	}

	// Legitimate owner app request (full role + PersonHeader + no extension header) succeeds.
	rec, dec := postDecision(body, map[string]string{RoleHeader: roleFull, assistantwire.PersonHeader: "1"})
	if rec.Code != http.StatusOK || dec.ID == "" {
		t.Fatalf("owner app manual drop failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := confirmTestFindings(s, todo.ID, r.Version, findingDisposition{ID: "logs", Drop: dec.ID}); err != nil {
		t.Fatalf("owner app drop decision rejected: %v", err)
	}
}
