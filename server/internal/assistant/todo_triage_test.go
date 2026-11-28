package assistant

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The clients' SOP cases (spec/fixtures/triage, web/src/lib/todo-triage-fixture.test.ts reads the same files): the hub's
// copy places every to-do in the same section by the same rule, merges the same rows and counts the same.

const triageFixtures = "../../../spec/fixtures/triage/"

type triageFixtureCase struct {
	Name  string `json:"name"`
	Input struct {
		Now          time.Time       `json:"now"`
		Sessions     []proto.Session `json:"sessions"`
		Todo         json.RawMessage `json:"todo"`
		Todos        json.RawMessage `json:"todos"`
		RestoredAt   time.Time       `json:"restored_at"`
		IncludeQuiet bool            `json:"include_quiet"`
	} `json:"input"`
	Expected struct {
		Section   string                `json:"section"`
		Rule      string                `json:"rule"`
		Last      time.Time             `json:"last"`
		Sections  map[string][][]string `json:"sections"`
		Open      int                   `json:"open"`
		Shelved   int                   `json:"shelved"`
		Finished  int                   `json:"finished"`
		Cancelled int                   `json:"cancelled"`
		Keys      []string              `json:"keys"`
	} `json:"expected"`
}

func readTriageCases(t *testing.T, file string) []triageFixtureCase {
	t.Helper()
	b, err := os.ReadFile(triageFixtures + file)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int                 `json:"version"`
		Cases   []triageFixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil || f.Version != 1 || len(f.Cases) == 0 {
		t.Fatalf("%s: %v (version %d, %d cases)", file, err, f.Version, len(f.Cases))
	}
	return f.Cases
}

func lookupOf(list []proto.Session) triageLookup {
	return func(machine, session string) (proto.Session, bool) {
		i := slices.IndexFunc(list, func(s proto.Session) bool { return s.MachineID == machine && s.SessionID == session })
		if i < 0 {
			return proto.Session{}, false
		}
		return list[i], true
	}
}

func TestTriageFollowsTheSharedCases(t *testing.T) {
	for _, c := range readTriageCases(t, "triage.json") {
		t.Run(c.Name, func(t *testing.T) {
			var td listedTodo
			if err := json.Unmarshal(c.Input.Todo, &td); err != nil {
				t.Fatal(err)
			}
			got := triageTodo(td, lookupOf(c.Input.Sessions), c.Input.Now, c.Input.RestoredAt, time.UTC, false)
			if got.Section != c.Expected.Section || got.Rule != c.Expected.Rule {
				t.Fatalf("got %s / %s, want %s / %s", got.Section, got.Rule, c.Expected.Section, c.Expected.Rule)
			}
			if !c.Expected.Last.IsZero() && !got.Last.Equal(c.Expected.Last) {
				t.Fatalf("last %v, want %v", got.Last, c.Expected.Last)
			}
		})
	}
}

func TestTriageTodosFollowsTheSharedCases(t *testing.T) {
	for _, c := range readTriageCases(t, "groups.json") {
		t.Run(c.Name, func(t *testing.T) {
			var list []listedTodo
			if err := json.Unmarshal(c.Input.Todos, &list); err != nil {
				t.Fatal(err)
			}
			r := triageTodos(list, lookupOf(c.Input.Sessions), c.Input.Now, time.UTC, c.Input.IncludeQuiet)
			for _, s := range triageSections {
				var got [][]string
				for _, g := range r.Sections[s] {
					var ids []string
					for _, m := range g.Members {
						ids = append(ids, m.ID)
					}
					got = append(got, ids)
				}
				want := c.Expected.Sections[s]
				if len(got) != len(want) || !slices.EqualFunc(got, want, slices.Equal) {
					t.Fatalf("section %s: got %v, want %v", s, got, want)
				}
			}
			if r.Open != c.Expected.Open || r.Shelved != c.Expected.Shelved || r.Finished != c.Expected.Finished || r.Cancelled != c.Expected.Cancelled {
				t.Fatalf("counts open/shelved/finished/cancelled %d/%d/%d/%d, want %d/%d/%d/%d", r.Open, r.Shelved, r.Finished, r.Cancelled,
					c.Expected.Open, c.Expected.Shelved, c.Expected.Finished, c.Expected.Cancelled)
			}
		})
	}
}

