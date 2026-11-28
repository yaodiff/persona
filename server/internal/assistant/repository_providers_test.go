package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const acmeProviderID = "ext:acme-code/acme"

// acmeProvider is a repository provider extension on code.acme.test (ssh.acme.test for git): it answers like the
// extensions' manager does, re-checking its own answers, and records every call.
type acmeProvider struct {
	mu        sync.Mutex
	installed bool
	state     string
	checkErr  error
	calls     []string
	lookups   int
}

func newAcmeProvider() *acmeProvider {
	return &acmeProvider{installed: true, state: repository.ProviderAvailable}
}

func (f *acmeProvider) set(installed bool, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed, f.state = installed, state
}

// asked are the provider's calls since the last time, forgotten.
func (f *acmeProvider) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func (f *acmeProvider) looked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.lookups
	f.lookups = 0
	return n
}

func (f *acmeProvider) info() repository.ProviderInfo {
	return repository.ProviderInfo{ID: acmeProviderID, Name: "Acme Code", Hosts: []string{"code.acme.test", "ssh.acme.test"}, State: f.state,
		LinkKinds: []repository.LinkLabel{{Kind: "pull_request", Label: "Review"}}}
}

func (f *acmeProvider) RepositoryProviders() []repository.ProviderInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.installed {
		return nil
	}
	archive := repository.ProviderInfo{ID: "ext:acme-code/archive", Name: "Acme Archive", Hosts: []string{"archive.acme.test"}, State: repository.ProviderOff}
	return []repository.ProviderInfo{f.info(), archive}
}

func (f *acmeProvider) RepositoryProvider(id string) (repository.ProviderInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	return f.info(), f.installed && id == acmeProviderID
}

func (f *acmeProvider) RepositoryProviderForHost(host string) (repository.ProviderInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	return f.info(), f.installed && slices.Contains(f.info().Hosts, host)
}

func (f *acmeProvider) call(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *acmeProvider) RepositoryParse(_ context.Context, provider, remote string) (repository.Parsed, error) {
	f.call("parse")
	path := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(remote, "https://code.acme.test/"), "git@ssh.acme.test:"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return repository.Parsed{}, &repository.ProviderError{Code: "not_found", Provider: "Acme Code"}
	}
	path = parts[0] + "/" + parts[1]
	return repository.CheckParseAnswer(f.info(), repository.ParseAnswer{InstanceURL: "https://code.acme.test", Path: path,
		CanonicalURL: "https://code.acme.test/" + path, CloneURL: "git@ssh.acme.test:" + path + ".git", DisplayName: strings.ToUpper(parts[1][:1]) + parts[1][1:]})
}

func (f *acmeProvider) RepositoryCheck(context.Context, repository.Identity) error {
	f.call("check")
	return f.checkErr
}

func (f *acmeProvider) RepositoryLink(_ context.Context, repo repository.Identity, in repository.LinkInput) (repository.TaskLink, error) {
	f.call("link")
	kind, number := in.Kind, in.Number
	if in.URL != "" {
		rest := strings.Split(strings.TrimPrefix(in.URL, repo.CanonicalURL+"/"), "/")
		if len(rest) < 2 || rest[0] != "reviews" && rest[0] != "issues" {
			return repository.TaskLink{}, &repository.ProviderError{Code: "not_found", Provider: "Acme Code"}
		}
		kind, number = map[string]string{"reviews": "pull_request", "issues": "issue"}[rest[0]], rest[1]
	}
	segment := map[string]string{"pull_request": "reviews", "issue": "issues"}[kind]
	return repository.CheckLinkAnswer(repo, in, repository.LinkAnswer{Kind: kind, Number: number, CanonicalURL: repo.CanonicalURL + "/" + segment + "/" + number})
}

func (f *acmeProvider) RepositoryDescribe(_ context.Context, _, canonicalURL string) (repository.Description, error) {
	f.call("describe")
	return repository.CheckDescribeAnswer("Title of "+canonicalURL[strings.LastIndex(canonicalURL, "/")+1:], "open"), nil
}

