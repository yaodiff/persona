package teamdocs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowseQueryValidation(t *testing.T) {
	const documents = "/v1/projects/alpha/documents"
	const approvals = documents + "/Guide/approvals"
	for _, target := range []string{
		documents + "?limit=0", documents + "?limit=101", documents + "?limit=1.5",
		documents + "?after=", documents + "?after=bad%2Fid", documents + "?after=a&after=b",
		documents + "?limit=1&limit=2", documents + "?limit=%ZZ", documents + "?before=3",
		approvals + "?before=0", approvals + "?before=-1", approvals + "?before=1.5",
		approvals + "?before=9223372036854775808", approvals + "?after=a",
	} {
		t.Run(target, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			request.Header.Set("Authorization", "Bearer fixture")
			response := httptest.NewRecorder()
			Handler(nil).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Errorf("GET %s returned %d, want 400", target, response.Code)
			}
		})
	}
	for _, tc := range []struct {
		query string
		want  int
	}{{"", 25}, {"limit=1", 1}, {"limit=100", 100}} {
		_, limit, err := pageQuery(tc.query, "after")
		if err != nil || limit != tc.want {
			t.Errorf("pageQuery(%q) = limit %d, err %v; want %d", tc.query, limit, err, tc.want)
		}
	}
}

func TestBrowseApprovedProjectDocuments(t *testing.T) {
	store := newStoreTest(t)
	ctx := t.Context()
	reader := grantStoreToken(t, store, "alpha", "tester", roleReader)
	writer := grantStoreToken(t, store, "alpha", "product", roleWriter)
	approver := grantStoreToken(t, store, "alpha", "lead", roleApprover)
	beta := grantStoreToken(t, store, "beta", "lead", roleApprover)
	handler := Handler(store)

	const documents = "/v1/projects/alpha/documents"
	const history = documents + "/Guide/approvals"
	get := func(token, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s returned %d %s (want %d, no-store)", path, response.Code, response.Body, status)
		}
		return response
	}

	page := func(path string) approvalPage {
		t.Helper()
		response := get(reader, path, http.StatusOK)
		// Disallow unknown fields so the test catches accidentally exposing revision bodies.
		decoder := json.NewDecoder(response.Body)
		decoder.DisallowUnknownFields()
		var result approvalPage
		if err := decoder.Decode(&result); err != nil || result.Items == nil {
			t.Fatalf("invalid page for %s: %+v, %v", path, result, err)
		}
		return result
	}

	if got := get(reader, documents, http.StatusOK).Body.String(); strings.TrimSpace(got) != `{"items":[]}` {
		t.Fatalf("empty catalog = %s", got)
	}

	publish := func(document string, expected int64, approve bool) Revision {
		t.Helper()
		revision, err := store.Publish(ctx, writer, "alpha", document, expected, fmt.Sprintf("%s revision %d", document, expected+1))
		requireStoreError(t, err, nil)
		if approve {
			revision, err = store.Approve(ctx, approver, "alpha", document, revision.Number)
			requireStoreError(t, err, nil)
		}
		return revision
	}

	first := publish("Guide", 0, true)
	publish("Guide", 1, false)
	third := publish("Guide", 2, true)
	publish("Guide", 3, false)
	publish("Draft", 0, false)
	publish("guide", 0, true)
	publish("_notes", 0, true)
	_, err := store.Publish(ctx, beta, "beta", "Foreign", 0, "other project")
	requireStoreError(t, err, nil)
	_, err = store.Approve(ctx, beta, "beta", "Foreign", 1)
	requireStoreError(t, err, nil)

	checkEntry := func(got approvedRevision, want Revision) {
		t.Helper()
		if got.Project != want.Project || got.Document != want.Document || got.Number != want.Number ||
			got.SHA256 != want.SHA256 || got.Author != want.Author || got.ApprovedBy != want.ApprovedBy ||
			!got.ApprovedAt.Equal(*want.ApprovedAt) {
			t.Fatalf("approval metadata = %+v, want metadata for %s r%d", got, want.Document, want.Number)
		}
	}
	catalog := page(documents + "?limit=2")
	if len(catalog.Items) != 2 || catalog.Next != "_notes" || catalog.Items[1].Document != "_notes" {
		t.Fatalf("first catalog page = %+v", catalog)
	}
	checkEntry(catalog.Items[0], third)
	publish("A-new", 0, true)
	last := page(documents + "?limit=2&after=" + catalog.Next)
	if len(last.Items) != 1 || last.Next != "" || last.Items[0].Document != "guide" {
		t.Fatalf("last catalog page after concurrent insert = %+v", last)
	}

	revisions := page(history + "?limit=1")
	if len(revisions.Items) != 1 || revisions.Next != "3" {
		t.Fatalf("first history page = %+v", revisions)
	}
	checkEntry(revisions.Items[0], third)
	publish("Guide", 4, true)
	last = page(history + "?limit=1&before=" + revisions.Next)
	if len(last.Items) != 1 || last.Next != "" {
		t.Fatalf("last history page after later approval = %+v", last)
	}
	checkEntry(last.Items[0], first)
	var pinned Revision
	if err := json.Unmarshal(get(reader, documents+"/Guide/revisions/1", http.StatusOK).Body.Bytes(), &pinned); err != nil || pinned.Number != first.Number || pinned.Content != first.Content || pinned.SHA256 != first.SHA256 {
		t.Fatalf("could not open pinned approval: %v", err)
	}
	for _, path := range []string{documents + "/Draft/approvals", documents + "/Foreign/approvals", history + "?before=1", documents + "?after=zzzz"} {
		if empty := page(path); len(empty.Items) != 0 || empty.Next != "" {
			t.Fatalf("expected empty page for %s, got %+v", path, empty)
		}
	}
	for _, path := range []string{documents, history} {
		get(beta, path, http.StatusUnauthorized)
	}
	requireStoreError(t, store.Revoke(ctx, "alpha", "tester"), nil)
	for _, path := range []string{documents, history} {
		get(reader, path, http.StatusUnauthorized)
	}
}
