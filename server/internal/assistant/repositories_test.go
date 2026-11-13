package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

func repositoryService(t *testing.T, store Store) (*Service, *int) {
	t.Helper()
	s := New(Options{Hub: newFakeHub(), Store: func() Store { return store }})
	calls := new(int)
	s.repositoryCheck = func(context.Context, repository.Identity) error { *calls++; return nil }
	return s, calls
}

const repoBase = "/api/v1/assistant/repositories"
const repoExample = `{"url":"https://gitlab.example.com/example/subgroup/project.git","provider":"gitlab","name":"Example","aliases":["app"],"purpose":"Demo project"}`

func TestRepositorySettingsIsolationAndVerificationGate(t *testing.T) {
	st := ruleStore{&memStore{}}
	s, calls := repositoryService(t, st)
	other, otherCalls := repositoryService(t, ruleStore{&memStore{}})
	other.id = "work"
	w := placeDo(s, "", "POST", repoBase, repoExample)
	var saved struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Repository.ID == "" || saved.Repository.VerifiedAt.IsZero() {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	id := saved.Repository.ID
	if *calls != 1 {
		t.Fatal("creation checks once")
	}
	if w := placeDo(s, "", "POST", repoBase, strings.ReplaceAll(repoExample, "https://gitlab.example.com/example/subgroup/project.git", "git@gitlab.example.com:example/subgroup/project.git")); w.Code != 409 || *calls != 1 {
		t.Fatalf("duplicate is refused without recheck: %d %s, calls %d", w.Code, w.Body, *calls)
	}
	next, nextCalls := repositoryService(t, st)
	w = placeDo(next, "", "PUT", repoBase+"/"+id, strings.Replace(repoExample, `"Example"`, `"Renamed"`, 1))
	if w.Code != 200 || *nextCalls != 0 || !strings.Contains(w.Body.String(), "Renamed") || !strings.Contains(w.Body.String(), saved.Repository.VerifiedAt.Format("2006-01-02T15:04:05")) {
		t.Fatalf("descriptive edits persist without rechecking: %d %s", w.Code, w.Body)
	}
	if w := placeDo(other, "", "GET", repoBase+"/"+id, ""); w.Code != 404 {
		t.Fatalf("another assistant: %d", w.Code)
	}
	if w := placeDo(other, "", "POST", repoBase, repoExample); w.Code != 200 || *otherCalls != 1 {
		t.Fatalf("independent binding: %d %s", w.Code, w.Body)
	}
	next.repositoryCheck = func(context.Context, repository.Identity) error { return &repository.CheckError{Code: "unavailable"} }
	changed := strings.ReplaceAll(repoExample, "subgroup/project", "subgroup/changed")
	if w := placeDo(next, "", "PUT", repoBase+"/"+id, changed); w.Code != 422 {
		t.Fatalf("unverified change: %d %s", w.Code, w.Body)
	}
	if w := placeDo(next, "", "GET", repoBase+"/"+id, ""); !strings.Contains(w.Body.String(), "subgroup/project") {
		t.Fatal("failed check overwrote binding", w.Body)
	}
	if w := placeDo(next, "", "POST", repoBase, changed); w.Code != 422 {
		t.Fatalf("unverified creation: %d", w.Code)
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		path := repoBase
		if method != "POST" {
			path += "/" + id
		}
		if w := placeDo(s, "read", method, path, repoExample); w.Code != 403 {
			t.Errorf("%s read-only: %d", method, w.Code)
		}
	}
	for _, bad := range []string{
		`{"url":"https://token@code.example.com/example/project"}`,
		`{"url":"https://github.com/example/project","verified_at":"2026-01-01T00:00:00Z"}`,
		`{"url":"https://github.com/example/project","manager_id":"other"}`,
		repoExample + `{}`,
	} {
		if w := placeDo(s, "", "POST", repoBase, bad); w.Code != 400 {
			t.Errorf("invalid: %d %s", w.Code, w.Body)
		}
	}
}

func TestRepositoryUnbindAndReceiptScopedUndo(t *testing.T) {
	s, calls := repositoryService(t, ruleStore{&memStore{}})
	w := placeDo(s, "", "POST", repoBase, repoExample)
	var saved struct{ Repository RepositoryBinding }
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	path := repoBase + "/" + saved.Repository.ID
	w = placeDo(s, "", "DELETE", path, "")
	var removed struct{ Receipt string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &removed) != nil || removed.Receipt == "" {
		t.Fatalf("unbind: %d %s", w.Code, w.Body)
	}
	other, _ := repositoryService(t, ruleStore{&memStore{}})
	other.id = "work"
	if w := placeDo(other, "", "POST", path+"/restore", `{"receipt":"`+removed.Receipt+`"}`); w.Code != 409 {
		t.Fatalf("another manager restored a foreign receipt: %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "", "GET", path, ""); w.Code != 404 {
		t.Fatal("unbound appears in reads")
	}
	if w := placeDo(s, "", "POST", path+"/restore", `{"receipt":"stale"}`); w.Code != 409 {
		t.Fatal("stale undo accepted")
	}
	w = placeDo(s, "", "POST", path+"/restore", `{"receipt":"`+removed.Receipt+`"}`)
	if w.Code != 200 || *calls != 1 || !strings.Contains(w.Body.String(), saved.Repository.ID) {
		t.Fatalf("undo without re-probe: %d %s", w.Code, w.Body)
	}
	w = placeDo(s, "", "POST", path+"/restore", `{"receipt":"`+removed.Receipt+`"}`)
	if w.Code != 409 {
		t.Fatal("receipt replay accepted")
	}
}

func TestRepositoryStorageFailuresStayFailures(t *testing.T) {
	s, calls := repositoryService(t, nil)
	if w := placeDo(s, "", "POST", repoBase, repoExample); w.Code != 503 || *calls != 0 {
		t.Fatalf("no store: %d calls %d", w.Code, *calls)
	}
	s, _ = repositoryService(t, repositoryFailStore{Store: ruleStore{&memStore{}}})
	if w := placeDo(s, "", "POST", repoBase, repoExample); w.Code != 503 {
		t.Fatalf("read failure: %d %s", w.Code, w.Body)
	}
}

type repositoryFailStore struct{ Store }

func (repositoryFailStore) Setting(context.Context, string, any) (bool, error) {
	return false, errors.New("synthetic private store detail")
}

func TestRepositoryPostgresPersistenceAndIsolation(t *testing.T) {
	dsn := newDatabase(t)
	database, err := db.Open(t.Context(), dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	s, calls := repositoryService(t, database)
	w := placeDo(s, "", "POST", repoBase, repoExample)
	var saved struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	database.Close()
	database, err = db.Open(t.Context(), dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _ = repositoryService(t, database)
	if w := placeDo(s, "", "GET", repoBase+"/"+saved.Repository.ID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), saved.Repository.ID) || *calls != 1 {
		t.Fatalf("reopen: %d %s", w.Code, w.Body)
	}
	otherDB, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(otherDB.Close)
	other, _ := repositoryService(t, otherDB)
	if w := placeDo(other, "", "GET", repoBase+"/"+saved.Repository.ID, ""); w.Code != 404 {
		t.Fatalf("cross-manager database: %d", w.Code)
	}
}

type repositoryCASStore struct {
	Store
	saved bool
	err   error
}

func (s repositoryCASStore) CompareSetting(context.Context, string, int64, any) (bool, error) {
	return s.saved, s.err
}

func TestRepositoryPreviewAndWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		saved  bool
		err    error
		status int
	}{
		{false, nil, 409}, {false, errors.New("synthetic private write detail"), 503},
	} {
		s, calls := repositoryService(t, repositoryCASStore{ruleStore{&memStore{}}, tc.saved, tc.err})
		if w := placeDo(s, "", "POST", repoBase+"/preview", repoExample); w.Code != 200 || *calls != 0 {
			t.Fatalf("preview: %d %s", w.Code, w.Body)
		}
		w := placeDo(s, "", "POST", repoBase, repoExample)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("failed write: %d %s", w.Code, w.Body)
		}
		if w := placeDo(s, "", "GET", repoBase, ""); strings.Contains(w.Body.String(), "repo_") {
			t.Fatal("failed write became visible", w.Body)
		}
	}
}