func TestTriageRelatedKeysFollowTheSharedCases(t *testing.T) {
	for _, c := range readTriageCases(t, "related.json") {
		t.Run(c.Name, func(t *testing.T) {
			var td listedTodo
			if err := json.Unmarshal(c.Input.Todo, &td); err != nil {
				t.Fatal(err)
			}
			if got := triageRelatedKeys(td.Todo); !slices.Equal(got, c.Expected.Keys) {
				t.Fatalf("got %v, want %v", got, c.Expected.Keys)
			}
		})
	}
}

func TestTriageDueByUsesUTCWithoutAUserZone(t *testing.T) {
	td := Todo{ID: "due", DueDate: "2026-11-10"}
	got, ok := triageDueBy(td, nil)
	if !ok || got.Location() != time.UTC || got.Format(time.RFC3339) != "2026-11-10T23:59:59Z" {
		t.Fatalf("nil zone: got %v (%v), ok=%v", got, got.Location(), ok)
	}
	zone := time.FixedZone("user", 8*60*60)
	got, ok = triageDueBy(td, zone)
	if !ok || got.Location() != zone || got.Format(time.RFC3339) != "2026-11-10T23:59:59+08:00" {
		t.Fatalf("user zone: got %v (%v), ok=%v", got, got.Location(), ok)
	}
}

// Dismissed agents are not the apps' (formatTodo leaves them out): one still asking does not make the to-do wait.
func TestTriageTransientReplacementWithTurnErrorOutranksOldInterruption(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	td := Todo{ID: "t", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Agents: []TodoAgent{
		{MachineID: "m", SessionID: "old"}, {MachineID: "m", SessionID: "replacement"},
	}}
	old := proto.Session{MachineID: "m", SessionID: "old", State: proto.StateIdle, Status: proto.StatusDone,
		TurnError: "Context unavailable", TurnErrorKind: proto.TurnErrorUnrecoverable}
	replacement := proto.Session{MachineID: "m", SessionID: "replacement", State: proto.StateIdle, Status: proto.StatusIdle,
		TurnError: "API Error: 503 Service Unavailable", TurnErrorKind: proto.TurnErrorTransient,
		Work: &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient}}
	staleInvalid := old
	staleInvalid.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient}
	invalidOnly := td
	invalidOnly.Agents = td.Agents[:1]
	if got := triageStatus(invalidOnly, lookupOf([]proto.Session{staleInvalid}), now); got != "interrupted" {
		t.Fatalf("permanent error with stale transient work: got %q, want interrupted", got)
	}
	if got := triageStatus(td, lookupOf([]proto.Session{old, replacement}), now); got != "waiting" {
		t.Fatalf("transient replacement with its own recoverable error: got %q, want waiting", got)
	}
}

func TestTriageLeavesDismissedAgentsOut(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	gone := now.Add(-time.Hour)
	td := listedTodo{Todo: Todo{ID: "t", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour), Origin: "c",
		Agents: []TodoAgent{{MachineID: "m", SessionID: "ask", DismissedAt: &gone}}}}
	look := lookupOf([]proto.Session{{MachineID: "m", SessionID: "ask", State: proto.StateNeedsYou, UpdatedAt: now}})
	if got := triageTodo(td, look, now, time.Time{}, time.UTC, false); got.Rule != ruleRest {
		t.Fatalf("got %+v", got)
	}
}

func TestExplicitParkOutranksStoppedExternalWait(t *testing.T) {
	now := time.Now()
	todo := listedTodo{Todo: Todo{ID: "example", Stage: "review",
		Agents:      []TodoAgent{{MachineID: "box", SessionID: "worker"}},
		Supervision: &Supervision{State: reviewParked}}}
	session := proto.Session{MachineID: "box", SessionID: "worker", State: proto.StateIdle,
		Work: &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal}}
	check := func(want, rule string) {
		t.Helper()
		got := triageTodo(todo, lookupOf([]proto.Session{session}), now, time.Time{}, time.UTC, false)
		if got.Section != want || got.Rule != rule {
			t.Fatalf("got %s/%s, want %s/%s", got.Section, got.Rule, want, rule)
		}
	}
	check(triagePR, ruleParked)
	session.Background = []proto.BackgroundTask{{Kind: "command"}}
	check(triageWorking, ruleAgentRunning)
	session.Background = nil
	session.Hierarchy = &proto.SessionHierarchy{Children: &proto.SessionChildren{Total: 1, Working: 1}}
	check(triageWorking, ruleAgentRunning)
	session.Hierarchy = nil
	session.Work.Phase = proto.WorkWorking
	check(triageWorking, ruleAgentRunning)
	session.Work.Phase = proto.WorkBlocked
	todo.Ask = &todoAsk{Outcome: outcomeDecide, Verdict: "Choose a release track", AskedAt: now}
	check(triageWaiting, ruleOwnerDecides)
	todo.Ask = nil
	session.State = proto.StateNeedsYou
	check(triageWaiting, ruleAgentAsks)
}

