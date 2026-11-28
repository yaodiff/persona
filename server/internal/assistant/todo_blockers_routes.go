package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
)

const maxTodoBlockerBody = 64 << 10

type addTodoBlockerRequest struct {
	Kind      string `json:"kind"`
	TargetRef string `json:"target_ref"`
}

type todoBlockerHTTPItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TargetRef string `json:"target_ref"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
}

type addTodoBlockerResponse struct {
	Blocker todoBlockerHTTPItem `json:"blocker"`
}

// todoBlockersRoutes registers additive manual blocker CRUD endpoints strictly gated under todos_v2=true (#16198).
func (s *Service) todoBlockersRoutes() {
	s.handle("POST /api/v1/todos/{id}/blockers", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.Write(w, http.StatusConflict, errors.New("todo blockers require todos_v2 to be enabled"))
			return
		}
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := strings.TrimSpace(r.PathValue("id"))
		todo, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if todo.DoneAt != nil || todo.Cancelled() {
			apierr.Write(w, http.StatusConflict, db.ErrTodoCompleted)
			return
		}
		var in addTodoBlockerRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTodoBlockerBody)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		kind := strings.TrimSpace(in.Kind)
		if kind != todoBlockerKindExternalPR {
			apierr.Write(w, http.StatusBadRequest, errors.New("kind must be external_pr"))
			return
		}
		urls, err := parseBlockedByPRURLs([]string{in.TargetRef})
		if err != nil || len(urls) == 0 {
			if err == nil {
				err = errors.New("target_ref must be a valid https:// URL")
			}
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		row, err := s.AddTodoBlocker(ctx, todoID, kind, urls[0], "")
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		if row == nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("todo blockers store unavailable"))
			return
		}
		s.todosChanged(ctx)
		apierr.WriteJSON(w, addTodoBlockerResponse{
			Blocker: todoBlockerHTTPItem{
				ID:        row.ID,
				Kind:      row.Kind,
				TargetRef: row.TargetRef,
				State:     row.State,
				Attempts:  row.Attempts,
			},
		})
	})

	s.handle("DELETE /api/v1/todos/{id}/blockers/{blockerId}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.Write(w, http.StatusConflict, errors.New("todo blockers require todos_v2 to be enabled"))
			return
		}
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := strings.TrimSpace(r.PathValue("id"))
		blockerID := strings.TrimSpace(r.PathValue("blockerId"))
		if _, ok := s.todoNow(todoID); !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		st := s.todoBlockerDB()
		if st == nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("todo blockers store unavailable"))
			return
		}
		rows, err := st.ListTodoBlockersByTodo(ctx, todoID)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		var matched *db.TodoBlockerRow
		for i := range rows {
			if rows[i].ID == blockerID {
				matched = &rows[i]
				break
			}
		}
		if matched == nil {
			apierr.Write(w, http.StatusNotFound, errors.New("blocker not found"))
			return
		}
		cancelled, err := s.cancelTodoBlocker(ctx, blockerID)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		if cancelled != nil {
			s.todosChanged(ctx)
			ready, err := s.blockerUnblockStillHolds(ctx, superviseSignal{Todo: todoID, Key: blockerResolutionKey(*cancelled)})
			if ready || err != nil {
				s.emitTodoBlockerWake(ctx, todoBlockerWake{last: *cancelled})
			}
			s.sweepAdmissionQueue(ctx)
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
