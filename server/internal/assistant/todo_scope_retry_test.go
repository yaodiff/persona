package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
)

// How an extension's token is told what it cannot be told yet (#12216, items 12 and 13): an answer it can try again,
// never one that reads as "does not exist", which an extension that mirrors the to-dos would take as a deletion. What
// is told to be of the health space stays as unknown as before (todo_scope_test.go).

// extList is an extension's GET /api/v1/todos: the ids it lists and whether the list says it is partial.
func extList(t *testing.T, e *todoEnv) (ids map[string]bool, partial bool, body string) {
	t.Helper()
	w := e.doAs(roleExtension, "GET", "/api/v1/todos", "")
	if w.Code != http.StatusOK {
		t.Fatalf("an extension's list: %d %s", w.Code, w.Body)
	}
	var l struct {
		Todos   []struct{ ID string }
		Partial bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	ids = map[string]bool{}
	for _, td := range l.Todos {
		ids[td.ID] = true
	}
	return ids, l.Partial, w.Body.String()
}

// A to-do whose run its computer does not list and whose link keeps no directory cannot be told yet: an extension's
// routes of it answer 503 todo_not_told_yet with Retry-After (a write as a read), not 404 todo_not_found, and the list
// leaves it out marked partial, not as if it were deleted. Once its run is listed outside the space it is shown as a
// device's; once one is listed in the space it is hidden as before: 404 and off the list, no longer partial. A to-do
// of the space stays 404 and off the list throughout, and nothing of the space is in any answer.
func TestTodoUntoldToAnExtensionIsRetryable(t *testing.T) {
	health := identity.HealthDir("/home/owner")
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		e.sessions("server", scopeListed()...)
		plain := e.create("Renew the passport").ID
		e.must("PUT", "/api/v1/todos/"+plain+"/agents/server/s1", "")
		hidden := e.create("Log the week").ID
		e.must("PUT", "/api/v1/todos/"+hidden+"/agents/server/h1", "")
		// Two runs the computer does not list (a device may link one): neither link keeps a directory.
		letters := e.create("Sort the letters").ID
		e.must("PUT", "/api/v1/todos/"+letters+"/agents/server/s9", "")
		diary := e.create("Write it down").ID
		e.must("PUT", "/api/v1/todos/"+diary+"/agents/server/h9", "")
		for _, id := range []string{letters, diary} {
			if v := e.s.todoVerdicts(context.Background(), []string{id}, nil, nil)[id]; v != todoUntold {
				t.Fatalf("the verdict of a to-do whose run is not listed: %d, want untold", v)
			}
		}
		words := []string{"Sleep log", health, scopeReply, "Diary"}

		ids, partial, body := extList(t, e)
		noneOf(t, "an extension's list", []byte(body), words...)
		if !ids[plain] || ids[hidden] || ids[letters] || ids[diary] || !partial {
			t.Errorf("an extension's list while two to-dos cannot be told: %v partial %v", ids, partial)
		}
		if dev := e.must("GET", "/api/v1/todos", "").Body.String(); strings.Contains(dev, `"partial"`) {
			t.Errorf("a device's list says partial: %s", dev)
		}
		for _, id := range []string{letters, diary} {
			for _, req := range []struct{ method, path, body string }{
				{"GET", "/api/v1/todos/" + id, ""},
				{"GET", "/api/v1/todos/" + id + "/timeline", ""},
				{"PATCH", "/api/v1/todos/" + id, `{"title":"Renamed"}`},
			} {
				w := e.doAs(roleExtension, req.method, req.path, req.body)
				noneOf(t, "an extension at "+req.path, w.Body.Bytes(), words...)
				if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"todo_not_told_yet"`) ||
					w.Header().Get("Retry-After") == "" {
					t.Errorf("an extension's %s %s of a to-do it cannot be told about yet: %d %s %v", req.method, req.path, w.Code, w.Body, w.Header())
				}
			}
			if got := todoNow(e.s, id).Title; got == "Renamed" {
				t.Errorf("an extension's refused PATCH renamed the to-do")
			}
		}
		if w := e.doAs(roleExtension, "GET", "/api/v1/todos/"+hidden, ""); w.Code != http.StatusNotFound ||
			!strings.Contains(w.Body.String(), `"todo_not_found"`) || w.Header().Get("Retry-After") != "" {
			t.Errorf("an extension's read of a to-do of the space: %d %s", w.Code, w.Body)
		}

		// Its computer lists the runs: s9 outside the space, h9 in it.
		e.sessions("server", append(scopeListed(), scopeSession("s9", "Letters", "/home/owner/letters"),
			scopeSession("h9", "Diary", health))...)
		ids, partial, body = extList(t, e)
		noneOf(t, "an extension's list", []byte(body), words...)
		if !ids[plain] || !ids[letters] || ids[hidden] || ids[diary] || partial {
			t.Errorf("an extension's list once both are told: %v partial %v", ids, partial)
		}
		if w, dev := e.doAs(roleExtension, "GET", "/api/v1/todos/"+letters, ""), e.must("GET", "/api/v1/todos/"+letters, ""); w.Code != http.StatusOK ||
			w.Body.String() != dev.Body.String() {
			t.Errorf("an extension's read of a to-do told outside the space: %d %s\na device's: %s", w.Code, w.Body, dev.Body)
		}
		for _, id := range []string{hidden, diary} {
			w := e.doAs(roleExtension, "GET", "/api/v1/todos/"+id, "")
			noneOf(t, "an extension's read of a to-do of the space", w.Body.Bytes(), words...)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"todo_not_found"`) {
				t.Errorf("an extension's read of a to-do of the space: %d %s", w.Code, w.Body)
			}
		}
	})
}

