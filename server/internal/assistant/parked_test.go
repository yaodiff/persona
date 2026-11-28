package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// requireParkEnded checks the one change besides its receipt that ending a park makes to review history (#10798):
// the park that ended records park_ended_at, no earlier than the park and no later than the wake. It returns after
// with that field reset, so the caller still compares every other field with before.
func requireParkEnded(t *testing.T, before *Supervision, after []TaskReview, wokeBy time.Time) []TaskReview {
	t.Helper()
	park := before.parking()
	if park == nil {
		t.Fatal("no current park to end")
	}
	if len(after) != len(before.Reviews) {
		t.Fatalf("review history length=%d, want unchanged %d", len(after), len(before.Reviews))
	}
	out := slices.Clone(after)
	ended := 0
	for i := range out {
		r, was := &out[i], before.Reviews[i]
		if r.ParkEndedAt.Equal(was.ParkEndedAt) {
			continue
		}
		ended++
		if r.Verdict != reviewParked || !r.At.Equal(park.At) || !was.ParkEndedAt.IsZero() {
			t.Fatalf("park_ended_at changed on review %d (%s at %v, was %v), not on the park that ended (at %v)",
				i, r.Verdict, r.At, was.ParkEndedAt, park.At)
		}
		if r.ParkEndedAt.Before(park.At) || r.ParkEndedAt.After(wokeBy) {
			t.Fatalf("park_ended_at %v is outside the park: from %v to the wake by %v", r.ParkEndedAt, park.At, wokeBy)
		}
		r.ParkEndedAt = was.ParkEndedAt
	}
	if ended != 1 {
		t.Fatalf("park_ended_at was recorded on %d reviews, want exactly the park that ended", ended)
	}
	return out
}

// wakeBound is the latest time a wake run at pass time now can have recorded: the pass time, or for a write that
// takes the wall clock, the moment the wake returned.
func wakeBound(now time.Time) time.Time {
	if wall := time.Now(); wall.After(now) {
		return wall
	}
	return now
}

