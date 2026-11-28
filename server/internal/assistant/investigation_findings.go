package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	maxInvestigationFindings   = 30
	findingDropQuestionPrefix  = "Drop investigation finding "
	findingOwnerDecisionPrefix = "owner-finding-drop:"
)

type investigationFinding struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Next    string `json:"next"`
	Source  string `json:"source"`
}

type findingDisposition struct {
	ID         string `json:"id"`
	FollowUp   string `json:"follow_up_todo_id,omitempty"`
	WaitTarget string `json:"wait_target,omitempty"`
	Done       string `json:"done_evidence,omitempty"`
	Drop       string `json:"owner_drop_decision_id,omitempty"`
}

type findingReview struct {
	Version      uint64               `json:"version"`
	Dispositions []findingDisposition `json:"dispositions,omitempty"`
}

type recordedFinding struct {
	investigationFinding
	Disposition findingDisposition `json:"disposition,omitempty"`
	ReportedAt  time.Time          `json:"reported_at"`
}

// A nil report is the ordinary single-scope flow. Version zero means the task was
// planned as an investigation but the worker has not declared its findings yet.
type investigationFindings struct {
	Version    uint64            `json:"version"`
	Confirmed  uint64            `json:"confirmed,omitempty"`
	Round      int               `json:"round"`
	Stale      bool              `json:"stale,omitempty"`
	ReportedAt time.Time         `json:"reported_at,omitzero"`
	Worker     SessionRef        `json:"worker,omitzero"`
	Items      []recordedFinding `json:"items"`
}

func (r *investigationFindings) clone() *investigationFindings {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Items = slices.Clone(r.Items)
	return &copy
}

