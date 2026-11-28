package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/nwverse/persona/server/internal/assistant/todo/stagegate"
	"github.com/nwverse/persona/server/internal/db"
)

const (
	maxBlockedByPRURLs            = 20
	externalPRBlockerFetchTimeout = 5 * time.Second
)

// todoBlockerStore is the narrow persistence seam for persistent todos_v2 blockers (#15731).
// Production *db.DB satisfies it directly without widening the core Store interface.
type todoBlockerStore interface {
	UpsertTodoBlocker(ctx context.Context, r db.TodoBlockerRow) (*db.TodoBlockerRow, error)
	EnsureTodoBlocker(ctx context.Context, r db.TodoBlockerRow) (*db.TodoBlockerRow, error)
	ListActiveTodoBlockers(ctx context.Context) ([]db.TodoBlockerRow, error)
	ListTodoReadinessBlockers(ctx context.Context, todoIDs []string) ([]db.TodoBlockerRow, error)
	ListMergeTrainPRBlockers(ctx context.Context, prURL, repoKey string) ([]db.TodoBlockerRow, error)
	UpdateActiveTodoBlockerCondition(ctx context.Context, id, condition string, at time.Time) (*db.TodoBlockerRow, error)
	ListTodoBlockersByTodo(ctx context.Context, todoID string) ([]db.TodoBlockerRow, error)
	ListActiveTodoBlockersByTarget(ctx context.Context, targetRef string) ([]db.TodoBlockerRow, error)
	ResolveTodoBlocker(ctx context.Context, id string, at time.Time) (*db.TodoBlockerRow, error)
	CancelTodoBlocker(ctx context.Context, id string, at time.Time) (*db.TodoBlockerRow, error)
	IncrementTodoBlockerAttempts(ctx context.Context, id string, at time.Time) (*db.TodoBlockerRow, error)
}

var _ todoBlockerStore = (*db.DB)(nil)

func (s *Service) todoBlockerDB() todoBlockerStore {
	if s == nil {
		return nil
	}
	st, _ := s.store().(todoBlockerStore)
	return st
}

// todoBlockerReconcileSummary reports the persisted and notification outcomes of one
// pull-based blocker reconciliation pass under todos_v2.
type todoBlockerReconcileSummary struct {
	Evaluated        int
	ResolvedIDs      []string
	EscalatedIDs     []string
	UnblockedTodoIDs []string
	SharedGroups     []todoBlockerSharedGroup
	cancelledMirrors int
}

type todoStageUnlockSpec struct {
	stage     string
	contracts []string
}

type todoStageBlockerCondition struct {
	RequiredStage     string   `json:"required_stage,omitempty"`
	RequiredContracts []string `json:"required_contracts,omitempty"`
}

type todoBlockerHooks struct {
	mu          sync.RWMutex
	registry    *blockerRegistry
	onUnblocked func(ctx context.Context, todoID string)
	fetchPR     func(ctx context.Context, targetRef string) (todoBlockerPRFact, bool)
}

// todoBlockerHookRegistry isolates optional per-Service blocker hooks without modifying Service fields
// and cleans up entries automatically when a Service is garbage-collected.
var todoBlockerHookRegistry = struct {
	mu    sync.Mutex
	bySvc map[weak.Pointer[Service]]*todoBlockerHooks
}{
	bySvc: make(map[weak.Pointer[Service]]*todoBlockerHooks),
}

func (s *Service) todoBlockerHooks(create bool) *todoBlockerHooks {
	if s == nil {
		return nil
	}
	wp := weak.Make(s)
	todoBlockerHookRegistry.mu.Lock()
	defer todoBlockerHookRegistry.mu.Unlock()
	if h, ok := todoBlockerHookRegistry.bySvc[wp]; ok {
		return h
	}
	if !create {
		return nil
	}
	h := &todoBlockerHooks{}
	todoBlockerHookRegistry.bySvc[wp] = h
	runtime.AddCleanup(s, func(key weak.Pointer[Service]) {
		todoBlockerHookRegistry.mu.Lock()
		delete(todoBlockerHookRegistry.bySvc, key)
		todoBlockerHookRegistry.mu.Unlock()
	}, wp)
	return h
}

// setTodoBlockerRegistry installs a custom blockerRegistry on s (tests / extensions).
func (s *Service) setTodoBlockerRegistry(reg *blockerRegistry) {
	if h := s.todoBlockerHooks(true); h != nil {
		h.mu.Lock()
		h.registry = reg
		h.mu.Unlock()
	}
}

// setTodoBlockerUnblockedHook installs a callback fired whenever a to-do's final active blocker resolves.
func (s *Service) setTodoBlockerUnblockedHook(fn func(ctx context.Context, todoID string)) {
	if h := s.todoBlockerHooks(true); h != nil {
		h.mu.Lock()
		h.onUnblocked = fn
		h.mu.Unlock()
	}
}

