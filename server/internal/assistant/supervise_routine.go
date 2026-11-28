package assistant

import "time"

// Use the same two-minute window as the conversation relay. OwnerWork only records
// verified direct input and names the node as well as the session.
func recentOwnerInput(t Todo, now time.Time) bool {
	if t.Supervision == nil {
		return false
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if w := t.Supervision.ownerSessionWork(a.MachineID, a.SessionID); w != nil && w.Instruction != nil {
			if age := now.Sub(w.Instruction.At); age >= 0 && age < ownerPresentFor {
				return true
			}
		}
	}
	return false
}

// An owner wait is delivered as an idle-shaped stop. Its work identity is stored
// with the wait, including for outbox entries written by earlier managers.
func ownerWaitSignal(t Todo, g superviseSignal) bool {
	return g.Kind == sigIdle && t.Supervision != nil && t.Supervision.Waits != nil &&
		g.Key == t.Supervision.Waits.Work.UTC().Format(time.RFC3339Nano)
}

func routineSupervisionSuppressed(t Todo, g superviseSignal, now time.Time) bool {
	switch g.Kind {
	case sigOwnerPresent:
		// Only the generic reminder is redundant. Reissuing an expired question
		// or permission card still needs the supervisor's direct path.
		key, _, waiting := ownerPresentTodoWait(t)
		return waiting && g.Key == key && recentOwnerInput(t, now)
	case sigIdle:
		if g.Handoff || ownerWaitSignal(t, g) {
			return false // deliver the decision or handoff itself
		}
	case sigOrphaned, sigEstimateDue, sigOverdue, sigStalled, sigNoProgress, sigUnjudged, sigInterrupted, sigOutageRecovery:
	default:
		return false
	}
	if t.DoneAt != nil {
		return true
	}
	switch triageReviewState(t) {
	case reviewUserCheck, reviewParked, reviewNeedsUser:
		return true
	}
	return t.Supervision != nil && t.Supervision.Waits != nil || recentOwnerInput(t, now)
}

func (s *Service) routineSignalSuppressed(g superviseSignal, now time.Time) bool {
	t, ok := s.todoNow(g.Todo)
	return ok && routineSupervisionSuppressed(t, g, now)
}