// testParkEvidence is a source-backed park fixture for admission tests. Its receipt was sent before the test starts.
func testParkEvidence(kind, target, sourceURL, wake string) map[string]any {
	evidence := map[string]any{"kind": kind, "next_actor": "external", "target": target, "wake": wake}
	switch kind {
	case "person_reply":
		evidence["contact"] = map[string]any{"channel": "GitHub discussion", "url": "https://example.com/messages/42", "sender": "task supervisor",
			"sent_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "request": "Please reply with the agreed decision.", "status": "sent"}
	case "merge_review":
		evidence["url"], evidence["ready"] = sourceURL, true
		evidence["contact"] = map[string]any{"channel": "GitHub review request", "url": "https://example.com/messages/43", "sender": "task supervisor",
			"sent_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "request": "Please review and merge this ready change.", "status": "sent"}
	case "external_event":
		evidence["url"] = sourceURL
	}
	return evidence
}

// waitingPark is a complete parked review: the task waits for someone else, for three days.
func waitingPark() map[string]any {
	return map[string]any{"verdict": "parked", "reason": "The design team has to pick the icon set first", "waiting_on": "someone_else", "until": "3d",
		"evidence": testParkEvidence("person_reply", "design team", "", "The design team replies with its icon choice")}
}

// seedLegacyPark records an old park directly in the persisted supervision ledger. Legacy owner_priority rows remain
// readable, but new task_review admissions must use needs_user for an owner decision.
func seedLegacyPark(t *testing.T, s *Service, id, reason, waitingOn, until string) {
	t.Helper()
	at := time.Now().UTC()
	deadline, err := parseParkUntil(until, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(context.Background(), id, func(v *Supervision) bool {
		v.State = reviewParked
		v.Reviews = append(v.Reviews, TaskReview{At: at, Verdict: reviewParked, Reason: reason, Until: deadline, WaitingOn: waitingOn, Parks: 1})
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

// noSessions is a lookup that finds no session: the to-do's agents are closed.
func noSessions(string, string) (proto.Session, bool) { return proto.Session{}, false }

// forgetUntold drops the signals that wait and what was told, as a manager restarted before its supervision message
// went out has them: nothing in memory.
func forgetUntold(s *Service) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer, b.timerAt = nil, time.Time{}
	}
	b.pending, b.sent = map[string]superviseSignal{}, map[string]string{}
}

// parked is refused without each thing it has to say: the refusal names what is missing, and the task is left as it was.
func TestParkedIsRefusedWithoutEachRequiredField(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	for _, tc := range []struct {
		name string
		drop func(map[string]any)
		want string
	}{
		{"no reason", func(a map[string]any) { delete(a, "reason") }, "reason (one sentence: what the task waits for)"},
		{"a blank reason", func(a map[string]any) { a["reason"] = "  " }, "reason (one sentence: what the task waits for)"},
		{"no waiting_on", func(a map[string]any) { delete(a, "waiting_on") }, "waiting_on (what kind of thing it waits on: someone_else, external_event, date)"},
		{"a waiting_on that is none of them", func(a map[string]any) { a["waiting_on"] = "later" }, "waiting_on ("},
		{"an owner decision is not a park", func(a map[string]any) { a["waiting_on"] = parkOwnerPriority }, "owner_priority is an owner decision: use needs_user"},
		{"an until that is no time", func(a map[string]any) { a["until"] = "soon" }, `until "soon": give a duration`},
		{"an until past 30 days", func(a map[string]any) { a["until"] = "31d" }, "must be between 1 minute and 30 days"},
		{"an until that passed", func(a map[string]any) { a["until"] = "2020-01-01T00:00:00Z" }, "must be between 1 minute and 30 days"},
	} {
		args := waitingPark()
		tc.drop(args)
		_, err := reviewErr(s, todo.ID, args)
		if err == nil || !strings.Contains(err.Error(), "parked is refused") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to name %q", tc.name, err, tc.want)
		}
		if v := todoNow(s, todo.ID).Supervision; v.State != "" || len(v.Reviews) != 0 {
			t.Fatalf("%s: the ledger changed: %+v", tc.name, v)
		}
	}
	// Everything missing is named at once.
	_, err := reviewErr(s, todo.ID, map[string]any{"verdict": "parked"})
	if err == nil || !strings.Contains(err.Error(), "reason (") || !strings.Contains(err.Error(), "waiting_on (") {
		t.Fatalf("err = %v", err)
	}
	// The fields belong to parked alone.
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "cancelled", "until": "3d"}); err == nil || !strings.Contains(err.Error(), "parked only") {
		t.Fatalf("until on another verdict: %v", err)
	}
	// A needs_user that is no decision is told it can park (#3926's refusal).
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "needs_user", "note": "waits for the experiment's scope"}); err == nil ||
		!strings.Contains(err.Error(), "park it (parked, with reason, waiting_on, evidence and until)") {
		t.Fatalf("the needs_user refusal does not offer parking: %v", err)
	}
	// Only the task's supervisor parks it, as for the other verdicts.
	args := waitingPark()
	args["todo_id"] = todo.ID
	raw, _ := json.Marshal(args)
	if _, err := s.runTool(context.Background(), "server", "conv2", "task_review", raw); err == nil ||
		!strings.Contains(err.Error(), "was not handed out from this conversation") {
		t.Fatalf("another conversation parked it: %v", err)
	}
}

// until: a duration that may count days, an RFC 3339 time, or nothing (a day).
func TestParkUntil(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for raw, want := range map[string]time.Duration{"": 24 * time.Hour, "3d": 72 * time.Hour, "2d12h": 60 * time.Hour, "36h": 36 * time.Hour,
		"90m": 90 * time.Minute, "30d": 30 * 24 * time.Hour, "2026-11-14T09:00:00+08:00": 88 * time.Hour} {
		at, err := parseParkUntil(raw, now)
		if err != nil || !at.Equal(now.Add(want)) {
			t.Errorf("%q: %v, %v; want %v", raw, at, err, now.Add(want))
		}
	}
	for _, raw := range []string{"soon", "d", "-1d", "3d-1h", "30d1m", "10s", "2026-11-14"} {
		if at, err := parseParkUntil(raw, now); err == nil {
			t.Errorf("%q was taken as %v", raw, at)
		}
	}
}