// setTodoBlockerPRFetcher installs a custom PR fact fetcher for external_pr blockers.
func (s *Service) setTodoBlockerPRFetcher(fn func(ctx context.Context, targetRef string) (todoBlockerPRFact, bool)) {
	if h := s.todoBlockerHooks(true); h != nil {
		h.mu.Lock()
		h.fetchPR = fn
		h.mu.Unlock()
	}
}

// addTodoBlocker persists a blocker edge when todos_v2 is enabled.
// When todos_v2 is disabled, it bypasses immediately with zero side effects.
func (s *Service) addTodoBlocker(ctx context.Context, row db.TodoBlockerRow) (*db.TodoBlockerRow, error) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil, nil
	}
	st := s.todoBlockerDB()
	if st == nil {
		return nil, nil
	}
	s.blockerReconcile.mu.Lock()
	defer s.blockerReconcile.mu.Unlock()
	return st.UpsertTodoBlocker(ctx, row)
}

func todoBlockerID(todoID, kind, targetRef string) string {
	hashRef := targetRef
	if kind == todoBlockerKindExternalPR {
		hashRef = strings.ToLower(normalizeTodoBlockerPRRef(targetRef))
	}
	sum := sha256.Sum256([]byte(todoID + "\x00" + kind + "\x00" + hashRef))
	return "blk_" + hex.EncodeToString(sum[:8])
}

// AddTodoBlocker persists a typed blocker edge for todoID when todos_v2 is enabled.
// Uses a deterministic edge ID per (todoID, kind, normalized targetRef) so repeated calls are idempotent.
// Strictly bypassed (no-op) when todos_v2 is off.
func (s *Service) AddTodoBlocker(ctx context.Context, todoID, kind, targetRef, conditionJSON string) (*db.TodoBlockerRow, error) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil, nil
	}
	todoID = strings.TrimSpace(todoID)
	kind = strings.TrimSpace(kind)
	targetRef = normalizeTodoBlockerPRRef(targetRef)
	if kind == todoBlockerKindExternalPR {
		targetRef = normalizeTodoBlockerPRRef(targetRef)
	}
	if todoID == "" || kind == "" || targetRef == "" {
		return nil, errors.New("todo_blockers: todo_id, kind, and target_ref are required")
	}
	return s.addTodoBlocker(ctx, db.TodoBlockerRow{
		ID:            todoBlockerID(todoID, kind, targetRef),
		TodoID:        todoID,
		Kind:          kind,
		TargetRef:     targetRef,
		ConditionJSON: strings.TrimSpace(conditionJSON),
		State:         db.TodoBlockerStateActive,
	})
}

// parseBlockedByPRURLs validates and deduplicates external PR URLs for blocked_by_pr inputs (#16099).
func parseBlockedByPRURLs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxBlockedByPRURLs {
		return nil, fmt.Errorf("blocked_by_pr accepts at most %d URLs", maxBlockedByPRURLs)
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		norm := normalizeTodoBlockerPRRef(item)
		if norm == "" {
			return nil, errors.New("blocked_by_pr contains an empty URL")
		}
		u, err := url.Parse(norm)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || strings.TrimSpace(u.Host) == "" {
			return nil, fmt.Errorf("blocked_by_pr URL %q must be a valid https:// URL", item)
		}
		key := strings.ToLower(norm)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, norm)
	}
	return out, nil
}

// persistExternalPRBlockers writes external_pr blocker rows for todoID strictly when todos_v2 is enabled.
func (s *Service) persistExternalPRBlockers(ctx context.Context, todoID string, urls []string) error {
	if s == nil || !s.settings(ctx).todosV2On() || len(urls) == 0 {
		return nil
	}
	for _, u := range urls {
		if _, err := s.AddTodoBlocker(ctx, todoID, todoBlockerKindExternalPR, u, ""); err != nil {
			return err
		}
	}
	return nil
}

// parseUnlockStageRule validates optional structured unlock_stage and unlock_contracts inputs.
func parseUnlockStageRule(rawStage string, rawContracts []string) (string, []string, error) {
	normStage := ""
	if trimmed := strings.TrimSpace(rawStage); trimmed != "" {
		st, ok := stagegate.NormalizeStage(trimmed)
		if !ok {
			return "", nil, fmt.Errorf("unlock_stage %q is invalid (valid: plan, contract_ready, develop, pr_open, merged, done)", rawStage)
		}
		normStage = string(st)
	}
	normContracts := stagegate.NormalizeContracts(rawContracts)
	return normStage, normContracts, nil
}

