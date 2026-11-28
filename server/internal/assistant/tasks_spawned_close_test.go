package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Finished task_spawn sub-sessions on a still-open to-do must retire after a short quiet window
// instead of staying alive for the entire lifetime of the parent to-do (#13578).
func TestSpawnedSessionsRetireOnOpenTodoAfterDone(t *testing.T) {
	ctx := context.Background()
	s, f, todo := spawnFixture(t, "codex", "plan")

	const totalDone = 300
	past := time.Now().Add(-2 * time.Minute)
	leader := idleAgent(past)
	leader.SessionID = todo.Agents[0].SessionID
	leader.Agent = "codex"
	leader.PaneID = "w1:p0"

	sessions := []proto.Session{leader}
	doneIDs := make(map[string]bool, totalDone)
	for i := range totalDone {
		sid := fmt.Sprintf("spawn-done-%03d", i)
		doneIDs[sid] = true
		if err := s.linkTodoAgentWithSource(ctx, todo.ID, "box", sid, fmt.Sprintf("review %d", i), "/code/app", "codex", "", "spawned", "box/"+leader.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.reportToSupervisor(ctx, "box", sid, json.RawMessage(`{"kind":"done","text":"APPROVE"}`), past); err != nil {
			t.Fatalf("report done %s: %v", sid, err)
		}
		c := idleAgent(past)
		c.SessionID = sid
		c.Agent = "codex"
		c.PaneID = fmt.Sprintf("w1:p%d", i+1)
		sessions = append(sessions, c)
	}

	// Control sessions on the same open to-do that MUST stay alive.
	keepCases := []struct {
		sid      string
		report   bool
		editRow  func(*proto.Session)
		reportAt time.Time
	}{
		{sid: "spawn-working", report: true, reportAt: past, editRow: func(c *proto.Session) { c.Status = proto.StatusWorking; c.State = proto.StateWorking }},
		{sid: "spawn-question", report: true, reportAt: past, editRow: func(c *proto.Session) { c.PendingInteractions = 1; c.State = proto.StateNeedsYou }},
		{sid: "spawn-bg", report: true, reportAt: past, editRow: func(c *proto.Session) { c.Background = []proto.BackgroundTask{{}} }},
		{sid: "spawn-queued", report: true, reportAt: past, editRow: func(c *proto.Session) { c.Queued = []string{"follow-up"} }},
		{sid: "spawn-unreported", report: false, reportAt: past},
		{sid: "spawn-grace", report: true, reportAt: time.Now(), editRow: func(c *proto.Session) { c.UpdatedAt = time.Now() }},
	}
	keepIDs := map[string]bool{leader.SessionID: true}
	for idx, kc := range keepCases {
		keepIDs[kc.sid] = true
		if err := s.linkTodoAgentWithSource(ctx, todo.ID, "box", kc.sid, kc.sid, "/code/app", "codex", "", "spawned", "box/"+leader.SessionID); err != nil {
			t.Fatal(err)
		}
		if kc.report {
			if _, err := s.reportToSupervisor(ctx, "box", kc.sid, json.RawMessage(`{"kind":"done","text":"APPROVE"}`), kc.reportAt); err != nil {
				t.Fatalf("report done %s: %v", kc.sid, err)
			}
		}
		c := idleAgent(past)
		c.SessionID = kc.sid
		c.Agent = "codex"
		c.PaneID = fmt.Sprintf("w2:p%d", idx+1)
		if kc.editRow != nil {
			kc.editRow(&c)
		}
		sessions = append(sessions, c)
	}
	f.setSessions("box", sessions...)

	oldEvery := taskCloseEvery
	taskCloseEvery = 5 * time.Millisecond
	t.Cleanup(func() { taskCloseEvery = oldEvery })

	loopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.taskCloseLoop(loopCtx)
	}()

	waitFor(t, "300 finished spawned sessions retired", func() bool {
		return len(f.retires()) >= totalDone
	})
	cancel()
	<-done

	retired := map[string]bool{}
	for _, sid := range f.retires() {
		retired[sid] = true
	}
	for sid := range doneIDs {
		if !retired[sid] {
			t.Fatalf("finished spawned session %s was not retired (total retired=%d)", sid, len(retired))
		}
	}
	for sid := range keepIDs {
		if retired[sid] {
			t.Fatalf("protected session %s was wrongly retired", sid)
		}
	}
	// Shared to-do stays open and retains every spawned link so transcripts remain resumable.
	gotTodo, ok := s.todoNow(todo.ID)
	if !ok || gotTodo.DoneAt != nil {
		t.Fatalf("shared to-do closed unexpectedly: ok=%v todo=%+v", ok, gotTodo)
	}
	if len(gotTodo.Agents) != 1+totalDone+len(keepCases) {
		t.Fatalf("spawned links lost: got %d, want %d", len(gotTodo.Agents), 1+totalDone+len(keepCases))
	}
}

