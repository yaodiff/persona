package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDirectionReadMetadataCurrentAndEnded(t *testing.T) {
	e := directionFilingEnv(t, nil)
	if code, _ := doRoute(t, e.s, http.MethodPut, "/api/v1/assistant/directions/conv_lead_a/name", `{"name":"Release train"}`, roleFull); code != http.StatusOK {
		t.Fatalf("name: %d", code)
	}
	if code, _ := doRoute(t, e.s, http.MethodPut, "/api/v1/assistant/directions/conv_lead_a/notes", `{"notes":"Verify on a real phone"}`, roleFull); code != http.StatusOK {
		t.Fatalf("notes: %d", code)
	}
	if _, err := runJSON(t, e.s, "conv_lead_a", "leader_progress", map[string]any{
		"reached": "RC built", "doing": "Device verification", "needs_you": "Choose the release day",
	}); err != nil {
		t.Fatal(err)
	}

	got := directionMustRead(t, e.s, "readonly", "conv_lead_a")
	if got.ID != "conv_lead_a" || got.Name != "Release train" || got.EndedAt != nil {
		t.Fatalf("current identity: %+v", got)
	}
	if got.Notes != "Verify on a real phone" || got.NotesAt == nil {
		t.Fatalf("notes: %+v", got)
	}
	if got.Progress == nil || got.Progress.Reached != "RC built" || got.Progress.Doing != "Device verification" || got.Progress.NeedsYou != "Choose the release day" {
		t.Fatalf("progress: %+v", got.Progress)
	}
	if got.Sessions == nil || got.EndedTasks == nil {
		t.Fatalf("new collections must be additive empty lists, not null: %s", mustDirectionJSON(got))
	}

	if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end: %d", code)
	}
	e.s.directionsSync(context.Background())
	ended := directionMustRead(t, e.s, "readonly", "conv_lead_a")
	if ended.EndedAt == nil || ended.Name != "Release train" || ended.Notes != got.Notes || ended.Progress == nil {
		t.Fatalf("ended direction read: %+v", ended)
	}
}

// directionSameRow: two reads list a session the same way (where, how it got there, since when, its state and title).
func directionSameRow(a, b directionSessionView) bool {
	return a.MachineID == b.MachineID && a.SessionID == b.SessionID && a.Title == b.Title && a.Source == b.Source &&
		a.State == b.State && (a.At == nil) == (b.At == nil) && (a.At == nil || a.At.Equal(*b.At))
}

func mustDirectionJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestDirectionReadPlacementOriginOnOwnAndNoFolder(t *testing.T) {
	e := directionFilingEnv(t, nil)
	// A folder or project is not placement evidence.
	e.s.directions.mu.Lock()
	if !e.s.directionsLocked(context.Background()) {
		e.s.directions.mu.Unlock()
		t.Fatal("directions unavailable")
	}
	e.s.directions.m["conv_lead_a"].Project = "weather"
	e.s.directions.mu.Unlock()
	directionFilingSetSessions(e, proto.Session{MachineID: "server", SessionID: "chat-same-folder", Cwd: "/code/weather",
		Assistant: true, Status: proto.StatusIdle, Title: "Same folder only"})

	e.s.originSet(context.Background(), "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
	before := directionMustRead(t, e.s, "", "conv_lead_a")
	opened := directionFindSession(t, before.Sessions, "chat-opened")
	if opened.Source == "" || opened.At == nil {
		t.Fatalf("leader-opened session lacks source/time: %+v", opened)
	}
	if slices.Contains(directionSessionIDs(before.Sessions), "chat-same-folder") {
		t.Fatalf("folder/project decided placement: %+v", before.Sessions)
	}

	onOwn := directionFilingMustPut(t, e.s, "server", "chat-opened", "")
	if onOwn.Previous != nil || !onOwn.Changed {
		t.Fatalf("on-own over origin: %s", onOwn.Raw)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-opened") {
		t.Fatalf("on-own did not override recorded origin: %+v", got.Sessions)
	}
	// Undo restores the earlier placement exactly: no entry, so the recorded origin places it again (#11458).
	undo := directionUndo(t, e.s, "server", "chat-opened", onOwn)
	if directionValue(undo.Previous) != "" || !undo.Changed {
		t.Fatalf("undo on-own over origin: %s", undo.Raw)
	}
	after := directionMustRead(t, e.s, "", "conv_lead_a")
	if restored := directionFindSession(t, after.Sessions, "chat-opened"); !directionSameRow(restored, opened) {
		t.Fatalf("undo changed the placement: before %+v, after %+v", opened, restored)
	}

	// A leader is never a session filed under its own or another direction.
	if got := directionMustRead(t, e.s, "", "conv_lead_b"); slices.Contains(directionSessionIDs(got.Sessions), "conv_lead_a") {
		t.Fatalf("a leader was listed as filed: %+v", got.Sessions)
	}
}

func TestDirectionReadSupervisedTaskPrecedesManualFiling(t *testing.T) {
	e := directionFilingEnv(t, nil)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-joins-task", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, Title: "Task agent"})
	directionFilingMustPut(t, e.s, "box", "agent-joins-task", "conv_lead_b")
	taskID := e.task("Task takes precedence", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-joins-task"}}
	})
	e.s.todos.mu.Lock()
	e.s.todos.activeBySession[[2]string{"box", "agent-joins-task"}] = taskID
	e.s.todos.mu.Unlock()

	if got := directionMustRead(t, e.s, "", "conv_lead_b"); slices.Contains(directionSessionIDs(got.Sessions), "agent-joins-task") {
		t.Fatalf("manual filing overrode the supervised open task: %+v", got.Sessions)
	}
	a := e.tree().Directions[0]
	if !slices.Contains(taskIDs(a), taskID) || len(a.Tasks) != 1 || len(a.Tasks[0].Agents) != 1 || a.Tasks[0].Agents[0].SessionID != "agent-joins-task" {
		t.Fatalf("Agent did not follow its task's direction: %+v", a)
	}
}

func TestDirectionReadPresenceAndFilingTimeTitle(t *testing.T) {
	e := directionFilingEnv(t, nil)
	e.f.setSessions("gone", proto.Session{MachineID: "gone", SessionID: "agent-offline", Agent: "claude", Status: proto.StatusIdle, Title: "Offline title"})
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-not-listed", Agent: "claude", Status: proto.StatusIdle, Title: "Archived title"})
	e.f.mu.Lock()
	for i := range e.f.sessions["server"] {
		if e.f.sessions["server"][i].SessionID == "chat-a" {
			e.f.sessions["server"][i].Status = proto.StatusWorking
			e.f.sessions["server"][i].State = proto.StateWorking
		}
	}
	e.f.mu.Unlock()

	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	directionFilingMustPut(t, e.s, "gone", "agent-offline", "conv_lead_a")
	directionFilingMustPut(t, e.s, "box", "agent-not-listed", "conv_lead_a")
	e.s.originSet(context.Background(), "server", "chat-opened", OriginSpawned, "conv_lead_a", "")

	// A later list title does not rewrite the title remembered by the filing. A quiet session remains in this read.
	e.f.mu.Lock()
	for i := range e.f.sessions["server"] {
		if e.f.sessions["server"][i].SessionID == "chat-a" {
			e.f.sessions["server"][i].Title = "Renamed after filing"
		}
	}
	e.f.mu.Unlock()
	e.f.sessionsFail["gone"] = 100
	e.f.setSessions("box") // the hub's reversible delete is invisible here: the session is simply not listed

	got := directionMustRead(t, e.s, "", "conv_lead_a")
	if len(got.Sessions) != 4 {
		t.Fatalf("read lost filed or leader-opened sessions: %+v", got.Sessions)
	}
	online := directionFindSession(t, got.Sessions, "chat-a")
	if online.State != proto.StateWorking || online.Title != "Original chat title" || online.At == nil || online.Source == "" {
		t.Fatalf("listed session: %+v", online)
	}
	// Its list could not be read: what the session does there is not known (#11458: never its last state).
	if offline := directionFindSession(t, got.Sessions, "agent-offline"); offline.State != "computer_offline" || offline.Title != "Offline title" {
		t.Fatalf("offline session: %+v", offline)
	}
	if missing := directionFindSession(t, got.Sessions, "agent-not-listed"); missing.State != "not_listed" || missing.Title != "Archived title" {
		t.Fatalf("not-listed session: %+v", missing)
	}
	opened := directionFindSession(t, got.Sessions, "chat-opened")
	if opened.Source == "" || opened.Source == online.Source {
		t.Fatalf("owner and leader-opened sessions need distinct sources: owner=%+v opened=%+v", online, opened)
	}
}

