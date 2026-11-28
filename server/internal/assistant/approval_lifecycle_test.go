package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

func preparedPauseState(ft *fleetTest, id string) (paused bool, accepted, answered uint64, runs int) {
	ft.s.fleet.mu.Lock()
	defer ft.s.fleet.mu.Unlock()
	p := ft.s.fleet.interactions[id]
	if p == nil || p.prepared == nil {
		return false, 0, 0, 0
	}
	return p.prepared.paused, p.prepared.acceptedSeq, p.prepared.answeredSeq, len(p.prepared.channelRuns)
}

func TestClarificationTurnsCoverOnlyTheirAcceptedQuestions(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedTest(t, ft, "ix_answer_turns", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_answer_turns", "First question", "owner-turn-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ref := SessionRef{MachineID: "mac", SessionID: "s1"}
	// The tool turn that produced the card may end after clarification. It is not
	// the answer turn and must not restart the original decision timer.
	ft.s.approvalAnswerTurnEnded(ref)
	if paused, _, _, _ := preparedPauseState(ft, "ix_answer_turns"); !paused {
		t.Fatal("the original tool turn ended the clarification pause")
	}
	ft.s.approvalAnswerTurnStarted(ref)
	if err := ft.s.clarify(context.Background(), "ix_answer_turns", "Question during the answer", "owner-turn-2", nil); err != nil {
		t.Fatal(err)
	}
	ft.s.approvalAnswerTurnEnded(ref)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_answer_turns"); !paused || accepted != 2 || answered != 1 {
		t.Fatalf("first answer turn covered a question accepted during it: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}
	ft.s.approvalAnswerTurnStarted(ref)
	ft.s.approvalAnswerTurnEnded(ref)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_answer_turns"); paused || accepted != 2 || answered != 2 {
		t.Fatalf("next answer turn did not cover the queued question: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}
	deny := false
	if err := ft.s.resolve("ix_answer_turns", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_answer_turns")
}

func TestClarificationQuestionWinsExpiryRaceWithoutLosingCard(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	started := make(chan struct{})
	release := make(chan struct{})
	ft.f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		close(started)
		<-release
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		ft.f.sentOnce = append(ft.f.sentOnce, p) // answerNode runs under the fake's lock
		return map[string]any{}, nil
	})
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_question_expiry_race", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "applied", nil
	})
	questionDone := make(chan error, 1)
	go func() {
		questionDone <- ft.s.clarify(context.Background(), "ix_question_expiry_race", "Question racing expiry", "owner-expiry-race", nil)
	}()
	<-started
	ft.s.fleet.mu.Lock()
	p := ft.s.fleet.interactions["ix_question_expiry_race"]
	ft.s.fleet.mu.Unlock()
	expired := make(chan struct{})
	go func() {
		ft.s.expirePrepared(p)
		close(expired)
	}()
	select {
	case <-expired:
		t.Fatal("expiry did not wait for the in-flight question")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-questionDone; err != nil {
		t.Fatal(err)
	}
	<-expired
	if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
		t.Fatalf("question did not win expiry race: %+v, %v", got.out, got.err)
	}
	ft.waitPending(1)
	if applies.Load() != 0 {
		t.Fatal("the question/expiry race executed the operation")
	}
	allow := true
	if err := ft.s.resolve("ix_question_expiry_race", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if applies.Load() != 1 {
		t.Fatalf("Allow after race applied %d times", applies.Load())
	}
	waitApprovalResult(t, ft, "ix_question_expiry_race")
}

func TestClarificationLosesToClaimedAllowWithoutSending(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	applyStarted := make(chan struct{})
	releaseApply := make(chan struct{})
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_allow_question_race", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		close(applyStarted)
		<-releaseApply
		return "applied", nil
	})
	allow := true
	resolved := make(chan error, 1)
	go func() { resolved <- ft.s.resolve("ix_allow_question_race", proto.Resolution{Allow: &allow}) }()
	<-applyStarted
	if err := ft.s.clarify(context.Background(), "ix_allow_question_race", "Too late", "owner-after-allow", nil); !errors.Is(err, errClarifyUnavailable) {
		t.Fatalf("question after final claim = %v", err)
	}
	if got := len(ownerQuestionSends(ft)); got != 0 {
		t.Fatalf("question after final claim produced %d sends", got)
	}
	close(releaseApply)
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	if got := awaitPreparedTest(t, done); got.err != nil || got.out.Clarifying || got.out.Text != "applied" {
		t.Fatalf("original Allow result = %+v, %v", got.out, got.err)
	}
	if applies.Load() != 1 {
		t.Fatalf("racing Allow applied %d times", applies.Load())
	}
}

