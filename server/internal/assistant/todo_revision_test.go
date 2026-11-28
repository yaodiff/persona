package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func readTodosRevision(s *Service, tag string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/revision", nil)
	if tag != "" {
		r.Header.Set("If-None-Match", tag)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// The pushed read's cheap validator (#13804): a full read leaves a token for exactly the rows and stable views it sent.
// Repeating the same conditional read neither rebuilds the list nor changes the token; every write that can change the
// list does, including marks that deliberately leave updated_at alone.
func TestTodosRevisionCoversFullRowsAndStableViews(t *testing.T) {
	s := manyTodos(40)
	s.todos.mu.Lock()
	s.todos.todos["todo_bench000003"].DoneAt = nil // Someday accepts only an open to-do.
	s.todos.mu.Unlock()
	full := httptest.NewRecorder()
	s.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	tag := full.Header().Get("X-Persona-Todos-Revision")
	if full.Code != http.StatusOK || tag == "" {
		t.Fatalf("full list: %d, revision %q", full.Code, tag)
	}
	if got := readTodosRevision(s, tag); got.Code != http.StatusNotModified || got.Body.Len() != 0 || got.Header().Get("ETag") != tag {
		t.Fatalf("unchanged revision: %d %q %q, want 304 with no body", got.Code, got.Header().Get("ETag"), got.Body.String())
	}
	// A push clears the per-push told book, not the stable classes the revision uses. Repeating that notification alone
	// must not manufacture a full-list change.
	s.todoViewPushed()
	if got := readTodosRevision(s, tag); got.Code != http.StatusNotModified {
		t.Fatalf("the push alone changed the revision: %d %s", got.Code, got.Body.String())
	}

	// Exercise the public mark routes rather than updated_at: seen, restored and Someday all notify without an edit.
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"seen", http.MethodPut, "/api/v1/todos/todo_bench000001/seen", ""},
		{"restored", http.MethodPut, "/api/v1/todos/todo_bench000002/restored", ""},
		{"someday", http.MethodPut, "/api/v1/todos/todo_bench000003/someday", `{"until":"2027-01-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := readTodosRevision(s, "").Header().Get("ETag")
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.body != "" {
				r = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("mark: %d %s", w.Code, w.Body.String())
			}
			after := readTodosRevision(s, "").Header().Get("ETag")
			if after == "" || after == before {
				t.Fatalf("revision stayed %q after %s", before, tc.name)
			}
		})
	}
}

func TestTodosRevisionChangesWithV2SettingsWithoutRows(t *testing.T) {
	e := directionEnv(t, &memStore{})
	s := e.s
	s.tpush.after = e.clock.After
	full := func(t *testing.T, board bool) string {
		t.Helper()
		w := e.must(http.MethodGet, "/api/v1/todos", "")
		var body todosList
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Todos) != 0 || (body.Board != nil) != board {
			t.Fatalf("full list (board = %v): %s, %v", board, w.Body.String(), err)
		}
		tag := w.Header().Get(assistantwire.TodoRevisionHeader)
		if tag == "" {
			t.Fatal("full list has no revision")
		}
		return tag
	}
	tag := full(t, false)
	for _, tc := range []struct {
		name, body string
		board      bool
	}{
		{"enable", `{"todos_v2":true,"expected_todos_v2":false}`, true},
		{"disable", `{"todos_v2":false,"expected_todos_v2":true}`, false},
		{"re-enable", `{"todos_v2":true,"expected_todos_v2":false}`, true},
		{"unfold children", `{"todos_v2_fold_children":false}`, true},
		{"pending review SLA", `{"todos_v2_pending_review_sla_hours":12}`, true},
		{"can wait SLA", `{"todos_v2_can_wait_sla_hours":72}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := s.trevision.writes.Load()
			pushes := todosSent(e.todoEnv)
			w, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("save settings: %d %s", w.Code, w.Body.String())
			}
			got := readTodosRevision(s, tag)
			next := got.Header().Get("ETag")
			if got.Code != http.StatusOK || next == "" || next == tag {
				t.Fatalf("saved setting left revision %q: %d %s", tag, got.Code, got.Body.String())
			}
			if s.trevision.writes.Load() <= writes {
				t.Fatal("saved setting did not bump the write revision")
			}
			if fullTag := full(t, tc.board); fullTag != next {
				t.Fatalf("full list revision = %q, want %q", fullTag, next)
			}
			e.clock.advance(todosSettle)
			if todosSent(e.todoEnv) != pushes+1 {
				t.Fatal("saved setting did not push a list refresh")
			}
			if same := readTodosRevision(s, next); same.Code != http.StatusNotModified {
				t.Fatalf("unchanged settings: %d %s", same.Code, same.Body.String())
			}
			tag = next
		})
	}
}