func encodeTodoStageBlockerCondition(stage string, contracts []string) string {
	cond := todoStageBlockerCondition{
		RequiredStage:     strings.TrimSpace(stage),
		RequiredContracts: stagegate.NormalizeContracts(contracts),
	}
	if cond.RequiredStage == "" && len(cond.RequiredContracts) == 0 {
		return "{}"
	}
	b, err := json.Marshal(cond)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeTodoStageBlockerCondition(raw string) (todoStageUnlockRule, bool) {
	var cond todoStageBlockerCondition
	if err := decodeTodoBlockerCondition(raw, &cond); err != nil {
		return todoStageUnlockRule{}, false
	}
	var rule todoStageUnlockRule
	if st, ok := stagegate.NormalizeStage(cond.RequiredStage); ok {
		rule.Stage = st
	}
	rule.Contracts = stagegate.NormalizeContracts(cond.RequiredContracts)
	if rule.Stage == "" && len(rule.Contracts) == 0 {
		return todoStageUnlockRule{}, false
	}
	return rule, true
}

func normalizeBlockerConditionJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return "{}"
	}
	return s
}

// syncAfterBlockers mirrors Todo.After edges into persistent todo_blockers (todo_done or todo_stage) strictly
// when todos_v2 is enabled. It upserts active edges for current afterIDs, skips already-resolved identical edges,
// and cancels stale active mirrored todo_done/todo_stage edges whose target_ref was removed without touching
// external_pr, merge_train, or other blocker kinds.
func (s *Service) syncAfterBlockers(ctx context.Context, todoID string, afterIDs []string, unlockStage string, unlockContracts []string) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return
	}
	todoID = strings.TrimSpace(todoID)
	if todoID == "" {
		return
	}

	todo, _ := s.todoNow(todoID)
	todo.ID = todoID
	if err := writeAfterBlockers(ctx, s.todoBlockerDB(), todo, afterIDs, unlockStage, unlockContracts, time.Now().UTC()); err != nil {
		s.logf("todo %s: sync after blockers: %v", todoID, err)
	}
}

type todoBlockerWriter interface {
	ListTodoBlockersByTodo(context.Context, string) ([]db.TodoBlockerRow, error)
	UpsertTodoBlocker(context.Context, db.TodoBlockerRow) (*db.TodoBlockerRow, error)
	CancelTodoBlocker(context.Context, string, time.Time) (*db.TodoBlockerRow, error)
}

func writeAfterBlockers(ctx context.Context, st todoBlockerWriter, todo Todo, afterIDs []string, unlockStage string, unlockContracts []string, now time.Time) error {
	if st == nil {
		return nil
	}
	todoID := todo.ID
	existing, err := st.ListTodoBlockersByTodo(ctx, todoID)
	if err != nil {
		return err
	}
	existingByID := make(map[string]db.TodoBlockerRow, len(existing))
	for _, r := range existing {
		existingByID[r.ID] = r
	}

	normStage := ""
	if stVal, ok := stagegate.NormalizeStage(unlockStage); ok {
		normStage = string(stVal)
	}
	normContracts := stagegate.NormalizeContracts(unlockContracts)
	if normStage == "" && len(normContracts) == 0 {
		if rule, hasRule := parseTodoStageUnlockRule(todo); hasRule {
			normStage = string(rule.Stage)
			normContracts = rule.Contracts
		}
	}
	if normStage == "" && len(normContracts) == 0 {
		for _, r := range existing {
			if r.Kind == todoBlockerKindTodoStage && r.State == db.TodoBlockerStateActive {
				if rule, hasRule := decodeTodoStageBlockerCondition(r.ConditionJSON); hasRule {
					normStage = string(rule.Stage)
					normContracts = rule.Contracts
					break
				}
			}
		}
	}

	kind := todoBlockerKindTodoDone
	condJSON := "{}"
	if normStage != "" || len(normContracts) > 0 {
		kind = todoBlockerKindTodoStage
		condJSON = encodeTodoStageBlockerCondition(normStage, normContracts)
	}

	deps := cleanIDs(afterIDs)
	wantIDs := make(map[string]struct{}, len(deps))
	for _, depID := range deps {
		if depID == todoID {
			continue
		}
		id := todoBlockerID(todoID, kind, depID)
		wantIDs[id] = struct{}{}
		if cur, ok := existingByID[id]; ok && cur.State == db.TodoBlockerStateResolved &&
			normalizeBlockerConditionJSON(cur.ConditionJSON) == condJSON {
			continue
		}
		if _, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
			ID:            id,
			TodoID:        todoID,
			Kind:          kind,
			TargetRef:     depID,
			ConditionJSON: condJSON,
			State:         db.TodoBlockerStateActive,
		}); err != nil {
			return fmt.Errorf("mirror after blocker %s (%s): %w", depID, kind, err)
		}
	}

	for _, r := range existing {
		if r.State != db.TodoBlockerStateActive {
			continue
		}
		if r.Kind != todoBlockerKindTodoDone && r.Kind != todoBlockerKindTodoStage {
			continue
		}
		if _, keep := wantIDs[r.ID]; keep {
			continue
		}
		if _, err := st.CancelTodoBlocker(ctx, r.ID, now); err != nil {
			return fmt.Errorf("cancel stale mirrored blocker %s: %w", r.ID, err)
		}
	}
	return nil
}

