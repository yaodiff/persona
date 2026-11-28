package node

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func agyBackgroundLogURI(path string) string {
	return (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}).String()
}

func agyBackgroundStep(index int, status, started string, task int, description, logURI string) string {
	content := fmt.Sprintf("Created At: %s\n%s %s/task-%d\nTask Description: %s", started, agyTaskPrefix, agyTestID, task, description)
	if logURI != "" {
		content += "\nTask logs are available at: " + logURI
	}
	return fmt.Sprintf(`{"step_index":%d,"type":"GENERIC","status":%q,"created_at":%q,"content":%q}`+"\n", index, status, started, content)
}

func TestAgyBackgroundLifecycle(t *testing.T) {
	n := &Node{home: t.TempDir()}
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	dir := n.agyDataRoot()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	finish := start.Add(time.Minute)
	started, ended := start.Format(time.RFC3339), finish.Format(time.RFC3339)
	logPath := filepath.Join(dir, "brain", agyTestID, ".system_generated", "tasks", "task-1.log")
	logURI := agyBackgroundLogURI(logPath)
	planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"read_file"},{"name":"run_command","args":{"CommandLine":"\"echo ready\""}},{"name":"schedule"},{"name":"run_workflow"},{"name":"invoke_subagent"}]}`+"\n", started)
	// The command's result arrives after its sibling's result.
	file := agyTestFile(t, dir, agyTestID, planner+
		agyBackgroundStep(3, "RUNNING", started, 2, "Cron: */30 * * * * Check status", "")+
		agyBackgroundStep(2, "RUNNING", started, 1, "Wait for ready", logURI)+
		agyBackgroundStep(4, "RUNNING", started, 3, "Run checks", "file:///unrelated/task-3.log")+
		agyBackgroundStep(5, "RUNNING", started, 4, "Review changes", ""))
	row := proto.Session{Status: proto.StatusIdle}
	n.mergeAgyTranscript(&row, agyTestID)
	if len(row.Background) != 4 || len(row.BackgroundEnded()) != 0 || row.Loop != nil {
		t.Fatalf("unexpected background state: %+v / %+v", row.Background, row.BackgroundEnded())
	}
	for i, kind := range []string{"shell", "monitor", "workflow", "agent"} {
		task := row.Background[i]
		if task.ID != fmt.Sprintf("%s/task-%d", agyTestID, i+1) || task.Kind != kind || !task.StartedAt.Equal(start) {
			t.Fatalf("task %d: %+v", i, task)
		}
	}
	if row.Background[0].Command != "echo ready" || row.Background[0].OutputFile != logPath || row.Background[1].Description != "Cron: */30 * * * * Check status" || row.Background[2].OutputFile != "" {
		t.Fatalf("lost or misattributed task metadata: %+v", row.Background)
	}

	agyTestAppend(t, file, fmt.Sprintf(`{"step_index":6,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", ended,
		fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] taskId=%[1]s/task-1 message=Task id \"%[1]s/task-1\" finished with result: Ready\n</SYSTEM_MESSAGE>", agyTestID)))
	agyTestAppend(t, file, fmt.Sprintf(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_task","args":{"Action":"\"kill\"","TaskId":"\"%s/task-2\""}},{"name":"manage_task","args":{"Action":"status","TaskId":"%s/task-4"}}]}`+"\n", ended, agyTestID, agyTestID))
	agyTestAppend(t, file, fmt.Sprintf(`{"step_index":4,"type":"GENERIC","status":"ERROR","created_at":%q,"updated_at":%q,"content":"Workflow failed"}`+"\n", started, ended))
	n.mergeAgyTranscript(&row, agyTestID)
	if len(row.Background) != 1 || row.Background[0].Kind != "agent" {
		t.Fatalf("running tasks: %+v", row.Background)
	}
	outcomes := row.BackgroundEnded()
	if len(outcomes) != 3 {
		t.Fatalf("ended tasks: %+v", outcomes)
	}
	for i, status := range []string{"completed", "killed", "failed"} {
		if outcomes[i].Status != status || !outcomes[i].EndedAt.Equal(finish) {
			t.Fatalf("outcome %d: %+v", i, outcomes[i])
		}
	}
	if outcomes[0].OutputFile != logPath || outcomes[2].Description != "Run checks" {
		t.Fatalf("lost outcome metadata: %+v", outcomes)
	}

	for _, status := range []string{proto.StatusOffline, proto.StatusInterrupted} {
		row.Status = status
		n.mergeAgyTranscript(&row, agyTestID)
		if len(row.Background) != 0 || len(row.BackgroundEnded()) != 3 {
			t.Fatalf("stopped row %s: %+v / %+v", status, row.Background, row.BackgroundEnded())
		}
	}
	row.Status = proto.StatusIdle
	agyTestAppend(t, file, fmt.Sprintf(`{"step_index":8,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", ended,
		fmt.Sprintf(`Task id "%s/task-4" was canceled.`, agyTestID)))
	n.mergeAgyTranscript(&row, agyTestID)
	if outcomes := row.BackgroundEnded(); len(row.Background) != 0 || len(outcomes) != 4 || outcomes[3].Status != "killed" {
		t.Fatalf("canceled subagent: %+v / %+v", row.Background, outcomes)
	}

	old := time.Now().UTC().Add(-2 * workflowKeep).Format(time.RFC3339)
	agyTestWrite(t, file, agyBackgroundStep(0, "RUNNING", old, 1, "Old task", "")+
		fmt.Sprintf(`{"step_index":1,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", old,
			fmt.Sprintf(`Task id "%s/task-1" was canceled.`, agyTestID)))
	n.mergeAgyTranscript(&row, agyTestID)
	if len(row.Background) != 0 || len(row.BackgroundEnded()) != 0 {
		t.Fatalf("old outcome survived: %+v / %+v", row.Background, row.BackgroundEnded())
	}
}

func TestAgyBackgroundScheduleMessages(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const fired = "2026-06-01T10:01:01Z"
	const id = agyTestID + "/task-1"
	for _, tc := range []struct {
		name, args, description, sender, message string
		completed                                bool
	}{
		{"timer duration", `{"DurationSeconds":60}`, "Check status", id, "Check again", true},
		{"timer description", `{}`, "Timer: 60s", id, "Check again", true},
		{"cron expression", `{"CronExpression":"0 * * * *"}`, "Timer: 60s", id, "Check again", false},
		{"cron description", `{"DurationSeconds":"60"}`, "Cron: 0 * * * *", id, "Check again", false},
		{"cron iteration", `{"DurationSeconds":"60"}`, "Timer: 60s", id, "Check again (iteration 1)", false},
		{"timer prompt containing still running", `{"DurationSeconds":"60"}`, "Timer: 60s", id, "Check build progress if still running", true},
		{"heartbeat", `{"DurationSeconds":"60"}`, "Timer: 60s", id, "Task is still running", false},
		{"heartbeat with notice prefix", `{"DurationSeconds":"60"}`, "Timer: 60s", id, "[Notice] Task is still running", false},
		{"heartbeat with task id", `{"DurationSeconds":"60"}`, "Timer: 60s", id, `Task id "task-3" is still running`, false},
		{"different sender", `{}`, "Timer: 60s", id + "0", "Check again", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"schedule","args":%s}]}`+"\n", tc.args)
			message := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s priority=MESSAGE_PRIORITY_HIGH content=%s\n</SYSTEM_MESSAGE>", started, tc.sender, tc.message)
			notice := fmt.Sprintf(`{"step_index":2,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", fired, message)
			steps, _ := parseAgySteps([]byte(planner+agyBackgroundStep(1, "RUNNING", started, 1, tc.description, "")+notice), agyTestID)
			tasks := agyBackground(steps, agyTestID, "", nil)
			if tc.completed {
				if len(tasks.running) != 0 || len(tasks.ended) != 1 {
					t.Fatalf("expected one ended timer: %+v", tasks)
				}
				ended := tasks.ended[0]
				if ended.ID != id || ended.Status != proto.WorkflowCompleted || ended.EndedAt.Format(time.RFC3339) != fired {
					t.Fatalf("unexpected timer outcome: %+v", ended)
				}
			} else if len(tasks.running) != 1 || tasks.running[0].ID != id || len(tasks.ended) != 0 {
				t.Fatalf("expected the task to keep running: %+v", tasks)
			}
		})
	}
}

func TestAgyBackgroundServerRestartNotice(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const completed = "2026-06-01T10:01:00Z"
	const restarted = "2026-06-01T10:05:00Z"
	const resumed = "2026-06-01T10:06:00Z"
	const secondRestart = "2026-06-01T10:10:00Z"
	const afterSecond = "2026-06-01T10:11:00Z"

	t.Run("stops prior tasks and preserves already ended and later tasks", func(t *testing.T) {
		plannerBefore := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"run_command","args":{"CommandLine":"npm test"}},{"name":"schedule","args":{"CronExpression":"*/5 * * * *"}},{"name":"run_workflow"}]}`+"\n", started)
		doneStep := strings.TrimSuffix(agyBackgroundStep(3, "DONE", started, 3, "Already completed", ""), "}\n") + fmt.Sprintf(`,"updated_at":%q}`+"\n", completed)
		noticeContent := fmt.Sprintf("The following is a <SYSTEM_MESSAGE> not actually sent by the user.\n\n<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>", restarted)
		restartStep := fmt.Sprintf(`{"step_index":4,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restarted, noticeContent)
		plannerAfter := fmt.Sprintf(`{"step_index":5,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"run_command","args":{"CommandLine":"npm run build"}}]}`+"\n", resumed)
		transcript := plannerBefore +
			agyBackgroundStep(1, "RUNNING", started, 1, "Run tests", "") +
			agyBackgroundStep(2, "RUNNING", started, 2, "Cron: */5 * * * * Poll", "") +
			doneStep + restartStep + plannerAfter +
			agyBackgroundStep(6, "RUNNING", resumed, 4, "Build after restart", "")
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 1 || tasks.running[0].ID != agyTestID+"/task-4" {
			t.Fatalf("expected only post-restart task running: %+v", tasks.running)
		}
		if len(tasks.ended) != 3 {
			t.Fatalf("expected 3 ended tasks: %+v", tasks.ended)
		}
		want := []struct{ id, status, at string }{
			{agyTestID + "/task-3", proto.WorkflowCompleted, completed},
			{agyTestID + "/task-1", proto.WorkflowKilled, restarted},
			{agyTestID + "/task-2", proto.WorkflowKilled, restarted},
		}
		for i, w := range want {
			got := tasks.ended[i]
			if got.ID != w.id || got.Status != w.status || got.EndedAt.Format(time.RFC3339) != w.at {
				t.Fatalf("ended[%d] = %+v, want %+v", i, got, w)
			}
		}
	})

	t.Run("handles bare notice and message timestamp fallback", func(t *testing.T) {
		bare := "The following is a <SYSTEM_MESSAGE> not actually sent by the user.\n\n<SYSTEM_MESSAGE>\n[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>"
		steps, _ := parseAgySteps([]byte(agyBackgroundStep(0, "RUNNING", started, 1, "Prior task", "")+
			fmt.Sprintf(`{"step_index":1,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restarted, bare)), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 0 || len(tasks.ended) != 1 || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != restarted {
			t.Fatalf("unexpected bare restart outcome: %+v", tasks)
		}

		wrapped := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>", restarted)
		steps, _ = parseAgySteps([]byte(agyBackgroundStep(0, "RUNNING", started, 1, "Prior task", "")+
			fmt.Sprintf(`{"step_index":1,"type":"SYSTEM_MESSAGE","content":%q}`+"\n", wrapped)), agyTestID)
		tasks = agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 0 || len(tasks.ended) != 1 || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != restarted {
			t.Fatalf("unexpected fallback timestamp outcome: %+v", tasks)
		}
	})

	t.Run("same timestamp order and multiple restarts", func(t *testing.T) {
		msg := func(ts string) string {
			return fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>", ts)
		}
		transcript := agyBackgroundStep(0, "RUNNING", restarted, 1, "Before first restart", "") +
			fmt.Sprintf(`{"step_index":1,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restarted, msg(restarted)) +
			agyBackgroundStep(2, "RUNNING", restarted, 2, "After first restart", "") +
			fmt.Sprintf(`{"step_index":3,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", secondRestart, msg(secondRestart)) +
			agyBackgroundStep(4, "RUNNING", afterSecond, 3, "After second restart", "")
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 1 || tasks.running[0].ID != agyTestID+"/task-3" {
			t.Fatalf("expected only task-3 running: %+v", tasks.running)
		}
		if len(tasks.ended) != 2 ||
			tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != restarted ||
			tasks.ended[1].ID != agyTestID+"/task-2" || tasks.ended[1].Status != proto.WorkflowKilled || tasks.ended[1].EndedAt.Format(time.RFC3339) != secondRestart {
			t.Fatalf("unexpected multi-restart outcomes: %+v", tasks.ended)
		}
	})

	t.Run("ignores non-system sender and quoted subagent idle report", func(t *testing.T) {
		fromTask := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s/task-1 priority=MESSAGE_PRIORITY_HIGH content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>", restarted, agyTestID)
		fromSubagent := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=system priority=MESSAGE_PRIORITY_HIGH content=Subagent 00000000-0000-4000-8000-000000000002 has gone idle. This was the subagent's most recent response: All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>", restarted)
		transcript := agyBackgroundStep(0, "RUNNING", started, 1, "Echo test output", "") +
			agyBackgroundStep(1, "RUNNING", started, 2, "Still alive", "") +
			fmt.Sprintf(`{"step_index":2,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restarted, fromTask) +
			fmt.Sprintf(`{"step_index":3,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restarted, fromSubagent)
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 2 || len(tasks.ended) != 0 {
			t.Fatalf("false-positive restart notice killed running tasks: %+v", tasks)
		}
	})
}

func TestAgyBackgroundTaskListReconciliation(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const listed = "2026-06-01T10:00:02Z"
	const later = "2026-06-01T10:00:03Z"
	const empty = "No background tasks are currently running."
	const partial = "You have 1 background task(s) currently running:\n- Check status (`task-2`)"
	const full = "You have 2 background task(s) currently running:\n- Wait for input (`" + agyTestID + "/task-1`)\n- Check status (`task-2`)"
	for _, tc := range []struct {
		name, action, status, content, errField string
		truncated, plannerTruncated             bool
		wantEnded                               int
	}{
		{"empty inventory with Output header", `"list"`, "DONE", "Created At: " + listed + "\nCompleted At: " + listed + "\nOutput:\n" + empty, "", false, false, 2},
		{"empty inventory without Output header", `"list"`, "DONE", "Created At: " + listed + "\nCompleted At: " + listed + "\n" + empty, "", false, false, 2},
		{"listed task stays running", `"\"list\""`, "DONE", "Created At: " + listed + "\nCompleted At: " + listed + "\nOutput:\n" + partial, "", false, false, 1},
		{"all tasks listed", `"list"`, "DONE", "Created At: " + listed + "\nCompleted At: " + listed + "\nOutput:\n" + full, "", false, false, 0},
		{"status is not a list", `"status"`, "DONE", empty, "", false, false, 0},
		{"failed list status", `"list"`, "ERROR", empty, "", false, false, 0},
		{"failed list error field", `"list"`, "DONE", empty, "tool failed", false, false, 0},
		{"incomplete list content", `"list"`, "DONE", partial, "", true, false, 0},
		{"truncated planner tool_calls", `"list"`, "DONE", empty, "", false, true, 0},
		{"missing row", `"list"`, "DONE", strings.Replace(partial, "have 1", "have 2", 1), "", false, false, 0},
		{"duplicate row", `"list"`, "DONE", partial + "\n- Check status (`task-2`)", "", false, false, 0},
		{"foreign inventory", `"list"`, "DONE", strings.Replace(partial, "task-2", "another-conversation/task-2", 1), "", false, false, 0},
		{"mixed inventory", `"list"`, "DONE", strings.Replace(full, "(`task-2`)", "(`another-conversation/task-2`)", 1), "", false, false, 0},
		{"filtered inventory", `"list","TaskId":"task-2"`, "DONE", partial, "", false, false, 0},
		{"unknown inventory selector", `"list","Scope":"current-turn"`, "DONE", empty, "", false, false, 0},
		{"empty task selector", `"list","TaskId":""`, "DONE", empty, "", false, false, 2},
		{"null task selector", `"list","TaskId":null`, "DONE", empty, "", false, false, 2},
		{"unknown output", `"list"`, "DONE", "Unable to list tasks", "", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var plannerFields string
			if tc.plannerTruncated {
				plannerFields = `,"truncated_fields":["tool_calls"]`
			}
			planner := fmt.Sprintf(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"run_command"},{"name":"manage_task","args":{"Action":%s}}]%s}`+"\n", listed, tc.action, plannerFields)
			var fields string
			if tc.truncated {
				fields += `,"truncated_fields":["content"]`
			}
			if tc.errField != "" {
				fields += fmt.Sprintf(`,"error":%q`, tc.errField)
			}
			result := fmt.Sprintf(`{"step_index":4,"type":"GENERIC","status":%q,"created_at":%q,"content":%q%s}`+"\n", tc.status, listed, tc.content, fields)
			steps, _ := parseAgySteps([]byte(agyBackgroundStep(0, "RUNNING", started, 1, "Wait for input", "")+
				agyBackgroundStep(1, "RUNNING", started, 2, "Check status", "")+planner+result+
				agyBackgroundStep(3, "RUNNING", later, 3, "Started after list", "")), agyTestID)
			tasks := agyBackground(steps, agyTestID, "", nil)
			if len(tasks.ended) != tc.wantEnded || len(tasks.running) != 3-tc.wantEnded || tasks.running[len(tasks.running)-1].ID != agyTestID+"/task-3" {
				t.Fatalf("unexpected inventory reconciliation: %+v", tasks)
			}
			for i, ended := range tasks.ended {
				if ended.ID != fmt.Sprintf("%s/task-%d", agyTestID, i+1) || ended.Status != proto.WorkflowCompleted || ended.EndedAt.Format(time.RFC3339) != listed {
					t.Fatalf("unexpected outcome: %+v", ended)
				}
			}
		})
	}
}

func TestAgyBackgroundTaskListOwnership(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const first = "2026-06-01T10:00:01Z"
	const second = "2026-06-01T10:00:02Z"
	const written = "2026-06-01T10:00:03Z"
	const empty = "No background tasks are currently running."
	const present = "You have 1 background task(s) currently running:\n- Wait for input (`task-1`)"
	const listCall = `{"name":"manage_task","args":{"Action":"list"}}`
	planner := func(index int, at, calls string) string {
		return fmt.Sprintf(`{"step_index":%d,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[%s]}`+"\n", index, at, calls)
	}
	result := func(index int, at, output string) string {
		return fmt.Sprintf(`{"step_index":%d,"type":"GENERIC","status":"DONE","created_at":%q,"content":%q}`+"\n", index, at, output)
	}
	for _, tc := range []struct {
		name, transcript, endedAt string
	}{
		{"late result keeps its native slot",
			planner(1, first, listCall) + planner(3, second, listCall) + result(2, written, empty), first},
		{"late result after user input",
			planner(1, first, listCall) + `{"step_index":3,"type":"USER_INPUT"}` + "\n" + result(2, written, empty), first},
		{"old result in newer planner slot",
			planner(1, first, listCall) + planner(3, written, listCall) + result(4, second, empty), ""},
		{"old tool timestamp in newer planner slot",
			planner(1, first, listCall) + planner(3, second, listCall) +
				result(4, written, "Created At: "+first+"\nCompleted At: "+written+"\nOutput:\n"+empty), ""},
		{"overlapping planner slots",
			planner(1, first, `{"name":"run_command"},`+listCall) + planner(2, second, listCall) + result(3, written, empty), ""},
		{"newer snapshot wins over later export",
			planner(1, first, listCall) + planner(3, second, listCall) + result(4, second, present) + result(2, written, empty), ""},
		{"repeated omission preserves first completion",
			planner(1, first, listCall) + planner(3, second, listCall) + result(4, second, empty) + result(2, written, empty), first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcript := agyBackgroundStep(0, "RUNNING", started, 1, "Wait for input", "") + tc.transcript
			steps, _ := parseAgySteps([]byte(transcript), agyTestID)
			tasks := agyBackground(steps, agyTestID, "", nil)
			if tc.endedAt == "" {
				if len(tasks.running) != 1 || len(tasks.ended) != 0 {
					t.Fatalf("expected the task to keep running: %+v", tasks)
				}
			} else if len(tasks.running) != 0 || len(tasks.ended) != 1 ||
				tasks.ended[0].Status != proto.WorkflowCompleted || tasks.ended[0].EndedAt.Format(time.RFC3339Nano) != tc.endedAt {
				t.Fatalf("expected the task to end at its own list call (%s): %+v", tc.endedAt, tasks)
			}
		})
	}
}

func TestAgyBackgroundTaskListUsesCallTimeAndLatestTaskUpdate(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const called = "2026-06-01T10:00:01Z"
	const changed = "2026-06-01T10:00:02Z"
	const written = "2026-06-01T10:00:03Z"
	planner := fmt.Sprintf(`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"run_command"},{"name":"manage_task","args":{"Action":"list"}}]}`+"\n", called)
	result := fmt.Sprintf(`{"step_index":5,"type":"GENERIC","status":"DONE","created_at":%q,"content":"No background tasks are currently running."}`+"\n", written)
	older := agyBackgroundStep(1, "RUNNING", started, 2, "Task with later update", "")
	update := strings.TrimSuffix(older, "}\n") + fmt.Sprintf(`,"updated_at":%q}`+"\n", changed)
	heartbeat := fmt.Sprintf("[Message] timestamp=%s sender=%s/task-3 priority=MESSAGE_PRIORITY_HIGH content=Task is still running", changed, agyTestID)
	message := fmt.Sprintf(`{"step_index":6,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", changed, heartbeat)
	transcript := agyBackgroundStep(0, "RUNNING", started, 1, "Omitted older task", "") + older +
		agyBackgroundStep(2, "RUNNING", started, 3, "Task with later heartbeat", "") + planner +
		agyBackgroundStep(4, "RUNNING", changed, 4, "Started after the list call", "") + result + message + update
	steps, _ := parseAgySteps([]byte(transcript), agyTestID)
	tasks := agyBackground(steps, agyTestID, "", nil)
	if len(tasks.ended) != 1 || tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowCompleted ||
		tasks.ended[0].EndedAt.Format(time.RFC3339Nano) != called {
		t.Fatalf("expected only the older, unchanged task to end at the list call: %+v", tasks)
	}
	if len(tasks.running) != 3 || tasks.running[0].ID != agyTestID+"/task-2" ||
		tasks.running[1].ID != agyTestID+"/task-3" || tasks.running[2].ID != agyTestID+"/task-4" {
		t.Fatalf("expected all tasks seen after the list call to stay running: %+v", tasks.running)
	}
}

