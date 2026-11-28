package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
)

// The To-dos view across managers (todo_view.go, #10226): the hub adds the managers' counts up and joins their places,
// lists the features only while every manager whose to-dos it merges names them, and never computes a view itself.

// todoReplies is what a test manager answers GET /api/v1/todos and GET /api/v1/todos/places with, and any other path
// it is given a body for (items).
type todoReplies struct {
	mu            sync.Mutex
	list          string // the list's body
	places        string // the places' body; "" answers placesStatus (405 when 0: an older manager's mux)
	status        int
	items         map[string]string
	revision      string // X-Persona-Todos-Revision on the list and revision route; "": an older manager
	fullReads     int
	revisionReads int
}

func (r *todoReplies) set(list, places string, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list, r.places, r.status = list, places, status
}

func (r *todoReplies) setRevision(revision string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revision = revision
}

func (r *todoReplies) reads() (full, revision int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fullReads, r.revisionReads
}

func (r *todoReplies) answer(method string, params json.RawMessage) (any, error) {
	if method != assistantwire.MethodHTTP {
		return map[string]any{}, nil
	}
	var req assistantwire.HTTPRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	json200 := func(body string) assistantwire.HTTPResponse {
		return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(body)}
	}
	switch {
	case req.Path == "/api/v1/todos":
		r.fullReads++
		resp := json200(r.list)
		if r.revision != "" {
			resp.Header["X-Persona-Todos-Revision"] = []string{r.revision}
		}
		return resp, nil
	case req.Path == "/api/v1/todos/revision" && r.revision != "":
		r.revisionReads++
		resp := json200(`{"revision":` + strconv.Quote(r.revision) + `}`)
		resp.Header["X-Persona-Todos-Revision"] = []string{r.revision}
		return resp, nil
	case req.Path == "/api/v1/todos/places" && r.places != "":
		return json200(r.places), nil
	case r.items[req.Path] != "":
		return json200(r.items[req.Path]), nil
	case req.Path == "/api/v1/todos/places":
		status := r.status
		if status == 0 {
			status = http.StatusMethodNotAllowed
		}
		return assistantwire.HTTPResponse{Status: status, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"error":"` + http.StatusText(status) + `"}`)}, nil
	}
	return assistantwire.HTTPResponse{Status: http.StatusNotFound, Body: []byte(`{"error":"Not Found"}`)}, nil
}

// todoViewHub is a hub with two managers on its bus, the default (assistant) and m-0c0c0c, each answering the to-dos
// with its replies.
func todoViewHub(t *testing.T) (*Hub, *httptest.Server, *guardManager, *guardManager, *todoReplies, *todoReplies) {
	t.Helper()
	h, srv, _ := newBusHub(t)
	ra, rb := &todoReplies{}, &todoReplies{}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, ra.answer)
	b := connectGuardManager(t, h, srv.URL, "m-0c0c0c", rb.answer)
	return h, srv, a, b, ra, rb
}

// todoViewCapabilities is the manager's hello again, naming the To-dos view's capabilities it is given.
func todoViewCapabilities(t *testing.T, m *guardManager, capabilities ...string) {
	t.Helper()
	if err := m.call(t, busHello, map[string]any{"id": m.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, capabilities...)}, nil); err != nil {
		t.Fatal(err)
	}
}

func todoViewGet(t *testing.T, h *Hub, srv *httptest.Server, path string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// todoCountsJSON is counts with every section, n in the ones named.
func todoCountsJSON(open, needsYou, inProgress int, sections map[string]int) string {
	c := assistantwire.TodoCounts{Open: open, NeedsYou: needsYou, InProgress: inProgress, Sections: map[string]int{}}
	for _, s := range assistantwire.TodoListSections {
		c.Sections[s] = sections[s]
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// Each feature is listed only while every manager whose to-dos the list merges is connected and names it.
func TestTodoViewFeaturesWhileEveryManagerNamesThem(t *testing.T) {
	h, _, a, b, _, _ := todoViewHub(t)
	listed := func() (view, places bool) {
		f := h.features()
		return slices.Contains(f, featureTodoView), slices.Contains(f, featureTodoPlaces)
	}
	if v, p := listed(); v || p {
		t.Fatalf("listed before any manager named them: view %v, places %v", v, p)
	}
	todoViewCapabilities(t, a, assistantwire.CapabilityTodoView, assistantwire.CapabilityTodoPlaces)
	if v, p := listed(); v || p {
		t.Fatalf("listed while m-0c0c0c names neither: view %v, places %v", v, p)
	}
	todoViewCapabilities(t, b, assistantwire.CapabilityTodoView)
	if v, p := listed(); !v || p {
		t.Fatalf("both name todo_view, one todo_places: view %v, places %v", v, p)
	}
	todoViewCapabilities(t, b, assistantwire.CapabilityTodoView, assistantwire.CapabilityTodoPlaces)
	if v, p := listed(); !v || !p {
		t.Fatalf("every manager names both: view %v, places %v", v, p)
	}
	todoViewCapabilities(t, a)
	if v, p := listed(); v || p {
		t.Fatalf("the default stopped naming them: view %v, places %v", v, p)
	}
	// The default names both again, then goes offline: its to-dos are part of the list, so both are hidden.
	todoViewCapabilities(t, a, assistantwire.CapabilityTodoView, assistantwire.CapabilityTodoPlaces)
	if v, p := listed(); !v || !p {
		t.Fatalf("every manager names both again: view %v, places %v", v, p)
	}
	if err := h.busConnOf(a.id).ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the default offline", func() bool { return h.busConnOf(a.id) == nil })
	if v, p := listed(); v || p {
		t.Fatalf("listed with the default offline: view %v, places %v", v, p)
	}
}

// todo_timeline (#10465) is listed only while every manager whose to-dos the list merges is connected and names it,
// whatever the other To-dos features.
func TestTodoTimelineFeatureWhileEveryManagerNamesIt(t *testing.T) {
	h, _, a, b, _, _ := todoViewHub(t)
	listed := func() bool { return slices.Contains(h.features(), featureTodoTimeline) }
	todoViewCapabilities(t, a, assistantwire.CapabilityTodoView, assistantwire.CapabilityTodoTimeline)
	todoViewCapabilities(t, b, assistantwire.CapabilityTodoView)
	if listed() {
		t.Fatal("listed while m-0c0c0c does not name it")
	}
	todoViewCapabilities(t, b, assistantwire.CapabilityTodoTimeline)
	if !listed() {
		t.Fatal("not listed with every manager naming it")
	}
	if f := h.features(); slices.Contains(f, featureTodoView) {
		t.Fatalf("todo_view listed with m-0c0c0c not naming it: %v", f)
	}
	if err := h.busConnOf(b.id).ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "m-0c0c0c offline", func() bool { return h.busConnOf(b.id) == nil })
	if !listed() {
		t.Fatal("not listed with only the default left, naming it") // an offline manager other than the default leaves the list
	}
	todoViewCapabilities(t, a, assistantwire.CapabilityTodoView)
	if listed() {
		t.Fatal("listed after the default stopped naming it")
	}
}

// An extension's report cannot add the To-dos view's features: only the managers' hellos promise them.
func TestTodoViewFeaturesNotFromAReport(t *testing.T) {
	h := newTestHub(t)
	h.keepExtReport(assistantwire.AssistantID, &extwire.Report{Features: []string{featureTodoView, featureTodoPlaces,
		featureTodoTimeline, "fa"}})
	features := h.features()
	if !slices.Contains(features, "fa") {
		t.Fatalf("the report's own feature is missing: %v", features)
	}
	for _, f := range []string{featureTodoView, featureTodoPlaces, featureTodoTimeline} {
		if slices.Contains(features, f) {
			t.Errorf("a report added %s", f)
		}
	}
}

// The merged list's counts are the managers' added up, field by field and section by section, before the validator is
// taken, so the tag is the hash of exactly the bytes sent; an older manager without counts adds nothing, and with none
// at all the list has none.
func TestTodosListCountsAddedUp(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	ca := todoCountsJSON(3, 1, 1, map[string]int{"needs_you": 1, "working": 1, "todo": 1, "done": 2})
	cb := todoCountsJSON(2, 1, 2, map[string]int{"needs_you": 2, "waiting": 2, "done": 1})
	ra.set(`{"todos":[{"id":"todo_a1","title":"Water the roses"}],"counts":`+ca+`}`, "", 0)
	rb.set(`{"counts":`+cb+`,"todos":[{"id":"todo_b1","title":"Book the ferry"}]}`, "", 0)

	read := func() (*http.Response, []byte, *assistantwire.TodoCounts, []map[string]any) {
		t.Helper()
		resp, body := todoViewGet(t, h, srv, "/api/v1/todos", nil)
		var out struct {
			Todos  []map[string]any          `json:"todos"`
			Counts *assistantwire.TodoCounts `json:"counts"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("list: %d %s", resp.StatusCode, body)
		}
		return resp, body, out.Counts, out.Todos
	}
	resp, body, counts, todos := read()
	want := assistantwire.TodoCounts{Open: 5, NeedsYou: 2, InProgress: 3, Sections: map[string]int{}}
	for _, s := range assistantwire.TodoListSections {
		want.Sections[s] = 0
	}
	want.Sections["needs_you"], want.Sections["working"], want.Sections["waiting"], want.Sections["todo"], want.Sections["done"] = 3, 1, 2, 1, 3
	if counts == nil || !reflect.DeepEqual(*counts, want) {
		t.Fatalf("counts %+v, want %+v", counts, want)
	}
	if len(todos) != 2 || todos[0]["manager"] != assistantwire.AssistantID || todos[1]["manager"] != "m-0c0c0c" {
		t.Fatalf("the rows: %v", todos)
	}
	sum := sha256.Sum256(body)
	tag := resp.Header.Get("ETag")
	if tag != `W/"t1-`+hex.EncodeToString(sum[:16])+`"` {
		t.Fatalf("the tag %q is not the hash of the bytes sent: %s", tag, body)
	}
	if again, _ := todoViewGet(t, h, srv, "/api/v1/todos", map[string]string{"If-None-Match": tag}); again.StatusCode != http.StatusNotModified {
		t.Fatalf("the same lists again: %d, want 304", again.StatusCode)
	}

	// m-0c0c0c changes only its counts: the merged list's bytes and tag change with them.
	rb.set(`{"counts":`+todoCountsJSON(2, 0, 2, map[string]int{"can_wait": 2, "waiting": 2, "done": 1})+`,"todos":[{"id":"todo_b1","title":"Book the ferry"}]}`, "", 0)
	if changed, _ := todoViewGet(t, h, srv, "/api/v1/todos", map[string]string{"If-None-Match": tag}); changed.StatusCode != http.StatusOK ||
		changed.Header.Get("ETag") == tag {
		t.Fatalf("other counts: %d %q, want 200 with a new tag", changed.StatusCode, changed.Header.Get("ETag"))
	}

	// An older m-0c0c0c sends no counts: the default's alone, though its list still merges.
	rb.set(`{"todos":[{"id":"todo_b1","title":"Book the ferry"}]}`, "", 0)
	if _, _, counts, todos := read(); counts == nil || counts.Open != 3 || counts.Sections["done"] != 2 || len(todos) != 2 {
		t.Fatalf("with an older manager: %+v, %d rows", counts, len(todos))
	}
	// An older default and a newer m-0c0c0c: its counts, though the rest of the answer is the default's.
	ra.set(`{"todos":[{"id":"todo_a1","title":"Water the roses"}],"as_of":"t"}`, "", 0)
	rb.set(`{"counts":`+cb+`,"todos":[{"id":"todo_b1","title":"Book the ferry"}],"as_of":"u"}`, "", 0)
	if _, body, counts, _ := read(); counts == nil || counts.Open != 2 || counts.Sections["waiting"] != 2 {
		t.Fatalf("with an older default: %s", body)
	}
	// Nobody sends counts: the list has none.
	rb.set(`{"todos":[{"id":"todo_b1","title":"Book the ferry"}]}`, "", 0)
	if _, body, counts, _ := read(); counts != nil {
		t.Fatalf("counts from nowhere: %s", body)
	}
}

