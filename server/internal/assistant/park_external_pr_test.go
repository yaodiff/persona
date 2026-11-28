package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const externalPRSource = "https://github.com/example/app/pull/12"

func externalPRParkArgs(source string) map[string]any {
	evidence := testParkEvidence("external_event", "the upstream change", source, "The PR becomes terminal")
	evidence["pr_event"] = "terminal"
	return map[string]any{"verdict": "parked", "reason": "Waiting for the recorded external object",
		"waiting_on": parkExternalEvent, "until": "2d",
		"evidence": evidence}
}

type externalPRForge struct {
	fakeForge
	terminal string
	calls    []string
	before   func()
}

func (f *externalPRForge) PRStatus(_ context.Context, repo string, number int) (TodoPRStatus, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s#%d", repo, number))
	if f.before != nil {
		before := f.before
		f.before = nil
		before()
	}
	if repo == "example/app" && number == 12 {
		return TodoPRStatus{State: f.terminal}, nil
	}
	return TodoPRStatus{State: "open", HeadOID: "other-head"}, nil
}

func externalPRReceipt(t *testing.T, task Todo) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(task.Supervision.Reviews)
	var reviews []map[string]any
	if err := json.Unmarshal(raw, &reviews); err != nil || len(reviews) == 0 {
		t.Fatalf("reviews: %s %v", raw, err)
	}
	receipt, _ := reviews[len(reviews)-1]["external_pr"].(map[string]any)
	return receipt
}

// Existing entrypoints/raw JSON keep these compiling when the added production implementation is removed.
func TestExternalPRParkWakesFromItsBoundObject(t *testing.T) {
	for _, association := range []string{"current", "earlier", "no association"} {
		for _, state := range []string{"merged", "closed"} {
			t.Run(association+"/"+state, func(t *testing.T) {
				s, _, task := newSupervisedService(t)
				holdDigest(s)
				ctx := context.Background()
				if association != "no association" {
					s.setTaskStage(ctx, task.ID, "review", externalPRSource)
				}
				if association == "earlier" {
					s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/99")
				}
				args := externalPRParkArgs("https://GitHub.com/Example/App/pull/12/?tab=checks#discussion")
				if _, err := reviewErr(s, task.ID, args); err != nil {
					t.Fatal(err)
				}
				before := todoNow(s, task.ID)
				forge := &externalPRForge{terminal: state}
				s.deploy.forge = forge
				now := time.Now()
				s.sweepPRStatuses(ctx, now)
				got := todoNow(s, task.ID)
				if association == "no association" && !slices.Equal(forge.calls, []string{"example/app#12"}) {
					t.Fatalf("evidence-only PR was not polled exactly once: reads=%v", forge.calls)
				}
				if association != "no association" {
					if facts := prFacts(got, externalPRSource); facts == nil || facts.State != state {
						t.Fatalf("stage PR facts did not record %s: %+v", state, facts)
					}
				}
				if got.Supervision.State != reviewPending || !pendingReviewSignal(s, task.ID) {
					t.Fatalf("bound PR was read as %s but the park did not wake: state=%q queued=%v reads=%v",
						state, got.Supervision.State, pendingKinds(s, task.ID), forge.calls)
				}
				receipt := externalPRReceipt(t, got)
				if receipt["url"] != externalPRSource || receipt["state"] != state || receipt["checked_at"] == nil {
					t.Fatalf("source receipt missing: %v", receipt)
				}
				if got.DoneAt != nil || got.PRURL != before.PRURL || !reflect.DeepEqual(got.PRs, before.PRs) ||
					len(got.Supervision.Reviews) != len(before.Supervision.Reviews) ||
					!got.Supervision.Reviews[0].At.Equal(before.Supervision.Reviews[0].At) {
					t.Fatal("observation completed the task, rewrote its park history, or fabricated a task PR association")
				}
				if association == "earlier" && !slices.Equal(forge.calls, []string{"example/app#99", "example/app#12"}) {
					t.Fatal("the independent open PR was displaced by the bound external source")
				}
				s.sweepPRStatuses(ctx, now.Add(deployRecheck))
				if !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
					t.Fatal("terminal replay introduced another logical wake")
				}
			})
		}
	}
}