func TestTriageWaitingForMergeUsesFreshMatchingPRFacts(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	url := "https://example.com/repo/pull/42"
	facts := func() *TodoPRStatus {
		return &TodoPRStatus{URL: url, State: "open", Review: "approved", Checks: "success", CheckedAt: now}
	}
	base := listedTodo{Todo: Todo{ID: "pr", Title: "PR task", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), Origin: "main", PRURL: url}}
	withFacts := func() listedTodo {
		td := base
		td.PRStatus = facts()
		return td
	}
	assertNotPR := func(name string, td listedTodo, look triageLookup) {
		t.Helper()
		if got := triageTodo(td, look, now, time.Time{}, time.UTC, false); got.Rule == rulePROpen || got.Section == triagePR {
			t.Errorf("%s incorrectly waits for merge: %+v", name, got)
		}
	}

	// Current observations outweigh stage hints while the task is still open.
	open := withFacts()
	open.Stage = "deploy"
	if got := triageTodo(open, nil, now, time.Time{}, time.UTC, false); got.Section != triagePR || got.Rule != rulePROpen {
		t.Fatalf("fresh open PR facts lost to the stage: %+v", got)
	}
	// Acceptance closes a task; an unaccepted live delivery still awaits review, whatever its PR facts say.
	live := withFacts()
	live.Delivery = &TodoDelivery{State: deliveryLive, At: now}
	got := triageTodo(live, nil, now, time.Time{}, time.UTC, false)
	if got.Section != triageJustDone || got.Rule != ruleFinishedUnseen || !triageCountsAsOpen(live.Todo, got) {
		t.Errorf("unaccepted live task with a fresh open PR: %+v (counts as open: %v)", got, triageCountsAsOpen(live.Todo, got))
	}
	accepted := withFacts()
	accepted.Supervision = &Supervision{Supervisor: "main", State: reviewAccepted, Reviews: []TaskReview{{At: now.Add(-time.Minute), Verdict: reviewAccepted}}}
	got = triageTodo(accepted, nil, now, time.Time{}, time.UTC, false)
	if got.Section != triageDone || got.Rule != ruleAccepted || triageCountsAsOpen(accepted.Todo, got) {
		t.Errorf("accepted task with a fresh open PR: %+v (counts as open: %v)", got, triageCountsAsOpen(accepted.Todo, got))
	}

	stale := withFacts()
	stale.PRStatus.CheckedAt = now.Add(-16 * time.Minute)
	stale.Delivery = &TodoDelivery{State: deliveryPR}
	assertNotPR("stale", stale, nil)

	mismatch := withFacts()
	mismatch.PRStatus.URL = "https://example.com/repo/pull/43"
	assertNotPR("mismatched URL", mismatch, nil)

	draft := withFacts()
	draft.PRStatus.Draft = true
	assertNotPR("draft", draft, nil)

	for _, state := range []string{"merged", "closed"} {
		terminal := withFacts()
		terminal.PRStatus.State = state
		terminal.Delivery = &TodoDelivery{State: deliveryPR}
		assertNotPR(state, terminal, nil)
	}
	completed := withFacts()
	doneAt := now.Add(-time.Minute)
	completed.DoneAt = &doneAt
	assertNotPR("completed todo", completed, nil)

	for _, state := range []string{proto.StateWorking, proto.StateNeedsYou} {
		active := withFacts()
		active.Agents = []TodoAgent{{MachineID: "m", SessionID: state}}
		look := lookupOf([]proto.Session{{MachineID: "m", SessionID: state, State: state, Status: proto.StatusWorking, UpdatedAt: now}})
		assertNotPR("active "+state, active, look)
	}

	decision := withFacts()
	decision.Supervision = &Supervision{State: reviewNeedsUser}
	assertNotPR("owner review", decision, nil)
	decision = withFacts()
	decision.Ask = &todoAsk{Outcome: outcomeDecide}
	assertNotPR("owner ask", decision, nil)
}

