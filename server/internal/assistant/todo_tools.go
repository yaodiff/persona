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
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's tools on Persona's todos (#1683, docs/assistant/todos.md "The assistant's todo tools"): list, read,
// create, change, complete, reopen and delete them, as the apps do through /api/v1/todos. Deleting a to-do is confirmed
// on the phone every time, and reopening a task or a to-do with Agents is confirmed too. Completing a to-do asks on the
// phone only when it has a real side effect: crossing conversation/supervisor ownership or stopping/closing a live or
// hanging linked Agent (tasks_close.go). Plain to-dos and same-session to-dos with no active Agents complete directly.
// A health conversation still asks before anything leaves its space (confirmOutbound). Plain todos only: work for an
// agent still goes through task_plan.

// The tools' descriptions and arguments are in prompts/mcp/todo (todo_list … todo_delete; tools.go lists them).

func (s *Service) toolTodo(ctx context.Context, machine, self, name string, args json.RawMessage) (string, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", fmt.Errorf("the todos can't be read now (%v): try again later", err)
	}
	switch name {
	case "todo_list":
		return s.toolTodoList(ctx, args)
	case "todo_get":
		return s.toolTodoGet(ctx, machine, self, args)
	case "todo_create":
		return s.toolTodoCreate(ctx, machine, self, args)
	case "todo_update":
		return s.toolTodoUpdate(ctx, machine, self, args)
	case "todo_complete", "todo_reopen":
		return s.toolTodoDone(ctx, machine, self, name, args)
	case "todo_delete":
		return s.toolTodoDelete(ctx, machine, self, args)
	case "todo_split":
		return s.toolTodoSplit(ctx, args)
	case "todo_merge":
		return s.toolTodoMerge(ctx, args)
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

// ---- reading ----

func (s *Service) toolTodoList(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Status     string `json:"status"`
		DueBefore  string `json:"due_before"`
		DueAfter   string `json:"due_after"`
		Overdue    bool   `json:"overdue"`
		WithAgents *bool  `json:"with_agents"`
		Query      string `json:"query"`
		Project    string `json:"project"`
		Kind       string `json:"kind"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	status := cmpOr(strings.ToLower(strings.TrimSpace(a.Status)), "open")
	if status != "open" && status != "done" && status != "all" {
		return "", fmt.Errorf("status must be open, done or all, not %q", a.Status)
	}
	if a.Limit <= 0 {
		a.Limit = 50
	}
	a.Limit = min(a.Limit, 200)
	now, loc := time.Now(), s.location(ctx)
	var before, after time.Time
	if a.DueBefore != "" {
		at, day, err := readToolTime(a.DueBefore, now, loc)
		if err != nil {
			return "", fmt.Errorf("due_before: %w", err)
		}
		before = at
		if day {
			before = at.AddDate(0, 0, 1).Add(-time.Second) // through that day
		}
	}
	if a.DueAfter != "" {
		at, _, err := readToolTime(a.DueAfter, now, loc)
		if err != nil {
			return "", fmt.Errorf("due_after: %w", err)
		}
		after = at
	}
	kind := strings.ToLower(strings.TrimSpace(a.Kind))
	if kind != "" && kind != "todo" && kind != db.KindConcern {
		return "", fmt.Errorf("kind must be todo or concern, not %q", a.Kind)
	}
	q := strings.ToLower(strings.TrimSpace(a.Query))
	project := ""
	if p := strings.TrimSpace(a.Project); p != "" {
		slug, err := s.todoProject(ctx, p)
		if err != nil {
			return "", err
		}
		project = slug
	}

	var list []Todo
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		list = append(list, formatTodo(copyTodoLocked(t)))
	}
	s.todos.mu.RUnlock()
	list = slices.DeleteFunc(list, func(t Todo) bool {
		done := t.DoneAt != nil
		if status == "open" && done && t.StartError == "" || status == "done" && !done {
			return true
		}
		by, hasDue := t.dueBy(loc)
		if (!before.IsZero() || !after.IsZero()) && !hasDue || !before.IsZero() && by.After(before) || !after.IsZero() && by.Before(after) {
			return true
		}
		if a.Overdue && !t.overdue(now, loc) {
			return true
		}
		if a.WithAgents != nil && *a.WithAgents != todoHasAgents(t) {
			return true
		}
		if project != "" && t.Project != project {
			return true
		}
		if kind == db.KindConcern && !t.isConcern() || kind == "todo" && t.isConcern() {
			return true
		}
		return q != "" && !strings.Contains(strings.ToLower(t.Title+"\n"+t.Notes), q)
	})
	sortTodosByDue(list, loc)

	if len(list) == 0 {
		return "No to-dos match.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d to-do(s), times in %s:\n", len(list), loc)
	for i, t := range list {
		if i == a.Limit {
			fmt.Fprintf(&b, "… and %d more: filter, or raise limit (at most 200).\n", len(list)-a.Limit)
			break
		}
		b.WriteString("- " + todoLine(t, now, loc) + "\n")
		if n := oneLine(t.Notes); n != "" {
			b.WriteString("  notes: " + clip(n, 160) + "\n")
		}
	}
	return b.String(), nil
}

func (s *Service) toolTodoGet(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	t, err := s.toolTodoArg(args)
	if err != nil {
		return "", err
	}
	now, loc := time.Now(), s.location(ctx)
	var b strings.Builder
	b.WriteString(todoLine(t, now, loc) + "\n")
	fmt.Fprintf(&b, "created %s, updated %s\n", t.CreatedAt.In(loc).Format(toolTimeLayout), t.UpdatedAt.In(loc).Format(toolTimeLayout))
	if brief, ok := s.briefOfTodo(t.ID); ok {
		fmt.Fprintf(&b, "scope_revision: %s\n", brief.scopeRevision().Format(time.RFC3339Nano))
	}
	if t.failedStart() {
		// Stored done (#1077), but nothing was done: it is open and waits on the owner, as the app shows it (#10365).
		fmt.Fprintf(&b, "did not start %s: not done, it still waits on the owner; task_retry starts it again (optionally with "+
			"another launcher), or task_plan with this todo_id takes it up\n", t.DoneAt.In(loc).Format(toolTimeLayout))
	} else if t.DoneAt != nil {
		fmt.Fprintf(&b, "done %s", t.DoneAt.In(loc).Format(toolTimeLayout))
		if t.DoneBy != "" {
			fmt.Fprintf(&b, " (by %s: %s)", t.DoneBy, clip(oneLine(t.DoneReason), 200))
		}
		b.WriteString("\n")
	}
	if t.StartError != "" {
		fmt.Fprintf(&b, "failed to start: %s\n", clip(oneLine(t.StartError), 200))
	}
	if t.RemindAt != nil && t.RemindedAt != nil {
		fmt.Fprintf(&b, "reminder pushed %s\n", t.RemindedAt.In(loc).Format(toolTimeLayout))
	}
	if t.isConcern() {
		fmt.Fprintf(&b, "a concern the user raised in conversation %s: it closes only with concern_report\n", t.Origin)
	} else if t.Origin != "" {
		fmt.Fprintf(&b, "a task from conversation %s", t.Origin)
		if t.PlanID != "" {
			fmt.Fprintf(&b, " (plan %s)", t.PlanID)
		}
		b.WriteString("\n")
	}
	if t.Stage != "" || t.PRURL != "" {
		if _, canonical := s.store().(todoRepositoryStore); canonical {
			fmt.Fprintf(&b, "stage %s\n", cmpOr(t.Stage, "-"))
		} else {
			fmt.Fprintf(&b, "stage %s %s\n", cmpOr(t.Stage, "-"), t.PRURL)
		}
	}
	if strings.TrimSpace(t.Notes) != "" {
		b.WriteString("notes:\n" + clip(strings.TrimSpace(t.Notes), 4000) + "\n")
	}
	if t.Supervision != nil {
		b.WriteString("done_when (current reports, not a task acceptance):\n")
		for i, c := range t.Supervision.Criteria {
			fmt.Fprintf(&b, "%d. [%s] %s", i+1, c.State, c.Criterion)
			if c.By != "" {
				fmt.Fprintf(&b, " — %s", c.By)
			}
			if c.At != nil {
				fmt.Fprintf(&b, " at %s", c.At.In(loc).Format(toolTimeLayout))
			}
			if c.Evidence != "" {
				fmt.Fprintf(&b, "\n   %s", c.Evidence)
			}
			b.WriteString("\n")
		}
	}
	if t.Responsible != nil {
		fmt.Fprintf(&b, "responsible: %s %q", t.Responsible.Kind, clip(oneLine(t.Responsible.Name), 120))
		if hints := formatResponsibleHints(t.Responsible); hints != "" {
			fmt.Fprintf(&b, " (%s)", hints)
		}
		b.WriteString("\n")
	}
	if len(t.Agents) > 0 {
		b.WriteString("agents:\n")
		for _, ag := range t.Agents {
			state := "no longer works on it"
			if ag.ReleasedAt == nil {
				state = "works on it"
				if c, ok := s.session(ctx, ag.MachineID, ag.SessionID); ok && c.Status != "" {
					state += ", " + string(c.Status)
				}
			}
			fmt.Fprintf(&b, "- %s/%s %q %s\n", ag.MachineID, ag.SessionID, clip(oneLine(ag.Title), 80), state)
		}
	}
	if a, ok := s.askCopy(t.ID); ok && decisionOpen(&a, t) && !acceptAskEnded(&a, t) &&
		a.RecheckAt.IsZero() && !ownerDecisionReturned(t.Supervision) {
		if dismissed, err := s.decisionDismissed(ctx, t); err == nil && !dismissed {
			fmt.Fprintf(&b, "pending user decision (needs_user, asked_at: %s): %s\n", a.AskedAt.UTC().Format(time.RFC3339Nano), a.Reason)
			if a.Recommend != "" {
				fmt.Fprintf(&b, "  recommended: %s\n", a.Recommend)
			}
			for _, o := range a.Options {
				fmt.Fprintf(&b, "  - %s: %s\n", o.Label, o.LeadsTo)
			}
		}
	}
	if st := s.todoDB(); st != nil {
		if ds, err := st.ListTodoDecisions(ctx, t.ID, false); err == nil && len(ds) > 0 {
			b.WriteString("decisions in force:\n")
			for _, d := range ds {
				question := d.Question
				if _, earlier := earlierOutcomeSummary(t, d); earlier {
					question = "Earlier phase outcome"
				}
				fmt.Fprintf(&b, "- %s → %s", clip(oneLine(question), 160), clip(oneLine(d.Choice), 160))
				if at, ok := completionOfDecision(d); ok {
					_, earlier := earlierPhase(t, d)
					phase := "current completion"
					if earlier {
						phase = "earlier phase"
					}
					fmt.Fprintf(&b, " [%s: %s; %s/%s]", phase, at.In(loc).Format(toolTimeLayout), d.SourceMachine, d.SourceSession)
				}
				if r := oneLine(d.Reason); r != "" {
					fmt.Fprintf(&b, " (%s)", clip(r, 160))
				}
				b.WriteString("\n")
			}
		}
	}
	if _, ok := s.store().(todoRepositoryStore); ok {
		set, err := s.readTodoRepositoriesForTool(ctx, machine, self, t.ID)
		if err != nil {
			return "", err
		}
		b.WriteString(formatTodoRepositoryContext(set))
	}
	return b.String(), nil
}

// ---- writing ----

func (s *Service) toolTodoCreate(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a struct {
		Title              string          `json:"title"`
		Notes              string          `json:"notes"`
		Due                string          `json:"due"`
		Remind             string          `json:"remind"`
		Priority           string          `json:"priority"`
		Project            string          `json:"project"`
		Kind               string          `json:"kind"`
		Audience           string          `json:"audience"`
		DirectionID        string          `json:"direction_id"`
		ItemType           string          `json:"item_type"`
		PartOf             json.RawMessage `json:"part_of"`
		After              json.RawMessage `json:"after"`
		BlockedByPR        []string        `json:"blocked_by_pr"`
		UnlockStage        string          `json:"unlock_stage"`
		UnlockContracts    []string        `json:"unlock_contracts"`
		RepositoryBindings json.RawMessage `json:"repository_bindings"`
		TopicMessageID     string          `json:"topic_message_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		return "", errors.New("title is required")
	}
	if a.TopicMessageID != "" {
		if a.Due != "" || a.Remind != "" || a.Priority != "" || a.Project != "" || a.Kind != "" ||
			a.Audience != "" || a.DirectionID != "" || a.PartOf != nil || a.After != nil || a.RepositoryBindings != nil {
			return "", errors.New("creating a Lark topic to-do takes only title, notes and topic_message_id")
		}
		run, ok := s.decidingTopicRun(ctx, machine, self, a.TopicMessageID)
		if !ok {
			return "", errors.New("topic_message_id must identify a pending Lark topic message for this main conversation")
		}
		if ok, outcome, err := s.confirmOutbound(ctx, machine, self, "todo_create", "", title, args); err != nil || !ok {
			return todoNotAllowed(outcome, err)
		}
		return s.larkModule().DecideTopicTodo(ctx, run, title, strings.TrimSpace(a.Notes))
	}
	bindings, err := parseCreateBindings(a.RepositoryBindings)
	if err != nil {
		return "", err
	}
	if err := s.resolveCreateBindings(ctx, bindings); err != nil { // before the store, outside todos.writing
		return "", s.taskRepositoryToolError(ctx, err)
	}
	kind, err := todoKind(a.Kind)
	if err != nil {
		return "", err
	}
	if kind == db.KindConcern && !s.isMain(ctx, self) {
		return "", errConcernMain
	}
	now, loc := time.Now(), s.location(ctx)
	var plan db.TodoEdit
	if err := setToolDue(&plan, a.Due, now, loc); err != nil {
		return "", err
	}
	if err := setToolRemind(&plan, a.Remind, now, loc); err != nil {
		return "", err
	}
	pr, err := todoPriority(a.Priority)
	if err != nil {
		return "", err
	}
	plan.Priority = &pr
	aud, err := todoAudience(a.Audience)
	if err != nil {
		return "", err
	}
	plan.Audience = &aud
	if dir := strings.TrimSpace(a.DirectionID); dir != "" {
		plan.DirectionID = &dir
	}
	var blockedByPR []string
	var unlockStage string
	var unlockContracts []string
	if s.settings(ctx).todosV2On() {
		if len(a.BlockedByPR) > 0 {
			var prErr error
			blockedByPR, prErr = parseBlockedByPRURLs(a.BlockedByPR)
			if prErr != nil {
				return "", prErr
			}
		}
		var unlockErr error
		unlockStage, unlockContracts, unlockErr = parseUnlockStageRule(a.UnlockStage, a.UnlockContracts)
		if unlockErr != nil {
			return "", unlockErr
		}
	}
	if s.settings(ctx).todosV2On() && strings.TrimSpace(a.ItemType) != "" {
		it, err := todoItemType(a.ItemType)
		if err != nil {
			return "", err
		}
		plan.ItemType = &it
	}
	if err := s.setTodoProject(ctx, &a.Project, &plan); err != nil {
		return "", err
	}
	pieces, err := pieceArgs(a.PartOf, a.After)
	if err != nil {
		return "", err
	}
	if pieces.set() && kind == db.KindConcern {
		return "", errors.New("a concern is no piece of a request: leave part_of and after out")
	}
	links, err := s.checkPieces("", pieces)
	if err != nil {
		return "", err
	}
	plan.PartOf, plan.After = links.PartOf, links.After
	if cfg := s.settings(ctx); cfg.todosV2On() && plan.DirectionID == nil {
		if callerConv := strings.TrimSpace(self); callerConv != "" && callerConv != cfg.Main && slices.Contains(cfg.Leaders, callerConv) {
			dir := callerConv
			plan.DirectionID = &dir
		} else if plan.PartOf != nil && *plan.PartOf != "" {
			if parent, ok := s.todoNow(*plan.PartOf); ok {
				if parentDir := strings.TrimSpace(parent.DirectionID); parentDir != "" {
					plan.DirectionID = &parentDir
				}
			}
		}
	}
	if t, ok := s.openTodoLike(title, kind, plan); ok {
		if bindings != nil {
			return "", fmt.Errorf("to-do %s already exists; associations were not changed: read todo_repositories_get and edit with todo_repositories_set", t.ID)
		}
		return "Already there, not created again: " + todoLine(t, now, loc), nil
	}
	if kind == db.KindConcern {
		t, err := s.createConcern(ctx, title, strings.TrimSpace(a.Notes), plan, bindings)
		if err != nil {
			return "", fmt.Errorf("the concern could not be saved, nothing changed: %w", err)
		}
		return "Concern recorded (it stays open until you report its outcome with concern_report): " + todoLine(t, now, loc), nil
	}
	if ok, outcome, err := s.confirmOutbound(ctx, machine, self, "todo_create", "", title, args); err != nil || !ok {
		return todoNotAllowed(outcome, err)
	}
	if s.isLeader(ctx, self) {
		t, err := s.createDirectionTodo(ctx, self, title, strings.TrimSpace(a.Notes), plan, bindings)
		if err != nil {
			return "", fmt.Errorf("the to-do could not be saved, nothing changed: %w", err)
		}
		if err := s.persistExternalPRBlockers(ctx, t.ID, blockedByPR); err != nil {
			s.logf("todo %s: persist external_pr blockers: %v", t.ID, err)
		}
		if len(t.After) > 0 {
			s.syncAfterBlockers(ctx, t.ID, t.After, unlockStage, unlockContracts)
		}
		return "Created under your direction (you supervise it; to have an Agent do it, use task_plan with todo_id " + t.ID +
			"): " + todoLine(t, now, loc) +
			reuseHintText("todo_create", s.activeReuseHints(ctx, self, 3)), nil
	}
	if plan.PartOf != nil && *plan.PartOf != "" && self != "" && s.workerRefusal(ctx, machine, self) == nil {
		// a piece the conversation will hand out itself: its own, so that task_plan's todo_id can run an Agent on it
		t, err := s.createPieceTodo(ctx, self, title, strings.TrimSpace(a.Notes), plan, bindings)
		if err != nil {
			return "", fmt.Errorf("the to-do could not be saved, nothing changed: %w", err)
		}
		if err := s.persistExternalPRBlockers(ctx, t.ID, blockedByPR); err != nil {
			s.logf("todo %s: persist external_pr blockers: %v", t.ID, err)
		}
		if len(t.After) > 0 {
			s.syncAfterBlockers(ctx, t.ID, t.After, unlockStage, unlockContracts)
		}
		return "Created as a piece of " + t.PartOf + " (to have an Agent do it when its turn comes, use task_plan with todo_id " + t.ID +
			"): " + todoLine(t, now, loc), nil
	}
	t, err := s.createPlainTodo(ctx, title, strings.TrimSpace(a.Notes), plan, bindings)
	if err != nil {
		return "", fmt.Errorf("the to-do could not be saved, nothing changed: %w", err)
	}
	if err := s.persistExternalPRBlockers(ctx, t.ID, blockedByPR); err != nil {
		s.logf("todo %s: persist external_pr blockers: %v", t.ID, err)
	}
	if len(t.After) > 0 {
		s.syncAfterBlockers(ctx, t.ID, t.After, unlockStage, unlockContracts)
	}
	return "Created: " + todoLine(t, now, loc) +
		reuseHintText("todo_create", s.activeReuseHints(ctx, self, 3)), nil
}

func (s *Service) toolTodoUpdate(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID          string          `json:"id"`
		Title       json.RawMessage `json:"title"`
		Notes       json.RawMessage `json:"notes"`
		Due         json.RawMessage `json:"due"`
		Remind      json.RawMessage `json:"remind"`
		Priority    json.RawMessage `json:"priority"`
		Project     json.RawMessage `json:"project"`
		Audience    json.RawMessage `json:"audience"`
		DirectionID json.RawMessage `json:"direction_id"`
		ItemType    json.RawMessage `json:"item_type"`
		PartOf      json.RawMessage `json:"part_of"`
		After       json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	t, ok := s.todoNow(strings.TrimSpace(a.ID))
	if !ok {
		return "", fmt.Errorf("no to-do %q: todo_list has their ids", a.ID)
	}
	now, loc := time.Now(), s.location(ctx)
	var e db.TodoEdit
	changed := false
	for _, f := range []struct {
		name string
		raw  json.RawMessage
		set  func(string) error
	}{
		{"title", a.Title, func(v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				return errors.New("title can't be empty")
			}
			e.Title = &v
			return nil
		}},
		{"notes", a.Notes, func(v string) error { v = strings.TrimSpace(v); e.Notes = &v; return nil }},
		{"due", a.Due, func(v string) error { e.SetDue = true; return setToolDue(&e, v, now, loc) }},
		{"remind", a.Remind, func(v string) error { e.SetRemind = true; return setToolRemind(&e, v, now, loc) }},
		{"priority", a.Priority, func(v string) error {
			pr, err := todoPriority(v)
			e.Priority = &pr
			return err
		}},
		{"project", a.Project, func(v string) error { return s.setTodoProject(ctx, &v, &e) }},
		{"audience", a.Audience, func(v string) error {
			aud, err := todoAudience(v)
			e.Audience = &aud
			return err
		}},
		{"direction_id", a.DirectionID, func(v string) error {
			dir := strings.TrimSpace(v)
			e.DirectionID = &dir
			return nil
		}},
	} {
		v, set, err := optionalText(f.raw, f.name)
		if err == nil && set {
			err = f.set(v)
		}
		if err != nil {
			return "", err
		}
		changed = changed || set
	}
	if s.settings(ctx).todosV2On() && a.ItemType != nil {
		v, set, err := optionalText(a.ItemType, "item_type")
		if err != nil {
			return "", err
		}
		if set {
			it, err := todoItemType(v)
			if err != nil {
				return "", err
			}
			e.ItemType = &it
			changed = true
		}
	}
	pieces, err := pieceArgs(a.PartOf, a.After)
	if err != nil {
		return "", err
	}
	if pieces.set() && t.isConcern() {
		return "", errors.New("a concern is no piece of a request: leave part_of and after out")
	}
	links, err := s.checkPieces(t.ID, pieces)
	if err != nil {
		return "", err
	}
	e.PartOf, e.After = links.PartOf, links.After
	changed = changed || !pieces.empty()
	if !changed {
		return "", errors.New("nothing to change: give title, notes, due, remind, priority, project, audience, direction_id, part_of or after")
	}
	if ok, outcome, err := s.confirmOutbound(ctx, machine, self, "todo_update", "", t.Title, args); err != nil || !ok {
		return todoNotAllowed(outcome, err)
	}
	out, err := s.editTodo(ctx, t.ID, e)
	if err != nil {
		return "", fmt.Errorf("the change could not be saved, nothing changed: %w", err)
	}
	if e.After != nil {
		s.syncAfterBlockers(ctx, out.ID, out.After, "", nil)
	}
	return "Updated: " + todoLine(out, now, loc), nil
}

