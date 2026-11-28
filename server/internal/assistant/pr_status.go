package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// TodoPRStatus keeps forge observations beside the existing stage/delivery. A ready,
// open PR with no working Agent is a view of that state, not another task lifecycle.
type TodoPRStatus struct {
	URL                string     `json:"url"`
	Title              string     `json:"title,omitempty"`
	HeadOID            string     `json:"head_oid,omitempty"`
	State              string     `json:"state"`
	Draft              bool       `json:"draft"`
	Own                bool       `json:"own,omitempty"`
	Review             string     `json:"review"`
	ChangesRequestedAt *time.Time `json:"changes_requested_at,omitempty"`
	Checks             string     `json:"checks"`
	Conflicts          bool       `json:"conflicts"`
	CheckedAt          time.Time  `json:"checked_at"`

	terminalAt       time.Time          // forge time for a merge or close, when known
	submittedReview  prSubmittedReview  // consumed by the park in the same pass, separate from the stage's review summary
	actionableReview prActionableReview // current-head changes_requested for opt-in Agent routing
	failingCheck     prFailingCheck     // current-head failing CI summary for opt-in Agent routing
}

func (p TodoPRStatus) sameRuntimeFacts(o TodoPRStatus) bool {
	return p.State == o.State && p.HeadOID == o.HeadOID && p.Draft == o.Draft &&
		p.Checks == o.Checks && p.Review == o.Review && p.Conflicts == o.Conflicts
}

type prSubmittedReview struct {
	ID          int64     `json:"id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type prActionableReview struct {
	ID          int64
	Author      string
	State       string
	Body        string
	URL         string
	SubmittedAt time.Time
}

type prFailingCheck struct {
	ID         int64
	Name       string
	Conclusion string
	URL        string
	Summary    string
	Count      int
	Digest     string
}

type prStatusForge interface {
	PRStatus(context.Context, string, int) (TodoPRStatus, error)
}

// prFeedback is an observation for the routing pass. It stays out of persisted PR status and park evidence.
type prFeedback struct {
	pullPath      string
	commentsPath  string
	author        string
	viewer        string
	viewerChecked bool
	reviews       []prFeedbackItem
}

type githubPRFeedbackPost struct {
	ID                int64  `json:"id"`
	Body              string `json:"body"`
	HTMLURL           string `json:"html_url"`
	AuthorAssociation string `json:"author_association"`
	User              struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

type githubPRReview struct {
	githubPRFeedbackPost
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type githubPRIssueComment struct {
	githubPRFeedbackPost
	CreatedAt time.Time `json:"created_at"`
}

type githubPRInlineComment struct {
	ID           int64  `json:"id"`
	Body         string `json:"body"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	OriginalLine int    `json:"original_line"`
}

func (g githubForge) prViewer(ctx context.Context) string {
	var viewer struct {
		Login string `json:"login"`
	}
	if err := g.get(ctx, "/user", &viewer); err != nil {
		return ""
	}
	return strings.TrimSpace(viewer.Login)
}

