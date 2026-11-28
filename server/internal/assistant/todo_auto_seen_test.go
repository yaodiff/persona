package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func setTodosV2ForTest(t *testing.T, s *Service, want, expected bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"todos_v2": want, "expected_todos_v2": expected})
	w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT todos_v2=%t: code=%d body=%s", want, w.Code, w.Body.String())
	}
}

func acceptReviewForTest(t *testing.T, s *Service, todoID string, extra map[string]any) {
	t.Helper()
	payload := map[string]any{
		"todo_id": todoID,
		"verdict": reviewAccepted,
		"note":    "Verified and merged",
		"checks": []map[string]any{
			{"criterion": "PR 合并到 master", "met": true, "evidence": "merged"},
			{"criterion": "浅色 / 深色截图贴进 PR", "met": true, "evidence": "attached"},
		},
	}
	for k, v := range extra {
		payload[k] = v
	}
	raw, _ := json.Marshal(payload)
	if out, err := s.toolTaskReview(context.Background(), "conv1", raw); err != nil {
		t.Fatalf("toolTaskReview(%s): err=%v out=%s", todoID, err, out)
	}
}

func doTodoHTTP(s *Service, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(RoleHeader, "full")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestTodosV2AutoSeenOnReviewAccept(t *testing.T) {
	cases := []struct {
		name           string
		todosV2        bool
		audience       string
		userCheck      bool
		outcome        string
		workerStatus   string
		workerState    string
		wantTodoSeen   bool
		wantWorkerSeen bool
	}{
		{
			name:           "todos_v2 off never auto-sees autonomous",
			todosV2:        false,
			audience:       db.AudienceAutonomous,
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateUnread,
			wantTodoSeen:   false,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on autonomous done auto-sees todo and finished worker",
			todosV2:        true,
			audience:       db.AudienceAutonomous,
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateUnread,
			wantTodoSeen:   true,
			wantWorkerSeen: true,
		},
		{
			name:           "todos_v2 on autonomous skips working worker without blocking",
			todosV2:        true,
			audience:       db.AudienceAutonomous,
			workerStatus:   proto.StatusWorking,
			workerState:    proto.StateWorking,
			wantTodoSeen:   true,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on autonomous skips needs_you worker without blocking",
			todosV2:        true,
			audience:       db.AudienceAutonomous,
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateNeedsYou,
			wantTodoSeen:   true,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on autonomous skips offline worker without blocking",
			todosV2:        true,
			audience:       db.AudienceAutonomous,
			workerStatus:   proto.StatusOffline,
			workerState:    proto.StateIdle,
			wantTodoSeen:   true,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on autonomous with user_check never auto-sees",
			todosV2:        true,
			audience:       db.AudienceAutonomous,
			userCheck:      true,
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateUnread,
			wantTodoSeen:   false,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on autonomous with outcome=cannot never auto-sees",
			todosV2:        true,
			audience:       db.AudienceAutonomous,
			outcome:        outcomeCannot,
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateUnread,
			wantTodoSeen:   false,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on human never auto-sees",
			todosV2:        true,
			audience:       db.AudienceHuman,
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateUnread,
			wantTodoSeen:   false,
			wantWorkerSeen: false,
		},
		{
			name:           "todos_v2 on unset audience never auto-sees",
			todosV2:        true,
			audience:       "",
			workerStatus:   proto.StatusDone,
			workerState:    proto.StateUnread,
			wantTodoSeen:   false,
			wantWorkerSeen: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			if tc.todosV2 {
				setTodosV2ForTest(t, s, true, false)
			}
			if tc.audience != "" {
				aud := tc.audience
				if _, err := s.editTodo(context.Background(), todo.ID, db.TodoEdit{Audience: &aud}); err != nil {
					t.Fatalf("set audience: %v", err)
				}
			}
			f.setSessions("box", proto.Session{
				SessionID: "s1",
				MachineID: "box",
				Cwd:       "/code/app",
				Agent:     "claude",
				Status:    tc.workerStatus,
				State:     tc.workerState,
				UpdatedAt: time.Now().UTC(),
			})

			extra := map[string]any{}
			if tc.userCheck {
				extra["user_check"] = true
			}
			if tc.outcome != "" {
				extra["outcome"] = tc.outcome
			}
			acceptReviewForTest(t, s, todo.ID, extra)

			got, ok := s.todoNow(todo.ID)
			if !ok {
				t.Fatalf("todo %s missing after accept", todo.ID)
			}
			if tc.wantTodoSeen {
				if got.SeenAt == nil {
					t.Fatalf("expected seen_at to be set, got nil")
				}
				if got.DoneAt != nil && got.SeenAt.Before(*got.DoneAt) {
					t.Fatalf("expected seen_at (%v) >= done_at (%v)", got.SeenAt, got.DoneAt)
				}
			} else if got.SeenAt != nil {
				t.Fatalf("expected seen_at to remain nil, got %v", got.SeenAt)
			}

			workerSeen := !s.seenAt("box", "s1").IsZero()
			if workerSeen != tc.wantWorkerSeen {
				t.Fatalf("worker seen = %t, want %t", workerSeen, tc.wantWorkerSeen)
			}
		})
	}
}