func (s *Service) toolTodoDone(ctx context.Context, machine, self, name string, args json.RawMessage) (string, error) {
	t, err := s.toolTodoArg(args)
	if err != nil {
		return "", err
	}
	complete := name == "todo_complete"
	now, loc := time.Now(), s.location(ctx)
	if complete == (t.DoneAt != nil) {
		return "Nothing to do, it already is: " + todoLine(t, now, loc), nil
	}
	if complete && t.isConcern() {
		return "", fmt.Errorf("%s is a concern: it closes only when you report its outcome to the user with concern_report", t.ID)
	}
	needsConfirm := todoHasWork(t)
	if complete {
		if err := s.taskCloseActivityError(ctx, t, now); err != nil {
			return "", s.taskCloseToolError(ctx, err)
		}
		needsConfirm = s.todoCompleteNeedsConfirm(ctx, t, self)
	}
	if needsConfirm {
		key := "assistant-todos.reopen_card"
		if complete {
			key = "assistant-todos.complete_card"
		}
		said, err := s.confirmTodoCard(ctx, machine, self, name, i18n.T(s.lang(ctx), key, i18n.Args{"title": clip(t.Title, 80)}), args, t.ID)
		if err != nil || said != "" {
			return said, err
		}
	} else if ok, outcome, err := s.confirmOutbound(ctx, machine, self, name, "", t.Title, args); err != nil || !ok {
		return todoNotAllowed(outcome, err)
	}
	out, err := s.editTodoWithCloseActivity(ctx, t.ID, db.TodoEdit{Done: &complete}, complete)
	if err != nil {
		return "", fmt.Errorf("the change could not be saved, nothing changed: %w", s.taskCloseToolError(ctx, err))
	}
	if complete && s.settings(ctx).todosV2On() {
		if recErr := s.ResetSupervisedTaskRecovery(ctx, t.ID); recErr != nil {
			s.logf("todo %s: reset todos_v2 restart recovery: %v", t.ID, recErr)
		}
		if _, blkErr := s.ReconcileTodoBlockers(ctx, t.ID); blkErr != nil {
			s.logf("todo %s: reconcile todos_v2 blockers: %v", t.ID, blkErr)
		}
	}
	if complete {
		return "Completed: " + todoLine(out, now, loc), nil
	}
	return "Reopened: " + todoLine(out, now, loc), nil
}