// The connector and CLI transports return decoded bodies. A full page needs a continuation even
// when there is no Link header available to the caller. Never use an incomplete list as evidence.
func readPRPages[T any](ctx context.Context, g githubForge, path string) ([]T, error) {
	const pageSize = 100
	const maxPages = 100
	first := fmt.Sprintf("%s?per_page=%d", path, pageSize)
	var all []T
	for page := 1; page <= maxPages; page++ {
		request := first
		if page > 1 {
			request += "&page=" + strconv.Itoa(page)
		}
		var rows []T
		if err := g.get(ctx, request, &rows); err != nil {
			return nil, fmt.Errorf("%s page %d: %w", path, page, err)
		}
		if rows == nil {
			return nil, fmt.Errorf("%s page %d: expected a list", path, page)
		}
		all = append(all, rows...)
		if len(rows) < pageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("%s: pagination exceeded %d pages", path, maxPages)
}

func (g githubForge) prReviewComments(ctx context.Context, path string, reviewID int64) ([]githubPRInlineComment, error) {
	return readPRPages[githubPRInlineComment](ctx, g, fmt.Sprintf("%s/reviews/%d/comments", path, reviewID))
}

func (g githubForge) checkPRFeedbackSource(ctx context.Context, path, head string) error {
	var pr struct {
		State string `json:"state"`
		Head  struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.get(ctx, path, &pr); err != nil {
		return err
	}
	if pr.State != "open" || head != "" && pr.Head.SHA != head {
		return fmt.Errorf("PR changed while reading feedback")
	}
	return nil
}

func (g githubForge) PRStatus(ctx context.Context, repo string, number int) (TodoPRStatus, error) {
	return g.readPRStatus(ctx, repo, number, nil)
}

func (g githubForge) readPRStatus(ctx context.Context, repo string, number int, feedback *prFeedback) (TodoPRStatus, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	var pr struct {
		Title          string    `json:"title"`
		State          string    `json:"state"`
		Draft          bool      `json:"draft"`
		Merged         bool      `json:"merged"`
		MergedAt       time.Time `json:"merged_at"`
		ClosedAt       time.Time `json:"closed_at"`
		Mergeable      *bool     `json:"mergeable"`
		MergeableState string    `json:"mergeable_state"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Reviewers []any `json:"requested_reviewers"`
		Teams     []any `json:"requested_teams"`
	}
	if err := g.get(ctx, path, &pr); err != nil {
		return TodoPRStatus{}, err
	}
	out := TodoPRStatus{Title: pr.Title, HeadOID: pr.Head.SHA, State: pr.State, Draft: pr.Draft, Review: "pending", Checks: "none",
		Conflicts: pr.MergeableState == "dirty" || pr.Mergeable != nil && !*pr.Mergeable}
	if pr.Merged {
		out.State = "merged"
		out.terminalAt = pr.MergedAt
	} else if pr.State == "closed" {
		out.terminalAt = pr.ClosedAt
	}
	if out.State != "open" {
		return out, nil
	}
	viewerChecked := out.Draft && pr.User.Login != ""
	viewer := ""
	if viewerChecked {
		// Unknown ownership must not turn someone else's draft into work for our supervisor.
		viewer = g.prViewer(ctx)
		out.Own = viewer != "" && strings.EqualFold(viewer, pr.User.Login)
	}
	if pr.Head.SHA == "" {
		return TodoPRStatus{}, fmt.Errorf("PR has no head commit")
	}
	reviews, err := readPRPages[githubPRReview](ctx, g, path+"/reviews")
	if err != nil {
		return TodoPRStatus{}, err
	}
	latest := map[string]githubPRReview{}
	for _, r := range reviews {
		// A submitted review is an event even if it did not change the review verdict. Use provider time and head.
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
			if r.ID > 0 && r.User.Login != "" && r.CommitID == pr.Head.SHA && !r.SubmittedAt.IsZero() &&
				(r.SubmittedAt.After(out.submittedReview.SubmittedAt) ||
					r.SubmittedAt.Equal(out.submittedReview.SubmittedAt) && r.ID > out.submittedReview.ID) {
				out.submittedReview = prSubmittedReview{ID: r.ID, SubmittedAt: r.SubmittedAt.UTC()}
			}
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[r.User.Login] = r
		}
	}
	for _, r := range latest {
		if r.State == "APPROVED" {
			out.Review = "approved"
		}
	}
	for _, r := range latest {
		if r.State == "CHANGES_REQUESTED" {
			out.Review = "changes_requested"
			// Only the provider can date a review.
			if strings.TrimSpace(r.User.Login) != "" && r.CommitID == pr.Head.SHA && !r.SubmittedAt.IsZero() &&
				(out.ChangesRequestedAt == nil || r.SubmittedAt.After(*out.ChangesRequestedAt) ||
					r.SubmittedAt.Equal(*out.ChangesRequestedAt) && r.ID > out.actionableReview.ID) {
				at := r.SubmittedAt.UTC()
				out.ChangesRequestedAt = &at
				out.actionableReview = prActionableReview{
					ID:          r.ID,
					Author:      strings.TrimSpace(r.User.Login),
					State:       r.State,
					Body:        strings.TrimSpace(r.Body),
					URL:         strings.TrimSpace(r.HTMLURL),
					SubmittedAt: at,
				}
			}
		}
	}
	if out.Review != "changes_requested" && len(pr.Reviewers)+len(pr.Teams) > 0 {
		out.Review = "required"
	}
	var status struct {
		State    string `json:"state"`
		Total    int    `json:"total_count"`
		Statuses []struct {
			ID          int64  `json:"id"`
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	commit := fmt.Sprintf("/repos/%s/commits/%s", repo, pr.Head.SHA)
	if err := g.get(ctx, commit+"/status", &status); err != nil {
		return TodoPRStatus{}, err
	}
	type failedCheckItem struct {
		id         int64
		name       string
		conclusion string
		url        string
		summary    string
	}
	var failedItems []failedCheckItem
	if status.Total > 0 {
		out.Checks = checkState(status.State)
		for _, st := range status.Statuses {
			if checkState(st.State) != "failure" {
				continue
			}
			name := strings.TrimSpace(st.Context)
			if st.ID == 0 && name == "" {
				continue
			}
			failedItems = append(failedItems, failedCheckItem{
				id:         st.ID,
				name:       name,
				conclusion: strings.ToLower(strings.TrimSpace(st.State)),
				url:        strings.TrimSpace(st.TargetURL),
				summary:    strings.TrimSpace(st.Description),
			})
		}
	}
	type checkRun struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"details_url"`
		HTMLURL    string `json:"html_url"`
		Output     struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	type checkRunsPage struct {
		Total int        `json:"total_count"`
		Runs  []checkRun `json:"check_runs"`
	}
	const maxCheckRunPages = 10
	var firstChecks checkRunsPage
	if err := g.get(ctx, commit+"/check-runs?per_page=100&filter=latest", &firstChecks); err != nil {
		return TodoPRStatus{}, err
	}
	totalChecks := firstChecks.Total
	allRuns := make([]checkRun, 0, len(firstChecks.Runs))
	seenRunID := make(map[int64]bool, len(firstChecks.Runs))
	appendRuns := func(runs []checkRun) {
		for _, run := range runs {
			if run.ID > 0 {
				if seenRunID[run.ID] {
					continue
				}
				seenRunID[run.ID] = true
			}
			allRuns = append(allRuns, run)
		}
	}
	appendRuns(firstChecks.Runs)
	for page := 2; totalChecks > len(allRuns) && len(firstChecks.Runs) > 0 && page <= maxCheckRunPages; page++ {
		var next checkRunsPage
		if err := g.get(ctx, fmt.Sprintf("%s/check-runs?per_page=100&filter=latest&page=%d", commit, page), &next); err != nil {
			return TodoPRStatus{}, err
		}
		if next.Total > totalChecks {
			totalChecks = next.Total
		}
		if len(next.Runs) == 0 {
			break
		}
		before := len(allRuns)
		appendRuns(next.Runs)
		if len(allRuns) == before {
			break
		}
	}
	checksTruncated := totalChecks > len(allRuns) || (len(status.Statuses) > 0 && status.Total > len(status.Statuses))
	if totalChecks > len(allRuns) {
		out.Checks = joinCheckState(out.Checks, "pending")
	}
	for _, run := range allRuns {
		state := "pending"
		if run.Status == "completed" {
			state = checkState(run.Conclusion)
		}
		out.Checks = joinCheckState(out.Checks, state)
		if state == "failure" {
			name := strings.TrimSpace(run.Name)
			if run.ID == 0 && name == "" {
				continue
			}
			failedItems = append(failedItems, failedCheckItem{
				id:         run.ID,
				name:       name,
				conclusion: strings.ToLower(strings.TrimSpace(run.Conclusion)),
				url:        cmp.Or(strings.TrimSpace(run.HTMLURL), strings.TrimSpace(run.DetailsURL)),
				summary:    cmp.Or(strings.TrimSpace(run.Output.Title), strings.TrimSpace(run.Output.Summary)),
			})
		}
	}
	if len(failedItems) > 0 {
		sort.Slice(failedItems, func(i, j int) bool {
			li, lj := strings.ToLower(failedItems[i].name), strings.ToLower(failedItems[j].name)
			if li != lj {
				return li < lj
			}
			if failedItems[i].name != failedItems[j].name {
				return failedItems[i].name < failedItems[j].name
			}
			return failedItems[i].id < failedItems[j].id
		})
		var names []string
		var digest []string
		for _, item := range failedItems {
			if item.name != "" && !slices.Contains(names, item.name) && len(names) < 3 {
				names = append(names, item.name)
			}
			if key := normalizedFailingCheckIdentity(item.name, item.id); key != "" && !slices.Contains(digest, key) {
				digest = append(digest, key)
			}
		}
		sort.Strings(digest)
		digestStr := strings.Join(digest, ",")
		if checksTruncated {
			// Never use a truncated API page's membership as an event fingerprint; keep it stable per head SHA.
			digestStr = "truncated"
		}
		out.failingCheck = prFailingCheck{
			ID:         failedItems[0].id,
			Name:       strings.Join(names, ", "),
			Conclusion: failedItems[0].conclusion,
			URL:        failedItems[0].url,
			Summary:    failedItems[0].summary,
			Count:      len(failedItems),
			Digest:     digestStr,
		}
	}
	if feedback != nil {
		*feedback = prFeedback{
			pullPath: path, commentsPath: fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number),
			author: pr.User.Login, viewer: viewer, viewerChecked: viewerChecked,
		}
		for _, r := range reviews {
			if r.State == "COMMENTED" {
				feedback.reviews = append(feedback.reviews, prFeedbackItem{githubPRFeedbackPost: r.githubPRFeedbackPost, at: r.SubmittedAt, commitID: r.CommitID})
			}
		}
	}
	return out, nil
}

func normalizedFailingCheckIdentity(name string, id int64) string {
	if norm := strings.ToLower(strings.Join(strings.Fields(name), " ")); norm != "" {
		return norm
	}
	if id > 0 {
		return "id:" + strconv.FormatInt(id, 10)
	}
	return ""
}

func checkState(state string) string {
	switch strings.ToLower(state) {
	case "success", "neutral", "skipped":
		return "success"
	case "failure", "error", "cancelled", "timed_out", "action_required", "startup_failure", "stale":
		return "failure"
	default:
		return "pending"
	}
}

func joinCheckState(a, b string) string {
	for _, state := range []string{"failure", "pending", "success"} {
		if a == state || b == state {
			return state
		}
	}
	return "none"
}

// prsPolled bounds how many of a task's earlier PRs the poll reads besides its current one, newest first.
const prsPolled = 4

// polledPRs retains the current PR and bounded earlier sample (#7935), then includes a typed wait's source if needed.
// A bound source outside that sample adds at most one read, never displacing another open or unknown PR.
func polledPRs(t Todo) []string {
	out := []string{t.PRURL}
	known := map[string]TodoPRStatus{}
	for _, f := range t.OtherPRs {
		known[f.URL] = f
	}
	for i := len(t.PRs) - 1; i >= 0 && len(out) <= prsPolled; i-- {
		u := t.PRs[i]
		if u == t.PRURL || !ciPRURL.MatchString(u) && !otherReviewLink(u) {
			continue
		}
		if f, ok := known[u]; ok && (f.State == "merged" || f.State == "closed") {
			continue
		}
		out = append(out, u)
	}
	if r := t.Supervision.parking(); r != nil && r.Evidence != nil {
		for i := len(t.PRs) - 1; i >= 0; i-- {
			u := t.PRs[i]
			if slices.Contains(out, u) || !parkWaitsOnPR(r, u) {
				continue
			}
			if f, ok := known[u]; !ok || f.State != "merged" && f.State != "closed" || f.CheckedAt.Before(r.At) {
				out = append(out, u)
			}
			break
		}
		if u := externalParkPRURL(r); u != "" && !slices.ContainsFunc(out, func(known string) bool {
			return ciPRURL.MatchString(known) && triageNormalizePRURL(known) == u
		}) {
			out = append(out, u)
		}
	}
	return out
}

// prFacts are t's last observed facts for PR url: its current PR's, or an earlier one's.
func prFacts(t Todo, url string) *TodoPRStatus {
	if url == t.PRURL {
		if t.PRStatus != nil && t.PRStatus.URL == url {
			return t.PRStatus
		}
		return nil
	}
	for i := range t.OtherPRs {
		if t.OtherPRs[i].URL == url {
			return &t.OtherPRs[i]
		}
	}
	return nil
}

// The existing minute deploy pass owns polling. Reads never block a task list or
// snapshot, and failures leave the last observation to expire rather than invent success.
// A review request on another host is polled the same way through its code host (code_host.go), which says its state
// only: no review, checks or head.
func (s *Service) sweepPRStatuses(ctx context.Context, now time.Time) {
	f, github := s.forge().(prStatusForge)
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	s.followLinkedDeliveryPRs(ctx, now) // delivery PRs only in repository links (#14049)
	s.todos.mu.RLock()
	var tasks []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && (t.PRURL != "" || len(t.PRs) > 0 || externalParkPRURL(t.Supervision.parking()) != "") && !isAskTodo(t.ID) {
			tasks = append(tasks, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	for _, t := range tasks {
		if t.DoneAt != nil || t.Cancelled() {
			continue
		}
		held := t.Supervision.parking()
		source := externalParkPRURL(held)
		for _, url := range polledPRs(t) {
			m := ciPRURL.FindStringSubmatch(url)
			var other codeHost
			switch {
			case m != nil && !github:
				continue
			case m == nil && !otherReviewLink(url):
				continue
			case m == nil:
				if other, _ = s.codeHostFor(url); other == nil {
					continue
				}
			}
			var checkedAt time.Time
			bound := source != "" && source == triageNormalizePRURL(url)
			if bound {
				if held.ExternalPR != nil {
					checkedAt = held.ExternalPR.CheckedAt
				}
			} else if old := prFacts(t, url); old != nil {
				checkedAt = old.CheckedAt
			}
			if !checkedAt.IsZero() && now.Sub(checkedAt) < deployRecheck {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			key := t.ID
			if url != t.PRURL {
				key = t.ID + "|" + url
			}
			p := &s.deploy
			p.mu.Lock()
			last := p.seen[key]
			if last.statusPR == url && now.Sub(last.statusAt) < deployRecheck {
				p.mu.Unlock()
				continue
			}
			if p.seen == nil || len(p.seen) > 2000 {
				p.seen = map[string]prSeen{}
			}
			last.statusPR, last.statusAt = url, now
			p.seen[key] = last // failures are attempts too, not fresh evidence
			p.mu.Unlock()
			var status TodoPRStatus
			var feedback prFeedback
			g, canReadFeedback := f.(githubForge)
			var err error
			if other != nil {
				status, err = otherPRStatus(ctx, other, url)
			} else {
				number, _ := strconv.Atoi(m[2])
				if canReadFeedback && url == t.PRURL && s.prEventsRouteToAgentOn(ctx) {
					status, err = g.readPRStatus(ctx, m[1], number, &feedback)
				} else {
					status, err = f.PRStatus(ctx, m[1], number)
				}
			}
			if err != nil {
				s.logf("task %s: PR status: %v", t.ID, err)
				continue
			}
			observedAt := now.UTC()
			// The pass may have started before this captured wait. Its source request cannot predate that wait.
			if held != nil && held.Evidence != nil && (parkWaitsOnPR(held, url) || bound) && observedAt.Before(held.At) {
				observedAt = held.At
			}
			status.URL, status.CheckedAt = url, observedAt
			if err := s.applyPRStatus(ctx, t, status); err != nil {
				s.logf("task %s: keep PR status: %v", t.ID, err)
				continue
			}
			if feedback.pullPath != "" {
				if err := s.routePRFeedback(ctx, t.ID, status, g, feedback); err != nil {
					s.logf("task %s: route PR feedback: %v", t.ID, err)
				}
			}
		}
	}
}

func (s *Service) isReferencePRLink(ctx context.Context, todoID, prURL string) bool {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return false
	}
	want := normalizePRKey(prURL)
	if want == "" {
		return false
	}
	set, err := st.TodoRepositories(ctx, todoID)
	if err != nil {
		return false
	}
	for _, row := range set.Repositories {
		for _, l := range row.Links {
			if l.Kind != "pull_request" {
				continue
			}
			if normalizePRKey(l.CanonicalURL) == want && l.Role == repository.LinkRoleReference {
				return true
			}
		}
	}
	return false
}

func (s *Service) applyExternalParkOnlyPRStatus(ctx context.Context, t, current Todo, held *TaskReview, status TodoPRStatus) error {
	conflictRouted, reviewRouted := false, false
	if externalParkAcceptsStatus(current, held, status) && !needsUserWakeEligible(current) && s.prEventsRouteToAgentOn(ctx) {
		conflictRouted, reviewRouted = s.routePREvents(ctx, current, todoStage{}, held, status, true)
	}
	_, err := s.applyExternalParkPRStatus(ctx, t, status, conflictRouted, reviewRouted)
	return err
}

func (s *Service) applyPRStatus(ctx context.Context, t Todo, status TodoPRStatus) error {
	// The wait belongs to the snapshot taken before the forge request, not a park created while it was in flight.
	held := t.Supervision.parking()
	status.Title = repository.ObservedLinkTitle(status.Title)
	externalSource := externalParkPRURL(held)
	isExternalParkPR := externalSource != "" && externalSource == triageNormalizePRURL(status.URL)
	terminalState := strings.EqualFold(status.State, "merged") || strings.EqualFold(status.State, "closed")
	reconcileTerminalBlockers := func() {
		if _, err := s.reconcileTodoBlockersForPR(ctx, status); err != nil {
			s.logf("task %s: reconcile blockers for PR %s: %v", t.ID, status.URL, err)
		}
	}
	reconcileEarlyTerminal := func() {
		if terminalState {
			s.onPRStatusMergeTrain(ctx, t, status)
			reconcileTerminalBlockers()
		}
	}
	current, ok := s.todoNow(t.ID)
	if !ok || current.DoneAt != nil {
		reconcileEarlyTerminal()
		return nil
	}
	if current.PRURL != status.URL && !slices.Contains(current.PRs, status.URL) {
		if isExternalParkPR {
			if err := s.applyExternalParkOnlyPRStatus(ctx, t, current, held, status); err != nil {
				return err
			}
		}
		reconcileEarlyTerminal()
		return nil
	}
	t = current
	checksReason := ""
	var completionMode assistantwire.CompletionMode
	if t.hasTaskRuntime() {
		checksReason = i18n.T(s.lang(ctx), "assistant-reports.pr_checks_pending")
		brief, _ := s.committedBriefOfTodo(t.ID)
		completionMode = brief.CompletionModeNow
	}
	// Returning from a known open PR (or a legacy PR-stage hold) wakes the same supervisor once no other PR of the task
	// is still open (#7935). A merged PR alone is never acceptance, and unrelated later parks stay put.
	old := prFacts(t, status.URL)
	wasOpen := old != nil && old.State == "open"
	// A PR-stage hold first read terminal; "merge" too, which the task's own current PR merging records before an
	// earlier PR of it is read.
	legacyHold := old == nil && (t.Delivery != nil && t.Delivery.State == deliveryPR || t.Stage == "review" || t.Stage == "merge" && len(t.PRs) > 0)
	terminal := status.State == "merged" || status.State == "closed"
	// The stage's PRs are checked under the stage lock first (below): an observation of a PR an Agent reply replaced
	// while the request was in flight changes nothing, neither the facts nor the supervision.
	b := &s.reports
	b.stageWriting.Lock()
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		b.stageWriting.Unlock()
		return fmt.Errorf("stages could not be read")
	}
	cur := b.stages[t.ID]
	var previous *TodoPRStatus
	if cur.PR == status.URL {
		previous = cur.PRStatus
	} else if f, ok := cur.EarlierPRStatus[status.URL]; ok {
		previous = &f
	}
	if previous != nil && status.CheckedAt.Before(previous.CheckedAt) {
		b.mu.Unlock()
		b.stageWriting.Unlock()
		return nil // an older response must not replace newer facts or resolve a wait
	}
	var prevPR *TodoPRStatus
	if previous != nil {
		cp := *previous
		prevPR = &cp
	}
	switch {
	case cur.PR == status.URL:
		cur.PRStatus = &status
	case slices.Contains(cur.PRs, status.URL):
		facts := make(map[string]TodoPRStatus, len(cur.EarlierPRStatus)+1)
		for u, f := range cur.EarlierPRStatus {
			facts[u] = f
		}
		facts[status.URL] = status
		cur.EarlierPRStatus = facts
	default:
		b.mu.Unlock()
		b.stageWriting.Unlock()
		if isExternalParkPR {
			if err := s.applyExternalParkOnlyPRStatus(ctx, t, current, held, status); err != nil {
				return err
			}
		}
		reconcileEarlyTerminal()
		return nil // a new Agent reply replaced the PR while the request was in flight
	}
	if t.hasTaskRuntime() && (previous == nil || !status.sameRuntimeFacts(*previous) ||
		previous.CheckedAt.Before(cur.RuntimeAt) && !status.CheckedAt.Before(cur.RuntimeAt)) {
		cur.refreshRuntimeFromProviders(status.CheckedAt, checksReason, completionMode)
	}
	if err := s.keepTodoStageLocked(ctx, t.ID, cur); err != nil {
		b.mu.Unlock()
		b.stageWriting.Unlock()
		return err
	}
	b.mu.Unlock()
	s.publishRuntimeStage(t.ID, cur)
	// Keep title writes in observation order, even if two polls for the same PR finish together.
	if st, ok := s.store().(todoPRLinkTitleStore); ok && status.Title != "" {
		if err := st.SetTodoPRLinkTitle(ctx, t.ID, triageNormalizePRURL(status.URL), status.Title); err != nil {
			// A link metadata error can retry on the next poll without delaying delivery or supervision.
			s.logf("task %s: save PR link title: %v", t.ID, err)
		}
	}
	b.stageWriting.Unlock()
	s.discardStalePRFeedback(ctx, t.ID, prevPR, status)
	defer s.todosChanged(ctx)
	parkWoke := false
	preStage := t.Stage
	needsUserMergeHandled := false
	if needsUserWakeEligible(t) {
		needsUserMergeHandled = s.maybeWakeNeedsUserForPR(ctx, t, prevPR, status)
	} else {
		if status.State == "open" && status.Draft && status.Own && status.HeadOID != "" && t.Supervision != nil {
			s.signal(superviseSignal{Todo: t.ID, Kind: sigDraftPR, PRURL: status.URL, Key: status.URL + "@" + status.HeadOID})
		}
		refPR := status.Conflicts && !isExternalParkPR && s.isReferencePRLink(ctx, t.ID, status.URL)
		conflictRouted, reviewRouted := false, false
		if s.prEventsRouteToAgentOn(ctx) {
			conflictRouted, reviewRouted = s.routePREvents(ctx, t, cur, held, status, !refPR)
		}
		externalParkWoke := false
		if isExternalParkPR {
			var err error
			externalParkWoke, err = s.applyExternalParkPRStatus(ctx, t, status, conflictRouted, reviewRouted)
			if err != nil {
				return err
			}
			if externalParkWoke {
				parkWoke = true
				if refreshed, ok := s.todoNow(t.ID); ok {
					t = refreshed
				}
			}
		}
		if !conflictRouted && !refPR && !externalParkWoke &&
			status.State == "open" && status.Conflicts && status.HeadOID != "" &&
			t.Supervision != nil && !t.Supervision.externalPRConflictWoke(status.URL, status.HeadOID) {
			s.signal(superviseSignal{Todo: t.ID, Kind: sigPRConflict, PRURL: status.URL, Key: status.URL + "@" + status.HeadOID,
				Detail: fmt.Sprintf("PR %s at head %s is open and has merge conflicts.", status.URL, status.HeadOID)})
		}
		reviewChange := !reviewRouted && parkReviewChangeRequested(held, status)
		if !externalParkWoke && held != nil && (terminal && (wasOpen || legacyHold || held.Evidence != nil) || reviewChange) {
			// Typed waits reconcile terminal facts even after a failed ledger write or a restart. Legacy parks retain
			// their transition/stage gate. A new changes-request review is actionable before other PRs resolve.
			v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
				p := v.parking()
				if p == nil || !p.At.Equal(held.At) {
					return false
				}
				now, ok := s.todoNow(t.ID)
				if !ok || now.DoneAt != nil || now.PRURL != status.URL && !slices.Contains(now.PRs, status.URL) || !s.stageFollows(t.ID, status.URL) {
					return false
				}
				f := prFacts(now, status.URL)
				if f == nil || f.State != status.State || !f.CheckedAt.Equal(status.CheckedAt) {
					return false
				}
				if terminal {
					if !parkPRsResolved(now, p, status.URL) {
						return false
					}
				} else if f.HeadOID != status.HeadOID || !parkReviewChangeRequested(p, *f) ||
					!f.ChangesRequestedAt.Equal(*status.ChangesRequestedAt) {
					return false
				}
				v.State = reviewPending
				return true
			})
			if err != nil {
				return err
			}
			if changed {
				parkWoke = true
				detail := "PR " + status.State + ": " + status.URL
				if status.State == "closed" {
					if hint := s.closedPRSuccessors(ctx, t, status.URL); hint != "" { // continued or abandoned (#14049)
						detail += ". " + hint
					}
				}
				if reviewChange {
					detail = "PR changes requested: " + status.URL
				}
				s.signal(superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v), Detail: detail})
			}
		}
	}
	if status.URL == t.PRURL && status.State == "merged" && stageIndex(t.Stage) < stageIndex("merge") {
		s.setTaskStageOn(ctx, t.ID, "merge", "", "", false, status.URL)
	}
	s.onPRStatusMergeTrain(ctx, t, status)
	if terminalState {
		reconcileTerminalBlockers()
	}
	if s.settings(ctx).todosV2On() {
		// Stage/contract after edges can become ready when the tracked PR changes.
		if _, err := s.ReconcileTodoBlockers(ctx, t.ID); err != nil {
			s.logf("task %s: reconcile PR stage prerequisites: %v", t.ID, err)
		}
	}
	if strings.EqualFold(status.State, "merged") {
		if parkWoke || needsUserMergeHandled {
			s.prMergeWakeTold(ctx, []superviseSignal{{Todo: t.ID, Kind: sigPRMerged, PRURL: status.URL, Key: prMergeSignalKey(status.URL)}})
		} else {
			s.maybeEmitUnparkedPRMerged(ctx, t.ID, status.URL, held, parkWoke, wasOpen, preStage)
		}
		s.drainWorkspaceQueuesForPrereq(ctx, t.ID)
		s.evaluateTodoAfterDependents(ctx, t.ID)
	} else if status.State == "closed" && !parkWoke && !needsUserMergeHandled {
		// The last open delivery PR closed unmerged after another merged: the delivery is as complete as it gets, which
		// its supervisor hears through the merged one (#14049). This closing is that completion, whether or not the closed
		// PR was ever read open (its earlier reads may have failed); a closing already recorded is not news again.
		if u := s.mergedDeliveryPR(ctx, t.ID); u != "" {
			newlyClosed := old == nil || !strings.EqualFold(old.State, "closed")
			s.maybeEmitUnparkedPRMerged(ctx, t.ID, u, held, parkWoke, newlyClosed, preStage)
		}
	}
	return nil
}

