package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const acmeID = "ext:acme-code/acme"

var acme = ProviderInfo{ID: acmeID, Name: "Acme Code", Hosts: []string{"code.acme.test", "ssh.acme.test"}, State: ProviderAvailable,
	LinkKinds: []LinkLabel{{Kind: "pull_request", Label: "Review"}}}

// fakeExtensions is a provider extension on code.acme.test; it records what it was asked.
type fakeExtensions struct {
	providers []ProviderInfo
	asked     []string
}

func (f *fakeExtensions) RepositoryProviders() []ProviderInfo { return f.providers }
func (f *fakeExtensions) RepositoryProvider(id string) (ProviderInfo, bool) {
	for _, p := range f.providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}
func (f *fakeExtensions) RepositoryProviderForHost(host string) (ProviderInfo, bool) {
	for _, p := range f.providers {
		for _, h := range p.Hosts {
			if h == host {
				return p, true
			}
		}
	}
	return ProviderInfo{}, false
}
func (f *fakeExtensions) RepositoryParse(_ context.Context, provider, remote string) (Parsed, error) {
	f.asked = append(f.asked, "parse "+provider+" "+remote)
	info, _ := f.RepositoryProvider(provider)
	return CheckParseAnswer(info, ParseAnswer{InstanceURL: "https://code.acme.test", Path: "team/app", CanonicalURL: "https://code.acme.test/team/app"})
}
func (f *fakeExtensions) RepositoryCheck(context.Context, Identity) error { return nil }
func (f *fakeExtensions) RepositoryLink(_ context.Context, repo Identity, in LinkInput) (TaskLink, error) {
	f.asked = append(f.asked, "link "+in.URL+in.Kind+in.Number)
	return CheckLinkAnswer(repo, in, LinkAnswer{Kind: "pull_request", Number: "12", CanonicalURL: repo.CanonicalURL + "/reviews/12"})
}
func (f *fakeExtensions) RepositoryDescribe(context.Context, string, string) (Description, error) {
	return Description{}, nil
}

func TestExtensionProviderIDs(t *testing.T) {
	if got := ExtensionProvider("acme-code", "acme"); got != acmeID {
		t.Fatal(got)
	}
	if ext, contribution, ok := SplitExtensionProvider(acmeID); !ok || ext != "acme-code" || contribution != "acme" {
		t.Fatal(ext, contribution, ok)
	}
	for _, bad := range []string{"github", "gitlab", "", "ext:", "ext:acme", "ext:/acme", "ext:acme/", "ext:Acme/acme", "ext:acme/a/b",
		"ext:-acme/acme", "ext:acme/" + strings.Repeat("a", 33), "ext:acme/ac me", "extension:acme/acme"} {
		if IsExtensionProvider(bad) {
			t.Error("accepted", bad)
		}
	}
}

// The reserved hosts are exactly the ones the built-in rules turn into GitHub or GitLab identities, and an address on
// any other host needs an explicit provider (so no extension host is ever taken for a built-in one by accident).
func TestBuiltinHostsAreTheOnesParseMaps(t *testing.T) {
	for host, remote := range map[string]string{
		"github.com":        "https://github.com/example/web",
		"www.github.com":    "https://www.github.com/example/web",
		"api.github.com":    "https://api.github.com/repos/example/web",
		"ssh.github.com":    "ssh://git@" + "ssh.github.com:443/example/web.git",
		"gitlab.com":        "https://gitlab.com/example/web",
		"altssh.gitlab.com": "ssh://git@" + "altssh.gitlab.com:443/example/web.git",
	} {
		id, err := Parse(remote, "")
		if err != nil || id.Provider != GitHub && id.Provider != GitLab {
			t.Errorf("%s: %+v %v", host, id, err)
		}
	}
	if len(BuiltinHosts) != 6 {
		t.Fatal("a reserved host was added without a parse case", BuiltinHosts)
	}
	if _, err := Parse("https://code.acme.test/team/app", ""); err == nil {
		t.Fatal("a custom host parsed without a provider")
	}
}