// Handoff guidance must tell leaders to reuse existing reviewer/executor sessions for follow-up rounds
// and rework, spawning a new session only for genuinely separate work (#13578).
func TestTaskHandoffPromptGuidesSessionReuse(t *testing.T) {
	for _, h := range []prompts.TaskHandoff{
		{MCP: true},
		{Executable: `"/opt/persona-node"`},
	} {
		out := prompts.RenderTaskHandoff(h)
		low := strings.ToLower(out)
		if !strings.Contains(low, "reuse") || !strings.Contains(low, "rework") {
			t.Fatalf("handoff prompt (%+v) lacks session reuse guidance:\n%s", h, out)
		}
	}
}

// Reusing a completed spawned session (via non-done report, user input, or resume) clears/cancels
// retirement, WorkDone also qualifies once quiet, and close_ended_terminals is honored (#13578).
func TestSpawnedSessionReuseWorkDoneAndTerminalSetting(t *testing.T) {
	ctx := context.Background()
	past := time.Now().Add(-2 * time.Minute)

	t.Run("reuse clears done mark and resume stays alive", func(t *testing.T) {
		s, f, todo := spawnFixture(t, "codex", "plan")
		leaderID := todo.Agents[0].SessionID
		for _, sid := range []string{"spawn-reused-report", "spawn-reused-user", "spawn-resumed"} {
			if err := s.linkTodoAgentWithSource(ctx, todo.ID, "box", sid, sid, "/code/app", "codex", "", "spawned", "box/"+leaderID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.reportToSupervisor(ctx, "box", sid, json.RawMessage(`{"kind":"done","text":"round 1 done"}`), past); err != nil {
				t.Fatal(err)
			}
		}
		// 1. Progress report clears done mark.
		if _, err := s.reportToSupervisor(ctx, "box", "spawn-reused-report", json.RawMessage(`{"kind":"progress","text":"round 2 rework"}`), past.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		// 2. Owner speaking clears done mark.
		s.UserSpoke(ctx, SessionRef{MachineID: "box", SessionID: "spawn-reused-user"})

		mk := func(sid string) proto.Session {
			c := idleAgent(past)
			c.SessionID = sid
			c.Agent = "codex"
			c.PaneID = "w1:" + sid
			return c
		}
		f.setSessions("box", mk("spawn-reused-report"), mk("spawn-reused-user"), mk("spawn-resumed"))

		refused := taskCloseRefused{}
		seen := doneSeen{}
		s.closeDoneSpawnedAgents(ctx, refused, seen)
		if got := f.retires(); len(got) != 1 || got[0] != "spawn-resumed" {
			t.Fatalf("first pass retired %v, want only [spawn-resumed]", got)
		}
		// If spawn-resumed is later brought back running without a new done report, it must not retire again.
		s.closeDoneSpawnedAgents(ctx, refused, seen)
		if got := f.retires(); len(got) != 1 {
			t.Fatalf("resumed spawned session was re-retired: %v", got)
		}
	})

	t.Run("work_done and close_ended_terminals", func(t *testing.T) {
		for _, closeTerm := range []bool{false, true} {
			s, f, todo := spawnFixture(t, "codex", "plan")
			closeOnEnd(t, s, closeTerm)
			leaderID := todo.Agents[0].SessionID
			if err := s.linkTodoAgentWithSource(ctx, todo.ID, "box", "spawn-work-done", "verify", "/code/app", "codex", "", "spawned", "box/"+leaderID); err != nil {
				t.Fatal(err)
			}
			c := idleAgent(past)
			c.SessionID = "spawn-work-done"
			c.Agent = "codex"
			c.Work = &proto.WorkState{Phase: proto.WorkDone, TurnAt: past}
			f.setSessions("box", c)
			s.closeDoneSpawnedAgents(ctx, taskCloseRefused{}, doneSeen{})
			calls := retireCalls(f)
			if len(calls) != 1 {
				t.Fatalf("closeTerm=%v: retire calls %v, want 1", closeTerm, calls)
			}
			if closeTerm != strings.Contains(calls[0], "close_terminal=true") {
				t.Fatalf("closeTerm=%v: call %q did not match setting", closeTerm, calls[0])
			}
		}
	})
}
