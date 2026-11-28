package assistant

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Parks that wait on a GitHub issue (#14050).
//
// A park whose evidence names a GitHub issue (an external event or someone else closing it) used to wait for its date
// even after the issue closed: only PR evidence was watched (externalParkPRURL). The minute PR pass now also reads
// such an issue, at most once per deployRecheck, and a closed one ends the park and wakes its supervisor with a review
// signal, as a terminal PR does. The observation stays on the park's review, so the same park is not woken twice.

// externalIssueObservation is the last read of the issue a park waits on.
type externalIssueObservation struct {
	URL       string    `json:"url"`
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checked_at"`
}

// parkIssueURL is the GitHub issue park r waits on, "" when it waits on none.
func parkIssueURL(r *TaskReview) string {
	if r == nil || r.Handoff != nil || r.Evidence == nil || r.WaitingOn != parkExternalEvent && r.WaitingOn != parkSomeoneElse {
		return ""
	}
	return githubIssueURL(r.Evidence.URL)
}

// githubIssueURL is raw as a canonical GitHub issue URL when raw itself is one ("" otherwise): an issue URL quoted
// inside another URL (a query parameter, a longer path) names no issue to watch. A query, a fragment or a trailing
// slash is dropped; a number too large to read is refused.
func githubIssueURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || !strings.EqualFold(u.Host, "github.com") && !strings.EqualFold(u.Host, "www.github.com") {
		return ""
	}
	source := "https://github.com" + strings.TrimSuffix(u.EscapedPath(), "/")
	m := ciIssueURL.FindStringSubmatch(source)
	if m == nil || m[0] != source {
		return ""
	}
	if _, err := strconv.Atoi(m[2]); err != nil {
		return ""
	}
	return source
}

// sweepParkIssues reads the issue each open parked task waits on and wakes the supervisor of one whose issue closed.
func (s *Service) sweepParkIssues(ctx context.Context, now time.Time) {
	f, ok := s.forge().(issueStateForge)
	if !ok || s.ensureTodosLoaded(ctx) != nil {
		return
	}
	s.todos.mu.RLock()
	var tasks []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && !isAskTodo(t.ID) && parkIssueURL(t.Supervision.parking()) != "" {
			tasks = append(tasks, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		held := t.Supervision.parking()
		url := parkIssueURL(held)
		if o := held.ExternalIssue; o != nil && o.URL == url && now.Sub(o.CheckedAt) < deployRecheck || !s.issueReadDue(t.ID, now) {
			continue
		}
		m := ciIssueURL.FindStringSubmatch(url)
		number, _ := strconv.Atoi(m[2])
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		state, err := f.IssueState(cctx, m[1], number)
		cancel()
		if err != nil {
			s.logf("task %s: parked issue %s: %v", t.ID, url, err)
			continue
		}
		s.applyParkIssueState(ctx, t.ID, held, url, state, now)
	}
}

// issueReadDue says whether task id's parked issue is due to be read again at now, and records the read if so.
func (s *Service) issueReadDue(id string, now time.Time) bool {
	key := id + "|park-issue"
	p := &s.deploy
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.seen[key]; ok && now.Sub(last.statusAt) < deployRecheck {
		return false
	}
	if p.seen == nil || len(p.seen) > 2000 {
		p.seen = map[string]prSeen{}
	}
	p.seen[key] = prSeen{statusAt: now}
	return true
}

// applyParkIssueState keeps issue url's state on park held of task id, and ends the park once it is closed.
func (s *Service) applyParkIssueState(ctx context.Context, id string, held *TaskReview, url, state string, now time.Time) {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	v, changed, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		p := v.parking()
		cur, ok := s.todoNow(id)
		if !ok || !taskOpen(cur) || p == nil || !p.At.Equal(held.At) || parkIssueURL(p) != url {
			return false
		}
		p.ExternalIssue = &externalIssueObservation{URL: url, State: state, CheckedAt: now.UTC()}
		if state == "closed" {
			v.State = reviewPending
		}
		return true
	})
	if err != nil {
		s.logf("task %s: parked issue %s: %v", id, url, err)
		return
	}
	if changed && v.State == reviewPending {
		s.signal(superviseSignal{Todo: id, Kind: sigReview, Key: reviewKey(&v),
			Detail: "The issue this park waits on is closed: " + url + ". Reassess the task against its criteria."})
	}
}
