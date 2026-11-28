package assistant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func (t Todo) hasTaskRuntime() bool { return t.Phase != "" || t.WaitState != nil }

func (st todoStage) hasTaskRuntime() bool { return st.Phase != "" || st.WaitState != nil }

func (st todoStage) applyRuntime(t *Todo) {
	t.Phase, t.WaitState, t.runtimeAt = st.Phase, st.WaitState, st.RuntimeAt
	t.runtimeObservedWait = st.ObservedWait
}

func sameTaskWait(a, b *assistantwire.TaskWaitState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.WaitingOn == b.WaitingOn && a.Target == b.Target && a.Reason == b.Reason && a.Until.Equal(b.Until)
}

func sameTaskRuntime(a, b Todo) bool {
	return a.Phase == b.Phase && a.runtimeObservedWait == b.runtimeObservedWait &&
		a.runtimeAt.Equal(b.runtimeAt) && sameTaskWait(a.WaitState, b.WaitState)
}

func taskRuntimeWork(t Todo, lang i18n.Lang) *proto.WorkState {
	switch t.Phase {
	case assistantwire.TaskPhaseDone:
		return &proto.WorkState{Phase: proto.WorkDone, Line: workT(lang, "done")}
	case assistantwire.TaskPhaseWaiting:
		if w := t.WaitState; w != nil && w.WaitingOn.Valid() {
			on := proto.WaitExternal
			if w.WaitingOn == assistantwire.WaitUser {
				on = "" // supervision owns the decision until it opens a question for the owner
			}
			return &proto.WorkState{Phase: proto.WorkBlocked, On: on, Wait: clip(oneLine(w.Reason), 80), Line: clip(oneLine(w.Reason), 60)}
		}
	}
	return &proto.WorkState{}
}

// readTaskRuntimeReport validates at admission. Stored enums keep unknown values
// readable across a rolling upgrade.
func readTaskRuntimeReport(phase *assistantwire.TaskPhase, wait *assistantwire.TaskWaitState) (assistantwire.TaskPhase, error) {
	var p assistantwire.TaskPhase
	if phase != nil {
		p = *phase
		if !p.Valid() {
			return "", errors.New("phase must be preparing, working, waiting, reviewing or done")
		}
	}
	if wait == nil {
		return p, nil
	}
	if !wait.WaitingOn.Valid() {
		return "", errors.New("wait_state.waiting_on must be user, external, dependency or date")
	}
	if p != "" && p != assistantwire.TaskPhaseWaiting {
		return "", errors.New("wait_state requires phase waiting")
	}
	target := &wait.Target
	if target.Kind != "" && !target.Kind.Valid() {
		return "", errors.New("wait_target.kind must be todo, pr, issue, chat, url or time")
	}
	target.Ref = strings.TrimSpace(target.Ref)
	wait.Reason = strings.TrimSpace(wait.Reason)
	if target.Ref == "" || len(target.Ref) > 600 || wait.Reason == "" || len(wait.Reason) > 600 {
		return "", errors.New("wait_target.ref and wait_state.reason are required and can hold up to 600 bytes")
	}
	target.Summary = clip(oneLine(target.Summary), 200)
	wait.Until = wait.Until.UTC()
	return assistantwire.TaskPhaseWaiting, nil
}

func (s *Service) recordTaskRuntimeReport(ctx context.Context, t Todo, machine, session, kind string,
	phase assistantwire.TaskPhase, wait *assistantwire.TaskWaitState, deliveryPR string, at time.Time) error {
	if phase == "" && deliveryPR == "" && t.hasTaskRuntime() {
		return nil // reports omitting runtime fields leave the last explicit state in place
	}
	brief, _ := s.committedBriefOfTodo(t.ID)
	var initial assistantwire.TaskPhase
	if phase == "" {
		if deliveryPR != "" || brief.WorkModeNow.Valid() {
			switch kind {
			case upReportProgress, upReportQuestion:
				initial = assistantwire.TaskPhaseWorking
			case upReportBlocked:
				initial = assistantwire.TaskPhaseWaiting
			case upReportDone:
				initial = assistantwire.TaskPhaseDone
			}
		}
		if initial == "" {
			return nil
		}
	}
	var pr string
	if deliveryPR != "" {
		var err error
		pr, err = s.reportedRuntimeDeliveryPR(ctx, t, machine, session, deliveryPR)
		if err != nil {
			return err
		}
	}
	_, err := s.updateTaskRuntime(ctx, t.ID, func(current Todo, st *todoStage) (bool, error) {
		if linked := taskRepositoryAgentLinkedAt(t, machine, session); linked.IsZero() ||
			!taskRepositoryAgentLinkedAt(current, machine, session).Equal(linked) ||
			current.ReopenedAt != nil && at.Before(*current.ReopenedAt) || at.Before(st.RuntimeAt) {
			return false, errors.New("the task changed while recording the report")
		}
		wasStructured := st.hasTaskRuntime()
		if phase != "" || !wasStructured {
			p := phase
			if p == "" {
				p = initial
			}
			if p != assistantwire.TaskPhaseWaiting || wait != nil {
				st.WaitState, st.ObservedWait = wait, false
			}
			st.Phase = p
			st.advanceRuntimeStage()
		} else if pr == "" {
			return false, nil
		}
		st.RuntimeAt = at.UTC()
		newPR := st.bindPR(pr)
		if newPR || !wasStructured {
			st.refreshRuntimeDelivery(at, brief.CompletionModeNow)
		}
		return true, nil
	})
	return err
}

