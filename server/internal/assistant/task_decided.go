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
// without sending it again (decisionTold).

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
	var taken bool
	var err error
	if a.ToldAgent {
		taken, err = s.decisionTold(ctx, t.ID, a.Option, machine, self, a.Said)
	} else {
		taken, err = s.chooseOption(ctx, t.ID, "", a.Option)
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
	if !taken {
		return "", fmt.Errorf("task %s has no decision waiting for the owner (answered, re-asked or closed meanwhile): nothing recorded", t.ID)
	}
	if a.ToldAgent {
		return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision. The decision is answered (out of their Needs you); nothing was sent to its agent again.", a.Option, t.ID), nil
	}
	return fmt.Sprintf("Recorded: the owner chose %q for task %s's decision, and it went to the task's agent as their answer on the card would.", a.Option, t.ID), nil
}

// decisionTold: the owner chose option label of task id's waiting decision in conversation (machine, session), which
// already passed it on to the task's agent. It ends the decision as chooseOption does (no default at its deadline, the
// to-do's decisions with the owner's words said as the reason, out of needs_user, the ask answered and its cards
// redrawn) but sends nothing. False: no decision waits; errDecisionOption: no such option.
func (s *Service) decisionTold(ctx context.Context, id, label, machine, session, said string) (bool, error) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	if t, ok := s.todoNow(id); ok {
		if err := s.undoDismissalForAnswer(ctx, t); err != nil { // an answer beats Don't handle
			return false, err
		}
	}
	a, err := s.askRound(id, "")
	if err != nil || a == nil {
		return false, err
	}
	label = strings.TrimSpace(label)
	if !slices.ContainsFunc(a.Options, func(o DecisionOption) bool { return o.Label == label }) {
		return false, errDecisionOption
	}
	s.deadlineAnswered(ctx, id, a.AskedAt)
	if st := s.todoDB(); st != nil {
		at, dec := decisionNow(), "dec_"+randomHex(8)
		if ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: dec, TodoID: id,
			Question: cmp.Or(a.Reason, i18n.T(s.botLang(ctx), "assistant-reports.verdict_decide")), Choice: decisionText(label),
			Reason: decisionText(said), Source: db.DecisionManual, SourceMachine: machine, SourceSession: session,
			CreatedAt: at}); err != nil {
			s.logf("task %s: decision: %v", id, err)
		} else if ok {
			s.ownerDecided(id, dec, a.WhyOwner)
			s.decisionsChanged(ctx, id, at)
		}
	}
	s.userDecided(ctx, id)
	chosen := label
	if label == a.Recommend {
		chosen = "" // as "OK" takes it
	}
	s.endAsk(ctx, id, a.AskedAt, "", chosen)
	return true, nil
}
