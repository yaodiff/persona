package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

const maxReorganizeIDs = 100

// todoReorganizeRequest is the body of POST /api/v1/todos/reorganize (#14704).
// It allows reorganizing any to-do (or parent/child subtree) in any state without interrupting running Agents
// or mutating origin / todo_agents.
type todoReorganizeRequest struct {
	IDs                 []string `json:"ids"`
	PartOf              *string  `json:"part_of"`
	DirectionID         *string  `json:"direction_id"`
	IncludeChildren     bool     `json:"include_children"`
	TransferSupervision bool     `json:"transfer_supervision"`
}

// todoReorganizeResponse is the answer of POST /api/v1/todos/reorganize (#14704).
type todoReorganizeResponse struct {
	Updated        []Todo   `json:"updated"`
	TransferredIDs []string `json:"transferred_ids,omitempty"`
}

type reorganizeBadRequestError struct{ err error }

func (e reorganizeBadRequestError) Error() string { return e.err.Error() }
func (e reorganizeBadRequestError) Unwrap() error { return e.err }

// todoReorganizeRoute registers POST /api/v1/todos/reorganize (#14704).
func (s *Service) todoReorganizeRoute() {
	s.handle("POST /api/v1/todos/reorganize", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if !s.settings(r.Context()).todosV2On() {
			apierr.WriteCode(w, http.StatusConflict, "todos_v2_disabled", i18n.Err("assistant-todos.reorganize_requires_v2"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var in todoReorganizeRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		res, err := s.reorganizeTodos(r.Context(), in)
		if err != nil {
			var badReq reorganizeBadRequestError
			var conflict topologyConflictError
			switch {
			case errors.As(err, &badReq):
				apierr.Write(w, http.StatusBadRequest, err)
			case errors.As(err, &conflict):
				apierr.Write(w, http.StatusConflict, err)
			case errors.Is(err, db.ErrTodoNotFound):
				apierr.Write(w, http.StatusNotFound, err)
			default:
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, res)
	})
}

// reorganizeTodos validates and applies a batch part_of / direction_id reorganization, then optionally hands over
// runtime supervision for open supervised tasks when explicitly requested and targeting an active direction leader.
func (s *Service) reorganizeTodos(ctx context.Context, in todoReorganizeRequest) (todoReorganizeResponse, error) {
	ids := cleanIDs(in.IDs)
	if len(ids) == 0 || len(ids) > maxReorganizeIDs {
		return todoReorganizeResponse{}, reorganizeBadRequestError{
			err: fmt.Errorf("ids must contain between 1 and %d todo ids", maxReorganizeIDs),
		}
	}
	if in.PartOf == nil && in.DirectionID == nil {
		return todoReorganizeResponse{}, reorganizeBadRequestError{
			err: errors.New("at least one of part_of or direction_id must be specified"),
		}
	}

	var targetPartOf *string
	if in.PartOf != nil {
		p := strings.TrimSpace(*in.PartOf)
		targetPartOf = &p
	}
	var targetDirection *string
	if in.DirectionID != nil {
		d := strings.TrimSpace(*in.DirectionID)
		targetDirection = &d
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	lead := ""
	if in.TransferSupervision && targetDirection != nil && *targetDirection != "" {
		if s.isLeader(ctx, *targetDirection) && s.transferTarget(ctx, s.settings(ctx), *targetDirection) == nil {
			lead = *targetDirection
			// Keep the assignment and the ensuing routing changes ordered with ordinary transfers.
			s.supervise.routingMu.Lock()
			defer s.supervise.routingMu.Unlock()
		}
	}

	var transferred []string
	updated, err := s.writeTodoTopology(ctx, nil, func(w *todoTopologyWrite) error {
		affected, err := s.validateReorganizeLocked(ids, idSet, targetPartOf, targetDirection != nil && in.IncludeChildren)
		if err != nil {
			return err
		}
		checkIDs := slices.Clone(affected)
		if targetPartOf != nil {
			checkIDs = append(checkIDs, s.todoTopologyAncestors(*targetPartOf)...)
		}
		if err := w.lockTodos(ctx, checkIDs); err != nil {
			return err
		}
		w.ledgers = make(map[string]*Supervision)
		blockers := w.blockers()
		for _, tid := range affected {
			cur, ok := s.todoNow(tid)
			if !ok {
				return db.ErrTodoNotFound
			}
			edit := db.TodoEdit{DirectionID: targetDirection}
			if targetPartOf != nil && idSet[tid] {
				edit.PartOf = targetPartOf
				if cur.PartOf != *targetPartOf && len(cur.After) > 0 {
					kept := s.filterValidReorganizeAfter(tid, cur.After)
					if !slices.Equal(kept, cur.After) {
						edit.After = &kept
					}
				}
			}
			row, err := w.edit(ctx, cur, edit)
			if err != nil {
				return err
			}
			if edit.After != nil {
				if err := writeAfterBlockers(ctx, blockers, cur, row.After, "", nil, w.at); err != nil {
					return err
				}
			}
			if lead == "" || cur.closedForGood() || cur.Origin == "" || cur.Supervision == nil || cur.Supervision.Supervisor == lead {
				continue
			}
			if w.tx != nil {
				moved, err := db.TransferTodoSupervisionTx(ctx, w.tx, cur.Supervision.row(tid), lead, w.at)
				if err != nil {
					return err
				}
				if !moved {
					return topologyConflictError{err: i18n.Err("assistant-todos.topology_changed")}
				}
			}
			v := cur.Supervision.clone()
			v.Supervisor, v.SupervisorSource, v.UpdatedAt = lead, "explicit", w.at
			w.ledgers[tid] = v
			transferred = append(transferred, tid)
		}
		return nil
	})
	if err != nil {
		return todoReorganizeResponse{}, err
	}

	// The ledger is durable. These effects retarget work already in the supervision queues.
	for _, tid := range transferred {
		if cur, ok := s.todoNow(tid); ok && !cur.closedForGood() && cur.Supervision != nil && cur.Supervision.Supervisor == lead {
			s.taskCard(ctx, lead, tid, wakeActive)
			if err := s.transferPendingAsks(ctx, cur); err != nil {
				s.logf("todo %s: reorganize supervision routing: %v", tid, err)
			}
		}
	}
	return todoReorganizeResponse{Updated: updated, TransferredIDs: transferred}, nil
}

// validateReorganizeLocked checks that all requested ids exist, validates part_of existence and cycle-freeness
// (both self-loops and multi-hop ancestor/descendant cycles), and expands part_of descendants when requested.
// Caller must hold s.todos.writing.
func (s *Service) validateReorganizeLocked(ids []string, idSet map[string]bool, targetPartOf *string, expandChildren bool) ([]string, error) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()

	book := s.todos.todos
	for _, id := range ids {
		if _, ok := book[id]; !ok {
			return nil, fmt.Errorf("%w: %s", db.ErrTodoNotFound, id)
		}
	}

	if targetPartOf != nil && *targetPartOf != "" {
		parent := *targetPartOf
		if _, ok := book[parent]; !ok {
			return nil, reorganizeBadRequestError{
				err: fmt.Errorf("part_of: target todo %q not found", parent),
			}
		}
		// Walk ancestors of parent (cycle-safe) to reject self-cycles and multi-hop ancestor/descendant cycles.
		seen := make(map[string]bool)
		for cur := parent; cur != "" && !seen[cur]; {
			if idSet[cur] {
				if cur == parent {
					return nil, reorganizeBadRequestError{
						err: fmt.Errorf("part_of: cannot attach todo %q to itself", cur),
					}
				}
				return nil, reorganizeBadRequestError{
					err: fmt.Errorf("part_of: attaching %q under %q would create a cycle", cur, parent),
				}
			}
			seen[cur] = true
			node, ok := book[cur]
			if !ok {
				break
			}
			cur = node.PartOf
		}
	}

	affected := make([]string, 0, len(ids))
	affected = append(affected, ids...)
	if !expandChildren {
		return affected, nil
	}

	childrenByParent := make(map[string][]string)
	for _, t := range book {
		if t.PartOf != "" {
			childrenByParent[t.PartOf] = append(childrenByParent[t.PartOf], t.ID)
		}
	}
	for p := range childrenByParent {
		slices.Sort(childrenByParent[p])
	}

	visited := make(map[string]bool, len(ids))
	queue := make([]string, 0, len(ids))
	for _, id := range ids {
		visited[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[cur] {
			if visited[child] {
				continue
			}
			visited[child] = true
			affected = append(affected, child)
			queue = append(queue, child)
		}
	}
	return affected, nil
}

// filterValidReorganizeAfter keeps every existing After dependency that still points to an existing, non-self
// to-do without introducing a dependency cycle, pruning only self-references, missing targets, or cyclic edges.
// Caller must hold s.todos.writing.
func (s *Service) filterValidReorganizeAfter(tid string, after []string) []string {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()

	book := s.todos.todos
	kept := make([]string, 0, len(after))
	seen := make(map[string]bool, len(after))
	for _, raw := range after {
		dep := strings.TrimSpace(raw)
		if dep == "" || dep == tid || seen[dep] {
			continue
		}
		if _, exists := book[dep]; !exists {
			continue
		}
		if afterCycle(book, tid, []string{dep}) != "" {
			continue
		}
		seen[dep] = true
		kept = append(kept, dep)
	}
	return kept
}
