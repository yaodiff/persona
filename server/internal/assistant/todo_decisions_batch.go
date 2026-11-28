package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	maxBatchDecisionItems = 50
	maxBatchDecisionBody  = 512 << 10
)

// batchDecisionItem is one item in POST /api/v1/todos/decisions/batch (#15698).
// Each item must explicitly specify either an option or a custom reply; the server never auto-picks a default option.
type batchDecisionItem struct {
	TodoID string `json:"todo_id"`
	Option string `json:"option,omitempty"`
	Reply  string `json:"reply,omitempty"`
}

// batchDecisionRequest is the body of POST /api/v1/todos/decisions/batch (#15698).
type batchDecisionRequest struct {
	Items []batchDecisionItem `json:"items"`
}

// batchDecisionResult is the per-item outcome of POST /api/v1/todos/decisions/batch (#15698).
type batchDecisionResult struct {
	TodoID string `json:"todo_id"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// batchDecisionResponse is the answer of POST /api/v1/todos/decisions/batch (#15698).
type batchDecisionResponse struct {
	Results   []batchDecisionResult `json:"results"`
	Succeeded int                   `json:"succeeded"`
	Failed    int                   `json:"failed"`
}

// decisionGroup is one same-cause cluster returned by GET /api/v1/todos/decisions/groups (#16433).
type decisionGroup struct {
	Key       string   `json:"key"`
	CauseKind string   `json:"cause_kind"`
	Label     string   `json:"label"`
	URL       string   `json:"url,omitempty"`
	TodoIDs   []string `json:"todo_ids"`
	Count     int      `json:"count"`
}

// decisionGroupsResponse is the answer of GET /api/v1/todos/decisions/groups (#16433).
type decisionGroupsResponse struct {
	Groups       []decisionGroup `json:"groups"`
	TotalPending int             `json:"total_pending"`
}

// todoDecisionsBatchRoute registers POST /api/v1/todos/decisions/batch (#15698)
// and GET /api/v1/todos/decisions/groups (#16433).
func (s *Service) todoDecisionsBatchRoute() {
	s.handle("GET /api/v1/todos/decisions/groups", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.WriteCode(w, http.StatusConflict, "todos_v2_disabled", errors.New("todos_v2 must be enabled to group pending decisions"))
			return
		}
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		res := s.pendingDecisionGroups(ctx, r.Header.Get(RoleHeader) == roleExtension)
		apierr.WriteJSON(w, res)
	})
	s.handle("POST /api/v1/todos/decisions/batch", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var in batchDecisionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBatchDecisionBody)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		items, err := validateBatchDecisionItems(in.Items)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		res := s.resolveBatchDecisions(r.Context(), items)
		apierr.WriteJSON(w, res)
	})
}

func validateBatchDecisionItems(raw []batchDecisionItem) ([]batchDecisionItem, error) {
	if len(raw) == 0 || len(raw) > maxBatchDecisionItems {
		return nil, fmt.Errorf("items must contain between 1 and %d decision items", maxBatchDecisionItems)
	}
	seen := make(map[string]bool, len(raw))
	out := make([]batchDecisionItem, 0, len(raw))
	for i, it := range raw {
		todoID := strings.TrimSpace(it.TodoID)
		if todoID == "" {
			return nil, fmt.Errorf("items[%d]: todo_id cannot be empty", i)
		}
		if seen[todoID] {
			return nil, fmt.Errorf("items[%d]: duplicate todo_id %q", i, todoID)
		}
		seen[todoID] = true
		option := strings.TrimSpace(it.Option)
		reply := strings.TrimSpace(it.Reply)
		if option == "" && reply == "" {
			return nil, fmt.Errorf("items[%d]: option or reply must be specified", i)
		}
		if option != "" && reply != "" {
			return nil, fmt.Errorf("items[%d]: specify either option or reply, not both", i)
		}
		if utf8.RuneCountInString(reply) > decisionTextMax {
			return nil, fmt.Errorf("items[%d]: reply is longer than %d characters", i, decisionTextMax)
		}
		out = append(out, batchDecisionItem{
			TodoID: todoID,
			Option: option,
			Reply:  reply,
		})
	}
	return out, nil
}

func (s *Service) resolveBatchDecisions(ctx context.Context, items []batchDecisionItem) batchDecisionResponse {
	results := make([]batchDecisionResult, 0, len(items))
	succeeded, failed := 0, 0
	for _, item := range items {
		res := s.resolveBatchDecisionItem(ctx, item)
		results = append(results, res)
		if res.OK {
			succeeded++
		} else {
			failed++
		}
	}
	if succeeded > 0 {
		s.todosChanged(ctx)
	}
	return batchDecisionResponse{
		Results:   results,
		Succeeded: succeeded,
		Failed:    failed,
	}
}

func (s *Service) resolveBatchDecisionItem(ctx context.Context, item batchDecisionItem) batchDecisionResult {
	t, ok := s.todoNow(item.TodoID)
	if !ok {
		return batchDecisionResult{
			TodoID: item.TodoID,
			OK:     false,
			Status: http.StatusNotFound,
			Error:  "todo not found",
		}
	}
	if t.DoneAt != nil {
		return batchDecisionResult{
			TodoID: item.TodoID,
			OK:     false,
			Status: http.StatusConflict,
			Error:  db.ErrTodoCompleted.Error(),
		}
	}
	a, err := s.askRound(item.TodoID, "")
	if err != nil {
		return batchDecisionResult{
			TodoID: item.TodoID,
			OK:     false,
			Status: batchDecisionErrStatus(err),
			Error:  s.batchDecisionErrText(ctx, err),
		}
	}
	if a == nil {
		return batchDecisionResult{
			TodoID: item.TodoID,
			OK:     false,
			Status: http.StatusConflict,
			Error:  s.batchDecisionErrText(ctx, errAskStale),
		}
	}
	round := a.AskedAt.UTC().Format(timeKeyLayout)
	var result acceptResult
	if item.Option != "" {
		result, err = s.chooseOption(ctx, item.TodoID, round, item.Option)
	} else {
		result, err = s.answerDecisionText(ctx, item.TodoID, round, item.Reply)
	}
	if errors.Is(err, db.ErrTodoCompleted) && writeDelivered(err) == "none" {
		err = errAskStale
	}
	if err != nil {
		return batchDecisionResult{
			TodoID: item.TodoID,
			OK:     false,
			Status: batchDecisionErrStatus(err),
			Error:  s.batchDecisionErrText(ctx, err),
		}
	}
	if !result.taken {
		return batchDecisionResult{
			TodoID: item.TodoID,
			OK:     false,
			Status: http.StatusConflict,
			Error:  s.batchDecisionErrText(ctx, errAskStale),
		}
	}
	return batchDecisionResult{
		TodoID: item.TodoID,
		OK:     true,
	}
}

func batchDecisionErrStatus(err error) int {
	if writeDelivered(err) != "none" {
		return http.StatusBadGateway
	}
	switch {
	case errors.Is(err, errAskStale), errors.Is(err, db.ErrTodoCompleted):
		return http.StatusConflict
	case errors.Is(err, errDecisionOption):
		return http.StatusBadRequest
	case errors.Is(err, errAcceptStore):
		return http.StatusServiceUnavailable
	case errors.Is(err, db.ErrTodoNotFound):
		return http.StatusNotFound
	case nodeRefusal(err):
		return http.StatusLocked
	default:
		return http.StatusBadGateway
	}
}

func (s *Service) batchDecisionErrText(ctx context.Context, err error) string {
	return i18n.Localize(err, s.requestCtxLang(ctx))
}

type pendingDecisionEntry struct {
	todo     Todo
	ask      *todoAsk
	blockers []db.TodoBlockerRow
}

func (s *Service) pendingDecisionGroups(ctx context.Context, isExtension bool) decisionGroupsResponse {
	s.todos.mu.RLock()
	openTodos := make([]Todo, 0, len(s.todos.todos))
	titlesByID := make(map[string]string, len(s.todos.todos))
	for _, t := range s.todos.todos {
		titlesByID[t.ID] = strings.TrimSpace(t.Title)
		if t.DoneAt == nil {
			openTodos = append(openTodos, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()

	asks := s.todoAsks(ctx)
	pending := make([]pendingDecisionEntry, 0, len(asks))
	for _, t := range openTodos {
		ask, hasAsk := asks[t.ID]
		needsUser := t.Supervision != nil && t.Supervision.State == reviewNeedsUser
		if !hasAsk && !needsUser {
			continue
		}
		var askPtr *todoAsk
		if hasAsk {
			a := ask
			askPtr = &a
		}
		pending = append(pending, pendingDecisionEntry{todo: t, ask: askPtr})
	}

	if isExtension && len(pending) > 0 {
		ids := make([]string, len(pending))
		for i := range pending {
			ids[i] = pending[i].todo.ID
		}
		hidden := s.todosHidden(ctx, ids, nil)
		filtered := pending[:0]
		for _, p := range pending {
			if !hidden[p.todo.ID] {
				filtered = append(filtered, p)
			}
		}
		pending = filtered
	}

	if len(pending) == 0 {
		return decisionGroupsResponse{Groups: []decisionGroup{}, TotalPending: 0}
	}

	// Deterministic order before counting/grouping.
	slices.SortFunc(pending, func(a, b pendingDecisionEntry) int {
		return strings.Compare(a.todo.ID, b.todo.ID)
	})

	blockersByTodo := map[string][]db.TodoBlockerRow{}
	if bst := s.todoBlockerDB(); bst != nil {
		if rows, err := bst.ListActiveTodoBlockers(ctx); err == nil {
			for _, row := range rows {
				if strings.TrimSpace(row.TargetRef) == "" {
					continue
				}
				blockersByTodo[row.TodoID] = append(blockersByTodo[row.TodoID], row)
			}
		}
	}
	for i := range pending {
		rows := blockersByTodo[pending[i].todo.ID]
		slices.SortFunc(rows, func(a, b db.TodoBlockerRow) int {
			return cmp.Or(
				strings.Compare(normalizeDecisionCauseRef(a.TargetRef), normalizeDecisionCauseRef(b.TargetRef)),
				strings.Compare(a.ID, b.ID),
			)
		})
		pending[i].blockers = rows
	}

	blockerCounts := map[string]int{}
	blockerKind := map[string]string{}
	prCounts := map[string]int{}
	parentCounts := map[string]int{}
	fallbackCounts := map[string]int{}

	for _, p := range pending {
		seenRef := map[string]bool{}
		for _, b := range p.blockers {
			ref := normalizeDecisionCauseRef(b.TargetRef)
			if ref == "" || seenRef[ref] {
				continue
			}
			seenRef[ref] = true
			blockerCounts[ref]++
			if _, ok := blockerKind[ref]; !ok {
				blockerKind[ref] = strings.TrimSpace(b.Kind)
			}
		}
		if pr := normalizeDecisionCauseRef(p.todo.PRURL); pr != "" {
			prCounts[pr]++
		}
		if parent := strings.TrimSpace(p.todo.PartOf); parent != "" {
			parentCounts[parent]++
		}
		if fk, _ := decisionFallbackKey(p); fk != "" {
			fallbackCounts[fk]++
		}
	}

	byKey := map[string]*decisionGroup{}
	addToGroup := func(key, causeKind, label, url, todoID string) {
		g, ok := byKey[key]
		if !ok {
			g = &decisionGroup{
				Key:       key,
				CauseKind: causeKind,
				Label:     label,
				URL:       url,
			}
			byKey[key] = g
		}
		g.TodoIDs = append(g.TodoIDs, todoID)
	}

	for _, p := range pending {
		// 1. Shared active persistent blocker target_ref (2+ pending decisions sharing same ref).
		bestRef, bestCount := "", 0
		for _, b := range p.blockers {
			ref := normalizeDecisionCauseRef(b.TargetRef)
			if c := blockerCounts[ref]; c >= 2 {
				if c > bestCount || (c == bestCount && (bestRef == "" || ref < bestRef)) {
					bestRef, bestCount = ref, c
				}
			}
		}
		if bestRef != "" {
			label := bestRef
			if k := blockerKind[bestRef]; k != "" {
				label = k + ": " + bestRef
			}
			url := ""
			if isHTTPURL(bestRef) {
				url = bestRef
			}
			addToGroup("blocker:"+bestRef, "shared_blocker", label, url, p.todo.ID)
			continue
		}

		// 2. Shared PRURL.
		if pr := normalizeDecisionCauseRef(p.todo.PRURL); pr != "" && prCounts[pr] >= 2 {
			url := ""
			if isHTTPURL(pr) {
				url = pr
			}
			addToGroup("pr:"+pr, "shared_pr", pr, url, p.todo.ID)
			continue
		}

		// 3. Shared PartOf parent goal.
		if parent := strings.TrimSpace(p.todo.PartOf); parent != "" && parentCounts[parent] >= 2 {
			label := titlesByID[parent]
			if label == "" {
				label = parent
			}
			addToGroup("parent:"+parent, "shared_parent", label, "", p.todo.ID)
			continue
		}

		// 4. Fallback: same direction_id (or project) + normalized ask reason, else singleton.
		if fk, reasonLabel := decisionFallbackKey(p); fk != "" && fallbackCounts[fk] >= 2 {
			addToGroup("cause:"+fk, "shared_cause", reasonLabel, "", p.todo.ID)
			continue
		}

		label := strings.TrimSpace(p.todo.Title)
		if label == "" {
			label = p.todo.ID
		}
		url := normalizeDecisionCauseRef(p.todo.PRURL)
		if url == "" && p.ask != nil {
			url = normalizeDecisionCauseRef(p.ask.Link)
		}
		if !isHTTPURL(url) {
			url = ""
		}
		addToGroup("todo:"+p.todo.ID, "singleton", label, url, p.todo.ID)
	}

	groups := make([]decisionGroup, 0, len(byKey))
	for _, g := range byKey {
		slices.Sort(g.TodoIDs)
		g.Count = len(g.TodoIDs)
		groups = append(groups, *g)
	}
	slices.SortFunc(groups, func(a, b decisionGroup) int {
		return cmp.Or(
			cmp.Compare(b.Count, a.Count),
			strings.Compare(a.Key, b.Key),
		)
	})
	return decisionGroupsResponse{
		Groups:       groups,
		TotalPending: len(pending),
	}
}

func normalizeDecisionCauseRef(raw string) string {
	s := strings.TrimSpace(raw)
	if isHTTPURL(s) {
		s = strings.TrimRight(s, "/")
	}
	return s
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func decisionFallbackKey(p pendingDecisionEntry) (string, string) {
	scope := strings.TrimSpace(p.todo.DirectionID)
	if scope == "" {
		scope = strings.TrimSpace(p.todo.Project)
	}
	if scope == "" {
		return "", ""
	}
	reason := ""
	if p.ask != nil {
		reason = strings.TrimSpace(p.ask.Question)
		if reason == "" {
			reason = strings.TrimSpace(p.ask.Verdict)
		}
	}
	if reason == "" && p.todo.Supervision != nil {
		for i := len(p.todo.Supervision.Reviews) - 1; i >= 0; i-- {
			if r := strings.TrimSpace(p.todo.Supervision.Reviews[i].Reason); r != "" {
				reason = r
				break
			}
		}
	}
	norm := strings.ToLower(strings.Join(strings.Fields(reason), " "))
	if norm == "" {
		return "", ""
	}
	return scope + "|" + norm, reason
}