// ReconcileTodoBlockers performs a pull-based reconciliation of persisted active blockers under todos_v2 (#16099).
// Pass targetRef="" for a full fallback sweep, or a specific PR URL / todo ID for targeted fast-path reconciliation.
func (s *Service) ReconcileTodoBlockers(ctx context.Context, targetRef string) (todoBlockerReconcileSummary, error) {
	return s.reconcilePersistedTodoBlockers(ctx, targetRef)
}

// reconcileTodoBlockersForPR runs targeted blocker reconciliation using an already-observed PR status
// from pr_status.go, avoiding a redundant forge round-trip on the event-driven fast path (#16099).
func (s *Service) reconcileTodoBlockersForPR(ctx context.Context, status TodoPRStatus) (todoBlockerReconcileSummary, error) {
	norm := normalizeTodoBlockerPRRef(status.URL)
	if norm == "" {
		return todoBlockerReconcileSummary{}, nil
	}
	fact := todoBlockerPRFact{
		URL:    norm,
		State:  status.State,
		Merged: strings.EqualFold(status.State, "merged"),
	}
	now := time.Now()
	s.blockerReconcile.prs.record(norm, fact, true, now, now)
	return s.reconcilePersistedTodoBlockersWithPRFact(ctx, norm, &fact)
}

// incrementTodoBlockerAttempts increments the persistent attempt counter for blocker id under todos_v2.
func (s *Service) incrementTodoBlockerAttempts(ctx context.Context, id string) (*db.TodoBlockerRow, error) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil, nil
	}
	st := s.todoBlockerDB()
	if st == nil {
		return nil, nil
	}
	return st.IncrementTodoBlockerAttempts(ctx, id, time.Now().UTC())
}

// cancelTodoBlocker cancels blocker id under todos_v2.
func (s *Service) cancelTodoBlocker(ctx context.Context, id string) (*db.TodoBlockerRow, error) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil, nil
	}
	st := s.todoBlockerDB()
	if st == nil {
		return nil, nil
	}
	s.blockerReconcile.mu.Lock()
	defer s.blockerReconcile.mu.Unlock()
	row, err := st.CancelTodoBlocker(ctx, id, time.Now().UTC())
	if row != nil && row.Kind == todoBlockerKindMergeTrain {
		s.clearMergeTrainBlocker(row.TodoID, row.TargetRef, row.ConditionJSON)
	}
	return row, err
}

// sharedActiveTodoBlockerGroups returns active blockers aggregated by shared target_ref (count >= 2)
// when todos_v2 is enabled, or nil when todos_v2 is disabled.
func (s *Service) sharedActiveTodoBlockerGroups(ctx context.Context) ([]todoBlockerSharedGroup, error) {
	if s == nil || !s.settings(ctx).todosV2On() {
		return nil, nil
	}
	st := s.todoBlockerDB()
	if st == nil {
		return nil, nil
	}
	rows, err := st.ListActiveTodoBlockers(ctx)
	if err != nil {
		return nil, err
	}
	return sharedActiveTodoBlockers(dbRowsToTodoBlockers(rows)), nil
}

// reconcilePersistedTodoBlockers uses local and cached facts. With Labs off, it still
// services previously stored external PR waits.
func (s *Service) reconcilePersistedTodoBlockers(ctx context.Context, targetRef string) (todoBlockerReconcileSummary, error) {
	return s.reconcilePersistedTodoBlockersWithPRFact(ctx, targetRef, nil)
}

func loadActiveBlockerRows(ctx context.Context, st todoBlockerStore, targetRef string) ([]db.TodoBlockerRow, error) {
	if targetRef == "" {
		return st.ListActiveTodoBlockers(ctx)
	}
	targeted, err := st.ListActiveTodoBlockersByTarget(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(targeted))
	var rows []db.TodoBlockerRow
	for _, row := range targeted {
		if seen[row.TodoID] {
			continue
		}
		seen[row.TodoID] = true
		attached, err := st.ListTodoBlockersByTodo(ctx, row.TodoID)
		if err != nil {
			return nil, err
		}
		for _, b := range attached {
			if b.State == db.TodoBlockerStateActive {
				rows = append(rows, b)
			}
		}
	}
	return rows, nil
}

