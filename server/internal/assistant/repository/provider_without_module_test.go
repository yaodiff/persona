//go:build persona_core_only || persona_without_github

package repository

import (
	"testing"
)

func TestGitHubRepositoryAndLinksResolveWithoutAnyProvider(t *testing.T) {
	registry := Registry{}
	providers := registry.Providers()
	if len(providers) != 2 || providers[0].ID != GitHub || providers[1].ID != GitLab {
		t.Fatalf("providers without extensions: %+v", providers)
	}
	for _, remote := range []string{
		"https://github.com/example/web",
		"https://github.com/example/web.git",
		"https://www.github.com/example/web",
		"https://github.com/example/web/pull/12",
		"https://github.com/example/web/issues/7",
	} {
		parsed, err := registry.Parse(t.Context(), remote, "")
		if err != nil {
			t.Fatalf("Parse(%q): %v", remote, err)
		}
		want := Identity{Provider: GitHub, InstanceURL: "https://github.com", Path: "example/web", CanonicalURL: "https://github.com/example/web"}
		if parsed.Identity != want || parsed.ProviderName != "" {
			t.Fatalf("Parse(%q) = %+v, want %+v", remote, parsed, want)
		}
	}

	repo, err := registry.Parse(t.Context(), "https://github.com/example/web", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		in   LinkInput
		want TaskLink
	}{
		{name: "pull request by number", in: LinkInput{Kind: "pull_request", Number: "12"}, want: TaskLink{Kind: "pull_request", Number: "12", CanonicalURL: "https://github.com/example/web/pull/12"}},
		{name: "issue by number", in: LinkInput{Kind: "issue", Number: "7"}, want: TaskLink{Kind: "issue", Number: "7", CanonicalURL: "https://github.com/example/web/issues/7"}},
		{name: "canonical pull request URL", in: LinkInput{URL: "https://github.com/example/web/pull/12"}, want: TaskLink{Kind: "pull_request", Number: "12", CanonicalURL: "https://github.com/example/web/pull/12"}},
		{name: "canonical issue URL", in: LinkInput{URL: "https://github.com/example/web/issues/7"}, want: TaskLink{Kind: "issue", Number: "7", CanonicalURL: "https://github.com/example/web/issues/7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := registry.Link(t.Context(), repo.Identity, tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("Link(%+v) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			}
		})
	}

	// Canonical URLs round-trip above. A subpage or fragment is not itself canonical and must not be stored as one.
	for _, raw := range []string{
		"https://github.com/example/web/pull/12/files",
		"https://github.com/example/web/issues/7#issuecomment-1",
	} {
		if _, err := registry.Link(t.Context(), repo.Identity, LinkInput{URL: raw}); err == nil {
			t.Fatalf("non-canonical URL %q was accepted as a stored link", raw)
		}
	}
}
