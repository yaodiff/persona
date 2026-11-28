package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

type todoDecisionLister interface {
	ListTodoDecisions(ctx context.Context, todoID string, all bool) ([]db.TodoDecisionRow, error)
}

type todoCausalTraceEvent struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	Category string    `json:"category"`
	Kind     string    `json:"kind"`
	Summary  string    `json:"summary"`
	Ref      string    `json:"ref,omitempty"`
	URL      string    `json:"url,omitempty"`
}

type todoCausalTraceResponse struct {
	TodoID string                 `json:"todo_id"`
	Title  string                 `json:"title"`
	Events []todoCausalTraceEvent `json:"events"`
	Count  int                    `json:"count"`
}

// todoCausalTraceRoutes registers the read-only causal audit trace endpoint strictly gated under todos_v2=true (#16433).
func (s *Service) todoCausalTraceRoutes() {
	s.handle("GET /api/v1/todos/{id}/trace", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !s.settings(ctx).todosV2On() {
			apierr.Write(w, http.StatusConflict, errors.New("todo causal trace requires todos_v2 to be enabled"))
			return
		}
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		todo, ok := s.todoNow(id)
		if !ok {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		if r.Header.Get(RoleHeader) == roleExtension && s.todosHidden(ctx, []string{id}, nil)[id] {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		resp := s.buildTodoCausalTrace(ctx, todo)
		apierr.WriteJSON(w, resp)
	})
}

func (s *Service) buildTodoCausalTrace(ctx context.Context, todo Todo) todoCausalTraceResponse {
	events := make([]todoCausalTraceEvent, 0, 16)
	events = appendTodoLifecycleTraceEvents(events, todo)
	events = s.appendTodoQueueTraceEvents(ctx, events, todo)
	events = s.appendTodoBlockerTraceEvents(ctx, events, todo.ID)
	events = s.appendTodoRecoveryTraceEvents(ctx, events, todo.ID)
	events = s.appendTodoSupervisionTraceEvents(ctx, events, todo)
	events = appendTodoPRTraceEvents(events, todo)

	slices.SortFunc(events, func(a, b todoCausalTraceEvent) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.ID, b.ID))
	})
	return todoCausalTraceResponse{
		TodoID: todo.ID,
		Title:  todo.Title,
		Events: events,
		Count:  len(events),
	}
}

func appendTodoLifecycleTraceEvents(events []todoCausalTraceEvent, todo Todo) []todoCausalTraceEvent {
	if !todo.CreatedAt.IsZero() {
		events = append(events, todoCausalTraceEvent{
			ID:       "lifecycle:created",
			At:       todo.CreatedAt.UTC(),
			Category: "lifecycle",
			Kind:     "created",
			Summary:  "To-do created",
		})
	}
	if todo.ReopenedAt != nil && !todo.ReopenedAt.IsZero() {
		events = append(events, todoCausalTraceEvent{
			ID:       "lifecycle:reopened",
			At:       todo.ReopenedAt.UTC(),
			Category: "lifecycle",
			Kind:     "reopened",
			Summary:  "To-do reopened",
		})
	}
	if todo.SeenAt != nil && !todo.SeenAt.IsZero() {
		events = append(events, todoCausalTraceEvent{
			ID:       "lifecycle:seen",
			At:       todo.SeenAt.UTC(),
			Category: "lifecycle",
			Kind:     "seen",
			Summary:  "Marked as seen",
		})
	}
	if errMsg := strings.TrimSpace(todo.StartError); errMsg != "" {
		at := todo.UpdatedAt
		if todo.DoneAt != nil && !todo.DoneAt.IsZero() {
			at = *todo.DoneAt
		} else if at.IsZero() {
			at = todo.CreatedAt
		}
		if !at.IsZero() {
			events = append(events, todoCausalTraceEvent{
				ID:       "lifecycle:start_error",
				At:       at.UTC(),
				Category: "lifecycle",
				Kind:     "start_error",
				Summary:  "Start error: " + errMsg,
			})
		}
	}
	if todo.CancelledAt != nil && !todo.CancelledAt.IsZero() {
		summary := "To-do cancelled"
		if reason := strings.TrimSpace(todo.CancelledReason); reason != "" {
			summary = "Cancelled: " + reason
		}
		events = append(events, todoCausalTraceEvent{
			ID:       "lifecycle:cancelled",
			At:       todo.CancelledAt.UTC(),
			Category: "lifecycle",
			Kind:     "cancelled",
			Summary:  summary,
		})
	}
	if todo.DoneAt != nil && !todo.DoneAt.IsZero() && todo.CancelledAt == nil && strings.TrimSpace(todo.DoneBy) != "cancelled" {
		doneBy := strings.TrimSpace(todo.DoneBy)
		doneReason := strings.TrimSpace(todo.DoneReason)
		summary := "To-do completed"
		switch {
		case doneBy != "" && doneReason != "":
			summary = fmt.Sprintf("Completed (%s): %s", doneBy, doneReason)
		case doneBy != "":
			summary = fmt.Sprintf("Completed (%s)", doneBy)
		case doneReason != "":
			summary = fmt.Sprintf("Completed: %s", doneReason)
		}
		events = append(events, todoCausalTraceEvent{
			ID:       "lifecycle:completed",
			At:       todo.DoneAt.UTC(),
			Category: "lifecycle",
			Kind:     "completed",
			Summary:  summary,
			Ref:      doneBy,
		})
	}
	return events
}

