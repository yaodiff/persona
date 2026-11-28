package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var (
	unsafePlanRepositoryURL = errors.New("unsafe repository issue/PR URL: remove credentials or invalid query parameters; nothing was dispatched")
	// A malformed authority, path or fragment may keep net/url from exposing the link's route.
	planRepositoryURLShape = lazyre.New(`(?i)^https?://[^/]+/(?:[^/]+/[^/]+/(?:issues|pull)|(?:[^/]+/){2,}(?:-/)?merge_requests|(?:[^/]+/){2,}-/issues)/[1-9][0-9]{0,19}/?$`)
)

// TaskDistinctFrom explains why a new task is separate from an existing to-do
// whose repository links it references. The declaration stays in its brief.
type TaskDistinctFrom struct {
	TodoID string `json:"todo_id"`
	Reason string `json:"reason"`
}

func planRepositoryReferences(t PlanTask) (map[string]bool, error) {
	refs := map[string]bool{}
	for _, raw := range inboxTaskURL.FindAllString(t.Title+"\n"+t.Goal+"\n"+t.Prompt, -1) {
		raw = strings.TrimRight(raw, ".,;:!?)]}，。；：！）》」』") // i18n-ignore: URL punctuation, not displayed text
		u, err := url.Parse(raw)
		if err != nil {
			address := raw
			if i := strings.IndexAny(raw, "?#"); i >= 0 {
				address = raw[:i]
			}
			if planRepositoryURLShape.MatchString(address) {
				return nil, unsafePlanRepositoryURL
			}
			continue
		}
		// Identify the link without credentials, then validate the original before admitting it.
		u.User = nil
		u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = "", "", "", false
		u.Path = strings.TrimRight(u.Path, "/")
		identity, link, err := repository.ParseTaskLink(u.String())
		if err != nil {
			continue
		}
		if _, err := repository.Parse(raw, identity.Provider); err != nil {
			return nil, unsafePlanRepositoryURL
		}
		refs[link.CanonicalURL] = true
	}
	return refs, nil
}

// checkPlanRepositoryDuplicates uses saved associations only, never the legacy
// import-on-read path, title similarity, agent activity or bare issue numbers.
func (s *Service) checkPlanRepositoryDuplicates(ctx context.Context, tasks []PlanTask) error {
	type linkedTodo struct {
		todo Todo
		refs []string
	}
	var linked []linkedTodo
	loaded := false
	for i := range tasks {
		t := &tasks[i]
		refs, err := planRepositoryReferences(*t)
		if err != nil {
			return fmt.Errorf("task %d: %w", i+1, err)
		}
		if strings.TrimSpace(t.Continues) != "" || t.ReuseTodo != "" {
			if len(t.DistinctFrom) != 0 {
				return fmt.Errorf("task %d: distinct_from is only for new tasks without continues or todo_id", i+1)
			}
			continue
		}
		distinct := map[string]bool{}
		for j := range t.DistinctFrom {
			d := &t.DistinctFrom[j]
			d.TodoID, d.Reason = strings.TrimSpace(d.TodoID), strings.TrimSpace(d.Reason)
			if d.TodoID == "" || d.Reason == "" || distinct[d.TodoID] {
				return fmt.Errorf("task %d: each distinct_from entry needs a unique todo_id and a nonempty reason", i+1)
			}
			distinct[d.TodoID] = true
		}
		if len(refs) == 0 {
			continue
		}
		if !loaded {
			s.todos.mu.RLock()
			var open []Todo
			for _, todo := range s.todos.todos {
				if todo.DoneAt == nil && !todo.Cancelled() {
					open = append(open, copyTodoLocked(todo))
				}
			}
			s.todos.mu.RUnlock()
			slices.SortFunc(open, func(a, b Todo) int { return strings.Compare(a.ID, b.ID) })
			for _, todo := range open {
				st, ok := s.store().(todoRepositoryStore)
				if !ok {
					return fmt.Errorf("task %d: repository links are unavailable; nothing was dispatched", i+1)
				}
				set, err := st.TodoRepositories(ctx, todo.ID)
				if err != nil {
					return fmt.Errorf("task %d: read repository links for to-do %s: %w; nothing was dispatched", i+1, todo.ID, err)
				}
				row := linkedTodo{todo: todo}
				for _, repo := range set.Repositories {
					for _, link := range repo.Links {
						row.refs = append(row.refs, link.CanonicalURL)
					}
				}
				linked = append(linked, row)
			}
			loaded = true
		}
		var conflicts []string
		for _, row := range linked {
			var shared []string
			for _, ref := range row.refs {
				if refs[ref] {
					shared = append(shared, ref)
				}
			}
			if len(shared) == 0 {
				continue
			}
			if distinct[row.todo.ID] {
				delete(distinct, row.todo.ID)
				continue
			}
			conflicts = append(conflicts, fmt.Sprintf("to-do %s (%q): %s", row.todo.ID, row.todo.Title, strings.Join(shared, ", ")))
		}
		if len(conflicts) != 0 {
			return fmt.Errorf("task %d (%s): repository references already belong to open %s. For the same work, use continues "+
				"with continues_why, or todo_id, naming that to-do; do not create another one. For different work, give "+
				"distinct_from: [{\"todo_id\":\"<existing to-do>\",\"reason\":\"<why this is separate work>\"}] for every "+
				"conflicting to-do; the reasons are kept in the new task's brief. Nothing was dispatched",
				i+1, t.Title, strings.Join(conflicts, "; "))
		}
	}
	return nil
}
