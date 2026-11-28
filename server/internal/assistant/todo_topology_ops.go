package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	maxTodoSplitChildren = 20
	maxTodoTopologyBody  = 128 << 10
)

type topologyBadRequestError struct{ err error }

func (e topologyBadRequestError) Error() string { return e.err.Error() }
func (e topologyBadRequestError) Unwrap() error { return e.err }

type topologyConflictError struct{ err error }

func (e topologyConflictError) Error() string { return e.err.Error() }
func (e topologyConflictError) Unwrap() error { return e.err }

type todoSplitChildInput struct {
	Title          string   `json:"title"`
	Notes          string   `json:"notes,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	AfterIndices   []int    `json:"after_indices,omitempty"`
	After          []string `json:"after,omitempty"`
	MoveBlockerIDs []string `json:"move_blocker_ids,omitempty"`
	CopyRepoLinks  bool     `json:"copy_repo_links,omitempty"`
}

type todoSplitRequest struct {
	Children        []todoSplitChildInput `json:"children"`
	AppendAuditNote *bool                 `json:"append_audit_note,omitempty"`
}

type todoSplitChildResult struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	After []string `json:"after,omitempty"`
}

type todoSplitResponse struct {
	SourceID      string                 `json:"source_id"`
	Children      []todoSplitChildResult `json:"children"`
	MovedBlockers int                    `json:"moved_blockers"`
}

type todoMergeRequest struct {
	SourceID             string `json:"source_id"`
	ForceWithActiveAgent bool   `json:"force_with_active_agent,omitempty"`
}

type todoMergeResponse struct {
	TargetID            string   `json:"target_id"`
	SourceID            string   `json:"source_id"`
	ReparentedChildren  []string `json:"reparented_children"`
	TransferredBlockers int      `json:"transferred_blockers"`
	TransferredLinks    int      `json:"transferred_links"`
}

// todoTopologyOpsRoutes registers atomic split and merge endpoints strictly gated under todos_v2=true (#16433).
func (s *Service) todoTopologyOpsRoutes() {
	s.handle("POST /api/v1/todos/{id}/split", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.Write(w, http.StatusConflict, errors.New("todo split requires todos_v2 to be enabled"))
			return
		}
		var in todoSplitRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTodoTopologyBody)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		res, err := s.splitTodo(ctx, strings.TrimSpace(r.PathValue("id")), in)
		if err != nil {
			writeTopologyError(w, err)
			return
		}
		apierr.WriteJSON(w, res)
	})

	s.handle("POST /api/v1/todos/{id}/merge", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.Write(w, http.StatusConflict, errors.New("todo merge requires todos_v2 to be enabled"))
			return
		}
		var in todoMergeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTodoTopologyBody)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		res, err := s.mergeTodos(ctx, strings.TrimSpace(r.PathValue("id")), in)
		if err != nil {
			writeTopologyError(w, err)
			return
		}
		apierr.WriteJSON(w, res)
	})
}

func writeTopologyError(w http.ResponseWriter, err error) {
	var badReq topologyBadRequestError
	var conflict topologyConflictError
	var pe *pieceError
	switch {
	case errors.As(err, &badReq), errors.As(err, &pe):
		apierr.Write(w, http.StatusBadRequest, err)
	case errors.As(err, &conflict):
		apierr.Write(w, http.StatusConflict, err)
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errTodosUnavailable):
		apierr.Write(w, http.StatusServiceUnavailable, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

func normalizeTopologyPriority(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "", "normal":
		return "", nil
	case "high", "low":
		return p, nil
	default:
		return "", fmt.Errorf("invalid priority %q: must be high, normal, or low", raw)
	}
}

// topoOrderSplitChildren validates after_indices bounds, self-references, and acyclicity, returning a
// topological creation order where every dependency index appears before its dependents.
func topoOrderSplitChildren(children []todoSplitChildInput) ([]int, error) {
	n := len(children)
	adj := make([][]int, n)
	inDeg := make([]int, n)
	for i, c := range children {
		seen := make(map[int]bool, len(c.AfterIndices))
		for _, dep := range c.AfterIndices {
			if dep < 0 || dep >= n {
				return nil, topologyBadRequestError{err: fmt.Errorf("child %d: after_indices contains out-of-range index %d", i, dep)}
			}
			if dep == i {
				return nil, topologyBadRequestError{err: fmt.Errorf("child %d: after_indices cannot reference itself", i)}
			}
			if seen[dep] {
				continue
			}
			seen[dep] = true
			adj[dep] = append(adj[dep], i)
			inDeg[i]++
		}
	}
	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if inDeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	order := make([]int, 0, n)
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		order = append(order, u)
		for _, v := range adj[u] {
			inDeg[v]--
			if inDeg[v] == 0 {
				queue = append(queue, v)
			}
		}
	}
	if len(order) != n {
		return nil, topologyBadRequestError{err: errors.New("after_indices contains a cycle")}
	}
	return order, nil
}

// splitTodo creates children, their dependencies, copied links and the source's audit in one write.
func (s *Service) splitTodo(ctx context.Context, sourceID string, in todoSplitRequest) (todoSplitResponse, error) {
	if !s.settings(ctx).todosV2On() {
		return todoSplitResponse{}, topologyConflictError{err: errors.New("todo split requires todos_v2 to be enabled")}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return todoSplitResponse{}, errTodosUnavailable
	}
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return todoSplitResponse{}, topologyBadRequestError{err: errors.New("source todo id is required")}
	}
	if len(in.Children) == 0 || len(in.Children) > maxTodoSplitChildren {
		return todoSplitResponse{}, topologyBadRequestError{
			err: fmt.Errorf("children must contain between 1 and %d items", maxTodoSplitChildren),
		}
	}
	order, err := topoOrderSplitChildren(in.Children)
	if err != nil {
		return todoSplitResponse{}, err
	}

	children := slices.Clone(in.Children)
	blockerMoves := make(map[string]int)
	copyLinks := false
	for i, c := range children {
		c.Title = strings.TrimSpace(c.Title)
		if c.Title == "" {
			return todoSplitResponse{}, topologyBadRequestError{err: fmt.Errorf("child %d: title is required", i)}
		}
		c.Notes = strings.TrimSpace(c.Notes)
		c.Priority, err = normalizeTopologyPriority(c.Priority)
		if err != nil {
			return todoSplitResponse{}, topologyBadRequestError{err: fmt.Errorf("child %d: %w", i, err)}
		}
		c.After = cleanIDs(c.After)
		c.AfterIndices = uniqueSplitIndices(c.AfterIndices)
		c.MoveBlockerIDs = cleanIDs(c.MoveBlockerIDs)
		slices.Sort(c.MoveBlockerIDs)
		for _, bid := range c.MoveBlockerIDs {
			if prev, dup := blockerMoves[bid]; dup {
				return todoSplitResponse{}, topologyBadRequestError{
					err: fmt.Errorf("blocker %q cannot be moved to both child %d and child %d", bid, prev, i),
				}
			}
			blockerMoves[bid] = i
		}
		children[i] = c
		copyLinks = copyLinks || c.CopyRepoLinks
	}
	childIDs, err := splitChildIDs(sourceID, children)
	if err != nil {
		return todoSplitResponse{}, err
	}

	result := todoSplitResponse{SourceID: sourceID, Children: make([]todoSplitChildResult, len(children)), MovedBlockers: len(blockerMoves)}
	_, err = s.writeTodoTopology(ctx, nil, func(w *todoTopologyWrite) error {
		source, ok := s.todoNow(sourceID)
		if !ok {
			return db.ErrTodoNotFound
		}
		found := 0
		for i, id := range childIDs {
			if cur, ok := s.todoNow(id); ok {
				if cur.PartOf != sourceID {
					return topologyConflictError{err: i18n.Err("assistant-todos.split_children_changed")}
				}
				result.Children[i] = todoSplitChildResult{ID: cur.ID, Title: cur.Title, After: cur.After}
				found++
			}
		}
		if found == len(children) {
			return nil
		}
		if found != 0 {
			return topologyConflictError{err: i18n.Err("assistant-todos.split_children_changed")}
		}
		if source.DoneAt != nil || source.Cancelled() {
			return topologyConflictError{err: db.ErrTodoCompleted}
		}
		if source.PartOf != "" {
			return topologyConflictError{err: fmt.Errorf("cannot split %q: it is already a child piece of %q", source.ID, source.PartOf)}
		}

		checkIDs := []string{sourceID}
		rows := make([]db.TodoRow, len(children))
		for i, c := range children {
			for _, depID := range c.After {
				dep, exists := s.todoNow(depID)
				if !exists || dep.PartOf != source.ID {
					return topologyBadRequestError{err: fmt.Errorf("child %d: after todo %q must be a sibling child of %q", i, depID, source.ID)}
				}
				checkIDs = append(checkIDs, depID)
			}
			after := slices.Clone(c.After)
			for _, dep := range c.AfterIndices {
				after = append(after, childIDs[dep])
			}
			rows[i] = db.TodoRow{
				ID: childIDs[i], Title: c.Title, Notes: c.Notes, Priority: c.Priority, PartOf: source.ID,
				After: cleanIDs(after), DirectionID: source.DirectionID, Project: source.Project, ItemType: source.ItemType,
				CreatedAt: w.at, UpdatedAt: w.at,
			}
		}
		if err := s.checkSplitGraph(rows); err != nil {
			return err
		}
		if err := w.lockTodos(ctx, checkIDs); err != nil {
			return err
		}

		blockers := w.blockers()
		sourceBlockers, err := splitSourceBlockers(ctx, blockers, sourceID, blockerMoves)
		if err != nil {
			return err
		}
		var links []repository.LinkInput
		if copyLinks {
			links, err = w.collectRepoLinks(ctx, sourceID, true)
			if err != nil {
				return err
			}
		}
		for _, i := range order {
			row := rows[i]
			if err := w.create(ctx, row); err != nil {
				return err
			}
			if err := writeAfterBlockers(ctx, blockers, Todo{ID: row.ID, Notes: row.Notes}, row.After, "", nil, w.at); err != nil {
				return err
			}
			if children[i].CopyRepoLinks {
				if _, err := w.applyRepoLinks(ctx, row.ID, links); err != nil {
					return err
				}
			}
			for _, bid := range children[i].MoveBlockerIDs {
				if err := moveTopologyBlocker(ctx, blockers, sourceBlockers[bid], row.ID, true, w.at); err != nil {
					return err
				}
			}
			result.Children[i] = todoSplitChildResult{ID: row.ID, Title: row.Title, After: row.After}
		}
		if in.AppendAuditNote == nil || *in.AppendAuditNote {
			snippet := fmt.Sprintf("[split] Split into %d child todos: %s", len(childIDs), strings.Join(childIDs, ", "))
			notes := appendProvenanceNote(source.Notes, snippet)
			if _, err := w.edit(ctx, source, db.TodoEdit{Notes: &notes}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return todoSplitResponse{}, err
	}
	return result, nil
}

func uniqueSplitIndices(indices []int) []int {
	seen := make(map[int]bool, len(indices))
	var unique []int
	for _, id := range indices {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return unique
}

// splitChildIDs gives an identical normalized split the same IDs, including across restarts.
// The children are the durable record of the split even when its optional audit note is off.
func splitChildIDs(sourceID string, children []todoSplitChildInput) ([]string, error) {
	payload, err := json.Marshal(children)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(children))
	for i := range ids {
		sum := sha256.Sum256(fmt.Appendf(nil, "todo_split\x00%s\x00%d\x00%s", sourceID, i, payload))
		ids[i] = fmt.Sprintf("todo_%x", sum[:8])
	}
	return ids, nil
}

func (s *Service) checkSplitGraph(rows []db.TodoRow) error {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	book := maps.Clone(s.todos.todos)
	for _, row := range rows {
		book[row.ID] = &Todo{ID: row.ID, PartOf: row.PartOf, After: row.After}
	}
	for _, row := range rows {
		if loop := afterCycle(book, row.ID, row.After); loop != "" {
			return topologyBadRequestError{err: fmt.Errorf("child %q would create an after cycle through %q", row.Title, loop)}
		}
	}
	return nil
}

func splitSourceBlockers(ctx context.Context, st todoBlockerWriter, sourceID string, moves map[string]int) (map[string]db.TodoBlockerRow, error) {
	if len(moves) == 0 {
		return nil, nil
	}
	if st == nil {
		return nil, errors.New("todo blockers store unavailable")
	}
	rows, err := st.ListTodoBlockersByTodo(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]db.TodoBlockerRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	for id := range moves {
		row, found := byID[id]
		if !found {
			return nil, topologyBadRequestError{err: fmt.Errorf("move_blocker_ids: blocker %q not found on source todo %q", id, sourceID)}
		}
		if row.State != db.TodoBlockerStateActive {
			return nil, topologyConflictError{err: fmt.Errorf("move_blocker_ids: blocker %q is not active (state=%s)", id, row.State)}
		}
	}
	return byID, nil
}

// mergeTodos transfers the source's links, blockers, active decisions and children before closing it.
func (s *Service) mergeTodos(ctx context.Context, targetID string, in todoMergeRequest) (todoMergeResponse, error) {
	if !s.settings(ctx).todosV2On() {
		return todoMergeResponse{}, topologyConflictError{err: errors.New("todo merge requires todos_v2 to be enabled")}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return todoMergeResponse{}, errTodosUnavailable
	}
	targetID = strings.TrimSpace(targetID)
	sourceID := strings.TrimSpace(in.SourceID)
	if targetID == "" {
		return todoMergeResponse{}, topologyBadRequestError{err: errors.New("target todo id is required")}
	}
	if sourceID == "" {
		return todoMergeResponse{}, topologyBadRequestError{err: errors.New("source_id is required")}
	}
	if sourceID == targetID {
		return todoMergeResponse{}, topologyBadRequestError{err: errors.New("source_id and target_id must differ")}
	}

	s.loadSeen(ctx)
	_ = s.loadTaskActivity(ctx)
	var source, target Todo
	var closed *db.TodoRow
	var replay bool
	reason := "merged into " + targetID
	result := todoMergeResponse{TargetID: targetID, SourceID: sourceID, ReparentedChildren: []string{}}
	prepare := func() error {
		var ok bool
		source, ok = s.todoNow(sourceID)
		if !ok {
			return db.ErrTodoNotFound
		}
		target, ok = s.todoNow(targetID)
		if !ok {
			return db.ErrTodoNotFound
		}
		replay = source.DoneAt != nil && source.DoneBy == db.DoneByCancelled && source.DoneReason == reason
		if replay {
			return nil
		}
		if target.DoneAt != nil || target.Cancelled() {
			return topologyConflictError{err: errors.New("target todo is already closed")}
		}
		if source.DoneAt != nil || source.Cancelled() {
			return topologyConflictError{err: errors.New("source todo is already closed")}
		}
		if source.isConcern() {
			return topologyConflictError{err: db.ErrConcernUnreported}
		}
		if todoHasActiveAgent(source) && !in.ForceWithActiveAgent {
			return topologyConflictError{
				err: errors.New("source todo has an active agent; release/stop it first or pass force_with_active_agent=true"),
			}
		}
		if err := s.taskCloseSnapshotError(source, time.Now()); err != nil {
			return err
		}
		return s.reviewInvestigationCloseError(ctx, source, true)
	}
	_, err := s.writeTodoTopology(ctx, prepare, func(w *todoTopologyWrite) error {
		if replay {
			return nil
		}

		parents := s.todoTopologyAncestors(target.PartOf)
		if slices.Contains(parents, sourceID) {
			return topologyConflictError{
				err: fmt.Errorf("cannot merge %q into %q: target is a descendant of source (would create a part_of cycle)", sourceID, targetID),
			}
		}
		children, edits := s.mergeGraphEdits(sourceID, targetID)
		result.ReparentedChildren = append(result.ReparentedChildren, children...)
		editIDs := make([]string, 0, len(edits))
		for id := range edits {
			editIDs = append(editIDs, id)
		}
		slices.Sort(editIDs)
		checkIDs := append([]string{sourceID, targetID}, parents...)
		checkIDs = append(checkIDs, editIDs...)
		if err := w.lockTodos(ctx, checkIDs); err != nil {
			return err
		}

		links, err := w.collectRepoLinks(ctx, sourceID, false)
		if err != nil {
			return err
		}
		result.TransferredLinks, err = w.applyRepoLinks(ctx, targetID, links)
		if err != nil {
			return err
		}
		transferredDecisions, err := w.copyMergeDecisions(ctx, sourceID, targetID)
		if err != nil {
			return err
		}

		var prov strings.Builder
		fmt.Fprintf(&prov, "[merged from %s] %s", source.ID, source.Title)
		if notes := strings.TrimSpace(source.Notes); notes != "" {
			prov.WriteString("\n" + notes)
		}
		if transferredDecisions > 0 {
			fmt.Fprintf(&prov, "\n(carried over %d active decision(s))", transferredDecisions)
		}
		notes := appendProvenanceNote(target.Notes, prov.String())
		edits[targetID].Notes = &notes

		blockers := w.blockers()
		for _, id := range editIDs {
			cur, ok := s.todoNow(id)
			if !ok {
				return db.ErrTodoNotFound
			}
			edit := *edits[id]
			row, err := w.edit(ctx, cur, edit)
			if err != nil {
				return err
			}
			if edit.After != nil {
				cur.Notes = row.Notes
				if err := writeAfterBlockers(ctx, blockers, cur, row.After, "", nil, w.at); err != nil {
					return err
				}
			}
		}
		result.TransferredBlockers, err = mergeTopologyBlockers(ctx, blockers, sourceID, targetID, w.at)
		if err != nil {
			return err
		}

		// The source remains open unless all the target's changes and the close guard pass.
		if w.tx != nil {
			row, closedNow, err := db.ReviewDoneTodoTx(ctx, w.tx, sourceID, reason, false, w.at)
			if err != nil {
				return err
			}
			if !closedNow {
				return topologyConflictError{err: db.ErrTodoCompleted}
			}
			closed = row
		} else {
			row := todoRowOf(source)
			reviewClose(&row, reason, false, w.at)
			closed = &row
		}
		w.edited = append(w.edited, *closed)
		return s.taskCloseSnapshotError(source, time.Now())
	})
	if err != nil {
		return todoMergeResponse{}, err
	}
	if closed != nil {
		s.afterReviewClose(ctx, source, *closed, false)
		_, _ = s.ReconcileTodoBlockers(ctx, sourceID)
	}
	return result, nil
}

func (s *Service) todoTopologyAncestors(id string) []string {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var ids []string
	seen := make(map[string]bool)
	for id != "" && !seen[id] {
		t := s.todos.todos[id]
		if t == nil {
			break
		}
		seen[id] = true
		ids = append(ids, id)
		id = t.PartOf
	}
	return ids
}

func (s *Service) mergeGraphEdits(sourceID, targetID string) ([]string, map[string]*db.TodoEdit) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	edits := map[string]*db.TodoEdit{targetID: {}}
	var children, rewrites []string
	book := maps.Clone(s.todos.todos)
	for id, t := range s.todos.todos {
		if id == sourceID {
			continue
		}
		edit := edits[id]
		if edit == nil {
			edit = &db.TodoEdit{}
		}
		if t.PartOf == sourceID {
			edit.PartOf = &targetID
			children = append(children, id)
		}
		if t.DoneAt == nil && !t.Cancelled() && slices.Contains(t.After, sourceID) {
			// Remove source edges before checking the new graph for cycles.
			next := *t
			next.After = slices.DeleteFunc(slices.Clone(t.After), func(dep string) bool { return dep == sourceID })
			book[id] = &next
			rewrites = append(rewrites, id)
		}
		if edit.PartOf != nil {
			edits[id] = edit
		}
	}
	slices.Sort(children)
	slices.Sort(rewrites)
	for _, id := range rewrites {
		next := book[id]
		if id != targetID && afterCycle(book, id, []string{targetID}) == "" {
			after := slices.Clone(s.todos.todos[id].After)
			for i, dep := range after {
				if dep == sourceID {
					after[i] = targetID
				}
			}
			next.After = cleanIDs(after)
		}
		if edits[id] == nil {
			edits[id] = &db.TodoEdit{}
		}
		edits[id].After = &next.After
	}
	return children, edits
}

func (w *todoTopologyWrite) copyMergeDecisions(ctx context.Context, sourceID, targetID string) (int, error) {
	if w.tx == nil {
		return 0, nil
	}
	rows, err := db.ListTodoDecisionsTx(ctx, w.tx, sourceID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, copied := range rows {
		copied.ID = "dec_" + randomHex(8)
		copied.TodoID = targetID
		copied.SupersedesID, copied.SupersededBy = "", ""
		if copied.Reason != "" {
			copied.Reason += " (merged from " + sourceID + ")"
		} else {
			copied.Reason = "merged from " + sourceID
		}
		inserted, err := db.InsertTodoDecisionTx(ctx, w.tx, copied)
		if err != nil {
			return 0, err
		}
		if inserted {
			count++
		}
	}
	return count, nil
}

func mergeTopologyBlockers(ctx context.Context, st todoBlockerWriter, sourceID, targetID string, now time.Time) (int, error) {
	if st == nil {
		return 0, nil
	}
	source, err := st.ListTodoBlockersByTodo(ctx, sourceID)
	if err != nil {
		return 0, err
	}
	target, err := st.ListTodoBlockersByTodo(ctx, targetID)
	if err != nil {
		return 0, err
	}
	active := make(map[string]bool)
	for _, row := range target {
		if row.State == db.TodoBlockerStateActive {
			active[row.ID] = true
		}
	}
	count := 0
	for _, row := range source {
		if row.State != db.TodoBlockerStateActive {
			continue
		}
		id := todoBlockerID(targetID, row.Kind, row.TargetRef)
		copyBlocker := row.TargetRef != targetID && !active[id]
		if err := moveTopologyBlocker(ctx, st, row, targetID, copyBlocker, now); err != nil {
			return 0, err
		}
		if copyBlocker {
			count++
			active[id] = true
		}
	}
	return count, nil
}

func moveTopologyBlocker(ctx context.Context, st todoBlockerWriter, from db.TodoBlockerRow, to string, copyBlocker bool, now time.Time) error {
	if copyBlocker && from.TargetRef != to {
		row := db.TodoBlockerRow{
			ID: todoBlockerID(to, from.Kind, from.TargetRef), TodoID: to, Kind: from.Kind,
			TargetRef: from.TargetRef, ConditionJSON: from.ConditionJSON, State: db.TodoBlockerStateActive,
			CreatedAt: now, UpdatedAt: now,
		}
		if _, err := st.UpsertTodoBlocker(ctx, row); err != nil {
			return err
		}
	}
	_, err := st.CancelTodoBlocker(ctx, from.ID, now)
	return err
}

func appendProvenanceNote(existing, snippet string) string {
	existing = strings.TrimSpace(existing)
	snippet = strings.TrimSpace(snippet)
	if snippet == "" {
		return existing
	}
	if existing == "" {
		return snippet
	}
	return existing + "\n\n" + snippet
}

// collectRepoLinks reads saved repository links without looking up live provider data.
// When asReference is true, every returned link is marked with LinkRoleReference so copied links
// on split children do not hijack the source todo's delivery gate.
func (w *todoTopologyWrite) collectRepoLinks(ctx context.Context, todoID string, asReference bool) ([]repository.LinkInput, error) {
	set, err := w.repoLinks(ctx, todoID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []repository.LinkInput
	for _, row := range set.Repositories {
		for _, link := range row.Links {
			u := strings.TrimSpace(link.CanonicalURL)
			if u == "" {
				continue
			}
			key := normalizePRKey(u)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			role := link.Role
			if asReference {
				role = repository.LinkRoleReference
			} else if role == "" {
				role = repository.LinkRoleDelivery
			}
			in := repository.LinkInput{
				URL:   u,
				Title: link.Title,
				Role:  role,
			}
			if repository.IsExtensionProvider(row.SourceSnapshot.Identity.Provider) {
				linkCopy := link
				linkCopy.Role = role
				in.Resolved = &repository.ResolvedLink{
					Source: row.SourceSnapshot,
					Link:   linkCopy,
				}
			}
			out = append(out, in)
		}
	}
	return out, nil
}

// applyRepoLinks imports links onto destTodoID, deduplicating against already-saved links on destTodoID,
// and returns the count of newly added links.
func (w *todoTopologyWrite) applyRepoLinks(ctx context.Context, destTodoID string, links []repository.LinkInput) (int, error) {
	if len(links) == 0 {
		return 0, nil
	}
	set, err := w.repoLinks(ctx, destTodoID)
	if err != nil {
		return 0, err
	}
	existing := make(map[string]bool)
	for _, row := range set.Repositories {
		for _, link := range row.Links {
			if key := normalizePRKey(link.CanonicalURL); key != "" {
				existing[key] = true
			}
		}
	}
	toAdd := make([]repository.LinkInput, 0, len(links))
	for _, l := range links {
		key := normalizePRKey(l.URL)
		if key == "" || existing[key] {
			continue
		}
		existing[key] = true
		toAdd = append(toAdd, l)
	}
	if len(toAdd) == 0 {
		return 0, nil
	}
	if w.tx != nil {
		_, _, err := repositorystore.ImportTodoRepositoryLinkDetailsTx(ctx, w.tx, destTodoID, toAdd, "explicit")
		if err != nil {
			return 0, err
		}
		return len(toAdd), nil
	}
	if st, ok := w.s.store().(todoRepositoryLinkDetailsStore); ok {
		_, _, err := st.ImportTodoRepositoryLinkDetails(ctx, destTodoID, toAdd, "explicit")
		if err != nil {
			return 0, err
		}
		return len(toAdd), nil
	}
	if st, ok := w.s.store().(todoRepositoryImportStore); ok {
		urls := make([]string, 0, len(toAdd))
		for _, link := range toAdd {
			urls = append(urls, link.URL)
		}
		_, _, err := st.ImportTodoRepositoryLinks(ctx, destTodoID, urls, "explicit")
		if err != nil {
			return 0, err
		}
		return len(toAdd), nil
	}
	return 0, nil
}

func (w *todoTopologyWrite) repoLinks(ctx context.Context, todoID string) (repository.TaskSet, error) {
	st, ok := w.s.store().(todoRepositoryStore)
	if !ok {
		return repository.TaskSet{}, nil
	}
	if w.tx != nil {
		return repositorystore.ReadTodoRepositoriesTx(ctx, w.tx, todoID, nil)
	}
	return st.TodoRepositories(ctx, todoID)
}

// ---- Assistant MCP tool handlers ----

func (s *Service) toolTodoSplit(ctx context.Context, args json.RawMessage) (string, error) {
	if !s.settings(ctx).todosV2On() {
		return "", errors.New("todo_split requires todos_v2 to be enabled")
	}
	var a struct {
		ID              string                `json:"id"`
		Children        []todoSplitChildInput `json:"children"`
		AppendAuditNote *bool                 `json:"append_audit_note,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	res, err := s.splitTodo(ctx, a.ID, todoSplitRequest{
		Children:        a.Children,
		AppendAuditNote: a.AppendAuditNote,
	})
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(res)
	return string(raw), err
}

func (s *Service) toolTodoMerge(ctx context.Context, args json.RawMessage) (string, error) {
	if !s.settings(ctx).todosV2On() {
		return "", errors.New("todo_merge requires todos_v2 to be enabled")
	}
	var a struct {
		TargetID             string `json:"target_id"`
		SourceID             string `json:"source_id"`
		ForceWithActiveAgent bool   `json:"force_with_active_agent,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	res, err := s.mergeTodos(ctx, a.TargetID, todoMergeRequest{
		SourceID:             a.SourceID,
		ForceWithActiveAgent: a.ForceWithActiveAgent,
	})
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(res)
	return string(raw), err
}