// acmeService is repositoryService with the Acme Code provider installed and on.
func acmeService(t *testing.T, store Store) (*Service, *acmeProvider, *int) {
	t.Helper()
	s, checks := repositoryService(t, store)
	f := newAcmeProvider()
	s.repoExt = f
	return s, f, checks
}

// repositoryReporter attaches a real task Agent in the fixture. Its report and
// independent native creation outcomes are supplied separately by each test.
func repositoryReporter(t *testing.T, s *Service, id string) func(string, ...string) {
	t.Helper()
	const machine, session, cwd = "box", "provider-reporter", "/home/test/work"
	f := s.hub.(*fakeHub)
	f.setSessions(machine, proto.Session{MachineID: machine, SessionID: session, Cwd: cwd, Status: proto.StatusIdle})
	if err := s.linkTodoAgent(t.Context(), id, machine, session, "Reporter", cwd, "claude"); err != nil {
		t.Fatal(err)
	}
	return func(text string, created ...string) {
		t.Helper()
		todo := todoNow(s, id)
		reply := artifactReportProof(f, todo, text, created...)
		s.advanceTaskReplyAt(t.Context(), todo, machine, session, text, reply.TS)
	}
}

func savedRepository(t *testing.T, s *Service, body string) RepositoryBinding {
	t.Helper()
	w := placeDo(s, "", "POST", repoBase, body)
	var saved struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	return saved.Repository
}