func TestRegistryRoutesAddresses(t *testing.T) {
	ctx := context.Background()
	f := &fakeExtensions{providers: []ProviderInfo{acme}}
	r := Registry{Extensions: f}
	got, err := r.Parse(ctx, "https://github.com/example/web/pull/3", "")
	if err != nil || got.Identity.Provider != GitHub || got.ProviderName != "" || len(f.asked) != 0 {
		t.Fatalf("built-in: %+v %v %v", got, err, f.asked)
	}
	// An explicit built-in provider is the user's choice, even on a host an extension claims.
	if got, err := r.Parse(ctx, "https://code.acme.test/team/app", GitLab); err != nil || got.Identity.Provider != GitLab || len(f.asked) != 0 {
		t.Fatalf("explicit gitlab: %+v %v", got, err)
	}
	for _, remote := range []string{"https://code.acme.test/team/app/-/tree/main", "git@ssh.acme.test:team/app.git"} {
		got, err := r.Parse(ctx, remote, "")
		if err != nil || got.Identity != (Identity{Provider: acmeID, InstanceURL: "https://code.acme.test", Path: "team/app", CanonicalURL: "https://code.acme.test/team/app"}) || got.ProviderName != "Acme Code" {
			t.Fatalf("%s: %+v %v", remote, got, err)
		}
	}
	if got, err := r.Parse(ctx, "https://code.acme.test/team/app", acmeID); err != nil || got.Identity.Provider != acmeID {
		t.Fatalf("explicit extension: %+v %v", got, err)
	}
	asked := len(f.asked)
	for _, remote := range []string{"https://other.acme.test/team/app", "https://me:secret@code.acme.test/team/app",
		"https://code.acme.test/team/app?private_token=x", "ssh://root@code.acme.test/team/app"} {
		if _, err := r.Parse(ctx, remote, acmeID); !errors.Is(err, ErrRemote) {
			t.Errorf("%s: %v", remote, err)
		}
	}
	if len(f.asked) != asked {
		t.Fatal("an address the provider may not see was sent to it", f.asked)
	}
	var pe *ProviderError
	if _, err := r.Parse(ctx, "https://code.acme.test/team/app", "ext:gone/acme"); !errors.As(err, &pe) || pe.Code != "no_provider" {
		t.Fatalf("uninstalled: %v", err)
	}
	if _, err := r.Parse(ctx, "https://code.acme.test/team/app", "bitbucket"); !errors.Is(err, ErrRemote) {
		t.Fatalf("unknown provider: %v", err)
	}
	f.providers[0].State = ProviderOff
	if _, err := r.Parse(ctx, "https://code.acme.test/team/app", ""); !errors.As(err, &pe) || pe.Code != "provider_off" || pe.Provider != "Acme Code" {
		t.Fatalf("off: %v", err)
	}
	if _, err := (Registry{}).Parse(ctx, "https://code.acme.test/team/app", acmeID); !errors.As(err, &pe) || pe.Code != "no_provider" {
		t.Fatalf("no extensions: %v", err)
	}
	if ps := (Registry{Extensions: f}).Providers(); len(ps) != 3 || ps[0].ID != GitHub || ps[1].ID != GitLab || ps[2].ID != acmeID {
		t.Fatalf("providers: %+v", ps)
	}
}

