package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	prFeedbackCursorsKey    = "assistant.pr_feedback_cursors"
	prFeedbackReviews       = "reviews"
	prFeedbackIssueComments = "issue_comments"
	prFeedbackQueuePrefix   = "assistant.pr_feedback_queue/"

	prFeedbackBatchWindow     = time.Minute
	prSuppressedFallbackDelay = 6 * time.Hour
	prSuppressedPreviewLimit  = 500
)

// A cursor remembers every handled ID at its newest timestamp. Feedback from that same second
// can appear on a later poll, regardless of its ID's order.
type prFeedbackCursor struct {
	At    time.Time `json:"at"`
	IDsAt []string  `json:"ids_at,omitempty"`
}

type prFeedbackCursors map[string]map[string]prFeedbackCursor // (todo, normalized PR URL) -> stream -> cursor

type prFeedbackItem struct {
	githubPRFeedbackPost
	at       time.Time
	commitID string // reviews refer to a particular head
}

type prFeedbackBook struct {
	locks   cardLocks // one routing pass per task and PR, without holding a lock across other PRs' sends
	writeMu sync.Mutex

	timerMu sync.Mutex
	timers  map[string]*prFeedbackTimer
}

type prFeedbackTimer struct {
	at    time.Time
	timer *time.Timer
}

// The queue is stored per task and PR beside the legacy cursors. Keep IDs across heads, even after
// removing the body previews, so an edit or a retry cannot admit the same event again.
type prFeedbackQueue struct {
	HeadSHA    string              `json:"head_sha,omitempty"`
	Seen       map[string]struct{} `json:"seen,omitempty"`
	BatchAt    *time.Time          `json:"batch_at,omitempty"`
	Actionable []prFeedbackRecord  `json:"actionable,omitempty"`
	Suppressed []prFeedbackRecord  `json:"suppressed,omitempty"`
	Delivering *prFeedbackDelivery `json:"delivering,omitempty"`
}

type prFeedbackRecord struct {
	ID         string    `json:"id"`
	Author     string    `json:"author"`
	URL        string    `json:"url"`
	Preview    string    `json:"preview"`
	ObservedAt time.Time `json:"observed_at"`
}

// A sealed delivery keeps its membership and receipt ID through retries.
type prFeedbackDelivery struct {
	Records  []prFeedbackRecord `json:"records"`
	HeadSHA  string             `json:"head_sha"`
	Fallback bool               `json:"fallback,omitempty"`
}

type prFeedbackCandidate struct {
	record     prFeedbackRecord
	postedAt   time.Time
	immediate  bool
	suppressed bool
}

func (r prFeedbackRecord) isIssueComment() bool {
	return strings.HasPrefix(r.ID, "issue_comment:")
}

func (q *prFeedbackQueue) setHead(head string) bool {
	if q.HeadSHA == head {
		return false
	}
	q.HeadSHA = head
	q.Suppressed = nil
	// Undelivered discussion follows a PR across commits. Reviews refer to the old diff.
	q.Actionable = slices.DeleteFunc(q.Actionable, func(r prFeedbackRecord) bool {
		return head == "" || !r.isIssueComment()
	})
	if len(q.Actionable) == 0 {
		q.BatchAt = nil
	}
	if d := q.Delivering; d != nil && (head == "" || d.Fallback || !slices.ContainsFunc(d.Records, prFeedbackRecord.isIssueComment)) {
		q.Delivering = nil
	}
	return true
}

func (q *prFeedbackQueue) add(c prFeedbackCandidate, now time.Time) {
	if c.suppressed {
		q.Suppressed = append(q.Suppressed, c.record)
		return
	}
	q.Actionable = append(q.Actionable, c.record)
	posted := c.postedAt
	if posted.After(now) {
		posted = now
	}
	at := posted.Add(prFeedbackBatchWindow)
	if c.immediate {
		at = now
	}
	if q.BatchAt == nil || at.Before(*q.BatchAt) {
		q.BatchAt = &at
	}
}

func (q prFeedbackQueue) fallbackAt() time.Time {
	if len(q.Suppressed) == 0 {
		return time.Time{}
	}
	return q.Suppressed[0].ObservedAt.Add(prSuppressedFallbackDelay)
}

