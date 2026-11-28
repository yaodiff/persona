package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const taskHerdrWorkspaceID = "ws-task"

// taskHerdrHub is a herdr node whose first session answer names the task workspace it created.
// The new task-workspace inputs stay wire-level here: the base must compile and fail on behavior.
func taskHerdrHub(t *testing.T, store Store) (*Service, *fakeHub) {
	t.Helper()
	s, f := newQuietServiceOn(t, store)
	f.answerNode("worktree.status", func(string, json.RawMessage) (any, error) {
		return proto.WorktreeStatus{Worktree: true, Path: "/code/app", Root: "/code/app"}, nil
	})
	root := ""
	f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
		return map[string]any{"task_id": root, "task_workspace_id": taskHerdrWorkspaceID}, nil
	})
	f.startReply = func(params map[string]any, session *proto.Session) *proto.StartInstead {
		root, _ = params["task_id"].(string)
		if session.SessionID == "s1" {
			session.WorkspaceID = taskHerdrWorkspaceID
		}
		if id, _ := params["workspace_id"].(string); id != "" {
			session.WorkspaceID = id
		}
		return nil
	}
	return s, f
}

func taskWorktreeCreate(f *fakeHub) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.worktrees) - 1; i >= 0; i-- {
		if f.worktrees[i]["method"] == DeviceWorktreeCreate {
			return f.worktrees[i]
		}
	}
	return nil
}

func TestTaskLeaderHerdrWorkspaceSurvivesRestartAndGroupsWorkers(t *testing.T) {
	store := taskRecoveryDatabase(t)
	s, f := taskHerdrHub(t, store)
	leader := startTaskLeader(t, s, f)

	// A manager restart must not lose the workspace before a later worker is admitted.
	restarted := newServiceOn(t, f, store)
	setSettings(t, restarted, func(v *Settings) { v.TaskWorkspacePreference = preferNew })
	if _, _, ok := restarted.leaderTask(context.Background(), "box", "s1"); !ok {
		t.Fatal("the restarted manager did not rediscover the task leader")
	}
	worker := planWorker(t, restarted, f, nil)
	starts := f.started()
	if len(starts) != 2 {
		t.Fatalf("starts = %v", starts)
	}
	if starts[0]["task_id"] != leader.ID || starts[0]["workspace_label"] != leader.Title {
		t.Errorf("leader did not ask for its own named task workspace: %v", starts[0])
	}
	if starts[1]["workspace_id"] != taskHerdrWorkspaceID || starts[1]["workspace_label"] != leader.Title {
		t.Errorf("worker did not reopen the recorded task workspace after restart: %v", starts[1])
	}
	create := taskWorktreeCreate(f)
	if create == nil || create["open_workspace"] != false {
		t.Errorf("writable worker's checkout opened another herdr workspace: %v", create)
	}
	if create == nil || starts[1]["cwd"] != create["path"] || starts[1]["cwd"] == "/code/app" {
		t.Errorf("worker lost its isolated checkout cwd: start=%v create=%v", starts[1], create)
	}
	if worker.Agents[0].Via != leader.ID {
		t.Fatalf("worker is not under the leader task: %+v", worker.Agents[0])
	}
}

const resumedTaskLeaderSession = "leader01"

func stoppedTaskLeader(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := taskHerdrHub(t, nil)
	startReply := f.startReply
	f.startReply = func(params map[string]any, session *proto.Session) *proto.StartInstead {
		instead := startReply(params, session)
		session.SessionID = resumedTaskLeaderSession
		return instead
	}
	leader := startTaskLeader(t, s, f)
	f.setSessions("box", stoppedBoxAgent(resumedTaskLeaderSession))
	resumingNode(f)
	return s, f, leader
}

func resumeTaskStarts(f *fakeHub) []map[string]any {
	var out []map[string]any
	for _, params := range startCalls(f) {
		if params["resume_session_id"] != nil {
			out = append(out, params)
		}
	}
	return out
}

