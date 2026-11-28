package assistant

import (
	"cmp"
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/deeplink"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The ask a to-do waits on, on the to-do itself (#2056, docs/assistant/todos.md "Waiting for the owner"): the apps list
// the to-dos, not the briefing, and a to-do whose conclusion needs the owner's decision is where they show the
// recommendation and take it with one tap (POST /api/v1/todos/{id}/accept, todo_accept_route.go). It is the briefing's
// `accept[]` row (modules/briefing/feed.go), read from the same book (awaitingAcceptance): a "Please verify" ask, or a supervised
// task's conclusion (conclusion.go). A decision that recommends an option is also pushed with that option, and the
// push's OK takes it the same way (pushDecision).

// todoAsk is a to-do's open ask, as GET /api/v1/todos carries it: the verdict line in the words of its Lark card; outcome a
// conclusion's verdict (done, cannot or decide; none for a deployed task's "Please verify"); recommend a decision's
// recommended option, which OK takes; asked_at the round, which the accept route takes; risk how risky answering it is
// (askRisk), which the view's decision reads; link the direct evidence/PR/MR URL when present and not merely the
// to-do's own /todos/<id> page.
//
// A decision also carries what makes it answerable where it is listed (#3804, decision.go): question, what is to be
// decided; why_owner, why only the owner can decide; options, the recommended first, each with what it leads to;
// cost_of_waiting; from and stands, where the task came from and where it stands, as the hub knows them, and background,
// both in one line in the request's language; deadline, and default: "taken" when the recommendation is taken at the
// deadline, "never" when it never is; responses, the fixed answers the accept route takes besides an option. A
// decision from before #3804 has none of them.
type todoAsk struct {
	Outcome   string    `json:"outcome,omitempty"`
	Verdict   string    `json:"verdict"`
	Summary   string    `json:"summary,omitempty"`
	Recommend string    `json:"recommend,omitempty"`
	Link      string    `json:"link,omitempty"`
	AskedAt   time.Time `json:"asked_at"`
	Risk      string    `json:"risk,omitempty"`

	Terminal       *SessionRef      `json:"resume_terminal,omitempty"`
	TerminalOption string           `json:"terminal_option,omitempty"`
	Question       string           `json:"question,omitempty"`
	WhyOwner       string           `json:"why_owner,omitempty"`
	Options        []DecisionOption `json:"options,omitempty"`
	CostOfWaiting  string           `json:"cost_of_waiting,omitempty"`
	From           *askFrom         `json:"from,omitempty"`
	Stands         string           `json:"stands,omitempty"`
	Background     string           `json:"background,omitempty"`
	Deadline       *time.Time       `json:"deadline,omitempty"`
	Default        string           `json:"default,omitempty"`
	Responses      []string         `json:"responses,omitempty"`
	// Response: the fixed answer the owner already gave it while it still waits (explain_first).
	Response string `json:"response,omitempty"`
	// Suggestion: the owner gave this go-ahead for this directory three times; the decision offers a standing
	// authorization for it (rule_suggest.go, #4929).
	Suggestion *proto.RuleSuggestion `json:"suggestion,omitempty"`
	// ForBriefing: it can wait and nothing was pushed for it yet: the next briefing lists it (decision_wait.go).
	ForBriefing bool `json:"for_briefing,omitempty"`
	// Questions retains an expired ask_user card's original form, including multiple questions/selections.
	Questions []proto.Question `json:"questions,omitempty"`
}

// How a decision's default reads on its to-do (todoAsk.Default).
const (
	defaultTaken = "taken"
	defaultNever = "never"
)

// todoActiveBlockerView is a persistent active blocker edge attached to a listedTodo under todos_v2.
type todoActiveBlockerView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TargetRef string `json:"target_ref"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts,omitempty"`
}