func TestClarifyDeletedConversationReturnsVisibleErrorWithoutFallback(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	other := proto.Session{MachineID: "mac", SessionID: "other", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
	ft.f.setSessions("mac", other)
	ft.d.setSessions("mac", []proto.Session{{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle}, other})
	row := deletedConversation{Machine: "mac", ID: "s1", At: time.Now().UTC()}
	if err := ft.st.PutSetting(context.Background(), deletedConversationsKey, map[string]deletedConversation{runtimeListID("mac", "s1"): row}); err != nil {
		t.Fatal(err)
	}
	done := startPreparedTest(t, ft, "ix_deleted_origin", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_deleted_origin", "Will this go elsewhere?", "owner-deleted-origin", nil); convCode(err) != "conversation_deleted" {
		t.Fatalf("deleted-origin clarification error = %v", err)
	}
	if got := len(ownerQuestionSends(ft)); got != 0 {
		t.Fatalf("deleted-origin question fell back to another conversation: %+v", ownerQuestionSends(ft))
	}
	select {
	case got := <-done:
		t.Fatalf("deleted-origin failure released the original tool: %+v", got)
	default:
	}
	deny := false
	if err := ft.s.resolve("ix_deleted_origin", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
}

func TestClarifyArchivedConversationExplicitlyReopensSameConversation(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	if _, err := ft.s.setArchived(context.Background(), "mac", "s1", true, titleByUser); err != nil {
		t.Fatal(err)
	}
	if !ft.s.isArchived("s1") {
		t.Fatal("fixture did not archive the originating conversation")
	}
	done := startPreparedTest(t, ft, "ix_archived_origin", "", nil)
	question := "Reopen this exact conversation"
	if err := ft.s.clarify(context.Background(), "ix_archived_origin", question, "owner-archived-origin", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	if ft.s.isArchived("s1") {
		t.Fatal("the archived origin was not explicitly reopened")
	}
	sent := ownerQuestionSends(ft)
	if len(sent) != 1 || sent[0]["session_id"] != "s1" || sent[0]["text"] != question {
		t.Fatalf("archived-origin delivery = %+v", sent)
	}
	deny := false
	if err := ft.s.resolve("ix_archived_origin", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_archived_origin")
}

func TestClarifyEndedRoomReturnsVisibleError(t *testing.T) {
	r := newRoomFixture(t)
	sid := r.roomUp(meetingRec, "participant")
	r.do("POST", livePath(meetingRec), "", `{"from":0,"segments":[{"start":1,"end":3,"text":"Ada: done."}]}`, 200)
	end := time.Now().UTC().Format(time.RFC3339)
	r.upload(meetingRec, 2, map[string]any{"room": map[string]any{"attendance": "participant"}, "ended_at": end, "complete": true})
	r.look(meetingRec)
	r.f.mu.Lock()
	for i := range r.f.sessions["server"] {
		if r.f.sessions["server"][i].SessionID == sid {
			r.f.sessions["server"][i].UpdatedAt = time.Now().Add(time.Second)
		}
	}
	r.f.mu.Unlock()
	r.look(meetingRec)
	done := make(chan preparedTestResult, 1)
	go func() {
		out, err := r.s.waitPreparedCard(context.Background(), proto.Interaction{ID: "ix_ended_room", MachineID: "server", SessionID: sid,
			Kind: proto.IxPermission, ExpiresAt: time.Now().Add(time.Hour)}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
			return "unexpected", nil
		}})
		done <- preparedTestResult{out: out, err: err}
	}()
	waitFor(t, "ended-room approval registration", func() bool {
		_, ok := r.s.pendingIx("ix_ended_room")
		return ok
	})
	if err := r.s.clarify(context.Background(), "ix_ended_room", "Can you answer here?", "owner-ended-room", nil); !isCode(err, CodeMeetingEnded) {
		t.Fatalf("ended-room clarification error = %v", err)
	}
	r.f.mu.Lock()
	var sentQuestion bool
	for _, p := range r.f.sentOnce {
		if p["text"] == "Can you answer here?" {
			sentQuestion = true
		}
	}
	r.f.mu.Unlock()
	if sentQuestion {
		t.Fatal("the ended-room question was sent anyway")
	}
	deny := false
	if err := r.s.resolve("ix_ended_room", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.out.Clarifying {
			t.Fatalf("ended-room Deny = %+v, %v", got.out, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ended-room original tool did not finish on Deny")
	}
}

func drainInOrder(t *testing.T, s *Service) {
	t.Helper()
	done := make(chan struct{})
	s.inOrder(func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("inOrder queue did not drain")
	}
}

func TestClarificationResumesFromObservedSessionTurns(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	sess := func(status string) proto.Session {
		return proto.Session{
			MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude",
			Status: status, State: proto.StateIdle, UpdatedAt: time.Now().UTC(),
		}
	}
	// The original tool turn is currently blocked waiting on the permission card.
	ft.sessions("mac", sess(proto.StatusBlocked))
	drainInOrder(t, ft.s)

	done := startPreparedTest(t, ft, "ix_observed_turns", "", nil)
	waitFor(t, "observed-turns approval registration", func() bool {
		_, ok := ft.s.pendingIx("ix_observed_turns")
		return ok
	})
	if err := ft.s.clarify(context.Background(), "ix_observed_turns", "Why this branch?", "owner-observed-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)

	// Releasing the tool call lets the original tool turn unblock (StatusBlocked -> StatusWorking, newTurn=false)
	// and finish (StatusDone). That original turn must not resume the decision timer.
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_observed_turns"); !paused || accepted != 1 || answered != 0 {
		t.Fatalf("original tool turn resumed clarification pause: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	// The actual answer turn starts from StatusDone -> StatusWorking (newTurn=true) and finishes -> StatusDone.
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	waitFor(t, "clarification pause resumes after observed answer turn", func() bool {
		paused, accepted, answered, _ := preparedPauseState(ft, "ix_observed_turns")
		return !paused && accepted == 1 && answered == 1
	})
	if _, ok := ft.s.approvalNudgeDeadline(proto.Interaction{ID: "ix_observed_turns"}); !ok {
		t.Fatal("approvalNudgeDeadline remained paused after answer turn ended")
	}

	deny := false
	if err := ft.s.resolve("ix_observed_turns", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_observed_turns")
}

func TestClarificationResumesWhenAnswerTurnRacesSendOnce(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	sess := func(status string) proto.Session {
		return proto.Session{
			MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude",
			Status: status, State: proto.StateIdle, UpdatedAt: time.Now().UTC(),
		}
	}
	ft.sessions("mac", sess(proto.StatusIdle))
	drainInOrder(t, ft.s)

	// Case A: the node starts the answer turn (StatusIdle -> StatusWorking) before sendOnce returns.
	ft.f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		ft.f.sentOnce = append(ft.f.sentOnce, p)
		ft.f.mu.Unlock()
		defer ft.f.mu.Lock()
		ft.sessions("mac", sess(proto.StatusWorking))
		drainInOrder(t, ft.s)
		return map[string]any{}, nil
	})

	done := startPreparedTest(t, ft, "ix_sendonce_race", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_sendonce_race", "First racing question", "owner-race-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)

	ft.sessions("mac", sess(proto.StatusDone))
	waitFor(t, "pause resumes after answer turn that started during sendOnce", func() bool {
		paused, accepted, answered, _ := preparedPauseState(ft, "ix_sendonce_race")
		return !paused && accepted == 1 && answered == 1
	})

	// Case B: a follow-up question's entire answer turn (StatusWorking -> StatusDone) completes before sendOnce returns.
	ft.f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		ft.f.sentOnce = append(ft.f.sentOnce, p)
		ft.f.mu.Unlock()
		defer ft.f.mu.Lock()
		ft.sessions("mac", sess(proto.StatusWorking))
		drainInOrder(t, ft.s)
		ft.sessions("mac", sess(proto.StatusDone))
		drainInOrder(t, ft.s)
		return map[string]any{}, nil
	})

	if err := ft.s.clarify(context.Background(), "ix_sendonce_race", "Second fast question", "owner-race-2", nil); err != nil {
		t.Fatal(err)
	}
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_sendonce_race"); paused || accepted != 2 || answered != 2 {
		t.Fatalf("pause did not resume when answer turn finished during sendOnce: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	// Case C: when an earlier question is still unanswered, a turn starting during a follow-up question's sendOnce
	// may belong to the earlier question, so it must not prematurely mark the follow-up question answered.
	ft.f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		ft.f.sentOnce = append(ft.f.sentOnce, p)
		return map[string]any{}, nil
	})
	if err := ft.s.clarify(context.Background(), "ix_sendonce_race", "Third queued question", "owner-race-3", nil); err != nil {
		t.Fatal(err)
	}
	ft.f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		ft.f.sentOnce = append(ft.f.sentOnce, p)
		ft.f.mu.Unlock()
		defer ft.f.mu.Lock()
		ft.sessions("mac", sess(proto.StatusWorking))
		drainInOrder(t, ft.s)
		return map[string]any{}, nil
	})
	if err := ft.s.clarify(context.Background(), "ix_sendonce_race", "Fourth queued question", "owner-race-4", nil); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", sess(proto.StatusDone))
	waitFor(t, "earlier question answered while follow-up stays paused", func() bool {
		paused, accepted, answered, _ := preparedPauseState(ft, "ix_sendonce_race")
		return paused && accepted == 4 && answered == 3
	})
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	waitFor(t, "follow-up question resumes pause after its own answer turn", func() bool {
		paused, accepted, answered, _ := preparedPauseState(ft, "ix_sendonce_race")
		return !paused && accepted == 4 && answered == 4
	})

	deny := false
	if err := ft.s.resolve("ix_sendonce_race", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_sendonce_race")
}

// BLOCKER #1: Headless assistant sessions (Claude/Codex) stay StatusWorking throughout waitPreparedCard
// and either steer into the running turn or coalesce result->started inside notifySessionsSoon, emitting
// only StatusWorking -> StatusDone with no StatusBlocked or intermediate stopped edge.
func TestClarificationResumesForHeadlessContinuousWorkingTurn(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	sess := func(status string) proto.Session {
		return proto.Session{
			MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "codex",
			Status: status, State: proto.StateWorking, UpdatedAt: time.Now().UTC(),
		}
	}
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)

	done := startPreparedTest(t, ft, "ix_headless_working", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_headless_working", "Which repo will this touch?", "owner-headless-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)

	// A single StatusWorking -> StatusDone transition (no StatusDone -> StatusWorking edge in between)
	// must mark the steered/coalesced clarification answered and resume the decision timer.
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_headless_working"); paused || accepted != 1 || answered != 1 {
		t.Fatalf("headless continuous working turn did not resume pause: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}
	if _, ok := ft.s.approvalNudgeDeadline(proto.Interaction{ID: "ix_headless_working"}); !ok {
		t.Fatal("approvalNudgeDeadline remained paused after headless answer turn ended")
	}

	// An unrelated later turn while no clarification is pending starts StatusWorking, and the owner
	// asks a follow-up question while that turn is already StatusWorking -> single StatusDone resumes too.
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	if err := ft.s.clarify(context.Background(), "ix_headless_working", "Follow-up during active turn", "owner-headless-2", nil); err != nil {
		t.Fatal(err)
	}
	// A third question submitted while question 2's clarification turn is already active must still wait
	// for the next turn per docs/assistant/approval-clarification.md.
	if err := ft.s.clarify(context.Background(), "ix_headless_working", "Queued behind answer turn", "owner-headless-3", nil); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_headless_working"); !paused || accepted != 3 || answered != 2 {
		t.Fatalf("mid-answer follow-up did not wait for next turn: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_headless_working"); paused || accepted != 3 || answered != 3 {
		t.Fatalf("next turn did not resume queued follow-up: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	deny := false
	if err := ft.s.resolve("ix_headless_working", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_headless_working")
}

// BLOCKER #2: Clarifying a reaped, stopped, or reopened archived conversation transitions
// StatusOffline -> StatusWorking (or non-working StatusUnknown -> StatusWorking), where fleet_sessions
// sets newTurn=false; clarification must still recognize the answer turn and resume on StatusDone.
func TestClarificationResumesFromOfflineAndUnknownSessionTurns(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	sess := func(status string) proto.Session {
		return proto.Session{
			MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude",
			Status: status, State: proto.StateIdle, UpdatedAt: time.Now().UTC(),
		}
	}
	// Reaped / stopped / archived session is StatusOffline before clarification.
	ft.sessions("mac", sess(proto.StatusOffline))
	drainInOrder(t, ft.s)

	done := startPreparedTest(t, ft, "ix_offline_resume", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_offline_resume", "Reopen and explain", "owner-offline-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)

	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_offline_resume"); paused || accepted != 1 || answered != 1 {
		t.Fatalf("StatusOffline -> StatusWorking answer turn did not resume pause: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	// Non-working StatusUnknown -> StatusWorking (was idle before unknown) must also resume.
	ft.sessions("mac", sess(proto.StatusUnknown))
	drainInOrder(t, ft.s)
	if err := ft.s.clarify(context.Background(), "ix_offline_resume", "Explain after unknown", "owner-offline-2", nil); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_offline_resume"); paused || accepted != 2 || answered != 2 {
		t.Fatalf("non-working StatusUnknown -> StatusWorking did not resume pause: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	// Working -> StatusUnknown -> StatusWorking blip (wasWorking=true) must NOT treat the same turn
	// as a brand-new turn that covers a follow-up question accepted mid-turn.
	if err := ft.s.clarify(context.Background(), "ix_offline_resume", "Question 3", "owner-offline-3", nil); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	if err := ft.s.clarify(context.Background(), "ix_offline_resume", "Question 4 during turn 3", "owner-offline-4", nil); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", sess(proto.StatusUnknown))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_offline_resume"); !paused || accepted != 4 || answered != 3 {
		t.Fatalf("Working->Unknown->Working blip misattributed Q4 to turn 3: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	deny := false
	if err := ft.s.resolve("ix_offline_resume", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_offline_resume")
}

type blockingA2AStore struct {
	*memA2AStore
	onReady func(ctx context.Context)
}

func (b *blockingA2AStore) A2AReady(ctx context.Context, machine, session string) error {
	if b.onReady != nil {
		b.onReady(ctx)
	}
	return b.memA2AStore.A2AReady(ctx, machine, session)
}

// BLOCKER #3: A delayed async TurnEnded goroutine (for example blocked in Postgres A2AReady)
// from an earlier turn must never run approvalAnswerTurnEnded out of order after a later turn's
// synchronous inOrder turn-start.
func TestClarificationTurnEndOrderSurvivesDelayedTurnEnded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	st := &blockingA2AStore{
		memA2AStore: newMemA2AStore(),
		onReady: func(ctx context.Context) {
			// Only block TurnEnded's A2AReady (which carries supervisionEndKey), not inOrder's a2aIdleRefs.
			if ctx.Value(supervisionEndKey{}) == nil {
				return
			}
			if calls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		},
	}
	f := newFakeHub()
	d := newFleetDevs()
	d.add("mac", "Mac")
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d})
	ft := &fleetTest{t: t, s: s, d: d, f: f}
	preparedTestConversation(ft)

	sess := func(status string) proto.Session {
		return proto.Session{
			MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude",
			Status: status, State: proto.StateIdle, UpdatedAt: time.Now().UTC(),
		}
	}
	ft.sessions("mac", sess(proto.StatusBlocked))
	drainInOrder(t, ft.s)

	done := startPreparedTest(t, ft, "ix_delayed_turn_end", "", nil)
	waitFor(t, "delayed-turn-end approval registration", func() bool {
		_, ok := ft.s.pendingIx("ix_delayed_turn_end")
		return ok
	})
	if err := ft.s.clarify(context.Background(), "ix_delayed_turn_end", "Explain this action", "owner-order-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)

	// Turn 1 (original tool turn) unblocks and ends -> spawns go s.TurnEnded which blocks in A2AReady.
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Turn 1's TurnEnded did not reach A2AReady")
	}

	// While Turn 1's TurnEnded is still blocked in A2AReady, Turn 2 (the answer turn) starts.
	ft.sessions("mac", sess(proto.StatusWorking))
	drainInOrder(t, ft.s)

	// Plant a stopMark to observe deterministically when Turn 1's delayed TurnEnded runs past approvalAnswerTurnEnded.
	ref := SessionRef{MachineID: "mac", SessionID: "s1"}
	ft.s.tasks.quiet.mu.Lock()
	if ft.s.tasks.quiet.stops == nil {
		ft.s.tasks.quiet.stops = map[SessionRef]stopMark{}
	}
	ft.s.tasks.quiet.stops[ref] = stopMark{at: time.Now().Add(-time.Second)}
	ft.s.tasks.quiet.mu.Unlock()

	close(release)
	waitFor(t, "Turn 1 delayed TurnEnded ran past approvalAnswerTurnEnded", func() bool {
		ft.s.tasks.quiet.mu.Lock()
		defer ft.s.tasks.quiet.mu.Unlock()
		return !ft.s.tasks.quiet.stops[ref].ended.IsZero()
	})

	// Turn 2 is STILL running, so the delayed TurnEnded from Turn 1 must NOT have resumed the pause or cleared turnActive.
	paused, accepted, answered, _ := preparedPauseState(ft, "ix_delayed_turn_end")
	ft.s.fleet.mu.Lock()
	turnActive := ft.s.fleet.interactions["ix_delayed_turn_end"].prepared.turnActive
	ft.s.fleet.mu.Unlock()
	if !paused || accepted != 1 || answered != 0 || !turnActive {
		t.Fatalf("delayed Turn 1 TurnEnded corrupted Turn 2: paused=%v accepted=%d answered=%d turnActive=%v", paused, accepted, answered, turnActive)
	}

	// Now Turn 2 finishes -> pause must resume cleanly.
	ft.sessions("mac", sess(proto.StatusDone))
	drainInOrder(t, ft.s)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_delayed_turn_end"); paused || accepted != 1 || answered != 1 {
		t.Fatalf("Turn 2 end did not resume pause: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}

	deny := false
	if err := ft.s.resolve("ix_delayed_turn_end", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_delayed_turn_end")
}
