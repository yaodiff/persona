package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// turnPage is what the node pages as an agent's latest turn: the prompt, what the agent did since and, once the
// runtime closed the turn, its end as the transcript reader maps it (Codex's task_complete, transcript/codex.go).
// An open turn ends on a tool call still running.
func turnPage(turn, reply string, closed bool) []proto.Item {
	at := time.Now().UTC()
	items := []proto.Item{{ID: turn + "-ask#0", Kind: proto.KindText, Role: "user", Text: "iPad 横屏时登录页崩溃，修好并补测试", TS: at}}
	if reply != "" {
		items = append(items, proto.Item{ID: turn + "-reply#0", Kind: proto.KindText, Role: "assistant", Text: reply, TS: at})
	}
	if !closed {
		return append(items, proto.Item{ID: turn + "-tool#0", Kind: proto.KindToolUse, Role: "assistant", ToolName: "shell", ToolUseID: turn + "-call", TS: at})
	}
	return append(items, proto.Item{ID: turn + "-end#0", Kind: "turn_duration", Role: "system", TS: at,
		TurnDuration: &proto.TurnDuration{DurationMS: 1200, Source: "codex_task_complete", TurnID: turn}})
}

// turnRow is the task's agent s1 on box as its node lists it: status, the transcript's size rev and its last words.
func turnRow(status string, rev int64, text string) proto.Session {
	state := proto.StateUnread
	switch status {
	case proto.StatusWorking:
		state = proto.StateWorking
	case proto.StatusIdle:
		state = proto.StateIdle
	case proto.StatusUnknown:
		state = proto.StateUnknown
	}
	return proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "codex", Status: status, State: state,
		LastText: text, Rev: rev, UpdatedAt: time.Now()}
}

// nodeListed has box's node list row, and the manager hear that list (setSessions): all the manager is told of a session
// whose runtime sends no hook.
func nodeListed(s *Service, f *fakeHub, row proto.Session) {
	f.setSessions("box", row)
	s.setSessions("box", []proto.Session{row})
}

// turnCounts are what completion handling did for a task: the receipts in its conversation, the done judge's calls
// to the model, and the supervision messages put into the main conversation.
type turnCounts struct{ receipts, judgments, messages int }

func countTurns(t *testing.T, s *Service, todo string, hubs ...*fakeHub) (n turnCounts) {
	t.Helper()
	n.receipts = len(recoveryReceipts(t, s, todo))
	for _, f := range hubs {
		f.mu.Lock()
		n.judgments += f.judged
		f.mu.Unlock()
		n.messages += len(f.notices())
	}
	return n
}

// wantTurns waits until completion handling did want, and fails with what it did when it does not get there.
func wantTurns(t *testing.T, what string, want turnCounts, s *Service, todo string, hubs ...*fakeHub) {
	t.Helper()
	var got turnCounts
	waitFor(t, what, func() bool {
		got = countTurns(t, s, todo, hubs...)
		return got == want
	})
	if got != want {
		t.Fatalf("%s: receipts=%d judgments=%d supervision messages=%d, want %d, %d, %d", what, got.receipts, got.judgments, got.messages,
			want.receipts, want.judgments, want.messages)
	}
}

// stillTurns gives late duplicates the time to show (several supervision messages' spacing), then checks the counts.
// The quiet window is the same length as before; a ticker drives it so the wait is not a fixed time.Sleep.
func stillTurns(t *testing.T, what string, want turnCounts, s *Service, todo string, hubs ...*fakeHub) {
	t.Helper()
	quiet := time.NewTicker(4 * shortSuperviseTimings.every)
	defer quiet.Stop()
	<-quiet.C
	if got := countTurns(t, s, todo, hubs...); got != want {
		t.Fatalf("%s: receipts=%d judgments=%d supervision messages=%d, want %d, %d, %d", what, got.receipts, got.judgments, got.messages,
			want.receipts, want.judgments, want.messages)
	}
}

var oneEach = turnCounts{1, 1, 1}

// workMark is when the manager last took a start of todo's work (zero: none).
func workMark(s *Service, todo string) time.Time {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return s.tasks.working[todo]
}

// startTaken waits until the manager took a start of todo's work after the one at before.
func startTaken(t *testing.T, s *Service, todo string, before time.Time) {
	t.Helper()
	waitFor(t, "the start to be taken", func() bool { return workMark(s, todo).After(before) })
}

// sweeps runs the supervision sweep n times, each after the looks of the one before finished.
func sweeps(t *testing.T, s *Service, todo string, n int) {
	t.Helper()
	for range n {
		s.superviseWatch(context.Background(), time.Now())
		turnsQuiet(t, s, todo)
	}
}

// itemReadsCounted counts the session.items reads from here on.
func itemReadsCounted(f *fakeHub) *atomic.Int32 {
	var n atomic.Int32
	f.hookReads(receiptHooks{before: func(int) { n.Add(1) }})
	return &n
}

// shortTurnRetries has the sweep's looks that could not finish tried again at once, and puts the spacing back.
func shortTurnRetries(t *testing.T, base time.Duration) {
	t.Helper()
	old := turnRetryBase
	turnRetryBase = base
	t.Cleanup(func() { turnRetryBase = old })
}

// restartedOn is the manager started again on database st: newQuietServiceOn without setting the package's timings a
// second time. The test's first manager is not stopped the way a process is, and what it still runs reads them.
func restartedOn(t *testing.T, st Store) (*Service, *fakeHub) {
	t.Helper()
	s, f := newTaskServiceOn(t, st)
	shortSupervise(t, s)
	f.hello.Assistant.MachineID = "server"
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	return s, f
}

// turnsQuiet waits until manager s is in the middle of nothing for task todo: no turn end being settled or judged, no
// sweep's look going. A restart is taken from there, so that the two managers of a test never work at once.
func turnsQuiet(t *testing.T, s *Service, todo string) {
	t.Helper()
	waitFor(t, "the manager to be done with the turn", func() bool {
		b := &s.tasks.turns
		b.mu.Lock()
		defer b.mu.Unlock()
		return !b.settling(todo) && !b.recovering[todo]
	})
}