func TestRepositoryCatalogThroughAProvider(t *testing.T) {
	s, f, checks := acmeService(t, ruleStore{&memStore{}})
	w := placeDo(s, "", "POST", repoBase+"/preview", `{"url":"https://code.acme.test/team/app/reviews/3"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"provider_name":"Acme Code"`) || !strings.Contains(w.Body.String(), `"provider":"`+acmeProviderID+`"`) {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	if got := f.asked(); !slices.Equal(got, []string{"parse"}) {
		t.Fatal("preview asked", got)
	}
	// The host says which provider it is; the provider's own name for the repository is the default name.
	saved := savedRepository(t, s, `{"url":"https://code.acme.test/team/app","purpose":"Demo"}`)
	if saved.Provider != acmeProviderID || saved.ProviderName != "Acme Code" || saved.Name != "App" || saved.CanonicalURL != "https://code.acme.test/team/app" ||
		saved.CloneURL != "git@ssh.acme.test:team/app.git" || saved.VerifiedAt.IsZero() {
		t.Fatalf("%+v", saved)
	}
	if got := f.asked(); !slices.Equal(got, []string{"parse", "check"}) || *checks != 0 {
		t.Fatal("save asked", got, *checks)
	}
	// An explicit GitLab stays GitLab, offline and checked as before, on a host an extension claims.
	gitlab := savedRepository(t, s, `{"url":"https://code.acme.test/team/web","provider":"gitlab"}`)
	if gitlab.Provider != "gitlab" || gitlab.ProviderName != "" || *checks != 1 || len(f.asked()) != 0 {
		t.Fatalf("explicit gitlab: %+v %d", gitlab, *checks)
	}
	// The provider's check answers map to the catalog's codes.
	for code, want := range map[string]string{"not_found": "repository_provider_not_found", "not_signed_in": "repository_provider_not_signed_in",
		"refused": "repository_provider_refused", "failed": "repository_provider_failed", "unheard_of": "repository_provider_failed"} {
		f.checkErr = &repository.ProviderError{Code: code, Provider: "Acme Code", Why: "the words of " + code}
		w := placeDo(s, "", "POST", repoBase, `{"url":"https://code.acme.test/team/other"}`)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"`+want+`"`) || !strings.Contains(w.Body.String(), "Acme Code") ||
			(code == "refused" || code == "failed") && !strings.Contains(w.Body.String(), "the words of "+code) {
			t.Errorf("%s: %d %s", code, w.Code, w.Body)
		}
	}
	f.checkErr = nil
	f.asked()
	// With the provider off, a saved reference's details still change, without asking it anything.
	f.set(true, repository.ProviderOff)
	for _, body := range []string{
		`{"url":"https://code.acme.test/team/app","provider":"` + acmeProviderID + `","name":"Renamed","purpose":"Demo"}`,
		`{"url":"https://code.acme.test/team/app","name":"Again","clone_url":"https://code.acme.test/team/app.git"}`,
	} {
		w = placeDo(s, "", "PUT", repoBase+"/"+saved.ID, body)
		var got struct{ Repository RepositoryBinding }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Repository.Identity != saved.Identity ||
			got.Repository.ProviderName != "Acme Code" || !got.Repository.VerifiedAt.Equal(saved.VerifiedAt) {
			t.Fatalf("metadata edit: %d %s", w.Code, w.Body)
		}
	}
	if w := placeDo(s, "", "PUT", repoBase+"/"+saved.ID, `{"url":"https://code.acme.test/team/app","clone_url":"https://elsewhere.example.test/team/app.git"}`); w.Code != 400 {
		t.Fatalf("clone address off the provider's hosts: %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "", "POST", repoBase, `{"url":"https://code.acme.test/team/new"}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"repository_provider_off"`) || !strings.Contains(w.Body.String(), "Acme Code") {
		t.Fatalf("new reference, provider off: %d %s", w.Code, w.Body)
	}
	if _, err := s.toolRepositoryAdd(t.Context(), "server", "c1", json.RawMessage(`{"url":"https://code.acme.test/team/new"}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "repository_provider_off: ") || !strings.Contains(err.Error(), "Acme Code") {
		t.Fatal("tool, provider off:", err)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an off provider was asked", got)
	}
	// Uninstalled: the saved reference stays editable, a new one names the missing provider.
	f.set(false, repository.ProviderOff)
	if w := placeDo(s, "", "PUT", repoBase+"/"+saved.ID, `{"url":"https://code.acme.test/team/app","name":"Kept"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "Kept") {
		t.Fatalf("metadata edit, uninstalled: %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "", "POST", repoBase, `{"url":"https://code.acme.test/team/new","provider":"`+acmeProviderID+`"}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"repository_no_provider"`) {
		t.Fatalf("uninstalled: %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "", "POST", repoBase, `{"url":"https://code.acme.test/team/new"}`); w.Code != 400 || !strings.Contains(w.Body.String(), `"repository_remote"`) {
		t.Fatalf("unclaimed host without a provider: %d %s", w.Code, w.Body)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an uninstalled provider was asked", got)
	}
}

func TestRepositoryProvidersRoute(t *testing.T) {
	s, f, _ := acmeService(t, ruleStore{&memStore{}})
	read := func() []repository.ProviderInfo {
		t.Helper()
		w := placeDo(s, "read", "GET", repoBase+"/providers", "")
		var out struct {
			Manager   string
			Providers []repository.ProviderInfo
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Manager != s.managerIdentity() {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out.Providers
	}
	got := read()
	ids := []string{}
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"github", "gitlab", "ext:acme-code/archive", acmeProviderID}) || !got[0].Builtin || got[3].Name != "Acme Code" ||
		got[3].State != repository.ProviderAvailable || got[2].State != repository.ProviderOff || len(got[3].LinkKinds) != 1 {
		t.Fatalf("%+v", got)
	}
	f.set(false, repository.ProviderOff)
	if got := read(); len(got) != 2 {
		t.Fatalf("uninstalled: %+v", got)
	}
	s.repoExt = nil
	if got := read(); len(got) != 2 || got[0].ID != "github" || got[1].ID != "gitlab" {
		t.Fatalf("no extensions: %+v", got)
	}
}

// todoRepositoriesOf is GET /todos/{id}/repositories.
func todoRepositoriesOf(t *testing.T, s *Service, id string) repository.TaskSet {
	t.Helper()
	w := placeDo(s, "", "GET", "/api/v1/todos/"+id+"/repositories", "")
	var set repository.TaskSet
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &set) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return set
}

func linkTitles(row repository.TaskRepository) map[string]string {
	out := map[string]string{}
	for _, l := range row.Links {
		out[l.CanonicalURL] = l.Title
	}
	return out
}

func TestTodoRepositoryLinksThroughAProvider(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	repo := savedRepository(t, s, `{"url":"https://code.acme.test/team/app"}`)
	f.asked()
	const app = "https://code.acme.test/team/app"
	// A new to-do binds the provider's repository; its link comes from the provider, the missing title from describe.
	w := placeDo(s, "", "POST", "/api/v1/todos", fmt.Sprintf(`{"title":"Provider work","repository_bindings":{"catalog_revision":1,"repositories":[{"repo_id":%q,"links":[{"kind":"pull_request","number":"12"}]}]}}`, repo.ID))
	var created struct{ Todo Todo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := created.Todo.ID
	path := "/api/v1/todos/" + id + "/repositories"
	if got := f.asked(); !slices.Equal(got, []string{"link", "describe"}) {
		t.Fatal("create asked", got)
	}
	set := todoRepositoriesOf(t, s, id)
	row := set.Repositories[0]
	if len(set.Repositories) != 1 || row.SourceSnapshot.ProviderName != "Acme Code" || row.SourceSnapshot.CloneURL != "git@ssh.acme.test:team/app.git" ||
		row.ProviderState != repository.ProviderAvailable || len(row.LinkKinds) != 1 || row.LinkKinds[0].Label != "Review" ||
		linkTitles(row)[app+"/reviews/12"] != "Title of 12" {
		t.Fatalf("%+v", set)
	}
	// PUT …/links: the saved link needs no call, the new one is one repo.link, and describe fills its title.
	w = placeDo(s, "", "PUT", path+"/"+repo.ID+"/links", `{"expected_revision":1,"links":[{"kind":"pull_request","number":"12"},{"url":"`+app+`/issues/3"}]}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if got := f.asked(); !slices.Equal(got, []string{"link", "describe"}) {
		t.Fatal("links asked", got)
	}
	set = todoRepositoriesOf(t, s, id)
	if titles := linkTitles(set.Repositories[0]); len(titles) != 2 || titles[app+"/issues/3"] != "Title of 3" || titles[app+"/reviews/12"] != "Title of 12" {
		t.Fatalf("%+v", set)
	}
	// Off: saving the links it has (as the web does, by kind and number) asks nothing and keeps them; a new one is refused.
	f.set(true, repository.ProviderOff)
	keep := fmt.Sprintf(`{"expected_revision":%d,"expected_catalog_revision":1,"repositories":[{"repo_id":%q,"links":[{"kind":"pull_request","number":"12"},{"kind":"issue","number":"3","title":"Renamed issue"}]}]}`, set.Revision, repo.ID)
	if w := placeDo(s, "", "PUT", path, keep); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	set = todoRepositoriesOf(t, s, id)
	if titles := linkTitles(set.Repositories[0]); len(titles) != 2 || titles[app+"/issues/3"] != "Renamed issue" || titles[app+"/reviews/12"] != "Title of 12" ||
		set.Repositories[0].ProviderState != repository.ProviderOff {
		t.Fatalf("off: %+v", set)
	}
	added := strings.Replace(keep, `]}]}`, `,{"kind":"issue","number":"4"}]}]}`, 1)
	added = strings.Replace(added, fmt.Sprintf(`"expected_revision":%d`, set.Revision-1), fmt.Sprintf(`"expected_revision":%d`, set.Revision), 1)
	if w := placeDo(s, "", "PUT", path, added); w.Code != 409 || !strings.Contains(w.Body.String(), `"task_repositories_provider_off"`) || !strings.Contains(w.Body.String(), "Acme Code") {
		t.Fatal("new link, provider off:", w.Code, w.Body)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an off provider was asked", got)
	}
	// Uninstalled: reads keep the rows and links as saved; links_set keeps the links it already has.
	f.set(false, repository.ProviderOff)
	before := todoRepositoriesOf(t, s, id)
	if r := before.Repositories[0]; r.ProviderState != repository.ProviderMissing || r.LinkKinds != nil || len(r.Links) != 2 || r.SourceSnapshot != row.SourceSnapshot {
		t.Fatalf("uninstalled read: %+v", before)
	}
	args := fmt.Sprintf(`{"id":%q,"repo_id":%q,"expected_revision":%d,"links":[{"kind":"pull_request","number":"12"},{"url":"%s/issues/3"}]}`, id, repo.ID, before.Revision, app)
	if _, err := s.toolTodoRepositories(t.Context(), "machine", "conversation", "todo_repository_links_set", json.RawMessage(args)); err != nil {
		t.Fatal(err)
	}
	after := todoRepositoriesOf(t, s, id)
	if !slices.Equal(after.Repositories[0].Links, before.Repositories[0].Links) || after.Revision != before.Revision+1 {
		t.Fatalf("links_set lost links: %+v", after)
	}
	args = fmt.Sprintf(`{"id":%q,"repo_id":%q,"expected_revision":%d,"links":[{"kind":"issue","number":"5"}]}`, id, repo.ID, after.Revision)
	if _, err := s.toolTodoRepositories(t.Context(), "machine", "conversation", "todo_repository_links_set", json.RawMessage(args)); err == nil ||
		!strings.HasPrefix(err.Error(), "task_repositories_no_provider: ") || !strings.Contains(err.Error(), "Acme Code") {
		t.Fatal("new link, uninstalled:", err)
	}
	// A new address on the host only saved rows use is no_provider, never a GitHub-style guess.
	args = fmt.Sprintf(`{"id":%q,"urls":["%s/pull/6"]}`, id, app)
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "machine", "conversation", "todo_repository_links_add", json.RawMessage(args)); err == nil ||
		!strings.HasPrefix(err.Error(), "task_repositories_no_provider: ") {
		t.Fatal("unclaimed host of a saved provider:", err)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an uninstalled provider was asked", got)
	}
}

func TestTodoRepositoryLinkAddressesThroughAProvider(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	todo, err := s.createPlainTodo(t.Context(), "Reported work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	const review = "https://code.acme.test/team/app/reviews/15"
	add := func() error {
		args, _ := json.Marshal(map[string]any{"id": todo.ID, "urls": []string{review}})
		_, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args)
		return err
	}
	if err := add(); err != nil {
		t.Fatal(err)
	}
	if got := f.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
		t.Fatal("first add asked", got)
	}
	set := todoRepositoriesOf(t, s, todo.ID)
	if len(set.Repositories) != 1 || !strings.HasPrefix(set.Repositories[0].RepoID, "linked_") || set.Repositories[0].SourceSnapshot.Identity.Provider != acmeProviderID ||
		set.Repositories[0].SourceSnapshot.ProviderName != "Acme Code" || linkTitles(set.Repositories[0])[review] != "Title of 15" {
		t.Fatalf("%+v", set)
	}
	if err := add(); err != nil {
		t.Fatal(err)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("a saved link was asked again", got)
	}
	// Reports: an off provider's links are skipped (and never guessed as GitHub's); on, they go through it.
	report := repositoryReporter(t, s, todo.ID)
	const nextReview = "https://code.acme.test/team/app/reviews/16"
	const otherPR = "https://code.acme.test/team/app/pull/17"
	f.set(true, repository.ProviderOff)
	report("Opened "+nextReview+" and "+otherPR+".", nextReview, otherPR)
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an off provider was asked", got)
	}
	if got := todoRepositoriesOf(t, s, todo.ID); got.Revision != set.Revision || len(got.Repositories) != 1 || len(got.Repositories[0].Links) != 1 {
		t.Fatalf("off report changed links: %+v", got)
	}
	f.set(true, repository.ProviderAvailable)
	report("Opened " + nextReview) // An attached Agent's report still needs independent proof.
	s.reportedLinks.runs.Wait()
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an unproven report reached the provider", got)
	}
	if got := todoRepositoriesOf(t, s, todo.ID); got.Revision != set.Revision {
		t.Fatalf("an unproven report changed associations: %+v", got)
	}
	report("Opened "+nextReview+", see https://code.acme.test/team/app/issues/2.", nextReview)
	s.reportedLinks.runs.Wait()
	if got := todoRepositoriesOf(t, s, todo.ID); len(got.Repositories) != 1 || len(got.Repositories[0].Links) != 2 || linkTitles(got.Repositories[0])["https://code.acme.test/team/app/reviews/16"] != "Title of 16" {
		t.Fatalf("report: %+v", got)
	}
}