// A parked task stays open and is nobody's to answer: its state and what it waits for are on the to-do, nothing asks
// the owner, and none of the signals that say work stopped are raised while it is parked.
func TestParkedTaskIsNotInNeedsYouAndRaisesNoSignals(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	f.judge = `{"verdict":"waiting","reason":"waits for the design team"}`
	agentTurn(s, f, proto.StatusDone, "Blocked until the design team picks the icon set.", 0)
	waitFor(t, "the stop's signal", func() bool { return signalled(s, f, todo.ID, sigIdle, "Agent 停下了，但没做完") })
	forgetSignals(t, s)
	pushes := len(f.pushed())
	start := time.Now()
	args := waitingPark()
	args["until"] = "5d"
	got, err := reviewErr(s, todo.ID, args)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"is parked until", "It stays open", "supervision message (parked_due)", "parked 1 time(s) in a row"} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer lacks %q: %s", want, got)
		}
	}
	cur := todoNow(s, todo.ID)
	v := cur.Supervision
	if cur.DoneAt != nil || v.state(cur) != reviewParked || v.Parked == nil {
		t.Fatalf("todo = %+v, ledger = %+v", cur, v)
	}
	if p := v.Parked; p.Reason != "The design team has to pick the icon set first" || p.WaitingOn != parkSomeoneElse || p.Count != 1 ||
		p.Until.Before(start.Add(5*24*time.Hour)) || p.Until.After(time.Now().Add(5*24*time.Hour)) {
		t.Fatalf("parked = %+v", p)
	}
	// The to-do as the apps get it: supervision.state and supervision.parked, and no ask.
	listed := listedTodos(t, s)[todo.ID]
	sup, _ := listed["supervision"].(map[string]any)
	parked, _ := sup["parked"].(map[string]any)
	if sup["state"] != "parked" || parked["reason"] != "The design team has to pick the icon set first" || parked["waiting_on"] != "someone_else" ||
		parked["count"] != float64(1) || parked["until"] == nil {
		t.Fatalf("supervision = %v", sup)
	}
	if ask := listedAsk(t, s, todo.ID); ask != nil || askOf(s, todo.ID) != nil {
		t.Fatalf("a parked task asks the owner: %v", ask)
	}
	if len(f.pushed()) != pushes {
		t.Fatalf("a parked task pushed: %+v", f.pushed()[pushes:])
	}
	if tr := triageTodo(listedTodo{Todo: cur}, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section == triageWaiting {
		t.Fatalf("a parked task is filed as waiting on the owner: %+v", tr)
	}
	// Nothing while it is parked: not stalled (its agent shows working with nothing moving), not orphaned, not overdue
	// (its brief's clock ran out a day in), not idle (its agent's turn judged again), not undecidable.
	later := time.Now().Add(3 * 24 * time.Hour)
	stale := proto.Session{SessionID: "s1", MachineID: "box", State: proto.StateWorking, Status: proto.StatusWorking, UpdatedAt: time.Now().Add(-6 * time.Hour)}
	if sigs := watchSignals(cur, []proto.Session{stale}, later); len(sigs) != 0 {
		t.Fatalf("signals while parked: %+v", sigs)
	}
	if o, ok := taskOrphan(cur, []orphanAgent{{machine: "box", session: "s1", reachable: true}}, later); ok {
		t.Fatalf("orphaned while parked: %+v", o)
	}
	s.superviseWatch(ctx, later)
	s.superviseJudged(ctx, cur, time.Now(), taskDoneCheck{Verdict: verdictNotDone, Reason: "not finished"}, nil)
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("signals while parked: %v", got)
	}
	if b, ok := s.briefOfTodo(todo.ID); !ok || !later.After(b.DueAt) || b.Overdue != nil {
		t.Fatalf("the brief's clock: %+v, %v", b, ok)
	}
	// The same task, not parked, does raise them: the checks above are not empty.
	open := cur
	open.Supervision = v.clone()
	open.Supervision.State = ""
	if sigs := watchSignals(open, []proto.Session{stale}, later); len(sigs) != 1 || sigs[0].Kind != sigStalled {
		t.Fatalf("signals of the task when it is not parked: %+v", sigs)
	}
	if _, ok := taskOrphan(open, []orphanAgent{{machine: "box", session: "s1", reachable: true}}, later); !ok {
		t.Fatal("the task is not orphaned when it is not parked")
	}
	// The snapshot's task line says it is parked, until when and why.
	if h, _ := taskHealth(cur, nil, time.Now()); !strings.Contains(h, "已搁置到") || !strings.Contains(h, "等别人") ||
		!strings.Contains(h, "The design team has to pick the icon set first") {
		t.Fatalf("health = %q", h)
	}
	// A task that is done is not parked.
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "cancelled", "note": "no longer wanted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewErr(s, todo.ID, waitingPark()); err == nil || !strings.Contains(err.Error(), "is done already") {
		t.Fatalf("parked a closed task: %v", err)
	}
}

