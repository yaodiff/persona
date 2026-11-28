package assistant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/todo/conflict"
	"github.com/nwverse/persona/server/internal/proto"
)

// planWriteScope is the scope explicitly declared on task_plan and retained on the saved plan.
type planWriteScope struct {
	WritePrefixes []string `json:"write_prefixes,omitempty"`
	ChangedFiles  []string `json:"changed_files,omitempty"`
	ForceParallel bool     `json:"force_parallel,omitempty"`
}

type plannedWriteAssignment struct {
	planID string
	scope  conflict.TaskScope
	title  string
}

type writeConflictWake struct {
	PeerTodo   string `json:"peer_todo"`
	PlanID     string `json:"plan_id"`
	PeerPlanID string `json:"peer_plan_id"`
}

func (w writeConflictWake) key() string { return w.PlanID + "|" + w.PeerPlanID }

func declaredTaskScope(t PlanTask, todoID string) conflict.TaskScope {
	if t.WriteScope == nil {
		return conflict.TaskScope{}
	}
	return conflict.TaskScope{TodoID: todoID, RepoKey: plannedTaskRepoKey(t), WritePrefixes: t.WriteScope.WritePrefixes,
		ChangedFiles: t.WriteScope.ChangedFiles, ForceParallel: t.WriteScope.ForceParallel}
}

// rememberPlannedWriteScopes updates the conflict check's active assignments after the plan binds to actual to-dos.
// A replacement plan supplies its own scope, including an empty one.
func (s *Service) rememberPlannedWriteScopes(ctx context.Context, p *TaskPlan) {
	enabled := s.settings(ctx).todosV2On()
	hasScope := false
	s.tasks.mu.Lock()
	for _, t := range p.Tasks {
		if t.TodoID == "" || t.Error != "" {
			continue
		}
		scope := declaredTaskScope(t, t.TodoID)
		if !enabled || scope.RepoKey == "" {
			delete(s.tasks.writeScopes, t.TodoID)
			continue
		}
		if s.tasks.writeScopes == nil {
			s.tasks.writeScopes = make(map[string]plannedWriteAssignment)
		}
		s.tasks.writeScopes[t.TodoID] = plannedWriteAssignment{planID: p.ID, scope: scope, title: t.Title}
		hasScope = true
	}
	s.tasks.mu.Unlock()
	if !hasScope {
		return
	}

	open := s.openWriteScopes()
	scopes := make([]conflict.TaskScope, 0, len(open))
	for _, assignment := range open {
		scopes = append(scopes, assignment.scope)
	}
	for _, pair := range conflict.Detect(scopes) {
		if pair.Action != conflict.ActionSerialize {
			continue
		}
		task, peer := open[pair.TodoA], open[pair.TodoB]
		if task.planID != p.ID {
			task, peer = peer, task
		}
		if task.planID != p.ID {
			continue
		}
		if t, ok := s.todoNow(task.scope.TodoID); ok && t.DoneAt == nil && !t.Cancelled() {
			wake := &writeConflictWake{PeerTodo: peer.scope.TodoID, PlanID: task.planID, PeerPlanID: peer.planID}
			s.signal(superviseSignal{Todo: t.ID, Kind: sigWriteConflict, Key: wake.key(), WriteConflict: wake,
				To: s.todoReadySupervisor(ctx, s.settings(ctx), t)})
		}
	}
}

func (s *Service) openWriteScopes() map[string]plannedWriteAssignment {
	s.tasks.mu.Lock()
	open := maps.Clone(s.tasks.writeScopes)
	s.tasks.mu.Unlock()
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for id := range open {
		if t := s.todos.todos[id]; t == nil || t.DoneAt != nil || t.Cancelled() {
			delete(open, id)
		}
	}
	return open
}

// planWriteScopeConflicts is the immediate task_plan advisory, before approval or dispatch.
func (s *Service) planWriteScopeConflicts(ctx context.Context, planned []PlanTask) string {
	if !s.settings(ctx).todosV2On() {
		return ""
	}
	labels := make(map[string]string, len(planned))
	plannedIDs := make(map[string]bool, len(planned))
	skipExisting := make(map[string]bool, len(planned))
	scopes := make([]conflict.TaskScope, 0, len(planned))
	for i, task := range planned {
		id := task.ReuseTodo
		if id == "" {
			id = fmt.Sprintf("planned:%d", i+1)
		}
		skipExisting[task.ReuseTodo], skipExisting[task.Continues] = true, true
		scope := declaredTaskScope(task, id)
		if scope.RepoKey == "" {
			continue
		}
		plannedIDs[id] = true
		if task.ReuseTodo == "" {
			labels[id] = fmt.Sprintf("planned #%d (%s)", i+1, task.Title)
		} else {
			labels[id] = fmt.Sprintf("%s (%s)", id, task.Title)
		}
		scopes = append(scopes, scope)
	}
	if len(scopes) == 0 {
		return ""
	}
	for id, assignment := range s.openWriteScopes() {
		if !skipExisting[id] {
			scopes = append(scopes, assignment.scope)
			labels[id] = fmt.Sprintf("%s (%s)", id, assignment.title)
		}
	}
	var pairs []conflict.PairConflict
	for _, pair := range conflict.Detect(scopes) {
		if plannedIDs[pair.TodoA] || plannedIDs[pair.TodoB] {
			pairs = append(pairs, pair)
		}
	}
	return formatPlannedWriteScopeConflicts(pairs, labels)
}