// parkReviewChangeRequested dates an active current-head review, not the poll, against this typed wait. Ambiguous
// same-second provider times stay parked rather than attributing a possibly pre-park review to a newer wait.
func parkReviewChangeRequested(r *TaskReview, status TodoPRStatus) bool {
	return r != nil && !r.At.IsZero() && r.Evidence != nil && parkWaitsOnPR(r, status.URL) &&
		status.State == "open" && status.HeadOID != "" && status.Review == "changes_requested" &&
		status.ChangesRequestedAt != nil && status.ChangesRequestedAt.After(r.At) &&
		!status.CheckedAt.IsZero() && !status.ChangesRequestedAt.After(status.CheckedAt)
}

// stagePR is the PR task id's stage holds now ("" when none).
func (s *Service) stagePR(id string) string {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stages[id].PR
}

// stageFollows says whether task id's stage still follows the PR at url: its current PR or an earlier one.
func (s *Service) stageFollows(id, url string) bool {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stages[id]
	return st.PR == url || slices.Contains(st.PRs, url)
}

// parkPRsResolved retains the multi-PR gate, using current facts under the supervision write lock. A bound earlier
// PR may already be terminal and no longer polled: keep its source available when the last other PR resolves.
func parkPRsResolved(t Todo, r *TaskReview, observed string) bool {
	for _, u := range polledPRs(t) {
		if f := prFacts(t, u); f == nil || f.State != "merged" && f.State != "closed" {
			return false
		}
	}
	var terminal []string
	for _, u := range append([]string{t.PRURL}, t.PRs...) {
		// An old terminal receipt is not evidence that this wait's source has stayed resolved.
		if f := prFacts(t, u); f != nil && (f.State == "merged" || f.State == "closed") &&
			(r.Evidence == nil || u == observed || !f.CheckedAt.Before(r.At)) {
			terminal = append(terminal, u)
		}
	}
	return parkWaitsOnPR(r, terminal...)
}