func TestTodosCompleteListKeepsAllRowsAndItsETag(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	rows := make([]string, 60)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"id":"todo_complete_%02d","title":"Done %d","created_at":"2026-10-01T09:%02d:00Z","updated_at":"2026-10-01T10:00:00Z","done_at":"2026-10-01T10:00:00Z"}`, i, i, i)
	}
	board := `{"status_groups":[],"direction_groups":[],"sidebar_tree":[],"counts":{"needs_you":0,"in_progress":0,"pending_review":0,"waiting":0,"someday":0,"done":60,"open_total":0}}`
	ra.set(`{"todos":[`+strings.Join(rows, ",")+`],"counts":`+todoCountsJSON(0, 0, 0, map[string]int{"done": 60})+`,"board":`+board+`}`, "", 0)
	rb.set(`{"todos":[]}`, "", 0)

	resp, body := todoViewGet(t, h, srv, "/api/v1/todos", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete list: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Todos []map[string]any `json:"todos"`
		Board *struct {
			Counts struct {
				Done int `json:"done"`
			} `json:"counts"`
		} `json:"board"`
		ClosedNextCursor string         `json:"closed_next_cursor"`
		ClosedHasMore    bool           `json:"closed_has_more"`
		ClosedCursors    map[string]any `json:"closed_cursors"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Todos) != 60 || out.Board == nil || out.Board.Counts.Done != 60 {
		t.Fatalf("complete response: %d rows, board %+v", len(out.Todos), out.Board)
	}
	if out.ClosedNextCursor != "" || out.ClosedHasMore || len(out.ClosedCursors) != 0 {
		t.Fatalf("complete response has closed pagination fields: %+v", out)
	}
	tag := resp.Header.Get("ETag")
	if tag == "" {
		t.Fatal("complete response has no ETag")
	}
	again, againBody := todoViewGet(t, h, srv, "/api/v1/todos", map[string]string{"If-None-Match": tag})
	if again.StatusCode != http.StatusNotModified || len(againBody) != 0 {
		t.Fatalf("same complete response: %d %s, want 304 with no body", again.StatusCode, againBody)
	}
}

