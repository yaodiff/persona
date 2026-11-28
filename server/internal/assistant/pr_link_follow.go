package assistant

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Delivery PRs that exist only as repository links (#14049).
//
// The PR status sweep reads a task's stage PRs: the one its Agent's replies named last and the ones before it. A task
// whose delivery PR was linked some other way (todo_repository_links_add, a reply that only named a branch, several
// PRs of which the stage kept none) had no stage PR, so nothing ever read it: not the merge wake, not a park's wake,
// not the Waiting for merge state. followLinkedDeliveryPRs adds such links to the stage's earlier PRs, where the
// sweep polls them like any other within its usual cap (prsPolled). Reference links stay out (todoDeliveryPRs).

// followLinkedDeliveryPRs adds the delivery PRs in each open supervised task's repository links that its stage does not
// follow yet to the stage's earlier PRs. Each task's links are read at most once per deployRecheck.
func (s *Service) followLinkedDeliveryPRs(ctx context.Context, now time.Time) {
	if _, ok := s.store().(todoRepositoryStore); !ok {
		return
	}
	s.todos.mu.RLock()
	var tasks []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && t.Supervision != nil && !isAskTodo(t.ID) {
			tasks = append(tasks, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		if t.Cancelled() || !s.linkReadDue(t.ID, now) {
			continue
		}
		delivery, err := s.todoDeliveryPRs(ctx, t)
		if err != nil {
			continue // read again after deployRecheck
		}
		followed := map[string]bool{normalizePRKey(t.PRURL): true}
		for _, u := range t.PRs {
			followed[normalizePRKey(u)] = true
		}
		var add []string
		for _, u := range delivery {
			if k := normalizePRKey(u); k != "" && !followed[k] && (ciPRURL.MatchString(u) || otherReviewLink(u)) {
				followed[k] = true
				add = append(add, u)
			}
		}
		if len(add) > 0 {
			s.followStagePRs(ctx, t.ID, add)
		}
	}
}

// linkReadDue says whether task id's repository links are due to be read again at now, and records the read if so.
func (s *Service) linkReadDue(id string, now time.Time) bool {
	key := id + "|links"
	p := &s.deploy
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.seen[key]; ok && now.Sub(last.statusAt) < deployRecheck {
		return false
	}
	if p.seen == nil || len(p.seen) > 2000 {
		p.seen = map[string]prSeen{}
	}
	p.seen[key] = prSeen{statusAt: now}
	return true
}

// followStagePRs adds urls to task id's stage as earlier PRs (oldest first, the current PR unchanged), so the PR
// status sweep reads them.
func (s *Service) followStagePRs(ctx context.Context, id string, urls []string) {
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		return
	}
	cur := b.stages[id]
	prs := slices.Clone(cur.PRs)
	for _, u := range urls {
		k := normalizePRKey(u)
		if k == normalizePRKey(cur.PR) || slices.ContainsFunc(prs, func(p string) bool { return normalizePRKey(p) == k }) {
			continue
		}
		prs = keptPRs(prs, u)
	}
	if slices.Equal(prs, cur.PRs) {
		b.mu.Unlock()
		return
	}
	cur.PRs = prs
	if st := s.store(); st != nil {
		stages := make(map[string]todoStage, len(b.stages))
		for k, v := range b.stages {
			stages[k] = v
		}
		stages[id] = cur
		if err := st.PutSetting(ctx, reportStagesKey, stages); err != nil {
			b.mu.Unlock()
			s.logf("task %s: follow linked delivery PRs: %v", id, err)
			return
		}
	}
	b.stages[id] = cur
	b.mu.Unlock()
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.PRs, t.OtherPRs = cur.PRs, cur.otherPRFacts()
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
}

// closedPRSuccessors names the other PRs in task t's repository links, whatever their role, when its PR closed
// unmerged: one of them may continue it (a maintainer's follow-up PR), which the supervisor tells apart from abandoned
// work. Merged ones come first, marked so. "" when there are none or the links cannot be read.
func (s *Service) closedPRSuccessors(ctx context.Context, t Todo, closed string) string {
	if _, ok := s.store().(todoRepositoryStore); !ok {
		return ""
	}
	set, err := s.savedTodoRepositories(ctx, t.ID)
	if err != nil {
		return ""
	}
	var merged, other []string
	seen := map[string]bool{normalizePRKey(closed): true}
	for _, row := range set.Repositories {
		for _, l := range row.Links {
			u := strings.TrimSpace(l.CanonicalURL)
			k := normalizePRKey(u)
			if l.Kind != "pull_request" || k == "" || seen[k] {
				continue
			}
			seen[k] = true
			if f, ok := todoObservedPRState(t, u); ok && strings.EqualFold(f.State, "merged") {
				merged = append(merged, u+" (merged)")
			} else {
				other = append(other, u)
			}
		}
	}
	if len(merged)+len(other) == 0 {
		return ""
	}
	return "Linked PRs that may continue it: " + strings.Join(append(merged, other...), ", ") + "."
}

// mergedDeliveryPR is a delivery PR of task id observed merged, "" when none is.
func (s *Service) mergedDeliveryPR(ctx context.Context, id string) string {
	t, ok := s.todoNow(id)
	if !ok {
		return ""
	}
	delivery, err := s.todoDeliveryPRs(ctx, t)
	if err != nil {
		return ""
	}
	for _, u := range delivery {
		if f, ok := todoObservedPRState(t, u); ok && strings.EqualFold(f.State, "merged") {
			return u
		}
	}
	return ""
}