func TestRetiredTaskLeaderReworkAndContinueReuseWorkspace(t *testing.T) {
	for _, action := range []string{"rework", "continue"} {
		t.Run(action, func(t *testing.T) {
			s, f, leader := stoppedTaskLeader(t)
			var err error
			if action == "rework" {
				_, err = runJSON(t, s, "conv1", "task_review", map[string]any{
					"todo_id": leader.ID, "verdict": "rework", "note": "add the missing regression test",
				})
			} else {
				_, err = runJSON(t, s, "conv1", "agent_send", map[string]any{
					"machine": "box", "session_id": resumedTaskLeaderSession, "text": "continue with the same context",
					"resume": true, "resume_why": "the retired leader has the task context",
				})
			}
			if err != nil {
				t.Fatalf("%s of the retired leader failed: %v", action, err)
			}
			starts := resumeTaskStarts(f)
			if len(starts) != 1 || starts[0]["resume_session_id"] != resumedTaskLeaderSession {
				t.Fatalf("%s did not resume the same session: %v", action, starts)
			}
			if starts[0]["workspace_id"] != taskHerdrWorkspaceID || starts[0]["tab_name"] != leader.Title {
				t.Fatalf("%s would open outside the recorded task workspace or label: %v", action, starts[0])
			}
		})
	}
}

func leaderWithAcceptedWorker(t *testing.T) (*Service, *fakeHub, Todo, Todo) {
	t.Helper()
	s, f := taskHerdrHub(t, nil)
	leader := startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, nil)
	raw, _ := json.Marshal(map[string]any{
		"todo_id": worker.ID, "verdict": "accepted", "note": "worker checks pass",
		"checks": completeReviewChecks(todoNow(s, worker.ID)),
	})
	if res := s.mcpCall(context.Background(), "box", "s1", "task_review", raw, false); res.IsError {
		t.Fatalf("accept worker: %+v", res)
	}
	f.answerNode("workspace.close", func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		if params["task_id"] != leader.ID || params["workspace_id"] != taskHerdrWorkspaceID {
			t.Errorf("closed a foreign or user workspace: %v", params)
		}
		return map[string]any{"closed": true}, nil
	})
	return s, f, leader, worker
}

func taskCloseSession(session string, working, offline bool) proto.Session {
	sessionRow := proto.Session{
		SessionID: session, MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusIdle, PaneID: "w-task:" + session, StartedByPersona: true,
		UpdatedAt: time.Now().Add(-time.Hour),
	}
	if working {
		sessionRow.Status = proto.StatusWorking
	}
	if offline {
		sessionRow.Status, sessionRow.PaneID = proto.StatusOffline, ""
	}
	return sessionRow
}

func taskWorkspaceCloses(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, call := range f.calls {
		if call.Method != "workspace.close" {
			continue
		}
		var params map[string]any
		_ = json.Unmarshal(call.Params, &params)
		out = append(out, params)
	}
	return out
}