func (s *Service) toolTodoDelete(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	t, err := s.toolTodoArg(args)
	if err != nil {
		return "", err
	}
	said, err := s.confirmTodoCard(ctx, machine, self, "todo_delete",
		i18n.T(s.lang(ctx), "assistant-todos.delete_card", i18n.Args{"title": clip(t.Title, 80)}), args, t.ID)
	if err != nil || said != "" {
		return said, err
	}
	if err := s.deleteTodo(ctx, t.ID); err != nil {
		if errors.Is(err, db.ErrTodoNotFound) {
			return "It was already gone.", nil
		}
		return "", fmt.Errorf("the to-do could not be deleted, nothing changed: %w", err)
	}
	return fmt.Sprintf("Deleted %s %q.", t.ID, t.Title), nil
}

// confirmTodoCard asks the phone before a todo change worded as summary: "" when the user allows it, else what the
// model is told (nothing changed).
func (s *Service) confirmTodoCard(ctx context.Context, machine, self, tool, summary string, args json.RawMessage, todoID string) (string, error) {
	now := time.Now().UTC()
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, Kind: proto.IxPermission, ToolName: tool,
		Summary: summary, ToolInput: args, SessionTitle: "Persona", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if conv, ok := s.session(ctx, machine, self); ok {
		ix.Cwd = conv.Cwd
	}
	r, err := s.ask(ctx, ix)
	if errors.Is(err, errUserSpoke) {
		return "The user wrote in the conversation instead of confirming: nothing changed.", nil
	}
	if err != nil {
		return "", err
	}
	if tool == "todo_complete" && r.UserDenied() {
		id := todoID
		s.reviewDismissals.actions.acquire(id)
		defer s.reviewDismissals.actions.release(id)
		s.tasks.completionMu.Lock()
		defer s.tasks.completionMu.Unlock()
		if cur, ok := s.todoNow(id); ok && cur.DoneAt == nil {
			if err := s.recordTodoCloseDecline(context.WithoutCancel(ctx), id, time.Now().UTC()); err != nil {
				return "", fmt.Errorf("the user declined and nothing changed, but the decline could not be saved: %w", err)
			}
		}
	}
	if r.Allow == nil || !*r.Allow {
		return outboundNotDone(r.Outcome(), "The user declined on the phone: nothing changed.", "nothing changed"), nil
	}
	return "", nil
}