func (s *Service) appendTodoQueueTraceEvents(ctx context.Context, events []todoCausalTraceEvent, todo Todo) []todoCausalTraceEvent {
	for _, p := range s.queueCandidatePlans(ctx) {
		if p == nil {
			continue
		}
		for i, pt := range p.Tasks {
			if pt.TodoID != todo.ID || pt.Queue == nil {
				continue
			}
			at := pt.Queue.QueuedAt
			if at.IsZero() {
				at = p.UpdatedAt
			}
			if at.IsZero() {
				at = todo.UpdatedAt
			}
			if at.IsZero() {
				continue
			}
			kind := traceQueueKind(pt.Queue.Key)
			summary := strings.TrimSpace(pt.Queue.Reason)
			if summary == "" {
				if key := strings.TrimSpace(pt.Queue.Key); key != "" {
					summary = fmt.Sprintf("Queued (%s)", key)
				} else {
					summary = "Queued for execution"
				}
			}
			ref := strings.TrimSpace(p.ID)
			if ref == "" {
				ref = strings.TrimSpace(pt.Queue.Key)
			}
			var link string
			if len(pt.Queue.Blockers) == 1 {
				link = safeTraceURL(pt.Queue.Blockers[0])
			}
			events = append(events, todoCausalTraceEvent{
				ID:       fmt.Sprintf("queue:%s:%d:%s", p.ID, i, kind),
				At:       at.UTC(),
				Category: "queue",
				Kind:     kind,
				Summary:  summary,
				Ref:      ref,
				URL:      link,
			})
		}
	}
	return events
}

func traceQueueKind(key string) string {
	switch strings.TrimSpace(key) {
	case blockerWaitQueueKey:
		return "queued_blocker"
	case pendingPlacementQueueKey:
		return "queued_placement"
	case workspaceQueueKey:
		return "queued_workspace"
	case "request_gate":
		return "queued_request_gate"
	case "":
		return "queued"
	default:
		return "queued_" + strings.TrimSpace(key)
	}
}

func (s *Service) appendTodoBlockerTraceEvents(ctx context.Context, events []todoCausalTraceEvent, todoID string) []todoCausalTraceEvent {
	st := s.todoBlockerDB()
	if st == nil {
		return events
	}
	rows, err := st.ListTodoBlockersByTodo(ctx, todoID)
	if err != nil {
		return events
	}
	for _, b := range rows {
		link := safeTraceURL(b.TargetRef)
		addedAt := b.CreatedAt
		if addedAt.IsZero() {
			addedAt = b.UpdatedAt
		}
		if !addedAt.IsZero() {
			summary := fmt.Sprintf("Blocker added (%s): %s", b.Kind, b.TargetRef)
			if b.Attempts > 0 {
				summary = fmt.Sprintf("%s (attempts: %d)", summary, b.Attempts)
			}
			events = append(events, todoCausalTraceEvent{
				ID:       "blocker:added:" + b.ID,
				At:       addedAt.UTC(),
				Category: "blocker",
				Kind:     "blocker_added",
				Summary:  summary,
				Ref:      b.ID,
				URL:      link,
			})
		}
		if b.ResolvedAt != nil && !b.ResolvedAt.IsZero() {
			kind := "blocker_resolved"
			verb := "resolved"
			if b.State == db.TodoBlockerStateCancelled {
				kind = "blocker_cancelled"
				verb = "cancelled"
			}
			events = append(events, todoCausalTraceEvent{
				ID:       fmt.Sprintf("blocker:%s:%s", verb, b.ID),
				At:       b.ResolvedAt.UTC(),
				Category: "blocker",
				Kind:     kind,
				Summary:  fmt.Sprintf("Blocker %s (%s): %s", verb, b.Kind, b.TargetRef),
				Ref:      b.ID,
				URL:      link,
			})
		}
	}
	return events
}

