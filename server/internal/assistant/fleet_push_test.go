package assistant

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/i18n"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The session lists show what the assistant's snapshot says: the todo each agent works for, the quiet ones, the task
// on an agent's card; SpawnFrom is not shown. A snapshot that changes them has the hub told the machine's list.
func TestSessionsShowTheTodoReport(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.d.setSessions("mac", []proto.Session{{SessionID: "s1"}, {SessionID: "s2"}, {SessionID: "s3", SpawnFrom: "todo_1@mac/s1"}})
	report := &TodoReport{Links: []TodoLink{
		{MachineID: "mac", SessionID: "s1", TodoID: "todo_1", Title: "修登录崩溃", Task: true},
		{MachineID: "mac", SessionID: "s2", TodoID: "todo_2", Title: "买牛奶"},
	}, Quiet: []SessionRef{{MachineID: "mac", SessionID: "s3"}}}
	n := len(ft.toldOf(assistantwire.EventFleetSessions))
	s.fleetTakeSnapshot(Snapshot{Todos: report})
	if got := len(ft.toldOf(assistantwire.EventFleetSessions)); got != n+1 {
		t.Fatalf("lists told %d, want 1", got-n)
	}
	s.fleetTakeSnapshot(Snapshot{Todos: report})
	if got := len(ft.toldOf(assistantwire.EventFleetSessions)); got != n+1 {
		t.Fatal("the same report told the list again")
	}
	s1, s2, s3 := listed(t, s, "mac", "s1"), listed(t, s, "mac", "s2"), listed(t, s, "mac", "s3")
	if s1.Todo == nil || s1.Todo.ID != "todo_1" || s1.Todo.Title != "修登录崩溃" || s1.Quiet {
		t.Fatalf("s1 = %+v quiet %v", s1.Todo, s1.Quiet)
	}
	if s2.Todo == nil || s2.Todo.ID != "todo_2" || s3.Todo != nil || !s3.Quiet || s3.SpawnFrom != "" {
		t.Fatalf("s2 %+v, s3 %+v quiet %v from %q", s2.Todo, s3.Todo, s3.Quiet, s3.SpawnFrom)
	}
	if got := s.withTask(proto.Interaction{MachineID: "mac", SessionID: "s2"}).TaskTitle; got != "" {
		t.Fatalf("a todo by hand named on the card: %q", got)
	}
}