// todoNotAllowed is what a health conversation is told when a change did not get out of the space (outcome: how the
// card ended, confirmOutbound): the user's decline only when they declined it themselves, else nobody answered.
func todoNotAllowed(outcome string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return outboundNotDone(outcome, "The user did not allow it on the phone: nothing changed.", "nothing changed"), nil
}

// ---- helpers ----

// toolTodoArg is the todo the arguments' id names.
func (s *Service) toolTodoArg(args json.RawMessage) (Todo, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Todo{}, fmt.Errorf("arguments: %w", err)
	}
	t, ok := s.todoNow(strings.TrimSpace(a.ID))
	if !ok {
		return Todo{}, fmt.Errorf("no to-do %q: todo_list has their ids", a.ID)
	}
	return formatTodo(t), nil
}

// openTodoLike is an open todo with this title, the due date of plan and its project: a create retried (or a list of
// to-dos handed in twice) is not made twice.
func (s *Service) openTodoLike(title, kind string, plan db.TodoEdit) (Todo, bool) {
	probe := Todo{}
	probe.setPlan(db.TodoRow{DueAt: plan.DueAt, DueDate: plan.DueDate})
	if plan.Project != nil {
		probe.Project = *plan.Project
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && t.Kind == kind && strings.TrimSpace(t.Title) == title && t.DueDate == probe.DueDate && t.Project == probe.Project &&
			(t.DueAt == nil) == (probe.DueAt == nil) && (t.DueAt == nil || t.DueAt.Equal(*probe.DueAt)) {
			return formatTodo(copyTodoLocked(t)), true
		}
	}
	return Todo{}, false
}