func (s *Service) reconcilePersistedTodoBlockersWithPRFact(ctx context.Context, targetRef string, observedPR *todoBlockerPRFact) (todoBlockerReconcileSummary, error) {
	st := s.todoBlockerDB()
	if st == nil {
		return todoBlockerReconcileSummary{}, nil
	}

	s.blockerReconcile.mu.Lock()
	summary, wakes, err := s.reconcileBlockerRows(ctx, st, normalizeTodoBlockerPRRef(targetRef), observedPR)
	s.blockerReconcile.mu.Unlock()
	if len(summary.ResolvedIDs) > 0 || summary.cancelledMirrors > 0 {
		s.todosChanged(ctx)
		for _, wake := range wakes {
			s.emitTodoBlockerWake(ctx, wake)
		}
		s.sweepAdmissionQueue(ctx)
	}
	return summary, err
}

type todoBlockerWake struct {
	last     db.TodoBlockerRow
	resolved []todoBlockerRecord
}

func (s *Service) reconcileBlockerRows(ctx context.Context, st todoBlockerStore, targetRef string, observedPR *todoBlockerPRFact) (todoBlockerReconcileSummary, []todoBlockerWake, error) {
	rows, err := loadActiveBlockerRows(ctx, st, targetRef)
	if err != nil {
		return todoBlockerReconcileSummary{}, nil, err
	}
	v2 := s.settings(ctx).todosV2On()
	if !v2 {
		rows = s.approvedExternalPRWaits(ctx, rows)
	}
	if len(rows) == 0 {
		return todoBlockerReconcileSummary{}, nil, nil
	}

	active := dbRowsToTodoBlockers(rows)
	facts, err := s.buildBlockerFactSnapshot(ctx, active, observedPR)
	if err != nil {
		return todoBlockerReconcileSummary{}, nil, err
	}
	var reg *blockerRegistry
	if h := s.todoBlockerHooks(false); h != nil {
		h.mu.RLock()
		reg = h.registry
		h.mu.RUnlock()
	}
	result := reconcileTodoBlockers(active, facts, reg)
	mirrors, mirrorErr := s.todoAfterMirrorVerdicts(ctx, rows)
	now := time.Now().UTC()
	summary := todoBlockerReconcileSummary{Evaluated: len(active)}
	lastByTodo := make(map[string]todoBlockerWake)
	remaining := make([]todoBlockerRecord, 0, len(active))
	rowsByID := make(map[string]todoBlockerRecord, len(active))
	for _, b := range active {
		rowsByID[b.ID] = b
	}
	var writeErr error
	for _, decision := range result.Decisions {
		b := rowsByID[decision.BlockerID]
		verdict, mirror := mirrors[b.ID]
		if !mirror {
			verdict.ready = decision.Resolution.Resolved
		}
		if !verdict.ready && !verdict.removed {
			if !mirror && decision.Resolution.Escalate {
				summary.EscalatedIDs = append(summary.EscalatedIDs, b.ID)
			}
			remaining = append(remaining, b)
			continue
		}
		var row *db.TodoBlockerRow
		var err error
		if verdict.removed {
			row, err = st.CancelTodoBlocker(ctx, b.ID, now)
		} else {
			row, err = st.ResolveTodoBlocker(ctx, b.ID, now)
		}
		if err != nil {
			writeErr = errors.Join(writeErr, err)
			remaining = append(remaining, b)
			continue
		}
		if row == nil {
			continue // a competing transition already owns this row
		}
		if verdict.removed {
			summary.cancelledMirrors++
		} else {
			summary.ResolvedIDs = append(summary.ResolvedIDs, row.ID)
		}
		wake := lastByTodo[row.TodoID]
		wake.resolved = append(wake.resolved, b)
		if laterBlockerResolution(*row, wake.last) {
			wake.last = *row
		}
		lastByTodo[row.TodoID] = wake
	}
	summary.SharedGroups = sharedActiveTodoBlockers(remaining)

	var wakes []todoBlockerWake
	for todoID, wake := range lastByTodo {
		ready, err := s.blockerUnblockStillHolds(ctx, superviseSignal{Todo: todoID, Key: blockerResolutionKey(wake.last)})
		if ready {
			summary.UnblockedTodoIDs = append(summary.UnblockedTodoIDs, todoID)
			wakes = append(wakes, wake)
		} else if err != nil {
			// An immediate post-commit read can fail. The durable wake rechecks before delivery.
			if !slices.ContainsFunc(remaining, func(b todoBlockerRecord) bool { return b.TodoID == todoID }) {
				wakes = append(wakes, wake)
			}
			writeErr = errors.Join(writeErr, err)
		}
	}
	slices.Sort(summary.UnblockedTodoIDs)
	slices.SortFunc(wakes, func(a, b todoBlockerWake) int { return strings.Compare(a.last.TodoID, b.last.TodoID) })
	return summary, wakes, errors.Join(mirrorErr, writeErr)
}

type todoAfterMirrorVerdict struct {
	ready   bool
	removed bool
}

