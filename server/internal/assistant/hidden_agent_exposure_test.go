package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestHiddenDirectoryAmbientExposure(t *testing.T) {
	for _, runtime := range []string{"claude", "codex"} {
		t.Run(runtime, func(t *testing.T) {
			ft := newFleetTest(t)
			hideExposureDirectory(ft.s, "mac", "~/automation")
			rows := []proto.Session{
				{SessionID: "hidden-needs", Agent: runtime, Cwd: "/home/u/automation/a", Title: "Hidden question", Status: proto.StatusBlocked, UpdatedAt: time.Now()},
				{SessionID: "hidden-done", Agent: runtime, Cwd: "/home/u/automation/b", Title: "Hidden completion", Status: proto.StatusDone, LastText: "Complete", UpdatedAt: time.Now()},
				{SessionID: "visible-work", Agent: runtime, Cwd: "/code/app", Title: "Visible work", Status: proto.StatusWorking, UpdatedAt: time.Now()},
			}
			ft.d.setSessions("mac", rows)
			inventory := ft.s.decorated("mac")
			if len(inventory) != 3 || !inventory[0].HiddenByDirectory() || !inventory[1].HiddenByDirectory() || inventory[2].HiddenByDirectory() {
				t.Fatalf("inventory must remain recorded and marked: %+v", inventory)
			}
			for _, state := range []string{"all", "active", "needs_you", "unread", "done", "muted", "archived"} {
				if got := ft.s.describeAgents(context.Background(), state, "", ""); strings.Contains(got, "Hidden") || strings.Contains(got, "hidden-") {
					t.Fatalf("agents_list state=%s leaked hidden session: %s", state, got)
				}
			}
			needs, unread, awaiting, waiting, done, muted, working := ft.s.snapshotAgents(context.Background(), []Device{{ID: "mac"}}, "")
			if len(needs)+len(unread)+len(awaiting)+len(waiting)+len(done)+len(muted) != 0 || len(working) != 1 || working[0].Label != "Visible work" {
				t.Fatalf("snapshot includes hidden sessions: needs=%v unread=%v awaiting=%v waiting=%v done=%v muted=%v working=%v", needs, unread, awaiting, waiting, done, muted, working)
			}
			fleet := ft.s.fleetSnapshot()
			if fleet.NeedsYou != 0 || fleet.Unread != 0 || fleet.Working != 1 {
				t.Fatalf("live counts include hidden sessions: %+v", fleet)
			}
			agents, ixs, told := ft.s.fleetInputs()
			if got := linkAttention(i18n.Default, agents, ixs, told); got.NeedsYou != 0 {
				t.Fatalf("Link needs-you includes hidden: %+v", got)
			}
			if !ft.s.muted("mac", "hidden-done") {
				t.Fatal("hidden completion can notify")
			}
		})
	}
}

func TestHiddenDirectoryStopsAmbientTriageAndReminders(t *testing.T) {
	s, f := workService(t)
	c := stopped("hidden-session", "Please choose whether to continue")
	c.SetHiddenByDirectory(true)
	f.setSessions("box", c)
	triage(t, s, c.SessionID)
	if _, ok := s.workEntry([2]string{"box", c.SessionID}); ok {
		t.Fatal("hidden ambient session was triaged")
	}
	v := s.newWorkView(context.Background())
	if !v.isMuted("box", c.SessionID) {
		t.Fatal("hidden session may create tool-loop card")
	}
	if _, why := s.workGuard(v, db.SessionWork{MachineID: "box", SessionID: c.SessionID, TurnAt: c.UpdatedAt}, false); why != "hidden by directory" {
		t.Fatalf("work guard = %q", why)
	}
	c.State, c.AwaitsReply, c.UpdatedAt = proto.StateIdle, true, time.Now().Add(-time.Hour)
	if s.replyWaits("box", c, time.Now()) {
		t.Fatal("hidden question may remind")
	}
}

func TestHiddenDirectoryFoldDoesNotCountChildren(t *testing.T) {
	list := []proto.Session{
		{SessionID: "parent", State: proto.StateIdle},
		{SessionID: "hidden-child", ParentSessionID: "parent", IsSubagent: true, State: proto.StateNeedsYou, Hierarchy: &proto.SessionHierarchy{HiddenByDirectory: true}},
		{SessionID: "visible-child", ParentSessionID: "parent", IsSubagent: true, State: proto.StateWorking},
	}
	foldSessions(list, nil).apply(list)
	if got := list[0].Children(); got.Total != 1 || got.Working != 1 || got.NeedsYou != 0 {
		t.Fatalf("hidden child's state leaked into parent: %+v", got)
	}
	if list[1].FoldedUnder() != "" {
		t.Fatal("hidden child still folded")
	}
}