func TestTaskWorkspaceClosesOnlyAfterWholeLeaderTaskRetires(t *testing.T) {
	for _, verdict := range []string{"accepted", "cancelled"} {
		t.Run(verdict, func(t *testing.T) {
			s, f, leader, worker := leaderWithAcceptedWorker(t)
			oldIdle := taskCloseIdle
			taskCloseIdle = 0
			t.Cleanup(func() { taskCloseIdle = oldIdle })
			refused, seen := taskCloseRefused{}, doneSeen{}
			f.setSessions("box", taskCloseSession("s1", false, false), taskCloseSession(worker.Agents[0].SessionID, true, false))
			s.closeDoneTaskAgents(context.Background(), refused, seen)
			if got := taskWorkspaceCloses(f); len(got) != 0 {
				t.Fatalf("closed a child workspace while the top-level task was open: %v", got)
			}

			args := map[string]any{"todo_id": leader.ID, "verdict": verdict, "note": "finish the whole task"}
			if verdict == "accepted" {
				args["checks"] = completeReviewChecks(todoNow(s, leader.ID))
			}
			if _, err := runJSON(t, s, "conv1", "task_review", args); err != nil {
				t.Fatalf("%s: %v", verdict, err)
			}
			// Retire the leader, but not its still-working worker: the shared workspace must stay open.
			s.closeDoneTaskAgents(context.Background(), refused, seen)
			s.closeDoneTaskAgents(context.Background(), refused, seen)
			if got := taskWorkspaceCloses(f); len(got) != 0 {
				t.Fatalf("closed before every Agent under the task retired: %v", got)
			}

			f.setSessions("box", taskCloseSession("s1", false, true), taskCloseSession(worker.Agents[0].SessionID, false, false))
			s.closeDoneTaskAgents(context.Background(), refused, seen)
			s.closeDoneTaskAgents(context.Background(), refused, seen)
			got := taskWorkspaceCloses(f)
			if len(got) != 1 || got[0]["task_id"] != leader.ID || got[0]["workspace_id"] != taskHerdrWorkspaceID {
				t.Fatalf("%s close = %v; want only task %s workspace %s", verdict, got, leader.ID, taskHerdrWorkspaceID)
			}
			s.closeDoneTaskAgents(context.Background(), refused, seen)
			if again := taskWorkspaceCloses(f); len(again) != 1 {
				t.Fatalf("repeated close pass was not idempotent: %v", again)
			}
		})
	}
}

func TestNonTaskAndUnsupportedHostKeepTheirPlacement(t *testing.T) {
	for _, tc := range []struct {
		name, engine, launcher string
		leader                 bool
	}{
		{name: "ordinary herdr session", launcher: "claude"},
		{name: "ordinary Orca session", engine: proto.EngineOrca, launcher: "claude"},
		{name: "leader on Paseo", engine: proto.EnginePaseo, launcher: "claude-sub", leader: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			f.devices = []Device{
				{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
				{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true, Engine: tc.engine},
			}
			f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
			if tc.engine == proto.EnginePaseo {
				f.hostLaunchers = map[string][]proto.Launcher{"box": {{Name: tc.launcher, Source: "paseo", Agent: "claude"}}}
			}
			task := PlanTask{Title: "Inspect only", Prompt: "read the project", Machine: "box", Cwd: "/code/app",
				Readonly: true, Permission: "plan", Launcher: tc.launcher}
			if tc.leader {
				task.Role = roleLeader
			}
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			starts := f.started()
			if len(starts) != 1 || starts[0]["cwd"] != "/code/app" {
				t.Fatalf("placement changed: %v", starts)
			}
			if starts[0]["task_id"] != nil || starts[0]["workspace_label"] != nil || starts[0]["open_workspace"] != nil {
				t.Fatalf("non-target start acquired task-workspace controls: %v", starts[0])
			}
			for _, call := range f.calls {
				if call.Method == "workspace.close" || call.Method == "workspace.create" ||
					strings.Contains(string(call.Params), `"open_workspace":false`) {
					t.Fatalf("non-target start changed workspace lifecycle: %+v", call)
				}
			}
		})
	}
}

