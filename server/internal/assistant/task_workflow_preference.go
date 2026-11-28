package assistant

import (
	"context"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/proto"
)

// anySharedBranchRepository reports whether this assistant has at least one saved repository configured for
// shared_branch workflow. When false (fresh installs and untouched settings), task_plan default workspace resolution
// skips repository/git-origin lookups completely and preserves existing behavior with zero extra RPCs.
func (s *Service) anySharedBranchRepository(ctx context.Context) (repositoryDocument, bool) {
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return repositoryDocument{}, false
	}
	for _, b := range doc.Repositories {
		if b.TaskWorkflow == repository.TaskWorkflowSharedBranch {
			return doc, true
		}
	}
	return repositoryDocument{}, false
}

func (s *Service) bindingForTaskRepo(doc repositoryDocument, row repository.TaskRepository) (RepositoryBinding, bool) {
	for _, b := range doc.Repositories {
		if b.ID == row.RepoID {
			return b, true
		}
	}
	for _, b := range doc.Repositories {
		if s.sameRepository(b.Identity, row.SourceSnapshot.Identity) {
			return b, true
		}
	}
	return RepositoryBinding{}, false
}

func canonicalBindingOrigin(instanceURL, repoPath string) string {
	u, err := url.Parse(strings.TrimSpace(instanceURL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	}
	cleanPath := strings.TrimSuffix(strings.Trim(strings.TrimSpace(repoPath), "/"), ".git")
	if cleanPath == "" {
		return ""
	}
	return strings.ToLower(host + "/" + cleanPath)
}

func bindingByGitOrigin(doc repositoryDocument, origin string) (RepositoryBinding, bool) {
	origin = strings.ToLower(strings.TrimSpace(origin))
	if origin == "" || strings.HasPrefix(origin, "file:") || strings.Contains(origin, "@") {
		return RepositoryBinding{}, false
	}
	for _, b := range doc.Repositories {
		if canonicalBindingOrigin(b.InstanceURL, b.Path) == origin {
			return b, true
		}
	}
	return RepositoryBinding{}, false
}

// automaticWorkspaceRepository reports whether a saved row can supply a workspace without an explicit repo selection.
func automaticWorkspaceRepository(row repository.TaskRepository) bool {
	if row.CatalogState == "unlisted" || strings.HasPrefix(row.RepoID, "linked_") {
		return false
	}
	if row.BindingOrigin != repository.RepositoryBindingUnknown {
		return row.BindingOrigin == repository.RepositoryBindingExplicit
	}
	// Older catalog-matched imports have no origin marker. A default row containing only reference links is treated
	// as context. A configured ref or alias identifies workspace intent, and explicit binding records it on save.
	return row.DirectoryAlias != "" || row.RequestedRef.Kind != "default" || len(row.Links) == 0 ||
		slices.ContainsFunc(row.Links, func(link repository.TaskLink) bool { return link.Role != repository.LinkRoleReference })
}

// inferredSharedBranchContainerRepos returns the bound repository IDs when a writable task omits workspace policy,
// targets a multi-repository to-do (2+ bound repos), and the bound repositories opt into shared_branch without an
// explicit isolated override. Cooperates with #13143 container workspace dispatch.
func (s *Service) inferredSharedBranchContainerRepos(ctx context.Context, t PlanTask) []string {
	if t.Readonly {
		return nil
	}
	doc, ok := s.anySharedBranchRepository(ctx)
	if !ok {
		return nil
	}
	st := s.store()
	if _, ok := st.(todoRepositoryStore); !ok {
		return nil
	}
	if _, ok := st.(workspaceNamingStore); !ok {
		return nil
	}
	if _, ok := st.(taskWorkspaceStore); !ok {
		return nil
	}
	todoID := firstNonEmpty(t.TodoID, t.ReuseTodo)
	if todoID == "" && t.Cwd == "" && t.PartOf != "" {
		todoID = t.PartOf
	}
	if todoID == "" {
		return nil
	}
	set, err := s.savedTodoRepositories(ctx, todoID)
	if err != nil {
		return nil
	}
	hasShared := false
	ids := make([]string, 0, len(set.Repositories))
	for _, row := range set.Repositories {
		if !automaticWorkspaceRepository(row) {
			continue
		}
		ids = append(ids, row.RepoID)
		if b, found := s.bindingForTaskRepo(doc, row); found {
			switch b.TaskWorkflow {
			case repository.TaskWorkflowSharedBranch:
				hasShared = true
			case repository.TaskWorkflowIsolated:
				return nil
			}
		}
	}
	if !hasShared || len(ids) < 2 {
		return nil
	}
	return ids
}

func (s *Service) canInferContainerWorkspace(ctx context.Context, t PlanTask) bool {
	return t.Workspace == nil && !t.Readonly && len(s.inferredSharedBranchContainerRepos(ctx, t)) >= 2
}

// resolvedTaskWorkflow resolves the optional per-repository task_workflow preference for a single-repo task whose
// workspace policy was omitted. Returns "" (or "isolated") when no shared_branch repository matches.
func (s *Service) resolvedTaskWorkflow(ctx context.Context, t PlanTask) string {
	doc, ok := s.anySharedBranchRepository(ctx)
	if !ok {
		return ""
	}
	if todoID := firstNonEmpty(t.TodoID, t.ReuseTodo); todoID != "" {
		if _, hasStore := s.store().(todoRepositoryStore); hasStore {
			if b, found := s.singleTaskRepoBinding(ctx, doc, todoID); found {
				return b.TaskWorkflow
			}
		}
	}
	if t.Cwd != "" {
		var id proto.RepositoryIdentity
		if err := s.call(ctx, t.Machine, "git.status", map[string]any{"cwd": t.Cwd, "identity_only": true}, &id); err == nil && id.IsRepo && id.Origin != "" {
			if b, found := bindingByGitOrigin(doc, id.Origin); found {
				return b.TaskWorkflow
			}
		}
	}
	if t.PartOf != "" {
		if _, hasStore := s.store().(todoRepositoryStore); hasStore {
			if b, found := s.singleTaskRepoBinding(ctx, doc, t.PartOf); found {
				return b.TaskWorkflow
			}
		}
	}
	return ""
}

func (s *Service) singleTaskRepoBinding(ctx context.Context, doc repositoryDocument, todoID string) (RepositoryBinding, bool) {
	set, err := s.savedTodoRepositories(ctx, todoID)
	if err != nil {
		return RepositoryBinding{}, false
	}
	var chosen *repository.TaskRepository
	for i := range set.Repositories {
		row := &set.Repositories[i]
		if !automaticWorkspaceRepository(*row) {
			continue
		}
		if chosen != nil {
			return RepositoryBinding{}, false
		}
		chosen = row
	}
	if chosen == nil {
		return RepositoryBinding{}, false
	}
	return s.bindingForTaskRepo(doc, *chosen)
}