// Parking also silences a stop the turn ledger had not told yet. The replay must acknowledge that obsolete signal,
// not emit it on every later sweep; without parking, the same untold ledger entry is the control and still replays.
func TestParkedTaskDoesNotReplayAnUntoldTurnSignal(t *testing.T) {
	for _, parked := range []bool{false, true} {
		t.Run(map[bool]string{false: "unparked control", true: "parked before ledger replay"}[parked], func(t *testing.T) {
			s, _, todo := newSupervisedService(t)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			g := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "stop-before-park", Detail: "在等别人或别的事：旧决定"}
			markLeaderTurnSignalled(t, s, g)
			last := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle, UpdatedAt: time.Now().UTC()}
			s.tasks.turns.mu.Lock()
			s.tasks.turns.looked[turnRecoveryKey{Todo: todo.ID, Machine: last.MachineID, Session: last.SessionID}] = lookOf(last) // only the ledger replay is under test, not another transcript read
			s.tasks.turns.mu.Unlock()
			if parked {
				_, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
					v.State = reviewParked
					v.Reviews = []TaskReview{{Verdict: reviewParked, At: time.Now().UTC(), Until: time.Now().UTC().Add(time.Hour),
						WaitingOn: parkSomeoneElse, Reason: "The design team decides"}}
					return true
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			s.watchTurn(context.Background(), todoNow(s, todo.ID), []proto.Session{last})
			got := pendingKinds(s, todo.ID)
			e, ok := s.turnOf(context.Background(), todo.ID)
			if !ok || e.Signal == nil {
				t.Fatalf("the turn signal is missing: %+v", e)
			}
			if parked {
				if len(got) != 0 {
					t.Fatalf("the pre-park turn replayed: %v", got)
				}
				if e.Signal.Told == nil {
					t.Fatal("the obsolete turn signal was left for every later sweep")
				}
			} else {
				if !slices.Contains(got, sigIdle) || e.Signal.Told != nil {
					t.Fatalf("the untold control did not replay: signals=%v turn=%+v", got, e.Signal)
				}
			}
		})
	}
}

