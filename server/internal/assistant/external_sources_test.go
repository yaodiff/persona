package assistant

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

func externalSourceHTTPFixture(t *testing.T) (*Service, *repositorystore.Store) {
	t.Helper()
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store := repositorystore.New(database)
	now := time.Now()
	if err := store.CreateTodo(t.Context(), db.TodoRow{ID: "todo_source", Title: "Work", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(t.Context(), directionsKey, map[string]*direction{"dir_source": {At: now, By: directionByUser}}); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return store }, Logf: testLogf(t)})
	return s, store
}

func TestExternalSourceHTTPBindingsAndPolicies(t *testing.T) {
	s, _ := externalSourceHTTPFixture(t)
	const (
		bindings = "/api/v1/assistant/external-source-bindings"
		policies = "/api/v1/assistant/external-source-policies"
		channel  = `{"provider":"lark"}`
		group    = `{"provider":"lark","container":{"kind":"chat","id":"group"}}`
		topic    = `{"provider":"lark","container":{"kind":"chat","id":"group"},"item":{"kind":"thread","id":"topic"}}`
	)
	channelQuery := "?external_ref=" + url.QueryEscape(channel)
	groupQuery := "?external_ref=" + url.QueryEscape(group)
	topicQuery := "?external_ref=" + url.QueryEscape(topic)
	call := func(method, route, body string, status int, out any) {
		t.Helper()
		w := placeDo(s, "", method, route, body)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s, want %d", method, route, w.Code, w.Body, status)
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatalf("%s %s: %v: %s", method, route, err, w.Body)
			}
		}
	}

	var effective assistantwire.ExternalEffectivePolicy
	call(http.MethodGet, policies+"/effective"+topicQuery, "", http.StatusOK, &effective)
	if *effective.Policy.Mode != "off" || *effective.Policy.NewItemPolicy != "ask_owner" || effective.Provenance.Mode.Source != "default" {
		t.Fatalf("defaults: %+v", effective)
	}
	call(http.MethodPut, policies+channelQuery, `{"mode":"digest","digest_interval_minutes":15}`, http.StatusOK, nil)
	call(http.MethodPut, policies+groupQuery, `{"action_role":"owner"}`, http.StatusOK, nil)
	call(http.MethodPut, policies+topicQuery, `{"new_item_policy":"auto_todo"}`, http.StatusOK, nil)
	var stored assistantwire.ExternalScopedPolicy
	call(http.MethodGet, policies+channelQuery, "", http.StatusOK, &stored)
	if stored.ExternalRef.Provider != "lark" || *stored.Policy.DigestIntervalMinutes != 15 || stored.Policy.ActionRole != nil {
		t.Fatalf("scope read returns sparse policy: %+v", stored)
	}

	const createTodo = `{"external_ref":{"provider":"Feishu","container":{"kind":"CHAT","id":"group"}},"target_kind":"todo","target_id":"todo_source"}`
	const createDirection = `{"external_ref":` + topic + `,"target_kind":"direction","target_id":"dir_source","policy":{"digest_interval_minutes":45}}`
	var todo, direction assistantwire.ExternalBinding
	call(http.MethodPost, bindings, createTodo, http.StatusCreated, &todo)
	call(http.MethodPost, bindings, createDirection, http.StatusCreated, &direction)
	if todo.ID == "" || direction.ID == "" || todo.ExternalRef.Provider != "lark" || todo.ExternalRef.Container.Kind != "chat" {
		t.Fatalf("created bindings: %+v, %+v", todo, direction)
	}
	call(http.MethodPost, bindings, createTodo, http.StatusConflict, nil)
	call(http.MethodPost, bindings, strings.Replace(createTodo, "todo_source", "missing", 1), http.StatusNotFound, nil)
	call(http.MethodPost, bindings, strings.Replace(createDirection, "dir_source", "missing", 1), http.StatusNotFound, nil)

	var list assistantwire.ExternalBindingList
	call(http.MethodGet, bindings+groupQuery+"&provider=FEISHU&target_kind=todo&target_id=todo_source", "", http.StatusOK, &list)
	if len(list.Bindings) != 1 || list.Bindings[0].ID != todo.ID {
		t.Fatalf("filtered list: %+v", list)
	}
	var matches assistantwire.ExternalBindingLookup
	call(http.MethodGet, bindings+"/lookup"+topicQuery, "", http.StatusOK, &matches)
	if len(matches.Path) != 3 || len(matches.Matches) != 2 || matches.Matches[0].BindingID != direction.ID ||
		matches.Matches[1].BindingID != todo.ID || matches.Matches[0].Source != "explicit" {
		t.Fatalf("lookup: %+v", matches)
	}
	var summary assistantwire.ExternalSourceSummary
	call(http.MethodGet, bindings+"/summary?provider=feishu&channel=", "", http.StatusOK, &summary)
	if len(summary.Bindings) != 2 {
		t.Fatalf("settings data flow: %+v", summary)
	}

	effective = assistantwire.ExternalEffectivePolicy{}
	call(http.MethodGet, policies+"/effective"+topicQuery+"&binding_id="+string(direction.ID), "", http.StatusOK, &effective)
	if *effective.Policy.DigestIntervalMinutes != 45 || *effective.Policy.Mode != "digest" || *effective.Policy.ActionRole != "owner" ||
		*effective.Policy.NewItemPolicy != "auto_todo" || effective.Provenance.Mode.Source != "channel" ||
		effective.Provenance.ActionRole.Source != "container" || effective.Provenance.NewItemPolicy.Source != "item" ||
		effective.Provenance.DigestIntervalMinutes.BindingID != direction.ID {
		t.Fatalf("binding preview: %+v", effective)
	}
	call(http.MethodGet, policies+"/effective"+groupQuery+"&binding_id="+string(direction.ID), "", http.StatusNotFound, nil)
	call(http.MethodPut, bindings+"/"+string(todo.ID), strings.Replace(createTodo, "group", "another", 1), http.StatusOK, nil)
	call(http.MethodDelete, bindings+"/"+string(direction.ID), "", http.StatusNoContent, nil)
	call(http.MethodDelete, bindings+"/"+string(direction.ID), "", http.StatusNotFound, nil)
	call(http.MethodGet, bindings+"/lookup"+topicQuery, "", http.StatusOK, &matches)
	if len(matches.Matches) != 0 || matches.Matches == nil {
		t.Fatalf("stale bindings: %+v", matches)
	}
	call(http.MethodPut, policies+topicQuery, `{}`, http.StatusOK, nil)
	effective = assistantwire.ExternalEffectivePolicy{}
	call(http.MethodGet, policies+"/effective"+topicQuery, "", http.StatusOK, &effective)
	if *effective.Policy.NewItemPolicy != "ask_owner" || effective.Provenance.NewItemPolicy.Source != "default" {
		t.Fatalf("reset did not restore inheritance: %+v", effective)
	}

	for _, invalid := range []string{`null`, createTodo + `{}`, `{"external_ref":` + group + `,"target_kind":"todo","target_id":"todo_source","bad":1}`,
		`{"external_ref":` + group + `,"target_kind":"todo","target_id":"todo_source","target_id":"missing"}`} {
		call(http.MethodPost, bindings, invalid, http.StatusBadRequest, nil)
	}
	call(http.MethodPut, policies+channelQuery, `{"mode":"digest","digest_interval_minutes":-1}`, http.StatusBadRequest, nil)
	call(http.MethodPut, policies+"?external_ref="+url.QueryEscape(`{"provider":"lark","item":{}}`), `{}`, http.StatusBadRequest, nil)
	call(http.MethodPut, policies+"?external_ref="+url.QueryEscape(`{"provider":"lark","item":null}`), `{}`, http.StatusBadRequest, nil)
	call(http.MethodPut, policies+channelQuery+"&external_ref="+url.QueryEscape(group), `{}`, http.StatusBadRequest, nil)
	call(http.MethodPut, policies+channelQuery, strings.Repeat(" ", 65<<10)+`{}`, http.StatusRequestEntityTooLarge, nil)
	call(http.MethodGet, bindings+"/lookup", "", http.StatusBadRequest, nil)
	call(http.MethodGet, bindings+"/summary?channel=account", "", http.StatusBadRequest, nil)
	for _, route := range []string{bindings, bindings + "/summary", bindings + "/lookup" + topicQuery, policies + channelQuery, policies + "/effective" + topicQuery} {
		if w := placeDo(s, "extension", http.MethodGet, route, ""); w.Code != http.StatusForbidden {
			t.Fatalf("extension credential reached %s: %d %s", route, w.Code, w.Body)
		}
	}
	if w := placeDo(s, "read", http.MethodPut, policies+channelQuery, `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("read-only device changed the channel policy: %d %s", w.Code, w.Body)
	}
}

func TestExternalSourceHTTPLookupReturnsRepositoryRole(t *testing.T) {
	s, store := externalSourceHTTPFixture(t)
	input := []repository.LinkInput{{URL: "https://github.com/example/web/pull/17", Role: repository.LinkRoleReference}}
	set, _, err := store.ImportTodoRepositoryLinkDetails(t.Context(), "todo_source", input, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "/api/v1/assistant/external-source-bindings/lookup?external_ref="
	ref := `{"provider":"github","container":{"kind":"repository","id":"Example/Web.git"},"item":{"kind":"pull_request","id":"17"}}`
	w := placeDo(s, "", http.MethodGet, endpoint+url.QueryEscape(ref), "")
	var out assistantwire.ExternalBindingLookup
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Matches) != 1 {
		t.Fatalf("lookup: %d %s", w.Code, w.Body)
	}
	match := out.Matches[0]
	if match.Source != "repository_link" || match.RepositoryRole != "reference" || match.BindingID != "" || match.TargetID != "todo_source" {
		t.Fatalf("repository projection: %+v", match)
	}
	after, err := store.TodoRepositories(t.Context(), "todo_source")
	if err != nil || after.Revision != set.Revision {
		t.Fatalf("lookup changed repository revision: %+v, %v", after, err)
	}
}