func TestTodosV2BoardMergedAcrossManagers(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	const a = `{
		"status_groups":[{"key":"needs_you","total_count":0,"visible_count":0,"entries":[]},
			{"key":"todo","total_count":2,"visible_count":1,"entries":[{"todo_id":"a","folded_child_ids":["a-step"],"folded_count":1}]}],
		"direction_groups":[{"direction_id":"shared","label":"Shared work","counts":{"total":2},"todo_ids":["a","a-step"]},
			{"direction_id":"","counts":{"total":1},"todo_ids":["a-other"]}],
		"sidebar_tree":[{"todo_id":"a","section":"todo","child_ids":["a-step"]}],
		"counts":{"open_total":1,"needs_you":0},"future_member":"keep"
	}`
	const b = `{
		"status_groups":[{"key":"needs_you","total_count":2,"visible_count":2,"entries":[{"todo_id":"b"},{"todo_id":"b-child"}]},
			{"key":"todo","total_count":1,"visible_count":1,"entries":[{"todo_id":"b-other"}]},
			{"key":"cancelled","total_count":1,"visible_count":1,"entries":[{"todo_id":"b-ended"}]}],
		"direction_groups":[{"direction_id":"shared","counts":{"total":2,"needs_you":1},"todo_ids":["b","b-child"]},
			{"direction_id":"another","label":"Another direction","counts":{"total":1},"todo_ids":["b-ended"]},
			{"direction_id":"","counts":{"total":1},"todo_ids":["b-other"]}],
		"sidebar_tree":[{"todo_id":"b","section":"needs_you","surfaced_child_ids":["b-child"]},
			{"todo_id":"b-other","section":"todo"}],
		"counts":{"open_total":2,"needs_you":1,"cancelled":1}
	}`
	const expected = `{
		"status_groups":[{"key":"needs_you","total_count":2,"visible_count":2,"entries":[{"todo_id":"b"},{"todo_id":"b-child"}]},
			{"key":"todo","total_count":3,"visible_count":2,"entries":[{"todo_id":"a","folded_child_ids":["a-step"],"folded_count":1},{"todo_id":"b-other"}]},
			{"key":"cancelled","total_count":1,"visible_count":1,"entries":[{"todo_id":"b-ended"}]}],
		"direction_groups":[{"direction_id":"shared","label":"Shared work","counts":{"total":4,"needs_you":1},"todo_ids":["a","a-step","b","b-child"]},
			{"direction_id":"another","label":"Another direction","counts":{"total":1},"todo_ids":["b-ended"]},
			{"direction_id":"","counts":{"total":2},"todo_ids":["a-other","b-other"]}],
		"sidebar_tree":[{"todo_id":"a","section":"todo","child_ids":["a-step"]},
			{"todo_id":"b","section":"needs_you","surfaced_child_ids":["b-child"]},{"todo_id":"b-other","section":"todo"}],
		"counts":{"open_total":3,"needs_you":1,"cancelled":1},"future_member":"keep"
	}`
	const aList = `{"todos":[{"id":"a"},{"id":"a-step"},{"id":"a-other"}]`
	const bList = `{"todos":[{"id":"b"},{"id":"b-child"},{"id":"b-other"},{"id":"b-ended"}]`
	ra.set(aList+`,"board":`+a+`}`, "", 0)
	rb.set(bList+`,"board":`+b+`}`, "", 0)

	read := func(want string) string {
		t.Helper()
		resp, body := todoViewGet(t, h, srv, "/api/v1/todos", nil)
		var list struct {
			Board json.RawMessage `json:"board"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
			t.Fatalf("list: %d %s", resp.StatusCode, body)
		}
		if want == "" && list.Board != nil || want != "" && !sameJSONValue(string(list.Board), want) {
			t.Fatalf("board = %s, want %s", list.Board, want)
		}
		return resp.Header.Get("ETag")
	}
	tag := read(expected)
	if again := read(expected); tag == "" || again != tag {
		t.Fatalf("identical board merges gave different ETags: %q and %q", tag, again)
	}
	ra.set(aList+`}`, "", 0)
	read(b) // An older default manager has no board.
	rb.set(bList+`}`, "", 0)
	read("") // No manager publishes a board.

	answers := []listAnswer{listOf("assistant", 200, aList+`,"board":`+a+`}`)}
	sums := todoListSums(answers)
	if raw := string(sums["board"]); raw != a {
		t.Fatalf("a single manager's board must pass through as sent, got %s", raw)
	}
	withBoardMerge, withoutBoardMerge := httptest.NewRecorder(), httptest.NewRecorder()
	writeMergedSet(withBoardMerge, "todos", answers, sums)
	writeMerged(withoutBoardMerge, "todos", answers)
	sameResponse(t, "single-manager board", withBoardMerge, withoutBoardMerge)
}

// The merge writes the added-up counts byte for byte as decoding every answer and encoding the merge again does, on
// every shape the scan meets.
func TestTodoCountsMergeMatchesDecoding(t *testing.T) {
	ca := todoCountsJSON(1, 1, 0, map[string]int{"needs_you": 1})
	cb := todoCountsJSON(2, 0, 2, map[string]int{"working": 1, "waiting": 1})
	for _, c := range []struct {
		name    string
		answers []listAnswer
	}{
		{"both", []listAnswer{listOf("assistant", 200, `{"todos":[{"id":"a"}],"counts":`+ca+`}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[{"id":"b"}]}`)}},
		{"the second only", []listAnswer{listOf("assistant", 200, `{"todos":[{"id":"a"}],"as_of":"t"}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[]}`)}},
		{"null counts", []listAnswer{listOf("assistant", 200, `{"todos":[{"id":"a"}],"counts":null}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[]}`)}},
		{"a failed default", []listAnswer{listOf("assistant", 503, `{"code":"assistant_offline","counts":`+ca+`}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[]}`)}},
		{"not an object", []listAnswer{listOf("assistant", 200, `[]`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":null}`)}},
		{"a list that is no array", []listAnswer{listOf("assistant", 200, `{"todos":{"id":"a"},"counts":`+ca+`}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[]}`)}},
		{"one needing decoding", []listAnswer{listOf("assistant", 200, `{"todos":[{"id":"a"}],"a":1,"a":2,"counts":`+ca+`}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[{"id":"b"}]}`)}},
		{"escaped counts key", []listAnswer{listOf("assistant", 200, `{"todos":[],"\u0063ounts":`+ca+`}`), listOf("m-b", 200, `{"counts":`+cb+`,"todos":[]}`)}},
		{"nobody", []listAnswer{listOf("assistant", 200, `{"todos":[{"id":"a"}],"z":"<&>"}`), listOf("m-b", 200, `{"todos":[{"id":"b"}]}`)}},
	} {
		got, want := httptest.NewRecorder(), httptest.NewRecorder()
		answers := cloneAnswers(c.answers)
		for i := range answers {
			if answers[i].ok() {
				(&Hub{}).noteTodoOwners(&answers[i]) // the merge's own scan, as mergeList takes it
			}
		}
		writeMergedSet(got, "todos", answers, todoListSums(answers))
		plain := cloneAnswers(c.answers)
		writeDecoded(want, "todos", plain, todoListSums(plain))
		sameResponse(t, c.name, got, want)
	}
	// What they add up to: one answer's counts as it sent them; two, key by key.
	sums := todoListSums([]listAnswer{listOf("assistant", 200, `{"todos":[],"counts":`+ca+`}`), listOf("m-b", 200, `{"todos":[],"counts":`+cb+`}`)})
	if got, want := string(sums["counts"]), todoCountsJSON(3, 1, 2, map[string]int{"needs_you": 1, "working": 1, "waiting": 1}); !sameJSONValue(got, want) {
		t.Fatalf("summed %s, want %s", got, want)
	}
	if got := string(todoListSums([]listAnswer{listOf("assistant", 200, `{"todos":[],"counts":`+ca+`}`)})["counts"]); got != ca {
		t.Fatalf("one manager's %s, want %s", got, ca)
	}
	// Members a newer manager adds, which this hub does not know: passed on as sent from one manager; from two, a
	// number and a map added up, anything else the first's.
	newer := `{"open":1,"needs_you":1,"in_progress":0,"sections":{"needs_you":1,"later":2},"assistant_needs":3,"note":"a"}`
	if got := string(todoListSums([]listAnswer{listOf("assistant", 200, `{"todos":[],"counts":`+newer+`}`)})["counts"]); got != newer {
		t.Fatalf("one newer manager's %s, want %s", got, newer)
	}
	sums = todoListSums([]listAnswer{listOf("assistant", 200, `{"todos":[],"counts":`+newer+`}`),
		listOf("m-b", 200, `{"todos":[],"counts":{"open":2,"sections":{"later":1,"todo":2},"assistant_needs":4,"note":"b","x":1}}`)})
	if got, want := string(sums["counts"]), `{"open":3,"needs_you":1,"in_progress":0,"sections":{"needs_you":1,"later":3,"todo":2},"assistant_needs":7,"note":"a","x":1}`; !sameJSONValue(got, want) {
		t.Fatalf("two newer managers' %s, want %s", got, want)
	}
	sums = todoListSums([]listAnswer{
		listOf("assistant", 200, `{"todos":[],"closed_next_cursor":"ca","closed_has_more":true}`),
		listOf("m-b", 200, `{"todos":[],"closed_next_cursor":"cb","closed_has_more":true}`),
		listOf("m-c", 200, `{"todos":[]}`),
	})
	if got, want := string(sums["closed_cursors"]), `{"assistant":{"next_cursor":"ca","has_more":true},"m-b":{"next_cursor":"cb","has_more":true}}`; !sameJSONValue(got, want) {
		t.Fatalf("closed cursors %s, want %s", got, want)
	}
}