// At its time a parked task comes back to its supervisor once: not before, not a second time while the supervisor has
// time to act on it, not again after a restart once a message carried it, and not lost by a restart before one did.
func TestParkedDueComesOnceAtItsTimeAndAfterARestart(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	args := waitingPark()
	args["until"] = "2h"
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.superviseWatch(ctx, now.Add(time.Hour))
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("signals before its time: %v", got)
	}
	// Its time came, and the manager restarts before the supervision message goes out: the ledger still says it is
	// due and untold, so it is raised again.
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.last = time.Now() // a message just went: this one waits its turn, as after any other
	s.supervise.mu.Unlock()
	due := now.Add(3 * time.Hour)
	s.superviseWatch(ctx, due)
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkedDue}) {
		t.Fatalf("signals at its time: %v", got)
	}
	if r := todoNow(s, todo.ID).Supervision.parking(); r == nil || !r.DueAt.IsZero() {
		t.Fatalf("marked told before a message carried it: %+v", r)
	}
	forgetUntold(s)
	before := len(f.notices())
	s.superviseWatch(ctx, due.Add(time.Minute))
	waitFor(t, "the parked_due message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	for _, want := range []string{"搁置到期了", "The design team has to pick the icon set first", "等别人", "已经连续搁置 1 次", "剩余搁置次数 3",
		"signal=parked_due"} {
		if !strings.Contains(n, want) {
			t.Errorf("the message lacks %q:\n%s", want, n)
		}
	}
	waitFor(t, "the mark on the review", func() bool {
		r := todoNow(s, todo.ID).Supervision.parking()
		return r != nil && !r.DueAt.IsZero()
	})
	// Told: a manager that forgot what it told (a restart) does not tell it again while the supervisor has time to
	// act on it (an unanswered reminder is resent later: TestUnansweredParkedDueIsResentAndCapped).
	forgetSignals(t, s)
	told := todoNow(s, todo.ID).Supervision.parking().DueAt
	for _, d := range []time.Duration{time.Minute, 30 * time.Minute, parkedDueResendAfter - time.Minute} {
		s.superviseWatch(ctx, told.Add(d))
	}
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("told a second time: %v", got)
	}
	if got := len(f.notices()); got != before+1 {
		t.Fatalf("%d supervision messages, want one", got-before)
	}
	// Still parked (the supervisor has not acted), and parked again it is due again at its new time, once.
	if st := todoNow(s, todo.ID).Supervision.State; st != reviewParked {
		t.Fatalf("state = %q", st)
	}
	args = waitingPark()
	args["reason"], args["until"] = "The design team moved its review to next week", "1h"
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatal(err)
	}
	s.superviseWatch(ctx, time.Now().Add(30*time.Minute))
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("signals before the new time: %v", got)
	}
	s.superviseWatch(ctx, time.Now().Add(2*time.Hour))
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkedDue}) {
		t.Fatalf("signals at the new time: %v", got)
	}
}

// A parked_due the supervisor never acted on (its turn ended on a model API error, a 429, and wrote no task_review) is
// told again parkedDueResendAfter later, as a numbered resend, at most maxParkedDueResends times; a task_review that
// parks it again stops the resends of the old park (#14048).
func TestUnansweredParkedDueIsResentAndCapped(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	args := waitingPark()
	args["until"] = "2h"
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatal(err)
	}
	forgetUntold(s)
	before := len(f.notices())
	s.superviseWatch(ctx, time.Now().Add(3*time.Hour))
	waitFor(t, "the first parked_due message", func() bool { return len(f.notices()) > before })
	waitFor(t, "the mark on the review", func() bool { return !todoNow(s, todo.ID).Supervision.parking().DueAt.IsZero() })
	if n := f.notices()[before]; strings.Contains(n, "次重发") {
		t.Fatalf("the first reminder reads as a resend:\n%s", n)
	}
	for n := 1; n <= maxParkedDueResends; n++ {
		forgetSignals(t, s)
		told := func() time.Time { r := todoNow(s, todo.ID).Supervision.parking(); return laterTime(r.DueAt, r.Until) }()
		s.superviseWatch(ctx, told.Add(parkedDueResendAfter-time.Minute))
		if got := pendingKinds(s, todo.ID); len(got) != 0 {
			t.Fatalf("resend %d before its time: %v", n, got)
		}
		forgetUntold(s)
		s.supervise.mu.Lock()
		s.supervise.last = time.Time{} // the earlier reminder's watch ran at a later clock than this one
		s.supervise.mu.Unlock()
		before = len(f.notices())
		s.superviseWatch(ctx, told.Add(parkedDueResendAfter+time.Minute))
		waitFor(t, fmt.Sprintf("resend %d", n), func() bool { return len(f.notices()) > before })
		msg := f.notices()[before]
		for _, want := range []string{"signal=parked_due", fmt.Sprintf("第 %d 次重发", n), "The design team has to pick the icon set first"} {
			if !strings.Contains(msg, want) {
				t.Errorf("resend %d lacks %q:\n%s", n, want, msg)
			}
		}
		waitFor(t, fmt.Sprintf("resend %d marked", n), func() bool {
			r := todoNow(s, todo.ID).Supervision.parking()
			return r != nil && r.DueResends == n
		})
	}
	// The cap: no further reminder for this park, however long it stays unanswered.
	forgetSignals(t, s)
	told := func() time.Time { r := todoNow(s, todo.ID).Supervision.parking(); return laterTime(r.DueAt, r.Until) }()
	for _, d := range []time.Duration{2 * parkedDueResendAfter, 24 * time.Hour, 20 * 24 * time.Hour} {
		s.superviseWatch(ctx, told.Add(d))
	}
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("resent past the cap: %v", got)
	}
	// The supervisor parks it again: the new park starts with no reminder told, and the old one's resends are over.
	args = waitingPark()
	args["reason"], args["until"] = "The design team moved its review to next week", "1h"
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatal(err)
	}
	if r := todoNow(s, todo.ID).Supervision.parking(); r.DueResends != 0 || !r.DueAt.IsZero() {
		t.Fatalf("the new park inherited the old reminders: %+v", r)
	}
	s.superviseWatch(ctx, time.Now().Add(30*time.Minute))
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("signals before the new park's time: %v", got)
	}
}

