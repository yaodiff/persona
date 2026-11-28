package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestCommentedReviewFailureReportsAreActionable(t *testing.T) {
	for _, body := range []string{
		"CI failed",
		"All CI checks are failing.",
		"Tests: ❌ failed",
		"Build is red!",
		"Lint: failure",
		"Coverage failed",
		"Coverage report: failure",
		"Verification completed.\nCI failed",
		"Automated independent semantic review found no blocking defects.\nLocal checks completed:\n- `CI failed`",
	} {
		if commentedReviewLooksInformational(body) {
			t.Errorf("failure report was treated as informational: %q", body)
		}
	}
	for _, body := range []string{"CI passed", "All tests are green", "Coverage: 98.5%", "LGTM"} {
		if !commentedReviewLooksInformational(body) {
			t.Errorf("routine status was treated as actionable: %q", body)
		}
	}
}

func TestPRFeedbackFailureReportsAndExplicitOptOut(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, hub, task := newBoundPRTask(t, &memStore{}, feedbackTestAgent(now, false))
	turnPREventsRouteOn(t, s)
	feedbackTestSendReceipt(hub, agentSendReply{OK: true})
	source, forge := newFeedbackTestForge(t)
	s.deploy.forge = forge
	s.sweepPRStatuses(t.Context(), now)

	const optedOut = "Please rerun this check. <!-- \n\t PeRsOnA : NO-WAKE \t-->"
	observation := now.Add(deployRecheck)
	source.mu.Lock()
	source.comments = []map[string]any{
		feedbackTestComment(41, observation, "CI failed"),
		feedbackTestComment(42, observation, optedOut),
	}
	source.reviews = []githubPRReview{
		feedbackTestReview(41, observation, "Coverage failed"),
		feedbackTestReview(42, observation, optedOut),
		feedbackTestReview(43, observation, ""),
	}
	source.inline = map[int64][]githubPRInlineComment{
		43: {{ID: 101, Body: optedOut}},
	}
	source.mu.Unlock()
	before := len(sentToAgent(hub))
	s.sweepPRStatuses(t.Context(), observation)
	sent := sentToAgent(hub)[before:]
	if len(sent) != 1 {
		t.Fatalf("expected one immediate batch, got %q", sent)
	}
	got := sent[0]
	for _, want := range []string{"CI failed", "Coverage failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("first observation did not deliver %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Please rerun") {
		t.Fatalf("opt-out woke the Agent: %s", got)
	}
	// A repeated observation is still silent even without the marker in an edited body.
	source.mu.Lock()
	source.comments[1]["body"] = "Please rerun this check."
	source.mu.Unlock()
	n := len(sentToAgent(hub))
	s.sweepPRStatuses(t.Context(), observation.Add(7*time.Hour))
	if got := sentToAgent(hub)[n:]; len(got) != 0 {
		t.Fatalf("opt-out was replayed: %q", got)
	}
	if got := feedbackTestCursors(t, s.store(), task)[prFeedbackReviews].IDsAt; len(got) != 3 {
		t.Fatalf("opt-outs did not advance the cursor: %v", got)
	}
}

