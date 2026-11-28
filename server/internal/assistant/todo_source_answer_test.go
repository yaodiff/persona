package assistant

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSourceReportFullTaskAnswer(t *testing.T) {
	needsLarkLayer(t)
	forStores(t, func(t *testing.T, e *askEnv, _ *db.DB) {
		e.fullReply = true
		e.ask(ownerAsk("om_task_result", "/ai Check the release notes"))
		id := onlyTodo(e.s).ID
		if err := e.s.linkTodoAgent(t.Context(), id, "box", "agent01", "Check the release notes", "", "claude"); err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC()
		e.f.setItems(
			proto.Item{Role: "user", Kind: proto.KindText, TS: at, Text: "Check the release notes"},
			proto.Item{Role: "assistant", Kind: proto.KindText, TS: at, Text: "I am running the checks."},
			proto.Item{Role: "assistant", Kind: proto.KindToolUse, TS: at, ToolName: "check"},
			proto.Item{Role: "user", Kind: proto.KindToolResult, TS: at, Text: "Tool log"},
			proto.Item{ID: "final", Role: "assistant", Kind: proto.KindText, TS: at, Text: "Truncated", FullSize: 100},
			proto.Item{Role: "assistant", Kind: proto.KindText, TS: at, Text: "See the [release notes](https://example.com/notes)."},
			proto.Item{ID: "end", Kind: "turn_duration", TS: at},
		)
		final := "The notes are ready.\n\n- Fixed a crash\n- Added an example"
		e.f.answerNode("session.item", func(machine string, args json.RawMessage) (any, error) {
			var req struct {
				SessionID string `json:"session_id"`
				ItemID    string `json:"item_id"`
			}
			_ = json.Unmarshal(args, &req)
			if machine != "box" || req.SessionID != "agent01" || req.ItemID != "final" {
				t.Errorf("unexpected transcript read %s %s", machine, args)
			}
			return proto.Item{ID: "final", Role: "assistant", Kind: proto.KindText, TS: at, Text: final}, nil
		})
		e.closeDone(id)
		want := final + "\n\nSee the [release notes](https://example.com/notes)."
		notice := e.said("source_report_full_notice", i18n.Args{"minutes": 10, "text": want})
		if !slices.ContainsFunc(textsTo(e.f, askHome), func(p map[string]any) bool {
			return p["text"] == notice && p["full_answer"] == true
		}) {
			t.Fatal("the owner did not get the complete answer preview")
		}
		if source := onlyTodo(e.s).Source; !source.ReportFullReply || source.ReportText != want {
			t.Fatalf("planned reply: %+v", source)
		}
		e.clock.advance(sourceReportDelay - time.Second)
		if got := reportsTo(e.f, askSrc, "om_task_result"); len(got) != 0 {
			t.Fatalf("reported during the preview: %v", got)
		}
		e.clock.advance(time.Second)
		if got := reportsTo(e.f, askSrc, "om_task_result"); len(got) != 1 || got[0]["text"] != want || got[0]["full_answer"] != true || got[0]["no_detour"] != true {
			t.Fatalf("the original message did not get the full answer: %v", got)
		}
	})
}

