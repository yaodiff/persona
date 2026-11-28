package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// acmeRenamedID is Acme Code's provider id after its extension's market rename from acme-code to acme-hub.
const acmeRenamedID = "ext:acme-hub/acme"

// renamedAcme is Acme Code after that rename, as the extensions' manager gives it: a lookup by the earlier id finds it,
// and every listing and answer carries the current id.
type renamedAcme struct{ *acmeProvider }

func (p renamedAcme) RepositoryProviders() []repository.ProviderInfo {
	out := p.acmeProvider.RepositoryProviders()
	for i := range out {
		if out[i].ID == acmeProviderID {
			out[i].ID = acmeRenamedID
		}
	}
	return out
}

func (p renamedAcme) RepositoryProvider(id string) (repository.ProviderInfo, bool) {
	if id == acmeRenamedID {
		id = acmeProviderID
	}
	info, ok := p.acmeProvider.RepositoryProvider(id)
	info.ID = acmeRenamedID
	return info, ok
}

func (p renamedAcme) RepositoryProviderForHost(host string) (repository.ProviderInfo, bool) {
	info, ok := p.acmeProvider.RepositoryProviderForHost(host)
	info.ID = acmeRenamedID
	return info, ok
}

func (p renamedAcme) RepositoryParse(ctx context.Context, provider, remote string) (repository.Parsed, error) {
	parsed, err := p.acmeProvider.RepositoryParse(ctx, provider, remote)
	if err == nil {
		parsed.Identity.Provider = acmeRenamedID
	}
	return parsed, err
}

