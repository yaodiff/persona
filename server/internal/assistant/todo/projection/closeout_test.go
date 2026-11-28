package projection

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func closeoutFacts(status string) Facts {
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	work := &proto.WorkState{TurnAt: at}
	if status != "" {
		work.Closeout = &proto.Closeout{Status: status, Waiting: "the release owner"}
	}
	return Facts{ID: "task", Title: "Check the release", Origin: "main", CreatedAt: at, UpdatedAt: at,
		Agents:      []Agent{{MachineID: "box", SessionID: "worker", LinkedAt: &at, Title: "Release check"}},
		Supervision: &Supervision{State: reviewPending, UpdatedAt: at},
		Sessions: []proto.Session{{MachineID: "box", SessionID: "worker", Title: "Release check",
			State: proto.StateUnread, Status: proto.StatusIdle, StateSince: at, UpdatedAt: at, Work: work}}}
}

func TestCloseoutPartitionAndExactFollowupWindow(t *testing.T) {
	for _, status := range []string{proto.CloseoutNotDone, proto.CloseoutBlocked, ""} {
		t.Run(status, func(t *testing.T) {
			f := closeoutFacts(status)
			if status == "" {
				f.Sessions[0].State = proto.StateIdle
			}
			cutoff := f.CreatedAt.Add(time.Hour)
			v := View(f, cutoff)
			if v.Phase != assistantwire.TodoPhaseTodo || !v.NextChangeAt.Equal(cutoff.Add(time.Nanosecond)) {
				t.Fatalf("exactly one hour is still the follow-up window: %+v", v)
			}
			v = View(f, cutoff.Add(time.Nanosecond))
			if v.Phase != assistantwire.TodoPhaseNeedsYou || v.Section != assistantwire.TodoListSectionNeedsYou ||
				v.Reason != assistantwire.TodoReasonStoppedUnfinished || !v.Since.Equal(f.CreatedAt) {
				t.Fatalf("unfinished stop needs follow-up, never acceptance: %+v", v)
			}
		})
	}
}

func TestOnlyRealSupervisorFollowupResetsProjectionClock(t *testing.T) {
	f := closeoutFacts(proto.CloseoutNotDone)
	now := f.CreatedAt.Add(2 * time.Hour)
	recent := now.Add(-30 * time.Minute)
	f.Supervision.UpdatedAt = now
	f.Supervision.Reviews = []Review{{At: recent, Verdict: "rework", By: "reviewer"}}
	f.Sessions[0].State = proto.StateIdle
	f.Sessions[0].StateSince, f.Sessions[0].UpdatedAt = recent, recent
	if v := View(f, now); v.Reason != assistantwire.TodoReasonStoppedUnfinished || !v.Since.Equal(f.CreatedAt) {
		t.Fatalf("automatic writes and read receipts cannot reset turn_at: %+v", v)
	}
	f.Supervision.FollowedUpAt = &recent
	if v := View(f, now); v.Phase != assistantwire.TodoPhaseTodo {
		t.Fatalf("confirmed supervisor send starts a new follow-up window: %+v", v)
	}
	f.Supervision.FollowedUpAt = nil
	f.Supervision.Reviews[0].By = ""
	if v := View(f, now); v.Phase != assistantwire.TodoPhaseTodo {
		t.Fatalf("explicit supervisor review starts a new follow-up window: %+v", v)
	}
}

func TestCloseoutFollowupKeepsRunAskRisk(t *testing.T) {
	f := closeoutFacts(proto.CloseoutNotDone)
	now := f.CreatedAt.Add(2 * time.Hour)
	f.Agents = append(f.Agents, Agent{MachineID: "box", SessionID: "choice", Title: "Release choice"})
	f.Sessions = append(f.Sessions, proto.Session{
		MachineID: "box", SessionID: "choice", State: proto.StateNeedsYou, StateSince: now,
	})
	f.Interactions = []proto.Interaction{{
		ID: "release-choice", MachineID: "box", SessionID: "choice", Kind: proto.IxQuestion, CreatedAt: now,
		Questions: []proto.Question{{Question: "Which target?",
			Options: []proto.QuestionOption{{Label: "Preview"}, {Label: "Stable"}}}},
	}}
	v := View(f, now)
	if v.Reason != assistantwire.TodoReasonAgentAsks || len(v.Asks) != 1 {
		t.Fatalf("a real run question takes precedence over an unfinished stop: %+v", v)
	}
	if ask := v.Asks[0]; ask.Interaction != "release-choice" || ask.Risk != assistantwire.OwnerAskRiskSafe ||
		ask.Lockscreen != assistantwire.OwnerAskLockscreenAnswer {
		t.Fatalf("the run question keeps the upstream card and risk: %+v", ask)
	}

	f.Agents, f.Sessions, f.Interactions = f.Agents[:1], f.Sessions[:1], nil
	v = View(f, now)
	if v.Reason != assistantwire.TodoReasonStoppedUnfinished || v.Section != assistantwire.TodoListSectionNeedsYou ||
		len(v.Asks) != 0 {
		t.Fatalf("without the question, the unfinished stop still needs follow-up: %+v", v)
	}
}

