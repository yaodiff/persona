package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// A PR an agent names by number alone (#7921): "PR #7350", or "pushed to #7350" in a reply that speaks of a PR. prLink
// reads only full URLs, so such a PR used to go unrecorded: no pr_url, no PR status, never Waiting for merge. It is
// recorded as its full URL when the repository is known without guessing, from (in order) the task's own PRs, the one
// GitHub repository the reply links, the one its to-do's title or notes link, or the repository earlier replies showed
// the agent's working copy (its repository root on that computer) pushes to. Anything ambiguous records nothing.

var (
	prNumbered     = lazyre.New(`(?i)\b(?:PR|pull request)\s*#(\d+)\b`) // i18n-ignore: match what the agent wrote
	prBareRef      = lazyre.New(`(?:^|[^\w/&#])#(\d+)\b`)
	prWords        = lazyre.New(`(?i)\bPRs?\b|pull requests?`) // i18n-ignore: match what the agent wrote
	githubRepoLink = lazyre.New(`https://github\.com/([\w.-]+/[\w.-]+)/(?:pull|issues)/\d+`)
)

// repoRootsKey keeps, per computer and repository root, the GitHub repository its agents' PRs went to.
const repoRootsKey = "assistant.repo_roots"

// prNumberIn prefers the closeout's explicit PR artifact, else the single "PR #N" or single "#N" in PR-related
// prose. Full URLs are handled by stageOf; ambiguous prose records nothing.
func prNumberIn(text string) string {
	if ref := closeoutPRRef(text); ref != "" {
		if ciArtifactNumber.MatchString(ref) {
			return strings.TrimPrefix(ref, "#")
		}
		if m := ciArtifactRepo.FindStringSubmatch(ref); m != nil {
			return m[2]
		}
		return "" // an explicit artifact outranks incidental PR numbers in the prose
	}
	one := func(re *lazyre.Regexp) string {
		n := ""
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if n != "" && n != m[1] {
				return "-"
			}
			n = m[1]
		}
		return n
	}
	switch n := one(prNumbered); n {
	case "-":
		return ""
	case "":
	default:
		return n
	}
	if !prWords.MatchString(text) {
		return ""
	}
	if n := one(prBareRef); n != "-" {
		return n
	}
	return ""
}

func closeoutPRRef(text string) string {
	_, artifacts, _ := closeout.Parse(text)
	ref := ""
	for _, a := range artifacts {
		if a.Kind == "pr" {
			ref = strings.TrimSpace(a.Ref)
		}
	}
	return ref
}

// githubRepoIn is the one GitHub repository ("owner/repo") the texts link an issue or PR of; "" for none or several.
func githubRepoIn(texts ...string) string {
	repo := ""
	for _, text := range texts {
		for _, m := range githubRepoLink.FindAllStringSubmatch(text, -1) {
			if r := strings.ToLower(m[1]); repo != "" && repo != r {
				return ""
			} else {
				repo = r
			}
		}
	}
	return repo
}

// prFromNumber is the full URL of the PR text names by number alone, when the repository is known (above); "" else.
func (s *Service) prFromNumber(ctx context.Context, t Todo, machine, session, text string) string {
	pr, err := s.numberedPR(ctx, t, machine, session, text)
	if err != nil {
		s.logf("task %s: numbered PR could not be confirmed: %v", t.ID, err)
	}
	return pr
}

func (s *Service) numberedPR(ctx context.Context, t Todo, machine, session, text string) (string, error) {
	n := prNumberIn(text)
	if n == "" {
		return "", nil
	}
	repo := ""
	if m := ciArtifactRepo.FindStringSubmatch(closeoutPRRef(text)); m != nil {
		repo = strings.ToLower(m[1])
	}
	if repo == "" {
		repo = githubRepoIn(t.PRURL)
	}
	for i := len(t.PRs) - 1; repo == "" && i >= 0; i-- {
		repo = githubRepoIn(t.PRs[i])
	}
	if repo == "" {
		repo = githubRepoIn(text)
	}
	if repo == "" {
		repo = githubRepoIn(t.Title, t.Notes)
	}
	if repo == "" {
		repo = s.repoOfRoot(ctx, machine, session)
	}
	if repo == "" {
		return "", nil
	}
	number, err := strconv.Atoi(n)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	isPR, err := s.forge().IsPR(ctx, repo, number)
	if err != nil {
		return "", err
	}
	if !isPR {
		return "", nil
	}
	return "https://github.com/" + repo + "/pull/" + n, nil
}

// reportedTaskPR recovers a delivery artifact hidden by a clipped session-list preview. It reads only linked
// agents' latest turns, and only while repairing a confirmed false PR; normal session polling never fetches text.
func (s *Service) reportedTaskPR(ctx context.Context, t Todo, wrong string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for i := len(t.Agents) - 1; i >= 0; i-- {
		a := t.Agents[i]
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		items, ok := s.lastTurnItems(ctx, a.MachineID, a.SessionID)
		if !ok {
			continue
		}
		_, text := turnExchange(items)
		ref := closeoutPRRef(text)
		if ref == "" {
			continue
		}
		pr := prLink.FindString(ref)
		if pr == "" {
			var err error
			pr, err = s.numberedPR(ctx, t, a.MachineID, a.SessionID, text)
			if err != nil {
				return "", fmt.Errorf("the reported delivery PR could not be confirmed: %w", err)
			}
		}
		if pr != "" && pr != wrong {
			return pr, nil
		}
	}
	return "", nil
}

// rootKey names the repository root session on machine works in; "" when the node does not say.
func (s *Service) rootKey(ctx context.Context, machine, session string) string {
	if machine == "" || session == "" {
		return ""
	}
	sess, ok := s.session(ctx, machine, session)
	if !ok || sess.RepoRoot == "" {
		return ""
	}
	return machine + "|" + sess.RepoRoot
}

// repoOfRoot is the GitHub repository earlier replies showed the working copy of session on machine pushes to.
func (s *Service) repoOfRoot(ctx context.Context, machine, session string) string {
	st := s.store()
	key := s.rootKey(ctx, machine, session)
	if st == nil || key == "" {
		return ""
	}
	roots := map[string]string{}
	if _, err := st.Setting(ctx, repoRootsKey, &roots); err != nil {
		return ""
	}
	return roots[key]
}

// learnRepoRoot: a reply from session on machine named the full URL of PR pr, so its working copy pushes to that
// repository; kept for the PRs its agents later name by number alone.
func (s *Service) learnRepoRoot(ctx context.Context, machine, session, pr string) {
	repo := githubRepoIn(pr)
	st := s.store()
	if repo == "" || st == nil {
		return
	}
	key := s.rootKey(ctx, machine, session)
	if key == "" {
		return
	}
	s.repoRootsMu.Lock()
	defer s.repoRootsMu.Unlock()
	roots := map[string]string{}
	if _, err := st.Setting(ctx, repoRootsKey, &roots); err != nil {
		return
	}
	if roots == nil {
		roots = map[string]string{}
	}
	if roots[key] == repo {
		return
	}
	roots[key] = repo
	if err := st.PutSetting(ctx, repoRootsKey, roots); err != nil {
		s.logf("repository of %s: %v", key, err)
	}
}
