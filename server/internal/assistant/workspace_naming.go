package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type workspaceNamingStore interface {
	TodoWorkspaceRules(context.Context, string) (repositorystore.WorkspaceRulesRecord, error)
	SetTodoWorkspaceRules(context.Context, string, int64, json.RawMessage) (repositorystore.WorkspaceRulesRecord, error)
	WorkspaceNamingPlan(context.Context, string, string) (*workspacenaming.Plan, error)
	WorkspaceNamingPlanForSave(context.Context, string, string) (*workspacenaming.Plan, error)
	SaveWorkspaceNamingPlan(context.Context, string, string, int64, workspacenaming.Plan) (*workspacenaming.Plan, error)
}

var _ workspaceNamingStore = (*repositorystore.Store)(nil)

type namingRequest struct {
	MachineID       string            `json:"machine_id"`
	Layout          repository.Layout `json:"layout"`
	SelectedRepoIDs []string          `json:"selected_repo_ids"`
	WorkspaceRules  json.RawMessage   `json:"workspace_rules,omitempty"`
	OperationID     string            `json:"operation_id,omitempty"`
}

func namingError(w http.ResponseWriter, err error) {
	var p workspacenaming.Problem
	if errors.As(err, &p) {
		status, key := 400, "invalid"
		if p.Code == "node_unavailable" {
			status, key = 503, "node_unavailable"
		}
		body := apierr.LocalizedBody(status, "workspace_naming_"+p.Code, i18n.Err("workspace-naming."+key), i18n.OfWriter(w))
		body["field"] = p.Field
		apierr.WriteBody(w, status, body)
		return
	}
	taskRepositoriesError(w, err)
}
func namingBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if !todoWriter(r) {
		namingError(w, repository.Problem("readonly"))
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		namingError(w, repository.Problem("invalid"))
		return false
	}
	// Rule patches allow null resets. The enclosing request still rejects unknown fields.
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		namingError(w, repository.Problem("invalid"))
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		namingError(w, repository.Problem("invalid"))
		return false
	}
	return true
}

func (s *Service) namingPreview(ctx context.Context, id string, in namingRequest, sample bool) (workspacenaming.Preview, int64, error) {
	var empty workspacenaming.Preview
	if in.MachineID == "" || in.Layout != repository.LayoutRoot && in.Layout != repository.LayoutContainer {
		return empty, 0, repository.Problem("invalid")
	}
	if _, ok := s.device(ctx, in.MachineID); !ok {
		return empty, 0, repository.Problem("not_found")
	}
	settings, ok := s.savedSettings(ctx)
	if !ok {
		return empty, 0, repository.Problem("store_unavailable")
	}
	source := workspacenaming.Source{Manager: s.managerIdentity(), TodoID: id, MachineID: in.MachineID, Timezone: settings.Timezone, PlannedAt: time.Now().UTC(), Layout: in.Layout}
	var rules workspacenaming.Rules
	var revision int64
	if sample {
		source.TodoID = "todo_example"
		source.Title = "Example task"
		rows := []repository.TaskRepository{}
		for _, name := range []string{"web", "api"} {
			identity, _ := repository.Parse("https://github.com/example/"+name, "")
			rows = append(rows, repository.TaskRepository{RepoID: name, RequestedRef: repository.RequestedRef{Kind: "default"}, SourceSnapshot: repository.SourceSnapshot{Identity: identity}})
			if in.Layout == repository.LayoutRoot {
				break
			}
		}
		ids := []string{}
		for _, row := range rows {
			ids = append(ids, row.RepoID)
		}
		source.Repositories, _ = repository.Snapshot(source.Manager, source.TodoID, 0, rows, ids)
	} else {
		if err := s.ensureTodosLoaded(ctx); err != nil {
			return empty, 0, err
		}
		todo, ok := s.todoNow(id)
		if !ok {
			return empty, 0, db.ErrTodoNotFound
		}
		source.Title = todo.Title
		st, ok := s.store().(workspaceNamingStore)
		if !ok {
			return empty, 0, repository.Problem("store_unavailable")
		}
		record, err := st.TodoWorkspaceRules(ctx, id)
		if err != nil {
			return empty, 0, err
		}
		rules = record.Rules
		revision = record.Revision
		set, err := s.readTodoRepositories(ctx, id)
		if err != nil {
			return empty, 0, err
		}
		if in.SelectedRepoIDs == nil {
			return empty, 0, repository.Problem("invalid")
		}
		source.Repositories, err = repository.Snapshot(source.Manager, id, set.Revision, set.Repositories, in.SelectedRepoIDs)
		if err != nil {
			return empty, 0, err
		}
	}
	if len(in.WorkspaceRules) > 0 {
		var err error
		rules, err = workspacenaming.Patch(rules, in.WorkspaceRules)
		if err != nil {
			return empty, 0, err
		}
	}
	source.Rules, source.RuleSources = workspacenaming.Effective(settings.WorkspaceRules, rules)
	// A sample edits the assistant layer, not task inheritance.
	if sample {
		assistant, err := workspacenaming.Patch(settings.WorkspaceRules, in.WorkspaceRules)
		if err != nil {
			return empty, 0, err
		}
		source.Rules, source.RuleSources = workspacenaming.Effective(assistant, workspacenaming.Rules{})
	}
	var preview workspacenaming.Preview
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.call(callCtx, in.MachineID, "workspace.naming.resolve", source, &preview); err != nil {
		return empty, 0, workspacenaming.Problem{Code: "node_unavailable", Field: "machine_id", Severity: "error"}
	}
	if preview.Plan != nil {
		if err := validateWorkspaceNamingPlan(*preview.Plan, source); err != nil {
			return empty, 0, err
		}
	}
	preview.Describe(source)
	preview.Sample = sample
	return preview, revision, nil
}

