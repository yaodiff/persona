package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/agentprompt"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A todo's decision record (#1618, docs/assistant/todos.md "Decision records"): the calls the user made on it, written by hand in the
// todo or from their answers to the question cards of the agents working on it (interaction.resolved), and handed to
// every agent the todo is given to (assistant.todo_handoff).

const decisionTextMax = 4000 // a question, a choice or a reason

var errNoDecisionStore = errors.New("decisions need the hub's database")

func (s *Service) todoDecisionRoutes() {
	// GET /api/v1/todos/{id}/decisions[?all=1]: the decisions in effect, oldest first; all=1 adds the superseded ones.
	s.handle("GET /api/v1/todos/{id}/decisions", func(w http.ResponseWriter, r *http.Request) {
		st, id, ok := s.decisionTodo(w, r)
		if !ok {
			return
		}
		all := r.URL.Query().Get("all")
		list, err := st.ListTodoDecisions(r.Context(), id, all == "1" || all == "true")
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		type decisionView struct {
			db.TodoDecisionRow
			PhaseCompletedAt *time.Time `json:"phase_completed_at,omitempty"`
		}
		todo, hasTodo := s.todoNow(id)
		views := make([]decisionView, 0, len(list))
		var earlierLabel string
		for _, row := range list {
			view := decisionView{TodoDecisionRow: row}
			if at, ok := completionOfDecision(row); ok {
				view.PhaseCompletedAt = &at
				if _, earlier := earlierOutcomeSummary(todo, row); hasTodo && earlier {
					if earlierLabel == "" {
						earlierLabel = i18n.T(s.requestLang(r), "assistant-tasks.earlier_phase_outcome")
					}
					view.Question = earlierLabel
				}
			}
			views = append(views, view)
		}
		apierr.WriteJSON(w, map[string]any{"decisions": views})
	})

	// POST /api/v1/todos/{id}/decisions: write one {question, choice, reason?, supersedes_id?, source?, ref?}. A device
	// writes one by hand (source manual, the default), an outcome, or on a concern the report of its outcome to the owner
	// (source reported: choice is what they were told; question defaults to the concern's title; it closes the concern,
	// concerns.go); an extension (todos.write) writes only outcomes,
	// such as a scheduler's acceptance evidence, never the user's decisions and never superseding one. ref (an
	// extension's, at most decisionRefMax runes, no '#') makes the write idempotent: the same ref and question is
	// written once, and a replay answers inserted false.
	s.handle("POST /api/v1/todos/{id}/decisions", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		st, id, ok := s.decisionTodo(w, r)
		if !ok {
			return
		}
		var in struct {
			Question     string `json:"question"`
			Choice       string `json:"choice"`
			Reason       string `json:"reason"`
			SupersedesID string `json:"supersedes_id"`
			Source       string `json:"source"`
			Ref          string `json:"ref"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(in.Source) == db.DecisionReported {
			s.reportConcernRoute(w, r, id, in.Choice)
			return
		}
		row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: id, Question: decisionText(in.Question),
			Choice: decisionText(in.Choice), Reason: decisionText(in.Reason), Source: db.DecisionManual,
			SupersedesID: strings.TrimSpace(in.SupersedesID), CreatedAt: decisionNow()}
		if err := decisionOrigin(r, &row, strings.TrimSpace(in.Source), strings.TrimSpace(in.Ref)); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if ownerFindingDropRequest(r) && row.Source == db.DecisionManual && row.SourceMachine == "" &&
			strings.HasPrefix(row.Question, findingDropQuestionPrefix) && row.Choice == "Drop" {
			// A model can relay an answer and write a manual decision, but this
			// provenance is minted only for a direct owner-app decision.
			row.SourceMessage = findingOwnerDecisionPrefix + row.ID
		}
		if row.Question == "" || row.Choice == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("question and choice cannot be empty"))
			return
		}
		inserted, err := s.insertDecision(r.Context(), st, row)
		if err != nil {
			decisionErr(w, err)
			return
		}
		if inserted {
			s.decisionsChanged(r.Context(), id, row.CreatedAt)
		}
		apierr.WriteJSON(w, map[string]any{"decision": row, "inserted": inserted})
	})

	// PATCH /api/v1/todos/{id}/decisions/{did}: {supersedes_id}: mark it as replacing an earlier decision ("" undoes it).
	s.handle("PATCH /api/v1/todos/{id}/decisions/{did}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		st, id, ok := s.decisionTodo(w, r)
		if !ok {
			return
		}
		var in struct {
			SupersedesID *string `json:"supersedes_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in.SupersedesID == nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("supersedes_id is required"))
			return
		}
		at := decisionNow()
		row, err := st.SetTodoDecisionSupersedes(r.Context(), id, r.PathValue("did"), strings.TrimSpace(*in.SupersedesID), at)
		if err != nil {
			decisionErr(w, err)
			return
		}
		s.decisionsChanged(r.Context(), id, at)
		apierr.WriteJSON(w, map[string]any{"decision": row})
	})
}