func TestSourceReportMainAnswerWaitsForItsOwnTurn(t *testing.T) {
	needsLarkLayer(t)
	forStores(t, func(t *testing.T, e *askEnv, database *db.DB) {
		minutes := 0
		e.fullReply, e.reportDelay = true, &minutes
		if database != nil {
			e.home = "" // cover the private-chat handoff as well as the topic
		}
		e.ask(ownerAsk("om_main_result", "/ai What shipped?"))
		todo := onlyTodo(e.s)
		handoff := todo.Source.Handoff
		if handoff == "" {
			handoff = todo.Source.Anchor
		}
		at := time.Now().UTC()
		prompt := proto.Item{
			Role: "user", Kind: proto.KindText, TS: at, Text: "What shipped?",
			ClientMsgID: channel.ClientMsgID(channel.Feishu, handoff),
		}
		e.f.setItems(prompt)
		e.closeDone(todo.ID)
		e.clock.advance(0)
		if src := onlyTodo(e.s).Source; src.ReportDueAt != "" {
			t.Fatal("planned a status reply while its own turn was still finishing")
		}
		answer := "This release includes:\n\n1. A fix\n2. A guide"
		e.f.setItems(prompt,
			proto.Item{Role: "assistant", Kind: proto.KindText, TS: at, Text: answer},
			proto.Item{ID: "end", Kind: "turn_duration", TS: at},
			proto.Item{Role: "user", Kind: proto.KindText, TS: at, Text: "Separate private request"},
			proto.Item{Role: "assistant", Kind: proto.KindText, TS: at, Text: "Separate private answer"},
			proto.Item{ID: "other_end", Kind: "turn_duration", TS: at},
		)
		e.clock.advance(5 * time.Second)
		if got := reportsTo(e.f, askSrc, "om_main_result"); len(got) != 1 || got[0]["text"] != answer {
			t.Fatalf("expected the handoff's answer, got %v", got)
		}
	})
}