// Parked again and again with no agent turn in between is the old habit under a new name: three re-parks are taken,
// the fourth is refused with the count; an agent turn starts the count again.
func TestFourthReparkIsRefused(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	for n := 1; n <= 1+maxReparks; n++ {
		args := waitingPark()
		args["reason"] = "Still waiting for the design team, round " + string(rune('0'+n))
		if _, err := reviewErr(s, todo.ID, args); err != nil {
			t.Fatalf("park %d: %v", n, err)
		}
		if p := todoNow(s, todo.ID).Supervision.Parked; p == nil || p.Count != n {
			t.Fatalf("park %d: parked = %+v", n, p)
		}
	}
	before := todoNow(s, todo.ID).Supervision
	_, err := reviewErr(s, todo.ID, waitingPark())
	if err == nil {
		t.Fatal("the fourth re-park was taken")
	}
	for _, want := range []string{"parked 4 times in a row (re-parked 3 times)", "its agent did not work in between", "Close it", "needs_user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if v := todoNow(s, todo.ID).Supervision; len(v.Reviews) != len(before.Reviews) || v.Parked.Count != 4 ||
		v.Parked.Reason != before.Parked.Reason {
		t.Fatalf("the refused park changed the ledger: %+v", v)
	}
	// Its agent works again: the task is parked no longer, and a park after that is the first of a new row.
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if _, err := reviewErr(s, todo.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	if p := todoNow(s, todo.ID).Supervision.Parked; p == nil || p.Count != 1 {
		t.Fatalf("parked after an agent turn = %+v", p)
	}
}

// A parked task whose agent starts a turn again is parked no longer and is supervised as before: the signals it was
// spared come back, and its time no longer brings it up.
func TestParkedTaskLeavesParkedWhenItsAgentWorksAgain(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	args := waitingPark()
	args["until"] = "2h"
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatal(err)
	}
	// Due and waiting to be told when its agent starts a turn: the signal is moot.
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.last = time.Now()
	s.supervise.mu.Unlock()
	s.superviseWatch(ctx, time.Now().Add(3*time.Hour))
	if got := pendingKinds(s, todo.ID); !slices.Equal(got, []string{sigParkedDue}) {
		t.Fatalf("signals at its time: %v", got)
	}
	if !s.parkedSession("box", "s1") {
		t.Fatal("the manager's own follow-ups are not held while it is parked")
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	cur := todoNow(s, todo.ID)
	if v := cur.Supervision; v.State != "" || v.Parked != nil || v.state(cur) != "" {
		t.Fatalf("still parked: %+v", v)
	}
	if sup, _ := listedTodos(t, s)[todo.ID]["supervision"].(map[string]any); sup["state"] != "" || sup["parked"] != nil {
		t.Fatalf("supervision = %v", sup)
	}
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigParkedDue) {
		t.Fatalf("its parked_due still waits: %v", got)
	}
	if s.parkedSession("box", "s1") {
		t.Fatal("follow-ups still held")
	}
	// Supervised normally again: a turn that stops short is told, and a finished one waits for review.
	f.judge = `{"verdict":"not_done","reason":"half way"}`
	agentTurn(s, f, proto.StatusDone, "Half of it is in.", 0)
	waitFor(t, "the stop's signal", func() bool { return signalled(s, f, todo.ID, sigIdle, "Agent 停下了，但没做完") })
	if sigs := watchSignals(todoNow(s, todo.ID), nil, time.Now().Add(30*24*time.Hour)); len(sigs) != 0 {
		t.Fatalf("its old time still brings it up: %+v", sigs)
	}
	waitsForReview(t, s, f, todo.ID, "All of it is in.")
}

// Only a task parked for the owner's priority call concerns the owner: the daily digest lists it with its reason, and
// it is still not in their Needs you.
func TestDigestListsATaskParkedForTheOwnersPriorityCall(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	ctx := context.Background()
	now := time.Now().Add(time.Minute)
	digest := func() []prompts.BriefTask {
		open, _ := s.briefDigest(ctx, now, now.Add(-time.Hour), time.UTC)
		return open
	}
	if open := digest(); len(open) != 0 {
		t.Fatalf("digest before = %+v", open)
	}
	if _, err := reviewErr(s, todo.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	if open := digest(); len(open) != 0 {
		t.Fatalf("a task parked for someone else is in the owner's digest: %+v", open)
	}
	seedLegacyPark(t, s, todo.ID, "Whether this goes ahead of the billing work", parkOwnerPriority, "7d")
	open := digest()
	if len(open) != 1 || open[0].Parked != "Whether this goes ahead of the billing work" || open[0].Decide || open[0].Need != "" {
		t.Fatalf("digest = %+v", open)
	}
	facts := prompts.RenderBriefFacts(prompts.BriefFacts{Card: true, Hours: 12, Briefs: open})
	if !strings.Contains(facts, "；搁置中，等你定优先级：Whether this goes ahead of the billing work") || strings.Contains(facts, "等用户拍板") {
		t.Fatalf("the facts do not say it:\n%s", facts)
	}
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("it asks the owner: %v", ask)
	}
	cur := todoNow(s, todo.ID)
	if tr := triageTodo(listedTodo{Todo: cur}, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section == triageWaiting {
		t.Fatalf("filed as waiting on the owner: %+v", tr)
	}
}

// The supervision message for a parked task whose time came, and the briefing's line for one parked for the owner's
// priority call, as they are read.
func TestGoldenParked(t *testing.T) {
	golden(t, "supervise.parked_due", prompts.RenderSupervise(prompts.Supervise{ParkedDue: true, Entries: sortEntries([]prompts.SuperviseEntry{
		{ID: "todo_1", Title: "Redraw the toolbar icons", Kind: sigParkedDue, DoneWhen: []string{"icons merged"},
			Parked: &prompts.SuperviseParked{Reason: "The design team has to pick the icon set first", WaitingOn: parkSomeoneElse,
				Until: "2026-11-14 Sat 09:00", Count: 1, Left: 3}},
		{ID: "todo_2", Title: "Move the reports to the new store", Kind: sigParkedDue,
			Parked: &prompts.SuperviseParked{Reason: "Whether this goes ahead of the billing work", WaitingOn: parkOwnerPriority,
				Until: "2026-11-12 Thu 18:00", Count: 4, Left: 0}},
	})}))
	golden(t, "routine_facts.parked", prompts.RenderBriefFacts(prompts.BriefFacts{Card: true, Hours: 12, Briefs: []prompts.BriefTask{
		{Date: "2026-11-05", Words: "Move the reports to the new store", Now: "The plan is written", Parked: "Whether this goes ahead of the billing work"},
	}}))
}