// sameJSONValue: a and b are the same JSON value, whatever the order of their members.
func sameJSONValue(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

// The places of every manager joined: All To-dos, Today and Upcoming once with needs_you added up, each project kept as
// its manager's and stamped with it (two managers' project:garden are two places), the counts added up. An older
// manager (405 or 404) adds nothing; when none answers with places, 404.
func TestTodoPlacesJoined(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	places := func(counts string, list ...assistantwire.TodoPlace) string {
		b, _ := json.Marshal(list)
		return `{"places":` + string(b) + `,"counts":` + counts + `}`
	}
	fixed := func(all int) []assistantwire.TodoPlace {
		return []assistantwire.TodoPlace{{ID: "all", Kind: "all", NeedsYou: all}, {ID: "today", Kind: "today"}, {ID: "upcoming", Kind: "upcoming"}}
	}
	ca := todoCountsJSON(2, 1, 0, map[string]int{"needs_you": 1, "todo": 1})
	cb := todoCountsJSON(3, 2, 1, map[string]int{"needs_you": 2, "working": 1})
	ra.set(`{"todos":[]}`, places(ca, append(fixed(1), assistantwire.TodoPlace{ID: "project:garden", Kind: "project", Title: "Garden",
		Project: "garden", NeedsYou: 1})...), 0)
	rb.set(`{"todos":[]}`, places(cb, append(fixed(2), assistantwire.TodoPlace{ID: "project:garden", Kind: "project", Title: "Garden",
		Project: "garden", NeedsYou: 2}, assistantwire.TodoPlace{ID: "project:kyoto-trip", Kind: "project", Title: "Kyoto trip",
		Project: "kyoto-trip"})...), 0)

	read := func(path string) (int, assistantwire.TodoPlaces, []byte) {
		t.Helper()
		resp, body := todoViewGet(t, h, srv, path, nil)
		var out assistantwire.TodoPlaces
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, out, body
	}
	code, got, body := read("/api/v1/todos/places")
	want := append(fixed(3),
		assistantwire.TodoPlace{ID: "project:garden", Kind: "project", Title: "Garden", Project: "garden", NeedsYou: 1, Manager: assistantwire.AssistantID},
		assistantwire.TodoPlace{ID: "project:garden", Kind: "project", Title: "Garden", Project: "garden", NeedsYou: 2, Manager: "m-0c0c0c"},
		assistantwire.TodoPlace{ID: "project:kyoto-trip", Kind: "project", Title: "Kyoto trip", Project: "kyoto-trip", Manager: "m-0c0c0c"})
	var wantCounts assistantwire.TodoCounts
	_ = json.Unmarshal([]byte(todoCountsJSON(5, 3, 1, map[string]int{"needs_you": 3, "working": 1, "todo": 1})), &wantCounts)
	if code != http.StatusOK || !reflect.DeepEqual(got.Places, want) || !reflect.DeepEqual(got.Counts, wantCounts) {
		t.Fatalf("joined places: %d %s", code, body)
	}

	// Naming a manager: its places alone, its projects still stamped.
	if code, got, body := read("/api/v1/todos/places?manager=m-0c0c0c"); code != http.StatusOK || len(got.Places) != 5 ||
		got.Places[0].NeedsYou != 2 || got.Places[0].Manager != "" || got.Places[4].Manager != "m-0c0c0c" || got.Counts.Open != 3 {
		t.Fatalf("one manager's places: %d %s", code, body)
	}

	// An older m-0c0c0c, whose mux answers 405 (PATCH and DELETE /api/v1/todos/{id} match the path), or one that
	// answers 404: the default's places alone.
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusNotFound} {
		rb.set(`{"todos":[]}`, "", status)
		code, got, body := read("/api/v1/todos/places")
		if code != http.StatusOK || len(got.Places) != 4 || got.Places[0].NeedsYou != 1 || got.Places[3].Manager != assistantwire.AssistantID ||
			got.Counts.Open != 2 {
			t.Fatalf("with an older manager (%d): %d %s", status, code, body)
		}
	}
	// No manager has the route: 404, as a hub without it.
	ra.set(`{"todos":[]}`, "", http.StatusMethodNotAllowed)
	if code, _, body := read("/api/v1/todos/places"); code != http.StatusNotFound {
		t.Fatalf("no manager has places: %d %s", code, body)
	}
	// One failed otherwise, and none answered: its answer as it is.
	rb.set(`{"todos":[]}`, "", http.StatusServiceUnavailable)
	if code, _, body := read("/api/v1/todos/places"); code != http.StatusServiceUnavailable {
		t.Fatalf("a manager failed: %d %s", code, body)
	}
}

