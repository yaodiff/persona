package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The To-dos view on the wire (#10226, docs/assistant/todo-status-sop.md "The hub's view"): every row of
// GET /api/v1/todos carries its view and the list its counts, GET /api/v1/todos/{id} answers one row the same way, and
// GET /api/v1/todos/places the sidebar's places. Each read works the view out once, over the whole list, through the
// adapter (todo_projection.go): a row's rank, its steps and a superseded failed start are read against the rest of the
// list, so a row reads the same in the list and on its own. Every time in a view is absolute, so the same state gives
// the same bytes and the list's ETag (#9590) holds while nothing changes. The manager names CapabilityTodoView and
// CapabilityTodoPlaces in its hello; the hub adds the counts and places of several managers up.

// todosList is GET /api/v1/todos's answer: the rows, each with its view, the list's counts, optional todos_v2 shared
// external blockers and board projection, and the cursor for closed rows beyond the bounded default window.
type todosList struct {
	Todos          []listedTodo               `json:"todos"`
	Counts         assistantwire.TodoCounts   `json:"counts"`
	SharedBlockers []projection.SharedBlocker `json:"shared_blockers,omitempty"`
	Board          *todoBoardProjection       `json:"board,omitempty"`
	// ClosedNextCursor and ClosedHasMore let a client ask the existing paginated read for closed rows older than the
	// default window. They are absent when the whole closed list is already in Todos.
	ClosedNextCursor string `json:"closed_next_cursor,omitempty"`
	ClosedHasMore    bool   `json:"closed_has_more,omitempty"`
	// Partial: an extension's list left out a to-do it cannot be told about yet (todoUntold), which is not one that
	// was deleted: read the list again later (#12216). Never on a device's list.
	Partial bool `json:"partial,omitempty"`
}

// listedTodos are the to-dos as GET /api/v1/todos lists them, newest created first: each formatted, with its decision
// review, its ask while one waits (todo_ask.go) and, while open and nobody works on it, why (tasks_orphan.go); with
// briefs also what was asked and who carried it (task_brief.go), which the view does not read. Without views: viewed
// adds them. An error when the to-dos or their decision reviews cannot be read.
func (s *Service) listedTodos(ctx context.Context, briefs bool) ([]listedTodo, error) {
	return s.listedTodosOf(ctx, briefs, nil)
}

// listedTodosOf is listedTodos of the to-dos whose ids are in ids alone; nil is every to-do.
func (s *Service) listedTodosOf(ctx context.Context, briefs bool, ids map[string]bool) ([]listedTodo, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	s.todos.mu.RLock()
	n := len(s.todos.todos)
	if ids != nil {
		n = len(ids)
	}
	list := make([]listedTodo, 0, n)
	for _, t := range s.todos.todos {
		if ids == nil || ids[t.ID] {
			list = append(list, listedTodo{Todo: formatTodo(copyTodoLocked(t))})
		}
	}
	s.todos.mu.RUnlock()
	if err := s.listDecisionReviews(ctx, list); err != nil {
		return nil, err
	}
	asks := s.todoAsks(ctx) // once the todos are let go: it reads them itself
	var links briefLinks
	if briefs {
		links = s.briefLinksNow() // every lineage below is read from these, not from all the links each time
	}
	plans := make(map[string]*TaskPlan)
	for i := range list {
		piece := &list[i]
		if piece.PartOf != "" {
			if root, ok := s.todoNow(piece.PartOf); ok && root.RequireAcceptedAfter {
				task := PlanTask{TodoID: piece.ID, RequestGate: root.ID}
				if piece.PlanID != "" {
					plan, cached := plans[piece.PlanID]
					if !cached {
						var err error
						plan, err = s.loadPlan(ctx, piece.PlanID)
						if err != nil && !errors.Is(err, errPlanNotFound) {
							return nil, err
						}
						plans[piece.PlanID] = plan
					}
					if plan != nil && plan.State == planStarted {
						for _, pt := range plan.Tasks {
							if pt.TodoID == piece.ID && pt.RequestGate == root.ID && pt.OwnerStarted {
								task.After = pt.After
								break
							}
						}
					}
				}
				blockers := s.requestBlockers(ctx, task)
				// An empty array tells clients this piece passed the manager's gate.
				piece.PrerequisiteBlockers = append([]string{}, blockers...)
			}
		}
		if briefs {
			list[i].Todo = s.withBriefFrom(list[i].Todo, links)
		}
		if a, ok := asks[list[i].ID]; ok {
			list[i].Ask = &a
		}
		if o, ok := s.orphanOf(list[i].ID); ok && list[i].DoneAt == nil {
			list[i].Orphaned = &o
		}
	}
	// Newest first; equal times by id, so unchanged state is the same bytes whatever order the map gave (the ETag).
	slices.SortFunc(list, func(a, b listedTodo) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return list, nil
}

// viewed gives every row of list its view at now and answers the list's projection.
func (s *Service) viewed(ctx context.Context, list []listedTodo, now time.Time) todoProjection {
	p := s.todoProjectionOf(ctx, list, now)
	for i := range list {
		list[i].View = &p.views[i]
		list[i].Wait = p.waits[i]
	}
	return p
}

// todosViewed is every to-do as GET /api/v1/todos lists it, each with its view now (the views' clock,
// todo_view_push.go), and the list's projection. What each row is filed under is held against what the apps were
// told, and any that reads differently tells them again (todoViewsSeen, todoViewDiffers).
func (s *Service) todosViewed(ctx context.Context, briefs bool) ([]listedTodo, todoProjection, error) {
	gen := s.todoViewGen() // before the to-dos are read: a push from then on makes these views old
	list, err := s.listedTodos(ctx, briefs)
	if err != nil {
		return nil, todoProjection{}, err
	}
	p := s.viewed(ctx, list, s.todoViewNow())
	if s.todoViewsSeen(gen, p, nil) {
		s.todoViewDiffers()
	}
	s.todosCounted(gen, p) // the counts view's to-dos (counts_view.go)
	return list, p, nil
}

// todoItem is GET /api/v1/todos/{id}'s answer: the to-do's row, and beside it the paper's rounds, results and recap
// (todo_timeline.go, #10465), plus the latest auditable context handoff when present (#12980).
type todoItem struct {
	Todo json.RawMessage `json:"todo"`
	assistantwire.TodoItemDetail
	Handoff *TodoContextHandoff `json:"handoff,omitempty"`
}

// viewedTodo is to-do id as GET /api/v1/todos/{id} answers it: its row as the list has it, with its view and its
// brief; false when there is none.
func (s *Service) viewedTodo(ctx context.Context, id string) (listedTodo, bool, error) {
	t, _, found, err := s.viewedTodoRuns(ctx, id, false)
	return t, found, err
}

// viewedTodoRuns is viewedTodo with the sessions of the to-do's active runs as its view read them (todoFactsOf),
// which its timeline reads rather than ask the computers again (todo_timeline.go).
func (s *Service) viewedTodoRuns(ctx context.Context, id string, extension bool) (listedTodo, []proto.Session, bool, error) {
	list, p, err := s.todosViewed(ctx, false)
	if err != nil {
		return listedTodo{}, nil, false, err
	}
	i := slices.IndexFunc(list, func(t listedTodo) bool { return t.ID == id })
	if i == -1 {
		return listedTodo{}, nil, false, nil
	}
	sessions := p.facts[i].Sessions
	if cfg := s.settings(ctx); cfg.todosV2On() {
		if extension {
			// The requested row passed todoScoped. Its direction can also come from another row, so
			// apply the extension's list visibility rules to the graph before resolving that field.
			list, _, err = s.extensionRows(ctx, list, p)
			if err != nil {
				return listedTodo{}, nil, false, err
			}
			i = slices.IndexFunc(list, func(t listedTodo) bool { return t.ID == id })
			if i == -1 {
				return listedTodo{}, nil, false, nil
			}
		}
		s.populateTodoEffectiveDirections(list, s.todoDirectionLabels(ctx, cfg))
	}
	t := list[i]
	t.Todo = s.withBrief(t.Todo)
	return t, sessions, true, nil
}

func (s *Service) todoViewRoutes() {
	// GET /api/v1/todos/{id}: one to-do as GET /api/v1/todos lists it, its view included, and what the paper shows of
	// its history (todoItemDetail; an extension's token as todoTimelineOf reads it) → {"todo": row, "rounds",
	// "rounds_total", "recap"?, "results"?}; an unknown id 404 todo_not_found, as is one an extension is not shown
	// (todoScoped).
	s.handle("GET /api/v1/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		s.todos.mu.RLock()
		_, known := s.todos.todos[id]
		s.todos.mu.RUnlock()
		if !known { // before the whole list's pass, which only a known id needs
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		ext := r.Header.Get(RoleHeader) == roleExtension
		t, sessions, found, err := s.viewedTodoRuns(r.Context(), id, ext)
		switch {
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
		case !found: // deleted meanwhile
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
		default:
			raw, err := json.Marshal(t)
			if err != nil {
				apierr.Write(w, http.StatusInternalServerError, err)
				return
			}
			// Its runs' sessions as its view read them: the timeline asks no computer again.
			item := todoItem{Todo: raw, TodoItemDetail: s.todoItemDetail(r.Context(), t.Todo, raw, sessions, ext)}
			if !ext {
				item.Handoff = s.latestTodoContextHandoff(r.Context(), t.ID)
			}
			apierr.WriteJSON(w, item)
		}
	})
	// GET /api/v1/todos/places: the To-dos sidebar's places with the list's counts (assistantwire.TodoPlaces).
	s.handle("GET /api/v1/todos/places", func(w http.ResponseWriter, r *http.Request) {
		_, p, err := s.todosViewed(r.Context(), false)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		apierr.WriteJSON(w, s.todoPlaces(r.Context(), p))
	})
}
