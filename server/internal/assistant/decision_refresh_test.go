package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Stale decisions and their origins (#3804, acceptance AC1 and AC2). All names, ids and texts are placeholders.

// refreshListedTodo is the hub's owner-facing projection of one to-do, including the needs_user fallback used when
// there is no complete ask. The clients apply the same triage rules to the fields returned by GET /api/v1/todos.
func refreshListedTodo(ctx context.Context, s *Service, id string) listedTodo {
	cur := formatTodo(todoNow(s, id))
	listed := listedTodo{Todo: cur, DecisionReview: currentDecisionReview(cur)}
	if ask, ok := s.todoAsks(ctx)[id]; ok {
		listed.Ask = &ask
	}
	return listed
}

// requireRefreshOwnerFacing says task id has a current, answerable owner round in every hub projection.
func requireRefreshOwnerFacing(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	if ask := listedAsk(t, s, id); ask == nil {
		t.Error("the current decision has no ask on GET /api/v1/todos")
	}
	awaiting := false
	for _, row := range s.awaitingAcceptance(ctx) {
		awaiting = awaiting || row.todo == id
	}
	if !awaiting {
		t.Error("the current decision is absent from the owner acceptance projection")
	}
	onBoard := false
	for _, row := range s.boardRows(ctx, s.botLang(ctx), "", time.Now()) {
		onBoard = onBoard || row.id == id
	}
	if !onBoard {
		t.Error("the current decision is absent from the owner board")
	}
	listed := refreshListedTodo(ctx, s, id)
	if tr := triageTodo(listed, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section != triageWaiting {
		t.Errorf("the current decision is not in Needs you: %+v", tr)
	}
	title := todoNow(s, id).Title
	unaccepted, total := s.snapshotUnaccepted(ctx, time.UTC)
	found := false
	for _, row := range unaccepted {
		found = found || row.Title == title
	}
	if !found || total != 1 {
		t.Errorf("the current decision is absent from the owner snapshot: rows %+v, total %d", unaccepted, total)
	}
}

// requireRefreshNotOwnerFacing says a round handed back for rechecking or restatement has left every owner queue.
// Its stored ask and needs_user ledger remain as facts for the supervisor; neither may project a live owner round.
func requireRefreshNotOwnerFacing(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	if ask := listedAsk(t, s, id); ask != nil {
		t.Errorf("the handed-back round is still on GET /api/v1/todos: %v", ask)
	}
	for _, row := range s.awaitingAcceptance(ctx) {
		if row.todo == id {
			t.Errorf("the handed-back round is still in the owner acceptance projection: %+v", row)
		}
	}
	for _, row := range s.boardRows(ctx, s.botLang(ctx), "", time.Now()) {
		if row.id == id {
			t.Errorf("the handed-back round is still on the owner board: %+v", row)
		}
	}
	listed := refreshListedTodo(ctx, s, id)
	if listed.DecisionReview != nil {
		t.Errorf("the handed-back round still exposes decision_review: %+v", listed.DecisionReview)
	}
	if tr := triageTodo(listed, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section == triageWaiting {
		t.Errorf("the handed-back needs_user fallback is still in Needs you: %+v", tr)
	}
	now := time.Now()
	open, _ := s.briefDigest(ctx, now, now.Add(-time.Hour), time.UTC)
	for _, row := range open {
		if row.Decide || row.Need != "" {
			t.Errorf("the handed-back round is still a decision in the owner digest: %+v", row)
		}
	}
	title := todoNow(s, id).Title
	unaccepted, total := s.snapshotUnaccepted(ctx, time.UTC)
	for _, row := range unaccepted {
		if row.Title == title {
			t.Errorf("the handed-back round is still in the owner snapshot: %+v", row)
		}
	}
	if total != 0 {
		t.Errorf("the handed-back round is still counted by the owner snapshot: %d", total)
	}
}

// recheckPendingDecision creates a complete decision and ages it into its one recheck hand-back with the sweep's
// clock. It returns the old round and the recheck time; no wall-clock waiting is involved.
func recheckPendingDecision(t *testing.T, s *Service, f *fakeHub, todo Todo, args map[string]any) (acceptAsk, time.Time) {
	t.Helper()
	waitsForReview(t, s, f, todo.ID, "The prepared work is ready for the owner's decision.")
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatal(err)
	}
	requireRefreshOwnerFacing(t, s, todo.ID)
	old, ok := s.askCopy(todo.ID)
	if !ok {
		t.Fatal("the decision has no stored round")
	}
	recheckedAt := time.Now().Add(decisionRecheckAfter + time.Minute)
	s.sweepDecisionAges(recheckedAt)
	cur := askOf(s, todo.ID)
	if cur == nil || cur.RecheckAt.IsZero() || !cur.AskedAt.Equal(old.AskedAt) {
		t.Fatalf("the fixture did not mark the old round for recheck: %+v", cur)
	}
	return old, recheckedAt
}

// Once a decision's facts are back with their supervisor, neither its concrete ask nor the bare needs_user fallback
// may keep it in an owner queue.
func TestDecisionRefreshRecheckLeavesOwnerQueues(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	recheckPendingDecision(t, s, f, todo, dropTableDecision())
	requireRefreshNotOwnerFacing(t, s, todo.ID)
}

// A prior Don't handle receipt cannot resurrect a recheck-pending round on a real client. This exercises the actual
// GET projection, where listDecisionReviews reads the already-formatted supervision state.
func TestDecisionRefreshDismissedRecheckLeavesActualGET(t *testing.T) {
	at := time.Now().UTC()
	e := dismissalEnv(t, &memStore{}, at, true)
	dismissReview(t, e, at)
	e.s.sweepDecisionAges(at.Add(decisionRecheckAfter + time.Minute))

	w := e.must("GET", "/api/v1/todos", "")
	var out struct {
		Todos []listedTodo `json:"todos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Todos) != 1 {
		t.Fatalf("GET /api/v1/todos: %s", w.Body)
	}
	row := out.Todos[0]
	if row.Ask != nil || row.Supervision.State != "" || row.DecisionReview != nil {
		t.Fatalf("the handed-back dismissal is still a live owner round: %+v", row)
	}
	if tr := triageTodo(row, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section == triageWaiting || tr.Rule == ruleOwnerDecides {
		t.Fatalf("the handed-back dismissal returned to Needs you: %+v", tr)
	}
}

// Every way of answering the old round is closed while its facts are rechecked. In particular, explain_first must not
// turn the recheck marker into an apparently live fixed-response round.
func TestDecisionRefreshRecheckCannotBeAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]string
	}{
		{"plain OK", map[string]string{}},
		{"recommended option", map[string]string{"option": "删掉旧表"}},
		{"alternative option", map[string]string{"option": "先留着"}},
		{"you decide", map[string]string{"response": respondDecideYourself}},
		{"not what I asked", map[string]string{"response": respondNotAsked}},
		{"explain first", map[string]string{"response": respondExplainFirst}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			old, _ := recheckPendingDecision(t, s, f, todo, dropTableDecision())
			sent := len(sentToAgent(f))
			rec := answerAsk(s, todo.ID, tc.body)
			if rec.Code != http.StatusConflict {
				t.Errorf("answering the recheck-pending round: %d %s, want 409", rec.Code, rec.Body.String())
			}
			if got := len(sentToAgent(f)); got != sent {
				t.Errorf("the stale answer sent %d messages to the Agent", got-sent)
			}
			a := askOf(s, todo.ID)
			if a == nil || !a.AskedAt.Equal(old.AskedAt) || !a.AcceptedAt.IsZero() || a.ByDefault || a.Response != "" || a.Chosen != "" {
				t.Errorf("the stale answer changed its round: %+v", a)
			}
			v := todoNow(s, todo.ID).Supervision
			if r := v.Reviews[len(v.Reviews)-1]; r.OwnerResponse != "" || !r.OwnerResponseAt.IsZero() {
				t.Errorf("the stale fixed response changed the review: %+v", r)
			}
			if got := pendingKinds(s, todo.ID); slices.Contains(got, sigOwnerCannot) {
				t.Errorf("the stale fixed response signalled the supervisor: %v", got)
			}
		})
	}
}

// A reversible decision can have a deadline later than its one-day recheck. Reaching that deadline while the recheck
// is pending must not take its recommendation.
func TestDecisionRefreshRecheckCannotTakeDefault(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	args := rolloutDecision()
	args["deadline"], args["reversible"] = "72h", true
	old, _ := recheckPendingDecision(t, s, f, todo, args)
	sent := len(sentToAgent(f))
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f); len(got) != sent {
		t.Errorf("the recheck-pending recommendation went to the Agent: %q", got[sent:])
	}
	a := askOf(s, todo.ID)
	if a == nil || !a.AskedAt.Equal(old.AskedAt) || !a.AcceptedAt.IsZero() || a.ByDefault || !a.DueAt.IsZero() {
		t.Errorf("the stale deadline handled the round: %+v", a)
	}
	if got := todoNow(s, todo.ID).Supervision.State; got != reviewNeedsUser {
		t.Errorf("the stale default ended needs_user: %q", got)
	}
}

// The grace after a recheck is not permission to re-push the facts being checked. A genuinely fresh needs_user review
// replaces that round and becomes answerable, while the old recheck signal stays one-shot across manager restarts.
func TestDecisionRefreshGraceDoesNotPushAndFreshReviewAnswers(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	old, recheckedAt := recheckPendingDecision(t, s, f, todo, dropTableDecision())
	forgetSignals(t, s) // as after a restart: only the durable round and review markers remain
	s.sweepDecisionAges(recheckedAt.Add(time.Minute))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigRecheck) {
		t.Fatalf("the old recheck was handed back twice: %v", got)
	}
	pushes := len(decidePushes(t, f))
	s.sweepDecisionAges(recheckedAt.Add(time.Hour + time.Minute))
	if got := len(decidePushes(t, f)); got != pushes {
		t.Errorf("the unrefreshed round was re-pushed after the recheck grace: %d pushes, want %d", got, pushes)
	}
	if a := askOf(s, todo.ID); a == nil || !a.RaisedAt.IsZero() {
		t.Errorf("the unrefreshed round was raised to the owner: %+v", a)
	}

	again := dropTableDecision()
	again["reason"] = "Facts checked: may the old table be dropped now?"
	if _, err := reviewErr(s, todo.ID, again); err != nil {
		t.Fatal(err)
	}
	fresh := askOf(s, todo.ID)
	if fresh == nil || !fresh.AskedAt.After(old.AskedAt) || !fresh.RecheckAt.IsZero() || !fresh.RaisedAt.IsZero() {
		t.Fatalf("the fresh review did not start a clean round: %+v", fresh)
	}
	requireRefreshOwnerFacing(t, s, todo.ID)
	s.sweepDecisionAges(time.Now().Add(time.Hour))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigRecheck) {
		t.Fatalf("the fresh round inherited the old recheck: %v", got)
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "删掉旧表"}); rec.Code != http.StatusOK {
		t.Fatalf("the fresh round is not answerable: %d %s", rec.Code, rec.Body.String())
	}
}

// A legacy needs_user entry handed back as undecidable is no more an owner decision than a recheck-pending one. This
// holds both with no ask at all and with the old no-recommendation fallback ask.
func TestDecisionRefreshUndecidableLeavesOwnerQueues(t *testing.T) {
	for _, withAsk := range []bool{false, true} {
		name := "no ask"
		if withAsk {
			name = "no recommendation"
		}
		t.Run(name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			waitsForReview(t, s, f, todo.ID, "The work is ready, but there is no owner-only decision.")
			old := time.Now().Add(-92 * time.Hour).UTC()
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.State = reviewNeedsUser
				v.Reviews = append(v.Reviews, TaskReview{At: old, Verdict: reviewNeedsUser, Note: "A prepared draft waits with no decision to take"})
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if withAsk {
				s.askConclusion(ctx, todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Note: "A prepared draft waits with no decision to take"})
			}
			before := len(f.notices())
			s.superviseWatch(ctx, time.Now())
			waitFor(t, "the undecidable message", func() bool { return len(f.notices()) > before })
			v := todoNow(s, todo.ID).Supervision
			if r := v.Reviews[len(v.Reviews)-1]; r.RestateAt.IsZero() || r.OwnerResponse != "" {
				t.Fatalf("the fixture did not hand back a legacy undecidable review: %+v", r)
			}
			requireRefreshNotOwnerFacing(t, s, todo.ID)

			forgetSignals(t, s)
			s.superviseWatch(ctx, time.Now().Add(time.Hour))
			if got := pendingKinds(s, todo.ID); slices.Contains(got, sigUndecidable) {
				t.Fatalf("the undecidable review was handed back twice after a restart: %v", got)
			}
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			requireRefreshOwnerFacing(t, s, todo.ID)
			if rec := answerAsk(s, todo.ID, map[string]string{"option": "先灰度 10%"}); rec.Code != http.StatusOK {
				t.Fatalf("the restated round is not answerable: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The old no-recommendation ask also carried fixed responses. Its undecidable hand-back closes those responses too;
// they are not a way to answer a round the supervisor has to restate.
func TestDecisionRefreshUndecidableCannotUseFixedResponse(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	legacyDecision(t, s, todo.ID, "A legacy note with no decision the owner can answer")
	old := time.Now().Add(-92 * time.Hour).UTC()
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.Reviews[len(v.Reviews)-1].At = old
		return true
	}); err != nil {
		t.Fatal(err)
	}
	before := len(f.notices())
	s.superviseWatch(ctx, time.Now())
	waitFor(t, "the undecidable message", func() bool { return len(f.notices()) > before })
	round := *askOf(s, todo.ID)
	if rec := answerAsk(s, todo.ID, map[string]string{"response": respondExplainFirst}); rec.Code != http.StatusConflict {
		t.Errorf("answering the handed-back legacy round: %d %s, want 409", rec.Code, rec.Body.String())
	}
	a := askOf(s, todo.ID)
	if a == nil || !a.AskedAt.Equal(round.AskedAt) || !a.AcceptedAt.IsZero() || a.Response != "" {
		t.Errorf("the stale fixed response changed the legacy ask: %+v", a)
	}
	v := todoNow(s, todo.ID).Supervision
	if r := v.Reviews[len(v.Reviews)-1]; r.OwnerResponse != "" {
		t.Errorf("the stale fixed response changed the legacy review: %+v", r)
	}
}

// RestateAt also records a delivered fixed response. It must not hide explain_first, whose whole point is that the same
// decision stays answerable while the supervisor prepares its background.
func TestDecisionRefreshFixedResponseRoundStaysAnswerable(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "The rollout is ready for a priority call.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"response": respondExplainFirst}); rec.Code != http.StatusOK {
		t.Fatalf("explain_first: %d %s", rec.Code, rec.Body.String())
	}
	v := todoNow(s, todo.ID).Supervision
	if r := v.Reviews[len(v.Reviews)-1]; r.RestateAt.IsZero() || r.OwnerResponse != respondExplainFirst {
		t.Fatalf("the fixture did not record the fixed response: %+v", r)
	}
	requireRefreshOwnerFacing(t, s, todo.ID)
	if ask := listedAsk(t, s, todo.ID); ask == nil || ask["response"] != respondExplainFirst {
		t.Fatalf("the fixed-response round lost its existing projection: %v", ask)
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "直接全量"}); rec.Code != http.StatusOK {
		t.Fatalf("the fixed-response round no longer accepts an option: %d %s", rec.Code, rec.Body.String())
	}
}

// backgroundText names the origin frozen in the brief instead of calling every non-owner origin the assistant. The
// existing owner-message and unknown-origin fallbacks, date, where and current closeout/status stay byte-for-byte intact.
func TestDecisionRefreshBackgroundNamesOrigins(t *testing.T) {
	at := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	origins := []struct {
		by, en, zhHans, ja string
	}{
		{briefByAssistant, "the assistant", "助理", "アシスタント"},
		{briefByLeader, "a leader conversation", "负责人对话", "リーダーの会話"},
		{briefByRoutine, "a routine", "例行任务", "定期タスク"},
		{briefByHub, "Persona itself", "Persona ", "Persona "},
	}
	langs := []struct {
		name          string
		lang          i18n.Lang
		originPhrase  string
		assistantWant string
		ownerWant     string
	}{
		{"en", i18n.Lang("en"), "the assistant", "Started by the assistant on 2026-11-07, in the main conversation · Now: Closeout approved",
			"From the main conversation, 2026-11-07: “Prepare the release” · Now: Closeout approved"},
		{"zh-Hans", i18n.Lang("zh-Hans"), "助理", "助理于 2026-11-07 在主对话发起 · 现状：Closeout approved",
			"来自主对话，2026-11-07：「Prepare the release」 · 现状：Closeout approved"},
		{"ja", i18n.Lang("ja"), "アシスタント", "アシスタントが 2026-11-07 にメインの会話で開始 · 現状：Closeout approved",
			"メインの会話より、2026-11-07：「Prepare the release」 · 現状：Closeout approved"},
	}
	for _, lang := range langs {
		t.Run(lang.name, func(t *testing.T) {
			for _, origin := range origins {
				t.Run(origin.by, func(t *testing.T) {
					phrase := origin.en
					switch lang.name {
					case "zh-Hans":
						phrase = origin.zhHans
					case "ja":
						phrase = origin.ja
					}
					want := strings.Replace(lang.assistantWant, lang.originPhrase, phrase, 1)
					ask := &acceptAsk{From: &askFrom{Main: true, At: at, ByAssistant: true, StartedBy: origin.by}, Stands: "Closeout approved"}
					if got := backgroundText(lang.lang, time.UTC, ask); got != want {
						t.Errorf("background %q, want %q", got, want)
					}
				})
			}
			owner := &acceptAsk{From: &askFrom{Main: true, At: at, Message: "Prepare the release", StartedBy: briefByOwner}, Stands: "Closeout approved"}
			if got := backgroundText(lang.lang, time.UTC, owner); got != lang.ownerWant {
				t.Errorf("owner background changed: %q, want %q", got, lang.ownerWant)
			}
			unknown := &acceptAsk{From: &askFrom{Main: true, At: at, ByAssistant: true, StartedBy: briefByUnknown}, Stands: "Closeout approved"}
			if got := backgroundText(lang.lang, time.UTC, unknown); got != lang.assistantWant {
				t.Errorf("unknown-origin fallback changed: %q, want %q", got, lang.assistantWant)
			}
		})
	}
}

// A decision that ages past its recheck while its conversation is stopped (/stop, #3308) stays with the owner, since
// signal would drop the hand-back then; once the user speaks again the sweep hands it back (#13415).
func TestDecisionRecheckUnderStopWaitsForTheUser(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "The prepared work is ready for the owner's decision.")
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err != nil {
		t.Fatal(err)
	}
	s.markReporter(ctx, "box", todo.Origin)
	s.setReporterStopped(ctx, todo.Origin, "app")
	at := time.Now().Add(decisionRecheckAfter + time.Minute)
	s.sweepDecisionAges(at)
	if a := askOf(s, todo.ID); a == nil || !a.RecheckAt.IsZero() {
		t.Fatal("the decision was marked for recheck while stopped, though nothing was queued")
	}
	requireRefreshOwnerFacing(t, s, todo.ID)
	s.setReporterStopped(ctx, todo.Origin, "")
	s.sweepDecisionAges(at.Add(time.Minute))
	if a := askOf(s, todo.ID); a == nil || a.RecheckAt.IsZero() {
		t.Fatal("the decision was not handed back once /stop was lifted")
	}
	if !slices.Contains(pendingKinds(s, todo.ID), sigRecheck) {
		t.Fatalf("no recheck signal once /stop was lifted: %v", pendingKinds(s, todo.ID))
	}
}
