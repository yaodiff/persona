package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// An owner's answer to a task's decision given in a conversation (#6470, docs/assistant/todos.md "Decisions the owner
// can answer"). The owner often answers a needs_user decision in the chat, in their own words, instead of on its card;
// the conversation passing that on with agent_send alone leaves the decision waiting (in Needs you, raised again every
// day, not on the to-do). task_decided ends it as the card does: told_agent false takes the option through chooseOption
// (sent to the task's agent, then recorded); told_agent true, for an answer agent_send already carried, records it
// without sending it again (decisionTold). A recurring check's decision goes to the task's supervisor either way
// (checkDecided), and the tool's reply says so.

// toolTaskDecided is task_decided: the main conversation, or task todo_id's supervisor, records the option the owner
// chose of its waiting decision in conversation self on machine.
func (s *Service) toolTaskDecided(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a struct {
		TodoID    string `json:"todo_id"`
		Option    string `json:"option"`
		Said      string `json:"said"`
		ToldAgent bool   `json:"told_agent"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	a.TodoID, a.Option, a.Said = strings.TrimSpace(a.TodoID), strings.TrimSpace(a.Option), strings.TrimSpace(a.Said)
	if a.Option == "" || a.Said == "" {
		return "", errors.New("task_decided needs option (the label of the option the owner chose) and said (their words)")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	t, ok := s.todoNow(a.TodoID)
	if !ok {
		return "", fmt.Errorf("no task %q", a.TodoID)
	}
	supervisor := t.Origin
	if t.Supervision != nil && t.Supervision.Supervisor != "" {
		supervisor = t.Supervision.Supervisor
	}
	// The main conversation (where the owner talks), or the task's supervisor, as for task_scope.
	if supervisor != self && !s.isMain(ctx, self) && !s.backgroundSupervises(ctx, self, t) {
		return "", fmt.Errorf("task %s is supervised by another conversation: record the owner's answer from the main conversation or its supervisor", t.ID)
	}
	// The decision it answers: a recurring check's goes to the supervisor, not the Agent (checkDecided), and the reply
	// says so, so neither conversation takes the choice for one the Agent has. An open personal verification (user_check)
	// has no multi-option decision ask; when the owner explicitly passes it in chat, settle it through acceptTry.
	asked, ok := s.askCopy(t.ID)
	if ok && asked.OutsideID != "" {
		return "", fmt.Errorf("task %s is an outside decision: only its owner's Needs-you answer may settle it, not task_decided", t.ID)
	}
	if t.DoneAt == nil && t.Supervision.state(t) == reviewUserCheck {
		return s.toolTaskDecidedUserCheck(ctx, machine, self, t.ID, a.Option, a.Said)
	}
	var result acceptResult
	var err error
	if a.ToldAgent {
		var taken bool
		taken, err = s.decisionTold(ctx, t.ID, a.Option, machine, self, a.Said)
		result.taken = taken
	} else {
		result, err = s.chooseOption(ctx, t.ID, "", a.Option)
	}
	if ask, ok := s.askCopy(t.ID); errors.Is(err, errDecisionOption) && ok {
		labels := make([]string, 0, len(ask.Options))
		for _, o := range ask.Options {
			labels = append(labels, o.Label)
		}
		return "", fmt.Errorf("%q is none of task %s's options (%s): give one of them as written, or ask the owner",
			a.Option, t.ID, strings.Join(labels, " / "))
	}
	if err != nil {
		return "", err
	}
	if !result.taken {
		return "", fmt.Errorf("task %s has no decision waiting for the owner (answered, re-asked or closed meanwhile): nothing recorded", t.ID)
	}
	if result.undelivered != nil {
		return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision. The ask is answered and the supervisor was told to pass the choice on; no running Agent received it.", a.Option, t.ID), nil
	}
	switch {
	case a.ToldAgent && asked.Check != "":
		return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision. The decision is answered (out of their Needs you); nothing was sent to its agent again. Its recurring progress check raised it, so the choice also went to the task's supervisor as a check signal.", a.Option, t.ID), nil
	case a.ToldAgent:
		return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision. The decision is answered (out of their Needs you); nothing was sent to its agent again.", a.Option, t.ID), nil
	case asked.Check != "":
		return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision. Its recurring progress check raised it, so, as their answer on the card would, the choice went to the task's supervisor as a check signal, not to its agent.", a.Option, t.ID), nil
	}
	return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision, and it went to the task's agent as their answer on the card would.", a.Option, t.ID), nil
}

// toolTaskDecidedUserCheck settles an open personal verification (user_check) when the owner verbally passes it in a
// conversation. Only an explicit approval word (passWords, such as "Pass" or "通过") settles the round; change requests
// or non-pass options are refused so the supervisor routes them to rework or agent_send instead.
func (s *Service) toolTaskDecidedUserCheck(ctx context.Context, machine, self, id, option, said string) (string, error) {
	if !passWords.MatchString(option) {
		return "", fmt.Errorf("task %s is awaiting the owner's verification (user_check), not a multi-option decision: pass option \"Pass\" with the owner's approval words in said to pass and close it, or use task_review(verdict=%q) if they requested changes", id, reviewRework)
	}
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	t, ok := s.todoNow(id)
	asked, askOK := s.askCopy(id)
	if !ok || t.DoneAt != nil || t.Supervision.state(t) != reviewUserCheck ||
		!askOK || decideAsk(&asked) || asked.Conclusion || asked.AskedAt.IsZero() || !asked.AcceptedAt.IsZero() ||
		acceptAskEnded(&asked, t) || acceptAskStale(&asked, t) {
		return "", fmt.Errorf("task %s has no personal verification waiting for the owner (answered, re-asked or closed meanwhile): nothing recorded", id)
	}
	result, err := s.acceptTryAsDismissalLocked(ctx, id, "", asked.AskedAt, false)
	if err != nil {
		return "", err
	}
	if !result.taken {
		return "", fmt.Errorf("task %s has no personal verification waiting for the owner (answered, re-asked or closed meanwhile): nothing recorded", id)
	}
	if st := s.todoDB(); st != nil {
		at, dec := decisionNow(), "dec_"+randomHex(8)
		l := s.botLang(ctx)
		question := cmp.Or(verdictText(l, &asked, currentDelivery(t)), i18n.T(l, "assistant-reports.verdict_verify"))
		if inserted, err := s.insertDecision(ctx, st, db.TodoDecisionRow{
			ID: dec, TodoID: id, Question: question, Choice: decisionText(option), Reason: decisionText(said),
			Source: db.DecisionManual, SourceMachine: machine, SourceSession: self, CreatedAt: at,
		}); err != nil {
			s.logf("task %s: record oral user_check pass decision: %v", id, err)
		} else if inserted {
			s.decisionsChanged(ctx, id, at)
		}
	}
	return fmt.Sprintf("Recorded: the owner passed task %s's personal verification (%q). The task is now accepted, verified, and closed (out of their Needs you).", id, option), nil
}

// decisionTold: the owner chose option label of task id's waiting decision in conversation (machine, session), which
// already passed it on to the task's agent. It ends the decision as chooseOption does (no default at its deadline, the
// to-do's decisions with the owner's words said as the reason, out of needs_user, the ask answered and its cards
// redrawn) but sends nothing. A recurring check's decision still goes to the supervisor, which hears that the Agent
// already has the answer (checkDecided). False: no decision waits; errDecisionOption: no such option.
func (s *Service) decisionTold(ctx context.Context, id, label, machine, session, said string) (bool, error) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	return s.decisionToldLocked(ctx, id, label, machine, session, said)
}

// decisionToldLocked also serves exact owner replies observed under the task's action lock.
func (s *Service) decisionToldLocked(ctx context.Context, id, label, machine, session, said string) (bool, error) {
	a, err := s.askRound(id, "")
	if err != nil || a == nil {
		return false, err
	}
	label = strings.TrimSpace(label)
	if !slices.ContainsFunc(a.Options, func(o DecisionOption) bool { return o.Label == label }) {
		return false, errDecisionOption
	}
	t, _ := s.todoNow(id)
	completeDismissal, err := s.prepareDismissalForAnswer(ctx, t)
	if err != nil {
		return false, err
	}
	s.deadlineAnswered(ctx, id, a.AskedAt)
	if err := s.recordToldDecision(ctx, id, a, label, machine, session, said); err != nil {
		return false, err
	}
	if a.Check != "" { // a recurring check's decision goes to the supervisor, as from its card (#11747)
		s.checkDecided(ctx, t, a, label, true)
	} else {
		s.userDecided(ctx, id)
	}
	chosen := label
	if label == a.Recommend && a.Check == "" {
		chosen = "" // as "OK" takes it; a check's keeps the option, so its card does not say the Agent was not told
	}
	s.endAsk(ctx, id, a.AskedAt, "", chosen)
	completeDismissal()
	return true, nil
}

// recordToldDecision records the owner's exact choice without sending it to the Agent.
func (s *Service) recordToldDecision(ctx context.Context, id string, a *acceptAsk, label, machine, session, said string) error {
	if st := s.todoDB(); st != nil {
		at, dec := decisionNow(), "dec_"+randomHex(8)
		if ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: dec, TodoID: id,
			Question: cmp.Or(a.Reason, i18n.T(s.botLang(ctx), "assistant-reports.verdict_decide")), Choice: decisionText(label),
			Reason: decisionText(said), Source: db.DecisionManual, SourceMachine: machine, SourceSession: session,
			CreatedAt: at}); err != nil {
			return err
		} else if ok {
			s.ownerDecided(id, dec, a.WhyOwner)
			s.decisionsChanged(ctx, id, at)
		}
	}
	return nil
}
