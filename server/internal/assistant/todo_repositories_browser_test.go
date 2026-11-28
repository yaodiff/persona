package assistant

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

// Opt-in fixture uses real manager persistence and handlers; unrelated app routes
// use the repository mock hub. It never connects to a production hub or engine.
func TestTodoRepositoryBrowserHarness(t *testing.T) {
	if os.Getenv("TODO_REPOSITORY_QA") != "1" {
		t.Skip("opt-in isolated browser fixture")
	}
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _ := repositoryService(t, database)
	for _, raw := range []string{`{"url":"https://github.com/example/web","name":"Example Web"}`, `{"url":"https://gitlab.example.com/example/subgroup/api","provider":"gitlab","name":"Example API"}`} {
		var in repositoryInput
		if err := decodeTaskRepositoryInput([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		binding, err := in.binding()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.saveRepository(t.Context(), "", binding); err != nil {
			t.Fatal(err)
		}
	}
	todo, err := s.createPlainTodo(t.Context(), "Review example checkout", "Keep the existing workspace and task progress.", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.repositoryDocument(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if _, err = s.setTodoRepositories(t.Context(), todo.ID, repository.SetInput{ExpectedRevision: &zero, ExpectedCatalogRevision: &doc.Revision, Repositories: []repository.TaskInput{{RepoID: doc.Repositories[0].ID, Links: []repository.LinkInput{{Kind: "pull_request", Number: "7"}, {Kind: "pull_request", Number: "8"}}}}}); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(cmpOr(os.Getenv("TODO_REPOSITORY_QA_MOCK"), "http://127.0.0.1:18892"))
	fallback := httputil.NewSingleHostReverseProxy(target)
	done := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_qa/stop":
			select {
			case done <- struct{}{}:
			default:
			}
			apierr.WriteJSON(w, map[string]bool{"stopped": true})
		case strings.HasPrefix(r.URL.Path, "/api/v1/todos"), strings.HasPrefix(r.URL.Path, "/api/v1/assistant/repositories"):
			r.Header.Set(RoleHeader, roleFull)
			s.ServeHTTP(w, r)
		default:
			fallback.ServeHTTP(w, r)
		}
	})
	listener, err := net.Listen("tcp", cmpOr(os.Getenv("TODO_REPOSITORY_QA_ADDR"), "127.0.0.1:18893"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer srv.Close()
	go srv.Serve(listener)
	t.Log("isolated Todo repository fixture ready")
	select {
	case <-done:
	case <-time.After(20 * time.Minute):
		t.Fatal("browser fixture deadline exceeded")
	}
}