// An explicit PR can name a stored delivery link or a PR whose ownership is
// independently established by the existing report importer.
func (s *Service) reportedRuntimeDeliveryPR(ctx context.Context, t Todo, machine, session, raw string) (string, error) {
	_, link, err := repository.ParseTaskLink(raw)
	if err != nil || link.Kind != "pull_request" {
		return "", errors.New("delivery_pr must be a full pull request URL")
	}
	pr := link.CanonicalURL
	rows, ok := s.replyRepositoryBindings(ctx, t.ID)
	if !ok {
		return "", errors.New("task repository links could not be read")
	}
	for _, row := range rows {
		for _, saved := range row.Links {
			if sameReplyPR(saved.CanonicalURL, pr) {
				if saved.Role == repository.LinkRoleDelivery {
					return saved.CanonicalURL, nil
				}
				if saved.Role == repository.LinkRoleReference {
					return "", errors.New("delivery_pr is recorded as a reference")
				}
			}
		}
	}
	if sameReplyPR(t.PRURL, pr) {
		return t.PRURL, nil
	}
	links, err := s.importOwnedTaskRepositoryLinks(ctx, t, machine, session, []string{pr}, "", time.Time{}, nil)
	if err != nil {
		return "", err
	}
	if accepted := taskReplyAcceptedPR(pr, links); accepted.canonical != "" {
		return accepted.canonical, nil
	}
	return "", errors.New("delivery_pr needs a bound delivery link or proof from the Agent's checkout")
}

func (st *todoStage) advanceRuntimeStage() {
	var stage string
	switch st.Phase {
	case assistantwire.TaskPhasePreparing:
		stage = "plan"
	case assistantwire.TaskPhaseWorking:
		stage = "develop"
	case assistantwire.TaskPhaseReviewing:
		stage = "review"
	}
	if stageIndex(stage) > stageIndex(st.Stage) {
		st.Stage = stage
	}
}

// runtimePR selects an outstanding delivery before a resolved one. An
// unreadable link stays outstanding until the forge can answer for it.
func (st todoStage) runtimePR() *TodoPRStatus {
	if p := st.PRStatus; p != nil && p.State == "open" {
		return p
	}
	var merged, closed *TodoPRStatus
	unread := st.PR != "" && st.PRStatus == nil
	if p := st.PRStatus; p != nil {
		switch p.State {
		case "merged":
			merged = p
		case "closed":
			closed = p
		default:
			unread = true
		}
	}
	for _, url := range slices.Backward(st.PRs) {
		if url == st.PR {
			continue
		}
		p := st.EarlierPRStatus[url]
		switch p.State {
		case "open":
			return &p
		case "merged":
			if merged == nil {
				merged = &p
			}
		case "closed":
			if closed == nil {
				closed = &p
			}
		default:
			unread = true
		}
	}
	if unread {
		return nil
	}
	if merged != nil {
		return merged
	}
	return closed
}

func (st todoStage) runtimeDeploy(p *TodoPRStatus) *deployWatch {
	w := st.Deploy
	if w != nil && (p != nil && p.URL == w.PR || p == nil &&
		(st.PR == w.PR && len(st.PRs) == 0 || st.PR == "" && len(st.PRs) == 1 && st.PRs[0] == w.PR)) {
		return w
	}
	return nil
}

func (st *todoStage) refreshRuntimeDelivery(at time.Time, mode assistantwire.CompletionMode) {
	if st.PR == "" && len(st.PRs) == 0 {
		return
	}
	p := st.runtimePR()
	w := st.runtimeDeploy(p)
	d := TodoDelivery{State: deliveryPR, At: at.UTC()}
	switch {
	case w != nil && w.State == deployIn:
		d.State = deliveryLive
	case w != nil && (w.State == deployOut || w.State == deployNotIn && w.Merged):
		d.State, d.Unblock = deliveryMerged, unblockAgent
		if mode != assistantwire.CompletionModeLanded {
			d.Gate, d.Unblock = w.Gate, unblockOther
		}
	case p != nil && p.State == "merged":
		d.State, d.Unblock = deliveryMerged, unblockAgent
	}
	if prev := st.Delivery; prev == nil || prev.State != d.State || prev.Gate != d.Gate || prev.Unblock != d.Unblock {
		st.Delivery = &d
	}
	if stage := deliveryStage(d.State); stageIndex(stage) > stageIndex(st.Stage) {
		st.Stage = stage
	}
}

func prNeedsWork(p TodoPRStatus) bool {
	return p.Draft || p.Conflicts || p.Review == "changes_requested" || p.Checks == "failure" || p.Checks == "failing"
}

