package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ---- the brief of a task_plan task (#3822) ----

// briefRequired refuses a plan's task that does not say what done is: a writable task needs done_when (one criterion
// that can be checked, at least), a read-only one a goal. The hub supplies an automatic estimate for every task_plan
// task; expect_minutes is only an optional override. A continuation takes its brief instead and says why.
// The refusal names what is missing.
func briefRequired(t *PlanTask) error {
	if t.Continues != "" {
		if t.ContinuesWhy == "" {
			return errors.New("continues needs continues_why: one line on why the earlier task stopped and this one takes over")
		}
		return nil
	}
	var missing []string
	if t.Readonly {
		if t.Goal == "" {
			missing = append(missing, "goal (what the owner gets from it, in one sentence)")
		}
	} else if len(t.DoneWhen) == 0 {
		missing = append(missing, "done_when (at least one result that can be checked)")
	}
	if len(missing) == 0 {
		return nil
	}
	kind := "a writable task"
	if t.Readonly {
		kind = "a read-only task"
	}
	return fmt.Errorf("%s needs %s; nothing was planned. Add them and call task_plan again (a task that takes over from another "+
		"gives continues and continues_why instead and keeps that task's brief)", kind, strings.Join(missing, " and "))
}

// openSuccessor is an open to-do that already carries on to-do id (continues, directly or down a chain of to-dos that
// did): a second continuation of id would be a second to-do for the same work (#6427). False when there is none.
func (s *Service) openSuccessor(id string) (Todo, bool) {
	seen := map[string]bool{id: true}
	next := []string{id}
	for len(next) > 0 {
		b := &s.tbriefs
		b.mu.Lock()
		b.init()
		var found []string
		for todo, l := range b.links {
			if l.Continues != "" && slices.Contains(next, l.Continues) && !seen[todo] {
				found = append(found, todo)
			}
		}
		b.mu.Unlock()
		slices.Sort(found)
		next = nil
		for _, todo := range found {
			seen[todo] = true
			if t, ok := s.todoNow(todo); ok && t.DoneAt == nil && !t.Cancelled() {
				return t, true
			}
			next = append(next, todo)
		}
	}
	return Todo{}, false
}

// errContinued refuses a task that continues prev when to-do succ already does.
func errContinued(prev string, succ Todo) error {
	return fmt.Errorf("continues: %s is already carried on by to-do %s (%q), which is still open. Do not open a second "+
		"to-do for the same work: plan the task with todo_id %s (it runs on that to-do), or send its agent what is new "+
		"(agent_send)", prev, succ.ID, succ.Title, succ.ID)
}

// takeOver binds a continuation to its original to-do, without stopping Agents or reopening it before Start.
// An unsafe or unauthorized handoff is refused, never replaced with a second to-do.
func (s *Service) takeOver(ctx context.Context, conversation string, t *PlanTask) error {
	if t.Continues == "" {
		return nil
	}
	if t.ReuseTodo != "" && t.ReuseTodo != t.Continues {
		return fmt.Errorf("continues and todo_id must name the same to-do")
	}
	if succ, ok := s.openSuccessor(t.Continues); ok {
		return errContinued(t.Continues, succ)
	}
	if _, err := s.taskTodoForConversation(conversation, t.Continues); err != nil {
		return err
	}
	t.ReuseTodo = t.Continues
	return nil
}

// planBriefs gives each task of a plan from conversation (on machine) its brief as it will be kept, for the card to
// show before "Start": who asked is read off the conversation once (briefOrigin); a task that continues another takes
// that task's brief, with its criteria and expected time as they stand. The optional contracts from task_plan
// are in task order.
func (s *Service) planBriefs(ctx context.Context, machine, conversation string, tasks []PlanTask, contracts ...taskContract) error {
	var origin *briefRecord
	for i := range tasks {
		t := &tasks[i]
		var contract taskContract
		if i < len(contracts) {
			contract = contracts[i]
		}
		if t.Continues != "" {
			prev, ok := s.todoNow(t.Continues)
			if !ok {
				return fmt.Errorf("task %d: continues: no task %q", i+1, t.Continues)
			}
			if prev.Origin == "" {
				return fmt.Errorf("task %d: continues: %s is the user's own to-do, not a task that was handed out", i+1, prev.ID)
			}
			brief, err := s.ensureTaskBrief(ctx, prev)
			if err != nil {
				return fmt.Errorf("task %d: continues: %v", i+1, err)
			}
			if t.ScopeRevision != "" && t.ScopeRevision != brief.scopeRevision().Format(time.RFC3339Nano) {
				return fmt.Errorf("task %d: the scope of %s changed; read its brief and plan the continuation again", i+1, prev.ID)
			}
			if contract.changesFrom(brief) != (taskContract{}) {
				return fmt.Errorf("task %d: a continuation keeps its task contract. Use task_scope to change work_mode or completion_mode first", i+1)
			}
			// Its own prompt stays its own; what done means is the brief's, as its scope changes left it.
			// A continuation of an automatic brief keeps its current estimate. A manual or legacy brief becomes
			// automatic only now, through this task_plan dispatch, with the system default unless explicitly overridden.
			if t.EstimateSource == "" {
				if brief.EstimateSourceNow != "" {
					t.ExpectMinutes, t.EstimateSource = automaticEstimate(brief.ExpectNow, brief.EstimateSourceNow)
					t.EstimateEvidence = brief.EstimateEvidenceNow
				} else {
					t.ExpectMinutes, t.EstimateSource = automaticEstimate(0, "")
				}
			} else {
				t.ExpectMinutes, t.EstimateSource = automaticEstimate(t.ExpectMinutes, t.EstimateSource)
			}
			t.DoneWhen = brief.Criteria
			t.Brief = &brief
			continue
		}
		if origin == nil {
			o := s.briefOrigin(ctx, machine, conversation)
			origin = &o
		}
		rec := *origin
		if current, ok := s.briefOfTodo(t.ReuseTodo); ok {
			if contract.changesFrom(current) != (taskContract{}) {
				return fmt.Errorf("task %d: this to-do already has a task contract. Use task_scope to change work_mode or completion_mode", i+1)
			}
			rec.taskContract = taskContract{WorkMode: current.WorkModeNow, CompletionMode: current.CompletionModeNow,
				WorkModeDeclared: current.workModeDeclared()}
		} else {
			rec.taskContract = contract.withDefaultWorkMode(t.Readonly)
		}
		if t.EstimateCalibration {
			s.estimateNewPlanTask(ctx, conversation, t)
		} else {
			t.ExpectMinutes, t.EstimateSource = automaticEstimate(t.ExpectMinutes, t.EstimateSource)
		}
		rec.ID, rec.CreatedAt = "brief_"+randomHex(8), time.Now().UTC()
		rec.EstimateEvidence = t.EstimateEvidence
		rec.DistinctFrom = slices.Clone(t.DistinctFrom)
		rec.Goal, rec.DoneWhen, rec.ExpectMinutes, rec.EstimateSource, rec.OutOfBounds = firstNonEmpty(t.Goal, t.Title), t.DoneWhen, t.ExpectMinutes, t.EstimateSource, t.OutOfBounds
		if rec.StartedBy != briefByOwner {
			rec.Reason = t.StartedWhy
		}
		v := (&taskBrief{rec: rec}).view()
		t.Brief = &v
	}
	return nil
}

// checkContinuationScope rejects a plan that was prepared against a different
// revision of the original brief. A later estimate updates the dispatch clock only.
func (s *Service) checkContinuationScope(t *PlanTask) error {
	if t.Continues == "" {
		return nil
	}
	current, ok := s.briefOfTodo(t.Continues)
	if !ok || t.Brief == nil || current.ID != t.Brief.ID || !current.scopeRevision().Equal(t.Brief.scopeRevision()) {
		return fmt.Errorf("the scope of %s changed or is unavailable; read its brief and plan the continuation again", t.Continues)
	}
	if !current.ClockAt.Equal(t.Brief.ClockAt) {
		t.ExpectMinutes, t.EstimateSource = automaticEstimate(current.ExpectNow, current.EstimateSourceNow)
		t.EstimateEvidence, t.Brief = current.EstimateEvidenceNow, &current
	}
	return nil
}

// briefStarted keeps the brief of plan p's task t and appends one dispatch round: the card's brief, dated from the
// start for new work, or the brief it continues. Every task_plan dispatch, including reuse of a manual/legacy to-do,
// gets an explicit round id and estimate source. A plan saved before briefs gets a reconstructed brief.
func (s *Service) briefStarted(ctx context.Context, p *TaskPlan, t *PlanTask, todo Todo) {
	startedAt := time.Now().UTC()
	t.ExpectMinutes, t.EstimateSource = automaticEstimate(t.ExpectMinutes, t.EstimateSource)
	briefID := ""
	if current, ok := s.briefOfTodo(todo.ID); ok {
		briefID = current.ID // a reused to-do keeps its brief and gains a new dispatch round below
	} else if t.Continues != "" {
		prev, ok := s.todoNow(t.Continues)
		if ok {
			if brief, err := s.ensureTaskBrief(ctx, prev); err == nil {
				if err := s.linkTaskBrief(ctx, brief.ID, todo.ID, prev.ID, t.ContinuesWhy); err != nil {
					s.logf("task %s: brief of %s: %v", todo.ID, prev.ID, err)
					return
				}
				briefID = brief.ID
			}
		}
		if briefID == "" {
			s.logf("task %s: the task it continues (%s) is gone: it gets a brief of its own", todo.ID, t.Continues)
		}
	}
	if briefID == "" {
		rec := briefRecord{StartedBy: briefByUnknown, Conversation: p.Conversation, Goal: firstNonEmpty(t.Goal, t.Title),
			DoneWhen: t.DoneWhen, ExpectMinutes: t.ExpectMinutes, EstimateSource: t.EstimateSource,
			EstimateEvidence: t.EstimateEvidence, OutOfBounds: t.OutOfBounds, DistinctFrom: slices.Clone(t.DistinctFrom)}
		if t.Brief != nil && !t.Brief.Reconstructed && t.Continues == "" {
			rec = t.Brief.briefRecord
		}
		rec.CreatedAt = startedAt // the clock starts when the work is handed out, not when it was proposed
		v, err := s.putTaskBrief(ctx, rec, todo.ID)
		if err != nil {
			s.logf("task %s: brief: %v", todo.ID, err)
			return
		}
		briefID = v.ID
	}
	roundID := p.ID + "/" + todo.ID + "@" + startedAt.Format(time.RFC3339Nano)
	_, changed, err := s.changeBrief(ctx, briefID, func(tb *taskBrief) bool {
		for _, e := range tb.st.Log {
			if e.Kind == briefEventDispatch && e.RoundID == roundID {
				return false
			}
		}
		tb.st.Log = append(tb.st.Log, BriefEvent{At: startedAt, Kind: briefEventDispatch, Todo: todo.ID,
			Source: "task_plan", PlanID: p.ID, RoundID: roundID, ExpectMinutes: t.ExpectMinutes,
			EstimateSource: t.EstimateSource, EstimateEvidence: t.EstimateEvidence})
		return true
	})
	if err != nil {
		s.logf("task %s: dispatch record: %v", todo.ID, err)
		return
	}
	if changed && t.EstimateCalibration {
		s.estimateDispatch(ctx, p, t, todo.ID, briefID, roundID, startedAt)
	}
}