func (s *Service) appendTodoRecoveryTraceEvents(ctx context.Context, events []todoCausalTraceEvent, todoID string) []todoCausalTraceEvent {
	st := s.todoRecoveryStore()
	if st == nil {
		return events
	}
	rec, found, err := st.GetTodoRecoveryState(ctx, todoID)
	if err != nil || !found {
		return events
	}
	ref := traceAgentRef(rec.MachineID, rec.SessionID)
	if !rec.UpdatedAt.IsZero() {
		summary := "Restart recovery checkpoint recorded"
		if rec.MachineID != "" && rec.LastActiveBootEpoch != "" {
			summary = fmt.Sprintf("Active run checkpoint recorded on %s (epoch %s)", rec.MachineID, rec.LastActiveBootEpoch)
		} else if rec.MachineID != "" {
			summary = fmt.Sprintf("Active run checkpoint recorded on %s", rec.MachineID)
		}
		events = append(events, todoCausalTraceEvent{
			ID:       "recovery:checkpoint",
			At:       rec.UpdatedAt.UTC(),
			Category: "recovery",
			Kind:     "recovery_checkpoint",
			Summary:  summary,
			Ref:      ref,
		})
	}
	if rec.RecoveryAttempts > 0 || (rec.LastRecoveredAt != nil && !rec.LastRecoveredAt.IsZero()) {
		at := rec.UpdatedAt
		if rec.LastRecoveredAt != nil && !rec.LastRecoveredAt.IsZero() {
			at = *rec.LastRecoveredAt
		}
		if !at.IsZero() {
			summary := fmt.Sprintf("Restart recovery attempted (%d)", rec.RecoveryAttempts)
			if rec.MachineID != "" {
				summary = fmt.Sprintf("Restart recovery attempted (%d) on %s", rec.RecoveryAttempts, rec.MachineID)
			}
			events = append(events, todoCausalTraceEvent{
				ID:       fmt.Sprintf("recovery:attempted:%d", rec.RecoveryAttempts),
				At:       at.UTC(),
				Category: "recovery",
				Kind:     "recovery_attempted",
				Summary:  summary,
				Ref:      ref,
			})
		}
	}
	return events
}

func (s *Service) appendTodoSupervisionTraceEvents(ctx context.Context, events []todoCausalTraceEvent, todo Todo) []todoCausalTraceEvent {
	hasParkedReview := false
	if todo.Supervision != nil {
		for idx, rev := range todo.Supervision.Reviews {
			if rev.At.IsZero() {
				continue
			}
			kind, summary := traceReviewEvent(rev)
			if kind == "parked" {
				hasParkedReview = true
			}
			ref := strings.TrimSpace(rev.By)
			if ref == "" {
				ref = strings.TrimSpace(rev.Decided)
			}
			events = append(events, todoCausalTraceEvent{
				ID:       fmt.Sprintf("supervision:review:%d:%s", idx, kind),
				At:       rev.At.UTC(),
				Category: "supervision",
				Kind:     kind,
				Summary:  summary,
				Ref:      ref,
				URL:      safeTraceURL(rev.Link),
			})
		}
		if !hasParkedReview && todo.Supervision.Parked != nil && !todo.Supervision.UpdatedAt.IsZero() {
			summary := "Task parked"
			if reason := strings.TrimSpace(todo.Supervision.Parked.Reason); reason != "" {
				summary = "Task parked: " + reason
			} else if wait := strings.TrimSpace(todo.Supervision.Parked.WaitingOn); wait != "" {
				summary = "Task parked waiting on " + wait
			}
			events = append(events, todoCausalTraceEvent{
				ID:       "supervision:parked",
				At:       todo.Supervision.UpdatedAt.UTC(),
				Category: "supervision",
				Kind:     "parked",
				Summary:  summary,
			})
		}
	}
	if st, ok := s.store().(todoDecisionLister); ok && st != nil {
		decisions, err := st.ListTodoDecisions(ctx, todo.ID, true)
		if err == nil {
			for _, d := range decisions {
				if d.CreatedAt.IsZero() {
					continue
				}
				source := strings.TrimSpace(d.Source)
				if source == "" {
					source = "manual"
				}
				summary := fmt.Sprintf("Decision recorded (%s): %s -> %s", source, strings.TrimSpace(d.Question), strings.TrimSpace(d.Choice))
				events = append(events, todoCausalTraceEvent{
					ID:       "supervision:decision:" + d.ID,
					At:       d.CreatedAt.UTC(),
					Category: "supervision",
					Kind:     "decision_recorded",
					Summary:  summary,
					Ref:      d.ID,
				})
			}
		}
	}
	return events
}

