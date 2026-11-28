package assistant

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Pure, extensible pull-based blocker evaluation engine for todos_v2 (#15731).
//
// This file stays pure: no IO, no clock, no network, and no dependencies outside
// the Go standard library (guarded by TestTodoBlockersEngineStaysPure).
// Blockers are evaluated against a caller-supplied todoBlockerFactSnapshot via
// kind-keyed blockerResolver implementations registered in a blockerRegistry, so
// upstream targets never need to push notifications and new blocker kinds can be
// registered without changing the database schema or reconciliation loop.

const (
	// Built-in blocker kinds (untyped string constants to satisfy archguard app-named-primitive).
	todoBlockerKindTodoDone      = "todo_done"
	todoBlockerKindTodoStage     = "todo_stage"
	todoBlockerKindExternalPR    = "external_pr"
	todoBlockerKindWriteConflict = "write_conflict"
	todoBlockerKindMergeTrain    = "merge_train"
	todoBlockerKindOwnerDecision = "owner_decision"

	// Persisted lifecycle states for a blocker edge.
	todoBlockerStateActive    = "active"
	todoBlockerStateResolved  = "resolved"
	todoBlockerStateCancelled = "cancelled"

	// Default attempt threshold before a merge_train blocker escalates.
	defaultMergeTrainBlockerMaxAttempts = 2
)

var (
	errTodoBlockerInvalidKind = errors.New("todo blockers: kind is required")
	errTodoBlockerNilResolver = errors.New("todo blockers: resolver is required")
	errTodoBlockerUnknownKind = errors.New("todo blockers: unknown blocker kind")
)

type todoBlockerRecord struct {
	ID            string `json:"id"`
	TodoID        string `json:"todo_id"`
	Kind          string `json:"kind"`
	TargetRef     string `json:"target_ref"`
	ConditionJSON string `json:"condition_json,omitempty"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts,omitempty"`
}

type todoBlockerTodoFact struct {
	ID              string   `json:"id"`
	Done            bool     `json:"done,omitempty"`
	Accepted        bool     `json:"accepted,omitempty"`
	Cancelled       bool     `json:"cancelled,omitempty"`
	Stage           string   `json:"stage,omitempty"`
	Contracts       []string `json:"contracts,omitempty"`
	HasActiveAgent  bool     `json:"has_active_agent,omitempty"`
	HasOpenDecision bool     `json:"has_open_decision,omitempty"`
}

type todoBlockerPRFact struct {
	URL    string `json:"url"`
	State  string `json:"state"`
	Merged bool   `json:"merged,omitempty"`
}

type todoBlockerMergeTrainFact struct {
	RepoKey       string   `json:"repo_key"`
	HeadTodoID    string   `json:"head_todo_id,omitempty"`
	HeadState     string   `json:"head_state,omitempty"`
	ActiveTodoIDs []string `json:"active_todo_ids,omitempty"`
}

type todoBlockerFactSnapshot struct {
	Todos       map[string]todoBlockerTodoFact       `json:"todos,omitempty"`
	PRs         map[string]todoBlockerPRFact         `json:"prs,omitempty"`
	MergeTrains map[string]todoBlockerMergeTrainFact `json:"merge_trains,omitempty"`
}

type todoBlockerResolution struct {
	Resolved bool   `json:"resolved"`
	Escalate bool   `json:"escalate,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type blockerResolver interface {
	Resolve(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error)
}

type blockerResolverFunc func(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error)

func (f blockerResolverFunc) Resolve(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	return f(b, facts)
}

type blockerRegistry struct {
	mu        sync.RWMutex
	resolvers map[string]blockerResolver
}

func newBlockerRegistry() *blockerRegistry {
	return &blockerRegistry{
		resolvers: make(map[string]blockerResolver),
	}
}

func defaultBlockerRegistry() *blockerRegistry {
	r := newBlockerRegistry()
	_ = r.register(todoBlockerKindTodoDone, blockerResolverFunc(resolveTodoDoneBlocker))
	_ = r.register(todoBlockerKindTodoStage, blockerResolverFunc(resolveTodoStageBlocker))
	_ = r.register(todoBlockerKindExternalPR, blockerResolverFunc(resolveExternalPRBlocker))
	_ = r.register(todoBlockerKindWriteConflict, blockerResolverFunc(resolveWriteConflictBlocker))
	_ = r.register(todoBlockerKindMergeTrain, blockerResolverFunc(resolveMergeTrainBlocker))
	_ = r.register(todoBlockerKindOwnerDecision, blockerResolverFunc(resolveOwnerDecisionBlocker))
	return r
}

func (r *blockerRegistry) register(kind string, res blockerResolver) error {
	k := strings.TrimSpace(kind)
	if k == "" {
		return errTodoBlockerInvalidKind
	}
	if res == nil {
		return errTodoBlockerNilResolver
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolvers == nil {
		r.resolvers = make(map[string]blockerResolver)
	}
	r.resolvers[k] = res
	return nil
}

func (r *blockerRegistry) lookup(kind string) (blockerResolver, bool) {
	if r == nil {
		return nil, false
	}
	k := strings.TrimSpace(kind)
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.resolvers[k]
	return res, ok
}

type todoBlockerDecision struct {
	BlockerID  string                `json:"blocker_id"`
	TodoID     string                `json:"todo_id"`
	Kind       string                `json:"kind"`
	TargetRef  string                `json:"target_ref"`
	Resolution todoBlockerResolution `json:"resolution"`
	Error      string                `json:"error,omitempty"`
}

type todoBlockerReconcileResult struct {
	ResolvedIDs      []string              `json:"resolved_ids,omitempty"`
	EscalatedIDs     []string              `json:"escalated_ids,omitempty"`
	UnblockedTodoIDs []string              `json:"unblocked_todo_ids,omitempty"`
	Decisions        []todoBlockerDecision `json:"decisions,omitempty"`
}

type todoBlockerSharedGroup struct {
	TargetRef  string   `json:"target_ref"`
	Count      int      `json:"count"`
	TodoIDs    []string `json:"todo_ids"`
	BlockerIDs []string `json:"blocker_ids"`
	Kinds      []string `json:"kinds"`
}

// reconcileTodoBlockers evaluates all active blockers against facts using reg (defaulting to defaultBlockerRegistry).
//
// A to-do appears in UnblockedTodoIDs if and only if it had at least one active blocker in active
// and every active blocker attached to that to-do resolved in this pass.
// All output slices are deterministically ordered regardless of input order.
func reconcileTodoBlockers(active []todoBlockerRecord, facts todoBlockerFactSnapshot, reg *blockerRegistry) todoBlockerReconcileResult {
	if reg == nil {
		reg = defaultBlockerRegistry()
	}

	candidates := make([]todoBlockerRecord, 0, len(active))
	for _, b := range active {
		if b.State != "" && b.State != todoBlockerStateActive {
			continue
		}
		candidates = append(candidates, b)
	}
	slices.SortFunc(candidates, func(a, b todoBlockerRecord) int {
		if c := cmp.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.TodoID, b.TodoID); c != 0 {
			return c
		}
		return cmp.Compare(a.TargetRef, b.TargetRef)
	})

	var res todoBlockerReconcileResult
	if len(candidates) == 0 {
		return res
	}

	totalByTodo := make(map[string]int)
	resolvedByTodo := make(map[string]int)
	seenResolved := make(map[string]struct{})
	seenEscalated := make(map[string]struct{})

	for _, b := range candidates {
		todoID := strings.TrimSpace(b.TodoID)
		if todoID != "" {
			totalByTodo[todoID]++
		}

		resolver, ok := reg.lookup(b.Kind)
		if !ok {
			res.Decisions = append(res.Decisions, todoBlockerDecision{
				BlockerID: b.ID,
				TodoID:    todoID,
				Kind:      b.Kind,
				TargetRef: b.TargetRef,
				Resolution: todoBlockerResolution{
					Resolved: false,
					Reason:   "unknown_kind",
				},
				Error: fmt.Sprintf("%v: %s", errTodoBlockerUnknownKind, b.Kind),
			})
			continue
		}

		outcome, err := resolver.Resolve(b, facts)
		if err != nil {
			res.Decisions = append(res.Decisions, todoBlockerDecision{
				BlockerID: b.ID,
				TodoID:    todoID,
				Kind:      b.Kind,
				TargetRef: b.TargetRef,
				Resolution: todoBlockerResolution{
					Resolved: false,
					Reason:   "resolver_error",
				},
				Error: err.Error(),
			})
			continue
		}

		res.Decisions = append(res.Decisions, todoBlockerDecision{
			BlockerID:  b.ID,
			TodoID:     todoID,
			Kind:       b.Kind,
			TargetRef:  b.TargetRef,
			Resolution: outcome,
		})

		if outcome.Resolved {
			if todoID != "" {
				resolvedByTodo[todoID]++
			}
			if _, dup := seenResolved[b.ID]; !dup {
				seenResolved[b.ID] = struct{}{}
				res.ResolvedIDs = append(res.ResolvedIDs, b.ID)
			}
		} else if outcome.Escalate {
			if _, dup := seenEscalated[b.ID]; !dup {
				seenEscalated[b.ID] = struct{}{}
				res.EscalatedIDs = append(res.EscalatedIDs, b.ID)
			}
		}
	}

	for todoID, total := range totalByTodo {
		if total > 0 && resolvedByTodo[todoID] == total {
			res.UnblockedTodoIDs = append(res.UnblockedTodoIDs, todoID)
		}
	}

	slices.Sort(res.ResolvedIDs)
	slices.Sort(res.EscalatedIDs)
	slices.Sort(res.UnblockedTodoIDs)
	return res
}

// sharedActiveTodoBlockers aggregates active blockers sharing the same target_ref across
// at least 2 distinct to-dos, sorted deterministically by Count descending then TargetRef ascending.
func sharedActiveTodoBlockers(active []todoBlockerRecord) []todoBlockerSharedGroup {
	type acc struct {
		todos    map[string]struct{}
		blockers map[string]struct{}
		kinds    map[string]struct{}
	}
	byTarget := make(map[string]*acc)
	for _, b := range active {
		if b.State != "" && b.State != todoBlockerStateActive {
			continue
		}
		target := strings.TrimSpace(b.TargetRef)
		todoID := strings.TrimSpace(b.TodoID)
		if target == "" || todoID == "" {
			continue
		}
		g := byTarget[target]
		if g == nil {
			g = &acc{
				todos:    make(map[string]struct{}),
				blockers: make(map[string]struct{}),
				kinds:    make(map[string]struct{}),
			}
			byTarget[target] = g
		}
		g.todos[todoID] = struct{}{}
		if b.ID != "" {
			g.blockers[b.ID] = struct{}{}
		}
		if b.Kind != "" {
			g.kinds[b.Kind] = struct{}{}
		}
	}

	var out []todoBlockerSharedGroup
	for target, g := range byTarget {
		if len(g.todos) < 2 {
			continue
		}
		todoIDs := make([]string, 0, len(g.todos))
		for id := range g.todos {
			todoIDs = append(todoIDs, id)
		}
		slices.Sort(todoIDs)

		blockerIDs := make([]string, 0, len(g.blockers))
		for id := range g.blockers {
			blockerIDs = append(blockerIDs, id)
		}
		slices.Sort(blockerIDs)

		kinds := make([]string, 0, len(g.kinds))
		for k := range g.kinds {
			kinds = append(kinds, k)
		}
		slices.Sort(kinds)

		out = append(out, todoBlockerSharedGroup{
			TargetRef:  target,
			Count:      len(todoIDs),
			TodoIDs:    todoIDs,
			BlockerIDs: blockerIDs,
			Kinds:      kinds,
		})
	}

	slices.SortFunc(out, func(a, b todoBlockerSharedGroup) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.TargetRef, b.TargetRef)
	})
	return out
}

func decodeTodoBlockerCondition(raw string, dst any) error {
	s := strings.TrimSpace(raw)
	if s == "" || s == "{}" || s == "null" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}

func resolveTodoDoneBlocker(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	var cond struct {
		RequireAccepted bool `json:"require_accepted"`
	}
	if err := decodeTodoBlockerCondition(b.ConditionJSON, &cond); err != nil {
		return todoBlockerResolution{}, fmt.Errorf("todo_done condition_json: %w", err)
	}
	target := strings.TrimSpace(b.TargetRef)
	todo, ok := facts.Todos[target]
	if !ok {
		return todoBlockerResolution{Resolved: false, Reason: "todo_not_found"}, nil
	}
	if todo.Cancelled {
		return todoBlockerResolution{Resolved: false, Escalate: true, Reason: "prerequisite_cancelled"}, nil
	}
	if cond.RequireAccepted {
		if todo.Accepted {
			return todoBlockerResolution{Resolved: true, Reason: "todo_accepted"}, nil
		}
		return todoBlockerResolution{Resolved: false, Reason: "todo_not_accepted"}, nil
	}
	if todo.Done {
		return todoBlockerResolution{Resolved: true, Reason: "todo_done"}, nil
	}
	return todoBlockerResolution{Resolved: false, Reason: "todo_not_done"}, nil
}

var todoBlockerStageRank = map[string]int{
	"plan":           0,
	"contract_ready": 1,
	"develop":        2,
	"pr_open":        3,
	"merged":         4,
	"done":           5,
}

func normalizeTodoBlockerStage(raw string) (string, int, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	rank, ok := todoBlockerStageRank[s]
	return s, rank, ok
}

func resolveTodoStageBlocker(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	var cond struct {
		RequiredStage     string   `json:"required_stage"`
		RequiredContracts []string `json:"required_contracts"`
	}
	if err := decodeTodoBlockerCondition(b.ConditionJSON, &cond); err != nil {
		return todoBlockerResolution{}, fmt.Errorf("todo_stage condition_json: %w", err)
	}

	reqStageRaw := strings.TrimSpace(cond.RequiredStage)
	var reqContracts []string
	for _, c := range cond.RequiredContracts {
		if norm := strings.ToLower(strings.TrimSpace(c)); norm != "" {
			reqContracts = append(reqContracts, norm)
		}
	}
	if reqStageRaw == "" && len(reqContracts) == 0 {
		return todoBlockerResolution{Resolved: false, Reason: "empty_stage_condition"}, nil
	}

	target := strings.TrimSpace(b.TargetRef)
	todo, ok := facts.Todos[target]
	if !ok {
		return todoBlockerResolution{Resolved: false, Reason: "todo_not_found"}, nil
	}
	if todo.Cancelled {
		return todoBlockerResolution{Resolved: false, Escalate: true, Reason: "prerequisite_cancelled"}, nil
	}

	if reqStageRaw != "" {
		_, reqRank, ok := normalizeTodoBlockerStage(reqStageRaw)
		if !ok {
			return todoBlockerResolution{Resolved: false, Reason: "unknown_required_stage"}, nil
		}
		curStage := todo.Stage
		if todo.Done && curStage == "" {
			curStage = "done"
		}
		_, curRank, curOK := normalizeTodoBlockerStage(curStage)
		if todo.Done && (!curOK || curRank < todoBlockerStageRank["done"]) {
			curRank = todoBlockerStageRank["done"]
			curOK = true
		}
		if !curOK || curRank < reqRank {
			return todoBlockerResolution{Resolved: false, Reason: "stage_not_reached"}, nil
		}
	}

	if len(reqContracts) > 0 {
		have := make(map[string]struct{}, len(todo.Contracts))
		for _, c := range todo.Contracts {
			if norm := strings.ToLower(strings.TrimSpace(c)); norm != "" {
				have[norm] = struct{}{}
			}
		}
		for _, req := range reqContracts {
			if _, present := have[req]; !present {
				return todoBlockerResolution{Resolved: false, Reason: "missing_contracts"}, nil
			}
		}
	}

	return todoBlockerResolution{Resolved: true, Reason: "stage_satisfied"}, nil
}

func normalizeTodoBlockerPRRef(raw string) string {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" {
		return ""
	}
	scheme, rest, found := strings.Cut(s, "://")
	if !found {
		return s
	}
	lowerScheme := strings.ToLower(scheme)
	if lowerScheme != "http" && lowerScheme != "https" {
		return s
	}
	end := strings.IndexAny(rest, "/?#")
	authority := rest
	suffix := ""
	if end >= 0 {
		authority = rest[:end]
		suffix = rest[end:]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[:at+1] + strings.ToLower(authority[at+1:])
	} else {
		authority = strings.ToLower(authority)
	}
	return lowerScheme + "://" + authority + suffix
}

func lookupTodoBlockerPRFact(prs map[string]todoBlockerPRFact, targetRef string) (todoBlockerPRFact, bool) {
	if len(prs) == 0 {
		return todoBlockerPRFact{}, false
	}
	if pr, ok := prs[targetRef]; ok {
		return pr, true
	}
	norm := normalizeTodoBlockerPRRef(targetRef)
	if pr, ok := prs[norm]; ok {
		return pr, true
	}
	for k, pr := range prs {
		if strings.EqualFold(normalizeTodoBlockerPRRef(k), norm) || (pr.URL != "" && strings.EqualFold(normalizeTodoBlockerPRRef(pr.URL), norm)) {
			return pr, true
		}
	}
	return todoBlockerPRFact{}, false
}

func resolveExternalPRBlocker(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	var cond struct {
		RequireMerged bool `json:"require_merged"`
	}
	if err := decodeTodoBlockerCondition(b.ConditionJSON, &cond); err != nil {
		return todoBlockerResolution{}, fmt.Errorf("external_pr condition_json: %w", err)
	}

	pr, ok := lookupTodoBlockerPRFact(facts.PRs, b.TargetRef)
	if !ok {
		return todoBlockerResolution{Resolved: false, Reason: "pr_not_found"}, nil
	}
	state := strings.ToLower(strings.TrimSpace(pr.State))
	if pr.Merged || state == "merged" {
		return todoBlockerResolution{Resolved: true, Reason: "pr_merged"}, nil
	}
	if state == "closed" {
		if cond.RequireMerged {
			return todoBlockerResolution{Resolved: false, Escalate: true, Reason: "pr_closed_without_merge"}, nil
		}
		return todoBlockerResolution{Resolved: true, Reason: "pr_closed"}, nil
	}
	return todoBlockerResolution{Resolved: false, Reason: "pr_open"}, nil
}

func resolveWriteConflictBlocker(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	target := strings.TrimSpace(b.TargetRef)
	todo, ok := facts.Todos[target]
	if !ok {
		return todoBlockerResolution{Resolved: false, Reason: "todo_not_found"}, nil
	}
	if todo.Done || todo.Cancelled {
		return todoBlockerResolution{Resolved: true, Reason: "write_scope_released"}, nil
	}
	return todoBlockerResolution{Resolved: false, Reason: "write_scope_held"}, nil
}

func resolveMergeTrainBlocker(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	var cond struct {
		MaxAttempts int    `json:"max_attempts"`
		RepoKey     string `json:"repo_key"`
		PRURL       string `json:"pr_url"`
	}
	if err := decodeTodoBlockerCondition(b.ConditionJSON, &cond); err != nil {
		return todoBlockerResolution{}, fmt.Errorf("merge_train condition_json: %w", err)
	}
	target := strings.TrimSpace(b.TargetRef)
	prURL := strings.TrimSpace(cond.PRURL)
	if prURL == "" && strings.Contains(target, "://") &&
		(strings.Contains(target, "/pull/") || strings.Contains(target, "/merge_requests/")) {
		prURL = target
	}
	if prURL != "" {
		if pr, ok := lookupTodoBlockerPRFact(facts.PRs, prURL); ok &&
			(pr.Merged || strings.EqualFold(pr.State, "merged") || strings.EqualFold(pr.State, "closed")) {
			return todoBlockerResolution{Resolved: true, Reason: "merge_train_pr_cleared"}, nil
		}
		// PR-specific rows written by earlier versions counted observations. Only this
		// PR's status can clear the wait, regardless of the repository's current head.
		return todoBlockerResolution{Resolved: false, Reason: "merge_train_waiting"}, nil
	}
	maxAttempts := cond.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMergeTrainBlockerMaxAttempts
	}
	if b.Attempts >= maxAttempts {
		return todoBlockerResolution{Resolved: false, Escalate: true, Reason: "merge_train_max_attempts"}, nil
	}

	repoKey := strings.TrimSpace(cond.RepoKey)
	if repoKey == "" {
		repoKey = target
	}

	if train, ok := facts.MergeTrains[repoKey]; ok {
		headState := strings.ToLower(strings.TrimSpace(train.HeadState))
		if headState == "merged" || headState == "cancelled" {
			return todoBlockerResolution{Resolved: true, Reason: "merge_train_head_cleared"}, nil
		}
		if target != "" && target != repoKey && train.HeadTodoID != target && !slices.Contains(train.ActiveTodoIDs, target) {
			return todoBlockerResolution{Resolved: true, Reason: "merge_train_head_cleared"}, nil
		}
		if train.HeadTodoID == "" && len(train.ActiveTodoIDs) == 0 {
			return todoBlockerResolution{Resolved: true, Reason: "merge_train_empty"}, nil
		}
	}

	if pr, ok := lookupTodoBlockerPRFact(facts.PRs, target); ok {
		state := strings.ToLower(strings.TrimSpace(pr.State))
		if pr.Merged || state == "merged" || state == "closed" {
			return todoBlockerResolution{Resolved: true, Reason: "merge_train_pr_cleared"}, nil
		}
	}

	if todo, ok := facts.Todos[target]; ok && (todo.Done || todo.Cancelled) {
		return todoBlockerResolution{Resolved: true, Reason: "merge_train_todo_cleared"}, nil
	}

	return todoBlockerResolution{Resolved: false, Reason: "merge_train_waiting"}, nil
}

func resolveOwnerDecisionBlocker(b todoBlockerRecord, facts todoBlockerFactSnapshot) (todoBlockerResolution, error) {
	target := strings.TrimSpace(b.TargetRef)
	if target == "" {
		target = strings.TrimSpace(b.TodoID)
	}
	todo, ok := facts.Todos[target]
	if !ok {
		return todoBlockerResolution{Resolved: false, Reason: "todo_not_found"}, nil
	}
	if !todo.HasOpenDecision {
		return todoBlockerResolution{Resolved: true, Reason: "owner_decision_cleared"}, nil
	}
	return todoBlockerResolution{Resolved: false, Reason: "owner_decision_pending"}, nil
}