// An open concern is never shelved (#3582): quiet for days it stays in Other open; while an agent of it runs it is in
// Working; a task the same age is shelved. Closed by its report, it is done like any other. Go only until the clients
// get the rule and the shared fixtures with it.
func TestTriageNeverShelvesAConcern(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-5 * 24 * time.Hour)
	concern := Todo{ID: "c", Kind: db.KindConcern, Origin: "main", CreatedAt: old, UpdatedAt: old}
	task := concern
	task.ID, task.Kind = "t", ""
	none := lookupOf(nil)
	if got := triageTodo(listedTodo{Todo: task}, none, now, time.Time{}, time.UTC, false); got.Section != triageShelved {
		t.Fatalf("a quiet task: %+v", got)
	}
	if got := triageTodo(listedTodo{Todo: concern}, none, now, time.Time{}, time.UTC, false); got.Section != triageOther || got.Rule != ruleRest {
		t.Fatalf("a quiet concern: %+v", got)
	}
	running := concern
	running.Agents = []TodoAgent{{MachineID: "m", SessionID: "s"}}
	look := lookupOf([]proto.Session{{MachineID: "m", SessionID: "s", State: proto.StateWorking, Status: proto.StatusWorking, UpdatedAt: now}})
	if got := triageTodo(listedTodo{Todo: running}, look, now, time.Time{}, time.UTC, false); got.Section != triageWorking {
		t.Fatalf("a concern with an agent working: %+v", got)
	}
	res := triageTodos([]listedTodo{{Todo: concern}, {Todo: task}}, none, now, time.UTC, false)
	if res.Shelved != 1 || res.Open != 1 {
		t.Fatalf("counts: open %d shelved %d", res.Open, res.Shelved)
	}
	// A failed start long ago, an accepted review or a live delivery file a task with the finished ones, never a concern.
	failed := concern
	failed.StartError = "no launcher"
	if got := triageTodo(listedTodo{Todo: failed}, none, now, time.Time{}, time.UTC, false); got.Section != triageOther {
		t.Fatalf("a concern whose agent failed to start days ago: %+v", got)
	}
	accepted := concern
	accepted.Supervision = &Supervision{State: reviewAccepted, Reviews: []TaskReview{{Verdict: reviewAccepted, At: old}}}
	live := concern
	live.Delivery = &TodoDelivery{State: deliveryLive, At: old}
	for name, c := range map[string]Todo{"accepted": accepted, "deployed": live} {
		if got := triageTodo(listedTodo{Todo: c}, none, now, time.Time{}, time.UTC, false); got.Section != triageOther {
			t.Fatalf("a %s concern: %+v", name, got)
		}
	}
	reported := concern
	at := now.Add(-time.Minute)
	reported.DoneAt = &at
	if got := triageTodo(listedTodo{Todo: reported}, none, now, time.Time{}, time.UTC, false); got.Section != triageJustDone {
		t.Fatalf("a reported concern: %+v", got)
	}
}

func TestPRFactsCannotReopenCompletedTasks(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	const pr = "https://github.com/nwverse/persona/pull/123"
	for _, tc := range []struct {
		name, review, delivery, kind string
		closed                       bool
		rule                         string
		open                         bool
	}{
		{"accepted", reviewAccepted, deliveryPR, "", false, ruleAccepted, false},
		{"deployed", "", deliveryLive, "", false, ruleFinishedUnseen, true},
		{"closed", "", deliveryPR, "", true, ruleClosed, false},
		{"unfinished PR", "", deliveryPR, "", false, rulePROpen, true},
		{"accepted concern", reviewAccepted, deliveryPR, db.KindConcern, false, "", true},
		{"deployed concern", "", deliveryLive, db.KindConcern, false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := Todo{ID: "pr-task", Kind: tc.kind, Origin: "main", CreatedAt: old, UpdatedAt: old,
				PRURL: pr, Stage: "review",
				PRStatus: &TodoPRStatus{URL: pr, State: "open", CheckedAt: now},
				Delivery: &TodoDelivery{State: tc.delivery, At: old}}
			if tc.review != "" {
				td.Supervision = &Supervision{State: tc.review, Reviews: []TaskReview{{Verdict: tc.review, At: old}}}
			}
			if tc.closed {
				td.DoneAt = &old
			}
			look := lookupOf(nil)
			got := triageTodo(listedTodo{Todo: td}, look, now, time.Time{}, time.UTC, false)
			if tc.rule != "" && got.Rule != tc.rule || triageCountsAsOpen(td, got) != tc.open {
				t.Fatalf("fresh open PR facts changed completion: triage=%+v, open=%v; want rule=%s open=%v",
					got, triageCountsAsOpen(td, got), tc.rule, tc.open)
			}
			list := triageTodos([]listedTodo{{Todo: td}}, look, now, time.UTC, false)
			if (list.Open == 1) != tc.open {
				t.Fatalf("header open count=%d, want open=%v", list.Open, tc.open)
			}
		})
	}
}