// What a newer manager adds to the places, which this hub does not know, is passed on: a place's members, a fixed
// place of a new kind (joined by id, as All To-dos is), the answer's own members (the default's) and the counts'.
func TestTodoPlacesKeepWhatTheHubDoesNotKnow(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	ra.set(`{"todos":[]}`, `{"rev":"r1","places":[{"id":"all","kind":"all","title":"","needs_you":1,"badge":"blue"},`+
		`{"id":"someday","kind":"someday","title":"","needs_you":0},`+
		`{"id":"project:garden","kind":"project","title":"Garden","project":"garden","needs_you":1,"color":"green"}],`+
		`"counts":{"open":2,"needs_you":1,"in_progress":0,"sections":{"needs_you":1,"todo":1},"assistant_needs":1}}`, 0)
	read := func() map[string]any {
		t.Helper()
		resp, body := todoViewGet(t, h, srv, "/api/v1/todos/places", nil)
		var out map[string]any
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("places: %d %s", resp.StatusCode, body)
		}
		return out
	}
	want := func(got map[string]any, js string) {
		t.Helper()
		b, _ := json.Marshal(got)
		if !sameJSONValue(string(b), js) {
			t.Fatalf("places %s, want %s", b, js)
		}
	}
	// One manager answers (m-0c0c0c is an older one): everything as sent, its project stamped.
	want(read(), `{"rev":"r1","places":[{"id":"all","kind":"all","title":"","needs_you":1,"badge":"blue"},`+
		`{"id":"someday","kind":"someday","title":"","needs_you":0},`+
		`{"manager":"assistant","id":"project:garden","kind":"project","title":"Garden","project":"garden","needs_you":1,"color":"green"}],`+
		`"counts":{"open":2,"needs_you":1,"in_progress":0,"sections":{"needs_you":1,"todo":1},"assistant_needs":1}}`)

	// Two: the new fixed place once with needs_you added up, the default's members first, the counts added up.
	rb.set(`{"todos":[]}`, `{"rev":"r2","places":[{"id":"all","kind":"all","title":"","needs_you":2,"badge":"gray"},`+
		`{"id":"someday","kind":"someday","title":"","needs_you":3}],`+
		`"counts":{"open":3,"needs_you":2,"in_progress":1,"sections":{"needs_you":2,"working":1},"assistant_needs":2}}`, 0)
	want(read(), `{"rev":"r1","places":[{"id":"all","kind":"all","title":"","needs_you":3,"badge":"blue"},`+
		`{"id":"someday","kind":"someday","title":"","needs_you":3},`+
		`{"manager":"assistant","id":"project:garden","kind":"project","title":"Garden","project":"garden","needs_you":1,"color":"green"}],`+
		`"counts":{"open":5,"needs_you":3,"in_progress":1,"sections":{"needs_you":3,"todo":1,"working":1},"assistant_needs":3}}`)
}