// todoAfterMirrorVerdicts gives persisted after edges the same live verdict used by projection and dispatch.
func (s *Service) todoAfterMirrorVerdicts(ctx context.Context, rows []db.TodoBlockerRow) (map[string]todoAfterMirrorVerdict, error) {
	byTodo := make(map[string][]db.TodoBlockerRow)
	for _, row := range rows {
		if isTodoAfterMirror(row) {
			byTodo[row.TodoID] = append(byTodo[row.TodoID], row)
		}
	}
	verdicts := make(map[string]todoAfterMirrorVerdict)
	var readErr error
	for id, attached := range byTodo {
		todo, ok := s.todoNow(id)
		if !ok {
			continue
		}
		task, err := s.readinessPlan(ctx, todo)
		todo.After = cleanIDs(slices.Concat(todo.After, task.After))
		var readiness todoReadiness
		if err == nil {
			readiness, err = s.evaluateTodoAfter(ctx, todo, task, attached, true)
		}
		readErr = errors.Join(readErr, err)
		for _, row := range attached {
			named := slices.Contains(todo.After, row.TargetRef)
			verdicts[row.ID] = todoAfterMirrorVerdict{
				ready: err == nil && named && !slices.Contains(readiness.after, row.TargetRef), removed: err == nil && !named,
			}
		}
	}
	return verdicts, readErr
}

// collectUpstreamPRURLs gathers deduplicated upstream PR URLs observed on prerequisite to-dos and resolved
// external_pr blockers so sigTodoReady wakeups immediately expose upstream deliverable links.
func (s *Service) collectUpstreamPRURLs(ctx context.Context, todoID string, extraDepIDs []string, resolvedBlockers []todoBlockerRecord, extraPRs []string) []string {
	seen := make(map[string]struct{})
	var out []string
	addURL := func(raw string) {
		u := strings.TrimRight(strings.TrimSpace(raw), "/")
		if u == "" {
			return
		}
		key := strings.ToLower(normalizeTodoBlockerPRRef(u))
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, u)
	}
	seenDep := make(map[string]struct{})
	addDep := func(depID string) {
		depID = strings.TrimSpace(depID)
		if depID == "" {
			return
		}
		if _, dup := seenDep[depID]; dup {
			return
		}
		seenDep[depID] = struct{}{}
		dep, ok := s.todoNow(depID)
		if !ok {
			return
		}
		if dep.PRStatus != nil {
			addURL(dep.PRStatus.URL)
		}
		addURL(dep.PRURL)
		for _, u := range dep.PRs {
			addURL(u)
		}
		for _, other := range dep.OtherPRs {
			addURL(other.URL)
		}
	}

	for _, u := range extraPRs {
		addURL(u)
	}
	for _, depID := range extraDepIDs {
		addDep(depID)
	}
	for _, b := range resolvedBlockers {
		switch b.Kind {
		case todoBlockerKindExternalPR:
			addURL(b.TargetRef)
		case todoBlockerKindTodoDone, todoBlockerKindTodoStage:
			addDep(b.TargetRef)
		}
	}
	if t, ok := s.todoNow(todoID); ok {
		for _, depID := range cleanIDs(t.After) {
			addDep(depID)
		}
	}
	if len(resolvedBlockers) == 0 && s.settings(ctx).todosV2On() {
		if st := s.todoBlockerDB(); st != nil {
			if rows, err := st.ListTodoBlockersByTodo(ctx, todoID); err == nil {
				for _, r := range rows {
					if r.State != db.TodoBlockerStateResolved {
						continue
					}
					switch r.Kind {
					case todoBlockerKindExternalPR:
						addURL(r.TargetRef)
					case todoBlockerKindTodoDone, todoBlockerKindTodoStage:
						addDep(r.TargetRef)
					}
				}
			}
		}
	}
	return out
}

func laterBlockerResolution(row, previous db.TodoBlockerRow) bool {
	return row.ResolvedAt != nil && (previous.ResolvedAt == nil || row.ResolvedAt.After(*previous.ResolvedAt) ||
		row.ResolvedAt.Equal(*previous.ResolvedAt) && row.ID > previous.ID)
}

func blockerResolutionKey(row db.TodoBlockerRow) string {
	if row.ResolvedAt == nil {
		return ""
	}
	return row.ID + "@" + row.ResolvedAt.UTC().Format(time.RFC3339Nano)
}

func (s *Service) emitTodoBlockerWake(ctx context.Context, wake todoBlockerWake) {
	todoID, last := wake.last.TodoID, wake.last
	if !s.settings(ctx).todosV2On() {
		return // the persisted approved queue is still woken by the ordinary admission sweep
	}
	if t, ok := s.todoNow(todoID); ok && t.DoneAt == nil && !t.Cancelled() && last.ResolvedAt != nil {
		s.signal(superviseSignal{
			Todo:       todoID,
			Kind:       sigBlockersUnblocked,
			Key:        blockerResolutionKey(last),
			To:         s.todoReadySupervisor(ctx, s.settings(ctx), t),
			BlockerPRs: s.collectUpstreamPRURLs(ctx, todoID, nil, wake.resolved, nil),
		})
		s.maybeEmitTodoReady(ctx, t)
	}
	if h := s.todoBlockerHooks(false); h != nil {
		h.mu.RLock()
		fn := h.onUnblocked
		h.mu.RUnlock()
		if fn != nil {
			fn(ctx, todoID)
		}
	}
}

