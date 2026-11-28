package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The project tree over HTTP and the assistant's tools (#12927): the list carries each project's parent and
// milestones, the detail its tree, a refused parent a code, and each change the update that undoes it.
func TestKBProjectTreeHTTPAndTools(t *testing.T) {
	s := newKBService(t, newFakeHub())
	tool := func(raw string) string {
		t.Helper()
		out, err := runTool(s, "example", "kb_update", json.RawMessage(raw))
		if err != nil {
			t.Fatalf("kb_update %s: %v", raw, err)
		}
		return out
	}
	for _, name := range []string{"Garden app", "Seeds", "Docs site"} {
		tool(`{"kind":"projects","id":"` + name + `","goal":"x"}`)
	}
	out := tool(`{"kind":"projects","id":"Seeds","parent":"Garden app"}`)
	if !strings.Contains(out, "Now under Garden app (garden-app), level 2.") || !strings.Contains(out, `To undo, if the user asks: kb_update {"id":"seeds","kind":"projects","parent":""}`) {
		t.Fatalf("kb_update said: %s", out)
	}
	out = tool(`{"kind":"projects","id":"seeds","milestone":{"title":"Beta out","target":"2026-12-15"}}`)
	if !strings.Contains(out, "Beta out [open]") || !strings.Contains(out, `"milestones":[]`) {
		t.Fatalf("milestone said: %s", out)
	}
	got, err := runTool(s, "example", "kb_get", json.RawMessage(`{"kind":"projects","id":"garden-app"}`))
	if err != nil || !strings.Contains(got, "- child: seeds · Seeds [active]") {
		t.Fatalf("kb_get: %v %s", err, got)
	}

	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			Parent     string `json:"parent"`
			Milestones []struct {
				Title, State string
			} `json:"milestones"`
		} `json:"items"`
	}
	w := do(http.MethodGet, "/api/v1/kb/projects", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	parents := map[string]string{}
	for _, x := range list.Items {
		parents[x.ID] = x.Parent
		if x.ID == "seeds" && (len(x.Milestones) != 1 || x.Milestones[0].State != "open") {
			t.Fatalf("seeds' milestones: %+v", x.Milestones)
		}
	}
	if parents["seeds"] != "garden-app" || parents["garden-app"] != "" || len(parents) != 3 {
		t.Fatalf("parents = %v", parents)
	}
	var detail struct {
		Tree struct {
			Parent     *struct{ ID, Name string }   `json:"parent"`
			Children   []struct{ ID string }        `json:"children"`
			Milestones []struct{ ID, Title string } `json:"milestones"`
			Depth      int                          `json:"depth"`
		} `json:"tree"`
	}
	w = do(http.MethodGet, "/api/v1/kb/projects/garden-app", "")
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.Tree.Children) != 1 || detail.Tree.Depth != 1 || detail.Tree.Parent != nil {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	w = do(http.MethodPut, "/api/v1/kb/projects/garden-app", `{"parent":"seeds"}`)
	var refused struct{ Code, Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || w.Code != http.StatusBadRequest || refused.Code != "parent_cycle" || refused.Error == "parent_cycle" {
		t.Fatalf("cycle: %d %s", w.Code, w.Body)
	}
	w = do(http.MethodPut, "/api/v1/kb/projects/docs-site", `{"parent":"garden-app"}`)
	var moved struct {
		Undo struct {
			Parent *string `json:"parent"`
		} `json:"undo"`
		Tree struct{ Depth int } `json:"tree"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &moved); err != nil || w.Code != http.StatusOK || moved.Undo.Parent == nil || *moved.Undo.Parent != "" || moved.Tree.Depth != 2 {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), `"relevance"`) {
		t.Fatalf("the undo carries more than the tree: %s", w.Body)
	}
}
