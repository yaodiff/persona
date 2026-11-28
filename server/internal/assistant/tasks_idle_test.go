package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Closing agents idle for a long time (tasks_idle.go).

func TestIdleCloseable(t *testing.T) {
	now := time.Now()
	idle := 12 * time.Hour
	since := now.Add(-13 * time.Hour)
	wake := now.Add(time.Hour)
	cases := []struct {
		name    string
		edit    func(*proto.Session)
		since   time.Time
		open    bool
		touched time.Time
		want    bool
	}{
		{"idle long enough", func(*proto.Session) {}, since, false, time.Time{}, true},
		{"seen only just now (resumed)", func(*proto.Session) {}, now.Add(-time.Minute), false, time.Time{}, false},
		{"active an hour ago", func(s *proto.Session) { s.UpdatedAt = now.Add(-time.Hour) }, since, false, time.Time{}, false},
		{"scheduled wake", func(s *proto.Session) { s.Work = &proto.WorkState{Phase: proto.WorkDone, WakeAt: &wake} }, since, false, time.Time{}, false},
		{"blocked on the user", func(s *proto.Session) { s.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser} }, since, false, time.Time{}, false},
		{"blocked on CI", func(s *proto.Session) { s.Work = &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal} }, since, false, time.Time{}, false},
		{"work done", func(s *proto.Session) { s.Work = &proto.WorkState{Phase: proto.WorkDone} }, since, false, time.Time{}, true},
		{"loop", func(s *proto.Session) { s.Loop = &proto.LoopInfo{} }, since, false, time.Time{}, false},
		{"background shell", func(s *proto.Session) { s.Background = []proto.BackgroundTask{{}} }, since, false, time.Time{}, false},
		{"waiting for the user", func(s *proto.Session) { s.State = proto.StateNeedsYou }, since, false, time.Time{}, false},
		{"open task, touched an hour ago", func(*proto.Session) {}, since, true, now.Add(-time.Hour), false},
		{"open task, stalled", func(*proto.Session) {}, since, true, now.Add(-40 * time.Hour), true},
		{"not started by Persona", func(s *proto.Session) { s.StartedByPersona = false }, since, false, time.Time{}, false},
	}
	for _, c := range cases {
		s := idleAgent(now.Add(-48 * time.Hour))
		c.edit(&s)
		if got := idleCloseable(s, c.since, now, idle, c.open, c.touched); got != c.want {
			t.Errorf("%s: idleCloseable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIdleCloseAfterSetting(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskService(t)
	if got := s.idleCloseAfter(ctx); got != 12*time.Hour {
		t.Fatalf("default = %v", got)
	}
	for _, c := range []struct {
		hours int
		want  time.Duration
	}{{0, 0}, {6, 6 * time.Hour}, {-1, 12 * time.Hour}} {
		h := c.hours
		setSettings(t, s, func(v *Settings) { v.IdleCloseHours = &h })
		if got := s.idleCloseAfter(ctx); got != c.want {
			t.Errorf("hours %d = %v, want %v", c.hours, got, c.want)
		}
	}
}

// An agent of no task, still for long enough, is closed; a task's open agent is not; a resumed one waits first; off
// means never.
func TestIdleAgentIsClosed(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo := startedTask(t, s, f) // its agent is s1 of an open task
	free := idleAgent(time.Now().Add(-48 * time.Hour))
	free.SessionID, free.PaneID = "free", "w2:p1"
	taskAgent := idleAgent(time.Now().Add(-48 * time.Hour))
	f.setSessions("box", taskAgent, free)
	refused, seen := taskCloseRefused{}, idleSeen{idlePrimed("box"): time.Now()}

	s.closeIdleAgents(ctx, refused, nil, seen) // first sight after the hub's first list: the clock starts now
	if got := f.retires(); len(got) != 0 {
		t.Fatalf("closed at first sight: %v", got)
	}
	if len(seen) != 3 {
		t.Fatalf("seen %v", seen)
	}
	for k := range seen {
		seen[k] = time.Now().Add(-13 * time.Hour)
	}
	s.closeIdleAgents(ctx, refused, nil, seen)
	if got := f.retires(); len(got) != 1 || got[0] != "free" {
		t.Fatalf("retired %v, want only the agent of no task", got)
	}
	if a := todoNow(s, todo.ID).Agents; len(a) != 1 || a[0].ReleasedAt != nil {
		t.Fatalf("the todo lost its agent: %+v", a)
	}

	// Closed (offline) it is forgotten; switched off nothing closes.
	off := 0
	setSettings(t, s, func(v *Settings) { v.IdleCloseHours = &off })
	seen[[2]string{"box", "free"}] = time.Now().Add(-100 * time.Hour)
	s.closeIdleAgents(ctx, refused, nil, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("closed while off: %v", got)
	}
}

// The idle clock lives in memory: after a hub restart the first list of a machine counts from each agent's own last
// activity, so an agent still for 13 hours is stopped at once; an agent that starts running after that (resumed) counts
// from when it was seen, however old its last activity.
func TestIdleClockSurvivesAHubRestart(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	still := idleAgent(time.Now().Add(-13 * time.Hour))
	still.SessionID, still.PaneID = "still", "w2:p1"
	f.setSessions("box", still)
	refused, seen := taskCloseRefused{}, idleSeen{} // a fresh hub

	s.closeIdleAgents(ctx, refused, nil, seen)
	if got := f.retires(); len(got) != 1 || got[0] != "still" {
		t.Fatalf("after a restart retired %v, want the agent still for 13h", got)
	}

	// Later a stopped agent is resumed: its last activity is as old, but it has only just started running.
	resumed := idleAgent(time.Now().Add(-13 * time.Hour))
	resumed.SessionID, resumed.PaneID = "resumed", "w3:p1"
	f.setSessions("box", resumed)
	s.closeIdleAgents(ctx, refused, nil, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("retired a just-resumed agent: %v", got)
	}
	if since := seen[[2]string{"box", "resumed"}]; time.Since(since) > time.Minute {
		t.Fatalf("resumed agent's clock starts at %v, want now", since)
	}
}