// A quiet task's agent (#1731) is flagged on the list, stays off the Live Activity, and its card is no push (the main
// conversation is told); another agent's card is pushed.
func TestQuietAgents(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1"},
		{SessionID: "s2", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p2"},
	})
	s.fleetTakeSnapshot(Snapshot{Todos: &TodoReport{Quiet: []SessionRef{{MachineID: "mac", SessionID: "s1"}}}})
	if !listed(t, s, "mac", "s1").Quiet || listed(t, s, "mac", "s2").Quiet {
		t.Fatal("quiet flags")
	}
	if st := s.fleetSnapshot(); st.Working != 1 || len(st.Items) != 1 || st.Items[0].ID != "mac/s2" {
		t.Fatalf("fleet = %+v", st)
	}
	s.announce(proto.Interaction{ID: "ix_q", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash"})
	s.announce(proto.Interaction{ID: "ix_s", MachineID: "mac", SessionID: "s2", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash"})
	got := ft.pushesOf(assistantwire.NotifyInput, 2, 300*time.Millisecond)
	if len(got) != 1 || got[0].SessionID != "s2" {
		t.Fatalf("pushes %+v", got)
	}
}

// fastDone shortens the finished push's delay for a test.
func fastDone(ft *fleetTest) {
	ft.f.mu.Lock()
	ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 50, LongTurnMS: 60_000}
	ft.f.mu.Unlock()
}

// A turn that ends (working → done) and is left alone is pushed once, with how long it took; the same completion
// seen again (its Stop's reply already pushed for) is not; one the user looked at, a muted one, one a channel
// answers, an inbox agent's and one the conversation pushed itself (notify_user) are not either.
func TestFinishedPushDecidedOnce(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	work := proto.Session{SessionID: "s1", Title: "修登录", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1",
		UpdatedAt: time.Now()}
	done := work
	done.Status, done.LastText = proto.StatusDone, "改好了"
	ft.event("mac", "hook.event", map[string]any{"session_id": "s1", "event": "UserPromptSubmit"})
	ft.sessions("mac", work)
	ft.event("mac", "hook.event", map[string]any{"session_id": "s1", "event": "Stop", "stop_sig": "sig1"})
	ft.sessions("mac", done)
	got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
	if len(got) != 1 || got[0].SessionID != "s1" || got[0].Title != "修登录" || got[0].Body != "改好了" || got[0].Subtitle != "Mac" ||
		got[0].LongMS != 60_000 {
		t.Fatalf("pushes %+v", got)
	}
	// The status flaps around the same turn: the same reply is no second push.
	ft.sessions("mac", work)
	ft.sessions("mac", done)
	time.Sleep(300 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 2, 0); len(got) != 1 {
		t.Fatalf("pushed again for the same completion: %+v", got)
	}
	// Seen in the app before the delay: nothing.
	ft.event("mac", "hook.event", map[string]any{"session_id": "s1", "event": "UserPromptSubmit"})
	ft.sessions("mac", work)
	done.UpdatedAt = time.Now()
	ft.sessions("mac", done)
	ft.s.markSeen(context.Background(), "mac", "s1")
	time.Sleep(300 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 2, 0); len(got) != 1 {
		t.Fatalf("pushed a turn the user looked at: %+v", got)
	}
	// The conversation pushed what matters itself.
	ft.s.noteAsked("s1")
	ft.event("mac", "hook.event", map[string]any{"session_id": "s1", "event": "UserPromptSubmit"})
	ft.sessions("mac", work)
	done.UpdatedAt, done.LastText = time.Now().Add(time.Second), "又改了"
	ft.sessions("mac", done)
	time.Sleep(300 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 2, 0); len(got) != 1 {
		t.Fatalf("pushed after notify_user: %+v", got)
	}
}

func TestFinishedPushSkipsBackgroundPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name, source, reply, turnError string
		want                           int
	}{
		{name: "supervision placeholder", source: proto.SourceSupervision, reply: "No response requested."},
		{name: "scheduled tools only", source: proto.SourcePatrol},
		{name: "relay result", source: proto.SourceConversation, reply: "Verification passed.", want: 1},
		{name: "direct placeholder", reply: "No response requested.", want: 1},
		{name: "model error", source: proto.SourceSupervision, reply: "No response requested.", turnError: "Model overloaded", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			fastDone(ft)
			owner := proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "Check the result."}
			if tc.source != "" {
				owner.Source = &proto.MessageSource{Kind: tc.source}
			}
			ft.f.setItems(owner, proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: tc.reply})
			work := proto.Session{SessionID: "s1", Title: "Check", Cwd: "/home/u/.persona/assistant",
				Assistant: true, Agent: "codex", Status: proto.StatusWorking}
			ft.f.setSessions("mac", work)
			ft.sessions("mac", work)
			done := work
			done.Status, done.LastText, done.TurnError = proto.StatusDone, tc.reply, tc.turnError
			ft.sessions("mac", done)
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, 400*time.Millisecond); len(got) != tc.want {
				t.Fatalf("finished pushes = %d, want %d", len(got), tc.want)
			}
		})
	}
}

