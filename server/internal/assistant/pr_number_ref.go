package assistant

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// A PR an agent names by number alone (#7921): "PR #7350", or "pushed to #7350" in a reply that speaks of a PR. prLink
// reads only full URLs, so such a PR used to go unrecorded: no pr_url, no PR status, never Waiting for merge. It is
// recorded as its full URL when the repository is known without guessing, from (in order) the task's own PRs, the one
// GitHub repository the reply links, the one its to-do's title or notes link, or the repository earlier replies showed
// the agent's working copy (its repository root on that computer) pushes to. Anything ambiguous records nothing.

var (
	prNumbered            = lazyre.New(`(?i)\b(?:PR|pull request)\s*#(\d+)\b`) // i18n-ignore: match what the agent wrote
	prBareRef             = lazyre.New(`(?:^|[^\w/&#])#(\d+)\b`)
	prWords               = lazyre.New(`(?i)\bPRs?\b|pull requests?`) // i18n-ignore: match what the agent wrote
	githubRepoLink        = lazyre.New(`https://github\.com/([\w.-]+/[\w.-]+)/(?:pull|issues)/\d+`)
	closeoutApplicability = lazyre.New(`\b(merged|deployed)\s*=\s*"([^"]*)"`)
	closeoutPRLine        = lazyre.New(`(?im)^[\t *-]*artifact\s*[:\x{ff1a}]\s*pr(?:\s|:|$)`)
	replyPRNumber         = lazyre.New(`(?i)(?:^|[^\w/&#])(?:(build[ \t]*)|([\w.-]+/[\w.-]+))?#(\d+)\b`)
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
	if deliveryCloseout(text) != "" {
		return "" // a closeout without a delivery PR does not delegate that choice to the prose
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
	_, artifacts, _ := closeout.Parse(deliveryCloseout(text))
	ref := ""
	for _, a := range artifacts {
		if a.Kind == "pr" {
			ref = strings.TrimSpace(a.Ref)
		}
	}
	if ref != "" && !ciArtifactNumber.MatchString(ref) && !ciArtifactRepo.MatchString(ref) && prLink.FindString(ref) != ref {
		return ""
	}
	return ref
}

func canonicalReplyPR(raw string) string {
	if _, link, err := repository.ParseTaskLink(raw); err == nil && link.Kind == "pull_request" {
		return link.CanonicalURL
	}
	return raw
}

func sameReplyPR(a, b string) bool {
	return a == b || canonicalReplyPR(a) == canonicalReplyPR(b)
}

// The checkout lookup uses gh. Other providers can supply native evidence without that lookup.
func githubPROwnershipCandidate(raw string) bool {
	host, _ := repository.RemoteHost(raw)
	switch host {
	case "github.com", "www.github.com", "api.github.com", "ssh.github.com":
		return true
	}
	identity, link, err := repository.ParseTaskLink(raw)
	return err == nil && identity.Provider == repository.GitHub && link.Kind == "pull_request"
}

// replyNamesOtherPR prevents an unqualified status word from being applied to the task's PR when the reply
// also names a different change. A bare #number can refer to a dependency even without the word PR.
func replyNamesOtherPR(text, pr string, aliases ...string) bool {
	links := prLink.FindAllString(text, -1)
	if deliveryCloseout(text) != "" {
		links = append(links, taskRepositoryReportLinks(text)...)
	}
	for _, link := range links {
		if !sameReplyPR(link, pr) && !slices.ContainsFunc(aliases, func(alias string) bool { return sameReplyPR(link, alias) }) {
			return true
		}
	}
	known := ciPRURL.FindStringSubmatch(pr)
	for _, ref := range replyPRNumber.FindAllStringSubmatch(text, -1) {
		if ref[1] != "" { // build #1234
			continue
		}
		if known == nil || ref[3] != known[2] || ref[2] != "" && !strings.EqualFold(ref[2], known[1]) {
			return true
		}
	}
	return false
}

// ownedReplyPR selects delivery evidence without changing the to-do or importing repository links.
// Its saved PR is already eligible. Any new PR must pass the same ownership check as a report.
func (s *Service) ownedReplyPR(ctx context.Context, t Todo, machine, session, text string, at time.Time) string {
	urls := prLink.FindAllString(text, -1)
	if deliveryCloseout(text) != "" {
		urls = taskRepositoryReportLinks(text)
	}
	if len(urls) == 0 && prNumberIn(text) == "" {
		return t.PRURL
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if len(urls) == 0 {
		if pr := s.prFromNumber(ctx, t, machine, session, text); pr != "" {
			urls = append(urls, pr)
		}
	}
	var candidates []repository.LinkInput
	for _, url := range urls {
		if sameReplyPR(url, t.PRURL) {
			return t.PRURL
		}
		candidates = append(candidates, repository.LinkInput{URL: url})
	}
	owned, err := s.ownedTaskRepositoryLinkInputs(ctx, t, machine, session, candidates, text, at)
	if err == nil && len(owned) > 0 {
		return owned[len(owned)-1].URL
	}
	return t.PRURL
}

// deliveryCloseout accepts the reply's own final record, not a quoted example or an earlier reference.
// The general closeout parser intentionally also serves transcripts and does not impose these ownership rules.
func deliveryCloseout(text string) string {
	if report, ok := textfmt.FinalWorkReport(text); ok {
		return text[report.Start:report.End]
	}
	if !strings.Contains(text, "<closeout") {
		return ""
	}
	text = strings.TrimRight(text, " \t\r\n") // keep first-line indentation: an indented example is not the reply
	if footer := strings.LastIndex(text, "\n<oai-mem-citation>"); footer >= 0 && strings.HasSuffix(text, "</oai-mem-citation>") {
		text = strings.TrimRight(text[:footer], " \t\r\n") // runtime citation metadata may follow the agent's final record
	}
	var lines []string
	fence := ""
	inCloseout := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, trimmed[:1]))]
			continue
		}
		if strings.HasPrefix(trimmed, ">") || !inCloseout && (strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")) {
			continue
		}
		if strings.HasPrefix(trimmed, "<closeout") {
			inCloseout = true
		}
		lines = append(lines, trimmed)
		if strings.HasSuffix(trimmed, "</closeout>") {
			inCloseout = false
		}
	}
	reply := strings.TrimSpace(strings.Join(lines, "\n"))
	start := strings.LastIndex(reply, "\n<closeout")
	if start >= 0 {
		start++
	} else if strings.HasPrefix(reply, "<closeout") {
		start = 0
	}
	if start < 0 || !strings.HasSuffix(reply, "</closeout>") {
		return ""
	}
	block := reply[start:]
	if _, _, ok := closeout.Parse(block); !ok {
		return ""
	}
	return block
}

