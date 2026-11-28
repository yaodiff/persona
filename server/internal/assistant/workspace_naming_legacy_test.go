package assistant

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingLegacyImportBeforeResolution(t *testing.T) {
	for _, edit := range []string{"none", "links", "selected ref"} {
		t.Run(edit, func(t *testing.T) {
			database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(database.Close)
			store := repositorystore.New(database)
			const id = "todo_legacy_naming"
			now := time.Now().UTC()
			if err := database.CreateTodo(t.Context(), db.TodoRow{ID: id, Title: "Historic work", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			catalog := repository.Catalog{Revision: 1}
			for _, name := range []string{"web", "api"} {
				identity, err := repository.Parse("https://github.com/example/"+name, "")
				if err != nil {
					t.Fatal(err)
				}
				catalog.Repositories = append(catalog.Repositories, repository.CatalogEntry{ID: name, Identity: identity})
			}
			if err := database.PutSetting(t.Context(), repository.CatalogKey, catalog); err != nil {
				t.Fatal(err)
			}
			urls := []string{"https://github.com/example/web/pull/7", "https://github.com/example/api/pull/9"}
			const latest = "https://github.com/example/web/pull/8"
			if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{id: {Stage: "review", PR: latest, PRs: urls}}); err != nil {
				t.Fatal(err)
			}
			before, err := store.TodoRepositories(t.Context(), id)
			if err != nil || before.Revision != 0 || len(before.Repositories) != 0 {
				t.Fatal("fixture already imported legacy links", before, err)
			}
			var receipts int
			if err := database.QueryRow(t.Context(), `SELECT count(*) FROM todo_repository_link_receipts WHERE todo_id=$1`, id).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatal("fixture already has an import receipt", receipts, err)
			}

			f := newFakeHub()
			f.sessions["box"] = nil
			var calls atomic.Int32
			entered := make(chan workspacenaming.Source, 1)
			release := make(chan struct{})
			// Always release a blocked synthetic Node before Service cleanup.
			defer close(release)
			f.nodes = map[string]nodeAnswer{"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
				var source workspacenaming.Source
				if err := json.Unmarshal(raw, &source); err != nil {
					return nil, err
				}
				if calls.Add(1) == 1 {
					entered <- source
					select {
					case <-release:
					case <-t.Context().Done():
						return nil, t.Context().Err()
					}
				}
				return workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example"}), nil
			}}
			s := newServiceOn(t, f, database)
			path := "/api/v1/todos/" + id
			const body = `{"machine_id":"box","layout":"root","selected_repo_ids":["web"],"operation_id":"legacy-first-operation"}`
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- placeDo(s, "", "POST", path+"/workspace-naming/plans", body) }()
			var source workspacenaming.Source
			select {
			case source = <-entered:
			case response := <-result:
				t.Fatalf("request failed before Node resolution: %d %s", response.Code, response.Body)
			case <-time.After(5 * time.Second):
				t.Fatal("Node resolution did not start")
			}
			set, err := store.TodoRepositories(t.Context(), id)
			if err != nil || set.Revision != 1 || len(set.Repositories) != 2 {
				t.Fatal("legacy import was not committed before Node resolution", set, err)
			}
			web := slices.IndexFunc(set.Repositories, func(row repository.TaskRepository) bool { return row.RepoID == "web" })
			if web < 0 || len(set.Repositories[web].Links) != 2 {
				t.Fatal("historical PRs missing", set)
			}
			want, err := repository.Snapshot(s.managerIdentity(), id, set.Revision, set.Repositories, []string{"web"})
			if err != nil || !reflect.DeepEqual(source.Repositories, want) {
				t.Fatal("Node received a different repository snapshot", source.Repositories, want, err)
			}
			var imported bool
			var seen []string
			if err := database.QueryRow(t.Context(), `SELECT legacy_imported,seen_urls FROM todo_repository_link_receipts WHERE todo_id=$1`, id).Scan(&imported, &seen); err != nil || !imported || len(seen) != 3 || !slices.Contains(seen, latest) {
				t.Fatal("legacy import receipt missing before Node resolution", imported, seen, err)
			}
			if edit == "links" {
				if w := placeDo(s, "", "PUT", path+"/repositories/web/links", `{"expected_revision":1,"links":[]}`); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			} else if edit == "selected ref" {
				if w := placeDo(s, "", "PUT", path+"/repositories", `{"expected_revision":1,"expected_catalog_revision":1,"repositories":[{"repo_id":"web","requested_ref":{"kind":"branch","value":"updated"}},{"repo_id":"api"}]}`); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			}
			release <- struct{}{}
			var w *httptest.ResponseRecorder
			select {
			case w = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("plan save did not finish after Node resolution")
			}
			if edit == "selected ref" {
				if w.Code != 409 {
					t.Fatal("concurrent source edit was not rejected", w.Code, w.Body)
				}
				if plan, err := store.WorkspaceNamingPlan(t.Context(), id, "legacy-first-operation"); err != nil || plan != nil {
					t.Fatal("conflict persisted a stale plan", plan, err)
				}
				w = placeDo(s, "", "POST", path+"/workspace-naming/plans", body)
			}
			var saved workspacenaming.Plan
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || workspacenaming.CheckPlan(saved) != nil {
				t.Fatal("first save or conflict retry failed", w.Code, w.Body)
			}
			if edit == "selected ref" && saved.Repositories[0].RequestedRef.Value != "updated" {
				t.Fatal("retry did not use the edited source", saved)
			}
			count := calls.Load()
			restarted := newServiceOn(t, f, database)
			w = placeDo(restarted, "", "POST", path+"/workspace-naming/plans", body)
			var retry workspacenaming.Plan
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &retry) != nil || !reflect.DeepEqual(saved, retry) || calls.Load() != count {
				t.Fatal("persisted retry changed or called Node", w.Code, w.Body, calls.Load())
			}
			after, err := restarted.readTodoRepositories(t.Context(), id)
			wantRevision := int64(1)
			if edit != "none" {
				wantRevision = 2
			}
			if err != nil || after.Revision != wantRevision {
				t.Fatal("restart changed repository state", after, err)
			}
			if edit == "links" {
				web = slices.IndexFunc(after.Repositories, func(row repository.TaskRepository) bool { return row.RepoID == "web" })
				if after.Revision != 2 || web < 0 || len(after.Repositories[web].Links) != 0 {
					t.Fatal("removed legacy links resurrected", after)
				}
			}
			if todo := todoNow(restarted, id); todo.Stage != "review" || todo.PRURL != latest {
				t.Fatal("naming changed legacy task progress", todo)
			}
			t.Logf("Node source revision=%d, saved source revision=%d, current revision=%d, Node calls=%d; restart retry identical", source.Repositories.Revision, saved.Source.Repositories.Revision, after.Revision, calls.Load())
		})
	}
}