func (q prFeedbackQueue) nextWake() time.Time {
	at := q.fallbackAt()
	if q.BatchAt != nil && (at.IsZero() || q.BatchAt.Before(at)) {
		return *q.BatchAt
	}
	return at
}

func (q *prFeedbackQueue) sealDue(now time.Time) {
	if q.Delivering != nil {
		return
	}
	if q.BatchAt != nil && !q.BatchAt.After(now) {
		q.Delivering = &prFeedbackDelivery{Records: q.Actionable, HeadSHA: q.HeadSHA}
		q.Actionable, q.BatchAt = nil, nil
	} else if at := q.fallbackAt(); !at.IsZero() && !at.After(now) {
		q.Delivering = &prFeedbackDelivery{Records: q.Suppressed, HeadSHA: q.HeadSHA, Fallback: true}
		q.Suppressed = nil
	}
}

func readPRFeedbackQueue(ctx context.Context, st Store, key string) (prFeedbackQueue, error) {
	var q prFeedbackQueue
	_, err := st.Setting(ctx, prFeedbackQueuePrefix+key, &q)
	return q, err
}

func savePRFeedbackQueue(ctx context.Context, st Store, key string, q prFeedbackQueue) error {
	if err := st.PutSetting(ctx, prFeedbackQueuePrefix+key, q); err != nil {
		return fmt.Errorf("save PR feedback queue: %w", err)
	}
	return nil
}

func prFeedbackKey(todoID, url string) string {
	raw, _ := json.Marshal([2]string{todoID, triageNormalizePRURL(url)})
	return string(raw)
}

func (c prFeedbackCursor) handled(at time.Time, id string) bool {
	return at.Before(c.At) || at.Equal(c.At) && slices.Contains(c.IDsAt, id)
}

func (c *prFeedbackCursor) advance(at time.Time, id string) {
	if at.After(c.At) {
		c.At, c.IDsAt = at.UTC(), nil
	}
	c.IDsAt = append(c.IDsAt, id)
}

func (s *Service) savePRFeedbackCursor(ctx context.Context, st Store, key, stream string, cursor prFeedbackCursor) error {
	s.prFeedback.writeMu.Lock()
	defer s.prFeedback.writeMu.Unlock()
	var cursors prFeedbackCursors
	if _, err := st.Setting(ctx, prFeedbackCursorsKey, &cursors); err != nil {
		return fmt.Errorf("read feedback cursors: %w", err)
	}
	if cursors == nil {
		cursors = make(prFeedbackCursors)
	}
	if cursors[key] == nil {
		cursors[key] = make(map[string]prFeedbackCursor)
	}
	cursors[key][stream] = cursor
	if err := st.PutSetting(ctx, prFeedbackCursorsKey, cursors); err != nil {
		return fmt.Errorf("save feedback cursor: %w", err)
	}
	return nil
}

func prReviewEventID(id int64) string { return "review:" + strconv.FormatInt(id, 10) }

func prFeedbackEventID(stream string, id int64) string {
	if stream == prFeedbackIssueComments {
		return "issue_comment:" + strconv.FormatInt(id, 10)
	}
	return prReviewEventID(id)
}