// The relay is delivered in every case. Only an ordinary finished alert for a completed main relay may be quiet.
func TestFinishedPushFoldsOnlyRoutineMainRelay(t *testing.T) {
	const on = `{"fold_routine_relay_replies":true}`
	const off = `{"fold_routine_relay_replies":false}`
	for _, agent := range []string{"", "codex", "dsh"} {
		for _, tc := range []struct {
			name, prefs, source, main, turnError string
			promote, trailing, cutOff, stuck     bool
			want                                 int
		}{
			{name: "default", source: proto.SourceConversation, main: "main-1", want: 1},
			{name: "explicitly off", prefs: off, source: proto.SourceConversation, main: "main-1", want: 1},
			{name: "enabled", prefs: on, source: proto.SourceConversation, main: "main-1"},
			{name: "display folding disabled", prefs: `{"main_background_folded":false,"fold_routine_relay_replies":true}`, source: proto.SourceConversation, main: "main-1", want: 1},
			{name: "same relay outside main", prefs: on, source: proto.SourceConversation, main: "leader-1", want: 1},
			{name: "main explicitly promotes", prefs: on, source: proto.SourceConversation, main: "main-1", promote: true, want: 1},
			{name: "main scheduled reply", prefs: on, source: proto.SourcePatrol, main: "main-1", want: 1},
			{name: "direct answer then unanswered relay", prefs: on, main: "main-1", trailing: true, want: 1},
			{name: "promoted answer then unanswered relay", prefs: on, source: proto.SourceConversation, main: "main-1", promote: true, trailing: true, want: 1},
			{name: "routine answer then unanswered relay", prefs: on, source: proto.SourceConversation, main: "main-1", trailing: true},
			{name: "model error", prefs: on, source: proto.SourceConversation, main: "main-1", turnError: "Model overloaded", want: 1},
			{name: "cut off", prefs: on, source: proto.SourceConversation, main: "main-1", cutOff: true, want: 1},
			{name: "stuck", prefs: on, source: proto.SourceConversation, main: "main-1", stuck: true, want: 1},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				ft := newFleetTestNoStore(t)
				fastDone(ft)
				setSettings(t, ft.s, func(v *Settings) {
					v.Main = tc.main
					if tc.prefs != "" {
						if err := json.Unmarshal([]byte(tc.prefs), v); err != nil {
							t.Fatal(err)
						}
					}
				})
				owner := proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "Report the result."}
				if tc.source != "" {
					owner.Source = &proto.MessageSource{Kind: tc.source, ConversationID: "leader"}
				}
				items := []proto.Item{owner}
				if tc.promote {
					items = append(items, proto.Item{ID: "n1", Role: "assistant", Kind: proto.KindToolUse, ToolName: "notify_user", ToolUseID: "notify-1"},
						proto.Item{ID: "n2", Role: "user", Kind: proto.KindToolResult, ToolUseID: "notify-1", Text: "sent"})
				}
				reply := "Verification passed."
				if tc.cutOff {
					reply = "API Error: 529 overloaded"
				}
				items = append(items, proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: reply})
				if tc.trailing {
					items = append(items, proto.Item{ID: "u2", Role: "user", Kind: proto.KindText, Text: "Another report.", Source: &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "peer"}})
				}
				ft.f.setItems(items...)
				work := proto.Session{SessionID: "main-1", Title: "Delegated check", Cwd: "/home/u/.persona/assistant", Assistant: true, Agent: agent, Status: proto.StatusWorking}
				if agent == "dsh" {
					work.SourceKind = proto.SourceRuntime
				}
				ft.f.setSessions("mac", work)
				ft.sessions("mac", work)
				done := work
				done.Status, done.LastText, done.TurnError = proto.StatusDone, reply, tc.turnError
				if tc.stuck {
					done.Work = &proto.WorkState{Phase: proto.WorkBlocked, Line: "Waiting for the owner"}
				}
				ft.sessions("mac", done)
				got := ft.pushesOf(assistantwire.NotifyDone, 1, 450*time.Millisecond)
				if len(got) != tc.want {
					t.Fatalf("finished pushes = %d, want %d", len(got), tc.want)
				}
			})
		}
	}
}

// A runtime can report a host-side failure without a model TurnError. That failure must still reach the owner even
// when the transcript's last answer looks routine and the owner opted into quiet relay replies.
func TestFinishedPushDeliversRuntimeHostFailure(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	setSettings(t, ft.s, func(v *Settings) {
		v.Main = "main-1"
		v.FoldRoutineRelayReplies = new(true)
	})
	ft.f.setItems(
		proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "Report the result.", Source: &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "leader"}},
		proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: "Verification passed."},
	)
	work := proto.Session{SessionID: "main-1", Title: "Delegated check", Cwd: "/home/u/.persona/assistant", Assistant: true,
		Agent: "dsh", SourceKind: proto.SourceRuntime, Status: proto.StatusWorking}
	ft.f.setSessions("mac", work)
	ft.sessions("mac", work)
	done := work
	done.Status, done.LastText = proto.StatusDone, "Verification passed."
	done.FailureReason = "The host stopped the turn on an error."
	ft.sessions("mac", done)
	got := ft.pushesOf(assistantwire.NotifyDone, 1, 450*time.Millisecond)
	if len(got) != 1 || got[0].SessionID != "main-1" {
		t.Fatalf("host failure finished pushes = %+v, want the normal notification", got)
	}
}

