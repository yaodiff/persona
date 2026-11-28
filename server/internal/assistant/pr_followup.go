package assistant

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// prFollowupStore is the Postgres persistence for post-closure PR subscriptions. A store that does not implement it
// (in-memory test adapter) simply has no follow-up lifecycle, matching the rest of the repository store's fallback.
type prFollowupStore interface {
	UpsertPRFollowups(ctx context.Context, todoID, supervisor, origin string, prURLs []string, now time.Time) error
	UpsertPRFollowupRecords(ctx context.Context, subs []repository.PRFollowup) error
	ListPRFollowups(ctx context.Context) ([]repository.PRFollowup, error)
	SavePRFollowupObservation(ctx context.Context, f repository.PRFollowup) (bool, error)
	SavePRFollowupWatchStatus(ctx context.Context, id, status, reason string, updatedAt time.Time) (bool, error)
	DeletePRFollowup(ctx context.Context, id string) error
	StalePRFollowups(ctx context.Context, before time.Time) ([]repository.PRFollowup, error)
}

type followupWatchHint struct {
	status string
	reason string
}

type followupWatchHintsKey struct{}

// withFollowupWatchHints attaches a per-acceptance map so acceptedDeliveryError can pass host-not-connected findings
// to createPRFollowups without changing reviewDoneTodo's signature or repeating remote calls.
func withFollowupWatchHints(ctx context.Context) context.Context {
	return context.WithValue(ctx, followupWatchHintsKey{}, map[string]followupWatchHint{})
}

func followupWatchHintKey(prURL string) string {
	prURL = strings.TrimSpace(prURL)
	if ciPRURL.MatchString(prURL) {
		return triageNormalizePRURL(prURL)
	}
	return prURL
}

func recordFollowupWatchHint(ctx context.Context, prURL, status, reason string) {
	hints, ok := ctx.Value(followupWatchHintsKey{}).(map[string]followupWatchHint)
	if !ok || hints == nil {
		return
	}
	hint := followupWatchHint{status: status, reason: reason}
	if key := followupWatchHintKey(prURL); key != "" {
		hints[key] = hint
	}
	// Host-level disconnection applies to every delivery PR on the same host within this acceptance pass.
	if reason == repository.PRFollowupReasonHostNotConnected {
		if host, ok := repository.RemoteHost(prURL); ok && host != "" {
			hints["host:"+host] = hint
		}
	}
}

func lookupFollowupWatchHint(ctx context.Context, prURL string) (followupWatchHint, bool) {
	hints, ok := ctx.Value(followupWatchHintsKey{}).(map[string]followupWatchHint)
	if !ok || hints == nil {
		return followupWatchHint{}, false
	}
	if h, found := hints[followupWatchHintKey(prURL)]; found {
		return h, true
	}
	if host, ok := repository.RemoteHost(prURL); ok && host != "" {
		if h, found := hints["host:"+host]; found {
			return h, true
		}
	}
	return followupWatchHint{}, false
}

func (s *Service) prFollowupDB() prFollowupStore {
	st, ok := s.store().(prFollowupStore)
	if !ok {
		return nil
	}
	return st
}

// sweepPRFollowupsBounded is sweepPRFollowups with the same per-pass deadline as the open-to-do PR sweep.
func (s *Service) sweepPRFollowupsBounded(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), prStatusPassMost)
	defer cancel()
	s.sweepPRFollowups(ctx, now)
}

// followupRecheck bounds how often one subscription's PR is polled, reusing the deploy sweep's cadence. A var so tests
// can shorten it without waiting out the real ten-minute floor.
var followupRecheck = deployRecheck

// followupStaleAfter is how long a non-terminal subscription stays silent before it surfaces in the daily briefing.
const followupStaleAfter = 24 * time.Hour

// deliveryPRsForFollowup returns the stage-tracked delivery PRs that may still change after closure.
func deliveryPRsForFollowup(t Todo) []string {
	out := make([]string, 0, 1+len(t.PRs))
	seen := map[string]bool{}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] || !ciPRURL.MatchString(u) && !otherReviewLink(u) {
			return
		}
		if facts := prFacts(t, u); facts != nil && facts.State == repository.ReviewMerged {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	add(t.PRURL)
	for _, u := range t.PRs {
		add(u)
	}
	return out
}