func TestAgyBackgroundRestartAndTaskListOrdering(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const listTime = "2026-06-01T10:02:00Z"
	const afterList = "2026-06-01T10:03:00Z"
	const restartTime = "2026-06-01T10:05:00Z"
	const laterListTime = "2026-06-01T10:06:00Z"
	const listCall = `{"name":"manage_task","args":{"Action":"list"}}`
	restartMsg := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>", restartTime)

	t.Run("list omission before restart keeps completed status for earlier task", func(t *testing.T) {
		transcript := agyBackgroundStep(0, "RUNNING", started, 1, "Gone before list", "") +
			fmt.Sprintf(`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[%s]}`+"\n", listTime, listCall) +
			fmt.Sprintf(`{"step_index":2,"type":"GENERIC","status":"DONE","created_at":%q,"content":"No background tasks are currently running."}`+"\n", listTime) +
			agyBackgroundStep(3, "RUNNING", afterList, 2, "Running at restart", "") +
			fmt.Sprintf(`{"step_index":4,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restartTime, restartMsg)
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 0 || len(tasks.ended) != 2 {
			t.Fatalf("unexpected tasks: %+v", tasks)
		}
		if tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowCompleted || tasks.ended[0].EndedAt.Format(time.RFC3339) != listTime ||
			tasks.ended[1].ID != agyTestID+"/task-2" || tasks.ended[1].Status != proto.WorkflowKilled || tasks.ended[1].EndedAt.Format(time.RFC3339) != restartTime {
			t.Fatalf("unexpected outcomes: %+v", tasks.ended)
		}
	})

	t.Run("restart before empty list preserves killed status at restart time", func(t *testing.T) {
		transcript := agyBackgroundStep(0, "RUNNING", started, 1, "Killed by restart", "") +
			fmt.Sprintf(`{"step_index":1,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", restartTime, restartMsg) +
			fmt.Sprintf(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[%s]}`+"\n", laterListTime, listCall) +
			fmt.Sprintf(`{"step_index":3,"type":"GENERIC","status":"DONE","created_at":%q,"content":"No background tasks are currently running."}`+"\n", laterListTime)
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 0 || len(tasks.ended) != 1 {
			t.Fatalf("unexpected tasks: %+v", tasks)
		}
		if tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != restartTime {
			t.Fatalf("unexpected outcome: %+v", tasks.ended[0])
		}
	})
}

func TestAgyBackgroundSessionListMerge(t *testing.T) {
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	dir := n.agyDataRoot()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	logPath := filepath.Join(dir, "brain", agyTestID, ".system_generated", "tasks", "task-1.log")
	logURI := agyBackgroundLogURI(logPath)
	agyTestFile(t, dir, agyTestID, `{"step_index":0,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"schedule"}]}`+"\n"+
		agyBackgroundStep(1, "RUNNING", stamp, 1, "Cron: */30 * * * * Check status", logURI))

	const sid = "agy-" + agyTestID
	n.runtimes = newRuntimeRegistry()
	h := n.runtimes.add(nil)
	h.ready, h.hello.Runtime = true, agyRuntimeID
	runtimeHost := &RuntimeHost{reg: n.runtimes}
	setRuntimeRow := func(status, backgroundField string) {
		t.Helper()
		raw := fmt.Sprintf(`{"sessions":[{"session_id":%q,"cwd":%q,"model":"gemini-test","status":%q,%q:[{"id":%q,"kind":"monitor","output_file":"/unrelated.log"}],"background_ended":[{"id":%q,"kind":"workflow","status":"completed","ended_at":%q,"output_file":"/unrelated.log"}]}]}`,
			sid, home, status, backgroundField, agyTestID+"/task-1", agyTestID+"/task-2", stamp)
		runtimeHost.event(h, "runtime.sessions", json.RawMessage(raw))
	}
	setRuntimeRow(proto.StatusIdle, "background")
	rows := listed(t, n)
	acp := rows[sid]
	if len(acp.Background) != 1 || acp.Background[0].Kind != "monitor" || acp.Background[0].OutputFile != logPath ||
		len(acp.BackgroundEnded()) != 1 || acp.BackgroundEnded()[0].Kind != "workflow" || acp.BackgroundEnded()[0].OutputFile != "" {
		t.Fatalf("ACP fields after normalization: %+v / %+v", acp.Background, acp.BackgroundEnded())
	}

	setRuntimeRow(proto.StatusIdle, "agy_pane_background") // The companion sees a transcript without an ACP process.
	if acp := listed(t, n)[sid]; acp.Model != "gemini-test" || len(acp.Background) != 0 {
		t.Fatalf("history row shows pane-only work: %+v", acp)
	}
	pane := agyTestAgent(agyTestPane, home, agyTestID)
	herdr.reply("session.snapshot", snapshot(nil, nil, []any{pane}, nil))
	rows = listed(t, n)
	row := rows[agyTestRow]
	if _, duplicate := rows[sid]; duplicate || row.Model != "gemini-test" || len(row.Background) != 1 || row.Background[0].Kind != "monitor" || row.Loop != nil {
		t.Fatalf("live pane merge: pane=%+v, duplicate=%v", row, duplicate)
	}
	if row.Background[0].OutputFile != logPath || len(row.BackgroundEnded()) != 1 || row.BackgroundEnded()[0].OutputFile != "" {
		t.Fatalf("pane did not keep projected tasks with verified logs: %+v / %+v", row.Background, row.BackgroundEnded())
	}

	setRuntimeRow(proto.StatusOffline, "background")
	pane["agent_status"] = proto.StatusOffline
	herdr.reply("session.snapshot", snapshot(nil, nil, []any{pane}, nil))
	rows = listed(t, n)
	if len(rows[sid].Background) != 0 || len(rows[agyTestRow].Background) != 0 {
		t.Fatalf("stopped sessions show running work: %+v / %+v", rows[sid].Background, rows[agyTestRow].Background)
	}
}

func TestAgyPaneKeepsCompanionBackgroundAfterRewind(t *testing.T) {
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	dir := n.agyDataRoot()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	kept, ended, abandoned := agyTestID+"/task-1", agyTestID+"/task-2", agyTestID+"/task-3"
	logPath := filepath.Join(dir, "brain", agyTestID, ".system_generated", "tasks", "task-1.log")
	// The compact export still contains a running task and a completion past the native rewind cut.
	agyTestFile(t, dir, agyTestID,
		agyBackgroundStep(0, "RUNNING", stamp, 1, "Kept task", agyBackgroundLogURI(logPath))+
			agyBackgroundStep(1, "DONE", stamp, 2, "Finished before the cut", "")+
			agyBackgroundStep(2, "RUNNING", stamp, 3, "Abandoned task", "")+
			fmt.Sprintf(`{"step_index":3,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", stamp,
				fmt.Sprintf(`Task id "%s" was killed.`, kept)))
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))
	n.runtimes = newRuntimeRegistry()
	h := n.runtimes.add(nil)
	h.ready, h.hello.Runtime = true, agyRuntimeID

	companion := proto.Session{SessionID: agyAgent + "-" + agyTestID, Status: proto.StatusIdle, Cwd: home}
	companion.Background = []proto.BackgroundTask{{ID: kept, Kind: "monitor", StartedAt: now}}
	companion.SetBackgroundEnded([]proto.BackgroundTask{{ID: ended, Kind: "workflow", Status: proto.WorkflowCompleted, EndedAt: &now}})
	n.runtimes.setSessions(h, normalizeRuntimeSessions(h, []proto.Session{companion}))

	row := listed(t, n)[agyTestRow]
	if len(row.Background) != 1 || row.Background[0].ID != kept || row.Background[0].Kind != "monitor" || row.Background[0].OutputFile != logPath {
		t.Errorf("pane should keep the projected running task and attach its local log: %+v", row.Background)
	}
	if outcomes := row.BackgroundEnded(); len(outcomes) != 1 || outcomes[0].ID != ended || outcomes[0].Status != proto.WorkflowCompleted || row.Loop != nil {
		t.Errorf("pane should keep the projected outcomes without adding a loop: %+v / %+v", outcomes, row.Loop)
	}

	companion.Background = nil
	companion.SetBackgroundEnded(nil)
	n.runtimes.setSessions(h, normalizeRuntimeSessions(h, []proto.Session{companion}))
	row = listed(t, n)[agyTestRow]
	if len(row.Background) != 0 || len(row.BackgroundEnded()) != 0 {
		t.Errorf("a full rewind should keep the pane inventory empty: %+v / %+v", row.Background, row.BackgroundEnded())
	}

	n.runtimes.setSessions(h, nil)
	row = listed(t, n)[agyTestRow]
	if len(row.Background) != 1 || row.Background[0].ID != abandoned || len(row.BackgroundEnded()) != 2 {
		t.Errorf("pane without a companion row should still read local tasks: %+v / %+v", row.Background, row.BackgroundEnded())
	}
}

func TestAgyBackgroundTaskLogRejectsEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	path := agyTestFile(t, dir, agyTestID, "")
	logPath := filepath.Join(filepath.Dir(filepath.Dir(path)), "tasks", "task-1.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.log")
	agyTestWrite(t, outside, "unrelated file")
	if err := os.Symlink(outside, logPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	brain, err := agyBrain(dir, agyTestID)
	if err != nil {
		t.Fatal(err)
	}
	defer brain.Close()
	uri := agyBackgroundLogURI(logPath)
	if path := agyTaskLog(uri, dir, agyTestID, "task-1", brain); path != "" {
		t.Fatalf("accepted a symlink outside the conversation: %s", path)
	}
}

func TestAgyBackgroundTimerCondition(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const received = "2026-06-01T10:00:02Z"
	const id = agyTestID + "/task-1"
	const sender = agyTestID + "/task-2"
	const subagent = "22222222-3333-4444-8555-666666666666"
	for _, tc := range []struct {
		name, args, description, msgSender, msgBody, at string
		canceled                                        bool
	}{
		{"any sender task finish", `{"DurationSeconds":180,"TimerCondition":"any"}`, "Timer: 3m", sender, fmt.Sprintf(`Task id "%s" finished with result: ok`, sender), received, true},
		{"any sender JSON-encoded args", `{"DurationSeconds":"180","TimerCondition":"\"any\""}`, "Timer: 3m", sender, "Done", received, true},
		{"any sender report", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", "subagent", "Ready", received, true},
		{"any sender subagent", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", subagent, "Ready", received, true},
		{"specific subagent idle system notice", fmt.Sprintf(`{"DurationSeconds":60,"TimerCondition":%q}`, subagent), "Timer: 60s", "system", fmt.Sprintf("Subagent %s has gone idle. This was the subagent's most recent response: Done", subagent), received, true},
		{"specific full sender", fmt.Sprintf(`{"DurationSeconds":60,"TimerCondition":%q}`, sender), "Timer: 60s", sender, "Ready", received, true},
		{"specific short sender", `{"DurationSeconds":60,"TimerCondition":"task-2"}`, "Timer: 60s", sender, "Ready", received, true},
		{"different sender", fmt.Sprintf(`{"DurationSeconds":60,"TimerCondition":%q}`, sender), "Timer: 60s", agyTestID + "/task-3", "Ready", received, false},
		{"never", `{"DurationSeconds":60,"TimerCondition":"never"}`, "Timer: 60s", sender, "Ready", received, false},
		{"omitted", `{"DurationSeconds":60}`, "Timer: 60s", sender, "Ready", received, false},
		{"before start", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", sender, "Ready", "2026-06-01T09:59:59Z", false},
		{"after deadline", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", sender, "Ready", "2026-06-01T10:01:01Z", false},
		{"own heartbeat", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", id, "Task is still running", received, false},
		{"other task heartbeat", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", sender, `[Notice] Task id "task-2" is still running`, received, false},
		{"watched heartbeat", `{"DurationSeconds":60,"TimerCondition":"task-2"}`, "Timer: 60s", sender, "Task is STILL RUNNING", received, false},
		{"empty message", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", sender, "", received, false},
		{"system update", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", "system", "Reminder: Background tasks are still running", received, false},
		{"watched task notice", `{"DurationSeconds":60,"TimerCondition":"task-2"}`, "Timer: 60s", sender, "[Notice] Task started", received, false},
		{"cron iteration", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", sender, "Poll (iteration 1)", received, false},
		{"cron ignores condition", `{"CronExpression":"*/5 * * * *","TimerCondition":"any"}`, "Cron: */5 * * * *", sender, "Ready", received, false},
		{"cron expression takes precedence over duration", `{"CronExpression":"*/5 * * * *","DurationSeconds":60,"TimerCondition":"any"}`, "Timer: 60s", sender, "Ready", received, false},
		{"cron description takes precedence over duration", `{"DurationSeconds":60,"TimerCondition":"any"}`, "Cron: */5 * * * *", sender, "Ready", received, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"schedule","args":%s}]}`+"\n", started, tc.args)
			message := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s priority=MESSAGE_PRIORITY_HIGH content=%s\n</SYSTEM_MESSAGE>", tc.at, tc.msgSender, tc.msgBody)
			notice := fmt.Sprintf(`{"step_index":2,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", tc.at, message)
			steps, _ := parseAgySteps([]byte(planner+agyBackgroundStep(1, "RUNNING", started, 1, tc.description, "")+notice), agyTestID)
			tasks := agyBackground(steps, agyTestID, "", nil)
			if tc.canceled {
				if len(tasks.running) != 0 || len(tasks.ended) != 1 || tasks.ended[0].ID != id || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != tc.at {
					t.Fatalf("expected canceled timer: %+v", tasks)
				}
			} else if len(tasks.running) != 1 || tasks.running[0].ID != id || len(tasks.ended) != 0 {
				t.Fatalf("expected timer to keep running: %+v", tasks)
			}
		})
	}

	t.Run("later list liveness prevents false condition cancel", func(t *testing.T) {
		const listedAt = "2026-06-01T10:00:05Z"
		planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"schedule","args":{"DurationSeconds":60,"TimerCondition":"any"}}]}`+"\n", started)
		msg := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s priority=MESSAGE_PRIORITY_HIGH content=Ready\n</SYSTEM_MESSAGE>", received, sender)
		listOut := fmt.Sprintf("You have 1 background task(s) currently running:\n[{\"taskId\":%q,\"toolName\":\"schedule\"}]", id)
		transcript := planner + agyBackgroundStep(1, "RUNNING", started, 1, "Timer: 60s", "") +
			fmt.Sprintf(`{"step_index":2,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", received, msg) +
			fmt.Sprintf(`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_task","args":{"Action":"list"}}]}`+"\n", listedAt) +
			fmt.Sprintf(`{"step_index":4,"type":"GENERIC","status":"DONE","created_at":%q,"content":%q}`+"\n", listedAt, listOut)
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 1 || tasks.running[0].ID != id || len(tasks.ended) != 0 {
			t.Fatalf("expected later list proof to keep timer running: %+v", tasks)
		}
	})

	t.Run("out-of-order late timer export still reconciles against later condition message", func(t *testing.T) {
		planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"schedule","args":{"DurationSeconds":60,"TimerCondition":"any"}}]}`+"\n", started)
		msg := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s priority=MESSAGE_PRIORITY_HIGH content=Ready\n</SYSTEM_MESSAGE>", received, sender)
		transcript := planner +
			fmt.Sprintf(`{"step_index":2,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", received, msg) +
			agyBackgroundStep(1, "RUNNING", started, 1, "Timer: 60s", "")
		steps, _ := parseAgySteps([]byte(transcript), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 0 || len(tasks.ended) != 1 || tasks.ended[0].ID != id || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != received {
			t.Fatalf("expected late-exported timer to be canceled: %+v", tasks)
		}
	})
}

func TestAgyBackgroundRestartAndTimerConditionOrdering(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const beforeRestart = "2026-06-01T10:01:00Z"
	const restartTime = "2026-06-01T10:02:00Z"
	const afterRestart = "2026-06-01T10:03:00Z"
	planner := `{"step_index":0,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"schedule","args":{"DurationSeconds":600,"TimerCondition":"early"}},{"name":"schedule","args":{"DurationSeconds":600,"TimerCondition":"late"}}]}` + "\n"
	message := func(index int, at, sender, content string) string {
		body := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s priority=MESSAGE_PRIORITY_HIGH content=%s\n</SYSTEM_MESSAGE>", at, sender, content)
		return fmt.Sprintf(`{"step_index":%d,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", index, at, body)
	}
	transcript := planner + agyBackgroundStep(1, "RUNNING", started, 1, "Timer: 600s", "") +
		agyBackgroundStep(2, "RUNNING", started, 2, "Timer: 600s", "") +
		message(3, beforeRestart, "early", "Ready") +
		message(4, restartTime, "system", "[Notice] All your subagents and background tasks have been stopped due to server restart.") +
		message(5, afterRestart, "late", "Ready")
	steps, _ := parseAgySteps([]byte(transcript), agyTestID)
	now, _ := time.Parse(time.RFC3339, afterRestart)
	tasks := agyBackgroundAt(steps, agyTestID, "", nil, now)
	if len(tasks.running) != 0 || len(tasks.ended) != 2 ||
		tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[0].EndedAt.Format(time.RFC3339) != beforeRestart ||
		tasks.ended[1].ID != agyTestID+"/task-2" || tasks.ended[1].Status != proto.WorkflowKilled || tasks.ended[1].EndedAt.Format(time.RFC3339) != restartTime {
		t.Fatalf("unexpected timer outcomes: %+v", tasks)
	}
}

func TestAgyBackgroundTaskListJSONArray(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const listed = "2026-06-01T10:00:02Z"
	const later = "2026-06-01T10:00:03Z"
	dupHeaders := "Created At: " + listed + "\nCompleted At: " + listed + "\nCreated At: " + listed + "\nCompleted At: " + listed + "\n"
	oneJSON := fmt.Sprintf("You have 1 background task(s) currently running:\n[{\"taskId\":%q,\"toolName\":\"run_command\"}]", agyTestID+"/task-2")
	shortJSON := "You have 1 background task(s) currently running:\n[{\"taskId\":\"task-2\",\"toolName\":\"run_command\"}]"
	twoJSON := fmt.Sprintf("You have 2 background task(s) currently running:\n[{\"taskId\":%q},{\"taskId\":\"task-2\"}]", agyTestID+"/task-1")
	for _, tc := range []struct {
		name, content string
		wantEnded     int
	}{
		{"JSON array with duplicated headers", dupHeaders + oneJSON, 1},
		{"JSON array with short taskId", shortJSON, 1},
		{"JSON array with all tasks listed", dupHeaders + twoJSON, 0},
		{"malformed JSON array", "You have 1 background task(s) currently running:\n[{\"taskId\":", 0},
		{"JSON array count mismatch", strings.Replace(oneJSON, "have 1", "have 2", 1), 0},
		{"JSON array duplicate taskId", fmt.Sprintf("You have 2 background task(s) currently running:\n[{\"taskId\":\"task-2\"},{\"taskId\":%q}]", agyTestID+"/task-2"), 0},
		{"JSON array foreign taskId", "You have 1 background task(s) currently running:\n[{\"taskId\":\"other-conv/task-2\"}]", 0},
		{"JSON array missing taskId", "You have 1 background task(s) currently running:\n[{\"toolName\":\"run_command\"}]", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := fmt.Sprintf(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_task","args":{"Action":"\"list\""}}]}`+"\n", listed)
			result := fmt.Sprintf(`{"step_index":3,"type":"GENERIC","status":"DONE","created_at":%q,"content":%q}`+"\n", listed, tc.content)
			steps, _ := parseAgySteps([]byte(agyBackgroundStep(0, "RUNNING", started, 1, "Ghost timer", "")+
				agyBackgroundStep(1, "RUNNING", started, 2, "Active command", "")+planner+result+
				agyBackgroundStep(4, "RUNNING", later, 3, "Started after list", "")), agyTestID)
			tasks := agyBackground(steps, agyTestID, "", nil)
			if len(tasks.ended) != tc.wantEnded || len(tasks.running) != 3-tc.wantEnded {
				t.Fatalf("unexpected JSON inventory reconciliation: %+v", tasks)
			}
			if tc.wantEnded == 1 && (tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowCompleted || tasks.ended[0].EndedAt.Format(time.RFC3339) != listed) {
				t.Fatalf("unexpected ended task: %+v", tasks.ended[0])
			}
		})
	}
}

func TestAgyBackgroundTaskStatusReconciliation(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const checked = "2026-06-01T10:00:05Z"
	const id = agyTestID + "/task-1"
	dupHeaders := "Created At: " + checked + "\nCompleted At: " + checked + "\nCreated At: " + checked + "\nCompleted At: " + checked + "\n"
	for _, tc := range []struct {
		name, reported, wantStatus string
	}{
		{"DONE", "DONE", proto.WorkflowCompleted},
		{"ERROR", "ERROR", proto.WorkflowFailed},
		{"CANCELED", "CANCELED", proto.WorkflowKilled},
		{"RUNNING", "RUNNING", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := fmt.Sprintf(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_task","args":{"Action":"\"status\"","TaskId":%q}}]}`+"\n", checked, id)
			content := fmt.Sprintf("%sTask: %s\nStatus: %s\nLog: file:///tmp/task-1.log", dupHeaders, id, tc.reported)
			result := fmt.Sprintf(`{"step_index":3,"type":"GENERIC","status":"DONE","created_at":%q,"content":%q}`+"\n", checked, content)
			steps, _ := parseAgySteps([]byte(agyBackgroundStep(0, "RUNNING", started, 1, "Checked task", "")+
				agyBackgroundStep(1, "RUNNING", started, 2, "Other task", "")+planner+result), agyTestID)
			tasks := agyBackground(steps, agyTestID, "", nil)
			if tc.wantStatus != "" {
				if len(tasks.running) != 1 || tasks.running[0].ID != agyTestID+"/task-2" || len(tasks.ended) != 1 || tasks.ended[0].ID != id || tasks.ended[0].Status != tc.wantStatus {
					t.Fatalf("unexpected status reconciliation: %+v", tasks)
				}
			} else if len(tasks.running) != 2 || len(tasks.ended) != 0 {
				t.Fatalf("expected RUNNING status to preserve both tasks: %+v", tasks)
			}
		})
	}
}

func TestAgyBackgroundMaxIterations(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const t1 = "2026-06-01T10:05:00Z"
	const t2 = "2026-06-01T10:10:00Z"
	planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"schedule","args":{"CronExpression":"*/5 * * * *","MaxIterations":2}},{"name":"schedule","args":{"CronExpression":"*/5 * * * *"}},{"name":"schedule","args":{"DurationSeconds":900,"TimerCondition":"task-1"}}]}`+"\n", started)
	iter := func(stepIdx, taskNum int, at string, n int) string {
		msg := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s/task-%d priority=MESSAGE_PRIORITY_HIGH content=Poll (iteration %d)\n</SYSTEM_MESSAGE>", at, agyTestID, taskNum, n)
		return fmt.Sprintf(`{"step_index":%d,"type":"SYSTEM_MESSAGE","created_at":%q,"content":%q}`+"\n", stepIdx, at, msg)
	}
	base := planner + agyBackgroundStep(1, "RUNNING", started, 1, "Cron: */5 * * * *", "") +
		agyBackgroundStep(2, "RUNNING", started, 2, "Cron: */5 * * * *", "") +
		agyBackgroundStep(3, "RUNNING", started, 3, "Timer: 15m", "") +
		iter(4, 1, t1, 1) + iter(5, 2, t1, 1)
	steps1, _ := parseAgySteps([]byte(base), agyTestID)
	if tasks := agyBackground(steps1, agyTestID, "", nil); len(tasks.running) != 3 || len(tasks.ended) != 0 {
		t.Fatalf("expected both crons and the timer running after iteration 1: %+v", tasks)
	}
	steps2, _ := parseAgySteps([]byte(base+iter(6, 1, t2, 2)+iter(7, 2, t2, 2)), agyTestID)
	tasks := agyBackground(steps2, agyTestID, "", nil)
	if len(tasks.running) != 1 || tasks.running[0].ID != agyTestID+"/task-2" || len(tasks.ended) != 2 || tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowCompleted {
		t.Fatalf("expected bounded cron to finish on iteration 2: %+v", tasks)
	}
	if timer := tasks.ended[1]; timer.ID != agyTestID+"/task-3" || timer.Status != proto.WorkflowKilled || timer.EndedAt.Format(time.RFC3339) != t2 {
		t.Fatalf("expected bounded cron completion to cancel the timer: %+v", timer)
	}
}

