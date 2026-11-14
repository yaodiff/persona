package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const savedSearchPath = "/api/v1/saved-agent-searches"

type savedSearchMemoryStore struct {
	Store
	mu                    sync.Mutex
	raw                   []byte
	readError, writeError bool
	saves                 int
}

func (st *savedSearchMemoryStore) Setting(_ context.Context, key string, out any) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if key != savedAgentSearchesKey {
		return false, nil
	}
	if st.readError {
		return false, errors.New("read unavailable")
	}
	if st.raw == nil {
		return false, nil
	}
	return true, json.Unmarshal(st.raw, out)
}
func (st *savedSearchMemoryStore) CompareSetting(_ context.Context, key string, revision int64, value any) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if key != savedAgentSearchesKey {
		return false, errors.New("wrong key")
	}
	st.saves++
	if st.writeError {
		return false, errors.New("write unavailable")
	}
	var before savedAgentSearchDocument
	if st.raw != nil {
		if err := json.Unmarshal(st.raw, &before); err != nil {
			return false, err
		}
	}
	if before.Revision != revision {
		return false, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	st.raw = raw
	return true, nil
}

type savedSearchEventHub struct {
	Hub
	mu     sync.Mutex
	events []map[string]any
}

func (h *savedSearchEventHub) Broadcast(_ context.Context, event Broadcast) {
	var value map[string]any
	_ = json.Unmarshal(event.Message, &value)
	h.mu.Lock()
	h.events = append(h.events, value)
	h.mu.Unlock()
}
func (h *savedSearchEventHub) messages() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}
func savedSearchService(st Store) (*Service, *savedSearchEventHub) {
	h := &savedSearchEventHub{}
	return New(Options{Hub: h, Store: func() Store { return st }}), h
}
func savedSearchResponseOf(t *testing.T, s *Service, method string, body any) savedAgentSearchResponse {
	t.Helper()
	raw := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(data)
	}
	w := serveAs(s, roleFull, method, savedSearchPath, raw)
	var out savedAgentSearchResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%s %s: %d %s", method, raw, w.Code, w.Body)
	}
	return out
}
func searchMutation(revision, action string, fields map[string]any) map[string]any {
	out := map[string]any{"revision": revision, "action": action}
	for k, v := range fields {
		out[k] = v
	}
	return out
}
func TestSavedAgentSearchLifecycleAndRestore(t *testing.T) {
	store := &savedSearchMemoryStore{}
	s, h := savedSearchService(store)
	empty := savedSearchResponseOf(t, s, "GET", nil)
	if empty.Revision != "0" || len(empty.Searches) != 0 || !empty.CanWrite {
		t.Fatalf("initial: %+v", empty)
	}
	first := savedSearchResponseOf(t, s, "POST", searchMutation("0", "add", map[string]any{"name": "  Review queue  ", "query": "  state:needs_you  "}))
	item := first.Searches[0]
	if first.Revision != "1" || !validSavedSearchID(item.ID) || item.Name != "Review queue" || item.Query != "state:needs_you" || item.CreatedAt.IsZero() || !item.CreatedAt.Equal(item.UpdatedAt) {
		t.Fatalf("add: %+v", first)
	}
	second := savedSearchResponseOf(t, s, "POST", searchMutation("1", "add", map[string]any{"name": "Builds", "query": "machine:build state:working"}))
	other := second.Searches[1]
	moved := savedSearchResponseOf(t, s, "POST", searchMutation("2", "move", map[string]any{"id": other.ID, "before_id": item.ID}))
	if moved.Revision != "3" || !reflect.DeepEqual(moved.Searches, []SavedAgentSearch{other, item}) {
		t.Fatalf("move changed metadata: %+v", moved)
	}
	renamed := savedSearchResponseOf(t, s, "POST", searchMutation("3", "update", map[string]any{"id": item.ID, "name": "Review Queue", "query": "is:error since:7d"}))
	updated := renamed.Searches[1]
	if updated.Name != "Review Queue" || updated.Query != "is:error since:7d" || !updated.CreatedAt.Equal(item.CreatedAt) || updated.UpdatedAt.Before(item.UpdatedAt) {
		t.Fatalf("update: %+v", updated)
	}
	deleted := savedSearchResponseOf(t, s, "POST", searchMutation("4", "delete", map[string]any{"id": other.ID}))
	if deleted.Revision != "5" || len(deleted.Searches) != 1 {
		t.Fatal(deleted)
	}
	restored := savedSearchResponseOf(t, s, "POST", searchMutation("5", "restore", map[string]any{"item": other, "before_id": item.ID}))
	if restored.Revision != "6" || !reflect.DeepEqual(restored.Searches, []SavedAgentSearch{other, updated}) {
		t.Fatalf("restore changed identity, dates or position: %+v", restored)
	}
	restarted, _ := savedSearchService(store)
	if got := savedSearchResponseOf(t, restarted, "GET", nil); !reflect.DeepEqual(got, restored) {
		t.Fatalf("restart: %+v", got)
	}
	events := h.messages()
	if len(events) != 6 {
		t.Fatalf("mutation events: %v", events)
	}
	for i, event := range events {
		if len(event) != 2 || event["type"] != "saved_agent_searches" || event["revision"] != strconv.Itoa(i+1) {
			t.Fatalf("event contains wrong revision or query contents: %v", event)
		}
	}
	for _, role := range []string{roleAnswer, roleExtension} {
		w := serveAs(s, role, "GET", savedSearchPath, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"can_write":false`) {
			t.Fatalf("read-only: %d %s", w.Code, w.Body)
		}
		w = serveAs(s, role, "POST", savedSearchPath, `{"revision":"6","action":"delete","id":"`+item.ID+`"}`)
		if w.Code != 403 {
			t.Fatalf("unauthorized write: %d %s", w.Code, w.Body)
		}
	}
	if len(h.messages()) != 6 || store.saves != 6 {
		t.Fatal("read-only action reached persistence")
	}
}
func TestSavedAgentSearchInvalidRequestsNeverWrite(t *testing.T) {
	s, h := savedSearchService(&savedSearchMemoryStore{})
	validID := "search_" + strings.Repeat("a", 24)
	invalid := []string{
		`null`, `{}`, `{"revision":0,"action":"add","name":"x","query":"y"}`, `{"revision":"00","action":"add","name":"x","query":"y"}`, `{"revision":"-1","action":"add","name":"x","query":"y"}`,
		`{"revision":"9223372036854775808","action":"add","name":"x","query":"y"}`, `{"revision":"9223372036854775807","action":"add","name":"x","query":"y"}`,
		`{"revision":"0","action":"add","name":"x","name":"other","query":"y"}`, `{"revision":"0","action":"add","Name":"x","query":"y"}`,
		`{"revision":"0","action":"add","name":"x","query":"y","id":"` + validID + `"}`, `{"revision":"0","action":"delete","id":"` + validID + `","query":null}`,
		`{"revision":"0","action":"add","name":null,"query":"y"}`, `{"revision":"0","action":"add","name":" ","query":"y"}`, `{"revision":"0","action":"add","name":"bad\u0000","query":"y"}`,
		`{"revision":"0","action":"add","name":"x","query":"one\ntwo"}`, `{"revision":"0","action":"add","name":"x","query":"\uFEFF> close"}`, `{"revision":"0","action":"add","name":"x","query":" >command "}`,
		`{"revision":"0","action":"add","name":"x","query":"y"} {}`, `{"revision":"0","action":"move","id":"bad"}`, `{"revision":"0","action":"restore","item":null}`,
		`{"revision":"0","action":"restore","item":{"id":"` + validID + `","name":"x","query":"y"}}`,
		`{"revision":"0","action":"unknown"}`,
	}
	for _, body := range invalid {
		w := serve(s, "POST", savedSearchPath, body)
		if w.Code != 400 {
			t.Errorf("accepted %s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, fields := range []map[string]any{{"name": strings.Repeat("界", 65), "query": "x"}, {"name": "x", "query": strings.Repeat("界", 342)}} {
		raw, _ := json.Marshal(searchMutation("0", "add", fields))
		if w := serve(s, "POST", savedSearchPath, string(raw)); w.Code != 400 {
			t.Fatalf("bound: %d %s", w.Code, w.Body)
		}
	}
	if w := serve(s, "POST", savedSearchPath, strings.Repeat(" ", 16<<10+1)); w.Code != 413 {
		t.Fatalf("body bound: %d %s", w.Code, w.Body)
	}
	if len(h.messages()) != 0 {
		t.Fatal("invalid mutation broadcast")
	}
	// Boundary counts are Unicode characters for names, UTF-8 bytes for queries.
	got := savedSearchResponseOf(t, s, "POST", searchMutation("0", "add", map[string]any{"name": strings.Repeat("界", 64), "query": strings.Repeat("界", 341) + "x"}))
	if got.Revision != "1" {
		t.Fatal(got)
	}
	wrapped := savedSearchResponseOf(t, s, "POST", searchMutation("1", "add", map[string]any{"name": "\uFEFF Named \uFEFF", "query": "\uFEFF state:working \uFEFF"}))
	if wrapped.Searches[1].Name != "Named" || wrapped.Searches[1].Query != "state:working" {
		t.Fatalf("browser whitespace normalization differs: %+v", wrapped)
	}
}
func TestSavedAgentSearchConflictsBoundsAndTimestamps(t *testing.T) {
	store := &savedSearchMemoryStore{}
	s, h := savedSearchService(store)
	doc := savedSearchResponseOf(t, s, "POST", searchMutation("0", "add", map[string]any{"name": "Queue", "query": "is:error"}))
	first := doc.Searches[0]
	for _, tc := range []struct {
		body   map[string]any
		status int
	}{
		{searchMutation("0", "add", map[string]any{"name": "stale", "query": "x"}), 409},
		{searchMutation("1", "add", map[string]any{"name": "qUEUE", "query": "x"}), 409},
		{searchMutation("1", "move", map[string]any{"id": first.ID, "before_id": first.ID}), 400},
		{searchMutation("1", "move", map[string]any{"id": first.ID, "before_id": "search_" + strings.Repeat("b", 24)}), 400},
		{searchMutation("1", "restore", map[string]any{"item": first}), 409},
	} {
		raw, _ := json.Marshal(tc.body)
		w := serve(s, "POST", savedSearchPath, string(raw))
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", raw, w.Code, w.Body)
		}
	}
	bad := first
	bad.ID = "search_" + strings.Repeat("c", 24)
	bad.Name = "Other"
	bad.UpdatedAt = bad.CreatedAt.Add(-time.Second)
	raw, _ := json.Marshal(searchMutation("1", "restore", map[string]any{"item": bad}))
	if w := serve(s, "POST", savedSearchPath, string(raw)); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if len(h.messages()) != 1 {
		t.Fatal("refused operations broadcast")
	}
	now := time.Now().UTC()
	full := savedAgentSearchDocument{Revision: 50, Searches: []SavedAgentSearch{}}
	for i := range 50 {
		full.Searches = append(full.Searches, SavedAgentSearch{ID: fmt.Sprintf("search_%024x", i), Name: fmt.Sprint("Saved ", i), Query: "x", CreatedAt: now, UpdatedAt: now})
	}
	store.raw, _ = json.Marshal(full)
	raw, _ = json.Marshal(searchMutation("50", "add", map[string]any{"name": "Too many", "query": "x"}))
	if w := serve(s, "POST", savedSearchPath, string(raw)); w.Code != 400 || !strings.Contains(w.Body.String(), "limit_reached") {
		t.Fatalf("capacity: %d %s", w.Code, w.Body)
	}
	// Removing the last entry keeps its revision; undo cannot race past another writer.
	store.raw, _ = json.Marshal(savedAgentSearchDocument{Revision: 100, Searches: []SavedAgentSearch{first}})
	gone := savedSearchResponseOf(t, s, "POST", searchMutation("100", "delete", map[string]any{"id": first.ID}))
	if gone.Revision != "101" || len(gone.Searches) != 0 {
		t.Fatal(gone)
	}
	_ = savedSearchResponseOf(t, s, "POST", searchMutation("101", "add", map[string]any{"name": "New", "query": "new"}))
	raw, _ = json.Marshal(searchMutation("101", "restore", map[string]any{"item": first}))
	if w := serve(s, "POST", savedSearchPath, string(raw)); w.Code != 409 {
		t.Fatalf("stale undo: %d %s", w.Code, w.Body)
	}
}
func TestSavedAgentSearchStoreFailuresKeepData(t *testing.T) {
	for _, st := range []Store{nil, &memStore{}} {
		s, _ := savedSearchService(st)
		for _, method := range []string{"GET", "POST"} {
			w := serve(s, method, savedSearchPath, `{"revision":"0","action":"add","name":"x","query":"y"}`)
			if w.Code != 503 || !strings.Contains(w.Body.String(), "not_supported") {
				t.Fatalf("ephemeral store advertised: %d %s", w.Code, w.Body)
			}
		}
	}
	store := &savedSearchMemoryStore{}
	s, h := savedSearchService(store)
	before := savedSearchResponseOf(t, s, "POST", searchMutation("0", "add", map[string]any{"name": "Kept", "query": "x"}))
	rawBefore := slices.Clone(store.raw)
	for _, failure := range []string{"read", "write"} {
		store.readError = failure == "read"
		store.writeError = failure == "write"
		raw, _ := json.Marshal(searchMutation("1", "delete", map[string]any{"id": before.Searches[0].ID}))
		w := serve(s, "POST", savedSearchPath, string(raw))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "storage_unavailable") || !slices.Equal(store.raw, rawBefore) {
			t.Fatalf("failed %s lost data: %d %s", failure, w.Code, w.Body)
		}
	}
	store.readError, store.writeError = false, false
	for _, raw := range []string{`null`, `{}`, `{"revision":null,"searches":[]}`, `{"revision":1,"searches":null}`, `{"revision":-1,"searches":[]}`, `{"revision":1,"searches":[{"id":"invalid","name":"x","query":"y"}]}`} {
		store.raw = []byte(raw)
		if w := serve(s, "GET", savedSearchPath, ""); w.Code != 503 {
			t.Fatalf("corrupt document became empty: %s: %d %s", raw, w.Code, w.Body)
		}
	}
	if len(h.messages()) != 1 {
		t.Fatal("failed persistence broadcast")
	}
	store.raw, _ = json.Marshal(savedAgentSearchDocument{Revision: math.MaxInt64, Searches: before.Searches})
	if got := savedSearchResponseOf(t, s, "GET", nil); got.Revision != "9223372036854775807" {
		t.Fatal("revision lost integer precision")
	}
}
