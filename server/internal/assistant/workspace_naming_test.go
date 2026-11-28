package assistant

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingSettingsWireRoundTrip(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub()
	s := newServiceOn(t, f, database)
	check := func(t *testing.T, s *Service, method, body string, saved, effective map[string]string) {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/assistant", strings.NewReader(body))
		r.Header.Set(assistantwire.ManagerHeader, s.managerIdentity())
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
			t.Fatal(err)
		}
		system := workspacenaming.Defaults()
		var gotSystem workspacenaming.Rules
		if err := json.Unmarshal(wire["system_defaults"], &gotSystem); err != nil || gotSystem != system {
			t.Fatalf("system_defaults = %s; want %+v (error %v)", wire["system_defaults"], system, err)
		}
		for field, want := range map[string]map[string]string{"workspace_rules": saved, "effective_workspace_rules": effective} {
			var got map[string]string
			if err := json.Unmarshal(wire[field], &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s %s = %s; want %+v (error %v)", method, field, wire[field], want, err)
			}
		}
		t.Logf("%s: workspace_rules=%s effective_workspace_rules=%s", method, wire["workspace_rules"], wire["effective_workspace_rules"])
	}
	defaults := map[string]string{"base_directory": "{home}/workspace", "task_directory_template": "task-{task-id}", "branch_template": "persona/{task-id}"}
	check(t, s, "GET", "", map[string]string{}, defaults)
	for _, tc := range []struct {
		name, patch      string
		saved, effective map[string]string
	}{
		{"partial", `{"workspace_rules":{"branch_template":"custom/{task-id}"}}`, map[string]string{"branch_template": "custom/{task-id}"}, map[string]string{"base_directory": "{home}/workspace", "task_directory_template": "task-{task-id}", "branch_template": "custom/{task-id}"}},
		{"omitted preserves", `{"notes":"Example note"}`, map[string]string{"branch_template": "custom/{task-id}"}, map[string]string{"base_directory": "{home}/workspace", "task_directory_template": "task-{task-id}", "branch_template": "custom/{task-id}"}},
		{"explicit default stays saved", `{"workspace_rules":{"base_directory":"{home}/workspace"}}`, map[string]string{"base_directory": "{home}/workspace", "branch_template": "custom/{task-id}"}, map[string]string{"base_directory": "{home}/workspace", "task_directory_template": "task-{task-id}", "branch_template": "custom/{task-id}"}},
		{"field null inherits", `{"workspace_rules":{"branch_template":null}}`, map[string]string{"base_directory": "{home}/workspace"}, defaults},
		{"whole null inherits", `{"workspace_rules":null}`, map[string]string{}, defaults},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check(t, s, "PUT", tc.patch, tc.saved, tc.effective)
			// Read the serialized database value, then GET through a fresh service/cache.
			var persisted map[string]json.RawMessage
			if found, err := database.Setting(t.Context(), settingsKey, &persisted); err != nil || !found {
				t.Fatal("saved settings missing", found, err)
			}
			var rules map[string]string
			if err := json.Unmarshal(persisted["workspace_rules"], &rules); err != nil || !reflect.DeepEqual(rules, tc.saved) {
				t.Fatalf("persisted rules %s; want %+v (error %v)", persisted["workspace_rules"], tc.saved, err)
			}
			s = newServiceOn(t, f, database)
			check(t, s, "GET", "", tc.saved, tc.effective)
		})
		if t.Failed() {
			return
		}
	}
}

func TestWorkspaceNamingSampleUsesRepositoryDefaultAndDescribesOldNode(t *testing.T) {
	s, _, calls := namingHTTPFixture(t)
	path := "/api/v1/assistant/workspace-naming/preview"
	// The node fixture resolves the plan without the added metadata, like an older node.
	w := placeDo(s, "", "POST", path,
		`{"machine_id":"box","layout":"root","workspace_rules":{"branch_template":"persona/{base-branch}/{task-id}"}}`)
	var preview workspacenaming.Preview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || calls.Load() != 1 {
		t.Fatal(w.Code, w.Body)
	}
	if !preview.Sample || preview.Status != "incomplete" || preview.Plan != nil || preview.SystemDefaults == nil || *preview.SystemDefaults != workspacenaming.Defaults() {
		t.Fatal(w.Body)
	}
	if len(preview.BranchSources) != 1 || preview.BranchSources[0] != (workspacenaming.BranchSource{RepoID: "web", Kind: repositorywire.BranchRepositoryDefault}) ||
		!slices.ContainsFunc(preview.Diagnostics, func(d workspacenaming.Problem) bool {
			return d.Code == "field_unavailable" && d.Field == "branch_template"
		}) {
		t.Fatal(w.Body)
	}
}

