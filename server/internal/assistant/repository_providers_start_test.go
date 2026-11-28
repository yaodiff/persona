package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// startingAcme is Acme Code right after a restart, as the extensions' manager answers then: no provider is listed
// until it has started.
type startingAcme struct {
	*acmeProvider
	started chan struct{}
}

func (p startingAcme) up() bool {
	select {
	case <-p.started:
		return true
	default:
		return false
	}
}

func (p startingAcme) RepositoryProviders() []repository.ProviderInfo {
	if !p.up() {
		return nil
	}
	return p.acmeProvider.RepositoryProviders()
}

func (p startingAcme) RepositoryProvider(id string) (repository.ProviderInfo, bool) {
	if !p.up() {
		return repository.ProviderInfo{}, false
	}
	return p.acmeProvider.RepositoryProvider(id)
}

func (p startingAcme) RepositoryProviderForHost(host string) (repository.ProviderInfo, bool) {
	if !p.up() {
		return repository.ProviderInfo{}, false
	}
	return p.acmeProvider.RepositoryProviderForHost(host)
}

// providersRestarting makes s's providers those of a manager that has not started yet; the channel starts it.
func providersRestarting(s *Service, f *acmeProvider) chan struct{} {
	started := make(chan struct{})
	s.repoExt, s.repoStarted = startingAcme{f, started}, started
	return started
}

// shorten sets *v to short until the test ends.
func shorten[T any](t *testing.T, v *T, short T) {
	t.Helper()
	old := *v
	*v = short
	t.Cleanup(func() { *v = old })
}