// todoHasAgents: agents work or worked on t (the ones the user dismissed do not count).
func todoHasAgents(t Todo) bool {
	return slices.ContainsFunc(t.Agents, func(a TodoAgent) bool { return a.DismissedAt == nil })
}

// todoHasWork: t is a task or agents work on it, so reopening it still confirms.
func todoHasWork(t Todo) bool {
	return t.Origin != "" || t.Supervision != nil || todoHasAgents(t)
}

// todoCompleteCrossSession reports whether caller session self is completing a to-do owned or supervised
// by another conversation (or by an unknown caller when the to-do has an owner or supervisor).
func todoCompleteCrossSession(t Todo, self string) bool {
	if sup := strings.TrimSpace(supervisorOf(t)); sup != "" {
		return self == "" || self != sup
	}
	if origin := strings.TrimSpace(t.Origin); origin != "" {
		return self == "" || self != origin
	}
	return false
}

// todoCompleteNeedsConfirm reports whether completing t from session self has a real side effect
// requiring owner confirmation: crossing conversation ownership or stopping/closing a live or hanging
// linked Agent. Plain to-dos and same-session to-dos with no active Agents complete directly
// (health-space outbound confirmation still runs via confirmOutbound).
func (s *Service) todoCompleteNeedsConfirm(ctx context.Context, t Todo, self string) bool {
	if todoCompleteCrossSession(t, self) {
		return true
	}
	return s.todoHasActiveAgents(ctx, t)
}