func validateWorkspaceNamingPlan(plan workspacenaming.Plan, source workspacenaming.Source) error {
	if err := workspacenaming.CheckPlan(plan); err != nil {
		return err
	}
	if source.Timezone == "" {
		source.Timezone = "UTC"
	}
	want, _ := json.Marshal(source)
	got, _ := json.Marshal(plan.Source)
	if !bytes.Equal(want, got) {
		return repository.Problem("invalid")
	}
	return nil
}

func (s *Service) workspaceNamingRoutes() {
	s.handle("GET /api/v1/todos/{id}/workspace-rules", func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.store().(workspaceNamingStore)
		if !ok {
			namingError(w, repository.Problem("store_unavailable"))
			return
		}
		record, err := st.TodoWorkspaceRules(r.Context(), r.PathValue("id"))
		if err != nil {
			namingError(w, err)
			return
		}
		apierr.WriteJSON(w, record)
	})
	s.handle("PUT /api/v1/todos/{id}/workspace-rules", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExpectedRevision *int64          `json:"expected_revision"`
			Rules            json.RawMessage `json:"workspace_rules"`
		}
		if !namingBody(w, r, &in) {
			return
		}
		if !repository.ValidRevision(in.ExpectedRevision) || len(in.Rules) == 0 {
			namingError(w, repository.Problem("invalid"))
			return
		}
		st, ok := s.store().(workspaceNamingStore)
		if !ok {
			namingError(w, repository.Problem("store_unavailable"))
			return
		}
		record, err := st.SetTodoWorkspaceRules(r.Context(), r.PathValue("id"), *in.ExpectedRevision, in.Rules)
		if err != nil {
			namingError(w, err)
			return
		}
		apierr.WriteJSON(w, record)
	})
	for _, sample := range []bool{false, true} {
		pattern := "POST /api/v1/todos/{id}/workspace-naming/preview"
		if sample {
			pattern = "POST /api/v1/assistant/workspace-naming/preview"
		}
		s.handle(pattern, func(w http.ResponseWriter, r *http.Request) {
			var in namingRequest
			if !namingBody(w, r, &in) {
				return
			}
			preview, _, err := s.namingPreview(r.Context(), r.PathValue("id"), in, sample)
			if err != nil {
				namingError(w, err)
				return
			}
			apierr.WriteJSON(w, preview)
		})
	}
	s.handle("GET /api/v1/todos/{id}/workspace-naming/plans/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.store().(workspaceNamingStore)
		if !ok {
			namingError(w, repository.Problem("store_unavailable"))
			return
		}
		p, err := st.WorkspaceNamingPlan(r.Context(), r.PathValue("id"), r.PathValue("operation_id"))
		if err != nil {
			namingError(w, err)
			return
		}
		if p == nil {
			namingError(w, db.ErrTodoNotFound)
			return
		}
		apierr.WriteJSON(w, p)
	})
	s.handle("POST /api/v1/todos/{id}/workspace-naming/plans", func(w http.ResponseWriter, r *http.Request) {
		var in namingRequest
		if !namingBody(w, r, &in) {
			return
		}
		if !workspacenaming.ValidOperationID(in.OperationID) || len(in.WorkspaceRules) > 0 || in.SelectedRepoIDs == nil {
			namingError(w, repository.Problem("invalid"))
			return
		}
		st, ok := s.store().(workspaceNamingStore)
		if !ok {
			namingError(w, repository.Problem("store_unavailable"))
			return
		}
		id := r.PathValue("id")
		prior, err := st.WorkspaceNamingPlanForSave(r.Context(), id, in.OperationID)
		if err != nil {
			namingError(w, err)
			return
		}
		if prior != nil {
			ids := []string{}
			for _, repo := range prior.Source.Repositories.Repositories {
				ids = append(ids, repo.RepoID)
			}
			selected := slices.Clone(in.SelectedRepoIDs)
			slices.Sort(selected)
			slices.Sort(ids)
			if prior.Source.MachineID != in.MachineID || prior.Source.Layout != in.Layout || !slices.Equal(ids, selected) {
				namingError(w, repository.Problem("revision_conflict"))
				return
			}
			apierr.WriteJSON(w, prior)
			return
		}
		preview, revision, err := s.namingPreview(r.Context(), id, in, false)
		if err != nil {
			namingError(w, err)
			return
		}
		if preview.Plan == nil {
			apierr.WriteJSON(w, preview)
			return
		}
		plan, err := st.SaveWorkspaceNamingPlan(r.Context(), id, in.OperationID, revision, *preview.Plan)
		if err != nil {
			namingError(w, err)
			return
		}
		apierr.WriteJSON(w, plan)
	})
}