func TestAgyBackgroundManageSubagents(t *testing.T) {
	const started = "2026-06-01T10:00:00Z"
	const actAt = "2026-06-01T10:02:00Z"
	const sub1 = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1"
	const sub2 = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee2"
	planner := fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"run_command","args":{"CommandLine":"npm test"}},{"name":"schedule","args":{"CronExpression":"*/5 * * * *"}},{"name":"invoke_subagent"},{"name":"invoke_subagent"}]}`+"\n", started)
	base := planner +
		agyBackgroundStep(1, "RUNNING", started, 1, "Run tests", "") +
		agyBackgroundStep(2, "RUNNING", started, 2, "Cron: */5 * * * *", "") +
		agyBackgroundStep(3, "RUNNING", started, 3, "Subagent "+sub1, "") +
		agyBackgroundStep(4, "RUNNING", started, 4, "Subagent "+sub2, "")

	t.Run("kill_all kills only agent tasks", func(t *testing.T) {
		steps, _ := parseAgySteps([]byte(base+
			fmt.Sprintf(`{"step_index":5,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_subagents","args":{"Action":"\"kill_all\""}}]}`+"\n", actAt)+
			fmt.Sprintf(`{"step_index":6,"type":"GENERIC","status":"DONE","created_at":%q,"content":"Successfully killed 2 subagent(s)."}`+"\n", actAt)), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 2 || len(tasks.ended) != 2 || tasks.ended[0].ID != agyTestID+"/task-3" || tasks.ended[0].Status != proto.WorkflowKilled || tasks.ended[1].ID != agyTestID+"/task-4" {
			t.Fatalf("unexpected kill_all outcome: %+v", tasks)
		}
	})

	t.Run("kill by ConversationIds kills only matching agent task", func(t *testing.T) {
		steps, _ := parseAgySteps([]byte(base+
			fmt.Sprintf(`{"step_index":5,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_subagents","args":{"Action":"kill","ConversationIds":[%q]}}]}`+"\n", actAt, sub1)+
			fmt.Sprintf(`{"step_index":6,"type":"GENERIC","status":"DONE","created_at":%q,"content":"Successfully killed 1 subagent(s)."}`+"\n", actAt)), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 3 || len(tasks.ended) != 1 || tasks.ended[0].ID != agyTestID+"/task-3" || tasks.ended[0].Status != proto.WorkflowKilled {
			t.Fatalf("unexpected selective kill outcome: %+v", tasks)
		}
	})

	t.Run("empty subagent list completes only agent tasks", func(t *testing.T) {
		steps, _ := parseAgySteps([]byte(base+
			fmt.Sprintf(`{"step_index":5,"type":"PLANNER_RESPONSE","created_at":%q,"tool_calls":[{"name":"manage_subagents","args":{"Action":"list"}}]}`+"\n", actAt)+
			fmt.Sprintf(`{"step_index":6,"type":"GENERIC","status":"DONE","created_at":%q,"content":"You have 0 active subagent(s):\n[]"}`+"\n", actAt)), agyTestID)
		tasks := agyBackground(steps, agyTestID, "", nil)
		if len(tasks.running) != 2 || len(tasks.ended) != 2 || tasks.ended[0].Status != proto.WorkflowCompleted {
			t.Fatalf("unexpected empty subagent list outcome: %+v", tasks)
		}
	})
}