func TestTaskWorkspaceSessionRowsVerifyOwnership(t *testing.T) {
	s, f := taskHerdrHub(t, nil)
	leader := startTaskLeader(t, s, f)
	f.answerNode("session.info", func(_ string, raw json.RawMessage) (any, error) {
		return map[string]any{"task_id": leader.ID, "task_workspace_id": taskHerdrWorkspaceID}, nil
	})
	row := proto.Session{SessionID: "s1", MachineID: "box", WorkspaceID: "w-user", PaneID: "w-user:p1", Status: proto.StatusIdle}
	s.taskSpacesSessionsChanged(context.Background(), DeviceSessions{MachineID: "box", Sessions: []proto.Session{row}})
	if space, _ := s.taskSpaceOf(context.Background(), leader.ID); space.ID != taskHerdrWorkspaceID {
		t.Fatalf("a moved user workspace replaced the task's recorded workspace: %+v", space)
	}
	raw, _ := json.Marshal(map[string]any{"session": map[string]any{"session_id": "s1", "workspace_id": "w-user"}})
	s.noteTaskSpaceResult(context.Background(), leader.ID, "box", raw)
	if space, _ := s.taskSpaceOf(context.Background(), leader.ID); space.ID != taskHerdrWorkspaceID {
		t.Fatalf("a kept-shell response replaced the task's recorded workspace: %+v", space)
	}

	f.answerNode("session.info", func(_ string, raw json.RawMessage) (any, error) {
		return map[string]any{"task_id": leader.ID, "task_workspace_id": "w-recreated"}, nil
	})
	row.WorkspaceID = "w-recreated"
	s.taskSpacesSessionsChanged(context.Background(), DeviceSessions{MachineID: "box", Sessions: []proto.Session{row}})
	if space, _ := s.taskSpaceOf(context.Background(), leader.ID); space.ID != "w-recreated" {
		t.Fatalf("node-confirmed recreated workspace was not recorded: %+v", space)
	}
}

func TestStoppedTaskFollowupsDoNotResumeExceptRework(t *testing.T) {
	s, f, leader := stoppedTaskLeader(t)
	if _, err := s.sendToTaskAs(context.Background(), leader, "ordinary follow-up", false); !errors.Is(err, errTaskAgentsStopped) {
		t.Fatalf("ordinary stopped follow-up resumed the Agent: %v", err)
	}
	if starts := resumeTaskStarts(f); len(starts) != 0 {
		t.Fatalf("ordinary follow-up started a resume: %v", starts)
	}
}

func TestStoppedTaskReworkChecksContextAndWritesResumeNote(t *testing.T) {
	s, f, leader := stoppedTaskLeader(t)
	c := stoppedBoxAgent(resumedTaskLeaderSession)
	c.ContextTokens, c.ContextWindow, c.Agent = 90, 100, "pi"
	f.setSessions("box", c)
	resumingNode(f)
	res, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": leader.ID, "verdict": "rework", "note": "finish the missing checks",
	})
	if err == nil && !strings.Contains(res, "nearly full") {
		t.Fatalf("rework skipped the context check: result=%q error=%v", res, err)
	}
	if starts := resumeTaskStarts(f); len(starts) != 0 {
		t.Fatalf("uncompactable context started a resume: %v", starts)
	}

	s, f, leader = stoppedTaskLeader(t)
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": leader.ID, "verdict": "rework", "note": "finish the missing checks",
	}); err != nil {
		t.Fatal(err)
	}
	items, _, _ := s.feed(context.Background(), "conv1", time.Time{})
	found := false
	for _, item := range items {
		found = found || strings.Contains(string(item.Body), "rework of this task")
	}
	if !found {
		t.Fatalf("rework wrote no resume note on the task card: %+v", items)
	}
}

func TestTaskLeaderHerdrBothUsesTaskWorkspace(t *testing.T) {
	s, f := taskHerdrHub(t, nil)
	for i := range f.devices {
		if f.devices[i].ID == "box" {
			f.devices[i].Engine = proto.EngineHerdrBoth
		}
	}
	leader := startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, nil)
	starts := f.started()
	if len(starts) != 2 || starts[0]["workspace_label"] != leader.Title || starts[1]["workspace_id"] != taskHerdrWorkspaceID {
		t.Fatalf("herdr-both leader/worker placement = %v", starts)
	}
	create := taskWorktreeCreate(f)
	if create == nil || create["open_workspace"] != false || starts[1]["cwd"] != create["path"] || worker.ID == leader.ID {
		t.Fatalf("herdr-both worker lost checkout-only placement: start=%v create=%v", starts[1], create)
	}
}