// blockerUnblockStillHolds checks both the current wait and the persisted resolution that ended this round.
func (s *Service) blockerUnblockStillHolds(ctx context.Context, g superviseSignal) (bool, error) {
	st := s.todoBlockerDB()
	todo, ok := s.todoNow(g.Todo)
	if st == nil || !ok || todo.DoneAt != nil || todo.Cancelled() {
		return false, nil
	}
	rows, err := st.ListTodoBlockersByTodo(ctx, g.Todo)
	if err != nil {
		return false, err
	}
	var latest db.TodoBlockerRow
	for _, row := range rows {
		if laterBlockerResolution(row, latest) {
			latest = row
		}
	}
	if g.Key == "" || g.Key != blockerResolutionKey(latest) {
		return false, nil
	}
	task, err := s.readinessPlan(ctx, todo)
	if err != nil {
		return false, err
	}
	readiness, err := s.evaluateTodoReadiness(ctx, todo, task, rows, s.settings(ctx).todosV2On())
	return err == nil && readiness.ready(), err
}

func (s *Service) buildBlockerFactSnapshot(ctx context.Context, active []todoBlockerRecord, observedPR *todoBlockerPRFact) (todoBlockerFactSnapshot, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return todoBlockerFactSnapshot{}, err
	}

	asks := s.todoAsks(ctx)
	facts := todoBlockerFactSnapshot{
		Todos:       make(map[string]todoBlockerTodoFact),
		PRs:         make(map[string]todoBlockerPRFact),
		MergeTrains: make(map[string]todoBlockerMergeTrainFact),
	}

	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		cp := copyTodoLocked(t)
		cancelled := cp.Cancelled()
		accepted := cp.DoneAt != nil && cp.DoneBy == db.DoneByReview &&
			cp.Supervision != nil && cp.Supervision.state(cp) == reviewAccepted
		stage, contracts := parseTodoStageAndContracts(cp)
		if norm, ok := stagegate.NormalizeStage(cp.Stage); ok {
			stage = maxStageGateStage(stage, norm)
		} else {
			switch strings.ToLower(strings.TrimSpace(cp.Stage)) {
			case "review":
				stage = maxStageGateStage(stage, stagegate.StagePROpen)
			case "merge", "deploy", "verify":
				stage = maxStageGateStage(stage, stagegate.StageMerged)
			}
		}
		if cp.DoneAt != nil {
			stage = maxStageGateStage(stage, stagegate.StageDone)
		} else if (cp.PRStatus != nil && strings.EqualFold(cp.PRStatus.State, "merged")) ||
			(cp.Delivery != nil && (cp.Delivery.State == deliveryMerged || cp.Delivery.State == deliveryLive)) {
			stage = maxStageGateStage(stage, stagegate.StageMerged)
		} else if todoHasOpenPR(cp, nil) {
			stage = maxStageGateStage(stage, stagegate.StagePROpen)
		} else if todoHasActiveAgent(cp) || len(cp.Agents) > 0 {
			stage = maxStageGateStage(stage, stagegate.StageDevelop)
		}
		_, hasAsk := asks[id]
		facts.Todos[id] = todoBlockerTodoFact{
			ID:              id,
			Done:            cp.DoneAt != nil,
			Accepted:        accepted,
			Cancelled:       cancelled,
			Stage:           string(stage),
			Contracts:       contracts,
			HasActiveAgent:  todoHasActiveAgent(cp),
			HasOpenDecision: hasAsk,
		}
		if cp.PRStatus != nil && strings.TrimSpace(cp.PRStatus.URL) != "" {
			u := strings.TrimRight(strings.TrimSpace(cp.PRStatus.URL), "/")
			facts.PRs[u] = todoBlockerPRFact{
				URL:    u,
				State:  cp.PRStatus.State,
				Merged: strings.EqualFold(cp.PRStatus.State, "merged"),
			}
		}
		for _, other := range cp.OtherPRs {
			if strings.TrimSpace(other.URL) == "" {
				continue
			}
			u := strings.TrimRight(strings.TrimSpace(other.URL), "/")
			facts.PRs[u] = todoBlockerPRFact{
				URL:    u,
				State:  other.State,
				Merged: strings.EqualFold(other.State, "merged"),
			}
		}
	}
	s.todos.mu.RUnlock()

	snap := s.mergeTrainSnapshot(ctx)
	for _, repo := range snap.Repos {
		mt := todoBlockerMergeTrainFact{
			RepoKey: repo.RepoKey,
		}
		if repo.Head != nil {
			mt.HeadTodoID = repo.Head.TodoID
			mt.HeadState = string(repo.Head.State)
			mt.ActiveTodoIDs = append(mt.ActiveTodoIDs, repo.Head.TodoID)
		}
		for _, q := range repo.Queue {
			mt.ActiveTodoIDs = append(mt.ActiveTodoIDs, q.TodoID)
		}
		facts.MergeTrains[repo.RepoKey] = mt
	}

	// Network observations are refreshed by the background worker. A committed, merged
	// local observation is terminal and newer targeted facts take precedence for this pass.
	now := time.Now()
	for _, b := range active {
		if b.Kind != todoBlockerKindExternalPR {
			continue
		}
		norm := normalizeTodoBlockerPRRef(b.TargetRef)
		if existing, ok := lookupTodoBlockerPRFact(facts.PRs, norm); ok && existing.Merged {
			continue
		}
		if fact, ok := s.blockerReconcile.prs.lookup(norm, now); ok {
			facts.PRs[norm] = fact
		}
	}
	if observedPR != nil {
		if norm := normalizeTodoBlockerPRRef(observedPR.URL); norm != "" {
			cp := *observedPR
			cp.URL = norm
			facts.PRs[norm] = cp
		}
	}

	return facts, nil
}