// initialFollowupWatchState determines the initial persisted watch status for prURL without remote API calls.
func (s *Service) initialFollowupWatchState(ctx context.Context, prURL string) (status, reason string) {
	if hint, ok := lookupFollowupWatchHint(ctx, prURL); ok && hint.status != "" {
		return hint.status, hint.reason
	}
	if ciPRURL.MatchString(prURL) {
		forge := s.forge()
		if _, ok := forge.(prStatusForge); !ok {
			return repository.PRFollowupWatchHostNotConnected, repository.PRFollowupReasonHostNotConnected
		}
		if g, ok := forge.(githubForge); ok && g.base == "https://api.github.com" && !s.githubAuthConfiguredLocally(ctx, g) {
			return repository.PRFollowupWatchHostNotConnected, repository.PRFollowupReasonHostNotConnected
		}
		return repository.PRFollowupWatchActive, ""
	}
	if !otherReviewLink(prURL) {
		return repository.PRFollowupWatchHostNotConnected, repository.PRFollowupReasonUnsupportedHost
	}
	h, _ := s.codeHostFor(prURL)
	if h == nil {
		if s.repoExt != nil && !s.providersListed() {
			return repository.PRFollowupWatchActive, ""
		}
		return repository.PRFollowupWatchHostNotConnected, repository.PRFollowupReasonUnsupportedHost
	}
	if p, ok := h.(providerHost); ok && p.info.State != repository.ProviderAvailable {
		return repository.PRFollowupWatchHostNotConnected, repository.PRFollowupReasonHostNotConnected
	}
	return repository.PRFollowupWatchActive, ""
}

// githubAuthConfiguredLocally reports whether production githubForge has any local credential source available
// (connected GitHub connector, environment token, or official gh CLI) without making remote network calls.
func (s *Service) githubAuthConfiguredLocally(ctx context.Context, g githubForge) bool {
	if g.via != nil {
		if st, ok := s.ext.(githubConnectorStatus); ok {
			if st.GitHubConnected(ctx) {
				return true
			}
		} else {
			// Custom test githubGetter without status check: let the first sweep decide.
			return true
		}
	}
	if strings.TrimSpace(os.Getenv("GH_TOKEN")) != "" || strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "" {
		return true
	}
	cli, err := hasGitHubCLI(ctx)
	return err != nil || cli
}

// createPRFollowups records the task's closure time and initial watch status so the first sweep can date any terminal
// events it finds and unwatched subscriptions are recorded durably from closure.
func (s *Service) createPRFollowups(ctx context.Context, t Todo) {
	st := s.prFollowupDB()
	if st == nil || t.DoneAt == nil {
		return
	}
	prs := deliveryPRsForFollowup(t)
	if len(prs) == 0 {
		return
	}
	supervisor := ""
	origin := t.Origin
	if t.Supervision != nil {
		supervisor = t.Supervision.Supervisor
	}
	if supervisor == "" {
		supervisor = origin // no recorded supervisor: fall back to the to-do's own conversation rather than dropping it
	}
	doneAt := t.DoneAt.UTC()
	records := make([]repository.PRFollowup, 0, len(prs))
	for _, prURL := range prs {
		watchStatus, watchReason := s.initialFollowupWatchState(ctx, prURL)
		var watchUpdatedAt *time.Time
		if watchStatus != repository.PRFollowupWatchActive {
			at := doneAt
			watchUpdatedAt = &at
		}
		records = append(records, repository.PRFollowup{
			TodoID:         t.ID,
			PRURL:          prURL,
			Supervisor:     supervisor,
			Origin:         origin,
			CreatedAt:      doneAt,
			WatchStatus:    watchStatus,
			WatchReason:    watchReason,
			WatchUpdatedAt: watchUpdatedAt,
		})
	}
	if err := st.UpsertPRFollowupRecords(ctx, records); err != nil {
		s.logf("task %s: PR follow-up subscriptions not saved: %v", t.ID, err)
	}
}