// listedTodo is a to-do as GET /api/v1/todos lists it: with its ask while one waits (none: no `ask`, which a client that
// does not know it never reads), and, while it is open and nobody works on it, why (`orphaned`, tasks_orphan.go); on
// the routes, its view (todo_view_routes.go).
type listedTodo struct {
	Todo
	readinessWaits         []assistantwire.TodoWait
	PrerequisiteBlockers   []string                   `json:"prerequisite_blockers,omitzero"`
	ActiveBlockers         []todoActiveBlockerView    `json:"active_blockers,omitempty"`
	Ask                    *todoAsk                   `json:"ask,omitempty"`
	DecisionReview         *decisionReview            `json:"decision_review,omitempty"`
	Orphaned               *TodoOrphan                `json:"orphaned,omitempty"`
	View                   *assistantwire.TodoSummary `json:"view,omitempty"`
	Wait                   *assistantwire.TodoWait    `json:"wait,omitempty"`
	RepoGate               *repoGate                  `json:"repo_gate,omitempty"`
	ItemType               string                     `json:"item_type,omitempty"`
	ChildSummary           *todoChildSummary          `json:"child_summary,omitempty"`
	EffectiveDirectionID   string                     `json:"effective_direction_id,omitempty"`
	EffectiveDirectionName string                     `json:"effective_direction_name,omitempty"`
}

// askEvidenceLink returns raw when it is a valid http(s) URL and not merely the to-do's own /todos/<id> page.
func askEvidenceLink(raw, web, todoID string) string {
	u := validWebURL(raw)
	if u == "" || isSelfTodoLink(u, web, todoID) {
		return ""
	}
	return u
}