func (s *Service) fetchBlockerPRFact(ctx context.Context, targetRef string) (todoBlockerPRFact, bool) {
	if h := s.todoBlockerHooks(false); h != nil {
		h.mu.RLock()
		fn := h.fetchPR
		h.mu.RUnlock()
		if fn != nil {
			return fn(ctx, targetRef)
		}
	}
	return s.fetchExternalPRBlockerFact(ctx, targetRef)
}

func parseGitHubPRBlockerRef(targetRef string) (repo string, number int, ok bool) {
	if m := ciPRURL.FindStringSubmatch(targetRef); m != nil {
		n, err := strconv.Atoi(m[2])
		if err == nil && n > 0 {
			return m[1], n, true
		}
		return "", 0, false
	}
	if !strings.Contains(targetRef, "://") {
		if repoPart, numPart, found := strings.Cut(targetRef, "#"); found {
			parts := strings.Split(repoPart, "/")
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				if n, err := strconv.Atoi(numPart); err == nil && n > 0 {
					return repoPart, n, true
				}
			}
		}
	}
	return "", 0, false
}

// fetchExternalPRBlockerFact resolves live remote PR status for an external_pr blocker edge
// using the configured GitHub forge or supported code host, bounded by a short timeout so a
// single network failure never stalls blocker reconciliation.
func (s *Service) fetchExternalPRBlockerFact(ctx context.Context, targetRef string) (todoBlockerPRFact, bool) {
	targetRef = normalizeTodoBlockerPRRef(targetRef)
	if targetRef == "" {
		return todoBlockerPRFact{}, false
	}
	fetchCtx, cancel := context.WithTimeout(ctx, externalPRBlockerFetchTimeout)
	defer cancel()

	if repo, number, ok := parseGitHubPRBlockerRef(targetRef); ok {
		f, hasForge := s.forge().(prStatusForge)
		if !hasForge {
			return todoBlockerPRFact{}, false
		}
		st, err := f.PRStatus(fetchCtx, repo, number)
		if err != nil {
			s.logf("todo_blockers: fetch external PR %s: %v", targetRef, err)
			return todoBlockerPRFact{}, false
		}
		return todoBlockerPRFact{
			URL:    targetRef,
			State:  st.State,
			Merged: strings.EqualFold(st.State, "merged"),
		}, true
	}

	if otherReviewLink(targetRef) {
		other, _ := s.codeHostFor(targetRef)
		if other == nil {
			return todoBlockerPRFact{}, false
		}
		st, err := otherPRStatus(fetchCtx, other, targetRef)
		if err != nil {
			s.logf("todo_blockers: fetch external PR %s: %v", targetRef, err)
			return todoBlockerPRFact{}, false
		}
		return todoBlockerPRFact{
			URL:    targetRef,
			State:  st.State,
			Merged: strings.EqualFold(st.State, "merged"),
		}, true
	}

	return todoBlockerPRFact{}, false
}

func dbRowsToTodoBlockers(rows []db.TodoBlockerRow) []todoBlockerRecord {
	out := make([]todoBlockerRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, todoBlockerRecord{
			ID:            r.ID,
			TodoID:        r.TodoID,
			Kind:          r.Kind,
			TargetRef:     r.TargetRef,
			ConditionJSON: r.ConditionJSON,
			State:         r.State,
			Attempts:      r.Attempts,
		})
	}
	return out
}