func feedbackTestQueue(t *testing.T, s *Service, task Todo) prFeedbackQueue {
	t.Helper()
	q, err := readPRFeedbackQueue(t.Context(), s.store(), prFeedbackKey(task.ID, task.PRURL))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestPRFeedbackBatchesBurstAndDeduplicatesAcrossHeads(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, hub, task := newBoundPRTask(t, &memStore{}, feedbackTestAgent(now, false))
	turnPREventsRouteOn(t, s)
	feedbackTestSendReceipt(hub, agentSendReply{OK: true})
	source, forge := newFeedbackTestForge(t)
	s.deploy.forge = forge
	s.sweepPRStatuses(t.Context(), now)
	before := len(sentToAgent(hub))

	first := now.Add(deployRecheck)
	source.mu.Lock()
	source.comments = []map[string]any{feedbackTestComment(10, first, "Keep the receipt.")}
	source.mu.Unlock()
	s.sweepPRStatuses(t.Context(), first)
	if got := sentToAgent(hub)[before:]; len(got) != 0 {
		t.Fatalf("fresh feedback did not wait for its batch window: %q", got)
	}
	if q := feedbackTestQueue(t, s, task); q.BatchAt == nil || !q.BatchAt.Equal(first.Add(time.Minute)) || len(q.Actionable) != 1 {
		t.Fatalf("batch deadline or receipt was not saved: %+v", q)
	}

	more := first.Add(30 * time.Second)
	source.mu.Lock()
	source.comments = append(source.comments, source.comments[0], feedbackTestComment(11, more, "Check the retry limit."))
	review := feedbackTestReview(10, more, "Preserve the existing behavior.")
	source.reviews = []githubPRReview{review, review}
	source.mu.Unlock()
	p := todoNow(s, task.ID).PRStatus
	if err := s.refreshPRFeedback(t.Context(), task.ID, p.URL, p.HeadOID, forge, more); err != nil {
		t.Fatal(err)
	}
	if got := sentToAgent(hub)[before:]; len(got) != 0 {
		t.Fatalf("arrivals within the first window started a second wake: %q", got)
	}
	if err := s.refreshPRFeedback(t.Context(), task.ID, p.URL, p.HeadOID, forge, first.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := sentToAgent(hub)[before:]
	if len(got) != 1 {
		t.Fatalf("expected one batch for both streams, got %q", got)
	}
	for _, want := range []string{"#issuecomment-10", "#issuecomment-11", "#pullrequestreview-10"} {
		if strings.Count(got[0], want) != 1 {
			t.Errorf("batch did not include %s exactly once: %s", want, got[0])
		}
	}

	source.mu.Lock()
	source.head = "head-two"
	source.comments = []map[string]any{feedbackTestComment(10, first.Add(2*time.Minute), "Edited comment.")}
	review.CommitID, review.SubmittedAt = source.head, first.Add(2*time.Minute)
	source.reviews = []githubPRReview{review}
	source.mu.Unlock()
	s.sweepPRStatuses(t.Context(), first.Add(deployRecheck))
	if got := sentToAgent(hub)[before:]; len(got) != 1 {
		t.Fatalf("a previously handled ID woke the Agent on a new head: %q", got)
	}
}

func TestPRFeedbackStaleFallbackSurvivesRestartAndReplaysOnce(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	st := &memStore{}
	agent := feedbackTestAgent(now, false)
	s, hub, task := newBoundPRTask(t, st, agent)
	turnPREventsRouteOn(t, s)
	feedbackTestSendReceipt(hub, agentSendReply{OK: true})
	source, forge := newFeedbackTestForge(t)
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)

	source.mu.Lock()
	source.comments = []map[string]any{
		feedbackTestComment(21, now, "CI passed"),
		feedbackTestComment(22, now, "Coverage passed <!-- persona:no-wake -->"),
	}
	source.reviews = []githubPRReview{feedbackTestReview(23, now, "LGTM")}
	source.mu.Unlock()
	observedAt := now.Add(deployRecheck)
	before := len(sentToAgent(hub))
	s.sweepPRStatuses(ctx, observedAt)
	if got := sentToAgent(hub)[before:]; len(got) != 0 {
		t.Fatalf("routine feedback triggered an immediate wake: %q", got)
	}
	q := feedbackTestQueue(t, s, task)
	if q.HeadSHA != "head-one" || len(q.Suppressed) != 2 {
		t.Fatalf("want only the two human acknowledgments in the buffer: %+v", q)
	}
	for _, r := range q.Suppressed {
		if r.Author != "reviewer" || r.URL == "" || r.Preview == "" || !r.ObservedAt.Equal(observedAt) {
			t.Errorf("incomplete persisted feedback record: %+v", r)
		}
	}

	restarted, restartedHub := newQuietServiceOn(t, st)
	holdDigest(restarted)
	turnPREventsRouteOn(t, restarted)
	restarted.deploy.forge = forge
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	restored := todoNow(s, task.ID)
	restarted.todos.mu.Lock()
	restarted.todos.todos[restored.ID] = &restored
	restarted.todos.activeBySession[[2]string{agent.MachineID, agent.SessionID}] = restored.ID
	restarted.todos.mu.Unlock()
	restartedHub.setSessions(agent.MachineID, agent)
	feedbackTestSendReceipt(restartedHub, agentSendReply{OK: true})
	due := observedAt.Add(6 * time.Hour)
	if err := restarted.refreshPRFeedback(ctx, task.ID, task.PRURL, q.HeadSHA, forge, due.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := sentToAgent(restartedHub); len(got) != 0 {
		t.Fatalf("fallback woke before six hours: %q", got)
	}
	restarted.sweepPRStatuses(ctx, due)
	got := sentToAgent(restartedHub)
	if len(got) != 1 {
		t.Fatalf("expected one stale PR digest at six hours after restart, got %q", got)
	}
	for _, want := range []string{"after six hours", "head-one", "#issuecomment-21", "CI passed", "#pullrequestreview-23", "LGTM"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("fallback lost %q: %s", want, got[0])
		}
	}
	if strings.Contains(got[0], "#issuecomment-22") {
		t.Fatalf("explicit opt-out entered the fallback: %s", got[0])
	}
	restarted.sweepPRStatuses(ctx, due.Add(deployRecheck))
	if q := feedbackTestQueue(t, restarted, task); len(q.Suppressed) != 0 || q.Delivering != nil || len(sentToAgent(restartedHub)) != 1 {
		t.Fatalf("fallback was not consumed once: %+v", q)
	}
}

func TestPRFeedbackRetryKeepsDiscussionWhenTheReviewHeadMoves(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	st := &feedbackTestFaultStore{Store: &memStore{}, key: relayKey}
	s, hub, task := newBoundPRTask(t, st, feedbackTestAgent(now, true))
	turnPREventsRouteOn(t, s)
	source, forge := newFeedbackTestForge(t)
	s.deploy.forge = forge
	s.sweepPRStatuses(t.Context(), now)
	source.mu.Lock()
	source.comments = []map[string]any{feedbackTestComment(41, now, "Check cancellation in the worker.")}
	source.reviews = []githubPRReview{feedbackTestReview(41, now, "Change the old implementation.")}
	source.mu.Unlock()
	st.fail.Store(true)
	s.sweepPRStatuses(t.Context(), now.Add(deployRecheck))
	if q := feedbackTestQueue(t, s, task); q.Delivering == nil || len(q.Delivering.Records) != 2 {
		t.Fatalf("failed outbox write did not preserve both records for retry: %+v", q)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("failed outbox write left a signal: %+v", got)
	}
	source.mu.Lock()
	source.head = "head-two"
	source.mu.Unlock()
	st.fail.Store(false)
	s.sweepPRStatuses(t.Context(), now.Add(2*deployRecheck))
	if got := sentToAgent(hub); len(got) != 0 {
		t.Fatalf("discussion reached a stopped Agent: %q", got)
	}
	pending := storedSupervisePending(t, s)
	if len(pending) != 1 {
		t.Fatalf("expected one retried supervisor signal: %+v", pending)
	}
	for _, g := range pending {
		if !s.prEventSignalStillHolds(todoNow(s, task.ID), g) || !strings.Contains(g.Detail, "#issuecomment-41") || strings.Contains(g.Detail, "#pullrequestreview-41") {
			t.Errorf("retry should contain only the discussion after the head moved: %+v", g)
		}
	}
}

func TestPRFeedbackQueuedDiscussionFollowsPRWhileQueuedReviewsFollowHead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		discussion bool
	}{
		{name: "mixed_batch", discussion: true},
		{name: "reviews_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			s, _, _ := newBoundPRTask(t, &memStore{}, feedbackTestAgent(now, true))
			turnPREventsRouteOn(t, s)
			source, forge := newFeedbackTestForge(t)
			s.deploy.forge = forge
			s.sweepPRStatuses(t.Context(), now)
			source.mu.Lock()
			source.reviews = []githubPRReview{
				feedbackTestReview(51, now, "Revise the old implementation."),
				feedbackTestReview(52, now, "Add a regression test."),
			}
			if tc.discussion {
				source.comments = []map[string]any{feedbackTestComment(51, now, "Check the cancellation requirement.")}
			}
			source.mu.Unlock()
			s.sweepPRStatuses(t.Context(), now.Add(deployRecheck))
			pending := storedSupervisePending(t, s)
			if len(pending) != 1 {
				t.Fatalf("expected one queued supervisor wake: %+v", pending)
			}
			for _, signal := range pending {
				if _, ok := s.digestEntry(t.Context(), signal, ""); !ok {
					t.Fatal("fresh batch is missing from the supervisor digest")
				}
			}

			source.mu.Lock()
			source.head = "head-two"
			source.mu.Unlock()
			s.sweepPRStatuses(t.Context(), now.Add(2*deployRecheck))
			for _, signal := range pending {
				entry, ok := s.digestEntry(t.Context(), signal, "")
				if ok != tc.discussion {
					t.Fatalf("queued batch deliverable after head change = %t, want %t", ok, tc.discussion)
				}
				if ok && (!strings.Contains(entry.Detail, "#issuecomment-51") || !strings.Contains(entry.Detail, "head-one")) {
					t.Fatalf("queued discussion lost its comment or review provenance: %+v", entry)
				}
			}
			if got := storedSupervisePending(t, s); len(got) != 1 {
				t.Fatalf("head change queued another wake: %+v", got)
			}
		})
	}
}

