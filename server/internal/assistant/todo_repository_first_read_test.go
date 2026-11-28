package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTodoRepositoriesFirstLegacyReadReturnsCommittedLinks(t *testing.T) {
	for _, entry := range []string{"GET", "todo_get"} {
		t.Run(entry, func(t *testing.T) {
			database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			const id = "legacy-first-read"
			urls := []string{"https://github.com/example/web/pull/7", "https://github.com/example/web/pull/8", "https://gitlab.example.com/team/api/-/merge_requests/7"}
			if err := database.CreateTodo(t.Context(), db.TodoRow{ID: id, Title: "Historic work", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{id: {Stage: "review", PR: urls[1], PRs: []string{urls[0], urls[2]}}}); err != nil {
				t.Fatal(err)
			}
			before, err := repositorystore.New(database).TodoRepositories(t.Context(), id)
			if err != nil || before.Revision != 0 || len(before.Repositories) != 0 {
				t.Fatal("fixture already migrated", before, err)
			}
			s, _ := repositoryService(t, database)
			path := "/api/v1/todos/" + id + "/repositories"
			args := json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))
			if entry == "GET" {
				w := placeDo(s, "", "GET", path, "")
				var immediate repository.TaskSet
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &immediate) != nil || immediate.Revision != 1 || len(immediate.Repositories) != 2 {
					t.Fatal("first GET returned pre-import state", w.Code, w.Body)
				}
				for _, url := range urls {
					if !strings.Contains(w.Body.String(), url) {
						t.Fatalf("first GET omitted %s: %s", url, w.Body)
					}
				}
			} else {
				text, err := s.toolTodo(t.Context(), "", "", "todo_get", args)
				if err != nil || !strings.Contains(text, "repositories (revision 1):") {
					t.Fatal("first tool read returned pre-import revision", text, err)
				}
				for _, url := range urls {
					if !strings.Contains(text, url) {
						t.Fatalf("first tool read omitted %s: %s", url, text)
					}
				}
			}
			persisted, err := repositorystore.New(database).TodoRepositories(t.Context(), id)
			if err != nil || persisted.Revision != 1 || len(persisted.Repositories) != 2 {
				t.Fatal("first response did not describe committed migration", persisted, err)
			}
			// The first response's revision must be immediately usable by an editor.
			if w := placeDo(s, "", "PUT", path, `{"expected_revision":1,"expected_catalog_revision":0,"repositories":[]}`); w.Code != 200 {
				t.Fatal("first response gave an unusable revision", w.Code, w.Body)
			}
			restarted, _ := repositoryService(t, database)
			w := placeDo(restarted, "", "GET", path, "")
			var empty repository.TaskSet
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &empty) != nil || empty.Revision != 2 || len(empty.Repositories) != 0 {
				t.Fatal("legacy read resurrected removed links", w.Code, w.Body)
			}
			text, err := restarted.toolTodo(t.Context(), "", "", "todo_get", args)
			if err != nil || !strings.Contains(text, "repositories (revision 2):") || !strings.Contains(text, "stage review") {
				t.Fatal("canonical removal changed legacy progress", text, err)
			}
			for _, url := range urls {
				if strings.Contains(text, url) {
					t.Fatalf("tool read resurrected %s after removal: %s", url, text)
				}
			}
		})
	}
}