func cleanInvestigationFindings(in []investigationFinding) ([]investigationFinding, error) {
	if len(in) > maxInvestigationFindings {
		return nil, fmt.Errorf("report at most %d investigation findings", maxInvestigationFindings)
	}
	out := make([]investigationFinding, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, f := range in {
		f.ID = strings.TrimSpace(f.ID)
		f.Summary = clip(oneLine(f.Summary), 200)
		f.Next = oneLine(f.Next)
		f.Source = clip(oneLine(f.Source), 600)
		if f.ID == "" || len(f.ID) > 48 || strings.ContainsFunc(f.ID, func(c rune) bool {
			return !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '-' || c == '_')
		}) {
			return nil, errors.New("a finding id must be 1–48 ASCII letters, digits, hyphens or underscores")
		}
		if seen[f.ID] || f.Summary == "" || f.Next == "" || clip(f.Next, 200) != f.Next || f.Source == "" {
			return nil, fmt.Errorf("finding %q needs a unique id, summary, next action up to 200 characters, and source", f.ID)
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	return out, nil
}

func (r *investigationFindings) current(t Todo, v *Supervision) bool {
	if r.Version == 0 || r.Stale || r.Round != v.Round || t.ReopenedAt != nil && r.ReportedAt.Before(*t.ReopenedAt) {
		return false
	}
	for _, a := range t.Agents {
		if a.active() && a.LinkedAt.After(r.ReportedAt) &&
			(a.MachineID != r.Worker.MachineID || a.SessionID != r.Worker.SessionID) {
			return false
		}
	}
	return true
}

// Successive reports add and update findings by id; omitting an earlier finding
// never removes it. A changed finding has to be disposed of again.
func (s *Service) recordInvestigationFindings(ctx context.Context, t Todo, worker SessionRef, in []investigationFinding, now time.Time) (uint64, error) {
	s.reviewDismissals.actions.acquire(t.ID)
	defer s.reviewDismissals.actions.release(t.ID)
	var version uint64
	var blocked error
	_, _, err := s.updateSupervisionGuardedAt(ctx, t.ID, now, func(cur Todo) bool {
		if cur.DoneAt != nil || s.activeTodoOf(worker.MachineID, worker.SessionID) != cur.ID {
			blocked = errors.New("the task is no longer open for this reporting Agent")
			return false
		}
		return true
	}, func(v *Supervision) bool {
		r := v.Investigation
		if r == nil {
			r = &investigationFindings{}
		}
		current, _ := s.todoNow(t.ID)
		if !r.ReportedAt.IsZero() && now.Before(r.ReportedAt) {
			blocked = errors.New("a newer investigation report is already recorded")
			return false
		}
		version = r.Version
		changed := !r.current(current, v) || r.Worker != worker
		for _, f := range in {
			i := slices.IndexFunc(r.Items, func(item recordedFinding) bool { return item.ID == f.ID })
			if i >= 0 && r.Items[i].investigationFinding == f {
				continue
			}
			next := recordedFinding{investigationFinding: f, ReportedAt: now.UTC()}
			if i >= 0 {
				r.Items[i] = next
			} else {
				r.Items = append(r.Items, next)
			}
			changed = true
		}
		if len(r.Items) > maxInvestigationFindings {
			blocked = fmt.Errorf("this report already holds %d investigation findings", maxInvestigationFindings)
			return false
		}
		if !changed {
			return false
		}
		version++
		r.Version, r.Round, r.ReportedAt, r.Worker, r.Stale = version, v.Round, now.UTC(), worker, false
		v.Investigation = r
		return true
	})
	if err != nil {
		return 0, err
	}
	return version, blocked
}

func (r *investigationFindings) review(t Todo, v *Supervision, in findingReview, allowStale bool) error {
	if r == nil || r.Version == 0 || r.Version != in.Version || (!r.current(t, v) && !allowStale) {
		return errors.New("the investigation findings changed or have not been reported for this round. Review the current version")
	}
	seen := make(map[string]bool, len(in.Dispositions))
	for _, d := range in.Dispositions {
		i := slices.IndexFunc(r.Items, func(f recordedFinding) bool { return f.ID == d.ID })
		if i < 0 || seen[d.ID] {
			return fmt.Errorf("unknown or repeated investigation finding %q", d.ID)
		}
		seen[d.ID] = true
		d.FollowUp, d.WaitTarget = strings.TrimSpace(d.FollowUp), clip(oneLine(d.WaitTarget), 200)
		d.Done, d.Drop = clip(oneLine(d.Done), 600), strings.TrimSpace(d.Drop)
		count := 0
		for _, value := range []string{d.FollowUp, d.WaitTarget, d.Done, d.Drop} {
			if value != "" {
				count++
			}
		}
		if count > 1 {
			return fmt.Errorf("give only one disposition for finding %q", d.ID)
		}
		r.Items[i].Disposition = d
	}
	if !r.current(t, v) {
		r.Stale = false
		r.Round = v.Round
		if t.ReopenedAt != nil && r.ReportedAt.Before(*t.ReopenedAt) {
			r.ReportedAt = *t.ReopenedAt
		}
		for _, a := range t.Agents {
			if a.active() && a.LinkedAt.After(r.ReportedAt) {
				r.ReportedAt = a.LinkedAt
			}
		}
	}
	r.Confirmed = in.Version
	return nil
}

func activeInvestigationAgents(t Todo) []TodoAgent {
	var out []TodoAgent
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			out = append(out, a)
		}
	}
	return out
}

func sameActiveAgents(a, b []TodoAgent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].MachineID != b[i].MachineID || a[i].SessionID != b[i].SessionID || !a[i].LinkedAt.Equal(b[i].LinkedAt) {
			return false
		}
	}
	return true
}

func (s *Service) todoAgentPositivelyStopped(ctx context.Context, a TodoAgent) bool {
	list, err := s.sessions(ctx, a.MachineID)
	if err != nil {
		return false
	}
	matched := false
	for _, c := range list {
		if !sessionNamesID(c, a.SessionID) {
			continue
		}
		matched = true
		if !(c.Archived || switchStopped(c)) {
			return false
		}
	}
	if matched {
		return true
	}
	return s.todoAgentConfirmedMissing(ctx, a)
}

func orphanCoversAgent(o TodoOrphan, a TodoAgent) bool {
	body, ok := strings.CutPrefix(o.key, "orphaned:")
	if !ok {
		return false
	}
	idsPart, _, ok := strings.Cut(body, "@")
	if !ok || idsPart == "" {
		return false
	}
	want := a.MachineID + "/" + a.SessionID
	for id := range strings.SplitSeq(idsPart, ",") {
		if id == want {
			return true
		}
	}
	return false
}