// A supervised task whose agent's turn opens and completes while its node never lists it as working (a Codex
// inventory that says idle, done or unknown throughout, #5718) still gets its receipt, its judgment and the
// supervision message, once each: the sweep finds the turn the runtime closed.
func TestCompletionWithoutWorking(t *testing.T) {
	for _, status := range []string{proto.StatusIdle, proto.StatusDone, proto.StatusUnknown} {
		t.Run(status, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			nodeListed(s, f, turnRow(status, 1, ""))
			// The turn opens: the transcript grows, the status does not say so.
			f.setItems(turnPage("turn-1", "", false)...)
			nodeListed(s, f, turnRow(status, 2, ""))
			s.superviseWatch(ctx, time.Now())
			stillTurns(t, "while the turn is open", turnCounts{}, s, todo.ID, f)
			// The runtime closes it.
			f.setItems(turnPage("turn-1", "修好了，PR https://github.com/o/r/pull/901 已合并", true)...)
			row := turnRow(status, 3, "修好了，PR https://github.com/o/r/pull/901 已合并")
			nodeListed(s, f, row)
			s.superviseWatch(ctx, time.Now())
			wantTurns(t, "the turn completed with no working seen", oneEach, s, todo.ID, f)
			if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Supervision.State != reviewPending {
				t.Fatalf("not waiting for review: %+v %+v", now, now.Supervision)
			}
			// The same list and later sweeps are no second completion.
			for range 3 {
				nodeListed(s, f, row)
				s.superviseWatch(ctx, time.Now())
			}
			stillTurns(t, "after later sweeps", oneEach, s, todo.ID, f)
		})
	}
}

// The usual path (working, then stopped) and the sweep both see the same completed turn: whichever comes first
// handles it, and the other adds nothing.
func TestCompletionSeenByBothTriggers(t *testing.T) {
	for _, first := range []string{"the list's edge", "the sweep"} {
		t.Run(first, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
			s.superviseWatch(ctx, time.Now()) // the manager's sweep has run: the turns' ledger is kept from here on
			f.setItems(turnPage("turn-1", "", false)...)
			before := workMark(s, todo.ID)
			nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
			startTaken(t, s, todo.ID, before) // a list's start is taken after setSessions returns; the turn ends after that
			f.setItems(turnPage("turn-1", "修好了", true)...)
			done := turnRow(proto.StatusDone, 3, "修好了")
			if first == "the sweep" {
				f.setSessions("box", done) // the node lists it stopped; that list has not reached the manager yet
				s.superviseWatch(ctx, time.Now())
				wantTurns(t, "the sweep", oneEach, s, todo.ID, f)
				s.setSessions("box", []proto.Session{done}) // and now it has: working → done
			} else {
				nodeListed(s, f, done)
				wantTurns(t, "the list's edge", oneEach, s, todo.ID, f)
				s.superviseWatch(ctx, time.Now())
			}
			s.superviseWatch(ctx, time.Now())
			stillTurns(t, "after both", oneEach, s, todo.ID, f)
			if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Supervision.State != reviewPending || now.Check == nil || now.Check.Verdict != verdictDone {
				t.Fatalf("not waiting for review with its check: %+v %+v %+v", now, now.Supervision, now.Check)
			}
		})
	}
}

// A completion told again changes nothing: the hub's turn end twice, the settled look again, the same list again, and
// a stale working row that comes after the turn's end and goes back to done (reordered lists). The task still waits
// for the one review, with the judgment on it.
func TestCompletionDuplicateEvents(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
	f.setItems(turnPage("turn-1", "修好了", true)...)
	done := turnRow(proto.StatusDone, 3, "修好了")
	nodeListed(s, f, done)
	wantTurns(t, "the turn", oneEach, s, todo.ID, f)

	s.TurnEnded(ctx, ref)
	s.TurnEnded(ctx, ref)
	s.taskTurnEnded(ctx, "box", "s1")
	nodeListed(s, f, done)
	s.superviseWatch(ctx, time.Now())
	stillTurns(t, "after the same end again", oneEach, s, todo.ID, f)

	// A list from before the end arrives late, then the current one again: the transcript has no turn past turn-1.
	stale := turnRow(proto.StatusWorking, 3, "")
	nodeListed(s, f, stale)
	nodeListed(s, f, done)
	s.superviseWatch(ctx, time.Now())
	stillTurns(t, "after a stale working row", oneEach, s, todo.ID, f)
	waitFor(t, "the review to wait again", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Check == nil || now.Check.Verdict != verdictDone {
		t.Fatalf("the judgment was lost: %+v %+v", now, now.Check)
	}
	stillTurns(t, "after the review waits again", oneEach, s, todo.ID, f)
}

// A second turn in the same session is its own completion: here it ran while the manager heard nothing from the
// node (disconnected), and the list it hears afterwards says done, as the one before did.
func TestCompletionWhileDisconnected(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	f.judge = `{"verdict":"not_done","reason":"测试还没补"}`
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
	f.setItems(turnPage("turn-1", "修了崩溃，测试还没补", true)...)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修了崩溃，测试还没补"))
	wantTurns(t, "the first turn", oneEach, s, todo.ID, f)

	// Disconnected: the agent was sent more, worked and stopped. The manager heard none of it.
	f.judge = `{"verdict":"done","reason":"测试补上了"}`
	f.setItems(turnPage("turn-2", "测试补上了，PR https://github.com/o/r/pull/901 已合并", true)...)
	nodeListed(s, f, turnRow(proto.StatusDone, 5, "测试补上了，PR https://github.com/o/r/pull/901 已合并")) // connected again: done, as before
	s.superviseWatch(ctx, time.Now())
	wantTurns(t, "the turn that ran unheard", turnCounts{2, 2, 2}, s, todo.ID, f)
	if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Supervision.State != reviewPending {
		t.Fatalf("not waiting for review: %+v %+v", now, now.Supervision)
	}
	s.superviseWatch(ctx, time.Now())
	stillTurns(t, "after later sweeps", turnCounts{2, 2, 2}, s, todo.ID, f)
}

// The manager is started again on the same database at each point of a completion: before it heard of it at all,
// after the receipt, after the judgment, and after the supervision message. Each time the turn ends up with one
// receipt, one judgment applied and one message, whichever manager wrote them.
func TestCompletionAcrossRestart(t *testing.T) {
	for _, at := range []string{"before the receipt", "before the judgment", "before the message", "after the message"} {
		t.Run(at, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, f, todo := newSupervisedServiceOn(t, database)
			ctx := context.Background()
			nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
			s.superviseWatch(ctx, time.Now()) // the first manager's sweep has run: the turns' ledger is kept from here on
			done := turnRow(proto.StatusDone, 3, "修好了")
			page := turnPage("turn-1", "修好了", true)
			switch at {
			case "before the receipt":
				nodeListed(s, f, turnRow(proto.StatusWorking, 2, "")) // heard to start; it stops while the manager is down
			case "before the judgment":
				f.hookReads(receiptHooks{receipt: func(FeedItem) {
					f.mu.Lock()
					f.sessionsFail["box"] = 1000 // the receipt is out; this manager never gets to judge (its link is gone, then it is)
					f.mu.Unlock()
				}})
				nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
				f.setItems(page...)
				nodeListed(s, f, done)
				wantTurns(t, "the first manager's receipt", turnCounts{receipts: 1}, s, todo.ID, f)
			case "before the message":
				s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour} // judged; the message never leaves
				nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
				f.setItems(page...)
				nodeListed(s, f, done)
				wantTurns(t, "the first manager's judgment", turnCounts{receipts: 1, judgments: 1}, s, todo.ID, f)
				waitFor(t, "the review to wait", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			default:
				nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
				f.setItems(page...)
				nodeListed(s, f, done)
				wantTurns(t, "the first manager", oneEach, s, todo.ID, f)
			}

			turnsQuiet(t, s, todo.ID)
			again, f2 := restartedOn(t, database) // the manager after a restart: memory gone, the database kept
			f2.judge = `{"verdict":"done","reason":"PR 已合并"}`
			f2.setItems(page...)
			if err := again.ensureTodosLoaded(ctx); err != nil {
				t.Fatal(err)
			}
			nodeListed(again, f2, done) // its first list: nothing to compare it with
			again.superviseWatch(ctx, time.Now())
			wantTurns(t, "after the restart", oneEach, again, todo.ID, f, f2)
			// What a restarted manager may hear again: the hub's turn end, the list, the sweep.
			again.TurnEnded(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
			nodeListed(again, f2, done)
			again.superviseWatch(ctx, time.Now())
			stillTurns(t, "after the restart, told again", oneEach, again, todo.ID, f, f2)
			if now := todoNow(again, todo.ID); now.DoneAt != nil || now.Supervision.State != reviewPending {
				t.Fatalf("not waiting for review: %+v %+v", now, now.Supervision)
			}
		})
	}
}

// Nothing is recovered for a task nobody supervises, nor for a closed one: a turn that completes unseen there gets no
// receipt, no judgment and no message, and a closed task stays closed.
func TestCompletionUnseenNotSupervisedOrClosed(t *testing.T) {
	t.Run("not supervised", func(t *testing.T) {
		s, f := newTaskService(t) // no main conversation: the task it starts has no ledger
		shortSupervise(t, s)
		f.judge = `{"verdict":"done","reason":"修好了"}`
		todo := startedTask(t, s, f)
		if todo.Supervision != nil {
			t.Fatalf("supervised: %+v", todo.Supervision)
		}
		ctx := context.Background()
		nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
		f.setItems(turnPage("turn-1", "修好了", true)...)
		nodeListed(s, f, turnRow(proto.StatusIdle, 3, "修好了"))
		s.superviseWatch(ctx, time.Now())
		stillTurns(t, "a task nobody supervises", turnCounts{}, s, todo.ID, f)
		if now := todoNow(s, todo.ID); now.DoneAt != nil {
			t.Fatalf("closed: %+v", now)
		}
	})
	t.Run("closed", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		ctx := context.Background()
		nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
		nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
		f.setItems(turnPage("turn-1", "修好了", true)...)
		nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
		wantTurns(t, "the turn", oneEach, s, todo.ID, f)
		raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "看过了"})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
			t.Fatal(err)
		}
		closed := todoNow(s, todo.ID)
		if closed.DoneAt == nil {
			t.Fatalf("not closed: %+v", closed)
		}
		// Its agent is sent something by hand afterwards and answers; no list says it worked.
		f.setItems(turnPage("turn-2", "不客气", true)...)
		nodeListed(s, f, turnRow(proto.StatusDone, 5, "不客气"))
		s.superviseWatch(ctx, time.Now())
		stillTurns(t, "a closed task", oneEach, s, todo.ID, f)
		if now := todoNow(s, todo.ID); now.DoneAt == nil || !now.DoneAt.Equal(*closed.DoneAt) {
			t.Fatalf("reopened: %+v", now)
		}
		// Opened again by hand: it is judged by what its agent does after that, not by the turn that ended before.
		if err := s.reopenTodo(ctx, todo.ID); err != nil {
			t.Fatal(err)
		}
		before := todoNow(s, todo.ID).Supervision.State
		s.superviseWatch(ctx, time.Now())
		s.superviseWatch(ctx, time.Now())
		stillTurns(t, "a task opened again", oneEach, s, todo.ID, f)
		if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Supervision.State != before {
			t.Fatalf("after the reopen: %+v %+v (was %q)", now, now.Supervision, before)
		}
	})
}

