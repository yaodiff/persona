package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTodoRepositoryToolsEnforceTitleByteLimitAtomically(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	e := newTodoEnv(t, database)
	e.s.repositoryCheck = func(context.Context, repository.Identity) error { return nil }
	w := placeDo(e.s, "", "POST", repoBase, `{"url":"https://github.com/example/web"}`)
	var catalog struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil {
		t.Fatal(w.Code, w.Body)
	}
	for _, name := range []string{"todo_create", "todo_repositories_set", "todo_repository_links_set", "todo_repository_links_add"} {
		t.Run(name, func(t *testing.T) {
			id := e.create("Existing work for " + name).ID
			zero, one := int64(0), int64(1)
			if _, err := e.s.setTodoRepositories(t.Context(), id, repository.SetInput{ExpectedRevision: &zero, ExpectedCatalogRevision: &one, Repositories: []repository.TaskInput{{RepoID: catalog.Repository.ID}}}); err != nil {
				t.Fatal(err)
			}
			before, err := repositorystore.New(database).TodoRepositories(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			initialTodos, err := database.ListTodos(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, length := range []int{513, 512} {
				title := strings.Repeat("é", length)
				links := []repository.LinkInput{{Kind: "issue", Number: "7", Title: title}}
				rows := []repository.TaskInput{{RepoID: catalog.Repository.ID, Links: links}}
				args := map[string]any{"id": id, "expected_revision": before.Revision}
				switch name {
				case "todo_create":
					args = map[string]any{"title": "New linked work for " + name, "repository_bindings": map[string]any{"catalog_revision": 1, "repositories": rows}}
				case "todo_repositories_set":
					args["expected_catalog_revision"], args["repositories"] = 1, rows
				case "todo_repository_links_set":
					args["repo_id"], args["links"] = catalog.Repository.ID, links
				case "todo_repository_links_add":
					args = map[string]any{"id": id, "links": []repository.LinkInput{{URL: "https://github.com/example/web/issues/7", Title: title}}}
				}
				raw, _ := json.Marshal(args)
				text, err := e.s.runTool(t.Context(), "", "", name, raw)
				if length == 513 {
					if err == nil {
						t.Fatal("accepted 1,026-byte source title", text)
					}
					after, readErr := repositorystore.New(database).TodoRepositories(t.Context(), id)
					if readErr != nil || !reflect.DeepEqual(after, before) {
						t.Fatal("rejected title partially changed associations", after, readErr)
					}
					todos, err := database.ListTodos(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if len(todos) != len(initialTodos) {
						t.Fatal("rejected title partially created a to-do")
					}
				} else if err != nil || name != "todo_create" && !strings.Contains(text, title) {
					t.Fatal("rejected 1,024-byte source title", text, err)
				} else {
					if name == "todo_create" {
						todos, err := database.ListTodos(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						id = ""
						for _, todo := range todos {
							if todo.Title == "New linked work for "+name {
								id = todo.ID
							}
						}
					}
					after, err := repositorystore.New(database).TodoRepositories(t.Context(), id)
					if err != nil || len(after.Repositories) != 1 || len(after.Repositories[0].Links) != 1 || after.Repositories[0].Links[0].Title != title {
						t.Fatal("valid source title was not saved", after, err)
					}
				}
			}
		})
	}
}