// Right after a restart the catalog waits, within its budget, for the providers; still not listed, it says they are
// starting (409), never that the provider is missing or the address is no repository's.
func TestRepositoryCatalogWaitsForTheProviders(t *testing.T) {
	s, f, _ := acmeService(t, ruleStore{&memStore{}})
	saved := savedRepository(t, s, `{"url":"https://code.acme.test/team/app"}`)
	f.asked()
	for _, req := range []struct{ method, path, body string }{
		{"POST", repoBase + "/preview", `{"url":"https://code.acme.test/team/web"}`},
		{"POST", repoBase, `{"url":"https://code.acme.test/team/web","provider":"` + acmeProviderID + `"}`},
		{"POST", repoBase, `{"url":"https://code.acme.test/team/api"}`},
		{"PUT", repoBase + "/" + saved.ID, `{"url":"https://code.acme.test/team/app","clone_url":"https://code.acme.test/team/app.git"}`},
	} {
		started := providersRestarting(s, f)
		time.AfterFunc(50*time.Millisecond, func() { close(started) })
		if w := placeDo(s, "", req.method, req.path, req.body); w.Code != 200 {
			t.Fatalf("%s %s: %d %s", req.method, req.body, w.Code, w.Body)
		}
	}
	started := providersRestarting(s, f)
	time.AfterFunc(50*time.Millisecond, func() { close(started) })
	w := placeDo(s, "read", "GET", repoBase+"/providers", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"`+acmeProviderID+`"`) {
		t.Fatalf("providers after the start: %d %s", w.Code, w.Body)
	}

	shorten(t, &providerBudget, 100*time.Millisecond)
	shorten(t, &providersListWait, 50*time.Millisecond)
	providersRestarting(s, f)
	f.asked()
	for _, req := range []struct{ method, path, body string }{
		{"POST", repoBase + "/preview", `{"url":"https://code.acme.test/team/new"}`},
		{"POST", repoBase, `{"url":"https://code.acme.test/team/new"}`},
		{"POST", repoBase, `{"url":"https://code.acme.test/team/new","provider":"` + acmeProviderID + `"}`},
		{"PUT", repoBase + "/" + saved.ID, `{"url":"https://code.acme.test/team/app","clone_url":"git@ssh.acme.test:team/app.git"}`},
	} {
		w := placeDo(s, "", req.method, req.path, req.body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), `"repository_providers_starting"`) || !strings.Contains(w.Body.String(), "扩展还在启动中") {
			t.Errorf("%s %s: %d %s", req.method, req.body, w.Code, w.Body)
		}
	}
	if _, err := s.toolRepositoryAdd(t.Context(), "server", "c1", json.RawMessage(`{"url":"https://code.acme.test/team/new"}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "repository_providers_starting: ") {
		t.Fatal("tool:", err)
	}
	// A saved repository's details, and the built-ins, need no provider: they don't wait.
	if w := placeDo(s, "", "PUT", repoBase+"/"+saved.ID, `{"url":"https://code.acme.test/team/app","name":"Kept"}`); w.Code != 200 {
		t.Fatalf("metadata edit while starting: %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "", "POST", repoBase, `{"url":"https://github.com/example/web"}`); w.Code != 200 {
		t.Fatalf("built-in while starting: %d %s", w.Code, w.Body)
	}
	// The providers list answers what it has after a moment.
	w = placeDo(s, "read", "GET", repoBase+"/providers", "")
	var out struct{ Providers []repository.ProviderInfo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Providers) != 2 {
		t.Fatalf("providers while starting: %d %s", w.Code, w.Body)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("asked while starting", got)
	}
}

// A to-do link that can't be placed yet says, in the asker's language, that the extensions are still starting.
func TestTodoRepositoryLinksSayTheProvidersAreStarting(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	repo := savedRepository(t, s, `{"url":"https://code.acme.test/team/app"}`)
	w := placeDo(s, "", "POST", "/api/v1/todos", fmt.Sprintf(`{"title":"Provider work","repository_bindings":{"catalog_revision":1,"repositories":[{"repo_id":%q,"links":[]}]}}`, repo.ID))
	var created struct{ Todo Todo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	plain, err := s.createPlainTodo(t.Context(), "Other work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	shorten(t, &providerBudget, 100*time.Millisecond)
	providersRestarting(s, f)
	f.asked()
	w = placeDo(s, "", "PUT", "/api/v1/todos/"+created.Todo.ID+"/repositories/"+repo.ID+"/links", `{"expected_revision":1,"links":[{"kind":"pull_request","number":"4"}]}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"task_repositories_providers_starting"`) || !strings.Contains(w.Body.String(), "扩展还在启动中") {
		t.Fatalf("links: %d %s", w.Code, w.Body)
	}
	args, _ := json.Marshal(map[string]any{"id": plain.ID, "urls": []string{"https://code.acme.test/team/app/reviews/5"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err == nil ||
		!strings.HasPrefix(err.Error(), "task_repositories_providers_starting: ") || strings.Contains(err.Error(), "try again") {
		t.Fatal("links_add:", err)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("asked while starting", got)
	}
	// Its own text in every language, with nothing of the manager's English in it.
	pe := providerProblem(providersStarting, "", "code.acme.test")
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, ns := range []string{"task-repositories", "assistant-repositories"} {
			if got := i18n.T(l, ns+"."+providerCode(pe), providerArgs(pe)); got == ns+".providers_starting" || strings.Contains(got, "{") ||
				l != i18n.En && strings.ContainsAny(got, "abcdefghijklmnopqrstuvwxyz") {
				t.Errorf("%s %s: %q", l, ns, got)
			}
		}
	}
}

// stuckAcme is Acme Code getting no answer about an address containing stuck: the call waits out its time and fails
// the way the extensions' manager says a call out of time; a call already out of time fails the same way.
type stuckAcme struct {
	*acmeProvider
	stuck string
}

func (p *stuckAcme) RepositoryParse(ctx context.Context, provider, remote string) (repository.Parsed, error) {
	if p.stuck != "" && strings.Contains(remote, p.stuck) {
		<-ctx.Done()
	}
	if ctx.Err() != nil {
		p.call("parse-timeout")
		return repository.Parsed{}, &repository.ProviderError{Code: "failed", Provider: "Acme Code", Why: "no answer in time"}
	}
	return p.acmeProvider.RepositoryParse(ctx, provider, remote)
}

// The links of one reply each get a provider budget of their own, and only a provider's answer stops a later reply
// from asking again: a call out of time, or the providers still starting, does not.
func TestReportedLinksEachGetTheirOwnBudget(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	p := &stuckAcme{acmeProvider: f, stuck: "/reviews/1"}
	s.repoExt = p
	shorten(t, &providerBudget, 200*time.Millisecond)
	shorten(t, &reportedLinkStart, 50*time.Millisecond)
	todo, err := s.createPlainTodo(t.Context(), "Reported work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	const app = "https://code.acme.test/team/app"
	links := func() []string {
		t.Helper()
		var out []string
		for _, row := range todoRepositoriesOf(t, s, todo.ID).Repositories {
			for _, l := range row.Links {
				out = append(out, l.CanonicalURL)
			}
		}
		slices.Sort(out)
		return out
	}
	report := repositoryReporter(t, s, todo.ID)
	reply := func(text string, created ...string) {
		t.Helper()
		report(text, created...)
		s.reportedLinks.runs.Wait()
	}
	reply("Opened "+app+"/reviews/1 and "+app+"/reviews/2", app+"/reviews/1", app+"/reviews/2")
	if got := links(); !slices.Equal(got, []string{app + "/reviews/2"}) {
		t.Fatal("a slow first link left the second no time:", got, f.asked())
	}
	p.stuck = ""
	f.asked()
	reply("Still open: "+app+"/reviews/1 and "+app+"/reviews/2", app+"/reviews/1", app+"/reviews/2")
	if got := f.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
		t.Fatal("asked again", got) // reviews/1 got no answer before; reviews/2 was answered
	}
	if got := links(); !slices.Equal(got, []string{app + "/reviews/1", app + "/reviews/2"}) {
		t.Fatal(got)
	}
	// Still starting: nothing is asked, and the next reply after the start asks.
	started := make(chan struct{})
	s.repoStarted = started
	reply("Opened "+app+"/reviews/3", app+"/reviews/3")
	if got := f.asked(); len(got) != 0 {
		t.Fatal("asked while starting", got)
	}
	close(started)
	reply("Opened "+app+"/reviews/3", app+"/reviews/3")
	if got := links(); len(got) != 3 || !slices.Contains(got, app+"/reviews/3") {
		t.Fatal("after the start:", got, f.asked())
	}
}