func TestParseAnswersAreRechecked(t *testing.T) {
	good := ParseAnswer{InstanceURL: "https://code.acme.test", Path: "Team/App", CanonicalURL: "https://code.acme.test/Team/App",
		CloneURL: "git@ssh.acme.test:Team/App.git", DisplayName: "  Team\n app\t"}
	got, err := CheckParseAnswer(acme, good)
	if err != nil || got.Identity.Provider != acmeID || got.Identity.Path != "Team/App" || got.CloneURL != good.CloneURL || got.DisplayName != "Team app" || got.ProviderName != "Acme Code" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := CheckParseAnswer(acme, ParseAnswer{InstanceURL: "https://code.acme.test:8443", Path: "a/b", CanonicalURL: "https://code.acme.test:8443/a/b", CloneURL: "https://code.acme.test:8443/a/b"}); err != nil || got.CloneURL != "" {
		t.Fatalf("port: %+v %v", got, err)
	}
	if got, _ := CheckParseAnswer(acme, ParseAnswer{InstanceURL: good.InstanceURL, Path: "a", CanonicalURL: "https://code.acme.test/a", DisplayName: strings.Repeat("x", 90)}); len([]rune(got.DisplayName)) != 80 {
		t.Fatal("display name not cut", got.DisplayName)
	}
	for name, bad := range map[string]ParseAnswer{
		"other host":       {InstanceURL: "https://evil.example.test", Path: "a/b", CanonicalURL: "https://evil.example.test/a/b"},
		"http":             {InstanceURL: "http://code.acme.test", Path: "a/b", CanonicalURL: "http://code.acme.test/a/b"},
		"credentials":      {InstanceURL: "https://me:pw@code.acme.test", Path: "a/b", CanonicalURL: "https://me:pw@code.acme.test/a/b"},
		"upper host":       {InstanceURL: "https://Code.acme.test", Path: "a/b", CanonicalURL: "https://Code.acme.test/a/b"},
		"instance path":    {InstanceURL: "https://code.acme.test/git", Path: "a/b", CanonicalURL: "https://code.acme.test/git/a/b"},
		"default port":     {InstanceURL: "https://code.acme.test:443", Path: "a/b", CanonicalURL: "https://code.acme.test:443/a/b"},
		"query":            {InstanceURL: "https://code.acme.test?x=1", Path: "a/b", CanonicalURL: "https://code.acme.test?x=1/a/b"},
		"traversal":        {InstanceURL: "https://code.acme.test", Path: "a/../b", CanonicalURL: "https://code.acme.test/a/../b"},
		"empty segment":    {InstanceURL: "https://code.acme.test", Path: "a//b", CanonicalURL: "https://code.acme.test/a//b"},
		"encoded":          {InstanceURL: "https://code.acme.test", Path: "a/%2e%2e", CanonicalURL: "https://code.acme.test/a/%2e%2e"},
		"no path":          {InstanceURL: "https://code.acme.test", Path: "", CanonicalURL: "https://code.acme.test/"},
		"long path":        {InstanceURL: "https://code.acme.test", Path: strings.Repeat("a/", 20) + "a", CanonicalURL: "https://code.acme.test/" + strings.Repeat("a/", 20) + "a"},
		"canonical":        {InstanceURL: "https://code.acme.test", Path: "a/b", CanonicalURL: "https://code.acme.test/a/c"},
		"clone other host": {InstanceURL: "https://code.acme.test", Path: "a/b", CanonicalURL: "https://code.acme.test/a/b", CloneURL: "https://evil.example.test/a/b.git"},
		"clone creds":      {InstanceURL: "https://code.acme.test", Path: "a/b", CanonicalURL: "https://code.acme.test/a/b", CloneURL: "https://me:pw@code.acme.test/a/b.git"},
		"clone ssh user":   {InstanceURL: "https://code.acme.test", Path: "a/b", CanonicalURL: "https://code.acme.test/a/b", CloneURL: "ssh://root@code.acme.test/a/b.git"},
		"clone http":       {InstanceURL: "https://code.acme.test", Path: "a/b", CanonicalURL: "https://code.acme.test/a/b", CloneURL: "http://code.acme.test/a/b.git"},
		"clone query":      {InstanceURL: "https://code.acme.test", Path: "a/b", CanonicalURL: "https://code.acme.test/a/b", CloneURL: "https://code.acme.test/a/b.git?token=x"},
	} {
		if _, err := CheckParseAnswer(acme, bad); err == nil {
			t.Error("accepted", name)
		}
	}
}