// One to-do, and its timeline, is relayed to the manager that listed it (byTodo), as the other to-do routes are, with
// no hub code: the to-do's answer is passed on as its manager wrote it, an older manager's (the row alone) and a newer
// one's (with the paper's rounds, #10465) alike, and so is an older manager's answer to the timeline (its mux has no
// such route: 404).
func TestTodoItemRelayedToItsManager(t *testing.T) {
	h, srv, _, b, ra, rb := todoViewHub(t)
	ra.set(`{"todos":[{"id":"todo_a1"}]}`, "", 0)
	rb.set(`{"todos":[{"id":"todo_b1"},{"id":"todo_b2"}]}`, "", 0)
	older, newer := `{"todo":{"id":"todo_b1","title":"Water the roses"}}`,
		`{"todo":{"id":"todo_b2","title":"Prune the hedge"},"rounds":[],"rounds_total":0}`
	rb.mu.Lock()
	rb.items = map[string]string{"/api/v1/todos/todo_b1": older, "/api/v1/todos/todo_b2": newer}
	rb.mu.Unlock()
	if resp, body := todoViewGet(t, h, srv, "/api/v1/todos", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/api/v1/todos/todo_b1", "/api/v1/todos/todo_b1/timeline?before=r%3Afeed%3Af1&limit=5"} {
		resp, body := todoViewGet(t, h, srv, path, nil)
		calls := archiveHTTPCalls(t, b)
		last := calls[len(calls)-1]
		if want, query, _ := strings.Cut(path, "?"); last.Method != http.MethodGet || last.Path != want || last.Query != query {
			t.Fatalf("m-0c0c0c got %+v for %s", last, path) // the page asked for (before, limit) reaches the manager
		}
		if strings.Contains(path, "/timeline") && (resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "Not Found")) {
			t.Fatalf("an older manager's timeline: %d %s", resp.StatusCode, body)
		}
	}
	for path, want := range map[string]string{"/api/v1/todos/todo_b1": older, "/api/v1/todos/todo_b2": newer} {
		if resp, body := todoViewGet(t, h, srv, path, nil); resp.StatusCode != http.StatusOK || !sameJSONValue(string(body), want) {
			t.Errorf("%s: %d %s, want %s", path, resp.StatusCode, body, want)
		}
	}
}

