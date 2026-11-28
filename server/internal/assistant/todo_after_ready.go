package assistant

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/todo/stagegate"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Deterministic todo.after dependency auto-advance (#12606).
//
// When a prerequisite to-do completes (todo_complete / task_review accepted / autoDoneTodo) or its bound delivery PRs
// transition to MERGED (applyPRStatus), the hub deterministically checks open, unstarted dependent to-dos whose after
// list names that prerequisite. When every prerequisite in a dependent's after list is satisfied, the hub emits one
// durable, idempotent todo_ready supervision signal to the dependent's owning supervisor conversation (Main chat or
// Leader). It never auto-starts unapproved work silently.

const todoReadyWakesKey = "assistant.todo_ready_wakes"

// todoReadyWakeMarker records that dependent to-do TodoID was already woken for canonical prerequisite set Key.
type todoReadyWakeMarker struct {
	Key    string    `json:"key"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
}

// todoHasActiveAgent reports whether any Agent is currently active on to-do t.
func todoHasActiveAgent(t Todo) bool {
	return slices.ContainsFunc(t.Agents, func(a TodoAgent) bool { return a.active() })
}

// normalizePRKey returns a canonical comparison key for a PR URL across stage records and repository bindings.
func normalizePRKey(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if ciPRURL.MatchString(u) {
		return triageNormalizePRURL(u)
	}
	return strings.ToLower(strings.TrimRight(u, "/"))
}

// todoDeliveryPRs returns the deduplicated set of bound delivery PR URLs for to-do t using local state only (no
// network calls). Saved repository links with role "reference" are excluded even if they also appeared in t.PRURL or
// t.PRs. Any repository read error other than genuine not-found (including store_unavailable) is returned so callers
// fail closed rather than falsely satisfying a merge prerequisite.
func (s *Service) todoDeliveryPRs(ctx context.Context, t Todo) ([]string, error) {
	refPRs := map[string]bool{}
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		key := normalizePRKey(u)
		if key == "" || refPRs[key] || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, u)
	}

	if _, hasRepoStore := s.store().(todoRepositoryStore); hasRepoStore {
		set, err := s.savedTodoRepositories(ctx, t.ID)
		if err != nil {
			var problem repository.Problem
			if !errors.Is(err, db.ErrTodoNotFound) && (!errors.As(err, &problem) || problem != "not_found") {
				s.logf("todo_ready: read repositories for prerequisite %s: %v", t.ID, err)
				return nil, err
			}
		} else {
			for _, row := range set.Repositories {
				for _, l := range row.Links {
					if l.Kind != "pull_request" {
						continue
					}
					u := strings.TrimSpace(l.CanonicalURL)
					if u == "" {
						continue
					}
					role := l.Role
					if role == "" {
						role = repository.LinkRoleDelivery
					}
					if role == repository.LinkRoleReference {
						refPRs[normalizePRKey(u)] = true
						continue
					}
					add(u)
				}
			}
		}
	}

	for _, u := range append([]string{t.PRURL}, t.PRs...) {
		u = strings.TrimSpace(u)
		if u == "" || !ciPRURL.MatchString(u) && !otherReviewLink(u) {
			continue
		}
		add(u)
	}
	return out, nil
}

// todoObservedPRState finds the latest observed PR status for url on to-do t, matching exact or normalized URLs.
func todoObservedPRState(t Todo, url string) (TodoPRStatus, bool) {
	if f := prFacts(t, url); f != nil {
		return *f, true
	}
	want := normalizePRKey(url)
	if want == "" {
		return TodoPRStatus{}, false
	}
	if t.PRStatus != nil && normalizePRKey(t.PRStatus.URL) == want {
		return *t.PRStatus, true
	}
	for _, f := range t.OtherPRs {
		if normalizePRKey(f.URL) == want {
			return f, true
		}
	}
	return TodoPRStatus{}, false
}

// todoObservedMergedPRs returns the merged PR URLs known for prerequisite t, preserving delivery order and never
// fabricating merged status for an open or unmerged-closed PR.
func todoObservedMergedPRs(t Todo, delivery []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		key := normalizePRKey(u)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, u)
	}
	for _, u := range delivery {
		if f, ok := todoObservedPRState(t, u); ok && strings.EqualFold(f.State, "merged") {
			add(u)
		}
	}
	if t.PRStatus != nil && strings.EqualFold(t.PRStatus.State, "merged") {
		add(t.PRStatus.URL)
	}
	for _, f := range t.OtherPRs {
		if strings.EqualFold(f.State, "merged") {
			add(f.URL)
		}
	}
	if t.DoneAt != nil && t.PRURL != "" && t.PRStatus == nil &&
		t.Delivery != nil && (t.Delivery.State == deliveryMerged || t.Delivery.State == deliveryLive) {
		add(t.PRURL)
	}
	return out
}

// prerequisiteSatisfied reports whether prerequisite dep counts as satisfied under exact-delivery rules:
//   - Cancelled or failed-start to-dos never satisfy a dependency.
//   - Under a RequireAcceptedAfter root, dep must be accepted and closed via task_review accepted.
//   - Otherwise, dep is satisfied when it is done, or when it has at least one bound delivery PR and every bound
//     delivery PR is observed as MERGED (merely opening or closing without merging never satisfies).
//
// A non-nil error indicates a transient store read failure while verifying an open prerequisite's delivery PRs.
func (s *Service) prerequisiteSatisfied(ctx context.Context, dep Todo, requireAccepted bool) (prompts.SupervisePrerequisite, bool, error) {
	if dep.Cancelled() || dep.StartError != "" {
		return prompts.SupervisePrerequisite{}, false, nil
	}
	delivery, deliveryErr := s.todoDeliveryPRs(ctx, dep)
	if requireAccepted {
		if dep.DoneAt == nil || dep.DoneBy != db.DoneByReview || dep.Supervision == nil || dep.Supervision.state(dep) != reviewAccepted {
			return prompts.SupervisePrerequisite{}, false, nil
		}
		return prompts.SupervisePrerequisite{
			ID:        dep.ID,
			Title:     dep.Title,
			Reason:    "done",
			MergedPRs: todoObservedMergedPRs(dep, delivery),
		}, true, nil
	}
	if dep.DoneAt != nil {
		return prompts.SupervisePrerequisite{
			ID:        dep.ID,
			Title:     dep.Title,
			Reason:    "done",
			MergedPRs: todoObservedMergedPRs(dep, delivery),
		}, true, nil
	}
	if deliveryErr != nil {
		return prompts.SupervisePrerequisite{}, false, deliveryErr
	}
	if len(delivery) == 0 {
		return prompts.SupervisePrerequisite{}, false, nil
	}
	for _, u := range delivery {
		f, ok := todoObservedPRState(dep, u)
		if !ok || !strings.EqualFold(f.State, "merged") {
			return prompts.SupervisePrerequisite{}, false, nil
		}
	}
	return prompts.SupervisePrerequisite{
		ID:        dep.ID,
		Title:     dep.Title,
		Reason:    "pr_merged",
		MergedPRs: delivery,
	}, true, nil
}

// todoStageUnlockRule holds explicit stage/contract early-unlock criteria declared on a dependent to-do.
type todoStageUnlockRule struct {
	Stage     stagegate.Stage
	Contracts []string
}

// todoMetadataSources returns the structured text fields inspected for stage/contract declarations.
func todoMetadataSources(t Todo) []string {
	var out []string
	if s := strings.TrimSpace(t.Notes); s != "" {
		out = append(out, s)
	}
	if t.Brief != nil {
		if g := strings.TrimSpace(t.Brief.Goal); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// splitStageGateValues splits comma-, semicolon-, or whitespace-separated tag lists.
func splitStageGateValues(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
}

// parseStageGateKV parses a single "key: value" or "key=value" declaration into a lowercased key and trimmed value.
func parseStageGateKV(raw string) (string, string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimLeft(s, "-*• \t")
	if s == "" {
		return "", "", false
	}
	sep := strings.IndexAny(s, ":=")
	if sep <= 0 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(s[:sep]))
	val := strings.TrimSpace(s[sep+1:])
	if key == "" || val == "" {
		return "", "", false
	}
	return key, val, true
}

// scanStageGateDecls extracts both bracketed inline tags (e.g. [stage:contract_ready], [unlock_stage: pr_open]) and
// line-level key/value declarations (e.g. unlock_stage: pr_open) from text.
func scanStageGateDecls(text string, visit func(key, val string)) {
	rest := text
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			break
		}
		rest = rest[open+1:]
		close := strings.IndexByte(rest, ']')
		if close < 0 {
			break
		}
		if k, v, ok := parseStageGateKV(rest[:close]); ok {
			visit(k, v)
		}
		rest = rest[close+1:]
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "[") {
			continue
		}
		if k, v, ok := parseStageGateKV(trimmed); ok {
			visit(k, v)
		}
	}
}

// maxStageGateStage returns whichever built-in stage has the higher monotonic rank.
func maxStageGateStage(a, b stagegate.Stage) stagegate.Stage {
	ra, oka := stagegate.StageRank(a)
	rb, okb := stagegate.StageRank(b)
	switch {
	case !oka && !okb:
		return ""
	case !oka:
		return b
	case !okb:
		return a
	case rb > ra:
		return b
	default:
		return a
	}
}

// parseTodoStageUnlockRule extracts explicit stage/contract unlock criteria from a dependent to-do's notes or brief.
// When no valid unlock_stage or unlock_contracts declaration is present, it returns (zero, false) so callers fall back
// strictly to stagegate.CompletionEdges.
func parseTodoStageUnlockRule(t Todo) (todoStageUnlockRule, bool) {
	var rule todoStageUnlockRule
	var rawContracts []string
	for _, src := range todoMetadataSources(t) {
		scanStageGateDecls(src, func(key, val string) {
			switch key {
			case "unlock_stage":
				if norm, ok := stagegate.NormalizeStage(val); ok {
					rule.Stage = norm
				}
			case "unlock_contract", "unlock_contracts":
				rawContracts = append(rawContracts, splitStageGateValues(val)...)
			}
		})
	}
	rule.Contracts = stagegate.NormalizeContracts(rawContracts)
	if rule.Stage == "" && len(rule.Contracts) == 0 {
		return todoStageUnlockRule{}, false
	}
	return rule, true
}

// parseTodoStageAndContracts extracts explicit stage and contract readiness tags declared on a prerequisite to-do
// (for example [contract:ready], [stage:contract_ready], or contracts: api_contract).
func parseTodoStageAndContracts(t Todo) (stagegate.Stage, []string) {
	var stage stagegate.Stage
	var rawContracts []string
	for _, src := range todoMetadataSources(t) {
		scanStageGateDecls(src, func(key, val string) {
			switch key {
			case "stage":
				if norm, ok := stagegate.NormalizeStage(val); ok {
					stage = maxStageGateStage(stage, norm)
				}
			case "contract", "contracts":
				tags := stagegate.NormalizeContracts(splitStageGateValues(val))
				if len(tags) > 0 {
					rawContracts = append(rawContracts, tags...)
					stage = maxStageGateStage(stage, stagegate.StageContractReady)
				}
			}
		})
	}
	return stage, stagegate.NormalizeContracts(rawContracts)
}

// todoAllDeliveryPRsMerged reports whether t has at least one bound delivery PR and every one is observed as merged.
func todoAllDeliveryPRsMerged(t Todo, delivery []string) bool {
	if len(delivery) == 0 {
		return false
	}
	for _, u := range delivery {
		f, ok := todoObservedPRState(t, u)
		if !ok || !strings.EqualFold(f.State, "merged") {
			return false
		}
	}
	return true
}

// todoHasOpenPR reports whether any observed PR on t is currently open.
func todoHasOpenPR(t Todo, delivery []string) bool {
	if t.PRStatus != nil && strings.EqualFold(t.PRStatus.State, "open") {
		return true
	}
	for _, f := range t.OtherPRs {
		if strings.EqualFold(f.State, "open") {
			return true
		}
	}
	for _, u := range delivery {
		if f, ok := todoObservedPRState(t, u); ok && strings.EqualFold(f.State, "open") {
			return true
		}
	}
	return false
}

// todoStageGateNode projects a live Todo into a pure stagegate.Node for todos_v2 dependency evaluation.
// Cancelled and failed-start to-dos never report Done, Accepted, Stage, or Contracts so evaluation always fails closed.
func todoStageGateNode(t Todo, requireAccepted bool, delivery []string) stagegate.Node {
	return todoStageGateNodeWithRule(t, requireAccepted, delivery, nil)
}

func todoStageGateNodeWithRule(t Todo, requireAccepted bool, delivery []string, overrideRule *todoStageUnlockRule) stagegate.Node {
	afterIDs := cleanIDs(t.After)
	effectiveRequireAccepted := requireAccepted || t.RequireAcceptedAfter
	var edges []stagegate.Edge
	if len(afterIDs) > 0 {
		rule, hasRule := parseTodoStageUnlockRule(t)
		if !hasRule && overrideRule != nil && (overrideRule.Stage != "" || len(overrideRule.Contracts) > 0) {
			rule = *overrideRule
			hasRule = true
		}
		if hasRule {
			sortedIDs := slices.Clone(afterIDs)
			slices.Sort(sortedIDs)
			edges = make([]stagegate.Edge, 0, len(sortedIDs))
			for _, id := range sortedIDs {
				edges = append(edges, stagegate.Edge{
					PrereqID:          id,
					Mode:              stagegate.UnlockOnStage,
					RequiredStage:     rule.Stage,
					RequiredContracts: rule.Contracts,
					RequireAccepted:   effectiveRequireAccepted,
				})
			}
		} else {
			edges = stagegate.CompletionEdges(afterIDs, effectiveRequireAccepted)
		}
	}

	cancelled := t.Cancelled() || t.DoneBy == db.DoneByCancelled
	failedStart := t.StartError != ""
	if cancelled || failedStart {
		return stagegate.Node{
			TodoID:    t.ID,
			Cancelled: cancelled,
			After:     edges,
		}
	}

	allMerged := todoAllDeliveryPRsMerged(t, delivery)
	hasOpenPR := todoHasOpenPR(t, delivery)
	done := t.DoneAt != nil || allMerged
	accepted := t.DoneAt != nil && t.DoneBy == db.DoneByReview && t.Supervision != nil && t.Supervision.state(t) == reviewAccepted

	stage, contracts := parseTodoStageAndContracts(t)
	if norm, ok := stagegate.NormalizeStage(t.Stage); ok {
		stage = maxStageGateStage(stage, norm)
	} else {
		switch strings.ToLower(strings.TrimSpace(t.Stage)) {
		case "review":
			if hasOpenPR || (t.PRURL != "" && (t.PRStatus == nil || !strings.EqualFold(t.PRStatus.State, "closed"))) {
				stage = maxStageGateStage(stage, stagegate.StagePROpen)
			}
		case "merge", "deploy", "verify":
			stage = maxStageGateStage(stage, stagegate.StageMerged)
		}
	}
	switch {
	case t.DoneAt != nil:
		stage = maxStageGateStage(stage, stagegate.StageDone)
	case allMerged || (t.Delivery != nil && (t.Delivery.State == deliveryMerged || t.Delivery.State == deliveryLive)):
		stage = maxStageGateStage(stage, stagegate.StageMerged)
	case hasOpenPR:
		stage = maxStageGateStage(stage, stagegate.StagePROpen)
	case todoHasActiveAgent(t) || len(t.Agents) > 0:
		stage = maxStageGateStage(stage, stagegate.StageDevelop)
	}

	return stagegate.Node{
		TodoID:    t.ID,
		Stage:     stage,
		Contracts: contracts,
		Done:      done,
		Accepted:  accepted,
		Cancelled: false,
		After:     edges,
	}
}

// todoStageGateSatisfiedReason summarizes why a prerequisite satisfied a stagegate evaluation.
func todoStageGateSatisfiedReason(dep Todo, delivery []string, node stagegate.Node) string {
	if dep.DoneAt != nil {
		return "done"
	}
	if todoAllDeliveryPRsMerged(dep, delivery) {
		return "pr_merged"
	}
	if node.Stage != "" {
		return "stage:" + string(node.Stage)
	}
	return "stage_ready"
}

// evaluateTodoAfterStageGate evaluates candidate's after prerequisites using the pure stagegate package when
// todos_v2=true. It captures the transitive after subgraph for cycle detection and fails closed on missing
// prerequisites, cancelled prerequisites, dependency cycles, or transient repository-store read errors.
func (s *Service) evaluateTodoAfterStageGate(ctx context.Context, candidate Todo, rule *todoStageUnlockRule) (todoReadiness, error) {
	afterIDs := candidate.After
	reachable := map[string]Todo{candidate.ID: candidate}
	reqAcceptedByID := map[string]bool{}

	s.todos.mu.RLock()
	reqAcceptedByID[candidate.ID] = candidate.PartOf != "" && s.hasRequestGateLocked(candidate.PartOf)
	queue := append([]string(nil), afterIDs...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := reachable[id]; seen {
			continue
		}
		t, ok := s.todos.todos[id]
		if !ok {
			continue
		}
		cp := copyTodoLocked(t)
		reachable[id] = cp
		reqAcceptedByID[id] = cp.PartOf != "" && s.hasRequestGateLocked(cp.PartOf)
		for _, nextID := range cleanIDs(cp.After) {
			if _, seen := reachable[nextID]; !seen {
				queue = append(queue, nextID)
			}
		}
	}
	s.todos.mu.RUnlock()

	deliveryByID := make(map[string][]string, len(afterIDs))
	for _, id := range afterIDs {
		dep, ok := reachable[id]
		if !ok {
			continue
		}
		delivery, deliveryErr := s.todoDeliveryPRs(ctx, dep)
		if deliveryErr != nil && dep.DoneAt == nil && !dep.Cancelled() && dep.StartError == "" {
			return todoReadiness{}, deliveryErr
		}
		deliveryByID[id] = delivery
	}

	ids := make([]string, 0, len(reachable))
	for id := range reachable {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	nodes := make([]stagegate.Node, 0, len(ids))
	nodeByID := make(map[string]stagegate.Node, len(ids))
	for _, id := range ids {
		var ruleOverride *todoStageUnlockRule
		if id == candidate.ID {
			ruleOverride = rule
		}
		n := todoStageGateNodeWithRule(reachable[id], reqAcceptedByID[id], deliveryByID[id], ruleOverride)
		nodes = append(nodes, n)
		nodeByID[id] = n
	}

	ev := stagegate.Evaluate(nodes, candidate.ID, true)
	if ev.Status == stagegate.StatusCycleDetected {
		return todoReadiness{after: afterIDs}, nil
	}

	var result todoReadiness
	for _, dep := range ev.Unsatisfied {
		result.after = append(result.after, dep.PrereqID)
	}
	for _, id := range afterIDs {
		if slices.Contains(result.after, id) {
			continue
		}
		dep := reachable[id]
		delivery := deliveryByID[id]
		result.prerequisites = append(result.prerequisites, prompts.SupervisePrerequisite{
			ID:        dep.ID,
			Title:     dep.Title,
			Reason:    todoStageGateSatisfiedReason(dep, delivery, nodeByID[id]),
			MergedPRs: todoObservedMergedPRs(dep, delivery),
		})
	}
	return result, nil
}

// checkTodoAfterReady checks whether candidate is an open, unstarted dependent to-do whose after prerequisites are
// all satisfied, distinguishing a transient store read failure (err != nil) from a genuinely unready/ineligible to-do.
func (s *Service) checkTodoAfterReady(ctx context.Context, candidate Todo) ([]prompts.SupervisePrerequisite, bool, error) {
	if candidate.DoneAt != nil || candidate.Cancelled() || candidate.StartError != "" ||
		candidate.isConcern() || isAskTodo(candidate.ID) {
		return nil, false, nil
	}
	if todoHasActiveAgent(candidate) || len(cleanIDs(candidate.After)) == 0 {
		return nil, false, nil
	}
	task, err := s.readinessPlan(ctx, candidate)
	if err != nil || task.Queue != nil && (task.RequestGate != "" || task.Queue.Workspace != nil) {
		return nil, false, err
	}
	result, err := s.readTodoReadiness(ctx, candidate, task, false)
	if err != nil || !result.ready() {
		return nil, false, err
	}
	return result.prerequisites, true, nil
}

// todoAfterReadyState is checkTodoAfterReady when callers only need a boolean readiness verdict.
func (s *Service) todoAfterReadyState(ctx context.Context, candidate Todo) ([]prompts.SupervisePrerequisite, bool) {
	prereqs, ready, err := s.checkTodoAfterReady(ctx, candidate)
	return prereqs, ready && err == nil
}

// clearSentTodoReady removes in-memory sent markers for sigTodoReady signals that were dropped before delivery, so a
// later recovery or re-completion is never permanently suppressed until restart. Delivered wakes remain deduplicated
// by assistant.todo_ready_wakes and assistant.supervision_dedup.
func (s *Service) clearSentTodoReady(list []superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range list {
		if g.Kind == sigTodoReady && b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
	}
}

// resetTodoReadyWake clears pending/sent in-memory todo_ready signals, the durable assistant.todo_ready_wakes marker,
// and the task-bound todo_ready supervision dedup fingerprint for todoID when that to-do is reopened, so a subsequent
// readiness transition can wake its owner once.
func (s *Service) resetTodoReadyWake(ctx context.Context, todoID string) {
	todoID = strings.TrimSpace(todoID)
	if todoID == "" {
		return
	}
	sigID := superviseSignal{Todo: todoID, Kind: sigTodoReady}.id()
	b := &s.supervise
	b.mu.Lock()
	delete(b.pending, sigID)
	delete(b.sent, sigID)
	b.mu.Unlock()

	tb := &s.todos
	tb.readyWakeMu.Lock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		s.logf("todo_ready: load wake markers for reset (%s): %v", todoID, err)
	} else if _, ok := tb.readyWakes[todoID]; ok {
		next := maps.Clone(tb.readyWakes)
		delete(next, todoID)
		if st := s.store(); st != nil {
			if err := st.PutSetting(ctx, todoReadyWakesKey, next); err != nil {
				s.logf("todo_ready: clear wake marker for %s: %v", todoID, err)
			} else {
				tb.readyWakes = next
			}
		} else {
			tb.readyWakes = next
		}
	}
	tb.readyWakeMu.Unlock()

	s.clearTodoReadyDedup(ctx, todoID)
}

// todoReadyKey is the canonical idempotency key for candidate's current after prerequisite set and reopen generation.
// It is independent of prerequisite order and of whether a prerequisite was satisfied first via PR merge and later
// marked done. Truncating ReopenedAt to microsecond precision keeps keys stable across Postgres round-trips.
func todoReadyKey(candidate Todo) string {
	ids := cleanIDs(candidate.After)
	sort.Strings(ids)
	key := "after:" + strings.Join(ids, ",")
	if candidate.ReopenedAt != nil && !candidate.ReopenedAt.IsZero() {
		key += "@reopen:" + candidate.ReopenedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	return key
}

// todoReadySupervisor resolves the owning supervisor conversation (Leader or Main chat) for an unstarted dependent
// to-do. A to-do owned by a task leader agent returns "" so leaderRoute delivers to that agent; otherwise Main-owned
// to-dos go directly to cfg.Main (never to the background supervisor).
func (s *Service) todoReadySupervisor(ctx context.Context, cfg Settings, candidate Todo) string {
	isLiveConversation := func(id string) bool {
		if id == "" || id == cfg.Main || id == cfg.BackgroundSupervisor || s.isLeaderRef(id) {
			return false
		}
		if s.isLeader(ctx, id) {
			return true
		}
		if cfg.MachineID != "" {
			if c, ok := s.session(ctx, cfg.MachineID, id); ok && c.Assistant {
				return true
			}
		}
		return false
	}

	if sup := supervisorOf(candidate); sup != "" {
		if s.isLeaderRef(sup) {
			return ""
		}
		if isLiveConversation(sup) {
			return sup
		}
		if sup == cfg.Main {
			return cfg.Main
		}
	}
	if orig := candidate.Origin; orig != "" {
		if isLiveConversation(orig) {
			return orig
		}
		if orig == cfg.Main {
			return cfg.Main
		}
	}
	if candidate.PartOf != "" {
		if root, ok := s.todoNow(candidate.PartOf); ok {
			if sup := supervisorOf(root); s.isLeader(ctx, sup) {
				return sup
			}
			if s.isLeader(ctx, root.Origin) {
				return root.Origin
			}
		}
	}
	// If all prerequisites belong to one leader conversation, wake that leader.
	var sharedLeader string
	ambiguous := false
	for _, depID := range cleanIDs(candidate.After) {
		dep, ok := s.todoNow(depID)
		if !ok {
			continue
		}
		lead := ""
		if sup := supervisorOf(dep); s.isLeader(ctx, sup) {
			lead = sup
		} else if s.isLeader(ctx, dep.Origin) {
			lead = dep.Origin
		}
		if lead == "" {
			continue
		}
		if sharedLeader == "" {
			sharedLeader = lead
		} else if sharedLeader != lead {
			ambiguous = true
			break
		}
	}
	if sharedLeader != "" && !ambiguous {
		return sharedLeader
	}
	return cfg.Main
}

// loadTodoReadyWakesLocked loads durable wake markers from the settings store once per Service lifetime.
// s.todos.readyWakeMu must be held.
func (s *Service) loadTodoReadyWakesLocked(ctx context.Context) error {
	b := &s.todos
	if b.readyWakesLoaded {
		if b.readyWakes == nil {
			b.readyWakes = map[string]todoReadyWakeMarker{}
		}
		return nil
	}
	if st := s.store(); st != nil {
		loaded := map[string]todoReadyWakeMarker{}
		if _, err := st.Setting(ctx, todoReadyWakesKey, &loaded); err != nil {
			return err
		}
		b.readyWakes = loaded
	} else if b.readyWakes == nil {
		b.readyWakes = map[string]todoReadyWakeMarker{}
	}
	b.readyWakesLoaded = true
	return nil
}

// todoReadyWakeRecorded reports whether dependent todoID already has a persisted wake marker for key.
func (s *Service) todoReadyWakeRecorded(ctx context.Context, todoID, key string) (bool, error) {
	b := &s.todos
	b.readyWakeMu.Lock()
	defer b.readyWakeMu.Unlock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		return false, err
	}
	m, ok := b.readyWakes[todoID]
	return ok && m.Key == key, nil
}

// todoReadyTold persists idempotent wake markers for every sigTodoReady signal that was delivered or durably queued,
// so restarts and repeated sweeps never re-emit duplicate wakes.
func (s *Service) todoReadyTold(ctx context.Context, list []superviseSignal) {
	var ready []superviseSignal
	for _, g := range list {
		if g.Kind == sigTodoReady && g.Todo != "" && g.Key != "" {
			ready = append(ready, g)
		}
	}
	if len(ready) == 0 {
		return
	}
	b := &s.todos
	b.readyWakeMu.Lock()
	defer b.readyWakeMu.Unlock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		s.logf("todo_ready: load wake markers: %v", err)
		return
	}
	next := maps.Clone(b.readyWakes)
	if next == nil {
		next = map[string]todoReadyWakeMarker{}
	}
	now := time.Now().UTC()
	changed := false
	for _, g := range ready {
		if cur, ok := next[g.Todo]; ok && cur.Key == g.Key {
			continue
		}
		next[g.Todo] = todoReadyWakeMarker{Key: g.Key, Target: g.To, At: now}
		changed = true
	}
	if !changed {
		return
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, todoReadyWakesKey, next); err != nil {
			s.logf("todo_ready: persist wake markers: %v", err)
			return
		}
	}
	b.readyWakes = next
}

// pruneTodoReadyWakes removes wake markers for to-dos that have been completed or deleted so the stored map stays
// bounded by open dependent to-dos.
func (s *Service) pruneTodoReadyWakes(ctx context.Context) {
	b := &s.todos
	b.readyWakeMu.Lock()
	defer b.readyWakeMu.Unlock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		s.logf("todo_ready: load wake markers for prune: %v", err)
		return
	}
	if len(b.readyWakes) == 0 {
		return
	}
	var remove []string
	b.mu.RLock()
	if b.loaded {
		for id := range b.readyWakes {
			if t := b.todos[id]; t == nil || t.DoneAt != nil {
				remove = append(remove, id)
			}
		}
	}
	b.mu.RUnlock()
	if len(remove) == 0 {
		return
	}
	next := maps.Clone(b.readyWakes)
	for _, id := range remove {
		delete(next, id)
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, todoReadyWakesKey, next); err != nil {
			s.logf("todo_ready: prune wake markers: %v", err)
			return
		}
	}
	b.readyWakes = next
}

// maybeEmitTodoReady evaluates candidate and, if ready and not already woken for its current after set, queues a
// todo_ready supervision signal for its owning supervisor conversation.
func (s *Service) maybeEmitTodoReady(ctx context.Context, candidate Todo) {
	prereqs, ready := s.todoAfterReadyState(ctx, candidate)
	if !ready {
		return
	}
	key := todoReadyKey(candidate)
	recorded, err := s.todoReadyWakeRecorded(ctx, candidate.ID, key)
	if err != nil {
		s.logf("todo_ready: check wake marker for %s: %v", candidate.ID, err)
		return
	}
	if recorded {
		return
	}
	cfg := s.settings(ctx)
	to := s.todoReadySupervisor(ctx, cfg, candidate)
	// The sweep takes a snapshot before reading the plans and prerequisites. A workspace waiter can
	// finish its automatic dispatch in the meantime. Its cleared queue is no longer a request to start it.
	if s.isWorkspaceDispatching(candidate.ID) {
		return
	}
	current, ok := s.todoNow(candidate.ID)
	if !ok || current.DoneAt != nil || current.Cancelled() || current.StartError != "" || todoHasActiveAgent(current) ||
		current.PlanID != candidate.PlanID || current.Origin != candidate.Origin || current.PartOf != candidate.PartOf ||
		todoReadyKey(current) != key {
		return
	}
	var extraPRs []string
	for _, p := range prereqs {
		extraPRs = append(extraPRs, p.MergedPRs...)
	}
	detail := ""
	if prURLs := s.collectUpstreamPRURLs(ctx, candidate.ID, current.After, nil, extraPRs); len(prURLs) > 0 {
		detail = "upstream PRs: " + strings.Join(prURLs, ", ")
	}
	s.signal(superviseSignal{
		Todo:   candidate.ID,
		Kind:   sigTodoReady,
		Key:    key,
		To:     to,
		Detail: detail,
	})
}

// evaluateTodoAfterDependents deterministically checks every open dependent to-do whose after list names prereqID.
func (s *Service) evaluateTodoAfterDependents(ctx context.Context, prereqID string) {
	prereqID = strings.TrimSpace(prereqID)
	if prereqID == "" {
		return
	}
	s.todos.mu.RLock()
	var prereq Todo
	var hasPrereq bool
	if p, ok := s.todos.todos[prereqID]; ok {
		prereq, hasPrereq = copyTodoLocked(p), true
	}
	var candidates []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && slices.Contains(t.After, prereqID) {
			candidates = append(candidates, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	if len(candidates) == 0 {
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, c := range candidates {
		s.maybeEmitTodoReady(ctx, c)
		if hasPrereq && needsUserWakeEligible(c) {
			s.todos.mu.RLock()
			requireAccepted := c.PartOf != "" && s.hasRequestGateLocked(c.PartOf)
			s.todos.mu.RUnlock()
			if p, ok, err := s.prerequisiteSatisfied(ctx, prereq, requireAccepted); err == nil && ok {
				summary := "Prerequisite " + p.Title + " (" + p.ID + ") is now " + p.Reason
				if len(p.MergedPRs) > 0 {
					summary += " (" + strings.Join(p.MergedPRs, ", ") + ")"
				}
				s.emitNeedsUserExternalInfo(ctx, c, "prerequisite "+p.ID, summary, "prereq:"+p.ID+":"+p.Reason)
			}
		}
	}
}

// sweepTodoAfterReady reconciles open dependent to-dos on startup / supervision sweeps so wakes lost before delivery
// are recovered while already-delivered wakes stay suppressed by their persisted markers.
func (s *Service) sweepTodoAfterReady(ctx context.Context) {
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	s.pruneTodoReadyWakes(ctx)
	s.todos.mu.RLock()
	var candidates []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && len(t.After) > 0 {
			candidates = append(candidates, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	if len(candidates) == 0 {
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, c := range candidates {
		s.maybeEmitTodoReady(ctx, c)
	}
}