func pendingPRFeedback(stream string, items []prFeedbackItem, cursor prFeedbackCursor) []prFeedbackItem {
	var pending []prFeedbackItem
	seen := make(map[int64]bool)
	for _, item := range items {
		if item.ID > 0 && !item.at.IsZero() && !seen[item.ID] && !cursor.handled(item.at, prFeedbackEventID(stream, item.ID)) {
			seen[item.ID] = true
			pending = append(pending, item)
		}
	}
	slices.SortFunc(pending, func(a, b prFeedbackItem) int {
		if order := a.at.Compare(b.at); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return pending
}

func (s *Service) prFeedbackTarget(todoID, url, head string, checkedAt time.Time) (Todo, bool) {
	t, ok := s.todoNow(todoID)
	if !ok {
		return t, false
	}
	p := s.prFeedbackBinding(t, url)
	return t, p != nil && p.HeadOID == head && p.CheckedAt.Equal(checkedAt)
}

func (s *Service) prFeedbackBinding(t Todo, url string) *TodoPRStatus {
	if !taskOpen(t) || t.PRURL != url || s.stagePR(t.ID) != url {
		return nil
	}
	p := t.PRStatus
	if p == nil || p.URL != url || p.State != "open" || p.HeadOID == "" {
		return nil
	}
	return p
}

func (s *Service) routePRFeedback(ctx context.Context, todoID string, status TodoPRStatus, g githubForge, feedback prFeedback) error {
	return s.routePRFeedbackAt(ctx, todoID, status, g, feedback, status.CheckedAt)
}

func (s *Service) routePRFeedbackAt(ctx context.Context, todoID string, status TodoPRStatus, g githubForge, feedback prFeedback, now time.Time) error {
	key := prFeedbackKey(todoID, status.URL)
	s.prFeedback.locks.acquire(key)
	defer s.prFeedback.locks.release(key)
	if ctx.Err() != nil || !s.prEventsRouteToAgentOn(ctx) {
		return ctx.Err()
	}
	if _, ok := s.prFeedbackTarget(todoID, status.URL, status.HeadOID, status.CheckedAt); !ok {
		return nil
	}
	st := s.store()
	if st == nil {
		return errors.New("feedback needs persistent storage")
	}
	var saved prFeedbackCursors
	if _, err := st.Setting(ctx, prFeedbackCursorsKey, &saved); err != nil {
		return fmt.Errorf("read feedback cursors: %w", err)
	}
	q, err := readPRFeedbackQueue(ctx, st, key)
	if err != nil {
		return fmt.Errorf("read PR feedback queue: %w", err)
	}
	dirty := q.setHead(status.HeadOID)
	type streamUpdate struct {
		stream     string
		cursor     prFeedbackCursor
		ids        []string
		candidates []prFeedbackCandidate
	}
	var updates []streamUpdate
	observe := func(stream string, items []prFeedbackItem) error {
		cursor, initialized := saved[key][stream]
		pending := pendingPRFeedback(stream, items, cursor)
		if initialized && len(pending) == 0 {
			return nil
		}
		update := streamUpdate{stream: stream, cursor: cursor}
		var fresh []prFeedbackItem
		for _, item := range pending {
			id := prFeedbackEventID(stream, item.ID)
			update.cursor.advance(item.at, id)
			if _, seen := q.Seen[id]; !seen {
				update.ids = append(update.ids, id)
				fresh = append(fresh, item)
			}
		}
		if initialized {
			var err error
			update.candidates, err = feedback.candidates(ctx, status, g, stream, fresh, now)
			if err != nil {
				return err // never consume a partially read stream
			}
		}
		updates = append(updates, update)
		return nil
	}

	reviewErr := observe(prFeedbackReviews, feedback.reviews)
	comments, commentErr := readPRPages[githubPRIssueComment](ctx, g, feedback.commentsPath)
	if commentErr == nil {
		items := make([]prFeedbackItem, 0, len(comments))
		for _, row := range comments {
			// An edit does not create a new discussion event.
			items = append(items, prFeedbackItem{githubPRFeedbackPost: row.githubPRFeedbackPost, at: row.CreatedAt})
		}
		commentErr = observe(prFeedbackIssueComments, items)
	}

	inputErr := errors.Join(reviewErr, commentErr)
	due := q.nextWake()
	if len(updates) > 0 || q.Delivering != nil || !due.IsZero() && !due.After(now) {
		if err := g.checkPRFeedbackSource(ctx, feedback.pullPath, status.HeadOID); err != nil {
			return errors.Join(inputErr, err)
		}
	}
	if _, ok := s.prFeedbackTarget(todoID, status.URL, status.HeadOID, status.CheckedAt); !ok {
		return inputErr
	}
	for _, u := range updates {
		if len(u.ids) == 0 {
			continue
		}
		if q.Seen == nil {
			q.Seen = make(map[string]struct{})
		}
		for _, id := range u.ids {
			q.Seen[id] = struct{}{}
		}
		for _, c := range u.candidates {
			q.add(c, now)
		}
		dirty = true
	}
	// Save the receipt IDs and queued items together before advancing either stream. A retry
	// after a cursor write failure can complete the cursor without copying events into a new batch.
	for sent := false; ; sent = true {
		if q.Delivering == nil {
			q.sealDue(now)
			dirty = dirty || q.Delivering != nil
		}
		if dirty {
			if err := savePRFeedbackQueue(ctx, st, key, q); err != nil {
				return errors.Join(inputErr, err)
			}
		}
		if q.Delivering == nil {
			break
		}
		t, ok := s.prFeedbackTarget(todoID, status.URL, status.HeadOID, status.CheckedAt)
		if !ok {
			return inputErr
		}
		if sent {
			if err := g.checkPRFeedbackSource(ctx, feedback.pullPath, status.HeadOID); err != nil {
				return errors.Join(inputErr, err)
			}
		}
		ev := q.Delivering.event(todoID, status)
		if err := s.deliverPRFeedback(ctx, t, ev); err != nil {
			return errors.Join(inputErr, err)
		}
		q.Delivering, dirty = nil, true
	}
	s.schedulePRFeedback(key, todoID, status, g, q.nextWake())
	for _, u := range updates {
		if err := s.savePRFeedbackCursor(ctx, st, key, u.stream, u.cursor); err != nil {
			inputErr = errors.Join(inputErr, err)
		}
	}
	return inputErr
}

func (f *prFeedback) candidates(ctx context.Context, status TodoPRStatus, g githubForge, stream string, items []prFeedbackItem, now time.Time) ([]prFeedbackCandidate, error) {
	var candidates []prFeedbackCandidate
	reviews := stream == prFeedbackReviews
	for _, item := range items {
		author := strings.TrimSpace(item.User.Login)
		if reviews && item.commitID != status.HeadOID || author == "" || strings.EqualFold(author, f.author) ||
			isBotReviewAuthor(author, item.User.Type) || !reviewAuthorHasWriteAssociation(item.AuthorAssociation) {
			continue
		}
		body := strings.TrimSpace(item.Body)
		if prFeedbackNoWake.MatchString(body) {
			continue
		}
		if !f.viewerChecked {
			f.viewer = g.prViewer(ctx)
			f.viewerChecked = true
		}
		if strings.EqualFold(author, f.viewer) {
			continue
		}
		anchor := "#issuecomment-"
		substantive, failure := !commentedReviewLooksInformational(body), prFeedbackReportsFailure(body)
		if reviews {
			inline, err := g.prReviewComments(ctx, f.pullPath, item.ID)
			if err != nil {
				return nil, err
			}
			body, substantive, failure = combinedPRReview(body, inline)
			anchor = "#pullrequestreview-"
		}
		if body == "" {
			continue
		}
		limit := prEventBodyLimit
		if !substantive {
			limit = prSuppressedPreviewLimit
		}
		candidates = append(candidates, prFeedbackCandidate{
			record: prFeedbackRecord{
				ID: prFeedbackEventID(stream, item.ID), Author: author, ObservedAt: now.UTC(),
				URL:     cmp.Or(strings.TrimSpace(item.HTMLURL), triageNormalizePRURL(status.URL)+anchor+strconv.FormatInt(item.ID, 10)),
				Preview: clip(strings.ReplaceAll(body, "\x00", ""), limit),
			},
			postedAt:   item.at,
			immediate:  failure,
			suppressed: !substantive,
		})
	}
	return candidates, nil
}

func (s *Service) discardStalePRFeedback(ctx context.Context, todoID string, previous *TodoPRStatus, status TodoPRStatus) {
	if !ciPRURL.MatchString(status.URL) {
		return
	}
	head := status.HeadOID
	switch status.State {
	case "open":
		if previous == nil || previous.State == "open" && previous.HeadOID == head {
			return
		}
	case "closed", "merged":
		head = ""
	default:
		return
	}
	key := prFeedbackKey(todoID, status.URL)
	s.prFeedback.locks.acquire(key)
	defer s.prFeedback.locks.release(key)
	t, ok := s.todoNow(todoID)
	p := prFacts(t, status.URL)
	if !ok || p == nil || p.State != status.State || p.HeadOID != status.HeadOID || !p.CheckedAt.Equal(status.CheckedAt) {
		return
	}
	st := s.store()
	if st == nil {
		return
	}
	q, err := readPRFeedbackQueue(ctx, st, key)
	if err == nil && q.setHead(head) {
		s.prFeedback.setTimer(key, time.Time{}, nil)
		err = savePRFeedbackQueue(ctx, st, key, q)
	}
	if err != nil {
		s.logf("task %s: discard stale PR feedback: %v", todoID, err)
	}
}

func (b *prFeedbackBook) setTimer(key string, at time.Time, fire func()) {
	b.timerMu.Lock()
	defer b.timerMu.Unlock()
	if old := b.timers[key]; old != nil {
		if old.at.Equal(at) {
			return
		}
		old.timer.Stop()
		delete(b.timers, key)
	}
	if at.IsZero() {
		return
	}
	if b.timers == nil {
		b.timers = make(map[string]*prFeedbackTimer)
	}
	alarm := &prFeedbackTimer{at: at}
	alarm.timer = time.AfterFunc(time.Until(at), func() {
		b.timerMu.Lock()
		current := b.timers[key] == alarm
		if current {
			delete(b.timers, key)
		}
		b.timerMu.Unlock()
		if current {
			fire()
		}
	})
	b.timers[key] = alarm
}

// A deadline needs its own read: the ordinary PR status sweep can be farther away than the batch window.
func (s *Service) schedulePRFeedback(key, todoID string, status TodoPRStatus, g githubForge, at time.Time) {
	if s.running.Load() == nil || s.runCtx().Err() != nil {
		return
	}
	run := s.runCtx()
	s.prFeedback.setTimer(key, at, func() {
		ctx, cancel := context.WithTimeout(run, prStatusPassMost)
		defer cancel()
		if err := s.refreshPRFeedback(ctx, todoID, status.URL, status.HeadOID, g, time.Now().UTC()); err != nil {
			s.logf("task %s: refresh PR feedback: %v", todoID, err)
		}
	})
}

func (s *Service) refreshPRFeedback(ctx context.Context, todoID, url, head string, g githubForge, now time.Time) error {
	if ctx.Err() != nil || !s.prEventsRouteToAgentOn(ctx) {
		return ctx.Err()
	}
	t, ok := s.todoNow(todoID)
	if !ok {
		return nil
	}
	status := s.prFeedbackBinding(t, url)
	match := ciPRURL.FindStringSubmatch(url)
	if status == nil || status.HeadOID != head || match == nil {
		return nil
	}
	number, _ := strconv.Atoi(match[2])
	var feedback prFeedback
	fresh, err := g.readPRStatus(ctx, match[1], number, &feedback)
	if err != nil {
		return err
	}
	if fresh.State != "open" || fresh.HeadOID != head {
		fresh.URL, fresh.CheckedAt = url, now
		if err := s.applyPRStatus(ctx, t, fresh); err != nil {
			return err
		}
		if fresh.State != "open" {
			return nil
		}
		status = &fresh
	}
	return s.routePRFeedbackAt(ctx, todoID, *status, g, feedback, now)
}

func combinedPRReview(body string, inline []githubPRInlineComment) (preview string, substantive, failure bool) {
	var parts []string
	if body = strings.TrimSpace(body); body != "" {
		parts = append(parts, body)
	}
	substantive, failure = !commentedReviewLooksInformational(body), prFeedbackReportsFailure(body)
	seen := make(map[int64]bool)
	for _, c := range inline {
		body := strings.TrimSpace(c.Body)
		if body == "" || prFeedbackNoWake.MatchString(body) || c.ID > 0 && seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		substantive = substantive || !commentedReviewLooksInformational(body)
		failure = failure || prFeedbackReportsFailure(body)
		location := oneLine(c.Path)
		if line := cmp.Or(c.Line, c.OriginalLine); location != "" && line > 0 {
			location += ":" + strconv.Itoa(line)
		}
		if location != "" {
			body = location + ": " + body
		}
		parts = append(parts, body)
	}
	if body != "" && len(parts) > 1 {
		// Leave room for inline findings in the bounded notification preview.
		parts[0] = clip(parts[0], prEventBodyLimit/2)
	}
	return strings.Join(parts, "\n\n"), substantive, failure
}