// sweepPRFollowups polls every active or unwatched post-closure subscription: terminal PRs clear the subscription and
// save quiet notices on post-closure transitions; a new conflict or a current-head changes-requested review is kept for the original
// supervisor once per head. When GitHub is disconnected or the PR's host has no code host that can read review state,
// the subscription persists watch_status = host_not_connected without updating last_observed_at or spamming notices,
// and returns to active watching once a later sweep successfully polls the PR. The to-do is never reopened.
func (s *Service) sweepPRFollowups(ctx context.Context, now time.Time) {
	st := s.prFollowupDB()
	if st == nil {
		return
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return
	}
	f, github := s.forge().(prStatusForge)
	subs, err := st.ListPRFollowups(ctx)
	if err != nil {
		s.logf("PR follow-ups: list failed: %v", err)
		return
	}
	now = now.UTC()
	for i := range subs {
		if ctx.Err() != nil {
			return
		}
		sub := subs[i]
		if followupRecheck > 0 {
			lastCheck := sub.LastObservedAt
			if sub.WatchUpdatedAt != nil && (lastCheck == nil || sub.WatchUpdatedAt.After(*lastCheck)) {
				lastCheck = sub.WatchUpdatedAt
			}
			if lastCheck != nil && now.Sub(*lastCheck) < followupRecheck {
				continue
			}
		}
		res := s.observeFollowupPR(ctx, sub.PRURL, f, github)
		switch res.outcome {
		case followupTransient:
			continue
		case followupUnwatched:
			if _, err := st.SavePRFollowupWatchStatus(ctx, sub.ID, repository.PRFollowupWatchHostNotConnected, res.reason, now); err != nil {
				s.logf("PR follow-up %s: watch status not saved: %v", sub.PRURL, err)
			}
			continue
		case followupObserved:
			observedAt := now
			res.status.URL = sub.PRURL
			res.status.CheckedAt = observedAt
			s.applyFollowupObservation(ctx, st, sub, res.status, observedAt)
		}
	}
}

type followupObserveOutcome int

const (
	followupObserved followupObserveOutcome = iota
	followupUnwatched
	followupTransient
)

type followupObserveResult struct {
	outcome followupObserveOutcome
	status  TodoPRStatus
	reason  string
}

// observeFollowupPR reads one PR through the GitHub forge or the matching code host, distinguishing a live observation
// from a disconnected/unsupported host (persisted as host_not_connected) and a transient error (retried next pass).
func (s *Service) observeFollowupPR(ctx context.Context, url string, f prStatusForge, github bool) followupObserveResult {
	m := ciPRURL.FindStringSubmatch(url)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if m != nil {
		if !github {
			return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonHostNotConnected}
		}
		number, _ := strconv.Atoi(m[2])
		status, err := f.PRStatus(cctx, m[1], number)
		if err != nil {
			s.logf("PR follow-up %s: %v", url, err)
			if errors.Is(err, errGitHubUnconfigured) {
				return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonHostNotConnected}
			}
			return followupObserveResult{outcome: followupTransient}
		}
		return followupObserveResult{outcome: followupObserved, status: status}
	}
	if !otherReviewLink(url) {
		return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonUnsupportedHost}
	}
	h, _ := s.codeHostFor(url)
	if h == nil {
		if s.repoExt != nil && !s.providersListed() {
			return followupObserveResult{outcome: followupTransient}
		}
		s.logf("PR follow-up %s: no code host reads it", url)
		return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonUnsupportedHost}
	}
	status, err := otherPRStatus(cctx, h, url)
	if err != nil {
		s.logf("PR follow-up %s: %v", url, err)
		if errors.Is(err, errReviewUnsupported) || errors.Is(err, errNotReviewRequest) {
			return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonUnsupportedHost}
		}
		if errors.Is(err, errGitLabTokenUnreadable) {
			return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonHostNotConnected}
		}
		var ce *repository.CheckError
		if errors.As(err, &ce) && ce.Code == "authentication" {
			return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonHostNotConnected}
		}
		var pe *repository.ProviderError
		if errors.As(err, &pe) {
			switch pe.Code {
			case "no_provider", "provider_off", "not_signed_in":
				return followupObserveResult{outcome: followupUnwatched, reason: repository.PRFollowupReasonHostNotConnected}
			case providersStartingCode:
				return followupObserveResult{outcome: followupTransient}
			}
		}
		return followupObserveResult{outcome: followupTransient}
	}
	return followupObserveResult{outcome: followupObserved, status: status}
}

