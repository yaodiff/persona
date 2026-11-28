package repository

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskRepositoryDefaultsLinksAndIdentitySnapshot(t *testing.T) {
	id, _ := Parse("https://github.com/example/web", "")
	catalog := Catalog{Revision: 3, Repositories: []CatalogEntry{{Identity: id, ID: "web", CloneURL: "git@" + "github.com:example/web.git"}}}
	input := []TaskInput{{RepoID: "web", Links: []LinkInput{{Kind: "pull_request", Number: "7"}, {URL: id.CanonicalURL + "/pull/8"}, {Kind: "issue", Number: "7"}}}}
	rows, err := Bind(nil, input, catalog)
	if err != nil || len(rows) != 1 || rows[0].RequestedRef.Kind != "default" || rows[0].DirectoryAlias != "" || len(rows[0].Links) != 3 || rows[0].SourceSnapshot.CloneURL != catalog.Repositories[0].CloneURL {
		t.Fatalf("bind: %+v %v", rows, err)
	}
	catalog.Repositories[0].Path = "example/other"
	catalog.Repositories[0].CanonicalURL = "https://github.com/example/other"
	input[0].Links = nil
	kept, err := Bind(rows, input, catalog)
	if err != nil || kept[0].SourceSnapshot != rows[0].SourceSnapshot || len(kept[0].Links) != 3 {
		t.Fatalf("identity replaced: %+v %v", kept, err)
	}
	if CatalogState(kept[0], catalog) != "identity_changed" {
		t.Fatal("changed catalog identity hidden")
	}
	snapshot, err := Snapshot("assistant", "todo", 4, rows, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(snapshot)
	rows[0].Links = nil
	rows[0].DirectoryAlias = "changed"
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("snapshot shares mutable input")
	}
}

func TestTaskRepositoryValidation(t *testing.T) {
	id, _ := Parse("https://gitlab.example.com/group/sub/project", "gitlab")
	for _, url := range []string{id.CanonicalURL + "/-/merge_requests/42", id.CanonicalURL + "/-/issues/42"} {
		if _, err := NormalizeLink(id, LinkInput{URL: url}); err != nil {
			t.Fatal(url, err)
		}
	}
	for _, url := range []string{"https://gitlab.example.com/other/project/-/issues/42", id.CanonicalURL + "/-/issues/42?token=x", id.CanonicalURL + "/-/issues/42#note", id.CanonicalURL + "/-/issues/%34%32", id.CanonicalURL + "/-/issues/042", id.CanonicalURL + "/-/issues/42/notes"} {
		if _, err := NormalizeLink(id, LinkInput{URL: url}); err == nil {
			t.Fatal("accepted", url)
		}
	}
	for _, alias := range []string{"..", ".persona", "CON", "a/b", "a\\b", "foo.", "é"} {
		if ValidateAlias(alias) == nil {
			t.Fatal("accepted alias", alias)
		}
	}
	for _, alias := range []string{"", "web", "api-server", "Repo_2"} {
		if err := ValidateAlias(alias); err != nil {
			t.Fatal(alias, err)
		}
	}
	for _, ref := range []RequestedRef{{Kind: "branch", Value: ""}, {Kind: "branch", Value: "main~1"}, {Kind: "ref", Value: "main"}, {Kind: "default", Value: "main"}, {Kind: "commit", Value: "abc"}} {
		if _, err := NormalizeRef(ref); err == nil {
			t.Fatal("accepted ref", ref)
		}
	}
	for _, ref := range []RequestedRef{{}, {Kind: "branch", Value: "feature/login"}, {Kind: "tag", Value: "v1.0"}, {Kind: "ref", Value: "refs/pull/42/head"}, {Kind: "commit", Value: strings.Repeat("a", 40)}} {
		if _, err := NormalizeRef(ref); err != nil {
			t.Fatal(ref, err)
		}
	}
	catalog := Catalog{Repositories: []CatalogEntry{{Identity: id, ID: "a"}, {Identity: id, ID: "b"}}}
	if _, err := Bind(nil, []TaskInput{{RepoID: "a"}, {RepoID: "b"}}, catalog); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err := Bind(nil, []TaskInput{{RepoID: "foreign"}}, catalog); err == nil {
		t.Fatal("foreign repo accepted")
	}
}