// A hub from before #10465 has no GET /api/v1/todos/{id}/timeline: it answers 404 to any method there itself, which
// clients take as "not there", while the to-do still goes to its manager. Built as such a hub is, without the route.
func TestTodoTimelineOnAnOlderHub(t *testing.T) {
	// The hub reads the route list when it is built; the hub's tests run one at a time (none calls t.Parallel), so
	// swapping the list for this one hub reaches no other.
	routes := assistantwire.Routes
	t.Cleanup(func() { assistantwire.Routes = routes })
	assistantwire.Routes = slices.DeleteFunc(slices.Clone(routes), func(p string) bool { return p == "GET /api/v1/todos/{id}/timeline" })
	h, srv, a, _, ra, _ := todoViewHub(t)
	ra.set(`{"todos":[{"id":"todo_a1"}]}`, "", 0)
	if resp, body := todoViewGet(t, h, srv, "/api/v1/todos", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	before := len(archiveHTTPCalls(t, a))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(method, srv.URL+"/api/v1/todos/todo_a1/timeline", nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s on an older hub: %d %s", method, resp.StatusCode, body)
		}
	}
	if calls := archiveHTTPCalls(t, a)[before:]; len(calls) != 0 {
		t.Fatalf("an older hub relayed the timeline: %+v", calls)
	}
	todoViewGet(t, h, srv, "/api/v1/todos/todo_a1", nil)
	if calls := archiveHTTPCalls(t, a)[before:]; len(calls) != 1 || calls[0].Path != "/api/v1/todos/todo_a1" {
		t.Fatalf("the to-do itself: %+v", calls)
	}
}