func TestSourceReportMainAnswerWaitsForItsBackgroundWork(t *testing.T) {
	needsLarkLayer(t)
	forStores(t, func(t *testing.T, e *askEnv, _ *db.DB) {
		minutes := 0
		e.fullReply, e.reportDelay = true, &minutes
		e.ask(ownerAsk("om_background", "/ai Check the release"))
		todo := onlyTodo(e.s)
		at := time.Now().UTC()
		items := []proto.Item{
			{Seq: 1, Role: "user", Kind: proto.KindText, TS: at, ClientMsgID: channel.ClientMsgID(channel.Feishu, todo.Source.Handoff)},
			{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_check"},
			{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_check", Text: "Command running in background with ID: check1."},
			{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "The check is still running."},
			{ID: "interim_end", Seq: 5, Kind: turnDurationKind},
		}
		e.f.setItems(items...)
		e.closeDone(todo.ID) // the channel watcher can mark the run held after the todo closes
		e.clock.advance(0)
		if src := onlyTodo(e.s).Source; src.ReportDueAt != "" {
			t.Fatalf("planned an interim reply: %q", src.ReportText)
		}

		run, _ := e.s.channelRunByID("fb_" + todo.Source.Handoff)
		if run == nil {
			t.Fatal("missing handoff run")
		}
		e.s.chans.mu.Lock()
		run.Hold(5, at)
		e.s.chans.mu.Unlock()
		items = append(items,
			proto.Item{Seq: 6, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "unrelated", Status: "completed"}},
			proto.Item{Seq: 7, Role: "assistant", Kind: proto.KindText, Text: "A different background task finished."},
			proto.Item{ID: "other_end", Seq: 8, Kind: turnDurationKind},
		)
		e.f.setItems(items...)
		e.clock.advance(5 * time.Second)
		if src := onlyTodo(e.s).Source; src.ReportDueAt != "" {
			t.Fatalf("planned an unrelated reply: %q", src.ReportText)
		}

		const answer = "The release check passed."
		items = append(items,
			proto.Item{Seq: 9, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
			proto.Item{Seq: 10, Role: "assistant", Kind: proto.KindText, Text: answer},
			proto.Item{ID: "final_end", Seq: 11, Kind: turnDurationKind},
		)
		e.f.setItems(items...)
		e.clock.advance(5 * time.Second)
		if got := reportsTo(e.f, askSrc, "om_background"); len(got) != 1 || got[0]["text"] != answer || got[0]["full_answer"] != true {
			t.Fatalf("expected the background continuation's final answer, got %v", got)
		}
	})
}

func TestSourceMainReplyMatchesWakeForAHeldRun(t *testing.T) {
	items := []proto.Item{
		{Seq: 2, Kind: proto.KindToolUse, ToolUseID: "background_call"},
		{Seq: 3, Role: "assistant", Kind: proto.KindText, Text: "Still running."},
		{ID: "interim", Seq: 4, Kind: turnDurationKind},
	}
	if reply, wait := sourceMainReplyItems(items, 4, false); len(reply) != 0 || wait != channel.HeldMost {
		t.Fatalf("the held turn must wait for background work, got %v with wait %s", reply, wait)
	}
	items = append(items,
		proto.Item{Seq: 5, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{ToolUseID: "background_call", Status: "completed"}},
		proto.Item{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "Work finished."},
		proto.Item{ID: "final", Seq: 7, Kind: turnDurationKind},
	)
	for _, heldThrough := range []int{4, -1} {
		if reply, wait := sourceMainReplyItems(items, heldThrough, false); len(reply) != 1 || reply[0].Text != "Work finished." || wait != 0 {
			t.Fatalf("the matching wake supplies the final reply with heldThrough %d, got %v with wait %s", heldThrough, reply, wait)
		}
	}
}

func TestSourceReportImmediateAnswerWaitsForLateHandoff(t *testing.T) {
	needsLarkLayer(t)
	forStores(t, func(t *testing.T, e *askEnv, _ *db.DB) {
		minutes := 0
		e.fullReply, e.reportDelay = true, &minutes
		e.ask(ownerAsk("om_late_handoff", "/ai Check the release"))
		todo := onlyTodo(e.s)
		run, _ := e.s.channelRunByID("fb_" + todo.Source.Handoff)
		if run == nil {
			t.Fatal("missing handoff run")
		}
		// The transcript can lag while the run is absent, for example after it folds
		// into another channel run. The todo still has its original client message ID.
		e.s.chans.mu.Lock()
		e.s.chans.st.Runs = slices.DeleteFunc(e.s.chans.st.Runs, func(candidate *channel.Run) bool { return candidate == run })
		e.s.chans.mu.Unlock()
		e.f.setItems(
			proto.Item{Role: "user", Kind: proto.KindText, Text: "Earlier request"},
			proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Earlier answer"},
			proto.Item{ID: "earlier_end", Kind: turnDurationKind},
		)
		e.closeDone(todo.ID)
		e.clock.advance(0)
		if src := onlyTodo(e.s).Source; src.ReportDueAt != "" {
			t.Fatalf("planned a fallback before the prompt arrived: %q", src.ReportText)
		}
		const answer = "The release check passed."
		e.f.setItems(
			proto.Item{Role: "user", Kind: proto.KindText, ClientMsgID: channel.ClientMsgID(channel.Feishu, todo.Source.Handoff)},
			proto.Item{Role: "assistant", Kind: proto.KindText, Text: answer},
			proto.Item{ID: "final_end", Kind: turnDurationKind},
		)
		e.clock.advance(5 * time.Second)
		if got := reportsTo(e.f, askSrc, "om_late_handoff"); len(got) != 1 || got[0]["text"] != answer || got[0]["full_answer"] != true {
			t.Fatalf("expected the handoff's final answer, got %v", got)
		}
	})
}

// backgroundHandoff is a handoff's turn that leaves a background check running, as a Claude Code
// transcript has it: the call's result is a user line.
func backgroundHandoff(handoff string) []proto.Item {
	return []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: channel.ClientMsgID(channel.Feishu, handoff)},
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_check"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_check", Text: "Command running in background with ID: check1."},
		{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "The check is still running."},
		{ID: "interim_end", Seq: 5, Kind: turnDurationKind},
	}
}