// feedFailStore is a store whose read of the to-dos' history conversations fails while fail is set: a store-wide error.
type feedFailStore struct {
	*memStore
	fail atomic.Bool
}

func (s *feedFailStore) FeedConversations(ctx context.Context, refs, kinds []string) (map[string][]string, error) {
	if s.fail.Load() {
		return nil, errors.New("the store is not answering")
	}
	return s.memStore.FeedConversations(ctx, refs, kinds)
}

// A store that cannot tell the to-dos' history answers an extension 503 on the list and on a to-do's routes, not a
// list without them nor 404; a to-do its run's link tells of the space is 404 still, a device's list as before.
func TestTodoStoreErrorToAnExtensionIs503(t *testing.T) {
	st := &feedFailStore{memStore: &memStore{}}
	e := newTodoEnv(t, st)
	e.sessions("server", scopeListed()...)
	plain := e.create("Renew the passport").ID
	e.must("PUT", "/api/v1/todos/"+plain+"/agents/server/s1", "")
	hidden := e.create("Log the week").ID
	e.must("PUT", "/api/v1/todos/"+hidden+"/agents/server/h1", "")
	if ids, partial, _ := extList(t, e); !ids[plain] || ids[hidden] || partial {
		t.Fatalf("an extension's list while the store answers: %v partial %v", ids, partial)
	}

	st.fail.Store(true)
	if w := e.doAs(roleExtension, "GET", "/api/v1/todos", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("an extension's list while the store cannot tell: %d %s", w.Code, w.Body)
	}
	for _, method := range []string{"GET", "PATCH"} {
		w := e.doAs(roleExtension, method, "/api/v1/todos/"+plain, `{}`)
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "todo_not_found") {
			t.Errorf("an extension's %s of a to-do while the store cannot tell: %d %s", method, w.Code, w.Body)
		}
	}
	if w := e.doAs(roleExtension, "GET", "/api/v1/todos/"+hidden, ""); w.Code != http.StatusNotFound ||
		strings.Contains(w.Body.String(), "Sleep log") {
		t.Errorf("an extension's read of a to-do of the space while the store cannot tell: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/todos", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Log the week") {
		t.Errorf("a device's list while the store cannot tell: %d %s", w.Code, w.Body)
	}

	st.fail.Store(false)
	if ids, partial, _ := extList(t, e); !ids[plain] || ids[hidden] || partial {
		t.Errorf("an extension's list once the store answers again: %v partial %v", ids, partial)
	}
}

// An extension's take of a run its computer does not list answers by the computer: 404 machine_not_found for one that
// is not one of the hub's, 503 machine_offline with Retry-After for one that is off (or does not answer the read),
// and 409 agent_not_listed with Retry-After only on one that is on. Nothing is linked.
func TestTodoTakeOfAnExtensionByComputer(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.sessions("server", scopeListed()...)
	e.f.mu.Lock()
	e.f.sessions["laptop"], e.f.sessions["desk"] = nil, nil
	e.f.down = map[string]bool{"laptop": true}
	// desk's read fails as the hub answers a computer that is not connected; every other is read as the fake does.
	e.f.nodes = map[string]nodeAnswer{DeviceSessionGet: func(machine string, params json.RawMessage) (any, error) {
		if machine == "desk" {
			return nil, apierr.ErrOffline
		}
		var a struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &a)
		for _, c := range e.f.sessions[machine] {
			if c.SessionID == a.SessionID {
				return map[string]any{"session": c, "ok": true}, nil
			}
		}
		return map[string]any{"ok": false}, nil
	}}
	e.f.mu.Unlock()

	var own struct{ Todo Todo }
	if err := json.Unmarshal(e.doAs(roleExtension, "POST", "/api/v1/todos", `{"title":"Sync"}`).Body.Bytes(), &own); err != nil || own.Todo.ID == "" {
		t.Fatalf("an extension's to-do: %v", err)
	}
	for _, c := range []struct {
		machine, code string
		status        int
		retry         bool
	}{
		{"nowhere", "machine_not_found", http.StatusNotFound, false},
		{"laptop", "machine_offline", http.StatusServiceUnavailable, true},
		{"desk", "machine_offline", http.StatusServiceUnavailable, true},
		{"server", "agent_not_listed", http.StatusConflict, true},
	} {
		w := e.doAs(roleExtension, "PUT", "/api/v1/todos/"+own.Todo.ID+"/agents/"+c.machine+"/gone", "")
		if w.Code != c.status || !strings.Contains(w.Body.String(), `"`+c.code+`"`) || (w.Header().Get("Retry-After") != "") != c.retry {
			t.Errorf("an extension takes a run %s does not list: %d %s %v, want %d %s", c.machine, w.Code, w.Body, w.Header(), c.status, c.code)
		}
	}
	if a := todoNow(e.s, own.Todo.ID).Agents; len(a) != 0 {
		t.Errorf("the extension's own to-do took %+v", a)
	}
	// A run the computer lists outside the space is taken as before.
	if w := e.doAs(roleExtension, "PUT", "/api/v1/todos/"+own.Todo.ID+"/agents/server/s1", ""); w.Code != http.StatusOK {
		t.Errorf("an extension takes a listed run: %d %s", w.Code, w.Body)
	}
}