// What the sweep's look costs: a transcript read only for a supervised task whose agents look stopped, at most
// turnRecoverMost going at a time, one for each task until its row moves again, and none for a task whose agent
// works. So many stopped sessions cost nothing by themselves: only open supervised tasks are looked at, once each.
func TestTurnSweepReadsAreBounded(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	tasks := 2*turnRecoverMost + 1
	var rows []proto.Session
	for i := range tasks + 1 {
		sid := fmt.Sprintf("agent%02d", i)
		todo, err := s.createTodo(ctx, "任务 "+sid, "做 "+sid, "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
		if err := s.linkTodoAgent(ctx, todo.ID, "box", sid, "任务", "/code/app", "codex"); err != nil {
			t.Fatal(err)
		}
		row := proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "codex", Status: proto.StatusIdle,
			State: proto.StateIdle, Rev: 7, UpdatedAt: time.Now()}
		if i == tasks { // one more whose agent is listed as working: never read
			row.Status, row.State = proto.StatusWorking, proto.StateWorking
		}
		rows = append(rows, row)
	}
	for i := range 20 { // and stopped sessions of no task at all
		rows = append(rows, proto.Session{SessionID: fmt.Sprintf("other%02d", i), MachineID: "box", Cwd: "/code/app", Agent: "codex",
			Status: proto.StatusDone, State: proto.StateUnread, Rev: 3, UpdatedAt: time.Now()})
	}
	f.setSessions("box", rows...)
	f.setItems(turnPage("turn-1", "", false)...) // every transcript: a turn still open
	var reads, going, most atomic.Int32
	release := make(chan struct{})
	f.hookReads(receiptHooks{before: func(int) {
		reads.Add(1)
		if n := going.Add(1); n > most.Load() {
			most.Store(n)
		}
		<-release
	}, after: func(int) { going.Add(-1) }})
	s.superviseWatch(ctx, time.Now())
	waitFor(t, "the first reads", func() bool { return int(going.Load()) == turnRecoverMost })
	s.superviseWatch(ctx, time.Now()) // a tick while they are still going starts no more
	quiet := time.NewTicker(50 * time.Millisecond)
	defer quiet.Stop()
	<-quiet.C // a tick this soon starts no further reads while the first batch is held
	if got := int(reads.Load()); got != turnRecoverMost {
		t.Fatalf("%d reads going at a time, want at most %d", got, turnRecoverMost)
	}
	close(release)
	waitFor(t, "a read for each task", func() bool {
		s.superviseWatch(ctx, time.Now())
		return int(reads.Load()) == tasks && going.Load() == 0
	})
	for range 3 {
		s.superviseWatch(ctx, time.Now())
	}
	<-quiet.C // rows that did not move schedule no further reads
	if got := int(reads.Load()); got != tasks || int(most.Load()) > turnRecoverMost {
		t.Fatalf("%d reads for %d tasks whose rows did not move (at most %d at a time, want %d)", got, tasks, most.Load(), turnRecoverMost)
	}
	rows[0].Rev, rows[0].UpdatedAt = 8, time.Now() // one transcript grew
	f.setSessions("box", rows...)
	waitFor(t, "the read of the row that moved", func() bool {
		s.superviseWatch(ctx, time.Now())
		return int(reads.Load()) == tasks+1
	})
	s.superviseWatch(ctx, time.Now())
	<-quiet.C // the row that did not move again schedules nothing
	if got := int(reads.Load()); got != tasks+1 {
		t.Fatalf("%d reads after one row moved, want %d", got, tasks+1)
	}
}