func (s *Service) todoAgentLiveListed(ctx context.Context, a TodoAgent) bool {
	return s.todoAgentLiveListedAt(ctx, a, false, TodoOrphan{}, time.Now())
}

func (s *Service) todoAgentLiveListedAt(ctx context.Context, a TodoAgent, hasOrphan bool, o TodoOrphan, now time.Time) bool {
	devices := s.devices(ctx)
	reachable := false
	for _, d := range devices {
		if d.ID == a.MachineID {
			reachable = d.Reachable
			break
		}
	}
	if !reachable {
		return true
	}
	connected := s.deviceConnectedAt(a.MachineID)
	if !connected.IsZero() && now.Sub(connected) < orphanReconnectWait {
		return true
	}
	if hasOrphan && !connected.IsZero() && (o.Since.IsZero() || connected.After(o.Since)) {
		return true
	}
	list, err := s.sessions(ctx, a.MachineID)
	if err != nil {
		return true
	}
	matched := false
	for _, c := range list {
		if !sessionNamesID(c, a.SessionID) {
			continue
		}
		matched = true
		if !(c.Archived || switchStopped(c)) &&
			agentGone(orphanAgent{machine: a.MachineID, session: a.SessionID, reachable: true, listed: true, c: c, connected: connected}, now) == "" {
			return true
		}
	}
	if matched {
		return false
	}
	if s.todoAgentConfirmedMissing(ctx, a) {
		return false
	}
	if hasOrphan && (o.Reason == orphanClosed || o.Reason == orphanError) && orphanCoversAgent(o, a) &&
		agentGone(orphanAgent{machine: a.MachineID, session: a.SessionID, reachable: true, listed: false, connected: connected}, now) == orphanClosed {
		return false
	}
	return true
}

// investigationWorkerGone reports whether open to-do t currently has no active Agent
// that can produce a fresh findings report. It returns the active-agent snapshot so
// callers can re-check under the todo write lock that no replacement Agent was linked.
// Live or unreachable Agents stay fail-closed.
func (s *Service) investigationWorkerGone(ctx context.Context, t Todo) ([]TodoAgent, bool) {
	active := activeInvestigationAgents(t)
	if len(active) == 0 {
		return active, true
	}
	allStopped := true
	for _, a := range active {
		if !s.todoAgentPositivelyStopped(ctx, a) {
			allStopped = false
			break
		}
	}
	if allStopped {
		return active, true
	}
	o, ok := s.orphanOf(t.ID)
	if !ok || (o.Reason != orphanClosed && o.Reason != orphanError) {
		return nil, false
	}
	now := time.Now()
	for _, a := range active {
		if !o.Since.IsZero() && a.LinkedAt.After(o.Since) {
			return nil, false
		}
		if !orphanCoversAgent(o, a) {
			return nil, false
		}
		if s.todoAgentLiveListedAt(ctx, a, true, o, now) {
			return nil, false
		}
	}
	return active, true
}

func (t Todo) hasStaleInvestigationReport() bool {
	if t.DoneAt != nil || t.Supervision == nil {
		return false
	}
	r := t.Supervision.Investigation
	return r != nil && r.Version > 0 && r.Stale
}

func (s *Service) checkStaleInvestigationCancelWorkers(ctx context.Context, t Todo) ([]TodoAgent, bool) {
	if t.DoneAt != nil || t.Supervision == nil || t.Supervision.Investigation == nil {
		return nil, false
	}
	if t.hasStaleInvestigationReport() || t.Supervision.Investigation.current(t, t.Supervision) {
		return nil, false
	}
	return s.investigationWorkerGone(ctx, t)
}

func staleInvestigationCancelAllowed(cur Todo, checked []TodoAgent, workersGone bool) bool {
	return cur.hasStaleInvestigationReport() ||
		(workersGone && cur.Supervision != nil && cur.Supervision.Investigation != nil &&
			!cur.Supervision.Investigation.current(cur, cur.Supervision) &&
			sameActiveAgents(activeInvestigationAgents(cur), checked))
}