func TestHiddenDirectoryArchiveRowsRemainHidden(t *testing.T) {
	ft, rows := archiveTestService(t)
	for i := range rows {
		rows[i].Cwd = "/code/hidden"
	}
	ft.d.setSessions("mac", rows)
	for _, row := range rows {
		if w := archiveRequest(ft.s, "mac", row.SessionID, roleFull, true); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	ft.d.setSessions("mac", nil)
	hideExposureDirectory(ft.s, "mac", "/code/hidden")
	inventory := ft.s.decorated("mac")
	if len(inventory) != 2 {
		t.Fatalf("archive inventory lost: %+v", inventory)
	}
	for _, row := range inventory {
		if !row.HiddenByDirectory() {
			t.Fatalf("remembered row exposed: %+v", row)
		}
		if one, ok := ft.s.decoratedSession("mac", row.SessionID); !ok || !one.HiddenByDirectory() {
			t.Fatalf("single archived row exposed: %+v", one)
		}
	}
	if got := archiveRows(t, ft.s); len(got) != 0 {
		t.Fatalf("archive list exposed hidden rows: %+v", got)
	}
}

func TestHiddenDirectorySupervisedTaskStillObserved(t *testing.T) {
	e := newSupervisedEnv(t, false)
	todo := e.task("conv1", "conv1")
	hideExposureDirectory(e.s, "mac", "/code/app")
	c, ok := e.s.decoratedSession("mac", supAgent)
	if !ok || c.HiddenByDirectory() {
		t.Fatalf("supervised task hidden: %+v", c)
	}
	if got := e.s.describeAgents(context.Background(), "all", "", ""); !strings.Contains(got, supAgent) {
		t.Fatalf("supervised task disappeared from observation: %s", got)
	}
	if ok, err := e.s.reviewDoneTodo(context.Background(), todo.ID, "Accepted", true); err != nil || !ok {
		t.Fatalf("close: %v %v", ok, err)
	}
	c, ok = e.s.decoratedSession("mac", supAgent)
	if !ok || !c.HiddenByDirectory() {
		t.Fatalf("closed supervised task bypasses directory rule: %+v", c)
	}
}

func TestHiddenDirectorySupervisionNoticeDelivered(t *testing.T) {
	e := newSupervisedEnv(t, false)
	todo := e.task("conv1", "conv1")
	hideExposureDirectory(e.s, "mac", "/code/app")
	e.f.mu.Lock()
	e.f.judge = `{"verdict":"done","reason":"Checks passed"}`
	e.f.mu.Unlock()
	e.holdDone()
	sess := supAgentSession()
	sess.LastText = "Change completed and checks passed"
	driveAgentDone(e.fleetTest, sess)
	e.s.taskTurnEnded(context.Background(), "mac", supAgent)
	waitFor(t, "supervision notice despite matching directory rule", func() bool { return len(e.f.notices()) > 0 })
	found := false
	for _, notice := range e.f.notices() {
		if strings.Contains(notice, todo.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("task supervision missing from delivered notices: %v", e.f.notices())
	}
}

func TestHiddenDirectoryColdSupervision(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	seedTodos(t, database, db.TodoRow{ID: "visibility-task", Title: "Check change"})
	if err := database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: "visibility-task", Supervisor: "main", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.LinkTodoAgentWithSource(ctx, "visibility-task", "mac", "cold-agent", "Worker", "/code/hidden", "codex", "", "manual", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	ft := newFleetTest(t)
	ft.s.setStore(func() Store { return database })
	hideExposureDirectory(ft.s, "mac", "/code/hidden")
	ft.d.setSessions("mac", []proto.Session{{SessionID: "cold-agent", Cwd: "/code/hidden", Agent: "codex", Status: proto.StatusWorking}})
	if ft.s.todosLoaded() {
		t.Fatal("test must begin before task inventory loads")
	}
	c, ok := ft.s.decoratedSession("mac", "cold-agent")
	if !ok || c.HiddenByDirectory() {
		t.Fatalf("persisted active supervision hidden on first read: %+v", c)
	}
	// Finish first-load publication before the synthetic database closes.
	published := make(chan struct{})
	ft.s.inOrder(func() { close(published) })
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("first-load publication did not finish")
	}
}

type visibilityTodoReadFailure struct{ *db.DB }

func (*visibilityTodoReadFailure) ListTodos(context.Context) ([]db.TodoRow, error) {
	return nil, errors.New("task inventory unavailable")
}

func TestHiddenDirectoryKeepsUnknownSupervisionVisible(t *testing.T) {
	ft := newFleetTest(t)
	ft.s.setStore(func() Store { return &visibilityTodoReadFailure{} })
	if !ft.s.directorySessionSupervised("mac", "agent") {
		t.Fatal("unreadable inventory must not hide potentially supervised work")
	}
}

// get counts only what each rule hides now: an assistant conversation and an active supervised task under the folder
// are not hidden (hiddenByDirectory), and each rule keeps its own count.
func TestHiddenDirectorySettingCountsOnlyHiddenSessions(t *testing.T) {
	e := newSupervisedEnv(t, false)
	e.task("conv1", "conv1")
	sup := supAgentSession()
	sup.MachineID, sup.Status = "mac", proto.StatusIdle
	e.sessions("mac", sup,
		proto.Session{SessionID: "plain", MachineID: "mac", Cwd: "/code/app/x", Status: proto.StatusIdle},
		proto.Session{SessionID: "plain2", MachineID: "mac", Cwd: "/code/other", Status: proto.StatusIdle},
		proto.Session{SessionID: "talk", MachineID: "mac", Cwd: "/code/app", Assistant: true, Status: proto.StatusIdle})
	f := useAgentDirs(e.s)
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/code/app", Enabled: true}, proto.HiddenAgentDir{Pattern: "/code", Enabled: true})
	got := settingsTool(e.s, `{"action":"get","key":"agents.hidden_dirs"}`)
	if got.IsError {
		t.Fatalf("get: %+v", got)
	}
	text := got.Content[0].Text
	for _, want := range []string{"/code/app (hide, on; matches 1 sessions)", "/code (hide, on; matches 2 sessions)"} {
		if !strings.Contains(text, want) {
			t.Errorf("want %q in:\n%s", want, text)
		}
	}
}

// A standing authorization still answers a card from a directory-hidden session (an open task no one supervises, so
// the folder rule hides it): hiding changes what is shown, never what is approved. A card no grant covers stays
// pending and is kept for the hidden session's own page, out of the user-visible routing.
func TestHiddenDirectoryKeepsStandingApproval(t *testing.T) {
	for _, covered := range []bool{true, false} {
		t.Run(map[bool]string{true: "covered", false: "not_covered"}[covered], func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			e := newRouteEnv(t, database, true)
			t.Cleanup(func() { settleService(t, e.s) })
			grant := routeStandingGrant()
			grant.Actions, grant.Reversible = []string{"merge"}, true
			if covered {
				saveDispatchAuthorizations(t, database, grant)
			}
			agents := append([]proto.Session(nil), e.agents...)
			agents[0].Cwd = "/code/app"
			e.sessions("mac", agents...)
			e.f.setSessions("mac", agents...)
			e.s.todos.mu.Lock()
			e.s.todos.todos[e.todo.ID].Supervision = nil // the task is open, but no one supervises it
			e.s.todos.mu.Unlock()
			hideExposureDirectory(e.s, "mac", "/code/app")
			if !e.s.directoryHiddenSession("mac", agents[0].SessionID) {
				t.Fatal("setup: the session is not directory-hidden")
			}
			ix := askQuestion("standing-hidden", agents[0].SessionID)
			ix.Questions[0].Question = "Merge?"
			ix.Questions[0].Options = []proto.QuestionOption{{Label: "Continue"}, {Label: "Not now"}}
			ix.ToolInput, _ = json.Marshal(map[string]any{"questions": ix.Questions})
			p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
			putPending(e.s, p)
			e.s.record(ix)
			e.s.announce(ix)
			record, err := database.InteractionRecord(t.Context(), ix.ID)
			if err != nil {
				t.Fatal(err)
			}
			if covered {
				select {
				case answer := <-p.done:
					if answer.AnsweredBy != proto.AnsweredByAssistant || !strings.Contains(answer.Reason, grant.ID) {
						t.Fatalf("answer lacks authority: %+v", answer)
					}
				default:
					t.Fatal("a standing authorization did not answer a hidden session's card")
				}
				if record.Status != "resolved" {
					t.Fatalf("outcome: %+v", record)
				}
				return
			}
			if record.Status != "pending" {
				t.Fatalf("uncovered card changed: %+v", record)
			}
			if _, ok := e.s.pendingIx(ix.ID); !ok {
				t.Fatal("hidden card left the inventory")
			}
			if e.pushesOf(ix.ID) != 0 || len(e.s.heldAsks(e.todo.ID, "lead1")) != 0 {
				t.Fatal("a hidden session's card was routed to the user or a supervisor")
			}
		})
	}
}