// The stretch's receipt is reserved before the agents are asked once more whether they stopped. A node that does not
// answer that one time (a dropped link) must not leave the reservation behind: the turn still gets its receipt and
// its judgment once the node answers again, when its end is told again and, when nothing tells of it again, by the
// look the manager takes by itself a little later.
func TestTaskReceiptReservationIsReleased(t *testing.T) {
	for _, how := range []string{"told again", "by itself"} {
		t.Run(how, func(t *testing.T) {
			reachA, reachB, reachM, retry := agentReachAttempts, agentReachBackoff, agentReachMax, receiptRetryAfter
			agentReachAttempts, agentReachBackoff, agentReachMax, receiptRetryAfter = 2, time.Millisecond, 200*time.Millisecond, time.Hour
			if how == "by itself" {
				receiptRetryAfter = 30 * time.Millisecond
			}
			t.Cleanup(func() {
				agentReachAttempts, agentReachBackoff, agentReachMax, receiptRetryAfter = reachA, reachB, reachM, retry
			})
			s, f := newTaskService(t) // no main conversation: the task is not supervised, so no sweep looks at it
			f.judge = `{"verdict":"done","reason":"修好了"}`
			todo := startedTask(t, s, f)
			if todo.Supervision != nil {
				t.Fatalf("supervised: %+v", todo.Supervision)
			}
			ctx := context.Background()
			agentTurn(s, f, proto.StatusWorking, "", 0)
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
				State: proto.StateUnread, LastText: "修好了", UpdatedAt: time.Now()})
			f.mu.Lock()
			f.onDevicesList = func() { // the node stops answering between the first look and the second
				f.mu.Lock()
				f.sessionsFail["box"] = agentReachAttempts
				f.mu.Unlock()
			}
			f.mu.Unlock()
			s.taskTurnEnded(ctx, "box", "s1")
			f.mu.Lock()
			fails, hook := f.sessionsFail["box"], f.onDevicesList
			f.mu.Unlock()
			if hook != nil || fails != 0 {
				t.Fatalf("the second look did not meet the dropped link: hook left %v, %d failures left", hook != nil, fails)
			}
			if how == "told again" {
				if got := len(recoveryReceipts(t, s, todo.ID)); got != 0 {
					t.Fatalf("%d receipts while the node did not answer", got)
				}
				s.taskTurnEnded(ctx, "box", "s1") // the node answers again, and the same turn end is told again
			}
			waitFor(t, "the receipt", func() bool { return len(recoveryReceipts(t, s, todo.ID)) == 1 })
			waitFor(t, "the task to close", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
			quiet := time.NewTicker(100 * time.Millisecond)
			defer quiet.Stop()
			<-quiet.C // a second receipt or judgment would show by now
			f.mu.Lock()
			judged := f.judged
			f.mu.Unlock()
			if got := len(recoveryReceipts(t, s, todo.ID)); got != 1 || judged != 1 {
				t.Fatalf("%d receipts, judged %d times", got, judged)
			}
		})
	}
}

// A turn is closed where the runtime says so, and is named by that end; whatever starts or continues a turn after it
// leaves no closed turn to go by.
func TestTurnEndOf(t *testing.T) {
	ask := proto.Item{ID: "u1#0", Kind: proto.KindText, Role: "user", Text: "修登录崩溃"}
	reply := proto.Item{ID: "a1#0", Kind: proto.KindText, Role: "assistant", Text: "修好了"}
	codexEnd := proto.Item{ID: "line9#0", Kind: "turn_duration", Role: "system", TurnDuration: &proto.TurnDuration{DurationMS: 5, Source: "codex_task_complete", TurnID: "turn-7"}}
	claudeEnd := proto.Item{ID: "d1#0", Kind: "turn_duration", Role: "system", TurnDuration: &proto.TurnDuration{DurationMS: 5, Source: "claude", ParentUUID: "a1"}}
	stopped := proto.Item{ID: "n1#0", Kind: proto.KindNotice, Role: "system", Text: userStoppedNotice}
	for _, c := range []struct {
		name  string
		items []proto.Item
		key   string
	}{
		{"nothing read", nil, ""},
		{"open: the reply is last", []proto.Item{ask, reply}, ""},
		{"open: a tool runs", []proto.Item{ask, reply, {ID: "t1#0", Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "c"}}, ""},
		{"Codex task_complete: its turn id", []proto.Item{ask, reply, codexEnd}, "turn-7"},
		{"Claude Code turn_duration: its item", []proto.Item{ask, reply, claudeEnd}, "d1#0"},
		{"stopped by the user", []proto.Item{ask, stopped}, "n1#0"},
		{"an end with nothing to name it", []proto.Item{ask, reply, {Kind: "turn_duration", Role: "system"}}, ""},
		{"a compaction notice after the end", []proto.Item{ask, reply, codexEnd, {ID: "n2#0", Kind: proto.KindNotice, Role: "system", Text: "compacted", Compact: &proto.Compact{}}}, "turn-7"},
		{"a queued message started the next turn", []proto.Item{ask, reply, codexEnd, {ID: "u2#0", Kind: proto.KindText, Role: "user", Text: "再看看深色", Queued: true}}, ""},
		{"a wake started the next turn", []proto.Item{ask, reply, claudeEnd, {ID: "w1#0", Kind: proto.KindNotice, Role: "system", Wake: true}}, ""},
		{"a background task's notice started the next turn", []proto.Item{ask, reply, claudeEnd, {ID: "k1#0", Kind: proto.KindNotice, Role: "system", Task: &proto.TaskNote{Kind: "shell"}}}, ""},
		{"the next turn ended too", []proto.Item{ask, reply, codexEnd, {ID: "u2#0", Kind: proto.KindText, Role: "user", Text: "再看看深色", Queued: true},
			{ID: "line20#0", Kind: "turn_duration", Role: "system", TurnDuration: &proto.TurnDuration{DurationMS: 5, Source: "codex_task_complete", TurnID: "turn-8"}}}, "turn-8"},
	} {
		end, closed := turnEndOf(c.items)
		if closed != (c.key != "") || end.Key != c.key {
			t.Errorf("%s: closed %v with key %q, want %q", c.name, closed, end.Key, c.key)
		}
	}
	if a, b := receiptID("todo1", taskTurn{Machine: "box", Session: "s1", Key: "turn-7"}), receiptID("todo1", taskTurn{Machine: "box", Session: "s1", Key: "turn-8"}); a == b ||
		a != receiptID("todo1", taskTurn{Machine: "box", Session: "s1", Key: "turn-7", Rev: 9}) || receiptID("todo1", taskTurn{Machine: "box", Session: "s1"}) != "" {
		t.Errorf("receipt ids: %q %q", a, b)
	}
}