func (s *Service) investigationActionCovered(parent Todo, f recordedFinding) bool {
	target, ok := s.todoNow(f.Disposition.FollowUp)
	if !ok || target.ID == parent.ID || target.isConcern() || target.Cancelled() ||
		target.StartError != "" || target.Supervision == nil || target.Supervision.Supervisor == "" ||
		!slices.Contains(target.Supervision.DoneWhen, f.Next) {
		return false
	}
	// After this parent would leave the handoff stranded during a park.
	if slices.Contains(target.After, parent.ID) && parent.DoneAt == nil {
		return false
	}
	if target.DoneAt == nil {
		return true
	}
	return findingActionMet(target.Supervision, f.Next)
}

func findingActionMet(v *Supervision, next string) bool {
	if v == nil {
		return false
	}
	return slices.ContainsFunc(v.Criteria, func(c db.TodoCriterion) bool {
		return c.Criterion == next && c.State == criterionMet && strings.TrimSpace(c.Evidence) != "" &&
			strings.HasPrefix(c.By, "supervisor:")
	})
}

func (s *Service) investigationDropRecorded(ctx context.Context, id string, f recordedFinding) bool {
	st := s.todoDB()
	if st == nil {
		return false
	}
	decisions, err := st.ListTodoDecisions(ctx, id, false)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(decisions, func(d db.TodoDecisionRow) bool {
		return d.ID == f.Disposition.Drop && d.Source == db.DecisionManual &&
			d.SourceMessage == findingOwnerDecisionPrefix+d.ID && d.SourceMachine == "" && d.SourceSession == "" &&
			d.Question == findingDropQuestionPrefix+f.ID+"?" && d.Choice == "Drop" &&
			strings.TrimSpace(d.Reason) != "" && !d.CreatedAt.Before(f.ReportedAt.Truncate(time.Microsecond))
	})
}