// parkWaitsOnPR uses the exact source of a typed review or an automatically admitted passive PR wait.
// Only legacy parks without evidence use prose or bare "#N"; manual external-event binding is separate.
func parkWaitsOnPR(r *TaskReview, urls ...string) bool {
	if r == nil || r.WaitingOn != parkExternalEvent && r.WaitingOn != parkSomeoneElse {
		return false
	}
	if e := r.Evidence; e != nil {
		review := e.Kind == "merge_review" && e.Ready && r.WaitingOn == parkSomeoneElse
		automatic := r.Automatic != nil && e.Kind == "external_event" && r.WaitingOn == parkExternalEvent
		if !review && !automatic {
			return false
		}
		source := triageNormalizePRURL(e.URL)
		if source == "" || ciPRURL.FindString(source) != source && !otherReviewLink(source) { // other hosts: code_host.go
			return false
		}
		for _, u := range urls {
			if triageNormalizePRURL(u) == source {
				return true
			}
		}
		return false
	}
	reason := strings.ToLower(r.Reason)
	links, refs := ciPRURL.FindAllStringSubmatch(reason, -1), prBareRef.FindAllStringSubmatch(reason, -1)
	for _, url := range urls {
		m := ciPRURL.FindStringSubmatch(url)
		if m == nil {
			continue
		}
		for _, link := range links {
			if strings.EqualFold(link[1], m[1]) && link[2] == m[2] {
				return true
			}
		}
		for _, ref := range refs {
			if ref[1] == m[2] {
				return true
			}
		}
	}
	return false
}

// otherPRStatus is url's state as code host h reads it, with the labels GitHub's read keeps when it knows no more.
func otherPRStatus(ctx context.Context, h codeHost, url string) (TodoPRStatus, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := h.ReviewStatus(cctx, url)
	if err == nil && st.State == "" {
		err = errReviewUnsupported
	}
	if err != nil {
		return TodoPRStatus{}, fmt.Errorf("%s: %w", h.Name(), err)
	}
	return TodoPRStatus{State: st.State, Review: "pending", Checks: "none", terminalAt: st.TerminalAt}, nil
}