func traceReviewEvent(rev TaskReview) (kind, summary string) {
	verdict := strings.TrimSpace(rev.Verdict)
	note := strings.TrimSpace(rev.Note)
	switch verdict {
	case reviewAccepted:
		kind = "review_accepted"
		summary = "Supervisor accepted"
		if note != "" {
			summary += ": " + note
		}
	case reviewRework:
		kind = "review_rework"
		summary = "Supervisor requested rework"
		if rev.Round > 0 {
			summary = fmt.Sprintf("Supervisor requested rework (round %d)", rev.Round)
		}
		if note != "" {
			summary += ": " + note
		}
	case reviewParked:
		kind = "parked"
		summary = "Task parked"
		if reason := strings.TrimSpace(rev.Reason); reason != "" {
			summary += ": " + reason
		} else if wait := strings.TrimSpace(rev.WaitingOn); wait != "" {
			summary += " waiting on " + wait
		} else if note != "" {
			summary += ": " + note
		}
	case reviewNeedsUser:
		kind = "needs_user"
		summary = "Needs owner decision"
		if reason := strings.TrimSpace(rev.Reason); reason != "" {
			summary += ": " + reason
		} else if why := strings.TrimSpace(rev.WhyOwner); why != "" {
			summary += ": " + why
		} else if note != "" {
			summary += ": " + note
		}
	default:
		if verdict == "" {
			kind = "review"
			summary = "Supervision review"
		} else {
			kind = "review_" + verdict
			summary = fmt.Sprintf("Supervision review (%s)", verdict)
		}
		if note != "" {
			summary += ": " + note
		}
	}
	return kind, summary
}

func appendTodoPRTraceEvents(events []todoCausalTraceEvent, todo Todo) []todoCausalTraceEvent {
	var prs []TodoPRStatus
	if todo.PRStatus != nil {
		prs = append(prs, *todo.PRStatus)
	}
	prs = append(prs, todo.OtherPRs...)
	seen := make(map[string]bool, len(prs))
	for i, pr := range prs {
		u := strings.TrimSpace(pr.URL)
		dedupeKey := strings.ToLower(u)
		if dedupeKey != "" {
			if seen[dedupeKey] {
				continue
			}
			seen[dedupeKey] = true
		}
		at := pr.CheckedAt
		if at.IsZero() {
			at = todo.UpdatedAt
		}
		if at.IsZero() {
			continue
		}
		kind := "pr_observed"
		summary := ""
		state := strings.TrimSpace(pr.State)
		switch {
		case strings.EqualFold(state, "merged"):
			kind = "pr_merged"
			summary = "PR merged"
			if title := strings.TrimSpace(pr.Title); title != "" {
				summary += ": " + title
			}
		case pr.Conflicts:
			kind = "pr_conflict"
			summary = "PR has merge conflicts"
			if title := strings.TrimSpace(pr.Title); title != "" {
				summary += ": " + title
			}
		default:
			parts := make([]string, 0, 3)
			if state != "" {
				parts = append(parts, state)
			}
			if checks := strings.TrimSpace(pr.Checks); checks != "" {
				parts = append(parts, "checks="+checks)
			}
			if review := strings.TrimSpace(pr.Review); review != "" {
				parts = append(parts, "review="+review)
			}
			if len(parts) > 0 {
				summary = "PR observed (" + strings.Join(parts, ", ") + ")"
			} else {
				summary = "PR observed"
			}
		}
		idSuffix := u
		if idSuffix == "" {
			idSuffix = fmt.Sprintf("%d", i)
		}
		ref := strings.TrimSpace(pr.HeadOID)
		if ref == "" {
			ref = u
		}
		events = append(events, todoCausalTraceEvent{
			ID:       fmt.Sprintf("pr:%s:%s", kind, idSuffix),
			At:       at.UTC(),
			Category: "pr",
			Kind:     kind,
			Summary:  summary,
			Ref:      ref,
			URL:      safeTraceURL(u),
		})
	}
	return events
}

func safeTraceURL(raw string) string {
	u := strings.TrimSpace(raw)
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return ""
}

func traceAgentRef(machineID, sessionID string) string {
	m := strings.TrimSpace(machineID)
	sid := strings.TrimSpace(sessionID)
	switch {
	case m != "" && sid != "":
		return m + "/" + sid
	case m != "":
		return m
	default:
		return sid
	}
}