// recheckWriteConflict reads the two exact execution bindings so a closed task, replacement plan, or changed workspace
// cannot leave a stale advisory in the outbox. The saved declarations also let a restarted manager recheck a queued wake.
func (s *Service) recheckWriteConflict(ctx context.Context, g superviseSignal) (conflict.PairConflict, Todo, error) {
	wake := g.WriteConflict
	if wake == nil || g.Key != wake.key() {
		return conflict.PairConflict{}, Todo{}, nil
	}
	peer, ok := s.todoNow(wake.PeerTodo)
	if !ok || peer.DoneAt != nil || peer.Cancelled() {
		return conflict.PairConflict{}, Todo{}, nil
	}
	own, err := s.loadPlan(ctx, wake.PlanID)
	if errors.Is(err, errPlanNotFound) {
		return conflict.PairConflict{}, Todo{}, nil
	}
	if err != nil {
		return conflict.PairConflict{}, Todo{}, err
	}
	other := own
	if wake.PlanID != wake.PeerPlanID {
		other, err = s.loadPlan(ctx, wake.PeerPlanID)
		if errors.Is(err, errPlanNotFound) {
			return conflict.PairConflict{}, Todo{}, nil
		}
		if err != nil {
			return conflict.PairConflict{}, Todo{}, err
		}
	}
	for _, pair := range conflict.Detect([]conflict.TaskScope{scopeOfDispatchedTask(own, g.Todo), scopeOfDispatchedTask(other, peer.ID)}) {
		if pair.Action == conflict.ActionSerialize {
			return pair, peer, nil
		}
	}
	return conflict.PairConflict{}, Todo{}, nil
}

func scopeOfDispatchedTask(p *TaskPlan, todoID string) conflict.TaskScope {
	if p.State == planStarted {
		for _, task := range p.Tasks {
			if task.TodoID == todoID && task.Error == "" {
				return declaredTaskScope(task, todoID)
			}
		}
	}
	return conflict.TaskScope{}
}

func plannedTaskRepoKey(t PlanTask) string {
	if strings.TrimSpace(t.Repo) != "" {
		if key := normalizeConflictRepoPathKey(t.Machine, t.Repo); key != "" {
			return key
		}
	}
	if t.WorkspaceResolved != nil && strings.TrimSpace(t.WorkspaceResolved.Path) != "" {
		machine := t.WorkspaceResolved.Machine
		if strings.TrimSpace(machine) == "" {
			machine = t.Machine
		}
		if key := normalizeConflictRepoPathKey(machine, t.WorkspaceResolved.Path); key != "" {
			return key
		}
	}
	if dir := strings.TrimSpace(taskRunDir(t)); dir != "" {
		if key := normalizeConflictRepoPathKey(t.Machine, dir); key != "" {
			return key
		}
	}
	return ""
}

func normalizeConflictRepoPathKey(machine, dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	cleaned := proto.NodeClean(dir)
	if cleaned == "" || cleaned == "." {
		return ""
	}
	machine = strings.ToLower(strings.TrimSpace(machine))
	if machine == "" {
		return cleaned
	}
	return machine + ":" + cleaned
}

func formatPlannedWriteScopeConflicts(conflicts []conflict.PairConflict, labels map[string]string) string {
	if len(conflicts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nWrite-scope conflicts (same repository):")
	for _, c := range conflicts {
		labelA := c.TodoA
		if l := labels[c.TodoA]; l != "" {
			labelA = l
		}
		labelB := c.TodoB
		if l := labels[c.TodoB]; l != "" {
			labelB = l
		}
		var details []string
		if len(c.StaticOverlaps) > 0 {
			details = append(details, "static overlaps: "+strings.Join(c.StaticOverlaps, ", "))
		}
		if len(c.RuntimeFiles) > 0 {
			details = append(details, "runtime files: "+strings.Join(c.RuntimeFiles, ", "))
		}
		var guidance string
		switch c.Action {
		case conflict.ActionWarnOverridden:
			guidance = "static overlap explicitly allowed via force_parallel"
		default:
			if c.Layer == conflict.LayerRuntime || c.Layer == conflict.LayerBoth {
				guidance = "runtime file intersection detected; downgrade to serial execution/merge (force_parallel does not override runtime intersections)"
			} else {
				guidance = "recommend running serially or setting force_parallel=true if write scopes are actually disjoint"
			}
		}
		fmt.Fprintf(&b, "\n- %s <-> %s [repo=%s, layer=%s, action=%s] (%s): %s",
			labelA, labelB, c.RepoKey, c.Layer, c.Action, strings.Join(details, "; "), guidance)
	}
	return b.String()
}