func (s *Service) investigationTransitionError(ctx context.Context, t Todo, v *Supervision, park *ParkEvidence) error {
	if v == nil || v.Investigation == nil {
		return nil
	}
	r := v.Investigation
	if !r.current(t, v) || r.Confirmed != r.Version {
		return i18n.ErrEnglish("assistant-tasks.investigation_review_required")
	}
	var missing []string
	waitUsed := false
	for _, f := range r.Items {
		d := f.Disposition
		switch {
		case d.Done != "" && findingActionMet(v, f.Next):
			continue
		case d.FollowUp != "" && s.investigationActionCovered(t, f):
			continue
		case d.Drop != "" && s.investigationDropRecorded(ctx, t.ID, f):
			continue
		case d.WaitTarget != "" && park != nil && park.NextActor == nextActorExternal && park.InteractionID == "" &&
			!waitUsed && d.WaitTarget == park.Target:
			waitUsed = true
		default:
			missing = append(missing, f.ID+" ("+f.Summary+")")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return i18n.ErrEnglish("assistant-tasks.investigation_findings_open", i18n.Args{"findings": strings.Join(missing, ", ")})
}

const reviewFindings = "findings"

func (s *Service) reviewInvestigationFindings(ctx context.Context, t Todo, in *findingReview, checks []ReviewCheck, reviewer string, allowed func(Todo) bool) (string, error) {
	var checkedAgents []TodoAgent
	allowStale := false
	if in != nil && !t.hasStaleInvestigationReport() && t.Supervision != nil && t.Supervision.Investigation != nil &&
		t.Supervision.Investigation.Version > 0 && !t.Supervision.Investigation.current(t, t.Supervision) {
		checkedAgents, allowStale = s.investigationWorkerGone(ctx, t)
	}
	var problem error
	var current Todo
	var report *investigationFindings
	_, _, err := s.updateSupervisionGuarded(ctx, t.ID, func(cur Todo) bool {
		current = cur
		if !allowed(cur) {
			problem = errors.New("only the current supervisor may review the findings")
			return false
		}
		return true
	}, func(v *Supervision) bool {
		if in == nil {
			report = v.Investigation
			return false
		}
		canReviewStale := current.hasStaleInvestigationReport() ||
			(allowStale && sameActiveAgents(activeInvestigationAgents(current), checkedAgents))
		if problem = v.Investigation.review(current, v, *in, canReviewStale); problem != nil {
			return false
		}
		for _, f := range v.Investigation.Items {
			if !slices.ContainsFunc(in.Dispositions, func(d findingDisposition) bool { return d.ID == f.ID }) {
				continue
			}
			if d := f.Disposition; d.FollowUp != "" {
				target, exists := s.todoNow(d.FollowUp)
				if !exists || target.DoneAt != nil || !s.investigationActionCovered(current, f) {
					problem = fmt.Errorf("finding %q needs an open independent to-do with the exact next action as a supervised criterion", f.ID)
					return false
				}
			}
			if d := f.Disposition; d.Done != "" {
				// The disposition itself is not evidence. Keep the concrete
				// check and reviewer in the ordinary supervision ledger.
				if slices.Contains(v.DoneWhen, f.Next) && slices.ContainsFunc(checks, func(c ReviewCheck) bool {
					return c.Criterion == f.Next && c.Met && clip(oneLine(c.Evidence), 600) == d.Done
				}) {
					v.applyChecks([]ReviewCheck{{Criterion: f.Next, Met: true, Evidence: d.Done}}, time.Now().UTC(), "supervisor:"+reviewer)
				}
				if !findingActionMet(v, f.Next) {
					problem = fmt.Errorf("finding %q needs an exact scoped next-action check with completion evidence", f.ID)
					return false
				}
			}
			if f.Disposition.Drop != "" && !s.investigationDropRecorded(ctx, t.ID, f) {
				problem = fmt.Errorf("finding %q needs a current explicit owner decision to drop it", f.ID)
				return false
			}
		}
		if current.DoneAt != nil {
			problem = s.investigationTransitionError(ctx, current, v, nil)
		}
		return problem == nil
	})
	if err != nil {
		return "", err
	}
	if problem != nil {
		return "", problem
	}
	if in == nil {
		if report == nil {
			return "This task has no investigation findings report.", nil
		}
		body, err := json.Marshal(report)
		return string(body), err
	}
	return fmt.Sprintf("Recorded investigation findings review version %d. The task state stays open for its next review.", in.Version), nil
}

// A handoff remains actionable after the parent is closed. Refuse to close its
// receiver without completion evidence so that cancellation cannot hide it.
func (s *Service) investigationReceivedFindings(id string) []recordedFinding {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var found []recordedFinding
	for _, t := range s.todos.todos {
		if t.Supervision != nil && t.Supervision.Investigation != nil {
			r := t.Supervision.Investigation
			for _, f := range r.Items {
				if f.Disposition.FollowUp == id {
					found = append(found, f)
				}
			}
		}
	}
	return found
}

func (s *Service) receivedFindingsCloseError(t Todo, checks []ReviewCheck) error {
	for _, f := range s.investigationReceivedFindings(t.ID) {
		if findingActionMet(t.Supervision, f.Next) || slices.ContainsFunc(checks, func(c ReviewCheck) bool {
			return c.Criterion == f.Next && c.Met && strings.TrimSpace(c.Evidence) != ""
		}) {
			continue
		}
		return i18n.ErrEnglish("assistant-tasks.investigation_handoff_open", i18n.Args{"finding": f.Summary})
	}
	return nil
}

func (s *Service) investigationParentDeleteError(t Todo) error {
	if t.Supervision == nil || t.Supervision.Investigation == nil {
		return nil
	}
	for _, f := range t.Supervision.Investigation.Items {
		if f.Disposition.FollowUp == "" {
			continue
		}
		target, ok := s.todoNow(f.Disposition.FollowUp)
		if ok && findingActionMet(target.Supervision, f.Next) {
			continue
		}
		return i18n.ErrEnglish("assistant-tasks.investigation_parent_handoff_open", i18n.Args{"finding": f.Summary})
	}
	return nil
}

func (s *Service) investigationCloseError(ctx context.Context, t Todo) error {
	if err := s.investigationTransitionError(ctx, t, t.Supervision, nil); err != nil {
		return err
	}
	return s.receivedFindingsCloseError(t, nil)
}

func isInvestigationGuardError(err error) bool {
	var message *i18n.Message
	return errors.As(err, &message) && strings.HasPrefix(message.Key, "assistant-tasks.investigation_")
}
