package assistant

import (
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Weakest-link delivery gate states across a to-do's bound Target Repos under todos_v2.
const (
	repoGateBlockedCI       = "blocked_ci"
	repoGateBlockedConflict = "blocked_conflict"
	repoGateBlockedChanges  = "blocked_changes"
	repoGateAwaitingReview  = "awaiting_review"
	repoGateChecksPending   = "checks_pending"
	repoGateMissingPR       = "missing_pr"
	repoGateReady           = "ready"
	repoGateReferenceOnly   = "reference_only"
)

// repoGatePR is a single delivery pull request observation belonging to a bound repository.
type repoGatePR struct {
	URL       string
	State     string // "open", "merged", "closed", or "" (unobserved)
	Draft     bool
	Review    string // "approved", "changes_requested", "required", "pending", or ""
	Checks    string // "success", "failure", "pending", "none", or ""
	Conflicts bool
}

// repoGateRepo is one bound repository's delivery gate input for aggregateRepoGate.
type repoGateRepo struct {
	RepoKey   string       // deterministic canonical identifier (e.g. canonical URL or repo ID)
	RepoLabel string       // human-readable repository name (e.g. "owner/repo")
	IsTarget  bool         // true when the repo has delivery/default links or no links; false for reference-only repos
	PRs       []repoGatePR // delivery PRs belonging to this repository
}

// repoGate is the weakest-link ("barrel rule") delivery gate summary across a to-do's bound Target Repos.
type repoGate struct {
	State          string `json:"state"`
	TargetCount    int    `json:"target_count"`
	ReadyCount     int    `json:"ready_count"`
	ReferenceCount int    `json:"reference_count"`
	BlockingRepo   string `json:"blocking_repo,omitempty"`
	BlockingPR     string `json:"blocking_pr,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type repoGateEval struct {
	severity int
	state    string
	repo     string
	pr       string
	reason   string
}

const (
	gateSeverityReady           = 0
	gateSeverityMissingPR       = 1
	gateSeverityChecksPending   = 2
	gateSeverityAwaitingReview  = 3
	gateSeverityBlockedChanges  = 4
	gateSeverityBlockedConflict = 5
	gateSeverityBlockedCI       = 6
)

// snapshotRepoGate keeps same-repository observations in the legacy task snapshot when todos_v2 is off.
func snapshotRepoGate(t Todo, bound []repository.TaskRepository, todosV2 bool) *repoGate {
	if todosV2 {
		return aggregateRepoGate(buildRepoGateInputs(t, bound))
	}

	prByURL := repoGatePRObservations(t)
	repos := linkedRepoGateInputs(bound, prByURL)
	for i, binding := range bound {
		repo := &repos[i]
		repoURL := normalizeRepoGateURL(binding.SourceSnapshot.Identity.CanonicalURL)
		if !repo.IsTarget || repoURL == "" {
			continue
		}
		linked := make(map[string]bool, len(binding.Links))
		for _, link := range binding.Links {
			if link.Kind == "pull_request" || strings.TrimSpace(link.Role) == repository.LinkRoleReference {
				linked[normalizeRepoGateURL(link.CanonicalURL)] = true
			}
		}
		prefix := repoURL + "/"
		for key, obs := range prByURL {
			if !linked[key] && strings.HasPrefix(key, prefix) {
				repo.PRs = append(repo.PRs, toRepoGatePR(obs.URL, obs))
			}
		}
	}
	return aggregateRepoGate(repos)
}

// aggregateRepoGate computes a deterministic weakest-link ("barrel rule") delivery gate summary
// across a to-do's bound repositories. Reference-only repositories (IsTarget == false) are counted
// in ReferenceCount and strictly ignored for blocking and readiness calculations.
// Returns nil when repos is empty.
func aggregateRepoGate(repos []repoGateRepo) *repoGate {
	if len(repos) == 0 {
		return nil
	}

	var targetCount, readyCount, refCount int
	var worst *repoGateEval

	for _, repo := range repos {
		if !repo.IsTarget {
			refCount++
			continue
		}
		targetCount++
		ev := evaluateTargetRepo(repo)
		if ev.severity == gateSeverityReady {
			readyCount++
			continue
		}
		if worst == nil || isWeakerEval(ev, *worst) {
			cp := ev
			worst = &cp
		}
	}

	if targetCount == 0 {
		return &repoGate{
			State:          repoGateReferenceOnly,
			TargetCount:    0,
			ReadyCount:     0,
			ReferenceCount: refCount,
			Reason:         "reference_only",
		}
	}

	if worst == nil {
		return &repoGate{
			State:          repoGateReady,
			TargetCount:    targetCount,
			ReadyCount:     readyCount,
			ReferenceCount: refCount,
			Reason:         "all_targets_ready",
		}
	}

	return &repoGate{
		State:          worst.state,
		TargetCount:    targetCount,
		ReadyCount:     readyCount,
		ReferenceCount: refCount,
		BlockingRepo:   worst.repo,
		BlockingPR:     worst.pr,
		Reason:         worst.reason,
	}
}

func evaluateTargetRepo(repo repoGateRepo) repoGateEval {
	name := strings.TrimSpace(repo.RepoLabel)
	if name == "" {
		name = strings.TrimSpace(repo.RepoKey)
	}

	var worst *repoGateEval
	activeOrMerged := 0

	for _, pr := range repo.PRs {
		state := strings.ToLower(strings.TrimSpace(pr.State))
		if state == "closed" {
			continue
		}
		activeOrMerged++
		ev := evaluateTargetPR(name, pr)
		if worst == nil || isWeakerEval(ev, *worst) {
			cp := ev
			worst = &cp
		}
	}

	if activeOrMerged == 0 {
		return repoGateEval{
			severity: gateSeverityMissingPR,
			state:    repoGateMissingPR,
			repo:     name,
			reason:   "missing_pr",
		}
	}
	return *worst
}

func evaluateTargetPR(repoName string, pr repoGatePR) repoGateEval {
	url := strings.TrimSpace(pr.URL)
	state := strings.ToLower(strings.TrimSpace(pr.State))
	review := strings.ToLower(strings.TrimSpace(pr.Review))
	checks := strings.ToLower(strings.TrimSpace(pr.Checks))

	if state == "merged" || state == "ready" {
		return repoGateEval{
			severity: gateSeverityReady,
			state:    repoGateReady,
			repo:     repoName,
			pr:       url,
		}
	}

	if isRepoGateCheckFailure(checks) {
		return repoGateEval{
			severity: gateSeverityBlockedCI,
			state:    repoGateBlockedCI,
			repo:     repoName,
			pr:       url,
			reason:   "ci_failed",
		}
	}
	if pr.Conflicts {
		return repoGateEval{
			severity: gateSeverityBlockedConflict,
			state:    repoGateBlockedConflict,
			repo:     repoName,
			pr:       url,
			reason:   "merge_conflicts",
		}
	}
	if review == "changes_requested" {
		return repoGateEval{
			severity: gateSeverityBlockedChanges,
			state:    repoGateBlockedChanges,
			repo:     repoName,
			pr:       url,
			reason:   "changes_requested",
		}
	}
	if pr.Draft {
		return repoGateEval{
			severity: gateSeverityAwaitingReview,
			state:    repoGateAwaitingReview,
			repo:     repoName,
			pr:       url,
			reason:   "draft_pr",
		}
	}
	if review == "required" || review == "review_required" || review == "pending" {
		return repoGateEval{
			severity: gateSeverityAwaitingReview,
			state:    repoGateAwaitingReview,
			repo:     repoName,
			pr:       url,
			reason:   "review_required",
		}
	}
	if checks == "pending" {
		return repoGateEval{
			severity: gateSeverityChecksPending,
			state:    repoGateChecksPending,
			repo:     repoName,
			pr:       url,
			reason:   "checks_pending",
		}
	}
	if review == "approved" && (checks == "success" || checks == "none") {
		return repoGateEval{
			severity: gateSeverityReady,
			state:    repoGateReady,
			repo:     repoName,
			pr:       url,
		}
	}
	return repoGateEval{
		severity: gateSeverityChecksPending,
		state:    repoGateChecksPending,
		repo:     repoName,
		pr:       url,
		reason:   "pr_open",
	}
}

func isRepoGateCheckFailure(checks string) bool {
	switch checks {
	case "failure", "error", "cancelled", "timed_out", "action_required", "startup_failure", "stale":
		return true
	default:
		return false
	}
}

// isWeakerEval reports whether candidate should beat current as the weakest link:
// strictly higher severity wins; ties break deterministically by lexicographically smallest
// repository label, then PR URL, then reason.
func isWeakerEval(candidate, current repoGateEval) bool {
	if candidate.severity != current.severity {
		return candidate.severity > current.severity
	}
	if candidate.repo != current.repo {
		return candidate.repo < current.repo
	}
	if candidate.pr != current.pr {
		return candidate.pr < current.pr
	}
	return candidate.reason < current.reason
}

// repoGateOpenNonDraftPR reports whether g represents at least one open non-draft delivery PR on a target repository.
// Draft PRs, missing PRs, reference-only repos and ready/merged gates return false.
func repoGateOpenNonDraftPR(g *repoGate) bool {
	if g == nil || g.TargetCount <= 0 || strings.EqualFold(strings.TrimSpace(g.Reason), "draft_pr") {
		return false
	}
	switch g.State {
	case repoGateBlockedCI, repoGateBlockedConflict, repoGateBlockedChanges, repoGateAwaitingReview, repoGateChecksPending:
		return true
	default:
		return false
	}
}

func repoGateProjection(g *repoGate) *assistantwire.TodoWait {
	if !repoGateOpenNonDraftPR(g) {
		return nil
	}
	var blocker assistantwire.TodoWaitPRBlocker
	switch g.State {
	case repoGateBlockedConflict:
		blocker = assistantwire.TodoWaitPRBlockerConflicts
	case repoGateBlockedChanges:
		blocker = assistantwire.TodoWaitPRBlockerChangesRequested
	case repoGateBlockedCI:
		blocker = assistantwire.TodoWaitPRBlockerChecksFailing
	case repoGateAwaitingReview:
		blocker = assistantwire.TodoWaitPRBlockerReviewRequired
	case repoGateChecksPending:
		if strings.EqualFold(strings.TrimSpace(g.Reason), "checks_pending") {
			blocker = assistantwire.TodoWaitPRBlockerChecksPending
		}
	}
	return projection.RepoGateWait(strings.TrimSpace(g.BlockingRepo), strings.TrimSpace(g.BlockingPR), blocker)
}