func TestTodoRepositoryLegacyImportNeverAsksAProvider(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _, _ := acmeService(t, database)
	todo, err := s.createPlainTodo(t.Context(), "Historic work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{todo.ID: {Stage: "review", PR: "https://code.acme.test/team/app/pull/30",
		PRs: []string{"https://github.com/example/web/pull/7"}}}); err != nil {
		t.Fatal(err)
	}
	s, f, _ := acmeService(t, database)
	set := todoRepositoriesOf(t, s, todo.ID)
	if len(set.Repositories) != 1 || set.Repositories[0].SourceSnapshot.Identity.Provider != "github" {
		t.Fatalf("legacy import: %+v", set)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("a read asked the provider", got)
	}
}

func TestTodoRepositoryBuiltinLinksNeverAskAProvider(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	repo := savedRepository(t, s, `{"url":"https://github.com/example/web"}`)
	f.asked()
	f.looked()
	w := placeDo(s, "", "POST", "/api/v1/todos", fmt.Sprintf(`{"title":"Built-in work","repository_bindings":{"catalog_revision":1,"repositories":[{"repo_id":%q,"links":[{"kind":"pull_request","number":"7"}]}]}}`, repo.ID))
	var created struct{ Todo Todo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := created.Todo.ID
	if w := placeDo(s, "", "PUT", "/api/v1/todos/"+id+"/repositories/"+repo.ID+"/links", `{"expected_revision":1,"links":[{"kind":"pull_request","number":"7"},{"url":"https://github.com/example/web/issues/8"}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	args, _ := json.Marshal(map[string]any{"id": id, "urls": []string{"https://github.com/example/web/pull/9"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	const reportedPR = "https://github.com/example/web/pull/10"
	repositoryReporter(t, s, id)("Opened "+reportedPR+" (see https://github.com/example/web/actions)", reportedPR)
	set := todoRepositoriesOf(t, s, id)
	if len(set.Repositories) != 1 || len(set.Repositories[0].Links) != 4 || set.Repositories[0].ProviderState != "" {
		t.Fatalf("%+v", set)
	}
	if got, n := f.asked(), f.looked(); len(got) != 0 || n != 0 {
		t.Fatal("built-in links reached the provider", got, n)
	}
}

// A GitHub or GitLab repository saved by choice on a host a provider claims keeps the built-in rules, offline.
func TestTodoRepositoryBuiltinRowOnAClaimedHost(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	repo := savedRepository(t, s, `{"url":"https://code.acme.test/team/web","provider":"gitlab"}`)
	const web = "https://code.acme.test/team/web/-/merge_requests/"
	w := placeDo(s, "", "POST", "/api/v1/todos", fmt.Sprintf(`{"title":"GitLab work","repository_bindings":{"catalog_revision":1,"repositories":[{"repo_id":%q,"links":[{"kind":"pull_request","number":"3"}]}]}}`, repo.ID))
	var created struct{ Todo Todo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	plain, err := s.createPlainTodo(t.Context(), "Unbound work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	f.asked()
	for _, state := range []string{repository.ProviderOff, repository.ProviderAvailable} {
		f.set(true, state)
		args, _ := json.Marshal(map[string]any{"id": created.Todo.ID, "urls": []string{web + "4"}})
		if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
			t.Fatal(state, err)
		}
	}
	repositoryReporter(t, s, created.Todo.ID)("Opened "+web+"5", web+"5")
	set := todoRepositoriesOf(t, s, created.Todo.ID)
	if len(set.Repositories) != 1 || set.Repositories[0].RepoID != repo.ID || len(set.Repositories[0].Links) != 3 {
		t.Fatalf("%+v", set)
	}
	// A to-do without the repository yet still gets the catalog's GitLab entry.
	args, _ := json.Marshal(map[string]any{"id": plain.ID, "urls": []string{web + "6"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	if set := todoRepositoriesOf(t, s, plain.ID); len(set.Repositories) != 1 || set.Repositories[0].RepoID != repo.ID {
		t.Fatalf("%+v", set)
	}
	s.reportedLinks.runs.Wait()
	if got := f.asked(); len(got) != 0 {
		t.Fatal("a built-in repository's links reached the provider", got)
	}
}

// With its provider gone, a host the catalog's extension repositories use never gets a GitHub-style guess.
func TestTodoRepositoryCatalogHostWithoutItsProvider(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	savedRepository(t, s, `{"url":"https://code.acme.test/team/app"}`)
	todo, err := s.createPlainTodo(t.Context(), "Later work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{todo.ID: {Stage: "review", PR: "https://code.acme.test/team/app/pull/5"}}); err != nil {
		t.Fatal(err)
	}
	s, f, _ = acmeService(t, database)
	f.set(false, repository.ProviderOff)
	args, _ := json.Marshal(map[string]any{"id": todo.ID, "urls": []string{"https://code.acme.test/team/app/pull/6"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err == nil ||
		!strings.HasPrefix(err.Error(), "task_repositories_no_provider: ") || !strings.Contains(err.Error(), "Acme Code") {
		t.Fatal("links_add:", err)
	}
	const reportedPR = "https://code.acme.test/team/app/pull/7"
	repositoryReporter(t, s, todo.ID)("Opened "+reportedPR, reportedPR)
	s.reportedLinks.runs.Wait()
	if set := todoRepositoriesOf(t, s, todo.ID); len(set.Repositories) != 0 {
		t.Fatalf("guessed: %+v", set)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("an uninstalled provider was asked", got)
	}
}

// Right after a restart the providers are not listed yet: nothing on a host no built-in owns is guessed meanwhile.
func TestTodoRepositoryLinksWaitForTheProviders(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _, _ := acmeService(t, database)
	todo, err := s.createPlainTodo(t.Context(), "Historic work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{todo.ID: {Stage: "review", PR: "https://code.acme.test/team/app/pull/30",
		PRs: []string{"https://github.com/example/web/pull/7"}}}); err != nil {
		t.Fatal(err)
	}
	s, f, _ := acmeService(t, database)
	starting := func() chan struct{} {
		c := make(chan struct{})
		s.repoStarted = c
		return c
	}
	// The legacy import waits for a read after the start; the report waits off the event loop.
	started := starting()
	if set := todoRepositoriesOf(t, s, todo.ID); len(set.Repositories) != 0 {
		t.Fatalf("imported before the start: %+v", set)
	}
	const reportedPR = "https://code.acme.test/team/app/reviews/31"
	repositoryReporter(t, s, todo.ID)("Opened "+reportedPR, reportedPR)
	if got := f.asked(); len(got) != 0 {
		t.Fatal("asked before the start", got)
	}
	close(started)
	s.reportedLinks.runs.Wait()
	set := todoRepositoriesOf(t, s, todo.ID)
	providers := map[string]int{}
	for _, row := range set.Repositories {
		providers[row.SourceSnapshot.Identity.Provider] = len(row.Links)
	}
	if len(set.Repositories) != 2 || providers["github"] != 1 || providers[acmeProviderID] != 1 {
		t.Fatalf("%+v", set)
	}
	if got := f.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
		t.Fatal("report asked", got)
	}
	// A request waits for the start within its budget.
	started = starting()
	time.AfterFunc(50*time.Millisecond, func() { close(started) })
	args, _ := json.Marshal(map[string]any{"id": todo.ID, "urls": []string{"https://code.acme.test/team/app/reviews/32"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	if set := todoRepositoriesOf(t, s, todo.ID); len(set.Repositories) != 2 || len(set.Repositories[1].Links)+len(set.Repositories[0].Links) != 3 {
		t.Fatalf("%+v", set)
	}
}

// slowAcme is Acme Code answering repo.parse only once let go.
type slowAcme struct {
	*acmeProvider
	release chan struct{}
}

func (p slowAcme) RepositoryParse(ctx context.Context, provider, remote string) (repository.Parsed, error) {
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return p.acmeProvider.RepositoryParse(ctx, provider, remote)
}

// An agent's reply never waits on a provider, and asks it about an address once, the PR it names included.
func TestReportedLinksNeverWaitOnAProvider(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	slow := slowAcme{f, make(chan struct{})}
	s.repoExt = slow
	todo, err := s.createPlainTodo(t.Context(), "Reported work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	const mr = "https://code.acme.test/team/app/merge_requests/3"
	report := repositoryReporter(t, s, todo.ID)
	start := time.Now()
	report("Opened "+mr, mr)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatal("the reply waited on the provider", took)
	}
	close(slow.release)
	s.reportedLinks.runs.Wait()
	if got := f.asked(); !slices.Equal(got, []string{"parse", "link"}) {
		t.Fatal("asked", got) // the provider has no such link: asked once, for the reply and its PR together
	}
	report("Opened "+mr+" again", mr)
	s.reportedLinks.runs.Wait()
	if got := f.asked(); len(got) != 0 {
		t.Fatal("asked again", got)
	}
	const reportedPR = "https://code.acme.test/team/app/reviews/4"
	report("Opened "+reportedPR, reportedPR)
	s.reportedLinks.runs.Wait()
	if set := todoRepositoriesOf(t, s, todo.ID); len(set.Repositories) != 1 || len(set.Repositories[0].Links) != 1 {
		t.Fatalf("%+v", set)
	}
}

func TestReportedLinkOwnershipIsRecheckedAfterProviderLookup(t *testing.T) {
	s, f, _ := acmeService(t, todoTestDB(t))
	slow := slowAcme{f, make(chan struct{})}
	s.repoExt = slow
	todo, err := s.createPlainTodo(t.Context(), "Reported work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://code.acme.test/team/app/reviews/4"
	repositoryReporter(t, s, todo.ID)("Opened "+url, url)
	a := todoNow(s, todo.ID).Agents[0]
	removed := placeDo(s, "", "DELETE", "/api/v1/todos/"+todo.ID+"/agents/"+a.MachineID+"/"+a.SessionID, "")
	close(slow.release)
	s.reportedLinks.runs.Wait()
	if removed.Code != 200 {
		t.Fatalf("unlink: %d %s", removed.Code, removed.Body)
	}
	if got := f.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
		t.Fatal("the provider did not finish resolving the report", got)
	}
	if set := todoRepositoriesOf(t, s, todo.ID); len(set.Repositories) != 0 {
		t.Fatalf("the released Agent's report added a repository: %+v", set)
	}
}

// A provider is sent only plain links on its own hosts: anything else is refused before any call.
func TestProviderLinksSendOnlyPlainLinks(t *testing.T) {
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
	f.asked()
	path := "/api/v1/todos/" + created.Todo.ID + "/repositories/" + repo.ID + "/links"
	for link, code := range map[string]string{
		`{"url":"https://me:s3cret@code.acme.test/team/app/reviews/1"}`:               "link_repository_mismatch",
		`{"url":"https://elsewhere.example.test/private/doc?access_token=abc"}`:       "link_repository_mismatch",
		`{"url":"https://elsewhere.example.test/team/app/reviews/1"}`:                 "link_repository_mismatch",
		`{"url":"http://code.acme.test/team/app/reviews/2#part"}`:                     "link_repository_mismatch",
		`{"url":"https://code.acme.test/team/app/reviews/2?tab=files"}`:               "link_repository_mismatch",
		`{"url":"ssh://git@code.acme.test/team/app/reviews/2"}`:                       "link_repository_mismatch",
		`{"url":"https://code.acme.test/team/app/` + strings.Repeat("a", 2048) + `"}`: "link_repository_mismatch",
		`{"kind":"pull_request","number":"012"}`:                                      "invalid",
		`{"kind":"issue","number":"1a"}`:                                              "invalid",
		`{"kind":"issue","number":"123456789012345678901"}`:                           "invalid",
	} {
		w := placeDo(s, "", "PUT", path, `{"expected_revision":1,"links":[`+link+`]}`)
		if w.Code == 200 || !strings.Contains(w.Body.String(), `"task_repositories_`+code+`"`) {
			t.Errorf("%s: %d %s", link, w.Code, w.Body)
		}
	}
	args, _ := json.Marshal(map[string]any{"id": created.Todo.ID, "urls": []string{"https://code.acme.test/team/app/reviews/5?token=abc"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err == nil {
		t.Fatal("links_add took a link with a query")
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("the provider was sent", got)
	}
}