func TestTaskWorkspaceCloseChecksEveryAgentMachine(t *testing.T) {
	s, f, leader, worker := leaderWithAcceptedWorker(t)
	const remote = "remote"
	f.mu.Lock()
	f.devices = append(f.devices, Device{ID: remote, Name: remote, Home: "/home/me", Online: true, Reachable: true})
	f.mu.Unlock()
	s.todos.mu.Lock()
	s.todos.todos[worker.ID].Agents[0].MachineID = remote
	s.todos.mu.Unlock()
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": leader.ID, "verdict": "accepted", "note": "finish the whole task", "checks": completeReviewChecks(todoNow(s, leader.ID)),
	}); err != nil {
		t.Fatal(err)
	}
	oldIdle := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = oldIdle })
	refused, seen := taskCloseRefused{}, doneSeen{}
	f.setSessions("box", taskCloseSession("s1", false, true))
	remoteRow := taskCloseSession(worker.Agents[0].SessionID, true, false)
	remoteRow.MachineID = remote
	f.setSessions(remote, remoteRow)
	s.closeDoneTaskAgents(context.Background(), refused, seen)
	s.closeDoneTaskAgents(context.Background(), refused, seen)
	if got := taskWorkspaceCloses(f); len(got) != 0 {
		t.Fatalf("closed with a live worker on another machine: %v", got)
	}

	remoteRow = taskCloseSession(worker.Agents[0].SessionID, false, true)
	remoteRow.MachineID = remote
	f.setSessions(remote, remoteRow)
	f.answerNode(DeviceSessionsList, func(machine string, raw json.RawMessage) (any, error) {
		if machine == remote {
			return nil, errors.New("session list unavailable")
		}
		return map[string]any{"sessions": []any{}}, nil
	})
	s.closeDoneTaskAgents(context.Background(), refused, seen)
	if got := taskWorkspaceCloses(f); len(got) != 0 {
		t.Fatalf("closed while another machine's snapshot was unavailable: %v", got)
	}

	f.mu.Lock()
	delete(f.nodes, DeviceSessionsList)
	f.mu.Unlock()
	f.setSessions(remote, remoteRow)
	f.mu.Lock()
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true},
		{ID: remote, Name: remote, Home: "/home/me", Online: true, Reachable: true},
	}
	f.mu.Unlock()
	boxList, boxErr := s.sessions(context.Background(), "box")
	remoteList, remoteErr := s.sessions(context.Background(), remote)
	lists := map[string][]proto.Session{"box": boxList, remote: remoteList}
	if busy := s.taskSpaceBusy(leader.ID, taskHerdrWorkspaceID, lists, seen); busy {
		t.Fatalf("task still busy with every machine retired: seen=%+v rows=%+v errors=%v/%v", seen, lists, boxErr, remoteErr)
	}
	s.closeDoneTaskAgents(context.Background(), refused, seen)
	s.closeDoneTaskAgents(context.Background(), refused, seen)
	if got := taskWorkspaceCloses(f); len(got) != 1 {
		t.Fatalf("did not close after every machine retired: %v", got)
	}
}

func TestTaskWorkspaceCloseRequiresConfirmedStopTime(t *testing.T) {
	s, _, leader := stoppedTaskLeader(t)
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": leader.ID, "verdict": "accepted", "note": "finish", "checks": completeReviewChecks(todoNow(s, leader.ID)),
	}); err != nil {
		t.Fatal(err)
	}
	row := taskCloseSession(resumedTaskLeaderSession, false, false)
	key := [2]string{"box", resumedTaskLeaderSession}
	seen := doneSeen{key: {stopped: true}}
	lists := map[string][]proto.Session{"box": {row}}
	if !s.taskSpaceBusy(leader.ID, taskHerdrWorkspaceID, lists, seen) {
		t.Fatal("a zero-time stale stop marked a live Agent retired")
	}
	watched := seen[key]
	watched.lastStop = time.Now()
	seen[key] = watched
	if s.taskSpaceBusy(leader.ID, taskHerdrWorkspaceID, lists, seen) {
		t.Fatal("a confirmed stop older than the row did not count as retired")
	}
}