func TestStoppedProjectionDoesNotOverrideExecutionParkOrAcceptance(t *testing.T) {
	for _, kind := range []string{"running", "starting", "background", "another run", "parked", "accepted", "closed"} {
		t.Run(kind, func(t *testing.T) {
			f := closeoutFacts(proto.CloseoutBlocked)
			now := f.CreatedAt.Add(2 * time.Hour)
			switch kind {
			case "running":
				f.Sessions[0].State, f.Sessions[0].Work.Phase = proto.StateWorking, proto.WorkWorking
			case "starting":
				f.Sessions[0].Starting = true
			case "background":
				f.Sessions[0].BackgroundWorking = true
			case "another run":
				f.Agents = append(f.Agents, Agent{MachineID: "box", SessionID: "other", Title: "Other run"})
				f.Sessions = append(f.Sessions, proto.Session{MachineID: "box", SessionID: "other", State: proto.StateWorking})
			case "parked":
				f.Supervision.State = reviewParked
				f.Supervision.Parked = &Parked{Reason: "Waiting for the release owner", Until: now.Add(24 * time.Hour)}
			case "accepted":
				f.Supervision.State = reviewAccepted
				f.Supervision.Reviews = []Review{{At: now, Verdict: reviewAccepted}}
			case "closed":
				f.DoneAt = &now
			}
			v := View(f, now)
			if v.Reason == assistantwire.TodoReasonStoppedUnfinished {
				t.Fatalf("%s is not unattended unfinished work: %+v", kind, v)
			}
			if kind == "parked" && v.Section != assistantwire.TodoListSectionWaiting {
				t.Fatalf("explicit park stays Waiting: %+v", v)
			}
			if kind == "closed" && v.Phase != assistantwire.TodoPhaseDone {
				t.Fatalf("a closeout cannot reopen a closed to-do: %+v", v)
			}
		})
	}
}

func TestReportedDoneStaysReadyAfterReadWithoutAcceptingTodo(t *testing.T) {
	f := closeoutFacts(proto.CloseoutDone)
	now := f.CreatedAt.Add(2 * time.Hour)
	seen := now.Add(-30 * time.Minute)
	f.SeenAt = &seen
	f.Sessions[0].State = proto.StateIdle
	f.Sessions[0].StateSince, f.Sessions[0].UpdatedAt = now, now
	v := View(f, now)
	if v.Phase != assistantwire.TodoPhaseReady || v.Section != assistantwire.TodoListSectionJustFinished ||
		v.Reason != assistantwire.TodoReasonFinishedRecent || !v.Seen || !v.Since.Equal(f.CreatedAt) {
		t.Fatalf("read status and acceptance are separate: %+v", v)
	}
	if f.DoneAt != nil || f.Supervision.State != reviewPending {
		t.Fatal("projection mutated acceptance")
	}
	v = View(f, seen.Add(24*time.Hour))
	if v.Phase != assistantwire.TodoPhaseTodo || v.Reason != assistantwire.TodoReasonFinishedSeen || !v.Seen {
		t.Fatalf("the decided seen window files ready work in To do, never Done: %+v", v)
	}
}

func TestCloseoutCountsUseTheDecidedPartition(t *testing.T) {
	stopped, done, parked := closeoutFacts(proto.CloseoutNotDone), closeoutFacts(proto.CloseoutDone), closeoutFacts(proto.CloseoutBlocked)
	done.ID, parked.ID = "reported", "parked"
	now := stopped.CreatedAt.Add(2 * time.Hour)
	parked.Supervision.State = reviewParked
	parked.Supervision.Parked = &Parked{Reason: "Waiting for review", Until: now.Add(24 * time.Hour)}
	list := []Facts{stopped, done, parked}
	c := Counts(list, Views(list, now))
	if c.NeedsYou != 1 || c.InProgress != 1 || c.Open != 3 ||
		c.Sections[assistantwire.TodoListSectionJustFinished] != 1 {
		t.Fatalf("Needs you=1, Waiting=1, uncounted Just finished=1, open=3: %+v", c)
	}
}

func TestCloseoutDoesNotOverrideTheDecidedRunWaits(t *testing.T) {
	for _, on := range []string{proto.WaitAgent, proto.WaitExternal} {
		t.Run(on, func(t *testing.T) {
			f := closeoutFacts(proto.CloseoutBlocked)
			f.Sessions[0].State = proto.StateIdle
			f.Sessions[0].Work.Phase = proto.WorkBlocked
			f.Sessions[0].Work.On = on
			f.Sessions[0].Work.Wait = "the release finishes"
			v := View(f, f.CreatedAt.Add(2*time.Hour))
			if v.Phase != assistantwire.TodoPhaseWaiting || v.Reason != assistantwire.TodoReasonSomeoneElse {
				t.Fatalf("a named wait is Waiting even with a pending ledger and an old closeout: %+v", v)
			}
		})
	}
}