// isSelfTodoLink reports whether u points solely to todoID's own /todos/<id> page.
func isSelfTodoLink(u, web, todoID string) bool {
	if todoID == "" {
		return false
	}
	if self := todoLink(web, todoID); self != "" && strings.TrimRight(u, "/") == strings.TrimRight(self, "/") {
		return true
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	selfPath := deeplink.Todo(todoID)
	return strings.TrimRight(parsed.EscapedPath(), "/") == selfPath || strings.TrimRight(parsed.Path, "/") == "/todos/"+todoID
}

// verificationDetails takes the owner's summary and deliverable from the current verification round.
func (w awaitingAccept) verificationDetails(web, verdict string) (summary, link string) {
	if w.ask.Conclusion {
		return "", ""
	}
	summary = strings.TrimSpace(w.ask.Reason)
	var reviewedLink string
	if r := w.review; r != nil {
		summary = cmp.Or(summary, strings.TrimSpace(r.Reason), strings.TrimSpace(r.Note))
		reviewedLink = r.Link
	}
	if summary == strings.TrimSpace(verdict) {
		summary = ""
	}
	for _, raw := range []string{reviewedLink, w.ask.Link} {
		link = askEvidenceLink(raw, web, w.todo)
		if link == "" {
			continue
		}
		u, _ := url.Parse(link) // askEvidenceLink has validated it.
		path := strings.TrimRight(u.EscapedPath(), "/")
		if path == "/todos" && u.Query().Get("id") == w.todo {
			continue // An older hub linked to a task with a query parameter.
		}
		u.RawQuery, u.Fragment, u.ForceQuery = "", "", false
		target := strings.TrimRight(u.String(), "/")
		if isSelfTodoLink(target, web, w.todo) || (w.session != "" &&
			(target == sessionLink(web, w.machine, w.session, false) || (web == "" && path == deeplink.Session(w.machine, w.session)))) {
			continue
		}
		return summary, link
	}
	return summary, ""
}

// todoAsks are the open asks by to-do id, in the request's language: the ones the briefing lists (not passed, not ended,
// not from before the to-do's latest reopen).
func (s *Service) todoAsks(ctx context.Context) map[string]todoAsk {
	l, loc, web := s.botLang(ctx), s.location(ctx), s.webBase(ctx)
	out := map[string]todoAsk{}
	for _, w := range s.awaitingAcceptance(ctx) {
		a := todoAsk{Outcome: w.ask.Outcome, Verdict: verdictText(l, &w.ask, w.delivery), AskedAt: w.ask.AskedAt}
		var agents []TodoAgent
		if !w.ask.Conclusion {
			a.Summary, a.Link = w.verificationDetails(web, a.Verdict)
		} else {
			a.Link = askEvidenceLink(w.ask.Link, web, w.todo)
		}
		if decideAsk(&w.ask) {
			a.Recommend = w.ask.Recommend
			if w.ask.OutsideID == "" {
				if task, ok := s.todoNow(w.todo); ok {
					agents = task.Agents
					for _, option := range w.ask.Options {
						if target := decisionTerminal(task, option.Label, w.ask.AskedAt); target != nil {
							a.Terminal, a.TerminalOption = target, option.Label
							break
						}
					}
					if ref := w.ask.ResumeTerminal; a.Terminal == nil && ref != nil {
						if ag, bound := findActiveLinkedAgent(task, ref.MachineID, ref.SessionID); bound && !ag.LinkedAt.After(w.ask.AskedAt) {
							target := *ref
							a.Terminal = &target
						}
					}
				}
			}
			a.Question, a.Responses, a.Response = w.ask.Reason, ownerResponses, w.ask.Response
			a.ForBriefing = w.ask.ForBriefing
			a.WhyOwner, a.Options, a.CostOfWaiting = w.ask.WhyOwner, w.ask.Options, w.ask.CostOfWaiting
			a.From, a.Stands, a.Background = w.ask.From, w.ask.Stands, backgroundText(l, loc, &w.ask)
			a.Suggestion = s.askSuggestion(ctx, w.todo, &w.ask, l)
			switch {
			case !w.ask.Deadline.IsZero() && w.ask.Reversible && w.ask.DueAt.IsZero() && a.Recommend != "" && !w.ask.manualTerminalRecommendation(agents):
				d := w.ask.Deadline
				a.Deadline, a.Default = &d, defaultTaken
			case w.ask.WhyOwner != "" || !w.ask.Deadline.IsZero():
				// Never taken: it cannot be undone, others will see it, the owner asked for the background first, or its
				// default did not reach the agent.
				a.Default = defaultNever
				if d := w.ask.Deadline; !d.IsZero() && w.ask.DueAt.IsZero() {
					a.Deadline = &d
				}
			}
		}
		a.Risk = askRisk(&w.ask, agents)
		out[w.todo] = a
	}
	s.addExpiredQuestionAsks(ctx, out)
	return out
}

// askRisk is open when answering requires the owner's terminal, cannot be undone, or is visible to others.
// cannotUndo uses the raw deadline, including after the list stops showing it. The list's ask carries it and the view's decision reads it from there (todo/projection), so whatever answers a
// decision, a row or a push about it, can keep this one rule.
func askRisk(a *acceptAsk, agents []TodoAgent) string {
	if a.manualTerminalRecommendation(agents) || a.OutsideID != "" && !a.Reversible || cannotUndo(a.WhyOwner, a.Deadline, a.Reversible) {
		return assistantwire.OwnerAskRiskOpen
	}
	return assistantwire.OwnerAskRiskSafe
}

// askChanged: a to-do's ask came up, was taken or ended: the apps read the to-dos again ({"type":"todos"}) and the board
// follows.
func (s *Service) askChanged(context.Context) {
	s.trevision.changed()
	s.todosPushSoon() // with the rest of the write that changed it
	s.boardChanged()
	s.directionTreeSoon()
}

// pushDecision pushes task id's decision that waits for the owner and recommends an option (a "decide" push): what is to
// be decided and the option, in the words of its card, what the notification's OK needs to take it (the to-do, the
// round), and how its push may answer it (the view's ask and lock-screen rule). True when the hub took it. None for a
// decision without a recommendation (nothing for OK to take), for a task from a conversation of a space that redacts
// (the lock screen says nothing of those) or a muted one.
func (s *Service) pushDecision(ctx context.Context, id string) bool {
	sent, _ := s.pushDecisionErr(ctx, id)
	return sent
}

// pushDecisionErr is pushDecision that tells a push the hub did not take (the error: it may take it later) from one
// that is not due (false, nil).
func (s *Service) pushDecisionErr(ctx context.Context, id string) (bool, error) {
	return s.pushDecisionAbout(ctx, id, time.Time{})
}

// pushDecisionAbout sends the current decision, or an overdue reminder for the exact round asked at overdueRound.
// A direct to-do edit can end the decision after the sweep claimed its deadline.
func (s *Service) pushDecisionAbout(ctx context.Context, id string, overdueRound time.Time) (bool, error) {
	a, ok := s.askCopy(id)
	t, found := s.todoNow(id)
	if !ok || !found || !decideAsk(&a) || a.Recommend == "" || !a.AcceptedAt.IsZero() ||
		a.OutsideID == "" && t.Origin == "" || !a.RecheckAt.IsZero() || ownerDecisionReturned(t.Supervision) {
		return false, nil
	}
	reason := assistantwire.TodoPushDecide
	if !overdueRound.IsZero() {
		if !a.AskedAt.Equal(overdueRound) || a.DueAt.IsZero() || a.AgentReply != nil || acceptAskEnded(&a, t) || acceptAskStale(&a, t) {
			return false, nil
		}
		reason = assistantwire.TodoPushDecideOverdue
	}
	here := s.settings(ctx).MachineID
	sessionID := t.Origin
	if a.OutsideID != "" && sessionID == "" {
		sessionID = s.settings(ctx).Main // a standalone decision opens the main chat's Needs you
	}
	if s.muted(here, sessionID) || s.selfPolicy(ctx, sessionID).Redact {
		return false, nil
	}
	l := s.lang(ctx)
	n := Notification{ID: "ntf_" + randomHex(8), Kind: assistantwire.NotifyDecide, Subtitle: "Persona", DeviceID: here, SessionID: sessionID,
		Title: i18n.T(l, "assistant-reports.verdict_title", i18n.Args{"task": clip(oneLine(t.Title), 30)}),
		Body:  verdictText(l, &a, nil) + "\n" + recommendText(l, &a),
		// The level goes by why it is the owner's (decisionPushLevel): one that cannot wait interrupts even with the
		// hub's push tiering on, every time it is pushed (its reminders too); the hub keys it by to-do and round.
		Level: decisionPushLevel(a.WhyOwner),
		// The ask and how its push may answer it: the view's (projection), so a notification never offers what the
		// to-do's row would not answer in place.
		Todo: &assistantwire.NotifyTodo{ID: id, AskedAt: a.AskedAt, Recommend: a.Recommend, Responses: ownerResponses,
			Ask: projection.DecisionAskID(a.AskedAt), Lockscreen: projection.Lockscreen(askRisk(&a, t.Agents))},
		// One push a to-do and reason while it waits, not one a round (todoPushOver lets the next one ring). The hub
		// names its card (TodoAskCardID) as the push's card_id, never interaction_id: the iOS app clears a delivered
		// push whose interaction_id is not in its card list as stale, and a decision is never in that list.
		RelatedKey: assistantwire.TodoPushKey(id, reason)}
	for _, o := range a.Options {
		n.Todo.Options = append(n.Todo.Options, o.Label)
	}
	if d := a.deadlineText(l, s.location(ctx), time.Now(), t.Agents); d != "" && a.AgentReply == nil {
		n.Body += "\n" + d
	}
	// Self-contained (#3804): where it came from and stands, what each option leads to, what waiting costs.
	if lines := decisionLines(l, s.location(ctx), &a); len(lines) > 0 {
		n.Body += "\n" + strings.Join(lines, "\n")
	}
	// A decision whose words say something sensitive is still pushed (it is due), but the lock screen hears only that
	// there is news, and a tap opens it, with no answer to give from there (exitPolicy; a track that redacts is not
	// pushed, above). The recommended label stays in the payload for the app, which the lock screen does not show.
	space, _ := s.selfSpace(ctx, sessionID)
	if s.exitPolicy(ctx, n.Title+"\n"+n.Body+"\n"+strings.Join(n.Todo.Options, "\n"), sessionID, space).Redact {
		n.Title, n.Body = s.redactedNotice(ctx, l, space)
		n.Todo.Options, n.Todo.Lockscreen = nil, assistantwire.OwnerAskLockscreenOpenOnly
	}
	res, err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n})
	if err != nil {
		s.logf("task %s: decision push: %v", id, err)
		return false, err
	}
	s.noteCardResult(ctx, decisionCardSource(a.WhyOwner), n, res)
	return true, nil
}

// todoPushOver: to-do id no longer waits on the owner (its decision answered, superseded or withdrawn by its
// supervisor, its owner wait over, the to-do closed). Its card is over (resolveCard): the hub lets the next push about it
// ring (assistantwire.TodoPushKey) and takes the decision or needs-user push (which carries card_id and todo_id, never
// interaction_id) off the devices it went to (#13916, #14388). Nothing is pushed.
func (s *Service) todoPushOver(id string) {
	if id != "" {
		s.resolveCard(assistantwire.TodoAskCardID(id))
	}
}