func namingHTTPFixture(t *testing.T) (*Service, *repositorystore.Store, *atomic.Int32) {
	t.Helper()
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	f := newFakeHub()
	f.sessions["box"] = nil
	calls := new(atomic.Int32)
	f.nodes = map[string]nodeAnswer{"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
		calls.Add(1)
		var source workspacenaming.Source
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, err
		}
		preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example"})
		preview.SystemDefaults, preview.BranchSources = nil, nil
		return preview, nil
	}}
	return newServiceOn(t, f, database), repositorystore.New(database), calls
}

func TestWorkspaceNamingBodiesRejectTrailingValuesWithoutEffects(t *testing.T) {
	s, store, calls := namingHTTPFixture(t)
	todo, err := s.createPlainTodo(t.Context(), "Example task", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/todos/" + todo.ID
	routes := []struct{ method, path, body string }{
		{"PUT", path + "/workspace-rules", `{"expected_revision":0,"workspace_rules":{"branch_template":"custom/{task-id}"}}`},
		{"POST", "/api/v1/assistant/workspace-naming/preview", `{"machine_id":"box","layout":"container","selected_repo_ids":[]}`},
		{"POST", path + "/workspace-naming/preview", `{"machine_id":"box","layout":"container","selected_repo_ids":[]}`},
		{"POST", path + "/workspace-naming/plans", `{"machine_id":"box","layout":"container","selected_repo_ids":[],"operation_id":"body-boundary-operation"}`},
	}
	for _, route := range routes {
		for _, tail := range []string{` {}`, ` null`, ` 1`, ` "extra"`, ` []`, ` garbage`} {
			w := placeDo(s, "", route.method, route.path, route.body+tail)
			if w.Code != 400 {
				t.Fatalf("%s with suffix %q: %d %s", route.path, tail, w.Code, w.Body)
			}
		}
	}
	record, err := store.TodoWorkspaceRules(t.Context(), todo.ID)
	if err != nil || record.Revision != 0 || record.Rules != (workspacenaming.Rules{}) || calls.Load() != 0 {
		t.Fatalf("rejected bodies had effects: %+v, node calls %d, error %v", record, calls.Load(), err)
	}
	if plan, err := store.WorkspaceNamingPlan(t.Context(), todo.ID, "body-boundary-operation"); err != nil || plan != nil {
		t.Fatal("rejected body saved a plan", plan, err)
	}
	// Every prefix is a valid request; ordinary trailing whitespace stays accepted.
	for _, route := range routes {
		if w := placeDo(s, "", route.method, route.path, route.body+" \n\t"); w.Code != 200 {
			t.Fatalf("valid %s: %d %s", route.path, w.Code, w.Body)
		}
	}
}

func TestWorkspaceNamingCrossTodoOperationConflictsBeforeNode(t *testing.T) {
	s, _, calls := namingHTTPFixture(t)
	first, err := s.createPlainTodo(t.Context(), "First example task", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.createPlainTodo(t.Context(), "Second example task", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"machine_id":"box","layout":"container","selected_repo_ids":[],"operation_id":"shared-example-operation"}`
	path := "/api/v1/todos/"
	if w := placeDo(s, "", "POST", path+second.ID+"/workspace-naming/plans", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	calls.Store(0)
	w := placeDo(s, "", "POST", path+first.ID+"/workspace-naming/plans", body)
	if w.Code != 409 || calls.Load() != 0 {
		t.Fatalf("cross-task operation: status %d, node calls %d, body %s", w.Code, calls.Load(), w.Body)
	}
	if strings.Contains(w.Body.String(), second.ID) || strings.Contains(w.Body.String(), "Second example task") {
		t.Fatal("conflict exposed another task's data", w.Body)
	}
	w = placeDo(s, "", "GET", path+first.ID+"/workspace-naming/plans/shared-example-operation", "")
	if w.Code != 404 || strings.Contains(w.Body.String(), second.ID) {
		t.Fatal("scoped read exposed another task's plan", w.Code, w.Body)
	}
	if w := placeDo(s, "", "POST", path+second.ID+"/workspace-naming/plans", body); w.Code != 200 || calls.Load() != 0 {
		t.Fatal("same-task retry called Node", w.Code, calls.Load(), w.Body)
	}
}

func TestWorkspaceNamingSettingsPersistAndOldClientsKeepWorking(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub()
	s := newServiceOn(t, f, database)
	put := func(body string, owner bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("PUT", "/api/v1/assistant", strings.NewReader(body))
		if owner {
			r.Header.Set(assistantwire.ManagerHeader, s.managerIdentity())
		}
		s.ServeHTTP(w, r)
		return w
	}
	patch := `{"workspace_rules":{"branch_template":"custom/{task-id}","base_directory":"{home}/projects"}}`
	if w := put(patch, false); w.Code != 400 {
		t.Fatal("unowned settings", w.Code, w.Body)
	}
	if w := put(patch, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := put(`{"notes":"Old client edit","task_workspace_preference":"prefer-new"}`, false); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	restarted := newServiceOn(t, f, database)
	if got := restarted.settings(t.Context()); got.WorkspaceRules.BranchTemplate != "custom/{task-id}" || got.TaskWorkspacePreference != "prefer-new" {
		t.Fatal(got)
	}
	if w := put(`{"workspace_rules":{"branch_template":null}}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := put(`{"workspace_rules":{"task_directory_template":"{branch}"}}`, true); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestWorkspaceNamingAndDispatchPolicySettingsAreAtomic(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub()
	s := newServiceOn(t, f, database)
	put := func(body string, owner bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/v1/assistant", strings.NewReader(body))
		if owner {
			r.Header.Set(assistantwire.ManagerHeader, s.managerIdentity())
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	const policy = `{"rules":[{"conversation_class":"scheduled","deny":["task_plan"]}]}`
	if w := put(`{"notes":"Before","workspace_rules":{"branch_template":"custom/{task-id}"},"dispatch_policy":`+policy+`}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	before, err := json.Marshal(s.settings(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		owner      bool
	}{
		{"invalid policy", `{"notes":"After","workspace_rules":{"branch_template":"changed/{task-id}"},"dispatch_policy":{"rules":[{"conversation_class":"unknown","deny":["task_plan"]}]}}`, true},
		{"invalid naming", `{"notes":"After","workspace_rules":{"task_directory_template":"{branch}"},"dispatch_policy":{"rules":[]}}`, true},
		{"missing manager", `{"notes":"After","workspace_rules":null,"dispatch_policy":{"rules":[]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := put(tc.body, tc.owner); w.Code != 400 {
				t.Fatalf("invalid combined settings: %d %s", w.Code, w.Body)
			}
			restarted := newServiceOn(t, f, database)
			after, err := json.Marshal(restarted.settings(t.Context()))
			if err != nil || string(after) != string(before) {
				t.Fatalf("rejected settings changed persisted values: %s (was %s), error %v", after, before, err)
			}
		})
	}
	if t.Failed() {
		return
	}
	if w := put(`{"workspace_rules":null,"dispatch_policy":{"rules":[]}}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	got := newServiceOn(t, f, database).settings(t.Context())
	if got.WorkspaceRules != (workspacenaming.Rules{}) || got.DispatchPolicy != nil || got.Notes == nil || *got.Notes != "Before" {
		t.Fatalf("clearing both settings did not persist: %+v", got)
	}
}

func TestWorkspaceNamingHTTPOverridesAndStableOperationRetry(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub()
	f.sessions["box"] = nil
	calls := 0
	f.nodes = map[string]nodeAnswer{"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
		calls++
		var source workspacenaming.Source
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, err
		}
		return workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example"}), nil
	}}
	s := newServiceOn(t, f, database)
	todo, err := s.createPlainTodo(t.Context(), "Original title", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/todos/" + todo.ID
	if w := placeDo(s, "", "PUT", path+"/workspace-rules", `{"expected_revision":0,"workspace_rules":{"task_directory_template":"{yyyy}{mmdd}/{task-id}-{task-slug}"}}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	in := `{"machine_id":"box","layout":"container","selected_repo_ids":[],"operation_id":"example-operation"}`
	w := placeDo(s, "", "POST", path+"/workspace-naming/plans", in)
	var plan workspacenaming.Plan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &plan) != nil || plan.Digest == "" {
		t.Fatal(w.Code, w.Body)
	}
	if w := placeDo(s, "", "PATCH", path, `{"title":"Changed title"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := placeDo(s, "", "PUT", path+"/workspace-rules", `{"expected_revision":1,"workspace_rules":null}`); w.Code != 200 {
		t.Fatal("Todo permanently frozen", w.Code, w.Body)
	}
	restarted := newServiceOn(t, f, database)
	w = placeDo(restarted, "", "POST", path+"/workspace-naming/plans", in)
	var retry workspacenaming.Plan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &retry) != nil || retry.Digest != plan.Digest || calls != 1 {
		t.Fatal(w.Code, w.Body, calls)
	}
	if w := placeDo(s, "read", "PUT", path+"/workspace-rules", `{"expected_revision":2,"workspace_rules":null}`); w.Code != 403 {
		t.Fatal("readonly changed", w.Code)
	}
}
