package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

type todoRepositoryStore interface {
	TodoRepositories(context.Context, string) (repository.TaskSet, error)
	CreateTodoWithRepositories(context.Context, db.TodoRow, repository.CreateBindings) error
	SetTodoRepositories(context.Context, string, repository.SetInput) (repository.TaskSet, error)
	SetTodoRepositoryLinks(context.Context, string, string, repository.LinksInput) (repository.TaskSet, error)
}

var _ todoRepositoryStore = (*repositorystore.Store)(nil)

type todoPRLinkTitleStore interface {
	SetTodoPRLinkTitle(ctx context.Context, id, canonicalURL, title string) error
}

var _ todoPRLinkTitleStore = (*repositorystore.Store)(nil)

func decodeTaskRepositoryInput(raw []byte, out any) error {
	if len(raw) > 256<<10 {
		return repository.Problem("limit")
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return repository.Problem("invalid")
	}
	var nonnull func(any) bool
	nonnull = func(v any) bool {
		switch v := v.(type) {
		case nil:
			return false
		case map[string]any:
			for _, f := range v {
				if !nonnull(f) {
					return false
				}
			}
		case []any:
			for _, f := range v {
				if !nonnull(f) {
					return false
				}
			}
		}
		return true
	}
	if _, ok := value.(map[string]any); !ok || !nonnull(value) {
		return repository.Problem("invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return repository.Problem("invalid")
	}
	return nil
}
func parseCreateBindings(raw json.RawMessage) (*repository.CreateBindings, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in repository.CreateBindings
	if err := decodeTaskRepositoryInput(raw, &in); err != nil {
		return nil, err
	}
	if !repository.ValidRevision(in.CatalogRevision) || in.Repositories == nil {
		return nil, repository.Problem("invalid")
	}
	return &in, nil
}

func taskRepositoriesError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "store_unavailable"
	var problem repository.Problem
	var provider *repository.ProviderError
	if errors.Is(err, db.ErrTodoNotFound) {
		status, code = 404, "not_found"
	} else if errors.As(err, &provider) {
		code = providerCode(provider)
		apierr.WriteCode(w, http.StatusConflict, "task_repositories_"+code, i18n.Err("task-repositories."+code, providerArgs(provider)))
		return
	} else if errors.As(err, &problem) {
		code = string(problem)
		switch code {
		case "invalid":
			status = 400
		case "readonly":
			status = 403
		case "not_found":
			status = 404
		case "store_unavailable":
			status = 503
		default:
			status = 409
		}
	}
	apierr.WriteCode(w, status, "task_repositories_"+code, i18n.Err("task-repositories."+code))
}
func (s *Service) readTodoRepositories(ctx context.Context, id string) (repository.TaskSet, error) {
	_, ok := s.store().(todoRepositoryStore)
	if !ok {
		return repository.TaskSet{}, repository.Problem("store_unavailable")
	}
	if err := s.importLegacyTodoRepositoryLinks(ctx, id); err != nil {
		return repository.TaskSet{}, err
	}
	return s.savedTodoRepositories(ctx, id)
}

// Meeting reads must not migrate metadata or acknowledge historical reports.
// Sessionless Agent calls conservatively use the same machine-wide meeting check.
func (s *Service) readTodoRepositoriesForTool(ctx context.Context, machine, session, id string) (repository.TaskSet, error) {
	running, err := s.meetingTurnRunning(ctx, machine, session)
	if session == "" {
		running, err = s.meetingTurnAgentRunning(ctx, machine)
	}
	if err != nil {
		return repository.TaskSet{}, err
	}
	if running || s.isRoom(session) {
		return s.savedTodoRepositories(ctx, id)
	}
	return s.readTodoRepositories(ctx, id)
}

