package assistant

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's pictures (#11084) use the existing image input ({id,mime,data}) and private media-read contract. The
// response carries metadata and a manager-relative URL, never the bytes or a machine-local path in every list row.
type todoImageView struct {
	ID     string `json:"id"`
	Mime   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URL    string `json:"url"`
	Data   string `json:"data"`
	Path   string `json:"path"`
}

type todoWithImages struct {
	ID     string          `json:"id"`
	Title  string          `json:"title"`
	Notes  string          `json:"notes"`
	Images []todoImageView `json:"images"`
}

func todoImageInput(id, mime, data string) map[string]any {
	return map[string]any{"id": id, "mime": mime, "data": data}
}

func postTodoImages(t *testing.T, e *todoEnv, role, op string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/todos", bytes.NewReader(raw))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	if op != "" {
		r.Header.Set("X-Persona-Op-Id", op)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

func decodeTodoWithImages(t *testing.T, raw []byte) todoWithImages {
	t.Helper()
	var out struct{ Todo todoWithImages }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode to-do: %v: %s", err, raw)
	}
	return out.Todo
}

func getTodoWithImages(t *testing.T, e *todoEnv, id string) todoWithImages {
	t.Helper()
	return decodeTodoWithImages(t, e.must(http.MethodGet, "/api/v1/todos/"+id, "").Body.Bytes())
}

func checkTodoImageViews(t *testing.T, got []todoImageView, want []map[string]any, width, height int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("images = %+v, want %d", got, len(want))
	}
	for i, img := range got {
		if img.ID != want[i]["id"] || img.Mime != want[i]["mime"] || img.Width != width || img.Height != height {
			t.Errorf("image %d = %+v", i, img)
		}
		if img.URL == "" || !strings.HasPrefix(img.URL, "/api/v1/todos/") || strings.HasPrefix(img.URL, "//") {
			t.Errorf("image %d URL %q is not a manager-relative to-do media URL", i, img.URL)
		}
		if img.Data != "" || img.Path != "" {
			t.Errorf("image %d list metadata leaks bytes or a local path: %+v", i, img)
		}
	}
}

func TestTodoImagesCreateReadAndReload(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		images := []map[string]any{
			todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", pngData(t, 2, 3)),
			todoImageInput("d0000000-0000-4000-8000-000000000002", "image/jpeg", jpegData(t, 2, 3)),
		}
		w := postTodoImages(t, e, "", "d0000000-0000-4000-8000-000000000010", map[string]any{
			"title": "Fix the clipped screen", "notes": "The screenshots show the two broken states.", "images": images,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("create with images: %d %s", w.Code, w.Body)
		}
		created := decodeTodoWithImages(t, w.Body.Bytes())
		if created.ID == "" || created.Title != "Fix the clipped screen" || created.Notes != "The screenshots show the two broken states." {
			t.Fatalf("created = %+v", created)
		}
		checkTodoImageViews(t, created.Images, images, 2, 3)

		// Creating, including uploading the pictures, is not an implicit Agent handoff or external message.
		e.f.mu.Lock()
		starts, agentSends := len(e.f.starts), len(e.f.agentSent)
		e.f.mu.Unlock()
		if starts != 0 || agentSends != 0 {
			t.Fatalf("create started/sent to an Agent: starts=%d sends=%d", starts, agentSends)
		}

		read := func() todoWithImages { return getTodoWithImages(t, e, created.ID) }
		fresh := read()
		checkTodoImageViews(t, fresh.Images, images, 2, 3)
		if !reflect.DeepEqual(fresh.Images, created.Images) {
			t.Fatalf("fresh GET changed the image association:\ncreate %+v\nGET    %+v", created.Images, fresh.Images)
		}
		for i, img := range fresh.Images {
			want, err := base64.StdEncoding.DecodeString(images[i]["data"].(string))
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"", "answer"} { // the same caller/manager read path as the to-do
				got := e.doAs(role, http.MethodGet, img.URL, "")
				if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), want) {
					t.Fatalf("GET %s as %q: %d, %d bytes", img.URL, role, got.Code, got.Body.Len())
				}
				if got.Header().Get("Content-Type") != images[i]["mime"] || got.Header().Get("X-Content-Type-Options") != "nosniff" ||
					got.Header().Get("Cache-Control") != "private, no-store" {
					t.Errorf("GET %s headers = %v", img.URL, got.Header())
				}
			}
		}

		if database != nil {
			reloadTodos(t, e.s) // a manager restart, not merely a second read of its cache
			again := read()
			checkTodoImageViews(t, again.Images, images, 2, 3)
			if !reflect.DeepEqual(again.Images, created.Images) {
				t.Fatalf("reload lost the persisted image association: %+v", again.Images)
			}
		}

		// An old/no-image client keeps the old request and response shape; additive metadata is not required of it.
		plain := postTodoImages(t, e, "", "", map[string]any{"title": "Plain to-do", "notes": "unchanged"})
		if plain.Code != http.StatusOK {
			t.Fatalf("plain create: %d %s", plain.Code, plain.Body)
		}
		var raw struct{ Todo map[string]json.RawMessage }
		if err := json.Unmarshal(plain.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if images, present := raw.Todo["images"]; present && string(images) != "[]" && string(images) != "null" {
			t.Fatalf("no-image create changed the response with %s", images)
		}
	})
}

