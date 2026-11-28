package repository

import (
	"strings"
	"testing"
)

// A repository a to-do first gets through a provider's link is cloned from where the provider said (repo.parse's
// clone_url); a saved catalog entry still gives its own transport, as binding it would.
func TestNewProviderRowsCloneWhereTheProviderSays(t *testing.T) {
	repo := Identity{Provider: acmeID, InstanceURL: "https://code.acme.test", Path: "team/app", CanonicalURL: "https://code.acme.test/team/app"}
	review := TaskLink{Kind: "pull_request", Number: "12", CanonicalURL: repo.CanonicalURL + "/reviews/12"}
	source := SourceSnapshot{Identity: repo, CloneURL: "git@ssh.acme.test:team/app.git", TransportOrigin: "provider", ProviderName: "Acme Code"}
	merge := func(src SourceSnapshot, catalog Catalog) SourceSnapshot {
		t.Helper()
		rows, err := MergeTaskLinkDetails(nil, []LinkInput{{URL: review.CanonicalURL, Resolved: &ResolvedLink{Source: src, Link: review}}}, catalog, LinkProvenanceUnbound)
		if err != nil || len(rows) != 1 || len(rows[0].Links) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		if (len(catalog.Repositories) == 0) != strings.HasPrefix(rows[0].RepoID, "linked_") {
			t.Fatalf("row id %q", rows[0].RepoID)
		}
		return rows[0].SourceSnapshot
	}
	if got := merge(source, Catalog{}); got != source {
		t.Fatalf("provider's clone address: %+v", got)
	}
	canonical := SourceSnapshot{Identity: repo, CloneURL: repo.CanonicalURL, TransportOrigin: "canonical_default", ProviderName: "Acme Code"}
	for _, clone := range []string{"", repo.CanonicalURL, "https://me:pw@code.acme.test/team/app.git", "file:///tmp/app"} {
		src := source
		src.CloneURL = clone
		if got := merge(src, Catalog{}); got != canonical {
			t.Errorf("clone %q: %+v", clone, got)
		}
	}
	saved := Catalog{Revision: 2, Repositories: []CatalogEntry{{Identity: repo, ID: "repo_acme", CloneURL: "ssh://git@ssh.acme.test/team/app.git", ProviderName: "Acme Code"}}}
	if got := merge(source, saved); got.CloneURL != "ssh://git@ssh.acme.test/team/app.git" || got.TransportOrigin != "catalog" || got.CatalogRevision != 2 {
		t.Fatalf("catalog entry: %+v", got)
	}
	saved.Repositories[0].CloneURL = ""
	if got := merge(source, saved); got.CloneURL != repo.CanonicalURL || got.TransportOrigin != "canonical_default" || got.CatalogRevision != 2 {
		t.Fatalf("catalog entry without a clone address: %+v", got)
	}
	// Built-in reports carry no provider answer and stay as they were.
	rows, err := MergeTaskLinks(nil, []string{"https://github.com/example/web/pull/7"}, Catalog{})
	if err != nil || rows[0].SourceSnapshot.CloneURL != "https://github.com/example/web" || rows[0].SourceSnapshot.TransportOrigin != "canonical_default" {
		t.Fatalf("built-in: %+v %v", rows, err)
	}
}
