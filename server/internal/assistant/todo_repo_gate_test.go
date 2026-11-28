package assistant

import (
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

func TestRepoGateInputsUseExplicitDeliveryLinks(t *testing.T) {
	const repoURL = "https://github.com/org/core"
	delivery := TodoPRStatus{URL: repoURL + "/pull/1", State: "merged"}
	reference := TodoPRStatus{URL: repoURL + "/pull/2", State: "open", Checks: "failure"}
	unlinked := TodoPRStatus{URL: repoURL + "/pull/3", State: "open", Checks: "failure"}
	bound := []repository.TaskRepository{{
		SourceSnapshot: repository.SourceSnapshot{Identity: repository.Identity{CanonicalURL: repoURL}},
		Links: []repository.TaskLink{
			{Kind: "pull_request", CanonicalURL: delivery.URL, Role: repository.LinkRoleDelivery},
			{Kind: "pull_request", CanonicalURL: reference.URL, Role: repository.LinkRoleReference},
		},
	}}
	want := []repoGatePR{{URL: delivery.URL, State: "merged"}}

	for _, tc := range []struct {
		name string
		todo Todo
	}{
		{"unlinked current observation", Todo{PRStatus: &unlinked, OtherPRs: []TodoPRStatus{delivery, reference}}},
		{"unlinked earlier observation", Todo{PRStatus: &delivery, OtherPRs: []TodoPRStatus{unlinked, reference}}},
		{"unlinked URL without observation", Todo{PRURL: unlinked.URL, OtherPRs: []TodoPRStatus{delivery, reference}}},
		{"same-repo reference", Todo{PRStatus: &delivery, OtherPRs: []TodoPRStatus{reference}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := buildRepoGateInputs(tc.todo, bound)
			if len(inputs) != 1 || !inputs[0].IsTarget || !reflect.DeepEqual(inputs[0].PRs, want) {
				t.Fatalf("repo gate inputs = %+v, want one target with PRs %+v", inputs, want)
			}
		})
	}
}

func TestRepoGateEmptyInput(t *testing.T) {
	if got := aggregateRepoGate(nil); got != nil {
		t.Fatalf("aggregateRepoGate(nil) = %+v, want nil", got)
	}
	if got := aggregateRepoGate([]repoGateRepo{}); got != nil {
		t.Fatalf("aggregateRepoGate(empty) = %+v, want nil", got)
	}
}

func TestRepoGateReferenceOnlyDoesNotBlock(t *testing.T) {
	repos := []repoGateRepo{
		{
			RepoKey:   "https://github.com/org/docs-ref",
			RepoLabel: "org/docs-ref",
			IsTarget:  false,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/docs-ref/pull/1", State: "open", Checks: "failure", Conflicts: true},
			},
		},
		{
			RepoKey:   "https://github.com/org/upstream-ref",
			RepoLabel: "org/upstream-ref",
			IsTarget:  false,
		},
	}
	want := &repoGate{
		State:          repoGateReferenceOnly,
		TargetCount:    0,
		ReadyCount:     0,
		ReferenceCount: 2,
		Reason:         "reference_only",
	}
	if got := aggregateRepoGate(repos); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRepoGateIgnoresFailingReferenceReposWhenTargetsReady(t *testing.T) {
	repos := []repoGateRepo{
		{
			RepoKey:   "https://github.com/org/api",
			RepoLabel: "org/api",
			IsTarget:  true,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/api/pull/10", State: "merged"},
			},
		},
		{
			RepoKey:   "https://github.com/org/web",
			RepoLabel: "org/web",
			IsTarget:  true,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/web/pull/20", State: "open", Review: "approved", Checks: "success"},
			},
		},
		{
			RepoKey:   "https://github.com/org/reference-red",
			RepoLabel: "org/reference-red",
			IsTarget:  false,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/reference-red/pull/99", State: "open", Checks: "failure", Conflicts: true, Review: "changes_requested"},
			},
		},
		{
			RepoKey:   "https://github.com/org/reference-empty",
			RepoLabel: "org/reference-empty",
			IsTarget:  false,
		},
	}
	want := &repoGate{
		State:          repoGateReady,
		TargetCount:    2,
		ReadyCount:     2,
		ReferenceCount: 2,
		Reason:         "all_targets_ready",
	}
	if got := aggregateRepoGate(repos); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRepoGateWeakestLinkPriorityOrder(t *testing.T) {
	repoMissing := repoGateRepo{
		RepoKey: "k-missing", RepoLabel: "org/a-missing", IsTarget: true,
	}
	repoClosedOnly := repoGateRepo{
		RepoKey: "k-closed", RepoLabel: "org/b-closed", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/b-closed/pull/1", State: "closed"}},
	}
	repoOpen := repoGateRepo{
		RepoKey: "k-open", RepoLabel: "org/c-open", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/c-open/pull/2", State: "open"}},
	}
	repoChecksPending := repoGateRepo{
		RepoKey: "k-pending", RepoLabel: "org/d-pending", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/d-pending/pull/3", State: "open", Review: "approved", Checks: "pending"}},
	}
	repoDraft := repoGateRepo{
		RepoKey: "k-draft", RepoLabel: "org/e-draft", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/e-draft/pull/4", State: "open", Draft: true}},
	}
	repoReviewReq := repoGateRepo{
		RepoKey: "k-review", RepoLabel: "org/f-review", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/f-review/pull/5", State: "open", Review: "required", Checks: "success"}},
	}
	repoChanges := repoGateRepo{
		RepoKey: "k-changes", RepoLabel: "org/g-changes", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/g-changes/pull/6", State: "open", Review: "changes_requested", Checks: "success"}},
	}
	repoConflict := repoGateRepo{
		RepoKey: "k-conflict", RepoLabel: "org/h-conflict", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/h-conflict/pull/7", State: "open", Conflicts: true, Review: "changes_requested"}},
	}
	repoCIFail := repoGateRepo{
		RepoKey: "k-ci", RepoLabel: "org/i-ci", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/i-ci/pull/8", State: "open", Checks: "failure", Conflicts: true}},
	}
	repoReady := repoGateRepo{
		RepoKey: "k-ready", RepoLabel: "org/j-ready", IsTarget: true,
		PRs: []repoGatePR{{URL: "https://github.com/org/j-ready/pull/9", State: "merged"}},
	}

	cases := []struct {
		name         string
		repos        []repoGateRepo
		wantState    string
		wantRepo     string
		wantPR       string
		wantReason   string
		wantTarget   int
		wantReady    int
		wantRefCount int
	}{
		{
			name:       "blocked_ci beats conflict and all others",
			repos:      []repoGateRepo{repoReady, repoMissing, repoChecksPending, repoReviewReq, repoChanges, repoConflict, repoCIFail},
			wantState:  repoGateBlockedCI,
			wantRepo:   "org/i-ci",
			wantPR:     "https://github.com/org/i-ci/pull/8",
			wantReason: "ci_failed",
			wantTarget: 7,
			wantReady:  1,
		},
		{
			name:       "blocked_conflict beats changes_requested",
			repos:      []repoGateRepo{repoReady, repoMissing, repoChecksPending, repoReviewReq, repoChanges, repoConflict},
			wantState:  repoGateBlockedConflict,
			wantRepo:   "org/h-conflict",
			wantPR:     "https://github.com/org/h-conflict/pull/7",
			wantReason: "merge_conflicts",
			wantTarget: 6,
			wantReady:  1,
		},
		{
			name:       "blocked_changes beats awaiting_review",
			repos:      []repoGateRepo{repoReady, repoMissing, repoChecksPending, repoDraft, repoReviewReq, repoChanges},
			wantState:  repoGateBlockedChanges,
			wantRepo:   "org/g-changes",
			wantPR:     "https://github.com/org/g-changes/pull/6",
			wantReason: "changes_requested",
			wantTarget: 6,
			wantReady:  1,
		},
		{
			name:       "awaiting_review draft beats checks_pending",
			repos:      []repoGateRepo{repoReady, repoMissing, repoChecksPending, repoDraft},
			wantState:  repoGateAwaitingReview,
			wantRepo:   "org/e-draft",
			wantPR:     "https://github.com/org/e-draft/pull/4",
			wantReason: "draft_pr",
			wantTarget: 4,
			wantReady:  1,
		},
		{
			name:       "awaiting_review required beats checks_pending",
			repos:      []repoGateRepo{repoReady, repoMissing, repoChecksPending, repoReviewReq},
			wantState:  repoGateAwaitingReview,
			wantRepo:   "org/f-review",
			wantPR:     "https://github.com/org/f-review/pull/5",
			wantReason: "review_required",
			wantTarget: 4,
			wantReady:  1,
		},
		{
			name:       "checks_pending beats missing_pr",
			repos:      []repoGateRepo{repoReady, repoMissing, repoChecksPending},
			wantState:  repoGateChecksPending,
			wantRepo:   "org/d-pending",
			wantPR:     "https://github.com/org/d-pending/pull/3",
			wantReason: "checks_pending",
			wantTarget: 3,
			wantReady:  1,
		},
		{
			name:       "open pr without approval counts as checks_pending over missing_pr",
			repos:      []repoGateRepo{repoReady, repoMissing, repoOpen},
			wantState:  repoGateChecksPending,
			wantRepo:   "org/c-open",
			wantPR:     "https://github.com/org/c-open/pull/2",
			wantReason: "pr_open",
			wantTarget: 3,
			wantReady:  1,
		},
		{
			name:       "missing_pr when target has no PRs",
			repos:      []repoGateRepo{repoReady, repoMissing},
			wantState:  repoGateMissingPR,
			wantRepo:   "org/a-missing",
			wantPR:     "",
			wantReason: "missing_pr",
			wantTarget: 2,
			wantReady:  1,
		},
		{
			name:       "closed-only PR treated as missing_pr",
			repos:      []repoGateRepo{repoReady, repoClosedOnly},
			wantState:  repoGateMissingPR,
			wantRepo:   "org/b-closed",
			wantPR:     "",
			wantReason: "missing_pr",
			wantTarget: 2,
			wantReady:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := aggregateRepoGate(tc.repos)
			want := &repoGate{
				State:          tc.wantState,
				TargetCount:    tc.wantTarget,
				ReadyCount:     tc.wantReady,
				ReferenceCount: tc.wantRefCount,
				BlockingRepo:   tc.wantRepo,
				BlockingPR:     tc.wantPR,
				Reason:         tc.wantReason,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestRepoGateDeterministicTieBreakingAcrossOrders(t *testing.T) {
	repos := []repoGateRepo{
		{
			RepoKey:   "https://github.com/org/z-service",
			RepoLabel: "org/z-service",
			IsTarget:  true,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/z-service/pull/1", State: "open", Checks: "failure"},
			},
		},
		{
			RepoKey:   "https://github.com/org/a-service",
			RepoLabel: "org/a-service",
			IsTarget:  true,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/a-service/pull/9", State: "open", Checks: "failure"},
				{URL: "https://github.com/org/a-service/pull/2", State: "open", Checks: "failure"},
			},
		},
		{
			RepoKey:   "https://github.com/org/m-service",
			RepoLabel: "org/m-service",
			IsTarget:  true,
			PRs: []repoGatePR{
				{URL: "https://github.com/org/m-service/pull/5", State: "merged"},
			},
		},
	}

	want := &repoGate{
		State:          repoGateBlockedCI,
		TargetCount:    3,
		ReadyCount:     1,
		ReferenceCount: 0,
		BlockingRepo:   "org/a-service",
		BlockingPR:     "https://github.com/org/a-service/pull/2",
		Reason:         "ci_failed",
	}

	if got := aggregateRepoGate(repos); !reflect.DeepEqual(got, want) {
		t.Fatalf("forward order: got %+v, want %+v", got, want)
	}

	reversed := slices.Clone(repos)
	slices.Reverse(reversed)
	if got := aggregateRepoGate(reversed); !reflect.DeepEqual(got, want) {
		t.Fatalf("reverse order: got %+v, want %+v", got, want)
	}
}