// A cancelled task that is still open is left as it is when its agent ends a turn unheard: no receipt, judgment or
// message, no start taken for it (its stage and its supervision stay), and its transcript is read once, not on every
// tick. (Todo.Cancelled: the cancellation mark. No path sets it on an open todo today; the end's refusal is there.)
func TestCompletionUnheardOnACancelledOpenTask(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	at := time.Now()
	changeTodo(s, todo.ID, func(x *Todo) { x.CancelledAt = &at })
	before := todoNow(s, todo.ID)
	if !before.Cancelled() || before.DoneAt != nil {
		t.Fatalf("not cancelled and open: %+v", before)
	}
	mark := workMark(s, todo.ID)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	reads := itemReadsCounted(f)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
	sweeps(t, s, todo.ID, 5)
	stillTurns(t, "a cancelled open task", turnCounts{}, s, todo.ID, f)
	now := todoNow(s, todo.ID)
	if got := reads.Load(); got != 1 {
		t.Fatalf("%d transcript reads over 5 ticks for a row that did not move, want 1", got)
	}
	if !workMark(s, todo.ID).Equal(mark) || now.Stage != before.Stage || now.Supervision.State != before.Supervision.State || !now.Cancelled() || now.DoneAt != nil {
		t.Fatalf("the task was changed for a turn whose end is refused: start %v → %v, stage %q → %q, %+v", mark, workMark(s, todo.ID), before.Stage, now.Stage, now.Supervision)
	}
	if e, ok := s.turnOf(context.Background(), todo.ID); !ok || e.State != turnBefore || e.Key != "turn-1" {
		t.Fatalf("the ledger's turn = %+v, want turn-1 kept as seen", e)
	}
	// The row moves with the same turn (its node lists it again): it is known as seen, nothing more is made of it.
	nodeListed(s, f, turnRow(proto.StatusDone, 4, "修好了"))
	sweeps(t, s, todo.ID, 3)
	stillTurns(t, "a cancelled open task, its row moved", turnCounts{}, s, todo.ID, f)
	if got := reads.Load(); got != 2 {
		t.Fatalf("%d transcript reads after the row moved once, want 2", got)
	}
}

// A task its review cancelled that is open again (the user reopened it, its agent was sent more) is no cancelled
// task (Supervision.state): a turn that ends unheard there is taken up once, like any other, and its row is not read
// tick after tick.
func TestCompletionUnheardOnATaskCancelledByReviewAndOpenAgain(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Supervision.State = reviewCancelled
	s.todos.mu.Unlock()
	if now := todoNow(s, todo.ID); now.Cancelled() || now.DoneAt != nil {
		t.Fatalf("an open task whose ledger says cancelled reads as cancelled: %+v", now.Supervision)
	}
	f.setItems(turnPage("turn-1", "修好了", true)...)
	reads := itemReadsCounted(f)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
	sweeps(t, s, todo.ID, 1)
	waitFor(t, "the turn's receipt and judgment", func() bool {
		n := countTurns(t, s, todo.ID, f)
		return n.receipts == 1 && n.judgments == 1
	})
	wantTurns(t, "cancelled by its review, open again", oneEach, s, todo.ID, f) // done: it waits for review again (#5892)
	turnsQuiet(t, s, todo.ID)
	handled := reads.Load()
	sweeps(t, s, todo.ID, 5)
	stillTurns(t, "cancelled by its review, open again: later ticks", oneEach, s, todo.ID, f)
	if got := reads.Load(); got != handled {
		t.Fatalf("%d transcript reads over 5 more ticks of a row that did not move, want none (%d handled the turn)", got-handled, handled)
	}
}

// While a usage limit is waited out (the CLI goes on after the reset) the turn has not ended: the sweep reads nothing
// and takes no start, tick after tick. Once the wait is over with nothing gone on, the turn is taken up once.
func TestCompletionUnheardWhileAUsageLimitIsWaitedOut(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	before, mark := todoNow(s, todo.ID), workMark(s, todo.ID)
	limited := func(reset time.Time) proto.Session {
		row := turnRow(proto.StatusDone, 3, limitText)
		row.TurnError, row.TurnErrorKind, row.TurnErrorResetsAt, row.TurnErrorResumes = limitText, proto.TurnErrorUsageLimit, &reset, true
		return row
	}
	f.setItems(turnPage("turn-1", limitText, true)...)
	reads := itemReadsCounted(f)
	nodeListed(s, f, limited(time.Now().Add(time.Hour)))
	sweeps(t, s, todo.ID, 5)
	stillTurns(t, "a usage limit waited out", turnCounts{}, s, todo.ID, f)
	now := todoNow(s, todo.ID)
	if got := reads.Load(); got != 0 {
		t.Fatalf("%d transcript reads while the limit is waited out, want none", got)
	}
	if !workMark(s, todo.ID).Equal(mark) || now.Stage != before.Stage || now.Supervision.State != before.Supervision.State {
		t.Fatalf("the task was changed while its turn waits for the reset: start %v → %v, stage %q → %q, %+v", mark, workMark(s, todo.ID), before.Stage, now.Stage, now.Supervision)
	}
	// The reset passed long ago and nothing went on: the same row is now a turn that ended, cut off by the limit.
	f.setSessions("box", limited(time.Now().Add(-limitResumeGrace-time.Minute)))
	sweeps(t, s, todo.ID, 4)
	waitFor(t, "the interrupted receipt", func() bool { return len(recoveryReceipts(t, s, todo.ID)) == 1 })
	sweeps(t, s, todo.ID, 3)
	quiet := time.NewTicker(4 * shortSuperviseTimings.every)
	defer quiet.Stop()
	<-quiet.C // late ticks add no second receipt or judgment
	receipts := recoveryReceipts(t, s, todo.ID)
	if len(receipts) != 1 || receipts[0].State != "interrupted" || f.judged != 0 {
		t.Fatalf("after the wait: %d receipts %+v, %d judgments, want one interrupted receipt and no judgment", len(receipts), receipts, f.judged)
	}
	if got := reads.Load(); got < 1 || got > 2 {
		t.Fatalf("%d transcript reads to take the turn up, want the sweep's and the end's own", got)
	}
}