// sortTodosByDue orders todos as the tools list them: open first, then by due moment (none last), priority, newest.
func sortTodosByDue(list []Todo, loc *time.Location) {
	rank := map[string]int{db.PriorityHigh: 0, "": 1, db.PriorityLow: 2}
	slices.SortStableFunc(list, func(a, b Todo) int {
		if (a.DoneAt == nil) != (b.DoneAt == nil) {
			if a.DoneAt == nil {
				return -1
			}
			return 1
		}
		ad, aok := a.dueBy(loc)
		bd, bok := b.dueBy(loc)
		if aok != bok {
			if aok {
				return -1
			}
			return 1
		}
		if c := ad.Compare(bd); aok && c != 0 {
			return c
		}
		if c := rank[a.Priority] - rank[b.Priority]; c != 0 {
			return c
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
}

// toolTimeLayout is how the tools write a moment for the model: the year and the weekday, so neither is guessed.
const toolTimeLayout = "2006-01-02 Mon 15:04"

// todoLine is one todo for the model: id, title, state, priority, due date, reminder and agents.
func todoLine(t Todo, now time.Time, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q [", t.ID, clip(oneLine(t.Title), 120))
	if t.StartError != "" {
		b.WriteString("failed to start")
	} else if t.DoneAt != nil {
		b.WriteString("done")
	} else {
		b.WriteString("open")
	}
	if t.Priority != "" {
		b.WriteString(", " + t.Priority + " priority")
	}
	if t.isConcern() {
		b.WriteString(", concern")
	} else if t.Origin != "" || t.Supervision != nil {
		b.WriteString(", task")
	}
	b.WriteString("]")
	if t.Project != "" {
		fmt.Fprintf(&b, " · project %s", t.Project)
	}
	b.WriteString(pieceLine(t))
	b.WriteString(todoDue(t, now, loc))
	if t.RemindAt != nil {
		fmt.Fprintf(&b, " · remind %s", t.RemindAt.In(loc).Format(toolTimeLayout))
		if t.RemindedAt != nil {
			b.WriteString(" (pushed)")
		}
	}
	active := 0
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			active++
		}
	}
	if active > 0 {
		fmt.Fprintf(&b, " · %d agent(s) on it", active)
	}
	if t.Responsible != nil {
		fmt.Fprintf(&b, " · responsible %s: %s", t.Responsible.Kind, clip(oneLine(t.Responsible.Name), 80))
	}
	return b.String()
}

// todoDue is todoLine's due date, with how far off (or overdue) it is while the todo is open; "" when it has none.
func todoDue(t Todo, now time.Time, loc *time.Location) string {
	var b strings.Builder
	if t.DueAt != nil {
		fmt.Fprintf(&b, " · due %s", t.DueAt.In(loc).Format(toolTimeLayout))
	} else if d, err := time.Parse(dayLayout, t.DueDate); err == nil {
		fmt.Fprintf(&b, " · due %s (the whole day)", d.Format("2006-01-02 Mon"))
	}
	if by, ok := t.dueBy(loc); ok && t.DoneAt == nil {
		if t.overdue(now, loc) {
			fmt.Fprintf(&b, " (overdue, %s)", ago(by))
		} else {
			fmt.Fprintf(&b, " (%s)", until(by))
		}
	}
	return b.String()
}

// readToolTime reads a time the model wrote, in the user's time zone loc: a day "2006-01-02" (its start; day true), a
// moment in RFC 3339 or "2006-01-02 15:04", or 30m / 2h / 1d from now.
func readToolTime(v string, now time.Time, loc *time.Location) (time.Time, bool, error) {
	v = strings.TrimSpace(v)
	if d, err := time.ParseInLocation(dayLayout, v, loc); err == nil {
		return d, true, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", v, loc); err == nil {
		return t, false, nil
	}
	t, err := parseWhen(v, now, loc)
	return t, false, err
}

// setToolDue sets due date v ("" clears it) into e: a day stays a day, a moment a moment.
func setToolDue(e *db.TodoEdit, v string, now time.Time, loc *time.Location) error {
	if strings.TrimSpace(v) == "" {
		e.DueAt, e.DueDate = nil, nil
		return nil
	}
	e.SetDue = true
	at, day, err := readToolTime(v, now, loc)
	if err != nil {
		return fmt.Errorf("due: %w", err)
	}
	if day {
		d := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		e.DueAt, e.DueDate = nil, &d
		return nil
	}
	u := at.UTC()
	e.DueAt, e.DueDate = &u, nil
	return nil
}

// setToolRemind sets reminder v ("" clears it) into e; a moment past by more than a minute is refused (a wrong year or
// time zone, most likely), as is a day without a time.
func setToolRemind(e *db.TodoEdit, v string, now time.Time, loc *time.Location) error {
	if strings.TrimSpace(v) == "" {
		e.RemindAt = nil
		return nil
	}
	e.SetRemind = true
	at, day, err := readToolTime(v, now, loc)
	if err != nil {
		return fmt.Errorf("remind: %w", err)
	}
	if day {
		return fmt.Errorf("remind %q is a day: give the time too (2026-11-12 09:00 or RFC 3339)", v)
	}
	if at.Before(now.Add(-time.Minute)) {
		return fmt.Errorf("remind %s is in the past (now is %s)", at.In(loc).Format(toolTimeLayout), now.In(loc).Format(toolTimeLayout))
	}
	u := at.UTC()
	e.RemindAt = &u
	return nil
}

// optionalText reads an optional string argument: set false when it is left out, "" for null.
func optionalText(raw json.RawMessage, name string) (string, bool, error) {
	if raw == nil {
		return "", false, nil
	}
	if string(raw) == "null" {
		return "", true, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false, fmt.Errorf("%s must be a string (or null to clear it)", name)
	}
	return v, true, nil
}