// Structured waits for other people, dates and dependencies live independently
// of delivery. Only evidence about the exact observed dependency can clear it.
func (st *todoStage) refreshRuntimeFromProviders(at time.Time, checksReason string, mode assistantwire.CompletionMode) {
	st.refreshRuntimeDelivery(at, mode)
	if !st.Phase.Valid() || at.Before(st.RuntimeAt) {
		return
	}
	p := st.runtimePR()
	w := st.runtimeDeploy(p)
	if wait := st.WaitState; wait != nil && !st.ObservedWait {
		if wait.WaitingOn != assistantwire.WaitExternal || wait.Target.Kind != assistantwire.WaitTargetPR {
			return
		}
		fact := st.PRStatus
		if fact == nil || !sameReplyPR(fact.URL, wait.Target.Ref) {
			fact = nil
			for url, earlier := range st.EarlierPRStatus {
				if sameReplyPR(url, wait.Target.Ref) {
					fact = &earlier
					break
				}
			}
		}
		if fact == nil || fact.State == "open" && !prNeedsWork(*fact) &&
			(w == nil || w.State != deployIn || !sameReplyPR(w.PR, fact.URL)) {
			return
		}
		if p == nil {
			p = fact
		}
	}
	var reason, url string
	switch {
	case p != nil && (p.State == "closed" || p.State == "open" && prNeedsWork(*p)):
		st.Phase = assistantwire.TaskPhaseWorking
	case p != nil && p.State == "open" && p.Checks == "pending":
		st.Phase, url, reason = assistantwire.TaskPhaseWaiting, p.URL, checksReason
	case w != nil && w.State == deployIn:
		st.Phase = assistantwire.TaskPhaseReviewing
	case w != nil && (w.State == deployOut || w.State == deployNotIn && w.Merged):
		st.Phase = assistantwire.TaskPhaseReviewing
		if mode != assistantwire.CompletionModeLanded {
			st.Phase, url, reason = assistantwire.TaskPhaseWaiting, w.PR, w.Gate
		}
	case w != nil && w.State == deployUnmerged && p == nil:
		st.Phase = assistantwire.TaskPhaseWorking
	case p == nil:
		return // no usable observation
	default:
		st.Phase = assistantwire.TaskPhaseReviewing
	}
	st.WaitState, st.ObservedWait = nil, url != ""
	if url != "" {
		st.WaitState = &assistantwire.TaskWaitState{
			WaitingOn: assistantwire.WaitExternal,
			Target:    assistantwire.TaskWaitTarget{Kind: assistantwire.WaitTargetPR, Ref: url},
			Reason:    reason,
		}
	}
}

func (s *Service) taskRuntimeWorking(ctx context.Context, t Todo, at time.Time) {
	if !t.hasTaskRuntime() {
		return
	}
	if _, err := s.updateTaskRuntime(ctx, t.ID, func(_ Todo, st *todoStage) (bool, error) {
		if at.Before(st.RuntimeAt) || !st.Phase.Valid() {
			return false, nil
		}
		st.Phase, st.WaitState, st.ObservedWait, st.RuntimeAt = assistantwire.TaskPhaseWorking, nil, false, at.UTC()
		st.advanceRuntimeStage()
		return true, nil
	}); err != nil {
		s.logf("task %s: runtime start: %v", t.ID, err)
	}
}

// updateTaskRuntime serializes runtime, delivery and PR changes through the
// stage ledger. Failed writes leave both the published task and its saved row intact.
func (s *Service) updateTaskRuntime(ctx context.Context, id string, update func(Todo, *todoStage) (bool, error)) (bool, error) {
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	current, ok := s.todoNow(id)
	if !ok || current.DoneAt != nil {
		return false, errors.New("task is no longer open")
	}
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		return false, errors.New("task stages could not be read")
	}
	st := b.stages[id]
	changed, err := update(current, &st)
	if changed && err == nil {
		err = s.keepTodoStageLocked(ctx, id, st)
	}
	b.mu.Unlock()
	if err != nil || !changed {
		return false, err
	}
	s.publishRuntimeStage(id, st)
	s.todosChanged(ctx)
	return true, nil
}

func (s *Service) keepTodoStageLocked(ctx context.Context, id string, st todoStage) error {
	next := maps.Clone(s.reports.stages)
	next[id] = st
	if store := s.store(); store != nil {
		if err := store.PutSetting(ctx, reportStagesKey, next); err != nil {
			return fmt.Errorf("save task stage: %w", err)
		}
	}
	s.reports.stages = next
	return nil
}

func (s *Service) publishRuntimeStage(id string, st todoStage) {
	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()
	if t := s.todos.todos[id]; t != nil {
		st.applyRuntime(t)
		t.Stage, t.StageKind, t.Delivery = st.Stage, st.Kind, st.Delivery
		t.PRURL, t.PRs, t.PRStatus, t.OtherPRs = st.PR, st.PRs, st.PRStatus, st.otherPRFacts()
	}
}
