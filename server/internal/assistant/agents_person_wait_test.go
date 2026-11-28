package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const personWaitCloseout = "The form can only be filled in by hand.\n<closeout status=\"blocked\">\n" +
	"unblock: other\nwaiting: someone fills in the web form and takes it live\n</closeout>"

// A turn that stopped on a person while it left background work running is read from its closeout, not taken for a
// wait that ends by itself: the background tasks it left are no word that it goes on (#14899).
func TestBackgroundDoesNotHideAPersonWait(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"external","line":"Waits for someone to fill in the form"}`
	c := stopped("s1", personWaitCloseout)
	c.Background = []proto.BackgroundTask{{ID: "b1", Kind: "shell"}, {ID: "b2", Kind: "shell"}}
	f.setSessions("box", c)
	triage(t, s, "s1")
	w := entryOf(t, s, "s1").Work
	if w.By != "rule" || w.Closeout == nil || w.Closeout.Unblock != unblockOther || w.On != proto.WaitExternal ||
		w.Wait != w.Closeout.Waiting || w.WakeAt != nil {
		t.Fatalf("a person wait with background work read as %+v", w)
	}
	c.Work = &w
	if !taskAgentSettled(c) {
		t.Fatal("its turn does not settle past the background work")
	}
	if n := len(triageCalls(f)); n != 0 {
		t.Fatalf("the model was asked %d times", n)
	}
}

// Background work stays what it was for a turn whose closeout waits on that work, or that has none.
func TestBackgroundStillWaitsOnItsOwnWork(t *testing.T) {
	for name, last := range map[string]string{
		"own work":    "Polling the job.\n<closeout status=\"not_done\">\nunblock: agent\nwaiting: the backfill job finishes\n</closeout>",
		"no unblock":  "Polling the job.\n<closeout status=\"not_done\">\nwaiting: the backfill job finishes\n</closeout>",
		"no closeout": "Polling the job.",
	} {
		t.Run(name, func(t *testing.T) {
			s, f := workService(t)
			f.triage = `{"phase":"blocked","on":"user","line":"should not be asked"}`
			c := stopped("s1", last)
			c.Background = []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}
			f.setSessions("box", c)
			triage(t, s, "s1")
			if w := entryOf(t, s, "s1").Work; w.By != "rule" || w.On != proto.WaitExternal {
				t.Fatalf("background wait read as %+v", w)
			}
			if len(f.triageAsked) != 0 {
				t.Fatalf("the model was asked %d times", len(f.triageAsked))
			}
		})
	}
}

// An owner wait comes directly from the closeout. A delivery hold reaches the owner through delivery (#14899).
func TestTriageCloseoutUsesStructuredOwnerWait(t *testing.T) {
	s, f := workService(t)
	setTriageAnswer(f, `{"phase":"blocked","on":"user","line":"Upgrade the driver?","questions":[{"question":"Please confirm: upgrade the sandbox driver now?"}]}`)
	const wait = "the owner agrees to upgrade the sandbox driver"
	f.setSessions("box", stopped("s1", "Please confirm: upgrade the sandbox driver now?\n<closeout status=\"done\">\ndelivery: none\nunblock: user\n"+
		"waiting: "+wait+"\n</closeout>"))
	triage(t, s, "s1")
	if w := entryOf(t, s, "s1").Work; w.By != "rule" || w.Phase != proto.WorkBlocked || w.On != proto.WaitUser {
		t.Fatalf("a done closeout asking the owner read as %+v", w)
	}
	cards := s.workCards()
	if len(cards) != 1 || len(cards[0].Questions) != 1 || cards[0].Questions[0].Question != wait {
		t.Fatalf("cards = %+v, want one with the closeout's wait", cards)
	}

	f.setSessions("box", stopped("s2", "Opened the PR.\n<closeout status=\"done\" merged=\"no\">\ndelivery: pr\nunblock: user\n"+
		"waiting: the owner merges the PR\n</closeout>"))
	triage(t, s, "s2")
	if w := entryOf(t, s, "s2").Work; w.Phase != proto.WorkDone || w.By != "rule" || len(w.Questions) != 0 {
		t.Fatalf("a delivery hold read as %+v", w)
	}
	if n := len(triageCalls(f)); n != 0 {
		t.Fatalf("closeouts triggered %d model calls", n)
	}
}

func TestCloseoutWaitsOnPerson(t *testing.T) {
	for _, c := range []struct {
		co   proto.Closeout
		want bool
	}{
		{proto.Closeout{Status: proto.CloseoutBlocked, Unblock: unblockOther, Waiting: "a reviewer"}, true},
		{proto.Closeout{Status: proto.CloseoutNotDone, Unblock: unblockUser}, true},
		{proto.Closeout{Status: proto.CloseoutDone, Unblock: unblockUser, Waiting: "the owner's OK", Delivery: deliveryNone}, true},
		{proto.Closeout{Status: proto.CloseoutDone, Unblock: unblockUser, Waiting: "the owner merges", Delivery: deliveryPR}, false},
		{proto.Closeout{Status: proto.CloseoutDone, Unblock: unblockUser}, false},
		{proto.Closeout{Status: proto.CloseoutDone, Unblock: unblockOther, Waiting: "deploy window"}, false},
		{proto.Closeout{Status: proto.CloseoutNotDone, Unblock: unblockAgent, Waiting: "the job"}, false},
		{proto.Closeout{Status: proto.CloseoutBlocked, Waiting: "the job"}, false},
	} {
		if got := closeoutWaitsOnPerson(c.co); got != c.want {
			t.Errorf("closeoutWaitsOnPerson(%+v) = %v, want %v", c.co, got, c.want)
		}
	}
}

// Background work keeps a turn from settling, unless the turn's own closeout, read for this very turn, says it stopped
// on a person (#14899).
func TestTaskAgentSettledPastBackgroundForAPersonWait(t *testing.T) {
	at := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	base := proto.Session{Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: at,
		Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}}
	if taskAgentSettled(base) {
		t.Fatal("background work alone settled the turn")
	}
	person := &proto.Closeout{Status: proto.CloseoutBlocked, Unblock: unblockOther, Waiting: "a reviewer"}
	c := base
	c.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, TurnAt: at, Closeout: person}
	if !taskAgentSettled(c) {
		t.Fatal("a turn stopped on a person did not settle past its background work")
	}
	c.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, TurnAt: at.Add(-time.Hour), Closeout: person}
	if taskAgentSettled(c) {
		t.Fatal("an earlier turn's closeout settled this one")
	}
	c.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, TurnAt: at,
		Closeout: &proto.Closeout{Status: proto.CloseoutNotDone, Unblock: unblockAgent, Waiting: "the job"}}
	if taskAgentSettled(c) {
		t.Fatal("a turn waiting on its own work settled")
	}
}

// An agent that goes on and ends another turn while its task waits on an open owner decision wakes the supervisor, as
// any other news during needs_user does (#13112): what it found may answer or replace the question. The decision
// stays as it was (#14899).
func TestAgentTurnDuringNeedsUserWakesSupervisor(t *testing.T) {
	s, f, todo, _, _ := setupNeedsUserTaskWithLark(t, nil)
	before := len(f.notices())
	beforeAsk := *askOf(s, todo.ID)
	f.judge = `{"verdict":"not_done","reason":"The web form still needs a person; a newer driver may let the agent fill it in."}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Tried both routes; the form still needs a person.", 0)
	defer func() {
		if t.Failed() {
			t.Logf("notices: %q", f.notices()[before:])
		}
	}()
	waitFor(t, "the supervisor hears of the turn", func() bool {
		for _, n := range f.notices()[before:] {
			if strings.Contains(n, "signal=needs_user_external_info") && strings.Contains(n, "agent turn box/s1") && strings.Contains(n, "The web form still needs a person") {
				return true
			}
		}
		return false
	})
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.State != reviewNeedsUser {
		t.Fatalf("the turn changed the task: done=%v state=%q", got.DoneAt, got.Supervision.State)
	}
	if a := askOf(s, todo.ID); a == nil || !a.AcceptedAt.IsZero() || a.Superseded || !a.AskedAt.Equal(beforeAsk.AskedAt) {
		t.Fatalf("the turn changed the open decision: %+v", a)
	}
	if _, listed := s.todoAsks(context.Background())[todo.ID]; !listed {
		t.Fatal("the open decision left Needs you")
	}
}