// A node that lists the session but does not answer the transcript read is not asked on every tick: the next tick,
// then ever later while the row stays as it is. A row that moves is looked at on the next tick again.
func TestTurnSweepSpacesLooksThatCannotFinish(t *testing.T) {
	shortTurnRetries(t, time.Hour)
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	reads, mark := itemReadsCounted(f), workMark(s, todo.ID)
	f.mu.Lock()
	f.itemsFail = 1000
	f.mu.Unlock()
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
	sweeps(t, s, todo.ID, 8)
	if got := reads.Load(); got != 2 {
		t.Fatalf("%d reads over 8 ticks of a node that does not answer, want 2 (the first, the next tick, then an hour's wait)", got)
	}
	nodeListed(s, f, turnRow(proto.StatusDone, 4, "修好了")) // the row moved
	sweeps(t, s, todo.ID, 4)
	if got := reads.Load(); got != 4 {
		t.Fatalf("%d reads after the row moved, want 4 (two more: at once, the next tick)", got)
	}
	stillTurns(t, "a node that does not answer", turnCounts{}, s, todo.ID, f)
	if now := workMark(s, todo.ID); !now.Equal(mark) {
		t.Fatalf("a start was taken for a turn that could not be read: %v → %v", mark, now)
	}
	f.mu.Lock()
	f.itemsFail = 0
	f.mu.Unlock()
	nodeListed(s, f, turnRow(proto.StatusDone, 5, "修好了")) // it answers again, and its row moved
	sweeps(t, s, todo.ID, 1)
	wantTurns(t, "once the node answers", oneEach, s, todo.ID, f)
}

// A turn the edge handled before its transcript named it — the runtime had not written its end yet, or the read
// failed — is that same turn when it is found named later, by the sweep (its row moved with the end record) or by
// the end being told again at the same row after a stale start: no second receipt, judgment or message, and its
// judgment and review wait stand.
func TestCompletionUnnamedThenNamed(t *testing.T) {
	const laterRecord, readFailed, sameRow = "the end record came later", "the read failed", "the read failed, told again at the same row"
	for _, how := range []string{laterRecord, readFailed, sameRow} {
		t.Run(how, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
			sweeps(t, s, todo.ID, 1)
			closed := turnPage("turn-1", "修好了", true)
			f.setItems(closed[:len(closed)-1]...) // the prompt and the reply; the runtime's end record is not written yet
			if how != laterRecord {
				f.mu.Lock()
				f.itemsFail = 1 // the end's one read of the transcript
				f.mu.Unlock()
			}
			before := workMark(s, todo.ID)
			nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
			startTaken(t, s, todo.ID, before)
			done := turnRow(proto.StatusDone, 3, "修好了")
			nodeListed(s, f, done)
			wantTurns(t, "the turn, with no name yet", oneEach, s, todo.ID, f)
			waitFor(t, "the review to wait", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			turnsQuiet(t, s, todo.ID)
			if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Key != "" || e.State != turnDone || (e.Open == "") != (how != laterRecord) {
				t.Fatalf("the ledger's turn = %+v, want one with no name", e)
			}
			mark, judged := workMark(s, todo.ID), todoNow(s, todo.ID)
			f.setItems(closed...) // the end record is in the transcript now
			if how == sameRow {
				// Nothing more was written. A list from before the end arrives late, then the current one again.
				nodeListed(s, f, turnRow(proto.StatusWorking, 3, ""))
				startTaken(t, s, todo.ID, mark)
				nodeListed(s, f, done)
				waitFor(t, "the turn to get its name", func() bool { e, _ := s.turnOf(ctx, todo.ID); return e.Key == "turn-1" })
				waitFor(t, "the review to wait again", func() bool {
					now := todoNow(s, todo.ID)
					return now.Check != nil && now.Supervision.State == reviewPending
				})
				turnsQuiet(t, s, todo.ID)
			} else {
				nodeListed(s, f, turnRow(proto.StatusDone, 4, "修好了")) // the row moved with the end record
				sweeps(t, s, todo.ID, 3)
			}
			stillTurns(t, "found named later", oneEach, s, todo.ID, f)
			now := todoNow(s, todo.ID)
			if how != sameRow && !workMark(s, todo.ID).Equal(mark) {
				t.Fatalf("a start was taken for a turn that was handled: %v → %v", mark, workMark(s, todo.ID))
			}
			if now.Supervision.State != reviewPending || now.Check == nil || judged.Check == nil || now.Check.Verdict != judged.Check.Verdict {
				t.Fatalf("the turn was taken for a new one: %+v, check %+v", now.Supervision, now.Check)
			}
			if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Key != "turn-1" || e.Judged == nil {
				t.Fatalf("the ledger's turn = %+v, want turn-1 with its judgment", e)
			}
			// And told once more by the list: still that turn.
			s.TurnEnded(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
			sweeps(t, s, todo.ID, 2)
			stillTurns(t, "told again under its name", oneEach, s, todo.ID, f)
		})
	}
}

// A turn taken up while its transcript could not be read has no prompt to know it by. The turn after it is still its
// own turn, whatever the manager's clock says: here the node's clock is ten minutes behind the manager's, so the
// second turn's prompt carries a time before the moment the manager took up the first.
func TestCompletionAfterAnUnnamedTurnWithTheNodeClockBehind(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeTime := func() time.Time { return time.Now().Add(-10 * time.Minute) }
	row := func(status string, rev int64, text string) proto.Session {
		r := turnRow(status, rev, text)
		r.UpdatedAt = nodeTime()
		return r
	}
	page := func(turn, reply string) []proto.Item {
		items := turnPage(turn, reply, true)
		for i := range items {
			items[i].TS = nodeTime()
		}
		return items
	}
	nodeListed(s, f, row(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	f.judge = `{"verdict":"not_done","reason":"测试还没补"}`
	f.setItems(page("turn-1", "修了崩溃，测试还没补")...)
	f.mu.Lock()
	f.itemsFail = 1 // the end's one read of the transcript
	f.mu.Unlock()
	before := workMark(s, todo.ID)
	nodeListed(s, f, row(proto.StatusWorking, 2, ""))
	startTaken(t, s, todo.ID, before)
	nodeListed(s, f, row(proto.StatusDone, 3, "修了崩溃，测试还没补"))
	wantTurns(t, "the first turn, not read", oneEach, s, todo.ID, f)
	turnsQuiet(t, s, todo.ID)
	if e, ok := s.turnOf(context.Background(), todo.ID); !ok || e.Key != "" || e.Open != "" {
		t.Fatalf("the ledger's turn = %+v, want one with no name and no prompt", e)
	}

	f.judge = `{"verdict":"done","reason":"测试补上了"}`
	before = workMark(s, todo.ID)
	nodeListed(s, f, row(proto.StatusWorking, 4, ""))
	startTaken(t, s, todo.ID, before)
	f.setItems(page("turn-2", "测试补上了，PR https://github.com/o/r/pull/901 已合并")...)
	nodeListed(s, f, row(proto.StatusDone, 5, "测试补上了，PR https://github.com/o/r/pull/901 已合并"))
	wantTurns(t, "the turn after it", turnCounts{2, 2, 2}, s, todo.ID, f)
	if e, ok := s.turnOf(context.Background(), todo.ID); !ok || e.Key != "turn-2" {
		t.Fatalf("the ledger's turn = %+v, want turn-2", e)
	}
	sweeps(t, s, todo.ID, 3)
	stillTurns(t, "later ticks", turnCounts{2, 2, 2}, s, todo.ID, f)
}

// A meeting's turn in the task's agent is never the task's completion: found by the sweep with no start heard, it is
// left alone (no receipt, no start taken), kept as seen and not read again.
func TestCompletionUnheardMeetingTurn(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	if _, err := s.meetingTurnSent("box", "s1", "rec-1", false); err != nil {
		t.Fatal(err)
	}
	s.meetingTurnStarted("box", "s1", true)
	if !s.meetingTurnNow("box", "s1") {
		t.Fatal("no meeting turn marked")
	}
	before, mark := todoNow(s, todo.ID), workMark(s, todo.ID)
	f.setItems(turnPage("turn-1", "会议纪要整理好了", true)...)
	reads := itemReadsCounted(f)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "会议纪要整理好了"))
	sweeps(t, s, todo.ID, 5)
	stillTurns(t, "a meeting's turn", turnCounts{}, s, todo.ID, f)
	if got := reads.Load(); got != 1 {
		t.Fatalf("%d transcript reads over 5 ticks, want 1", got)
	}
	if now := todoNow(s, todo.ID); !workMark(s, todo.ID).Equal(mark) || now.Stage != before.Stage || now.Supervision.State != before.Supervision.State {
		t.Fatalf("the task was changed for a meeting's turn: start %v → %v, stage %q → %q, %+v", mark, workMark(s, todo.ID), before.Stage, now.Stage, now.Supervision)
	}
}

// A turn the user stopped between a tool and the answer gets no receipt until something is written (the end's own
// rule, taskTurnAwaitingAnswer). Found by the sweep with no start heard, no start is taken for it either, and its
// transcript is read for the look that found it, not tick after tick.
func TestCompletionUnheardStoppedBeforeItsAnswer(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	before, mark := todoNow(s, todo.ID), workMark(s, todo.ID)
	open := turnPage("turn-1", "", false) // the prompt and a tool call
	f.setItems(append(open, proto.Item{ID: "turn-1-stop#0", Kind: proto.KindNotice, Text: userStoppedNotice, TS: time.Now().UTC()})...)
	reads := itemReadsCounted(f)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, ""))
	sweeps(t, s, todo.ID, 5)
	stillTurns(t, "stopped before its answer", turnCounts{}, s, todo.ID, f)
	if got := reads.Load(); got != 2 {
		t.Fatalf("%d transcript reads over 5 ticks, want 2 (the sweep's and the end's own)", got)
	}
	if now := todoNow(s, todo.ID); !workMark(s, todo.ID).Equal(mark) || now.Stage != before.Stage || now.Supervision.State != before.Supervision.State {
		t.Fatalf("the task was changed for a turn that gets no receipt: start %v → %v, stage %q → %q, %+v", mark, workMark(s, todo.ID), before.Stage, now.Stage, now.Supervision)
	}
}

