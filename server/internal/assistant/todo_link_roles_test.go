package assistant

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

func TestReferenceReportLinks(t *testing.T) {
	own := "https://github.com/o/r/pull/1"
	foreign := "https://github.com/o/r/pull/10"
	other := "https://github.com/o/r/pull/11"
	raws := []string{own, foreign, other}
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{
			name: "related work line",
			text: "Done with the fix.\nRelated work: " + foreign + " tracks the upstream dependency.\nDelivery PR: " + own + "\n",
			want: []string{foreign},
		},
		{
			name: "background bullet",
			text: "- Background: " + foreign + "\nMerged our own change in " + own,
			want: []string{foreign},
		},
		{
			name: "not this delivery clause",
			text: foreign + " is related work, not this task's delivery. Our PR is " + own + ".",
			want: []string{foreign},
		},
		{
			name: "citation footer",
			text: "Shipped in " + own + ".\n<oai-mem-citation>\n<citation_entries>\nnotes:1|note=[see " + other + "]\n</citation_entries>\n</oai-mem-citation>",
			want: []string{other},
		},
		{
			name: "plain wording adds no legacy reference markers",
			text: "Status: done\nPR: " + own + " and a follow-up " + foreign + " ready for review.",
			want: nil,
		},
		{
			// A bare "background" inside a delivery sentence must not demote the task's own PR (P1 review finding).
			name: "background worker delivery stays delivery",
			text: "Fixed the background sync worker in " + own + " and shipped it.",
			want: nil,
		},
		{
			name: "sentence initial background sync worker delivery stays delivery",
			text: "Background sync worker fixed in " + own,
			want: nil,
		},
		{
			// Explicit framing still marks the link reference, including the colon form and the "as background" form.
			name: "explicit background framing is reference",
			text: own + " is included as background only; our delivery is " + foreign + ".",
			want: []string{own},
		},
		{
			// Mid-sentence colon framing, not a line-start marker: only the link inside that sentence is reference.
			name: "background colon clause is reference",
			text: "See background: " + foreign + " for the upstream bug. Delivery landed in " + own + ".",
			want: []string{foreign},
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := referenceReportLinks(tc.text, raws)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, u := range tc.want {
				if !got[u] {
					t.Fatalf("expected %s marked reference, got %v", u, got)
				}
			}
		})
	}
}

func TestReferenceReportLinksUseBindingsAndCloseout(t *testing.T) {
	const bound = "https://github.com/example/project/pull/1"
	const explicit = "https://github.com/example/project/pull/2"
	const incidental = "https://github.com/example/project/pull/10"
	alias := "https://github.com/Example/Project/pull/1#discussion_r3"
	rows := []repository.TaskRepository{{Links: []repository.TaskLink{{CanonicalURL: bound}}}}
	raws := []string{alias, explicit, incidental}

	_, got := reportLinkRoles(raws, []string{explicit}, rows)
	if len(got) != 1 || got[incidental] != repository.LinkRoleReference {
		t.Fatalf("report roles = %v, want only the incidental link marked reference", got)
	}
	_, got = reportLinkRoles(raws, nil, nil)
	for _, raw := range raws {
		if got[raw] != repository.LinkRoleReference {
			t.Errorf("unselected URL %s got role %q, want reference", raw, got[raw])
		}
	}
}

func TestReferenceReportLinksKeepUnlinkedTarget(t *testing.T) {
	const repo = "https://github.com/example/project"
	const siblingPR = "https://github.com/example/project-tools/pull/3"
	rows := []repository.TaskRepository{{SourceSnapshot: repository.SourceSnapshot{
		Identity: repository.Identity{CanonicalURL: repo},
	}}}
	links, roles := reportLinkRoles([]string{repo + "/pull/1", repo + "/pull/2", siblingPR}, []string{repo + "/pull/2"}, rows)
	if len(links) != 2 || links[0] != repo+"/pull/2" || links[1] != siblingPR || roles[siblingPR] != repository.LinkRoleReference {
		t.Fatalf("report links = %v, roles = %v, want explicit delivery and reference outside the target repo", links, roles)
	}
}
