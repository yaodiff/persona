package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTodosV2AttentionPrefsSettingsReadWriteAndValidation(t *testing.T) {
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)

	getView := func(svc *Service) assistantView {
		t.Helper()
		w, view := permissionSettingsRequest(t, svc, http.MethodGet, "/api/v1/assistant", svc.managerIdentity(), "full", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/assistant: code=%d body=%s", w.Code, w.Body.String())
		}
		return view
	}
	putRaw := func(body string, wantStatus int) assistantView {
		t.Helper()
		w, view := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", body)
		if w.Code != wantStatus {
			t.Fatalf("PUT %s: got %d, want %d: %s", body, w.Code, wantStatus, w.Body.String())
		}
		return view
	}

	// Defaults when never written.
	def := getView(s)
	if def.TodosV2 {
		t.Fatalf("default todos_v2 = true, want false")
	}
	if def.TodosV2AutoSeen != todosV2AutoSeenAllAutonomous {
		t.Fatalf("default todos_v2_auto_seen = %q, want %q", def.TodosV2AutoSeen, todosV2AutoSeenAllAutonomous)
	}
	if def.TodosV2PendingReviewSLAHours != 24 {
		t.Fatalf("default todos_v2_pending_review_sla_hours = %d, want 24", def.TodosV2PendingReviewSLAHours)
	}
	if def.TodosV2CanWaitSLAHours != 48 {
		t.Fatalf("default todos_v2_can_wait_sla_hours = %d, want 48", def.TodosV2CanWaitSLAHours)
	}
	if !def.TodosV2FoldChildren {
		t.Fatalf("default todos_v2_fold_children = false, want true")
	}

	// Invalid values are rejected with 400 and do not mutate stored settings.
	invalidPayloads := []string{
		`{"todos_v2_auto_seen":"invalid"}`,
		`{"todos_v2_auto_seen":"none"}`,
		`{"todos_v2_pending_review_sla_hours":-1}`,
		`{"todos_v2_pending_review_sla_hours":6}`,
		`{"todos_v2_pending_review_sla_hours":36}`,
		`{"todos_v2_pending_review_sla_hours":72}`,
		`{"todos_v2_can_wait_sla_hours":-1}`,
		`{"todos_v2_can_wait_sla_hours":12}`,
		`{"todos_v2_can_wait_sla_hours":36}`,
		`{"todos_v2_can_wait_sla_hours":96}`,
	}
	for _, body := range invalidPayloads {
		putRaw(body, http.StatusBadRequest)
	}
	afterInvalid := getView(s)
	if afterInvalid.TodosV2AutoSeen != todosV2AutoSeenAllAutonomous ||
		afterInvalid.TodosV2PendingReviewSLAHours != 24 ||
		afterInvalid.TodosV2CanWaitSLAHours != 48 ||
		!afterInvalid.TodosV2FoldChildren {
		t.Fatalf("invalid PUTs mutated defaults: %+v", afterInvalid)
	}

	// Updating the 4 preferences without an explicit manager target header is rejected with 400 (#16198).
	w := doTodoHTTP(s, http.MethodPut, "/api/v1/assistant", `{"todos_v2_auto_seen":"subtasks","todos_v2_pending_review_sla_hours":12,"todos_v2_can_wait_sla_hours":24,"todos_v2_fold_children":false}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT prefs without explicit target header: code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	// With an explicit manager target header, updating the 4 preferences succeeds.
	putRaw(`{"todos_v2_auto_seen":"subtasks","todos_v2_pending_review_sla_hours":12,"todos_v2_can_wait_sla_hours":24,"todos_v2_fold_children":false}`, http.StatusOK)

	// Cycle through every allowed option.
	for _, mode := range []string{"off", "subtasks", "all_autonomous"} {
		body, _ := json.Marshal(map[string]any{"todos_v2_auto_seen": mode})
		v := putRaw(string(body), http.StatusOK)
		if v.TodosV2AutoSeen != mode {
			t.Fatalf("todos_v2_auto_seen = %q, want %q", v.TodosV2AutoSeen, mode)
		}
	}
	for _, h := range []int{0, 12, 24, 48} {
		body, _ := json.Marshal(map[string]any{"todos_v2_pending_review_sla_hours": h})
		v := putRaw(string(body), http.StatusOK)
		if v.TodosV2PendingReviewSLAHours != h {
			t.Fatalf("todos_v2_pending_review_sla_hours = %d, want %d", v.TodosV2PendingReviewSLAHours, h)
		}
	}
	for _, h := range []int{0, 24, 48, 72} {
		body, _ := json.Marshal(map[string]any{"todos_v2_can_wait_sla_hours": h})
		v := putRaw(string(body), http.StatusOK)
		if v.TodosV2CanWaitSLAHours != h {
			t.Fatalf("todos_v2_can_wait_sla_hours = %d, want %d", v.TodosV2CanWaitSLAHours, h)
		}
	}
	for _, fold := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"todos_v2_fold_children": fold})
		v := putRaw(string(body), http.StatusOK)
		if v.TodosV2FoldChildren != fold {
			t.Fatalf("todos_v2_fold_children = %t, want %t", v.TodosV2FoldChildren, fold)
		}
	}

	// Persist non-default values together with todos_v2=true and verify reload from store.
	putRaw(`{"todos_v2":true,"expected_todos_v2":false,"todos_v2_auto_seen":"subtasks","todos_v2_pending_review_sla_hours":0,"todos_v2_can_wait_sla_hours":72,"todos_v2_fold_children":false}`, http.StatusOK)
	// Stale expected_todos_v2 still returns 409 conflict cleanly.
	putRaw(`{"todos_v2":false,"expected_todos_v2":false}`, http.StatusConflict)

	reloaded := newServiceOn(t, f, st)
	got := getView(reloaded)
	if !got.TodosV2 || got.TodosV2AutoSeen != "subtasks" || got.TodosV2PendingReviewSLAHours != 0 || got.TodosV2CanWaitSLAHours != 72 || got.TodosV2FoldChildren {
		t.Fatalf("reloaded prefs mismatch: todos_v2=%t auto_seen=%q pending=%d can_wait=%d fold=%t",
			got.TodosV2, got.TodosV2AutoSeen, got.TodosV2PendingReviewSLAHours, got.TodosV2CanWaitSLAHours, got.TodosV2FoldChildren)
	}
}

func TestTodosV2AttentionPrefsAutoSeenModesAndUndo(t *testing.T) {
	s, _, _ := briefService(t, "Auto-seen attention preference modes")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	makeDoneTodo := func(title, partOf, audience string) string {
		t.Helper()
		var edit db.TodoEdit
		if partOf != "" {
			edit.PartOf = &partOf
		}
		if audience != "" {
			edit.Audience = &audience
		}
		td, err := s.createPieceTodo(ctx, "conv1", title, "", edit)
		if err != nil {
			t.Fatal(err)
		}
		s.todos.mu.Lock()
		doneAt := now
		s.todos.todos[td.ID].DoneAt = &doneAt
		s.todos.todos[td.ID].SeenAt = nil
		s.todos.todos[td.ID].AutoSeenUndone = false
		s.todos.mu.Unlock()
		return td.ID
	}

	setPrefs := func(todosV2 bool, mode string) {
		t.Helper()
		if _, err := s.updateSettings(ctx, func(v *Settings) error {
			on := todosV2
			v.TodosV2 = &on
			v.TodosV2AutoSeen = mode
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	autoSee := func(id string) {
		completed, _ := s.todoNow(id)
		s.autoSeeAcceptedTodo(ctx, completionOf(completed), outcomeDone, false)
	}
	parentID := makeDoneTodo("Parent goal", "", db.AudienceHuman)

	// 1. todos_v2=false bypasses auto-seen even when mode is all_autonomous.
	setPrefs(false, "all_autonomous")
	offTop := makeDoneTodo("Off top autonomous", "", db.AudienceAutonomous)
	offSub := makeDoneTodo("Off sub autonomous", parentID, db.AudienceAutonomous)
	autoSee(offTop)
	autoSee(offSub)
	if cur, _ := s.todoNow(offTop); cur.SeenAt != nil {
		t.Fatalf("todos_v2=false unexpectedly auto-saw top task")
	}
	if cur, _ := s.todoNow(offSub); cur.SeenAt != nil {
		t.Fatalf("todos_v2=false unexpectedly auto-saw subtask")
	}

	// 2. todos_v2=true, mode="off": neither top-level nor subtask is auto-seen.
	setPrefs(true, "off")
	modeOffTop := makeDoneTodo("Mode off top", "", db.AudienceAutonomous)
	modeOffSub := makeDoneTodo("Mode off sub", parentID, db.AudienceAutonomous)
	autoSee(modeOffTop)
	autoSee(modeOffSub)
	if cur, _ := s.todoNow(modeOffTop); cur.SeenAt != nil {
		t.Fatalf("mode=off unexpectedly auto-saw top task")
	}
	if cur, _ := s.todoNow(modeOffSub); cur.SeenAt != nil {
		t.Fatalf("mode=off unexpectedly auto-saw subtask")
	}

	// 3. todos_v2=true, mode="subtasks": only part_of autonomous subtasks are auto-seen.
	setPrefs(true, "subtasks")
	subModeTop := makeDoneTodo("Subtasks mode top autonomous", "", db.AudienceAutonomous)
	subModeChild := makeDoneTodo("Subtasks mode child autonomous", parentID, db.AudienceAutonomous)
	subModeHumanChild := makeDoneTodo("Subtasks mode child human", parentID, db.AudienceHuman)
	autoSee(subModeTop)
	autoSee(subModeChild)
	autoSee(subModeHumanChild)
	if cur, _ := s.todoNow(subModeTop); cur.SeenAt != nil {
		t.Fatalf("mode=subtasks unexpectedly auto-saw top-level autonomous task")
	}
	if cur, _ := s.todoNow(subModeChild); cur.SeenAt == nil {
		t.Fatalf("mode=subtasks failed to auto-see autonomous subtask")
	}
	if cur, _ := s.todoNow(subModeHumanChild); cur.SeenAt != nil {
		t.Fatalf("mode=subtasks unexpectedly auto-saw human subtask")
	}

	// Undo protection (DELETE /api/v1/todos/{id}/seen -> AutoSeenUndone=true) always wins in subtasks mode.
	w := doTodoHTTP(s, http.MethodDelete, "/api/v1/todos/"+subModeChild+"/seen", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE seen: code=%d body=%s", w.Code, w.Body.String())
	}
	autoSee(subModeChild)
	if cur, _ := s.todoNow(subModeChild); cur.SeenAt != nil || !cur.AutoSeenUndone {
		t.Fatalf("mode=subtasks overrode AutoSeenUndone: seen_at=%v undone=%t", cur.SeenAt, cur.AutoSeenUndone)
	}

	// 4. todos_v2=true, mode="all_autonomous": both top-level and subtask autonomous are auto-seen,
	// while AutoSeenUndone still blocks re-auto-seeing.
	setPrefs(true, "all_autonomous")
	allModeTop := makeDoneTodo("All mode top autonomous", "", db.AudienceAutonomous)
	allModeChild := makeDoneTodo("All mode child autonomous", parentID, db.AudienceAutonomous)
	autoSee(allModeTop)
	autoSee(allModeChild)
	autoSee(subModeChild)
	if cur, _ := s.todoNow(allModeTop); cur.SeenAt == nil {
		t.Fatalf("mode=all_autonomous failed to auto-see top-level autonomous task")
	}
	if cur, _ := s.todoNow(allModeChild); cur.SeenAt == nil {
		t.Fatalf("mode=all_autonomous failed to auto-see autonomous subtask")
	}
	if cur, _ := s.todoNow(subModeChild); cur.SeenAt != nil {
		t.Fatalf("mode=all_autonomous overrode AutoSeenUndone")
	}
}

func TestTodosV2AttentionPrefsPendingReviewSLA(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	makeFacts := func(todosV2 bool, hours int, disabled bool) projection.Facts {
		since := base
		return projection.Facts{
			ID:                       "todo_pr_sla",
			Title:                    "Pending review task",
			CreatedAt:                base.Add(-time.Hour),
			UpdatedAt:                base,
			TodosV2:                  todosV2,
			PendingReviewSLAHours:    hours,
			PendingReviewSLADisabled: disabled,
			Supervision: &projection.Supervision{
				State:        reviewPending,
				Supervisor:   "conv_sup",
				PendingSince: &since,
				UpdatedAt:    base,
			},
		}
	}

	// todos_v2=false: completely ignores SLA fields (legacy just_finished).
	legacy := projection.View(makeFacts(false, 0, true), base.Add(time.Minute))
	if legacy.Section != assistantwire.TodoListSectionJustFinished {
		t.Fatalf("todos_v2=false section = %q, want %q", legacy.Section, assistantwire.TodoListSectionJustFinished)
	}

	// todos_v2=true, SLA=0 (disabled): surfaces immediately to needs_you.
	imm := projection.View(makeFacts(true, 0, true), base.Add(time.Second))
	if imm.Section != assistantwire.TodoListSectionNeedsYou || imm.Reason != assistantwire.TodoReasonOwnerDecides {
		t.Fatalf("SLA=0 view = %+v, want needs_you/owner_decides", imm)
	}

	// todos_v2=true, SLA=12/24/48h: quiet in working before deadline, escalates to needs_you at deadline.
	for _, hours := range []int{12, 24, 48} {
		f := makeFacts(true, hours, false)
		before := projection.View(f, base.Add(time.Duration(hours)*time.Hour-time.Minute))
		if before.Section != assistantwire.TodoListSectionWorking || before.Reason != assistantwire.TodoReasonPersonaWorking {
			t.Fatalf("SLA=%dh before deadline: got %+v, want working/persona_working", hours, before)
		}
		wantChange := base.Add(time.Duration(hours) * time.Hour)
		if !before.NextChangeAt.Equal(wantChange) {
			t.Fatalf("SLA=%dh next_change_at = %v, want %v", hours, before.NextChangeAt, wantChange)
		}
		at := projection.View(f, wantChange)
		if at.Section != assistantwire.TodoListSectionNeedsYou || at.Reason != assistantwire.TodoReasonOwnerDecides {
			t.Fatalf("SLA=%dh at deadline: got %+v, want needs_you/owner_decides", hours, at)
		}
	}

	// End-to-end through Service.todoProjectionOf + Settings.
	s, _, _ := briefService(t, "Pending review SLA wiring")
	ctx := context.Background()
	td, err := s.createPieceTodo(ctx, "conv1", "Pending review HTTP task", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	pendingAt := time.Now().UTC().Add(-15 * time.Hour).Truncate(time.Second)
	s.todos.mu.Lock()
	s.todos.todos[td.ID].Supervision = &Supervision{
		State:        reviewPending,
		Supervisor:   "conv_sup",
		PendingSince: &pendingAt,
		UpdatedAt:    pendingAt,
	}
	s.todos.mu.Unlock()

	setSLA := func(v2 bool, hours int) {
		t.Helper()
		if _, err := s.updateSettings(ctx, func(v *Settings) error {
			on := v2
			h := hours
			v.TodosV2 = &on
			v.TodosV2PendingReviewSLAHours = &h
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sectionOf := func() string {
		t.Helper()
		list, p, err := s.todosViewed(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		_ = list
		return p.byID()[td.ID].Section
	}

	// 15h old pending_review:
	// - todos_v2=false (even with SLA=12 saved) -> legacy just_finished
	setSLA(false, 12)
	if got := sectionOf(); got != assistantwire.TodoListSectionJustFinished {
		t.Fatalf("todos_v2=false 15h pending_review section = %q, want just_finished", got)
	}
	// - todos_v2=true, SLA=24 -> working
	setSLA(true, 24)
	if got := sectionOf(); got != assistantwire.TodoListSectionWorking {
		t.Fatalf("todos_v2=true SLA=24 15h pending_review section = %q, want working", got)
	}
	// - todos_v2=true, SLA=12 -> needs_you
	setSLA(true, 12)
	if got := sectionOf(); got != assistantwire.TodoListSectionNeedsYou {
		t.Fatalf("todos_v2=true SLA=12 15h pending_review section = %q, want needs_you", got)
	}
	// - todos_v2=true, SLA=0 -> needs_you immediately
	setSLA(true, 0)
	if got := sectionOf(); got != assistantwire.TodoListSectionNeedsYou {
		t.Fatalf("todos_v2=true SLA=0 pending_review section = %q, want needs_you", got)
	}
	// - todos_v2=false rollback -> legacy just_finished
	setSLA(false, 0)
	if got := sectionOf(); got != assistantwire.TodoListSectionJustFinished {
		t.Fatalf("rollback todos_v2=false pending_review section = %q, want just_finished", got)
	}
}

func TestTodosV2AttentionPrefsCanWaitSLA(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	makeFacts := func(todosV2 bool, hours int, disabled bool) projection.Facts {
		return projection.Facts{
			ID:                 "todo_cw_sla",
			Title:              "Can-wait decision task",
			CreatedAt:          base.Add(-time.Hour),
			UpdatedAt:          base,
			TodosV2:            todosV2,
			CanWaitSLAHours:    hours,
			CanWaitSLADisabled: disabled,
			Ask: &projection.Ask{
				Outcome:     outcomeDecide,
				Verdict:     "decide",
				Question:    "Pick color?",
				AskedAt:     base,
				ForBriefing: true,
			},
		}
	}

	// todos_v2=false: never escalates even after 200h.
	legacy := projection.View(makeFacts(false, 24, false), base.Add(200*time.Hour))
	if legacy.Section != assistantwire.TodoListSectionCanWait || !legacy.CanWait {
		t.Fatalf("todos_v2=false can_wait = %+v, want can_wait", legacy)
	}

	// todos_v2=true, SLA=0 (disabled): never auto-escalates even after 200h.
	never := projection.View(makeFacts(true, 0, true), base.Add(200*time.Hour))
	if never.Section != assistantwire.TodoListSectionCanWait || !never.CanWait {
		t.Fatalf("SLA=0 can_wait = %+v, want can_wait", never)
	}
	if !never.NextChangeAt.IsZero() {
		t.Fatalf("SLA=0 can_wait next_change_at = %v, want zero", never.NextChangeAt)
	}

	// todos_v2=true, SLA=24/48/72h: stays in can_wait before deadline, ages into needs_you at deadline.
	for _, hours := range []int{24, 48, 72} {
		f := makeFacts(true, hours, false)
		before := projection.View(f, base.Add(time.Duration(hours)*time.Hour-time.Minute))
		if before.Section != assistantwire.TodoListSectionCanWait || !before.CanWait {
			t.Fatalf("SLA=%dh before deadline: got %+v, want can_wait", hours, before)
		}
		wantChange := base.Add(time.Duration(hours) * time.Hour)
		if !before.NextChangeAt.Equal(wantChange) {
			t.Fatalf("SLA=%dh next_change_at = %v, want %v", hours, before.NextChangeAt, wantChange)
		}
		at := projection.View(f, wantChange)
		if at.Section != assistantwire.TodoListSectionNeedsYou || at.CanWait {
			t.Fatalf("SLA=%dh at deadline: got %+v, want needs_you (CanWait=false)", hours, at)
		}
	}

	// End-to-end through Service.todoProjectionOf + Settings.
	s, _, _ := briefService(t, "Can-wait SLA wiring")
	ctx := context.Background()
	td, err := s.createPieceTodo(ctx, "conv1", "Can-wait HTTP task", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	askedAt := now.Add(-30 * time.Hour)
	rows := []listedTodo{{
		Todo: td,
		Ask: &todoAsk{
			Outcome:     outcomeDecide,
			Verdict:     "Needs your decision",
			AskedAt:     askedAt,
			Question:    "Optional style choice?",
			ForBriefing: true,
		},
	}}

	setSLA := func(v2 bool, hours int) {
		t.Helper()
		if _, err := s.updateSettings(ctx, func(v *Settings) error {
			on := v2
			h := hours
			v.TodosV2 = &on
			v.TodosV2CanWaitSLAHours = &h
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sectionOf := func() string {
		t.Helper()
		return s.todoProjectionOf(ctx, rows, now).byID()[td.ID].Section
	}

	// 30h old can_wait decision:
	// - todos_v2=false (even with SLA=24 saved) -> stays can_wait
	setSLA(false, 24)
	if got := sectionOf(); got != assistantwire.TodoListSectionCanWait {
		t.Fatalf("todos_v2=false 30h can_wait section = %q, want can_wait", got)
	}
	// - todos_v2=true, SLA=48 -> stays can_wait
	setSLA(true, 48)
	if got := sectionOf(); got != assistantwire.TodoListSectionCanWait {
		t.Fatalf("todos_v2=true SLA=48 30h can_wait section = %q, want can_wait", got)
	}
	// - todos_v2=true, SLA=24 -> escalates to needs_you
	setSLA(true, 24)
	if got := sectionOf(); got != assistantwire.TodoListSectionNeedsYou {
		t.Fatalf("todos_v2=true SLA=24 30h can_wait section = %q, want needs_you", got)
	}
	// - todos_v2=true, SLA=0 -> never escalates (can_wait)
	setSLA(true, 0)
	if got := sectionOf(); got != assistantwire.TodoListSectionCanWait {
		t.Fatalf("todos_v2=true SLA=0 30h can_wait section = %q, want can_wait", got)
	}
	// - todos_v2=false rollback -> stays can_wait
	setSLA(false, 24)
	if got := sectionOf(); got != assistantwire.TodoListSectionCanWait {
		t.Fatalf("rollback todos_v2=false can_wait section = %q, want can_wait", got)
	}
}

func TestTodosV2AttentionPrefsFoldChildrenAndRollback(t *testing.T) {
	s, _, _ := briefService(t, "Board projection fold_children preference and rollback")

	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Goal Fold Pref"}); bad {
		t.Fatal(out)
	}
	goal, ok := todoByTitle(s, "Goal Fold Pref")
	if !ok {
		t.Fatal("missing Goal Fold Pref")
	}
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Child Fold Quiet", "part_of": goal.ID}); bad {
		t.Fatal(out)
	}
	childQuiet, ok := todoByTitle(s, "Child Fold Quiet")
	if !ok {
		t.Fatal("missing Child Fold Quiet")
	}
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Child Fold Blocked", "part_of": goal.ID}); bad {
		t.Fatal(out)
	}
	childBlocked, ok := todoByTitle(s, "Child Fold Blocked")
	if !ok {
		t.Fatal("missing Child Fold Blocked")
	}
	s.todos.mu.Lock()
	s.todos.todos[childBlocked.ID].StartError = "launch error"
	s.todos.mu.Unlock()

	// Save non-default preferences while todos_v2 is still OFF: legacy response must stay untouched.
	w, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", `{"todos_v2_fold_children":false,"todos_v2_pending_review_sla_hours":12,"todos_v2_can_wait_sla_hours":24,"todos_v2_auto_seen":"off"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT prefs while off: code=%d body=%s", w.Code, w.Body.String())
	}
	baselineResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if baselineResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos baseline: code=%d body=%s", baselineResp.Code, baselineResp.Body.String())
	}
	baselineBytes := append([]byte(nil), baselineResp.Body.Bytes()...)
	var baselineRaw map[string]json.RawMessage
	if err := json.Unmarshal(baselineBytes, &baselineRaw); err != nil {
		t.Fatal(err)
	}
	if _, hasBoard := baselineRaw["board"]; hasBoard {
		t.Fatalf("todos_v2=false must omit board even when prefs are set")
	}

	// Enable todos_v2=true with fold_children=false -> all 3 open todos appear flat.
	setTodosV2ForTest(t, s, true, false)
	flatResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if flatResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos flat: code=%d body=%s", flatResp.Code, flatResp.Body.String())
	}
	var flatPayload struct {
		Board *todoBoardProjection `json:"board"`
	}
	if err := json.Unmarshal(flatResp.Body.Bytes(), &flatPayload); err != nil {
		t.Fatal(err)
	}
	if flatPayload.Board == nil {
		t.Fatal("expected board when todos_v2=true")
	}
	for _, g := range flatPayload.Board.StatusGroups {
		if g.VisibleCount != g.TotalCount {
			t.Fatalf("fold_children=false group %q: visible=%d != total=%d", g.Key, g.VisibleCount, g.TotalCount)
		}
		for _, e := range g.Entries {
			if len(e.FoldedChildIDs) != 0 || e.FoldedCount != 0 || len(e.SurfacedChildIDs) != 0 {
				t.Fatalf("fold_children=false entry %+v unexpectedly has folded/surfaced children", e)
			}
		}
	}
	if len(flatPayload.Board.SidebarTree) != 3 {
		t.Fatalf("fold_children=false sidebar_tree len = %d, want 3 flat open nodes: %+v", len(flatPayload.Board.SidebarTree), flatPayload.Board.SidebarTree)
	}
	for _, n := range flatPayload.Board.SidebarTree {
		if n.FoldedCount != 0 || len(n.ChildIDs) != 0 || len(n.SurfacedChildIDs) != 0 {
			t.Fatalf("fold_children=false sidebar node %+v unexpectedly has folded/surfaced children", n)
		}
	}

	// Toggle fold_children=true -> childQuiet folds under goal, sidebar_tree drops to 2 nodes.
	w, _ = permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", `{"todos_v2_fold_children":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT fold_children=true: code=%d body=%s", w.Code, w.Body.String())
	}
	foldedResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if foldedResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos folded: code=%d body=%s", foldedResp.Code, foldedResp.Body.String())
	}
	var foldedPayload struct {
		Board *todoBoardProjection `json:"board"`
	}
	if err := json.Unmarshal(foldedResp.Body.Bytes(), &foldedPayload); err != nil {
		t.Fatal(err)
	}
	if len(foldedPayload.Board.SidebarTree) != 2 {
		t.Fatalf("fold_children=true sidebar_tree len = %d, want 2", len(foldedPayload.Board.SidebarTree))
	}
	todoGroup := requireBoardStatusGroup(t, foldedPayload.Board, boardSectionTodo)
	if todoGroup.TotalCount != 1 || todoGroup.VisibleCount != 0 {
		t.Fatalf("fold_children=true todo group = %+v, want total=1 visible=0", todoGroup)
	}
	if got, want := foldedPayload.Board.Counts, flatPayload.Board.Counts; got != want || got.NeedsYou != 1 || got.OpenTotal != 1 {
		t.Fatalf("folding changed counts or counted steps separately: folded=%+v flat=%+v", got, want)
	}
	var foundGoalFold bool
	for _, e := range foldedPayload.Board.StatusGroups[0].Entries {
		if e.TodoID == goal.ID && e.FoldedCount == 1 && reflect.DeepEqual(e.FoldedChildIDs, []string{childQuiet.ID}) {
			foundGoalFold = true
		}
	}
	if !foundGoalFold {
		t.Fatalf("fold_children=true did not fold %s under %s: %+v", childQuiet.ID, goal.ID, foldedPayload.Board.StatusGroups[0].Entries)
	}

	// Rollback todos_v2=false -> exact byte-for-byte match with baseline.
	setTodosV2ForTest(t, s, false, true)
	rollbackResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if rollbackResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos rollback: code=%d body=%s", rollbackResp.Code, rollbackResp.Body.String())
	}
	if !bytes.Equal(rollbackResp.Body.Bytes(), baselineBytes) {
		t.Fatalf("rollback bytes diverged from baseline:\ngot:  %s\nwant: %s", rollbackResp.Body.String(), string(baselineBytes))
	}

	// Re-enable todos_v2=true -> saved preferences survive intact.
	setTodosV2ForTest(t, s, true, false)
	_, afterView := permissionSettingsRequest(t, s, http.MethodGet, "/api/v1/assistant", s.managerIdentity(), "full", "")
	if !afterView.TodosV2 || afterView.TodosV2AutoSeen != "off" || afterView.TodosV2PendingReviewSLAHours != 12 || afterView.TodosV2CanWaitSLAHours != 24 || !afterView.TodosV2FoldChildren {
		t.Fatalf("re-enabled preferences mismatch: %+v", afterView)
	}
}