func TestLinkAnswersStayUnderTheRepository(t *testing.T) {
	repo := Identity{Provider: acmeID, InstanceURL: "https://code.acme.test", Path: "team/app", CanonicalURL: "https://code.acme.test/team/app"}
	in := LinkInput{URL: "https://code.acme.test/team/app/reviews/12/files"}
	got, err := CheckLinkAnswer(repo, in, LinkAnswer{Kind: "pull_request", Number: "12", CanonicalURL: repo.CanonicalURL + "/reviews/12", Title: "Fix\nthe  build"})
	if err != nil || got != (TaskLink{Kind: "pull_request", Number: "12", CanonicalURL: repo.CanonicalURL + "/reviews/12", Title: "Fix the build"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := CheckLinkAnswer(repo, in, LinkAnswer{Kind: "issue", Number: "3", CanonicalURL: repo.CanonicalURL + "/i/3", Title: strings.Repeat("é", 600)}); len(got.Title) > 1024 {
		t.Fatal("title not cut", len(got.Title))
	}
	for name, bad := range map[string]LinkAnswer{
		"sibling repository": {Kind: "issue", Number: "1", CanonicalURL: "https://code.acme.test/team/app2/issues/1"},
		"the repository":     {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL},
		"just a slash":       {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL + "/"},
		"other host":         {Kind: "issue", Number: "1", CanonicalURL: "https://evil.example.test/team/app/issues/1"},
		"http":               {Kind: "issue", Number: "1", CanonicalURL: "http://code.acme.test/team/app/issues/1"},
		"query":              {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL + "/issues/1?x=1"},
		"fragment":           {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL + "/issues/1#c"},
		"traversal":          {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL + "/../other/issues/1"},
		"encoded":            {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL + "/issues/%31"},
		"credentials":        {Kind: "issue", Number: "1", CanonicalURL: "https://me@code.acme.test/team/app/issues/1"},
		"kind":               {Kind: "merge_request", Number: "1", CanonicalURL: repo.CanonicalURL + "/issues/1"},
		"leading zero":       {Kind: "issue", Number: "01", CanonicalURL: repo.CanonicalURL + "/issues/01"},
		"not digits":         {Kind: "issue", Number: "D12", CanonicalURL: repo.CanonicalURL + "/issues/D12"},
		"long":               {Kind: "issue", Number: "1", CanonicalURL: repo.CanonicalURL + "/" + strings.Repeat("x", 2048)},
	} {
		if _, err := CheckLinkAnswer(repo, in, bad); err == nil {
			t.Error("accepted", name)
		}
	}
	if _, err := CheckLinkAnswer(repo, LinkInput{Kind: "issue", Number: "4"}, LinkAnswer{Kind: "issue", Number: "5", CanonicalURL: repo.CanonicalURL + "/i/5"}); err == nil {
		t.Fatal("accepted another link than the one asked about")
	}
	if d := CheckDescribeAnswer("A\ttitle", "draft"); d != (Description{Title: "A title"}) {
		t.Fatalf("%+v", d)
	}
	if d := CheckDescribeAnswer("A", "merged"); d.State != "merged" {
		t.Fatalf("%+v", d)
	}
}

// An extension's links come from the store or the provider's answer, never from a GitHub-style guess, and its rows bind
// and read with the provider gone.
func TestExtensionRowsWorkWithoutTheProvider(t *testing.T) {
	repo := Identity{Provider: acmeID, InstanceURL: "https://code.acme.test", Path: "team/app", CanonicalURL: "https://code.acme.test/team/app"}
	source := SourceSnapshot{Identity: repo, CloneURL: repo.CanonicalURL, TransportOrigin: "canonical_default", ProviderName: "Acme Code"}
	review := TaskLink{Kind: "pull_request", Number: "12", CanonicalURL: repo.CanonicalURL + "/reviews/12"}
	if _, err := NormalizeLink(repo, LinkInput{Kind: "pull_request", Number: "12"}); err == nil {
		t.Fatal("synthesized a link without the provider")
	}
	if _, err := NormalizeLink(repo, LinkInput{URL: review.CanonicalURL}); err == nil {
		t.Fatal("accepted a link without an answer")
	}
	got, err := NormalizeLink(repo, LinkInput{Kind: "pull_request", Number: "12", Title: "Fix", Resolved: &ResolvedLink{Source: source, Link: review}})
	if err != nil || got.CanonicalURL != review.CanonicalURL || got.Title != "Fix" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, in := range map[string]LinkInput{
		"another number": {Kind: "pull_request", Number: "13", Resolved: &ResolvedLink{Source: source, Link: review}},
		"another repo":   {URL: review.CanonicalURL, Resolved: &ResolvedLink{Source: SourceSnapshot{Identity: Identity{Provider: acmeID, InstanceURL: "https://code.acme.test", Path: "team/other", CanonicalURL: "https://code.acme.test/team/other"}}, Link: review}},
		"outside":        {URL: review.CanonicalURL, Resolved: &ResolvedLink{Source: source, Link: TaskLink{Kind: "issue", Number: "1", CanonicalURL: "https://code.acme.test/team/app2/i/1"}}},
		"both forms":     {URL: review.CanonicalURL, Kind: "pull_request", Number: "12", Resolved: &ResolvedLink{Source: source, Link: review}},
	} {
		if _, err := NormalizeLink(repo, in); err == nil {
			t.Error("accepted", name)
		}
	}
	for _, clone := range []string{repo.CanonicalURL, "git@ssh.acme.test:team/app.git", "ssh://git@ssh.acme.test:2222/team/app.git", "https://code.acme.test:8443/team/app.git"} {
		if _, err := CloneTransport(clone, repo); err != nil {
			t.Error(clone, err)
		}
	}
	for _, clone := range []string{"https://me:pw@code.acme.test/team/app.git", "http://code.acme.test/team/app.git", "https://code.acme.test/team/app.git?x=1", "ext::sh -c id", "file:///tmp/app", "ssh://root@code.acme.test/team/app"} {
		if _, err := CloneTransport(clone, repo); err == nil {
			t.Error("accepted", clone)
		}
	}
	catalog := Catalog{Revision: 2, Repositories: []CatalogEntry{{Identity: repo, ID: "repo_acme", CloneURL: "git@ssh.acme.test:team/app.git", ProviderName: "Acme Code"}}}
	rows, err := Bind(nil, []TaskInput{{RepoID: "repo_acme"}}, catalog)
	if err != nil || rows[0].SourceSnapshot.ProviderName != "Acme Code" || rows[0].SourceSnapshot.CloneURL != catalog.Repositories[0].CloneURL || rows[0].SourceSnapshot.TransportOrigin != "catalog" {
		t.Fatalf("bind: %+v %v", rows, err)
	}
	rows[0].Links = []TaskLink{review}
	stored := LinkInput{Kind: "pull_request", Number: "12", Resolved: &ResolvedLink{Source: rows[0].SourceSnapshot, Link: review}}
	kept, err := Bind(rows, []TaskInput{{RepoID: "repo_acme", Links: []LinkInput{stored}}}, Catalog{})
	if err != nil || len(kept[0].Links) != 1 || kept[0].Links[0] != review {
		t.Fatalf("kept: %+v %v", kept, err)
	}
	merged, err := MergeTaskLinkDetails(nil, []LinkInput{{URL: review.CanonicalURL + "/files", Title: "Fix", Resolved: &ResolvedLink{Source: source, Link: review}},
		{URL: repo.CanonicalURL + "/i/4", Resolved: &ResolvedLink{Source: source, Link: TaskLink{Kind: "issue", Number: "4", CanonicalURL: repo.CanonicalURL + "/i/4"}}}}, Catalog{}, LinkProvenanceUnbound)
	if err != nil || len(merged) != 1 || !strings.HasPrefix(merged[0].RepoID, "linked_") || merged[0].SourceSnapshot != source || len(merged[0].Links) != 2 || merged[0].Links[0].Title != "Fix" {
		t.Fatalf("merge: %+v %v", merged, err)
	}
	if _, err := MergeTaskLinkDetails(nil, []LinkInput{{URL: review.CanonicalURL, Resolved: &ResolvedLink{Source: SourceSnapshot{Identity: mustParse(t, "https://github.com/example/web")}, Link: review}}}, Catalog{}, LinkProvenanceUnbound); err == nil {
		t.Fatal("a resolved link claimed a built-in repository")
	}
}

// The built-ins' stored and hashed bytes do not change: the provider name is absent for them.
func TestBuiltinSnapshotsHaveNoProviderName(t *testing.T) {
	id := mustParse(t, "https://github.com/example/web")
	rows, err := Bind(nil, []TaskInput{{RepoID: "web"}}, Catalog{Revision: 1, Repositories: []CatalogEntry{{Identity: id, ID: "web"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "provider_name") || strings.Contains(string(raw), "provider_state") || strings.Contains(string(raw), "link_kinds") {
		t.Fatal(string(raw))
	}
}

func mustParse(t *testing.T, remote string) Identity {
	t.Helper()
	id, err := Parse(remote, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}
