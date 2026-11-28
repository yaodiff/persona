package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTodoRepositoryHTTPJourneyAndLegacyCompatibility(t *testing.T) {
	dsn := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(t.Context(), dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _ := repositoryService(t, database)
	w := placeDo(s, "", "POST", repoBase, `{"url":"https://github.com/example/web"}`)
	var saved struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatal(w.Code, w.Body)
	}
	repoID := saved.Repository.ID
	payload := fmt.Sprintf(`{"title":"Linked work","repository_bindings":{"catalog_revision":1,"repositories":[{"repo_id":%q,"links":[{"kind":"pull_request","number":"7"},{"kind":"pull_request","number":"8"}]}]}}`, repoID)
	w = placeDo(s, "", "POST", "/api/v1/todos", payload)
	var created struct{ Todo Todo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Todo.ID == "" {
		t.Fatal(w.Code, w.Body)
	}
	id := created.Todo.ID
	path := "/api/v1/todos/" + id + "/repositories"
	read := func(service *Service) repository.TaskSet {
		t.Helper()
		w := placeDo(service, "", "GET", path, "")
		var set repository.TaskSet
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &set) != nil {
			t.Fatal(w.Code, w.Body)
		}
		return set
	}
	original := read(s)
	if original.Manager != s.managerIdentity() || len(original.Repositories) != 1 || len(original.Repositories[0].Links) != 2 || original.Repositories[0].RequestedRef.Kind != "default" {
		t.Fatal(original)
	}
	if w := placeDo(s, "", "PATCH", "/api/v1/todos/"+id, `{"notes":"Old client update"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	restarted, _ := repositoryService(t, database)
	if got := read(restarted); len(got.Repositories[0].Links) != 2 || got.Revision != original.Revision {
		t.Fatal("restart/old client lost links", got)
	}
	// Existing task metadata, legacy PR progress and workspace admission remain untouched.
	s.todos.mu.Lock()
	s.todos.todos[id].Stage = "review"
	s.todos.todos[id].PRURL = "https://github.com/example/web/pull/99"
	s.todos.mu.Unlock()
	w = placeDo(s, "", "PUT", path+"/"+repoID+"/links", `{"expected_revision":1,"links":[{"kind":"issue","number":"7"}]}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := placeDo(s, "", "PUT", path, `{"expected_revision":2,"expected_catalog_revision":1,"repositories":[]}`); w.Code != 200 {
		t.Fatal("task associations permanently frozen", w.Code, w.Body)
	}
	todo, _ := s.todoNow(id)
	if todo.Stage != "review" || todo.PRURL != "https://github.com/example/web/pull/99" || len(read(s).Repositories) != 0 {
		t.Fatal("association edit changed task progress", todo)
	}
	// Invalid create rolls back the Todo instead of returning a partial success.
	w = placeDo(s, "", "POST", "/api/v1/todos", strings.Replace(payload, repoID, "foreign_repo", 1))
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	rows, err := database.ListTodos(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal("partial create", len(rows), err)
	}
	for _, bad := range []string{`null`, `{"expected_revision":3,"expected_catalog_revision":1,"repositories":null}`, `{"expected_revision":3,"expected_catalog_revision":1,"repositories":[],"manager":"other"}`, `{"expected_revision":3,"expected_catalog_revision":1,"repositories":[]} {}`} {
		if w := placeDo(s, "", "PUT", path, bad); w.Code != 400 {
			t.Fatal(bad, w.Code, w.Body)
		}
	}
	if w := placeDo(s, "read", "PUT", path, `{"expected_revision":3,"expected_catalog_revision":1,"repositories":[]}`); w.Code != 403 {
		t.Fatal("read-only write", w.Code, w.Body)
	}
	if w := placeDo(s, "", "PUT", path, `{"expected_revision":1,"expected_catalog_revision":1,"repositories":[]}`); w.Code != 409 || !strings.Contains(w.Body.String(), "task_repositories_revision_conflict") {
		t.Fatal(w.Code, w.Body)
	}
	// Zero-repository legacy creation still needs neither a catalog nor a new store capability.
	legacy, _ := repositoryService(t, ruleStore{&memStore{}})
	if w := placeDo(legacy, "", "POST", "/api/v1/todos", `{"title":"Ordinary work"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := placeDo(legacy, "", "POST", "/api/v1/todos", `{"title":"Requires atomic storage","repository_bindings":{"catalog_revision":0,"repositories":[]}}`); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestTodoRepositoryToolsAndTransportCompatibility(t *testing.T) {
	dsn := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(t.Context(), dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _ := repositoryService(t, database)
	w := placeDo(s, "", "POST", repoBase, `{"url":"https://github.com/example/web","clone_url":"git@`+`github.com:example/web.git"}`)
	var saved struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := saved.Repository.ID
	if w := placeDo(s, "", "PUT", repoBase+"/"+id, `{"url":"https://github.com/example/web","name":"Renamed"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "git@"+"github.com:example/web.git") {
		t.Fatal("old catalog client lost transport", w.Code, w.Body)
	}
	args := fmt.Sprintf(`{"title":"Tool work","repository_bindings":{"catalog_revision":2,"repositories":[{"repo_id":%q}]}}`, id)
	if _, err := s.toolTodoCreate(t.Context(), "machine", "conversation", json.RawMessage(args)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTodoCreate(t.Context(), "machine", "conversation", json.RawMessage(args)); err == nil {
		t.Fatal("dedup silently discarded associations")
	}
	rows, err := database.ListTodos(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	result, err := s.toolTodoRepositories(t.Context(), "machine", "conversation", "todo_repositories_get", json.RawMessage(fmt.Sprintf(`{"id":%q}`, rows[0].ID)))
	if err != nil || !strings.Contains(result, "git@"+"github.com:example/web.git") {
		t.Fatal(result, err)
	}
	_, err = s.toolTodoRepositories(t.Context(), "machine", "conversation", "todo_repository_links_set", json.RawMessage(fmt.Sprintf(`{"id":%q,"repo_id":%q,"expected_revision":1,"links":[{"kind":"pull_request","number":"9007199254740993"}]}`, rows[0].ID, id)))
	if err != nil {
		t.Fatal(err)
	}
	if w := placeDo(s, "", "PUT", repoBase+"/"+id, `{"url":"https://github.com/example/other"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	set, err := s.readTodoRepositories(t.Context(), rows[0].ID)
	if err != nil || set.Repositories[0].CatalogState != "identity_changed" || set.Repositories[0].SourceSnapshot.Identity.Path != "example/web" {
		t.Fatal(set, err)
	}
	if w := placeDo(s, "", "DELETE", repoBase+"/"+id, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	set, err = s.readTodoRepositories(t.Context(), rows[0].ID)
	if err != nil || set.Repositories[0].CatalogState != "removed" || len(set.Repositories[0].Links) != 1 {
		t.Fatal(set, err)
	}
}

func TestTodoRepositoryAgentReportsUseUnifiedStore(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f := newQuietServiceOn(t, repositorystore.New(database))
	todo := agentTask(t, s, f)
	text := "Opened https://github.com/example/web/pull/7 and https://github.com/example/web/pull/8; https://gitlab.example.com/team/api/-/merge_requests/7"
	reply := artifactReportProof(f, todo, text, "https://github.com/example/web/pull/7", "https://github.com/example/web/pull/8",
		"https://gitlab.example.com/team/api/-/merge_requests/7")
	s.advanceTaskReplyAt(t.Context(), todo, todo.Agents[0].MachineID, todo.Agents[0].SessionID, text, reply.TS)
	set, err := s.readTodoRepositories(t.Context(), todo.ID)
	if err != nil || len(set.Repositories) != 2 || len(set.Repositories[0].Links) != 2 {
		t.Fatal(set, err)
	}
	if got := todoNow(s, todo.ID); got.Stage != "review" || got.DoneAt != nil {
		t.Fatal("association changed task completion", got)
	}
	args, _ := json.Marshal(map[string]any{"id": todo.ID, "urls": []string{"https://github.com/example/web/issues/12"}, "links": []repository.LinkInput{{URL: "https://github.com/example/web/pull/7", Title: "Keep task context during checkout"}, {URL: "https://github.com/example/web/issues/12", Title: "Retain the current workspace"}}})
	if _, err = s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	text, err = s.toolTodoGet(t.Context(), "", "", json.RawMessage(fmt.Sprintf(`{"id":%q}`, todo.ID)))
	if err != nil || !strings.Contains(text, "issues/12") || !strings.Contains(text, "pull/7") || !strings.Contains(text, "pull/8") || !strings.Contains(text, "Keep task context during checkout") || !strings.Contains(text, "Retain the current workspace") {
		t.Fatal("agent context omitted associations", text, err)
	}
	set, err = s.readTodoRepositories(t.Context(), todo.ID)
	if err != nil || len(set.Repositories[0].Links) != 3 || set.Repositories[0].Links[0].Title != "Keep task context during checkout" {
		t.Fatal(set, err)
	}
	// A title refresh is a real metadata change and invalidates an older editor revision.
	oldRevision := set.Revision
	args, _ = json.Marshal(map[string]any{"id": todo.ID, "links": []repository.LinkInput{{URL: "https://github.com/example/web/pull/7", Title: "Updated checkout title"}}})
	if _, err = s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	set, err = s.readTodoRepositories(t.Context(), todo.ID)
	if err != nil || set.Revision != oldRevision+1 || set.Repositories[0].Links[0].Title != "Updated checkout title" {
		t.Fatal(set, err)
	}
}

func TestAgentRepositoryToolsStayOnTheirOwnTodo(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	e := newTodoEnv(t, database)
	e.f.setSessions("box", proto.Session{SessionID: "linked"}, proto.Session{SessionID: "idle"})
	own, other := e.create("Related work").ID, e.create("Other work").ID
	e.must("PUT", "/api/v1/todos/"+own+"/agents/box/linked", "")
	args, _ := json.Marshal(map[string]any{"id": own, "urls": []string{"https://github.com/example/web/pull/7"}})
	res := e.s.mcpCall(t.Context(), "box", "linked", "todo_repository_links_add", args, false)
	if res.IsError || !strings.Contains(res.Content[0].Text, "pull/7") {
		t.Fatal(res)
	}
	read := func(session, id string) proto.ToolResult {
		return e.s.mcpCall(t.Context(), "box", session, "todo_repositories_get", json.RawMessage(fmt.Sprintf(`{"id":%q}`, id)), false)
	}
	if got := read("linked", own); got.IsError || !strings.Contains(got.Content[0].Text, "pull/7") {
		t.Fatal(got)
	}
	for _, v := range [][2]string{{"linked", other}, {"idle", own}, {"", own}} {
		if got := read(v[0], v[1]); !got.IsError {
			t.Fatal("unscoped repository access", v, got)
		}
	}
	for _, name := range []string{"todo_repository_links_add", "todo_repository_links_set", "todo_repositories_set"} {
		if got := e.s.mcpCall(t.Context(), "box", "linked", name, json.RawMessage(fmt.Sprintf(`{"id":%q}`, other)), false); !got.IsError {
			t.Fatal("foreign write accepted", name, got)
		}
	}
	if got, err := repositorystore.New(database).TodoRepositories(t.Context(), other); err != nil || got.Revision != 0 {
		t.Fatal("foreign Todo changed", got, err)
	}
}

func TestTodoRepositoryReportsNotifyWithoutChangingLegacyProgress(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f := newQuietServiceOn(t, repositorystore.New(database))
	todo := agentTask(t, s, f)
	first := "https://github.com/example/web/pull/7"
	machine, session := todo.Agents[0].MachineID, todo.Agents[0].SessionID
	reply := artifactReportProof(f, todo, first, first)
	s.advanceTaskReplyAt(t.Context(), todo, machine, session, first, reply.TS)
	todo = todoNow(s, todo.ID)
	todosTold(t, s)
	before := strings.Count(f.eventTypes(), "todos")
	reply = artifactReportProof(f, todo, "https://github.com/example/web/pull/8 "+first, first, "https://github.com/example/web/pull/8")
	s.advanceTaskReplyAt(t.Context(), todo, machine, session, reply.Text, reply.TS)
	todosTold(t, s)
	after := strings.Count(f.eventTypes(), "todos")
	if after <= before {
		t.Fatal("new association with unchanged legacy stage/PR sent no update")
	}
	s.advanceTaskReplyAt(t.Context(), todo, machine, session, reply.Text, reply.TS)
	todosTold(t, s)
	if got := strings.Count(f.eventTypes(), "todos"); got != after {
		t.Fatal("replayed links emitted another update", after, got)
	}
	// Metadata limits do not change the old stage/delivery path.
	urls := []string{}
	for n := 9; n <= 56; n++ {
		urls = append(urls, fmt.Sprintf("https://github.com/example/web/pull/%d", n))
	}
	if err := s.importTodoRepositoryLinks(t.Context(), todo.ID, urls, "explicit"); err != nil {
		t.Fatal(err)
	}
	s.setTaskStage(t.Context(), todo.ID, "merge", "https://github.com/example/web/pull/57")
	if got := todoNow(s, todo.ID); got.Stage != "merge" || got.DoneAt != nil {
		t.Fatal("metadata limit changed legacy progress", got)
	}
	todosTold(t, s)
}

func TestTodoRepositoryLegacyPRsImportOnceBeforeFirstEdit(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _ := repositoryService(t, database)
	todo, err := s.createPlainTodo(t.Context(), "Historic work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{todo.ID: {Stage: "review", PR: "https://github.com/example/web/pull/8", PRs: []string{"https://github.com/example/web/pull/7"}}}); err != nil {
		t.Fatal(err)
	}
	s, _ = repositoryService(t, database)
	path := "/api/v1/todos/" + todo.ID + "/repositories"
	if w := placeDo(s, "", "PUT", path, `{"expected_revision":0,"expected_catalog_revision":0,"repositories":[]}`); w.Code != 409 {
		t.Fatal("edit missed historical PRs", w.Code, w.Body)
	}
	set, err := s.readTodoRepositories(t.Context(), todo.ID)
	if err != nil || len(set.Repositories) != 1 || len(set.Repositories[0].Links) != 2 {
		t.Fatal("historical links lost", set, err)
	}
	if w := placeDo(s, "", "PUT", path, fmt.Sprintf(`{"expected_revision":%d,"expected_catalog_revision":0,"repositories":[]}`, set.Revision)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	next, _ := repositoryService(t, database)
	got, err := next.readTodoRepositories(t.Context(), todo.ID)
	if err != nil || len(got.Repositories) != 0 {
		t.Fatal("removed history returned on restart", got, err)
	}
	if old := todoNow(next, todo.ID); old.Stage != "review" || old.PRURL != "https://github.com/example/web/pull/8" {
		t.Fatal("legacy progress changed", old)
	}
	text, err := next.toolTodoGet(t.Context(), "", "", json.RawMessage(fmt.Sprintf(`{"id":%q}`, todo.ID)))
	if err != nil || strings.Contains(text, "/pull/7") || strings.Contains(text, "/pull/8") || !strings.Contains(text, "stage review") {
		t.Fatal("agent context resurrected removed legacy PR", text, err)
	}
	next.advanceTaskReply(t.Context(), todoNow(next, todo.ID), "", "", "Opened https://code.example.com/Example/Api/pull/17.")
	got, err = next.readTodoRepositories(t.Context(), todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range got.Repositories {
		for _, link := range row.Links {
			if link.Role != repository.LinkRoleReference {
				t.Fatal("an unlinked report claimed a self-hosted PR as delivery without ownership", got)
			}
		}
	}
	if old := todoNow(next, todo.ID); old.PRURL != "https://github.com/example/web/pull/8" {
		t.Fatal("an unlinked report overwrote the legacy PR without ownership", old)
	}
}

func TestTodoRepositoryEmptyTodoNeedsNoCatalog(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _ := repositoryService(t, database)
	todo, err := s.createPlainTodo(t.Context(), "Plain work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PutSetting(t.Context(), repository.CatalogKey, "invalid catalog"); err != nil {
		t.Fatal(err)
	}
	text, err := s.toolTodoGet(t.Context(), "", "", json.RawMessage(fmt.Sprintf(`{"id":%q}`, todo.ID)))
	if err != nil || !strings.Contains(text, "Plain work") {
		t.Fatal("ordinary Todo gained a catalog dependency", text, err)
	}
}
