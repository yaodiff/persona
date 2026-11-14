package hub

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// Sessions by to-do (#6662): a session's open to-do (row.Todo, which the manager sets for an active link only) is what
// todo=<id> selects and the counts' todo map counts; todo=none selects the sessions on none, a done to-do's included.
func TestRuntimeSessionsByTodo(t *testing.T) {
	h := newTestHub(t)
	catalogMachine(h, "mac", nil,
		proto.Session{SessionID: "a1", Todo: &proto.TodoRef{ID: "todo_a", Title: "Ship 1.3"}},
		proto.Session{SessionID: "a2", Todo: &proto.TodoRef{ID: "todo_a", Title: "Ship 1.3"}, State: "needs_you"},
		proto.Session{SessionID: "a3"},
	)
	done := proto.Session{SessionID: "b1"}
	done.SetCompletedTodo(&proto.TodoRef{ID: "todo_done", Title: "Done already"})
	catalogMachine(h, "pc", nil, done, proto.Session{SessionID: "b2", Todo: &proto.TodoRef{ID: "todo_b", Title: "Review"}})

	ids := func(query string) []string {
		t.Helper()
		var out []string
		for _, s := range runtimePage(t, h, "summary=1&"+query).Sessions {
			out = append(out, s.MachineID+"/"+s.SessionID)
		}
		slices.Sort(out)
		return out
	}
	for query, want := range map[string][]string{
		"todo=todo_a":                 {"mac/a1", "mac/a2"},
		"todo=todo_b":                 {"pc/b2"},
		"todo=none":                   {"mac/a3", "pc/b1"},
		"todo=todo_a&state=needs_you": {"mac/a2"},
		"todo=none&machine_id=pc":     {"pc/b1"},
		"todo=todo_unknown":           nil,
	} {
		if got := ids(query); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", query, got, want)
		}
	}

	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?counts=1", nil))
	var counts sessionlist.Counts
	if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil || counts.Todo["todo_a"] != 2 || counts.Todo["todo_b"] != 1 || counts.Todo[""] != 2 || len(counts.Todo) != 3 {
		t.Fatalf("counts: %s %v", w.Body, err)
	}

	for _, bad := range []string{"todo=", "todo=a&todo=b"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+bad, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if !slices.Contains(h.features(), featureSessionsTodo) || slices.Contains(proto.Features, featureSessionsTodo) {
		t.Error("sessions_todo must be advertised by the hub")
	}
}

// A to-do-only row shares the optional fold allocation without changing the row's own state or background wait under
// roots=1. Its Fold carries the same State and BackgroundWaiting that a nil Fold would mean.
func TestRuntimeSessionsTodoKeepsOwnFoldState(t *testing.T) {
	h := newTestHub(t)
	catalogMachine(h, "mac", nil,
		proto.Session{SessionID: "waiting", State: "working", BackgroundWaiting: true, Todo: &proto.TodoRef{ID: "todo_a"}},
		proto.Session{SessionID: "active", State: "working", Todo: &proto.TodoRef{ID: "todo_a"}},
	)
	for query, want := range map[string]string{"waiting=0": "active", "waiting=1": "waiting"} {
		page := runtimePage(t, h, "summary=1&roots=1&todo=todo_a&"+query)
		if len(page.Sessions) != 1 || page.Sessions[0].SessionID != want {
			t.Errorf("%s: %+v, want only %s", query, page.Sessions, want)
		}
	}
}

// The sidebar's quick filters on the paged list (#6662): state=working with waiting=1 the sessions only waiting in the
// background, waiting=0 the others; exclude_pane_source leaves one origin out (the hidden botmux sessions).
func TestRuntimeSessionsQuickFilters(t *testing.T) {
	h := newTestHub(t)
	catalogMachine(h, "mac", nil,
		proto.Session{SessionID: "w1", State: "working", PaneID: "w1:p1"},
		proto.Session{SessionID: "w2", State: "working", BackgroundWaiting: true, PaneID: "w2:p1"},
		proto.Session{SessionID: "lark", State: "idle", PaneID: "botmux:x"},
	)
	ids := func(query string) []string {
		t.Helper()
		var out []string
		for _, s := range runtimePage(t, h, "summary=1&"+query).Sessions {
			out = append(out, s.SessionID)
		}
		slices.Sort(out)
		return out
	}
	for query, want := range map[string][]string{
		"state=working&waiting=0":    {"w1"},
		"state=working&waiting=1":    {"w2"},
		"exclude_pane_source=feishu": {"w1", "w2"},
		"pane_source=feishu":         {"lark"},
	} {
		if got := ids(query); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", query, got, want)
		}
	}
	for _, bad := range []string{"waiting=yes", "exclude_pane_source=", "exclude_pane_source=elsewhere"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+bad, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if !slices.Contains(h.features(), featureSessionsQuickFilters) || slices.Contains(proto.Features, featureSessionsQuickFilters) {
		t.Error("sessions_quick_filters must be advertised by the hub")
	}
}