func TestAgyBackgroundTimerExpiryWithoutTranscriptUpdate(t *testing.T) {
	n := &Node{home: t.TempDir()}
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	started := start.Format(time.RFC3339)
	deadline := start.Add(time.Minute)
	expiry := deadline.Add(agyTimerExpiryGrace)
	fallbackDeadline := start.Add(90 * time.Second)
	fallbackExpiry := fallbackDeadline.Add(agyTimerExpiryGrace)
	planner := `{"step_index":0,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"schedule","args":{"DurationSeconds":60}},{"name":"schedule","args":{"DurationSeconds":60,"CronExpression":"0 * * * *"}}]}` + "\n"
	path := agyTestFile(t, n.agyDataRoot(), agyTestID, planner+
		agyBackgroundStep(1, "RUNNING", started, 1, "Timer: 60s", "")+
		agyBackgroundStep(2, "RUNNING", started, 2, "Cron: 0 * * * *", "")+
		agyBackgroundStep(9, "RUNNING", started, 3, "Timer: 1m 30s, Prompt: Fallback", ""))
	if err := os.Chtimes(path, start, start); err != nil {
		t.Fatal(err)
	}
	meta, ok := n.agyTranscriptMetaAt(agyTestID, expiry.Add(-time.Nanosecond))
	if tasks := meta.background; !ok || len(tasks.running) != 3 || len(tasks.ended) != 0 || !tasks.nextExpiry.Equal(expiry) {
		t.Fatalf("unexpected state during grace period: %+v", tasks)
	}
	meta, ok = n.agyTranscriptMetaAt(agyTestID, expiry)
	if tasks := meta.background; !ok || len(tasks.running) != 2 || len(tasks.ended) != 1 || !tasks.nextExpiry.Equal(fallbackExpiry) ||
		tasks.ended[0].ID != agyTestID+"/task-1" || tasks.ended[0].Status != proto.WorkflowCompleted || !tasks.ended[0].EndedAt.Equal(deadline) {
		t.Fatalf("expected first timer expired at grace boundary: %+v", tasks)
	}
	meta, ok = n.agyTranscriptMetaAt(agyTestID, fallbackExpiry)
	if tasks := meta.background; !ok || len(tasks.running) != 1 || tasks.running[0].ID != agyTestID+"/task-2" || len(tasks.ended) != 2 || !tasks.nextExpiry.IsZero() {
		t.Fatalf("expected only cron to stay running after both timers expire: %+v", tasks)
	}
}