// applyFollowupObservation clears terminal subscriptions, records news for the next conversation turn, and refreshes
// the stored facts and active watch status for PRs that remain open.
func (s *Service) applyFollowupObservation(ctx context.Context, st prFollowupStore, sub repository.PRFollowup, status TodoPRStatus, observedAt time.Time) {
	terminal := status.State == "merged" || status.State == "closed"
	// Conflict and review only act on the current head, and each head is recorded once.
	newConflict := status.State == "open" && status.Conflicts && status.HeadOID != "" &&
		sub.NotifiedConflictHead != status.HeadOID
	newReview := status.State == "open" && status.Review == "changes_requested" && status.HeadOID != "" &&
		status.ChangesRequestedAt != nil && sub.NotifiedReviewHead != status.HeadOID &&
		!status.ChangesRequestedAt.After(observedAt)

	t, hasTodo := s.todoNow(sub.TodoID)
	title := sub.TodoID
	supervisor := sub.Supervisor
	if hasTodo {
		title = t.Title
	}

	switch {
	case terminal:
		// An open observation proves a transition. Forge time can also prove one before the first poll.
		if (sub.State == repository.ReviewOpen || status.terminalAt.After(sub.CreatedAt)) &&
			!s.keepPRFollowup(ctx, sub, title, supervisor, status.State, status.HeadOID) {
			return // retry the observation until the quiet record is safe
		}
		if err := st.DeletePRFollowup(ctx, sub.ID); err != nil {
			s.logf("PR follow-up %s: delete failed: %v", sub.PRURL, err)
		}
		return
	case newConflict:
		if !s.keepPRFollowup(ctx, sub, title, supervisor, "conflict", status.HeadOID) {
			return
		}
		sub.NotifiedConflictHead = status.HeadOID
	case newReview:
		if !s.keepPRFollowup(ctx, sub, title, supervisor, "changes_requested", status.HeadOID) {
			return
		}
		sub.NotifiedReviewHead = status.HeadOID
	}

	sub.HeadOID = status.HeadOID
	sub.State = status.State
	sub.Review = status.Review
	sub.ChangesRequestedAt = status.ChangesRequestedAt
	sub.LastObservedAt = &observedAt
	sub.WatchStatus = repository.PRFollowupWatchActive
	sub.WatchReason = ""
	sub.WatchUpdatedAt = &observedAt
	if _, err := st.SavePRFollowupObservation(ctx, sub); err != nil {
		s.logf("PR follow-up %s: observation not saved: %v", sub.PRURL, err)
	}
}

// keepPRFollowup carries post-closure news into a later turn the supervisor
// was already going to run.
func (s *Service) keepPRFollowup(ctx context.Context, sub repository.PRFollowup, title, supervisor, event, head string) bool {
	if supervisor == "" {
		return true
	}
	text := prompts.RenderPRFollowupNotice(prompts.PRFollowupNotice{
		ID: sub.TodoID, Title: title, PRURL: sub.PRURL, Event: event,
		Language: answerLanguage(s.hello(ctx).Locale),
	})
	_, kept := s.keepRelayLater(supervisor, relayMsg{
		From: sub.TodoID, FromTitle: title, Tool: prFollowupTool, Text: text, At: time.Now().UTC(),
		OnceID: sub.ID + ":" + event + ":" + head,
	})
	return kept
}

// stalePRFollowups returns non-terminal subscriptions to surface in the daily briefing: unwatched subscriptions
// (host_not_connected) immediately, plus actively watched subscriptions silent for at least followupStaleAfter.
func (s *Service) stalePRFollowups(ctx context.Context, now time.Time) []repository.PRFollowup {
	st := s.prFollowupDB()
	if st == nil {
		return nil
	}
	subs, err := st.StalePRFollowups(ctx, now.Add(-followupStaleAfter))
	if err != nil {
		s.logf("PR follow-ups: stale list failed: %v", err)
		return nil
	}
	return slices.DeleteFunc(subs, func(f repository.PRFollowup) bool { return f.State == "merged" || f.State == "closed" })
}

// prFollowupUnwatchedNote formats a localized explanation for a briefing row whose PR is not currently watched.
func prFollowupUnwatchedNote(l i18n.Lang, f repository.PRFollowup) string {
	host, ok := repository.RemoteHost(f.PRURL)
	if !ok || host == "" {
		return i18n.T(l, "assistant-briefing.pr_followup_unwatched")
	}
	if f.WatchReason == repository.PRFollowupReasonUnsupportedHost {
		return i18n.T(l, "assistant-briefing.pr_followup_unsupported_host", i18n.Args{"host": host})
	}
	return i18n.T(l, "assistant-briefing.pr_followup_host_not_connected", i18n.Args{"host": host})
}