func TestDirectionReadEndedTaskKeepsOpenOwnership(t *testing.T) {
	e := directionFilingEnv(t, nil)
	taskID := e.task("Same direction after ending", supervisedBy("conv_lead_a"))
	if got := e.tree().Directions[0]; !slices.Contains(taskIDs(got), taskID) {
		t.Fatalf("open task not under A: %+v", got)
	}
	now := time.Now().UTC()
	e.s.todos.mu.Lock()
	e.s.todos.todos[taskID].DoneAt = &now
	e.s.todos.todos[taskID].UpdatedAt = now
	e.s.todos.mu.Unlock()

	got := directionMustRead(t, e.s, "", "conv_lead_a")
	if len(got.EndedTasks) != 1 || got.EndedTasks[0].ID != taskID || got.EndedTasks[0].State != "done" {
		t.Fatalf("ended task changed direction: %+v", got.EndedTasks)
	}
	if other := directionMustRead(t, e.s, "", "conv_lead_b"); len(other.EndedTasks) != 0 {
		t.Fatalf("ended task leaked to B: %+v", other.EndedTasks)
	}
}

func TestDirectionReadTodosV2SharesBoardAttribution(t *testing.T) {
	e := directionFilingEnv(t, nil)
	on := true
	setSettings(t, e.s, func(cfg *Settings) { cfg.TodosV2 = &on })
	at := time.Now().UTC()
	parent := e.task("Task from a historical direction", func(todo *Todo) {
		todo.Origin = "conv_lead_a"
	})
	closedChild := e.task("Closed step keeps its inherited direction", func(todo *Todo) {
		todo.PartOf, todo.DoneAt = parent, &at
	})
	explicit := e.task("Explicit attribution wins in history", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.DirectionID, todo.DoneAt = "conv_lead_b", &at
	})
	openChild := e.task("Open step of a historical direction", func(todo *Todo) {
		todo.PartOf = parent
	})
	if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end direction: %d", code)
	}
	e.s.directionsSync(context.Background())

	boardResp := doTodoHTTP(e.s, http.MethodGet, "/api/v1/todos", "")
	var list todosList
	if boardResp.Code != http.StatusOK || json.Unmarshal(boardResp.Body.Bytes(), &list) != nil || list.Board == nil {
		t.Fatalf("GET /api/v1/todos: %d %s", boardResp.Code, boardResp.Body.String())
	}
	groupOf := map[string]string{}
	var historicalLabel string
	for _, group := range list.Board.DirectionGroups {
		if group.DirectionID == "conv_lead_a" {
			historicalLabel = group.Label
		}
		for _, id := range group.TodoIDs {
			groupOf[id] = group.DirectionID
		}
	}
	for id, want := range map[string]string{parent: "conv_lead_a", openChild: "conv_lead_a", closedChild: "conv_lead_a", explicit: "conv_lead_b"} {
		if groupOf[id] != want {
			t.Errorf("board direction for %s = %q, want %q", id, groupOf[id], want)
		}
	}
	for direction, want := range map[string]string{"conv_lead_a": closedChild, "conv_lead_b": explicit} {
		got := directionMustRead(t, e.s, "", direction)
		if direction == "conv_lead_a" && (historicalLabel == "" || got.Name != historicalLabel) {
			t.Errorf("historical direction label = %q in the board, %q in the direction read", historicalLabel, got.Name)
		}
		if len(got.EndedTasks) != 1 || got.EndedTasks[0].ID != want {
			t.Errorf("ended tasks in %s = %+v, want %s", direction, got.EndedTasks, want)
		}
	}
	for _, group := range e.tree().Directions {
		if group.ID == "conv_lead_a" || !group.Secretary && (slices.Contains(taskIDs(group), parent) || slices.Contains(taskIDs(group), openChild)) {
			t.Errorf("historical attribution leaked into current direction %s: %+v", group.ID, group)
		}
		if group.Secretary && (!slices.Contains(taskIDs(group), parent) || !slices.Contains(taskIDs(group), openChild)) {
			t.Errorf("historically attributed open work missing from the secretary group: %+v", group)
		}
	}
}