// While background work runs past sourceReportWait, a read that fails must keep waiting instead
// of sending the fallback.
func TestSourceReportBackgroundWaitSurvivesUnclearReads(t *testing.T) {
	needsLarkLayer(t)
	e := newAskEnv(t, nil)
	minutes := 0
	e.fullReply, e.reportDelay = true, &minutes
	e.f.pageTurns = true
	e.ask(ownerAsk("om_long_background", "/ai Check the release"))
	todo := onlyTodo(e.s)
	items := backgroundHandoff(todo.Source.Handoff)
	e.f.setItems(items...)
	e.closeDone(todo.ID)
	e.clock.advance(0)

	// The background work outlives the short wait.
	e.s.todos.mu.Lock()
	old := time.Now().Add(-sourceReportWait - time.Minute)
	e.s.todos.todos[todo.ID].DoneAt = &old
	e.s.todos.mu.Unlock()

	e.f.mu.Lock()
	e.f.itemsFail = 1 // the node does not answer this read
	e.f.mu.Unlock()
	e.clock.advance(5 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_long_background"); len(got) != 0 {
		t.Fatalf("a failed read sent the fallback: %v", got)
	}

	// Another agent reports back meanwhile: a wake, not a request, so the handoff still waits.
	items = append(items,
		proto.Item{Seq: 6, Role: "user", Kind: proto.KindToolResult, ToolUseID: "peer_call", Wake: true, Task: &proto.TaskNote{Kind: "agent", TaskID: "peer", ToolUseID: "peer_call", Status: "completed"}},
		proto.Item{Seq: 7, Role: "assistant", Kind: proto.KindText, Text: "The peer finished."},
		proto.Item{ID: "peer_end", Seq: 8, Kind: turnDurationKind},
	)
	e.f.setItems(items...)
	e.clock.advance(30 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_long_background"); len(got) != 0 {
		t.Fatalf("an unrelated wake ended the wait: %v", got)
	}

	const answer = "The release check passed."
	items = append(items,
		proto.Item{Seq: 9, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
		proto.Item{Seq: 10, Role: "assistant", Kind: proto.KindText, Text: answer},
		proto.Item{ID: "final_end", Seq: 11, Kind: turnDurationKind},
	)
	e.f.setItems(items...)
	e.clock.advance(30 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_long_background"); len(got) != 1 || got[0]["text"] != answer || got[0]["full_answer"] != true {
		t.Fatalf("expected the background work's final answer, got %v", got)
	}
}

// Another request in the shared conversation ends the handoff before its background work reports
// back. The reply is the handoff's own last words, not the later continuation, and it goes out
// even though the requests pushed the handoff's prompt out of the last five turns.
func TestSourceReportEndsAtAnotherRequest(t *testing.T) {
	needsLarkLayer(t)
	e := newAskEnv(t, nil)
	minutes := 0
	e.fullReply, e.reportDelay = true, &minutes
	e.f.pageTurns = true
	e.ask(ownerAsk("om_interrupted", "/ai Check the release"))
	todo := onlyTodo(e.s)
	items := backgroundHandoff(todo.Source.Handoff)
	e.f.setItems(items...)
	e.closeDone(todo.ID)
	e.clock.advance(0)
	if src := onlyTodo(e.s).Source; src.ReportDueAt != "" {
		t.Fatalf("planned a reply while the background work ran: %q", src.ReportText)
	}

	seq := 5
	for i := range 5 {
		seq += 3
		items = append(items,
			proto.Item{Seq: seq - 2, Role: "user", Kind: proto.KindText, Text: fmt.Sprintf("Other request %d", i)},
			proto.Item{Seq: seq - 1, Role: "assistant", Kind: proto.KindText, Text: "Other answer"},
			proto.Item{ID: fmt.Sprintf("other_end_%d", i), Seq: seq, Kind: turnDurationKind},
		)
	}
	items = append(items,
		proto.Item{Seq: seq + 1, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
		proto.Item{Seq: seq + 2, Role: "assistant", Kind: proto.KindText, Text: "The check passed, and here is the other request again."},
		proto.Item{ID: "late_end", Seq: seq + 3, Kind: turnDurationKind},
	)
	e.f.setItems(items...)
	e.clock.advance(5 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_interrupted"); len(got) != 1 || got[0]["text"] != "The check is still running." || got[0]["full_answer"] != true {
		t.Fatalf("expected the handoff's own last words, got %v", got)
	}
}

func TestSourceMainReplyEndsAtAnotherRequest(t *testing.T) {
	launch := []proto.Item{
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_check"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_check", Text: "Command running in background with ID: check1."},
	}
	other := []proto.Item{
		{Seq: 6, Role: "user", Kind: proto.KindText, Text: "Other request"},
		{Seq: 7, Role: "assistant", Kind: proto.KindText, Text: "Other answer"},
		{ID: "other_end", Seq: 8, Kind: turnDurationKind},
	}
	interim := []proto.Item{
		{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "Interim."},
		{ID: "interim_end", Seq: 5, Kind: turnDurationKind},
	}
	wake := []proto.Item{
		{Seq: 9, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
		{Seq: 10, Role: "assistant", Kind: proto.KindText, Text: "Continuation."},
		{ID: "wake_end", Seq: 11, Kind: turnDurationKind},
	}
	cases := []struct {
		name  string
		items []proto.Item
		held  int
		want  string
	}{
		{"the handoff's wake after another request", slices.Concat(launch, interim, other, wake), -1, "Interim."},
		{"background that never reports", slices.Concat(launch, interim, other), -1, "Interim."},
		{"a held run", slices.Concat(launch, interim, other), 5, "Interim."},
		{"a request taken before any words", slices.Concat(launch, other, wake), -1, ""},
		{"a picture sent before any words", slices.Concat(launch, []proto.Item{{Seq: 6, Role: "user", Kind: proto.KindImage}}, interim), -1, ""},
	}
	for _, c := range cases {
		reply, wait := sourceMainReplyItems(c.items, c.held, false)
		var got string
		if len(reply) == 1 {
			got = reply[0].Text
		}
		if len(reply) > 1 || got != c.want || wait != 0 {
			t.Errorf("%s: got %v with wait %s, want %q at once", c.name, reply, wait, c.want)
		}
	}
}

func TestSourceMainReplyReadsToolLines(t *testing.T) {
	// A call's result and the picture it returns are user lines in a transcript, not requests.
	items := []proto.Item{
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "shot"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "shot", Text: "Screenshot taken"},
		{Seq: 4, Role: "user", Kind: proto.KindImage, ToolUseID: "shot"},
		{Seq: 5, Role: "assistant", Kind: proto.KindText, Text: "The page renders."},
		{ID: "end", Seq: 6, Kind: turnDurationKind},
	}
	if reply, wait := sourceMainReplyItems(items, -1, false); len(reply) != 1 || reply[0].Text != "The page renders." || wait != 0 {
		t.Fatalf("tool lines: got %v with wait %s", reply, wait)
	}
	// A Monitor reports events, not an end: the answer does not wait for it.
	items = []proto.Item{
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "watch"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "watch", Text: "Monitor started (task m1, expires in 30m unless stopped)"},
		{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "Watching the deploy."},
		{ID: "end", Seq: 5, Kind: turnDurationKind},
	}
	if reply, wait := sourceMainReplyItems(items, -1, false); len(reply) != 1 || reply[0].Text != "Watching the deploy." || wait != 0 {
		t.Fatalf("a Monitor: got %v with wait %s", reply, wait)
	}
}