// closeoutExplicitlyNA distinguishes an explicit n/a from absent, repeated, or unrecognized facts.
func closeoutExplicitlyNA(block string) bool {
	if report, ok := textfmt.FinalWorkReport(block); ok {
		return report.Merged == "n/a" && report.Deployed == "n/a"
	}
	end := strings.IndexByte(block, '>')
	if end < 0 {
		return false
	}
	mergedNA, deployedNA := false, false
	attrs := closeoutApplicability.FindAllStringSubmatch(block[:end], -1)
	if len(attrs) != 2 {
		return false
	}
	for _, a := range attrs {
		if a[1] == "merged" {
			mergedNA = strings.EqualFold(a[2], "n/a")
		} else {
			deployedNA = strings.EqualFold(a[2], "n/a")
		}
	}
	return mergedNA && deployedNA
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
	return s.numberedPRPreferred(ctx, t, machine, session, text, "")
}

// numberedPRPreferred resolves a numbered artifact with an optional repository established by its own reply. Ordinary
// number-only resolution keeps the saved-history order above; ownership-sensitive recovery puts that reply evidence first.
func (s *Service) numberedPRPreferred(ctx context.Context, t Todo, machine, session, text, preferred string) (string, error) {
	n := prNumberIn(text)
	if n == "" {
		return "", nil
	}
	repo := preferred
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

// reportedTaskPR reads explicit delivery artifacts hidden by clipped session previews. noPR is true only when
// every active agent's own final closeout explicitly reports a finished, non-PR deliverable.
func (s *Service) reportedTaskPR(ctx context.Context, t Todo, current string) (pr string, noPR bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	active := 0
	noPR = true
	recovered := ""
	for i := len(t.Agents) - 1; i >= 0; i-- {
		a := t.Agents[i]
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		active++
		items, ok := s.lastTurnItems(ctx, a.MachineID, a.SessionID)
		if !ok {
			noPR = false
			continue
		}
		_, items = splitLastTurn(items)
		var replyAt time.Time
		for j := len(items) - 1; j >= 0; j-- {
			if it := items[j]; it.Role == "assistant" && it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "" {
				replyAt = it.TS
				if it.FullSize > 0 || !it.TS.IsZero() && it.TS.Before(a.LinkedAt) {
					ok = false
				}
				break
			}
		}
		from, source := taskTurnSource(t, items)
		ask, text := turnExchange(items)
		if !ok || source != "" || from == "" && ask != "" && !sameRequest(ask, t.Notes) {
			noPR = false
			continue
		}
		block := deliveryCloseout(text)
		c, artifacts, ok := closeout.Parse(block)
		if !ok {
			noPR = false
			continue
		}
		ref := closeoutPRRef(text)
		explicit := ""
		parsedPRs := 0
		for _, artifact := range artifacts {
			if artifact.Kind == "pr" {
				parsedPRs++
				if explicit != "" && explicit != artifact.Ref {
					return "", false, fmt.Errorf("task %s: several explicit delivery PR artifacts need clarification", t.ID)
				}
				explicit = artifact.Ref
			}
		}
		// A malformed or clipped artifact must not turn an explicitly declared PR into a no-PR exemption.
		if declared := len(closeoutPRLine.FindAllString(block, -1)); declared != parsedPRs || declared > 0 && ref == "" {
			return "", false, fmt.Errorf("task %s: its explicit delivery PR artifact is incomplete or ambiguous", t.ID)
		}
		if ref == "" {
			hasPR := false
			for _, a := range artifacts {
				hasPR = hasPR || a.Kind == "pr"
			}
			noPR = noPR && !hasPR && len(artifacts) > 0 && c.Status == proto.CloseoutDone &&
				c.Merged == "" && c.Deployed == "" && closeoutExplicitlyNA(block)
			continue
		}
		noPR = false
		pr = prLink.FindString(ref)
		if pr == "" {
			replyRepo := githubRepoIn(text)
			m, qualified := ciPRURL.FindStringSubmatch(current), ciArtifactRepo.FindStringSubmatch(ref)
			if m != nil && prNumberIn(text) == m[2] && (qualified == nil || strings.EqualFold(qualified[1], m[1])) &&
				(replyRepo == "" || strings.EqualFold(replyRepo, m[1])) {
				pr = current // let the existing gate use local deployment proof without an extra forge dependency
			} else {
				pr, err = s.numberedPRPreferred(ctx, t, a.MachineID, a.SessionID, text, replyRepo)
				if err != nil {
					return "", false, fmt.Errorf("the reported delivery PR could not be confirmed: %w", err)
				}
			}
		} else if m := ciPRURL.FindStringSubmatch(pr); m != nil && !sameReplyPR(pr, current) {
			number, err := strconv.Atoi(m[2])
			if err != nil {
				return "", false, err
			}
			isPR, err := s.forge().IsPR(ctx, m[1], number)
			if err != nil {
				return "", false, fmt.Errorf("the reported delivery PR could not be confirmed: %w", err)
			}
			if !isPR {
				continue // do not cycle between several misreported issue URLs
			}
		}
		if pr == "" {
			continue
		}
		if sameReplyPR(pr, current) {
			pr = current
		} else {
			owned, err := s.importOwnedTaskRepositoryLinks(ctx, t, a.MachineID, a.SessionID, []string{pr}, text, replyAt, nil)
			if err != nil {
				return "", false, fmt.Errorf("task %s: delivery PR ownership could not be verified: %w", t.ID, err)
			}
			if len(owned) == 0 && !githubPROwnershipCandidate(pr) {
				continue // no automatic evidence; any recorded GitHub delivery still goes through its gate
			}
			if len(owned) != 1 {
				return "", false, fmt.Errorf("task %s: the reported PR has no verified ownership or belongs to another open to-do", t.ID)
			}
			pr = owned[0].canonical // the importer verified this one candidate and returns its stored canonical URL
		}
		if recovered != "" && !sameReplyPR(recovered, pr) {
			return "", false, fmt.Errorf("task %s: its active agents report different delivery PR artifacts; clarify before accepting", t.ID)
		}
		recovered = pr
	}
	return recovered, active > 0 && noPR, nil
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