// A turn that a channel answers is no push to the phone, nor is an inbox agent's.
func TestFinishedPushSkipsChannelTurns(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	ft.s.fleetTakeSnapshot(Snapshot{Channels: &ChannelsReport{Turns: []ChannelTurn{{MachineID: "mac", SessionID: "c1"}}},
		Inbox: &assistantwire.InboxReport{Agents: []SessionRef{{MachineID: "mac", SessionID: "in1"}}}})
	for _, id := range []string{"c1", "in1"} {
		work := proto.Session{SessionID: id, Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p-" + id}
		done := work
		done.Status, done.LastText = proto.StatusDone, "好了"
		ft.sessions("mac", work)
		ft.sessions("mac", done)
	}
	time.Sleep(400 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 0); len(got) != 0 {
		t.Fatalf("pushed %+v", got)
	}
}

// A turn that stops on a background command that reports back before the push's delay is up: the turn the report
// wakes is the end of the work, pushed once with the time from the first start (#4187). It used to be dropped as a
// turn a task started on its own, since the hold was only noted by a check that came after the wake.
func TestFinishedPushAfterQuickBackground(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.f.mu.Lock()
	ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 300, LongTurnMS: 60_000}
	ft.f.mu.Unlock()
	work := proto.Session{SessionID: "s1", Title: "跑测试", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1",
		UpdatedAt: time.Now()}
	ft.sessions("mac", work)
	stopped := work
	stopped.Status, stopped.LastText = proto.StatusDone, "测试放到后台跑了"
	stopped.Background = []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}
	ft.sessions("mac", stopped)
	woken := work
	woken.WokeByTask = true // the command's report starts the next turn
	ft.sessions("mac", woken)
	finished := woken
	finished.Status, finished.LastText = proto.StatusDone, "全部通过"
	ft.sessions("mac", finished)
	got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
	if len(got) != 1 || got[0].SessionID != "s1" || got[0].Body != "全部通过" {
		t.Fatalf("pushes %+v", got)
	}
	time.Sleep(400 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 2, 0); len(got) != 1 {
		t.Fatalf("pushed twice: %+v", got)
	}
}

// The same, when the stop and the report's wake fall in one of the node's list ticks: the hub never sees the turn
// stop, only the woken flag turn on while it works. The woken turn's end is still the one push of the work.
func TestFinishedPushAfterBackgroundWakeInOneTick(t *testing.T) {
	for name, merged := range map[string]func(work proto.Session) []proto.Session{
		// stop + wake in one list, then the woken turn ends
		"woken while working": func(work proto.Session) []proto.Session {
			woken := work
			woken.WokeByTask = true
			finished := woken
			finished.Status, finished.LastText = proto.StatusDone, "全部通过"
			return []proto.Session{woken, finished}
		},
		// the transcript is re-read at most every few seconds: the report (the background gone, the woken flag) comes
		// with the woken turn's end
		"report comes with the end": func(work proto.Session) []proto.Session {
			finished := work
			finished.Status, finished.LastText, finished.WokeByTask = proto.StatusDone, "全部通过", true
			return []proto.Session{finished}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			ft.f.mu.Lock()
			ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 300, LongTurnMS: 60_000}
			ft.f.mu.Unlock()
			work := proto.Session{SessionID: "s1", Title: "跑测试", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking,
				PaneID: "p1", UpdatedAt: time.Now()}
			ft.sessions("mac", work)
			work.Background = []proto.BackgroundTask{{ID: "b1", Kind: "shell"}} // the turn puts the tests in the background
			ft.sessions("mac", work)
			work.Background = nil // reported back by the next list
			for _, sess := range merged(work) {
				ft.sessions("mac", sess)
			}
			got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
			if len(got) != 1 || got[0].Body != "全部通过" {
				t.Fatalf("pushes %+v", got)
			}
		})
	}
}

// The background seen earlier in the turn counts, though the list just before the wake no longer has it (its
// background and its woken flag are read apart), and so does one seen when the manager came up mid-turn.
func TestFinishedPushAfterBackgroundGoneBeforeWake(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.f.mu.Lock()
	ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 300, LongTurnMS: 60_000}
	ft.f.mu.Unlock()
	work := proto.Session{SessionID: "s1", Title: "跑测试", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1",
		UpdatedAt: time.Now(), Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}}
	ft.sessions("mac", work) // the first list the manager sees: already working, with background
	work.Background = nil    // the report read before the wake
	ft.sessions("mac", work)
	woken := work
	woken.WokeByTask = true
	ft.sessions("mac", woken)
	finished := woken
	finished.Status, finished.LastText = proto.StatusDone, "全部通过"
	ft.sessions("mac", finished)
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second); len(got) != 1 || got[0].Body != "全部通过" {
		t.Fatalf("pushes %+v", got)
	}
}