// reportConcernRoute is POST …/decisions {source: "reported", choice}: a device writes the report of concern id's
// outcome, which closes it. Only a device (an extension never reports to the owner for the main conversation).
func (s *Service) reportConcernRoute(w http.ResponseWriter, r *http.Request, id, choice string) {
	if r.Header.Get(RoleHeader) == roleExtension {
		apierr.Write(w, http.StatusBadRequest, errors.New("an extension writes outcomes only"))
		return
	}
	t, closed, err := s.reportConcern(r.Context(), id, choice, "", "")
	switch {
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, db.ErrNotConcern):
		apierr.Write(w, http.StatusBadRequest, errors.New("source reported is for a concern only"))
	case errors.Is(err, errEmptyReport):
		apierr.Write(w, http.StatusBadRequest, err)
	case err != nil:
		apierr.Write(w, http.StatusInternalServerError, err)
	default:
		apierr.WriteJSON(w, map[string]any{"todo": t, "closed": closed})
	}
}

// decisionTodo is the store and the request's todo id once the todos are loaded and it is one of them; otherwise it
// answers.
func (s *Service) decisionTodo(w http.ResponseWriter, r *http.Request) (todoStore, string, bool) {
	st := s.todoDB()
	if st == nil {
		apierr.Write(w, http.StatusServiceUnavailable, errNoDecisionStore)
		return nil, "", false
	}
	if err := s.ensureTodosLoaded(r.Context()); err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
		return nil, "", false
	}
	id := r.PathValue("id")
	if _, ok := s.todoNow(id); !ok {
		apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
		return nil, "", false
	}
	return st, id, true
}

func decisionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTodoNotFound), errors.Is(err, db.ErrDecisionNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, db.ErrDecisionSupersede):
		apierr.Write(w, http.StatusBadRequest, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// decisionRefMax bounds an extension's ref.
const decisionRefMax = 200

// ownerFindingDropRequest reports whether r is a validated non-extension owner-app
// request allowed to mint owner-finding-drop provenance.
func ownerFindingDropRequest(r *http.Request) bool {
	return r.Header.Get(RoleHeader) == roleFull &&
		strings.TrimSpace(r.Header.Get(ExtensionHeader)) == "" &&
		r.Header.Get(assistantwire.PersonHeader) == "1"
}

// decisionOrigin sets row's source and provenance from who asks: a device's source is manual (the default) or outcome;
// an extension's is outcome only, its machine "extension:<id>", and with a ref its message "ext:<id>:<ref>" (the key
// that keeps a replay from writing twice). An extension may not supersede.
func decisionOrigin(r *http.Request, row *db.TodoDecisionRow, source, ref string) error {
	ext := strings.TrimSpace(r.Header.Get(ExtensionHeader))
	if r.Header.Get(RoleHeader) == roleExtension {
		if ext == "" {
			return errors.New("extension unknown")
		}
	} else if ext != "" {
		return errors.New("extension identity requires extension role")
	}
	switch {
	case source == "" && ext != "", source == db.DecisionOutcome:
		row.Source = db.DecisionOutcome
	case source == "" || source == db.DecisionManual:
		if ext != "" {
			return errors.New("an extension writes outcomes only")
		}
	default:
		return errors.New("source must be manual or outcome")
	}
	if ext == "" {
		if ref != "" {
			return errors.New("ref is an extension's")
		}
		return nil
	}
	if row.SupersedesID != "" {
		return errors.New("an extension cannot supersede a decision")
	}
	if strings.Contains(ref, "#") || utf8.RuneCountInString(ref) > decisionRefMax {
		return fmt.Errorf("ref must be at most %d characters without '#'", decisionRefMax)
	}
	row.SourceMachine = "extension:" + ext
	if ref != "" {
		row.SourceMessage = "ext:" + ext + ":" + ref
	}
	return nil
}

// decisionNow is now as Postgres keeps it (microseconds), so the order checks compare what was stored.
func decisionNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// decisionText is a decision's text as kept: trimmed, at most decisionTextMax runes.
func decisionText(s string) string { return clip(strings.TrimSpace(s), decisionTextMax) }

// cardDecisions: the user answered a question card (interaction.resolved); when its agent works on a todo, each
// answered question is a decision of that todo. Only Claude Code's option questions (AskUserQuestion) count: approvals
// and permissions are not decisions about the work, and a dismissed or failed card answered nothing.
func (s *Service) cardDecisions(ctx context.Context, ix proto.Interaction, r proto.Resolution, status string) {
	if err := s.ledgerCardDecisions(ctx, ix, r, status); err != nil {
		s.logf("entries: card answer could not be recorded: %v", err)
	}
	st := s.todoDB()
	if st == nil || status != "resolved" || r.Dismiss || ix.Kind != proto.IxQuestion || len(r.Answers) == 0 {
		return
	}
	if r.AnsweredBy != "" || r.Late {
		// Not one the user made on the card: the assistant's call for them (delegate.go; "Change answer" is theirs), a
		// recommendation taken at a deadline, the terminal's, or an answer that came after the card expired.
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("decisions: card %s: todos not loaded: %v", ix.ID, err)
		return
	}
	s.todos.mu.RLock()
	todoID := s.todos.activeBySession[[2]string{ix.MachineID, ix.SessionID}]
	s.todos.mu.RUnlock()
	if todoID == "" {
		return
	}
	at := decisionNow()
	wrote, last := false, at
	for i, a := range proto.QuestionAnswers(ix, r) {
		q, c := decisionText(a.Question), decisionText(a.Answer)
		if q == "" || c == "" {
			continue
		}
		// A microsecond apart, so they list in the card's order (the list orders by time, then by the random id).
		at := at.Add(time.Duration(i) * time.Microsecond)
		ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todoID,
			Question: q, Choice: c, Source: db.DecisionCard, SourceMachine: ix.MachineID, SourceSession: ix.SessionID,
			SourceMessage: ix.ID, CreatedAt: at})
		if err != nil {
			s.logf("decisions: todo %s, card %s: %v", todoID, ix.ID, err)
			return
		}
		if ok {
			wrote, last = true, at
		}
	}
	if wrote {
		s.decisionsChanged(ctx, todoID, last)
	}
}

