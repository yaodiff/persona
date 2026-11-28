package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	prEventTerminal         = "terminal"
	prEventTerminalOrReview = "terminal_or_review"
	externalPRReviewPrefix  = "external-pr@"
)

func externalPREvent(event string) bool {
	return event == prEventTerminal || event == prEventTerminalOrReview
}

func githubParkPRURL(raw string) string {
	source := triageNormalizePRURL(raw)
	m := ciPRURL.FindStringSubmatch(source)
	if m == nil || m[0] != source {
		return ""
	}
	if _, err := strconv.Atoi(m[2]); err != nil {
		return ""
	}
	return source
}

// A PR may be the reference for a different external dependency. Only an explicit PR event enrolls the link.
func externalParkPRURL(r *TaskReview) string {
	if r == nil || r.WaitingOn != parkExternalEvent || r.Evidence == nil ||
		r.Evidence.Kind != "external_event" || !externalPREvent(r.Evidence.PREvent) {
		return ""
	}
	return githubParkPRURL(r.Evidence.URL)
}

// externalPRObservation travels with the park even when the task has no stage PR.
type externalPRObservation struct {
	URL          string            `json:"url"`
	State        string            `json:"state"`
	CheckedAt    time.Time         `json:"checked_at"`
	HeadOID      string            `json:"head_oid,omitempty"`
	Conflicts    bool              `json:"conflicts,omitempty"`
	Review       prSubmittedReview `json:"review,omitzero"`
	PendingSince time.Time         `json:"pending_since,omitzero"`
}

func (o externalPRObservation) terminalWakes() bool {
	return o.State == "merged" || o.State == "closed"
}

func (o externalPRObservation) conflictWakes() bool {
	return o.State == "open" && o.Conflicts && o.HeadOID != ""
}

func (o externalPRObservation) reviewWakes(r *TaskReview) bool {
	return o.State == "open" && r != nil && r.Evidence != nil && r.Evidence.PREvent == prEventTerminalOrReview &&
		o.HeadOID != "" && o.Review.ID > 0 && o.Review.SubmittedAt.After(r.At) && !o.Review.SubmittedAt.After(o.CheckedAt)
}

func (o externalPRObservation) wakes(r *TaskReview) bool {
	return o.terminalWakes() || o.conflictWakes() || o.reviewWakes(r)
}

func (o externalPRObservation) wakesWithoutRouted(r *TaskReview, conflictRouted, reviewRouted bool) bool {
	return o.terminalWakes() || (!conflictRouted && o.conflictWakes()) || (!reviewRouted && o.reviewWakes(r))
}

func externalParkAcceptsStatus(cur Todo, held *TaskReview, status TodoPRStatus) bool {
	source := externalParkPRURL(held)
	if !taskOpen(cur) || source == "" || source != triageNormalizePRURL(status.URL) || held.At.IsZero() ||
		status.CheckedAt.IsZero() || status.CheckedAt.Before(held.At) ||
		status.State != "open" && status.State != "merged" && status.State != "closed" {
		return false
	}
	p := cur.Supervision.parking()
	if p == nil || !p.At.Equal(held.At) || externalParkPRURL(p) != source ||
		p.Evidence.PREvent != held.Evidence.PREvent ||
		p.ExternalPR != nil && !status.CheckedAt.After(p.ExternalPR.CheckedAt) {
		return false
	}
	if f := prFacts(cur, status.URL); f != nil && status.CheckedAt.Before(f.CheckedAt) {
		return false
	}
	return true
}

func (s *Service) applyExternalParkPRStatus(ctx context.Context, t Todo, status TodoPRStatus, conflictRouted, reviewRouted bool) (bool, error) {
	held := t.Supervision.parking()
	source := externalParkPRURL(held)
	if source == "" || source != triageNormalizePRURL(status.URL) || held.At.IsZero() ||
		status.CheckedAt.IsZero() || status.CheckedAt.Before(held.At) ||
		status.State != "open" && status.State != "merged" && status.State != "closed" {
		return false, nil
	}
	observation := externalPRObservation{URL: source, State: status.State, CheckedAt: status.CheckedAt,
		HeadOID: status.HeadOID, Conflicts: status.Conflicts, Review: status.submittedReview}
	hint := ""
	if status.State == "closed" { // closed unmerged: name the linked PRs that may continue it (#14049)
		hint = s.closedPRSuccessors(ctx, t, status.URL)
	}
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		p := v.parking()
		cur, ok := s.todoNow(t.ID)
		if !ok || !taskOpen(cur) || p == nil || !p.At.Equal(held.At) || externalParkPRURL(p) != source ||
			p.Evidence.PREvent != held.Evidence.PREvent ||
			p.ExternalPR != nil && !status.CheckedAt.After(p.ExternalPR.CheckedAt) {
			return false
		}
		if f := prFacts(cur, status.URL); f != nil && status.CheckedAt.Before(f.CheckedAt) {
			return false
		}
		p.ExternalPR = &observation
		if observation.wakesWithoutRouted(p, conflictRouted, reviewRouted) {
			v.State = reviewPending
		}
		return true
	})
	if err != nil {
		return false, err
	}
	if changed && v.State == reviewPending {
		s.signal(superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v), Detail: hint})
		return true, nil
	}
	return false, nil
}

func (v *Supervision) externalPRWake() *TaskReview {
	if v == nil || v.State != reviewPending || v.PendingSince == nil || len(v.Reviews) == 0 {
		return nil
	}
	r := &v.Reviews[len(v.Reviews)-1]
	if r.ExternalPR != nil && r.ExternalPR.PendingSince.Equal(v.PendingSince.Truncate(time.Microsecond)) {
		return r
	}
	return nil
}

func (v *Supervision) externalPRConflictWoke(prURL, headOID string) bool {
	r := v.externalPRWake()
	if r == nil || r.ExternalPR == nil || !r.ExternalPR.conflictWakes() || headOID == "" {
		return false
	}
	return r.ExternalPR.HeadOID == headOID && normalizePRKey(r.ExternalPR.URL) == normalizePRKey(prURL)
}

func (o externalPRObservation) detail(r *TaskReview) string {
	if o.State == "open" {
		hasReview := o.reviewWakes(r)
		hasConflict := o.conflictWakes()
		switch {
		case hasConflict && hasReview:
			return fmt.Sprintf("The bound PR has merge conflicts at head %s and a new review (%s#pullrequestreview-%d). Reassess the task against its criteria.", o.HeadOID, o.URL, o.Review.ID)
		case hasConflict:
			return fmt.Sprintf("The bound PR has merge conflicts at head %s: %s. Reassess the task against its criteria.", o.HeadOID, o.URL)
		default:
			return fmt.Sprintf("A new review was submitted on the bound PR: %s#pullrequestreview-%d. Reassess the task against its criteria.", o.URL, o.Review.ID)
		}
	}
	return fmt.Sprintf("The bound PR is %s: %s. Reassess the task against its criteria.", o.State, o.URL)
}

func (g superviseSignal) externalPRReview() bool {
	return g.Kind == sigReview && strings.HasPrefix(g.Key, externalPRReviewPrefix)
}