// A repository saved before its extension was renamed stays one repository: the catalog does not save it twice, its
// details stay editable under the provider's current id, and to-do links land on it.
func TestRepositoriesSavedBeforeAnExtensionRename(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, _ := acmeService(t, database)
	repo := savedRepository(t, s, `{"url":"https://code.acme.test/team/app"}`)
	s.repoExt = renamedAcme{f}
	f.asked()
	if w := placeDo(s, "", "POST", repoBase, `{"url":"https://code.acme.test/team/app"}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"repository_duplicate"`) {
		t.Fatalf("saved again: %d %s", w.Code, w.Body)
	}
	if got := f.asked(); !slices.Equal(got, []string{"parse"}) {
		t.Fatal("checked again", got)
	}
	if _, err := s.toolRepositoryAdd(t.Context(), "server", "c1", json.RawMessage(`{"url":"https://code.acme.test/team/app/reviews/3"}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "repository_duplicate: ") || len(s.pendingQuestions()) != 0 {
		t.Fatal("tool:", err)
	}
	f.asked()
	edit := func(body string) RepositoryBinding {
		t.Helper()
		w := placeDo(s, "", "PUT", repoBase+"/"+repo.ID, body)
		var got struct{ Repository RepositoryBinding }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Repository.Identity != repo.Identity ||
			!got.Repository.VerifiedAt.Equal(repo.VerifiedAt) {
			t.Fatalf("edit %s: %d %s", body, w.Code, w.Body)
		}
		return got.Repository
	}
	// Its own address under the provider's current id needs no call.
	if got := edit(`{"url":"https://code.acme.test/team/app","provider":"` + acmeRenamedID + `","name":"Renamed"}`); got.Name != "Renamed" {
		t.Fatalf("%+v", got)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatal("a metadata edit asked", got)
	}
	// Another address of it is asked about, and is still the saved repository (not checked again).
	edit(`{"url":"git@ssh.acme.test:team/app.git","name":"Again"}`)
	if got := f.asked(); !slices.Equal(got, []string{"parse"}) {
		t.Fatal("re-parse asked", got)
	}
	if doc, err := s.repositoryDocument(t.Context()); err != nil || len(doc.Repositories) != 1 {
		t.Fatalf("%+v %v", doc, err)
	}
	// A to-do's new link on it lands on the saved repository, with its own transport, not on a second, unlisted one.
	todo, err := s.createPlainTodo(t.Context(), "Renamed work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"id": todo.ID, "urls": []string{"https://code.acme.test/team/app/reviews/12"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	const reportedPR = "https://code.acme.test/team/app/reviews/13"
	repositoryReporter(t, s, todo.ID)("<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr "+reportedPR+"\n</closeout>", reportedPR)
	s.reportedLinks.runs.Wait()
	set := todoRepositoriesOf(t, s, todo.ID)
	if len(set.Repositories) != 1 || set.Repositories[0].RepoID != repo.ID || set.Repositories[0].CatalogState != "current" ||
		set.Repositories[0].SourceSnapshot.Identity != repo.Identity || set.Repositories[0].SourceSnapshot.CloneURL != repo.CloneURL ||
		set.Repositories[0].SourceSnapshot.TransportOrigin != "catalog" || len(set.Repositories[0].Links) != 2 ||
		set.Repositories[0].ProviderState != repository.ProviderAvailable {
		t.Fatalf("%+v", set)
	}
	// Removed, saved anew under the current id, the earlier one can't be restored beside it.
	w := placeDo(s, "", "DELETE", repoBase+"/"+repo.ID, "")
	var removed struct{ Receipt string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &removed) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if again := savedRepository(t, s, `{"url":"https://code.acme.test/team/app"}`); again.Provider != acmeRenamedID {
		t.Fatalf("%+v", again)
	}
	if w := placeDo(s, "", "POST", repoBase+"/"+repo.ID+"/restore", `{"receipt":"`+removed.Receipt+`"}`); w.Code != 409 {
		t.Fatalf("restored a second copy: %d %s", w.Code, w.Body)
	}
}

// A repository a to-do first gets through a provider's link is cloned from where the provider said.
func TestProviderLinkedRowsKeepTheProvidersCloneAddress(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _, _ := acmeService(t, database)
	todo, err := s.createPlainTodo(t.Context(), "Linked work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"id": todo.ID, "urls": []string{"https://code.acme.test/team/app/reviews/15"}})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	set := todoRepositoriesOf(t, s, todo.ID)
	if len(set.Repositories) != 1 || !strings.HasPrefix(set.Repositories[0].RepoID, "linked_") {
		t.Fatalf("%+v", set)
	}
	if src := set.Repositories[0].SourceSnapshot; src.CloneURL != "git@ssh.acme.test:team/app.git" || src.TransportOrigin != "provider" ||
		src.ProviderName != "Acme Code" || src.Identity.CanonicalURL != "https://code.acme.test/team/app" {
		t.Fatalf("%+v", src)
	}
}

// The approval card of an extension provider's repository shows what approving saves: the provider, the name it
// chose and the clone address; a built-in one still shows the Agent's input as it was.
func TestRepositoryAddCardShowsWhatAProviderChose(t *testing.T) {
	s, _, _ := acmeService(t, ruleStore{&memStore{}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	allow := true
	add := func(args string) (json.RawMessage, RepositoryBinding) {
		t.Helper()
		var shown json.RawMessage
		done := answerRepositoryCard(t, s, proto.Resolution{Allow: &allow}, func(card proto.Interaction) { shown = card.ToolInput })
		out := repositoryAddCall(ctx, s, "repository_add", args)
		<-done
		var saved RepositoryBinding
		if out.IsError || json.Unmarshal([]byte(out.Content[0].Text), &saved) != nil {
			t.Fatalf("add %s: %+v", args, out)
		}
		return shown, saved
	}
	shown, saved := add(`{"url":"https://code.acme.test/team/app/reviews/3","purpose":"Demo"}`)
	var card map[string]any
	if err := json.Unmarshal(shown, &card); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"url": "https://code.acme.test/team/app", "provider": acmeProviderID, "provider_name": "Acme Code", "name": "App",
		"aliases": nil, "purpose": "Demo", "clone_url": "git@ssh.acme.test:team/app.git"}
	if len(card) != len(want) {
		t.Fatalf("card %s", shown)
	}
	for k, v := range want {
		if card[k] != v {
			t.Errorf("card %s: %v, want %v", k, card[k], v)
		}
	}
	if saved.Name != "App" || saved.CloneURL != "git@ssh.acme.test:team/app.git" || saved.ProviderName != "Acme Code" || saved.Provider != acmeProviderID {
		t.Fatalf("saved %+v", saved)
	}
	shown, _ = add(repoExample)
	var in repositoryInput
	if err := json.Unmarshal([]byte(repoExample), &in); err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(in); string(shown) != string(raw) {
		t.Fatalf("built-in card %s, want %s", shown, raw)
	}
}