// agentDecisionTool runs todo_decisions / todo_decision_add for agent machine/session (#5279): the decisions of the
// open todo it works on, read or noted by it. The todo is the agent's own (activeBySession), never one it names:
// an agent cannot read or write another todo's decisions.
func (s *Service) agentDecisionTool(ctx context.Context, machine, session, name string, args json.RawMessage, now time.Time) (string, error) {
	if session == "" { // an older node names no session for these tools
		return "", errors.New("Persona could not tell which agent session is calling (the node on this machine may be older than the hub)")
	}
	st := s.todoDB()
	if st == nil {
		return "", errNoDecisionStore
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", errTodosUnavailable
	}
	t, ok := s.openTodoOfAgent(machine, session)
	if !ok {
		return "You are not working on an open todo: there are no decisions to read or note.", nil
	}
	if name == "todo_decisions" {
		list, err := st.ListTodoDecisions(ctx, t.ID, false)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return fmt.Sprintf("Todo %s (%q) has no decisions yet.", t.ID, t.Title), nil
		}
		b, _ := json.Marshal(map[string]any{"todo": map[string]string{"id": t.ID, "title": t.Title}, "decisions": agentDecisionViews(t, list)})
		return string(b), nil
	}
	var a struct {
		Question     string `json:"question"`
		Choice       string `json:"choice"`
		Reason       string `json:"reason"`
		SupersedesID string `json:"supersedes_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: t.ID, Question: decisionText(a.Question),
		Choice: decisionText(a.Choice), Reason: decisionText(a.Reason), Source: db.DecisionAgent, SourceMachine: machine,
		SourceSession: session, SupersedesID: strings.TrimSpace(a.SupersedesID), CreatedAt: now.UTC().Truncate(time.Microsecond)}
	if row.Question == "" || row.Choice == "" {
		return "", errors.New("question and choice cannot be empty")
	}
	if row.SupersedesID != "" {
		// The user's calls are theirs to change: an agent replacing one would drop it from what every later agent reads.
		all, err := st.ListTodoDecisions(ctx, t.ID, true)
		if err != nil {
			return "", err
		}
		i := slices.IndexFunc(all, func(d db.TodoDecisionRow) bool { return d.ID == row.SupersedesID })
		if i < 0 {
			return "", errors.New("supersedes_id must be an earlier decision of this todo (see todo_decisions)")
		}
		if src := all[i].Source; src != db.DecisionAgent && src != db.DecisionOutcome {
			return "", errors.New("that decision is the user's: ask them instead of replacing it")
		}
	}
	if !s.noteRate.allow(machine, session, now) {
		return "", fmt.Errorf("too many decisions: at most %d in %d minutes; note only the calls that change the work",
			reportLimit, int(reportWindow.Minutes()))
	}
	if _, err := s.insertDecision(ctx, st, row); err != nil {
		if errors.Is(err, db.ErrDecisionSupersede) {
			return "", errors.New("supersedes_id must be an earlier decision of this todo (see todo_decisions)")
		}
		return "", err
	}
	s.decisionsChanged(ctx, t.ID, row.CreatedAt)
	s.logf("todo %s: %s noted decision %s", t.ID, short(session), row.ID)
	return fmt.Sprintf("Noted on todo %s as decision %s (yours, not the user's: it shows as noted by an agent).", t.ID, row.ID), nil
}

// agentDecisionView is a decision as todo_decisions gives it to an agent: who made the call, in words.
type agentDecisionView struct {
	ID               string     `json:"id"`
	Question         string     `json:"question"`
	Choice           string     `json:"choice"`
	Reason           string     `json:"reason,omitempty"`
	By               string     `json:"by"` // user / agent
	Supersedes       string     `json:"supersedes,omitempty"`
	At               string     `json:"at"`
	PhaseCompletedAt *time.Time `json:"phase_completed_at,omitempty"`
	SourceSession    string     `json:"source_session,omitempty"`
	Historical       bool       `json:"historical,omitempty"`
}

func agentDecisionViews(todo Todo, list []db.TodoDecisionRow) []agentDecisionView {
	out := make([]agentDecisionView, 0, len(list))
	for _, r := range list {
		by := "user"
		if r.Source == db.DecisionAgent || r.Source == db.DecisionOutcome {
			by = "agent"
		}
		view := agentDecisionView{ID: r.ID, Question: r.Question, Choice: r.Choice, Reason: r.Reason, By: by,
			Supersedes: r.SupersedesID, At: r.CreatedAt.UTC().Format(time.RFC3339)}
		if at, ok := completionOfDecision(r); ok {
			view.PhaseCompletedAt, view.SourceSession = &at, r.SourceSession
			_, view.Historical = earlierPhase(todo, r)
		}
		out = append(out, view)
	}
	return out
}

// decisionsChanged: the store bumped the todo's updated_at with its decisions; the kept todo follows, and the apps
// refetch (they reload a todo's decisions when its updated_at moves).
func (s *Service) decisionsChanged(ctx context.Context, todoID string, at time.Time) {
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[todoID]; ok && at.After(t.UpdatedAt) {
		t.UpdatedAt = at
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
}

// todoDecisionsContext is the context block of one todo's decisions in effect, for supervision and card delegation;
// empty when it has none or they cannot be read.
func (s *Service) todoDecisionsContext(ctx context.Context, todoID string) string {
	return s.renderTodoDecisionsContext(ctx, todoID, []string{todoID})
}

// todoHandoffDecisionsContext also carries the decisions of the todo's actual continuation ancestors. Their records
// stay historical context: no decision supersedes one on another todo, and none creates an authorization.
func (s *Service) todoHandoffDecisionsContext(ctx context.Context, todoID string) string {
	return s.renderTodoDecisionsContext(ctx, todoID, s.continuationTodos(todoID))
}

// renderTodoDecisionsContext renders the effective decisions of ids (oldest first), globally in time order. A read
// failure omits only that todo's decisions and is logged; the handoff still goes on with the rest.
func (s *Service) renderTodoDecisionsContext(ctx context.Context, todoID string, ids []string) string {
	st := s.todoDB()
	if st == nil || todoID == "" {
		return ""
	}
	var list []db.TodoDecisionRow
	for _, id := range ids {
		rows, err := st.ListTodoDecisions(ctx, id, false)
		if err != nil {
			s.logf("decisions: todo %s: %v", id, err)
			continue
		}
		list = append(list, rows...)
	}
	if len(list) == 0 {
		return ""
	}
	slices.SortFunc(list, func(a, b db.TodoDecisionRow) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	todos := map[string]Todo{}
	agents := map[[2]string]string{}
	for _, id := range ids {
		if t, ok := s.todoNow(id); ok {
			todos[id] = t
			for _, a := range t.Agents {
				agents[[2]string{a.MachineID, a.SessionID}] = a.Title
			}
		}
	}
	loc := s.location(ctx)
	d := prompts.TodoDecisions{Language: answerLanguage(s.hello(ctx).Locale), Title: contextText(todos[todoID].Title)}
	for i, r := range list {
		var source, asker, ancestor string
		switch r.Source {
		case db.DecisionOutcome: // todo_outcome.go: pulled out of a reply, the user did not make it
			source = prompts.TodoDecisionOutcome
			if todo, ok := todos[r.TodoID]; ok {
				if _, earlier := earlierPhase(todo, r); earlier {
					source = prompts.TodoDecisionEarlierOutcome
				}
			}
		case db.DecisionAgent: // todo_decision_add, the agent's own call
			source = prompts.TodoDecisionAgent
		case db.DecisionThread:
			source = prompts.TodoDecisionThread
		case db.DecisionCard:
			source = prompts.TodoDecisionCard
			asker = contextText(oneLine(strings.TrimSpace(agents[[2]string{r.SourceMachine, r.SourceSession}])))
		}
		if r.TodoID != todoID {
			ancestor = strings.TrimSpace(todos[r.TodoID].Title)
			if ancestor == "" {
				ancestor = r.TodoID
			}
			ancestor = contextText(oneLine(ancestor))
		}
		d.Decisions = append(d.Decisions, prompts.TodoDecision{N: i + 1, Question: contextText(r.Question),
			Choice: contextText(r.Choice), Reason: contextText(r.Reason), When: r.CreatedAt.In(loc).Format("01-02 15:04"),
			Source: source, Asker: asker, Ancestor: ancestor})
	}
	return prompts.RenderTodoDecisions(d)
}

// contextText keeps a text from closing the context block it is put in (prompts.ContextBody), and indents its later lines under the item, so a text cannot pass for a
// decision of its own (a "\n2. Chose: …" line).
func contextText(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(prompts.ContextBody(s)), "\n", "\n      ")
}

// withTodoDecisions puts the todo's and its continuation ancestors' decisions before text handed to an agent;
// text as it is without any.
func (s *Service) withTodoDecisions(ctx context.Context, todoID, text string) string {
	return s.withTodoDecisionsForAgent(ctx, todoID, "", text)
}

// withTodoDecisionsForAgent is withTodoDecisions for a known agent kind. An agy workflow
// command keeps its body at byte 0 and receives the decisions after it.
func (s *Service) withTodoDecisionsForAgent(ctx context.Context, todoID, agent, text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	if c := s.todoHandoffDecisionsContext(ctx, todoID); c != "" {
		return agentprompt.WithContext(agent, text, c)
	}
	return text
}