// A turn with no background work whose woken flag turns on while it works (a /loop firing seen late) is no held
// work: its end is not pushed.
func TestNoPushForWakeWithoutBackground(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.f.mu.Lock()
	ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 200, LongTurnMS: 60_000}
	ft.f.mu.Unlock()
	work := proto.Session{SessionID: "s1", Title: "跑测试", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1",
		UpdatedAt: time.Now()}
	ft.sessions("mac", work)
	woken := work
	woken.WokeByTask = true
	ft.sessions("mac", woken)
	finished := woken
	finished.Status, finished.LastText = proto.StatusDone, "又跑了一次"
	ft.sessions("mac", finished)
	time.Sleep(500 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 0); len(got) != 0 {
		t.Fatalf("pushes %+v", got)
	}
}

// A turn a background task started after the user looked at the stop (it was seen, the hold dropped) is still no push.
func TestNoPushForWakeAfterSeenStop(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.f.mu.Lock()
	ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 200, LongTurnMS: 60_000}
	ft.f.mu.Unlock()
	work := proto.Session{SessionID: "s1", Title: "跑测试", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1",
		UpdatedAt: time.Now()}
	ft.sessions("mac", work)
	idle := work
	idle.Status = proto.StatusIdle // looked at in herdr
	ft.sessions("mac", idle)
	time.Sleep(400 * time.Millisecond)
	woken := work
	woken.WokeByTask = true
	ft.sessions("mac", woken)
	finished := woken
	finished.Status, finished.LastText = proto.StatusDone, "又跑了一次"
	ft.sessions("mac", finished)
	time.Sleep(500 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 0); len(got) != 0 {
		t.Fatalf("pushes %+v", got)
	}
}

// The own conversation of a scheduled task that pushes only what its runs send (#2396) is known by the task list the
// snapshot has; other tasks' conversations, a task without one yet and the main conversation are not.
func TestQuietTaskConversation(t *testing.T) {
	s := newFleetTestNoStore(t).s
	if s.quietTaskConversation("own-q") {
		t.Fatal("nothing reported yet: no conversation is quiet")
	}
	s.fleetTakeSnapshot(Snapshot{Tasks: []TaskView{
		{ID: "fu_q", Push: assistantwire.PushAsked, RunMachineID: "server", RunSessionID: "own-q"},
		{ID: "fu_e", RunMachineID: "server", RunSessionID: "own-e"},
		{ID: "fu_new", Push: assistantwire.PushAsked}, // its first run has not started its conversation yet
	}})
	for session, want := range map[string]bool{"own-q": true, "own-e": false, "main-1": false, "": false} {
		if got := s.quietTaskConversation(session); got != want {
			t.Errorf("%q: %v, want %v", session, got, want)
		}
	}
}

// A quiet task's normal finish on Claude Code or a headless Codex is no push, but not once that conversation is the
// main one, nor a runtime's (its failures are not on the session), nor an error.
func TestQuietTaskFinish(t *testing.T) {
	s := newFleetTestNoStore(t).s
	tasks := []TaskView{{ID: "fu_q", Push: assistantwire.PushAsked, RunSessionID: "own-q"}}
	s.fleetTakeSnapshot(Snapshot{Tasks: tasks})
	done := proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "没有新邮件。"}
	if s.quietTaskFinish(done) {
		t.Fatal("the assistant's settings (its main conversation) not known yet: pushed as ever")
	}
	s.fleetTakeSnapshot(Snapshot{Tasks: tasks, Profile: &Profile{MachineID: "server", Main: "main-1"}})
	if !s.quietTaskFinish(done) {
		t.Fatal("a quiet task's normal finish is not pushed")
	}
	codex := done
	codex.Agent = "codex"
	if !s.quietTaskFinish(codex) {
		t.Fatal("a quiet task's normal finish on a headless Codex is not pushed (#2571 reports its failures)")
	}
	codexFailed := codex
	codexFailed.TurnError, codexFailed.TurnErrorKind = "unexpected status 503 Service Unavailable", proto.TurnErrorTransient
	runtime := done
	runtime.Agent = "dsh"
	cut := done
	cut.LastText = "API Error: 529 overloaded"
	other := done
	other.SessionID = "main-1"
	for name, sess := range map[string]proto.Session{"headless codex, failed turn": codexFailed, "a runtime": runtime, "cut off": cut,
		"another conversation": other} {
		if s.quietTaskFinish(sess) {
			t.Errorf("%s is pushed as ever", name)
		}
	}
	s.fleetTakeSnapshot(Snapshot{Tasks: tasks, Profile: &Profile{MachineID: "server", Main: "own-q"}})
	if s.quietTaskFinish(done) {
		t.Fatal("the main conversation is never quiet, even when it was the task's")
	}
}