// A task_plan start on the to-do uses the same stored material as the app's manual new/existing-Agent handoffs.
func TestTodoTaskPlanCarriesImages(t *testing.T) {
	e := newTodoEnv(t, nil)
	data := pngData(t, 2, 3)
	created := decodeTodoWithImages(t, postTodoImages(t, e, "", "", map[string]any{
		"title": "Inspect the screenshot", "images": []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", data)},
	}).Body.Bytes())
	if _, _, err := e.s.startTaskAgent(t.Context(), &PlanTask{TodoID: created.ID, Prompt: "Read the attached picture", Machine: "server", Cwd: "/code", Readonly: true}, false); err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	starts := append([]map[string]any(nil), e.f.starts...)
	e.f.mu.Unlock()
	if len(starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(starts))
	}
	raw, err := json.Marshal(starts[0]["images"])
	if err != nil {
		t.Fatal(err)
	}
	var images []proto.Image
	if err := json.Unmarshal(raw, &images); err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Mime != "image/png" || images[0].Data != data {
		t.Fatalf("task_plan images = %#v, want the stored PNG", starts[0]["images"])
	}
}

func TestTodoImageValidationCreatesNothing(t *testing.T) {
	png := pngData(t, 1, 1)
	tooMany := make([]map[string]any, kb.MaxPhotos+1)
	for i := range tooMany {
		tooMany[i] = todoImageInput(fmt.Sprintf("d0000000-0000-4000-8000-%012d", i+1), "image/png", png)
	}
	for _, tc := range []struct {
		name   string
		images any
		want   int
	}{
		{"not an image", []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", base64.StdEncoding.EncodeToString([]byte("not a PNG")))}, http.StatusBadRequest},
		{"unsupported MIME", []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/gif", png)}, http.StatusBadRequest},
		{"false MIME label", []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/jpeg", png)}, http.StatusBadRequest},
		{"invalid base64", []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", "not base64!")}, http.StatusBadRequest},
		{"empty image", []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", "")}, http.StatusBadRequest},
		{"oversize image", []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", base64.StdEncoding.EncodeToString(make([]byte, maxPhotoBytes+1)))}, http.StatusRequestEntityTooLarge},
		{"too many images", tooMany, http.StatusRequestEntityTooLarge},
		{"malformed image entry", []any{map[string]any{"id": "d0000000-0000-4000-8000-000000000001", "mime": "image/png", "data": 42}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTodoEnv(t, nil)
			w := postTodoImages(t, e, "", "", map[string]any{"title": "Must not be created", "notes": "retain this only in the dialog", "images": tc.images})
			if w.Code != tc.want {
				t.Errorf("create = %d %s, want %d", w.Code, w.Body, tc.want)
			}
			if got := e.list(); len(got) != 0 {
				t.Fatalf("rejected images left a partial to-do: %+v", got)
			}
			e.f.mu.Lock()
			starts, sends := len(e.f.starts), len(e.f.agentSent)
			e.f.mu.Unlock()
			if starts != 0 || sends != 0 {
				t.Fatalf("rejected create reached an Agent: starts=%d sends=%d", starts, sends)
			}
		})
	}
}

func TestTodoImageCreateReadOnlyCreatesNothing(t *testing.T) {
	e := newTodoEnv(t, nil)
	w := postTodoImages(t, e, "answer", "", map[string]any{
		"title": "Watch must not write", "images": []map[string]any{todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", pngData(t, 1, 1))},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("read-only create = %d %s", w.Code, w.Body)
	}
	if got := e.list(); len(got) != 0 {
		t.Fatalf("read-only create left a to-do: %+v", got)
	}
}

// Repository-enabled creation uses the same image contract and the same atomic write as ordinary creation. A bad
// association cannot leave a to-do whose pictures need later reconciliation; a good one survives a manager restart.
func TestTodoRepositoryImagesCreateAtomically(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, _ := repositoryService(t, database)
	w := placeDo(s, "", http.MethodPost, repoBase, `{"url":"https://github.com/example/web"}`)
	var saved struct{ Repository RepositoryBinding }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatalf("save repository: %d %s", w.Code, w.Body)
	}
	image := todoImageInput("d0000000-0000-4000-8000-000000000001", "image/png", pngData(t, 2, 3))
	create := func(repoID string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{
			"title": "Linked screenshot work", "notes": "keep the image and association together", "images": []map[string]any{image},
			"repository_bindings": map[string]any{"catalog_revision": 1, "repositories": []map[string]any{{"repo_id": repoID, "links": []map[string]any{{"kind": "issue", "number": "7"}}}}},
		})
		return placeDo(s, "", http.MethodPost, "/api/v1/todos", string(raw))
	}
	if w := create("foreign_repo"); w.Code != http.StatusNotFound {
		t.Fatalf("bad repository with an image = %d %s", w.Code, w.Body)
	}
	if rows, err := database.ListTodos(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("bad repository left a partial to-do/image association: %+v, %v", rows, err)
	}
	w = create(saved.Repository.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("repository create with image: %d %s", w.Code, w.Body)
	}
	created := decodeTodoWithImages(t, w.Body.Bytes())
	checkTodoImageViews(t, created.Images, []map[string]any{image}, 2, 3)
	restarted, _ := repositoryService(t, database)
	e := &todoEnv{t: t, s: restarted}
	fresh := getTodoWithImages(t, e, created.ID)
	checkTodoImageViews(t, fresh.Images, []map[string]any{image}, 2, 3)
	if rows, err := database.ListTodos(t.Context()); err != nil || len(rows) != 1 {
		t.Fatalf("stored to-dos = %+v, %v", rows, err)
	}
}
