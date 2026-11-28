package assistant

import (
	"context"
	"encoding/json"
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
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

// Real manager HTTP and PostgreSQL, synthetic Node metadata and mock unrelated
// routes. The fixture cannot reach a production hub, Engine or remote repository.
func TestWorkspaceNamingBrowserHarness(t *testing.T) {
	if os.Getenv("WORKSPACE_NAMING_QA") != "1" {
		t.Skip("opt-in isolated browser fixture")
	}
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub().withMachine()
	f.nodes = map[string]nodeAnswer{"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
		var source workspacenaming.Source
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, err
		}
		return workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example", PersonaHome: "/home/example/.persona"}), nil
	}}
	s := newServiceOn(t, f, database)
	s.repositoryCheck = func(context.Context, repository.Identity) error { return nil }
	if _, err = s.updateSettings(t.Context(), func(v *Settings) error { v.MachineID = "server"; v.Timezone = "UTC"; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "api"} {
		identity, _ := repository.Parse("https://github.com/example/"+name, "")
		if _, err = s.saveRepository(t.Context(), "", RepositoryBinding{Identity: identity, Name: "Example " + name}); err != nil {
			t.Fatal(err)
		}
	}
	todo, err := s.createPlainTodo(t.Context(), "Example naming task", "Preview metadata without starting an Agent.", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.repositoryDocument(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	rows := []repository.TaskInput{}
	for _, r := range doc.Repositories {
		rows = append(rows, repository.TaskInput{RepoID: r.ID, RequestedRef: repository.RequestedRef{Kind: "default"}})
	}
	if _, err = s.setTodoRepositories(t.Context(), todo.ID, repository.SetInput{ExpectedRevision: &zero, ExpectedCatalogRevision: &doc.Revision, Repositories: rows}); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(cmpOr(os.Getenv("WORKSPACE_NAMING_QA_MOCK"), "http://127.0.0.1:19852"))
	fallback := httputil.NewSingleHostReverseProxy(target)
	done := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/_qa/stop":
			select {
			case done <- struct{}{}:
			default:
			}
			apierr.WriteJSON(w, map[string]bool{"stopped": true})
		case r.URL.Path == "/api/v1/_qa/todo":
			apierr.WriteJSON(w, map[string]string{"id": todo.ID})
		case r.URL.Path == "/api/v1/assistant" || strings.HasPrefix(r.URL.Path, "/api/v1/assistant/workspace-naming") || strings.HasPrefix(r.URL.Path, "/api/v1/todos") || strings.HasPrefix(r.URL.Path, "/api/v1/assistant/repositories"):
			r.Header.Set(RoleHeader, roleFull)
			s.ServeHTTP(w, r)
		default:
			fallback.ServeHTTP(w, r)
		}
	})
	listener, err := net.Listen("tcp", cmpOr(os.Getenv("WORKSPACE_NAMING_QA_ADDR"), "127.0.0.1:19853"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer srv.Close()
	go srv.Serve(listener)
	t.Log("isolated workspace naming fixture ready")
	select {
	case <-done:
	case <-time.After(12 * time.Minute):
		t.Fatal("browser fixture deadline exceeded")
	}
}
