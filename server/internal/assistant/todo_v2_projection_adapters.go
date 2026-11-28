package assistant

import (
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Unexported adapters for todos_v2 projection unification (#16432):
//   - map projection.Views output (assistantwire.TodoSummary) to legacy todoTriage / open membership
//     so direction_tree, leader_reports, and tasks_snapshot share one source of truth when todos_v2=true;
//   - compute read-only direct-child rollup summaries for parent goals without ever auto-closing parents.

// todoChildSummary is a read-only rollup of direct children (child.PartOf == parent.ID) attached to
// parent listedTodo rows under todos_v2=true.
type todoChildSummary struct {
	Total    int `json:"total"`
	Open     int `json:"open"`
	NeedsYou int `json:"needs_you"`
	Working  int `json:"working"`
	Done     int `json:"done"`
}

// projectionViewCountsAsOpen reports whether a projected view is open (matching projection.Counts.Open
// and todoBoardProjection.Counts.OpenTotal: neither done nor someday).
func projectionViewCountsAsOpen(v assistantwire.TodoSummary) bool {
	return !projectionViewIsClosed(v) && v.Section != assistantwire.TodoListSectionSomeday
}

func projectionViewIsClosed(v assistantwire.TodoSummary) bool {
	return v.Phase == assistantwire.TodoPhaseDone || v.Section == assistantwire.TodoListSectionDone
}

// projectionViewNeedsYou is the immediate, non-quiet attention counted by projection.Counts.
func projectionViewNeedsYou(v assistantwire.TodoSummary, quiet bool) bool {
	return v.Section == assistantwire.TodoListSectionNeedsYou && !quiet
}

// todoCarriedByParent mirrors the section count membership of projection.Counts. The caller supplies
// whether the direct parent is present and unclosed. Outside decisions keep their own count.
func todoCarriedByParent(t Todo, parentUnclosed bool) bool {
	return parentUnclosed && t.PartOf != "" && t.PartOf != t.ID && !isOutsideDecisionTodo(t.ID)
}

// todoTriageFromProjectionView maps a projection TodoSummary into todoTriage so server consumers
// (direction_tree, leader_reports, tasks_snapshot) follow todo/projection under todos_v2=true.
func todoTriageFromProjectionView(t listedTodo, v assistantwire.TodoSummary, look triageLookup) todoTriage {
	last := triageLastActivity(t, look, time.Time{})
	if last.IsZero() && !v.Since.IsZero() {
		last = v.Since
	}
	at := func(section, rule string) todoTriage {
		return todoTriage{Section: section, Rule: rule, Last: last}
	}
	switch {
	case projectionViewIsClosed(v):
		switch v.Reason {
		case assistantwire.TodoReasonCanceled:
			return at(triageCancelled, ruleCancelled)
		case assistantwire.TodoReasonSuperseded:
			return at(triageDone, ruleSuperseded)
		default:
			if v.Section == assistantwire.TodoListSectionJustFinished {
				return at(triageJustDone, ruleClosed)
			}
			return at(triageDone, ruleClosed)
		}
	case v.Section == assistantwire.TodoListSectionNeedsYou:
		switch v.Reason {
		case assistantwire.TodoReasonFailedStart:
			return at(triageWaiting, ruleFailedStart)
		case assistantwire.TodoReasonAgentAsks, assistantwire.TodoReasonInterrupted, assistantwire.TodoReasonStoppedUnfinished:
			return at(triageWaiting, ruleAgentAsks)
		default:
			return at(triageWaiting, ruleOwnerDecides)
		}
	case v.Section == assistantwire.TodoListSectionCanWait:
		// Deferred for the briefing until CanWaitSLAHours expires; not actively stuck or blocking.
		return at(triageWorking, ruleRest)
	case v.Section == assistantwire.TodoListSectionWorking || v.Phase == assistantwire.TodoPhaseWorking:
		return at(triageWorking, ruleAgentRunning)
	case v.Section == assistantwire.TodoListSectionWaiting || v.Phase == assistantwire.TodoPhaseWaiting:
		if v.Reason == assistantwire.TodoReasonPrOpen {
			return at(triagePR, rulePROpen)
		}
		// Parked, active persistent blocker, usage limit, or waiting on someone else:
		// explicitly waiting on a named condition rather than stopped unfinished.
		return at(triageWorking, ruleAgentRunning)
	case v.Section == assistantwire.TodoListSectionJustFinished:
		if v.Line != nil && v.Line.Key == assistantwire.TodoLineKeyAccepted {
			return at(triageJustDone, ruleAccepted)
		}
		return at(triageJustDone, ruleFinishedUnseen)
	case v.Section == assistantwire.TodoListSectionSomeday || v.Phase == assistantwire.TodoPhaseSomeday:
		return at(triageShelved, ruleStale)
	default:
		if v.Reason == assistantwire.TodoReasonFinishedSeen {
			if v.Line != nil && v.Line.Key == assistantwire.TodoLineKeyAccepted {
				return at(triageDone, ruleAccepted)
			}
			return at(triageDone, ruleSeen)
		}
		if !v.Inactive.IsZero() {
			return at(triageShelved, ruleStale)
		}
		return at(triageOther, ruleRest)
	}
}

// populateTodoChildSummaries computes read-only direct-child rollups from already-projected views
// and attaches ChildSummary only to parents with at least one direct child. Callers must invoke
// this only when todos_v2 is enabled.
func populateTodoChildSummaries(list []listedTodo, views []assistantwire.TodoSummary) {
	if len(list) == 0 || len(views) == 0 {
		return
	}
	byParent := make(map[string]*todoChildSummary)
	for i := range list {
		if i >= len(views) {
			break
		}
		parentID := strings.TrimSpace(list[i].PartOf)
		if parentID == "" || parentID == list[i].ID {
			continue
		}
		sum := byParent[parentID]
		if sum == nil {
			sum = &todoChildSummary{}
			byParent[parentID] = sum
		}
		sum.Total++
		v := views[i]
		if projectionViewIsClosed(v) {
			sum.Done++
			continue
		}
		if !projectionViewCountsAsOpen(v) {
			continue
		}
		sum.Open++
		switch {
		case projectionViewNeedsYou(v, list[i].Quiet):
			sum.NeedsYou++
		case v.Section == assistantwire.TodoListSectionWorking || v.Phase == assistantwire.TodoPhaseWorking:
			sum.Working++
		}
	}
	for i := range list {
		if sum := byParent[list[i].ID]; sum != nil && sum.Total > 0 {
			list[i].ChildSummary = sum
		}
	}
}