func TestSourceMainReplyWaitsForAllItsBackgroundWork(t *testing.T) {
	items := []proto.Item{
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "call_a"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call_a", Text: "Command running in background with ID: a1."},
		{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "call_b"},
		{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call_b", Text: "Command running in background with ID: b1."},
		{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "Both are running."},
		{ID: "first_end", Seq: 7, Kind: turnDurationKind},
		{Seq: 8, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{ToolUseID: "call_a", Status: "completed"}},
		{Seq: 9, Role: "assistant", Kind: proto.KindText, Text: "A passed; B is still running."},
		{ID: "a_end", Seq: 10, Kind: turnDurationKind},
	}
	if reply, wait := sourceMainReplyItems(items, -1, false); len(reply) != 0 || wait != channel.HeldMost {
		t.Fatalf("one of two tasks reported: got %v with wait %s", reply, wait)
	}
	items = append(items,
		proto.Item{Seq: 11, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{ToolUseID: "call_b", Status: "completed"}},
		proto.Item{Seq: 12, Role: "assistant", Kind: proto.KindText, Text: "Both passed."},
		proto.Item{ID: "b_end", Seq: 13, Kind: turnDurationKind},
	)
	if reply, wait := sourceMainReplyItems(items, -1, false); len(reply) != 1 || reply[0].Text != "Both passed." || wait != 0 {
		t.Fatalf("both tasks reported: got %v with wait %s", reply, wait)
	}
}

func TestSourceMainReplyWaitsForItsContinuation(t *testing.T) {
	launch := backgroundHandoff("")[1:]
	wake := proto.Item{Seq: 6, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}}
	read := []proto.Item{
		{Seq: 7, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_read"},
		{Seq: 8, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_read", Text: "Release log"},
	}
	end := proto.Item{ID: "continuation_end", Seq: 10, Kind: turnDurationKind}
	cases := []struct {
		name  string
		items []proto.Item
		held  int
		reply string
		wait  time.Duration
	}{
		{"the continuation is working", slices.Concat(launch, []proto.Item{wake}, read), -1, "", channel.HeldMost},
		{"the continuation is working for a held run", slices.Concat(launch, []proto.Item{wake}, read), 5, "", channel.HeldMost},
		{"the continuation answered", slices.Concat(launch, []proto.Item{wake}, read, []proto.Item{
			{Seq: 9, Role: "assistant", Kind: proto.KindText, Text: "The check passed."}, end}), -1, "The check passed.", 0},
		{"the continuation ended without words", slices.Concat(launch, []proto.Item{wake}, read, []proto.Item{end}), -1, "", sourceReportWait},
		{"the continuation was stopped", slices.Concat(launch, []proto.Item{wake}, read, []proto.Item{
			{Seq: 9, Kind: proto.KindNotice, Text: userStoppedNotice}}), -1, "", 0},
	}
	for _, c := range cases {
		reply, wait := sourceMainReplyItems(c.items, c.held, false)
		var got string
		if len(reply) == 1 {
			got = reply[0].Text
		}
		if len(reply) > 1 || got != c.reply || wait != c.wait {
			t.Errorf("%s: got %v with wait %s, want %q with wait %s", c.name, reply, wait, c.reply, c.wait)
		}
	}
}

// The handoff's background work wakes it after the short wait, and its channel run is gone (it
// can fold into another run). The continuation keeps the long wait until its turn ends.
func TestSourceReportWaitsForTheContinuationTurn(t *testing.T) {
	needsLarkLayer(t)
	e := newAskEnv(t, nil)
	minutes := 0
	e.fullReply, e.reportDelay = true, &minutes
	e.ask(ownerAsk("om_continuation", "/ai Check the release"))
	todo := onlyTodo(e.s)
	run, _ := e.s.channelRunByID("fb_" + todo.Source.Handoff)
	if run == nil {
		t.Fatal("missing handoff run")
	}
	e.s.chans.mu.Lock()
	e.s.chans.st.Runs = slices.DeleteFunc(e.s.chans.st.Runs, func(candidate *channel.Run) bool { return candidate == run })
	e.s.chans.mu.Unlock()
	items := backgroundHandoff(todo.Source.Handoff)
	e.f.setItems(items...)
	e.closeDone(todo.ID)
	e.clock.advance(0)

	e.s.todos.mu.Lock()
	old := time.Now().Add(-sourceReportWait - time.Minute)
	e.s.todos.todos[todo.ID].DoneAt = &old
	e.s.todos.mu.Unlock()

	// The check ends and wakes the handoff; the continuation is still reading its log.
	items = append(items,
		proto.Item{Seq: 6, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
		proto.Item{Seq: 7, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_read"},
		proto.Item{Seq: 8, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_read", Text: "Release log"},
	)
	e.f.setItems(items...)
	e.clock.advance(30 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_continuation"); len(got) != 0 {
		t.Fatalf("the fallback went out while the continuation worked: %v", got)
	}

	const answer = "The release check passed."
	items = append(items,
		proto.Item{Seq: 9, Role: "assistant", Kind: proto.KindText, Text: answer},
		proto.Item{ID: "final_end", Seq: 10, Kind: turnDurationKind},
	)
	e.f.setItems(items...)
	e.clock.advance(30 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_continuation"); len(got) != 1 || got[0]["text"] != answer || got[0]["full_answer"] != true {
		t.Fatalf("expected the continuation's answer, got %v", got)
	}
}

// A headless main conversation (claude -p) writes no turn_duration line: its items end with the
// answer, and the node's session state or what follows says the turn is over.
func TestSourceMainReplyHeadlessTurnEnds(t *testing.T) {
	answer := []proto.Item{
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_weather"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_weather", Text: "5°C, sunny"},
		{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "Beijing is sunny."},
	}
	launch := []proto.Item{
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_check"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_check", Text: "Command running in background with ID: check1."},
		{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "Still running."},
	}
	cases := []struct {
		name  string
		items []proto.Item
		idle  bool
		reply string
		wait  time.Duration
	}{
		{"idle after its words", answer, true, "Beijing is sunny.", 0},
		{"still working", answer, false, "", sourceReportWait},
		{"idle before its words are in", answer[:2], true, "", sourceReportWait},
		{"an unrelated wake follows the answer", slices.Concat(answer, []proto.Item{
			{Seq: 5, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "other", Status: "completed"}},
			{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "Another task finished."},
		}), false, "Beijing is sunny.", 0},
		{"another request follows the answer", slices.Concat(answer, []proto.Item{
			{Seq: 5, Role: "user", Kind: proto.KindText, Text: "Other request"},
			{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "Other answer"},
		}), true, "Beijing is sunny.", 0},
		{"a message queued mid-turn does not end it", []proto.Item{
			{Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "Interim."},
			{Seq: 3, Role: "user", Kind: proto.KindText, Text: "Also this", Queued: true},
			{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "Both done."},
		}, true, "", 0},
		{"its background work still runs", launch, true, "", channel.HeldMost},
		{"the continuation is working", slices.Concat(launch, []proto.Item{
			{Seq: 5, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
			{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "The check passed."},
		}), false, "", channel.HeldMost},
		{"the continuation is idle", slices.Concat(launch, []proto.Item{
			{Seq: 5, Kind: proto.KindNotice, Wake: true, Task: &proto.TaskNote{TaskID: "check1", Status: "completed"}},
			{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "The check passed."},
		}), true, "The check passed.", 0},
	}
	for _, c := range cases {
		reply, wait := sourceMainReplyItems(c.items, -1, c.idle)
		var got string
		if len(reply) == 1 {
			got = reply[0].Text
		}
		if len(reply) > 1 || got != c.reply || wait != c.wait {
			t.Errorf("%s: got %v with wait %s, want %q with wait %s", c.name, reply, wait, c.reply, c.wait)
		}
	}
}

// The main conversation runs headless: its transcript has no turn end. The answer goes out once
// the node lists the conversation as done, not at the fallback three minutes after the close.
func TestSourceReportHeadlessMainAnswer(t *testing.T) {
	needsLarkLayer(t)
	e := newAskEnv(t, nil)
	minutes := 0
	e.fullReply, e.reportDelay = true, &minutes
	e.ask(ownerAsk("om_headless", "/ai What is the weather?"))
	todo := onlyTodo(e.s)
	mainIs := func(status, state string) {
		e.sessions("server",
			proto.Session{SessionID: "mask", Assistant: true, Status: status, State: state},
			proto.Session{SessionID: "c1x1x1", Assistant: true})
	}
	mainIs(proto.StatusWorking, proto.StateWorking)
	items := []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: channel.ClientMsgID(channel.Feishu, todo.Source.Handoff)},
		{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool_done"},
		{Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool_done", Text: "Completed"},
	}
	e.f.setItems(items...)
	e.closeDone(todo.ID)
	e.clock.advance(0)

	const answer = "Beijing is sunny today."
	items = append(items, proto.Item{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: answer})
	e.f.setItems(items...)
	e.clock.advance(5 * time.Second)
	if src := onlyTodo(e.s).Source; src.ReportDueAt != "" {
		t.Fatalf("planned a reply while the conversation was still working: %q", src.ReportText)
	}

	mainIs(proto.StatusDone, proto.StateIdle)
	e.clock.advance(5 * time.Second)
	if got := reportsTo(e.f, askSrc, "om_headless"); len(got) != 1 || got[0]["text"] != answer || got[0]["full_answer"] != true {
		t.Fatalf("expected the headless turn's answer, got %v", got)
	}
}

func TestSourceAnswerPollBacksOff(t *testing.T) {
	if got := sourceAnswerPoll(time.Minute); got != 5*time.Second {
		t.Fatalf("early poll %s", got)
	}
	if got := sourceAnswerPoll(sourceReportWait); got != 30*time.Second {
		t.Fatalf("late poll %s", got)
	}
}

func TestSourceReportDoesNotShareAnUnrelatedTurn(t *testing.T) {
	needsLarkLayer(t)
	e := newAskEnv(t, nil)
	minutes := 0
	e.fullReply, e.reportDelay = true, &minutes
	e.ask(ownerAsk("om_safe_result", "/ai What shipped?"))
	todo := onlyTodo(e.s)
	at := time.Now().UTC()
	e.f.setItems(
		proto.Item{Role: "user", Kind: proto.KindText, TS: at, Text: "What shipped?", ClientMsgID: channel.ClientMsgID(channel.Feishu, todo.Source.Handoff)},
		proto.Item{Role: "user", Kind: proto.KindText, TS: at, Text: "Unrelated request"},
		proto.Item{Role: "assistant", Kind: proto.KindText, TS: at, Text: "Unrelated answer"},
		proto.Item{ID: "end", Kind: "turn_duration", TS: at},
	)
	e.closeDone(todo.ID)
	e.clock.advance(0)
	if got := reportsTo(e.f, askSrc, "om_safe_result"); len(got) != 1 || got[0]["text"] != e.said("source_report_no_final") {
		t.Fatalf("expected the safe outcome fallback, got %v", got)
	}
}

func TestSourceReportFullPreviewCanBeCancelled(t *testing.T) {
	needsLarkLayer(t)
	e := newAskEnv(t, nil)
	e.fullReply = true
	e.ask(ownerAsk("om_cancellable_result", "/ai What shipped?"))
	todo := onlyTodo(e.s)
	at := time.Now().UTC()
	e.f.setItems(
		proto.Item{Role: "user", Kind: proto.KindText, TS: at, Text: "What shipped?", ClientMsgID: channel.ClientMsgID(channel.Feishu, todo.Source.Handoff)},
		proto.Item{Role: "assistant", Kind: proto.KindText, TS: at, Text: "A guide shipped."},
		proto.Item{ID: "end", Kind: "turn_duration", TS: at},
	)
	e.closeDone(todo.ID)
	e.deliver(topicEventIn("om_cancel_result", askHome, "omt_home", "/noreply"))
	note := lastNote(t, e.f)
	if !strings.Contains(note, e.said("source_noreply_done")) {
		t.Fatalf("cancel reply %q", note)
	}
	e.clock.advance(sourceReportDelay)
	if got := reportsTo(e.f, askSrc, "om_cancellable_result"); len(got) != 0 {
		t.Fatalf("the cancelled reply went out: %v", got)
	}
}
