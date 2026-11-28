package repositorystore

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

// A title-less PUT that carries an explicit delivery role must switch a stored reference link back to delivery;
// NormalizeLink stores delivery as the empty value, so only the raw input can distinguish this from a role-less
// older-client edit (P1 review finding on PR #12395).
func TestSetTodoRepositoryLinksExplicitDeliveryRoleReplacesReference(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	identity, _ := repository.Parse("https://github.com/example/web", "")
	catalog := repository.Catalog{Revision: 1, Repositories: []repository.CatalogEntry{{Identity: identity, ID: "web"}}}
	if err := d.PutSetting(ctx, repository.CatalogKey, catalog); err != nil {
		t.Fatal(err)
	}
	row := db.TodoRow{ID: "role-edit", Title: "Work", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	url := identity.CanonicalURL + "/pull/7"
	imported, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, row.ID,
		[]repository.LinkInput{{URL: url, Title: "Keep this title", Role: repository.LinkRoleReference}}, "explicit")
	if err != nil || !changed {
		t.Fatal(imported, changed, err)
	}
	repoID := imported.Repositories[0].RepoID

	// A role-less older-client edit must keep the stored reference role and the title.
	kept, err := d.SetTodoRepositoryLinks(ctx, row.ID, repoID, repository.LinksInput{
		ExpectedRevision: &imported.Revision,
		Links:            []repository.LinkInput{{URL: url}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := kept.Repositories[0].Links[0]; got.Role != repository.LinkRoleReference || got.Title != "Keep this title" {
		t.Fatalf("role-less edit changed metadata: %+v", got)
	}

	// The explicit title-less switch back to delivery must take effect while the title stays preserved.
	switched, err := d.SetTodoRepositoryLinks(ctx, row.ID, repoID, repository.LinksInput{
		ExpectedRevision: &kept.Revision,
		Links:            []repository.LinkInput{{URL: url, Role: repository.LinkRoleDelivery}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := switched.Repositories[0].Links[0]; got.Role != "" || got.Title != "Keep this title" {
		t.Fatalf("explicit delivery role did not replace reference: %+v", got)
	}
}

// The single-link role rewrite changes only its target inside one transaction: another link's concurrent role and
// the revision bookkeeping survive, and an unknown URL is a no-op rather than an error (P2 review finding).
func TestSetTodoRepositoryLinkRoleTouchesOnlyTargetLink(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	identity, _ := repository.Parse("https://github.com/example/web", "")
	catalog := repository.Catalog{Revision: 1, Repositories: []repository.CatalogEntry{{Identity: identity, ID: "web"}}}
	if err := d.PutSetting(ctx, repository.CatalogKey, catalog); err != nil {
		t.Fatal(err)
	}
	row := db.TodoRow{ID: "single-role", Title: "Work", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	first := identity.CanonicalURL + "/pull/1"
	second := identity.CanonicalURL + "/pull/2"
	imported, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, row.ID, []repository.LinkInput{
		{URL: first, Role: repository.LinkRoleDelivery},
		{URL: second, Role: repository.LinkRoleReference},
	}, "explicit")
	if err != nil || !changed {
		t.Fatal(imported, changed, err)
	}
	before := imported.Revision

	updated, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, first, repository.LinkRoleReference)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != before+1 {
		t.Fatalf("revision = %d, want %d", updated.Revision, before+1)
	}
	roles := map[string]string{}
	for _, l := range updated.Repositories[0].Links {
		roles[l.CanonicalURL] = l.Role
	}
	if roles[first] != repository.LinkRoleReference {
		t.Fatalf("target link role = %q, want reference", roles[first])
	}
	if roles[second] != repository.LinkRoleReference {
		t.Fatalf("other link role changed to %q", roles[second])
	}

	// Re-applying the same role must not bump the revision.
	same, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, first, repository.LinkRoleReference)
	if err != nil || same.Revision != updated.Revision {
		t.Fatalf("idempotent rewrite changed state: rev %d err %v", same.Revision, err)
	}
	// An unknown URL changes nothing and reports no error.
	miss, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, identity.CanonicalURL+"/pull/999", repository.LinkRoleReference)
	if err != nil || miss.Revision != updated.Revision {
		t.Fatalf("unknown URL rewrite: rev %d err %v", miss.Revision, err)
	}
}

// A delivery write can bind a mirror that already has the delivery role. A repeated binding is idempotent.
func TestSetTodoRepositoryLinkRoleBindsMirror(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	identity, _ := repository.Parse("https://github.com/example/web", "")
	catalog := repository.Catalog{Revision: 1, Repositories: []repository.CatalogEntry{{Identity: identity, ID: "web"}}}
	if err := d.PutSetting(ctx, repository.CatalogKey, catalog); err != nil {
		t.Fatal(err)
	}
	row := db.TodoRow{ID: "delivery-noop", Title: "Work", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	url := identity.CanonicalURL + "/pull/3"
	imported, changed, err := d.ImportTodoRepositoryLinks(ctx, row.ID, []string{url}, "report")
	if err != nil || !changed {
		t.Fatal(imported, changed, err)
	}
	if got := imported.Repositories[0].Links[0].Role; got != "" {
		t.Fatalf("seed role = %q, want empty stored delivery", got)
	}
	bound, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, url, repository.LinkRoleDelivery)
	if err != nil || bound.Revision != imported.Revision+1 {
		t.Fatalf("delivery binding: rev %d err %v", bound.Revision, err)
	}
	assertStoredLinkProvenance(t, d, row.ID, map[string]string{url: "explicit"})
	before := bound.Revision

	again, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, url, repository.LinkRoleDelivery)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != before {
		t.Fatalf("delivery re-apply bumped revision %d -> %d", before, again.Revision)
	}
	if got := again.Repositories[0].Links[0]; got.Role != "" {
		t.Fatalf("delivery re-apply wrote literal role %q", got.Role)
	}

	// Switching back from reference to delivery must clear the stored role in exactly one revision bump.
	ref, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, url, repository.LinkRoleReference)
	if err != nil || ref.Revision != before+1 {
		t.Fatalf("reference rewrite: rev %d err %v", ref.Revision, err)
	}
	assertStoredLinkProvenance(t, d, row.ID, map[string]string{url: "unbound"})
	back, err := d.SetTodoRepositoryLinkRole(ctx, row.ID, url, repository.LinkRoleDelivery)
	if err != nil || back.Revision != before+2 {
		t.Fatalf("delivery switch: rev %d err %v", back.Revision, err)
	}
	if got := back.Repositories[0].Links[0].Role; got != "" {
		t.Fatalf("delivery switch left role %q, want empty", got)
	}
	assertStoredLinkProvenance(t, d, row.ID, map[string]string{url: "explicit"})
}