// A health conversation's finished turn is pushed without its words or title; another's says them as before.
func TestHealthDonePushHidesContent(t *testing.T) {
	head, text := doneAlert(i18n.ZhHans, proto.Session{Title: "血压", Cwd: "/home/me/.persona/health", LastText: "这周平均 150/95"})
	if strings.Contains(head, "血压") || strings.Contains(text, "150") || !strings.Contains(head, "健康") {
		t.Fatalf("health done: %q %q", head, text)
	}
	if head, text := doneAlert(i18n.ZhHans, proto.Session{Title: "周报", Cwd: "/home/me/.persona/assistant", LastText: "写好了"}); head != "周报" || text != "写好了" {
		t.Fatalf("assistant's done: %q %q", head, text)
	}
}

// A scheduled run (morning briefing) or a task notice that the assistant answers quickly is pushed all the same: its finished push
// asks for no minimum length (LongMS 0), so "Only long-running turns" does not drop it (#3438). The user's next turn there goes by
// the device's setting again, and so does a turn of the user's during which a run was sent (the run is the next turn).
func TestReportTurnPushedHoweverShort(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	work := proto.Session{SessionID: "s1", Title: "早报", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1",
		UpdatedAt: time.Now()}
	done := work
	turn := func(text string) {
		work.UpdatedAt = time.Now()
		ft.sessions("mac", work)
		done.Status, done.LastText, done.UpdatedAt = proto.StatusDone, text, time.Now()
		ft.sessions("mac", done)
	}
	wantLong := func(n int, long int64) {
		t.Helper()
		got := ft.pushesOf(assistantwire.NotifyDone, n, 2*time.Second)
		if len(got) != n || got[n-1].LongMS != long {
			t.Fatalf("push %d: want LongMS %d, have %+v", n, long, got)
		}
	}

	ft.s.noteReportTurn("s1")
	turn("今天三个会")
	wantLong(1, 0)

	turn("好的") // the user's own turn
	wantLong(2, 60_000)

	// A run sent a minute into a turn of the user's: that turn is not the run's, the next one is.
	work.UpdatedAt = time.Now()
	ft.sessions("mac", work)
	tb := ft.s.turnsBook()
	tb.mu.Lock()
	tb.turns["mac/s1"].start = time.Now().Add(-time.Minute)
	tb.mu.Unlock()
	ft.s.noteReportTurn("s1")
	done.LastText, done.UpdatedAt = "改好了", time.Now()
	ft.sessions("mac", done)
	wantLong(3, 60_000)
	turn("巡检：一切正常")
	wantLong(4, 0)

	// The run's turn is dropped (it pushed with notify_user): its mark goes with it, the user's quick reply after it
	// is not pushed as a report (review of #3713).
	ft.s.noteReportTurn("s1")
	ft.s.noteAsked("s1")
	turn("已通知")
	time.Sleep(300 * time.Millisecond)
	if got := ft.pushesOf(assistantwire.NotifyDone, 5, 0); len(got) != 4 {
		t.Fatalf("a turn that pushed itself is not pushed again: %+v", got)
	}
	ft.s.fleet.mu.Lock()
	delete(ft.s.fleet.notified, "s1")
	ft.s.fleet.mu.Unlock()
	turn("好的")
	wantLong(5, 60_000)
}