func TestTaskRepositoryHTTPInstanceLinks(t *testing.T) {
	identity, err := Parse("http://gitlab.example.com/group/project", "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	link, err := NormalizeLink(identity, LinkInput{Kind: "issue", Number: "7"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := NormalizeLink(identity, LinkInput{URL: link.CanonicalURL})
	if err != nil || parsed != link {
		t.Fatal(parsed, err)
	}
}

func TestTaskRepositoryTransportAndLayouts(t *testing.T) {
	id, _ := Parse("https://github.com/example/web", "")
	for _, s := range []string{"git@" + "github.com:example/web.git", "ssh://git@" + "ssh.github.com:443/example/web.git", id.CanonicalURL + ".git"} {
		if _, err := CloneTransport(s, id); err != nil {
			t.Fatal(s, err)
		}
	}
	for _, s := range []string{"https://token@" + "github.com/example/web", "https://github.com/example/other.git", id.CanonicalURL + "?secret=x", id.CanonicalURL + "/issues/1"} {
		if _, err := CloneTransport(s, id); err == nil {
			t.Fatal("accepted transport", s)
		}
	}
	if path, err := RepositoryPath(LayoutRoot, "", 1); err != nil || path != "." {
		t.Fatal(path, err)
	}
	if path, err := RepositoryPath(LayoutContainer, "web", 1); err != nil || path != "web" {
		t.Fatal(path, err)
	}
	if _, err := RepositoryPath(LayoutRoot, "", 2); err == nil {
		t.Fatal("two root repositories")
	}
}

func TestAliasReservedDeviceNames(t *testing.T) {
	for _, name := range []string{"CON", "prn.txt", "Aux.tar.gz", "nUl", "COM1", "com9.txt", "LPT1", "lpt9.txt"} {
		if err := ValidateAlias(name); err == nil {
			t.Errorf("accepted reserved alias %q", name)
		}
	}
	for _, name := range []string{"console", "NUL-backup", "AUX_1", "COM0", "COM10.txt", "LPT0", "LPT10", "prefix.NUL"} {
		if err := ValidateAlias(name); err != nil {
			t.Errorf("rejected ordinary alias %q: %v", name, err)
		}
	}
}

func TestTaskRepositorySameNumberAcrossRepositories(t *testing.T) {
	web, _ := Parse("https://github.com/example/web", "")
	api, _ := Parse("https://github.com/example/api", "")
	catalog := Catalog{Repositories: []CatalogEntry{{Identity: web, ID: "web"}, {Identity: api, ID: "api"}}}
	input := []TaskInput{{RepoID: "web", DirectoryAlias: "web", Links: []LinkInput{{Kind: "pull_request", Number: "7"}}}, {RepoID: "api", DirectoryAlias: "api", Links: []LinkInput{{Kind: "pull_request", Number: "7"}}}}
	rows, err := Bind(nil, input, catalog)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if LinkKey(rows[0].SourceSnapshot.Identity, rows[0].Links[0]) == LinkKey(rows[1].SourceSnapshot.Identity, rows[1].Links[0]) {
		t.Fatal("cross-repository PR identities collided")
	}
	input[1].DirectoryAlias = "WEB"
	if _, err := Bind(nil, input, catalog); err != Problem("duplicate_alias") {
		t.Fatal("case-insensitive duplicate alias", err)
	}
}

func TestTaskLinkReportsPreserveSourcesAndNormalizeReferences(t *testing.T) {
	urls := []string{"https://GitHub.com/Example/Web/pull/7", "https://github.com/example/web/pull/7", "https://github.com/example/api/pull/7", "https://gitlab.example.com/Team/Api/-/merge_requests/7"}
	rows, err := MergeTaskLinks(nil, urls, Catalog{})
	if err != nil || len(rows) != 3 || len(rows[0].Links) != 1 || rows[2].Links[0].CanonicalURL != "https://gitlab.example.com/team/api/-/merge_requests/7" {
		t.Fatal(rows, err)
	}
	first := rows[0]
	first.DirectoryAlias = "frontend"
	first.RequestedRef = RequestedRef{Kind: "branch", Value: "release"}
	rows[0] = first
	catalog := Catalog{Revision: 9, Repositories: []CatalogEntry{{ID: first.RepoID, Identity: rows[1].SourceSnapshot.Identity}}}
	got, err := MergeTaskLinks(rows, []string{"https://github.com/example/web/issues/7"}, catalog)
	if err != nil || got[0].SourceSnapshot != first.SourceSnapshot || got[0].RequestedRef != first.RequestedRef || got[0].DirectoryAlias != first.DirectoryAlias || len(got[0].Links) != 2 || len(rows[0].Links) != 1 {
		t.Fatal("report overwrote source/ref/alias or input", got, rows, err)
	}
	before, _ := json.Marshal(rows)
	if _, err := MergeTaskLinks(rows, []string{"https://github.com/example/web/pull/9", "https://github.com/example/web/pull/0"}, catalog); err == nil {
		t.Fatal("invalid batch accepted")
	}
	after, _ := json.Marshal(rows)
	if string(before) != string(after) {
		t.Fatal("failed batch mutated input")
	}
	for _, raw := range []string{"https://user:secret@" + "github.com/example/web/pull/7", "https://github.com/example/web/pull/7?token=secret", "https://github.com/example/web/pull/%37", "https://github.com/Example/Web/pull/%37", "https://github.com/example/web/pull/7#note", "ssh://git@" + "github.com/example/web/pull/7"} {
		if _, _, err := ParseTaskLink(raw); err == nil {
			t.Fatal("accepted unsafe/noncanonical reference", raw)
		}
	}
}

func TestTaskLinkTitlesSurviveOlderEditsAndURLReports(t *testing.T) {
	identity, _ := Parse("https://github.com/example/web", "")
	catalog := Catalog{Repositories: []CatalogEntry{{ID: "web", Identity: identity}}}
	rows, err := Bind(nil, []TaskInput{{RepoID: "web", Links: []LinkInput{{Kind: "pull_request", Number: "7", Title: "  Keep the full review title  "}, {Kind: "issue", Number: "7", Title: "Fix sign in"}}}}, catalog)
	if err != nil || rows[0].Links[0].Title != "Keep the full review title" {
		t.Fatal(rows, err)
	}
	// An older editor knows only the link's identity. It must not erase newer metadata.
	kept, err := Bind(rows, []TaskInput{{RepoID: "web", Links: []LinkInput{{Kind: "pull_request", Number: "7"}, {Kind: "issue", Number: "7"}}}}, catalog)
	if err != nil || kept[0].Links[0].Title != rows[0].Links[0].Title || kept[0].Links[1].Title != "Fix sign in" {
		t.Fatal(kept, err)
	}
	updated, err := MergeTaskLinkDetails(kept, []LinkInput{{URL: identity.CanonicalURL + "/pull/7", Title: "Updated review title"}}, catalog, LinkProvenanceUnbound)
	if err != nil || len(updated[0].Links) != 2 || updated[0].Links[0].Title != "Updated review title" || kept[0].Links[0].Title != rows[0].Links[0].Title {
		t.Fatal(updated, kept, err)
	}
	reported, err := MergeTaskLinks(updated, []string{identity.CanonicalURL + "/pull/7"}, catalog)
	if err != nil || reported[0].Links[0].Title != "Updated review title" {
		t.Fatal(reported, err)
	}
	if got := reported[0].Links[0].Provenance; got != LinkProvenanceExplicit {
		t.Fatalf("a URL-only report changed the saved delivery binding to %q", got)
	}
	cleared, err := Bind(rows, []TaskInput{{RepoID: "web", Links: []LinkInput{}}}, catalog)
	if err != nil || len(cleared[0].Links) != 0 {
		t.Fatal(cleared, err)
	}
	for _, title := range []string{"bad\x00title", "bad\ntitle", strings.Repeat("a", 1025), string([]byte{0xff})} {
		if _, err := NormalizeLink(identity, LinkInput{Kind: "issue", Number: "7", Title: title}); err == nil {
			t.Fatal("accepted invalid title")
		}
	}
}

func TestTaskLinkLegacyMergeRequestRequiresGitLab(t *testing.T) {
	for _, raw := range []string{
		"https://code.example.com/team/app/merge_requests/7",
		"https://code.example.com/group/team/app/merge_requests/7",
	} {
		if identity, link, err := ParseTaskLink(raw); err == nil {
			t.Fatalf("inferred a built-in provider for an unknown legacy MR path: %s -> %+v %+v", raw, identity, link)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{"https://gitlab.com/Team/Api/merge_requests/7", "https://gitlab.com/team/api/-/merge_requests/7"},
		{"https://git.example.com/Team/Api/-/merge_requests/7", "https://git.example.com/team/api/-/merge_requests/7"},
	} {
		identity, link, err := ParseTaskLink(tc.raw)
		if err != nil || identity.Provider != GitLab || link.CanonicalURL != tc.want {
			t.Fatalf("GitLab MR lost its canonical URL: %s -> %+v %+v %v", tc.raw, identity, link, err)
		}
	}
}