// An unknown row counts as a stopped agent only at the very row its transcript was read at. A row whose transcript
// is written to while the sweep reads (its size moved) is not taken on that look, with no start taken for it; the
// next tick looks at the row as it is then, and the turn is taken up once.
func TestCompletionUnknownRowMovesDuringTheLook(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusUnknown, 1, ""))
	sweeps(t, s, todo.ID, 1)
	mark := workMark(s, todo.ID)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	nodeListed(s, f, turnRow(proto.StatusUnknown, 3, "修好了"))
	var once sync.Once
	f.hookReads(receiptHooks{before: func(int) {
		once.Do(func() { f.setSessions("box", turnRow(proto.StatusUnknown, 4, "修好了")) }) // written to under the read
	}})
	sweeps(t, s, todo.ID, 1)
	if n := countTurns(t, s, todo.ID, f); n != (turnCounts{}) || !workMark(s, todo.ID).Equal(mark) {
		t.Fatalf("taken on a row that moved under the look: %+v, start %v → %v", n, mark, workMark(s, todo.ID))
	}
	sweeps(t, s, todo.ID, 1)
	wantTurns(t, "the next tick, at the row as it is", oneEach, s, todo.ID, f)
	sweeps(t, s, todo.ID, 3)
	stillTurns(t, "later ticks", oneEach, s, todo.ID, f)
}

// The row the sweep read and the rows the end's own looks get later are the same row as long as the transcript's
// size is: a node's later list can carry another time for a transcript that did not change (its metadata is read a
// few seconds late). An unknown row is still the stopped agent its transcript showed, in the same look and through
// the judgment, and the list that only changed the time costs no second read.
func TestCompletionUnknownRowListedAgainWithALaterTime(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusUnknown, 1, ""))
	sweeps(t, s, todo.ID, 1)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	row := turnRow(proto.StatusUnknown, 3, "修好了")
	nodeListed(s, f, row)
	var reads atomic.Int32
	f.hookReads(receiptHooks{before: func(int) {
		later := row
		later.UpdatedAt = row.UpdatedAt.Add(time.Duration(reads.Add(1)) * 3 * time.Second) // listed again with a later time, at every read
		f.setSessions("box", later)
	}})
	sweeps(t, s, todo.ID, 1)
	wantTurns(t, "one look", oneEach, s, todo.ID, f)
	if now := todoNow(s, todo.ID); now.Check == nil || now.Check.Verdict != verdictDone {
		t.Fatalf("the judgment did not land on the first look: %+v", now.Check)
	}
	turnsQuiet(t, s, todo.ID)
	handled := reads.Load()
	sweeps(t, s, todo.ID, 4)
	stillTurns(t, "later ticks", oneEach, s, todo.ID, f)
	if got := reads.Load(); got != handled || f.judged != 1 {
		t.Fatalf("%d more transcript reads and %d judgments for a row whose time alone moved, want none and 1", got-handled, f.judged)
	}
}

