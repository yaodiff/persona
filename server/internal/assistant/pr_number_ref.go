package assistant

import (
	"context"
	"strings"

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

// prNumberIn is the one PR number text names without a URL: the single "PR #N", else the single "#N" in text that
// speaks of a PR. "" when there is none, or several different ones.
func prNumberIn(text string) string {
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
	n := prNumberIn(text)
	if n == "" {
		return ""
	}
	repo := githubRepoIn(t.PRURL)
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
		return ""
	}
	return "https://github.com/" + repo + "/pull/" + n
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
