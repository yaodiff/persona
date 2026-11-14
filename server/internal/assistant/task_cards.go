package assistant

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// TaskCard is a stable rendering slot for a task's plan, progress and completion.
// The feed remains an immutable audit log. These are upserts for the events in
// this response, not a replacement list; an empty page never deletes a card.
type TaskCard struct {
	ID        string    `json:"id"`
	TodoID    string    `json:"todo_id,omitempty"`
	PlanID    string    `json:"plan_id,omitempty"`
	Index     *int      `json:"index,omitempty"`
	At        time.Time `json:"at"`
	UpdatedAt time.Time `json:"updated_at"`
	State     string    `json:"state"`
	// Reason is why the card is in its current state when the hub knows: an admission-gate queue's waiting reason.
	// Clients that do not render it yet simply ignore the field.
	Reason    string `json:"reason,omitempty"`
	ReceiptID string `json:"receipt_id,omitempty"`
	ReportID  string `json:"report_id,omitempty"`
}

// taskCards projects only task-related feed events. A since page may not contain
// the original plan, so resolve its durable todo binding in one batch rather than
// giving a later receipt another identity. Failed reads leave the projection null
// and preserve the existing feed response and the client's last known cards.
func (s *Service) taskCards(ctx context.Context, items []FeedItem, feedPlans map[string]*TaskPlan) ([]TaskCard, error) {
	plans := make(map[string]*TaskPlan, len(feedPlans))
	for id, p := range feedPlans {
		plans[id] = p
	}
	planRefs, todoRefs := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		switch it.Kind {
		case feedPlan:
			planRefs[it.Ref] = true
		case feedTask, feedReceipt, feedReport, feedReview:
			todoRefs[it.Ref] = true
		}
	}
	if len(planRefs)+len(todoRefs) == 0 {
		return []TaskCard{}, nil
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	todos := map[string]Todo{}
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		if todoRefs[id] || planRefs[t.PlanID] {
			todos[id] = copyTodoLocked(t)
		}
	}
	s.todos.mu.RUnlock()
	for _, t := range todos {
		if t.PlanID != "" {
			planRefs[t.PlanID] = true
		}
	}
	var missing []string
	for id := range planRefs {
		if plans[id] == nil && !feedHasPlan(items, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if st := s.store(); st != nil {
			rows, err := st.TaskPlansByID(ctx, missing)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				plans[row.ID] = planFromRow(row)
			}
		} else {
			s.tasks.mu.Lock()
			for _, id := range missing {
				if p := s.tasks.plans[id]; p != nil {
					cp := *p
					cp.Tasks = append([]PlanTask(nil), p.Tasks...)
					plans[id] = &cp
				}
			}
			s.tasks.mu.Unlock()
		}
	}

	// Read each machine once, not once per card or linked Agent. A feed page may
	// contain hundreds of events from the same computer.
	machines := map[string]map[string]proto.Session{}
	var machineErr error
	agentsFor := func(t Todo) []proto.Session {
		var agents []proto.Session
		for _, a := range t.Agents {
			if a.ReleasedAt != nil || a.DismissedAt != nil {
				continue
			}
			list, known := machines[a.MachineID]
			if !known {
				list = map[string]proto.Session{}
				rows, err := s.sessions(ctx, a.MachineID)
				if err != nil {
					machineErr = err
				}
				for _, row := range rows {
					list[row.SessionID] = row
				}
				machines[a.MachineID] = list
			}
			if row, ok := list[a.SessionID]; ok {
				agents = append(agents, row)
			}
		}
		return agents
	}
	cards := map[string]TaskCard{}
	for id := range planRefs {
		p := plans[id]
		if p == nil {
			// Do not invent a new todo-only identity after losing its plan.
			continue
		}
		for index, task := range p.Tasks {
			if !feedHasPlan(items, id) && !todoRefs[task.TodoID] {
				continue
			}
			if t, ok := todos[task.TodoID]; ok && t.PlanID != "" && t.PlanID != id {
				// A reused task belongs to its current plan, not both old and new slots.
				continue
			}
			card := TaskCard{ID: fmt.Sprintf("plan:%s:%d", id, index), PlanID: id, Index: &index,
				TodoID: task.TodoID, At: p.CreatedAt, UpdatedAt: p.UpdatedAt, State: "not_started"}
			if p.State == planDismissed {
				card.State = "cancelled"
			} else if p.State == planStarted {
				card.State = "working"
			}
			if task.Error != "" {
				card.State = "failed"
			}
			if t, ok := todos[task.TodoID]; ok {
				taskCardState(&card, t, agentsFor(t))
			}
			// A task parked by the admission gate has no agent yet, so the todo alone reads "not started": show it as
			// waiting (the hub will retry it) with the gate's reason (#6909).
			if task.Queue != nil {
				card.State = "waiting"
				card.Reason = i18n.T(s.lang(ctx), "assistant-tasks.admission_queued", i18n.Args{
					"reason": task.Queue.Reason})
				card.UpdatedAt = task.Queue.NextAt
			}
			cards[card.ID] = card
		}
	}
	for id := range todoRefs {
		t, ok := todos[id]
		if !ok || t.PlanID != "" {
			continue
		}
		card := TaskCard{ID: "todo:" + id, TodoID: id, At: t.CreatedAt, UpdatedAt: t.UpdatedAt}
		taskCardState(&card, t, agentsFor(t))
		cards[card.ID] = card
	}
	if machineErr != nil {
		return nil, machineErr
	}
	out := make([]TaskCard, 0, len(cards))
	for _, card := range cards {
		var receiptAt, reportAt time.Time
		for _, it := range items {
			if it.Ref != card.TodoID || card.TodoID == "" {
				continue
			}
			switch it.Kind {
			case feedReceipt:
				if card.ReceiptID == "" || it.At.After(receiptAt) || it.At.Equal(receiptAt) && it.ID > card.ReceiptID {
					card.ReceiptID, receiptAt = it.ID, it.At
				}
			case feedReport:
				if card.ReportID == "" || it.At.After(reportAt) || it.At.Equal(reportAt) && it.ID > card.ReportID {
					card.ReportID, reportAt = it.ID, it.At
				}
			}
			if it.At.After(card.UpdatedAt) {
				card.UpdatedAt = it.At
			}
		}
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].ID < out[j].ID
		}
		return out[i].At.Before(out[j].At)
	})
	return out, nil
}

func feedHasPlan(items []FeedItem, id string) bool {
	for _, it := range items {
		if it.Kind == feedPlan && it.Ref == id {
			return true
		}
	}
	return false
}

func taskCardState(card *TaskCard, t Todo, agents []proto.Session) {
	card.State = taskState(t, agents)
	if t.DoneAt != nil && t.Supervision != nil && t.Supervision.state(t) == reviewAccepted && !t.Cancelled() {
		card.State = "accepted"
	}
	if t.UpdatedAt.After(card.UpdatedAt) {
		card.UpdatedAt = t.UpdatedAt
	}
	if v := t.Supervision; v != nil {
		if v.UpdatedAt.After(card.UpdatedAt) {
			card.UpdatedAt = v.UpdatedAt
		}
		if v.Progress != nil && v.Progress.At.After(card.UpdatedAt) {
			card.UpdatedAt = v.Progress.At
		}
	}
	for _, agent := range agents {
		if agent.UpdatedAt.After(card.UpdatedAt) {
			card.UpdatedAt = agent.UpdatedAt
		}
	}
}