func TestPRFeedbackDiscardsSuppressedFeedbackOnTerminalStateOrNewHead(t *testing.T) {
	for _, state := range []string{"merged", "closed", "open"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Second)
			s, hub, task := newBoundPRTask(t, &memStore{}, feedbackTestAgent(now, false))
			turnPREventsRouteOn(t, s)
			feedbackTestSendReceipt(hub, agentSendReply{OK: true})
			source, forge := newFeedbackTestForge(t)
			s.deploy.forge = forge
			s.sweepPRStatuses(ctx, now)
			source.mu.Lock()
			source.comments = []map[string]any{feedbackTestComment(31, now, "Thanks")}
			source.mu.Unlock()
			observedAt := now.Add(deployRecheck)
			s.sweepPRStatuses(ctx, observedAt)
			if q := feedbackTestQueue(t, s, task); len(q.Suppressed) != 1 {
				t.Fatalf("suppressed feedback was not buffered: %+v", q)
			}
			before := len(sentToAgent(hub))
			source.mu.Lock()
			source.state = state
			wantHead := ""
			if state == "open" {
				source.head = "head-two"
				wantHead = source.head
			}
			source.mu.Unlock()
			s.sweepPRStatuses(ctx, observedAt.Add(deployRecheck))
			if q := feedbackTestQueue(t, s, task); q.HeadSHA != wantHead || len(q.Suppressed) != 0 || q.Delivering != nil {
				t.Fatalf("PR transition left stale buffered feedback: %+v", q)
			}

			// Even a later observation of the original open head must not re-admit these IDs.
			source.mu.Lock()
			source.state, source.head = "open", "head-one"
			source.mu.Unlock()
			var feedback prFeedback
			p, err := forge.readPRStatus(ctx, "example/app", 12, &feedback)
			if err != nil {
				t.Fatal(err)
			}
			p.URL, p.CheckedAt = task.PRURL, observedAt.Add(7*time.Hour)
			if err := s.applyPRStatus(ctx, todoNow(s, task.ID), p); err != nil {
				t.Fatal(err)
			}
			if err := s.routePRFeedback(ctx, task.ID, p, forge, feedback); err != nil {
				t.Fatal(err)
			}
			if got := sentToAgent(hub)[before:]; len(got) != 0 {
				t.Fatalf("discarded feedback woke the Agent: %q", got)
			}
		})
	}
}