func TestTodosV2AutoSeenUndoProtectionAndRollback(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	setTodosV2ForTest(t, s, true, false)

	aud := db.AudienceAutonomous
	if _, err := s.editTodo(context.Background(), todo.ID, db.TodoEdit{Audience: &aud}); err != nil {
		t.Fatalf("set audience: %v", err)
	}
	f.setSessions("box", proto.Session{
		SessionID: "s1",
		MachineID: "box",
		Cwd:       "/code/app",
		Agent:     "claude",
		Status:    proto.StatusDone,
		State:     proto.StateUnread,
		UpdatedAt: time.Now().UTC(),
	})

	// 1. First acceptance auto-sees the to-do and its finished worker.
	acceptReviewForTest(t, s, todo.ID, nil)
	got, _ := s.todoNow(todo.ID)
	if got.SeenAt == nil || got.AutoSeenUndone {
		t.Fatalf("after first accept: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}
	if s.seenAt("box", "s1").IsZero() {
		t.Fatal("expected finished worker s1 to be marked seen")
	}

	// 2. Manual Undo (DELETE /api/v1/todos/{id}/seen) clears seen_at and sets persistent undo guard.
	w := doTodoHTTP(s, "DELETE", "/api/v1/todos/"+todo.ID+"/seen", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /seen: code=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "auto_seen_undone") {
		t.Fatalf("internal auto_seen_undone leaked onto wire: %s", w.Body.String())
	}
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt != nil || !got.AutoSeenUndone {
		t.Fatalf("after manual Undo: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}

	// 3. Re-acceptance in the same completion round respects manual Undo.
	acceptReviewForTest(t, s, todo.ID, nil)
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt != nil || !got.AutoSeenUndone {
		t.Fatalf("re-accept overwrote manual Undo: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}
	// 4. Manual PUT /seen works normally without clearing AutoSeenUndone.
	w = doTodoHTTP(s, "PUT", "/api/v1/todos/"+todo.ID+"/seen", "")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /seen: code=%d body=%s", w.Code, w.Body.String())
	}
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt == nil || !got.AutoSeenUndone {
		t.Fatalf("after manual PUT /seen: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}
	w = doTodoHTTP(s, "DELETE", "/api/v1/todos/"+todo.ID+"/seen", "")
	if w.Code != http.StatusOK {
		t.Fatalf("second DELETE /seen: code=%d body=%s", w.Code, w.Body.String())
	}

	// 5. Turning todos_v2 off performs lossless rollback; subsequent autonomous acceptances stay legacy.
	setTodosV2ForTest(t, s, false, true)
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt != nil || !got.AutoSeenUndone || got.Audience != db.AudienceAutonomous {
		t.Fatalf("after todos_v2 rollback: %+v", got)
	}

	// 6. Turning todos_v2 back on preserves Undo protection.
	setTodosV2ForTest(t, s, true, false)
	acceptReviewForTest(t, s, todo.ID, nil)
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt != nil || !got.AutoSeenUndone {
		t.Fatalf("after todos_v2 re-enable, re-accept overwrote Undo: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}

	// 7. Reopening ends the Undo-protected round. A late callback for it must leave the open to-do unseen.
	prior := got
	ctx := t.Context()
	got, err := s.editTodo(ctx, todo.ID, todoDoneEdit(false))
	if err != nil || got.DoneAt != nil || got.SeenAt != nil || got.AutoSeenUndone {
		t.Fatalf("reopen did not clear completion marks: todo=%+v err=%v", got, err)
	}
	s.autoSeeAcceptedTodo(ctx, completionOf(prior), outcomeDone, false)
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt != nil {
		t.Fatalf("late auto-seen marked an open to-do: %v", got.SeenAt)
	}
	acceptReviewForTest(t, s, todo.ID, nil)
	got, _ = s.todoNow(todo.ID)
	if got.SeenAt == nil || got.AutoSeenUndone {
		t.Fatalf("new completion round was not auto-seen: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}
}

func TestTodosV2AutoSeenUndoSurvivesRestart(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	setTodosV2ForTest(t, s, true, false)

	aud := db.AudienceAutonomous
	if _, err := s.editTodo(context.Background(), todo.ID, db.TodoEdit{Audience: &aud}); err != nil {
		t.Fatalf("set audience: %v", err)
	}
	f.setSessions("box", proto.Session{
		SessionID: "s1",
		MachineID: "box",
		Cwd:       "/code/app",
		Agent:     "claude",
		Status:    proto.StatusDone,
		State:     proto.StateUnread,
		UpdatedAt: time.Now().UTC(),
	})

	acceptReviewForTest(t, s, todo.ID, nil)
	w := doTodoHTTP(s, "DELETE", "/api/v1/todos/"+todo.ID+"/seen", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /seen: code=%d body=%s", w.Code, w.Body.String())
	}

	// Reload a fresh Service against the same Postgres database.
	reloaded := newServiceOn(t, f, database)
	reloaded.id = s.id
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatalf("ensureTodosLoaded: %v", err)
	}
	got, ok := reloaded.todoNow(todo.ID)
	if !ok || got.SeenAt != nil || !got.AutoSeenUndone {
		t.Fatalf("after restart: ok=%t seen_at=%v auto_seen_undone=%t", ok, got.SeenAt, got.AutoSeenUndone)
	}

	// Re-acceptance after restart still respects persisted AutoSeenUndone.
	reloaded.autoSeeAcceptedTodo(context.Background(), completionOf(got), outcomeDone, false)
	got, _ = reloaded.todoNow(todo.ID)
	if got.SeenAt != nil {
		t.Fatalf("autoSeeAcceptedTodo after restart overwrote Undo: seen_at=%v", got.SeenAt)
	}
}