func TestDirectionReadEndedTaskUsesStoredCancellation(t *testing.T) {
	e := directionFilingEnv(t, nil)
	now := time.Now().UTC()
	taskID := e.task("Cancelled in review", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.DoneAt = &now
		todo.DoneBy = "cancelled"
	})
	got := directionMustRead(t, e.s, "", "conv_lead_a")
	if len(got.EndedTasks) != 1 || got.EndedTasks[0].ID != taskID || got.EndedTasks[0].State != "cancelled" {
		t.Fatalf("stored cancellation was lost through the client projection: %+v", got.EndedTasks)
	}
}

func TestDirectionReadEndedTaskPagingAndTitleFilter(t *testing.T) {
	e := directionFilingEnv(t, nil)
	base := time.Now().Add(-2 * time.Hour).UTC()
	var ids []string
	for i := range 22 {
		at := base.Add(time.Duration(i) * time.Minute)
		id := e.task(fmt.Sprintf("Release %02d", i), func(todo *Todo) {
			todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
			todo.CreatedAt, todo.UpdatedAt = at, at
			if i%2 == 0 {
				todo.DoneAt = &at
			} else {
				todo.DoneAt, todo.CancelledAt = &at, &at
				todo.Supervision.State = reviewCancelled
			}
		})
		ids = append(ids, id)
	}
	at := base.Add(23 * time.Minute)
	e.task("Unrelated title", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.CreatedAt, todo.UpdatedAt, todo.DoneAt = at, at, &at
	})

	readPage := func(query string) directionReadAnswer {
		t.Helper()
		code, got, raw := directionReadGet(t, e.s, "readonly", "/api/v1/assistant/directions/conv_lead_a?"+query)
		if code != http.StatusOK {
			t.Fatalf("read page %s: %d %s", query, code, raw)
		}
		return got
	}
	first := readPage("title=Release")
	if len(first.EndedTasks) != 20 || !first.More {
		t.Fatalf("first page has %d tasks, more=%v: %+v", len(first.EndedTasks), first.More, first.EndedTasks)
	}
	var want []string
	for i := 21; i >= 2; i-- {
		want = append(want, ids[i])
	}
	var got []string
	for _, task := range first.EndedTasks {
		got = append(got, task.ID)
		if !strings.HasPrefix(task.Title, "Release ") {
			t.Fatalf("title filter leaked %q", task.Title)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("newest-first page:\ngot  %v\nwant %v", got, want)
	}
	before := url.QueryEscape(first.EndedTasks[len(first.EndedTasks)-1].ID)
	second := readPage("title=Release&before=" + before)
	if second.More || len(second.EndedTasks) != 2 || second.EndedTasks[0].ID != ids[1] || second.EndedTasks[1].ID != ids[0] {
		t.Fatalf("second page: %+v", second.EndedTasks)
	}
	if second.EndedTasks[0].State != "cancelled" || second.EndedTasks[1].State != "done" {
		t.Fatalf("done/cancelled states: %+v", second.EndedTasks)
	}
}