// The pushed web read checks the merged managers' revisions, not their full lists (#13804). An unchanged conditional
// read is a 304 without one manager rebuilding /todos; a change returns a new hub-wide token; an older manager fails the
// cheap route closed so the web falls back to the original full read rather than trusting a partial answer.
func TestTodoRevisionMergedWithoutFullReads(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	ra.set(`{"todos":[{"id":"todo_a1"}]}`, "", 0)
	rb.set(`{"todos":[{"id":"todo_b1"}]}`, "", 0)
	ra.setRevision(`W/"manager-a1"`)
	rb.setRevision(`W/"manager-b1"`)

	full, body := todoViewGet(t, h, srv, "/api/v1/todos", nil)
	tag := full.Header.Get("X-Persona-Todos-Revision")
	if full.StatusCode != http.StatusOK || tag == "" {
		t.Fatalf("full list: %d, revision %q, %s", full.StatusCode, tag, body)
	}
	aFull, _ := ra.reads()
	bFull, _ := rb.reads()

	same, body := todoViewGet(t, h, srv, "/api/v1/todos/revision", map[string]string{"If-None-Match": tag})
	if same.StatusCode != http.StatusNotModified || len(body) != 0 || same.Header.Get("ETag") != tag {
		t.Fatalf("unchanged: %d %q %s", same.StatusCode, same.Header.Get("ETag"), body)
	}
	for name, r := range map[string]*todoReplies{"default": ra, "other": rb} {
		fullReads, revisionReads := r.reads()
		wantFull := aFull
		if name == "other" {
			wantFull = bFull
		}
		if fullReads != wantFull || revisionReads != 1 {
			t.Errorf("%s reads: full %d→%d, revision %d", name, wantFull, fullReads, revisionReads)
		}
	}

	rb.setRevision(`W/"manager-b2"`)
	changed, body := todoViewGet(t, h, srv, "/api/v1/todos/revision", map[string]string{"If-None-Match": tag})
	if changed.StatusCode != http.StatusOK || changed.Header.Get("ETag") == "" || changed.Header.Get("ETag") == tag {
		t.Fatalf("changed: %d %q %s", changed.StatusCode, changed.Header.Get("ETag"), body)
	}
	if a, _ := ra.reads(); a != aFull {
		t.Fatalf("a changed revision read built the default's full list: %d→%d", aFull, a)
	}
	if b, _ := rb.reads(); b != bFull {
		t.Fatalf("a changed revision read built the other full list: %d→%d", bFull, b)
	}

	rb.setRevision("") // rolling upgrade: one old manager means no trustworthy merged revision
	unsupported, _ := todoViewGet(t, h, srv, "/api/v1/todos/revision", map[string]string{"If-None-Match": tag})
	if unsupported.StatusCode != http.StatusConflict {
		t.Fatalf("mixed managers: %d, want 409 so the client falls back", unsupported.StatusCode)
	}
	mixed, _ := todoViewGet(t, h, srv, "/api/v1/todos", nil)
	if got := mixed.Header.Get("X-Persona-Todos-Revision"); got != "" {
		t.Fatalf("mixed full list passed off the base manager's token as hub-wide: %q", got)
	}
}

func TestTodoRevisionFeatureNeedsEveryManager(t *testing.T) {
	h, _, a, b, _, _ := todoViewHub(t)
	listed := func() bool { return slices.Contains(h.features(), "todo_revision") }
	if listed() {
		t.Fatal("listed before either manager named todo_revision")
	}
	todoViewCapabilities(t, a, "todo_revision")
	if listed() {
		t.Fatal("listed while the other manager is old")
	}
	todoViewCapabilities(t, b, "todo_revision")
	if !listed() {
		t.Fatal("not listed with every manager capable")
	}
	todoViewCapabilities(t, b)
	if listed() {
		t.Fatal("still listed after one manager stopped naming it")
	}
}