// lateStart has a start of todo's work land while the judge is asked, n times: the judgment of the turn that just
// ended does not land then (autoFinishTask: the work changed while it was judged).
func lateStart(s *Service, f *fakeHub, todo string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := f.judge
	f.judgeBy = func(string) string {
		if n > 0 {
			n--
			s.tasks.mu.Lock()
			s.tasks.working[todo] = time.Now()
			s.tasks.mu.Unlock()
		}
		return reply
	}
}

// A judgment that did not land (a start told late changed the task's work while the judge was asked, and no turn
// came of it) is asked again by the sweep: the turn still ends with one receipt, one judgment written and one
// supervision message.
func TestCompletionJudgmentThatDidNotLand(t *testing.T) {
	shortTurnRetries(t, time.Millisecond)
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	lateStart(s, f, todo.ID, 1)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
	sweeps(t, s, todo.ID, 1)
	if now := todoNow(s, todo.ID); now.Check != nil || f.judged != 1 {
		t.Fatalf("the first judgment landed: %+v after %d calls", now.Check, f.judged)
	}
	// The looks' retry spacing is turnRetryBase doubled; a ticker paces the sweeps at that same interval.
	pace := time.NewTicker(2 * time.Millisecond)
	defer pace.Stop()
	for range 4 {
		<-pace.C
		sweeps(t, s, todo.ID, 1)
	}
	wantTurns(t, "asked again", turnCounts{receipts: 1, judgments: 2, messages: 1}, s, todo.ID, f)
	waitFor(t, "the review to wait", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if now := todoNow(s, todo.ID); now.Check == nil || now.Check.Verdict != verdictDone {
		t.Fatalf("no judgment on the todo: %+v", now.Check)
	}
	for range 4 {
		<-pace.C
		sweeps(t, s, todo.ID, 1)
	}
	stillTurns(t, "later ticks", turnCounts{receipts: 1, judgments: 2, messages: 1}, s, todo.ID, f)
}

// A judgment that never lands is not asked for ever: after turnJudgeTries more tries the turn is told unjudged, once,
// and the sweep is done with it.
func TestCompletionJudgmentThatNeverLands(t *testing.T) {
	shortTurnRetries(t, time.Millisecond)
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	lateStart(s, f, todo.ID, 1000)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	reads := itemReadsCounted(f)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
	// Past the spacing of the looks (this test's turnRetryBase, doubled); a ticker paces the sweeps at that interval.
	pace := time.NewTicker(20 * time.Millisecond)
	defer pace.Stop()
	for range turnJudgeTries + 4 {
		sweeps(t, s, todo.ID, 1)
		<-pace.C
	}
	want := turnCounts{receipts: 1, judgments: 1 + turnJudgeTries, messages: 1}
	wantTurns(t, "told unjudged", want, s, todo.ID, f)
	if e, ok := s.turnOf(context.Background(), todo.ID); !ok || e.Judged == nil || e.Judged.Err == "" || e.Signal == nil || e.Signal.Kind != sigUnjudged {
		t.Fatalf("the ledger's turn = %+v, want it told unjudged", e)
	}
	turnsQuiet(t, s, todo.ID)
	done := reads.Load()
	for range 5 {
		sweeps(t, s, todo.ID, 1)
		<-pace.C
	}
	stillTurns(t, "later ticks", want, s, todo.ID, f)
	if got := reads.Load(); got != done {
		t.Fatalf("%d more transcript reads after the turn was told unjudged, want none", got-done)
	}
}

// feedDown is a database whose feed cannot be asked while down is set.
type feedDown struct {
	*db.DB
	down atomic.Bool
}

func (d *feedDown) FeedItem(ctx context.Context, id string) (*db.FeedRow, error) {
	if d.down.Load() {
		return nil, errors.New("the database did not answer")
	}
	return d.DB.FeedItem(ctx, id)
}

// The end of a turn the ledger has is told again while the feed cannot be asked for its receipt: the ledger's word
// stands. No second notice and no second judgment over a database error; the judgment it has is put back.
func TestCompletionRepeatedEndWhileTheFeedIsDown(t *testing.T) {
	st := &feedDown{DB: taskRecoveryDatabase(t)}
	s, f, todo := newSupervisedServiceOn(t, st)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	before := workMark(s, todo.ID)
	nodeListed(s, f, turnRow(proto.StatusWorking, 2, ""))
	startTaken(t, s, todo.ID, before)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	done := turnRow(proto.StatusDone, 3, "修好了")
	nodeListed(s, f, done)
	wantTurns(t, "the turn", oneEach, s, todo.ID, f)
	waitFor(t, "the review to wait", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	turnsQuiet(t, s, todo.ID)
	notices := len(f.notices())

	st.down.Store(true)
	before = workMark(s, todo.ID)
	nodeListed(s, f, turnRow(proto.StatusWorking, 3, "")) // a list from before the end, arriving late
	startTaken(t, s, todo.ID, before)
	nodeListed(s, f, done)
	waitFor(t, "the judgment to be put back", func() bool {
		now := todoNow(s, todo.ID)
		return now.Check != nil && now.Supervision.State == reviewPending
	})
	turnsQuiet(t, s, todo.ID)
	st.down.Store(false)
	stillTurns(t, "the same end again, the feed down", oneEach, s, todo.ID, f)
	if got := len(f.notices()); got != notices {
		t.Fatalf("%d notices, want the %d there were: the turn was handled a second time", got, notices)
	}
}

// With the fleet the manager's own, the rows of a computer that is away are its copy of the node's last list, marked
// unknown: the sweep asks that computer nothing, tick after tick, and looks once it is back.
func TestTurnSweepLeavesAComputerThatIsAwayAlone(t *testing.T) {
	ft := newFleetTestNoStore(t)
	node := ft.d.add("box", "Box")
	away := func(is bool) {
		ft.d.mu.Lock()
		node.away = is
		ft.d.mu.Unlock()
	}
	linked := time.Now().Add(-time.Hour)
	task := Todo{ID: "todo_away", Origin: "conv1", CreatedAt: linked, Supervision: &Supervision{Supervisor: "conv1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: linked}}}
	row := turnRow(proto.StatusUnknown, 3, "修好了") // as its rows are kept when its connection ends
	ft.f.setSessions("box", row)
	ft.f.setItems(turnPage("turn-1", "", false)...) // a turn still open: one read, and the look is done
	reads := itemReadsCounted(ft.f)
	away(true)
	for range 5 {
		ft.s.watchTurn(context.Background(), task, []proto.Session{row})
		turnsQuiet(t, ft.s, task.ID)
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("%d transcript reads on a computer that is away, want none", got)
	}
	away(false)
	for range 3 {
		ft.s.watchTurn(context.Background(), task, []proto.Session{row})
		turnsQuiet(t, ft.s, task.ID)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("%d transcript reads once it is back, want 1 (the row did not move after it)", got)
	}
}