// A turn told elsewhere (answered in a channel, a quiet task's told in the main conversation) is over all the same:
// the next turn there counts from its own start, so a quick answer is not pushed as a long one and the Live
// Activity's timer does not start hours ago (#4133).
func TestToldElsewhereTurnDoesNotLendItsStart(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	cases := []struct {
		id          string
		elsewhere   Snapshot
		backToPhone Snapshot
	}{
		{"c1", Snapshot{Channels: &ChannelsReport{Turns: []ChannelTurn{{MachineID: "mac", SessionID: "c1"}}}},
			Snapshot{Channels: &ChannelsReport{Turns: []ChannelTurn{}}}},
		{"q1", Snapshot{Todos: &TodoReport{Quiet: []SessionRef{{MachineID: "mac", SessionID: "q1"}}}},
			Snapshot{Todos: &TodoReport{}}},
		{"in1", Snapshot{Inbox: &assistantwire.InboxReport{Agents: []SessionRef{{MachineID: "mac", SessionID: "in1"}}}},
			Snapshot{Inbox: &assistantwire.InboxReport{}}},
	}
	for i, c := range cases {
		work := proto.Session{SessionID: c.id, Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p-" + c.id,
			UpdatedAt: time.Now()}
		done := work
		done.Status, done.LastText = proto.StatusDone, "好了"
		ft.s.fleetTakeSnapshot(c.elsewhere)
		ft.sessions("mac", work)
		tb := ft.s.turnsBook()
		tb.mu.Lock()
		tb.turns["mac/"+c.id].start = time.Now().Add(-2 * time.Hour) // a long turn, told elsewhere
		tb.mu.Unlock()
		ft.sessions("mac", done)
		for deadline := time.Now().Add(2 * time.Second); !tb.startOf("mac/" + c.id).IsZero(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the turn told elsewhere is still tracked from %v", c.id, tb.startOf("mac/"+c.id))
			}
		}
		if got := ft.pushesOf(assistantwire.NotifyDone, i+1, 0); len(got) != i {
			t.Fatalf("%s: the turn told elsewhere was pushed: %+v", c.id, got)
		}
		// The next turn is the user's in the app, and quick.
		ft.s.fleetTakeSnapshot(c.backToPhone)
		work.UpdatedAt = time.Now()
		ft.sessions("mac", work)
		done.UpdatedAt, done.LastText = time.Now(), "又好了"
		ft.sessions("mac", done)
		got := ft.pushesOf(assistantwire.NotifyDone, i+1, 2*time.Second)
		if len(got) != i+1 || got[i].SessionID != c.id || got[i].LongMS != 60_000 || got[i].TookMS >= got[i].LongMS {
			t.Fatalf("%s: the quick turn after it: %+v", c.id, got)
		}
	}
}

// A task notice answered by a turn told elsewhere is used up by it: the user's quick turn right after goes by the
// device's setting, not pushed as a report (#4133).
func TestToldElsewhereTurnUsesUpItsReportMark(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	ft.s.fleetTakeSnapshot(Snapshot{Channels: &ChannelsReport{Turns: []ChannelTurn{{MachineID: "mac", SessionID: "c1"}}}})
	work := proto.Session{SessionID: "c1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p-c1", UpdatedAt: time.Now()}
	done := work
	done.Status, done.LastText = proto.StatusDone, "好了"
	ft.s.noteReportTurn("c1")
	ft.sessions("mac", work)
	ft.sessions("mac", done)
	tb := ft.s.turnsBook()
	for deadline := time.Now().Add(2 * time.Second); !tb.startOf("mac/c1").IsZero(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the turn told elsewhere is still tracked")
		}
	}
	ft.s.fleetTakeSnapshot(Snapshot{Channels: &ChannelsReport{Turns: []ChannelTurn{}}})
	work.UpdatedAt = time.Now()
	ft.sessions("mac", work)
	done.UpdatedAt, done.LastText = time.Now(), "又好了"
	ft.sessions("mac", done)
	got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
	if len(got) != 1 || got[0].LongMS != 60_000 {
		t.Fatalf("the user's turn after it: %+v", got)
	}
}

// The main chat's push for a turn that wrote a long report, called agent_send and closed with a short line: the node
// puts the turn's answer from the report on in the row's LastText (node assistantAnswer), and the body is that, so the
// report is what the lock screen shows first.
func TestDoneAlertBodyStartsWithTheReport(t *testing.T) {
	row := "今天的进展：第一项已经合并，第二项在等 CI，第三项需要你决定是否上线。 已经发给它了。"
	_, text := doneAlert(i18n.ZhHans, proto.Session{Title: "主对话", Cwd: "/home/me/.persona/assistant", LastText: row})
	if !strings.HasPrefix(text, "今天的进展") || !strings.HasSuffix(text, "已经发给它了。") {
		t.Fatalf("body %q", text)
	}
}