func (s *Service) savedTodoRepositories(ctx context.Context, id string) (repository.TaskSet, error) {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return repository.TaskSet{}, repository.Problem("store_unavailable")
	}
	// Import may have committed a new revision. The first response must describe that
	// committed state, just like later reads, rather than a pre-import snapshot.
	set, err := st.TodoRepositories(ctx, id)
	if err != nil {
		return set, err
	}
	return s.describeTodoRepositories(ctx, set)
}
func (s *Service) describeTodoRepositories(ctx context.Context, set repository.TaskSet) (repository.TaskSet, error) {
	set.Manager = s.managerIdentity()
	if len(set.Repositories) == 0 {
		return set, nil
	}
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return set, err
	}
	catalog := doc.taskCatalog()
	for i, row := range set.Repositories {
		set.Repositories[i].CatalogState = repository.CatalogState(row, catalog)
		s.describeProviderRow(&set.Repositories[i])
	}
	return set, nil
}
func (s *Service) setTodoRepositories(ctx context.Context, id string, in repository.SetInput) (repository.TaskSet, error) {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return repository.TaskSet{}, repository.Problem("store_unavailable")
	}
	if err := s.importLegacyTodoRepositoryLinks(ctx, id); err != nil {
		return repository.TaskSet{}, err
	}
	if err := s.resolveSetInput(ctx, id, in); err != nil {
		return repository.TaskSet{}, err
	}
	set, err := st.SetTodoRepositories(ctx, id, in)
	if err != nil {
		return set, err
	}
	// Associations carry no task progress and need no in-memory Todo projection.
	s.todosChanged(ctx)
	return s.describeTodoRepositories(ctx, set)
}
func (s *Service) setTodoRepositoryLinks(ctx context.Context, id, repoID string, in repository.LinksInput) (repository.TaskSet, error) {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return repository.TaskSet{}, repository.Problem("store_unavailable")
	}
	if err := s.importLegacyTodoRepositoryLinks(ctx, id); err != nil {
		return repository.TaskSet{}, err
	}
	if err := s.resolveLinksInput(ctx, id, repoID, in); err != nil {
		return repository.TaskSet{}, err
	}
	set, err := st.SetTodoRepositoryLinks(ctx, id, repoID, in)
	if err != nil {
		return set, err
	}
	s.todosChanged(ctx)
	return s.describeTodoRepositories(ctx, set)
}
func taskRepositoryBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if !todoWriter(r) {
		taskRepositoriesError(w, repository.Problem("readonly"))
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		taskRepositoriesError(w, repository.Problem("invalid"))
		return false
	}
	if err = decodeTaskRepositoryInput(raw, out); err != nil {
		taskRepositoriesError(w, err)
		return false
	}
	return true
}
func (s *Service) todoRepositoryRoutes() {
	s.handle("GET /api/v1/todos/{id}/repositories", func(w http.ResponseWriter, r *http.Request) {
		set, err := s.readTodoRepositories(r.Context(), r.PathValue("id"))
		if err != nil {
			taskRepositoriesError(w, err)
			return
		}
		apierr.WriteJSON(w, set)
	})
	s.handle("PUT /api/v1/todos/{id}/repositories", func(w http.ResponseWriter, r *http.Request) {
		var in repository.SetInput
		if !taskRepositoryBody(w, r, &in) {
			return
		}
		set, err := s.setTodoRepositories(r.Context(), r.PathValue("id"), in)
		if err != nil {
			taskRepositoriesError(w, err)
			return
		}
		apierr.WriteJSON(w, set)
	})
	s.handle("PUT /api/v1/todos/{id}/repositories/{repo_id}/links", func(w http.ResponseWriter, r *http.Request) {
		var in repository.LinksInput
		if !taskRepositoryBody(w, r, &in) {
			return
		}
		set, err := s.setTodoRepositoryLinks(r.Context(), r.PathValue("id"), r.PathValue("repo_id"), in)
		if err != nil {
			taskRepositoriesError(w, err)
			return
		}
		apierr.WriteJSON(w, set)
	})
}

func (s *Service) toolTodoRepositories(ctx context.Context, machine, self, name string, args json.RawMessage) (string, error) {
	if name == "todo_repositories_get" {
		var in struct {
			ID string `json:"id"`
		}
		if err := decodeTaskRepositoryInput(args, &in); err != nil {
			return "", err
		}
		set, err := s.readTodoRepositoriesForTool(ctx, machine, self, in.ID)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(set)
		return string(raw), err
	}
	var in struct {
		ID                      string                 `json:"id"`
		RepoID                  string                 `json:"repo_id,omitempty"`
		ExpectedRevision        *int64                 `json:"expected_revision"`
		ExpectedCatalogRevision *int64                 `json:"expected_catalog_revision,omitempty"`
		Repositories            []repository.TaskInput `json:"repositories,omitempty"`
		Links                   []repository.LinkInput `json:"links,omitempty"`
	}
	if err := decodeTaskRepositoryInput(args, &in); err != nil {
		return "", err
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	todo, ok := s.todoNow(strings.TrimSpace(in.ID))
	if !ok {
		return "", db.ErrTodoNotFound
	}
	if ok, outcome, err := s.confirmOutbound(ctx, machine, self, name, "", todo.Title, args); err != nil || !ok {
		return todoNotAllowed(outcome, err)
	}
	var set repository.TaskSet
	var err error
	if name == "todo_repositories_set" {
		if in.RepoID != "" || in.Links != nil {
			return "", repository.Problem("invalid")
		}
		set, err = s.setTodoRepositories(ctx, todo.ID, repository.SetInput{ExpectedRevision: in.ExpectedRevision, ExpectedCatalogRevision: in.ExpectedCatalogRevision, Repositories: in.Repositories})
	} else {
		if in.Repositories != nil || in.ExpectedCatalogRevision != nil || in.RepoID == "" {
			return "", repository.Problem("invalid")
		}
		set, err = s.setTodoRepositoryLinks(ctx, todo.ID, in.RepoID, repository.LinksInput{ExpectedRevision: in.ExpectedRevision, Links: in.Links})
	}
	if err != nil {
		return "", s.taskRepositoryToolError(ctx, err)
	}
	raw, err := json.Marshal(set)
	return string(raw), err
}