func TestExternalPRParkIgnoresNonObjectSources(t *testing.T) {
	for _, source := range []string{
		"https://example.org/releases/next", "https://github.com/example/app/issues/12",
		"https://github.com/example/app/pull/12/files", "https://github.com.example.org/example/app/pull/12",
		"https://git.example.org/example/app/-/merge_requests/12", "https://github.com/example/app/pull/12345678901234567890123",
	} {
		t.Run(source, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			holdDigest(s)
			args := externalPRParkArgs(source)
			if _, err := reviewErr(s, task.ID, args); err == nil || !strings.Contains(err.Error(), "evidence.pr_event") {
				t.Fatalf("invalid PR binding: %v", err)
			}
			delete(args["evidence"].(map[string]any), "pr_event")
			if _, err := reviewErr(s, task.ID, args); err != nil {
				t.Fatal(err)
			}
			forge := &externalPRForge{terminal: "merged"}
			s.deploy.forge = forge
			s.sweepPRStatuses(context.Background(), time.Now())
			if todoNow(s, task.ID).Supervision.State != reviewParked || len(forge.calls) != 0 || pendingReviewSignal(s, task.ID) {
				t.Fatal("a different external object was treated as a GitHub PR")
			}
		})
	}
}

func TestExternalPRParkContextLinkKeepsTimedRecheck(t *testing.T) {
	s, _, task := newSupervisedService(t)
	holdDigest(s)
	args := externalPRParkArgs(externalPRSource)
	evidence := args["evidence"].(map[string]any)
	evidence["wake"] = "Deployment verification returns"
	delete(evidence, "pr_event")
	if _, err := reviewErr(s, task.ID, args); err != nil {
		t.Fatal(err)
	}
	forge := &externalPRForge{terminal: "merged"}
	s.deploy.forge = forge
	s.sweepPRStatuses(context.Background(), time.Now())
	got := todoNow(s, task.ID)
	if len(forge.calls) != 0 || got.Supervision.State != reviewParked || externalPRReceipt(t, got) != nil {
		t.Fatalf("PR used as context started a watch: state=%s reads=%v", got.Supervision.State, forge.calls)
	}
	if _, due := parkedDueSignal(got, got.Supervision.parking().Until); !due {
		t.Fatal("context-only park lost its timed recheck")
	}
}