func TestTodosRevisionChangesWithDirectionNamesWithoutRows(t *testing.T) {
	st := &memStore{}
	e := directionEnv(t, st)
	s := e.s
	s.tpush.after = e.clock.After
	const lead = "conv_lead_a"
	e.title(lead, "Direction title")
	e.leaders(lead)
	setSettings(t, s, func(v *Settings) { v.Main = "conv_main" })
	e.syncLeaders()
	e.clock.advance(todosSettle)
	for _, v2 := range []bool{false, true} {
		setSettings(t, s, func(v *Settings) { v.TodosV2 = &v2 })
		tag := e.must(http.MethodGet, "/api/v1/todos", "").Header().Get(assistantwire.TodoRevisionHeader)
		writes := s.trevision.writes.Load()
		pushes := todosSent(e.todoEnv)
		if v2 {
			if _, err := runJSON(t, s, "conv_main", "direction_name", map[string]any{"id": lead, "name": "Named by the secretary"}); err != nil {
				t.Fatal(err)
			}
		} else {
			e.must(http.MethodPut, "/api/v1/assistant/directions/"+lead+"/name", `{"name":"Named by the owner"}`)
		}
		got := readTodosRevision(s, tag)
		next := got.Header().Get("ETag")
		if tag == "" || got.Code != http.StatusOK || next == "" || tag == next {
			t.Fatalf("rename (v2 = %v) left revision %q: %d %s", v2, tag, got.Code, got.Body.String())
		}
		if s.trevision.writes.Load() <= writes {
			t.Fatal("direction rename did not bump the write revision")
		}
		e.clock.advance(todosSettle)
		if todosSent(e.todoEnv) != pushes+1 {
			t.Fatal("direction rename did not push a list refresh")
		}
	}
	tag := readTodosRevision(s, "").Header().Get("ETag")
	writes := s.trevision.writes.Load()
	st.setFailPut(errors.New("test write failure"))
	put := doTodoHTTP(s, http.MethodPut, "/api/v1/assistant/directions/"+lead+"/name", `{"name":"Unsaved name"}`)
	if put.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed rename: %d %s", put.Code, put.Body.String())
	}
	if got := readTodosRevision(s, tag); got.Code != http.StatusNotModified || s.trevision.writes.Load() != writes {
		t.Fatalf("failed rename invalidated the list: %d %s", got.Code, got.Body.String())
	}
}

func TestTodosRevisionFingerprintsProjectionInputs(t *testing.T) {
	e := directionEnv(t, &memStore{})
	s := e.s
	s.tpush.after = e.clock.After
	const lead = "conv_lead_a"
	e.title(lead, "Direction title")
	e.leaders(lead)
	e.syncLeaders()
	e.clock.advance(todosSettle)
	tag := e.must(http.MethodGet, "/api/v1/todos", "").Header().Get(assistantwire.TodoRevisionHeader)
	if tag == "" {
		t.Fatal("full list has no revision")
	}
	writes := s.trevision.writes.Load()
	changed := func(input string) {
		t.Helper()
		got := readTodosRevision(s, tag)
		next := got.Header().Get("ETag")
		if got.Code != http.StatusOK || next == "" || next == tag {
			t.Fatalf("%s was missing from the fingerprint: %d %s", input, got.Code, got.Body.String())
		}
		tag = next
	}

	// Simulate fresh inputs without the profile and direction writers' notifications.
	for _, setting := range []struct {
		name string
		edit func(*Settings)
	}{
		{"todos_v2", func(v *Settings) { v.TodosV2 = new(true) }},
		{"fold children", func(v *Settings) { v.TodosV2FoldChildren = new(false) }},
		{"pending review SLA", func(v *Settings) { v.TodosV2PendingReviewSLAHours = new(12) }},
		{"can wait SLA", func(v *Settings) { v.TodosV2CanWaitSLAHours = new(72) }},
	} {
		if _, err := s.updateSettings(t.Context(), func(v *Settings) error {
			setting.edit(v)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		changed(setting.name)
	}
	e.title(lead, "Renamed fallback title")
	changed("direction fallback label")
	if s.trevision.writes.Load() != writes {
		t.Fatal("a write notification masked the fingerprint check")
	}
	pushes := todosSent(e.todoEnv)
	e.syncLeaders()
	e.clock.advance(todosSettle)
	if s.trevision.writes.Load() <= writes || todosSent(e.todoEnv) != pushes+1 {
		t.Fatal("direction sync did not invalidate and push the changed fallback label")
	}
}

// A revision read is a lock-and-hash operation, not another projection. Once a full read has produced its token, even a
// forced reload boundary does not make the conditional read rebuild or reload the list just to answer 304.
func TestTodosRevisionUnchangedDoesNotReadTheStore(t *testing.T) {
	s := manyTodos(40)
	full := httptest.NewRecorder()
	s.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	tag := full.Header().Get("X-Persona-Todos-Revision")
	if tag == "" {
		t.Fatal("full list has no revision")
	}
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/revision", nil)
	r.Header.Set("If-None-Match", tag)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Fatalf("revision rebuilt or reloaded the list: %d %s", w.Code, w.Body.String())
	}
}

func TestTodosRevisionFullListBodyStaysComplete(t *testing.T) {
	s := manyTodos(4)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	if w.Code != http.StatusOK || len(w.Body.Bytes()) < 1000 {
		t.Fatalf("legacy full list: %d, %d bytes", w.Code, w.Body.Len())
	}
	for _, field := range []string{`"brief"`, `"lineage"`, `"view"`, `"notes"`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Errorf("legacy full list lost %s", field)
		}
	}
}

func TestTodosRevisionChangesWithBrief(t *testing.T) {
	s := manyTodos(1)
	full := httptest.NewRecorder()
	s.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	tag := full.Header().Get("X-Persona-Todos-Revision")
	if tag == "" {
		t.Fatal("full list has no revision")
	}
	_, changed, err := s.changeBrief(t.Context(), "brief_bench000000", func(tb *taskBrief) bool {
		tb.st.Log = append(tb.st.Log, BriefEvent{Kind: "scope", Change: "cover the narrow case too"})
		return true
	})
	if err != nil || !changed {
		t.Fatalf("brief change: changed %v, %v", changed, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/revision", nil)
	r.Header.Set("If-None-Match", tag)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("ETag") == tag {
		t.Fatalf("brief change left revision %q: %d", tag, w.Code)
	}
}