// The triage settles a task turn that stopped on a person while background work it left still runs: no other event
// settles it, for the session does not change again until that work ends (#14899).
func TestPersonWaitWithBackgroundGetsItsReceipt(t *testing.T) {
	s, f := newQuietService(t)
	s.work.switches = &TriageSwitches{Enabled: true, AutoContinue: true, Wake: true, Decide: true}
	f.judge = `{"verdict":"waiting","reason":"Someone must fill in the web form."}`
	f.triage = `{"phase":"blocked","on":"external","line":"Waits for someone to fill in the form"}`
	startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	c := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, LastText: personWaitCloseout, UpdatedAt: time.Now(),
		Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}}
	f.setSessions("box", c)
	s.taskTurnEnded(context.Background(), "box", "s1")
	if strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatal("background work did not hold the turn before its triage")
	}
	triage(t, s, "s1")
	waitFor(t, "the receipt", func() bool { return strings.Contains(feedKinds(s, "conv1"), "receipt") })
	if got := f.sessions["box"][0].Background; len(got) == 0 {
		t.Fatal("the background work ended under the test")
	}
}

// A turn judged needs_user while its task already waits on an owner decision is an owner wait (noteOwnerWait), not
// news for the open decision: no needs_user_external_info for it (#14899).
func TestNeedsUserTurnDuringNeedsUserIsNoExternalInfo(t *testing.T) {
	s, f, _, _, _ := setupNeedsUserTaskWithLark(t, nil)
	before := len(f.notices())
	f.judge = `{"verdict":"needs_user","reason":"Only the owner can approve the driver upgrade."}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Please approve the driver upgrade?", 0)
	waitFor(t, "the owner wait", func() bool {
		for _, n := range f.notices()[before:] {
			if strings.Contains(n, "Only the owner can approve") {
				return true
			}
		}
		return false
	})
	for _, n := range f.notices()[before:] {
		if strings.Contains(n, "agent turn box/s1") {
			t.Fatalf("a needs_user turn also went out as news for the open decision:\n%s", n)
		}
	}
}

// The turn that raised the open decision, judged again, is no news for it: only a turn after the decision's review
// wakes the supervisor (#14899).
func TestTurnThatRaisedTheDecisionIsNoNews(t *testing.T) {
	s, f, todo, _, _ := setupNeedsUserTaskWithLark(t, nil)
	before := len(f.notices())
	s.needsUserAgentTurn(context.Background(), todo, taskDoneCheck{Verdict: verdictNotDone, Reason: "The raising turn, judged again."}, nil)
	f.judge = `{"verdict":"not_done","reason":"A later turn found another route."}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Found another route.", 0)
	waitFor(t, "the later turn's news", func() bool {
		for _, n := range f.notices()[before:] {
			if strings.Contains(n, "A later turn found another route") {
				return true
			}
		}
		return false
	})
	for _, n := range f.notices()[before:] {
		if strings.Contains(n, "The raising turn") {
			t.Fatalf("the turn that raised the decision went out as news:\n%s", n)
		}
	}
}