func TestExternalPRParkWakesOnANewSubmittedReview(t *testing.T) {
	for _, tc := range []struct {
		name, event, state, head string
		at                       time.Duration
		wake                     bool
	}{
		{"formal comment", "terminal_or_review", "COMMENTED", "current", time.Minute, true},
		{"approval", "terminal_or_review", "APPROVED", "current", time.Minute, true},
		{"request changes", "terminal_or_review", "CHANGES_REQUESTED", "current", time.Minute, true},
		{"terminal only", "terminal", "APPROVED", "current", time.Minute, false},
		{"prior review", "terminal_or_review", "APPROVED", "current", -time.Minute, false},
		{"same second", "terminal_or_review", "APPROVED", "current", 0, false},
		{"future provider time", "terminal_or_review", "APPROVED", "current", 3 * time.Minute, false},
		{"another head", "terminal_or_review", "APPROVED", "old", time.Minute, false},
		{"pending review", "terminal_or_review", "PENDING", "current", time.Minute, false},
		{"dismissed review", "terminal_or_review", "DISMISSED", "current", time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			holdDigest(s)
			args := externalPRParkArgs(externalPRSource)
			args["evidence"].(map[string]any)["pr_event"] = tc.event
			if _, err := reviewErr(s, task.ID, args); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			parkAt := todoNow(s, task.ID).Supervision.parking().At
			first := parkAt.Add(tc.at).UTC().Format(time.RFC3339)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/example/app/pulls/12":
					fmt.Fprint(w, `{"state":"open","head":{"sha":"current"}}`)
				case "/repos/example/app/pulls/12/reviews":
					fmt.Fprintf(w, `[{"id":42,"user":{"login":"reviewer"},"state":%q,"commit_id":%q,"submitted_at":%q}]`, tc.state, tc.head, first)
				case "/repos/example/app/commits/current/status", "/repos/example/app/commits/current/check-runs":
					fmt.Fprint(w, `{"total_count":0}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer remote.Close()
			s.deploy.forge = githubForge{base: remote.URL, client: remote.Client()}
			s.sweepPRStatuses(ctx, parkAt.Add(2*time.Minute))
			got := todoNow(s, task.ID)
			if (got.Supervision.State == reviewPending) != tc.wake || pendingReviewSignal(s, task.ID) != tc.wake {
				t.Fatalf("review wake = %s, pending signals = %v, want wake = %v", got.Supervision.State, pendingKinds(s, task.ID), tc.wake)
			}
			receipt := externalPRReceipt(t, got)
			if receipt["url"] != externalPRSource || receipt["state"] != "open" || got.PRURL != "" {
				t.Fatalf("review observation lost its source or invented a stage PR: %v, %q", receipt, got.PRURL)
			}
			if tc.wake {
				review, _ := receipt["review"].(map[string]any)
				if review["id"] != float64(42) || review["submitted_at"] != first {
					t.Fatalf("provider review identity missing: %v", receipt)
				}
				signals := watchSignals(got, nil, parkAt.Add(2*time.Minute))
				if len(signals) != 1 {
					t.Fatalf("expected one review signal: %v", signals)
				}
				entry, ok := s.digestEntry(ctx, signals[0], "")
				if !ok || !strings.Contains(entry.Detail, externalPRSource+"#pullrequestreview-42") {
					t.Fatalf("review signal lost its source: %+v", entry)
				}
			}
		})
	}
}

func TestExternalPRParkRechecksCapturedWait(t *testing.T) {
	for _, change := range []string{"repark", "replace", "change event", "complete", "cancel"} {
		t.Run(change, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			holdDigest(s)
			if _, err := reviewErr(s, task.ID, externalPRParkArgs(externalPRSource)); err != nil {
				t.Fatal(err)
			}
			forge := &externalPRForge{terminal: "merged", before: func() {
				switch change {
				case "complete":
					w := httptest.NewRecorder()
					s.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/v1/todos/"+task.ID, strings.NewReader(`{"done":true}`)))
					if w.Code != http.StatusOK {
						t.Errorf("complete: %d %s", w.Code, w.Body.String())
					}
				case "cancel":
					if _, err := reviewErr(s, task.ID, map[string]any{"verdict": "cancelled", "note": "No longer needed"}); err != nil {
						t.Error(err)
					}
				default:
					source := externalPRSource
					if change == "replace" {
						source = "https://github.com/example/app/pull/98"
					}
					next := externalPRParkArgs(source)
					if change == "change event" {
						next["evidence"].(map[string]any)["pr_event"] = "terminal_or_review"
					}
					if _, err := reviewErr(s, task.ID, next); err != nil {
						t.Error(err)
					}
				}
			}}
			s.deploy.forge = forge
			s.sweepPRStatuses(context.Background(), time.Now())
			if len(forge.calls) != 1 {
				t.Fatal("the source read did not run, so the in-flight fence was not exercised")
			}
			if todoNow(s, task.ID).Supervision.State == reviewPending || pendingReviewSignal(s, task.ID) {
				t.Fatal("an in-flight response woke a replaced or closed wait")
			}
		})
	}
}

func TestExternalPRParkRejectsOlderReceiptAndKeepsPollInterval(t *testing.T) {
	s, _, task := newSupervisedService(t)
	holdDigest(s)
	if _, err := reviewErr(s, task.ID, externalPRParkArgs(externalPRSource)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(context.Background(), task.ID, func(v *Supervision) bool {
		v.Reviews[len(v.Reviews)-1].At = time.Now().Add(-time.Minute)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	forge := &externalPRForge{terminal: "open"}
	s.deploy.forge = forge
	now := time.Now()
	s.sweepPRStatuses(context.Background(), now)
	cur := todoNow(s, task.ID)
	if receipt := externalPRReceipt(t, cur); receipt["state"] != "open" || pendingReviewSignal(s, task.ID) {
		t.Fatalf("open source not durably observed, or woke early: %v", receipt)
	}
	if err := s.applyPRStatus(context.Background(), cur,
		TodoPRStatus{URL: externalPRSource, State: "merged", CheckedAt: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyPRStatus(context.Background(), cur,
		TodoPRStatus{URL: externalPRSource, State: "merged", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	s.sweepPRStatuses(context.Background(), now.Add(time.Minute))
	if len(forge.calls) != 1 || todoNow(s, task.ID).Supervision.State != reviewParked {
		t.Fatal("an old receipt or unthrottled source woke the task")
	}
	forge.terminal = "merged"
	s.sweepPRStatuses(context.Background(), now.Add(deployRecheck))
	if len(forge.calls) != 2 || !pendingReviewSignal(s, task.ID) {
		t.Fatal("the terminal source was not reconsidered after the normal interval")
	}
}

func TestExternalPRParkReceiptAndReviewRecoverTogether(t *testing.T) {
	ctx := context.Background()
	store := &ledgerDown{DB: taskRecoveryDatabase(t)}
	s, _, task := newSupervisedServiceOn(t, store)
	runCardlessManager(t, s)
	quietDigest(s)
	holdDigest(s)
	if _, err := reviewErr(s, task.ID, externalPRParkArgs(externalPRSource)); err != nil {
		t.Fatal(err)
	}
	before := todoNow(s, task.ID)
	old, _ := json.Marshal(before.Supervision)
	forge := &externalPRForge{terminal: "merged"}
	s.deploy.forge = forge
	store.down.Store(true)
	now := time.Now()
	s.sweepPRStatuses(ctx, now)
	after, _ := json.Marshal(todoNow(s, task.ID).Supervision)
	if string(old) != string(after) || pendingReviewSignal(s, task.ID) || len(forge.calls) != 1 {
		t.Fatal("failed ledger write published a receipt/review, or source was not read")
	}
	// End the old manager while its ledger is still unavailable. Its timer
	// must not claim the recovery wake from the next process.
	stopCardlessManager(s)
	store.down.Store(false)
	again, previousHub := restartedOn(t, store)
	runCardlessManager(t, again)
	quietDigest(again)
	holdDigest(again)
	again.deploy.forge = &externalPRForge{terminal: "merged"}
	again.sweepPRStatuses(ctx, now.Add(time.Minute))
	got := todoNow(again, task.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewPending || !pendingReviewSignal(again, task.ID) ||
		externalPRReceipt(t, got)["state"] != "merged" {
		t.Fatal("reconstruction did not retry and commit the same external source")
	}
	stopCardlessManager(again)
	third, hub := restartedOn(t, store)
	quietDigest(third)
	holdDigest(third)
	if err := third.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := todoNow(third, task.ID)
	if loaded.Supervision.State != reviewPending || externalPRReceipt(t, loaded)["state"] != "merged" {
		t.Fatal("receipt and review did not survive another reconstruction")
	}
	signals := watchSignals(loaded, nil, now.Add(2*time.Minute))
	if len(signals) != 1 || signals[0].Kind != sigReview || signals[0].Key != reviewKey(got.Supervision) {
		t.Fatalf("lost in-memory wake was not recoverable from the persisted review: %v", signals)
	}
	hub.mu.Lock()
	hub.known["conv2"] = true
	hub.mu.Unlock()
	hub.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	if _, _, err := third.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.Supervisor = "conv2"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	// conv2 is only a legacy side-supervisor, not an explicit manual leader: the recovered wake follows universal-main
	// routing (#5732), without changing the external source's identity or duplicating the receipt.
	third.signal(signals[0])
	setSuperviseTimings(third, shortSuperviseTimings)
	sendPendingPRConflicts(third)
	receipt := "The bound PR is merged: " + externalPRSource
	previousHub.mu.Lock()
	deliveredBeforeRestart := strings.Contains(sentText(previousHub.sends), receipt)
	previousHub.mu.Unlock()
	if deliveredBeforeRestart {
		t.Fatal("the previous manager delivered the receipt reserved for recovery")
	}
	// Relay delivery is asynchronous: observe the actual main-conversation message before restarting again.
	var message string
	waitFor(t, "the recovered PR receipt to reach main", func() bool {
		hub.mu.Lock()
		message = sentText(hub.sends)
		hub.mu.Unlock()
		return hub.sentTo("conv1") == 1 && strings.Contains(message, receipt)
	})
	if hub.sentTo("conv1") != 1 || hub.sentTo("conv2") != 0 ||
		!strings.Contains(message, receipt) {
		t.Fatalf("recovered wake did not reach main with the receipt: %q", message)
	}

	fourth, _ := restartedOn(t, store)
	if err := fourth.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	loaded = todoNow(fourth, task.ID)
	entry, ok := fourth.digestEntry(ctx, signals[0], "")
	if !ok {
		t.Fatal("the source wake lost its identity")
	}
	if entries, _, _, err := fourth.reserveSupervision(ctx,
		[]prompts.SuperviseEntry{entry}, signals, now.Add(time.Hour)); len(entries) != 0 || err != nil {
		t.Fatalf("a delivered park wake was offered again after restart: %v, %v", entries, err)
	}
	if key := reviewKey(loaded.Supervision); key != signals[0].Key {
		t.Fatalf("supervisor change replaced the source wake identity: %q", key)
	}
}

func TestExternalPRParkWakesOnMergeConflict(t *testing.T) {
	for _, association := range []string{"current", "earlier", "no association"} {
		for _, event := range []string{"terminal", "terminal_or_review"} {
			t.Run(association+"/"+event, func(t *testing.T) {
				s, _, task := newSupervisedService(t)
				holdDigest(s)
				ctx := context.Background()
				if association != "no association" {
					s.setTaskStage(ctx, task.ID, "review", externalPRSource)
				}
				if association == "earlier" {
					s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/99")
				}
				args := externalPRParkArgs(externalPRSource)
				args["evidence"].(map[string]any)["pr_event"] = event
				if _, err := reviewErr(s, task.ID, args); err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC().Truncate(time.Second)

				// Open without conflicts stays parked.
				s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{
					12: {State: "open", HeadOID: "head-clean", Conflicts: false},
					99: {State: "open", HeadOID: "other-head"},
				}}
				s.sweepPRStatuses(ctx, now)
				if got := todoNow(s, task.ID); got.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) {
					t.Fatalf("clean open PR woke external park: state=%q pending=%v", got.Supervision.State, pendingKinds(s, task.ID))
				}

				// Conflicting without head OID stays parked.
				s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{
					12: {State: "open", Conflicts: true},
					99: {State: "open", HeadOID: "other-head"},
				}}
				s.sweepPRStatuses(ctx, now.Add(deployRecheck))
				if got := todoNow(s, task.ID); got.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) {
					t.Fatalf("conflict without head woke external park: state=%q pending=%v", got.Supervision.State, pendingKinds(s, task.ID))
				}

				// Open with conflicts + head wakes the external park once, without duplicating sigPRConflict.
				s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{
					12: {State: "open", HeadOID: "head-conflict-1", Conflicts: true},
					99: {State: "open", HeadOID: "other-head"},
				}}
				s.sweepPRStatuses(ctx, now.Add(2*deployRecheck))
				got := todoNow(s, task.ID)
				if got.Supervision.State != reviewPending || !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
					t.Fatalf("conflicting bound PR did not wake once via sigReview: state=%q pending=%v", got.Supervision.State, pendingKinds(s, task.ID))
				}
				receipt := externalPRReceipt(t, got)
				if receipt["conflicts"] != true || receipt["head_oid"] != "head-conflict-1" {
					t.Fatalf("conflict receipt missing fields: %v", receipt)
				}
				signals := watchSignals(got, nil, now.Add(2*deployRecheck))
				if len(signals) != 1 {
					t.Fatalf("expected one review signal: %v", signals)
				}
				entry, ok := s.digestEntry(ctx, signals[0], "")
				if !ok || !strings.Contains(entry.Detail, "merge conflicts") || !strings.Contains(entry.Detail, "head-conflict-1") || !strings.Contains(entry.Detail, externalPRSource) || strings.Contains(entry.Detail, "pullrequestreview-0") {
					t.Fatalf("conflict wake detail = %q (ok=%v)", entry.Detail, ok)
				}

				// Repeated sweep on the same conflicting head must not emit a duplicate sigPRConflict.
				s.sweepPRStatuses(ctx, now.Add(3*deployRecheck))
				if !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
					t.Fatalf("repeated sweep on same conflicting head added duplicate signal: %v", pendingKinds(s, task.ID))
				}
			})
		}
	}
}

func TestExternalPRParkConflictRespectsAgentRoutingAndFallback(t *testing.T) {
	for _, association := range []string{"current", "no association"} {
		for _, agentOnline := range []bool{true, false} {
			name := fmt.Sprintf("%s/agent_online_%t", association, agentOnline)
			t.Run(name, func(t *testing.T) {
				agent := proto.Session{
					SessionID: "agent-ext-park", MachineID: "box", Cwd: "/code/app", Agent: "claude", Title: "worker",
					Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now(),
				}
				s, f, task := newBoundPRTask(t, &memStore{}, agent)
				turnPREventsRouteOn(t, s)
				ctx := context.Background()
				if association == "no association" {
					s.reports.mu.Lock()
					delete(s.reports.stages, task.ID)
					s.reports.mu.Unlock()
					s.todos.mu.Lock()
					row := s.todos.todos[task.ID]
					row.PRURL, row.PRs, row.PRStatus, row.OtherPRs = "", nil, nil, nil
					s.todos.mu.Unlock()
				}
				if !agentOnline {
					f.devices = []Device{
						{ID: "server", Name: "server", Online: true, Reachable: true},
						{ID: "box", Name: "box", Online: false, Reachable: false},
					}
				}
				if _, err := reviewErr(s, task.ID, externalPRParkArgs(externalPRSource)); err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC().Truncate(time.Second)
				s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{
					12: {State: "open", HeadOID: "head-ext-1", Conflicts: true},
				}}
				before := len(sentToAgent(f))
				s.sweepPRStatuses(ctx, now)
				got := todoNow(s, task.ID)
				if agentOnline {
					if sent := sentToAgent(f)[before:]; len(sent) != 1 || !strings.Contains(sent[0], "Event: pr_conflict") || !strings.Contains(sent[0], "head-ext-1") {
						t.Fatalf("expected 1 routed conflict to Agent, got %q", sent)
					}
					if got.Supervision.State != reviewParked || len(pendingKinds(s, task.ID)) != 0 {
						t.Fatalf("routed conflict unexpectedly woke supervisor: state=%q pending=%v", got.Supervision.State, pendingKinds(s, task.ID))
					}
					// Repeated sweep on the same head must dedup without sending or waking supervisor.
					s.sweepPRStatuses(ctx, now.Add(deployRecheck))
					if len(sentToAgent(f))-before != 1 || todoNow(s, task.ID).Supervision.State != reviewParked || len(pendingKinds(s, task.ID)) != 0 {
						t.Fatalf("repeated sweep leaked send or supervisor wake: sends=%d state=%q pending=%v", len(sentToAgent(f))-before, todoNow(s, task.ID).Supervision.State, pendingKinds(s, task.ID))
					}
					return
				}
				// Agent offline: fallback must wake external park once via sigReview, without duplicate sigPRConflict.
				if len(sentToAgent(f))-before != 0 {
					t.Fatalf("offline Agent unexpectedly received sends")
				}
				if got.Supervision.State != reviewPending || !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
					t.Fatalf("fallback did not wake external park cleanly: state=%q pending=%v", got.Supervision.State, pendingKinds(s, task.ID))
				}
				// Repeated sweep on same head does not add a duplicate sigPRConflict.
				s.sweepPRStatuses(ctx, now.Add(deployRecheck))
				if !slices.Equal(pendingKinds(s, task.ID), []string{sigReview}) {
					t.Fatalf("fallback repeated sweep added duplicate wake: %v", pendingKinds(s, task.ID))
				}
			})
		}
	}
}
