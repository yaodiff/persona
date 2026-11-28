package repositorystore

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

func createLinkProvenanceTodo(t *testing.T, d *Store, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := d.CreateTodo(t.Context(), db.TodoRow{ID: id, Title: "Work", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func assertStoredLinkProvenance(t *testing.T, d *Store, id string, want map[string]string) {
	t.Helper()
	var raw []byte
	if err := d.QueryRow(t.Context(), `SELECT repositories FROM todo_repository_sets WHERE todo_id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Links []struct {
			URL        string `json:"canonical_url"`
			Provenance string `json:"provenance"`
		} `json:"links"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		for _, link := range row.Links {
			got[link.URL] = link.Provenance
		}
	}
	if !maps.Equal(got, want) {
		t.Fatalf("persisted link provenance = %v, want %v", got, want)
	}
}

func TestImportTodoRepositoryLinkProvenance(t *testing.T) {
	d := open(t)
	const pr = "https://github.com/example/web/pull/7"
	for _, tc := range []struct{ initial, binding, role string }{
		{"report", "explicit", ""},
		{"report", "report", repository.LinkRoleDelivery},
		{"legacy", "legacy", repository.LinkRoleDelivery},
	} {
		t.Run(tc.initial+"_to_"+tc.binding, func(t *testing.T) {
			id := t.Name()
			ctx := t.Context()
			createLinkProvenanceTodo(t, d, id)
			// Reported PR claims are exclusive across open to-dos.
			t.Cleanup(func() {
				if err := d.DeleteTodo(context.Background(), id); err != nil {
					t.Error(err)
				}
			})
			mirror, changed, err := d.ImportTodoRepositoryLinks(ctx, id, []string{pr}, tc.initial)
			if err != nil || !changed {
				t.Fatalf("import mirror: %v, changed=%v", err, changed)
			}
			assertStoredLinkProvenance(t, d, id, map[string]string{pr: "unbound"})

			binding := []repository.LinkInput{{URL: "https://GitHub.com/Example/Web/pull/7", Role: tc.role}}
			bound, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, id, binding, tc.binding)
			if err != nil || !changed || bound.Revision != mirror.Revision+1 {
				t.Fatalf("bind mirror: %v, changed=%v, revision=%d", err, changed, bound.Revision)
			}
			assertStoredLinkProvenance(t, d, id, map[string]string{pr: "explicit"})
			if again, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, id, binding, tc.binding); err != nil || changed || again.Revision != bound.Revision {
				t.Fatalf("repeat binding: %v, changed=%v, revision=%d", err, changed, again.Revision)
			}
			if _, _, err := d.ImportTodoRepositoryLinks(ctx, id, []string{pr}, "report"); err != nil {
				t.Fatal(err)
			}
			assertStoredLinkProvenance(t, d, id, map[string]string{pr: "explicit"})
			raw, err := json.Marshal(bound)
			if err != nil || bytes.Contains(raw, []byte(`"provenance"`)) {
				t.Fatalf("repository response exposes storage metadata: %s, %v", raw, err)
			}
		})
	}
}

func TestSetTodoRepositoryLinksDiffPreservesProvenance(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	const id = "edit-link-provenance"
	const base = "https://github.com/example/web/pull/"
	const edited, sibling, reference, removed, added = base + "1", base + "2", base + "3", base + "4", base + "5"
	createLinkProvenanceTodo(t, d, id)
	if _, _, err := d.ImportTodoRepositoryLinks(ctx, id, []string{edited, sibling}, "report"); err != nil {
		t.Fatal(err)
	}
	set, _, err := d.ImportTodoRepositoryLinkDetails(ctx, id, []repository.LinkInput{
		{URL: reference, Role: repository.LinkRoleReference}, {URL: removed},
	}, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	repoID := set.Repositories[0].RepoID
	set, err = d.SetTodoRepositoryLinks(ctx, id, repoID, repository.LinksInput{
		ExpectedRevision: &set.Revision,
		Links: []repository.LinkInput{
			{URL: edited, Title: "Updated title"}, {URL: sibling, Role: repository.LinkRoleDelivery},
			{URL: reference, Role: repository.LinkRoleDelivery}, {URL: added},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStoredLinkProvenance(t, d, id, map[string]string{edited: "unbound", sibling: "unbound", reference: "explicit", added: "explicit"})

	if _, err := d.SetTodoRepositoryLinks(ctx, id, repoID, repository.LinksInput{
		ExpectedRevision: &set.Revision,
		Links: []repository.LinkInput{
			{URL: edited}, {URL: sibling}, {URL: reference, Role: repository.LinkRoleReference},
			{URL: added, Title: "An explicit link's updated title"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, id, []repository.LinkInput{
		{URL: removed, Role: repository.LinkRoleDelivery},
	}, "report"); err != nil || changed {
		t.Fatalf("report restored a removed binding: %v, changed=%v", err, changed)
	}
	assertStoredLinkProvenance(t, d, id, map[string]string{edited: "unbound", sibling: "unbound", reference: "unbound", added: "explicit"})
}