func TestRepositoryPreviewCheckAndStorageUseSameIdentity(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/example/project/pull/5581?diff=split#review",
		"git@" + "github.com:example/project.git",
		"ssh://git@" + "ssh.github.com:443/example/project.git",
		"https://gitlab.com/group/sub/project/-/merge_requests/1#note",
		"http://code.example.com:8080/group/sub/project/-/tree/main",
	} {
		t.Run(remote, func(t *testing.T) {
			provider := ""
			if strings.Contains(remote, "code.example.com") {
				provider = "gitlab"
			}
			body, _ := json.Marshal(repositoryInput{URL: remote, Provider: provider})
			st := ruleStore{&memStore{}}
			s, calls := repositoryService(t, st)
			w := placeDo(s, "", "POST", repoBase+"/preview", string(body))
			var preview struct{ Identity repository.Identity }
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || *calls != 0 {
				t.Fatalf("static preview: %d %s", w.Code, w.Body)
			}
			s.repositoryCheck = func(_ context.Context, id repository.Identity) error {
				*calls++
				if id != preview.Identity {
					t.Fatalf("check identity differs from preview: %+v", id)
				}
				return nil
			}
			w = placeDo(s, "", "POST", repoBase, string(body))
			var saved struct{ Repository RepositoryBinding }
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Repository.Identity != preview.Identity || *calls != 1 {
				t.Fatalf("save: %d %s", w.Code, w.Body)
			}
			doc, err := s.repositoryDocument(context.Background())
			if err != nil || len(doc.Repositories) != 1 || doc.Repositories[0].Identity != preview.Identity {
				t.Fatalf("stored identity: %+v %v", doc, err)
			}
		})
	}
}

func TestRepositoryFailedChecksNeverPersist(t *testing.T) {
	for _, code := range []string{"timeout", "canceled", "network", "rate_limited", "unsafe_target"} {
		t.Run(code, func(t *testing.T) {
			s, _ := repositoryService(t, ruleStore{&memStore{}})
			s.repositoryCheck = func(context.Context, repository.Identity) error { return &repository.CheckError{Code: code} }
			if w := placeDo(s, "", "POST", repoBase, repoExample); w.Code != 422 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			doc, err := s.repositoryDocument(context.Background())
			if err != nil || len(doc.Repositories) != 0 || doc.Revision != 0 {
				t.Fatalf("failed check changed storage: %+v %v", doc, err)
			}
		})
	}
}
